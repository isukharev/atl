package mirror

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	AttachmentSidecarSchemaV1      = 1
	maxAttachmentSidecarFieldBytes = 64 << 10
	maxAttachmentSidecarRecords    = 10_000
	// MaxAttachmentSidecarPublicationBytes bounds one encoded attachment
	// sidecar that can join a complete-pull transaction. App-level planners
	// reserve an exact preflighted portion of this finite publisher budget
	// before opening optional attachment bodies.
	MaxAttachmentSidecarPublicationBytes = 16 << 20

	AttachmentInventoryForbidden   = "forbidden"
	AttachmentInventoryUnsupported = "unsupported"
)

type AttachmentBodiesState string

const (
	AttachmentBodiesNotRequested AttachmentBodiesState = "not_requested"
	AttachmentBodiesComplete     AttachmentBodiesState = "complete"
	AttachmentBodiesPartial      AttachmentBodiesState = "partial"
)

type AttachmentBodyState string

const (
	AttachmentBodyNotRequested AttachmentBodyState = "not_requested"
	AttachmentBodyCaptured     AttachmentBodyState = "captured"
	AttachmentBodyExcluded     AttachmentBodyState = "excluded"
	AttachmentBodyForbidden    AttachmentBodyState = "forbidden"
	AttachmentBodyFailed       AttachmentBodyState = "failed"
)

type AttachmentPartialReason string

const (
	AttachmentReasonInventoryPageLimit   AttachmentPartialReason = "inventory_page_limit"
	AttachmentReasonInventoryItemLimit   AttachmentPartialReason = "inventory_item_limit"
	AttachmentReasonInventoryStalled     AttachmentPartialReason = "inventory_pagination_stalled"
	AttachmentReasonInventoryLegacy      AttachmentPartialReason = "inventory_legacy_unqualified"
	AttachmentReasonInventoryField       AttachmentPartialReason = "inventory_field_unavailable"
	AttachmentReasonInventoryForbidden   AttachmentPartialReason = "inventory_forbidden"
	AttachmentReasonInventoryUnsupported AttachmentPartialReason = "inventory_unsupported"
	AttachmentReasonBodyForbidden        AttachmentPartialReason = "body_forbidden"
	AttachmentReasonBodyFailed           AttachmentPartialReason = "body_failed"
	AttachmentReasonBodyCountLimit       AttachmentPartialReason = "body_count_limit"
	AttachmentReasonBodyItemLimit        AttachmentPartialReason = "body_item_limit"
	AttachmentReasonBodyAggregateLimit   AttachmentPartialReason = "body_aggregate_limit"
	AttachmentReasonBodySizeMismatch     AttachmentPartialReason = "body_size_mismatch"
)

type AttachmentBodyReason string

const (
	AttachmentBodyReasonMediaExcluded  AttachmentBodyReason = "media_type_excluded"
	AttachmentBodyReasonCountLimit     AttachmentBodyReason = "count_limit"
	AttachmentBodyReasonItemLimit      AttachmentBodyReason = "item_limit"
	AttachmentBodyReasonAggregateLimit AttachmentBodyReason = "aggregate_limit"
	AttachmentBodyReasonForbidden      AttachmentBodyReason = "forbidden"
	AttachmentBodyReasonFailed         AttachmentBodyReason = "failed"
	AttachmentBodyReasonSizeMismatch   AttachmentBodyReason = "size_mismatch"
)

type AttachmentSidecarBody struct {
	State  AttachmentBodyState  `json:"state"`
	Reason AttachmentBodyReason `json:"reason,omitempty"`
	Path   string               `json:"path,omitempty"`
	Size   int64                `json:"size,omitempty"`
	SHA256 string               `json:"sha256,omitempty"`
}

type AttachmentSidecarAuthor struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type AttachmentSidecarRecord struct {
	ID           string                  `json:"id"`
	Version      int                     `json:"version"`
	Filename     string                  `json:"filename"`
	MediaType    string                  `json:"media_type,omitempty"`
	DeclaredSize int64                   `json:"declared_size"`
	CreatedAt    string                  `json:"created_at,omitempty"`
	Author       AttachmentSidecarAuthor `json:"author"`
	Body         AttachmentSidecarBody   `json:"body"`
}

