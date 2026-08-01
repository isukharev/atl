package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

const confluenceCommentInventorySchemaVersion = 2

type ConfluenceCommentInventoryOpts struct {
	Location            string
	State               string
	Depth               string
	ExpectedPageVersion int
}

type ConfluenceCommentQuery struct {
	Mode      string `json:"mode"`
	Location  string `json:"location"`
	State     string `json:"state"`
	Depth     string `json:"depth"`
	CommentID string `json:"comment_id,omitempty"`
}

type ConfluenceCommentAuthor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type ConfluenceInlineAnchor struct {
	MarkerRef         string                        `json:"marker_ref"`
	OriginalSelection string                        `json:"original_selection"`
	ObservedSelection string                        `json:"observed_selection"`
	Status            domain.ConfluenceAnchorStatus `json:"status"`
}

type ConfluenceCommentResultRecord struct {
	ID          string                             `json:"id"`
	PageID      string                             `json:"page_id"`
	ParentID    *string                            `json:"parent_id"`
	RootID      *string                            `json:"root_id"`
	Relation    domain.ConfluenceCommentRelation   `json:"relation"`
	Location    domain.ConfluenceCommentLocation   `json:"location"`
	Resolution  domain.ConfluenceCommentResolution `json:"resolution"`
	Version     int                                `json:"version"`
	Author      ConfluenceCommentAuthor            `json:"author"`
	CreatedAt   string                             `json:"created_at"`
	UpdatedAt   string                             `json:"updated_at"`
	Body        string                             `json:"body"`
	BodyStorage string                             `json:"body_storage"`
	Anchor      *ConfluenceInlineAnchor            `json:"anchor"`
}

type ConfluenceCommentResultDiagnostic struct {
	Code      string                           `json:"code"`
	CommentID string                           `json:"comment_id,omitempty"`
	MarkerRef string                           `json:"marker_ref,omitempty"`
	Selector  domain.ConfluenceCommentSelector `json:"selector,omitempty"`
	Location  domain.ConfluenceCommentLocation `json:"location,omitempty"`
}

type ConfluenceCommentInventoryResult struct {
	SchemaVersion    int                                  `json:"schema_version"`
	PageID           string                               `json:"page_id"`
	PageVersion      int                                  `json:"page_version"`
	PageVersionGated bool                                 `json:"page_version_gated"`
	Query            ConfluenceCommentQuery               `json:"query"`
	Complete         bool                                 `json:"complete"`
	CommentsComplete bool                                 `json:"comments_complete"`
	ThreadsComplete  bool                                 `json:"threads_complete"`
	AnchorsComplete  bool                                 `json:"anchors_complete"`
	Count            int                                  `json:"count"`
	RootCount        int                                  `json:"root_count"`
	PartialReasons   []string                             `json:"partial_reasons"`
	Capabilities     domain.ConfluenceCommentCapabilities `json:"capabilities"`
	Comments         []ConfluenceCommentResultRecord      `json:"comments"`
	Diagnostics      []ConfluenceCommentResultDiagnostic  `json:"diagnostics"`
}

func ValidateConfluenceCommentInventoryOpts(opts ConfluenceCommentInventoryOpts) error {
	switch opts.Location {
	case "", "all", "footer", "inline", "resolved":
	default:
		return fmt.Errorf("%w: --location must be all, footer, inline, or resolved", domain.ErrUsage)
	}
	switch opts.State {
	case "", "all", "open", "resolved", "unknown":
	default:
		return fmt.Errorf("%w: --state must be all, open, resolved, or unknown", domain.ErrUsage)
	}
	switch opts.Depth {
	case "", "all", "root":
	default:
		return fmt.Errorf("%w: --depth must be root or all", domain.ErrUsage)
	}
	if opts.ExpectedPageVersion < 0 {
		return fmt.Errorf("%w: --expected-version must be positive (0 disables the gate)", domain.ErrUsage)
	}
	return nil
}

