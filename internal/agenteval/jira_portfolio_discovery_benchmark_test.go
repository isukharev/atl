package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func TestRepositoryJiraPortfolioDiscoveryFixturesDriveProviderOracles(t *testing.T) {
	tests := []struct {
		name            string
		directory       string
		project         string
		limit           int
		structureID     int64
		codexCommands   [][]string
		promptCommands  []string
		claudeCommands  []string
		expectedMethods map[string]int
	}{
		{
			name:        "complete nested primary",
			directory:   "jira-portfolio-source-discovery",
			project:     "ZX",
			limit:       10,
			structureID: 123,
			codexCommands: [][]string{
				{"capabilities", "--task", "jira/portfolio"},
				{"jira", "board", "list", "--project", "ZX", "--limit", "10"},
				{"jira", "structure", "folders", "123"},
			},
			promptCommands: []string{
				"atl capabilities --task jira/portfolio",
				"atl jira board list --project ZX --limit 10",
				"atl jira structure folders 123",
			},
			claudeCommands: []string{
				"atl capabilities --task jira/portfolio --",
				"atl jira board list --project ZX --limit 10 --",
				"atl jira structure folders 123 --",
			},
			expectedMethods: map[string]int{"GET": 3, "POST": 1},
		},
		{
			name:        "paginated partially labeled holdout",
			directory:   "jira-portfolio-source-discovery-holdout",
			project:     "ZY",
			limit:       1,
			structureID: 124,
			codexCommands: [][]string{
				{"capabilities", "--task", "jira/portfolio"},
				{"jira", "board", "list", "--project", "ZY", "--limit", "1"},
				{"jira", "structure", "folders", "124"},
			},
			promptCommands: []string{
				"atl capabilities --task jira/portfolio",
				"atl jira board list --project ZY --limit 1",
				"atl jira structure folders 124",
			},
			claudeCommands: []string{
				"atl capabilities --task jira/portfolio --",
				"atl jira board list --project ZY --limit 1 --",
				"atl jira structure folders 124 --",
			},
			expectedMethods: map[string]int{"GET": 3, "POST": 1},
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
			boards, next, err := service.Boards(context.Background(), test.project, test.limit, "")
			if err != nil {
				t.Fatal(err)
			}
			folders, err := service.StructureFolders(context.Background(), test.structureID)
			if err != nil {
				t.Fatal(err)
			}
			final := jiraPortfolioDiscoveryBenchmarkFinal(t, boards, next, folders)
			methods, unexpected, duplicates := backend.Summary()
			if unexpected != 0 || duplicates != 0 || !equalHTTPMethods(methods, test.expectedMethods) {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}

			for _, provider := range []string{"codex", "claude"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli."+provider+".json"))
				scenario := loadRepositoryScenario(t, filepath.Join(root, spec.ScenarioFile))
				assertJiraPortfolioDiscoveryTransportBudget(t, scenario, spec, methods)
				assertJiraPortfolioDiscoveryCommandPolicy(
					t, root, spec, test.promptCommands, test.claudeCommands, test.codexCommands,
				)
				assertJiraPortfolioDiscoverySchemaMatchesFinal(t, root, spec, final)
				checks, err := evaluateRunChecks(
					spec.Checks, final, "", 3, 0, unexpected, 1,
					map[string]int{"atl:jira": 1}, 0, 0, methods, true, []int{0, 0, 0},
				)
				if err != nil {
					t.Fatal(err)
				}
				for name, passed := range checks {
					if !passed {
						t.Fatalf("%s fixture-derived final failed run check %q", provider, name)
					}
				}
				assertJiraPortfolioDiscoveryCheckMutationFails(t, spec, final, methods)
			}
		})
	}
}

