package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	inverseReferenceMaxResults          = jiraSearchMaxResults
	inverseReferenceMaxFieldIDs         = 129 // 128 explicit fields plus the app's implicit description field.
	inverseReferenceMaxProperties       = 128
	inverseReferenceMaxValueBytes       = 64 << 10
	inverseReferenceMaxFieldIDBytes     = 255
	inverseReferenceMaxPropertyKeyBytes = 255
)

var _ domain.JiraInverseReferenceSelector = (*Jira)(nil)
var _ domain.JiraInverseReferenceSnapshotReader = (*Jira)(nil)

// SelectInverseReferencePage executes the caller-qualified JQL for one exact
// page. Candidate matching and JQL composition belong to the app; this adapter
// retains only Jira's pagination coordinates and issue identities.
func (j *Jira) SelectInverseReferencePage(ctx context.Context, selection domain.JiraInverseReferenceSelection) (domain.JiraInverseReferencePage, error) {
	if selection.StartAt < 0 || selection.MaxResults <= 0 || selection.MaxResults > inverseReferenceMaxResults {
		return domain.JiraInverseReferencePage{}, fmt.Errorf("%w: inverse-reference page coordinates are outside the supported bounds", domain.ErrUsage)
	}
	query := url.Values{}
	query.Set("jql", selection.JQL)
	query.Set("startAt", strconv.Itoa(selection.StartAt))
	query.Set("maxResults", strconv.Itoa(selection.MaxResults))
	// id and key are top-level issue members. Requesting only key prevents
	// Jira's default issue-field projection from widening this bounded read.
	query.Set("fields", "key")
	var response struct {
		StartAt    *int `json:"startAt"`
		MaxResults *int `json:"maxResults"`
		Total      *int `json:"total"`
		Issues     []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"issues"`
	}
	if err := j.c.GetJSONUseNumber(domain.WithSingleAttempt(ctx), "/rest/api/2/search?"+query.Encode(), &response); err != nil {
		return domain.JiraInverseReferencePage{}, err
	}
	if response.StartAt == nil || response.MaxResults == nil || response.Total == nil {
		return domain.JiraInverseReferencePage{}, fmt.Errorf("%w: Jira inverse-reference search omitted pagination coordinates", domain.ErrCheckFailed)
	}
	if response.Issues == nil {
		return domain.JiraInverseReferencePage{}, fmt.Errorf("%w: Jira inverse-reference search omitted its issue collection", domain.ErrCheckFailed)
	}
	page := domain.JiraInverseReferencePage{
		StartAt: *response.StartAt, MaxResults: *response.MaxResults, Total: *response.Total,
		Issues: make([]domain.JiraInverseReferenceIssueIdentity, 0, len(response.Issues)),
	}
	for _, issue := range response.Issues {
		page.Issues = append(page.Issues, domain.JiraInverseReferenceIssueIdentity{ID: issue.ID, Key: issue.Key})
	}
	return page, nil
}

// ReadInverseReferenceSnapshot fetches only the caller-selected issue fields.
// Properties are a separate opt-in source and are never fetched by default.
func (j *Jira) ReadInverseReferenceSnapshot(ctx context.Context, request domain.JiraInverseReferenceSnapshotRequest) (domain.JiraInverseReferenceSnapshot, error) {
	if strings.TrimSpace(request.Issue.Key) == "" || len(request.FieldIDs) > inverseReferenceMaxFieldIDs {
		return domain.JiraInverseReferenceSnapshot{}, fmt.Errorf("%w: inverse-reference snapshot request is outside the supported bounds", domain.ErrUsage)
	}
	fieldIDs := make([]string, len(request.FieldIDs))
	seenFields := make(map[string]bool, len(request.FieldIDs))
	for index, fieldID := range request.FieldIDs {
		if !validInverseReferenceIdentifier(fieldID, inverseReferenceMaxFieldIDBytes) ||
			strings.Contains(fieldID, ",") || fieldID == "*all" || fieldID == "-*all" || seenFields[fieldID] {
			return domain.JiraInverseReferenceSnapshot{}, fmt.Errorf("%w: inverse-reference field selection is invalid", domain.ErrUsage)
		}
		seenFields[fieldID] = true
		fieldIDs[index] = fieldID
	}
	query := url.Values{}
	// Do not let an empty selection fall back to Jira's default projection.
	// key is Jira's narrow documented sentinel for property-only reads; it is a
	// top-level member and is never retained as a requested field snapshot.
	fieldsQuery := strings.Join(fieldIDs, ",")
	if fieldsQuery == "" {
		fieldsQuery = "key"
	}
	query.Set("fields", fieldsQuery)
	if request.IncludeProperties {
		query.Set("properties", "*all")
	}
	var response struct {
		ID         string                      `json:"id"`
		Key        string                      `json:"key"`
		Fields     *map[string]json.RawMessage `json:"fields"`
		Properties *map[string]json.RawMessage `json:"properties"`
	}
	path := "/rest/api/2/issue/" + url.PathEscape(strings.TrimSpace(request.Issue.Key)) + "?" + query.Encode()
	if err := j.c.GetJSONUseNumber(domain.WithSingleAttempt(ctx), path, &response); err != nil {
		return domain.JiraInverseReferenceSnapshot{}, err
	}
	if response.Fields == nil || (request.IncludeProperties && response.Properties == nil) {
		return domain.JiraInverseReferenceSnapshot{}, fmt.Errorf("%w: Jira inverse-reference snapshot omitted a requested section", domain.ErrCheckFailed)
	}
	if response.Key != request.Issue.Key || (request.Issue.ID != "" && response.ID != request.Issue.ID) {
		return domain.JiraInverseReferenceSnapshot{}, fmt.Errorf("%w: Jira inverse-reference snapshot returned a different issue", domain.ErrCheckFailed)
	}
	out := domain.JiraInverseReferenceSnapshot{
		Issue:  request.Issue,
		Fields: make([]domain.JiraInverseReferenceFieldSnapshot, 0, len(fieldIDs)),
	}
	for _, fieldID := range fieldIDs {
		value, present := (*response.Fields)[fieldID]
		if present && !validInverseReferenceRawValue(value) {
			return domain.JiraInverseReferenceSnapshot{}, fmt.Errorf("%w: Jira inverse-reference snapshot field value exceeds the supported bound", domain.ErrCheckFailed)
		}
		out.Fields = append(out.Fields, domain.JiraInverseReferenceFieldSnapshot{FieldID: fieldID, Present: present, Value: append(json.RawMessage(nil), value...)})
	}
	if !request.IncludeProperties {
		return out, nil
	}
	if len(*response.Properties) > inverseReferenceMaxProperties {
		return domain.JiraInverseReferenceSnapshot{}, fmt.Errorf("%w: Jira inverse-reference snapshot properties exceed the supported bound", domain.ErrCheckFailed)
	}
	keys := make([]string, 0, len(*response.Properties))
	for key := range *response.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out.Properties = make([]domain.JiraInverseReferencePropertySnapshot, 0, len(keys))
	for _, key := range keys {
		value := (*response.Properties)[key]
		if !validInverseReferenceIdentifier(key, inverseReferenceMaxPropertyKeyBytes) || !validInverseReferenceRawValue(value) {
			return domain.JiraInverseReferenceSnapshot{}, fmt.Errorf("%w: Jira inverse-reference snapshot property exceeds the supported bound", domain.ErrCheckFailed)
		}
		out.Properties = append(out.Properties, domain.JiraInverseReferencePropertySnapshot{Key: key, Value: append(json.RawMessage(nil), value...)})
	}
	return out, nil
}

func validInverseReferenceRawValue(value json.RawMessage) bool {
	return len(value) <= inverseReferenceMaxValueBytes && utf8.Valid(value) && json.Valid(value)
}

func validInverseReferenceIdentifier(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return false
		}
	}
	return true
}
