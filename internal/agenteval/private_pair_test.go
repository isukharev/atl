package agenteval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePrivateRunPairRequiresIdenticalTaskContract(t *testing.T) {
	directory, cliPath, mcpPath, _, _ := writePrivatePairFixture(t)
	pair, err := ValidatePrivateRunPair(cliPath, mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !pair.Comparable || pair.Provider != "codex" || len(pair.Transports) != 2 || pair.Transports[0] != "cli" || pair.Transports[1] != "mcp" {
		t.Fatalf("pair=%+v", pair)
	}
	encoded, err := json.Marshal(pair)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private.pair") || strings.Contains(string(encoded), directory) {
		t.Fatalf("pair summary retained private identity: %s", encoded)
	}
}

func TestValidatePrivateRunPairRejectsTransportAndContractDrift(t *testing.T) {
	for name, mutate := range map[string]func(string, *RunSpec, *RunSpec){
		"same transport": func(_ string, cli, mcp *RunSpec) {
			*mcp = *cli
			mcp.Variant = "second-cli"
		},
		"prompt": func(directory string, _ *RunSpec, mcp *RunSpec) {
			writeTestFile(t, filepath.Join(directory, "other-prompt.md"), "different private task\n", 0o600)
			mcp.PromptFile = "other-prompt.md"
		},
		"provider": func(_ string, _ *RunSpec, mcp *RunSpec) {
			mcp.Provider = "claude-code"
			mcp.Pricing = Pricing{}
		},
		"checks": func(_ string, _ *RunSpec, mcp *RunSpec) {
			mcp.Checks[0].Expected = json.RawMessage(`false`)
		},
		"explicit cli activation": func(_ string, cli, _ *RunSpec) {
			cli.SkillActivation = SkillActivationExplicit
			cli.DataCapabilities = []string{"jira.fields"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory, cliPath, mcpPath, cli, mcp := writePrivatePairFixture(t)
			mutate(directory, &cli, &mcp)
			writeJSONTestFile(t, cliPath, cli)
			writeJSONTestFile(t, mcpPath, mcp)
			if _, err := ValidatePrivateRunPair(cliPath, mcpPath); err == nil {
				t.Fatal("incompatible private pair passed")
			}
		})
	}
}

func TestValidatePrivateRunComparisonSetAcceptsThreeUniqueSurfacesAndMechanicalChecks(t *testing.T) {
	directory, cliPath, mcpPath, _, mcp := writePrivatePairFixture(t)
	external := mcp
	external.Variant = "external-read-only-mcp"
	external.Surface = SurfaceExternalMCP
	external.Checks = append([]RunCheck(nil), mcp.Checks...)
	for index := range external.Checks {
		switch external.Checks[index].Kind {
		case "atl_invocations_min":
			external.Checks[index].Kind = "interface_invocations_min"
		case "atl_all_succeeded":
			external.Checks[index].Kind = "interface_all_succeeded"
		}
	}
	external.Checks = append(external.Checks, RunCheck{Name: "bounded_interface", Kind: "interface_invocations_max", Maximum: 4})
	externalPath := filepath.Join(directory, "run.external.json")
	writeJSONTestFile(t, externalPath, external)

	set, err := ValidatePrivateRunComparisonSet(cliPath, mcpPath, externalPath)
	if err != nil {
		t.Fatal(err)
	}
	wantSurfaces := []string{SurfaceATLMCP, SurfaceCLISkill, SurfaceExternalMCP}
	if !set.Comparable || set.Category != BenchmarkCategoryRouteFixed || set.Provider != "codex" || !equalPrivateComparisonJSON(set.Surfaces, wantSurfaces) {
		t.Fatalf("set=%+v", set)
	}
	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private.pair") || strings.Contains(string(encoded), directory) {
		t.Fatalf("comparison summary retained private identity: %s", encoded)
	}
}

func TestValidatePrivateRunComparisonSetRejectsOpaqueExternalRequiredMetric(t *testing.T) {
	directory, cliPath, mcpPath, _, mcp := writePrivatePairFixture(t)
	var scenario Scenario
	data, err := os.ReadFile(filepath.Join(directory, "scenario.json"))
	if err != nil || json.Unmarshal(data, &scenario) != nil {
		t.Fatal(err)
	}
	scenario.RequiredMetrics = append(scenario.RequiredMetrics, "backend_requests")
	writeJSONTestFile(t, filepath.Join(directory, "scenario.json"), scenario)
	external := mcp
	external.Variant = "external-read-only-mcp"
	external.Surface = SurfaceExternalMCP
	externalPath := filepath.Join(directory, "run.external.json")
	writeJSONTestFile(t, externalPath, external)
	if _, err := ValidatePrivateRunComparisonSet(cliPath, mcpPath, externalPath); err == nil || !strings.Contains(err.Error(), "opaque backend metrics") {
		t.Fatalf("opaque external metric passed comparison validation: %v", err)
	}
}

