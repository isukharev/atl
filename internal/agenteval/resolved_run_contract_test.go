package agenteval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const stableResolvedRunSpec = "jira-epic-evidence/run.codex.json"

func TestResolveRunContractNeedsOnlySpecPath(t *testing.T) {
	paths := resolvedRunTestPaths(t)

	contract, err := resolveRunContract(paths.spec)
	if err != nil {
		t.Fatal(err)
	}
	if contract.spec.Provider != "codex" || contract.spec.Model != "gpt-5.6-sol" ||
		contract.spec.Repetitions != 3 || contract.scenario.ID != "jira.synthetic-epic-evidence" ||
		contract.fixture == nil || contract.rubric.ID == "" || len(contract.prompt) == 0 ||
		len(contract.providerPrompt) == 0 || len(contract.responseSchema) == 0 ||
		contract.workspaceTemplate == "" || contract.specDir != filepath.Dir(paths.spec) {
		t.Fatalf("resolved contract omitted stable spec inputs: %+v", contract)
	}
}

func TestResolvedRunContractAndRunSpecExcludeRuntimeMCPBindings(t *testing.T) {
	paths := resolvedRunTestPaths(t)
	contract, err := resolveRunContract(paths.spec)
	if err != nil {
		t.Fatal(err)
	}

	runSpecType := reflect.TypeOf(RunSpec{})
	for index := 0; index < runSpecType.NumField(); index++ {
		if field := runSpecType.Field(index); field.PkgPath != "" {
			t.Errorf("RunSpec has hidden non-durable field %s", field.Name)
		}
	}
	resolvedType := reflect.TypeOf(contract)
	gotFields := make([]string, 0, resolvedType.NumField())
	for index := 0; index < resolvedType.NumField(); index++ {
		gotFields = append(gotFields, resolvedType.Field(index).Name)
	}
	wantFields := []string{
		"spec", "scenario", "fixture", "prompt", "providerPrompt",
		"promptContractSHA256", "responseSchema", "rubric", "workspaceTemplate", "specDir",
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("resolved contract field inventory=%v, want %v", gotFields, wantFields)
	}
	bindingType := reflect.TypeOf(providerCommandBindings{})
	gotFields = gotFields[:0]
	for index := 0; index < bindingType.NumField(); index++ {
		gotFields = append(gotFields, bindingType.Field(index).Name)
	}
	wantFields = []string{"externalMCPServerURL", "externalMCPBearerTokenEnv"}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("provider command binding inventory=%v, want %v", gotFields, wantFields)
	}
}

