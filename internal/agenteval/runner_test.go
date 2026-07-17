package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunHeadlessWithFakeProvidersUsesPrivateWrapperAndSyntheticMetrics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tempRepository := t.TempDir()
	if err := exec.Command("git", "-C", tempRepository, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(tempRepository, ".gitignore"), "private/\n", 0o600)
	outputRoot := filepath.Join(tempRepository, "private", "runs")

	caseDir := filepath.Join(tempRepository, "case")
	if err := os.MkdirAll(filepath.Join(caseDir, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(caseDir, "workspace", "README.md"), "synthetic workspace\n", 0o600)
	scenario := validScenario()
	scenario.ID = "jira.synthetic-model"
	scenario.RequiredChecks = []string{"answer_correct", "atl_succeeded", "mock_clean", "used_atl"}
	scenario.RequiredMetrics = []string{
		"agent_turns", "tool_calls", "interface_invocations", "backend_requests",
		"output_bytes", "input_tokens", "output_tokens",
		"estimated_cost_microusd", "duration_millis",
	}
	scenario.Budgets = Budgets{
		MaxAgentTurns: 2, MaxToolCalls: 2, MaxInterfaceInvocations: 2,
		MaxBackendRequests: 0, MaxRemoteWrites: 0, MaxOutputBytes: 4096,
		MaxInputTokens: 1000, MaxOutputTokens: 1000,
		MaxMainThreadInputTokens: 1000, MaxMainThreadOutputTokens: 1000,
		MaxEstimatedCostMicroUSD: 10_000_000, MaxDurationMillis: 30_000,
		AllowedHTTPMethods: []string{"GET"},
	}
	writeJSONTestFile(t, filepath.Join(caseDir, "scenario.json"), scenario)
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{Method: "GET", Path: "/jira/rest/api/2/field", Status: 200, Body: []byte(`[]`)}},
	}
	writeJSONTestFile(t, filepath.Join(caseDir, "fixture.json"), fixture)
	writeTestFile(t, filepath.Join(caseDir, "prompt.md"), "Use atl and return the requested JSON.\n", 0o600)
	writeTestFile(t, filepath.Join(caseDir, "response.json"), `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`, 0o600)
	rubric := Rubric{SchemaVersion: 1, ID: "synthetic-answer", ScenarioID: scenario.ID, MinimumScoreBPS: 6000, Criteria: []RubricCriterion{{ID: "usefulness", Description: "The answer is useful.", Maximum: 4, Minimum: 2, Weight: 1}}, AllowedFindingIDs: []string{"unclear"}}
	writeJSONTestFile(t, filepath.Join(caseDir, "rubric.json"), rubric)
	spec := RunSpec{
		SchemaVersion: RunSpecSchemaVersion, ScenarioFile: "scenario.json", Provider: "claude-code",
		Variant: "baseline", Model: "claude-test-1", PromptFile: "prompt.md",
		ResponseSchemaFile: "response.json", QualitativeRubricFile: "rubric.json", WorkspaceTemplate: "workspace",
		FixtureFile: "fixture.json", Repetitions: 1, TimeoutSeconds: 30,
		MaxEstimatedCostMicroUSD: 10_000_000,
		Pricing:                  Pricing{},
		AllowedTools:             []string{"Bash(atl *)", "Skill"},
		AllowedATLCommands:       []string{"atl version"},
		Checks: []RunCheck{
			{Name: "answer_correct", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`"ok"`)},
			{Name: "atl_succeeded", Kind: "interface_all_succeeded"},
			{Name: "mock_clean", Kind: "mock_no_unexpected"},
			{Name: "used_atl", Kind: "interface_invocations_min", Minimum: 1},
		},
	}
	writeJSONTestFile(t, filepath.Join(caseDir, "run.json"), spec)

	pluginRoot := filepath.Join(tempRepository, "plugin")
	if err := os.MkdirAll(filepath.Join(pluginRoot, ".claude-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pluginRoot, "skills", "atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(pluginRoot, ".claude-plugin", "plugin.json"), `{"version":"0.4.0"}`, 0o600)
	writeTestFile(t, filepath.Join(pluginRoot, "skills", "atl", "SKILL.md"), "---\nname: atl\n---\nSynthetic skill.\n", 0o600)

	fakeAgent := filepath.Join(tempRepository, "fake-agent")
	writeTestFile(t, fakeAgent, `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo fake-agent-1
  exit 0
fi
if [ "$1" = "-p" ]; then
  mcp=0
  for arg in "$@"; do
    if [ "$arg" = "--mcp-config" ]; then
      mcp=1
    fi
  done
  if [ "$mcp" = "1" ]; then
    if [ -n "$ATL_JIRA_PAT" ] || [ -n "$ATL_CONFLUENCE_PAT" ]; then
      echo synthetic backend credentials leaked into the provider environment >&2
      exit 33
    fi
    printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"mcp-1","name":"mcp__atl__jira_fields"}]}}'
    printf '%s\n' '{"type":"user","tool_use_result":{"content":[{"type":"text","text":"synthetic"}]},"message":{"content":[{"type":"tool_result","tool_use_id":"mcp-1","is_error":false,"content":"{\"fields\":[]}"}]}}'
    printf '%s\n' '{"type":"result","num_turns":1,"duration_ms":10,"total_cost_usd":0.00014,"usage":{"input_tokens":100,"output_tokens":20},"structured_output":{"answer":"ok"}}'
    exit 0
  fi
  atl version >/dev/null
  printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use"}]}}'
  printf '%s\n' '{"type":"result","num_turns":1,"duration_ms":10,"total_cost_usd":0.00014,"usage":{"input_tokens":100,"output_tokens":20},"structured_output":{"answer":"ok"}}'
  exit 0
fi
final=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then
    final="$2"
    shift 2
    continue
  fi
  shift
done
printf '%s\n' '{"type":"item.completed","item":{"type":"mcp_tool_call","server":"atl","tool":"jira_fields","status":"completed","result":{"fields":[]}}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20}}'
printf '%s\n' '{"answer":"ok"}' >"$final"
`, 0o700)
	fakeATL := filepath.Join(tempRepository, "fake-atl")
	writeTestFile(t, fakeATL, `#!/bin/sh
if [ "$1" = "version" ]; then
  printf '%s\n' '{"version":"0.4.0","commit":"test","build_state":"clean"}'
  exit 0
fi
exit 2
`, 0o700)
	wrapper := filepath.Join(tempRepository, "agent-eval")
	build := exec.Command("go", "build", "-o", wrapper, "./scripts/agent-eval")
	build.Dir = repositoryRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build wrapper: %v\n%s", err, output)
	}

	output, err := RunHeadless(context.Background(), RunOptions{
		SpecPath: filepath.Join(caseDir, "run.json"), OutputRoot: outputRoot,
		RepositoryRoot: tempRepository, AgentBinary: fakeAgent, ATLBinary: fakeATL,
		PluginRoot: pluginRoot, WrapperExecutable: wrapper,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != 1 || output.Results[0].Status != "pass" {
		t.Fatalf("output=%+v", output)
	}
	if output.Preview.Command.Path != "claude" {
		t.Fatalf("preview command path=%q", output.Preview.Command.Path)
	}
	result := output.Results[0]
	if result.Metrics.ATLInvocations != 0 || result.Metrics.InterfaceInvocations != 1 || result.Metrics.BackendRequests != 0 || result.Metrics.EstimatedCostMicroUSD != 140 {
		t.Fatalf("metrics=%+v", result.Metrics)
	}
	transcript := filepath.Join(outputRoot, scenario.ID, "claude-code", "baseline", "run-01", "transcript.jsonl")
	info, err := os.Stat(transcript)
	if err != nil || info.Mode().Perm() != 0o600 {
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("transcript mode=%v", info.Mode())
	}
	cliSettings, err := os.ReadFile(filepath.Join(outputRoot, scenario.ID, "claude-code", "baseline", "run-01", "claude-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cliSettings, []byte("enabledMcpjsonServers")) {
		t.Fatalf("CLI run unexpectedly approves MCP servers: %s", cliSettings)
	}

	spec.Variant = "typed-mcp"
	spec.ToolTransport = "mcp"
	spec.AllowedTools = nil
	spec.AllowedATLCommands = nil
	spec.AllowedMCPTools = []string{"jira_fields"}
	writeJSONTestFile(t, filepath.Join(caseDir, "run.json"), spec)
	output, err = RunHeadless(context.Background(), RunOptions{
		SpecPath: filepath.Join(caseDir, "run.json"), OutputRoot: outputRoot,
		RepositoryRoot: tempRepository, AgentBinary: fakeAgent, ATLBinary: fakeATL,
		PluginRoot: pluginRoot, WrapperExecutable: wrapper,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != 1 || output.Results[0].Status != "pass" {
		t.Fatalf("claude MCP output=%+v", output)
	}
	result = output.Results[0]
	if result.Metrics.ATLInvocations != 0 || result.Metrics.InterfaceInvocations != 1 || result.Metrics.ToolCalls != 1 || result.Metrics.EstimatedCostMicroUSD != 140 {
		t.Fatalf("claude MCP metrics=%+v", result.Metrics)
	}
	if !result.Coverage["capability_families"] || len(result.CapabilityFamilies) != 1 || result.CapabilityFamilies[0].Family != "jira.fields" {
		t.Fatalf("families=%+v coverage=%+v", result.CapabilityFamilies, result.Coverage)
	}
	mcpConfigPath := filepath.Join(outputRoot, scenario.ID, "claude-code", "typed-mcp", "run-01", "claude-mcp.json")
	mcpConfigInfo, err := os.Stat(mcpConfigPath)
	if err != nil || mcpConfigInfo.Mode().Perm() != 0o600 {
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("MCP config mode=%v", mcpConfigInfo.Mode())
	}
	var mcpConfig struct {
		Servers map[string]struct {
			Command    string            `json:"command"`
			Args       []string          `json:"args"`
			Env        map[string]string `json:"env"`
			AlwaysLoad bool              `json:"alwaysLoad"`
		} `json:"mcpServers"`
	}
	mcpConfigData, err := os.ReadFile(mcpConfigPath)
	if err != nil || json.Unmarshal(mcpConfigData, &mcpConfig) != nil {
		t.Fatalf("read MCP config: %v", err)
	}
	server := mcpConfig.Servers["atl"]
	configuredATL, configuredErr := filepath.EvalSymlinks(server.Command)
	wantATL, wantErr := filepath.EvalSymlinks(fakeATL)
	if configuredErr != nil || wantErr != nil || configuredATL != wantATL || len(server.Args) != 2 || server.Args[0] != "mcp" || server.Args[1] != "serve" || server.Env["ATL_READ_ONLY"] != "1" || server.Env["ATL_JIRA_PAT"] != "synthetic-jira-token" || !server.AlwaysLoad {
		t.Fatalf("MCP config is not bound to the reviewed child: %+v", server)
	}
	settingsData, err := os.ReadFile(filepath.Join(outputRoot, scenario.ID, "claude-code", "typed-mcp", "run-01", "claude-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Enabled     []string `json:"enabledMcpjsonServers"`
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(settingsData, &settings); err != nil || len(settings.Enabled) != 1 || settings.Enabled[0] != "atl" || len(settings.Permissions.Allow) != 1 || settings.Permissions.Allow[0] != "mcp__atl__jira_fields" {
		t.Fatalf("MCP approval settings=%s err=%v", settingsData, err)
	}
	if bytes.Contains(settingsData, []byte(`"matcher"`)) {
		t.Fatalf("MCP guard must omit matcher to cover every non-MCP tool: %s", settingsData)
	}

	spec.Provider = "codex"
	spec.Variant = "typed-mcp-codex"
	spec.Model = "gpt-test-1"
	spec.Pricing = Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000}
	spec.AllowedTools = nil
	spec.AllowedATLCommands = nil
	spec.AllowedMCPTools = []string{"jira_fields"}
	writeJSONTestFile(t, filepath.Join(caseDir, "run.json"), spec)
	output, err = RunHeadless(context.Background(), RunOptions{
		SpecPath: filepath.Join(caseDir, "run.json"), OutputRoot: outputRoot,
		RepositoryRoot: tempRepository, AgentBinary: fakeAgent, ATLBinary: fakeATL,
		PluginRoot: pluginRoot, WrapperExecutable: wrapper,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != 1 || output.Results[0].Status != "pass" {
		t.Fatalf("codex output=%+v", output)
	}
	result = output.Results[0]
	if result.Metrics.ATLInvocations != 0 || result.Metrics.InterfaceInvocations != 1 || result.Metrics.ToolCalls != 1 || result.Metrics.EstimatedCostMicroUSD != 140 {
		t.Fatalf("codex metrics=%+v", result.Metrics)
	}
}

func TestCommittedHeadlessRunSpecs(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "benchmarks", "agent-eval", "*", "run.*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no committed agent run specs")
	}
	for _, path := range paths {
		if _, _, err := ValidateRunSpecFile(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestPrivateLiveRunUsesCopiedCredentialsAndObservedReadMethods(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tempRepository := t.TempDir()
	if err := exec.Command("git", "-C", tempRepository, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(tempRepository, ".gitignore"), "private/\n", 0o600)
	caseDir := filepath.Join(tempRepository, "private", "live-case")
	if err := os.MkdirAll(filepath.Join(caseDir, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(caseDir, "workspace", "README.md"), "private live workspace\n", 0o600)
	scenario := validScenario()
	scenario.ID = "jira.private-live"
	scenario.DataClass = "private-local"
	scenario.RequiredChecks = []string{"answer_correct", "atl_succeeded", "guard_clean", "http_observed", "no_delegation", "used_atl"}
	scenario.RequiredMetrics = []string{"atl_invocations", "backend_requests", "duplicate_backend_requests", "output_bytes"}
	scenario.Budgets = Budgets{MaxAgentTurns: 2, MaxToolCalls: 2, MaxATLInvocations: 2, MaxBackendRequests: 2, MaxRemoteWrites: 0, MaxOutputBytes: 4096, MaxInputTokens: 1000, MaxOutputTokens: 1000, MaxMainThreadInputTokens: 1000, MaxMainThreadOutputTokens: 1000, MaxEstimatedCostMicroUSD: 10_000_000, MaxDurationMillis: 30_000, AllowedHTTPMethods: []string{"GET", "HEAD"}}
	writeJSONTestFile(t, filepath.Join(caseDir, "scenario.json"), scenario)
	writeTestFile(t, filepath.Join(caseDir, "prompt.md"), "Read the private fixture.\n", 0o600)
	writeTestFile(t, filepath.Join(caseDir, "response.json"), `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`, 0o600)
	rubric := Rubric{SchemaVersion: 1, ID: "private-answer", ScenarioID: scenario.ID, MinimumScoreBPS: 6000, Criteria: []RubricCriterion{{ID: "usefulness", Description: "The answer is useful.", Maximum: 4, Minimum: 2, Weight: 1}}, AllowedFindingIDs: []string{"unclear"}}
	writeJSONTestFile(t, filepath.Join(caseDir, "rubric.json"), rubric)
	spec := RunSpec{SchemaVersion: RunSpecSchemaVersion, BackendMode: BackendModePrivateLive, ScenarioFile: "scenario.json", Provider: "codex", Variant: "typed-mcp", Model: "gpt-test-1", PromptFile: "prompt.md", ResponseSchemaFile: "response.json", QualitativeRubricFile: "rubric.json", WorkspaceTemplate: "workspace", Repetitions: 1, TimeoutSeconds: 30, MaxEstimatedCostMicroUSD: 10_000_000, Pricing: Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000}, ToolTransport: "mcp", AllowedMCPTools: []string{"jira_fields"}, Checks: []RunCheck{{Name: "answer_correct", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`"ok"`)}, {Name: "atl_succeeded", Kind: "atl_all_succeeded"}, {Name: "guard_clean", Kind: "guard_no_denials"}, {Name: "http_observed", Kind: "http_methods_observed"}, {Name: "no_delegation", Kind: "delegations_none"}, {Name: "used_atl", Kind: "atl_invocations_min", Minimum: 1}}}
	writeJSONTestFile(t, filepath.Join(caseDir, "run.json"), spec)

	liveConfig := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(liveConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(liveConfig, "config.json"), `{"jira_url":"https://private.invalid"}`, 0o600)
	writeTestFile(t, filepath.Join(liveConfig, "credentials.json"), `{"jira":"private-test-token"}`, 0o600)
	pluginRoot := filepath.Join(tempRepository, "plugin")
	if err := os.MkdirAll(filepath.Join(pluginRoot, ".claude-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pluginRoot, "skills", "atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(pluginRoot, ".claude-plugin", "plugin.json"), `{"version":"0.4.0"}`, 0o600)
	writeTestFile(t, filepath.Join(pluginRoot, "skills", "atl", "SKILL.md"), "---\nname: atl\n---\nPrivate live skill.\n", 0o600)
	fakeAgent := filepath.Join(tempRepository, "fake-agent")
	writeTestFile(t, fakeAgent, `#!/bin/sh
if [ "$1" = "--version" ]; then echo fake-agent-1; exit 0; fi
if [ -z "$ATL_EVAL_HTTP_GUARD_FILE" ]; then echo missing HTTP guard >&2; exit 31; fi
case "$ATL_CONFIG_DIR" in */atl-agent-eval-live-config-*) ;; *) echo source config exposed directly >&2; exit 32;; esac
if [ ! -s "$ATL_CONFIG_DIR/config.json" ] || [ ! -s "$ATL_CONFIG_DIR/credentials.json" ]; then echo copied config missing >&2; exit 33; fi
printf '%s\n' "$ATL_CONFIG_DIR" >"${ATL_EVAL_COUNTER}.config-path"
printf '%s\n' '{"method":"GET","request_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' >"$ATL_EVAL_HTTP_GUARD_FILE"
final=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then final="$2"; shift 2; continue; fi
  shift
done
printf '%s\n' '{"type":"item.completed","item":{"type":"mcp_tool_call","server":"atl","tool":"jira_fields","status":"completed","result":{"fields":[]}}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20}}'
printf '%s\n' '{"answer":"ok"}' >"$final"
`, 0o700)
	fakeATL := filepath.Join(tempRepository, "fake-atl")
	writeTestFile(t, fakeATL, `#!/bin/sh
if [ "$1" = "version" ]; then printf '%s\n' '{"version":"0.4.0","commit":"test","build_state":"clean"}'; exit 0; fi
exit 2
`, 0o700)
	wrapper := filepath.Join(tempRepository, "agent-eval")
	build := exec.Command("go", "build", "-o", wrapper, "./scripts/agent-eval")
	build.Dir = repositoryRoot
	build.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build wrapper: %v\n%s", err, output)
	}
	outputRoot := filepath.Join(tempRepository, "private", "runs")
	output, err := RunHeadless(context.Background(), RunOptions{SpecPath: filepath.Join(caseDir, "run.json"), OutputRoot: outputRoot, RepositoryRoot: tempRepository, AgentBinary: fakeAgent, ATLBinary: fakeATL, PluginRoot: pluginRoot, WrapperExecutable: wrapper, LiveConfigDir: liveConfig})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Results) != 1 || output.Results[0].Status != "pass" || output.Results[0].Metrics.BackendRequests != 1 || output.Results[0].Metrics.RemoteWrites != 0 || output.Results[0].HTTPMethods["GET"] != 1 {
		t.Fatalf("output=%+v", output)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("private.invalid")) || bytes.Contains(encoded, []byte("private-test-token")) || bytes.Contains(encoded, []byte(liveConfig)) {
		t.Fatalf("public-safe result leaked live configuration: %s", encoded)
	}
	configPathRecord, err := os.ReadFile(filepath.Join(outputRoot, scenario.ID, "codex", spec.Variant, "run-01", ".atl-eval", "atl-invocations.jsonl.config-path"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimSpace(string(configPathRecord))); !os.IsNotExist(err) {
		t.Fatalf("ephemeral live config was not removed: %v", err)
	}
}

func TestPrivateLiveCLIProvidersUseGatewayWithoutSourceCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable scripts are Unix-only")
	}
	var upstreamRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/jira/rest/api/2/field" || request.Header.Get("Authorization") != "Bearer upstream-secret" {
			http.Error(response, "unexpected", http.StatusBadRequest)
			return
		}
		upstreamRequests++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`[{"id":"summary","name":"Summary","custom":false,"schema":{"type":"string"}}]`))
	}))
	defer upstream.Close()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tempRepository := t.TempDir()
	if err := exec.Command("git", "-C", tempRepository, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(tempRepository, ".gitignore"), "private/\n", 0o600)
	caseDir := filepath.Join(tempRepository, "private", "cli-live")
	if err := os.MkdirAll(filepath.Join(caseDir, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(caseDir, "workspace", "README.md"), "Use the installed atl skill.\n", 0o600)
	scenario := validScenario()
	scenario.ID = "jira.private-cli"
	scenario.DataClass = "private-local"
	scenario.RequiredChecks = []string{"answer_correct", "atl_succeeded", "guard_clean", "http_observed", "no_delegation", "used_atl"}
	scenario.RequiredMetrics = []string{"atl_invocations", "backend_requests", "duplicate_backend_requests", "output_bytes"}
	scenario.Budgets = Budgets{MaxAgentTurns: 2, MaxToolCalls: 2, MaxATLInvocations: 1, MaxBackendRequests: 1, MaxRemoteWrites: 0, MaxOutputBytes: 1 << 20, MaxInputTokens: 1000, MaxOutputTokens: 1000, MaxMainThreadInputTokens: 1000, MaxMainThreadOutputTokens: 1000, MaxEstimatedCostMicroUSD: 10_000_000, MaxDurationMillis: 30_000, AllowedHTTPMethods: []string{"GET", "HEAD"}}
	writeJSONTestFile(t, filepath.Join(caseDir, "scenario.json"), scenario)
	writeTestFile(t, filepath.Join(caseDir, "prompt.md"), "Use atl to inspect the field catalog.\n", 0o600)
	writeTestFile(t, filepath.Join(caseDir, "response.json"), `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`, 0o600)
	rubric := Rubric{SchemaVersion: 1, ID: "private-cli-answer", ScenarioID: scenario.ID, MinimumScoreBPS: 6000, Criteria: []RubricCriterion{{ID: "usefulness", Description: "The answer is useful.", Maximum: 4, Minimum: 2, Weight: 1}}, AllowedFindingIDs: []string{"unclear"}}
	writeJSONTestFile(t, filepath.Join(caseDir, "rubric.json"), rubric)
	liveConfig := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(liveConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(liveConfig, "config.json"), `{"jira_url":`+quotedJSON(t, upstream.URL+"/jira")+`}`, 0o600)
	writeTestFile(t, filepath.Join(liveConfig, "credentials.json"), `{"jira":"upstream-secret"}`, 0o600)
	pluginRoot := filepath.Join(tempRepository, "plugin")
	if err := os.MkdirAll(filepath.Join(pluginRoot, ".claude-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pluginRoot, "skills", "atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(pluginRoot, ".claude-plugin", "plugin.json"), `{"version":"0.4.0"}`, 0o600)
	writeTestFile(t, filepath.Join(pluginRoot, "skills", "atl", "SKILL.md"), "---\nname: atl\n---\nUse read-only atl commands.\n", 0o600)
	fakeAgent := filepath.Join(tempRepository, "fake-agent")
	writeTestFile(t, fakeAgent, `#!/bin/sh
if [ "$1" = "--version" ]; then echo fake-agent-1; exit 0; fi
if [ "$1" = "sandbox" ]; then
  for last do :; done
  ATL_EVAL_FORBIDDEN_NETWORK_ADDRESS=127.0.0.1:9 "$last"
  exit $?
fi
if [ -z "$ATL_EVAL_CLI_POLICY_FILE" ] || [ "$ATL_EVAL_GUARD_MODE" != "private-cli" ]; then exit 31; fi
if [ -n "$ATL_JIRA_PAT" ]; then exit 32; fi
if [ "$1" = "-p" ]; then
  case "$ATL_CONFIG_DIR" in */atl-agent-eval-live-config-*) ;; *) exit 34;; esac
  if /bin/grep -q 'upstream-secret' "$ATL_CONFIG_DIR/credentials.json"; then exit 35; fi
else
  if [ -n "$ATL_CONFIG_DIR" ] || [ -n "$ATL_EVAL_REAL_BINARY" ] || [ -z "$ATL_EVAL_COMMAND_BROKER_FILE" ]; then exit 36; fi
fi
atl jira fields >/dev/null || exit 33
if [ "$1" = "-p" ]; then
  printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use"}]}}'
  printf '%s\n' '{"type":"result","num_turns":1,"duration_ms":10,"total_cost_usd":0.00014,"usage":{"input_tokens":100,"output_tokens":20},"structured_output":{"answer":"ok"}}'
  exit 0
fi
final=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then final="$2"; shift 2; continue; fi
  shift
done
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20}}'
printf '%s\n' '{"answer":"ok"}' >"$final"
`, 0o700)
	wrapper := filepath.Join(tempRepository, "agent-eval")
	buildWrapper := exec.Command("go", "build", "-o", wrapper, "./scripts/agent-eval")
	buildWrapper.Dir = repositoryRoot
	buildWrapper.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	if output, err := buildWrapper.CombinedOutput(); err != nil {
		t.Fatalf("build wrapper: %v\n%s", err, output)
	}
	atlBinary := filepath.Join(tempRepository, "real-atl")
	buildATL := exec.Command("go", "build", "-o", atlBinary, "./cmd/atl")
	buildATL.Dir = repositoryRoot
	buildATL.Env = append(os.Environ(), "GOTOOLCHAIN=auto")
	if output, err := buildATL.CombinedOutput(); err != nil {
		t.Fatalf("build atl: %v\n%s", err, output)
	}
	for _, provider := range []string{"claude-code", "codex"} {
		t.Run(provider, func(t *testing.T) {
			spec := RunSpec{SchemaVersion: RunSpecSchemaVersion, BackendMode: BackendModePrivateLive, ScenarioFile: "scenario.json", Provider: provider, Variant: "cli-skill-" + provider, Model: "test-model", PromptFile: "prompt.md", ResponseSchemaFile: "response.json", QualitativeRubricFile: "rubric.json", WorkspaceTemplate: "workspace", Repetitions: 1, TimeoutSeconds: 30, MaxEstimatedCostMicroUSD: 10_000_000, Pricing: Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000}, ToolTransport: "cli", AllowedTools: []string{"Bash(atl *)", "Read", "Skill"}, AllowedCLICommands: []CLICommandRule{{Name: "jira_fields", Command: []string{"jira", "fields"}, MaxInvocations: 1}}, AllowedGatewayRoutes: map[string][]LiveGatewayRoute{"jira": {{Name: "jira_api", PathPrefix: "/rest/api/2"}}}, GatewayMaxResponseBytes: 1 << 20, GatewayMaxTotalBytes: 1 << 20, Checks: []RunCheck{{Name: "answer_correct", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`"ok"`)}, {Name: "atl_succeeded", Kind: "atl_all_succeeded"}, {Name: "guard_clean", Kind: "guard_no_denials"}, {Name: "http_observed", Kind: "http_methods_observed"}, {Name: "no_delegation", Kind: "delegations_none"}, {Name: "used_atl", Kind: "atl_invocations_min", Minimum: 1}}}
			if provider == "claude-code" {
				spec.Pricing = Pricing{}
			}
			specPath := filepath.Join(caseDir, "run-"+provider+".json")
			writeJSONTestFile(t, specPath, spec)
			output, err := RunHeadless(context.Background(), RunOptions{SpecPath: specPath, OutputRoot: filepath.Join(tempRepository, "private", "runs"), RepositoryRoot: tempRepository, AgentBinary: fakeAgent, ATLBinary: atlBinary, PluginRoot: pluginRoot, WrapperExecutable: wrapper, LiveConfigDir: liveConfig})
			if err != nil {
				runDir := filepath.Join(tempRepository, "private", "runs", scenario.ID, provider, spec.Variant, "run-01")
				stderr, _ := os.ReadFile(filepath.Join(runDir, "agent.stderr"))
				t.Fatalf("%v; agent stderr=%s", err, stderr)
			}
			if len(output.Results) != 1 || output.Results[0].Status != "pass" || output.Results[0].Metrics.BackendRequests != 1 || output.Results[0].Metrics.RemoteWrites != 0 || output.Results[0].HTTPMethods["GET"] != 1 {
				t.Fatalf("output=%+v", output)
			}
			runDir := filepath.Join(tempRepository, "private", "runs", scenario.ID, provider, spec.Variant, "run-01")
			policyData, err := os.ReadFile(filepath.Join(runDir, ".atl-eval", "cli-policy.json"))
			if err != nil || !strings.Contains(string(policyData), `"jira_fields"`) {
				t.Fatalf("policy err=%v data=%s", err, policyData)
			}
			if _, err := os.Stat(filepath.Join(runDir, "workspace", ".atl-eval")); !os.IsNotExist(err) {
				t.Fatalf("model-readable telemetry exists: %v", err)
			}
			if provider == "codex" {
				for _, directory := range []string{"command-broker-requests", "command-broker-responses"} {
					entries, err := os.ReadDir(filepath.Join(runDir, ".atl-eval", directory))
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range entries {
						if strings.HasPrefix(entry.Name(), "request-") || strings.HasPrefix(entry.Name(), "processing-") || strings.HasPrefix(entry.Name(), "response-") {
							t.Fatalf("transient broker payload survived: %s", entry.Name())
						}
					}
				}
				if _, err := os.Stat(filepath.Join(runDir, ".atl-eval", "command-broker.json")); !os.IsNotExist(err) {
					t.Fatalf("command broker manifest survived: %v", err)
				}
			}
			if err := filepath.WalkDir(runDir, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil || entry.IsDir() {
					return walkErr
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if bytes.Contains(data, []byte("upstream-secret")) || bytes.Contains(data, []byte(upstream.URL)) {
					return fmt.Errorf("run artifact retained source backend material")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	if upstreamRequests != 2 {
		t.Fatalf("upstream requests=%d", upstreamRequests)
	}
}

func TestResolveProviderLaunchUsesAbsoluteEnvInterpreter(t *testing.T) {
	launcher := filepath.Join(t.TempDir(), "agent")
	writeTestFile(t, launcher, "#!/usr/bin/env sh\nexit 0\n", 0o700)
	plan, err := resolveProviderLaunch(ProviderCommand{Path: launcher, Args: []string{"exec"}})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(plan.Path) || filepath.Base(plan.Path) != "sh" {
		t.Fatalf("path=%q", plan.Path)
	}
	if len(plan.Args) != 2 || plan.Args[0] != launcher || plan.Args[1] != "exec" {
		t.Fatalf("args=%q", plan.Args)
	}
}

func TestCanonicalizeRunOptionsResolvesRelativeDirectories(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	options, err := canonicalizeRunOptions(RunOptions{
		RepositoryRoot: ".", PluginRoot: ".", AgentBinary: executable,
		ATLBinary: executable, WrapperExecutable: executable,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"repository": options.RepositoryRoot, "plugin": options.PluginRoot,
		"agent": options.AgentBinary, "atl": options.ATLBinary, "wrapper": options.WrapperExecutable,
	} {
		if !filepath.IsAbs(path) {
			t.Errorf("%s path is relative: %q", name, path)
		}
	}
}

func TestSafeAgentEnvironmentDropsUnrelatedCredentials(t *testing.T) {
	environment := safeAgentEnvironment([]string{
		"HOME=/home/test", "HTTPS_PROXY=http://proxy.invalid", "GH_TOKEN=secret",
		"OPENAI_API_KEY=secret", "ATL_JIRA_PAT=secret", "PATH=/usr/bin",
	})
	if environment["HOME"] != "/home/test" || environment["HTTPS_PROXY"] != "http://proxy.invalid" {
		t.Fatalf("environment=%v", environment)
	}
	for _, name := range []string{"GH_TOKEN", "OPENAI_API_KEY", "ATL_JIRA_PAT", "PATH"} {
		if _, ok := environment[name]; ok {
			t.Errorf("unexpected %s in provider environment", name)
		}
	}
}

func TestReadLiveGatewayRecordsRequiresCompleteAllowedPairs(t *testing.T) {
	identity := strings.Repeat("a", 64)
	forward := LiveGatewayAuditRecord{Sequence: 1, Phase: "preflight", Service: "jira", Route: "jira_api", Method: "GET", RequestHMAC: identity, Decision: "forward"}
	complete := LiveGatewayAuditRecord{Sequence: 2, Phase: "complete", Service: "jira", Route: "jira_api", Method: "GET", RequestHMAC: identity, Decision: "allow", StatusClass: "2xx", ResponseBytes: 7}
	writeRecords := func(t *testing.T, records ...LiveGatewayAuditRecord) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "audit.jsonl")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		encoder := json.NewEncoder(file)
		for _, record := range records {
			if err := encoder.Encode(record); err != nil {
				t.Fatal(err)
			}
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}
	methods, duplicates, observed, err := readLiveGatewayRecords(writeRecords(t, forward, complete))
	if err != nil || !observed || methods["GET"] != 1 || duplicates != 0 {
		t.Fatalf("methods=%v duplicates=%d observed=%v err=%v", methods, duplicates, observed, err)
	}
	if _, _, _, err := readLiveGatewayRecords(writeRecords(t, forward)); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete err=%v", err)
	}
	denied := forward
	denied.Decision = "deny"
	denied.Reason = "route"
	if _, _, _, err := readLiveGatewayRecords(writeRecords(t, denied)); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("denied err=%v", err)
	}
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func writeJSONTestFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, string(data), 0o600)
}
