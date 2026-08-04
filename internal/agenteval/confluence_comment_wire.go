package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// ConfluenceCommentViewSchemaVersion is the released comment list/thread projection.
	ConfluenceCommentViewSchemaVersion = 1

	confluenceCommentWireMaxBytes = 1 << 20
	confluenceCommentMaxPages     = 32
	confluenceCommentMaxItems     = 1000
	confluenceCommentMinBytes     = 1 << 10
)

// ConfluenceCommentQuery is the closed selector echo shared by list and thread views.
type ConfluenceCommentQuery struct {
	Mode      string `json:"mode"`
	Location  string `json:"location"`
	State     string `json:"state"`
	Depth     string `json:"depth"`
	CommentID string `json:"comment_id,omitempty"`
}

// ConfluenceCommentViewBounds records the resolved MCP read and output bounds.
type ConfluenceCommentViewBounds struct {
	MaxCommentPages int `json:"max_comment_pages"`
	MaxItems        int `json:"max_items"`
	MaxBytes        int `json:"max_bytes"`
}

// ConfluenceCommentCapabilities is the closed qualification catalog returned
// with every comment view.
type ConfluenceCommentCapabilities struct {
	Footer           string `json:"footer"`
	Inline           string `json:"inline"`
	Resolved         string `json:"resolved"`
	DepthAll         string `json:"depth_all"`
	ThreadAncestry   string `json:"thread_ancestry"`
	InlineProperties string `json:"inline_properties"`
	Resolution       string `json:"resolution"`
}

// ConfluenceCommentAuthor contains only the privacy-safe released identity fields.
type ConfluenceCommentAuthor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ConfluenceCommentViewAnchor is the selection-free inline anchor projection.
type ConfluenceCommentViewAnchor struct {
	MarkerRef string `json:"marker_ref"`
	Status    string `json:"status"`
}

// ConfluenceCommentViewDiagnostic is one content-free qualification diagnostic.
type ConfluenceCommentViewDiagnostic struct {
	Code      string `json:"code"`
	CommentID string `json:"comment_id,omitempty"`
	MarkerRef string `json:"marker_ref,omitempty"`
	Selector  string `json:"selector,omitempty"`
	Location  string `json:"location,omitempty"`
}

// ConfluenceCommentListViewRecord is one body-free discovery row.
type ConfluenceCommentListViewRecord struct {
	ID         string                       `json:"id"`
	ParentID   *string                      `json:"parent_id"`
	RootID     *string                      `json:"root_id"`
	Relation   string                       `json:"relation"`
	Location   string                       `json:"location"`
	Resolution string                       `json:"resolution"`
	Version    int                          `json:"version"`
	Author     ConfluenceCommentAuthor      `json:"author"`
	CreatedAt  string                       `json:"created_at"`
	UpdatedAt  string                       `json:"updated_at"`
	Anchor     *ConfluenceCommentViewAnchor `json:"anchor"`
}

// ConfluenceCommentThreadViewRecord adds only nullable reconciled plain text.
// A nil BodyText is qualified missing evidence; an empty string is projected
// empty content.
type ConfluenceCommentThreadViewRecord struct {
	ID         string                       `json:"id"`
	ParentID   *string                      `json:"parent_id"`
	RootID     *string                      `json:"root_id"`
	Relation   string                       `json:"relation"`
	Location   string                       `json:"location"`
	Resolution string                       `json:"resolution"`
	Version    int                          `json:"version"`
	Author     ConfluenceCommentAuthor      `json:"author"`
	CreatedAt  string                       `json:"created_at"`
	UpdatedAt  string                       `json:"updated_at"`
	BodyText   *string                      `json:"body_text"`
	Anchor     *ConfluenceCommentViewAnchor `json:"anchor"`
}