func TestRunHeadlessDryRunStableResolvedContractPreview(t *testing.T) {
	paths := resolvedRunTestPaths(t)
	output, err := RunHeadless(context.Background(), paths.options())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(output.Preview)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schema_version":1,"scenario_id":"jira.synthetic-epic-evidence","provider":"codex","variant":"v0.4-skill","category":"route-fixed","surface":"cli-skill","prompt_contract_bound":true,"backend_mode":"synthetic","repetitions":3,"max_estimated_cost_microusd_total":10000000,"max_estimated_cost_microusd_per_run":3333333,"command":{"path":"codex","args":["exec","--json","--ephemeral","--strict-config","--skip-git-repo-check","--model","gpt-5.6-sol","--ignore-user-config","--disable","apps","--disable","browser_use","--disable","computer_use","--disable","image_generation","--disable","remote_plugin","--sandbox","read-only","-C","\u003cworkspace\u003e","--output-schema","\u003cresponse-schema\u003e","--output-last-message","\u003cfinal-response\u003e","-c","project_doc_max_bytes=0","-c","shell_environment_policy.inherit=\"all\"","-c","shell_environment_policy.include_only=[\"PATH\",\"ATL_READ_ONLY\",\"ATL_NO_UPDATE\",\"ATL_CONFIG_DIR\",\"ATL_MIRROR_ROOT\",\"ATL_JIRA_URL\",\"ATL_CONFLUENCE_URL\",\"ATL_JIRA_PAT\",\"ATL_CONFLUENCE_PAT\",\"ATL_ALLOW_INSECURE\",\"ATL_EVAL_REAL_BINARY\",\"ATL_EVAL_COUNTER\",\"ATL_EVAL_ALLOWED_COMMANDS\"]","-c","model_reasoning_effort=\"medium\"","-"]},"output_root":"\u003cprivate-output-root\u003e","qualitative_rubric_id":"jira-epic-evidence-answer"}`
	if string(data) != want {
		t.Fatalf("preview JSON changed\n got: %s\nwant: %s", data, want)
	}
	if output.Results == nil || len(output.Results) != 0 || output.EstimatedCostMicroUSDTotal != 0 || output.BudgetExhausted {
		t.Fatalf("dry-run output=%+v", output)
	}
}

func TestRunHeadlessOverridesCopyResolvedContractAndRepartitionCost(t *testing.T) {
	paths := resolvedRunTestPaths(t)
	contract, err := resolveRunContract(paths.spec)
	if err != nil {
		t.Fatal(err)
	}
	overridden, err := contract.withOverrides("gpt-characterization-override", 2)
	if err != nil {
		t.Fatal(err)
	}
	attempt := overridden.forAttempt()
	if contract.spec.Model != "gpt-5.6-sol" || contract.spec.Repetitions != 3 || contract.spec.MaxEstimatedCostMicroUSD != 10_000_000 {
		t.Fatalf("original contract mutated: %+v", contract.spec)
	}
	if overridden.spec.Model != "gpt-characterization-override" || overridden.spec.Repetitions != 2 || overridden.spec.MaxEstimatedCostMicroUSD != 10_000_000 {
		t.Fatalf("override contract=%+v", overridden.spec)
	}
	if attempt.spec.Model != overridden.spec.Model || attempt.spec.Repetitions != 2 || attempt.spec.MaxEstimatedCostMicroUSD != 5_000_000 {
		t.Fatalf("attempt contract=%+v", attempt.spec)
	}
	if overridden.spec.MaxEstimatedCostMicroUSD != 10_000_000 {
		t.Fatalf("attempt partition mutated override contract: %+v", overridden.spec)
	}

	options := paths.options()
	options.ModelOverride = "gpt-characterization-override"
	options.RepetitionsOverride = 2

	output, err := RunHeadless(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if output.Preview.Repetitions != 2 || output.Preview.MaxEstimatedCostMicroUSDTotal != 10_000_000 ||
		output.Preview.MaxEstimatedCostMicroUSDPerRun != 5_000_000 {
		t.Fatalf("override cost partition=%+v", output.Preview)
	}
	model := providerCommandArgument(output.Preview.Command.Args, "--model")
	if model != options.ModelOverride {
		t.Fatalf("preview model=%q, want %q (command=%+v)", model, options.ModelOverride, output.Preview.Command)
	}

	if contract.spec.Model != "gpt-5.6-sol" || contract.spec.Repetitions != 3 || contract.spec.MaxEstimatedCostMicroUSD != 10_000_000 {
		t.Fatalf("RunHeadless mutated original resolved contract: %+v", contract.spec)
	}
	if perRepetitionCostCap(contract.spec) != 3_333_333 {
		t.Fatalf("durable per-repetition cap=%d", perRepetitionCostCap(contract.spec))
	}
}

func TestRunHeadlessResolvedContractFailurePrecedence(t *testing.T) {
	paths := resolvedRunTestPaths(t)
	invalidSpec := filepath.Join(t.TempDir(), "invalid-run.json")
	writeTestFile(t, invalidSpec, "{\n", 0o600)
	validDirectory := t.TempDir()
	invalidOutput := filepath.Join(t.TempDir(), "occupied-output")
	if err := os.Mkdir(invalidOutput, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(invalidOutput, "unrelated"), "must survive\n", 0o600)

	tests := []struct {
		name    string
		options RunOptions
		want    string
	}{
		{
			name: "required paths precede spec loading",
			options: RunOptions{
				SpecPath: invalidSpec,
			},
			want: "run options require output, repository, agent, atl, plugin, and wrapper paths",
		},
		{
			name: "required path resolution precedes spec loading",
			options: func() RunOptions {
				options := paths.options()
				options.SpecPath = invalidSpec
				options.RepositoryRoot = filepath.Join(t.TempDir(), "missing-repository")
				return options
			}(),
			want: "repository root:",
		},
		{
			name: "spec loading precedes overrides and runtime inputs",
			options: func() RunOptions {
				options := paths.options()
				options.SpecPath = invalidSpec
				options.LiveConfigDir = validDirectory
				options.ExternalMCPProfile = filepath.Join(t.TempDir(), "missing-profile.json")
				options.RepetitionsOverride = 99
				options.OutputRoot = invalidOutput
				return options
			}(),
			want: "decode run spec:",
		},
		{
			name: "override validation precedes runtime inputs",
			options: func() RunOptions {
				options := paths.options()
				options.LiveConfigDir = validDirectory
				options.ExternalMCPProfile = filepath.Join(t.TempDir(), "missing-profile.json")
				options.RepetitionsOverride = 4
				options.OutputRoot = invalidOutput
				return options
			}(),
			want: "repetitions override must be in 1..3",
		},
		{
			name: "live config applicability precedes external profile and output",
			options: func() RunOptions {
				options := paths.options()
				options.LiveConfigDir = validDirectory
				options.ExternalMCPProfile = filepath.Join(t.TempDir(), "missing-profile.json")
				options.OutputRoot = invalidOutput
				return options
			}(),
			want: "--live-config-dir is only valid for private-live runs",
		},
		{
			name: "external profile applicability precedes output",
			options: func() RunOptions {
				options := paths.options()
				options.ExternalMCPProfile = filepath.Join(t.TempDir(), "missing-profile.json")
				options.OutputRoot = invalidOutput
				return options
			}(),
			want: "--external-mcp-profile is valid only for external-mcp runs",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := RunHeadless(context.Background(), test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRunHeadlessOutputMarkerPrecedesSyntheticAttestation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based attestation failure is Unix-specific")
	}
	paths := resolvedRunTestPaths(t)
	unreadable := filepath.Join(t.TempDir(), "unreadable-agent")
	writeTestFile(t, unreadable, "#!/bin/sh\nexit 0\n", 0o100)
	if file, err := os.Open(unreadable); err == nil {
		_ = file.Close()
		t.Skip("current user can read mode-0100 files")
	}

	occupied := filepath.Join(t.TempDir(), "occupied-output")
	if err := os.Mkdir(occupied, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(occupied, "unrelated"), "must survive\n", 0o600)
	options := paths.options()
	options.AgentBinary = unreadable
	options.OutputRoot = occupied
	if _, err := RunHeadless(context.Background(), options); err == nil || !strings.Contains(err.Error(), "existing evaluation output root is not initialized") {
		t.Fatalf("output marker failure did not precede attestation: %v", err)
	}

	options.OutputRoot = filepath.Join(t.TempDir(), "fresh-output")
	if _, err := RunHeadless(context.Background(), options); err == nil || !strings.Contains(err.Error(), "hash agent executable") {
		t.Fatalf("attestation failure=%v", err)
	}
	marker, err := os.ReadFile(filepath.Join(options.OutputRoot, privateOutputRootMarker))
	if err != nil || string(marker) != privateOutputRootMarkerContents {
		t.Fatalf("output marker was not durably initialized before attestation: data=%q err=%v", marker, err)
	}
}

type resolvedRunPaths struct {
	repository string
	spec       string
	plugin     string
	output     string
	executable string
}

func resolvedRunTestPaths(t *testing.T) resolvedRunPaths {
	t.Helper()
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	repository, err = filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return resolvedRunPaths{
		repository: repository,
		spec:       filepath.Join(repository, "benchmarks", "agent-eval", filepath.FromSlash(stableResolvedRunSpec)),
		plugin:     repository,
		output:     filepath.Join(t.TempDir(), "output"),
		executable: executable,
	}
}

func (paths resolvedRunPaths) options() RunOptions {
	return RunOptions{
		SpecPath: paths.spec, OutputRoot: paths.output, RepositoryRoot: paths.repository,
		AgentBinary: paths.executable, ATLBinary: paths.executable, PluginRoot: paths.plugin,
		WrapperExecutable: paths.executable, DryRun: true,
	}
}

func providerCommandArgument(arguments []string, flag string) string {
	for index := range arguments {
		if arguments[index] == flag && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return ""
}
