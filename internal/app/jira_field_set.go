package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

const JiraFieldSetValueCap = int(domain.JiraGuardedFieldMaxInputBytes)

type JiraFieldProposal struct {
	Field      string
	Value      any
	Source     string // raw|markdown
	InputBytes int
}

type JiraFieldSetOpts struct {
	Proposals            []JiraFieldProposal
	AllowFields          []string
	ExpectedUpdated      string
	ExpectedProposalHash string
	Apply                bool
}

type JiraFieldValueProjection struct {
	Field   string `json:"field"`
	Present bool   `json:"present"`
	Kind    string `json:"kind"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
}

type JiraFieldPayloadProjection struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type JiraFieldCatalogProjection struct {
	ID     string `json:"id"`
	Custom bool   `json:"custom"`
}

type JiraFieldSetBounds struct {
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

type JiraFieldSetUsage struct {
	Requests              int   `json:"requests"`
	ResponseBytes         int64 `json:"response_bytes"`
	InputBytes            int   `json:"input_bytes"`
	DesiredCanonicalBytes int   `json:"desired_canonical_bytes"`
	CurrentCanonicalBytes int   `json:"current_canonical_bytes"`
}

type JiraFieldSetResult struct {
	SchemaVersion   int                          `json:"schema_version"`
	Operation       string                       `json:"operation"`
	BackendSHA256   string                       `json:"backend_sha256"`
	RequestedKey    string                       `json:"requested_key"`
	IssueID         string                       `json:"issue_id"`
	Key             string                       `json:"key"`
	Project         string                       `json:"project"`
	Mode            string                       `json:"mode"`
	Status          string                       `json:"status"`
	ExpectedUpdated string                       `json:"expected_updated"`
	ActualUpdated   string                       `json:"actual_updated"`
	ProposalHash    string                       `json:"proposal_hash"`
	Catalog         []JiraFieldCatalogProjection `json:"catalog"`
	Current         []JiraFieldValueProjection   `json:"current"`
	Readback        []JiraFieldValueProjection   `json:"readback,omitempty"`
	Prepared        JiraFieldPayloadProjection   `json:"prepared"`
	Bounds          JiraFieldSetBounds           `json:"bounds"`
	Usage           JiraFieldSetUsage            `json:"usage"`
	WriteAttempted  bool                         `json:"write_attempted"`
	Reconciled      bool                         `json:"reconciled"`
	Complete        bool                         `json:"complete"`
	Fields          []JiraFieldSetPreview        `json:"fields"`
}

type JiraFieldSetPreview struct {
	Field  string `json:"field"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	Value  any    `json:"value"`
}

type jiraFieldWriteError struct {
	message   string
	cause     error
	ambiguous bool
}

func (e *jiraFieldWriteError) Error() string                  { return definitiveWriteMessage(e.message, e.cause) }
func (e *jiraFieldWriteError) Unwrap() error                  { return e.cause }
func (e *jiraFieldWriteError) DiagnosticAmbiguousWrite() bool { return e != nil && e.ambiguous }
func sanitizedFieldWriteError(message string, cause error, ambiguous bool) error {
	return &jiraFieldWriteError{message: message, cause: cause, ambiguous: ambiguous}
}

type jiraGuardedFieldError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *jiraGuardedFieldError) Error() string { return e.message }
func (e *jiraGuardedFieldError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}
func (e *jiraGuardedFieldError) DiagnosticAmbiguousWrite() bool { return e != nil && e.ambiguous }
func (e *jiraGuardedFieldError) DiagnosticTerminalCheckFailure() bool {
	return e != nil && e.closed
}

func guardedFieldFailure(message string, cause error, closed, ambiguous bool) error {
	return &jiraGuardedFieldError{message: message, cause: sanitizeRemoteWriteCause(cause), closed: closed, ambiguous: ambiguous}
}

