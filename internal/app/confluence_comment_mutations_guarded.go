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

	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

const confluenceCommentMutationProposalSchemaVersion = 1

// ConfluenceCommentMutationOpts is the closed guarded input shared by inline
// create, reply, resolve, and reopen. Bodies and selections are never emitted.
type ConfluenceCommentMutationOpts struct {
	Operation            domain.ConfluenceCommentMutationOperation
	ThreadID             string
	Body                 []byte
	Selection            []byte
	Occurrence           int
	Apply                bool
	ExpectedProposalHash string
}

// ConfluenceCommentMutationProviderEvidence exposes only the compiled provider
// identity and a digest of the owner-private exact activation.
type ConfluenceCommentMutationProviderEvidence struct {
	ID string `json:"id"`
}

type ConfluenceCommentMutationActor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ConfluenceCommentMutationGuardedResult is deliberately content-free. Exact
// native body and selection bytes participate in hashes but never cross the
// output boundary.
type ConfluenceCommentMutationGuardedResult struct {
	SchemaVersion     int                                       `json:"schema_version"`
	PageID            string                                    `json:"page_id"`
	ThreadID          string                                    `json:"thread_id"`
	Operation         domain.ConfluenceCommentMutationOperation `json:"operation"`
	Mode              string                                    `json:"mode"`
	Status            string                                    `json:"status"`
	PageVersion       int                                       `json:"page_version"`
	ThreadVersion     int                                       `json:"thread_version"`
	SourceState       domain.ConfluenceCommentResolution        `json:"source_state"`
	TargetState       domain.ConfluenceCommentResolution        `json:"target_state,omitempty"`
	BodySHA256        string                                    `json:"body_sha256,omitempty"`
	BodyBytes         int                                       `json:"body_bytes,omitempty"`
	SelectionSHA256   string                                    `json:"selection_sha256,omitempty"`
	SelectionBytes    int                                       `json:"selection_bytes,omitempty"`
	Occurrence        *int                                      `json:"occurrence,omitempty"`
	NumMatches        int                                       `json:"num_matches,omitempty"`
	MatchIndex        *int                                      `json:"match_index,omitempty"`
	HighlightCount    int                                       `json:"highlight_count,omitempty"`
	GeometrySHA256    string                                    `json:"geometry_sha256,omitempty"`
	PageBodySHA256    string                                    `json:"page_body_sha256,omitempty"`
	MarkerCount       *int                                      `json:"marker_count,omitempty"`
	MarkerSHA256      string                                    `json:"marker_sha256,omitempty"`
	Actor             ConfluenceCommentMutationActor            `json:"actor"`
	Provider          ConfluenceCommentMutationProviderEvidence `json:"provider"`
	CurrentCount      int                                       `json:"current_count"`
	BaselineSHA256    string                                    `json:"baseline_sha256"`
	BackendSHA256     string                                    `json:"backend_sha256"`
	ProposalHash      string                                    `json:"proposal_hash"`
	CommentID         string                                    `json:"comment_id,omitempty"`
	MarkerRef         string                                    `json:"marker_ref,omitempty"`
	ResultPageVersion int                                       `json:"result_page_version,omitempty"`
	Complete          bool                                      `json:"complete"`
	Reconciled        bool                                      `json:"reconciled,omitempty"`
	Warning           string                                    `json:"warning"`
}

type confluenceCommentMutationSnapshot struct {
	pageID             string
	pageVersion        int
	actor              ConfluenceCommentMutationActor
	provider           ConfluenceCommentMutationProviderEvidence
	activation         compatibility.Activation
	comments           []ConfluenceCommentResultRecord
	target             ConfluenceCommentResultRecord
	capabilities       domain.ConfluenceCommentCapabilities
	baselineSHA256     string
	backend            string
	configuredIdentity string
	untrustedReference bool
	noOp               bool
}

type confluenceCommentMutationWriteError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *confluenceCommentMutationWriteError) Error() string { return e.message }

