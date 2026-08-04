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
	"unicode/utf16"

	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

type confluenceInlineMarkerEvidence struct {
	Ref       string `json:"ref"`
	Selection string `json:"selection"`
}

type confluenceInlineCreateSnapshot struct {
	pageID               string
	pageVersion          int
	pageBody             []byte
	pageBodySHA256       string
	actor                ConfluenceCommentMutationActor
	provider             ConfluenceCommentMutationProviderEvidence
	activation           compatibility.Activation
	comments             []ConfluenceCommentResultRecord
	capabilities         domain.ConfluenceCommentCapabilities
	baselineSHA256       string
	markers              []confluenceInlineMarkerEvidence
	markerSHA256         string
	backend              string
	configuredIdentity   string
	preparation          domain.ConfluenceInlineCommentPreparation
	geometrySHA256       string
	highlightedSelection string
}

func (s *ConfluenceService) createInlineCommentGuarded(ctx context.Context, reference string, opts ConfluenceCommentMutationOpts, body []byte) (*ConfluenceCommentMutationGuardedResult, error) {
	selection := string(opts.Selection)
	snapshot, err := s.confluenceInlineCreateSnapshot(ctx, reference, selection, opts.Occurrence)
	if err != nil {
		return nil, err
	}
	bodySum := sha256.Sum256(body)
	bodySHA256 := hex.EncodeToString(bodySum[:])
	selectionSum := sha256.Sum256(opts.Selection)
	selectionSHA256 := hex.EncodeToString(selectionSum[:])
	proposalHash := confluenceInlineCreateProposalHash(snapshot, bodySHA256, len(body), selectionSHA256, len(opts.Selection), opts.Occurrence)
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	backendSum := sha256.Sum256([]byte(snapshot.backend))
	occurrence := opts.Occurrence
	matchIndex := snapshot.preparation.MatchIndex
	markerCount := len(snapshot.markers)
	result := &ConfluenceCommentMutationGuardedResult{
		SchemaVersion: confluenceCommentMutationProposalSchemaVersion,
		PageID:        snapshot.pageID, Operation: domain.ConfluenceCommentMutationInlineCreate,
		Mode: mode, Status: "would_apply", PageVersion: snapshot.pageVersion,
		BodySHA256: bodySHA256, BodyBytes: len(body), SelectionSHA256: selectionSHA256,
		SelectionBytes: len(opts.Selection), Occurrence: &occurrence, NumMatches: snapshot.preparation.NumMatches,
		MatchIndex:     &matchIndex,
		HighlightCount: len(snapshot.preparation.SerializedHighlights), GeometrySHA256: snapshot.geometrySHA256,
		PageBodySHA256: snapshot.pageBodySHA256, MarkerCount: &markerCount, MarkerSHA256: snapshot.markerSHA256,
		Actor: snapshot.actor, Provider: snapshot.provider, CurrentCount: len(snapshot.comments),
		BaselineSHA256: snapshot.baselineSHA256, BackendSHA256: hex.EncodeToString(backendSum[:]),
		ProposalHash: proposalHash, Complete: true,
		Warning: "non_idempotent_write_requires_single_attempt_and_reconciliation",
	}
	if opts.Apply && strings.TrimSpace(opts.ExpectedProposalHash) != proposalHash {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: Confluence inline comment proposal changed since review", domain.ErrCheckFailed)
	}
	if !opts.Apply {
		return result, nil
	}

	prewrite, err := s.confluenceInlineCreateSnapshot(ctx, snapshot.pageID, selection, opts.Occurrence)
	if err != nil {
		result.Status = "conflict"
		result.Complete = false
		return result, &confluenceCommentMutationWriteError{
			message: "Confluence inline comment could not be revalidated immediately before the write",
			cause:   sanitizeConfluenceWriteCause(err), closed: true,
		}
	}
	if confluenceInlineCreateProposalHash(prewrite, bodySHA256, len(body), selectionSHA256, len(opts.Selection), opts.Occurrence) != proposalHash {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: Confluence inline comment target, selection geometry, actor, provider, page, marker inventory, or comment baseline changed since review", domain.ErrCheckFailed)
	}

	request := domain.ConfluenceCommentMutationRequest{
		Operation: domain.ConfluenceCommentMutationInlineCreate,
		PageID:    prewrite.pageID, PageVersion: prewrite.pageVersion, BodyStorage: body,
		SearchSelection: prewrite.preparation.SearchSelection, OriginalSelection: prewrite.preparation.OriginalSelection,
		NumMatches: prewrite.preparation.NumMatches,
		MatchIndex: prewrite.preparation.MatchIndex, LastFetchTime: prewrite.preparation.LastFetchTime,
		SerializedHighlights: cloneConfluenceInlineHighlights(prewrite.preparation.SerializedHighlights),
	}
	if err := domain.ValidateConfluenceCommentMutationRequest(request); err != nil {
		result.Status = "conflict"
		return result, fmt.Errorf("%w: Confluence inline comment preparation is not write-qualified", domain.ErrCheckFailed)
	}
	providerResult, writeErr := s.commentMutator.MutateConfluenceComment(domain.WithSingleAttempt(ctx), request)
	if writeErr != nil && (definitiveWriteRejection(writeErr) || confluenceCommentWriteDefinitelyNotAttempted(writeErr)) {
		result.Status = "not_applied"
		return result, &confluenceCommentMutationWriteError{
			message: "Confluence rejected the inline comment; it was not applied",
			cause:   sanitizeConfluenceWriteCause(writeErr),
		}
	}

	readback, readbackErr := s.confluenceInlineCreateBaseSnapshot(ctx, prewrite.pageID)
	if readbackErr != nil {
		result.Status = "outcome_unknown"
		result.Complete = false
		return result, confluenceCommentMutationAmbiguousError(
			"Confluence inline comment outcome is unknown; complete readback failed; do not replay automatically",
			errors.Join(sanitizeConfluenceWriteCause(writeErr), sanitizeConfluenceWriteCause(readbackErr)),
		)
	}
	result.Reconciled = true
	return reconcileConfluenceInlineCreate(result, prewrite, readback, body, providerResult, writeErr)
}

