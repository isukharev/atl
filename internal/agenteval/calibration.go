package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	CodexCLICalibrationSchemaVersion = 1
	calibrationOutputLimit           = 64 << 10
	maxCodexCLICalibrationTimeout    = 300
)

var (
	codexCLICalibrationPrompt = []byte("Use the shell tool to run the literal command `atl version` exactly once. Do not run any other command. After the command succeeds, return the required JSON object.\n")
	codexCLICalibrationSchema = []byte(`{"type":"object","properties":{"ok":{"type":"boolean","const":true}},"required":["ok"],"additionalProperties":false}`)
)

// CodexCLICalibrationOptions describes the backend-free provider preflight.
// It deliberately has no live config, URL, credential, fixture, or gateway
// fields: those inputs cannot be projected into this invocation by accident.
type CodexCLICalibrationOptions struct {
	OutputRoot               string
	RepositoryRoot           string
	AgentBinary              string
	ATLBinary                string
	PluginRoot               string
	WrapperExecutable        string
	ScratchRoot              string
	Model                    string
	Reasoning                string
	TimeoutSeconds           int
	MaxEstimatedCostMicroUSD int64
	Pricing                  Pricing
	providerAuthSession      *codexAuthSession
	providerAttemptCommitted func() error
}

// CodexCLICalibrationReceipt is content-free evidence that the actual Codex
// model-facing shell route, hook, shim, and broker were exercised successfully.
type CodexCLICalibrationReceipt struct {
	SchemaVersion         int    `json:"schema_version"`
	ContractSHA256        string `json:"contract_sha256"`
	Passed                bool   `json:"passed"`
	CommandFamily         string `json:"command_family"`
	CommandExecutions     int    `json:"command_executions"`
	BrokeredInvocations   int    `json:"brokered_invocations"`
	GuardAdmissions       int    `json:"guard_admissions"`
	GuardATLAdmissions    int    `json:"guard_atl_admissions"`
	GuardDenials          int    `json:"guard_denials"`
	BackendRequests       int    `json:"backend_requests"`
	RemoteWrites          int    `json:"remote_writes"`
	StdoutBytes           int64  `json:"stdout_bytes"`
	InputTokens           int64  `json:"input_tokens"`
	OutputTokens          int64  `json:"output_tokens"`
	EstimatedCostMicroUSD int64  `json:"estimated_cost_microusd"`
	DurationMillis        int64  `json:"duration_millis"`
}

// CodexCLICalibrationContract is the durable, content-free identity of the
// fixed calibration invocation. Recomputing it binds plan review to the exact
// prompt, schema, policy, provider launch, model settings, pricing, cost cap,
// timeout, and output bound without persisting provider-visible text twice.
type CodexCLICalibrationContract struct {
	SchemaVersion            int     `json:"schema_version"`
	SHA256                   string  `json:"sha256"`
	Provider                 string  `json:"provider"`
	Model                    string  `json:"model"`
	Reasoning                string  `json:"reasoning,omitempty"`
	TimeoutSeconds           int     `json:"timeout_seconds"`
	MaxEstimatedCostMicroUSD int64   `json:"max_estimated_cost_microusd"`
	Pricing                  Pricing `json:"pricing"`
	MaxCommandOutputBytes    int64   `json:"max_command_output_bytes"`
}

func (c CodexCLICalibrationContract) Validate() error {
	if c.SchemaVersion != CodexCLICalibrationSchemaVersion || c.Provider != "codex" || c.Model == "" ||
		c.TimeoutSeconds < 1 || c.TimeoutSeconds > 300 || c.MaxEstimatedCostMicroUSD < 1 ||
		c.MaxCommandOutputBytes != calibrationOutputLimit {
		return fmt.Errorf("invalid codex cli calibration contract")
	}
	rebuilt, err := BuildCodexCLICalibrationContract(c.Model, c.Reasoning, c.TimeoutSeconds, c.MaxEstimatedCostMicroUSD, c.Pricing)
	if err != nil || rebuilt.SHA256 != c.SHA256 {
		return fmt.Errorf("invalid codex cli calibration contract")
	}
	return nil
}

