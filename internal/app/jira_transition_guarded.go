package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const jiraTransitionProposalSchemaVersion = 1

// JiraTransitionFieldInput preserves one repeated --field entry until the app
// boundary has rejected empty or duplicate keys. Value follows the legacy Jira
// transition coercion contract: valid objects/arrays are typed; scalars remain
// exact strings.
type JiraTransitionFieldInput struct {
	Field string
	Value string
}

type JiraTransitionGuardedOpts struct {
	To                   string
	Comment              []byte
	Fields               []JiraTransitionFieldInput
	Apply                bool
	ExpectedProposalHash string
}

type JiraTransitionStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Updated string `json:"updated"`
}

type JiraTransitionSelection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   string `json:"to"`
	ToID string `json:"to_id"`
}

type JiraTransitionField struct {
	Field            string `json:"field"`
	Current          any    `json:"current"`
	Desired          any    `json:"desired"`
	CurrentPresent   bool   `json:"current_present"`
	CurrentCanonical string `json:"current_canonical"`
	DesiredCanonical string `json:"desired_canonical"`
}

type JiraTransitionComment struct {
	Body           string           `json:"body"`
	BodySHA256     string           `json:"body_sha256"`
	BodyBytes      int              `json:"body_bytes"`
	Actor          JiraCommentActor `json:"actor"`
	CurrentCount   int              `json:"current_count"`
	BaselineSHA256 string           `json:"baseline_sha256"`
	Created        *domain.Comment  `json:"created,omitempty"`
}

type JiraTransitionGuardedResult struct {
	SchemaVersion int                     `json:"schema_version"`
	RequestedKey  string                  `json:"requested_key"`
	Key           string                  `json:"key"`
	IssueID       string                  `json:"issue_id"`
	Mode          string                  `json:"mode"`
	Status        string                  `json:"status"`
	CurrentStatus JiraTransitionStatus    `json:"current_status"`
	Transition    JiraTransitionSelection `json:"transition"`
	Fields        []JiraTransitionField   `json:"fields"`
	Comment       *JiraTransitionComment  `json:"comment,omitempty"`
	ProposalHash  string                  `json:"proposal_hash"`
	Complete      bool                    `json:"complete"`
	Reconciled    bool                    `json:"reconciled,omitempty"`
}

type jiraTransitionWriteError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *jiraTransitionWriteError) Error() string { return definitiveWriteMessage(e.message, e.cause) }

func (e *jiraTransitionWriteError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}

func (e *jiraTransitionWriteError) DiagnosticAmbiguousWrite() bool {
	return e != nil && e.ambiguous
}

type jiraTransitionPreparedField struct {
	name      string
	desired   any
	canonical string
}

type jiraTransitionSnapshot struct {
	result      *JiraTransitionGuardedResult
	issueFields []string
	desired     map[string]any
	comments    []domain.Comment
	commentIDs  []string
	commentBody []byte
	updatedTime time.Time
}

