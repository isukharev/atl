package agenteval

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestBuildProviderCommandsAreEphemeralAndReadOnly(t *testing.T) {
	spec := validRunSpec()
	codex, err := BuildProviderCommand(spec, "codex", "/atl", "/guard", "/workspace", "/schema", "/final", "", "", "", ProviderConfinement{}, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(codex.Args, " ")
	if !slices.Contains(codex.Args, `shell_environment_policy.include_only=["PATH","ATL_READ_ONLY","ATL_NO_UPDATE","ATL_CONFIG_DIR","ATL_MIRROR_ROOT","ATL_JIRA_URL","ATL_CONFLUENCE_URL","ATL_JIRA_PAT","ATL_CONFLUENCE_PAT","ATL_ALLOW_INSECURE","ATL_EVAL_REAL_BINARY","ATL_EVAL_COUNTER","ATL_EVAL_ALLOWED_COMMANDS"]`) {
		t.Fatalf("synthetic CLI environment projection drifted: %s", joined)
	}
	for _, value := range []string{"exec", "--ephemeral", "--ignore-user-config", "--sandbox read-only", "--output-schema /schema", "--output-last-message /final", `project_doc_max_bytes=0`, `shell_environment_policy.inherit="all"`, "shell_environment_policy.include_only="} {
		if !strings.Contains(joined, value) {
			t.Errorf("Codex command misses %q: %s", value, joined)
		}
	}
	for _, feature := range []string{"apps", "browser_use", "computer_use", "image_generation", "remote_plugin"} {
		if !containsArgumentPair(codex.Args, "--disable", feature) {
			t.Errorf("Codex command does not disable provider feature %q: %s", feature, joined)
		}
	}
	for _, feature := range []string{"shell_tool", "unified_exec", "multi_agent", "enable_fanout", "plugins"} {
		if containsArgumentPair(codex.Args, "--disable", feature) {
			t.Errorf("Codex command disables reviewed local tool feature %q: %s", feature, joined)
		}
	}
	spec.Provider = "claude-code"
	spec.Pricing = Pricing{}
	originalAllowedTools := slices.Clone(spec.AllowedTools)
	claude, err := BuildProviderCommand(spec, "claude", "/atl", "/guard", "/workspace", "/schema", "/final", "/plugin", "/settings", "", ProviderConfinement{}, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.AllowedTools, originalAllowedTools) {
		t.Fatalf("provider command mutated run spec allowed tools: got %v want %v", spec.AllowedTools, originalAllowedTools)
	}
	joined = strings.Join(claude.Args, " ")
	for _, value := range []string{"--no-session-persistence", "--permission-mode dontAsk", "--setting-sources project", "--max-budget-usd 10.000000", "--tools Bash", "--allowed-tools Bash(atl *),Bash(export ATL_READ_ONLY=1),Bash(command -v atl)", "--plugin-dir /plugin", "--settings /settings"} {
		if !strings.Contains(joined, value) {
			t.Errorf("Claude command misses %q: %s", value, joined)
		}
	}
}

func TestBuildProviderCommandRequiresExternalMCPProxy(t *testing.T) {
	spec := validRunSpec()
	spec.ToolTransport = "mcp"
	spec.Provider = "codex"
	spec.BackendMode = BackendModePrivateLive
	spec.FixtureFile = ""
	spec.Repetitions = 1
	spec.Surface = SurfaceExternalMCP
	spec.AllowedTools = nil
	spec.AllowedATLCommands = nil
	spec.AllowedMCPTools = []string{"jira_fields"}
	_, err := BuildProviderCommand(spec, "codex", "/atl", "/guard", "/workspace", "/schema", "/final", "", "", "/mcp.json", ProviderConfinement{}, []byte(`{"type":"object"}`))
	if err == nil || !strings.Contains(err.Error(), "local proxy") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildCodexMCPCommandIsCredentialIsolatedAndHookGuarded(t *testing.T) {
	spec := validRunSpec()
	spec.ToolTransport = "mcp"
	spec.AllowedTools = nil
	spec.AllowedATLCommands = nil
	spec.AllowedMCPTools = []string{"jira_fields", "jira_epic_digest", "confluence_page_section"}
	command, err := BuildProviderCommand(spec, "codex", "/opt/atl", "/opt/guard", "/workspace", "/schema", "/final", "", "", "", ProviderConfinement{}, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, " ")
	for _, value := range []string{
		"--dangerously-bypass-hook-trust", `web_search="disabled"`,
		`mcp_servers.atl.command="/opt/atl"`, `mcp_servers.atl.args=["mcp","serve"]`,
		`mcp_servers.atl.required=true`, `mcp_servers.atl.enabled_tools=["jira_fields","jira_epic_digest","confluence_page_section"]`,
		`"ATL_EVAL_HTTP_GUARD_FILE"`,
		`default_tools_approval_mode="approve"`, "hooks.PreToolUse=", "/opt/guard",
		`shell_environment_policy.include_only=["PATH","LANG","LC_ALL","TERM"]`,
	} {
		if !strings.Contains(joined, value) {
			t.Errorf("MCP command misses %q: %s", value, joined)
		}
	}
	for _, secretName := range []string{"ATL_JIRA_PAT", "ATL_CONFLUENCE_PAT", "ATL_CONFIG_DIR"} {
		if strings.Contains(joined, `shell_environment_policy.include_only=["PATH","LANG","LC_ALL","TERM","`+secretName) {
			t.Errorf("shell environment exposes %s: %s", secretName, joined)
		}
	}
	for _, feature := range []string{"shell_tool", "unified_exec"} {
		if containsArgumentPair(command.Args, "--enable", feature) {
			t.Errorf("typed MCP command exposes private CLI feature %q: %s", feature, joined)
		}
	}
}

func TestBuildPrivateCodexMCPProjectsOnlyReviewedSkillReadPolicy(t *testing.T) {
	spec := validRunSpec()
	spec.Provider = "codex"
	spec.ToolTransport = "mcp"
	spec.BackendMode = BackendModePrivateLive
	spec.FixtureFile = ""
	spec.Repetitions = 1
	spec.AllowedTools = nil
	spec.AllowedATLCommands = nil
	spec.AllowedMCPTools = []string{"jira_fields"}
	command, err := BuildProviderCommand(spec, "codex", "/opt/atl", "/opt/guard", "/workspace", "/schema", "/final", "", "", "", ProviderConfinement{}, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, " ")
	want := `shell_environment_policy.include_only=["PATH","LANG","LC_ALL","TERM","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
	if !strings.Contains(joined, want) {
		t.Fatalf("private MCP command misses reviewed read policy: %s", joined)
	}
	for _, forbidden := range []string{"ATL_JIRA_PAT", "ATL_CONFLUENCE_PAT", "ATL_CONFIG_DIR", "ATL_EVAL_GUARD_COUNTER"} {
		if strings.Contains(joined, `shell_environment_policy.include_only=["PATH","LANG","LC_ALL","TERM","`+forbidden) {
			t.Fatalf("private MCP shell exposes %s: %s", forbidden, joined)
		}
	}
	spec.Surface = SurfaceExternalMCP
	spec.mcpServerURL = "http://127.0.0.1:1234/mcp"
	spec.mcpBearerTokenEnv = "ATL_EVAL_EXTERNAL_MCP_TOKEN"
	external, err := BuildProviderCommand(spec, "codex", "/opt/atl", "/opt/guard", "/workspace", "/schema", "/final", "", "", "", ProviderConfinement{}, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	externalWant := `shell_environment_policy.include_only=["PATH","LANG","LC_ALL","TERM","NO_PROXY","no_proxy","ATL_EVAL_EXTERNAL_MCP_TOKEN","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
	if !slices.Contains(external.Args, externalWant) {
		t.Fatalf("private external MCP environment projection drifted: %s", strings.Join(external.Args, " "))
	}
}

func TestBuildClaudeMCPCommandDisablesBuiltinsAndUsesQualifiedAllowlist(t *testing.T) {
	spec := validRunSpec()
	spec.Provider = "claude-code"
	spec.Pricing = Pricing{}
	spec.ToolTransport = "mcp"
	spec.AllowedTools = nil
	spec.AllowedATLCommands = nil
	spec.AllowedMCPTools = []string{"jira_fields", "jira_epic_digest"}
	command, err := BuildProviderCommand(spec, "claude", "/opt/atl", "/opt/guard", "/workspace", "/schema", "/final", "/plugin", "/settings", "/mcp.json", ProviderConfinement{}, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, " ")
	for _, value := range []string{"--mcp-config /mcp.json", "--plugin-dir /plugin", "--settings /settings"} {
		if !strings.Contains(joined, value) {
			t.Errorf("Claude MCP command misses %q: %s", value, joined)
		}
	}
	if !slices.Contains(command.Args, "--strict-mcp-config") {
		t.Errorf("Claude MCP command is not strict: %s", joined)
	}
	tools, toolsOK := providerArgument(command.Args, "--tools")
	allowed, allowedOK := providerArgument(command.Args, "--allowed-tools")
	if toolsOK || allowedOK {
		t.Errorf("Claude MCP tool boundary tools=%q allowed=%q: %s", tools, allowed, joined)
	}
}

func TestBuildPrivateCLIProviderCommandsEnforceHooksAndCodexCommandBroker(t *testing.T) {
	for _, provider := range []string{"claude-code", "codex"} {
		t.Run(provider, func(t *testing.T) {
			spec := validRunSpec()
			spec.BackendMode = BackendModePrivateLive
			spec.FixtureFile = ""
			spec.Repetitions = 1
			spec.Provider = provider
			spec.ToolTransport = "cli"
			spec.AllowedTools = []string{"Bash(atl *)", "Read", "Skill"}
			spec.AllowedATLCommands = nil
			spec.AllowedCLICommands = validCLICommandPolicy().Rules
			spec.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": {{Name: "jira_api", PathPrefix: "/rest/api/2"}}}
			spec.GatewayMaxResponseBytes = 1 << 20
			spec.GatewayMaxTotalBytes = 2 << 20
			if provider == "claude-code" {
				spec.Pricing = Pricing{}
			}
			confinement := ProviderConfinement{}
			if provider == "codex" {
				confinement.RequestDirectory = "/private/requests"
				confinement.ResponseDirectory = "/private/responses"
			}
			command, err := BuildProviderCommand(spec, provider, "/opt/atl", "/opt/guard", "/workspace", "/schema", "/final", "/plugin", "/settings", "", confinement, []byte(`{"type":"object"}`))
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(command.Args, " ")
			if provider == "claude-code" {
				for _, value := range []string{"--permission-mode dontAsk", "--tools Bash,Read,Skill", "--allowed-tools Bash(atl *),Read,Skill,Bash(export ATL_READ_ONLY=1),Bash(command -v atl)", "--settings /settings"} {
					if !strings.Contains(joined, value) {
						t.Errorf("Claude private CLI command misses %q: %s", value, joined)
					}
				}
				if sources, ok := providerArgument(command.Args, "--setting-sources"); !ok || sources != "" {
					t.Errorf("Claude private CLI loaded ambient setting sources %q: %s", sources, joined)
				}
				return
			}
			for _, value := range []string{
				"--ignore-rules", "--dangerously-bypass-hook-trust",
				`approval_policy="never"`, `web_search="disabled"`,
				`plugins."atl@atl".enabled=true`,
				`developer_instructions=` + strconv.Quote(codexPrivateCLIInstructions(spec)),
				`default_permissions="atl_agent_eval"`, `permissions.atl_agent_eval.extends=":workspace"`,
				`permissions.atl_agent_eval.filesystem={"/private/requests"="write","/private/responses"="read"}`,
				"hooks.PreToolUse=", "/opt/guard", `"SHELL"`, `"ATL_EVAL_CLI_POLICY_FILE"`, `"ATL_EVAL_GUARD_MODE"`,
				`"ATL_EVAL_COMMAND_BROKER_FILE"`, `project_doc_max_bytes=0`,
			} {
				if !strings.Contains(joined, value) {
					t.Errorf("Codex private CLI command misses %q: %s", value, joined)
				}
			}
			if !slices.Contains(command.Args, `shell_environment_policy.include_only=["PATH","SHELL","LANG","LC_ALL","TERM","ATL_READ_ONLY","ATL_EVAL_COUNTER","ATL_EVAL_GUARD_COUNTER","ATL_EVAL_CLI_POLICY_FILE","ATL_EVAL_COMMAND_BROKER_FILE","ATL_EVAL_GUARD_MODE","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`) {
				t.Fatalf("private CLI environment projection drifted: %s", joined)
			}
			if slices.Contains(command.Args, "--ignore-user-config") {
				t.Errorf("Codex private CLI ignored its fresh isolated installed-plugin config: %s", joined)
			}
			for _, feature := range []string{"shell_tool", "unified_exec"} {
				if !containsArgumentPair(command.Args, "--enable", feature) {
					t.Errorf("Codex private CLI command does not enable %q: %s", feature, joined)
				}
			}
			for _, forbidden := range []string{`"ATL_JIRA_PAT"`, `"ATL_CONFLUENCE_PAT"`, `"ATL_JIRA_URL"`, `"ATL_CONFLUENCE_URL"`} {
				if strings.Contains(joined, forbidden) {
					t.Errorf("Codex subprocess environment includes %s: %s", forbidden, joined)
				}
			}
			for _, forbidden := range []string{"--sandbox workspace-write", `sandbox_workspace_write.network_access=true`, `features.network_proxy.enabled=false`, `network.enabled=true`, `unix_sockets=`, `dangerously_allow_all_unix_sockets=true`, `"*"="allow"`} {
				if strings.Contains(joined, forbidden) {
					t.Errorf("Codex private CLI command weakens confinement with %q: %s", forbidden, joined)
				}
			}
		})
	}
}

