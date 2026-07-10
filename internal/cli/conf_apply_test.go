package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mdmerge"
	"github.com/isukharev/atl/internal/mirror"
)

const applyFixtureCSF = `<h1>Intro</h1><p>Hello world.</p>` +
	`<ac:structured-macro ac:name="toc"/>`

// scaffoldApplyPage builds a minimal mirrored page and returns the .md path.
func scaffoldApplyPage(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "SP", "page")
	if err := os.MkdirAll(filepath.Join(root, ".atl", "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	croot, err := csf.Parse([]byte(applyFixtureCSF))
	if err != nil {
		t.Fatal(err)
	}
	refs := fragment.Extract(croot)
	md := mirror.RenderMarkdownOpts(croot, refs, mirror.MDViewOpts{})
	metaJSON := `{"id":"777","title":"page","version":1,"content_hash":"` + mirror.Hash([]byte(applyFixtureCSF)) + `"}`
	for name, b := range map[string][]byte{
		"page.csf":       []byte(applyFixtureCSF),
		"page.md":        md,
		"page.meta.json": []byte(metaJSON),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".atl", "base", "777.csf"), []byte(applyFixtureCSF), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "page.md")
}

func TestConfApply_EditFlow(t *testing.T) {
	mdPath := scaffoldApplyPage(t)
	md, _ := os.ReadFile(mdPath)
	os.WriteFile(mdPath, []byte(strings.Replace(string(md), "Hello world.", "Hello CLI.", 1)), 0o644)

	out, code := runCLI(t, nil, "conf", "apply", mdPath)
	if code != exitOK {
		t.Fatalf("apply: exit %d (stdout=%q)", code, out)
	}
	var res struct {
		DryRun bool `json:"dry_run"`
		Wrote  bool `json:"wrote"`
		CsfOK  bool `json:"csf_ok"`
		Report struct {
			Unchanged int `json:"unchanged"`
			Converted int `json:"converted"`
			Removed   int `json:"removed"`
		} `json:"report"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if !res.Wrote || !res.CsfOK || res.Report.Converted != 1 || res.Report.Unchanged != 2 {
		t.Errorf("result = %+v", res)
	}
	after, _ := os.ReadFile(strings.TrimSuffix(mdPath, ".md") + ".csf")
	if !strings.Contains(string(after), "<p>Hello CLI.</p>") || !strings.Contains(string(after), `ac:name="toc"`) {
		t.Errorf("csf = %q", after)
	}
}

func TestConfApply_FragmentLossIsExit8(t *testing.T) {
	mdPath := scaffoldApplyPage(t)
	md, _ := os.ReadFile(mdPath)
	os.WriteFile(mdPath, []byte(strings.Replace(string(md), "⟦table of contents⟧\n", "", 1)), 0o644)

	out, code := runCLI(t, nil, "conf", "apply", mdPath)
	if code != 8 {
		t.Fatalf("exit = %d, want 8 (stdout=%q)", code, out)
	}
	// Overridden, it proceeds and reports the loss.
	out, code = runCLI(t, nil, "conf", "apply", mdPath, "--allow-fragment-loss")
	if code != exitOK {
		t.Fatalf("with flag: exit %d (%q)", code, out)
	}
	if !strings.Contains(out, `"removed_fragments"`) {
		t.Errorf("loss not reported: %s", out)
	}
}

func TestConfApply_DivergedCSFIsExit8(t *testing.T) {
	mdPath := scaffoldApplyPage(t)
	csfPath := strings.TrimSuffix(mdPath, ".md") + ".csf"
	os.WriteFile(csfPath, []byte(applyFixtureCSF+"<p>direct</p>"), 0o644)

	_, code := runCLI(t, nil, "conf", "apply", mdPath)
	if code != 8 {
		t.Fatalf("exit = %d, want 8", code)
	}
}

// TestApplyText locks the `-o text` loss-review contract of `conf apply` on a
// dry-run with a dropped fragment, a validation problem, and a merged table:
// every section is exercised with a relative (non-volatile) path.
func TestApplyText(t *testing.T) {
	res := &app.ApplyResult{
		Path:    "SP/page/page.md",
		CSFPath: "SP/page/page.csf",
		DryRun:  true,
		CSFOK:   true,
		Report: &mdmerge.Report{
			Unchanged:        3,
			Moved:            1,
			Converted:        2,
			Removed:          1,
			MergedTables:     1,
			RemovedFragments: []domain.Ref{{Kind: domain.RefDrawio, Display: "diagram-1"}},
			Problems:         []csf.Problem{{Severity: "error", Line: 123, Message: "malformed element"}},
		},
	}
	want := strings.Join([]string{
		"dry-run: no files written",
		"blocks: 3 unchanged, 1 moved, 2 converted, 1 removed, 1 table merged",
		"removed fragments:",
		`  - drawio "diagram-1"`,
		"problems:",
		"  - error at 123: malformed element",
		"validation: ok",
		"next: restore the marker(s) in the .md, or re-run with --allow-fragment-loss to accept the loss",
	}, "\n")
	if got := applyText(res); got != want {
		t.Errorf("applyText:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestConfApply_TextDryRunLoss drives the CLI end-to-end: `-o text` on a dry-run
// that drops the toc fragment prints a readable review listing it.
func TestConfApply_TextDryRunLoss(t *testing.T) {
	mdPath := scaffoldApplyPage(t)
	md, _ := os.ReadFile(mdPath)
	os.WriteFile(mdPath, []byte(strings.Replace(string(md), "⟦table of contents⟧\n", "", 1)), 0o644)

	out, code := runCLI(t, nil, "conf", "apply", mdPath, "--allow-fragment-loss", "--dry-run", "-o", "text")
	if code != exitOK {
		t.Fatalf("exit %d (stdout=%q)", code, out)
	}
	for _, want := range []string{
		"dry-run: no files written",
		"removed fragments:",
		"validation: ok",
		"next: restore the marker(s) in the .md",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
}

func TestConfApply_NotMdIsUsage(t *testing.T) {
	_, code := runCLI(t, nil, "conf", "apply", "page.csf")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestConfApply_UnpulledIsExit4(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "stray.md")
	os.WriteFile(mdPath, []byte("# x\n"), 0o644)
	_, code := runCLI(t, nil, "conf", "apply", mdPath, "--into", dir)
	if code != 4 {
		t.Fatalf("exit = %d, want 4", code)
	}
}
