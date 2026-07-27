package agenteval

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	// CLIRouteQualificationSchemaVersion versions the content-free report that
	// binds one reviewed model-facing CLI route to a private comparison plan.
	CLIRouteQualificationSchemaVersion = 1
	maxCLIRouteProbeRequestBytes       = 4 << 20
	maxCLIRouteProbeOutputBytes        = 4 << 20
	maxCLIRouteProbeTimeout            = 60
	defaultCLIRouteProbeTimeout        = 30
	cliRouteProbeTerminationStatus     = http.StatusBadRequest
	// maxCLIRouteAuxiliaryRequests bounds the Claude Code connectivity probe
	// that precedes its first model request. It is deliberately tiny: it never
	// admits a second one, never admits one after capture, and never admits a
	// method or path outside the fixed auxiliary set.
	maxCLIRouteAuxiliaryRequests = 1
	// cliRouteProbeSyntheticAPIKey is a fixed non-credential. It exists so the
	// Claude client has a syntactically complete key without any ambient
	// provider credential crossing into the probe environment.
	// #nosec G101 -- fixed non-secret placeholder; it authenticates nothing and replaces the ambient credential.
	cliRouteProbeSyntheticAPIKey = "atl-agent-eval-synthetic-cli-route-probe-key"
	claudeShellRouteToolName     = "Bash"
	claudeShellRoute             = "bash"
)

// CLIRouteQualificationStatus is a closed vocabulary. Anything that is not an
// exactly recognized reviewed route fails closed under one of the other terms.
type CLIRouteQualificationStatus string

const (
	CLIRouteQualificationSupported     CLIRouteQualificationStatus = "supported"
	CLIRouteQualificationRouteMissing  CLIRouteQualificationStatus = "route_inventory_missing"
	CLIRouteQualificationAmbiguous     CLIRouteQualificationStatus = "route_inventory_ambiguous"
	CLIRouteQualificationSchemaFailed  CLIRouteQualificationStatus = "request_schema_failed"
	CLIRouteQualificationProcessFailed CLIRouteQualificationStatus = "process_failed"
)

// CLIRouteQualificationOptions carries only what is needed to reproduce the
// reviewed model-facing launch. It deliberately has no live config directory,
// backend URL, credential, gateway, or fixture field: qualification must not be
// able to reach a backend even by accident.
type CLIRouteQualificationOptions struct {
	Provider    string
	Surface     string
	AgentBinary string
	ScratchRoot string
	Model       string
	Reasoning   string
	// AllowedTools is the reviewed permission-rule inventory of the CLI item.
	AllowedTools []string
	// The remaining identities reproduce or bind the reviewed launch without
	// retaining any of its content.
	PluginSHA256         string
	PluginManifestSHA256 string
	SettingsSHA256       string
	ResponseSchemaSHA256 string
	PromptContractSHA256 string
	TimeoutSeconds       int
}

// CLIRouteQualificationReport is a content-free qualification of one exact
// agent binary and reviewed launch. Every field is a scalar so two reports are
// comparable by value. It never retains the probe prompt, request bytes, tool
// schemas, command output, paths, credentials, or backend identity.
type CLIRouteQualificationReport struct {
	SchemaVersion     int                         `json:"schema_version"`
	Provider          string                      `json:"provider"`
	Surface           string                      `json:"surface"`
	AgentIdentity     string                      `json:"agent_identity"`
	ContractSHA256    string                      `json:"contract_sha256"`
	Status            CLIRouteQualificationStatus `json:"status"`
	Route             string                      `json:"route,omitempty"`
	RequestObserved   bool                        `json:"request_observed"`
	SyntheticRequests int                         `json:"synthetic_requests"`
	AuxiliaryRequests int                         `json:"auxiliary_requests"`
	ProviderRequests  int                         `json:"provider_requests"`
	BackendRequests   int                         `json:"backend_requests"`
	RemoteWrites      int                         `json:"remote_writes"`
}

