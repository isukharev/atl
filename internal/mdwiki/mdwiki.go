// Package mdwiki converts a strict markdown subset into Jira wiki markup
// (Data Center). It exists so agents can author issue descriptions and
// comments in markdown (`--from-md`) instead of hand-writing wiki syntax —
// Jira does not interpret Markdown, so `**bold**` or ``` fences pasted raw
// publish as literal characters. Like internal/mdcsf, the converter fails
// closed: any construct outside the subset (or one that wiki cannot express
// unambiguously) returns an *UnsupportedError naming it, never partial or
// guessed output.
package mdwiki

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/mdcsf"
)

// UnsupportedError reports a markdown construct outside the supported subset.
type UnsupportedError struct {
	Construct string // human-readable name of the construct
	Detail    string // the offending line or span, for actionable messages
}

func (e *UnsupportedError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("unsupported markdown construct: %s (%q)", e.Construct, e.Detail)
	}
	return "unsupported markdown construct: " + e.Construct
}

func unsupported(construct, detail string) error {
	return &UnsupportedError{Construct: construct, Detail: detail}
}

// ConvertDocument turns a whole markdown document into a Jira wiki body:
// blocks split the same way the md edit surface does (mdcsf.SplitBlocks),
// each converted to wiki markup, joined by blank lines. Fail-closed like
// mdcsf.ConvertDocument — the first unconvertible block aborts with an error
// naming it (1-based index and first line); an empty document is an error.
func ConvertDocument(md string) (string, error) {
	md = strings.TrimPrefix(md, "\ufeff") // Windows-authored files may carry a BOM
	blocks := mdcsf.SplitBlocks(md)
	if len(blocks) == 0 {
		return "", unsupported("empty document", "")
	}
	parts := make([]string, 0, len(blocks))
	for i, block := range blocks {
		out, err := convertBlock(block, Options{})
		if err != nil {
			first, _, _ := strings.Cut(strings.TrimSpace(block), "\n")
			return "", fmt.Errorf("block %d (%q): %w", i+1, clip(first), err)
		}
		parts = append(parts, out)
	}
	return strings.Join(parts, "\n\n"), nil
}

// ConvertBlock converts a single, already-split markdown block (one
// mdcsf.SplitBlocks unit) into Jira wiki markup. It is the block-level entry
// point used by internal/wikimerge, which does its own block splitting/
// alignment and needs to convert one changed block at a time. Fail-closed like
// ConvertDocument: an unconvertible block returns an *UnsupportedError.
func ConvertBlock(block string) (string, error) {
	return convertBlock(block, Options{})
}

// Options tune conversion of Markdown that came from a generated Jira view.
// HeadingOffset reverses wikimd.Options.HeadingOffset. Standalone --from-md
// conversion uses the zero value and keeps ordinary Markdown heading levels.
type Options struct {
	HeadingOffset int
}

// ConvertBlockWithOptions converts one block under an explicit generated-view
// heading contract. It is used by wikimerge for changed Description blocks.
func ConvertBlockWithOptions(block string, opts Options) (string, error) {
	return convertBlock(block, opts)
}

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*$`)
var jiraHeadingSuffixRe = regexp.MustCompile(`^######\s+(.*?)\s+<!-- atl:jira-heading level=([1-6]) -->\s*$`)
var listItemRe = regexp.MustCompile(`^(\s*)(?:([-*+])|(\d+)[.)])\s+(.*)$`)

// blockish matches a paragraph line that Jira would parse as block markup of
// its own (heading, blockquote line, horizontal rule) — emitting it verbatim
// would silently change structure, so such text is refused instead. It is
// deliberately un-multiline: convertParagraph tests it per converted line.
var blockish = regexp.MustCompile(`^(?:h[1-6]\.\s|bq\.\s|-{4,}\s*$)`)

