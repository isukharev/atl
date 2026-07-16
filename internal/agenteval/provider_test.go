package agenteval

import (
	"encoding/json"
	"slices"
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
	for _, value := range []string{"exec", "--ephemeral", "--ignore-user-config", "--sandbox read-only", "--output-schema /schema", "--output-last-message /final", `project_doc_max_bytes=0`, `shell_environment_policy.inherit="all"`, "shell_environment_policy.include_only="} {
		if !strings.Contains(joined, value) {
			t.Errorf("Codex command misses %q: %s", value, joined)
		}
	}
	spec.Provider = "claude-code"
	spec.Pricing = Pricing{}
	claude, err := BuildProviderCommand(spec, "claude", "/atl", "/guard", "/workspace", "/schema", "/final", "/plugin", "/settings", "", ProviderConfinement{}, []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(claude.Args, " ")
	for _, value := range []string{"--no-session-persistence", "--permission-mode dontAsk", "--setting-sources project", "--max-budget-usd 10.000000", "--tools Bash", "--allowed-tools Bash(atl *)", "--plugin-dir /plugin", "--settings /settings"} {
		if !strings.Contains(joined, value) {
			t.Errorf("Claude command misses %q: %s", value, joined)
		}
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
				for _, value := range []string{"--permission-mode dontAsk", "--tools Bash,Read,Skill", "--allowed-tools Bash(atl *),Read,Skill", "--settings /settings"} {
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
				`default_permissions="atl_agent_eval"`, `permissions.atl_agent_eval.extends=":workspace"`,
				`permissions.atl_agent_eval.filesystem={"/private/requests"="write","/private/responses"="read"}`,
				"hooks.PreToolUse=", "/opt/guard", `"ATL_EVAL_CLI_POLICY_FILE"`, `"ATL_EVAL_GUARD_MODE"`,
				`"ATL_EVAL_COMMAND_BROKER_FILE"`, `project_doc_max_bytes=0`,
			} {
				if !strings.Contains(joined, value) {
					t.Errorf("Codex private CLI command misses %q: %s", value, joined)
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

func TestParseProviderOutputs(t *testing.T) {
	claude := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"agent-1","name":"Agent"},{"type":"tool_use","id":"mcp-1","name":"mcp__atl__jira_fields"}]}}`,
		`{"type":"user","tool_use_result":{"content":[{"type":"text","text":"synthetic"}]},"message":{"content":[{"type":"tool_result","tool_use_id":"mcp-1","is_error":false,"content":"{\"x\":1}"}]}}`,
		`{"type":"result","num_turns":2,"duration_ms":123,"total_cost_usd":0.25,"usage":{"input_tokens":100,"cache_read_input_tokens":20,"output_tokens":30},"modelUsage":{"parent":{"inputTokens":5,"cacheReadInputTokens":40,"cacheCreationInputTokens":10,"outputTokens":7},"child":{"inputTokens":3,"cacheReadInputTokens":20,"cacheCreationInputTokens":2,"outputTokens":11}},"structured_output":{"answer":"ok"}}`,
	}, "\n")
	metrics, final, err := ParseProviderOutput("claude-code", []byte(claude), nil)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.AgentTurns != 2 || metrics.ToolCalls != 2 || metrics.Delegations != 1 || metrics.MCPToolCalls != 1 || metrics.FailedMCPToolCalls != 0 || metrics.MCPToolOutputBytes != 7 || !metrics.CapabilityFamilyCoverage || len(metrics.CapabilityFamilies) != 1 || metrics.CapabilityFamilies[0].Family != "jira.fields" || metrics.CapabilityFamilies[0].OutputBytes != 7 || !metrics.Coverage["delegations"] || metrics.MainThreadInputTokens != 120 || metrics.MainThreadOutputTokens != 30 || metrics.InputTokens != 80 || metrics.OutputTokens != 18 || metrics.EstimatedCostMicroUSD != 250_000 || string(final) != `{"answer":"ok"}` {
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
		`{"type":"result","num_turns":1,"duration_ms":1,"total_cost_usd":0,"usage":{"input_tokens":1,"output_tokens":1},"structured_output":{"answer":"ok"}}`,
	}, "\n")
	metrics, _, err := ParseProviderOutput("claude-code", []byte(transcript), nil)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.ToolCalls != 2 || metrics.MCPToolCalls != 1 || metrics.FailedMCPToolCalls != 1 || metrics.MCPToolOutputBytes != int64(len("server error")) {
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
