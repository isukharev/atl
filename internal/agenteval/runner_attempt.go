package agenteval

import (
	"bytes"
	"fmt"
	"path/filepath"
)

type headlessAttemptLayout struct {
	privateCLI               bool
	isolatedRuntimeCLI       bool
	syntheticBrokerCLI       bool
	syntheticBrokerWriteCLI  bool
	privateLiveWriteCLI      bool
	reviewedWriteCLI         bool
	guardedBrokerCLI         bool
	brokerCLI                bool
	directFinalCaptureCLI    bool
	gatewayBackedMCP         bool
	runDir                   string
	workspace                string
	workspaceAdmissionSHA256 string
	taskContractSHA256       string
	executionContractSHA256  string
	providerResponseSchema   string
	finalPath                string
	transcriptPath           string
	stderrPath               string
	evalDir                  string
	mirrorRoot               string
	counterPath              string
	guardCounterPath         string
	wrapperDir               string
	guardPath                string
	brokerRequestDirectory   string
	brokerResponseDirectory  string
	cliResultDirectory       string
	probeExecutablePath      string
	settingsPath             string
	atlConfigDir             string
	mcpConfigPath            string
	admittedAgentBinary      string
	admittedATLBinary        string
	admittedPluginRoot       string
	admittedWrapper          string
}

