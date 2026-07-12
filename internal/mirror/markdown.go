package mirror

import (
	"fmt"
	"html"
	neturl "net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// RenderMarkdown produces the read-only markdown view of a CSF body: legible
// prose for grep/Read, with opaque fragments shown as resolved placeholders
// (⟦…⟧) and images/diagrams as ![](assets/…) links. It is intentionally lossy —
// the .csf file remains the editable source of truth.
func RenderMarkdown(root *csf.Node, refs []domain.Ref) []byte {
	return []byte(renderMarkdownHeadingOffset(root, refs, 0))
}

func renderMarkdownHeadingOffset(root *csf.Node, refs []domain.Ref, headingOffset int) string {
	r := newMDRendererOffset(refs, headingOffset)
	var b strings.Builder
	forEachBlockNode(root, func(n *csf.Node) {
		r.block(&b, n)
	})
	return normalizeBlankLines(b.String())
}

func renderCommentMarkdown(root *csf.Node) string {
	r := newMDRendererOffset(nil, 2)
	var b strings.Builder
	forEachBlockNode(root, func(n *csf.Node) {
		if code, ok := commentCodeTable(n); ok {
			fence := markdownFence(code)
			fmt.Fprintf(&b, "%s\n%s\n%s\n\n", fence, code, fence)
			return
		}
		r.block(&b, n)
	})
	return normalizeBlankLines(b.String())
}

// commentCodeTable recognizes the one-cell table Confluence commonly creates
// when a pasted multiline code snippet is placed in a comment. Rendering it as
// a GFM table collapses every <br>; a fenced block preserves the readable shape.
func commentCodeTable(n *csf.Node) (string, bool) {
	if n.Type != csf.Element || n.Name.Space != "" || n.Name.Local != "table" {
		return "", false
	}
	rows := tableRows(n)
	if len(rows) != 1 {
		return "", false
	}
	cells := rowCells(rows[0])
	if len(cells) != 1 {
		return "", false
	}
	var code *csf.Node
	csf.Walk(cells[0], func(x *csf.Node) bool {
		if code == nil && x.Type == csf.Element && x.Name.Space == "" && x.Name.Local == "code" {
			code = x
			return false
		}
		return true
	})
	if code == nil {
		return "", false
	}
	if !exclusiveCommentCodeWrapper(cells[0], code) || !commentCodeIsMultiline(code) {
		return "", false
	}
	var b strings.Builder
	var write func(*csf.Node)
	write = func(x *csf.Node) {
		switch x.Type {
		case csf.Text, csf.CData:
			b.WriteString(x.Data)
		case csf.Element:
			if x.Name.Space == "" && x.Name.Local == "br" {
				b.WriteByte('\n')
				return
			}
			for _, child := range x.Children {
				write(child)
			}
		}
	}
	write(code)
	value := strings.TrimSpace(b.String())
	return value, value != ""
}

func exclusiveCommentCodeWrapper(n, code *csf.Node) bool {
	if n == code {
		return true
	}
	var meaningful []*csf.Node
	for _, child := range n.Children {
		if (child.Type == csf.Text || child.Type == csf.CData) && strings.TrimSpace(child.Data) == "" {
			continue
		}
		meaningful = append(meaningful, child)
	}
	if len(meaningful) != 1 || meaningful[0].Type != csf.Element {
		return false
	}
	child := meaningful[0]
	if child != code && (child.Name.Space != "" || child.Name.Local != "p") {
		return false
	}
	return exclusiveCommentCodeWrapper(child, code)
}

func commentCodeIsMultiline(code *csf.Node) bool {
	multiline := false
	csf.Walk(code, func(n *csf.Node) bool {
		if n.Type == csf.Element && n.Name.Space == "" && n.Name.Local == "br" ||
			(n.Type == csf.Text || n.Type == csf.CData) && strings.Contains(n.Data, "\n") {
			multiline = true
			return false
		}
		return !multiline
	})
	return multiline
}

func markdownFence(content string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r == rune(0x60) {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat(string(rune(0x60)), max(3, longest+1))
}

// MDViewOpts carries the profile-driven additions to a Confluence markdown view.
// Metadata/comments are optional; ReadOnly switches the body boundary for a
// transient document that has no writeback baseline. A zero value renders the
// standard editable mirror envelope around RenderMarkdown's output. The app
// layer assembles these from the page metadata and, for Comments, the
// `<slug>.comments.json` sidecar (absent → nil → the section is skipped).
type MDViewOpts struct {
	PageFields []PageField
	Comments   []domain.Comment
	JiraMacros []JiraMacroView
	ReadOnly   bool
}

// PageField is one already-resolved, read-only Confluence metadata value. The
// renderer owns structural and Markdown escaping; callers supply plain text.
type PageField struct {
	ID        string
	Label     string
	Placement string
	Values    []string
	ShowEmpty bool
}

// RenderMarkdownOpts renders a versioned derived view with stable generated
// boundaries, optional YAML metadata, and a trailing Comments section.
func RenderMarkdownOpts(root *csf.Node, refs []domain.Ref, opts MDViewOpts) []byte {
	prefix, body, suffix := RenderMarkdownViewParts(root, refs, opts)
	return []byte(prefix + body + suffix)
}

// RenderMarkdownViewParts renders the view as three concatenable parts —
// prefix (generated metadata), body, suffix (the "# Comments" section) — such that
// prefix+body+suffix is byte-identical to RenderMarkdownOpts(root, refs, opts).
// The split exists for `conf apply`: the editable body must be located by these
// structural anchors (the metadata above and the Comments section below are
// read-only in the view), NOT by re-parsing headings — a body heading renders
// as a top-level `## ` line and would be misread as a generated section.
func RenderMarkdownViewParts(root *csf.Node, refs []domain.Ref, opts MDViewOpts) (prefix, body, suffix string) {
	body = string(RenderMarkdown(root, refs))
	prefix = ConfluenceDocumentMarker + "\n"
	if fields := renderPageFields(opts.PageFields); fields != "" {
		prefix += fields + "\n\n"
	}
	bodyMarker := ConfluenceBodyMarker
	if opts.ReadOnly {
		bodyMarker = ConfluenceBodyReadOnlyMarker
	}
	prefix += bodyMarker + "\n# Content\n\n"
	if len(opts.JiraMacros) > 0 {
		var generated strings.Builder
		generated.WriteString("\n" + ConfluenceJiraMacrosMarker + "\n# Jira Queries\n\n")
		for _, macro := range opts.JiraMacros {
			fmt.Fprintf(&generated, "## Jira Query %d\n\n%s", macro.Index+1, strings.TrimSpace(macro.Markdown))
			if macro.Truncated || !macro.Complete {
				generated.WriteString("\n\n> **Partial:** this macro result is truncated; refresh the page view to retrieve current rows.")
			}
			generated.WriteString("\n\n")
		}
		suffix = generated.String()
	}
	if len(opts.Comments) > 0 {
		suffix += "\n" + ConfluenceCommentsMarker + "\n" + string(RenderCommentsMarkdown(opts.Comments))
	}
	// RenderMarkdownOpts applies TrimRight(whole, "\n")+"\n" to the concatenation.
	// Reproduce it by trimming the assembled whole, then re-slicing at the raw
	// part boundaries (clamped): slicing one string at increasing offsets keeps
	// prefix+body+suffix == whole byte-for-byte in every case, including a body
	// that is entirely trailing newlines.
	full := prefix + body + suffix
	full = strings.TrimRight(full, "\n") + "\n"
	pEnd := len(prefix)
	if pEnd > len(full) {
		pEnd = len(full)
	}
	bEnd := len(prefix) + len(body)
	if bEnd > len(full) {
		bEnd = len(full)
	}
	return full[:pEnd], full[pEnd:bEnd], full[bEnd:]
}

func renderPageFields(fields []PageField) string {
	var metadata []PageField
	var sections []PageField
	for _, field := range fields {
		if len(field.Values) == 0 && !field.ShowEmpty {
			continue
		}
		if field.Placement == "section" {
			sections = append(sections, field)
		} else {
			metadata = append(metadata, field)
		}
	}
	if len(metadata) == 0 && len(sections) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(ConfluencePageFieldsMarker + "\n")
	if len(metadata) > 0 {
		b.WriteString("# Metadata\n\n| Field | Value |\n| --- | --- |\n")
		for _, field := range metadata {
			fmt.Fprintf(&b, "| %s | %s |\n", pageTableValue(field.Label), pageTableValue(strings.Join(field.Values, ", ")))
		}
		b.WriteByte('\n')
	}
	for _, field := range sections {
		fmt.Fprintf(&b, "<!-- atl:section page-field.%s readonly -->\n# %s\n\n", safeMarkerID(field.ID), pageTableValue(field.Label))
		if len(field.Values) == 0 {
			b.WriteString("_Empty_\n\n")
		} else if len(field.Values) == 1 {
			b.WriteString(pageSectionValue(field.Values[0]) + "\n\n")
		} else {
			for _, value := range field.Values {
				b.WriteString("- " + pageSectionValue(value) + "\n")
			}
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func pageSectionValue(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	escaped := pageTableValue(s)
	if s == "" {
		return escaped
	}
	switch s[0] {
	case '-':
		return "&#45;" + escaped[1:]
	case '+':
		return "&#43;" + escaped[1:]
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i < len(s) && (s[i] == '.' || s[i] == ')') {
		entity := "&#46;"
		if s[i] == ')' {
			entity = "&#41;"
		}
		return escaped[:i] + entity + escaped[i+1:]
	}
	return escaped
}

func pageTableValue(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.NewReplacer(
		"&", "&amp;", "\\", "&#92;", "|", "&#124;", "<", "&lt;", ">", "&gt;",
		"`", "&#96;", "*", "&#42;", "_", "&#95;", "~", "&#126;",
		"[", "&#91;", "]", "&#93;", "!", "&#33;", "#", "&#35;",
	).Replace(s)
}

func safeMarkerID(s string) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('_')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

type mdRenderer struct {
	refs           map[string]domain.Ref
	headingOffset  int
	escapeHTMLText bool
}

func newMDRenderer(refs []domain.Ref) *mdRenderer {
	return newMDRendererOffset(refs, 0)
}

func newMDRendererOffset(refs []domain.Ref, headingOffset int) *mdRenderer {
	byKey := map[string]domain.Ref{}
	for _, r := range refs {
		byKey[string(r.Kind)+"\x00"+r.Key] = r
	}
	return &mdRenderer{refs: byKey, headingOffset: headingOffset}
}

func (r *mdRenderer) ref(kind domain.RefKind, key string) (domain.Ref, bool) {
	v, ok := r.refs[string(kind)+"\x00"+key]
	return v, ok
}

// block renders a block-level node, emitting trailing blank lines as needed.
func (r *mdRenderer) block(b *strings.Builder, n *csf.Node) {
	if n.Type == csf.Text {
		if s := strings.TrimSpace(n.Data); s != "" {
			b.WriteString(s)
			b.WriteString("\n\n")
		}
		return
	}
	if n.Type != csf.Element {
		return
	}
	switch {
	case isHeading(n.Name):
		level := min(6, int(n.Name.Local[1]-'0')+r.headingOffset)
		fmt.Fprintf(b, "%s %s\n\n", strings.Repeat("#", level), r.inline(n))
	case n.Name.Local == "p" && n.Name.Space == "":
		// Confluence routinely wraps a single block macro in <p>; route it to
		// the block handler so it keeps its body instead of degrading to ⟦name⟧.
		if m := soleBlockMacro(n); m != nil {
			r.macro(b, m)
			return
		}
		if s := strings.TrimSpace(r.inline(n)); s != "" {
			b.WriteString(s)
			b.WriteString("\n\n")
		}
	case n.Name.Local == "blockquote" && n.Name.Space == "":
		var inner strings.Builder
		for _, c := range n.Children {
			r.block(&inner, c)
		}
		if s := strings.TrimSpace(inner.String()); s != "" {
			b.WriteString(blockquote(s))
			b.WriteString("\n\n")
		}
	case n.Name.Local == "pre" && n.Name.Space == "":
		fmt.Fprintf(b, "```\n%s\n```\n\n", csf.TextContent(n))
	case n.Name.Space == "ac" && n.Name.Local == "task-list":
		r.taskList(b, n, 0)
		b.WriteString("\n")
	case n.Name.Local == "table":
		r.table(b, n)
	case n.Name.Local == "ul" || n.Name.Local == "ol":
		r.list(b, n, n.Name.Local == "ol", 0)
		b.WriteString("\n")
	case n.Name.Local == "hr":
		b.WriteString("---\n\n")
	case n.Name.Space == "ac" && (n.Name.Local == "structured-macro" || n.Name.Local == "macro"):
		r.macro(b, n)
	case n.Name.Space == "ac" && n.Name.Local == "layout":
		for _, c := range n.Children {
			r.block(b, c)
		}
	case n.Name.Space == "ac" && (n.Name.Local == "layout-section" || n.Name.Local == "layout-cell"):
		for _, c := range n.Children {
			r.block(b, c)
		}
	default:
		// Unknown block: descend so we don't drop its content.
		for _, c := range n.Children {
			r.block(b, c)
		}
	}
}

func (r *mdRenderer) macro(b *strings.Builder, n *csf.Node) {
	name := n.Attrv("ac", "name")
	switch name {
	case "code":
		lang := macroParam(n, "language")
		body := plainBody(n)
		fmt.Fprintf(b, "```%s\n%s\n```\n\n", lang, body)
	case "noformat":
		fmt.Fprintf(b, "```\n%s\n```\n\n", plainBody(n))
	case "expand":
		title := macroParam(n, "title")
		if title == "" {
			title = "Details"
		}
		var inner strings.Builder
		for _, c := range richBody(n) {
			r.block(&inner, c)
		}
		fmt.Fprintf(b, "**%s**\n\n%s\n\n", title, strings.TrimSpace(inner.String()))
	case "jira":
		if key := macroParam(n, "key"); key != "" {
			fmt.Fprintf(b, "[%s](jira:%s)\n\n", key, key)
		} else if jql := macroParam(n, "jqlQuery"); jql != "" {
			fmt.Fprintf(b, "⟦jira query: %s⟧\n\n", jql)
		} else {
			b.WriteString("⟦jira⟧\n\n")
		}
	case "view-file":
		if fn := attachmentNameUnder(n); fn != "" {
			fmt.Fprintf(b, "📎 [%s](attachment:%s)\n\n", fn, fn)
		} else {
			b.WriteString("⟦macro view-file⟧\n\n")
		}
	case "include", "excerpt-include":
		if title := includedPageTitle(n); title != "" {
			fmt.Fprintf(b, "⟦include: %s⟧\n\n", title)
		} else {
			b.WriteString("⟦macro include⟧\n\n")
		}
	case "children":
		b.WriteString("⟦child pages (listed in Confluence)⟧\n\n")
	case "drawio":
		dn := macroParam(n, "diagramName")
		if ref, ok := r.ref(domain.RefDrawio, dn); ok && ref.Asset != "" {
			fmt.Fprintf(b, "![diagram: %s](%s)\n\n", dn, ref.Asset)
		} else {
			fmt.Fprintf(b, "⟦drawio diagram: %s (open in Confluence)⟧\n\n", dn)
		}
	case "info", "note", "warning", "tip", "panel":
		title := macroParam(n, "title")
		var inner strings.Builder
		for _, c := range richBody(n) {
			r.block(&inner, c)
		}
		label := strings.ToUpper(name)
		if title != "" {
			label += ": " + title
		}
		b.WriteString(blockquote(label+"\n\n"+strings.TrimSpace(inner.String())) + "\n\n")
	case "toc":
		b.WriteString("⟦table of contents⟧\n\n")
	case "status":
		// inline-ish; render on its own line
		fmt.Fprintf(b, "`[%s]`\n\n", macroParam(n, "title"))
	default:
		// Generic macro: show name + any rich body so content isn't lost.
		var inner strings.Builder
		for _, c := range richBody(n) {
			r.block(&inner, c)
		}
		if s := strings.TrimSpace(inner.String()); s != "" {
			fmt.Fprintf(b, "⟦macro %s⟧\n\n%s\n\n", name, s)
		} else {
			fmt.Fprintf(b, "⟦macro %s⟧\n\n", name)
		}
	}
}

func (r *mdRenderer) list(b *strings.Builder, n *csf.Node, ordered bool, depth int) {
	i := 1
	for _, c := range n.Children {
		if c.Type != csf.Element || c.Name.Local != "li" {
			continue
		}
		marker := "- "
		if ordered {
			marker = fmt.Sprintf("%d. ", i)
		}
		fmt.Fprintf(b, "%s%s%s\n", strings.Repeat("  ", depth), marker, strings.TrimSpace(r.inlineNoBlock(c)))
		for _, gc := range c.Children {
			if gc.Type == csf.Element && (gc.Name.Local == "ul" || gc.Name.Local == "ol") {
				r.list(b, gc, gc.Name.Local == "ol", depth+1)
			}
		}
		i++
	}
}

func (r *mdRenderer) table(b *strings.Builder, n *csf.Node) {
	grid, _, header := r.tableGrid(n)
	if len(grid) == 0 {
		return
	}
	width := len(grid[0])
	for ri, row := range grid {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = strings.ReplaceAll(c.Text, "|", "\\|")
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		if ri == header || (header < 0 && ri == 0) {
			seps := make([]string, width)
			for i := range seps {
				seps[i] = "---"
			}
			b.WriteString("| " + strings.Join(seps, " | ") + " |\n")
		}
	}
	b.WriteString("\n")
}

// TableCell is one slot of a table's md view: the owning td/th node (nil for
// width padding), whether the slot is the cell's top-left origin (span
// continuations and padding are not), and the exact text the .md table view
// renders there (before pipe escaping).
type TableCell struct {
	Node   *csf.Node
	Origin bool
	Text   string
}

// TableGrid exposes the md-view grid of a <table> node — one slice per md
// table row (all padded to uniform width), the parallel <tr> source nodes,
// and the header row index (-1 when no row holds a <th>). The md→CSF table
// merge aligns against this grid, so it must stay the single source of what
// the .md view shows (the renderer itself draws from it).
func TableGrid(table *csf.Node, refs []domain.Ref) ([][]TableCell, []*csf.Node, int) {
	r := newMDRenderer(refs)
	return r.tableGrid(table)
}

func (r *mdRenderer) tableGrid(n *csf.Node) ([][]TableCell, []*csf.Node, int) {
	var grid [][]TableCell
	var trs []*csf.Node
	header := -1

	pending := map[int]pendingCell{}
	for _, tr := range tableRows(n) {
		var cells []TableCell
		isHeader := false
		col := 0
		for _, c := range rowCells(tr) {
			for {
				if p, ok := pending[col]; ok {
					cells = append(cells, TableCell{Node: p.node, Text: p.text})
					p.rows--
					if p.rows <= 0 {
						delete(pending, col)
					} else {
						pending[col] = p
					}
					col++
					continue
				}
				break
			}
			if c.Name.Local == "th" {
				isHeader = true
			}
			text := strings.TrimSpace(r.inline(c))
			colspan := colspanOf(c)
			rowspan := rowspanOf(c)
			for spanCol := 0; spanCol < colspan; spanCol++ {
				cellText := text
				if spanCol > 0 {
					cellText = ""
				}
				cells = append(cells, TableCell{Node: c, Origin: spanCol == 0, Text: cellText})
				if rowspan > 1 {
					pending[col] = pendingCell{node: c, text: cellText, rows: rowspan - 1}
				}
				col++
			}
		}
		for col <= maxPendingCol(pending) {
			if p, ok := pending[col]; ok {
				cells = append(cells, TableCell{Node: p.node, Text: p.text})
				p.rows--
				if p.rows <= 0 {
					delete(pending, col)
				} else {
					pending[col] = p
				}
			} else {
				cells = append(cells, TableCell{})
			}
			col++
		}
		if isHeader && header < 0 {
			header = len(grid)
		}
		grid = append(grid, cells)
		trs = append(trs, tr)
	}
	width := 0
	for _, row := range grid {
		if len(row) > width {
			width = len(row)
		}
	}
	for i, row := range grid {
		for len(row) < width {
			row = append(row, TableCell{})
		}
		grid[i] = row
	}
	return grid, trs, header
}

type pendingCell struct {
	node *csf.Node
	text string
	rows int
}

func tableRows(table *csf.Node) []*csf.Node {
	var out []*csf.Node
	csf.Walk(table, func(x *csf.Node) bool {
		if x != table && x.Name.Local == "table" && x.Name.Space == "" {
			return false
		}
		if x.Name.Local == "tr" && x.Name.Space == "" {
			out = append(out, x)
			return false
		}
		return true
	})
	return out
}

func rowCells(row *csf.Node) []*csf.Node {
	var out []*csf.Node
	for _, c := range row.Children {
		if c.Type == csf.Element && c.Name.Space == "" && (c.Name.Local == "td" || c.Name.Local == "th") {
			out = append(out, c)
		}
	}
	return out
}

func maxPendingCol(pending map[int]pendingCell) int {
	max := -1
	for col := range pending {
		if col > max {
			max = col
		}
	}
	return max
}

// inline renders inline content of a node to a single line.
func (r *mdRenderer) inline(n *csf.Node) string {
	var b strings.Builder
	for _, c := range n.Children {
		r.inlineNode(&b, c)
	}
	return strings.TrimSpace(squeezeSpaces(b.String()))
}

// inlineNoBlock is like inline but used inside list items.
func (r *mdRenderer) inlineNoBlock(n *csf.Node) string {
	var b strings.Builder
	for _, c := range n.Children {
		if c.Type == csf.Element && (c.Name.Local == "ul" || c.Name.Local == "ol") {
			continue
		}
		r.inlineNode(&b, c)
	}
	return strings.TrimSpace(squeezeSpaces(b.String()))
}

func (r *mdRenderer) inlineNode(b *strings.Builder, n *csf.Node) {
	if n.Type == csf.Text || n.Type == csf.CData {
		text := collapseWS(n.Data)
		if r.escapeHTMLText {
			text = html.EscapeString(text)
		}
		b.WriteString(text)
		return
	}
	if n.Type != csf.Element {
		return
	}
	switch {
	case n.Name.Local == "strong" || n.Name.Local == "b":
		b.WriteString("**" + r.inline(n) + "**")
	case n.Name.Local == "em" || n.Name.Local == "i":
		b.WriteString("_" + r.inline(n) + "_")
	case n.Name.Local == "s" || n.Name.Local == "del" || n.Name.Local == "strike":
		b.WriteString("~~" + r.inline(n) + "~~")
	case n.Name.Local == "code":
		b.WriteString("`" + r.inline(n) + "`")
	case n.Name.Local == "pre":
		b.WriteString("`" + r.inline(n) + "`")
	case n.Name.Local == "time" && n.Name.Space == "":
		b.WriteString(n.Attrv("", "datetime"))
	case n.Name.Space == "ac" && n.Name.Local == "emoticon":
		if fb := n.Attrv("ac", "emoji-fallback"); fb != "" {
			b.WriteString(fb)
		} else {
			b.WriteString(":" + n.Attrv("ac", "name") + ":")
		}
	case n.Name.Local == "br":
		b.WriteString(" ")
	case n.Name.Local == "a":
		href := n.Attrv("", "href")
		b.WriteString("[" + r.inline(n) + "](" + href + ")")
	case n.Name.Local == "span" && n.Name.Space == "":
		if color := styleColor(n); color != "" {
			wasEscaping := r.escapeHTMLText
			r.escapeHTMLText = true
			inner := r.inline(n)
			r.escapeHTMLText = wasEscaping
			if inner != "" {
				if safe, ok := SafeCSSColor(color); ok {
					b.WriteString("<span style=\"color: " + html.EscapeString(safe) + "\">" + inner + "</span>")
				} else {
					b.WriteString("<span data-atl-color=\"" + html.EscapeString(color) + "\">" + inner + "</span>")
				}
			}
			return
		}
		for _, c := range n.Children {
			r.inlineNode(b, c)
		}
	case n.Name.Space == "ac" && n.Name.Local == "link":
		r.acLink(b, n)
	case n.Name.Space == "ac" && n.Name.Local == "image":
		r.acImage(b, n)
	case n.Name.Space == "ri" && n.Name.Local == "user":
		key := n.Attrv("ri", "userkey")
		if key == "" {
			key = n.Attrv("ri", "account-id")
		}
		if ref, ok := r.ref(domain.RefUser, key); ok {
			b.WriteString(ref.Display)
		} else {
			b.WriteString("@" + key)
		}
	case n.Name.Space == "ac" && (n.Name.Local == "structured-macro" || n.Name.Local == "macro"):
		switch name := n.Attrv("ac", "name"); name {
		case "status":
			b.WriteString("`[" + macroParam(n, "title") + "]`")
		case "drawio":
			dn := macroParam(n, "diagramName")
			if ref, ok := r.ref(domain.RefDrawio, dn); ok && ref.Asset != "" {
				b.WriteString("![diagram: " + dn + "](" + ref.Asset + ")")
			} else {
				b.WriteString("⟦drawio diagram: " + dn + "⟧")
			}
		case "jira":
			if key := macroParam(n, "key"); key != "" {
				b.WriteString("[" + key + "](jira:" + key + ")")
			} else if jql := macroParam(n, "jqlQuery"); jql != "" {
				b.WriteString("⟦jira query: " + jql + "⟧")
			} else {
				b.WriteString("⟦jira⟧")
			}
		case "toc":
			b.WriteString("⟦table of contents⟧")
		case "code":
			// Inline (mixed-with-text) code: collapse a multi-line body so the
			// span stays on one line — a literal newline in backticks is broken.
			b.WriteString("`" + collapseWS(plainBody(n)) + "`")
		default:
			b.WriteString("⟦" + name + "⟧")
		}
	default:
		// A block-level element flattened into an inline context (e.g. several
		// <p>/<div> inside a table cell or <li>) must not glue to its siblings.
		// Inline elements like <span> fall through without a separator.
		if isFlowBreak(n.Name) {
			b.WriteString(" ")
		}
		for _, c := range n.Children {
			r.inlineNode(b, c)
		}
	}
}

// isFlowBreak reports whether an element is block-level, so that when it is
// flattened into an inline string a separating space is inserted around it.
func isFlowBreak(n csf.Name) bool {
	if n.Space != "" {
		return false
	}
	switch n.Local {
	case "p", "div", "li", "tr", "blockquote", "dt", "dd", "pre",
		"h1", "h2", "h3", "h4", "h5", "h6":
		return true
	}
	return false
}

func (r *mdRenderer) acLink(b *strings.Builder, n *csf.Node) {
	// Resolve the link target from its ri:* child.
	var label, target, pageTitle, pageSpace string
	csf.Walk(n, func(x *csf.Node) bool {
		switch {
		case x.Name.Space == "ri" && x.Name.Local == "page":
			pageTitle = x.Attrv("ri", "content-title")
			pageSpace = x.Attrv("ri", "space-key")
			target = "page:" + pageTitle
		case x.Name.Space == "ri" && x.Name.Local == "attachment":
			target = "attachment:" + x.Attrv("ri", "filename")
		case x.Name.Space == "ri" && x.Name.Local == "user":
			key := x.Attrv("ri", "userkey")
			if ref, ok := r.ref(domain.RefUser, key); ok {
				label = ref.Display
			} else {
				label = "@" + key
			}
			target = "user"
		case x.Name.Space == "ac" && x.Name.Local == "link-body":
			label = r.inline(x)
		case x.Name.Space == "ac" && x.Name.Local == "plain-text-link-body":
			label = collapseWS(csf.TextContent(x))
		}
		return true
	})
	if label == "" {
		label = strings.TrimPrefix(strings.TrimPrefix(target, "page:"), "attachment:")
	}
	if target == "user" {
		b.WriteString(label)
		return
	}
	if strings.HasPrefix(target, "page:") {
		b.WriteString("[" + markdownLinkLabel(label) + "](confluence-page:" + pageLinkIdentity(pageSpace, pageTitle) + ")")
		return
	}
	b.WriteString("[" + label + "](" + target + ")")
}

func pageLinkIdentity(space, title string) string {
	identity := neturl.PathEscape(title)
	if space != "" {
		identity = neturl.PathEscape(space) + "/" + identity
	}
	return identity
}

func markdownLinkLabel(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "[", "\\[")
	return strings.ReplaceAll(s, "]", "\\]")
}

func (r *mdRenderer) acImage(b *strings.Builder, n *csf.Node) {
	// Decorative icons (e.g. Jira issue-type avatars next to issue links) only
	// add noise to a prose view; skip them.
	if strings.Contains(n.Attrv("ac", "class"), "icon") {
		return
	}
	var fn, url string
	csf.Walk(n, func(x *csf.Node) bool {
		switch {
		case x.Name.Space == "ri" && x.Name.Local == "attachment" && fn == "":
			fn = x.Attrv("ri", "filename")
		case x.Name.Space == "ri" && x.Name.Local == "url" && url == "":
			url = x.Attrv("ri", "value")
		}
		return true
	})
	if fn != "" {
		if ref, ok := r.ref(domain.RefImage, fn); ok && ref.Asset != "" {
			b.WriteString("![" + fn + "](" + ref.Asset + ")")
		} else {
			b.WriteString("![" + fn + "](attachment:" + fn + ")")
		}
		return
	}
	if url != "" {
		b.WriteString("![](" + url + ")")
	}
}

// --- helpers ---

func isHeading(n csf.Name) bool {
	return n.Space == "" && len(n.Local) == 2 && n.Local[0] == 'h' && n.Local[1] >= '1' && n.Local[1] <= '6'
}

func macroParam(macro *csf.Node, name string) string {
	for _, c := range macro.Children {
		if c.Type == csf.Element && c.Name.Space == "ac" && c.Name.Local == "parameter" && c.Attrv("ac", "name") == name {
			return csf.TextContent(c)
		}
	}
	return ""
}

func plainBody(macro *csf.Node) string {
	for _, c := range macro.Children {
		if c.Type == csf.Element && c.Name.Space == "ac" && c.Name.Local == "plain-text-body" {
			return csf.TextContent(c)
		}
	}
	return ""
}

func richBody(macro *csf.Node) []*csf.Node {
	for _, c := range macro.Children {
		if c.Type == csf.Element && c.Name.Space == "ac" && c.Name.Local == "rich-text-body" {
			return c.Children
		}
	}
	return nil
}

// soleBlockMacro returns the structured-macro a paragraph wraps when that macro
// is the paragraph's only meaningful child and is a block-kind macro. Returns
// nil otherwise (mixed text, multiple children, or an inline-natural macro).
func soleBlockMacro(p *csf.Node) *csf.Node {
	var macro *csf.Node
	for _, c := range p.Children {
		switch c.Type {
		case csf.Text, csf.CData:
			if strings.TrimSpace(c.Data) != "" {
				return nil
			}
		case csf.Element:
			if macro != nil {
				return nil // more than one element child
			}
			if c.Name.Space == "ac" && (c.Name.Local == "structured-macro" || c.Name.Local == "macro") && isBlockMacroName(c.Attrv("ac", "name")) {
				macro = c
			} else {
				return nil
			}
		}
	}
	return macro
}

// IsBlockMacro reports whether a macro renders block content — used by the
// md→CSF merge to refuse splicing such a macro into an inline context.
func IsBlockMacro(name string) bool { return isBlockMacroName(name) }

// isBlockMacroName reports whether a macro carries block content (a body or a
// full-width rendering) and so should never be downgraded to an inline ⟦name⟧.
func isBlockMacroName(name string) bool {
	switch name {
	case "code", "noformat", "info", "note", "warning", "tip", "panel", "expand", "toc", "drawio":
		return true
	}
	return false
}

// taskList renders an ac:task-list as GFM task items. A nested task-list (which
// Confluence stores inside a task-body) is rendered as an indented sub-list, not
// flattened into the parent line.
func (r *mdRenderer) taskList(b *strings.Builder, n *csf.Node, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, c := range n.Children {
		if c.Type != csf.Element || c.Name.Space != "ac" || c.Name.Local != "task" {
			continue
		}
		mark := "[ ]"
		var body, nested *csf.Node
		for _, gc := range c.Children {
			if gc.Type != csf.Element {
				continue
			}
			switch gc.Name.Local {
			case "task-status":
				if csf.TextContent(gc) == "complete" {
					mark = "[x]"
				}
			case "task-body":
				body = gc
			}
		}
		text := ""
		if body != nil {
			text = r.inlineTaskBody(body)
			for _, gc := range body.Children {
				if gc.Type == csf.Element && gc.Name.Space == "ac" && gc.Name.Local == "task-list" {
					nested = gc
				}
			}
		}
		fmt.Fprintf(b, "%s- %s %s\n", indent, mark, text)
		if nested != nil {
			r.taskList(b, nested, depth+1)
		}
	}
}

// inlineTaskBody renders a task body inline, skipping a nested task-list (which
// taskList renders separately as an indented sub-list).
func (r *mdRenderer) inlineTaskBody(body *csf.Node) string {
	var b strings.Builder
	for _, c := range body.Children {
		if c.Type == csf.Element && c.Name.Space == "ac" && c.Name.Local == "task-list" {
			continue
		}
		r.inlineNode(&b, c)
	}
	return strings.TrimSpace(squeezeSpaces(b.String()))
}

// maxSpan caps col/rowspan expansion: server bytes are untrusted, and a
// hostile `colspan="24444444"` would otherwise balloon the md grid into
// millions of phantom cells.
const maxSpan = 100

func colspanOf(cell *csf.Node) int {
	if n, err := strconv.Atoi(cell.Attrv("", "colspan")); err == nil && n > 1 {
		return min(n, maxSpan)
	}
	return 1
}

func rowspanOf(cell *csf.Node) int {
	if n, err := strconv.Atoi(cell.Attrv("", "rowspan")); err == nil && n > 1 {
		return min(n, maxSpan)
	}
	return 1
}

func styleColor(n *csf.Node) string {
	if color := strings.TrimSpace(n.Attrv("", "data-color")); color != "" {
		return color
	}
	style := n.Attrv("", "style")
	for _, decl := range strings.Split(style, ";") {
		k, v, ok := strings.Cut(decl, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "color") {
			continue
		}
		if color := strings.TrimSpace(v); color != "" {
			return color
		}
	}
	return ""
}

// SafeCSSColor accepts only inert CSS color values. It deliberately excludes
// var(), url(), declarations and arbitrary functions so a server-controlled
// page cannot turn a derived Markdown preview into an active network/style
// injection surface.
func SafeCSSColor(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 96 {
		return "", false
	}
	if value[0] == '#' {
		n := len(value) - 1
		if n != 3 && n != 4 && n != 6 && n != 8 {
			return "", false
		}
		for _, r := range value[1:] {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return "", false
			}
		}
		return value, true
	}
	lettersOnly := true
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			lettersOnly = false
			break
		}
	}
	if lettersOnly {
		return value, true
	}
	lower := strings.ToLower(value)
	for _, fn := range []string{"rgb(", "rgba(", "hsl(", "hsla("} {
		if !strings.HasPrefix(lower, fn) || !strings.HasSuffix(lower, ")") {
			continue
		}
		inside := value[len(fn) : len(value)-1]
		if strings.TrimSpace(inside) == "" {
			return "", false
		}
		for _, r := range inside {
			if (r >= '0' && r <= '9') || strings.ContainsRune(" \t.,%/+-", r) {
				continue
			}
			return "", false
		}
		return value, true
	}
	return "", false
}

// attachmentNameUnder finds a ri:attachment filename nested anywhere in a macro
// (view-file stores its target in a parameter, not as text).
func attachmentNameUnder(macro *csf.Node) string {
	var fn string
	csf.Walk(macro, func(x *csf.Node) bool {
		if fn == "" && x.Name.Space == "ri" && x.Name.Local == "attachment" {
			fn = x.Attrv("ri", "filename")
		}
		return true
	})
	return fn
}

// includedPageTitle finds the ri:page title an include/excerpt-include targets.
func includedPageTitle(macro *csf.Node) string {
	var title string
	csf.Walk(macro, func(x *csf.Node) bool {
		if title == "" && x.Name.Space == "ri" && x.Name.Local == "page" {
			title = x.Attrv("ri", "content-title")
		}
		return true
	})
	return title
}

func blockquote(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// collapseWS squeezes internal whitespace runs to a single space but, unlike a
// bare strings.Fields/Join, preserves a single leading/trailing space when the
// node had one. That boundary space is what keeps words from gluing together
// across inline elements (e.g. "word <strong>bold</strong>" → "word **bold**").
func collapseWS(s string) string {
	out := strings.Join(strings.Fields(s), " ")
	if out == "" {
		// Whitespace-only node between inline elements: keep one space.
		if s != "" {
			return " "
		}
		return ""
	}
	if hasLeadingSpace(s) {
		out = " " + out
	}
	if hasTrailingSpace(s) {
		out += " "
	}
	return out
}

func hasLeadingSpace(s string) bool {
	for _, r := range s {
		return unicode.IsSpace(r)
	}
	return false
}

func hasTrailingSpace(s string) bool {
	r := []rune(s)
	return len(r) > 0 && unicode.IsSpace(r[len(r)-1])
}

// squeezeSpaces collapses runs of ASCII spaces to one. Adjacent text nodes can
// each contribute a boundary space; this neutralizes the resulting double space.
func squeezeSpaces(s string) string {
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

func normalizeBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s) + "\n"
}
