package mirror

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const ConfluenceCommentsSidecarSchemaVersion = 2

// ConfluenceCommentsSidecarFormat identifies which durable comment sidecar
// representation was decoded. Legacy is the pre-versioned flat Comment array.
type ConfluenceCommentsSidecarFormat string

const (
	ConfluenceCommentsSidecarFormatV2     ConfluenceCommentsSidecarFormat = "v2"
	ConfluenceCommentsSidecarFormatLegacy ConfluenceCommentsSidecarFormat = "legacy"
)

type ConfluenceCommentsSidecarAuthor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ConfluenceCommentsSidecarAnchor preserves the app-level reconciliation of
// an inline comment against the exact CSF page version named by the sidecar.
type ConfluenceCommentsSidecarAnchor struct {
	MarkerRef         string                        `json:"marker_ref"`
	OriginalSelection string                        `json:"original_selection"`
	ObservedSelection string                        `json:"observed_selection"`
	Status            domain.ConfluenceAnchorStatus `json:"status"`
}

type ConfluenceCommentsSidecarComment struct {
	ID          string                             `json:"id"`
	PageID      string                             `json:"page_id"`
	ParentID    *string                            `json:"parent_id"`
	RootID      *string                            `json:"root_id"`
	Relation    domain.ConfluenceCommentRelation   `json:"relation"`
	Location    domain.ConfluenceCommentLocation   `json:"location"`
	Resolution  domain.ConfluenceCommentResolution `json:"resolution"`
	Version     int                                `json:"version"`
	Author      ConfluenceCommentsSidecarAuthor    `json:"author"`
	CreatedAt   string                             `json:"created_at"`
	UpdatedAt   string                             `json:"updated_at"`
	Body        string                             `json:"body"`
	BodyStorage string                             `json:"body_storage"`
	Anchor      *ConfluenceCommentsSidecarAnchor   `json:"anchor"`
}

// ConfluenceCommentsSidecarDiagnostic contains only closed diagnostic codes,
// stable identities, selectors, and locations. Backend messages are not part
// of the durable format.
type ConfluenceCommentsSidecarDiagnostic struct {
	Code      string                           `json:"code"`
	CommentID string                           `json:"comment_id,omitempty"`
	MarkerRef string                           `json:"marker_ref,omitempty"`
	Selector  domain.ConfluenceCommentSelector `json:"selector,omitempty"`
	Location  domain.ConfluenceCommentLocation `json:"location,omitempty"`
}

// ConfluenceCommentsSidecarV2 is the owned, qualified comments sidecar. Count,
// RootCount, and Complete are stored contract assertions rather than hints and
// must agree with the records and the three completeness dimensions.
type ConfluenceCommentsSidecarV2 struct {
	SchemaVersion    int                                   `json:"schema_version"`
	PageID           string                                `json:"page_id"`
	PageVersion      int                                   `json:"page_version"`
	Complete         bool                                  `json:"complete"`
	CommentsComplete bool                                  `json:"comments_complete"`
	ThreadsComplete  bool                                  `json:"threads_complete"`
	AnchorsComplete  bool                                  `json:"anchors_complete"`
	Count            int                                   `json:"count"`
	RootCount        int                                   `json:"root_count"`
	PartialReasons   []string                              `json:"partial_reasons"`
	Capabilities     domain.ConfluenceCommentCapabilities  `json:"capabilities"`
	Comments         []ConfluenceCommentsSidecarComment    `json:"comments"`
	Diagnostics      []ConfluenceCommentsSidecarDiagnostic `json:"diagnostics"`
}

// DecodedConfluenceCommentsSidecar makes legacy handling explicit. Format says
// which representation is authoritative; Legacy is always a non-nil slice,
// including for an empty legacy document and for v2.
type DecodedConfluenceCommentsSidecar struct {
	Format ConfluenceCommentsSidecarFormat
	V2     *ConfluenceCommentsSidecarV2
	Legacy []domain.Comment
}

// EncodeConfluenceCommentsSidecarV2 validates and deterministically encodes a
// schema-v2 sidecar. It does not mutate the caller's slices or pointer fields.
func EncodeConfluenceCommentsSidecarV2(value ConfluenceCommentsSidecarV2) ([]byte, error) {
	if err := validateConfluenceCommentsSidecarV2(value); err != nil {
		return nil, err
	}
	canonical := canonicalConfluenceCommentsSidecarV2(value)
	b, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: encode Confluence comments sidecar: %v", domain.ErrCheckFailed, err)
	}
	return append(b, '\n'), nil
}

