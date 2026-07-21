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
	"strconv"
	"strings"
	"time"
)

const (
	CodexCLICalibrationSchemaVersion = 1
	calibrationOutputLimit           = 64 << 10
	maxCodexCLICalibrationTimeout    = 300
)

type CodexCLICalibrationStatus string

const (
	CodexCLICalibrationProcessFailed        CodexCLICalibrationStatus = "process_failed"
	CodexCLICalibrationResponseSchemaFailed CodexCLICalibrationStatus = "response_schema_failed"
	CodexCLICalibrationPolicyDenied         CodexCLICalibrationStatus = "policy_denied"
	CodexCLICalibrationModelNonInvocation   CodexCLICalibrationStatus = "model_non_invocation"
	CodexCLICalibrationInvocationFailed     CodexCLICalibrationStatus = "invocation_failed"
	CodexCLICalibrationSucceeded            CodexCLICalibrationStatus = "succeeded"
)

type CodexCLICalibrationFailure struct {
	Status CodexCLICalibrationStatus
}

func (e *CodexCLICalibrationFailure) Error() string {
	return "codex cli calibration status " + string(e.Status)
}

var (
	codexCLICalibrationPrompt            = []byte("Use the shell tool to run the literal command `atl version` exactly once. Do not run any other command. Copy the version, commit, and build_state string values from its JSON output exactly into the required JSON object; do not infer or invent them.\n")
	codexCLICalibrationSchema            = []byte(`{"type":"object","properties":{"version":{"type":"string","minLength":1},"commit":{"type":"string","minLength":1},"build_state":{"type":"string","enum":["clean","dirty","unknown"]}},"required":["version","commit","build_state"],"additionalProperties":false}`)
	legacyToolQualifiedCalibrationPrompt = []byte("Use the shell tool to run the literal command `atl version` exactly once. Do not run any other command. Copy the version, commit, and build_state string values from its JSON output exactly into the required JSON object; do not infer or invent them.\n")
	legacyToolQualifiedCalibrationSchema = []byte(`{"type":"object","properties":{"version":{"type":"string","minLength":1},"commit":{"type":"string","minLength":1},"build_state":{"type":"string","enum":["clean","dirty","unknown"]}},"required":["version","commit","build_state"],"additionalProperties":false}`)
)

const legacyToolQualifiedCalibrationDeveloperInstructions = "This is an evidence task. Use the literal atl executable through the shell tool to retrieve the evidence required for the answer. Make only the minimum necessary invocation or invocations allowed by the reviewed command policy. Base the answer on the returned evidence; a no-tool answer or an answer based on assumptions is invalid for this benchmark. Never use apply_patch, Edit, Write, or direct filesystem operations to create, inspect, or modify command-broker manifests or request/response files. If evidence retrieval through atl fails, do not invent or use an alternate broker-file protocol; return the failure through the required response schema."

type codexCLICalibrationResponse struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	BuildState string `json:"build_state"`
}

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
	if !validCodexCLICalibrationContractFields(c) {
		return fmt.Errorf("invalid codex cli calibration contract")
	}
	rebuilt, err := BuildCodexCLICalibrationContract(c.Model, c.Reasoning, c.TimeoutSeconds, c.MaxEstimatedCostMicroUSD, c.Pricing)
	if err != nil || rebuilt.SHA256 != c.SHA256 {
		return fmt.Errorf("invalid codex cli calibration contract")
	}
	return nil
}

func (c CodexCLICalibrationContract) validateLegacyToolQualified() error {
	if !validCodexCLICalibrationContractFields(c) {
		return fmt.Errorf("invalid legacy codex cli calibration contract")
	}
	rebuilt, err := buildLegacyToolQualifiedCalibrationContract(c.Model, c.Reasoning, c.TimeoutSeconds, c.MaxEstimatedCostMicroUSD, c.Pricing)
	if err != nil || rebuilt.SHA256 != c.SHA256 {
		return fmt.Errorf("invalid legacy codex cli calibration contract")
	}
	return nil
}

func validCodexCLICalibrationContractFields(c CodexCLICalibrationContract) bool {
	return c.SchemaVersion == CodexCLICalibrationSchemaVersion && c.Provider == "codex" && c.Model != "" &&
		c.TimeoutSeconds >= 1 && c.TimeoutSeconds <= maxCodexCLICalibrationTimeout && c.MaxEstimatedCostMicroUSD >= 1 &&
		c.MaxCommandOutputBytes == calibrationOutputLimit
}

func (r CodexCLICalibrationReceipt) Validate(contract CodexCLICalibrationContract) error {
	return validateCodexCLICalibrationReceipt(r, contract, false)
}

func (r CodexCLICalibrationReceipt) validateLegacyToolQualified(contract CodexCLICalibrationContract) error {
	return validateCodexCLICalibrationReceipt(r, contract, true)
}