func (r CodexCLICalibrationReceipt) Validate(contract CodexCLICalibrationContract) error {
	if contract.Validate() != nil || r.SchemaVersion != CodexCLICalibrationSchemaVersion || !r.Passed ||
		r.ContractSHA256 != contract.SHA256 || r.CommandFamily != "atl_version" || r.CommandExecutions != 1 ||
		r.BrokeredInvocations != 1 || r.GuardAdmissions != 1 || r.GuardATLAdmissions != 1 || r.GuardDenials != 0 || r.BackendRequests != 0 ||
		r.RemoteWrites != 0 || r.StdoutBytes < 1 || r.StdoutBytes > contract.MaxCommandOutputBytes ||
		r.InputTokens < 1 || r.OutputTokens < 1 || r.EstimatedCostMicroUSD < 0 || r.DurationMillis < 0 {
		return fmt.Errorf("invalid codex cli calibration receipt")
	}
	cost, err := estimateCost(r.InputTokens, r.OutputTokens, contract.Pricing)
	if err != nil || cost != r.EstimatedCostMicroUSD || cost > contract.MaxEstimatedCostMicroUSD {
		return fmt.Errorf("invalid codex cli calibration receipt")
	}
	return nil
}

// BuildCodexCLICalibrationContract is pure: path placeholders are fixed and no
// filesystem, provider, environment, or backend state is consulted.
func BuildCodexCLICalibrationContract(model, reasoning string, timeoutSeconds int, maxEstimatedCostMicroUSD int64, pricing Pricing) (CodexCLICalibrationContract, error) {
	if model == "" || timeoutSeconds < 1 || timeoutSeconds > maxCodexCLICalibrationTimeout || maxEstimatedCostMicroUSD < 1 ||
		pricing.InputMicroUSDPerMillionTokens < 1 || pricing.OutputMicroUSDPerMillionTokens < 1 {
		return CodexCLICalibrationContract{}, fmt.Errorf("invalid codex cli calibration inputs")
	}
	options := CodexCLICalibrationOptions{
		Model: model, Reasoning: reasoning, TimeoutSeconds: timeoutSeconds,
		MaxEstimatedCostMicroUSD: maxEstimatedCostMicroUSD, Pricing: pricing,
	}
	spec := calibrationRunSpec(options)
	confinement := ProviderConfinement{
		RequestDirectory: "/private/requests", ResponseDirectory: "/private/responses",
		GuardMode: "provider-calibration", GuardCounterPath: "/private/guard-decisions.jsonl",
		WorkspaceReadRoot: "/private/workspace",
		AllowedReadRoots:  []string{"/private/installed-plugin-skills", "/private/workspace"},
	}
	command, err := BuildProviderCommand(spec, "codex", "/private/atl", "/private/guard", "/private/workspace", "/private/response-schema.json", "/private/final.json", "", "", "", confinement, codexCLICalibrationSchema)
	if err != nil {
		return CodexCLICalibrationContract{}, err
	}
	envelope := struct {
		SchemaVersion         int              `json:"schema_version"`
		Prompt                []byte           `json:"prompt"`
		ResponseSchema        []byte           `json:"response_schema"`
		CommandPolicy         CLICommandPolicy `json:"command_policy"`
		ProviderCommand       ProviderCommand  `json:"provider_command"`
		Model                 string           `json:"model"`
		Reasoning             string           `json:"reasoning,omitempty"`
		TimeoutSeconds        int              `json:"timeout_seconds"`
		CostCapMicroUSD       int64            `json:"cost_cap_microusd"`
		Pricing               Pricing          `json:"pricing"`
		MaxCommandOutputBytes int64            `json:"max_command_output_bytes"`
	}{
		SchemaVersion: 1, Prompt: codexCLICalibrationPrompt,
		ResponseSchema: codexCLICalibrationSchema, CommandPolicy: calibrationCLICommandPolicy(),
		ProviderCommand: command, Model: model, Reasoning: reasoning,
		TimeoutSeconds: timeoutSeconds, CostCapMicroUSD: maxEstimatedCostMicroUSD,
		Pricing: pricing, MaxCommandOutputBytes: calibrationOutputLimit,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return CodexCLICalibrationContract{}, err
	}
	return CodexCLICalibrationContract{
		SchemaVersion: CodexCLICalibrationSchemaVersion, SHA256: sha256HexBytes(data),
		Provider: "codex", Model: model, Reasoning: reasoning,
		TimeoutSeconds: timeoutSeconds, MaxEstimatedCostMicroUSD: maxEstimatedCostMicroUSD,
		Pricing: pricing, MaxCommandOutputBytes: calibrationOutputLimit,
	}, nil
}

