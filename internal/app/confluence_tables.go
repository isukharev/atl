package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// ConfluenceTableSchemaVersion gates the table extract/summary JSON shape. It
// moves to 3 for the compact cell contract: a native origin is now the unmarked
// default and carries no source coordinates at all, so origin-heavy tables — the
// common case — no longer pay a source_row/source_column pair on every cell.
// Provenance stays durable because the two non-default kinds mark themselves: a
// repeated cell keeps repeated:true plus the covering origin's coordinates, and a
// synthetic pad cell emits synthetic:true (see ConfluenceTableCell). Schema 2
// origins named themselves (source_row == row) and schema 1 predates the cell
// contract entirely; both read as invalid cells under the compact classifier, so
// neither is eligible for reconciliation on the deserialized path even if its
// schema_version field is relabelled.
const ConfluenceTableSchemaVersion = 3

// ConfluenceTableCellContract is the stable, non-empty marker for the compact
// cell-kind contract. Every extract stamps it at top level; deserialized
// reconciliation requires it to match exactly, so a payload cannot claim the
// current contract by only relabelling schema_version. Its value must never
// change without a schema bump.
const ConfluenceTableCellContract = "confluence-table-cells/compact-v3"

// ConfluenceTableExtract is a structured, read-only view of tables on a page.
type ConfluenceTableExtract struct {
	SchemaVersion       int               `json:"schema_version"`
	CellContract        string            `json:"cell_contract"`
	PageID              string            `json:"page_id"`
	Title               string            `json:"title,omitempty"`
	Version             int               `json:"version"`
	PageVersionGated    bool              `json:"page_version_gated"`
	TableCount          int               `json:"table_count"`
	Table               int               `json:"selected_table,omitempty"`
	ReturnedTableCount  int               `json:"returned_table_count"`
	SelectionReconciled bool              `json:"selection_reconciled"`
	Tables              []ConfluenceTable `json:"tables"`
}

// ConfluenceTableSummary is a bounded, content-free structural inventory of
// tables on a page. It deliberately excludes page and cell content.
type ConfluenceTableSummary struct {
	SchemaVersion       int                            `json:"schema_version"`
	CellContract        string                         `json:"cell_contract"`
	PageID              string                         `json:"page_id"`
	Version             int                            `json:"version"`
	PageVersionGated    bool                           `json:"page_version_gated"`
	TableCount          int                            `json:"table_count"`
	Table               int                            `json:"selected_table,omitempty"`
	ReturnedTableCount  int                            `json:"returned_table_count"`
	SelectionReconciled bool                           `json:"selection_reconciled"`
	Tables              []ConfluenceTableSummaryRecord `json:"tables"`
}

// ConfluenceTableReadOpts optionally binds a table read to a page revision the
// caller already observed. Zero leaves a direct externally fixed selection
// explicitly ungated; a positive value refuses a read after the page moves.
type ConfluenceTableReadOpts struct {
	ExpectedPageVersion int
}

// ConfluenceTableSummaryRecord describes one expanded table without exposing
// cell text, links, style values, raw attributes, or warning text.
type ConfluenceTableSummaryRecord struct {
	Index                     int  `json:"index"`
	RowCount                  int  `json:"row_count"`
	ColumnCount               int  `json:"column_count"`
	Rectangular               bool `json:"rectangular"`
	HeaderRowCount            int  `json:"header_row_count"`
	HeaderCellCount           int  `json:"header_cell_count"`
	ExpandedCellCount         int  `json:"expanded_cell_count"`
	OriginCellCount           int  `json:"origin_cell_count"`
	RepeatedCellCount         int  `json:"repeated_cell_count"`
	SyntheticEmptyCellCount   int  `json:"synthetic_empty_cell_count"`
	CellCountReconciled       bool `json:"cell_count_reconciled"`
	NonemptyTextCellCount     int  `json:"nonempty_text_cell_count"`
	NonemptyMarkdownCellCount int  `json:"nonempty_markdown_cell_count"`
	NonemptyRawCellCount      int  `json:"nonempty_raw_cell_count"`
	StyledCellCount           int  `json:"styled_cell_count"`
	StyleEntryCount           int  `json:"style_entry_count"`
	DistinctStyleMarkerCount  int  `json:"distinct_style_marker_count"`
	LinkedCellCount           int  `json:"linked_cell_count"`
	RowspanMetadataCellCount  int  `json:"rowspan_metadata_cell_count"`
	RowspanSourceCellCount    int  `json:"rowspan_source_cell_count"`
	RowspanCoveredCellCount   int  `json:"rowspan_covered_cell_count"`
	ColspanMetadataCellCount  int  `json:"colspan_metadata_cell_count"`
	ColspanSourceCellCount    int  `json:"colspan_source_cell_count"`
	ColspanCoveredCellCount   int  `json:"colspan_covered_cell_count"`
	WarningCount              int  `json:"warning_count"`
}

// ConfluenceTable is one expanded table. Index is 1-based in document order.
//
// sourcePlacementChecked/sourcePlacementReconciled carry the verdict of the
// independent DOM source-placement ledger (see reconcileConfluenceTableSource).
// They are unexported on purpose: they are in-process provenance that compares
// the emitted grid against markup no consumer receives. A deserialized table has
// neither; it is reconciled from the durable cell contract instead (see
// reconcileConfluenceTableCells), cross-checked against Summary.
type ConfluenceTable struct {
	Index       int                          `json:"index"`
	RowCount    int                          `json:"row_count"`
	ColumnCount int                          `json:"column_count"`
	Summary     ConfluenceTableSummaryRecord `json:"summary"`
	Headers     []string                     `json:"headers,omitempty"`
	Rows        []ConfluenceTableRow         `json:"rows"`
	Warnings    []string                     `json:"warnings,omitempty"`
	Metadata    map[string]map[string]any    `json:"metadata,omitempty"`

	sourcePlacementChecked    bool
	sourcePlacementReconciled bool
}

