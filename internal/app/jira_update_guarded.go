package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mdwiki"
	"github.com/isukharev/atl/internal/strictjson"
)

const jiraGuardedUpdateSchemaVersion = 1

type JiraGuardedUpdateOpts struct {
	Summary              string
	SummaryPresent       bool
	Description          []byte
	DescriptionPresent   bool
	DescriptionSource    string
	Fields               map[string]domain.JiraFieldInput
	Apply                bool
	ExpectedProposalHash string
}

type JiraGuardedUpdateSource struct {
	Kind    string `json:"kind"`
	Present bool   `json:"present"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256,omitempty"`
}

type JiraGuardedUpdateFieldProjection struct {
	FieldID   string `json:"field_id"`
	InputKind string `json:"input_kind"`
	JSONKind  string `json:"normalized_json_kind"`
	Bytes     int    `json:"normalized_bytes"`
	SHA256    string `json:"normalized_sha256"`
}

type JiraGuardedUpdateBounds struct {
	MaxFields                int   `json:"max_fields"`
	MaxCatalogEntries        int   `json:"max_catalog_entries"`
	MaxFieldIDBytes          int   `json:"max_field_id_bytes"`
	MaxRequestedKeyBytes     int   `json:"max_requested_key_bytes"`
	MaxImmutableIDBytes      int   `json:"max_immutable_id_bytes"`
	MaxInputBytes            int64 `json:"max_input_bytes"`
	MaxDesiredCanonicalBytes int64 `json:"max_desired_canonical_bytes"`
	MaxCurrentCanonicalBytes int64 `json:"max_current_canonical_bytes"`
	MaxPreparedBytes         int64 `json:"max_prepared_bytes"`
	MaxCatalogResponseBytes  int64 `json:"max_catalog_response_bytes"`
	MaxIssueResponseBytes    int64 `json:"max_issue_response_bytes"`
	MaxWriteResponseBytes    int64 `json:"max_write_response_bytes"`
	MaxQueryAndPathBytes     int   `json:"max_query_and_path_bytes"`
	PreviewMaxRequests       int   `json:"preview_max_requests"`
	ApplyMaxRequests         int   `json:"apply_max_requests"`
	PreviewMaxResponseBytes  int64 `json:"preview_max_response_bytes"`
	ApplyMaxResponseBytes    int64 `json:"apply_max_response_bytes"`
	DeadlineMillis           int64 `json:"deadline_millis"`
}

type JiraGuardedUpdateUsage struct {
	Requests              int   `json:"requests"`
	ResponseBytes         int64 `json:"response_bytes"`
	InputBytes            int   `json:"input_bytes"`
	DesiredCanonicalBytes int   `json:"desired_canonical_bytes"`
	CurrentCanonicalBytes int   `json:"current_canonical_bytes"`
}

type JiraGuardedUpdateResult struct {
	SchemaVersion   int                                `json:"schema_version"`
	Operation       string                             `json:"operation"`
	Mode            string                             `json:"mode"`
	Status          string                             `json:"status"`
	BackendSHA256   string                             `json:"backend_sha256,omitempty"`
	RequestedKey    string                             `json:"requested_key"`
	IssueID         string                             `json:"issue_id,omitempty"`
	Key             string                             `json:"key"`
	Project         string                             `json:"project,omitempty"`
	Updated         string                             `json:"updated,omitempty"`
	ReadbackUpdated string                             `json:"readback_updated,omitempty"`
	Source          JiraGuardedUpdateSource            `json:"source"`
	Catalog         []JiraFieldCatalogProjection       `json:"catalog"`
	Current         []JiraFieldValueProjection         `json:"current"`
	Readback        []JiraFieldValueProjection         `json:"readback,omitempty"`
	Desired         []JiraGuardedUpdateFieldProjection `json:"desired"`
	Prepared        JiraFieldPayloadProjection         `json:"prepared"`
	ProposalHash    string                             `json:"proposal_hash,omitempty"`
	Bounds          JiraGuardedUpdateBounds            `json:"bounds"`
	Usage           JiraGuardedUpdateUsage             `json:"usage"`
	WriteAttempted  bool                               `json:"write_attempted"`
	Reconciled      bool                               `json:"reconciled"`
	Complete        bool                               `json:"complete"`
}

