package domain

const (
	ConfluencePullIncludeAssets   = "assets"
	ConfluencePullIncludeComments = "comments"

	ConfluencePullIncludeQualified = "qualified"
	ConfluencePullIncludePartial   = "partial"

	ConfluencePullIncludeReasonResolutionIncomplete = "resolution_incomplete"
	ConfluencePullIncludeReasonInventoryIncomplete  = "inventory_incomplete"
)

// ConfluencePullIncludeEvidence is the content-free, per-page evidence that a
// complete-pull publication journal binds to the same accepted page. Failed or
// deferred work is never checkpointed as accepted evidence.
type ConfluencePullIncludeEvidence struct {
	Dimension     string `json:"dimension"`
	Qualification string `json:"qualification"`
	Reason        string `json:"reason,omitempty"`
}

func ValidConfluencePullIncludeEvidence(value ConfluencePullIncludeEvidence) bool {
	if value.Dimension != ConfluencePullIncludeAssets && value.Dimension != ConfluencePullIncludeComments {
		return false
	}
	switch value.Qualification {
	case ConfluencePullIncludeQualified:
		return value.Reason == ""
	case ConfluencePullIncludePartial:
		return value.Reason == ConfluencePullIncludeReasonResolutionIncomplete ||
			value.Reason == ConfluencePullIncludeReasonInventoryIncomplete
	}
	return false
}