// TransitionGuarded previews or applies one reviewed Jira transition. It does
// not treat an already-target status as idempotent: self-transitions and other
// workflow actions may have deliberate side effects.
func (s *JiraService) TransitionGuarded(ctx context.Context, requestedKey string, opts JiraTransitionGuardedOpts) (*JiraTransitionGuardedResult, error) {
	requestedKey = strings.TrimSpace(requestedKey)
	if requestedKey == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	to := strings.TrimSpace(opts.To)
	if to == "" {
		return nil, fmt.Errorf("%w: transition name or target status is required", domain.ErrUsage)
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	prepared, err := prepareJiraTransitionFields(opts.Fields)
	if err != nil {
		return nil, err
	}
	var body []byte
	if opts.Comment != nil {
		body, err = ValidateJiraCommentBody(opts.Comment)
		if err != nil {
			return nil, err
		}
	}
	if opts.Apply {
		if _, ok := s.tr.(domain.JiraTransitionWriter); !ok {
			return nil, fmt.Errorf("%w: configured Jira backend does not support exact transition application", domain.ErrCheckFailed)
		}
	}

	initial, err := s.buildJiraTransitionSnapshot(ctx, requestedKey, to, prepared, body)
	if err != nil {
		return nil, err
	}
	initial.result.Mode = "dry-run"
	if opts.Apply {
		initial.result.Mode = "apply"
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != initial.result.ProposalHash {
		initial.result.Status = "conflict"
		return initial.result, fmt.Errorf("%w: transition proposal changed since review: expected hash %q, got %q", domain.ErrCheckFailed, strings.TrimSpace(opts.ExpectedProposalHash), initial.result.ProposalHash)
	}
	if !opts.Apply {
		return initial.result, nil
	}

	prewrite, err := s.buildJiraTransitionSnapshot(ctx, requestedKey, to, prepared, body)
	if err != nil {
		initial.result.Status = "conflict"
		initial.result.Complete = false
		return initial.result, &jiraTransitionWriteError{
			message: "transition proposal could not be revalidated immediately before the write",
			cause:   err,
			closed:  true,
		}
	}
	if prewrite.result.ProposalHash != initial.result.ProposalHash {
		initial.result.Status = "conflict"
		return initial.result, fmt.Errorf("%w: transition proposal changed since review; rerun the dry-run", domain.ErrCheckFailed)
	}

	writer := s.tr.(domain.JiraTransitionWriter)
	request := domain.JiraTransitionRequest{
		ID:      prewrite.result.Transition.ID,
		Fields:  cloneTransitionValues(prewrite.desired),
		Comment: append([]byte(nil), prewrite.commentBody...),
	}
	writeErr := writer.TransitionByID(domain.WithSingleAttempt(ctx), prewrite.result.Key, request)
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		initial.result.Status = "not_applied"
		return initial.result, &jiraTransitionWriteError{
			message: "Jira rejected the reviewed transition; the transition was not applied",
			cause:   writeErr,
		}
	}

	readback, readbackErr := s.readJiraTransitionEndState(ctx, prewrite)
	if readbackErr != nil {
		initial.result.Status = "unverifiable"
		initial.result.Complete = false
		return initial.result, jiraTransitionAmbiguousError(
			"transition outcome is unverifiable; complete readback failed; do not replay automatically",
			errors.Join(writeErr, readbackErr),
		)
	}
	initial.result.Reconciled = true

	unchanged := jiraTransitionEndStateUnchanged(prewrite, readback)
	exact, created, reason := jiraTransitionEndStateExact(prewrite, readback)
	if exact {
		initial.result.Status = "applied"
		if initial.result.Comment != nil {
			initial.result.Comment.Created = created
		}
		return initial.result, nil
	}
	if unchanged {
		initial.result.Status = "unverifiable"
		return initial.result, jiraTransitionAmbiguousError(
			"transition outcome is unverifiable because complete readback is unchanged; do not replay automatically",
			writeErr,
		)
	}
	initial.result.Status = "conflict"
	return initial.result, jiraTransitionAmbiguousError(
		"transition outcome conflicts with the reviewed end state; "+reason+"; do not replay automatically",
		writeErr,
	)
}

func prepareJiraTransitionFields(inputs []JiraTransitionFieldInput) ([]jiraTransitionPreparedField, error) {
	prepared := make([]jiraTransitionPreparedField, 0, len(inputs))
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		name := strings.TrimSpace(input.Field)
		if name == "" || name != input.Field {
			return nil, fmt.Errorf("%w: transition field key is empty or non-canonical", domain.ErrUsage)
		}
		if seen[name] {
			return nil, fmt.Errorf("%w: duplicate transition field key %q", domain.ErrUsage, name)
		}
		seen[name] = true
		desired := coerceJiraTransitionField(input.Value)
		canonical, err := canonicalJiraTransitionValue(desired)
		if err != nil {
			return nil, fmt.Errorf("%w: transition field %q has an unsupported value", domain.ErrUsage, name)
		}
		prepared = append(prepared, jiraTransitionPreparedField{name: name, desired: desired, canonical: canonical})
	}
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].name < prepared[j].name })
	return prepared, nil
}

func coerceJiraTransitionField(value string) any {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err == nil {
			var trailing any
			if errors.Is(decoder.Decode(&trailing), io.EOF) {
				return decoded
			}
		}
	}
	return value
}

