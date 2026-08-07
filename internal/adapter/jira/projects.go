package jira

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

// ReadProjects returns the atomic Jira Data Center inventory visible to the
// authenticated caller. The endpoint is not paginated.
func (j *Jira) ReadProjects(ctx context.Context, includeArchived bool) ([]domain.JiraProject, error) {
	path := "/rest/api/2/project"
	if includeArchived {
		path += "?includeArchived=true"
	}
	var raw []struct {
		ID             string `json:"id"`
		Key            string `json:"key"`
		Name           string `json:"name"`
		ProjectTypeKey string `json:"projectTypeKey"`
		Archived       *bool  `json:"archived"`
	}
	if err := j.c.GetJSON(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make([]domain.JiraProject, 0, len(raw))
	for _, project := range raw {
		if project.ID == "" || project.Key == "" || project.Name == "" {
			return nil, fmt.Errorf("%w: Jira project inventory contains an incomplete row", domain.ErrCheckFailed)
		}
		out = append(out, domain.JiraProject{
			ID: project.ID, Key: project.Key, Name: project.Name,
			ProjectTypeKey: project.ProjectTypeKey, Archived: project.Archived,
		})
	}
	return out, nil
}