type jiraGuardedUpdateError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *jiraGuardedUpdateError) Error() string { return e.message }
func (e *jiraGuardedUpdateError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}
func (e *jiraGuardedUpdateError) DiagnosticAmbiguousWrite() bool { return e != nil && e.ambiguous }
func (e *jiraGuardedUpdateError) DiagnosticTerminalCheckFailure() bool {
	return e != nil && e.closed
}

func guardedUpdateFailure(message string, cause error, closed, ambiguous bool) error {
	return &jiraGuardedUpdateError{
		message: message, cause: preserveGuardedBudgetCause(cause, sanitizeRemoteWriteCause(cause)),
		closed: closed, ambiguous: ambiguous,
	}
}

type jiraGuardedUpdateInput struct {
	summary            string
	summaryPresent     bool
	description        []byte
	descriptionPresent bool
	descriptionSource  string
	fields             map[string]domain.JiraFieldInput
	expected           map[string]any
	selected           []string
	inputBytes         int64
	source             JiraGuardedUpdateSource
}

type jiraGuardedUpdateSnapshot struct {
	result      *JiraGuardedUpdateResult
	issue       domain.JiraGuardedFieldIssue
	catalog     domain.JiraGuardedFieldCatalog
	prepared    domain.JiraGuardedUpdatePreparation
	updatedTime time.Time
}

// ValidateJiraGuardedUpdateOpts validates and converts candidate bytes without
// reading configuration, credentials, updater state, or a backend.
func ValidateJiraGuardedUpdateOpts(opts JiraGuardedUpdateOpts) error {
	_, err := normalizeGuardedUpdateInput(opts)
	return err
}

// ValidateJiraGuardedUpdateFieldInputs validates inline field candidates
// without reading description files, configuration, credentials, or a backend.
func ValidateJiraGuardedUpdateFieldInputs(fields map[string]domain.JiraFieldInput) error {
	if len(fields) > domain.JiraGuardedUpdateMaxFields {
		return fmt.Errorf("%w: guarded Jira update field count exceeds its bound", domain.ErrUsage)
	}
	var inputBytes int64
	for field, value := range fields {
		if !domain.ValidJiraGuardedUpdateCandidateField(field) {
			return fmt.Errorf("%w: generic update fields must be exact custom field ids", domain.ErrUsage)
		}
		inputBytes += int64(len(field)) + int64(len(value.Value))
		if inputBytes > domain.JiraGuardedUpdateMaxInputBytes {
			return fmt.Errorf("%w: guarded Jira update input exceeds its bound", domain.ErrUsage)
		}
		if _, err := normalizeGuardedUpdateFieldValue(value); err != nil {
			return fmt.Errorf("%w: field %q has an invalid value", domain.ErrUsage, field)
		}
	}
	return nil
}

