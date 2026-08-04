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

var (
	listRe           = regexp.MustCompile(`^[ \t]*([*#]+)[ \t]+(.*)$`)
	headingRe        = regexp.MustCompile(`^h([1-6])\.[ \t]+(.*)$`)
	codeOpenRe       = regexp.MustCompile(`^\{(code|noformat)(?::([^}]*))?\}(.*)$`)
	quoteOpenRe      = regexp.MustCompile(`^\{quote\}(.*)$`)
	panelOpenRe      = regexp.MustCompile(`^\{panel(?::([^}]*))?\}(.*)$`)
	horizontalRuleRe = regexp.MustCompile(`^-{4,}[ \t]*$`)
)

// ParseHeading recognizes a Jira wiki heading and returns its numeric level
// and inline body.
func ParseHeading(line string) (level int, body string, ok bool) {
	m := headingRe.FindStringSubmatch(line)
	if m == nil {
		return 0, "", false
	}
	return int(m[1][0] - '0'), m[2], true
}

// IsHeading reports whether line is a Jira wiki heading.
func IsHeading(line string) bool {
	_, _, ok := ParseHeading(line)
	return ok
}

// ParseCodeOpen recognizes a {code} or {noformat} opening line.
func ParseCodeOpen(line string) (macro, params, rest string, ok bool) {
	m := codeOpenRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// IsCodeOpen reports whether line opens a Jira wiki code or noformat macro.
func IsCodeOpen(line string) bool {
	_, _, _, ok := ParseCodeOpen(line)
	return ok
}

// ParseQuoteOpen recognizes a {quote} opening line and returns trailing text.
func ParseQuoteOpen(line string) (rest string, ok bool) {
	m := quoteOpenRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// IsQuoteOpen reports whether line opens a Jira wiki quote macro.
func IsQuoteOpen(line string) bool {
	_, ok := ParseQuoteOpen(line)
	return ok
}

// ParsePanelOpen recognizes a {panel} opening line.
func ParsePanelOpen(line string) (params, rest string, ok bool) {
	m := panelOpenRe.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// IsPanelOpen reports whether line opens a Jira wiki panel macro.
func IsPanelOpen(line string) bool {
	_, _, ok := ParsePanelOpen(line)
	return ok
}

// IsHorizontalRule reports whether line is a Jira wiki horizontal rule.
func IsHorizontalRule(line string) bool { return horizontalRuleRe.MatchString(line) }

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

// MarkdownBlockType classifies lines for reversible paragraph-collision
// escaping. It intentionally preserves the broad legacy fence classification;
// actual fence scanners use ParseMarkdownFenceOpen and IsMarkdownFenceClose.
func MarkdownBlockType(line string) MarkdownBlockKind {
	body := strings.TrimSpace(line)
	// Keep collision recognition deliberately broader than the actual fence
	// grammar. Existing generated Jira views escaped indented and backtick-in-info
	// lines this way, so apply must continue to reverse those sentinel slashes.
	if strings.HasPrefix(body, "```") {
		return MarkdownFence
	}
	if IsThematicRun(body) && (body[0] != '-' || len(body) == 3) {
		return MarkdownThematicBreak
	}
	return MarkdownParagraph
}

// ParseMarkdownFenceOpen recognizes a backtick fence opener. Markdown permits
// at most three leading spaces, requires a run of at least three backticks,
// and forbids backticks in the info string. The returned run length lets a
// scanner distinguish a real closer from a shorter run inside the body.
func ParseMarkdownFenceOpen(line string) (run int, info string, ok bool) {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	start := i
	for i < len(line) && line[i] == '`' {
		i++
	}
	if i-start < 3 || strings.ContainsRune(line[i:], '`') {
		return 0, "", false
	}
	return i - start, line[i:], true
}

// IsMarkdownFenceClose reports whether line closes a backtick fence of the
// given opener length. A closer may be longer than its opener, but after the
// run it may contain whitespace only.
func IsMarkdownFenceClose(line string, openerRun int) bool {
	if openerRun < 3 {
		return false
	}
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	start := i
	for i < len(line) && line[i] == '`' {
		i++
	}
	return i-start >= openerRun && strings.TrimSpace(line[i:]) == ""
}

// NormalizeMarkdownFenceInfo returns the trimmed form of an inert backtick
// fence info string. Backticks or line breaks could change fence structure and
// therefore cannot be represented in a generated opener.
func NormalizeMarkdownFenceInfo(info string) (string, bool) {
	info = strings.TrimSpace(info)
	if strings.ContainsAny(info, "`\r\n") {
		return "", false
	}
	return info, true
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
