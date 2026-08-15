package domain

const (
	ConfluencePullIncludeAssets      = "assets"
	ConfluencePullIncludeComments    = "comments"
	ConfluencePullIncludeAttachments = "attachments"

	ConfluencePullIncludeQualified = "qualified"
	ConfluencePullIncludePartial   = "partial"

	ConfluencePullIncludeReasonResolutionIncomplete = "resolution_incomplete"
	ConfluencePullIncludeReasonInventoryIncomplete  = "inventory_incomplete"
	ConfluencePullIncludeReasonBodyIncomplete       = "body_incomplete"
)

// ConfluencePullIncludeEvidence is the content-free, per-page evidence that a
// complete-pull publication journal binds to the same accepted page. Failed or
// deferred work is never checkpointed as accepted evidence.
type ConfluencePullIncludeEvidence struct {
	Dimension     string `json:"dimension"`
	Qualification string `json:"qualification"`
	Reason        string `json:"reason,omitempty"`
	// BodyBytes is private complete-pull accounting for successfully published
	// attachment bodies. It is not a public pull-result field and is valid only
	// for the attachment dimension.
	BodyBytes int64 `json:"body_bytes,omitempty"`
}

func ValidConfluencePullIncludeEvidence(value ConfluencePullIncludeEvidence) bool {
	if value.BodyBytes < 0 || value.Dimension != ConfluencePullIncludeAttachments && value.BodyBytes != 0 {
		return false
	}
	if value.Dimension != ConfluencePullIncludeAssets && value.Dimension != ConfluencePullIncludeComments &&
		value.Dimension != ConfluencePullIncludeAttachments {
		return false
	}
	switch value.Qualification {
	case ConfluencePullIncludeQualified:
		return value.Reason == ""
	case ConfluencePullIncludePartial:
		return value.Reason == ConfluencePullIncludeReasonResolutionIncomplete ||
			value.Reason == ConfluencePullIncludeReasonInventoryIncomplete ||
			value.Dimension == ConfluencePullIncludeAttachments && value.Reason == ConfluencePullIncludeReasonBodyIncomplete
	}
	return false
}

// ValidLegacyConfluencePullIncludeEvidence recognizes the exact closed
// vocabulary written by complete-pull schema v5. It keeps a new attachment
// evidence row from being accepted as historical journal evidence.
func ValidLegacyConfluencePullIncludeEvidence(value ConfluencePullIncludeEvidence) bool {
	if value.Dimension != ConfluencePullIncludeAssets && value.Dimension != ConfluencePullIncludeComments {
		return false
	}
	return ValidConfluencePullIncludeEvidence(value)
}
