package wikimd

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzRender ingests server-controlled Jira wiki bytes: Render must never panic
// or hang, and must keep the output valid UTF-8 whenever the input is. The
// seeds cover every construct the renderer handles plus known nasties
// (unterminated macros, mixed CRLF, BOM, cyrillic, huge lines, nested markers,
// NUL) and double as deterministic regression tests under plain `go test`.
func FuzzRender(f *testing.F) {
	seeds := []string{
		"h1. Title\nh6. Deep\n\npara *bold* _em_ {{mono}} -strike-",
		"* a\n* b\n** c\n# 1\n## 2\n#* mix",
		"||H1||H2||\n|a|b|\n|c|d|",
		"|no|header|\n|row|two|",
		"[text|https://x] [https://y] [~user.name] [TODO]",
		"!shot.png! !shot.png|thumbnail,width=300! !https://h/a.png!",
		"{code:go}\nfmt.Println()\n{code}",
		"{code:title=x|language=java}\nSystem.out;\n{code}",
		"{noformat}\nverbatim *stuff*\n{noformat}",
		"{quote}\nquoted line\n{quote}",
		"{panel:title=Note}\nbody\n{panel}",
		"{color:red}alert{color} normal",
		"line1\\\\line2\n----\n",
		"{code}\nunterminated code body",
		"{quote}\nunterminated quote",
		"\ufeffBOM start\r\nmixed\rCRLF\n",
		"snake_case well-known 3 - 5 a*b*c",
		"кириллица с *акцентом* и {скобками} и [~пользователь]",
		strings.Repeat("* very long list item text ", 500),
		strings.Repeat("*", 4000),
		"\x00\x01\x02 control bytes with *bold*",
		"![[weird]] !! {{}} [] || {} ~~ __",
		"{{mono with ` backtick}}",
		"Done! v1.2! yes",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, wiki string) {
		out := Render(wiki, Options{Images: map[string]string{"shot.png": "a.assets/1-shot.png"}})
		if utf8.ValidString(wiki) && !utf8.ValidString(out) {
			t.Fatalf("valid-UTF-8 input produced invalid output: in=%q out=%q", wiki, out)
		}
	})
}
