package agenteval

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		"workspace oracle": func(s *RunSpec, _ *Scenario) {
			s.Checks = append(s.Checks, RunCheck{Name: "workspace", Kind: "json_equals_workspace_json", Pointer: "/answer", Expected: json.RawMessage(`{"path":"plan.json","pointer":"/answer"}`)})
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

func TestRunSpecSyntheticWritesRequireExplicitSyntheticBudget(t *testing.T) {
	scenario := validScenario()
	scenario.Budgets.MaxRemoteWrites = 1
	scenario.Budgets.AllowedHTTPMethods = []string{"GET", "PUT"}
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
	scenario.RequiredChecks = []string{"answer_correct", "used_atl", "guard_clean", "methods", "mock_clean"}
	spec := validRunSpec()
	spec.Provider = "claude-code"
	spec.Pricing = Pricing{}
	spec.AllowSyntheticWrites = true
	spec.Checks = append(spec.Checks,
		RunCheck{Name: "guard_clean", Kind: "guard_no_denials"},
		RunCheck{Name: "methods", Kind: "http_methods_equal", Expected: json.RawMessage(`{"GET":2,"PUT":1}`)},
		RunCheck{Name: "mock_clean", Kind: "mock_no_unexpected"},
	)
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := spec.ValidateAgainstScenario(scenario); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RunSpec, *Scenario){
		"private": func(s *RunSpec, _ *Scenario) {
			s.BackendMode = BackendModePrivateLive
			s.FixtureFile = ""
			s.Repetitions = 1
		},
		"mcp": func(s *RunSpec, _ *Scenario) { s.ToolTransport = "mcp" },
		"codex": func(s *RunSpec, _ *Scenario) {
			s.Provider = "codex"
			s.Pricing = Pricing{InputMicroUSDPerMillionTokens: 1, OutputMicroUSDPerMillionTokens: 1}
		},
		"zero budget": func(_ *RunSpec, sc *Scenario) { sc.Budgets.MaxRemoteWrites = 0 },
		"read methods": func(_ *RunSpec, sc *Scenario) {
			sc.Budgets.AllowedHTTPMethods = []string{"GET"}
		},
		"no guard oracle": func(s *RunSpec, _ *Scenario) {
			s.Checks = s.Checks[:len(s.Checks)-3]
		},
		"no exact methods oracle": func(s *RunSpec, _ *Scenario) {
			s.Checks = append(s.Checks[:len(s.Checks)-2], s.Checks[len(s.Checks)-1])
		},
		"no mock oracle": func(s *RunSpec, _ *Scenario) {
			s.Checks = s.Checks[:len(s.Checks)-1]
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := spec
			candidateScenario := scenario
			mutate(&candidate, &candidateScenario)
			if candidate.Validate() == nil && candidate.ValidateAgainstScenario(candidateScenario) == nil {
				t.Fatal("unsafe synthetic write spec passed")
			}
		})
	}
}

