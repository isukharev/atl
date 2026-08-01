package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

const confluenceCommentInventorySchemaVersion = 2

const ConfluenceCommentViewSchemaVersion = 1

type ConfluenceCommentInventoryOpts struct {
	Location            string
	State               string
	Depth               string
	ExpectedPageVersion int
	// MaxPages and MaxItems are optional aggregate bounds for qualified reads.
	// Zero preserves the historical CLI defaults.
	MaxPages int
	MaxItems int
}

type ConfluenceCommentThreadOpts struct {
	ExpectedPageVersion int
	MaxPages            int
	MaxItems            int
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

// ConfluenceCommentViewBounds records the resolved transport limits that
// qualified one transient comment read. Values are positive in every emitted
// view; zero remains reserved for the service's legacy-default input contract.
type ConfluenceCommentViewBounds struct {
	MaxCommentPages int `json:"max_comment_pages"`
	MaxItems        int `json:"max_items"`
	MaxBytes        int `json:"max_bytes"`
}

// ConfluenceCommentViewAnchor is deliberately selection-free. Marker identity
// and qualification status are sufficient for routing; original and observed
// page text stay outside the typed transport.
type ConfluenceCommentViewAnchor struct {
	MarkerRef string                        `json:"marker_ref"`
	Status    domain.ConfluenceAnchorStatus `json:"status"`
}

type ConfluenceCommentViewDiagnostic struct {
	Code      string                           `json:"code"`
	CommentID string                           `json:"comment_id,omitempty"`
	MarkerRef string                           `json:"marker_ref,omitempty"`
	Selector  domain.ConfluenceCommentSelector `json:"selector,omitempty"`
	Location  domain.ConfluenceCommentLocation `json:"location,omitempty"`
}

// ConfluenceCommentListViewRecord is a body-free discovery projection. It has
// no native-storage, rendered-body, URL, page-title, or anchor-selection field.
type ConfluenceCommentListViewRecord struct {
	ID         string                             `json:"id"`
	ParentID   *string                            `json:"parent_id"`
	RootID     *string                            `json:"root_id"`
	Relation   domain.ConfluenceCommentRelation   `json:"relation"`
	Location   domain.ConfluenceCommentLocation   `json:"location"`
	Resolution domain.ConfluenceCommentResolution `json:"resolution"`
	Version    int                                `json:"version"`
	Author     ConfluenceCommentAuthor            `json:"author"`
	CreatedAt  string                             `json:"created_at"`
	UpdatedAt  string                             `json:"updated_at"`
	Anchor     *ConfluenceCommentViewAnchor       `json:"anchor"`
}

// ConfluenceCommentThreadViewRecord adds only reconciled plain text. A nil
// BodyText is explicit partial evidence that native storage was unavailable;
// a non-nil empty string is a successfully parsed comment with no text.
type ConfluenceCommentThreadViewRecord struct {
	ID         string                             `json:"id"`
	ParentID   *string                            `json:"parent_id"`
	RootID     *string                            `json:"root_id"`
	Relation   domain.ConfluenceCommentRelation   `json:"relation"`
	Location   domain.ConfluenceCommentLocation   `json:"location"`
	Resolution domain.ConfluenceCommentResolution `json:"resolution"`
	Version    int                                `json:"version"`
	Author     ConfluenceCommentAuthor            `json:"author"`
	CreatedAt  string                             `json:"created_at"`
	UpdatedAt  string                             `json:"updated_at"`
	BodyText   *string                            `json:"body_text"`
	Anchor     *ConfluenceCommentViewAnchor       `json:"anchor"`
}

type ConfluenceCommentListView struct {
	SchemaVersion    int                                  `json:"schema_version"`
	PageID           string                               `json:"page_id"`
	PageVersion      int                                  `json:"page_version"`
	PageVersionGated bool                                 `json:"page_version_gated"`
	Query            ConfluenceCommentQuery               `json:"query"`
	Bounds           ConfluenceCommentViewBounds          `json:"bounds"`
	Complete         bool                                 `json:"complete"`
	CommentsComplete bool                                 `json:"comments_complete"`
	ThreadsComplete  bool                                 `json:"threads_complete"`
	AnchorsComplete  bool                                 `json:"anchors_complete"`
	Count            int                                  `json:"count"`
	RootCount        int                                  `json:"root_count"`
	PartialReasons   []string                             `json:"partial_reasons"`
	Capabilities     domain.ConfluenceCommentCapabilities `json:"capabilities"`
	Comments         []ConfluenceCommentListViewRecord    `json:"comments"`
	Diagnostics      []ConfluenceCommentViewDiagnostic    `json:"diagnostics"`
}

type ConfluenceCommentThreadView struct {
	SchemaVersion    int                                  `json:"schema_version"`
	PageID           string                               `json:"page_id"`
	PageVersion      int                                  `json:"page_version"`
	PageVersionGated bool                                 `json:"page_version_gated"`
	Query            ConfluenceCommentQuery               `json:"query"`
	Bounds           ConfluenceCommentViewBounds          `json:"bounds"`
	Complete         bool                                 `json:"complete"`
	CommentsComplete bool                                 `json:"comments_complete"`
	ThreadsComplete  bool                                 `json:"threads_complete"`
	AnchorsComplete  bool                                 `json:"anchors_complete"`
	Count            int                                  `json:"count"`
	RootCount        int                                  `json:"root_count"`
	PartialReasons   []string                             `json:"partial_reasons"`
	Capabilities     domain.ConfluenceCommentCapabilities `json:"capabilities"`
	Comments         []ConfluenceCommentThreadViewRecord  `json:"comments"`
	Diagnostics      []ConfluenceCommentViewDiagnostic    `json:"diagnostics"`
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
	if opts.MaxPages < 0 || opts.MaxItems < 0 {
		return fmt.Errorf("%w: Confluence comment page and item bounds must be zero or positive", domain.ErrUsage)
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
	readOpts := domain.ConfluenceCommentReadOptions{
		DepthAll: opts.Depth == "all", MaxPages: opts.MaxPages, MaxItems: opts.MaxItems,
	}
	readOpts.Locations = confluenceCommentReadLocations(opts.Location)
	qualified, ok := s.store.(domain.QualifiedConfluenceCommentReader)
	var inventory domain.ConfluenceCommentInventory
	var err error
	if ok {
		inventory, err = qualified.ListConfluenceComments(ctx, page.ID, readOpts)
	} else if opts.MaxPages > 0 || opts.MaxItems > 0 {
		return nil, fmt.Errorf("%w: legacy Confluence comment reader cannot enforce explicit bounds", domain.ErrCheckFailed)
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
	return s.CommentThreadWithOptions(ctx, reference, commentID, ConfluenceCommentThreadOpts{ExpectedPageVersion: expectedVersion})
}

// CommentThreadWithOptions preserves the exact thread-selection semantics while
// allowing a typed transport to impose stricter aggregate read bounds. The
// historical CommentThread method delegates with zero legacy-default bounds.
func (s *ConfluenceService) CommentThreadWithOptions(ctx context.Context, reference, commentID string, opts ConfluenceCommentThreadOpts) (*ConfluenceCommentInventoryResult, error) {
	if err := ValidateConfluenceCommentID(commentID); err != nil {
		return nil, err
	}
	result, err := s.CommentInventory(ctx, reference, ConfluenceCommentInventoryOpts{
		Location: "all", State: "all", Depth: "all", ExpectedPageVersion: opts.ExpectedPageVersion,
		MaxPages: opts.MaxPages, MaxItems: opts.MaxItems,
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

// ProjectConfluenceCommentListView copies one qualified inventory into a
// closed, body-free transport projection. Validation happens on both sides of
// the copy so malformed source evidence and future accidental view widening
// fail before emission.
func ProjectConfluenceCommentListView(result *ConfluenceCommentInventoryResult, bounds ConfluenceCommentViewBounds) (*ConfluenceCommentListView, error) {
	if err := validateConfluenceCommentInventoryResult(result, "list"); err != nil {
		return nil, err
	}
	if err := validateConfluenceCommentViewBounds(bounds); err != nil {
		return nil, err
	}
	comments := make([]ConfluenceCommentListViewRecord, 0, len(result.Comments))
	for _, comment := range result.Comments {
		comments = append(comments, ConfluenceCommentListViewRecord{
			ID: comment.ID, ParentID: cloneStringPointer(comment.ParentID), RootID: cloneStringPointer(comment.RootID),
			Relation: comment.Relation, Location: comment.Location, Resolution: comment.Resolution,
			Version: comment.Version, Author: comment.Author, CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt,
			Anchor: projectConfluenceCommentViewAnchor(comment.Anchor),
		})
	}
	view := &ConfluenceCommentListView{
		SchemaVersion: ConfluenceCommentViewSchemaVersion, PageID: result.PageID, PageVersion: result.PageVersion,
		PageVersionGated: result.PageVersionGated, Query: result.Query, Bounds: bounds,
		Complete: result.Complete, CommentsComplete: result.CommentsComplete,
		ThreadsComplete: result.ThreadsComplete, AnchorsComplete: result.AnchorsComplete,
		Count: result.Count, RootCount: result.RootCount,
		PartialReasons: cloneStringsNonNil(result.PartialReasons), Capabilities: result.Capabilities,
		Comments: comments, Diagnostics: projectConfluenceCommentViewDiagnostics(result.Diagnostics),
	}
	if err := ValidateConfluenceCommentListView(view); err != nil {
		return nil, err
	}
	return view, nil
}

// ProjectConfluenceCommentThreadView projects only plain text derived directly
// from successfully parsed native storage. It never trusts the source Body
// convenience field because that field intentionally falls back to raw CSF for
// the legacy CLI contract when parsing fails.
func ProjectConfluenceCommentThreadView(result *ConfluenceCommentInventoryResult, bounds ConfluenceCommentViewBounds) (*ConfluenceCommentThreadView, error) {
	if err := validateConfluenceCommentInventoryResult(result, "thread"); err != nil {
		return nil, err
	}
	if err := validateConfluenceCommentViewBounds(bounds); err != nil {
		return nil, err
	}
	comments := make([]ConfluenceCommentThreadViewRecord, 0, len(result.Comments))
	for _, comment := range result.Comments {
		var bodyText *string
		if comment.BodyStorage != "" {
			root, err := csf.Parse([]byte(comment.BodyStorage))
			if err != nil {
				return nil, fmt.Errorf("%w: Confluence comment body cannot be projected as reconciled plain text", domain.ErrCheckFailed)
			}
			plain := csf.TextContent(root)
			bodyText = &plain
		}
		comments = append(comments, ConfluenceCommentThreadViewRecord{
			ID: comment.ID, ParentID: cloneStringPointer(comment.ParentID), RootID: cloneStringPointer(comment.RootID),
			Relation: comment.Relation, Location: comment.Location, Resolution: comment.Resolution,
			Version: comment.Version, Author: comment.Author, CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt,
			BodyText: bodyText, Anchor: projectConfluenceCommentViewAnchor(comment.Anchor),
		})
	}
	view := &ConfluenceCommentThreadView{
		SchemaVersion: ConfluenceCommentViewSchemaVersion, PageID: result.PageID, PageVersion: result.PageVersion,
		PageVersionGated: result.PageVersionGated, Query: result.Query, Bounds: bounds,
		Complete: result.Complete, CommentsComplete: result.CommentsComplete,
		ThreadsComplete: result.ThreadsComplete, AnchorsComplete: result.AnchorsComplete,
		Count: result.Count, RootCount: result.RootCount,
		PartialReasons: cloneStringsNonNil(result.PartialReasons), Capabilities: result.Capabilities,
		Comments: comments, Diagnostics: projectConfluenceCommentViewDiagnostics(result.Diagnostics),
	}
	if err := ValidateConfluenceCommentThreadView(view); err != nil {
		return nil, err
	}
	return view, nil
}

func projectConfluenceCommentViewAnchor(anchor *ConfluenceInlineAnchor) *ConfluenceCommentViewAnchor {
	if anchor == nil {
		return nil
	}
	return &ConfluenceCommentViewAnchor{MarkerRef: anchor.MarkerRef, Status: anchor.Status}
}

func projectConfluenceCommentViewDiagnostics(in []ConfluenceCommentResultDiagnostic) []ConfluenceCommentViewDiagnostic {
	out := make([]ConfluenceCommentViewDiagnostic, 0, len(in))
	for _, diagnostic := range in {
		out = append(out, ConfluenceCommentViewDiagnostic(diagnostic))
	}
	return out
}

func cloneStringsNonNil(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func validateConfluenceCommentViewBounds(bounds ConfluenceCommentViewBounds) error {
	if bounds.MaxCommentPages < 1 || bounds.MaxItems < 1 || bounds.MaxBytes < 1 {
		return fmt.Errorf("%w: Confluence comment view bounds must be positive", domain.ErrCheckFailed)
	}
	return nil
}

func validateConfluenceCommentInventoryResult(result *ConfluenceCommentInventoryResult, mode string) error {
	if result == nil || result.SchemaVersion != confluenceCommentInventorySchemaVersion ||
		!canonicalConfluenceContentID(result.PageID) || result.PageVersion < 1 ||
		result.PartialReasons == nil || result.Comments == nil || result.Diagnostics == nil {
		return fmt.Errorf("%w: Confluence comment result provenance is not reconciled", domain.ErrCheckFailed)
	}
	if err := validateConfluenceCommentQuery(result.Query, mode); err != nil {
		return err
	}
	if result.Complete != (result.CommentsComplete && result.ThreadsComplete && result.AnchorsComplete) ||
		result.Complete != (len(result.PartialReasons) == 0) ||
		result.Count != len(result.Comments) || result.RootCount < 0 || result.RootCount > result.Count {
		return fmt.Errorf("%w: Confluence comment result accounting is not reconciled", domain.ErrCheckFailed)
	}
	if err := validateConfluenceCommentQualification(result.PartialReasons, result.Capabilities); err != nil {
		return err
	}
	rootCount := 0
	seen := make(map[string]struct{}, len(result.Comments))
	byID := make(map[string]ConfluenceCommentResultRecord, len(result.Comments))
	for _, comment := range result.Comments {
		if comment.PageID != result.PageID || !canonicalConfluenceContentID(comment.ID) ||
			!optionalCanonicalConfluenceContentID(comment.ParentID) || !optionalCanonicalConfluenceContentID(comment.RootID) ||
			comment.Version < 0 ||
			!domain.ValidConfluenceCommentRelation(comment.Relation) ||
			!domain.ValidConfluenceCommentLocation(comment.Location) ||
			!domain.ValidConfluenceCommentResolution(comment.Resolution) {
			return fmt.Errorf("%w: Confluence comment result record is not reconciled", domain.ErrCheckFailed)
		}
		if err := validateConfluenceCommentViewAuthor(comment.Author); err != nil {
			return err
		}
		if !validConfluenceCommentTimestamp(comment.CreatedAt) || !validConfluenceCommentTimestamp(comment.UpdatedAt) {
			return fmt.Errorf("%w: Confluence comment timestamps are not reconciled", domain.ErrCheckFailed)
		}
		if _, duplicate := seen[comment.ID]; duplicate {
			return fmt.Errorf("%w: Confluence comment result repeats an identity", domain.ErrCheckFailed)
		}
		seen[comment.ID] = struct{}{}
		byID[comment.ID] = comment
		if err := validateConfluenceCommentRelationship(comment.ID, comment.Relation, comment.ParentID, comment.RootID); err != nil {
			return err
		}
		if comment.Relation == domain.ConfluenceCommentRelationRoot {
			rootCount++
		}
		if err := validateConfluenceCommentViewAnchor(projectConfluenceCommentViewAnchor(comment.Anchor)); err != nil {
			return err
		}
	}
	if rootCount != result.RootCount {
		return fmt.Errorf("%w: Confluence comment root count is not reconciled", domain.ErrCheckFailed)
	}
	if mode == "thread" && result.ThreadsComplete {
		if err := validateConfluenceCommentThreadAncestry(byID); err != nil {
			return err
		}
	}
	return validateConfluenceCommentResultDiagnostics(result.Diagnostics)
}

func validateConfluenceCommentQuery(query ConfluenceCommentQuery, mode string) error {
	if query.Mode != mode {
		return fmt.Errorf("%w: Confluence comment query mode is not reconciled", domain.ErrCheckFailed)
	}
	switch query.Location {
	case "all", "footer", "inline", "resolved":
	default:
		return fmt.Errorf("%w: Confluence comment query location is not reconciled", domain.ErrCheckFailed)
	}
	switch query.State {
	case "all", "open", "resolved", "unknown":
	default:
		return fmt.Errorf("%w: Confluence comment query state is not reconciled", domain.ErrCheckFailed)
	}
	switch query.Depth {
	case "all", "root":
	default:
		return fmt.Errorf("%w: Confluence comment query depth is not reconciled", domain.ErrCheckFailed)
	}
	if mode == "list" && query.CommentID != "" {
		return fmt.Errorf("%w: Confluence comment list unexpectedly selects an identity", domain.ErrCheckFailed)
	}
	if mode == "thread" {
		if err := ValidateConfluenceCommentID(query.CommentID); err != nil {
			return fmt.Errorf("%w: Confluence comment thread identity is not reconciled", domain.ErrCheckFailed)
		}
		if query.Location != "all" || query.State != "all" || query.Depth != "all" {
			return fmt.Errorf("%w: Confluence comment thread query is not canonical", domain.ErrCheckFailed)
		}
	}
	return nil
}

func validateConfluenceCommentQualification(reasons []string, capabilities domain.ConfluenceCommentCapabilities) error {
	if !sort.StringsAreSorted(reasons) {
		return fmt.Errorf("%w: Confluence comment partial reasons are not canonical", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(reasons))
	for _, reason := range reasons {
		if !domain.ValidConfluenceCommentPartialReason(reason) {
			return fmt.Errorf("%w: Confluence comment result has an unknown partial reason", domain.ErrCheckFailed)
		}
		if _, duplicate := seen[reason]; duplicate {
			return fmt.Errorf("%w: Confluence comment result repeats a partial reason", domain.ErrCheckFailed)
		}
		seen[reason] = struct{}{}
	}
	for _, status := range []domain.ConfluenceCapabilityStatus{
		capabilities.Footer, capabilities.Inline, capabilities.Resolved, capabilities.DepthAll,
		capabilities.ThreadAncestry, capabilities.InlineProperties, capabilities.Resolution,
	} {
		if !domain.ValidConfluenceCapabilityStatus(status) {
			return fmt.Errorf("%w: Confluence comment capabilities are not reconciled", domain.ErrCheckFailed)
		}
	}
	return nil
}

func validateConfluenceCommentRelationship(id string, relation domain.ConfluenceCommentRelation, parentID, rootID *string) error {
	switch relation {
	case domain.ConfluenceCommentRelationRoot:
		if parentID != nil || rootID == nil || *rootID != id {
			return fmt.Errorf("%w: Confluence comment root relationship is not reconciled", domain.ErrCheckFailed)
		}
	case domain.ConfluenceCommentRelationReply:
		if parentID == nil || rootID == nil || *parentID == "" || *rootID == "" || *parentID == id || *rootID == id {
			return fmt.Errorf("%w: Confluence comment reply relationship is not reconciled", domain.ErrCheckFailed)
		}
	case domain.ConfluenceCommentRelationUnknown:
		if parentID != nil || rootID != nil {
			return fmt.Errorf("%w: unknown Confluence comment relationship carries inferred ancestry", domain.ErrCheckFailed)
		}
	}
	return nil
}

func validateConfluenceCommentViewAnchor(anchor *ConfluenceCommentViewAnchor) error {
	if anchor == nil {
		return nil
	}
	if !domain.ValidConfluenceAnchorStatus(anchor.Status) ||
		(anchor.MarkerRef == "" && anchor.Status != domain.ConfluenceAnchorUnavailable) ||
		(anchor.MarkerRef != "" && !validConfluenceCommentMarkerRef(anchor.MarkerRef)) {
		return fmt.Errorf("%w: Confluence comment anchor is not reconciled", domain.ErrCheckFailed)
	}
	return nil
}

func validateConfluenceCommentViewAuthor(author ConfluenceCommentAuthor) error {
	if !utf8.ValidString(author.ID) || !utf8.ValidString(author.DisplayName) ||
		strings.Contains(author.ID, "@") || strings.Contains(author.DisplayName, "@") ||
		strings.Contains(author.ID, "://") || strings.Contains(author.DisplayName, "://") {
		return fmt.Errorf("%w: Confluence comment author is not privacy-safe", domain.ErrCheckFailed)
	}
	return nil
}

func validConfluenceCommentTimestamp(value string) bool {
	if value == "" {
		return true
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func validConfluenceCommentMarkerRef(value string) bool {
	if len(value) < 1 || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func validateConfluenceCommentResultDiagnostics(diagnostics []ConfluenceCommentResultDiagnostic) error {
	for _, diagnostic := range diagnostics {
		if !domain.ValidConfluenceCommentDiagnosticCode(diagnostic.Code) ||
			(diagnostic.CommentID != "" && !canonicalConfluenceContentID(diagnostic.CommentID)) ||
			(diagnostic.MarkerRef != "" && !validConfluenceCommentMarkerRef(diagnostic.MarkerRef)) ||
			(diagnostic.Selector != "" && !domain.ValidConfluenceCommentSelector(diagnostic.Selector)) ||
			(diagnostic.Location != "" && !domain.ValidConfluenceCommentLocation(diagnostic.Location)) {
			return fmt.Errorf("%w: Confluence comment diagnostic is not reconciled", domain.ErrCheckFailed)
		}
	}
	return nil
}

func validateConfluenceCommentViewDiagnostics(diagnostics []ConfluenceCommentViewDiagnostic) error {
	for _, diagnostic := range diagnostics {
		if !domain.ValidConfluenceCommentDiagnosticCode(diagnostic.Code) ||
			(diagnostic.CommentID != "" && !canonicalConfluenceContentID(diagnostic.CommentID)) ||
			(diagnostic.MarkerRef != "" && !validConfluenceCommentMarkerRef(diagnostic.MarkerRef)) ||
			(diagnostic.Selector != "" && !domain.ValidConfluenceCommentSelector(diagnostic.Selector)) ||
			(diagnostic.Location != "" && !domain.ValidConfluenceCommentLocation(diagnostic.Location)) {
			return fmt.Errorf("%w: Confluence comment view diagnostic is not reconciled", domain.ErrCheckFailed)
		}
	}
	return nil
}

func validateConfluenceCommentThreadAncestry(byID map[string]ConfluenceCommentResultRecord) error {
	for _, comment := range byID {
		if comment.Relation == domain.ConfluenceCommentRelationRoot {
			continue
		}
		if comment.Relation != domain.ConfluenceCommentRelationReply || comment.ParentID == nil || comment.RootID == nil {
			return fmt.Errorf("%w: complete Confluence comment thread has unknown ancestry", domain.ErrCheckFailed)
		}
		rootID := *comment.RootID
		root, rootOK := byID[rootID]
		if !rootOK || root.Relation != domain.ConfluenceCommentRelationRoot || root.RootID == nil || *root.RootID != rootID {
			return fmt.Errorf("%w: complete Confluence comment thread omits ancestry", domain.ErrCheckFailed)
		}
		seen := map[string]struct{}{comment.ID: {}}
		current := comment
		for current.Relation == domain.ConfluenceCommentRelationReply && current.ParentID != nil {
			parentID := *current.ParentID
			if _, duplicate := seen[parentID]; duplicate {
				return fmt.Errorf("%w: complete Confluence comment thread has cyclic ancestry", domain.ErrCheckFailed)
			}
			seen[parentID] = struct{}{}
			parent, present := byID[parentID]
			if !present {
				return fmt.Errorf("%w: complete Confluence comment thread omits ancestry", domain.ErrCheckFailed)
			}
			if parentID == rootID {
				if parent.Relation != domain.ConfluenceCommentRelationRoot {
					return fmt.Errorf("%w: complete Confluence comment thread has inconsistent ancestry", domain.ErrCheckFailed)
				}
				break
			}
			if parent.Relation != domain.ConfluenceCommentRelationReply || parent.RootID == nil || *parent.RootID != rootID {
				return fmt.Errorf("%w: complete Confluence comment thread has inconsistent ancestry", domain.ErrCheckFailed)
			}
			current = parent
		}
	}
	return nil
}

// ValidateConfluenceCommentListView validates the closed body-free projection
// independently of its source result.
func ValidateConfluenceCommentListView(view *ConfluenceCommentListView) error {
	if view == nil || view.SchemaVersion != ConfluenceCommentViewSchemaVersion || !canonicalConfluenceContentID(view.PageID) ||
		view.PageVersion < 1 || view.PartialReasons == nil || view.Comments == nil || view.Diagnostics == nil {
		return fmt.Errorf("%w: Confluence comment list view provenance is not reconciled", domain.ErrCheckFailed)
	}
	if err := validateConfluenceCommentQuery(view.Query, "list"); err != nil {
		return err
	}
	if err := validateConfluenceCommentViewBounds(view.Bounds); err != nil {
		return err
	}
	if view.Complete != (view.CommentsComplete && view.ThreadsComplete && view.AnchorsComplete) ||
		view.Complete != (len(view.PartialReasons) == 0) || view.Count != len(view.Comments) {
		return fmt.Errorf("%w: Confluence comment list view accounting is not reconciled", domain.ErrCheckFailed)
	}
	if err := validateConfluenceCommentQualification(view.PartialReasons, view.Capabilities); err != nil {
		return err
	}
	rootCount := 0
	seen := make(map[string]struct{}, len(view.Comments))
	for _, comment := range view.Comments {
		if !canonicalConfluenceContentID(comment.ID) ||
			!optionalCanonicalConfluenceContentID(comment.ParentID) || !optionalCanonicalConfluenceContentID(comment.RootID) ||
			comment.Version < 0 || !domain.ValidConfluenceCommentRelation(comment.Relation) ||
			!domain.ValidConfluenceCommentLocation(comment.Location) || !domain.ValidConfluenceCommentResolution(comment.Resolution) {
			return fmt.Errorf("%w: Confluence comment list record is not reconciled", domain.ErrCheckFailed)
		}
		if err := validateConfluenceCommentViewAuthor(comment.Author); err != nil {
			return err
		}
		if !validConfluenceCommentTimestamp(comment.CreatedAt) || !validConfluenceCommentTimestamp(comment.UpdatedAt) {
			return fmt.Errorf("%w: Confluence comment list timestamps are not reconciled", domain.ErrCheckFailed)
		}
		if _, duplicate := seen[comment.ID]; duplicate {
			return fmt.Errorf("%w: Confluence comment list repeats an identity", domain.ErrCheckFailed)
		}
		seen[comment.ID] = struct{}{}
		if err := validateConfluenceCommentRelationship(comment.ID, comment.Relation, comment.ParentID, comment.RootID); err != nil {
			return err
		}
		if comment.Relation == domain.ConfluenceCommentRelationRoot {
			rootCount++
		}
		if err := validateConfluenceCommentViewAnchor(comment.Anchor); err != nil {
			return err
		}
	}
	if rootCount != view.RootCount {
		return fmt.Errorf("%w: Confluence comment list root count is not reconciled", domain.ErrCheckFailed)
	}
	return validateConfluenceCommentViewDiagnostics(view.Diagnostics)
}

// ValidateConfluenceCommentThreadView additionally validates self-contained
// complete ancestry and the nullable reconciled body projection.
func ValidateConfluenceCommentThreadView(view *ConfluenceCommentThreadView) error {
	if view == nil || view.SchemaVersion != ConfluenceCommentViewSchemaVersion || !canonicalConfluenceContentID(view.PageID) ||
		view.PageVersion < 1 || view.PartialReasons == nil || view.Comments == nil || view.Diagnostics == nil {
		return fmt.Errorf("%w: Confluence comment thread view provenance is not reconciled", domain.ErrCheckFailed)
	}
	if err := validateConfluenceCommentQuery(view.Query, "thread"); err != nil {
		return err
	}
	if err := validateConfluenceCommentViewBounds(view.Bounds); err != nil {
		return err
	}
	if view.Complete != (view.CommentsComplete && view.ThreadsComplete && view.AnchorsComplete) ||
		view.Complete != (len(view.PartialReasons) == 0) || view.Count != len(view.Comments) {
		return fmt.Errorf("%w: Confluence comment thread view accounting is not reconciled", domain.ErrCheckFailed)
	}
	if err := validateConfluenceCommentQualification(view.PartialReasons, view.Capabilities); err != nil {
		return err
	}
	rootCount := 0
	seen := make(map[string]struct{}, len(view.Comments))
	byID := make(map[string]ConfluenceCommentResultRecord, len(view.Comments))
	for _, comment := range view.Comments {
		if !canonicalConfluenceContentID(comment.ID) ||
			!optionalCanonicalConfluenceContentID(comment.ParentID) || !optionalCanonicalConfluenceContentID(comment.RootID) ||
			comment.Version < 0 || !domain.ValidConfluenceCommentRelation(comment.Relation) ||
			!domain.ValidConfluenceCommentLocation(comment.Location) || !domain.ValidConfluenceCommentResolution(comment.Resolution) ||
			(comment.BodyText != nil && !utf8.ValidString(*comment.BodyText)) {
			return fmt.Errorf("%w: Confluence comment thread record is not reconciled", domain.ErrCheckFailed)
		}
		if err := validateConfluenceCommentViewAuthor(comment.Author); err != nil {
			return err
		}
		if !validConfluenceCommentTimestamp(comment.CreatedAt) || !validConfluenceCommentTimestamp(comment.UpdatedAt) {
			return fmt.Errorf("%w: Confluence comment thread timestamps are not reconciled", domain.ErrCheckFailed)
		}
		if comment.BodyText == nil && !hasResultPartialReason(view.PartialReasons, domain.ConfluenceCommentPartialBodyUnavailable) {
			return fmt.Errorf("%w: Confluence comment thread omits body text without qualification", domain.ErrCheckFailed)
		}
		if _, duplicate := seen[comment.ID]; duplicate {
			return fmt.Errorf("%w: Confluence comment thread repeats an identity", domain.ErrCheckFailed)
		}
		seen[comment.ID] = struct{}{}
		if err := validateConfluenceCommentRelationship(comment.ID, comment.Relation, comment.ParentID, comment.RootID); err != nil {
			return err
		}
		if comment.Relation == domain.ConfluenceCommentRelationRoot {
			rootCount++
		}
		if err := validateConfluenceCommentViewAnchor(comment.Anchor); err != nil {
			return err
		}
		byID[comment.ID] = ConfluenceCommentResultRecord{
			ID: comment.ID, ParentID: comment.ParentID, RootID: comment.RootID, Relation: comment.Relation,
		}
	}
	if rootCount != view.RootCount {
		return fmt.Errorf("%w: Confluence comment thread root count is not reconciled", domain.ErrCheckFailed)
	}
	if view.ThreadsComplete {
		if err := validateConfluenceCommentThreadAncestry(byID); err != nil {
			return err
		}
	}
	return validateConfluenceCommentViewDiagnostics(view.Diagnostics)
}

func hasResultPartialReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func canonicalConfluenceContentID(value string) bool {
	if value == "" || value[0] == '0' || strings.TrimSpace(value) != value {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func optionalCanonicalConfluenceContentID(value *string) bool {
	return value == nil || canonicalConfluenceContentID(*value)
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
