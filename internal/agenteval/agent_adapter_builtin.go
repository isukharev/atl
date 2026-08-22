package agenteval

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
)

type providerCommandInput struct {
	spec           RunSpec
	agentBinary    string
	atlBinary      string
	guardPath      string
	workspace      string
	schemaPath     string
	finalPath      string
	pluginRoot     string
	settingsPath   string
	mcpConfigPath  string
	confinement    ProviderConfinement
	responseSchema []byte
	bindings       providerCommandBindings
}

type agentAdapterLayoutPolicy struct {
	privateCLI              bool
	isolatedRuntimeCLI      bool
	syntheticBrokerCLI      bool
	syntheticBrokerWriteCLI bool
	privateLiveWriteCLI     bool
	reviewedWriteCLI        bool
	guardedBrokerCLI        bool
	brokerCLI               bool
	directFinalCaptureCLI   bool
}

type builtInAgentAdapter interface {
	id() string
	buildCommand(providerCommandInput) (ProviderCommand, error)
	parseOutput(transcript, finalFile []byte) (ProviderMetrics, []byte, error)
	projectResponseSchema(RunSpec, []byte) ([]byte, error)
	readFinal(string) ([]byte, error)
	preserveFinal(string, []byte) error
	pluginLayout(string) (string, string)
	pluginPath(RunSpec, string) string
	guardSettingsPath(string) string
	mcpConfigPath(RunSpec, string) string
	previewBinary() string
	installBenchmarkSkills(RunSpec) bool
	capabilitySupport() map[agentadapter.CapabilityID]agentadapter.Support
	layoutPolicy(RunSpec) agentAdapterLayoutPolicy
	reviewedMCPTools(RunSpec) []string
	previewConfinement(RunSpec) ProviderConfinement
	validateExecution(RunSpec) error
	usesProviderRuntime() bool
	prepareAuthSession([]string, *codexAuthSession) (*codexAuthSession, bool, error)
	newProviderRuntime(string, *codexAuthSession) (*providerRuntimeCapsule, error)
	skillReadRoot(headlessAttemptLayout, string, string, *providerRuntimeCapsule) (string, error)
	separatePrivateSkillRoot(headlessAttemptLayout) bool
	applyConfinement(RunSpec, headlessAttemptLayout, *ProviderConfinement, string, string, []string, []string)
	includeExternalMCPToken(RunSpec) bool
	inheritBackendEnvironment(RunSpec, headlessAttemptLayout) bool
	allowSyntheticCLIWrite(RunSpec) bool
	provisionBenchmarkSkills(context.Context, string, string, *providerRuntimeCapsule) error
	runConfinementPreflight(context.Context, string, string, string, string, ProviderConfinement, *providerRuntimeCapsule) error
}

type codexAgentAdapter struct{}
type claudeAgentAdapter struct{}

func builtInAgentAdapterRegistry() [2]builtInAgentAdapter {
	return [2]builtInAgentAdapter{claudeAgentAdapter{}, codexAgentAdapter{}}
}

func isCodexConfinedCLI(spec RunSpec) bool {
	mode := spec.EffectiveBackendMode()
	return spec.Provider == "codex" && spec.EffectiveToolTransport() == "cli" &&
		(mode == BackendModePrivateLive || mode == BackendModeProviderCalibration || isCodexSyntheticBrokerCLI(spec))
}

func builtInAgentAdapterFor(id string) (builtInAgentAdapter, error) {
	for _, adapter := range builtInAgentAdapterRegistry() {
		if adapter.id() == id {
			return adapter, nil
		}
	}
	return nil, fmt.Errorf("unsupported provider %q", id)
}

func (codexAgentAdapter) id() string  { return "codex" }
func (claudeAgentAdapter) id() string { return "claude-code" }

func (codexAgentAdapter) buildCommand(input providerCommandInput) (ProviderCommand, error) {
	return buildCodexProviderCommand(input)
}

func (claudeAgentAdapter) buildCommand(input providerCommandInput) (ProviderCommand, error) {
	return buildClaudeProviderCommand(input)
}

func (codexAgentAdapter) parseOutput(transcript, finalFile []byte) (ProviderMetrics, []byte, error) {
	metrics, err := parseCodexOutput(transcript)
	if err != nil {
		return ProviderMetrics{}, nil, err
	}
	final := bytes.TrimSpace(finalFile)
	if len(final) == 0 {
		return ProviderMetrics{}, nil, fmt.Errorf("codex final response is empty")
	}
	return metrics, final, nil
}

func (claudeAgentAdapter) parseOutput(transcript, _ []byte) (ProviderMetrics, []byte, error) {
	return parseClaudeOutput(transcript)
}

func (codexAgentAdapter) projectResponseSchema(spec RunSpec, original []byte) ([]byte, error) {
	return projectCodexResponseSchema(spec, original)
}

