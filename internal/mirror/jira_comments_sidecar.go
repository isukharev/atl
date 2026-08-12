package mirror

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	JiraCommentsSidecarSchemaV1     = 1
	maxJiraCommentsSidecarTextBytes = 64 << 20
	maxJiraCommentsMetadataBytes    = 64 << 10

	JiraCommentsPartialForbidden   = "forbidden"
	JiraCommentsPartialUnsupported = "unsupported"
)

type JiraCommentsSidecarComment struct {
	ID                string `json:"id"`
	AuthorKey         string `json:"author_key,omitempty"`
	AuthorName        string `json:"author_name,omitempty"`
	AuthorDisplayName string `json:"author_display_name,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
	ParentID          string `json:"parent_id,omitempty"`
	Body              string `json:"body"`
}

// JiraCommentsSidecarV1 binds a qualified dedicated-endpoint inventory to the
// exact native and primary-metadata bytes captured for one stable numeric issue
// identity. Mutable issue keys and backend URLs are deliberately absent.
type JiraCommentsSidecarV1 struct {
	SchemaVersion  int                          `json:"schema_version"`
	Service        string                       `json:"service"`
	OriginSHA256   string                       `json:"origin_sha256"`
	ParentID       string                       `json:"parent_id"`
	ParentRevision string                       `json:"parent_revision"`
	NativeSHA256   string                       `json:"native_sha256"`
	MetadataSHA256 string                       `json:"metadata_sha256"`
	Complete       bool                         `json:"complete"`
	PartialReason  string                       `json:"partial_reason,omitempty"`
	Count          int                          `json:"count"`
	Total          int                          `json:"total"`
	TotalKnown     bool                         `json:"total_known"`
	PageCount      int                          `json:"page_count"`
	Comments       []JiraCommentsSidecarComment `json:"comments"`
}

func EncodeJiraCommentsSidecarV1(value JiraCommentsSidecarV1) ([]byte, error) {
	canonical, err := canonicalJiraCommentsSidecar(value)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, jiraCommentsSidecarError("encode failed")
	}
	return append(data, '\n'), nil
}

func DecodeJiraCommentsSidecarV1(data []byte) (JiraCommentsSidecarV1, error) {
	var value JiraCommentsSidecarV1
	if err := decodeCorpusSnapshotJSON(data, &value); err != nil {
		return JiraCommentsSidecarV1{}, jiraCommentsSidecarError("invalid JSON")
	}
	return canonicalJiraCommentsSidecar(value)
}

func canonicalJiraCommentsSidecar(value JiraCommentsSidecarV1) (JiraCommentsSidecarV1, error) {
	if value.SchemaVersion != JiraCommentsSidecarSchemaV1 || value.Service != CorpusSnapshotJira ||
		!validCorpusSnapshotOrigin(value.OriginSHA256) || !positiveDecimalIdentity(value.ParentID) ||
		!validJiraCommentsMetadata(value.ParentRevision, false) || !validCorpusSnapshotDigest(value.NativeSHA256) ||
		!validCorpusSnapshotDigest(value.MetadataSHA256) || value.Count != len(value.Comments) || value.Comments == nil {
		return JiraCommentsSidecarV1{}, jiraCommentsSidecarError("invalid schema or parent binding")
	}
	comments := make([]domain.Comment, 0, len(value.Comments))
	for _, comment := range value.Comments {
		if !validCorpusProviderID(comment.ID) || !validJiraCommentsMetadata(comment.AuthorKey, true) ||
			!validJiraCommentsMetadata(comment.AuthorName, true) || !validJiraCommentsMetadata(comment.AuthorDisplayName, true) ||
			!validJiraCommentsMetadata(comment.CreatedAt, true) || !validJiraCommentsMetadata(comment.UpdatedAt, true) ||
			(comment.ParentID != "" && !validCorpusProviderID(comment.ParentID)) ||
			len(comment.Body) > maxJiraCommentsSidecarTextBytes || !utf8.ValidString(comment.Body) {
			return JiraCommentsSidecarV1{}, jiraCommentsSidecarError("invalid comment record")
		}
		comments = append(comments, domain.Comment{
			ID: comment.ID, Author: comment.AuthorDisplayName, AuthorName: comment.AuthorName,
			AuthorKey: comment.AuthorKey, Created: comment.CreatedAt, Updated: comment.UpdatedAt,
			ParentID: comment.ParentID, Body: comment.Body,
		})
	}
	inventory := domain.JiraCommentInventory{
		Comments: comments, Complete: value.Complete, PartialReason: value.PartialReason,
		Total: value.Total, TotalKnown: value.TotalKnown, PageCount: value.PageCount,
	}
	if value.PartialReason == JiraCommentsPartialForbidden || value.PartialReason == JiraCommentsPartialUnsupported {
		if value.Complete || value.Count != 0 || value.Total != 0 || value.TotalKnown || value.PageCount != 0 {
			return JiraCommentsSidecarV1{}, jiraCommentsSidecarError("invalid unavailable inventory")
		}
	} else if err := domain.ValidateJiraCommentInventory(inventory); err != nil {
		return JiraCommentsSidecarV1{}, jiraCommentsSidecarError("invalid qualified inventory")
	}
	value.Comments = append([]JiraCommentsSidecarComment{}, value.Comments...)
	sort.Slice(value.Comments, func(i, j int) bool { return value.Comments[i].ID < value.Comments[j].ID })
	return value, nil
}

func validJiraCommentsMetadata(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	return strings.TrimSpace(value) != "" && len(value) <= maxJiraCommentsMetadataBytes && utf8.ValidString(value)
}

func jiraCommentsSidecarError(reason string) error {
	return fmt.Errorf("%w: Jira comments sidecar: %s", domain.ErrCheckFailed, reason)
}
