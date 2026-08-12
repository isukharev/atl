package domain

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ConfluenceCommentLocation is the semantic placement of one comment. Resolved
// is deliberately not a location: the REST API uses it as a query selector,
// while the emitted record remains inline with an independent resolution state.
type ConfluenceCommentLocation string

const (
	ConfluenceCommentLocationFooter  ConfluenceCommentLocation = "footer"
	ConfluenceCommentLocationInline  ConfluenceCommentLocation = "inline"
	ConfluenceCommentLocationUnknown ConfluenceCommentLocation = "unknown"
)

func ValidConfluenceCommentLocation(location ConfluenceCommentLocation) bool {
	switch location {
	case ConfluenceCommentLocationFooter, ConfluenceCommentLocationInline,
		ConfluenceCommentLocationUnknown:
		return true
	}
	return false
}

// ConfluenceCommentSelector is the documented location query vocabulary. The
// resolved selector returns resolved inline discussions; it is never copied to
// ConfluenceCommentRecord.Location.
type ConfluenceCommentSelector string

const (
	ConfluenceCommentSelectorFooter   ConfluenceCommentSelector = "footer"
	ConfluenceCommentSelectorInline   ConfluenceCommentSelector = "inline"
	ConfluenceCommentSelectorResolved ConfluenceCommentSelector = "resolved"
)

func ValidConfluenceCommentSelector(selector ConfluenceCommentSelector) bool {
	switch selector {
	case ConfluenceCommentSelectorFooter, ConfluenceCommentSelectorInline,
		ConfluenceCommentSelectorResolved:
		return true
	}
	return false
}

// ConfluenceCommentResolution is the independently reported thread resolution
// state. Unknown is never promoted to open.
type ConfluenceCommentResolution string

const (
	ConfluenceCommentResolutionOpen     ConfluenceCommentResolution = "open"
	ConfluenceCommentResolutionResolved ConfluenceCommentResolution = "resolved"
	ConfluenceCommentResolutionUnknown  ConfluenceCommentResolution = "unknown"
)

func ValidConfluenceCommentResolution(resolution ConfluenceCommentResolution) bool {
	switch resolution {
	case ConfluenceCommentResolutionOpen, ConfluenceCommentResolutionResolved,
		ConfluenceCommentResolutionUnknown:
		return true
	}
	return false
}

// ConfluenceCommentRelation records only relationships proven by an explicit
// ancestors projection. Query order, timestamps, titles, and bodies are never
// relationship evidence.
type ConfluenceCommentRelation string

const (
	ConfluenceCommentRelationRoot    ConfluenceCommentRelation = "root"
	ConfluenceCommentRelationReply   ConfluenceCommentRelation = "reply"
	ConfluenceCommentRelationUnknown ConfluenceCommentRelation = "unknown"
)

func ValidConfluenceCommentRelation(relation ConfluenceCommentRelation) bool {
	switch relation {
	case ConfluenceCommentRelationRoot, ConfluenceCommentRelationReply,
		ConfluenceCommentRelationUnknown:
		return true
	}
	return false
}

// ConfluenceAnchorStatus qualifies the later app-layer join between one inline
// comment and native CSF markers.
type ConfluenceAnchorStatus string

const (
	ConfluenceAnchorMatched     ConfluenceAnchorStatus = "matched"
	ConfluenceAnchorMissing     ConfluenceAnchorStatus = "missing"
	ConfluenceAnchorAmbiguous   ConfluenceAnchorStatus = "ambiguous"
	ConfluenceAnchorUnavailable ConfluenceAnchorStatus = "unavailable"
)

func ValidConfluenceAnchorStatus(status ConfluenceAnchorStatus) bool {
	switch status {
	case ConfluenceAnchorMatched, ConfluenceAnchorMissing,
		ConfluenceAnchorAmbiguous, ConfluenceAnchorUnavailable:
		return true
	}
	return false
}

