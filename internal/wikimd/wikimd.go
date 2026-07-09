// Package wikimd renders Jira wiki markup (Data Center) into a best-effort,
// lossy Markdown *read view*. It is the reverse of internal/mdwiki and, unlike
// it, is a TOTAL function: Render never returns an error and never panics on
// input it does not understand — an unrecognized construct degrades to plain
// text passthrough, so the text content is never lost.
//
// It renders the read view only and must never feed a write path: the
// <KEY>.wiki substrate remains the editable source of truth, and this output is
// regenerated best-effort on every pull (a Jira analog of
// internal/mirror.RenderMarkdown for CSF).
package wikimd

import (
	"regexp"
	"strings"
)

// Options tunes the render. Images resolves a Jira image-embed filename (the
// text inside `!name.png!`) to a local relative path as written by a
// `jira pull --assets` run (e.g. "PROJ-1.assets/10001-name.png"). A filename
// absent from the map renders as inline code signaling an unresolved image.
type Options struct {
	Images map[string]string
}

// Render converts a Jira wiki body into a Markdown read view. It is total: any
// input yields some output, degrading unknown constructs to their literal text.
// The result carries no leading/trailing blank lines so the caller controls the
// surrounding spacing.
func Render(wiki string, opts Options) string {
	lines := splitLines(wiki)
	var b strings.Builder
	renderBlocks(&b, lines, opts)
	return tidy(b.String())
}

// splitLines strips a leading BOM, normalizes CRLF / lone CR to LF, and splits
// on LF so the block scanner sees uniform lines regardless of origin.
func splitLines(s string) []string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// tidy collapses runs of 3+ newlines to a single blank line and trims blank
// lines at the document edges (spaces on a boundary hard-break line are kept).
func tidy(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.Trim(s, "\n")
}

var (
	headingRe   = regexp.MustCompile(`^h([1-6])\.[ \t]+(.*)$`)
	codeOpenRe  = regexp.MustCompile(`^\{(code|noformat)(?::([^}]*))?\}(.*)$`)
	quoteOpenRe = regexp.MustCompile(`^\{quote\}(.*)$`)
	panelOpenRe = regexp.MustCompile(`^\{panel(?::([^}]*))?\}(.*)$`)
	hrRe        = regexp.MustCompile(`^-{4,}[ \t]*$`)
	listRe      = regexp.MustCompile(`^[ \t]*([*#]+)[ \t]+(.*)$`)
	langRe      = regexp.MustCompile(`[^A-Za-z0-9#+.\-]`)
)

// renderBlocks is the line-based block scanner. Each iteration recognizes one
// block construct at the cursor (or a single paragraph/blank line) and advances.
// Block-level constructs get a trailing blank line; adjacent paragraph lines
// stay together as markdown soft breaks. Order matters: a heading/macro/table/
// list marker must be tried before the paragraph fallthrough.
func renderBlocks(b *strings.Builder, lines []string, opts Options) {
	// writeBlock emits a block-level construct framed by blank lines: a leading
	// blank separates it from a preceding paragraph or block (so a list/table/quote
	// that directly follows a paragraph line in the source is not glued to it), and
	// a trailing blank separates it from what follows. tidy later collapses the
	// redundant runs. This keeps the read view's block boundaries in lockstep with
	// the wiki block scanner, which is what makes the markdown view re-split into
	// the same blocks — the round-trip invariant behind `jira apply`.
	writeBlock := func(out string) {
		ensureBlankLine(b)
		b.WriteString(strings.TrimRight(out, "\n"))
		b.WriteString("\n\n")
	}
	i := 0
	for i < len(lines) {
		line := lines[i]
		switch {
		case strings.TrimSpace(line) == "":
			b.WriteString("\n")
			i++
		case headingRe.MatchString(line):
			m := headingRe.FindStringSubmatch(line)
			n := int(m[1][0] - '0') // m[1] is a single digit 1..6
			writeBlock(strings.Repeat("#", n) + " " + inline(strings.TrimSpace(m[2]), opts))
			i++
		case codeOpenRe.MatchString(line):
			out, next := codeBlock(lines, i)
			writeBlock(out)
			i = next
		case quoteOpenRe.MatchString(line):
			out, next := quoteBlock(lines, i, opts)
			writeBlock(out)
			i = next
		case panelOpenRe.MatchString(line):
			out, next := panelBlock(lines, i, opts)
			writeBlock(out)
			i = next
		case hrRe.MatchString(line):
			writeBlock("---")
			i++
		case strings.HasPrefix(line, "|"):
			out, next := tableBlock(lines, i, opts)
			writeBlock(out)
			i = next
		case listRe.MatchString(line):
			out, next := listBlock(lines, i, opts)
			writeBlock(out)
			i = next
		default:
			b.WriteString(inline(line, opts) + "\n")
			i++
		}
	}
}

