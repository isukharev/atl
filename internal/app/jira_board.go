package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/config"
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
	fields = normalizedBoardFields(fields, false)
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

func (s *JiraService) BoardIssueList(ctx context.Context, boardID int, scope string, columns []string, jql string, limit int, cursor string) (*IssueList, error) {
	return s.BoardIssueListView(ctx, boardID, scope, columns, "", jql, limit, cursor)
}

func (s *JiraService) BoardIssueListView(ctx context.Context, boardID int, scope string, columns []string, view, jql string, limit int, cursor string) (*IssueList, error) {
	selected, preset, err := s.resolveListColumns(config.JiraListSourceBoard, view, columns)
	if err != nil {
		return nil, err
	}
	resolved, fields, err := NormalizeIssueListColumns(selected, nil, "board")
	if err != nil {
		return nil, err
	}
	needColumn := false
	for _, column := range resolved {
		if strings.HasPrefix(column, "board.") {
			needColumn = needColumn || column == "board.column" || column == "board.column_index" || column == "board.column_mapped"
		}
	}
	backendFields := append([]string(nil), fields...)
	if needColumn && !slicesContain(backendFields, "status") {
		backendFields = append(backendFields, "status")
	}
	page, err := s.BoardIssuePage(ctx, boardID, scope, issueListBackendFields(backendFields), jql, limit, cursor)
	if err != nil {
		return nil, err
	}
	var config *domain.BoardConfiguration
	if needColumn {
		config, err = s.BoardConfiguration(ctx, boardID)
		if err != nil {
			return nil, err
		}
	}
	contexts := make([]map[string]map[string]any, len(page.Issues))
	for position, issue := range page.Issues {
		board := map[string]any{"in_board": scope == "board", "in_backlog": scope == "backlog"}
		if needColumn {
			column, index, mapped := boardColumnForStatus(config, issue.StatusID)
			board["column"], board["column_index"], board["column_mapped"] = column, index, mapped
		}
		contexts[position] = map[string]map[string]any{"board": board}
	}
	selection := map[string]any{"scope": scope}
	if strings.TrimSpace(jql) != "" {
		selection["jql"] = jql
	}
	list := NewIssueList(IssueListSource{Kind: "board", ID: strconv.Itoa(boardID)}, selection, resolved, fields, "backend-rank", page.Issues, contexts, page.NextCursor)
	list.Projection.View = preset
	return list, nil
}

// BoardSnapshotOpts controls a complete normalized board read.
type BoardSnapshotOpts struct {
	Scope        string
	Columns      []string
	View         string
	JQL          string
	Limit        int
	EpicField    string
	DoneStatuses []string
}

