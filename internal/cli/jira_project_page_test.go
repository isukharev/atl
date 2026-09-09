package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func TestJiraProjectPageCommandIsClosedReadOnlyWithBoundedSelectors(t *testing.T) {
	command, _, err := newRoot().Find([]string{"jira", "issue", "project-page"})
	if err != nil || command == nil {
		t.Fatalf("command=%v err=%v", command, err)
	}
	if command.Annotations[accessAnnotation] != "read-only" || command.Annotations[effectProfileAnnotation] != "remote-read-fixed" ||
		command.Annotations[textOutputAnnotation] != "supported" || command.Annotations[idOutputAnnotation] != "supported" {
		t.Fatalf("annotations=%v", command.Annotations)
	}
	for name, want := range map[string]string{"project": "", "fields": "summary", "limit": "15", "cursor": "0"} {
		flag := command.Flags().Lookup(name)
		if flag == nil || flag.DefValue != want {
			t.Fatalf("--%s=%v want default %q", name, flag, want)
		}
	}
}

func TestJiraProjectPageCLIRejectsInvalidSelectorsBeforeConfiguration(t *testing.T) {
	for _, arguments := range [][]string{
		{"jira", "issue", "project-page"},
		{"jira", "issue", "project-page", "--project", "example"},
		{"jira", "issue", "project-page", "--project", "EXAMPLE", "--fields", "status"},
		{"jira", "issue", "project-page", "--project", "EXAMPLE", "--fields", "summary,summary"},
		{"jira", "issue", "project-page", "--project", "EXAMPLE", "--limit", "16"},
		{"jira", "issue", "project-page", "--project", "EXAMPLE", "--cursor", ""},
		{"jira", "issue", "project-page", "--project", "EXAMPLE", "--cursor", "01"},
	} {
		if output, code := runCLI(t, nil, arguments...); code != exitUsage {
			t.Fatalf("args=%v exit=%d output=%s", arguments, code, output)
		}
	}
}

func TestJiraProjectPageDirectModeDoesNotFallBackToJQL(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	output, code := runCLI(t, jiraEnv(server), "jira", "issue", "project-page", "--project", "EXAMPLE", "--fields", "summary,description")
	if code != exitUsage || requests.Load() != 0 || output != "" {
		t.Fatalf("exit=%d requests=%d output=%s", code, requests.Load(), output)
	}
}

func TestJiraProjectPageTextIsBoundedAndPreservesPageTruth(t *testing.T) {
	issues := make([]app.JiraProjectIssuePageIssue, 15)
	for index := range issues {
		issues[index] = app.JiraProjectIssuePageIssue{
			ID: "123", Key: "EXAMPLE-123", Updated: "2026-09-09T10:00:00Z",
			Fields: []app.JiraProjectIssuePageField{
				{Field: "description", Present: true, Null: index == 0, Value: strings.Repeat("description ", 10_000)},
				{Field: "summary", Present: true, Value: strings.Repeat("summary ", 10_000)},
			},
		}
	}
	result := &app.JiraProjectIssuePageResult{
		Issues: issues,
		Page: app.JiraProjectIssuePagePage{
			StartAt: 15, MaxResults: 15, Total: 31, Count: 15, NextCursor: "30", NextCursorPresent: true,
			SelectionComplete: false, CoordinateExhausted: false,
		},
		Complete: true,
	}
	text := jiraProjectIssuePageText(result)
	if len(text) > jiraProjectPageTextMaxBytes || !strings.Contains(text, "<null>") ||
		!strings.Contains(text, "next_cursor_present=true") || !strings.Contains(text, "selection_complete=false") ||
		!strings.Contains(text, "...") {
		t.Fatalf("bytes=%d text=%q", len(text), text)
	}
}