func (claudeAgentAdapter) projectResponseSchema(_ RunSpec, original []byte) ([]byte, error) {
	return append([]byte(nil), original...), nil
}

func (codexAgentAdapter) readFinal(path string) ([]byte, error) { return readBoundedFile(path, 4<<20) }
func (claudeAgentAdapter) readFinal(string) ([]byte, error)     { return nil, nil }

func (codexAgentAdapter) preserveFinal(path string, _ []byte) error { return os.Chmod(path, 0o600) }
func (claudeAgentAdapter) preserveFinal(path string, final []byte) error {
	return writePrivateFile(path, append(append([]byte(nil), final...), '\n'))
}

func (codexAgentAdapter) pluginLayout(root string) (string, string) {
	codexRoot := filepath.Join(root, "plugins", "atl")
	return filepath.Join(codexRoot, ".codex-plugin", "plugin.json"), filepath.Join(codexRoot, "skills")
}

func (claudeAgentAdapter) pluginLayout(root string) (string, string) {
	return filepath.Join(root, ".claude-plugin", "plugin.json"), filepath.Join(root, "skills")
}

func (codexAgentAdapter) pluginPath(RunSpec, string) string { return "" }
func (claudeAgentAdapter) pluginPath(spec RunSpec, root string) string {
	if !isSyntheticTypedMCP(spec) {
		return root
	}
	return ""
}

func (codexAgentAdapter) guardSettingsPath(string) string       { return "" }
func (claudeAgentAdapter) guardSettingsPath(path string) string { return path }
func (codexAgentAdapter) mcpConfigPath(RunSpec, string) string  { return "" }
func (claudeAgentAdapter) mcpConfigPath(spec RunSpec, path string) string {
	if spec.ToolTransport == "mcp" {
		return path
	}
	return ""
}

func (codexAgentAdapter) previewBinary() string  { return "codex" }
func (claudeAgentAdapter) previewBinary() string { return "claude" }

func (codexAgentAdapter) installBenchmarkSkills(spec RunSpec) bool {
	privateCLI := spec.EffectiveBackendMode() == BackendModePrivateLive && spec.EffectiveToolTransport() == "cli"
	return !privateCLI && !isSyntheticTypedMCP(spec)
}
func (claudeAgentAdapter) installBenchmarkSkills(RunSpec) bool { return false }

func (codexAgentAdapter) capabilitySupport() map[agentadapter.CapabilityID]agentadapter.Support {
	return agentAdapterCapabilitySupport(true)
}

func (claudeAgentAdapter) capabilitySupport() map[agentadapter.CapabilityID]agentadapter.Support {
	return agentAdapterCapabilitySupport(false)
}

func (codexAgentAdapter) layoutPolicy(spec RunSpec) agentAdapterLayoutPolicy {
	privateCLI := spec.EffectiveBackendMode() == BackendModePrivateLive && spec.EffectiveToolTransport() == "cli"
	syntheticBroker := isCodexSyntheticBrokerCLI(spec)
	syntheticWrite := syntheticBroker && spec.AllowSyntheticWrites
	privateWrite := privateCLI && spec.AllowLiveWrites
	return agentAdapterLayoutPolicy{privateCLI: privateCLI, isolatedRuntimeCLI: privateCLI,
		syntheticBrokerCLI: syntheticBroker, syntheticBrokerWriteCLI: syntheticWrite,
		privateLiveWriteCLI: privateWrite, reviewedWriteCLI: syntheticWrite || privateWrite,
		guardedBrokerCLI: privateCLI || syntheticBroker, brokerCLI: privateCLI || syntheticBroker}
}

func (claudeAgentAdapter) layoutPolicy(spec RunSpec) agentAdapterLayoutPolicy {
	privateCLI := spec.EffectiveBackendMode() == BackendModePrivateLive && spec.EffectiveToolTransport() == "cli"
	syntheticBroker := isSyntheticBrokerCLI(spec)
	syntheticWrite := syntheticBroker && spec.AllowSyntheticWrites
	privateWrite := privateCLI && spec.AllowLiveWrites
	return agentAdapterLayoutPolicy{privateCLI: privateCLI, syntheticBrokerCLI: syntheticBroker,
		syntheticBrokerWriteCLI: syntheticWrite, privateLiveWriteCLI: privateWrite,
		reviewedWriteCLI: syntheticWrite || privateWrite, guardedBrokerCLI: syntheticBroker,
		brokerCLI: privateCLI || syntheticBroker, directFinalCaptureCLI: privateCLI}
}

func (codexAgentAdapter) reviewedMCPTools(RunSpec) []string { return nil }
func (claudeAgentAdapter) reviewedMCPTools(spec RunSpec) []string {
	if spec.ToolTransport != "mcp" {
		return nil
	}
	return claudeMCPToolNamesForServer(mcpServerName(spec), spec.AllowedMCPTools)
}

