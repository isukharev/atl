package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// ServerMetadata reads Jira's product/version endpoint once and projects only
// the fields needed by environment diagnostics. Product is static so backend
// response fields cannot replace it with arbitrary text.
func (j *Jira) ServerMetadata(ctx context.Context) (domain.ServerMetadata, error) {
	var response struct {
		DeploymentType string          `json:"deploymentType"`
		Version        string          `json:"version"`
		BuildNumber    json.RawMessage `json:"buildNumber"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/serverInfo", &response); err != nil {
		return domain.ServerMetadata{}, err
	}
	buildNumber, _ := jiraBuildNumber(response.BuildNumber)
	return domain.ServerMetadata{
		Product:        domain.ServerProductJira,
		DeploymentType: response.DeploymentType,
		Version:        response.Version,
		BuildNumber:    buildNumber,
	}, nil
}

// ExactServerMetadata uses Jira's documented serverInfo identity. Validation
// of the projected release/build tuple belongs to the compatibility boundary.
func (j *Jira) ExactServerMetadata(ctx context.Context) (domain.ServerMetadata, error) {
	var response struct {
		DeploymentType string          `json:"deploymentType"`
		Version        string          `json:"version"`
		BuildNumber    json.RawMessage `json:"buildNumber"`
	}
	if err := j.c.GetJSON(ctx, "/rest/api/2/serverInfo", &response); err != nil {
		return domain.ServerMetadata{}, err
	}
	buildNumber, err := jiraBuildNumber(response.BuildNumber)
	if err != nil {
		return domain.ServerMetadata{}, err
	}
	metadata := domain.ServerMetadata{
		Product: domain.ServerProductJira, DeploymentType: response.DeploymentType,
		Version: response.Version, BuildNumber: buildNumber,
	}
	if metadata.Version == "" || metadata.BuildNumber == "" {
		return domain.ServerMetadata{}, fmt.Errorf("%w: Jira exact product identity is unavailable", domain.ErrCheckFailed)
	}
	return metadata, nil
}

func jiraBuildNumber(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	value := strings.TrimSpace(string(raw))
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return "", fmt.Errorf("%w: Jira build identity is malformed", domain.ErrCheckFailed)
		}
		value = decoded
	}
	if !decimalBuildNumber(value) {
		return "", fmt.Errorf("%w: Jira build identity is malformed", domain.ErrCheckFailed)
	}
	return value, nil
}

func decimalBuildNumber(value string) bool {
	if len(value) == 0 || len(value) > 20 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
