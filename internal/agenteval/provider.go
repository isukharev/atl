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
	GuardMode         string
	GuardCounterPath  string
	WorkspaceReadRoot string
	AllowedReadRoots  []string
	SkillReadRoots    []string
	AllowedMCPTools   []string
}

const (
	codexAgentEvalPermissionProfile           = "atl_agent_eval"
	codexPrivateCLIInstructionsText           = "This is an evidence task. Use the literal atl executable through the shell tool to retrieve the evidence required for the answer. Make only the minimum necessary invocation or invocations allowed by the reviewed command policy. Base the answer on the returned evidence; a no-tool answer or an answer based on assumptions is invalid for this benchmark. Never use apply_patch, Edit, Write, or direct filesystem operations to create, inspect, or modify command-broker manifests or request/response files. If evidence retrieval through atl fails, do not invent or use an alternate broker-file protocol; return the failure through the required response schema."
	codexPrivateCLIInstructionsReinforcedTail = ", then use the literal atl executable through the shell tool to retrieve the evidence required for the answer. Make only the minimum necessary invocation or invocations allowed by the reviewed command policy. Base the answer on the returned evidence; a no-tool answer or an answer based on assumptions is invalid for this benchmark. Never use apply_patch, Edit, Write, or direct filesystem operations to create, inspect, or modify command-broker manifests or request/response files. If evidence retrieval through atl fails, do not invent or use an alternate broker-file protocol; return the failure through the required response schema."
	maxProviderPromptBytes                    = 1 << 20
)

type ProviderMetrics struct {
	AgentTurns               int
	ToolCalls                int
	SkillToolCalls           int
	SkillToolCallsByName     map[string]int
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
	CommandExecutions        int
	CapabilityFamilies       []CapabilityFamilyMetric
	CapabilityFamilyCoverage bool
	Coverage                 map[string]bool
}

