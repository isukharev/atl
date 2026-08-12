package agenteval

import (
	"fmt"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/agenteval/executionbackend"
)

func localExecutionBackendTrialPlan(contract resolvedRunContract, skillDigest, atlDigest, wrapperDigest string) (executionbackend.Contract, executionbackend.Plan, string, error) {
	var err error
	skillDigest, err = normalizedRunSkillDigest(skillDigest)
	if err != nil {
		return executionbackend.Contract{}, executionbackend.Plan{}, "", err
	}
	if !validSHA256(skillDigest) || !validSHA256(atlDigest) || !validSHA256(wrapperDigest) {
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
		Spec                 RunSpec  `json:"spec"`
		Scenario             Scenario `json:"scenario"`
		PromptSHA256         string   `json:"prompt_sha256"`
		ProviderPromptSHA256 string   `json:"provider_prompt_sha256"`
		ResponseSHA256       string   `json:"response_sha256"`
	}{contract.spec, contract.scenario, sha256HexBytes(contract.prompt), sha256HexBytes(contract.providerPrompt), sha256HexBytes(contract.responseSchema)})
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

func localExecutionBackendPlanForAttempt(contract resolvedRunContract, bindings runAttemptBindings) error {
	if bindings.attemptSession == nil {
		return nil
	}
	atlDigest, err := digestSyntheticExecutable(bindings.atlBinary, privateAgentBinaryMaxBytes)
	if err != nil {
		return err
	}
	wrapperDigest, err := digestSyntheticExecutable(bindings.wrapperExecutable, 128<<20)
	if err != nil {
		return err
	}
	_, _, digest, err := localExecutionBackendTrialPlan(contract, bindings.runtime.SkillDigest, atlDigest, wrapperDigest)
	if err != nil || digest != bindings.attemptSession.plan.Binding.Identity.EnvironmentSHA256 {
		return fmt.Errorf("execution backend attempt binding changed")
	}
	return nil
}

func normalizedRunSkillDigest(value string) (string, error) {
	if validSHA256(value) {
		return value, nil
	}
	return contentMinimizedAttemptDigest("skill-identity", value)
}
