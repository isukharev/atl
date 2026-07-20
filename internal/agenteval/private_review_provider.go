package agenteval

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	privateReviewRequestLimit  = 4 << 20
	privateReviewResponseLimit = 32 << 20
)

var (
	privateReviewCodexChatGPTOrigin = "https://chatgpt.com"
	privateReviewCodexAPIOrigin     = "https://api.openai.com"
	privateReviewClaudeOrigin       = "https://api.anthropic.com"
	privateReviewHTTPClient         = &http.Client{Timeout: 20 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return fmt.Errorf("review provider redirect rejected")
	}}
)

type privateReviewProxyObservation struct {
	ModelRequests, AuxiliaryRequests int
	InputTools, ForwardedTools       int
	ToolOutputs                      int
	Unexpected                       bool
	AuthenticationSeen               bool
}

type privateReviewProxy struct {
	mu               sync.Mutex
	provider         string
	model, reasoning string
	origin           *url.URL
	observation      privateReviewProxyObservation
	abort            context.CancelFunc
}

type claudeReviewRuntime struct {
	root        string
	environment map[string]string
}

func runPrivateReviewProvider(ctx context.Context, root, packet, agentBinary string, reviewer Reviewer,
	execution PrivateReviewerExecution, _ []byte, finalData, rubricData []byte, rubric Rubric,
) (result privateReviewProviderResult, returnErr error) {
	agent, _, err := inspectPrivateAgentBinary(agentBinary, "")
	if err != nil {
		return result, privatePlanError("review_agent_binary")
	}
	result.AgentIdentity = agent.identity
	templateData, err := readPrivatePlanLifecycleFile(root, filepath.Join(packet, "review.json"), maxReviewBytes)
	if err != nil {
		return result, privatePlanError("review_read")
	}
	template, err := DecodeReview(bytes.NewReader(templateData))
	if err != nil || template.Reviewer != reviewer {
		return result, privatePlanError("review_template")
	}
	schema, err := buildPrivateReviewSchema(template, rubric)
	if err != nil {
		return result, privatePlanError("review_schema")
	}
	resultData := privateReviewResultDataForPrompt(packet)
	prompt := buildPrivateReviewPrompt(templateData, rubricData, resultData, finalData)

	scratch, err := os.MkdirTemp(filepath.Join(root, ".ephemeral"), "review-provider-")
	if err != nil {
		return result, privatePlanError("review_runtime")
	}
	if err := os.Chmod(scratch, 0o700); err != nil {
		_ = removePrivateTree(root, scratch)
		return result, privatePlanError("review_runtime")
	}
	defer func() { returnErr = errors.Join(returnErr, removePrivateTree(root, scratch)) }()
	workspace := filepath.Join(scratch, "workspace")
	if err := safepath.MkdirAllWithin(root, workspace, 0o700); err != nil {
		return result, privatePlanError("review_runtime")
	}
	schemaPath := filepath.Join(scratch, "schema.json")
	finalPath := filepath.Join(scratch, "final.json")
	if err := safepath.WriteFileExclusiveWithin(root, schemaPath, schema, 0o600); err != nil {
		return result, privatePlanError("review_runtime")
	}

	var environment map[string]string
	var commandArgs []string
	var codexSession *codexAuthSession
	var codexRuntime *providerRuntimeCapsule
	var claudeRuntime *claudeReviewRuntime
	var origin string
	proxyPath := ""
	switch reviewer.Kind {
	case "codex":
		codexSession, err = newCodexAuthSession(os.Environ())
		if err != nil {
			return result, privatePlanError("review_auth")
		}
		defer func() { returnErr = errors.Join(returnErr, codexSession.Close()) }()
		codexRuntime, err = newCodexProviderRuntime(scratch, codexSession)
		if err != nil {
			return result, privatePlanError("review_runtime")
		}
		defer func() { returnErr = errors.Join(returnErr, codexRuntime.Close()) }()
		authMode, modeErr := codexReviewAuthMode(codexSession)
		if modeErr != nil {
			return result, privatePlanError("review_auth")
		}
		origin = privateReviewCodexAPIOrigin
		proxyPath = "/v1"
		if authMode == "chatgpt" {
			origin = privateReviewCodexChatGPTOrigin
			proxyPath = "/backend-api/codex"
		}
		environment = codexRuntime.Environment()
		commandArgs = privateCodexReviewArgs(reviewer, execution, workspace, schemaPath, finalPath)
	case "claude-code":
		claudeRuntime, err = newClaudeReviewRuntime(root, scratch, os.Environ())
		if err != nil {
			return result, privatePlanError("review_auth")
		}
		defer func() { returnErr = errors.Join(returnErr, claudeRuntime.Close()) }()
		origin = privateReviewClaudeOrigin
		environment = claudeRuntime.Environment()
		commandArgs = privateClaudeReviewArgs(reviewer, execution)
	default:
		return result, privatePlanError("review_provider")
	}

	deadline, cancel := context.WithTimeout(ctx, time.Duration(execution.TimeoutSeconds)*time.Second)
	defer cancel()
	proxy, listener, server, err := startPrivateReviewProxy(reviewer.Kind, reviewer.Model, execution.Reasoning, origin)
	if err != nil {
		return result, privatePlanError("review_proxy")
	}
	proxy.abort = cancel
	defer func() {
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		returnErr = errors.Join(returnErr, server.Shutdown(shutdownCtx))
	}()
	baseURL := "http://" + listener.Addr().String() + proxyPath
	environment["NO_PROXY"] = "127.0.0.1,localhost"
	environment["no_proxy"] = "127.0.0.1,localhost"
	if reviewer.Kind == "codex" {
		commandArgs = append(commandArgs[:len(commandArgs)-1],
			"-c", `model_provider="no_tools_review"`,
			"-c", `model_providers.no_tools_review.name="No-tools review boundary"`,
			"-c", `model_providers.no_tools_review.base_url=`+strconv.Quote(baseURL),
			"-c", `model_providers.no_tools_review.wire_api="responses"`,
			"-c", `model_providers.no_tools_review.requires_openai_auth=true`,
			"-c", `model_providers.no_tools_review.supports_websockets=false`,
			"-c", `model_providers.no_tools_review.request_max_retries=0`,
			"-c", `model_providers.no_tools_review.stream_max_retries=0`, "-")
	} else {
		environment["ANTHROPIC_BASE_URL"] = baseURL
	}

	command := exec.CommandContext(deadline, agent.canonicalPath, commandArgs...)
	command.Dir = workspace
	command.Env = flattenEnvironment(environment)
	command.Stdin = bytes.NewReader(prompt)
	stdout := &boundedCommandBuffer{maximum: privateReviewProviderOutputLimit}
	stderr := &boundedCommandBuffer{maximum: 1 << 20}
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	observation := proxy.Observation()
	result.ModelRequests = observation.ModelRequests
	result.Auxiliary = observation.AuxiliaryRequests
	result.InputTools = observation.InputTools
	result.ForwardedTools = observation.ForwardedTools
	result.ToolOutputs = observation.ToolOutputs
	if runErr != nil || stdout.exceeded || stderr.exceeded || deadline.Err() != nil || observation.Unexpected ||
		observation.ModelRequests != 1 || observation.ForwardedTools != 0 || observation.ToolOutputs != 0 || !observation.AuthenticationSeen {
		return result, privatePlanError("review_provider_failed")
	}
	finalFile := []byte(nil)
	if reviewer.Kind == "codex" {
		finalFile, err = readBoundedFile(finalPath, maxReviewBytes)
		if err != nil {
			return result, privatePlanError("review_provider_output")
		}
	}
	metrics, reviewData, err := ParseProviderOutput(reviewer.Kind, stdout.Bytes(), finalFile)
	if err != nil || !metrics.Coverage["input_tokens"] || !metrics.Coverage["output_tokens"] || metrics.InputTokens < 1 || metrics.OutputTokens < 1 {
		return result, privatePlanError("review_provider_output")
	}
	result.InputTokens = metrics.InputTokens
	result.OutputTokens = metrics.OutputTokens
	cost, err := estimateCost(result.InputTokens, result.OutputTokens, execution.Pricing)
	if err != nil {
		return result, privatePlanError("review_cost_unknown")
	}
	result.CostKnown = true
	result.EstimatedCost = cost
	if cost > execution.MaxEstimatedCostMicroUSD {
		return result, privatePlanError("review_cost_cap")
	}
	result.Review, err = writeCompletedPrivateReview(root, packet, template, rubric, reviewData)
	if err != nil {
		return result, err
	}
	return result, nil
}