// UpdateIssueGuarded previews or applies one exact whole-issue update. The
// proposal is requalified in full immediately before its sole numeric-id PUT.
func (s *JiraService) UpdateIssueGuarded(ctx context.Context, requestedKey string, opts JiraGuardedUpdateOpts) (*JiraGuardedUpdateResult, error) {
	key, err := ValidateJiraGuardedFieldKey(requestedKey)
	if err != nil {
		return nil, err
	}
	input, err := normalizeGuardedUpdateInput(opts)
	if err != nil {
		return nil, err
	}
	opts.ExpectedProposalHash = strings.TrimSpace(opts.ExpectedProposalHash)
	if opts.Apply {
		if err := ValidateJiraDescriptionEditReviewHash(opts.ExpectedProposalHash); err != nil {
			return nil, err
		}
	} else if opts.ExpectedProposalHash != "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash requires --apply", domain.ErrUsage)
	}
	result := newJiraGuardedUpdateResult(key, input, opts.Apply)
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return result, guardedUpdateFailure("guarded Jira update backend identity is invalid", domain.ErrCheckFailed, true, false)
	}
	result.BackendSHA256 = backendHash
	port, ok := s.tr.(domain.JiraGuardedUpdatePort)
	if !ok {
		return result, guardedUpdateFailure("guarded Jira update is unavailable", domain.ErrConfig, true, false)
	}
	maxRequests, maxResponse := domain.JiraGuardedUpdatePreviewRequests, domain.JiraGuardedUpdatePreviewResponseBytes
	if opts.Apply {
		maxRequests, maxResponse = domain.JiraGuardedUpdateApplyRequests, domain.JiraGuardedUpdateApplyResponseBytes
	}
	execution, err := newJiraGuardedExecution(ctx, domain.ReadBudgetFromContext(ctx), maxRequests, maxResponse, time.Duration(domain.JiraGuardedUpdateDeadlineMillis)*time.Millisecond)
	if err != nil {
		return result, guardedUpdateFailure("guarded Jira update budget is invalid", err, true, false)
	}
	defer execution.Close()
	defer func() {
		usage := execution.Usage()
		result.Usage.Requests, result.Usage.ResponseBytes = usage.Attempts, usage.ResponseBytes
	}()
	initial, err := s.buildGuardedUpdateSnapshot(execution.ctx, port, key, key, "", input, backendHash, opts.Apply)
	if err != nil {
		return result, guardedUpdateFailure("guarded Jira update proposal qualification failed", err, true, false)
	}
	result = initial.result
	if opts.Apply && opts.ExpectedProposalHash != result.ProposalHash {
		result.Status = "blocked"
		return result, guardedUpdateFailure("guarded Jira update proposal changed since review", domain.ErrCheckFailed, true, false)
	}
	if guardedUpdateSatisfied(initial.issue.Fields, initial.prepared.Values) {
		result.Status = "already_satisfied"
		return result, nil
	}
	result.Status = "would_apply"
	if !opts.Apply {
		return result, nil
	}
	prewrite, err := s.buildGuardedUpdateSnapshot(execution.ctx, port, initial.issue.ID, key, initial.issue.ID, input, backendHash, true)
	if err != nil {
		result.Status, result.Complete = "blocked", false
		return result, guardedUpdateFailure("guarded Jira update could not be qualified immediately before dispatch", err, true, false)
	}
	if prewrite.result.ProposalHash != result.ProposalHash {
		result.Status = "blocked"
		return result, guardedUpdateFailure("guarded Jira update proposal changed immediately before dispatch", domain.ErrCheckFailed, true, false)
	}
	if err := execution.ctx.Err(); err != nil {
		result.Status, result.Complete = "blocked", false
		return result, guardedUpdateFailure("guarded Jira update deadline expired before dispatch", err, true, false)
	}
	result.WriteAttempted = true
	writeErr := port.WriteGuardedUpdate(execution.ctx, domain.JiraGuardedUpdateWrite{
		ID: prewrite.issue.ID, Key: prewrite.issue.Key, Project: prewrite.issue.Project,
		Qualified: append([]domain.JiraGuardedFieldCatalogEntry(nil), prewrite.catalog.Fields...),
		Prepared:  cloneGuardedUpdatePreparation(prewrite.prepared),
	})
	if writeDefinitelyNotAttempted(writeErr) {
		result.WriteAttempted, result.Status = false, "blocked"
		return result, guardedUpdateFailure("guarded Jira update was refused before dispatch", writeErr, true, false)
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status, result.Complete = "not_applied", true
		return result, guardedUpdateFailure("Jira definitively rejected the guarded update", writeErr, false, false)
	}
	closeout, closeCancel := execution.Closeout()
	defer closeCancel()
	readback, readErr := s.readGuardedUpdateIssue(closeout, port, prewrite.issue.ID, key, prewrite.issue.ID, selectedUpdateFields(input), domain.JiraGuardedUpdateMaxCurrentBytes)
	if readErr != nil || closeout.Err() != nil {
		result.Status, result.Complete = "outcome_unknown", false
		return result, guardedUpdateFailure("guarded Jira update outcome is unknown; do not replay automatically", errors.Join(writeErr, readErr, closeout.Err()), true, true)
	}
	result.Readback, result.ReadbackUpdated = readback.result.Current, readback.issue.Updated
	result.Usage.CurrentCanonicalBytes += readback.result.Usage.CurrentCanonicalBytes
	result.Reconciled = true
	if guardedUpdateSatisfied(readback.issue.Fields, prewrite.prepared.Values) && readback.updatedTime.After(prewrite.updatedTime) {
		result.Complete = true
		if writeErr == nil {
			result.Status = "applied"
		} else {
			result.Status = "recovered"
		}
		return result, nil
	}
	result.Status, result.Complete = "outcome_unknown", false
	return result, guardedUpdateFailure("guarded Jira update readback did not prove an exact advancing end state; do not replay automatically", writeErr, true, true)
}