// ensureBlankLine appends newlines so the builder ends with a blank line (an empty
// builder is left untouched, and a trailing blank line is not doubled). It is how
// a block-level construct guarantees a blank separator from whatever preceded it.
func ensureBlankLine(b *strings.Builder) {
	s := b.String()
	switch {
	case s == "":
		return
	case strings.HasSuffix(s, "\n\n"):
		return
	case strings.HasSuffix(s, "\n"):
		b.WriteString("\n")
	default:
		b.WriteString("\n\n")
	}
}

// codeBlock renders a {code}/{noformat} macro as a fenced code block. Content is
// verbatim (no inline wiki parsing). An unterminated macro consumes the rest of
// the document as body rather than losing it (best-effort, never an error).
func codeBlock(lines []string, i int) (string, int) {
	m := codeOpenRe.FindStringSubmatch(lines[i])
	macro, params, rest := m[1], m[2], m[3]
	lang := ""
	if macro == "code" {
		lang = codeLang(params)
	}
	closeTag := "{" + macro + "}"
	// One-liner: {code}body{code} all on the opening line.
	if idx := strings.Index(rest, closeTag); idx >= 0 {
		return fence(lang, rest[:idx]), i + 1
	}
	var body []string
	if rest != "" {
		body = append(body, rest)
	}
	for j := i + 1; j < len(lines); j++ {
		if idx := strings.Index(lines[j], closeTag); idx >= 0 {
			if pre := lines[j][:idx]; pre != "" {
				body = append(body, pre)
			}
			return fence(lang, strings.Join(body, "\n")), j + 1
		}
		body = append(body, lines[j])
	}
	return fence(lang, strings.Join(body, "\n")), len(lines)
}

// fence wraps body in a markdown code fence at least one backtick longer than
// the longest backtick run inside the body, so content containing ``` (or
// longer runs) cannot close the fence early.
func fence(lang, body string) string {
	body = strings.Trim(body, "\n")
	n := 3
	if run := longestBacktickRun(body); run >= n {
		n = run + 1
	}
	marker := strings.Repeat("`", n)
	return marker + lang + "\n" + body + "\n" + marker + "\n"
}

// longestBacktickRun returns the length of the longest run of consecutive
// backticks in s.
func longestBacktickRun(s string) (longest int) {
	run := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	return longest
}

// codeLang extracts a fenced-block language from {code} parameters. It accepts
// the bare `{code:go}` form and the parameterized `{code:title=x|language=go}`
// form, and drops any character that would break the markdown fence.
func codeLang(params string) string {
	if params == "" {
		return ""
	}
	for _, part := range strings.Split(params, "|") {
		part = strings.TrimSpace(part)
		if k, v, ok := strings.Cut(part, "="); ok {
			if strings.EqualFold(strings.TrimSpace(k), "language") {
				return langRe.ReplaceAllString(strings.TrimSpace(v), "")
			}
			continue
		}
		return langRe.ReplaceAllString(part, "")
	}
	return ""
}

// quoteBlock renders a {quote} macro as a `>` blockquote. Inner content is
// rendered recursively so it keeps its own structure (headings, lists, code).
func quoteBlock(lines []string, i int, opts Options) (string, int) {
	inner, next := collectMacroBody(lines, i, quoteOpenRe.FindStringSubmatch(lines[i])[1], "{quote}")
	return blockquote(Render(inner, opts)), next
}

// panelBlock renders a {panel}/{panel:title=X} macro as a blockquote; a title
// becomes a leading bold line.
func panelBlock(lines []string, i int, opts Options) (string, int) {
	m := panelOpenRe.FindStringSubmatch(lines[i])
	title := panelTitle(m[1])
	inner, next := collectMacroBody(lines, i, m[2], "{panel}")
	body := Render(inner, opts)
	content := body
	if title != "" {
		content = "**" + inline(title, opts) + "**"
		if body != "" {
			content += "\n\n" + body
		}
	}
	return blockquote(content), next
}

// collectMacroBody gathers the raw inner text of a paired brace macro whose
// opening line is lines[i]. rest is the text after the opening tag on that same
// line; closeTag is the terminator (e.g. "{quote}"). It handles the one-liner
// form, the multi-line form, and an unterminated macro (body runs to EOF).
// Returns the joined inner text and the index of the line after the macro.
func collectMacroBody(lines []string, i int, rest, closeTag string) (string, int) {
	if idx := strings.Index(rest, closeTag); idx >= 0 {
		return rest[:idx], i + 1
	}
	var inner []string
	if strings.TrimSpace(rest) != "" {
		inner = append(inner, rest)
	}
	for j := i + 1; j < len(lines); j++ {
		if idx := strings.Index(lines[j], closeTag); idx >= 0 {
			if pre := lines[j][:idx]; strings.TrimSpace(pre) != "" {
				inner = append(inner, pre)
			}
			return strings.Join(inner, "\n"), j + 1
		}
		inner = append(inner, lines[j])
	}
	return strings.Join(inner, "\n"), len(lines)
}

