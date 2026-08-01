package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func TestJiraIssueGraphToolUsesClosedDefaultsAndOmitsNarrativeLabels(t *testing.T) {
	const marker = "PRIVATE-SUMMARY PRIVATE-PAGE-TITLE PRIVATE-FILENAME PRIVATE-REMOTE-TITLE"
	opts := defaultMCPGraphOptions(0)
	reader := &recordingJiraReader{graphResult: validMCPGraphResult("PROJ-1", opts, marker)}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result := callToolOK(t, client, "jira_issue_graph", map[string]any{"key": "PROJ-1"})
	if reader.graphKey != "PROJ-1" || !reflect.DeepEqual(reader.graphOpts, opts) || reader.graphOpts.ResolveConfluence {
		t.Fatalf("graph call key=%q opts=%+v want=%+v", reader.graphKey, reader.graphOpts, opts)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), marker) || strings.Contains(string(encoded), `"label"`) {
		t.Fatalf("narrative graph label crossed MCP projection: %s", encoded)
	}
	var out JiraIssueGraphOutput
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	if out.SchemaVersion != 2 || out.RootID != "jira:issue:PROJ-1" || !out.Complete ||
		out.Bounds.RequestedDepth != 0 || out.Bounds.MaxNodes != 50 || out.Bounds.MaxEdges != 200 ||
		out.Bounds.MaxEvidence != 500 || out.Bounds.MaxRequests != 50 || out.Bounds.MaxResponseBytes != 16<<20 {
		t.Fatalf("output=%+v", out)
	}
}

func TestJiraIssueGraphToolRejectsInvalidInputBeforeJiraConstruction(t *testing.T) {
	constructed := 0
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) {
			constructed++
			return nil, errors.New("unexpected construction")
		},
	}))
	defer closeSessions()

	tests := []map[string]any{
		{},
		{"key": "proj-1"},
		{"key": " PROJ-1"},
		{"key": "PROJ-1", "depth": -1},
		{"key": "PROJ-1", "depth": 3},
		{"key": "PROJ-1", "max_nodes": 101},
		{"key": "PROJ-1", "max_edges": 501},
		{"key": "PROJ-1", "max_requests": 101},
		{"key": "PROJ-1", "max_bytes": 1023},
		{"key": "PROJ-1", "max_bytes": (1 << 20) + 1},
		{"key": "PROJ-1", "resolve_confluence": true},
		{"key": "PROJ-1", "include_development": false},
	}
	for _, args := range tests {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "jira_issue_graph", Arguments: args})
		if err != nil {
			t.Fatalf("args=%v CallTool: %v", args, err)
		}
		if !result.IsError || result.StructuredContent != nil {
			t.Fatalf("args=%v result=%+v", args, result)
		}
	}
	if constructed != 0 {
		t.Fatalf("invalid graph inputs constructed Jira %d times", constructed)
	}
}

