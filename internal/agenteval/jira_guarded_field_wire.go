package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	jiraGuardedFieldWireMaxBytes          = 32 << 10
	jiraGuardedFieldResultMaxNestingDepth = 10_000
	jiraGuardedFieldValueMaxNestingDepth  = jiraGuardedFieldResultMaxNestingDepth - 3
)

var jiraGuardedFieldKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`)
var jiraGuardedFieldNumericIDRE = regexp.MustCompile(`^[1-9][0-9]*$`)

// JiraGuardedFieldResult is the content-free evaluator projection of the
// released guarded custom-field result. Raw desired values are decoded and
// validated internally, but never escape this decoder.
type JiraGuardedFieldResult struct {
	ProposalHash   string
	Mode           string
	Status         string
	RequestedKey   string
	IssueID        string
	Key            string
	Project        string
	WriteAttempted bool
	Reconciled     bool
	Complete       bool
}

type jiraGuardedFieldWire struct {
	SchemaVersion   int                              `json:"schema_version"`
	Operation       string                           `json:"operation"`
	BackendSHA256   string                           `json:"backend_sha256"`
	RequestedKey    string                           `json:"requested_key"`
	IssueID         string                           `json:"issue_id"`
	Key             string                           `json:"key"`
	Project         string                           `json:"project"`
	Mode            string                           `json:"mode"`
	Status          string                           `json:"status"`
	ExpectedUpdated string                           `json:"expected_updated"`
	ActualUpdated   string                           `json:"actual_updated"`
	ProposalHash    string                           `json:"proposal_hash"`
	Catalog         []jiraGuardedFieldCatalogWire    `json:"catalog"`
	Current         []jiraGuardedFieldProjectionWire `json:"current"`
	Readback        []jiraGuardedFieldProjectionWire `json:"readback,omitempty"`
	Prepared        jiraGuardedFieldPreparedWire     `json:"prepared"`
	Bounds          jiraGuardedFieldBoundsWire       `json:"bounds"`
	Usage           jiraGuardedFieldUsageWire        `json:"usage"`
	WriteAttempted  bool                             `json:"write_attempted"`
	Reconciled      bool                             `json:"reconciled"`
	Complete        bool                             `json:"complete"`
	Fields          []jiraGuardedFieldDesiredWire    `json:"fields"`
}

type jiraGuardedFieldCatalogWire struct {
	ID     string `json:"id"`
	Custom bool   `json:"custom"`
}
type jiraGuardedFieldProjectionWire struct {
	Field   string `json:"field"`
	Present bool   `json:"present"`
	Kind    string `json:"kind"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
}
type jiraGuardedFieldPreparedWire struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type jiraGuardedFieldDesiredWire struct {
	Field  string          `json:"field"`
	Source string          `json:"source"`
	Kind   string          `json:"kind"`
	Bytes  int             `json:"bytes"`
	SHA256 string          `json:"sha256"`
	Value  json.RawMessage `json:"value"`
}
type jiraGuardedFieldUsageWire struct {
	Requests              int   `json:"requests"`
	ResponseBytes         int64 `json:"response_bytes"`
	InputBytes            int   `json:"input_bytes"`
	DesiredCanonicalBytes int   `json:"desired_canonical_bytes"`
	CurrentCanonicalBytes int   `json:"current_canonical_bytes"`
}
type jiraGuardedFieldBoundsWire struct {
	MaxCatalogEntries           int   `json:"max_catalog_entries"`
	MaxSelectedFields           int   `json:"max_selected_fields"`
	MaxAllowlistEntries         int   `json:"max_allowlist_entries"`
	MaxFieldIDBytes             int   `json:"max_field_id_bytes"`
	MaxRequestedKeyBytes        int   `json:"max_requested_key_bytes"`
	MaxImmutableIDBytes         int   `json:"max_immutable_id_bytes"`
	MaxJSONNestingDepth         int   `json:"max_json_nesting_depth"`
	MaxValueNestingDepth        int   `json:"max_value_nesting_depth"`
	MaxCatalogResponseBytes     int64 `json:"max_catalog_response_bytes"`
	MaxIssueResponseBytes       int64 `json:"max_issue_response_bytes"`
	MaxInputBytes               int64 `json:"max_input_bytes"`
	MaxDesiredCanonicalBytes    int64 `json:"max_desired_canonical_bytes"`
	MaxCurrentCanonicalBytes    int64 `json:"max_current_canonical_bytes"`
	MaxPreparedBytes            int64 `json:"max_prepared_bytes"`
	MaxQueryAndPathBytes        int   `json:"max_query_and_path_bytes"`
	MaxWriteResponseBytes       int64 `json:"max_write_response_bytes"`
	PreviewMaxRequests          int   `json:"preview_max_requests"`
	ApplyMaxRequests            int   `json:"apply_max_requests"`
	PreviewMaxAggregateResponse int64 `json:"preview_max_aggregate_response_bytes"`
	ApplyMaxAggregateResponse   int64 `json:"apply_max_aggregate_response_bytes"`
	DeadlineMillis              int64 `json:"deadline_millis"`
}

