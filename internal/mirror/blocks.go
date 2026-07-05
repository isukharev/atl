package mirror

import (
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// Block is one renderable unit of a CSF body: the byte range of its source
// node in the raw CSF and the markdown it renders to. Blocks come in document
// order with non-overlapping ranges. Bytes between consecutive blocks (layout
// container tags, whitespace, comments, nodes that render to nothing) belong
// to no block and must be preserved verbatim by any merge.
type Block struct {
	CSFStart int    `json:"csf_start"` // first byte of the source node in the raw body
	CSFEnd   int    `json:"csf_end"`   // one past the last byte
	MD       string `json:"md"`        // normalized markdown: trimmed, blank runs collapsed
	Kind     string `json:"kind"`      // "h1".."h6", "p", "table", "list", "tasklist", "blockquote", "pre", "hr", "macro:<name>", "text", "other"
}

// RenderBlocks renders a parsed CSF body block by block for the md→CSF merge.
// Joining the MD fields with blank lines yields RenderMarkdown's output up to
// whitespace normalization (each MD is individually trimmed and collapsed;
// alignment must normalize the compared side the same way). Blocks that render
// to nothing are omitted — their bytes are part of the surrounding gap. An
// "other" block (unknown wrapper element) may span several logical paragraphs;
// a merge must treat it as one opaque unit, never split it.
func RenderBlocks(root *csf.Node, refs []domain.Ref) []Block {
	r := newMDRenderer(refs)
	var out []Block
	forEachBlockNode(root, func(n *csf.Node) {
		var b strings.Builder
		r.block(&b, n)
		md := strings.TrimSpace(collapseBlankRuns(b.String()))
		if md == "" {
			return
		}
		out = append(out, Block{CSFStart: n.Start, CSFEnd: n.End, MD: md, Kind: blockKind(n)})
	})
	return out
}

// forEachBlockNode visits the body's block-level nodes in document order,
// descending through layout containers (which group, but do not render). This
// is the single traversal both RenderMarkdown and RenderBlocks use, so the two
// views can never disagree about what a block is.
func forEachBlockNode(root *csf.Node, fn func(*csf.Node)) {
	var walk func(*csf.Node)
	walk = func(n *csf.Node) {
		if n.Type == csf.Element && n.Name.Space == "ac" &&
			(n.Name.Local == "layout" || n.Name.Local == "layout-section" || n.Name.Local == "layout-cell") {
			for _, c := range n.Children {
				walk(c)
			}
			return
		}
		fn(n)
	}
	for _, c := range root.Children {
		walk(c)
	}
}

// blockKind names a block for reports and merge decisions.
func blockKind(n *csf.Node) string {
	if n.Type != csf.Element {
		return "text"
	}
	if n.Name.Space == "ac" {
		switch n.Name.Local {
		case "structured-macro", "macro":
			return "macro:" + n.Attrv("ac", "name")
		case "task-list":
			return "tasklist"
		}
		return "other"
	}
	if n.Name.Space != "" {
		return "other"
	}
	switch {
	case isHeading(n.Name):
		return n.Name.Local
	case n.Name.Local == "p":
		if m := soleBlockMacro(n); m != nil {
			return "macro:" + m.Attrv("ac", "name")
		}
		return "p"
	case n.Name.Local == "table":
		return "table"
	case n.Name.Local == "ul" || n.Name.Local == "ol":
		return "list"
	case n.Name.Local == "blockquote":
		return "blockquote"
	case n.Name.Local == "pre":
		return "pre"
	case n.Name.Local == "hr":
		return "hr"
	}
	return "other"
}

// collapseBlankRuns squeezes runs of 3+ newlines to a blank line, the same
// normalization normalizeBlankLines applies to the whole document.
func collapseBlankRuns(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
