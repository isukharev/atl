package jira

import (
	"context"
	"fmt"
	"net/url"
	"sort"

	"github.com/isukharev/atl/internal/domain"
)

const (
	createMetaPageSize = 200
	createMetaMaxItems = 1000
)

type createIssueTypeDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

type createFieldDTO struct {
	FieldID       string `json:"fieldId"`
	Name          string `json:"name"`
	Required      bool   `json:"required"`
	AllowedValues []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"allowedValues"`
}

func createMetaBase(project string) string {
	return "/rest/api/2/issue/createmeta/" + url.PathEscape(project) + "/issuetypes"
}

func (j *Jira) readCreateIssueTypes(ctx context.Context, project string) ([]createIssueTypeDTO, error) {
	var out []createIssueTypeDTO
	for start := 0; ; {
		var page struct {
			StartAt int                  `json:"startAt"`
			Total   int                  `json:"total"`
			IsLast  bool                 `json:"isLast"`
			Values  []createIssueTypeDTO `json:"values"`
		}
		path := fmt.Sprintf("%s?startAt=%d&maxResults=%d", createMetaBase(project), start, createMetaPageSize)
		if err := j.c.GetJSON(ctx, path, &page); err != nil {
			return nil, err
		}
		if page.Total > createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create metadata exceeds the 1000 item limit", domain.ErrCheckFailed)
		}
		out = append(out, page.Values...)
		if len(out) > createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create metadata exceeds the 1000 item limit", domain.ErrCheckFailed)
		}
		if page.IsLast || page.Total > 0 && len(out) >= page.Total || page.Total == 0 && len(page.Values) < createMetaPageSize {
			return out, nil
		}
		if len(out) >= createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create metadata exceeds the 1000 item limit", domain.ErrCheckFailed)
		}
		if len(page.Values) == 0 {
			return nil, fmt.Errorf("%w: Jira create metadata pagination made no progress", domain.ErrCheckFailed)
		}
		start += len(page.Values)
	}
}

func (j *Jira) readCreateFields(ctx context.Context, project, typeID string) ([]createFieldDTO, error) {
	var out []createFieldDTO
	base := createMetaBase(project) + "/" + url.PathEscape(typeID)
	for start := 0; ; {
		var page struct {
			StartAt int              `json:"startAt"`
			Total   int              `json:"total"`
			IsLast  bool             `json:"isLast"`
			Values  []createFieldDTO `json:"values"`
		}
		path := fmt.Sprintf("%s?startAt=%d&maxResults=%d", base, start, createMetaPageSize)
		if err := j.c.GetJSON(ctx, path, &page); err != nil {
			return nil, err
		}
		if page.Total > createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create-screen metadata exceeds the 1000 field limit", domain.ErrCheckFailed)
		}
		out = append(out, page.Values...)
		if len(out) > createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create-screen metadata exceeds the 1000 field limit", domain.ErrCheckFailed)
		}
		if page.IsLast || page.Total > 0 && len(out) >= page.Total || page.Total == 0 && len(page.Values) < createMetaPageSize {
			return out, nil
		}
		if len(out) >= createMetaMaxItems {
			return nil, fmt.Errorf("%w: Jira create-screen metadata exceeds the 1000 field limit", domain.ErrCheckFailed)
		}
		if len(page.Values) == 0 {
			return nil, fmt.Errorf("%w: Jira create-screen pagination made no progress", domain.ErrCheckFailed)
		}
		start += len(page.Values)
	}
}

func (j *Jira) ReadCreateIssueTypes(ctx context.Context, project string) ([]domain.JiraIssueType, error) {
	raw, err := j.readCreateIssueTypes(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]domain.JiraIssueType, 0, len(raw))
	for _, issueType := range raw {
		if issueType.ID == "" || issueType.Name == "" {
			return nil, fmt.Errorf("%w: Jira issue-type metadata contains an incomplete row", domain.ErrCheckFailed)
		}
		out = append(out, domain.JiraIssueType{ID: issueType.ID, Name: issueType.Name, Subtask: issueType.Subtask})
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].Name == out[k].Name {
			return out[i].ID < out[k].ID
		}
		return out[i].Name < out[k].Name
	})
	return out, nil
}

func (j *Jira) ReadCreateMetadata(ctx context.Context, project, selector string) (*domain.JiraCreateMetadata, error) {
	types, err := j.ReadCreateIssueTypes(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, issueType := range types {
		if issueType.ID == selector {
			return j.readCreateMetadataFields(ctx, project, issueType)
		}
	}
	var matches []domain.JiraIssueType
	for _, issueType := range types {
		if issueType.Name == selector {
			matches = append(matches, issueType)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: issue type was not found in project create metadata; run 'atl jira issue types --project PROJECT' and use an exact id or name", domain.ErrNotFound)
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("%w: issue type selector is ambiguous", domain.ErrCheckFailed)
	}
	return j.readCreateMetadataFields(ctx, project, matches[0])
}

func (j *Jira) readCreateMetadataFields(ctx context.Context, project string, issueType domain.JiraIssueType) (*domain.JiraCreateMetadata, error) {
	rawFields, err := j.readCreateFields(ctx, project, issueType.ID)
	if err != nil {
		return nil, err
	}
	fields := make([]domain.JiraCreateField, 0, len(rawFields))
	for _, field := range rawFields {
		if field.FieldID == "" || field.Name == "" {
			return nil, fmt.Errorf("%w: Jira create-screen metadata contains an incomplete field", domain.ErrCheckFailed)
		}
		fields = append(fields, domain.JiraCreateField{
			FieldID: field.FieldID, Name: field.Name, Required: field.Required,
			HasAllowedValues: len(field.AllowedValues) > 0,
		})
	}
	sort.Slice(fields, func(i, k int) bool { return fields[i].FieldID < fields[k].FieldID })
	return &domain.JiraCreateMetadata{Project: project, IssueType: issueType, Fields: fields}, nil
}
