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
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

// JiraCommentBodyMaxBytes bounds the native Jira-wiki body accepted by the
// reviewed comment workflow. The bytes are otherwise preserved exactly.
const JiraCommentBodyMaxBytes = 1 << 20

type JiraCommentActor struct {
	Name string `json:"name"`
	Key  string `json:"key,omitempty"`
}

type JiraCommentAddOpts struct {
	Body                 []byte
	Apply                bool
	ExpectedProposalHash string
}

type JiraCommentAddResult struct {
	Key            string           `json:"key"`
	Mode           string           `json:"mode"`
	Status         string           `json:"status"`
	Body           string           `json:"body"`
	BodySHA256     string           `json:"body_sha256"`
	BodyBytes      int              `json:"body_bytes"`
	Actor          JiraCommentActor `json:"actor"`
	CurrentCount   int              `json:"current_count"`
	BaselineSHA256 string           `json:"baseline_sha256"`
	ProposalHash   string           `json:"proposal_hash"`
	Created        *domain.Comment  `json:"created,omitempty"`
	Complete       bool             `json:"complete"`
	Reconciled     bool             `json:"reconciled,omitempty"`
}

type jiraCommentWriteError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *jiraCommentWriteError) Error() string { return e.message }

func (e *jiraCommentWriteError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}

func (e *jiraCommentWriteError) DiagnosticAmbiguousWrite() bool { return e != nil && e.ambiguous }

func (s *JiraService) AddCommentGuarded(ctx context.Context, key string, opts JiraCommentAddOpts) (*JiraCommentAddResult, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("%w: issue key is required", domain.ErrUsage)
	}
	body, err := ValidateJiraCommentBody(opts.Body)
	if err != nil {
		return nil, err
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the dry-run first", domain.ErrUsage)
	}
	actor, err := s.jiraCommentActor(ctx)
	if err != nil {
		return nil, err
	}
	current, baselineIDs, baselineSHA256, err := s.jiraCommentBaseline(ctx, key)
	if err != nil {
		return nil, err
	}
	bodySum := sha256.Sum256(body)
	bodySHA256 := hex.EncodeToString(bodySum[:])
	proposalHash := jiraCommentProposalHash(key, body, bodySHA256, actor, baselineIDs)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	result := &JiraCommentAddResult{
		Key: key, Mode: mode, Status: "would_apply", Body: string(body), BodySHA256: bodySHA256,
		BodyBytes: len(body), Actor: actor, CurrentCount: len(current), BaselineSHA256: baselineSHA256,
		ProposalHash: proposalHash, Complete: true,
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: comment proposal changed since review: expected hash %q, got %q", domain.ErrCheckFailed, strings.TrimSpace(opts.ExpectedProposalHash), proposalHash)
	}
	if !opts.Apply {
		return result, nil
	}

	prewrite, prewriteIDs, prewriteSHA256, err := s.jiraCommentBaseline(ctx, key)
	if err != nil {
		result.Status = "conflict"
		result.Complete = false
		return result, &jiraCommentWriteError{
			message: "comment baseline could not be revalidated immediately before the write",
			cause:   err,
			closed:  true,
		}
	}
	if prewriteSHA256 != baselineSHA256 || !equalStrings(prewriteIDs, baselineIDs) {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: comment baseline changed since review; rerun the dry-run", domain.ErrCheckFailed)
	}
	if reason := changedCommentBaselineMember(current, prewrite); reason != "" {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: comment baseline changed since review: %s; rerun the dry-run", domain.ErrCheckFailed, reason)
	}

	created, writeErr := s.tr.AddComment(domain.WithSingleAttempt(ctx), key, body)
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "not_applied"
		return result, &jiraCommentWriteError{message: "Jira rejected the comment add; the comment was not applied", cause: writeErr}
	}

	readback, _, _, readbackErr := s.jiraCommentBaseline(ctx, key)
	if readbackErr != nil {
		result.Status = "unverifiable"
		result.Complete = false
		return result, jiraCommentAmbiguousError("comment add outcome is unverifiable; complete readback failed; do not replay automatically", errors.Join(writeErr, readbackErr))
	}
	result.Reconciled = true
	if reason := changedCommentBaselineMember(prewrite, readback); reason != "" {
		result.Status = "conflict"
		return result, jiraCommentAmbiguousError("comment add outcome conflicts with the reviewed baseline; "+reason+"; do not replay automatically", writeErr)
	}

	baselineSet := make(map[string]bool, len(prewriteIDs))
	for _, id := range prewriteIDs {
		baselineSet[id] = true
	}
	newComments := make([]domain.Comment, 0, len(readback)-len(prewrite))
	for _, comment := range readback {
		if !baselineSet[comment.ID] {
			newComments = append(newComments, comment)
		}
	}
	returnedID := ""
	if created != nil {
		returnedID = strings.TrimSpace(created.ID)
	}
	if returnedID != "" {
		for i := range newComments {
			if newComments[i].ID != returnedID {
				continue
			}
			if jiraCommentMatches(newComments[i], body, actor) {
				result.Status = "applied"
				result.Created = &newComments[i]
				return result, nil
			}
			result.Status = "conflict"
			return result, jiraCommentAmbiguousError("comment add outcome conflicts with the returned comment identity; do not replay automatically", writeErr)
		}
		result.Status = "conflict"
		return result, jiraCommentAmbiguousError("comment add outcome conflicts because the returned comment identity is absent from complete readback; do not replay automatically", writeErr)
	}

	matches := make([]domain.Comment, 0, 1)
	for _, comment := range newComments {
		if jiraCommentMatches(comment, body, actor) {
			matches = append(matches, comment)
		}
	}
	if len(matches) == 1 {
		result.Status = "applied"
		result.Created = &matches[0]
		return result, nil
	}
	if len(matches) > 1 {
		result.Status = "conflict"
		return result, jiraCommentAmbiguousError(fmt.Sprintf("comment add outcome conflicts because complete readback found %d indistinguishable new comments; do not replay automatically", len(matches)), writeErr)
	}
	result.Status = "unverifiable"
	return result, jiraCommentAmbiguousError("comment add outcome is unverifiable because complete readback found no exact new comment; do not replay automatically", writeErr)
}

