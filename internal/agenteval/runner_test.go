package agenteval

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunHeadlessWithFakeCodexUsesPrivateWrapperAndSyntheticMetrics(t *testing.T) {
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
		"agent_turns", "tool_calls", "atl_invocations", "backend_requests",
		"output_bytes", "input_tokens", "output_tokens",
		"estimated_cost_microusd", "duration_millis",
	}
	scenario.Budgets = Budgets{
		MaxAgentTurns: 2, MaxToolCalls: 2, MaxATLInvocations: 2,
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
	spec := RunSpec{
		SchemaVersion: 1, ScenarioFile: "scenario.json", Provider: "claude-code",
		Variant: "baseline", Model: "claude-test-1", PromptFile: "prompt.md",
		ResponseSchemaFile: "response.json", WorkspaceTemplate: "workspace",
		FixtureFile: "fixture.json", Repetitions: 1, TimeoutSeconds: 30,
		MaxEstimatedCostMicroUSD: 10_000_000,
		Pricing:                  Pricing{},
		AllowedTools:             []string{"Bash(atl *)", "Skill"},
		AllowedATLCommands:       []string{"atl version"},
		Checks: []RunCheck{
			{Name: "answer_correct", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`"ok"`)},
			{Name: "atl_succeeded", Kind: "atl_all_succeeded"},
			{Name: "mock_clean", Kind: "mock_no_unexpected"},
			{Name: "used_atl", Kind: "atl_invocations_min", Minimum: 1},
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
atl version >/dev/null
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution"}}'
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
	result := output.Results[0]
	if result.Metrics.ATLInvocations != 1 || result.Metrics.BackendRequests != 0 || result.Metrics.EstimatedCostMicroUSD != 140 {
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

	spec.Provider = "codex"
	spec.Model = "gpt-test-1"
	spec.Pricing = Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000}
	spec.AllowedTools = []string{"Bash(atl *)"}
	writeJSONTestFile(t, filepath.Join(caseDir, "run.json"), spec)
	_, err = RunHeadless(context.Background(), RunOptions{
		SpecPath: filepath.Join(caseDir, "run.json"), OutputRoot: outputRoot,
		RepositoryRoot: tempRepository, AgentBinary: fakeAgent, ATLBinary: fakeATL,
		PluginRoot: pluginRoot, WrapperExecutable: wrapper,
	})
	if err == nil || !strings.Contains(err.Error(), "codex model execution is disabled") {
		t.Fatalf("codex error=%v", err)
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
