package agenteval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type RunOptions struct {
	SpecPath            string
	OutputRoot          string
	RepositoryRoot      string
	AgentBinary         string
	ATLBinary           string
	PluginRoot          string
	WrapperExecutable   string
	ModelOverride       string
	RepetitionsOverride int
	DryRun              bool
}

type RunPreview struct {
	SchemaVersion                  int             `json:"schema_version"`
	ScenarioID                     string          `json:"scenario_id"`
	Provider                       string          `json:"provider"`
	Variant                        string          `json:"variant"`
	Repetitions                    int             `json:"repetitions"`
	MaxEstimatedCostMicroUSDTotal  int64           `json:"max_estimated_cost_microusd_total"`
	MaxEstimatedCostMicroUSDPerRun int64           `json:"max_estimated_cost_microusd_per_run"`
	Command                        ProviderCommand `json:"command"`
	OutputRoot                     string          `json:"output_root"`
}

type RunOutput struct {
	Preview                    RunPreview `json:"preview"`
	Results                    []Result   `json:"results"`
	EstimatedCostMicroUSDTotal int64      `json:"estimated_cost_microusd_total"`
	BudgetExhausted            bool       `json:"budget_exhausted"`
}

type atlProxyRecord struct {
	StdoutBytes int64 `json:"stdout_bytes"`
	StderrBytes int64 `json:"stderr_bytes"`
	ExitCode    int   `json:"exit_code"`
}

func RunHeadless(ctx context.Context, options RunOptions) (RunOutput, error) {
	if options.OutputRoot == "" || options.RepositoryRoot == "" || options.AgentBinary == "" || options.ATLBinary == "" || options.PluginRoot == "" || options.WrapperExecutable == "" {
		return RunOutput{}, fmt.Errorf("run options require output, repository, agent, atl, plugin, and wrapper paths")
	}
	var err error
	options, err = canonicalizeRunOptions(options)
	if err != nil {
		return RunOutput{}, err
	}
	loaded, err := loadRunInputs(options)
	if err != nil {
		return RunOutput{}, err
	}
	if options.ModelOverride != "" {
		loaded.spec.Model = options.ModelOverride
	}
	if options.RepetitionsOverride != 0 {
		if options.RepetitionsOverride < 1 || options.RepetitionsOverride > loaded.spec.Repetitions {
			return RunOutput{}, fmt.Errorf("repetitions override must be in 1..%d", loaded.spec.Repetitions)
		}
		loaded.spec.Repetitions = options.RepetitionsOverride
	}
	if err := loaded.spec.Validate(); err != nil {
		return RunOutput{}, err
	}
	if err := loaded.spec.ValidateAgainstScenario(loaded.scenario); err != nil {
		return RunOutput{}, err
	}
	outputRoot, err := PreparePrivateOutputRoot(options.OutputRoot, options.RepositoryRoot)
	if err != nil {
		return RunOutput{}, err
	}
	invocationSpec := loaded.spec
	invocationSpec.MaxEstimatedCostMicroUSD = perRepetitionCostCap(loaded.spec)
	previewCommand, err := BuildProviderCommand(invocationSpec, filepath.Base(options.AgentBinary), "<workspace>", "<response-schema>", "<final-response>", pluginPreviewPath(options.PluginRoot), claudeGuardSettingsPath(loaded.spec.Provider, "<guard-settings>"), loaded.responseSchema)
	if err != nil {
		return RunOutput{}, err
	}
	preview := RunPreview{
		SchemaVersion: 1, ScenarioID: loaded.scenario.ID,
		Provider: loaded.spec.Provider, Variant: loaded.spec.Variant,
		Repetitions:                    loaded.spec.Repetitions,
		MaxEstimatedCostMicroUSDTotal:  loaded.spec.MaxEstimatedCostMicroUSD,
		MaxEstimatedCostMicroUSDPerRun: invocationSpec.MaxEstimatedCostMicroUSD,
		Command:                        previewCommand,
		OutputRoot:                     "<private-output-root>",
	}
	if options.DryRun {
		return RunOutput{Preview: preview, Results: []Result{}}, nil
	}
	if loaded.spec.Provider == "codex" {
		return RunOutput{}, fmt.Errorf("codex model execution is disabled until atl uses an isolated typed tool or external container; validate or dry-run the spec instead")
	}

	agentVersion, err := commandVersion(ctx, options.AgentBinary)
	if err != nil {
		return RunOutput{}, fmt.Errorf("agent version: %w", err)
	}
	atlVersion, err := atlRuntimeVersion(ctx, options.ATLBinary)
	if err != nil {
		return RunOutput{}, fmt.Errorf("atl version: %w", err)
	}
	pluginVersion, skillDigest, err := pluginIdentity(options.PluginRoot)
	if err != nil {
		return RunOutput{}, err
	}

	results := make([]Result, 0, loaded.spec.Repetitions)
	var totalCost int64
	var budgetExhausted bool
	for repetition := 1; repetition <= loaded.spec.Repetitions; repetition++ {
		perRun := loaded
		perRun.spec.MaxEstimatedCostMicroUSD = perRepetitionCostCap(loaded.spec)
		result, err := runHeadlessOnce(ctx, perRun, options, outputRoot, repetition, Runtime{
			Provider: loaded.spec.Provider, AgentVersion: agentVersion,
			Model: loaded.spec.Model, Reasoning: loaded.spec.Reasoning,
			ATLVersion: atlVersion, PluginVersion: pluginVersion, SkillDigest: skillDigest,
		})
		if err != nil {
			return RunOutput{}, fmt.Errorf("repetition %d: %w", repetition, err)
		}
		results = append(results, result)
		if result.Coverage["estimated_cost_microusd"] {
			totalCost += result.Metrics.EstimatedCostMicroUSD
			if result.Metrics.EstimatedCostMicroUSD > perRun.spec.MaxEstimatedCostMicroUSD || totalCost > loaded.spec.MaxEstimatedCostMicroUSD {
				budgetExhausted = true
				break
			}
		}
	}
	return RunOutput{Preview: preview, Results: results, EstimatedCostMicroUSDTotal: totalCost, BudgetExhausted: budgetExhausted}, nil
}

