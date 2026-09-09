package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func TestJiraIssueGraphMCPPreservesRemoteLinkFailureInFullAndCompactJSON(t *testing.T) {
	for _, projection := range []string{"full", "compact"} {
		t.Run(projection, func(t *testing.T) {
			opts := defaultMCPGraphOptions(0)
			graph := validMCPGraphResult("PROJ-1", opts, "PRIVATE-SUMMARY")
			setMCPRemoteLinkFailure(t, graph)
			reader := &recordingJiraReader{graphResult: graph}
			client, closeSessions := connectTestClient(t, New("test", Dependencies{
				Jira: func() (JiraReader, error) { return reader, nil },
			}))
			defer closeSessions()

			arguments := map[string]any{"key": "PROJ-1"}
			if projection == "compact" {
				arguments["projection"] = "compact"
				arguments["select"] = []string{"none"}
			}
			result := callToolOK(t, client, "jira_issue_graph", arguments)
			structured, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok || !json.Valid([]byte(text.Text)) {
				t.Fatalf("MCP text projection=%T %q", result.Content[0], text.Text)
			}
			var structuredValue, textValue any
			if json.Unmarshal(structured, &structuredValue) != nil || json.Unmarshal([]byte(text.Text), &textValue) != nil || !reflect.DeepEqual(structuredValue, textValue) {
				t.Fatal("MCP text and structured diagnostics diverged")
			}
			if strings.Contains(string(structured), "PRIVATE") {
				t.Fatalf("MCP diagnostic exposed narrative: %s", structured)
			}
			var document struct {
				Sources []JiraIssueGraphSourceOutput `json:"sources"`
			}
			if err := json.Unmarshal(structured, &document); err != nil || len(document.Sources) == 0 {
				t.Fatalf("decode %s diagnostic: sources=%d err=%v", projection, len(document.Sources), err)
			}
			found := false
			for _, source := range document.Sources {
				if source.Kind == "remote_links" {
					found = source.Failure != nil && source.Failure.Class == domain.ArtifactSourceFailurePermission &&
						source.Failure.HTTPStatus != nil && *source.Failure.HTTPStatus == 403
				}
			}
			if !found {
				t.Fatalf("%s projection omitted remote-link diagnostic: %+v", projection, document.Sources)
			}
		})
	}
}

func TestJiraIssueGraphMCPOutputSchemaKeepsFailureClosedAndOptional(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != "jira_issue_graph" {
			continue
		}
		encoded, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"failure"`, `"class"`, `"http_status"`, `"additionalProperties":false`} {
			if !strings.Contains(string(encoded), want) {
				t.Fatalf("output schema omitted %s: %s", want, encoded)
			}
		}
		if strings.Contains(string(encoded), `"required":["failure"`) {
			t.Fatalf("failure must remain optional: %s", encoded)
		}
		return
	}
	t.Fatal("jira_issue_graph not registered")
}

type jiraGraphRemoteDiagnosticSchemaInput struct{}

func TestJiraIssueGraphMCPOutputSchemaRejectsFailureNullAndUnknownMembers(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"null failure", func(source map[string]any) { source["failure"] = nil }},
		{"null HTTP status", func(source map[string]any) { source["failure"].(map[string]any)["http_status"] = nil }},
		{"unknown member", func(source map[string]any) { source["failure"].(map[string]any)["message"] = "PRIVATE" }},
		{"unknown class", func(source map[string]any) { source["failure"].(map[string]any)["class"] = "unknown" }},
		{"mismatched status", func(source map[string]any) { source["failure"].(map[string]any)["http_status"] = 401 }},
		{"explicit zero status", func(source map[string]any) { source["failure"].(map[string]any)["http_status"] = 0 }},
		{"success status", func(source map[string]any) {
			source["failure"] = map[string]any{"class": "http", "http_status": 200}
		}},
		{"transport with status", func(source map[string]any) {
			source["failure"] = map[string]any{"class": "transport", "http_status": 500}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := defaultMCPGraphOptions(0)
			graph := validMCPGraphResult("PROJ-1", opts, "PRIVATE-SUMMARY")
			setMCPRemoteLinkFailure(t, graph)
			projected, err := projectJiraIssueGraph(graph, "PROJ-1", opts)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(projected)
			var invalid map[string]any
			if err := json.Unmarshal(encoded, &invalid); err != nil {
				t.Fatal(err)
			}
			for _, value := range invalid["sources"].([]any) {
				source := value.(map[string]any)
				if source["kind"] == "remote_links" {
					test.mutate(source)
				}
			}

			server := mcp.NewServer(&mcp.Implementation{Name: "schema-test", Version: "1"}, nil)
			tool := readOnlyTool("remote_diagnostic_schema_test", "Remote diagnostic schema", "Validate a closed output")
			tool.OutputSchema = jiraIssueGraphOutputSchema(tool.Name)
			addReadOnlyTool(server, tool,
				func(context.Context, *mcp.CallToolRequest, jiraGraphRemoteDiagnosticSchemaInput) (*mcp.CallToolResult, any, error) {
					return nil, invalid, nil
				})
			client, closeSessions := connectTestClient(t, server)
			defer closeSessions()
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: map[string]any{}})
			if err == nil || result != nil || !strings.Contains(err.Error(), "validating tool output") {
				t.Fatalf("invalid output result=%+v err=%v", result, err)
			}
		})
	}
}

func setMCPRemoteLinkFailure(t *testing.T, graph *app.JiraIssueGraphResult) {
	t.Helper()
	status := 403
	found := false
	for index := range graph.Sources {
		if graph.Sources[index].Kind != "remote_links" {
			continue
		}
		graph.Sources[index].Status = domain.ArtifactSourceForbidden
		graph.Sources[index].Complete = false
		graph.Sources[index].Failure = &domain.ArtifactGraphSourceFailure{
			Class: domain.ArtifactSourceFailurePermission, HTTPStatus: &status,
		}
		found = true
	}
	if !found {
		t.Fatal("MCP graph fixture omitted remote_links")
	}
	graph.Complete = false
	graph.Summary.IncompleteSourceCount = 1
	graph.Summary.SourceStatusCounts["empty"]--
	graph.Summary.SourceStatusCounts["forbidden"]++
	graph.Warnings = []string{"one or more requested graph sources are incomplete"}
}
