package wikimd

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// pin is a wiki input, its Options, and the exact markdown Render must produce.
func TestRenderExact(t *testing.T) {
	imgs := map[string]string{"shot.png": "PROJ-1.assets/10001-shot.png"}
	cases := []struct {
		name string
		in   string
		opts Options
		want string
	}{
		// headings
		{"h1", "h1. Title", Options{}, "# Title"},
		{"h2", "h2. Foo", Options{}, "## Foo"},
		{"h6", "h6. Deep", Options{}, "###### Deep"},
		{"heading with inline", "h3. a *bold* word", Options{}, "### a **bold** word"},

		// inline emphasis
		{"bold", "*bold*", Options{}, "**bold**"},
		{"italic", "_italic_", Options{}, "*italic*"},
		{"mono", "{{mono}}", Options{}, "`mono`"},
		{"strike", "a -gone- b", Options{}, "a ~~gone~~ b"},
		{"combo", "*b* and _i_ and {{m}} and -s-", Options{}, "**b** and *i* and `m` and ~~s~~"},

		// emphasis that must NOT fire (passthrough)
		{"intraword underscore", "snake_case_var", Options{}, "snake_case_var"},
		{"hyphen word", "well-known thing", Options{}, "well-known thing"},
		{"hyphen spaced range", "3 - 5 items", Options{}, "3 - 5 items"},
		{"lone star", "2 * 3 = 6", Options{}, "2 * 3 = 6"},

		// mentions, colors, breaks, rules
		{"mention", "[~jsmith] shipped it", Options{}, "**@jsmith** shipped it"},
		{"color drop", "{color:red}alert{color} now", Options{}, "alert now"},
		{"forced break", `line1\\line2`, Options{}, "line1  \nline2"},
		{"hr", "----", Options{}, "---"},

		// links
		{"link text url", "see [docs|https://x/y] here", Options{}, "see [docs](https://x/y) here"},
		{"bare url autolink", "at [https://x] ok", Options{}, "at <https://x> ok"},
		{"bracket prose kept", "a [TODO] item", Options{}, "a [TODO] item"},

		// images
		{"image resolved", "!shot.png!", Options{Images: imgs}, "![shot.png](PROJ-1.assets/10001-shot.png)"},
		{"image resolved with params", "!shot.png|thumbnail,width=300!", Options{Images: imgs}, "![shot.png](PROJ-1.assets/10001-shot.png)"},
		{"image unresolved", "!missing.png!", Options{}, "`!missing.png!`"},
		{"image external", "!https://h/a.png!", Options{}, "![](https://h/a.png)"},
		{"exclamations kept", "Wow! Great! Yes!", Options{}, "Wow! Great! Yes!"},
		{"padded bang span stays literal", "Done! v1.2! yes", Options{}, "Done! v1.2! yes"},
		{"image path with md-significant chars",
			"!shot (v1).png!",
			Options{Images: map[string]string{"shot (v1).png": "PROJ-1.assets/7-shot (v1).png"}},
			"![shot (v1).png](PROJ-1.assets/7-shot%20%28v1%29.png)"},
		{"image alt with brackets",
			"!a[1].png!",
			Options{Images: map[string]string{"a[1].png": "PROJ-1.assets/8-a[1].png"}},
			`![a\[1\].png](PROJ-1.assets/8-a[1].png)`},
		{"link url with parens", "[go|https://x/wiki/Go_(lang)]", Options{}, "[go](https://x/wiki/Go_%28lang%29)"},
		{"link url keeps existing percent-encoding", "[y|https://x/a%20b]", Options{}, "[y](https://x/a%20b)"},

		// code / noformat (verbatim, no inner parsing)
		{"code lang", "{code:go}\nfmt.Println(x)\n{code}", Options{}, "```go\nfmt.Println(x)\n```"},
		{"code no lang", "{code}\nplain *notbold*\n{code}", Options{}, "```\nplain *notbold*\n```"},
		{"code param language", "{code:title=x|language=java}\nSystem.out;\n{code}", Options{}, "```java\nSystem.out;\n```"},
		{"noformat", "{noformat}\nraw _text_\n{noformat}", Options{}, "```\nraw _text_\n```"},
		{"code oneliner", "{code:sh}echo hi{code}", Options{}, "```sh\necho hi\n```"},
		{"code body with triple backticks", "{code}\na\n```\nb\n{code}", Options{}, "````\na\n```\nb\n````"},

		// quote / panel
		{"quote", "{quote}\nhello there\n{quote}", Options{}, "> hello there"},
		{"quote inline", "{quote}short{quote}", Options{}, "> short"},
		{"panel titled", "{panel:title=Note}\nbody text\n{panel}", Options{}, "> **Note**\n>\n> body text"},
		{"panel plain", "{panel}\njust body\n{panel}", Options{}, "> just body"},

		// tables
		{"table with header", "||H1||H2||\n|a|b|\n|c|d|", Options{}, "| H1 | H2 |\n| --- | --- |\n| a | b |\n| c | d |"},
		{"table no header uses first row", "|a|b|\n|c|d|", Options{}, "| a | b |\n| --- | --- |\n| c | d |"},

		// lists
		{"list nested", "* one\n* two\n** deep\n# num", Options{}, "- one\n- two\n  - deep\n1. num"},
		{"list ordered nested", "# a\n## b\n#* c", Options{}, "1. a\n  1. b\n  - c"},

		// empty
		{"empty", "", Options{}, ""},
		{"blank only", "\n\n\n", Options{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Render(c.in, c.opts)
			if got != c.want {
				t.Errorf("Render(%q):\n got: %q\nwant: %q", c.in, got, c.want)
			}
		})
	}
}

