package confluence

import (
	"context"
	"errors"
	"net/http"

	"github.com/isukharev/atl/internal/domain"
)

// ServerMetadata reads Confluence's product/version endpoint and projects only
// its version. A legacy deployment that lacks that endpoint is qualified by one
// body-free content-collection probe. Product is static so no backend-controlled
// product or deployment text crosses this adapter boundary.
func (cf *Confluence) ServerMetadata(ctx context.Context) (domain.ServerMetadata, error) {
	var response struct {
		Version string `json:"version"`
	}
	if err := cf.c.GetJSON(ctx, "/rest/api/server-information", &response); err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return domain.ServerMetadata{}, err
		}
		if _, headErr := cf.c.Do(ctx, http.MethodHead, "/rest/api/content", nil, nil); headErr != nil {
			return domain.ServerMetadata{}, headErr
		}
		return domain.ServerMetadata{Product: domain.ServerProductConfluence}, nil
	}
	return domain.ServerMetadata{Product: domain.ServerProductConfluence, Version: response.Version}, nil
}
