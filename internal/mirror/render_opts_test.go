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

// A zero MDViewOpts wraps the body renderer in the versioned document envelope.
func TestRenderMarkdownOptsZeroWrapsBody(t *testing.T) {
	snippet := "<h1>Title</h1><p>Body text.</p>"
	root := parseNode(t, snippet)
	base := string(RenderMarkdown(root, nil))
	opt := string(RenderMarkdownOpts(root, nil, MDViewOpts{}))
	if !strings.HasSuffix(opt, base) {
		t.Errorf("zero opts lost body:\n body=%q\n opt=%q", base, opt)
	}
	if !strings.HasPrefix(opt, ConfluenceDocumentMarker+"\n"+ConfluenceBodyMarker+"\n") {
		t.Fatalf("zero opts lacks versioned boundaries: %q", opt)
	}
}

func TestRenderMarkdownOptsMetadataAndComments(t *testing.T) {
	root := parseNode(t, "<p>Body text.</p>")
	out := string(RenderMarkdownOpts(root, nil, MDViewOpts{
		PageFields: []PageField{
			{ID: "title", Label: "Title", Values: []string{"My Page"}},
			{ID: "space", Label: "Space", Values: []string{"DOCS"}},
			{ID: "version", Label: "Version", Values: []string{"3"}},
		},
		Comments: []domain.Comment{{Author: "alice", Created: "2026-01-01", Body: "nice"}},
	}))
	wantPrefix := ConfluenceDocumentMarker + "\n" + ConfluencePageFieldsMarker + "\n# Metadata\n\n"
	if !strings.HasPrefix(out, wantPrefix) {
		t.Errorf("metadata block wrong:\n%s", out)
	}
	if !strings.Contains(out, "Body text.") {
		t.Errorf("body missing:\n%s", out)
	}
	if !strings.Contains(out, "# Content\n\nBody text.") {
		t.Errorf("content boundary missing:\n%s", out)
	}
	if !strings.Contains(out, "# Comments\n\n## Comment by alice (2026-01-01)\n\nnice") {
		t.Errorf("comments section wrong:\n%s", out)
	}
	if !strings.Contains(out, ConfluenceCommentsMarker+"\n# Comments") {
		t.Errorf("comments boundary missing:\n%s", out)
	}
}

func TestRenderCommentsMarkdownPreservesNativeFormattingAndNestsHeadings(t *testing.T) {
	got := string(RenderCommentsMarkdown([]domain.Comment{{
		Author: "Ada", Created: "2026-01-01", Body: "flattened fallback",
		BodyStorage: "<h1>Decision</h1><p><strong>Ship</strong> it.</p><ul><li>First</li><li>Second</li></ul>",
	}}))
	for _, want := range []string{
		"# Comments", "## Comment by Ada (2026-01-01)", "### Decision",
		"**Ship** it.", "- First", "- Second",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted comment missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "flattened fallback") {
		t.Fatalf("native comment formatting was not preferred:\n%s", got)
	}
}

func TestRenderCommentsMarkdownPreservesPastedMultilineCodeTable(t *testing.T) {
	storage := `<p>Run this:</p><table><tbody><tr><td><p><code>first<br/>second<br/>third</code></p></td></tr></tbody></table>`
	got := string(RenderCommentsMarkdown([]domain.Comment{{Author: "Ada", BodyStorage: storage}}))
	if !strings.Contains(got, "Run this:\n\n```\nfirst\nsecond\nthird\n```") {
		t.Fatalf("multiline comment code collapsed:\n%s", got)
	}
}

func TestRenderCommentsMarkdownDoesNotCollapseOrdinaryCodeTables(t *testing.T) {
	for _, storage := range []string{
		`<table><tbody><tr><td><p>Run <code>first<br/>second</code> now</p></td></tr></tbody></table>`,
		`<table><tbody><tr><td><p><code>inline only</code></p></td></tr></tbody></table>`,
	} {
		got := string(RenderCommentsMarkdown([]domain.Comment{{Author: "Ada", BodyStorage: storage}}))
		if strings.Contains(got, "```\n") {
			t.Fatalf("ordinary table was rewritten as a code fence:\n%s", got)
		}
		if strings.Contains(storage, "Run ") && (!strings.Contains(got, "Run") || !strings.Contains(got, "now")) {
			t.Fatalf("surrounding table prose was dropped:\n%s", got)
		}
	}
}

