// Package mdcsf converts a strict markdown subset into Confluence Storage
// Format. It exists for the md→CSF block merge (`conf apply`): only blocks the
// agent changed are converted; everything else keeps its original bytes. The
// converter therefore fails closed — any construct outside the subset returns
// an *UnsupportedError naming it, never partial or guessed output. Opaque
// markers the renderer emits (⟦…⟧ placeholders, attachment links, protected
// color HTML, images) are rejected here by design: apply substitutes original
// CSF bytes first. Explicit jira: and confluence-page: links have strict safe
// conversions for newly authored targets.
package mdcsf

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/wikiscanner"
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

// SplitBlocks splits markdown into blocks the way the renderer emits them:
// blank-line separated, except inside fenced code (never split). A heading,
// fence, or thematic break also starts a new block even without a preceding
// blank line, matching how the constructs parse.
func SplitBlocks(md string) []string {
	lines := strings.Split(md, "\n")
	var blocks []string
	var cur []string
	inFence := false
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range lines {
		if inFence {
			cur = append(cur, line)
			if wikiscanner.MarkdownBlockType(line) == wikiscanner.MarkdownFence {
				inFence = false
				flush()
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if wikiscanner.MarkdownBlockType(line) == wikiscanner.MarkdownFence {
			flush()
			cur = append(cur, line)
			inFence = true
			continue
		}
		if headingRe.MatchString(line) || wikiscanner.MarkdownBlockType(line) == wikiscanner.MarkdownThematicBreak {
			// Single-line constructs: always their own block.
			flush()
			cur = append(cur, line)
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return blocks
}

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*$`)

// Convert turns one markdown block into CSF bytes. The result of a successful
// conversion is always well-formed CSF (asserted by tests and fuzzing).
func Convert(block string) ([]byte, error) {
	block = strings.TrimRight(block, "\n")
	if strings.TrimSpace(block) == "" {
		return nil, unsupported("empty block", "")
	}
	if !utf8.ValidString(block) {
		return nil, unsupported("invalid UTF-8", "")
	}
	for _, r := range block {
		// XML 1.0 forbids most control characters; they cannot appear in CSF.
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xFFFE || r == 0xFFFF {
			return nil, unsupported("control character", fmt.Sprintf("U+%04X", r))
		}
	}
	lines := strings.Split(block, "\n")
	first := strings.TrimSpace(lines[0])
	switch {
	case strings.HasPrefix(first, "```"):
		return convertFence(lines)
	case headingRe.MatchString(lines[0]):
		return convertHeading(lines)
	case wikiscanner.MarkdownBlockType(first) == wikiscanner.MarkdownThematicBreak && len(lines) == 1:
		return []byte("<hr/>"), nil
	case strings.HasPrefix(first, ">"):
		return convertBlockquote(lines)
	case strings.HasPrefix(first, "|"):
		return convertTable(lines)
	case listItemRe.MatchString(lines[0]):
		return convertList(lines)
	default:
		return convertParagraph(lines)
	}
}

func convertHeading(lines []string) ([]byte, error) {
	if len(lines) != 1 {
		return nil, unsupported("heading with continuation lines", lines[1])
	}
	m := headingRe.FindStringSubmatch(lines[0])
	level := len(m[1])
	body, err := inline(m[2])
	if err != nil {
		return nil, err
	}
	return fmt.Appendf(nil, "<h%d>%s</h%d>", level, body, level), nil
}

func convertParagraph(lines []string) ([]byte, error) {
	body, err := inline(strings.Join(trimAll(lines), " "))
	if err != nil {
		return nil, err
	}
	return fmt.Appendf(nil, "<p>%s</p>", body), nil
}

func convertFence(lines []string) ([]byte, error) {
	open := strings.TrimSpace(lines[0])
	lang := strings.TrimSpace(strings.TrimPrefix(open, "```"))
	if strings.ContainsAny(lang, "`") {
		return nil, unsupported("code fence info string", open)
	}
	last := len(lines) - 1
	if last == 0 || strings.TrimSpace(lines[last]) != "```" {
		return nil, unsupported("unterminated code fence", lines[0])
	}
	// CRLF input reaches here verbatim (every other block type sheds \r via
	// TrimSpace); normalize so CDATA never carries stray carriage returns.
	body := strings.ReplaceAll(strings.Join(lines[1:last], "\n"), "\r\n", "\n")
	body = strings.TrimSuffix(body, "\r")
	var b strings.Builder
	b.WriteString(`<ac:structured-macro ac:name="code">`)
	if lang != "" {
		b.WriteString(`<ac:parameter ac:name="language">` + escapeText(lang) + `</ac:parameter>`)
	}
	b.WriteString(`<ac:plain-text-body><![CDATA[` + cdataEscape(body) + `]]></ac:plain-text-body></ac:structured-macro>`)
	return []byte(b.String()), nil
}

// cdataEscape splits any "]]>" in the body across two CDATA sections.
func cdataEscape(s string) string {
	return strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>")
}

// admonitionRe matches the label line the renderer emits for info-family
// macros inside a blockquote: "INFO", "WARNING: title", …
var admonitionRe = regexp.MustCompile(`^(INFO|NOTE|WARNING|TIP|PANEL)(?::\s*(.*))?$`)

func convertBlockquote(lines []string) ([]byte, error) {
	inner := make([]string, len(lines))
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, ">") {
			return nil, unsupported("blockquote with unprefixed line", l)
		}
		inner[i] = strings.TrimPrefix(strings.TrimPrefix(t, ">"), " ")
	}
	if m := admonitionRe.FindStringSubmatch(strings.TrimSpace(inner[0])); m != nil {
		return convertAdmonition(strings.ToLower(m[1]), m[2], inner[1:])
	}
	body, err := convertInnerBlocks(inner)
	if err != nil {
		return nil, err
	}
	return fmt.Appendf(nil, "<blockquote>%s</blockquote>", body), nil
}

