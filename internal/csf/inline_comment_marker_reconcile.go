package csf

import (
	"bytes"
	"fmt"
	"strings"
)

// ReconcileInlineCommentMarkerInsertion proves that after differs from before
// only by one server-owned ac:inline-comment-marker wrapper. It is deliberately
// read-only: the reconstructed bytes exist only for exact comparison and are
// never suitable for a page write.
func ReconcileInlineCommentMarkerInsertion(before, after []byte, markerRef, selection string) (bool, error) {
	if strings.TrimSpace(markerRef) == "" || selection == "" {
		return false, nil
	}
	beforeRoot, err := Parse(before)
	if err != nil {
		return false, fmt.Errorf("parse prewrite CSF: %w", err)
	}
	afterRoot, err := Parse(after)
	if err != nil {
		return false, fmt.Errorf("parse readback CSF: %w", err)
	}
	if countInlineCommentMarkerRef(beforeRoot, markerRef) != 0 {
		return false, nil
	}

	var candidate *Node
	Walk(afterRoot, func(node *Node) bool {
		if node.Name.Space != "ac" || node.Name.Local != "inline-comment-marker" || node.Attrv("ac", "ref") != markerRef {
			return true
		}
		if candidate == nil {
			candidate = node
		} else {
			candidate = afterRoot // a sentinel that cannot be a marker
		}
		return true
	})
	if candidate == nil || candidate == afterRoot || inlineCommentMarkerText(candidate) != selection {
		return false, nil
	}
	if candidate.Start < 0 || candidate.End > len(after) || candidate.Start >= candidate.End {
		return false, nil
	}

	outer := after[candidate.Start:candidate.End]
	startTagEnd, ok := inlineMarkerStartTagEnd(outer)
	if !ok {
		return false, nil
	}
	endTagStart := bytes.LastIndex(outer, []byte("</"))
	if endTagStart <= startTagEnd || !bytes.Equal(bytes.TrimSpace(outer[endTagStart:]), []byte("</ac:inline-comment-marker>")) {
		return false, nil
	}

	reconstructed := make([]byte, 0, len(after)-(startTagEnd+1)-(len(outer)-endTagStart))
	reconstructed = append(reconstructed, after[:candidate.Start]...)
	reconstructed = append(reconstructed, outer[startTagEnd+1:endTagStart]...)
	reconstructed = append(reconstructed, after[candidate.End:]...)
	return bytes.Equal(reconstructed, before), nil
}

func inlineCommentMarkerText(node *Node) string {
	var text strings.Builder
	var visit func(*Node)
	visit = func(current *Node) {
		if current.Type == Text || current.Type == CData {
			text.WriteString(current.Data)
		}
		for _, child := range current.Children {
			visit(child)
		}
	}
	visit(node)
	return text.String()
}

func countInlineCommentMarkerRef(root *Node, markerRef string) int {
	count := 0
	Walk(root, func(node *Node) bool {
		if node.Name.Space == "ac" && node.Name.Local == "inline-comment-marker" && node.Attrv("ac", "ref") == markerRef {
			count++
		}
		return true
	})
	return count
}

func inlineMarkerStartTagEnd(raw []byte) (int, bool) {
	quote := byte(0)
	for index, value := range raw {
		switch {
		case quote != 0 && value == quote:
			quote = 0
		case quote == 0 && (value == '\'' || value == '"'):
			quote = value
		case quote == 0 && value == '>':
			return index, index > 0 && raw[index-1] != '/'
		}
	}
	return 0, false
}