func TestEvaluateRunChecksUsesStructuredValuesOnly(t *testing.T) {
	checks := []RunCheck{
		{Name: "equals", Kind: "json_equals", Pointer: "/nested/value", Expected: json.RawMessage(`7`)},
		{Name: "present", Kind: "json_present", Pointer: "/nested"},
		{Name: "used", Kind: "atl_invocations_min", Minimum: 2},
		{Name: "used_interface", Kind: "interface_invocations_min", Minimum: 2},
		{Name: "used_skill", Kind: "skill_invocations_min", Minimum: 1},
		{Name: "used_jira_skill", Kind: "skill_invocations_min", Minimum: 1, Expected: json.RawMessage(`"atl:jira"`)},
		{Name: "bounded", Kind: "atl_invocations_max", Maximum: 2},
		{Name: "bounded_interface", Kind: "interface_invocations_max", Maximum: 2},
		{Name: "interface_failures", Kind: "interface_failures_equals", Expected: json.RawMessage(`1`)},
		{Name: "expected_fail_closed", Kind: "atl_failures_equals", Expected: json.RawMessage(`1`)},
		{Name: "routes", Kind: "mock_no_unexpected"},
		{Name: "delegated", Kind: "delegations_min", Minimum: 1},
		{Name: "guarded", Kind: "guard_no_denials"},
		{Name: "methods", Kind: "http_methods_equal", Expected: json.RawMessage(`{"GET":2,"PUT":1}`)},
	}
	result, err := evaluateRunChecks(checks, []byte(`{"nested":{"value":7}}`), "", 2, 1, 0, 1, map[string]int{"atl:jira": 1}, 1, 0, map[string]int{"GET": 2, "PUT": 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	for name, passed := range result {
		if !passed {
			t.Errorf("check %s failed", name)
		}
	}
	over, err := evaluateRunChecks(checks, []byte(`{"nested":{"value":7}}`), "", 3, 0, 0, 1, map[string]int{"atl:confluence": 1}, 1, 0, map[string]int{"GET": 3}, true)
	if err != nil {
		t.Fatal(err)
	}
	if over["bounded"] {
		t.Fatal("atl_invocations_max accepted an over-budget run")
	}
	if over["bounded_interface"] {
		t.Fatal("interface_invocations_max accepted an over-budget run")
	}
	if over["interface_failures"] {
		t.Fatal("interface_failures_equals accepted the wrong failure count")
	}
	if over["expected_fail_closed"] {
		t.Fatal("atl_failures_equals accepted the wrong failure count")
	}
	if !over["used_skill"] {
		t.Fatal("skill_invocations_min rejected an unqualified Skill invocation")
	}
	if over["used_jira_skill"] {
		t.Fatal("skill_invocations_min accepted the wrong named skill")
	}
	if over["methods"] {
		t.Fatal("http_methods_equal accepted a different method map")
	}
	success, err := evaluateRunChecks([]RunCheck{{Name: "interface_succeeded", Kind: "interface_all_succeeded"}}, []byte(`{}`), "", 1, 0, 0, 0, nil, 0, 0, nil, false)
	if err != nil || !success["interface_succeeded"] {
		t.Fatalf("interface success alias result=%v err=%v", success, err)
	}
}

func TestRunSpecCategoryAndSurfaceDefaultsAndCompatibility(t *testing.T) {
	legacy := validRunSpec()
	if legacy.EffectiveCategory() != BenchmarkCategoryRouteFixed || legacy.EffectiveSurface() != SurfaceCLISkill {
		t.Fatalf("legacy identity category=%q surface=%q", legacy.EffectiveCategory(), legacy.EffectiveSurface())
	}
	legacy.ToolTransport = "mcp"
	legacy.AllowedTools = nil
	legacy.AllowedATLCommands = nil
	legacy.AllowedMCPTools = []string{"jira_epic_digest"}
	if legacy.EffectiveSurface() != SurfaceATLMCP {
		t.Fatalf("mcp surface=%q", legacy.EffectiveSurface())
	}
	if err := legacy.Validate(); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*RunSpec){
		"unknown category": func(spec *RunSpec) { spec.Category = "unknown" },
		"unknown surface":  func(spec *RunSpec) { spec.Surface = "unknown" },
		"cli as mcp": func(spec *RunSpec) {
			spec.Surface = SurfaceCLISkill
			spec.ToolTransport = "mcp"
			spec.AllowedTools = nil
			spec.AllowedATLCommands = nil
			spec.AllowedMCPTools = []string{"jira_epic_digest"}
		},
		"mcp as cli": func(spec *RunSpec) { spec.Surface = SurfaceATLMCP },
	} {
		t.Run(name, func(t *testing.T) {
			spec := validRunSpec()
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("invalid identity passed")
			}
		})
	}
}

func TestRunSpecRequiresMatchingScenarioCategory(t *testing.T) {
	spec := validRunSpec()
	spec.Category = BenchmarkCategoryNeutralCommon
	spec.DataCapabilities = []string{"jira.epic.digest", "jira.issue.fields"}
	if err := spec.ValidateAgainstScenario(validScenario()); err == nil || !strings.Contains(err.Error(), "category") {
		t.Fatalf("err=%v", err)
	}
}

func TestNeutralCommonRunSpecBindsDeclaredDataCapabilities(t *testing.T) {
	spec := validRunSpec()
	spec.Category = BenchmarkCategoryNeutralCommon
	spec.DataCapabilities = []string{"jira.epic.digest", "jira.issue.fields"}
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}

	bad := spec
	bad.DataCapabilities = []string{"jira.issue.fields"}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("missing declared capability passed: %v", err)
	}
	bad = spec
	bad.AllowedATLCommands = append([]string(nil), spec.AllowedATLCommands...)
	bad.AllowedATLCommands = append(bad.AllowedATLCommands, "atl conf page view")
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown or richer route passed: %v", err)
	}
}