type BoardProjection struct {
	Kind     string   `json:"kind"`
	Columns  []string `json:"columns"`
	Fields   []string `json:"fields"`
	Ordering string   `json:"ordering"`
	View     string   `json:"view,omitempty"`
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
	EpicRollup     *BoardEpicRollup           `json:"epic_rollup,omitempty"`
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

// BoardEpicRollup is an optional deterministic relation aggregate derived only
// from rows already present in a board snapshot.
type BoardEpicRollup struct {
	EpicField    string                 `json:"epic_field"`
	DoneStatuses []string               `json:"done_statuses"`
	Complete     bool                   `json:"complete"`
	Epics        []BoardEpicRollupEntry `json:"epics"`
}

type BoardEpicRollupEntry struct {
	Key                       string             `json:"key"`
	ParentPresent             bool               `json:"parent_present"`
	ChildCount                int                `json:"child_count"`
	DoneChildCount            int                `json:"done_child_count"`
	StatusCounts              []BoardStatusCount `json:"status_counts"`
	LatestChildUpdated        string             `json:"latest_child_updated,omitempty"`
	TimestampedChildren       int                `json:"timestamped_children"`
	MissingUpdatedChildren    int                `json:"missing_updated_children"`
	TimestampCoverageComplete bool               `json:"timestamp_coverage_complete"`
}

type BoardStatusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
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
	selected, preset, err := s.resolveListColumns(config.JiraListSourceBoardSnapshot, opts.View, opts.Columns)
	if err != nil {
		return nil, err
	}
	columns, fields, err := NormalizeIssueListColumns(selected, nil, "board")
	if err != nil {
		return nil, err
	}
	epicField, doneStatuses, err := normalizeBoardEpicRollupOptions(opts.EpicField, opts.DoneStatuses, fields)
	if err != nil {
		return nil, err
	}
	backendFields := normalizedBoardFields(fields, true)
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
		boardIssues, boardComplete, err = s.collectBoardScope(ctx, boardID, backendFields, opts.JQL, opts.Limit, s.agile.BoardIssues)
		if err != nil {
			return nil, err
		}
	}
	backlogFetched := strings.EqualFold(config.Type, "scrum") && (scope == "all" || scope == "backlog")
	if backlogFetched {
		backlogIssues, backlogComplete, err = s.collectBoardScope(ctx, boardID, backendFields, opts.JQL, opts.Limit, s.agile.BoardBacklog)
		if err != nil {
			return nil, err
		}
	}
	result := &BoardSnapshot{
		SchemaVersion: 1, Board: config, Scope: scope,
		Projection: BoardProjection{Kind: "jira-fields-v1", Columns: columns, Fields: fields, Ordering: "backend-rank", View: preset},
		Rows:       []BoardSnapshotRow{}, Complete: boardComplete && backlogComplete, BacklogFetched: backlogFetched,
	}
	result.Truncated = !result.Complete
	byKey := map[string]int{}
	epicRelations := map[string]string{}
	for position, issue := range boardIssues {
		if epicField != "" {
			relation, present, relationErr := boardEpicRelation(issue, epicField)
			if relationErr != nil {
				return nil, relationErr
			}
			if present {
				epicRelations[issue.Key] = relation
			}
		}
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
		if epicField != "" {
			relation, present, relationErr := boardEpicRelation(issue, epicField)
			if relationErr != nil {
				return nil, relationErr
			}
			if present {
				epicRelations[issue.Key] = relation
			}
		}
		row := boardSnapshotRow(issue, fields, config, len(result.Rows))
		row.InBacklog, row.BacklogPosition = true, &p
		byKey[issue.Key] = len(result.Rows)
		result.Rows = append(result.Rows, row)
	}
	result.RowCount = len(result.Rows)
	if epicField != "" {
		result.EpicRollup, err = boardEpicRollup(result.Rows, epicRelations, epicField, doneStatuses, result.Complete)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func normalizeBoardEpicRollupOptions(epicField string, rawDoneStatuses, fields []string) (string, []string, error) {
	epicField = strings.TrimSpace(epicField)
	doneStatuses := make([]string, 0, len(rawDoneStatuses))
	seenStatuses := map[string]bool{}
	for _, raw := range rawDoneStatuses {
		for _, candidate := range strings.Split(raw, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				return "", nil, fmt.Errorf("%w: --done-status values must be non-empty", domain.ErrUsage)
			}
			folded := strings.ToLower(candidate)
			if seenStatuses[folded] {
				return "", nil, fmt.Errorf("%w: duplicate --done-status %q", domain.ErrUsage, candidate)
			}
			seenStatuses[folded] = true
			doneStatuses = append(doneStatuses, candidate)
		}
	}
	sort.Slice(doneStatuses, func(i, j int) bool {
		return strings.ToLower(doneStatuses[i]) < strings.ToLower(doneStatuses[j])
	})
	if epicField == "" {
		if len(doneStatuses) != 0 {
			return "", nil, fmt.Errorf("%w: --done-status requires --epic-field", domain.ErrUsage)
		}
		return "", nil, nil
	}
	if len(doneStatuses) == 0 {
		return "", nil, fmt.Errorf("%w: --epic-field requires at least one --done-status", domain.ErrUsage)
	}
	for _, required := range []string{epicField, "updated"} {
		found := false
		for _, field := range fields {
			if field == required {
				found = true
				break
			}
		}
		if !found {
			return "", nil, fmt.Errorf("%w: epic rollup requires %q in --columns or the selected view", domain.ErrUsage, required)
		}
	}
	return epicField, doneStatuses, nil
}