func (codexAgentAdapter) previewConfinement(spec RunSpec) ProviderConfinement {
	confinement := ProviderConfinement{}
	if spec.EffectiveBackendMode() == BackendModePrivateLive {
		confinement.GuardMode = "mcp-with-skill-read"
		confinement.GuardCounterPath = "/private/guard-decisions.jsonl"
		confinement.WorkspaceReadRoot = "/private/workspace"
		confinement.AllowedReadRoots = []string{"/private/workspace"}
		confinement.SkillReadRoots = []string{"/private/workspace/.agents/skills"}
		confinement.AllowedMCPTools = claudeMCPToolNamesForServer(mcpServerName(spec), spec.AllowedMCPTools)
		if spec.ToolTransport == "cli" {
			confinement.GuardMode = "private-cli"
			confinement.AllowedReadRoots = []string{"/private/skill-read-root", "/private/workspace"}
			confinement.SkillReadRoots = []string{"/private/skill-read-root"}
			confinement.AllowedMCPTools = nil
			confinement.RequestDirectory = "/private/requests"
			confinement.ResponseDirectory = "/private/responses"
		}
	}
	if isCodexSyntheticBrokerCLI(spec) {
		confinement.GuardMode = "private-cli"
		confinement.GuardCounterPath = "/private/guard-decisions.jsonl"
		confinement.WorkspaceReadRoot = "/private/workspace"
		confinement.AllowedReadRoots = []string{"/private/workspace"}
		confinement.SkillReadRoots = []string{"/private/workspace/.agents/skills"}
		confinement.RequestDirectory = "/private/requests"
		confinement.ResponseDirectory = "/private/responses"
	}
	return confinement
}

func (claudeAgentAdapter) previewConfinement(RunSpec) ProviderConfinement {
	return ProviderConfinement{}
}

func (codexAgentAdapter) validateExecution(spec RunSpec) error {
	if spec.EffectiveToolTransport() != "mcp" && spec.EffectiveBackendMode() != BackendModePrivateLive && !isCodexSyntheticBrokerCLI(spec) {
		return fmt.Errorf("codex synthetic model execution requires tool_transport=mcp; cli transport remains validate/dry-run only")
	}
	return nil
}

func (claudeAgentAdapter) validateExecution(RunSpec) error { return nil }
func (codexAgentAdapter) usesProviderRuntime() bool        { return true }
func (claudeAgentAdapter) usesProviderRuntime() bool       { return false }

func (codexAgentAdapter) prepareAuthSession(ambient []string, existing *codexAuthSession) (*codexAuthSession, bool, error) {
	if existing != nil {
		return existing, false, nil
	}
	session, err := newCodexAuthSession(ambient)
	return session, err == nil, err
}

func (claudeAgentAdapter) prepareAuthSession(_ []string, existing *codexAuthSession) (*codexAuthSession, bool, error) {
	if existing != nil {
		return nil, false, fmt.Errorf("the selected agent adapter does not accept an external authentication session")
	}
	return nil, false, nil
}

func (codexAgentAdapter) newProviderRuntime(root string, session *codexAuthSession) (*providerRuntimeCapsule, error) {
	return newCodexProviderRuntime(root, session)
}

func (claudeAgentAdapter) newProviderRuntime(string, *codexAuthSession) (*providerRuntimeCapsule, error) {
	return nil, nil
}

func (codexAgentAdapter) skillReadRoot(layout headlessAttemptLayout, workspace, _ string, runtime *providerRuntimeCapsule) (string, error) {
	if layout.isolatedRuntimeCLI {
		if runtime == nil {
			return "", fmt.Errorf("private codex CLI run requires an isolated provider runtime")
		}
		root := runtime.PluginSkillRoot()
		if root == "" {
			return "", fmt.Errorf("private codex CLI run has no installed plugin skill root")
		}
		return root, nil
	}
	return filepath.Join(workspace, ".agents", "skills"), nil
}

func (claudeAgentAdapter) skillReadRoot(_ headlessAttemptLayout, _, defaultRoot string, _ *providerRuntimeCapsule) (string, error) {
	return defaultRoot, nil
}

func (codexAgentAdapter) separatePrivateSkillRoot(layout headlessAttemptLayout) bool {
	return !layout.isolatedRuntimeCLI
}
func (claudeAgentAdapter) separatePrivateSkillRoot(headlessAttemptLayout) bool { return false }

