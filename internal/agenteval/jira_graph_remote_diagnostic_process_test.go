package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const jiraGraphRemoteDiagnosticCanary = "PRIVATE-REMOTE-LINK-RESPONSE-CANARY"

func TestJiraGraphRemoteDiagnosticMCPDrivesSelectedATLBinary(t *testing.T) {
	for _, projection := range []string{"full", "compact"} {
		t.Run(projection, func(t *testing.T) {
			arguments := jiraGraphRemoteDiagnosticMCPArguments(projection)
			invocation := mustMCPInvocation(t, jiraArtifactGraphMCPTool, arguments)
			process := startRepositoryJiraGraphProcess(
				t, jiraGraphRemoteDiagnosticFixture(t), invocation,
				[]string{"issue", "remote_links"}, []map[string]string{jiraGraphRemoteDiagnosticIssueQuery(), {}},
			)
			called := callRepositoryJiraGraph(t, process, invocation)
			if called.IsError {
				t.Fatalf("remote-link diagnostic %s MCP failed", projection)
			}
			assertRepositoryMCPTextMatchesStructured(t, called)
			assertJiraGraphRemoteDiagnosticDocument(t, called.StructuredContent, projection, true)
			if bytes.Contains(called.StructuredContent, []byte(jiraGraphRemoteDiagnosticCanary)) ||
				strings.Contains(strings.Join(called.TextContent, "\n"), jiraGraphRemoteDiagnosticCanary) {
				t.Fatal("MCP exposed the remote-link response canary")
			}
			assertJiraGraphRemoteDiagnosticProcessAccounting(t, process, "MCP")
		})
	}
}

func TestJiraGraphRemoteDiagnosticCLIJSONDrivesSelectedATLBinary(t *testing.T) {
	for _, projection := range []string{"full", "compact"} {
		t.Run(projection, func(t *testing.T) {
			args := []string{"jira", "issue", "graph", "AG-41", "--include-sources", "remote_links"}
			flags := []CLIFlagRule{{Name: "--include-sources", Values: []string{"remote_links"}, Required: true, Occurrences: 1}}
			if projection == "compact" {
				args = append(args, "--projection", "compact", "--select", "none")
				flags = append(flags,
					CLIFlagRule{Name: "--projection", Values: []string{"compact"}, Required: true, Occurrences: 1},
					CLIFlagRule{Name: "--select", Values: []string{"none"}, Required: true, Occurrences: 1},
				)
			}
			process := startJiraGraphRemoteDiagnosticCLIProcess(t, CLICommandPolicy{
				SchemaVersion: CLICommandPolicySchemaVersion,
				Rules: []CLICommandRule{{
					Name: "remote_diagnostic_" + projection, Command: []string{"jira", "issue", "graph"},
					Positionals: []CLIArgumentRule{{Values: []string{"AG-41"}}}, Flags: flags, MaxInvocations: 1,
				}},
			})
			called, err := process.RunCLIJSON(t.Context(), args...)
			if err != nil || called.ExitCode != 0 || len(called.Stderr) != 0 {
				t.Fatalf("remote-link diagnostic %s CLI failed: result=%+v err=%v", projection, called, err)
			}
			assertJiraGraphRemoteDiagnosticDocument(t, called.JSON, projection, false)
			if bytes.Contains(called.JSON, []byte(jiraGraphRemoteDiagnosticCanary)) {
				t.Fatal("CLI JSON exposed the remote-link response canary")
			}
			assertJiraGraphRemoteDiagnosticProcessAccounting(t, process, "remote_diagnostic_"+projection)
		})
	}
}

func TestJiraGraphRemoteDiagnosticCLITextIsClosedAndSelected(t *testing.T) {
	policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{
		Name: "remote_diagnostic_text", Command: []string{"jira", "issue", "graph"},
		Positionals: []CLIArgumentRule{{Values: []string{"AG-41"}}},
		Flags: []CLIFlagRule{
			{Name: "--include-sources", Values: []string{"remote_links"}, Required: true, Occurrences: 1},
			{Name: "-o", Values: []string{"text"}, Required: true, Occurrences: 1},
		},
		MaxInvocations: 1,
	}}}
	process := startJiraGraphRemoteDiagnosticCLIProcess(t, policy)
	called, err := process.RunCLIBytes(t.Context(), "jira", "issue", "graph", "AG-41", "--include-sources", "remote_links", "-o", "text")
	if err != nil || called.ExitCode != 0 || len(called.Stderr) != 0 {
		t.Fatalf("remote-link diagnostic text CLI failed: result=%+v err=%v", called, err)
	}
	text := string(called.Stdout)
	for _, expected := range []string{"| Failure | HTTP status |", "| permission | 403 |"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("text omitted %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, jiraGraphRemoteDiagnosticCanary) {
		t.Fatal("CLI text exposed the remote-link response canary")
	}
	assertJiraGraphRemoteDiagnosticProcessAccounting(t, process, "remote_diagnostic_text")
}

