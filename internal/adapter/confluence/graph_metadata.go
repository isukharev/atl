package confluence

import (
	"context"
	"fmt"
	"net/url"

	"github.com/isukharev/atl/internal/domain"
)

var _ domain.ConfluenceGraphPageMetadataReader = (*Confluence)(nil)

const confluenceGraphPageIDMaxDigits = 32

// ReadGraphPageMetadata reads only the canonical id and title for one exact
// numeric page id. The request is always relative to the adapter's configured
// Confluence backend and is forced to a single transport attempt.
func (cf *Confluence) ReadGraphPageMetadata(ctx context.Context, id string) (domain.ConfluenceGraphPageMetadata, error) {
	if !canonicalConfluenceGraphPageID(id) {
		return domain.ConfluenceGraphPageMetadata{}, fmt.Errorf("%w: Confluence graph page id must be a canonical positive integer", domain.ErrUsage)
	}
	var response struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	requestPath := "/rest/api/content/" + url.PathEscape(id)
	if err := cf.c.GetJSON(domain.WithSingleAttempt(ctx), requestPath, &response); err != nil {
		return domain.ConfluenceGraphPageMetadata{}, err
	}
	if response.ID == "" || response.ID != id {
		return domain.ConfluenceGraphPageMetadata{}, fmt.Errorf("%w: Confluence graph metadata identity does not match the requested page", domain.ErrCheckFailed)
	}
	return domain.ConfluenceGraphPageMetadata{ID: response.ID, Title: response.Title}, nil
}

func canonicalConfluenceGraphPageID(id string) bool {
	if len(id) == 0 || len(id) > confluenceGraphPageIDMaxDigits || id[0] == '0' {
		return false
	}
	for index := range len(id) {
		if id[index] < '0' || id[index] > '9' {
			return false
		}
	}
	return true
}