func TestValidatePairRetainsExactMechanicalChecksWhileComparisonSetAllowsThem(t *testing.T) {
	_, cliPath, mcpPath, _, mcp := writePrivatePairFixture(t)
	mcp.Checks = append(mcp.Checks, RunCheck{Name: "bounded_interface", Kind: "interface_invocations_max", Maximum: 4})
	writeJSONTestFile(t, mcpPath, mcp)
	if _, err := ValidatePrivateRunComparisonSet(cliPath, mcpPath); err != nil {
		t.Fatalf("comparison set rejected mechanical difference: %v", err)
	}
	if _, err := ValidatePrivateRunPair(cliPath, mcpPath); err == nil || !strings.Contains(err.Error(), "run checks") {
		t.Fatalf("validate-pair did not preserve exact checks: %v", err)
	}
}

func TestComparisonSetRejectsMechanicalSubstitutionForRequiredSemanticCheck(t *testing.T) {
	directory, cliPath, mcpPath, _, mcp := writePrivatePairFixture(t)
	external := mcp
	external.Variant = "external-read-only-mcp"
	external.Surface = SurfaceExternalMCP
	external.Checks = append([]RunCheck(nil), mcp.Checks...)
	external.Checks[0] = RunCheck{Name: "answer", Kind: "guard_no_denials"}
	external.Checks = append(external.Checks, RunCheck{Name: "irrelevant_answer", Kind: "json_equals", Pointer: "/complete", Expected: json.RawMessage(`true`)})
	externalPath := filepath.Join(directory, "run.external.json")
	writeJSONTestFile(t, externalPath, external)
	if _, err := ValidatePrivateRunComparisonSet(cliPath, mcpPath, externalPath); err == nil || !strings.Contains(err.Error(), "required semantic") {
		t.Fatalf("semantic substitution passed: %v", err)
	}
}

func TestValidatePrivateRunComparisonSetRejectsSemanticAndIdentityDrift(t *testing.T) {
	for name, mutate := range map[string]func(string, *RunSpec){
		"semantic check": func(_ string, external *RunSpec) {
			external.Checks[0].Expected = json.RawMessage(`false`)
		},
		"surface": func(_ string, external *RunSpec) {
			external.Surface = SurfaceATLMCP
		},
		"category": func(directory string, external *RunSpec) {
			scenarioPath := filepath.Join(directory, "other-scenario.json")
			scenario := validScenario()
			scenario.ID = "private.pair"
			scenario.DataClass = "private-local"
			scenario.Category = BenchmarkCategoryNeutralCommon
			scenario.RequiredChecks = []string{"answer", "atl_succeeded", "guard_clean", "http_observed", "no_delegation", "used_atl"}
			scenario.Budgets.MaxRemoteWrites = 0
			scenario.Budgets.MaxDelegations = 0
			scenario.Budgets.MaxBackendRequests = 4
			scenario.Budgets.MaxATLInvocations = 4
			scenario.Budgets.AllowedHTTPMethods = []string{"GET", "HEAD"}
			scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
			writeJSONTestFile(t, scenarioPath, scenario)
			external.ScenarioFile = "other-scenario.json"
			external.Category = BenchmarkCategoryNeutralCommon
		},
		"prompt": func(directory string, external *RunSpec) {
			writeTestFile(t, filepath.Join(directory, "other-prompt.md"), "A transport-specific task.\n", 0o600)
			external.PromptFile = "other-prompt.md"
		},
	} {
		t.Run(name, func(t *testing.T) {
			directory, cliPath, mcpPath, _, mcp := writePrivatePairFixture(t)
			external := mcp
			external.Variant = "external-read-only-mcp"
			external.Surface = SurfaceExternalMCP
			external.Checks = append([]RunCheck(nil), mcp.Checks...)
			mutate(directory, &external)
			externalPath := filepath.Join(directory, "run.external.json")
			writeJSONTestFile(t, externalPath, external)
			if _, err := ValidatePrivateRunComparisonSet(cliPath, mcpPath, externalPath); err == nil {
				t.Fatal("incompatible private comparison set passed")
			}
		})
	}
}

func TestValidatePrivateRunComparisonSetRequiresBoundedCardinalityAndSemanticCheck(t *testing.T) {
	_, cliPath, mcpPath, cli, mcp := writePrivatePairFixture(t)
	if _, err := ValidatePrivateRunComparisonSet(cliPath); err == nil {
		t.Fatal("one-member comparison set passed")
	}
	if _, err := ValidatePrivateRunComparisonSet(cliPath, mcpPath, cliPath, mcpPath); err == nil {
		t.Fatal("four-member comparison set passed")
	}
	cli.Checks[0] = RunCheck{Name: "answer", Kind: "guard_no_denials"}
	mcp.Checks[0] = RunCheck{Name: "answer", Kind: "guard_no_denials"}
	writeJSONTestFile(t, cliPath, cli)
	writeJSONTestFile(t, mcpPath, mcp)
	if _, err := ValidatePrivateRunComparisonSet(cliPath, mcpPath); err == nil || !strings.Contains(err.Error(), "semantic") {
		t.Fatalf("err=%v", err)
	}
}

