package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	jiraRemoteLinkMaxObjectURLBytes       = 2048
	jiraRemoteLinkMaxGlobalIDBytes        = 2048
	jiraRemoteLinkMaxApplicationTypeBytes = 256
)

// ReadIssueSnapshot fetches all fields, properties, names, and schemas in one
// permission-relative Jira request. It does not consult the global field
// catalog, whose visibility and applicability can differ from the issue.
func (j *Jira) ReadIssueSnapshot(ctx context.Context, key string) (*domain.QualifiedIssueSnapshot, error) {
	return j.readIssueSnapshot(ctx, key, domain.IssueSnapshotProjection{Fields: []string{"*all"}, Properties: true})
}

// ReadIssueSnapshotProjection uses the same qualified issue read as the legacy
// snapshot, with explicit fields and no unselected property expansion.
func (j *Jira) ReadIssueSnapshotProjection(ctx context.Context, key string, projection domain.IssueSnapshotProjection) (*domain.QualifiedIssueSnapshot, error) {
	validFields := slices.Equal(projection.Fields, []string{"*all"}) ||
		slices.Equal(projection.Fields, []string{"summary"}) ||
		slices.Equal(projection.Fields, []string{"summary", "issuelinks"}) ||
		slices.Equal(projection.Fields, []string{"summary", "attachment"}) ||
		slices.Equal(projection.Fields, []string{"summary", "issuelinks", "attachment"})
	if !validFields || projection.SupportingFieldsReason != "" &&
		(projection.SupportingFieldsReason != "hierarchy_discovery" || !slices.Equal(projection.Fields, []string{"*all"})) {
		return nil, fmt.Errorf("%w: Jira graph snapshot projection is invalid", domain.ErrUsage)
	}
	return j.readIssueSnapshot(ctx, key, projection)
}

func (j *Jira) readIssueSnapshot(ctx context.Context, key string, projection domain.IssueSnapshotProjection) (*domain.QualifiedIssueSnapshot, error) {
	query := url.Values{}
	query.Set("expand", "names,schema")
	query.Set("fields", strings.Join(projection.Fields, ","))
	if projection.Properties {
		query.Set("properties", "*all")
	}
	var response struct {
		ID         string                              `json:"id"`
		Key        string                              `json:"key"`
		Fields     *map[string]any                     `json:"fields"`
		Names      *map[string]string                  `json:"names"`
		Schema     *map[string]domain.IssueFieldSchema `json:"schema"`
		Properties json.RawMessage                     `json:"properties"`
	}
	path := "/rest/api/2/issue/" + url.PathEscape(strings.TrimSpace(key)) + "?" + query.Encode()
	if err := j.c.GetJSONUseNumber(domain.WithSingleAttempt(ctx), path, &response); err != nil {
		return nil, err
	}
	if response.Fields == nil || response.Names == nil || response.Schema == nil {
		return nil, fmt.Errorf("%w: Jira issue snapshot omitted a requested section", domain.ErrCheckFailed)
	}
	properties := map[string]any{}
	if projection.Properties {
		decoder := json.NewDecoder(bytes.NewReader(response.Properties))
		decoder.UseNumber()
		if err := decoder.Decode(&properties); err != nil || properties == nil {
			return nil, fmt.Errorf("%w: Jira issue snapshot omitted a requested section", domain.ErrCheckFailed)
		}
	}
	issue := MapIssueFields(response.ID, response.Key, *response.Fields)
	return &domain.QualifiedIssueSnapshot{
		RequestedKey: strings.TrimSpace(key),
		ID:           response.ID,
		Key:          response.Key,
		Issue:        *issue,
		Fields:       *response.Fields,
		Names:        *response.Names,
		Schema:       *response.Schema,
		Properties:   properties,
	}, nil
}

// ReadIssueRemoteLinks reads Jira's supported non-paginated remote-link
// endpoint. URL safety and graph identity normalization remain app concerns.
func (j *Jira) ReadIssueRemoteLinks(ctx context.Context, key string) (domain.JiraRemoteLinkInventory, error) {
	var response []struct {
		ID           string          `json:"id"`
		GlobalID     json.RawMessage `json:"globalId"`
		Relationship string          `json:"relationship"`
		Application  json.RawMessage `json:"application"`
		Object       struct {
			URL   json.RawMessage `json:"url"`
			Title string          `json:"title"`
		} `json:"object"`
	}
	path := "/rest/api/2/issue/" + url.PathEscape(strings.TrimSpace(key)) + "/remotelink"
	if err := j.c.GetJSONUseNumber(domain.WithSingleAttempt(ctx), path, &response); err != nil {
		return domain.JiraRemoteLinkInventory{}, err
	}
	if response == nil {
		return domain.JiraRemoteLinkInventory{}, fmt.Errorf("%w: Jira remote-link response omitted its collection", domain.ErrCheckFailed)
	}
	out := domain.JiraRemoteLinkInventory{Links: []domain.JiraRemoteLink{}, Total: len(response)}
	seen := make(map[string]bool, len(response))
	for _, link := range response {
		objectURL, objectURLOK := decodeJiraRemoteLinkMetadata(link.Object.URL)
		globalID, globalIDOK := decodeJiraRemoteLinkMetadata(link.GlobalID)
		applicationType, applicationTypeOK := decodeJiraRemoteLinkApplicationType(link.Application)
		parsed, err := url.Parse(objectURL)
		numericID, idErr := strconv.ParseInt(link.ID, 10, 64)
		if idErr != nil || numericID <= 0 || seen[link.ID] || err != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Hostname() == "" || parsed.User != nil ||
			!objectURLOK || !globalIDOK || !applicationTypeOK ||
			!validJiraRemoteLinkMetadata(objectURL, jiraRemoteLinkMaxObjectURLBytes) ||
			!validJiraRemoteLinkMetadata(globalID, jiraRemoteLinkMaxGlobalIDBytes) ||
			!validJiraRemoteLinkMetadata(applicationType, jiraRemoteLinkMaxApplicationTypeBytes) {
			out.Unsupported++
			continue
		}
		seen[link.ID] = true
		out.Links = append(out.Links, domain.JiraRemoteLink{
			ID:              link.ID,
			Relationship:    link.Relationship,
			ObjectURL:       objectURL,
			ObjectTitle:     link.Object.Title,
			GlobalID:        globalID,
			ApplicationType: applicationType,
		})
	}
	return out, nil
}

// decodeJiraRemoteLinkMetadata keeps malformed metadata local to its row. Raw
// JSON is checked before Go's JSON decoder can replace invalid UTF-8 bytes.
func decodeJiraRemoteLinkMetadata(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true
	}
	if !utf8.Valid(raw) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !utf8.ValidString(value) {
		return "", false
	}
	return value, true
}

func decodeJiraRemoteLinkApplicationType(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true
	}
	if !utf8.Valid(raw) {
		return "", false
	}
	var application struct {
		Type json.RawMessage `json:"type"`
	}
	if err := json.Unmarshal(raw, &application); err != nil {
		return "", false
	}
	return decodeJiraRemoteLinkMetadata(application.Type)
}

// validJiraRemoteLinkMetadata admits omitted optional metadata while keeping
// populated URL and identity values bounded and safe for graph processing.
func validJiraRemoteLinkMetadata(value string, maxBytes int) bool {
	if len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return false
		}
	}
	return true
}
