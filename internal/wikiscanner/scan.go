// Package wikiscanner owns Jira wiki block-boundary rules shared by the
// Markdown renderer and the guarded Markdown-to-wiki merge path.
package wikiscanner

import (
	"regexp"
	"strings"
)

type MarkdownBlockKind uint8

const (
	MarkdownParagraph MarkdownBlockKind = iota
	MarkdownFence
	MarkdownThematicBreak
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

// MarkdownBlockCollision reports whether a rendered paragraph line would be
// parsed as a fenced code block or thematic break instead of paragraph text.
func MarkdownBlockCollision(line string) bool {
	return MarkdownBlockType(line) != MarkdownParagraph
}

// MarkdownBlockType mirrors the block classifier used by mdcsf.SplitBlocks.
func MarkdownBlockType(line string) MarkdownBlockKind {
	body := strings.TrimSpace(line)
	if strings.HasPrefix(body, "```") {
		return MarkdownFence
	}
	if IsThematicRun(body) && (body[0] != '-' || len(body) == 3) {
		return MarkdownThematicBreak
	}
	return MarkdownParagraph
}

// EscapeMarkdownBlockCollision adds one reversible sentinel slash whenever the
// line, after any genuine leading slashes, would otherwise start a block.
func EscapeMarkdownBlockCollision(line string) string {
	n := 0
	for n < len(line) && line[n] == '\\' {
		n++
	}
	if MarkdownBlockCollision(line[n:]) {
		return `\` + line
	}
	return line
}

// UnescapeMarkdownBlockCollision reverses one sentinel slash while preserving
// every genuine leading slash. It returns the original line and block kind.
func UnescapeMarkdownBlockCollision(line string) (string, MarkdownBlockKind, bool) {
	n := 0
	for n < len(line) && line[n] == '\\' {
		n++
	}
	if n == 0 {
		return "", MarkdownParagraph, false
	}
	kind := MarkdownBlockType(line[n:])
	if kind == MarkdownParagraph {
		return "", kind, false
	}
	return line[1:], kind, true
}

// IsThematicRun reports whether body is exactly 3+ '-', '*', or '_' bytes.
func IsThematicRun(body string) bool {
	if len(body) < 3 {
		return false
	}
	c := body[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	for i := 1; i < len(body); i++ {
		if body[i] != c {
			return false
		}
	}
	return true
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