// A {code} body must be passed through verbatim: no heading, list, or emphasis
// inside it may be interpreted.
func TestRenderCodeIsVerbatim(t *testing.T) {
	in := "{code}\nh1. not a heading\n* not a list\n*not bold*\n{code}"
	got := Render(in, Options{})
	want := "```\nh1. not a heading\n* not a list\n*not bold*\n```"
	if got != want {
		t.Errorf("verbatim code:\n got: %q\nwant: %q", got, want)
	}
}

// An unterminated {code} consumes the rest of the document rather than losing
// it, and still closes the fence.
func TestRenderUnterminatedCode(t *testing.T) {
	got := Render("{code:go}\nleft open\nsecond line", Options{})
	want := "```go\nleft open\nsecond line\n```"
	if got != want {
		t.Errorf("unterminated code:\n got: %q\nwant: %q", got, want)
	}
}

// Document-level structure: paragraphs, a heading, and a code block keep a
// single blank line between them and no leading/trailing blanks.
func TestRenderDocumentStructure(t *testing.T) {
	in := "para one\n\nh2. Section\n\nmore prose\n\n{code}\nx\n{code}"
	got := Render(in, Options{})
	want := "para one\n\n## Section\n\nmore prose\n\n```\nx\n```"
	if got != want {
		t.Errorf("doc structure:\n got: %q\nwant: %q", got, want)
	}
}

// A table cell's inline markup is rendered and pipes are escaped so the row
// survives.
func TestRenderTableCellInline(t *testing.T) {
	got := Render("||H||\n|*bold* text|", Options{})
	want := "| H |\n| --- |\n| **bold** text |"
	if got != want {
		t.Errorf("table cell inline:\n got: %q\nwant: %q", got, want)
	}
}

// Render is a total function: for a spread of gnarly inputs it must not panic
// and must keep the output valid UTF-8 when the input is.
func TestRenderTotalSpotChecks(t *testing.T) {
	inputs := []string{
		"{code}\nunterminated",
		"{quote}\nno close",
		"||h||\n|only header pipes||",
		"\x00\x01 control bytes",
		"кириллица с *акцентом*",
		strings.Repeat("*x* ", 5000),
		"![[nested]] and !a! and [x|y|z]",
		"{color}{color}{color:blue}text",
	}
	for _, in := range inputs {
		out := Render(in, Options{})
		if utf8.ValidString(in) && !utf8.ValidString(out) {
			t.Errorf("Render(%q) produced invalid UTF-8: %q", in, out)
		}
	}
}