func TestCodexPrivateCLIInstructionsAreScopedToPrivateCLI(t *testing.T) {
	privateSpec := validRunSpec()
	privateSpec.BackendMode = BackendModePrivateLive
	privateSpec.FixtureFile = ""
	privateSpec.Repetitions = 1
	privateSpec.Provider = "codex"
	privateSpec.Category = BenchmarkCategoryNeutralCommon
	privateSpec.ToolTransport = "cli"
	privateSpec.AllowedTools = []string{"Bash(atl *)", "Read", "Skill"}
	privateSpec.AllowedATLCommands = nil
	privateSpec.AllowedCLICommands = validCLICommandPolicy().Rules
	privateSpec.AllowedGatewayRoutes = map[string][]LiveGatewayRoute{"jira": {{Name: "jira_api", PathPrefix: "/rest/api/2"}}}
	privateSpec.GatewayMaxResponseBytes = 1 << 20
	privateSpec.GatewayMaxTotalBytes = 2 << 20
	privateSpec.DataCapabilities = []string{"jira.epic.digest"}
	confinement := ProviderConfinement{RequestDirectory: "/private/requests", ResponseDirectory: "/private/responses"}
	command, err := BuildProviderCommand(privateSpec, "codex", "/opt/atl", "/opt/guard", "/workspace", "/schema", "/final", "", "", "", confinement, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	instructions := codexPrivateCLIInstructions(privateSpec)
	exactSetting := `developer_instructions=` + strconv.Quote(instructions)
	if !slices.Contains(command.Args, exactSetting) {
		t.Fatalf("private CLI command misses exact operational instruction: %v", command.Args)
	}
	for _, required := range []string{
		"This is an evidence task",
		"select and follow the installed $atl:jira skill implied by the reviewed data capabilities",
		"use the literal atl executable through the shell tool to retrieve the evidence required for the answer",
		"minimum necessary invocation or invocations allowed by the reviewed command policy",
		"Base the answer on the returned evidence",
		"a no-tool answer or an answer based on assumptions is invalid",
		"Never use apply_patch, Edit, Write",
		"return the failure through the required response schema",
	} {
		if !strings.Contains(instructions, required) {
			t.Errorf("private CLI operational instruction misses %q: %s", required, instructions)
		}
	}
	privateMCP := privateSpec
	privateMCP.Surface = SurfaceATLMCP
	privateMCP.Category = ""
	privateMCP.DataCapabilities = nil
	privateMCP.ToolTransport = "mcp"
	privateMCP.AllowedTools = nil
	privateMCP.AllowedCLICommands = nil
	privateMCP.AllowedMCPTools = []string{"jira_fields"}
	privateMCP.AllowedGatewayRoutes = nil
	privateMCP.GatewayMaxResponseBytes = 0
	privateMCP.GatewayMaxTotalBytes = 0
	privateClaudeCLI := privateSpec
	privateClaudeCLI.Provider = "claude-code"
	privateClaudeCLI.Pricing = Pricing{}

	for name, spec := range map[string]RunSpec{
		"private-claude-cli": privateClaudeCLI,
		"synthetic-cli":      validRunSpec(),
		"synthetic-mcp": func() RunSpec {
			value := validRunSpec()
			value.ToolTransport = "mcp"
			value.AllowedTools = nil
			value.AllowedATLCommands = nil
			value.AllowedMCPTools = []string{"jira_fields"}
			return value
		}(),
		"private-mcp": privateMCP,
	} {
		t.Run(name, func(t *testing.T) {
			built, err := BuildProviderCommand(spec, "codex", "/opt/atl", "/opt/guard", "/workspace", "/schema", "/final", "", "", "", ProviderConfinement{}, []byte(`{"type":"object"}`))
			if err != nil {
				t.Fatal(err)
			}
			for _, arg := range built.Args {
				if strings.HasPrefix(arg, "developer_instructions=") {
					t.Fatalf("non-private-CLI command received private operational instruction: %v", built.Args)
				}
			}
		})
	}
}

func TestCodexPrivateCLISkillRouteUsesOnlyReviewedCapabilityFamilies(t *testing.T) {
	for _, test := range []struct {
		name         string
		capabilities []string
		expected     string
	}{
		{name: "generic", capabilities: nil, expected: "select and follow the installed task-matching skill"},
		{name: "unknown", capabilities: []string{"knowledge.search"}, expected: "select and follow the installed task-matching skill"},
		{name: "lookalike-prefixes", capabilities: []string{"confluence2.page", "jira-extra.issue", "jirassic.issue"}, expected: "select and follow the installed task-matching skill"},
		{name: "jira", capabilities: []string{"jira.issue.field"}, expected: "select and follow the installed $atl:jira skill implied by the reviewed data capabilities"},
		{name: "confluence", capabilities: []string{"confluence.page.section"}, expected: "select and follow the installed $atl:confluence skill implied by the reviewed data capabilities"},
		{name: "mixed-deduplicated", capabilities: []string{"confluence.page", "jira.issue", "JIRA.ISSUE.FIELD", "other"}, expected: "select and follow the installed $atl:jira and $atl:confluence skills implied by the reviewed data capabilities"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if actual := codexPrivateCLISkillRoute(test.capabilities); actual != test.expected {
				t.Fatalf("route=%q want=%q", actual, test.expected)
			}
		})
	}
}

