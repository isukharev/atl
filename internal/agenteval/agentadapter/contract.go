// Package agentadapter owns the provider-neutral agent adapter contract.
package agentadapter

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

const (
	ContractSchema       = "agent-eval/agent-adapter-contract"
	ObservationSchema    = "agent-eval/agent-observation"
	SchemaVersion        = 1
	ContractVersion      = "0.1.0-pre-release"
	MaxContractBytes     = 64 << 10
	MaxObservationBytes  = 1 << 20
	MaxCapabilities      = 32
	MaxConfigurationKeys = 64
	MaxEvents            = 256
	MaxNodes             = 32
	MaxDepth             = 2
	MaxIdentifierBytes   = 128
)

var ErrContract = errors.New("agent_adapter_contract_invalid")

type Support string

const (
	SupportNotApplicable Support = "not_applicable"
	SupportSupported     Support = "supported"
	SupportUnknown       Support = "unknown"
	SupportUnsupported   Support = "unsupported"
)

type CapabilityID string

const (
	CapabilityActivationDeveloperInstructions CapabilityID = "activation.developer_instructions"
	CapabilityActivationEvidence              CapabilityID = "activation.evidence"
	CapabilityActivationForcedInjection       CapabilityID = "activation.forced_injection"
	CapabilityActivationNative                CapabilityID = "activation.native"
	CapabilityCancellation                    CapabilityID = "execution.cancellation"
	CapabilityLocalExecution                  CapabilityID = "execution.local"
	CapabilityPermissionPolicy                CapabilityID = "execution.permission_policy"
	CapabilityProcessTree                     CapabilityID = "execution.process_tree"
	CapabilitySandbox                         CapabilityID = "execution.sandbox"
	CapabilityMCP                             CapabilityID = "tools.mcp"
	CapabilityTrajectory                      CapabilityID = "trajectory.events"
	CapabilityCost                            CapabilityID = "usage.cost"
	CapabilityParentUsage                     CapabilityID = "usage.parent"
	CapabilityTreeUsage                       CapabilityID = "usage.tree"
	CapabilityGenericChild                    CapabilityID = "orchestration.generic_child"
	CapabilityParallelChildren                CapabilityID = "orchestration.parallel_children"
	CapabilitySingle                          CapabilityID = "orchestration.single"
	CapabilitySpecializedChildren             CapabilityID = "orchestration.specialized_children"
)

var closedCapabilities = []CapabilityID{
	CapabilityActivationDeveloperInstructions,
	CapabilityActivationEvidence,
	CapabilityActivationForcedInjection,
	CapabilityActivationNative,
	CapabilityCancellation,
	CapabilityLocalExecution,
	CapabilityPermissionPolicy,
	CapabilityProcessTree,
	CapabilitySandbox,
	CapabilityGenericChild,
	CapabilityParallelChildren,
	CapabilitySingle,
	CapabilitySpecializedChildren,
	CapabilityMCP,
	CapabilityTrajectory,
	CapabilityCost,
	CapabilityParentUsage,
	CapabilityTreeUsage,
}

type Capability struct {
	ID      CapabilityID `json:"id"`
	Support Support      `json:"support"`
}

type ConfigurationKey struct {
	Name      string `json:"name"`
	Sensitive bool   `json:"sensitive"`
}

// Contract binds one selected adapter implementation and its closed capability
// claims. It stores identities and digests only, never argv, environment,
// prompts, credentials, or private paths.
type Contract struct {
	Schema              string             `json:"schema"`
	SchemaVersion       int                `json:"schema_version"`
	ContractVersion     string             `json:"contract_version"`
	AdapterID           string             `json:"adapter_id"`
	AdapterVersion      string             `json:"adapter_version"`
	SourceSHA256        string             `json:"source_sha256"`
	ExecutableSHA256    string             `json:"executable_sha256"`
	ConfigurationSHA256 string             `json:"configuration_sha256"`
	Capabilities        []Capability       `json:"capabilities"`
	ConfigurationKeys   []ConfigurationKey `json:"configuration_keys"`
}

func Capabilities() []CapabilityID { return append([]CapabilityID(nil), closedCapabilities...) }

func ValidateContract(contract Contract) error {
	if contract.Schema != ContractSchema || contract.SchemaVersion != SchemaVersion ||
		contract.ContractVersion != ContractVersion || !validIdentifier(contract.AdapterID) ||
		!validVersion(contract.AdapterVersion) || !validSHA256(contract.SourceSHA256) ||
		!validSHA256(contract.ExecutableSHA256) || !validSHA256(contract.ConfigurationSHA256) ||
		len(contract.Capabilities) != len(closedCapabilities) || contract.ConfigurationKeys == nil ||
		len(contract.ConfigurationKeys) > MaxConfigurationKeys {
		return contractError("shape")
	}
	for index, capability := range contract.Capabilities {
		if capability.ID != closedCapabilities[index] || !capability.Support.valid() {
			return contractError("capabilities")
		}
	}
	for index, key := range contract.ConfigurationKeys {
		if !validIdentifier(key.Name) || index > 0 && contract.ConfigurationKeys[index-1].Name >= key.Name {
			return contractError("configuration_keys")
		}
	}
	if contract.AdapterID == referenceAdapterID && !referenceContractMatches(contract) {
		return contractError("reference_identity")
	}
	return nil
}

func NewContract(id, version, sourceSHA256, executableSHA256, configurationSHA256 string, support map[CapabilityID]Support, keys []ConfigurationKey) (Contract, error) {
	capabilities := make([]Capability, len(closedCapabilities))
	for index, id := range closedCapabilities {
		state, ok := support[id]
		if !ok {
			state = SupportUnknown
		}
		capabilities[index] = Capability{ID: id, Support: state}
	}
	keys = append([]ConfigurationKey{}, keys...)
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	contract := Contract{Schema: ContractSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		AdapterID: id, AdapterVersion: version, SourceSHA256: sourceSHA256, ExecutableSHA256: executableSHA256,
		ConfigurationSHA256: configurationSHA256, Capabilities: capabilities, ConfigurationKeys: keys}
	if err := ValidateContract(contract); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func ContractSHA256(contract Contract) (string, error) {
	data, err := EncodeContract(contract)
	if err != nil {
		return "", err
	}
	return hashDomain("contract", data), nil
}

func (support Support) valid() bool {
	return support == SupportNotApplicable || support == SupportSupported || support == SupportUnknown || support == SupportUnsupported
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > MaxIdentifierBytes {
		return false
	}
	for index, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || index > 0 && (char == '-' || char == '.' || char == '_' || char == '/') {
			continue
		}
		return false
	}
	return true
}

func validVersion(value string) bool {
	if len(value) == 0 || len(value) > MaxIdentifierBytes {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e || char == '/' || char == '\\' {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func hashDomain(domain string, data []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/agent-adapter/" + domain + "/v1\x00"))
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func contractError(code string) error { return fmt.Errorf("%w: %s", ErrContract, code) }