func boardEpicRelation(issue domain.Issue, epicField string) (string, bool, error) {
	raw := issueListField(issue, epicField)
	if raw == nil {
		return "", false, nil
	}
	switch value := raw.(type) {
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return "", false, nil
		}
		return value, true, nil
	case map[string]any:
		rawKey, ok := value["key"]
		if !ok {
			return "", false, fmt.Errorf("%w: board row %q has an epic relation object without a key", domain.ErrCheckFailed, issue.Key)
		}
		key, ok := rawKey.(string)
		if !ok || strings.TrimSpace(key) == "" {
			return "", false, fmt.Errorf("%w: board row %q has an invalid epic relation key", domain.ErrCheckFailed, issue.Key)
		}
		return strings.TrimSpace(key), true, nil
	default:
		return "", false, fmt.Errorf("%w: board row %q has unsupported epic relation type", domain.ErrCheckFailed, issue.Key)
	}
}

func boardEpicRollup(rows []BoardSnapshotRow, epicRelations map[string]string, epicField string, doneStatuses []string, snapshotComplete bool) (*BoardEpicRollup, error) {
	type aggregate struct {
		entry       BoardEpicRollupEntry
		status      map[string]int
		latest      time.Time
		latestKnown bool
	}
	parents := make(map[string]bool, len(rows))
	for _, row := range rows {
		parents[row.Key] = true
	}
	byEpic := map[string]*aggregate{}
	for _, row := range rows {
		relation, related := epicRelations[row.Key]
		if !related {
			continue
		}
		item := byEpic[relation]
		if item == nil {
			item = &aggregate{
				entry:  BoardEpicRollupEntry{Key: relation},
				status: map[string]int{},
			}
			byEpic[relation] = item
		}
		status := strings.TrimSpace(row.Status)
		if status == "" {
			return nil, fmt.Errorf("%w: board child row %q has no status", domain.ErrCheckFailed, row.Key)
		}
		item.entry.ChildCount++
		item.status[status]++
		if boardStatusMatches(status, doneStatuses) {
			item.entry.DoneChildCount++
		}

		rawUpdated := row.Values["updated"]
		if rawUpdated == nil || strings.TrimSpace(snapshotText(rawUpdated)) == "" {
			item.entry.MissingUpdatedChildren++
			continue
		}
		updated, ok := rawUpdated.(string)
		if !ok {
			return nil, fmt.Errorf("%w: board child row %q has non-string updated timestamp", domain.ErrCheckFailed, row.Key)
		}
		parsed, err := parseJiraHistoryTime(updated)
		if err != nil {
			return nil, fmt.Errorf("%w: board child row %q has unsupported updated timestamp", domain.ErrCheckFailed, row.Key)
		}
		item.entry.TimestampedChildren++
		if !item.latestKnown || parsed.After(item.latest) {
			item.latest, item.latestKnown = parsed, true
			item.entry.LatestChildUpdated = updated
		}
	}

	keys := make([]string, 0, len(byEpic))
	for key := range byEpic {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rollup := &BoardEpicRollup{
		EpicField: epicField, DoneStatuses: append([]string(nil), doneStatuses...),
		Complete: snapshotComplete, Epics: make([]BoardEpicRollupEntry, 0, len(keys)),
	}
	for _, key := range keys {
		item := byEpic[key]
		item.entry.ParentPresent = parents[key]
		item.entry.TimestampCoverageComplete = item.entry.MissingUpdatedChildren == 0
		if !item.entry.ParentPresent || !item.entry.TimestampCoverageComplete {
			rollup.Complete = false
		}
		statuses := make([]string, 0, len(item.status))
		for status := range item.status {
			statuses = append(statuses, status)
		}
		sort.Strings(statuses)
		item.entry.StatusCounts = make([]BoardStatusCount, 0, len(statuses))
		for _, status := range statuses {
			item.entry.StatusCounts = append(item.entry.StatusCounts, BoardStatusCount{Status: status, Count: item.status[status]})
		}
		rollup.Epics = append(rollup.Epics, item.entry)
	}
	return rollup, nil
}

func boardStatusMatches(status string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(status, candidate) {
			return true
		}
	}
	return false
}