func TestBuildCodexConfinementProbeUsesTheSameExactFilesystemPolicy(t *testing.T) {
	confinement := ProviderConfinement{RequestDirectory: "/private/requests", ResponseDirectory: "/private/responses"}
	command, err := BuildCodexConfinementProbeCommand("codex", "/workspace", "/probe", confinement)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, " ")
	for _, value := range []string{
		"sandbox -P atl_agent_eval", `permissions.atl_agent_eval.extends=":workspace"`,
		`permissions.atl_agent_eval.filesystem={"/private/requests"="write","/private/responses"="read"}`,
		"-C /workspace /probe",
	} {
		if !strings.Contains(joined, value) {
			t.Errorf("probe command misses %q: %s", value, joined)
		}
	}
	for _, forbidden := range []string{"default_permissions=", `"*"="allow"`, "dangerously_", "network_access=true", "network.enabled", "unix_sockets"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("probe command weakens confinement with %q: %s", forbidden, joined)
		}
	}
	for _, invalid := range []ProviderConfinement{
		{},
		{RequestDirectory: "relative", ResponseDirectory: "/private/responses"},
		{RequestDirectory: "/private/same", ResponseDirectory: "/private/same"},
	} {
		if _, err := BuildCodexConfinementProbeCommand("codex", "/workspace", "/probe", invalid); err == nil {
			t.Fatalf("unsafe broker directories passed: %+v", invalid)
		}
	}
}