// RunCodexCLICalibration performs one paid, backend-free Codex invocation. It
// is a study precondition, not a benchmark arm. Callers must durably commit the
// provider attempt in providerAttemptCommitted because an ambiguous spawn must
// never be replayed automatically.
func RunCodexCLICalibration(parent context.Context, options CodexCLICalibrationOptions) (receipt CodexCLICalibrationReceipt, returnErr error) {
	if parent == nil || options.OutputRoot == "" || options.RepositoryRoot == "" || options.AgentBinary == "" || options.ATLBinary == "" || options.PluginRoot == "" || options.WrapperExecutable == "" || options.ScratchRoot == "" || options.providerAuthSession == nil || options.providerAttemptCommitted == nil {
		return receipt, fmt.Errorf("codex cli calibration requires private output, repository, provider, atl, plugin, wrapper, scratch, authentication, and attempt-boundary inputs")
	}
	if options.TimeoutSeconds < 1 || options.TimeoutSeconds > maxCodexCLICalibrationTimeout {
		return receipt, fmt.Errorf("codex cli calibration timeout_seconds must be in 1..%d", maxCodexCLICalibrationTimeout)
	}
	contract, err := BuildCodexCLICalibrationContract(options.Model, options.Reasoning, options.TimeoutSeconds, options.MaxEstimatedCostMicroUSD, options.Pricing)
	if err != nil {
		return receipt, err
	}
	runOptions, err := canonicalizeRunOptions(RunOptions{
		RepositoryRoot: options.RepositoryRoot, AgentBinary: options.AgentBinary,
		ATLBinary: options.ATLBinary, PluginRoot: options.PluginRoot,
		WrapperExecutable: options.WrapperExecutable, ScratchRoot: options.ScratchRoot,
	})
	if err != nil {
		return receipt, err
	}
	outputRoot, err := filepath.Abs(options.OutputRoot)
	if err != nil {
		return receipt, err
	}
	outputRoot, err = filepath.EvalSymlinks(outputRoot)
	if err != nil {
		return receipt, fmt.Errorf("calibration output root: %w", err)
	}
	if err := requirePrivateDirectory("calibration output root", outputRoot); err != nil {
		return receipt, err
	}
	runDir := filepath.Join(outputRoot, "provider-calibration")
	if err := mkdirPrivateWithin(outputRoot, runDir); err != nil {
		return receipt, err
	}
	workspace := filepath.Join(runDir, "workspace")
	evalDir := filepath.Join(runDir, ".atl-eval")
	binDir := filepath.Join(runDir, "bin")
	for _, directory := range []string{workspace, evalDir, binDir} {
		if err := mkdirPrivate(directory); err != nil {
			return receipt, err
		}
	}
	for name, path := range map[string]string{
		wrapperName():          runOptions.WrapperExecutable,
		guardName():            runOptions.WrapperExecutable,
		confinementProbeName(): runOptions.WrapperExecutable,
	} {
		if err := copyExecutable(path, filepath.Join(binDir, name)); err != nil {
			return receipt, err
		}
	}
	guardPath := filepath.Join(binDir, guardName())
	probePath := filepath.Join(binDir, confinementProbeName())
	responseSchemaPath := filepath.Join(runDir, "response-schema.json")
	finalPath := filepath.Join(runDir, "final.json")
	transcriptPath := filepath.Join(runDir, "transcript.jsonl")
	stderrPath := filepath.Join(runDir, "agent.stderr")
	if err := writePrivateFile(responseSchemaPath, codexCLICalibrationSchema); err != nil {
		return receipt, err
	}
	requestDir := filepath.Join(evalDir, "command-broker-requests")
	responseDir := filepath.Join(evalDir, "command-broker-responses")
	for _, directory := range []string{requestDir, responseDir} {
		if err := mkdirPrivate(directory); err != nil {
			return receipt, err
		}
	}
	counterPath := filepath.Join(requestDir, "atl-invocations.jsonl")
	guardCounterPath := filepath.Join(evalDir, "guard-decisions.jsonl")
	policy := calibrationCLICommandPolicy()
	policyData, err := EncodeCLICommandPolicy(policy)
	if err != nil {
		return receipt, err
	}
	policyPath := filepath.Join(evalDir, "cli-policy.json")
	if err := writePrivateFile(policyPath, policyData); err != nil {
		return receipt, err
	}
	manifestPath := filepath.Join(evalDir, "command-broker.json")
	brokerEnvironment := map[string]string{"ATL_READ_ONLY": "1", "ATL_NO_UPDATE": "1"}
	for _, name := range []string{"LANG", "LC_ALL", "LC_CTYPE", "TZ"} {
		if value := os.Getenv(name); value != "" {
			brokerEnvironment[name] = value
		}
	}
	brokerTimeout := time.Duration(options.TimeoutSeconds) * time.Second
	if brokerTimeout > 2*time.Minute {
		brokerTimeout = 2 * time.Minute
	}
	broker, err := StartCommandBroker(CommandBrokerConfig{
		RequestDirectory: requestDir, ResponseDirectory: responseDir,
		ManifestPath: manifestPath, RealBinary: runOptions.ATLBinary,
		Policy: policy, Environment: flattenEnvironment(brokerEnvironment),
		MaxStdoutBytes: calibrationOutputLimit, MaxStderrBytes: calibrationOutputLimit,
		CommandTimeout: brokerTimeout,
	})
	if err != nil {
		return receipt, err
	}
	brokerClosed := false
	defer func() {
		if !brokerClosed {
			returnErr = errors.Join(returnErr, broker.Close())
		}
	}()

	providerRuntime, err := newCodexProviderRuntime(runOptions.ScratchRoot, options.providerAuthSession)
	if err != nil {
		return receipt, err
	}
	defer func() { returnErr = errors.Join(returnErr, providerRuntime.Close()) }()
	provisionContext, cancelProvision := context.WithTimeout(parent, 30*time.Second)
	err = provisionCodexBenchmarkPlugin(provisionContext, runOptions.AgentBinary, runOptions.PluginRoot, providerRuntime)
	cancelProvision()
	if err != nil {
		return receipt, err
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return receipt, err
	}
	canonicalSkillRoot, err := filepath.EvalSymlinks(providerRuntime.PluginSkillRoot())
	if err != nil {
		return receipt, fmt.Errorf("resolve calibration skill root: %w", err)
	}
	confinement := ProviderConfinement{
		RequestDirectory: requestDir, ResponseDirectory: responseDir,
		GuardMode: "provider-calibration", GuardCounterPath: guardCounterPath,
		WorkspaceReadRoot: canonicalWorkspace,
		AllowedReadRoots:  []string{canonicalSkillRoot, canonicalWorkspace},
	}
	if err := runCodexConfinementPreflight(parent, runOptions.AgentBinary, canonicalWorkspace, probePath, manifestPath, confinement, providerRuntime); err != nil {
		return receipt, err
	}
	spec := calibrationRunSpec(options)
	plan, err := BuildProviderCommand(spec, runOptions.AgentBinary, runOptions.ATLBinary, guardPath, canonicalWorkspace, responseSchemaPath, finalPath, "", "", "", confinement, codexCLICalibrationSchema)
	if err != nil {
		return receipt, err
	}
	plan, err = resolveProviderLaunch(plan)
	if err != nil {
		return receipt, err
	}
	transcript, err := os.OpenFile(transcriptPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return receipt, err
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = transcript.Close()
		return receipt, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(options.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, plan.Path, plan.Args...)
	command.Dir = canonicalWorkspace
	command.Stdin = bytes.NewReader(codexCLICalibrationPrompt)
	command.Stdout = transcript
	command.Stderr = stderr
	environment := providerRuntime.Environment()
	environment["PATH"] = binDir
	environment["ATL_READ_ONLY"] = "1"
	environment["ATL_EVAL_COUNTER"] = counterPath
	environment["ATL_EVAL_GUARD_COUNTER"] = guardCounterPath
	environment["ATL_EVAL_CLI_POLICY_FILE"] = policyPath
	environment["ATL_EVAL_COMMAND_BROKER_FILE"] = manifestPath
	environment["ATL_EVAL_GUARD_MODE"] = "provider-calibration"
	allowedRoots, _ := json.Marshal(confinement.AllowedReadRoots)
	environment["ATL_EVAL_ALLOWED_READ_ROOTS"] = string(allowedRoots)
	environment["ATL_EVAL_WORKSPACE_ROOT"] = canonicalWorkspace
	command.Env = flattenEnvironment(environment)

	started := time.Now()
	if err := options.providerAttemptCommitted(); err != nil {
		_ = transcript.Close()
		_ = stderr.Close()
		return receipt, fmt.Errorf("persist calibration provider attempt boundary: %w", err)
	}
	if err := providerRuntime.verifyPluginPackage(); err != nil {
		_ = transcript.Close()
		_ = stderr.Close()
		return receipt, err
	}
	runErr := command.Run()
	duration := time.Since(started).Milliseconds()
	closeTranscriptErr := transcript.Close()
	closeStderrErr := stderr.Close()
	brokerErr := broker.Close()
	brokerClosed = true
	if brokerErr != nil {
		return receipt, fmt.Errorf("close calibration command broker: %w", brokerErr)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return receipt, fmt.Errorf("calibration provider exceeded %d second timeout", options.TimeoutSeconds)
	}
	if runErr != nil {
		return receipt, fmt.Errorf("calibration provider process failed: %w", runErr)
	}
	if closeTranscriptErr != nil || closeStderrErr != nil {
		return receipt, fmt.Errorf("close calibration provider output: %v %v", closeTranscriptErr, closeStderrErr)
	}
	transcriptData, err := readBoundedFile(transcriptPath, 64<<20)
	if err != nil {
		return receipt, err
	}
	finalData, err := readBoundedFile(finalPath, 4<<20)
	if err != nil {
		return receipt, err
	}
	providerMetrics, final, err := ParseProviderOutput("codex", transcriptData, finalData)
	if err != nil {
		return receipt, err
	}
	records, err := readProxyRecords(counterPath)
	if err != nil {
		return receipt, err
	}
	guardSummary, err := readGuardDecisionSummary(guardCounterPath)
	if err != nil {
		return receipt, err
	}
	if err := validateCalibrationEvidence(providerMetrics, records, guardSummary, final); err != nil {
		return receipt, err
	}
	if err := verifyCalibrationCommandSlot(requestDir); err != nil {
		return receipt, err
	}
	if !providerMetrics.Coverage["input_tokens"] || !providerMetrics.Coverage["output_tokens"] {
		return receipt, fmt.Errorf("calibration provider did not report token usage")
	}
	cost, err := estimateCost(providerMetrics.InputTokens, providerMetrics.OutputTokens, options.Pricing)
	if err != nil {
		return receipt, err
	}
	if cost > options.MaxEstimatedCostMicroUSD {
		return receipt, fmt.Errorf("calibration exceeded its reviewed cost cap")
	}
	receipt = CodexCLICalibrationReceipt{
		SchemaVersion: CodexCLICalibrationSchemaVersion, ContractSHA256: contract.SHA256, Passed: true,
		CommandFamily:       "atl_version",
		CommandExecutions:   providerMetrics.CommandExecutions,
		BrokeredInvocations: 1, GuardAdmissions: guardSummary.Admissions, GuardATLAdmissions: guardSummary.ATLAdmissions,
		GuardDenials:    guardSummary.Denials,
		BackendRequests: 0, RemoteWrites: 0, StdoutBytes: records[0].StdoutBytes,
		InputTokens: providerMetrics.InputTokens, OutputTokens: providerMetrics.OutputTokens,
		EstimatedCostMicroUSD: cost, DurationMillis: duration,
	}
	if err := receipt.Validate(contract); err != nil {
		return CodexCLICalibrationReceipt{}, err
	}
	return receipt, nil
}

func codexCLICalibrationTimeout(treatmentTimeoutSeconds int) int {
	return min(treatmentTimeoutSeconds, maxCodexCLICalibrationTimeout)
}

func calibrationCLICommandPolicy() CLICommandPolicy {
	return CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{
		Name: "atl_version", Command: []string{"version"}, MaxInvocations: 1,
	}}}
}

