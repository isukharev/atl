package agenteval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestValidateCalibrationEvidenceFailsClosed(t *testing.T) {
	goodMetrics := ProviderMetrics{CommandExecutions: 1}
	goodFinal := []byte(`{"version":"0.4.0","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","build_state":"clean"}`)
	goodObservation, err := CalibrationVersionObservationSHA256(goodFinal)
	if err != nil {
		t.Fatal(err)
	}
	goodRecords := []atlProxyRecord{{CommandFamily: "atl_version", CalibrationObservationSHA256: goodObservation, StdoutBytes: 32}}
	badObservationRecords := append([]atlProxyRecord(nil), goodRecords...)
	badObservationRecords[0].CalibrationObservationSHA256 = strings.Repeat("f", 64)
	stderrRecords := append([]atlProxyRecord(nil), goodRecords...)
	stderrRecords[0].StderrBytes = 1
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
		"no call":         {metrics: goodMetrics, guard: goodGuard, final: goodFinal},
		"multiple":        {metrics: goodMetrics, records: append(append([]atlProxyRecord(nil), goodRecords...), goodRecords...), guard: goodGuard, final: goodFinal},
		"failed":          {metrics: goodMetrics, records: []atlProxyRecord{{StdoutBytes: 32, ExitCode: 1}}, guard: goodGuard, final: goodFinal},
		"wrong family":    {metrics: goodMetrics, records: []atlProxyRecord{{CommandFamily: "other", StdoutBytes: 32}}, guard: goodGuard, final: goodFinal},
		"denied record":   {metrics: goodMetrics, records: []atlProxyRecord{{Denied: true}}, guard: goodGuard, final: goodFinal},
		"missing hook":    {metrics: goodMetrics, records: goodRecords, final: goodFinal},
		"wrong hook":      {metrics: goodMetrics, records: goodRecords, guard: guardDecisionSummary{Admissions: 1}, final: goodFinal},
		"extra hook":      {metrics: goodMetrics, records: goodRecords, guard: guardDecisionSummary{Admissions: 2, ATLAdmissions: 1}, final: goodFinal},
		"hook denial":     {metrics: goodMetrics, records: goodRecords, guard: guardDecisionSummary{Admissions: 1, ATLAdmissions: 1, Denials: 1}, final: goodFinal},
		"empty output":    {metrics: goodMetrics, records: []atlProxyRecord{{}}, guard: goodGuard, final: goodFinal},
		"no shell event":  {records: goodRecords, guard: goodGuard, final: goodFinal},
		"bad response":    {metrics: goodMetrics, records: goodRecords, guard: goodGuard, final: []byte(`{"version":"0.4.0","commit":"other","build_state":"clean"}`)},
		"bad observation": {metrics: goodMetrics, records: badObservationRecords, guard: goodGuard, final: goodFinal},
		"stderr":          {metrics: goodMetrics, records: stderrRecords, guard: goodGuard, final: goodFinal},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateCalibrationEvidence(test.metrics, test.records, test.guard, test.final); err == nil {
				t.Fatal("invalid calibration evidence passed")
			}
		})
	}
}

func TestCalibrationEvidenceStatusDistinguishesPolicyNonInvocationSchemaAndSuccess(t *testing.T) {
	final := []byte(`{"version":"0.4.0","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","build_state":"clean"}`)
	observation, err := CalibrationVersionObservationSHA256(final)
	if err != nil {
		t.Fatal(err)
	}
	records := []atlProxyRecord{{CommandFamily: "atl_version", CalibrationObservationSHA256: observation, StdoutBytes: 32}}
	tests := []struct {
		name    string
		metrics ProviderMetrics
		records []atlProxyRecord
		guard   guardDecisionSummary
		final   []byte
		want    CodexCLICalibrationStatus
	}{
		{name: "model non invocation", final: []byte(`{"retrieval":"failed"}`), want: CodexCLICalibrationModelNonInvocation},
		{name: "policy denied", guard: guardDecisionSummary{Denials: 1}, final: final, want: CodexCLICalibrationPolicyDenied},
		{name: "response schema", metrics: ProviderMetrics{CommandExecutions: 1}, records: records, guard: guardDecisionSummary{Admissions: 1, ATLAdmissions: 1}, final: []byte(`{"version":"missing-fields"}`), want: CodexCLICalibrationResponseSchemaFailed},
		{name: "invocation failed", metrics: ProviderMetrics{CommandExecutions: 1}, guard: guardDecisionSummary{Admissions: 1, ATLAdmissions: 1}, final: final, want: CodexCLICalibrationInvocationFailed},
		{name: "success", metrics: ProviderMetrics{CommandExecutions: 1}, records: records, guard: guardDecisionSummary{Admissions: 1, ATLAdmissions: 1}, final: final, want: CodexCLICalibrationSucceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyCalibrationEvidence(test.metrics, test.records, test.guard, test.final)
			if got != test.want {
				t.Fatalf("status=%s want=%s", got, test.want)
			}
			err := validateCalibrationEvidence(test.metrics, test.records, test.guard, test.final)
			if test.want == CodexCLICalibrationSucceeded {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var failure *CodexCLICalibrationFailure
			if !errors.As(err, &failure) || failure.Status != test.want {
				t.Fatalf("failure=%v", err)
			}
		})
	}
}