func normalizeConfluenceCommentOpts(opts ConfluenceCommentInventoryOpts) ConfluenceCommentInventoryOpts {
	if opts.Location == "" {
		opts.Location = "all"
	}
	if opts.State == "" {
		opts.State = "all"
	}
	if opts.Depth == "" {
		opts.Depth = "all"
	}
	return opts
}

func (s *ConfluenceService) CommentInventory(ctx context.Context, reference string, opts ConfluenceCommentInventoryOpts) (*ConfluenceCommentInventoryResult, error) {
	opts = normalizeConfluenceCommentOpts(opts)
	if err := ValidateConfluenceCommentInventoryOpts(opts); err != nil {
		return nil, err
	}
	resolved, err := s.ResolvePageReference(ctx, reference)
	if err != nil {
		return nil, err
	}
	page, err := s.store.GetPage(ctx, resolved.ID, domain.PullOpts{Format: "csf"})
	if err != nil {
		return nil, err
	}
	if page == nil || strings.TrimSpace(page.ID) == "" || page.ID != resolved.ID || page.Version <= 0 {
		return nil, fmt.Errorf("%w: Confluence comment page metadata is not reconciled", domain.ErrCheckFailed)
	}
	if opts.ExpectedPageVersion > 0 && page.Version != opts.ExpectedPageVersion {
		return nil, fmt.Errorf("%w: Confluence page version changed from %d to %d", domain.ErrVersionConflict, opts.ExpectedPageVersion, page.Version)
	}
	return s.commentInventoryForPage(ctx, page, opts)
}

// commentInventoryForPage qualifies comments against an already fetched native
// page snapshot. Pull uses this form so --comments never performs a second page
// GET whose version or CSF bytes could differ from the mirrored substrate.
func (s *ConfluenceService) commentInventoryForPage(ctx context.Context, page *domain.Resource, opts ConfluenceCommentInventoryOpts) (*ConfluenceCommentInventoryResult, error) {
	opts = normalizeConfluenceCommentOpts(opts)
	if err := ValidateConfluenceCommentInventoryOpts(opts); err != nil {
		return nil, err
	}
	if page == nil || strings.TrimSpace(page.ID) == "" || page.Version <= 0 {
		return nil, fmt.Errorf("%w: Confluence comment page metadata is not reconciled", domain.ErrCheckFailed)
	}
	readOpts := domain.ConfluenceCommentReadOptions{DepthAll: opts.Depth == "all"}
	readOpts.Locations = confluenceCommentReadLocations(opts.Location)
	qualified, ok := s.store.(domain.QualifiedConfluenceCommentReader)
	var inventory domain.ConfluenceCommentInventory
	var err error
	if ok {
		inventory, err = qualified.ListConfluenceComments(ctx, page.ID, readOpts)
	} else {
		inventory, err = s.legacyConfluenceCommentInventory(ctx, page.ID)
	}
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateConfluenceCommentInventory(inventory); err != nil {
		return nil, err
	}
	return buildConfluenceCommentInventoryResult(page, inventory, opts), nil
}

func confluenceCommentReadLocations(location string) []domain.ConfluenceCommentSelector {
	switch location {
	case "footer":
		return []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorFooter}
	case "inline":
		return []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorInline, domain.ConfluenceCommentSelectorResolved}
	case "resolved":
		return []domain.ConfluenceCommentSelector{domain.ConfluenceCommentSelectorResolved}
	default:
		return nil
	}
}

