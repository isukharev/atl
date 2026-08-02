package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

const confluenceCommentExpand = "body.storage,history,version,ancestors,extensions.inlineProperties,extensions.resolution"

var canonicalCommentSelectors = []domain.ConfluenceCommentSelector{
	domain.ConfluenceCommentSelectorFooter,
	domain.ConfluenceCommentSelectorInline,
	domain.ConfluenceCommentSelectorResolved,
}

type commentPageJSON struct {
	Results *[]commentJSON `json:"results"`
	Start   *int           `json:"start"`
	Limit   *int           `json:"limit"`
	Size    *int           `json:"size"`
	Links   *struct {
		Next string `json:"next"`
	} `json:"_links"`
}

type commentJSON struct {
	ID        string                        `json:"id"`
	Type      string                        `json:"type"`
	Status    string                        `json:"status"`
	History   *commentHistoryJSON           `json:"history"`
	Version   *commentVersionJSON           `json:"version"`
	Ancestors *[]commentContentIdentityJSON `json:"ancestors"`
	Body      *struct {
		Storage *commentBodyJSON `json:"storage"`
	} `json:"body"`
	Extensions map[string]json.RawMessage `json:"extensions"`
}

type commentHistoryJSON struct {
	CreatedDate string             `json:"createdDate"`
	CreatedBy   *commentPersonJSON `json:"createdBy"`
}

type commentVersionJSON struct {
	Number int                `json:"number"`
	When   string             `json:"when"`
	By     *commentPersonJSON `json:"by"`
}

