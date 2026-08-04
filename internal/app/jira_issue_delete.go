package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

const jiraIssueDeleteSchemaVersion = 1

type JiraIssueDeleteOpts struct {
	Apply                bool
	Confirm              string
	DeleteSubtasks       bool
	ExpectedUpdated      string
	ExpectedProposalHash string
}

type JiraIssueDeleteResult struct {
	SchemaVersion      int                      `json:"schema_version"`
	RequestedKey       string                   `json:"requested_key"`
	Key                string                   `json:"key"`
	IssueID            string                   `json:"issue_id"`
	IssueIDSHA256      string                   `json:"issue_id_sha256"`
	Mode               string                   `json:"mode"`
	Status             string                   `json:"status"`
	Operation          string                   `json:"operation"`
	ObservedState      string                   `json:"observed_state,omitempty"`
	CurrentUpdated     string                   `json:"current_updated"`
	ExpectedUpdated    string                   `json:"expected_updated"`
	SubtaskCount       int                      `json:"subtask_count"`
	Subtasks           []JiraIssueDeleteSubtask `json:"subtasks"`
	SubtasksSHA256     string                   `json:"subtasks_sha256"`
	DeleteSubtasks     bool                     `json:"delete_subtasks"`
	BackendSHA256      string                   `json:"backend_sha256"`
	ProposalHash       string                   `json:"proposal_hash"`
	WriteAttempted     bool                     `json:"write_attempted"`
	Reconciled         bool                     `json:"reconciled,omitempty"`
	PermissionRelative bool                     `json:"permission_relative"`
	Complete           bool                     `json:"complete"`
	Warning            string                   `json:"warning"`
}

type JiraIssueDeleteSubtask struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

type jiraIssueDeleteSnapshot struct {
	requestedKey  string
	key           string
	issueID       string
	issueIDSHA256 string
	updated       string
	subtasks      []JiraIssueDeleteSubtask
	subtasksHash  string
	backendHash   string
}

type jiraIssueDeleteWriteError struct {
	message   string
	cause     error
	ambiguous bool
}

func (e *jiraIssueDeleteWriteError) Error() string { return e.message }

func (e *jiraIssueDeleteWriteError) Unwrap() []error {
	if e == nil {
		return nil
	}
	errList := []error{domain.ErrCheckFailed}
	if e.cause != nil {
		errList = append(errList, e.cause)
	}
	return errList
}

func (e *jiraIssueDeleteWriteError) DiagnosticAmbiguousWrite() bool {
	return e != nil && e.ambiguous
}