func jiraGraphRemoteDiagnosticFixture(t *testing.T) MockFixture {
	t.Helper()
	fixture := loadRepositoryMockFixture(t, "../../benchmarks/agent-eval/jira-artifact-graph-mcp/fixture.json")
	fixture.Routes = []MockRoute{fixture.Routes[0], fixture.Routes[3]}
	fixture.Routes[1].Status = 403
	fixture.Routes[1].Body = json.RawMessage(`{"error":"` + jiraGraphRemoteDiagnosticCanary + `"}`)
	fixture.RequestSequence = nil
	return fixture
}

func jiraGraphRemoteDiagnosticMCPArguments(projection string) map[string]any {
	arguments := map[string]any{
		"key": "AG-41", "depth": 0, "include_sources": []string{"remote_links"},
		"max_nodes": 12, "max_edges": 16, "max_requests": 8, "max_bytes": 65536,
	}
	if projection == "compact" {
		arguments["projection"] = "compact"
		arguments["select"] = []string{"none"}
	}
	return arguments
}

func jiraGraphRemoteDiagnosticIssueQuery() map[string]string {
	return map[string]string{"expand": "names,schema", "fields": "summary"}
}

func startJiraGraphRemoteDiagnosticCLIProcess(t *testing.T, policy CLICommandPolicy) *SyntheticATLProcess {
	t.Helper()
	fixture := jiraGraphRemoteDiagnosticFixture(t)
	fixture.Routes[0].Name = "issue"
	fixture.Routes[0].QueryEquals = jiraGraphRemoteDiagnosticIssueQuery()
	fixture.Routes[0].closedQuery = true
	fixture.Routes[1].Name = "remote_links"
	fixture.Routes[1].QueryEquals = map[string]string{}
	fixture.Routes[1].closedQuery = true
	fixture.RequestSequence = []string{"issue", "remote_links"}
	if err := fixture.Validate(); err != nil {
		t.Fatalf("prepare remote-link diagnostic CLI fixture: %v", err)
	}
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: fixture,
		ScratchRoot: privateSyntheticATLScratch(t), CLIPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close remote-link diagnostic CLI process: %v", err)
		}
	})
	return process
}

func assertJiraGraphRemoteDiagnosticDocument(t *testing.T, data []byte, projection string, strictMCPFull bool) {
	t.Helper()
	var sources []JiraIssueGraphSource
	if projection == "full" && strictMCPFull {
		view, err := DecodeJiraIssueGraphView(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("decode full remote-link diagnostic: %v", err)
		}
		sources = view.Sources
	} else {
		var document map[string]json.RawMessage
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		if err := decodeStrict(bytes.NewReader(document["sources"]), &sources); err != nil {
			t.Fatalf("decode compact remote-link diagnostic sources: %v", err)
		}
	}
	if len(sources) != 1 || sources[0].Kind != "remote_links" || sources[0].Status != "forbidden" || sources[0].Complete ||
		sources[0].Failure == nil || sources[0].Failure.Class != "permission" || sources[0].Failure.HTTPStatus == nil || *sources[0].Failure.HTTPStatus != 403 {
		t.Fatalf("%s remote-link diagnostic sources=%+v", projection, sources)
	}
}

func assertJiraGraphRemoteDiagnosticProcessAccounting(t *testing.T, process *SyntheticATLProcess, surface string) {
	t.Helper()
	summary := process.Summary()
	if !process.RequestSequenceComplete() || !equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": 2}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 {
		t.Fatalf("remote-link diagnostic %s accounting=%+v complete=%t", surface, summary, process.RequestSequenceComplete())
	}
	if surface == "MCP" {
		if !equalHTTPMethods(summary.MCPInvocations, map[string]int{jiraArtifactGraphMCPTool: 1}) || len(summary.CLIInvocations) != 0 {
			t.Fatalf("remote-link diagnostic MCP invocation=%+v", summary)
		}
	} else if summary.CLIInvocations[surface] != 1 || len(summary.MCPInvocations) != 0 {
		t.Fatalf("remote-link diagnostic CLI invocation=%+v", summary)
	}
}
