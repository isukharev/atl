package agenteval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
)

const (
	confluenceCommentListTool     = "confluence_comment_list"
	confluenceCommentThreadTool   = "confluence_comment_thread"
	confluenceCommentListFamily   = "confluence.comment.list"
	confluenceCommentThreadFamily = "confluence.comment.thread"
)

func TestConfluenceCommentRoutingCorpusContracts(t *testing.T) {
	tests := []struct {
		directory   string
		repetitions int
		final       string
	}{
		{
			directory: "confluence-comment-routing-mcp", repetitions: 3,
			final: `{"schema_version":1,"page_version":7,"inventory_complete":true,"selected_comment_id":"5101","thread_complete":true,"thread_text":"Synthetic approval remains pending.","brief":"Synthetic thread read."}`,
		},
		{
			directory: "confluence-comment-routing-mcp-holdout", repetitions: 1,
			final: `{"schema_version":1,"inventory_count":1,"inventory_complete":false,"list_page_version":4,"thread_page_version":9,"thread_page_version_gated":false,"thread_complete":true,"thread_text":"Synthetic rollout is paused.","brief":"Synthetic inventory is partial and the fixed thread is complete."}`,
		},
	}
	for _, test := range tests {
		t.Run(test.directory, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			var baseline RunSpec
			for _, provider := range []string{"claude", "codex"} {
				loaded, err := resolveRunContract(filepath.Join(root, "run.mcp."+provider+".json"))
				if err != nil {
					t.Fatalf("load %s: %v", provider, err)
				}
				spec := loaded.spec
				if spec.EffectiveBackendMode() != BackendModeSynthetic || spec.EffectiveSurface() != SurfaceATLMCP ||
					spec.EffectiveToolTransport() != "mcp" || spec.Repetitions != test.repetitions ||
					!slices.Equal(spec.AllowedMCPTools, []string{confluenceCommentListTool, confluenceCommentThreadTool}) ||
					len(spec.AllowedTools) != 0 || len(spec.AllowedATLCommands) != 0 || len(spec.AllowedCLICommands) != 0 ||
					spec.AllowSyntheticWrites || spec.AllowLiveWrites {
					t.Fatalf("%s route authority drifted: %+v", provider, spec)
				}
				if loaded.scenario.Budgets.MaxRemoteWrites != 0 ||
					!slices.Equal(loaded.scenario.Budgets.AllowedHTTPMethods, []string{"GET"}) {
					t.Fatalf("%s read-only budget drifted: %+v", provider, loaded.scenario.Budgets)
				}
				if provider == "claude" {
					baseline = spec
				} else if !slices.Equal(baseline.AllowedMCPTools, spec.AllowedMCPTools) {
					t.Fatal("provider tool inventories diverged")
				}
				assertConfluenceCommentChecks(t, spec, []byte(test.final), test.directory)
			}
		})
	}
}

func TestConfluenceCommentRoutingFixturesDriveProductionMCP(t *testing.T) {
	tests := []struct {
		directory           string
		wantInventoryFull   bool
		wantListVersion     int
		wantThreadVersion   int
		wantThreadVersioned bool
		wantText            string
		wantDuplicates      int
	}{
		{"confluence-comment-routing-mcp", true, 7, 7, true, "Synthetic approval remains pending.", 1},
		{"confluence-comment-routing-mcp-holdout", false, 4, 9, false, "Synthetic rollout is paused.", 0},
	}
	for _, test := range tests {
		t.Run(test.directory, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			loaded, err := resolveRunContract(filepath.Join(root, "run.mcp.codex.json"))
			if err != nil {
				t.Fatal(err)
			}
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			backend, err := StartMockBackend(fixture)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(backend.Close)
			for name, value := range backend.Environment() {
				t.Setenv(name, value)
			}
			t.Setenv("ATL_CONFIG_DIR", t.TempDir())
			t.Setenv("ATL_READ_ONLY", "1")
			t.Setenv("ATL_NO_UPDATE", "1")
			client := connectRepositoryMCPClient(t)

			invocations := confluenceCommentExpectedInvocations(t, loaded.spec)
			var list app.ConfluenceCommentListView
			callConfluenceCommentTool(t, client, invocations[0], &list)
			if list.Complete != test.wantInventoryFull || list.PageVersion != test.wantListVersion ||
				list.PageVersionGated || list.Count != 1 {
				t.Fatalf("list result drifted: %+v", list)
			}

			var thread app.ConfluenceCommentThreadView
			callConfluenceCommentTool(t, client, invocations[1], &thread)
			if !thread.Complete || thread.PageVersion != test.wantThreadVersion ||
				thread.PageVersionGated != test.wantThreadVersioned || len(thread.Comments) != 1 ||
				thread.Comments[0].BodyText == nil || *thread.Comments[0].BodyText != test.wantText {
				t.Fatalf("thread result drifted: %+v", thread)
			}

			methods, unexpected, duplicates := backend.Summary()
			if !backend.RequestSequenceComplete() || unexpected != 0 || duplicates != test.wantDuplicates ||
				!equalHTTPMethods(methods, map[string]int{"GET": 6}) {
				t.Fatalf("backend route drifted: methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
		})
	}
}

func confluenceCommentExpectedInvocations(t *testing.T, spec RunSpec) []MCPInvocation {
	t.Helper()
	for _, check := range spec.Checks {
		if check.Name == "route_arguments" {
			invocations, ok := expectedMCPInvocations(check.Expected)
			if !ok || len(invocations) != 2 {
				t.Fatal("route_arguments is not an exact two-call route")
			}
			return invocations
		}
	}
	t.Fatal("route_arguments check is missing")
	return nil
}

func callConfluenceCommentTool(t *testing.T, client *mcp.ClientSession, invocation MCPInvocation, target any) {
	t.Helper()
	var arguments map[string]any
	if err := json.Unmarshal(invocation.Arguments, &arguments); err != nil {
		t.Fatal(err)
	}
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: invocation.Tool, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		message := ""
		if len(result.Content) > 0 {
			if text, ok := result.Content[0].(*mcp.TextContent); ok {
				message = text.Text
			}
		}
		t.Fatalf("%s returned an error: %s", invocation.Tool, message)
	}
	decodeRepositoryStructuredContent(t, result.StructuredContent, target)
}

