package agenteval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type structureQualificationExpectation struct {
	directory   string
	structureID int64
	rootRow     int64
	path        []string
	readOnly    bool
	repetitions int
	canaries    []string
}

func TestRepositoryStructureQualificationProviderParityAndBudgets(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for _, expected := range repositoryStructureQualificationExpectations() {
		t.Run(expected.directory, func(t *testing.T) {
			directory := filepath.Join(root, expected.directory)
			scenario := loadRepositoryScenario(t, filepath.Join(directory, "scenario.v1.json"))
			if scenario.Budgets.MaxRemoteWrites != 1 || scenario.Budgets.MaxToolCalls != 3 ||
				scenario.Budgets.MaxATLInvocations != 0 || scenario.Budgets.MaxInterfaceInvocations != 2 ||
				scenario.Budgets.MaxDelegations != 0 || scenario.Budgets.MaxBackendRequests != 5 ||
				scenario.Budgets.MaxDuplicateBackendRequests != 1 ||
				!slices.Equal(scenario.Budgets.AllowedHTTPMethods, []string{"GET", "POST"}) ||
				!slices.Equal(scenario.RequiredCapabilities, []string{"jira.structure.get", "jira.structure.view"}) {
				t.Fatalf("qualification scenario escaped bounded read policy: %+v", scenario)
			}

			specs := make(map[string]RunSpec, 2)
			for _, provider := range []string{"claude", "codex"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(directory, "run.mcp."+provider+".json"))
				if spec.ScenarioFile != "scenario.v1.json" || spec.PromptFile != "prompt.mcp.v1.md" ||
					spec.ResponseSchemaFile != "response-schema.v1.json" || spec.QualitativeRubricFile != "rubric.v1.json" ||
					spec.EffectiveToolTransport() != "mcp" ||
					!slices.Equal(spec.AllowedMCPTools, []string{"jira_structure_get", "jira_structure_view"}) ||
					len(spec.AllowedTools) != 0 || len(spec.AllowedATLCommands) != 0 ||
					spec.Repetitions != expected.repetitions {
					t.Fatalf("%s spec escaped qualification contract: %+v", provider, spec)
				}
				specs[provider] = spec
			}

			claude, codex := specs["claude"], specs["codex"]
			if claude.Provider != "claude-code" || claude.Model != "claude-opus-4-8" ||
				codex.Provider != "codex" || codex.Model != "gpt-5.6-luna" {
				t.Fatalf("provider/model parity drifted: claude=%s/%s codex=%s/%s", claude.Provider, claude.Model, codex.Provider, codex.Model)
			}
			if claude.PromptFile != codex.PromptFile || claude.FixtureFile != codex.FixtureFile ||
				claude.ResponseSchemaFile != codex.ResponseSchemaFile ||
				claude.QualitativeRubricFile != codex.QualitativeRubricFile ||
				claude.WorkspaceTemplate != codex.WorkspaceTemplate || claude.Category != codex.Category ||
				claude.Surface != codex.Surface || claude.Variant != codex.Variant ||
				claude.Reasoning != codex.Reasoning || claude.Repetitions != codex.Repetitions ||
				claude.TimeoutSeconds != codex.TimeoutSeconds ||
				claude.MaxEstimatedCostMicroUSD != codex.MaxEstimatedCostMicroUSD ||
				!equalPrivateComparisonJSON(claude.Checks, codex.Checks) {
				t.Fatalf("provider contract drifted: claude=%+v codex=%+v", claude, codex)
			}
		})
	}
}

func TestRepositoryStructureQualificationHoldoutIsDistinct(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	primaryDirectory := filepath.Join(root, "jira-structure-qualification-mcp")
	holdoutDirectory := filepath.Join(root, "jira-structure-qualification-mcp-holdout")
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
	if repositoryTreeDigest(t, filepath.Join(primaryDirectory, "workspace")) ==
		repositoryTreeDigest(t, filepath.Join(holdoutDirectory, "workspace")) {
		t.Fatal("holdout reused the primary workspace tree")
	}
}