// DecodeConfluenceCommentsSidecar strictly decodes either schema v2 or the
// historical flat []domain.Comment representation. Unknown keys and trailing
// JSON values are rejected in both formats.
func DecodeConfluenceCommentsSidecar(data []byte) (DecodedConfluenceCommentsSidecar, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return DecodedConfluenceCommentsSidecar{}, confluenceCommentsSidecarError("empty document")
	}
	switch trimmed[0] {
	case '[':
		var comments []domain.Comment
		if err := decodeConfluenceCommentsSidecarJSON(trimmed, &comments); err != nil {
			return DecodedConfluenceCommentsSidecar{}, err
		}
		if comments == nil {
			return DecodedConfluenceCommentsSidecar{}, confluenceCommentsSidecarError("legacy comment array is null")
		}
		if err := validateLegacyConfluenceComments(comments); err != nil {
			return DecodedConfluenceCommentsSidecar{}, err
		}
		return DecodedConfluenceCommentsSidecar{
			Format: ConfluenceCommentsSidecarFormatLegacy,
			Legacy: comments,
		}, nil
	case '{':
		var value ConfluenceCommentsSidecarV2
		if err := decodeConfluenceCommentsSidecarJSON(trimmed, &value); err != nil {
			return DecodedConfluenceCommentsSidecar{}, err
		}
		if err := validateConfluenceCommentsSidecarV2(value); err != nil {
			return DecodedConfluenceCommentsSidecar{}, err
		}
		canonical := canonicalConfluenceCommentsSidecarV2(value)
		return DecodedConfluenceCommentsSidecar{
			Format: ConfluenceCommentsSidecarFormatV2,
			V2:     &canonical,
			Legacy: []domain.Comment{},
		}, nil
	default:
		return DecodedConfluenceCommentsSidecar{}, confluenceCommentsSidecarError("top-level value is not an object or array")
	}
}

func decodeConfluenceCommentsSidecarJSON(data []byte, destination any) error {
	if err := rejectDuplicateConfluenceCommentsSidecarKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return confluenceCommentsSidecarError("invalid JSON: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return confluenceCommentsSidecarError("trailing JSON value")
		}
		return confluenceCommentsSidecarError("trailing JSON data: %v", err)
	}
	return nil
}

func rejectDuplicateConfluenceCommentsSidecarKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected delimiter %q", delim)
		}
	}
	if err := walk(); err != nil {
		return confluenceCommentsSidecarError("invalid JSON: %v", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return confluenceCommentsSidecarError("trailing JSON value")
		}
		return confluenceCommentsSidecarError("trailing JSON data: %v", err)
	}
	return nil
}

func validateConfluenceCommentsSidecarV2(value ConfluenceCommentsSidecarV2) error {
	if value.SchemaVersion != ConfluenceCommentsSidecarSchemaVersion {
		return confluenceCommentsSidecarError("unsupported schema version %d", value.SchemaVersion)
	}
	if strings.TrimSpace(value.PageID) == "" || value.PageVersion <= 0 {
		return confluenceCommentsSidecarError("invalid page identity or version")
	}
	if value.Comments == nil || value.PartialReasons == nil || value.Diagnostics == nil {
		return confluenceCommentsSidecarError("required array is null")
	}
	complete := value.CommentsComplete && value.ThreadsComplete && value.AnchorsComplete
	if value.Complete != complete {
		return confluenceCommentsSidecarError("overall completeness is inconsistent")
	}
	if value.Complete && len(value.PartialReasons) != 0 {
		return confluenceCommentsSidecarError("complete inventory contains partial reasons")
	}
	if value.Count != len(value.Comments) {
		return confluenceCommentsSidecarError("comment count is inconsistent")
	}
	rootCount := 0
	comments := make([]domain.ConfluenceCommentRecord, 0, len(value.Comments))
	for _, comment := range value.Comments {
		if comment.PageID != value.PageID {
			return confluenceCommentsSidecarError("comment page identity is inconsistent")
		}
		if comment.Relation == domain.ConfluenceCommentRelationRoot {
			rootCount++
		}
		// Schema-v2 historically allowed reply-level anchor copies. Keep accepting
		// and preserving those legacy bytes so pristine v5 views reconstruct
		// identically, while current app validators prevent new projections from
		// emitting them. A proven reply with a nil anchor is the current shape.
		if comment.Relation != domain.ConfluenceCommentRelationReply &&
			comment.Location == domain.ConfluenceCommentLocationInline && comment.Anchor == nil {
			return confluenceCommentsSidecarError("inline comment has no anchor projection")
		}
		if comment.Anchor != nil {
			if !domain.ValidConfluenceAnchorStatus(comment.Anchor.Status) {
				return confluenceCommentsSidecarError("comment anchor status is invalid")
			}
			if comment.Anchor.Status != domain.ConfluenceAnchorUnavailable && strings.TrimSpace(comment.Anchor.MarkerRef) == "" {
				return confluenceCommentsSidecarError("qualified comment anchor has no marker reference")
			}
			if value.AnchorsComplete && comment.Anchor.Status != domain.ConfluenceAnchorMatched {
				return confluenceCommentsSidecarError("complete anchors contain an unmatched projection")
			}
		}
		record := domain.ConfluenceCommentRecord{
			ID: comment.ID, PageID: comment.PageID,
			ParentID: cloneConfluenceCommentsSidecarString(comment.ParentID),
			RootID:   cloneConfluenceCommentsSidecarString(comment.RootID),
			Relation: comment.Relation, Location: comment.Location, Resolution: comment.Resolution,
			Version: comment.Version, AuthorID: comment.Author.ID, AuthorDisplayName: comment.Author.DisplayName,
			CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt,
			Body: comment.Body, BodyStorage: comment.BodyStorage,
		}
		if comment.Anchor != nil {
			record.MarkerRef = comment.Anchor.MarkerRef
			record.OriginalSelection = comment.Anchor.OriginalSelection
		}
		comments = append(comments, record)
	}
	if value.RootCount != rootCount {
		return confluenceCommentsSidecarError("root comment count is inconsistent")
	}
	diagnostics := make([]domain.ConfluenceCommentDiagnostic, 0, len(value.Diagnostics))
	partialReasonSet := make(map[string]struct{}, len(value.PartialReasons))
	for _, reason := range value.PartialReasons {
		partialReasonSet[reason] = struct{}{}
	}
	for _, diagnostic := range value.Diagnostics {
		if diagnostic.Location != "" && !domain.ValidConfluenceCommentLocation(diagnostic.Location) {
			return confluenceCommentsSidecarError("diagnostic location is invalid")
		}
		if domain.ValidConfluenceCommentPartialReason(diagnostic.Code) {
			if _, present := partialReasonSet[diagnostic.Code]; !present {
				return confluenceCommentsSidecarError("partial diagnostic has no matching partial reason")
			}
		}
		diagnostics = append(diagnostics, domain.ConfluenceCommentDiagnostic{
			Code: diagnostic.Code, CommentID: diagnostic.CommentID, Selector: diagnostic.Selector,
		})
	}
	inventory := domain.ConfluenceCommentInventory{
		Comments: comments, CommentsComplete: value.CommentsComplete, ThreadsComplete: value.ThreadsComplete,
		PartialReasons: append([]string{}, value.PartialReasons...), Capabilities: value.Capabilities,
		Diagnostics: diagnostics,
	}
	if err := domain.ValidateConfluenceCommentInventory(inventory); err != nil {
		return confluenceCommentsSidecarError("invalid qualified inventory: %v", err)
	}
	if !value.AnchorsComplete && len(value.PartialReasons) == 0 {
		return confluenceCommentsSidecarError("incomplete anchors have no partial reason")
	}
	return nil
}