func (codexAgentAdapter) applyConfinement(spec RunSpec, layout headlessAttemptLayout, confinement *ProviderConfinement,
	guardCounterPath, workspace string, readRoots, skillRoots []string) {
	if spec.EffectiveBackendMode() != BackendModePrivateLive && !layout.syntheticBrokerCLI {
		return
	}
	confinement.GuardMode = "mcp-with-skill-read"
	if layout.guardedBrokerCLI {
		confinement.GuardMode = "private-cli"
	}
	confinement.GuardCounterPath = guardCounterPath
	confinement.WorkspaceReadRoot = workspace
	confinement.AllowedReadRoots = append([]string(nil), readRoots...)
	confinement.SkillReadRoots = append([]string(nil), skillRoots...)
	confinement.AllowedMCPTools = claudeMCPToolNamesForServer(mcpServerName(spec), spec.AllowedMCPTools)
	if layout.isolatedRuntimeCLI {
		confinement.AllowedMCPTools = nil
	}
}

func (claudeAgentAdapter) applyConfinement(RunSpec, headlessAttemptLayout, *ProviderConfinement, string, string, []string, []string) {
}

func (codexAgentAdapter) includeExternalMCPToken(spec RunSpec) bool {
	return spec.EffectiveSurface() == SurfaceExternalMCP
}
func (claudeAgentAdapter) includeExternalMCPToken(RunSpec) bool { return false }

func (codexAgentAdapter) inheritBackendEnvironment(_ RunSpec, layout headlessAttemptLayout) bool {
	return !layout.syntheticBrokerCLI
}
func (claudeAgentAdapter) inheritBackendEnvironment(spec RunSpec, _ headlessAttemptLayout) bool {
	return spec.EffectiveToolTransport() != "mcp"
}

func (codexAgentAdapter) allowSyntheticCLIWrite(RunSpec) bool { return false }
func (claudeAgentAdapter) allowSyntheticCLIWrite(spec RunSpec) bool {
	return spec.EffectiveBackendMode() == BackendModeSynthetic && spec.EffectiveToolTransport() == "cli" && spec.AllowSyntheticWrites
}

func (codexAgentAdapter) provisionBenchmarkSkills(ctx context.Context, agentBinary, pluginRoot string, runtime *providerRuntimeCapsule) error {
	return provisionCodexBenchmarkPlugin(ctx, agentBinary, pluginRoot, runtime)
}

func (claudeAgentAdapter) provisionBenchmarkSkills(context.Context, string, string, *providerRuntimeCapsule) error {
	return nil
}

func (codexAgentAdapter) runConfinementPreflight(ctx context.Context, agentBinary, workspace, probeExecutablePath,
	brokerManifestPath string, confinement ProviderConfinement, runtime *providerRuntimeCapsule) error {
	return runCodexConfinementPreflight(ctx, agentBinary, workspace, probeExecutablePath, brokerManifestPath, confinement, runtime)
}

func (claudeAgentAdapter) runConfinementPreflight(context.Context, string, string, string, string, ProviderConfinement,
	*providerRuntimeCapsule) error {
	return nil
}

func agentAdapterCapabilitySupport(codex bool) map[agentadapter.CapabilityID]agentadapter.Support {
	support := map[agentadapter.CapabilityID]agentadapter.Support{
		agentadapter.CapabilityActivationDeveloperInstructions: agentadapter.SupportUnsupported,
		agentadapter.CapabilityActivationEvidence:              agentadapter.SupportSupported,
		agentadapter.CapabilityActivationForcedInjection:       agentadapter.SupportSupported,
		agentadapter.CapabilityActivationNative:                agentadapter.SupportSupported,
		agentadapter.CapabilityCancellation:                    agentadapter.SupportSupported,
		agentadapter.CapabilityLocalExecution:                  agentadapter.SupportSupported,
		agentadapter.CapabilityPermissionPolicy:                agentadapter.SupportSupported,
		agentadapter.CapabilityProcessTree:                     agentadapter.SupportSupported,
		agentadapter.CapabilitySandbox:                         agentadapter.SupportSupported,
		agentadapter.CapabilityGenericChild:                    agentadapter.SupportUnknown,
		agentadapter.CapabilityParallelChildren:                agentadapter.SupportUnknown,
		agentadapter.CapabilitySingle:                          agentadapter.SupportSupported,
		agentadapter.CapabilitySpecializedChildren:             agentadapter.SupportUnknown,
		agentadapter.CapabilityMCP:                             agentadapter.SupportSupported,
		agentadapter.CapabilityTrajectory:                      agentadapter.SupportSupported,
		agentadapter.CapabilityCost:                            agentadapter.SupportSupported,
		agentadapter.CapabilityParentUsage:                     agentadapter.SupportSupported,
		agentadapter.CapabilityTreeUsage:                       agentadapter.SupportUnknown,
	}
	if codex {
		support[agentadapter.CapabilityActivationDeveloperInstructions] = agentadapter.SupportSupported
	} else {
		support[agentadapter.CapabilityActivationForcedInjection] = agentadapter.SupportUnsupported
		support[agentadapter.CapabilityActivationNative] = agentadapter.SupportUnsupported
	}
	return support
}
