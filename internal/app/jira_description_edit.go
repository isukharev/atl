package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/textedit"
)

const jiraDescriptionEditSchemaVersion = 1

type JiraDescriptionEditOpts struct {
	Old                  []byte
	New                  []byte
	All                  bool
	Apply                bool
	ExpectedProposalHash string
}

type JiraDescriptionEditOffset struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type JiraDescriptionEditMatcher struct {
	Pass    string                      `json:"pass"`
	Count   int                         `json:"count"`
	Offsets []JiraDescriptionEditOffset `json:"offsets"`
}

// JiraDescriptionEditResult is a content-free, schema-v1 proposal and
// closeout record. Native Jira wiki bodies and matcher inputs are represented
// only by exact byte lengths and SHA-256 digests.
type JiraDescriptionEditResult struct {
	SchemaVersion   int                        `json:"schema_version"`
	BackendSHA256   string                     `json:"backend_sha256"`
	RequestedKey    string                     `json:"requested_key"`
	Key             string                     `json:"key"`
	IssueID         string                     `json:"issue_id"`
	Updated         string                     `json:"updated"`
	ReadbackUpdated string                     `json:"readback_updated,omitempty"`
	Mode            string                     `json:"mode"`
	Status          string                     `json:"status"`
	OldSHA256       string                     `json:"old_sha256"`
	OldBytes        int                        `json:"old_bytes"`
	NewSHA256       string                     `json:"new_sha256"`
	NewBytes        int                        `json:"new_bytes"`
	All             bool                       `json:"all"`
	Matcher         JiraDescriptionEditMatcher `json:"matcher"`
	BeforeSHA256    string                     `json:"before_sha256"`
	BeforeBytes     int                        `json:"before_bytes"`
	AfterSHA256     string                     `json:"after_sha256"`
	AfterBytes      int                        `json:"after_bytes"`
	ProposalHash    string                     `json:"proposal_hash"`
	WriteAttempted  bool                       `json:"write_attempted"`
	Reconciled      bool                       `json:"reconciled,omitempty"`
	Complete        bool                       `json:"complete"`
}

type jiraDescriptionEditEvidence struct {
	requestedKey string
	key          string
	issueID      string
	body         []byte
	updated      string
	updatedTime  time.Time
	backendHash  string
}

type jiraDescriptionEditSnapshot struct {
	evidence jiraDescriptionEditEvidence
	result   *JiraDescriptionEditResult
	after    []byte
}

type jiraDescriptionEditError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *jiraDescriptionEditError) Error() string {
	if e == nil {
		return "Jira description edit failed"
	}
	return e.message
}

func (e *jiraDescriptionEditError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}

func (e *jiraDescriptionEditError) DiagnosticAmbiguousWrite() bool {
	return e != nil && e.ambiguous
}

