package app

import (
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
)

const (
	jiraGuardedLabelSchemaVersion    = 1
	jiraGuardedLabelMaxBytes         = 255
	jiraGuardedLabelMaxKeyBytes      = 64
	jiraGuardedLabelMaxRequested     = 100
	jiraGuardedLabelMaxCurrent       = 4096
	jiraGuardedLabelMaxRequests      = 4
	jiraGuardedLabelPreviewRequests  = 1
	jiraGuardedLabelMaxResponseBytes = 16 << 20
	jiraGuardedLabelDeadline         = 60 * time.Second
)

type JiraGuardedLabelOpts struct {
	Add                  []string
	Remove               []string
	Apply                bool
	ExpectedProposalHash string
}

type JiraGuardedLabelDigest struct {
	Count  int    `json:"count"`
	SHA256 string `json:"sha256"`
}

type JiraGuardedLabelBounds struct {
	MaxKeyBytes        int   `json:"max_key_bytes"`
	MaxLabelBytes      int   `json:"max_label_bytes"`
	MaxRequestedLabels int   `json:"max_requested_labels"`
	MaxCurrentLabels   int   `json:"max_current_labels"`
	MaxRequests        int   `json:"max_requests"`
	PreviewMaxRequests int   `json:"preview_max_requests"`
	MaxResponseBytes   int64 `json:"max_response_bytes"`
	DeadlineMillis     int64 `json:"deadline_millis"`
}

type JiraGuardedLabelUsage struct {
	Requests      int   `json:"requests"`
	ResponseBytes int64 `json:"response_bytes"`
}

// JiraGuardedLabelResult contains requested labels but never emits the
// unrelated current or desired label values learned from Jira.
type JiraGuardedLabelResult struct {
	SchemaVersion   int                    `json:"schema_version"`
	Operation       string                 `json:"operation"`
	BackendSHA256   string                 `json:"backend_sha256"`
	RequestedKey    string                 `json:"requested_key"`
	IssueID         string                 `json:"issue_id"`
	Key             string                 `json:"key"`
	Project         string                 `json:"project"`
	Updated         string                 `json:"updated"`
	ReadbackUpdated string                 `json:"readback_updated,omitempty"`
	Add             []string               `json:"add"`
	Remove          []string               `json:"remove"`
	Current         JiraGuardedLabelDigest `json:"current"`
	Desired         JiraGuardedLabelDigest `json:"desired"`
	EffectiveAdd    JiraGuardedLabelDigest `json:"effective_add"`
	EffectiveRemove JiraGuardedLabelDigest `json:"effective_remove"`
	Bounds          JiraGuardedLabelBounds `json:"bounds"`
	ProposalHash    string                 `json:"proposal_hash,omitempty"`
	Usage           JiraGuardedLabelUsage  `json:"usage"`
	Mode            string                 `json:"mode"`
	Status          string                 `json:"status"`
	WriteAttempted  bool                   `json:"write_attempted"`
	Reconciled      bool                   `json:"reconciled"`
	Complete        bool                   `json:"complete"`
}

type jiraGuardedLabelSnapshot struct {
	result          *JiraGuardedLabelResult
	evidence        domain.JiraGuardedLabelSnapshot
	backendHash     string
	effectiveAdd    []string
	effectiveRemove []string
	updatedTime     time.Time
}

type jiraGuardedLabelPrepared struct {
	result  *JiraGuardedLabelResult
	issueID string
}

type jiraGuardedLabelError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *jiraGuardedLabelError) Error() string { return e.message }
func (e *jiraGuardedLabelError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}
func (e *jiraGuardedLabelError) DiagnosticAmbiguousWrite() bool { return e != nil && e.ambiguous }

