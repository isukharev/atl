package agenteval

import (
	"encoding/json"
	"fmt"
	"slices"
)

// These names form the internal environment ABI between the evaluator runner,
// provider launchers, and scripts/agent-eval wrappers. Keep them untyped so
// wrapper consumers can use them directly with os.Getenv and environment maps.
const (
	WrapperEnvAllowedCommands         = "ATL_EVAL_ALLOWED_COMMANDS"
	WrapperEnvAllowedMCPTools         = "ATL_EVAL_ALLOWED_MCP_TOOLS"
	WrapperEnvAllowedReadRoots        = "ATL_EVAL_ALLOWED_READ_ROOTS"
	WrapperEnvAllowReviewedWrites     = "ATL_EVAL_ALLOW_REVIEWED_WRITES"
	WrapperEnvAllowSyntheticWrites    = "ATL_EVAL_ALLOW_SYNTHETIC_WRITES"
	WrapperEnvCLIPolicyFile           = "ATL_EVAL_CLI_POLICY_FILE"
	WrapperEnvCLIResultDir            = "ATL_EVAL_CLI_RESULT_DIR"
	WrapperEnvCommandBrokerFile       = "ATL_EVAL_COMMAND_BROKER_FILE"
	WrapperEnvCounter                 = "ATL_EVAL_COUNTER"
	WrapperEnvExternalMCPToken        = "ATL_EVAL_EXTERNAL_MCP_TOKEN" // #nosec G101 -- environment variable name, not a credential
	WrapperEnvForbiddenNetworkAddress = "ATL_EVAL_FORBIDDEN_NETWORK_ADDRESS"
	WrapperEnvGuardCounter            = "ATL_EVAL_GUARD_COUNTER"
	WrapperEnvGuardMode               = "ATL_EVAL_GUARD_MODE"
	WrapperEnvMaxDelegations          = "ATL_EVAL_MAX_DELEGATIONS"
	WrapperEnvRealBinary              = "ATL_EVAL_REAL_BINARY"
	WrapperEnvSkillReadRoots          = "ATL_EVAL_SKILL_READ_ROOTS"
	WrapperEnvWorkspaceRoot           = "ATL_EVAL_WORKSPACE_ROOT"
	WrapperEnvHTTPGuardFile           = "ATL_" + "EVAL_HTTP_GUARD_FILE"
)

type wrapperEnvironmentValueKind string

const (
	wrapperValueEnum               wrapperEnvironmentValueKind = "enum"
	wrapperValueJSONArrayPaths     wrapperEnvironmentValueKind = "json_path_array"
	wrapperValueJSONArrayStrings   wrapperEnvironmentValueKind = "json_string_array"
	wrapperValueNonnegativeInteger wrapperEnvironmentValueKind = "nonnegative_integer"
	wrapperValueOpaqueCapability   wrapperEnvironmentValueKind = "opaque_token"
	wrapperValuePath               wrapperEnvironmentValueKind = "path"
	wrapperValuePresenceFlag       wrapperEnvironmentValueKind = "presence_flag"
	wrapperValueSocketAddress      wrapperEnvironmentValueKind = "socket_address"
)

type wrapperEnvironmentMode string

const (
	wrapperModeAll                 wrapperEnvironmentMode = "all"
	wrapperModeCLI                 wrapperEnvironmentMode = "cli"
	wrapperModeExternalMCP         wrapperEnvironmentMode = "external_mcp"
	wrapperModeForbiddenAmbient    wrapperEnvironmentMode = "forbidden_ambient"
	wrapperModePrivateCLI          wrapperEnvironmentMode = "private_cli"
	wrapperModePrivateMCP          wrapperEnvironmentMode = "private_mcp"
	wrapperModeProviderCalibration wrapperEnvironmentMode = "provider_calibration"
	wrapperModeSkillRead           wrapperEnvironmentMode = "skill_read"
	wrapperModeSyntheticCLI        wrapperEnvironmentMode = "synthetic_cli"
	wrapperModeSyntheticMCP        wrapperEnvironmentMode = "synthetic_mcp"
)

