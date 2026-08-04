package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

const (
	// JiraSnapshotWireSchemaVersion is the released issue-list and exact-field
	// evidence schema shared by the two Jira MCP tools used for reconciliation.
	JiraSnapshotWireSchemaVersion = 1

	jiraSnapshotFieldMinValueBytes = 256
	jiraSnapshotFieldMaxValueBytes = 128 << 10
)

var jiraSnapshotViewName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// JiraSnapshotIssueListSource identifies the released JQL search source.
type JiraSnapshotIssueListSource struct {
	Kind string `json:"kind"`
}

// JiraSnapshotIssueListProjection describes the normalized ordered search
// projection independently from the arbitrary Jira values in each row.
type JiraSnapshotIssueListProjection struct {
	Columns  []string `json:"columns"`
	Fields   []string `json:"fields"`
	Ordering string   `json:"ordering"`
	View     string   `json:"view"`
}

// JiraSnapshotIssueListRow is one released JQL search row. Values deliberately
// remains open JSON because Jira field values are backend- and plugin-defined.
type JiraSnapshotIssueListRow struct {
	Key      string         `json:"key"`
	ID       string         `json:"id,omitempty"`
	Position int            `json:"position"`
	Values   map[string]any `json:"values"`
}

// JiraSnapshotIssueListPage qualifies one bounded search page.
type JiraSnapshotIssueListPage struct {
	Count         int     `json:"count"`
	Complete      bool    `json:"complete"`
	Truncated     bool    `json:"truncated"`
	PartialReason string  `json:"partial_reason,omitempty"`
	NextCursor    *string `json:"next_cursor"`
}

// JiraSnapshotIssueList is the evaluator-owned released jira_issue_search
// wire. It duplicates the public JSON shape without importing product code.
type JiraSnapshotIssueList struct {
	SchemaVersion int                             `json:"schema_version"`
	Source        JiraSnapshotIssueListSource     `json:"source"`
	Selection     map[string]any                  `json:"selection"`
	Projection    JiraSnapshotIssueListProjection `json:"projection"`
	Rows          []JiraSnapshotIssueListRow      `json:"rows"`
	Page          JiraSnapshotIssueListPage       `json:"page"`
}

// JiraSnapshotFieldIssue carries exact issue identity and snapshot provenance.
type JiraSnapshotFieldIssue struct {
	ID      string `json:"id,omitempty"`
	Key     string `json:"key"`
	Updated string `json:"updated"`
}

// JiraSnapshotField describes the resolved exact Jira field.
type JiraSnapshotField struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Custom    bool   `json:"custom"`
	Schema    string `json:"schema,omitempty"`
	Present   bool   `json:"present"`
	Empty     bool   `json:"empty"`
	ValueType string `json:"value_type"`
}

// JiraSnapshotFieldEvidence is the evaluator-owned released
// jira_issue_field_get wire. Value deliberately remains arbitrary JSON.
type JiraSnapshotFieldEvidence struct {
	SchemaVersion      int                    `json:"schema_version"`
	Issue              JiraSnapshotFieldIssue `json:"issue"`
	Field              JiraSnapshotField      `json:"field"`
	Projection         string                 `json:"projection"`
	MaxValueBytes      int                    `json:"max_value_bytes"`
	OriginalValueBytes int                    `json:"original_value_bytes"`
	EmittedValueBytes  int                    `json:"emitted_value_bytes"`
	Complete           bool                   `json:"complete"`
	Truncated          bool                   `json:"truncated"`
	Value              any                    `json:"value"`
}

// DecodeJiraSnapshotIssueList strictly decodes and reconciles one released
// jira_issue_search result.
func DecodeJiraSnapshotIssueList(r io.Reader) (JiraSnapshotIssueList, error) {
	var result JiraSnapshotIssueList
	if err := decodeJiraSnapshotWire(r, &result, validateJiraSnapshotIssueListMembers, "issue list"); err != nil {
		return JiraSnapshotIssueList{}, err
	}
	if err := result.validate(); err != nil {
		return JiraSnapshotIssueList{}, fmt.Errorf("validate jira snapshot issue list: %w", err)
	}
	return result, nil
}