type jiraGuardedFieldSnapshot struct {
	result      *JiraFieldSetResult
	catalog     domain.JiraGuardedFieldCatalog
	issue       domain.JiraGuardedFieldIssue
	prepared    domain.JiraGuardedFieldPreparation
	values      map[string]any
	updatedTime time.Time
}

// SetFieldsGuarded previews or applies one atomic, catalog-qualified custom-
// field update. Every request shares one deadline and aggregate response
// budget, and every actual PUT receives exactly one immutable-id readback.
func (s *JiraService) SetFieldsGuarded(ctx context.Context, requestedKey string, opts JiraFieldSetOpts) (*JiraFieldSetResult, error) {
	requestedKey, err := ValidateJiraGuardedFieldKey(requestedKey)
	if err != nil {
		return nil, err
	}
	proposals, values, allowlist, inputBytes, err := normalizeGuardedFieldInputs(opts.Proposals, opts.AllowFields)
	if err != nil {
		return nil, err
	}
	opts.ExpectedUpdated = strings.TrimSpace(opts.ExpectedUpdated)
	opts.ExpectedProposalHash = strings.TrimSpace(opts.ExpectedProposalHash)
	if !opts.Apply {
		if opts.ExpectedUpdated != "" || opts.ExpectedProposalHash != "" {
			return nil, fmt.Errorf("%w: reviewed field markers require --apply", domain.ErrUsage)
		}
	} else {
		if opts.ExpectedUpdated == "" {
			return nil, fmt.Errorf("%w: --expected-updated is required with --apply; run the dry-run first to capture it", domain.ErrUsage)
		}
		if err := ValidateJiraDescriptionEditReviewHash(opts.ExpectedProposalHash); err != nil {
			return nil, err
		}
	}
	result := newJiraFieldSetResult(requestedKey, opts.Apply)
	result.Usage.InputBytes = inputBytes
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return result, guardedFieldFailure("guarded Jira field backend identity is invalid", domain.ErrCheckFailed, true, false)
	}
	result.BackendSHA256 = backendHash
	port, ok := s.tr.(domain.JiraGuardedFieldPort)
	if !ok {
		return result, guardedFieldFailure("guarded Jira fields are unavailable", domain.ErrConfig, true, false)
	}
	maxRequests, maxResponses := domain.JiraGuardedFieldPreviewMaxRequests, domain.JiraGuardedFieldPreviewMaxResponseBytes
	if opts.Apply {
		maxRequests, maxResponses = domain.JiraGuardedFieldApplyMaxRequests, domain.JiraGuardedFieldApplyMaxResponseBytes
	}
	budget, err := domain.NewReadBudget(maxRequests, maxResponses)
	if err != nil {
		return result, guardedFieldFailure("guarded Jira field budget is invalid", err, true, false)
	}
	workflowCtx, cancel := context.WithTimeout(ctx, time.Duration(domain.JiraGuardedFieldDeadlineMillis)*time.Millisecond)
	defer cancel()
	deadline, _ := workflowCtx.Deadline()
	workflowCtx = domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(workflowCtx, budget)))
	defer func() {
		usage := budget.Usage()
		result.Usage.Requests, result.Usage.ResponseBytes = usage.Attempts, usage.ResponseBytes
	}()

	initial, err := s.buildGuardedFieldSnapshot(workflowCtx, port, requestedKey, requestedKey, "", proposals, values, allowlist, backendHash, opts.Apply, inputBytes)
	if err != nil {
		return result, guardedFieldFailure("guarded Jira field proposal qualification failed", err, true, false)
	}
	result = initial.result
	if opts.Apply {
		// Preserve the caller-reviewed marker for audit even when the current
		// backend marker differs. The proposal hash continues to bind actual.
		result.ExpectedUpdated = opts.ExpectedUpdated
	}
	if err := workflowCtx.Err(); err != nil {
		result.Status, result.Complete = "blocked", false
		return result, guardedFieldFailure("guarded Jira field deadline expired during proposal qualification", err, true, false)
	}
	if opts.Apply && opts.ExpectedProposalHash != result.ProposalHash {
		result.Status = "blocked"
		return result, guardedFieldFailure("guarded Jira field proposal changed since review", domain.ErrCheckFailed, true, false)
	}
	if opts.Apply && opts.ExpectedUpdated != result.ActualUpdated {
		result.Status = "blocked"
		return result, guardedFieldFailure("guarded Jira field updated marker changed since review", domain.ErrCheckFailed, true, false)
	}
	if guardedFieldValuesSatisfied(initial.issue.Fields, initial.values) {
		result.Status = "already_satisfied"
		return result, nil
	}
	result.Status = "would_apply"
	if !opts.Apply {
		return result, nil
	}

	prewrite, err := s.buildGuardedFieldSnapshot(workflowCtx, port, initial.issue.ID, requestedKey, initial.issue.ID, proposals, values, allowlist, backendHash, true, inputBytes)
	if err != nil {
		result.Status, result.Complete = "blocked", false
		return result, guardedFieldFailure("guarded Jira field proposal could not be qualified immediately before dispatch", err, true, false)
	}
	if prewrite.result.ProposalHash != result.ProposalHash {
		result.Status = "blocked"
		return result, guardedFieldFailure("guarded Jira field proposal changed immediately before dispatch", domain.ErrCheckFailed, true, false)
	}
	if err := workflowCtx.Err(); err != nil {
		result.Status = "blocked"
		return result, guardedFieldFailure("guarded Jira field deadline expired before dispatch", err, true, false)
	}

	result.WriteAttempted = true
	writeErr := port.WriteGuardedFields(workflowCtx, domain.JiraGuardedFieldWrite{
		ID: prewrite.issue.ID, Key: prewrite.issue.Key, Project: prewrite.issue.Project,
		Qualified: append([]domain.JiraGuardedFieldCatalogEntry(nil), prewrite.catalog.Fields...),
		Prepared:  cloneGuardedFieldPreparation(prewrite.prepared),
	})
	if writeDefinitelyNotAttempted(writeErr) {
		result.WriteAttempted = false
		result.Status = "blocked"
		return result, guardedFieldFailure("guarded Jira field write was refused before dispatch", writeErr, true, false)
	}
	definitive := writeErr != nil && definitiveWriteRejection(writeErr)
	closeout, closeCancel := context.WithDeadline(context.WithoutCancel(workflowCtx), deadline)
	defer closeCancel()
	remainingCurrent := domain.JiraGuardedFieldMaxCurrentBytes - int64(result.Usage.CurrentCanonicalBytes)
	readback, readErr := s.readGuardedFieldIssue(closeout, port, prewrite.issue.ID, requestedKey, prewrite.issue.ID, selectedFieldIDs(proposals), remainingCurrent)
	if readErr != nil || closeout.Err() != nil {
		result.Complete, result.Reconciled = false, false
		if definitive {
			result.Status = "failed"
			return result, guardedFieldFailure("Jira definitively rejected the guarded field update and readback was unavailable", writeErr, false, false)
		}
		result.Status = "unknown"
		return result, guardedFieldFailure("guarded Jira field outcome is unknown; do not replay automatically", nil, false, true)
	}
	result.Reconciled, result.Complete = true, true
	result.ActualUpdated = readback.issue.Updated
	result.Readback = readback.result.Current
	result.Usage.CurrentCanonicalBytes += readback.result.Usage.CurrentCanonicalBytes
	satisfied := guardedFieldValuesSatisfied(readback.issue.Fields, prewrite.values)
	advanced := readback.updatedTime.After(prewrite.updatedTime)
	if definitive {
		if satisfied {
			result.Status = "already_satisfied"
			return result, nil
		}
		result.Status = "failed"
		return result, guardedFieldFailure("Jira definitively rejected the guarded field update", writeErr, false, false)
	}
	if satisfied && advanced {
		result.Status = "applied"
		return result, nil
	}
	result.Status = "unknown"
	return result, guardedFieldFailure("guarded Jira field readback did not prove a satisfying advancing end state; do not replay automatically", nil, false, true)
}

