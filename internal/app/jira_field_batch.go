package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraFieldBatchSchemaVersion               = 1
	JiraFieldBatchMaxKeys                     = 25
	JiraFieldBatchMaxKeyBytes                 = 64
	JiraFieldBatchMaxFields                   = 8
	JiraFieldBatchMaxSelectorBytes            = 1024
	JiraFieldBatchMaxCatalogFields            = 4096
	JiraFieldBatchMaxCatalogMemberBytes       = 1024
	JiraFieldBatchMaxCellBytes                = 16 << 10
	JiraFieldBatchMaxSearchPages              = 25
	JiraFieldBatchMaxRequests                 = 64
	JiraFieldBatchMaxResponseBytes      int64 = 16 << 20
	JiraFieldBatchMaxOutputBytes              = 16 << 20
	jiraFieldBatchDeadline                    = 60 * time.Second
)

type JiraFieldBatchOpts struct {
	Keys      []string
	Selectors []string
}

type JiraFieldBatchSelection struct {
	KeyCount   int `json:"key_count"`
	FieldCount int `json:"field_count"`
}

type JiraFieldBatchBounds struct {
	MaxKeys               int   `json:"max_keys"`
	MaxKeyBytes           int   `json:"max_key_bytes"`
	MaxFields             int   `json:"max_fields"`
	MaxFieldSelectorBytes int   `json:"max_field_selector_bytes"`
	MaxCatalogFields      int   `json:"max_catalog_fields"`
	MaxCatalogMemberBytes int   `json:"max_catalog_member_bytes"`
	MaxCellBytes          int   `json:"max_cell_bytes"`
	MaxSearchPages        int   `json:"max_search_pages"`
	MaxRequests           int   `json:"max_requests"`
	MaxResponseBytes      int64 `json:"max_response_bytes"`
	MaxOutputBytes        int   `json:"max_output_bytes"`
	DeadlineMillis        int64 `json:"deadline_millis"`
}

type JiraFieldBatchUsage struct {
	Requests      int   `json:"requests"`
	ResponseBytes int64 `json:"response_bytes"`
	SearchPages   int   `json:"search_pages"`
	FoundCount    int   `json:"found_count"`
	MissingCount  int   `json:"missing_count"`
}

type JiraFieldBatchField struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Custom bool   `json:"custom"`
	Schema string `json:"schema,omitempty"`
}

type JiraFieldBatchCell struct {
	FieldID            string `json:"field_id"`
	State              string `json:"state"`
	Complete           bool   `json:"complete"`
	Truncated          bool   `json:"truncated"`
	OriginalValueBytes int    `json:"original_value_bytes"`
	EmittedValueBytes  int    `json:"emitted_value_bytes"`
	Value              *any   `json:"value,omitempty"`
}

type JiraFieldBatchIssue struct {
	RequestedKey string               `json:"requested_key"`
	Found        bool                 `json:"found"`
	Reason       string               `json:"reason,omitempty"`
	ID           string               `json:"id,omitempty"`
	Key          string               `json:"key,omitempty"`
	Updated      string               `json:"updated,omitempty"`
	Cells        []JiraFieldBatchCell `json:"cells,omitempty"`
}

type JiraFieldBatchResult struct {
	SchemaVersion int                     `json:"schema_version"`
	Operation     string                  `json:"operation"`
	Projection    string                  `json:"projection"`
	Complete      bool                    `json:"complete"`
	Reconciled    bool                    `json:"reconciled"`
	Selection     JiraFieldBatchSelection `json:"selection"`
	Bounds        JiraFieldBatchBounds    `json:"bounds"`
	Usage         JiraFieldBatchUsage     `json:"usage"`
	Fields        []JiraFieldBatchField   `json:"fields"`
	Issues        []JiraFieldBatchIssue   `json:"issues"`
	encodedJSON   []byte
}

// EncodedJSON returns the already-bounded complete JSON document. The app
// prepares it before the CLI writes stdout, so output overflow cannot expose a
// partial result.
func (r *JiraFieldBatchResult) EncodedJSON() []byte {
	if r == nil {
		return nil
	}
	return append([]byte(nil), r.encodedJSON...)
}