// ConfluenceCommentListView is the evaluator-owned released list wire. It
// deliberately does not import the product projection that it verifies.
type ConfluenceCommentListView struct {
	SchemaVersion    int                               `json:"schema_version"`
	PageID           string                            `json:"page_id"`
	PageVersion      int                               `json:"page_version"`
	PageVersionGated bool                              `json:"page_version_gated"`
	Query            ConfluenceCommentQuery            `json:"query"`
	Bounds           ConfluenceCommentViewBounds       `json:"bounds"`
	Complete         bool                              `json:"complete"`
	CommentsComplete bool                              `json:"comments_complete"`
	ThreadsComplete  bool                              `json:"threads_complete"`
	AnchorsComplete  bool                              `json:"anchors_complete"`
	Count            int                               `json:"count"`
	RootCount        int                               `json:"root_count"`
	PartialReasons   []string                          `json:"partial_reasons"`
	Capabilities     ConfluenceCommentCapabilities     `json:"capabilities"`
	Comments         []ConfluenceCommentListViewRecord `json:"comments"`
	Diagnostics      []ConfluenceCommentViewDiagnostic `json:"diagnostics"`
}

// ConfluenceCommentThreadView is the evaluator-owned released exact-thread wire.
type ConfluenceCommentThreadView struct {
	SchemaVersion    int                                 `json:"schema_version"`
	PageID           string                              `json:"page_id"`
	PageVersion      int                                 `json:"page_version"`
	PageVersionGated bool                                `json:"page_version_gated"`
	Query            ConfluenceCommentQuery              `json:"query"`
	Bounds           ConfluenceCommentViewBounds         `json:"bounds"`
	Complete         bool                                `json:"complete"`
	CommentsComplete bool                                `json:"comments_complete"`
	ThreadsComplete  bool                                `json:"threads_complete"`
	AnchorsComplete  bool                                `json:"anchors_complete"`
	Count            int                                 `json:"count"`
	RootCount        int                                 `json:"root_count"`
	PartialReasons   []string                            `json:"partial_reasons"`
	Capabilities     ConfluenceCommentCapabilities       `json:"capabilities"`
	Comments         []ConfluenceCommentThreadViewRecord `json:"comments"`
	Diagnostics      []ConfluenceCommentViewDiagnostic   `json:"diagnostics"`
}

// DecodeConfluenceCommentListView strictly decodes and reconciles one released
// body-free list view.
func DecodeConfluenceCommentListView(r io.Reader) (ConfluenceCommentListView, error) {
	var view ConfluenceCommentListView
	data, err := decodeConfluenceCommentWire(r, "comment list", false)
	if err != nil {
		return ConfluenceCommentListView{}, err
	}
	if err := json.Unmarshal(data, &view); err != nil {
		return ConfluenceCommentListView{}, fmt.Errorf("decode Confluence comment list wire: %w", err)
	}
	if err := view.validate(data); err != nil {
		return ConfluenceCommentListView{}, fmt.Errorf("validate Confluence comment list view: %w", err)
	}
	return view, nil
}

// DecodeConfluenceCommentThreadView strictly decodes and reconciles one
// released exact-thread view.
func DecodeConfluenceCommentThreadView(r io.Reader) (ConfluenceCommentThreadView, error) {
	var view ConfluenceCommentThreadView
	data, err := decodeConfluenceCommentWire(r, "comment thread", true)
	if err != nil {
		return ConfluenceCommentThreadView{}, err
	}
	if err := json.Unmarshal(data, &view); err != nil {
		return ConfluenceCommentThreadView{}, fmt.Errorf("decode Confluence comment thread wire: %w", err)
	}
	if err := view.validate(data); err != nil {
		return ConfluenceCommentThreadView{}, fmt.Errorf("validate Confluence comment thread view: %w", err)
	}
	return view, nil
}

func decodeConfluenceCommentWire(r io.Reader, subject string, thread bool) ([]byte, error) {
	limited := &io.LimitedReader{R: r, N: confluenceCommentWireMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read Confluence %s wire: %w", subject, err)
	}
	if limited.N <= 0 {
		return nil, fmt.Errorf("confluence %s wire exceeds %d bytes", subject, confluenceCommentWireMaxBytes)
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return nil, fmt.Errorf("decode Confluence %s wire: %w", subject, err)
	}
	if err := validateConfluenceCommentMembers(data, thread); err != nil {
		return nil, fmt.Errorf("decode Confluence %s wire: %w", subject, err)
	}
	return data, nil
}

