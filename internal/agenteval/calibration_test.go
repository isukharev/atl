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

func TestValidateCalibrationEvidenceFailsClosed(t *testing.T) {
	goodMetrics := ProviderMetrics{CommandExecutions: 1}
	goodRecords := []atlProxyRecord{{CommandFamily: "atl_version", StdoutBytes: 32}}
	goodFinal := []byte(`{"ok":true}`)
	goodGuard := guardDecisionSummary{Admissions: 1, ATLAdmissions: 1}
	if err := validateCalibrationEvidence(goodMetrics, goodRecords, goodGuard, goodFinal); err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		metrics ProviderMetrics
		records []atlProxyRecord
		guard   guardDecisionSummary
		final   []byte
	}{
		"no call":        {metrics: goodMetrics, guard: goodGuard, final: goodFinal},
		"multiple":       {metrics: goodMetrics, records: append(append([]atlProxyRecord(nil), goodRecords...), goodRecords...), guard: goodGuard, final: goodFinal},
		"failed":         {metrics: goodMetrics, records: []atlProxyRecord{{StdoutBytes: 32, ExitCode: 1}}, guard: goodGuard, final: goodFinal},
		"wrong family":   {metrics: goodMetrics, records: []atlProxyRecord{{CommandFamily: "other", StdoutBytes: 32}}, guard: goodGuard, final: goodFinal},
		"denied record":  {metrics: goodMetrics, records: []atlProxyRecord{{Denied: true}}, guard: goodGuard, final: goodFinal},
		"missing hook":   {metrics: goodMetrics, records: goodRecords, final: goodFinal},
		"wrong hook":     {metrics: goodMetrics, records: goodRecords, guard: guardDecisionSummary{Admissions: 1}, final: goodFinal},
		"extra hook":     {metrics: goodMetrics, records: goodRecords, guard: guardDecisionSummary{Admissions: 2, ATLAdmissions: 1}, final: goodFinal},
		"hook denial":    {metrics: goodMetrics, records: goodRecords, guard: guardDecisionSummary{Admissions: 1, ATLAdmissions: 1, Denials: 1}, final: goodFinal},
		"empty output":   {metrics: goodMetrics, records: []atlProxyRecord{{}}, guard: goodGuard, final: goodFinal},
		"no shell event": {records: goodRecords, guard: goodGuard, final: goodFinal},
		"bad response":   {metrics: goodMetrics, records: goodRecords, guard: goodGuard, final: []byte(`{"ok":false}`)},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateCalibrationEvidence(test.metrics, test.records, test.guard, test.final); err == nil {
				t.Fatal("invalid calibration evidence passed")
			}
		})
	}
}

func TestBuildCodexCLICalibrationContractIsDeterministicAndComplete(t *testing.T) {
	pricing := Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000}
	first, err := BuildCodexCLICalibrationContract("test-model", "high", 60, 500_000, pricing)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildCodexCLICalibrationContract("test-model", "high", 60, 500_000, pricing)
	if err != nil || first != second || !validSHA256(first.SHA256) || first.Validate() != nil {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	mutations := []CodexCLICalibrationContract{}
	for _, input := range []struct {
		model, reasoning string
		timeout          int
		cap              int64
		pricing          Pricing
	}{
		{"other-model", "high", 60, 500_000, pricing},
		{"test-model", "low", 60, 500_000, pricing},
		{"test-model", "high", 61, 500_000, pricing},
		{"test-model", "high", 60, 500_001, pricing},
		{"test-model", "high", 60, 500_000, Pricing{InputMicroUSDPerMillionTokens: 2_000_000, OutputMicroUSDPerMillionTokens: 2_000_000}},
	} {
		contract, err := BuildCodexCLICalibrationContract(input.model, input.reasoning, input.timeout, input.cap, input.pricing)
		if err != nil {
			t.Fatal(err)
		}
		mutations = append(mutations, contract)
	}
	for _, mutation := range mutations {
		if mutation.SHA256 == first.SHA256 {
			t.Fatalf("contract mutation did not change digest: %+v", mutation)
		}
	}
}