type jiraFieldBatchError struct{ cause error }

func (e *jiraFieldBatchError) Error() string { return "jira field batch read failed" }

func (e *jiraFieldBatchError) Is(target error) bool {
	return e != nil && errors.Is(e.cause, target)
}

func (e *jiraFieldBatchError) Format(state fmt.State, verb rune) {
	safe := e.Error()
	if verb == 'q' {
		safe = strconv.Quote(safe)
	}
	_, _ = io.WriteString(state, safe)
}

func contentFreeJiraFieldBatchError(err error) error {
	if err == nil {
		return nil
	}
	return &jiraFieldBatchError{cause: err}
}

// ValidateJiraFieldBatchOpts performs the complete local-only admission check.
// The CLI uses it before configuration so invalid bounds never reach a backend.
func ValidateJiraFieldBatchOpts(opts JiraFieldBatchOpts) error {
	_, _, err := validateJiraFieldBatchInput(opts)
	return err
}

func (s *JiraService) IssueFieldBatch(ctx context.Context, opts JiraFieldBatchOpts) (*JiraFieldBatchResult, error) {
	keys, selectors, err := validateJiraFieldBatchInput(opts)
	if err != nil {
		return nil, err
	}
	reader, readerOK := s.tr.(domain.QualifiedFieldCatalogReader)
	searcher, searcherOK := s.tr.(domain.QualifiedIssueSearcher)
	if !readerOK || !searcherOK {
		return nil, fmt.Errorf("%w: qualified Jira field batch discovery is unavailable", domain.ErrConfig)
	}
	budget, err := domain.NewChildReadBudget(domain.ReadBudgetFromContext(ctx), JiraFieldBatchMaxRequests, JiraFieldBatchMaxResponseBytes)
	if err != nil {
		return nil, contentFreeJiraFieldBatchError(err)
	}
	bounded, cancel := context.WithTimeout(domain.WithReadBudget(domain.WithRedactedHTTPTrace(ctx), budget), jiraFieldBatchDeadline)
	defer cancel()

	snapshot, err := reader.ReadFieldCatalog(bounded)
	if err != nil {
		return nil, contentFreeJiraFieldBatchError(err)
	}
	defs, err := resolveJiraFieldBatchSelectors(snapshot, selectors)
	if err != nil {
		return nil, contentFreeJiraFieldBatchError(err)
	}
	issuesByKey, searchPages, err := readJiraFieldBatchIssues(bounded, searcher, keys, defs)
	if err != nil {
		return nil, contentFreeJiraFieldBatchError(err)
	}
	if err := bounded.Err(); err != nil {
		return nil, contentFreeJiraFieldBatchError(err)
	}

	result, err := projectJiraFieldBatch(bounded, keys, defs, issuesByKey)
	if err != nil {
		return nil, contentFreeJiraFieldBatchError(err)
	}
	usage := budget.Usage()
	result.Usage.Requests = usage.Attempts
	result.Usage.ResponseBytes = usage.ResponseBytes
	result.Usage.SearchPages = searchPages
	encoded, err := encodeJiraFieldBatchResult(result, JiraFieldBatchMaxOutputBytes)
	if err != nil {
		return nil, contentFreeJiraFieldBatchError(err)
	}
	if err := bounded.Err(); err != nil {
		return nil, contentFreeJiraFieldBatchError(err)
	}
	result.encodedJSON = encoded
	return result, nil
}

func encodeJiraFieldBatchResult(result *JiraFieldBatchResult, maximum int) ([]byte, error) {
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: encode result", domain.ErrCheckFailed)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maximum {
		return nil, fmt.Errorf("%w: %w", domain.ErrCheckFailed, domain.ErrOutputLimit)
	}
	return encoded, nil
}

