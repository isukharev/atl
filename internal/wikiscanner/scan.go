// Package wikiscanner owns Jira wiki block-boundary rules shared by the
// Markdown renderer and the guarded Markdown-to-wiki merge path.
package wikiscanner

import (
	"regexp"
	"strings"
)

var listRe = regexp.MustCompile(`^[ \t]*([*#]+)[ \t]+(.*)$`)

// ParseListLine recognizes a Jira wiki list line and returns its marker run and
// body. Both renderer and merge scanner use this function so their block
// boundaries cannot drift independently.
func ParseListLine(line string) (markers, body string, ok bool) {
	m := listRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// IsListLine reports whether line begins a Jira wiki list item.
func IsListLine(line string) bool {
	_, _, ok := ParseListLine(line)
	return ok
}

// TableRowEnd returns the last physical line of one logical Jira table row.
// lineAt lets scanners with different line representations share the exact
// boundary rule without allocating a second copy of the input.
func TableRowEnd(lineCount, start int, lineAt func(int) string) int {
	if start < 0 || start >= lineCount || strings.HasSuffix(strings.TrimSpace(lineAt(start)), "|") {
		return start
	}
	for i := start + 1; i < lineCount; i++ {
		line := lineAt(i)
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "|") {
			return start
		}
		if strings.HasSuffix(strings.TrimSpace(line), "|") {
			return i
		}
	}
	return start
}