func canonicalizeRunOptions(options RunOptions) (RunOptions, error) {
	canonicalDirectory := func(name, path string) (string, error) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory", name)
		}
		return resolved, nil
	}
	canonicalExecutable := func(name, path string) (string, error) {
		if !filepath.IsAbs(path) {
			resolved, err := exec.LookPath(path)
			if err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
			path = resolved
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
			return "", fmt.Errorf("%s is not an executable regular file", name)
		}
		return resolved, nil
	}
	var err error
	if options.RepositoryRoot, err = canonicalDirectory("repository root", options.RepositoryRoot); err != nil {
		return RunOptions{}, err
	}
	if options.PluginRoot, err = canonicalDirectory("plugin root", options.PluginRoot); err != nil {
		return RunOptions{}, err
	}
	if options.AgentBinary, err = canonicalExecutable("agent binary", options.AgentBinary); err != nil {
		return RunOptions{}, err
	}
	if options.ATLBinary, err = canonicalExecutable("atl binary", options.ATLBinary); err != nil {
		return RunOptions{}, err
	}
	if options.WrapperExecutable, err = canonicalExecutable("evaluation wrapper", options.WrapperExecutable); err != nil {
		return RunOptions{}, err
	}
	return options, nil
}

func perRepetitionCostCap(spec RunSpec) int64 {
	return spec.MaxEstimatedCostMicroUSD / int64(spec.Repetitions)
}

type loadedRun struct {
	spec           RunSpec
	scenario       Scenario
	fixture        MockFixture
	prompt         []byte
	responseSchema []byte
	workspace      string
	specDir        string
}

