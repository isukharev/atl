package domain

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	JiraCommentReadMaxPages = 100
	JiraCommentReadMaxItems = 10_000
	// Jira evidence sidecars use the same durable field envelope in the adapter
	// and complete-pull owners. Keeping it in domain lets an adapter refuse an
	// unrepresentable remote record before an app has retained a bounded page.
	JiraEvidenceIDMaxBytes              = 64
	JiraCommentEvidenceMetadataMaxBytes = 64 << 10
	JiraCommentEvidenceBodyMaxBytes     = 64 << 20
	// JiraCommentReadMaxBytes bounds the conservative encoded size of one
	// qualified issue comment inventory. It keeps a direct adapter read finite;
	// complete-pull callers commonly pass a smaller exact transaction budget.
	JiraCommentReadMaxBytes = int64(256 << 20)

	JiraCommentPartialPageLimit         = "page_limit"
	JiraCommentPartialItemLimit         = "item_limit"
	JiraCommentPartialByteLimit         = "byte_limit"
	JiraCommentPartialPaginationStalled = "pagination_stalled"

	JiraAttachmentPartialFieldUnavailable = "field_unavailable"
	JiraAttachmentPartialItemLimit        = "item_limit"
	JiraAttachmentReadMaxItems            = 10_000
)

// JiraCommentReadOptions makes the qualified comment read finite at both the
// response-page, logical-item, and conservative serialized-byte dimensions.
// Callers must opt into explicit page/item bounds. MaxBytes zero selects the
// compatibility maximum above; complete-pull callers pass their exact
// remaining transaction capacity.
type JiraCommentReadOptions struct {
	MaxPages int
	MaxItems int
	MaxBytes int64
}

// JiraCommentInventory distinguishes a proven-empty comment set from a
// bounded prefix. Total is exact when TotalKnown is true; a complete inventory
// always has an exact total equal to len(Comments).
type JiraCommentInventory struct {
	Comments      []Comment
	Complete      bool
	PartialReason string
	Total         int
	TotalKnown    bool
	PageCount     int
}

// QualifiedJiraCommentReader is an optional tracker capability for callers
// that must preserve terminal pagination evidence. issueID is expected to be
// the provider-stable numeric issue identity rather than a mutable key.
type QualifiedJiraCommentReader interface {
	ListJiraCommentsQualified(context.Context, string, JiraCommentReadOptions) (JiraCommentInventory, error)
}

// JiraAttachmentInventory distinguishes an exact issue attachment field from
// an unavailable requested field. Jira exposes the inventory in one issue
// response rather than through an attachment pagination endpoint.
type JiraAttachmentInventory struct {
	Attachments   []Attachment
	Complete      bool
	PartialReason string
}

type JiraAttachmentReadOptions struct {
	MaxItems int
}

// QualifiedJiraAttachmentReader is an optional tracker capability for exact
// attachment-field evidence. issueID is the provider-stable numeric identity.
type QualifiedJiraAttachmentReader interface {
	ListJiraAttachmentsQualified(context.Context, string, JiraAttachmentReadOptions) (JiraAttachmentInventory, error)
}

// JiraAttachmentDownloadEvidence is one immediately revalidated attachment
// selector. The download reference is opaque adapter metadata: callers bind
// it to the inventory record before passing it back to Tracker.StreamAttachment.
type JiraAttachmentDownloadEvidence struct {
	ParentID   string
	Attachment Attachment
}

// QualifiedJiraAttachmentDownloadRevalidator narrows the interval between a
// qualified attachment inventory and its binary GET. Jira's content URL is an
// attachment-field attribute, so a body capture must re-read that exact parent
// field and prove the stable attachment identity still has the same durable
// metadata before it attributes bytes to the inventory record.
type QualifiedJiraAttachmentDownloadRevalidator interface {
	RevalidateJiraAttachmentDownload(context.Context, string, string) (JiraAttachmentDownloadEvidence, error)
}

// ValidJiraEvidenceParentRevision admits the exact immutable issue revision
// grammar used by both Jira comments and attachment sidecars.
func ValidJiraEvidenceParentRevision(value string) bool {
	return strings.TrimSpace(value) == value && ValidJiraCommentEvidenceMetadata(value, false)
}

// ValidJiraCommentEvidenceMetadata admits one optional sidecar metadata field.
func ValidJiraCommentEvidenceMetadata(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	return strings.TrimSpace(value) != "" && len(value) <= JiraCommentEvidenceMetadataMaxBytes && utf8.ValidString(value)
}

