package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	RunSpecSchemaVersion = 2
	maxRunSpecBytes      = 1 << 20
	maxRunCostMicroUSD   = 10_000_000
)

var mcpToolNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// RunSpec is intentionally separate from Scenario: scenarios define comparable
// budgets, while run specs define one provider invocation and may remain local.
type RunSpec struct {
	SchemaVersion            int                           `json:"schema_version"`
	BackendMode              string                        `json:"backend_mode,omitempty"`
	ScenarioFile             string                        `json:"scenario_file"`
	Provider                 string                        `json:"provider"`
	Variant                  string                        `json:"variant"`
	Model                    string                        `json:"model"`
	Reasoning                string                        `json:"reasoning,omitempty"`
	PromptFile               string                        `json:"prompt_file"`
	ResponseSchemaFile       string                        `json:"response_schema_file"`
	QualitativeRubricFile    string                        `json:"qualitative_rubric_file"`
	WorkspaceTemplate        string                        `json:"workspace_template"`
	FixtureFile              string                        `json:"fixture_file"`
	Repetitions              int                           `json:"repetitions"`
	TimeoutSeconds           int                           `json:"timeout_seconds"`
	MaxEstimatedCostMicroUSD int64                         `json:"max_estimated_cost_microusd"`
	Pricing                  Pricing                       `json:"pricing"`
	ToolTransport            string                        `json:"tool_transport,omitempty"`
	AllowedTools             []string                      `json:"allowed_tools"`
	AllowedATLCommands       []string                      `json:"allowed_atl_commands"`
	AllowedCLICommands       []CLICommandRule              `json:"allowed_cli_commands,omitempty"`
	AllowedMCPTools          []string                      `json:"allowed_mcp_tools,omitempty"`
	AllowedGatewayRoutes     map[string][]LiveGatewayRoute `json:"allowed_gateway_routes,omitempty"`
	GatewayMaxResponseBytes  int64                         `json:"gateway_max_response_bytes,omitempty"`
	GatewayMaxTotalBytes     int64                         `json:"gateway_max_total_response_bytes,omitempty"`
	Checks                   []RunCheck                    `json:"checks"`
}

const (
	BackendModeSynthetic   = "synthetic"
	BackendModePrivateLive = "private-live"
)

func (s RunSpec) EffectiveBackendMode() string {
	if s.BackendMode == "" {
		return BackendModeSynthetic
	}
	return s.BackendMode
}

type Pricing struct {
	InputMicroUSDPerMillionTokens  int64 `json:"input_microusd_per_million_tokens"`
	OutputMicroUSDPerMillionTokens int64 `json:"output_microusd_per_million_tokens"`
}

type RunCheck struct {
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Pointer  string          `json:"pointer,omitempty"`
	Expected json.RawMessage `json:"expected,omitempty"`
	Minimum  int             `json:"minimum,omitempty"`
}

func DecodeRunSpec(r io.Reader) (RunSpec, error) {
	var spec RunSpec
	limited := &io.LimitedReader{R: r, N: maxRunSpecBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return RunSpec{}, fmt.Errorf("decode run spec: %w", err)
	}
	if limited.N <= 0 {
		return RunSpec{}, fmt.Errorf("run spec exceeds %d bytes", maxRunSpecBytes)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return RunSpec{}, fmt.Errorf("run spec contains trailing JSON data")
	}
	if err := spec.Validate(); err != nil {
		return RunSpec{}, err
	}
	return spec, nil
}