func providerArgument(args []string, name string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1], true
		}
	}
	return "", false
}

func containsArgumentPair(args []string, name, value string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name && args[index+1] == value {
			return true
		}
	}
	return false
}

func TestParseProviderOutputs(t *testing.T) {
	claude := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"skill-1","name":"Skill","input":{"skill":"atl:jira"}},{"type":"tool_use","id":"agent-1","name":"Agent"},{"type":"tool_use","id":"mcp-1","name":"mcp__atl__jira_fields"}]}}`,
		`{"type":"user","tool_use_result":{"content":[{"type":"text","text":"synthetic"}]},"message":{"content":[{"type":"tool_result","tool_use_id":"mcp-1","is_error":false,"content":"{\"x\":1}"}]}}`,
		`{"type":"result","num_turns":2,"duration_ms":123,"total_cost_usd":0.25,"usage":{"input_tokens":100,"cache_read_input_tokens":20,"output_tokens":30},"modelUsage":{"parent":{"inputTokens":5,"cacheReadInputTokens":40,"cacheCreationInputTokens":10,"outputTokens":7},"child":{"inputTokens":3,"cacheReadInputTokens":20,"cacheCreationInputTokens":2,"outputTokens":11}},"structured_output":{"answer":"ok"}}`,
	}, "\n")
	metrics, final, err := ParseProviderOutput("claude-code", []byte(claude), nil)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.AgentTurns != 2 || metrics.ToolCalls != 3 || metrics.SkillToolCalls != 1 || metrics.SkillToolCallsByName["atl:jira"] != 1 || metrics.Delegations != 1 || metrics.MCPToolCalls != 1 || metrics.FailedMCPToolCalls != 0 || metrics.MCPToolOutputBytes != 7 || !metrics.CapabilityFamilyCoverage || len(metrics.CapabilityFamilies) != 1 || metrics.CapabilityFamilies[0].Family != "jira.fields" || metrics.CapabilityFamilies[0].OutputBytes != 7 || !metrics.Coverage["delegations"] || metrics.MainThreadInputTokens != 120 || metrics.MainThreadOutputTokens != 30 || metrics.InputTokens != 80 || metrics.OutputTokens != 18 || metrics.EstimatedCostMicroUSD != 250_000 || string(final) != `{"answer":"ok"}` {
		t.Fatalf("metrics=%+v final=%s", metrics, final)
	}
	codex := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"error","message":"reviewed invocation warning"}}`,
		`{"type":"item.completed","item":{"type":"command_execution"}}`,
		`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"atl","tool":"jira_fields","status":"completed","result":{"fields":[]}}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":25,"output_tokens":30}}`,
	}, "\n")
	metrics, final, err = ParseProviderOutput("codex", []byte(codex), []byte(`{"answer":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	if metrics.AgentTurns != 1 || metrics.ToolCalls != 2 || metrics.MCPToolCalls != 1 || metrics.MCPToolOutputBytes == 0 || !metrics.CapabilityFamilyCoverage || len(metrics.CapabilityFamilies) != 1 || metrics.CapabilityFamilies[0].Family != "jira.fields" || metrics.InputTokens != 100 || metrics.MainThreadInputTokens != 100 || metrics.OutputTokens != 30 || metrics.MainThreadOutputTokens != 30 || string(final) != `{"answer":"ok"}` {
		t.Fatalf("metrics=%+v final=%s", metrics, final)
	}
}

func TestClaudeClientSideMissingToolIsNotCountedAsATLInvocation(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"mcp-1","name":"mcp__atl__jira_fields"}]}}`,
		`{"type":"user","tool_use_result":"Error: No such tool available: mcp__atl__jira_fields","message":{"content":[{"type":"tool_result","tool_use_id":"mcp-1","is_error":true,"content":"synthetic client error"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"mcp-2","name":"mcp__atl__jira_fields"}]}}`,
		`{"type":"user","tool_use_result":{"isError":true},"message":{"content":[{"type":"tool_result","tool_use_id":"mcp-2","is_error":true,"content":"server error"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"mcp-3","name":"mcp__atl__jira_fields"}]}}`,
		`{"type":"user","tool_use_result":"Error: {\"kind\":\"not_found\",\"remediation\":\"verify_identifier_or_access\",\"message\":\"not found\"}","message":{"content":[{"type":"tool_result","tool_use_id":"mcp-3","is_error":true,"content":"classified server error"}]}}`,
		`{"type":"result","num_turns":1,"duration_ms":1,"total_cost_usd":0,"usage":{"input_tokens":1,"output_tokens":1},"structured_output":{"answer":"ok"}}`,
	}, "\n")
	metrics, _, err := ParseProviderOutput("claude-code", []byte(transcript), nil)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.ToolCalls != 3 || metrics.MCPToolCalls != 2 || metrics.FailedMCPToolCalls != 2 || metrics.MCPToolOutputBytes != int64(len("server error")+len("classified server error")) {
		t.Fatalf("metrics=%+v", metrics)
	}
}