func calibrationRunSpec(options CodexCLICalibrationOptions) RunSpec {
	return RunSpec{
		SchemaVersion: RunSpecSchemaVersion, BackendMode: BackendModeProviderCalibration,
		ScenarioFile: "internal-scenario.json", Provider: "codex", Variant: "provider-calibration",
		Model: options.Model, Reasoning: options.Reasoning, PromptFile: "internal-prompt.md",
		ResponseSchemaFile: "internal-response.json", QualitativeRubricFile: "internal-rubric.json",
		WorkspaceTemplate: "workspace", Repetitions: 1, TimeoutSeconds: options.TimeoutSeconds,
		MaxEstimatedCostMicroUSD: options.MaxEstimatedCostMicroUSD, Pricing: options.Pricing,
		ToolTransport: "cli", AllowedTools: []string{"Bash(atl *)"},
		AllowedCLICommands: calibrationCLICommandPolicy().Rules,
		Checks:             []RunCheck{{Name: "calibration_response", Kind: "json_equals", Pointer: "/ok", Expected: json.RawMessage("true")}},
	}
}

func calibrationResponseSucceeded(data []byte) bool {
	var response struct {
		OK bool `json:"ok"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&response) == nil && response.OK && decoder.Decode(new(any)) == io.EOF
}

func validateCalibrationEvidence(metrics ProviderMetrics, records []atlProxyRecord, guardSummary guardDecisionSummary, final []byte) error {
	if !calibrationResponseSucceeded(final) {
		return fmt.Errorf("calibration provider response did not match the fixed success contract")
	}
	if len(records) != 1 || records[0].CommandFamily != "atl_version" || records[0].Denied || records[0].ExitCode != 0 || records[0].StdoutBytes < 1 || records[0].StdoutBytes > calibrationOutputLimit || guardSummary.Admissions != 1 || guardSummary.ATLAdmissions != 1 || guardSummary.Denials != 0 || metrics.CommandExecutions != 1 {
		return fmt.Errorf("calibration did not observe exactly one admitted successful brokered atl version invocation")
	}
	return nil
}

func verifyCalibrationCommandSlot(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	const wanted = "cli-slot-atl_version-1"
	seen := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "cli-slot-") {
			continue
		}
		if entry.Name() != wanted || seen {
			return fmt.Errorf("calibration command policy observed an unexpected command family")
		}
		seen = true
	}
	if !seen {
		return fmt.Errorf("calibration command policy did not observe atl_version")
	}
	return nil
}