func validateLegacyConfluenceComments(comments []domain.Comment) error {
	seen := make(map[string]struct{}, len(comments))
	for _, comment := range comments {
		if strings.TrimSpace(comment.ID) == "" {
			return confluenceCommentsSidecarError("legacy comment has an empty identity")
		}
		if _, duplicate := seen[comment.ID]; duplicate {
			return confluenceCommentsSidecarError("legacy comments contain a duplicate identity")
		}
		seen[comment.ID] = struct{}{}
	}
	return nil
}

func canonicalConfluenceCommentsSidecarV2(value ConfluenceCommentsSidecarV2) ConfluenceCommentsSidecarV2 {
	value.PartialReasons = append([]string{}, value.PartialReasons...)
	value.Comments = cloneConfluenceCommentsSidecarComments(value.Comments)
	sort.SliceStable(value.Comments, func(i, j int) bool { return value.Comments[i].ID < value.Comments[j].ID })
	value.Diagnostics = append([]ConfluenceCommentsSidecarDiagnostic{}, value.Diagnostics...)
	sort.SliceStable(value.Diagnostics, func(i, j int) bool {
		a, b := value.Diagnostics[i], value.Diagnostics[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.CommentID != b.CommentID {
			return a.CommentID < b.CommentID
		}
		if a.MarkerRef != b.MarkerRef {
			return a.MarkerRef < b.MarkerRef
		}
		if a.Selector != b.Selector {
			return a.Selector < b.Selector
		}
		return a.Location < b.Location
	})
	return value
}

func cloneConfluenceCommentsSidecarComments(values []ConfluenceCommentsSidecarComment) []ConfluenceCommentsSidecarComment {
	out := make([]ConfluenceCommentsSidecarComment, len(values))
	for i, value := range values {
		out[i] = value
		out[i].ParentID = cloneConfluenceCommentsSidecarString(value.ParentID)
		out[i].RootID = cloneConfluenceCommentsSidecarString(value.RootID)
		if value.Anchor != nil {
			anchor := *value.Anchor
			out[i].Anchor = &anchor
		}
	}
	return out
}

func cloneConfluenceCommentsSidecarString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func confluenceCommentsSidecarError(format string, args ...any) error {
	return fmt.Errorf("%w: Confluence comments sidecar: %s", domain.ErrCheckFailed, fmt.Sprintf(format, args...))
}
