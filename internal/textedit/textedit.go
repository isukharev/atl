// Package textedit implements precise, whitespace/invisible-tolerant string
// replacement for server-derived text such as Confluence Storage Format
// bodies. Real CSF is usually one huge line salted with invisible bytes
// (U+00A0, zero-width characters, &nbsp; entities), which defeats exact-match
// editing: the needle *looks* identical but never matches.
//
// The matcher runs layered passes over an index-mapped canonical stream —
// exact bytes first, then invisible-tolerant, then whitespace-run-tolerant —
// and splices the replacement into exactly the matched original byte range,
// preserving every surrounding byte verbatim. It never guesses: zero matches
// and ambiguous matches are refusals, not fallbacks to similarity scoring (a
// wrong splice into a live wiki page is worse than an error).
package textedit

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Pass identifies which normalization layer produced the match.
type Pass string

const (
	PassExact      Pass = "exact"
	PassInvisible  Pass = "invisible"
	PassWhitespace Pass = "whitespace"
)

// Match is one located occurrence of the needle in the original text.
type Match struct {
	Start int // byte offset in the original text
	End   int // byte offset just past the matched region
}

// Result describes a successful Replace.
type Result struct {
	Text    string // full text after the splice(s)
	Pass    Pass   // which pass matched
	Matches []Match
}

// NoMatchError reports that no pass found the needle. Context holds a
// repr()-style dump of the closest candidate region so the caller can see the
// hidden bytes that broke exact matching.
type NoMatchError struct {
	Context string
}

func (e *NoMatchError) Error() string {
	if e.Context == "" {
		return "old text not found"
	}
	return "old text not found; closest region: " + e.Context
}

// AmbiguousError reports that a pass matched more than once and All was not
// requested.
type AmbiguousError struct {
	Pass    Pass
	Matches []Match
}

func (e *AmbiguousError) Error() string {
	offs := make([]string, 0, len(e.Matches))
	for _, m := range e.Matches {
		offs = append(offs, fmt.Sprintf("%d", m.Start))
	}
	return fmt.Sprintf("old text matches %d times (%s pass) at byte offsets %s; make it unique or pass --all",
		len(e.Matches), e.Pass, strings.Join(offs, ", "))
}

// canon is a canonicalized view of a text with a map back to original byte
// offsets. canon[i] corresponds to original bytes [pos[i], pos[i+1]).
type canon struct {
	s   []rune
	pos []int // len(s)+1; pos[len(s)] = len(original)
}

// zero-width characters that are dropped entirely in tolerant passes:
// ZWSP, ZWNJ, ZWJ, BOM/ZWNBSP, word joiner, soft hyphen.
func isZeroWidth(r rune) bool {
	switch r {
	case '\u200b', '\u200c', '\u200d', '\ufeff', '\u2060', '\u00ad':
		return true
	}
	return false
}

// entitySpace returns the byte length of an XML/HTML entity encoding of a
// non-breaking space starting at s[i] ("&nbsp;", "&#160;", "&#xa0;", "&#xA0;"),
// or 0 when there is none.
func entitySpace(s string, i int) int {
	for _, e := range [...]string{"&nbsp;", "&#160;", "&#xa0;", "&#xA0;"} {
		if strings.HasPrefix(s[i:], e) {
			return len(e)
		}
	}
	return 0
}

// canonicalize maps a text to the canonical rune stream of the given pass.
// PassInvisible: NBSP (raw or entity) becomes a plain space; zero-width runes
// vanish. PassWhitespace: additionally, any run of whitespace (including the
// NBSP forms) collapses to a single space.
func canonicalize(s string, pass Pass) canon {
	c := canon{}
	i := 0
	emit := func(r rune, at int) {
		c.s = append(c.s, r)
		c.pos = append(c.pos, at)
	}
	for i < len(s) {
		if n := entitySpace(s, i); n > 0 {
			emit(' ', i)
			i += n
			continue
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		switch {
		case isZeroWidth(r):
			// dropped: no canonical rune
		case r == '\u00a0':
			emit(' ', i)
		default:
			emit(r, i)
		}
		i += sz
	}
	c.pos = append(c.pos, len(s))
	if pass == PassWhitespace {
		c = collapseWS(c)
	}
	return c
}

// collapseWS folds every whitespace run in an already-canonical stream into a
// single space anchored at the run's first byte.
func collapseWS(in canon) canon {
	out := canon{}
	i := 0
	for i < len(in.s) {
		if unicode.IsSpace(in.s[i]) {
			out.s = append(out.s, ' ')
			out.pos = append(out.pos, in.pos[i])
			for i < len(in.s) && unicode.IsSpace(in.s[i]) {
				i++
			}
			continue
		}
		out.s = append(out.s, in.s[i])
		out.pos = append(out.pos, in.pos[i])
		i++
	}
	out.pos = append(out.pos, in.pos[len(in.s)])
	return out
}

// find returns every occurrence of needle in hay (canonical streams), mapped
// back to original byte ranges of hay.
func find(hay, needle canon) []Match {
	if len(needle.s) == 0 {
		return nil
	}
	var out []Match
	limit := len(hay.s) - len(needle.s)
	for i := 0; i <= limit; {
		ok := true
		for j := range needle.s {
			if hay.s[i+j] != needle.s[j] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, Match{Start: hay.pos[i], End: hay.pos[i+len(needle.s)]})
			i += len(needle.s) // non-overlapping, left to right
			continue
		}
		i++
	}
	return out
}