func DecodeJiraGuardedFieldResult(r io.Reader) (JiraGuardedFieldResult, error) {
	var wire jiraGuardedFieldWire
	if err := decodeJiraGuardedFieldWire(r, &wire); err != nil {
		return JiraGuardedFieldResult{}, err
	}
	if err := validateJiraGuardedFieldWire(wire); err != nil {
		return JiraGuardedFieldResult{}, fmt.Errorf("validate Jira guarded field: %w", err)
	}
	return JiraGuardedFieldResult{ProposalHash: wire.ProposalHash, Mode: wire.Mode, Status: wire.Status,
		RequestedKey: wire.RequestedKey, IssueID: wire.IssueID, Key: wire.Key, Project: wire.Project,
		WriteAttempted: wire.WriteAttempted, Reconciled: wire.Reconciled, Complete: wire.Complete}, nil
}

func decodeJiraGuardedFieldWire(r io.Reader, wire *jiraGuardedFieldWire) error {
	limited := &io.LimitedReader{R: r, N: jiraGuardedFieldWireMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read Jira guarded field wire: %w", err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("jira guarded field wire exceeds %d bytes", jiraGuardedFieldWireMaxBytes)
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("decode Jira guarded field wire: wire is not valid UTF-8")
	}
	if err := validateJiraGuardedFieldJSON(data); err != nil {
		return fmt.Errorf("decode Jira guarded field wire: %w", err)
	}
	if err := validateJiraGuardedFieldMembers(data); err != nil {
		return fmt.Errorf("decode Jira guarded field wire: %w", err)
	}
	if err := decodeStrict(bytes.NewReader(data), wire); err != nil {
		return fmt.Errorf("decode Jira guarded field wire: %w", err)
	}
	return nil
}

type jiraGuardedFieldJSONFrame struct {
	kind      json.Delim
	expectKey bool
	seen      map[string]struct{}
}

// validateJiraGuardedFieldJSON is field-wire-specific: open desired values use
// exact-case JSON member identity and the full released-result depth envelope.
// Other evaluator wires retain the established case-folded 128-level policy.
func validateJiraGuardedFieldJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	stack := make([]jiraGuardedFieldJSONFrame, 0, 16)
	rootSeen := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if !rootSeen || len(stack) != 0 {
				return fmt.Errorf("JSON value is incomplete")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if len(stack) == 0 {
			if rootSeen {
				return fmt.Errorf("JSON contains trailing data")
			}
			rootSeen = true
		}
		delimiter, compound := token.(json.Delim)
		if compound && (delimiter == '}' || delimiter == ']') {
			if len(stack) == 0 || stack[len(stack)-1].kind+2 != delimiter {
				return fmt.Errorf("JSON compound delimiter is malformed")
			}
			if delimiter == '}' && !stack[len(stack)-1].expectKey {
				return fmt.Errorf("JSON object value is missing")
			}
			stack = stack[:len(stack)-1]
			continue
		}
		if len(stack) > 0 {
			parent := &stack[len(stack)-1]
			if parent.kind == '{' && parent.expectKey {
				name, ok := token.(string)
				if !ok {
					return fmt.Errorf("JSON object member name is malformed")
				}
				if _, duplicate := parent.seen[name]; duplicate {
					return fmt.Errorf("JSON object contains a duplicate member")
				}
				parent.seen[name] = struct{}{}
				parent.expectKey = false
				continue
			}
		}
		if compound {
			if len(stack)+1 > jiraGuardedFieldResultMaxNestingDepth {
				return fmt.Errorf("JSON nesting exceeds the guarded field result maximum")
			}
			switch delimiter {
			case '{':
				markJiraGuardedFieldParentValueComplete(stack)
				stack = append(stack, jiraGuardedFieldJSONFrame{kind: delimiter, expectKey: true, seen: map[string]struct{}{}})
			case '[':
				markJiraGuardedFieldParentValueComplete(stack)
				stack = append(stack, jiraGuardedFieldJSONFrame{kind: delimiter})
			default:
				return fmt.Errorf("JSON delimiter is malformed")
			}
			continue
		}
		markJiraGuardedFieldParentValueComplete(stack)
	}
}

