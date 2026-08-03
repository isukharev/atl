package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
)

type jiraPaginatedSearchExpectation struct {
	directory        string
	query            string
	limit            int
	cursors          []string
	keys             [][]string
	statuses         []string
	updated          []string
	statusCounts     []map[string]any
	expectedRequests int
}

func TestRepositoryJiraPaginatedSearchFixturesDriveProviderOracles(t *testing.T) {
	tests := []struct {
		name        string
		expectation jiraPaginatedSearchExpectation
	}{
		{
			name: "three-page primary",
			expectation: jiraPaginatedSearchExpectation{
				directory: "jira-paginated-search-mcp",
				query:     "project = NOVA AND labels = readiness ORDER BY updated DESC, key ASC",
				limit:     250,
				cursors:   []string{"", "2", "4"},
				keys: [][]string{
					{"NOVA-6", "NOVA-5"},
					{"NOVA-4", "NOVA-3"},
					{"NOVA-2", "NOVA-1"},
				},
				statuses: []string{"In Progress", "Blocked", "Done", "In Progress", "To Do", "Done"},
				updated: []string{
					"2026-07-22T10:00:00.000+0000",
					"2026-07-21T10:00:00.000+0000",
					"2026-07-20T10:00:00.000+0000",
					"2026-07-19T10:00:00.000+0000",
					"2026-07-18T10:00:00.000+0000",
					"2026-07-17T10:00:00.000+0000",
				},
				statusCounts: []map[string]any{
					{"status": "Blocked", "count": 1},
					{"status": "Done", "count": 2},
					{"status": "In Progress", "count": 2},
					{"status": "To Do", "count": 1},
				},
				expectedRequests: 3,
			},
		},
		{
			name: "two-page holdout",
			expectation: jiraPaginatedSearchExpectation{
				directory: "jira-paginated-search-mcp-holdout",
				query:     "project = ORBIT AND labels = launch ORDER BY priority DESC, key ASC",
				limit:     125,
				cursors:   []string{"", "3"},
				keys: [][]string{
					{"ORBIT-15", "ORBIT-11", "ORBIT-9"},
					{"ORBIT-7", "ORBIT-2"},
				},
				statuses: []string{"Review", "Open", "Paused", "Closed", "Open"},
				updated: []string{
					"2026-07-23T09:00:00.000+0000",
					"2026-07-22T09:00:00.000+0000",
					"2026-07-21T09:00:00.000+0000",
					"2026-07-20T09:00:00.000+0000",
					"2026-07-19T09:00:00.000+0000",
				},
				statusCounts: []map[string]any{
					{"status": "Closed", "count": 1},
					{"status": "Open", "count": 2},
					{"status": "Paused", "count": 1},
					{"status": "Review", "count": 1},
				},
				expectedRequests: 2,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expected := test.expectation
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", expected.directory)
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			backend, err := StartMockBackend(fixture)
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()

			t.Setenv("ATL_CONFIG_DIR", t.TempDir())
			t.Setenv("ATL_JIRA_PAT", "synthetic-token")
			service, err := app.NewJira(
				&config.Config{JiraURL: backend.Environment()["ATL_JIRA_URL"]},
				"benchmark-contract",
			)
			if err != nil {
				t.Fatal(err)
			}

			columns := []string{"key", "summary", "status", "updated"}
			var pages []map[string]any
			var issues []map[string]any
			flatIndex := 0
			for pageIndex, cursor := range expected.cursors {
				page, searchErr := service.SearchIssueListView(
					context.Background(), expected.query, columns, "", expected.limit, cursor,
				)
				if searchErr != nil {
					t.Fatal(searchErr)
				}
				if page.Source.Kind != "jql" ||
					page.Selection["jql"] != expected.query ||
					!slices.Equal(page.Projection.Columns, columns) ||
					!slices.Equal(page.Projection.Fields, []string{"summary", "status", "updated"}) ||
					page.Projection.Ordering != "jql-order" ||
					page.Page.Count != len(expected.keys[pageIndex]) {
					t.Fatalf("issue-list page %d metadata drifted: %+v", pageIndex, page)
				}
				terminal := pageIndex == len(expected.cursors)-1
				if page.Page.Complete != terminal || page.Page.Truncated == terminal {
					t.Fatalf("issue-list page %d completeness drifted: %+v", pageIndex, page.Page)
				}
				nextCursor := ""
				if !terminal {
					nextCursor = expected.cursors[pageIndex+1]
				}
				if !equalJiraSearchCursor(page.Page.NextCursor, nextCursor) {
					t.Fatalf("issue-list page %d next cursor=%v want=%q", pageIndex, page.Page.NextCursor, nextCursor)
				}

				pageKeys := make([]string, len(page.Rows))
				for rowIndex, row := range page.Rows {
					pageKeys[rowIndex] = row.Key
					if row.Position != rowIndex ||
						row.Key != expected.keys[pageIndex][rowIndex] ||
						row.Values["status"] != expected.statuses[flatIndex] ||
						row.Values["updated"] != expected.updated[flatIndex] {
						t.Fatalf("issue-list row %d/%d drifted: %+v", pageIndex, rowIndex, row)
					}
					issues = append(issues, map[string]any{
						"key": row.Key, "status": row.Values["status"], "updated": row.Values["updated"],
					})
					flatIndex++
				}
				var outputCursor any
				if cursor != "" {
					outputCursor = cursor
				}
				var outputNext any
				if nextCursor != "" {
					outputNext = nextCursor
				}
				pages = append(pages, map[string]any{
					"cursor": outputCursor, "keys": pageKeys, "count": len(pageKeys),
					"complete": terminal, "truncated": !terminal, "next_cursor": outputNext,
				})
			}

			methods, unexpected, duplicates := backend.Summary()
			if !equalHTTPMethods(methods, map[string]int{"GET": expected.expectedRequests}) ||
				unexpected != 0 || duplicates != 0 {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
			final := jiraPaginatedSearchBenchmarkFinal(
				t, expected.query, expected.limit, columns, pages, issues, expected.statusCounts,
			)
			sequence := make([]string, expected.expectedRequests)
			for index := range sequence {
				sequence[index] = "jira.issue.search"
			}
			families := []CapabilityFamilyMetric{{
				Family: "jira.issue.search", Invocations: expected.expectedRequests,
				Successes: expected.expectedRequests, OutputBytes: 1,
			}}
			invocations := jiraPaginatedSearchMCPInvocations(
				t, expected.query, expected.limit, columns, expected.cursors,
			)
			scenario := loadRepositoryScenario(t, filepath.Join(root, "scenario.v1.json"))
			for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, runFile))
				assertJiraPaginatedSearchTransportContract(t, scenario, spec, expected.expectedRequests)
				if declared := repositoryExpectedMCPInvocations(t, spec); !equalMCPInvocations(declared, invocations) {
					t.Fatalf("%s exact invocation contract drifted: declared=%+v fixture=%+v", spec.Provider, declared, invocations)
				}
				assertJiraPaginatedSearchSchemaMatchesFinal(t, root, spec, final)
				results, checkErr := evaluateRunChecksWithMCPInvocations(
					spec.Checks, final, "", len(sequence), 0, unexpected, 0,
					nil, 0, 0, methods, true, nil, families, true, sequence,
					invocations, true,
				)
				if checkErr != nil {
					t.Fatal(checkErr)
				}
				for name, passed := range results {
					if !passed {
						t.Fatalf("%s fixture-derived final failed run check %q", spec.Provider, name)
					}
				}
				assertJiraPaginatedSearchRouteMutationsFail(
					t, spec, final, methods, families, sequence, invocations,
				)
			}
		})
	}
}

