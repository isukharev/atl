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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	CodexCLIToolAvailabilitySchemaVersion = 2
	maxCodexToolProbeRequestBytes         = 4 << 20
	maxCodexToolProbeOutputBytes          = 4 << 20
	maxCodexToolProbeTimeout              = 60
	defaultCodexToolProbeTimeout          = 30
)

type CodexCLIToolAvailabilityStatus string

const (
	CodexCLIToolAvailabilitySupported     CodexCLIToolAvailabilityStatus = "supported"
	CodexCLIToolAvailabilityMissing       CodexCLIToolAvailabilityStatus = "tool_inventory_missing"
	CodexCLIToolAvailabilityAmbiguous     CodexCLIToolAvailabilityStatus = "tool_inventory_ambiguous"
	CodexCLIToolAvailabilitySchemaFailed  CodexCLIToolAvailabilityStatus = "request_schema_failed"
	CodexCLIToolAvailabilityProcessFailed CodexCLIToolAvailabilityStatus = "process_failed"
)

type CodexCLIToolAvailabilityOptions struct {
	AgentBinary    string
	ScratchRoot    string
	Model          string
	Reasoning      string
	TimeoutSeconds int
}

// CodexCLIToolAvailabilityReport is a content-free qualification of the exact
// snapshotted Codex binary and model configuration. It never retains the probe
// prompt, request body, tool schemas, command output, paths, or credentials.
type CodexCLIToolAvailabilityReport struct {
	SchemaVersion     int                            `json:"schema_version"`
	Provider          string                         `json:"provider"`
	AgentIdentity     string                         `json:"agent_identity"`
	ContractSHA256    string                         `json:"contract_sha256"`
	Status            CodexCLIToolAvailabilityStatus `json:"status"`
	ShellTool         string                         `json:"shell_tool,omitempty"`
	RequestObserved   bool                           `json:"request_observed"`
	SyntheticRequests int                            `json:"synthetic_requests"`
	ProviderRequests  int                            `json:"provider_requests"`
	BackendRequests   int                            `json:"backend_requests"`
	RemoteWrites      int                            `json:"remote_writes"`
}

func (r CodexCLIToolAvailabilityReport) Validate() error {
	if (r.SchemaVersion != 1 && r.SchemaVersion != CodexCLIToolAvailabilitySchemaVersion) || r.Provider != "codex" ||
		len(r.AgentIdentity) != len("binary-sha256:")+64 || r.AgentIdentity[:len("binary-sha256:")] != "binary-sha256:" ||
		!validSHA256(r.AgentIdentity[len("binary-sha256:"):]) || !validSHA256(r.ContractSHA256) ||
		r.ProviderRequests != 0 || r.BackendRequests != 0 || r.RemoteWrites != 0 {
		return fmt.Errorf("invalid codex cli tool availability report")
	}
	switch r.Status {
	case CodexCLIToolAvailabilitySupported:
		legacyDirect := r.SchemaVersion == 1 && (r.ShellTool == "exec_command" || r.ShellTool == "shell_command")
		currentRoute := r.SchemaVersion == CodexCLIToolAvailabilitySchemaVersion &&
			(r.ShellTool == "exec_command" || r.ShellTool == "shell_command" || r.ShellTool == "exec")
		if !r.RequestObserved || r.SyntheticRequests != 1 || (!legacyDirect && !currentRoute) {
			return fmt.Errorf("invalid codex cli tool availability report")
		}
	case CodexCLIToolAvailabilityMissing, CodexCLIToolAvailabilityAmbiguous, CodexCLIToolAvailabilitySchemaFailed:
		if !r.RequestObserved || r.SyntheticRequests != 1 || r.ShellTool != "" {
			return fmt.Errorf("invalid codex cli tool availability report")
		}
	case CodexCLIToolAvailabilityProcessFailed:
		if r.ShellTool != "" || (!r.RequestObserved && r.SyntheticRequests != 0) || (r.RequestObserved && r.SyntheticRequests != 2) {
			return fmt.Errorf("invalid codex cli tool availability report")
		}
	default:
		return fmt.Errorf("invalid codex cli tool availability report")
	}
	return nil
}

func (r CodexCLIToolAvailabilityReport) Supported() bool {
	return r.Validate() == nil && r.Status == CodexCLIToolAvailabilitySupported
}

type codexToolProbeRuntime struct {
	root      string
	scratch   string
	workspace string
	env       []string
}

type codexToolProbeObservation struct {
	status    CodexCLIToolAvailabilityStatus
	shellTool string
}