// EditDescriptionGuarded previews or applies one targeted replacement against
// native Jira wiki bytes. Apply revalidates the complete proposal by immutable
// issue ID, sends at most one description-only PUT, and proves success only by
// exact advancing readback.
func (s *JiraService) EditDescriptionGuarded(ctx context.Context, requestedKey string, opts JiraDescriptionEditOpts) (*JiraDescriptionEditResult, error) {
	requestedKey = strings.ToUpper(strings.TrimSpace(requestedKey))
	if !domain.ValidJiraIssueKey(requestedKey) {
		return nil, fmt.Errorf("%w: issue key must be canonical (for example PROJ-1)", domain.ErrUsage)
	}
	if len(opts.Old) == 0 {
		return nil, fmt.Errorf("%w: --old (or --old-file) is required and must be non-empty", domain.ErrUsage)
	}
	policy, err := newGuardedWritePolicy(opts.Apply, opts.ExpectedProposalHash)
	if err != nil {
		return nil, err
	}
	if opts.Apply {
		if err := ValidateJiraDescriptionEditReviewHash(opts.ExpectedProposalHash); err != nil {
			return nil, err
		}
	}

	initial, err := s.buildJiraDescriptionEditSnapshot(ctx, requestedKey, requestedKey, "", opts)
	if err != nil {
		return nil, err
	}
	decision := policy.decide(initial.result.ProposalHash, equalBytes(initial.evidence.body, initial.after))
	initial.result.Mode = decision.mode
	initial.result.Status = decision.status
	if decision.hashMismatch {
		return initial.result, fmt.Errorf("%w: description edit proposal changed since review; run the preview again", domain.ErrCheckFailed)
	}
	if !decision.writeRequired {
		return initial.result, nil
	}

	prewrite, err := s.buildJiraDescriptionEditSnapshot(ctx, initial.evidence.issueID, requestedKey, initial.evidence.issueID, opts)
	if err != nil {
		initial.result.Status = "blocked"
		return initial.result, &jiraDescriptionEditError{
			message: "description edit proposal could not be revalidated immediately before the write",
			cause:   sanitizeJiraDescriptionEditCause(err), closed: true,
		}
	}
	if prewrite.result.ProposalHash != initial.result.ProposalHash {
		initial.result.Status = "blocked"
		return initial.result, fmt.Errorf("%w: description edit proposal changed immediately before the write; run the preview again", domain.ErrCheckFailed)
	}

	initial.result.WriteAttempted = true
	writeErr := s.tr.Update(domain.WithSingleAttempt(ctx), prewrite.evidence.issueID, "", cloneJiraDescription(prewrite.after), nil)
	if writeDefinitelyNotAttempted(writeErr) {
		initial.result.WriteAttempted = false
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		initial.result.Status = "not_applied"
		return initial.result, &jiraDescriptionEditError{
			message: "Jira rejected the reviewed description edit; it was not applied",
			cause:   sanitizeJiraDescriptionEditCause(writeErr),
		}
	}

	readback, readbackErr := s.readJiraDescriptionEditEvidence(ctx, prewrite.evidence.issueID, requestedKey, prewrite.evidence.issueID)
	if readbackErr != nil {
		initial.result.Status = "outcome_unknown"
		initial.result.Complete = false
		return initial.result, jiraDescriptionEditAmbiguousError(
			"description edit outcome is unknown because exact readback failed; do not replay automatically",
			errors.Join(sanitizeJiraDescriptionEditCause(writeErr), sanitizeJiraDescriptionEditCause(readbackErr)),
		)
	}
	initial.result.Reconciled = true
	exactBody := equalBytes(readback.body, prewrite.after)
	advanced := readback.updatedTime.After(prewrite.evidence.updatedTime)
	if exactBody && advanced {
		initial.result.ReadbackUpdated = readback.updated
		if writeErr == nil {
			initial.result.Status = "applied"
		} else {
			initial.result.Status = "recovered"
		}
		return initial.result, nil
	}
	initial.result.Status = "outcome_unknown"
	return initial.result, jiraDescriptionEditAmbiguousError(
		"description edit outcome is unknown because readback did not prove the exact advancing end state; do not replay automatically",
		sanitizeJiraDescriptionEditCause(writeErr),
	)
}

// ValidateJiraDescriptionEditReviewHash keeps malformed apply markers out of
// configuration and network setup when called from the CLI, while the app
// repeats the same preflight for non-CLI callers.
func ValidateJiraDescriptionEditReviewHash(value string) error {
	if len(value) != sha256.Size*2 {
		return fmt.Errorf("%w: --expected-proposal-hash must be a lowercase 64-character SHA-256", domain.ErrUsage)
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return fmt.Errorf("%w: --expected-proposal-hash must be a lowercase 64-character SHA-256", domain.ErrUsage)
		}
	}
	return nil
}

func cloneJiraDescription(value []byte) []byte {
	cloned := make([]byte, len(value))
	copy(cloned, value)
	return cloned
}