func privateCodexReviewArgs(reviewer Reviewer, execution PrivateReviewerExecution, workspace, schemaPath, finalPath string) []string {
	args := []string{"exec", "--json", "--ephemeral", "--strict-config", "--skip-git-repo-check", "--ignore-user-config", "--model", reviewer.Model}
	for _, feature := range []string{"apps", "browser_use", "computer_use", "image_generation", "remote_plugin", "shell_tool", "unified_exec", "code_mode_host", "code_mode", "code_mode_only", "multi_agent", "collaboration_modes", "goals"} {
		args = append(args, "--disable", feature)
	}
	return append(args, "--sandbox", "read-only", "-C", workspace, "--output-schema", schemaPath,
		"--output-last-message", finalPath, "-c", `approval_policy="never"`, "-c", `web_search="disabled"`,
		"-c", `project_doc_max_bytes=0`, "-c", `model_reasoning_effort=`+strconv.Quote(execution.Reasoning), "-")
}

func privateClaudeReviewArgs(reviewer Reviewer, execution PrivateReviewerExecution) []string {
	return []string{"-p", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--safe-mode",
		"--disable-slash-commands", "--model", reviewer.Model, "--max-budget-usd", formatMicroUSD(execution.MaxEstimatedCostMicroUSD),
		"--permission-mode", "dontAsk", "--strict-mcp-config", "--no-chrome", "--setting-sources", "",
		"--tools", "", "--allowed-tools", "", "--prompt-suggestions", "false", "--effort", execution.Reasoning}
}