func markJiraGuardedFieldParentValueComplete(stack []jiraGuardedFieldJSONFrame) {
	if len(stack) > 0 && stack[len(stack)-1].kind == '{' {
		stack[len(stack)-1].expectKey = true
	}
}

func validateJiraGuardedFieldMembers(data []byte) error {
	root, err := jiraWorkflowObject(data, "Jira guarded field")
	if err != nil {
		return err
	}
	required := []string{"schema_version", "operation", "backend_sha256", "requested_key", "issue_id", "key", "project", "mode", "status", "expected_updated", "actual_updated", "proposal_hash", "catalog", "current", "prepared", "bounds", "usage", "write_attempted", "reconciled", "complete", "fields"}
	if err := jiraWorkflowMembers(root, "Jira guarded field", required, []string{"readback"}); err != nil {
		return err
	}
	if err := jiraWorkflowArray(root["catalog"], "Jira guarded field.catalog", func(item map[string]json.RawMessage, owner string) error {
		return jiraWorkflowMembers(item, owner, []string{"id", "custom"}, nil)
	}); err != nil {
		return err
	}
	for _, member := range []string{"current", "readback"} {
		raw, exists := root[member]
		if !exists {
			continue
		}
		if err := jiraWorkflowArray(raw, "Jira guarded field."+member, func(item map[string]json.RawMessage, owner string) error {
			return jiraWorkflowMembers(item, owner, []string{"field", "present", "kind", "bytes", "sha256"}, nil)
		}); err != nil {
			return err
		}
	}
	if err := jiraWorkflowArray(root["fields"], "Jira guarded field.fields", func(item map[string]json.RawMessage, owner string) error {
		return jiraWorkflowMembers(item, owner, []string{"field", "source", "kind", "bytes", "sha256", "value"}, nil)
	}); err != nil {
		return err
	}
	nested := []struct {
		name    string
		members []string
	}{
		{"prepared", []string{"bytes", "sha256"}},
		{"usage", []string{"requests", "response_bytes", "input_bytes", "desired_canonical_bytes", "current_canonical_bytes"}},
		{"bounds", []string{"max_catalog_entries", "max_selected_fields", "max_allowlist_entries", "max_field_id_bytes", "max_requested_key_bytes", "max_immutable_id_bytes", "max_json_nesting_depth", "max_value_nesting_depth", "max_catalog_response_bytes", "max_issue_response_bytes", "max_input_bytes", "max_desired_canonical_bytes", "max_current_canonical_bytes", "max_prepared_bytes", "max_query_and_path_bytes", "max_write_response_bytes", "preview_max_requests", "apply_max_requests", "preview_max_aggregate_response_bytes", "apply_max_aggregate_response_bytes", "deadline_millis"}},
	}
	for _, item := range nested {
		object, err := jiraWorkflowNestedObject(root[item.name], "Jira guarded field."+item.name)
		if err != nil {
			return err
		}
		if err := jiraWorkflowMembers(object, "Jira guarded field."+item.name, item.members, nil); err != nil {
			return err
		}
	}
	return nil
}

