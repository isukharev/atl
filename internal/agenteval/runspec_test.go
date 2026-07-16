package agenteval

import (
	"encoding/json"
	"strings"
	"testing"
)

func validRunSpec() RunSpec {
	return RunSpec{
		SchemaVersion: RunSpecSchemaVersion, ScenarioFile: "scenario.json",
		Provider: "codex", Variant: "baseline", Model: "gpt-test-1",
		PromptFile: "prompt.md", ResponseSchemaFile: "response.json",
		QualitativeRubricFile: "rubric.json",
		WorkspaceTemplate:     "workspace", FixtureFile: "fixture.json",
		Repetitions: 3, TimeoutSeconds: 300, MaxEstimatedCostMicroUSD: 10_000_000,
		Pricing:            Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000},
		AllowedTools:       []string{"Bash(atl *)"},
		AllowedATLCommands: []string{"atl jira issue fields", "atl jira epic digest"},
		Checks: []RunCheck{
			{Name: "answer_correct", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`"ok"`)},
			{Name: "used_atl", Kind: "atl_invocations_min", Minimum: 1},
		},
	}
}

func TestRunSpecFailsClosedOnCostPathsAndOracle(t *testing.T) {
	for name, mutate := range map[string]func(*RunSpec){
		"cost":    func(spec *RunSpec) { spec.MaxEstimatedCostMicroUSD = maxRunCostMicroUSD + 1 },
		"path":    func(spec *RunSpec) { spec.PromptFile = "../private" },
		"oracle":  func(spec *RunSpec) { spec.Checks[0].Expected = json.RawMessage(`no`) },
		"pricing": func(spec *RunSpec) { spec.Pricing = Pricing{} },
	} {
		t.Run(name, func(t *testing.T) {
			spec := validRunSpec()
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("invalid run spec passed")
			}
		})
	}
}

func TestRunSpecSeparatesCLIAndMCPAllowlists(t *testing.T) {
	mcpSpec := validRunSpec()
	mcpSpec.ToolTransport = "mcp"
	mcpSpec.AllowedTools = nil
	mcpSpec.AllowedATLCommands = nil
	mcpSpec.AllowedMCPTools = []string{"jira_fields", "jira_epic_digest"}
	if err := mcpSpec.Validate(); err != nil {
		t.Fatal(err)
	}
	claudeSpec := mcpSpec
	claudeSpec.Provider = "claude-code"
	claudeSpec.Model = "claude-test-1"
	claudeSpec.Pricing = Pricing{}
	if err := claudeSpec.Validate(); err != nil {
		t.Fatalf("Claude MCP run spec: %v", err)
	}
	for name, mutate := range map[string]func(*RunSpec){
		"shell_tools":   func(spec *RunSpec) { spec.AllowedTools = []string{"Bash"} },
		"cli_commands":  func(spec *RunSpec) { spec.AllowedATLCommands = []string{"atl jira fields"} },
		"missing_mcp":   func(spec *RunSpec) { spec.AllowedMCPTools = nil },
		"duplicate_mcp": func(spec *RunSpec) { spec.AllowedMCPTools = []string{"jira_fields", "jira_fields"} },
	} {
		t.Run(name, func(t *testing.T) {
			spec := mcpSpec
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("invalid MCP run spec passed")
			}
		})
	}
}

