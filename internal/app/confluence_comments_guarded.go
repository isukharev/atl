package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// ConfluenceFooterCommentBodyMaxBytes bounds the native CSF fragment accepted
// by the reviewed footer-comment workflow. Valid bytes are preserved exactly.
const ConfluenceFooterCommentBodyMaxBytes = 1 << 20

const confluenceFooterCommentProposalSchemaVersion = 1

type ConfluenceFooterCommentAddOpts struct {
	Body                 []byte
	Apply                bool
	ExpectedProposalHash string
}

type ConfluenceFooterCommentActor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type ConfluenceFooterCommentCapability struct {
	Provider  string                            `json:"provider"`
	Operation string                            `json:"operation"`
	Write     domain.ConfluenceCapabilityStatus `json:"write"`
	Readback  domain.ConfluenceCapabilityStatus `json:"readback"`
	Depth     string                            `json:"depth"`
}

type ConfluenceFooterCommentAddResult struct {
	SchemaVersion  int                               `json:"schema_version"`
	PageID         string                            `json:"page_id"`
	Mode           string                            `json:"mode"`
	Status         string                            `json:"status"`
	CommentType    string                            `json:"comment_type"`
	PageVersion    int                               `json:"page_version"`
	BodySHA256     string                            `json:"body_sha256"`
	BodyBytes      int                               `json:"body_bytes"`
	Actor          ConfluenceFooterCommentActor      `json:"actor"`
	Capability     ConfluenceFooterCommentCapability `json:"capability"`
	CurrentCount   int                               `json:"current_count"`
	BaselineSHA256 string                            `json:"baseline_sha256"`
	BackendSHA256  string                            `json:"backend_sha256"`
	ProposalHash   string                            `json:"proposal_hash"`
	Created        *ConfluenceCommentResultRecord    `json:"created,omitempty"`
	Complete       bool                              `json:"complete"`
	Reconciled     bool                              `json:"reconciled,omitempty"`
	Warning        string                            `json:"warning"`
}

type confluenceFooterCommentSnapshot struct {
	pageID             string
	pageVersion        int
	actor              ConfluenceFooterCommentActor
	capability         ConfluenceFooterCommentCapability
	comments           []ConfluenceCommentResultRecord
	baselineSHA256     string
	backend            string
	untrustedReference bool
}

type confluenceFooterCommentWriteError struct {
	message   string
	cause     error
	closed    bool
	ambiguous bool
}

func (e *confluenceFooterCommentWriteError) Error() string { return e.message }

func (e *confluenceFooterCommentWriteError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return operationErrorCauses(e.cause, e.closed)
}

func (e *confluenceFooterCommentWriteError) DiagnosticAmbiguousWrite() bool {
	return e != nil && e.ambiguous
}