func TestRenderMarkdownOptsReadOnlyBody(t *testing.T) {
	root := parseNode(t, `<p>Hello</p>`)
	got := string(RenderMarkdownOpts(root, nil, MDViewOpts{ReadOnly: true}))
	wantPrefix := ConfluenceDocumentMarker + "\n" + ConfluenceBodyReadOnlyMarker + "\n"
	if !strings.HasPrefix(got, wantPrefix) || strings.Contains(got, ConfluenceBodyMarker) {
		t.Fatalf("read-only view marker mismatch:\n%s", got)
	}
}

func TestRenderMarkdownOptsTypedPageFieldsAreReadOnlyAndEscaped(t *testing.T) {
	root := parseNode(t, `<p>Body</p>`)
	out := string(RenderMarkdownOpts(root, nil, MDViewOpts{PageFields: []PageField{
		{ID: "title", Label: "Ti|tle", Values: []string{"<b>*Roadmap*</b>"}},
		{ID: "labels", Label: "Labels", Placement: "section", Values: []string{"- injected", "1. ordered", "---"}},
	}}))
	for _, want := range []string{
		ConfluencePageFieldsMarker, "# Metadata", "| Ti&#124;tle | &lt;b&gt;&#42;Roadmap&#42;&lt;/b&gt; |",
		"<!-- atl:section page-field.labels readonly -->", "- &#45; injected", "- 1&#46; ordered", "- &#45;--",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("page fields missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<b>") {
		t.Fatalf("raw server-controlled HTML survived:\n%s", out)
	}
}

// RenderMarkdownViewParts must satisfy the concatenation identity
// prefix+body+suffix == RenderMarkdownOpts across every opts shape, so conf
// apply can anchor-extract the editable body byte-for-byte.
func TestRenderMarkdownViewPartsConcatIdentity(t *testing.T) {
	fields := []PageField{{ID: "title", Label: "Title", Values: []string{"My Page"}}}
	cs := []domain.Comment{{Author: "alice", Created: "2026-01-01", Body: "nice"}}
	cases := []struct {
		name string
		body string
		opts MDViewOpts
	}{
		{"zero", "<h1>Title</h1><p>Body text.</p>", MDViewOpts{}},
		{"metadata-only", "<p>Body text.</p>", MDViewOpts{PageFields: fields}},
		{"comments-only", "<p>Body text.</p>", MDViewOpts{Comments: cs}},
		{"both", "<h1>T</h1><p>Body text.</p>", MDViewOpts{PageFields: fields, Comments: cs}},
		{"read-only", "<p>Body text.</p>", MDViewOpts{ReadOnly: true}},
		{"page-fields", "<p>Body text.</p>", MDViewOpts{PageFields: []PageField{{ID: "title", Label: "Title", Values: []string{"T"}}}}},
		{"empty-body-metadata", "", MDViewOpts{PageFields: fields}},
		{"empty-body-both", "", MDViewOpts{PageFields: fields, Comments: cs}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := parseNode(t, tc.body)
			want := string(RenderMarkdownOpts(root, nil, tc.opts))
			prefix, body, suffix := RenderMarkdownViewParts(root, nil, tc.opts)
			if got := prefix + body + suffix; got != want {
				t.Errorf("concat identity broken:\n got=%q\nwant=%q", got, want)
			}
		})
	}
}

func TestConfluenceDocumentMarkerLineNormalizesOnlyAttachedCR(t *testing.T) {
	document := ConfluenceDocumentMarker + "\r\nbody\r\n"
	if got := ConfluenceDocumentMarkerLine(document); got != ConfluenceDocumentMarker {
		t.Fatalf("marker=%q", got)
	}
	if !strings.Contains(document, "body\r\n") {
		t.Fatal("test control lost body bytes")
	}
}
