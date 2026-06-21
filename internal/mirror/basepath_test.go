package mirror

import (
	"os"
	"path/filepath"
	"testing"
)

// A hostile/compromised backend controls the content id. It must never let the
// pristine-base store write or read outside <root>/.atl/base.
func TestSaveBaseCannotEscapeRoot(t *testing.T) {
	root := t.TempDir()
	m := New(root)

	const id = "../../../../tmp/atl-evil"
	if err := m.saveBase(id, []byte("payload")); err != nil {
		t.Fatalf("saveBase returned %v", err)
	}

	// Nothing must be written outside the mirror root.
	escaped := filepath.Clean(filepath.Join(root, "..", "..", "..", "..", "tmp", "atl-evil.csf"))
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("traversal id escaped the root: wrote %s", escaped)
	}

	// Every file created stays under <root>/.atl/base.
	base := filepath.Join(root, ".atl", "base")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read base dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one sanitized base file, got %d", len(entries))
	}

	// Round-trip with the same id must return the same bytes.
	got, ok := m.BaseBody(id)
	if !ok || string(got) != "payload" {
		t.Fatalf("BaseBody round-trip failed: ok=%v got=%q", ok, got)
	}
}