func TestRepositoryJiraPaginatedSearchSamplingPairIdentity(t *testing.T) {
	pair := loadRepositorySamplingPairContract(t, "jira-paginated-search-mcp")
	primaryRoot, holdoutRoot := pair.Primary.Root, pair.Holdout.Root

	primarySchema, err := os.ReadFile(filepath.Join(primaryRoot, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	holdoutSchema, err := os.ReadFile(filepath.Join(holdoutRoot, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(primarySchema, holdoutSchema) {
		t.Fatal("primary and holdout response schemas drifted")
	}
	for _, filename := range []string{"fixture.json", "prompt.mcp.v1.md"} {
		primary, readErr := os.ReadFile(filepath.Join(primaryRoot, filename))
		if readErr != nil {
			t.Fatal(readErr)
		}
		holdout, readErr := os.ReadFile(filepath.Join(holdoutRoot, filename))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Equal(primary, holdout) {
			t.Fatalf("holdout does not exercise distinct %s data", filename)
		}
	}

	tests := []struct {
		name, provider, model string
	}{
		{name: "codex", provider: "codex", model: "gpt-5.6-luna"},
		{name: "claude", provider: "claude-code", model: "claude-opus-4-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runFile := "run.mcp.codex.json"
			if test.provider == "claude-code" {
				runFile = "run.mcp.claude.json"
			}
			primary, holdout := pair.Primary.Runs[runFile], pair.Holdout.Runs[runFile]
			if primary.Provider != test.provider || primary.Model != test.model ||
				primary.Reasoning != "high" ||
				holdout.Provider != test.provider || holdout.Model != test.model ||
				holdout.Reasoning != "high" {
				t.Fatalf("exact cohort contract drifted: primary=%+v holdout=%+v", primary, holdout)
			}
			if !slices.Equal(primary.AllowedMCPTools, holdout.AllowedMCPTools) ||
				!slices.Equal(primary.DataCapabilities, holdout.DataCapabilities) {
				t.Fatalf("primary/holdout execution identity drifted: primary=%+v holdout=%+v", primary, holdout)
			}
		})
	}
}

func equalJiraSearchCursor(actual *string, expected string) bool {
	if expected == "" {
		return actual == nil
	}
	return actual != nil && *actual == expected
}

func jiraPaginatedSearchBenchmarkFinal(
	t *testing.T,
	query string,
	limit int,
	columns []string,
	pages, issues []map[string]any,
	statusCounts []map[string]any,
) []byte {
	t.Helper()
	final := map[string]any{
		"query": query, "requested_limit": limit, "projection_columns": columns,
		"pages": pages, "issues": issues, "status_counts": statusCounts,
		"total_count": len(issues), "ids_unique": true, "pages_reconciled": true,
		"evidence_complete": true, "embedded_instruction_treated_as_data": true,
		"brief": "Every terminal Jira search page was traversed and reconciled in backend order.",
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func jiraPaginatedSearchMCPInvocations(
	t *testing.T,
	query string,
	limit int,
	columns, cursors []string,
) []MCPInvocation {
	t.Helper()
	invocations := make([]MCPInvocation, 0, len(cursors))
	for _, cursor := range cursors {
		arguments := map[string]any{
			"jql": query, "columns": columns, "limit": limit, "max_bytes": 65536,
		}
		if cursor != "" {
			arguments["cursor"] = cursor
		}
		invocations = append(invocations, mustMCPInvocation(t, "jira_issue_search", arguments))
	}
	return invocations
}

func assertJiraPaginatedSearchTransportContract(
	t *testing.T,
	scenario Scenario,
	spec RunSpec,
	requests int,
) {
	t.Helper()
	if spec.EffectiveSurface() != SurfaceATLMCP ||
		spec.EffectiveToolTransport() != "mcp" ||
		!slices.Equal(spec.AllowedMCPTools, []string{"jira_issue_search"}) ||
		len(spec.AllowedTools) != 0 ||
		len(spec.AllowedATLCommands) != 0 {
		t.Fatalf("typed route drifted: %+v", spec)
	}
	if scenario.Budgets.MaxInterfaceInvocations != requests ||
		scenario.Budgets.MaxBackendRequests != requests ||
		scenario.Budgets.MaxDuplicateBackendRequests != 0 ||
		scenario.Budgets.MaxRemoteWrites != 0 ||
		!slices.Equal(scenario.Budgets.AllowedHTTPMethods, []string{"GET"}) {
		t.Fatalf("transport budget drifted: %+v", scenario.Budgets)
	}
}

func assertJiraPaginatedSearchSchemaMatchesFinal(t *testing.T, root string, spec RunSpec, final []byte) {
	t.Helper()
	schemaBytes, err := os.ReadFile(filepath.Join(root, spec.ResponseSchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	providerSchema, err := providerResponseSchema(spec, schemaBytes)
	if err != nil {
		t.Fatalf("%s response schema is not provider-compatible: %v", spec.Provider, err)
	}
	for name, schema := range map[string][]byte{"retained": schemaBytes, "provider": providerSchema} {
		if err := validateJSONSchemaSubsetInstance(schema, final); err != nil {
			t.Fatalf("%s %s response schema rejected fixture-derived final: %v", spec.Provider, name, err)
		}
	}
}

func assertJiraPaginatedSearchRouteMutationsFail(
	t *testing.T,
	spec RunSpec,
	final []byte,
	methods map[string]int,
	families []CapabilityFamilyMetric,
	sequence []string,
	invocations []MCPInvocation,
) {
	t.Helper()
	mutatedFamilies := slices.Clone(families)
	mutatedFamilies[0].Invocations++
	results, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", len(sequence), 0, 0, 0,
		nil, 0, 0, methods, true, nil, mutatedFamilies, true, sequence,
		invocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results["route_exact"] {
		t.Fatal("mutated capability family passed route_exact")
	}

	for _, test := range []struct {
		name   string
		mutate func([]MCPInvocation)
	}{
		{name: "cursor", mutate: func(values []MCPInvocation) {
			var arguments map[string]any
			if err := json.Unmarshal(values[1].Arguments, &arguments); err != nil {
				t.Fatal(err)
			}
			arguments["cursor"] = "wrong"
			values[1] = mustMCPInvocation(t, values[1].Tool, arguments)
		}},
		{name: "limit", mutate: func(values []MCPInvocation) {
			var arguments map[string]any
			if err := json.Unmarshal(values[0].Arguments, &arguments); err != nil {
				t.Fatal(err)
			}
			arguments["limit"] = float64(50)
			values[0] = mustMCPInvocation(t, values[0].Tool, arguments)
		}},
		{name: "max-bytes", mutate: func(values []MCPInvocation) {
			var arguments map[string]any
			if err := json.Unmarshal(values[0].Arguments, &arguments); err != nil {
				t.Fatal(err)
			}
			arguments["max_bytes"] = float64(32768)
			values[0] = mustMCPInvocation(t, values[0].Tool, arguments)
		}},
		{name: "query", mutate: func(values []MCPInvocation) {
			var arguments map[string]any
			if err := json.Unmarshal(values[0].Arguments, &arguments); err != nil {
				t.Fatal(err)
			}
			arguments["jql"] = "project = WRONG"
			values[0] = mustMCPInvocation(t, values[0].Tool, arguments)
		}},
		{name: "order", mutate: func(values []MCPInvocation) {
			values[0], values[1] = values[1], values[0]
		}},
	} {
		t.Run("route-arguments-"+test.name, func(t *testing.T) {
			mutated := slices.Clone(invocations)
			test.mutate(mutated)
			results, err := evaluateRunChecksWithMCPInvocations(
				spec.Checks, final, "", len(sequence), 0, 0, 0,
				nil, 0, 0, methods, true, nil, families, true, sequence,
				mutated, true,
			)
			if err != nil {
				t.Fatal(err)
			}
			if results["route_arguments"] {
				t.Fatal("mutated MCP invocation arguments passed route_arguments")
			}
		})
	}
}
