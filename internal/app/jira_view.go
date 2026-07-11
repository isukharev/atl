package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

// JiraIssueViewOpts controls a transient, read-only Markdown projection. Root
// selects the presentation-only local config layer; it is never written.
type JiraIssueViewOpts struct {
	Root   string
	Render config.RenderService
}

// JiraIssueViewResult is the scriptable JSON form of `jira issue view`.
// Warnings are emitted on stderr by the CLI so stdout remains either one JSON
// object or raw Markdown under -o text.
type JiraIssueViewResult struct {
	Key      string   `json:"key"`
	Markdown string   `json:"markdown"`
	Warnings []string `json:"-"`
}

// ViewIssue fetches one issue and renders it through the same configured
// Markdown pipeline as pull/render without creating a mirror, snapshot,
// sidecar, asset, or view-state entry.
func (s *JiraService) ViewIssue(ctx context.Context, key string, opts JiraIssueViewOpts) (*JiraIssueViewResult, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = "."
	}
	rs, warnings := ResolveRender(s.cfg, root, opts.Render, "jira")
	is, err := s.tr.GetIssue(ctx, key, jiraIssueViewFields(rs))
	if err != nil {
		return nil, err
	}

	var related *JiraEpicChildrenSidecar
	if rs.On(SecEpicChildren) && (strings.TrimSpace(rs.EpicField) != "" || isEpicIssue(*is)) {
		selector := strings.TrimSpace(rs.EpicField)
		epicField, resolveErr := s.resolveEpicField(ctx, selector)
		if resolveErr != nil {
			return nil, resolveErr
		}
		rs.EpicField = epicField
		byEpic, truncated, fetchErr := s.fetchEpicChildrenPage(ctx, []domain.Issue{*is}, epicField)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if sidecar, ok := byEpic[is.Key]; ok {
			sidecar.EpicSelector = selector
			related = &sidecar
		}
		if truncated {
			warnings = append(warnings, fmt.Sprintf("render: epic children for %s truncated at %d issues", is.Key, jiraEpicChildrenCap))
		}
	}

	return &JiraIssueViewResult{
		Key:      is.Key,
		Markdown: string(renderTransientIssueMarkdown(is, related, rs)),
		Warnings: warnings,
	}, nil
}

// jiraIssueViewFields is the exact one-issue projection required by the
// resolved view. Unlike mirror pull, it has no compatibility snapshot to keep
// wide, so a minimal transient read requests only summary + description.
func jiraIssueViewFields(rs RenderSettings) []string {
	fields := []string{"summary", "description"}
	sectionFields := map[string]string{
		SecStatus:         "status",
		SecType:           "issuetype",
		SecProject:        "project",
		SecAssignee:       "assignee",
		SecReporter:       "reporter",
		SecLabels:         "labels",
		SecPriority:       "priority",
		SecParent:         "parent",
		SecCreated:        "created",
		SecUpdated:        "updated",
		SecResolution:     "resolution",
		SecDuedate:        "duedate",
		SecComponents:     "components",
		SecFixVersions:    "fixVersions",
		SecAttachmentsAll: "attachment",
		SecLinks:          "issuelinks",
		SecComments:       "comment",
		SecSubtasks:       "subtasks",
	}
	var enabled []string
	for section, field := range sectionFields {
		if rs.On(section) {
			enabled = append(enabled, field)
		}
	}
	if rs.On(SecEpicChildren) && strings.TrimSpace(rs.EpicField) == "" && !rs.On(SecType) {
		enabled = append(enabled, "issuetype")
	}
	if rs.On(SecCustomFields) {
		enabled = append(enabled, rs.CustomFields...)
		for _, view := range rs.FieldViews {
			enabled = append(enabled, view.ID)
		}
	}
	sort.Strings(enabled)

	seen := map[string]bool{"summary": true, "description": true}
	for _, field := range enabled {
		field = strings.TrimSpace(field)
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		fields = append(fields, field)
	}
	return fields
}
