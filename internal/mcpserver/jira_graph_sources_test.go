package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func TestJiraIssueGraphSourcesProjectFullAndCompact(t *testing.T) {
	for _, projection := range []string{"full", "compact"} {
		opts := defaultMCPGraphOptions(1)
		opts.IncludeSources = []string{"issue_links"}
		reader := &recordingJiraReader{graphResult: validMCPGraphResult("PROJ-1", opts, "not emitted")}
		client, closeSessions := connectTestClient(t, New("test", Dependencies{Jira: func() (JiraReader, error) { return reader, nil }}))
		result := callToolOK(t, client, "jira_issue_graph", map[string]any{"key": "PROJ-1", "depth": 1, "projection": projection, "include_sources": []string{"issue_links"}})
		closeSessions()
		if !reflect.DeepEqual(reader.graphOpts, opts) || reader.graphOpts.ResolveConfluence {
			t.Fatalf("options=%+v want=%+v", reader.graphOpts, opts)
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			SourceSelection *app.JiraIssueGraphSourceSelection `json:"source_selection"`
			Complete        bool                               `json:"complete"`
			Bounds          JiraIssueGraphBoundsOutput         `json:"bounds"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		if !out.Complete || out.Bounds.MaxSources != 51 || !reflect.DeepEqual(out.SourceSelection, reader.graphResult.SourceSelection) {
			t.Fatalf("output=%s", data)
		}
	}
}

func TestJiraIssueGraphSourcesRejectResultOutsideRequestedScope(t *testing.T) {
	opts := defaultMCPGraphOptions(0)
	opts.IncludeSources = []string{"issue_links"}
	for _, mutate := range []func(*app.JiraIssueGraphResult){
		func(r *app.JiraIssueGraphResult) { r.SourceSelection = nil },
		func(r *app.JiraIssueGraphResult) { r.SourceSelection.Selected[0] = "comments" },
		func(r *app.JiraIssueGraphResult) { r.SourceSelection.Snapshot.Fields = []string{"*all"} },
		func(r *app.JiraIssueGraphResult) { r.Bounds.MaxSources = 401 },
		func(r *app.JiraIssueGraphResult) { r.Sources[0].Kind = "comments" },
	} {
		result := validMCPGraphResult("PROJ-1", opts, "")
		mutate(result)
		if _, err := projectJiraIssueGraph(result, "PROJ-1", opts); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("invalid result accepted: %v", err)
		}
	}
	otherOpts := defaultMCPGraphOptions(0)
	otherOpts.IncludeSources = []string{"comments"}
	result := validMCPGraphResult("PROJ-1", otherOpts, "")
	if _, err := projectJiraIssueGraph(result, "PROJ-1", opts); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("valid result from different scope accepted: %v", err)
	}
}

func TestJiraIssueGraphExplicitNullSourceFormsNeverConstructJira(t *testing.T) {
	constructed := false
	client, closeSessions := connectTestClient(t, New("test", Dependencies{Jira: func() (JiraReader, error) { constructed = true; return nil, errors.New("unexpected") }}))
	defer closeSessions()
	for _, field := range []string{"include_sources", "exclude_sources"} {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "jira_issue_graph", Arguments: map[string]any{"key": "PROJ-1", field: nil}})
		if err != nil || !result.IsError || constructed {
			t.Fatalf("field=%s result=%+v err=%v constructed=%v", field, result, err, constructed)
		}
	}
}
