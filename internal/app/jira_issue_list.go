package app

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

type IssueListSource struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type IssueListProjection struct {
	Columns  []string `json:"columns"`
	Fields   []string `json:"fields"`
	Ordering string   `json:"ordering"`
	View     string   `json:"view,omitempty"`
}

type IssueListRow struct {
	Key      string                    `json:"key"`
	ID       string                    `json:"id,omitempty"`
	Position int                       `json:"position"`
	Values   map[string]any            `json:"values"`
	Context  map[string]map[string]any `json:"context,omitempty"`
}

type IssueListPage struct {
	Count      int     `json:"count"`
	Complete   bool    `json:"complete"`
	Truncated  bool    `json:"truncated"`
	NextCursor *string `json:"next_cursor"`
}

type IssueList struct {
	SchemaVersion int                 `json:"schema_version"`
	Source        IssueListSource     `json:"source"`
	Selection     map[string]any      `json:"selection"`
	Projection    IssueListProjection `json:"projection"`
	Rows          []IssueListRow      `json:"rows"`
	Page          IssueListPage       `json:"page"`
}

var issueListContextColumns = map[string]bool{
	"board.column": true, "board.column_index": true, "board.column_mapped": true,
	"board.in_board": true, "board.in_backlog": true, "sprint.id": true,
	"structure.row_id": true, "structure.depth": true, "structure.relative_depth": true, "structure.path": true,
	"epic.parent": true, "epic.relation": true,
}

// NormalizeIssueListColumns validates and splits presentation columns from the
// Jira value fields that must be requested from the backend.
func NormalizeIssueListColumns(columns []string, defaults []string, allowedContexts ...string) ([]string, []string, error) {
	if len(columns) == 0 {
		columns = defaults
	}
	allowed := map[string]bool{}
	for _, prefix := range allowedContexts {
		allowed[prefix] = true
	}
	seen, fieldSeen := map[string]bool{}, map[string]bool{}
	normalized, fields := []string{}, []string{}
	for _, raw := range columns {
		column := strings.TrimSpace(raw)
		if column == "" || seen[column] {
			continue
		}
		seen[column] = true
		switch column {
		case "position", "key", "id":
		default:
			if prefix, _, ok := strings.Cut(column, "."); ok {
				if !allowed[prefix] || !issueListContextColumns[column] {
					return nil, nil, fmt.Errorf("%w: column %q is not available for this issue-list source", domain.ErrUsage, column)
				}
			} else if !fieldSeen[column] {
				fieldSeen[column] = true
				fields = append(fields, column)
			}
		}
		normalized = append(normalized, column)
	}
	if len(normalized) == 0 {
		return nil, nil, fmt.Errorf("%w: --columns must select at least one column", domain.ErrUsage)
	}
	return normalized, fields, nil
}

func NewIssueList(source IssueListSource, selection map[string]any, columns, fields []string, ordering string, issues []domain.Issue, contexts []map[string]map[string]any, next string) *IssueList {
	rows := make([]IssueListRow, 0, len(issues))
	for position, issue := range issues {
		values := make(map[string]any, len(fields))
		for _, field := range fields {
			values[field] = normalizedSnapshotValue(issueListField(issue, field))
		}
		var context map[string]map[string]any
		if position < len(contexts) {
			context = contexts[position]
		}
		rows = append(rows, IssueListRow{Key: issue.Key, ID: issue.ID, Position: position, Values: values, Context: context})
	}
	var nextCursor *string
	if next != "" {
		nextValue := next
		nextCursor = &nextValue
	}
	complete := next == ""
	return &IssueList{
		SchemaVersion: 1, Source: source, Selection: selection,
		Projection: IssueListProjection{Columns: columns, Fields: fields, Ordering: ordering},
		Rows:       rows, Page: IssueListPage{Count: len(rows), Complete: complete, Truncated: !complete, NextCursor: nextCursor},
	}
}

func issueListBackendFields(fields []string) []string {
	if len(fields) == 0 {
		// Jira always returns key/id identities, but an explicit minimal fields
		// parameter avoids its broad default projection for identity-only lists.
		return []string{"key"}
	}
	return fields
}

func issueListField(issue domain.Issue, field string) any {
	switch field {
	case "summary":
		return issue.Summary
	case "status":
		return issue.Status
	case "assignee":
		return issue.Assignee
	case "issuetype":
		return issue.Type
	case "project":
		return issue.Project
	case "labels":
		return issue.Labels
	default:
		return issue.Fields[field]
	}
}

func IssueListKeys(list *IssueList) []string {
	keys := make([]string, len(list.Rows))
	for i, row := range list.Rows {
		keys[i] = row.Key
	}
	return keys
}

func IssueListMarkdown(list *IssueList, embedded bool) string {
	var b strings.Builder
	if !embedded {
		b.WriteString("# Jira issues\n\n")
		fmt.Fprintf(&b, "> Source: %s", list.Source.Kind)
		if list.Source.ID != "" {
			fmt.Fprintf(&b, " `%s`", list.Source.ID)
		}
		if list.Source.Name != "" {
			fmt.Fprintf(&b, " — %s", markdownSingleLine(list.Source.Name))
		}
		if list.Projection.View != "" {
			fmt.Fprintf(&b, "; view: %s", list.Projection.View)
		}
		fmt.Fprintf(&b, "; ordering: %s; complete: %t; rows: %d.\n\n", strings.ReplaceAll(list.Projection.Ordering, "-", " "), list.Page.Complete, list.Page.Count)
		if list.Page.Truncated {
			b.WriteString("> **Truncated:** continue with `page.next_cursor` or increase the limit.\n\n")
		}
	}
	headers := make([]string, len(list.Projection.Columns))
	for i, column := range list.Projection.Columns {
		headers[i] = issueListColumnLabel(column)
	}
	rows := make([][]string, len(list.Rows))
	for i, row := range list.Rows {
		cells := make([]string, len(list.Projection.Columns))
		for j, column := range list.Projection.Columns {
			cells[j] = issueListCell(row, column)
		}
		rows[i] = cells
	}
	b.WriteString(MarkdownTable(headers, rows))
	return b.String()
}

func issueListColumnLabel(column string) string {
	labels := map[string]string{
		"position": "#", "key": "Key", "id": "ID", "summary": "Summary", "status": "Status",
		"assignee": "Assignee", "priority": "Priority", "issuetype": "Type", "board.column": "Column",
		"board.in_backlog": "Backlog", "structure.depth": "Depth", "structure.path": "Path", "epic.parent": "Epic", "epic.relation": "Relation",
	}
	if label := labels[column]; label != "" {
		return label
	}
	return column
}

func issueListCell(row IssueListRow, column string) string {
	switch column {
	case "position":
		return strconv.Itoa(row.Position)
	case "key":
		return row.Key
	case "id":
		return row.ID
	}
	if prefix, name, ok := strings.Cut(column, "."); ok {
		return snapshotText(row.Context[prefix][name])
	}
	return snapshotText(row.Values[column])
}