func writePrivatePairFixture(t *testing.T) (string, string, string, RunSpec, RunSpec) {
	t.Helper()
	directory := t.TempDir()
	workspace := filepath.Join(directory, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(workspace, "README.md"), "reviewed private evidence\n", 0o600)
	scenario := validScenario()
	scenario.ID = "private.pair"
	scenario.DataClass = "private-local"
	scenario.RequiredChecks = []string{"answer", "atl_succeeded", "guard_clean", "http_observed", "no_delegation", "used_atl"}
	scenario.RequiredSemanticChecks = []string{"answer"}
	scenario.Budgets.MaxRemoteWrites = 0
	scenario.Budgets.MaxDelegations = 0
	scenario.Budgets.MaxBackendRequests = 4
	scenario.Budgets.MaxATLInvocations = 4
	scenario.Budgets.AllowedHTTPMethods = []string{"GET", "HEAD"}
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
	filteredMetrics := scenario.RequiredMetrics[:0]
	for _, metric := range scenario.RequiredMetrics {
		if !externalMCPMetricIsOpaque(metric) {
			filteredMetrics = append(filteredMetrics, metric)
		}
	}
	scenario.RequiredMetrics = filteredMetrics
	writeJSONTestFile(t, filepath.Join(directory, "scenario.json"), scenario)
	writeTestFile(t, filepath.Join(directory, "prompt.md"), "Use the reviewed atl interface.\n", 0o600)
	writeTestFile(t, filepath.Join(directory, "response.json"), `{"type":"object","properties":{"complete":{"type":"boolean"}},"required":["complete"],"additionalProperties":false}`, 0o600)
	rubric := Rubric{SchemaVersion: 1, ID: "private-pair", ScenarioID: scenario.ID, MinimumScoreBPS: 5000, Criteria: []RubricCriterion{{ID: "grounded", Description: "Grounded.", Maximum: 4, Minimum: 2, Weight: 1}}, AllowedFindingIDs: []string{"missing"}}
	writeJSONTestFile(t, filepath.Join(directory, "rubric.json"), rubric)
	checks := []RunCheck{
		{Name: "answer", Kind: "json_equals", Pointer: "/complete", Expected: json.RawMessage(`true`)},
		{Name: "atl_succeeded", Kind: "atl_all_succeeded"},
		{Name: "guard_clean", Kind: "guard_no_denials"},
		{Name: "http_observed", Kind: "http_methods_observed"},
		{Name: "no_delegation", Kind: "delegations_none"},
		{Name: "used_atl", Kind: "atl_invocations_min", Minimum: 1},
	}
	base := RunSpec{SchemaVersion: RunSpecSchemaVersion, BackendMode: BackendModePrivateLive, ScenarioFile: "scenario.json", Provider: "codex", Model: "test-model", PromptFile: "prompt.md", ResponseSchemaFile: "response.json", QualitativeRubricFile: "rubric.json", WorkspaceTemplate: "workspace", Repetitions: 1, TimeoutSeconds: 60, MaxEstimatedCostMicroUSD: 10_000_000, Pricing: Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000}, Checks: checks}
	cli := base
	cli.Variant = "cli-skill"
	cli.ToolTransport = "cli"
	cli.SkillActivation = SkillActivationImplicit
	cli.AllowedTools = []string{"Bash(atl *)", "Read", "Skill"}
	cli.AllowedCLICommands = []CLICommandRule{{Name: "jira_fields", Command: []string{"jira", "fields"}, MaxInvocations: 1}}
	cli.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": {{Name: "jira_api", PathPrefix: "/rest/api/2"}}}
	cli.GatewayMaxResponseBytes = 1 << 20
	cli.GatewayMaxTotalBytes = 2 << 20
	mcp := base
	mcp.Checks = append([]RunCheck(nil), checks...)
	mcp.Variant = "typed-mcp"
	mcp.ToolTransport = "mcp"
	mcp.AllowedMCPTools = []string{"jira_fields"}
	cliPath := filepath.Join(directory, "run.cli.json")
	mcpPath := filepath.Join(directory, "run.mcp.json")
	writeJSONTestFile(t, cliPath, cli)
	writeJSONTestFile(t, mcpPath, mcp)
	return directory, cliPath, mcpPath, cli, mcp
}