// ConfluenceCapabilityStatus separates documented support from a response
// shape actually observed during this inventory.
type ConfluenceCapabilityStatus string

const (
	ConfluenceCapabilityObserved    ConfluenceCapabilityStatus = "observed"
	ConfluenceCapabilityDocumented  ConfluenceCapabilityStatus = "documented"
	ConfluenceCapabilityUnsupported ConfluenceCapabilityStatus = "unsupported"
	ConfluenceCapabilityUnknown     ConfluenceCapabilityStatus = "unknown"
)

func ValidConfluenceCapabilityStatus(status ConfluenceCapabilityStatus) bool {
	switch status {
	case ConfluenceCapabilityObserved, ConfluenceCapabilityDocumented,
		ConfluenceCapabilityUnsupported, ConfluenceCapabilityUnknown:
		return true
	}
	return false
}

// Closed, content-free reasons used by qualified comment inventories. These
// values may cross CLI/MCP boundaries; none contains backend-controlled text.
const (
	ConfluenceCommentPartialPageLimit                   = "page_limit"
	ConfluenceCommentPartialItemLimit                   = "item_limit"
	ConfluenceCommentPartialPaginationStalled           = "pagination_stalled"
	ConfluenceCommentPartialPaginationUnqualified       = "pagination_unqualified"
	ConfluenceCommentPartialConflictingDuplicates       = "conflicting_duplicate_objects"
	ConfluenceCommentPartialBackendOmittedChildren      = "backend_omitted_children"
	ConfluenceCommentPartialParentUnavailable           = "parent_unavailable"
	ConfluenceCommentPartialMalformedAncestry           = "malformed_ancestry"
	ConfluenceCommentPartialLocationUnavailable         = "location_unavailable"
	ConfluenceCommentPartialInlineExpansionUnavailable  = "inline_expansion_unavailable"
	ConfluenceCommentPartialResolutionUnavailable       = "resolution_unavailable"
	ConfluenceCommentPartialMetadataUnavailable         = "comment_metadata_unavailable"
	ConfluenceCommentPartialBodyUnavailable             = "comment_body_unavailable"
	ConfluenceCommentPartialPageBodyUnavailable         = "page_body_unavailable"
	ConfluenceCommentPartialAnchorMissing               = "anchor_missing"
	ConfluenceCommentPartialAnchorAmbiguous             = "anchor_ambiguous"
	ConfluenceCommentPartialEndpointUnavailable         = "endpoint_unavailable"
	ConfluenceCommentPartialForbidden                   = "forbidden"
	ConfluenceCommentPartialLegacyUnqualified           = "legacy_unqualified"
	ConfluenceCommentDiagnosticOrphanMarker             = "orphan_marker"
	ConfluenceCommentDiagnosticOriginalSelectionChanged = "original_selection_changed"
)

func ValidConfluenceCommentPartialReason(reason string) bool {
	switch reason {
	case ConfluenceCommentPartialPageLimit,
		ConfluenceCommentPartialItemLimit,
		ConfluenceCommentPartialPaginationStalled,
		ConfluenceCommentPartialPaginationUnqualified,
		ConfluenceCommentPartialConflictingDuplicates,
		ConfluenceCommentPartialBackendOmittedChildren,
		ConfluenceCommentPartialParentUnavailable,
		ConfluenceCommentPartialMalformedAncestry,
		ConfluenceCommentPartialLocationUnavailable,
		ConfluenceCommentPartialInlineExpansionUnavailable,
		ConfluenceCommentPartialResolutionUnavailable,
		ConfluenceCommentPartialMetadataUnavailable,
		ConfluenceCommentPartialBodyUnavailable,
		ConfluenceCommentPartialPageBodyUnavailable,
		ConfluenceCommentPartialAnchorMissing,
		ConfluenceCommentPartialAnchorAmbiguous,
		ConfluenceCommentPartialEndpointUnavailable,
		ConfluenceCommentPartialForbidden,
		ConfluenceCommentPartialLegacyUnqualified:
		return true
	}
	return false
}

