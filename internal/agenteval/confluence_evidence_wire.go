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
	// confluenceEvidenceWireMaxBytes admits the released 1 MiB section body
	// after worst-case six-byte JSON escaping, plus its bounded metadata. It is
	// deliberately separate from the smaller lifecycle-contract file bound.
	confluenceEvidenceWireMaxBytes = 8 << 20

	// ConfluencePageMetadataViewSchemaVersion is the released metadata projection.
	ConfluencePageMetadataViewSchemaVersion = 1
	// ConfluencePageOutlineViewSchemaVersion is the released outline projection.
	ConfluencePageOutlineViewSchemaVersion = 1
	// ConfluencePageSectionViewSchemaVersion is the released single-section projection.
	ConfluencePageSectionViewSchemaVersion = 1
	// ConfluenceAttachmentInventoryViewSchemaVersion is the released attachment projection.
	ConfluenceAttachmentInventoryViewSchemaVersion = 1
)

const (
	ConfluenceRestrictionUnknown      = "unknown"
	ConfluenceRestrictionRestricted   = "restricted"
	ConfluenceRestrictionUnrestricted = "unrestricted"
)

const (
	confluenceOutlinePartialHeadingLimit = "heading_limit"
	confluenceOutlinePartialByteLimit    = "byte_limit"
	confluenceSectionPartialMaxBytes     = "max_bytes"
	confluenceSectionPartialInvalidUTF8  = "invalid_utf8"
	confluenceAttachmentPartialPageLimit = "page_limit"
	confluenceAttachmentPartialItemLimit = "item_limit"
	confluenceAttachmentPartialStalled   = "pagination_stalled"
	confluenceAttachmentPartialLegacy    = "legacy_unqualified"
)

// ConfluencePageMetadataView is the evaluator-owned public MCP metadata wire.
type ConfluencePageMetadataView struct {
	SchemaVersion    int    `json:"schema_version"`
	ID               string `json:"id"`
	Title            string `json:"title"`
	Space            string `json:"space"`
	Version          int    `json:"version"`
	Updated          string `json:"updated,omitempty"`
	RestrictionState string `json:"restriction_state"`
}

// ConfluenceOutlineEntryView is one released outline selection row.
type ConfluenceOutlineEntryView struct {
	Index      int      `json:"index"`
	Level      int      `json:"level"`
	Title      string   `json:"title"`
	Path       []string `json:"path"`
	Occurrence int      `json:"occurrence"`
}

// ConfluencePageOutlineView is the evaluator-owned public MCP outline wire.
type ConfluencePageOutlineView struct {
	SchemaVersion int                          `json:"schema_version"`
	ID            string                       `json:"id"`
	Title         string                       `json:"title"`
	Space         string                       `json:"space"`
	Version       int                          `json:"version"`
	Count         int                          `json:"count"`
	Total         int                          `json:"total"`
	Complete      bool                         `json:"complete"`
	Truncated     bool                         `json:"truncated,omitempty"`
	PartialReason string                       `json:"partial_reason,omitempty"`
	OriginalBytes int                          `json:"original_bytes"`
	EmittedBytes  int                          `json:"emitted_bytes"`
	Headings      []ConfluenceOutlineEntryView `json:"headings"`
}

// ConfluencePageSectionView is the evaluator-owned public MCP single-section wire.
type ConfluencePageSectionView struct {
	SchemaVersion    int      `json:"schema_version"`
	ID               string   `json:"id"`
	PageTitle        string   `json:"page_title"`
	Space            string   `json:"space"`
	Version          int      `json:"version"`
	PageVersionGated bool     `json:"page_version_gated"`
	Heading          string   `json:"heading"`
	Level            int      `json:"level"`
	Path             []string `json:"path"`
	Occurrence       int      `json:"occurrence"`
	Markdown         string   `json:"markdown"`
	Complete         bool     `json:"complete"`
	Truncated        bool     `json:"truncated,omitempty"`
	PartialReason    string   `json:"partial_reason,omitempty"`
	OriginalBytes    int      `json:"original_bytes"`
	EmittedBytes     int      `json:"emitted_bytes"`
}

// ConfluenceAttachmentInventoryView is the evaluator-owned public MCP attachment wire.
type ConfluenceAttachmentInventoryView struct {
	SchemaVersion int                        `json:"schema_version"`
	PageID        string                     `json:"page_id"`
	PageVersion   int                        `json:"page_version"`
	Count         int                        `json:"count"`
	Complete      bool                       `json:"complete"`
	PartialReason string                     `json:"partial_reason,omitempty"`
	Attachments   []ConfluenceAttachmentView `json:"attachments"`
}

