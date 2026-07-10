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

// fullViewComments is the fixture comment set embedded in a full-profile view.
var fullViewComments = []domain.Comment{
	{ID: "c1", Author: "Ann", Created: "2024-01-02", Body: "first note"},
}

// scaffoldFullPage lays out a mirrored page whose .md view was written under the
// FULL Confluence profile (YAML frontmatter + a "## Comments" section) and whose
// view state is recorded in the sidecar — the shape `conf pull --render-profile
// full` produces. It returns the mirror root, the .md path, and the exact full
// view bytes on disk.
func scaffoldFullPage(t *testing.T, body string) (rootDir, mdPath, fullMD string) {
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

	meta := mirror.Meta{ID: "4242", Title: "page", Space: "SP", Version: 3, Labels: []string{"x"}, Hash: mirror.Hash([]byte(body)), Refs: refs}
	page := &domain.Resource{Title: meta.Title, SpaceKey: meta.Space, Version: meta.Version, Labels: meta.Labels}

	rsFull := settingsFromViewState(mirror.ViewState{Sections: []string{SecComments, SecFrontmatter}})
	mdOpts := confMDViewOpts(rsFull, page, fullViewComments)
	full := mirror.RenderMarkdownOpts(croot, refs, mdOpts)
	fullMD = string(full)

	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("page.csf", []byte(body))
	write("page.md", full)
	mb, _ := json.Marshal(meta)
	write("page.meta.json", mb)
	cb, _ := json.Marshal(fullViewComments)
	write("page.comments.json", cb)

	baseDir := filepath.Join(rootDir, ".atl", "base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "4242.csf"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m := mirror.New(rootDir)
	if err := m.SaveViewStates(map[string]mirror.ViewState{"4242": {Sections: []string{SecComments, SecFrontmatter}}}); err != nil {
		t.Fatal(err)
	}
	return rootDir, filepath.Join(dir, "page.md"), fullMD
}

// The core regression for #166: an untouched FULL-profile view must not inject
// its frontmatter/comments decorations into the page body. The report shows zero
// converted/added blocks and the .csf stays byte-identical.
func TestApplyFullProfileUntouchedNoInjection(t *testing.T) {
	rootDir, mdPath, _ := scaffoldFullPage(t, applyPage)
	res, err := Apply(mdPath, ApplyOpts{Into: rootDir})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Report.Converted != 0 || res.Report.Moved != 0 || res.Report.Removed != 0 {
		t.Errorf("untouched full view should convert/move/remove nothing, got %+v", res.Report)
	}
	csfNow, _ := os.ReadFile(res.CSFPath)
	if string(csfNow) != applyPage {
		t.Fatalf(".csf not byte-identical after untouched full-profile apply:\n got=%s\nwant=%s", csfNow, applyPage)
	}
}

// A body edit under the full profile merges into the .csf and the refreshed .md
// keeps its full decorations (frontmatter + Comments).
func TestApplyFullProfileBodyEditMergesAndRefreshesFull(t *testing.T) {
	rootDir, mdPath, fullMD := scaffoldFullPage(t, applyPage)
	edited := strings.Replace(fullMD, "Hello world.", "Hello edited world.", 1)
	if edited == fullMD {
		t.Fatal("edit anchor not found in full view")
	}
	if err := os.WriteFile(mdPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(mdPath, ApplyOpts{Into: rootDir})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	csfNow, _ := os.ReadFile(res.CSFPath)
	if !strings.Contains(string(csfNow), "<p>Hello edited world.</p>") {
		t.Fatalf("body edit not merged: %s", csfNow)
	}
	mdNow, _ := os.ReadFile(mdPath)
	if !strings.HasPrefix(string(mdNow), mirror.ConfluenceDocumentMarker+"\n") || !strings.Contains(string(mdNow), mirror.ConfluenceCommentsMarker+"\n## Comments") {
		t.Fatalf("refreshed .md lost its full decorations:\n%s", mdNow)
	}
	if !strings.Contains(string(mdNow), "Hello edited world.") {
		t.Fatalf("refreshed .md missing the edit:\n%s", mdNow)
	}
}

// Editing the read-only YAML frontmatter is refused (exit 8) with a pointer at
// the page-metadata commands.
func TestApplyFullProfileFrontmatterEditRefused(t *testing.T) {
	rootDir, mdPath, fullMD := scaffoldFullPage(t, applyPage)
	edited := strings.Replace(fullMD, "version: 3", "version: 999", 1)
	if edited == fullMD {
		t.Fatal("frontmatter anchor not found")
	}
	if err := os.WriteFile(mdPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(mdPath, ApplyOpts{Into: rootDir})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err = %v, want ErrCheckFailed", err)
	}
	if !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("error should point at the frontmatter: %v", err)
	}
	csfNow, _ := os.ReadFile(strings.TrimSuffix(mdPath, ".md") + ".csf")
	if string(csfNow) != applyPage {
		t.Error(".csf modified on a frontmatter-edit refusal")
	}
}

// Editing the read-only "## Comments" section is refused (exit 8) with a pointer
// at `conf comment add`.
func TestApplyFullProfileCommentsEditRefused(t *testing.T) {
	rootDir, mdPath, fullMD := scaffoldFullPage(t, applyPage)
	edited := strings.Replace(fullMD, "first note", "hijacked note", 1)
	if edited == fullMD {
		t.Fatal("comment anchor not found")
	}
	if err := os.WriteFile(mdPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(mdPath, ApplyOpts{Into: rootDir})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("err = %v, want ErrCheckFailed", err)
	}
	if !strings.Contains(err.Error(), "Comments") {
		t.Errorf("error should point at the Comments section: %v", err)
	}
}
