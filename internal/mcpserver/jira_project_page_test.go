package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

type mcpProjectPageReader struct {
	*recordingJiraReader
	calls     int
	arguments domain.BrokerProjectPageArguments
	result    *app.JiraProjectIssuePageResult
}

func (reader *mcpProjectPageReader) ProjectIssuePage(_ context.Context, arguments domain.BrokerProjectPageArguments) (*app.JiraProjectIssuePageResult, error) {
	reader.calls++
	reader.arguments = arguments
	reader.arguments.Fields = append([]domain.BrokerProjectPageField{}, arguments.Fields...)
	return reader.result, nil
}

func TestJiraProjectIssuePageToolMapsCanonicalInputAndPreservesTruth(t *testing.T) {
	reader := &mcpProjectPageReader{recordingJiraReader: &recordingJiraReader{}, result: mcpProjectPageResult("Synthetic summary")}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{Jira: func() (JiraReader, error) { return reader, nil }}))
	defer closeSessions()
	result := callToolOK(t, client, "jira_project_issue_page", map[string]any{
		"project_key": "EXAMPLE", "fields": []string{"summary", "description"}, "limit": 2, "cursor": "15", "max_bytes": 4096,
	})
	if reader.calls != 1 || reader.arguments.ProjectKey != "EXAMPLE" || reader.arguments.StartAt != 15 || reader.arguments.MaxResults != 2 ||
		len(reader.arguments.Fields) != 2 || reader.arguments.Fields[0] != domain.BrokerProjectPageFieldDescription || reader.arguments.Fields[1] != domain.BrokerProjectPageFieldSummary {
		t.Fatalf("calls=%d arguments=%+v", reader.calls, reader.arguments)
	}
	content, ok := result.StructuredContent.(map[string]any)
	page, pageOK := content["page"].(map[string]any)
	issues, issuesOK := content["issues"].([]any)
	if !ok || !pageOK || !issuesOK || len(issues) != 1 || content["arguments_sha256"] == "" ||
		content["consistency_profile"] != domain.BrokerReadConsistencyIdentitySnapshotV1 || content["complete"] != true ||
		page["next_cursor_present"] != true || page["coordinate_exhausted"] != false || page["selection_complete"] != false {
		t.Fatalf("content=%#v", result.StructuredContent)
	}
}

func TestJiraProjectIssuePageToolAllowsOmittedAndExplicitEmptyIdentityProjection(t *testing.T) {
	for _, arguments := range []map[string]any{
		{"project_key": "EXAMPLE"},
		{"project_key": "EXAMPLE", "fields": []string{}},
	} {
		reader := &mcpProjectPageReader{recordingJiraReader: &recordingJiraReader{}, result: mcpProjectPageResult("")}
		reader.result.Issues[0].Fields = []app.JiraProjectIssuePageField{}
		client, closeSessions := connectTestClient(t, New("test", Dependencies{Jira: func() (JiraReader, error) { return reader, nil }}))
		_ = callToolOK(t, client, "jira_project_issue_page", arguments)
		closeSessions()
		if reader.calls != 1 || len(reader.arguments.Fields) != 0 || reader.arguments.MaxResults != domain.BrokerProjectPageMaxResults || reader.arguments.StartAt != 0 {
			t.Fatalf("input=%v calls=%d arguments=%+v", arguments, reader.calls, reader.arguments)
		}
	}
}