func normalizeGuardedUpdateInput(opts JiraGuardedUpdateOpts) (jiraGuardedUpdateInput, error) {
	totalFields := len(opts.Fields)
	if opts.SummaryPresent {
		totalFields++
	}
	if opts.DescriptionPresent {
		totalFields++
	}
	if totalFields == 0 {
		return jiraGuardedUpdateInput{}, fmt.Errorf("%w: nothing to update", domain.ErrUsage)
	}
	if totalFields > domain.JiraGuardedUpdateMaxFields {
		return jiraGuardedUpdateInput{}, fmt.Errorf("%w: guarded Jira update field count exceeds its bound", domain.ErrUsage)
	}
	inputBytes := int64(len(opts.Summary)) + int64(len(opts.Description))
	if inputBytes > domain.JiraGuardedUpdateMaxInputBytes {
		return jiraGuardedUpdateInput{}, fmt.Errorf("%w: guarded Jira update input exceeds its bound", domain.ErrUsage)
	}
	for field, value := range opts.Fields {
		inputBytes += int64(len(field)) + int64(len(value.Value))
		if inputBytes > domain.JiraGuardedUpdateMaxInputBytes {
			return jiraGuardedUpdateInput{}, fmt.Errorf("%w: guarded Jira update input exceeds its bound", domain.ErrUsage)
		}
	}
	input := jiraGuardedUpdateInput{
		summary: opts.Summary, summaryPresent: opts.SummaryPresent, descriptionPresent: opts.DescriptionPresent,
		descriptionSource: opts.DescriptionSource, fields: make(map[string]domain.JiraFieldInput, len(opts.Fields)), expected: make(map[string]any, len(opts.Fields)+2),
		inputBytes: inputBytes,
	}
	if !input.descriptionPresent && input.descriptionSource == "" {
		input.descriptionSource = "none"
	}
	if input.summaryPresent {
		if input.summary == "" || !utf8.ValidString(input.summary) {
			return input, fmt.Errorf("%w: --summary must be non-empty valid UTF-8", domain.ErrUsage)
		}
		input.expected["summary"] = input.summary
	} else if input.summary != "" {
		return input, fmt.Errorf("%w: summary presence is inconsistent", domain.ErrUsage)
	}
	if input.descriptionPresent {
		if !utf8.Valid(opts.Description) || input.descriptionSource != "wiki" && input.descriptionSource != "markdown" {
			return input, fmt.Errorf("%w: description source must be wiki or markdown valid UTF-8", domain.ErrUsage)
		}
		input.source = digestGuardedUpdateSource(input.descriptionSource, opts.Description)
		if input.descriptionSource == "markdown" {
			converted, err := mdwiki.ConvertDocument(string(opts.Description))
			if err != nil {
				return input, fmt.Errorf("%w: description Markdown contains an unsupported construct", domain.ErrCheckFailed)
			}
			input.description = []byte(converted)
		} else {
			input.description = bytes.Clone(opts.Description)
		}
		input.expected["description"] = string(input.description)
	} else {
		if len(opts.Description) != 0 || input.descriptionSource != "none" {
			return input, fmt.Errorf("%w: description presence is inconsistent", domain.ErrUsage)
		}
		input.source = JiraGuardedUpdateSource{Kind: "none"}
	}
	if err := ValidateJiraGuardedUpdateFieldInputs(opts.Fields); err != nil {
		return input, err
	}
	for rawField, value := range opts.Fields {
		field := strings.TrimSpace(rawField)
		if field != rawField || !domain.ValidJiraGuardedUpdateCandidateField(field) {
			return input, fmt.Errorf("%w: generic update fields must be exact custom field ids", domain.ErrUsage)
		}
		input.fields[field] = value
		normalized, err := normalizeGuardedUpdateFieldValue(value)
		if err != nil {
			return input, fmt.Errorf("%w: field %q has an invalid value", domain.ErrUsage, field)
		}
		input.expected[field] = normalized
		input.selected = append(input.selected, field)
	}
	sort.Strings(input.selected)
	return input, nil
}