func loadRunInputs(options RunOptions) (loadedRun, error) {
	if options.SpecPath == "" {
		return loadedRun{}, fmt.Errorf("run options require a spec path")
	}
	file, err := os.Open(options.SpecPath)
	if err != nil {
		return loadedRun{}, err
	}
	spec, decodeErr := DecodeRunSpec(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return loadedRun{}, decodeErr
	}
	if closeErr != nil {
		return loadedRun{}, closeErr
	}
	specPath, err := filepath.Abs(options.SpecPath)
	if err != nil {
		return loadedRun{}, err
	}
	specPath, err = filepath.EvalSymlinks(specPath)
	if err != nil {
		return loadedRun{}, err
	}
	specDir := filepath.Dir(specPath)
	resolveRelative := func(relative string) (string, error) {
		target, err := filepath.EvalSymlinks(filepath.Join(specDir, relative))
		if err != nil {
			return "", err
		}
		inside, err := pathWithin(specDir, target)
		if err != nil || !inside {
			return "", fmt.Errorf("run spec path %q escapes its directory", relative)
		}
		return target, nil
	}
	openRelative := func(relative string) (*os.File, error) {
		target, err := resolveRelative(relative)
		if err != nil {
			return nil, err
		}
		return os.Open(target)
	}
	scenarioFile, err := openRelative(spec.ScenarioFile)
	if err != nil {
		return loadedRun{}, err
	}
	scenario, scenarioErr := DecodeScenario(scenarioFile)
	_ = scenarioFile.Close()
	if scenarioErr != nil {
		return loadedRun{}, scenarioErr
	}
	if err := spec.ValidateAgainstScenario(scenario); err != nil {
		return loadedRun{}, err
	}
	fixtureFile, err := openRelative(spec.FixtureFile)
	if err != nil {
		return loadedRun{}, err
	}
	fixture, fixtureErr := DecodeMockFixture(fixtureFile)
	_ = fixtureFile.Close()
	if fixtureErr != nil {
		return loadedRun{}, fixtureErr
	}
	promptPath, err := resolveRelative(spec.PromptFile)
	if err != nil {
		return loadedRun{}, err
	}
	prompt, err := readBoundedFile(promptPath, 1<<20)
	if err != nil {
		return loadedRun{}, err
	}
	responseSchemaPath, err := resolveRelative(spec.ResponseSchemaFile)
	if err != nil {
		return loadedRun{}, err
	}
	responseSchema, err := readBoundedFile(responseSchemaPath, 1<<20)
	if err != nil || !json.Valid(responseSchema) {
		return loadedRun{}, fmt.Errorf("response schema is invalid")
	}
	workspace, err := resolveRelative(spec.WorkspaceTemplate)
	if err != nil {
		return loadedRun{}, err
	}
	return loadedRun{spec: spec, scenario: scenario, fixture: fixture, prompt: prompt, responseSchema: responseSchema, workspace: workspace, specDir: specDir}, nil
}

func ValidateRunSpecFile(path string) (RunSpec, Scenario, error) {
	loaded, err := loadRunInputs(RunOptions{SpecPath: path})
	if err != nil {
		return RunSpec{}, Scenario{}, err
	}
	return loaded.spec, loaded.scenario, nil
}

