package mirror

import (
	"os"
	"path/filepath"
	"testing"
)

// The exported ext-aware base store round-trips a Jira `.wiki` body and keeps
// the `.csf` and `.wiki` copies of the same id in distinct files, so a
// Confluence page and a Jira issue that happen to share an id never clobber
// each other's base.
func TestBaseExtRoundTripAndIsolation(t *testing.T) {
	root := t.TempDir()
	m := New(root)

	const id = "PROJ-1"
	if err := m.SaveBaseExt(id, []byte("h1. wiki body"), ".wiki"); err != nil {
		t.Fatalf("SaveBaseExt(.wiki): %v", err)
	}
	if err := m.saveBase(id, []byte("<p>csf body</p>")); err != nil {
		t.Fatalf("saveBase(.csf): %v", err)
	}

	if got, ok := m.BaseBodyExt(id, ".wiki"); !ok || string(got) != "h1. wiki body" {
		t.Fatalf("BaseBodyExt(.wiki) = %q ok=%v, want the wiki body", got, ok)
	}
	if got, ok := m.BaseBody(id); !ok || string(got) != "<p>csf body</p>" {
		t.Fatalf("BaseBody(.csf) = %q ok=%v, want the csf body", got, ok)
	}

	base := filepath.Join(root, ".atl", "base")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read base dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two distinct base files (.csf + .wiki), got %d", len(entries))
	}
}

// A missing `.wiki` base is a clean "not present", never an error — Status/Push
// rely on this to treat a not-yet-based issue as un-driftable.
func TestBaseBodyExtMissing(t *testing.T) {
	m := New(t.TempDir())
	if _, ok := m.BaseBodyExt("nope", ".wiki"); ok {
		t.Fatal("BaseBodyExt on a missing id must report ok=false")
	}
}

// A hostile/compromised backend controls the issue key. The ext-aware base
// store must never write or read outside <root>/.atl/base.
func TestSaveBaseExtCannotEscapeRoot(t *testing.T) {
	root := t.TempDir()
	m := New(root)

	const key = "../../../../tmp/atl-evil"
	if err := m.SaveBaseExt(key, []byte("payload"), ".wiki"); err != nil {
		t.Fatalf("SaveBaseExt returned %v", err)
	}
	escaped := filepath.Clean(filepath.Join(root, "..", "..", "..", "..", "tmp", "atl-evil.wiki"))
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("traversal key escaped the root: wrote %s", escaped)
	}
	base := filepath.Join(root, ".atl", "base")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read base dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one sanitized base file, got %d", len(entries))
	}
	if got, ok := m.BaseBodyExt(key, ".wiki"); !ok || string(got) != "payload" {
		t.Fatalf("BaseBodyExt round-trip failed: ok=%v got=%q", ok, got)
	}
}