func (s RunSpec) Validate() error {
	if s.SchemaVersion != RunSpecSchemaVersion {
		return fmt.Errorf("unsupported run spec schema_version %d", s.SchemaVersion)
	}
	if s.Provider != "claude-code" && s.Provider != "codex" {
		return fmt.Errorf("provider must be claude-code or codex")
	}
	if !identifierRE.MatchString(s.Variant) {
		return fmt.Errorf("invalid run variant %q", s.Variant)
	}
	if strings.TrimSpace(s.Model) == "" || len(s.Model) > 256 || strings.ContainsAny(s.Model, "\r\n\x00") {
		return fmt.Errorf("model must contain 1..256 safe bytes")
	}
	if len(s.Reasoning) > 64 || strings.ContainsAny(s.Reasoning, "\r\n\x00") {
		return fmt.Errorf("reasoning is invalid")
	}
	for name, value := range map[string]string{
		"scenario_file": s.ScenarioFile, "prompt_file": s.PromptFile,
		"response_schema_file":    s.ResponseSchemaFile,
		"qualitative_rubric_file": s.QualitativeRubricFile,
		"workspace_template":      s.WorkspaceTemplate,
	} {
		if value == "" || filepath.IsAbs(value) || escapesBase(value) {
			return fmt.Errorf("%s must be a relative contained path", name)
		}
	}
	switch s.EffectiveBackendMode() {
	case BackendModeSynthetic:
		if s.FixtureFile == "" || filepath.IsAbs(s.FixtureFile) || escapesBase(s.FixtureFile) {
			return fmt.Errorf("fixture_file must be a relative contained path for synthetic runs")
		}
	case BackendModePrivateLive:
		if s.FixtureFile != "" {
			return fmt.Errorf("fixture_file must be empty for private-live runs")
		}
		if s.Repetitions != 1 {
			return fmt.Errorf("private-live runs require exactly one repetition")
		}
		if s.ToolTransport != "mcp" && s.ToolTransport != "cli" {
			return fmt.Errorf("private-live runs require an explicit cli or mcp tool_transport")
		}
	default:
		return fmt.Errorf("backend_mode must be synthetic or private-live")
	}
	if s.Repetitions < 1 || s.Repetitions > 20 {
		return fmt.Errorf("repetitions must be in 1..20")
	}
	if s.TimeoutSeconds < 1 || s.TimeoutSeconds > 3600 {
		return fmt.Errorf("timeout_seconds must be in 1..3600")
	}
	if s.MaxEstimatedCostMicroUSD < int64(s.Repetitions) || s.MaxEstimatedCostMicroUSD > maxRunCostMicroUSD {
		return fmt.Errorf("max_estimated_cost_microusd must cover every repetition and not exceed %d", maxRunCostMicroUSD)
	}
	if s.Pricing.InputMicroUSDPerMillionTokens < 0 || s.Pricing.OutputMicroUSDPerMillionTokens < 0 {
		return fmt.Errorf("pricing values must be non-negative")
	}
	if s.Provider == "codex" && (s.Pricing.InputMicroUSDPerMillionTokens == 0 || s.Pricing.OutputMicroUSDPerMillionTokens == 0) {
		return fmt.Errorf("codex runs require explicit input and output pricing")
	}
	transport := s.ToolTransport
	if transport == "" {
		transport = "cli"
	}
	if transport != "cli" && transport != "mcp" {
		return fmt.Errorf("tool_transport must be cli or mcp")
	}
	if transport == "cli" && (len(s.AllowedTools) == 0 || len(s.AllowedTools) > 32) {
		return fmt.Errorf("allowed_tools must contain 1..32 entries for cli transport")
	}
	if transport == "mcp" && len(s.AllowedTools) != 0 {
		return fmt.Errorf("allowed_tools must be empty for mcp transport")
	}
	seenTools := map[string]struct{}{}
	for _, tool := range s.AllowedTools {
		if strings.TrimSpace(tool) == "" || len(tool) > 128 || strings.ContainsAny(tool, "\r\n\x00") {
			return fmt.Errorf("invalid allowed tool")
		}
		if _, ok := seenTools[tool]; ok {
			return fmt.Errorf("duplicate allowed tool %q", tool)
		}
		seenTools[tool] = struct{}{}
	}
	if transport == "cli" {
		switch s.EffectiveBackendMode() {
		case BackendModeSynthetic:
			if len(s.AllowedATLCommands) == 0 || len(s.AllowedATLCommands) > 32 {
				return fmt.Errorf("allowed_atl_commands must contain 1..32 entries for synthetic cli transport")
			}
			if len(s.AllowedCLICommands) != 0 {
				return fmt.Errorf("allowed_cli_commands must be empty for synthetic cli transport")
			}
		case BackendModePrivateLive:
			if len(s.AllowedATLCommands) != 0 {
				return fmt.Errorf("private-live cli transport forbids prefix-based allowed_atl_commands")
			}
			if err := (CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: s.AllowedCLICommands}).Validate(); err != nil {
				return fmt.Errorf("allowed_cli_commands: %w", err)
			}
			allowedPrivateTools := map[string]bool{"Bash(atl *)": true, "Read": true, "Skill": true}
			for _, tool := range s.AllowedTools {
				if !allowedPrivateTools[tool] {
					return fmt.Errorf("private-live cli transport has unsupported allowed tool %q", tool)
				}
			}
			if !containsRunString(s.AllowedTools, "Bash(atl *)") {
				return fmt.Errorf("private-live cli transport requires Bash(atl *)")
			}
			if err := validateLiveGatewayRoutePolicy(s.AllowedGatewayRoutes); err != nil {
				return fmt.Errorf("allowed_gateway_routes: %w", err)
			}
			if s.GatewayMaxResponseBytes < 1 || s.GatewayMaxResponseBytes > 64<<20 || s.GatewayMaxTotalBytes < s.GatewayMaxResponseBytes || s.GatewayMaxTotalBytes > 256<<20 {
				return fmt.Errorf("private-live cli gateway response budgets are invalid")
			}
		}
	}
	if transport == "mcp" && (len(s.AllowedATLCommands) != 0 || len(s.AllowedCLICommands) != 0) {
		return fmt.Errorf("CLI command allowlists must be empty for mcp transport")
	}
	seenCommands := map[string]struct{}{}
	for _, command := range s.AllowedATLCommands {
		if command != strings.TrimSpace(command) || !strings.HasPrefix(command, "atl ") || len(command) > 256 || strings.ContainsAny(command, "\r\n;&|`><") || strings.Contains(command, "$(") {
			return fmt.Errorf("invalid allowed atl command prefix")
		}
		if _, ok := seenCommands[command]; ok {
			return fmt.Errorf("duplicate allowed atl command prefix %q", command)
		}
		seenCommands[command] = struct{}{}
	}
	if transport == "mcp" && (len(s.AllowedMCPTools) == 0 || len(s.AllowedMCPTools) > 16) {
		return fmt.Errorf("allowed_mcp_tools must contain 1..16 entries for mcp transport")
	}
	if transport == "cli" && len(s.AllowedMCPTools) != 0 {
		return fmt.Errorf("allowed_mcp_tools must be empty for cli transport")
	}
	if (s.EffectiveBackendMode() != BackendModePrivateLive || transport != "cli") && (len(s.AllowedGatewayRoutes) != 0 || s.GatewayMaxResponseBytes != 0 || s.GatewayMaxTotalBytes != 0) {
		return fmt.Errorf("gateway policy is only valid for private-live cli transport")
	}
	seenMCPTools := map[string]struct{}{}
	for _, tool := range s.AllowedMCPTools {
		if tool != strings.TrimSpace(tool) || !mcpToolNameRE.MatchString(tool) || len(tool) > 128 {
			return fmt.Errorf("invalid allowed MCP tool %q", tool)
		}
		if _, ok := seenMCPTools[tool]; ok {
			return fmt.Errorf("duplicate allowed MCP tool %q", tool)
		}
		seenMCPTools[tool] = struct{}{}
	}
	if len(s.Checks) == 0 || len(s.Checks) > maxContractListEntries {
		return fmt.Errorf("checks must contain 1..%d entries", maxContractListEntries)
	}
	seenChecks := map[string]struct{}{}
	for _, check := range s.Checks {
		if !identifierRE.MatchString(check.Name) {
			return fmt.Errorf("invalid run check name %q", check.Name)
		}
		if _, ok := seenChecks[check.Name]; ok {
			return fmt.Errorf("duplicate run check %q", check.Name)
		}
		seenChecks[check.Name] = struct{}{}
		switch check.Kind {
		case "json_equals":
			if check.Pointer == "" || !json.Valid(check.Expected) {
				return fmt.Errorf("json_equals check %q requires pointer and valid expected JSON", check.Name)
			}
		case "json_present":
			if check.Pointer == "" || len(check.Expected) != 0 {
				return fmt.Errorf("json_present check %q requires only pointer", check.Name)
			}
		case "atl_invocations_min":
			if check.Minimum < 1 || check.Pointer != "" || len(check.Expected) != 0 {
				return fmt.Errorf("atl_invocations_min check %q is invalid", check.Name)
			}
		case "atl_all_succeeded":
			if check.Minimum != 0 || check.Pointer != "" || len(check.Expected) != 0 {
				return fmt.Errorf("atl_all_succeeded check %q is invalid", check.Name)
			}
		case "mock_no_unexpected":
			if check.Minimum != 0 || check.Pointer != "" || len(check.Expected) != 0 {
				return fmt.Errorf("mock_no_unexpected check %q is invalid", check.Name)
			}
		case "delegations_min":
			if check.Minimum < 1 || check.Pointer != "" || len(check.Expected) != 0 {
				return fmt.Errorf("delegations_min check %q is invalid", check.Name)
			}
		case "guard_no_denials":
			if check.Minimum != 0 || check.Pointer != "" || len(check.Expected) != 0 {
				return fmt.Errorf("guard_no_denials check %q is invalid", check.Name)
			}
		case "delegations_none":
			if check.Minimum != 0 || check.Pointer != "" || len(check.Expected) != 0 {
				return fmt.Errorf("delegations_none check %q is invalid", check.Name)
			}
		case "http_methods_observed":
			if check.Minimum != 0 || check.Pointer != "" || len(check.Expected) != 0 {
				return fmt.Errorf("http_methods_observed check %q is invalid", check.Name)
			}
		default:
			return fmt.Errorf("unsupported run check kind %q", check.Kind)
		}
	}
	return nil
}