func newClaudeReviewRuntime(root, scratch string, ambient []string) (*claudeReviewRuntime, error) {
	values := environmentMap(ambient)
	runtimeRoot := filepath.Join(scratch, "claude-runtime")
	config := filepath.Join(runtimeRoot, "config")
	home := filepath.Join(runtimeRoot, "home")
	temporary := filepath.Join(runtimeRoot, "tmp")
	for _, directory := range []string{runtimeRoot, config, home, temporary} {
		if err := safepath.MkdirAllWithin(root, directory, 0o700); err != nil {
			return nil, err
		}
	}
	environment := map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": config, "TMPDIR": temporary, "TMP": temporary, "TEMP": temporary,
		"PATH": values["PATH"], "USER": "atl-agent-eval", "LOGNAME": "atl-agent-eval",
		"DISABLE_TELEMETRY": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}
	hasEnvironmentCredential := false
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if values[name] != "" {
			environment[name] = values[name]
			hasEnvironmentCredential = true
		}
	}
	if !hasEnvironmentCredential {
		sourceConfig := values["CLAUDE_CONFIG_DIR"]
		if sourceConfig == "" && values["HOME"] != "" {
			sourceConfig = filepath.Join(values["HOME"], ".claude")
		}
		if sourceConfig == "" || requireOwnerOnly("claude reviewer credential directory", sourceConfig, true) != nil {
			return nil, fmt.Errorf("claude reviewer requires an owner-only credential projection")
		}
		source := filepath.Join(sourceConfig, ".credentials.json")
		info, err := os.Lstat(source)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("claude reviewer requires an owner-only credential projection")
		}
		data, err := readBoundedFile(source, 4<<20)
		if err != nil || !json.Valid(data) {
			return nil, fmt.Errorf("claude reviewer credential projection is invalid")
		}
		var projection struct {
			ClaudeAIOAuth struct {
				AccessToken string `json:"accessToken"`
				ExpiresAt   int64  `json:"expiresAt"`
			} `json:"claudeAiOauth"`
		}
		if json.Unmarshal(data, &projection) != nil || projection.ClaudeAIOAuth.AccessToken == "" ||
			len(projection.ClaudeAIOAuth.AccessToken) > 1<<20 || projection.ClaudeAIOAuth.ExpiresAt <= time.Now().UnixMilli() {
			clear(data)
			return nil, fmt.Errorf("claude reviewer credential projection is expired or invalid")
		}
		environment["CLAUDE_CODE_OAUTH_TOKEN"] = projection.ClaudeAIOAuth.AccessToken
		clear(data)
	}
	return &claudeReviewRuntime{root: runtimeRoot, environment: environment}, nil
}

