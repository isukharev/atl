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
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// ConfluenceTableExtract is a structured, read-only view of tables on a page.
type ConfluenceTableExtract struct {
	PageID     string            `json:"page_id"`
	Title      string            `json:"title,omitempty"`
	TableCount int               `json:"table_count"`
	Table      int               `json:"selected_table,omitempty"`
	Tables     []ConfluenceTable `json:"tables"`
}

// ConfluenceTableSummary is a bounded, content-free structural inventory of
// tables on a page. It deliberately excludes page and cell content.
type ConfluenceTableSummary struct {
	PageID     string                         `json:"page_id"`
	TableCount int                            `json:"table_count"`
	Table      int                            `json:"selected_table,omitempty"`
	Tables     []ConfluenceTableSummaryRecord `json:"tables"`
}

// ConfluenceTableSummaryRecord describes one expanded table without exposing
// cell text, links, style values, raw attributes, or warning text.
type ConfluenceTableSummaryRecord struct {
	Index                   int `json:"index"`
	RowCount                int `json:"row_count"`
	ColumnCount             int `json:"column_count"`
	HeaderRowCount          int `json:"header_row_count"`
	HeaderCellCount         int `json:"header_cell_count"`
	ExpandedCellCount       int `json:"expanded_cell_count"`
	RepeatedCellCount       int `json:"repeated_cell_count"`
	StyledCellCount         int `json:"styled_cell_count"`
	LinkedCellCount         int `json:"linked_cell_count"`
	RowspanSourceCellCount  int `json:"rowspan_source_cell_count"`
	RowspanCoveredCellCount int `json:"rowspan_covered_cell_count"`
	ColspanSourceCellCount  int `json:"colspan_source_cell_count"`
	ColspanCoveredCellCount int `json:"colspan_covered_cell_count"`
	WarningCount            int `json:"warning_count"`
}

// ConfluenceTable is one expanded table. Index is 1-based in document order.
type ConfluenceTable struct {
	Index       int                       `json:"index"`
	RowCount    int                       `json:"row_count"`
	ColumnCount int                       `json:"column_count"`
	Headers     []string                  `json:"headers,omitempty"`
	Rows        []ConfluenceTableRow      `json:"rows"`
	Warnings    []string                  `json:"warnings,omitempty"`
	Metadata    map[string]map[string]any `json:"metadata,omitempty"`
}

// ConfluenceTableRow is one expanded row.
type ConfluenceTableRow struct {
	Index  int                   `json:"index"`
	Header bool                  `json:"header,omitempty"`
	Cells  []ConfluenceTableCell `json:"cells"`
}

// ConfluenceTableCell is one expanded cell. Repeated cells come from a
// rowspan/colspan-covered source cell and keep SourceRow/SourceColumn set.
type ConfluenceTableCell struct {
	Row          int                   `json:"row"`
	Column       int                   `json:"column"`
	Text         string                `json:"text"`
	Markdown     string                `json:"markdown,omitempty"`
	Links        []ConfluenceTableLink `json:"links,omitempty"`
	Styles       map[string]string     `json:"styles,omitempty"`
	Header       bool                  `json:"header,omitempty"`
	Rowspan      int                   `json:"rowspan,omitempty"`
	Colspan      int                   `json:"colspan,omitempty"`
	Repeated     bool                  `json:"repeated,omitempty"`
	SourceRow    int                   `json:"source_row,omitempty"`
	SourceColumn int                   `json:"source_column,omitempty"`
	Raw          map[string]string     `json:"raw,omitempty"`
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

// ExtractTables fetches a page's native CSF and extracts table data. table is
// 1-based; table <= 0 returns all tables.
func (s *ConfluenceService) ExtractTables(ctx context.Context, id string, table int) (*ConfluenceTableExtract, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: --id is required", domain.ErrUsage)
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
	if err := requireConfluenceNativeBody(page, id, "table extraction"); err != nil {
		return nil, err
	}
	return ExtractTablesFromCSF(page.ID, page.Title, page.Body, table)
}

// SummarizeTables fetches a page's native CSF and returns only bounded table
// structure. table is 1-based; table <= 0 summarizes all tables.
func (s *ConfluenceService) SummarizeTables(ctx context.Context, id string, table int) (*ConfluenceTableSummary, error) {
	extract, err := s.ExtractTables(ctx, id, table)
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
		PageID:     extract.PageID,
		TableCount: extract.TableCount,
		Table:      extract.Table,
		Tables:     make([]ConfluenceTableSummaryRecord, 0, len(extract.Tables)),
	}
	for _, table := range extract.Tables {
		record := ConfluenceTableSummaryRecord{
			Index:        table.Index,
			RowCount:     table.RowCount,
			ColumnCount:  table.ColumnCount,
			WarningCount: len(table.Warnings),
		}
		for _, row := range table.Rows {
			if row.Header {
				record.HeaderRowCount++
			}
			for _, cell := range row.Cells {
				record.ExpandedCellCount++
				if cell.Header {
					record.HeaderCellCount++
				}
				if cell.Repeated {
					record.RepeatedCellCount++
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
				if len(cell.Styles) > 0 {
					record.StyledCellCount++
				}
				if len(cell.Links) > 0 {
					record.LinkedCellCount++
				}
			}
		}
		res.Tables = append(res.Tables, record)
	}
	return res
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
		PageID:     pageID,
		Title:      title,
		TableCount: len(all),
		Tables:     all,
	}
	if table > 0 {
		if table > len(all) {
			return nil, fmt.Errorf("%w: table %d not found (page has %d tables)", domain.ErrNotFound, table, len(all))
		}
		res.Table = table
		res.Tables = []ConfluenceTable{all[table-1]}
	}
	return res, nil
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
	return out
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
	}
	if raw := cellRaw(n); len(raw) > 0 {
		cell.Raw = raw
	}
	return cell
}

func emptyTableCell(row, col int) ConfluenceTableCell {
	return ConfluenceTableCell{Row: row + 1, Column: col + 1}
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
				if cell.SourceRow > 0 {
					record[8] = strconv.Itoa(cell.SourceRow)
				}
				if cell.SourceColumn > 0 {
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

// WriteConfluenceTableXLSX writes a minimal XLSX workbook with one worksheet per
// extracted table. It uses inline strings so no shared string table is needed.
func WriteConfluenceTableXLSX(path string, res *ConfluenceTableExtract) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: --out is required for --format xlsx", domain.ErrUsage)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
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