func TestNeutralPrivateCLIDataCapabilityUsesReviewedSelectorFlags(t *testing.T) {
	spec := validRunSpec()
	spec.BackendMode = BackendModePrivateLive
	spec.Category = BenchmarkCategoryNeutralCommon
	spec.Surface = SurfaceCLISkill
	spec.FixtureFile = ""
	spec.Repetitions = 1
	spec.ToolTransport = "cli"
	spec.AllowedTools = []string{"Bash(atl *)", "Read", "Skill"}
	spec.AllowedATLCommands = nil
	spec.AllowedCLICommands = []CLICommandRule{{
		Name: "batch", Command: []string{"jira", "export"}, MaxInvocations: 1,
		Flags: []CLIFlagRule{{Name: "--keys", Values: []string{"PROJ-1"}, Required: true}},
	}}
	spec.DataCapabilities = []string{"jira.issue.list"}
	if err := validateRunDataCapabilities(spec); err != nil {
		t.Fatal(err)
	}

	spec.AllowedCLICommands[0].Flags = []CLIFlagRule{
		{Name: "--keys", Values: []string{"PROJ-1"}},
		{Name: "--jql", Values: []string{"project = PROJ"}},
	}
	if err := validateRunDataCapabilities(spec); err == nil || !strings.Contains(err.Error(), "unclassified") {
		t.Fatalf("optional selector or alternate query passed as a bounded issue list: %v", err)
	}
}

func TestNeutralCommonRequiresGenericMetricsSemanticChecksAndAliases(t *testing.T) {
	scenario := validScenario()
	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.RequiredChecks = []string{"answer_correct", "used_atl"}
	scenario.RequiredSemanticChecks = []string{"answer_correct"}
	scenario.RequiredMetrics = []string{"interface_invocations", "backend_requests"}
	scenario.Budgets.MaxATLInvocations = 0
	scenario.Budgets.MaxInterfaceInvocations = 2
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
	spec := validRunSpec()
	spec.Category = BenchmarkCategoryNeutralCommon
	spec.DataCapabilities = []string{"jira.epic.digest", "jira.issue.fields"}
	for index := range spec.Checks {
		if spec.Checks[index].Kind == "atl_invocations_min" {
			spec.Checks[index].Kind = "interface_invocations_min"
		}
	}
	if err := spec.ValidateAgainstScenario(scenario); err != nil {
		t.Fatal(err)
	}

	bad := spec
	bad.Checks = append([]RunCheck(nil), spec.Checks...)
	bad.Checks[0] = RunCheck{Name: "answer_correct", Kind: "guard_no_denials"}
	bad.Checks = append(bad.Checks, RunCheck{Name: "irrelevant", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`true`)})
	if err := bad.ValidateAgainstScenario(scenario); err == nil || !strings.Contains(err.Error(), "required semantic") {
		t.Fatalf("semantic substitution passed: %v", err)
	}

	bad = spec
	bad.Checks = append([]RunCheck(nil), spec.Checks...)
	bad.Checks = append(bad.Checks, RunCheck{Name: "legacy", Kind: "atl_all_succeeded"})
	if err := bad.ValidateAgainstScenario(scenario); err == nil || !strings.Contains(err.Error(), "generic") {
		t.Fatalf("legacy atl alias passed neutral contract: %v", err)
	}
}

func TestNeutralCorePromptRouteHintScannerIsConservative(t *testing.T) {
	for _, prompt := range []string{
		"Use the available ATL interface to investigate Jira and Confluence evidence.",
		"Return a concise answer with explicit completeness.",
	} {
		if err := validateNeutralCorePrompt([]byte(prompt)); err != nil {
			t.Fatalf("natural prompt %q rejected: %v", prompt, err)
		}
	}
	for _, prompt := range []string{
		"Call mcp__atl__jira_fields first.",
		"Call jira_fields first.",
		"Call confluence_search first.",
		"Run atl jira fields before answering.",
		"Use atl conf page section.",
		"Begin with atl capabilities --task jira/evidence.",
		"Invoke jira_epic_digest and confluence_page_section.",
	} {
		if err := validateNeutralCorePrompt([]byte(prompt)); err == nil {
			t.Fatalf("route hint %q passed", prompt)
		}
	}
}