func (r *claudeReviewRuntime) Environment() map[string]string {
	out := make(map[string]string, len(r.environment))
	for key, value := range r.environment {
		out[key] = value
	}
	return out
}

func (r *claudeReviewRuntime) Close() error {
	if r == nil || r.root == "" {
		return nil
	}
	for key := range r.environment {
		delete(r.environment, key)
	}
	r.environment = nil
	r.root = ""
	return nil
}

func codexReviewAuthMode(session *codexAuthSession) (string, error) {
	data, err := session.authentication()
	if err != nil {
		return "", err
	}
	defer clear(data)
	var projection struct {
		AuthMode string `json:"auth_mode"`
	}
	if json.Unmarshal(data, &projection) != nil {
		return "", fmt.Errorf("codex auth projection is invalid")
	}
	if projection.AuthMode == "chatgpt" {
		return projection.AuthMode, nil
	}
	if projection.AuthMode == "apikey" || projection.AuthMode == "api_key" {
		return "api_key", nil
	}
	return "", fmt.Errorf("unsupported codex auth mode")
}

func startPrivateReviewProxy(provider, model, reasoning, origin string) (*privateReviewProxy, net.Listener, *http.Server, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" {
		return nil, nil, nil, fmt.Errorf("invalid review origin")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, nil, nil, err
	}
	proxy := &privateReviewProxy{provider: provider, model: model, reasoning: reasoning, origin: parsed}
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return proxy, listener, server, nil
}

func (p *privateReviewProxy) Observation() privateReviewProxyObservation {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.observation
}

func (p *privateReviewProxy) ServeHTTP(w http.ResponseWriter, incoming *http.Request) {
	if incoming.Method == http.MethodHead && incoming.URL.Path == "/" && p.provider == "claude-code" {
		p.mu.Lock()
		p.observation.AuxiliaryRequests++
		p.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		return
	}
	if incoming.Method == http.MethodGet && p.provider == "codex" && strings.Contains(incoming.URL.Path, "models") {
		p.mu.Lock()
		p.observation.AuxiliaryRequests++
		p.mu.Unlock()
		p.forward(w, incoming, nil, false)
		return
	}
	if incoming.Method != http.MethodPost || !p.modelPath(incoming.URL.Path) {
		p.rejectUnexpected(w)
		return
	}
	p.mu.Lock()
	if p.observation.ModelRequests != 0 {
		p.observation.Unexpected = true
		p.mu.Unlock()
		p.abortProvider()
		http.Error(w, "single model request already consumed", http.StatusBadRequest)
		return
	}
	p.observation.ModelRequests++
	p.mu.Unlock()
	data, err := io.ReadAll(io.LimitReader(incoming.Body, privateReviewRequestLimit+1))
	if err != nil || len(data) > privateReviewRequestLimit {
		p.rejectUnexpected(w)
		return
	}
	var envelope map[string]any
	if json.Unmarshal(data, &envelope) != nil {
		p.rejectUnexpected(w)
		return
	}
	if envelope["model"] != p.model {
		p.rejectUnexpected(w)
		return
	}
	if !p.reasoningMatches(envelope) {
		p.rejectUnexpected(w)
		return
	}
	inputTools := 0
	if p.provider == "codex" {
		inputTools, err = countAndStripCodexReviewTools(envelope)
	} else {
		inputTools, err = countAndStripClaudeReviewTools(envelope)
	}
	shapeErr := validatePrivateReviewRequestShape(p.provider, envelope)
	if err != nil || shapeErr != nil {
		p.rejectUnexpected(w)
		return
	}
	forwarded := countPrivateReviewTools(p.provider, envelope)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		p.rejectUnexpected(w)
		return
	}
	p.mu.Lock()
	p.observation.InputTools += inputTools
	p.observation.ForwardedTools += forwarded
	p.mu.Unlock()
	if forwarded != 0 {
		p.rejectUnexpected(w)
		return
	}
	p.forward(w, incoming, encoded, true)
}