func ValidateJiraGuardedFieldKey(value string) (string, error) {
	key := strings.ToUpper(strings.TrimSpace(value))
	if key == "" || len(key) > domain.JiraGuardedFieldMaxRequestedKeyBytes || !domain.ValidJiraIssueKey(key) {
		return "", fmt.Errorf("%w: issue key must be canonical and at most %d bytes (for example PROJ-1)", domain.ErrUsage, domain.JiraGuardedFieldMaxRequestedKeyBytes)
	}
	return key, nil
}

func normalizeGuardedFieldInputs(input []JiraFieldProposal, allowInput []string) ([]JiraFieldProposal, map[string]any, []string, int, error) {
	if len(input) == 0 || len(input) > domain.JiraGuardedFieldMaxSelected {
		return nil, nil, nil, 0, fmt.Errorf("%w: between 1 and %d field inputs are required", domain.ErrUsage, domain.JiraGuardedFieldMaxSelected)
	}
	if len(allowInput) == 0 || len(allowInput) > domain.JiraGuardedFieldMaxAllowlist {
		return nil, nil, nil, 0, fmt.Errorf("%w: --allow-fields requires between 1 and %d entries", domain.ErrUsage, domain.JiraGuardedFieldMaxAllowlist)
	}
	allow := make(map[string]bool, len(allowInput))
	allowlist := make([]string, 0, len(allowInput))
	for _, raw := range allowInput {
		field := strings.TrimSpace(raw)
		if !domain.ValidJiraGuardedFieldID(field) || domain.JiraGuardedFieldReserved(field) {
			return nil, nil, nil, 0, fmt.Errorf("%w: --allow-fields contains an invalid or reserved field id", domain.ErrUsage)
		}
		if allow[field] {
			return nil, nil, nil, 0, fmt.Errorf("%w: --allow-fields contains duplicate field %q", domain.ErrUsage, field)
		}
		allow[field] = true
		allowlist = append(allowlist, field)
	}
	seen := make(map[string]bool, len(input))
	values := make(map[string]any, len(input))
	proposals := make([]JiraFieldProposal, 0, len(input))
	totalInput := 0
	for _, proposal := range input {
		field := strings.TrimSpace(proposal.Field)
		if !domain.ValidJiraGuardedFieldID(field) || domain.JiraGuardedFieldReserved(field) {
			return nil, nil, nil, 0, fmt.Errorf("%w: field id is invalid or reserved", domain.ErrUsage)
		}
		if !allow[field] {
			return nil, nil, nil, 0, fmt.Errorf("%w: field %q is not in --allow-fields", domain.ErrUsage, field)
		}
		if seen[field] {
			return nil, nil, nil, 0, fmt.Errorf("%w: duplicate input for field %q", domain.ErrUsage, field)
		}
		if proposal.Source != "raw" && proposal.Source != "markdown" {
			return nil, nil, nil, 0, fmt.Errorf("%w: field %q has invalid source", domain.ErrUsage, field)
		}
		if proposal.InputBytes < 0 {
			return nil, nil, nil, 0, fmt.Errorf("%w: field %q has invalid input size", domain.ErrUsage, field)
		}
		totalInput += proposal.InputBytes
		if int64(totalInput) > domain.JiraGuardedFieldMaxInputBytes {
			return nil, nil, nil, 0, fmt.Errorf("%w: field input exceeds the 64 MiB aggregate limit", domain.ErrUsage)
		}
		switch proposal.Value.(type) {
		case string:
		case map[string]any, []any:
			if proposal.Source == "markdown" {
				return nil, nil, nil, 0, fmt.Errorf("%w: Markdown field %q must be a string", domain.ErrUsage, field)
			}
		default:
			return nil, nil, nil, 0, fmt.Errorf("%w: field %q value must be a string, object, or array", domain.ErrUsage, field)
		}
		if !domain.JiraGuardedFieldValueWithinNestingBound(proposal.Value) {
			return nil, nil, nil, 0, fmt.Errorf("%w: guarded Jira field value exceeds the supported nesting bound", domain.ErrUsage)
		}
		proposal.Field = field
		seen[field], values[field] = true, proposal.Value
		proposals = append(proposals, proposal)
	}
	sort.Slice(proposals, func(i, j int) bool { return proposals[i].Field < proposals[j].Field })
	sort.Strings(allowlist)
	return proposals, values, allowlist, totalInput, nil
}

