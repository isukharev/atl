package confluence

import (
	"context"

	"github.com/isukharev/atl/internal/domain"
)

// ServerMetadata reads Confluence's product/version endpoint once and
// projects only its version. Product is static so no backend-controlled product
// or deployment text crosses this adapter boundary.
func (cf *Confluence) ServerMetadata(ctx context.Context) (domain.ServerMetadata, error) {
	var response struct {
		Version string `json:"version"`
	}
	if err := cf.c.GetJSON(ctx, "/rest/api/server-information", &response); err != nil {
		return domain.ServerMetadata{}, err
	}
	return domain.ServerMetadata{Product: domain.ServerProductConfluence, Version: response.Version}, nil
}