func assertConfluenceCommentChecks(t *testing.T, spec RunSpec, final []byte, directory string) {
	t.Helper()
	expectedInvocations := confluenceCommentExpectedInvocations(t, spec)
	if len(expectedInvocations) != 2 {
		t.Fatalf("exact route=%v", expectedInvocations)
	}
	if expectedInvocations[0].Tool != confluenceCommentListTool || expectedInvocations[1].Tool != confluenceCommentThreadTool {
		t.Fatalf("inventory and expansion routes widened: %v", expectedInvocations)
	}
	var listArguments, threadArguments map[string]any
	if err := json.Unmarshal(expectedInvocations[0].Arguments, &listArguments); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expectedInvocations[1].Arguments, &threadArguments); err != nil {
		t.Fatal(err)
	}
	if directory == "confluence-comment-routing-mcp" {
		if listArguments["page_id"] != "9101" || threadArguments["page_id"] != "9101" ||
			threadArguments["expected_page_version"] != float64(7) {
			t.Fatalf("list-derived thread lost page/version binding: list=%v thread=%v", listArguments, threadArguments)
		}
	} else if listArguments["page_id"] != "9201" || threadArguments["page_id"] != "9202" ||
		threadArguments["comment_id"] != "6202" || threadArguments["expected_page_version"] != nil {
		t.Fatalf("externally fixed thread gained list provenance: list=%v thread=%v", listArguments, threadArguments)
	}
	families := []CapabilityFamilyMetric{
		{Family: confluenceCommentListFamily, Invocations: 1, Successes: 1},
		{Family: confluenceCommentThreadFamily, Invocations: 1, Successes: 1},
	}
	sequence := []string{confluenceCommentListFamily, confluenceCommentThreadFamily}
	checks, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 2, 0, 0, 0, nil, 0, 0,
		map[string]int{"GET": 6}, true, nil, families, true, sequence,
		expectedInvocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range spec.Checks {
		if !checks[check.Name] {
			t.Fatalf("load-bearing check %q failed on retained evidence: %v", check.Name, checks)
		}
	}

	wrongTool := slices.Clone(expectedInvocations)
	wrongTool[1].Tool = confluenceCommentListTool
	toolChecks, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 2, 0, 0, 0, nil, 0, 0,
		map[string]int{"GET": 6}, true, nil, families, true, sequence,
		wrongTool, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if toolChecks["route_arguments"] {
		t.Fatal("wrong tool selection passed the exact route oracle")
	}

	mutated := slices.Clone(expectedInvocations)
	var arguments map[string]any
	if err := json.Unmarshal(mutated[1].Arguments, &arguments); err != nil {
		t.Fatal(err)
	}
	if _, gated := arguments["expected_page_version"]; gated {
		arguments["expected_page_version"] = 8
	} else {
		arguments["expected_page_version"] = 9
	}
	mutated[1], _ = newMCPInvocation(confluenceCommentThreadTool, arguments)
	mutatedChecks, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 2, 0, 0, 0, nil, 0, 0,
		map[string]int{"GET": 6}, true, nil, families, true, sequence, mutated, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if mutatedChecks["route_arguments"] {
		t.Fatal("version-gate widening passed the exact route oracle")
	}

	var answer map[string]any
	if err := json.Unmarshal(final, &answer); err != nil {
		t.Fatal(err)
	}
	answer["inventory_complete"] = !answer["inventory_complete"].(bool)
	wrongCompleteness, _ := json.Marshal(answer)
	completenessChecks, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, wrongCompleteness, "", 2, 0, 0, 0, nil, 0, 0,
		map[string]int{"GET": 6}, true, nil, families, true, sequence,
		expectedInvocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if completenessChecks["inventory_complete_exact"] {
		t.Fatal("completeness widening passed the exact semantic oracle")
	}
}