func TestPrivateLiveRunSpecFailsClosed(t *testing.T) {
	scenario := validScenario()
	scenario.DataClass = "private-local"
	scenario.RequiredChecks = []string{"answer_correct", "used_atl", "http_observed", "guard_clean", "no_delegation", "atl_succeeded"}
	scenario.Budgets.MaxRemoteWrites = 0
	scenario.Budgets.MaxDelegations = 0
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
	scenario.Budgets.AllowedHTTPMethods = []string{"GET", "HEAD"}
	spec := validRunSpec()
	spec.BackendMode = BackendModePrivateLive
	spec.FixtureFile = ""
	spec.Repetitions = 1
	spec.ToolTransport = "mcp"
	spec.AllowedTools = nil
	spec.AllowedATLCommands = nil
	spec.AllowedMCPTools = []string{"jira_epic_digest"}
	spec.MaxEstimatedCostMicroUSD = scenario.Budgets.MaxEstimatedCostMicroUSD
	spec.Checks = append(spec.Checks,
		RunCheck{Name: "http_observed", Kind: "http_methods_observed"},
		RunCheck{Name: "guard_clean", Kind: "guard_no_denials"},
		RunCheck{Name: "no_delegation", Kind: "delegations_none"},
		RunCheck{Name: "atl_succeeded", Kind: "atl_all_succeeded"},
	)
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := spec.ValidateAgainstScenario(scenario); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RunSpec, *Scenario){
		"fixture":     func(s *RunSpec, _ *Scenario) { s.FixtureFile = "fixture.json" },
		"repetitions": func(s *RunSpec, _ *Scenario) { s.Repetitions = 2 },
		"prefix cli policy": func(s *RunSpec, _ *Scenario) {
			s.ToolTransport = "cli"
			s.AllowedTools = []string{"Bash(atl *)"}
			s.AllowedATLCommands = []string{"atl jira fields"}
			s.AllowedMCPTools = nil
		},
		"public data":  func(_ *RunSpec, sc *Scenario) { sc.DataClass = "synthetic" },
		"write budget": func(_ *RunSpec, sc *Scenario) { sc.Budgets.MaxRemoteWrites = 1 },
		"write method": func(_ *RunSpec, sc *Scenario) { sc.Budgets.AllowedHTTPMethods = []string{"GET", "POST"} },
		"delegation":   func(_ *RunSpec, sc *Scenario) { sc.Budgets.MaxDelegations = 1 },
		"mock oracle": func(s *RunSpec, _ *Scenario) {
			s.Checks = append(s.Checks, RunCheck{Name: "mock", Kind: "mock_no_unexpected"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := spec
			candidate.Checks = append([]RunCheck(nil), spec.Checks...)
			candidateScenario := scenario
			mutate(&candidate, &candidateScenario)
			if candidate.Validate() == nil && candidate.ValidateAgainstScenario(candidateScenario) == nil {
				t.Fatal("unsafe private-live spec passed")
			}
		})
	}
}

func TestPrivateLiveCLIRunSpecRequiresStructuredArgumentPolicy(t *testing.T) {
	scenario := validScenario()
	scenario.DataClass = "private-local"
	scenario.RequiredChecks = []string{"answer_correct", "used_atl", "http_observed", "guard_clean", "no_delegation", "atl_succeeded"}
	scenario.Budgets.MaxRemoteWrites = 0
	scenario.Budgets.MaxDelegations = 0
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
	scenario.Budgets.AllowedHTTPMethods = []string{"GET", "HEAD"}
	spec := validRunSpec()
	spec.BackendMode = BackendModePrivateLive
	spec.FixtureFile = ""
	spec.Repetitions = 1
	spec.ToolTransport = "cli"
	spec.AllowedTools = []string{"Bash(atl *)", "Read"}
	spec.AllowedATLCommands = nil
	spec.AllowedCLICommands = validCLICommandPolicy().Rules
	spec.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{
		"jira": {{Name: "jira_api", PathPrefix: "/rest/api/2"}},
	}
	spec.GatewayMaxResponseBytes = 1 << 20
	spec.GatewayMaxTotalBytes = 4 << 20
	spec.MaxEstimatedCostMicroUSD = scenario.Budgets.MaxEstimatedCostMicroUSD
	spec.Checks = append(spec.Checks,
		RunCheck{Name: "http_observed", Kind: "http_methods_observed"},
		RunCheck{Name: "guard_clean", Kind: "guard_no_denials"},
		RunCheck{Name: "no_delegation", Kind: "delegations_none"},
		RunCheck{Name: "atl_succeeded", Kind: "atl_all_succeeded"},
	)
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := spec.ValidateAgainstScenario(scenario); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*RunSpec){
		"missing policy": func(s *RunSpec) { s.AllowedCLICommands = nil },
		"legacy prefix":  func(s *RunSpec) { s.AllowedATLCommands = []string{"atl jira epic digest"} },
		"mcp tool":       func(s *RunSpec) { s.AllowedMCPTools = []string{"jira_epic_digest"} },
		"agent tool":     func(s *RunSpec) { s.AllowedTools = append(s.AllowedTools, "Agent") },
		"bad target":     func(s *RunSpec) { s.AllowedCLICommands[0].Positionals[0].Values[0] = "PROJ-1\nnext" },
		"no routes":      func(s *RunSpec) { s.AllowedGatewayRoutes = nil },
		"root route":     func(s *RunSpec) { s.AllowedGatewayRoutes["jira"][0].PathPrefix = "/" },
		"wide response":  func(s *RunSpec) { s.GatewayMaxResponseBytes = 65 << 20 },
		"unbounded total": func(s *RunSpec) {
			s.GatewayMaxTotalBytes = 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := spec
			candidate.AllowedCLICommands = append([]CLICommandRule(nil), spec.AllowedCLICommands...)
			candidate.AllowedCLICommands[0].Positionals = append([]CLIArgumentRule(nil), spec.AllowedCLICommands[0].Positionals...)
			candidate.AllowedCLICommands[0].Positionals[0].Values = append([]string(nil), spec.AllowedCLICommands[0].Positionals[0].Values...)
			candidate.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": append([]LiveGatewayRoute(nil), spec.AllowedGatewayRoutes["jira"]...)}
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("unsafe private-live CLI spec passed")
			}
		})
	}
}