// GuardedLabels previews or applies one reviewed add/remove delta. All four
// possible Jira requests share one deadline and one aggregate response budget.
func (s *JiraService) GuardedLabels(ctx context.Context, requestedKey string, opts JiraGuardedLabelOpts) (*JiraGuardedLabelResult, error) {
	requestedKey, keyErr := ValidateJiraGuardedLabelKey(requestedKey)
	if keyErr != nil {
		return nil, keyErr
	}
	normalized, err := NormalizeJiraGuardedLabelOpts(opts)
	if err != nil {
		return nil, err
	}
	opts = normalized
	base := newJiraGuardedLabelResult(requestedKey, opts)
	port, ok := s.tr.(domain.JiraGuardedLabelPort)
	if !ok {
		return base, guardedLabelFailure("guarded Jira labels are unavailable", domain.ErrConfig, true, false)
	}
	maxRequests := jiraGuardedLabelPreviewRequests
	if opts.Apply {
		maxRequests = jiraGuardedLabelMaxRequests
	}
	execution, err := newJiraGuardedExecution(ctx, domain.ReadBudgetFromContext(ctx), maxRequests, jiraGuardedLabelMaxResponseBytes, jiraGuardedLabelDeadline)
	if err != nil {
		return base, guardedLabelFailure("guarded Jira label budget is invalid", err, true, false)
	}
	defer execution.Close()
	defer func() {
		usage := execution.Usage()
		base.Usage = JiraGuardedLabelUsage{Requests: usage.Attempts, ResponseBytes: usage.ResponseBytes}
	}()
	if err := execution.ctx.Err(); err != nil {
		return base, guardedLabelFailure("guarded Jira label workflow was canceled before qualification", err, true, false)
	}
	initial, err := s.buildGuardedLabelSnapshot(execution.ctx, port, requestedKey, requestedKey, "", opts)
	if err != nil {
		return base, guardedLabelFailure("guarded Jira label proposal qualification failed", err, true, false)
	}
	base = initial.result
	return s.guardedLabelsPreparedCore(execution, port, requestedKey, opts, &jiraGuardedLabelPrepared{result: base, issueID: initial.evidence.ID})
}

func (s *JiraService) guardedLabelsPreparedCore(execution *jiraGuardedExecution, port domain.JiraGuardedLabelPort, requestedKey string, opts JiraGuardedLabelOpts, prepared *jiraGuardedLabelPrepared) (*JiraGuardedLabelResult, error) {
	base := prepared.result
	if err := execution.ctx.Err(); err != nil {
		base.Status = "blocked"
		return base, guardedLabelFailure("guarded Jira label deadline expired during proposal qualification", err, true, false)
	}
	decision := guardedLabelDecision(opts, base.ProposalHash, base.EffectiveAdd.Count+base.EffectiveRemove.Count == 0)
	base.Mode, base.Status = decision.mode, decision.status
	if decision.hashMismatch {
		return base, guardedLabelFailure("guarded Jira label proposal changed since review", domain.ErrCheckFailed, true, false)
	}
	if !decision.writeRequired {
		return base, nil
	}

	prewrite, err := s.buildGuardedLabelSnapshot(execution.ctx, port, prepared.issueID, requestedKey, prepared.issueID, opts)
	if err != nil {
		base.Status, base.Complete = "blocked", false
		return base, guardedLabelFailure("guarded Jira label proposal could not be qualified immediately before dispatch", errors.Join(err, domain.ErrCheckFailed), true, false)
	}
	if prewrite.result.ProposalHash != base.ProposalHash {
		base.Status = "blocked"
		return base, guardedLabelFailure("guarded Jira label proposal changed immediately before dispatch", domain.ErrCheckFailed, true, false)
	}
	if err := execution.ctx.Err(); err != nil {
		base.Status = "blocked"
		return base, guardedLabelFailure("guarded Jira label deadline expired before dispatch", err, true, false)
	}

	base.WriteAttempted = true
	writeErr := port.WriteGuardedLabelDelta(execution.ctx, domain.JiraGuardedLabelWrite{
		ID: prewrite.evidence.ID, Key: prewrite.evidence.Key, Project: prewrite.evidence.Project,
		Add: append([]string(nil), prewrite.effectiveAdd...), Remove: append([]string(nil), prewrite.effectiveRemove...),
	})
	if writeDefinitelyNotAttempted(writeErr) {
		base.WriteAttempted = false
		base.Status = "blocked"
		return base, guardedLabelFailure("guarded Jira label write was refused before dispatch", writeErr, true, false)
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		base.Status = "not_applied"
		return base, guardedLabelFailure("Jira definitively rejected the reviewed label change", writeErr, false, false)
	}

	closeout, closeCancel := execution.Closeout()
	defer closeCancel()
	readback, readErr := s.readGuardedLabelEvidence(closeout, port, prewrite.evidence.ID, requestedKey, prewrite.evidence.ID)
	if readErr != nil || closeout.Err() != nil {
		base.Status, base.Complete = "outcome_unknown", false
		return base, guardedLabelFailure("guarded Jira label outcome is unknown; do not replay automatically", errors.Join(writeErr, readErr, closeout.Err()), true, true)
	}
	base.Reconciled = true
	exact := guardedLabelDigest(readback.evidence.Labels) == prewrite.result.Desired
	advanced := readback.updatedTime.After(prewrite.updatedTime)
	if exact && advanced {
		base.ReadbackUpdated = readback.evidence.Updated
		base.Status = "applied"
		if writeErr != nil {
			base.Status = "recovered"
		}
		return base, nil
	}
	base.Status = "outcome_unknown"
	return base, guardedLabelFailure("guarded Jira label readback did not prove the exact advancing end state; do not replay automatically", writeErr, true, true)
}

