package mirror

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

func (r *mdRenderer) macro(b *strings.Builder, n *csf.Node) {
	name := n.Attrv("ac", "name")
	switch name {
	case "code":
		lang := macroParam(n, "language")
		body := plainBody(n)
		fence := "```"
		if r.format == confluenceMarkdownCurrent {
			fence = markdownFence(body)
			lang = safeMarkdownFenceInfo(lang)
		}
		fmt.Fprintf(b, "%s%s\n%s\n%s\n\n", fence, lang, body, fence)
	case "noformat":
		body := plainBody(n)
		fence := "```"
		if r.format == confluenceMarkdownCurrent {
			fence = markdownFence(body)
		}
		fmt.Fprintf(b, "%s\n%s\n%s\n\n", fence, body, fence)
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
			fmt.Fprintf(b, "[%s](%s)\n\n", key, r.resolvedLink("jira:"+key))
		} else if jql := macroParam(n, "jqlQuery"); jql != "" {
			fmt.Fprintf(b, "⟦jira query: %s⟧\n\n", jql)
		} else {
			b.WriteString("⟦jira⟧\n\n")
		}
	case "view-file":
		if fn := attachmentNameUnder(n); fn != "" {
			fmt.Fprintf(b, "📎 [%s](%s)\n\n", fn, r.resolvedLink("attachment:"+fn))
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