func validateJiraGuardedFieldWire(w jiraGuardedFieldWire) error {
	if w.SchemaVersion != 3 || w.Operation != "jira_issue_field_set" || len(w.RequestedKey) > w.Bounds.MaxRequestedKeyBytes || !jiraGuardedFieldKeyRE.MatchString(w.RequestedKey) ||
		w.Key != w.RequestedKey || w.BackendSHA256 != "" && !validBackendSHA256(w.BackendSHA256) || w.ProposalHash != "" && !validSHA256(w.ProposalHash) {
		return fmt.Errorf("invalid guarded-field result identity")
	}
	wantBounds := jiraGuardedFieldBoundsWire{MaxCatalogEntries: 4096, MaxSelectedFields: 1024, MaxAllowlistEntries: 1024,
		MaxFieldIDBytes: 1024, MaxRequestedKeyBytes: 64, MaxImmutableIDBytes: 64, MaxJSONNestingDepth: jiraGuardedFieldResultMaxNestingDepth, MaxValueNestingDepth: jiraGuardedFieldValueMaxNestingDepth,
		MaxCatalogResponseBytes: 16 << 20, MaxIssueResponseBytes: 64 << 20, MaxInputBytes: 64 << 20,
		MaxDesiredCanonicalBytes: 64 << 20, MaxCurrentCanonicalBytes: 64 << 20, MaxPreparedBytes: 64 << 20,
		MaxQueryAndPathBytes: 64 << 10, MaxWriteResponseBytes: 1 << 20, PreviewMaxRequests: 2, ApplyMaxRequests: 6,
		PreviewMaxAggregateResponse: 80 << 20, ApplyMaxAggregateResponse: 225 << 20, DeadlineMillis: 60_000}
	if w.Bounds != wantBounds {
		return fmt.Errorf("guarded bounds do not match the released contract")
	}
	if !validJiraGuardedFieldTruth(w.Mode, w.Status, w.WriteAttempted, w.Reconciled, w.Complete) {
		return fmt.Errorf("contradictory terminal state")
	}
	qualified := w.ProposalHash != ""
	if !qualified {
		if w.Status != "blocked" || w.WriteAttempted || w.Reconciled || w.Complete || w.IssueID != "" || w.Project != "" ||
			w.ExpectedUpdated != "" || w.ActualUpdated != "" || len(w.Fields) != 0 || len(w.Catalog) != 0 || len(w.Current) != 0 || len(w.Readback) != 0 ||
			w.Prepared.Bytes != 0 || w.Prepared.SHA256 != "" || w.Usage.DesiredCanonicalBytes != 0 || w.Usage.CurrentCanonicalBytes != 0 {
			return fmt.Errorf("incomplete qualification contains contradictory proposal evidence")
		}
	} else if !validBackendSHA256(w.BackendSHA256) || len(w.IssueID) > w.Bounds.MaxImmutableIDBytes || !jiraGuardedFieldNumericIDRE.MatchString(w.IssueID) || !jiraWorkflowNormalized(w.Project) ||
		!strings.HasPrefix(w.Key, w.Project+"-") || !jiraWorkflowNormalized(w.ExpectedUpdated) || !jiraWorkflowNormalized(w.ActualUpdated) ||
		len(w.Fields) == 0 || len(w.Fields) > 1024 || len(w.Catalog) != len(w.Fields) || len(w.Current) != len(w.Fields) ||
		!validSHA256(w.Prepared.SHA256) || w.Prepared.Bytes < 0 || int64(w.Prepared.Bytes) > w.Bounds.MaxPreparedBytes {
		return fmt.Errorf("invalid qualified identity or field cardinality")
	}
	seen := make(map[string]bool, len(w.Fields))
	var desiredBytes int64
	for index, field := range w.Fields {
		if !validGuardedFieldWireID(field.Field) || reservedGuardedFieldWireID(field.Field) || seen[field.Field] || (field.Source != "raw" && field.Source != "markdown") || !validSHA256(field.SHA256) || field.Bytes < 0 || int64(field.Bytes) > w.Bounds.MaxDesiredCanonicalBytes {
			return fmt.Errorf("fields[%d] is invalid", index)
		}
		var value any
		if json.Unmarshal(field.Value, &value) != nil {
			return fmt.Errorf("fields[%d].value is invalid", index)
		}
		kind := ""
		switch value.(type) {
		case string:
			kind = "string"
		case []any:
			kind = "array"
		case map[string]any:
			kind = "object"
		default:
			return fmt.Errorf("fields[%d].value has an unsupported type", index)
		}
		if field.Kind != kind || field.Source == "markdown" && kind != "string" {
			return fmt.Errorf("fields[%d].kind contradicts value", index)
		}
		if int64(field.Bytes) > w.Bounds.MaxDesiredCanonicalBytes-desiredBytes {
			return fmt.Errorf("desired projection aggregate is oversized")
		}
		desiredBytes += int64(field.Bytes)
		seen[field.Field] = true
	}
	if !validGuardedFieldProjectionSet(w.Catalog, w.Current, w.Fields, "current") {
		return fmt.Errorf("field projections are invalid")
	}
	if len(w.Readback) > 0 && !validGuardedFieldReadback(w.Readback, w.Fields) {
		return fmt.Errorf("readback projections are invalid")
	}
	requestCap, responseCap := w.Bounds.PreviewMaxRequests, w.Bounds.PreviewMaxAggregateResponse
	if w.Mode == "apply" {
		requestCap, responseCap = w.Bounds.ApplyMaxRequests, w.Bounds.ApplyMaxAggregateResponse
	}
	currentBytes, currentOK := guardedFieldProjectionBytes(w.Current, w.Bounds.MaxCurrentCanonicalBytes)
	readbackBytes, readbackOK := guardedFieldProjectionBytes(w.Readback, w.Bounds.MaxCurrentCanonicalBytes-currentBytes)
	if (qualified && w.Reconciled != (len(w.Readback) == len(w.Fields))) || (!qualified && (w.Reconciled || len(w.Readback) != 0)) ||
		w.Usage.Requests < 0 || w.Usage.Requests > requestCap || w.Usage.ResponseBytes < 0 || w.Usage.ResponseBytes > responseCap || w.Usage.InputBytes < 0 || int64(w.Usage.InputBytes) > w.Bounds.MaxInputBytes ||
		w.Usage.DesiredCanonicalBytes < 0 || int64(w.Usage.DesiredCanonicalBytes) > w.Bounds.MaxDesiredCanonicalBytes || w.Usage.CurrentCanonicalBytes < 0 || int64(w.Usage.CurrentCanonicalBytes) > w.Bounds.MaxCurrentCanonicalBytes ||
		!currentOK || !readbackOK || qualified && (int64(w.Usage.DesiredCanonicalBytes) != desiredBytes || int64(w.Usage.CurrentCanonicalBytes) != currentBytes+readbackBytes) {
		return fmt.Errorf("invalid content-free evidence")
	}
	return nil
}