func (s *JiraService) buildGuardedUpdateSnapshot(ctx context.Context, port domain.JiraGuardedUpdatePort, reference, requestedKey, expectedID string, input jiraGuardedUpdateInput, backendHash string, apply bool) (*jiraGuardedUpdateSnapshot, error) {
	catalog := domain.JiraGuardedFieldCatalog{Complete: true, Fields: []domain.JiraGuardedFieldCatalogEntry{}}
	var err error
	if len(input.selected) > 0 {
		catalog, err = port.ReadGuardedUpdateFieldCatalog(ctx, append([]string(nil), input.selected...))
		if err != nil || !validGuardedUpdateCatalog(catalog, input.selected) {
			return nil, fmt.Errorf("%w: Jira returned incomplete custom-field qualification", domain.ErrCheckFailed)
		}
	}
	selected := selectedUpdateFields(input)
	issueRead, err := s.readGuardedUpdateIssue(ctx, port, reference, requestedKey, expectedID, selected, domain.JiraGuardedUpdateMaxCurrentBytes)
	if err != nil {
		return nil, err
	}
	prepared, err := port.PrepareGuardedUpdate(updatePreparationRequest(input, catalog.Fields))
	if err != nil {
		return nil, err
	}
	prepared = cloneGuardedUpdatePreparation(prepared)
	desired, desiredBytes, err := validateGuardedUpdatePreparation(input, catalog.Fields, prepared)
	if err != nil {
		return nil, err
	}
	result := newJiraGuardedUpdateResult(requestedKey, input, apply)
	result.BackendSHA256, result.IssueID, result.Key, result.Project = backendHash, issueRead.issue.ID, issueRead.issue.Key, issueRead.issue.Project
	result.Updated, result.Catalog, result.Current, result.Desired = issueRead.issue.Updated, projectGuardedUpdateCatalog(catalog.Fields), issueRead.result.Current, desired
	result.Usage.CurrentCanonicalBytes, result.Usage.DesiredCanonicalBytes = issueRead.result.Usage.CurrentCanonicalBytes, desiredBytes
	sum := sha256.Sum256(prepared.Payload)
	result.Prepared = JiraFieldPayloadProjection{Bytes: len(prepared.Payload), SHA256: hex.EncodeToString(sum[:])}
	result.Complete, result.Status = true, "would_apply"
	result.ProposalHash, err = jiraGuardedUpdateProposalHash(result)
	if err != nil {
		return nil, err
	}
	return &jiraGuardedUpdateSnapshot{result: result, issue: issueRead.issue, catalog: catalog, prepared: prepared, updatedTime: issueRead.updatedTime}, nil
}

func (s *JiraService) readGuardedUpdateIssue(ctx context.Context, port domain.JiraGuardedUpdatePort, reference, requestedKey, expectedID string, selected []string, maximum int64) (*jiraGuardedUpdateSnapshot, error) {
	issue, err := port.ReadGuardedUpdateIssue(ctx, reference, selected)
	if err != nil {
		return nil, err
	}
	if !issue.Complete || !canonicalPositiveNumericString(issue.ID) || len(issue.ID) > domain.JiraGuardedUpdateMaxImmutableIDBytes ||
		issue.Key != requestedKey || !domain.ValidJiraIssueKey(issue.Key) || !domain.ValidJiraIssueKey(issue.Project+"-1") ||
		!strings.HasPrefix(issue.Key, issue.Project+"-") || expectedID != "" && issue.ID != expectedID || len(issue.Fields) != len(selected) {
		return nil, fmt.Errorf("%w: Jira returned missing, moved, or malformed guarded update identity", domain.ErrCheckFailed)
	}
	for field, evidence := range issue.Fields {
		if !evidence.Present || !validGuardedUpdateObservedField(field, evidence.Value) {
			return nil, fmt.Errorf("%w: Jira returned an unsupported guarded update field shape", domain.ErrCheckFailed)
		}
	}
	updatedTime, err := parseJiraUpdatedTime(issue.Updated)
	if err != nil || strings.TrimSpace(issue.Updated) != issue.Updated {
		return nil, fmt.Errorf("%w: Jira returned an unsupported updated marker", domain.ErrCheckFailed)
	}
	current, currentBytes, err := guardedFieldEvidenceProjections(issue.Fields, selected, maximum)
	if err != nil {
		return nil, err
	}
	return &jiraGuardedUpdateSnapshot{issue: issue, updatedTime: updatedTime, result: &JiraGuardedUpdateResult{Current: current, Usage: JiraGuardedUpdateUsage{CurrentCanonicalBytes: currentBytes}}}, nil
}