func runHeadlessOnce(parent context.Context, loaded loadedRun, options RunOptions, outputRoot string, repetition int, runtime Runtime) (Result, error) {
	runDir := filepath.Join(outputRoot, loaded.scenario.ID, loaded.spec.Provider, loaded.spec.Variant, fmt.Sprintf("run-%02d", repetition))
	if err := mkdirPrivate(runDir); err != nil {
		return Result{}, err
	}
	workspace := filepath.Join(runDir, "workspace")
	if err := copyWorkspace(loaded.workspace, workspace); err != nil {
		return Result{}, err
	}
	if loaded.spec.Provider == "codex" {
		if err := copyWorkspace(filepath.Join(options.PluginRoot, "skills"), filepath.Join(workspace, ".agents", "skills")); err != nil {
			return Result{}, fmt.Errorf("install benchmark skills: %w", err)
		}
	}
	responseSchemaPath := filepath.Join(runDir, "response-schema.json")
	if err := writePrivateFile(responseSchemaPath, loaded.responseSchema); err != nil {
		return Result{}, err
	}
	finalPath := filepath.Join(runDir, "final.json")
	transcriptPath := filepath.Join(runDir, "transcript.jsonl")
	stderrPath := filepath.Join(runDir, "agent.stderr")
	evalDir := filepath.Join(workspace, ".atl-eval")
	if err := mkdirPrivate(evalDir); err != nil {
		return Result{}, err
	}
	counterPath := filepath.Join(evalDir, "atl-invocations.jsonl")
	wrapperDir := filepath.Join(runDir, "bin")
	if err := mkdirPrivate(wrapperDir); err != nil {
		return Result{}, err
	}
	if err := copyExecutable(options.WrapperExecutable, filepath.Join(wrapperDir, wrapperName())); err != nil {
		return Result{}, err
	}
	guardPath := filepath.Join(wrapperDir, guardName())
	if err := copyExecutable(options.WrapperExecutable, guardPath); err != nil {
		return Result{}, err
	}
	settingsPath := filepath.Join(runDir, "claude-settings.json")
	if err := writeClaudeGuardSettings(settingsPath, guardPath); err != nil {
		return Result{}, err
	}
	backend, err := StartMockBackend(loaded.fixture)
	if err != nil {
		return Result{}, err
	}
	defer backend.Close()

	commandPlan, err := BuildProviderCommand(loaded.spec, options.AgentBinary, workspace, responseSchemaPath, finalPath, claudePluginPath(loaded.spec.Provider, options.PluginRoot), claudeGuardSettingsPath(loaded.spec.Provider, settingsPath), loaded.responseSchema)
	if err != nil {
		return Result{}, err
	}
	commandPlan, err = resolveProviderLaunch(commandPlan)
	if err != nil {
		return Result{}, err
	}
	transcript, err := os.OpenFile(transcriptPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Result{}, err
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = transcript.Close()
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(loaded.spec.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, commandPlan.Path, commandPlan.Args...)
	command.Dir = workspace
	command.Stdin = bytes.NewReader(loaded.prompt)
	command.Stdout = transcript
	command.Stderr = stderr
	environment := safeAgentEnvironment(os.Environ())
	environment["ATL_READ_ONLY"] = "1"
	environment["ATL_NO_UPDATE"] = "1"
	environment["ATL_CONFIG_DIR"] = filepath.Join(evalDir, "atl-config")
	environment["ATL_MIRROR_ROOT"] = filepath.Join(evalDir, "mirror")
	environment["ATL_EVAL_REAL_BINARY"] = options.ATLBinary
	environment["ATL_EVAL_COUNTER"] = counterPath
	allowedCommands, _ := json.Marshal(loaded.spec.AllowedATLCommands)
	environment["ATL_EVAL_ALLOWED_COMMANDS"] = string(allowedCommands)
	environment["PATH"] = wrapperDir
	for name, value := range backend.Environment() {
		environment[name] = value
	}
	command.Env = flattenEnvironment(environment)
	started := time.Now()
	runErr := command.Run()
	duration := time.Since(started).Milliseconds()
	closeTranscriptErr := transcript.Close()
	closeStderrErr := stderr.Close()
	if ctx.Err() == context.DeadlineExceeded {
		return Result{}, fmt.Errorf("agent exceeded %d second timeout", loaded.spec.TimeoutSeconds)
	}
	if runErr != nil {
		return Result{}, fmt.Errorf("agent process failed: %w", runErr)
	}
	if closeTranscriptErr != nil || closeStderrErr != nil {
		return Result{}, fmt.Errorf("close agent output: %v %v", closeTranscriptErr, closeStderrErr)
	}
	transcriptData, err := readBoundedFile(transcriptPath, 64<<20)
	if err != nil {
		return Result{}, err
	}
	var finalData []byte
	if loaded.spec.Provider == "codex" {
		finalData, err = readBoundedFile(finalPath, 4<<20)
		if err != nil {
			return Result{}, err
		}
	}
	providerMetrics, final, err := ParseProviderOutput(loaded.spec.Provider, transcriptData, finalData)
	if err != nil {
		return Result{}, err
	}
	proxyRecords, err := readProxyRecords(counterPath)
	if err != nil {
		return Result{}, err
	}
	methods, unexpected := backend.Summary()
	if loaded.spec.Provider == "claude-code" {
		if err := writePrivateFile(finalPath, append(append([]byte(nil), final...), '\n')); err != nil {
			return Result{}, err
		}
	} else if err := os.Chmod(finalPath, 0o600); err != nil {
		return Result{}, err
	}
	var failedATL int
	for _, record := range proxyRecords {
		if record.ExitCode != 0 {
			failedATL++
		}
	}
	checks, err := evaluateRunChecks(loaded.spec.Checks, final, len(proxyRecords), failedATL, unexpected)
	if err != nil {
		return Result{}, err
	}
	var outputBytes int64
	for _, record := range proxyRecords {
		outputBytes += record.StdoutBytes
	}
	providerMetrics.DurationMillis = duration
	providerMetrics.Coverage["duration_millis"] = true
	if !providerMetrics.Coverage["estimated_cost_microusd"] && providerMetrics.Coverage["input_tokens"] && providerMetrics.Coverage["output_tokens"] {
		cost, err := estimateCost(providerMetrics.InputTokens, providerMetrics.OutputTokens, loaded.spec.Pricing)
		if err != nil {
			return Result{}, err
		}
		providerMetrics.EstimatedCostMicroUSD = cost
		providerMetrics.Coverage["estimated_cost_microusd"] = true
	}
	providerMetrics.Coverage["atl_invocations"] = true
	providerMetrics.Coverage["backend_requests"] = true
	providerMetrics.Coverage["output_bytes"] = true
	observation := Observation{
		SchemaVersion: ObservationSchemaVersion, ScenarioID: loaded.scenario.ID,
		Variant: loaded.spec.Variant, Runtime: runtime,
		Metrics: InputMetrics{
			AgentTurns: providerMetrics.AgentTurns, ToolCalls: providerMetrics.ToolCalls,
			ATLInvocations: len(proxyRecords), OutputBytes: outputBytes,
			InputTokens: providerMetrics.InputTokens, OutputTokens: providerMetrics.OutputTokens,
			EstimatedCostMicroUSD: providerMetrics.EstimatedCostMicroUSD,
			DurationMillis:        providerMetrics.DurationMillis,
		},
		Coverage: providerMetrics.Coverage, HTTPMethods: methods, Checks: checks,
	}
	result, err := Evaluate(loaded.scenario, observation)
	if err != nil {
		return Result{}, err
	}
	if result.Coverage["estimated_cost_microusd"] && result.Metrics.EstimatedCostMicroUSD > loaded.spec.MaxEstimatedCostMicroUSD {
		result.Status = "fail"
		result.Violations = append(result.Violations, Violation{
			Code: "run_cost_cap_exceeded", Subject: "estimated_cost_microusd",
			Observed: result.Metrics.EstimatedCostMicroUSD, Limit: loaded.spec.MaxEstimatedCostMicroUSD,
		})
		sort.Slice(result.Violations, func(i, j int) bool {
			if result.Violations[i].Code != result.Violations[j].Code {
				return result.Violations[i].Code < result.Violations[j].Code
			}
			return result.Violations[i].Subject < result.Violations[j].Subject
		})
	}
	resultPath := filepath.Join(runDir, "result.json")
	encoded, _ := json.MarshalIndent(result, "", "  ")
	encoded = append(encoded, '\n')
	if err := writePrivateFile(resultPath, encoded); err != nil {
		return Result{}, err
	}
	return result, nil
}

// resolveProviderLaunch keeps the model-visible PATH restricted even when a
// provider CLI is installed as an /usr/bin/env script (for example Codex's
// Node launcher). Only the provider process gets the absolute interpreter;
// tools started by the model still inherit the synthetic proxy-only PATH.
func resolveProviderLaunch(plan ProviderCommand) (ProviderCommand, error) {
	file, err := os.Open(plan.Path)
	if err != nil {
		return ProviderCommand{}, err
	}
	prefix := make([]byte, 512)
	count, readErr := file.Read(prefix)
	closeErr := file.Close()
	if readErr != nil && readErr != io.EOF {
		return ProviderCommand{}, readErr
	}
	if closeErr != nil {
		return ProviderCommand{}, closeErr
	}
	line, _, _ := bytes.Cut(prefix[:count], []byte{'\n'})
	fields := strings.Fields(strings.TrimSpace(string(line)))
	if len(fields) == 0 || fields[0] != "#!/usr/bin/env" {
		return plan, nil
	}
	if len(fields) != 2 || strings.HasPrefix(fields[1], "-") {
		return ProviderCommand{}, fmt.Errorf("unsupported provider /usr/bin/env shebang")
	}
	interpreter, err := exec.LookPath(fields[1])
	if err != nil {
		return ProviderCommand{}, fmt.Errorf("provider interpreter %q: %w", fields[1], err)
	}
	args := make([]string, 0, len(plan.Args)+1)
	args = append(args, plan.Path)
	args = append(args, plan.Args...)
	return ProviderCommand{Path: interpreter, Args: args}, nil
}

func readProxyRecords(path string) ([]atlProxyRecord, error) {
	data, err := readBoundedFile(path, 1<<20)
	if os.IsNotExist(err) {
		return []atlProxyRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []atlProxyRecord
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record atlProxyRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("decode atl proxy record: %w", err)
		}
		records = append(records, record)
	}
	return records, nil
}

