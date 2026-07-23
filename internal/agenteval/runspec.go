package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	RunSpecSchemaVersion              = 7
	LegacyRunSpecSchemaVersion        = 6
	LegacyPromptChannelRunSpecVersion = 5
	maxRunSpecBytes                   = 1 << 20
	maxRunCostMicroUSD                = 10_000_000
	maxWorkspaceArtifactBytes         = 16 << 20
)

var (
	mcpToolNameRE        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	skillNameRE          = regexp.MustCompile(`^[a-z0-9][a-z0-9:._-]{0,127}$`)
	neutralATLRouteRE    = regexp.MustCompile(`(?m)(^|[^a-z0-9_-])atl[ \t]+(jira|conf|capabilities|config)([^a-z0-9_-]|$)`)
	neutralTypedToolRE   = regexp.MustCompile(`(^|[^a-z0-9_])(jira|confluence)_[a-z0-9]+(_[a-z0-9]+)*([^a-z0-9_]|$)`)
	workspaceSHA256HexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// RunSpec is intentionally separate from Scenario: scenarios define comparable
// budgets, while run specs define one provider invocation and may remain local.
type RunSpec struct {
	SchemaVersion               int                           `json:"schema_version"`
	BackendMode                 string                        `json:"backend_mode,omitempty"`
	Category                    string                        `json:"category,omitempty"`
	Surface                     string                        `json:"surface,omitempty"`
	ScenarioFile                string                        `json:"scenario_file"`
	Provider                    string                        `json:"provider"`
	Variant                     string                        `json:"variant"`
	Model                       string                        `json:"model"`
	Reasoning                   string                        `json:"reasoning,omitempty"`
	PromptFile                  string                        `json:"prompt_file"`
	ResponseSchemaFile          string                        `json:"response_schema_file"`
	QualitativeRubricFile       string                        `json:"qualitative_rubric_file"`
	WorkspaceTemplate           string                        `json:"workspace_template"`
	FixtureFile                 string                        `json:"fixture_file"`
	Repetitions                 int                           `json:"repetitions"`
	TimeoutSeconds              int                           `json:"timeout_seconds"`
	MaxEstimatedCostMicroUSD    int64                         `json:"max_estimated_cost_microusd"`
	Pricing                     Pricing                       `json:"pricing"`
	ToolTransport               string                        `json:"tool_transport,omitempty"`
	SkillActivation             string                        `json:"skill_activation,omitempty"`
	AllowedTools                []string                      `json:"allowed_tools"`
	AllowedATLCommands          []string                      `json:"allowed_atl_commands"`
	AllowedCLICommands          []CLICommandRule              `json:"allowed_cli_commands,omitempty"`
	AllowedMCPTools             []string                      `json:"allowed_mcp_tools,omitempty"`
	DataCapabilities            []string                      `json:"data_capabilities,omitempty"`
	AllowedGatewayRoutes        map[string][]LiveGatewayRoute `json:"allowed_gateway_routes,omitempty"`
	GatewayMaxResponseBytes     int64                         `json:"gateway_max_response_bytes,omitempty"`
	GatewayMaxTotalBytes        int64                         `json:"gateway_max_total_response_bytes,omitempty"`
	GatewayMaxRequestBytes      int64                         `json:"gateway_max_request_bytes,omitempty"`
	GatewayMaxTotalRequestBytes int64                         `json:"gateway_max_total_request_bytes,omitempty"`
	AllowSyntheticWrites        bool                          `json:"allow_synthetic_writes,omitempty"`
	AllowLiveWrites             bool                          `json:"allow_live_writes,omitempty"`
	Checks                      []RunCheck                    `json:"checks"`
	mcpServerURL                string
	mcpBearerTokenEnv           string
}

const (
	BackendModeSynthetic   = "synthetic"
	BackendModePrivateLive = "private-live"
	// BackendModeProviderCalibration is an internal, backend-free Codex CLI
	// transport check. It is never a benchmark treatment and is not accepted by
	// RunHeadless; the calibration runner constructs its fixed contract in code.
	BackendModeProviderCalibration = "provider-calibration"
	SkillActivationImplicit        = "implicit"
	SkillActivationExplicit        = "explicit"
	SkillActivationDeveloper       = "developer"
	SkillActivationCombined        = "combined"
)

type skillActivationDescriptor struct {
	promptPrefix           bool
	developerReinforcement bool
}

type capabilityFamilyExpectation struct {
	Family      string `json:"family"`
	Invocations int    `json:"invocations"`
	Successes   int    `json:"successes"`
	Failures    int    `json:"failures"`
}

func describeSkillActivation(value string) (skillActivationDescriptor, error) {
	switch value {
	case "", SkillActivationImplicit:
		return skillActivationDescriptor{}, nil
	case SkillActivationExplicit:
		return skillActivationDescriptor{promptPrefix: true}, nil
	case SkillActivationDeveloper:
		return skillActivationDescriptor{developerReinforcement: true}, nil
	case SkillActivationCombined:
		return skillActivationDescriptor{promptPrefix: true, developerReinforcement: true}, nil
	default:
		return skillActivationDescriptor{}, fmt.Errorf("skill_activation must be implicit, explicit, developer, or combined")
	}
}

func (d skillActivationDescriptor) hintsServiceSkill() bool {
	return d.promptPrefix || d.developerReinforcement
}

func (d skillActivationDescriptor) serviceSkill(capabilities []string) (string, error) {
	if !d.hintsServiceSkill() {
		return "", nil
	}
	return explicitServiceSkill(capabilities)
}

func (s RunSpec) EffectiveBackendMode() string {
	if s.BackendMode == "" {
		return BackendModeSynthetic
	}
	return s.BackendMode
}

func (s RunSpec) EffectiveCategory() string {
	if s.Category == "" {
		return BenchmarkCategoryRouteFixed
	}
	return s.Category
}

func (s RunSpec) EffectiveSurface() string {
	if s.Surface != "" {
		return s.Surface
	}
	if s.EffectiveToolTransport() == "mcp" {
		return SurfaceATLMCP
	}
	return SurfaceCLISkill
}

func (s RunSpec) EffectiveToolTransport() string {
	if s.ToolTransport == "" {
		return "cli"
	}
	return s.ToolTransport
}

// SkillActivationIdentity returns a value only on the surface where activation
// is an experimental treatment. MCP and Claude runs are not labeled as an
// implicit skill arm because they do not use the Codex prompt-routing contract.
func (s RunSpec) SkillActivationIdentity() string {
	if s.Provider != "codex" || s.EffectiveBackendMode() != BackendModePrivateLive ||
		s.EffectiveSurface() != SurfaceCLISkill || s.EffectiveToolTransport() != "cli" {
		return ""
	}
	return s.SkillActivation
}

func validateSkillActivation(s RunSpec) error {
	descriptor, err := describeSkillActivation(s.SkillActivation)
	if err != nil {
		return err
	}
	eligible := s.Provider == "codex" && s.EffectiveBackendMode() == BackendModePrivateLive &&
		s.EffectiveSurface() == SurfaceCLISkill && s.EffectiveToolTransport() == "cli"
	if eligible && s.SkillActivation == "" {
		return fmt.Errorf("codex private-live cli-skill runs require skill_activation")
	}
	if s.SkillActivation != "" && !eligible {
		return fmt.Errorf("skill_activation is valid only for codex private-live cli-skill runs")
	}
	if _, err := descriptor.serviceSkill(s.DataCapabilities); err != nil {
		return err
	}
	return nil
}

func explicitServiceSkill(capabilities []string) (string, error) {
	service := ""
	for _, capability := range capabilities {
		family := strings.ToLower(strings.TrimSpace(capability))
		candidate := ""
		switch {
		case family == "jira" || strings.HasPrefix(family, "jira."):
			candidate = "jira"
		case family == "confluence" || strings.HasPrefix(family, "confluence."):
			candidate = "confluence"
		default:
			return "", fmt.Errorf("hinted skill_activation requires jira-only or confluence-only data_capabilities")
		}
		if service != "" && service != candidate {
			return "", fmt.Errorf("hinted skill_activation does not support mixed data_capabilities")
		}
		service = candidate
	}
	if service == "" {
		return "", fmt.Errorf("hinted skill_activation requires jira-only or confluence-only data_capabilities")
	}
	return "atl:" + service, nil
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
	Maximum  int             `json:"maximum,omitempty"`
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
	if s.SchemaVersion != RunSpecSchemaVersion && s.SchemaVersion != LegacyRunSpecSchemaVersion && s.SchemaVersion != LegacyPromptChannelRunSpecVersion {
		return fmt.Errorf("unsupported run spec schema_version %d", s.SchemaVersion)
	}
	if s.Provider != "claude-code" && s.Provider != "codex" {
		return fmt.Errorf("provider must be claude-code or codex")
	}
	if err := validatePathComponentID("run variant", s.Variant); err != nil {
		return err
	}
	if !validBenchmarkCategory(s.EffectiveCategory()) {
		return fmt.Errorf("invalid benchmark category %q", s.Category)
	}
	if !validRunSurface(s.EffectiveSurface()) {
		return fmt.Errorf("invalid benchmark surface %q", s.Surface)
	}
	if err := validateRunDataCapabilities(s); err != nil {
		return err
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
		if s.AllowSyntheticWrites {
			return fmt.Errorf("allow_synthetic_writes is valid only for synthetic runs")
		}
		if s.AllowLiveWrites && s.SchemaVersion != RunSpecSchemaVersion {
			return fmt.Errorf("allow_live_writes requires current run spec schema")
		}
		if s.FixtureFile != "" {
			return fmt.Errorf("fixture_file must be empty for private-live runs")
		}
		if s.Repetitions != 1 {
			return fmt.Errorf("private-live runs require exactly one repetition")
		}
		if s.ToolTransport != "mcp" && s.ToolTransport != "cli" {
			return fmt.Errorf("private-live runs require an explicit cli or mcp tool_transport")
		}
	case BackendModeProviderCalibration:
		if s.SchemaVersion != RunSpecSchemaVersion || s.Provider != "codex" || s.AllowSyntheticWrites || s.AllowLiveWrites || s.FixtureFile != "" || s.Repetitions != 1 || s.ToolTransport != "cli" {
			return fmt.Errorf("provider-calibration requires one read-only codex cli invocation without a fixture")
		}
		if s.SkillActivation != "" || len(s.DataCapabilities) != 0 {
			return fmt.Errorf("provider-calibration is not a skill-activation treatment")
		}
		if !isExactProviderCalibrationSpec(s) {
			return fmt.Errorf("provider-calibration requires the fixed atl_version command and response contract")
		}
	default:
		return fmt.Errorf("backend_mode must be synthetic, private-live, or provider-calibration")
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
	transport := s.EffectiveToolTransport()
	if transport != "cli" && transport != "mcp" {
		return fmt.Errorf("tool_transport must be cli or mcp")
	}
	switch s.EffectiveSurface() {
	case SurfaceCLISkill:
		if transport != "cli" {
			return fmt.Errorf("surface %s requires cli tool_transport", SurfaceCLISkill)
		}
	case SurfaceATLMCP, SurfaceExternalMCP:
		if transport != "mcp" {
			return fmt.Errorf("surface %s requires mcp tool_transport", s.EffectiveSurface())
		}
	}
	if s.EffectiveSurface() == SurfaceExternalMCP && s.EffectiveBackendMode() != BackendModePrivateLive {
		return fmt.Errorf("external-mcp surface is valid only for private-live runs")
	}
	if err := validateSkillActivation(s); err != nil {
		return err
	}
	if s.AllowSyntheticWrites && transport != "cli" {
		return fmt.Errorf("allow_synthetic_writes requires cli transport")
	}
	if s.AllowLiveWrites && (s.EffectiveBackendMode() != BackendModePrivateLive || transport != "cli") {
		return fmt.Errorf("allow_live_writes requires private-live cli transport")
	}
	if s.AllowSyntheticWrites && s.Provider == "codex" && !isCodexSyntheticBrokerCLI(s) {
		return fmt.Errorf("codex synthetic writes require exact allowed_cli_commands")
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
			if isCodexSyntheticBrokerCLI(s) {
				if len(s.AllowedATLCommands) != 0 {
					return fmt.Errorf("brokered synthetic codex cli transport forbids prefix-based allowed_atl_commands")
				}
				if err := (CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: s.AllowedCLICommands}).Validate(); err != nil {
					return fmt.Errorf("allowed_cli_commands: %w", err)
				}
			} else {
				if len(s.AllowedATLCommands) == 0 || len(s.AllowedATLCommands) > 32 {
					return fmt.Errorf("allowed_atl_commands must contain 1..32 entries for synthetic cli transport")
				}
				if len(s.AllowedCLICommands) != 0 {
					return fmt.Errorf("allowed_cli_commands must be empty for ordinary synthetic cli transport")
				}
			}
		case BackendModePrivateLive, BackendModeProviderCalibration:
			if len(s.AllowedATLCommands) != 0 {
				return fmt.Errorf("confined cli transport forbids prefix-based allowed_atl_commands")
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
			if s.EffectiveBackendMode() == BackendModePrivateLive {
				if err := validateLiveGatewayRoutePolicy(s.AllowedGatewayRoutes); err != nil {
					return fmt.Errorf("allowed_gateway_routes: %w", err)
				}
				if s.GatewayMaxResponseBytes < 1 || s.GatewayMaxResponseBytes > 64<<20 || s.GatewayMaxTotalBytes < s.GatewayMaxResponseBytes || s.GatewayMaxTotalBytes > 256<<20 {
					return fmt.Errorf("private-live cli gateway response budgets are invalid")
				}
				if s.AllowLiveWrites {
					if s.GatewayMaxRequestBytes < 1 || s.GatewayMaxRequestBytes > 16<<20 || s.GatewayMaxTotalRequestBytes < s.GatewayMaxRequestBytes || s.GatewayMaxTotalRequestBytes > 64<<20 {
						return fmt.Errorf("private-live cli gateway request budgets are invalid")
					}
				} else if s.GatewayMaxRequestBytes != 0 || s.GatewayMaxTotalRequestBytes != 0 {
					return fmt.Errorf("read-only private-live runs forbid gateway request-body budgets")
				}
			} else if len(s.AllowedGatewayRoutes) != 0 || s.GatewayMaxResponseBytes != 0 || s.GatewayMaxTotalBytes != 0 || s.GatewayMaxRequestBytes != 0 || s.GatewayMaxTotalRequestBytes != 0 {
				return fmt.Errorf("provider-calibration forbids a backend gateway policy")
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
	if (s.EffectiveBackendMode() != BackendModePrivateLive || transport != "cli") && (len(s.AllowedGatewayRoutes) != 0 || s.GatewayMaxResponseBytes != 0 || s.GatewayMaxTotalBytes != 0 || s.GatewayMaxRequestBytes != 0 || s.GatewayMaxTotalRequestBytes != 0) {
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
	requiresSkillInvocation := false
	requiresNamedSkillInvocation := false
	for _, check := range s.Checks {
		if !identifierRE.MatchString(check.Name) {
			return fmt.Errorf("invalid run check name %q", check.Name)
		}
		if _, ok := seenChecks[check.Name]; ok {
			return fmt.Errorf("duplicate run check %q", check.Name)
		}
		seenChecks[check.Name] = struct{}{}
		if check.Kind != "atl_invocations_max" && check.Kind != "interface_invocations_max" && check.Maximum != 0 {
			return fmt.Errorf("run check %q does not accept maximum", check.Name)
		}
		switch check.Kind {
		case "json_equals":
			if check.Pointer == "" || !json.Valid(check.Expected) {
				return fmt.Errorf("json_equals check %q requires pointer and valid expected JSON", check.Name)
			}
		case "json_present":
			if check.Pointer == "" || len(check.Expected) != 0 {
				return fmt.Errorf("json_present check %q requires only pointer", check.Name)
			}
		case "json_equals_workspace_json":
			if check.Pointer == "" {
				return fmt.Errorf("json_equals_workspace_json check %q requires an output pointer", check.Name)
			}
			if _, ok := workspaceJSONExpectationFrom(check.Expected); !ok {
				return fmt.Errorf("json_equals_workspace_json check %q requires a contained JSON file and pointer", check.Name)
			}
		case "workspace_file_sha256":
			if check.Pointer != "" || check.Minimum != 0 {
				return fmt.Errorf("workspace_file_sha256 check %q does not accept pointer or minimum", check.Name)
			}
			if _, ok := workspaceFileSHA256ExpectationFrom(check.Expected); !ok {
				return fmt.Errorf("workspace_file_sha256 check %q requires a contained file and lowercase SHA-256", check.Name)
			}
		case "atl_invocations_min", "interface_invocations_min":
			if check.Minimum < 1 || check.Pointer != "" || len(check.Expected) != 0 {
				return fmt.Errorf("%s check %q is invalid", check.Kind, check.Name)
			}
		case "skill_invocations_min":
			target, ok := skillInvocationTarget(check.Expected)
			if check.Minimum < 1 || check.Pointer != "" || !ok {
				return fmt.Errorf("skill_invocations_min check %q is invalid", check.Name)
			}
			requiresSkillInvocation = true
			requiresNamedSkillInvocation = requiresNamedSkillInvocation || target != ""
		case "atl_invocations_max", "interface_invocations_max":
			if check.Maximum < 1 || check.Minimum != 0 || check.Pointer != "" || len(check.Expected) != 0 {
				return fmt.Errorf("%s check %q is invalid", check.Kind, check.Name)
			}
		case "atl_all_succeeded", "interface_all_succeeded":
			if check.Minimum != 0 || check.Pointer != "" || len(check.Expected) != 0 {
				return fmt.Errorf("%s check %q is invalid", check.Kind, check.Name)
			}
		case "atl_failures_equals", "interface_failures_equals":
			if _, ok := expectedATLFailureCount(check.Expected); check.Minimum != 0 || check.Pointer != "" || !ok {
				return fmt.Errorf("%s check %q requires a non-negative integer expected value", check.Kind, check.Name)
			}
		case "cli_exit_codes_equal":
			if _, ok := expectedCLIExitCodes(check.Expected); transport != "cli" || check.Minimum != 0 || check.Pointer != "" || !ok {
				return fmt.Errorf("cli_exit_codes_equal check %q requires CLI transport and an exact exit-code array", check.Name)
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
		case "http_methods_equal":
			if _, ok := expectedHTTPMethods(check.Expected); check.Minimum != 0 || check.Maximum != 0 || check.Pointer != "" || !ok {
				return fmt.Errorf("http_methods_equal check %q requires a bounded method-count object", check.Name)
			}
		case "capability_families_equal":
			if _, ok := expectedCapabilityFamilies(check.Expected); check.Minimum != 0 || check.Maximum != 0 || check.Pointer != "" || !ok {
				return fmt.Errorf("capability_families_equal check %q requires a sorted bounded exact family-count array", check.Name)
			}
		case "capability_sequence_equal":
			if _, ok := expectedCapabilitySequence(check.Expected); check.Minimum != 0 || check.Maximum != 0 || check.Pointer != "" || !ok {
				return fmt.Errorf("capability_sequence_equal check %q requires a bounded ordered family array", check.Name)
			}
		default:
			return fmt.Errorf("unsupported run check kind %q", check.Kind)
		}
	}
	if requiresSkillInvocation && (transport != "cli" || !containsRunString(s.AllowedTools, "Skill") || s.Provider != "claude-code" && !isCodexConfinedCLI(s)) {
		return fmt.Errorf("skill_invocations_min requires Claude Code or confined Codex CLI transport with Skill allowed")
	}
	if requiresNamedSkillInvocation && s.Provider != "claude-code" {
		return fmt.Errorf("named skill_invocations_min requires Claude Code Skill events")
	}
	return nil
}

// isCodexSyntheticBrokerCLI selects the executable synthetic CLI route without
// changing legacy prefix-based Codex specs, which remain validation/dry-run
// controls. Exact structured argv rules are the explicit opt-in to the
// zero-network host broker for both read-only and mutation scenarios.
func isCodexSyntheticBrokerCLI(s RunSpec) bool {
	return s.Provider == "codex" && s.EffectiveBackendMode() == BackendModeSynthetic &&
		s.EffectiveToolTransport() == "cli" && len(s.AllowedCLICommands) != 0
}

func isExactProviderCalibrationSpec(s RunSpec) bool {
	if len(s.AllowedTools) != 1 || s.AllowedTools[0] != "Bash(atl *)" || len(s.AllowedATLCommands) != 0 || len(s.AllowedCLICommands) != 1 || len(s.Checks) != 1 {
		return false
	}
	rule := s.AllowedCLICommands[0]
	check := s.Checks[0]
	return rule.Name == "atl_version" && equalStrings(rule.Command, []string{"version"}) && len(rule.Positionals) == 0 && len(rule.Flags) == 0 && rule.MaxInvocations == 1 &&
		check.Name == "calibration_response" && check.Kind == "json_present" && check.Pointer == "/version" && len(check.Expected) == 0 && check.Minimum == 0 && check.Maximum == 0
}

func containsRunString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateLiveWriteGatewayPolicy(services map[string][]LiveGatewayRoute, budgets Budgets) error {
	allowedMethods := make(map[string]struct{}, len(budgets.AllowedHTTPMethods))
	for _, method := range budgets.AllowedHTTPMethods {
		allowedMethods[method] = struct{}{}
	}
	totalRequests := 0
	totalWrites := 0
	for _, routes := range services {
		for _, route := range routes {
			if len(route.Methods) == 0 || route.MaxRequests < 1 {
				return fmt.Errorf("live-write gateway routes require explicit methods and request budgets")
			}
			mutating := routeHasMutatingMethod(route)
			for _, method := range route.Methods {
				if _, ok := allowedMethods[method]; !ok {
					return fmt.Errorf("live-write gateway method %s is outside the scenario allowlist", method)
				}
				if mutating && !isMutatingHTTPMethod(method) {
					return fmt.Errorf("live-write gateway routes cannot mix read and mutating methods")
				}
			}
			totalRequests += route.MaxRequests
			if mutating {
				totalWrites += route.MaxRequests
			}
		}
	}
	if totalRequests > budgets.MaxBackendRequests {
		return fmt.Errorf("live-write gateway route budgets exceed max_backend_requests")
	}
	if totalWrites != budgets.MaxRemoteWrites {
		return fmt.Errorf("live-write gateway mutation budgets must equal max_remote_writes")
	}
	return nil
}

func (s RunSpec) ValidateAgainstScenario(scenario Scenario) error {
	if err := scenario.Validate(); err != nil {
		return err
	}
	if s.EffectiveCategory() != scenario.EffectiveCategory() {
		return fmt.Errorf("run category %q does not match scenario category %q", s.EffectiveCategory(), scenario.EffectiveCategory())
	}
	if scenario.EffectiveCategory() == BenchmarkCategoryNeutralCommon {
		for _, check := range s.Checks {
			if strings.HasPrefix(check.Kind, "atl_") {
				return fmt.Errorf("neutral-common run checks must use generic interface aliases, found %q", check.Kind)
			}
		}
	}
	checksByName := make(map[string]RunCheck, len(s.Checks))
	for _, check := range s.Checks {
		checksByName[check.Name] = check
	}
	for _, name := range scenario.RequiredSemanticChecks {
		check, ok := checksByName[name]
		if !ok || runCheckClass(check.Kind) != "semantic" {
			return fmt.Errorf("required semantic check %q must exist with a semantic kind", name)
		}
	}
	if s.EffectiveBackendMode() == BackendModePrivateLive {
		if scenario.DataClass != "private-local" {
			return fmt.Errorf("private-live runs require scenario data_class=private-local")
		}
		if s.AllowLiveWrites {
			if scenario.Budgets.MaxRemoteWrites < 1 {
				return fmt.Errorf("live-write private runs require a positive max_remote_writes")
			}
			if err := validateLiveWriteGatewayPolicy(s.AllowedGatewayRoutes, scenario.Budgets); err != nil {
				return err
			}
		} else if scenario.Budgets.MaxRemoteWrites != 0 {
			return fmt.Errorf("read-only private-live runs require max_remote_writes=0")
		}
		if scenario.Budgets.MaxDelegations != 0 {
			return fmt.Errorf("private-live runs do not allow delegation")
		}
		if scenario.Budgets.MaxBackendRequests < 1 || scenario.Budgets.EffectiveMaxInterfaceInvocations() < 1 {
			return fmt.Errorf("private-live runs require positive backend and interface invocation budgets")
		}
		if len(scenario.Budgets.AllowedHTTPMethods) == 0 {
			return fmt.Errorf("private-live runs require an explicit GET/HEAD method allowlist")
		}
		if s.EffectiveSurface() == SurfaceExternalMCP {
			for _, metric := range scenario.RequiredMetrics {
				if externalMCPMetricIsOpaque(metric) {
					return fmt.Errorf("external MCP runs cannot require opaque backend metrics")
				}
			}
		}
		if !s.AllowLiveWrites {
			for _, routes := range s.AllowedGatewayRoutes {
				for _, route := range routes {
					if routeHasMutatingMethod(route) {
						return fmt.Errorf("read-only private-live gateway routes may contain only GET and HEAD")
					}
				}
			}
			for _, method := range scenario.Budgets.AllowedHTTPMethods {
				if method != "GET" && method != "HEAD" {
					return fmt.Errorf("read-only private-live allowed_http_methods may contain only GET and HEAD")
				}
			}
		}
		requiredSuccess := false
		requiredInvocation := false
		var expectedExitCodes []int
		expectedFailures := -1
		requiredKinds := map[string]bool{"guard_no_denials": false, "delegations_none": false}
		if s.EffectiveSurface() != SurfaceExternalMCP {
			requiredKinds["http_methods_observed"] = false
		}
		for _, check := range s.Checks {
			if check.Kind == "mock_no_unexpected" {
				return fmt.Errorf("private-live runs cannot use mock_no_unexpected")
			}
			if check.Kind == "json_equals_workspace_json" {
				return fmt.Errorf("private-live runs cannot inspect workspace JSON as an oracle")
			}
			if _, ok := requiredKinds[check.Kind]; ok {
				requiredKinds[check.Kind] = true
			}
			if check.Kind == "atl_all_succeeded" || check.Kind == "interface_all_succeeded" {
				requiredSuccess = true
			}
			if check.Kind == "cli_exit_codes_equal" {
				expectedExitCodes, _ = expectedCLIExitCodes(check.Expected)
			}
			if check.Kind == "atl_failures_equals" || check.Kind == "interface_failures_equals" {
				expectedFailures, _ = expectedATLFailureCount(check.Expected)
			}
			if check.Kind == "atl_invocations_min" || check.Kind == "interface_invocations_min" {
				requiredInvocation = true
			}
		}
		if !requiredSuccess {
			nonzero := 0
			for _, code := range expectedExitCodes {
				if code != 0 {
					nonzero++
				}
			}
			if len(expectedExitCodes) == 0 || nonzero == 0 || expectedFailures != nonzero {
				return fmt.Errorf("private-live negative paths require exact non-zero CLI exit codes and a matching failure count")
			}
		}
		if !requiredInvocation {
			return fmt.Errorf("private-live runs require an interface_invocations_min or atl_invocations_min check")
		}
		for kind, present := range requiredKinds {
			if !present {
				return fmt.Errorf("private-live runs require a %s check", kind)
			}
		}
		if s.AllowLiveWrites {
			hasExactMethods := false
			for _, check := range s.Checks {
				hasExactMethods = hasExactMethods || check.Kind == "http_methods_equal"
			}
			if !hasExactMethods {
				return fmt.Errorf("live-write private runs require a http_methods_equal check")
			}
		}
	}
	if s.AllowSyntheticWrites {
		if scenario.DataClass != "synthetic" || scenario.Budgets.MaxRemoteWrites < 1 {
			return fmt.Errorf("allow_synthetic_writes requires a synthetic scenario with a positive remote-write budget")
		}
		requiredKinds := map[string]bool{
			"guard_no_denials":   false,
			"http_methods_equal": false,
			"mock_no_unexpected": false,
		}
		for _, check := range s.Checks {
			if _, ok := requiredKinds[check.Kind]; ok {
				requiredKinds[check.Kind] = true
			}
		}
		for kind, present := range requiredKinds {
			if !present {
				return fmt.Errorf("allow_synthetic_writes requires a %s check", kind)
			}
		}
		mutatingMethod := false
		for _, method := range scenario.Budgets.AllowedHTTPMethods {
			if method != "GET" && method != "HEAD" && method != "OPTIONS" {
				mutatingMethod = true
				break
			}
		}
		if !mutatingMethod {
			return fmt.Errorf("allow_synthetic_writes requires an explicit mutating HTTP method")
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

func validateNeutralCorePrompt(prompt []byte) error {
	lower := strings.ToLower(string(prompt))
	if strings.Contains(lower, "mcp__") || neutralATLRouteRE.MatchString(lower) || neutralTypedToolRE.MatchString(lower) {
		return fmt.Errorf("neutral-common core prompt contains a transport-specific route hint")
	}
	return nil
}

func escapesBase(path string) bool {
	clean := filepath.Clean(path)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func evaluateRunChecks(checks []RunCheck, final []byte, workspace string, atlInvocations, failedATL, unexpectedRequests, skillInvocations int, skillInvocationsByName map[string]int, delegations, guardDenials int, httpMethods map[string]int, httpMethodsObserved bool, cliExitCodes []int) (map[string]bool, error) {
	return evaluateRunChecksWithCapabilities(
		checks, final, workspace, atlInvocations, failedATL, unexpectedRequests,
		skillInvocations, skillInvocationsByName, delegations, guardDenials,
		httpMethods, httpMethodsObserved, cliExitCodes, nil, false,
		nil,
	)
}

func evaluateRunChecksWithCapabilities(
	checks []RunCheck,
	final []byte,
	workspace string,
	atlInvocations, failedATL, unexpectedRequests, skillInvocations int,
	skillInvocationsByName map[string]int,
	delegations, guardDenials int,
	httpMethods map[string]int,
	httpMethodsObserved bool,
	cliExitCodes []int,
	capabilityFamilies []CapabilityFamilyMetric,
	capabilityFamiliesObserved bool,
	capabilitySequence []string,
) (map[string]bool, error) {
	var document any
	if err := json.Unmarshal(final, &document); err != nil {
		return nil, fmt.Errorf("decode structured final response: %w", err)
	}
	results := make(map[string]bool, len(checks))
	for _, check := range checks {
		switch check.Kind {
		case "atl_invocations_min", "interface_invocations_min":
			results[check.Name] = atlInvocations >= check.Minimum
		case "skill_invocations_min":
			target, _ := skillInvocationTarget(check.Expected)
			observed := skillInvocations
			if target != "" {
				observed = skillInvocationsByName[target]
			}
			results[check.Name] = observed >= check.Minimum
		case "atl_invocations_max", "interface_invocations_max":
			results[check.Name] = atlInvocations <= check.Maximum
		case "atl_all_succeeded", "interface_all_succeeded":
			results[check.Name] = failedATL == 0
		case "atl_failures_equals", "interface_failures_equals":
			expected, _ := expectedATLFailureCount(check.Expected)
			results[check.Name] = failedATL == expected
		case "cli_exit_codes_equal":
			expected, _ := expectedCLIExitCodes(check.Expected)
			results[check.Name] = slices.Equal(cliExitCodes, expected)
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
		case "http_methods_equal":
			expected, _ := expectedHTTPMethods(check.Expected)
			results[check.Name] = httpMethodsObserved && equalHTTPMethods(httpMethods, expected)
		case "capability_families_equal":
			expected, _ := expectedCapabilityFamilies(check.Expected)
			results[check.Name] = capabilityFamiliesObserved && equalCapabilityFamilyExpectations(expected, capabilityFamilies)
		case "capability_sequence_equal":
			expected, _ := expectedCapabilitySequence(check.Expected)
			results[check.Name] = capabilityFamiliesObserved && slices.Equal(expected, capabilitySequence)
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
		case "json_equals_workspace_json":
			actual, ok := resolveJSONPointer(document, check.Pointer)
			if !ok {
				results[check.Name] = false
				continue
			}
			expectation, _ := workspaceJSONExpectationFrom(check.Expected)
			expected, ok := readWorkspaceJSONPointer(workspace, expectation)
			if !ok {
				results[check.Name] = false
				continue
			}
			actualJSON, _ := json.Marshal(actual)
			expectedJSON, _ := json.Marshal(expected)
			results[check.Name] = bytes.Equal(actualJSON, expectedJSON)
		case "workspace_file_sha256":
			expectation, _ := workspaceFileSHA256ExpectationFrom(check.Expected)
			results[check.Name] = workspaceFileMatchesSHA256(workspace, expectation)
		}
	}
	return results, nil
}

func expectedCapabilityFamilies(raw json.RawMessage) ([]capabilityFamilyExpectation, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var expected []capabilityFamilyExpectation
	if err := decoder.Decode(&expected); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	if len(expected) == 0 || len(expected) > 64 {
		return nil, false
	}
	for index, value := range expected {
		if _, known := allowedCapabilityFamilies[value.Family]; !known ||
			value.Invocations < 1 ||
			value.Invocations > maxObservedMethodCount ||
			value.Successes < 0 ||
			value.Failures < 0 ||
			value.Successes+value.Failures != value.Invocations ||
			index > 0 && expected[index-1].Family >= value.Family {
			return nil, false
		}
	}
	return expected, true
}

func equalCapabilityFamilyExpectations(expected []capabilityFamilyExpectation, observed []CapabilityFamilyMetric) bool {
	normalized, err := normalizeCapabilityFamilies(observed)
	if err != nil || len(expected) != len(normalized) {
		return false
	}
	for index, want := range expected {
		got := normalized[index]
		if want.Family != got.Family ||
			want.Invocations != got.Invocations ||
			want.Successes != got.Successes ||
			want.Failures != got.Failures {
			return false
		}
	}
	return true
}

func expectedCapabilitySequence(raw json.RawMessage) ([]string, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var expected []string
	if err := decoder.Decode(&expected); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	if len(expected) == 0 || len(expected) > 64 {
		return nil, false
	}
	for _, family := range expected {
		if _, known := allowedCapabilityFamilies[family]; !known {
			return nil, false
		}
	}
	return expected, true
}

type workspaceJSONExpectation struct {
	Path    string `json:"path"`
	Pointer string `json:"pointer"`
}

type workspaceFileSHA256Expectation struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func workspaceFileSHA256ExpectationFrom(raw json.RawMessage) (workspaceFileSHA256Expectation, bool) {
	var value workspaceFileSHA256Expectation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return workspaceFileSHA256Expectation{}, false
	}
	cleanPath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value.Path)))
	if value.Path == "" || filepath.IsAbs(filepath.FromSlash(value.Path)) || escapesBase(filepath.FromSlash(value.Path)) ||
		strings.Contains(value.Path, `\`) || cleanPath != value.Path || !workspaceSHA256HexRE.MatchString(value.SHA256) {
		return workspaceFileSHA256Expectation{}, false
	}
	return value, true
}

func workspaceFileMatchesSHA256(workspace string, expectation workspaceFileSHA256Expectation) bool {
	target := filepath.Join(workspace, filepath.FromSlash(expectation.Path))
	info, err := safepath.StatWithin(workspace, target)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxWorkspaceArtifactBytes {
		return false
	}
	data, err := safepath.ReadFileWithinLimit(workspace, target, maxWorkspaceArtifactBytes)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest) == expectation.SHA256
}

func workspaceJSONExpectationFrom(raw json.RawMessage) (workspaceJSONExpectation, bool) {
	var value workspaceJSONExpectation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return workspaceJSONExpectation{}, false
	}
	cleanPath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value.Path)))
	if value.Path == "" || filepath.IsAbs(filepath.FromSlash(value.Path)) || escapesBase(filepath.FromSlash(value.Path)) || cleanPath != value.Path || value.Pointer == "" {
		return workspaceJSONExpectation{}, false
	}
	return value, true
}

func readWorkspaceJSONPointer(workspace string, expectation workspaceJSONExpectation) (any, bool) {
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, false
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(expectation.Path)))
	if err != nil {
		return nil, false
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || escapesBase(relative) {
		return nil, false
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, false
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, false
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&document) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, false
	}
	value, ok := resolveJSONPointer(document, expectation.Pointer)
	return value, ok
}

func expectedHTTPMethods(raw json.RawMessage) (map[string]int, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var methods map[string]int
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&methods); err != nil || decoder.Decode(new(any)) != io.EOF || methods == nil || len(methods) > 16 {
		return nil, false
	}
	for method, count := range methods {
		if !methodRE.MatchString(method) || count < 1 || count > maxObservedMethodCount {
			return nil, false
		}
	}
	return methods, true
}

func equalHTTPMethods(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for method, count := range left {
		if right[method] != count {
			return false
		}
	}
	return true
}

func skillInvocationTarget(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	var target string
	if err := json.Unmarshal(raw, &target); err != nil || !skillNameRE.MatchString(target) {
		return "", false
	}
	return target, true
}

func expectedATLFailureCount(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false
	}
	var expected int
	if err := json.Unmarshal(raw, &expected); err != nil || expected < 0 {
		return 0, false
	}
	return expected, true
}

func expectedCLIExitCodes(raw json.RawMessage) ([]int, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var expected []int
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&expected) != nil || decoder.Decode(new(any)) != io.EOF || len(expected) == 0 || len(expected) > maxContractListEntries {
		return nil, false
	}
	for _, code := range expected {
		if code < 0 || code > 255 {
			return nil, false
		}
	}
	return expected, true
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
