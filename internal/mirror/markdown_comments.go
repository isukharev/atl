package mirror

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/csf"
)

func renderQualifiedCommentMarkdownVersion(root *csf.Node, parentHeading int, format confluenceMarkdownFormat) string {
	r := newMDRendererOffsetVersion(nil, parentHeading, format)
	r.headingOverflowAsStrong = true
	return renderCommentMarkdownWithRenderer(root, r)
}

func renderCommentMarkdownWithRenderer(root *csf.Node, r *mdRenderer) string {
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