// ConfluenceAttachmentView is one metadata-only released attachment row.
type ConfluenceAttachmentView struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	MediaType string `json:"media_type,omitempty"`
	FileSize  int64  `json:"file_size"`
	Version   int    `json:"version"`
}

// DecodeConfluencePageMetadataView strictly decodes and reconciles one MCP view.
func DecodeConfluencePageMetadataView(r io.Reader) (ConfluencePageMetadataView, error) {
	var view ConfluencePageMetadataView
	if err := decodeConfluenceEvidenceWire(r, &view, "page metadata", validateConfluencePageMetadataMembers); err != nil {
		return ConfluencePageMetadataView{}, err
	}
	if err := view.validate(); err != nil {
		return ConfluencePageMetadataView{}, fmt.Errorf("validate Confluence page metadata view: %w", err)
	}
	return view, nil
}

// DecodeConfluencePageOutlineView strictly decodes and reconciles one MCP view.
func DecodeConfluencePageOutlineView(r io.Reader) (ConfluencePageOutlineView, error) {
	var view ConfluencePageOutlineView
	if err := decodeConfluenceEvidenceWire(r, &view, "page outline", validateConfluencePageOutlineMembers); err != nil {
		return ConfluencePageOutlineView{}, err
	}
	if err := view.validate(); err != nil {
		return ConfluencePageOutlineView{}, fmt.Errorf("validate Confluence page outline view: %w", err)
	}
	return view, nil
}

// DecodeConfluencePageSectionView strictly decodes and reconciles one MCP view.
func DecodeConfluencePageSectionView(r io.Reader) (ConfluencePageSectionView, error) {
	var view ConfluencePageSectionView
	if err := decodeConfluenceEvidenceWire(r, &view, "page section", validateConfluencePageSectionMembers); err != nil {
		return ConfluencePageSectionView{}, err
	}
	if err := view.validate(); err != nil {
		return ConfluencePageSectionView{}, fmt.Errorf("validate Confluence page section view: %w", err)
	}
	return view, nil
}

// DecodeConfluenceAttachmentInventoryView strictly decodes and reconciles one MCP view.
func DecodeConfluenceAttachmentInventoryView(r io.Reader) (ConfluenceAttachmentInventoryView, error) {
	var view ConfluenceAttachmentInventoryView
	if err := decodeConfluenceEvidenceWire(r, &view, "attachment inventory", validateConfluenceAttachmentInventoryMembers); err != nil {
		return ConfluenceAttachmentInventoryView{}, err
	}
	if err := view.validate(); err != nil {
		return ConfluenceAttachmentInventoryView{}, fmt.Errorf("validate Confluence attachment inventory view: %w", err)
	}
	return view, nil
}

func decodeConfluenceEvidenceWire(r io.Reader, dst any, subject string, validateMembers func([]byte) error) error {
	limited := &io.LimitedReader{R: r, N: confluenceEvidenceWireMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read Confluence %s wire: %w", subject, err)
	}
	if limited.N <= 0 {
		return fmt.Errorf("confluence %s wire exceeds %d bytes", subject, confluenceEvidenceWireMaxBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return fmt.Errorf("decode Confluence %s wire: %w", subject, err)
	}
	if err := validateMembers(data); err != nil {
		return fmt.Errorf("decode Confluence %s wire: %w", subject, err)
	}
	if err := rejectJSONNulls(data, subject); err != nil {
		return fmt.Errorf("decode Confluence %s wire: %w", subject, err)
	}
	// The exact-member and duplicate-key passes above already supply the strict
	// object semantics. Decode the bounded released wire directly so the
	// lifecycle-file decoder's smaller 1 MiB limit is not applied a second time.
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode Confluence %s wire: %w", subject, err)
	}
	return nil
}

func validateConfluencePageMetadataMembers(data []byte) error {
	root, err := confluenceEvidenceObject(data, "page metadata")
	if err != nil {
		return err
	}
	return requireConfluenceEvidenceMembers(root, "page metadata",
		[]string{"schema_version", "id", "title", "space", "version", "restriction_state"},
		[]string{"updated"})
}