func (s *JiraService) buildJiraDescriptionEditSnapshot(ctx context.Context, lookup, requestedKey, expectedID string, opts JiraDescriptionEditOpts) (*jiraDescriptionEditSnapshot, error) {
	evidence, err := s.readJiraDescriptionEditEvidence(ctx, lookup, requestedKey, expectedID)
	if err != nil {
		return nil, err
	}
	res, err := textedit.Replace(string(evidence.body), string(opts.Old), string(opts.New), opts.All)
	if err != nil {
		var noMatch *textedit.NoMatchError
		if errors.As(err, &noMatch) {
			return nil, fmt.Errorf("%w: old text was not found in the Jira description", domain.ErrNotFound)
		}
		var ambiguous *textedit.AmbiguousError
		if errors.As(err, &ambiguous) {
			return nil, fmt.Errorf("%w: old text matched more than once; make it unique or pass --all", domain.ErrUsage)
		}
		return nil, fmt.Errorf("%w: description matcher failed", domain.ErrCheckFailed)
	}
	if res.Pass == textedit.PassWhitespace {
		for _, match := range res.Matches {
			if strings.Count(string(evidence.body[match.Start:match.End]), "\n") != strings.Count(string(opts.Old), "\n") {
				return nil, fmt.Errorf("%w: the whitespace-tolerant match crosses an unrequested Jira wiki line boundary", domain.ErrCheckFailed)
			}
		}
	}
	after := []byte(res.Text)
	result := &JiraDescriptionEditResult{
		SchemaVersion: jiraDescriptionEditSchemaVersion,
		BackendSHA256: evidence.backendHash, RequestedKey: requestedKey, Key: evidence.key,
		IssueID: evidence.issueID, Updated: evidence.updated, Status: "would_apply",
		OldSHA256: hashBytes(opts.Old), OldBytes: len(opts.Old), NewSHA256: hashBytes(opts.New), NewBytes: len(opts.New),
		All: opts.All, Matcher: JiraDescriptionEditMatcher{Pass: string(res.Pass), Count: len(res.Matches), Offsets: jiraDescriptionOffsets(res.Matches)},
		BeforeSHA256: hashBytes(evidence.body), BeforeBytes: len(evidence.body), AfterSHA256: hashBytes(after), AfterBytes: len(after),
		Complete: true,
	}
	result.ProposalHash = jiraDescriptionEditProposalHash(result)
	return &jiraDescriptionEditSnapshot{evidence: evidence, result: result, after: after}, nil
}

func (s *JiraService) readJiraDescriptionEditEvidence(ctx context.Context, lookup, requestedKey, expectedID string) (jiraDescriptionEditEvidence, error) {
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return jiraDescriptionEditEvidence{}, fmt.Errorf("%w: invalid Jira backend identity", domain.ErrCheckFailed)
	}
	issue, err := s.tr.GetIssue(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), lookup, []string{"description", "updated"})
	if err != nil {
		return jiraDescriptionEditEvidence{}, &jiraDescriptionEditError{message: "Jira description snapshot failed", cause: sanitizeJiraDescriptionEditCause(err)}
	}
	if issue == nil || !canonicalPositiveNumericString(issue.ID) || issue.Key != requestedKey || !domain.ValidJiraIssueKey(issue.Key) || (expectedID != "" && issue.ID != expectedID) {
		return jiraDescriptionEditEvidence{}, fmt.Errorf("%w: Jira returned a missing, moved, or malformed issue identity", domain.ErrCheckFailed)
	}
	description, present := issue.Raw["description"]
	if issue.Raw == nil {
		description, present = issue.Fields["description"]
	}
	var body []byte
	switch value := description.(type) {
	case nil:
		if !present {
			return jiraDescriptionEditEvidence{}, fmt.Errorf("%w: Jira omitted the requested description field", domain.ErrCheckFailed)
		}
		body = []byte{}
	case string:
		body = []byte(value)
	default:
		return jiraDescriptionEditEvidence{}, fmt.Errorf("%w: Jira returned a structured description instead of native wiki bytes", domain.ErrCheckFailed)
	}
	updatedRaw, present := issue.Fields["updated"]
	updated, ok := updatedRaw.(string)
	if !present || !ok || !canonicalJiraTransitionIdentity(updated) {
		return jiraDescriptionEditEvidence{}, fmt.Errorf("%w: Jira omitted or malformed the requested updated marker", domain.ErrCheckFailed)
	}
	updatedTime, err := parseJiraUpdatedTime(updated)
	if err != nil {
		return jiraDescriptionEditEvidence{}, fmt.Errorf("%w: Jira returned an unsupported updated datetime", domain.ErrCheckFailed)
	}
	return jiraDescriptionEditEvidence{
		requestedKey: requestedKey, key: issue.Key, issueID: issue.ID, body: body,
		updated: updated, updatedTime: updatedTime, backendHash: backendHash,
	}, nil
}

