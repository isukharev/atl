package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mirror"
)

const applyPage = `<h1>Intro</h1><p>Hello world.</p>` +
	`<ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">AB-1</ac:parameter></ac:structured-macro>`

// scaffoldPage lays out a minimal mirrored page: .csf, .md, .meta.json and the
// pristine base under .atl/base/.
func scaffoldPage(t *testing.T, body string) (rootDir, mdPath string) {
	t.Helper()
	rootDir = t.TempDir()
	dir := filepath.Join(rootDir, "SP", "page")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	croot, err := csf.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	refs := fragment.Extract(croot)
	md := mirror.RenderMarkdownOpts(croot, refs, mirror.MDViewOpts{})
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("page.csf", []byte(body))
	write("page.md", md)
	meta, _ := json.Marshal(mirror.Meta{ID: "4242", Title: "page", Version: 3, Hash: mirror.Hash([]byte(body)), Refs: refs})
	write("page.meta.json", meta)
	baseDir := filepath.Join(rootDir, ".atl", "base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "4242.csf"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return rootDir, filepath.Join(dir, "page.md")
}

// A corrupt sidecar surfaces as its own actionable ErrCheckFailed (exit 8),
// not as "not a mirrored page" (exit 4) — the page IS mirrored, and a re-pull
// hint would misdirect.
func TestApplyCorruptSidecarNotMislabeled(t *testing.T) {
	rootDir, mdPath := scaffoldPage(t, applyPage)
	if err := os.WriteFile(filepath.Join(rootDir, ".atl", "state.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(mdPath, ApplyOpts{Into: rootDir})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("corrupt sidecar error = %v, want ErrCheckFailed", err)
	}
	if errors.Is(err, domain.ErrNotFound) || strings.Contains(err.Error(), "is this a mirrored page") {
		t.Errorf("corruption mislabeled as not-mirrored: %v", err)
	}
	if !strings.Contains(err.Error(), "corrupt mirror sidecar") {
		t.Errorf("error lost the actionable corruption text: %v", err)
	}
}

// Atomic refresh replaces a read-only derived view rather than treating its
// old inode mode as a write failure.
func TestApplyRefreshReplacesReadOnlyView(t *testing.T) {
	_, mdPath := scaffoldPage(t, applyPage)
	md, _ := os.ReadFile(mdPath)
	edited := strings.Replace(string(md), "Hello world.", "Hello edited world.", 1)
	if err := os.WriteFile(mdPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	// The root-contained atomic writer creates a fresh inode and renames it over
	// this read-only derived view.
	if err := os.Chmod(mdPath, 0o444); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(mdPath, ApplyOpts{})
	if err != nil {
		t.Fatalf("apply must succeed when only the .md refresh fails: %v", err)
	}
	if !res.Wrote {
		t.Error("wrote=false despite a successful merge")
	}
	if res.Warning != "" {
		t.Errorf("warning = %q, want none", res.Warning)
	}
	// The merged edit reached the .csf even though the .md refresh failed.
	csfBytes, _ := os.ReadFile(strings.TrimSuffix(mdPath, ".md") + ".csf")
	if !strings.Contains(string(csfBytes), "Hello edited world.") {
		t.Errorf("merged edit missing from .csf: %q", csfBytes)
	}
	mdBytes, _ := os.ReadFile(mdPath)
	if !strings.Contains(string(mdBytes), "Hello edited world.") {
		t.Errorf("refreshed edit missing from .md: %q", mdBytes)
	}
}

func TestApplyEndToEnd(t *testing.T) {
	_, mdPath := scaffoldPage(t, applyPage)
	md, _ := os.ReadFile(mdPath)
	edited := strings.Replace(string(md), "Hello world.", "Hello edited world.", 1)
	if err := os.WriteFile(mdPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Apply(mdPath, ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Wrote || !res.CSFOK {
		t.Fatalf("result = %+v", res)
	}
	csfNow, _ := os.ReadFile(res.CSFPath)
	if !strings.Contains(string(csfNow), "<p>Hello edited world.</p>") {
		t.Fatalf("edit not applied: %s", csfNow)
	}
	if !strings.Contains(string(csfNow), `ac:name="jira"`) {
		t.Fatalf("macro lost: %s", csfNow)
	}
	// The md view is renormalized from the merged body.
	mdNow, _ := os.ReadFile(mdPath)
	if !strings.Contains(string(mdNow), "Hello edited world.") {
		t.Fatalf("md not regenerated: %s", mdNow)
	}
}

func TestApplyDryRunWritesNothing(t *testing.T) {
	_, mdPath := scaffoldPage(t, applyPage)
	md, _ := os.ReadFile(mdPath)
	edited := strings.Replace(string(md), "Hello", "Goodbye", 1)
	os.WriteFile(mdPath, []byte(edited), 0o644)

	res, err := Apply(mdPath, ApplyOpts{DryRun: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Wrote {
		t.Fatal("dry run wrote")
	}
	csfNow, _ := os.ReadFile(res.CSFPath)
	if string(csfNow) != applyPage {
		t.Fatal("dry run modified the .csf")
	}
}

func TestApplyRejectsUnsupportedConfluenceViewFormatsBeforeWrite(t *testing.T) {
	tests := []struct {
		name string
		edit func(string) string
		want string
	}{
		{name: "legacy body only", edit: func(md string) string {
			return strings.TrimPrefix(md, mirror.ConfluenceDocumentMarker+"\n"+mirror.ConfluenceBodyMarker+"\n")
		}, want: "conf render"},
		{name: "future", edit: func(md string) string {
			return strings.Replace(md, mirror.ConfluenceDocumentMarker, "<!-- atl:document confluence-page v99 -->", 1)
		}, want: "update atl"},
		{name: "legacy yaml", edit: func(md string) string {
			return "---\ntitle: old\n---\n\n" + md
		}, want: "conf render"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, mdPath := scaffoldPage(t, applyPage)
			md := tt.edit(mustReadFile(t, mdPath))
			if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Apply(mdPath, ApplyOpts{Into: root})
			if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("format refusal = %v", err)
			}
			if got := mustReadFile(t, strings.TrimSuffix(mdPath, ".md")+".csf"); got != applyPage {
				t.Fatalf("CSF changed on format refusal: %q", got)
			}
		})
	}
}

func TestApplyConfluenceViewCRLFAndReservedMarker(t *testing.T) {
	root, mdPath := scaffoldPage(t, applyPage)
	md := strings.ReplaceAll(mustReadFile(t, mdPath), "\n", "\r\n")
	md = strings.Replace(md, "Hello world.", "Hello CRLF.", 1)
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(mdPath, ApplyOpts{Into: root}); err != nil {
		t.Fatalf("CRLF apply: %v", err)
	}
	if !strings.Contains(mustReadFile(t, strings.TrimSuffix(mdPath, ".md")+".csf"), "Hello CRLF.") {
		t.Fatal("CRLF edit was not applied")
	}

	root, mdPath = scaffoldPage(t, applyPage)
	md = strings.Replace(mustReadFile(t, mdPath), "Hello world.", "Hello world.\n<!-- atl:section comments readonly -->", 1)
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(mdPath, ApplyOpts{Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved marker refusal = %v", err)
	}
	if got := mustReadFile(t, strings.TrimSuffix(mdPath, ".md")+".csf"); got != applyPage {
		t.Fatal("CSF changed on reserved-marker refusal")
	}
}

func TestApplyFailsClosedWhenPristineCSFNoLongerParses(t *testing.T) {
	root, mdPath := scaffoldPage(t, applyPage)
	broken := "<broken>"
	if err := os.WriteFile(filepath.Join(root, ".atl", "base", "4242.csf"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	csfPath := strings.TrimSuffix(mdPath, ".md") + ".csf"
	if err := os.WriteFile(csfPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(mdPath, ApplyOpts{Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "no longer parses") {
		t.Fatalf("parse refusal = %v", err)
	}
	if got := mustReadFile(t, csfPath); got != broken {
		t.Fatal("CSF changed after pristine parse refusal")
	}
}

func TestApplyRefusesDivergedCSF(t *testing.T) {
	_, mdPath := scaffoldPage(t, applyPage)
	csfPath := strings.TrimSuffix(mdPath, ".md") + ".csf"
	os.WriteFile(csfPath, []byte(applyPage+"<p>direct edit</p>"), 0o644)

	_, err := Apply(mdPath, ApplyOpts{})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("want ErrCheckFailed, got %v", err)
	}
}

func TestApplyRefusesFragmentLossWithoutFlag(t *testing.T) {
	_, mdPath := scaffoldPage(t, applyPage)
	md, _ := os.ReadFile(mdPath)
	edited := strings.Replace(string(md), "[AB-1](jira:AB-1)", "", 1)
	os.WriteFile(mdPath, []byte(edited), 0o644)

	_, err := Apply(mdPath, ApplyOpts{})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("want ErrCheckFailed, got %v", err)
	}
	res, err := Apply(mdPath, ApplyOpts{AllowFragmentLoss: true})
	if err != nil {
		t.Fatalf("with flag: %v", err)
	}
	if len(res.Report.RemovedFragments) == 0 {
		t.Fatalf("report misses removed fragments: %+v", res.Report)
	}
}

// Regression: a relative .md path from inside the mirror must still find the
// mirror root (the walk-up loop terminates immediately on ".").
func TestApplyRelativePathFromMirrorCwd(t *testing.T) {
	_, mdPath := scaffoldPage(t, applyPage)
	md, _ := os.ReadFile(mdPath)
	edited := strings.Replace(string(md), "Hello world.", "Hello relative.", 1)
	os.WriteFile(mdPath, []byte(edited), 0o644)

	t.Chdir(filepath.Dir(mdPath))
	res, err := Apply("page.md", ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply with relative path: %v", err)
	}
	if !res.Wrote {
		t.Fatalf("result = %+v", res)
	}
}

// A styled table (the shape the Confluence editor saves) is merged cell-wise
// through apply: the edited cell changes, every other table byte survives.
func TestApplyStyledTableCellEdit(t *testing.T) {
	table := `<table><tbody><tr><th>Name</th><th>State</th></tr>` +
		`<tr><td><div class="content-wrapper"><p>alpha</p></div></td><td style="text-align: center;">?</td></tr>` +
		`</tbody></table>`
	_, mdPath := scaffoldPage(t, applyPage+table)
	md, _ := os.ReadFile(mdPath)
	edited := strings.Replace(string(md), "| alpha | ? |", "| alpha | yes |", 1)
	if err := os.WriteFile(mdPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(mdPath, ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Report.MergedTables != 1 {
		t.Fatalf("report = %+v", res.Report)
	}
	csfNow, _ := os.ReadFile(res.CSFPath)
	want := applyPage + strings.Replace(table, `>?<`, `>yes<`, 1)
	if string(csfNow) != want {
		t.Fatalf("csf diverges:\n got %s\nwant %s", csfNow, want)
	}
}

func TestApplyRequiresPulledPage(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "stray.md")
	os.WriteFile(mdPath, []byte("# x\n"), 0o644)
	_, err := Apply(mdPath, ApplyOpts{Into: dir})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := Apply(filepath.Join(dir, "x.csf"), ApplyOpts{}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("non-.md arg: want ErrUsage, got %v", err)
	}
}
