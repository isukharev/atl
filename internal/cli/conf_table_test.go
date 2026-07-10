package cli

import (
	"archive/zip"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const confTableCSF = `
<table>
  <tbody>
    <tr><th>Note</th><th>Item</th></tr>
    <tr><td rowspan="2">Shared note</td><td>A</td></tr>
    <tr><td>B</td></tr>
  </tbody>
</table>
<table>
  <tbody>
    <tr><th>Name</th><th>URL</th></tr>
    <tr><td>Product</td><td><a href="https://example.test/product">Link</a></td></tr>
  </tbody>
</table>`

func TestConfTableExtractCLIJSON(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("12345", "Design Doc", 3, confTableCSF)

	out, code := runCLI(t, confEnv(cs.srv), "conf", "table", "extract", "--id", "12345")
	if code != exitOK {
		t.Fatalf("conf table extract: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		TableCount int `json:"table_count"`
		Tables     []struct {
			Index int `json:"index"`
			Rows  []struct {
				Cells []struct {
					Text     string `json:"text"`
					Repeated bool   `json:"repeated"`
					Links    []struct {
						URL string `json:"url"`
					} `json:"links"`
				} `json:"cells"`
			} `json:"rows"`
		} `json:"tables"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if res.TableCount != 2 || len(res.Tables) != 2 {
		t.Fatalf("res = %+v, want two tables", res)
	}
	if !res.Tables[0].Rows[2].Cells[0].Repeated || res.Tables[0].Rows[2].Cells[0].Text != "Shared note" {
		t.Fatalf("rowspan cell = %+v", res.Tables[0].Rows[2].Cells[0])
	}
	if got := res.Tables[1].Rows[1].Cells[1].Links[0].URL; got != "https://example.test/product" {
		t.Fatalf("link url = %q", got)
	}
}

func TestConfTableExtractCLICSVFormulaSafetyAndRawEscapeHatch(t *testing.T) {
	const formulaTable = `<table><tbody><tr><th>=Header</th></tr><tr><td>@cmd</td></tr></tbody></table>`
	for _, tc := range []struct {
		raw        bool
		wantHeader string
		wantCell   string
	}{{false, "'=Header", "'@cmd"}, {true, "=Header", "@cmd"}} {
		cs := newConfServer(t)
		cs.page = pageJSON("12345", "Formula", 1, formulaTable)
		args := []string{"conf", "table", "extract", "--id", "12345", "--table", "1", "--format", "csv"}
		if tc.raw {
			args = append(args, "--raw-csv")
		}
		out, code := runCLI(t, confEnv(cs.srv), args...)
		if code != exitOK {
			t.Fatalf("raw=%v exit=%d output=%q", tc.raw, code, out)
		}
		records, err := csv.NewReader(strings.NewReader(out)).ReadAll()
		if err != nil || len(records) != 2 || records[0][0] != tc.wantHeader || records[1][0] != tc.wantCell {
			t.Fatalf("raw=%v records=%#v error=%v", tc.raw, records, err)
		}
	}
}

func TestConfTableExtractCLICSVSelectedTable(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("12345", "Design Doc", 3, confTableCSF)

	out, code := runCLI(t, confEnv(cs.srv), "conf", "table", "extract", "--id", "12345", "--table", "2", "--format", "csv")
	if code != exitOK {
		t.Fatalf("conf table extract csv: exit %d, want 0 (stdout=%q)", code, out)
	}
	if !strings.Contains(out, "Name,URL") || !strings.Contains(out, "Product,[Link](https://example.test/product)") {
		t.Fatalf("csv output = %q", out)
	}
}

func TestConfTableExtractCLIXLSXRequiresOutAndWritesWorkbook(t *testing.T) {
	cs := newConfServer(t)
	cs.page = pageJSON("12345", "Design Doc", 3, confTableCSF)

	_, code := runCLI(t, confEnv(cs.srv), "conf", "table", "extract", "--id", "12345", "--format", "xlsx")
	if code != exitUsage {
		t.Fatalf("xlsx without --out exit = %d, want %d", code, exitUsage)
	}

	outPath := filepath.Join(t.TempDir(), "tables.xlsx")
	out, code := runCLI(t, confEnv(cs.srv), "conf", "table", "extract", "--id", "12345", "--format", "xlsx", "--out", outPath)
	if code != exitOK {
		t.Fatalf("conf table extract xlsx: exit %d, want 0 (stdout=%q)", code, out)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("xlsx not written: %v", err)
	}
	zr, err := zip.OpenReader(outPath)
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	defer zr.Close()
	if len(zr.File) == 0 {
		t.Fatal("xlsx zip is empty")
	}
}