func jiraDescriptionOffsets(matches []textedit.Match) []JiraDescriptionEditOffset {
	offsets := make([]JiraDescriptionEditOffset, len(matches))
	for i, match := range matches {
		offsets[i] = JiraDescriptionEditOffset{Start: match.Start, End: match.End}
	}
	return offsets
}

func jiraDescriptionEditProposalHash(result *JiraDescriptionEditResult) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion int                        `json:"schema_version"`
		Operation     string                     `json:"operation"`
		BackendSHA256 string                     `json:"backend_sha256"`
		RequestedKey  string                     `json:"requested_key"`
		Key           string                     `json:"key"`
		IssueID       string                     `json:"issue_id"`
		Updated       string                     `json:"updated"`
		OldSHA256     string                     `json:"old_sha256"`
		OldBytes      int                        `json:"old_bytes"`
		NewSHA256     string                     `json:"new_sha256"`
		NewBytes      int                        `json:"new_bytes"`
		All           bool                       `json:"all"`
		Matcher       JiraDescriptionEditMatcher `json:"matcher"`
		BeforeSHA256  string                     `json:"before_sha256"`
		BeforeBytes   int                        `json:"before_bytes"`
		AfterSHA256   string                     `json:"after_sha256"`
		AfterBytes    int                        `json:"after_bytes"`
	}{
		result.SchemaVersion, "edit_description", result.BackendSHA256,
		result.RequestedKey, result.Key, result.IssueID, result.Updated,
		result.OldSHA256, result.OldBytes, result.NewSHA256, result.NewBytes,
		result.All, result.Matcher, result.BeforeSHA256, result.BeforeBytes,
		result.AfterSHA256, result.AfterBytes,
	})
	return guardedProposalDigest(canonical)
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func jiraDescriptionEditAmbiguousError(message string, cause error) error {
	return &jiraDescriptionEditError{message: message, cause: cause, closed: true, ambiguous: true}
}

// sanitizeJiraDescriptionEditCause is deliberately stronger than the shared
// write sanitizer: even a definitely-not-attempted transport error can carry a
// request path, so this surface retains only safe sentinel and status identity.
func sanitizeJiraDescriptionEditCause(err error) error {
	if err == nil {
		return nil
	}
	var causes []error
	for _, sentinel := range []error{
		domain.ErrUsage, domain.ErrAuth, domain.ErrNotFound, domain.ErrVersionConflict,
		domain.ErrForbidden, domain.ErrConfig, domain.ErrCheckFailed,
	} {
		if errors.Is(err, sentinel) {
			causes = append(causes, sentinel)
		}
	}
	var statusErr interface{ HTTPStatus() int }
	if errors.As(err, &statusErr) {
		causes = append(causes, remoteWriteHTTPStatus(statusErr.HTTPStatus()))
	}
	if len(causes) == 0 {
		return errors.New("request failed")
	}
	return errors.Join(causes...)
}

func JiraDescriptionEditText(result *JiraDescriptionEditResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("status: %s\nkey: %s\nissue_id: %s\nupdated: %s\npass: %s\ncount: %d\nproposal_hash: %s\nwrite_attempted: %t",
		result.Status, result.Key, result.IssueID, result.Updated, result.Matcher.Pass,
		result.Matcher.Count, result.ProposalHash, result.WriteAttempted)
}
