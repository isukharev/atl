package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type JiraProjectIssuePageField struct {
	Field   string `json:"field"`
	Present bool   `json:"present"`
	Null    bool   `json:"null"`
	Value   string `json:"value"`
}

type JiraProjectIssuePageIssue struct {
	ID         string                      `json:"id"`
	Key        string                      `json:"key"`
	ProjectID  string                      `json:"project_id"`
	ProjectKey string                      `json:"project_key"`
	Updated    string                      `json:"updated"`
	Fields     []JiraProjectIssuePageField `json:"fields"`
}

type JiraProjectIssuePagePage struct {
	StartAt             int    `json:"start_at"`
	MaxResults          int    `json:"max_results"`
	Total               int    `json:"total"`
	Count               int    `json:"count"`
	NextCursor          string `json:"next_cursor"`
	NextCursorPresent   bool   `json:"next_cursor_present"`
	CoordinateExhausted bool   `json:"coordinate_exhausted"`
	SelectionComplete   bool   `json:"selection_complete"`
	PartialReason       string `json:"partial_reason"`
}

// JiraProjectIssuePageResult is the public application projection for one
// execution-v2 page. Every digest, identity, field-presence, pagination, and
// completeness fact remains explicit; issue and field order is preserved.
type JiraProjectIssuePageResult struct {
	SchemaVersion      int                         `json:"schema_version"`
	ArgumentsSHA256    string                      `json:"arguments_sha256"`
	ConsistencyProfile string                      `json:"consistency_profile"`
	ProjectID          string                      `json:"project_id"`
	ProjectKey         string                      `json:"project_key"`
	Issues             []JiraProjectIssuePageIssue `json:"issues"`
	Page               JiraProjectIssuePagePage    `json:"page"`
	Complete           bool                        `json:"complete"`
}

// NewJiraProjectIssuePageArguments validates the complete public selector and
// returns its sole canonical domain representation. Cursor is a decimal
// coordinate, not an opaque JQL continuation.
func NewJiraProjectIssuePageArguments(projectKey string, fields []string, limit int, cursor string) (domain.BrokerProjectPageArguments, error) {
	if projectKey == "" || len(projectKey) > 32 || strings.TrimSpace(projectKey) != projectKey || !domain.ValidJiraIssueKey(projectKey+"-1") {
		return domain.BrokerProjectPageArguments{}, fmt.Errorf("%w: project must be a canonical Jira project key", domain.ErrUsage)
	}
	if limit < 1 || limit > domain.BrokerProjectPageMaxResults {
		return domain.BrokerProjectPageArguments{}, fmt.Errorf("%w: limit must be between 1 and %d", domain.ErrUsage, domain.BrokerProjectPageMaxResults)
	}
	if len(fields) > 2 {
		return domain.BrokerProjectPageArguments{}, fmt.Errorf("%w: fields may contain at most 2 entries", domain.ErrUsage)
	}
	startAt, err := parseJiraProjectPageCursor(cursor)
	if err != nil {
		return domain.BrokerProjectPageArguments{}, err
	}
	requested := make([]domain.BrokerProjectPageField, 0, len(fields))
	seen := make(map[domain.BrokerProjectPageField]struct{}, len(fields))
	for _, raw := range fields {
		field := domain.BrokerProjectPageField(raw)
		if strings.TrimSpace(raw) != raw || field != domain.BrokerProjectPageFieldDescription && field != domain.BrokerProjectPageFieldSummary {
			return domain.BrokerProjectPageArguments{}, fmt.Errorf("%w: fields may contain only description and summary", domain.ErrUsage)
		}
		if _, duplicate := seen[field]; duplicate {
			return domain.BrokerProjectPageArguments{}, fmt.Errorf("%w: fields must be unique", domain.ErrUsage)
		}
		seen[field] = struct{}{}
		requested = append(requested, field)
	}
	slices.Sort(requested)
	return domain.BrokerProjectPageArguments{ProjectKey: projectKey, Fields: requested, StartAt: startAt, MaxResults: limit}, nil
}

