package agenteval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/agenteval/executionbackend"
)

type localExecutionBackendAttemptAdmission struct {
	plan          executionbackend.Plan
	skillSHA256   string
	pluginSHA256  string
	atlSHA256     string
	wrapperSHA256 string
}

func localExecutionBackendTrialPlan(contract resolvedRunContract, skillDigest, pluginDigest, atlDigest, wrapperDigest string) (executionbackend.Contract, executionbackend.Plan, string, error) {
	var err error
	skillDigest, err = normalizedRunSkillDigest(skillDigest)
	if err != nil {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", err
	}
	pluginDigest = strings.TrimPrefix(pluginDigest, "sha256:")
	if !validSHA256(skillDigest) || !validSHA256(pluginDigest) || !validSHA256(atlDigest) || !validSHA256(wrapperDigest) {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", fmt.Errorf("local execution backend identity is invalid")
	}
	implementation, err := contentMinimizedAttemptDigest("execution-backend-implementation", "local-process/built-in-v1")
	if err != nil {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", err
	}
	content, err := contentMinimizedAttemptDigest("execution-backend-content", []string{atlDigest, wrapperDigest})
	if err != nil {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", err
	}
	backendContract, err := executionbackend.LocalProcessContract(implementation, content)
	if err != nil {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", err
	}
	definitions, err := contentMinimizedAttemptDigest("execution-backend-definitions", struct {
		Spec                 RunSpec      `json:"spec"`
		Scenario             Scenario     `json:"scenario"`
		Fixture              *MockFixture `json:"fixture,omitempty"`
		PluginSHA256         string       `json:"plugin_sha256"`
		PromptSHA256         string       `json:"prompt_sha256"`
		ProviderPromptSHA256 string       `json:"provider_prompt_sha256"`
		ResponseSHA256       string       `json:"response_sha256"`
	}{contract.spec, contract.scenario, contract.fixture, pluginDigest, sha256HexBytes(contract.prompt), sha256HexBytes(contract.providerPrompt), sha256HexBytes(contract.responseSchema)})
	if err != nil {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", err
	}
	fixture, err := digestWorkspaceTree(contract.workspaceTemplate)
	if err != nil {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", err
	}
	fixture = strings.TrimPrefix(fixture, "sha256:")
	if !validSHA256(fixture) {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", fmt.Errorf("local execution backend fixture identity is invalid")
	}
	deadline := time.Duration(contract.spec.TimeoutSeconds) * time.Second
	if deadline <= 0 || deadline/time.Millisecond > executionbackend.MaxDeadlineMillis {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", fmt.Errorf("local execution backend deadline is invalid")
	}
	plan, err := executionbackend.NewLocalProcessPlan(backendContract, executionbackend.LocalProcessPlanOptions{
		DefinitionsSHA256: definitions, FixtureSHA256: fixture, SkillSHA256: skillDigest,
		DeadlineMillis: uint64(deadline / time.Millisecond)}) // #nosec G115 -- positive bounded duration above.
	if err != nil {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", err
	}
	admitted, err := executionbackend.Admit(backendContract, plan)
	if err != nil {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", err
	}
	return backendContract, plan, admitted.SHA256(), nil
}

func localExecutionBackendPlanForAttempt(contract resolvedRunContract, bindings runAttemptBindings) (*localExecutionBackendAttemptAdmission, error) {
	if bindings.attemptSession == nil {
		return nil, nil
	}
	atlDigest, err := digestSyntheticExecutable(bindings.atlBinary, privateAgentBinaryMaxBytes)
	if err != nil {
		return nil, err
	}
	wrapperDigest, err := digestSyntheticExecutable(bindings.wrapperExecutable, 128<<20)
	if err != nil {
		return nil, err
	}
	skillDigest, err := normalizedRunSkillDigest(bindings.runtime.SkillDigest)
	if err != nil {
		return nil, err
	}
	pluginDigest, err := digestProviderPluginRoot(bindings.pluginRoot, contract.spec)
	if err != nil {
		return nil, err
	}
	_, plan, digest, err := localExecutionBackendTrialPlan(contract, skillDigest, pluginDigest, atlDigest, wrapperDigest)
	if err != nil || digest != bindings.attemptSession.plan.Binding.Identity.EnvironmentSHA256 {
		return nil, fmt.Errorf("execution backend attempt binding changed")
	}
	return &localExecutionBackendAttemptAdmission{plan: plan, skillSHA256: skillDigest, pluginSHA256: pluginDigest,
		atlSHA256: atlDigest, wrapperSHA256: wrapperDigest}, nil
}