func TestBuildCodexCLICalibrationContractRejectsExecutionInvalidInputs(t *testing.T) {
	pricing := Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000}
	tests := []struct {
		name    string
		model   string
		timeout int
		cap     int64
		pricing Pricing
	}{
		{name: "model", timeout: 60, cap: 500_000, pricing: pricing},
		{name: "zero timeout", model: "test-model", cap: 500_000, pricing: pricing},
		{name: "long timeout", model: "test-model", timeout: maxCodexCLICalibrationTimeout + 1, cap: 500_000, pricing: pricing},
		{name: "cost cap", model: "test-model", timeout: 60, pricing: pricing},
		{name: "input pricing", model: "test-model", timeout: 60, cap: 500_000, pricing: Pricing{OutputMicroUSDPerMillionTokens: 2_000_000}},
		{name: "output pricing", model: "test-model", timeout: 60, cap: 500_000, pricing: Pricing{InputMicroUSDPerMillionTokens: 1_000_000}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildCodexCLICalibrationContract(test.model, "high", test.timeout, test.cap, test.pricing); err == nil {
				t.Fatal("execution-invalid calibration contract was built")
			}
		})
	}
}

func TestCodexCLICalibrationReceiptValidationBindsEverySafetyField(t *testing.T) {
	pricing := Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000}
	contract, err := BuildCodexCLICalibrationContract("test-model", "high", 60, 500_000, pricing)
	if err != nil {
		t.Fatal(err)
	}
	valid := CodexCLICalibrationReceipt{
		SchemaVersion: CodexCLICalibrationSchemaVersion, ContractSHA256: contract.SHA256, Passed: true,
		CommandFamily: "atl_version", CommandExecutions: 1, BrokeredInvocations: 1,
		GuardAdmissions: 1, GuardATLAdmissions: 1, StdoutBytes: 8,
		InputTokens: 30, OutputTokens: 10, EstimatedCostMicroUSD: 50, DurationMillis: 1,
	}
	if err := valid.Validate(contract); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*CodexCLICalibrationReceipt){
		"schema":          func(r *CodexCLICalibrationReceipt) { r.SchemaVersion++ },
		"contract":        func(r *CodexCLICalibrationReceipt) { r.ContractSHA256 = strings.Repeat("f", 64) },
		"passed":          func(r *CodexCLICalibrationReceipt) { r.Passed = false },
		"family":          func(r *CodexCLICalibrationReceipt) { r.CommandFamily = "other" },
		"commands":        func(r *CodexCLICalibrationReceipt) { r.CommandExecutions++ },
		"broker":          func(r *CodexCLICalibrationReceipt) { r.BrokeredInvocations = 0 },
		"admissions":      func(r *CodexCLICalibrationReceipt) { r.GuardAdmissions = 0 },
		"atl admissions":  func(r *CodexCLICalibrationReceipt) { r.GuardATLAdmissions = 0 },
		"extra admission": func(r *CodexCLICalibrationReceipt) { r.GuardAdmissions = 2 },
		"denials":         func(r *CodexCLICalibrationReceipt) { r.GuardDenials = 1 },
		"backend request": func(r *CodexCLICalibrationReceipt) { r.BackendRequests = 1 },
		"remote write":    func(r *CodexCLICalibrationReceipt) { r.RemoteWrites = 1 },
		"stdout":          func(r *CodexCLICalibrationReceipt) { r.StdoutBytes = 0 },
		"tokens":          func(r *CodexCLICalibrationReceipt) { r.InputTokens = 0 },
		"cost":            func(r *CodexCLICalibrationReceipt) { r.EstimatedCostMicroUSD++ },
		"duration":        func(r *CodexCLICalibrationReceipt) { r.DurationMillis = -1 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(contract); err == nil {
				t.Fatal("mutated calibration receipt passed")
			}
		})
	}
}

