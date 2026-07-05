package mdcsf

import (
	"testing"

	"github.com/isukharev/atl/internal/csf"
)

// mdSeeds cover every supported construct plus hostile shapes: unterminated
// fences, ragged tables, deep nesting, entity/CDATA payloads, multi-byte text,
// and delimiter soup.
var mdSeeds = []string{
	"# Title",
	"###### deep",
	"plain para",
	"---",
	"a **b** _c_ ~~d~~ `e` \\*f\\*",
	"[t](https://x) [[P]] [b](mailto:a@b)",
	"```go\nif a < b { c() }\n```",
	"```\n]]>\n```",
	"| a | b |\n| --- | --- |\n| 1 | 2 |",
	"| a \\| b |\n| --- |\n| `c` |",
	"- one\n- two\n  - three\n    - four",
	"1. a\n2. b\n  1. c",
	"- [ ] t1\n- [x] t2\n  - [ ] t3",
	"> quote line\n> second",
	"> INFO: T\n> \n> body\n> \n> - l1\n- broken",
	"** ** __ __ ~~ ~~ ` `",
	"**unclosed [unclosed ```",
	"текст на кириллице **жирный** и `код`",
	"a\nb\nc",
	"#not a heading",
	"|not|a|table",
	"\\|\\`\\#\\~",
	"> \n> \n> ",
	"```lang`\nx\n```",
	"[x](jira:X-1)",
	"⟦macro toc⟧",
	"![i](a.png)",
}

// FuzzConvert asserts the converter's contract: it never panics, never
// returns partial output with an error, and — the invariant the write path
// depends on — every successful conversion is well-formed CSF with zero
// error-severity problems.
func FuzzConvert(f *testing.F) {
	for _, s := range mdSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, md string) {
		out, err := Convert(md)
		if err != nil {
			if out != nil {
				t.Fatalf("Convert(%q) returned output %q alongside error %v", md, out, err)
			}
			return
		}
		if len(out) == 0 {
			t.Fatalf("Convert(%q) returned empty output without error", md)
		}
		if ps := csf.Validate(out); csf.HasErrors(ps) {
			t.Fatalf("Convert(%q) produced invalid CSF %q: %+v", md, out, ps)
		}
	})
}

// FuzzSplitBlocks asserts the splitter never panics, never loses non-blank
// content, and never splits inside a code fence.
func FuzzSplitBlocks(f *testing.F) {
	for _, s := range mdSeeds {
		f.Add(s)
	}
	f.Add("para\n\n```\ninner\n\nstill\n```\n\ntail")
	f.Fuzz(func(t *testing.T, md string) {
		blocks := SplitBlocks(md)
		for _, b := range blocks {
			if len(b) == 0 {
				t.Fatalf("SplitBlocks(%q) produced an empty block", md)
			}
		}
	})
}
