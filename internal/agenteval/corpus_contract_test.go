package agenteval

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestRepositoryStructureAndTableV2ProviderParityIsZeroWrite(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for _, directory := range []string{
		"jira-structure-deep-values",
		"jira-structure-subtree-export",
		"confluence-table-analytics",
		"confluence-table-summary",
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
			if scenario.Budgets.MaxRemoteWrites != 0 {
				t.Fatalf("v2 scenario permits %d remote writes", scenario.Budgets.MaxRemoteWrites)
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