type codexToolProbeRequest struct {
	Model  string          `json:"model"`
	Stream bool            `json:"stream"`
	Tools  json.RawMessage `json:"tools"`
	Input  json.RawMessage `json:"input"`
}

// QualifyCodexCLIToolAvailability runs no model and sends no provider or
// backend request. A loopback-only synthetic Responses endpoint captures one
// bounded request from the exact native Codex binary and immediately returns a
// fixed assistant response.
func QualifyCodexCLIToolAvailability(parent context.Context, options CodexCLIToolAvailabilityOptions) (report CodexCLIToolAvailabilityReport, returnErr error) {
	if parent == nil || options.AgentBinary == "" || options.ScratchRoot == "" || options.Model == "" ||
		options.TimeoutSeconds < 0 || options.TimeoutSeconds > maxCodexToolProbeTimeout {
		return report, fmt.Errorf("codex cli tool availability requires agent, private scratch, model, and a bounded timeout")
	}
	if options.TimeoutSeconds == 0 {
		options.TimeoutSeconds = defaultCodexToolProbeTimeout
	}
	agent, _, err := inspectPrivateAgentBinary(options.AgentBinary, "")
	if err != nil {
		return report, err
	}
	base := CodexCLIToolAvailabilityReport{
		SchemaVersion:  CodexCLIToolAvailabilitySchemaVersion,
		Provider:       "codex",
		AgentIdentity:  agent.identity,
		ContractSHA256: codexToolAvailabilityContractSHA256(agent.identity, options),
		Status:         CodexCLIToolAvailabilityProcessFailed,
	}
	runtime, err := newCodexToolProbeRuntime(options.ScratchRoot)
	if err != nil {
		return report, err
	}
	defer func() { returnErr = errors.Join(returnErr, runtime.Close()) }()
	probeAgentName := privateAgentSnapshotName(agent.canonicalPath)
	probeAgentPath := filepath.Join(runtime.root, probeAgentName)
	if agent.resourceRelativePath != "" {
		probeBinRoot := filepath.Join(runtime.root, "bin")
		if err := safepath.MkdirAllWithin(runtime.scratch, probeBinRoot, 0o700); err != nil {
			return report, fmt.Errorf("prepare codex cli tool availability runtime")
		}
		probeAgentPath = filepath.Join(probeBinRoot, probeAgentName)
	}
	if err := copyReviewedPrivateAgent(runtime.scratch, runtime.root, agent, probeAgentPath); err != nil {
		return report, fmt.Errorf("prepare codex cli tool availability runtime")
	}
	probeAgent, _, err := inspectPrivateAgentBinary(probeAgentPath, agent.provenanceSHA256)
	if err != nil || probeAgent.identity != agent.identity || verifyPrivateAgentResourceSnapshot(probeAgentPath, agent) != nil {
		return report, fmt.Errorf("prepare codex cli tool availability runtime")
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return base, nil
	}
	defer func() { _ = listener.Close() }()
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return report, fmt.Errorf("prepare codex cli tool availability probe")
	}
	nonce := hex.EncodeToString(nonceBytes)
	requestPath := "/" + nonce + "/v1/responses"
	baseURL := "http://" + listener.Addr().String() + "/" + nonce + "/v1"

	var mu sync.Mutex
	observations := make([]codexToolProbeObservation, 0, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != requestPath || request.URL.RawQuery != "" {
			http.NotFound(w, request)
			return
		}
		observation := observeCodexToolProbeRequest(w, request, options.Model)
		mu.Lock()
		if len(observations) < 2 {
			observations = append(observations, observation)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, codexToolProbeSSE)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	args := codexToolProbeArgs(options, runtime.workspace, baseURL)
	ctx, cancel := context.WithTimeout(parent, time.Duration(options.TimeoutSeconds)*time.Second)
	command := exec.CommandContext(ctx, probeAgent.canonicalPath, args...)
	command.Dir = runtime.workspace
	command.Env = runtime.env
	command.Stdin = bytes.NewReader(codexToolProbePrompt)
	stdout := &cappedCommandOutput{limit: maxCodexToolProbeOutputBytes}
	stderr := &cappedCommandOutput{limit: maxCodexToolProbeOutputBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	cancel()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := server.Shutdown(shutdownContext)
	shutdownCancel()
	serveErr := <-serveDone
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return base, nil
	}
	if shutdownErr != nil || ctx.Err() == context.DeadlineExceeded || runErr != nil || stdout.overflow || stderr.overflow {
		return base, nil
	}
	mu.Lock()
	captured := append([]codexToolProbeObservation(nil), observations...)
	mu.Unlock()
	if len(captured) != 1 {
		if len(captured) == 0 {
			return base, nil
		}
		base.Status = CodexCLIToolAvailabilityProcessFailed
		base.RequestObserved = true
		base.SyntheticRequests = 2
		return base, nil
	}
	base.Status = captured[0].status
	base.ShellTool = captured[0].shellTool
	base.RequestObserved = true
	base.SyntheticRequests = 1
	if err := base.Validate(); err != nil {
		return CodexCLIToolAvailabilityReport{}, err
	}
	return base, nil
}