func (r CLIRouteQualificationReport) Validate() error {
	invalid := fmt.Errorf("invalid cli route qualification report")
	const identityPrefix = "binary-sha256:"
	if r.SchemaVersion != CLIRouteQualificationSchemaVersion || !validCLIRouteProvider(r.Provider) ||
		r.Surface != SurfaceCLISkill || len(r.AgentIdentity) != len(identityPrefix)+64 ||
		!strings.HasPrefix(r.AgentIdentity, identityPrefix) ||
		!validSHA256(strings.TrimPrefix(r.AgentIdentity, identityPrefix)) || !validSHA256(r.ContractSHA256) ||
		r.AuxiliaryRequests < 0 || r.AuxiliaryRequests > maxCLIRouteAuxiliaryRequests ||
		(r.Provider == "codex" && r.AuxiliaryRequests != 0) ||
		r.ProviderRequests != 0 || r.BackendRequests != 0 || r.RemoteWrites != 0 {
		return invalid
	}
	switch r.Status {
	case CLIRouteQualificationSupported:
		if !r.RequestObserved || r.SyntheticRequests != 1 || !validCLIRoute(r.Provider, r.Route) {
			return invalid
		}
	case CLIRouteQualificationRouteMissing, CLIRouteQualificationAmbiguous, CLIRouteQualificationSchemaFailed:
		if !r.RequestObserved || r.SyntheticRequests != 1 || r.Route != "" {
			return invalid
		}
	case CLIRouteQualificationProcessFailed:
		if r.Route != "" || (!r.RequestObserved && r.SyntheticRequests != 0) || (r.RequestObserved && r.SyntheticRequests != 2) {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}

func (r CLIRouteQualificationReport) Supported() bool {
	return r.Validate() == nil && r.Status == CLIRouteQualificationSupported
}

func validCLIRouteProvider(provider string) bool {
	return provider == "codex" || provider == "claude-code"
}

// validCLIRoute keeps the route vocabulary provider-scoped, so a Codex alias
// can never satisfy a Claude Code plan or the reverse.
func validCLIRoute(provider, route string) bool {
	if provider == "codex" {
		return route == "exec_command" || route == "shell_command" || route == "exec"
	}
	return route == claudeShellRoute
}

type cliRouteProbeObservation struct {
	status CLIRouteQualificationStatus
	route  string
}

type cliRouteProbeRuntime struct {
	root, scratch, workspace string
	env                      []string
}

func (r *cliRouteProbeRuntime) Close() error {
	if r == nil || r.root == "" {
		return nil
	}
	err := removePrivateTree(r.scratch, r.root)
	r.root = ""
	r.workspace = ""
	r.env = nil
	return err
}

// QualifyCLIRoute runs no model and sends no provider or backend request. A
// nonce-scoped loopback endpoint captures the first exact model-facing request
// from the reviewed agent binary, reduces it in memory to a closed report, and
// then deliberately terminates the child. It never fabricates a model response
// and never retries, so a run can neither continue nor bill after capture.
func QualifyCLIRoute(parent context.Context, options CLIRouteQualificationOptions) (report CLIRouteQualificationReport, returnErr error) {
	if parent == nil || !validCLIRouteProvider(options.Provider) || options.Surface != SurfaceCLISkill ||
		options.AgentBinary == "" || options.ScratchRoot == "" || options.Model == "" ||
		options.TimeoutSeconds < 0 || options.TimeoutSeconds > maxCLIRouteProbeTimeout {
		return report, fmt.Errorf("cli route qualification requires provider, surface, agent, private scratch, model, and a bounded timeout")
	}
	if options.TimeoutSeconds == 0 {
		options.TimeoutSeconds = defaultCLIRouteProbeTimeout
	}
	agent, _, err := inspectPrivateAgentBinary(options.AgentBinary, "")
	if err != nil {
		return report, err
	}
	base := CLIRouteQualificationReport{
		SchemaVersion:  CLIRouteQualificationSchemaVersion,
		Provider:       options.Provider,
		Surface:        options.Surface,
		AgentIdentity:  agent.identity,
		ContractSHA256: cliRouteQualificationContractSHA256(agent.identity, options),
		Status:         CLIRouteQualificationProcessFailed,
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return base, nil
	}
	defer func() { _ = listener.Close() }()
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return report, fmt.Errorf("prepare cli route qualification probe")
	}
	nonce := hex.EncodeToString(nonceBytes)
	route := cliRouteProbeEndpoint(options.Provider, listener.Addr().String(), nonce)

	runtime, err := newCLIRouteProbeRuntime(options.Provider, options.ScratchRoot, route.baseURL)
	if err != nil {
		return report, err
	}
	defer func() { returnErr = errors.Join(returnErr, runtime.Close()) }()
	probeAgent, err := preparePrivateProbeAgent(runtime.scratch, runtime.root, agent)
	if err != nil {
		return report, fmt.Errorf("prepare cli route qualification runtime")
	}

	ctx, cancel := context.WithTimeout(parent, time.Duration(options.TimeoutSeconds)*time.Second)
	defer cancel()

	var mu sync.Mutex
	observations := make([]cliRouteProbeObservation, 0, 1)
	auxiliary := 0
	unexpected := false
	modelRequestStarted := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if route.auxiliary(request) {
			mu.Lock()
			modelStarted := modelRequestStarted
			admitted, ignored := classifyCLIRouteAuxiliary(modelStarted, auxiliary)
			if admitted {
				auxiliary++
			} else if !ignored {
				unexpected = true
			}
			mu.Unlock()
			// Once an exact model request starts, a concurrently arriving
			// connectivity HEAD is incidental client bookkeeping, not a second
			// model route and not qualification drift. Refuse it without changing
			// the closed observation, even while the POST body is still being read.
			if modelStarted {
				http.Error(w, "cli route probe complete", http.StatusBadRequest)
				return
			}
			if !admitted {
				http.Error(w, "cli route probe rejected request", http.StatusBadRequest)
				cancel()
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != route.path || request.URL.RawQuery != route.query {
			mu.Lock()
			unexpected = true
			mu.Unlock()
			http.NotFound(w, request)
			cancel()
			return
		}
		mu.Lock()
		modelRequestStarted = true
		mu.Unlock()
		observation := observeCLIRouteProbeRequest(w, request, options.Provider, options.Model)
		mu.Lock()
		if len(observations) < 2 {
			observations = append(observations, observation)
		}
		mu.Unlock()
		// Never fabricate a model response. The reviewed route has already been
		// observed, so the only correct next step is to end the child.
		// A non-retryable client error closes the HTTP exchange while process
		// cancellation closes the child. Do not invite a provider SDK retry in
		// the small interval before CommandContext delivers termination.
		terminateCLIRouteProbe(w, cancel)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	command := exec.CommandContext(ctx, probeAgent.canonicalPath, cliRouteProbeArgs(options, runtime.workspace, route.baseURL)...)
	command.Dir = runtime.workspace
	command.Env = runtime.env
	command.Stdin = bytes.NewReader(cliRouteProbePrompt)
	stdout := &cappedCommandOutput{limit: maxCLIRouteProbeOutputBytes}
	stderr := &cappedCommandOutput{limit: maxCLIRouteProbeOutputBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	// The child is deliberately terminated after the first captured request, so
	// its exit status carries no qualification signal.
	_ = command.Run()
	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := server.Shutdown(shutdownContext)
	shutdownCancel()
	serveErr := <-serveDone
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return base, nil
	}
	mu.Lock()
	captured := append([]cliRouteProbeObservation(nil), observations...)
	observedAuxiliary := auxiliary
	rejected := unexpected
	mu.Unlock()
	return finalizeCLIRouteProbe(base, captured, observedAuxiliary,
		shutdownErr != nil || rejected || stdout.overflow || stderr.overflow)
}

func terminateCLIRouteProbe(w http.ResponseWriter, cancel context.CancelFunc) {
	w.WriteHeader(cliRouteProbeTerminationStatus)
	cancel()
}

// finalizeCLIRouteProbe reduces the bounded in-memory observations to the
// closed report. Keeping this decision separate from process scheduling makes
// the repeated-request refusal deterministic to test: in a real run the first
// observation normally kills the child before its retry can reach loopback,
// while any second request that did arrive still fails closed.
func finalizeCLIRouteProbe(base CLIRouteQualificationReport, captured []cliRouteProbeObservation, auxiliary int, processFailed bool) (CLIRouteQualificationReport, error) {
	base.AuxiliaryRequests = auxiliary
	if processFailed || len(captured) == 0 {
		base.Status = CLIRouteQualificationProcessFailed
		return base, nil
	}
	if len(captured) != 1 {
		base.Status = CLIRouteQualificationProcessFailed
		base.RequestObserved = true
		base.SyntheticRequests = 2
		return base, nil
	}
	base.Status = captured[0].status
	base.Route = captured[0].route
	base.RequestObserved = true
	base.SyntheticRequests = 1
	if err := base.Validate(); err != nil {
		return CLIRouteQualificationReport{}, err
	}
	return base, nil
}

func classifyCLIRouteAuxiliary(modelRequestStarted bool, auxiliaryRequests int) (admitted, ignored bool) {
	if modelRequestStarted {
		return false, true
	}
	return auxiliaryRequests < maxCLIRouteAuxiliaryRequests, false
}

type cliRouteProbeEndpointBinding struct {
	baseURL   string
	path      string
	query     string
	auxiliary func(*http.Request) bool
}

// cliRouteProbeEndpoint pins the exact model-facing route for each provider.
// Any other method, path, or query is unexpected and fails the qualification.
func cliRouteProbeEndpoint(provider, address, nonce string) cliRouteProbeEndpointBinding {
	if provider == "codex" {
		return cliRouteProbeEndpointBinding{
			baseURL:   "http://" + address + "/" + nonce + "/v1",
			path:      "/" + nonce + "/v1/responses",
			auxiliary: func(*http.Request) bool { return false },
		}
	}
	// Current Claude Code binaries append the beta-scoped Messages path to
	// ANTHROPIC_BASE_URL, and may first send one credential-free connectivity
	// HEAD to the base or origin root.
	auxiliaryURIs := map[string]struct{}{
		"/": {}, "/api/hello": {},
		"/" + nonce: {}, "/" + nonce + "/": {}, "/" + nonce + "/api/hello": {},
	}
	return cliRouteProbeEndpointBinding{
		baseURL: "http://" + address + "/" + nonce,
		path:    "/" + nonce + "/v1/messages",
		query:   "beta=true",
		auxiliary: func(request *http.Request) bool {
			if request.Method != http.MethodHead || request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
				return false
			}
			_, known := auxiliaryURIs[request.RequestURI]
			return known
		},
	}
}

func observeCLIRouteProbeRequest(w http.ResponseWriter, request *http.Request, provider, expectedModel string) cliRouteProbeObservation {
	if provider == "codex" {
		observed := observeCodexToolProbeRequest(w, request, expectedModel)
		return cliRouteProbeObservation{status: codexRouteStatus(observed.status), route: observed.shellTool}
	}
	return observeClaudeRouteProbeRequest(w, request, expectedModel)
}

func codexRouteStatus(status CodexCLIToolAvailabilityStatus) CLIRouteQualificationStatus {
	switch status {
	case CodexCLIToolAvailabilitySupported:
		return CLIRouteQualificationSupported
	case CodexCLIToolAvailabilityMissing:
		return CLIRouteQualificationRouteMissing
	case CodexCLIToolAvailabilityAmbiguous:
		return CLIRouteQualificationAmbiguous
	case CodexCLIToolAvailabilitySchemaFailed:
		return CLIRouteQualificationSchemaFailed
	default:
		return CLIRouteQualificationProcessFailed
	}
}

func observeClaudeRouteProbeRequest(w http.ResponseWriter, request *http.Request, expectedModel string) cliRouteProbeObservation {
	// The probe environment carries one fixed synthetic key and no ambient
	// provider credential. Anything else on the wire means an ambient
	// credential crossed the boundary, so the request is rejected before its
	// body is inspected.
	if request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" ||
		!constantTimeStringEqual(request.Header.Get("X-Api-Key"), cliRouteProbeSyntheticAPIKey) {
		return cliRouteProbeObservation{status: CLIRouteQualificationSchemaFailed}
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, request.Body, maxCLIRouteProbeRequestBytes))
	if err != nil || !json.Valid(data) || validateJSONNoDuplicateKeys(data) != nil {
		return cliRouteProbeObservation{status: CLIRouteQualificationSchemaFailed}
	}
	var envelope struct {
		Model    string            `json:"model"`
		Stream   bool              `json:"stream"`
		Tools    json.RawMessage   `json:"tools"`
		Messages []json.RawMessage `json:"messages"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF ||
		envelope.Model != expectedModel || !envelope.Stream || len(envelope.Messages) == 0 {
		return cliRouteProbeObservation{status: CLIRouteQualificationSchemaFailed}
	}
	return classifyClaudeRouteInventory(envelope.Tools)
}

// classifyClaudeRouteInventory recognizes exactly one reviewed local-execution
// route. A duplicate name, a second shell entry, or a shell entry without the
// exact required command parameter is ambiguous rather than supported.
func classifyClaudeRouteInventory(raw json.RawMessage) cliRouteProbeObservation {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return cliRouteProbeObservation{status: CLIRouteQualificationRouteMissing}
	}
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return cliRouteProbeObservation{status: CLIRouteQualificationSchemaFailed}
	}
	seen := make(map[string]struct{}, len(tools))
	route := ""
	for _, rawTool := range tools {
		var tool struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if json.Unmarshal(rawTool, &tool) != nil || tool.Name == "" {
			return cliRouteProbeObservation{status: CLIRouteQualificationSchemaFailed}
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return cliRouteProbeObservation{status: CLIRouteQualificationAmbiguous}
		}
		seen[tool.Name] = struct{}{}
		if tool.Name != claudeShellRouteToolName {
			continue
		}
		if route != "" || !validClaudeShellToolSchema(tool.InputSchema) {
			return cliRouteProbeObservation{status: CLIRouteQualificationAmbiguous}
		}
		route = claudeShellRoute
	}
	if route == "" {
		return cliRouteProbeObservation{status: CLIRouteQualificationRouteMissing}
	}
	return cliRouteProbeObservation{status: CLIRouteQualificationSupported, route: route}
}

func validClaudeShellToolSchema(data json.RawMessage) bool {
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if json.Unmarshal(data, &schema) != nil || schema.Type != "object" || schema.Properties == nil {
		return false
	}
	var command struct {
		Type string `json:"type"`
	}
	raw, present := schema.Properties["command"]
	if !present || json.Unmarshal(raw, &command) != nil || command.Type != "string" {
		return false
	}
	return containsString(schema.Required, "command")
}

func cliRouteProbeArgs(options CLIRouteQualificationOptions, workspace, baseURL string) []string {
	if options.Provider == "codex" {
		// Reuse the reviewed route-determining Codex flags. Other measured-run
		// artifacts are independently bound by digest rather than loaded into
		// this backend-free qualifier.
		return codexToolProbeArgs(CodexCLIToolAvailabilityOptions{
			Model: options.Model, Reasoning: options.Reasoning, TimeoutSeconds: options.TimeoutSeconds,
		}, workspace, baseURL)
	}
	return claudeRouteProbeArgs(options)
}

func claudeRouteProbeArgs(options CLIRouteQualificationOptions) []string {
	toolNames, allowedTools := claudeReviewedToolInventory(options.AllowedTools, true)
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--no-session-persistence",
		"--disable-slash-commands", "--model", options.Model,
		"--permission-mode", "dontAsk", "--strict-mcp-config", "--no-chrome",
		"--setting-sources", "",
		"--tools", strings.Join(toolNames, ","),
		"--allowed-tools", strings.Join(allowedTools, ","),
		"--prompt-suggestions", "false",
	}
	if options.Reasoning != "" {
		args = append(args, "--effort", options.Reasoning)
	}
	return args
}

// cliRouteQualificationContractSHA256 binds everything that could change which
// route the model is offered: provider, surface, exact agent identity, model,
// reasoning, reviewed tool flags, plugin/settings/response-schema/prompt
// identities, launch arguments, bounds, and timeout.
func cliRouteQualificationContractSHA256(agentIdentity string, options CLIRouteQualificationOptions) string {
	if options.TimeoutSeconds == 0 {
		options.TimeoutSeconds = defaultCLIRouteProbeTimeout
	}
	envelope := struct {
		SchemaVersion        int      `json:"schema_version"`
		Provider             string   `json:"provider"`
		Surface              string   `json:"surface"`
		AgentIdentity        string   `json:"agent_identity"`
		Model                string   `json:"model"`
		Reasoning            string   `json:"reasoning"`
		AllowedTools         []string `json:"allowed_tools"`
		PluginSHA256         string   `json:"plugin_sha256"`
		PluginManifestSHA256 string   `json:"plugin_manifest_sha256"`
		SettingsSHA256       string   `json:"settings_sha256"`
		ResponseSchemaSHA256 string   `json:"response_schema_sha256"`
		PromptContractSHA256 string   `json:"prompt_contract_sha256"`
		Prompt               []byte   `json:"prompt"`
		ProviderArgs         []string `json:"provider_args"`
		RequestLimit         int      `json:"request_limit"`
		OutputLimit          int      `json:"output_limit"`
		AuxiliaryLimit       int      `json:"auxiliary_limit"`
		Timeout              int      `json:"timeout_seconds"`
	}{
		SchemaVersion: CLIRouteQualificationSchemaVersion,
		Provider:      options.Provider, Surface: options.Surface, AgentIdentity: agentIdentity,
		Model: options.Model, Reasoning: options.Reasoning,
		AllowedTools:         append([]string(nil), options.AllowedTools...),
		PluginSHA256:         options.PluginSHA256,
		PluginManifestSHA256: options.PluginManifestSHA256,
		SettingsSHA256:       options.SettingsSHA256,
		ResponseSchemaSHA256: options.ResponseSchemaSHA256,
		PromptContractSHA256: options.PromptContractSHA256,
		Prompt:               cliRouteProbePrompt,
		ProviderArgs:         cliRouteProbeArgs(options, "/private/workspace", "http://127.0.0.1/probe"),
		RequestLimit:         maxCLIRouteProbeRequestBytes,
		OutputLimit:          maxCLIRouteProbeOutputBytes,
		AuxiliaryLimit:       maxCLIRouteAuxiliaryRequests,
		Timeout:              options.TimeoutSeconds,
	}
	data, _ := json.Marshal(envelope)
	return sha256HexBytes(data)
}

func newCLIRouteProbeRuntime(provider, scratchRoot, baseURL string) (*cliRouteProbeRuntime, error) {
	if provider == "codex" {
		// Codex reaches the loopback endpoint through launch configuration, so
		// its isolated runtime is exactly the reviewed backend-free one.
		inner, err := newCodexIsolatedProbeRuntime(scratchRoot, "cli-route-qualification-")
		if err != nil {
			return nil, err
		}
		return &cliRouteProbeRuntime{root: inner.root, scratch: inner.scratch, workspace: inner.workspace, env: inner.env}, nil
	}
	return newClaudeRouteProbeRuntime(scratchRoot, baseURL)
}

// newClaudeRouteProbeRuntime builds the Claude Code probe environment from a
// fixed allowlist. No ambient provider credential, configuration directory, or
// proxy setting is copied, so the probe cannot authenticate to a real provider
// even if the operator shell is fully authenticated.
func newClaudeRouteProbeRuntime(scratchRoot, baseURL string) (*cliRouteProbeRuntime, error) {
	if err := requirePrivateDirectory("cli route probe scratch root", scratchRoot); err != nil {
		return nil, fmt.Errorf("prepare cli route qualification runtime")
	}
	root, err := os.MkdirTemp(scratchRoot, "cli-route-qualification-")
	if err != nil {
		return nil, fmt.Errorf("prepare cli route qualification runtime")
	}
	runtime := &cliRouteProbeRuntime{root: root, scratch: scratchRoot, workspace: filepath.Join(root, "workspace")}
	failed := true
	defer func() {
		if failed {
			_ = runtime.Close()
		}
	}()
	if os.Chmod(root, 0o700) != nil {
		return nil, fmt.Errorf("prepare cli route qualification runtime")
	}
	directories := map[string]string{
		"HOME":              filepath.Join(root, "home"),
		"CLAUDE_CONFIG_DIR": filepath.Join(root, "claude-config"),
		"XDG_CONFIG_HOME":   filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME":     filepath.Join(root, "xdg-data"),
		"XDG_CACHE_HOME":    filepath.Join(root, "xdg-cache"),
		"TMPDIR":            filepath.Join(root, "tmp"),
		"TMP":               filepath.Join(root, "tmp"),
		"TEMP":              filepath.Join(root, "tmp"),
	}
	for _, directory := range directories {
		if err := safepath.MkdirAllWithin(root, directory, 0o700); err != nil {
			return nil, fmt.Errorf("prepare cli route qualification runtime")
		}
	}
	if err := safepath.MkdirAllWithin(root, runtime.workspace, 0o700); err != nil {
		return nil, fmt.Errorf("prepare cli route qualification runtime")
	}
	environment := make([]string, 0, len(directories)+12)
	for name, value := range directories {
		environment = append(environment, name+"="+value)
	}
	environment = append(environment,
		"PATH="+os.Getenv("PATH"), "SHELL="+codexIsolatedShell, "USER=atl-agent-eval", "LOGNAME=atl-agent-eval",
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
		"DISABLE_TELEMETRY=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"ANTHROPIC_BASE_URL="+baseURL, "ANTHROPIC_API_KEY="+cliRouteProbeSyntheticAPIKey,
	)
	for _, name := range []string{"LANG", "LC_ALL", "LC_CTYPE", "TERM", "TZ"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	runtime.env = environment
	failed = false
	return runtime, nil
}

// cliRouteProbePrompt is fixed, generic, and content-free. It never carries
// case, backend, or customer material.
var cliRouteProbePrompt = []byte("Return the fixed word done without calling any tool.\n")
