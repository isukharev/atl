package app

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"io"
	"strings"
	"testing"
)

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
	if res.TableCount != 2 || len(res.Tables) != 2 {
		t.Fatalf("tables = %d/%d, want two", res.TableCount, len(res.Tables))
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
	if origin.Styles["color"] != "red" || !strings.Contains(origin.Markdown, "⟦color:red⟧") {
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

func TestExtractTablesFromCSFSelectsOneTable(t *testing.T) {
	res, err := ExtractTablesFromCSF("123", "Doc", []byte(tableExtractCSF), 2)
	if err != nil {
		t.Fatalf("ExtractTablesFromCSF: %v", err)
	}
	if res.Table != 2 || res.TableCount != 2 || len(res.Tables) != 1 || res.Tables[0].Index != 2 {
		t.Fatalf("selection = %+v", res)
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
	if !strings.Contains(string(data), "table,row,column,text,markdown") || !strings.Contains(string(data), "true,2,1") {
		t.Fatalf("all-table csv missing cell metadata:\n%s", data)
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