func containsRunString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func (s RunSpec) ValidateAgainstScenario(scenario Scenario) error {
	if err := scenario.Validate(); err != nil {
		return err
	}
	if s.EffectiveBackendMode() == BackendModePrivateLive {
		if scenario.DataClass != "private-local" {
			return fmt.Errorf("private-live runs require scenario data_class=private-local")
		}
		if scenario.Budgets.MaxRemoteWrites != 0 {
			return fmt.Errorf("private-live runs require max_remote_writes=0")
		}
		if scenario.Budgets.MaxDelegations != 0 {
			return fmt.Errorf("private-live runs do not allow delegation")
		}
		if scenario.Budgets.MaxBackendRequests < 1 || scenario.Budgets.MaxATLInvocations < 1 {
			return fmt.Errorf("private-live runs require positive backend and atl invocation budgets")
		}
		if len(scenario.Budgets.AllowedHTTPMethods) == 0 {
			return fmt.Errorf("private-live runs require an explicit GET/HEAD method allowlist")
		}
		for _, method := range scenario.Budgets.AllowedHTTPMethods {
			if method != "GET" && method != "HEAD" {
				return fmt.Errorf("private-live allowed_http_methods may contain only GET and HEAD")
			}
		}
		requiredKinds := map[string]bool{
			"atl_all_succeeded":     false,
			"atl_invocations_min":   false,
			"http_methods_observed": false,
			"guard_no_denials":      false,
			"delegations_none":      false,
		}
		for _, check := range s.Checks {
			if check.Kind == "mock_no_unexpected" {
				return fmt.Errorf("private-live runs cannot use mock_no_unexpected")
			}
			if _, ok := requiredKinds[check.Kind]; ok {
				requiredKinds[check.Kind] = true
			}
		}
		for kind, present := range requiredKinds {
			if !present {
				return fmt.Errorf("private-live runs require a %s check", kind)
			}
		}
	}
	perRunCap := (s.MaxEstimatedCostMicroUSD + int64(s.Repetitions) - 1) / int64(s.Repetitions)
	if perRunCap > scenario.Budgets.MaxEstimatedCostMicroUSD {
		return fmt.Errorf("per-repetition run cost cap exceeds scenario budget")
	}
	defined := make(map[string]struct{}, len(s.Checks))
	for _, check := range s.Checks {
		defined[check.Name] = struct{}{}
	}
	for _, check := range scenario.RequiredChecks {
		if _, ok := defined[check]; !ok {
			return fmt.Errorf("required scenario check %q has no run oracle", check)
		}
	}
	return nil
}

