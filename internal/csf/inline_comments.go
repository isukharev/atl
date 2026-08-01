package csf

import "strings"

// InlineCommentMarker is the selected text associated with one Confluence
// inline-comment marker. Repeated Ref values are retained as separate records.
type InlineCommentMarker struct {
	Ref       string
	Selection string
}

// ExtractInlineCommentMarkers parses raw CSF and returns inline-comment
// markers in document order. Only ac:inline-comment-marker elements carrying a
// non-empty ac:ref attribute are included. Selection uses TextContent, so text
// inside nested inline elements is included with entities decoded.
//
// The input bytes are never modified. Malformed or over-depth CSF returns the
// same parse error as Parse.
func ExtractInlineCommentMarkers(raw []byte) ([]InlineCommentMarker, error) {
	root, err := Parse(raw)
	if err != nil {
		return nil, err
	}

	markers := make([]InlineCommentMarker, 0)
	Walk(root, func(n *Node) bool {
		if n.Name.Space != "ac" || n.Name.Local != "inline-comment-marker" {
			return true
		}

		ref := n.Attrv("ac", "ref")
		if strings.TrimSpace(ref) == "" {
			return true
		}
		markers = append(markers, InlineCommentMarker{
			Ref:       ref,
			Selection: TextContent(n),
		})
		return true
	})
	return markers, nil
}