// ValidateJiraGuardedLabelKey keeps oversized canonical-looking keys out of
// configuration and network setup while matching the strict adapter bound.
func ValidateJiraGuardedLabelKey(value string) (string, error) {
	key := strings.ToUpper(strings.TrimSpace(value))
	if len(key) == 0 || len(key) > jiraGuardedLabelMaxKeyBytes || !domain.ValidJiraIssueKey(key) {
		return "", fmt.Errorf("%w: issue key must be canonical and at most %d bytes (for example PROJ-1)", domain.ErrUsage, jiraGuardedLabelMaxKeyBytes)
	}
	return key, nil
}

type jiraGuardedLabelDecision struct {
	mode, status  string
	hashMismatch  bool
	writeRequired bool
}

func guardedLabelDecision(opts JiraGuardedLabelOpts, proposalHash string, satisfied bool) jiraGuardedLabelDecision {
	decision := jiraGuardedLabelDecision{mode: "preview", status: "would_apply"}
	if !opts.Apply {
		if satisfied {
			decision.status = "already_satisfied"
		}
		return decision
	}
	decision.mode = "apply"
	if opts.ExpectedProposalHash != proposalHash {
		decision.status, decision.hashMismatch = "blocked", true
		return decision
	}
	if satisfied {
		decision.status = "already_satisfied"
		return decision
	}
	decision.writeRequired = true
	return decision
}

// NormalizeJiraGuardedLabelOpts is the pure shared CLI/app input boundary.
func NormalizeJiraGuardedLabelOpts(opts JiraGuardedLabelOpts) (JiraGuardedLabelOpts, error) {
	var err error
	opts.Add, err = normalizeGuardedLabelList(opts.Add, "--add")
	if err != nil {
		return JiraGuardedLabelOpts{}, err
	}
	opts.Remove, err = normalizeGuardedLabelList(opts.Remove, "--remove")
	if err != nil {
		return JiraGuardedLabelOpts{}, err
	}
	if len(opts.Add)+len(opts.Remove) == 0 {
		return JiraGuardedLabelOpts{}, fmt.Errorf("%w: pass --add and/or --remove", domain.ErrUsage)
	}
	if len(opts.Add)+len(opts.Remove) > jiraGuardedLabelMaxRequested {
		return JiraGuardedLabelOpts{}, fmt.Errorf("%w: at most 100 labels may be requested", domain.ErrUsage)
	}
	if guardedLabelListsOverlap(opts.Add, opts.Remove) {
		return JiraGuardedLabelOpts{}, fmt.Errorf("%w: the same label cannot be added and removed", domain.ErrUsage)
	}
	opts.ExpectedProposalHash = strings.TrimSpace(opts.ExpectedProposalHash)
	if !opts.Apply {
		if opts.ExpectedProposalHash != "" {
			return JiraGuardedLabelOpts{}, fmt.Errorf("%w: --expected-proposal-hash requires --apply", domain.ErrUsage)
		}
		return opts, nil
	}
	if err := ValidateJiraDescriptionEditReviewHash(opts.ExpectedProposalHash); err != nil {
		return JiraGuardedLabelOpts{}, err
	}
	return opts, nil
}

