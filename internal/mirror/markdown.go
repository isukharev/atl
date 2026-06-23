package mirror

import (
	"fmt"
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
	byKey := map[string]domain.Ref{}
	for _, r := range refs {
		byKey[string(r.Kind)+"\x00"+r.Key] = r
	}
	r := &mdRenderer{refs: byKey}
	var b strings.Builder
	for _, c := range root.Children {
		r.block(&b, c)
	}
	out := normalizeBlankLines(b.String())
	return []byte(out)
}

type mdRenderer struct {
	refs map[string]domain.Ref
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
		level := int(n.Name.Local[1] - '0')
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
	var rows [][]string
	header := -1
	csf.Walk(n, func(x *csf.Node) bool {
		if x.Name.Local == "tr" {
			var cells []string
			isHeader := false
			for _, c := range x.Children {
				if c.Type == csf.Element && (c.Name.Local == "td" || c.Name.Local == "th") {
					if c.Name.Local == "th" {
						isHeader = true
					}
					cells = append(cells, strings.ReplaceAll(strings.TrimSpace(r.inline(c)), "|", "\\|"))
					// Expand a merged cell into blanks so columns stay aligned
					// with the body rows (markdown has no native colspan).
					for k := colspanOf(c); k > 1; k-- {
						cells = append(cells, "")
					}
				}
			}
			if isHeader && header < 0 {
				header = len(rows)
			}
			rows = append(rows, cells)
			return false
		}
		return true
	})
	if len(rows) == 0 {
		return
	}
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	for ri, row := range rows {
		for len(row) < width {
			row = append(row, "")
		}
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
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
		b.WriteString(collapseWS(n.Data))
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
	var label, target string
	csf.Walk(n, func(x *csf.Node) bool {
		switch {
		case x.Name.Space == "ri" && x.Name.Local == "page":
			target = "page:" + x.Attrv("ri", "content-title")
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
			label = csf.TextContent(x)
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
	b.WriteString("[" + label + "](" + target + ")")
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
		if c.Type != csf.Element || !(c.Name.Space == "ac" && c.Name.Local == "task") {
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

func colspanOf(cell *csf.Node) int {
	if n, err := strconv.Atoi(cell.Attrv("", "colspan")); err == nil && n > 1 {
		return n
	}
	return 1
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