func TestRunSpecRequiresScenarioOracleAndCostBoundary(t *testing.T) {
	scenario := validScenario()
	scenario.RequiredChecks = []string{"answer_correct", "used_atl"}
	scenario.Budgets.MaxEstimatedCostMicroUSD = 2_000_000
	spec := validRunSpec()
	if err := spec.ValidateAgainstScenario(scenario); err == nil || !strings.Contains(err.Error(), "cost cap") {
		t.Fatalf("err=%v", err)
	}
	spec.MaxEstimatedCostMicroUSD = 6_000_000
	spec.Checks = spec.Checks[:1]
	if err := spec.ValidateAgainstScenario(scenario); err == nil || !strings.Contains(err.Error(), "used_atl") {
		t.Fatalf("err=%v", err)
	}
}

func TestEvaluateRunChecksUsesStructuredValuesOnly(t *testing.T) {
	checks := []RunCheck{
		{Name: "equals", Kind: "json_equals", Pointer: "/nested/value", Expected: json.RawMessage(`7`)},
		{Name: "present", Kind: "json_present", Pointer: "/nested"},
		{Name: "used", Kind: "atl_invocations_min", Minimum: 2},
		{Name: "bounded", Kind: "atl_invocations_max", Maximum: 2},
		{Name: "routes", Kind: "mock_no_unexpected"},
		{Name: "delegated", Kind: "delegations_min", Minimum: 1},
		{Name: "guarded", Kind: "guard_no_denials"},
	}
	result, err := evaluateRunChecks(checks, []byte(`{"nested":{"value":7}}`), 2, 0, 0, 1, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	for name, passed := range result {
		if !passed {
			t.Errorf("check %s failed", name)
		}
	}
}

func TestRunSpecificChecksAreMandatory(t *testing.T) {
	result := Result{Status: "pass", Checks: map[string]bool{"shared": false, "variant": false}}
	checks := []RunCheck{{Name: "shared"}, {Name: "variant"}}
	addRunCheckViolations(&result, checks, []string{"shared"})
	if result.Status != "fail" || len(result.Violations) != 1 || result.Violations[0].Subject != "variant" {
		t.Fatalf("result=%+v", result)
	}
}

func TestCleanGuardRequirementIsDetected(t *testing.T) {
	if requiresCleanGuard([]RunCheck{{Kind: "json_present"}}) {
		t.Fatal("unrelated oracle enabled guard cancellation")
	}
	if !requiresCleanGuard([]RunCheck{{Kind: "guard_no_denials"}}) {
		t.Fatal("guard oracle did not enable cancellation")
	}
}