func ValidConfluenceCommentDiagnosticCode(code string) bool {
	return ValidConfluenceCommentPartialReason(code) ||
		code == ConfluenceCommentDiagnosticOrphanMarker ||
		code == ConfluenceCommentDiagnosticOriginalSelectionChanged
}

// ConfluenceCommentReadOptions selects the fixed documented location queries.
// An empty Locations slice means all three locations in canonical order.
type ConfluenceCommentReadOptions struct {
	// ParentVersion binds every selector and pagination request to the exact
	// reconciled parent content revision. Qualified reads fail closed when it is
	// absent rather than silently reading the backend's current revision.
	ParentVersion int
	// Locations contains REST selectors, not emitted semantic locations. The
	// historical field name is retained to keep call sites concise.
	Locations []ConfluenceCommentSelector
	DepthAll  bool
	// MaxPages and MaxItems are optional aggregate safety bounds across every
	// selected location. Zero preserves the adapter's historical defaults;
	// positive values are enforced exactly and negative values are invalid.
	MaxPages int
	MaxItems int
}

// ValidateConfluenceCommentReadOptions rejects malformed explicit bounds and
// selectors before an adapter performs any backend work. Upper bounds belong
// to the transport that grants the read; the domain only distinguishes the
// legacy zero default from a positive explicit limit.
func ValidateConfluenceCommentReadOptions(options ConfluenceCommentReadOptions) error {
	if options.ParentVersion <= 0 {
		return fmt.Errorf("%w: Confluence comment parent version must be positive", ErrUsage)
	}
	if options.MaxPages < 0 || options.MaxItems < 0 {
		return fmt.Errorf("%w: Confluence comment page and item bounds must be zero or positive", ErrUsage)
	}
	for _, selector := range options.Locations {
		if !ValidConfluenceCommentSelector(selector) {
			return fmt.Errorf("%w: invalid Confluence comment location", ErrUsage)
		}
	}
	return nil
}

// ConfluenceUserIdentity is the minimal stable identity of the authenticated
// Confluence user. ID is the backend user key (or the documented legacy
// username fallback); email is deliberately not part of this capability.
type ConfluenceUserIdentity struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ValidateConfluenceUserIdentity rejects an identity that cannot safely bind a
// guarded comment write to the authenticated backend actor.
func ValidateConfluenceUserIdentity(identity ConfluenceUserIdentity) error {
	if strings.TrimSpace(identity.ID) == "" || strings.TrimSpace(identity.DisplayName) == "" {
		return fmt.Errorf("%w: Confluence current user omitted stable identity", ErrCheckFailed)
	}
	return nil
}

// ConfluenceCommentRecord is the lossless-enough read model shared by the
// Confluence adapter and app. ParentID and RootID are pointers so unknown is not
// confused with a proven root. BodyStorage is native CSF and is never rewritten.
type ConfluenceCommentRecord struct {
	ID                string                      `json:"id"`
	PageID            string                      `json:"page_id"`
	ParentID          *string                     `json:"parent_id"`
	RootID            *string                     `json:"root_id"`
	Relation          ConfluenceCommentRelation   `json:"relation"`
	Location          ConfluenceCommentLocation   `json:"location"`
	Resolution        ConfluenceCommentResolution `json:"resolution"`
	Version           int                         `json:"version"`
	AuthorID          string                      `json:"author_id,omitempty"`
	AuthorDisplayName string                      `json:"author_display_name,omitempty"`
	CreatedAt         string                      `json:"created_at,omitempty"`
	UpdatedAt         string                      `json:"updated_at,omitempty"`
	Body              string                      `json:"body"`
	BodyStorage       string                      `json:"body_storage"`
	MarkerRef         string                      `json:"marker_ref,omitempty"`
	OriginalSelection string                      `json:"original_selection,omitempty"`
}

