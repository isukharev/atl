package agenteval

import (
	"encoding/json"
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
