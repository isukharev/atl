package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
			command:     "atl jira issue history QZ-42 --field customfield_20001 --summary-only --",
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
			command:     "atl jira issue history RV-9 --summary-only --",
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
				assertHistorySummaryOnlyCommandPolicy(t, root, spec, test.command, test.commandArgs)
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

func assertHistorySummaryOnlyCommandPolicy(t *testing.T, root string, spec RunSpec, claudeCommand string, codexArgs []string) {
	t.Helper()
	prompt, err := os.ReadFile(filepath.Join(root, spec.PromptFile))
	if err != nil {
		t.Fatal(err)
	}
	expectedPromptCommand := claudeCommand
	if spec.Provider == "codex" {
		expectedPromptCommand = "atl " + strings.Join(codexArgs, " ")
	}
	if !bytes.Contains(prompt, []byte("`"+expectedPromptCommand+"`")) {
		t.Fatalf("%s prompt does not contain its exact reviewed command %q", spec.Provider, expectedPromptCommand)
	}
	lowerPrompt := strings.ToLower(string(prompt))
	for _, required := range []string{
		"exact advertised skill file",
		"routed reference named by",
		"do not search for skills",
		"inspect unrelated",
	} {
		if !strings.Contains(lowerPrompt, required) {
			t.Fatalf("%s prompt omits bounded activation guidance %q", spec.Provider, required)
		}
	}
	if strings.Contains(lowerPrompt, "do not inspect skill or repository files") {
		t.Fatalf("%s prompt still forbids its required skill activation", spec.Provider)
	}
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
		withFalseOverride := append(slices.Clone(codexArgs), "--summary-only=false")
		if _, err := policy.Match(withFalseOverride); err == nil {
			t.Fatal("Codex policy accepted a trailing false summary-only override")
		}
	case "claude-code":
		if !slices.Contains(spec.AllowedATLCommands, claudeCommand) {
			t.Fatalf("Claude policy does not contain exact summary-only command %q: %v", claudeCommand, spec.AllowedATLCommands)
		}
		if !strings.HasSuffix(claudeCommand, " --") {
			t.Fatalf("Claude summary-only prefix lacks an option terminator: %q", claudeCommand)
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
	providerSchema, err := providerResponseSchema(spec, schemaBytes)
	if err != nil {
		t.Fatalf("%s response schema is not provider-compatible: %v", spec.Provider, err)
	}
	if err := validateHistoryBenchmarkSchemaInstance(schemaBytes, final); err != nil {
		t.Fatalf("%s retained response schema rejected fixture-derived final: %v", spec.Provider, err)
	}
	if err := validateHistoryBenchmarkSchemaInstance(providerSchema, final); err != nil {
		t.Fatalf("%s provider response schema rejected fixture-derived final: %v", spec.Provider, err)
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

	var mutated map[string]any
	if err := decodeHistoryBenchmarkJSON(schemaBytes, &mutated); err != nil {
		t.Fatal(err)
	}
	entries := mutated["properties"].(map[string]any)["entries"].(map[string]any)
	items := entries["properties"].(map[string]any)["items"].(map[string]any)
	items["type"] = "string"
	mutatedBytes, err := json.Marshal(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateHistoryBenchmarkSchemaInstance(mutatedBytes, final); err == nil {
		t.Fatal("fixture-derived final passed a response schema with incompatible nested item type")
	}
}

func validateHistoryBenchmarkSchemaInstance(schemaBytes, instanceBytes []byte) error {
	var schema, instance any
	if err := decodeHistoryBenchmarkJSON(schemaBytes, &schema); err != nil {
		return fmt.Errorf("decode schema: %w", err)
	}
	if err := decodeHistoryBenchmarkJSON(instanceBytes, &instance); err != nil {
		return fmt.Errorf("decode instance: %w", err)
	}
	return validateHistoryBenchmarkSchemaNode(schema, instance, "")
}

func decodeHistoryBenchmarkJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func validateHistoryBenchmarkSchemaNode(rawSchema, value any, path string) error {
	schema, ok := rawSchema.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema node is not an object", historyBenchmarkPath(path))
	}
	types, err := historyBenchmarkSchemaTypes(schema["type"])
	if err != nil {
		return fmt.Errorf("%s: %w", historyBenchmarkPath(path), err)
	}
	if len(types) > 0 {
		matched := false
		for _, candidate := range types {
			if historyBenchmarkTypeMatches(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value type %s does not match %v", historyBenchmarkPath(path), historyBenchmarkValueType(value), types)
		}
	}
	if value == nil {
		return nil
	}

	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value is outside enum", historyBenchmarkPath(path))
		}
	}

	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, rawName := range required {
				name, ok := rawName.(string)
				if !ok {
					return fmt.Errorf("%s: required member is not a string", historyBenchmarkPath(path))
				}
				if _, exists := typed[name]; !exists {
					return fmt.Errorf("%s: missing required property %q", historyBenchmarkPath(path), name)
				}
			}
		}
		additional, hasAdditional := schema["additionalProperties"].(bool)
		for name, child := range typed {
			childSchema, exists := properties[name]
			if !exists {
				if hasAdditional && !additional {
					return fmt.Errorf("%s: unexpected property %q", historyBenchmarkPath(path), name)
				}
				continue
			}
			if err := validateHistoryBenchmarkSchemaNode(childSchema, child, path+"/"+name); err != nil {
				return err
			}
		}
	case []any:
		itemSchema, exists := schema["items"]
		if !exists {
			return nil
		}
		for index, child := range typed {
			if err := validateHistoryBenchmarkSchemaNode(itemSchema, child, fmt.Sprintf("%s/%d", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func historyBenchmarkSchemaTypes(raw any) ([]string, error) {
	switch typed := raw.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{typed}, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, value := range typed {
			name, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("schema type union contains a non-string")
			}
			out = append(out, name)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("schema type is neither a string nor an array")
	}
}

func historyBenchmarkTypeMatches(schemaType string, value any) bool {
	switch schemaType {
	case "null":
		return value == nil
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		if _, err := number.Int64(); err == nil {
			return true
		}
		parsed, err := number.Float64()
		return err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed) && math.Trunc(parsed) == parsed
	case "number":
		_, ok := value.(json.Number)
		return ok
	default:
		return false
	}
}

func historyBenchmarkValueType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func historyBenchmarkPath(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