// ConfluenceCommentCapabilities is fixed-shape invocation evidence. Documented
// means the REST contract names the feature but this response did not exercise
// its shape; observed requires a qualifying response object.
type ConfluenceCommentCapabilities struct {
	Footer           ConfluenceCapabilityStatus `json:"footer"`
	Inline           ConfluenceCapabilityStatus `json:"inline"`
	Resolved         ConfluenceCapabilityStatus `json:"resolved"`
	DepthAll         ConfluenceCapabilityStatus `json:"depth_all"`
	ThreadAncestry   ConfluenceCapabilityStatus `json:"thread_ancestry"`
	InlineProperties ConfluenceCapabilityStatus `json:"inline_properties"`
	Resolution       ConfluenceCapabilityStatus `json:"resolution"`
}

// ConfluenceCommentDiagnostic is deliberately content-free. Stable identifiers
// may locate the affected record, but messages and backend response text cannot
// cross this boundary.
type ConfluenceCommentDiagnostic struct {
	Code      string                    `json:"code"`
	CommentID string                    `json:"comment_id,omitempty"`
	Selector  ConfluenceCommentSelector `json:"selector,omitempty"`
}

// ConfluenceCommentInventory qualifies both set exhaustion and thread shape.
// Every collection is non-nil, including for a proven-empty page.
type ConfluenceCommentInventory struct {
	Comments         []ConfluenceCommentRecord     `json:"comments"`
	CommentsComplete bool                          `json:"comments_complete"`
	ThreadsComplete  bool                          `json:"threads_complete"`
	PartialReasons   []string                      `json:"partial_reasons"`
	Capabilities     ConfluenceCommentCapabilities `json:"capabilities"`
	Diagnostics      []ConfluenceCommentDiagnostic `json:"diagnostics"`
}

// QualifiedConfluenceCommentReader is optional and intentionally does not
// extend DocStore. Legacy callers keep DocStore.ListComments unchanged.
type QualifiedConfluenceCommentReader interface {
	ListConfluenceComments(context.Context, string, ConfluenceCommentReadOptions) (ConfluenceCommentInventory, error)
}

// ConfluenceCurrentUserReader is optional and intentionally separate from
// DocStore. It exposes only the stable backend identifier and display name.
type ConfluenceCurrentUserReader interface {
	CurrentConfluenceUser(context.Context) (ConfluenceUserIdentity, error)
}

