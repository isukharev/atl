package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

func (s *JiraService) Create(ctx context.Context, project, issueType, summary string, body []byte, fields map[string]domain.JiraFieldInput) (*domain.Issue, error) {
	if err := rejectCreateIssueTypeOverride(fields); err != nil {
		return nil, err
	}
	resolved, err := s.resolveCreateIssueType(ctx, project, issueType)
	if err != nil {
		return nil, err
	}
	created, err := s.tr.Create(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), project, resolved.ID, summary, body, fields)
	if err != nil {
		return nil, classifyCreateWriteError("issue create", err)
	}
	if created != nil {
		created.Type = resolved.Name
	}
	return created, nil
}

func rejectCreateIssueTypeOverride(fields map[string]domain.JiraFieldInput) error {
	for key := range fields {
		if strings.EqualFold(strings.TrimSpace(key), "issuetype") {
			return fmt.Errorf("%w: --field and --field-json must not override issuetype; use --type", domain.ErrUsage)
		}
	}
	return nil
}

func (s *JiraService) resolveCreateIssueType(ctx context.Context, project, selector string) (domain.JiraIssueType, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return domain.JiraIssueType{}, fmt.Errorf("%w: Jira project is required", domain.ErrUsage)
	}
	reader, ok := s.tr.(domain.JiraCreateIssueTypeReader)
	if !ok {
		return domain.JiraIssueType{}, fmt.Errorf("%w: Jira create metadata discovery is unavailable", domain.ErrConfig)
	}
	types, err := reader.ReadCreateIssueTypes(ctx, project)
	if err != nil {
		return domain.JiraIssueType{}, err
	}
	return domain.ResolveJiraIssueType(types, selector)
}