func (p *privateReviewProxy) modelPath(path string) bool {
	if p.provider == "codex" {
		return strings.HasSuffix(path, "/responses")
	}
	return path == "/v1/messages"
}

func (p *privateReviewProxy) reasoningMatches(envelope map[string]any) bool {
	if p.provider == "codex" {
		reasoning, _ := envelope["reasoning"].(map[string]any)
		return reasoning["effort"] == p.reasoning
	}
	output, _ := envelope["output_config"].(map[string]any)
	return output["effort"] == p.reasoning
}

func (p *privateReviewProxy) rejectUnexpected(w http.ResponseWriter) {
	p.mu.Lock()
	p.observation.Unexpected = true
	p.mu.Unlock()
	p.abortProvider()
	http.Error(w, "review boundary rejected request", http.StatusBadRequest)
}

func (p *privateReviewProxy) abortProvider() {
	p.mu.Lock()
	abort := p.abort
	p.mu.Unlock()
	if abort != nil {
		abort()
	}
}

func (p *privateReviewProxy) forward(w http.ResponseWriter, incoming *http.Request, body []byte, inspectResponse bool) {
	target := *p.origin
	target.Path = incoming.URL.Path
	target.RawQuery = incoming.URL.RawQuery
	request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		p.rejectUnexpected(w)
		return
	}
	for key, values := range incoming.Header {
		if privateReviewHopHeader(key) || strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if len(body) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	authSeen := request.Header.Get("Authorization") != "" || request.Header.Get("X-Api-Key") != ""
	p.mu.Lock()
	p.observation.AuthenticationSeen = p.observation.AuthenticationSeen || authSeen
	p.mu.Unlock()
	if !authSeen {
		p.rejectUnexpected(w)
		return
	}
	// #nosec G704 -- p.origin is selected from fixed provider origins; the request path cannot change scheme or authority.
	response, err := privateReviewHTTPClient.Do(request)
	if err != nil {
		p.abortProvider()
		http.Error(w, "review upstream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	responseData, err := io.ReadAll(io.LimitReader(response.Body, privateReviewResponseLimit+1))
	if err != nil || len(responseData) > privateReviewResponseLimit {
		p.rejectUnexpected(w)
		return
	}
	if inspectResponse {
		inspectionData, decodeErr := decodePrivateReviewResponseBody(responseData, response.Header.Get("Content-Encoding"))
		toolOutputs, inspectErr := 0, decodeErr
		if inspectErr == nil {
			toolOutputs, inspectErr = inspectPrivateReviewResponse(p.provider, inspectionData)
		}
		p.mu.Lock()
		p.observation.ToolOutputs += toolOutputs
		p.observation.Unexpected = p.observation.Unexpected || inspectErr != nil || toolOutputs != 0
		p.mu.Unlock()
		if inspectErr != nil || toolOutputs != 0 {
			p.abortProvider()
			http.Error(w, "review tool output rejected", http.StatusBadGateway)
			return
		}
	}
	for key, values := range response.Header {
		if privateReviewHopHeader(key) || strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseData)
}

func decodePrivateReviewResponseBody(data []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "":
		return data, nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("invalid gzip review response")
		}
		decompressed, readErr := io.ReadAll(io.LimitReader(reader, privateReviewResponseLimit+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(decompressed) > privateReviewResponseLimit {
			return nil, fmt.Errorf("bounded gzip review response failed")
		}
		return decompressed, nil
	default:
		return nil, fmt.Errorf("unsupported review response encoding")
	}
}

func countAndStripCodexReviewTools(envelope map[string]any) (int, error) {
	count := countPrivateReviewTools("codex", envelope)
	if tools, present := envelope["tools"]; present {
		if _, ok := tools.([]any); !ok {
			return 0, fmt.Errorf("invalid Codex review tools")
		}
	}
	delete(envelope, "tools")
	delete(envelope, "tool_choice")
	delete(envelope, "client_metadata")
	if input, ok := envelope["input"].([]any); ok {
		filtered := input[:0]
		for _, raw := range input {
			item, _ := raw.(map[string]any)
			if item["type"] == "additional_tools" {
				if _, ok := item["tools"].([]any); !ok {
					return 0, fmt.Errorf("invalid embedded Codex review tools")
				}
				continue
			}
			filtered = append(filtered, raw)
		}
		envelope["input"] = filtered
	}
	return count, nil
}

func countAndStripClaudeReviewTools(envelope map[string]any) (int, error) {
	count := countPrivateReviewTools("claude-code", envelope)
	if tools, present := envelope["tools"]; present {
		if _, ok := tools.([]any); !ok {
			return 0, fmt.Errorf("invalid Claude review tools")
		}
	}
	delete(envelope, "tools")
	delete(envelope, "tool_choice")
	delete(envelope, "context_management")
	return count, nil
}

func validatePrivateReviewRequestShape(provider string, envelope map[string]any) error {
	allowedKeys := map[string]bool{}
	allowedTypes := map[string]bool{}
	if provider == "codex" {
		for _, key := range []string{"model", "instructions", "input", "include", "max_output_tokens", "metadata", "parallel_tool_calls",
			"prompt_cache_key", "prompt_cache_retention", "reasoning", "safety_identifier", "service_tier", "store", "stream",
			"temperature", "text", "top_logprobs", "top_p", "truncation", "user"} {
			allowedKeys[key] = true
		}
		for _, kind := range []string{"message", "input_text", "json_schema", "object", "array", "string", "integer", "number", "boolean", "null"} {
			allowedTypes[kind] = true
		}
	} else {
		for _, key := range []string{"model", "messages", "system", "max_tokens", "metadata", "stop_sequences", "stream", "temperature",
			"top_k", "top_p", "output_config", "service_tier", "thinking"} {
			allowedKeys[key] = true
		}
		for _, kind := range []string{"text", "ephemeral", "enabled", "adaptive"} {
			allowedTypes[kind] = true
		}
	}
	for key := range envelope {
		if !allowedKeys[key] {
			return fmt.Errorf("unsupported review request field %q", key)
		}
	}
	if provider == "codex" {
		if input, ok := envelope["input"].([]any); !ok || len(input) == 0 {
			return fmt.Errorf("unsupported Codex review input")
		}
		if raw, present := envelope["include"]; present {
			included, ok := raw.([]any)
			if !ok {
				return fmt.Errorf("unsupported Codex review include")
			}
			for _, item := range included {
				if item != "reasoning.encrypted_content" {
					return fmt.Errorf("unsupported Codex review include")
				}
			}
		}
		if parallel, present := envelope["parallel_tool_calls"]; !present || parallel != false {
			return fmt.Errorf("unsupported Codex parallel tool setting")
		}
	} else if messages, ok := envelope["messages"].([]any); !ok || len(messages) == 0 {
		return fmt.Errorf("unsupported Claude review messages")
	}
	return validatePrivateReviewTypeValues(envelope, allowedTypes)
}

func validatePrivateReviewTypeValues(value any, allowed map[string]bool) error {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if err := validatePrivateReviewTypeValues(item, allowed); err != nil {
				return err
			}
		}
	case map[string]any:
		if kind, ok := typed["type"].(string); ok && !allowed[kind] {
			return fmt.Errorf("unsupported review request type %q", kind)
		}
		for _, item := range typed {
			if err := validatePrivateReviewTypeValues(item, allowed); err != nil {
				return err
			}
		}
	}
	return nil
}