func (e *confluenceCommentMutationWriteError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}

func (e *confluenceCommentMutationWriteError) DiagnosticAmbiguousWrite() bool {
	return e != nil && e.ambiguous
}

// MutateCommentGuarded previews or applies one exact inline-thread mutation.
// Apply rebuilds the reviewed snapshot, revalidates it immediately before one
// provider invocation, then reconciles from a complete qualified inventory.
func (s *ConfluenceService) MutateCommentGuarded(ctx context.Context, reference string, opts ConfluenceCommentMutationOpts) (*ConfluenceCommentMutationGuardedResult, error) {
	body, err := validateConfluenceCommentMutationOpts(opts)
	if err != nil {
		return nil, err
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with apply; run the preview first", domain.ErrUsage)
	}
	if opts.Operation == domain.ConfluenceCommentMutationInlineCreate {
		return s.createInlineCommentGuarded(ctx, reference, opts, body)
	}

	snapshot, err := s.confluenceCommentMutationSnapshot(ctx, reference, opts.Operation, opts.ThreadID)
	if err != nil {
		return nil, err
	}
	bodySum := sha256.Sum256(body)
	bodySHA256 := ""
	if opts.Operation == domain.ConfluenceCommentMutationReply {
		bodySHA256 = hex.EncodeToString(bodySum[:])
	}
	proposalHash := confluenceCommentMutationProposalHash(snapshot, opts.Operation, bodySHA256, len(body))
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	backendSum := sha256.Sum256([]byte(snapshot.backend))
	result := &ConfluenceCommentMutationGuardedResult{
		SchemaVersion: confluenceCommentMutationProposalSchemaVersion,
		PageID:        snapshot.pageID, ThreadID: snapshot.target.ID, Operation: opts.Operation,
		Mode: mode, Status: "would_apply", PageVersion: snapshot.pageVersion,
		ThreadVersion: snapshot.target.Version, SourceState: snapshot.target.Resolution,
		TargetState: confluenceCommentMutationTargetState(opts.Operation), BodySHA256: bodySHA256,
		BodyBytes: len(body), Actor: snapshot.actor, Provider: snapshot.provider,
		CurrentCount: len(snapshot.comments), BaselineSHA256: snapshot.baselineSHA256,
		BackendSHA256: hex.EncodeToString(backendSum[:]), ProposalHash: proposalHash,
		Complete: true, Warning: "non_idempotent_write_requires_single_attempt_and_reconciliation",
	}
	if snapshot.noOp {
		result.Status = "no_op"
		result.Warning = ""
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: Confluence comment mutation proposal changed since review", domain.ErrCheckFailed)
	}
	if !opts.Apply || snapshot.noOp {
		return result, nil
	}
	if s.commentMutator == nil {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: Confluence comment mutation provider is unavailable", domain.ErrConfig)
	}
	if snapshot.untrustedReference {
		ctx = domain.WithUntrustedConfluenceReference(ctx)
	}

	prewrite, err := s.confluenceCommentMutationSnapshot(ctx, snapshot.pageID, opts.Operation, snapshot.target.ID)
	if err != nil {
		result.Status = "conflict"
		result.Complete = false
		return result, &confluenceCommentMutationWriteError{
			message: "Confluence comment mutation could not be revalidated immediately before the write",
			cause:   sanitizeConfluenceWriteCause(err), closed: true,
		}
	}
	if prewrite.noOp || confluenceCommentMutationProposalHash(prewrite, opts.Operation, bodySHA256, len(body)) != proposalHash {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: Confluence comment mutation target, actor, provider, page version, or baseline changed since review", domain.ErrCheckFailed)
	}

	providerResult, writeErr := s.commentMutator.MutateConfluenceComment(
		domain.WithSingleAttempt(domain.WithConfluenceCommentContainment(ctx, prewrite.pageID, prewrite.target.ID)),
		domain.ConfluenceCommentMutationRequest{
			Operation: opts.Operation, PageID: prewrite.pageID,
			ThreadID: prewrite.target.ID, BodyStorage: body,
		},
	)
	if writeErr != nil {
		var attemptEvidence interface{ DiagnosticWriteAttempted() bool }
		if errors.As(writeErr, &attemptEvidence) && !attemptEvidence.DiagnosticWriteAttempted() {
			result.Status = "not_applied"
			return result, &confluenceCommentMutationWriteError{
				message: definitiveWriteMessage("Confluence comment mutation stopped before a write was attempted", writeErr),
				cause:   sanitizeConfluenceWriteCause(writeErr), closed: true,
			}
		}
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "not_applied"
		return result, &confluenceCommentMutationWriteError{
			message: definitiveWriteMessage("Confluence rejected the comment mutation; it was not applied", writeErr),
			cause:   sanitizeConfluenceWriteCause(writeErr),
		}
	}

	readback, readbackErr := s.confluenceCommentMutationSnapshot(ctx, prewrite.pageID, opts.Operation, prewrite.target.ID)
	if readbackErr != nil {
		result.Status = "outcome_unknown"
		result.Complete = false
		return result, confluenceCommentMutationAmbiguousError(
			"Confluence comment mutation outcome is unknown; complete readback failed; do not replay automatically",
			errors.Join(sanitizeConfluenceWriteCause(writeErr), sanitizeConfluenceWriteCause(readbackErr)),
		)
	}
	result.Reconciled = true
	if !sameConfluenceCommentMutationContext(prewrite, readback) {
		result.Status = "outcome_unknown"
		return result, confluenceCommentMutationAmbiguousError(
			"Confluence comment mutation outcome is unknown because its page, actor, or provider identity changed; do not replay automatically",
			sanitizeConfluenceWriteCause(writeErr),
		)
	}

	switch opts.Operation {
	case domain.ConfluenceCommentMutationReply:
		return reconcileConfluenceCommentReply(result, prewrite, readback, body, providerResult, writeErr)
	case domain.ConfluenceCommentMutationResolve, domain.ConfluenceCommentMutationReopen:
		return reconcileConfluenceCommentResolution(result, prewrite, readback, providerResult, writeErr)
	default:
		return result, fmt.Errorf("%w: unsupported Confluence comment mutation", domain.ErrUsage)
	}
}

