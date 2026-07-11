package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

var defaultBoardFields = []string{"summary", "status", "assignee", "priority", "issuetype"}

// BoardIssuePage is one explicit Agile API page. Complete is false when a next
// cursor exists; callers can continue without guessing whether output truncated.
type BoardIssuePage struct {
	BoardID    int            `json:"board_id"`
	Scope      string         `json:"scope"`
	Fields     []string       `json:"fields"`
	Issues     []domain.Issue `json:"issues"`
	Count      int            `json:"count"`
	Complete   bool           `json:"complete"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// BoardIssuePage reads one board or backlog page.
func (s *JiraService) BoardIssuePage(ctx context.Context, boardID int, scope string, fields []string, jql string, limit int, cursor string) (*BoardIssuePage, error) {
	if boardID <= 0 {
		return nil, fmt.Errorf("%w: board id must be positive", domain.ErrUsage)
	}
	if limit < 0 {
		return nil, fmt.Errorf("%w: --limit must be non-negative", domain.ErrUsage)
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != "board" && scope != "backlog" {
		return nil, fmt.Errorf("%w: board issue scope must be board or backlog", domain.ErrUsage)
	}
	fields = normalizedBoardFields(fields)
	if scope == "backlog" {
		config, err := s.BoardConfiguration(ctx, boardID)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(config.Type, "scrum") {
			return nil, fmt.Errorf("%w: Jira Software exposes the backlog issue endpoint only for Scrum boards; inspect board columns/issues instead", domain.ErrUsage)
		}
	}
	var issues []domain.Issue
	var next string
	var err error
	if scope == "backlog" {
		issues, next, err = s.BoardBacklog(ctx, boardID, fields, jql, limit, cursor)
	} else {
		issues, next, err = s.BoardIssues(ctx, boardID, fields, jql, limit, cursor)
	}
	if err != nil {
		return nil, err
	}
	return &BoardIssuePage{BoardID: boardID, Scope: scope, Fields: fields, Issues: issues, Count: len(issues), Complete: next == "", NextCursor: next}, nil
}

// BoardSnapshotOpts controls a complete normalized board read.
type BoardSnapshotOpts struct {
	Scope  string
	Fields []string
	JQL    string
	Limit  int
}

type BoardProjection struct {
	Kind     string   `json:"kind"`
	Fields   []string `json:"fields"`
	Ordering string   `json:"ordering"`
}

// BoardSnapshot is a jq-friendly workflow snapshot. All scope membership and
// unknown status mappings remain explicit.
type BoardSnapshot struct {
	SchemaVersion  int                        `json:"schema_version"`
	Board          *domain.BoardConfiguration `json:"board"`
	Scope          string                     `json:"scope"`
	Projection     BoardProjection            `json:"projection"`
	Rows           []BoardSnapshotRow         `json:"rows"`
	RowCount       int                        `json:"row_count"`
	Complete       bool                       `json:"complete"`
	Truncated      bool                       `json:"truncated"`
	BacklogFetched bool                       `json:"backlog_fetched"`
}

type BoardSnapshotRow struct {
	Key             string         `json:"key"`
	ID              string         `json:"id,omitempty"`
	Position        int            `json:"position"`
	BoardPosition   *int           `json:"board_position,omitempty"`
	BacklogPosition *int           `json:"backlog_position,omitempty"`
	InBoard         bool           `json:"in_board"`
	InBacklog       bool           `json:"in_backlog"`
	StatusID        string         `json:"status_id,omitempty"`
	Status          string         `json:"status"`
	Column          string         `json:"column"`
	ColumnIndex     int            `json:"column_index"`
	ColumnMapped    bool           `json:"column_mapped"`
	Values          map[string]any `json:"values"`
}

type boardScopePageFunc func(context.Context, int, []string, string, int, string) ([]domain.Issue, string, error)

func (s *JiraService) collectBoardScope(ctx context.Context, boardID int, fields []string, jql string, limit int, page boardScopePageFunc) ([]domain.Issue, bool, error) {
	var out []domain.Issue
	cursor := ""
	seenKeys := map[string]bool{}
	for pages := 0; pages < 10000; pages++ {
		pageSize := 50
		if limit > 0 && limit-len(out) < pageSize {
			pageSize = limit - len(out)
		}
		if pageSize <= 0 {
			return out, false, nil
		}
		issues, next, err := page(ctx, boardID, fields, jql, pageSize, cursor)
		if err != nil {
			return nil, false, err
		}
		for _, issue := range issues {
			if seenKeys[issue.Key] {
				return nil, false, fmt.Errorf("%w: board pagination repeated issue %q; retry the read", domain.ErrCheckFailed, issue.Key)
			}
			seenKeys[issue.Key] = true
			out = append(out, issue)
			if limit > 0 && len(out) >= limit {
				return out, next == "", nil
			}
		}
		if next == "" {
			return out, true, nil
		}
		if next == cursor {
			return nil, false, fmt.Errorf("%w: board pagination cursor did not advance", domain.ErrCheckFailed)
		}
		cursor = next
	}
	return nil, false, fmt.Errorf("%w: board pagination exceeded the safety cap", domain.ErrCheckFailed)
}

func (s *JiraService) BoardSnapshot(ctx context.Context, boardID int, opts BoardSnapshotOpts) (*BoardSnapshot, error) {
	if boardID <= 0 {
		return nil, fmt.Errorf("%w: board id must be positive", domain.ErrUsage)
	}
	if opts.Limit < 0 {
		return nil, fmt.Errorf("%w: --limit must be non-negative", domain.ErrUsage)
	}
	scope := strings.ToLower(strings.TrimSpace(opts.Scope))
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "board" && scope != "backlog" {
		return nil, fmt.Errorf("%w: --scope must be all, board, or backlog", domain.ErrUsage)
	}
	fields := normalizedBoardFields(opts.Fields)
	config, err := s.BoardConfiguration(ctx, boardID)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, fmt.Errorf("%w: board configuration response was empty", domain.ErrCheckFailed)
	}
	var boardIssues, backlogIssues []domain.Issue
	boardComplete, backlogComplete := true, true
	if !strings.EqualFold(config.Type, "scrum") && scope == "backlog" {
		return nil, fmt.Errorf("%w: Jira Software exposes the backlog issue endpoint only for Scrum boards; use --scope board and configured columns", domain.ErrUsage)
	}
	if scope == "all" || scope == "board" {
		boardIssues, boardComplete, err = s.collectBoardScope(ctx, boardID, fields, opts.JQL, opts.Limit, s.agile.BoardIssues)
		if err != nil {
			return nil, err
		}
	}
	backlogFetched := strings.EqualFold(config.Type, "scrum") && (scope == "all" || scope == "backlog")
	if backlogFetched {
		backlogIssues, backlogComplete, err = s.collectBoardScope(ctx, boardID, fields, opts.JQL, opts.Limit, s.agile.BoardBacklog)
		if err != nil {
			return nil, err
		}
	}
	result := &BoardSnapshot{
		SchemaVersion: 1, Board: config, Scope: scope,
		Projection: BoardProjection{Kind: "jira-fields-v1", Fields: fields, Ordering: "backend-rank"},
		Rows:       []BoardSnapshotRow{}, Complete: boardComplete && backlogComplete, BacklogFetched: backlogFetched,
	}
	result.Truncated = !result.Complete
	byKey := map[string]int{}
	for position, issue := range boardIssues {
		p := position
		row := boardSnapshotRow(issue, fields, config, len(result.Rows))
		row.InBoard, row.BoardPosition = true, &p
		byKey[issue.Key] = len(result.Rows)
		result.Rows = append(result.Rows, row)
	}
	for position, issue := range backlogIssues {
		p := position
		if index, ok := byKey[issue.Key]; ok {
			result.Rows[index].InBacklog = true
			result.Rows[index].BacklogPosition = &p
			continue
		}
		row := boardSnapshotRow(issue, fields, config, len(result.Rows))
		row.InBacklog, row.BacklogPosition = true, &p
		byKey[issue.Key] = len(result.Rows)
		result.Rows = append(result.Rows, row)
	}
	result.RowCount = len(result.Rows)
	return result, nil
}

func normalizedBoardFields(fields []string) []string {
	if len(fields) == 0 {
		fields = defaultBoardFields
	}
	out := make([]string, 0, len(fields)+1)
	seen := map[string]bool{}
	for _, field := range append([]string{"status"}, fields...) {
		field = strings.TrimSpace(field)
		if field != "" && !seen[field] {
			seen[field] = true
			out = append(out, field)
		}
	}
	return out
}

func boardSnapshotRow(issue domain.Issue, fields []string, config *domain.BoardConfiguration, position int) BoardSnapshotRow {
	column, columnIndex, mapped := boardColumnForStatus(config, issue.StatusID)
	values := make(map[string]any, len(fields))
	for _, field := range fields {
		values[field] = normalizedSnapshotValue(issue.Fields[field])
	}
	return BoardSnapshotRow{
		Key: issue.Key, ID: issue.ID, Position: position,
		StatusID: issue.StatusID, Status: issue.Status,
		Column: column, ColumnIndex: columnIndex, ColumnMapped: mapped,
		Values: values,
	}
}

func boardColumnForStatus(config *domain.BoardConfiguration, statusID string) (string, int, bool) {
	if config != nil {
		for index, column := range config.Columns {
			for _, candidate := range column.StatusIDs {
				if statusID != "" && candidate == statusID {
					return column.Name, index, true
				}
			}
		}
	}
	return "Unmapped", -1, false
}

type BoardExportOpts struct {
	BoardSnapshotOpts
	Format string
	Out    string
	RawCSV bool
}

type BoardExportResult struct {
	Path     string `json:"path"`
	Format   string `json:"format"`
	BoardID  int    `json:"board_id"`
	RowCount int    `json:"row_count"`
	Complete bool   `json:"complete"`
}

func (s *JiraService) BoardExport(ctx context.Context, boardID int, opts BoardExportOpts) (*BoardExportResult, error) {
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "json"
	}
	if format == "markdown" {
		format = "md"
	}
	if format != "json" && format != "jsonl" && format != "csv" && format != "md" {
		return nil, fmt.Errorf("%w: --format must be json, jsonl, csv, or md", domain.ErrUsage)
	}
	if strings.TrimSpace(opts.Out) == "" || opts.Out == "-" {
		return nil, fmt.Errorf("%w: --out is required and must be a file path", domain.ErrUsage)
	}
	if opts.RawCSV && format != "csv" {
		return nil, fmt.Errorf("%w: --raw-csv requires --format csv", domain.ErrUsage)
	}
	snapshot, err := s.BoardSnapshot(ctx, boardID, opts.BoardSnapshotOpts)
	if err != nil {
		return nil, err
	}
	data, err := renderBoardSnapshot(format, snapshot, opts.RawCSV)
	if err != nil {
		return nil, err
	}
	if err := writeUserFile(opts.Out, data); err != nil {
		return nil, err
	}
	return &BoardExportResult{Path: opts.Out, Format: format, BoardID: boardID, RowCount: snapshot.RowCount, Complete: snapshot.Complete}, nil
}

func renderBoardSnapshot(format string, snapshot *BoardSnapshot, rawCSV bool) ([]byte, error) {
	switch format {
	case "json":
		data, err := json.MarshalIndent(snapshot, "", "  ")
		return append(data, '\n'), err
	case "jsonl":
		var b bytes.Buffer
		encoder := json.NewEncoder(&b)
		encoder.SetEscapeHTML(false)
		for _, row := range snapshot.Rows {
			record := struct {
				SchemaVersion  int              `json:"schema_version"`
				BoardID        int              `json:"board_id"`
				BoardName      string           `json:"board_name"`
				BoardType      string           `json:"board_type"`
				Scope          string           `json:"scope"`
				Projection     BoardProjection  `json:"projection"`
				RowCount       int              `json:"row_count"`
				Complete       bool             `json:"complete"`
				Truncated      bool             `json:"truncated"`
				BacklogFetched bool             `json:"backlog_fetched"`
				Row            BoardSnapshotRow `json:"row"`
			}{snapshot.SchemaVersion, snapshot.Board.ID, snapshot.Board.Name, snapshot.Board.Type, snapshot.Scope, snapshot.Projection, snapshot.RowCount, snapshot.Complete, snapshot.Truncated, snapshot.BacklogFetched, row}
			if err := encoder.Encode(record); err != nil {
				return nil, err
			}
		}
		return b.Bytes(), nil
	case "csv":
		return renderBoardSnapshotCSV(snapshot, rawCSV)
	case "md":
		return []byte(BoardSnapshotMarkdown(snapshot)), nil
	default:
		return nil, fmt.Errorf("%w: unsupported board export format %q", domain.ErrUsage, format)
	}
}

func renderBoardSnapshotCSV(snapshot *BoardSnapshot, rawCSV bool) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	header := append([]string{"position", "key", "id", "status_id", "status", "column", "column_index", "column_mapped", "in_board", "in_backlog", "board_position", "backlog_position"}, snapshot.Projection.Fields...)
	if err := w.Write(spreadsheetRecord(header, rawCSV)); err != nil {
		return nil, err
	}
	for _, row := range snapshot.Rows {
		record := []string{strconv.Itoa(row.Position), row.Key, row.ID, row.StatusID, row.Status, row.Column, strconv.Itoa(row.ColumnIndex), strconv.FormatBool(row.ColumnMapped), strconv.FormatBool(row.InBoard), strconv.FormatBool(row.InBacklog), optionalInt(row.BoardPosition), optionalInt(row.BacklogPosition)}
		for _, field := range snapshot.Projection.Fields {
			record = append(record, snapshotText(row.Values[field]))
		}
		if err := w.Write(spreadsheetRecord(record, rawCSV)); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return b.Bytes(), w.Error()
}

func BoardSnapshotMarkdown(snapshot *BoardSnapshot) string {
	var b strings.Builder
	name := ""
	if snapshot != nil && snapshot.Board != nil {
		name = snapshot.Board.Name
	}
	b.WriteString("# Jira Board")
	if name != "" {
		b.WriteString(": ")
		b.WriteString(markdownTableCell(name))
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "> Scope: `%s`; ordering: `backend-rank`; complete: `%t`; backlog fetched: `%t`; rows: %d.\n\n", snapshot.Scope, snapshot.Complete, snapshot.BacklogFetched, snapshot.RowCount)
	if snapshot.Board != nil && strings.EqualFold(snapshot.Board.Type, "kanban") && snapshot.Scope == "all" {
		b.WriteString("> Kanban note: Jira's backlog issue endpoint is Scrum-only; use configured columns to interpret Kanban workflow scope.\n\n")
	}
	if snapshot.Truncated {
		b.WriteString("> **Truncated:** increase or remove `--limit` for a complete snapshot.\n\n")
	}
	header := []string{"#", "Key", "Status", "Column", "Backlog"}
	fields := make([]string, 0, len(snapshot.Projection.Fields))
	for _, field := range snapshot.Projection.Fields {
		if field == "status" {
			continue
		}
		fields = append(fields, field)
		header = append(header, markdownTableCell(field))
	}
	b.WriteString("| ")
	b.WriteString(strings.Join(header, " | "))
	b.WriteString(" |\n|")
	for range header {
		b.WriteString(" --- |")
	}
	b.WriteByte('\n')
	for _, row := range snapshot.Rows {
		cells := []string{strconv.Itoa(row.Position), markdownTableCell(row.Key), markdownTableCell(row.Status), markdownTableCell(row.Column), strconv.FormatBool(row.InBacklog)}
		for _, field := range fields {
			cells = append(cells, markdownTableCell(snapshotText(row.Values[field])))
		}
		b.WriteString("| ")
		b.WriteString(strings.Join(cells, " | "))
		b.WriteString(" |\n")
	}
	return b.String()
}

func optionalInt(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}
