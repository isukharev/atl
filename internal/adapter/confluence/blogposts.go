package confluence

import (
	"context"
	"net/http"
	"net/url"

	"github.com/isukharev/atl/internal/domain"
)

var _ domain.BlogPostCreator = (*Confluence)(nil)

// CreateBlogPost creates native Confluence blog content. The content type is
// closed here rather than accepted from a caller, and blog posts never carry a
// page ancestor. expand makes the write response independently verifiable.
func (cf *Confluence) CreateBlogPost(ctx context.Context, space, title string, body []byte) (*domain.Resource, error) {
	payload := map[string]any{
		"type":  "blogpost",
		"title": title,
		"space": map[string]string{"key": space},
		"body": map[string]any{
			"storage": map[string]any{"value": string(body), "representation": "storage"},
		},
	}
	query := url.Values{}
	query.Set("expand", "body.storage,version,space")
	var out content
	if err := cf.c.SendJSON(ctx, http.MethodPost, "/rest/api/content?"+query.Encode(), payload, &out); err != nil {
		return nil, err
	}
	bodyValue, present := out.storageBody()
	resource := out.toResource(cf.base, bodyValue)
	resource.BodyPresent = present
	return resource, nil
}
