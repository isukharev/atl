package agenteval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type ProviderCommand struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
}

type ProviderMetrics struct {
	AgentTurns             int
	ToolCalls              int
	Delegations            int
	InputTokens            int64
	OutputTokens           int64
	MainThreadInputTokens  int64
	MainThreadOutputTokens int64
	EstimatedCostMicroUSD  int64
	DurationMillis         int64
	Coverage               map[string]bool
}

func BuildProviderCommand(spec RunSpec, agentBinary, workspace, schemaPath, finalPath, pluginRoot, settingsPath string, responseSchema []byte) (ProviderCommand, error) {
	if err := spec.Validate(); err != nil {
		return ProviderCommand{}, err
	}
	switch spec.Provider {
	case "claude-code":
		if !json.Valid(responseSchema) {
			return ProviderCommand{}, fmt.Errorf("response schema is not valid JSON")
		}
		args := []string{
			"-p", "--output-format", "stream-json", "--verbose",
			"--no-session-persistence", "--model", spec.Model,
			"--max-budget-usd", formatMicroUSD(spec.MaxEstimatedCostMicroUSD),
			"--permission-mode", "dontAsk", "--strict-mcp-config", "--no-chrome",
			"--setting-sources", "project",
			"--tools", strings.Join(claudeToolNames(spec.AllowedTools), ","),
			"--allowed-tools", strings.Join(spec.AllowedTools, ","),
			"--json-schema", string(responseSchema),
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
		args := []string{
			"exec", "--json", "--ephemeral", "--strict-config",
			"--ignore-user-config", "--skip-git-repo-check",
			"--model", spec.Model, "--sandbox", "read-only", "-C", workspace,
			"--output-schema", schemaPath, "--output-last-message", finalPath,
			"-c", `shell_environment_policy.inherit="all"`,
			"-c", `shell_environment_policy.include_only=["PATH","ATL_READ_ONLY","ATL_NO_UPDATE","ATL_CONFIG_DIR","ATL_MIRROR_ROOT","ATL_JIRA_URL","ATL_CONFLUENCE_URL","ATL_JIRA_PAT","ATL_CONFLUENCE_PAT","ATL_ALLOW_INSECURE","ATL_EVAL_REAL_BINARY","ATL_EVAL_COUNTER"]`,
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
	metrics := ProviderMetrics{Coverage: map[string]bool{}}
	var final []byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return ProviderMetrics{}, nil, fmt.Errorf("decode Claude event: %w", err)
		}
		if event["type"] == "assistant" {
			toolCalls, delegations := countClaudeToolCalls(event)
			metrics.ToolCalls += toolCalls
			metrics.Delegations += delegations
			metrics.Coverage["tool_calls"] = true
			metrics.Coverage["delegations"] = true
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
	return metrics, bytes.TrimSpace(final), nil
}

func parseCodexOutput(data []byte) (ProviderMetrics, error) {
	metrics := ProviderMetrics{Coverage: map[string]bool{"tool_calls": true}}
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
				if kind != "agent_message" && kind != "reasoning" {
					metrics.ToolCalls++
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
	return metrics, nil
}

func countClaudeToolCalls(event map[string]any) (int, int) {
	message, _ := event["message"].(map[string]any)
	content, _ := message["content"].([]any)
	var count, delegations int
	for _, value := range content {
		block, _ := value.(map[string]any)
		if block["type"] == "tool_use" {
			count++
			name, _ := block["name"].(string)
			if name == "Agent" || name == "Task" {
				delegations++
			}
		}
	}
	return count, delegations
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
