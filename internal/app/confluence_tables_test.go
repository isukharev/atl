package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

func TestExtractTablesRejectsProjectionWithoutNativeBody(t *testing.T) {
	store := &recordingStore{page: &domain.Resource{ID: "123", Version: 1}, omitBody: true}
	svc := &ConfluenceService{store: store}
	_, err := svc.ExtractTables(context.Background(), "123", 0)
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want check failure", err)
	}
}

func TestConfluenceTableReadsRejectUnreconciledPageIdentity(t *testing.T) {
	for _, test := range []struct {
		name string
		page *domain.Resource
	}{
		{name: "missing page"},
		{name: "missing id", page: &domain.Resource{Version: 3, Body: []byte(tableExtractCSF)}},
		{name: "foreign id", page: &domain.Resource{ID: "124", Version: 3, Body: []byte(tableExtractCSF)}},
		{name: "no version", page: &domain.Resource{ID: "123", Body: []byte(tableExtractCSF)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := &ConfluenceService{store: &recordingStore{page: test.page}}
			for _, read := range []struct {
				name string
				run  func() error
			}{
				{name: "extract", run: func() error {
					_, err := svc.ExtractTables(context.Background(), "123", 1)
					return err
				}},
				{name: "summary", run: func() error {
					_, err := svc.SummarizeTables(context.Background(), "123", 0)
					return err
				}},
			} {
				t.Run(read.name, func(t *testing.T) {
					if err := read.run(); !errors.Is(err, domain.ErrCheckFailed) {
						t.Fatalf("error = %v, want check failure", err)
					}
				})
			}
		})
	}
}

func TestConfluenceTableReadVersionBinding(t *testing.T) {
	page := &domain.Resource{ID: "123", Title: "Doc", Version: 5, Body: []byte(`<table><tbody><tr><td>A</td></tr></tbody></table>`)}
	store := &recordingStore{page: page}
	svc := &ConfluenceService{store: store}

	ungated, err := svc.ExtractTables(context.Background(), "123", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ungated.SchemaVersion != ConfluenceTableSchemaVersion || ungated.Version != 5 || ungated.PageVersionGated {
		t.Fatalf("ungated result = %+v", ungated)
	}

	gated, err := svc.SummarizeTablesWithOptions(context.Background(), "123", 0, ConfluenceTableReadOpts{ExpectedPageVersion: 5})
	if err != nil {
		t.Fatal(err)
	}
	if gated.SchemaVersion != ConfluenceTableSchemaVersion || gated.Version != 5 || !gated.PageVersionGated {
		t.Fatalf("gated result = %+v", gated)
	}

	store.page.Body = []byte(`<broken`)
	_, err = svc.ExtractTablesWithOptions(context.Background(), "123", 1, ConfluenceTableReadOpts{ExpectedPageVersion: 4})
	var mismatch *ConfluencePageVersionMismatchError
	if !errors.As(err, &mismatch) || mismatch.Expected != 4 || mismatch.Current != 5 {
		t.Fatalf("stale error = %#v, want typed 4/5 mismatch", err)
	}
}

func TestConfluenceTableReadRejectsInvalidVersionBeforeBackend(t *testing.T) {
	store := &recordingStore{page: &domain.Resource{ID: "123", Version: 5, Body: []byte(tableExtractCSF)}}
	svc := &ConfluenceService{store: store}
	_, err := svc.ExtractTablesWithOptions(context.Background(), "123", 1, ConfluenceTableReadOpts{ExpectedPageVersion: -1})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error = %v, want usage", err)
	}
	if store.getID != "" {
		t.Fatalf("invalid gate reached backend for %q", store.getID)
	}
}

const tableExtractCSF = `
<p>Intro</p>
<table>
  <tbody>
    <tr><th>Note</th><th>Item</th><th>Link</th></tr>
    <tr>
      <td rowspan="2"><span style="color: red;">Shared</span> note</td>
      <td>A</td>
      <td><a href="https://example.test/a">Alpha</a></td>
    </tr>
    <tr><td>B</td><td>Plain</td></tr>
  </tbody>
</table>
<table>
  <tbody>
    <tr><th colspan="2">Merged</th></tr>
    <tr><td>C</td><td>D</td></tr>
  </tbody>
</table>`