func validateJiraFieldBatchInput(opts JiraFieldBatchOpts) ([]string, []string, error) {
	if len(opts.Keys) < 1 || len(opts.Keys) > JiraFieldBatchMaxKeys {
		return nil, nil, fmt.Errorf("%w: Jira field batch requires 1-%d issue keys", domain.ErrUsage, JiraFieldBatchMaxKeys)
	}
	if len(opts.Selectors) < 1 || len(opts.Selectors) > JiraFieldBatchMaxFields {
		return nil, nil, fmt.Errorf("%w: Jira field batch requires 1-%d field selectors", domain.ErrUsage, JiraFieldBatchMaxFields)
	}
	keys := make([]string, len(opts.Keys))
	seenKeys := make(map[string]bool, len(opts.Keys))
	for index, raw := range opts.Keys {
		if len(raw) > JiraFieldBatchMaxKeyBytes || !utf8.ValidString(raw) || raw != strings.TrimSpace(raw) || !domain.ValidJiraIssueKey(raw) {
			return nil, nil, fmt.Errorf("%w: Jira field batch keys must be canonical and at most %d bytes", domain.ErrUsage, JiraFieldBatchMaxKeyBytes)
		}
		if seenKeys[raw] {
			return nil, nil, fmt.Errorf("%w: Jira field batch keys must be unique", domain.ErrUsage)
		}
		seenKeys[raw] = true
		keys[index] = raw
	}
	selectors := make([]string, len(opts.Selectors))
	seenSelectors := make(map[string]bool, len(opts.Selectors))
	for index, raw := range opts.Selectors {
		if raw == "" || len(raw) > JiraFieldBatchMaxSelectorBytes || !utf8.ValidString(raw) || raw != strings.TrimSpace(raw) {
			return nil, nil, fmt.Errorf("%w: Jira field selectors must be non-empty UTF-8 values of at most %d bytes", domain.ErrUsage, JiraFieldBatchMaxSelectorBytes)
		}
		if seenSelectors[raw] {
			return nil, nil, fmt.Errorf("%w: Jira field selectors must be unique", domain.ErrUsage)
		}
		seenSelectors[raw] = true
		selectors[index] = raw
	}
	return keys, selectors, nil
}

func resolveJiraFieldBatchSelectors(snapshot domain.FieldCatalogSnapshot, selectors []string) ([]domain.FieldDef, error) {
	if err := validateFieldCatalogSnapshot(snapshot); err != nil || !snapshot.Complete {
		return nil, fmt.Errorf("%w: qualified Jira field catalog is incomplete or malformed", domain.ErrCheckFailed)
	}
	if len(snapshot.Fields) > JiraFieldBatchMaxCatalogFields {
		return nil, fmt.Errorf("%w: qualified Jira field catalog exceeds the bounded size", domain.ErrCheckFailed)
	}
	seenFoldedIDs := make(map[string]bool, len(snapshot.Fields))
	for _, def := range snapshot.Fields {
		if def.Name == "" || !validJiraFieldBatchCatalogMember(def.ID) ||
			!validJiraFieldBatchCatalogMember(def.Name) ||
			(def.Schema != "" && !validJiraFieldBatchCatalogMember(def.Schema)) {
			return nil, fmt.Errorf("%w: qualified Jira field catalog is incomplete or malformed", domain.ErrCheckFailed)
		}
		folded := strings.ToLower(def.ID)
		if seenFoldedIDs[folded] {
			return nil, fmt.Errorf("%w: qualified Jira field catalog contains duplicate field identity", domain.ErrCheckFailed)
		}
		seenFoldedIDs[folded] = true
	}
	resolved := make([]domain.FieldDef, 0, len(selectors))
	seenResolved := make(map[string]bool, len(selectors))
	for _, selector := range selectors {
		one, err := ResolveJiraFieldSelectors(snapshot.Fields, []string{selector})
		if err != nil || len(one) != 1 {
			if err == nil {
				err = domain.ErrCheckFailed
			}
			return nil, err
		}
		if seenResolved[one[0].ID] {
			return nil, fmt.Errorf("%w: Jira field selectors converge on one technical field", domain.ErrUsage)
		}
		seenResolved[one[0].ID] = true
		resolved = append(resolved, one[0])
	}
	return resolved, nil
}

func validJiraFieldBatchCatalogMember(value string) bool {
	return value != "" && len(value) <= JiraFieldBatchMaxCatalogMemberBytes &&
		utf8.ValidString(value) && strings.TrimSpace(value) == value
}

