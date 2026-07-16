package agenteval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

type ProviderCommand struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
}

type ProviderConfinement struct {
	RequestDirectory  string
	ResponseDirectory string
}

const codexAgentEvalPermissionProfile = "atl_agent_eval"

type ProviderMetrics struct {
	AgentTurns               int
	ToolCalls                int
	Delegations              int
	InputTokens              int64
	OutputTokens             int64
	MainThreadInputTokens    int64
	MainThreadOutputTokens   int64
	EstimatedCostMicroUSD    int64
	DurationMillis           int64
	MCPToolCalls             int
	FailedMCPToolCalls       int
	MCPToolOutputBytes       int64
	CapabilityFamilies       []CapabilityFamilyMetric
	CapabilityFamilyCoverage bool
	Coverage                 map[string]bool
}

func BuildProviderCommand(spec RunSpec, agentBinary, atlBinary, guardPath, workspace, schemaPath, finalPath, pluginRoot, settingsPath, mcpConfigPath string, confinement ProviderConfinement, responseSchema []byte) (ProviderCommand, error) {
	if err := spec.Validate(); err != nil {
		return ProviderCommand{}, err
	}
	switch spec.Provider {
	case "claude-code":
		if !json.Valid(responseSchema) {
			return ProviderCommand{}, fmt.Errorf("response schema is not valid JSON")
		}
		toolNames := claudeToolNames(spec.AllowedTools)
		allowedTools := spec.AllowedTools
		settingSources := "project"
		if spec.EffectiveBackendMode() == BackendModePrivateLive {
			settingSources = ""
		}
		if spec.ToolTransport == "mcp" {
			if atlBinary == "" || guardPath == "" || mcpConfigPath == "" {
				return ProviderCommand{}, fmt.Errorf("claude mcp transport requires atl, guard, and MCP config paths")
			}
			// Claude's --tools flag configures the built-in tool set, but supplying
			// it (including an empty value or MCP-qualified names) can hide tools
			// discovered from a connected MCP server. Omit both CLI tool filters for
			// MCP runs; dontAsk plus the exact private-settings permission list
			// remains the execution boundary for built-ins and dynamic tools alike.
			toolNames = nil
			allowedTools = claudeMCPToolNames(spec.AllowedMCPTools)
		}
		args := []string{
			"-p", "--output-format", "stream-json", "--verbose",
			"--no-session-persistence", "--model", spec.Model,
			"--max-budget-usd", formatMicroUSD(spec.MaxEstimatedCostMicroUSD),
			"--permission-mode", "dontAsk", "--strict-mcp-config", "--no-chrome",
			"--setting-sources", settingSources,
		}
		if spec.ToolTransport != "mcp" {
			args = append(args,
				"--tools", strings.Join(toolNames, ","),
				"--allowed-tools", strings.Join(allowedTools, ","),
			)
		}
		args = append(args, "--json-schema", string(responseSchema))
		if spec.ToolTransport == "mcp" {
			args = append(args, "--mcp-config", mcpConfigPath)
		}
		if spec.Reasoning != "" {
			args = append(args, "--effort", spec.Reasoning)
		}
		if pluginRoot != "" {
			args = append(args, "--plugin-dir", pluginRoot)
		}
		if settingsPath != "" {
			args = append(args, "--settings", settingsPath)
		}
		return ProviderCommand{Path: agentBinary, Args: args}, nil
	case "codex":
		includeOnly := `["PATH","ATL_READ_ONLY","ATL_NO_UPDATE","ATL_CONFIG_DIR","ATL_MIRROR_ROOT","ATL_JIRA_URL","ATL_CONFLUENCE_URL","ATL_JIRA_PAT","ATL_CONFLUENCE_PAT","ATL_ALLOW_INSECURE","ATL_EVAL_REAL_BINARY","ATL_EVAL_COUNTER"]`
		sandboxMode := "read-only"
		if spec.ToolTransport == "mcp" {
			includeOnly = `["PATH","LANG","LC_ALL","TERM"]`
		}
		if spec.EffectiveBackendMode() == BackendModePrivateLive && spec.ToolTransport == "cli" {
			sandboxMode = "workspace-write"
			includeOnly = `["PATH","LANG","LC_ALL","TERM","ATL_READ_ONLY","ATL_EVAL_COUNTER","ATL_EVAL_GUARD_COUNTER","ATL_EVAL_CLI_POLICY_FILE","ATL_EVAL_COMMAND_BROKER_FILE","ATL_EVAL_GUARD_MODE","ATL_EVAL_ALLOWED_READ_ROOTS"]`
		}
		args := []string{
			"exec", "--json", "--ephemeral", "--strict-config",
			"--ignore-user-config", "--skip-git-repo-check",
			"--model", spec.Model,
		}
		privateCLI := spec.EffectiveBackendMode() == BackendModePrivateLive && spec.ToolTransport == "cli"
		if !privateCLI {
			args = append(args, "--sandbox", sandboxMode)
		}
		args = append(args,
			"-C", workspace,
			"--output-schema", schemaPath, "--output-last-message", finalPath,
			"-c", `project_doc_max_bytes=0`,
			"-c", `shell_environment_policy.inherit="all"`,
			"-c", `shell_environment_policy.include_only=`+includeOnly,
		)
		if spec.ToolTransport == "mcp" {
			if atlBinary == "" || guardPath == "" {
				return ProviderCommand{}, fmt.Errorf("codex mcp transport requires atl and guard executables")
			}
			args = append(args,
				"--dangerously-bypass-hook-trust",
				"-c", `web_search="disabled"`,
				"-c", `mcp_servers.atl.command=`+strconv.Quote(atlBinary),
				"-c", `mcp_servers.atl.args=["mcp","serve"]`,
				"-c", `mcp_servers.atl.required=true`,
				"-c", `mcp_servers.atl.enabled_tools=`+quotedStringList(spec.AllowedMCPTools),
				"-c", `mcp_servers.atl.default_tools_approval_mode="approve"`,
				"-c", `mcp_servers.atl.env_vars=["ATL_READ_ONLY","ATL_NO_UPDATE","ATL_CONFIG_DIR","ATL_MIRROR_ROOT","ATL_JIRA_URL","ATL_CONFLUENCE_URL","ATL_JIRA_PAT","ATL_CONFLUENCE_PAT","ATL_ALLOW_INSECURE","ATL_EVAL_HTTP_GUARD_FILE"]`,
				"-c", codexDenyNonMCPHook(guardPath),
			)
		}
		if spec.EffectiveBackendMode() == BackendModePrivateLive && spec.ToolTransport == "cli" {
			if guardPath == "" {
				return ProviderCommand{}, fmt.Errorf("codex private-live cli transport requires a guard executable")
			}
			confinementArgs, err := codexConfinementConfigArgs(confinement, true)
			if err != nil {
				return ProviderCommand{}, err
			}
			args = append(args,
				"--ignore-rules", "--dangerously-bypass-hook-trust",
				"-c", `approval_policy="never"`,
				"-c", `web_search="disabled"`,
				"-c", codexDenyNonMCPHook(guardPath),
			)
			args = append(args, confinementArgs...)
		}
		if spec.Reasoning != "" {
			args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(spec.Reasoning))
		}
		args = append(args, "-")
		return ProviderCommand{Path: agentBinary, Args: args}, nil
	default:
		return ProviderCommand{}, fmt.Errorf("unsupported provider %q", spec.Provider)
	}
}