// DeleteIssueGuarded previews or applies one reviewed permanent Jira issue
// deletion. Jira Data Center has no delete CAS: apply therefore revalidates the
// exact immutable issue id, updated marker, and complete permission-relative
// subtask inventory immediately before one non-replayed DELETE addressed by id.
func (s *JiraService) DeleteIssueGuarded(ctx context.Context, requestedKey string, opts JiraIssueDeleteOpts) (*JiraIssueDeleteResult, error) {
	requestedKey = strings.TrimSpace(requestedKey)
	if !canonicalJiraIssueDeleteKey(requestedKey) {
		return nil, fmt.Errorf("%w: issue key must be canonical (for example PROJ-1)", domain.ErrUsage)
	}
	if !opts.Apply && (opts.Confirm != "" || strings.TrimSpace(opts.ExpectedUpdated) != "" || strings.TrimSpace(opts.ExpectedProposalHash) != "") {
		return nil, fmt.Errorf("%w: --confirm, --expected-updated, and --expected-proposal-hash require --apply", domain.ErrUsage)
	}
	if opts.Apply && opts.Confirm != "DELETE" {
		return nil, fmt.Errorf("%w: --confirm must be exactly DELETE with --apply", domain.ErrUsage)
	}
	if opts.Apply && !canonicalJiraTransitionIdentity(opts.ExpectedUpdated) {
		return nil, fmt.Errorf("%w: --expected-updated is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	if opts.Apply && !canonicalJiraTransitionIdentity(opts.ExpectedProposalHash) {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	if opts.Apply {
		if err := ValidateJiraIssueDeleteReviewMarkers(opts.ExpectedUpdated, opts.ExpectedProposalHash); err != nil {
			return nil, err
		}
	}

	initial, err := s.jiraIssueDeleteSnapshot(ctx, requestedKey, requestedKey, "")
	if err != nil {
		return nil, fmt.Errorf("qualify Jira issue deletion proposal: %w", sanitizeRemoteWriteCause(err))
	}
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	expectedUpdated := initial.updated
	if opts.ExpectedUpdated != "" {
		expectedUpdated = opts.ExpectedUpdated
	}
	proposalHash := jiraIssueDeleteProposalHash(initial, opts.DeleteSubtasks)
	result := &JiraIssueDeleteResult{
		SchemaVersion: jiraIssueDeleteSchemaVersion, RequestedKey: requestedKey, Key: initial.key,
		IssueID: initial.issueID, IssueIDSHA256: initial.issueIDSHA256, Mode: mode, Status: "would_apply", Operation: "delete",
		ObservedState: "present", CurrentUpdated: initial.updated, ExpectedUpdated: expectedUpdated,
		SubtaskCount: len(initial.subtasks), Subtasks: append([]JiraIssueDeleteSubtask{}, initial.subtasks...), SubtasksSHA256: initial.subtasksHash,
		DeleteSubtasks: opts.DeleteSubtasks, BackendSHA256: initial.backendHash, ProposalHash: proposalHash,
		PermissionRelative: true, Complete: true,
		Warning: "Jira deletion is permanent and has no server-side CAS; apply revalidates immediately before one DELETE and never replays it",
	}
	if len(initial.subtasks) > 0 && !opts.DeleteSubtasks {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: the complete permission-relative snapshot contains subtasks; review and explicitly pass --delete-subtasks to include cascade intent", domain.ErrCheckFailed)
	}
	if opts.Apply && expectedUpdated != initial.updated {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: reviewed issue updated marker changed; run the dry-run again", domain.ErrCheckFailed)
	}
	if opts.Apply && opts.ExpectedProposalHash != proposalHash {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: issue deletion proposal changed since review; run the dry-run again", domain.ErrCheckFailed)
	}
	if !opts.Apply {
		return result, nil
	}

	prewrite, err := s.jiraIssueDeleteSnapshot(ctx, initial.issueID, requestedKey, initial.issueID)
	if err != nil {
		result.Status = "blocked"
		return result, &jiraIssueDeleteWriteError{
			message: "issue deletion proposal could not be revalidated immediately before the write",
			cause:   sanitizeRemoteWriteCause(err),
		}
	}
	if jiraIssueDeleteProposalHash(prewrite, opts.DeleteSubtasks) != proposalHash {
		result.Status = "blocked"
		return result, fmt.Errorf("%w: issue deletion proposal changed since review; run the dry-run again", domain.ErrCheckFailed)
	}

	result.WriteAttempted = true
	writeErr := s.tr.DeleteIssue(domain.WithSingleAttempt(ctx), prewrite.issueID, opts.DeleteSubtasks)
	if writeErr != nil && definitiveWriteRejection(writeErr) && !errors.Is(writeErr, domain.ErrNotFound) {
		result.Status = "not_applied"
		return result, &jiraIssueDeleteWriteError{
			message: "Jira rejected the reviewed issue deletion; it was not applied",
			cause:   sanitizeRemoteWriteCause(writeErr),
		}
	}

	_, readbackErr := s.jiraIssueDeleteSnapshot(ctx, prewrite.issueID, requestedKey, prewrite.issueID)
	if errors.Is(readbackErr, domain.ErrNotFound) {
		result.Reconciled = true
		result.ObservedState = "absent"
		if writeErr == nil {
			result.Status = "applied"
			return result, nil
		}
		result.Status = "outcome_unknown"
		return result, jiraIssueDeleteAmbiguousError(
			"issue deletion outcome is unknown because permission-relative absence after a failed request cannot prove physical deletion; do not replay automatically",
			sanitizeRemoteWriteCause(writeErr),
		)
	}
	result.Status = "outcome_unknown"
	if readbackErr != nil {
		result.Complete = false
		result.ObservedState = "unavailable"
		return result, jiraIssueDeleteAmbiguousError(
			"issue deletion outcome is unknown because exact permission-relative readback failed; do not replay automatically",
			errors.Join(sanitizeRemoteWriteCause(writeErr), sanitizeRemoteWriteCause(readbackErr)),
		)
	}
	result.Reconciled = true
	result.ObservedState = "present"
	return result, jiraIssueDeleteAmbiguousError(
		"issue deletion outcome is unknown because the exact immutable issue remains visible; do not replay automatically",
		sanitizeRemoteWriteCause(writeErr),
	)
}

func (s *JiraService) jiraIssueDeleteSnapshot(ctx context.Context, lookup, requestedKey, expectedID string) (jiraIssueDeleteSnapshot, error) {
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return jiraIssueDeleteSnapshot{}, fmt.Errorf("%w: invalid Jira backend identity", domain.ErrCheckFailed)
	}
	readCtx := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
	issue, err := s.tr.GetIssue(readCtx, lookup, []string{"updated", "subtasks"})
	if err != nil {
		return jiraIssueDeleteSnapshot{}, err
	}
	if issue == nil || !canonicalPositiveNumericString(issue.ID) || issue.Key != requestedKey || !canonicalJiraIssueDeleteKey(issue.Key) {
		return jiraIssueDeleteSnapshot{}, fmt.Errorf("%w: Jira returned a missing, moved, or malformed issue identity", domain.ErrCheckFailed)
	}
	if expectedID != "" && issue.ID != expectedID {
		return jiraIssueDeleteSnapshot{}, fmt.Errorf("%w: Jira returned a different immutable issue identity", domain.ErrCheckFailed)
	}
	updatedRaw, present := issue.Fields["updated"]
	updated, ok := updatedRaw.(string)
	if !present || !ok || !canonicalJiraTransitionIdentity(updated) {
		return jiraIssueDeleteSnapshot{}, fmt.Errorf("%w: Jira omitted or malformed the requested updated marker", domain.ErrCheckFailed)
	}
	if _, err := parseJiraUpdatedTime(updated); err != nil {
		return jiraIssueDeleteSnapshot{}, fmt.Errorf("%w: Jira returned an unsupported updated datetime", domain.ErrCheckFailed)
	}
	subtasks, err := jiraIssueDeleteSubtasks(issue.Fields, issue.ID, issue.Key)
	if err != nil {
		return jiraIssueDeleteSnapshot{}, err
	}
	issueSum := sha256.Sum256([]byte(issue.ID))
	subtaskBytes, _ := json.Marshal(subtasks)
	subtaskSum := sha256.Sum256(subtaskBytes)
	return jiraIssueDeleteSnapshot{
		requestedKey: requestedKey, key: issue.Key, issueID: issue.ID,
		issueIDSHA256: hex.EncodeToString(issueSum[:]), updated: updated,
		subtasks: subtasks, subtasksHash: hex.EncodeToString(subtaskSum[:]), backendHash: backendHash,
	}, nil
}