func (s *ConfluenceService) confluenceInlineCreateSnapshot(ctx context.Context, reference, selection string, occurrence int) (confluenceInlineCreateSnapshot, error) {
	snapshot, err := s.confluenceInlineCreateBaseSnapshot(ctx, reference)
	if err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	if s.commentPreparer == nil {
		return confluenceInlineCreateSnapshot{}, fmt.Errorf("%w: Confluence inline comment preparation provider is unavailable", domain.ErrConfig)
	}
	prepared, err := s.commentPreparer.PrepareConfluenceInlineComment(domain.WithSingleAttempt(ctx), domain.ConfluenceInlineCommentPreparationRequest{
		PageID: snapshot.pageID, ExpectedPageVersion: snapshot.pageVersion,
		OriginalSelection: selection, MatchIndex: occurrence,
	})
	if err != nil {
		return confluenceInlineCreateSnapshot{}, &confluenceCommentMutationWriteError{
			message: "Confluence inline selection could not be prepared from the current page view",
			cause:   sanitizeConfluenceWriteCause(err), closed: true,
		}
	}
	prepared, geometryHash, highlightedSelection, err := validateConfluenceInlinePreparation(prepared, snapshot)
	if err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	snapshot.preparation = prepared
	snapshot.geometrySHA256 = geometryHash
	snapshot.highlightedSelection = highlightedSelection
	return snapshot, nil
}