func BuildProviderCommand(spec RunSpec, agentBinary, atlBinary, guardPath, workspace, schemaPath, finalPath, pluginRoot, settingsPath, mcpConfigPath string, confinement ProviderConfinement, responseSchema []byte) (ProviderCommand, error) {
	if err := spec.Validate(); err != nil {
		return ProviderCommand{}, err
	}
	projectedResponseSchema, err := providerResponseSchema(spec, responseSchema)
	if err != nil {
		return ProviderCommand{}, err
	}
	switch spec.Provider {
	case "claude-code":
		toolNames := claudeToolNames(spec.AllowedTools)
		allowedTools := append([]string(nil), spec.AllowedTools...)
		if containsRunString(spec.AllowedTools, "Bash(atl *)") && !containsRunString(allowedTools, "Bash(export ATL_READ_ONLY=1)") {
			// Shipped read-only skills intentionally establish a block-wide
			// safety policy before their first atl command. Grant only that exact
			// inert export; the hook still rejects every other shell command.
			allowedTools = append(allowedTools, "Bash(export ATL_READ_ONLY=1)")
		}
		if containsRunString(spec.AllowedTools, "Bash(atl *)") && !containsRunString(allowedTools, "Bash(command -v atl)") {
			// The same preflight verifies which executable the benchmark is about
			// to use. Keep this as an exact inert command rather than broadening
			// the reviewed atl prefix.
			allowedTools = append(allowedTools, "Bash(command -v atl)")
		}
		if spec.EffectiveBackendMode() == BackendModePrivateLive && spec.ToolTransport == "cli" &&
			containsRunString(spec.AllowedTools, "Bash(atl *)") && !containsRunString(toolNames, "Read") {
			// Large reviewed CLI responses are staged by the benchmark command
			// broker. Expose Claude's reader for that artifact; the PreToolUse hook
			// still binds it to the exact runner-owned directory, line window, and
			// read budget. It is never interface or backend evidence.
			toolNames = append(toolNames, "Read")
			allowedTools = append(allowedTools, "Read")
		}
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
			allowedTools = claudeMCPToolNamesForServer(mcpServerName(spec), spec.AllowedMCPTools)
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
		args = append(args, "--json-schema", string(projectedResponseSchema))
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
		includeOnly := `["PATH","ATL_READ_ONLY","ATL_NO_UPDATE","ATL_CONFIG_DIR","ATL_MIRROR_ROOT","ATL_JIRA_URL","ATL_CONFLUENCE_URL","ATL_JIRA_PAT","ATL_CONFLUENCE_PAT","ATL_ALLOW_INSECURE","ATL_EVAL_REAL_BINARY","ATL_EVAL_COUNTER","ATL_EVAL_ALLOWED_COMMANDS"]`
		sandboxMode := "read-only"
		if spec.ToolTransport == "mcp" {
			includeOnly = `["PATH","LANG","LC_ALL","TERM"]`
			if spec.EffectiveSurface() == SurfaceExternalMCP {
				includeOnly = `["PATH","LANG","LC_ALL","TERM","NO_PROXY","no_proxy","ATL_EVAL_EXTERNAL_MCP_TOKEN"]`
			}
			if spec.EffectiveBackendMode() == BackendModePrivateLive {
				includeOnly = `["PATH","LANG","LC_ALL","TERM","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_SKILL_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
				if spec.EffectiveSurface() == SurfaceExternalMCP {
					includeOnly = `["PATH","LANG","LC_ALL","TERM","NO_PROXY","no_proxy","ATL_EVAL_EXTERNAL_MCP_TOKEN","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_SKILL_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
				}
			}
		}
		confinedCLI := isCodexConfinedCLI(spec)
		privateCLI := spec.EffectiveBackendMode() == BackendModePrivateLive || spec.EffectiveBackendMode() == BackendModeProviderCalibration
		if confinedCLI {
			sandboxMode = "workspace-write"
			includeOnly = `["PATH","SHELL","LANG","LC_ALL","TERM","ATL_READ_ONLY","ATL_EVAL_COUNTER","ATL_EVAL_GUARD_COUNTER","ATL_EVAL_CLI_POLICY_FILE","ATL_EVAL_COMMAND_BROKER_FILE","ATL_EVAL_GUARD_MODE","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_SKILL_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
			if spec.EffectiveBackendMode() == BackendModePrivateLive && spec.AllowLiveWrites {
				includeOnly = `["PATH","SHELL","LANG","LC_ALL","TERM","ATL_READ_ONLY","ATL_EVAL_ALLOW_REVIEWED_WRITES","ATL_EVAL_COUNTER","ATL_EVAL_GUARD_COUNTER","ATL_EVAL_CLI_POLICY_FILE","ATL_EVAL_COMMAND_BROKER_FILE","ATL_EVAL_GUARD_MODE","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_SKILL_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
			} else if spec.EffectiveBackendMode() == BackendModeSynthetic && spec.AllowSyntheticWrites {
				includeOnly = `["PATH","SHELL","LANG","LC_ALL","TERM","ATL_EVAL_ALLOW_SYNTHETIC_WRITES","ATL_EVAL_COUNTER","ATL_EVAL_GUARD_COUNTER","ATL_EVAL_CLI_POLICY_FILE","ATL_EVAL_COMMAND_BROKER_FILE","ATL_EVAL_GUARD_MODE","ATL_EVAL_ALLOWED_READ_ROOTS","ATL_EVAL_SKILL_READ_ROOTS","ATL_EVAL_WORKSPACE_ROOT"]`
			}
		}
		args := []string{
			"exec", "--json", "--ephemeral", "--strict-config",
			"--skip-git-repo-check",
			"--model", spec.Model,
		}
		if !privateCLI {
			args = append(args, "--ignore-user-config")
		}
		// Provider-managed remote tools are outside the reviewed benchmark
		// surface. Disable them explicitly rather than relying on a clean home:
		// account-side Apps or browser/computer capabilities may otherwise be
		// discovered after authentication. The built-in shell and the exact MCP
		// server configured below remain available and are still hook-guarded.
		for _, feature := range []string{"apps", "browser_use", "computer_use", "image_generation", "remote_plugin"} {
			args = append(args, "--disable", feature)
		}
		if privateCLI {
			// Pin the local execution route instead of relying on feature defaults
			// that may differ between reviewed Codex binaries. Hooks, the custom
			// filesystem profile, and the command broker remain the authority over
			// what that shell can do.
			args = append(args, "--enable", "shell_tool", "--enable", "unified_exec")
		}
		if !confinedCLI {
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
			if spec.EffectiveSurface() == SurfaceExternalMCP {
				if spec.mcpServerURL == "" || spec.mcpBearerTokenEnv == "" {
					return ProviderCommand{}, fmt.Errorf("codex external MCP requires a local proxy")
				}
				hookConfig, err := codexDenyNonMCPHook(guardPath, spec, confinement)
				if err != nil {
					return ProviderCommand{}, err
				}
				args = append(args,
					"--dangerously-bypass-hook-trust", "-c", `web_search="disabled"`,
					"-c", `mcp_servers.external_ro.url=`+strconv.Quote(spec.mcpServerURL),
					"-c", `mcp_servers.external_ro.bearer_token_env_var=`+strconv.Quote(spec.mcpBearerTokenEnv),
					"-c", `mcp_servers.external_ro.required=true`,
					"-c", `mcp_servers.external_ro.enabled_tools=`+quotedStringList(spec.AllowedMCPTools),
					"-c", `mcp_servers.external_ro.default_tools_approval_mode="approve"`,
					"-c", hookConfig,
				)
			} else {
				hookConfig, err := codexDenyNonMCPHook(guardPath, spec, confinement)
				if err != nil {
					return ProviderCommand{}, err
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
					"-c", hookConfig,
				)
			}
		}
		if confinedCLI {
			if guardPath == "" {
				return ProviderCommand{}, fmt.Errorf("codex confined cli transport requires a guard executable")
			}
			hookConfig, err := codexDenyNonMCPHook(guardPath, spec, confinement)
			if err != nil {
				return ProviderCommand{}, err
			}
			confinementArgs, err := codexConfinementConfigArgs(confinement, true)
			if err != nil {
				return ProviderCommand{}, err
			}
			args = append(args,
				"--ignore-rules", "--dangerously-bypass-hook-trust",
				"-c", `approval_policy="never"`,
				"-c", `web_search="disabled"`,
				"-c", hookConfig,
			)
			if privateCLI {
				developerInstructions, err := codexPrivateCLIInstructions(spec)
				if err != nil {
					return ProviderCommand{}, err
				}
				args = append(args,
					"-c", `plugins."atl@atl".enabled=true`,
					"-c", `developer_instructions=`+strconv.Quote(developerInstructions),
				)
			}
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

func isCodexConfinedCLI(spec RunSpec) bool {
	mode := spec.EffectiveBackendMode()
	return spec.Provider == "codex" && spec.EffectiveToolTransport() == "cli" &&
		(mode == BackendModePrivateLive || mode == BackendModeProviderCalibration || isCodexSyntheticBrokerCLI(spec))
}

func codexPrivateCLIInstructions(spec RunSpec) (string, error) {
	descriptor, err := describeSkillActivation(spec.SkillActivation)
	if err != nil {
		return "", err
	}
	skill, err := descriptor.serviceSkill(spec.DataCapabilities)
	if err != nil {
		return "", err
	}
	if !descriptor.developerReinforcement {
		return codexPrivateCLIInstructionsText, nil
	}
	// Preserve the exact developer channel used by the pre-v4 compatibility
	// control. The factorial treatment must remove or add only this channel;
	// rewording it would create a new, confounded intervention.
	return "This is an evidence task. Before answering, select and follow the installed $" + skill +
		" skill implied by the reviewed data capabilities" + codexPrivateCLIInstructionsReinforcedTail, nil
}

func effectiveProviderPrompt(spec RunSpec, core []byte) ([]byte, error) {
	descriptor, err := describeSkillActivation(spec.SkillActivation)
	if err != nil {
		return nil, err
	}
	skill, err := descriptor.serviceSkill(spec.DataCapabilities)
	if err != nil {
		return nil, err
	}
	if !descriptor.promptPrefix {
		if len(core) > maxProviderPromptBytes {
			return nil, fmt.Errorf("effective provider prompt exceeds %d bytes", maxProviderPromptBytes)
		}
		return core, nil
	}
	prefix := []byte("$" + skill + "\n\n")
	if len(core) > maxProviderPromptBytes-len(prefix) {
		return nil, fmt.Errorf("effective provider prompt exceeds %d bytes", maxProviderPromptBytes)
	}
	result := make([]byte, 0, len(prefix)+len(core))
	result = append(result, prefix...)
	return append(result, core...), nil
}

func providerPromptContractSHA256(spec RunSpec, core, effective []byte) (string, error) {
	if spec.SkillActivationIdentity() == "" {
		return "", nil
	}
	developerInstructions, err := codexPrivateCLIInstructions(spec)
	if err != nil {
		return "", err
	}
	return promptContractSHA256(spec.SkillActivationIdentity(), core, effective, developerInstructions)
}

func promptContractSHA256(skillActivation string, core, effective []byte, developerInstructions string) (string, error) {
	envelope := struct {
		SchemaVersion         int    `json:"schema_version"`
		SkillActivation       string `json:"skill_activation"`
		CorePrompt            []byte `json:"core_prompt"`
		EffectiveStdin        []byte `json:"effective_stdin"`
		DeveloperInstructions string `json:"developer_instructions"`
	}{
		SchemaVersion: 1, SkillActivation: skillActivation,
		CorePrompt: core, EffectiveStdin: effective, DeveloperInstructions: developerInstructions,
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode provider prompt contract: %w", err)
	}
	return sha256HexBytes(canonical), nil
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
		return nil, fmt.Errorf("codex confined cli has invalid broker directories")
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

func claudeMCPToolNamesForServer(server string, values []string) []string {
	qualified := make([]string, len(values))
	for i, value := range values {
		qualified[i] = "mcp__" + server + "__" + value
	}
	return qualified
}

func mcpServerName(spec RunSpec) string {
	if spec.EffectiveSurface() == SurfaceExternalMCP {
		return externalMCPServerName
	}
	return "atl"
}

func codexDenyNonMCPHook(guardPath string, spec RunSpec, confinement ProviderConfinement) (string, error) {
	command := guardPath
	if spec.EffectiveBackendMode() == BackendModePrivateLive || spec.EffectiveBackendMode() == BackendModeProviderCalibration ||
		isCodexSyntheticBrokerCLI(spec) {
		expectedMode := "mcp-with-skill-read"
		expectedTools := claudeMCPToolNamesForServer(mcpServerName(spec), spec.AllowedMCPTools)
		if spec.EffectiveToolTransport() == "cli" {
			expectedMode = "private-cli"
			expectedTools = nil
			if spec.EffectiveBackendMode() == BackendModeProviderCalibration {
				expectedMode = "provider-calibration"
			}
		}
		var err error
		command, err = codexPrivateHookCommand(guardPath, expectedMode, expectedTools, confinement)
		if err != nil {
			return "", err
		}
	}
	return `hooks.PreToolUse=[{matcher="^(Bash|apply_patch|Edit|Write|Read|Agent)$",hooks=[{type="command",command=` + strconv.Quote(command) + `,timeout=5}]}]`, nil
}

func codexPrivateHookCommand(guardPath, expectedMode string, expectedTools []string, confinement ProviderConfinement) (string, error) {
	if err := validateCodexPrivateHookPolicy(expectedMode, expectedTools, confinement); err != nil {
		return "", err
	}
	roots, err := json.Marshal(confinement.AllowedReadRoots)
	if err != nil {
		return "", fmt.Errorf("encode codex private-live hook read policy: %w", err)
	}
	skillRoots, err := json.Marshal(confinement.SkillReadRoots)
	if err != nil {
		return "", fmt.Errorf("encode codex private-live hook skill read policy: %w", err)
	}
	tools, err := json.Marshal(confinement.AllowedMCPTools)
	if err != nil {
		return "", fmt.Errorf("encode codex private-live hook tool policy: %w", err)
	}
	return "ATL_EVAL_GUARD_MODE=" + shellSingleQuote(confinement.GuardMode) +
		" ATL_EVAL_GUARD_COUNTER=" + shellSingleQuote(confinement.GuardCounterPath) +
		" ATL_EVAL_ALLOWED_MCP_TOOLS=" + shellSingleQuote(string(tools)) +
		" ATL_EVAL_WORKSPACE_ROOT=" + shellSingleQuote(confinement.WorkspaceReadRoot) +
		" ATL_EVAL_ALLOWED_READ_ROOTS=" + shellSingleQuote(string(roots)) +
		" ATL_EVAL_SKILL_READ_ROOTS=" + shellSingleQuote(string(skillRoots)) +
		" " + shellSingleQuote(guardPath), nil
}

func validateCodexPrivateHookPolicy(expectedMode string, expectedTools []string, confinement ProviderConfinement) error {
	if confinement.GuardMode != expectedMode || !equalStrings(confinement.AllowedMCPTools, expectedTools) || !validCodexHookPath(confinement.GuardCounterPath) {
		return fmt.Errorf("codex private-live hook requires an explicit guard policy")
	}
	workspace := confinement.WorkspaceReadRoot
	if !validCodexHookReadRoot(workspace) || len(confinement.AllowedReadRoots) == 0 || len(confinement.AllowedReadRoots) > 2 {
		return fmt.Errorf("codex private-live hook requires an explicit workspace read policy")
	}
	seen := map[string]struct{}{}
	workspaceAllowed := false
	for _, root := range confinement.AllowedReadRoots {
		if !validCodexHookReadRoot(root) {
			return fmt.Errorf("codex private-live hook read policy contains an invalid root")
		}
		if _, exists := seen[root]; exists {
			return fmt.Errorf("codex private-live hook read policy contains duplicate roots")
		}
		seen[root] = struct{}{}
		workspaceAllowed = workspaceAllowed || root == workspace
	}
	if !workspaceAllowed {
		return fmt.Errorf("codex private-live hook workspace is outside its read roots")
	}
	if len(confinement.SkillReadRoots) != 1 || !validCodexHookReadRoot(confinement.SkillReadRoots[0]) {
		return fmt.Errorf("codex private-live hook requires one explicit skill read root")
	}
	skillRootAllowed := false
	for _, root := range confinement.AllowedReadRoots {
		relative, err := filepath.Rel(root, confinement.SkillReadRoots[0])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			skillRootAllowed = true
			break
		}
	}
	if !skillRootAllowed {
		return fmt.Errorf("codex private-live hook skill root is outside its read roots")
	}
	return nil
}

func validCodexHookReadRoot(value string) bool {
	return validCodexHookPath(value) && filepath.Clean(value) != string(filepath.Separator)
}

func validCodexHookPath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n")
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
	metrics := ProviderMetrics{Coverage: map[string]bool{}, CapabilityFamilyCoverage: true, SkillToolCallsByName: map[string]int{}}
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
			counts := countClaudeToolCalls(event)
			metrics.ToolCalls += counts.Total
			metrics.SkillToolCalls += counts.Skill
			metrics.Delegations += counts.Delegations
			for name, count := range counts.SkillNames {
				metrics.SkillToolCallsByName[name] += count
			}
			for id, family := range counts.MCPIDs {
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
				if kind == "command_execution" {
					metrics.CommandExecutions++
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

type claudeToolCallCounts struct {
	Total       int
	Skill       int
	Delegations int
	SkillNames  map[string]int
	MCPIDs      map[string]string
}

func countClaudeToolCalls(event map[string]any) claudeToolCallCounts {
	message, _ := event["message"].(map[string]any)
	content, _ := message["content"].([]any)
	counts := claudeToolCallCounts{SkillNames: map[string]int{}, MCPIDs: map[string]string{}}
	for _, value := range content {
		block, _ := value.(map[string]any)
		if block["type"] == "tool_use" {
			counts.Total++
			name, _ := block["name"].(string)
			if name == "Skill" {
				counts.Skill++
				input, _ := block["input"].(map[string]any)
				skillName, _ := input["skill"].(string)
				if skillNameRE.MatchString(skillName) {
					counts.SkillNames[skillName]++
				}
			}
			if name == "Agent" || name == "Task" {
				counts.Delegations++
			}
			if strings.HasPrefix(name, "mcp__") {
				if id, _ := block["id"].(string); id != "" {
					family := ""
					if strings.HasPrefix(name, "mcp__atl__") {
						family, _ = CapabilityFamilyForMCP(strings.TrimPrefix(name, "mcp__atl__"))
					}
					counts.MCPIDs[id] = family
				}
			}
		}
	}
	return counts
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
		// responses carry an object; current Claude releases may wrap a
		// classified server error as "Error: {<atl envelope>}". Unknown shapes
		// fail closed so a provider change cannot silently undercount.
		switch result := event["tool_use_result"].(type) {
		case map[string]any:
		case string:
			if strings.HasPrefix(result, "Error: No such tool available:") {
				continue
			}
			if !isClaudeMCPServerError(result) {
				return 0, 0, 0, nil, false, fmt.Errorf("claude MCP result has an unsupported client-side shape")
			}
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

func isClaudeMCPServerError(value string) bool {
	raw := strings.TrimPrefix(value, "Error: ")
	if raw == value {
		return false
	}
	var envelope struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal([]byte(raw), &envelope) == nil && envelope.Kind != ""
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
