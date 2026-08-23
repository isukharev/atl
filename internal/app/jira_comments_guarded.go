package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

const (
	JiraCommentBodyMaxBytes = 1 << 20

	jiraGuardedCommentSchemaVersion         = 1
	jiraGuardedCommentMaxKeyBytes           = 64
	jiraGuardedCommentMaxInventoryBytes     = int64(16 << 20)
	jiraGuardedCommentMaxResponseBytes      = int64(16 << 20)
	jiraGuardedCommentPreviewMaxRequests    = 102
	jiraGuardedCommentApplyMaxRequests      = 306
	jiraGuardedCommentDeadline              = 60 * time.Second
	jiraCommentSatisfactionAppendAlways     = "append_always"
	jiraCommentSatisfactionExactBodyPresent = "exact_body_present"
)

type JiraCommentAddOpts struct {
	Body                 []byte
	Apply                bool
	ExpectedProposalHash string
	// SatisfactionPolicy is an app-only seam for the future guarded CSV owner.
	// The direct CLI always supplies append_always.
	SatisfactionPolicy string
}

type JiraCommentDigest struct {
	Count  int    `json:"count"`
	SHA256 string `json:"sha256"`
}

type JiraCommentBounds struct {
	MaxKeyBytes               int   `json:"max_key_bytes"`
	MaxBodyBytes              int   `json:"max_body_bytes"`
	MaxEvidenceIDBytes        int   `json:"max_evidence_id_bytes"`
	MaxEvidenceMetadataBytes  int   `json:"max_evidence_metadata_bytes"`
	MaxPages                  int   `json:"max_pages"`
	MaxItems                  int   `json:"max_items"`
	MaxInventoryBytes         int64 `json:"max_inventory_bytes"`
	PreviewMaxRequests        int   `json:"preview_max_requests"`
	ApplyMaxRequests          int   `json:"apply_max_requests"`
	MaxAggregateResponseBytes int64 `json:"max_aggregate_response_bytes"`
	DeadlineMillis            int64 `json:"deadline_millis"`
}

type JiraCommentUsage struct {
	Requests      int   `json:"requests"`
	ResponseBytes int64 `json:"response_bytes"`
}

// JiraCommentAddResult is a content-minimized proposal and closeout record.
// It never emits native comment bytes, actor values, baseline identities, or
// backend response detail.
type JiraCommentAddResult struct {
	SchemaVersion      int               `json:"schema_version"`
	Operation          string            `json:"operation"`
	SatisfactionPolicy string            `json:"satisfaction_policy"`
	BackendSHA256      string            `json:"backend_sha256"`
	RequestedKey       string            `json:"requested_key"`
	IssueID            string            `json:"issue_id"`
	Key                string            `json:"key"`
	Project            string            `json:"project"`
	Updated            string            `json:"updated"`
	ReadbackUpdated    string            `json:"readback_updated,omitempty"`
	BodySHA256         string            `json:"body_sha256"`
	BodyBytes          int               `json:"body_bytes"`
	ActorSHA256        string            `json:"actor_sha256"`
	CurrentCount       int               `json:"current_count"`
	BaselineSHA256     string            `json:"baseline_sha256"`
	ExactBodyCount     int               `json:"exact_body_count"`
	Bounds             JiraCommentBounds `json:"bounds"`
	Usage              JiraCommentUsage  `json:"usage"`
	Mode               string            `json:"mode"`
	Status             string            `json:"status"`
	ProposalHash       string            `json:"proposal_hash,omitempty"`
	CommentID          string            `json:"comment_id,omitempty"`
	WriteAttempted     bool              `json:"write_attempted"`
	Reconciled         bool              `json:"reconciled"`
	Complete           bool              `json:"complete"`
}

type jiraGuardedCommentRecord struct {
	ID         string `json:"id"`
	AuthorName string `json:"author_name"`
	AuthorKey  string `json:"author_key"`
	Created    string `json:"created"`
	Updated    string `json:"updated"`
	ParentID   string `json:"parent_id"`
	Body       string `json:"body"`
}

type jiraGuardedCommentSnapshot struct {
	result      *JiraCommentAddResult
	issue       domain.JiraGuardedCommentIssue
	actor       domain.JiraGuardedCommentActor
	records     []jiraGuardedCommentRecord
	updatedTime time.Time
	body        []byte
}