func jiraIssueDeleteSubtasks(fields map[string]any, parentID, parentKey string) ([]JiraIssueDeleteSubtask, error) {
	raw, present := fields["subtasks"]
	rows, ok := raw.([]any)
	if !present || raw == nil || !ok {
		return nil, fmt.Errorf("%w: Jira did not return a complete permission-relative subtask array", domain.ErrCheckFailed)
	}
	result := make([]JiraIssueDeleteSubtask, 0, len(rows))
	seenIDs := make(map[string]bool, len(rows))
	seenKeys := make(map[string]bool, len(rows))
	for _, row := range rows {
		object, ok := row.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: Jira returned a malformed subtask row", domain.ErrCheckFailed)
		}
		id, ok := graphStrictPositiveID(object["id"])
		key, keyOK := object["key"].(string)
		if !ok || !keyOK || !canonicalJiraIssueDeleteKey(key) || id == parentID || key == parentKey || seenIDs[id] || seenKeys[key] {
			return nil, fmt.Errorf("%w: Jira returned a missing, duplicate, or malformed subtask identity", domain.ErrCheckFailed)
		}
		seenIDs[id] = true
		seenKeys[key] = true
		result = append(result, JiraIssueDeleteSubtask{ID: id, Key: key})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID != result[j].ID {
			return result[i].ID < result[j].ID
		}
		return result[i].Key < result[j].Key
	})
	return result, nil
}

