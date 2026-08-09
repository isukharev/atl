package cli

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/app"
)

func confluenceSearchText(result *app.ConfluenceSearchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "> CQL search; complete: %t; rows: %d.\n", result.Complete, result.Count)
	if result.Truncated {
		if result.NextCursor != nil {
			b.WriteString("> **Truncated:** continue with `next_cursor`; absence claims are not supported.\n")
		} else {
			b.WriteString("> **Truncated:** no safe continuation cursor; narrow the query or investigate terminal pagination evidence. Absence claims are not supported.\n")
		}
	}
	rows := make([][]string, len(result.Results))
	for index, hit := range result.Results {
		rows[index] = []string{hit.ID, fmt.Sprintf("v%d", hit.Version), hit.Space, hit.Title, hit.Excerpt}
	}
	b.WriteString("\n")
	b.WriteString(app.MarkdownTable([]string{"ID", "Version", "Space", "Title", "Excerpt"}, rows))
	return strings.TrimRight(b.String(), "\n")
}