// AttachmentSidecarV1 is shared by Jira and Confluence. Exact parent hashes
// bind metadata to the pristine snapshot; optional native bodies live in a
// distinct .attachments tree and are referenced only by contained path/hash.
type AttachmentSidecarV1 struct {
	SchemaVersion          int                       `json:"schema_version"`
	Service                string                    `json:"service"`
	OriginSHA256           string                    `json:"origin_sha256"`
	ParentID               string                    `json:"parent_id"`
	ParentVersion          int                       `json:"parent_version"`
	ParentRevision         string                    `json:"parent_revision,omitempty"`
	NativeSHA256           string                    `json:"native_sha256"`
	MetadataSHA256         string                    `json:"metadata_sha256"`
	InventoryComplete      bool                      `json:"inventory_complete"`
	InventoryPartialReason string                    `json:"inventory_partial_reason,omitempty"`
	BodiesState            AttachmentBodiesState     `json:"bodies_state"`
	Complete               bool                      `json:"complete"`
	Count                  int                       `json:"count"`
	PartialReasons         []AttachmentPartialReason `json:"partial_reasons"`
	Attachments            []AttachmentSidecarRecord `json:"attachments"`
}

func EncodeAttachmentSidecarV1(value AttachmentSidecarV1) ([]byte, error) {
	if !attachmentSidecarMarshalBounded(value) {
		return nil, attachmentSidecarError("encoded sidecar exceeds its publication bound")
	}
	canonical, err := canonicalAttachmentSidecar(value)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, attachmentSidecarError("encode failed")
	}
	data = append(data, '\n')
	if err := ValidateAttachmentSidecarPublicationData(data, 0); err != nil {
		return nil, err
	}
	return data, nil
}

// ValidateAttachmentSidecarPublicationData bounds the canonical sidecar bytes
// that can join one complete-pull publication. reserve lets callers reject an
// otherwise valid provisional sidecar before a later partial-status suffix can
// make the final sidecar unpublishable.
func ValidateAttachmentSidecarPublicationData(data []byte, reserve int) error {
	if reserve < 0 || reserve > MaxAttachmentSidecarPublicationBytes || len(data) > MaxAttachmentSidecarPublicationBytes-reserve {
		return attachmentSidecarError("encoded sidecar exceeds its publication bound")
	}
	return nil
}

// attachmentSidecarMarshalBounded rejects a source value before json.Marshal
// can allocate an unbounded escaped representation. JSON may encode one input
// byte as up to six bytes, so this is deliberately a conservative upper bound;
// exact canonical bytes are checked again after encoding.
func attachmentSidecarMarshalBounded(value AttachmentSidecarV1) bool {
	if len(value.Attachments) > maxAttachmentSidecarRecords || len(value.PartialReasons) > len(value.Attachments)+16 {
		return false
	}
	total := 2048 // fixed object keys, punctuation, indentation, and numerics.
	addString := func(text string) bool {
		if len(text) > (MaxAttachmentSidecarPublicationBytes-total-2)/6 {
			return false
		}
		total += 2 + 6*len(text)
		return true
	}
	for _, text := range []string{
		value.Service, value.OriginSHA256, value.ParentID, value.ParentRevision,
		value.NativeSHA256, value.MetadataSHA256, value.InventoryPartialReason,
		string(value.BodiesState),
	} {
		if !addString(text) {
			return false
		}
	}
	for _, reason := range value.PartialReasons {
		if !addString(string(reason)) {
			return false
		}
	}
	for _, attachment := range value.Attachments {
		// Reserve substantially more than the fixed per-record JSON spelling so
		// the escaped-string bound remains simple and fail-closed.
		if total > MaxAttachmentSidecarPublicationBytes-1024 {
			return false
		}
		total += 1024
		for _, text := range []string{
			attachment.ID, attachment.Filename, attachment.MediaType, attachment.CreatedAt,
			attachment.Author.ID, attachment.Author.Name, attachment.Author.DisplayName,
			string(attachment.Body.State), string(attachment.Body.Reason), attachment.Body.Path,
			attachment.Body.SHA256,
		} {
			if !addString(text) {
				return false
			}
		}
	}
	return true
}

func DecodeAttachmentSidecarV1(data []byte) (AttachmentSidecarV1, error) {
	var value AttachmentSidecarV1
	if err := decodeCorpusSnapshotJSON(data, &value); err != nil {
		return AttachmentSidecarV1{}, attachmentSidecarError("invalid JSON")
	}
	return canonicalAttachmentSidecar(value)
}

