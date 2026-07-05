package mdcsf

import (
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/mirror"
)

// convertOK converts and asserts the result is well-formed CSF.
func convertOK(t *testing.T, md string) string {
	t.Helper()
	out, err := Convert(md)
	if err != nil {
		t.Fatalf("Convert(%q): %v", md, err)
	}
	if ps := csf.Validate(out); csf.HasErrors(ps) {
		t.Fatalf("Convert(%q) produced invalid CSF %q: %+v", md, out, ps)
	}
	return string(out)
}

func TestConvertBasics(t *testing.T) {
	cases := []struct{ md, want string }{
		{"# Title", "<h1>Title</h1>"},
		{"### Deep **bold**", "<h3>Deep <strong>bold</strong></h3>"},
		{"Plain paragraph.", "<p>Plain paragraph.</p>"},
		{"line one\nline two", "<p>line one line two</p>"},
		{"---", "<hr/>"},
		{"a **b** _c_ ~~d~~ `e`", "<p>a <strong>b</strong> <em>c</em> <s>d</s> <code>e</code></p>"},
		{"5 < 6 & 7 > 4", "<p>5 &lt; 6 &amp; 7 &gt; 4</p>"},
		{"[site](https://example.com)", `<p><a href="https://example.com">site</a></p>`},
		// Balanced parens inside the URL must survive into the href intact.
		{"[Foo](https://en.wikipedia.org/wiki/Foo_(bar)) tail",
			`<p><a href="https://en.wikipedia.org/wiki/Foo_(bar)">Foo</a> tail</p>`},
		{"[[Page Title]]", `<p><ac:link><ri:page ri:content-title="Page Title"/></ac:link></p>`},
		{"[DS-1](jira:DS-1)", `<p><ac:structured-macro ac:name="jira"><ac:parameter ac:name="key">DS-1</ac:parameter></ac:structured-macro></p>`},
		{"snake_case stays_literal", "<p>snake_case stays_literal</p>"},
		{"unmatched **bold", "<p>unmatched **bold</p>"},
		{"escaped \\*star\\*", "<p>escaped *star*</p>"},
	}
	for _, c := range cases {
		if got := convertOK(t, c.md); got != c.want {
			t.Errorf("Convert(%q) = %q, want %q", c.md, got, c.want)
		}
	}
}

func TestConvertCodeFence(t *testing.T) {
	got := convertOK(t, "```go\nif a < b {\n\treturn\n}\n```")
	want := `<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">go</ac:parameter>` +
		"<ac:plain-text-body><![CDATA[if a < b {\n\treturn\n}]]></ac:plain-text-body></ac:structured-macro>"
	if got != want {
		t.Errorf("got %q", got)
	}
	// A CDATA terminator inside the body must not break well-formedness.
	convertOK(t, "```\npayload ]]> more\n```")
}

func TestConvertTable(t *testing.T) {
	got := convertOK(t, "| K | V |\n| --- | --- |\n| a | 1 |\n| b \\| c |")
	want := "<table><tbody><tr><th>K</th><th>V</th></tr>" +
		"<tr><td>a</td><td>1</td></tr><tr><td>b | c</td><td></td></tr></tbody></table>"
	if got != want {
		t.Errorf("got %q", got)
	}
	if _, err := Convert("| a | b | c |\n| --- | --- |\n| 1 | 2 | 3 |"); err == nil {
		t.Error("row wider than header must be refused")
	} else {
		var ue *UnsupportedError
		if !errors.As(err, &ue) {
			t.Errorf("want *UnsupportedError, got %T", err)
		}
	}
}

func TestConvertLists(t *testing.T) {
	got := convertOK(t, "- one\n- two\n  - nested\n- three")
	want := "<ul><li>one</li><li>two<ul><li>nested</li></ul></li><li>three</li></ul>"
	if got != want {
		t.Errorf("got %q", got)
	}
	got = convertOK(t, "1. first\n2. second")
	if got != "<ol><li>first</li><li>second</li></ol>" {
		t.Errorf("ordered got %q", got)
	}
	got = convertOK(t, "- [ ] open\n- [x] done")
	want = "<ac:task-list><ac:task><ac:task-status>incomplete</ac:task-status><ac:task-body>open</ac:task-body></ac:task>" +
		"<ac:task><ac:task-status>complete</ac:task-status><ac:task-body>done</ac:task-body></ac:task></ac:task-list>"
	if got != want {
		t.Errorf("tasks got %q", got)
	}
	for _, bad := range []string{
		"- plain\n- [ ] task",       // mixed task/plain
		"- a\nnot an item",          // continuation line
		"  - deep\n- shallow",       // dedent below first
		"- a\n    - jump\n  - back", // indentation jump is fine? 4>0 then 2: jump from 0 to 4 relative to nested run start 4, back to 2 → dedent mid-list
	} {
		if _, err := Convert(bad); err == nil {
			t.Errorf("Convert(%q) should fail", bad)
		}
	}
}