func (s *JiraService) buildGuardedFieldSnapshot(ctx context.Context, port domain.JiraGuardedFieldPort, reference, requestedKey, expectedID string, proposals []JiraFieldProposal, values map[string]any, _ []string, backendHash string, apply bool, inputBytes int) (*jiraGuardedFieldSnapshot, error) {
	selected := selectedFieldIDs(proposals)
	catalog, err := port.ReadGuardedFieldCatalog(ctx, selected)
	if err != nil {
		return nil, err
	}
	if !validGuardedFieldCatalog(catalog, selected) {
		return nil, fmt.Errorf("%w: Jira returned incomplete custom-field qualification", domain.ErrCheckFailed)
	}
	issueRead, err := s.readGuardedFieldIssue(ctx, port, reference, requestedKey, expectedID, selected, domain.JiraGuardedFieldMaxCurrentBytes)
	if err != nil {
		return nil, err
	}
	prepared, err := port.PrepareGuardedFields(domain.JiraGuardedFieldPreparationRequest{Values: cloneGuardedFieldValues(values), Qualified: append([]domain.JiraGuardedFieldCatalogEntry(nil), catalog.Fields...)})
	if err != nil {
		return nil, err
	}
	prepared = cloneGuardedFieldPreparation(prepared)
	previews, desiredBytes, err := validateGuardedFieldPreparation(proposals, values, catalog.Fields, prepared)
	if err != nil || int64(desiredBytes) > domain.JiraGuardedFieldMaxDesiredBytes {
		return nil, fmt.Errorf("%w: guarded Jira desired field evidence is incomplete or oversized", domain.ErrCheckFailed)
	}
	result := newJiraFieldSetResult(requestedKey, apply)
	result.BackendSHA256, result.IssueID, result.Key, result.Project = backendHash, issueRead.issue.ID, issueRead.issue.Key, issueRead.issue.Project
	result.ActualUpdated, result.ExpectedUpdated = issueRead.issue.Updated, issueRead.issue.Updated
	result.Fields, result.Current = previews, issueRead.result.Current
	result.Usage = JiraFieldSetUsage{InputBytes: inputBytes, DesiredCanonicalBytes: desiredBytes, CurrentCanonicalBytes: issueRead.result.Usage.CurrentCanonicalBytes}
	result.Catalog = make([]JiraFieldCatalogProjection, len(catalog.Fields))
	for index, entry := range catalog.Fields {
		result.Catalog[index] = JiraFieldCatalogProjection{ID: entry.ID, Custom: entry.Custom}
	}
	payloadSum := sha256.Sum256(prepared.Payload)
	result.Prepared = JiraFieldPayloadProjection{Bytes: len(prepared.Payload), SHA256: hex.EncodeToString(payloadSum[:])}
	result.Complete, result.Status = true, "would_apply"
	result.ProposalHash, err = jiraFieldProposalHash(result)
	if err != nil {
		return nil, err
	}
	return &jiraGuardedFieldSnapshot{result: result, catalog: catalog, issue: issueRead.issue, prepared: cloneGuardedFieldPreparation(prepared), values: cloneGuardedFieldValues(values), updatedTime: issueRead.updatedTime}, nil
}

