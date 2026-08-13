package agenteval

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
)

const (
	ConfluenceAttachmentDiscoveryViewSchemaVersion = 1

	confluenceAttachmentDiscoveryWireMaxBytes         = 1 << 20
	confluenceAttachmentDiscoveryWireMaxItems         = 10_000
	confluenceAttachmentDiscoveryWireMaxRequests      = 100
	confluenceAttachmentDiscoveryWireMaxResponseBytes = 256 << 20
	confluenceAttachmentDiscoveryWireMaxDeadlineMS    = 10 * 60 * 1000
	confluenceAttachmentDiscoveryWireMaxCursorBytes   = 2048
)

var confluenceReadOpaqueID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// ConfluenceAttachmentDiscoveryView is the evaluator-owned released
// confluence_attachment_search wire. It intentionally contains only bounded
// attachment and parent-container metadata.
type ConfluenceAttachmentDiscoveryView struct {
	SchemaVersion int                                     `json:"schema_version"`
	Qualification string                                  `json:"qualification"`
	Complete      bool                                    `json:"complete"`
	Reason        string                                  `json:"reason,omitempty"`
	Consistency   string                                  `json:"consistency"`
	ScopeSHA256   string                                  `json:"scope_sha256"`
	StartOffset   int                                     `json:"start_offset"`
	NextCursor    string                                  `json:"next_cursor,omitempty"`
	Count         int                                     `json:"count"`
	TotalSize     *int                                    `json:"total_size,omitempty"`
	Bounds        ConfluenceAttachmentDiscoveryViewBounds `json:"bounds"`
	Attachments   []ConfluenceAttachmentDiscoveryMetadata `json:"attachments"`
}

type ConfluenceAttachmentDiscoveryViewBounds struct {
	MaxItems          int   `json:"max_items"`
	MaxRequests       int   `json:"max_requests"`
	MaxResponseBytes  int64 `json:"max_response_bytes"`
	DeadlineMillis    int64 `json:"deadline_ms"`
	RequestsUsed      int   `json:"requests_used"`
	ResponseBytesUsed int64 `json:"response_bytes_used"`
}