// DecodeJiraSnapshotFieldEvidence strictly decodes and reconciles one released
// jira_issue_field_get result.
func DecodeJiraSnapshotFieldEvidence(r io.Reader) (JiraSnapshotFieldEvidence, error) {
	var result JiraSnapshotFieldEvidence
	if err := decodeJiraSnapshotWire(r, &result, validateJiraSnapshotFieldMembers, "field evidence"); err != nil {
		return JiraSnapshotFieldEvidence{}, err
	}
	if err := result.validate(); err != nil {
		return JiraSnapshotFieldEvidence{}, fmt.Errorf("validate jira snapshot field evidence: %w", err)
	}
	return result, nil
}

func decodeJiraSnapshotWire(r io.Reader, dst any, validateMembers func([]byte) error, subject string) error {
	limited := &io.LimitedReader{R: r, N: maxContractBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read jira snapshot %s wire: %w", subject, err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("jira snapshot %s wire exceeds %d bytes", subject, maxContractBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return fmt.Errorf("decode jira snapshot %s wire: %w", subject, err)
	}
	if err := validateMembers(data); err != nil {
		return fmt.Errorf("decode jira snapshot %s wire: %w", subject, err)
	}
	if err := decodeStrict(bytes.NewReader(data), dst); err != nil {
		return fmt.Errorf("decode jira snapshot %s wire: %w", subject, err)
	}
	return nil
}