type wrapperEnvironmentSensitivity string

const (
	wrapperSensitivityPolicy            wrapperEnvironmentSensitivity = "policy"
	wrapperSensitivityPrivateAddress    wrapperEnvironmentSensitivity = "private_address"
	wrapperSensitivityPrivatePath       wrapperEnvironmentSensitivity = "private_path"
	wrapperSensitivityPrivilegedControl wrapperEnvironmentSensitivity = "privileged_control"
	wrapperSensitivitySecret            wrapperEnvironmentSensitivity = "secret"
)

type wrapperEnvironmentVariable struct {
	Name        string
	Producers   []string
	Consumers   []string
	ValueKind   wrapperEnvironmentValueKind
	Modes       []wrapperEnvironmentMode
	Sensitivity wrapperEnvironmentSensitivity
}

var wrapperEnvironmentRegistry = []wrapperEnvironmentVariable{
	{Name: WrapperEnvAllowedCommands, Producers: []string{"internal/agenteval/runner.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValueJSONArrayStrings, Modes: []wrapperEnvironmentMode{wrapperModeSyntheticCLI, wrapperModePrivateCLI}, Sensitivity: wrapperSensitivityPolicy},
	{Name: WrapperEnvAllowedMCPTools, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/calibration.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValueJSONArrayStrings, Modes: []wrapperEnvironmentMode{wrapperModeSyntheticMCP, wrapperModePrivateMCP, wrapperModeProviderCalibration}, Sensitivity: wrapperSensitivityPolicy},
	{Name: WrapperEnvAllowedReadRoots, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/calibration.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValueJSONArrayPaths, Modes: []wrapperEnvironmentMode{wrapperModeAll}, Sensitivity: wrapperSensitivityPrivatePath},
	{Name: WrapperEnvAllowReviewedWrites, Producers: []string{"internal/agenteval/runner.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValuePresenceFlag, Modes: []wrapperEnvironmentMode{wrapperModePrivateCLI}, Sensitivity: wrapperSensitivityPolicy},
	{Name: WrapperEnvAllowSyntheticWrites, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/runner_provider.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValuePresenceFlag, Modes: []wrapperEnvironmentMode{wrapperModeSyntheticCLI}, Sensitivity: wrapperSensitivityPolicy},
	{Name: WrapperEnvCLIPolicyFile, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/calibration.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValuePath, Modes: []wrapperEnvironmentMode{wrapperModeCLI, wrapperModeProviderCalibration}, Sensitivity: wrapperSensitivityPrivatePath},
	{Name: WrapperEnvCLIResultDir, Producers: []string{"internal/agenteval/runner.go"}, Consumers: []string{"scripts/agent-eval/proxy.go"}, ValueKind: wrapperValuePath, Modes: []wrapperEnvironmentMode{wrapperModePrivateCLI}, Sensitivity: wrapperSensitivityPrivatePath},
	{Name: WrapperEnvCommandBrokerFile, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/calibration.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/command_broker.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValuePath, Modes: []wrapperEnvironmentMode{wrapperModeCLI, wrapperModeProviderCalibration}, Sensitivity: wrapperSensitivityPrivatePath},
	{Name: WrapperEnvCounter, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/calibration.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValuePath, Modes: []wrapperEnvironmentMode{wrapperModeCLI, wrapperModeProviderCalibration}, Sensitivity: wrapperSensitivityPrivatePath},
	{Name: WrapperEnvExternalMCPToken, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/runner_provider.go"}, Consumers: []string{"internal/agenteval/provider.go"}, ValueKind: wrapperValueOpaqueCapability, Modes: []wrapperEnvironmentMode{wrapperModeExternalMCP}, Sensitivity: wrapperSensitivitySecret},
	{Name: WrapperEnvForbiddenNetworkAddress, Producers: []string{"internal/agenteval/runner.go"}, Consumers: []string{"scripts/agent-eval/command_broker.go"}, ValueKind: wrapperValueSocketAddress, Modes: []wrapperEnvironmentMode{wrapperModePrivateCLI}, Sensitivity: wrapperSensitivityPrivateAddress},
	{Name: WrapperEnvGuardCounter, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/calibration.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValuePath, Modes: []wrapperEnvironmentMode{wrapperModeAll}, Sensitivity: wrapperSensitivityPrivatePath},
	{Name: WrapperEnvGuardMode, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/calibration.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValueEnum, Modes: []wrapperEnvironmentMode{wrapperModeAll}, Sensitivity: wrapperSensitivityPolicy},
	{Name: WrapperEnvHTTPGuardFile, Producers: []string{}, Consumers: []string{}, ValueKind: wrapperValuePath, Modes: []wrapperEnvironmentMode{wrapperModeForbiddenAmbient}, Sensitivity: wrapperSensitivityPrivilegedControl},
	{Name: WrapperEnvMaxDelegations, Producers: []string{"internal/agenteval/runner.go"}, Consumers: []string{"scripts/agent-eval/proxy.go"}, ValueKind: wrapperValueNonnegativeInteger, Modes: []wrapperEnvironmentMode{wrapperModeAll}, Sensitivity: wrapperSensitivityPolicy},
	{Name: WrapperEnvRealBinary, Producers: []string{"internal/agenteval/runner.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValuePath, Modes: []wrapperEnvironmentMode{wrapperModeCLI}, Sensitivity: wrapperSensitivityPrivatePath},
	{Name: WrapperEnvSkillReadRoots, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/calibration.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValueJSONArrayPaths, Modes: []wrapperEnvironmentMode{wrapperModeSkillRead}, Sensitivity: wrapperSensitivityPrivatePath},
	{Name: WrapperEnvWorkspaceRoot, Producers: []string{"internal/agenteval/runner.go", "internal/agenteval/calibration.go"}, Consumers: []string{"internal/agenteval/provider.go", "scripts/agent-eval/proxy.go"}, ValueKind: wrapperValuePath, Modes: []wrapperEnvironmentMode{wrapperModeAll}, Sensitivity: wrapperSensitivityPrivatePath},
}

func wrapperEnvironmentVariables() []wrapperEnvironmentVariable {
	variables := make([]wrapperEnvironmentVariable, len(wrapperEnvironmentRegistry))
	for i, variable := range wrapperEnvironmentRegistry {
		variable.Producers = slices.Clone(variable.Producers)
		variable.Consumers = slices.Clone(variable.Consumers)
		variable.Modes = slices.Clone(variable.Modes)
		variables[i] = variable
	}
	return variables
}

type wrapperEnvironmentProjectionID string

const (
	wrapperProjectionSyntheticCLI            wrapperEnvironmentProjectionID = "synthetic_cli"
	wrapperProjectionMCP                     wrapperEnvironmentProjectionID = "mcp"
	wrapperProjectionExternalMCP             wrapperEnvironmentProjectionID = "external_mcp"
	wrapperProjectionPrivateMCP              wrapperEnvironmentProjectionID = "private_mcp"
	wrapperProjectionPrivateExternalMCP      wrapperEnvironmentProjectionID = "private_external_mcp"
	wrapperProjectionConfinedCLI             wrapperEnvironmentProjectionID = "confined_cli"
	wrapperProjectionPrivateReviewedWriteCLI wrapperEnvironmentProjectionID = "private_reviewed_write_cli"
	wrapperProjectionSyntheticWriteCLI       wrapperEnvironmentProjectionID = "synthetic_write_cli"
	wrapperProjectionLegacyCalibration       wrapperEnvironmentProjectionID = "legacy_calibration"
)

func wrapperEnvironmentProjection(id wrapperEnvironmentProjectionID) []string {
	var names []string
	switch id {
	case wrapperProjectionSyntheticCLI:
		names = []string{"PATH", "ATL_READ_ONLY", "ATL_NO_UPDATE", "ATL_CONFIG_DIR", "ATL_MIRROR_ROOT", "ATL_JIRA_URL", "ATL_CONFLUENCE_URL", "ATL_JIRA_PAT", "ATL_CONFLUENCE_PAT", "ATL_ALLOW_INSECURE", WrapperEnvRealBinary, WrapperEnvCounter, WrapperEnvAllowedCommands}
	case wrapperProjectionMCP:
		names = []string{"PATH", "LANG", "LC_ALL", "TERM"}
	case wrapperProjectionExternalMCP:
		names = []string{"PATH", "LANG", "LC_ALL", "TERM", "NO_PROXY", "no_proxy", WrapperEnvExternalMCPToken}
	case wrapperProjectionPrivateMCP:
		names = []string{"PATH", "LANG", "LC_ALL", "TERM", WrapperEnvAllowedReadRoots, WrapperEnvSkillReadRoots, WrapperEnvWorkspaceRoot}
	case wrapperProjectionPrivateExternalMCP:
		names = []string{"PATH", "LANG", "LC_ALL", "TERM", "NO_PROXY", "no_proxy", WrapperEnvExternalMCPToken, WrapperEnvAllowedReadRoots, WrapperEnvSkillReadRoots, WrapperEnvWorkspaceRoot}
	case wrapperProjectionConfinedCLI:
		names = []string{"PATH", "SHELL", "LANG", "LC_ALL", "TERM", "ATL_READ_ONLY", WrapperEnvCounter, WrapperEnvGuardCounter, WrapperEnvCLIPolicyFile, WrapperEnvCommandBrokerFile, WrapperEnvGuardMode, WrapperEnvAllowedReadRoots, WrapperEnvSkillReadRoots, WrapperEnvWorkspaceRoot}
	case wrapperProjectionPrivateReviewedWriteCLI:
		names = []string{"PATH", "SHELL", "LANG", "LC_ALL", "TERM", "ATL_READ_ONLY", WrapperEnvAllowReviewedWrites, WrapperEnvCounter, WrapperEnvGuardCounter, WrapperEnvCLIPolicyFile, WrapperEnvCommandBrokerFile, WrapperEnvGuardMode, WrapperEnvAllowedReadRoots, WrapperEnvSkillReadRoots, WrapperEnvWorkspaceRoot}
	case wrapperProjectionSyntheticWriteCLI:
		names = []string{"PATH", "SHELL", "LANG", "LC_ALL", "TERM", WrapperEnvAllowSyntheticWrites, WrapperEnvCounter, WrapperEnvGuardCounter, WrapperEnvCLIPolicyFile, WrapperEnvCommandBrokerFile, WrapperEnvGuardMode, WrapperEnvAllowedReadRoots, WrapperEnvSkillReadRoots, WrapperEnvWorkspaceRoot}
	case wrapperProjectionLegacyCalibration:
		names = []string{"PATH", "SHELL", "LANG", "LC_ALL", "TERM", "ATL_READ_ONLY", WrapperEnvCounter, WrapperEnvGuardCounter, WrapperEnvCLIPolicyFile, WrapperEnvCommandBrokerFile, WrapperEnvGuardMode, WrapperEnvAllowedReadRoots, WrapperEnvWorkspaceRoot}
	default:
		panic(fmt.Sprintf("unknown wrapper environment projection %q", id))
	}
	return slices.Clone(names)
}

func renderWrapperEnvironmentProjection(id wrapperEnvironmentProjectionID) string {
	data, err := json.Marshal(wrapperEnvironmentProjection(id))
	if err != nil {
		panic(fmt.Sprintf("render wrapper environment projection %q: %v", id, err))
	}
	return string(data)
}
