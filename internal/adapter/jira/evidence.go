package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const jiraCommentPageSize = 100

// ListJiraCommentsQualified reads the dedicated comment endpoint through
// explicit page/item bounds. The caller supplies the provider-stable numeric
// issue id, so mutable issue keys do not enter captured identity.
func (j *Jira) ListJiraCommentsQualified(ctx context.Context, issueID string, options domain.JiraCommentReadOptions) (domain.JiraCommentInventory, error) {
	if strings.TrimSpace(issueID) == "" {
		return domain.JiraCommentInventory{}, fmt.Errorf("%w: Jira comment read requires an issue id", domain.ErrUsage)
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = domain.JiraCommentReadMaxBytes
	}
	if err := domain.ValidateJiraCommentReadOptions(options); err != nil {
		return domain.JiraCommentInventory{}, err
	}

	cursor := jiraOffsetCursor{}
	expectedTotal := -1
	out := []domain.Comment{}
	seenIDs := make(map[string]struct{})
	var encodedBytes int64
	for page := 0; page < options.MaxPages; page++ {
		remaining := options.MaxItems - len(out)
		if remaining <= 0 {
			return validatedJiraCommentInventory(domain.JiraCommentInventory{
				Comments: out, PartialReason: domain.JiraCommentPartialItemLimit,
				Total: expectedTotal, TotalKnown: expectedTotal >= 0, PageCount: page,
			})
		}
		requested := jiraCommentPageSize
		if remaining < requested {
			requested = remaining
		}
		var response struct {
			StartAt  *int `json:"startAt"`
			Total    *int `json:"total"`
			Comments []struct {
				ID       string          `json:"id"`
				Author   map[string]any  `json:"author"`
				Created  string          `json:"created"`
				Updated  string          `json:"updated"`
				ParentID string          `json:"parentId"`
				Body     json.RawMessage `json:"body"`
			} `json:"comments"`
		}
		query := url.Values{}
		query.Set("startAt", strconv.Itoa(cursor.requested()))
		query.Set("maxResults", strconv.Itoa(requested))
		path := "/rest/api/2/issue/" + url.PathEscape(issueID) + "/comment?" + query.Encode()
		if err := j.c.GetJSON(ctx, path, &response); err != nil {
			return domain.JiraCommentInventory{}, err
		}
		pageCount := page + 1
		if response.Total == nil || response.StartAt == nil || response.Comments == nil {
			return domain.JiraCommentInventory{}, fmt.Errorf("%w: Jira comment pagination metadata is unavailable", domain.ErrCheckFailed)
		}
		total := *response.Total
		if total < 0 || !cursor.matches(*response.StartAt) || len(response.Comments) > requested {
			return domain.JiraCommentInventory{}, fmt.Errorf("%w: Jira comment pagination metadata is inconsistent", domain.ErrCheckFailed)
		}
		if expectedTotal < 0 {
			expectedTotal = total
		} else if expectedTotal != total {
			return domain.JiraCommentInventory{}, fmt.Errorf("%w: Jira comment total changed while paging", domain.ErrCheckFailed)
		}

		for _, value := range response.Comments {
			if value.ID == "" {
				return domain.JiraCommentInventory{}, fmt.Errorf("%w: Jira comment identity is unavailable", domain.ErrCheckFailed)
			}
			if _, duplicate := seenIDs[value.ID]; duplicate {
				return domain.JiraCommentInventory{}, fmt.Errorf("%w: Jira comment identity is duplicated", domain.ErrCheckFailed)
			}
			if len(value.Body) == 0 || bytes.Equal(bytes.TrimSpace(value.Body), []byte("null")) {
				return domain.JiraCommentInventory{}, fmt.Errorf("%w: Jira comment body is unavailable", domain.ErrCheckFailed)
			}
			body, valid := decodeJiraRemoteLinkMetadata(value.Body)
			if !valid {
				return domain.JiraCommentInventory{}, fmt.Errorf("%w: Jira comment body is malformed", domain.ErrCheckFailed)
			}
			parentID := value.ParentID
			if parentID == "0" {
				parentID = ""
			}
			comment := domain.Comment{
				ID: value.ID, Author: nestedDisplay(value.Author), AuthorName: nestedName(value.Author),
				AuthorKey: nestedKey(value.Author), Created: value.Created, Updated: value.Updated,
				ParentID: parentID, Body: body,
			}
			if !domain.ValidJiraCommentEvidenceRecord(comment) {
				return domain.JiraCommentInventory{}, fmt.Errorf("%w: Jira comment cannot be represented in qualified evidence", domain.ErrCheckFailed)
			}
			footprint, bounded := jiraCommentConservativeEncodedBytes(comment)
			if !bounded || footprint > options.MaxBytes-encodedBytes {
				return validatedJiraCommentInventory(domain.JiraCommentInventory{
					Comments: out, PartialReason: domain.JiraCommentPartialByteLimit,
					Total: total, TotalKnown: true, PageCount: pageCount,
				})
			}
			seenIDs[value.ID] = struct{}{}
			out = append(out, comment)
			encodedBytes += footprint
		}

		decision := cursor.advance(len(response.Comments), &total)
		switch decision.state {
		case jiraOffsetBeyondTotal, jiraOffsetOverflow:
			return domain.JiraCommentInventory{}, fmt.Errorf("%w: Jira comment pagination made invalid progress", domain.ErrCheckFailed)
		case jiraOffsetStalled:
			return validatedJiraCommentInventory(domain.JiraCommentInventory{
				Comments: out, PartialReason: domain.JiraCommentPartialPaginationStalled,
				Total: total, TotalKnown: true, PageCount: pageCount,
			})
		case jiraOffsetComplete:
			return validatedJiraCommentInventory(domain.JiraCommentInventory{
				Comments: out, Complete: true, Total: total, TotalKnown: true, PageCount: pageCount,
			})
		}
		if len(out) >= options.MaxItems {
			return validatedJiraCommentInventory(domain.JiraCommentInventory{
				Comments: out, PartialReason: domain.JiraCommentPartialItemLimit,
				Total: total, TotalKnown: true, PageCount: pageCount,
			})
		}
		if pageCount >= options.MaxPages {
			return validatedJiraCommentInventory(domain.JiraCommentInventory{
				Comments: out, PartialReason: domain.JiraCommentPartialPageLimit,
				Total: total, TotalKnown: true, PageCount: pageCount,
			})
		}
	}
	panic("unreachable")
}

