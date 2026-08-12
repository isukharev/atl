package agenteval

import "github.com/isukharev/atl/internal/agenteval/lifecycle"

func runAttemptBinding(contract resolvedRunContract, options RunOptions, skillDigest string) (lifecycle.Binding, error) {
	agentDigest, err := digestSyntheticExecutable(options.AgentBinary, privateAgentBinaryMaxBytes)
	if err != nil {
		return lifecycle.Binding{}, err
	}
	atlDigest, err := digestSyntheticExecutable(options.ATLBinary, privateAgentBinaryMaxBytes)
	if err != nil {
		return lifecycle.Binding{}, err
	}
	wrapperDigest, err := digestSyntheticExecutable(options.WrapperExecutable, 128<<20)
	if err != nil {
		return lifecycle.Binding{}, err
	}
	if !validSHA256(skillDigest) {
		skillDigest, err = contentMinimizedAttemptDigest("skill-identity", skillDigest)
		if err != nil {
			return lifecycle.Binding{}, err
		}
	}
	digest := func(domain string, value any) (string, error) { return contentMinimizedAttemptDigest(domain, value) }
	experiment, err := digest("experiment", struct {
		Spec     RunSpec  `json:"spec"`
		Scenario Scenario `json:"scenario"`
	}{contract.spec, contract.scenario})
	if err != nil {
		return lifecycle.Binding{}, err
	}
	task, err := digest("task", struct {
		Scenario              Scenario `json:"scenario"`
		PromptSHA256          string   `json:"prompt_sha256"`
		ProviderPromptSHA256  string   `json:"provider_prompt_sha256"`
		ResponseSchemaSHA256  string   `json:"response_schema_sha256"`
		WorkspaceTemplateHash string   `json:"workspace_template_sha256"`
	}{contract.scenario, sha256HexBytes(contract.prompt), sha256HexBytes(contract.providerPrompt),
		sha256HexBytes(contract.responseSchema), sha256HexBytes([]byte(contract.workspaceTemplate))})
	if err != nil {
		return lifecycle.Binding{}, err
	}
	model, err := digest("model", []string{contract.spec.Provider, contract.spec.Model, contract.spec.Reasoning})
	if err != nil {
		return lifecycle.Binding{}, err
	}
	environment, err := digest("environment", struct {
		BackendMode   string `json:"backend_mode"`
		Surface       string `json:"surface"`
		ToolTransport string `json:"tool_transport"`
		ATL           string `json:"atl_sha256"`
		Wrapper       string `json:"wrapper_sha256"`
	}{contract.spec.EffectiveBackendMode(), contract.spec.EffectiveSurface(), contract.spec.EffectiveToolTransport(), atlDigest, wrapperDigest})
	if err != nil {
		return lifecycle.Binding{}, err
	}
	grader, err := digest("grader", contract.rubric)
	if err != nil {
		return lifecycle.Binding{}, err
	}
	budgets, err := digest("budgets", struct {
		Scenario Budgets `json:"scenario"`
		Cost     int64   `json:"max_estimated_cost_microusd"`
		Timeout  int     `json:"timeout_seconds"`
	}{contract.scenario.Budgets, contract.spec.MaxEstimatedCostMicroUSD, contract.spec.TimeoutSeconds})
	if err != nil {
		return lifecycle.Binding{}, err
	}
	_, adapter, err := builtInAgentAdapterContract(contract.spec, agentDigest)
	if err != nil {
		return lifecycle.Binding{}, err
	}
	authority, err := digest("authority", struct {
		Surface                     string                        `json:"surface"`
		BackendMode                 string                        `json:"backend_mode"`
		ToolTransport               string                        `json:"tool_transport"`
		AllowedTools                []string                      `json:"allowed_tools"`
		AllowedATLCommands          []string                      `json:"allowed_atl_commands"`
		AllowedCLICommands          []CLICommandRule              `json:"allowed_cli_commands"`
		AllowedMCPTools             []string                      `json:"allowed_mcp_tools"`
		DataCapabilities            []string                      `json:"data_capabilities"`
		AllowedGatewayRoutes        map[string][]LiveGatewayRoute `json:"allowed_gateway_routes"`
		GatewayMaxResponseBytes     int64                         `json:"gateway_max_response_bytes"`
		GatewayMaxTotalBytes        int64                         `json:"gateway_max_total_response_bytes"`
		GatewayMaxRequestBytes      int64                         `json:"gateway_max_request_bytes"`
		GatewayMaxTotalRequestBytes int64                         `json:"gateway_max_total_request_bytes"`
		AllowSyntheticWrites        bool                          `json:"allow_synthetic_writes"`
		AllowLiveWrites             bool                          `json:"allow_live_writes"`
	}{
		Surface: contract.spec.EffectiveSurface(), BackendMode: contract.spec.EffectiveBackendMode(),
		ToolTransport: contract.spec.EffectiveToolTransport(), AllowedTools: contract.spec.AllowedTools,
		AllowedATLCommands: contract.spec.AllowedATLCommands, AllowedCLICommands: contract.spec.AllowedCLICommands,
		AllowedMCPTools: contract.spec.AllowedMCPTools, DataCapabilities: contract.spec.DataCapabilities,
		AllowedGatewayRoutes: contract.spec.AllowedGatewayRoutes, GatewayMaxResponseBytes: contract.spec.GatewayMaxResponseBytes,
		GatewayMaxTotalBytes: contract.spec.GatewayMaxTotalBytes, GatewayMaxRequestBytes: contract.spec.GatewayMaxRequestBytes,
		GatewayMaxTotalRequestBytes: contract.spec.GatewayMaxTotalRequestBytes,
		AllowSyntheticWrites:        contract.spec.AllowSyntheticWrites, AllowLiveWrites: contract.spec.AllowLiveWrites,
	})
	if err != nil {
		return lifecycle.Binding{}, err
	}
	privacy := lifecycle.PrivacyContentMinimized
	if contract.spec.EffectiveBackendMode() == BackendModePrivateLive {
		privacy = lifecycle.PrivacyOwnerPrivate
	}
	return lifecycle.Binding{Privacy: privacy, Identity: lifecycle.Identity{
		ExperimentSHA256: experiment, TaskSHA256: task, SkillSHA256: skillDigest, AgentSHA256: agentDigest,
		ModelSHA256: model, EnvironmentSHA256: environment, GraderSHA256: grader, BudgetsSHA256: budgets, AdapterSHA256: adapter,
		AuthoritySHA256: authority,
	}}, nil
}