func canonicalJiraTransitionValue(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (s *JiraService) buildJiraTransitionSnapshot(ctx context.Context, requestedKey, to string, prepared []jiraTransitionPreparedField, body []byte) (*jiraTransitionSnapshot, error) {
	issueFields := make([]string, 0, len(prepared)+2)
	for _, field := range prepared {
		issueFields = append(issueFields, field.name)
	}
	issueFields = append(issueFields, "status", "updated")
	issue, err := s.tr.GetIssue(ctx, requestedKey, issueFields)
	if err != nil {
		return nil, err
	}
	status, updatedTime, err := validateJiraTransitionIssue(issue)
	if err != nil {
		return nil, err
	}
	transitions, err := s.tr.Transitions(ctx, issue.Key)
	if err != nil {
		return nil, err
	}
	selection, err := resolveJiraTransition(to, transitions)
	if err != nil {
		return nil, err
	}

	fields := make([]JiraTransitionField, 0, len(prepared))
	desired := make(map[string]any, len(prepared))
	for _, field := range prepared {
		current, present := issue.Fields[field.name]
		if !present {
			return nil, fmt.Errorf("%w: Jira omitted requested transition field %q", domain.ErrCheckFailed, field.name)
		}
		currentCanonical, err := canonicalJiraTransitionValue(current)
		if err != nil {
			return nil, fmt.Errorf("%w: Jira returned a non-canonical value for transition field %q", domain.ErrCheckFailed, field.name)
		}
		fields = append(fields, JiraTransitionField{
			Field: field.name, Current: current, Desired: field.desired, CurrentPresent: true,
			CurrentCanonical: currentCanonical, DesiredCanonical: field.canonical,
		})
		desired[field.name] = field.desired
	}

	result := &JiraTransitionGuardedResult{
		SchemaVersion: jiraTransitionProposalSchemaVersion,
		RequestedKey:  requestedKey,
		Key:           issue.Key,
		IssueID:       issue.ID,
		Status:        "would_apply",
		CurrentStatus: status,
		Transition:    selection,
		Fields:        fields,
		Complete:      true,
	}
	snapshot := &jiraTransitionSnapshot{
		result: result, issueFields: issueFields, desired: desired,
		commentBody: append([]byte(nil), body...), updatedTime: updatedTime,
	}
	if body != nil {
		actor, err := s.jiraCommentActor(ctx)
		if err != nil {
			return nil, err
		}
		comments, ids, baselineHash, err := s.jiraCommentBaseline(ctx, issue.Key)
		if err != nil {
			return nil, err
		}
		bodySum := sha256.Sum256(body)
		result.Comment = &JiraTransitionComment{
			Body: string(body), BodySHA256: hex.EncodeToString(bodySum[:]), BodyBytes: len(body),
			Actor: actor, CurrentCount: len(comments), BaselineSHA256: baselineHash,
		}
		snapshot.comments = comments
		snapshot.commentIDs = ids
	}
	hash, err := jiraTransitionProposalHash(result, snapshot.commentIDs)
	if err != nil {
		return nil, err
	}
	result.ProposalHash = hash
	return snapshot, nil
}

func validateJiraTransitionIssue(issue *domain.Issue) (JiraTransitionStatus, time.Time, error) {
	if issue == nil {
		return JiraTransitionStatus{}, time.Time{}, fmt.Errorf("%w: Jira returned no issue for transition review", domain.ErrCheckFailed)
	}
	if !canonicalJiraTransitionIdentity(issue.ID) || !canonicalJiraTransitionIdentity(issue.Key) ||
		!canonicalJiraTransitionIdentity(issue.StatusID) || !canonicalJiraTransitionIdentity(issue.Status) {
		return JiraTransitionStatus{}, time.Time{}, fmt.Errorf("%w: Jira returned a missing or malformed issue/status identity", domain.ErrCheckFailed)
	}
	rawStatus, present := issue.Fields["status"]
	statusObject, ok := rawStatus.(map[string]any)
	if !present || !ok || statusObject["id"] != issue.StatusID || statusObject["name"] != issue.Status {
		return JiraTransitionStatus{}, time.Time{}, fmt.Errorf("%w: Jira returned an inconsistent issue status identity", domain.ErrCheckFailed)
	}
	updated, present := issue.Fields["updated"]
	if !present {
		return JiraTransitionStatus{}, time.Time{}, fmt.Errorf("%w: Jira omitted the updated marker", domain.ErrCheckFailed)
	}
	updatedString, ok := updated.(string)
	if !ok || !canonicalJiraTransitionIdentity(updatedString) {
		return JiraTransitionStatus{}, time.Time{}, fmt.Errorf("%w: Jira returned a missing or malformed updated marker", domain.ErrCheckFailed)
	}
	updatedTime, err := parseJiraUpdatedTime(updatedString)
	if err != nil {
		return JiraTransitionStatus{}, time.Time{}, fmt.Errorf("%w: Jira returned an unsupported updated datetime", domain.ErrCheckFailed)
	}
	return JiraTransitionStatus{ID: issue.StatusID, Name: issue.Status, Updated: updatedString}, updatedTime, nil
}

// parseJiraUpdatedTime accepts only the timestamp representations Jira Data
// Center uses for issue updated fields. Date-only values and prose/local-time
// fallbacks are intentionally excluded because they cannot order write
// evidence precisely.
func parseJiraUpdatedTime(value string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999-0700",
		"2006-01-02T15:04:05-0700",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported Jira updated datetime")
}

func canonicalJiraTransitionIdentity(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func resolveJiraTransition(to string, transitions []domain.TransitionDef) (JiraTransitionSelection, error) {
	seenIDs := make(map[string]bool, len(transitions))
	nameMatches := make([]domain.TransitionDef, 0, 1)
	statusMatches := make([]domain.TransitionDef, 0, 1)
	for _, transition := range transitions {
		if !canonicalJiraTransitionIdentity(transition.ID) ||
			!canonicalJiraTransitionIdentity(transition.Name) ||
			!canonicalJiraTransitionIdentity(transition.To) ||
			!canonicalJiraTransitionIdentity(transition.ToID) || seenIDs[transition.ID] {
			return JiraTransitionSelection{}, fmt.Errorf("%w: Jira returned a missing, duplicate, or malformed transition identity", domain.ErrCheckFailed)
		}
		seenIDs[transition.ID] = true
		if strings.EqualFold(transition.Name, to) {
			nameMatches = append(nameMatches, transition)
		}
		if strings.EqualFold(transition.To, to) {
			statusMatches = append(statusMatches, transition)
		}
	}
	selected := nameMatches
	matchKind := "name"
	if len(nameMatches) == 0 {
		selected = statusMatches
		matchKind = "target status"
	}
	if len(selected) == 0 {
		return JiraTransitionSelection{}, fmt.Errorf("%w: no transition uniquely matches the requested name or target status", domain.ErrUsage)
	}
	if len(selected) != 1 {
		return JiraTransitionSelection{}, fmt.Errorf("%w: multiple transitions match the requested %s", domain.ErrCheckFailed, matchKind)
	}
	transition := selected[0]
	return JiraTransitionSelection{ID: transition.ID, Name: transition.Name, To: transition.To, ToID: transition.ToID}, nil
}

func jiraTransitionProposalHash(result *JiraTransitionGuardedResult, commentIDs []string) (string, error) {
	type fieldEntry struct {
		Field            string `json:"field"`
		CurrentPresent   bool   `json:"current_present"`
		CurrentCanonical string `json:"current_canonical"`
		DesiredCanonical string `json:"desired_canonical"`
	}
	type transitionEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		To   string `json:"to"`
		ToID string `json:"to_id"`
	}
	fields := make([]fieldEntry, len(result.Fields))
	for i, field := range result.Fields {
		fields[i] = fieldEntry{field.Field, field.CurrentPresent, field.CurrentCanonical, field.DesiredCanonical}
	}
	var comment any
	if result.Comment != nil {
		comment = struct {
			Body         string           `json:"body"`
			BodySHA256   string           `json:"body_sha256"`
			BodyBytes    int              `json:"body_bytes"`
			Actor        JiraCommentActor `json:"actor"`
			BaselineIDs  []string         `json:"baseline_ids"`
			BaselineHash string           `json:"baseline_sha256"`
		}{result.Comment.Body, result.Comment.BodySHA256, result.Comment.BodyBytes, result.Comment.Actor, commentIDs, result.Comment.BaselineSHA256}
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion int                  `json:"schema_version"`
		RequestedKey  string               `json:"requested_key"`
		Key           string               `json:"key"`
		IssueID       string               `json:"issue_id"`
		CurrentStatus JiraTransitionStatus `json:"current_status"`
		Transition    transitionEntry      `json:"transition"`
		Fields        []fieldEntry         `json:"fields"`
		Comment       any                  `json:"comment,omitempty"`
	}{
		result.SchemaVersion, result.RequestedKey, result.Key, result.IssueID, result.CurrentStatus,
		transitionEntry{result.Transition.ID, result.Transition.Name, result.Transition.To, result.Transition.ToID},
		fields, comment,
	})
	if err != nil {
		return "", err
	}
	return guardedProposalDigest(encoded), nil
}

