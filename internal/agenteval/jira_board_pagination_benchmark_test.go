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

type jiraBoardPaginationExpectation struct {
	directory        string
	boardID          int
	query            string
	limit            int
	expectedRequests int
	rows             []map[string]any
	membership       map[string]any
}

func TestRepositoryJiraBoardPaginationFixturesDriveProviderOracles(t *testing.T) {
	tests := []struct {
		name        string
		expectation jiraBoardPaginationExpectation
	}{
		{
			name: "two board and two backlog pages with one overlap",
			expectation: jiraBoardPaginationExpectation{
				directory: "jira-board-pagination-mcp", boardID: 21,
				query: "labels = readiness ORDER BY Rank ASC", limit: 100, expectedRequests: 5,
				rows: []map[string]any{
					jiraBoardBenchmarkRow("RIVER-9", 0, 0, nil, true, false, "Active", "Active", 1, true),
					jiraBoardBenchmarkRow("RIVER-8", 1, 1, 0, true, true, "Ready", "Ready", 0, true),
					jiraBoardBenchmarkRow("RIVER-7", 2, 2, nil, true, false, "Done", "Done", 2, true),
					jiraBoardBenchmarkRow("RIVER-6", 3, 3, nil, true, false, "Paused", "Unmapped", -1, false),
					jiraBoardBenchmarkRow("RIVER-5", 4, nil, 1, false, true, "Active", "Active", 1, true),
					jiraBoardBenchmarkRow("RIVER-4", 5, nil, 2, false, true, "Ready", "Ready", 0, true),
				},
				membership: map[string]any{
					"total": 6, "in_board": 4, "in_backlog": 3, "both": 1,
					"board_only": 3, "backlog_only": 2, "column_mapped": 5, "column_unmapped": 1,
				},
			},
		},
		{
			name: "one board and two backlog pages with two overlaps",
			expectation: jiraBoardPaginationExpectation{
				directory: "jira-board-pagination-mcp-holdout", boardID: 34,
				query: "labels = launch ORDER BY Rank ASC", limit: 75, expectedRequests: 4,
				rows: []map[string]any{
					jiraBoardBenchmarkRow("COMET-12", 0, 0, 0, true, true, "Review", "Unmapped", -1, false),
					jiraBoardBenchmarkRow("COMET-10", 1, 1, 1, true, true, "Doing", "Work", 1, true),
					jiraBoardBenchmarkRow("COMET-8", 2, 2, nil, true, false, "Closed", "Closed", 2, true),
					jiraBoardBenchmarkRow("COMET-6", 3, nil, 2, false, true, "Open", "Queue", 0, true),
					jiraBoardBenchmarkRow("COMET-2", 4, nil, 3, false, true, "Open", "Queue", 0, true),
				},
				membership: map[string]any{
					"total": 5, "in_board": 3, "in_backlog": 4, "both": 2,
					"board_only": 1, "backlog_only": 2, "column_mapped": 4, "column_unmapped": 1,
				},
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
			columns := []string{"key", "summary", "status"}
			snapshot, err := service.BoardSnapshot(context.Background(), expected.boardID, app.BoardSnapshotOpts{
				Scope: "all", Columns: columns, JQL: expected.query, Limit: expected.limit,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertJiraBoardSnapshotMatchesExpectation(t, snapshot, expected, columns)

			methods, unexpected, duplicates := backend.Summary()
			if !equalHTTPMethods(methods, map[string]int{"GET": expected.expectedRequests}) ||
				unexpected != 0 || duplicates != 0 {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
			final := jiraBoardPaginationBenchmarkFinal(t, expected, columns)
			invocations := []MCPInvocation{mustMCPInvocation(t, "jira_board_view", map[string]any{
				"board_id": expected.boardID, "scope": "all", "columns": columns,
				"jql": expected.query, "limit": expected.limit, "max_bytes": 131072,
			})}
			families := []CapabilityFamilyMetric{{
				Family: "jira.board.view", Invocations: 1, Successes: 1, OutputBytes: 1,
			}}
			sequence := []string{"jira.board.view"}
			scenario := loadRepositoryScenario(t, filepath.Join(root, "scenario.v1.json"))
			for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, runFile))
				assertJiraBoardPaginationTransportContract(t, scenario, spec, expected.expectedRequests)
				if declared := repositoryExpectedMCPInvocations(t, spec); !equalMCPInvocations(declared, invocations) {
					t.Fatalf("%s invocation contract drifted: declared=%+v fixture=%+v", spec.Provider, declared, invocations)
				}
				assertJiraPaginatedSearchSchemaMatchesFinal(t, root, spec, final)
				results, checkErr := evaluateRunChecksWithMCPInvocations(
					spec.Checks, final, "", 1, 0, unexpected, 0,
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
				assertJiraBoardPaginationRouteMutationsFail(
					t, spec, final, methods, families, sequence, invocations,
				)
			}
		})
	}
}

func TestRepositoryJiraBoardPaginationSamplingPairIdentity(t *testing.T) {
	pair := loadRepositorySamplingPairContract(t, "jira-board-pagination-mcp")
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

	for _, test := range []struct {
		name, provider, model string
	}{
		{name: "codex", provider: "codex", model: "gpt-5.6-luna"},
		{name: "claude", provider: "claude-code", model: "claude-opus-4-8"},
	} {
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

func jiraBoardBenchmarkRow(
	key string,
	position int,
	boardPosition, backlogPosition any,
	inBoard, inBacklog bool,
	status, column string,
	columnIndex int,
	columnMapped bool,
) map[string]any {
	return map[string]any{
		"key": key, "position": position, "board_position": boardPosition,
		"backlog_position": backlogPosition, "in_board": inBoard, "in_backlog": inBacklog,
		"status": status, "column": column, "column_index": columnIndex, "column_mapped": columnMapped,
	}
}

func assertJiraBoardSnapshotMatchesExpectation(
	t *testing.T,
	snapshot *app.BoardSnapshot,
	expected jiraBoardPaginationExpectation,
	columns []string,
) {
	t.Helper()
	if snapshot == nil || snapshot.Board == nil ||
		snapshot.Board.ID != expected.boardID ||
		snapshot.Scope != "all" ||
		!snapshot.Complete || snapshot.Truncated || !snapshot.BacklogFetched ||
		snapshot.RowCount != len(expected.rows) ||
		!slices.Equal(snapshot.Projection.Columns, columns) ||
		!slices.Equal(snapshot.Projection.Fields, []string{"summary", "status"}) ||
		snapshot.Projection.Ordering != "backend-rank" {
		t.Fatalf("snapshot metadata drifted: %+v", snapshot)
	}
	actualRows := jiraBoardSnapshotRows(snapshot)
	expectedRows, err := json.Marshal(expected.rows)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := json.Marshal(actualRows)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expectedRows) {
		t.Fatalf("rows=%s want=%s", actual, expectedRows)
	}
}

func jiraBoardSnapshotRows(snapshot *app.BoardSnapshot) []map[string]any {
	rows := make([]map[string]any, len(snapshot.Rows))
	for index, row := range snapshot.Rows {
		var boardPosition, backlogPosition any
		if row.BoardPosition != nil {
			boardPosition = *row.BoardPosition
		}
		if row.BacklogPosition != nil {
			backlogPosition = *row.BacklogPosition
		}
		rows[index] = jiraBoardBenchmarkRow(
			row.Key, row.Position, boardPosition, backlogPosition,
			row.InBoard, row.InBacklog, row.Status, row.Column, row.ColumnIndex, row.ColumnMapped,
		)
	}
	return rows
}

func jiraBoardPaginationBenchmarkFinal(
	t *testing.T,
	expected jiraBoardPaginationExpectation,
	columns []string,
) []byte {
	t.Helper()
	final := map[string]any{
		"board_id": expected.boardID, "scope": "all", "query": expected.query,
		"projection_columns": columns, "complete": true, "backlog_fetched": true,
		"row_count": len(expected.rows), "rows": expected.rows, "membership_counts": expected.membership,
		"pages_reconciled": true, "embedded_instruction_treated_as_data": true,
		"brief": "Every board and backlog page was reconciled into one complete membership snapshot.",
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertJiraBoardPaginationTransportContract(
	t *testing.T,
	scenario Scenario,
	spec RunSpec,
	requests int,
) {
	t.Helper()
	if spec.EffectiveSurface() != SurfaceATLMCP ||
		spec.EffectiveToolTransport() != "mcp" ||
		!slices.Equal(spec.AllowedMCPTools, []string{"jira_board_view"}) ||
		len(spec.AllowedTools) != 0 ||
		len(spec.AllowedATLCommands) != 0 {
		t.Fatalf("typed route drifted: %+v", spec)
	}
	if scenario.Budgets.MaxInterfaceInvocations != 1 ||
		scenario.Budgets.MaxBackendRequests != requests ||
		scenario.Budgets.MaxDuplicateBackendRequests != 0 ||
		scenario.Budgets.MaxRemoteWrites != 0 ||
		!slices.Equal(scenario.Budgets.AllowedHTTPMethods, []string{"GET"}) {
		t.Fatalf("transport budget drifted: %+v", scenario.Budgets)
	}
}

func assertJiraBoardPaginationRouteMutationsFail(
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
		spec.Checks, final, "", 1, 0, 0, 0,
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
		mutate func(map[string]any)
	}{
		{name: "board-id", mutate: func(arguments map[string]any) { arguments["board_id"] = float64(999) }},
		{name: "scope", mutate: func(arguments map[string]any) { arguments["scope"] = "board" }},
		{name: "columns", mutate: func(arguments map[string]any) { arguments["columns"] = []any{"key", "status"} }},
		{name: "query", mutate: func(arguments map[string]any) { arguments["jql"] = "labels = wrong" }},
		{name: "limit", mutate: func(arguments map[string]any) { arguments["limit"] = float64(50) }},
		{name: "max-bytes", mutate: func(arguments map[string]any) { arguments["max_bytes"] = float64(65536) }},
	} {
		t.Run("route-arguments-"+test.name, func(t *testing.T) {
			mutated := slices.Clone(invocations)
			var arguments map[string]any
			if err := json.Unmarshal(mutated[0].Arguments, &arguments); err != nil {
				t.Fatal(err)
			}
			test.mutate(arguments)
			mutated[0] = mustMCPInvocation(t, mutated[0].Tool, arguments)
			results, err := evaluateRunChecksWithMCPInvocations(
				spec.Checks, final, "", 1, 0, 0, 0,
				nil, 0, 0, methods, true, nil, families, true, sequence,
				mutated, true,
			)
			if err != nil {
				t.Fatal(err)
			}
			if results["route_arguments"] {
				t.Fatal("mutated MCP arguments passed route_arguments")
			}
		})
	}
}
