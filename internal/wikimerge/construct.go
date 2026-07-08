package wikimerge

import (
	"regexp"
	"sort"
	"strings"
)

// The construct scanners are deliberately simple and total: each finds a class
// of notable Jira wiki markup whose meaning a markdown edit cannot reproduce, so
// dropping one is real loss the gate must surface. They over- rather than
// under-report by design (a false positive costs a --allow-loss; a false
// negative silently loses content).
var (
	cMention = regexp.MustCompile(`\[~[^\]\n]+\]`)
	cImage   = regexp.MustCompile(`!([^!\n|]+\.[A-Za-z0-9]{1,6})(?:\|[^!\n]*)?!`)
	cMono    = regexp.MustCompile(`\{\{[^}\n]*\}\}`)
	cBrace   = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9]*)`)
	cLink    = regexp.MustCompile(`\[[^\]~\n][^\]\n]*\|[^\]\n]*\]`)
)

// braceKind maps a brace-macro name to a construct kind. Named macros with a
// dedicated kind read better in the loss report; everything else is "macro".
func braceKind(name string) string {
	switch strings.ToLower(name) {
	case "panel":
		return "panel"
	case "color":
		return "color"
	case "code":
		return "code"
	case "noformat":
		return "noformat"
	case "quote":
		return "quote"
	default:
		return "macro"
	}
}

// constructCounts tallies notable wiki constructs in s by (kind, text). Monospace
// is scanned before generic braces so `{{...}}` is not miscounted as a `{`-macro,
// and the generic brace scan skips a `{` immediately followed by another `{`.
func constructCounts(s string) map[[2]string]int {
	counts := map[[2]string]int{}
	bump := func(kind, text string) { counts[[2]string{kind, clipText(text)}]++ }

	for _, m := range cMention.FindAllString(s, -1) {
		bump("mention", m)
	}
	for _, m := range cImage.FindAllString(s, -1) {
		bump("image", m)
	}
	for _, m := range cMono.FindAllString(s, -1) {
		bump("monospace", m)
	}
	for _, m := range cLink.FindAllString(s, -1) {
		bump("link", m)
	}
	for _, loc := range cBrace.FindAllStringSubmatchIndex(s, -1) {
		if loc[0] > 0 && s[loc[0]-1] == '{' {
			continue // part of a `{{monospace}}` run, already counted
		}
		name := s[loc[2]:loc[3]]
		bump(braceKind(name), "{"+name)
	}
	return counts
}

// removedConstructs returns the constructs present in base but absent (or fewer)
// in out — one entry per distinct (kind, text), sorted for a deterministic
// report and gate message.
func removedConstructs(base, out string) []Construct {
	b := constructCounts(base)
	o := constructCounts(out)
	var removed []Construct
	for key, bc := range b {
		if bc > o[key] {
			removed = append(removed, Construct{Kind: key[0], Text: key[1]})
		}
	}
	sort.Slice(removed, func(i, j int) bool {
		if removed[i].Kind != removed[j].Kind {
			return removed[i].Kind < removed[j].Kind
		}
		return removed[i].Text < removed[j].Text
	})
	return removed
}

func clipText(s string) string {
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}
