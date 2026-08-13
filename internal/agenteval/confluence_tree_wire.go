package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	ConfluenceSpaceTreeViewSchemaVersion = 1
	confluenceSpaceTreeWireMaxBytes      = 4 << 20
	confluenceSpaceTreeMaxItems          = 2_000
	confluenceSpaceTreeMaxScannedItems   = 20_000
	confluenceSpaceTreeMaxRequests       = 100
	confluenceSpaceTreeMaxResponseBytes  = 256 << 20
	confluenceSpaceTreeMaxDeadlineMS     = 10 * 60 * 1000
)

// ConfluenceSpaceTreeView is the evaluator-owned released `conf space tree`
// JSON wire. It preserves the live, caller-bounded qualification envelope.
type ConfluenceSpaceTreeView struct {
	SchemaVersion int                           `json:"schema_version"`
	Space         string                        `json:"space"`
	Depth         int                           `json:"depth"`
	Count         int                           `json:"count"`
	Complete      bool                          `json:"complete"`
	Truncated     bool                          `json:"truncated,omitempty"`
	PartialReason string                        `json:"partial_reason,omitempty"`
	Consistency   string                        `json:"consistency"`
	Bounds        ConfluenceSpaceTreeViewBounds `json:"bounds"`
	Pages         []ConfluenceSpaceTreePage     `json:"pages"`
}

type ConfluenceSpaceTreeViewBounds struct {
	MaxItems          int   `json:"max_items"`
	MaxScannedItems   int   `json:"max_scanned_items"`
	MaxRequests       int   `json:"max_requests"`
	MaxResponseBytes  int64 `json:"max_response_bytes"`
	DeadlineMillis    int64 `json:"deadline_ms"`
	ScannedItems      int   `json:"scanned_items"`
	RequestsUsed      int   `json:"requests_used"`
	ResponseBytesUsed int64 `json:"response_bytes_used"`
}

type ConfluenceSpaceTreePage struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Space   string `json:"space"`
	Version int    `json:"version"`
	Parent  string `json:"parent,omitempty"`
}