func validateConfluenceCommentMutationOpts(opts ConfluenceCommentMutationOpts) ([]byte, error) {
	if !domain.ValidConfluenceCommentMutationOperation(opts.Operation) {
		return nil, fmt.Errorf("%w: unsupported Confluence comment mutation", domain.ErrUsage)
	}
	if opts.Occurrence < 0 {
		return nil, fmt.Errorf("%w: Confluence selection occurrence must be zero or positive", domain.ErrUsage)
	}
	if opts.Operation == domain.ConfluenceCommentMutationInlineCreate {
		if opts.ThreadID != "" {
			return nil, fmt.Errorf("%w: Confluence inline create must not target an existing thread", domain.ErrUsage)
		}
		if len(opts.Selection) == 0 || !utf8.Valid(opts.Selection) {
			return nil, fmt.Errorf("%w: Confluence inline selection must be non-empty UTF-8", domain.ErrUsage)
		}
		if len(opts.Selection) > ConfluenceFooterCommentBodyMaxBytes {
			return nil, fmt.Errorf("%w: Confluence inline selection exceeds the %d MiB limit", domain.ErrUsage, ConfluenceFooterCommentBodyMaxBytes>>20)
		}
		return validateConfluenceCommentMutationBody(opts.Body, "inline comment")
	}
	if len(opts.Selection) != 0 || opts.Occurrence != 0 {
		return nil, fmt.Errorf("%w: Confluence selection fields are accepted only for inline create", domain.ErrUsage)
	}
	if err := ValidateConfluenceCommentID(opts.ThreadID); err != nil {
		return nil, fmt.Errorf("%w: Confluence thread id is invalid", domain.ErrUsage)
	}
	if opts.Operation != domain.ConfluenceCommentMutationReply {
		if len(opts.Body) != 0 {
			return nil, fmt.Errorf("%w: Confluence resolution mutation must not carry a body", domain.ErrUsage)
		}
		return nil, nil
	}
	return validateConfluenceCommentMutationBody(opts.Body, "reply")
}