func TestRepositoryStructureQualificationFixturesMatchSafeOracles(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for _, expected := range repositoryStructureQualificationExpectations() {
		t.Run(expected.directory, func(t *testing.T) {
			directory := filepath.Join(root, expected.directory)
			final := repositoryStructureQualificationFinal(t, directory, expected)
			for _, canary := range expected.canaries {
				if strings.Contains(string(final), canary) {
					t.Fatalf("compact metadata projection leaked %q: %s", canary, final)
				}
			}

			spec := loadRepositoryRunSpec(t, filepath.Join(directory, "run.mcp.codex.json"))
			invocations := repositoryExpectedMCPInvocations(t, spec)
			if len(invocations) != 2 || invocations[0].Tool != "jira_structure_get" ||
				invocations[1].Tool != "jira_structure_view" {
				t.Fatalf("metadata-first route drifted: %+v", invocations)
			}
			capabilities := []CapabilityFamilyMetric{
				{Family: "jira.structure.get", Invocations: 1, Successes: 1, OutputBytes: 1},
				{Family: "jira.structure.view", Invocations: 1, Successes: 1, OutputBytes: int64(len(final))},
			}
			methods := map[string]int{"GET": 4, "POST": 1}
			checks, err := evaluateRunChecksWithMCPInvocations(
				spec.Checks, final, "", 2, 0, 0, 0, nil, 0, 0,
				methods, true, nil, capabilities, true,
				[]string{"jira.structure.get", "jira.structure.view"}, invocations, true,
			)
			if err != nil {
				t.Fatal(err)
			}
			for name, passed := range checks {
				if !passed {
					t.Fatalf("fixture-derived qualification result failed run check %q: %s", name, final)
				}
			}

			schemaBytes, err := os.ReadFile(filepath.Join(directory, spec.ResponseSchemaFile))
			if err != nil {
				t.Fatal(err)
			}
			for _, provider := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
				providerSpec := loadRepositoryRunSpec(t, filepath.Join(directory, provider))
				providerSchema, schemaErr := providerResponseSchema(providerSpec, schemaBytes)
				if schemaErr != nil {
					t.Fatalf("%s response schema is not provider-compatible: %v", providerSpec.Provider, schemaErr)
				}
				if schemaErr = validateHistoryBenchmarkSchemaInstance(providerSchema, final); schemaErr != nil {
					t.Fatalf("%s response schema rejected fixture-derived final: %v", providerSpec.Provider, schemaErr)
				}
			}

			scenario := loadRepositoryScenario(t, filepath.Join(directory, "scenario.v1.json"))
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
					AgentTurns: 1, ToolCalls: 2, InterfaceInvocations: 2,
					DuplicateBackendRequests: 1, OutputBytes: int64(len(final)),
					InputTokens: 1, OutputTokens: 1, MainThreadInputTokens: 1,
					MainThreadOutputTokens: 1, EstimatedCostMicroUSD: 1, DurationMillis: 1,
				},
				Coverage: coverage, HTTPMethods: methods, Checks: checks,
				CapabilityFamilies: capabilities,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "pass" || result.Metrics.BackendRequests != 5 ||
				result.Metrics.RemoteWrites != 1 || result.Metrics.DuplicateBackendRequests != 1 ||
				len(result.Violations) != 0 {
				t.Fatalf("fixture-derived scenario did not pass conservative transport budget: %+v", result)
			}

			var mutated map[string]any
			if err := json.Unmarshal(final, &mutated); err != nil {
				t.Fatal(err)
			}
			mutated["metadata"].(map[string]any)["owner"] = "forbidden"
			mutatedFinal, err := json.Marshal(mutated)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateHistoryBenchmarkSchemaInstance(schemaBytes, mutatedFinal); err == nil {
				t.Fatal("closed response schema accepted owner transport metadata")
			}
		})
	}
}

