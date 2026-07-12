package mirror

import (
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/csf"
)

// JiraMacroDescriptor is one Jira JQL macro in CSF document order. Index counts
// all Jira macros, including single-key links, so one occurrence has a stable
// identity within the exact body revision.
type JiraMacroDescriptor struct {
	Index   int      `json:"index"`
	JQL     string   `json:"jql"`
	Columns []string `json:"columns,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

// JiraMacroDescriptors extracts JQL-bearing Jira macros without executing
// them. Single-key macros remain ordinary jira: links and are not returned.
func JiraMacroDescriptors(root *csf.Node) []JiraMacroDescriptor {
	var out []JiraMacroDescriptor
	index := 0
	csf.Walk(root, func(node *csf.Node) bool {
		if node.Type != csf.Element || node.Name.Space != "ac" ||
			(node.Name.Local != "structured-macro" && node.Name.Local != "macro") ||
			node.Attrv("ac", "name") != "jira" {
			return true
		}
		current := index
		index++
		jql := strings.TrimSpace(macroParam(node, "jqlQuery"))
		if jql == "" {
			return true
		}
		descriptor := JiraMacroDescriptor{Index: current, JQL: jql}
		for _, raw := range strings.Split(macroParam(node, "columns"), ",") {
			if column := strings.TrimSpace(raw); column != "" {
				descriptor.Columns = append(descriptor.Columns, column)
			}
		}
		if limit, err := strconv.Atoi(strings.TrimSpace(macroParam(node, "maximumIssues"))); err == nil && limit > 0 {
			descriptor.Limit = limit
		}
		out = append(out, descriptor)
		return true
	})
	return out
}

// JiraMacroView is an already-safe Markdown projection supplied by the app
// layer. It is rendered in a generated read-only suffix, never in editable CSF
// body space.
type JiraMacroView struct {
	Index     int
	Markdown  string
	Complete  bool
	Truncated bool
}
