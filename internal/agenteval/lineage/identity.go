package lineage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

type identityProjection struct {
	SkillSHA256       string   `json:"skill_sha256"`
	EvalSHA256        string   `json:"eval_sha256"`
	GraderSHA256      string   `json:"grader_sha256"`
	AgentSHA256       string   `json:"agent_sha256"`
	ModelSHA256       string   `json:"model_sha256"`
	HarnessSHA256     string   `json:"harness_sha256"`
	EnvironmentSHA256 string   `json:"environment_sha256"`
	ToolAPISHA256     string   `json:"tool_api_sha256"`
	DependencySHA256  []string `json:"dependency_sha256"`
}

// SHA256Hex computes an opaque lowercase SHA-256 identity for bytes supplied
// by the caller. The package never retains the bytes.
func SHA256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func (identity RuntimeIdentity) projection() identityProjection {
	return identityProjection{
		SkillSHA256:       identity.SkillSHA256,
		EvalSHA256:        identity.EvalSHA256,
		GraderSHA256:      identity.GraderSHA256,
		AgentSHA256:       identity.AgentSHA256,
		ModelSHA256:       identity.ModelSHA256,
		HarnessSHA256:     identity.HarnessSHA256,
		EnvironmentSHA256: identity.EnvironmentSHA256,
		ToolAPISHA256:     identity.ToolAPISHA256,
		DependencySHA256:  append([]string{}, identity.DependencySHA256...),
	}
}

func cloneIdentity(input RuntimeIdentity) RuntimeIdentity {
	output := input
	output.DependencySHA256 = append([]string{}, input.DependencySHA256...)
	return output
}

func sealIdentity(input RuntimeIdentity) (RuntimeIdentity, error) {
	identity := cloneIdentity(input)
	providedDigest := identity.IdentitySHA256
	if identity.DependencySHA256 == nil {
		identity.DependencySHA256 = []string{}
	}
	sort.Strings(identity.DependencySHA256)
	identity.IdentitySHA256 = ""
	if err := validateIdentityShape(identity, false); err != nil {
		return RuntimeIdentity{}, err
	}
	digest, err := digestProjection("identity", identity.projection())
	if err != nil {
		return RuntimeIdentity{}, fail(ErrorInvalidIdentity)
	}
	if providedDigest != "" && providedDigest != digest {
		return RuntimeIdentity{}, fail(ErrorInvalidIdentity)
	}
	identity.IdentitySHA256 = digest
	if err := validateIdentityShape(identity, true); err != nil {
		return RuntimeIdentity{}, err
	}
	return identity, nil
}

func validateIdentity(identity RuntimeIdentity) error {
	if err := validateIdentityShape(identity, true); err != nil {
		return err
	}
	digest, err := digestProjection("identity", identity.projection())
	if err != nil || digest != identity.IdentitySHA256 {
		return fail(ErrorInvalidIdentity)
	}
	return nil
}

func validateIdentityShape(identity RuntimeIdentity, requireDigest bool) error {
	if !validDigest(identity.SkillSHA256) || !validDigest(identity.EvalSHA256) ||
		!validDigest(identity.GraderSHA256) || !validDigest(identity.AgentSHA256) ||
		!validDigest(identity.ModelSHA256) || !validDigest(identity.HarnessSHA256) ||
		!validDigest(identity.EnvironmentSHA256) || !validDigest(identity.ToolAPISHA256) ||
		identity.DependencySHA256 == nil || len(identity.DependencySHA256) > MaxDependencies ||
		(requireDigest && !validDigest(identity.IdentitySHA256)) ||
		(!requireDigest && identity.IdentitySHA256 != "") {
		return fail(ErrorInvalidIdentity)
	}
	for index, dependency := range identity.DependencySHA256 {
		if !validDigest(dependency) || index > 0 && identity.DependencySHA256[index-1] >= dependency {
			return fail(ErrorInvalidIdentity)
		}
	}
	return nil
}

func dependencySetDigest(identity RuntimeIdentity) string {
	digest, _ := digestProjection("dependency-set", identity.DependencySHA256)
	return digest
}

func digestProjection(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/dataset-lineage/v1\x00"))
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validDigest(value string) bool {
	if len(value) != SHA256HexLength {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}
