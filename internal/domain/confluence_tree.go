package domain

import "context"

const (
	ConfluenceTreePartialItemLimit             = "item_limit"
	ConfluenceTreePartialScanLimit             = "scan_limit"
	ConfluenceTreePartialRequestLimit          = "request_limit"
	ConfluenceTreePartialResponseByteLimit     = "response_byte_limit"
	ConfluenceTreePartialDeadline              = "deadline"
	ConfluenceTreePartialPaginationStalled     = "pagination_stalled"
	ConfluenceTreePartialPaginationUnqualified = "pagination_unqualified"
	ConfluenceTreePartialLegacyUnqualified     = "legacy_unqualified"

	// ConfluenceTreeConsistencyLiveUnproven states the honest consistency
	// boundary of offset pagination: every page is a live backend observation,
	// not a server-provided snapshot transaction.
	ConfluenceTreeConsistencyLiveUnproven = "live_unproven"
)

// ValidConfluenceTreePartialReason keeps backend text and selectors out of the
// public qualification field.
func ValidConfluenceTreePartialReason(reason string) bool {
	switch reason {
	case ConfluenceTreePartialItemLimit,
		ConfluenceTreePartialScanLimit,
		ConfluenceTreePartialRequestLimit,
		ConfluenceTreePartialResponseByteLimit,
		ConfluenceTreePartialDeadline,
		ConfluenceTreePartialPaginationStalled,
		ConfluenceTreePartialPaginationUnqualified,
		ConfluenceTreePartialLegacyUnqualified:
		return true
	}
	return false
}

// ConfluenceTreeRequest carries adapter-owned hierarchy selection and item
// bounds. Physical request and response-byte limits remain below orchestration
// in the ReadBudget carried by ctx.
type ConfluenceTreeRequest struct {
	Space           string
	Depth           int
	MaxItems        int
	MaxScannedItems int
}

// ConfluenceTreePage is the adapter-qualified hierarchy prefix. Pages is
// always non-nil. Complete means the backend stopped advertising more pages
// before a bound or pagination inconsistency intervened.
type ConfluenceTreePage struct {
	Pages         []PageRef
	ScannedItems  int
	Complete      bool
	PartialReason string
	Consistency   string
}

// QualifiedConfluenceTreeReader is the optional bounded hierarchy capability.
// Implementations consume a command-scoped ReadBudget from ctx for every
// physical attempt and buffered response byte.
type QualifiedConfluenceTreeReader interface {
	TreeQualified(ctx context.Context, request ConfluenceTreeRequest) (ConfluenceTreePage, error)
}