func validGuardedUpdateObservedField(field string, value any) bool {
	switch field {
	case "summary":
		text, ok := value.(string)
		return ok && text != ""
	case "description":
		if value == nil {
			return true
		}
		_, ok := value.(string)
		return ok
	default:
		return true
	}
}

func validateGuardedUpdatePreparation(input jiraGuardedUpdateInput, qualified []domain.JiraGuardedFieldCatalogEntry, prepared domain.JiraGuardedUpdatePreparation) ([]JiraGuardedUpdateFieldProjection, int, error) {
	selected := selectedUpdateFields(input)
	if len(prepared.Values) != len(selected) || len(prepared.Fields) != len(selected) || len(prepared.Payload) == 0 || int64(len(prepared.Payload)) > domain.JiraGuardedUpdateMaxPreparedBytes {
		return nil, 0, domain.ErrCheckFailed
	}
	wantQualified := optimisticUpdateQualification(input.selected)
	if !equalGuardedUpdateQualification(qualified, wantQualified) {
		return nil, 0, domain.ErrCheckFailed
	}
	expectedPayload, err := json.Marshal(map[string]any{"fields": prepared.Values})
	if err != nil || !bytes.Equal(expectedPayload, prepared.Payload) {
		return nil, 0, domain.ErrCheckFailed
	}
	out := make([]JiraGuardedUpdateFieldProjection, len(prepared.Fields))
	var total int64
	for index, field := range prepared.Fields {
		if field.FieldID != selected[index] || !strictLowerSHA256(field.SHA256) {
			return nil, 0, domain.ErrCheckFailed
		}
		value, ok := prepared.Values[field.FieldID]
		if !ok {
			return nil, 0, domain.ErrCheckFailed
		}
		expectedValue, expected := input.expected[field.FieldID]
		if !expected || !exactGuardedUpdateValue(value, expectedValue) {
			return nil, 0, domain.ErrCheckFailed
		}
		kind, size, digest, projectionErr := canonicalGuardedFieldValue(value, domain.JiraGuardedUpdateMaxDesiredBytes-total)
		if projectionErr != nil || kind != field.JSONKind || size != field.Bytes || digest != field.SHA256 || field.InputKind != updateInputKind(input, field.FieldID) {
			return nil, 0, domain.ErrCheckFailed
		}
		total += int64(size)
		out[index] = JiraGuardedUpdateFieldProjection{FieldID: field.FieldID, InputKind: field.InputKind, JSONKind: field.JSONKind, Bytes: field.Bytes, SHA256: field.SHA256}
	}
	return out, int(total), nil
}

func normalizeGuardedUpdateFieldValue(input domain.JiraFieldInput) (any, error) {
	if !utf8.ValidString(input.Value) {
		return nil, domain.ErrUsage
	}
	if input.ExplicitJSON {
		return strictjson.DecodeValue([]byte(input.Value))
	}
	trimmed := strings.TrimSpace(input.Value)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		value, err := strictjson.DecodeValue([]byte(input.Value))
		if err != nil {
			return nil, err
		}
		switch value.(type) {
		case map[string]any, []any:
			return value, nil
		default:
			return nil, domain.ErrUsage
		}
	}
	return input.Value, nil
}