func TestParseCodexCalibrationProviderOutputDistinguishesProcessAndResponseSchema(t *testing.T) {
	validTranscript := []byte(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`)
	tests := []struct {
		name       string
		transcript []byte
		final      []byte
		want       CodexCLICalibrationStatus
	}{
		{name: "process", transcript: []byte(`not-json`), final: []byte(`{}`), want: CodexCLICalibrationProcessFailed},
		{name: "empty response", transcript: validTranscript, want: CodexCLICalibrationResponseSchemaFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseCodexCalibrationProviderOutput(test.transcript, test.final)
			var failure *CodexCLICalibrationFailure
			if !errors.As(err, &failure) || failure.Status != test.want {
				t.Fatalf("failure=%v want=%s", err, test.want)
			}
		})
	}
}

func TestCalibrationVersionObservationSHA256IsSemanticAndClosed(t *testing.T) {
	first, err := CalibrationVersionObservationSHA256([]byte("{\n  \"version\": \"0.4.0\",\n  \"commit\": \"abc\",\n  \"build_state\": \"clean\"\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := CalibrationVersionObservationSHA256([]byte(`{"build_state":"clean","commit":"abc","version":"0.4.0"}`))
	if err != nil || first != second || !validSHA256(first) {
		t.Fatalf("first=%q second=%q err=%v", first, second, err)
	}
	for name, value := range map[string]string{
		"changed":   `{"version":"0.4.1","commit":"abc","build_state":"clean"}`,
		"missing":   `{"version":"0.4.0","commit":"abc"}`,
		"extra":     `{"version":"0.4.0","commit":"abc","build_state":"clean","other":true}`,
		"legacy":    `{"ok":true}`,
		"duplicate": `{"version":"0.4.0","version":"0.4.1","commit":"abc","build_state":"clean"}`,
	} {
		t.Run(name, func(t *testing.T) {
			digest, digestErr := CalibrationVersionObservationSHA256([]byte(value))
			if name == "changed" {
				if digestErr != nil || digest == first {
					t.Fatalf("digest=%q err=%v", digest, digestErr)
				}
				return
			}
			if digestErr == nil {
				t.Fatalf("invalid response digest=%q", digest)
			}
		})
	}
}

func TestCodexCLICalibrationSchemaTypesEveryProperty(t *testing.T) {
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type      string   `json:"type"`
			Enum      []string `json:"enum"`
			MinLength int      `json:"minLength"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(codexCLICalibrationSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || len(schema.Properties) != 3 || len(schema.Required) != 3 || schema.AdditionalProperties {
		t.Fatalf("provider-incompatible calibration schema: %+v", schema)
	}
	for _, name := range []string{"version", "commit", "build_state"} {
		property, exists := schema.Properties[name]
		if !exists || property.Type != "string" || !slices.Contains(schema.Required, name) {
			t.Fatalf("provider-incompatible calibration schema property %q: %+v", name, schema)
		}
	}
	if schema.Properties["version"].MinLength != 1 || schema.Properties["commit"].MinLength != 1 {
		t.Fatalf("provider-incompatible non-empty fields: %+v", schema.Properties)
	}
	if !slices.Equal(schema.Properties["build_state"].Enum, []string{"clean", "dirty", "unknown"}) {
		t.Fatalf("provider-incompatible build_state enum: %+v", schema.Properties["build_state"])
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

func TestCodexCLICalibrationContractRejectsPreviousPromptAndSchemaDigest(t *testing.T) {
	currentPrompt := codexCLICalibrationPrompt
	currentSchema := codexCLICalibrationSchema
	codexCLICalibrationPrompt = []byte("Use the shell tool to run the literal command `atl version` exactly once. Do not run any other command. After the command succeeds, return the required JSON object.\n")
	codexCLICalibrationSchema = []byte(`{"type":"object","properties":{"ok":{"type":"boolean","const":true}},"required":["ok"],"additionalProperties":false}`)
	legacy, err := BuildCodexCLICalibrationContract("test-model", "high", 60, 500_000, Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000})
	codexCLICalibrationPrompt = currentPrompt
	codexCLICalibrationSchema = currentSchema
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Validate(); err == nil {
		t.Fatal("previous calibration prompt/schema digest remained executable")
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
observed=$(atl version) || exit 62
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","status":"completed","command":"atl version"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":20}}'
printf '%s\n' "$observed" >"$final"
`
}