// AddFooterCommentGuarded previews or performs one reviewed public-REST footer
// comment append. Apply recomputes the complete proposal and then revalidates it
// immediately before a single-attempt POST. Every possibly committed outcome
// is reconciled from a complete bounded read and is never replayed.
func (s *ConfluenceService) AddFooterCommentGuarded(ctx context.Context, reference string, opts ConfluenceFooterCommentAddOpts) (*ConfluenceFooterCommentAddResult, error) {
	body, err := ValidateConfluenceFooterCommentBody(opts.Body)
	if err != nil {
		return nil, err
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --apply; run the preview first", domain.ErrUsage)
	}

	snapshot, err := s.confluenceFooterCommentSnapshot(ctx, reference)
	if err != nil {
		return nil, err
	}
	if snapshot.untrustedReference {
		ctx = domain.WithUntrustedConfluenceReference(ctx)
	}
	bodySum := sha256.Sum256(body)
	bodySHA256 := hex.EncodeToString(bodySum[:])
	proposalHash := confluenceFooterCommentProposalHash(snapshot, body, bodySHA256)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	backendSum := sha256.Sum256([]byte(snapshot.backend))
	result := &ConfluenceFooterCommentAddResult{
		SchemaVersion: confluenceFooterCommentProposalSchemaVersion,
		PageID:        snapshot.pageID, Mode: mode, Status: "would_apply", CommentType: "footer",
		PageVersion: snapshot.pageVersion, BodySHA256: bodySHA256, BodyBytes: len(body),
		Actor: snapshot.actor, Capability: snapshot.capability, CurrentCount: len(snapshot.comments),
		BaselineSHA256: snapshot.baselineSHA256, BackendSHA256: hex.EncodeToString(backendSum[:]),
		ProposalHash: proposalHash, Complete: true,
		Warning: "non_idempotent_write_requires_single_attempt_and_reconciliation",
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: footer comment proposal changed since review: expected hash %q, got %q", domain.ErrCheckFailed, strings.TrimSpace(opts.ExpectedProposalHash), proposalHash)
	}
	if !opts.Apply {
		return result, nil
	}

	prewrite, err := s.confluenceFooterCommentSnapshot(ctx, snapshot.pageID)
	if err != nil {
		result.Status = "conflict"
		result.Complete = false
		return result, &confluenceFooterCommentWriteError{
			message: "footer comment proposal could not be revalidated immediately before the write",
			cause:   sanitizeConfluenceWriteCause(err), closed: true,
		}
	}
	if confluenceFooterCommentProposalHash(prewrite, body, bodySHA256) != proposalHash {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: footer comment target, actor, page version, capabilities, or baseline changed since review; rerun the preview", domain.ErrCheckFailed)
	}
	if !confluenceFooterBaselineMembersUnchanged(snapshot.comments, prewrite.comments) {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: footer comment baseline members changed since review; rerun the preview", domain.ErrCheckFailed)
	}

	created, writeErr := s.store.AddComment(domain.WithSingleAttempt(ctx), prewrite.pageID, body)
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		result.Status = "not_applied"
		return result, &confluenceFooterCommentWriteError{
			message: definitiveWriteMessage("Confluence rejected the footer comment; it was not applied", writeErr),
			cause:   sanitizeConfluenceWriteCause(writeErr),
		}
	}

	readback, readbackErr := s.confluenceFooterCommentInventorySnapshot(ctx, prewrite.pageID)
	if readbackErr != nil {
		result.Status = "outcome_unknown"
		result.Complete = false
		return result, confluenceFooterCommentAmbiguousError(
			"footer comment outcome is unknown; complete readback failed; do not replay automatically",
			errors.Join(sanitizeConfluenceWriteCause(writeErr), sanitizeConfluenceWriteCause(readbackErr)),
		)
	}
	result.Reconciled = true
	if readback.pageVersion != prewrite.pageVersion {
		result.Status = "outcome_unknown"
		return result, confluenceFooterCommentAmbiguousError(
			"footer comment outcome is unknown because the page version changed during reconciliation; do not replay automatically",
			sanitizeConfluenceWriteCause(writeErr),
		)
	}
	if !confluenceFooterBaselineMembersUnchanged(prewrite.comments, readback.comments) {
		result.Status = "outcome_unknown"
		return result, confluenceFooterCommentAmbiguousError(
			"footer comment outcome is unknown because the reviewed comment baseline changed during reconciliation; do not replay automatically",
			sanitizeConfluenceWriteCause(writeErr),
		)
	}

	baselineIDs := make(map[string]struct{}, len(prewrite.comments))
	for _, comment := range prewrite.comments {
		baselineIDs[comment.ID] = struct{}{}
	}
	newComments := make([]ConfluenceCommentResultRecord, 0, len(readback.comments)-len(prewrite.comments))
	for _, comment := range readback.comments {
		if _, existed := baselineIDs[comment.ID]; !existed {
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
			if confluenceFooterCommentMatches(newComments[i], body, prewrite.actor) {
				result.Status = "applied"
				result.Created = &newComments[i]
				return result, nil
			}
			result.Status = "outcome_unknown"
			return result, confluenceFooterCommentAmbiguousError(
				"footer comment outcome is unknown because the returned comment does not match the reviewed proposal; do not replay automatically",
				sanitizeConfluenceWriteCause(writeErr),
			)
		}
		result.Status = "outcome_unknown"
		return result, confluenceFooterCommentAmbiguousError(
			"footer comment outcome is unknown because the returned comment identity is absent from complete readback; do not replay automatically",
			sanitizeConfluenceWriteCause(writeErr),
		)
	}

	matches := make([]ConfluenceCommentResultRecord, 0, 1)
	for _, comment := range newComments {
		if confluenceFooterCommentMatches(comment, body, prewrite.actor) {
			matches = append(matches, comment)
		}
	}
	if len(matches) == 1 {
		result.Status = "recovered"
		result.Created = &matches[0]
		return result, nil
	}
	result.Status = "outcome_unknown"
	return result, confluenceFooterCommentAmbiguousError(
		fmt.Sprintf("footer comment outcome is unknown because complete readback found %d exact new candidates; do not replay automatically", len(matches)),
		sanitizeConfluenceWriteCause(writeErr),
	)
}

func ValidateConfluenceFooterCommentBody(raw []byte) ([]byte, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("%w: footer comment body must not be empty", domain.ErrUsage)
	}
	if len(raw) > ConfluenceFooterCommentBodyMaxBytes {
		return nil, fmt.Errorf("%w: footer comment body exceeds the %d MiB limit", domain.ErrUsage, ConfluenceFooterCommentBodyMaxBytes>>20)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("%w: footer comment body is not valid UTF-8", domain.ErrUsage)
	}
	if csf.HasErrors(csf.Validate(raw)) {
		return nil, fmt.Errorf("%w: footer comment body is invalid Confluence Storage Format", domain.ErrCheckFailed)
	}
	return append([]byte(nil), raw...), nil
}

