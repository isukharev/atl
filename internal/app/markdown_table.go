package app

import "strings"

// MarkdownTable is the shared safe table primitive for human-facing list
// projections. Callers provide already-normalized text cells; this layer owns
// structural escaping and the explicit empty-list representation.
func MarkdownTable(headers []string, rows [][]string) string {
	if len(rows) == 0 {
		return "_None._\n"
	}
	escapedHeaders := make([]string, len(headers))
	for i, header := range headers {
		escapedHeaders[i] = markdownTableCell(header)
	}
	var b strings.Builder
	b.WriteString("| ")
	b.WriteString(strings.Join(escapedHeaders, " | "))
	b.WriteString(" |\n|")
	for range headers {
		b.WriteString(" --- |")
	}
	b.WriteByte('\n')
	for _, row := range rows {
		cells := make([]string, len(headers))
		for i := range headers {
			if i < len(row) {
				cells[i] = markdownTableCell(row[i])
			}
		}
		b.WriteString("| ")
		b.WriteString(strings.Join(cells, " | "))
		b.WriteString(" |\n")
	}
	return b.String()
}