func validateJiraSnapshotIssueListMembers(data []byte) error {
	root, err := jiraSnapshotObject(data, "issue list")
	if err != nil {
		return err
	}
	if err := requireJiraSnapshotMembers(root, "issue list",
		[]string{"schema_version", "source", "selection", "projection", "rows", "page"}, nil); err != nil {
		return err
	}
	if err := requireJiraSnapshotNonNullMembers(root, "issue list",
		[]string{"schema_version", "source", "selection", "projection", "rows", "page"}); err != nil {
		return err
	}

	source, err := jiraSnapshotNestedObject(root["source"], "issue list.source")
	if err != nil {
		return err
	}
	if err := requireJiraSnapshotMembers(source, "issue list.source", []string{"kind"}, nil); err != nil {
		return err
	}
	if err := requireJiraSnapshotNonNullMembers(source, "issue list.source", []string{"kind"}); err != nil {
		return err
	}

	selection, err := jiraSnapshotNestedObject(root["selection"], "issue list.selection")
	if err != nil {
		return err
	}
	if err := requireJiraSnapshotMembers(selection, "issue list.selection", []string{"jql"}, nil); err != nil {
		return err
	}
	if err := requireJiraSnapshotNonNullMembers(selection, "issue list.selection", []string{"jql"}); err != nil {
		return err
	}

	projection, err := jiraSnapshotNestedObject(root["projection"], "issue list.projection")
	if err != nil {
		return err
	}
	if err := requireJiraSnapshotMembers(projection, "issue list.projection",
		[]string{"columns", "fields", "ordering", "view"}, nil); err != nil {
		return err
	}
	if err := requireJiraSnapshotNonNullMembers(projection, "issue list.projection",
		[]string{"columns", "fields", "ordering", "view"}); err != nil {
		return err
	}

	if jiraSnapshotNull(root["rows"]) {
		return fmt.Errorf("issue list.rows must not be null")
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(root["rows"], &rows); err != nil {
		return fmt.Errorf("issue list.rows: %w", err)
	}
	for index, raw := range rows {
		owner := fmt.Sprintf("issue list.rows[%d]", index)
		row, err := jiraSnapshotNestedObject(raw, owner)
		if err != nil {
			return err
		}
		if err := requireJiraSnapshotMembers(row, owner,
			[]string{"key", "position", "values"}, []string{"id"}); err != nil {
			return err
		}
		if err := requireJiraSnapshotNonNullMembers(row, owner,
			[]string{"key", "position", "values", "id"}); err != nil {
			return err
		}
		if err := requireJiraSnapshotNonemptyOptionalString(row, owner, "id"); err != nil {
			return err
		}
		if _, err := jiraSnapshotNestedObject(row["values"], owner+".values"); err != nil {
			return err
		}
	}

	page, err := jiraSnapshotNestedObject(root["page"], "issue list.page")
	if err != nil {
		return err
	}
	if err := requireJiraSnapshotMembers(page, "issue list.page",
		[]string{"count", "complete", "truncated", "next_cursor"}, []string{"partial_reason"}); err != nil {
		return err
	}
	if err := requireJiraSnapshotNonNullMembers(page, "issue list.page",
		[]string{"count", "complete", "truncated", "partial_reason"}); err != nil {
		return err
	}
	return requireJiraSnapshotNonemptyOptionalString(page, "issue list.page", "partial_reason")
}

func validateJiraSnapshotFieldMembers(data []byte) error {
	root, err := jiraSnapshotObject(data, "field evidence")
	if err != nil {
		return err
	}
	rootMembers := []string{
		"schema_version", "issue", "field", "projection", "max_value_bytes",
		"original_value_bytes", "emitted_value_bytes", "complete", "truncated", "value",
	}
	if err := requireJiraSnapshotMembers(root, "field evidence", rootMembers, nil); err != nil {
		return err
	}
	if err := requireJiraSnapshotNonNullMembers(root, "field evidence", rootMembers[:len(rootMembers)-1]); err != nil {
		return err
	}

	issue, err := jiraSnapshotNestedObject(root["issue"], "field evidence.issue")
	if err != nil {
		return err
	}
	if err := requireJiraSnapshotMembers(issue, "field evidence.issue", []string{"key", "updated"}, []string{"id"}); err != nil {
		return err
	}
	if err := requireJiraSnapshotNonNullMembers(issue, "field evidence.issue", []string{"id", "key", "updated"}); err != nil {
		return err
	}
	if err := requireJiraSnapshotNonemptyOptionalString(issue, "field evidence.issue", "id"); err != nil {
		return err
	}

	field, err := jiraSnapshotNestedObject(root["field"], "field evidence.field")
	if err != nil {
		return err
	}
	if err := requireJiraSnapshotMembers(field, "field evidence.field",
		[]string{"id", "name", "custom", "present", "empty", "value_type"}, []string{"schema"}); err != nil {
		return err
	}
	if err := requireJiraSnapshotNonNullMembers(field, "field evidence.field",
		[]string{"id", "name", "custom", "schema", "present", "empty", "value_type"}); err != nil {
		return err
	}
	return requireJiraSnapshotNonemptyOptionalString(field, "field evidence.field", "schema")
}

func jiraSnapshotObject(data []byte, owner string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("%s: %w", owner, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func jiraSnapshotNestedObject(raw json.RawMessage, owner string) (map[string]json.RawMessage, error) {
	if jiraSnapshotNull(raw) {
		return nil, fmt.Errorf("%s must not be null", owner)
	}
	return jiraSnapshotObject(raw, owner)
}

func requireJiraSnapshotMembers(document map[string]json.RawMessage, owner string, required, optional []string) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = struct{}{}
		if _, ok := document[name]; !ok {
			return fmt.Errorf("%s is missing required member %q", owner, name)
		}
	}
	for _, name := range optional {
		allowed[name] = struct{}{}
	}
	for name := range document {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func requireJiraSnapshotNonNullMembers(document map[string]json.RawMessage, owner string, members []string) error {
	for _, name := range members {
		raw, ok := document[name]
		if ok && jiraSnapshotNull(raw) {
			return fmt.Errorf("%s.%s must not be null", owner, name)
		}
	}
	return nil
}

func requireJiraSnapshotNonemptyOptionalString(document map[string]json.RawMessage, owner, name string) error {
	raw, ok := document[name]
	if !ok {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s.%s: %w", owner, name, err)
	}
	if value == "" {
		return fmt.Errorf("%s.%s must be omitted when empty", owner, name)
	}
	return nil
}

func jiraSnapshotNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (r JiraSnapshotIssueList) validate() error {
	if r.SchemaVersion != JiraSnapshotWireSchemaVersion {
		return fmt.Errorf("schema_version must be %d", JiraSnapshotWireSchemaVersion)
	}
	if r.Source.Kind != "jql" {
		return fmt.Errorf("source.kind %q is unsupported", r.Source.Kind)
	}
	jql, ok := r.Selection["jql"].(string)
	if !ok || strings.TrimSpace(jql) == "" {
		return fmt.Errorf("selection.jql must be a non-empty string")
	}
	if r.Projection.Ordering != "jql-order" {
		return fmt.Errorf("projection.ordering %q is unsupported", r.Projection.Ordering)
	}
	if !jiraSnapshotViewName.MatchString(r.Projection.View) && r.Projection.View != "explicit" {
		return fmt.Errorf("projection.view %q is invalid", r.Projection.View)
	}
	fields, err := validateJiraSnapshotProjection(r.Projection.Columns)
	if err != nil {
		return err
	}
	if r.Projection.Fields == nil || !slices.Equal(r.Projection.Fields, fields) {
		return fmt.Errorf("projection.fields are not reconciled with columns")
	}

	if r.Rows == nil {
		return fmt.Errorf("rows must be an array")
	}
	seenKeys := make(map[string]struct{}, len(r.Rows))
	seenIDs := make(map[string]struct{}, len(r.Rows))
	for index, row := range r.Rows {
		if strings.TrimSpace(row.Key) == "" || strings.TrimSpace(row.Key) != row.Key {
			return fmt.Errorf("row[%d].key must be non-empty and whitespace-normalized", index)
		}
		if _, duplicate := seenKeys[row.Key]; duplicate {
			return fmt.Errorf("row[%d].key duplicates an earlier row", index)
		}
		seenKeys[row.Key] = struct{}{}
		if row.ID != "" {
			if strings.TrimSpace(row.ID) != row.ID {
				return fmt.Errorf("row[%d].id must be whitespace-normalized", index)
			}
			if _, duplicate := seenIDs[row.ID]; duplicate {
				return fmt.Errorf("row[%d].id duplicates an earlier row", index)
			}
			seenIDs[row.ID] = struct{}{}
		}
		if row.Position != index {
			return fmt.Errorf("row[%d].position is not reconciled with row order", index)
		}
		if row.Values == nil || len(row.Values) != len(fields) {
			return fmt.Errorf("row[%d].values are not reconciled with projection.fields", index)
		}
		for _, field := range fields {
			if _, present := row.Values[field]; !present {
				return fmt.Errorf("row[%d].values omit projected field %q", index, field)
			}
		}
	}

	if r.Page.Count != len(r.Rows) {
		return fmt.Errorf("page.count is not reconciled with rows")
	}
	if r.Page.Complete == r.Page.Truncated {
		return fmt.Errorf("page complete and truncated flags are contradictory")
	}
	if r.Page.Complete {
		if r.Page.NextCursor != nil || r.Page.PartialReason != "" {
			return fmt.Errorf("complete page carries continuation or partial reason")
		}
		return nil
	}
	if r.Page.NextCursor != nil {
		if strings.TrimSpace(*r.Page.NextCursor) == "" || strings.TrimSpace(*r.Page.NextCursor) != *r.Page.NextCursor {
			return fmt.Errorf("page.next_cursor must be non-empty and whitespace-normalized")
		}
		if r.Page.PartialReason != "" || len(r.Rows) == 0 {
			return fmt.Errorf("resumable page carries contradictory qualification")
		}
		return nil
	}
	if !jiraSnapshotValidSearchPartialReason(r.Page.PartialReason) {
		return fmt.Errorf("terminal incomplete page has invalid partial_reason %q", r.Page.PartialReason)
	}
	return nil
}

func validateJiraSnapshotProjection(columns []string) ([]string, error) {
	if len(columns) == 0 {
		return nil, fmt.Errorf("projection.columns must not be empty")
	}
	seenColumns := make(map[string]struct{}, len(columns))
	seenFields := make(map[string]struct{}, len(columns))
	fields := make([]string, 0, len(columns))
	for index, column := range columns {
		if strings.TrimSpace(column) == "" || strings.TrimSpace(column) != column || strings.Contains(column, ".") {
			return nil, fmt.Errorf("projection.columns[%d] is not a normalized search column", index)
		}
		if _, duplicate := seenColumns[column]; duplicate {
			return nil, fmt.Errorf("projection.columns[%d] duplicates an earlier column", index)
		}
		seenColumns[column] = struct{}{}
		switch column {
		case "position", "key", "id":
			continue
		}
		if _, duplicate := seenFields[column]; duplicate {
			return nil, fmt.Errorf("projection field %q is duplicated", column)
		}
		seenFields[column] = struct{}{}
		fields = append(fields, column)
	}
	return fields, nil
}

func jiraSnapshotValidSearchPartialReason(reason string) bool {
	switch reason {
	case "legacy_unqualified", "pagination_unqualified", "pagination_stalled":
		return true
	}
	return false
}

func (r JiraSnapshotFieldEvidence) validate() error {
	if r.SchemaVersion != JiraSnapshotWireSchemaVersion {
		return fmt.Errorf("schema_version must be %d", JiraSnapshotWireSchemaVersion)
	}
	if strings.TrimSpace(r.Issue.Key) == "" || strings.TrimSpace(r.Issue.Key) != r.Issue.Key {
		return fmt.Errorf("issue.key must be non-empty and whitespace-normalized")
	}
	if r.Issue.ID != "" && strings.TrimSpace(r.Issue.ID) != r.Issue.ID {
		return fmt.Errorf("issue.id must be whitespace-normalized")
	}
	if strings.TrimSpace(r.Issue.Updated) == "" || strings.TrimSpace(r.Issue.Updated) != r.Issue.Updated {
		return fmt.Errorf("issue.updated must be non-empty and whitespace-normalized")
	}
	if strings.TrimSpace(r.Field.ID) == "" || strings.TrimSpace(r.Field.ID) != r.Field.ID {
		return fmt.Errorf("field.id must be non-empty and whitespace-normalized")
	}
	if strings.TrimSpace(r.Field.Name) == "" || strings.TrimSpace(r.Field.Name) != r.Field.Name {
		return fmt.Errorf("field.name must be non-empty and whitespace-normalized")
	}
	if r.Field.Schema != "" && (strings.TrimSpace(r.Field.Schema) == "" || strings.TrimSpace(r.Field.Schema) != r.Field.Schema) {
		return fmt.Errorf("field.schema must be non-empty and whitespace-normalized when present")
	}
	if !jiraSnapshotValidValueType(r.Field.ValueType) {
		return fmt.Errorf("field.value_type %q is unsupported", r.Field.ValueType)
	}
	if !r.Field.Present && (!r.Field.Empty || r.Field.ValueType != "null" || r.Value != nil) {
		return fmt.Errorf("absent field metadata is not reconciled with value")
	}
	if r.Field.ValueType == "null" && (!r.Field.Empty || r.Value != nil) {
		return fmt.Errorf("null field metadata is not reconciled with value")
	}
	if r.Projection != "compact" {
		return fmt.Errorf("projection %q is unsupported", r.Projection)
	}
	if r.MaxValueBytes < jiraSnapshotFieldMinValueBytes || r.MaxValueBytes > jiraSnapshotFieldMaxValueBytes {
		return fmt.Errorf("max_value_bytes is outside the released bound")
	}
	if r.OriginalValueBytes <= 0 || r.EmittedValueBytes <= 0 || r.EmittedValueBytes > r.MaxValueBytes {
		return fmt.Errorf("value byte accounting is outside the released bound")
	}
	encoded, err := json.Marshal(r.Value)
	if err != nil {
		return fmt.Errorf("encode projected value: %w", err)
	}
	if len(encoded) != r.EmittedValueBytes {
		return fmt.Errorf("emitted_value_bytes is not reconciled with value")
	}
	if r.Complete == r.Truncated {
		return fmt.Errorf("complete and truncated flags are contradictory")
	}
	return nil
}

func jiraSnapshotValidValueType(value string) bool {
	switch value {
	case "null", "string", "boolean", "number", "list", "object", "unknown":
		return true
	}
	return false
}
