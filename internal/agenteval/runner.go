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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
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
	LiveConfigDir       string
	ModelOverride       string
	RepetitionsOverride int
	DryRun              bool
}

type RunPreview struct {
	SchemaVersion                  int             `json:"schema_version"`
	ScenarioID                     string          `json:"scenario_id"`
	Provider                       string          `json:"provider"`
	Variant                        string          `json:"variant"`
	BackendMode                    string          `json:"backend_mode"`
	Repetitions                    int             `json:"repetitions"`
	MaxEstimatedCostMicroUSDTotal  int64           `json:"max_estimated_cost_microusd_total"`
	MaxEstimatedCostMicroUSDPerRun int64           `json:"max_estimated_cost_microusd_per_run"`
	Command                        ProviderCommand `json:"command"`
	OutputRoot                     string          `json:"output_root"`
	QualitativeRubricID            string          `json:"qualitative_rubric_id"`
}

type RunOutput struct {
	Preview                    RunPreview `json:"preview"`
	Results                    []Result   `json:"results"`
	EstimatedCostMicroUSDTotal int64      `json:"estimated_cost_microusd_total"`
	BudgetExhausted            bool       `json:"budget_exhausted"`
}

type atlProxyRecord struct {
	CommandFamily string `json:"command_family,omitempty"`
	Denied        bool   `json:"denied,omitempty"`
	StdoutBytes   int64  `json:"stdout_bytes"`
	StderrBytes   int64  `json:"stderr_bytes"`
	ExitCode      int    `json:"exit_code"`
}

type guardDecisionRecord struct {
	Decision string `json:"decision"`
}