func (s *ConfluenceService) confluenceFooterCommentSnapshot(ctx context.Context, reference string) (confluenceFooterCommentSnapshot, error) {
	identityReader, ok := s.store.(domain.ConfluenceCurrentUserReader)
	if !ok {
		return confluenceFooterCommentSnapshot{}, fmt.Errorf("%w: Confluence backend does not expose a stable current-user identity", domain.ErrCheckFailed)
	}
	identity, err := identityReader.CurrentConfluenceUser(ctx)
	if err != nil {
		return confluenceFooterCommentSnapshot{}, err
	}
	actor := ConfluenceFooterCommentActor{ID: strings.TrimSpace(identity.ID), DisplayName: strings.TrimSpace(identity.DisplayName)}
	if actor.ID == "" || actor.DisplayName == "" {
		return confluenceFooterCommentSnapshot{}, fmt.Errorf("%w: Confluence current-user identity is incomplete", domain.ErrCheckFailed)
	}
	snapshot, err := s.confluenceFooterCommentInventorySnapshot(ctx, reference)
	if err != nil {
		return confluenceFooterCommentSnapshot{}, err
	}
	snapshot.actor = actor
	return snapshot, nil
}

func (s *ConfluenceService) confluenceFooterCommentInventorySnapshot(ctx context.Context, reference string) (confluenceFooterCommentSnapshot, error) {
	backend := strings.TrimRight(strings.TrimSpace(s.baseURL), "/")
	if backend == "" {
		return confluenceFooterCommentSnapshot{}, fmt.Errorf("%w: Confluence backend identity is unavailable", domain.ErrCheckFailed)
	}
	resolved, err := s.ResolvePageReference(ctx, reference)
	if err != nil {
		return confluenceFooterCommentSnapshot{}, err
	}
	ctx = resolved.Context(ctx)
	meta, err := s.store.GetMeta(ctx, resolved.ID)
	if err != nil {
		return confluenceFooterCommentSnapshot{}, err
	}
	if meta == nil || meta.ID != resolved.ID || meta.Type != "page" || meta.Version <= 0 {
		return confluenceFooterCommentSnapshot{}, fmt.Errorf("%w: Confluence footer comment target metadata is not reconciled", domain.ErrCheckFailed)
	}
	reader, ok := s.store.(domain.QualifiedConfluenceCommentReader)
	if !ok {
		return confluenceFooterCommentSnapshot{}, fmt.Errorf("%w: Confluence backend does not expose qualified footer comments", domain.ErrCheckFailed)
	}
	inventory, err := reader.ListConfluenceComments(ctx, resolved.ID, domain.ConfluenceCommentReadOptions{
		ParentVersion: meta.Version, DepthAll: false, Locations: []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter},
	})
	if err != nil {
		return confluenceFooterCommentSnapshot{}, err
	}
	if err := domain.ValidateConfluenceCommentInventory(inventory); err != nil {
		return confluenceFooterCommentSnapshot{}, err
	}
	if !inventory.CommentsComplete || !inventory.ThreadsComplete {
		return confluenceFooterCommentSnapshot{}, fmt.Errorf("%w: complete footer comment inventory is required for guarded reconciliation", domain.ErrCheckFailed)
	}
	capability := ConfluenceFooterCommentCapability{
		Provider: "public_rest", Operation: "footer_root_create",
		Write: domain.ConfluenceCapabilityDocumented, Readback: inventory.Capabilities.Footer, Depth: "root",
	}
	if capability.Readback != domain.ConfluenceCapabilityDocumented && capability.Readback != domain.ConfluenceCapabilityObserved {
		return confluenceFooterCommentSnapshot{}, fmt.Errorf("%w: Confluence footer comment reconciliation capability is unavailable", domain.ErrCheckFailed)
	}
	comments := make([]ConfluenceCommentResultRecord, 0, len(inventory.Comments))
	for _, comment := range inventory.Comments {
		if comment.PageID != resolved.ID || comment.Relation != domain.ConfluenceCommentRelationRoot ||
			comment.Location != domain.ConfluenceCommentLocationFooter || comment.ParentID != nil ||
			comment.RootID == nil || *comment.RootID != comment.ID {
			return confluenceFooterCommentSnapshot{}, fmt.Errorf("%w: Confluence footer root inventory is not reconciled", domain.ErrCheckFailed)
		}
		comments = append(comments, ConfluenceCommentResultRecord{
			ID: comment.ID, PageID: comment.PageID, ParentID: comment.ParentID, RootID: comment.RootID,
			Relation: comment.Relation, Location: comment.Location, Resolution: comment.Resolution,
			Version:   comment.Version,
			Author:    ConfluenceCommentAuthor{ID: comment.AuthorID, DisplayName: comment.AuthorDisplayName},
			CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt,
			Body: comment.Body, BodyStorage: comment.BodyStorage,
		})
	}
	comments = sortConfluenceCommentResults(comments)
	hash, err := confluenceFooterCommentBaselineHash(resolved.ID, comments)
	if err != nil {
		return confluenceFooterCommentSnapshot{}, err
	}
	return confluenceFooterCommentSnapshot{
		pageID: resolved.ID, pageVersion: meta.Version, capability: capability,
		comments:       comments,
		baselineSHA256: hash, backend: backend, untrustedReference: resolved.Untrusted(),
	}, nil
}