// trimCanon strips leading/trailing canonical whitespace from a needle so the
// whitespace pass is not defeated by the needle's own surrounding spaces
// anchoring to different byte runs. Matching stays anchored to visible text.
func trimCanon(in canon) canon {
	lo, hi := 0, len(in.s)
	for lo < hi && unicode.IsSpace(in.s[lo]) {
		lo++
	}
	for hi > lo && unicode.IsSpace(in.s[hi-1]) {
		hi--
	}
	out := canon{s: in.s[lo:hi], pos: append([]int{}, in.pos[lo:hi+1]...)}
	return out
}

// Replace locates old in text via layered passes and splices new into the
// matched byte range(s). With all=false a multi-match is an AmbiguousError;
// with all=true every match of the winning pass is replaced (non-overlapping,
// left to right).
func Replace(text, old, new string, all bool) (*Result, error) {
	if old == "" {
		return nil, fmt.Errorf("old text must not be empty")
	}
	// A malformed needle could byte-match mid-rune (UTF-8 self-synchronization
	// only holds for valid sequences), and a malformed replacement would
	// corrupt the file — refuse both up front.
	if !utf8.ValidString(old) {
		return nil, fmt.Errorf("old text is not valid UTF-8")
	}
	if !utf8.ValidString(new) {
		return nil, fmt.Errorf("new text is not valid UTF-8")
	}
	type attempt struct {
		pass    Pass
		matches []Match
	}
	var attempts []attempt

	// Pass 1: exact bytes.
	var exact []Match
	for i := 0; ; {
		j := strings.Index(text[i:], old)
		if j < 0 {
			break
		}
		exact = append(exact, Match{Start: i + j, End: i + j + len(old)})
		i += j + len(old)
	}
	attempts = append(attempts, attempt{PassExact, exact})

	// Pass 2: invisible-tolerant. Pass 3: whitespace-run-tolerant.
	for _, pass := range [...]Pass{PassInvisible, PassWhitespace} {
		hay := canonicalize(text, pass)
		needle := canonicalize(old, pass)
		if pass == PassWhitespace {
			needle = trimCanon(needle)
		}
		attempts = append(attempts, attempt{pass, find(hay, needle)})
	}

	for _, a := range attempts {
		switch {
		case len(a.matches) == 1, len(a.matches) > 1 && all:
			return splice(text, new, a.pass, a.matches), nil
		case len(a.matches) > 1:
			return nil, &AmbiguousError{Pass: a.pass, Matches: a.matches}
		}
	}
	return nil, &NoMatchError{Context: nearestContext(text, old)}
}

func splice(text, repl string, pass Pass, matches []Match) *Result {
	var b strings.Builder
	prev := 0
	for _, m := range matches {
		b.WriteString(text[prev:m.Start])
		b.WriteString(repl)
		prev = m.End
	}
	b.WriteString(text[prev:])
	return &Result{Text: b.String(), Pass: pass, Matches: matches}
}

// nearestContext locates the longest matchable prefix of old (via the most
// tolerant pass) and returns a quoted dump of the original bytes around it,
// so hidden characters become visible to the caller.
func nearestContext(text, old string) string {
	hay := canonicalize(text, PassWhitespace)
	needle := trimCanon(canonicalize(old, PassWhitespace))
	for n := len(needle.s); n >= 3; n = n * 3 / 4 {
		part := canon{s: needle.s[:n], pos: needle.pos[: n+1 : n+1]}
		if ms := find(hay, part); len(ms) > 0 {
			start, end := ms[0].Start, ms[0].End
			lo, hi := start-20, end+40
			if lo < 0 {
				lo = 0
			}
			if hi > len(text) {
				hi = len(text)
			}
			return fmt.Sprintf("%q (bytes %d..%d)", text[lo:hi], lo, hi)
		}
	}
	return ""
}