func exactGuardedUpdateValue(left, right any) bool {
	leftKind, leftBytes, leftHash, leftErr := canonicalGuardedFieldValue(left, domain.JiraGuardedUpdateMaxDesiredBytes)
	rightKind, rightBytes, rightHash, rightErr := canonicalGuardedFieldValue(right, domain.JiraGuardedUpdateMaxDesiredBytes)
	return leftErr == nil && rightErr == nil && leftKind == rightKind && leftBytes == rightBytes && leftHash == rightHash
}

func jiraGuardedUpdateProposalHash(result *JiraGuardedUpdateResult) (string, error) {
	canonical := struct {
		SchemaVersion int                                `json:"schema_version"`
		Operation     string                             `json:"operation"`
		Backend       string                             `json:"backend_sha256"`
		RequestedKey  string                             `json:"requested_key"`
		IssueID       string                             `json:"issue_id"`
		Key           string                             `json:"key"`
		Project       string                             `json:"project"`
		Updated       string                             `json:"updated"`
		Source        JiraGuardedUpdateSource            `json:"source"`
		Catalog       []JiraFieldCatalogProjection       `json:"catalog"`
		Current       []JiraFieldValueProjection         `json:"current"`
		Desired       []JiraGuardedUpdateFieldProjection `json:"desired"`
		Prepared      JiraFieldPayloadProjection         `json:"prepared"`
		Bounds        JiraGuardedUpdateBounds            `json:"bounds"`
	}{jiraGuardedUpdateSchemaVersion, "jira_issue_update", result.BackendSHA256, result.RequestedKey, result.IssueID, result.Key, result.Project, result.Updated, result.Source, result.Catalog, result.Current, result.Desired, result.Prepared, result.Bounds}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("canonicalize guarded Jira update proposal: %w", err)
	}
	return guardedProposalDigest(encoded), nil
}

func selectedUpdateFields(input jiraGuardedUpdateInput) []string {
	fields := append([]string(nil), input.selected...)
	if input.descriptionPresent {
		fields = append(fields, "description")
	}
	if input.summaryPresent {
		fields = append(fields, "summary")
	}
	sort.Strings(fields)
	return fields
}

func updatePreparationRequest(input jiraGuardedUpdateInput, qualified []domain.JiraGuardedFieldCatalogEntry) domain.JiraGuardedUpdatePreparationRequest {
	return domain.JiraGuardedUpdatePreparationRequest{
		Summary: input.summary, SummaryPresent: input.summaryPresent,
		Description: bytes.Clone(input.description), DescriptionPresent: input.descriptionPresent, DescriptionSource: input.descriptionSource,
		Fields: cloneJiraFieldInputs(input.fields), Qualified: append([]domain.JiraGuardedFieldCatalogEntry(nil), qualified...),
	}
}