func confluenceFooterCommentBaselineHash(pageID string, comments []ConfluenceCommentResultRecord) (string, error) {
	seen := make(map[string]struct{}, len(comments))
	for _, comment := range comments {
		if strings.TrimSpace(comment.ID) == "" || comment.ID != strings.TrimSpace(comment.ID) || comment.PageID != pageID {
			return "", fmt.Errorf("%w: Confluence returned an invalid footer comment identity", domain.ErrCheckFailed)
		}
		if _, exists := seen[comment.ID]; exists {
			return "", fmt.Errorf("%w: Confluence returned duplicate footer comment identities", domain.ErrCheckFailed)
		}
		seen[comment.ID] = struct{}{}
	}
	ids := make([]string, len(comments))
	for i := range comments {
		ids[i] = comments[i].ID
	}
	canonical, err := json.Marshal(struct {
		SchemaVersion int      `json:"schema_version"`
		PageID        string   `json:"page_id"`
		IDs           []string `json:"ids"`
	}{1, pageID, ids})
	if err != nil {
		return "", fmt.Errorf("%w: encode Confluence footer comment baseline", domain.ErrCheckFailed)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func confluenceFooterCommentProposalHash(snapshot confluenceFooterCommentSnapshot, body []byte, bodySHA256 string) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion  int                               `json:"schema_version"`
		Backend        string                            `json:"backend"`
		PageID         string                            `json:"page_id"`
		PageVersion    int                               `json:"page_version"`
		CommentType    string                            `json:"comment_type"`
		Body           string                            `json:"body"`
		BodySHA256     string                            `json:"body_sha256"`
		BodyBytes      int                               `json:"body_bytes"`
		ActorID        string                            `json:"actor_id"`
		Capability     ConfluenceFooterCommentCapability `json:"capability"`
		BaselineSHA256 string                            `json:"baseline_sha256"`
		CurrentCount   int                               `json:"current_count"`
	}{
		confluenceFooterCommentProposalSchemaVersion, snapshot.backend, snapshot.pageID,
		snapshot.pageVersion, "footer", string(body), bodySHA256, len(body), snapshot.actor.ID,
		snapshot.capability, snapshot.baselineSHA256, len(snapshot.comments),
	})
	return guardedProposalDigest(canonical)
}

func confluenceFooterBaselineMembersUnchanged(before, after []ConfluenceCommentResultRecord) bool {
	afterByID := make(map[string]ConfluenceCommentResultRecord, len(after))
	for _, comment := range after {
		afterByID[comment.ID] = comment
	}
	for _, prior := range before {
		current, ok := afterByID[prior.ID]
		if !ok || !confluenceFooterCommentRecordsEqual(prior, current) {
			return false
		}
	}
	return true
}

func confluenceFooterCommentRecordsEqual(a, b ConfluenceCommentResultRecord) bool {
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return string(aJSON) == string(bJSON)
}

func confluenceFooterCommentMatches(comment ConfluenceCommentResultRecord, body []byte, actor ConfluenceFooterCommentActor) bool {
	return comment.Relation == domain.ConfluenceCommentRelationRoot &&
		comment.Location == domain.ConfluenceCommentLocationFooter &&
		comment.ParentID == nil && comment.RootID != nil && *comment.RootID == comment.ID &&
		comment.Author.ID == actor.ID &&
		comment.BodyStorage == string(body)
}

func confluenceFooterCommentAmbiguousError(message string, cause error) error {
	return &confluenceFooterCommentWriteError{message: message, cause: cause, closed: true, ambiguous: true}
}

func sanitizeConfluenceWriteCause(err error) error {
	return sanitizeRemoteWriteCause(err)
}

func ConfluenceFooterCommentAddText(result *ConfluenceFooterCommentAddResult) string {
	if result == nil {
		return ""
	}
	return fmt.Sprintf("status: %s\npage_id: %s\nproposal_hash: %s\nbody_sha256: %s\nbody_bytes: %d",
		result.Status, result.PageID, result.ProposalHash, result.BodySHA256, result.BodyBytes)
}
