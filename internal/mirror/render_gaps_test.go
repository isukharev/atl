package mirror

import (
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// These tests are driven by a frequency analysis of a corpus of real Confluence
// pages. Each locks one construct the converter previously mangled or dropped.
// Snippets are synthetic but mirror the storage-format shapes found in the wild.
// See docs/csf-markdown-testing.md.

func mustNotContain(t *testing.T, md string, bad string) {
	t.Helper()
	if strings.Contains(md, bad) {
		t.Errorf("markdown should not contain %q\n---\n%s", bad, md)
	}
}

// Gap #1b: block-level children flattened into an inline context (multiple <p>
// inside a table cell or <li>) must be space-separated, not glued
// ("Alpha"+"Beta" → "Alpha Beta", not "AlphaBeta").
func TestRenderCellBlockChildrenSeparated(t *testing.T) {
	md := render(t, `<table><tbody><tr><td><p>Alpha</p><p>Beta</p></td><td>x</td></tr></tbody></table>`, nil)
	mustContain(t, md, "| Alpha Beta |")
}

// Guard: inline <span> (the most common element, 593× in the corpus) must NOT
// gain spurious spaces from the flow-break separation — only block elements do.
func TestRenderInlineSpanNoSpuriousSpace(t *testing.T) {
	got := strings.TrimSpace(render(t, `<p>foo<span>bar</span>baz</p>`, nil))
	if got != "foobarbaz" {
		t.Errorf("span must not introduce spaces: got %q want %q", got, "foobarbaz")
	}
}

// Gap #1 (dominant): inline elements must not glue surrounding words together.
// Previously `collapseWS` dropped whitespace-only text nodes between inline
// elements, producing "Introwith**bold**,_italic_and`code`words.".
func TestRenderInlineSpacingPreserved(t *testing.T) {
	got := strings.TrimSpace(render(t,
		`<p>Intro with <strong>bold</strong>, <em>italic</em> and <code>code</code> words.</p>`, nil))
	want := "Intro with **bold**, _italic_ and `code` words."
	if got != want {
		t.Errorf("spacing not preserved:\n got: %q\nwant: %q", got, want)
	}
}

// NEW gap: a single block macro wrapped in <p> (a very common Confluence shape)
// must still get block treatment, not the impoverished inline path that emits a
// bare ⟦name⟧ and drops the body.
func TestRenderParagraphWrappedMacro(t *testing.T) {
	toc := render(t, `<p><ac:structured-macro ac:name="toc"/></p>`, nil)
	mustContain(t, toc, "⟦table of contents⟧")
	mustNotContain(t, toc, "⟦toc⟧")

	code := render(t, `<p><ac:structured-macro ac:name="code"><ac:parameter ac:name="language">go</ac:parameter><ac:plain-text-body><![CDATA[x := 1]]></ac:plain-text-body></ac:structured-macro></p>`, nil)
	mustContain(t, code, "```go", "x := 1", "```")
	mustNotContain(t, code, "⟦code⟧")
}

// NEW gap: jira macro (9/39 pages) must surface the issue key, not ⟦jira⟧.
func TestRenderJiraMacro(t *testing.T) {
	inline := render(t, `<p>See <ac:structured-macro ac:name="jira"><ac:parameter ac:name="server">Jira</ac:parameter><ac:parameter ac:name="key">PROJ-42</ac:parameter></ac:structured-macro> please</p>`, nil)
	mustContain(t, inline, "[PROJ-42](jira:PROJ-42)")
	mustNotContain(t, inline, "⟦jira⟧")
}

// NEW gap: a keyless jira issues/filter macro carries a JQL query, not a single
// key. Inline rendering must surface the query, not a bare ⟦jira⟧.
func TestRenderJiraFilterMacro(t *testing.T) {
	md := render(t, `<p><ac:structured-macro ac:name="jira"><ac:parameter ac:name="server">Jira</ac:parameter><ac:parameter ac:name="jqlQuery">issue in (PROJ-1, PROJ-2)</ac:parameter><ac:parameter ac:name="maximumIssues">20</ac:parameter></ac:structured-macro></p>`, nil)
	mustContain(t, md, "issue in (PROJ-1, PROJ-2)")
	mustNotContain(t, md, "⟦jira⟧")
}

// Gap: task lists must render as GFM task items, not a junk pile of ids and
// status words as separate paragraphs.
func TestRenderTaskList(t *testing.T) {
	md := render(t, `<ac:task-list><ac:task><ac:task-id>1</ac:task-id><ac:task-status>complete</ac:task-status><ac:task-body>Done thing</ac:task-body></ac:task><ac:task><ac:task-id>2</ac:task-id><ac:task-status>incomplete</ac:task-status><ac:task-body>Pending thing</ac:task-body></ac:task></ac:task-list>`, nil)
	mustContain(t, md, "- [x] Done thing", "- [ ] Pending thing")
	mustNotContain(t, md, "incomplete")
}

// Review #1: a nested task list must indent as a sub-list, not flatten its
// sub-tasks' id/status/body into the parent task's line.
func TestRenderNestedTaskList(t *testing.T) {
	md := render(t, `<ac:task-list><ac:task><ac:task-id>1</ac:task-id><ac:task-status>incomplete</ac:task-status><ac:task-body>Parent<ac:task-list><ac:task><ac:task-id>2</ac:task-id><ac:task-status>complete</ac:task-status><ac:task-body>Child</ac:task-body></ac:task></ac:task-list></ac:task-body></ac:task></ac:task-list>`, nil)
	mustContain(t, md, "- [ ] Parent", "  - [x] Child")
	mustNotContain(t, md, "Parent2")       // the nested id must not glue onto the parent line
	mustNotContain(t, md, "completeChild") // nor the nested status
}

// Review #2: a code macro that appears inline (mixed with text) must collapse a
// multi-line body so the code span stays on one line — a literal newline inside
// backticks is broken Markdown.
func TestRenderInlineCodeMacroMultiline(t *testing.T) {
	md := render(t, `<p>see <ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[line1
line2]]></ac:plain-text-body></ac:structured-macro> end</p>`, nil)
	mustContain(t, md, "`line1 line2`")
	mustNotContain(t, md, "line1\nline2")
}

// Gap: merged header cells (colspan, 8/39 pages) must pad the row so columns
// stay aligned with the body rows.
func TestRenderTableColspan(t *testing.T) {
	md := render(t, `<table><tbody><tr><th colspan="2">Span</th></tr><tr><td>a</td><td>b</td></tr></tbody></table>`, nil)
	mustContain(t, md, "| Span |", "| --- | --- |", "| a | b |")
}

// Gap: expand macro must keep its title (body was already kept).
func TestRenderExpandTitle(t *testing.T) {
	md := render(t, `<ac:structured-macro ac:name="expand"><ac:parameter ac:name="title">Show me</ac:parameter><ac:rich-text-body><p>hidden body</p></ac:rich-text-body></ac:structured-macro>`, nil)
	mustContain(t, md, "Show me", "hidden body")
}

// Gap: external ri:url images must render; decorative icons (ac:class="icon",
// the Jira issue-type avatars) must be skipped so they don't clutter links.
func TestRenderExternalImage(t *testing.T) {
	ext := render(t, `<p><ac:image><ri:url ri:value="https://ex.com/a.png"/></ac:image></p>`, nil)
	mustContain(t, ext, "![](https://ex.com/a.png)")

	icon := render(t, `<p>x <ac:image ac:class="icon"><ri:url ri:value="https://ex.com/i.png"/></ac:image> y</p>`, nil)
	mustNotContain(t, icon, "https://ex.com/i.png")
	mustContain(t, icon, "x", "y")
}

// Gap: noformat body was silently dropped (only the placeholder survived).
func TestRenderNoformatBody(t *testing.T) {
	md := render(t, `<ac:structured-macro ac:name="noformat"><ac:plain-text-body><![CDATA[raw payload]]></ac:plain-text-body></ac:structured-macro>`, nil)
	mustContain(t, md, "raw payload", "```")
}

// Gap: a top-level <pre> must fence, not flatten to a glued paragraph.
func TestRenderPreBlock(t *testing.T) {
	md := render(t, `<pre>sample-text</pre>`, nil)
	mustContain(t, md, "```", "sample-text")
}

// Gap: <time> carries its value in an attribute; it must not vanish.
func TestRenderTime(t *testing.T) {
	md := render(t, `<p>meet on <time datetime="2021-07-15"/> ok</p>`, nil)
	mustContain(t, md, "2021-07-15")
}

// Gap: emoticons must not silently disappear.
func TestRenderEmoticon(t *testing.T) {
	fb := render(t, `<p>x <ac:emoticon ac:name="smile" ac:emoji-fallback="🙂"/> y</p>`, nil)
	mustContain(t, fb, "🙂")

	named := render(t, `<p><ac:emoticon ac:name="light-on"/></p>`, nil)
	mustContain(t, named, ":light-on:")
}

// Gap: strikethrough must map to ~~...~~.
func TestRenderStrikethrough(t *testing.T) {
	md := render(t, `<p>a <s>gone</s> b</p>`, nil)
	mustContain(t, md, "~~gone~~")
}

// Gap: blockquote must keep its > prefix.
func TestRenderBlockquote(t *testing.T) {
	md := render(t, `<blockquote><p>quoted line</p></blockquote>`, nil)
	mustContain(t, md, "> quoted line")
}

// Gap: view-file / include carry their target in a nested ri:* element, not text.
func TestRenderViewFileAndInclude(t *testing.T) {
	vf := render(t, `<ac:structured-macro ac:name="view-file"><ac:parameter ac:name="name"><ri:attachment ri:filename="doc.pdf"/></ac:parameter></ac:structured-macro>`, nil)
	mustContain(t, vf, "doc.pdf")

	inc := render(t, `<ac:structured-macro ac:name="include"><ac:parameter ac:name=""><ac:link><ri:page ri:content-title="Other Page"/></ac:link></ac:parameter></ac:structured-macro>`, nil)
	mustContain(t, inc, "Other Page")
}

// Gap: children macro (#1 macro) — friendlier, distinguishable placeholder.
func TestRenderChildren(t *testing.T) {
	md := render(t, `<ac:structured-macro ac:name="children"/>`, nil)
	mustContain(t, md, "child pages")
}

// Regression guard: resolved user mention inside a real task body still works
// and the body stays on the task line.
func TestRenderTaskListWithInlineBody(t *testing.T) {
	refs := []domain.Ref{{Kind: domain.RefUser, Key: "u1", Display: "Ada"}}
	md := render(t, `<ac:task-list><ac:task><ac:task-id>9</ac:task-id><ac:task-status>incomplete</ac:task-status><ac:task-body><ac:link><ri:user ri:userkey="u1"/></ac:link> ping on <time datetime="2021-07-15"/></ac:task-body></ac:task></ac:task-list>`, refs)
	mustContain(t, md, "- [ ] Ada ping on 2021-07-15")
}

func TestRenderPageLinkWikiStyle(t *testing.T) {
	// A plain page link with no explicit label → [[Page Title]]
	md := render(t, `<p>See <ac:link><ri:page ri:content-title="My Page"/></ac:link> here</p>`, nil)
	mustContain(t, md, "[[My Page]]")
	mustNotContain(t, md, "(page:My Page)")

	// A page link with an explicit label → [[Custom Label]]
	md = render(t, `<p>See <ac:link><ri:page ri:content-title="My Page"/><ac:plain-text-link-body>Custom Label</ac:plain-text-link-body></ac:link> here</p>`, nil)
	mustContain(t, md, "[[Custom Label]]")
}
