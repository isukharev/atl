package domain

import "context"

const (
	ConfluenceAttachmentDiscoveryPartialItemLimit             = "item_limit"
	ConfluenceAttachmentDiscoveryPartialRequestLimit          = "request_limit"
	ConfluenceAttachmentDiscoveryPartialResponseByteLimit     = "response_byte_limit"
	ConfluenceAttachmentDiscoveryPartialDeadline              = "deadline"
	ConfluenceAttachmentDiscoveryPartialPaginationStalled     = "pagination_stalled"
	ConfluenceAttachmentDiscoveryPartialPaginationUnqualified = "pagination_unqualified"

	ConfluenceAttachmentDiscoveryConsistencyLiveUnproven = "live_unproven"
)

func ValidConfluenceAttachmentDiscoveryPartialReason(reason string) bool {
	switch reason {
	case ConfluenceAttachmentDiscoveryPartialItemLimit,
		ConfluenceAttachmentDiscoveryPartialRequestLimit,
		ConfluenceAttachmentDiscoveryPartialResponseByteLimit,
		ConfluenceAttachmentDiscoveryPartialDeadline,
		ConfluenceAttachmentDiscoveryPartialPaginationStalled,
		ConfluenceAttachmentDiscoveryPartialPaginationUnqualified:
		return true
	}
	return false
}

// ConfluenceAttachmentMetadata is the metadata-only projection returned by a
// global attachment search. It deliberately carries no body, comment, URL, or
// download path. Title is untrusted backend evidence.
type ConfluenceAttachmentMetadata struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Type             string `json:"type"`
	Version          int    `json:"version"`
	ContainerID      string `json:"container_id"`
	ContainerType    string `json:"container_type"`
	ContainerVersion int    `json:"container_version"`
	Space            string `json:"space"`
	MediaType        string `json:"media_type"`
	FileSize         int64  `json:"file_size"`
}

// ConfluenceAttachmentDiscoveryRequest gives the adapter an explicit live
// offset and item bound. Physical request and response-byte bounds are carried
// below orchestration in the context ReadBudget.
type ConfluenceAttachmentDiscoveryRequest struct {
	Space    string
	CQL      string
	Start    int
	MaxItems int
}

// ConfluenceAttachmentDiscoveryPage is one bounded live prefix. NextStart is
// the safe offset to retry/continue from and never implies snapshot identity.
type ConfluenceAttachmentDiscoveryPage struct {
	Attachments   []ConfluenceAttachmentMetadata
	Start         int
	NextStart     *int
	TotalSize     *int
	Complete      bool
	PartialReason string
	Consistency   string
}

// QualifiedConfluenceAttachmentDiscoverer is the optional Server/Data Center
// metadata-search capability. Implementations issue only bounded same-backend
// content-search GETs and never fetch attachment bodies or returned URLs.
type QualifiedConfluenceAttachmentDiscoverer interface {
	DiscoverAttachmentsQualified(context.Context, ConfluenceAttachmentDiscoveryRequest) (ConfluenceAttachmentDiscoveryPage, error)
}