func DecodeConfluenceSpaceTreeView(r io.Reader) (ConfluenceSpaceTreeView, error) {
	limited := &io.LimitedReader{R: r, N: confluenceSpaceTreeWireMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return ConfluenceSpaceTreeView{}, fmt.Errorf("read Confluence space tree wire: %w", err)
	}
	if limited.N <= 0 {
		return ConfluenceSpaceTreeView{}, fmt.Errorf("confluence space tree wire exceeds %d bytes", confluenceSpaceTreeWireMaxBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return ConfluenceSpaceTreeView{}, fmt.Errorf("decode Confluence space tree wire: %w", err)
	}
	if err := validateConfluenceSpaceTreeMembers(data); err != nil {
		return ConfluenceSpaceTreeView{}, fmt.Errorf("decode Confluence space tree wire: %w", err)
	}
	var view ConfluenceSpaceTreeView
	if err := decodeStrict(bytes.NewReader(data), &view); err != nil {
		return ConfluenceSpaceTreeView{}, fmt.Errorf("decode Confluence space tree wire: %w", err)
	}
	if err := view.validate(); err != nil {
		return ConfluenceSpaceTreeView{}, fmt.Errorf("validate Confluence space tree wire: %w", err)
	}
	return view, nil
}

func validateConfluenceSpaceTreeMembers(data []byte) error {
	root, err := confluenceEvidenceObject(data, "space tree")
	if err != nil {
		return err
	}
	if err := requireAttachmentDiscoveryMembers(root, "space tree", []string{
		"schema_version", "space", "depth", "count", "complete", "consistency", "bounds", "pages",
	}, []string{"truncated", "partial_reason"}); err != nil {
		return err
	}
	var complete bool
	if err := json.Unmarshal(root["complete"], &complete); err != nil {
		return fmt.Errorf("space tree.complete must be a boolean")
	}
	if complete {
		if _, ok := root["truncated"]; ok {
			return fmt.Errorf("complete space tree.truncated must be omitted")
		}
		if _, ok := root["partial_reason"]; ok {
			return fmt.Errorf("complete space tree.partial_reason must be omitted")
		}
	} else {
		if _, ok := root["truncated"]; !ok {
			return fmt.Errorf("partial space tree.truncated is required")
		}
		if _, ok := root["partial_reason"]; !ok {
			return fmt.Errorf("partial space tree.partial_reason is required")
		}
	}
	bounds, err := confluenceEvidenceObject(root["bounds"], "space tree.bounds")
	if err != nil {
		return err
	}
	if err := requireAttachmentDiscoveryMembers(bounds, "space tree.bounds", []string{
		"max_items", "max_scanned_items", "max_requests", "max_response_bytes", "deadline_ms",
		"scanned_items", "requests_used", "response_bytes_used",
	}, nil); err != nil {
		return err
	}
	var pages []json.RawMessage
	if err := json.Unmarshal(root["pages"], &pages); err != nil || pages == nil {
		return fmt.Errorf("space tree.pages must be a non-null array")
	}
	for index, raw := range pages {
		page, err := confluenceEvidenceObject(raw, fmt.Sprintf("space tree.pages[%d]", index))
		if err != nil {
			return err
		}
		if err := requireAttachmentDiscoveryMembers(page, fmt.Sprintf("space tree.pages[%d]", index),
			[]string{"id", "title", "space", "version"}, []string{"parent"}); err != nil {
			return err
		}
		if rawParent, ok := page["parent"]; ok {
			var parent string
			if err := json.Unmarshal(rawParent, &parent); err != nil || parent == "" {
				return fmt.Errorf("space tree.pages[%d].parent must be a non-empty string when present", index)
			}
		}
	}
	return nil
}

func (v ConfluenceSpaceTreeView) validate() error {
	if v.SchemaVersion != ConfluenceSpaceTreeViewSchemaVersion || !utf8.ValidString(v.Space) || strings.TrimSpace(v.Space) == "" ||
		len(v.Space) > 255 ||
		v.Depth < 0 || v.Count != len(v.Pages) || v.Consistency != "live_unproven" {
		return fmt.Errorf("root metadata is inconsistent")
	}
	if v.Bounds.MaxItems < 1 || v.Bounds.MaxItems > confluenceSpaceTreeMaxItems ||
		v.Bounds.MaxScannedItems < 1 || v.Bounds.MaxScannedItems > confluenceSpaceTreeMaxScannedItems ||
		v.Bounds.MaxRequests < 1 || v.Bounds.MaxRequests > confluenceSpaceTreeMaxRequests ||
		v.Bounds.MaxResponseBytes < 1 || v.Bounds.MaxResponseBytes > confluenceSpaceTreeMaxResponseBytes ||
		v.Bounds.DeadlineMillis < 1 || v.Bounds.DeadlineMillis > confluenceSpaceTreeMaxDeadlineMS ||
		v.Bounds.ScannedItems < v.Count || v.Bounds.ScannedItems > v.Bounds.MaxScannedItems ||
		v.Bounds.RequestsUsed < 0 || v.Bounds.RequestsUsed > v.Bounds.MaxRequests ||
		v.Bounds.ResponseBytesUsed < 0 || v.Bounds.ResponseBytesUsed > v.Bounds.MaxResponseBytes ||
		v.Count > v.Bounds.MaxItems {
		return fmt.Errorf("bounds are inconsistent")
	}
	if v.Complete {
		if v.Truncated || v.PartialReason != "" {
			return fmt.Errorf("complete qualification is contradictory")
		}
	} else if !v.Truncated || !validConfluenceSpaceTreePartialReason(v.PartialReason) {
		return fmt.Errorf("partial qualification is contradictory")
	}
	seen := make(map[string]struct{}, len(v.Pages))
	for _, page := range v.Pages {
		if !confluenceReadOpaqueID.MatchString(page.ID) || strings.TrimSpace(page.Title) == "" ||
			page.Space != v.Space || page.Version < 1 || page.Parent == page.ID ||
			page.Parent != "" && !confluenceReadOpaqueID.MatchString(page.Parent) {
			return fmt.Errorf("page metadata is invalid")
		}
		if _, duplicate := seen[page.ID]; duplicate {
			return fmt.Errorf("page ids are duplicated")
		}
		seen[page.ID] = struct{}{}
	}
	return nil
}

func validConfluenceSpaceTreePartialReason(reason string) bool {
	switch reason {
	case "item_limit", "scan_limit", "request_limit", "response_byte_limit", "deadline", "pagination_stalled", "pagination_unqualified", "legacy_unqualified":
		return true
	}
	return false
}
