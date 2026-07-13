package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func TestConfDiffIsOfflineAndSupportsMarkdown(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	page := &domain.Resource{ID: "100", Title: "Example", SpaceKey: "DOC", Version: 2, Body: []byte("<p>old</p>")}
	dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, slug+".csf"), []byte("<p>new</p>"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := runCLI(t, nil, "--read-only", "conf", "diff", root, "-o", "text")
	if code != exitOK || !strings.Contains(out, "# Confluence mirror diff") || !strings.Contains(out, "modified") {
		t.Fatalf("exit=%d output=%q", code, out)
	}
	jsonOut, code := runCLI(t, nil, "--read-only", "conf", "diff", root)
	if code != exitOK {
		t.Fatalf("json exit=%d output=%q", code, jsonOut)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ReplaceAll(jsonOut, canonicalRoot, "<ROOT>")
	normalized = strings.ReplaceAll(normalized, root, "<ROOT>")
	assertGolden(t, "conf_diff.json", []byte(normalized))
}