func TestRepositoryStructureQualificationExactRouteRejectsDrift(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for _, expected := range repositoryStructureQualificationExpectations() {
		t.Run(expected.directory, func(t *testing.T) {
			directory := filepath.Join(root, expected.directory)
			spec := loadRepositoryRunSpec(t, filepath.Join(directory, "run.mcp.codex.json"))
			final := repositoryStructureQualificationFinal(t, directory, expected)
			invocations := repositoryExpectedMCPInvocations(t, spec)
			mutated, ok := newMCPInvocation("jira_structure_get", map[string]any{"structure_id": expected.structureID + 1})
			if !ok {
				t.Fatal("failed to construct mutated invocation")
			}
			invocations[0] = mutated
			checks, err := evaluateRunChecksWithMCPInvocations(
				spec.Checks, final, "", 2, 0, 0, 0, nil, 0, 0,
				map[string]int{"GET": 4, "POST": 1}, true, nil,
				[]CapabilityFamilyMetric{
					{Family: "jira.structure.get", Invocations: 1, Successes: 1},
					{Family: "jira.structure.view", Invocations: 1, Successes: 1},
				},
				true, []string{"jira.structure.get", "jira.structure.view"}, invocations, true,
			)
			if err != nil {
				t.Fatal(err)
			}
			if checks["route_arguments"] {
				t.Fatal("wrong metadata Structure id passed exact route check")
			}
		})
	}
}

func repositoryStructureQualificationExpectations() []structureQualificationExpectation {
	return []structureQualificationExpectation{
		{
			directory: "jira-structure-qualification-mcp", structureID: 93,
			rootRow: 510, path: []string{"Plans", "Current"}, readOnly: true, repetitions: 3,
			canaries: []string{"OWNER-CANARY-PRIMARY", "PERMISSION-CANARY-PRIMARY", "VIEW-CANARY-PRIMARY"},
		},
		{
			directory: "jira-structure-qualification-mcp-holdout", structureID: 94,
			rootRow: 710, path: []string{"Capacity", "Week 28"}, readOnly: false, repetitions: 1,
			canaries: []string{"OWNER-CANARY-HOLDOUT", "PERMISSION-CANARY-HOLDOUT", "VIEW-CANARY-HOLDOUT"},
		},
	}
}

func repositoryStructureQualificationFinal(
	t *testing.T,
	directory string,
	expected structureQualificationExpectation,
) []byte {
	t.Helper()
	fixtureBytes, err := os.ReadFile(filepath.Join(directory, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range expected.canaries {
		if !strings.Contains(string(fixtureBytes), canary) {
			t.Fatalf("fixture lost privacy canary %q", canary)
		}
	}
	fixture := loadRepositoryMockFixture(t, filepath.Join(directory, "fixture.json"))
	var metadata struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		ReadOnly bool   `json:"readOnly"`
	}
	found := false
	for _, route := range fixture.Routes {
		if route.Method == "GET" && strings.HasSuffix(route.Path, "/structure/"+jsonNumber(expected.structureID)) {
			if err := json.Unmarshal(route.Body, &metadata); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found || metadata.ID != expected.structureID || metadata.Name == "" || metadata.ReadOnly != expected.readOnly {
		t.Fatalf("metadata fixture drifted: found=%t metadata=%+v", found, metadata)
	}

	base := repositoryStructureMCPFinal(t, directory, expected.structureID, expected.rootRow, expected.path)
	var final map[string]any
	if err := json.Unmarshal(base, &final); err != nil {
		t.Fatal(err)
	}
	delete(final, "structure_id")
	delete(final, "structure_name")
	final["metadata"] = map[string]any{
		"schema_version": 1,
		"id":             metadata.ID,
		"name":           metadata.Name,
		"read_only":      metadata.ReadOnly,
	}
	final["metadata_transport_fields_absent"] = true
	final["metadata_view_consistent"] = true
	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func jsonNumber(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