func canonicalPositiveNumericString(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || value[0] == '0' {
		return false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return err == nil && number > 0
}

func canonicalJiraIssueDeleteKey(key string) bool {
	span := graphJiraKeyPattern.FindStringIndex(key)
	return span != nil && span[0] == 0 && span[1] == len(key)
}

// ValidateJiraIssueDeleteReviewMarkers rejects malformed complete-looking
// apply invocations before configuration or network access. It is exported so
// the CLI preflight and app boundary enforce one exact grammar.
func ValidateJiraIssueDeleteReviewMarkers(updated, proposalHash string) error {
	if !canonicalJiraTransitionIdentity(updated) {
		return fmt.Errorf("%w: --expected-updated must be an exact supported Jira timestamp", domain.ErrUsage)
	}
	if _, err := parseJiraUpdatedTime(updated); err != nil {
		return fmt.Errorf("%w: --expected-updated must be an exact supported Jira timestamp", domain.ErrUsage)
	}
	if len(proposalHash) != sha256.Size*2 {
		return fmt.Errorf("%w: --expected-proposal-hash must be a lowercase 64-character SHA-256", domain.ErrUsage)
	}
	for _, char := range proposalHash {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return fmt.Errorf("%w: --expected-proposal-hash must be a lowercase 64-character SHA-256", domain.ErrUsage)
		}
	}
	return nil
}

func jiraIssueDeleteProposalHash(snapshot jiraIssueDeleteSnapshot, deleteSubtasks bool) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion  int                      `json:"schema_version"`
		Operation      string                   `json:"operation"`
		BackendSHA256  string                   `json:"backend_sha256"`
		RequestedKey   string                   `json:"requested_key"`
		Key            string                   `json:"key"`
		IssueID        string                   `json:"issue_id"`
		Updated        string                   `json:"updated"`
		Subtasks       []JiraIssueDeleteSubtask `json:"subtasks"`
		DeleteSubtasks bool                     `json:"delete_subtasks"`
	}{
		SchemaVersion: jiraIssueDeleteSchemaVersion, Operation: "delete_issue",
		BackendSHA256: snapshot.backendHash, RequestedKey: snapshot.requestedKey,
		Key: snapshot.key, IssueID: snapshot.issueID, Updated: snapshot.updated,
		Subtasks: snapshot.subtasks, DeleteSubtasks: deleteSubtasks,
	})
	return guardedProposalDigest(canonical)
}

func jiraIssueDeleteAmbiguousError(message string, cause error) error {
	return &jiraIssueDeleteWriteError{message: message, cause: cause, ambiguous: true}
}

func JiraIssueDeleteText(result *JiraIssueDeleteResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("status: %s\nkey: %s\nissue_id: %s\nupdated: %s\nsubtasks: %d\nproposal_hash: %s\nobserved_state: %s",
		result.Status, result.Key, result.IssueID, result.CurrentUpdated, result.SubtaskCount, result.ProposalHash, result.ObservedState)
}
