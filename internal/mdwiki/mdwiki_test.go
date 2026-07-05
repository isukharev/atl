package mdwiki

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func convertOK(t *testing.T, md string) string {
	t.Helper()
	out, err := ConvertDocument(md)
	if err != nil {
		t.Fatalf("ConvertDocument(%q): %v", md, err)
	}
	return out
}

// noMDisms asserts the converter never leaks markdown artifacts — the exact
// failure mode --from-md exists to prevent.
func noMDisms(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "```") || strings.Contains(out, "**") {
		t.Errorf("markdown artifact leaked into wiki output: %q", out)
	}
	if regexp.MustCompile(`(?m)^#{1,6} `).MatchString(out) {
		t.Errorf("markdown heading leaked into wiki output: %q", out)
	}
}

func TestConvertBasics(t *testing.T) {
	cases := []struct{ md, want string }{
		{"# Title", "h1. Title"},
		{"### Deep **bold**", "h3. Deep *bold*"},
		{"Plain paragraph.", "Plain paragraph."},
		{"line one\nline two", "line one line two"},
		{"---", "----"},
		{"a **b** _c_ ~~d~~ `e`", "a *b* _c_ -d- {{e}}"},
		{"*em* alone", "_em_ alone"},
		{"[site](https://example.com)", "[site|https://example.com]"},
		{"[DS-1](jira:DS-1)", "DS-1"},
		{"see PROJ-123 inline", "see PROJ-123 inline"},
		{"ping [~jdoe] now", "ping [~jdoe] now"},
		{"snake_case stays_literal", "snake_case stays_literal"},
		{"escaped \\*star\\*", "escaped \\*star\\*"},
		{"unmatched **bold", "unmatched \\*\\*bold"},
		// Multi-byte runes pass through byte-exact (regression: escapeChar once
		// widened bytes to runes, mangling Cyrillic).
		{"## Контекст **важно**", "h2. Контекст *важно*"},
		{"Кириллица с *акцентом* и `кодом`.", "Кириллица с _акцентом_ и {{кодом}}."},
	}
	for _, c := range cases {
		if got := convertOK(t, c.md); got != c.want {
			t.Errorf("ConvertDocument(%q) = %q, want %q", c.md, got, c.want)
		}
		noMDisms(t, convertOK(t, c.md))
	}
}

func TestEscaping(t *testing.T) {
	cases := []struct{ md, want string }{
		// Wiki-active characters in plain text must be neutralized.
		{"set {timeout} in [config]", `set \{timeout\} in \[config\]`},
		{"a | b outside a table", `a \| b outside a table`},
		{"warning! not an embed", `warning\! not an embed`}, // '!' opens image embeds — always neutralized
		{"dashed-word stays", "dashed-word stays"},
		{"2026-07-01 dates survive", "2026-07-01 dates survive"},
		{"a -maybe strike- b", `a \-maybe strike- b`}, // opening '-' neutralized
		{"plus +one+ two", `plus \+one+ two`},
	}
	for _, c := range cases {
		if got := convertOK(t, c.md); got != c.want {
			t.Errorf("ConvertDocument(%q) = %q, want %q", c.md, got, c.want)
		}
	}
}