func validateConfluencePageOutlineMembers(data []byte) error {
	root, err := confluenceEvidenceObject(data, "page outline")
	if err != nil {
		return err
	}
	if err := requireConfluenceEvidenceMembers(root, "page outline",
		[]string{"schema_version", "id", "title", "space", "version", "count", "total", "complete", "original_bytes", "emitted_bytes", "headings"},
		[]string{"truncated", "partial_reason"}); err != nil {
		return err
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(root["headings"], &entries); err != nil {
		return fmt.Errorf("page outline headings: %w", err)
	}
	for index, entry := range entries {
		if err := requireExactJSONMembers(entry, fmt.Sprintf("page outline heading[%d]", index),
			[]string{"index", "level", "title", "path", "occurrence"}); err != nil {
			return err
		}
	}
	return nil
}

func validateConfluencePageSectionMembers(data []byte) error {
	root, err := confluenceEvidenceObject(data, "page section")
	if err != nil {
		return err
	}
	return requireConfluenceEvidenceMembers(root, "page section", []string{
		"schema_version", "id", "page_title", "space", "version", "page_version_gated",
		"heading", "level", "path", "occurrence", "markdown", "complete",
		"original_bytes", "emitted_bytes",
	}, []string{"truncated", "partial_reason"})
}

func validateConfluenceAttachmentInventoryMembers(data []byte) error {
	root, err := confluenceEvidenceObject(data, "attachment inventory")
	if err != nil {
		return err
	}
	if err := requireConfluenceEvidenceMembers(root, "attachment inventory",
		[]string{"schema_version", "page_id", "page_version", "count", "complete", "attachments"},
		[]string{"partial_reason"}); err != nil {
		return err
	}
	var attachments []map[string]json.RawMessage
	if err := json.Unmarshal(root["attachments"], &attachments); err != nil {
		return fmt.Errorf("attachment inventory attachments: %w", err)
	}
	for index, attachment := range attachments {
		if err := requireConfluenceEvidenceMembers(attachment, fmt.Sprintf("attachment inventory attachment[%d]", index),
			[]string{"id", "title", "file_size", "version"}, []string{"media_type"}); err != nil {
			return err
		}
	}
	return nil
}

func confluenceEvidenceObject(data []byte, owner string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("%s: %w", owner, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func requireConfluenceEvidenceMembers(document map[string]json.RawMessage, owner string, required, optional []string) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = struct{}{}
		if _, ok := document[name]; !ok {
			return fmt.Errorf("%s is missing required member %q", owner, name)
		}
	}
	for _, name := range optional {
		allowed[name] = struct{}{}
		if raw, ok := document[name]; ok {
			empty, err := confluenceOptionalJSONValueEmpty(raw)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", owner, name, err)
			}
			if empty {
				return fmt.Errorf("%s optional member %q must be omitted when empty", owner, name)
			}
		}
	}
	for name := range document {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func confluenceOptionalJSONValueEmpty(raw json.RawMessage) (bool, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return false, err
	}
	switch typed := value.(type) {
	case bool:
		return !typed, nil
	case string:
		return typed == "", nil
	case json.Number:
		return typed.String() == "0", nil
	case []any:
		return len(typed) == 0, nil
	case map[string]any:
		return len(typed) == 0, nil
	default:
		return false, nil
	}
}

func (v ConfluencePageMetadataView) validate() error {
	if v.SchemaVersion != ConfluencePageMetadataViewSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", v.SchemaVersion)
	}
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Title) == "" ||
		strings.TrimSpace(v.Space) == "" || v.Version < 1 {
		return fmt.Errorf("page metadata provenance is not reconciled")
	}
	switch v.RestrictionState {
	case ConfluenceRestrictionUnknown, ConfluenceRestrictionRestricted, ConfluenceRestrictionUnrestricted:
		return nil
	default:
		return fmt.Errorf("restriction state %q is unsupported", v.RestrictionState)
	}
}

