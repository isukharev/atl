package textedit

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzReplace hammers the matcher with arbitrary server-derived bytes. The
// invariants: never panic; a successful splice yields exactly
// prefix+new+…+suffix built from reported match offsets; offsets are ordered,
// in-bounds, non-overlapping, and never split a UTF-8 rune of valid input.
// Seeds double as deterministic regression tests under plain `go test`.
func FuzzReplace(f *testing.F) {
	f.Add("<p>Запрос предназначен для получения</p>", "предназначен для", "x", false)
	f.Add("<td>нет,&nbsp;переношу в&#160;sandbox</td>", "нет, переношу в sandbox", "да", true)
	f.Add("a\u200bb\ufeffc", "abc", "", false)
	f.Add("&nbsp;&nbsp;&nbsp;", " ", "y", true)
	f.Add("<p>aa aa</p>", "aa", "b", false)
	f.Add("", "x", "y", false)
	f.Add("&#xa0;tail", " tail", "t", false)
	f.Add(strings.Repeat("да ", 50), "да", "no", true)

	f.Fuzz(func(t *testing.T, text, old, new string, all bool) {
		r, err := Replace(text, old, new, all)
		if err != nil {
			return
		}
		if len(r.Matches) == 0 {
			t.Fatal("success with zero matches")
		}
		prev := 0
		var b strings.Builder
		for _, m := range r.Matches {
			if m.Start < prev || m.End < m.Start || m.End > len(text) {
				t.Fatalf("bad match range %+v (prev %d, len %d)", m, prev, len(text))
			}
			if utf8.ValidString(text) {
				if !utf8.ValidString(text[m.Start:m.End]) {
					t.Fatalf("match %+v splits a rune", m)
				}
			}
			b.WriteString(text[prev:m.Start])
			b.WriteString(new)
			prev = m.End
		}
		b.WriteString(text[prev:])
		if r.Text != b.String() {
			t.Fatalf("splice mismatch:\n got %q\nwant %q", r.Text, b.String())
		}
	})
}