func estimateCost(inputTokens, outputTokens int64, pricing Pricing) (int64, error) {
	if inputTokens < 0 || outputTokens < 0 || pricing.InputMicroUSDPerMillionTokens < 0 || pricing.OutputMicroUSDPerMillionTokens < 0 {
		return 0, fmt.Errorf("cost inputs must be non-negative")
	}
	if inputTokens > math.MaxInt64/max64(1, pricing.InputMicroUSDPerMillionTokens) || outputTokens > math.MaxInt64/max64(1, pricing.OutputMicroUSDPerMillionTokens) {
		return 0, fmt.Errorf("estimated cost overflows")
	}
	return (inputTokens*pricing.InputMicroUSDPerMillionTokens + outputTokens*pricing.OutputMicroUSDPerMillionTokens + 999_999) / 1_000_000, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func commandVersion(ctx context.Context, binary string) (string, error) {
	command := exec.CommandContext(ctx, binary, "--version")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(output))
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("invalid version output")
	}
	return value, nil
}

func atlRuntimeVersion(ctx context.Context, binary string) (string, error) {
	command := exec.CommandContext(ctx, binary, "version")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	var value struct {
		Version    string `json:"version"`
		Commit     string `json:"commit"`
		BuildState string `json:"build_state"`
	}
	if json.Unmarshal(output, &value) == nil && value.Version != "" {
		return value.Version + "+" + value.Commit + "." + value.BuildState, nil
	}
	plain := strings.TrimSpace(string(output))
	if plain == "" || len(plain) > 256 {
		return "", fmt.Errorf("invalid atl version output")
	}
	return plain, nil
}