func (v ConfluencePageOutlineView) validate() error {
	if v.SchemaVersion != ConfluencePageOutlineViewSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", v.SchemaVersion)
	}
	if strings.TrimSpace(v.ID) == "" || v.Version < 1 {
		return fmt.Errorf("page outline provenance is not reconciled")
	}
	if v.Headings == nil || v.Count < 0 || v.Total < v.Count || v.Count != len(v.Headings) ||
		v.EmittedBytes < 0 || v.OriginalBytes < v.EmittedBytes {
		return fmt.Errorf("page outline accounting is not reconciled")
	}
	for index, heading := range v.Headings {
		if heading.Index != index+1 || heading.Level < 1 || heading.Level > 6 ||
			strings.TrimSpace(heading.Title) == "" || heading.Path == nil || len(heading.Path) == 0 ||
			heading.Path[len(heading.Path)-1] != heading.Title || heading.Occurrence < 1 {
			return fmt.Errorf("page outline heading[%d] is not reconciled", index)
		}
	}
	if err := validateConfluenceEvidenceCompleteness(v.Complete, v.Truncated, v.PartialReason, validConfluenceOutlinePartialReason); err != nil {
		return fmt.Errorf("page outline completeness: %w", err)
	}
	if v.Complete != (v.Count == v.Total) {
		return fmt.Errorf("page outline completeness contradicts its heading counts")
	}
	return nil
}

func (v ConfluencePageSectionView) validate() error {
	if v.SchemaVersion != ConfluencePageSectionViewSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", v.SchemaVersion)
	}
	if strings.TrimSpace(v.ID) == "" || v.Version < 1 {
		return fmt.Errorf("page section provenance is not reconciled")
	}
	if strings.TrimSpace(v.Heading) == "" || v.Level < 1 || v.Level > 6 ||
		v.Path == nil || len(v.Path) == 0 || v.Path[len(v.Path)-1] != v.Heading || v.Occurrence < 1 {
		return fmt.Errorf("page section selection is not reconciled")
	}
	if err := validateConfluenceEvidenceCompleteness(v.Complete, v.Truncated, v.PartialReason, validConfluenceSectionPartialReason); err != nil {
		return fmt.Errorf("page section completeness: %w", err)
	}
	if !utf8.ValidString(v.Markdown) || v.EmittedBytes != len(v.Markdown) || v.OriginalBytes < v.EmittedBytes ||
		v.Complete != (v.OriginalBytes == v.EmittedBytes) {
		return fmt.Errorf("page section byte accounting is not reconciled")
	}
	return nil
}

func (v ConfluenceAttachmentInventoryView) validate() error {
	if v.SchemaVersion != ConfluenceAttachmentInventoryViewSchemaVersion {
		return fmt.Errorf("unsupported schema_version %d", v.SchemaVersion)
	}
	if strings.TrimSpace(v.PageID) == "" || v.PageVersion < 1 || v.Attachments == nil ||
		v.Count < 0 || v.Count != len(v.Attachments) {
		return fmt.Errorf("attachment inventory accounting is not reconciled")
	}
	if v.Complete != (v.PartialReason == "") || (v.PartialReason != "" && !validConfluenceAttachmentPartialReason(v.PartialReason)) {
		return fmt.Errorf("attachment inventory completeness is not reconciled")
	}
	seen := make(map[string]struct{}, len(v.Attachments))
	for index, attachment := range v.Attachments {
		if strings.TrimSpace(attachment.ID) == "" || attachment.FileSize < 0 || attachment.Version < 0 {
			return fmt.Errorf("attachment inventory attachment[%d] is invalid", index)
		}
		if _, duplicate := seen[attachment.ID]; duplicate {
			return fmt.Errorf("attachment inventory attachment ids are not unique")
		}
		seen[attachment.ID] = struct{}{}
	}
	return nil
}

func validateConfluenceEvidenceCompleteness(complete, truncated bool, reason string, validReason func(string) bool) error {
	if complete {
		if truncated || reason != "" {
			return fmt.Errorf("complete result is marked partial")
		}
		return nil
	}
	if !truncated || !validReason(reason) {
		return fmt.Errorf("partial result has no recognized truncation reason")
	}
	return nil
}

func validConfluenceOutlinePartialReason(reason string) bool {
	return reason == confluenceOutlinePartialHeadingLimit || reason == confluenceOutlinePartialByteLimit
}

func validConfluenceSectionPartialReason(reason string) bool {
	return reason == confluenceSectionPartialMaxBytes || reason == confluenceSectionPartialInvalidUTF8
}

func validConfluenceAttachmentPartialReason(reason string) bool {
	switch reason {
	case confluenceAttachmentPartialPageLimit, confluenceAttachmentPartialItemLimit,
		confluenceAttachmentPartialStalled, confluenceAttachmentPartialLegacy:
		return true
	default:
		return false
	}
}