func BuildCodexConfinementProbeCommand(agentBinary, workspace, probeExecutable string, confinement ProviderConfinement) (ProviderCommand, error) {
	if agentBinary == "" || workspace == "" || probeExecutable == "" {
		return ProviderCommand{}, fmt.Errorf("codex confinement probe requires agent, workspace, and probe executable")
	}
	configArgs, err := codexConfinementConfigArgs(confinement, false)
	if err != nil {
		return ProviderCommand{}, err
	}
	args := []string{"sandbox", "-P", codexAgentEvalPermissionProfile}
	args = append(args, configArgs...)
	args = append(args, "-C", workspace, probeExecutable)
	return ProviderCommand{Path: agentBinary, Args: args}, nil
}

func codexConfinementConfigArgs(confinement ProviderConfinement, selectAsDefault bool) ([]string, error) {
	if !validConfinementDirectory(confinement.RequestDirectory) || !validConfinementDirectory(confinement.ResponseDirectory) || confinement.RequestDirectory == confinement.ResponseDirectory {
		return nil, fmt.Errorf("codex private-live cli confinement has invalid broker directories")
	}
	profile := "permissions." + codexAgentEvalPermissionProfile
	settings := []string{
		profile + `.extends=":workspace"`,
		profile + `.filesystem={` + strconv.Quote(confinement.RequestDirectory) + `="write",` + strconv.Quote(confinement.ResponseDirectory) + `="read"}`,
	}
	if selectAsDefault {
		settings = append([]string{`default_permissions="` + codexAgentEvalPermissionProfile + `"`}, settings...)
	}
	args := make([]string, 0, len(settings)*2)
	for _, setting := range settings {
		args = append(args, "-c", setting)
	}
	return args, nil
}