func canonicalAttachmentSidecar(value AttachmentSidecarV1) (AttachmentSidecarV1, error) {
	if value.SchemaVersion != AttachmentSidecarSchemaV1 ||
		(value.Service != CorpusSnapshotJira && value.Service != CorpusSnapshotConfluence) {
		return AttachmentSidecarV1{}, attachmentSidecarError("invalid schema")
	}
	if !validCorpusSnapshotOrigin(value.OriginSHA256) || !validCorpusProviderID(value.ParentID) || !validCorpusSnapshotDigest(value.NativeSHA256) ||
		!validCorpusSnapshotDigest(value.MetadataSHA256) {
		return AttachmentSidecarV1{}, attachmentSidecarError("invalid parent binding")
	}
	// Keep decoder admission aligned with the writer before copying, mapping, or
	// sorting either collection. Complete-pull recovery reads sidecars from the
	// local private tree, but their durable policy is still limited to one
	// bounded inventory rather than a larger hand-authored legacy payload.
	if len(value.Attachments) > maxAttachmentSidecarRecords || len(value.PartialReasons) > maxAttachmentSidecarRecords+16 {
		return AttachmentSidecarV1{}, attachmentSidecarError("attachment collection exceeds the supported bound")
	}
	if value.Count != len(value.Attachments) || value.PartialReasons == nil || value.Attachments == nil {
		return AttachmentSidecarV1{}, attachmentSidecarError("invalid collection")
	}
	if value.Service == CorpusSnapshotConfluence && (value.ParentVersion <= 0 || value.ParentRevision != "") ||
		value.Service == CorpusSnapshotJira && (value.ParentVersion != 0 || !validAttachmentSidecarField(value.ParentRevision, false)) {
		return AttachmentSidecarV1{}, attachmentSidecarError("invalid parent version")
	}
	if !validAttachmentInventoryReason(value.Service, value.InventoryComplete, value.InventoryPartialReason) {
		return AttachmentSidecarV1{}, attachmentSidecarError("invalid inventory qualification")
	}
	wantComplete := value.InventoryComplete && (value.BodiesState == AttachmentBodiesNotRequested || value.BodiesState == AttachmentBodiesComplete)
	if value.Complete != wantComplete || !validAttachmentBodiesState(value.BodiesState) {
		return AttachmentSidecarV1{}, attachmentSidecarError("invalid body qualification")
	}
	value.PartialReasons = append([]AttachmentPartialReason{}, value.PartialReasons...)
	sort.Slice(value.PartialReasons, func(i, j int) bool { return value.PartialReasons[i] < value.PartialReasons[j] })
	for index, reason := range value.PartialReasons {
		if !validAttachmentPartialReason(reason) || index > 0 && value.PartialReasons[index-1] == reason {
			return AttachmentSidecarV1{}, attachmentSidecarError("invalid partial reason")
		}
	}
	expectedReasons := map[AttachmentPartialReason]struct{}{}
	if !value.InventoryComplete {
		expectedReasons[attachmentInventoryPartialReason(value.Service, value.InventoryPartialReason)] = struct{}{}
	}
	seen := make(map[string]struct{}, len(value.Attachments))
	seenPaths := make(map[string]struct{}, len(value.Attachments))
	value.Attachments = append([]AttachmentSidecarRecord{}, value.Attachments...)
	for _, attachment := range value.Attachments {
		if !validCorpusProviderID(attachment.ID) || !validAttachmentSidecarField(attachment.Filename, false) ||
			!validAttachmentSidecarField(attachment.MediaType, true) || !validAttachmentSidecarField(attachment.CreatedAt, true) ||
			!validAttachmentSidecarField(attachment.Author.ID, true) || !validAttachmentSidecarField(attachment.Author.Name, true) ||
			!validAttachmentSidecarField(attachment.Author.DisplayName, true) || attachment.DeclaredSize < 0 ||
			value.Service == CorpusSnapshotConfluence && attachment.Version <= 0 ||
			value.Service == CorpusSnapshotJira && attachment.Version != 0 {
			return AttachmentSidecarV1{}, attachmentSidecarError("invalid attachment metadata")
		}
		if _, duplicate := seen[attachment.ID]; duplicate {
			return AttachmentSidecarV1{}, attachmentSidecarError("duplicate attachment identity")
		}
		seen[attachment.ID] = struct{}{}
		bodyReason, path, ok := validateAttachmentBody(value.BodiesState, attachment)
		if !ok {
			return AttachmentSidecarV1{}, attachmentSidecarError("invalid attachment body evidence")
		}
		if bodyReason != "" {
			expectedReasons[bodyReason] = struct{}{}
		}
		if path != "" {
			if _, duplicate := seenPaths[path]; duplicate {
				return AttachmentSidecarV1{}, attachmentSidecarError("duplicate attachment body path")
			}
			seenPaths[path] = struct{}{}
		}
	}
	if value.BodiesState == AttachmentBodiesPartial && value.InventoryComplete && len(expectedReasons) == 0 {
		return AttachmentSidecarV1{}, attachmentSidecarError("partial body selection has no reason")
	}
	if len(value.PartialReasons) != len(expectedReasons) {
		return AttachmentSidecarV1{}, attachmentSidecarError("partial reasons do not match evidence")
	}
	for _, reason := range value.PartialReasons {
		if _, present := expectedReasons[reason]; !present {
			return AttachmentSidecarV1{}, attachmentSidecarError("partial reasons do not match evidence")
		}
	}
	sort.Slice(value.Attachments, func(i, j int) bool { return value.Attachments[i].ID < value.Attachments[j].ID })
	return value, nil
}

