package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

const JiraBoardWireSchemaVersion = 1

// JiraBoardSnapshot is the evaluator-owned jira_board_view wire exercised by
// the retained board cohorts. It deliberately duplicates that released public
// JSON projection without importing product packages. Values is the only open
// object because Jira field values are backend- and plugin-defined. These
// cohorts do not request an epic rollup, so the strict decoder rejects that
// optional product projection instead of duplicating its derivation logic.
type JiraBoardSnapshot struct {
	SchemaVersion  int                 `json:"schema_version"`
	Board          JiraBoardConfig     `json:"board"`
	Scope          string              `json:"scope"`
	Projection     JiraBoardProjection `json:"projection"`
	Rows           []JiraBoardRow      `json:"rows"`
	RowCount       int                 `json:"row_count"`
	Complete       bool                `json:"complete"`
	Truncated      bool                `json:"truncated"`
	BacklogFetched bool                `json:"backlog_fetched"`
}

type JiraBoardConfig struct {
	ID              int               `json:"id"`
	Name            string            `json:"name"`
	Type            string            `json:"type"`
	FilterID        string            `json:"filter_id,omitempty"`
	KanbanSubquery  string            `json:"kanban_subquery,omitempty"`
	ConstraintType  string            `json:"constraint_type,omitempty"`
	Columns         []JiraBoardColumn `json:"columns"`
	EstimationType  string            `json:"estimation_type,omitempty"`
	EstimationField string            `json:"estimation_field,omitempty"`
	RankFieldID     string            `json:"rank_field_id,omitempty"`
}

type JiraBoardColumn struct {
	Name      string   `json:"name"`
	StatusIDs []string `json:"status_ids"`
	Min       *int     `json:"min,omitempty"`
	Max       *int     `json:"max,omitempty"`
}

type JiraBoardProjection struct {
	Kind     string   `json:"kind"`
	Columns  []string `json:"columns"`
	Fields   []string `json:"fields"`
	Ordering string   `json:"ordering"`
	View     string   `json:"view,omitempty"`
}