func TestProviderCalibrationRunSpecIsClosedToExactCommand(t *testing.T) {
	base := calibrationRunSpec(CodexCLICalibrationOptions{
		Model: "test-model", TimeoutSeconds: 60, MaxEstimatedCostMicroUSD: 500_000,
		Pricing: Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000},
	})
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*RunSpec){
		"command": func(spec *RunSpec) { spec.AllowedCLICommands[0].Command = []string{"jira", "fields"} },
		"family":  func(spec *RunSpec) { spec.AllowedCLICommands[0].Name = "other" },
		"limit":   func(spec *RunSpec) { spec.AllowedCLICommands[0].MaxInvocations = 2 },
		"tool":    func(spec *RunSpec) { spec.AllowedTools = append(spec.AllowedTools, "Read") },
		"check":   func(spec *RunSpec) { spec.Checks[0].Expected = json.RawMessage("false") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			spec := base
			spec.AllowedTools = append([]string(nil), base.AllowedTools...)
			spec.AllowedCLICommands = append([]CLICommandRule(nil), base.AllowedCLICommands...)
			spec.AllowedCLICommands[0].Command = append([]string(nil), base.AllowedCLICommands[0].Command...)
			spec.Checks = append([]RunCheck(nil), base.Checks...)
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("mutated provider-calibration spec passed")
			}
		})
	}
}

func TestCalibrationProviderCommandExposesNoBackendConfiguration(t *testing.T) {
	options := CodexCLICalibrationOptions{
		Model: "test-model", TimeoutSeconds: 60, MaxEstimatedCostMicroUSD: 500_000,
		Pricing: Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000},
	}
	spec := calibrationRunSpec(options)
	confinement := ProviderConfinement{
		RequestDirectory: "/private/requests", ResponseDirectory: "/private/responses",
		GuardMode: "provider-calibration", GuardCounterPath: "/private/guard.jsonl",
		WorkspaceReadRoot: "/private/workspace",
		AllowedReadRoots:  []string{"/private/skills", "/private/workspace"},
	}
	command, err := BuildProviderCommand(spec, "codex", "/private/atl", "/private/guard", "/private/workspace", "/private/schema", "/private/final", "", "", "", confinement, codexCLICalibrationSchema)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, " ")
	for _, required := range []string{"--enable shell_tool", "--enable unified_exec", `plugins."atl@atl".enabled=true`, "hooks.PreToolUse="} {
		if !strings.Contains(joined, required) {
			t.Fatalf("calibration provider command misses %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"ATL_CONFIG_DIR", "ATL_MIRROR_ROOT", "ATL_JIRA_URL", "ATL_CONFLUENCE_URL", "ATL_JIRA_PAT", "ATL_CONFLUENCE_PAT", "ATL_ALLOW_INSECURE", "NO_PROXY", "no_proxy"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("calibration provider command exposes %s: %s", forbidden, joined)
		}
	}
}

