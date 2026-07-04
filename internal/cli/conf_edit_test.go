package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeEditFixture(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfEdit_NBSPTolerantReplaceWrites(t *testing.T) {
	// The file has U+00A0 between words; the caller types plain spaces.
	p := writeEditFixture(t, "page.csf", "<p>Запрос предназначен для получения микса</p>")

	out, code := runCLI(t, nil, "conf", "edit", p,
		"--old", "Запрос предназначен для получения", "--new", "Запрос возвращает")
	if code != exitOK {
		t.Fatalf("edit: exit %d, want 0 (stdout=%q)", code, out)
	}
	var res struct {
		Pass  string `json:"pass"`
		Count int    `json:"count"`
		CsfOK bool   `json:"csf_ok"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if res.Pass != "invisible" || res.Count != 1 || !res.CsfOK {
		t.Errorf("result = %+v", res)
	}
	after, _ := os.ReadFile(p)
	if string(after) != "<p>Запрос возвращает микса</p>" {
		t.Errorf("file = %q", after)
	}
}

func TestConfEdit_DryRunDoesNotWrite(t *testing.T) {
	body := "<p>alpha beta</p>"
	p := writeEditFixture(t, "page.csf", body)

	out, code := runCLI(t, nil, "conf", "edit", p, "--old", "alpha", "--new", "gamma", "--dry-run")
	if code != exitOK {
		t.Fatalf("dry-run: exit %d (stdout=%q)", code, out)
	}
	after, _ := os.ReadFile(p)
	if string(after) != body {
		t.Errorf("dry-run modified the file: %q", after)
	}
}

func TestConfEdit_NoMatchIsExit4WithContext(t *testing.T) {
	p := writeEditFixture(t, "page.csf", "<p>Параметры запроса</p>")

	_, stderr, code := runCLIFull(t, nil, "conf", "edit", p, "--old", "нет такого текста", "--new", "x")
	if code != exitNotFound {
		t.Fatalf("exit %d, want %d (stderr=%q)", code, exitNotFound, stderr)
	}
	after, _ := os.ReadFile(p)
	if string(after) != "<p>Параметры запроса</p>" {
		t.Errorf("failed edit modified the file: %q", after)
	}
}

func TestConfEdit_AmbiguousIsExit2UnlessAll(t *testing.T) {
	p := writeEditFixture(t, "page.csf", "<td>да</td><td>да</td>")

	_, code := runCLI(t, nil, "conf", "edit", p, "--old", "да", "--new", "нет")
	if code != exitUsage {
		t.Fatalf("ambiguous: exit %d, want %d", code, exitUsage)
	}
	out, code := runCLI(t, nil, "conf", "edit", p, "--old", "да", "--new", "нет", "--all")
	if code != exitOK {
		t.Fatalf("--all: exit %d (stdout=%q)", code, out)
	}
	after, _ := os.ReadFile(p)
	if string(after) != "<td>нет</td><td>нет</td>" {
		t.Errorf("file = %q", after)
	}
}

// Breaking the XML must still write (the push gate blocks later) but must
// say so: csf_ok=false in JSON plus a stderr warning.
func TestConfEdit_InvalidResultWarns(t *testing.T) {
	p := writeEditFixture(t, "page.csf", "<p>keep <strong>bold</strong></p>")

	out, stderr, code := runCLIFull(t, nil, "conf", "edit", p, "--old", "</strong>", "--new", "")
	if code != exitOK {
		t.Fatalf("edit: exit %d", code)
	}
	var res struct {
		CsfOK    bool            `json:"csf_ok"`
		Problems json.RawMessage `json:"problems"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if res.CsfOK || len(res.Problems) == 0 {
		t.Errorf("expected csf_ok=false with problems, got %s", out)
	}
	if stderr == "" {
		t.Error("expected a stderr warning about not-well-formed result")
	}
}

func TestConfEdit_FlagGuards(t *testing.T) {
	p := writeEditFixture(t, "page.csf", "<p>x</p>")
	for _, args := range [][]string{
		{"conf", "edit", p, "--new", "y"},                                // no --old
		{"conf", "edit", p, "--old", "x"},                                // no --new
		{"conf", "edit", p, "--old", "x", "--old-file", p, "--new", "y"}, // both forms
	} {
		if _, code := runCLI(t, nil, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestConfEdit_ExplicitEmptyNewDeletes(t *testing.T) {
	p := writeEditFixture(t, "page.csf", "<p>drop this part</p>")
	out, code := runCLI(t, nil, "conf", "edit", p, "--old", " this part", "--new", "")
	if code != exitOK {
		t.Fatalf("edit: exit %d (stdout=%q)", code, out)
	}
	after, _ := os.ReadFile(p)
	if string(after) != "<p>drop</p>" {
		t.Errorf("file = %q", after)
	}
}

func TestConfEdit_NonCSFFileSkipsValidation(t *testing.T) {
	p := writeEditFixture(t, "notes.txt", "plain text body")
	out, code := runCLI(t, nil, "conf", "edit", p, "--old", "plain", "--new", "simple")
	if code != exitOK {
		t.Fatalf("edit: exit %d", code)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(out), &res)
	if _, has := res["csf_ok"]; has {
		t.Error("csf_ok must be absent for non-.csf files")
	}
}

// Agent Write tools terminate files with \n; --old-file/--new-file strip
// exactly one so the needle matches single-line CSF without ritual.
func TestConfEdit_FileFlagsStripOneTrailingNewline(t *testing.T) {
	p := writeEditFixture(t, "page.csf", "<p>alpha beta</p>")
	oldF := writeEditFixture(t, "old.txt", "alpha\n")
	newF := writeEditFixture(t, "new.txt", "gamma\n")
	if _, code := runCLI(t, nil, "conf", "edit", p, "--old-file", oldF, "--new-file", newF); code != exitOK {
		t.Fatalf("edit: exit %d", code)
	}
	after, _ := os.ReadFile(p)
	if string(after) != "<p>gamma beta</p>" {
		t.Errorf("file = %q", after)
	}
}