func convertAdmonition(name, title string, inner []string) ([]byte, error) {
	body, err := convertInnerBlocks(inner)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<ac:structured-macro ac:name=%q>`, name)
	if title != "" {
		b.WriteString(`<ac:parameter ac:name="title">` + escapeText(title) + `</ac:parameter>`)
	}
	b.WriteString(`<ac:rich-text-body>` + body + `</ac:rich-text-body></ac:structured-macro>`)
	return []byte(b.String()), nil
}

// convertInnerBlocks converts blank-line-separated inner content (of a
// blockquote or admonition) where each piece is itself a supported block.
// Nested blockquotes are out of the subset.
func convertInnerBlocks(lines []string) (string, error) {
	var parts []string
	for _, blk := range SplitBlocks(strings.Join(lines, "\n")) {
		if strings.HasPrefix(strings.TrimSpace(blk), ">") {
			return "", unsupported("nested blockquote", blk)
		}
		out, err := Convert(blk)
		if err != nil {
			return "", err
		}
		parts = append(parts, string(out))
	}
	if len(parts) == 0 {
		return "", unsupported("empty blockquote", "")
	}
	return strings.Join(parts, ""), nil
}

func convertTable(lines []string) ([]byte, error) {
	if len(lines) < 2 {
		return nil, unsupported("table without separator row", lines[0])
	}
	for _, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "|") {
			return nil, unsupported("table with non-row line", l)
		}
	}
	if !tableSepRe.MatchString(strings.TrimSpace(lines[1])) {
		return nil, unsupported("table separator row", lines[1])
	}
	header := splitRow(lines[0])
	width := len(header)
	if len(splitRow(lines[1])) != width {
		return nil, unsupported("table separator width differs from header", lines[1])
	}
	var b strings.Builder
	b.WriteString("<table><tbody><tr>")
	for _, c := range header {
		cell, err := inline(c)
		if err != nil {
			return nil, err
		}
		b.WriteString("<th>" + cell + "</th>")
	}
	b.WriteString("</tr>")
	for _, l := range lines[2:] {
		cells := splitRow(l)
		// GFM: the header defines the width; short rows pad, long rows are a
		// sign of a malformed edit — refuse rather than drop content.
		if len(cells) > width {
			return nil, unsupported("table row wider than header", l)
		}
		for len(cells) < width {
			cells = append(cells, "")
		}
		b.WriteString("<tr>")
		for _, c := range cells {
			cell, err := inline(c)
			if err != nil {
				return nil, err
			}
			b.WriteString("<td>" + cell + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</tbody></table>")
	return []byte(b.String()), nil
}

// ConvertDocument turns a whole markdown document into a CSF body: SplitBlocks
// plus Convert per block, joined with newlines. Fail-closed like Convert — the
// first unconvertible block aborts with an error naming it (1-based index and
// first line), never partial output. An empty document is an error: page and
// comment bodies must not be silently blank.
func ConvertDocument(md string) ([]byte, error) {
	// A file authored on Windows may open with a UTF-8 BOM; without this the
	// first block starts with U+FEFF, misses every construct regex, and lands
	// as a corrupted paragraph that still validates.
	md = strings.TrimPrefix(md, "\ufeff")
	blocks := SplitBlocks(md)
	if len(blocks) == 0 {
		return nil, unsupported("empty document", "")
	}
	parts := make([][]byte, 0, len(blocks))
	for i, block := range blocks {
		out, err := Convert(block)
		if err != nil {
			first, _, _ := strings.Cut(strings.TrimSpace(block), "\n")
			return nil, fmt.Errorf("block %d (%q): %w", i+1, clip(first), err)
		}
		parts = append(parts, out)
	}
	return bytes.Join(parts, []byte("\n")), nil
}

var tableSepRe = regexp.MustCompile(`^\|(\s*:?-+:?\s*\|)+$`)

// ConvertInline converts one line of inline markdown to CSF inline XHTML —
// the cell-content entry point for the table merge in internal/mdmerge. Same
// subset and fail-closed rules as cells in Convert'ed tables.
func ConvertInline(s string) (string, error) { return inline(s) }

// SplitTableRow splits a GFM table row into unescaped cell texts, honoring
// \| escapes — the row parser behind Convert, exported for the table merge.
func SplitTableRow(line string) []string { return splitRow(line) }

// IsTableSeparator reports a GFM table separator row (`| --- | :-: |`…).
func IsTableSeparator(line string) bool { return tableSepRe.MatchString(strings.TrimSpace(line)) }

// splitRow splits a GFM table row into cells, honoring \| escapes.
func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	var cur strings.Builder
	escaped := false
	for _, r := range line {
		switch {
		case escaped:
			if r != '|' {
				cur.WriteRune('\\')
			}
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if escaped {
		cur.WriteRune('\\')
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

var listItemRe = regexp.MustCompile(`^(\s*)(?:([-*+])|(\d+)[.)])\s+(.*)$`)
var taskRe = regexp.MustCompile(`^\[( |x|X)\]\s+(.*)$`)

type listItem struct {
	indent  int
	ordered bool
	task    bool
	done    bool
	text    string
}

func convertList(lines []string) ([]byte, error) {
	items := make([]listItem, 0, len(lines))
	for _, l := range lines {
		m := listItemRe.FindStringSubmatch(l)
		if m == nil {
			return nil, unsupported("list item continuation line", l)
		}
		it := listItem{indent: len(m[1]), ordered: m[3] != "", text: m[4]}
		if tm := taskRe.FindStringSubmatch(it.text); tm != nil {
			it.task = true
			it.done = tm[1] != " "
			it.text = tm[2]
		}
		items = append(items, it)
	}
	task := items[0].task
	for _, it := range items {
		if it.task != task {
			return nil, unsupported("mixed task and plain list items", it.text)
		}
	}
	var b strings.Builder
	var n int
	var err error
	if task {
		n, err = buildTaskList(&b, items, 0, items[0].indent)
	} else {
		n, err = buildList(&b, items, 0, items[0].indent)
	}
	if err != nil {
		return nil, err
	}
	if n != len(items) {
		return nil, unsupported("list dedents below its first item", items[n].text)
	}
	return []byte(b.String()), nil
}

// buildList emits a <ul>/<ol> for the run of items at `indent`, recursing for
// deeper indents. Returns the index of the first item it did not consume.
func buildList(b *strings.Builder, items []listItem, i, indent int) (int, error) {
	tag := "ul"
	if items[i].ordered {
		tag = "ol"
	}
	b.WriteString("<" + tag + ">")
	for i < len(items) {
		it := items[i]
		if it.indent < indent {
			break
		}
		if it.indent > indent {
			return 0, unsupported("list indentation jump", it.text)
		}
		if (it.ordered && tag == "ul") || (!it.ordered && tag == "ol") {
			return 0, unsupported("mixed ordered and unordered siblings", it.text)
		}
		text, err := inline(it.text)
		if err != nil {
			return 0, err
		}
		b.WriteString("<li>" + text)
		i++
		if i < len(items) && items[i].indent > indent {
			if i, err = buildList(b, items, i, items[i].indent); err != nil {
				return 0, err
			}
		}
		b.WriteString("</li>")
	}
	b.WriteString("</" + tag + ">")
	return i, nil
}

// buildTaskList emits an ac:task-list; nesting goes inside the task body, the
// shape the Confluence editor produces (task ids are assigned server-side).
func buildTaskList(b *strings.Builder, items []listItem, i, indent int) (int, error) {
	b.WriteString("<ac:task-list>")
	for i < len(items) {
		it := items[i]
		if it.indent < indent {
			break
		}
		if it.indent > indent {
			return 0, unsupported("list indentation jump", it.text)
		}
		status := "incomplete"
		if it.done {
			status = "complete"
		}
		text, err := inline(it.text)
		if err != nil {
			return 0, err
		}
		b.WriteString("<ac:task><ac:task-status>" + status + "</ac:task-status><ac:task-body>" + text)
		i++
		if i < len(items) && items[i].indent > indent {
			if i, err = buildTaskList(b, items, i, items[i].indent); err != nil {
				return 0, err
			}
		}
		b.WriteString("</ac:task-body></ac:task>")
	}
	b.WriteString("</ac:task-list>")
	return i, nil
}

func trimAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimSpace(l)
	}
	return out
}