func pluginIdentity(root string) (string, string, error) {
	manifest, err := readBoundedFile(filepath.Join(root, ".claude-plugin", "plugin.json"), 1<<20)
	if err != nil {
		return "", "", err
	}
	var value struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &value); err != nil || value.Version == "" {
		return "", "", fmt.Errorf("plugin manifest version is invalid")
	}
	digest, err := digestTree(filepath.Join(root, "skills"))
	return value.Version, digest, err
}

func digestTree(root string) (string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill tree contains symlink")
		}
		if entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = rootHandle.Close() }()
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		file, err := rootHandle.Open(relative)
		if err != nil {
			return "", err
		}
		data, readErr := ioReadAllLimit(file, 4<<20)
		closeErr := file.Close()
		if readErr != nil {
			return "", readErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		_, _ = hash.Write([]byte(filepath.ToSlash(relative)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := ioReadAllLimit(file, limit)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func ioReadAllLimit(file *os.File, limit int64) ([]byte, error) {
	reader := io.LimitReader(file, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}

func environmentMap(values []string) map[string]string {
	out := map[string]string{}
	for _, value := range values {
		if name, item, ok := strings.Cut(value, "="); ok {
			out[name] = item
		}
	}
	return out
}

func safeAgentEnvironment(ambient []string) map[string]string {
	all := environmentMap(ambient)
	allowed := []string{
		"HOME", "USER", "LOGNAME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL",
		"TERM", "COLORTERM", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
		"CODEX_HOME", "CLAUDE_CONFIG_DIR", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	}
	out := make(map[string]string, len(allowed))
	for _, name := range allowed {
		if value, ok := all[name]; ok {
			out[name] = value
		}
	}
	return out
}

func flattenEnvironment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

func copyExecutable(source, target string) error {
	data, err := readBoundedFile(source, 128<<20)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chmod(target, 0o700)
}

func wrapperName() string {
	if filepath.Ext(os.Args[0]) == ".exe" {
		return "atl.exe"
	}
	return "atl"
}
func guardName() string {
	if filepath.Ext(os.Args[0]) == ".exe" {
		return "atl-eval-guard.exe"
	}
	return "atl-eval-guard"
}
func writeClaudeGuardSettings(path, guardPath string) error {
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "Bash",
				"hooks": []any{map[string]any{
					"type": "command", "command": shellSingleQuote(guardPath), "timeout": 5,
				}},
			}},
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}
func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
func claudePluginPath(provider, root string) string {
	if provider == "claude-code" {
		return root
	}
	return ""
}
func claudeGuardSettingsPath(provider, path string) string {
	if provider == "claude-code" {
		return path
	}
	return ""
}
func pluginPreviewPath(root string) string {
	if root == "" {
		return ""
	}
	return "<plugin-root>"
}
