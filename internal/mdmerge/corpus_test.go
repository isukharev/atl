package mdmerge

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mirror"
)

// TestCorpusIdentity replays a no-edit merge over every corpus page: rendering
// a page to markdown and applying that markdown unchanged must reproduce the
// page byte-identically with a pure-unchanged report. This is the merge's
// central safety property on real content. Skipped without a corpus (see
// docs/csf-markdown-testing.md).
func TestCorpusIdentity(t *testing.T) {
	dir := os.Getenv("ATL_CSF_CORPUS")
	if dir == "" {
		if _, err := os.Stat(filepath.Join("..", "..", ".csf-corpus")); err == nil {
			dir = filepath.Join("..", "..", ".csf-corpus")
		}
	}
	if dir == "" {
		t.Skip("no CSF corpus; set ATL_CSF_CORPUS=<dir>")
	}
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".csf") {
			files = append(files, p)
		}
		return nil
	})
	if len(files) == 0 {
		t.Skip("corpus empty")
	}
	pages, identical := 0, 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		root, err := csf.Parse(raw)
		if err != nil {
			continue // malformed corpus entry; not this test's concern
		}
		pages++
		refs := fragment.Extract(root)
		md := string(mirror.RenderMarkdown(root, refs))
		out, rep, err := Merge(raw, refs, md, Options{})
		if err != nil {
			t.Errorf("%s: identity merge failed: %v", filepath.Base(f), err)
			continue
		}
		if !bytes.Equal(out, raw) {
			t.Errorf("%s: identity merge not byte-identical (unchanged=%d converted=%d removed=%d moved=%d)",
				filepath.Base(f), rep.Unchanged, rep.Converted, rep.Removed, rep.Moved)
			continue
		}
		if rep.Converted != 0 || rep.Removed != 0 || rep.Moved != 0 {
			t.Errorf("%s: identity merge not pure-unchanged: %+v", filepath.Base(f), rep)
			continue
		}
		identical++
	}
	t.Logf("corpus identity: %d/%d pages byte-identical", identical, pages)
}