func TestJiraIssueGraphToolForwardsCustomBoundsAndReturnsIncompleteGraph(t *testing.T) {
	opts := app.JiraIssueGraphOptions{
		Depth: 2, MaxNodes: 75, MaxEdges: 350, MaxEvidence: 500,
		MaxRequests: 80, MaxResponseBytes: 16 << 20,
	}
	graph := validMCPGraphResult("PROJ-9", opts, "private summary")
	graph.Sources[0].Status = domain.ArtifactSourcePartial
	graph.Sources[0].Complete = false
	graph.Sources[0].PartialReason = domain.ArtifactPartialRequestFailed
	graph.Complete = false
	graph.Summary.IncompleteSourceCount = 1
	graph.Summary.SourceStatusCounts["empty"] = 7
	graph.Summary.SourceStatusCounts["partial"] = 1
	graph.Warnings = []string{"one or more requested graph sources are incomplete"}
	reader := &recordingJiraReader{graphResult: graph}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result := callToolOK(t, client, "jira_issue_graph", map[string]any{
		"key": "PROJ-9", "depth": 2, "max_nodes": 75, "max_edges": 350,
		"max_requests": 80, "max_bytes": 1 << 20,
	})
	if !reflect.DeepEqual(reader.graphOpts, opts) || reader.graphOpts.ResolveConfluence {
		t.Fatalf("opts=%+v want=%+v", reader.graphOpts, opts)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	var out JiraIssueGraphOutput
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	if out.Complete || len(out.Sources) != 8 || out.Sources[0].PartialReason != domain.ArtifactPartialRequestFailed {
		t.Fatalf("incomplete output=%+v", out)
	}
}

func TestJiraIssueGraphToolRejectsFutureDevelopmentAndOversizeOutput(t *testing.T) {
	t.Run("development", func(t *testing.T) {
		opts := defaultMCPGraphOptions(0)
		graph := validMCPGraphResult("PROJ-1", opts, "private")
		graph.Sources[0].Kind = "development"
		reader := &recordingJiraReader{graphResult: graph}
		client, closeSessions := connectTestClient(t, New("test", Dependencies{
			Jira: func() (JiraReader, error) { return reader, nil },
		}))
		defer closeSessions()
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "jira_issue_graph", Arguments: map[string]any{"key": "PROJ-1"},
		})
		if err != nil || !result.IsError || result.StructuredContent != nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if strings.Contains(text, "development") || !strings.Contains(text, "failed validation") {
			t.Fatalf("error=%q", text)
		}
	})

	t.Run("max bytes", func(t *testing.T) {
		opts := defaultMCPGraphOptions(0)
		reader := &recordingJiraReader{graphResult: validMCPGraphResult("PROJ-1", opts, "private")}
		client, closeSessions := connectTestClient(t, New("test", Dependencies{
			Jira: func() (JiraReader, error) { return reader, nil },
		}))
		defer closeSessions()
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "jira_issue_graph", Arguments: map[string]any{"key": "PROJ-1", "max_bytes": 1024},
		})
		if err != nil || !result.IsError || result.StructuredContent != nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if strings.Contains(text, "private") || !strings.Contains(text, "exceeds max_bytes") || !strings.Contains(text, "narrow_graph_or_raise_bound") {
			t.Fatalf("error=%q", text)
		}
	})
}

func TestJiraIssueGraphToolSchemaHasNoExpansionOrNarrativeFields(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()
	for _, guidance := range []string{"jira_issue_graph", "depth 0..2", "performs no Confluence reads", "omits labels", "Development", "never infer zero"} {
		if !strings.Contains(client.InitializeResult().Instructions, guidance) {
			t.Errorf("instructions omit %q", guidance)
		}
	}
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != "jira_issue_graph" {
			continue
		}
		input, _ := json.Marshal(tool.InputSchema)
		output, _ := json.Marshal(tool.OutputSchema)
		for _, forbidden := range []string{"resolve_confluence", "include_development", "development", "snippet", "raw"} {
			if strings.Contains(string(input), forbidden) {
				t.Errorf("input schema contains %q: %s", forbidden, input)
			}
		}
		for _, forbidden := range []string{`"label"`, "snippet", "narrative", "development"} {
			if strings.Contains(string(output), forbidden) {
				t.Errorf("output schema contains %q: %s", forbidden, output)
			}
		}
		return
	}
	t.Fatal("jira_issue_graph not registered")
}

func TestJiraIssueGraphErrorsAreStatic(t *testing.T) {
	const marker = "PRIVATE-BACKEND-BODY-OR-KEY"
	tests := []error{
		fmt.Errorf("%w: %s", domain.ErrUsage, marker),
		fmt.Errorf("%w: %s", domain.ErrConfig, marker),
		fmt.Errorf("%w: %s", domain.ErrAuth, marker),
		fmt.Errorf("%w: %s", domain.ErrForbidden, marker),
		fmt.Errorf("%w: %s", domain.ErrNotFound, marker),
		fmt.Errorf("%w: %s", domain.ErrCheckFailed, marker),
		fmt.Errorf("%w: %s", domain.ErrOutputLimit, marker),
		&httpx.APIError{Status: 503, Body: marker},
		errors.New(marker),
	}
	for _, test := range tests {
		projected := classifiedJiraIssueGraphRead(test)
		if projected == nil || strings.Contains(projected.Error(), marker) {
			t.Fatalf("error %T crossed private text: %v", test, projected)
		}
	}
}