func TestConvertBlockquoteAndAdmonition(t *testing.T) {
	got := convertOK(t, "> just a quote")
	if got != "<blockquote><p>just a quote</p></blockquote>" {
		t.Errorf("got %q", got)
	}
	got = convertOK(t, "> WARNING: Careful\n> \n> Don't do this.")
	want := `<ac:structured-macro ac:name="warning"><ac:parameter ac:name="title">Careful</ac:parameter>` +
		`<ac:rich-text-body><p>Don't do this.</p></ac:rich-text-body></ac:structured-macro>`
	if got != want {
		t.Errorf("got %q", got)
	}
	got = convertOK(t, "> INFO\n> \n> Body here.")
	if !strings.Contains(got, `ac:name="info"`) || strings.Contains(got, "title") {
		t.Errorf("info without title got %q", got)
	}
}

func TestConvertFailsClosed(t *testing.T) {
	for _, bad := range []string{
		"⟦macro jira⟧",
		"see ⟦table of contents⟧",
		"![img](attachment:x.png)",
		"[fixed bug](jira:DS-1)", // jira link text must equal the key
		"[file](attachment:report.pdf)",
		"```\nunterminated",
		"# h1\ncontinuation",
	} {
		out, err := Convert(bad)
		if err == nil {
			t.Errorf("Convert(%q) = %q, want error", bad, out)
			continue
		}
		var ue *UnsupportedError
		if !errors.As(err, &ue) {
			t.Errorf("Convert(%q): want *UnsupportedError, got %T %v", bad, err, err)
		}
		if out != nil {
			t.Errorf("Convert(%q) returned partial output %q with error", bad, out)
		}
	}
}

func TestSplitBlocks(t *testing.T) {
	md := "# Title\n\npara one\npara one b\n\n```go\ncode\n\nstill code\n```\n\n- item\n- item2\n\n| a |\n| --- |\n"
	got := SplitBlocks(md)
	want := []string{
		"# Title",
		"para one\npara one b",
		"```go\ncode\n\nstill code\n```",
		"- item\n- item2",
		"| a |\n| --- |",
	}
	if len(got) != len(want) {
		t.Fatalf("blocks = %d, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("block %d = %q, want %q", i, got[i], want[i])
		}
	}
	// A heading directly after a paragraph line still starts its own block.
	got = SplitBlocks("text\n# H\nmore")
	if len(got) != 3 || got[1] != "# H" {
		t.Errorf("tight heading split = %q", got)
	}
}

// TestRoundTrip: converting a markdown block and re-rendering the CSF yields
// the same markdown. This pins converter output to the shapes the renderer
// understands — the property the md edit surface stands on.
func TestRoundTrip(t *testing.T) {
	cases := []string{
		"# Title",
		"## Sub **bold** _em_",
		"Paragraph with `code` and [link](https://example.com/x?a=1&b=2).",
		"---",
		"- one\n- two\n  - nested deep\n- three",
		"1. first\n2. second",
		"- [ ] open task\n- [x] closed task",
		"| K | V |\n| --- | --- |\n| a | 1 |\n| b | 2 |",
		"```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```",
		"> WARNING: Careful\n> \n> Don't touch.",
		"Ссылка на [[Другую Страницу]] по заголовку.",
	}
	for _, md := range cases {
		out := convertOK(t, md)
		root, err := csf.Parse([]byte(out))
		if err != nil {
			t.Errorf("re-parse %q: %v", out, err)
			continue
		}
		rendered := strings.TrimRight(string(mirror.RenderMarkdown(root, nil)), "\n")
		if rendered != md {
			t.Errorf("round trip drift:\n  in:  %q\n  csf: %q\n  out: %q", md, out, rendered)
		}
	}
}