func TestRunHeadlessRejectsProviderCalibrationMode(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "case")
	workspace := filepath.Join(caseDir, "workspace")
	plugin := filepath.Join(root, "plugin")
	for _, directory := range []string{workspace, plugin} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	scenario := validScenario()
	scenario.ID = "calibration.internal"
	scenario.RequiredChecks = []string{"calibration_response"}
	scenario.RequiredSemanticChecks = nil
	scenario.Budgets.MaxEstimatedCostMicroUSD = 500_000
	writeJSONTestFile(t, filepath.Join(caseDir, "scenario.json"), scenario)
	writeTestFile(t, filepath.Join(caseDir, "prompt.md"), "internal\n", 0o600)
	writeTestFile(t, filepath.Join(caseDir, "response.json"), string(codexCLICalibrationSchema), 0o600)
	rubric := Rubric{
		SchemaVersion: 1, ID: "calibration", ScenarioID: scenario.ID,
		MinimumScoreBPS: 1, Criteria: []RubricCriterion{{ID: "correct", Description: "Correct.", Maximum: 1, Minimum: 0, Weight: 1}},
		AllowedFindingIDs: []string{"incorrect"},
	}
	writeJSONTestFile(t, filepath.Join(caseDir, "rubric.json"), rubric)
	spec := calibrationRunSpec(CodexCLICalibrationOptions{
		Model: "test-model", TimeoutSeconds: 30, MaxEstimatedCostMicroUSD: 500_000,
		Pricing: Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000},
	})
	spec.ScenarioFile = "scenario.json"
	spec.PromptFile = "prompt.md"
	spec.ResponseSchemaFile = "response.json"
	spec.QualitativeRubricFile = "rubric.json"
	spec.WorkspaceTemplate = "workspace"
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(caseDir, "run.json")
	writeTestFile(t, specPath, string(data), 0o600)
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunHeadless(context.Background(), RunOptions{
		SpecPath: specPath, OutputRoot: filepath.Join(root, "output"), RepositoryRoot: root,
		AgentBinary: testBinary, ATLBinary: testBinary, PluginRoot: plugin, WrapperExecutable: testBinary,
	})
	if err == nil || !strings.Contains(err.Error(), "provider-calibration is an internal pre-study contract") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunCodexCLICalibrationUsesBackendFreeBrokeredRoute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Codex provider runtime is POSIX-only")
	}
	home, codexHome := useSyntheticCodexHome(t)
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugin")
	writeTestPluginTrees(t, pluginRoot, "0.4.0", "Calibration skill.")
	promptCapture := filepath.Join(root, "prompt")
	agent := filepath.Join(root, "codex")
	writeTestFile(t, agent, calibrationFakeCodexScript(promptCapture), 0o700)
	atl := filepath.Join(root, "real-atl")
	writeTestFile(t, atl, `#!/bin/sh
if [ "$#" -ne 1 ] || [ "$1" != "version" ]; then exit 70; fi
if [ "$ATL_READ_ONLY" != "1" ] || [ "$ATL_NO_UPDATE" != "1" ]; then exit 71; fi
if [ -n "$ATL_CONFIG_DIR$ATL_MIRROR_ROOT$ATL_JIRA_URL$ATL_CONFLUENCE_URL$ATL_JIRA_PAT$ATL_CONFLUENCE_PAT$NO_PROXY$no_proxy" ]; then exit 72; fi
printf '%s\n' '{"version":"test","commit":"test","build_state":"clean"}'
`, 0o700)
	wrapper := filepath.Join(root, "agent-eval")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", wrapper, "./scripts/agent-eval")
	build.Dir = repositoryRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build wrapper: %v\n%s", err, output)
	}
	outputRoot := filepath.Join(root, "output")
	scratch := filepath.Join(root, "scratch")
	for _, directory := range []string{outputRoot, scratch} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	session, err := newCodexAuthSession([]string{"HOME=" + home, "CODEX_HOME=" + codexHome})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	attempts := 0
	receipt, err := RunCodexCLICalibration(context.Background(), CodexCLICalibrationOptions{
		OutputRoot: outputRoot, RepositoryRoot: repositoryRoot,
		AgentBinary: agent, ATLBinary: atl, PluginRoot: pluginRoot,
		WrapperExecutable: wrapper, ScratchRoot: scratch,
		Model: "test-model", TimeoutSeconds: 30, MaxEstimatedCostMicroUSD: 1_000_000,
		Pricing:             Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000},
		providerAuthSession: session, providerAttemptCommitted: func() error { attempts++; return nil },
	})
	if err != nil {
		stderr, _ := os.ReadFile(filepath.Join(outputRoot, "provider-calibration", "agent.stderr"))
		t.Fatalf("calibration: %v; stderr=%s", err, stderr)
	}
	if !receipt.Passed || receipt.CommandFamily != "atl_version" || receipt.CommandExecutions != 1 || receipt.BrokeredInvocations != 1 || receipt.GuardAdmissions < 1 || receipt.GuardATLAdmissions != 1 || receipt.GuardDenials != 0 || receipt.BackendRequests != 0 || receipt.RemoteWrites != 0 || receipt.StdoutBytes == 0 || attempts != 1 {
		t.Fatalf("receipt=%+v attempts=%d", receipt, attempts)
	}
	prompt, err := os.ReadFile(promptCapture)
	if err != nil || string(prompt) != string(codexCLICalibrationPrompt) {
		t.Fatalf("prompt=%q err=%v", prompt, err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("provider scratch residue=%v err=%v", entries, err)
	}
	for _, directory := range []string{"command-broker-requests", "command-broker-responses"} {
		entries, err := os.ReadDir(filepath.Join(outputRoot, "provider-calibration", ".atl-eval", directory))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "request-") || strings.HasPrefix(entry.Name(), "processing-") || strings.HasPrefix(entry.Name(), "response-") {
				t.Fatalf("transient broker payload survived: %s", entry.Name())
			}
		}
	}
}