func (s *ConfluenceService) confluenceInlineCreateBaseSnapshot(ctx context.Context, reference string) (confluenceInlineCreateSnapshot, error) {
	if s == nil || s.commentMutationActivation == nil || s.commentMutator == nil {
		return confluenceInlineCreateSnapshot{}, fmt.Errorf("%w: Confluence comment compatibility is not activated", domain.ErrConfig)
	}
	activation := *s.commentMutationActivation
	if err := activation.Validate(compatibility.ProductConfluence); err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	backend := strings.TrimRight(strings.TrimSpace(s.baseURL), "/")
	if backend == "" {
		return confluenceInlineCreateSnapshot{}, fmt.Errorf("%w: Confluence backend identity is unavailable", domain.ErrCheckFailed)
	}
	configuredIdentity, err := confluenceCommentMutationIdentity(activation)
	if err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	resolved, err := s.ResolvePageReference(ctx, reference)
	if err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	page, err := s.store.GetPage(ctx, resolved.ID, domain.PullOpts{Format: "csf"})
	if err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	if page == nil || page.ID != resolved.ID || page.Type != "page" || page.Version <= 0 || !page.BodyPresent {
		return confluenceInlineCreateSnapshot{}, fmt.Errorf("%w: Confluence inline comment page is not an exact native page snapshot", domain.ErrCheckFailed)
	}
	inventory, err := s.commentInventoryForPage(ctx, page, ConfluenceCommentInventoryOpts{Location: "all", State: "all", Depth: "all"})
	if err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	if inventory == nil || !inventory.Complete || !inventory.CommentsComplete || !inventory.ThreadsComplete || !inventory.AnchorsComplete ||
		inventory.PageID != page.ID || inventory.PageVersion != page.Version {
		return confluenceInlineCreateSnapshot{}, fmt.Errorf("%w: complete Confluence comment and marker evidence is required", domain.ErrCheckFailed)
	}
	comments := append([]ConfluenceCommentResultRecord(nil), inventory.Comments...)
	sort.Slice(comments, func(i, j int) bool { return comments[i].ID < comments[j].ID })
	baselineHash, err := confluenceCommentMutationBaselineHash(page.ID, page.Version, comments)
	if err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	markers, err := confluenceInlineMarkerInventory(page.Body)
	if err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	markerHash, err := confluenceInlineMarkerHash(markers)
	if err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	identityReader, ok := s.store.(domain.ConfluenceCurrentUserReader)
	if !ok {
		return confluenceInlineCreateSnapshot{}, fmt.Errorf("%w: Confluence backend does not expose a stable current-user identity", domain.ErrCheckFailed)
	}
	currentUser, err := identityReader.CurrentConfluenceUser(ctx)
	if err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	if err := domain.ValidateConfluenceUserIdentity(currentUser); err != nil {
		return confluenceInlineCreateSnapshot{}, err
	}
	pageBodySum := sha256.Sum256(page.Body)
	return confluenceInlineCreateSnapshot{
		pageID: page.ID, pageVersion: page.Version, pageBody: append([]byte(nil), page.Body...),
		pageBodySHA256: hex.EncodeToString(pageBodySum[:]),
		actor:          ConfluenceCommentMutationActor{ID: strings.TrimSpace(currentUser.ID), DisplayName: strings.TrimSpace(currentUser.DisplayName)},
		provider:       ConfluenceCommentMutationProviderEvidence{ID: activation.ProviderID}, activation: activation,
		comments: comments, capabilities: inventory.Capabilities, baselineSHA256: baselineHash,
		markers: markers, markerSHA256: markerHash, backend: backend, configuredIdentity: configuredIdentity,
	}, nil
}

