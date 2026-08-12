package corpus

import "time"

const CaptureReceiptSchemaV1 = 1

// CaptureDimension is the closed remote-evidence inventory recorded for every
// qualified service capture. Keeping omitted dimensions explicit prevents a
// body-only pull from being mistaken for a complete comments/assets capture.
type CaptureDimension string

const (
	CaptureNative      CaptureDimension = "native"
	CaptureMetadata    CaptureDimension = "metadata"
	CaptureComments    CaptureDimension = "comments"
	CaptureAttachments CaptureDimension = "attachments"
)

type CaptureDimensionState string

const (
	CaptureComplete     CaptureDimensionState = "complete"
	CapturePartial      CaptureDimensionState = "partial"
	CaptureNotRequested CaptureDimensionState = "not_requested"
)

type CaptureDimensionEvidence struct {
	Dimension CaptureDimension      `json:"dimension"`
	State     CaptureDimensionState `json:"state"`
}

// CaptureUsage contains exact physical transport usage for the service pull.
// Principal revalidation and other command overhead remain in the aggregate
// build result rather than being attributed to a provider snapshot.
type CaptureUsage struct {
	Attempts      int   `json:"attempts"`
	ResponseBytes int64 `json:"response_bytes"`
}

// CaptureReceipt is a canonical, content-free proof that one exact qualified
// remote selection reconciled with one pristine mirror snapshot. It contains
// no selector, backend URL, principal identifier, object identity, or path.
type CaptureReceipt struct {
	SchemaVersion   int                        `json:"schema_version"`
	Service         Service                    `json:"service"`
	ScopeDigest     string                     `json:"scope_digest"`
	SelectorDigest  string                     `json:"selector_digest"`
	OptionsDigest   string                     `json:"options_digest"`
	SelectionDigest string                     `json:"selection_digest"`
	SnapshotDigest  string                     `json:"snapshot_digest"`
	StartedAt       string                     `json:"started_at"`
	CompletedAt     string                     `json:"completed_at"`
	Total           int                        `json:"total"`
	Completed       int                        `json:"completed"`
	Usage           CaptureUsage               `json:"usage"`
	Dimensions      []CaptureDimensionEvidence `json:"dimensions"`
	ReceiptDigest   string                     `json:"receipt_digest"`
}

// CaptureReceiptInput omits codec-owned schema and digest fields.
type CaptureReceiptInput struct {
	Service         Service
	ScopeDigest     string
	SelectorDigest  string
	OptionsDigest   string
	SelectionDigest string
	SnapshotDigest  string
	StartedAt       time.Time
	CompletedAt     time.Time
	Total           int
	Completed       int
	Usage           CaptureUsage
	Dimensions      []CaptureDimensionEvidence
}
