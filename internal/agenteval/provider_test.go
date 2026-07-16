package agenteval

import (
	"strings"
	"testing"
)

func TestBuildProviderCommandsAreEphemeralAndReadOnly(t *testing.T) {
	spec := validRunSpec()
	codex, err := BuildProviderCommand(spec, "codex", "/atl", "/guard", "/workspace", "/schema", "/final", "", "", []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(codex.Args, " ")
	for _, value := range []string{"exec", "--ephemeral", "--ignore-user-config", "--sandbox read-only", "--output-schema /schema", "--output-last-message /final", `shell_environment_policy.inherit="all"`, "shell_environment_policy.include_only="} {
		if !strings.Contains(joined, value) {
			t.Errorf("Codex command misses %q: %s", value, joined)
		}
	}
	spec.Provider = "claude-code"
	spec.Pricing = Pricing{}
	claude, err := BuildProviderCommand(spec, "claude", "/atl", "/guard", "/workspace", "/schema", "/final", "/plugin", "/settings", []byte(`{"type":"object"}`))
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
	command, err := BuildProviderCommand(spec, "codex", "/opt/atl", "/opt/guard", "/workspace", "/schema", "/final", "", "", []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, " ")
	for _, value := range []string{
		"--dangerously-bypass-hook-trust", `web_search="disabled"`,
		`mcp_servers.atl.command="/opt/atl"`, `mcp_servers.atl.args=["mcp","serve"]`,
		`mcp_servers.atl.required=true`, `mcp_servers.atl.enabled_tools=["jira_fields","jira_epic_digest","confluence_page_section"]`,
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

func TestParseProviderOutputs(t *testing.T) {
	claude := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent"}]}}`,
		`{"type":"result","num_turns":2,"duration_ms":123,"total_cost_usd":0.25,"usage":{"input_tokens":100,"cache_read_input_tokens":20,"output_tokens":30},"modelUsage":{"parent":{"inputTokens":5,"cacheReadInputTokens":40,"cacheCreationInputTokens":10,"outputTokens":7},"child":{"inputTokens":3,"cacheReadInputTokens":20,"cacheCreationInputTokens":2,"outputTokens":11}},"structured_output":{"answer":"ok"}}`,
	}, "\n")
	metrics, final, err := ParseProviderOutput("claude-code", []byte(claude), nil)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.AgentTurns != 2 || metrics.ToolCalls != 1 || metrics.Delegations != 1 || !metrics.Coverage["delegations"] || metrics.MainThreadInputTokens != 120 || metrics.MainThreadOutputTokens != 30 || metrics.InputTokens != 80 || metrics.OutputTokens != 18 || metrics.EstimatedCostMicroUSD != 250_000 || string(final) != `{"answer":"ok"}` {
		t.Fatalf("metrics=%+v final=%s", metrics, final)
	}
	codex := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"error","message":"reviewed invocation warning"}}`,
		`{"type":"item.completed","item":{"type":"command_execution"}}`,
		`{"type":"item.completed","item":{"type":"mcp_tool_call","status":"completed","result":{"fields":[]}}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":25,"output_tokens":30}}`,
	}, "\n")
	metrics, final, err = ParseProviderOutput("codex", []byte(codex), []byte(`{"answer":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	if metrics.AgentTurns != 1 || metrics.ToolCalls != 2 || metrics.MCPToolCalls != 1 || metrics.MCPToolOutputBytes == 0 || metrics.InputTokens != 100 || metrics.MainThreadInputTokens != 100 || metrics.OutputTokens != 30 || metrics.MainThreadOutputTokens != 30 || string(final) != `{"answer":"ok"}` {
		t.Fatalf("metrics=%+v final=%s", metrics, final)
	}
}