func validBackendSHA256(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validSHA256(strings.TrimPrefix(value, "sha256:"))
}

func validGuardedFieldWireID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 1024 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func reservedGuardedFieldWireID(value string) bool {
	switch strings.ToLower(value) {
	case "project", "issuetype", "summary", "description", "labels", "assignee":
		return true
	default:
		return false
	}
}

func validGuardedFieldProjectionSet(catalog []jiraGuardedFieldCatalogWire, current []jiraGuardedFieldProjectionWire, fields []jiraGuardedFieldDesiredWire, _ string) bool {
	for index := range fields {
		if catalog[index].ID != fields[index].Field || !catalog[index].Custom || current[index].Field != fields[index].Field || !current[index].Present || !validGuardedFieldProjectionKind(current[index].Kind) || current[index].Bytes < 0 || !validSHA256(current[index].SHA256) {
			return false
		}
	}
	return true
}

func validGuardedFieldReadback(values []jiraGuardedFieldProjectionWire, selected []jiraGuardedFieldDesiredWire) bool {
	if len(values) != len(selected) {
		return false
	}
	for index, value := range values {
		if value.Field != selected[index].Field || !value.Present || !validGuardedFieldProjectionKind(value.Kind) || value.Bytes < 0 || !validSHA256(value.SHA256) {
			return false
		}
	}
	return true
}

func guardedFieldProjectionBytes(values []jiraGuardedFieldProjectionWire, maximum int64) (int64, bool) {
	if maximum < 0 {
		return 0, false
	}
	var total int64
	for _, value := range values {
		if value.Bytes < 0 || int64(value.Bytes) > maximum-total {
			return 0, false
		}
		total += int64(value.Bytes)
	}
	return total, true
}

func validGuardedFieldProjectionKind(kind string) bool {
	switch kind {
	case "null", "boolean", "number", "string", "array", "object":
		return true
	default:
		return false
	}
}

func validJiraGuardedFieldTruth(mode, status string, attempted, reconciled, complete bool) bool {
	if mode == "dry-run" {
		return !attempted && !reconciled && ((status == "would_apply" || status == "already_satisfied") && complete || status == "blocked" && !complete)
	}
	if mode != "apply" {
		return false
	}
	switch status {
	case "already_satisfied":
		return !attempted && !reconciled && complete || attempted && reconciled && complete
	case "blocked":
		return !attempted && !reconciled
	case "applied":
		return attempted && reconciled && complete
	case "unknown", "failed":
		return attempted && (reconciled == complete)
	default:
		return false
	}
}