func validateCodexCLICalibrationReceipt(r CodexCLICalibrationReceipt, contract CodexCLICalibrationContract, legacyToolQualified bool) error {
	contractErr := contract.Validate()
	if legacyToolQualified {
		contractErr = contract.validateLegacyToolQualified()
	}
	if contractErr != nil || r.SchemaVersion != CodexCLICalibrationSchemaVersion || !r.Passed ||
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
	return buildCodexCLICalibrationContract(model, reasoning, timeoutSeconds, maxEstimatedCostMicroUSD, pricing, false)
}

func buildLegacyToolQualifiedCalibrationContract(model, reasoning string, timeoutSeconds int, maxEstimatedCostMicroUSD int64, pricing Pricing) (CodexCLICalibrationContract, error) {
	return buildCodexCLICalibrationContract(model, reasoning, timeoutSeconds, maxEstimatedCostMicroUSD, pricing, true)
}

func buildCodexCLICalibrationContract(model, reasoning string, timeoutSeconds int, maxEstimatedCostMicroUSD int64, pricing Pricing, legacyToolQualified bool) (CodexCLICalibrationContract, error) {
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
		SkillReadRoots:    []string{"/private/installed-plugin-skills"},
	}
	responseSchema := codexCLICalibrationSchema
	var command ProviderCommand
	var err error
	commandPolicy := calibrationCLICommandPolicy()
	prompt := codexCLICalibrationPrompt
	if legacyToolQualified {
		confinement.SkillReadRoots = nil
		responseSchema = legacyToolQualifiedCalibrationSchema
		prompt = legacyToolQualifiedCalibrationPrompt
		commandPolicy = CLICommandPolicy{SchemaVersion: 1, Rules: []CLICommandRule{{Name: "atl_version", Command: []string{"version"}, MaxInvocations: 1}}}
		command, err = buildLegacyToolQualifiedCalibrationProviderCommand(model, reasoning, confinement)
	} else {
		command, err = BuildProviderCommand(spec, "codex", "/private/atl", "/private/guard", "/private/workspace", "/private/response-schema.json", "/private/final.json", "", "", "", confinement, responseSchema)
	}
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
		SchemaVersion: 1, Prompt: prompt,
		ResponseSchema: responseSchema, CommandPolicy: commandPolicy,
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

func buildLegacyToolQualifiedCalibrationProviderCommand(model, reasoning string, confinement ProviderConfinement) (ProviderCommand, error) {
	if model == "" || confinement.GuardMode != "provider-calibration" || len(confinement.AllowedMCPTools) != 0 ||
		!validCodexHookPath(confinement.GuardCounterPath) || !validCodexHookReadRoot(confinement.WorkspaceReadRoot) ||
		len(confinement.AllowedReadRoots) != 2 || confinement.AllowedReadRoots[0] != "/private/installed-plugin-skills" ||
		confinement.AllowedReadRoots[1] != confinement.WorkspaceReadRoot || len(confinement.SkillReadRoots) != 0 ||
		!validConfinementDirectory(confinement.RequestDirectory) || !validConfinementDirectory(confinement.ResponseDirectory) ||
		confinement.RequestDirectory == confinement.ResponseDirectory {
		return ProviderCommand{}, fmt.Errorf("invalid legacy codex calibration confinement")
	}
	roots, err := json.Marshal(confinement.AllowedReadRoots)
	if err != nil {
		return ProviderCommand{}, err
	}
	tools, err := json.Marshal(confinement.AllowedMCPTools)
	if err != nil {
		return ProviderCommand{}, err
	}
	hookCommand := "ATL_EVAL_GUARD_MODE=" + shellSingleQuote(confinement.GuardMode) +
		" ATL_EVAL_GUARD_COUNTER=" + shellSingleQuote(confinement.GuardCounterPath) +
		" ATL_EVAL_ALLOWED_MCP_TOOLS=" + shellSingleQuote(string(tools)) +
		" ATL_EVAL_WORKSPACE_ROOT=" + shellSingleQuote(confinement.WorkspaceReadRoot) +
		" ATL_EVAL_ALLOWED_READ_ROOTS=" + shellSingleQuote(string(roots)) +
		" " + shellSingleQuote("/private/guard")
	hookConfig := `hooks.PreToolUse=[{matcher="^(Bash|apply_patch|Edit|Write|Read|Agent)$",hooks=[{type="command",command=` + strconv.Quote(hookCommand) + `,timeout=5}]}]`
	args := []string{
		"exec", "--json", "--ephemeral", "--strict-config", "--skip-git-repo-check", "--model", model,
	}
	for _, feature := range []string{"apps", "browser_use", "computer_use", "image_generation", "remote_plugin"} {
		args = append(args, "--disable", feature)
	}
	args = append(args,
		"--enable", "shell_tool", "--enable", "unified_exec",
		"-C", "/private/workspace", "--output-schema", "/private/response-schema.json", "--output-last-message", "/private/final.json",
		"-c", `project_doc_max_bytes=0`,
		"-c", `shell_environment_policy.inherit="all"`,
		"-c", `shell_environment_policy.include_only=["PATH","SHELL","LANG","LC_ALL","TERM","ATL_READ_ONLY","ATL_EVAL_COUNTER","ATL_EVAL_GUARD_COUNTER","ATL_EVAL_CLI_POLICY_FILE","ATL_EVAL_COMMAND_BROKER_FILE","ATL_EVAL_GUARD_MODE","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`,
		"--ignore-rules", "--dangerously-bypass-hook-trust",
		"-c", `approval_policy="never"`,
		"-c", `web_search="disabled"`,
		"-c", `plugins."atl@atl".enabled=true`,
		"-c", `developer_instructions=`+strconv.Quote(legacyToolQualifiedCalibrationDeveloperInstructions),
		"-c", hookConfig,
		"-c", `default_permissions="atl_agent_eval"`,
		"-c", `permissions.atl_agent_eval.extends=":workspace"`,
		"-c", `permissions.atl_agent_eval.filesystem={"/private/requests"="write","/private/responses"="read"}`,
	)
	if reasoning != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(reasoning))
	}
	args = append(args, "-")
	return ProviderCommand{Path: "codex", Args: args}, nil
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
		ManifestPath: manifestPath, RealBinary: runOptions.ATLBinary, WorkingDirectory: workspace,
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
		SkillReadRoots:    []string{canonicalSkillRoot},
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
	skillRoots, _ := json.Marshal(confinement.SkillReadRoots)
	environment["ATL_EVAL_SKILL_READ_ROOTS"] = string(skillRoots)
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
		return receipt, &CodexCLICalibrationFailure{Status: CodexCLICalibrationProcessFailed}
	}
	if runErr != nil {
		return receipt, &CodexCLICalibrationFailure{Status: CodexCLICalibrationProcessFailed}
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
	providerMetrics, final, err := parseCodexCalibrationProviderOutput(transcriptData, finalData)
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
		Checks:             []RunCheck{{Name: "calibration_response", Kind: "json_present", Pointer: "/version"}},
	}
}

