package app

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func (s *JiraService) resolveListColumns(source, view string, explicit []string) ([]string, string, error) {
	if len(explicit) > 0 {
		return append([]string(nil), explicit...), "explicit", nil
	}
	var views map[string]config.JiraListView
	if s != nil && s.cfg != nil {
		views = s.cfg.JiraListViews
	}
	columns, resolved, err := config.ResolveJiraListView(views, view, source)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	return columns, resolved, nil
}

func validateStructureViewFields(fields []string) error {
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || field == "position" || field == "id" || strings.Contains(field, ".") {
			return fmt.Errorf("%w: Structure list views accept Jira field ids only; invalid field %q", domain.ErrUsage, field)
		}
	}
	return nil
}