type ConfluenceAttachmentDiscoveryMetadata struct {
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

type confluenceAttachmentDiscoveryViewCursor struct {
	SchemaVersion int    `json:"schema_version"`
	ScopeSHA256   string `json:"scope_sha256"`
	Start         int    `json:"start"`
}

// DecodeConfluenceAttachmentDiscoveryView strictly decodes and independently
// reconciles one metadata-only, query-bound discovery prefix.
func DecodeConfluenceAttachmentDiscoveryView(r io.Reader) (ConfluenceAttachmentDiscoveryView, error) {
	limited := &io.LimitedReader{R: r, N: confluenceAttachmentDiscoveryWireMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return ConfluenceAttachmentDiscoveryView{}, fmt.Errorf("read Confluence attachment discovery wire: %w", err)
	}
	if limited.N <= 0 {
		return ConfluenceAttachmentDiscoveryView{}, fmt.Errorf("confluence attachment discovery wire exceeds %d bytes", confluenceAttachmentDiscoveryWireMaxBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return ConfluenceAttachmentDiscoveryView{}, fmt.Errorf("decode Confluence attachment discovery wire: %w", err)
	}
	if err := validateConfluenceAttachmentDiscoveryViewMembers(data); err != nil {
		return ConfluenceAttachmentDiscoveryView{}, fmt.Errorf("decode Confluence attachment discovery wire: %w", err)
	}
	var view ConfluenceAttachmentDiscoveryView
	if err := decodeStrict(bytes.NewReader(data), &view); err != nil {
		return ConfluenceAttachmentDiscoveryView{}, fmt.Errorf("decode Confluence attachment discovery wire: %w", err)
	}
	if err := view.validate(); err != nil {
		return ConfluenceAttachmentDiscoveryView{}, fmt.Errorf("validate Confluence attachment discovery wire: %w", err)
	}
	return view, nil
}

func validateConfluenceAttachmentDiscoveryViewMembers(data []byte) error {
	root, err := confluenceEvidenceObject(data, "attachment discovery")
	if err != nil {
		return err
	}
	required := []string{
		"schema_version", "qualification", "complete", "consistency", "scope_sha256",
		"start_offset", "count", "bounds", "attachments",
	}
	optional := []string{"reason", "next_cursor", "total_size"}
	if err := requireAttachmentDiscoveryMembers(root, "attachment discovery", required, optional); err != nil {
		return err
	}
	var qualification string
	if err := json.Unmarshal(root["qualification"], &qualification); err != nil {
		return fmt.Errorf("attachment discovery.qualification must be a string")
	}
	switch qualification {
	case "complete":
		if _, ok := root["total_size"]; !ok {
			return fmt.Errorf("complete attachment discovery.total_size is required")
		}
		if _, ok := root["reason"]; ok {
			return fmt.Errorf("complete attachment discovery.reason must be omitted")
		}
		if _, ok := root["next_cursor"]; ok {
			return fmt.Errorf("complete attachment discovery.next_cursor must be omitted")
		}
	case "partial":
		if _, ok := root["reason"]; !ok {
			return fmt.Errorf("partial attachment discovery.reason is required")
		}
		if _, ok := root["next_cursor"]; !ok {
			return fmt.Errorf("partial attachment discovery.next_cursor is required")
		}
	case "failed":
		if _, ok := root["reason"]; !ok {
			return fmt.Errorf("failed attachment discovery.reason is required")
		}
		if _, ok := root["next_cursor"]; ok {
			return fmt.Errorf("failed attachment discovery.next_cursor must be omitted")
		}
		if _, ok := root["total_size"]; ok {
			return fmt.Errorf("failed attachment discovery.total_size must be omitted")
		}
	}
	bounds, err := confluenceEvidenceObject(root["bounds"], "attachment discovery.bounds")
	if err != nil {
		return err
	}
	if err := requireAttachmentDiscoveryMembers(bounds, "attachment discovery.bounds", []string{
		"max_items", "max_requests", "max_response_bytes", "deadline_ms", "requests_used", "response_bytes_used",
	}, nil); err != nil {
		return err
	}
	var attachments []json.RawMessage
	if err := json.Unmarshal(root["attachments"], &attachments); err != nil || attachments == nil {
		return fmt.Errorf("attachment discovery.attachments must be a non-null array")
	}
	for index, raw := range attachments {
		attachment, err := confluenceEvidenceObject(raw, fmt.Sprintf("attachment discovery.attachments[%d]", index))
		if err != nil {
			return err
		}
		if err := requireAttachmentDiscoveryMembers(attachment, fmt.Sprintf("attachment discovery.attachments[%d]", index), []string{
			"id", "title", "type", "version", "container_id", "container_type", "container_version",
			"space", "media_type", "file_size",
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func requireAttachmentDiscoveryMembers(object map[string]json.RawMessage, owner string, required, optional []string) error {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, member := range required {
		allowed[member] = true
		raw, ok := object[member]
		if !ok {
			return fmt.Errorf("%s.%s is required", owner, member)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", owner, member)
		}
	}
	for _, member := range optional {
		allowed[member] = true
		if raw, ok := object[member]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", owner, member)
		}
	}
	for member := range object {
		if !allowed[member] {
			return fmt.Errorf("%s contains unknown member %q", owner, member)
		}
	}
	return nil
}

func (v ConfluenceAttachmentDiscoveryView) validate() error {
	if v.SchemaVersion != ConfluenceAttachmentDiscoveryViewSchemaVersion || !validAttachmentDiscoverySHA(v.ScopeSHA256) ||
		v.Consistency != "live_unproven" || v.StartOffset < 0 || v.Count != len(v.Attachments) {
		return fmt.Errorf("root metadata is inconsistent")
	}
	if v.Bounds.MaxItems < 1 || v.Bounds.MaxItems > confluenceAttachmentDiscoveryWireMaxItems ||
		v.Bounds.MaxRequests < 1 || v.Bounds.MaxRequests > confluenceAttachmentDiscoveryWireMaxRequests ||
		v.Bounds.MaxResponseBytes < 1 || v.Bounds.MaxResponseBytes > confluenceAttachmentDiscoveryWireMaxResponseBytes ||
		v.Bounds.DeadlineMillis < 1 || v.Bounds.DeadlineMillis > confluenceAttachmentDiscoveryWireMaxDeadlineMS ||
		v.Bounds.RequestsUsed < 0 || v.Bounds.RequestsUsed > v.Bounds.MaxRequests ||
		v.Bounds.ResponseBytesUsed < 0 || v.Bounds.ResponseBytesUsed > v.Bounds.MaxResponseBytes ||
		v.Count > v.Bounds.MaxItems {
		return fmt.Errorf("bounds are inconsistent")
	}
	if v.StartOffset > math.MaxInt-v.Count {
		return fmt.Errorf("prefix coordinates overflow")
	}
	end := v.StartOffset + v.Count
	if v.TotalSize != nil && (*v.TotalSize < 0 || end > *v.TotalSize || v.Complete && end != *v.TotalSize) {
		return fmt.Errorf("total_size contradicts the prefix")
	}
	switch v.Qualification {
	case "complete":
		if !v.Complete || v.Reason != "" || v.NextCursor != "" || v.TotalSize == nil {
			return fmt.Errorf("complete qualification is contradictory")
		}
	case "partial":
		if v.Complete || !validAttachmentDiscoveryPartialReason(v.Reason) || v.NextCursor == "" {
			return fmt.Errorf("partial qualification is contradictory")
		}
	case "failed":
		if v.Complete || v.Reason != "read_failed" && v.Reason != "validation_failed" || v.NextCursor != "" ||
			v.Count != 0 || len(v.Attachments) != 0 || v.TotalSize != nil {
			return fmt.Errorf("failed qualification is contradictory")
		}
	default:
		return fmt.Errorf("qualification is invalid")
	}
	seen := make(map[string]struct{}, len(v.Attachments))
	for _, attachment := range v.Attachments {
		if !confluenceReadOpaqueID.MatchString(attachment.ID) || strings.TrimSpace(attachment.Title) == "" ||
			attachment.Type != "attachment" || attachment.Version < 1 ||
			!confluenceReadOpaqueID.MatchString(attachment.ContainerID) ||
			attachment.ID == attachment.ContainerID ||
			attachment.ContainerType != "page" && attachment.ContainerType != "blogpost" ||
			attachment.ContainerVersion < 1 || strings.TrimSpace(attachment.Space) == "" ||
			strings.TrimSpace(attachment.MediaType) == "" || attachment.FileSize < 0 {
			return fmt.Errorf("attachment metadata is invalid")
		}
		if _, duplicate := seen[attachment.ID]; duplicate {
			return fmt.Errorf("attachment ids are duplicated")
		}
		seen[attachment.ID] = struct{}{}
	}
	if v.NextCursor != "" {
		next, err := decodeConfluenceAttachmentDiscoveryViewCursor(v.NextCursor)
		if err != nil || next.ScopeSHA256 != v.ScopeSHA256 || next.Start != end {
			return fmt.Errorf("continuation is not bound to this query prefix")
		}
	}
	return nil
}

func validAttachmentDiscoveryPartialReason(reason string) bool {
	switch reason {
	case "item_limit", "request_limit", "response_byte_limit", "deadline", "pagination_stalled", "pagination_unqualified":
		return true
	}
	return false
}

func validAttachmentDiscoverySHA(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decodeConfluenceAttachmentDiscoveryViewCursor(token string) (confluenceAttachmentDiscoveryViewCursor, error) {
	if len(token) > confluenceAttachmentDiscoveryWireMaxCursorBytes {
		return confluenceAttachmentDiscoveryViewCursor{}, fmt.Errorf("cursor is too large")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != token {
		return confluenceAttachmentDiscoveryViewCursor{}, fmt.Errorf("cursor is not canonical base64url")
	}
	if err := validateJSONNoDuplicateKeys(decoded); err != nil {
		return confluenceAttachmentDiscoveryViewCursor{}, err
	}
	object, err := confluenceEvidenceObject(decoded, "attachment discovery cursor")
	if err != nil {
		return confluenceAttachmentDiscoveryViewCursor{}, err
	}
	if err := requireAttachmentDiscoveryMembers(object, "attachment discovery cursor", []string{"schema_version", "scope_sha256", "start"}, nil); err != nil {
		return confluenceAttachmentDiscoveryViewCursor{}, err
	}
	var cursor confluenceAttachmentDiscoveryViewCursor
	if err := decodeStrict(bytes.NewReader(decoded), &cursor); err != nil ||
		cursor.SchemaVersion != ConfluenceAttachmentDiscoveryViewSchemaVersion ||
		!validAttachmentDiscoverySHA(cursor.ScopeSHA256) || cursor.Start < 0 {
		return confluenceAttachmentDiscoveryViewCursor{}, fmt.Errorf("cursor is invalid")
	}
	return cursor, nil
}