func TestJiraIssueGraphEvidenceProjectionRejectsNarrativeIdentityTokens(t *testing.T) {
	if safeJiraIssueGraphEvidence(domain.ArtifactGraphEvidence{SourceID: "PRIVATE-BACKEND-BODY"}) ||
		safeJiraIssueGraphEvidence(domain.ArtifactGraphEvidence{JSONPointer: "/fields/api_token"}) {
		t.Fatal("narrative or secret-like evidence identity passed the MCP allowlist")
	}
	if !safeJiraIssueGraphEvidence(domain.ArtifactGraphEvidence{SourceID: "subtasks/0", JSONPointer: "/fields/subtasks/0"}) {
		t.Fatal("canonical structured hierarchy evidence was rejected")
	}
}

func defaultMCPGraphOptions(depth int) app.JiraIssueGraphOptions {
	return app.JiraIssueGraphOptions{
		Depth: depth, MaxNodes: 50, MaxEdges: 200, MaxEvidence: 500,
		MaxRequests: 50, MaxResponseBytes: 16 << 20,
	}
}

func validMCPGraphResult(key string, opts app.JiraIssueGraphOptions, label string) *app.JiraIssueGraphResult {
	depth := 0
	kinds := []string{"issue_fields", "issue_links", "hierarchy", "attachments", "issue_properties", "comments", "worklogs", "remote_links"}
	sources := make([]domain.ArtifactGraphSource, 0, len(kinds))
	for _, kind := range kinds {
		stability := domain.ArtifactStabilityPublicAPI
		if kind == "issue_properties" {
			stability = domain.ArtifactStabilityExperimentalAPI
		}
		sources = append(sources, domain.ArtifactGraphSource{
			NodeID: "jira:issue:" + key, NodeDepth: &depth, Kind: kind, Requested: true,
			Status: domain.ArtifactSourceEmpty, Complete: true, Stability: stability,
		})
	}
	return &app.JiraIssueGraphResult{
		SchemaVersion: 2, RootID: "jira:issue:" + key, Complete: true,
		Bounds: app.JiraIssueGraphBounds{
			RequestedDepth: opts.Depth, MaxNodes: opts.MaxNodes, MaxEdges: opts.MaxEdges,
			MaxEvidence: opts.MaxEvidence, MaxSourceBytes: 1 << 20,
			ExpandedNodes: 1, AttemptedNodes: 1, MaxRequests: opts.MaxRequests,
			MaxResponseBytes: opts.MaxResponseBytes, MaxSources: opts.MaxNodes*8 + 1,
			MaxFrontier: opts.MaxNodes,
		},
		Summary: app.JiraIssueGraphSummary{
			NodeCount: 1, SourceCount: 8,
			SourceStatusCounts:    map[string]int{"complete": 0, "empty": 8, "partial": 0, "forbidden": 0, "unsupported": 0, "skipped": 0},
			NodeCountMatchesNodes: true, EdgeCountMatchesEdges: true,
			EvidenceCountMatchesEdges: true, SourceCountMatchesSources: true,
			SourceStatusCountsMatch: true, IncompleteCountMatches: true,
			ExpandedCountMatchesNodes: true, CompleteMatchesSources: true,
		},
		Nodes: []domain.ArtifactGraphNode{{
			ID: "jira:issue:" + key, Kind: "jira_issue", Service: "jira", ExternalID: key,
			Label: label, State: domain.ArtifactNodeResolved, Expanded: true, Depth: 0,
			Stability: domain.ArtifactStabilityPublicAPI,
		}},
		Edges: []domain.ArtifactGraphEdge{}, Sources: sources,
		Frontier: []app.JiraIssueGraphFrontierItem{}, Warnings: []string{},
	}
}
