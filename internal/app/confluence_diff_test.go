package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func writeDiffPage(t *testing.T, root, id, slug, body string) string {
	t.Helper()
	m := mirror.New(root)
	dir := filepath.Join(root, "DOC")
	if err := m.Write(dir, slug, &domain.Resource{ID: id, Title: "Page " + id, SpaceKey: "DOC", Version: 3, Body: []byte(body)}, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return filepath.Join(root, "DOC", slug+".csf")
}

func TestConfluenceDiffReportsSemanticAndByteEvidence(t *testing.T) {
	root := t.TempDir()
	path := writeDiffPage(t, root, "101", "one", `<h2>Plan</h2><p a="1" b="2">Old</p><ac:structured-macro ac:name="info"/>`)
	if err := os.WriteFile(path, []byte(`<h2>Plan</h2><p b="2" a="1">New</p><ac:structured-macro ac:name="warning"/>`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DiffConfluenceMirror(path, root)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.Complete || result.Summary.Modified != 1 || len(result.Pages) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	page := result.Pages[0]
	if !page.SemanticChanged || page.ByteOnly || page.ByteEvidence == nil || len(page.Blocks) != 3 {
		t.Fatalf("semantic diff not reported: %+v", page)
	}
	if len(page.Features) != 2 || page.Features[0].Kind != "macro" {
		t.Fatalf("feature deltas = %+v", page.Features)
	}
}

func TestConfluenceDiffRecognizesByteOnlyChange(t *testing.T) {
	root := t.TempDir()
	path := writeDiffPage(t, root, "102", "two", `<p a="1" b="2">Same</p>`)
	if err := os.WriteFile(path, []byte(`<p b="2" a="1">Same</p>`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DiffConfluenceMirror(path, root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pages[0].ByteOnly || result.Pages[0].SemanticChanged || len(result.Pages[0].Blocks) != 0 {
		t.Fatalf("expected byte-only diff, got %+v", result.Pages[0])
	}
}

func TestConfluenceDiffCapturesNonRenderingDocumentChange(t *testing.T) {
	root := t.TempDir()
	path := writeDiffPage(t, root, "103", "three", `<p>Same</p>`)
	if err := os.WriteFile(path, []byte(`<p>Same</p><p></p>`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DiffConfluenceMirror(path, root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Pages[0].SemanticChanged || len(result.Pages[0].Blocks) != 1 || result.Pages[0].Blocks[0].Kind != "document" {
		t.Fatalf("expected document-level semantic change, got %+v", result.Pages[0])
	}
}

func TestConfluenceDiffAggregatesLinkAndFragmentDeltas(t *testing.T) {
	root := t.TempDir()
	path := writeDiffPage(t, root, "105", "links", `<p><ac:link><ri:page ri:content-title="Guide"/></ac:link></p>`)
	if err := os.WriteFile(path, []byte(`<p>No link</p>`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DiffConfluenceMirror(path, root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"link/storage": false, "fragment/page-link": false}
	for _, delta := range result.Pages[0].Features {
		want[delta.Kind+"/"+delta.Name] = true
	}
	for key, found := range want {
		if !found {
			t.Fatalf("missing %s in %+v", key, result.Pages[0].Features)
		}
	}
}

func TestConfluenceDiffRejectsTargetOutsideExplicitRoot(t *testing.T) {
	root := t.TempDir()
	outside := writeDiffPage(t, t.TempDir(), "104", "outside", `<p>x</p>`)
	if result, err := DiffConfluenceMirror(outside, root); err == nil || result != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestConfluenceDiffReportsRemovedMalformedAndMissingBaseline(t *testing.T) {
	root := t.TempDir()
	removed := writeDiffPage(t, root, "201", "removed", `<p>old</p>`)
	malformed := writeDiffPage(t, root, "202", "malformed", `<p>old</p>`)
	missing := writeDiffPage(t, root, "203", "missing", `<p>old</p>`)
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(malformed, []byte(`<p>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".atl", "base", "203.csf")); err != nil {
		t.Fatal(err)
	}

	result, err := DiffConfluenceMirror(root, root)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.Complete || result.Summary.Removed != 1 || result.Summary.Malformed != 1 || result.Summary.MissingBaseline != 1 {
		t.Fatalf("unexpected summary: %+v", result)
	}
	if len(result.Pages) != 3 || result.Pages[0].ID != "202" || result.Pages[1].ID != "203" || result.Pages[2].ID != "201" {
		// Current files sort before the absent tracked path because of their slugs.
		t.Fatalf("unexpected deterministic page order: %+v", result.Pages)
	}
	_ = missing
}

func TestConfluenceDiffReturnsStructuredUnreadableEntry(t *testing.T) {
	root := t.TempDir()
	path := writeDiffPage(t, root, "301", "broken", `<p>old</p>`)
	meta := strings.TrimSuffix(path, ".csf") + ".meta.json"
	if err := os.WriteFile(meta, []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DiffConfluenceMirror(root, root)
	if err == nil || result == nil || result.Complete || result.Summary.Unreadable != 1 || result.Pages[0].State != "unreadable" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestConfluenceDiffRejectsCorruptBaselineHash(t *testing.T) {
	root := t.TempDir()
	path := writeDiffPage(t, root, "302", "corrupt-base", `<p>old</p>`)
	if err := os.WriteFile(filepath.Join(root, ".atl", "base", "302.csf"), []byte(`<p>other</p>`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := DiffConfluenceMirror(path, root)
	if err == nil || result == nil || result.Complete || result.Summary.Unreadable != 1 || result.Pages[0].State != "unreadable" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestConfluenceDiffMarkdownEscapesTableCells(t *testing.T) {
	md := ConfluenceDiffMarkdown(&ConfluenceDiffResult{Complete: true, Pages: []ConfluencePageDiff{{State: "modified", Title: "A | B", Path: "DOC/a.csf"}}})
	if want := "A \\| B"; !strings.Contains(md, want) {
		t.Fatalf("markdown = %q, want %q", md, want)
	}
}
