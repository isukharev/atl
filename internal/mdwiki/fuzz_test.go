package mdwiki

import (
	"strings"
	"testing"
)

// FuzzConvertDocument: the converter must never panic or hang, must never
// return partial output alongside an error, and success must never produce
// an empty body. The seeds double as deterministic regression tests under
// plain `go test`.
func FuzzConvertDocument(f *testing.F) {
	seeds := []string{
		"# Title\n\npara **bold** _em_ `code`\n\n- a\n- b\n  - c\n\n1. x\n2. y",
		"| K | V |\n| --- | --- |\n| a | 1 |",
		"```go\nfunc main() {}\n```",
		"> quoted\n> \n> more",
		"text with {braces} and [brackets] and | pipes",
		"[site](https://example.com) [DS-1](jira:DS-1) [~user.name]",
		"\ufeff# bom\n\nline\r\nwith crlf",
		"emphasis **without**boundary",
		"a -b- c +d+ e ~f~ g ^h^ ??i??",
		"---\n\n----\n\nh2. wikiish",
		"`{{already}}` and {code} outside",
		"- [ ] task",
		"## Контекст\n\nкириллица с *акцентом* и {скобками}",
		strings.Repeat("* very long line ", 500),
		"\x00control",
		// Multi-line paragraphs (issue #164): lines join with a real newline, so
		// each inner line must be guarded on its own — a blockish line, a list-
		// marker line, and cross-line emphasis that no longer pairs.
		"line one\nline two\nline three",
		"intro text\nh2. sneaky heading",
		"intro text\nbq. sneaky quote",
		"intro text\n- dash bullet\n* star bullet",
		"intro text\n----",
		"**bold\nwrapped across lines**",
		// Issue #167: the paragraph-line escapes wikimd emits for fence/thematic
		// collisions must un-escape to the bare, wiki-inert run (and their remainder
		// still convert) without tripping the block splitter or the fail-closed
		// guards; a real fence / break (no backslash) must stay a fence / hr.
		"intro\n\\```json\n\\---\n\\***\n\\___\ntail",
		"\\```lang **bold** rest\n\\*****",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, md string) {
		out, err := ConvertDocument(md)
		if err != nil {
			if out != "" {
				t.Fatalf("partial output %q with error %v", out, err)
			}
			return
		}
		if out == "" {
			t.Fatalf("success with empty output for %q", md)
		}
		// Md-ism leak checks ("```", "**", "#") can't be blanket properties
		// here: backticks are inert in wiki (literal backticks in a paragraph
		// are a correct rendering), and "#"/"**" are legitimate wiki list
		// markers. The unit tests pin those cases on known inputs instead.
	})
}