func TestClaudeUnknownMCPResultShapeFailsClosed(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"mcp-1","name":"mcp__atl__jira_fields"}]}}`,
		`{"type":"user","tool_use_result":"unexpected provider value","message":{"content":[{"type":"tool_result","tool_use_id":"mcp-1","content":"synthetic"}]}}`,
		`{"type":"result","structured_output":{"answer":"ok"}}`,
	}, "\n")
	if _, _, err := ParseProviderOutput("claude-code", []byte(transcript), nil); err == nil || !strings.Contains(err.Error(), "unsupported client-side shape") {
		t.Fatalf("err=%v", err)
	}
}

func TestUnknownMCPToolSuppressesCapabilityAttribution(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"atl","tool":"synthetic_sensitive_lookup","status":"completed","result":{"value":"SYNTHETIC-SENSITIVE-123"}}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}, "\n")
	metrics, _, err := ParseProviderOutput("codex", []byte(transcript), []byte(`{"answer":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	if metrics.CapabilityFamilyCoverage || len(metrics.CapabilityFamilies) != 0 || metrics.MCPToolCalls != 1 {
		t.Fatalf("metrics=%+v", metrics)
	}
	encoded, _ := json.Marshal(metrics.CapabilityFamilies)
	if strings.Contains(string(encoded), "SYNTHETIC-SENSITIVE") {
		t.Fatalf("leaked attribution: %s", encoded)
	}
}