func validateConfluenceInlinePreparation(prepared domain.ConfluenceInlineCommentPreparation, snapshot confluenceInlineCreateSnapshot) (domain.ConfluenceInlineCommentPreparation, string, string, error) {
	if prepared.PageID != snapshot.pageID || prepared.PageVersion != snapshot.pageVersion || prepared.LastFetchTime <= 0 ||
		prepared.SearchSelection == "" || prepared.OriginalSelection == "" || prepared.HighlightedSelection == "" || prepared.NumMatches <= 0 ||
		prepared.MatchIndex < 0 || prepared.MatchIndex >= prepared.NumMatches || !validSHA256Hex(prepared.ViewSHA256) {
		return domain.ConfluenceInlineCommentPreparation{}, "", "", fmt.Errorf("%w: Confluence inline preparation does not bind the exact page and occurrence", domain.ErrCheckFailed)
	}
	if len(prepared.SerializedHighlights) == 0 {
		return domain.ConfluenceInlineCommentPreparation{}, "", "", fmt.Errorf("%w: Confluence inline selection geometry is unavailable", domain.ErrCheckFailed)
	}
	var selected strings.Builder
	for index := range prepared.SerializedHighlights {
		highlight := &prepared.SerializedHighlights[index]
		if highlight.Text == "" || len(highlight.ChildIndexPath) == 0 || highlight.PreviousTextSiblingOffset < 0 ||
			highlight.Length != len(utf16.Encode([]rune(highlight.Text))) {
			return domain.ConfluenceInlineCommentPreparation{}, "", "", fmt.Errorf("%w: Confluence inline selection geometry is invalid", domain.ErrCheckFailed)
		}
		for _, childIndex := range highlight.ChildIndexPath {
			if childIndex < 0 {
				return domain.ConfluenceInlineCommentPreparation{}, "", "", fmt.Errorf("%w: Confluence inline selection geometry is invalid", domain.ErrCheckFailed)
			}
		}
		selected.WriteString(highlight.Text)
		highlight.ChildIndexPath = append([]int(nil), highlight.ChildIndexPath...)
	}
	highlightedSelection := selected.String()
	if highlightedSelection != prepared.HighlightedSelection {
		return domain.ConfluenceInlineCommentPreparation{}, "", "", fmt.Errorf("%w: Confluence inline selection geometry does not reconstruct the prepared highlight", domain.ErrCheckFailed)
	}
	markerRefs, err := canonicalConfluenceMarkerRefs(prepared.MarkerRefs)
	if err != nil {
		return domain.ConfluenceInlineCommentPreparation{}, "", "", err
	}
	currentRefs := make([]string, len(snapshot.markers))
	for index := range snapshot.markers {
		currentRefs[index] = snapshot.markers[index].Ref
	}
	if !equalConfluenceInlineStrings(markerRefs, currentRefs) {
		return domain.ConfluenceInlineCommentPreparation{}, "", "", fmt.Errorf("%w: rendered and native Confluence marker inventories differ", domain.ErrCheckFailed)
	}
	prepared.MarkerRefs = markerRefs
	geometryHash, err := confluenceInlineGeometryHash(prepared.SerializedHighlights)
	if err != nil {
		return domain.ConfluenceInlineCommentPreparation{}, "", "", err
	}
	return prepared, geometryHash, highlightedSelection, nil
}

func confluenceInlineMarkerInventory(body []byte) ([]confluenceInlineMarkerEvidence, error) {
	markers, err := csf.ExtractInlineCommentMarkers(body)
	if err != nil {
		return nil, fmt.Errorf("%w: Confluence native marker inventory is unavailable", domain.ErrCheckFailed)
	}
	out := make([]confluenceInlineMarkerEvidence, len(markers))
	for index, marker := range markers {
		if strings.TrimSpace(marker.Ref) == "" || marker.Ref != strings.TrimSpace(marker.Ref) {
			return nil, fmt.Errorf("%w: Confluence native marker inventory is invalid", domain.ErrCheckFailed)
		}
		out[index] = confluenceInlineMarkerEvidence{Ref: marker.Ref, Selection: marker.Selection}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ref != out[j].Ref {
			return out[i].Ref < out[j].Ref
		}
		return out[i].Selection < out[j].Selection
	})
	for index := 1; index < len(out); index++ {
		if out[index-1].Ref == out[index].Ref {
			return nil, fmt.Errorf("%w: Confluence native marker reference is ambiguous", domain.ErrCheckFailed)
		}
	}
	return out, nil
}

func canonicalConfluenceMarkerRefs(refs []string) ([]string, error) {
	out := append([]string(nil), refs...)
	for _, ref := range out {
		if strings.TrimSpace(ref) == "" || ref != strings.TrimSpace(ref) {
			return nil, fmt.Errorf("%w: rendered Confluence marker inventory is invalid", domain.ErrCheckFailed)
		}
	}
	sort.Strings(out)
	for index := 1; index < len(out); index++ {
		if out[index-1] == out[index] {
			return nil, fmt.Errorf("%w: rendered Confluence marker reference is ambiguous", domain.ErrCheckFailed)
		}
	}
	return out, nil
}

