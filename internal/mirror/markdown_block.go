package mirror

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/wikiscanner"
)

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
		level := int(n.Name.Local[1]-'0') + r.headingOffset
		if level > 6 && r.headingOverflowAsStrong {
			fmt.Fprintf(b, "**%s**\n\n", r.inline(n))
			return
		}
		level = min(6, level)
		fmt.Fprintf(b, "%s %s\n\n", strings.Repeat("#", level), r.inline(n))
	case n.Name.Local == "p" && n.Name.Space == "":
		// Confluence routinely wraps a single block macro in <p>; route it to
		// the block handler so it keeps its body instead of degrading to ⟦name⟧.
		if m := soleBlockMacro(n); m != nil {
			r.macro(b, m)
			return
		}
		if s := strings.TrimSpace(r.inline(n)); s != "" {
			if r.format == confluenceMarkdownCurrent {
				s = wikiscanner.EscapeMarkdownBlockCollision(s)
			}
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
		body := csf.TextContent(n)
		fence := "```"
		if r.format == confluenceMarkdownCurrent {
			fence = markdownFence(body)
		}
		fmt.Fprintf(b, "%s\n%s\n%s\n\n", fence, body, fence)
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

func isHeading(n csf.Name) bool {
	return n.Space == "" && len(n.Local) == 2 && n.Local[0] == 'h' && n.Local[1] >= '1' && n.Local[1] <= '6'
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

func safeMarkdownFenceInfo(info string) string {
	info, _ = wikiscanner.NormalizeMarkdownFenceInfo(info)
	return info
}
