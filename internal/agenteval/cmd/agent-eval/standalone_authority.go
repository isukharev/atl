package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sort"
)

type standaloneAuthorityDimensions struct {
	LocalRead              bool `json:"local_read"`
	LocalWrite             bool `json:"local_write"`
	ProcessSpawn           bool `json:"process_spawn"`
	ProviderContact        bool `json:"provider_contact"`
	BackendContact         bool `json:"backend_contact"`
	Network                bool `json:"network"`
	CredentialAccess       bool `json:"credential_access"`
	PrivateWorkspaceAccess bool `json:"private_workspace_access"`
}

type standaloneAuthorityProfile struct {
	Operation  string   `json:"operation"`
	Mode       string   `json:"mode"`
	Authority  string   `json:"authority"`
	Command    string   `json:"-"`
	Supported  bool     `json:"-"`
	ProcessAPI bool     `json:"-"`
	Formats    []string `json:"-"`
	standaloneAuthorityDimensions
}

// standaloneAuthorityProfiles is the canonical executable-operation registry.
// Help, routing, capabilities, Process API admission, preview, and explain
// output project fresh values from this one authority ceiling instead of
// inferring authority from the implementation path that happened to run.
func standaloneAuthorityProfiles() []standaloneAuthorityProfile {
	return []standaloneAuthorityProfile{
		{Operation: "capabilities", Mode: "default", Command: "capabilities", Authority: "none", Supported: true, ProcessAPI: true},
		{Operation: "compare", Mode: "default", Command: "compare", Authority: "local_read", Supported: true, ProcessAPI: true, standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "compat verify", Mode: "provider-free", Command: "compat verify", Authority: "verifier_execution", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, ProcessSpawn: true}},
		{Operation: "export", Mode: "agent-skills", Command: "export agent-skills", Authority: "local_write", Supported: true, Formats: []string{standaloneAgentSkillsVariantGuide, standaloneAgentSkillsVariantAnthropic}, standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true}},
		{Operation: "grade", Mode: "deterministic", Command: "grade", Authority: "verifier_execution", Supported: true, standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, ProcessSpawn: true}},
		{Operation: "grade", Mode: "judge", Command: "grade", Authority: "provider_execution", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, ProcessSpawn: true, ProviderContact: true, Network: true, CredentialAccess: true}},
		{Operation: "import", Mode: "agent-skills", Command: "import agent-skills", Authority: "local_read", Supported: true, Formats: []string{standaloneAgentSkillsVariantAuto, standaloneAgentSkillsVariantGuide, standaloneAgentSkillsVariantAnthropic}, standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "import", Mode: "default", Authority: "local_write", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true}},
		{Operation: "init", Mode: "default", Command: "init", Authority: "local_write", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalWrite: true, PrivateWorkspaceAccess: true}},
		{Operation: "inspect", Mode: "default", Command: "inspect", Authority: "local_read", Supported: true, ProcessAPI: true, standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "migrate apply", Mode: "default", Command: "migrate apply", Authority: "local_write", Supported: true, ProcessAPI: true, standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true, PrivateWorkspaceAccess: true}},
		{Operation: "migrate preview", Mode: "default", Command: "migrate preview", Authority: "local_read", Supported: true, ProcessAPI: true, standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, PrivateWorkspaceAccess: true}},
		{Operation: "plan", Mode: "default", Command: "plan", Authority: "local_write", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true, PrivateWorkspaceAccess: true}},
		{Operation: "reconcile", Mode: "evidence-only", Command: "reconcile", Authority: "local_write", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true, PrivateWorkspaceAccess: true}},
		{Operation: "report", Mode: "default", Command: "report", Authority: "local_read", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "resume", Mode: "default", Command: "resume", Authority: "agent_execution", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true, ProcessSpawn: true, ProviderContact: true, BackendContact: true, Network: true, CredentialAccess: true, PrivateWorkspaceAccess: true}},
		{Operation: "run", Mode: "default", Command: "run", Authority: "agent_execution", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true, ProcessSpawn: true, ProviderContact: true, BackendContact: true, Network: true, CredentialAccess: true}},
		{Operation: "schema inspect", Mode: "default", Command: "schema inspect", Authority: "local_read", Supported: true, ProcessAPI: true, standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "validate", Mode: "default", Command: "validate", Authority: "local_read", Supported: true, ProcessAPI: true, standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "version", Mode: "default", Command: "version", Authority: "none", Supported: true, ProcessAPI: true},
	}
}

func standaloneAuthorityProfileFor(operation, mode string) (standaloneAuthorityProfile, bool) {
	for _, profile := range standaloneAuthorityProfiles() {
		if profile.Operation == operation && profile.Mode == mode {
			return profile, true
		}
	}
	return standaloneAuthorityProfile{}, false
}

func standaloneCommandRegistryState(command string) (available, processAPI bool, found bool) {
	for _, profile := range standaloneAuthorityProfiles() {
		if profile.Command != command {
			continue
		}
		found = true
		available = available || profile.Supported
		processAPI = processAPI || profile.Supported && profile.ProcessAPI
	}
	return available, processAPI, found
}

func standaloneOperationModes(operation string, supported bool) []string {
	result := make([]string, 0)
	for _, profile := range standaloneAuthorityProfiles() {
		if profile.Operation == operation && profile.Supported == supported && profile.Mode != "default" {
			result = append(result, profile.Mode)
		}
	}
	sort.Strings(result)
	return result
}

func standaloneOperationFormats(operation, mode string) []string {
	profile, ok := standaloneAuthorityProfileFor(operation, mode)
	if !ok || !profile.Supported {
		return nil
	}
	return append([]string(nil), profile.Formats...)
}

type standaloneInputEvidence struct {
	Kind           string `json:"kind"`
	Count          int    `json:"count"`
	IdentitySHA256 string `json:"identity_sha256"`
}

type standaloneCapabilityEvidence struct {
	OperationState    string `json:"operation_state"`
	RequiredCount     int    `json:"required_count"`
	RequiredSHA256    string `json:"required_sha256"`
	ProviderExecution bool   `json:"provider_execution"`
	BackendExecution  bool   `json:"backend_execution"`
	GraderExecution   bool   `json:"grader_execution"`
}

type standaloneResolutionEvidence struct {
	Inputs       standaloneInputEvidence      `json:"inputs"`
	Capabilities standaloneCapabilityEvidence `json:"capabilities"`
}

func standaloneNewResolutionEvidence(operation, mode, kind string, count int, identities, capabilities []string) standaloneResolutionEvidence {
	capabilities = standaloneSortedUnique(capabilities)
	return standaloneResolutionEvidence{
		Inputs: standaloneInputEvidence{
			Kind:           kind,
			Count:          count,
			IdentitySHA256: standaloneEvidenceDigest("input", operation, mode, kind, identities),
		},
		Capabilities: standaloneCapabilityEvidence{
			OperationState: "supported",
			RequiredCount:  len(capabilities),
			RequiredSHA256: standaloneEvidenceDigest("capability", operation, mode, kind, capabilities),
		},
	}
}

func standaloneEvidenceDigest(class, operation, mode, kind string, values []string) string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	digest := sha256.New()
	_, _ = digest.Write([]byte("agent-eval/preview-evidence/v1\x00"))
	for _, value := range append([]string{class, operation, mode, kind}, values...) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(value))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func standaloneSemanticIdentity(kind string, value any) (string, error) {
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("agent-eval/semantic-input/v1\x00"))
	for _, value := range [][]byte{[]byte(kind), canonical} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write(value)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func standaloneSortedUnique(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