func confluenceInlineMarkerHash(markers []confluenceInlineMarkerEvidence) (string, error) {
	canonical, err := json.Marshal(struct {
		SchemaVersion int                              `json:"schema_version"`
		Markers       []confluenceInlineMarkerEvidence `json:"markers"`
	}{1, markers})
	if err != nil {
		return "", fmt.Errorf("%w: encode Confluence marker inventory", domain.ErrCheckFailed)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func confluenceInlineGeometryHash(highlights []domain.ConfluenceInlineHighlightGeometry) (string, error) {
	canonical, err := json.Marshal(struct {
		SchemaVersion int                                        `json:"schema_version"`
		Highlights    []domain.ConfluenceInlineHighlightGeometry `json:"highlights"`
	}{1, highlights})
	if err != nil {
		return "", fmt.Errorf("%w: encode Confluence inline geometry", domain.ErrCheckFailed)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func confluenceInlineCreateProposalHash(snapshot confluenceInlineCreateSnapshot, bodySHA256 string, bodyBytes int, selectionSHA256 string, selectionBytes, occurrence int) string {
	canonical, _ := json.Marshal(struct {
		SchemaVersion        int                                        `json:"schema_version"`
		Backend              string                                     `json:"backend"`
		ConfiguredIdentity   string                                     `json:"configured_identity"`
		Operation            domain.ConfluenceCommentMutationOperation  `json:"operation"`
		PageID               string                                     `json:"page_id"`
		PageVersion          int                                        `json:"page_version"`
		PageBodySHA256       string                                     `json:"page_body_sha256"`
		ViewSHA256           string                                     `json:"view_sha256"`
		BodySHA256           string                                     `json:"body_sha256"`
		BodyBytes            int                                        `json:"body_bytes"`
		SelectionSHA256      string                                     `json:"selection_sha256"`
		SelectionBytes       int                                        `json:"selection_bytes"`
		Occurrence           int                                        `json:"occurrence"`
		NumMatches           int                                        `json:"num_matches"`
		MatchIndex           int                                        `json:"match_index"`
		SearchSelection      string                                     `json:"search_selection"`
		OriginalSelection    string                                     `json:"original_selection"`
		HighlightedSelection string                                     `json:"highlighted_selection"`
		Highlights           []domain.ConfluenceInlineHighlightGeometry `json:"highlights"`
		ActorID              string                                     `json:"actor_id"`
		ProviderID           string                                     `json:"provider_id"`
		Capabilities         domain.ConfluenceCommentCapabilities       `json:"capabilities"`
		BaselineSHA256       string                                     `json:"baseline_sha256"`
		Markers              []confluenceInlineMarkerEvidence           `json:"markers"`
	}{
		confluenceCommentMutationProposalSchemaVersion, snapshot.backend, snapshot.configuredIdentity,
		domain.ConfluenceCommentMutationInlineCreate, snapshot.pageID, snapshot.pageVersion,
		snapshot.pageBodySHA256, snapshot.preparation.ViewSHA256, bodySHA256, bodyBytes,
		selectionSHA256, selectionBytes, occurrence, snapshot.preparation.NumMatches, snapshot.preparation.MatchIndex,
		snapshot.preparation.SearchSelection, snapshot.preparation.OriginalSelection, snapshot.highlightedSelection,
		snapshot.preparation.SerializedHighlights, snapshot.actor.ID, snapshot.provider.ID,
		snapshot.capabilities, snapshot.baselineSHA256, snapshot.markers,
	})
	return guardedProposalDigest(canonical)
}

func reconcileConfluenceInlineCreate(result *ConfluenceCommentMutationGuardedResult, before, after confluenceInlineCreateSnapshot, body []byte, providerResult domain.ConfluenceCommentMutationResult, writeErr error) (*ConfluenceCommentMutationGuardedResult, error) {
	unknown := func(message string) (*ConfluenceCommentMutationGuardedResult, error) {
		result.Status = "outcome_unknown"
		return result, confluenceCommentMutationAmbiguousError(message, sanitizeConfluenceWriteCause(writeErr))
	}
	if before.pageID != after.pageID || before.backend != after.backend || before.configuredIdentity != after.configuredIdentity ||
		before.actor != after.actor || before.provider != after.provider || !qualifiedConfluenceInlineCreateVersionTransition(before.pageVersion, after.pageVersion) {
		return unknown("Confluence inline comment outcome is unknown because page, actor, provider, or qualified page-version evidence changed; do not replay automatically")
	}
	if len(after.comments) != len(before.comments)+1 || !confluenceCommentMutationBaselineMembersUnchanged(before.comments, after.comments, "") {
		return unknown("Confluence inline comment outcome is unknown because the complete comment baseline changed unexpectedly; do not replay automatically")
	}
	beforeIDs := make(map[string]struct{}, len(before.comments))
	for _, comment := range before.comments {
		beforeIDs[comment.ID] = struct{}{}
	}
	candidates := make([]ConfluenceCommentResultRecord, 0, 1)
	for _, comment := range after.comments {
		if _, existed := beforeIDs[comment.ID]; !existed && confluenceInlineCreatedRootMatches(comment, before.pageID, body,
			before.preparation.OriginalSelection, before.highlightedSelection, before.actor) {
			candidates = append(candidates, comment)
		}
	}
	if len(candidates) != 1 || candidates[0].Anchor == nil {
		return unknown("Confluence inline comment outcome is unknown because complete readback did not find one exact new root; do not replay automatically")
	}
	candidate := candidates[0]
	markerMatched, markerErr := csf.ReconcileInlineCommentMarkerInsertion(before.pageBody, after.pageBody, candidate.Anchor.MarkerRef, before.highlightedSelection)
	if markerErr != nil || !markerMatched || len(after.markers) != len(before.markers)+1 {
		return unknown("Confluence inline comment outcome is unknown because the server-owned marker was not exactly reconciled; do not replay automatically")
	}
	result.CommentID = candidate.ID
	result.ThreadID = candidate.ID
	result.MarkerRef = candidate.Anchor.MarkerRef
	result.ResultPageVersion = after.pageVersion
	providerQualified := writeErr == nil && providerResult.Operation == domain.ConfluenceCommentMutationInlineCreate &&
		providerResult.CommentID == candidate.ID && providerResult.ThreadID == candidate.ID &&
		providerResult.MarkerRef == candidate.Anchor.MarkerRef && providerResult.OriginalSelection == before.preparation.OriginalSelection &&
		providerResult.PageVersion == after.pageVersion
	if writeErr == nil && !providerQualified {
		return unknown("Confluence inline comment outcome is unknown because provider evidence conflicts with exact readback; do not replay automatically")
	}
	if providerQualified {
		result.Status = "applied"
	} else {
		result.Status = "recovered"
	}
	return result, nil
}

// qualifiedConfluenceInlineCreateVersionTransition accepts only the two exact
// semantics exposed by the pinned compatibility profile. Some Data Center
// builds persist the server-owned marker without advancing the page's public
// content version, while others expose that insertion as the next version. The
// surrounding reconciliation still proves the complete comment-baseline delta
// and the exact native marker-only body transformation.
func qualifiedConfluenceInlineCreateVersionTransition(before, after int) bool {
	return before > 0 && (after == before || after == before+1)
}

func confluenceInlineCreatedRootMatches(comment ConfluenceCommentResultRecord, pageID string, body []byte, originalSelection, observedSelection string, actor ConfluenceCommentMutationActor) bool {
	return comment.PageID == pageID && comment.Relation == domain.ConfluenceCommentRelationRoot && comment.ParentID == nil &&
		comment.RootID != nil && *comment.RootID == comment.ID && comment.Location == domain.ConfluenceCommentLocationInline &&
		comment.Resolution == domain.ConfluenceCommentResolutionOpen && comment.Version > 0 && comment.Author.ID == actor.ID &&
		comment.BodyStorage == string(body) && comment.Anchor != nil && comment.Anchor.MarkerRef != "" &&
		comment.Anchor.OriginalSelection == originalSelection && comment.Anchor.ObservedSelection == observedSelection &&
		comment.Anchor.Status == domain.ConfluenceAnchorMatched
}

func cloneConfluenceInlineHighlights(values []domain.ConfluenceInlineHighlightGeometry) []domain.ConfluenceInlineHighlightGeometry {
	out := make([]domain.ConfluenceInlineHighlightGeometry, len(values))
	for index := range values {
		out[index] = values[index]
		out[index].ChildIndexPath = append([]int(nil), values[index].ChildIndexPath...)
	}
	return out
}

func confluenceCommentWriteDefinitelyNotAttempted(err error) bool {
	var diagnostic interface{ DiagnosticWriteAttempted() bool }
	return errors.As(err, &diagnostic) && !diagnostic.DiagnosticWriteAttempted()
}

func equalConfluenceInlineStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