func validateConfluenceCommentMembers(data []byte, thread bool) error {
	owner := "comment list"
	if thread {
		owner = "comment thread"
	}
	root, err := confluenceEvidenceObject(data, owner)
	if err != nil {
		return err
	}
	rootMembers := []string{
		"schema_version", "page_id", "page_version", "page_version_gated", "query", "bounds",
		"complete", "comments_complete", "threads_complete", "anchors_complete", "count", "root_count",
		"partial_reasons", "capabilities", "comments", "diagnostics",
	}
	if err := requireExactJSONMembers(root, owner, rootMembers); err != nil {
		return err
	}
	for _, member := range rootMembers {
		if err := requireConfluenceCommentNonNull(root[member], owner+"."+member); err != nil {
			return err
		}
	}

	query, err := confluenceCommentObject(root["query"], owner+".query")
	if err != nil {
		return err
	}
	queryMembers := []string{"mode", "location", "state", "depth"}
	if thread {
		queryMembers = append(queryMembers, "comment_id")
	}
	if err := requireExactJSONMembers(query, owner+".query", queryMembers); err != nil {
		return err
	}
	for _, member := range queryMembers {
		if err := requireConfluenceCommentNonNull(query[member], owner+".query."+member); err != nil {
			return err
		}
	}

	bounds, err := confluenceCommentObject(root["bounds"], owner+".bounds")
	if err != nil {
		return err
	}
	boundMembers := []string{"max_comment_pages", "max_items", "max_bytes"}
	if err := requireExactJSONMembers(bounds, owner+".bounds", boundMembers); err != nil {
		return err
	}
	for _, member := range boundMembers {
		if err := requireConfluenceCommentNonNull(bounds[member], owner+".bounds."+member); err != nil {
			return err
		}
	}

	capabilities, err := confluenceCommentObject(root["capabilities"], owner+".capabilities")
	if err != nil {
		return err
	}
	capabilityMembers := []string{"footer", "inline", "resolved", "depth_all", "thread_ancestry", "inline_properties", "resolution"}
	if err := requireExactJSONMembers(capabilities, owner+".capabilities", capabilityMembers); err != nil {
		return err
	}
	for _, member := range capabilityMembers {
		if err := requireConfluenceCommentNonNull(capabilities[member], owner+".capabilities."+member); err != nil {
			return err
		}
	}

	var reasons []json.RawMessage
	if err := json.Unmarshal(root["partial_reasons"], &reasons); err != nil {
		return fmt.Errorf("%s.partial_reasons: %w", owner, err)
	}
	for index, reason := range reasons {
		if err := requireConfluenceCommentNonNull(reason, fmt.Sprintf("%s.partial_reasons[%d]", owner, index)); err != nil {
			return err
		}
	}

	var comments []json.RawMessage
	if err := json.Unmarshal(root["comments"], &comments); err != nil {
		return fmt.Errorf("%s.comments: %w", owner, err)
	}
	for index, raw := range comments {
		if err := validateConfluenceCommentRecordMembers(raw, fmt.Sprintf("%s.comments[%d]", owner, index), thread); err != nil {
			return err
		}
	}

	var diagnostics []json.RawMessage
	if err := json.Unmarshal(root["diagnostics"], &diagnostics); err != nil {
		return fmt.Errorf("%s.diagnostics: %w", owner, err)
	}
	for index, raw := range diagnostics {
		if err := validateConfluenceCommentDiagnosticMembers(raw, fmt.Sprintf("%s.diagnostics[%d]", owner, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateConfluenceCommentRecordMembers(raw json.RawMessage, owner string, thread bool) error {
	record, err := confluenceCommentObject(raw, owner)
	if err != nil {
		return err
	}
	members := []string{
		"id", "parent_id", "root_id", "relation", "location", "resolution", "version",
		"author", "created_at", "updated_at", "anchor",
	}
	if thread {
		members = append(members, "body_text")
	}
	if err := requireExactJSONMembers(record, owner, members); err != nil {
		return err
	}
	allowedNull := map[string]bool{"parent_id": true, "root_id": true, "anchor": true}
	if thread {
		allowedNull["body_text"] = true
	}
	for _, member := range members {
		if !allowedNull[member] {
			if err := requireConfluenceCommentNonNull(record[member], owner+"."+member); err != nil {
				return err
			}
		}
	}
	author, err := confluenceCommentObject(record["author"], owner+".author")
	if err != nil {
		return err
	}
	if err := requireExactJSONMembers(author, owner+".author", []string{"id", "display_name"}); err != nil {
		return err
	}
	for _, member := range []string{"id", "display_name"} {
		if err := requireConfluenceCommentNonNull(author[member], owner+".author."+member); err != nil {
			return err
		}
	}
	if !bytes.Equal(bytes.TrimSpace(record["anchor"]), []byte("null")) {
		anchor, err := confluenceCommentObject(record["anchor"], owner+".anchor")
		if err != nil {
			return err
		}
		if err := requireExactJSONMembers(anchor, owner+".anchor", []string{"marker_ref", "status"}); err != nil {
			return err
		}
		for _, member := range []string{"marker_ref", "status"} {
			if err := requireConfluenceCommentNonNull(anchor[member], owner+".anchor."+member); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateConfluenceCommentDiagnosticMembers(raw json.RawMessage, owner string) error {
	diagnostic, err := confluenceCommentObject(raw, owner)
	if err != nil {
		return err
	}
	if err := requireConfluenceEvidenceMembers(diagnostic, owner, []string{"code"}, []string{"comment_id", "marker_ref", "selector", "location"}); err != nil {
		return err
	}
	if err := requireConfluenceCommentNonNull(diagnostic["code"], owner+".code"); err != nil {
		return err
	}
	for _, member := range []string{"comment_id", "marker_ref", "selector", "location"} {
		if value, present := diagnostic[member]; present {
			if err := requireConfluenceCommentNonNull(value, owner+"."+member); err != nil {
				return err
			}
		}
	}
	return nil
}

func confluenceCommentObject(raw json.RawMessage, owner string) (map[string]json.RawMessage, error) {
	if err := requireConfluenceCommentNonNull(raw, owner); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("%s: %w", owner, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be an object", owner)
	}
	return object, nil
}

func requireConfluenceCommentNonNull(raw json.RawMessage, owner string) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%s must not be null", owner)
	}
	return nil
}

func (v ConfluenceCommentListView) validate(data []byte) error {
	if err := validateConfluenceCommentCommon(
		v.SchemaVersion, v.PageID, v.PageVersion, v.Query, v.Bounds,
		v.Complete, v.CommentsComplete, v.ThreadsComplete, v.AnchorsComplete,
		v.Count, v.RootCount, v.PartialReasons, v.Capabilities, len(v.Comments), v.Diagnostics,
	); err != nil {
		return err
	}
	if v.Query.Mode != "list" || v.Query.CommentID != "" {
		return fmt.Errorf("comment list query mode is not reconciled")
	}
	if err := validateConfluenceCommentEncodedBound(data, v.Bounds.MaxBytes, v); err != nil {
		return err
	}
	rootCount := 0
	seen := make(map[string]struct{}, len(v.Comments))
	for index, comment := range v.Comments {
		if err := validateConfluenceCommentRecord(
			comment.ID, comment.ParentID, comment.RootID, comment.Relation, comment.Location,
			comment.Resolution, comment.Version, comment.Author, comment.CreatedAt, comment.UpdatedAt, comment.Anchor,
		); err != nil {
			return fmt.Errorf("comment[%d]: %w", index, err)
		}
		if _, duplicate := seen[comment.ID]; duplicate {
			return fmt.Errorf("comment list repeats identity %q", comment.ID)
		}
		seen[comment.ID] = struct{}{}
		if err := validateConfluenceCommentAnchorQualification(comment.Anchor, v.PartialReasons); err != nil {
			return fmt.Errorf("comment[%d]: %w", index, err)
		}
		if comment.Relation == "root" {
			rootCount++
		}
	}
	if rootCount != v.RootCount {
		return fmt.Errorf("comment list root count is not reconciled")
	}
	return nil
}

func (v ConfluenceCommentThreadView) validate(data []byte) error {
	if err := validateConfluenceCommentCommon(
		v.SchemaVersion, v.PageID, v.PageVersion, v.Query, v.Bounds,
		v.Complete, v.CommentsComplete, v.ThreadsComplete, v.AnchorsComplete,
		v.Count, v.RootCount, v.PartialReasons, v.Capabilities, len(v.Comments), v.Diagnostics,
	); err != nil {
		return err
	}
	if v.Query.Mode != "thread" || !canonicalConfluenceCommentID(v.Query.CommentID) ||
		v.Query.Location != "all" || v.Query.State != "all" || v.Query.Depth != "all" {
		return fmt.Errorf("comment thread query is not reconciled")
	}
	if err := validateConfluenceCommentEncodedBound(data, v.Bounds.MaxBytes, v); err != nil {
		return err
	}
	rootCount := 0
	seen := make(map[string]struct{}, len(v.Comments))
	byID := make(map[string]ConfluenceCommentThreadViewRecord, len(v.Comments))
	for index, comment := range v.Comments {
		if err := validateConfluenceCommentRecord(
			comment.ID, comment.ParentID, comment.RootID, comment.Relation, comment.Location,
			comment.Resolution, comment.Version, comment.Author, comment.CreatedAt, comment.UpdatedAt, comment.Anchor,
		); err != nil {
			return fmt.Errorf("comment[%d]: %w", index, err)
		}
		if comment.BodyText == nil && !containsConfluenceCommentString(v.PartialReasons, "comment_body_unavailable") {
			return fmt.Errorf("comment[%d] has null body_text without comment_body_unavailable", index)
		}
		if comment.BodyText != nil && !utf8.ValidString(*comment.BodyText) {
			return fmt.Errorf("comment[%d] body_text is not valid UTF-8", index)
		}
		if _, duplicate := seen[comment.ID]; duplicate {
			return fmt.Errorf("comment thread repeats identity %q", comment.ID)
		}
		seen[comment.ID] = struct{}{}
		byID[comment.ID] = comment
		if err := validateConfluenceCommentAnchorQualification(comment.Anchor, v.PartialReasons); err != nil {
			return fmt.Errorf("comment[%d]: %w", index, err)
		}
		if comment.Relation == "root" {
			rootCount++
		}
	}
	if rootCount != v.RootCount {
		return fmt.Errorf("comment thread root count is not reconciled")
	}
	selected, present := byID[v.Query.CommentID]
	if !present {
		return fmt.Errorf("comment thread does not contain selected identity")
	}
	if err := validateConfluenceCommentThreadScope(selected, v.Comments); err != nil {
		return err
	}
	if v.ThreadsComplete {
		if err := validateConfluenceCommentThreadAncestry(byID); err != nil {
			return err
		}
	}
	anchors := make(map[string]*ConfluenceCommentViewAnchor, len(v.Comments))
	for _, comment := range v.Comments {
		anchors[comment.ID] = comment.Anchor
	}
	for _, diagnostic := range v.Diagnostics {
		if diagnostic.Code == "orphan_marker" {
			return fmt.Errorf("comment thread contains an unrelated page marker diagnostic")
		}
		if diagnostic.CommentID != "" {
			if _, present := seen[diagnostic.CommentID]; !present {
				return fmt.Errorf("comment thread contains an unrelated comment diagnostic")
			}
		}
		if diagnostic.MarkerRef != "" {
			anchor, present := anchors[diagnostic.CommentID]
			if !present || anchor == nil || anchor.MarkerRef != diagnostic.MarkerRef {
				return fmt.Errorf("comment thread contains an unrelated marker diagnostic")
			}
		}
	}
	return nil
}

func validateConfluenceCommentCommon(
	schemaVersion int,
	pageID string,
	pageVersion int,
	query ConfluenceCommentQuery,
	bounds ConfluenceCommentViewBounds,
	complete, commentsComplete, threadsComplete, anchorsComplete bool,
	count, rootCount int,
	partialReasons []string,
	capabilities ConfluenceCommentCapabilities,
	commentCount int,
	diagnostics []ConfluenceCommentViewDiagnostic,
) error {
	if schemaVersion != ConfluenceCommentViewSchemaVersion || !canonicalConfluenceCommentID(pageID) || pageVersion < 1 {
		return fmt.Errorf("comment view provenance is not reconciled")
	}
	if partialReasons == nil || diagnostics == nil {
		return fmt.Errorf("comment view collections must not be null")
	}
	if query.Mode != "list" && query.Mode != "thread" {
		return fmt.Errorf("comment query mode %q is unsupported", query.Mode)
	}
	if !oneOfConfluenceComment(query.Location, "all", "footer", "inline", "resolved") ||
		!oneOfConfluenceComment(query.State, "all", "open", "resolved", "unknown") ||
		!oneOfConfluenceComment(query.Depth, "all", "root") {
		return fmt.Errorf("comment query selectors are not reconciled")
	}
	if bounds.MaxCommentPages != confluenceCommentMaxPages || bounds.MaxItems < 1 || bounds.MaxItems > confluenceCommentMaxItems ||
		bounds.MaxBytes < confluenceCommentMinBytes || bounds.MaxBytes > confluenceCommentWireMaxBytes {
		return fmt.Errorf("comment view bounds are not reconciled")
	}
	if count != commentCount || count > bounds.MaxItems || rootCount < 0 || rootCount > count {
		return fmt.Errorf("comment view counts are not reconciled")
	}
	if !sort.StringsAreSorted(partialReasons) {
		return fmt.Errorf("comment partial reasons are not sorted")
	}
	seenReasons := make(map[string]struct{}, len(partialReasons))
	for _, reason := range partialReasons {
		if !validConfluenceCommentPartialReason(reason) {
			return fmt.Errorf("comment partial reason %q is unsupported", reason)
		}
		if _, duplicate := seenReasons[reason]; duplicate {
			return fmt.Errorf("comment partial reason %q is repeated", reason)
		}
		seenReasons[reason] = struct{}{}
	}
	wantCommentsComplete, wantThreadsComplete, wantAnchorsComplete := true, true, true
	for _, reason := range partialReasons {
		wantCommentsComplete = wantCommentsComplete && !confluenceCommentReasonAffectsComments(reason)
		wantThreadsComplete = wantThreadsComplete && !confluenceCommentReasonAffectsThreads(reason)
		wantAnchorsComplete = wantAnchorsComplete && !confluenceCommentReasonAffectsAnchors(reason)
	}
	if commentsComplete != wantCommentsComplete || threadsComplete != wantThreadsComplete || anchorsComplete != wantAnchorsComplete ||
		complete != (len(partialReasons) == 0 && commentsComplete && threadsComplete && anchorsComplete) {
		return fmt.Errorf("comment completeness is not reconciled with partial reasons")
	}
	for _, status := range []string{
		capabilities.Footer, capabilities.Inline, capabilities.Resolved, capabilities.DepthAll,
		capabilities.ThreadAncestry, capabilities.InlineProperties, capabilities.Resolution,
	} {
		if !oneOfConfluenceComment(status, "observed", "documented", "unsupported", "unknown") {
			return fmt.Errorf("comment capability status %q is unsupported", status)
		}
	}
	for index, diagnostic := range diagnostics {
		if err := validateConfluenceCommentDiagnostic(diagnostic, seenReasons); err != nil {
			return fmt.Errorf("diagnostic[%d]: %w", index, err)
		}
	}
	return nil
}

func validateConfluenceCommentRecord(
	id string,
	parentID, rootID *string,
	relation, location, resolution string,
	version int,
	author ConfluenceCommentAuthor,
	createdAt, updatedAt string,
	anchor *ConfluenceCommentViewAnchor,
) error {
	if !canonicalConfluenceCommentID(id) || !optionalCanonicalConfluenceCommentID(parentID) ||
		!optionalCanonicalConfluenceCommentID(rootID) || version < 0 ||
		!oneOfConfluenceComment(relation, "root", "reply", "unknown") ||
		!oneOfConfluenceComment(location, "footer", "inline", "unknown") ||
		!oneOfConfluenceComment(resolution, "open", "resolved", "unknown") {
		return fmt.Errorf("record metadata is not reconciled")
	}
	if err := validateConfluenceCommentRelationship(id, relation, parentID, rootID); err != nil {
		return err
	}
	if !utf8.ValidString(author.ID) || !utf8.ValidString(author.DisplayName) ||
		strings.Contains(author.ID, "@") || strings.Contains(author.DisplayName, "@") ||
		strings.Contains(author.ID, "://") || strings.Contains(author.DisplayName, "://") {
		return fmt.Errorf("record author is not privacy-safe")
	}
	if !validConfluenceCommentTimestamp(createdAt) || !validConfluenceCommentTimestamp(updatedAt) {
		return fmt.Errorf("record timestamps are not reconciled")
	}
	if relation == "reply" && anchor != nil {
		return fmt.Errorf("reply carries root-owned anchor evidence")
	}
	if relation != "reply" && location == "inline" && anchor == nil {
		return fmt.Errorf("inline root has no anchor evidence")
	}
	if anchor != nil {
		if !oneOfConfluenceComment(anchor.Status, "matched", "missing", "ambiguous", "unavailable") ||
			(anchor.MarkerRef == "" && anchor.Status != "unavailable") ||
			(anchor.MarkerRef != "" && !validConfluenceCommentMarkerRef(anchor.MarkerRef)) {
			return fmt.Errorf("record anchor is not reconciled")
		}
	}
	return nil
}

func validateConfluenceCommentRelationship(id, relation string, parentID, rootID *string) error {
	switch relation {
	case "root":
		if parentID != nil || rootID == nil || *rootID != id {
			return fmt.Errorf("root relationship is not reconciled")
		}
	case "reply":
		if parentID == nil || rootID == nil || *parentID == id || *rootID == id {
			return fmt.Errorf("reply relationship is not reconciled")
		}
	case "unknown":
		if parentID != nil || rootID != nil {
			return fmt.Errorf("unknown relationship carries inferred ancestry")
		}
	}
	return nil
}

func validateConfluenceCommentAnchorQualification(anchor *ConfluenceCommentViewAnchor, reasons []string) error {
	if anchor == nil || anchor.Status == "matched" {
		return nil
	}
	want := map[string]string{
		"missing":     "anchor_missing",
		"ambiguous":   "anchor_ambiguous",
		"unavailable": "inline_expansion_unavailable",
	}[anchor.Status]
	if !containsConfluenceCommentString(reasons, want) {
		return fmt.Errorf("anchor status %q is not qualified by %s", anchor.Status, want)
	}
	return nil
}

func validateConfluenceCommentThreadScope(selected ConfluenceCommentThreadViewRecord, comments []ConfluenceCommentThreadViewRecord) error {
	if selected.Relation == "unknown" {
		if len(comments) != 1 {
			return fmt.Errorf("unknown selected comment is not an isolated partial thread")
		}
		return nil
	}
	rootID := selected.ID
	if selected.Relation == "reply" {
		rootID = *selected.RootID
	}
	for _, comment := range comments {
		switch comment.Relation {
		case "root":
			if comment.ID != rootID {
				return fmt.Errorf("comment thread contains an unrelated root")
			}
		case "reply":
			if comment.RootID == nil || *comment.RootID != rootID {
				return fmt.Errorf("comment thread contains an unrelated reply")
			}
		case "unknown":
			return fmt.Errorf("known comment thread contains an unrelated unknown row")
		}
	}
	return nil
}

func validateConfluenceCommentThreadAncestry(byID map[string]ConfluenceCommentThreadViewRecord) error {
	for _, comment := range byID {
		if comment.Relation == "root" {
			continue
		}
		if comment.Relation != "reply" || comment.ParentID == nil || comment.RootID == nil {
			return fmt.Errorf("complete comment thread has unknown ancestry")
		}
		rootID := *comment.RootID
		root, present := byID[rootID]
		if !present || root.Relation != "root" || root.RootID == nil || *root.RootID != rootID {
			return fmt.Errorf("complete comment thread omits root ancestry")
		}
		seen := map[string]struct{}{comment.ID: {}}
		current := comment
		for current.Relation == "reply" {
			parentID := *current.ParentID
			if _, duplicate := seen[parentID]; duplicate {
				return fmt.Errorf("complete comment thread has cyclic ancestry")
			}
			seen[parentID] = struct{}{}
			parent, present := byID[parentID]
			if !present {
				return fmt.Errorf("complete comment thread omits parent ancestry")
			}
			if parentID == rootID {
				if parent.Relation != "root" {
					return fmt.Errorf("complete comment thread has inconsistent root ancestry")
				}
				break
			}
			if parent.Relation != "reply" || parent.RootID == nil || *parent.RootID != rootID {
				return fmt.Errorf("complete comment thread has inconsistent parent ancestry")
			}
			current = parent
		}
	}
	return nil
}

func validateConfluenceCommentDiagnostic(diagnostic ConfluenceCommentViewDiagnostic, reasons map[string]struct{}) error {
	if !validConfluenceCommentDiagnosticCode(diagnostic.Code) ||
		(diagnostic.CommentID != "" && !canonicalConfluenceCommentID(diagnostic.CommentID)) ||
		(diagnostic.MarkerRef != "" && !validConfluenceCommentMarkerRef(diagnostic.MarkerRef)) ||
		(diagnostic.Selector != "" && !oneOfConfluenceComment(diagnostic.Selector, "footer", "inline", "resolved")) ||
		(diagnostic.Location != "" && !oneOfConfluenceComment(diagnostic.Location, "footer", "inline", "unknown")) {
		return fmt.Errorf("diagnostic is not reconciled")
	}
	if validConfluenceCommentPartialReason(diagnostic.Code) {
		if _, present := reasons[diagnostic.Code]; !present {
			return fmt.Errorf("partial diagnostic is absent from partial_reasons")
		}
	}
	return nil
}

func validateConfluenceCommentEncodedBound(data []byte, maxBytes int, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode comment view for bound reconciliation: %w", err)
	}
	if len(encoded) > maxBytes {
		return fmt.Errorf("encoded comment view exceeds echoed max_bytes")
	}
	if len(data) > confluenceCommentWireMaxBytes {
		return fmt.Errorf("comment wire exceeds decoder limit")
	}
	return nil
}

func canonicalConfluenceCommentID(value string) bool {
	if value == "" || value[0] == '0' || strings.TrimSpace(value) != value {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0
}

func optionalCanonicalConfluenceCommentID(value *string) bool {
	return value == nil || canonicalConfluenceCommentID(*value)
}

func validConfluenceCommentTimestamp(value string) bool {
	if value == "" {
		return true
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func validConfluenceCommentMarkerRef(value string) bool {
	if len(value) < 1 || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func validConfluenceCommentPartialReason(reason string) bool {
	return oneOfConfluenceComment(reason,
		"page_limit", "item_limit", "pagination_stalled", "pagination_unqualified",
		"conflicting_duplicate_objects", "backend_omitted_children", "parent_unavailable", "malformed_ancestry",
		"location_unavailable", "inline_expansion_unavailable", "resolution_unavailable", "comment_metadata_unavailable",
		"comment_body_unavailable", "page_body_unavailable", "anchor_missing", "anchor_ambiguous",
		"endpoint_unavailable", "legacy_unqualified",
	)
}

func validConfluenceCommentDiagnosticCode(code string) bool {
	return validConfluenceCommentPartialReason(code) || oneOfConfluenceComment(code, "orphan_marker", "original_selection_changed")
}

func confluenceCommentReasonAffectsComments(reason string) bool {
	return oneOfConfluenceComment(reason,
		"page_limit", "item_limit", "pagination_stalled", "pagination_unqualified", "conflicting_duplicate_objects",
		"location_unavailable", "resolution_unavailable", "comment_metadata_unavailable", "comment_body_unavailable",
		"endpoint_unavailable", "legacy_unqualified",
	)
}

func confluenceCommentReasonAffectsThreads(reason string) bool {
	return oneOfConfluenceComment(reason,
		"page_limit", "item_limit", "pagination_stalled", "pagination_unqualified", "conflicting_duplicate_objects",
		"backend_omitted_children", "parent_unavailable", "malformed_ancestry", "endpoint_unavailable", "legacy_unqualified",
	)
}

func confluenceCommentReasonAffectsAnchors(reason string) bool {
	return oneOfConfluenceComment(reason,
		"location_unavailable", "inline_expansion_unavailable", "page_body_unavailable", "anchor_missing", "anchor_ambiguous",
		"legacy_unqualified",
	)
}

func oneOfConfluenceComment(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func containsConfluenceCommentString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