func parseJiraProjectPageCursor(value string) (int, error) {
	if value == "" {
		value = "0"
	}
	if value != "0" && (value[0] < '1' || value[0] > '9') {
		return 0, fmt.Errorf("%w: cursor must be a canonical decimal from 0 to %d", domain.ErrUsage, domain.BrokerProjectPageMaxStartAt)
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return 0, fmt.Errorf("%w: cursor must be a canonical decimal from 0 to %d", domain.ErrUsage, domain.BrokerProjectPageMaxStartAt)
		}
	}
	startAt, err := strconv.Atoi(value)
	if err != nil || startAt < 0 || startAt > domain.BrokerProjectPageMaxStartAt || strconv.Itoa(startAt) != value {
		return 0, fmt.Errorf("%w: cursor must be a canonical decimal from 0 to %d", domain.ErrUsage, domain.BrokerProjectPageMaxStartAt)
	}
	return startAt, nil
}

func (s *JiraService) ProjectIssuePage(ctx context.Context, arguments domain.BrokerProjectPageArguments) (*JiraProjectIssuePageResult, error) {
	if s == nil || s.projectPages == nil {
		_, err := brokercontract.ErrorForReason(domain.BrokerReasonUnsupported)
		return nil, err
	}
	fields := make([]string, len(arguments.Fields))
	for index, field := range arguments.Fields {
		fields[index] = string(field)
	}
	canonical, err := NewJiraProjectIssuePageArguments(arguments.ProjectKey, fields, arguments.MaxResults, strconv.Itoa(arguments.StartAt))
	if err != nil || canonical.ProjectKey != arguments.ProjectKey || canonical.StartAt != arguments.StartAt ||
		canonical.MaxResults != arguments.MaxResults || !slices.Equal(canonical.Fields, arguments.Fields) {
		return nil, fmt.Errorf("%w: project-page arguments must be canonical", domain.ErrUsage)
	}
	result, err := s.projectPages.ReadJiraProjectIssuePage(ctx, canonical)
	if err != nil {
		return nil, err
	}
	return jiraProjectIssuePageResult(result), nil
}

func jiraProjectIssuePageResult(value domain.BrokerJiraProjectPageResultV2) *JiraProjectIssuePageResult {
	issues := make([]JiraProjectIssuePageIssue, len(value.Issues))
	for issueIndex, issue := range value.Issues {
		fields := make([]JiraProjectIssuePageField, len(issue.Fields))
		for fieldIndex, field := range issue.Fields {
			fields[fieldIndex] = JiraProjectIssuePageField{Field: string(field.Field), Present: field.Present, Null: field.Null, Value: field.Value}
		}
		issues[issueIndex] = JiraProjectIssuePageIssue{
			ID: issue.ID, Key: issue.Key, ProjectID: issue.ProjectID, ProjectKey: issue.ProjectKey,
			Updated: issue.Updated, Fields: fields,
		}
	}
	page := JiraProjectIssuePagePage{
		StartAt: value.Page.StartAt, MaxResults: value.Page.MaxResults, Total: value.Page.Total, Count: value.Page.Count,
		NextCursor: value.Page.NextCursor, NextCursorPresent: value.Page.NextCursorPresent,
		CoordinateExhausted: value.Page.CoordinateExhausted, SelectionComplete: value.Page.SelectionComplete,
		PartialReason: value.Page.PartialReason,
	}
	return &JiraProjectIssuePageResult{
		SchemaVersion: value.SchemaVersion, ArgumentsSHA256: value.ArgumentsSHA256,
		ConsistencyProfile: value.ConsistencyProfile, ProjectID: value.ProjectID, ProjectKey: value.ProjectKey,
		Issues: issues, Page: page, Complete: value.Complete,
	}
}