func countPrivateReviewTools(provider string, envelope map[string]any) int {
	count := 0
	if tools, ok := envelope["tools"].([]any); ok {
		count += len(tools)
	}
	if provider == "codex" {
		if input, ok := envelope["input"].([]any); ok {
			for _, raw := range input {
				item, _ := raw.(map[string]any)
				if item["type"] == "additional_tools" {
					if tools, ok := item["tools"].([]any); ok {
						count += len(tools)
					}
				}
			}
		}
	}
	return count
}

func inspectPrivateReviewResponse(provider string, data []byte) (int, error) {
	var whole any
	if json.Unmarshal(data, &whole) == nil {
		return inspectPrivateReviewResponseValue(provider, whole)
	}
	count, events := 0, 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte("id:")) ||
			bytes.HasPrefix(line, []byte("retry:")) || bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			return count, fmt.Errorf("unsupported review response framing")
		}
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(line, []byte("[DONE]")) {
			events++
			continue
		}
		var value any
		if len(line) == 0 || json.Unmarshal(line, &value) != nil {
			return count, fmt.Errorf("invalid review response event")
		}
		events++
		observed, inspectErr := inspectPrivateReviewResponseValue(provider, value)
		count += observed
		if inspectErr != nil {
			return count, inspectErr
		}
	}
	if events == 0 {
		return 0, fmt.Errorf("empty review response")
	}
	return count, nil
}