func validateConfluenceCommentMutationBody(body []byte, kind string) ([]byte, error) {
	if len(body) == 0 || strings.TrimSpace(string(body)) == "" {
		return nil, fmt.Errorf("%w: Confluence %s body must not be empty", domain.ErrUsage, kind)
	}
	if len(body) > ConfluenceFooterCommentBodyMaxBytes {
		return nil, fmt.Errorf("%w: Confluence %s body exceeds the %d MiB limit", domain.ErrUsage, kind, ConfluenceFooterCommentBodyMaxBytes>>20)
	}
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("%w: Confluence %s body is not valid UTF-8", domain.ErrUsage, kind)
	}
	if csf.HasErrors(csf.Validate(body)) {
		return nil, fmt.Errorf("%w: Confluence %s body is invalid Confluence Storage Format", domain.ErrCheckFailed, kind)
	}
	return append([]byte(nil), body...), nil
}

func (s *ConfluenceService) confluenceCommentMutationSnapshot(ctx context.Context, reference string, operation domain.ConfluenceCommentMutationOperation, threadID string) (confluenceCommentMutationSnapshot, error) {
	if s == nil || s.commentMutationActivation == nil || s.commentMutator == nil {
		return confluenceCommentMutationSnapshot{}, fmt.Errorf("%w: Confluence comment compatibility is not activated", domain.ErrConfig)
	}
	activation := *s.commentMutationActivation
	if err := activation.Validate(compatibility.ProductConfluence); err != nil {
		return confluenceCommentMutationSnapshot{}, err
	}
	backend := strings.TrimRight(strings.TrimSpace(s.baseURL), "/")
	if backend == "" {
		return confluenceCommentMutationSnapshot{}, fmt.Errorf("%w: Confluence backend identity is unavailable", domain.ErrCheckFailed)
	}
	identity, err := confluenceCommentMutationIdentity(activation)
	if err != nil {
		return confluenceCommentMutationSnapshot{}, err
	}
	identityReader, ok := s.store.(domain.ConfluenceCurrentUserReader)
	if !ok {
		return confluenceCommentMutationSnapshot{}, fmt.Errorf("%w: Confluence backend does not expose a stable current-user identity", domain.ErrCheckFailed)
	}
	currentUser, err := identityReader.CurrentConfluenceUser(ctx)
	if err != nil {
		return confluenceCommentMutationSnapshot{}, err
	}
	if err := domain.ValidateConfluenceUserIdentity(currentUser); err != nil {
		return confluenceCommentMutationSnapshot{}, err
	}

	resolved, err := s.ResolvePageReference(ctx, reference)
	if err != nil {
		return confluenceCommentMutationSnapshot{}, err
	}
	ctx = resolved.Context(ctx)
	inventory, err := s.CommentInventory(ctx, resolved.ID, ConfluenceCommentInventoryOpts{Location: "all", State: "all", Depth: "all"})
	if err != nil {
		return confluenceCommentMutationSnapshot{}, err
	}
	if inventory == nil || !inventory.CommentsComplete || !inventory.ThreadsComplete {
		return confluenceCommentMutationSnapshot{}, fmt.Errorf("%w: complete Confluence inline comment evidence is required", domain.ErrCheckFailed)
	}
	if inventory.PageID == "" || inventory.PageVersion <= 0 {
		return confluenceCommentMutationSnapshot{}, fmt.Errorf("%w: Confluence comment page metadata is not reconciled", domain.ErrCheckFailed)
	}
	comments := append([]ConfluenceCommentResultRecord(nil), inventory.Comments...)
	sort.Slice(comments, func(i, j int) bool { return comments[i].ID < comments[j].ID })
	var target ConfluenceCommentResultRecord
	found := false
	for _, comment := range comments {
		if comment.ID == threadID {
			target, found = comment, true
			break
		}
	}
	if !found {
		return confluenceCommentMutationSnapshot{}, fmt.Errorf("%w: Confluence inline thread is not present in the complete inventory", domain.ErrNotFound)
	}
	if target.PageID != inventory.PageID || target.Relation != domain.ConfluenceCommentRelationRoot || target.RootID == nil || *target.RootID != target.ID ||
		target.ParentID != nil || target.Location != domain.ConfluenceCommentLocationInline || target.Version <= 0 {
		return confluenceCommentMutationSnapshot{}, fmt.Errorf("%w: Confluence inline thread is not exactly reconciled", domain.ErrCheckFailed)
	}
	if target.Resolution != domain.ConfluenceCommentResolutionOpen && target.Resolution != domain.ConfluenceCommentResolutionResolved {
		return confluenceCommentMutationSnapshot{}, fmt.Errorf("%w: Confluence inline thread resolution is unavailable", domain.ErrCheckFailed)
	}
	if operation == domain.ConfluenceCommentMutationReply && target.Resolution != domain.ConfluenceCommentResolutionOpen {
		return confluenceCommentMutationSnapshot{}, fmt.Errorf("%w: replies require an open Confluence inline thread", domain.ErrCheckFailed)
	}
	noOp := operation == domain.ConfluenceCommentMutationResolve && target.Resolution == domain.ConfluenceCommentResolutionResolved ||
		operation == domain.ConfluenceCommentMutationReopen && target.Resolution == domain.ConfluenceCommentResolutionOpen
	baselineSHA256, err := confluenceCommentMutationBaselineHash(inventory.PageID, inventory.PageVersion, comments)
	if err != nil {
		return confluenceCommentMutationSnapshot{}, err
	}
	return confluenceCommentMutationSnapshot{
		pageID: inventory.PageID, pageVersion: inventory.PageVersion,
		actor:      ConfluenceCommentMutationActor{ID: strings.TrimSpace(currentUser.ID), DisplayName: strings.TrimSpace(currentUser.DisplayName)},
		provider:   ConfluenceCommentMutationProviderEvidence{ID: activation.ProviderID},
		activation: activation, comments: comments, target: target, capabilities: inventory.Capabilities,
		baselineSHA256: baselineSHA256, backend: backend, configuredIdentity: identity, noOp: noOp,
		untrustedReference: resolved.Untrusted() || domain.UntrustedConfluenceReference(ctx),
	}, nil
}