func observeCodexToolProbeRequest(w http.ResponseWriter, request *http.Request, expectedModel string) codexToolProbeObservation {
	if request.Header.Get("Authorization") != "" || request.Header.Get("Proxy-Authorization") != "" {
		return codexToolProbeObservation{status: CodexCLIToolAvailabilitySchemaFailed}
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, request.Body, maxCodexToolProbeRequestBytes))
	if err != nil || !json.Valid(data) || validateJSONNoDuplicateKeys(data) != nil {
		return codexToolProbeObservation{status: CodexCLIToolAvailabilitySchemaFailed}
	}
	var envelope codexToolProbeRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Model != expectedModel || !envelope.Stream {
		return codexToolProbeObservation{status: CodexCLIToolAvailabilitySchemaFailed}
	}
	return classifyCodexToolProbeInventories(envelope.Tools, envelope.Input)
}

func classifyCodexToolProbeInventories(topLevel, input json.RawMessage) codexToolProbeObservation {
	embedded, embeddedFound, invalid := codexAdditionalToolsInventory(input)
	if invalid {
		return codexToolProbeObservation{status: CodexCLIToolAvailabilitySchemaFailed}
	}
	topFound := len(topLevel) != 0 && !bytes.Equal(bytes.TrimSpace(topLevel), []byte("null"))
	if topFound && embeddedFound {
		return codexToolProbeObservation{status: CodexCLIToolAvailabilityAmbiguous}
	}
	if embeddedFound {
		return classifyCodexToolInventory(embedded, true)
	}
	return classifyCodexToolInventory(topLevel, false)
}

func codexAdditionalToolsInventory(input json.RawMessage) (json.RawMessage, bool, bool) {
	if len(input) == 0 || bytes.Equal(bytes.TrimSpace(input), []byte("null")) {
		return nil, false, false
	}
	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return nil, false, true
	}
	var found json.RawMessage
	for index, rawItem := range items {
		var item struct {
			Type  string          `json:"type"`
			Tools json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(rawItem, &item) != nil {
			return nil, false, true
		}
		if item.Type != "additional_tools" {
			continue
		}
		if index != 0 || found != nil || len(item.Tools) == 0 {
			return nil, false, true
		}
		found = item.Tools
	}
	return found, found != nil, false
}

