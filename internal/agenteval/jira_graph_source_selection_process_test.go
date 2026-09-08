package agenteval

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

func TestJiraGraphSourceSelectionMCPDrivesSelectedATLBinary(t *testing.T) {
	for _, projection := range []string{"full", "compact"} {
		t.Run(projection, func(t *testing.T) {
			fixture := jiraGraphSourceSelectionFixture(t)
			arguments := jiraGraphSourceSelectionMCPArguments(projection)
			invocation := mustMCPInvocation(t, jiraArtifactGraphMCPTool, arguments)
			process := startRepositoryJiraGraphProcess(t, fixture, invocation,
				[]string{"issue"}, []map[string]string{jiraGraphSourceSelectionIssueQuery()})
			called := callRepositoryJiraGraph(t, process, invocation)
			if called.IsError {
				t.Fatalf("selected-source %s graph failed: text_items=%d", projection, len(called.TextContent))
			}
			assertRepositoryMCPTextMatchesStructured(t, called)

			selection := decodeJiraGraphSourceSelectionDocument(t, called.StructuredContent, false)
			if !slices.Equal(selection.Selected, []string{"issue_links", "attachments"}) ||
				!slices.Equal(selection.Snapshot.Fields, []string{"summary", "issuelinks", "attachment"}) ||
				selection.Snapshot.Properties {
				t.Fatalf("selected-source %s projection drifted: %+v", projection, selection)
			}
			if projection == "full" {
				graph, err := DecodeJiraIssueGraphView(bytes.NewReader(called.StructuredContent))
				if err != nil {
					t.Fatalf("decode selected-source full graph: %v", err)
				}
				assertJiraGraphSelectedSourceRows(t, graph.Sources, selection.Selected)
			} else {
				assertJiraGraphCompactSelectedSourceRows(t, called.StructuredContent, selection.Selected)
			}

			assertJiraGraphSourceSelectionProcessAccounting(t, process, "MCP")
		})
	}
}

func TestJiraGraphSourceSelectionCLIDrivesSelectedATLBinary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		flagValues []string
	}{
		{name: "repeated", flagValues: []string{"issue_links", "attachments"}},
		{name: "comma separated", flagValues: []string{"issue_links,attachments"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"jira", "issue", "graph", "AG-41"}
			for _, value := range tc.flagValues {
				args = append(args, "--include-sources", value)
			}
			args = append(args, "--strict")
			policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{
				Name: "jira_graph_selected", Command: []string{"jira", "issue", "graph"},
				Positionals: []CLIArgumentRule{{Values: []string{"AG-41"}}},
				Flags: []CLIFlagRule{
					{Name: "--include-sources", Values: slices.Clone(tc.flagValues), Required: true, Occurrences: len(tc.flagValues)},
					{Name: "--strict", Required: true},
				},
				MaxInvocations: 1,
			}}}
			process := startJiraGraphSourceSelectionCLIProcess(t, jiraGraphSourceSelectionFixture(t), policy)
			called, err := process.RunCLIJSON(t.Context(), args...)
			if err != nil || called.ExitCode != 0 || len(called.Stderr) != 0 {
				t.Fatalf("selected-source strict CLI failed: result=%+v err=%v", called, err)
			}
			selection := decodeJiraGraphSourceSelectionDocument(t, called.JSON, false)
			assertJiraGraphDocumentSelectedSourceRows(t, called.JSON, selection.Selected, true)
			assertJiraGraphSourceSelectionProcessAccounting(t, process, "CLI")
		})
	}
}

func TestJiraGraphSourceSelectionMCPRejectsInvalidInputBeforeBackend(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arguments map[string]any
	}{
		{name: "explicit empty include", arguments: map[string]any{"key": "AG-41", "include_sources": []string{}}},
		{name: "unknown", arguments: map[string]any{"key": "AG-41", "include_sources": []string{"unknown"}}},
		{name: "development without opt in", arguments: map[string]any{"key": "AG-41", "include_sources": []string{"development"}}},
		{name: "development excluded with opt in", arguments: map[string]any{
			"key": "AG-41", "include_development": true, "exclude_sources": []string{"development"},
		}},
		{name: "empty selected result", arguments: map[string]any{
			"key": "AG-41", "include_sources": []string{"comments"}, "exclude_sources": []string{"comments"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invocation := mustMCPInvocation(t, jiraArtifactGraphMCPTool, tc.arguments)
			process := startRepositoryJiraGraphProcess(t, jiraGraphSourceSelectionFixture(t), invocation,
				[]string{"issue"}, []map[string]string{jiraGraphSourceSelectionIssueQuery()})
			called := callRepositoryJiraGraph(t, process, invocation)
			if !called.IsError {
				t.Fatal("invalid source selection reached the graph reader")
			}
			summary := process.Summary()
			if len(summary.HTTPMethods) != 0 || summary.UnexpectedRequests != 0 ||
				summary.DuplicateRequests != 0 || process.RequestSequenceComplete() {
				t.Fatalf("invalid source selection reached backend: summary=%+v", summary)
			}
		})
	}
}

