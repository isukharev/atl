package agenteval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"time"
)

type RunOptions struct {
	SpecPath                 string
	OutputRoot               string
	RepositoryRoot           string
	AgentBinary              string
	ATLBinary                string
	PluginRoot               string
	WrapperExecutable        string
	LiveConfigDir            string
	ExternalMCPProfile       string
	ScratchRoot              string
	PrivateWorkspaceRoot     string
	qualifiedAgentVersion    string
	providerAuthSession      *codexAuthSession
	providerAttemptCommitted func() error
	ModelOverride            string
	RepetitionsOverride      int
	DryRun                   bool
}

type RunPreview struct {
	SchemaVersion                  int             `json:"schema_version"`
	ScenarioID                     string          `json:"scenario_id"`
	Provider                       string          `json:"provider"`
	Variant                        string          `json:"variant"`
	Category                       string          `json:"category"`
	Surface                        string          `json:"surface"`
	SkillActivation                string          `json:"skill_activation,omitempty"`
	PromptContractBound            bool            `json:"prompt_contract_bound,omitempty"`
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

type guardDecisionRecord struct {
	Decision string `json:"decision"`
	Family   string `json:"family,omitempty"`
}

type liveHTTPRecord struct {
	Method      string `json:"method"`
	RequestHash string `json:"request_hash"`
}

func RunHeadless(ctx context.Context, options RunOptions) (output RunOutput, returnErr error) {
	if options.OutputRoot == "" || options.RepositoryRoot == "" || options.AgentBinary == "" || options.ATLBinary == "" || options.PluginRoot == "" || options.WrapperExecutable == "" {
		return RunOutput{}, fmt.Errorf("run options require output, repository, agent, atl, plugin, and wrapper paths")
	}
	var err error
	options, err = canonicalizeRunOptions(options)
	if err != nil {
		return RunOutput{}, err
	}
	contract, err := resolveRunContract(options.SpecPath)
	if err != nil {
		return RunOutput{}, err
	}
	if contract.spec.EffectiveBackendMode() == BackendModeProviderCalibration {
		return RunOutput{}, fmt.Errorf("provider-calibration is an internal pre-study contract; use the private activation plan runner")
	}
	contract, err = contract.withOverrides(options.ModelOverride, options.RepetitionsOverride)
	if err != nil {
		return RunOutput{}, err
	}
	if contract.spec.EffectiveBackendMode() == BackendModePrivateLive {
		if options.LiveConfigDir == "" {
			return RunOutput{}, fmt.Errorf("private-live runs require --live-config-dir")
		}
		if err := requirePrivateLiveInputsForWorkspace(options.SpecPath, options.LiveConfigDir, options.RepositoryRoot, options.PrivateWorkspaceRoot); err != nil {
			return RunOutput{}, err
		}
	} else if options.LiveConfigDir != "" {
		return RunOutput{}, fmt.Errorf("--live-config-dir is only valid for private-live runs")
	}
	var externalProfile ExternalMCPProfile
	if contract.spec.EffectiveSurface() == SurfaceExternalMCP {
		if options.ExternalMCPProfile == "" {
			return RunOutput{}, fmt.Errorf("external-mcp runs require --external-mcp-profile")
		}
		externalProfile, err = loadExternalMCPProfileForWorkspace(options.ExternalMCPProfile, options.RepositoryRoot, options.PrivateWorkspaceRoot)
		if err != nil {
			return RunOutput{}, err
		}
		if err := validateExternalMCPProfileForRun(externalProfile, contract.spec, contract.scenario); err != nil {
			return RunOutput{}, err
		}
	} else if options.ExternalMCPProfile != "" {
		return RunOutput{}, fmt.Errorf("--external-mcp-profile is valid only for external-mcp runs")
	}
	if err := admitATLCoreRunContract(contract); err != nil {
		return RunOutput{}, err
	}
	if contract.spec.EffectiveSurface() == SurfaceATLMCP {
		if err := VerifyATLCapabilityCatalog(ctx, options.ATLBinary); err != nil {
			return RunOutput{}, err
		}
	}
	outputRoot, err := PreparePrivateOutputRoot(options.OutputRoot, options.RepositoryRoot)
	if err != nil {
		return RunOutput{}, err
	}
	attestation, err := newSyntheticRunAttestation(contract.spec, options.AgentBinary, options.ATLBinary, options.WrapperExecutable)
	if err != nil {
		return RunOutput{}, err
	}
	invocationSpec := contract.forAttempt().spec
	previewProviderBindings := providerCommandBindings{}
	if invocationSpec.EffectiveSurface() == SurfaceExternalMCP {
		previewProviderBindings.externalMCPServerURL = "http://127.0.0.1:<private>/mcp"
		previewProviderBindings.externalMCPBearerTokenEnv = WrapperEnvExternalMCPToken
		invocationSpec.AllowedMCPTools = []string{"reviewed_tool"}
	}
	previewConfinement := ProviderConfinement{}
	if contract.spec.Provider == "codex" && contract.spec.EffectiveBackendMode() == BackendModePrivateLive {
		previewConfinement.GuardMode = "mcp-with-skill-read"
		previewConfinement.GuardCounterPath = "/private/guard-decisions.jsonl"
		previewConfinement.WorkspaceReadRoot = "/private/workspace"
		previewConfinement.AllowedReadRoots = []string{"/private/workspace"}
		previewConfinement.SkillReadRoots = []string{"/private/workspace/.agents/skills"}
		previewConfinement.AllowedMCPTools = claudeMCPToolNamesForServer(mcpServerName(invocationSpec), invocationSpec.AllowedMCPTools)
		if contract.spec.ToolTransport == "cli" {
			previewConfinement.GuardMode = "private-cli"
			previewConfinement.AllowedReadRoots = []string{"/private/skill-read-root", "/private/workspace"}
			previewConfinement.SkillReadRoots = []string{"/private/skill-read-root"}
			previewConfinement.AllowedMCPTools = nil
		}
	}
	if contract.spec.Provider == "codex" && contract.spec.EffectiveBackendMode() == BackendModePrivateLive && contract.spec.ToolTransport == "cli" {
		previewConfinement.RequestDirectory = "/private/requests"
		previewConfinement.ResponseDirectory = "/private/responses"
	}
	if isCodexSyntheticBrokerCLI(contract.spec) {
		previewConfinement.GuardMode = "private-cli"
		previewConfinement.GuardCounterPath = "/private/guard-decisions.jsonl"
		previewConfinement.WorkspaceReadRoot = "/private/workspace"
		previewConfinement.AllowedReadRoots = []string{"/private/workspace"}
		previewConfinement.SkillReadRoots = []string{"/private/workspace/.agents/skills"}
		previewConfinement.RequestDirectory = "/private/requests"
		previewConfinement.ResponseDirectory = "/private/responses"
	}
	previewCommand, err := buildProviderCommand(invocationSpec, providerPreviewBinary(contract.spec.Provider), "<atl-binary>", "<guard>", "<workspace>", "<response-schema>", "<final-response>", pluginPreviewPath(contract.spec, options.PluginRoot), claudeGuardSettingsPath(contract.spec.Provider, "<guard-settings>"), claudeMCPConfigPath(contract.spec, "<mcp-config>"), previewConfinement, contract.responseSchema, previewProviderBindings)
	if err != nil {
		return RunOutput{}, err
	}
	preview := RunPreview{
		SchemaVersion: 1, ScenarioID: contract.scenario.ID,
		Provider: contract.spec.Provider, Variant: contract.spec.Variant,
		Category: contract.spec.EffectiveCategory(), Surface: contract.spec.EffectiveSurface(),
		SkillActivation:                contract.spec.SkillActivationIdentity(),
		PromptContractBound:            contract.promptContractSHA256 != "",
		BackendMode:                    contract.spec.EffectiveBackendMode(),
		Repetitions:                    contract.spec.Repetitions,
		MaxEstimatedCostMicroUSDTotal:  contract.spec.MaxEstimatedCostMicroUSD,
		MaxEstimatedCostMicroUSDPerRun: invocationSpec.MaxEstimatedCostMicroUSD,
		Command:                        previewCommand,
		OutputRoot:                     "<private-output-root>",
		QualitativeRubricID:            contract.rubric.ID,
	}
	if options.DryRun {
		return RunOutput{Preview: preview, Results: []Result{}}, nil
	}
	if contract.spec.Provider == "codex" && contract.spec.EffectiveToolTransport() != "mcp" {
		if contract.spec.EffectiveBackendMode() != BackendModePrivateLive && !isCodexSyntheticBrokerCLI(contract.spec) {
			return RunOutput{}, fmt.Errorf("codex synthetic model execution requires tool_transport=mcp; cli transport remains validate/dry-run only")
		}
	}
	providerAuthSession := options.providerAuthSession
	providerAuthSessionOwned := false
	providerScratchRoot := options.ScratchRoot
	if contract.spec.Provider == "codex" {
		if providerScratchRoot == "" {
			providerScratchRoot = filepath.Join(outputRoot, ".ephemeral")
			if err := mkdirPrivate(providerScratchRoot); err != nil {
				return RunOutput{}, fmt.Errorf("prepare isolated codex provider runtime")
			}
		}
		if providerAuthSession == nil {
			providerAuthSession, err = newCodexAuthSession(os.Environ())
			if err != nil {
				return RunOutput{}, err
			}
			providerAuthSessionOwned = true
		}
		defer func() {
			if providerAuthSessionOwned {
				returnErr = errors.Join(returnErr, providerAuthSession.Close())
			}
		}()
	} else if providerAuthSession != nil {
		return RunOutput{}, fmt.Errorf("isolated codex provider authentication is valid only for codex runs")
	}

	var versionRuntime *providerRuntimeCapsule
	if providerAuthSession != nil && options.qualifiedAgentVersion == "" {
		versionRuntime, err = newCodexProviderRuntime(providerScratchRoot, providerAuthSession)
		if err != nil {
			return RunOutput{}, err
		}
	}
	agentVersion, versionErr := agentRuntimeVersion(ctx, options, versionRuntime)
	if versionRuntime != nil {
		versionErr = errors.Join(versionErr, versionRuntime.Close())
	}
	if versionErr != nil {
		return RunOutput{}, versionErr
	}
	atlVersion, err := atlRuntimeVersion(ctx, options.ATLBinary)
	if err != nil {
		return RunOutput{}, fmt.Errorf("atl version: %w", err)
	}
	pluginVersion, skillDigest, err := pluginIdentity(options.PluginRoot, contract.spec.Provider)
	if err != nil {
		return RunOutput{}, err
	}

	results := make([]Result, 0, contract.spec.Repetitions)
	receipts := make([]SyntheticRunReceipt, 0, contract.spec.Repetitions)
	var totalCost int64
	var budgetExhausted bool
	for repetition := 1; repetition <= contract.spec.Repetitions; repetition++ {
		attemptContract := contract.forAttempt()
		var providerRuntime *providerRuntimeCapsule
		if providerAuthSession != nil {
			providerRuntime, err = newCodexProviderRuntime(providerScratchRoot, providerAuthSession)
			if err != nil {
				return RunOutput{}, err
			}
		}
		var receipt SyntheticRunReceipt
		result, runErr := runHeadlessOnce(ctx, attemptContract, runAttemptBindings{
			outputRoot: outputRoot, repetition: repetition,
			agentBinary: options.AgentBinary, atlBinary: options.ATLBinary,
			pluginRoot: options.PluginRoot, wrapperExecutable: options.WrapperExecutable,
			liveConfigDir: options.LiveConfigDir, scratchRoot: options.ScratchRoot,
			runtime: Runtime{
				Provider: contract.spec.Provider, AgentVersion: agentVersion,
				Model: contract.spec.Model, Reasoning: contract.spec.Reasoning,
				ATLVersion: atlVersion, PluginVersion: pluginVersion, SkillDigest: skillDigest,
				SkillActivation: contract.spec.SkillActivationIdentity(), PromptContractSHA256: contract.promptContractSHA256,
			},
			externalProfile: externalProfile, providerRuntime: providerRuntime,
			attestation: attestation, providerAttemptCommitted: options.providerAttemptCommitted,
			receipt: &receipt,
		})
		if providerRuntime != nil {
			runErr = errors.Join(runErr, providerRuntime.Close())
		}
		if runErr != nil {
			return RunOutput{}, fmt.Errorf("repetition %d: %w", repetition, runErr)
		}
		results = append(results, result)
		if attestation != nil {
			receipts = append(receipts, receipt)
		}
		if result.Coverage["estimated_cost_microusd"] {
			totalCost += result.Metrics.EstimatedCostMicroUSD
			if result.Metrics.EstimatedCostMicroUSD > attemptContract.spec.MaxEstimatedCostMicroUSD || totalCost > contract.spec.MaxEstimatedCostMicroUSD {
				budgetExhausted = true
				break
			}
		}
	}
	if err := attestation.verifyExecutables(options.AgentBinary, options.ATLBinary, options.WrapperExecutable); err != nil {
		return RunOutput{}, err
	}
	if attestation != nil {
		finalPluginVersion, finalSkillDigest, err := pluginIdentity(options.PluginRoot, contract.spec.Provider)
		if err != nil || finalPluginVersion != pluginVersion || finalSkillDigest != skillDigest {
			return RunOutput{}, fmt.Errorf("synthetic run plugin changed during execution")
		}
	}
	for _, receipt := range receipts {
		if err := writeSyntheticRunReceipt(outputRoot, receipt); err != nil {
			return RunOutput{}, err
		}
	}
	return RunOutput{Preview: preview, Results: results, EstimatedCostMicroUSDTotal: totalCost, BudgetExhausted: budgetExhausted}, nil
}

func validQualifiedAgentVersion(value string) bool {
	return strings.HasPrefix(value, "binary-sha256:") && validSHA256(strings.TrimPrefix(value, "binary-sha256:"))
}

func agentRuntimeVersion(ctx context.Context, options RunOptions, providerRuntime *providerRuntimeCapsule) (string, error) {
	if options.qualifiedAgentVersion != "" {
		if options.PrivateWorkspaceRoot == "" || !validQualifiedAgentVersion(options.qualifiedAgentVersion) {
			return "", fmt.Errorf("qualified agent version requires a private workspace and binary-sha256 identity")
		}
		return options.qualifiedAgentVersion, nil
	}
	var environment []string
	if providerRuntime != nil {
		environment = flattenEnvironment(providerRuntime.Environment())
	}
	version, err := commandVersionWithEnvironment(ctx, options.AgentBinary, environment)
	if err != nil {
		return "", fmt.Errorf("agent version: %w", err)
	}
	return version, nil
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
	if options.ScratchRoot != "" {
		if options.ScratchRoot, err = canonicalDirectory("private scratch root", options.ScratchRoot); err != nil {
			return RunOptions{}, err
		}
		if err := requirePrivateDirectory("private scratch root", options.ScratchRoot); err != nil {
			return RunOptions{}, err
		}
	}
	if options.PrivateWorkspaceRoot != "" {
		if options.PrivateWorkspaceRoot, err = canonicalDirectory("private workspace root", options.PrivateWorkspaceRoot); err != nil {
			return RunOptions{}, err
		}
		if err := validatePrivateWorkspaceRootForRuntime(options.PrivateWorkspaceRoot); err != nil {
			return RunOptions{}, err
		}
	}
	return options, nil
}

func perRepetitionCostCap(spec RunSpec) int64 {
	return spec.MaxEstimatedCostMicroUSD / int64(spec.Repetitions)
}

type resolvedRunContract struct {
	spec                 RunSpec
	scenario             Scenario
	fixture              *MockFixture
	prompt               []byte
	providerPrompt       []byte
	promptContractSHA256 string
	responseSchema       []byte
	rubric               Rubric
	workspaceTemplate    string
	specDir              string
}

func (contract resolvedRunContract) withOverrides(model string, repetitions int) (resolvedRunContract, error) {
	resolved := contract
	if model != "" {
		resolved.spec.Model = model
	}
	if repetitions != 0 {
		if repetitions < 1 || repetitions > contract.spec.Repetitions {
			return resolvedRunContract{}, fmt.Errorf("repetitions override must be in 1..%d", contract.spec.Repetitions)
		}
		resolved.spec.Repetitions = repetitions
	}
	if err := resolved.spec.Validate(); err != nil {
		return resolvedRunContract{}, err
	}
	if err := resolved.spec.ValidateAgainstScenario(resolved.scenario); err != nil {
		return resolvedRunContract{}, err
	}
	return resolved, nil
}

func (contract resolvedRunContract) forAttempt() resolvedRunContract {
	attempt := contract
	attempt.spec.MaxEstimatedCostMicroUSD = perRepetitionCostCap(contract.spec)
	return attempt
}

func resolveRunContract(specPath string) (resolvedRunContract, error) {
	if specPath == "" {
		return resolvedRunContract{}, fmt.Errorf("run options require a spec path")
	}
	file, err := os.Open(specPath)
	if err != nil {
		return resolvedRunContract{}, err
	}
	spec, decodeErr := DecodeRunSpec(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return resolvedRunContract{}, decodeErr
	}
	if closeErr != nil {
		return resolvedRunContract{}, closeErr
	}
	specPath, err = filepath.Abs(specPath)
	if err != nil {
		return resolvedRunContract{}, err
	}
	specPath, err = filepath.EvalSymlinks(specPath)
	if err != nil {
		return resolvedRunContract{}, err
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
		return resolvedRunContract{}, err
	}
	scenario, scenarioErr := DecodeScenario(scenarioFile)
	_ = scenarioFile.Close()
	if scenarioErr != nil {
		return resolvedRunContract{}, scenarioErr
	}
	if err := spec.ValidateAgainstScenario(scenario); err != nil {
		return resolvedRunContract{}, err
	}
	var fixture *MockFixture
	if spec.EffectiveBackendMode() == BackendModeSynthetic {
		fixtureFile, err := openRelative(spec.FixtureFile)
		if err != nil {
			return resolvedRunContract{}, err
		}
		decoded, fixtureErr := DecodeMockFixture(fixtureFile)
		_ = fixtureFile.Close()
		if fixtureErr != nil {
			return resolvedRunContract{}, fixtureErr
		}
		fixture = &decoded
	}
	promptPath, err := resolveRelative(spec.PromptFile)
	if err != nil {
		return resolvedRunContract{}, err
	}
	prompt, err := readBoundedFile(promptPath, maxProviderPromptBytes)
	if err != nil {
		return resolvedRunContract{}, err
	}
	if scenario.EffectiveCategory() == BenchmarkCategoryNeutralCommon {
		if err := validateNeutralCorePrompt(prompt); err != nil {
			return resolvedRunContract{}, err
		}
	}
	providerPrompt, err := effectiveProviderPrompt(spec, prompt)
	if err != nil {
		return resolvedRunContract{}, err
	}
	promptContractSHA256, err := providerPromptContractSHA256(spec, prompt, providerPrompt)
	if err != nil {
		return resolvedRunContract{}, err
	}
	responseSchemaPath, err := resolveRelative(spec.ResponseSchemaFile)
	if err != nil {
		return resolvedRunContract{}, err
	}
	responseSchema, err := readBoundedFile(responseSchemaPath, 1<<20)
	if err != nil || !json.Valid(responseSchema) {
		return resolvedRunContract{}, fmt.Errorf("response schema is invalid")
	}
	rubricFile, err := openRelative(spec.QualitativeRubricFile)
	if err != nil {
		return resolvedRunContract{}, err
	}
	rubric, rubricErr := DecodeRubric(rubricFile)
	_ = rubricFile.Close()
	if rubricErr != nil {
		return resolvedRunContract{}, rubricErr
	}
	if rubric.ScenarioID != scenario.ID {
		return resolvedRunContract{}, fmt.Errorf("qualitative rubric scenario_id %q does not match %q", rubric.ScenarioID, scenario.ID)
	}
	workspace, err := resolveRelative(spec.WorkspaceTemplate)
	if err != nil {
		return resolvedRunContract{}, err
	}
	return resolvedRunContract{spec: spec, scenario: scenario, fixture: fixture, prompt: prompt, providerPrompt: providerPrompt, promptContractSHA256: promptContractSHA256, responseSchema: responseSchema, rubric: rubric, workspaceTemplate: workspace, specDir: specDir}, nil
}

func ValidateRunSpecFile(path string) (RunSpec, Scenario, error) {
	contract, err := resolveRunContract(path)
	if err != nil {
		return RunSpec{}, Scenario{}, err
	}
	return contract.spec, contract.scenario, nil
}

type runAttemptBindings struct {
	outputRoot               string
	repetition               int
	agentBinary              string
	atlBinary                string
	pluginRoot               string
	wrapperExecutable        string
	liveConfigDir            string
	scratchRoot              string
	runtime                  Runtime
	externalProfile          ExternalMCPProfile
	providerRuntime          *providerRuntimeCapsule
	attestation              *syntheticRunAttestation
	providerAttemptCommitted func() error
	receipt                  *SyntheticRunReceipt
}

func runHeadlessOnce(parent context.Context, contract resolvedRunContract, bindings runAttemptBindings) (Result, error) {
	layout, err := prepareHeadlessAttemptLayout(contract, bindings)
	if err != nil {
		return Result{}, err
	}
	codexPrivateCLI := layout.codexPrivateCLI
	codexSyntheticBrokerCLI := layout.codexSyntheticBrokerCLI
	reviewedWriteCLI := layout.reviewedWriteCLI
	codexBrokerCLI := layout.codexBrokerCLI
	gatewayBackedMCP := layout.gatewayBackedMCP
	runDir := layout.runDir
	workspace := layout.workspace
	taskContractSHA256 := layout.taskContractSHA256
	executionContractSHA256 := layout.executionContractSHA256
	providerResponseSchemaPath := layout.providerResponseSchema
	finalPath := layout.finalPath
	transcriptPath := layout.transcriptPath
	stderrPath := layout.stderrPath
	mirrorRoot := layout.mirrorRoot
	counterPath := layout.counterPath
	guardCounterPath := layout.guardCounterPath
	wrapperDir := layout.wrapperDir
	guardPath := layout.guardPath
	cliResultDirectory := layout.cliResultDirectory
	probeExecutablePath := layout.probeExecutablePath
	settingsPath := layout.settingsPath
	resources, err := prepareHeadlessProviderResources(parent, contract, bindings, layout)
	if err != nil {
		return Result{}, err
	}
	defer resources.closeDeferred()
	atlConfigDir := resources.atlConfigDir
	httpGuardPath := resources.httpGuardPath
	cliPolicyPath := resources.cliPolicyPath
	backendEnvironment := resources.backendEnvironment
	providerConfinement := resources.providerConfinement
	brokerManifestPath := resources.brokerManifestPath
	externalAuditPath := resources.externalAuditPath
	externalCanaries := resources.externalCanaries
	providerBindings := resources.providerBindings
	mcpConfigPath := layout.mcpConfigPath

	if codexPrivateCLI {
		if bindings.providerRuntime == nil {
			return Result{}, fmt.Errorf("private codex CLI run requires an isolated provider runtime")
		}
		provisionContext, cancelProvision := context.WithTimeout(parent, 30*time.Second)
		err := provisionCodexBenchmarkPlugin(provisionContext, bindings.agentBinary, bindings.pluginRoot, bindings.providerRuntime)
		cancelProvision()
		if err != nil {
			return Result{}, err
		}
	}
	skillReadRoot := filepath.Join(bindings.pluginRoot, "skills")
	if codexPrivateCLI {
		skillReadRoot = bindings.providerRuntime.PluginSkillRoot()
		if skillReadRoot == "" {
			return Result{}, fmt.Errorf("private codex CLI run has no installed plugin skill root")
		}
	}
	if contract.spec.Provider == "codex" && !codexPrivateCLI {
		skillReadRoot = filepath.Join(workspace, ".agents", "skills")
	}
	reviewedReadRoots := []string{skillReadRoot, workspace}
	reviewedSkillReadRoots := []string{skillReadRoot}
	canonicalWorkspace := ""
	if contract.spec.EffectiveBackendMode() == BackendModePrivateLive {
		canonicalWorkspace, err = filepath.EvalSymlinks(workspace)
		if err != nil {
			return Result{}, fmt.Errorf("resolve private benchmark workspace: %w", err)
		}
		if contract.spec.Provider == "codex" && !codexPrivateCLI {
			reviewedReadRoots = []string{canonicalWorkspace}
			canonicalSkillReadRoot, canonicalErr := filepath.EvalSymlinks(skillReadRoot)
			if canonicalErr != nil {
				return Result{}, fmt.Errorf("resolve private benchmark skill read root: %w", canonicalErr)
			}
			reviewedSkillReadRoots = []string{canonicalSkillReadRoot}
		} else {
			canonicalSkillReadRoot, canonicalErr := filepath.EvalSymlinks(skillReadRoot)
			if canonicalErr != nil {
				return Result{}, fmt.Errorf("resolve private benchmark skill read root: %w", canonicalErr)
			}
			reviewedReadRoots = []string{canonicalSkillReadRoot, canonicalWorkspace}
			reviewedSkillReadRoots = []string{canonicalSkillReadRoot}
		}
		if contract.spec.Provider == "codex" {
			providerConfinement.GuardMode = "mcp-with-skill-read"
			if codexPrivateCLI {
				providerConfinement.GuardMode = "private-cli"
			}
			providerConfinement.GuardCounterPath = guardCounterPath
			providerConfinement.WorkspaceReadRoot = canonicalWorkspace
			providerConfinement.AllowedReadRoots = append([]string(nil), reviewedReadRoots...)
			providerConfinement.SkillReadRoots = append([]string(nil), reviewedSkillReadRoots...)
			providerConfinement.AllowedMCPTools = claudeMCPToolNamesForServer(mcpServerName(contract.spec), contract.spec.AllowedMCPTools)
			if codexPrivateCLI {
				providerConfinement.AllowedMCPTools = nil
			}
		}
	} else if codexSyntheticBrokerCLI {
		canonicalWorkspace, err = filepath.EvalSymlinks(workspace)
		if err != nil {
			return Result{}, fmt.Errorf("resolve synthetic benchmark workspace: %w", err)
		}
		reviewedReadRoots = []string{canonicalWorkspace}
		canonicalSkillReadRoot, canonicalErr := filepath.EvalSymlinks(skillReadRoot)
		if canonicalErr != nil {
			return Result{}, fmt.Errorf("resolve synthetic benchmark skill read root: %w", canonicalErr)
		}
		reviewedSkillReadRoots = []string{canonicalSkillReadRoot}
		providerConfinement.GuardMode = "private-cli"
		providerConfinement.GuardCounterPath = guardCounterPath
		providerConfinement.WorkspaceReadRoot = canonicalWorkspace
		providerConfinement.AllowedReadRoots = append([]string(nil), reviewedReadRoots...)
		providerConfinement.SkillReadRoots = append([]string(nil), reviewedSkillReadRoots...)
	}
	allowedReadRoots, _ := json.Marshal(reviewedReadRoots)
	allowedSkillReadRoots, _ := json.Marshal(reviewedSkillReadRoots)
	commandPlan, err := buildProviderCommand(contract.spec, bindings.agentBinary, bindings.atlBinary, guardPath, workspace, providerResponseSchemaPath, finalPath, claudePluginPath(contract.spec, bindings.pluginRoot), claudeGuardSettingsPath(contract.spec.Provider, settingsPath), mcpConfigPath, providerConfinement, contract.responseSchema, providerBindings)
	if err != nil {
		return Result{}, err
	}
	if codexBrokerCLI {
		if err := runCodexConfinementPreflight(parent, bindings.agentBinary, workspace, probeExecutablePath, brokerManifestPath, providerConfinement, bindings.providerRuntime); err != nil {
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
	ctx, cancel := context.WithTimeout(parent, time.Duration(contract.spec.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, commandPlan.Path, commandPlan.Args...)
	command.Dir = workspace
	command.Stdin = bytes.NewReader(contract.providerPrompt)
	command.Stdout = transcript
	command.Stderr = stderr
	environment := safeAgentEnvironment(os.Environ())
	if bindings.providerRuntime != nil {
		environment = bindings.providerRuntime.Environment()
	}
	environment["ATL_READ_ONLY"] = "1"
	environment["ATL_NO_UPDATE"] = "1"
	environment["ATL_CONFIG_DIR"] = atlConfigDir
	environment["ATL_MIRROR_ROOT"] = mirrorRoot
	environment[WrapperEnvRealBinary] = bindings.atlBinary
	environment[WrapperEnvCounter] = counterPath
	environment[WrapperEnvGuardCounter] = guardCounterPath
	if cliResultDirectory != "" {
		environment[WrapperEnvCLIResultDir] = cliResultDirectory
	}
	if contract.spec.AllowSyntheticWrites {
		environment[WrapperEnvAllowSyntheticWrites] = "1"
	}
	if reviewedWriteCLI {
		environment[WrapperEnvAllowReviewedWrites] = "1"
	}
	if cliPolicyPath != "" {
		environment[WrapperEnvCLIPolicyFile] = cliPolicyPath
		if brokerManifestPath != "" {
			environment[WrapperEnvCommandBrokerFile] = brokerManifestPath
			delete(environment, "ATL_NO_UPDATE")
			delete(environment, "ATL_CONFIG_DIR")
			delete(environment, "ATL_MIRROR_ROOT")
			delete(environment, WrapperEnvRealBinary)
		}
		environment[WrapperEnvGuardMode] = "private-cli"
		environment["NO_PROXY"] = "127.0.0.1,localhost"
		environment["no_proxy"] = "127.0.0.1,localhost"
	}
	if contract.spec.ToolTransport == "mcp" {
		environment[WrapperEnvGuardMode] = "mcp-only"
		if contract.spec.EffectiveBackendMode() == BackendModePrivateLive {
			environment[WrapperEnvGuardMode] = "mcp-with-skill-read"
		}
	}
	if contract.spec.EffectiveSurface() == SurfaceExternalMCP && contract.spec.Provider == "codex" {
		environment[WrapperEnvExternalMCPToken] = backendEnvironment[WrapperEnvExternalMCPToken]
	}
	if contract.spec.EffectiveSurface() == SurfaceExternalMCP || gatewayBackedMCP {
		environment["NO_PROXY"] = "127.0.0.1,localhost"
		environment["no_proxy"] = "127.0.0.1,localhost"
	}
	environment[WrapperEnvMaxDelegations] = fmt.Sprintf("%d", contract.scenario.Budgets.MaxDelegations)
	allowedCommands, _ := json.Marshal(contract.spec.AllowedATLCommands)
	environment[WrapperEnvAllowedCommands] = string(allowedCommands)
	allowedMCPTools, _ := json.Marshal(claudeMCPToolNamesForServer(mcpServerName(contract.spec), contract.spec.AllowedMCPTools))
	environment[WrapperEnvAllowedMCPTools] = string(allowedMCPTools)
	environment[WrapperEnvAllowedReadRoots] = string(allowedReadRoots)
	environment[WrapperEnvSkillReadRoots] = string(allowedSkillReadRoots)
	if contract.spec.EffectiveBackendMode() == BackendModePrivateLive || codexSyntheticBrokerCLI {
		environment[WrapperEnvWorkspaceRoot] = canonicalWorkspace
	}
	environment["PATH"] = wrapperDir
	if (contract.spec.Provider != "claude-code" || contract.spec.EffectiveToolTransport() != "mcp") && !codexSyntheticBrokerCLI {
		for name, value := range backendEnvironment {
			environment[name] = value
		}
	}
	if contract.spec.Provider == "claude-code" && contract.spec.EffectiveBackendMode() == BackendModeSynthetic &&
		contract.spec.EffectiveToolTransport() == "cli" && contract.spec.AllowSyntheticWrites {
		// Ordinary Claude synthetic-write prompts use the same plain `atl ...`
		// form as their reviewed prefix policy. The proxy still requires the
		// explicit write authority below and verifies both backend URLs are
		// disposable loopback endpoints before it will forward a mutation.
		delete(environment, "ATL_READ_ONLY")
	}
	command.Env = flattenEnvironment(environment)
	execution := executeAndCloseHeadlessProvider(headlessProviderExecutionInput{
		contract: contract, bindings: bindings, layout: layout, resources: resources,
		command: command, transcript: transcript, stderr: stderr, ctx: ctx, cancel: cancel,
	})
	if execution.gatewayCloseErr != nil {
		return Result{}, fmt.Errorf("close private-live gateway: %w", execution.gatewayCloseErr)
	}
	if execution.brokerCloseErr != nil {
		return Result{}, fmt.Errorf("close private-live command broker: %w", execution.brokerCloseErr)
	}
	if execution.externalCloseErr != nil {
		return Result{}, fmt.Errorf("close external MCP proxy: %w", execution.externalCloseErr)
	}
	if execution.timedOut {
		return Result{}, fmt.Errorf("agent exceeded %d second timeout", contract.spec.TimeoutSeconds)
	}
	if execution.guardAborted {
		return Result{}, fmt.Errorf("agent attempted a command rejected by the benchmark guard")
	}
	if execution.runErr != nil {
		return Result{}, fmt.Errorf("agent process failed: %w", execution.runErr)
	}
	if execution.closeTranscriptErr != nil || execution.closeStderrErr != nil {
		return Result{}, fmt.Errorf("close agent output: %v %v", execution.closeTranscriptErr, execution.closeStderrErr)
	}
	trajectory, err := captureHeadlessTrajectory(headlessTrajectoryCaptureInput{
		contract:          contract,
		transcriptPath:    transcriptPath,
		stderrPath:        stderrPath,
		finalPath:         finalPath,
		mcpConfigPath:     mcpConfigPath,
		externalAuditPath: externalAuditPath,
		counterPath:       counterPath,
		guardCounterPath:  guardCounterPath,
		httpGuardPath:     httpGuardPath,
		externalCanaries:  externalCanaries,
		backend:           resources.backend,
		liveGateway:       resources.liveGateway,
	})
	if err != nil {
		return Result{}, err
	}
	return finalizeHeadlessOutcome(headlessOutcomeInput{
		contract:                contract,
		trajectory:              trajectory,
		workspace:               workspace,
		runDir:                  runDir,
		durationMillis:          execution.durationMillis,
		runtime:                 bindings.runtime,
		repetition:              bindings.repetition,
		taskContractSHA256:      taskContractSHA256,
		executionContractSHA256: executionContractSHA256,
		attestation:             bindings.attestation,
		receipt:                 bindings.receipt,
	})
}

func bindSyntheticWorkspaceMirrors(ctx context.Context, atlBinary, workspace, configDir string, backendEnvironment map[string]string) error {
	roots := make([]string, 0)
	err := filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == ".git" {
			return fs.SkipDir
		}
		if entry.Name() == ".atl" {
			roots = append(roots, filepath.Dir(path))
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("discover synthetic mirror roots: %w", err)
	}
	sort.Strings(roots)
	environment := map[string]string{
		"ATL_NO_UPDATE":  "1",
		"ATL_CONFIG_DIR": configDir,
	}
	for name, value := range backendEnvironment {
		environment[name] = value
	}
	for _, root := range roots {
		for _, service := range []struct {
			name   string
			urlEnv string
		}{
			{name: "confluence", urlEnv: "ATL_CONFLUENCE_URL"},
			{name: "jira", urlEnv: "ATL_JIRA_URL"},
		} {
			if strings.TrimSpace(environment[service.urlEnv]) == "" {
				continue
			}
			preview, err := runSyntheticMirrorBind(ctx, atlBinary, workspace, environment,
				"mirror", "backend", "bind", root, "--service", service.name)
			if err != nil {
				return err
			}
			var result struct {
				BackendSHA256 string `json:"backend_sha256"`
			}
			if json.Unmarshal(preview, &result) != nil || result.BackendSHA256 == "" {
				return fmt.Errorf("preview synthetic %s mirror binding: invalid output", service.name)
			}
			if _, err := runSyntheticMirrorBind(ctx, atlBinary, workspace, environment,
				"mirror", "backend", "bind", root, "--service", service.name,
				"--apply", "--expected-backend-sha256", result.BackendSHA256, "--confirm", "BIND"); err != nil {
				return err
			}
		}
	}
	return nil
}

func runSyntheticMirrorBind(ctx context.Context, atlBinary, workspace string, environment map[string]string, args ...string) ([]byte, error) {
	stdout := &cappedCommandOutput{limit: 64 << 10}
	stderr := &cappedCommandOutput{limit: 64 << 10}
	command := exec.CommandContext(ctx, atlBinary, args...)
	command.Dir = workspace
	command.Env = flattenEnvironment(environment)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil || stdout.overflow || stderr.overflow {
		return nil, fmt.Errorf("prepare synthetic mirror backend binding")
	}
	return append([]byte(nil), stdout.data.Bytes()...), nil
}

func deriveRunnerEvidenceAttempt(proxyRecords []atlProxyRecord, admittedTools, failedTools, guardDenials int) (EvidenceAttemptTelemetry, error) {
	admitted, failed, denied := admittedTools, failedTools, guardDenials
	for _, record := range proxyRecords {
		if record.Denied {
			denied++
			continue
		}
		admitted++
		if record.ExitCode != 0 {
			failed++
		}
	}
	return NewEvidenceAttemptTelemetry(true, EvidenceAttemptCounts{
		Attempts: admitted + denied, Admitted: admitted,
		Succeeded: admitted - failed, Failed: failed, Denied: denied,
	})
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
	args := []string{plan.Path}
	args = append(args, plan.Args...)
	return ProviderCommand{Path: interpreter, Args: args}, nil
}

func runCodexConfinementPreflight(parent context.Context, agentBinary, workspace, probeExecutable, brokerManifestPath string, confinement ProviderConfinement, providerRuntime *providerRuntimeCapsule) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("prepare codex cli confinement preflight")
	}
	defer func() { _ = listener.Close() }()
	plan, err := BuildCodexConfinementProbeCommand(agentBinary, workspace, probeExecutable, confinement)
	if err != nil {
		return fmt.Errorf("prepare codex cli confinement preflight")
	}
	plan, err = resolveProviderLaunch(plan)
	if err != nil {
		return fmt.Errorf("prepare codex cli confinement preflight")
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, plan.Path, plan.Args...)
	command.Dir = workspace
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	environment := safeAgentEnvironment(os.Environ())
	if providerRuntime != nil {
		environment = providerRuntime.Environment()
	}
	if pathValue := os.Getenv("PATH"); pathValue != "" {
		environment["PATH"] = pathValue
	}
	environment[WrapperEnvCommandBrokerFile] = brokerManifestPath
	environment[WrapperEnvForbiddenNetworkAddress] = listener.Addr().String()
	command.Env = flattenEnvironment(environment)
	if err := command.Run(); err != nil {
		return fmt.Errorf("codex cli confinement preflight failed before model and backend access")
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
	_, denials, err := countGuardDecisions(path)
	return denials, err
}

func countGuardDecisions(path string) (int, int, error) {
	summary, err := readGuardDecisionSummary(path)
	return summary.Admissions, summary.Denials, err
}

type guardDecisionSummary struct {
	Admissions          int
	Denials             int
	ATLAdmissions       int
	SkillReadAdmissions int
}

func readGuardDecisionSummary(path string) (guardDecisionSummary, error) {
	data, err := readBoundedFile(path, 1<<20)
	if os.IsNotExist(err) {
		return guardDecisionSummary{}, nil
	}
	if err != nil {
		return guardDecisionSummary{}, err
	}
	var summary guardDecisionSummary
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record guardDecisionRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return guardDecisionSummary{}, fmt.Errorf("decode guard decision record: %w", err)
		}
		if record.Family != "" && record.Family != "other" && record.Family != "atl" && record.Family != "skill_read" && record.Family != "tool_result_read" &&
			record.Family != "mcp" && record.Family != "structured_output" && record.Family != "agent" && record.Family != "read" {
			return guardDecisionSummary{}, fmt.Errorf("invalid guard decision family")
		}
		switch record.Decision {
		case "allow":
			summary.Admissions++
			if record.Family == "atl" {
				summary.ATLAdmissions++
			}
			if record.Family == "skill_read" {
				summary.SkillReadAdmissions++
			}
		case "deny":
			summary.Denials++
		default:
			return guardDecisionSummary{}, fmt.Errorf("invalid guard decision %q", record.Decision)
		}
	}
	return summary, nil
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
		// An existing, successfully parsed empty audit proves that the guarded
		// transport forwarded zero requests. Only a missing audit means that HTTP
		// behavior was not observed.
		return methods, 0, true, nil
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
		return nil, 0, false, fmt.Errorf("read private-live gateway audit")
	}
	return parseLiveGatewayRecords(data)
}

