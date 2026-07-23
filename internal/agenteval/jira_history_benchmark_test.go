package agenteval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
)

func TestRepositoryJiraHistorySummaryFixturesDriveProviderOracles(t *testing.T) {
	ascending := true
	tests := []struct {
		name          string
		directory     string
		key           string
		opts          app.JiraHistoryOpts
		total         int
		fetched       int
		count         int
		complete      bool
		partialReason string
		expectedGETs  int
		summary       app.JiraHistorySummary
		lastChanges   []app.JiraFieldLastChange
		countField    string
		command       string
		commandArgs   []string
	}{
		{
			name: "filtered complete primary", directory: "jira-history-summary", key: "QZ-42",
			opts:  app.JiraHistoryOpts{Fields: []string{"customfield_20001"}},
			total: 4, fetched: 4, count: 3, complete: true, expectedGETs: 1,
			summary: app.JiraHistorySummary{
				HistoryCount: 3, HistoryIDNonemptyCount: 2, HistoryIDMissingCount: 1,
				HistoryIDsUnique: false, HistoryNonemptyIDsUnique: false,
				AuthorNonemptyCount: 2, TimestampNonemptyCount: 3,
				ChronologicalComparable: true, ChronologicalAscending: &ascending,
				EntriesWithItems: 3, MultiItemEntryCount: 1, ItemCount: 4,
				ItemFieldNonemptyCount: 4, DistinctItemFieldCount: 1,
				ItemsWithFromCount: 3, ItemsWithToCount: 4, StatusItemCount: 0,
				CountMatchesHistory: true, FetchedMatchesTotal: true,
				Fields: []app.JiraHistoryFieldSummary{{
					FieldID: "customfield_20001", Field: "Forecast",
					Count: 4, WithFrom: 3, WithTo: 4,
				}},
			},
			lastChanges: []app.JiraFieldLastChange{{
				FieldID: "customfield_20001", Field: "customfield_20001",
				Created: "2026-06-03T09:00:00.000+0000", HistoryID: "801",
				From: "9", To: "10",
			}},
			countField:  "filtered_history_count",
			command:     "atl jira issue history QZ-42 --field customfield_20001 --summary-only",
			commandArgs: []string{"jira", "issue", "history", "QZ-42", "--field", "customfield_20001", "--summary-only"},
		},
		{
			name: "partial non-comparable holdout", directory: "jira-history-summary-holdout", key: "RV-9",
			total: 5, fetched: 3, count: 3, complete: false,
			partialReason: "Jira changelog pagination made no forward progress",
			expectedGETs:  2,
			summary: app.JiraHistorySummary{
				HistoryCount: 3, HistoryIDNonemptyCount: 3, HistoryIDMissingCount: 0,
				HistoryIDsUnique: true, HistoryNonemptyIDsUnique: true,
				AuthorNonemptyCount: 2, TimestampNonemptyCount: 3,
				ChronologicalComparable: false, ChronologicalAscending: nil,
				EntriesWithItems: 3, MultiItemEntryCount: 2, ItemCount: 5,
				ItemFieldNonemptyCount: 5, DistinctItemFieldCount: 4,
				ItemsWithFromCount: 4, ItemsWithToCount: 4, StatusItemCount: 1,
				CountMatchesHistory: true, FetchedMatchesTotal: false,
				Fields: []app.JiraHistoryFieldSummary{
					{FieldID: "customfield_30001", Field: "Risk", Count: 1, WithFrom: 0, WithTo: 1},
					{FieldID: "customfield_30002", Field: "Risk", Count: 2, WithFrom: 2, WithTo: 1},
					{FieldID: "status", Field: "Status", Count: 1, WithFrom: 1, WithTo: 1},
					{FieldID: "", Field: "Risk", Count: 1, WithFrom: 1, WithTo: 1},
				},
			},
			countField:  "history_count",
			command:     "atl jira issue history RV-9 --summary-only",
			commandArgs: []string{"jira", "issue", "history", "RV-9", "--summary-only"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			backend, err := StartMockBackend(fixture)
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
			full, err := service.HistoryFiltered(context.Background(), test.key, test.opts)
			if err != nil {
				t.Fatal(err)
			}
			projection := app.JiraHistorySummaryProjection(full)
			if projection == nil || projection.Key != test.key || projection.Source != "paginated" ||
				projection.Total != test.total || projection.Fetched != test.fetched ||
				projection.Count != test.count || projection.Complete != test.complete ||
				projection.PartialReason != test.partialReason {
				t.Fatalf("projection provenance=%+v", projection)
			}
			if !reflect.DeepEqual(projection.Summary, test.summary) {
				t.Fatalf("summary=%+v want=%+v", projection.Summary, test.summary)
			}
			if !reflect.DeepEqual(projection.LastChanges, test.lastChanges) {
				t.Fatalf("last_changes=%+v want=%+v", projection.LastChanges, test.lastChanges)
			}
			assertHistoryProjectionOmitsRawArray(t, projection)

			final := historyBenchmarkFinal(t, projection, test.countField)
			methods, unexpected, duplicates := backend.Summary()
			if unexpected != 0 || duplicates != 0 || len(methods) != 1 || methods["GET"] != test.expectedGETs {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
			for _, provider := range []string{"codex", "claude"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli."+provider+".json"))
				assertHistorySummaryOnlyCommandPolicy(t, spec, test.command, test.commandArgs)
				assertClosedResponseSchemaMatchesFinal(t, root, spec, final)
				checks, err := evaluateRunChecks(
					spec.Checks, final, "", 2, 0, unexpected, 1,
					map[string]int{"atl:jira": 1}, 0, 0, methods, true, []int{0, 0},
				)
				if err != nil {
					t.Fatal(err)
				}
				for name, passed := range checks {
					if !passed {
						t.Fatalf("%s fixture-derived final failed run check %q", provider, name)
					}
				}
			}
		})
	}
}

func historyBenchmarkFinal(t *testing.T, result *app.JiraHistorySummaryResult, countField string) []byte {
	t.Helper()
	summary := result.Summary
	fields := make([]map[string]any, 0, len(summary.Fields))
	for _, field := range summary.Fields {
		fields = append(fields, map[string]any{
			"field_id":  field.FieldID,
			"field":     field.Field,
			"count":     field.Count,
			"with_from": field.WithFrom,
			"with_to":   field.WithTo,
		})
	}
	final := map[string]any{
		"issue_key": result.Key,
		"complete":  result.Complete,
		"source":    result.Source,
		"total":     result.Total,
		"fetched":   result.Fetched,
		countField:  result.Count,
		"identity": map[string]any{
			"nonempty_ids":        summary.HistoryIDNonemptyCount,
			"missing_ids":         summary.HistoryIDMissingCount,
			"all_ids_unique":      summary.HistoryIDsUnique,
			"nonempty_ids_unique": summary.HistoryNonemptyIDsUnique,
		},
		"ordering": map[string]any{
			"comparable": summary.ChronologicalComparable,
			"ascending":  summary.ChronologicalAscending,
		},
		"entries": map[string]any{
			"with_items":      summary.EntriesWithItems,
			"multi_item":      summary.MultiItemEntryCount,
			"items":           summary.ItemCount,
			"distinct_fields": summary.DistinctItemFieldCount,
			"items_with_from": summary.ItemsWithFromCount,
			"items_with_to":   summary.ItemsWithToCount,
		},
		"fields": fields,
		"reconciliation": map[string]any{
			"count_matches_history": summary.CountMatchesHistory,
			"fetched_matches_total": summary.FetchedMatchesTotal,
		},
	}
	if countField == "filtered_history_count" {
		final["newest_selected_change"] = result.LastChanges[0]
	} else {
		final["partial_reason"] = result.PartialReason
		final["entries"].(map[string]any)["status_items"] = summary.StatusItemCount
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertHistoryProjectionOmitsRawArray(t *testing.T, result *app.JiraHistorySummaryResult) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, exists := envelope["history"]; exists {
		t.Fatalf("summary-only projection contains raw history: %s", encoded)
	}
}

func assertHistorySummaryOnlyCommandPolicy(t *testing.T, spec RunSpec, claudeCommand string, codexArgs []string) {
	t.Helper()
	switch spec.Provider {
	case "codex":
		policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands}
		if _, err := policy.Match(codexArgs); err != nil {
			t.Fatalf("summary-only command rejected by Codex policy: %v", err)
		}
		withoutProjection := slices.Delete(slices.Clone(codexArgs), len(codexArgs)-1, len(codexArgs))
		if _, err := policy.Match(withoutProjection); err == nil {
			t.Fatal("Codex policy accepted history command without --summary-only")
		}
	case "claude-code":
		if !slices.Contains(spec.AllowedATLCommands, claudeCommand) {
			t.Fatalf("Claude policy does not contain exact summary-only command %q: %v", claudeCommand, spec.AllowedATLCommands)
		}
		for _, command := range spec.AllowedATLCommands {
			if strings.HasPrefix(command, "atl jira issue history") && !strings.Contains(command, "--summary-only") {
				t.Fatalf("Claude policy admits history without summary-only: %q", command)
			}
		}
	default:
		t.Fatalf("unexpected provider %q", spec.Provider)
	}
}

func assertClosedResponseSchemaMatchesFinal(t *testing.T, root string, spec RunSpec, final []byte) {
	t.Helper()
	schemaBytes, err := os.ReadFile(filepath.Join(root, spec.ResponseSchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := providerResponseSchema(spec, schemaBytes); err != nil {
		t.Fatalf("%s response schema is not provider-compatible: %v", spec.Provider, err)
	}
	var schema struct {
		Type                 string                     `json:"type"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Fatalf("response schema root is not a closed object")
	}
	var document map[string]any
	if err := json.Unmarshal(final, &document); err != nil {
		t.Fatal(err)
	}
	propertyNames := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		propertyNames = append(propertyNames, name)
	}
	documentNames := make([]string, 0, len(document))
	for name := range document {
		documentNames = append(documentNames, name)
	}
	slices.Sort(propertyNames)
	slices.Sort(documentNames)
	required := slices.Clone(schema.Required)
	slices.Sort(required)
	if !slices.Equal(propertyNames, documentNames) || !slices.Equal(required, propertyNames) {
		t.Fatalf("schema/final root mismatch: properties=%v required=%v final=%v", propertyNames, required, documentNames)
	}
}