func confluenceCommentMutationIdentity(activation compatibility.Activation) (string, error) {
	canonical, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		ProviderID    string `json:"provider_id"`
		Version       string `json:"version"`
		BuildNumber   string `json:"build_number"`
	}{1, activation.ProviderID, string(activation.Version), string(activation.BuildNumber)})
	if err != nil {
		return "", fmt.Errorf("%w: encode Confluence compatibility identity", domain.ErrCheckFailed)
	}
	return string(canonical), nil
}

func confluenceCommentMutationBaselineHash(pageID string, pageVersion int, comments []ConfluenceCommentResultRecord) (string, error) {
	canonical, err := json.Marshal(struct {
		SchemaVersion int                             `json:"schema_version"`
		PageID        string                          `json:"page_id"`
		PageVersion   int                             `json:"page_version"`
		Comments      []ConfluenceCommentResultRecord `json:"comments"`
	}{1, pageID, pageVersion, comments})
	if err != nil {
		return "", fmt.Errorf("%w: encode Confluence comment mutation baseline", domain.ErrCheckFailed)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func confluenceCommentMutationProposalHash(snapshot confluenceCommentMutationSnapshot, operation domain.ConfluenceCommentMutationOperation, bodySHA256 string, bodyBytes int) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion      int                                       `json:"schema_version"`
		Backend            string                                    `json:"backend"`
		ConfiguredIdentity string                                    `json:"configured_identity"`
		Operation          domain.ConfluenceCommentMutationOperation `json:"operation"`
		PageID             string                                    `json:"page_id"`
		PageVersion        int                                       `json:"page_version"`
		Thread             ConfluenceCommentResultRecord             `json:"thread"`
		ActorID            string                                    `json:"actor_id"`
		BodySHA256         string                                    `json:"body_sha256,omitempty"`
		BodyBytes          int                                       `json:"body_bytes,omitempty"`
		Capabilities       domain.ConfluenceCommentCapabilities      `json:"capabilities"`
		BaselineSHA256     string                                    `json:"baseline_sha256"`
	}{
		confluenceCommentMutationProposalSchemaVersion, snapshot.backend, snapshot.configuredIdentity,
		operation, snapshot.pageID, snapshot.pageVersion, snapshot.target, snapshot.actor.ID,
		bodySHA256, bodyBytes, snapshot.capabilities, snapshot.baselineSHA256,
	})
	return guardedProposalDigest(canonical)
}

