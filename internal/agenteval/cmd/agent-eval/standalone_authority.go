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
	Operation string `json:"operation"`
	Mode      string `json:"mode"`
	Authority string `json:"authority"`
	standaloneAuthorityDimensions
}

// standaloneAuthorityProfiles is a fresh projection of the frozen standalone
// product contract. Preview and explain output never infer authority from the
// implementation path that happened to run.
func standaloneAuthorityProfiles() []standaloneAuthorityProfile {
	return []standaloneAuthorityProfile{
		{Operation: "capabilities", Mode: "default", Authority: "none"},
		{Operation: "compare", Mode: "default", Authority: "local_read", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "compat verify", Mode: "provider-free", Authority: "verifier_execution", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, ProcessSpawn: true}},
		{Operation: "export", Mode: "agent-skills", Authority: "local_write", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true}},
		{Operation: "grade", Mode: "deterministic", Authority: "verifier_execution", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, ProcessSpawn: true}},
		{Operation: "grade", Mode: "judge", Authority: "provider_execution", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, ProcessSpawn: true, ProviderContact: true, Network: true, CredentialAccess: true}},
		{Operation: "import", Mode: "agent-skills", Authority: "local_read", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "import", Mode: "default", Authority: "local_write", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true}},
		{Operation: "init", Mode: "default", Authority: "local_write", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalWrite: true, PrivateWorkspaceAccess: true}},
		{Operation: "inspect", Mode: "default", Authority: "local_read", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "migrate apply", Mode: "default", Authority: "local_write", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true, PrivateWorkspaceAccess: true}},
		{Operation: "migrate preview", Mode: "default", Authority: "local_read", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, PrivateWorkspaceAccess: true}},
		{Operation: "plan", Mode: "default", Authority: "local_write", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true, PrivateWorkspaceAccess: true}},
		{Operation: "reconcile", Mode: "evidence-only", Authority: "local_write", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true, PrivateWorkspaceAccess: true}},
		{Operation: "report", Mode: "default", Authority: "local_read", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "resume", Mode: "default", Authority: "agent_execution", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true, ProcessSpawn: true, ProviderContact: true, BackendContact: true, Network: true, CredentialAccess: true, PrivateWorkspaceAccess: true}},
		{Operation: "run", Mode: "default", Authority: "agent_execution", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true, LocalWrite: true, ProcessSpawn: true, ProviderContact: true, BackendContact: true, Network: true, CredentialAccess: true}},
		{Operation: "schema inspect", Mode: "default", Authority: "local_read", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "validate", Mode: "default", Authority: "local_read", standaloneAuthorityDimensions: standaloneAuthorityDimensions{LocalRead: true}},
		{Operation: "version", Mode: "default", Authority: "none"},
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
