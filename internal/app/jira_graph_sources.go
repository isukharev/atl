package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// JiraIssueGraphSourceSelection qualifies an explicit collection scope. Omitted
// sources do not participate in completeness and never establish absence.
type JiraIssueGraphSourceSelection struct {
	SchemaVersion int                            `json:"schema_version"`
	Selected      []string                       `json:"selected"`
	Omitted       []string                       `json:"omitted"`
	Snapshot      domain.IssueSnapshotProjection `json:"snapshot"`
}

// NormalizeJiraIssueGraphSources is the closed input contract shared by the
// application and its transports. Nil inputs preserve the released default.
// Explicit inputs contain at most nine tokens per form, with at most 32 bytes
// per token; repetition is deduplicated into the established collector order.
func NormalizeJiraIssueGraphSources(include, exclude []string, development bool) (*JiraIssueGraphSourceSelection, error) {
	if include == nil && exclude == nil {
		return nil, nil
	}
	parse := func(raw []string) (map[string]bool, error) {
		if raw != nil && (len(raw) == 0 || len(raw) > 9) {
			return nil, fmt.Errorf("%w: graph source selection requires 1..9 tokens per form", domain.ErrUsage)
		}
		out := map[string]bool{}
		count := 0
		for _, item := range raw {
			if len(item) > 9*33 {
				return nil, fmt.Errorf("%w: graph source selection exceeds its token bound", domain.ErrUsage)
			}
			for _, token := range strings.Split(item, ",") {
				count++
				if count > 9 || len(token) > 32 || !slices.Contains(jiraGraphSourceKinds(true), token) {
					return nil, fmt.Errorf("%w: graph sources must use the closed canonical source names", domain.ErrUsage)
				}
				out[token] = true
			}
		}
		return out, nil
	}
	selected, err := parse(include)
	if err != nil {
		return nil, err
	}
	omitted, err := parse(exclude)
	if err != nil {
		return nil, err
	}
	if selected["development"] && !development || omitted["development"] && development {
		return nil, fmt.Errorf("%w: development requires include-development and cannot be excluded with that opt-in", domain.ErrUsage)
	}
	if include == nil {
		for _, kind := range jiraGraphSourceOrder {
			selected[kind] = true
		}
	}
	if development {
		selected["development"] = true
	}
	for kind := range omitted {
		delete(selected, kind)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("%w: graph source selection must retain at least one source", domain.ErrUsage)
	}
	out := &JiraIssueGraphSourceSelection{SchemaVersion: 1, Selected: []string{}, Omitted: []string{}}
	for _, kind := range jiraGraphSourceKinds(true) {
		if selected[kind] {
			out.Selected = append(out.Selected, kind)
		} else {
			out.Omitted = append(out.Omitted, kind)
		}
	}
	out.Snapshot = jiraGraphSnapshotProjection(out.Selected)
	return out, nil
}

func jiraGraphSnapshotProjection(selected []string) domain.IssueSnapshotProjection {
	out := domain.IssueSnapshotProjection{Fields: []string{"summary"}, Properties: slices.Contains(selected, "issue_properties")}
	if slices.Contains(selected, "issue_fields") || slices.Contains(selected, "hierarchy") {
		out.Fields = []string{"*all"}
		if !slices.Contains(selected, "issue_fields") {
			out.SupportingFieldsReason = "hierarchy_discovery"
		}
	} else {
		if slices.Contains(selected, "issue_links") {
			out.Fields = append(out.Fields, "issuelinks")
		}
		if slices.Contains(selected, "attachments") {
			out.Fields = append(out.Fields, "attachment")
		}
	}
	return out
}

func jiraGraphSelectedKinds(selection *JiraIssueGraphSourceSelection, development bool) []string {
	if selection != nil {
		return slices.Clone(selection.Selected)
	}
	return jiraGraphSourceKinds(development)
}

func validateJiraGraphSourceSelection(selection *JiraIssueGraphSourceSelection, development bool) error {
	if selection == nil {
		return nil
	}
	expected, err := NormalizeJiraIssueGraphSources(selection.Selected, nil, development)
	if err != nil || selection.SchemaVersion != 1 || selection.Selected == nil || selection.Omitted == nil ||
		!slices.Equal(selection.Selected, expected.Selected) || !slices.Equal(selection.Omitted, expected.Omitted) ||
		!slices.Equal(selection.Snapshot.Fields, expected.Snapshot.Fields) ||
		selection.Snapshot.Properties != expected.Snapshot.Properties || selection.Snapshot.SupportingFieldsReason != expected.Snapshot.SupportingFieldsReason {
		return fmt.Errorf("%w: Jira graph source selection is invalid", domain.ErrCheckFailed)
	}
	return nil
}

func cloneJiraGraphSourceSelection(selection *JiraIssueGraphSourceSelection) *JiraIssueGraphSourceSelection {
	if selection == nil {
		return nil
	}
	out := *selection
	out.Selected, out.Omitted = slices.Clone(selection.Selected), slices.Clone(selection.Omitted)
	out.Snapshot.Fields = slices.Clone(selection.Snapshot.Fields)
	return &out
}

func jiraGraphSnapshotRead(tracker domain.Tracker, selection *JiraIssueGraphSourceSelection) (func(context.Context, string) (*domain.QualifiedIssueSnapshot, error), error) {
	if selection != nil {
		reader, ok := tracker.(domain.QualifiedIssueSnapshotProjectionReader)
		if !ok {
			return nil, fmt.Errorf("%w: Jira graph projected snapshot capability is unavailable", domain.ErrCheckFailed)
		}
		return func(ctx context.Context, key string) (*domain.QualifiedIssueSnapshot, error) {
			return reader.ReadIssueSnapshotProjection(ctx, key, selection.Snapshot)
		}, nil
	}
	reader, ok := tracker.(domain.QualifiedIssueSnapshotReader)
	if !ok {
		return nil, fmt.Errorf("%w: Jira graph snapshot capability is unavailable", domain.ErrCheckFailed)
	}
	return reader.ReadIssueSnapshot, nil
}