func jiraGraphSourceSelectionFixture(t *testing.T) MockFixture {
	t.Helper()
	fixture := loadRepositoryMockFixture(t, "../../benchmarks/agent-eval/jira-artifact-graph-mcp/fixture.json")
	fixture.Routes = slices.Clone(fixture.Routes[:1])
	fixture.RequestSequence = nil
	return fixture
}

func jiraGraphSourceSelectionMCPArguments(projection string) map[string]any {
	arguments := map[string]any{
		"key": "AG-41", "depth": 0, "include_sources": []string{"issue_links", "attachments"},
		"max_nodes": 12, "max_edges": 16, "max_requests": 8, "max_bytes": 65536,
	}
	if projection == "compact" {
		arguments["projection"] = "compact"
		arguments["select"] = []string{"none"}
	}
	return arguments
}

func jiraGraphSourceSelectionIssueQuery() map[string]string {
	return map[string]string{"expand": "names,schema", "fields": "summary,issuelinks,attachment"}
}

func startJiraGraphSourceSelectionCLIProcess(t *testing.T, fixture MockFixture, policy CLICommandPolicy) *SyntheticATLProcess {
	t.Helper()
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	prepared.Routes[0].Name = "issue"
	prepared.Routes[0].QueryEquals = jiraGraphSourceSelectionIssueQuery()
	prepared.Routes[0].closedQuery = true
	prepared.RequestSequence = []string{"issue"}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare source-selection CLI fixture: %v", err)
	}
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepared,
		ScratchRoot: privateSyntheticATLScratch(t), CLIPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close source-selection CLI process: %v", err)
		}
	})
	return process
}

func decodeJiraGraphSourceSelectionDocument(t *testing.T, data []byte, includeDevelopment bool) JiraIssueGraphSourceSelection {
	t.Helper()
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		t.Fatalf("source-selection document has duplicate JSON members: %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		t.Fatalf("source-selection document is not an object: %v", err)
	}
	raw, ok := root["source_selection"]
	if !ok || jiraGraphWireNull(raw) {
		t.Fatal("source-selection document omitted its non-null qualification")
	}
	if err := validateJiraGraphSourceSelectionMembers(raw); err != nil {
		t.Fatalf("source-selection shape is invalid: %v", err)
	}
	var selection JiraIssueGraphSourceSelection
	if err := decodeStrict(bytes.NewReader(raw), &selection); err != nil {
		t.Fatalf("decode source selection: %v", err)
	}
	if _, err := validateJiraGraphSourceSelection(&selection, includeDevelopment); err != nil {
		t.Fatalf("reconcile source selection: %v", err)
	}
	return selection
}

func assertJiraGraphSelectedSourceRows(t *testing.T, sources []JiraIssueGraphSource, selected []string) {
	t.Helper()
	got := make([]string, len(sources))
	for index, source := range sources {
		got[index] = source.Kind
	}
	if !slices.Equal(got, selected) {
		t.Fatalf("source rows=%v want selected=%v", got, selected)
	}
}

func assertJiraGraphCompactSelectedSourceRows(t *testing.T, data []byte, selected []string) {
	assertJiraGraphDocumentSelectedSourceRows(t, data, selected, false)
}

func assertJiraGraphDocumentSelectedSourceRows(t *testing.T, data []byte, selected []string, requireAll bool) {
	t.Helper()
	var root struct {
		Sources []JiraIssueGraphSource `json:"sources"`
	}
	if err := decodeStrict(bytes.NewReader(data), &root); err == nil {
		t.Fatal("compact document unexpectedly matched a source-only closed object")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if err := decodeStrict(bytes.NewReader(document["sources"]), &root.Sources); err != nil {
		t.Fatalf("decode compact source rows: %v", err)
	}
	if requireAll {
		assertJiraGraphSelectedSourceRows(t, root.Sources, selected)
		return
	}
	for _, source := range root.Sources {
		if !slices.Contains(selected, source.Kind) {
			t.Fatalf("compact retained omitted source row %q; selected=%v", source.Kind, selected)
		}
	}
}

func assertJiraGraphSourceSelectionProcessAccounting(t *testing.T, process *SyntheticATLProcess, surface string) {
	t.Helper()
	summary := process.Summary()
	if !process.RequestSequenceComplete() || !equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": 1}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 {
		t.Fatalf("selected-source %s process accounting drifted: summary=%+v complete=%t",
			surface, summary, process.RequestSequenceComplete())
	}
	if surface == "MCP" && (!equalHTTPMethods(summary.MCPInvocations, map[string]int{jiraArtifactGraphMCPTool: 1}) || len(summary.CLIInvocations) != 0) {
		t.Fatalf("selected-source MCP invocation accounting drifted: %+v", summary)
	}
	if surface == "CLI" && (summary.CLIInvocations["jira_graph_selected"] != 1 || len(summary.MCPInvocations) != 0) {
		t.Fatalf("selected-source CLI invocation accounting drifted: %+v", summary)
	}
}