// ConfluenceTableRow is one expanded row.
type ConfluenceTableRow struct {
	Index  int                   `json:"index"`
	Header bool                  `json:"header,omitempty"`
	Cells  []ConfluenceTableCell `json:"cells"`
}

// ConfluenceTableCell is one expanded cell. Its kind is durable and explicit;
// classifyConfluenceTableCell is the closed reading of the field combination:
//
//   - origin — the cell the markup actually declares, and the compact default:
//     Repeated false, Synthetic false, and zero source coordinates. It may carry
//     content, spans, or nothing at all (a valid empty native cell).
//   - repeated — a coordinate covered by a rowspan/colspan: Repeated true,
//     Synthetic false, and SourceRow/SourceColumn naming the covering origin,
//     never the cell's own coordinate and never one after it in the grid.
//   - synthetic padding — a coordinate the markup left unfilled: Synthetic true,
//     Repeated false, zero source coordinates, and no content, metadata, or spans.
//
// Every other combination — a marked cell that also carries source coordinates, a
// native origin that names a coordinate (the schema-2 self-naming shape), or both
// markers at once — is invalid and can never reconcile. Because the kind survives
// serialization, a deserialized cell carries the same provenance a freshly
// extracted one does; the compact form simply spends bytes only on the two kinds
// that are not the default.
type ConfluenceTableCell struct {
	Row          int                   `json:"row"`
	Column       int                   `json:"column"`
	Text         string                `json:"text" jsonschema:"whitespace-normalized plain text; use for exact plain-text values without formatting-preserved line breaks"`
	Markdown     string                `json:"markdown,omitempty" jsonschema:"whitespace-normalized, formatting-preserving Markdown for inline formatting such as links; use only when formatting is requested"`
	Links        []ConfluenceTableLink `json:"links,omitempty"`
	Styles       map[string]string     `json:"styles,omitempty"`
	Header       bool                  `json:"header,omitempty"`
	Rowspan      int                   `json:"rowspan,omitempty"`
	Colspan      int                   `json:"colspan,omitempty"`
	Repeated     bool                  `json:"repeated,omitempty"`
	Synthetic    bool                  `json:"synthetic,omitempty"`
	SourceRow    int                   `json:"source_row,omitempty"`
	SourceColumn int                   `json:"source_column,omitempty"`
	Raw          map[string]string     `json:"raw,omitempty"`
}

// confluenceTableCellKind is the closed set of cell kinds.
type confluenceTableCellKind int

const (
	confluenceTableInvalidCell confluenceTableCellKind = iota
	confluenceTableOriginCell
	confluenceTableRepeatedCell
	confluenceTableSyntheticCell
)

// classifyConfluenceTableCell reads a cell's kind from the durable contract
// documented on ConfluenceTableCell, validating the whole field combination
// rather than trusting one flag. Anything that does not match a kind exactly is
// confluenceTableInvalidCell, which lands in no summary bucket and therefore
// breaks the cell accounting instead of silently passing as some other kind.
func classifyConfluenceTableCell(cell ConfluenceTableCell) confluenceTableCellKind {
	if cell.Row < 1 || cell.Column < 1 || cell.SourceRow < 0 || cell.SourceColumn < 0 ||
		cell.Rowspan < 0 || cell.Colspan < 0 {
		return confluenceTableInvalidCell
	}
	if cell.Repeated && cell.Synthetic {
		return confluenceTableInvalidCell
	}
	own := cell.SourceRow == cell.Row && cell.SourceColumn == cell.Column
	switch {
	case cell.Repeated:
		if own || cell.SourceRow < 1 || cell.SourceColumn < 1 ||
			cell.SourceRow > cell.Row || cell.SourceColumn > cell.Column {
			return confluenceTableInvalidCell
		}
		return confluenceTableRepeatedCell
	case cell.Synthetic:
		if cell.SourceRow != 0 || cell.SourceColumn != 0 || !confluenceTableCellIsBare(cell) {
			return confluenceTableInvalidCell
		}
		return confluenceTableSyntheticCell
	default:
		// Compact native origin: unmarked and carrying no source coordinates. A
		// stray coordinate here is the schema-2 self-naming shape (or worse), which
		// is no longer the origin contract and must not pass as one.
		if cell.SourceRow != 0 || cell.SourceColumn != 0 {
			return confluenceTableInvalidCell
		}
		return confluenceTableOriginCell
	}
}

// confluenceTableCellIsBare reports whether a cell carries nothing but its
// coordinate and synthetic marker, as emptyTableCell emits it. Synthetic padding
// stands for markup that is absent, so a padding cell holding content or span
// metadata is a contradiction.
func confluenceTableCellIsBare(cell ConfluenceTableCell) bool {
	return cell.Text == "" && cell.Markdown == "" && !cell.Header &&
		cell.Rowspan == 0 && cell.Colspan == 0 &&
		len(cell.Links) == 0 && len(cell.Styles) == 0 && len(cell.Raw) == 0
}

// ConfluenceTableLink preserves ordinary table-cell links.
type ConfluenceTableLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type pendingTableCell struct {
	cell ConfluenceTableCell
	rows int
}