func readJiraFieldBatchIssues(ctx context.Context, searcher domain.QualifiedIssueSearcher, keys []string, defs []domain.FieldDef) (map[string]domain.Issue, int, error) {
	requestFields := fieldDefIDs(defs)
	if !jiraFieldBatchContains(requestFields, "updated") {
		requestFields = append(requestFields, "updated")
	}
	quoted := make([]string, len(keys))
	for index, key := range keys {
		quoted[index] = strconv.Quote(key)
	}
	jql := "key in (" + strings.Join(quoted, ",") + ") ORDER BY key ASC"
	requested := make(map[string]bool, len(keys))
	for _, key := range keys {
		requested[key] = true
	}
	issues := make(map[string]domain.Issue, len(keys))
	seenIDs := make(map[string]bool, len(keys))
	seenCursors := map[string]bool{"": true}
	cursor := ""
	wantTotal := -1
	for pageNumber := 1; pageNumber <= JiraFieldBatchMaxSearchPages; pageNumber++ {
		page, err := searcher.SearchQualified(ctx, jql, requestFields, JiraFieldBatchMaxKeys, cursor)
		if err != nil {
			return nil, pageNumber, err
		}
		if err := validateIssueSearchPage(page); err != nil || !page.TotalKnown || page.Total < 0 || page.Total > len(keys) {
			return nil, pageNumber, fmt.Errorf("%w: qualified Jira field batch search is incomplete or malformed", domain.ErrCheckFailed)
		}
		if wantTotal < 0 {
			wantTotal = page.Total
		} else if page.Total != wantTotal {
			return nil, pageNumber, fmt.Errorf("%w: qualified Jira field batch search total changed", domain.ErrCheckFailed)
		}
		for _, issue := range page.Issues {
			if err := validateJiraFieldBatchIssue(issue, requested); err != nil {
				return nil, pageNumber, err
			}
			if _, exists := issues[issue.Key]; exists {
				return nil, pageNumber, fmt.Errorf("%w: qualified Jira field batch search returned a duplicate issue", domain.ErrCheckFailed)
			}
			if seenIDs[issue.ID] {
				return nil, pageNumber, fmt.Errorf("%w: qualified Jira field batch search returned a duplicate issue identity", domain.ErrCheckFailed)
			}
			seenIDs[issue.ID] = true
			issues[issue.Key] = issue
		}
		if len(issues) > wantTotal {
			return nil, pageNumber, fmt.Errorf("%w: qualified Jira field batch search exceeded its total", domain.ErrCheckFailed)
		}
		if page.Complete {
			if len(issues) != wantTotal {
				return nil, pageNumber, fmt.Errorf("%w: qualified Jira field batch search total did not reconcile", domain.ErrCheckFailed)
			}
			return issues, pageNumber, nil
		}
		if page.Next == "" || seenCursors[page.Next] || !advanceJiraFieldBatchCursor(cursor, page.Next, len(page.Issues)) {
			return nil, pageNumber, fmt.Errorf("%w: qualified Jira field batch search cursor stalled", domain.ErrCheckFailed)
		}
		seenCursors[page.Next] = true
		cursor = page.Next
	}
	return nil, JiraFieldBatchMaxSearchPages, fmt.Errorf("%w: qualified Jira field batch search exceeded the page bound", domain.ErrCheckFailed)
}

func validateJiraFieldBatchIssue(issue domain.Issue, requested map[string]bool) error {
	if !requested[issue.Key] || !domain.ValidJiraIssueKey(issue.Key) || !canonicalPositiveDecimal(issue.ID) || issue.Fields == nil {
		return fmt.Errorf("%w: qualified Jira field batch search returned malformed identity", domain.ErrCheckFailed)
	}
	updated, ok := issue.Fields["updated"].(string)
	if !ok || !validJiraFieldBatchCatalogMember(updated) {
		return fmt.Errorf("%w: qualified Jira field batch search omitted updated provenance", domain.ErrCheckFailed)
	}
	return nil
}

func canonicalPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func advanceJiraFieldBatchCursor(current, next string, count int) bool {
	parse := func(value string) (int64, bool) {
		if value == "" {
			return 0, true
		}
		if value[0] == '0' && value != "0" {
			return 0, false
		}
		number, err := strconv.ParseInt(value, 10, 64)
		return number, err == nil && number >= 0
	}
	currentNumber, currentOK := parse(current)
	nextNumber, nextOK := parse(next)
	return currentOK && nextOK && count > 0 && nextNumber == currentNumber+int64(count)
}