func verifyLocalExecutionBackendAttempt(contract resolvedRunContract, bindings runAttemptBindings,
	layout headlessAttemptLayout, admission *localExecutionBackendAttemptAdmission,
) error {
	if admission == nil {
		return nil
	}
	if err := verifyLocalExecutionBackendLaunch(contract, bindings, admission); err != nil {
		return err
	}
	workspaceDigest, err := digestWorkspaceTree(layout.workspace)
	if err != nil || workspaceDigest != layout.workspaceAdmissionSHA256 {
		return fmt.Errorf("execution backend workspace copy changed before commit")
	}
	if shouldInstallBenchmarkSkills(contract.spec) {
		if err := verifyExecutionBackendSkillCopy(filepath.Join(layout.workspace, ".agents", "skills"), admission); err != nil {
			return err
		}
	}
	return nil
}

func verifyLocalExecutionBackendLaunch(contract resolvedRunContract, bindings runAttemptBindings,
	admission *localExecutionBackendAttemptAdmission,
) error {
	if admission == nil {
		return nil
	}
	agentDigest, err := digestSyntheticExecutable(bindings.agentBinary, privateAgentBinaryMaxBytes)
	if err != nil || agentDigest != bindings.attemptSession.plan.Binding.Identity.AgentSHA256 {
		return fmt.Errorf("execution backend agent identity changed before entry")
	}
	atlDigest, err := digestSyntheticExecutable(bindings.atlBinary, privateAgentBinaryMaxBytes)
	if err != nil || atlDigest != admission.atlSHA256 {
		return fmt.Errorf("execution backend atl identity changed before entry")
	}
	wrapperDigest, err := digestSyntheticExecutable(bindings.wrapperExecutable, 128<<20)
	if err != nil || wrapperDigest != admission.wrapperSHA256 {
		return fmt.Errorf("execution backend wrapper identity changed before entry")
	}
	pluginDigest, err := digestProviderPluginRoot(bindings.pluginRoot, contract.spec)
	if err != nil || pluginDigest != admission.pluginSHA256 {
		return fmt.Errorf("execution backend plugin identity changed before entry")
	}
	pluginVersion, skillDigest, err := pluginIdentity(bindings.pluginRoot, contract.spec.Provider)
	if err != nil {
		return err
	}
	skillDigest, err = normalizedRunSkillDigest(skillDigest)
	if err != nil || pluginVersion != bindings.runtime.PluginVersion || skillDigest != admission.skillSHA256 {
		return fmt.Errorf("execution backend skill identity changed before entry")
	}
	return nil
}

func beginLocalExecutionBackendAttempt(contract resolvedRunContract, bindings runAttemptBindings,
	layout headlessAttemptLayout, admission *localExecutionBackendAttemptAdmission,
) error {
	if bindings.attemptSession == nil {
		return nil
	}
	if err := verifyLocalExecutionBackendAttempt(contract, bindings, layout, admission); err != nil {
		return err
	}
	return beginRunAttempt(bindings.attemptSession)
}

func executionBackendMountSHA256(plan executionbackend.Plan, id executionbackend.MountID) string {
	for _, mount := range plan.Mounts {
		if mount.ID == id {
			return mount.ContentSHA256
		}
	}
	return ""
}

func verifyExecutionBackendWorkspaceCopy(root string, admission *localExecutionBackendAttemptAdmission) error {
	if admission == nil {
		return nil
	}
	digest, err := digestWorkspaceTree(root)
	if err != nil || strings.TrimPrefix(digest, "sha256:") != executionBackendMountSHA256(admission.plan, executionbackend.MountFixture) {
		return fmt.Errorf("execution backend workspace copy does not match its admitted fixture")
	}
	return nil
}

func verifyExecutionBackendSkillCopy(root string, admission *localExecutionBackendAttemptAdmission) error {
	if admission == nil {
		return nil
	}
	digest, err := digestTree(root)
	if err == nil {
		digest, err = normalizedRunSkillDigest(digest)
	}
	if err != nil || digest != executionBackendMountSHA256(admission.plan, executionbackend.MountSkill) {
		return fmt.Errorf("execution backend skill copy does not match its admitted skill")
	}
	return nil
}

func verifyExecutionBackendPluginCopy(root string, spec RunSpec, version string, admission *localExecutionBackendAttemptAdmission) error {
	if admission == nil {
		return nil
	}
	digest, err := digestProviderPluginRoot(root, spec)
	if err != nil || digest != admission.pluginSHA256 {
		return fmt.Errorf("execution backend plugin copy does not match its admitted tree")
	}
	pluginVersion, skillDigest, err := pluginIdentity(root, spec.Provider)
	if err == nil {
		skillDigest, err = normalizedRunSkillDigest(skillDigest)
	}
	if err != nil || pluginVersion != version || skillDigest != admission.skillSHA256 {
		return fmt.Errorf("execution backend plugin copy does not match its admitted skill")
	}
	return nil
}