func ValidateJiraCommentBody(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: comment body must not be empty", domain.ErrUsage)
	}
	if len(raw) > JiraCommentBodyMaxBytes {
		return nil, fmt.Errorf("%w: comment body exceeds the %d MiB limit", domain.ErrUsage, JiraCommentBodyMaxBytes>>20)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("%w: comment body is not valid UTF-8", domain.ErrUsage)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("%w: comment body must not be empty", domain.ErrUsage)
	}
	return append([]byte(nil), raw...), nil
}

func (s *JiraService) jiraCommentActor(ctx context.Context) (JiraCommentActor, error) {
	user, err := s.tr.CurrentUser(ctx)
	if err != nil {
		return JiraCommentActor{}, err
	}
	if user == nil || strings.TrimSpace(user.Name) == "" {
		return JiraCommentActor{}, fmt.Errorf("%w: current Jira Data Center user has no stable username", domain.ErrCheckFailed)
	}
	return JiraCommentActor{Name: strings.TrimSpace(user.Name), Key: strings.TrimSpace(user.Key)}, nil
}

func (s *JiraService) jiraCommentBaseline(ctx context.Context, key string) ([]domain.Comment, []string, string, error) {
	comments, err := s.tr.ListComments(ctx, key)
	if err != nil {
		return nil, nil, "", err
	}
	sorted, ids, hash, err := normalizeJiraCommentBaseline(comments)
	if err != nil {
		return nil, nil, "", err
	}
	return sorted, ids, hash, nil
}

func normalizeJiraCommentBaseline(comments []domain.Comment) ([]domain.Comment, []string, string, error) {
	sorted := append([]domain.Comment(nil), comments...)
	seen := make(map[string]bool, len(sorted))
	for _, comment := range sorted {
		id := strings.TrimSpace(comment.ID)
		if id == "" || id != comment.ID || seen[id] {
			return nil, nil, "", fmt.Errorf("%w: Jira returned a comment baseline with a missing or duplicate identity", domain.ErrCheckFailed)
		}
		seen[id] = true
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	ids := make([]string, len(sorted))
	for i := range sorted {
		ids[i] = sorted[i].ID
	}
	canonical, _ := json.Marshal(struct {
		SchemaVersion int      `json:"schema_version"`
		IDs           []string `json:"ids"`
	}{1, ids})
	sum := sha256.Sum256(canonical)
	return sorted, ids, hex.EncodeToString(sum[:]), nil
}

func jiraCommentProposalHash(key string, body []byte, bodySHA256 string, actor JiraCommentActor, baselineIDs []string) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion int      `json:"schema_version"`
		Key           string   `json:"key"`
		Body          string   `json:"body"`
		BodySHA256    string   `json:"body_sha256"`
		BodySize      int      `json:"body_size"`
		ActorName     string   `json:"actor_name"`
		ActorKey      string   `json:"actor_key"`
		BaselineIDs   []string `json:"baseline_ids"`
	}{1, key, string(body), bodySHA256, len(body), actor.Name, actor.Key, baselineIDs})
	return guardedProposalDigest(canonical)
}

func jiraCommentMatches(comment domain.Comment, body []byte, actor JiraCommentActor) bool {
	if comment.Body != string(body) {
		return false
	}
	if comment.AuthorName != actor.Name {
		return false
	}
	return actor.Key == "" || comment.AuthorKey == actor.Key
}

func changedCommentBaselineMember(before, after []domain.Comment) string {
	afterByID := make(map[string]domain.Comment, len(after))
	for _, comment := range after {
		afterByID[comment.ID] = comment
	}
	for _, prior := range before {
		current, ok := afterByID[prior.ID]
		if !ok {
			return fmt.Sprintf("baseline comment %s is missing", prior.ID)
		}
		if prior.Body != current.Body || prior.AuthorName != current.AuthorName || prior.AuthorKey != current.AuthorKey || prior.Created != current.Created {
			return fmt.Sprintf("baseline comment %s changed", prior.ID)
		}
	}
	return ""
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func jiraCommentAmbiguousError(message string, cause error) error {
	return &jiraCommentWriteError{message: message, cause: cause, closed: true, ambiguous: true}
}

func JiraCommentAddText(result *JiraCommentAddResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("status: %s\nkey: %s\nproposal_hash: %s\nbody_sha256: %s\nbody_bytes: %d",
		result.Status, result.Key, result.ProposalHash, result.BodySHA256, result.BodyBytes)
}