// ValidJiraCommentEvidenceRecord admits the intersection of the qualified
// Jira comments reader and the durable comments-sidecar grammar.
func ValidJiraCommentEvidenceRecord(value Comment) bool {
	return validJiraEvidenceID(value.ID, false) &&
		ValidJiraCommentEvidenceMetadata(value.Author, true) &&
		ValidJiraCommentEvidenceMetadata(value.AuthorName, true) &&
		ValidJiraCommentEvidenceMetadata(value.AuthorKey, true) &&
		ValidJiraCommentEvidenceMetadata(value.Created, true) &&
		ValidJiraCommentEvidenceMetadata(value.Updated, true) &&
		validJiraEvidenceID(value.ParentID, true) &&
		len(value.Body) <= JiraCommentEvidenceBodyMaxBytes && utf8.ValidString(value.Body)
}

func validJiraEvidenceID(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	if len(value) > JiraEvidenceIDMaxBytes || value[0] == '0' {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func ValidJiraCommentPartialReason(reason string) bool {
	switch reason {
	case JiraCommentPartialPageLimit, JiraCommentPartialItemLimit, JiraCommentPartialPaginationStalled:
		return true
	case JiraCommentPartialByteLimit:
		return true
	}
	return false
}

func ValidateJiraCommentReadOptions(options JiraCommentReadOptions) error {
	if options.MaxPages <= 0 || options.MaxPages > JiraCommentReadMaxPages {
		return fmt.Errorf("%w: Jira comment page bound is invalid", ErrUsage)
	}
	if options.MaxItems <= 0 || options.MaxItems > JiraCommentReadMaxItems {
		return fmt.Errorf("%w: Jira comment item bound is invalid", ErrUsage)
	}
	if options.MaxBytes < 0 || options.MaxBytes > JiraCommentReadMaxBytes {
		return fmt.Errorf("%w: Jira comment byte bound is invalid", ErrUsage)
	}
	return nil
}

func ValidateJiraCommentInventory(inventory JiraCommentInventory) error {
	if inventory.Comments == nil {
		return fmt.Errorf("%w: Jira comment inventory has an unavailable collection", ErrCheckFailed)
	}
	if len(inventory.Comments) > JiraCommentReadMaxItems {
		return fmt.Errorf("%w: Jira comment inventory exceeds the supported item bound", ErrCheckFailed)
	}
	if inventory.PageCount <= 0 || inventory.Total < 0 {
		return fmt.Errorf("%w: Jira comment inventory has invalid counters", ErrCheckFailed)
	}
	if inventory.TotalKnown && inventory.Total < len(inventory.Comments) {
		return fmt.Errorf("%w: Jira comment inventory total is inconsistent", ErrCheckFailed)
	}
	if inventory.Complete {
		if inventory.PartialReason != "" || !inventory.TotalKnown || inventory.Total != len(inventory.Comments) {
			return fmt.Errorf("%w: complete Jira comment inventory is inconsistent", ErrCheckFailed)
		}
	} else if !ValidJiraCommentPartialReason(inventory.PartialReason) {
		return fmt.Errorf("%w: partial Jira comment inventory has an invalid reason", ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(inventory.Comments))
	for _, comment := range inventory.Comments {
		if comment.ID == "" {
			return fmt.Errorf("%w: Jira comment inventory has a missing identity", ErrCheckFailed)
		}
		if _, duplicate := seen[comment.ID]; duplicate {
			return fmt.Errorf("%w: Jira comment inventory repeats an identity", ErrCheckFailed)
		}
		seen[comment.ID] = struct{}{}
	}
	return nil
}

func ValidateJiraAttachmentInventory(inventory JiraAttachmentInventory) error {
	if inventory.Attachments == nil {
		return fmt.Errorf("%w: Jira attachment inventory has an unavailable collection", ErrCheckFailed)
	}
	if len(inventory.Attachments) > JiraAttachmentReadMaxItems {
		return fmt.Errorf("%w: Jira attachment inventory exceeds the supported item bound", ErrCheckFailed)
	}
	if inventory.Complete {
		if inventory.PartialReason != "" {
			return fmt.Errorf("%w: complete Jira attachment inventory has a partial reason", ErrCheckFailed)
		}
	} else if inventory.PartialReason != JiraAttachmentPartialFieldUnavailable && inventory.PartialReason != JiraAttachmentPartialItemLimit {
		return fmt.Errorf("%w: partial Jira attachment inventory has an invalid reason", ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(inventory.Attachments))
	for _, attachment := range inventory.Attachments {
		if attachment.ID == "" || attachment.Title == "" || attachment.FileSize < 0 {
			return fmt.Errorf("%w: Jira attachment inventory has invalid metadata", ErrCheckFailed)
		}
		if _, duplicate := seen[attachment.ID]; duplicate {
			return fmt.Errorf("%w: Jira attachment inventory repeats an identity", ErrCheckFailed)
		}
		seen[attachment.ID] = struct{}{}
	}
	return nil
}

func ValidateJiraAttachmentReadOptions(options JiraAttachmentReadOptions) error {
	if options.MaxItems <= 0 || options.MaxItems > JiraAttachmentReadMaxItems {
		return fmt.Errorf("%w: Jira attachment item bound is invalid", ErrUsage)
	}
	return nil
}