type jiraCommentWriteError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *jiraCommentWriteError) Error() string {
	if e == nil {
		return "guarded Jira comment failed"
	}
	return definitiveWriteMessage(e.message, e.cause)
}
func (e *jiraCommentWriteError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}
func (e *jiraCommentWriteError) DiagnosticAmbiguousWrite() bool { return e != nil && e.ambiguous }
func (e *jiraCommentWriteError) DiagnosticTerminalCheckFailure() bool {
	return e != nil && e.closed
}

// AddCommentGuarded previews or applies one native Jira-wiki append. Every
// physical request shares one deadline and aggregate transport budget; the
// write is never replayed.
func (s *JiraService) AddCommentGuarded(ctx context.Context, requestedKey string, opts JiraCommentAddOpts) (*JiraCommentAddResult, error) {
	requestedKey, err := ValidateJiraGuardedCommentKey(requestedKey)
	if err != nil {
		return nil, err
	}
	opts, err = normalizeJiraCommentAddOpts(opts)
	if err != nil {
		return nil, err
	}
	result := newJiraCommentAddResult(requestedKey, opts)
	result.BodySHA256, result.BodyBytes = sha256Hex(opts.Body), len(opts.Body)
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return result, jiraCommentFailure("guarded Jira comment backend identity is invalid", domain.ErrCheckFailed, true, false)
	}
	result.BackendSHA256 = backendHash
	port, ok := s.tr.(domain.JiraGuardedCommentPort)
	if !ok {
		return result, jiraCommentFailure("guarded Jira comments are unavailable", domain.ErrConfig, true, false)
	}
	maxRequests := jiraGuardedCommentPreviewMaxRequests
	if opts.Apply {
		maxRequests = jiraGuardedCommentApplyMaxRequests
	}
	budget, err := domain.NewReadBudget(maxRequests, jiraGuardedCommentMaxResponseBytes)
	if err != nil {
		return result, jiraCommentFailure("guarded Jira comment budget is invalid", err, true, false)
	}
	workflowCtx, cancel := context.WithTimeout(ctx, jiraGuardedCommentDeadline)
	defer cancel()
	deadline, _ := workflowCtx.Deadline()
	workflowCtx = domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(workflowCtx, budget)))
	defer func() {
		usage := budget.Usage()
		result.Usage = JiraCommentUsage{Requests: usage.Attempts, ResponseBytes: usage.ResponseBytes}
	}()

	initial, err := s.buildGuardedCommentSnapshot(workflowCtx, port, requestedKey, requestedKey, "", opts)
	if err != nil {
		return result, jiraCommentFailure("guarded Jira comment proposal qualification failed", err, true, false)
	}
	result = initial.result
	if err := workflowCtx.Err(); err != nil {
		result.Status, result.Complete = "blocked", false
		return result, jiraCommentFailure("guarded Jira comment deadline expired during proposal qualification", err, true, false)
	}
	if opts.Apply && opts.ExpectedProposalHash != result.ProposalHash {
		result.Status = "blocked"
		return result, jiraCommentFailure("guarded Jira comment proposal changed since review", domain.ErrCheckFailed, true, false)
	}
	satisfied := opts.SatisfactionPolicy == jiraCommentSatisfactionExactBodyPresent && result.ExactBodyCount > 0
	if satisfied {
		result.Status = "already_satisfied"
		return result, nil
	}
	result.Status = "would_apply"
	if !opts.Apply {
		return result, nil
	}

	prewrite, err := s.buildGuardedCommentSnapshot(workflowCtx, port, initial.issue.ID, requestedKey, initial.issue.ID, opts)
	if err != nil {
		result.Status, result.Complete = "blocked", false
		return result, jiraCommentFailure("guarded Jira comment proposal could not be qualified immediately before dispatch", err, true, false)
	}
	if prewrite.result.ProposalHash != result.ProposalHash {
		result.Status = "blocked"
		return result, jiraCommentFailure("guarded Jira comment proposal changed immediately before dispatch", domain.ErrCheckFailed, true, false)
	}
	if err := workflowCtx.Err(); err != nil {
		result.Status = "blocked"
		return result, jiraCommentFailure("guarded Jira comment deadline expired before dispatch", err, true, false)
	}

	result.WriteAttempted = true
	ack, writeErr := port.WriteGuardedComment(workflowCtx, domain.JiraGuardedCommentWrite{
		ID: prewrite.issue.ID, Key: prewrite.issue.Key, Project: prewrite.issue.Project, Body: append([]byte(nil), prewrite.body...),
	})
	if writeDefinitelyNotAttempted(writeErr) {
		result.WriteAttempted = false
		result.Status = "blocked"
		return result, jiraCommentFailure("guarded Jira comment write was refused before dispatch", writeErr, true, false)
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "not_applied"
		return result, jiraCommentFailure("Jira definitively rejected the reviewed comment append", writeErr, false, false)
	}

	closeout, closeCancel := context.WithDeadline(context.WithoutCancel(workflowCtx), deadline)
	defer closeCancel()
	readback, readErr := s.readGuardedCommentReadback(closeout, port, prewrite, requestedKey)
	if readErr != nil || closeout.Err() != nil {
		result.Status, result.Complete = "outcome_unknown", false
		return result, jiraCommentFailure("guarded Jira comment outcome is unknown; do not replay automatically", errors.Join(writeErr, readErr, closeout.Err()), true, true)
	}
	result.Reconciled = true
	result.ReadbackUpdated = readback.issue.Updated
	newRecords, unchanged := guardedCommentNewRecords(prewrite.records, readback.records)
	advanced := readback.updatedTime.After(prewrite.updatedTime)
	if !unchanged || !advanced {
		result.Status = "outcome_unknown"
		return result, jiraCommentFailure("guarded Jira comment readback did not preserve the baseline and advance the issue revision; do not replay automatically", writeErr, true, true)
	}
	if writeErr == nil && canonicalPositiveNumericString(ack.ID) {
		for _, record := range newRecords {
			if record.ID == ack.ID && guardedCommentCandidate(record, prewrite.body, prewrite.actor) {
				result.Status, result.CommentID = "applied", record.ID
				return result, nil
			}
		}
		result.Status = "outcome_unknown"
		return result, jiraCommentFailure("guarded Jira comment acknowledgement was not proved by complete readback; do not replay automatically", writeErr, true, true)
	}
	candidates := make([]jiraGuardedCommentRecord, 0, 1)
	for _, record := range newRecords {
		if guardedCommentCandidate(record, prewrite.body, prewrite.actor) {
			candidates = append(candidates, record)
		}
	}
	if len(candidates) == 1 {
		result.Status, result.CommentID = "recovered", candidates[0].ID
		return result, nil
	}
	result.Status = "outcome_unknown"
	return result, jiraCommentFailure("guarded Jira comment readback did not prove one exact new comment; do not replay automatically", writeErr, true, true)
}