func (s *JiraService) readJiraTransitionEndState(ctx context.Context, before *jiraTransitionSnapshot) (*jiraTransitionSnapshot, error) {
	issue, err := s.tr.GetIssue(ctx, before.result.Key, before.issueFields)
	if err != nil {
		return nil, err
	}
	status, updatedTime, err := validateJiraTransitionIssue(issue)
	if err != nil {
		return nil, err
	}
	fields := make([]JiraTransitionField, len(before.result.Fields))
	for i, prior := range before.result.Fields {
		current, present := issue.Fields[prior.Field]
		if !present {
			return nil, fmt.Errorf("%w: Jira omitted a requested field from transition readback", domain.ErrCheckFailed)
		}
		canonical, err := canonicalJiraTransitionValue(current)
		if err != nil {
			return nil, fmt.Errorf("%w: Jira returned a non-canonical transition readback value", domain.ErrCheckFailed)
		}
		fields[i] = prior
		fields[i].Current = current
		fields[i].CurrentPresent = true
		fields[i].CurrentCanonical = canonical
	}
	result := &JiraTransitionGuardedResult{
		SchemaVersion: before.result.SchemaVersion, RequestedKey: before.result.RequestedKey,
		Key: issue.Key, IssueID: issue.ID, Status: "would_apply", CurrentStatus: status,
		Transition: before.result.Transition, Fields: fields, Complete: true,
	}
	readback := &jiraTransitionSnapshot{result: result, issueFields: before.issueFields, desired: before.desired, updatedTime: updatedTime}
	if before.result.Comment != nil {
		comments, ids, hash, err := s.jiraCommentBaseline(ctx, issue.Key)
		if err != nil {
			return nil, err
		}
		copyComment := *before.result.Comment
		copyComment.CurrentCount = len(comments)
		copyComment.BaselineSHA256 = hash
		copyComment.Created = nil
		result.Comment = &copyComment
		readback.comments = comments
		readback.commentIDs = ids
		readback.commentBody = before.commentBody
	}
	return readback, nil
}