func TestExtractTablesFromCSFMultipleTablesAndCellMetadata(t *testing.T) {
	res, err := ExtractTablesFromCSF("123", "Doc", []byte(tableExtractCSF), 0)
	if err != nil {
		t.Fatalf("ExtractTablesFromCSF: %v", err)
	}
	if res.TableCount != 2 || res.ReturnedTableCount != 2 || !res.SelectionReconciled || len(res.Tables) != 2 {
		t.Fatalf("tables = %d/%d, want two", res.TableCount, len(res.Tables))
	}
	summary := SummarizeConfluenceTables(res)
	for i := range res.Tables {
		if res.Tables[i].Summary != summary.Tables[i] {
			t.Fatalf("table %d embedded summary = %+v, want %+v", i+1, res.Tables[i].Summary, summary.Tables[i])
		}
	}
	first := res.Tables[0]
	if first.RowCount != 3 || first.ColumnCount != 3 {
		t.Fatalf("first table shape = %dx%d, want 3x3", first.RowCount, first.ColumnCount)
	}
	if strings.Join(first.Headers, ",") != "Note,Item,Link" {
		t.Fatalf("headers = %+v", first.Headers)
	}
	origin := first.Rows[1].Cells[0]
	repeated := first.Rows[2].Cells[0]
	if origin.Row != 2 || origin.Column != 1 {
		t.Fatalf("origin coordinates = %d/%d, want 2/1", origin.Row, origin.Column)
	}
	if origin.Rowspan != 2 || origin.Repeated {
		t.Fatalf("origin = %+v, want rowspan origin", origin)
	}
	if !repeated.Repeated || repeated.SourceRow != 2 || repeated.SourceColumn != 1 || repeated.Text != origin.Text {
		t.Fatalf("repeated = %+v, origin = %+v", repeated, origin)
	}
	if origin.Styles["color"] != "red" || !strings.Contains(origin.Markdown, "<span style=\"color: red\">") {
		t.Fatalf("origin style/markdown = %+v / %q", origin.Styles, origin.Markdown)
	}
	linkCell := first.Rows[1].Cells[2]
	if len(linkCell.Links) != 1 || linkCell.Links[0].URL != "https://example.test/a" || !strings.Contains(linkCell.Markdown, "[Alpha](https://example.test/a)") {
		t.Fatalf("link cell = %+v", linkCell)
	}
	second := res.Tables[1]
	if second.Rows[0].Cells[0].Colspan != 2 || !second.Rows[0].Cells[1].Repeated {
		t.Fatalf("second header cells = %+v", second.Rows[0].Cells)
	}
}

func TestExtractTablesKeepsUnsafeColorAndLiteralHTMLInert(t *testing.T) {
	body := `<table><tbody><tr><td><span data-color="url(https://attacker.invalid/x)">&lt;img src="https://attacker.invalid/pixel"&gt;</span></td></tr></tbody></table>`
	res, err := ExtractTablesFromCSF("123", "Doc", []byte(body), 0)
	if err != nil {
		t.Fatal(err)
	}
	md := res.Tables[0].Rows[0].Cells[0].Markdown
	if !strings.Contains(md, `data-atl-color="url(https://attacker.invalid/x)"`) ||
		!strings.Contains(md, `&lt;img src=`) || strings.Contains(md, `<span style=`) || strings.Contains(md, `<img src=`) {
		t.Fatalf("unsafe table color/html became active: %s", md)
	}
}

func TestExtractTablesFromCSFSelectsOneTable(t *testing.T) {
	res, err := ExtractTablesFromCSF("123", "Doc", []byte(tableExtractCSF), 2)
	if err != nil {
		t.Fatalf("ExtractTablesFromCSF: %v", err)
	}
	if res.Table != 2 || res.TableCount != 2 || res.ReturnedTableCount != 1 || !res.SelectionReconciled || len(res.Tables) != 1 || res.Tables[0].Index != 2 {
		t.Fatalf("selection = %+v", res)
	}
	if res.Tables[0].Summary.Index != 2 || !res.Tables[0].Summary.CellCountReconciled {
		t.Fatalf("selected table summary = %+v", res.Tables[0].Summary)
	}
}

func TestExtractTablesFromCSFTypesOutOfRangeSelectionWithoutContent(t *testing.T) {
	const secret = "SYNTHETIC-TABLE-SECRET"
	body := `<table><tbody><tr><td><a href="https://backend.invalid/` + secret + `">` + secret + `</a></td></tr></tbody></table>`
	_, err := ExtractTablesFromCSF("PAGE-"+secret, "Title "+secret, []byte(body), 4)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not-found sentinel", err)
	}
	var selection *ConfluenceTableSelectionError
	if !errors.As(err, &selection) {
		t.Fatalf("error = %#v, want *ConfluenceTableSelectionError", err)
	}
	if selection.Requested != 4 || selection.Available != 1 {
		t.Fatalf("selection = %+v, want requested 4 of 1", selection)
	}
	typ := reflect.TypeOf(*selection)
	if typ.NumField() != 2 {
		t.Fatalf("selection error carries %d fields, want only the two counters", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() != reflect.Int {
			t.Fatalf("field %s is %s, want an int counter", typ.Field(i).Name, typ.Field(i).Type)
		}
	}
	for _, forbidden := range []string{secret, "https://", "backend.invalid"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("selection error leaked %q: %s", forbidden, err.Error())
		}
	}
}

