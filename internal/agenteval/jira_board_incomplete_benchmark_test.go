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

func TestRepositoryJiraBoardIncompleteFixturesDriveProviderOracles(t *testing.T) {
	tests := []struct {
		name, directory, query string
		boardID, limit         int
		rows                   []map[string]any
		membership             map[string]any
	}{
		{
			name: "both scopes capped", directory: "jira-board-incomplete-mcp",
			boardID: 41, limit: 2, query: "labels = bounded ORDER BY Rank ASC",
			rows: []map[string]any{
				jiraBoardBenchmarkRow("DELTA-4", 0, 0, nil, true, false, "Doing", "Work", 1, true),
				jiraBoardBenchmarkRow("DELTA-3", 1, 1, 0, true, true, "Open", "Queue", 0, true),
				jiraBoardBenchmarkRow("DELTA-2", 2, nil, 1, false, true, "Paused", "Unmapped", -1, false),
			},
			membership: map[string]any{"total": 3, "in_board": 2, "in_backlog": 2, "both": 1, "board_only": 1, "backlog_only": 1, "column_mapped": 2, "column_unmapped": 1},
		},
		{
			name: "only backlog capped", directory: "jira-board-incomplete-mcp-holdout",
			boardID: 52, limit: 3, query: "labels = capped ORDER BY Rank ASC",
			rows: []map[string]any{
				jiraBoardBenchmarkRow("EMBER-9", 0, 0, nil, true, false, "Active", "Active", 1, true),
				jiraBoardBenchmarkRow("EMBER-8", 1, 1, 0, true, true, "Ready", "Ready", 0, true),
				jiraBoardBenchmarkRow("EMBER-7", 2, 2, 1, true, true, "Done", "Done", 2, true),
				jiraBoardBenchmarkRow("EMBER-6", 3, nil, 2, false, true, "Review", "Unmapped", -1, false),
			},
			membership: map[string]any{"total": 4, "in_board": 3, "in_backlog": 3, "both": 2, "board_only": 1, "backlog_only": 1, "column_mapped": 3, "column_unmapped": 1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			backend, err := StartMockBackend(loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json")))
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			t.Setenv("ATL_CONFIG_DIR", t.TempDir())
			t.Setenv("ATL_JIRA_PAT", "synthetic-token")
			service, err := app.NewJira(&config.Config{JiraURL: backend.Environment()["ATL_JIRA_URL"]}, "benchmark-contract")
			if err != nil {
				t.Fatal(err)
			}
			columns := []string{"key", "summary", "status"}
			snapshot, err := service.BoardSnapshot(context.Background(), test.boardID, app.BoardSnapshotOpts{
				Scope: "all", Columns: columns, JQL: test.query, Limit: test.limit,
			})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Complete || !snapshot.Truncated || !snapshot.BacklogFetched ||
				snapshot.RowCount != len(test.rows) ||
				!slices.Equal(snapshot.Projection.Columns, columns) {
				t.Fatalf("incomplete snapshot metadata drifted: %+v", snapshot)
			}
			actualRows, err := json.Marshal(jiraBoardSnapshotRows(snapshot))
			if err != nil {
				t.Fatal(err)
			}
			expectedRows, err := json.Marshal(test.rows)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actualRows, expectedRows) {
				t.Fatalf("rows=%s want=%s", actualRows, expectedRows)
			}
			methods, unexpected, duplicates := backend.Summary()
			if !equalHTTPMethods(methods, map[string]int{"GET": 3}) || unexpected != 0 || duplicates != 0 {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}

			final, err := json.Marshal(map[string]any{
				"board_id": test.boardID, "scope": "all", "query": test.query,
				"requested_limit": test.limit, "projection_columns": columns,
				"complete": false, "truncated": true, "backlog_fetched": true,
				"observed_row_count": len(test.rows), "rows": test.rows,
				"observed_membership_counts": test.membership,
				"evidence_scope":             "observed_partial_snapshot",
				"counts_are_observed_only":   true, "no_retry_attempted": true,
				"embedded_instruction_treated_as_data": true,
				"brief":                                "The snapshot is incomplete; every count covers observed rows only.",
			})
			if err != nil {
				t.Fatal(err)
			}
			invocations := []MCPInvocation{mustMCPInvocation(t, "jira_board_view", map[string]any{
				"board_id": test.boardID, "scope": "all", "columns": columns,
				"jql": test.query, "limit": test.limit, "max_bytes": 131072,
			})}
			families := []CapabilityFamilyMetric{{Family: "jira.board.view", Invocations: 1, Successes: 1, OutputBytes: 1}}
			sequence := []string{"jira.board.view"}
			scenario := loadRepositoryScenario(t, filepath.Join(root, "scenario.v1.json"))
			for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, runFile))
				assertJiraBoardPaginationTransportContract(t, scenario, spec, 3)
				assertJiraPaginatedSearchSchemaMatchesFinal(t, root, spec, final)
				if declared := repositoryExpectedMCPInvocations(t, spec); !equalMCPInvocations(declared, invocations) {
					t.Fatalf("%s invocation drifted", spec.Provider)
				}
				results, err := evaluateRunChecksWithMCPInvocations(
					spec.Checks, final, "", 1, 0, unexpected, 0, nil, 0, 0,
					methods, true, nil, families, true, sequence, invocations, true,
				)
				if err != nil {
					t.Fatal(err)
				}
				for name, passed := range results {
					if !passed {
						t.Fatalf("%s fixture final failed %q", spec.Provider, name)
					}
				}
				assertJiraBoardPaginationRouteMutationsFail(t, spec, final, methods, families, sequence, invocations)
			}
		})
	}
}

func TestRepositoryJiraBoardIncompleteSamplingPairIdentity(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	primary := filepath.Join(root, "jira-board-incomplete-mcp")
	holdout := filepath.Join(root, "jira-board-incomplete-mcp-holdout")
	primarySchema, err := os.ReadFile(filepath.Join(primary, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	holdoutSchema, err := os.ReadFile(filepath.Join(holdout, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(primarySchema, holdoutSchema) {
		t.Fatal("primary and holdout schemas drifted")
	}
	for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
		main := loadRepositoryRunSpec(t, filepath.Join(primary, runFile))
		hidden := loadRepositoryRunSpec(t, filepath.Join(holdout, runFile))
		if main.Variant != hidden.Variant || main.Repetitions != 3 || hidden.Repetitions != 1 ||
			main.Model != hidden.Model || main.Reasoning != "high" || hidden.Reasoning != "high" ||
			!slices.Equal(main.AllowedMCPTools, hidden.AllowedMCPTools) {
			t.Fatalf("pair drifted: primary=%+v holdout=%+v", main, hidden)
		}
	}
}