func TestEvaluateRunChecksBindsContainedWorkspaceJSON(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "plan.json"), []byte(`{"proposal_hash":"abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	check := RunCheck{
		Name: "proposal", Kind: "json_equals_workspace_json", Pointer: "/proposal_hash",
		Expected: json.RawMessage(`{"path":"plan.json","pointer":"/proposal_hash"}`),
	}
	result, err := evaluateRunChecks([]RunCheck{check}, []byte(`{"proposal_hash":"abc"}`), workspace, 0, 0, 0, 0, nil, 0, 0, nil, false)
	if err != nil || !result["proposal"] {
		t.Fatalf("result=%v err=%v", result, err)
	}
	result, err = evaluateRunChecks([]RunCheck{check}, []byte(`{"proposal_hash":"wrong"}`), workspace, 0, 0, 0, 0, nil, 0, 0, nil, false)
	if err != nil || result["proposal"] {
		t.Fatalf("mismatch result=%v err=%v", result, err)
	}

	valid := validRunSpec()
	valid.Checks = append(valid.Checks, check)
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := validRunSpec()
	check.Expected = json.RawMessage(`{"path":"../plan.json","pointer":"/proposal_hash"}`)
	invalid.Checks = append(invalid.Checks, check)
	if err := invalid.Validate(); err == nil {
		t.Fatal("escaping workspace JSON oracle passed")
	}
}

func TestRunSpecValidatesExpectedHTTPMethods(t *testing.T) {
	valid := validRunSpec()
	valid.Checks = append(valid.Checks, RunCheck{Name: "methods", Kind: "http_methods_equal", Expected: json.RawMessage(`{"GET":2,"PUT":1}`)})
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`{"get":1}`), json.RawMessage(`{"GET":0}`), json.RawMessage(`{"GET":1.5}`)} {
		spec := validRunSpec()
		spec.Checks = append(spec.Checks, RunCheck{Name: "methods", Kind: "http_methods_equal", Expected: expected})
		if err := spec.Validate(); err == nil {
			t.Fatalf("invalid method oracle passed: %s", expected)
		}
	}
}

func TestRunSpecValidatesExpectedATLFailureCount(t *testing.T) {
	for name, expected := range map[string]json.RawMessage{
		"missing":  nil,
		"null":     json.RawMessage(`null`),
		"negative": json.RawMessage(`-1`),
		"fraction": json.RawMessage(`1.5`),
		"string":   json.RawMessage(`"1"`),
	} {
		t.Run(name, func(t *testing.T) {
			spec := validRunSpec()
			spec.Checks = append(spec.Checks, RunCheck{Name: "expected_failure", Kind: "atl_failures_equals", Expected: expected})
			if err := spec.Validate(); err == nil {
				t.Fatal("invalid expected ATL failure count passed")
			}
		})
	}
	spec := validRunSpec()
	spec.Checks = append(spec.Checks, RunCheck{Name: "expected_failure", Kind: "atl_failures_equals", Expected: json.RawMessage(`1`)})
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRunSpecValidatesSkillInvocationMinimum(t *testing.T) {
	valid := validRunSpec()
	valid.Provider = "claude-code"
	valid.Pricing = Pricing{}
	valid.AllowedTools = []string{"Bash(atl *)", "Skill"}
	valid.Checks = append(valid.Checks, RunCheck{Name: "skill", Kind: "skill_invocations_min", Minimum: 1, Expected: json.RawMessage(`"atl:jira"`)})
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid skill invocation minimum: %v", err)
	}

	for _, check := range []RunCheck{
		{Name: "skill", Kind: "skill_invocations_min"},
		{Name: "skill", Kind: "skill_invocations_min", Minimum: 1, Pointer: "/answer"},
		{Name: "skill", Kind: "skill_invocations_min", Minimum: 1, Expected: json.RawMessage(`1`)},
		{Name: "skill", Kind: "skill_invocations_min", Minimum: 1, Expected: json.RawMessage(`""`)},
	} {
		spec := validRunSpec()
		spec.Checks = append(spec.Checks, check)
		if err := spec.Validate(); err == nil {
			t.Fatal("invalid skill invocation minimum passed")
		}
	}

	for name, mutate := range map[string]func(*RunSpec){
		"provider": func(spec *RunSpec) {
			spec.Provider = "codex"
			spec.Pricing = Pricing{InputMicroUSDPerMillionTokens: 1, OutputMicroUSDPerMillionTokens: 1}
		},
		"tool": func(spec *RunSpec) { spec.AllowedTools = []string{"Bash(atl *)"} },
	} {
		t.Run(name, func(t *testing.T) {
			spec := validRunSpec()
			spec.Provider = "claude-code"
			spec.Pricing = Pricing{}
			spec.AllowedTools = []string{"Bash(atl *)", "Skill"}
			spec.Checks = append(spec.Checks, RunCheck{Name: "skill", Kind: "skill_invocations_min", Minimum: 1})
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("unsupported skill invocation oracle passed")
			}
		})
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