// wikiListLine matches a converted paragraph line whose leading marker Jira
// would parse as a list item — a silent structure change now that intra-
// paragraph lines join with a real newline. inline() already backslash-escapes
// a leading `*`/`#` (Jira's bullet/numbered markers), so in practice this only
// fires for a leading `- ` (a dash bullet), which inline() leaves literal; the
// escape prepended in convertParagraph renders it as the author's literal text.
var wikiListLine = regexp.MustCompile(`^[*#-]+[ \t]`)

func convertBlock(block string, opts Options) (string, error) {
	block = strings.TrimRight(block, "\n")
	if strings.TrimSpace(block) == "" {
		return "", unsupported("empty block", "")
	}
	if !utf8.ValidString(block) {
		return "", unsupported("invalid UTF-8", "")
	}
	for _, r := range block {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xFFFE || r == 0xFFFF {
			return "", unsupported("control character", fmt.Sprintf("U+%04X", r))
		}
	}
	lines := strings.Split(block, "\n")
	first := strings.TrimSpace(lines[0])
	markedHeading := jiraHeadingSuffixRe.MatchString(lines[0])
	if strings.Contains(block, "<!-- atl:jira-heading") && !markedHeading {
		return "", unsupported("malformed reserved atl heading marker", block)
	}
	switch {
	case markedHeading:
		return convertMarkedHeading(lines, opts)
	case strings.HasPrefix(first, "```"):
		return convertFence(lines)
	case headingRe.MatchString(lines[0]):
		return convertHeading(lines, opts)
	case isThematicBreak(first) && len(lines) == 1:
		return "----", nil
	case strings.HasPrefix(first, ">"):
		return convertBlockquote(lines, opts)
	case strings.HasPrefix(first, "|"):
		return convertTable(lines)
	case listItemRe.MatchString(lines[0]):
		return convertList(lines)
	default:
		return convertParagraph(lines)
	}
}

func isThematicBreak(trimmed string) bool {
	return trimmed == "---" || trimmed == "***" || trimmed == "___"
}

func convertHeading(lines []string, opts Options) (string, error) {
	if len(lines) != 1 {
		return "", unsupported("heading with continuation lines", lines[1])
	}
	m := headingRe.FindStringSubmatch(lines[0])
	offset := opts.HeadingOffset
	if offset < 0 || offset > 5 {
		return "", unsupported("invalid heading offset", fmt.Sprint(offset))
	}
	mdLevel := len(m[1])
	if offset > 0 && mdLevel == 6 {
		return "", unsupported("generated-view H6 without atl heading marker", lines[0])
	}
	wikiLevel := mdLevel - offset
	if wikiLevel < 1 {
		return "", unsupported("heading level reserved for generated sections", lines[0])
	}
	body, err := inline(m[2])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("h%d. %s", wikiLevel, body), nil
}

func convertMarkedHeading(lines []string, opts Options) (string, error) {
	if opts.HeadingOffset <= 0 {
		return "", unsupported("atl heading marker outside a generated view", lines[0])
	}
	if len(lines) != 1 {
		return "", unsupported("marked Jira heading with continuation lines", strings.Join(lines, " "))
	}
	marked := jiraHeadingSuffixRe.FindStringSubmatch(lines[0])
	if marked == nil {
		return "", unsupported("invalid marked Jira heading", lines[0])
	}
	level := int(marked[2][0] - '0')
	if level+opts.HeadingOffset < 6 {
		return "", unsupported("unnecessary atl heading marker", marked[0])
	}
	body, err := inline(marked[1])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("h%d. %s", level, body), nil
}

