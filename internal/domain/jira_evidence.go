package domain

import (
	"context"
	"fmt"
)

const (
	JiraCommentReadMaxPages = 100
	JiraCommentReadMaxItems = 10_000

	JiraCommentPartialPageLimit         = "page_limit"
	JiraCommentPartialItemLimit         = "item_limit"
	JiraCommentPartialPaginationStalled = "pagination_stalled"

	JiraAttachmentPartialFieldUnavailable = "field_unavailable"
	JiraAttachmentPartialItemLimit        = "item_limit"
	JiraAttachmentReadMaxItems            = 10_000
)

// JiraCommentReadOptions makes the qualified comment read finite at both the
// response-page and logical-item dimensions. Callers must opt into explicit
// positive bounds; the legacy Tracker surface supplies the compatibility
// maxima above.
type JiraCommentReadOptions struct {
	MaxPages int
	MaxItems int
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

func ValidJiraCommentPartialReason(reason string) bool {
	switch reason {
	case JiraCommentPartialPageLimit, JiraCommentPartialItemLimit, JiraCommentPartialPaginationStalled:
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
	return nil
}

func ValidateJiraCommentInventory(inventory JiraCommentInventory) error {
	if inventory.Comments == nil {
		return fmt.Errorf("%w: Jira comment inventory has an unavailable collection", ErrCheckFailed)
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