// ConfluenceTableSelectionError reports a 1-based table selection that exceeds
// the number of tables on the page. It deliberately carries only the requested
// index and the available table count — never a page id, title, heading,
// caption, cell content, or backend text — so a transport can distinguish a
// recoverable caller-side selection mistake from an unavailable source without
// disclosing page content. It unwraps to domain.ErrNotFound, so sentinel-driven
// exit codes and classification stay unchanged.
type ConfluenceTableSelectionError struct {
	Requested int
	Available int
}

func (e *ConfluenceTableSelectionError) Error() string {
	return fmt.Sprintf("%v: table %d not found (page has %d tables)", domain.ErrNotFound, e.Requested, e.Available)
}

func (e *ConfluenceTableSelectionError) Unwrap() error { return domain.ErrNotFound }

func (e *ConfluenceTableSelectionError) DiagnosticSelection() (requested, available, matches int) {
	if e == nil {
		return 0, 0, 0
	}
	return e.Requested, e.Available, 0
}

// ExtractTables fetches a page's native CSF and extracts table data. table is
// 1-based; table <= 0 returns all tables.
func (s *ConfluenceService) ExtractTables(ctx context.Context, id string, table int) (*ConfluenceTableExtract, error) {
	return s.ExtractTablesWithOptions(ctx, id, table, ConfluenceTableReadOpts{})
}

// ExtractTablesWithOptions fetches a page's native CSF and extracts table data
// while optionally binding the positional selection to an observed revision.
func (s *ConfluenceService) ExtractTablesWithOptions(ctx context.Context, id string, table int, opts ConfluenceTableReadOpts) (*ConfluenceTableExtract, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: --id is required", domain.ErrUsage)
	}
	if opts.ExpectedPageVersion < 0 {
		return nil, fmt.Errorf("%w: --expected-version must be >= 1 when set", domain.ErrUsage)
	}
	resolved, err := s.ResolvePageReference(ctx, id)
	if err != nil {
		return nil, err
	}
	id = resolved.ID
	page, err := s.store.GetPage(ctx, id, domain.PullOpts{Format: "csf"})
	if err != nil {
		return nil, err
	}
	if page == nil || strings.TrimSpace(page.ID) == "" || page.ID != resolved.ID || page.Version < 1 {
		return nil, fmt.Errorf("%w: Confluence page %s identity is not reconciled for table extraction", domain.ErrCheckFailed, resolved.ID)
	}
	if opts.ExpectedPageVersion > 0 && opts.ExpectedPageVersion != page.Version {
		return nil, &ConfluencePageVersionMismatchError{Expected: opts.ExpectedPageVersion, Current: page.Version}
	}
	if err := requireConfluenceNativeBody(page, resolved.ID, "table extraction"); err != nil {
		return nil, err
	}
	extract, err := ExtractTablesFromCSF(page.ID, page.Title, page.Body, table)
	if err != nil {
		return nil, err
	}
	extract.SchemaVersion = ConfluenceTableSchemaVersion
	extract.CellContract = ConfluenceTableCellContract
	extract.Version = page.Version
	extract.PageVersionGated = opts.ExpectedPageVersion > 0
	return extract, nil
}

// SummarizeTables fetches a page's native CSF and returns only bounded table
// structure. table is 1-based; table <= 0 summarizes all tables.
func (s *ConfluenceService) SummarizeTables(ctx context.Context, id string, table int) (*ConfluenceTableSummary, error) {
	return s.SummarizeTablesWithOptions(ctx, id, table, ConfluenceTableReadOpts{})
}

// SummarizeTablesWithOptions returns content-free structure from the same
// optionally version-bound table read contract as extraction.
func (s *ConfluenceService) SummarizeTablesWithOptions(ctx context.Context, id string, table int, opts ConfluenceTableReadOpts) (*ConfluenceTableSummary, error) {
	extract, err := s.ExtractTablesWithOptions(ctx, id, table, opts)
	if err != nil {
		return nil, err
	}
	return SummarizeConfluenceTables(extract), nil
}

// SummarizeConfluenceTables removes all content-bearing fields from a table
// extract and counts structural properties over its expanded representation.
func SummarizeConfluenceTables(extract *ConfluenceTableExtract) *ConfluenceTableSummary {
	if extract == nil {
		return nil
	}
	res := &ConfluenceTableSummary{
		SchemaVersion:      extract.SchemaVersion,
		CellContract:       extract.CellContract,
		PageID:             extract.PageID,
		Version:            extract.Version,
		PageVersionGated:   extract.PageVersionGated,
		TableCount:         extract.TableCount,
		Table:              extract.Table,
		ReturnedTableCount: len(extract.Tables),
		Tables:             make([]ConfluenceTableSummaryRecord, 0, len(extract.Tables)),
	}
	res.SelectionReconciled = confluenceTableSelectionReconciled(extract.Table, extract.TableCount, extract.Tables)
	for _, table := range extract.Tables {
		res.Tables = append(res.Tables, summarizeConfluenceTable(table, extract.SchemaVersion, extract.CellContract))
	}
	return res
}