func calibrationResponse(data []byte) (codexCLICalibrationResponse, error) {
	var response codexCLICalibrationResponse
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return response, fmt.Errorf("invalid calibration version response")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil || response.Version == "" || len(response.Version) > 256 || response.Commit == "" || len(response.Commit) > 256 ||
		(response.BuildState != "clean" && response.BuildState != "dirty" && response.BuildState != "unknown") || decoder.Decode(new(any)) != io.EOF {
		return codexCLICalibrationResponse{}, fmt.Errorf("invalid calibration version response")
	}
	return response, nil
}

func parseCodexCalibrationProviderOutput(transcript, finalFile []byte) (ProviderMetrics, []byte, error) {
	metrics, err := parseCodexOutput(transcript)
	if err != nil {
		return ProviderMetrics{}, nil, &CodexCLICalibrationFailure{Status: CodexCLICalibrationProcessFailed}
	}
	final := bytes.TrimSpace(finalFile)
	if len(final) == 0 {
		return ProviderMetrics{}, nil, &CodexCLICalibrationFailure{Status: CodexCLICalibrationResponseSchemaFailed}
	}
	return metrics, final, nil
}

// CalibrationVersionObservationSHA256 returns a content-free semantic digest
// of the stable `atl version` JSON object. It is used only by the reserved
// backend-free calibration proxy and never retains the observed field values.
func CalibrationVersionObservationSHA256(data []byte) (string, error) {
	response, err := calibrationResponse(data)
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("encode calibration version response")
	}
	return sha256HexBytes(canonical), nil
}

func validateCalibrationEvidence(metrics ProviderMetrics, records []atlProxyRecord, guardSummary guardDecisionSummary, final []byte) error {
	status := classifyCalibrationEvidence(metrics, records, guardSummary, final)
	if status != CodexCLICalibrationSucceeded {
		return &CodexCLICalibrationFailure{Status: status}
	}
	return nil
}

func classifyCalibrationEvidence(metrics ProviderMetrics, records []atlProxyRecord, guardSummary guardDecisionSummary, final []byte) CodexCLICalibrationStatus {
	if guardSummary.Denials > 0 {
		return CodexCLICalibrationPolicyDenied
	}
	if metrics.CommandExecutions == 0 && len(records) == 0 && guardSummary.Admissions == 0 && guardSummary.ATLAdmissions == 0 {
		return CodexCLICalibrationModelNonInvocation
	}
	observation, err := CalibrationVersionObservationSHA256(final)
	if err != nil || len(records) != 1 || !validSHA256(records[0].CalibrationObservationSHA256) ||
		!constantTimeStringEqual(observation, records[0].CalibrationObservationSHA256) {
		if err != nil {
			return CodexCLICalibrationResponseSchemaFailed
		}
		return CodexCLICalibrationInvocationFailed
	}
	if records[0].CommandFamily != "atl_version" || records[0].Denied || records[0].ExitCode != 0 || records[0].StdoutBytes < 1 || records[0].StdoutBytes > calibrationOutputLimit || records[0].StderrBytes != 0 || guardSummary.Admissions != 1 || guardSummary.ATLAdmissions != 1 || guardSummary.Denials != 0 || metrics.CommandExecutions != 1 {
		return CodexCLICalibrationInvocationFailed
	}
	return CodexCLICalibrationSucceeded
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
