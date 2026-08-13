package agenteval

import (
	"fmt"
	"io"

	"github.com/isukharev/atl/internal/agenteval/agentadapter"
)

const (
	AgentAdapterContractSchema      = agentadapter.ContractSchema
	AgentAdapterObservationSchema   = agentadapter.ObservationSchema
	AgentAdapterSchemaVersion       = agentadapter.SchemaVersion
	AgentAdapterContractMaxBytes    = agentadapter.MaxContractBytes
	AgentAdapterObservationMaxBytes = agentadapter.MaxObservationBytes
)

type AgentAdapterContract = agentadapter.Contract
type AgentAdapterObservation = agentadapter.Observation
type AgentAdapterEvent = agentadapter.Event

func DecodeAgentAdapterContract(reader io.Reader) (AgentAdapterContract, error) {
	return agentadapter.DecodeContract(reader)
}

func EncodeAgentAdapterContract(contract AgentAdapterContract) ([]byte, error) {
	return agentadapter.EncodeContract(contract)
}

func DecodeAgentAdapterObservation(reader io.Reader, contract AgentAdapterContract) (AgentAdapterObservation, error) {
	return agentadapter.DecodeObservation(reader, contract)
}

func EncodeAgentAdapterObservation(contract AgentAdapterContract, observation AgentAdapterObservation) ([]byte, error) {
	return agentadapter.EncodeObservation(contract, observation)
}

func AgentAdapterContractSHA256(contract AgentAdapterContract) (string, error) {
	return agentadapter.ContractSHA256(contract)
}

func AgentAdapterObservationSHA256(contract AgentAdapterContract, observation AgentAdapterObservation) (string, error) {
	return agentadapter.ObservationSHA256(contract, observation)
}

func SequentialReferenceAgentAdapterContract() (AgentAdapterContract, error) {
	return agentadapter.ReferenceContract()
}

func builtInAgentAdapterContract(spec RunSpec, agentSHA256 string) (agentadapter.Contract, string, error) {
	adapter, err := builtInAgentAdapterFor(spec.Provider)
	if err != nil {
		return agentadapter.Contract{}, "", err
	}
	configurationSHA256, err := contentMinimizedAttemptDigest("agent-adapter-configuration", struct {
		Provider        string `json:"provider"`
		Model           string `json:"model"`
		Reasoning       string `json:"reasoning"`
		BackendMode     string `json:"backend_mode"`
		Surface         string `json:"surface"`
		ToolTransport   string `json:"tool_transport"`
		SkillActivation string `json:"skill_activation,omitempty"`
	}{spec.Provider, spec.Model, spec.Reasoning, spec.EffectiveBackendMode(), spec.EffectiveSurface(), spec.EffectiveToolTransport(), spec.SkillActivationIdentity()})
	if err != nil {
		return agentadapter.Contract{}, "", err
	}
	sourceSHA256, err := contentMinimizedAttemptDigest("agent-adapter-source", []string{adapter.id(), agentadapter.ContractVersion, "built-in-v1"})
	if err != nil {
		return agentadapter.Contract{}, "", err
	}
	contract, err := agentadapter.NewContract(adapter.id(), "built-in-v1", sourceSHA256, agentSHA256, configurationSHA256,
		adapter.capabilitySupport(), []agentadapter.ConfigurationKey{{Name: "backend_mode"}, {Name: "model"}, {Name: "reasoning"}, {Name: "skill_activation"}, {Name: "surface"}, {Name: "tool_transport"}})
	if err != nil {
		return agentadapter.Contract{}, "", err
	}
	if err := admitAgentAdapterCapabilities(contract, requiredAgentAdapterCapabilities(spec)); err != nil {
		return agentadapter.Contract{}, "", err
	}
	digest, err := agentadapter.ContractSHA256(contract)
	if err != nil {
		return agentadapter.Contract{}, "", err
	}
	return contract, digest, nil
}

func requiredAgentAdapterCapabilities(spec RunSpec) []agentadapter.CapabilityID {
	required := []agentadapter.CapabilityID{agentadapter.CapabilityCancellation, agentadapter.CapabilityCost,
		agentadapter.CapabilityParentUsage, agentadapter.CapabilityPermissionPolicy, agentadapter.CapabilityProcessTree,
		agentadapter.CapabilitySandbox, agentadapter.CapabilitySingle, agentadapter.CapabilityTrajectory}
	if spec.EffectiveToolTransport() == "mcp" {
		required = append(required, agentadapter.CapabilityMCP)
	} else {
		required = append(required, agentadapter.CapabilityLocalExecution)
	}
	switch spec.SkillActivationIdentity() {
	case SkillActivationImplicit:
		required = append(required, agentadapter.CapabilityActivationEvidence, agentadapter.CapabilityActivationNative)
	case SkillActivationExplicit:
		required = append(required, agentadapter.CapabilityActivationEvidence, agentadapter.CapabilityActivationForcedInjection)
	case SkillActivationDeveloper:
		required = append(required, agentadapter.CapabilityActivationDeveloperInstructions, agentadapter.CapabilityActivationEvidence)
	case SkillActivationCombined:
		required = append(required, agentadapter.CapabilityActivationDeveloperInstructions, agentadapter.CapabilityActivationEvidence,
			agentadapter.CapabilityActivationForcedInjection)
	}
	return required
}

func admitAgentAdapterCapabilities(contract agentadapter.Contract, required []agentadapter.CapabilityID) error {
	claims := make(map[agentadapter.CapabilityID]agentadapter.Support, len(contract.Capabilities))
	for _, capability := range contract.Capabilities {
		claims[capability.ID] = capability.Support
	}
	for _, capability := range required {
		if claims[capability] != agentadapter.SupportSupported {
			return fmt.Errorf("agent adapter capability %q is %s", capability, claims[capability])
		}
	}
	return nil
}

func agentAdapterContractForAttempt(spec RunSpec, session *DurableAttemptSession) (*AgentAdapterContract, error) {
	if session == nil {
		return nil, nil
	}
	contract, digest, err := builtInAgentAdapterContract(spec, session.plan.Binding.Identity.AgentSHA256)
	if err != nil || digest != session.plan.Binding.Identity.AdapterSHA256 {
		return nil, fmt.Errorf("agent adapter attempt binding changed")
	}
	return &contract, nil
}