func TestExtractTablesFromCSFKeepsUsageAndParseErrorsUntyped(t *testing.T) {
	var selection *ConfluenceTableSelectionError
	_, err := ExtractTablesFromCSF("123", "Doc", []byte(tableExtractCSF), -1)
	if !errors.Is(err, domain.ErrUsage) || errors.As(err, &selection) {
		t.Fatalf("negative selection error = %v", err)
	}
	_, err = ExtractTablesFromCSF("123", "Doc", []byte("<table>"), 1)
	if err == nil || errors.As(err, &selection) {
		t.Fatalf("parse error = %v", err)
	}
	res, err := ExtractTablesFromCSF("123", "Doc", []byte(tableExtractCSF), 2)
	if err != nil || res.Table != 2 {
		t.Fatalf("in-range selection = %+v, err = %v", res, err)
	}
}

func TestExtractTablesEmitsDurableCellKindWithoutInternalMarker(t *testing.T) {
	// The cell kind must be readable from the JSON alone, so ConfluenceTableCell
	// may not keep any unexported provenance the wire format cannot carry.
	typ := reflect.TypeOf(ConfluenceTableCell{})
	for i := 0; i < typ.NumField(); i++ {
		if !typ.Field(i).IsExported() {
			t.Fatalf("cell field %s is unexported, so its kind cannot survive JSON", typ.Field(i).Name)
		}
	}
	res, err := ExtractTablesFromCSF("123", "Doc", []byte(`<table><tbody><tr><td rowspan="2">A</td><td>B</td></tr><tr><td>C</td></tr></tbody></table>`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.SchemaVersion != ConfluenceTableSchemaVersion {
		t.Fatalf("schema version = %d, want %d", res.SchemaVersion, ConfluenceTableSchemaVersion)
	}
	origin := res.Tables[0].Rows[0].Cells[0]
	if origin.Repeated || origin.SourceRow != origin.Row || origin.SourceColumn != origin.Column ||
		classifyConfluenceTableCell(origin) != confluenceTableOriginCell {
		t.Fatalf("origin cell = %+v, want self-naming source coordinates", origin)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"origin"`)) {
		t.Fatalf("internal origin marker leaked into extraction JSON: %s", data)
	}
	if !bytes.Contains(data, []byte(`"text":"B","markdown":"B","source_row":1,"source_column":2`)) {
		t.Fatalf("origin source coordinates missing from extraction JSON: %s", data)
	}
}

func TestClassifyConfluenceTableCellRejectsWrongFieldCombinations(t *testing.T) {
	for _, test := range []struct {
		name string
		cell ConfluenceTableCell
		want confluenceTableCellKind
	}{
		{name: "origin", cell: ConfluenceTableCell{Row: 2, Column: 3, SourceRow: 2, SourceColumn: 3, Text: "A"}, want: confluenceTableOriginCell},
		{name: "repeated", cell: ConfluenceTableCell{Row: 2, Column: 3, Repeated: true, SourceRow: 1, SourceColumn: 3}, want: confluenceTableRepeatedCell},
		{name: "synthetic padding", cell: ConfluenceTableCell{Row: 2, Column: 3}, want: confluenceTableSyntheticCell},
		{name: "origin without source coordinates", cell: ConfluenceTableCell{Row: 2, Column: 3, Text: "A"}},
		{name: "origin naming another cell", cell: ConfluenceTableCell{Row: 2, Column: 3, SourceRow: 1, SourceColumn: 3}},
		{name: "repeated without source coordinates", cell: ConfluenceTableCell{Row: 2, Column: 3, Repeated: true}},
		{name: "repeated naming itself", cell: ConfluenceTableCell{Row: 2, Column: 3, Repeated: true, SourceRow: 2, SourceColumn: 3}},
		{name: "repeated naming a later cell", cell: ConfluenceTableCell{Row: 2, Column: 3, Repeated: true, SourceRow: 3, SourceColumn: 3}},
		{name: "padding carrying content", cell: ConfluenceTableCell{Row: 2, Column: 3, Text: "A"}},
		{name: "padding carrying a span", cell: ConfluenceTableCell{Row: 2, Column: 3, Rowspan: 2}},
		{name: "padding claiming header", cell: ConfluenceTableCell{Row: 2, Column: 3, Header: true}},
		{name: "unplaced cell", cell: ConfluenceTableCell{SourceRow: 1, SourceColumn: 1}},
		{name: "negative source", cell: ConfluenceTableCell{Row: 2, Column: 3, SourceRow: -2, SourceColumn: -3}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyConfluenceTableCell(test.cell); got != test.want {
				t.Fatalf("kind = %d, want %d for %+v", got, test.want, test.cell)
			}
		})
	}
}

func TestSummarizeConfluenceTablesCountsExpandedStructureWithoutContent(t *testing.T) {
	extract, err := ExtractTablesFromCSF("123", "Secret title", []byte(tableExtractCSF), 0)
	if err != nil {
		t.Fatal(err)
	}
	res := SummarizeConfluenceTables(extract)
	if res.PageID != "123" || res.TableCount != 2 || res.Table != 0 || res.ReturnedTableCount != 2 || !res.SelectionReconciled || len(res.Tables) != 2 {
		t.Fatalf("summary metadata = %+v", res)
	}
	want := []ConfluenceTableSummaryRecord{
		{Index: 1, RowCount: 3, ColumnCount: 3, Rectangular: true, HeaderRowCount: 1, HeaderCellCount: 3, ExpandedCellCount: 9, OriginCellCount: 8, RepeatedCellCount: 1, CellCountReconciled: true, NonemptyTextCellCount: 9, NonemptyMarkdownCellCount: 9, NonemptyRawCellCount: 2, StyledCellCount: 2, StyleEntryCount: 2, DistinctStyleMarkerCount: 1, LinkedCellCount: 1, RowspanMetadataCellCount: 2, RowspanSourceCellCount: 1, RowspanCoveredCellCount: 1},
		{Index: 2, RowCount: 2, ColumnCount: 2, Rectangular: true, HeaderRowCount: 1, HeaderCellCount: 2, ExpandedCellCount: 4, OriginCellCount: 3, RepeatedCellCount: 1, CellCountReconciled: true, NonemptyTextCellCount: 4, NonemptyMarkdownCellCount: 4, NonemptyRawCellCount: 2, ColspanMetadataCellCount: 2, ColspanSourceCellCount: 1, ColspanCoveredCellCount: 1},
	}
	if !reflect.DeepEqual(res.Tables, want) {
		t.Fatalf("summaries = %#v, want %#v", res.Tables, want)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Secret title", "Shared", "https://example.test", "color", "rowspan\""} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("content-bearing %q leaked in %s", forbidden, data)
		}
	}
}

func TestSummarizeConfluenceTablesRefusesOverlappingSourceSpans(t *testing.T) {
	// Row 1 places A at column 1 and B — rowspan 2 — at column 2, so B claims
	// (2,2). Row 2's only source cell C has colspan 2, lands at the first
	// unclaimed column 1, and therefore also covers (2,2). The expanded grid
	// still looks like a tidy 2x2, so only the independent placement ledger can
	// tell that the source markup is inconsistent.
	const body = `<table><tbody><tr><td>A</td><td rowspan="2">B</td></tr><tr><td colspan="2">C</td></tr></tbody></table>`
	extract, err := ExtractTablesFromCSF("123", "Doc", []byte(body), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := SummarizeConfluenceTables(extract).Tables[0]
	if !got.Rectangular || got.ExpandedCellCount != got.RowCount*got.ColumnCount {
		t.Fatalf("overlap case is not count-clean, ledger would be untested: %+v", got)
	}
	if got.CellCountReconciled {
		t.Fatalf("overlapping source spans reconciled: %+v", got)
	}
	if extract.Tables[0].Summary.CellCountReconciled {
		t.Fatalf("attached summary reconciled: %+v", extract.Tables[0].Summary)
	}
}

func TestSummarizeConfluenceTablesRefusesCoverageOutsideSourceRows(t *testing.T) {
	// A rowspan of 3 over a single source row claims (2,1) and (3,1), which lie
	// outside the source row domain entirely.
	const body = `<table><tbody><tr><td rowspan="3">A</td><td>B</td></tr></tbody></table>`
	extract, err := ExtractTablesFromCSF("123", "Doc", []byte(body), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := SummarizeConfluenceTables(extract).Tables[0]
	if !got.Rectangular || got.ExpandedCellCount != got.RowCount*got.ColumnCount {
		t.Fatalf("out-of-domain case is not count-clean, ledger would be untested: %+v", got)
	}
	if got.CellCountReconciled {
		t.Fatalf("rowspan past the last source row reconciled: %+v", got)
	}
}

func TestReconcileConfluenceTableSourceDetectsEmittedGridDisagreement(t *testing.T) {
	const body = `<table><tbody><tr><td rowspan="2">Shared</td><td>A</td></tr><tr><td>B</td></tr></tbody></table>`
	root, err := csf.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	node := topLevelTables(root)[0]
	rows := tableRows(node)
	grid := extractTable(1, node)
	if !grid.sourcePlacementChecked || !grid.sourcePlacementReconciled || !reconcileConfluenceTableSource(rows, grid) {
		t.Fatalf("valid rowspan table did not reconcile: %+v", grid)
	}
	for _, test := range []struct {
		name    string
		corrupt func(*ConfluenceTable)
	}{
		{name: "repeated cell names the wrong source", corrupt: func(g *ConfluenceTable) {
			g.Rows[1].Cells[0].SourceColumn = 2
		}},
		{name: "covered coordinate claims to be an origin", corrupt: func(g *ConfluenceTable) {
			g.Rows[1].Cells[0].Repeated = false
			g.Rows[1].Cells[0].SourceRow = g.Rows[1].Cells[0].Row
			g.Rows[1].Cells[0].SourceColumn = g.Rows[1].Cells[0].Column
		}},
		{name: "claimed coordinate emitted as synthetic padding", corrupt: func(g *ConfluenceTable) {
			g.Rows[1].Cells[0] = emptyTableCell(1, 0)
		}},
		{name: "claim left unrealized", corrupt: func(g *ConfluenceTable) {
			g.Rows[1].Cells = g.Rows[1].Cells[1:]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tampered := grid
			tampered.Rows = make([]ConfluenceTableRow, len(grid.Rows))
			for i, row := range grid.Rows {
				row.Cells = append([]ConfluenceTableCell(nil), row.Cells...)
				tampered.Rows[i] = row
			}
			test.corrupt(&tampered)
			if reconcileConfluenceTableSource(rows, tampered) {
				t.Fatalf("tampered grid reconciled against the source ledger")
			}
		})
	}
}

func TestSummarizeConfluenceTablesKeepsDOMLedgerAsAnIndependentWitness(t *testing.T) {
	// A grid that reads as four plain origins is perfectly self-consistent under
	// the durable cell contract, so only the DOM ledger can tell that the markup
	// declared a rowspan covering (2,1). An expansion bug of that shape must not
	// reconcile just because the grid it produced agrees with itself.
	const body = `<table><tbody><tr><td rowspan="2">Shared</td><td>A</td></tr><tr><td>B</td></tr></tbody></table>`
	root, err := csf.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	node := topLevelTables(root)[0]
	grid := extractTable(1, node)
	grid.Rows[0].Cells[0].Rowspan = 0
	covered := &grid.Rows[1].Cells[0]
	covered.Repeated = false
	covered.Rowspan = 0
	covered.SourceRow, covered.SourceColumn = covered.Row, covered.Column
	if !reconcileConfluenceTableCells(grid) {
		t.Fatalf("durable cell ledger rejected the grid, so the DOM ledger would be untested")
	}
	grid.sourcePlacementReconciled = reconcileConfluenceTableSource(tableRows(node), grid)
	if got := summarizeConfluenceTable(grid, ConfluenceTableSchemaVersion); got.CellCountReconciled {
		t.Fatalf("grid disagreeing with its own markup reconciled: %+v", got)
	}
}

func TestSummarizeConfluenceTablesSurvivesJSONRoundTrip(t *testing.T) {
	const raggedOverlap = `<table><tbody><tr><td>A</td><td rowspan="2">B</td></tr><tr><td colspan="2">C</td></tr></tbody></table>`
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "spans and padding", body: tableExtractCSF},
		{name: "combined span", body: `<table><tbody><tr><td rowspan="2" colspan="2">S</td><td>A</td></tr><tr><td>B</td></tr></tbody></table>`},
		{name: "ragged padding", body: `<table><tbody><tr><td>A</td><td>B</td></tr><tr><td>C</td></tr></tbody></table>`},
		{name: "unreconciled overlap", body: raggedOverlap},
	} {
		t.Run(test.name, func(t *testing.T) {
			extract, err := ExtractTablesFromCSF("123", "Doc", []byte(test.body), 0)
			if err != nil {
				t.Fatal(err)
			}
			want := SummarizeConfluenceTables(extract)
			data, err := json.Marshal(extract)
			if err != nil {
				t.Fatal(err)
			}
			var decoded ConfluenceTableExtract
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			// The DOM placement ledger is in-process only, so the decoded
			// extract must reproduce the summary — reconciled or not — by
			// recomputing it from the durable cell contract.
			for i, table := range decoded.Tables {
				if table.sourcePlacementChecked || table.sourcePlacementReconciled {
					t.Fatalf("table %d carried in-process provenance across JSON", i+1)
				}
			}
			got := SummarizeConfluenceTables(&decoded)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("round-tripped summary = %#v, want %#v", got, want)
			}
			for i, table := range decoded.Tables {
				if got.Tables[i] != table.Summary {
					t.Fatalf("table %d summary = %+v, want attached %+v", i+1, got.Tables[i], table.Summary)
				}
			}
		})
	}
}

func TestSummarizeConfluenceTablesNeverUpgradesAmbiguousDeserializedData(t *testing.T) {
	extract, err := ExtractTablesFromCSF("123", "Doc", []byte(`<table><tbody><tr><td rowspan="2">A</td><td>B</td></tr><tr><td>C</td></tr></tbody></table>`), 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(extract)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		tamper func(*ConfluenceTableExtract)
	}{
		{name: "no durable summary", tamper: func(e *ConfluenceTableExtract) {
			e.Tables[0].Summary = ConfluenceTableSummaryRecord{}
		}},
		{name: "summary disagrees with the grid", tamper: func(e *ConfluenceTableExtract) {
			e.Tables[0].Summary.HeaderCellCount++
		}},
		{name: "buckets do not add up", tamper: func(e *ConfluenceTableExtract) {
			e.Tables[0].Summary.OriginCellCount++
		}},
		{name: "repeated count contradicts the cells", tamper: func(e *ConfluenceTableExtract) {
			e.Tables[0].Summary.RepeatedCellCount++
			e.Tables[0].Summary.OriginCellCount--
		}},
		{name: "reconciled claimed over a non-rectangular grid", tamper: func(e *ConfluenceTableExtract) {
			e.Tables[0].Rows[1].Cells = e.Tables[0].Rows[1].Cells[:1]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var decoded ConfluenceTableExtract
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			test.tamper(&decoded)
			if got := SummarizeConfluenceTables(&decoded).Tables[0]; got.CellCountReconciled {
				t.Fatalf("ambiguous deserialized table reconciled: %+v", got)
			}
		})
	}
}

// roundTripConfluenceTableExtract returns an extract as a consumer receives it:
// through JSON, with no in-process provenance left.
func roundTripConfluenceTableExtract(t *testing.T, body string) *ConfluenceTableExtract {
	t.Helper()
	extract, err := ExtractTablesFromCSF("123", "Doc", []byte(body), 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(extract)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ConfluenceTableExtract
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return &decoded
}

// forgeReconciledConfluenceTableSummaries attaches the summary a caller would
// have to forge to claim reconciliation: the recomputation of the tables exactly
// as they are, with the verdict flipped to true. It isolates the durable cell
// ledger, because the summary cross-check can then no longer explain a refusal.
func forgeReconciledConfluenceTableSummaries(extract *ConfluenceTableExtract) {
	records := SummarizeConfluenceTables(extract).Tables
	for i := range extract.Tables {
		record := records[i]
		record.CellCountReconciled = true
		extract.Tables[i].Summary = record
	}
}

func TestSummarizeConfluenceTablesRefusesForgedSummaryOverLegacyCells(t *testing.T) {
	// A pre-contract payload carries no source coordinates on its origin cells.
	// A content-bearing one is refused by the cell contract itself; an all-empty
	// one is genuinely indistinguishable from synthetic padding, so the schema
	// version is what refuses it. Neither may be upgraded by attaching a summary
	// that claims reconciliation.
	for _, test := range []struct {
		name          string
		body          string
		schemaVersion int
	}{
		{
			name:          "content-bearing legacy cells",
			body:          `<table><tbody><tr><td rowspan="2">A</td><td>B</td></tr><tr><td>C</td></tr></tbody></table>`,
			schemaVersion: ConfluenceTableSchemaVersion,
		},
		{
			name:          "empty legacy cells on the previous schema version",
			body:          `<table><tbody><tr><td></td><td></td></tr></tbody></table>`,
			schemaVersion: ConfluenceTableSchemaVersion - 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoded := roundTripConfluenceTableExtract(t, test.body)
			decoded.SchemaVersion = test.schemaVersion
			for _, row := range decoded.Tables[0].Rows {
				for i := range row.Cells {
					if cell := &row.Cells[i]; !cell.Repeated {
						cell.SourceRow, cell.SourceColumn = 0, 0
					}
				}
			}
			forgeReconciledConfluenceTableSummaries(decoded)
			if got := SummarizeConfluenceTables(decoded).Tables[0]; got.CellCountReconciled {
				t.Fatalf("legacy cells reconciled behind a forged summary: %+v", got)
			}
		})
	}
}

func TestSummarizeConfluenceTablesRecomputesSpanClaimsFromDurableCells(t *testing.T) {
	// Row 1 declares a 2x2 span plus two plain cells; row 2 supplies one cell and
	// is padded out. The decoded grid therefore holds all three cell kinds, and
	// every mutation below carries a forged reconciled summary, so only the
	// recomputed span ledger can refuse it.
	const body = `<table><tbody><tr><td rowspan="2" colspan="2">S</td><td>A</td><td>Z</td></tr><tr><td>B</td></tr></tbody></table>`
	control := roundTripConfluenceTableExtract(t, body)
	got := SummarizeConfluenceTables(control).Tables[0]
	if got.RowCount != 2 || got.ColumnCount != 4 || got.OriginCellCount != 4 ||
		got.RepeatedCellCount != 3 || got.SyntheticEmptyCellCount != 1 || !got.CellCountReconciled {
		t.Fatalf("control summary = %+v, want a reconciled mixed-kind grid", got)
	}
	for _, test := range []struct {
		name   string
		tamper func(*ConfluenceTable)
	}{
		{name: "origin loses its source coordinates", tamper: func(tbl *ConfluenceTable) {
			tbl.Rows[0].Cells[0].SourceRow, tbl.Rows[0].Cells[0].SourceColumn = 0, 0
		}},
		{name: "origin names another cell", tamper: func(tbl *ConfluenceTable) {
			tbl.Rows[0].Cells[0].SourceColumn = 2
		}},
		{name: "repeated names a covered coordinate instead of its origin", tamper: func(tbl *ConfluenceTable) {
			tbl.Rows[1].Cells[1].SourceRow, tbl.Rows[1].Cells[1].SourceColumn = 2, 1
		}},
		{name: "repeated does not echo the origin spans", tamper: func(tbl *ConfluenceTable) {
			tbl.Rows[1].Cells[0].Rowspan = 0
		}},
		{name: "synthetic padding sits on a claimed coordinate", tamper: func(tbl *ConfluenceTable) {
			tbl.Rows[1].Cells[1] = emptyTableCell(1, 1)
		}},
		{name: "origin span runs past the grid", tamper: func(tbl *ConfluenceTable) {
			tbl.Rows[0].Cells[3].Rowspan = 3
		}},
		{name: "origin rectangles overlap", tamper: func(tbl *ConfluenceTable) {
			tbl.Rows[0].Cells[2].Colspan = 2
		}},
		{name: "cell coordinate disagrees with its position", tamper: func(tbl *ConfluenceTable) {
			tbl.Rows[0].Cells[1].Row = 2
		}},
		{name: "row index disagrees with its position", tamper: func(tbl *ConfluenceTable) {
			tbl.Rows[0].Index = 2
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoded := roundTripConfluenceTableExtract(t, body)
			test.tamper(&decoded.Tables[0])
			forgeReconciledConfluenceTableSummaries(decoded)
			if got := SummarizeConfluenceTables(decoded).Tables[0]; got.CellCountReconciled {
				t.Fatalf("tampered durable grid reconciled: %+v", got)
			}
		})
	}
}

func TestSummarizeConfluenceTablesNil(t *testing.T) {
	if got := SummarizeConfluenceTables(nil); got != nil {
		t.Fatalf("summary = %#v, want nil", got)
	}
}

func TestSummarizeConfluenceTablesClassifiesCombinedSpanCoordinates(t *testing.T) {
	const body = `<table><tbody><tr><td rowspan="2" colspan="2">Shared</td><td>A</td></tr><tr><td>B</td></tr></tbody></table>`
	extract, err := ExtractTablesFromCSF("123", "Doc", []byte(body), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := SummarizeConfluenceTables(extract).Tables[0]
	if got.RowCount != 2 || got.ColumnCount != 3 || got.ExpandedCellCount != 6 || got.RepeatedCellCount != 3 ||
		got.OriginCellCount != 3 || got.SyntheticEmptyCellCount != 0 || !got.Rectangular || !got.CellCountReconciled ||
		got.RowspanMetadataCellCount != 4 || got.ColspanMetadataCellCount != 4 || got.NonemptyRawCellCount != 4 ||
		got.RowspanSourceCellCount != 1 || got.RowspanCoveredCellCount != 2 ||
		got.ColspanSourceCellCount != 1 || got.ColspanCoveredCellCount != 2 {
		t.Fatalf("combined-span summary = %+v", got)
	}
}

func TestSummarizeConfluenceTablesDistinguishesOriginsAndSyntheticPadding(t *testing.T) {
	const body = `<table><tbody><tr><td>A</td><td>B</td></tr><tr><td>C</td></tr></tbody></table>`
	extract, err := ExtractTablesFromCSF("123", "Doc", []byte(body), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := SummarizeConfluenceTables(extract).Tables[0]
	if got.ExpandedCellCount != 4 || got.OriginCellCount != 3 || got.RepeatedCellCount != 0 || got.SyntheticEmptyCellCount != 1 || !got.Rectangular || !got.CellCountReconciled {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeConfluenceTablesCountsStyleEntriesAndDistinctMarkers(t *testing.T) {
	extract := &ConfluenceTableExtract{TableCount: 1, Tables: []ConfluenceTable{{
		Index: 1, RowCount: 1, ColumnCount: 3, Rows: []ConfluenceTableRow{{Cells: []ConfluenceTableCell{
			{Row: 1, Column: 1, SourceRow: 1, SourceColumn: 1, Styles: map[string]string{"color": "red", "background": "blue"}},
			{Row: 1, Column: 2, SourceRow: 1, SourceColumn: 2, Styles: map[string]string{"color": "red"}},
			{Row: 1, Column: 3, SourceRow: 1, SourceColumn: 3, Styles: map[string]string{"color": "green"}},
		}}},
	}}}
	got := SummarizeConfluenceTables(extract).Tables[0]
	if got.StyledCellCount != 3 || got.StyleEntryCount != 4 || got.DistinctStyleMarkerCount != 3 {
		t.Fatalf("summary=%+v", got)
	}
}

func TestSummarizeConfluenceTablesDetectsRaggedInputAndSelectionMismatch(t *testing.T) {
	extract := &ConfluenceTableExtract{TableCount: 2, Table: 2, Tables: []ConfluenceTable{{
		Index: 1, RowCount: 2, ColumnCount: 2, Rows: []ConfluenceTableRow{
			{Cells: []ConfluenceTableCell{
				{Row: 1, Column: 1, SourceRow: 1, SourceColumn: 1},
				{Row: 1, Column: 2, SourceRow: 1, SourceColumn: 2},
			}},
			{Cells: []ConfluenceTableCell{{Row: 2, Column: 1, SourceRow: 2, SourceColumn: 1}}},
		},
	}}}
	got := SummarizeConfluenceTables(extract)
	if got.SelectionReconciled || got.ReturnedTableCount != 1 || got.Tables[0].Rectangular || got.Tables[0].CellCountReconciled {
		t.Fatalf("summary=%+v", got)
	}
}

func TestRenderConfluenceTableCSV(t *testing.T) {
	res, err := ExtractTablesFromCSF("123", "Doc", []byte(tableExtractCSF), 2)
	if err != nil {
		t.Fatalf("ExtractTablesFromCSF: %v", err)
	}
	data, err := RenderConfluenceTableCSV(res)
	if err != nil {
		t.Fatalf("RenderConfluenceTableCSV: %v", err)
	}
	records, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatalf("read csv %q: %v", data, err)
	}
	if len(records) != 2 || strings.Join(records[0], ",") != "Merged,Merged" || strings.Join(records[1], ",") != "C,D" {
		t.Fatalf("records = %#v", records)
	}

	all, err := ExtractTablesFromCSF("123", "Doc", []byte(tableExtractCSF), 0)
	if err != nil {
		t.Fatalf("ExtractTablesFromCSF(all): %v", err)
	}
	data, err = RenderConfluenceTableCSV(all)
	if err != nil {
		t.Fatalf("RenderConfluenceTableCSV(all): %v", err)
	}
	records, err = csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatalf("read all-table csv %q: %v", data, err)
	}
	if len(records) < 3 || strings.Join(records[0], ",") != "table,row,column,text,markdown,links,styles,repeated,source_row,source_column" {
		t.Fatalf("all-table csv missing cell metadata:\n%s", data)
	}
	// Schema v2 makes native origins self-naming in the durable source columns.
	if records[1][7] != "false" || records[1][8] != "1" || records[1][9] != "1" {
		t.Fatalf("all-table csv origin provenance = %#v", records[1])
	}
}

func TestRenderConfluenceTableCSVNeutralizesFormulasUnlessRaw(t *testing.T) {
	res := &ConfluenceTableExtract{Table: 1, Tables: []ConfluenceTable{{
		ColumnCount: 1,
		Headers:     []string{"=Header"},
		Rows: []ConfluenceTableRow{
			{Cells: []ConfluenceTableCell{{Markdown: "=Header"}}},
			{Cells: []ConfluenceTableCell{{Markdown: "+cmd"}}},
		},
	}}}
	safe, err := RenderConfluenceTableCSV(res)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(safe)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if records[0][0] != "'=Header" || records[1][0] != "'+cmd" {
		t.Fatalf("safe records = %#v", records)
	}
	raw, err := RenderConfluenceTableCSVWithOptions(res, true)
	if err != nil {
		t.Fatal(err)
	}
	records, _ = csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if records[0][0] != "=Header" || records[1][0] != "+cmd" {
		t.Fatalf("raw records = %#v", records)
	}
}

func TestWriteConfluenceTableXLSX(t *testing.T) {
	res, err := ExtractTablesFromCSF("123", "Doc", []byte(tableExtractCSF), 0)
	if err != nil {
		t.Fatalf("ExtractTablesFromCSF: %v", err)
	}
	path := t.TempDir() + "/tables.xlsx"
	if err := WriteConfluenceTableXLSX(path, res); err != nil {
		t.Fatalf("WriteConfluenceTableXLSX: %v", err)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	defer zr.Close()
	foundWorkbook := false
	foundSecondSheet := false
	for _, f := range zr.File {
		switch f.Name {
		case "xl/workbook.xml":
			foundWorkbook = true
		case "xl/worksheets/sheet2.xml":
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open sheet2: %v", err)
			}
			body, _ := io.ReadAll(rc)
			_ = rc.Close()
			foundSecondSheet = strings.Contains(string(body), "Merged")
		}
	}
	if !foundWorkbook || !foundSecondSheet {
		t.Fatalf("xlsx missing workbook/sheet2 content")
	}
}
