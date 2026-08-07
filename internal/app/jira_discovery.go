package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const jiraDiscoverySchemaVersion = 1

type JiraProjectListResult struct {
	SchemaVersion   int                  `json:"schema_version"`
	IncludeArchived bool                 `json:"include_archived"`
	Count           int                  `json:"count"`
	Total           int                  `json:"total"`
	Complete        bool                 `json:"complete"`
	Truncated       bool                 `json:"truncated"`
	Projects        []domain.JiraProject `json:"projects"`
}

type JiraIssueTypeListResult struct {
	SchemaVersion int                    `json:"schema_version"`
	Project       string                 `json:"project"`
	Count         int                    `json:"count"`
	Complete      bool                   `json:"complete"`
	IssueTypes    []domain.JiraIssueType `json:"issue_types"`
}

type JiraCreateCheckResult struct {
	SchemaVersion int                      `json:"schema_version"`
	Project       string                   `json:"project"`
	IssueType     domain.JiraIssueType     `json:"issue_type"`
	Count         int                      `json:"count"`
	Complete      bool                     `json:"complete"`
	Fields        []domain.JiraCreateField `json:"fields"`
}

func (s *JiraService) ListProjects(ctx context.Context, includeArchived bool, limit int) (*JiraProjectListResult, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: project limit must be between 1 and 1000", domain.ErrUsage)
	}
	reader, ok := s.tr.(domain.JiraProjectReader)
	if !ok {
		return nil, fmt.Errorf("%w: Jira project discovery is unavailable", domain.ErrConfig)
	}
	projects, err := reader.ReadProjects(ctx, includeArchived)
	if err != nil {
		return nil, err
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Key == projects[j].Key {
			return projects[i].ID < projects[j].ID
		}
		return projects[i].Key < projects[j].Key
	})
	total := len(projects)
	if total > limit {
		projects = projects[:limit]
	}
	return &JiraProjectListResult{
		SchemaVersion: jiraDiscoverySchemaVersion, IncludeArchived: includeArchived,
		Count: len(projects), Total: total, Complete: total <= limit, Truncated: total > limit,
		Projects: projects,
	}, nil
}

func (s *JiraService) ListCreateIssueTypes(ctx context.Context, project string) (*JiraIssueTypeListResult, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("%w: Jira project is required", domain.ErrUsage)
	}
	reader, ok := s.tr.(domain.JiraCreateMetadataReader)
	if !ok {
		return nil, fmt.Errorf("%w: Jira create metadata discovery is unavailable", domain.ErrConfig)
	}
	issueTypes, err := reader.ReadCreateIssueTypes(ctx, project)
	if err != nil {
		return nil, err
	}
	return &JiraIssueTypeListResult{
		SchemaVersion: jiraDiscoverySchemaVersion, Project: project,
		Count: len(issueTypes), Complete: true, IssueTypes: issueTypes,
	}, nil
}

func (s *JiraService) CheckCreateMetadata(ctx context.Context, project, issueType string) (*JiraCreateCheckResult, error) {
	project, issueType = strings.TrimSpace(project), strings.TrimSpace(issueType)
	if project == "" || issueType == "" {
		return nil, fmt.Errorf("%w: Jira project and issue type are required", domain.ErrUsage)
	}
	reader, ok := s.tr.(domain.JiraCreateMetadataReader)
	if !ok {
		return nil, fmt.Errorf("%w: Jira create metadata discovery is unavailable", domain.ErrConfig)
	}
	metadata, err := reader.ReadCreateMetadata(ctx, project, issueType)
	if err != nil {
		return nil, err
	}
	return &JiraCreateCheckResult{
		SchemaVersion: jiraDiscoverySchemaVersion, Project: metadata.Project,
		IssueType: metadata.IssueType, Count: len(metadata.Fields), Complete: true, Fields: metadata.Fields,
	}, nil
}

func JiraProjectsMarkdown(result *JiraProjectListResult) string {
	rows := make([][]string, 0, len(result.Projects))
	for _, project := range result.Projects {
		archived := "unknown"
		if project.Archived != nil {
			archived = strconv.FormatBool(*project.Archived)
		}
		rows = append(rows, []string{markdownSingleLine(project.Key), markdownSingleLine(project.Name), markdownSingleLine(project.ProjectTypeKey), archived})
	}
	return MarkdownTable([]string{"Key", "Name", "Type", "Archived"}, rows)
}

func JiraIssueTypesMarkdown(result *JiraIssueTypeListResult) string {
	rows := make([][]string, 0, len(result.IssueTypes))
	for _, issueType := range result.IssueTypes {
		rows = append(rows, []string{markdownSingleLine(issueType.ID), markdownSingleLine(issueType.Name), strconv.FormatBool(issueType.Subtask)})
	}
	return MarkdownTable([]string{"ID", "Name", "Subtask"}, rows)
}

func JiraCreateCheckMarkdown(result *JiraCreateCheckResult) string {
	rows := make([][]string, 0, len(result.Fields))
	for _, field := range result.Fields {
		rows = append(rows, []string{
			markdownSingleLine(field.FieldID), markdownSingleLine(field.Name),
			strconv.FormatBool(field.Required), strconv.FormatBool(field.HasAllowedValues),
		})
	}
	return MarkdownTable([]string{"Field ID", "Name", "Required", "Allowed values"}, rows)
}
