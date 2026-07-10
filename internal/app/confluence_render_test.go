package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// seedConfMirror writes one page (.csf + .md + .meta.json + base + sidecar) via
// the mirror engine and returns the mirror root and page dir/slug.
func seedConfMirror(t *testing.T, comments []domain.Comment) (root, dir, slug string) {
	t.Helper()
	root = t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	page := &domain.Resource{
		ID: "1001", Title: "My Page", SpaceKey: "DOCS", Version: 3,
		Labels: []string{"a", "b"},
		Body:   []byte("<p>Body text.</p>"),
	}
	pdir, pslug, err := m.ClaimPageDir(page.SpaceKey, page.Ancestors, page.Title, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	if comments != nil {
		if err := b.WriteComments(pdir, pslug, page, nil, comments, false, mirror.MDViewOpts{}); err != nil {
			t.Fatal(err)
		}
	} else if err := b.Write(pdir, pslug, page, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	return root, pdir, pslug
}

// Default profile render leaves the .md byte-identical to the body-only view.
func TestConfRenderDefaultByteIdentical(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	before := mustReadFile(t, filepath.Join(dir, slug+".md"))
	svc := NewConfluenceRenderer(&config.Config{})
	if _, err := svc.Render(root, config.RenderService{}); err != nil {
		t.Fatal(err)
	}
	after := mustReadFile(t, filepath.Join(dir, slug+".md"))
	if before != after {
		t.Errorf("default render changed the .md:\n before=%q\n after=%q", before, after)
	}
	if strings.Contains(after, "---\ntitle:") {
		t.Error("default profile must not add frontmatter")
	}
}

// Full profile adds typed page fields and, when the sidecar is present, a Comments
// section.
func TestConfRenderFullAddsPageFieldsAndComments(t *testing.T) {
	root, dir, slug := seedConfMirror(t, []domain.Comment{{Author: "alice", Created: "2026-01-01", Body: "hi"}})
	svc := NewConfluenceRenderer(&config.Config{})
	if _, err := svc.Render(root, config.RenderService{Profile: "full"}); err != nil {
		t.Fatal(err)
	}
	md := mustReadFile(t, filepath.Join(dir, slug+".md"))
	if !strings.HasPrefix(md, mirror.ConfluenceDocumentMarker+"\n"+mirror.ConfluencePageFieldsMarker+"\n# Metadata\n") ||
		!strings.Contains(md, "| Title | My Page |") || !strings.Contains(md, "| Labels | a, b |") {
		t.Errorf("full render missing typed page fields:\n%s", md)
	}
	if !strings.Contains(md, "## Comments") || !strings.Contains(md, "**alice** (2026-01-01):") {
		t.Errorf("full render missing comments section:\n%s", md)
	}
}

// Full profile with no comments sidecar renders page fields but silently skips
// the Comments section.
func TestConfRenderFullNoSidecarSkipsComments(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	svc := NewConfluenceRenderer(&config.Config{})
	if _, err := svc.Render(root, config.RenderService{Profile: "full"}); err != nil {
		t.Fatal(err)
	}
	md := mustReadFile(t, filepath.Join(dir, slug+".md"))
	if !strings.Contains(md, "| Title | My Page |") {
		t.Errorf("expected page metadata table:\n%s", md)
	}
	if strings.Contains(md, "## Comments") {
		t.Errorf("no sidecar → no Comments section:\n%s", md)
	}
}

func TestConfRenderUnknownRestrictionIsExplicitAndWarned(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	svc := NewConfluenceRenderer(&config.Config{})
	res, err := svc.Render(root, config.RenderService{
		Profile: "minimal", Include: []string{SecPageFields},
		PageFields: []config.ConfluenceFieldView{{ID: "restricted", ShowEmpty: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "re-pull") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	md := mustReadFile(t, filepath.Join(dir, slug+".md"))
	if !strings.Contains(md, "| Restricted | Unknown — re-pull required |") {
		t.Fatalf("unknown restriction was presented as a fact:\n%s", md)
	}
}

// Render must not touch the substrate: the .csf and .meta.json stay byte-stable
// so conf status remains clean.
func TestConfRenderLeavesSubstrateUntouched(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	csfBefore := mustReadFile(t, filepath.Join(dir, slug+".csf"))
	metaBefore := mustReadFile(t, filepath.Join(dir, slug+".meta.json"))
	svc := NewConfluenceRenderer(&config.Config{})
	if _, err := svc.Render(root, config.RenderService{Profile: "full"}); err != nil {
		t.Fatal(err)
	}
	if mustReadFile(t, filepath.Join(dir, slug+".csf")) != csfBefore {
		t.Error(".csf changed")
	}
	if mustReadFile(t, filepath.Join(dir, slug+".meta.json")) != metaBefore {
		t.Error(".meta.json changed")
	}
	// Status stays clean (no local edit reported).
	entries, err := (&ConfluenceService{}).Status(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.LocallyEdited {
			t.Errorf("page %s reported locally edited after render", e.ID)
		}
	}
}