func validConfinementDirectory(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}

func quotedStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func claudeMCPToolNames(values []string) []string {
	qualified := make([]string, len(values))
	for i, value := range values {
		qualified[i] = "mcp__atl__" + value
	}
	return qualified
}

func codexDenyNonMCPHook(guardPath string) string {
	return `hooks.PreToolUse=[{matcher="^(Bash|apply_patch|Edit|Write|Read|Agent)$",hooks=[{type="command",command=` + strconv.Quote(guardPath) + `,timeout=5}]}]`
}

func claudeToolNames(rules []string) []string {
	seen := map[string]struct{}{}
	var names []string
	for _, rule := range rules {
		name := rule
		if index := strings.IndexByte(name, '('); index >= 0 {
			name = name[:index]
		}
		name = strings.TrimSpace(name)
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func ParseProviderOutput(provider string, transcript, finalFile []byte) (ProviderMetrics, []byte, error) {
	switch provider {
	case "claude-code":
		return parseClaudeOutput(transcript)
	case "codex":
		metrics, err := parseCodexOutput(transcript)
		if err != nil {
			return ProviderMetrics{}, nil, err
		}
		if len(bytes.TrimSpace(finalFile)) == 0 {
			return ProviderMetrics{}, nil, fmt.Errorf("codex final response is empty")
		}
		return metrics, bytes.TrimSpace(finalFile), nil
	default:
		return ProviderMetrics{}, nil, fmt.Errorf("unsupported provider %q", provider)
	}
}

func parseClaudeOutput(data []byte) (ProviderMetrics, []byte, error) {
	metrics := ProviderMetrics{Coverage: map[string]bool{}, CapabilityFamilyCoverage: true}
	mcpToolUseIDs := map[string]string{}
	families := map[string]CapabilityFamilyMetric{}
	var final []byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return ProviderMetrics{}, nil, fmt.Errorf("decode Claude event: %w", err)
		}
		if event["type"] == "assistant" {
			toolCalls, delegations, mcpIDs := countClaudeToolCalls(event)
			metrics.ToolCalls += toolCalls
			metrics.Delegations += delegations
			for id, family := range mcpIDs {
				mcpToolUseIDs[id] = family
			}
			metrics.Coverage["tool_calls"] = true
			metrics.Coverage["delegations"] = true
		}
		if event["type"] == "user" {
			calls, failed, outputBytes, attributed, complete, err := countClaudeMCPResults(event, mcpToolUseIDs)
			if err != nil {
				return ProviderMetrics{}, nil, err
			}
			metrics.MCPToolCalls += calls
			metrics.FailedMCPToolCalls += failed
			metrics.MCPToolOutputBytes += outputBytes
			for _, value := range attributed {
				existing := families[value.Family]
				existing.Family = value.Family
				existing.Invocations += value.Invocations
				existing.Successes += value.Successes
				existing.Failures += value.Failures
				existing.OutputBytes += value.OutputBytes
				families[value.Family] = existing
			}
			metrics.CapabilityFamilyCoverage = metrics.CapabilityFamilyCoverage && complete
		}
		if event["type"] != "result" {
			continue
		}
		if isError, _ := event["is_error"].(bool); isError {
			return ProviderMetrics{}, nil, fmt.Errorf("claude run reported an error result")
		}
		if value, ok := jsonInt64(event["num_turns"]); ok {
			metrics.AgentTurns = int(value)
			metrics.Coverage["agent_turns"] = true
		}
		if value, ok := jsonInt64(event["duration_ms"]); ok {
			metrics.DurationMillis = value
			metrics.Coverage["duration_millis"] = true
		}
		if value, ok := jsonFloat64(event["total_cost_usd"]); ok {
			metrics.EstimatedCostMicroUSD = int64(math.Round(value * 1_000_000))
			metrics.Coverage["estimated_cost_microusd"] = true
		}
		if usage, ok := event["usage"].(map[string]any); ok {
			if value, ok := jsonInt64(usage["input_tokens"]); ok {
				metrics.MainThreadInputTokens += value
				metrics.Coverage["main_thread_input_tokens"] = true
			}
			for _, name := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
				if value, ok := jsonInt64(usage[name]); ok {
					metrics.MainThreadInputTokens += value
					metrics.Coverage["main_thread_input_tokens"] = true
				}
			}
			if value, ok := jsonInt64(usage["output_tokens"]); ok {
				metrics.MainThreadOutputTokens = value
				metrics.Coverage["main_thread_output_tokens"] = true
			}
		}
		if modelUsage, ok := event["modelUsage"].(map[string]any); ok {
			for _, raw := range modelUsage {
				usage, _ := raw.(map[string]any)
				for _, name := range []string{"inputTokens", "cacheReadInputTokens", "cacheCreationInputTokens"} {
					if value, ok := jsonInt64(usage[name]); ok {
						metrics.InputTokens += value
						metrics.Coverage["input_tokens"] = true
					}
				}
				if value, ok := jsonInt64(usage["outputTokens"]); ok {
					metrics.OutputTokens += value
					metrics.Coverage["output_tokens"] = true
				}
			}
		}
		if value, ok := event["structured_output"]; ok && value != nil {
			final, _ = json.Marshal(value)
		} else if value, ok := event["result"].(string); ok {
			final = []byte(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return ProviderMetrics{}, nil, err
	}
	if len(bytes.TrimSpace(final)) == 0 {
		return ProviderMetrics{}, nil, fmt.Errorf("claude final response is empty")
	}
	if !metrics.Coverage["input_tokens"] && metrics.Coverage["main_thread_input_tokens"] {
		metrics.InputTokens = metrics.MainThreadInputTokens
		metrics.Coverage["input_tokens"] = true
	}
	if !metrics.Coverage["output_tokens"] && metrics.Coverage["main_thread_output_tokens"] {
		metrics.OutputTokens = metrics.MainThreadOutputTokens
		metrics.Coverage["output_tokens"] = true
	}
	metrics.CapabilityFamilies = capabilityFamilySlice(families)
	return metrics, bytes.TrimSpace(final), nil
}

func parseCodexOutput(data []byte) (ProviderMetrics, error) {
	metrics := ProviderMetrics{Coverage: map[string]bool{"tool_calls": true, "delegations": true}, CapabilityFamilyCoverage: true}
	families := map[string]CapabilityFamilyMetric{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return ProviderMetrics{}, fmt.Errorf("decode Codex event: %w", err)
		}
		switch event["type"] {
		case "item.completed":
			if item, ok := event["item"].(map[string]any); ok {
				kind, _ := item["type"].(string)
				// Codex also emits completed diagnostic "error" items for
				// invocation-level warnings (for example reviewed hook trust).
				// They are neither model tool calls nor failed tool results.
				if kind != "agent_message" && kind != "reasoning" && kind != "error" {
					metrics.ToolCalls++
				}
				if kind == "mcp_tool_call" {
					metrics.MCPToolCalls++
					if status, _ := item["status"].(string); status == "failed" {
						metrics.FailedMCPToolCalls++
					}
					if result, ok := item["result"]; ok {
						if data, err := json.Marshal(result); err == nil {
							metrics.MCPToolOutputBytes += int64(len(data))
						}
					}
					tool, _ := item["tool"].(string)
					family, known := CapabilityFamilyForMCP(tool)
					if !known {
						metrics.CapabilityFamilyCoverage = false
					} else {
						failed := false
						if status, _ := item["status"].(string); status == "failed" {
							failed = true
						}
						var size int64
						if result, ok := item["result"]; ok {
							if data, err := json.Marshal(result); err == nil {
								size = int64(len(data))
							}
						}
						mergeCapabilityFamily(families, family, failed, size)
					}
				}
			}
		case "turn.completed":
			metrics.AgentTurns++
			metrics.Coverage["agent_turns"] = true
			if usage, ok := event["usage"].(map[string]any); ok {
				if value, ok := jsonInt64(usage["input_tokens"]); ok {
					metrics.InputTokens += value
					metrics.MainThreadInputTokens += value
					metrics.Coverage["input_tokens"] = true
					metrics.Coverage["main_thread_input_tokens"] = true
				}
				if value, ok := jsonInt64(usage["output_tokens"]); ok {
					metrics.OutputTokens += value
					metrics.MainThreadOutputTokens += value
					metrics.Coverage["output_tokens"] = true
					metrics.Coverage["main_thread_output_tokens"] = true
				}
			}
		case "turn.failed", "error":
			return ProviderMetrics{}, fmt.Errorf("codex run reported a failure event")
		}
	}
	if err := scanner.Err(); err != nil {
		return ProviderMetrics{}, err
	}
	metrics.CapabilityFamilies = capabilityFamilySlice(families)
	return metrics, nil
}

