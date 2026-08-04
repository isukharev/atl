package agenteval

import (
	"bytes"
	"fmt"
	"path/filepath"
)

type headlessAttemptLayout struct {
	privateCLI              bool
	codexPrivateCLI         bool
	codexSyntheticBrokerCLI bool
	codexSyntheticWriteCLI  bool
	privateLiveWriteCLI     bool
	reviewedWriteCLI        bool
	codexBrokerCLI          bool
	brokerCLI               bool
	claudePrivateCLI        bool
	gatewayBackedMCP        bool
	runDir                  string
	workspace               string
	taskContractSHA256      string
	executionContractSHA256 string
	providerResponseSchema  string
	finalPath               string
	transcriptPath          string
	stderrPath              string
	evalDir                 string
	mirrorRoot              string
	counterPath             string
	guardCounterPath        string
	wrapperDir              string
	guardPath               string
	brokerRequestDirectory  string
	brokerResponseDirectory string
	cliResultDirectory      string
	probeExecutablePath     string
	settingsPath            string
	atlConfigDir            string
	mcpConfigPath           string
}

func prepareHeadlessAttemptLayout(contract resolvedRunContract, bindings runAttemptBindings) (headlessAttemptLayout, error) {
	privateCLI := contract.spec.EffectiveBackendMode() == BackendModePrivateLive && contract.spec.EffectiveToolTransport() == "cli"
	codexPrivateCLI := contract.spec.Provider == "codex" && privateCLI
	codexSyntheticBrokerCLI := isCodexSyntheticBrokerCLI(contract.spec)
	codexSyntheticWriteCLI := codexSyntheticBrokerCLI && contract.spec.AllowSyntheticWrites
	privateLiveWriteCLI := privateCLI && contract.spec.AllowLiveWrites
	reviewedWriteCLI := codexSyntheticWriteCLI || privateLiveWriteCLI
	codexBrokerCLI := codexPrivateCLI || codexSyntheticBrokerCLI
	brokerCLI := privateCLI || codexSyntheticBrokerCLI
	claudePrivateCLI := contract.spec.Provider == "claude-code" && privateCLI
	gatewayBackedMCP := gatewayBackedInternalMCP(contract.spec)
	var err error
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
	taskContractSHA256 := ""
	if bindings.attestation != nil {
		taskContractSHA256, err = syntheticTaskContractSHA256(contract, workspace)
		if err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	if shouldInstallCodexBenchmarkSkills(contract.spec) {
		_, skillRoot, err := providerPluginLayout(bindings.pluginRoot, contract.spec.Provider)
		if err != nil {
			return headlessAttemptLayout{}, err
		}
		if err := copyWorkspace(skillRoot, filepath.Join(workspace, ".agents", "skills")); err != nil {
			return headlessAttemptLayout{}, fmt.Errorf("install benchmark skills: %w", err)
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
	evalDir := filepath.Join(runDir, ".atl-eval")
	if err := mkdirPrivate(evalDir); err != nil {
		return headlessAttemptLayout{}, err
	}
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
	if err := copyExecutable(bindings.wrapperExecutable, filepath.Join(wrapperDir, wrapperName())); err != nil {
		return headlessAttemptLayout{}, err
	}
	guardPath := filepath.Join(wrapperDir, guardName())
	if err := copyExecutable(bindings.wrapperExecutable, guardPath); err != nil {
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
	if claudePrivateCLI {
		cliResultDirectory = filepath.Join(evalDir, "cli-results")
		if err := mkdirPrivate(cliResultDirectory); err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	probeExecutablePath := ""
	if codexBrokerCLI {
		probeExecutablePath = filepath.Join(wrapperDir, confinementProbeName())
		if err := copyExecutable(bindings.wrapperExecutable, probeExecutablePath); err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	if contract.spec.EffectiveBackendMode() == BackendModePrivateLive || codexSyntheticBrokerCLI {
		for _, reader := range []string{"cat", "sed", "wc"} {
			if err := copyExecutable(bindings.wrapperExecutable, filepath.Join(wrapperDir, reader)); err != nil {
				return headlessAttemptLayout{}, err
			}
		}
	}
	if reviewedWriteCLI {
		if err := copyExecutable(bindings.wrapperExecutable, filepath.Join(wrapperDir, "env")); err != nil {
			return headlessAttemptLayout{}, err
		}
	}
	settingsPath := filepath.Join(runDir, "claude-settings.json")
	var reviewedMCPTools []string
	if contract.spec.Provider == "claude-code" && contract.spec.ToolTransport == "mcp" {
		reviewedMCPTools = claudeMCPToolNamesForServer(mcpServerName(contract.spec), contract.spec.AllowedMCPTools)
	}
	if err := writeClaudeGuardSettings(settingsPath, guardPath, mcpServerName(contract.spec), reviewedMCPTools); err != nil {
		return headlessAttemptLayout{}, err
	}
	return headlessAttemptLayout{
		privateCLI: privateCLI, codexPrivateCLI: codexPrivateCLI,
		codexSyntheticBrokerCLI: codexSyntheticBrokerCLI, codexSyntheticWriteCLI: codexSyntheticWriteCLI,
		privateLiveWriteCLI: privateLiveWriteCLI, reviewedWriteCLI: reviewedWriteCLI,
		codexBrokerCLI: codexBrokerCLI, brokerCLI: brokerCLI, claudePrivateCLI: claudePrivateCLI,
		gatewayBackedMCP: gatewayBackedMCP,
		runDir:           runDir, workspace: workspace,
		taskContractSHA256: taskContractSHA256, executionContractSHA256: executionContractSHA256,
		providerResponseSchema: providerResponseSchemaPath,
		finalPath:              finalPath, transcriptPath: transcriptPath, stderrPath: stderrPath,
		evalDir: evalDir, mirrorRoot: mirrorRoot, counterPath: counterPath, guardCounterPath: guardCounterPath,
		wrapperDir: wrapperDir, guardPath: guardPath,
		brokerRequestDirectory: brokerRequestDirectory, brokerResponseDirectory: brokerResponseDirectory,
		cliResultDirectory: cliResultDirectory, probeExecutablePath: probeExecutablePath,
		settingsPath: settingsPath, atlConfigDir: filepath.Join(evalDir, "atl-config"),
		mcpConfigPath: claudeMCPConfigPath(contract.spec, filepath.Join(runDir, "claude-mcp.json")),
	}, nil
}
