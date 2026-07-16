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
	scenario.Budgets.MaxRemoteWrites = 0
	scenario.Budgets.MaxDelegations = 0
	scenario.Budgets.MaxBackendRequests = 4
	scenario.Budgets.MaxATLInvocations = 4
	scenario.Budgets.AllowedHTTPMethods = []string{"GET", "HEAD"}
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
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