type commentPersonJSON struct {
	UserKey     string `json:"userKey"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

type commentContentIdentityJSON struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type commentBodyJSON struct {
	Value          string  `json:"value"`
	Representation *string `json:"representation"`
}

type commentResolutionJSON struct {
	Status string `json:"status"`
}

type commentInlinePropertiesJSON struct {
	MarkerRef         string `json:"markerRef"`
	OriginalSelection string `json:"originalSelection"`
}

type commentInventoryBuilder struct {
	inventory                      domain.ConfluenceCommentInventory
	byID                           map[string]domain.ConfluenceCommentRecord
	conflicted                     map[string]struct{}
	replyInlineProperties          map[string]commentInlinePropertiesJSON
	replyInlinePropertiesConflicts map[string]struct{}
	reasons                        map[string]struct{}
	diagnostics                    map[string]struct{}
	pages                          int
	items                          int
}

func newCommentInventoryBuilder(depthAll bool) *commentInventoryBuilder {
	documented := domain.ConfluenceCapabilityDocumented
	depth := documented
	if !depthAll {
		// The endpoint's root-only behavior is still documented. DepthAll remains
		// documented rather than observed because this invocation did not request it.
		depth = documented
	}
	return &commentInventoryBuilder{
		inventory: domain.ConfluenceCommentInventory{
			Comments: []domain.ConfluenceCommentRecord{}, CommentsComplete: true, ThreadsComplete: true,
			PartialReasons: []string{}, Diagnostics: []domain.ConfluenceCommentDiagnostic{},
			Capabilities: domain.ConfluenceCommentCapabilities{
				Footer: documented, Inline: documented, Resolved: documented,
				DepthAll: depth, ThreadAncestry: documented,
				InlineProperties: documented, Resolution: documented,
			},
		},
		byID: make(map[string]domain.ConfluenceCommentRecord), conflicted: make(map[string]struct{}),
		replyInlineProperties: make(map[string]commentInlinePropertiesJSON), replyInlinePropertiesConflicts: make(map[string]struct{}),
		reasons: make(map[string]struct{}), diagnostics: make(map[string]struct{}),
	}
}

func (b *commentInventoryBuilder) partial(reason, commentID string, selector domain.ConfluenceCommentSelector, comments, threads bool) {
	b.reasons[reason] = struct{}{}
	if comments {
		b.inventory.CommentsComplete = false
	}
	if threads {
		b.inventory.ThreadsComplete = false
	}
	key := reason + "\x00" + commentID + "\x00" + string(selector)
	if _, exists := b.diagnostics[key]; exists {
		return
	}
	b.diagnostics[key] = struct{}{}
	b.inventory.Diagnostics = append(b.inventory.Diagnostics, domain.ConfluenceCommentDiagnostic{
		Code: reason, CommentID: commentID, Selector: selector,
	})
}

func (b *commentInventoryBuilder) finish() (domain.ConfluenceCommentInventory, error) {
	comments := make([]domain.ConfluenceCommentRecord, 0, len(b.byID))
	for id, comment := range b.byID {
		if _, conflict := b.conflicted[id]; conflict {
			continue
		}
		demotedReply := false
		if comment.Relation == domain.ConfluenceCommentRelationReply {
			if !commentAncestryObjectsPresent(comment, b.byID, b.conflicted) {
				b.partial(domain.ConfluenceCommentPartialBackendOmittedChildren, comment.ID, "", false, true)
			} else if !consistentCommentAncestry(comment, b.byID) {
				comment.Relation, comment.ParentID, comment.RootID = domain.ConfluenceCommentRelationUnknown, nil, nil
				demotedReply = true
				b.partial(domain.ConfluenceCommentPartialMalformedAncestry, comment.ID, "", false, true)
			}
		}
		if comment.Relation == domain.ConfluenceCommentRelationReply {
			// Some backend shapes copy the root's inline properties onto replies.
			// Replies are ancestry evidence, not independent anchor owners.
			comment.MarkerRef, comment.OriginalSelection = "", ""
		} else if demotedReply {
			properties, present := b.replyInlineProperties[comment.ID]
			_, conflicting := b.replyInlinePropertiesConflicts[comment.ID]
			if present && !conflicting {
				comment.MarkerRef, comment.OriginalSelection = properties.MarkerRef, properties.OriginalSelection
				if b.inventory.Capabilities.InlineProperties != domain.ConfluenceCapabilityUnknown {
					b.inventory.Capabilities.InlineProperties = domain.ConfluenceCapabilityObserved
				}
			} else if comment.Location == domain.ConfluenceCommentLocationInline {
				// A provisional reply can become unknown only after the whole ancestry
				// graph is available. Qualify its root-like anchor evidence now rather
				// than silently accepting the earlier provisional exemption.
				b.partial(domain.ConfluenceCommentPartialInlineExpansionUnavailable, comment.ID, "", false, false)
				b.inventory.Capabilities.InlineProperties = domain.ConfluenceCapabilityUnknown
			}
		}
		comments = append(comments, comment)
	}
	sort.Slice(comments, func(i, j int) bool { return comments[i].ID < comments[j].ID })
	b.inventory.Comments = comments
	b.inventory.PartialReasons = make([]string, 0, len(b.reasons))
	for reason := range b.reasons {
		b.inventory.PartialReasons = append(b.inventory.PartialReasons, reason)
	}
	sort.Strings(b.inventory.PartialReasons)
	sort.Slice(b.inventory.Diagnostics, func(i, j int) bool {
		a, c := b.inventory.Diagnostics[i], b.inventory.Diagnostics[j]
		if a.Code != c.Code {
			return a.Code < c.Code
		}
		if a.CommentID != c.CommentID {
			return a.CommentID < c.CommentID
		}
		return a.Selector < c.Selector
	})
	if err := domain.ValidateConfluenceCommentInventory(b.inventory); err != nil {
		return domain.ConfluenceCommentInventory{}, err
	}
	return b.inventory, nil
}

// ListConfluenceComments implements the optional qualified Confluence comment
// reader. It uses only the documented page-child endpoint and performs one
// fixed location query at a time so every selected result set is qualified
// independently. Pagination links are signals only; their URLs are never
// followed.
func (cf *Confluence) ListConfluenceComments(ctx context.Context, id string, options domain.ConfluenceCommentReadOptions) (domain.ConfluenceCommentInventory, error) {
	if strings.TrimSpace(id) == "" {
		return domain.ConfluenceCommentInventory{}, fmt.Errorf("%w: Confluence page id is required", domain.ErrUsage)
	}
	if err := domain.ValidateConfluenceCommentReadOptions(options); err != nil {
		return domain.ConfluenceCommentInventory{}, err
	}
	selectors, err := selectedCommentSelectors(options.Locations)
	if err != nil {
		return domain.ConfluenceCommentInventory{}, err
	}
	pageLimit, itemLimit := maxPages, maxItems
	if options.MaxPages > 0 {
		pageLimit = options.MaxPages
	}
	if options.MaxItems > 0 {
		itemLimit = options.MaxItems
	}
	builder := newCommentInventoryBuilder(options.DepthAll)
	stop := false
	for _, selector := range selectors {
		if stop {
			break
		}
		start := 0
		locationRows := 0
		locationObserved := false
		for {
			if builder.pages >= pageLimit {
				builder.partial(domain.ConfluenceCommentPartialPageLimit, "", selector, true, true)
				stop = true
				break
			}
			if builder.items >= itemLimit {
				builder.partial(domain.ConfluenceCommentPartialItemLimit, "", selector, true, true)
				stop = true
				break
			}
			q := url.Values{}
			q.Set("expand", confluenceCommentExpand)
			q.Set("limit", "100")
			q.Set("location", string(selector))
			q.Set("parentVersion", strconv.Itoa(options.ParentVersion))
			q.Set("start", strconv.Itoa(start))
			if options.DepthAll {
				q.Set("depth", "all")
			}
			path := "/rest/api/content/" + url.PathEscape(id) + "/child/comment?" + q.Encode()
			var response commentPageJSON
			if err := cf.c.GetJSON(ctx, path, &response); err != nil {
				if capability, handled := confluenceCommentSelectorErrorCapability(err); handled {
					builder.partial(domain.ConfluenceCommentPartialEndpointUnavailable, "", selector, true, true)
					builder.setSelectorCapability(selector, capability)
					break
				}
				return domain.ConfluenceCommentInventory{}, err
			}
			builder.pages++
			if response.Results == nil {
				return domain.ConfluenceCommentInventory{}, fmt.Errorf("%w: Confluence comment response omitted results", domain.ErrCheckFailed)
			}
			rows := *response.Results
			pageQualified := response.Start != nil && *response.Start == start &&
				response.Size != nil && *response.Size == len(rows) &&
				response.Limit != nil && *response.Limit > 0 && response.Links != nil
			for _, raw := range rows {
				if builder.items >= itemLimit {
					builder.partial(domain.ConfluenceCommentPartialItemLimit, "", selector, true, true)
					stop = true
					break
				}
				builder.items++
				locationRows++
				record, mappedSelector, mapErr := builder.mapComment(id, selector, raw)
				if mapErr != nil {
					return domain.ConfluenceCommentInventory{}, mapErr
				}
				if commentSelectorMatchesRecord(selector, mappedSelector, record) {
					locationObserved = true
				} else {
					builder.partial(domain.ConfluenceCommentPartialLocationUnavailable, record.ID, selector, true, false)
					builder.setSelectorCapability(selector, domain.ConfluenceCapabilityUnknown)
				}
				builder.merge(record, selector)
			}
			if stop {
				break
			}
			if !pageQualified {
				builder.partial(domain.ConfluenceCommentPartialPaginationUnqualified, "", selector, true, true)
				break
			}
			if response.Links.Next == "" {
				break
			}
			if len(rows) == 0 {
				builder.partial(domain.ConfluenceCommentPartialPaginationStalled, "", selector, true, true)
				break
			}
			start += len(rows)
		}
		if locationRows > 0 {
			if locationObserved {
				builder.setSelectorCapability(selector, domain.ConfluenceCapabilityObserved)
			} else {
				builder.setSelectorCapability(selector, domain.ConfluenceCapabilityUnknown)
			}
		}
	}
	return builder.finish()
}

// commentSelectorMatchesRecord qualifies the requested selector from response
// evidence rather than assuming that the response repeats the query value.
// Some Confluence Data Center versions return resolved discussions with the
// semantic location "inline" and a separate explicit resolved state. That is
// sufficient for the resolved selector, while missing or contradictory state
// remains fail-closed.
func commentSelectorMatchesRecord(requested, mapped domain.ConfluenceCommentSelector, record domain.ConfluenceCommentRecord) bool {
	switch requested {
	case domain.ConfluenceCommentSelectorFooter:
		return mapped == domain.ConfluenceCommentSelectorFooter && record.Location == domain.ConfluenceCommentLocationFooter
	case domain.ConfluenceCommentSelectorInline:
		return mapped == domain.ConfluenceCommentSelectorInline && record.Location == domain.ConfluenceCommentLocationInline
	case domain.ConfluenceCommentSelectorResolved:
		return (mapped == domain.ConfluenceCommentSelectorInline || mapped == domain.ConfluenceCommentSelectorResolved) &&
			record.Location == domain.ConfluenceCommentLocationInline &&
			record.Resolution == domain.ConfluenceCommentResolutionResolved
	default:
		return false
	}
}

func commentAncestryObjectsPresent(comment domain.ConfluenceCommentRecord, byID map[string]domain.ConfluenceCommentRecord, conflicted map[string]struct{}) bool {
	for _, id := range []string{*comment.ParentID, *comment.RootID} {
		if _, present := byID[id]; !present {
			return false
		}
		if _, conflict := conflicted[id]; conflict {
			return false
		}
	}
	return true
}

func consistentCommentAncestry(comment domain.ConfluenceCommentRecord, byID map[string]domain.ConfluenceCommentRecord) bool {
	rootID := *comment.RootID
	root := byID[rootID]
	if root.ID != rootID || root.Relation != domain.ConfluenceCommentRelationRoot || root.RootID == nil || *root.RootID != rootID {
		return false
	}
	seen := map[string]struct{}{comment.ID: {}}
	current := comment
	for current.Relation == domain.ConfluenceCommentRelationReply && current.ParentID != nil {
		parentID := *current.ParentID
		if _, duplicate := seen[parentID]; duplicate {
			return false
		}
		seen[parentID] = struct{}{}
		parent, present := byID[parentID]
		if !present {
			return false
		}
		if parentID == rootID {
			return parent.Relation == domain.ConfluenceCommentRelationRoot
		}
		if parent.Relation != domain.ConfluenceCommentRelationReply || parent.RootID == nil || *parent.RootID != rootID {
			return false
		}
		current = parent
	}
	return false
}

func confluenceCommentSelectorErrorCapability(err error) (domain.ConfluenceCapabilityStatus, bool) {
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) {
		return "", false
	}
	switch apiErr.Status {
	case http.StatusBadRequest:
		// The request contains selector, depth and expansion parameters, so a
		// generic 400 cannot identify which capability was rejected.
		return domain.ConfluenceCapabilityUnknown, true
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return domain.ConfluenceCapabilityUnsupported, true
	}
	return "", false
}

func selectedCommentSelectors(requested []domain.ConfluenceCommentSelector) ([]domain.ConfluenceCommentSelector, error) {
	if len(requested) == 0 {
		return append([]domain.ConfluenceCommentSelector(nil), canonicalCommentSelectors...), nil
	}
	selected := make(map[domain.ConfluenceCommentSelector]struct{}, len(requested))
	for _, selector := range requested {
		if !domain.ValidConfluenceCommentSelector(selector) {
			return nil, fmt.Errorf("%w: invalid Confluence comment location", domain.ErrUsage)
		}
		selected[selector] = struct{}{}
	}
	out := make([]domain.ConfluenceCommentSelector, 0, len(selected))
	for _, selector := range canonicalCommentSelectors {
		if _, ok := selected[selector]; ok {
			out = append(out, selector)
		}
	}
	return out, nil
}

func (b *commentInventoryBuilder) setSelectorCapability(selector domain.ConfluenceCommentSelector, status domain.ConfluenceCapabilityStatus) {
	switch selector {
	case domain.ConfluenceCommentSelectorFooter:
		b.inventory.Capabilities.Footer = status
	case domain.ConfluenceCommentSelectorInline:
		b.inventory.Capabilities.Inline = status
	case domain.ConfluenceCommentSelectorResolved:
		b.inventory.Capabilities.Resolved = status
	}
}

func (b *commentInventoryBuilder) mapComment(pageID string, querySelector domain.ConfluenceCommentSelector, raw commentJSON) (domain.ConfluenceCommentRecord, domain.ConfluenceCommentSelector, error) {
	if strings.TrimSpace(raw.ID) == "" || raw.Type != "comment" {
		return domain.ConfluenceCommentRecord{}, "", fmt.Errorf("%w: Confluence comment response contains an invalid identity", domain.ErrCheckFailed)
	}
	record := domain.ConfluenceCommentRecord{
		ID: raw.ID, PageID: pageID, Relation: domain.ConfluenceCommentRelationUnknown,
		Location:   domain.ConfluenceCommentLocationUnknown,
		Resolution: domain.ConfluenceCommentResolutionUnknown,
	}
	if raw.History == nil || raw.History.CreatedBy == nil || raw.History.CreatedDate == "" || raw.Version == nil || raw.Version.Number <= 0 {
		b.partial(domain.ConfluenceCommentPartialMetadataUnavailable, raw.ID, querySelector, true, false)
	} else {
		record.CreatedAt = raw.History.CreatedDate
		record.AuthorDisplayName = raw.History.CreatedBy.DisplayName
		record.AuthorID = firstNonEmpty(raw.History.CreatedBy.UserKey, raw.History.CreatedBy.Username)
		record.Version = raw.Version.Number
		record.UpdatedAt = raw.Version.When
	}
	if raw.Body == nil || raw.Body.Storage == nil || raw.Body.Storage.Representation == nil || *raw.Body.Storage.Representation != "storage" {
		b.partial(domain.ConfluenceCommentPartialBodyUnavailable, raw.ID, querySelector, true, false)
	} else {
		record.BodyStorage = raw.Body.Storage.Value
		record.Body = record.BodyStorage
		if root, err := csf.Parse([]byte(record.BodyStorage)); err == nil {
			record.Body = csf.TextContent(root)
		}
	}
	location, locationSelector, impliedResolution, ok := decodeCommentLocation(raw.Extensions["location"])
	if !ok {
		b.partial(domain.ConfluenceCommentPartialLocationUnavailable, raw.ID, querySelector, true, false)
	} else {
		record.Location = location
	}
	resolution, resolutionPresent, resolutionOK := decodeCommentResolution(raw.Extensions["resolution"])
	if resolutionOK {
		if impliedResolution != "" && resolution != impliedResolution {
			record.Resolution = domain.ConfluenceCommentResolutionUnknown
			b.partial(domain.ConfluenceCommentPartialResolutionUnavailable, raw.ID, querySelector, true, false)
			b.inventory.Capabilities.Resolution = domain.ConfluenceCapabilityUnknown
		} else {
			record.Resolution = resolution
			b.inventory.Capabilities.Resolution = domain.ConfluenceCapabilityObserved
		}
	} else if resolutionPresent {
		record.Resolution = domain.ConfluenceCommentResolutionUnknown
		b.partial(domain.ConfluenceCommentPartialResolutionUnavailable, raw.ID, querySelector, true, false)
		b.inventory.Capabilities.Resolution = domain.ConfluenceCapabilityUnknown
	} else if impliedResolution != "" {
		// A literal backend location of "resolved" is exact response evidence
		// for a resolved inline discussion, not an inference from the query selector.
		record.Resolution = impliedResolution
		b.inventory.Capabilities.Resolution = domain.ConfluenceCapabilityObserved
	} else if record.Location == domain.ConfluenceCommentLocationInline ||
		querySelector == domain.ConfluenceCommentSelectorInline || querySelector == domain.ConfluenceCommentSelectorResolved {
		b.partial(domain.ConfluenceCommentPartialResolutionUnavailable, raw.ID, querySelector, true, false)
		b.inventory.Capabilities.Resolution = domain.ConfluenceCapabilityUnknown
	}
	relation, parent, root, relationStatus := commentRelationship(raw.ID, raw.Ancestors)
	record.Relation, record.ParentID, record.RootID = relation, parent, root
	switch relationStatus {
	case "missing":
		b.partial(domain.ConfluenceCommentPartialParentUnavailable, raw.ID, querySelector, false, true)
	case "malformed":
		b.partial(domain.ConfluenceCommentPartialMalformedAncestry, raw.ID, querySelector, false, true)
	case "reply":
		b.inventory.Capabilities.ThreadAncestry = domain.ConfluenceCapabilityObserved
		b.inventory.Capabilities.DepthAll = domain.ConfluenceCapabilityObserved
	}
	// Inline marker metadata belongs to the root discussion. Proven replies are
	// qualified by explicit ancestry and do not need or expose a synthetic copy
	// of their root's inline properties. Unknown ancestry stays fail-closed
	// because the record may still be an unqualified root.
	properties, inlinePropertiesPresent := decodeInlineProperties(raw.Extensions["inlineProperties"])
	inlinePropertiesPresent = inlinePropertiesPresent && strings.TrimSpace(properties.MarkerRef) != ""
	if inlinePropertiesPresent && relation != domain.ConfluenceCommentRelationReply {
		record.MarkerRef = properties.MarkerRef
		record.OriginalSelection = properties.OriginalSelection
	}
	if relation == domain.ConfluenceCommentRelationReply {
		b.observeReplyInlineProperties(raw.ID, properties, inlinePropertiesPresent)
	} else {
		if inlinePropertiesPresent {
			if b.inventory.Capabilities.InlineProperties != domain.ConfluenceCapabilityUnknown {
				b.inventory.Capabilities.InlineProperties = domain.ConfluenceCapabilityObserved
			}
		} else if record.Location == domain.ConfluenceCommentLocationInline ||
			querySelector == domain.ConfluenceCommentSelectorInline || querySelector == domain.ConfluenceCommentSelectorResolved {
			b.partial(domain.ConfluenceCommentPartialInlineExpansionUnavailable, raw.ID, querySelector, false, false)
			b.inventory.Capabilities.InlineProperties = domain.ConfluenceCapabilityUnknown
		}
	}
	return record, locationSelector, nil
}

func (b *commentInventoryBuilder) observeReplyInlineProperties(id string, properties commentInlinePropertiesJSON, present bool) {
	if !present {
		return
	}
	if _, conflicting := b.replyInlinePropertiesConflicts[id]; conflicting {
		return
	}
	prior, exists := b.replyInlineProperties[id]
	if !exists {
		b.replyInlineProperties[id] = properties
		return
	}
	if prior != properties {
		delete(b.replyInlineProperties, id)
		b.replyInlinePropertiesConflicts[id] = struct{}{}
	}
}

func (b *commentInventoryBuilder) merge(record domain.ConfluenceCommentRecord, querySelector domain.ConfluenceCommentSelector) {
	if _, conflict := b.conflicted[record.ID]; conflict {
		return
	}
	prior, exists := b.byID[record.ID]
	if !exists {
		b.byID[record.ID] = record
		return
	}
	if reflect.DeepEqual(prior, record) {
		return
	}
	delete(b.byID, record.ID)
	b.conflicted[record.ID] = struct{}{}
	b.partial(domain.ConfluenceCommentPartialConflictingDuplicates, record.ID, querySelector, true, true)
}

func decodeCommentLocation(raw json.RawMessage) (domain.ConfluenceCommentLocation, domain.ConfluenceCommentSelector, domain.ConfluenceCommentResolution, bool) {
	var value string
	if !decodeCommentExtension(raw, &value) {
		return domain.ConfluenceCommentLocationUnknown, "", "", false
	}
	switch value {
	case string(domain.ConfluenceCommentSelectorFooter):
		return domain.ConfluenceCommentLocationFooter, domain.ConfluenceCommentSelectorFooter, "", true
	case string(domain.ConfluenceCommentSelectorInline):
		return domain.ConfluenceCommentLocationInline, domain.ConfluenceCommentSelectorInline, "", true
	case string(domain.ConfluenceCommentSelectorResolved):
		return domain.ConfluenceCommentLocationInline, domain.ConfluenceCommentSelectorResolved, domain.ConfluenceCommentResolutionResolved, true
	}
	return domain.ConfluenceCommentLocationUnknown, "", "", false
}

func decodeCommentResolution(raw json.RawMessage) (domain.ConfluenceCommentResolution, bool, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return domain.ConfluenceCommentResolutionUnknown, false, false
	}
	var value commentResolutionJSON
	if bytes.Equal(trimmed, []byte("null")) || json.Unmarshal(trimmed, &value) != nil {
		return domain.ConfluenceCommentResolutionUnknown, true, false
	}
	switch value.Status {
	case string(domain.ConfluenceCommentResolutionOpen), "reopened":
		return domain.ConfluenceCommentResolutionOpen, true, true
	case string(domain.ConfluenceCommentResolutionResolved):
		return domain.ConfluenceCommentResolutionResolved, true, true
	default:
		return domain.ConfluenceCommentResolutionUnknown, true, false
	}
}

func decodeInlineProperties(raw json.RawMessage) (commentInlinePropertiesJSON, bool) {
	var value commentInlinePropertiesJSON
	return value, decodeCommentExtension(raw, &value)
}

func decodeCommentExtension(raw json.RawMessage, out any) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	return json.Unmarshal(trimmed, out) == nil
}

// commentRelationship uses only explicit comment ancestors. It never consults
// response order or the selected location query.
func commentRelationship(id string, ancestors *[]commentContentIdentityJSON) (domain.ConfluenceCommentRelation, *string, *string, string) {
	if ancestors == nil {
		return domain.ConfluenceCommentRelationUnknown, nil, nil, "missing"
	}
	commentIDs := make([]string, 0, len(*ancestors))
	seen := make(map[string]struct{}, len(*ancestors))
	for _, ancestor := range *ancestors {
		if ancestor.Type != "comment" {
			continue
		}
		if strings.TrimSpace(ancestor.ID) == "" || ancestor.ID == id {
			return domain.ConfluenceCommentRelationUnknown, nil, nil, "malformed"
		}
		if _, duplicate := seen[ancestor.ID]; duplicate {
			return domain.ConfluenceCommentRelationUnknown, nil, nil, "malformed"
		}
		seen[ancestor.ID] = struct{}{}
		commentIDs = append(commentIDs, ancestor.ID)
	}
	if len(commentIDs) == 0 {
		root := id
		return domain.ConfluenceCommentRelationRoot, nil, &root, "root"
	}
	root, parent := commentIDs[0], commentIDs[len(commentIDs)-1]
	return domain.ConfluenceCommentRelationReply, &parent, &root, "reply"
}

var _ domain.QualifiedConfluenceCommentReader = (*Confluence)(nil)
