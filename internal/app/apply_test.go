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
	md := mirror.RenderMarkdown(croot, refs)
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
