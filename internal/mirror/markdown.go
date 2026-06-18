package mirror

import (
	"fmt"
	"strings"

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
		if s := strings.TrimSpace(r.inline(n)); s != "" {
			b.WriteString(s)
			b.WriteString("\n\n")
		}
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
	return strings.TrimSpace(b.String())
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
	return strings.TrimSpace(b.String())
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
	case n.Name.Local == "code":
		b.WriteString("`" + r.inline(n) + "`")
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
		default:
			b.WriteString("⟦" + name + "⟧")
		}
	default:
		for _, c := range n.Children {
			r.inlineNode(b, c)
		}
	}
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
	fn := ""
	csf.Walk(n, func(x *csf.Node) bool {
		if x.Name.Space == "ri" && x.Name.Local == "attachment" {
			fn = x.Attrv("ri", "filename")
		}
		return true
	})
	if fn == "" {
		return
	}
	if ref, ok := r.ref(domain.RefImage, fn); ok && ref.Asset != "" {
		b.WriteString("![" + fn + "](" + ref.Asset + ")")
	} else {
		b.WriteString("![" + fn + "](attachment:" + fn + ")")
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

func blockquote(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func normalizeBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s) + "\n"
}