func jiraTransitionEndStateUnchanged(before, after *jiraTransitionSnapshot) bool {
	if !jiraTransitionIssueAndFieldsEqual(before, after) {
		return false
	}
	if before.result.Comment == nil {
		return true
	}
	if !equalStrings(before.commentIDs, after.commentIDs) || len(before.comments) != len(after.comments) {
		return false
	}
	return changedCommentBaselineMember(before.comments, after.comments) == "" &&
		changedCommentBaselineMember(after.comments, before.comments) == ""
}

func jiraTransitionIssueAndFieldsEqual(before, after *jiraTransitionSnapshot) bool {
	if before.result.Key != after.result.Key || before.result.IssueID != after.result.IssueID ||
		before.result.CurrentStatus != after.result.CurrentStatus || len(before.result.Fields) != len(after.result.Fields) {
		return false
	}
	for i := range before.result.Fields {
		if before.result.Fields[i].Field != after.result.Fields[i].Field ||
			before.result.Fields[i].CurrentPresent != after.result.Fields[i].CurrentPresent ||
			before.result.Fields[i].CurrentCanonical != after.result.Fields[i].CurrentCanonical {
			return false
		}
	}
	return true
}

func jiraTransitionEndStateExact(before, after *jiraTransitionSnapshot) (bool, *domain.Comment, string) {
	if after.result.Key != before.result.Key || after.result.IssueID != before.result.IssueID {
		return false, nil, "issue identity changed"
	}
	if after.result.CurrentStatus.ID != before.result.Transition.ToID ||
		after.result.CurrentStatus.Name != before.result.Transition.To {
		return false, nil, "target status is not exact"
	}
	if !after.updatedTime.After(before.updatedTime) {
		return false, nil, "updated marker did not advance"
	}
	for _, field := range after.result.Fields {
		if !field.CurrentPresent || field.CurrentCanonical != field.DesiredCanonical {
			return false, nil, "requested field state is not exact"
		}
	}
	if before.result.Comment == nil {
		return true, nil, ""
	}
	if reason := changedCommentBaselineMember(before.comments, after.comments); reason != "" {
		return false, nil, reason
	}
	baseline := make(map[string]bool, len(before.commentIDs))
	for _, id := range before.commentIDs {
		baseline[id] = true
	}
	matches := make([]domain.Comment, 0, 1)
	for _, comment := range after.comments {
		if baseline[comment.ID] {
			continue
		}
		if jiraCommentMatches(comment, before.commentBody, before.result.Comment.Actor) {
			matches = append(matches, comment)
		}
	}
	if len(matches) != 1 {
		if len(matches) > 1 {
			return false, nil, "multiple indistinguishable new comments were found"
		}
		return false, nil, "the reviewed comment was not uniquely attributed"
	}
	created := matches[0]
	return true, &created, ""
}

func cloneTransitionValues(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]any, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func jiraTransitionAmbiguousError(message string, cause error) error {
	return &jiraTransitionWriteError{message: message, cause: cause, closed: true, ambiguous: true}
}