func (s *JiraService) readGuardedFieldIssue(ctx context.Context, port domain.JiraGuardedFieldPort, reference, requestedKey, expectedID string, selected []string, maximumCurrent int64) (*jiraGuardedFieldSnapshot, error) {
	issue, err := port.ReadGuardedFieldIssue(ctx, reference, selected)
	if err != nil {
		return nil, err
	}
	if !issue.Complete || !canonicalPositiveNumericString(issue.ID) || len(issue.ID) > domain.JiraGuardedFieldMaxImmutableIDBytes ||
		issue.Key != requestedKey || !domain.ValidJiraIssueKey(issue.Key) || !domain.ValidJiraIssueKey(issue.Project+"-1") ||
		!strings.HasPrefix(issue.Key, issue.Project+"-") || expectedID != "" && issue.ID != expectedID || len(issue.Fields) != len(selected) {
		return nil, fmt.Errorf("%w: Jira returned missing, moved, or malformed guarded field identity", domain.ErrCheckFailed)
	}
	updatedTime, err := parseJiraUpdatedTime(issue.Updated)
	if err != nil || strings.TrimSpace(issue.Updated) != issue.Updated {
		return nil, fmt.Errorf("%w: Jira returned an unsupported updated marker", domain.ErrCheckFailed)
	}
	current, currentBytes, err := guardedFieldEvidenceProjections(issue.Fields, selected, maximumCurrent)
	if err != nil {
		return nil, err
	}
	return &jiraGuardedFieldSnapshot{issue: issue, updatedTime: updatedTime, result: &JiraFieldSetResult{Current: current, Usage: JiraFieldSetUsage{CurrentCanonicalBytes: currentBytes}}}, nil
}