// jiraCommentConservativeEncodedBytes returns a strict upper bound for one
// sidecar record's JSON representation. Every UTF-8 byte may need a six-byte
// JSON escape; the fixed reserve covers member names, quotes, punctuation, and
// numeric/object framing. Callers use the bound before retaining a page member
// so a bounded complete-pull transaction cannot discover an oversized comments
// sidecar only after collecting many remote pages.
func jiraCommentConservativeEncodedBytes(comment domain.Comment) (int64, bool) {
	const recordOverhead int64 = 256
	bytes := recordOverhead
	for _, value := range []string{
		comment.ID, comment.Author, comment.AuthorName, comment.AuthorKey,
		comment.Created, comment.Updated, comment.ParentID, comment.Body,
	} {
		length := int64(len(value))
		if length > (domain.JiraCommentReadMaxBytes-bytes)/6 {
			return 0, false
		}
		bytes += length * 6
	}
	return bytes, true
}

func validatedJiraCommentInventory(inventory domain.JiraCommentInventory) (domain.JiraCommentInventory, error) {
	if err := domain.ValidateJiraCommentInventory(inventory); err != nil {
		return domain.JiraCommentInventory{}, err
	}
	return inventory, nil
}

// ListJiraAttachmentsQualified reads the exact issue attachment field and
// distinguishes an omitted/null field from a proven-empty array. It returns
// download references only as opaque adapter metadata; this method never
// dereferences them.
func (j *Jira) ListJiraAttachmentsQualified(ctx context.Context, issueID string, options domain.JiraAttachmentReadOptions) (domain.JiraAttachmentInventory, error) {
	if strings.TrimSpace(issueID) == "" {
		return domain.JiraAttachmentInventory{}, fmt.Errorf("%w: Jira attachment read requires an issue id", domain.ErrUsage)
	}
	if err := domain.ValidateJiraAttachmentReadOptions(options); err != nil {
		return domain.JiraAttachmentInventory{}, err
	}
	var response struct {
		ID     string                     `json:"id"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	path := "/rest/api/2/issue/" + url.PathEscape(issueID) + "?fields=attachment"
	if err := j.c.GetJSON(ctx, path, &response); err != nil {
		return domain.JiraAttachmentInventory{}, err
	}
	if response.ID == "" || response.ID != issueID {
		return domain.JiraAttachmentInventory{}, fmt.Errorf("%w: Jira attachment response identity is inconsistent", domain.ErrCheckFailed)
	}
	raw, present := response.Fields["attachment"]
	if !present || len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return validatedJiraAttachmentInventory(domain.JiraAttachmentInventory{
			Attachments: []domain.Attachment{}, PartialReason: domain.JiraAttachmentPartialFieldUnavailable,
		})
	}
	var values []struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
		MimeType string `json:"mimeType"`
		Size     int64  `json:"size"`
		Created  string `json:"created"`
		Content  string `json:"content"`
		Author   struct {
			Name        string `json:"name"`
			Key         string `json:"key"`
			DisplayName string `json:"displayName"`
		} `json:"author"`
	}
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return domain.JiraAttachmentInventory{}, fmt.Errorf("%w: Jira attachment field is malformed", domain.ErrCheckFailed)
	}
	attachments := make([]domain.Attachment, 0, len(values))
	partial := len(values) > options.MaxItems
	if partial {
		values = values[:options.MaxItems]
	}
	for _, value := range values {
		attachments = append(attachments, domain.Attachment{
			ID: value.ID, Title: value.Filename, MediaType: value.MimeType, FileSize: value.Size,
			Created: value.Created, Author: value.Author.DisplayName, AuthorName: value.Author.Name,
			AuthorKey: value.Author.Key, DownPath: value.Content,
		})
	}
	if partial {
		return validatedJiraAttachmentInventory(domain.JiraAttachmentInventory{Attachments: attachments, PartialReason: domain.JiraAttachmentPartialItemLimit})
	}
	return validatedJiraAttachmentInventory(domain.JiraAttachmentInventory{Attachments: attachments, Complete: true})
}

func validatedJiraAttachmentInventory(inventory domain.JiraAttachmentInventory) (domain.JiraAttachmentInventory, error) {
	if err := domain.ValidateJiraAttachmentInventory(inventory); err != nil {
		return domain.JiraAttachmentInventory{}, err
	}
	return inventory, nil
}

// RevalidateJiraAttachmentDownload re-reads the exact parent attachment field
// and returns one stable attachment record immediately before a binary GET.
// The qualified field inventory is bounded by the public maximum; an
// incomplete response never authorizes a body selector.
func (j *Jira) RevalidateJiraAttachmentDownload(ctx context.Context, issueID, attachmentID string) (domain.JiraAttachmentDownloadEvidence, error) {
	if strings.TrimSpace(issueID) == "" || strings.TrimSpace(attachmentID) == "" {
		return domain.JiraAttachmentDownloadEvidence{}, fmt.Errorf("%w: Jira attachment revalidation requires stable identities", domain.ErrUsage)
	}
	inventory, err := j.ListJiraAttachmentsQualified(ctx, issueID, domain.JiraAttachmentReadOptions{MaxItems: domain.JiraAttachmentReadMaxItems})
	if err != nil {
		return domain.JiraAttachmentDownloadEvidence{}, err
	}
	if !inventory.Complete {
		return domain.JiraAttachmentDownloadEvidence{}, fmt.Errorf("%w: Jira attachment revalidation is incomplete", domain.ErrCheckFailed)
	}
	for _, attachment := range inventory.Attachments {
		if attachment.ID != attachmentID {
			continue
		}
		if strings.TrimSpace(attachment.DownPath) == "" {
			return domain.JiraAttachmentDownloadEvidence{}, fmt.Errorf("%w: Jira attachment download reference is unavailable", domain.ErrCheckFailed)
		}
		return domain.JiraAttachmentDownloadEvidence{ParentID: issueID, Attachment: attachment}, nil
	}
	return domain.JiraAttachmentDownloadEvidence{}, fmt.Errorf("%w: Jira attachment disappeared during revalidation", domain.ErrCheckFailed)
}