func ValidateJiraGuardedCommentKey(value string) (string, error) {
	key := strings.ToUpper(strings.TrimSpace(value))
	if len(key) == 0 || len(key) > jiraGuardedCommentMaxKeyBytes || !domain.ValidJiraIssueKey(key) {
		return "", fmt.Errorf("%w: issue key must be canonical and at most %d bytes (for example PROJ-1)", domain.ErrUsage, jiraGuardedCommentMaxKeyBytes)
	}
	return key, nil
}

func ValidateJiraCommentBody(raw []byte) ([]byte, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("%w: comment body must not be empty", domain.ErrUsage)
	}
	if len(raw) > JiraCommentBodyMaxBytes {
		return nil, fmt.Errorf("%w: comment body exceeds the %d MiB limit", domain.ErrUsage, JiraCommentBodyMaxBytes>>20)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("%w: comment body is not valid UTF-8", domain.ErrUsage)
	}
	return append([]byte(nil), raw...), nil
}

func normalizeJiraCommentAddOpts(opts JiraCommentAddOpts) (JiraCommentAddOpts, error) {
	body, err := ValidateJiraCommentBody(opts.Body)
	if err != nil {
		return JiraCommentAddOpts{}, err
	}
	opts.Body = body
	if opts.SatisfactionPolicy == "" {
		opts.SatisfactionPolicy = jiraCommentSatisfactionAppendAlways
	}
	if opts.SatisfactionPolicy != jiraCommentSatisfactionAppendAlways && opts.SatisfactionPolicy != jiraCommentSatisfactionExactBodyPresent {
		return JiraCommentAddOpts{}, fmt.Errorf("%w: unsupported Jira comment satisfaction policy", domain.ErrUsage)
	}
	opts.ExpectedProposalHash = strings.TrimSpace(opts.ExpectedProposalHash)
	if !opts.Apply {
		if opts.ExpectedProposalHash != "" {
			return JiraCommentAddOpts{}, fmt.Errorf("%w: --expected-proposal-hash requires --apply", domain.ErrUsage)
		}
		return opts, nil
	}
	if err := ValidateJiraDescriptionEditReviewHash(opts.ExpectedProposalHash); err != nil {
		return JiraCommentAddOpts{}, err
	}
	return opts, nil
}