func classifyCodexToolInventory(raw json.RawMessage, allowCodeMode bool) codexToolProbeObservation {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return codexToolProbeObservation{status: CodexCLIToolAvailabilityMissing}
	}
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return codexToolProbeObservation{status: CodexCLIToolAvailabilitySchemaFailed}
	}
	seen := make(map[string]struct{}, len(tools))
	primary := ""
	codeModeExec := false
	codeModeWait := false
	for _, rawTool := range tools {
		var tool struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Format      json.RawMessage `json:"format"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		if json.Unmarshal(rawTool, &tool) != nil || tool.Type == "" {
			return codexToolProbeObservation{status: CodexCLIToolAvailabilitySchemaFailed}
		}
		if tool.Name == "" {
			continue
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return codexToolProbeObservation{status: CodexCLIToolAvailabilityAmbiguous}
		}
		seen[tool.Name] = struct{}{}
		if allowCodeMode && tool.Name == "exec" {
			if primary != "" || codeModeExec || tool.Type != "custom" ||
				!validCodexCodeModeExec(tool.Description, tool.Format, tool.Parameters) {
				return codexToolProbeObservation{status: CodexCLIToolAvailabilityAmbiguous}
			}
			codeModeExec = true
			continue
		}
		if allowCodeMode && tool.Name == "wait" {
			if codeModeWait || tool.Type != "function" || !validCodexCodeModeWait(tool.Parameters) {
				return codexToolProbeObservation{status: CodexCLIToolAvailabilityAmbiguous}
			}
			codeModeWait = true
			continue
		}
		if tool.Name != "exec_command" && tool.Name != "shell_command" {
			continue
		}
		requiredParameter := "cmd"
		if tool.Name == "shell_command" {
			requiredParameter = "command"
		}
		if primary != "" || tool.Type != "function" || !validCodexShellToolParameters(tool.Parameters, requiredParameter) {
			return codexToolProbeObservation{status: CodexCLIToolAvailabilityAmbiguous}
		}
		primary = tool.Name
	}
	if codeModeExec || codeModeWait {
		if primary != "" || !codeModeExec || !codeModeWait {
			return codexToolProbeObservation{status: CodexCLIToolAvailabilityAmbiguous}
		}
		return codexToolProbeObservation{status: CodexCLIToolAvailabilitySupported, shellTool: "exec"}
	}
	if primary == "" {
		return codexToolProbeObservation{status: CodexCLIToolAvailabilityMissing}
	}
	return codexToolProbeObservation{status: CodexCLIToolAvailabilitySupported, shellTool: primary}
}

func validCodexCodeModeExec(description string, format, parameters json.RawMessage) bool {
	if len(parameters) != 0 && !bytes.Equal(bytes.TrimSpace(parameters), []byte("null")) {
		return false
	}
	var grammarObject map[string]json.RawMessage
	if json.Unmarshal(format, &grammarObject) != nil || len(grammarObject) != 3 {
		return false
	}
	for _, name := range []string{"type", "syntax", "definition"} {
		if _, ok := grammarObject[name]; !ok {
			return false
		}
	}
	var grammar struct {
		Type       string `json:"type"`
		Syntax     string `json:"syntax"`
		Definition string `json:"definition"`
	}
	if json.Unmarshal(format, &grammar) != nil || grammar.Type != "grammar" || grammar.Syntax != "lark" ||
		strings.TrimSpace(grammar.Definition) != strings.TrimSpace(codexCodeModeExecGrammar) {
		return false
	}
	if !strings.Contains(description, "All nested tools are available on the global `tools` object") {
		return false
	}
	// Require the generated heading/declaration pairs, not loose name mentions
	// that could occur in a warning or a negated sentence.
	for _, declaration := range []string{
		"### `exec_command`\n", "declare const tools: { exec_command(args: {",
		"### `write_stdin`\n", "declare const tools: { write_stdin(args: {",
	} {
		if strings.Count(description, declaration) != 1 {
			return false
		}
	}
	return true
}

func validCodexCodeModeWait(data json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || len(object) != 4 {
		return false
	}
	for _, name := range []string{"type", "properties", "required", "additionalProperties"} {
		if _, ok := object[name]; !ok {
			return false
		}
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		Additional *bool                      `json:"additionalProperties"`
	}
	if json.Unmarshal(data, &schema) != nil || schema.Type != "object" ||
		len(schema.Properties) != 4 || len(schema.Required) != 1 || schema.Required[0] != "cell_id" ||
		schema.Additional == nil || *schema.Additional {
		return false
	}
	for name, wantType := range map[string]string{
		"cell_id": "string", "max_tokens": "number", "terminate": "boolean", "yield_time_ms": "number",
	} {
		var property struct {
			Type string `json:"type"`
		}
		if raw, ok := schema.Properties[name]; !ok || json.Unmarshal(raw, &property) != nil || property.Type != wantType {
			return false
		}
	}
	return true
}

const codexCodeModeExecGrammar = `
start: pragma_source | plain_source
pragma_source: PRAGMA_LINE NEWLINE SOURCE
plain_source: SOURCE

PRAGMA_LINE: /[ \t]*\/\/ @exec:[^\r\n]*/
NEWLINE: /\r?\n/
SOURCE: /[\s\S]+/
`

func validCodexShellToolParameters(data []byte, requiredParameter string) bool {
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if json.Unmarshal(data, &schema) != nil || schema.Type != "object" || schema.Properties == nil {
		return false
	}
	_, propertyExists := schema.Properties[requiredParameter]
	return propertyExists && containsString(schema.Required, requiredParameter)
}

func codexToolProbeArgs(options CodexCLIToolAvailabilityOptions, workspace, baseURL string) []string {
	args := []string{
		"exec", "--json", "--ephemeral", "--strict-config", "--skip-git-repo-check", "--ignore-user-config",
		"--model", options.Model,
	}
	for _, feature := range []string{"apps", "browser_use", "computer_use", "image_generation", "remote_plugin"} {
		args = append(args, "--disable", feature)
	}
	args = append(args,
		"--enable", "shell_tool", "--enable", "unified_exec", "--sandbox", "read-only", "-C", workspace,
		"-c", `model_provider="atl_tool_probe"`,
		"-c", `model_providers.atl_tool_probe.name="ATL tool inventory probe"`,
		"-c", `model_providers.atl_tool_probe.base_url=`+strconv.Quote(baseURL),
		"-c", `model_providers.atl_tool_probe.wire_api="responses"`,
		"-c", `model_providers.atl_tool_probe.requires_openai_auth=false`,
		"-c", `model_providers.atl_tool_probe.supports_websockets=false`,
		"-c", `model_providers.atl_tool_probe.request_max_retries=0`,
		"-c", `model_providers.atl_tool_probe.stream_max_retries=0`,
		"-c", `approval_policy="never"`, "-c", `web_search="disabled"`, "-c", `project_doc_max_bytes=0`,
	)
	if options.Reasoning != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(options.Reasoning))
	}
	return append(args, "-")
}

func codexToolAvailabilityContractSHA256(agentIdentity string, options CodexCLIToolAvailabilityOptions) string {
	if options.TimeoutSeconds == 0 {
		options.TimeoutSeconds = defaultCodexToolProbeTimeout
	}
	envelope := struct {
		SchemaVersion int      `json:"schema_version"`
		AgentIdentity string   `json:"agent_identity"`
		Prompt        []byte   `json:"prompt"`
		ProviderArgs  []string `json:"provider_args"`
		RequestLimit  int      `json:"request_limit"`
		OutputLimit   int      `json:"output_limit"`
		Timeout       int      `json:"timeout_seconds"`
	}{
		SchemaVersion: CodexCLIToolAvailabilitySchemaVersion,
		AgentIdentity: agentIdentity,
		Prompt:        codexToolProbePrompt,
		ProviderArgs:  codexToolProbeArgs(options, "/private/workspace", "http://127.0.0.1/probe/v1"),
		RequestLimit:  maxCodexToolProbeRequestBytes,
		OutputLimit:   maxCodexToolProbeOutputBytes,
		Timeout:       options.TimeoutSeconds,
	}
	data, _ := json.Marshal(envelope)
	return sha256HexBytes(data)
}

func newCodexToolProbeRuntime(scratchRoot string) (*codexToolProbeRuntime, error) {
	if err := requirePrivateDirectory("codex tool probe scratch root", scratchRoot); err != nil {
		return nil, fmt.Errorf("prepare codex cli tool availability runtime")
	}
	root, err := os.MkdirTemp(scratchRoot, "codex-tool-availability-")
	if err != nil {
		return nil, fmt.Errorf("prepare codex cli tool availability runtime")
	}
	runtime := &codexToolProbeRuntime{root: root, scratch: scratchRoot, workspace: filepath.Join(root, "workspace")}
	failed := true
	defer func() {
		if failed {
			_ = runtime.Close()
		}
	}()
	if os.Chmod(root, 0o700) != nil {
		return nil, fmt.Errorf("prepare codex cli tool availability runtime")
	}
	directories := map[string]string{
		"HOME":            filepath.Join(root, "home"),
		"CODEX_HOME":      filepath.Join(root, "codex-home"),
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME":   filepath.Join(root, "xdg-data"),
		"XDG_CACHE_HOME":  filepath.Join(root, "xdg-cache"),
		"TMPDIR":          filepath.Join(root, "tmp"),
		"TMP":             filepath.Join(root, "tmp"),
		"TEMP":            filepath.Join(root, "tmp"),
	}
	for _, directory := range directories {
		if err := safepath.MkdirAllWithin(root, directory, 0o700); err != nil {
			return nil, fmt.Errorf("prepare codex cli tool availability runtime")
		}
	}
	if err := safepath.MkdirAllWithin(root, runtime.workspace, 0o700); err != nil {
		return nil, fmt.Errorf("prepare codex cli tool availability runtime")
	}
	environment := make([]string, 0, len(directories)+8)
	for name, value := range directories {
		environment = append(environment, name+"="+value)
	}
	environment = append(environment,
		"PATH="+os.Getenv("PATH"), "SHELL="+codexIsolatedShell, "USER=atl-agent-eval", "LOGNAME=atl-agent-eval",
		"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
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

func (r *codexToolProbeRuntime) Close() error {
	if r == nil || r.root == "" {
		return nil
	}
	err := removePrivateTree(r.scratch, r.root)
	r.root = ""
	r.workspace = ""
	r.env = nil
	return err
}

var codexToolProbePrompt = []byte("Return the fixed word done without calling any tool.\n")

const codexToolProbeSSE = "event: response.created\n" +
	"data: {\"type\":\"response.created\",\"response\":{\"id\":\"atl-tool-probe\"}}\n\n" +
	"event: response.output_item.done\n" +
	"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"id\":\"atl-tool-probe-message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}}\n\n" +
	"event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"atl-tool-probe\",\"usage\":{\"input_tokens\":0,\"input_tokens_details\":null,\"output_tokens\":0,\"output_tokens_details\":null,\"total_tokens\":0}}}\n\n"