func cloneJiraFieldInputs(values map[string]domain.JiraFieldInput) map[string]domain.JiraFieldInput {
	out := make(map[string]domain.JiraFieldInput, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneGuardedUpdatePreparation(prepared domain.JiraGuardedUpdatePreparation) domain.JiraGuardedUpdatePreparation {
	return domain.JiraGuardedUpdatePreparation{Payload: bytes.Clone(prepared.Payload), Fields: append([]domain.JiraGuardedUpdatePreparedField(nil), prepared.Fields...), Values: cloneGuardedFieldValues(prepared.Values)}
}

func optimisticUpdateQualification(fields []string) []domain.JiraGuardedFieldCatalogEntry {
	out := make([]domain.JiraGuardedFieldCatalogEntry, len(fields))
	for index, field := range fields {
		out[index] = domain.JiraGuardedFieldCatalogEntry{ID: field, Custom: true}
	}
	return out
}

func equalGuardedUpdateQualification(actual, expected []domain.JiraGuardedFieldCatalogEntry) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}

func validGuardedUpdateCatalog(catalog domain.JiraGuardedFieldCatalog, selected []string) bool {
	return catalog.Complete && len(catalog.Fields) <= domain.JiraGuardedUpdateMaxCatalogEntries && equalGuardedUpdateQualification(catalog.Fields, optimisticUpdateQualification(selected))
}

func projectGuardedUpdateCatalog(entries []domain.JiraGuardedFieldCatalogEntry) []JiraFieldCatalogProjection {
	out := make([]JiraFieldCatalogProjection, len(entries))
	for index, entry := range entries {
		out[index] = JiraFieldCatalogProjection{ID: entry.ID, Custom: entry.Custom}
	}
	return out
}

func guardedUpdateSatisfied(current map[string]domain.JiraGuardedFieldEvidence, desired map[string]any) bool {
	if len(current) != len(desired) {
		return false
	}
	for field, desiredValue := range desired {
		evidence, ok := current[field]
		if !ok || !evidence.Present {
			return false
		}
		currentKind, currentBytes, currentHash, currentErr := canonicalGuardedFieldValue(evidence.Value, domain.JiraGuardedUpdateMaxCurrentBytes)
		desiredKind, desiredBytes, desiredHash, desiredErr := canonicalGuardedFieldValue(desiredValue, domain.JiraGuardedUpdateMaxDesiredBytes)
		if currentErr != nil || desiredErr != nil || currentKind != desiredKind || currentBytes != desiredBytes || currentHash != desiredHash {
			return false
		}
	}
	return true
}

func updateInputKind(input jiraGuardedUpdateInput, field string) string {
	switch field {
	case "summary":
		return "summary"
	case "description":
		return input.descriptionSource
	default:
		if input.fields[field].ExplicitJSON {
			return "explicit_json"
		}
		return "legacy"
	}
}

func digestGuardedUpdateSource(kind string, value []byte) JiraGuardedUpdateSource {
	sum := sha256.Sum256(value)
	return JiraGuardedUpdateSource{Kind: kind, Present: true, Bytes: len(value), SHA256: hex.EncodeToString(sum[:])}
}

func newJiraGuardedUpdateResult(key string, input jiraGuardedUpdateInput, apply bool) *JiraGuardedUpdateResult {
	mode := "dry-run"
	if apply {
		mode = "apply"
	}
	return &JiraGuardedUpdateResult{
		SchemaVersion: jiraGuardedUpdateSchemaVersion, Operation: "jira_issue_update", Mode: mode, Status: "blocked",
		RequestedKey: key, Key: key, Source: input.source, Catalog: []JiraFieldCatalogProjection{}, Current: []JiraFieldValueProjection{}, Desired: []JiraGuardedUpdateFieldProjection{},
		Usage: JiraGuardedUpdateUsage{InputBytes: int(input.inputBytes)},
		Bounds: JiraGuardedUpdateBounds{
			MaxFields: domain.JiraGuardedUpdateMaxFields, MaxCatalogEntries: domain.JiraGuardedUpdateMaxCatalogEntries,
			MaxFieldIDBytes: domain.JiraGuardedUpdateMaxFieldIDBytes, MaxRequestedKeyBytes: domain.JiraGuardedUpdateMaxRequestedKeyBytes,
			MaxImmutableIDBytes: domain.JiraGuardedUpdateMaxImmutableIDBytes, MaxInputBytes: domain.JiraGuardedUpdateMaxInputBytes,
			MaxDesiredCanonicalBytes: domain.JiraGuardedUpdateMaxDesiredBytes, MaxCurrentCanonicalBytes: domain.JiraGuardedUpdateMaxCurrentBytes,
			MaxPreparedBytes: domain.JiraGuardedUpdateMaxPreparedBytes, MaxCatalogResponseBytes: domain.JiraGuardedUpdateMaxCatalogResponseBytes,
			MaxIssueResponseBytes: domain.JiraGuardedUpdateMaxIssueResponseBytes, MaxWriteResponseBytes: domain.JiraGuardedUpdateMaxWriteResponseBytes,
			MaxQueryAndPathBytes: domain.JiraGuardedUpdateMaxQueryAndPathBytes, PreviewMaxRequests: domain.JiraGuardedUpdatePreviewRequests,
			ApplyMaxRequests: domain.JiraGuardedUpdateApplyRequests, PreviewMaxResponseBytes: domain.JiraGuardedUpdatePreviewResponseBytes,
			ApplyMaxResponseBytes: domain.JiraGuardedUpdateApplyResponseBytes, DeadlineMillis: domain.JiraGuardedUpdateDeadlineMillis,
		},
	}
}
