package confluence

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/isukharev/atl/internal/domain"
)

var _ domain.ContentLabelStore = (*Confluence)(nil)

// ListContentLabels follows Confluence's offset pagination until exhaustion.
// A server that keeps advertising _links.next is bounded by the shared safety
// caps and reported as truncated rather than silently returning a prefix.
func (cf *Confluence) ListContentLabels(ctx context.Context, id string) ([]domain.ContentLabel, bool, error) {
	start := 0
	var out []domain.ContentLabel
	for page := 0; page < maxPages && len(out) < maxItems; page++ {
		var response struct {
			Results []domain.ContentLabel `json:"results"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		query := url.Values{}
		query.Set("limit", "100")
		query.Set("start", strconv.Itoa(start))
		path := "/rest/api/content/" + url.PathEscape(id) + "/label?" + query.Encode()
		if err := cf.c.GetJSON(ctx, path, &response); err != nil {
			return nil, false, err
		}
		remaining := maxItems - len(out)
		if len(response.Results) > remaining {
			out = append(out, response.Results[:remaining]...)
			return out, true, nil
		}
		out = append(out, response.Results...)
		if response.Links.Next == "" {
			return out, false, nil
		}
		start += len(response.Results)
		if len(response.Results) == 0 {
			return out, true, nil
		}
	}
	return out, true, nil
}

// AddContentLabels sends exactly one non-retried POST. Confluence DC accepts
// the JSON representation of a label list at the content label collection.
func (cf *Confluence) AddContentLabels(ctx context.Context, id string, labels []domain.ContentLabel) error {
	body, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	_, err = cf.c.Do(ctx, http.MethodPost, "/rest/api/content/"+url.PathEscape(id)+"/label", body, map[string]string{"Content-Type": "application/json"})
	return err
}

// RemoveContentLabel uses the query-parameter endpoint so names containing '/'
// never become path components. The DELETE is sent once and never replayed.
func (cf *Confluence) RemoveContentLabel(ctx context.Context, id, name string) error {
	query := url.Values{}
	query.Set("name", name)
	_, err := cf.c.Do(ctx, http.MethodDelete, "/rest/api/content/"+url.PathEscape(id)+"/label?"+query.Encode(), nil, nil)
	return err
}
