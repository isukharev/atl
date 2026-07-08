package mirror

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
)

// TestCorpusSweep is an optional coverage radar for the CSF→Markdown renderer.
// Point it at a directory of real Confluence storage-format pages and it renders
// every .csf, failing on a panic and reporting (with -v) any macro that still
// falls through to a generic ⟦macro NAME⟧ placeholder — your prioritized TODO for
// new coverage.
//
// It is skipped unless a corpus is present, so CI and the default `go test` are
// unaffected. The corpus is never committed (it holds real page content;
// .csf-corpus/ is gitignored). See docs/csf-markdown-testing.md for how to build
// one with `atl conf pull` and how to extend coverage.
//
//	ATL_CSF_CORPUS=/path/to/corpus go test ./internal/mirror/ -run Sweep -v
func TestCorpusSweep(t *testing.T) {
	dir := corpusDir()
	if dir == "" {
		t.Skip("no CSF corpus; set ATL_CSF_CORPUS=<dir> or create .csf-corpus/ (see docs/csf-markdown-testing.md)")
	}

	var files []string
	if err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".csf") {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk corpus %q: %v", dir, err)
	}
	if len(files) == 0 {
		t.Skipf("corpus %q has no .csf files", dir)
	}

	genericMacro := regexp.MustCompile(`⟦macro ([a-z0-9-]+)⟧`)
	unhandled := map[string]int{}
	rendered := 0

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("read %s: %v", f, err)
			continue
		}
		root, err := csf.Parse(raw)
		if err != nil {
			// A parse failure means no .md is written (best-effort); not a render bug.
			continue
		}
		md := renderNoPanic(t, f, root)
		rendered++
		for _, m := range genericMacro.FindAllStringSubmatch(md, -1) {
			unhandled[m[1]]++
		}
		// The conf-apply anchored path depends on prefix+body+suffix being
		// byte-identical to the decorated full view; assert it across every real
		// page so no body shape breaks the identity.
		refs := fragment.Extract(root)
		opts := MDViewOpts{
			Frontmatter: &PageFrontmatter{Title: "T", Space: "S", Version: 1},
			Comments:    []domain.Comment{{Author: "a", Created: "d", Body: "c"}},
		}
		want := string(RenderMarkdownOpts(root, refs, opts))
		p, bmid, sfx := RenderMarkdownViewParts(root, refs, opts)
		if p+bmid+sfx != want {
			t.Errorf("%s: RenderMarkdownViewParts concat != RenderMarkdownOpts", f)
		}
	}

	t.Logf("rendered %d/%d page(s) from %s", rendered, len(files), dir)
	if len(unhandled) > 0 {
		type kv struct {
			name string
			n    int
		}
		ranked := make([]kv, 0, len(unhandled))
		for k, v := range unhandled {
			ranked = append(ranked, kv{k, v})
		}
		sort.Slice(ranked, func(i, j int) bool { return ranked[i].n > ranked[j].n })
		var b strings.Builder
		for _, e := range ranked {
			fmt.Fprintf(&b, "\n  %-24s %d", e.name, e.n)
		}
		t.Logf("macros without a dedicated handler (candidates for new coverage):%s", b.String())
	}
}

// corpusDir resolves the CSF corpus directory: ATL_CSF_CORPUS if set, else the
// conventional gitignored .csf-corpus/ at the repo root (../../ from this package).
// Returns "" when no corpus is available.
func corpusDir() string {
	if d := os.Getenv("ATL_CSF_CORPUS"); d != "" {
		if isDir(d) {
			return d
		}
		return ""
	}
	if d := filepath.Join("..", "..", ".csf-corpus"); isDir(d) {
		return d
	}
	return ""
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// renderNoPanic renders a body, turning a renderer panic into a test failure
// scoped to the offending file rather than aborting the whole sweep.
func renderNoPanic(t *testing.T, file string, root *csf.Node) (md string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic rendering %s: %v", file, r)
		}
	}()
	return string(RenderMarkdown(root, fragment.Extract(root)))
}