type liveHTTPRecord struct {
	Method      string `json:"method"`
	RequestHash string `json:"request_hash"`
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
	if loaded.spec.EffectiveBackendMode() == BackendModePrivateLive {
		if options.LiveConfigDir == "" {
			return RunOutput{}, fmt.Errorf("private-live runs require --live-config-dir")
		}
		if err := requirePrivateLiveInputs(options.SpecPath, options.LiveConfigDir, options.RepositoryRoot); err != nil {
			return RunOutput{}, err
		}
	} else if options.LiveConfigDir != "" {
		return RunOutput{}, fmt.Errorf("--live-config-dir is only valid for private-live runs")
	}
	outputRoot, err := PreparePrivateOutputRoot(options.OutputRoot, options.RepositoryRoot)
	if err != nil {
		return RunOutput{}, err
	}
	invocationSpec := loaded.spec
	invocationSpec.MaxEstimatedCostMicroUSD = perRepetitionCostCap(loaded.spec)
	previewConfinement := ProviderConfinement{}
	if loaded.spec.Provider == "codex" && loaded.spec.EffectiveBackendMode() == BackendModePrivateLive && loaded.spec.ToolTransport == "cli" {
		previewConfinement.RequestDirectory = "/private/requests"
		previewConfinement.ResponseDirectory = "/private/responses"
	}
	previewCommand, err := BuildProviderCommand(invocationSpec, providerPreviewBinary(loaded.spec.Provider), "<atl-binary>", "<guard>", "<workspace>", "<response-schema>", "<final-response>", pluginPreviewPath(options.PluginRoot), claudeGuardSettingsPath(loaded.spec.Provider, "<guard-settings>"), claudeMCPConfigPath(loaded.spec, "<mcp-config>"), previewConfinement, loaded.responseSchema)
	if err != nil {
		return RunOutput{}, err
	}
	preview := RunPreview{
		SchemaVersion: 1, ScenarioID: loaded.scenario.ID,
		Provider: loaded.spec.Provider, Variant: loaded.spec.Variant,
		BackendMode:                    loaded.spec.EffectiveBackendMode(),
		Repetitions:                    loaded.spec.Repetitions,
		MaxEstimatedCostMicroUSDTotal:  loaded.spec.MaxEstimatedCostMicroUSD,
		MaxEstimatedCostMicroUSDPerRun: invocationSpec.MaxEstimatedCostMicroUSD,
		Command:                        previewCommand,
		OutputRoot:                     "<private-output-root>",
		QualitativeRubricID:            loaded.rubric.ID,
	}
	if options.DryRun {
		return RunOutput{Preview: preview, Results: []Result{}}, nil
	}
	if loaded.spec.Provider == "codex" && loaded.spec.ToolTransport != "mcp" {
		if loaded.spec.EffectiveBackendMode() != BackendModePrivateLive {
			return RunOutput{}, fmt.Errorf("codex synthetic model execution requires tool_transport=mcp; cli transport remains validate/dry-run only")
		}
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
	if options.LiveConfigDir != "" {
		if options.LiveConfigDir, err = canonicalDirectory("live config dir", options.LiveConfigDir); err != nil {
			return RunOptions{}, err
		}
	}
	return options, nil
}

func perRepetitionCostCap(spec RunSpec) int64 {
	return spec.MaxEstimatedCostMicroUSD / int64(spec.Repetitions)
}

type loadedRun struct {
	spec           RunSpec
	scenario       Scenario
	fixture        *MockFixture
	prompt         []byte
	responseSchema []byte
	rubric         Rubric
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
	var fixture *MockFixture
	if spec.EffectiveBackendMode() == BackendModeSynthetic {
		fixtureFile, err := openRelative(spec.FixtureFile)
		if err != nil {
			return loadedRun{}, err
		}
		decoded, fixtureErr := DecodeMockFixture(fixtureFile)
		_ = fixtureFile.Close()
		if fixtureErr != nil {
			return loadedRun{}, fixtureErr
		}
		fixture = &decoded
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
	rubricFile, err := openRelative(spec.QualitativeRubricFile)
	if err != nil {
		return loadedRun{}, err
	}
	rubric, rubricErr := DecodeRubric(rubricFile)
	_ = rubricFile.Close()
	if rubricErr != nil {
		return loadedRun{}, rubricErr
	}
	if rubric.ScenarioID != scenario.ID {
		return loadedRun{}, fmt.Errorf("qualitative rubric scenario_id %q does not match %q", rubric.ScenarioID, scenario.ID)
	}
	workspace, err := resolveRelative(spec.WorkspaceTemplate)
	if err != nil {
		return loadedRun{}, err
	}
	return loadedRun{spec: spec, scenario: scenario, fixture: fixture, prompt: prompt, responseSchema: responseSchema, rubric: rubric, workspace: workspace, specDir: specDir}, nil
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
	if loaded.spec.EffectiveBackendMode() == BackendModePrivateLive {
		if err := validatePrivateWorkspaceTemplate(loaded.workspace); err != nil {
			return Result{}, err
		}
	}
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
	evalDir := filepath.Join(runDir, ".atl-eval")
	if err := mkdirPrivate(evalDir); err != nil {
		return Result{}, err
	}
	counterPath := filepath.Join(evalDir, "atl-invocations.jsonl")
	guardCounterPath := filepath.Join(evalDir, "guard-decisions.jsonl")
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
	codexPrivateCLI := loaded.spec.Provider == "codex" && loaded.spec.EffectiveBackendMode() == BackendModePrivateLive && loaded.spec.ToolTransport == "cli"
	brokerRequestDirectory := ""
	brokerResponseDirectory := ""
	if codexPrivateCLI {
		brokerRequestDirectory = filepath.Join(evalDir, "command-broker-requests")
		brokerResponseDirectory = filepath.Join(evalDir, "command-broker-responses")
		if err := mkdirPrivate(brokerRequestDirectory); err != nil {
			return Result{}, err
		}
		if err := mkdirPrivate(brokerResponseDirectory); err != nil {
			return Result{}, err
		}
		counterPath = filepath.Join(brokerRequestDirectory, "atl-invocations.jsonl")
	}
	probeExecutablePath := ""
	if codexPrivateCLI {
		probeExecutablePath = filepath.Join(wrapperDir, confinementProbeName())
		if err := copyExecutable(options.WrapperExecutable, probeExecutablePath); err != nil {
			return Result{}, err
		}
	}
	if loaded.spec.EffectiveBackendMode() == BackendModePrivateLive {
		for _, reader := range []string{"cat", "sed", "wc"} {
			if err := copyExecutable(options.WrapperExecutable, filepath.Join(wrapperDir, reader)); err != nil {
				return Result{}, err
			}
		}
	}
	settingsPath := filepath.Join(runDir, "claude-settings.json")
	var reviewedMCPTools []string
	if loaded.spec.Provider == "claude-code" && loaded.spec.ToolTransport == "mcp" {
		reviewedMCPTools = claudeMCPToolNames(loaded.spec.AllowedMCPTools)
	}
	if err := writeClaudeGuardSettings(settingsPath, guardPath, reviewedMCPTools); err != nil {
		return Result{}, err
	}
	atlConfigDir := filepath.Join(evalDir, "atl-config")
	httpGuardPath := ""
	cliPolicyPath := ""
	backendEnvironment := map[string]string{}
	providerConfinement := ProviderConfinement{}
	brokerManifestPath := ""
	var backend *MockBackend
	var liveGateway *LiveGateway
	var commandBroker *CommandBroker
	var err error
	if codexPrivateCLI {
		providerConfinement.RequestDirectory = brokerRequestDirectory
		providerConfinement.ResponseDirectory = brokerResponseDirectory
	}
	if loaded.spec.EffectiveBackendMode() == BackendModeSynthetic {
		if loaded.fixture == nil {
			return Result{}, fmt.Errorf("synthetic run has no fixture")
		}
		backend, err = StartMockBackend(*loaded.fixture)
		if err != nil {
			return Result{}, err
		}
		defer backend.Close()
		backendEnvironment = backend.Environment()
	} else {
		atlConfigDir, err = os.MkdirTemp("", "atl-agent-eval-live-config-")
		if err != nil {
			return Result{}, err
		}
		if err := os.Chmod(atlConfigDir, 0o700); err != nil {
			_ = os.RemoveAll(atlConfigDir)
			return Result{}, err
		}
		defer func() { _ = os.RemoveAll(atlConfigDir) }()
		if loaded.spec.ToolTransport == "cli" {
			cliPolicyPath = filepath.Join(evalDir, "cli-policy.json")
			cliPolicy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: loaded.spec.AllowedCLICommands}
			policyData, err := EncodeCLICommandPolicy(cliPolicy)
			if err != nil {
				return Result{}, err
			}
			if err := writePrivateFile(cliPolicyPath, policyData); err != nil {
				return Result{}, err
			}
			httpGuardPath = filepath.Join(evalDir, "gateway-audit.jsonl")
			liveGateway, err = startPrivateCLIGateway(options.LiveConfigDir, atlConfigDir, httpGuardPath, loaded.spec, loaded.scenario)
			if err != nil {
				return Result{}, err
			}
			defer func() { _ = liveGateway.Close(context.Background()) }()
			if codexPrivateCLI {
				brokerManifestPath = filepath.Join(evalDir, "command-broker.json")
				brokerTimeout := time.Duration(loaded.spec.TimeoutSeconds) * time.Second
				if brokerTimeout > 2*time.Minute {
					brokerTimeout = 2 * time.Minute
				}
				brokerEnvironment := map[string]string{
					"ATL_READ_ONLY": "1", "ATL_NO_UPDATE": "1",
					"ATL_CONFIG_DIR": atlConfigDir, "ATL_MIRROR_ROOT": filepath.Join(evalDir, "mirror"),
					"NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
				}
				for _, name := range []string{"LANG", "LC_ALL", "TERM", "TZ"} {
					if value := os.Getenv(name); value != "" {
						brokerEnvironment[name] = value
					}
				}
				maxStdout := loaded.scenario.Budgets.MaxOutputBytes
				if maxStdout > 4<<20 {
					maxStdout = 4 << 20
				}
				commandBroker, err = StartCommandBroker(CommandBrokerConfig{
					RequestDirectory: brokerRequestDirectory, ResponseDirectory: brokerResponseDirectory,
					ManifestPath: brokerManifestPath,
					RealBinary:   options.ATLBinary, Policy: cliPolicy,
					Environment:    flattenEnvironment(brokerEnvironment),
					MaxStdoutBytes: maxStdout, MaxStderrBytes: 64 << 10, CommandTimeout: brokerTimeout,
				})
				if err != nil {
					return Result{}, err
				}
				defer func() { _ = commandBroker.Close() }()
			}
		} else {
			if err := copyLiveConfig(options.LiveConfigDir, atlConfigDir); err != nil {
				return Result{}, err
			}
			httpGuardPath = filepath.Join(evalDir, "http-methods.jsonl")
			backendEnvironment["ATL_EVAL_HTTP_GUARD_FILE"] = httpGuardPath
		}
	}
	mcpConfigPath := claudeMCPConfigPath(loaded.spec, filepath.Join(runDir, "claude-mcp.json"))
	if mcpConfigPath != "" {
		mcpEnvironment := map[string]string{
			"ATL_READ_ONLY":   "1",
			"ATL_NO_UPDATE":   "1",
			"ATL_CONFIG_DIR":  atlConfigDir,
			"ATL_MIRROR_ROOT": filepath.Join(evalDir, "mirror"),
		}
		for name, value := range backendEnvironment {
			mcpEnvironment[name] = value
		}
		if err := writeClaudeMCPConfig(mcpConfigPath, options.ATLBinary, mcpEnvironment); err != nil {
			return Result{}, err
		}
	}

	commandPlan, err := BuildProviderCommand(loaded.spec, options.AgentBinary, options.ATLBinary, guardPath, workspace, responseSchemaPath, finalPath, claudePluginPath(loaded.spec.Provider, options.PluginRoot), claudeGuardSettingsPath(loaded.spec.Provider, settingsPath), mcpConfigPath, providerConfinement, loaded.responseSchema)
	if err != nil {
		return Result{}, err
	}
	if codexPrivateCLI {
		if err := runCodexConfinementPreflight(parent, options.AgentBinary, workspace, probeExecutablePath, brokerManifestPath, providerConfinement); err != nil {
			return Result{}, err
		}
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
	environment["ATL_CONFIG_DIR"] = atlConfigDir
	environment["ATL_MIRROR_ROOT"] = filepath.Join(evalDir, "mirror")
	environment["ATL_EVAL_REAL_BINARY"] = options.ATLBinary
	environment["ATL_EVAL_COUNTER"] = counterPath
	environment["ATL_EVAL_GUARD_COUNTER"] = guardCounterPath
	if cliPolicyPath != "" {
		environment["ATL_EVAL_CLI_POLICY_FILE"] = cliPolicyPath
		if brokerManifestPath != "" {
			environment["ATL_EVAL_COMMAND_BROKER_FILE"] = brokerManifestPath
			delete(environment, "ATL_NO_UPDATE")
			delete(environment, "ATL_CONFIG_DIR")
			delete(environment, "ATL_MIRROR_ROOT")
			delete(environment, "ATL_EVAL_REAL_BINARY")
		}
		environment["ATL_EVAL_GUARD_MODE"] = "private-cli"
		environment["NO_PROXY"] = "127.0.0.1,localhost"
		environment["no_proxy"] = "127.0.0.1,localhost"
	}
	if loaded.spec.ToolTransport == "mcp" {
		environment["ATL_EVAL_GUARD_MODE"] = "mcp-only"
		if loaded.spec.EffectiveBackendMode() == BackendModePrivateLive {
			environment["ATL_EVAL_GUARD_MODE"] = "mcp-with-skill-read"
		}
	}
	environment["ATL_EVAL_MAX_DELEGATIONS"] = fmt.Sprintf("%d", loaded.scenario.Budgets.MaxDelegations)
	allowedCommands, _ := json.Marshal(loaded.spec.AllowedATLCommands)
	environment["ATL_EVAL_ALLOWED_COMMANDS"] = string(allowedCommands)
	allowedMCPTools, _ := json.Marshal(claudeMCPToolNames(loaded.spec.AllowedMCPTools))
	environment["ATL_EVAL_ALLOWED_MCP_TOOLS"] = string(allowedMCPTools)
	allowedReadRoots, _ := json.Marshal([]string{filepath.Join(options.PluginRoot, "skills"), workspace})
	environment["ATL_EVAL_ALLOWED_READ_ROOTS"] = string(allowedReadRoots)
	environment["PATH"] = wrapperDir
	if loaded.spec.Provider != "claude-code" || loaded.spec.ToolTransport != "mcp" {
		for name, value := range backendEnvironment {
			environment[name] = value
		}
	}
	command.Env = flattenEnvironment(environment)
	started := time.Now()
	var guardAborted atomic.Bool
	guardStop := make(chan struct{})
	var guardDone chan struct{}
	if requiresCleanGuard(loaded.spec.Checks) {
		guardDone = make(chan struct{})
		go func() {
			defer close(guardDone)
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-guardStop:
					return
				case <-ticker.C:
					denials, countErr := countGuardDenials(guardCounterPath)
					if countErr == nil && denials > 0 {
						guardAborted.Store(true)
						cancel()
						return
					}
				}
			}
		}()
	}
	runErr := command.Run()
	var brokerCloseErr error
	if commandBroker != nil {
		brokerCloseErr = commandBroker.Close()
	}
	var gatewayCloseErr error
	if liveGateway != nil {
		gatewayCloseErr = liveGateway.Close(context.Background())
	}
	close(guardStop)
	if guardDone != nil {
		<-guardDone
	}
	duration := time.Since(started).Milliseconds()
	closeTranscriptErr := transcript.Close()
	closeStderrErr := stderr.Close()
	if gatewayCloseErr != nil {
		return Result{}, fmt.Errorf("close private-live gateway: %w", gatewayCloseErr)
	}
	if brokerCloseErr != nil {
		return Result{}, fmt.Errorf("close private-live command broker: %w", brokerCloseErr)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return Result{}, fmt.Errorf("agent exceeded %d second timeout", loaded.spec.TimeoutSeconds)
	}
	if guardAborted.Load() {
		return Result{}, fmt.Errorf("agent attempted a command rejected by the benchmark guard")
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
	var methods map[string]int
	unexpected := 0
	duplicateRequests := 0
	httpMethodsObserved := false
	if backend != nil {
		methods, unexpected, duplicateRequests = backend.Summary()
		httpMethodsObserved = true
	} else if liveGateway != nil {
		methods, duplicateRequests, httpMethodsObserved, err = readLiveGatewayRecords(httpGuardPath)
		if err != nil {
			return Result{}, err
		}
	} else {
		methods, duplicateRequests, httpMethodsObserved, err = readLiveHTTPRecords(httpGuardPath)
		if err != nil {
			return Result{}, err
		}
	}
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
	guardDenials, err := countGuardDenials(guardCounterPath)
	if err != nil {
		return Result{}, err
	}
	atlInvocations := len(proxyRecords) + providerMetrics.MCPToolCalls
	failedATL += providerMetrics.FailedMCPToolCalls
	checks, err := evaluateRunChecks(loaded.spec.Checks, final, atlInvocations, failedATL, unexpected, providerMetrics.Delegations, guardDenials, httpMethodsObserved)
	if err != nil {
		return Result{}, err
	}
	var outputBytes int64
	familyValues := map[string]CapabilityFamilyMetric{}
	familyCoverage := true
	for _, record := range proxyRecords {
		outputBytes += record.StdoutBytes
		if record.Denied || record.CommandFamily == "" {
			familyCoverage = false
			continue
		}
		mergeCapabilityFamily(familyValues, record.CommandFamily, record.ExitCode != 0, record.StdoutBytes)
	}
	outputBytes += providerMetrics.MCPToolOutputBytes
	for _, value := range providerMetrics.CapabilityFamilies {
		existing := familyValues[value.Family]
		existing.Family = value.Family
		existing.Invocations += value.Invocations
		existing.Successes += value.Successes
		existing.Failures += value.Failures
		existing.OutputBytes += value.OutputBytes
		familyValues[value.Family] = existing
	}
	if providerMetrics.MCPToolCalls > 0 && !providerMetrics.CapabilityFamilyCoverage {
		familyCoverage = false
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
	providerMetrics.Coverage["backend_requests"] = httpMethodsObserved
	providerMetrics.Coverage["duplicate_backend_requests"] = httpMethodsObserved
	providerMetrics.Coverage["output_bytes"] = true
	providerMetrics.Coverage["capability_families"] = familyCoverage
	capabilityFamilies := capabilityFamilySlice(familyValues)
	if !familyCoverage {
		capabilityFamilies = nil
	}
	observation := Observation{
		SchemaVersion: ObservationSchemaVersion, ScenarioID: loaded.scenario.ID,
		Variant: loaded.spec.Variant, Runtime: runtime,
		Metrics: InputMetrics{
			AgentTurns: providerMetrics.AgentTurns, ToolCalls: providerMetrics.ToolCalls,
			ATLInvocations: atlInvocations, Delegations: providerMetrics.Delegations,
			DuplicateBackendRequests: duplicateRequests, OutputBytes: outputBytes,
			InputTokens: providerMetrics.InputTokens, OutputTokens: providerMetrics.OutputTokens,
			MainThreadInputTokens: providerMetrics.MainThreadInputTokens, MainThreadOutputTokens: providerMetrics.MainThreadOutputTokens,
			EstimatedCostMicroUSD: providerMetrics.EstimatedCostMicroUSD,
			DurationMillis:        providerMetrics.DurationMillis,
		},
		Coverage: providerMetrics.Coverage, HTTPMethods: methods, Checks: checks,
		CapabilityFamilies: capabilityFamilies,
	}
	result, err := Evaluate(loaded.scenario, observation)
	if err != nil {
		return Result{}, err
	}
	addRunCheckViolations(&result, loaded.spec.Checks, loaded.scenario.RequiredChecks)
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
	sort.Slice(result.Violations, func(i, j int) bool {
		if result.Violations[i].Code != result.Violations[j].Code {
			return result.Violations[i].Code < result.Violations[j].Code
		}
		return result.Violations[i].Subject < result.Violations[j].Subject
	})
	resultPath := filepath.Join(runDir, "result.json")
	encoded, _ := json.MarshalIndent(result, "", "  ")
	encoded = append(encoded, '\n')
	if err := writePrivateFile(resultPath, encoded); err != nil {
		return Result{}, err
	}
	return result, nil
}

func requiresCleanGuard(checks []RunCheck) bool {
	for _, check := range checks {
		if check.Kind == "guard_no_denials" {
			return true
		}
	}
	return false
}

func addRunCheckViolations(result *Result, checks []RunCheck, scenarioRequired []string) {
	required := make(map[string]struct{}, len(scenarioRequired))
	for _, name := range scenarioRequired {
		required[name] = struct{}{}
	}
	for _, check := range checks {
		if result.Checks[check.Name] {
			continue
		}
		if _, exists := required[check.Name]; exists {
			continue
		}
		result.Status = "fail"
		result.Violations = append(result.Violations, Violation{Code: "run_check_failed", Subject: check.Name, Limit: 1})
	}
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

func runCodexConfinementPreflight(parent context.Context, agentBinary, workspace, probeExecutable, brokerManifestPath string, confinement ProviderConfinement) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("prepare codex private-live confinement preflight")
	}
	defer func() { _ = listener.Close() }()
	plan, err := BuildCodexConfinementProbeCommand(agentBinary, workspace, probeExecutable, confinement)
	if err != nil {
		return fmt.Errorf("prepare codex private-live confinement preflight")
	}
	plan, err = resolveProviderLaunch(plan)
	if err != nil {
		return fmt.Errorf("prepare codex private-live confinement preflight")
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, plan.Path, plan.Args...)
	command.Dir = workspace
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	environment := safeAgentEnvironment(os.Environ())
	if pathValue := os.Getenv("PATH"); pathValue != "" {
		environment["PATH"] = pathValue
	}
	environment["ATL_EVAL_COMMAND_BROKER_FILE"] = brokerManifestPath
	environment["ATL_EVAL_FORBIDDEN_NETWORK_ADDRESS"] = listener.Addr().String()
	command.Env = flattenEnvironment(environment)
	if err := command.Run(); err != nil {
		return fmt.Errorf("codex private-live confinement preflight failed before model and backend access")
	}
	return nil
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

func countGuardDenials(path string) (int, error) {
	data, err := readBoundedFile(path, 1<<20)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var denials int
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record guardDecisionRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return 0, fmt.Errorf("decode guard decision record: %w", err)
		}
		switch record.Decision {
		case "allow":
		case "deny":
			denials++
		default:
			return 0, fmt.Errorf("invalid guard decision %q", record.Decision)
		}
	}
	return denials, nil
}

