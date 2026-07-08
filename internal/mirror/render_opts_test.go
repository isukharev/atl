package mirror

import (
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

func parseNode(t *testing.T, snippet string) *csf.Node {
	t.Helper()
	root, err := csf.Parse([]byte(snippet))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return root
}

// A zero MDViewOpts must be byte-identical to RenderMarkdown so existing
// default-profile mirrors are unaffected.
func TestRenderMarkdownOptsZeroIsIdentical(t *testing.T) {
	snippet := "<h1>Title</h1><p>Body text.</p>"
	root := parseNode(t, snippet)
	base := string(RenderMarkdown(root, nil))
	opt := string(RenderMarkdownOpts(root, nil, MDViewOpts{}))
	if base != opt {
		t.Errorf("zero opts differs from RenderMarkdown:\n base=%q\n opt=%q", base, opt)
	}
}

func TestRenderMarkdownOptsFrontmatterAndComments(t *testing.T) {
	root := parseNode(t, "<p>Body text.</p>")
	out := string(RenderMarkdownOpts(root, nil, MDViewOpts{
		Frontmatter: &PageFrontmatter{Title: "My Page", Space: "DOCS", Version: 3, Labels: []string{"a", "b"}},
		Comments:    []domain.Comment{{Author: "alice", Created: "2026-01-01", Body: "nice"}},
	}))
	if !strings.HasPrefix(out, "---\ntitle: My Page\nspace: DOCS\nversion: 3\nlabels: [a, b]\n---\n") {
		t.Errorf("frontmatter block wrong:\n%s", out)
	}
	if !strings.Contains(out, "Body text.") {
		t.Errorf("body missing:\n%s", out)
	}
	if !strings.Contains(out, "## Comments\n\n**alice** (2026-01-01):\n\nnice") {
		t.Errorf("comments section wrong:\n%s", out)
	}
}

// A title with YAML-significant characters is quoted.
func TestRenderMarkdownOptsFrontmatterYAMLEscape(t *testing.T) {
	root := parseNode(t, "<p>x</p>")
	out := string(RenderMarkdownOpts(root, nil, MDViewOpts{
		Frontmatter: &PageFrontmatter{Title: `Plan: Q1 #1`, Space: "S", Version: 1},
	}))
	if !strings.Contains(out, `title: "Plan: Q1 #1"`) {
		t.Errorf("title not YAML-escaped:\n%s", out)
	}
}