// jiraFieldProposalHash binds the complete qualified schema-v3 proposal while
// excluding mode and post-write outcome state.
func jiraFieldProposalHash(result *JiraFieldSetResult) (string, error) {
	encoded, err := json.Marshal(jiraFieldProposalCanonicalValue(result))
	if err != nil {
		return "", fmt.Errorf("canonicalize guarded Jira field proposal: %w", err)
	}
	return guardedProposalDigest(encoded), nil
}

type jiraFieldDesiredHashProjection struct {
	Field   string `json:"field"`
	Source  string `json:"source"`
	Present bool   `json:"present"`
	Kind    string `json:"kind"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
}

type jiraFieldProposalCanonical struct {
	SchemaVersion   int                              `json:"schema_version"`
	Operation       string                           `json:"operation"`
	BackendSHA256   string                           `json:"backend_sha256"`
	RequestedKey    string                           `json:"requested_key"`
	IssueID         string                           `json:"issue_id"`
	Key             string                           `json:"key"`
	Project         string                           `json:"project"`
	Updated         string                           `json:"updated"`
	CatalogEndpoint string                           `json:"catalog_endpoint"`
	CatalogComplete bool                             `json:"catalog_complete"`
	Catalog         []JiraFieldCatalogProjection     `json:"catalog"`
	Current         []JiraFieldValueProjection       `json:"current"`
	Desired         []jiraFieldDesiredHashProjection `json:"desired"`
	Prepared        JiraFieldPayloadProjection       `json:"prepared"`
	Bounds          JiraFieldSetBounds               `json:"bounds"`
}

func jiraFieldProposalCanonicalValue(result *JiraFieldSetResult) jiraFieldProposalCanonical {
	desired := make([]jiraFieldDesiredHashProjection, len(result.Fields))
	for index, field := range result.Fields {
		desired[index] = jiraFieldDesiredHashProjection{Field: field.Field, Source: field.Source, Present: true, Kind: field.Kind, Bytes: field.Bytes, SHA256: field.SHA256}
	}
	return jiraFieldProposalCanonical{
		SchemaVersion: 3, Operation: "jira_issue_field_set", BackendSHA256: result.BackendSHA256,
		RequestedKey: result.RequestedKey, IssueID: result.IssueID, Key: result.Key, Project: result.Project, Updated: result.ActualUpdated,
		CatalogEndpoint: "/rest/api/2/field", CatalogComplete: result.Complete, Catalog: result.Catalog,
		Current: result.Current, Desired: desired, Prepared: result.Prepared, Bounds: result.Bounds,
	}
}

func jiraFieldProposalPreimage(result *JiraFieldSetResult) ([]byte, error) {
	encoded, err := json.Marshal(jiraFieldProposalCanonicalValue(result))
	if err != nil {
		return nil, fmt.Errorf("canonicalize guarded Jira field proposal: %w", err)
	}
	return encoded, nil
}

func validateGuardedFieldPreparation(proposals []JiraFieldProposal, values map[string]any, qualified []domain.JiraGuardedFieldCatalogEntry, prepared domain.JiraGuardedFieldPreparation) ([]JiraFieldSetPreview, int, error) {
	if len(proposals) == 0 || len(proposals) != len(prepared.Fields) || len(proposals) != len(qualified) || len(values) != len(proposals) ||
		len(prepared.Payload) == 0 || int64(len(prepared.Payload)) > domain.JiraGuardedFieldMaxPreparedBytes {
		return nil, 0, domain.ErrCheckFailed
	}
	expectedPayload, err := json.Marshal(map[string]any{"fields": values})
	if err != nil || !bytes.Equal(expectedPayload, prepared.Payload) {
		return nil, 0, domain.ErrCheckFailed
	}
	out := make([]JiraFieldSetPreview, len(proposals))
	var total int64
	for index, proposal := range proposals {
		projection := prepared.Fields[index]
		value, present := values[proposal.Field]
		if !present || qualified[index].ID != proposal.Field || !qualified[index].Custom || projection.FieldID != proposal.Field ||
			!strictLowerSHA256(projection.SHA256) {
			return nil, 0, domain.ErrCheckFailed
		}
		kind, valueBytes, digest, projectionErr := guardedDesiredFieldProjection(value, domain.JiraGuardedFieldMaxDesiredBytes-total)
		if projectionErr != nil || projection.Kind != kind || projection.Bytes != valueBytes || projection.SHA256 != digest {
			return nil, 0, domain.ErrCheckFailed
		}
		total += int64(valueBytes)
		if total > domain.JiraGuardedFieldMaxDesiredBytes {
			return nil, 0, domain.ErrCheckFailed
		}
		out[index] = JiraFieldSetPreview{Field: projection.FieldID, Source: proposal.Source, Kind: projection.Kind, Bytes: projection.Bytes, SHA256: projection.SHA256, Value: proposal.Value}
	}
	return out, int(total), nil
}

func guardedDesiredFieldProjection(value any, maximum int64) (string, int, string, error) {
	if text, ok := value.(string); ok {
		if maximum < 0 || int64(len(text)) > maximum {
			return "", 0, "", domain.ErrCheckFailed
		}
		sum := sha256.Sum256([]byte(text))
		return "string", len(text), hex.EncodeToString(sum[:]), nil
	}
	kind, size, digest, err := canonicalGuardedFieldValue(value, maximum)
	if err != nil || kind != "object" && kind != "array" {
		return "", 0, "", domain.ErrCheckFailed
	}
	return kind, size, digest, nil
}

func strictLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' {
			if current < 'a' || current > 'f' {
				return false
			}
		}
	}
	return true
}

func validGuardedFieldCatalog(catalog domain.JiraGuardedFieldCatalog, selected []string) bool {
	if !catalog.Complete || len(catalog.Fields) != len(selected) || len(catalog.Fields) == 0 {
		return false
	}
	for index, entry := range catalog.Fields {
		if !entry.Custom || entry.ID != selected[index] || !domain.ValidJiraGuardedFieldID(entry.ID) || domain.JiraGuardedFieldReserved(entry.ID) {
			return false
		}
	}
	return true
}

func selectedFieldIDs(proposals []JiraFieldProposal) []string {
	out := make([]string, len(proposals))
	for index, proposal := range proposals {
		out[index] = proposal.Field
	}
	return out
}

func cloneGuardedFieldValues(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for field, value := range values {
		out[field] = cloneGuardedFieldValue(value)
	}
	return out
}

func cloneGuardedFieldValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, member := range typed {
			out[key] = cloneGuardedFieldValue(member)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, member := range typed {
			out[index] = cloneGuardedFieldValue(member)
		}
		return out
	default:
		return value
	}
}

func cloneGuardedFieldPreparation(prepared domain.JiraGuardedFieldPreparation) domain.JiraGuardedFieldPreparation {
	return domain.JiraGuardedFieldPreparation{Payload: append([]byte(nil), prepared.Payload...), Fields: append([]domain.JiraGuardedFieldPreparedProjection(nil), prepared.Fields...)}
}

func guardedFieldValuesSatisfied(current map[string]domain.JiraGuardedFieldEvidence, desired map[string]any) bool {
	for field, value := range desired {
		evidence, present := current[field]
		if !present || !evidence.Present || !jiraFieldProposalEqual(evidence.Value, value) {
			return false
		}
	}
	return true
}

func jiraFieldProposalEqual(current, desired any) bool {
	if desiredString, ok := desired.(string); ok {
		currentString, ok := current.(string)
		return ok && currentString == desiredString
	}
	return planValueContains(current, desired)
}

func newJiraFieldSetResult(requestedKey string, apply bool) *JiraFieldSetResult {
	mode := "dry-run"
	if apply {
		mode = "apply"
	}
	return &JiraFieldSetResult{
		SchemaVersion: 3, Operation: "jira_issue_field_set", RequestedKey: requestedKey, Key: requestedKey,
		Mode: mode, Status: "blocked", Catalog: []JiraFieldCatalogProjection{}, Current: []JiraFieldValueProjection{}, Fields: []JiraFieldSetPreview{},
		Bounds: JiraFieldSetBounds{
			MaxCatalogEntries: domain.JiraGuardedFieldMaxCatalogEntries, MaxSelectedFields: domain.JiraGuardedFieldMaxSelected,
			MaxAllowlistEntries: domain.JiraGuardedFieldMaxAllowlist, MaxFieldIDBytes: domain.JiraGuardedFieldMaxIDBytes,
			MaxRequestedKeyBytes: domain.JiraGuardedFieldMaxRequestedKeyBytes, MaxImmutableIDBytes: domain.JiraGuardedFieldMaxImmutableIDBytes,
			MaxJSONNestingDepth: domain.JiraGuardedFieldMaxJSONNestingDepth, MaxValueNestingDepth: domain.JiraGuardedFieldMaxValueNestingDepth,
			MaxCatalogResponseBytes: domain.JiraGuardedFieldMaxCatalogResponseBytes, MaxIssueResponseBytes: domain.JiraGuardedFieldMaxIssueResponseBytes,
			MaxInputBytes: domain.JiraGuardedFieldMaxInputBytes, MaxDesiredCanonicalBytes: domain.JiraGuardedFieldMaxDesiredBytes,
			MaxCurrentCanonicalBytes: domain.JiraGuardedFieldMaxCurrentBytes, MaxPreparedBytes: domain.JiraGuardedFieldMaxPreparedBytes,
			MaxQueryAndPathBytes: domain.JiraGuardedFieldMaxQueryAndPathBytes, MaxWriteResponseBytes: domain.JiraGuardedFieldMaxWriteResponseBytes,
			PreviewMaxRequests: domain.JiraGuardedFieldPreviewMaxRequests, ApplyMaxRequests: domain.JiraGuardedFieldApplyMaxRequests,
			PreviewMaxAggregateResponse: domain.JiraGuardedFieldPreviewMaxResponseBytes, ApplyMaxAggregateResponse: domain.JiraGuardedFieldApplyMaxResponseBytes,
			DeadlineMillis: domain.JiraGuardedFieldDeadlineMillis,
		},
	}
}