func escapesBase(path string) bool {
	clean := filepath.Clean(path)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func evaluateRunChecks(checks []RunCheck, final []byte, atlInvocations, failedATL, unexpectedRequests, delegations, guardDenials int, httpMethodsObserved bool) (map[string]bool, error) {
	var document any
	if err := json.Unmarshal(final, &document); err != nil {
		return nil, fmt.Errorf("decode structured final response: %w", err)
	}
	results := make(map[string]bool, len(checks))
	for _, check := range checks {
		switch check.Kind {
		case "atl_invocations_min":
			results[check.Name] = atlInvocations >= check.Minimum
		case "atl_all_succeeded":
			results[check.Name] = failedATL == 0
		case "mock_no_unexpected":
			results[check.Name] = unexpectedRequests == 0
		case "delegations_min":
			results[check.Name] = delegations >= check.Minimum
		case "guard_no_denials":
			results[check.Name] = guardDenials == 0
		case "delegations_none":
			results[check.Name] = delegations == 0
		case "http_methods_observed":
			results[check.Name] = httpMethodsObserved
		case "json_present":
			_, ok := resolveJSONPointer(document, check.Pointer)
			results[check.Name] = ok
		case "json_equals":
			actual, ok := resolveJSONPointer(document, check.Pointer)
			if !ok {
				results[check.Name] = false
				continue
			}
			var expected any
			if err := json.Unmarshal(check.Expected, &expected); err != nil {
				return nil, err
			}
			actualJSON, _ := json.Marshal(actual)
			expectedJSON, _ := json.Marshal(expected)
			results[check.Name] = bytes.Equal(actualJSON, expectedJSON)
		}
	}
	return results, nil
}

func resolveJSONPointer(document any, pointer string) (any, bool) {
	if pointer == "" {
		return document, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := document
	for _, raw := range strings.Split(pointer[1:], "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