func readLiveHTTPRecords(path string) (map[string]int, int, bool, error) {
	data, err := readBoundedFile(path, 4<<20)
	if os.IsNotExist(err) {
		return map[string]int{}, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	methods := map[string]int{}
	identities := map[string]int{}
	var records int
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record liveHTTPRecord
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil || decoder.Decode(new(any)) != io.EOF {
			return nil, 0, false, fmt.Errorf("decode private-live HTTP audit")
		}
		if (record.Method != "GET" && record.Method != "HEAD") || len(record.RequestHash) != 64 {
			return nil, 0, false, fmt.Errorf("invalid private-live HTTP audit record")
		}
		if _, err := hex.DecodeString(record.RequestHash); err != nil {
			return nil, 0, false, fmt.Errorf("invalid private-live HTTP audit identity")
		}
		methods[record.Method]++
		identities[record.RequestHash]++
		records++
	}
	if records == 0 {
		return methods, 0, false, nil
	}
	duplicates := 0
	for _, count := range identities {
		if count > 1 {
			duplicates += count - 1
		}
	}
	return methods, duplicates, true, nil
}

func readLiveGatewayRecords(path string) (map[string]int, int, bool, error) {
	data, err := readBoundedFile(path, maxLiveGatewayAuditBytes)
	if os.IsNotExist(err) {
		return map[string]int{}, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	methods := map[string]int{}
	identities := map[string]int{}
	forwarded := map[string]int{}
	completed := map[string]int{}
	var allowed int
	var sequence int64
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record LiveGatewayAuditRecord
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil || decoder.Decode(new(any)) != io.EOF {
			return nil, 0, false, fmt.Errorf("decode private-live gateway audit")
		}
		sequence++
		if record.Sequence != sequence || (record.Service != "jira" && record.Service != "confluence") || (record.Method != "GET" && record.Method != "HEAD") || len(record.RequestHMAC) != 64 {
			return nil, 0, false, fmt.Errorf("invalid private-live gateway audit record")
		}
		if _, err := hex.DecodeString(record.RequestHMAC); err != nil {
			return nil, 0, false, fmt.Errorf("invalid private-live gateway audit identity")
		}
		identity := record.Service + "\x00" + record.Method + "\x00" + record.RequestHMAC
		switch record.Phase + ":" + record.Decision {
		case "preflight:forward":
			if record.Route == "" || record.Reason != "" || record.StatusClass != "" || record.ResponseBytes != 0 {
				return nil, 0, false, fmt.Errorf("invalid private-live gateway forward record")
			}
			forwarded[identity]++
		case "complete:allow":
			if record.Route == "" || record.Reason != "" || len(record.StatusClass) != 3 || record.StatusClass[1:] != "xx" || record.ResponseBytes < 0 {
				return nil, 0, false, fmt.Errorf("invalid private-live gateway completion record")
			}
			completed[identity]++
			identities[record.RequestHMAC]++
			methods[record.Method]++
			allowed++
		case "preflight:deny", "complete:deny":
			return nil, 0, false, fmt.Errorf("private-live gateway denied a request")
		default:
			return nil, 0, false, fmt.Errorf("invalid private-live gateway audit decision")
		}
	}
	if len(forwarded) != len(completed) {
		return nil, 0, false, fmt.Errorf("private-live gateway audit is incomplete")
	}
	for identity, count := range forwarded {
		if completed[identity] != count {
			return nil, 0, false, fmt.Errorf("private-live gateway audit is incomplete")
		}
	}
	if allowed == 0 {
		return methods, 0, false, nil
	}
	duplicates := 0
	for _, count := range identities {
		if count > 1 {
			duplicates += count - 1
		}
	}
	return methods, duplicates, true, nil
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

func confinementProbeName() string {
	if filepath.Ext(os.Args[0]) == ".exe" {
		return "atl-eval-confinement-probe.exe"
	}
	return "atl-eval-confinement-probe"
}
func writeClaudeGuardSettings(path, guardPath string, reviewedMCPTools []string) error {
	hooks := make([]any, 0, 6)
	matchers := []string{"Bash", "Agent", "Read", "Edit", "Write", "apply_patch"}
	if len(reviewedMCPTools) > 0 {
		// An omitted matcher applies the hook to every tool. This is required
		// because some built-ins (for example Skill and ToolSearch) do not cross
		// the ordinary permission prompt that dontAsk can reject.
		matchers = []string{""}
	}
	for _, matcher := range matchers {
		hook := map[string]any{
			"hooks": []any{map[string]any{
				"type": "command", "command": shellSingleQuote(guardPath), "timeout": 5,
			}},
		}
		if matcher != "" {
			hook["matcher"] = matcher
		}
		hooks = append(hooks, hook)
	}
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": hooks,
		},
	}
	if len(reviewedMCPTools) > 0 {
		// Headless dontAsk sessions cannot approve project-like MCP configs
		// interactively. Approve only the single generated server name and grant
		// only the run spec's exact dynamic tool names. Passing the same names to
		// Claude's --tools/--allowed-tools CLI filters hides dynamic MCP tools in
		// current releases before discovery completes.
		settings["enabledMcpjsonServers"] = []string{"atl"}
		settings["permissions"] = map[string]any{"allow": reviewedMCPTools}
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}

func writeClaudeMCPConfig(path, atlBinary string, environment map[string]string) error {
	config := map[string]any{
		"mcpServers": map[string]any{
			"atl": map[string]any{
				"type": "stdio", "command": atlBinary,
				"args": []string{"mcp", "serve"}, "env": environment,
				// Current Claude Code starts ordinary servers asynchronously. The
				// benchmark needs the reviewed tools in the first prompt, so make
				// readiness a bounded startup precondition rather than a model race.
				"alwaysLoad": true,
			},
		},
	}
	data, err := json.Marshal(config)
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
func claudeMCPConfigPath(spec RunSpec, path string) string {
	if spec.Provider == "claude-code" && spec.ToolTransport == "mcp" {
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
func providerPreviewBinary(provider string) string {
	if provider == "claude-code" {
		return "claude"
	}
	return provider
}