func TestJiraProjectIssuePageToolRejectsInvalidInputBeforeJiraConstruction(t *testing.T) {
	for _, arguments := range []map[string]any{
		{"fields": []string{"summary"}},
		{"project_key": "example", "fields": []string{"summary"}},
		{"project_key": "EXAMPLE", "fields": []string{"status"}},
		{"project_key": "EXAMPLE", "fields": []string{"summary", "summary"}},
		{"project_key": "EXAMPLE", "fields": []string{"summary", "description", "summary"}},
		{"project_key": "EXAMPLE", "fields": nil},
		{"project_key": "EXAMPLE", "fields": "summary"},
		{"project_key": "EXAMPLE", "fields": []string{"summary"}, "limit": 0},
		{"project_key": "EXAMPLE", "fields": []string{"summary"}, "limit": nil},
		{"project_key": "EXAMPLE", "fields": []string{"summary"}, "limit": 16},
		{"project_key": "EXAMPLE", "fields": []string{"summary"}, "cursor": ""},
		{"project_key": "EXAMPLE", "fields": []string{"summary"}, "cursor": "01"},
		{"project_key": "EXAMPLE", "fields": []string{"summary"}, "cursor": nil},
		{"project_key": "EXAMPLE", "fields": []string{"summary"}, "max_bytes": 1023},
		{"project_key": "EXAMPLE", "fields": []string{"summary"}, "max_bytes": 0},
		{"project_key": "EXAMPLE", "fields": []string{"summary"}, "max_bytes": nil},
		{"project_key": "EXAMPLE", "fields": []string{"summary"}, "jql": "project = EXAMPLE"},
	} {
		constructed := 0
		client, closeSessions := connectTestClient(t, New("test", Dependencies{Jira: func() (JiraReader, error) {
			constructed++
			return nil, context.Canceled
		}}))
		_, _ = callToolError(t, client, "jira_project_issue_page", arguments)
		closeSessions()
		if constructed != 0 {
			t.Fatalf("arguments=%v constructed=%d", arguments, constructed)
		}
	}
}

func TestJiraProjectIssuePageToolRequiresOptionalReaderAndBoundsOutput(t *testing.T) {
	t.Run("optional reader", func(t *testing.T) {
		client, closeSessions := connectTestClient(t, New("test", Dependencies{Jira: func() (JiraReader, error) { return &recordingJiraReader{}, nil }}))
		defer closeSessions()
		_, classified := callToolError(t, client, "jira_project_issue_page", map[string]any{"project_key": "EXAMPLE", "fields": []string{"summary"}})
		if classified.Kind != "usage_error" {
			t.Fatalf("classification=%+v", classified)
		}
	})

	t.Run("encoded output", func(t *testing.T) {
		reader := &mcpProjectPageReader{recordingJiraReader: &recordingJiraReader{}, result: mcpProjectPageResult(strings.Repeat("x", 2048))}
		client, closeSessions := connectTestClient(t, New("test", Dependencies{Jira: func() (JiraReader, error) { return reader, nil }}))
		defer closeSessions()
		result, classified := callToolError(t, client, "jira_project_issue_page", map[string]any{
			"project_key": "EXAMPLE", "fields": []string{"summary"}, "max_bytes": 1024,
		})
		if reader.calls != 1 || classified.Kind != "output_limit_exceeded" || result.StructuredContent != nil {
			t.Fatalf("calls=%d classification=%+v result=%+v", reader.calls, classified, result)
		}
	})
}

func mcpProjectPageResult(summary string) *app.JiraProjectIssuePageResult {
	return &app.JiraProjectIssuePageResult{
		SchemaVersion: 2, ArgumentsSHA256: strings.Repeat("a", 64),
		ConsistencyProfile: domain.BrokerReadConsistencyIdentitySnapshotV1, ProjectID: "7", ProjectKey: "EXAMPLE",
		Issues: []app.JiraProjectIssuePageIssue{{
			ID: "20", Key: "EXAMPLE-20", ProjectID: "7", ProjectKey: "EXAMPLE", Updated: "2026-09-09T10:00:00Z",
			Fields: []app.JiraProjectIssuePageField{{Field: "summary", Present: true, Value: summary}},
		}},
		Page: app.JiraProjectIssuePagePage{
			StartAt: 15, MaxResults: 2, Total: 18, Count: 1, NextCursor: "16", NextCursorPresent: true,
			CoordinateExhausted: false, SelectionComplete: false,
		},
		Complete: true,
	}
}