func confluenceCommentMutationTargetState(operation domain.ConfluenceCommentMutationOperation) domain.ConfluenceCommentResolution {
	switch operation {
	case domain.ConfluenceCommentMutationResolve:
		return domain.ConfluenceCommentResolutionResolved
	case domain.ConfluenceCommentMutationReopen:
		return domain.ConfluenceCommentResolutionOpen
	default:
		return ""
	}
}

func sameConfluenceCommentMutationContext(before, after confluenceCommentMutationSnapshot) bool {
	return before.pageID == after.pageID && before.pageVersion == after.pageVersion &&
		before.backend == after.backend && before.actor == after.actor &&
		before.provider == after.provider && before.configuredIdentity == after.configuredIdentity
}

func reconcileConfluenceCommentReply(result *ConfluenceCommentMutationGuardedResult, before, after confluenceCommentMutationSnapshot, body []byte, providerResult domain.ConfluenceCommentMutationResult, writeErr error) (*ConfluenceCommentMutationGuardedResult, error) {
	if !confluenceCommentMutationBaselineMembersUnchanged(before.comments, after.comments, "") {
		result.Status = "outcome_unknown"
		return result, confluenceCommentMutationAmbiguousError(
			"Confluence reply outcome is unknown because the reviewed baseline changed; do not replay automatically",
			sanitizeConfluenceWriteCause(writeErr),
		)
	}
	beforeIDs := make(map[string]struct{}, len(before.comments))
	for _, comment := range before.comments {
		beforeIDs[comment.ID] = struct{}{}
	}
	candidates := make([]ConfluenceCommentResultRecord, 0, 1)
	for _, comment := range after.comments {
		if _, existed := beforeIDs[comment.ID]; existed || !confluenceCommentReplyMatches(comment, before.target.ID, body, before.actor) {
			continue
		}
		candidates = append(candidates, comment)
	}
	providerQualified := writeErr == nil && providerResult.Operation == domain.ConfluenceCommentMutationReply &&
		providerResult.ThreadID == before.target.ID && providerResult.CommentID != ""
	if providerQualified {
		for _, candidate := range candidates {
			if candidate.ID == providerResult.CommentID {
				result.Status = "applied"
				result.CommentID = candidate.ID
				return result, nil
			}
		}
		result.Status = "outcome_unknown"
		return result, confluenceCommentMutationAmbiguousError(
			"Confluence reply outcome is unknown because the provider identity is absent from complete readback; do not replay automatically", nil,
		)
	}
	if len(candidates) == 1 {
		result.Status = "recovered"
		result.CommentID = candidates[0].ID
		return result, nil
	}
	result.Status = "outcome_unknown"
	return result, confluenceCommentMutationAmbiguousError(
		"Confluence reply outcome is unknown because complete readback did not find one exact new candidate; do not replay automatically",
		sanitizeConfluenceWriteCause(writeErr),
	)
}

