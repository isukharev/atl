package jira

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// ReadIssueSnapshot fetches all fields, properties, names, and schemas in one
// permission-relative Jira request. It does not consult the global field
// catalog, whose visibility and applicability can differ from the issue.
func (j *Jira) ReadIssueSnapshot(ctx context.Context, key string) (*domain.QualifiedIssueSnapshot, error) {
	query := url.Values{}
	query.Set("expand", "names,schema")
	query.Set("fields", "*all")
	query.Set("properties", "*all")
	var response struct {
		ID         string                              `json:"id"`
		Key        string                              `json:"key"`
		Fields     *map[string]any                     `json:"fields"`
		Names      *map[string]string                  `json:"names"`
		Schema     *map[string]domain.IssueFieldSchema `json:"schema"`
		Properties *map[string]any                     `json:"properties"`
	}
	path := "/rest/api/2/issue/" + url.PathEscape(strings.TrimSpace(key)) + "?" + query.Encode()
	if err := j.c.GetJSONUseNumber(domain.WithSingleAttempt(ctx), path, &response); err != nil {
		return nil, err
	}
	if response.Fields == nil || response.Names == nil || response.Schema == nil || response.Properties == nil {
		return nil, fmt.Errorf("%w: Jira issue snapshot omitted a requested section", domain.ErrCheckFailed)
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
		Properties:   *response.Properties,
	}, nil
}

// ReadIssueRemoteLinks reads Jira's supported non-paginated remote-link
// endpoint. URL safety and graph identity normalization remain app concerns.
func (j *Jira) ReadIssueRemoteLinks(ctx context.Context, key string) (domain.JiraRemoteLinkInventory, error) {
	var response []struct {
		ID           string `json:"id"`
		Relationship string `json:"relationship"`
		Object       struct {
			URL   string `json:"url"`
			Title string `json:"title"`
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
		parsed, err := url.Parse(link.Object.URL)
		numericID, idErr := strconv.ParseInt(link.ID, 10, 64)
		if idErr != nil || numericID <= 0 || seen[link.ID] || err != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Hostname() == "" || parsed.User != nil {
			out.Unsupported++
			continue
		}
		seen[link.ID] = true
		out.Links = append(out.Links, domain.JiraRemoteLink{
			ID:           link.ID,
			Relationship: link.Relationship,
			ObjectURL:    link.Object.URL,
			ObjectTitle:  link.Object.Title,
		})
	}
	return out, nil
}