// ValidateConfluenceCommentInventory prevents a malformed adapter snapshot from
// masquerading as qualified evidence. Errors are content-free and wrap
// ErrCheckFailed for stable exit classification.
func ValidateConfluenceCommentInventory(inventory ConfluenceCommentInventory) error {
	if inventory.Comments == nil || inventory.PartialReasons == nil || inventory.Diagnostics == nil {
		return fmt.Errorf("%w: Confluence comment inventory has an unavailable collection", ErrCheckFailed)
	}
	if (!inventory.CommentsComplete || !inventory.ThreadsComplete) && len(inventory.PartialReasons) == 0 {
		return fmt.Errorf("%w: partial Confluence comment inventory has no reason", ErrCheckFailed)
	}
	if !sort.StringsAreSorted(inventory.PartialReasons) {
		return fmt.Errorf("%w: Confluence comment partial reasons are not canonical", ErrCheckFailed)
	}
	seenReasons := make(map[string]struct{}, len(inventory.PartialReasons))
	for _, reason := range inventory.PartialReasons {
		if !ValidConfluenceCommentPartialReason(reason) {
			return fmt.Errorf("%w: Confluence comment inventory has an unknown partial reason", ErrCheckFailed)
		}
		if _, duplicate := seenReasons[reason]; duplicate {
			return fmt.Errorf("%w: Confluence comment inventory repeats a partial reason", ErrCheckFailed)
		}
		seenReasons[reason] = struct{}{}
	}
	statuses := []ConfluenceCapabilityStatus{
		inventory.Capabilities.Footer, inventory.Capabilities.Inline,
		inventory.Capabilities.Resolved, inventory.Capabilities.DepthAll,
		inventory.Capabilities.ThreadAncestry, inventory.Capabilities.InlineProperties,
		inventory.Capabilities.Resolution,
	}
	for _, status := range statuses {
		if !ValidConfluenceCapabilityStatus(status) {
			return fmt.Errorf("%w: Confluence comment inventory has an invalid capability status", ErrCheckFailed)
		}
	}
	seenIDs := make(map[string]struct{}, len(inventory.Comments))
	commentsByID := make(map[string]ConfluenceCommentRecord, len(inventory.Comments))
	for _, comment := range inventory.Comments {
		if strings.TrimSpace(comment.ID) == "" || strings.TrimSpace(comment.PageID) == "" {
			return fmt.Errorf("%w: Confluence comment inventory has an empty identity", ErrCheckFailed)
		}
		if _, duplicate := seenIDs[comment.ID]; duplicate {
			return fmt.Errorf("%w: Confluence comment inventory repeats a comment identity", ErrCheckFailed)
		}
		seenIDs[comment.ID] = struct{}{}
		commentsByID[comment.ID] = comment
		if !ValidConfluenceCommentRelation(comment.Relation) ||
			!ValidConfluenceCommentLocation(comment.Location) ||
			!ValidConfluenceCommentResolution(comment.Resolution) || comment.Version < 0 {
			return fmt.Errorf("%w: Confluence comment inventory has invalid record metadata", ErrCheckFailed)
		}
		switch comment.Relation {
		case ConfluenceCommentRelationRoot:
			if comment.ParentID != nil || comment.RootID == nil || *comment.RootID != comment.ID {
				return fmt.Errorf("%w: Confluence comment inventory has an invalid root relationship", ErrCheckFailed)
			}
		case ConfluenceCommentRelationReply:
			if comment.ParentID == nil || comment.RootID == nil || *comment.ParentID == "" || *comment.RootID == "" || *comment.ParentID == comment.ID || *comment.RootID == comment.ID {
				return fmt.Errorf("%w: Confluence comment inventory has an invalid reply relationship", ErrCheckFailed)
			}
		}
	}
	if inventory.ThreadsComplete {
		for _, comment := range inventory.Comments {
			if !completeConfluenceCommentAncestry(comment, commentsByID) {
				return fmt.Errorf("%w: complete Confluence comment inventory has inconsistent ancestry", ErrCheckFailed)
			}
		}
	}
	for _, diagnostic := range inventory.Diagnostics {
		if !ValidConfluenceCommentDiagnosticCode(diagnostic.Code) ||
			(diagnostic.Selector != "" && !ValidConfluenceCommentSelector(diagnostic.Selector)) {
			return fmt.Errorf("%w: Confluence comment inventory has an invalid diagnostic", ErrCheckFailed)
		}
	}
	return nil
}

func completeConfluenceCommentAncestry(comment ConfluenceCommentRecord, byID map[string]ConfluenceCommentRecord) bool {
	if comment.Relation == ConfluenceCommentRelationRoot {
		return true
	}
	if comment.Relation != ConfluenceCommentRelationReply || comment.RootID == nil || comment.ParentID == nil {
		return false
	}
	rootID := *comment.RootID
	root, present := byID[rootID]
	if !present || root.Relation != ConfluenceCommentRelationRoot || root.RootID == nil || *root.RootID != rootID {
		return false
	}
	seen := map[string]struct{}{comment.ID: {}}
	current := comment
	for current.Relation == ConfluenceCommentRelationReply && current.ParentID != nil {
		parentID := *current.ParentID
		if _, duplicate := seen[parentID]; duplicate {
			return false
		}
		seen[parentID] = struct{}{}
		parent, present := byID[parentID]
		if !present {
			return false
		}
		if parentID == rootID {
			return parent.Relation == ConfluenceCommentRelationRoot
		}
		if parent.Relation != ConfluenceCommentRelationReply || parent.RootID == nil || *parent.RootID != rootID {
			return false
		}
		current = parent
	}
	return false
}
