package agenteval

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func TestRepositoryBenchmarkCorpusContract(t *testing.T) {
	inventory, err := ValidateBenchmarkCorpus(filepath.Join("..", "..", "benchmarks", "agent-eval"))
	if err != nil {
		t.Fatal(err)
	}
	if inventory.SchemaVersion != 1 || inventory.Scenarios < 1 || inventory.Runs < inventory.Scenarios || len(inventory.Classes) < 1 {
		t.Fatalf("inventory=%+v", inventory)
	}
}

func TestRepositoryStructureAndTableV2ChecksRejectSemanticDrift(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	tests := []struct {
		name      string
		directory string
		check     string
		correct   string
		drifted   string
	}{
		{
			name:      "Structure subtree counts",
			directory: "jira-structure-subtree-export",
			check:     "counts_correct",
			correct:   `{"counts":{"selected_rows_including_root":5,"issue_rows_including_repeats":4,"unique_issue_ids":3,"repeated_issue_occurrences":1,"export_selectors_including_repeats":4,"exported_unique_issue_ids":2,"omitted_unique_issue_ids":1}}`,
			drifted:   `{"counts":{"selected_rows_including_root":4,"issue_rows_including_repeats":4,"unique_issue_ids":3,"repeated_issue_occurrences":1,"export_selectors_including_repeats":4,"exported_unique_issue_ids":2,"omitted_unique_issue_ids":1}}`,
		},
		{
			name:      "Structure value accessibility counts",
			directory: "jira-structure-deep-values",
			check:     "counts_correct",
			correct:   `{"counts":{"selected_rows_including_root":9,"issue_rows_including_repeats":5,"unique_issue_ids":4,"repeated_issue_occurrences":1,"queried_value_rows":9,"accessible_issue_rows":4,"inaccessible_issue_rows":1}}`,
			drifted:   `{"counts":{"selected_rows_including_root":9,"issue_rows_including_repeats":5,"unique_issue_ids":5,"repeated_issue_occurrences":0,"queried_value_rows":9,"accessible_issue_rows":4,"inaccessible_issue_rows":1}}`,
		},
		{
			name:      "Confluence expanded grid semantics",
			directory: "confluence-table-summary",
			check:     "count_semantics_correct",
			correct:   `{"count_semantics":{"table_count_scope":"page-wide","row_count_scope":"expanded-rows-including-headers","cell_count_scope":"expanded-rectangular-grid","repeated_cell_scope":"span-covered-coordinates","span_source_scope":"non-repeated-source-cells","combined_span_coverage":"counted-on-each-covered-axis"}}`,
			drifted:   `{"count_semantics":{"table_count_scope":"selected-only","row_count_scope":"expanded-rows-including-headers","cell_count_scope":"expanded-rectangular-grid","repeated_cell_scope":"span-covered-coordinates","span_source_scope":"non-repeated-source-cells","combined_span_coverage":"counted-on-each-covered-axis"}}`,
		},
		{
			name:      "Confluence qualifying identifiers",
			directory: "confluence-table-analytics",
			check:     "qualifying_ids_correct",
			correct:   `{"qualifying_item_codes":["ALPHA","ECHO","KILO","ROMEO","XRAY"]}`,
			drifted:   `{"qualifying_item_codes":["ALPHA","ECHO","KILO","ROMEO"]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.directory, "run.cli.claude.json")
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			spec, decodeErr := DecodeRunSpec(file)
			closeErr := file.Close()
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}

			for label, final := range map[string]string{"correct": test.correct, "drifted": test.drifted} {
				checks, err := evaluateRunChecks(spec.Checks, []byte(final), "", 0, 0, 0, 0, nil, 0, 0, nil, false, nil)
				if err != nil {
					t.Fatalf("%s: %v", label, err)
				}
				if got := checks[test.check]; got != (label == "correct") {
					t.Fatalf("%s check %q=%v", label, test.check, got)
				}
			}
		})
	}
}

func TestRepositoryStructureAndTableV2ProviderParityKeepsTransportBudgets(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for directory, wantRemoteWrites := range map[string]int{
		"jira-structure-deep-values":    2,
		"jira-structure-subtree-export": 0,
		"confluence-table-analytics":    0,
		"confluence-table-summary":      0,
	} {
		t.Run(directory, func(t *testing.T) {
			scenarioFile, err := os.Open(filepath.Join(root, directory, "scenario.v2.json"))
			if err != nil {
				t.Fatal(err)
			}
			scenario, decodeErr := DecodeScenario(scenarioFile)
			closeErr := scenarioFile.Close()
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if scenario.Budgets.MaxRemoteWrites != wantRemoteWrites {
				t.Fatalf("v2 scenario remote-write budget=%d want=%d", scenario.Budgets.MaxRemoteWrites, wantRemoteWrites)
			}

			specs := make(map[string]RunSpec, 2)
			for _, provider := range []string{"claude", "codex"} {
				path := filepath.Join(root, directory, "run.cli."+provider+".json")
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				spec, decodeErr := DecodeRunSpec(file)
				closeErr := file.Close()
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
				if spec.ScenarioFile != "scenario.v2.json" || spec.ResponseSchemaFile != "response-schema.v2.json" || spec.QualitativeRubricFile != "rubric.v2.json" {
					t.Fatalf("%s spec escaped the v2 contract: %+v", provider, spec)
				}
				specs[provider] = spec
			}

			claude, codex := specs["claude"], specs["codex"]
			if claude.Provider != "claude-code" || claude.Model != "claude-opus-4-8" ||
				codex.Provider != "codex" || codex.Model != "gpt-5.6-luna" {
				t.Fatalf("provider/model parity drifted: claude=%s/%s codex=%s/%s", claude.Provider, claude.Model, codex.Provider, codex.Model)
			}
			if claude.PromptFile != codex.PromptFile || claude.FixtureFile != codex.FixtureFile ||
				claude.ResponseSchemaFile != codex.ResponseSchemaFile || claude.QualitativeRubricFile != codex.QualitativeRubricFile ||
				claude.WorkspaceTemplate != codex.WorkspaceTemplate || claude.Category != codex.Category || claude.Surface != codex.Surface ||
				claude.Reasoning != codex.Reasoning || claude.Repetitions != codex.Repetitions || claude.TimeoutSeconds != codex.TimeoutSeconds ||
				claude.MaxEstimatedCostMicroUSD != codex.MaxEstimatedCostMicroUSD {
				t.Fatalf("shared provider contract drifted: claude=%+v codex=%+v", claude, codex)
			}
			claudeSemantic, err := semanticRunChecks(claude.Checks)
			if err != nil {
				t.Fatal(err)
			}
			codexSemantic, err := semanticRunChecks(codex.Checks)
			if err != nil {
				t.Fatal(err)
			}
			if !equalPrivateComparisonJSON(claudeSemantic, codexSemantic) {
				t.Fatalf("semantic checks drifted: claude=%+v codex=%+v", claudeSemantic, codexSemantic)
			}
		})
	}
}

func TestRepositoryStructureDeepValuesV2PassesConservativeQueryPOSTBudget(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-structure-deep-values")
	scenario := loadRepositoryScenario(t, filepath.Join(root, "scenario.v2.json"))
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli.codex.json"))
	fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
	httpMethods := make(map[string]int)
	requestIdentities := make(map[string]int)
	duplicateRequests := 0
	postIndex := 0
	for _, route := range fixture.Routes {
		httpMethods[route.Method]++
		identity, err := json.Marshal(struct {
			Method        string            `json:"method"`
			Path          string            `json:"path"`
			QueryContains map[string]string `json:"query_contains,omitempty"`
			QueryEquals   map[string]string `json:"query_equals,omitempty"`
		}{route.Method, route.Path, route.QueryContains, route.QueryEquals})
		if err != nil {
			t.Fatal(err)
		}
		requestIdentities[string(identity)]++
		if requestIdentities[string(identity)] > 1 {
			duplicateRequests++
		}
		if route.Method != "POST" {
			continue
		}
		if route.Path != "/jira/rest/structure/2.0/value" || len(route.QueryContains) != 0 || len(route.QueryEquals) != 0 || len(route.RequestBody) == 0 {
			t.Fatalf("query-only POST route is not exactly selector-bound: %+v", route)
		}
		var query struct {
			Requests []struct {
				ForestSpec struct {
					StructureID int64 `json:"structureId"`
				} `json:"forestSpec"`
				Rows       []int64 `json:"rows"`
				Attributes []struct {
					ID     string `json:"id"`
					Format string `json:"format"`
				} `json:"attributes"`
			} `json:"requests"`
		}
		if err := json.Unmarshal(route.RequestBody, &query); err != nil || len(query.Requests) != 1 || query.Requests[0].ForestSpec.StructureID != 88 {
			t.Fatalf("query-only POST body is not a single Structure value request: body=%s err=%v", route.RequestBody, err)
		}
		attributeIDs := make([]string, len(query.Requests[0].Attributes))
		for index, attribute := range query.Requests[0].Attributes {
			if attribute.Format != "text" {
				t.Fatalf("query-only POST attribute %q format=%q", attribute.ID, attribute.Format)
			}
			attributeIDs[index] = attribute.ID
		}
		var wantRows []int64
		var wantAttributes []string
		switch postIndex {
		case 0:
			wantRows = []int64{400, 410, 411, 417, 500}
			wantAttributes = []string{"key", "summary"}
		case 1:
			wantRows = []int64{410, 411, 412, 413, 414, 415, 416, 417, 418}
			wantAttributes = []string{"key", "summary", "status", "customfield_12345"}
		default:
			t.Fatalf("unexpected third query-only POST")
		}
		if !slices.Equal(query.Requests[0].Rows, wantRows) || !slices.Equal(attributeIDs, wantAttributes) {
			t.Fatalf("query-only POST %d shape drifted: rows=%v attributes=%v", postIndex+1, query.Requests[0].Rows, attributeIDs)
		}
		postIndex++
	}
	if httpMethods["GET"] != 1 || httpMethods["POST"] != 2 || len(httpMethods) != 2 || postIndex != 2 || duplicateRequests != 1 {
		t.Fatalf("fixture requests: methods=%v posts=%d duplicates=%d", httpMethods, postIndex, duplicateRequests)
	}
	derivedChecks, err := evaluateRunChecks(spec.Checks, []byte(`{"content_mutations":0}`), "", 3, 0, 0, 1,
		map[string]int{"atl:jira": 1}, 0, 0, httpMethods, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !derivedChecks["http_exact"] || !derivedChecks["content_not_mutated"] {
		t.Fatalf("fixture-derived run checks: http_exact=%v content_not_mutated=%v", derivedChecks["http_exact"], derivedChecks["content_not_mutated"])
	}

	checks := make(map[string]bool, len(spec.Checks))
	for _, check := range spec.Checks {
		checks[check.Name] = true
	}
	checks["http_exact"] = derivedChecks["http_exact"]
	checks["content_not_mutated"] = derivedChecks["content_not_mutated"]
	coverage := make(map[string]bool, len(scenario.RequiredMetrics)+1)
	for _, metric := range scenario.RequiredMetrics {
		coverage[metric] = true
	}
	coverage["remote_writes"] = true
	result, err := Evaluate(scenario, Observation{
		SchemaVersion: ObservationSchemaVersion, ScenarioID: scenario.ID,
		Variant: spec.Variant, Surface: spec.Surface,
		BackendObservation: BackendObservationHTTP, SafetyAssurance: SafetyAssuranceObservedHTTP,
		Runtime: Runtime{Provider: "deterministic", ATLVersion: "test"},
		Metrics: InputMetrics{
			AgentTurns: 1, ToolCalls: 3, ATLInvocations: 3, DuplicateBackendRequests: duplicateRequests,
			OutputBytes: 1, InputTokens: 1, OutputTokens: 1,
			MainThreadInputTokens: 1, MainThreadOutputTokens: 1,
			EstimatedCostMicroUSD: 1, DurationMillis: 1,
		},
		Coverage: coverage, HTTPMethods: httpMethods, Checks: checks,
		CapabilityFamilies: []CapabilityFamilyMetric{{
			Family: "jira.structure.values", Invocations: 2, Successes: 2, OutputBytes: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pass" || result.Metrics.BackendRequests != 3 || result.Metrics.RemoteWrites != 2 ||
		result.Metrics.DuplicateBackendRequests != 1 || len(result.Violations) != 0 {
		t.Fatalf("fixture-derived scenario did not pass conservative transport budget: %+v", result)
	}
}

func TestRepositoryStructureDeepValuesV2PromptPermitsProviderNativeSkillActivation(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-structure-deep-values")
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli.codex.json"))
	hasSkillGate := false
	for _, check := range spec.Checks {
		if check.Kind == "skill_invocations_min" {
			hasSkillGate = true
			break
		}
	}
	if !hasSkillGate {
		t.Fatal("Codex CLI benchmark lost its skill-activation gate")
	}
	prompt, err := os.ReadFile(filepath.Join(root, spec.PromptFile))
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(prompt))
	for _, forbidden := range []string{"do not inspect skill", "do not read skill", "must not count as remote writes"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("Codex CLI prompt conflicts with its measured contract: %q", forbidden)
		}
	}
	for _, required := range []string{"provider-native mechanism", "exact advertised skill file", "routed reference", "two transport-level `remote_writes`", "zero content mutation"} {
		if !strings.Contains(lower, required) {
			t.Fatalf("Codex CLI prompt omits reviewed activation/transport guidance: %q", required)
		}
	}
}

func TestRepositoryTableMCPV3ProviderParityIsOneRead(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for _, directory := range []string{
		"confluence-table-analytics-mcp",
		"confluence-table-analytics-mcp-holdout",
		"confluence-table-summary-mcp",
		"confluence-table-summary-mcp-holdout",
	} {
		t.Run(directory, func(t *testing.T) {
			scenarioFile, err := os.Open(filepath.Join(root, directory, "scenario.v3.json"))
			if err != nil {
				t.Fatal(err)
			}
			scenario, decodeErr := DecodeScenario(scenarioFile)
			closeErr := scenarioFile.Close()
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if scenario.Budgets.MaxRemoteWrites != 0 || scenario.Budgets.MaxToolCalls != 2 || scenario.Budgets.MaxBackendRequests != 1 ||
				scenario.Budgets.MaxInterfaceInvocations != 1 || len(scenario.Budgets.AllowedHTTPMethods) != 1 ||
				scenario.Budgets.AllowedHTTPMethods[0] != "GET" {
				t.Fatalf("v3 scenario escaped one-read policy: %+v", scenario.Budgets)
			}

			specs := make(map[string]RunSpec, 2)
			for _, provider := range []string{"claude", "codex"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, directory, "run.mcp."+provider+".json"))
				if spec.ScenarioFile != "scenario.v3.json" || spec.PromptFile != "prompt.mcp.v3.md" ||
					spec.ResponseSchemaFile != "response-schema.v3.json" || spec.QualitativeRubricFile != "rubric.v3.json" ||
					spec.EffectiveToolTransport() != "mcp" || len(spec.AllowedMCPTools) != 1 ||
					len(spec.AllowedTools) != 0 || len(spec.AllowedATLCommands) != 0 {
					t.Fatalf("%s spec escaped the v3 MCP contract: %+v", provider, spec)
				}
				specs[provider] = spec
			}

			claude, codex := specs["claude"], specs["codex"]
			if claude.Provider != "claude-code" || claude.Model != "claude-opus-4-8" ||
				codex.Provider != "codex" || codex.Model != "gpt-5.6-luna" {
				t.Fatalf("provider/model parity drifted: claude=%s/%s codex=%s/%s", claude.Provider, claude.Model, codex.Provider, codex.Model)
			}
			if claude.PromptFile != codex.PromptFile || claude.FixtureFile != codex.FixtureFile ||
				claude.ResponseSchemaFile != codex.ResponseSchemaFile || claude.QualitativeRubricFile != codex.QualitativeRubricFile ||
				claude.WorkspaceTemplate != codex.WorkspaceTemplate || claude.Category != codex.Category || claude.Surface != codex.Surface ||
				claude.Variant != codex.Variant || claude.Reasoning != codex.Reasoning || claude.Repetitions != codex.Repetitions ||
				claude.TimeoutSeconds != codex.TimeoutSeconds || claude.MaxEstimatedCostMicroUSD != codex.MaxEstimatedCostMicroUSD ||
				!slices.Equal(claude.AllowedMCPTools, codex.AllowedMCPTools) {
				t.Fatalf("shared provider contract drifted: claude=%+v codex=%+v", claude, codex)
			}
			if !equalPrivateComparisonJSON(claude.Checks, codex.Checks) {
				t.Fatalf("run checks drifted: claude=%+v codex=%+v", claude.Checks, codex.Checks)
			}
		})
	}
}

func TestRepositoryTableMCPV3HoldoutsAreDistinct(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for _, primaryDirectory := range []string{"confluence-table-analytics-mcp", "confluence-table-summary-mcp"} {
		holdoutDirectory := primaryDirectory + "-holdout"
		t.Run(primaryDirectory, func(t *testing.T) {
			primaryScenario := loadRepositoryScenario(t, filepath.Join(root, primaryDirectory, "scenario.v3.json"))
			holdoutScenario := loadRepositoryScenario(t, filepath.Join(root, holdoutDirectory, "scenario.v3.json"))
			if primaryScenario.ID == holdoutScenario.ID || primaryScenario.TaskClass != holdoutScenario.TaskClass ||
				primaryScenario.Category != holdoutScenario.Category || primaryScenario.DataClass != holdoutScenario.DataClass ||
				!slices.Equal(primaryScenario.RequiredCapabilities, holdoutScenario.RequiredCapabilities) {
				t.Fatalf("primary/holdout scenario relationship drifted: primary=%+v holdout=%+v", primaryScenario, holdoutScenario)
			}

			for _, name := range []string{"fixture.json", "prompt.mcp.v3.md"} {
				primary, err := os.ReadFile(filepath.Join(root, primaryDirectory, name))
				if err != nil {
					t.Fatal(err)
				}
				holdout, err := os.ReadFile(filepath.Join(root, holdoutDirectory, name))
				if err != nil {
					t.Fatal(err)
				}
				if string(primary) == string(holdout) {
					t.Fatalf("%s reused primary bytes", name)
				}
			}

			primary := loadRepositoryRunSpec(t, filepath.Join(root, primaryDirectory, "run.mcp.codex.json"))
			holdout := loadRepositoryRunSpec(t, filepath.Join(root, holdoutDirectory, "run.mcp.codex.json"))
			if primary.Repetitions != 3 || holdout.Repetitions != 1 || primary.Provider != holdout.Provider ||
				primary.Model != holdout.Model || primary.Reasoning != holdout.Reasoning || primary.Variant != holdout.Variant ||
				primary.Surface != holdout.Surface || primary.EffectiveToolTransport() != holdout.EffectiveToolTransport() {
				t.Fatalf("primary/holdout sampling contract drifted: primary=%+v holdout=%+v", primary, holdout)
			}
		})
	}
}

func TestRepositoryStructureMCPV1ProviderParityIsOneBoundedRead(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for _, directory := range []string{"jira-structure-view-mcp", "jira-structure-view-mcp-holdout"} {
		t.Run(directory, func(t *testing.T) {
			scenario := loadRepositoryScenario(t, filepath.Join(root, directory, "scenario.v1.json"))
			if scenario.Budgets.MaxRemoteWrites != 1 || scenario.Budgets.MaxToolCalls != 2 ||
				scenario.Budgets.MaxInterfaceInvocations != 1 || scenario.Budgets.MaxBackendRequests != 4 ||
				scenario.Budgets.MaxDuplicateBackendRequests != 0 ||
				!slices.Equal(scenario.Budgets.AllowedHTTPMethods, []string{"GET", "POST"}) {
				t.Fatalf("Structure MCP scenario escaped bounded read policy: %+v", scenario.Budgets)
			}

			specs := make(map[string]RunSpec, 2)
			for _, provider := range []string{"claude", "codex"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, directory, "run.mcp."+provider+".json"))
				if spec.ScenarioFile != "scenario.v1.json" || spec.PromptFile != "prompt.mcp.v1.md" ||
					spec.ResponseSchemaFile != "response-schema.v1.json" || spec.QualitativeRubricFile != "rubric.v1.json" ||
					spec.EffectiveToolTransport() != "mcp" || !slices.Equal(spec.AllowedMCPTools, []string{"jira_structure_view"}) ||
					len(spec.AllowedTools) != 0 || len(spec.AllowedATLCommands) != 0 {
					t.Fatalf("%s spec escaped the Structure MCP contract: %+v", provider, spec)
				}
				specs[provider] = spec
			}

			claude, codex := specs["claude"], specs["codex"]
			if claude.Provider != "claude-code" || claude.Model != "claude-opus-4-8" ||
				codex.Provider != "codex" || codex.Model != "gpt-5.6-luna" {
				t.Fatalf("provider/model parity drifted: claude=%s/%s codex=%s/%s", claude.Provider, claude.Model, codex.Provider, codex.Model)
			}
			if claude.PromptFile != codex.PromptFile || claude.FixtureFile != codex.FixtureFile ||
				claude.ResponseSchemaFile != codex.ResponseSchemaFile || claude.QualitativeRubricFile != codex.QualitativeRubricFile ||
				claude.WorkspaceTemplate != codex.WorkspaceTemplate || claude.Category != codex.Category || claude.Surface != codex.Surface ||
				claude.Variant != codex.Variant || claude.Reasoning != codex.Reasoning || claude.Repetitions != codex.Repetitions ||
				claude.TimeoutSeconds != codex.TimeoutSeconds || claude.MaxEstimatedCostMicroUSD != codex.MaxEstimatedCostMicroUSD ||
				!equalPrivateComparisonJSON(claude.Checks, codex.Checks) {
				t.Fatalf("provider contract drifted: claude=%+v codex=%+v", claude, codex)
			}
		})
	}
}

func TestRepositoryStructureMCPV1HoldoutIsDistinct(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	primaryDirectory := filepath.Join(root, "jira-structure-view-mcp")
	holdoutDirectory := filepath.Join(root, "jira-structure-view-mcp-holdout")
	primaryScenario := loadRepositoryScenario(t, filepath.Join(primaryDirectory, "scenario.v1.json"))
	holdoutScenario := loadRepositoryScenario(t, filepath.Join(holdoutDirectory, "scenario.v1.json"))
	if primaryScenario.ID == holdoutScenario.ID || primaryScenario.TaskClass != holdoutScenario.TaskClass ||
		primaryScenario.Category != holdoutScenario.Category || primaryScenario.DataClass != holdoutScenario.DataClass ||
		!slices.Equal(primaryScenario.RequiredCapabilities, holdoutScenario.RequiredCapabilities) {
		t.Fatalf("primary/holdout relationship drifted: primary=%+v holdout=%+v", primaryScenario, holdoutScenario)
	}
	for _, name := range []string{"fixture.json", "prompt.mcp.v1.md"} {
		primary, err := os.ReadFile(filepath.Join(primaryDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
		holdout, err := os.ReadFile(filepath.Join(holdoutDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(primary) == string(holdout) {
			t.Fatalf("%s reused primary bytes", name)
		}
	}
	primary := loadRepositoryRunSpec(t, filepath.Join(primaryDirectory, "run.mcp.codex.json"))
	holdout := loadRepositoryRunSpec(t, filepath.Join(holdoutDirectory, "run.mcp.codex.json"))
	if primary.Repetitions != 3 || holdout.Repetitions != 1 || primary.Provider != holdout.Provider ||
		primary.Model != holdout.Model || primary.Reasoning != holdout.Reasoning || primary.Variant != holdout.Variant ||
		primary.Surface != holdout.Surface || primary.EffectiveToolTransport() != holdout.EffectiveToolTransport() {
		t.Fatalf("primary/holdout sampling contract drifted: primary=%+v holdout=%+v", primary, holdout)
	}
}

func TestRepositoryStructureMCPV1FixturesMatchOracles(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for _, test := range []struct {
		directory   string
		structureID int64
		rootRow     int64
		path        []string
	}{
		{directory: "jira-structure-view-mcp", structureID: 91, rootRow: 110, path: []string{"Portfolio", "Quarter 3"}},
		{directory: "jira-structure-view-mcp-holdout", structureID: 92, rootRow: 310, path: []string{"Roadmap", "Quarter 4"}},
	} {
		t.Run(test.directory, func(t *testing.T) {
			directory := filepath.Join(root, test.directory)
			final := repositoryStructureMCPFinal(t, directory, test.structureID, test.rootRow, test.path)
			scenario := loadRepositoryScenario(t, filepath.Join(directory, "scenario.v1.json"))
			spec := loadRepositoryRunSpec(t, filepath.Join(directory, "run.mcp.codex.json"))
			checks, err := evaluateRunChecks(spec.Checks, final, "", 1, 0, 0, 0, nil, 0, 0, map[string]int{"GET": 3, "POST": 1}, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			for name, passed := range checks {
				if !passed {
					t.Fatalf("fixture-derived Structure result failed run check %q: %s", name, final)
				}
			}
			coverage := make(map[string]bool, len(scenario.RequiredMetrics)+1)
			for _, metric := range scenario.RequiredMetrics {
				coverage[metric] = true
			}
			coverage["remote_writes"] = true
			result, err := Evaluate(scenario, Observation{
				SchemaVersion: ObservationSchemaVersion, ScenarioID: scenario.ID,
				Variant: spec.Variant, Surface: spec.Surface,
				BackendObservation: BackendObservationHTTP, SafetyAssurance: SafetyAssuranceObservedHTTP,
				Runtime: Runtime{Provider: "deterministic", ATLVersion: "test"},
				Metrics: InputMetrics{
					AgentTurns: 1, ToolCalls: 1, InterfaceInvocations: 1,
					OutputBytes: int64(len(final)), InputTokens: 1, OutputTokens: 1,
					MainThreadInputTokens: 1, MainThreadOutputTokens: 1,
					EstimatedCostMicroUSD: 1, DurationMillis: 1,
				},
				Coverage: coverage, HTTPMethods: map[string]int{"GET": 3, "POST": 1}, Checks: checks,
				CapabilityFamilies: []CapabilityFamilyMetric{{
					Family: "jira.structure.view", Invocations: 1, Successes: 1, OutputBytes: int64(len(final)),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "pass" || result.Metrics.RemoteWrites != 1 || len(result.Violations) != 0 {
				t.Fatalf("fixture-derived scenario did not pass conservative transport budget: %+v", result)
			}
		})
	}
}

func repositoryStructureMCPFinal(t *testing.T, directory string, structureID, rootRow int64, expectedPath []string) []byte {
	t.Helper()
	fixture := loadRepositoryMockFixture(t, filepath.Join(directory, "fixture.json"))
	if len(fixture.Routes) != 4 {
		t.Fatalf("routes=%d want=4", len(fixture.Routes))
	}

	var metadata struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	var forestResponse struct {
		Formula   string            `json:"formula"`
		ItemTypes map[string]string `json:"itemTypes"`
	}
	labels := map[int64]string{}
	accessibleIssues := map[string]bool{}
	var valueRequest json.RawMessage
	var searchQuery map[string]string
	seenMetadata, seenForest, seenValues, seenSearch := false, false, false, false
	for _, route := range fixture.Routes {
		switch {
		case route.Method == "GET" && strings.HasSuffix(route.Path, "/structure/"+strconv.FormatInt(structureID, 10)):
			if err := json.Unmarshal(route.Body, &metadata); err != nil {
				t.Fatal(err)
			}
			seenMetadata = true
		case route.Method == "GET" && strings.HasSuffix(route.Path, "/forest/latest"):
			if err := json.Unmarshal(route.Body, &forestResponse); err != nil {
				t.Fatal(err)
			}
			seenForest = true
		case route.Method == "POST" && strings.HasSuffix(route.Path, "/value"):
			valueRequest = append(json.RawMessage(nil), route.RequestBody...)
			var values struct {
				Responses []struct {
					Rows []int64 `json:"rows"`
					Data []struct {
						Attribute struct {
							ID string `json:"id"`
						} `json:"attribute"`
						Values []any `json:"values"`
					} `json:"data"`
				} `json:"responses"`
			}
			if err := json.Unmarshal(route.Body, &values); err != nil {
				t.Fatal(err)
			}
			for _, response := range values.Responses {
				for _, block := range response.Data {
					if block.Attribute.ID != "summary" {
						continue
					}
					if len(block.Values) != len(response.Rows) {
						t.Fatalf("summary values=%d rows=%d", len(block.Values), len(response.Rows))
					}
					for index, value := range block.Values {
						label, ok := value.(string)
						if ok && strings.TrimSpace(label) != "" {
							labels[response.Rows[index]] = label
						}
					}
				}
			}
			seenValues = true
		case route.Method == "GET" && strings.HasSuffix(route.Path, "/rest/api/2/search"):
			searchQuery = route.QueryEquals
			var search struct {
				Issues []struct {
					ID string `json:"id"`
				} `json:"issues"`
			}
			if err := json.Unmarshal(route.Body, &search); err != nil {
				t.Fatal(err)
			}
			for _, issue := range search.Issues {
				accessibleIssues[issue.ID] = true
			}
			seenSearch = true
		}
	}
	if !seenMetadata || !seenForest || !seenValues || !seenSearch || metadata.ID != structureID || metadata.Name == "" {
		t.Fatalf("incomplete fixture metadata=%t forest=%t values=%t search=%t structure=%+v", seenMetadata, seenForest, seenValues, seenSearch, metadata)
	}

	rows, err := app.ParseStructureRows(&domain.StructureForest{Formula: forestResponse.Formula, ItemTypes: forestResponse.ItemTypes})
	if err != nil {
		t.Fatal(err)
	}
	byRowID := make(map[int64]domain.StructureRow, len(rows))
	folderRows := []int64{}
	rootIndex := -1
	for index, row := range rows {
		byRowID[row.RowID] = row
		if row.ItemType == "folder" && labels[row.RowID] == "" {
			t.Fatalf("folder row %d has no summary projection", row.RowID)
		}
		if row.ItemType == "folder" {
			folderRows = append(folderRows, row.RowID)
		}
		if row.RowID == rootRow {
			rootIndex = index
		}
	}
	if rootIndex < 0 || rows[rootIndex].ItemType != "folder" {
		t.Fatalf("folder root row %d was not found", rootRow)
	}
	var query struct {
		Requests []struct {
			ForestSpec struct {
				StructureID int64 `json:"structureId"`
			} `json:"forestSpec"`
			Rows       []int64 `json:"rows"`
			Attributes []struct {
				ID     string `json:"id"`
				Format string `json:"format"`
			} `json:"attributes"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(valueRequest, &query); err != nil {
		t.Fatal(err)
	}
	if len(query.Requests) != 1 || query.Requests[0].ForestSpec.StructureID != structureID ||
		!slices.Equal(query.Requests[0].Rows, folderRows) || len(query.Requests[0].Attributes) != 2 ||
		query.Requests[0].Attributes[0].ID != "key" || query.Requests[0].Attributes[0].Format != "text" ||
		query.Requests[0].Attributes[1].ID != "summary" || query.Requests[0].Attributes[1].Format != "text" {
		t.Fatalf("value query escaped exact folder-label projection: %+v", query)
	}
	root := rows[rootIndex]
	selected := rows[rootIndex : rootIndex+1]
	for index := rootIndex + 1; index < len(rows) && rows[index].Depth > root.Depth; index++ {
		selected = append(selected, rows[index])
	}

	path := []string{}
	for row := root; ; {
		if row.ItemType == "folder" {
			path = append(path, labels[row.RowID])
		}
		if row.ParentRowID == 0 {
			break
		}
		parent, ok := byRowID[row.ParentRowID]
		if !ok {
			t.Fatalf("row %d refers to missing parent %d", row.RowID, row.ParentRowID)
		}
		row = parent
	}
	slices.Reverse(path)
	if !slices.Equal(path, expectedPath) {
		t.Fatalf("selection path=%v want=%v", path, expectedPath)
	}

	orderedRows := make([]map[string]any, 0, len(selected))
	inaccessibleRows := []int64{}
	seenIssueIDs := map[string]bool{}
	issueIDs := []string{}
	accessibleIssueRows, inaccessibleIssueRows, repeatedIssueOccurrences, nonIssueRows := 0, 0, 0, 0
	for _, row := range selected {
		accessible := true
		if row.ItemType == "issue" {
			accessible = accessibleIssues[row.ItemID]
			if accessible {
				accessibleIssueRows++
			} else {
				inaccessibleIssueRows++
				inaccessibleRows = append(inaccessibleRows, row.RowID)
			}
			if seenIssueIDs[row.ItemID] {
				repeatedIssueOccurrences++
			} else {
				issueIDs = append(issueIDs, row.ItemID)
			}
			seenIssueIDs[row.ItemID] = true
		} else {
			nonIssueRows++
		}
		orderedRows = append(orderedRows, map[string]any{
			"row_id": row.RowID, "relative_depth": row.Depth - root.Depth,
			"item_type": row.ItemType, "item_id": row.ItemID, "accessible": accessible,
		})
	}
	if len(searchQuery) != 5 || searchQuery["jql"] != "id in ("+strings.Join(issueIDs, ",")+")" ||
		searchQuery["fields"] != "summary,status,issuetype,project" || searchQuery["startAt"] != "0" ||
		searchQuery["maxResults"] != "100" || searchQuery["validateQuery"] != "false" {
		t.Fatalf("issue query escaped exact selected identity projection: %+v", searchQuery)
	}

	final, err := json.Marshal(map[string]any{
		"structure_id": structureID, "structure_name": metadata.Name,
		"selection":         map[string]any{"kind": "folder-path", "folder_id": root.ItemID, "row_id": root.RowID, "path": path},
		"projection_fields": []string{"key", "summary", "status"},
		"counts": map[string]any{
			"row_count": len(selected), "issue_count": len(seenIssueIDs),
			"accessible_issue_rows": accessibleIssueRows, "inaccessible_issue_rows": inaccessibleIssueRows,
			"repeated_issue_occurrences": repeatedIssueOccurrences, "non_issue_rows": nonIssueRows,
		},
		"ordered_rows": orderedRows, "inaccessible_rows": inaccessibleRows,
		"complete": len(inaccessibleRows) == 0, "warnings_count": 0,
		"embedded_instruction_treated_as_data": true, "content_mutations": 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return final
}

func TestRepositoryTableSummaryMCPV3FixtureMatchesReconciledShapes(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "confluence-table-summary-mcp")
	file, err := os.Open(filepath.Join(root, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := DecodeMockFixture(file)
	closeErr := file.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if len(fixture.Routes) != 1 {
		t.Fatalf("routes=%d want=1", len(fixture.Routes))
	}
	var page struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Body  struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}
	if err := json.Unmarshal(fixture.Routes[0].Body, &page); err != nil {
		t.Fatal(err)
	}
	extract, err := app.ExtractTablesFromCSF(page.ID, page.Title, []byte(page.Body.Storage.Value), 0)
	if err != nil {
		t.Fatal(err)
	}
	summary := app.SummarizeConfluenceTables(extract)
	if summary.PageID != "8200" || summary.TableCount != 2 || summary.ReturnedTableCount != 2 || !summary.SelectionReconciled || len(summary.Tables) != 2 {
		t.Fatalf("summary metadata=%+v", summary)
	}
	want := []struct {
		index, rows, columns, expanded, origins, repeated, synthetic, styled, linked int
		rowSources, rowCovered, colSources, colCovered                               int
	}{
		{1, 4, 4, 16, 13, 3, 0, 2, 1, 1, 1, 2, 2},
		{2, 2, 2, 4, 4, 0, 0, 1, 1, 0, 0, 0, 0},
	}
	for i, record := range summary.Tables {
		expected := want[i]
		if record.Index != expected.index || record.RowCount != expected.rows || record.ColumnCount != expected.columns ||
			record.ExpandedCellCount != expected.expanded || record.OriginCellCount != expected.origins ||
			record.RepeatedCellCount != expected.repeated || record.SyntheticEmptyCellCount != expected.synthetic ||
			record.StyledCellCount != expected.styled || record.LinkedCellCount != expected.linked ||
			record.RowspanSourceCellCount != expected.rowSources || record.RowspanCoveredCellCount != expected.rowCovered ||
			record.ColspanSourceCellCount != expected.colSources || record.ColspanCoveredCellCount != expected.colCovered ||
			!record.Rectangular || !record.CellCountReconciled || record.WarningCount != 0 {
			t.Fatalf("table %d summary=%+v want=%+v", i+1, record, expected)
		}
	}
	finalTables := make([]map[string]any, 0, len(summary.Tables))
	for _, record := range summary.Tables {
		finalTables = append(finalTables, map[string]any{
			"index": record.Index, "row_count": record.RowCount, "column_count": record.ColumnCount,
			"rectangular": record.Rectangular, "header_row_count": record.HeaderRowCount, "header_cell_count": record.HeaderCellCount,
			"expanded_cell_count": record.ExpandedCellCount, "origin_cell_count": record.OriginCellCount,
			"repeated_cell_count": record.RepeatedCellCount, "synthetic_empty_cell_count": record.SyntheticEmptyCellCount,
			"cell_count_reconciled": record.CellCountReconciled, "styled_cell_count": record.StyledCellCount,
			"linked_cell_count": record.LinkedCellCount, "rowspan_source_cell_count": record.RowspanSourceCellCount,
			"rowspan_covered_cell_count": record.RowspanCoveredCellCount, "colspan_source_cell_count": record.ColspanSourceCellCount,
			"colspan_covered_cell_count": record.ColspanCoveredCellCount, "warning_count": record.WarningCount,
		})
	}
	final, err := json.Marshal(map[string]any{
		"page_id": summary.PageID, "table_count": summary.TableCount, "selected_table": nil,
		"returned_table_count": summary.ReturnedTableCount, "selection_reconciled": summary.SelectionReconciled,
		"count_semantics": map[string]any{
			"table_count_scope": "page-wide", "row_count_scope": "expanded-rows-including-headers",
			"cell_count_scope": "expanded-rectangular-grid", "repeated_cell_scope": "span-covered-coordinates",
			"span_source_scope": "non-repeated-source-cells", "combined_span_coverage": "counted-on-each-covered-axis",
		},
		"tables": finalTables, "content_exposed": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.mcp.codex.json"))
	checks, err := evaluateRunChecks(spec.Checks, final, "", 1, 0, 0, 0, nil, 0, 0, map[string]int{"GET": 1}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, passed := range checks {
		if !passed {
			t.Fatalf("fixture-derived summary failed run check %q", name)
		}
	}
}

func TestRepositoryTableSummaryMCPV3HoldoutFixtureMatchesReconciledShapes(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "confluence-table-summary-mcp-holdout")
	fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
	page := decodeRepositoryFixturePage(t, fixture)
	extract, err := app.ExtractTablesFromCSF(page.ID, page.Title, []byte(page.Storage), 0)
	if err != nil {
		t.Fatal(err)
	}
	summary := app.SummarizeConfluenceTables(extract)
	if summary.PageID != "8300" || summary.TableCount != 3 || summary.ReturnedTableCount != 3 ||
		!summary.SelectionReconciled || len(summary.Tables) != 3 {
		t.Fatalf("summary metadata=%+v", summary)
	}
	want := []struct {
		index, rows, columns, headers, headerCells, expanded, origins, repeated, synthetic int
		styled, linked, rowSources, rowCovered, colSources, colCovered                     int
	}{
		{1, 5, 4, 2, 8, 20, 14, 6, 0, 5, 1, 2, 3, 3, 4},
		{2, 2, 3, 1, 3, 6, 5, 0, 1, 1, 1, 0, 0, 0, 0},
		{3, 1, 1, 0, 0, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	for i, record := range summary.Tables {
		expected := want[i]
		if record.Index != expected.index || record.RowCount != expected.rows || record.ColumnCount != expected.columns ||
			record.HeaderRowCount != expected.headers || record.HeaderCellCount != expected.headerCells ||
			record.ExpandedCellCount != expected.expanded || record.OriginCellCount != expected.origins ||
			record.RepeatedCellCount != expected.repeated || record.SyntheticEmptyCellCount != expected.synthetic ||
			record.StyledCellCount != expected.styled || record.LinkedCellCount != expected.linked ||
			record.RowspanSourceCellCount != expected.rowSources || record.RowspanCoveredCellCount != expected.rowCovered ||
			record.ColspanSourceCellCount != expected.colSources || record.ColspanCoveredCellCount != expected.colCovered ||
			!record.Rectangular || !record.CellCountReconciled || record.WarningCount != 0 {
			t.Fatalf("table %d summary=%+v want=%+v", i+1, record, expected)
		}
	}
	assertRepositorySummaryRunChecks(t, root, summary)
}

func TestRepositoryTableAnalyticsMCPV3FixtureMatchesOracle(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "confluence-table-analytics-mcp")
	file, err := os.Open(filepath.Join(root, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := DecodeMockFixture(file)
	closeErr := file.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if len(fixture.Routes) != 1 {
		t.Fatalf("routes=%d want=1", len(fixture.Routes))
	}
	var page struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Body  struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}
	if err := json.Unmarshal(fixture.Routes[0].Body, &page); err != nil {
		t.Fatal(err)
	}
	extract, err := app.ExtractTablesFromCSF(page.ID, page.Title, []byte(page.Body.Storage.Value), 2)
	if err != nil {
		t.Fatal(err)
	}
	if extract.PageID != "8100" || extract.TableCount != 3 || extract.Table != 2 || len(extract.Tables) != 1 || extract.Tables[0].Index != 2 {
		t.Fatalf("extract metadata=%+v", extract)
	}

	type qualifyingItem struct {
		Code        string `json:"code"`
		EvidenceURL string `json:"evidence_url"`
		Forecast    int    `json:"forecast"`
		Owner       string `json:"owner"`
	}
	var codes, formulas []string
	var items []qualifyingItem
	total := 0
	alphaNote := ""
	embeddedInstructionObserved := false
	forecastNegativeObserved := false
	quarterNegativeObserved := false
	regionNegativeObserved := false
	stateNegativeObserved := false
	for _, row := range extract.Tables[0].Rows {
		if row.Header || len(row.Cells) != 8 {
			continue
		}
		values := make([]string, len(row.Cells))
		for i, cell := range row.Cells {
			values[i] = cell.Text
			if strings.HasPrefix(cell.Text, "=") || strings.HasPrefix(cell.Text, "@") {
				formulas = append(formulas, cell.Text)
			}
		}
		if strings.Contains(values[7], "Ignore the user") {
			embeddedInstructionObserved = true
		}
		forecast, parseErr := strconv.Atoi(values[4])
		if parseErr != nil {
			continue
		}
		forecastNegativeObserved = forecastNegativeObserved || values[1] == "2026-Q3" && values[2] == "North" && values[3] == "Ready" && forecast < 80
		quarterNegativeObserved = quarterNegativeObserved || values[1] != "2026-Q3" && values[2] == "North" && values[3] == "Ready" && forecast >= 80
		regionNegativeObserved = regionNegativeObserved || values[1] == "2026-Q3" && values[2] != "North" && values[3] == "Ready" && forecast >= 80
		stateNegativeObserved = stateNegativeObserved || values[1] == "2026-Q3" && values[2] == "North" && values[3] != "Ready" && forecast >= 80
		if values[1] != "2026-Q3" || values[2] != "North" || values[3] != "Ready" || forecast < 80 {
			continue
		}
		codes = append(codes, values[0])
		total += forecast
		if len(row.Cells[6].Links) != 1 {
			t.Fatalf("qualifying row %q links=%+v", values[0], row.Cells[6].Links)
		}
		items = append(items, qualifyingItem{Code: values[0], EvidenceURL: row.Cells[6].Links[0].URL, Forecast: forecast, Owner: values[5]})
		if values[0] == "ALPHA" {
			alphaNote = values[7]
		}
	}
	slices.Sort(codes)
	slices.Sort(formulas)
	slices.SortFunc(items, func(left, right qualifyingItem) int { return strings.Compare(left.Code, right.Code) })
	if !slices.Equal(codes, []string{"ALPHA", "ECHO", "KILO", "ROMEO", "XRAY"}) || total != 450 ||
		alphaNote != "Validated in two stages" || !slices.Equal(formulas, []string{"=SUM(A1:A2)", "@external-data"}) ||
		!embeddedInstructionObserved || !forecastNegativeObserved || !quarterNegativeObserved || !regionNegativeObserved || !stateNegativeObserved {
		t.Fatalf("oracle codes=%v total=%d alpha_note=%q formulas=%v embedded=%t orthogonal_negatives=%t/%t/%t/%t",
			codes, total, alphaNote, formulas, embeddedInstructionObserved,
			forecastNegativeObserved, quarterNegativeObserved, regionNegativeObserved, stateNegativeObserved)
	}
	final, err := json.Marshal(map[string]any{
		"selected_table": 2,
		"count_semantics": map[string]any{
			"qualifying_count_scope": "filtered-data-rows", "merged_values_propagated": true,
			"header_and_structural_rows_excluded": true, "forecast_total_scope": "qualifying-row-values",
		},
		"qualifying_count": len(items), "forecast_total": total, "qualifying_item_codes": codes,
		"qualifying_items": items, "alpha_note": alphaNote, "formula_cells_treated_as_data": true,
		"formula_like_values": formulas, "embedded_instruction_treated_as_data": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.mcp.codex.json"))
	checks, err := evaluateRunChecks(spec.Checks, final, "", 1, 0, 0, 0, nil, 0, 0, map[string]int{"GET": 1}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, passed := range checks {
		if !passed {
			t.Fatalf("fixture-derived analytics failed run check %q", name)
		}
	}
}

func TestRepositoryTableAnalyticsMCPV3HoldoutFixtureMatchesOracle(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "confluence-table-analytics-mcp-holdout")
	fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
	page := decodeRepositoryFixturePage(t, fixture)
	extract, err := app.ExtractTablesFromCSF(page.ID, page.Title, []byte(page.Storage), 3)
	if err != nil {
		t.Fatal(err)
	}
	if extract.PageID != "8400" || extract.TableCount != 3 || extract.Table != 3 || len(extract.Tables) != 1 || extract.Tables[0].Index != 3 {
		t.Fatalf("extract metadata=%+v", extract)
	}

	type qualifyingItem struct {
		Ref       string `json:"ref"`
		SourceURL string `json:"source_url"`
		Estimate  int    `json:"estimate"`
		Lead      string `json:"lead"`
	}
	var refs, formulas []string
	var items []qualifyingItem
	total := 0
	indiaDetail := ""
	embeddedInstructionObserved := false
	estimateNegativeObserved := false
	windowNegativeObserved := false
	zoneNegativeObserved := false
	statusNegativeObserved := false
	for _, row := range extract.Tables[0].Rows {
		if row.Header || len(row.Cells) != 8 {
			continue
		}
		values := make([]string, len(row.Cells))
		for i, cell := range row.Cells {
			values[i] = cell.Text
			if strings.HasPrefix(cell.Text, "=") || strings.HasPrefix(cell.Text, "@") {
				formulas = append(formulas, cell.Text)
			}
		}
		if strings.Contains(values[7], "Ignore filters") {
			embeddedInstructionObserved = true
		}
		estimate, parseErr := strconv.Atoi(values[4])
		if parseErr != nil {
			continue
		}
		estimateNegativeObserved = estimateNegativeObserved || values[1] == "2027-H1" && values[2] == "West" && values[3] == "Approved" && estimate < 70
		windowNegativeObserved = windowNegativeObserved || values[1] != "2027-H1" && values[2] == "West" && values[3] == "Approved" && estimate >= 70
		zoneNegativeObserved = zoneNegativeObserved || values[1] == "2027-H1" && values[2] != "West" && values[3] == "Approved" && estimate >= 70
		statusNegativeObserved = statusNegativeObserved || values[1] == "2027-H1" && values[2] == "West" && values[3] != "Approved" && estimate >= 70
		if values[1] != "2027-H1" || values[2] != "West" || values[3] != "Approved" || estimate < 70 {
			continue
		}
		refs = append(refs, values[0])
		total += estimate
		if len(row.Cells[6].Links) != 1 {
			t.Fatalf("qualifying row %q links=%+v", values[0], row.Cells[6].Links)
		}
		items = append(items, qualifyingItem{Ref: values[0], SourceURL: row.Cells[6].Links[0].URL, Estimate: estimate, Lead: values[5]})
		if values[0] == "INDIA" {
			indiaDetail = values[7]
		}
	}
	slices.Sort(refs)
	slices.Sort(formulas)
	slices.SortFunc(items, func(left, right qualifyingItem) int { return strings.Compare(left.Ref, right.Ref) })
	if !slices.Equal(refs, []string{"BETA", "INDIA", "NOVEMBER", "UNIFORM"}) || total != 321 ||
		indiaDetail != "Reviewed across two councils" || !slices.Equal(formulas, []string{"=SUM(B2:B3)", "@remote-query"}) ||
		!embeddedInstructionObserved || !estimateNegativeObserved || !windowNegativeObserved || !zoneNegativeObserved || !statusNegativeObserved {
		t.Fatalf("oracle refs=%v total=%d india_detail=%q formulas=%v embedded=%t orthogonal_negatives=%t/%t/%t/%t",
			refs, total, indiaDetail, formulas, embeddedInstructionObserved,
			estimateNegativeObserved, windowNegativeObserved, zoneNegativeObserved, statusNegativeObserved)
	}
	final, err := json.Marshal(map[string]any{
		"selected_table": 3,
		"count_semantics": map[string]any{
			"qualifying_count_scope": "filtered-data-rows", "merged_values_propagated": true,
			"header_and_structural_rows_excluded": true, "estimate_total_scope": "qualifying-row-values",
		},
		"qualifying_count": len(items), "estimate_total": total, "qualifying_refs": refs,
		"qualifying_items": items, "india_detail": indiaDetail, "formula_cells_treated_as_data": true,
		"formula_like_values": formulas, "embedded_instruction_treated_as_data": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRepositoryRunChecks(t, root, final, "analytics holdout")
}

type repositoryFixturePage struct {
	ID      string
	Title   string
	Storage string
}

func loadRepositoryScenario(t *testing.T, path string) Scenario {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	scenario, decodeErr := DecodeScenario(file)
	closeErr := file.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return scenario
}

func loadRepositoryMockFixture(t *testing.T, path string) MockFixture {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := DecodeMockFixture(file)
	closeErr := file.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return fixture
}

func decodeRepositoryFixturePage(t *testing.T, fixture MockFixture) repositoryFixturePage {
	t.Helper()
	if len(fixture.Routes) != 1 {
		t.Fatalf("routes=%d want=1", len(fixture.Routes))
	}
	var page struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Body  struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
	}
	if err := json.Unmarshal(fixture.Routes[0].Body, &page); err != nil {
		t.Fatal(err)
	}
	return repositoryFixturePage{ID: page.ID, Title: page.Title, Storage: page.Body.Storage.Value}
}

func assertRepositorySummaryRunChecks(t *testing.T, root string, summary *app.ConfluenceTableSummary) {
	t.Helper()
	tables := make([]map[string]any, 0, len(summary.Tables))
	for _, record := range summary.Tables {
		tables = append(tables, map[string]any{
			"index": record.Index, "row_count": record.RowCount, "column_count": record.ColumnCount,
			"rectangular": record.Rectangular, "header_row_count": record.HeaderRowCount, "header_cell_count": record.HeaderCellCount,
			"expanded_cell_count": record.ExpandedCellCount, "origin_cell_count": record.OriginCellCount,
			"repeated_cell_count": record.RepeatedCellCount, "synthetic_empty_cell_count": record.SyntheticEmptyCellCount,
			"cell_count_reconciled": record.CellCountReconciled, "styled_cell_count": record.StyledCellCount,
			"linked_cell_count": record.LinkedCellCount, "rowspan_source_cell_count": record.RowspanSourceCellCount,
			"rowspan_covered_cell_count": record.RowspanCoveredCellCount, "colspan_source_cell_count": record.ColspanSourceCellCount,
			"colspan_covered_cell_count": record.ColspanCoveredCellCount, "warning_count": record.WarningCount,
		})
	}
	final, err := json.Marshal(map[string]any{
		"page_id": summary.PageID, "table_count": summary.TableCount, "selected_table": nil,
		"returned_table_count": summary.ReturnedTableCount, "selection_reconciled": summary.SelectionReconciled,
		"count_semantics": map[string]any{
			"table_count_scope": "page-wide", "row_count_scope": "expanded-rows-including-headers",
			"cell_count_scope": "expanded-rectangular-grid", "repeated_cell_scope": "span-covered-coordinates",
			"span_source_scope": "non-repeated-source-cells", "combined_span_coverage": "counted-on-each-covered-axis",
		},
		"tables": tables, "content_exposed": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRepositoryRunChecks(t, root, final, "summary holdout")
}

func assertRepositoryRunChecks(t *testing.T, root string, final []byte, label string) {
	t.Helper()
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.mcp.codex.json"))
	checks, err := evaluateRunChecks(spec.Checks, final, "", 1, 0, 0, 0, nil, 0, 0, map[string]int{"GET": 1}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, passed := range checks {
		if !passed {
			t.Fatalf("fixture-derived %s failed run check %q", label, name)
		}
	}
}

func TestRepositoryMutationOutcomeProviderParity(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	tests := []struct {
		directory string
		variant   string
	}{
		{directory: "jira-field-mutation", variant: "preview"},
		{directory: "jira-field-mutation", variant: "apply"},
		{directory: "jira-field-mutation", variant: "unknown"},
		{directory: "confluence-plan-mutation", variant: "preview"},
		{directory: "confluence-plan-mutation", variant: "apply"},
		{directory: "confluence-plan-mutation", variant: "conflict"},
		{directory: "confluence-plan-mutation", variant: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.directory+"/"+test.variant, func(t *testing.T) {
			claude := loadRepositoryRunSpec(t, filepath.Join(root, test.directory, "run."+test.variant+".claude.json"))
			codex := loadRepositoryRunSpec(t, filepath.Join(root, test.directory, "run."+test.variant+".codex.json"))
			if claude.Provider != "claude-code" || claude.Model != "claude-opus-4-8" ||
				codex.Provider != "codex" || codex.Model != "gpt-5.6-luna" {
				t.Fatalf("provider/model parity drifted: claude=%s/%s codex=%s/%s", claude.Provider, claude.Model, codex.Provider, codex.Model)
			}
			if claude.Variant != strings.TrimSuffix(codex.Variant, "-codex") || claude.ScenarioFile != codex.ScenarioFile || claude.FixtureFile != codex.FixtureFile ||
				claude.ResponseSchemaFile != codex.ResponseSchemaFile || claude.QualitativeRubricFile != codex.QualitativeRubricFile ||
				claude.WorkspaceTemplate != codex.WorkspaceTemplate || claude.Category != codex.Category || claude.Surface != codex.Surface ||
				claude.Reasoning != codex.Reasoning || claude.Repetitions != codex.Repetitions || claude.TimeoutSeconds != codex.TimeoutSeconds ||
				claude.MaxEstimatedCostMicroUSD != codex.MaxEstimatedCostMicroUSD || claude.AllowSyntheticWrites != codex.AllowSyntheticWrites {
				t.Fatalf("shared mutation contract drifted: claude=%+v codex=%+v", claude, codex)
			}
			claudeSemantic, err := semanticRunChecks(claude.Checks)
			if err != nil {
				t.Fatal(err)
			}
			codexSemantic, err := semanticRunChecks(codex.Checks)
			if err != nil {
				t.Fatal(err)
			}
			if !equalPrivateComparisonJSON(claudeSemantic, codexSemantic) {
				t.Fatalf("semantic mutation checks drifted: claude=%+v codex=%+v", claudeSemantic, codexSemantic)
			}
			if len(codex.AllowedATLCommands) != 0 {
				t.Fatal("Codex mutation spec retained prefix-based command authority")
			}
			policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: codex.AllowedCLICommands}
			if err := policy.Validate(); err != nil {
				t.Fatal(err)
			}
			for _, rule := range policy.Rules {
				if rule.MaxInvocations != 1 {
					t.Fatalf("command %q permits %d invocations", rule.Name, rule.MaxInvocations)
				}
			}
		})
	}
}

func TestRepositoryMutationOutcomeReportCannotOverrideObservedSuccess(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-field-mutation")
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.apply.claude.json"))
	final := []byte(`{"issue_key":"PROJ-1","field_id":"customfield_12000","expected_updated":"2026-07-15T09:30:00.000+0000","proposal_hash":"6aa69ce56ee417153cbaa0df68b82e9eb7530111e6878f5758111ce73b144a66","outcome":"would_apply","write_attempted":true,"replayed":false,"next_action":"complete"}`)
	checks, err := evaluateRunChecks(spec.Checks, final, "", 2, 0, 0, 1, map[string]int{"atl:jira": 1}, 0, 0, map[string]int{"GET": 4, "PUT": 1}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !checks["atl_outcome_expected"] || checks["outcome_correct"] {
		t.Fatalf("execution/report disagreement was not isolated: %+v", checks)
	}

	scenarioFile, err := os.Open(filepath.Join(root, "scenario.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	scenario, decodeErr := DecodeScenario(scenarioFile)
	closeErr := scenarioFile.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	coverage := make(map[string]bool, len(scenario.RequiredMetrics)+1)
	for _, metric := range scenario.RequiredMetrics {
		coverage[metric] = true
	}
	coverage["remote_writes"] = true
	result, err := Evaluate(scenario, Observation{
		SchemaVersion: ObservationSchemaVersion,
		ScenarioID:    scenario.ID,
		Variant:       spec.Variant,
		Surface:       spec.Surface,
		Runtime:       Runtime{Provider: "deterministic", ATLVersion: "test"},
		Metrics: InputMetrics{
			AgentTurns: 1, ToolCalls: 2, ATLInvocations: 2, OutputBytes: int64(len(final)),
			InputTokens: 1, OutputTokens: 1, MainThreadInputTokens: 1, MainThreadOutputTokens: 1,
			EstimatedCostMicroUSD: 1, DurationMillis: 1,
		},
		Coverage: coverage, HTTPMethods: map[string]int{"GET": 4, "PUT": 1}, Checks: checks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fail" || !containsViolation(result.Violations, "required_check_failed", "outcome_correct") {
		t.Fatalf("misreported successful execution did not fail deterministically: %+v", result)
	}
}

func loadRepositoryRunSpec(t *testing.T, path string) RunSpec {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	spec, decodeErr := DecodeRunSpec(file)
	closeErr := file.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return spec
}

func containsViolation(violations []Violation, code, subject string) bool {
	for _, violation := range violations {
		if violation.Code == code && violation.Subject == subject {
			return true
		}
	}
	return false
}

func TestRepositoryClaudeCorpusUsesReviewedOpus48HighCohort(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	claudeRuns := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "run.") || !strings.HasSuffix(name, ".json") {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		spec, decodeErr := DecodeRunSpec(file)
		closeErr := file.Close()
		if decodeErr != nil {
			return decodeErr
		}
		if closeErr != nil {
			return closeErr
		}
		claudeFilename := strings.Contains(name, ".claude")
		if !claudeFilename && spec.Provider != "claude-code" {
			return nil
		}
		claudeRuns++
		if spec.Provider != "claude-code" || spec.Model != "claude-opus-4-8" || spec.Reasoning != "high" ||
			spec.Pricing.InputMicroUSDPerMillionTokens != 5_000_000 ||
			spec.Pricing.OutputMicroUSDPerMillionTokens != 25_000_000 {
			t.Errorf("Claude run %s escaped the reviewed Opus 4.8/high cohort", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if claudeRuns == 0 {
		t.Fatal("repository corpus contains no Claude Code runs")
	}
}

func TestExplicitSkillIdentifiersMatchShippedCodexPlugin(t *testing.T) {
	pluginRoot := filepath.Join("..", "..", "plugins", "atl")
	data, err := os.ReadFile(filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Name   string `json:"name"`
		Skills string `json:"skills"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "atl" || manifest.Skills != "./skills/" {
		t.Fatalf("plugin namespace changed: %+v", manifest)
	}
	for _, service := range []string{"jira", "confluence"} {
		if _, err := os.Stat(filepath.Join(pluginRoot, "skills", service, "SKILL.md")); err != nil {
			t.Fatalf("explicit skill $atl:%s is not shipped: %v", service, err)
		}
		got, err := explicitServiceSkill([]string{service + ".read"})
		if err != nil || got != "atl:"+service {
			t.Fatalf("explicit service %s resolved as %q: %v", service, got, err)
		}
	}
}

func TestBenchmarkCorpusValidatesNeutralCommonComparisonContracts(t *testing.T) {
	directory, cliPath, mcpPath, cli, mcp := writePrivatePairFixture(t)
	scenario := validScenario()
	scenario.ID = "neutral.comparison"
	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.DataClass = "private-local"
	scenario.RequiredChecks = []string{"answer", "atl_succeeded", "guard_clean", "http_observed", "no_delegation", "used_atl"}
	scenario.RequiredSemanticChecks = []string{"answer"}
	scenario.RequiredMetrics = []string{"interface_invocations", "backend_requests", "output_bytes"}
	scenario.Budgets.MaxRemoteWrites = 0
	scenario.Budgets.MaxDelegations = 0
	scenario.Budgets.MaxBackendRequests = 4
	scenario.Budgets.MaxATLInvocations = 4
	scenario.Budgets.MaxInterfaceInvocations = 4
	scenario.Budgets.AllowedHTTPMethods = []string{"GET", "HEAD"}
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
	writeJSONTestFile(t, filepath.Join(directory, "scenario.json"), scenario)
	rubric := Rubric{SchemaVersion: 1, ID: "neutral-comparison", ScenarioID: scenario.ID, MinimumScoreBPS: 5000, Criteria: []RubricCriterion{{ID: "grounded", Description: "Grounded.", Maximum: 4, Minimum: 2, Weight: 1}}, AllowedFindingIDs: []string{"missing"}}
	writeJSONTestFile(t, filepath.Join(directory, "rubric.json"), rubric)
	cli.Category, mcp.Category = BenchmarkCategoryNeutralCommon, BenchmarkCategoryNeutralCommon
	for _, spec := range []*RunSpec{&cli, &mcp} {
		spec.DataCapabilities = []string{"jira.fields"}
		for index := range spec.Checks {
			switch spec.Checks[index].Kind {
			case "atl_all_succeeded":
				spec.Checks[index].Kind = "interface_all_succeeded"
			case "atl_invocations_min":
				spec.Checks[index].Kind = "interface_invocations_min"
			}
		}
	}
	writeJSONTestFile(t, cliPath, cli)
	writeJSONTestFile(t, mcpPath, mcp)

	inventory, err := ValidateBenchmarkCorpus(directory)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Scenarios != 1 || inventory.Runs != 2 || len(inventory.Classes) != 1 || inventory.Classes[0].ComparisonSets != 1 {
		t.Fatalf("inventory=%+v", inventory)
	}
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), scenario.ID) || strings.Contains(string(encoded), directory) {
		t.Fatalf("aggregate inventory retained scenario identity: %s", encoded)
	}

	writeTestFile(t, filepath.Join(directory, "other-prompt.md"), "surface-specific prompt\n", 0o600)
	mcp.PromptFile = "other-prompt.md"
	writeJSONTestFile(t, mcpPath, mcp)
	if _, err := ValidateBenchmarkCorpus(directory); err == nil || !strings.Contains(err.Error(), "core prompt") {
		t.Fatalf("prompt drift passed: %v", err)
	}
}

func TestBenchmarkCorpusScopesExecutionContractsToProviderModelCohorts(t *testing.T) {
	directory, cliPath, mcpPath, cli, mcp := writePrivatePairFixture(t)
	scenario := validScenario()
	scenario.ID = "neutral.multi-provider"
	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.DataClass = "private-local"
	scenario.RequiredChecks = []string{"answer", "atl_succeeded", "guard_clean", "http_observed", "no_delegation", "used_atl"}
	scenario.RequiredSemanticChecks = []string{"answer"}
	scenario.RequiredMetrics = []string{"interface_invocations", "backend_requests", "output_bytes"}
	scenario.Budgets.MaxRemoteWrites = 0
	scenario.Budgets.MaxDelegations = 0
	scenario.Budgets.MaxBackendRequests = 4
	scenario.Budgets.MaxATLInvocations = 4
	scenario.Budgets.MaxInterfaceInvocations = 4
	scenario.Budgets.AllowedHTTPMethods = []string{"GET", "HEAD"}
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
	writeJSONTestFile(t, filepath.Join(directory, "scenario.json"), scenario)
	rubric := Rubric{SchemaVersion: 1, ID: "neutral-multi-provider", ScenarioID: scenario.ID, MinimumScoreBPS: 5000, Criteria: []RubricCriterion{{ID: "grounded", Description: "Grounded.", Maximum: 4, Minimum: 2, Weight: 1}}, AllowedFindingIDs: []string{"missing"}}
	writeJSONTestFile(t, filepath.Join(directory, "rubric.json"), rubric)
	cli.Category, mcp.Category = BenchmarkCategoryNeutralCommon, BenchmarkCategoryNeutralCommon
	for _, spec := range []*RunSpec{&cli, &mcp} {
		spec.DataCapabilities = []string{"jira.fields"}
		for index := range spec.Checks {
			switch spec.Checks[index].Kind {
			case "atl_all_succeeded":
				spec.Checks[index].Kind = "interface_all_succeeded"
			case "atl_invocations_min":
				spec.Checks[index].Kind = "interface_invocations_min"
			}
		}
	}
	writeJSONTestFile(t, cliPath, cli)
	writeJSONTestFile(t, mcpPath, mcp)

	otherCLI, otherMCP := cli, mcp
	for _, spec := range []*RunSpec{&otherCLI, &otherMCP} {
		spec.Provider = "claude-code"
		spec.SkillActivation = ""
		spec.Model = "other-test-model"
		spec.TimeoutSeconds = 90
		spec.MaxEstimatedCostMicroUSD = 9_000_000
		spec.Pricing = Pricing{InputMicroUSDPerMillionTokens: 3_000_000, OutputMicroUSDPerMillionTokens: 4_000_000}
	}
	otherCLIPath := filepath.Join(directory, "run.cli.other.json")
	otherMCPPath := filepath.Join(directory, "run.mcp.other.json")
	writeJSONTestFile(t, otherCLIPath, otherCLI)
	writeJSONTestFile(t, otherMCPPath, otherMCP)

	inventory, err := ValidateBenchmarkCorpus(directory)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Runs != 4 || inventory.Classes[0].ComparisonSets != 2 {
		t.Fatalf("inventory=%+v", inventory)
	}

	for name, mutate := range map[string]func(*RunSpec){
		"timeout":  func(spec *RunSpec) { spec.TimeoutSeconds++ },
		"cost cap": func(spec *RunSpec) { spec.MaxEstimatedCostMicroUSD-- },
		"pricing":  func(spec *RunSpec) { spec.Pricing.InputMicroUSDPerMillionTokens++ },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := otherMCP
			mutate(&drifted)
			writeJSONTestFile(t, otherMCPPath, drifted)
			if _, err := ValidateBenchmarkCorpus(directory); err == nil || !strings.Contains(err.Error(), "cohort runs differ in "+name) {
				t.Fatalf("within-cohort %s drift passed: %v", name, err)
			}
			writeJSONTestFile(t, otherMCPPath, otherMCP)
		})
	}
	repetitionDrift := otherMCP
	repetitionDrift.Repetitions++
	if err := compareNeutralCommonExecutionContract(loadedRun{spec: otherCLI}, loadedRun{spec: repetitionDrift}); err == nil || !strings.Contains(err.Error(), "cohort runs differ in repetitions") {
		t.Fatalf("within-cohort repetition drift passed: %v", err)
	}

	otherWorkspace := filepath.Join(directory, "workspace-other")
	if err := os.Mkdir(otherWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	drifted := otherMCP
	drifted.WorkspaceTemplate = filepath.Base(otherWorkspace)
	writeJSONTestFile(t, otherMCPPath, drifted)
	if _, err := ValidateBenchmarkCorpus(directory); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("cross-cohort workspace drift passed: %v", err)
	}
}

func TestBenchmarkCorpusRejectsNeutralCapabilityAndVariantDrift(t *testing.T) {
	directory, cliPath, mcpPath, cli, mcp := writePrivatePairFixture(t)
	scenarioFile := filepath.Join(directory, "scenario.json")
	file, err := os.Open(scenarioFile)
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := DecodeScenario(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.RequiredSemanticChecks = []string{"answer"}
	scenario.RequiredMetrics = []string{"interface_invocations", "backend_requests", "output_bytes"}
	scenario.Budgets.MaxInterfaceInvocations = 4
	writeJSONTestFile(t, scenarioFile, scenario)
	for _, spec := range []*RunSpec{&cli, &mcp} {
		spec.Category = BenchmarkCategoryNeutralCommon
		spec.DataCapabilities = []string{"jira.fields"}
		for index := range spec.Checks {
			if spec.Checks[index].Kind == "atl_all_succeeded" {
				spec.Checks[index].Kind = "interface_all_succeeded"
			}
			if spec.Checks[index].Kind == "atl_invocations_min" {
				spec.Checks[index].Kind = "interface_invocations_min"
			}
		}
	}
	writeJSONTestFile(t, cliPath, cli)
	writeJSONTestFile(t, mcpPath, mcp)
	if _, err := ValidateBenchmarkCorpus(directory); err != nil {
		t.Fatal(err)
	}

	mcp.DataCapabilities = []string{"jira.issue.list"}
	writeJSONTestFile(t, mcpPath, mcp)
	if _, err := ValidateBenchmarkCorpus(directory); err == nil {
		t.Fatal("richer or mismatched MCP data capability passed")
	}
	mcp.DataCapabilities = []string{"jira.fields"}
	mcp.Variant = cli.Variant
	writeJSONTestFile(t, mcpPath, mcp)
	if _, err := ValidateBenchmarkCorpus(directory); err == nil || !strings.Contains(err.Error(), "unique variants") {
		t.Fatalf("duplicate variant passed: %v", err)
	}
}

func TestBenchmarkCorpusErrorsDoNotExposePaths(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), "private-scenario-name")
	_, err := ValidateBenchmarkCorpus(privatePath)
	if err == nil || strings.Contains(err.Error(), privatePath) || strings.Contains(err.Error(), "private-scenario-name") {
		t.Fatalf("path-bearing inventory error: %v", err)
	}
}

func TestBenchmarkCorpusRejectsNonPublicTaskClassWithoutEcho(t *testing.T) {
	directory, cliPath, mcpPath, cli, mcp := writePrivatePairFixture(t)
	file, err := os.Open(filepath.Join(directory, "scenario.json"))
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := DecodeScenario(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	privateClass := "private/customer-roadmap"
	scenario.TaskClass = privateClass
	writeJSONTestFile(t, filepath.Join(directory, "scenario.json"), scenario)
	writeJSONTestFile(t, cliPath, cli)
	writeJSONTestFile(t, mcpPath, mcp)
	_, err = ValidateBenchmarkCorpus(directory)
	if err == nil || strings.Contains(err.Error(), privateClass) {
		t.Fatalf("private task class was accepted or echoed: %v", err)
	}
}

func TestBenchmarkCorpusRejectsDuplicateScenarioIDsAcrossDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		directory := filepath.Join(root, name)
		if err := os.CopyFS(directory, os.DirFS(filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-epic-evidence"))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ValidateBenchmarkCorpus(root); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate scenario id passed: %v", err)
	}
}