func assertJiraPortfolioDiscoveryTransportBudget(t *testing.T, scenario Scenario, spec RunSpec, methods map[string]int) {
	t.Helper()
	if scenario.Budgets.MaxRemoteWrites != 1 {
		t.Fatalf("Structure value read requires exactly one transport-level remote write budget, got %d", scenario.Budgets.MaxRemoteWrites)
	}
	if !slices.Contains(scenario.RequiredMetrics, "remote_writes") {
		t.Fatal("Structure value read must require observed remote_writes coverage")
	}
	coverage := make(map[string]bool, len(scenario.RequiredMetrics)+2)
	for _, metric := range scenario.RequiredMetrics {
		coverage[metric] = true
	}
	coverage["backend_requests"] = true
	coverage["duplicate_backend_requests"] = true
	coverage["remote_writes"] = true
	checks := make(map[string]bool, len(spec.Checks))
	for _, check := range spec.Checks {
		checks[check.Name] = true
	}
	observation := Observation{
		SchemaVersion:      ObservationSchemaVersion,
		ScenarioID:         scenario.ID,
		Variant:            spec.Variant,
		Surface:            SurfaceCLISkill,
		Eligibility:        EligibilitySupported,
		BackendObservation: BackendObservationHTTP,
		SafetyAssurance:    SafetyAssuranceObservedHTTP,
		Runtime: Runtime{
			Provider:             spec.Provider,
			Model:                spec.Model,
			Reasoning:            spec.Reasoning,
			ATLVersion:           "benchmark-contract",
			PromptContractSHA256: strings.Repeat("a", 64),
		},
		Metrics: InputMetrics{
			AgentTurns: 1, ToolCalls: 5, ATLInvocations: 3,
			OutputBytes: 4096, InputTokens: 1000, OutputTokens: 100,
			MainThreadInputTokens: 1000, MainThreadOutputTokens: 100,
			EstimatedCostMicroUSD: 1000, DurationMillis: 1000,
		},
		Coverage:    coverage,
		HTTPMethods: methods,
		Checks:      checks,
		CapabilityFamilies: []CapabilityFamilyMetric{
			{Family: "jira.structure.folders", Invocations: 1, Successes: 1, OutputBytes: 1},
		},
	}
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pass" || result.Metrics.RemoteWrites != 1 {
		t.Fatalf("read-semantic Structure POST was not accepted exactly once: %+v", result)
	}

	zeroBudget := scenario
	zeroBudget.Budgets.MaxRemoteWrites = 0
	result, err = Evaluate(zeroBudget, observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fail" {
		t.Fatalf("zero transport-write budget unexpectedly accepted Structure POST: %+v", result)
	}
	for _, violation := range result.Violations {
		if violation.Code == "budget_exceeded" && violation.Subject == "remote_writes" && violation.Observed == 1 {
			return
		}
	}
	t.Fatalf("zero budget did not produce the expected remote_writes violation: %+v", result.Violations)
}

func TestRepositoryJiraPortfolioDiscoverySamplingPairIdentity(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	primaryRoot := filepath.Join(root, "jira-portfolio-source-discovery")
	holdoutRoot := filepath.Join(root, "jira-portfolio-source-discovery-holdout")
	primaryScenario := loadRepositoryScenario(t, filepath.Join(primaryRoot, "scenario.v1.json"))
	holdoutScenario := loadRepositoryScenario(t, filepath.Join(holdoutRoot, "scenario.v1.json"))
	if primaryScenario.ID == holdoutScenario.ID ||
		primaryScenario.TaskClass != holdoutScenario.TaskClass ||
		primaryScenario.DataClass != holdoutScenario.DataClass {
		t.Fatalf("primary/holdout scenario identity is not distinct-compatible: primary=%+v holdout=%+v", primaryScenario, holdoutScenario)
	}

	for _, provider := range []string{"codex", "claude"} {
		primary := loadRepositoryRunSpec(t, filepath.Join(primaryRoot, "run.cli."+provider+".json"))
		holdout := loadRepositoryRunSpec(t, filepath.Join(holdoutRoot, "run.cli."+provider+".json"))
		expectedProvider := "codex"
		expectedModel := "gpt-5.6-luna"
		if provider == "claude" {
			expectedProvider = "claude-code"
			expectedModel = "claude-opus-4-8"
		}
		if primary.Provider != expectedProvider ||
			primary.Model != expectedModel ||
			primary.Reasoning != "high" ||
			primary.Repetitions != 3 ||
			holdout.Provider != expectedProvider ||
			holdout.Model != expectedModel ||
			holdout.Reasoning != "high" ||
			holdout.Repetitions != 1 {
			t.Fatalf("%s exact cohort contract drifted: primary=%+v holdout=%+v", provider, primary, holdout)
		}
		if primary.Variant != holdout.Variant ||
			primary.Provider != holdout.Provider ||
			primary.Model != holdout.Model ||
			primary.Reasoning != holdout.Reasoning ||
			primary.EffectiveCategory() != holdout.EffectiveCategory() ||
			primary.EffectiveSurface() != holdout.EffectiveSurface() {
			t.Fatalf("%s primary/holdout execution identity drifted: primary=%+v holdout=%+v", provider, primary, holdout)
		}
		primaryPrompt, err := os.ReadFile(filepath.Join(primaryRoot, primary.PromptFile))
		if err != nil {
			t.Fatal(err)
		}
		holdoutPrompt, err := os.ReadFile(filepath.Join(holdoutRoot, holdout.PromptFile))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(primaryPrompt, holdoutPrompt) {
			t.Fatalf("%s holdout does not have a distinct prompt contract", provider)
		}
	}
}

