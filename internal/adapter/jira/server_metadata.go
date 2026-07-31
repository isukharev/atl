package jira

import (
	"context"

	"github.com/isukharev/atl/internal/domain"
)

// ServerMetadata reads Jira's product/version endpoint once and projects only
// the fields needed by environment diagnostics. Product is static so backend
// response fields cannot replace it with arbitrary text.
func (j *Jira) ServerMetadata(ctx context.Context) (domain.ServerMetadata, error) {
	var response struct {
		DeploymentType string `json:"deploymentType"`
		Version        string `json:"version"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/serverInfo", &response); err != nil {
		return domain.ServerMetadata{}, err
	}
	return domain.ServerMetadata{
		Product:        domain.ServerProductJira,
		DeploymentType: response.DeploymentType,
		Version:        response.Version,
	}, nil
}