func TestVerifyCalibrationCommandSlotRequiresExactFamily(t *testing.T) {
	for name, entries := range map[string][]string{
		"missing":    nil,
		"wrong":      {"cli-slot-other-1"},
		"additional": {"cli-slot-atl_version-1", "cli-slot-other-1"},
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			for _, entry := range entries {
				writeTestFile(t, filepath.Join(directory, entry), "", 0o600)
			}
			if err := verifyCalibrationCommandSlot(directory); err == nil {
				t.Fatal("invalid command slot inventory passed")
			}
		})
	}
	directory := t.TempDir()
	writeTestFile(t, filepath.Join(directory, "cli-slot-atl_version-1"), "", 0o600)
	writeTestFile(t, filepath.Join(directory, "atl-invocations.jsonl"), "", 0o600)
	if err := verifyCalibrationCommandSlot(directory); err != nil {
		t.Fatal(err)
	}
}

func calibrationFakeCodexScript(promptCapture string) string {
	return `#!/bin/sh
if [ "$1" = "plugin" ] && [ "$2" = "marketplace" ] && [ "$3" = "add" ]; then
  printf '%s\n' "$4" >"$CODEX_HOME/marketplace-root"
  printf '%s\n' '[plugins."atl@atl"]' 'enabled = true' >"$CODEX_HOME/config.toml"
  exit 0
fi
if [ "$1" = "plugin" ] && [ "$2" = "add" ]; then
  root=$(/bin/cat "$CODEX_HOME/marketplace-root") || exit 51
  installed="$CODEX_HOME/plugins/cache/atl/atl/0.4.0"
  /bin/mkdir -p "$CODEX_HOME/plugins/cache/atl/atl" || exit 52
  /bin/cp -R "$root/plugins/atl" "$installed" || exit 53
  printf '{"pluginId":"atl@atl","name":"atl","marketplaceName":"atl","version":"0.4.0","installedPath":"%s"}\n' "$installed"
  exit 0
fi
if [ "$1" = "plugin" ] && [ "$2" = "list" ]; then
  root=$(/bin/cat "$CODEX_HOME/marketplace-root") || exit 54
  printf '{"installed":[{"pluginId":"atl@atl","name":"atl","marketplaceName":"atl","version":"0.4.0","installed":true,"enabled":true,"source":{"source":"local","path":"%s/plugins/atl"}}]}\n' "$root"
  exit 0
fi
if [ "$1" = "debug" ] && [ "$2" = "prompt-input" ]; then
  installed="$CODEX_HOME/plugins/cache/atl/atl/0.4.0"
  printf '[{"type":"message","role":"developer","content":[{"type":"input_text","text":"- atl:atl: Synthetic skill (file: %s/skills/atl/SKILL.md)"}]}]\n' "$installed"
  exit 0
fi
if [ "$1" = "mcp" ] && [ "$2" = "list" ]; then
  enabled=true
  case "$(/bin/cat "$CODEX_HOME/config.toml")" in *'mcp_servers."atl"'*) enabled=false;; esac
  printf '[{"name":"atl","enabled":%s}]\n' "$enabled"
  exit 0
fi
if [ "$1" = "sandbox" ]; then
  for last do :; done
  ATL_EVAL_FORBIDDEN_NETWORK_ADDRESS=127.0.0.1:9 "$last"
  exit $?
fi
if [ -n "$ATL_CONFIG_DIR$ATL_MIRROR_ROOT$ATL_JIRA_URL$ATL_CONFLUENCE_URL$ATL_JIRA_PAT$ATL_CONFLUENCE_PAT$ATL_EVAL_REAL_BINARY$NO_PROXY$no_proxy" ]; then exit 60; fi
/bin/cat >` + shellSingleQuote(promptCapture) + ` || exit 61
final=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then final="$2"; shift 2; continue; fi
  shift
done
printf '%s\n' '{"decision":"allow","family":"atl"}' >>"$ATL_EVAL_GUARD_COUNTER" || exit 63
atl version >/dev/null || exit 62
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","status":"completed","command":"atl version"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20}}'
printf '%s\n' '{"ok":true}' >"$final"
`
}