func jiraPortfolioDiscoveryBenchmarkFinal(t *testing.T, boards []domain.Board, next string, result *app.StructureFoldersResult) []byte {
	t.Helper()
	boardItems := make([]map[string]any, 0, len(boards))
	for _, board := range boards {
		boardItems = append(boardItems, map[string]any{
			"id": board.ID, "name": board.Name, "type": board.Type, "project_key": board.ProjectKey,
		})
	}
	folderItems := make([]map[string]any, 0, len(result.Folders))
	for _, folder := range result.Folders {
		folderItems = append(folderItems, map[string]any{
			"folder_id":          folder.FolderID,
			"row_id":             folder.RowID,
			"name":               folder.Name,
			"path":               folder.Path,
			"depth":              folder.Depth,
			"parent_folder_id":   folder.ParentFolderID,
			"descendant_rows":    folder.Stats.DescendantRows,
			"issue_rows":         folder.Stats.IssueRows,
			"unique_issues":      folder.Stats.UniqueIssues,
			"subfolders":         folder.Stats.Subfolders,
			"max_relative_depth": folder.Stats.MaxRelativeDepth,
		})
	}
	final := map[string]any{
		"boards": map[string]any{"count": len(boards), "next_cursor": next, "items": boardItems},
		"structure": map[string]any{
			"id":               result.Structure.ID,
			"name":             result.Structure.Name,
			"read_only":        result.Structure.ReadOnly,
			"complete":         result.Complete,
			"warning_count":    len(result.Warnings),
			"forest_signature": result.ForestVersion.Signature,
			"forest_version":   result.ForestVersion.Version,
			"folder_count":     len(result.Folders),
			"folders":          folderItems,
		},
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertJiraPortfolioDiscoveryCommandPolicy(
	t *testing.T,
	root string,
	spec RunSpec,
	promptCommands, claudeCommands []string,
	codexCommands [][]string,
) {
	t.Helper()
	prompt, err := os.ReadFile(filepath.Join(root, spec.PromptFile))
	if err != nil {
		t.Fatal(err)
	}
	expectedPromptCommands := promptCommands
	if spec.Provider == "claude-code" {
		expectedPromptCommands = claudeCommands
	}
	for _, command := range expectedPromptCommands {
		if !bytes.Contains(prompt, []byte("`"+command+"`")) {
			t.Fatalf("%s prompt does not contain reviewed command %q", spec.Provider, command)
		}
	}
	switch spec.Provider {
	case "codex":
		policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands}
		for _, command := range codexCommands {
			if _, err := policy.Match(command); err != nil {
				t.Fatalf("portfolio discovery command rejected by Codex policy: %v", err)
			}
		}
		for _, command := range codexCommands[1:] {
			if _, err := policy.Match(append(slices.Clone(command), "--limit", "50")); err == nil {
				t.Fatalf("Codex policy accepted appended command arguments: %v", command)
			}
		}
	case "claude-code":
		if !slices.Equal(spec.AllowedATLCommands, claudeCommands) {
			t.Fatalf("Claude policy does not contain the exact reviewed commands: got=%v want=%v", spec.AllowedATLCommands, claudeCommands)
		}
		for _, command := range claudeCommands {
			if !strings.HasSuffix(command, " --") {
				t.Fatalf("Claude discovery prefix lacks an option terminator: %q", command)
			}
		}
	default:
		t.Fatalf("unexpected provider %q", spec.Provider)
	}
}

func assertJiraPortfolioDiscoverySchemaMatchesFinal(t *testing.T, root string, spec RunSpec, final []byte) {
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
		if err := validateHistoryBenchmarkSchemaInstance(schema, final); err != nil {
			t.Fatalf("%s %s response schema rejected fixture-derived final: %v", spec.Provider, name, err)
		}
	}

	var mutated map[string]any
	if err := decodeHistoryBenchmarkJSON(schemaBytes, &mutated); err != nil {
		t.Fatal(err)
	}
	properties := mutated["properties"].(map[string]any)
	structure := properties["structure"].(map[string]any)
	folders := structure["properties"].(map[string]any)["folders"].(map[string]any)
	item := folders["items"].(map[string]any)
	issueRows := item["properties"].(map[string]any)["issue_rows"].(map[string]any)
	issueRows["type"] = "string"
	mutatedBytes, err := json.Marshal(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateHistoryBenchmarkSchemaInstance(mutatedBytes, final); err == nil {
		t.Fatal("fixture-derived final passed a schema with incompatible nested issue_rows type")
	}
}

func assertJiraPortfolioDiscoveryCheckMutationFails(t *testing.T, spec RunSpec, final []byte, methods map[string]int) {
	t.Helper()
	checks := slices.Clone(spec.Checks)
	for index := range checks {
		if checks[index].Name != "structure_correct" {
			continue
		}
		var expected map[string]any
		if err := json.Unmarshal(checks[index].Expected, &expected); err != nil {
			t.Fatal(err)
		}
		expected["folder_count"] = 999
		mutated, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		checks[index].Expected = mutated
		results, err := evaluateRunChecks(
			checks, final, "", 3, 0, 0, 1,
			map[string]int{"atl:jira": 1}, 0, 0, methods, true, []int{0, 0, 0},
		)
		if err != nil {
			t.Fatal(err)
		}
		if results["structure_correct"] {
			t.Fatal("mutated nested folder count passed structure_correct")
		}
		return
	}
	t.Fatal("structure_correct check not found")
}