// summarizeConfluenceTable counts one table's content-free structure.
//
// Every metric, buckets included, is recomputed from serialized fields alone, so
// it is identical before and after a JSON round trip: the cell contract makes
// the origin/repeated/synthetic split durable. CellCountReconciled is likewise
// recomputed — never read off a serialized boolean — from the cell accounting
// plus the durable span ledger, and then confirmed by whichever independent
// witness this table has:
//
//   - live extraction (sourcePlacementChecked): the DOM placement ledger, which
//     shares no state with the expansion, must also agree.
//   - deserialized: the attached Summary must equal the recomputation exactly,
//     and the payload must carry both the current schema version and the exact
//     cell contract marker, so neither a forged summary nor a schema-relabelled
//     pre-contract payload can upgrade itself.
func summarizeConfluenceTable(table ConfluenceTable, schemaVersion int, cellContract string) ConfluenceTableSummaryRecord {
	record := ConfluenceTableSummaryRecord{
		Index:        table.Index,
		RowCount:     table.RowCount,
		ColumnCount:  table.ColumnCount,
		Rectangular:  table.RowCount == len(table.Rows),
		WarningCount: len(table.Warnings),
	}
	styleMarkers := map[[2]string]struct{}{}
	for _, row := range table.Rows {
		if len(row.Cells) != table.ColumnCount {
			record.Rectangular = false
		}
		if row.Header {
			record.HeaderRowCount++
		}
		for _, cell := range row.Cells {
			record.ExpandedCellCount++
			switch classifyConfluenceTableCell(cell) {
			case confluenceTableOriginCell:
				record.OriginCellCount++
			case confluenceTableRepeatedCell:
				record.RepeatedCellCount++
			case confluenceTableSyntheticCell:
				record.SyntheticEmptyCellCount++
			}
			if cell.Header {
				record.HeaderCellCount++
			}
			if cell.Repeated {
				if cell.Row != cell.SourceRow {
					record.RowspanCoveredCellCount++
				}
				if cell.Column != cell.SourceColumn {
					record.ColspanCoveredCellCount++
				}
			} else {
				if cell.Rowspan > 1 {
					record.RowspanSourceCellCount++
				}
				if cell.Colspan > 1 {
					record.ColspanSourceCellCount++
				}
			}
			if cell.Rowspan > 1 {
				record.RowspanMetadataCellCount++
			}
			if cell.Colspan > 1 {
				record.ColspanMetadataCellCount++
			}
			if cell.Text != "" {
				record.NonemptyTextCellCount++
			}
			if cell.Markdown != "" {
				record.NonemptyMarkdownCellCount++
			}
			if len(cell.Raw) > 0 {
				record.NonemptyRawCellCount++
			}
			if len(cell.Styles) > 0 {
				record.StyledCellCount++
			}
			record.StyleEntryCount += len(cell.Styles)
			for key, value := range cell.Styles {
				styleMarkers[[2]string{key, value}] = struct{}{}
			}
			if len(cell.Links) > 0 {
				record.LinkedCellCount++
			}
		}
	}
	record.DistinctStyleMarkerCount = len(styleMarkers)
	record.CellCountReconciled = confluenceTableCountsAccounted(record) && reconcileConfluenceTableCells(table)
	if table.sourcePlacementChecked {
		record.CellCountReconciled = record.CellCountReconciled && table.sourcePlacementReconciled
	} else if record.CellCountReconciled &&
		(schemaVersion != ConfluenceTableSchemaVersion || cellContract != ConfluenceTableCellContract ||
			table.Summary != record) {
		record.CellCountReconciled = false
	}
	return record
}

// confluenceTableCountsAccounted is the rectangular grid/accounting half of
// reconciliation: the emitted grid fills exactly RowCount x ColumnCount and every
// emitted cell lands in exactly one bucket.
func confluenceTableCountsAccounted(record ConfluenceTableSummaryRecord) bool {
	return record.Rectangular &&
		record.ExpandedCellCount == record.RowCount*record.ColumnCount &&
		record.ExpandedCellCount == record.OriginCellCount+record.RepeatedCellCount+record.SyntheticEmptyCellCount
}

