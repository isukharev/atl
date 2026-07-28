package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func TestRunVariantRejectsOutputPathTraversal(t *testing.T) {
	for _, value := range []string{"a/../../escaped", `a\..\escaped`, ".", ".."} {
		t.Run(value, func(t *testing.T) {
			spec := validRunSpec()
			spec.Variant = value
			if err := spec.Validate(); err == nil {
				t.Fatal("path-like run variant passed")
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

func TestRunSpecMCPServiceProfileIsClosedAndToolBound(t *testing.T) {
	base := validRunSpec()
	base.ToolTransport = "mcp"
	base.AllowedTools = nil
	base.AllowedATLCommands = nil

	for _, test := range []struct {
		name    string
		profile string
		tools   []string
	}{
		{name: "jira", profile: "jira", tools: []string{"jira_fields", "jira_mirror_snapshot"}},
		{name: "confluence", profile: "confluence", tools: []string{"confluence_page_section", "confluence_mirror_snapshot"}},
		{name: "offline", profile: "offline", tools: []string{"jira_mirror_snapshot", "confluence_mirror_snapshot"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := base
			spec.MCPServiceProfile = test.profile
			spec.AllowedMCPTools = test.tools
			if err := spec.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}

	for name, mutate := range map[string]func(*RunSpec){
		"unknown profile": func(spec *RunSpec) {
			spec.MCPServiceProfile = "all"
			spec.AllowedMCPTools = []string{"jira_fields"}
		},
		"jira with confluence tool": func(spec *RunSpec) {
			spec.MCPServiceProfile = "jira"
			spec.AllowedMCPTools = []string{"confluence_page_section"}
		},
		"offline with online tool": func(spec *RunSpec) {
			spec.MCPServiceProfile = "offline"
			spec.AllowedMCPTools = []string{"jira_fields"}
		},
		"cli": func(spec *RunSpec) {
			spec.ToolTransport = "cli"
			spec.MCPServiceProfile = "jira"
			spec.AllowedMCPTools = nil
			spec.AllowedTools = []string{"Bash(atl *)"}
			spec.AllowedATLCommands = []string{"atl jira issue fields"}
		},
		"external mcp": func(spec *RunSpec) {
			spec.BackendMode = BackendModePrivateLive
			spec.Surface = SurfaceExternalMCP
			spec.FixtureFile = ""
			spec.Repetitions = 1
			spec.MCPServiceProfile = "jira"
			spec.AllowedMCPTools = []string{"jira_fields"}
		},
		"legacy schema": func(spec *RunSpec) {
			spec.SchemaVersion = LegacyRunSpecSchemaVersion
			spec.MCPServiceProfile = "jira"
			spec.AllowedMCPTools = []string{"jira_fields"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := base
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("invalid MCP service profile passed")
			}
		})
	}
	expected := map[string][]string{
		"jira": {
			"jira_board_view", "jira_epic_digest", "jira_fields", "jira_issue_field_get", "jira_issue_history",
			"jira_issue_refs", "jira_issue_search", "jira_mirror_snapshot", "jira_structure_get", "jira_structure_view",
		},
		"confluence": {
			"confluence_attachment_list", "confluence_mirror_snapshot", "confluence_page_meta", "confluence_page_outline",
			"confluence_page_resolve", "confluence_page_section", "confluence_page_sections", "confluence_search",
			"confluence_table_extract", "confluence_table_summary",
		},
		"offline": {"confluence_mirror_snapshot", "jira_mirror_snapshot"},
	}
	for profile, want := range expected {
		allowed, ok := mcpServiceProfileTools(profile)
		got := make([]string, 0, len(allowed))
		for tool := range allowed {
			got = append(got, tool)
		}
		slices.Sort(got)
		if !ok || !slices.Equal(got, want) {
			t.Errorf("%s profile inventory=%v want=%v", profile, got, want)
		}
	}
}

func TestRunSpecMCPServiceProfileOmissionPreservesJSON(t *testing.T) {
	data, err := json.Marshal(validRunSpec())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("mcp_service_profile")) {
		t.Fatalf("omitted MCP service profile changed run spec JSON: %s", data)
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
	artifactSpec := spec
	artifactSpec.Checks = append([]RunCheck(nil), spec.Checks...)
	artifactSpec.Checks = append(artifactSpec.Checks, RunCheck{Name: "artifact", Kind: "workspace_file_sha256", Expected: json.RawMessage(
		`{"path":"generated/report.bin","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)})
	artifactScenario := scenario
	artifactScenario.RequiredChecks = append([]string(nil), scenario.RequiredChecks...)
	artifactScenario.RequiredChecks = append(artifactScenario.RequiredChecks, "artifact")
	artifactScenario.RequiredSemanticChecks = append([]string(nil), scenario.RequiredSemanticChecks...)
	artifactScenario.RequiredSemanticChecks = append(artifactScenario.RequiredSemanticChecks, "artifact")
	if err := artifactSpec.Validate(); err != nil {
		t.Fatalf("private-live artifact spec validation: %v", err)
	}
	if err := artifactSpec.ValidateAgainstScenario(artifactScenario); err != nil {
		t.Fatalf("private-live artifact scenario validation: %v", err)
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
		"write route": func(s *RunSpec, _ *Scenario) {
			s.ToolTransport = "cli"
			s.AllowedTools = []string{"Bash(atl *)"}
			s.AllowedATLCommands = nil
			s.AllowedMCPTools = nil
			s.AllowedCLICommands = []CLICommandRule{{Name: "fields", Command: []string{"jira", "fields"}, MaxInvocations: 1}}
			s.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": {{Name: "write", PathPrefix: "/rest/api/2/issue/TEST-1", Exact: true, Methods: []string{"PUT"}, MaxRequests: 1, MaxRequestBytes: 1024}}}
			s.GatewayMaxResponseBytes = 1024
			s.GatewayMaxTotalBytes = 1024
		},
		"delegation": func(_ *RunSpec, sc *Scenario) { sc.Budgets.MaxDelegations = 1 },
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
	spec.SkillActivation = SkillActivationImplicit
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

func TestRunSpecSkillActivationIsClosedToPrivateCodexCLISkill(t *testing.T) {
	valid := validRunSpec()
	valid.BackendMode = BackendModePrivateLive
	valid.FixtureFile = ""
	valid.Repetitions = 1
	valid.ToolTransport = "cli"
	valid.SkillActivation = SkillActivationImplicit
	valid.AllowedTools = []string{"Bash(atl *)", "Read", "Skill"}
	valid.AllowedATLCommands = nil
	valid.AllowedCLICommands = validCLICommandPolicy().Rules
	valid.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": {{Name: "jira_api", PathPrefix: "/rest/api/2"}}}
	valid.GatewayMaxResponseBytes = 1 << 20
	valid.GatewayMaxTotalBytes = 4 << 20
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	missing := valid
	missing.SkillActivation = ""
	if err := missing.Validate(); err == nil || !strings.Contains(err.Error(), "require skill_activation") {
		t.Fatalf("missing activation err=%v", err)
	}
	invalid := valid
	invalid.SkillActivation = "automatic"
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "implicit, explicit, developer, or combined") {
		t.Fatalf("invalid activation err=%v", err)
	}
	for name, mutate := range map[string]func(*RunSpec){
		"synthetic": func(s *RunSpec) {
			s.BackendMode, s.FixtureFile, s.Repetitions = "", "fixture.json", 3
			s.AllowedATLCommands, s.AllowedCLICommands, s.AllowedGatewayRoutes = []string{"atl jira epic digest"}, nil, nil
			s.GatewayMaxResponseBytes, s.GatewayMaxTotalBytes = 0, 0
		},
		"claude": func(s *RunSpec) { s.Provider, s.Pricing = "claude-code", Pricing{} },
		"mcp": func(s *RunSpec) {
			s.Surface, s.ToolTransport, s.AllowedTools = SurfaceATLMCP, "mcp", nil
			s.AllowedCLICommands, s.AllowedGatewayRoutes = nil, nil
			s.GatewayMaxResponseBytes, s.GatewayMaxTotalBytes = 0, 0
			s.AllowedMCPTools = []string{"jira_epic_digest"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "valid only") {
				t.Fatalf("outside activation err=%v", err)
			}
		})
	}

	for _, activation := range []string{SkillActivationExplicit, SkillActivationDeveloper, SkillActivationCombined} {
		t.Run(activation, func(t *testing.T) {
			hinted := valid
			hinted.Category = BenchmarkCategoryNeutralCommon
			hinted.SkillActivation = activation
			hinted.DataCapabilities = []string{"jira.epic.digest"}
			if err := hinted.Validate(); err != nil {
				t.Fatalf("jira hinted activation: %v", err)
			}
			hinted.DataCapabilities = []string{"confluence.page.section"}
			// The fixture's reviewed command and gateway policy are Jira-only. Test
			// service-family routing here without conflating it with the independent
			// interface-capability validator.
			if err := validateSkillActivation(hinted); err != nil {
				t.Fatalf("confluence hinted activation: %v", err)
			}

			for name, capabilities := range map[string][]string{
				"missing":          nil,
				"unknown":          {"knowledge.search"},
				"lookalike-prefix": {"jira-extra.issue"},
				"mixed":            {"confluence.page.section", "jira.epic.digest"},
			} {
				t.Run(name, func(t *testing.T) {
					candidate := hinted
					candidate.DataCapabilities = capabilities
					err := validateSkillActivation(candidate)
					if err == nil {
						t.Fatal("invalid hinted activation passed")
					}
					if name == "mixed" && !strings.Contains(err.Error(), "mixed") {
						t.Fatalf("mixed activation err=%v", err)
					}
					if name != "mixed" && !strings.Contains(err.Error(), "jira-only or confluence-only") {
						t.Fatalf("invalid family err=%v", err)
					}
				})
			}
		})
	}
}

func TestSkillActivationDescriptorModelsTwoByTwoMatrix(t *testing.T) {
	tests := []struct {
		activation             string
		promptPrefix           bool
		developerReinforcement bool
	}{
		{activation: SkillActivationImplicit},
		{activation: SkillActivationExplicit, promptPrefix: true},
		{activation: SkillActivationDeveloper, developerReinforcement: true},
		{activation: SkillActivationCombined, promptPrefix: true, developerReinforcement: true},
	}
	for _, test := range tests {
		t.Run(test.activation, func(t *testing.T) {
			descriptor, err := describeSkillActivation(test.activation)
			if err != nil {
				t.Fatal(err)
			}
			if descriptor.promptPrefix != test.promptPrefix || descriptor.developerReinforcement != test.developerReinforcement ||
				descriptor.hintsServiceSkill() != (test.promptPrefix || test.developerReinforcement) {
				t.Fatalf("descriptor=%+v", descriptor)
			}
		})
	}
	if _, err := describeSkillActivation("automatic"); err == nil {
		t.Fatal("unknown activation descriptor passed")
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
		"codex legacy prefix": func(s *RunSpec, _ *Scenario) {
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
			candidate.Checks = append([]RunCheck(nil), spec.Checks...)
			candidateScenario := scenario
			mutate(&candidate, &candidateScenario)
			if candidate.Validate() == nil && candidate.ValidateAgainstScenario(candidateScenario) == nil {
				t.Fatal("unsafe synthetic write spec passed")
			}
		})
	}
	codex := spec
	codex.Checks = append([]RunCheck(nil), spec.Checks...)
	codex.Provider = "codex"
	codex.Pricing = Pricing{InputMicroUSDPerMillionTokens: 1, OutputMicroUSDPerMillionTokens: 1}
	codex.AllowedATLCommands = nil
	codex.AllowedCLICommands = validCLICommandPolicy().Rules
	if err := codex.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := codex.ValidateAgainstScenario(scenario); err != nil {
		t.Fatal(err)
	}
	codex.AllowedTools = append(codex.AllowedTools, "Skill")
	codex.Checks = append(codex.Checks, RunCheck{Name: "skill", Kind: "skill_invocations_min", Minimum: 1})
	if err := codex.Validate(); err != nil {
		t.Fatalf("confined Codex skill invocation minimum: %v", err)
	}
	codex.Checks[len(codex.Checks)-1].Expected = json.RawMessage(`"atl:confluence"`)
	if err := codex.Validate(); err == nil || !strings.Contains(err.Error(), "named skill_invocations_min") {
		t.Fatalf("named Codex skill invocation oracle passed: %v", err)
	}
}

func TestPrivateLiveWritesRequireExactReviewedBoundaries(t *testing.T) {
	build := func() (RunSpec, Scenario) {
		scenario := validScenario()
		scenario.DataClass = "private-local"
		scenario.RequiredChecks = []string{"answer_correct", "used_atl", "http_observed", "guard_clean", "no_delegation", "atl_succeeded", "methods"}
		scenario.Budgets.MaxRemoteWrites = 1
		scenario.Budgets.MaxDelegations = 0
		scenario.Budgets.MaxBackendRequests = 3
		scenario.Budgets.MaxATLInvocations = 2
		scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
		scenario.Budgets.AllowedHTTPMethods = []string{"GET", "POST"}
		spec := validRunSpec()
		spec.BackendMode = BackendModePrivateLive
		spec.FixtureFile = ""
		spec.Repetitions = 1
		spec.ToolTransport = "cli"
		spec.SkillActivation = SkillActivationImplicit
		spec.AllowedTools = []string{"Bash(atl *)", "Read"}
		spec.AllowedATLCommands = nil
		spec.AllowedCLICommands = []CLICommandRule{{Name: "create", Command: []string{"jira", "issue", "create"}, Flags: []CLIFlagRule{
			{Name: "--project", Values: []string{"TEST"}, Required: true},
			{Name: "--type", Values: []string{"Task"}, Required: true},
			{Name: "--summary", Values: []string{"reviewed fixture"}, Required: true},
			{Name: "--from-file", Values: []string{"body.txt"}, Required: true},
		}, MaxInvocations: 1}}
		spec.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": {
			{Name: "metadata", PathPrefix: "/rest/api/2/issue/createmeta", Methods: []string{"GET"}, MaxRequests: 2},
			{Name: "create", PathPrefix: "/rest/api/2/issue", Exact: true, Methods: []string{"POST"}, MaxRequests: 1, MaxRequestBytes: 1 << 20},
		}}
		spec.GatewayMaxResponseBytes = 1 << 20
		spec.GatewayMaxTotalBytes = 3 << 20
		spec.GatewayMaxRequestBytes = 1 << 20
		spec.GatewayMaxTotalRequestBytes = 1 << 20
		spec.AllowLiveWrites = true
		spec.MaxEstimatedCostMicroUSD = scenario.Budgets.MaxEstimatedCostMicroUSD
		spec.Checks = append(spec.Checks,
			RunCheck{Name: "http_observed", Kind: "http_methods_observed"},
			RunCheck{Name: "guard_clean", Kind: "guard_no_denials"},
			RunCheck{Name: "no_delegation", Kind: "delegations_none"},
			RunCheck{Name: "atl_succeeded", Kind: "atl_all_succeeded"},
			RunCheck{Name: "methods", Kind: "http_methods_equal", Expected: json.RawMessage(`{"GET":2,"POST":1}`)},
		)
		return spec, scenario
	}
	spec, scenario := build()
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := spec.ValidateAgainstScenario(scenario); err != nil {
		t.Fatal(err)
	}
	partitioned, partitionedScenario := build()
	partitioned.AllowedGatewayRoutes["jira"][0].PathPrefix = "/rest/api/2/issue"
	partitioned.AllowedGatewayRoutes["jira"][0].Exact = true
	if err := partitioned.Validate(); err != nil {
		t.Fatalf("method-partitioned exact routes: %v", err)
	}
	if err := partitioned.ValidateAgainstScenario(partitionedScenario); err != nil {
		t.Fatalf("method-partitioned exact routes against scenario: %v", err)
	}
	negative, negativeScenario := build()
	negative.Checks = slices.DeleteFunc(negative.Checks, func(check RunCheck) bool {
		return check.Kind == "atl_all_succeeded" || check.Kind == "interface_all_succeeded"
	})
	negative.Checks = append(negative.Checks,
		RunCheck{Name: "expected_failure", Kind: "interface_failures_equals", Expected: json.RawMessage(`1`)},
		RunCheck{Name: "exit_codes", Kind: "cli_exit_codes_equal", Expected: json.RawMessage(`[8]`)},
		RunCheck{Name: "error_contracts", Kind: "cli_error_contracts_equal",
			Expected: json.RawMessage(`[{"exit_code":8,"kind":"check_failed","remediation":"review_failed_check"}]`)},
	)
	negativeScenario.RequiredChecks = slices.DeleteFunc(negativeScenario.RequiredChecks, func(name string) bool { return name == "atl_succeeded" })
	negativeScenario.RequiredChecks = append(negativeScenario.RequiredChecks, "expected_failure", "exit_codes", "error_contracts")
	if err := negative.Validate(); err != nil {
		t.Fatalf("negative-path run spec: %v", err)
	}
	if err := negative.ValidateAgainstScenario(negativeScenario); err != nil {
		t.Fatalf("negative-path run spec against scenario: %v", err)
	}
	setNegativeExpected := func(s *RunSpec, name string, expected string) {
		for index := range s.Checks {
			if s.Checks[index].Name == name {
				s.Checks[index].Expected = json.RawMessage(expected)
				return
			}
		}
	}
	dropNegativeCheck := func(s *RunSpec, name string) {
		s.Checks = slices.DeleteFunc(s.Checks, func(check RunCheck) bool { return check.Name == name })
	}
	for name, mutate := range map[string]func(*RunSpec){
		"zero only":           func(s *RunSpec) { setNegativeExpected(s, "exit_codes", `[0]`) },
		"wrong failure count": func(s *RunSpec) { setNegativeExpected(s, "expected_failure", `2`) },
		"missing exit oracle": func(s *RunSpec) { dropNegativeCheck(s, "exit_codes") },
		"missing error contract oracle": func(s *RunSpec) {
			dropNegativeCheck(s, "error_contracts")
		},
		"short error contract oracle": func(s *RunSpec) {
			setNegativeExpected(s, "exit_codes", `[8,8]`)
			setNegativeExpected(s, "expected_failure", `2`)
		},
		"mismatched error contract exit": func(s *RunSpec) {
			setNegativeExpected(s, "error_contracts", `[{"exit_code":5,"kind":"version_conflict","remediation":"refresh_and_reapply"}]`)
		},
		"duplicate error contract oracle": func(s *RunSpec) {
			s.Checks = append(s.Checks, RunCheck{Name: "error_contracts_again", Kind: "cli_error_contracts_equal",
				Expected: json.RawMessage(`[{"exit_code":8,"kind":"check_failed","remediation":"review_failed_check"}]`)})
		},
		"unknown contract kind": func(s *RunSpec) {
			setNegativeExpected(s, "error_contracts", `[{"exit_code":8,"kind":"backend_said_no","remediation":"review_failed_check"}]`)
		},
		"mismatched contract remediation": func(s *RunSpec) {
			setNegativeExpected(s, "error_contracts", `[{"exit_code":8,"kind":"check_failed","remediation":"reauthenticate"}]`)
		},
		"successful contract exit code": func(s *RunSpec) {
			setNegativeExpected(s, "error_contracts", `[{"exit_code":0,"kind":"check_failed","remediation":"review_failed_check"}]`)
		},
		"free-text contract member": func(s *RunSpec) {
			setNegativeExpected(s, "error_contracts", `[{"exit_code":8,"kind":"check_failed","remediation":"review_failed_check","error":"backend prose"}]`)
		},
	} {
		t.Run("negative "+name, func(t *testing.T) {
			candidate := negative
			candidate.Checks = append([]RunCheck(nil), negative.Checks...)
			mutate(&candidate)
			if candidate.Validate() == nil && candidate.ValidateAgainstScenario(negativeScenario) == nil {
				t.Fatal("unsafe negative private-live contract passed")
			}
		})
	}
	for name, mutate := range map[string]func(*RunSpec, *Scenario){
		"legacy schema": func(s *RunSpec, _ *Scenario) { s.SchemaVersion = LegacyRunSpecSchemaVersion },
		"mcp":           func(s *RunSpec, _ *Scenario) { s.ToolTransport = "mcp" },
		"zero writes":   func(_ *RunSpec, sc *Scenario) { sc.Budgets.MaxRemoteWrites = 0 },
		"non-exact write route": func(s *RunSpec, _ *Scenario) {
			s.AllowedGatewayRoutes["jira"][1].Exact = false
		},
		"unreviewed method": func(s *RunSpec, _ *Scenario) {
			s.AllowedGatewayRoutes["jira"][1].Methods = []string{"PUT"}
		},
		"mixed route": func(s *RunSpec, _ *Scenario) {
			s.AllowedGatewayRoutes["jira"][1].Methods = []string{"GET", "POST"}
		},
		"route write budget": func(s *RunSpec, _ *Scenario) {
			s.AllowedGatewayRoutes["jira"][1].MaxRequests = 2
		},
		"missing request budget": func(s *RunSpec, _ *Scenario) {
			s.GatewayMaxRequestBytes = 0
			s.GatewayMaxTotalRequestBytes = 0
		},
		"missing exact methods oracle": func(s *RunSpec, _ *Scenario) {
			s.Checks = s.Checks[:len(s.Checks)-1]
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate, candidateScenario := build()
			mutate(&candidate, &candidateScenario)
			if candidate.Validate() == nil && candidate.ValidateAgainstScenario(candidateScenario) == nil {
				t.Fatal("unsafe private live-write spec passed")
			}
		})
	}
}

// The mandatory error-contract oracle is bound to write authority. A read-only
// private-live negative path keeps the exit-code contract it always had and
// must not be forced to declare a typed classification it never authorized.
func TestReadOnlyPrivateLiveNegativePathKeepsExitCodeContractOnly(t *testing.T) {
	scenario := validScenario()
	scenario.DataClass = "private-local"
	scenario.RequiredChecks = []string{"answer_correct", "used_atl", "http_observed", "guard_clean", "no_delegation", "expected_failure", "exit_codes"}
	scenario.Budgets.MaxRemoteWrites = 0
	scenario.Budgets.MaxDelegations = 0
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
	scenario.Budgets.AllowedHTTPMethods = []string{"GET", "HEAD"}
	spec := validRunSpec()
	spec.BackendMode = BackendModePrivateLive
	spec.FixtureFile = ""
	spec.Repetitions = 1
	spec.ToolTransport = "cli"
	spec.SkillActivation = SkillActivationImplicit
	spec.AllowedTools = []string{"Bash(atl *)", "Read"}
	spec.AllowedATLCommands = nil
	spec.AllowedCLICommands = validCLICommandPolicy().Rules
	spec.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": {{Name: "jira_api", PathPrefix: "/rest/api/2"}}}
	spec.GatewayMaxResponseBytes = 1 << 20
	spec.GatewayMaxTotalBytes = 4 << 20
	spec.MaxEstimatedCostMicroUSD = scenario.Budgets.MaxEstimatedCostMicroUSD
	spec.Checks = append(spec.Checks,
		RunCheck{Name: "http_observed", Kind: "http_methods_observed"},
		RunCheck{Name: "guard_clean", Kind: "guard_no_denials"},
		RunCheck{Name: "no_delegation", Kind: "delegations_none"},
		RunCheck{Name: "expected_failure", Kind: "interface_failures_equals", Expected: json.RawMessage(`1`)},
		RunCheck{Name: "exit_codes", Kind: "cli_exit_codes_equal", Expected: json.RawMessage(`[4]`)},
	)
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := spec.ValidateAgainstScenario(scenario); err != nil {
		t.Fatalf("read-only negative path without an error-contract oracle: %v", err)
	}
	// The same run may still declare one, and the exit-code rule keeps applying.
	optional := spec
	optional.Checks = append(append([]RunCheck(nil), spec.Checks...), RunCheck{
		Name: "error_contracts", Kind: "cli_error_contracts_equal",
		Expected: json.RawMessage(`[{"exit_code":4,"kind":"not_found","remediation":"verify_identifier_or_access"}]`)})
	if err := optional.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := optional.ValidateAgainstScenario(scenario); err != nil {
		t.Fatalf("read-only negative path with an error-contract oracle: %v", err)
	}
	zeroed := spec
	zeroed.Checks = append([]RunCheck(nil), spec.Checks...)
	zeroed.Checks[len(zeroed.Checks)-1].Expected = json.RawMessage(`[0]`)
	if zeroed.Validate() == nil && zeroed.ValidateAgainstScenario(scenario) == nil {
		t.Fatal("read-only negative path accepted a successful exit-code contract")
	}
}

func TestRunSpecCodexSyntheticReadOnlyBrokerUsesExactCommands(t *testing.T) {
	spec := validRunSpec()
	spec.AllowedATLCommands = nil
	spec.AllowedCLICommands = validCLICommandPolicy().Rules
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if !isCodexSyntheticBrokerCLI(spec) || !isCodexConfinedCLI(spec) {
		t.Fatal("exact synthetic Codex CLI policy did not select broker confinement")
	}

	legacy := validRunSpec()
	if isCodexSyntheticBrokerCLI(legacy) || isCodexConfinedCLI(legacy) {
		t.Fatal("legacy prefix-based synthetic Codex spec unexpectedly selected broker confinement")
	}

	mixed := spec
	mixed.AllowedATLCommands = []string{"atl version"}
	if err := mixed.Validate(); err == nil || !strings.Contains(err.Error(), "forbids prefix-based") {
		t.Fatalf("mixed exact/prefix policy err=%v", err)
	}
}

func TestEvaluateRunChecksUsesStructuredValuesOnly(t *testing.T) {
	checks := []RunCheck{
		{Name: "equals", Kind: "json_equals", Pointer: "/nested/value", Expected: json.RawMessage(`7`)},
		{Name: "present", Kind: "json_present", Pointer: "/nested"},
		{Name: "array_items", Kind: "json_array_min_items", Pointer: "/items", Minimum: 2},
		{Name: "used", Kind: "atl_invocations_min", Minimum: 2},
		{Name: "used_interface", Kind: "interface_invocations_min", Minimum: 2},
		{Name: "used_skill", Kind: "skill_invocations_min", Minimum: 1},
		{Name: "used_jira_skill", Kind: "skill_invocations_min", Minimum: 1, Expected: json.RawMessage(`"atl:jira"`)},
		{Name: "bounded", Kind: "atl_invocations_max", Maximum: 2},
		{Name: "bounded_interface", Kind: "interface_invocations_max", Maximum: 2},
		{Name: "interface_failures", Kind: "interface_failures_equals", Expected: json.RawMessage(`1`)},
		{Name: "expected_fail_closed", Kind: "atl_failures_equals", Expected: json.RawMessage(`1`)},
		{Name: "exit_codes", Kind: "cli_exit_codes_equal", Expected: json.RawMessage(`[0,8]`)},
		{Name: "routes", Kind: "mock_no_unexpected"},
		{Name: "delegated", Kind: "delegations_min", Minimum: 1},
		{Name: "guarded", Kind: "guard_no_denials"},
		{Name: "methods", Kind: "http_methods_equal", Expected: json.RawMessage(`{"GET":2,"PUT":1}`)},
	}
	result, err := evaluateRunChecks(checks, []byte(`{"nested":{"value":7},"items":["a","b"]}`), "", 2, 1, 0, 1, map[string]int{"atl:jira": 1}, 1, 0, map[string]int{"GET": 2, "PUT": 1}, true, []int{0, 8})
	if err != nil {
		t.Fatal(err)
	}
	for name, passed := range result {
		if !passed {
			t.Errorf("check %s failed", name)
		}
	}
	over, err := evaluateRunChecks(checks, []byte(`{"nested":{"value":7},"items":["a"]}`), "", 3, 0, 0, 1, map[string]int{"atl:confluence": 1}, 1, 0, map[string]int{"GET": 3}, true, []int{0, 0, 0})
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
	if over["array_items"] {
		t.Fatal("json_array_min_items accepted a short array")
	}
	if over["exit_codes"] {
		t.Fatal("cli_exit_codes_equal accepted different ordered exit codes")
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
	success, err := evaluateRunChecks([]RunCheck{{Name: "interface_succeeded", Kind: "interface_all_succeeded"}}, []byte(`{}`), "", 1, 0, 0, 0, nil, 0, 0, nil, false, []int{0})
	if err != nil || !success["interface_succeeded"] {
		t.Fatalf("interface success alias result=%v err=%v", success, err)
	}
}

func TestRunSpecValidatesOptionalTerminalPeriodStringCheck(t *testing.T) {
	check := RunCheck{
		Name: "qualified_limit", Kind: "json_string_equals_optional_period",
		Pointer: "/limit", Expected: json.RawMessage(`"Up to 40 percent"`),
	}
	valid := validRunSpec()
	valid.Checks = append(valid.Checks, check)
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if runCheckClass(check.Kind) != "semantic" ||
		privateActivationSafetyCheckKind(check.Kind) {
		t.Fatal("optional-period string check is not classified as semantic")
	}

	tests := []struct {
		name  string
		final string
		want  bool
	}{
		{name: "canonical", final: `{"limit":"Up to 40 percent"}`, want: true},
		{name: "one period", final: `{"limit":"Up to 40 percent."}`, want: true},
		{name: "missing qualifier", final: `{"limit":"40 percent"}`},
		{name: "different case", final: `{"limit":"up to 40 percent"}`},
		{name: "different value", final: `{"limit":"Up to 30 percent"}`},
		{name: "two periods", final: `{"limit":"Up to 40 percent.."}`},
		{name: "non-string", final: `{"limit":40}`},
		{name: "missing", final: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			results, err := evaluateRunChecks(
				[]RunCheck{check}, []byte(test.final), "", 0, 0, 0, 0,
				nil, 0, 0, nil, false, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if results[check.Name] != test.want {
				t.Fatalf("result=%v want %v", results[check.Name], test.want)
			}
		})
	}

	overlongExpected, err := json.Marshal(strings.Repeat("x", maxOptionalPeriodExpectedBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	invalidChecks := []RunCheck{
		{Name: "missing_pointer", Kind: check.Kind, Expected: check.Expected},
		{Name: "missing_expected", Kind: check.Kind, Pointer: check.Pointer},
		{Name: "empty", Kind: check.Kind, Pointer: check.Pointer, Expected: json.RawMessage(`""`)},
		{Name: "non_string", Kind: check.Kind, Pointer: check.Pointer, Expected: json.RawMessage(`40`)},
		{Name: "leading_space", Kind: check.Kind, Pointer: check.Pointer, Expected: json.RawMessage(`" Up to 40 percent"`)},
		{Name: "terminal_period", Kind: check.Kind, Pointer: check.Pointer, Expected: json.RawMessage(`"Up to 40 percent."`)},
		{Name: "minimum", Kind: check.Kind, Pointer: check.Pointer, Expected: check.Expected, Minimum: 1},
		{Name: "maximum", Kind: check.Kind, Pointer: check.Pointer, Expected: check.Expected, Maximum: 1},
		{Name: "overlong", Kind: check.Kind, Pointer: check.Pointer, Expected: overlongExpected},
	}
	for _, invalid := range invalidChecks {
		spec := validRunSpec()
		spec.Checks = append(spec.Checks, invalid)
		if err := spec.Validate(); err == nil {
			t.Fatalf("invalid check passed: %+v", invalid)
		}
	}
}

func TestRunSpecValidatesJSONArrayMinItemsCheck(t *testing.T) {
	check := RunCheck{
		Name: "evidence_present", Kind: "json_array_min_items",
		Pointer: "/evidence", Minimum: 2,
	}
	valid := validRunSpec()
	valid.Checks = append(valid.Checks, check)
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if runCheckClass(check.Kind) != "semantic" ||
		privateActivationSafetyCheckKind(check.Kind) {
		t.Fatal("array minimum check is not classified as semantic")
	}
	for name, test := range map[string]struct {
		final string
		want  bool
	}{
		"exact":   {final: `{"evidence":["a","b"]}`, want: true},
		"over":    {final: `{"evidence":["a","b","c"]}`, want: true},
		"short":   {final: `{"evidence":["a"]}`},
		"missing": {final: `{}`},
		"object":  {final: `{"evidence":{"a":"b"}}`},
		"null":    {final: `{"evidence":null}`},
	} {
		t.Run(name, func(t *testing.T) {
			results, err := evaluateRunChecks(
				[]RunCheck{check}, []byte(test.final), "", 0, 0, 0, 0,
				nil, 0, 0, nil, false, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if results[check.Name] != test.want {
				t.Fatalf("result=%v want %v", results[check.Name], test.want)
			}
		})
	}

	for name, mutate := range map[string]func(*RunCheck){
		"missing pointer": func(value *RunCheck) { value.Pointer = "" },
		"zero minimum":    func(value *RunCheck) { value.Minimum = 0 },
		"unexpected max":  func(value *RunCheck) { value.Maximum = 2 },
		"unexpected value": func(value *RunCheck) {
			value.Expected = json.RawMessage(`2`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validRunSpec()
			mutated := check
			mutate(&mutated)
			candidate.Checks = append(candidate.Checks, mutated)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid json_array_min_items check passed")
			}
		})
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
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "unclassified") {
		t.Fatalf("unclassified or richer route passed: %v", err)
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

func TestNeutralPrivateCLIDataCapabilityClassifiesJiraIssueRefs(t *testing.T) {
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
		Name: "refs", Command: []string{"jira", "issue", "refs"}, MaxInvocations: 1,
		Positionals: []CLIArgumentRule{{Values: []string{"PROJ-1"}}},
	}}
	spec.DataCapabilities = []string{"jira.issue.refs"}
	if err := validateRunDataCapabilities(spec); err != nil {
		t.Fatal(err)
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
	result, err := evaluateRunChecks([]RunCheck{check}, []byte(`{"proposal_hash":"abc"}`), workspace, 0, 0, 0, 0, nil, 0, 0, nil, false, nil)
	if err != nil || !result["proposal"] {
		t.Fatalf("result=%v err=%v", result, err)
	}
	result, err = evaluateRunChecks([]RunCheck{check}, []byte(`{"proposal_hash":"wrong"}`), workspace, 0, 0, 0, 0, nil, 0, 0, nil, false, nil)
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

func TestEvaluateRunChecksBindsGeneratedWorkspaceFileSHA256(t *testing.T) {
	workspace := t.TempDir()
	artifact := filepath.Join(workspace, "generated", "report.bin")
	if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("reviewed artifact\n")
	if err := os.WriteFile(artifact, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	check := RunCheck{Name: "artifact", Kind: "workspace_file_sha256", Expected: json.RawMessage(fmt.Sprintf(
		`{"path":"generated/report.bin","sha256":"%x"}`, digest))}
	result, err := evaluateRunChecks([]RunCheck{check}, []byte(`{}`), workspace, 0, 0, 0, 0, nil, 0, 0, nil, false, nil)
	if err != nil || !result["artifact"] {
		t.Fatalf("result=%v err=%v", result, err)
	}

	for name, prepare := range map[string]func(*testing.T) (string, [sha256.Size]byte){
		"missing": func(*testing.T) (string, [sha256.Size]byte) { return "missing.bin", digest },
		"directory": func(t *testing.T) (string, [sha256.Size]byte) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(workspace, "directory"), 0o700); err != nil {
				t.Fatal(err)
			}
			return "directory", digest
		},
		"oversized": func(t *testing.T) (string, [sha256.Size]byte) {
			t.Helper()
			path := filepath.Join(workspace, "oversized.bin")
			oversized := make([]byte, maxWorkspaceArtifactBytes+1)
			if err := os.WriteFile(path, oversized, 0o600); err != nil {
				t.Fatal(err)
			}
			return "oversized.bin", sha256.Sum256(oversized)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := check
			path, expectedDigest := prepare(t)
			candidate.Expected = json.RawMessage(fmt.Sprintf(`{"path":%q,"sha256":"%x"}`, path, expectedDigest))
			got, err := evaluateRunChecks([]RunCheck{candidate}, []byte(`{}`), workspace, 0, 0, 0, 0, nil, 0, 0, nil, false, nil)
			if err != nil || got["artifact"] {
				t.Fatalf("result=%v err=%v", got, err)
			}
		})
	}

	t.Run("digest mismatch", func(t *testing.T) {
		candidate := check
		candidate.Expected = json.RawMessage(`{"path":"generated/report.bin","sha256":"0000000000000000000000000000000000000000000000000000000000000000"}`)
		got, err := evaluateRunChecks([]RunCheck{candidate}, []byte(`{}`), workspace, 0, 0, 0, 0, nil, 0, 0, nil, false, nil)
		if err != nil || got["artifact"] {
			t.Fatalf("result=%v err=%v", got, err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(workspace, "artifact-link.bin")
		if err := os.Symlink("generated/report.bin", link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		candidate := check
		candidate.Expected = json.RawMessage(fmt.Sprintf(`{"path":"artifact-link.bin","sha256":"%x"}`, digest))
		got, err := evaluateRunChecks([]RunCheck{candidate}, []byte(`{}`), workspace, 0, 0, 0, 0, nil, 0, 0, nil, false, nil)
		if err != nil || got["artifact"] {
			t.Fatalf("result=%v err=%v", got, err)
		}
	})

	t.Run("intermediate symlink", func(t *testing.T) {
		link := filepath.Join(workspace, "generated-link")
		if err := os.Symlink("generated", link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		candidate := check
		candidate.Expected = json.RawMessage(fmt.Sprintf(`{"path":"generated-link/report.bin","sha256":"%x"}`, digest))
		got, err := evaluateRunChecks([]RunCheck{candidate}, []byte(`{}`), workspace, 0, 0, 0, 0, nil, 0, 0, nil, false, nil)
		if err != nil || got["artifact"] {
			t.Fatalf("result=%v err=%v", got, err)
		}
	})
}

func TestRunSpecValidatesWorkspaceFileSHA256Expectation(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := validRunSpec()
	valid.Checks = append(valid.Checks, RunCheck{Name: "artifact", Kind: "workspace_file_sha256", Expected: json.RawMessage(
		`{"path":"generated/report.bin","sha256":"` + digest + `"}`)})
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if runCheckClass("workspace_file_sha256") != "semantic" {
		t.Fatal("workspace_file_sha256 is not classified as a semantic check")
	}

	for name, expected := range map[string]json.RawMessage{
		"missing":       nil,
		"absolute":      json.RawMessage(`{"path":"/tmp/report.bin","sha256":"` + digest + `"}`),
		"traversal":     json.RawMessage(`{"path":"../report.bin","sha256":"` + digest + `"}`),
		"unclean":       json.RawMessage(`{"path":"generated/../report.bin","sha256":"` + digest + `"}`),
		"backslash":     json.RawMessage(`{"path":"generated\\report.bin","sha256":"` + digest + `"}`),
		"uppercase":     json.RawMessage(`{"path":"report.bin","sha256":"` + strings.ToUpper(digest) + `"}`),
		"short digest":  json.RawMessage(`{"path":"report.bin","sha256":"abcd"}`),
		"unknown field": json.RawMessage(`{"path":"report.bin","sha256":"` + digest + `","size":1}`),
	} {
		t.Run(name, func(t *testing.T) {
			spec := validRunSpec()
			spec.Checks = append(spec.Checks, RunCheck{Name: "artifact", Kind: "workspace_file_sha256", Expected: expected})
			if err := spec.Validate(); err == nil {
				t.Fatal("invalid workspace artifact expectation passed")
			}
		})
	}

	pointer := validRunSpec()
	pointer.Checks = append(pointer.Checks, RunCheck{Name: "artifact", Kind: "workspace_file_sha256", Pointer: "/artifact", Expected: valid.Checks[len(valid.Checks)-1].Expected})
	if err := pointer.Validate(); err == nil {
		t.Fatal("workspace artifact expectation accepted a final-response pointer")
	}
	minimum := validRunSpec()
	minimum.Checks = append(minimum.Checks, RunCheck{Name: "artifact", Kind: "workspace_file_sha256", Minimum: 1, Expected: valid.Checks[len(valid.Checks)-1].Expected})
	if err := minimum.Validate(); err == nil {
		t.Fatal("workspace artifact expectation accepted a minimum")
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

func TestRunSpecValidatesExactCapabilityFamilies(t *testing.T) {
	expected := json.RawMessage(`[
		{"family":"confluence.page.outline","invocations":1,"successes":1,"failures":0},
		{"family":"confluence.page.resolve","invocations":1,"successes":1,"failures":0},
		{"family":"confluence.page.section","invocations":1,"successes":1,"failures":0}
	]`)
	valid := validRunSpec()
	valid.Checks = append(valid.Checks, RunCheck{
		Name: "route_exact", Kind: "capability_families_equal", Expected: expected,
	})
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if runCheckClass("capability_families_equal") != "mechanical" {
		t.Fatal("capability_families_equal is not classified as a mechanical check")
	}

	observed := []CapabilityFamilyMetric{
		{Family: "confluence.page.resolve", Invocations: 1, Successes: 1, OutputBytes: 7},
		{Family: "confluence.page.section", Invocations: 1, Successes: 1, OutputBytes: 11},
		{Family: "confluence.page.outline", Invocations: 1, Successes: 1, OutputBytes: 9},
	}
	checks, err := evaluateRunChecksWithCapabilities(
		valid.Checks[len(valid.Checks)-1:], []byte(`{}`), "", 3, 0, 0, 0,
		nil, 0, 0, map[string]int{"GET": 2}, true, nil, observed, true, nil,
	)
	if err != nil || !checks["route_exact"] {
		t.Fatalf("exact capability route result=%v err=%v", checks, err)
	}

	tests := []struct {
		name     string
		observed []CapabilityFamilyMetric
		coverage bool
	}{
		{name: "uncovered", observed: observed, coverage: false},
		{name: "missing", observed: observed[:2], coverage: true},
		{name: "repeated", observed: []CapabilityFamilyMetric{
			{Family: "confluence.page.outline", Invocations: 1, Successes: 1},
			{Family: "confluence.page.resolve", Invocations: 1, Successes: 1},
			{Family: "confluence.page.section", Invocations: 2, Successes: 2},
		}, coverage: true},
		{name: "failed", observed: []CapabilityFamilyMetric{
			{Family: "confluence.page.outline", Invocations: 1, Successes: 1},
			{Family: "confluence.page.resolve", Invocations: 1, Successes: 1},
			{Family: "confluence.page.section", Invocations: 1, Failures: 1},
		}, coverage: true},
		{name: "extra", observed: append(slices.Clone(observed),
			CapabilityFamilyMetric{Family: "confluence.search", Invocations: 1, Successes: 1},
		), coverage: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checks, err := evaluateRunChecksWithCapabilities(
				valid.Checks[len(valid.Checks)-1:], []byte(`{}`), "", 3, 0, 0, 0,
				nil, 0, 0, map[string]int{"GET": 2}, true, nil, test.observed, test.coverage, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if checks["route_exact"] {
				t.Fatal("non-exact capability route passed")
			}
		})
	}

	for _, invalid := range []json.RawMessage{
		nil,
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`{}`),
		json.RawMessage(`[{"family":"unknown","invocations":1,"successes":1,"failures":0}]`),
		json.RawMessage(`[{"family":"jira.fields","invocations":0,"successes":0,"failures":0}]`),
		json.RawMessage(`[{"family":"jira.fields","invocations":1,"successes":0,"failures":0}]`),
		json.RawMessage(`[{"family":"jira.fields","invocations":1,"successes":1,"failures":0,"output_bytes":1}]`),
		json.RawMessage(`[{"family":"jira.issue.search","invocations":1,"successes":1,"failures":0},{"family":"jira.fields","invocations":1,"successes":1,"failures":0}]`),
		json.RawMessage(`[{"family":"jira.fields","invocations":1,"successes":1,"failures":0},{"family":"jira.fields","invocations":1,"successes":1,"failures":0}]`),
	} {
		spec := validRunSpec()
		spec.Checks = append(spec.Checks, RunCheck{
			Name: "route_exact", Kind: "capability_families_equal", Expected: invalid,
		})
		if err := spec.Validate(); err == nil {
			t.Fatalf("invalid capability oracle passed: %s", invalid)
		}
	}
}

func TestRunSpecValidatesExactCapabilitySequence(t *testing.T) {
	expected := json.RawMessage(`[
		"confluence.page.resolve",
		"confluence.page.outline",
		"confluence.page.section"
	]`)
	valid := validRunSpec()
	valid.Checks = append(valid.Checks, RunCheck{
		Name: "route_ordered", Kind: "capability_sequence_equal", Expected: expected,
	})
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if runCheckClass("capability_sequence_equal") != "mechanical" {
		t.Fatal("capability_sequence_equal is not classified as a mechanical check")
	}
	families := []CapabilityFamilyMetric{
		{Family: "confluence.page.outline", Invocations: 1, Successes: 1},
		{Family: "confluence.page.resolve", Invocations: 1, Successes: 1},
		{Family: "confluence.page.section", Invocations: 1, Successes: 1},
	}
	for name, test := range map[string]struct {
		sequence []string
		coverage bool
		want     bool
	}{
		"exact": {
			sequence: []string{"confluence.page.resolve", "confluence.page.outline", "confluence.page.section"},
			coverage: true, want: true,
		},
		"wrong order": {
			sequence: []string{"confluence.page.section", "confluence.page.outline", "confluence.page.resolve"},
			coverage: true,
		},
		"missing": {
			sequence: []string{"confluence.page.resolve", "confluence.page.section"},
			coverage: true,
		},
		"uncovered": {
			sequence: []string{"confluence.page.resolve", "confluence.page.outline", "confluence.page.section"},
			coverage: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			checks, err := evaluateRunChecksWithCapabilities(
				valid.Checks[len(valid.Checks)-1:], []byte(`{}`), "", 3, 0, 0, 0,
				nil, 0, 0, map[string]int{"GET": 2}, true, nil,
				families, test.coverage, test.sequence,
			)
			if err != nil {
				t.Fatal(err)
			}
			if checks["route_ordered"] != test.want {
				t.Fatalf("route_ordered=%v want=%v", checks["route_ordered"], test.want)
			}
		})
	}
	for _, invalid := range []json.RawMessage{
		nil,
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`{}`),
		json.RawMessage(`["unknown"]`),
		json.RawMessage(`["jira.fields",1]`),
		json.RawMessage(`["jira.fields"] true`),
	} {
		spec := validRunSpec()
		spec.Checks = append(spec.Checks, RunCheck{
			Name: "route_ordered", Kind: "capability_sequence_equal", Expected: invalid,
		})
		if err := spec.Validate(); err == nil {
			t.Fatalf("invalid capability sequence oracle passed: %s", invalid)
		}
	}
}

func TestRunSpecValidatesExactMCPInvocations(t *testing.T) {
	expected := json.RawMessage(`[
		{"tool":"jira_issue_search","arguments":{"jql":"project = DEMO","columns":["key","status"],"limit":10}},
		{"tool":"confluence_page_section","arguments":{"reference":"42","heading":"Decision","occurrence":2,"max_bytes":32768}}
	]`)
	valid := validRunSpec()
	valid.ToolTransport = "mcp"
	valid.Surface = SurfaceATLMCP
	valid.AllowedTools = nil
	valid.AllowedATLCommands = nil
	valid.AllowedMCPTools = []string{"jira_issue_search", "confluence_page_section"}
	valid.Checks = append(valid.Checks, RunCheck{
		Name: "route_arguments", Kind: "mcp_invocations_equal", Expected: expected,
	})
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if runCheckClass("mcp_invocations_equal") != "mechanical" ||
		!privateActivationSafetyCheckKind("mcp_invocations_equal") {
		t.Fatal("mcp_invocations_equal is not classified as a mechanical safety check")
	}

	observed, ok := expectedMCPInvocations(expected)
	if !ok {
		t.Fatal("valid invocation expectation did not decode")
	}
	checks, err := evaluateRunChecksWithMCPInvocations(
		valid.Checks[len(valid.Checks)-1:], []byte(`{}`), "", 2, 0, 0, 0,
		nil, 0, 0, map[string]int{"GET": 2}, true, nil, nil, true, nil,
		observed, true,
	)
	if err != nil || !checks["route_arguments"] {
		t.Fatalf("exact invocation result=%v err=%v", checks, err)
	}
	mutated := slices.Clone(observed)
	mutated[1].Arguments = json.RawMessage(`{"heading":"Decision","max_bytes":32768,"occurrence":1,"reference":"42"}`)
	checks, err = evaluateRunChecksWithMCPInvocations(
		valid.Checks[len(valid.Checks)-1:], []byte(`{}`), "", 2, 0, 0, 0,
		nil, 0, 0, map[string]int{"GET": 2}, true, nil, nil, true, nil,
		mutated, true,
	)
	if err != nil || checks["route_arguments"] {
		t.Fatalf("mutated invocation result=%v err=%v", checks, err)
	}

	for _, invalid := range []json.RawMessage{
		nil,
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`[{"tool":"jira_issue_search","arguments":null}]`),
		json.RawMessage(`[{"tool":"jira_issue_search","arguments":[]}]`),
		json.RawMessage(`[{"tool":"not_allowed","arguments":{}}]`),
		json.RawMessage(`[{"tool":"jira_issue_search","arguments":{},"extra":true}]`),
		json.RawMessage(`[{"tool":"jira_issue_search","tool":"confluence_page_section","arguments":{}}]`),
	} {
		spec := valid
		spec.Checks = slices.Clone(valid.Checks)
		spec.Checks[len(spec.Checks)-1].Expected = invalid
		if err := spec.Validate(); err == nil {
			t.Fatalf("invalid MCP invocation oracle passed: %s", invalid)
		}
	}
}

// TestRunSpecValidatesOrderInsensitiveMCPInvocations binds the diagnostic
// separation the order-insensitive kind exists for: it must accept a reordered
// but otherwise identical route while the ordered kind rejects it, and it must
// still reject a missing, extra, repeated, or differently-argued call.
func TestRunSpecValidatesOrderInsensitiveMCPInvocations(t *testing.T) {
	expected := json.RawMessage(`[
		{"tool":"jira_issue_search","arguments":{"jql":"project = DEMO","columns":["key","status"],"limit":10}},
		{"tool":"confluence_page_section","arguments":{"reference":"42","heading":"Decision","occurrence":1,"max_bytes":32768}},
		{"tool":"confluence_page_section","arguments":{"reference":"42","heading":"Decision","occurrence":2,"max_bytes":32768}}
	]`)
	valid := validRunSpec()
	valid.ToolTransport = "mcp"
	valid.Surface = SurfaceATLMCP
	valid.AllowedTools = nil
	valid.AllowedATLCommands = nil
	valid.AllowedMCPTools = []string{"jira_issue_search", "confluence_page_section"}
	valid.Checks = append(valid.Checks,
		RunCheck{Name: "route_arguments", Kind: "mcp_invocations_multiset_equal", Expected: expected},
		RunCheck{Name: "route_exact_order", Kind: "mcp_invocations_equal", Expected: expected},
	)
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if runCheckClass("mcp_invocations_multiset_equal") != "mechanical" ||
		!privateActivationSafetyCheckKind("mcp_invocations_multiset_equal") {
		t.Fatal("mcp_invocations_multiset_equal is not classified as a mechanical safety check")
	}

	observed, ok := expectedMCPInvocations(expected)
	if !ok {
		t.Fatal("valid invocation expectation did not decode")
	}
	routeChecks := valid.Checks[len(valid.Checks)-2:]
	evaluate := func(invocations []MCPInvocation, invocationsObserved bool) map[string]bool {
		t.Helper()
		checks, err := evaluateRunChecksWithMCPInvocations(
			routeChecks, []byte(`{}`), "", len(invocations), 0, 0, 0,
			nil, 0, 0, map[string]int{"GET": 3}, true, nil, nil, true, nil,
			invocations, invocationsObserved,
		)
		if err != nil {
			t.Fatal(err)
		}
		return checks
	}

	if checks := evaluate(observed, true); !checks["route_arguments"] || !checks["route_exact_order"] {
		t.Fatalf("conforming route rejected: %v", checks)
	}
	reordered := []MCPInvocation{observed[2], observed[0], observed[1]}
	if checks := evaluate(reordered, true); !checks["route_arguments"] || checks["route_exact_order"] {
		t.Fatalf("ordered and order-insensitive oracles did not separate: %v", checks)
	}
	if checks := evaluate(observed, false); checks["route_arguments"] {
		t.Fatalf("unobserved invocation telemetry passed: %v", checks)
	}

	withoutOrderedCompanion := valid
	withoutOrderedCompanion.Checks = slices.Clone(valid.Checks[:len(valid.Checks)-1])
	if err := withoutOrderedCompanion.Validate(); err == nil ||
		!strings.Contains(err.Error(), "requires a mcp_invocations_equal companion") {
		t.Fatalf("multiset route without ordered companion err=%v", err)
	}
	differentOrderedCompanion := valid
	differentOrderedCompanion.Checks = slices.Clone(valid.Checks)
	var differentExpected []json.RawMessage
	if err := json.Unmarshal(expected, &differentExpected); err != nil {
		t.Fatal(err)
	}
	differentExpected[0], differentExpected[1] = differentExpected[1], differentExpected[0]
	differentExpectedRaw, err := json.Marshal(differentExpected)
	if err != nil {
		t.Fatal(err)
	}
	differentOrderedCompanion.Checks[len(differentOrderedCompanion.Checks)-1].Expected = differentExpectedRaw
	if err := differentOrderedCompanion.Validate(); err == nil ||
		!strings.Contains(err.Error(), "requires a mcp_invocations_equal companion") {
		t.Fatalf("multiset route with different ordered companion err=%v", err)
	}

	changed := slices.Clone(observed)
	changed[2].Arguments = json.RawMessage(`{"heading":"Decision","max_bytes":16384,"occurrence":2,"reference":"42"}`)
	for name, invocations := range map[string][]MCPInvocation{
		"missing":   {observed[0], observed[1]},
		"extra":     {observed[0], observed[1], observed[2], observed[2]},
		"duplicate": {observed[0], observed[1], observed[1]},
		"arguments": changed,
	} {
		if checks := evaluate(invocations, true); checks["route_arguments"] {
			t.Fatalf("%s route passed the order-insensitive oracle", name)
		}
	}

	for _, invalid := range []json.RawMessage{
		nil,
		json.RawMessage(`[]`),
		json.RawMessage(`[{"tool":"jira_issue_search","arguments":[]}]`),
		json.RawMessage(`[{"tool":"not_allowed","arguments":{}}]`),
	} {
		spec := valid
		spec.Checks = slices.Clone(valid.Checks)
		spec.Checks[len(spec.Checks)-2].Expected = invalid
		if err := spec.Validate(); err == nil {
			t.Fatalf("invalid order-insensitive oracle passed: %s", invalid)
		}
	}
}

func TestRunSpecValidatesExactMCPRouteAlternatives(t *testing.T) {
	expected := json.RawMessage(`[
		{
			"http_methods":{"GET":2},
			"invocations":[{"tool":"jira_issue_field_get","arguments":{"key":"DEMO-1","field":"Description","max_bytes":16384}}]
		},
		{
			"http_methods":{"GET":1},
			"invocations":[{"tool":"jira_issue_field_get","arguments":{"key":"DEMO-1","field":"description","max_bytes":16384}}]
		}
	]`)
	valid := validRunSpec()
	valid.ToolTransport = "mcp"
	valid.Surface = SurfaceATLMCP
	valid.AllowedTools = nil
	valid.AllowedATLCommands = nil
	valid.AllowedMCPTools = []string{"jira_issue_field_get"}
	valid.Checks = append(valid.Checks, RunCheck{
		Name: "route_trajectory", Kind: "mcp_route_one_of", Expected: expected,
	})
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if runCheckClass("mcp_route_one_of") != "mechanical" ||
		!privateActivationSafetyCheckKind("mcp_route_one_of") {
		t.Fatal("mcp_route_one_of is not classified as a mechanical safety check")
	}

	alternatives, ok := expectedMCPRouteAlternatives(expected)
	if !ok || len(alternatives) != 2 {
		t.Fatalf("route alternatives=%+v ok=%v", alternatives, ok)
	}
	check := valid.Checks[len(valid.Checks)-1:]
	for index, alternative := range alternatives {
		results, err := evaluateRunChecksWithMCPInvocations(
			check, []byte(`{}`), "", 1, 0, 0, 0,
			nil, 0, 0, alternative.HTTPMethods, true, nil, nil, true, nil,
			alternative.Invocations, true,
		)
		if err != nil || !results["route_trajectory"] {
			t.Fatalf("alternative %d result=%v err=%v", index, results, err)
		}
	}
	results, err := evaluateRunChecksWithMCPInvocations(
		check, []byte(`{}`), "", 1, 0, 0, 0,
		nil, 0, 0, alternatives[0].HTTPMethods, true, nil, nil, true, nil,
		alternatives[1].Invocations, true,
	)
	if err != nil || results["route_trajectory"] {
		t.Fatalf("crossed alternative result=%v err=%v", results, err)
	}

	for _, invalid := range []json.RawMessage{
		nil,
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`[{"http_methods":{"GET":1},"invocations":[{"tool":"jira_issue_field_get","arguments":{}}]}]`),
		json.RawMessage(`[
			{"http_methods":{"GET":1},"invocations":[{"tool":"jira_issue_field_get","arguments":{}}]},
			{"http_methods":{"GET":1},"invocations":[{"tool":"jira_issue_field_get","arguments":{}}]}
		]`),
		json.RawMessage(`[
			{"http_methods":{"GET":1},"invocations":[{"tool":"jira_issue_field_get","arguments":{}}]},
			{"http_methods":{"GET":2},"invocations":[{"tool":"not_allowed","arguments":{}}]}
		]`),
		json.RawMessage(`[
			{"http_methods":{"GET":1},"invocations":[{"tool":"jira_issue_field_get","arguments":{}}],"extra":true},
			{"http_methods":{"GET":2},"invocations":[{"tool":"jira_issue_field_get","arguments":{}}]}
		]`),
		json.RawMessage(`[
			{"http_methods":{"GET":1}},
			{"http_methods":{"GET":2},"invocations":[{"tool":"jira_issue_field_get","arguments":{}}]}
		]`),
	} {
		spec := valid
		spec.Checks = slices.Clone(valid.Checks)
		spec.Checks[len(spec.Checks)-1].Expected = invalid
		if err := spec.Validate(); err == nil {
			t.Fatalf("invalid MCP route alternatives passed: %s", invalid)
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
		"unconfined provider": func(spec *RunSpec) {
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

// privateLiveQueryOnlyPair builds one reviewed query-only POST scenario shared
// by a paired private-live CLI-skill and internal ATL MCP run spec.
func privateLiveQueryOnlyPair() (RunSpec, RunSpec, Scenario) {
	scenario := validScenario()
	scenario.DataClass = "private-local"
	scenario.RequiredChecks = []string{"answer_correct", "used_atl", "http_observed", "guard_clean", "no_delegation", "atl_succeeded", "methods"}
	scenario.Budgets.MaxRemoteWrites = 2
	scenario.Budgets.MaxDelegations = 0
	scenario.Budgets.MaxBackendRequests = 5
	scenario.Budgets.MaxATLInvocations = 2
	scenario.Budgets.MaxInterfaceInvocations = 2
	scenario.Budgets.MaxEstimatedCostMicroUSD = 10_000_000
	scenario.Budgets.AllowedHTTPMethods = []string{"GET", "POST"}

	routes := map[string][]LiveGatewayRoute{"jira": {
		{Name: "structure_reads", PathPrefix: "/rest/structure/2.0", Methods: []string{"GET"}, MaxRequests: 2},
		{Name: "jira_reads", PathPrefix: "/rest/api/2", Methods: []string{"GET"}, MaxRequests: 1},
		{Name: "value_query", PathPrefix: "/rest/structure/2.0/value", Exact: true, Methods: []string{"POST"},
			QueryOnly: true, MaxRequests: 2, MaxRequestBytes: 1 << 10},
	}}
	checks := []RunCheck{
		{Name: "http_observed", Kind: "http_methods_observed"},
		{Name: "guard_clean", Kind: "guard_no_denials"},
		{Name: "no_delegation", Kind: "delegations_none"},
		{Name: "atl_succeeded", Kind: "atl_all_succeeded"},
		{Name: "methods", Kind: "http_methods_equal", Expected: json.RawMessage(`{"GET":3,"POST":2}`)},
	}
	bind := func(spec *RunSpec) {
		spec.BackendMode = BackendModePrivateLive
		spec.FixtureFile = ""
		spec.Repetitions = 1
		spec.MaxEstimatedCostMicroUSD = scenario.Budgets.MaxEstimatedCostMicroUSD
		spec.AllowedGatewayRoutes = cloneGatewayRoutes(routes)
		spec.GatewayMaxResponseBytes = 1 << 20
		spec.GatewayMaxTotalBytes = 3 << 20
		spec.GatewayMaxRequestBytes = 1 << 10
		spec.GatewayMaxTotalRequestBytes = 2 << 10
		spec.Checks = append(append([]RunCheck(nil), spec.Checks...), checks...)
	}
	cli := validRunSpec()
	cli.Variant = "cli"
	cli.ToolTransport = "cli"
	cli.Surface = SurfaceCLISkill
	cli.AllowedTools = []string{"Bash(atl *)", "Read"}
	cli.AllowedATLCommands = nil
	cli.AllowedCLICommands = validCLICommandPolicy().Rules
	cli.SkillActivation = SkillActivationImplicit
	bind(&cli)

	mcp := validRunSpec()
	mcp.Variant = "mcp"
	mcp.ToolTransport = "mcp"
	mcp.Surface = SurfaceATLMCP
	mcp.AllowedTools = nil
	mcp.AllowedATLCommands = nil
	mcp.AllowedMCPTools = []string{"jira_structure_view"}
	bind(&mcp)
	mcp.Checks = append(mcp.Checks, RunCheck{Name: "used_interface", Kind: "interface_invocations_min", Minimum: 1})
	return cli, mcp, scenario
}

func cloneGatewayRoutes(source map[string][]LiveGatewayRoute) map[string][]LiveGatewayRoute {
	out := make(map[string][]LiveGatewayRoute, len(source))
	for service, routes := range source {
		out[service] = append([]LiveGatewayRoute(nil), routes...)
	}
	return out
}

func TestPrivateLiveQueryOnlyPOSTRequiresReviewedBoundaries(t *testing.T) {
	cli, mcp, scenario := privateLiveQueryOnlyPair()
	for name, spec := range map[string]RunSpec{"cli-skill": cli, "atl-mcp": mcp} {
		t.Run(name, func(t *testing.T) {
			if err := spec.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := spec.ValidateAgainstScenario(scenario); err != nil {
				t.Fatal(err)
			}
		})
	}

	external := mcp
	external.Surface = SurfaceExternalMCP
	if err := validateRunSpecGatewayPolicy(external); err == nil {
		t.Fatal("external MCP accepted a reviewed gateway route policy")
	}

	for name, mutate := range map[string]func(*RunSpec, *Scenario){
		"unmarked post": func(spec *RunSpec, _ *Scenario) {
			spec.AllowedGatewayRoutes["jira"][2].QueryOnly = false
		},
		"live writes": func(spec *RunSpec, _ *Scenario) { spec.AllowLiveWrites = true },
		"legacy schema": func(spec *RunSpec, _ *Scenario) {
			spec.SchemaVersion = LegacyRunSpecSchemaVersion
		},
		"external mcp": func(spec *RunSpec, _ *Scenario) {
			spec.ToolTransport = "mcp"
			spec.Surface = SurfaceExternalMCP
			spec.AllowedTools = nil
			spec.AllowedATLCommands = nil
			spec.AllowedCLICommands = nil
			spec.AllowedMCPTools = []string{"jira_structure_view"}
		},
		"write budget too low":  func(_ *RunSpec, scenario *Scenario) { scenario.Budgets.MaxRemoteWrites = 1 },
		"write budget too high": func(_ *RunSpec, scenario *Scenario) { scenario.Budgets.MaxRemoteWrites = 3 },
		"unreviewed allowed method": func(_ *RunSpec, scenario *Scenario) {
			scenario.Budgets.AllowedHTTPMethods = []string{"GET", "PUT"}
		},
		"missing allowed post": func(_ *RunSpec, scenario *Scenario) {
			scenario.Budgets.AllowedHTTPMethods = []string{"GET"}
		},
		"route budget over backend requests": func(_ *RunSpec, scenario *Scenario) {
			scenario.Budgets.MaxBackendRequests = 2
		},
		"unbounded read route": func(spec *RunSpec, _ *Scenario) {
			spec.AllowedGatewayRoutes["jira"][0].MaxRequests = 0
		},
		"missing exact method oracle": func(spec *RunSpec, scenario *Scenario) {
			spec.Checks = slices.DeleteFunc(append([]RunCheck(nil), spec.Checks...), func(check RunCheck) bool {
				return check.Kind == "http_methods_equal"
			})
			scenario.RequiredChecks = slices.DeleteFunc(append([]string(nil), scenario.RequiredChecks...), func(name string) bool {
				return name == "methods"
			})
		},
		"absent request budgets": func(spec *RunSpec, _ *Scenario) {
			spec.GatewayMaxRequestBytes = 0
			spec.GatewayMaxTotalRequestBytes = 0
		},
		"inconsistent request budgets": func(spec *RunSpec, _ *Scenario) {
			spec.GatewayMaxTotalRequestBytes = spec.GatewayMaxRequestBytes - 1
		},
		"route body over gateway budget": func(spec *RunSpec, _ *Scenario) {
			spec.AllowedGatewayRoutes["jira"][2].MaxRequestBytes = spec.GatewayMaxRequestBytes + 1
		},
		"aggregate query body capacity over total budget": func(spec *RunSpec, _ *Scenario) {
			spec.GatewayMaxTotalRequestBytes = spec.GatewayMaxRequestBytes
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate, _, candidateScenario := privateLiveQueryOnlyPair()
			mutate(&candidate, &candidateScenario)
			if candidate.Validate() == nil && candidate.ValidateAgainstScenario(candidateScenario) == nil {
				t.Fatal("unsafe query-only run spec passed")
			}
		})
	}
}

func TestPrivateLiveReadOnlyRunsWithoutQueryOnlyStayGETAndHEAD(t *testing.T) {
	cli, _, scenario := privateLiveQueryOnlyPair()
	cli.AllowedGatewayRoutes["jira"] = cli.AllowedGatewayRoutes["jira"][:1]
	cli.GatewayMaxRequestBytes = 0
	cli.GatewayMaxTotalRequestBytes = 0
	scenario.Budgets.AllowedHTTPMethods = []string{"GET"}
	scenario.Budgets.MaxRemoteWrites = 0
	if err := cli.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := cli.ValidateAgainstScenario(scenario); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RunSpec, *Scenario){
		"request body budget": func(spec *RunSpec, _ *Scenario) {
			spec.GatewayMaxRequestBytes = 1 << 10
			spec.GatewayMaxTotalRequestBytes = 1 << 10
		},
		"remote write budget": func(_ *RunSpec, scenario *Scenario) { scenario.Budgets.MaxRemoteWrites = 1 },
		"post method": func(_ *RunSpec, scenario *Scenario) {
			scenario.Budgets.AllowedHTTPMethods = []string{"GET", "POST"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cli
			candidate.AllowedGatewayRoutes = cloneGatewayRoutes(cli.AllowedGatewayRoutes)
			candidateScenario := scenario
			mutate(&candidate, &candidateScenario)
			if candidate.Validate() == nil && candidate.ValidateAgainstScenario(candidateScenario) == nil {
				t.Fatal("read-only private-live spec gained non-safe transport")
			}
		})
	}
}

func TestInternalMCPWithoutGatewayRoutesKeepsGuardedTransport(t *testing.T) {
	_, mcp, scenario := privateLiveQueryOnlyPair()
	mcp.AllowedGatewayRoutes = nil
	mcp.GatewayMaxResponseBytes = 0
	mcp.GatewayMaxTotalBytes = 0
	mcp.GatewayMaxRequestBytes = 0
	mcp.GatewayMaxTotalRequestBytes = 0
	mcp.Checks = slices.DeleteFunc(append([]RunCheck(nil), mcp.Checks...), func(check RunCheck) bool {
		return check.Kind == "http_methods_equal"
	})
	scenario.Budgets.AllowedHTTPMethods = []string{"GET"}
	scenario.Budgets.MaxRemoteWrites = 0
	scenario.RequiredChecks = slices.DeleteFunc(append([]string(nil), scenario.RequiredChecks...), func(name string) bool {
		return name == "methods"
	})
	if err := mcp.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := mcp.ValidateAgainstScenario(scenario); err != nil {
		t.Fatal(err)
	}
	if gatewayBackedInternalMCP(mcp) {
		t.Fatal("internal MCP without routes claimed a gateway boundary")
	}
	orphaned := mcp
	orphaned.GatewayMaxResponseBytes = 1 << 20
	orphaned.GatewayMaxTotalBytes = 1 << 20
	if err := orphaned.Validate(); err == nil {
		t.Fatal("internal MCP gateway budgets without routes passed")
	}
}