func (s *ConfluenceService) legacyConfluenceCommentInventory(ctx context.Context, pageID string) (domain.ConfluenceCommentInventory, error) {
	comments, truncated, err := s.store.ListComments(ctx, pageID)
	if err != nil {
		return domain.ConfluenceCommentInventory{}, err
	}
	records := make([]domain.ConfluenceCommentRecord, 0, len(comments))
	for _, comment := range comments {
		records = append(records, domain.ConfluenceCommentRecord{
			ID: comment.ID, PageID: pageID,
			Relation:          domain.ConfluenceCommentRelationUnknown,
			Location:          domain.ConfluenceCommentLocationUnknown,
			Resolution:        domain.ConfluenceCommentResolutionUnknown,
			AuthorID:          firstNonEmptyString(comment.AuthorKey, comment.AuthorName),
			AuthorDisplayName: comment.Author, CreatedAt: comment.Created,
			Body: comment.Body, BodyStorage: comment.BodyStorage,
		})
	}
	unknown := domain.ConfluenceCapabilityUnknown
	reasons := []string{domain.ConfluenceCommentPartialLegacyUnqualified}
	diagnostics := []domain.ConfluenceCommentDiagnostic{{Code: domain.ConfluenceCommentPartialLegacyUnqualified}}
	if truncated {
		reasons = append(reasons, domain.ConfluenceCommentPartialPageLimit)
		sort.Strings(reasons)
		diagnostics = append(diagnostics, domain.ConfluenceCommentDiagnostic{Code: domain.ConfluenceCommentPartialPageLimit})
	}
	return domain.ConfluenceCommentInventory{
		Comments: records, CommentsComplete: false, ThreadsComplete: false,
		PartialReasons: reasons,
		Capabilities: domain.ConfluenceCommentCapabilities{
			Footer: unknown, Inline: unknown, Resolved: unknown, DepthAll: unknown,
			ThreadAncestry: unknown, InlineProperties: unknown, Resolution: unknown,
		},
		Diagnostics: diagnostics,
	}, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func buildConfluenceCommentInventoryResult(page *domain.Resource, inventory domain.ConfluenceCommentInventory, opts ConfluenceCommentInventoryOpts) *ConfluenceCommentInventoryResult {
	reasons := make(map[string]struct{}, len(inventory.PartialReasons)+3)
	for _, reason := range inventory.PartialReasons {
		reasons[reason] = struct{}{}
	}
	diagnostics := make([]ConfluenceCommentResultDiagnostic, 0, len(inventory.Diagnostics)+4)
	for _, diagnostic := range inventory.Diagnostics {
		diagnostics = append(diagnostics, ConfluenceCommentResultDiagnostic{
			Code: diagnostic.Code, CommentID: diagnostic.CommentID, Selector: diagnostic.Selector,
		})
	}

	markersByRef := map[string][]csf.InlineCommentMarker{}
	markersAvailable := page.BodyPresent
	if markersAvailable {
		markers, parseErr := csf.ExtractInlineCommentMarkers(page.Body)
		if parseErr != nil {
			markersAvailable = false
		} else {
			for _, marker := range markers {
				markersByRef[marker.Ref] = append(markersByRef[marker.Ref], marker)
			}
		}
	}
	anchorRelevant := opts.Location != "footer"
	anchorsComplete := !anchorRelevant || markersAvailable
	if anchorRelevant && (!inventory.CommentsComplete ||
		hasStringKey(reasons, domain.ConfluenceCommentPartialInlineExpansionUnavailable) ||
		hasStringKey(reasons, domain.ConfluenceCommentPartialLocationUnavailable)) {
		anchorsComplete = false
	}
	if anchorRelevant && !markersAvailable {
		reasons[domain.ConfluenceCommentPartialPageBodyUnavailable] = struct{}{}
		diagnostics = append(diagnostics, ConfluenceCommentResultDiagnostic{Code: domain.ConfluenceCommentPartialPageBodyUnavailable})
	}

	seenMarkerRefs := map[string]struct{}{}
	comments := make([]ConfluenceCommentResultRecord, 0, len(inventory.Comments))
	commentsComplete := inventory.CommentsComplete
	for _, comment := range inventory.Comments {
		location := publicConfluenceCommentLocation(comment.Location)
		if comment.MarkerRef != "" {
			// State filtering must not turn the marker of an intentionally omitted
			// or not-yet-qualified inline comment into an orphan diagnostic.
			seenMarkerRefs[comment.MarkerRef] = struct{}{}
		}
		if opts.State != "all" && comment.Resolution == domain.ConfluenceCommentResolutionUnknown &&
			confluenceCommentMatchesLocationAndDepth(comment, location, opts) {
			commentsComplete = false
			reasons[domain.ConfluenceCommentPartialResolutionUnavailable] = struct{}{}
		}
		if !confluenceCommentMatches(comment, location, opts) {
			continue
		}
		row := ConfluenceCommentResultRecord{
			ID: comment.ID, PageID: comment.PageID, ParentID: cloneStringPointer(comment.ParentID), RootID: cloneStringPointer(comment.RootID),
			Relation: comment.Relation, Location: location, Resolution: comment.Resolution, Version: comment.Version,
			Author:    ConfluenceCommentAuthor{ID: comment.AuthorID, DisplayName: comment.AuthorDisplayName},
			CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt, Body: comment.Body, BodyStorage: comment.BodyStorage,
		}
		if comment.MarkerRef != "" || location == domain.ConfluenceCommentLocationInline {
			anchor := &ConfluenceInlineAnchor{MarkerRef: comment.MarkerRef, OriginalSelection: comment.OriginalSelection}
			switch {
			case !markersAvailable || comment.MarkerRef == "":
				anchor.Status = domain.ConfluenceAnchorUnavailable
				anchorsComplete = false
				reasons[domain.ConfluenceCommentPartialInlineExpansionUnavailable] = struct{}{}
			case len(markersByRef[comment.MarkerRef]) == 0:
				anchor.Status = domain.ConfluenceAnchorMissing
				anchorsComplete = false
				reasons[domain.ConfluenceCommentPartialAnchorMissing] = struct{}{}
				diagnostics = append(diagnostics, ConfluenceCommentResultDiagnostic{Code: domain.ConfluenceCommentPartialAnchorMissing, CommentID: comment.ID, MarkerRef: comment.MarkerRef, Location: location})
			case len(markersByRef[comment.MarkerRef]) > 1:
				anchor.Status = domain.ConfluenceAnchorAmbiguous
				anchorsComplete = false
				reasons[domain.ConfluenceCommentPartialAnchorAmbiguous] = struct{}{}
				diagnostics = append(diagnostics, ConfluenceCommentResultDiagnostic{Code: domain.ConfluenceCommentPartialAnchorAmbiguous, CommentID: comment.ID, MarkerRef: comment.MarkerRef, Location: location})
			default:
				anchor.Status = domain.ConfluenceAnchorMatched
				anchor.ObservedSelection = markersByRef[comment.MarkerRef][0].Selection
				if comment.OriginalSelection != "" && comment.OriginalSelection != anchor.ObservedSelection {
					diagnostics = append(diagnostics, ConfluenceCommentResultDiagnostic{Code: domain.ConfluenceCommentDiagnosticOriginalSelectionChanged, CommentID: comment.ID, MarkerRef: comment.MarkerRef, Location: location})
				}
			}
			row.Anchor = anchor
		}
		comments = append(comments, row)
	}
	// Only the all and inline selectors exhaust both open and resolved inline
	// comments. A resolved-only projection cannot classify unrelated markers.
	if markersAvailable && (opts.Location == "all" || opts.Location == "inline") {
		for markerRef := range markersByRef {
			if _, exists := seenMarkerRefs[markerRef]; !exists {
				diagnostics = append(diagnostics, ConfluenceCommentResultDiagnostic{Code: domain.ConfluenceCommentDiagnosticOrphanMarker, MarkerRef: markerRef})
			}
		}
	}
	comments = sortConfluenceCommentResults(comments)
	sort.Slice(diagnostics, func(i, j int) bool {
		a, b := diagnostics[i], diagnostics[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.CommentID != b.CommentID {
			return a.CommentID < b.CommentID
		}
		if a.MarkerRef != b.MarkerRef {
			return a.MarkerRef < b.MarkerRef
		}
		return a.Location < b.Location
	})
	partialReasons := make([]string, 0, len(reasons))
	for reason := range reasons {
		partialReasons = append(partialReasons, reason)
	}
	sort.Strings(partialReasons)
	rootCount := 0
	for _, comment := range comments {
		if comment.Relation == domain.ConfluenceCommentRelationRoot {
			rootCount++
		}
	}
	threadsComplete := inventory.ThreadsComplete
	complete := commentsComplete && threadsComplete && anchorsComplete
	return &ConfluenceCommentInventoryResult{
		SchemaVersion: confluenceCommentInventorySchemaVersion, PageID: page.ID, PageVersion: page.Version,
		PageVersionGated: opts.ExpectedPageVersion > 0,
		Query:            ConfluenceCommentQuery{Mode: "list", Location: opts.Location, State: opts.State, Depth: opts.Depth},
		Complete:         complete, CommentsComplete: commentsComplete, ThreadsComplete: threadsComplete, AnchorsComplete: anchorsComplete,
		Count: len(comments), RootCount: rootCount, PartialReasons: partialReasons, Capabilities: inventory.Capabilities,
		Comments: comments, Diagnostics: diagnostics,
	}
}

func publicConfluenceCommentLocation(location domain.ConfluenceCommentLocation) domain.ConfluenceCommentLocation {
	if !domain.ValidConfluenceCommentLocation(location) || location == "" {
		return domain.ConfluenceCommentLocationUnknown
	}
	return location
}

func confluenceCommentMatches(comment domain.ConfluenceCommentRecord, publicLocation domain.ConfluenceCommentLocation, opts ConfluenceCommentInventoryOpts) bool {
	return confluenceCommentMatchesLocationAndDepth(comment, publicLocation, opts) &&
		(opts.State == "all" || string(comment.Resolution) == opts.State)
}

func confluenceCommentMatchesLocationAndDepth(comment domain.ConfluenceCommentRecord, publicLocation domain.ConfluenceCommentLocation, opts ConfluenceCommentInventoryOpts) bool {
	locationMatches := opts.Location == "all" ||
		(opts.Location == "footer" && publicLocation == domain.ConfluenceCommentLocationFooter) ||
		(opts.Location == "inline" && publicLocation == domain.ConfluenceCommentLocationInline) ||
		(opts.Location == "resolved" && comment.Resolution == domain.ConfluenceCommentResolutionResolved)
	depthMatches := opts.Depth == "all" || comment.Relation == domain.ConfluenceCommentRelationRoot
	return locationMatches && depthMatches
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func sortConfluenceCommentResults(comments []ConfluenceCommentResultRecord) []ConfluenceCommentResultRecord {
	less := func(a, b ConfluenceCommentResultRecord) bool {
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return a.ID < b.ID
	}
	byParent := map[string][]ConfluenceCommentResultRecord{}
	roots := []ConfluenceCommentResultRecord{}
	remaining := []ConfluenceCommentResultRecord{}
	for _, comment := range comments {
		switch {
		case comment.Relation == domain.ConfluenceCommentRelationRoot:
			roots = append(roots, comment)
		case comment.Relation == domain.ConfluenceCommentRelationReply && comment.ParentID != nil:
			byParent[*comment.ParentID] = append(byParent[*comment.ParentID], comment)
		default:
			remaining = append(remaining, comment)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return less(roots[i], roots[j]) })
	for parent := range byParent {
		rows := byParent[parent]
		sort.Slice(rows, func(i, j int) bool { return less(rows[i], rows[j]) })
		byParent[parent] = rows
	}
	sort.Slice(remaining, func(i, j int) bool { return less(remaining[i], remaining[j]) })
	out := make([]ConfluenceCommentResultRecord, 0, len(comments))
	seen := map[string]struct{}{}
	var appendTree func(ConfluenceCommentResultRecord)
	appendTree = func(comment ConfluenceCommentResultRecord) {
		if _, exists := seen[comment.ID]; exists {
			return
		}
		seen[comment.ID] = struct{}{}
		out = append(out, comment)
		for _, child := range byParent[comment.ID] {
			appendTree(child)
		}
	}
	for _, root := range roots {
		appendTree(root)
	}
	for _, comment := range remaining {
		appendTree(comment)
	}
	for _, comment := range comments {
		appendTree(comment)
	}
	return out
}

func (s *ConfluenceService) CommentThread(ctx context.Context, reference, commentID string, expectedVersion int) (*ConfluenceCommentInventoryResult, error) {
	if err := ValidateConfluenceCommentID(commentID); err != nil {
		return nil, err
	}
	result, err := s.CommentInventory(ctx, reference, ConfluenceCommentInventoryOpts{
		Location: "all", State: "all", Depth: "all", ExpectedPageVersion: expectedVersion,
	})
	if err != nil {
		return nil, err
	}
	var selected *ConfluenceCommentResultRecord
	for i := range result.Comments {
		if result.Comments[i].ID == commentID {
			selected = &result.Comments[i]
			break
		}
	}
	if selected == nil {
		if result.CommentsComplete {
			return nil, fmt.Errorf("%w: Confluence comment %s was not found", domain.ErrNotFound, commentID)
		}
		return nil, fmt.Errorf("%w: partial Confluence inventory cannot prove comment absence", domain.ErrCheckFailed)
	}
	rootID := selected.ID
	if selected.RootID != nil {
		rootID = *selected.RootID
	}
	thread := make([]ConfluenceCommentResultRecord, 0)
	if selected.Relation == domain.ConfluenceCommentRelationUnknown {
		thread = append(thread, *selected)
		result.ThreadsComplete = false
		result.Complete = false
		result.PartialReasons = appendUniqueSorted(result.PartialReasons, domain.ConfluenceCommentPartialParentUnavailable)
	} else {
		for _, comment := range result.Comments {
			if comment.ID == rootID || (comment.RootID != nil && *comment.RootID == rootID) {
				thread = append(thread, comment)
			}
		}
	}
	result.Comments = sortConfluenceCommentResults(thread)
	result.Count = len(result.Comments)
	result.RootCount = 0
	for _, comment := range result.Comments {
		if comment.Relation == domain.ConfluenceCommentRelationRoot {
			result.RootCount++
		}
	}
	result.Query = ConfluenceCommentQuery{Mode: "thread", Location: "all", State: "all", Depth: "all", CommentID: commentID}
	return result, nil
}

func ValidateConfluenceCommentID(value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: comment id must be a positive numeric content id", domain.ErrUsage)
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return fmt.Errorf("%w: comment id must be a positive numeric content id", domain.ErrUsage)
	}
	return nil
}

func appendUniqueSorted(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	values = append(values, value)
	sort.Strings(values)
	return values
}

func hasStringKey(values map[string]struct{}, value string) bool {
	_, exists := values[value]
	return exists
}

func confluenceCommentInventoryTruncated(result *ConfluenceCommentInventoryResult) bool {
	if result == nil {
		return false
	}
	for _, reason := range result.PartialReasons {
		switch reason {
		case domain.ConfluenceCommentPartialPageLimit,
			domain.ConfluenceCommentPartialItemLimit,
			domain.ConfluenceCommentPartialPaginationStalled,
			domain.ConfluenceCommentPartialPaginationUnqualified,
			domain.ConfluenceCommentPartialBackendOmittedChildren:
			return true
		}
	}
	return false
}