// convertParagraph converts a soft-wrapped markdown paragraph to wiki, joining
// its lines with a real newline (not a space): Jira renders an intra-paragraph
// `\n` as a visual line break, and the `.md` read view shows those breaks as
// adjacent lines, so preserving them keeps the line structure the author sees.
// Inline markup is converted per line — running inline() over a `\n`-joined
// string would let an emphasis pair span the break and emit markup Jira will
// not render — and the fail-closed guards run per line: any inner line Jira
// would parse as its own block (heading/bq/hr) or as a list item is refused or
// escaped so the newline never silently changes structure.
func convertParagraph(lines []string) (string, error) {
	out := make([]string, len(lines))
	for i, ln := range trimAll(lines) {
		if u, ok, err := unescapeBlockLine(ln); ok {
			if err != nil {
				return "", err
			}
			out[i] = u
			continue
		}
		body, err := inline(ln)
		if err != nil {
			return "", err
		}
		if blockish.MatchString(body) {
			return "", unsupported("paragraph that Jira would parse as block markup", clip(body))
		}
		if wikiListLine.MatchString(body) {
			body = `\` + body // neutralize a leading list marker; renders literal
		}
		out[i] = body
	}
	return strings.Join(out, "\n"), nil
}

// unescapeBlockLine reverses the paragraph-line escape wikimd adds (issue #167):
// a view line "\"+X whose X would otherwise re-parse in markdown as a code fence
// (a leading 3+ backtick run) or a thematic break (a 3+ run of `-`/`*`/`_`). The
// marker run is wiki-inert, so it is emitted verbatim — reproducing the base wiki
// bytes so an edited paragraph round-trips byte-identically. For a fence the
// remainder after the run must STILL go through inline() (it may carry
// emphasis/links that wikimd converted from wiki); the run itself must not be fed
// to inline(), which would pair the backticks into code spans. Returns ok=false
// for any other line so the caller falls through to normal conversion — including
// the generic `\`+escapable handling, which is left intact.
func unescapeBlockLine(ln string) (out string, ok bool, err error) {
	if !strings.HasPrefix(ln, `\`) {
		return "", false, nil
	}
	rest := ln[1:]
	body := rest[leadingIndent(rest):]
	switch {
	case strings.HasPrefix(body, "```"):
		n := 0
		for n < len(body) && body[n] == '`' {
			n++
		}
		conv, e := inline(body[n:])
		if e != nil {
			return "", true, e
		}
		return rest[:len(rest)-len(body)+n] + conv, true, nil
	case isThematicRun(body):
		return rest, true, nil
	default:
		return "", false, nil
	}
}

// leadingIndent returns the count of up to 3 leading spaces — markdown's fence /
// thematic-break indent tolerance — mirroring wikimd's escape detection.
func leadingIndent(s string) int {
	n := 0
	for n < 3 && n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// isThematicRun reports whether body is exactly a run of 3+ of a single `-`, `*`,
// or `_` — a markdown thematic break. Mirrors wikimd's escape detection.
func isThematicRun(body string) bool {
	if len(body) < 3 {
		return false
	}
	c := body[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	for i := 0; i < len(body); i++ {
		if body[i] != c {
			return false
		}
	}
	return true
}

// codeLangRe: languages Jira accepts in {code:lang}; anything else would leak
// into the macro parameter syntax.
var codeLangRe = regexp.MustCompile(`^[A-Za-z0-9#+.-]*$`)

func convertFence(lines []string) (string, error) {
	open := strings.TrimSpace(lines[0])
	lang := strings.TrimSpace(strings.TrimPrefix(open, "```"))
	if !codeLangRe.MatchString(lang) {
		return "", unsupported("code fence info string", open)
	}
	last := len(lines) - 1
	if last == 0 || strings.TrimSpace(lines[last]) != "```" {
		return "", unsupported("unterminated code fence", lines[0])
	}
	body := strings.ReplaceAll(strings.Join(lines[1:last], "\n"), "\r\n", "\n")
	body = strings.TrimSuffix(body, "\r")
	// {code} is terminated by the literal marker — a body containing it cannot
	// be expressed inside the macro.
	if strings.Contains(body, "{code}") {
		return "", unsupported("code block containing {code}", clip(body))
	}
	tag := "{code}"
	if lang != "" {
		tag = "{code:" + lang + "}"
	}
	return tag + "\n" + body + "\n{code}", nil
}

func convertBlockquote(lines []string, opts Options) (string, error) {
	inner := make([]string, len(lines))
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, ">") {
			return "", unsupported("blockquote with unprefixed line", l)
		}
		inner[i] = strings.TrimPrefix(strings.TrimPrefix(t, ">"), " ")
	}
	var parts []string
	for _, blk := range mdcsf.SplitBlocks(strings.Join(inner, "\n")) {
		if strings.HasPrefix(strings.TrimSpace(blk), ">") {
			return "", unsupported("nested blockquote", blk)
		}
		out, err := convertBlock(blk, opts)
		if err != nil {
			return "", err
		}
		if strings.Contains(out, "{quote}") {
			return "", unsupported("quote content containing {quote}", clip(out))
		}
		parts = append(parts, out)
	}
	if len(parts) == 0 {
		return "", unsupported("empty blockquote", "")
	}
	return "{quote}\n" + strings.Join(parts, "\n\n") + "\n{quote}", nil
}

func convertTable(lines []string) (string, error) {
	if len(lines) < 2 || !mdcsf.IsTableSeparator(lines[1]) {
		return "", unsupported("table without separator row", clip(lines[0]))
	}
	header := mdcsf.SplitTableRow(lines[0])
	width := len(header)
	if len(mdcsf.SplitTableRow(lines[1])) != width {
		return "", unsupported("table separator width differs from header", clip(lines[1]))
	}
	var b strings.Builder
	writeRow := func(cells []string, sep string) error {
		b.WriteString(sep)
		for _, c := range cells {
			out, err := inline(c)
			if err != nil {
				return err
			}
			if strings.Contains(out, "|") {
				return unsupported("table cell containing a pipe", clip(c))
			}
			if out == "" {
				out = " " // an empty wiki cell collapses the row structure
			}
			b.WriteString(out + sep)
		}
		return nil
	}
	if err := writeRow(header, "||"); err != nil {
		return "", err
	}
	for _, line := range lines[2:] {
		cells := mdcsf.SplitTableRow(line)
		if len(cells) > width {
			return "", unsupported("table row wider than header", clip(line))
		}
		for len(cells) < width {
			cells = append(cells, "")
		}
		b.WriteString("\n")
		if err := writeRow(cells, "|"); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

type listItem struct {
	indent  int
	ordered bool
	text    string
}

var taskRe = regexp.MustCompile(`^\[( |x|X)\]\s+`)

func convertList(lines []string) (string, error) {
	items := make([]listItem, 0, len(lines))
	for _, l := range lines {
		m := listItemRe.FindStringSubmatch(l)
		if m == nil {
			return "", unsupported("list item continuation line", l)
		}
		if taskRe.MatchString(m[4]) {
			return "", unsupported("task list (no Jira wiki equivalent)", m[4])
		}
		items = append(items, listItem{indent: len(m[1]), ordered: m[3] != "", text: m[4]})
	}
	var b strings.Builder
	n, err := buildList(&b, items, 0, items[0].indent, "")
	if err != nil {
		return "", err
	}
	if n != len(items) {
		return "", unsupported("list dedents below its first item", items[n].text)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// buildList emits wiki list lines for the run of items at `indent`, recursing
// for deeper indents with the parent's marker prefix ("*"/"#" per level, the
// wiki nesting syntax). Returns the index of the first item not consumed.
func buildList(b *strings.Builder, items []listItem, i, indent int, prefix string) (int, error) {
	marker := prefix + "*"
	if items[i].ordered {
		marker = prefix + "#"
	}
	ordered := items[i].ordered
	for i < len(items) {
		it := items[i]
		if it.indent < indent {
			break
		}
		if it.indent > indent {
			return 0, unsupported("list indentation jump", it.text)
		}
		if it.ordered != ordered {
			return 0, unsupported("mixed ordered and unordered siblings", it.text)
		}
		text, err := inline(it.text)
		if err != nil {
			return 0, err
		}
		b.WriteString(marker + " " + text + "\n")
		i++
		if i < len(items) && items[i].indent > indent {
			if i, err = buildList(b, items, i, items[i].indent, marker); err != nil {
				return 0, err
			}
		}
	}
	return i, nil
}

func trimAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimSpace(l)
	}
	return out
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}
