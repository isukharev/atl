package confluence

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// serializeInlineHighlights reproduces the reviewed browser plugin's compact
// row shape: [text, colon-separated child-node path, previous-text offset,
// UTF-16 length]. It accepts typed geometry only, never arbitrary JSON.
func serializeInlineHighlights(highlights []domain.ConfluenceInlineHighlightGeometry) (string, error) {
	rows := make([][4]any, 0, len(highlights))
	for _, highlight := range highlights {
		path := make([]string, len(highlight.ChildIndexPath))
		for index, childIndex := range highlight.ChildIndexPath {
			path[index] = strconv.Itoa(childIndex)
		}
		rows = append(rows, [4]any{highlight.Text, strings.Join(path, ":"), highlight.PreviousTextSiblingOffset, highlight.Length})
	}
	data, err := json.Marshal(rows)
	return string(data), err
}