func newJiraCommentAddResult(requestedKey string, opts JiraCommentAddOpts) *JiraCommentAddResult {
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	return &JiraCommentAddResult{
		SchemaVersion: jiraGuardedCommentSchemaVersion, Operation: "jira_issue_comment_append",
		SatisfactionPolicy: opts.SatisfactionPolicy, RequestedKey: requestedKey,
		Bounds: JiraCommentBounds{
			MaxKeyBytes: jiraGuardedCommentMaxKeyBytes, MaxBodyBytes: JiraCommentBodyMaxBytes,
			MaxEvidenceIDBytes: domain.JiraEvidenceIDMaxBytes, MaxEvidenceMetadataBytes: domain.JiraCommentEvidenceMetadataMaxBytes,
			MaxPages: domain.JiraCommentReadMaxPages, MaxItems: domain.JiraCommentReadMaxItems,
			MaxInventoryBytes: jiraGuardedCommentMaxInventoryBytes, PreviewMaxRequests: jiraGuardedCommentPreviewMaxRequests,
			ApplyMaxRequests: jiraGuardedCommentApplyMaxRequests, MaxAggregateResponseBytes: jiraGuardedCommentMaxResponseBytes,
			DeadlineMillis: jiraGuardedCommentDeadline.Milliseconds(),
		},
		Mode: mode, Status: "blocked",
	}
}

func JiraCommentAddText(result *JiraCommentAddResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("schema_version: %d\noperation: %s\nsatisfaction_policy: %s\nbackend_sha256: %s\nrequested_key: %s\nissue_id: %s\nkey: %s\nproject: %s\nupdated: %s\nreadback_updated: %s\nbody_sha256: %s\nbody_bytes: %d\nactor_sha256: %s\ncurrent_count: %d\nbaseline_sha256: %s\nexact_body_count: %d\nbounds.max_key_bytes: %d\nbounds.max_body_bytes: %d\nbounds.max_evidence_id_bytes: %d\nbounds.max_evidence_metadata_bytes: %d\nbounds.max_pages: %d\nbounds.max_items: %d\nbounds.max_inventory_bytes: %d\nbounds.preview_max_requests: %d\nbounds.apply_max_requests: %d\nbounds.max_aggregate_response_bytes: %d\nbounds.deadline_millis: %d\nusage.requests: %d\nusage.response_bytes: %d\nmode: %s\nstatus: %s\nproposal_hash: %s\ncomment_id: %s\nwrite_attempted: %t\nreconciled: %t\ncomplete: %t",
		result.SchemaVersion, result.Operation, result.SatisfactionPolicy, result.BackendSHA256,
		result.RequestedKey, result.IssueID, result.Key, result.Project, result.Updated, result.ReadbackUpdated,
		result.BodySHA256, result.BodyBytes, result.ActorSHA256, result.CurrentCount, result.BaselineSHA256, result.ExactBodyCount,
		result.Bounds.MaxKeyBytes, result.Bounds.MaxBodyBytes, result.Bounds.MaxEvidenceIDBytes, result.Bounds.MaxEvidenceMetadataBytes,
		result.Bounds.MaxPages, result.Bounds.MaxItems, result.Bounds.MaxInventoryBytes, result.Bounds.PreviewMaxRequests,
		result.Bounds.ApplyMaxRequests, result.Bounds.MaxAggregateResponseBytes, result.Bounds.DeadlineMillis,
		result.Usage.Requests, result.Usage.ResponseBytes, result.Mode, result.Status, result.ProposalHash, result.CommentID,
		result.WriteAttempted, result.Reconciled, result.Complete)
}

func jiraCommentFailure(message string, cause error, closed, ambiguous bool) error {
	return &jiraCommentWriteError{message: message, cause: sanitizeJiraDescriptionEditCause(cause), closed: closed, ambiguous: ambiguous}
}