func panelTitle(params string) string {
	for _, part := range strings.Split(params, "|") {
		if k, v, ok := strings.Cut(part, "="); ok && strings.EqualFold(strings.TrimSpace(k), "title") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// blockquote prefixes each line of s with "> " (a blank inner line becomes a
// bare ">"), and appends the trailing newline block constructs carry.
func blockquote(s string) string {
	s = strings.Trim(s, "\n")
	if s == "" {
		return ">\n"
	}
	lines := strings.Split(s, "\n")
	for k, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[k] = ">"
		} else {
			lines[k] = "> " + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// tableBlock gathers consecutive `|`-prefixed lines and renders them as a GFM
// table. The first `||`-delimited row is the header; if there is none, the first
// row is used as the header (pinned in tests).
func tableBlock(lines []string, i int, opts Options) (string, int) {
	j := i
	var rows []string
	for j < len(lines) && strings.HasPrefix(lines[j], "|") {
		rows = append(rows, lines[j])
		j++
	}
	return renderTable(rows, opts), j
}

func renderTable(rows []string, opts Options) string {
	var parsed [][]string
	headerIdx := -1
	for _, r := range rows {
		cells, isHeader := parseRow(r)
		if isHeader && headerIdx < 0 {
			headerIdx = len(parsed)
		}
		parsed = append(parsed, cells)
	}
	width := 0
	for _, r := range parsed {
		if len(r) > width {
			width = len(r)
		}
	}
	if len(parsed) == 0 || width == 0 {
		// A degenerate `|`-run with no real cells (e.g. a lone "|") is not a table.
		// Passing the raw lines through as plain text keeps wikimd total ("text
		// content is never lost") and lets the view round-trip back to the source.
		return strings.Join(rows, "\n")
	}
	hdr := headerIdx
	if hdr < 0 {
		hdr = 0 // no `||` header row: promote the first row
	}
	// GFM requires the header first; emit the chosen header row, the separator,
	// then every other row in original order.
	order := []int{hdr}
	for idx := range parsed {
		if idx != hdr {
			order = append(order, idx)
		}
	}
	var b strings.Builder
	writeRow := func(cells []string) {
		b.WriteString("|")
		for c := 0; c < width; c++ {
			cell := ""
			if c < len(cells) {
				cell = cells[c]
			}
			b.WriteString(" " + tableCell(cell, opts) + " |")
		}
		b.WriteString("\n")
	}
	writeRow(parsed[order[0]])
	b.WriteString("|")
	for c := 0; c < width; c++ {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, idx := range order[1:] {
		writeRow(parsed[idx])
	}
	return b.String()
}

// parseRow splits one wiki table row into cells and reports whether it is a
// header row (delimited by `||` rather than `|`).
func parseRow(row string) (cells []string, header bool) {
	row = strings.TrimSpace(row)
	sep := "|"
	if strings.HasPrefix(row, "||") {
		header = true
		sep = "||"
	}
	trimmed := strings.Trim(row, "|")
	if trimmed == "" {
		return nil, header
	}
	for _, c := range strings.Split(trimmed, sep) {
		cells = append(cells, strings.TrimSpace(c))
	}
	return cells, header
}

// tableCell renders a cell's inline markup and escapes pipes / newlines so the
// GFM row structure survives.
func tableCell(s string, opts Options) string {
	out := inline(s, opts)
	out = strings.ReplaceAll(out, "\n", " ")
	out = strings.ReplaceAll(out, "|", "\\|")
	return out
}

// listBlock gathers consecutive wiki list lines (`*`/`#` marker runs) and emits
// nested markdown lists: `-` for unordered, `1.` for ordered, two-space indent
// per nesting level. The last marker character of a run decides the item's kind.
func listBlock(lines []string, i int, opts Options) (string, int) {
	j := i
	var b strings.Builder
	for j < len(lines) {
		m := listRe.FindStringSubmatch(lines[j])
		if m == nil {
			break
		}
		markers, text := m[1], m[2]
		depth := len(markers)
		marker := "- "
		if markers[depth-1] == '#' {
			marker = "1. "
		}
		b.WriteString(strings.Repeat("  ", depth-1) + marker + inline(strings.TrimSpace(text), opts) + "\n")
		j++
	}
	return b.String(), j
}