func digestProviderPluginRoot(root string, spec RunSpec) (string, error) {
	var primaryPath, secondaryPath string
	secondaryOptional := false
	switch spec.Provider {
	case "codex":
		primaryPath = filepath.Join(root, "plugins", "atl")
		secondaryPath = filepath.Join(root, ".agents", "plugins")
		secondaryOptional = !codexAgentAdapter{}.layoutPolicy(spec).isolatedRuntimeCLI
	case "claude-code":
		primaryPath = filepath.Join(root, ".claude-plugin")
		secondaryPath = filepath.Join(root, "skills")
	default:
		return "", fmt.Errorf("execution backend plugin provider is invalid")
	}
	primary, err := digestTree(primaryPath)
	if err != nil {
		return "", err
	}
	secondary, err := digestProviderPluginTree(secondaryPath, secondaryOptional)
	if err != nil {
		return "", err
	}
	mcp := ""
	if spec.Provider == "claude-code" {
		mcpPath := filepath.Join(root, ".mcp.json")
		if _, statErr := os.Lstat(mcpPath); statErr == nil {
			mcp, err = digestSyntheticExecutable(mcpPath, 1<<20)
			if err != nil {
				return "", err
			}
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
	}
	return contentMinimizedAttemptDigest("execution-backend-plugin-root", []string{spec.Provider, primary, secondary, mcp})
}

func digestProviderPluginTree(path string, optional bool) (string, error) {
	info, err := os.Lstat(path)
	if optional && os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("execution backend plugin input is not a plain directory")
	}
	return digestTree(path)
}

func copyProviderPluginRoot(source, target string, spec RunSpec) error {
	if err := mkdirPrivate(target); err != nil {
		return err
	}
	type pluginTreeCopy struct {
		source, target string
		optional       bool
	}
	var copies []pluginTreeCopy
	switch spec.Provider {
	case "codex":
		if err := mkdirPrivate(filepath.Join(target, "plugins")); err != nil {
			return err
		}
		if err := mkdirPrivate(filepath.Join(target, ".agents")); err != nil {
			return err
		}
		copies = []pluginTreeCopy{{source: filepath.Join(source, "plugins", "atl"), target: filepath.Join(target, "plugins", "atl")},
			{source: filepath.Join(source, ".agents", "plugins"), target: filepath.Join(target, ".agents", "plugins"),
				optional: !codexAgentAdapter{}.layoutPolicy(spec).isolatedRuntimeCLI}}
	case "claude-code":
		copies = []pluginTreeCopy{{source: filepath.Join(source, ".claude-plugin"), target: filepath.Join(target, ".claude-plugin")},
			{source: filepath.Join(source, "skills"), target: filepath.Join(target, "skills")}}
	default:
		return fmt.Errorf("execution backend plugin provider is invalid")
	}
	for _, paths := range copies {
		if paths.optional {
			info, err := os.Lstat(paths.source)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("execution backend plugin input is not a plain directory")
			}
		}
		if err := copyWorkspace(paths.source, paths.target); err != nil {
			return err
		}
	}
	if spec.Provider == "claude-code" {
		sourceMCP := filepath.Join(source, ".mcp.json")
		if _, err := os.Lstat(sourceMCP); err == nil {
			if err := copyExecutableWithLimit(sourceMCP, filepath.Join(target, ".mcp.json"), 1<<20); err != nil {
				return err
			}
			if err := os.Chmod(filepath.Join(target, ".mcp.json"), 0o600); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func copyExecutionBackendExecutable(source, target, expectedSHA256 string, limit int64) error {
	if err := copyExecutableWithLimit(source, target, limit); err != nil {
		return err
	}
	digest, err := digestSyntheticExecutable(target, limit)
	if err != nil || digest != expectedSHA256 {
		return fmt.Errorf("execution backend executable copy does not match its admitted identity")
	}
	return nil
}

func copyExecutionBackendWrapper(source, target string, admission *localExecutionBackendAttemptAdmission) error {
	if err := copyExecutable(source, target); err != nil {
		return err
	}
	if admission == nil {
		return nil
	}
	digest, err := digestSyntheticExecutable(target, 128<<20)
	if err != nil || digest != admission.wrapperSHA256 {
		return fmt.Errorf("execution backend wrapper copy does not match its admitted executable")
	}
	return nil
}

func normalizedRunSkillDigest(value string) (string, error) {
	if validSHA256(value) {
		return value, nil
	}
	return contentMinimizedAttemptDigest("skill-identity", value)
}