func normalizedBoardFields(fields []string, requireStatus bool) []string {
	if len(fields) == 0 {
		fields = defaultBoardFields
	}
	out := make([]string, 0, len(fields)+1)
	seen := map[string]bool{}
	ordered := append([]string(nil), fields...)
	if requireStatus {
		ordered = append([]string{"status"}, ordered...)
	}
	for _, field := range ordered {
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
	columns := snapshot.Projection.Columns
	if len(columns) == 0 {
		columns = []string{"position", "key", "summary", "status", "board.column", "assignee"}
	}
	fields := snapshot.Projection.Fields
	list := &IssueList{SchemaVersion: issueListSchemaVersion, Source: IssueListSource{Kind: "board"}, Selection: map[string]any{"scope": snapshot.Scope}, Projection: IssueListProjection{Columns: columns, Fields: fields, Ordering: "backend-rank", View: snapshot.Projection.View}, Rows: []IssueListRow{}, Page: IssueListPage{Count: snapshot.RowCount, Complete: snapshot.Complete, Truncated: snapshot.Truncated}}
	if snapshot.Board != nil {
		list.Source.ID = strconv.Itoa(snapshot.Board.ID)
		list.Source.Name = snapshot.Board.Name
	}
	for _, row := range snapshot.Rows {
		values := make(map[string]any, len(fields))
		for _, field := range fields {
			if field == "status" {
				values[field] = row.Status
			} else {
				values[field] = row.Values[field]
			}
		}
		context := map[string]map[string]any{"board": {"column": row.Column, "column_index": row.ColumnIndex, "column_mapped": row.ColumnMapped, "in_backlog": row.InBacklog, "in_board": row.InBoard}}
		list.Rows = append(list.Rows, IssueListRow{Key: row.Key, ID: row.ID, Position: row.Position, Values: values, Context: context})
	}
	md := IssueListMarkdown(list, false)
	if snapshot.Board != nil && strings.EqualFold(snapshot.Board.Type, "kanban") && !snapshot.BacklogFetched {
		md = strings.Replace(md, "# Jira issues\n\n", "# Jira issues\n\n> Kanban board; Jira's Scrum backlog endpoint was not queried.\n\n", 1)
	}
	if snapshot.EpicRollup != nil {
		rows := make([][]string, 0, len(snapshot.EpicRollup.Epics))
		for _, epic := range snapshot.EpicRollup.Epics {
			statuses := make([]string, 0, len(epic.StatusCounts))
			for _, status := range epic.StatusCounts {
				statuses = append(statuses, fmt.Sprintf("%s=%d", status.Status, status.Count))
			}
			rows = append(rows, []string{
				epic.Key,
				strconv.FormatBool(epic.ParentPresent),
				strconv.Itoa(epic.ChildCount),
				strconv.Itoa(epic.DoneChildCount),
				epic.LatestChildUpdated,
				strconv.FormatBool(epic.TimestampCoverageComplete),
				strings.Join(statuses, ", "),
			})
		}
		md += "\n## Epic rollup\n\n"
		md += MarkdownTable(
			[]string{"Epic field", "Done statuses", "Complete"},
			[][]string{{
				snapshot.EpicRollup.EpicField,
				strings.Join(snapshot.EpicRollup.DoneStatuses, ", "),
				strconv.FormatBool(snapshot.EpicRollup.Complete),
			}},
		)
		md += "\n"
		md += MarkdownTable(
			[]string{"Epic", "Parent present", "Children", "Done", "Latest child update", "Timestamps complete", "Status counts"},
			rows,
		)
	}
	return md
}

func optionalInt(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}