func prepareHeadlessAttemptLayout(contract resolvedRunContract, bindings runAttemptBindings,
	backendAdmission *localExecutionBackendAttemptAdmission,
) (headlessAttemptLayout, error) {
	adapter, err := builtInAgentAdapterFor(contract.spec.Provider)
	if err != nil {
		return headlessAttemptLayout{}, err
	}
	policy := adapter.layoutPolicy(contract.spec)
	privateCLI, isolatedRuntimeCLI := policy.privateCLI, policy.isolatedRuntimeCLI
	syntheticBrokerCLI, syntheticBrokerWriteCLI := policy.syntheticBrokerCLI, policy.syntheticBrokerWriteCLI
	privateLiveWriteCLI, reviewedWriteCLI := policy.privateLiveWriteCLI, policy.reviewedWriteCLI
	guardedBrokerCLI, brokerCLI, directFinalCaptureCLI := policy.guardedBrokerCLI, policy.brokerCLI, policy.directFinalCaptureCLI
	gatewayBackedMCP := gatewayBackedInternalMCP(contract.spec)
	if err := validatePathComponentID("scenario id", contract.scenario.ID); err != nil {
		return headlessAttemptLayout{}, err
	}
	if err := validatePathComponentID("run variant", contract.spec.Variant); err != nil {
		return headlessAttemptLayout{}, err
	}
	runDir := filepath.Join(bindings.outputRoot, contract.scenario.ID, contract.spec.Provider, contract.spec.Variant, fmt.Sprintf("run-%02d", bindings.repetition))
	inside, pathErr := pathWithin(bindings.outputRoot, runDir)
	if pathErr != nil || !inside {
		return headlessAttemptLayout{}, fmt.Errorf("private run directory escapes its output root")
	}
	if err := mkdirPrivateWithin(bindings.outputRoot, runDir); err != nil {
		return headlessAttemptLayout{}, err
	}
	workspace := filepath.Join(runDir, "workspace")
	if contract.spec.EffectiveBackendMode() == BackendModePrivateLive {
		if err := validatePrivateWorkspaceTemplate(contract.workspaceTemplate); err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	if err := copyWorkspace(contract.workspaceTemplate, workspace); err != nil {
		return headlessAttemptLayout{}, err
	}
	if err := verifyExecutionBackendWorkspaceCopy(workspace, backendAdmission); err != nil {
		return headlessAttemptLayout{}, err
	}
	evalDir := filepath.Join(runDir, ".atl-eval")
	if err := mkdirPrivate(evalDir); err != nil {
		return headlessAttemptLayout{}, err
	}
	admittedPluginRoot := bindings.pluginRoot
	admittedATLBinary := bindings.atlBinary
	if backendAdmission != nil {
		admittedRoot := filepath.Join(evalDir, "admitted")
		if err := mkdirPrivate(admittedRoot); err != nil {
			return headlessAttemptLayout{}, err
		}
		admittedPluginRoot = filepath.Join(admittedRoot, "plugin")
		if err := copyProviderPluginRoot(bindings.pluginRoot, admittedPluginRoot, contract.spec); err != nil {
			return headlessAttemptLayout{}, err
		}
		if err := verifyExecutionBackendPluginCopy(admittedPluginRoot, contract.spec, bindings.runtime.PluginVersion,
			backendAdmission); err != nil {
			return headlessAttemptLayout{}, err
		}
		admittedATLBinary = filepath.Join(admittedRoot, "atl"+filepath.Ext(bindings.atlBinary))
		if err := copyExecutionBackendExecutable(bindings.atlBinary, admittedATLBinary, backendAdmission.atlSHA256,
			privateAgentBinaryMaxBytes); err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	taskContractSHA256 := ""
	if bindings.attestation != nil {
		taskContractSHA256, err = syntheticTaskContractSHA256(contract, workspace)
		if err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	if shouldInstallBenchmarkSkills(contract.spec) {
		_, skillRoot, err := providerPluginLayout(admittedPluginRoot, contract.spec.Provider)
		if err != nil {
			return headlessAttemptLayout{}, err
		}
		copiedSkillRoot := filepath.Join(workspace, ".agents", "skills")
		if err := copyWorkspace(skillRoot, copiedSkillRoot); err != nil {
			return headlessAttemptLayout{}, fmt.Errorf("install benchmark skills: %w", err)
		}
		if err := verifyExecutionBackendSkillCopy(copiedSkillRoot, backendAdmission); err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	workspaceAdmissionSHA256 := ""
	if backendAdmission != nil {
		workspaceAdmissionSHA256, err = digestWorkspaceTree(workspace)
		if err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	responseSchemaPath := filepath.Join(runDir, "response-schema.json")
	if err := writePrivateFile(responseSchemaPath, contract.responseSchema); err != nil {
		return headlessAttemptLayout{}, err
	}
	providerSchema, projectionErr := providerResponseSchema(contract.spec, contract.responseSchema)
	if projectionErr != nil {
		return headlessAttemptLayout{}, projectionErr
	}
	executionContractSHA256 := ""
	if bindings.attestation != nil {
		executionContractSHA256, err = syntheticExecutionContractSHA256(bindings.attestation, taskContractSHA256, bindings.runtime, providerSchema)
		if err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	providerResponseSchemaPath := responseSchemaPath
	if !bytes.Equal(providerSchema, contract.responseSchema) {
		providerResponseSchemaPath = filepath.Join(runDir, "provider-response-schema.json")
		if err := writePrivateFile(providerResponseSchemaPath, providerSchema); err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	finalPath := filepath.Join(runDir, "final.json")
	transcriptPath := filepath.Join(runDir, "transcript.jsonl")
	stderrPath := filepath.Join(runDir, "agent.stderr")
	mirrorRoot := filepath.Join(evalDir, "mirror")
	if contract.spec.EffectiveBackendMode() == BackendModeSynthetic && contract.spec.EffectiveSurface() == SurfaceATLMCP {
		resolvedMirrorRoot, rootErr := syntheticMCPMirrorRoot(workspace, mirrorRoot)
		if rootErr != nil {
			return headlessAttemptLayout{}, rootErr
		}
		mirrorRoot = resolvedMirrorRoot
	}
	counterPath := filepath.Join(evalDir, "atl-invocations.jsonl")
	guardCounterPath := filepath.Join(evalDir, "guard-decisions.jsonl")
	wrapperDir := filepath.Join(runDir, "bin")
	if err := mkdirPrivate(wrapperDir); err != nil {
		return headlessAttemptLayout{}, err
	}
	admittedWrapper := filepath.Join(wrapperDir, wrapperName())
	if err := copyExecutionBackendWrapper(bindings.wrapperExecutable, admittedWrapper, backendAdmission); err != nil {
		return headlessAttemptLayout{}, err
	}
	guardPath := filepath.Join(wrapperDir, guardName())
	if err := copyExecutionBackendWrapper(bindings.wrapperExecutable, guardPath, backendAdmission); err != nil {
		return headlessAttemptLayout{}, err
	}
	brokerRequestDirectory := ""
	brokerResponseDirectory := ""
	if brokerCLI {
		brokerRequestDirectory = filepath.Join(evalDir, "command-broker-requests")
		brokerResponseDirectory = filepath.Join(evalDir, "command-broker-responses")
		if err := mkdirPrivate(brokerRequestDirectory); err != nil {
			return headlessAttemptLayout{}, err
		}
		if err := mkdirPrivate(brokerResponseDirectory); err != nil {
			return headlessAttemptLayout{}, err
		}
		counterPath = filepath.Join(brokerRequestDirectory, "atl-invocations.jsonl")
	}
	cliResultDirectory := ""
	if directFinalCaptureCLI {
		cliResultDirectory = filepath.Join(evalDir, "cli-results")
		if err := mkdirPrivate(cliResultDirectory); err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	probeExecutablePath := ""
	if guardedBrokerCLI {
		probeExecutablePath = filepath.Join(wrapperDir, confinementProbeName())
		if err := copyExecutionBackendWrapper(bindings.wrapperExecutable, probeExecutablePath, backendAdmission); err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	if contract.spec.EffectiveBackendMode() == BackendModePrivateLive || syntheticBrokerCLI {
		for _, reader := range []string{"cat", "sed", "wc"} {
			if err := copyExecutionBackendWrapper(bindings.wrapperExecutable, filepath.Join(wrapperDir, reader), backendAdmission); err != nil {
				return headlessAttemptLayout{}, err
			}
		}
	}
	if reviewedWriteCLI {
		if err := copyExecutionBackendWrapper(bindings.wrapperExecutable, filepath.Join(wrapperDir, "env"), backendAdmission); err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	settingsPath := filepath.Join(runDir, "claude-settings.json")
	reviewedMCPTools := adapter.reviewedMCPTools(contract.spec)
	if err := writeClaudeGuardSettings(settingsPath, guardPath, mcpServerName(contract.spec), reviewedMCPTools); err != nil {
		return headlessAttemptLayout{}, err
	}
	return headlessAttemptLayout{
		privateCLI: privateCLI, isolatedRuntimeCLI: isolatedRuntimeCLI,
		syntheticBrokerCLI: syntheticBrokerCLI, syntheticBrokerWriteCLI: syntheticBrokerWriteCLI,
		privateLiveWriteCLI: privateLiveWriteCLI, reviewedWriteCLI: reviewedWriteCLI,
		guardedBrokerCLI: guardedBrokerCLI, brokerCLI: brokerCLI, directFinalCaptureCLI: directFinalCaptureCLI,
		gatewayBackedMCP: gatewayBackedMCP,
		runDir:           runDir, workspace: workspace, workspaceAdmissionSHA256: workspaceAdmissionSHA256,
		taskContractSHA256: taskContractSHA256, executionContractSHA256: executionContractSHA256,
		providerResponseSchema: providerResponseSchemaPath,
		finalPath:              finalPath, transcriptPath: transcriptPath, stderrPath: stderrPath,
		evalDir: evalDir, mirrorRoot: mirrorRoot, counterPath: counterPath, guardCounterPath: guardCounterPath,
		wrapperDir: wrapperDir, guardPath: guardPath,
		brokerRequestDirectory: brokerRequestDirectory, brokerResponseDirectory: brokerResponseDirectory,
		cliResultDirectory: cliResultDirectory, probeExecutablePath: probeExecutablePath,
		settingsPath: settingsPath, atlConfigDir: filepath.Join(evalDir, "atl-config"),
		mcpConfigPath:       adapterMCPConfigPath(contract.spec, filepath.Join(runDir, "claude-mcp.json")),
		admittedAgentBinary: bindings.agentBinary, admittedATLBinary: admittedATLBinary,
		admittedPluginRoot: admittedPluginRoot, admittedWrapper: admittedWrapper,
	}, nil
}