func validAttachmentInventoryReason(service string, complete bool, reason string) bool {
	if complete {
		return reason == ""
	}
	if reason == AttachmentInventoryForbidden || reason == AttachmentInventoryUnsupported {
		return true
	}
	if service == CorpusSnapshotJira {
		return reason == domain.JiraAttachmentPartialFieldUnavailable
	}
	return domain.ValidAttachmentPartialReason(reason)
}

func validAttachmentBodiesState(state AttachmentBodiesState) bool {
	return state == AttachmentBodiesNotRequested || state == AttachmentBodiesComplete || state == AttachmentBodiesPartial
}

func validAttachmentPartialReason(reason AttachmentPartialReason) bool {
	switch reason {
	case AttachmentReasonInventoryPageLimit, AttachmentReasonInventoryItemLimit, AttachmentReasonInventoryStalled,
		AttachmentReasonInventoryLegacy, AttachmentReasonInventoryField, AttachmentReasonInventoryForbidden,
		AttachmentReasonInventoryUnsupported, AttachmentReasonBodyForbidden, AttachmentReasonBodyFailed,
		AttachmentReasonBodyCountLimit, AttachmentReasonBodyItemLimit, AttachmentReasonBodyAggregateLimit,
		AttachmentReasonBodySizeMismatch:
		return true
	}
	return false
}

func validateAttachmentBody(state AttachmentBodiesState, attachment AttachmentSidecarRecord) (AttachmentPartialReason, string, bool) {
	body := attachment.Body
	if state == AttachmentBodiesNotRequested {
		return "", "", body.State == AttachmentBodyNotRequested && body.Reason == "" && body.Path == "" && body.Size == 0 && body.SHA256 == ""
	}
	if body.State == AttachmentBodyCaptured {
		path, err := NewPublicArtifactPath(body.Path)
		valid := err == nil && body.Reason == "" && strings.HasSuffix(filepath.ToSlash(filepath.Dir(path.String())), ".attachments") &&
			filepath.ToSlash(filepath.Base(path.String())) == attachment.ID+".body" &&
			body.Size == attachment.DeclaredSize && validCorpusSnapshotDigest(body.SHA256)
		return "", path.String(), valid
	}
	if body.Path != "" || body.Size != 0 || body.SHA256 != "" {
		return "", "", false
	}
	if body.State == AttachmentBodyExcluded {
		switch body.Reason {
		case AttachmentBodyReasonMediaExcluded:
			return "", "", true
		case AttachmentBodyReasonCountLimit:
			return AttachmentReasonBodyCountLimit, "", state == AttachmentBodiesPartial
		case AttachmentBodyReasonItemLimit:
			return AttachmentReasonBodyItemLimit, "", state == AttachmentBodiesPartial
		case AttachmentBodyReasonAggregateLimit:
			return AttachmentReasonBodyAggregateLimit, "", state == AttachmentBodiesPartial
		default:
			return "", "", false
		}
	}
	if state != AttachmentBodiesPartial {
		return "", "", false
	}
	if body.State == AttachmentBodyForbidden && body.Reason == AttachmentBodyReasonForbidden {
		return AttachmentReasonBodyForbidden, "", true
	}
	if body.State == AttachmentBodyFailed {
		switch body.Reason {
		case AttachmentBodyReasonFailed:
			return AttachmentReasonBodyFailed, "", true
		case AttachmentBodyReasonSizeMismatch:
			return AttachmentReasonBodySizeMismatch, "", true
		}
	}
	return "", "", false
}

func attachmentInventoryPartialReason(service, reason string) AttachmentPartialReason {
	switch reason {
	case domain.AttachmentPartialPageLimit:
		return AttachmentReasonInventoryPageLimit
	case domain.AttachmentPartialItemLimit:
		return AttachmentReasonInventoryItemLimit
	case domain.AttachmentPartialPaginationStalled:
		return AttachmentReasonInventoryStalled
	case domain.AttachmentPartialLegacyUnqualified:
		return AttachmentReasonInventoryLegacy
	case domain.JiraAttachmentPartialFieldUnavailable:
		return AttachmentReasonInventoryField
	case AttachmentInventoryForbidden:
		return AttachmentReasonInventoryForbidden
	case AttachmentInventoryUnsupported:
		return AttachmentReasonInventoryUnsupported
	default:
		_ = service
		return ""
	}
}

func validAttachmentSidecarField(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	return strings.TrimSpace(value) != "" && len(value) <= maxAttachmentSidecarFieldBytes && utf8.ValidString(value)
}

func attachmentSidecarError(reason string) error {
	return fmt.Errorf("%w: attachment sidecar: %s", domain.ErrCheckFailed, reason)
}