func countClaudeToolCalls(event map[string]any) (int, int, map[string]string) {
	message, _ := event["message"].(map[string]any)
	content, _ := message["content"].([]any)
	var count, delegations int
	mcpIDs := map[string]string{}
	for _, value := range content {
		block, _ := value.(map[string]any)
		if block["type"] == "tool_use" {
			count++
			name, _ := block["name"].(string)
			if name == "Agent" || name == "Task" {
				delegations++
			}
			if strings.HasPrefix(name, "mcp__atl__") {
				if id, _ := block["id"].(string); id != "" {
					family, _ := CapabilityFamilyForMCP(strings.TrimPrefix(name, "mcp__atl__"))
					mcpIDs[id] = family
				}
			}
		}
	}
	return count, delegations, mcpIDs
}

func countClaudeMCPResults(event map[string]any, mcpToolUseIDs map[string]string) (int, int, int64, []CapabilityFamilyMetric, bool, error) {
	message, _ := event["message"].(map[string]any)
	content, _ := message["content"].([]any)
	var calls int
	var failed int
	var outputBytes int64
	families := map[string]CapabilityFamilyMetric{}
	complete := true
	for _, value := range content {
		block, _ := value.(map[string]any)
		if block["type"] != "tool_result" {
			continue
		}
		id, _ := block["tool_use_id"].(string)
		family, ok := mcpToolUseIDs[id]
		if !ok {
			continue
		}
		// Claude emits this exact string class when its client cannot resolve a
		// requested tool while an MCP server is still starting. The attempt is
		// already a model tool call, but it never reached atl. Actual MCP
		// responses, including server errors, carry an object here. Unknown
		// shapes fail closed so a provider change cannot silently undercount.
		switch result := event["tool_use_result"].(type) {
		case map[string]any:
		case string:
			if strings.HasPrefix(result, "Error: No such tool available:") {
				continue
			}
			return 0, 0, 0, nil, false, fmt.Errorf("claude MCP result has an unsupported client-side shape")
		default:
			return 0, 0, 0, nil, false, fmt.Errorf("claude MCP result is missing its provider envelope")
		}
		calls++
		if isError, _ := block["is_error"].(bool); isError {
			failed++
		}
		var resultBytes int64
		switch result := block["content"].(type) {
		case string:
			resultBytes = int64(len(result))
			outputBytes += resultBytes
		case nil:
		default:
			if encoded, err := json.Marshal(result); err == nil {
				resultBytes = int64(len(encoded))
				outputBytes += resultBytes
			}
		}
		if family == "" {
			complete = false
		} else {
			isError, _ := block["is_error"].(bool)
			mergeCapabilityFamily(families, family, isError, resultBytes)
		}
	}
	return calls, failed, outputBytes, capabilityFamilySlice(families), complete, nil
}

func jsonInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		return int64(number), number >= 0 && number <= math.MaxInt64 && math.Trunc(number) == number
	case json.Number:
		value, err := number.Int64()
		return value, err == nil && value >= 0
	default:
		return 0, false
	}
}

func jsonFloat64(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && number >= 0
}

func formatMicroUSD(value int64) string {
	return strconv.FormatFloat(float64(value)/1_000_000, 'f', 6, 64)
}