type JiraBoardRow struct {
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

// DecodeJiraBoardSnapshot strictly decodes and independently reconciles one
// bounded released jira_board_view result.
func DecodeJiraBoardSnapshot(r io.Reader) (JiraBoardSnapshot, error) {
	limited := &io.LimitedReader{R: r, N: maxContractBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return JiraBoardSnapshot{}, fmt.Errorf("read jira board snapshot wire: %w", err)
	}
	if limited.N <= 0 {
		return JiraBoardSnapshot{}, fmt.Errorf("jira board snapshot wire exceeds %d bytes", maxContractBytes)
	}
	if !utf8.Valid(data) {
		return JiraBoardSnapshot{}, fmt.Errorf("decode jira board snapshot wire: wire is not valid UTF-8")
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return JiraBoardSnapshot{}, fmt.Errorf("decode jira board snapshot wire: %w", err)
	}
	if err := validateJiraBoardMembers(data); err != nil {
		return JiraBoardSnapshot{}, fmt.Errorf("decode jira board snapshot wire: %w", err)
	}
	var snapshot JiraBoardSnapshot
	if err := decodeStrict(bytes.NewReader(data), &snapshot); err != nil {
		return JiraBoardSnapshot{}, fmt.Errorf("decode jira board snapshot wire: %w", err)
	}
	if err := snapshot.validate(); err != nil {
		return JiraBoardSnapshot{}, fmt.Errorf("validate jira board snapshot: %w", err)
	}
	return snapshot, nil
}

func validateJiraBoardMembers(data []byte) error {
	root, err := jiraBoardObject(data, "snapshot")
	if err != nil {
		return err
	}
	if err := jiraBoardMembers(root, "snapshot", []string{
		"schema_version", "board", "scope", "projection", "rows", "row_count",
		"complete", "truncated", "backlog_fetched",
	}, nil); err != nil {
		return err
	}
	board, err := jiraBoardNestedObject(root["board"], "snapshot.board")
	if err != nil {
		return err
	}
	if err := jiraBoardMembers(board, "snapshot.board", []string{"id", "name", "type", "columns"}, []string{
		"filter_id", "kanban_subquery", "constraint_type", "estimation_type", "estimation_field", "rank_field_id",
	}); err != nil {
		return err
	}
	for _, name := range []string{"filter_id", "kanban_subquery", "constraint_type", "estimation_type", "estimation_field", "rank_field_id"} {
		if err := jiraBoardOptionalNonemptyString(board, "snapshot.board", name); err != nil {
			return err
		}
	}
	if err := jiraBoardArray(board["columns"], "snapshot.board.columns", validateJiraBoardColumnMembers); err != nil {
		return err
	}

	projection, err := jiraBoardNestedObject(root["projection"], "snapshot.projection")
	if err != nil {
		return err
	}
	if err := jiraBoardMembers(projection, "snapshot.projection", []string{"kind", "columns", "fields", "ordering"}, []string{"view"}); err != nil {
		return err
	}
	if err := jiraBoardOptionalNonemptyString(projection, "snapshot.projection", "view"); err != nil {
		return err
	}
	if err := jiraBoardArray(projection["columns"], "snapshot.projection.columns", nil); err != nil {
		return err
	}
	if err := jiraBoardArray(projection["fields"], "snapshot.projection.fields", nil); err != nil {
		return err
	}
	return jiraBoardArray(root["rows"], "snapshot.rows", validateJiraBoardRowMembers)
}

func validateJiraBoardColumnMembers(column map[string]json.RawMessage, owner string) error {
	if err := jiraBoardMembers(column, owner, []string{"name", "status_ids"}, []string{"min", "max"}); err != nil {
		return err
	}
	return jiraBoardArray(column["status_ids"], owner+".status_ids", nil)
}

func validateJiraBoardRowMembers(row map[string]json.RawMessage, owner string) error {
	if err := jiraBoardMembers(row, owner, []string{
		"key", "position", "in_board", "in_backlog", "status", "column",
		"column_index", "column_mapped", "values",
	}, []string{"id", "board_position", "backlog_position", "status_id"}); err != nil {
		return err
	}
	for _, name := range []string{"id", "status_id"} {
		if err := jiraBoardOptionalNonemptyString(row, owner, name); err != nil {
			return err
		}
	}
	_, err := jiraBoardNestedObject(row["values"], owner+".values")
	return err
}

func jiraBoardObject(data []byte, owner string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func jiraBoardNestedObject(raw json.RawMessage, owner string) (map[string]json.RawMessage, error) {
	return jiraBoardObject(raw, owner)
}

func jiraBoardMembers(object map[string]json.RawMessage, owner string, required, optional []string) error {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
		raw, ok := object[name]
		if !ok {
			return fmt.Errorf("%s.%s is required", owner, name)
		}
		if jiraBoardNull(raw) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	for _, name := range optional {
		allowed[name] = true
		if raw, ok := object[name]; ok && jiraBoardNull(raw) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	for name := range object {
		if !allowed[name] {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func jiraBoardArray(raw json.RawMessage, owner string, validate func(map[string]json.RawMessage, string) error) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return fmt.Errorf("%s must be a non-null array", owner)
	}
	for index, rawValue := range values {
		itemOwner := fmt.Sprintf("%s[%d]", owner, index)
		if jiraBoardNull(rawValue) {
			return fmt.Errorf("%s must not be null", itemOwner)
		}
		if validate != nil {
			item, err := jiraBoardNestedObject(rawValue, itemOwner)
			if err != nil {
				return err
			}
			if err := validate(item, itemOwner); err != nil {
				return err
			}
		}
	}
	return nil
}

func jiraBoardOptionalNonemptyString(object map[string]json.RawMessage, owner, name string) error {
	raw, ok := object[name]
	if !ok {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s.%s must be a string", owner, name)
	}
	if !jiraBoardNonempty(value) {
		return fmt.Errorf("%s.%s must be omitted when empty or invalid", owner, name)
	}
	return nil
}

func jiraBoardNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (s JiraBoardSnapshot) validate() error {
	if s.SchemaVersion != JiraBoardWireSchemaVersion {
		return fmt.Errorf("schema_version must be %d", JiraBoardWireSchemaVersion)
	}
	statusColumns, err := s.Board.validate()
	if err != nil {
		return err
	}
	if s.Scope != "all" && s.Scope != "board" && s.Scope != "backlog" {
		return fmt.Errorf("scope %q is unsupported", s.Scope)
	}
	if s.Board.Type == "kanban" && s.Scope == "backlog" {
		return fmt.Errorf("kanban snapshot cannot have backlog scope")
	}
	wantBacklog := s.Board.Type == "scrum" && (s.Scope == "all" || s.Scope == "backlog")
	if s.BacklogFetched != wantBacklog {
		return fmt.Errorf("backlog_fetched is not reconciled with board type and scope")
	}
	fields, err := s.Projection.validate()
	if err != nil {
		return err
	}
	if s.Rows == nil {
		return fmt.Errorf("rows must be an array")
	}
	if s.RowCount != len(s.Rows) {
		return fmt.Errorf("row_count is not reconciled with rows")
	}
	if s.Complete == s.Truncated {
		return fmt.Errorf("complete and truncated flags are contradictory")
	}
	return s.validateRows(fields, statusColumns)
}

func (b JiraBoardConfig) validate() (map[string]int, error) {
	if b.ID <= 0 {
		return nil, fmt.Errorf("board.id must be positive")
	}
	if !jiraBoardNonempty(b.Name) {
		return nil, fmt.Errorf("board.name must be non-empty UTF-8")
	}
	if b.Type != "scrum" && b.Type != "kanban" {
		return nil, fmt.Errorf("board.type %q is unsupported", b.Type)
	}
	if b.Columns == nil {
		return nil, fmt.Errorf("board.columns must be an array")
	}
	seenNames := make(map[string]bool, len(b.Columns))
	statusColumns := make(map[string]int)
	for index, column := range b.Columns {
		if !jiraBoardNonempty(column.Name) {
			return nil, fmt.Errorf("board.columns[%d].name must be non-empty UTF-8", index)
		}
		if seenNames[column.Name] {
			return nil, fmt.Errorf("board.columns[%d].name duplicates an earlier column", index)
		}
		seenNames[column.Name] = true
		if column.StatusIDs == nil {
			return nil, fmt.Errorf("board.columns[%d].status_ids must be an array", index)
		}
		for statusIndex, statusID := range column.StatusIDs {
			if !jiraBoardIdentity(statusID) {
				return nil, fmt.Errorf("board.columns[%d].status_ids[%d] is invalid", index, statusIndex)
			}
			if _, duplicate := statusColumns[statusID]; duplicate {
				return nil, fmt.Errorf("board status id %q is assigned more than once", statusID)
			}
			statusColumns[statusID] = index
		}
		if column.Min != nil && *column.Min < 0 || column.Max != nil && *column.Max < 0 {
			return nil, fmt.Errorf("board.columns[%d] has a negative constraint", index)
		}
		if column.Min != nil && column.Max != nil && *column.Min > *column.Max {
			return nil, fmt.Errorf("board.columns[%d] has contradictory constraints", index)
		}
	}
	return statusColumns, nil
}

func (p JiraBoardProjection) validate() ([]string, error) {
	if p.Kind != "jira-fields-v1" || p.Ordering != "backend-rank" {
		return nil, fmt.Errorf("projection kind or ordering is unsupported")
	}
	if len(p.Columns) == 0 || p.Fields == nil {
		return nil, fmt.Errorf("projection arrays are invalid")
	}
	if p.View != "" && (!utf8.ValidString(p.View) || !jiraSnapshotViewName.MatchString(p.View)) {
		return nil, fmt.Errorf("projection.view %q is invalid", p.View)
	}
	allowedContext := map[string]bool{
		"board.column": true, "board.column_index": true, "board.column_mapped": true,
		"board.in_board": true, "board.in_backlog": true,
	}
	seenColumns := make(map[string]bool, len(p.Columns))
	seenFields := make(map[string]bool, len(p.Fields))
	fields := make([]string, 0, len(p.Columns))
	for index, column := range p.Columns {
		if !jiraBoardIdentity(column) {
			return nil, fmt.Errorf("projection.columns[%d] is invalid", index)
		}
		if seenColumns[column] {
			return nil, fmt.Errorf("projection.columns[%d] duplicates an earlier column", index)
		}
		seenColumns[column] = true
		switch column {
		case "position", "key", "id":
			continue
		}
		if strings.Contains(column, ".") {
			if !allowedContext[column] {
				return nil, fmt.Errorf("projection.columns[%d] is not a board column", index)
			}
			continue
		}
		if seenFields[column] {
			return nil, fmt.Errorf("projection field %q is duplicated", column)
		}
		seenFields[column] = true
		fields = append(fields, column)
	}
	if !slices.Equal(p.Fields, fields) {
		return nil, fmt.Errorf("projection.fields are not reconciled with columns")
	}
	return fields, nil
}

func (s JiraBoardSnapshot) validateRows(fields []string, statusColumns map[string]int) error {
	seenKeys := make(map[string]bool, len(s.Rows))
	seenIDs := make(map[string]bool, len(s.Rows))
	boardPositions := make(map[int]bool)
	backlogPositions := make(map[int]bool)
	boardCount := 0
	lastBacklogOnlyPosition := -1
	seenBacklogOnly := false
	for index, row := range s.Rows {
		if !jiraBoardIdentity(row.Key) {
			return fmt.Errorf("rows[%d].key is invalid", index)
		}
		if seenKeys[row.Key] {
			return fmt.Errorf("rows[%d].key duplicates an earlier row", index)
		}
		seenKeys[row.Key] = true
		if row.ID != "" {
			if !jiraBoardIdentity(row.ID) || seenIDs[row.ID] {
				return fmt.Errorf("rows[%d].id is invalid or duplicated", index)
			}
			seenIDs[row.ID] = true
		}
		if row.Position != index {
			return fmt.Errorf("rows[%d].position is not reconciled with row order", index)
		}
		if row.InBoard != (row.BoardPosition != nil) || row.InBacklog != (row.BacklogPosition != nil) || !row.InBoard && !row.InBacklog {
			return fmt.Errorf("rows[%d] membership and positions are contradictory", index)
		}
		if s.Scope == "board" && (!row.InBoard || row.InBacklog) || s.Scope == "backlog" && (row.InBoard || !row.InBacklog) || !s.BacklogFetched && row.InBacklog {
			return fmt.Errorf("rows[%d] membership is outside snapshot scope", index)
		}
		if row.BoardPosition != nil {
			if *row.BoardPosition < 0 || boardPositions[*row.BoardPosition] || seenBacklogOnly || *row.BoardPosition != index {
				return fmt.Errorf("rows[%d].board_position is invalid", index)
			}
			boardPositions[*row.BoardPosition] = true
			boardCount++
		} else {
			seenBacklogOnly = true
		}
		if row.BacklogPosition != nil {
			position := *row.BacklogPosition
			if position < 0 || backlogPositions[position] {
				return fmt.Errorf("rows[%d].backlog_position is invalid", index)
			}
			backlogPositions[position] = true
			if !row.InBoard {
				if position <= lastBacklogOnlyPosition {
					return fmt.Errorf("rows[%d].backlog_position breaks backlog-only order", index)
				}
				lastBacklogOnlyPosition = position
			}
		}
		if !jiraBoardNonempty(row.Status) || !jiraBoardNonempty(row.Column) {
			return fmt.Errorf("rows[%d] status or column is invalid", index)
		}
		mappedIndex, mapped := statusColumns[row.StatusID]
		if row.ColumnMapped != mapped {
			return fmt.Errorf("rows[%d].column_mapped is not reconciled with status_id", index)
		}
		if mapped {
			if row.ColumnIndex != mappedIndex || row.Column != s.Board.Columns[mappedIndex].Name {
				return fmt.Errorf("rows[%d] mapped column is inconsistent with board configuration", index)
			}
		} else if row.ColumnIndex != -1 || row.Column != "Unmapped" {
			return fmt.Errorf("rows[%d] unmapped column is inconsistent", index)
		}
		if row.Values == nil || len(row.Values) != len(fields) {
			return fmt.Errorf("rows[%d].values are not reconciled with projection.fields", index)
		}
		for _, field := range fields {
			if _, ok := row.Values[field]; !ok {
				return fmt.Errorf("rows[%d].values omit projected field %q", index, field)
			}
		}
		if rawStatus, ok := row.Values["status"]; ok {
			status, stringStatus := rawStatus.(string)
			if !stringStatus || status != row.Status {
				return fmt.Errorf("rows[%d].values status is not reconciled with row status", index)
			}
		}
	}
	for position := 0; position < boardCount; position++ {
		if !boardPositions[position] {
			return fmt.Errorf("board positions are not contiguous")
		}
	}
	for position := 0; position < len(backlogPositions); position++ {
		if !backlogPositions[position] {
			return fmt.Errorf("backlog positions are not contiguous")
		}
	}
	return nil
}

func jiraBoardNonempty(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != ""
}

func jiraBoardIdentity(value string) bool {
	return jiraBoardNonempty(value) && strings.TrimSpace(value) == value
}