func parseLiveGatewayRecords(data []byte) (map[string]int, int, bool, error) {
	methods := map[string]int{}
	identities := map[string]int{}
	forwarded := map[string]int{}
	completed := map[string]int{}
	requestBytes := map[string]int64{}
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
		if record.Sequence != sequence || (record.Service != "jira" && record.Service != "confluence") ||
			(record.Method != "GET" && record.Method != "HEAD" && record.Method != "POST" && record.Method != "PUT" && record.Method != "PATCH" && record.Method != "DELETE") ||
			len(record.RequestHMAC) != 64 || record.RequestBytes < 0 {
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
			if previous, exists := requestBytes[identity]; exists && previous != record.RequestBytes {
				return nil, 0, false, fmt.Errorf("invalid private-live gateway request-byte binding")
			}
			requestBytes[identity] = record.RequestBytes
		case "complete:allow":
			if record.Route == "" || record.Reason != "" || len(record.StatusClass) != 3 || record.StatusClass[1:] != "xx" || record.ResponseBytes < 0 || requestBytes[identity] != record.RequestBytes {
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
		// The gateway creates the audit before provider execution. A present empty
		// file therefore remains observed evidence of zero forwarded requests.
		return methods, 0, true, nil
	}
	duplicates := 0
	for _, count := range identities {
		if count > 1 {
			duplicates += count - 1
		}
	}
	return methods, duplicates, true, nil
}

func closeAndReadLiveGatewayRecords(gateway *LiveGateway) (map[string]int, int, bool, error) {
	if gateway == nil {
		return nil, 0, false, fmt.Errorf("private-live gateway is unavailable")
	}
	if err := gateway.Close(context.Background()); err != nil {
		return nil, 0, false, fmt.Errorf("close private-live gateway: %w", err)
	}
	data, err := gateway.auditEvidence()
	if err != nil {
		return nil, 0, false, errLiveGatewayAuditUnavailable
	}
	return parseLiveGatewayRecords(data)
}

func estimateCost(inputTokens, outputTokens int64, pricing Pricing) (int64, error) {
	if inputTokens < 0 || outputTokens < 0 || pricing.InputMicroUSDPerMillionTokens < 0 || pricing.OutputMicroUSDPerMillionTokens < 0 {
		return 0, fmt.Errorf("cost inputs must be non-negative")
	}
	if inputTokens > math.MaxInt64/max64(1, pricing.InputMicroUSDPerMillionTokens) || outputTokens > math.MaxInt64/max64(1, pricing.OutputMicroUSDPerMillionTokens) {
		return 0, fmt.Errorf("estimated cost overflows")
	}
	inputProduct := inputTokens * pricing.InputMicroUSDPerMillionTokens
	outputProduct := outputTokens * pricing.OutputMicroUSDPerMillionTokens
	inputWhole, outputWhole := inputProduct/1_000_000, outputProduct/1_000_000
	if inputWhole > math.MaxInt64-outputWhole {
		return 0, fmt.Errorf("estimated cost overflows")
	}
	whole := inputWhole + outputWhole
	remainder := inputProduct%1_000_000 + outputProduct%1_000_000
	extra := int64(0)
	if remainder != 0 {
		extra = (remainder-1)/1_000_000 + 1
	}
	if whole > math.MaxInt64-extra {
		return 0, fmt.Errorf("estimated cost overflows")
	}
	return whole + extra, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func commandVersionWithEnvironment(ctx context.Context, binary string, environment []string) (string, error) {
	plan, err := resolveProviderLaunch(ProviderCommand{Path: binary, Args: []string{"--version"}})
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, plan.Path, plan.Args...)
	if environment != nil {
		command.Env = environment
	}
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

func pluginIdentity(root, provider string) (string, string, error) {
	manifestPath, skillRoot, err := providerPluginLayout(root, provider)
	if err != nil {
		return "", "", err
	}
	manifest, err := readBoundedFile(manifestPath, 1<<20)
	if err != nil {
		return "", "", err
	}
	var value struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &value); err != nil || value.Version == "" {
		return "", "", fmt.Errorf("plugin manifest version is invalid")
	}
	digest, err := digestTree(skillRoot)
	return value.Version, digest, err
}

func providerPluginLayout(root, provider string) (manifest, skills string, err error) {
	switch provider {
	case "claude-code":
		return filepath.Join(root, ".claude-plugin", "plugin.json"), filepath.Join(root, "skills"), nil
	case "codex":
		codexRoot := filepath.Join(root, "plugins", "atl")
		return filepath.Join(codexRoot, ".codex-plugin", "plugin.json"), filepath.Join(codexRoot, "skills"), nil
	default:
		return "", "", fmt.Errorf("unsupported provider %q", provider)
	}
}

type digestTreeFile struct {
	path string
	info fs.FileInfo
}

func digestTree(root string) (string, error) {
	return digestTreeWithHook(root, nil)
}

func digestTreeWithHook(root string, afterInitialInventory func()) (string, error) {
	return digestTreeWithPolicy(root, afterInitialInventory, 4<<20, "atl-tree-digest-v3\x00", "skill tree")
}

func digestWorkspaceTree(root string) (string, error) {
	return digestTreeWithPolicy(root, nil, maxWorkspaceBytes, "atl-workspace-tree-digest-v1\x00", "workspace tree")
}

func digestTreeWithPolicy(root string, afterInitialInventory func(), maxFileBytes int64, domain, name string) (string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", fmt.Errorf("%s root is not a plain directory", name)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = rootHandle.Close() }()
	openedRootInfo, err := rootHandle.Stat(".")
	if err != nil || !openedRootInfo.IsDir() || !os.SameFile(rootInfo, openedRootInfo) {
		return "", fmt.Errorf("%s root changed while it was opened", name)
	}
	files, err := digestTreeInventory(rootHandle)
	if err != nil {
		return "", err
	}
	if afterInitialInventory != nil {
		afterInitialInventory()
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	var length [8]byte
	for _, treeFile := range files {
		file, err := rootHandle.Open(treeFile.path)
		if err != nil {
			return "", err
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(treeFile.info, openedInfo) {
			_ = file.Close()
			return "", fmt.Errorf("%s changed while it was hashed", name)
		}
		data, readErr := ioReadAllLimit(file, maxFileBytes)
		finalInfo, finalStatErr := file.Stat()
		closeErr := file.Close()
		if readErr != nil {
			return "", readErr
		}
		if finalStatErr != nil || !os.SameFile(openedInfo, finalInfo) || finalInfo.Size() != int64(len(data)) || !finalInfo.ModTime().Equal(openedInfo.ModTime()) {
			return "", fmt.Errorf("%s changed while it was hashed", name)
		}
		if closeErr != nil {
			return "", closeErr
		}
		relativeBytes := []byte(filepath.ToSlash(treeFile.path))
		binary.BigEndian.PutUint64(length[:], uint64(len(relativeBytes)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(relativeBytes)
		binary.BigEndian.PutUint64(length[:], uint64(len(data)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(data)
	}
	finalFiles, err := digestTreeInventory(rootHandle)
	if err != nil || !sameDigestTreeInventory(files, finalFiles) {
		return "", fmt.Errorf("skill tree changed while it was hashed")
	}
	finalRootInfo, err := os.Lstat(root)
	if err != nil || finalRootInfo.Mode()&os.ModeSymlink != 0 || !finalRootInfo.IsDir() || !os.SameFile(openedRootInfo, finalRootInfo) {
		return "", fmt.Errorf("%s root changed while it was hashed", name)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func digestTreeInventory(root *os.Root) ([]digestTreeFile, error) {
	var files []digestTreeFile
	err := fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill tree contains symlink")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill tree contains special file")
		}
		files = append(files, digestTreeFile{path: filepath.FromSlash(path), info: info})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

func sameDigestTreeInventory(first, second []digestTreeFile) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index].path != second[index].path ||
			!os.SameFile(first[index].info, second[index].info) ||
			first[index].info.Size() != second[index].info.Size() ||
			!first[index].info.ModTime().Equal(second[index].info.ModTime()) ||
			first[index].info.Mode() != second[index].info.Mode() {
			return false
		}
	}
	return true
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

func ioReadAllLimit(reader io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)
	data, err := io.ReadAll(limited)
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
func writeClaudeGuardSettings(path, guardPath, serverName string, reviewedMCPTools []string) error {
	hooks := make([]any, 0, 6)
	matchers := []string{"Bash", "Agent", "Read", "Edit", "Write", "apply_patch"}
	if len(reviewedMCPTools) > 0 {
		// An omitted matcher applies the hook to every tool. This is required
		// because some built-ins (for example Skill and ToolSearch) do not cross
		// the ordinary permission-decision path.
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
		// Headless automatic sessions cannot approve project-like MCP configs
		// interactively. Approve only the single generated server name and grant
		// only the run spec's exact dynamic tool names. The provider command uses
		// an empty built-in --tools inventory; MCP names stay in settings because
		// --allowed-tools is a permission filter rather than an inventory filter.
		settings["enabledMcpjsonServers"] = []string{serverName}
		// Keep plugin workflow guidance available to CLI-skill runs, but remove
		// the Skill built-in from typed-MCP permissions as a second control. The
		// matcher-less hook remains the global fail-closed boundary.
		settings["permissions"] = map[string]any{"allow": reviewedMCPTools, "deny": []string{"Skill"}}
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}

func writeClaudeExternalMCPConfig(path, endpoint, capability string) error {
	if endpoint == "" || capability == "" {
		return fmt.Errorf("external MCP proxy is not configured")
	}
	config := map[string]any{"mcpServers": map[string]any{externalMCPServerName: map[string]any{"type": "http", "url": endpoint, "headers": map[string]string{"Authorization": "Bearer " + capability}, "alwaysLoad": true}}}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}

// syntheticMCPMirrorRoot binds typed-MCP runs to a copied fixture mirror when
// one exists. The candidate is fixed by the harness rather than supplied by the
// model, and both the root and marker must remain real directories contained in
// the copied workspace. Other runs retain the existing isolated fallback.
func syntheticMCPMirrorRoot(workspace, fallback string) (string, error) {
	candidate := filepath.Join(workspace, "mirror")
	info, err := os.Lstat(candidate)
	if errors.Is(err, os.ErrNotExist) {
		return fallback, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("synthetic MCP fixture mirror is not a real directory")
	}
	workspaceReal, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve synthetic MCP workspace: %w", err)
	}
	candidateReal, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve synthetic MCP fixture mirror: %w", err)
	}
	rel, err := filepath.Rel(workspaceReal, candidateReal)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("synthetic MCP fixture mirror escapes the copied workspace")
	}
	marker, err := os.Lstat(filepath.Join(candidateReal, ".atl"))
	if err != nil || !marker.IsDir() || marker.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("synthetic MCP fixture mirror has no real .atl directory")
	}
	return candidateReal, nil
}

// gatewayMCPEnvironmentAllowlist bounds what a gateway-backed internal MCP
// child may inherit. Upstream URL and PAT names, the insecure-transport
// override, and the HTTP guard file are deliberately absent: that child talks
// only to the disposable loopback gateway, which owns the real credential.
var gatewayMCPEnvironmentNames = []string{
	"ATL_READ_ONLY", "ATL_NO_UPDATE", "ATL_CONFIG_DIR", "ATL_MIRROR_ROOT", "NO_PROXY", "no_proxy",
}

var gatewayMCPEnvironmentAllowlist = func() map[string]struct{} {
	allowed := make(map[string]struct{}, len(gatewayMCPEnvironmentNames))
	for _, name := range gatewayMCPEnvironmentNames {
		allowed[name] = struct{}{}
	}
	return allowed
}()

func gatewayMCPEnvironment(atlConfigDir, mirrorRoot string) map[string]string {
	values := map[string]string{
		"ATL_READ_ONLY": "1", "ATL_NO_UPDATE": "1",
		"ATL_CONFIG_DIR": atlConfigDir, "ATL_MIRROR_ROOT": mirrorRoot,
		"NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
	}
	environment := make(map[string]string, len(gatewayMCPEnvironmentNames))
	for _, name := range gatewayMCPEnvironmentNames {
		environment[name] = values[name]
	}
	return environment
}

func validateGatewayMCPEnvironment(environment map[string]string) error {
	for name := range environment {
		if _, ok := gatewayMCPEnvironmentAllowlist[name]; !ok {
			return fmt.Errorf("gateway-backed MCP environment has an unsupported variable")
		}
	}
	return nil
}

func writeClaudeMCPConfig(path, atlBinary string, childArgs []string, environment map[string]string) error {
	config := map[string]any{
		"mcpServers": map[string]any{
			"atl": map[string]any{
				"type": "stdio", "command": atlBinary,
				"args": childArgs, "env": environment,
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
func isSyntheticTypedMCP(spec RunSpec) bool {
	return spec.EffectiveBackendMode() == BackendModeSynthetic && spec.EffectiveToolTransport() == "mcp"
}
func shouldInstallCodexBenchmarkSkills(spec RunSpec) bool {
	privateCLI := spec.EffectiveBackendMode() == BackendModePrivateLive && spec.EffectiveToolTransport() == "cli"
	return spec.Provider == "codex" && !privateCLI && !isSyntheticTypedMCP(spec)
}
func claudePluginPath(spec RunSpec, root string) string {
	if spec.Provider == "claude-code" && !isSyntheticTypedMCP(spec) {
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
func pluginPreviewPath(spec RunSpec, root string) string {
	if claudePluginPath(spec, root) == "" {
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
