package agenteval

import (
	"strings"
	"testing"
)

func TestBuildProviderCommandsAreEphemeralAndReadOnly(t *testing.T) {
	spec := validRunSpec()
	codex, err := BuildProviderCommand(spec, "codex", "/workspace", "/schema", "/final", "", []byte(`{"type":"object"}`))
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
	claude, err := BuildProviderCommand(spec, "claude", "/workspace", "/schema", "/final", "/plugin", []byte(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(claude.Args, " ")
	for _, value := range []string{"--no-session-persistence", "--permission-mode dontAsk", "--setting-sources project", "--max-budget-usd 10.000000", "--tools Bash", "--allowed-tools Bash(atl *)", "--plugin-dir /plugin"} {
		if !strings.Contains(joined, value) {
			t.Errorf("Claude command misses %q: %s", value, joined)
		}
	}
}

func TestParseProviderOutputs(t *testing.T) {
	claude := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use"}]}}`,
		`{"type":"result","num_turns":2,"duration_ms":123,"total_cost_usd":0.25,"usage":{"input_tokens":100,"cache_read_input_tokens":20,"output_tokens":30},"structured_output":{"answer":"ok"}}`,
	}, "\n")
	metrics, final, err := ParseProviderOutput("claude-code", []byte(claude), nil)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.AgentTurns != 2 || metrics.ToolCalls != 1 || metrics.InputTokens != 120 || metrics.OutputTokens != 30 || metrics.EstimatedCostMicroUSD != 250_000 || string(final) != `{"answer":"ok"}` {
		t.Fatalf("metrics=%+v final=%s", metrics, final)
	}
	codex := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"command_execution"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":25,"output_tokens":30}}`,
	}, "\n")
	metrics, final, err = ParseProviderOutput("codex", []byte(codex), []byte(`{"answer":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	if metrics.AgentTurns != 1 || metrics.ToolCalls != 1 || metrics.InputTokens != 100 || metrics.OutputTokens != 30 || string(final) != `{"answer":"ok"}` {
		t.Fatalf("metrics=%+v final=%s", metrics, final)
	}
}