// reconcileConfluenceTableCells rebuilds the span ledger from the durable cell
// contract alone — no markup, no in-process marker, no serialized verdict — so a
// deserialized table is reconciled by recomputation rather than by trust.
//
// Every origin claims its declared rowspan x colspan rectangle; the claims must
// stay inside the emitted grid and never collide. The grid must then realize
// exactly those claims: an origin owns its own coordinate, every other claimed
// coordinate holds a repeated cell naming the origin that actually claims it and
// echoing its spans, and every unclaimed coordinate holds synthetic padding.
func reconcileConfluenceTableCells(table ConfluenceTable) bool {
	type coord = [2]int
	if table.RowCount < 1 || table.ColumnCount < 1 || table.RowCount != len(table.Rows) {
		return false
	}
	claims := make(map[coord]coord)
	origins := make(map[coord]ConfluenceTableCell)
	for r, row := range table.Rows {
		if row.Index != r+1 || len(row.Cells) != table.ColumnCount {
			return false
		}
		for c, cell := range row.Cells {
			if cell.Row != r+1 || cell.Column != c+1 {
				return false
			}
			if classifyConfluenceTableCell(cell) != confluenceTableOriginCell {
				continue
			}
			at := coord{cell.Row, cell.Column}
			origins[at] = cell
			rowspan, colspan := max(1, cell.Rowspan), max(1, cell.Colspan)
			if cell.Row+rowspan-1 > table.RowCount || cell.Column+colspan-1 > table.ColumnCount {
				return false
			}
			for dr := 0; dr < rowspan; dr++ {
				for dc := 0; dc < colspan; dc++ {
					to := coord{cell.Row + dr, cell.Column + dc}
					if _, taken := claims[to]; taken {
						return false
					}
					claims[to] = at
				}
			}
		}
	}
	for _, row := range table.Rows {
		for _, cell := range row.Cells {
			at := coord{cell.Row, cell.Column}
			owner, claimed := claims[at]
			switch classifyConfluenceTableCell(cell) {
			case confluenceTableOriginCell:
				if owner != at {
					return false
				}
			case confluenceTableRepeatedCell:
				src := coord{cell.SourceRow, cell.SourceColumn}
				origin, exists := origins[src]
				if !claimed || owner != src || !exists ||
					cell.Rowspan != origin.Rowspan || cell.Colspan != origin.Colspan {
					return false
				}
			case confluenceTableSyntheticCell:
				if claimed {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// ExtractTablesFromCSF extracts all or one table from a CSF body.
func ExtractTablesFromCSF(pageID, title string, body []byte, table int) (*ConfluenceTableExtract, error) {
	if table < 0 {
		return nil, fmt.Errorf("%w: --table must be >= 1", domain.ErrUsage)
	}
	root, err := csf.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("parse CSF: %w", err)
	}
	nodes := topLevelTables(root)
	all := make([]ConfluenceTable, 0, len(nodes))
	for i, node := range nodes {
		all = append(all, extractTable(i+1, node))
	}
	res := &ConfluenceTableExtract{
		SchemaVersion: ConfluenceTableSchemaVersion,
		CellContract:  ConfluenceTableCellContract,
		PageID:        pageID,
		Title:         title,
		TableCount:    len(all),
		Tables:        all,
	}
	if table > 0 {
		if table > len(all) {
			return nil, &ConfluenceTableSelectionError{Requested: table, Available: len(all)}
		}
		res.Table = table
		res.Tables = []ConfluenceTable{all[table-1]}
	}
	res.ReturnedTableCount = len(res.Tables)
	res.SelectionReconciled = confluenceTableSelectionReconciled(res.Table, res.TableCount, res.Tables)
	if err := attachConfluenceTableSummaries(res); err != nil {
		return nil, err
	}
	return res, nil
}

func confluenceTableSelectionReconciled(table, tableCount int, tables []ConfluenceTable) bool {
	return (table == 0 && len(tables) == tableCount) ||
		(table > 0 && len(tables) == 1 && tables[0].Index == table && table <= tableCount)
}

func attachConfluenceTableSummaries(extract *ConfluenceTableExtract) error {
	summary := SummarizeConfluenceTables(extract)
	if summary == nil || len(summary.Tables) != len(extract.Tables) {
		return fmt.Errorf("%w: table summary could not be reconciled", domain.ErrCheckFailed)
	}
	for i := range extract.Tables {
		extract.Tables[i].Summary = summary.Tables[i]
	}
	return nil
}

func topLevelTables(root *csf.Node) []*csf.Node {
	var out []*csf.Node
	var walk func(*csf.Node, bool)
	walk = func(n *csf.Node, inTable bool) {
		if n.Type == csf.Element && n.Name.Space == "" && n.Name.Local == "table" {
			if !inTable {
				out = append(out, n)
			}
			inTable = true
		}
		for _, c := range n.Children {
			walk(c, inTable)
		}
	}
	walk(root, false)
	return out
}

func extractTable(index int, table *csf.Node) ConfluenceTable {
	rows := tableRows(table)
	out := ConfluenceTable{Index: index}
	pending := map[int]pendingTableCell{}
	for rowIdx, tr := range rows {
		row := ConfluenceTableRow{Index: rowIdx + 1}
		col := 0
		for _, cellNode := range rowCells(tr) {
			for {
				p, ok := pending[col]
				if !ok {
					break
				}
				row.Cells = append(row.Cells, repeatedCell(p.cell, rowIdx, col))
				p.rows--
				if p.rows <= 0 {
					delete(pending, col)
				} else {
					pending[col] = p
				}
				col++
			}
			header := cellNode.Name.Local == "th"
			cell := tableCell(rowIdx, col, header, cellNode)
			if header {
				row.Header = true
			}
			for spanCol := 0; spanCol < max(1, cell.Colspan); spanCol++ {
				placed := cell
				placed.Column = col + 1
				if spanCol > 0 {
					placed = repeatedCell(cell, rowIdx, col)
				}
				row.Cells = append(row.Cells, placed)
				if cell.Rowspan > 1 {
					pending[col] = pendingTableCell{cell: cell, rows: cell.Rowspan - 1}
				}
				col++
			}
		}
		for col <= maxPendingTableCol(pending) {
			if p, ok := pending[col]; ok {
				row.Cells = append(row.Cells, repeatedCell(p.cell, rowIdx, col))
				p.rows--
				if p.rows <= 0 {
					delete(pending, col)
				} else {
					pending[col] = p
				}
			} else {
				row.Cells = append(row.Cells, emptyTableCell(rowIdx, col))
			}
			col++
		}
		if len(row.Cells) > out.ColumnCount {
			out.ColumnCount = len(row.Cells)
		}
		out.Rows = append(out.Rows, row)
	}
	for i := range out.Rows {
		for len(out.Rows[i].Cells) < out.ColumnCount {
			out.Rows[i].Cells = append(out.Rows[i].Cells, emptyTableCell(i, len(out.Rows[i].Cells)))
		}
	}
	out.RowCount = len(out.Rows)
	if len(out.Rows) > 0 && out.Rows[0].Header {
		out.Headers = make([]string, out.ColumnCount)
		for i, cell := range out.Rows[0].Cells {
			out.Headers[i] = cell.Text
		}
	}
	out.sourcePlacementChecked = true
	out.sourcePlacementReconciled = reconcileConfluenceTableSource(rows, out)
	return out
}

// reconcileConfluenceTableSource independently reconstructs where every source
// cell lands, straight from the DOM, and then checks the emitted grid against
// that ledger. It shares no state with the expansion above, so an expansion bug
// shows up as a disagreement instead of being reproduced.
//
// Invariants, all of which must hold for a table to reconcile:
//
//   - Placement: a source cell occupies the first column of its own source row
//     not already claimed by an earlier cell's span rectangle.
//   - Claims: a source cell claims every coordinate of its declared
//     rowspan x colspan rectangle, and no coordinate is ever claimed twice.
//   - Domain: no claim falls outside the source row domain — a rowspan may not
//     run past the last source row, and a colspan may not run past the emitted
//     column count.
//   - Agreement: the emitted grid matches the ledger cell for cell. An origin
//     cell owns its own coordinate, a repeated cell names the coordinate of the
//     source cell that actually claims it, a synthetic pad cell sits on an
//     unclaimed coordinate, and every claim is realized by exactly one cell.
//
// Iteration is clamped to the row/column domain so a hostile span attribute
// cannot make the ledger allocate beyond the size of the document; exceeding the
// domain is itself a failure, so clamping never hides one.
func reconcileConfluenceTableSource(rows []*csf.Node, grid ConfluenceTable) bool {
	type coord = [2]int
	claims := make(map[coord]coord)
	ok := true
	for r, tr := range rows {
		col := 0
		for _, node := range rowCells(tr) {
			for {
				if _, taken := claims[coord{r + 1, col + 1}]; !taken {
					break
				}
				col++
			}
			rowspan, colspan := spanOf(node, "rowspan"), spanOf(node, "colspan")
			origin := coord{r + 1, col + 1}
			rowReach, colReach := rowspan, colspan
			if over := origin[0] + rowspan - 1 - len(rows); over > 0 {
				ok = false
				rowReach -= over
			}
			if over := origin[1] + colspan - 1 - grid.ColumnCount; over > 0 {
				ok = false
				colReach -= over
			}
			for dr := 0; dr < rowReach; dr++ {
				for dc := 0; dc < colReach; dc++ {
					at := coord{origin[0] + dr, origin[1] + dc}
					if _, taken := claims[at]; taken {
						ok = false
						continue
					}
					claims[at] = origin
				}
			}
			col += colspan
		}
	}
	realized := 0
	for _, row := range grid.Rows {
		for _, cell := range row.Cells {
			at := coord{cell.Row, cell.Column}
			owner, claimed := claims[at]
			if claimed {
				realized++
			}
			switch classifyConfluenceTableCell(cell) {
			case confluenceTableOriginCell:
				if owner != at {
					ok = false
				}
			case confluenceTableRepeatedCell:
				if owner != (coord{cell.SourceRow, cell.SourceColumn}) || !claimed {
					ok = false
				}
			case confluenceTableSyntheticCell:
				if claimed {
					ok = false
				}
			default:
				ok = false
			}
		}
	}
	return ok && realized == len(claims)
}

func tableCell(row, col int, header bool, n *csf.Node) ConfluenceTableCell {
	rowspan := spanOf(n, "rowspan")
	colspan := spanOf(n, "colspan")
	links := cellLinks(n)
	styles := cellStyles(n)
	cell := ConfluenceTableCell{
		Row:      row + 1,
		Column:   col + 1,
		Text:     normalizeCellText(csf.TextContent(n)),
		Markdown: normalizeCellText(cellMarkdown(n)),
		Links:    links,
		Styles:   styles,
		Header:   header,
		Rowspan:  omitOne(rowspan),
		Colspan:  omitOne(colspan),
		// A native origin is the compact default: no marker, no source
		// coordinates. classifyConfluenceTableCell reads its kind from that.
	}
	if raw := cellRaw(n); len(raw) > 0 {
		cell.Raw = raw
	}
	return cell
}

func emptyTableCell(row, col int) ConfluenceTableCell {
	return ConfluenceTableCell{Row: row + 1, Column: col + 1, Synthetic: true}
}

func repeatedCell(src ConfluenceTableCell, row, col int) ConfluenceTableCell {
	c := src
	c.Row = row + 1
	c.Column = col + 1
	c.Repeated = true
	c.SourceRow = src.Row
	c.SourceColumn = src.Column
	return c
}

func spanOf(n *csf.Node, name string) int {
	v, err := strconv.Atoi(strings.TrimSpace(n.Attrv("", name)))
	if err != nil || v < 1 {
		return 1
	}
	return v
}

func omitOne(v int) int {
	if v <= 1 {
		return 0
	}
	return v
}

func tableRows(table *csf.Node) []*csf.Node {
	var rows []*csf.Node
	csf.Walk(table, func(x *csf.Node) bool {
		if x != table && x.Name.Space == "" && x.Name.Local == "table" {
			return false
		}
		if x.Name.Space == "" && x.Name.Local == "tr" {
			rows = append(rows, x)
			return false
		}
		return true
	})
	return rows
}

func rowCells(row *csf.Node) []*csf.Node {
	var cells []*csf.Node
	for _, c := range row.Children {
		if c.Type == csf.Element && c.Name.Space == "" && (c.Name.Local == "td" || c.Name.Local == "th") {
			cells = append(cells, c)
		}
	}
	return cells
}

func maxPendingTableCol(pending map[int]pendingTableCell) int {
	maxCol := -1
	for col := range pending {
		if col > maxCol {
			maxCol = col
		}
	}
	return maxCol
}

func cellLinks(n *csf.Node) []ConfluenceTableLink {
	var links []ConfluenceTableLink
	csf.Walk(n, func(x *csf.Node) bool {
		if x.Type != csf.Element {
			return true
		}
		if x.Name.Space == "" && x.Name.Local == "a" {
			if href := strings.TrimSpace(x.Attrv("", "href")); href != "" {
				links = append(links, ConfluenceTableLink{Text: normalizeCellText(csf.TextContent(x)), URL: href})
			}
		}
		if x.Name.Space == "ri" && x.Name.Local == "url" {
			if href := strings.TrimSpace(x.Attrv("ri", "value")); href != "" {
				links = append(links, ConfluenceTableLink{Text: normalizeCellText(csf.TextContent(x)), URL: href})
			}
		}
		return true
	})
	return links
}

func cellStyles(n *csf.Node) map[string]string {
	styles := map[string]string{}
	csf.Walk(n, func(x *csf.Node) bool {
		if x.Type != csf.Element {
			return true
		}
		if color := styleColor(x); color != "" {
			styles["color"] = color
		}
		return true
	})
	if len(styles) == 0 {
		return nil
	}
	return styles
}

func cellRaw(n *csf.Node) map[string]string {
	raw := map[string]string{}
	for _, name := range []string{"rowspan", "colspan"} {
		if v := strings.TrimSpace(n.Attrv("", name)); v != "" {
			raw[name] = v
		}
	}
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func cellMarkdown(n *csf.Node) string {
	var render func(*csf.Node) string
	render = func(x *csf.Node) string {
		switch x.Type {
		case csf.Text, csf.CData:
			return html.EscapeString(x.Data)
		case csf.Element:
			var b strings.Builder
			for _, c := range x.Children {
				b.WriteString(render(c))
			}
			inner := b.String()
			if x.Name.Space == "" && x.Name.Local == "a" {
				if href := strings.TrimSpace(x.Attrv("", "href")); href != "" {
					return "[" + normalizeCellText(inner) + "](" + href + ")"
				}
			}
			if color := styleColor(x); color != "" {
				if safe, ok := mirror.SafeCSSColor(color); ok {
					return "<span style=\"color: " + html.EscapeString(safe) + "\">" + normalizeCellText(inner) + "</span>"
				}
				return "<span data-atl-color=\"" + html.EscapeString(color) + "\">" + normalizeCellText(inner) + "</span>"
			}
			return inner
		default:
			return ""
		}
	}
	return render(n)
}

func styleColor(n *csf.Node) string {
	if color := strings.TrimSpace(n.Attrv("", "data-color")); color != "" {
		return color
	}
	style := n.Attrv("", "style")
	for _, decl := range strings.Split(style, ";") {
		k, v, ok := strings.Cut(decl, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "color") {
			continue
		}
		if color := strings.TrimSpace(v); color != "" {
			return color
		}
	}
	return ""
}

var cellSpaceRE = regexp.MustCompile(`\s+`)

func normalizeCellText(s string) string {
	return strings.TrimSpace(cellSpaceRE.ReplaceAllString(s, " "))
}

// RenderConfluenceTableCSV renders a CSV view. When all tables are selected it
// emits a cell-level CSV so tables with different shapes can share one stream.
// When a single table was selected via --table, it emits a rectangular table CSV.
func RenderConfluenceTableCSV(res *ConfluenceTableExtract) ([]byte, error) {
	return RenderConfluenceTableCSVWithOptions(res, false)
}

// RenderConfluenceTableCSVWithOptions renders CSV with formula neutralization
// by default. rawCSV is an explicit escape hatch for non-spreadsheet consumers.
func RenderConfluenceTableCSVWithOptions(res *ConfluenceTableExtract, rawCSV bool) ([]byte, error) {
	if res == nil {
		return nil, fmt.Errorf("%w: no table extract result", domain.ErrUsage)
	}
	if res.Table > 0 && len(res.Tables) == 1 {
		return renderSelectedTableCSV(res.Tables[0], rawCSV)
	}
	return renderAllTablesCellCSV(res.Tables, rawCSV)
}

func renderSelectedTableCSV(table ConfluenceTable, rawCSV bool) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	header := table.Headers
	start := 0
	if len(header) == table.ColumnCount && nonEmptyHeader(header) {
		start = 1
	} else {
		header = make([]string, table.ColumnCount)
		for i := range header {
			header[i] = fmt.Sprintf("col_%d", i+1)
		}
	}
	if err := w.Write(spreadsheetRecord(header, rawCSV)); err != nil {
		return nil, err
	}
	for _, row := range table.Rows[start:] {
		record := make([]string, table.ColumnCount)
		for i, cell := range row.Cells {
			if i < len(record) {
				record[i] = cell.Markdown
				if record[i] == "" {
					record[i] = cell.Text
				}
			}
		}
		if err := w.Write(spreadsheetRecord(record, rawCSV)); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return b.Bytes(), w.Error()
}

func renderAllTablesCellCSV(tables []ConfluenceTable, rawCSV bool) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.Write(spreadsheetRecord([]string{"table", "row", "column", "text", "markdown", "links", "styles", "repeated", "source_row", "source_column"}, rawCSV)); err != nil {
		return nil, err
	}
	for _, table := range tables {
		for _, row := range table.Rows {
			for _, cell := range row.Cells {
				links, err := json.Marshal(cell.Links)
				if err != nil {
					return nil, err
				}
				styles, err := json.Marshal(cell.Styles)
				if err != nil {
					return nil, err
				}
				record := []string{
					strconv.Itoa(table.Index),
					strconv.Itoa(cell.Row),
					strconv.Itoa(cell.Column),
					cell.Text,
					cell.Markdown,
					string(links),
					string(styles),
					strconv.FormatBool(cell.Repeated),
					"",
					"",
				}
				// The compact JSON leaves a native origin's source coordinates
				// implicit, but the flat CSV states every origin's self placement
				// explicitly. Derive the pair from the durable kind so origins and
				// repeated cells both resolve to a real coordinate and synthetic
				// padding stays blank.
				switch classifyConfluenceTableCell(cell) {
				case confluenceTableOriginCell:
					record[8] = strconv.Itoa(cell.Row)
					record[9] = strconv.Itoa(cell.Column)
				case confluenceTableRepeatedCell:
					record[8] = strconv.Itoa(cell.SourceRow)
					record[9] = strconv.Itoa(cell.SourceColumn)
				}
				if err := w.Write(spreadsheetRecord(record, rawCSV)); err != nil {
					return nil, err
				}
			}
		}
	}
	w.Flush()
	return b.Bytes(), w.Error()
}

func nonEmptyHeader(header []string) bool {
	for _, h := range header {
		if strings.TrimSpace(h) != "" {
			return true
		}
	}
	return false
}

// WriteConfluenceTableArtifact atomically persists an already-rendered small
// table artifact — the JSON or CSV bytes — to path through the shared
// application user-file writer, so every table --out format lands on disk via
// the same atomic temp-file-then-rename boundary. A blank path is a usage
// error; any persistence failure wraps domain.ErrCheckFailed while preserving
// the underlying cause for errors.Is / errors.As inspection.
func WriteConfluenceTableArtifact(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: --out is required to persist a table artifact", domain.ErrUsage)
	}
	if err := writeUserFile(path, data); err != nil {
		return fmt.Errorf("%w: persist Confluence table artifact %q: %w", domain.ErrCheckFailed, path, err)
	}
	return nil
}

// WriteConfluenceTableXLSX writes a minimal XLSX workbook with one worksheet per
// extracted table. It uses inline strings so no shared string table is needed.
// The workbook streams through the same atomic user-file writer as the JSON and
// CSV artifacts; a blank path is a usage error and any persistence failure wraps
// domain.ErrCheckFailed while preserving the underlying cause.
func WriteConfluenceTableXLSX(path string, res *ConfluenceTableExtract) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: --out is required for --format xlsx", domain.ErrUsage)
	}
	if err := writeUserFileStream(path, func(w io.Writer) error {
		return streamConfluenceTableXLSX(w, res)
	}); err != nil {
		return fmt.Errorf("%w: persist Confluence table workbook %q: %w", domain.ErrCheckFailed, path, err)
	}
	return nil
}

// streamConfluenceTableXLSX writes the workbook parts into w. It performs no
// persistence of its own so the atomic temp-file boundary stays owned by
// writeUserFileStream.
func streamConfluenceTableXLSX(w io.Writer, res *ConfluenceTableExtract) error {
	zw := zip.NewWriter(w)
	if err := writeXLSXFile(zw, "[Content_Types].xml", xlsxContentTypes(len(res.Tables))); err != nil {
		return err
	}
	if err := writeXLSXFile(zw, "_rels/.rels", xlsxRootRels()); err != nil {
		return err
	}
	if err := writeXLSXFile(zw, "xl/workbook.xml", xlsxWorkbook(res.Tables)); err != nil {
		return err
	}
	if err := writeXLSXFile(zw, "xl/_rels/workbook.xml.rels", xlsxWorkbookRels(len(res.Tables))); err != nil {
		return err
	}
	if err := writeXLSXFile(zw, "xl/styles.xml", xlsxStyles()); err != nil {
		return err
	}
	for i, table := range res.Tables {
		if err := writeXLSXFile(zw, fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), xlsxWorksheet(table)); err != nil {
			return err
		}
	}
	return zw.Close()
}

func writeXLSXFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func xlsxContentTypes(sheets int) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	b.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	b.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	b.WriteString(`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	b.WriteString(`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	for i := 1; i <= sheets; i++ {
		fmt.Fprintf(&b, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i)
	}
	b.WriteString(`</Types>`)
	return []byte(b.String())
}

func xlsxRootRels() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`)
}

func xlsxWorkbook(tables []ConfluenceTable) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	for i, table := range tables {
		fmt.Fprintf(&b, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlAttr(fmt.Sprintf("Table %d", table.Index)), i+1, i+1)
	}
	b.WriteString(`</sheets></workbook>`)
	return []byte(b.String())
}

func xlsxWorkbookRels(sheets int) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := 1; i <= sheets; i++ {
		fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i, i)
	}
	fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, sheets+1)
	b.WriteString(`</Relationships>`)
	return []byte(b.String())
}

func xlsxStyles() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="1"><fill><patternFill patternType="none"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/></cellXfs></styleSheet>`)
}

func xlsxWorksheet(table ConfluenceTable) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for _, row := range table.Rows {
		fmt.Fprintf(&b, `<row r="%d">`, row.Index)
		for _, cell := range row.Cells {
			ref := spreadsheetColumn(cell.Column) + strconv.Itoa(row.Index)
			value := cell.Markdown
			if value == "" {
				value = cell.Text
			}
			fmt.Fprintf(&b, `<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, ref, xmlText(value))
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return []byte(b.String())
}

func spreadsheetColumn(n int) string {
	if n <= 0 {
		return ""
	}
	var out []byte
	for n > 0 {
		n--
		out = append([]byte{byte('A' + n%26)}, out...)
		n /= 26
	}
	return string(out)
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func xmlAttr(s string) string {
	return xmlText(s)
}
