package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiffFragments(t *testing.T) {
	old := []byte(`<p>x</p><ac:structured-macro ac:name="drawio"><ac:parameter ac:name="diagramName">d</ac:parameter></ac:structured-macro><ac:image><ri:attachment ri:filename="a.png"/></ac:image>`)
	// new body removes the drawio, keeps the image.
	neu := []byte(`<p>x edited</p><ac:image><ri:attachment ri:filename="a.png"/></ac:image>`)
	removed, added := diffFragments(old, neu)
	if len(removed) != 1 || removed[0].Kind != "drawio" {
		t.Fatalf("expected 1 removed drawio, got %+v", removed)
	}
	if len(added) != 0 {
		t.Errorf("expected no additions, got %+v", added)
	}
}

func TestMirrorRootOf(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "ARCH", "page")
	os.MkdirAll(deep, 0o755)
	csf := filepath.Join(deep, "page.csf")
	os.WriteFile(csf, []byte("<p/>"), 0o644)
	if got := mirrorRootOf(csf); got != root {
		t.Errorf("mirrorRootOf = %q, want %q", got, root)
	}
}

func TestWithin(t *testing.T) {
	if !within("/a/b", "/a/b/c/d.csf") {
		t.Error("should be within")
	}
	if within("/a/b", "/a/x/d.csf") {
		t.Error("should not be within")
	}
}