func reconcileConfluenceCommentResolution(result *ConfluenceCommentMutationGuardedResult, before, after confluenceCommentMutationSnapshot, providerResult domain.ConfluenceCommentMutationResult, writeErr error) (*ConfluenceCommentMutationGuardedResult, error) {
	if !confluenceCommentMutationBaselineMembersUnchanged(before.comments, after.comments, before.target.ID) {
		result.Status = "outcome_unknown"
		return result, confluenceCommentMutationAmbiguousError(
			"Confluence resolution outcome is unknown because the reviewed baseline changed; do not replay automatically",
			sanitizeConfluenceWriteCause(writeErr),
		)
	}
	want := confluenceCommentMutationTargetState(result.Operation)
	if after.target.Resolution != want || !sameConfluenceCommentMutationTargetExceptState(before.target, after.target) {
		result.Status = "outcome_unknown"
		return result, confluenceCommentMutationAmbiguousError(
			"Confluence resolution outcome is unknown because the exact target state was not reconciled; do not replay automatically",
			sanitizeConfluenceWriteCause(writeErr),
		)
	}
	providerQualified := writeErr == nil && providerResult.Operation == result.Operation &&
		providerResult.ThreadID == before.target.ID && providerResult.CommentID == before.target.ID &&
		providerResult.Resolved == (want == domain.ConfluenceCommentResolutionResolved)
	if writeErr == nil && !providerQualified {
		result.Status = "outcome_unknown"
		return result, confluenceCommentMutationAmbiguousError(
			"Confluence resolution outcome is unknown because provider evidence conflicts with readback; do not replay automatically", nil,
		)
	}
	result.CommentID = before.target.ID
	if providerQualified {
		result.Status = "applied"
	} else {
		result.Status = "recovered"
	}
	return result, nil
}

func confluenceCommentMutationBaselineMembersUnchanged(before, after []ConfluenceCommentResultRecord, mutableID string) bool {
	afterByID := make(map[string]ConfluenceCommentResultRecord, len(after))
	for _, comment := range after {
		afterByID[comment.ID] = comment
	}
	for _, prior := range before {
		current, ok := afterByID[prior.ID]
		if !ok {
			return false
		}
		if prior.ID == mutableID {
			if !sameConfluenceCommentMutationTargetExceptState(prior, current) {
				return false
			}
			continue
		}
		if !confluenceFooterCommentRecordsEqual(prior, current) {
			return false
		}
	}
	return true
}

func sameConfluenceCommentMutationTargetExceptState(before, after ConfluenceCommentResultRecord) bool {
	before.Resolution, after.Resolution = "", ""
	before.Version, after.Version = 0, 0
	before.UpdatedAt, after.UpdatedAt = "", ""
	return confluenceFooterCommentRecordsEqual(before, after)
}

func confluenceCommentReplyMatches(comment ConfluenceCommentResultRecord, threadID string, body []byte, actor ConfluenceCommentMutationActor) bool {
	return comment.Relation == domain.ConfluenceCommentRelationReply && comment.Location == domain.ConfluenceCommentLocationInline &&
		comment.Resolution == domain.ConfluenceCommentResolutionOpen && comment.ParentID != nil && *comment.ParentID == threadID &&
		comment.RootID != nil && *comment.RootID == threadID && comment.Author.ID == actor.ID && comment.BodyStorage == string(body)
}

func confluenceCommentMutationAmbiguousError(message string, cause error) error {
	return &confluenceCommentMutationWriteError{message: message, cause: cause, closed: true, ambiguous: true}
}
