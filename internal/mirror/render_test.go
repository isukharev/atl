package mirror

import (
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// render parses an inline CSF snippet and returns its markdown view. It fails
// the test on a parse error so each render case stays a one-liner.
func render(t *testing.T, snippet string, refs []domain.Ref) string {
	t.Helper()
	root, err := csf.Parse([]byte(snippet))
	if err != nil {
		t.Fatalf("parse %q: %v", snippet, err)
	}
	return string(RenderMarkdown(root, refs))
}

func mustContain(t *testing.T, md string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(md, w) {
			t.Errorf("markdown missing %q\n---\n%s", w, md)
		}
	}
}

// TestRenderBlocks locks paragraphs, headings, and the horizontal rule.
func TestRenderBlocks(t *testing.T) {
	mustContain(t, render(t, `<p>plain paragraph</p>`, nil), "plain paragraph")
	mustContain(t, render(t, `<h1>Top</h1>`, nil), "# Top")
	mustContain(t, render(t, `<h3>Three</h3>`, nil), "### Three")
	mustContain(t, render(t, `<h6>Deep</h6>`, nil), "###### Deep")
	mustContain(t, render(t, `<hr/>`, nil), "---")
}

// TestRenderInline locks bold/italic/code and a plain <a> link. The renderer
// collapses whitespace around inline elements (a best-effort view quirk), so we
// assert the markup substrings rather than exact spacing.
func TestRenderInline(t *testing.T) {
	md := render(t, `<p>x <strong>bold</strong> <em>it</em> <code>cd</code> y</p>`, nil)
	mustContain(t, md, "**bold**", "_it_", "`cd`")

	md = render(t, `<p>see <a href="https://ex.com/p">label</a> end</p>`, nil)
	mustContain(t, md, "[label](https://ex.com/p)")
}

// TestRenderLists locks unordered, ordered, and nested lists (the list() path,
// previously 0% covered).
func TestRenderLists(t *testing.T) {
	ul := render(t, `<ul><li>one</li><li>two</li></ul>`, nil)
	mustContain(t, ul, "- one", "- two")

	ol := render(t, `<ol><li>first</li><li>second</li></ol>`, nil)
	mustContain(t, ol, "1. first", "2. second")

	nested := render(t, `<ul><li>parent<ul><li>child</li></ul></li></ul>`, nil)
	mustContain(t, nested, "- parent", "  - child")
}

// TestRenderMacros locks the common Confluence macros and graceful degradation
// of an unknown macro.
func TestRenderMacros(t *testing.T) {
	// code with language → fenced block.
	code := render(t, `<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">go</ac:parameter><ac:plain-text-body><![CDATA[func main() {}]]></ac:plain-text-body></ac:structured-macro>`, nil)
	mustContain(t, code, "```go", "func main() {}", "```")

	// code with no language → bare fence.
	codeBare := render(t, `<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[x=1]]></ac:plain-text-body></ac:structured-macro>`, nil)
	mustContain(t, codeBare, "```\nx=1\n```")

	// info/note/warning panels → blockquote with the upper-cased label.
	info := render(t, `<ac:structured-macro ac:name="info"><ac:rich-text-body><p>be careful</p></ac:rich-text-body></ac:structured-macro>`, nil)
	mustContain(t, info, "> INFO", "> be careful")

	note := render(t, `<ac:structured-macro ac:name="note"><ac:parameter ac:name="title">Heads up</ac:parameter><ac:rich-text-body><p>body</p></ac:rich-text-body></ac:structured-macro>`, nil)
	mustContain(t, note, "> NOTE: Heads up", "> body")

	warn := render(t, `<ac:structured-macro ac:name="warning"><ac:rich-text-body><p>danger</p></ac:rich-text-body></ac:structured-macro>`, nil)
	mustContain(t, warn, "> WARNING", "> danger")

	// toc and status placeholders.
	mustContain(t, render(t, `<ac:structured-macro ac:name="toc"/>`, nil), "table of contents")
	mustContain(t, render(t, `<ac:structured-macro ac:name="status"><ac:parameter ac:name="title">DONE</ac:parameter></ac:structured-macro>`, nil), "`[DONE]`")

	// Unknown macro degrades to a placeholder that keeps the rich body content;
	// it must not crash or drop the body. (noformat is NOT special-cased: its
	// plain-text body is not surfaced, only the placeholder — assert reality.)
	unknown := render(t, `<ac:structured-macro ac:name="totallyunknown"><ac:rich-text-body><p>kept content</p></ac:rich-text-body></ac:structured-macro>`, nil)
	mustContain(t, unknown, "⟦macro totallyunknown⟧", "kept content")

	noformat := render(t, `<ac:structured-macro ac:name="noformat"><ac:plain-text-body><![CDATA[raw]]></ac:plain-text-body></ac:structured-macro>`, nil)
	mustContain(t, noformat, "⟦macro noformat⟧")
}

// TestRenderResolvedRefs locks the resolved-ref paths: a user mention and an
// inline image that have been resolved to a display name / asset path.
func TestRenderResolvedRefs(t *testing.T) {
	refs := []domain.Ref{
		{Kind: domain.RefUser, Key: "ukey1", Display: "Ada Lovelace"},
		{Kind: domain.RefImage, Key: "pic.png", Asset: "page.assets/pic.png"},
	}
	user := render(t, `<p>cc <ac:link><ri:user ri:userkey="ukey1"/></ac:link></p>`, refs)
	mustContain(t, user, "Ada Lovelace")

	img := render(t, `<p><ac:image><ri:attachment ri:filename="pic.png"/></ac:image></p>`, refs)
	mustContain(t, img, "![pic.png](page.assets/pic.png)")

	// Unresolved image degrades to an attachment: link rather than failing.
	unimg := render(t, `<p><ac:image><ri:attachment ri:filename="other.png"/></ac:image></p>`, nil)
	mustContain(t, unimg, "![other.png](attachment:other.png)")
}

// TestNormalizeBlankLines asserts the rendered view never contains more than one
// consecutive blank line and ends in a single trailing newline, across a
// document mixing several block kinds.
func TestNormalizeBlankLines(t *testing.T) {
	md := render(t, `<p>one</p><h2>Head</h2><p>two</p><ul><li>a</li></ul><p>three</p>`, nil)
	if strings.Contains(md, "\n\n\n") {
		t.Errorf("rendered md has >1 consecutive blank line:\n%q", md)
	}
	if !strings.HasSuffix(md, "\n") || strings.HasSuffix(md, "\n\n") {
		t.Errorf("rendered md should end in exactly one newline:\n%q", md)
	}
	// Direct unit check of the helper's collapsing + trimming guarantees.
	got := normalizeBlankLines("a\n\n\n\nb\n\n\n")
	if got != "a\n\nb\n" {
		t.Errorf("normalizeBlankLines = %q, want %q", got, "a\n\nb\n")
	}
}
