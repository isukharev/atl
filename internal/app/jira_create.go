package app

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/isukharev/atl/internal/domain"
)

func (s *JiraService) Create(ctx context.Context, project, issueType, summary string, body []byte, fields map[string]domain.JiraFieldInput) (*domain.Issue, error) {
	if err := rejectReservedCreateFields(fields); err != nil {
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

func rejectReservedCreateFields(fields map[string]domain.JiraFieldInput) error {
	for key := range fields {
		if reservedCreateField(key) {
			return fmt.Errorf("%w: --field and --field-json must not override project, issuetype, summary, or description; use the dedicated input", domain.ErrUsage)
		}
	}
	return nil
}

// reservedCreateField removes Unicode whitespace and folds ASCII letters only
// for comparison. It never returns a transformed non-reserved key to callers.
func reservedCreateField(key string) bool {
	var normalized strings.Builder
	normalized.Grow(len(key))
	for _, r := range key {
		if unicode.IsSpace(r) {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r > unicode.MaxASCII {
			return false
		}
		normalized.WriteRune(r)
	}
	switch normalized.String() {
	case "project", "issuetype", "summary", "description":
		return true
	default:
		return false
	}
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