func projectJiraFieldBatch(ctx context.Context, keys []string, defs []domain.FieldDef, issuesByKey map[string]domain.Issue) (*JiraFieldBatchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &JiraFieldBatchResult{
		SchemaVersion: jiraFieldBatchSchemaVersion,
		Operation:     "jira_issue_field_batch",
		Projection:    "compact",
		Complete:      true,
		Reconciled:    true,
		Selection:     JiraFieldBatchSelection{KeyCount: len(keys), FieldCount: len(defs)},
		Bounds: JiraFieldBatchBounds{
			MaxKeys: JiraFieldBatchMaxKeys, MaxKeyBytes: JiraFieldBatchMaxKeyBytes,
			MaxFields: JiraFieldBatchMaxFields, MaxFieldSelectorBytes: JiraFieldBatchMaxSelectorBytes,
			MaxCatalogFields: JiraFieldBatchMaxCatalogFields, MaxCatalogMemberBytes: JiraFieldBatchMaxCatalogMemberBytes,
			MaxCellBytes: JiraFieldBatchMaxCellBytes, MaxSearchPages: JiraFieldBatchMaxSearchPages,
			MaxRequests: JiraFieldBatchMaxRequests, MaxResponseBytes: JiraFieldBatchMaxResponseBytes,
			MaxOutputBytes: JiraFieldBatchMaxOutputBytes, DeadlineMillis: jiraFieldBatchDeadline.Milliseconds(),
		},
		Fields: make([]JiraFieldBatchField, 0, len(defs)),
		Issues: make([]JiraFieldBatchIssue, 0, len(keys)),
	}
	for _, def := range defs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result.Fields = append(result.Fields, JiraFieldBatchField{ID: def.ID, Name: def.Name, Custom: def.Custom, Schema: def.Schema})
	}
	for _, requestedKey := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		issue, found := issuesByKey[requestedKey]
		if !found {
			result.Issues = append(result.Issues, JiraFieldBatchIssue{RequestedKey: requestedKey, Found: false, Reason: "missing_or_inaccessible"})
			result.Usage.MissingCount++
			continue
		}
		row := JiraFieldBatchIssue{
			RequestedKey: requestedKey, Found: true, ID: issue.ID, Key: issue.Key,
			Updated: issue.Fields["updated"].(string), Cells: make([]JiraFieldBatchCell, 0, len(defs)),
		}
		for _, def := range defs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			cell, err := projectJiraFieldBatchCell(def.ID, issue.Fields)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if cell.Truncated {
				result.Complete = false
			}
			row.Cells = append(row.Cells, cell)
		}
		result.Issues = append(result.Issues, row)
		result.Usage.FoundCount++
	}
	return result, nil
}

func projectJiraFieldBatchCell(fieldID string, fields map[string]any) (JiraFieldBatchCell, error) {
	raw, present := fields[fieldID]
	cell := JiraFieldBatchCell{FieldID: fieldID, State: "absent", Complete: true}
	if !present {
		return cell, nil
	}
	rawJSON, err := json.Marshal(raw)
	if err != nil {
		return JiraFieldBatchCell{}, fmt.Errorf("%w: Jira field batch value is malformed", domain.ErrCheckFailed)
	}
	value, emittedBytes, truncated, err := boundedCompactJiraFieldValue(raw, JiraFieldBatchMaxCellBytes)
	if err != nil {
		return JiraFieldBatchCell{}, fmt.Errorf("%w: Jira field batch value is malformed", domain.ErrCheckFailed)
	}
	state := "value"
	if raw == nil {
		state = "null"
	} else if jiraFieldValueEmpty(raw) {
		state = "empty"
	}
	cell.State = state
	cell.Complete = !truncated
	cell.Truncated = truncated
	cell.OriginalValueBytes = len(rawJSON)
	cell.EmittedValueBytes = emittedBytes
	cell.Value = &value
	return cell, nil
}

func jiraFieldBatchContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