func TestConvertCodeFence(t *testing.T) {
	got := convertOK(t, "```go\nif a < b {\n\treturn\n}\n```")
	want := "{code:go}\nif a < b {\n\treturn\n}\n{code}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := convertOK(t, "```\nplain **not bold** ```\n```"); !strings.Contains(got, "**not bold**") {
		// fence bodies are verbatim — no inline processing, no escaping
		t.Errorf("fence body must stay verbatim, got %q", got)
	}
	if _, err := ConvertDocument("```\nbody with {code} inside\n```"); err == nil {
		t.Error("body containing {code} must be refused")
	}
	if _, err := ConvertDocument("```weird lang!\nx\n```"); err == nil {
		t.Error("non-identifier info string must be refused")
	}
}

func TestConvertTable(t *testing.T) {
	got := convertOK(t, "| K | V |\n| --- | --- |\n| a | 1 |\n| b |")
	want := "||K||V||\n|a|1|\n|b| |"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := ConvertDocument("| a | b | c |\n| --- | --- |\n| 1 | 2 | 3 |"); err == nil {
		t.Error("row wider than header must be refused")
	}
	// Wiki tables have no escape for '|' inside a cell — must refuse, not emit
	// a cell that silently splits in two.
	if _, err := ConvertDocument("| K |\n| --- |\n| b \\| c |"); err == nil {
		t.Error("cell containing a pipe must be refused")
	}
}

func TestConvertLists(t *testing.T) {
	got := convertOK(t, "- one\n- two\n  - nested\n- three")
	want := "* one\n* two\n** nested\n* three"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	got = convertOK(t, "1. first\n2. second\n   1. sub")
	want = "# first\n# second\n## sub"
	if got != want {
		t.Errorf("ordered got %q, want %q", got, want)
	}
	got = convertOK(t, "- outer\n  1. inner")
	want = "* outer\n*# inner"
	if got != want {
		t.Errorf("mixed nesting got %q, want %q", got, want)
	}
	for _, bad := range []string{
		"- [ ] task item",      // no wiki equivalent
		"- a\nnot an item",     // continuation line
		"  - deep\n- shallow",  // dedent below first
		"- a\n- b\n3. ordered", // mixed siblings
	} {
		if _, err := ConvertDocument(bad); err == nil {
			t.Errorf("ConvertDocument(%q) should fail", bad)
		}
	}
}

func TestConvertBlockquote(t *testing.T) {
	got := convertOK(t, "> just a quote")
	if got != "{quote}\njust a quote\n{quote}" {
		t.Errorf("got %q", got)
	}
	got = convertOK(t, "> line one\n> \n> line two")
	if got != "{quote}\nline one\n\nline two\n{quote}" {
		t.Errorf("got %q", got)
	}
}

func TestConvertDocumentWhole(t *testing.T) {
	md := "# Title\n\nIntro with **bold** and `code`.\n\n- one\n- two\n\n| K | V |\n| --- | --- |\n| a | 1 |\n\n```bash\necho hi\n```\n"
	out := convertOK(t, md)
	want := "h1. Title\n\nIntro with *bold* and {{code}}.\n\n* one\n* two\n\n||K||V||\n|a|1|\n\n{code:bash}\necho hi\n{code}"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	noMDisms(t, out)
}

func TestConvertDocumentFailsClosed(t *testing.T) {
	out, err := ConvertDocument("# Fine\n\n![img](x.png)\n")
	if err == nil {
		t.Fatalf("ConvertDocument = %q, want error", out)
	}
	if out != "" {
		t.Errorf("partial output %q escaped with error", out)
	}
	var ue *UnsupportedError
	if !errors.As(err, &ue) {
		t.Errorf("want wrapped *UnsupportedError, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "block 2") || !strings.Contains(err.Error(), "![img]") {
		t.Errorf("error must name the block and its first line, got %q", err)
	}

	for _, empty := range []string{"", "\n\n", "   \n\t\n"} {
		if out, err := ConvertDocument(empty); err == nil {
			t.Errorf("ConvertDocument(%q) = %q, want error", empty, out)
		}
	}

	for _, bad := range []string{
		"⟦macro jira⟧",
		"[[Page Title]]",                       // Confluence page link
		"[fixed bug](jira:DS-1)",               // text must equal key
		"[file](attachment:report.pdf)",        // marker scheme
		"**bold**suffix touches a word",        // wiki toggle needs a boundary
		"h2. already wiki markup",              // would silently become a heading
		"`code with {brace}`",                  // cannot sit inside {{...}}
		"[text|pipe](https://example.com/a|b)", // wiki delimiters in URL
		"```\nunterminated",
	} {
		out, err := ConvertDocument(bad)
		if err == nil {
			t.Errorf("ConvertDocument(%q) = %q, want error", bad, out)
			continue
		}
		var ue *UnsupportedError
		if !errors.As(err, &ue) {
			t.Errorf("ConvertDocument(%q): want *UnsupportedError, got %T %v", bad, err, err)
		}
	}
}

func TestBOMStripped(t *testing.T) {
	out := convertOK(t, "\ufeff# Title\n\nHello\n")
	if out != "h1. Title\n\nHello" {
		t.Errorf("BOM doc = %q", out)
	}
}