func inspectPrivateReviewResponseValue(provider string, value any) (int, error) {
	count := 0
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			observed, err := inspectPrivateReviewResponseValue(provider, item)
			count += observed
			if err != nil {
				return count, err
			}
		}
	case map[string]any:
		if kind, ok := typed["type"].(string); ok {
			if privateReviewToolOutputType(kind) {
				return 1, nil
			}
			if !privateReviewResponseTypeAllowed(provider, kind) {
				return count, fmt.Errorf("unsupported review response type %q", kind)
			}
		}
		for _, item := range typed {
			observed, err := inspectPrivateReviewResponseValue(provider, item)
			count += observed
			if err != nil {
				return count, err
			}
		}
	}
	return count, nil
}

func privateReviewResponseTypeAllowed(provider, kind string) bool {
	if provider == "codex" {
		switch kind {
		case "response.created", "response.queued", "response.in_progress", "response.completed", "response.failed", "response.incomplete", "response.metadata",
			"response.output_item.added", "response.output_item.done", "response.content_part.added", "response.content_part.done",
			"response.output_text.delta", "response.output_text.done", "response.refusal.delta", "response.refusal.done",
			"response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.reasoning_summary_text.delta",
			"response.reasoning_summary_text.done", "response.reasoning_text.delta", "response.reasoning_text.done", "error",
			"message", "reasoning", "output_text", "refusal", "summary_text", "text", "json_schema",
			"object", "array", "string", "integer", "number", "boolean", "null":
			return true
		}
		return false
	}
	switch kind {
	case "message_start", "message_delta", "message_stop", "content_block_start", "content_block_delta", "content_block_stop", "ping", "error",
		"message", "text", "text_delta", "thinking", "thinking_delta", "signature_delta", "redacted_thinking":
		return true
	default:
		return false
	}
}

func privateReviewToolOutputType(kind string) bool {
	switch kind {
	case "tool_use", "server_tool_use", "tool_result", "function_call", "function_call_output", "custom_tool_call",
		"custom_tool_call_output", "local_shell_call", "computer_call", "computer_call_output", "web_search_call", "mcp_call",
		"file_search_call", "file_search_call_results", "code_interpreter_call", "image_generation_call", "apply_patch_call",
		"apply_patch_call_output", "shell_call", "shell_call_output", "server_tool_result", "web_search_tool_result":
		return true
	default:
		return false
	}
}

func privateReviewHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}