func normalizeGuardedLabelList(values []string, flag string) ([]string, error) {
	if values == nil {
		return []string{}, nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !utf8.ValidString(value) {
			return nil, fmt.Errorf("%w: %s labels must be valid UTF-8", domain.ErrUsage, flag)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%w: %s must not contain empty labels", domain.ErrUsage, flag)
		}
		if len(value) > jiraGuardedLabelMaxBytes {
			return nil, fmt.Errorf("%w: %s labels must not exceed 255 bytes", domain.ErrUsage, flag)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("%w: %s must not contain duplicate labels", domain.ErrUsage, flag)
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func guardedLabelListsOverlap(add, remove []string) bool {
	seen := make(map[string]struct{}, len(add))
	for _, value := range add {
		seen[value] = struct{}{}
	}
	for _, value := range remove {
		if _, ok := seen[value]; ok {
			return true
		}
	}
	return false
}

func newJiraGuardedLabelResult(requestedKey string, opts JiraGuardedLabelOpts) *JiraGuardedLabelResult {
	mode := "preview"
	if opts.Apply {
		mode = "apply"
	}
	return &JiraGuardedLabelResult{
		SchemaVersion: jiraGuardedLabelSchemaVersion, Operation: "jira_issue_labels",
		RequestedKey: requestedKey, Add: append([]string{}, opts.Add...), Remove: append([]string{}, opts.Remove...),
		Bounds: JiraGuardedLabelBounds{
			MaxKeyBytes: jiraGuardedLabelMaxKeyBytes, MaxLabelBytes: jiraGuardedLabelMaxBytes, MaxRequestedLabels: jiraGuardedLabelMaxRequested,
			MaxCurrentLabels: jiraGuardedLabelMaxCurrent, MaxRequests: jiraGuardedLabelMaxRequests,
			PreviewMaxRequests: jiraGuardedLabelPreviewRequests, MaxResponseBytes: jiraGuardedLabelMaxResponseBytes,
			DeadlineMillis: jiraGuardedLabelDeadline.Milliseconds(),
		},
		Mode: mode, Status: "blocked",
	}
}

func (s *JiraService) buildGuardedLabelSnapshot(ctx context.Context, port domain.JiraGuardedLabelPort, reference, requestedKey, expectedID string, opts JiraGuardedLabelOpts) (*jiraGuardedLabelSnapshot, error) {
	read, err := s.readGuardedLabelEvidence(ctx, port, reference, requestedKey, expectedID)
	if err != nil {
		return nil, err
	}
	current := append([]string(nil), read.evidence.Labels...)
	desired, effectiveAdd, effectiveRemove := guardedLabelDesired(current, opts.Add, opts.Remove)
	result := newJiraGuardedLabelResult(requestedKey, opts)
	result.BackendSHA256, result.IssueID, result.Key, result.Project, result.Updated = read.backendHash, read.evidence.ID, read.evidence.Key, read.evidence.Project, read.evidence.Updated
	result.Current, result.Desired = guardedLabelDigest(current), guardedLabelDigest(desired)
	result.EffectiveAdd, result.EffectiveRemove = guardedLabelDigest(effectiveAdd), guardedLabelDigest(effectiveRemove)
	result.Status, result.Complete = "would_apply", true
	result.ProposalHash = guardedLabelProposalHash(result, current, desired, effectiveAdd, effectiveRemove)
	read.result, read.effectiveAdd, read.effectiveRemove = result, effectiveAdd, effectiveRemove
	return read, nil
}

func (s *JiraService) readGuardedLabelEvidence(ctx context.Context, port domain.JiraGuardedLabelPort, reference, requestedKey, expectedID string) (*jiraGuardedLabelSnapshot, error) {
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Jira backend identity", domain.ErrCheckFailed)
	}
	evidence, err := port.ReadGuardedLabelSnapshot(ctx, reference)
	if err != nil {
		return nil, err
	}
	if !evidence.Complete || !canonicalPositiveNumericString(evidence.ID) || evidence.Key != requestedKey ||
		!domain.ValidJiraIssueKey(evidence.Key) || !domain.ValidJiraIssueKey(evidence.Project+"-1") || !strings.HasPrefix(evidence.Key, evidence.Project+"-") ||
		(expectedID != "" && evidence.ID != expectedID) || len(evidence.Labels) > jiraGuardedLabelMaxCurrent ||
		evidence.Labels == nil || !sort.StringsAreSorted(evidence.Labels) || !validGuardedEvidenceLabels(evidence.Labels) {
		return nil, fmt.Errorf("%w: Jira returned missing, moved, or malformed label evidence", domain.ErrCheckFailed)
	}
	updatedTime, err := parseJiraUpdatedTime(evidence.Updated)
	if err != nil || evidence.Updated == "" || strings.TrimSpace(evidence.Updated) != evidence.Updated {
		return nil, fmt.Errorf("%w: Jira returned an unsupported updated marker", domain.ErrCheckFailed)
	}
	return &jiraGuardedLabelSnapshot{evidence: evidence, updatedTime: updatedTime, backendHash: backendHash}, nil
}

func validGuardedEvidenceLabels(labels []string) bool {
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		if label == "" || len(label) > jiraGuardedLabelMaxBytes || !utf8.ValidString(label) {
			return false
		}
		if _, duplicate := seen[label]; duplicate {
			return false
		}
		seen[label] = struct{}{}
	}
	return true
}

func guardedLabelDesired(current, add, remove []string) (desired, effectiveAdd, effectiveRemove []string) {
	currentSet := make(map[string]struct{}, len(current))
	for _, label := range current {
		currentSet[label] = struct{}{}
	}
	removeSet := make(map[string]struct{}, len(remove))
	for _, label := range remove {
		removeSet[label] = struct{}{}
		if _, exists := currentSet[label]; exists {
			effectiveRemove = append(effectiveRemove, label)
		}
	}
	for _, label := range current {
		if _, remove := removeSet[label]; !remove {
			desired = append(desired, label)
		}
	}
	for _, label := range add {
		if _, exists := currentSet[label]; !exists {
			effectiveAdd = append(effectiveAdd, label)
			desired = append(desired, label)
		}
	}
	sort.Strings(desired)
	return desired, effectiveAdd, effectiveRemove
}

func guardedLabelDigest(values []string) JiraGuardedLabelDigest {
	encoded, _ := json.Marshal(values)
	sum := sha256.Sum256(encoded)
	return JiraGuardedLabelDigest{Count: len(values), SHA256: hex.EncodeToString(sum[:])}
}

func guardedLabelProposalHash(result *JiraGuardedLabelResult, current, desired, effectiveAdd, effectiveRemove []string) string {
	payload := struct {
		SchemaVersion   int                    `json:"schema_version"`
		Operation       string                 `json:"operation"`
		BackendSHA256   string                 `json:"backend_sha256"`
		RequestedKey    string                 `json:"requested_key"`
		IssueID         string                 `json:"issue_id"`
		Key             string                 `json:"key"`
		Project         string                 `json:"project"`
		Updated         string                 `json:"updated"`
		Add             []string               `json:"add"`
		Remove          []string               `json:"remove"`
		Current         []string               `json:"current"`
		Desired         []string               `json:"desired"`
		EffectiveAdd    []string               `json:"effective_add"`
		EffectiveRemove []string               `json:"effective_remove"`
		Bounds          JiraGuardedLabelBounds `json:"bounds"`
	}{result.SchemaVersion, result.Operation, result.BackendSHA256, result.RequestedKey, result.IssueID, result.Key, result.Project, result.Updated,
		result.Add, result.Remove, current, desired, effectiveAdd, effectiveRemove, result.Bounds}
	encoded, _ := json.Marshal(payload)
	return guardedProposalDigest(encoded)
}

func guardedLabelFailure(message string, cause error, closed, ambiguous bool) error {
	return &jiraGuardedLabelError{message: message, cause: preserveGuardedBudgetCause(cause, sanitizeJiraDescriptionEditCause(cause)), closed: closed, ambiguous: ambiguous}
}
