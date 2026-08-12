package executionbackend

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func (support Support) valid() bool {
	return support == SupportNotApplicable || support == SupportSupported || support == SupportUnknown || support == SupportUnsupported
}
func (assurance Assurance) valid() bool {
	return assurance == AssuranceHermeticReference || assurance == AssuranceIsolatedDeclaredGaps || assurance == AssuranceLocalProcess
}
func (privacy ArtifactPrivacy) valid() bool {
	return privacy == PrivacyContentMinimized || privacy == PrivacyOwnerPrivate || privacy == PrivacyPublic
}
func (presence Presence) valid() bool {
	return presence == PresenceNotApplicable || presence == PresenceObserved || presence == PresenceUnknown || presence == PresenceUnsupported
}
func (verdict Verdict) valid() bool {
	return verdict == VerdictFailed || verdict == VerdictNotApplicable || verdict == VerdictSucceeded || verdict == VerdictUnknown
}
func (mode VerifierMode) valid() bool {
	return mode == VerifierSeparateCopy || mode == VerifierSharedReadOnly || mode == VerifierProfileOwned
}

func (id MountID) valid() bool {
	return id == MountDefinitions || id == MountFixture || id == MountSkill
}

func (policy NetworkPolicy) valid() bool {
	switch policy.Mode {
	case NetworkDeny, NetworkAmbient:
		return policy.AllowlistSHA256 == ""
	case NetworkAllowlist:
		return validSHA256(policy.AllowlistSHA256)
	default:
		return false
	}
}

func (policy CredentialPolicy) valid() bool {
	switch policy.Mode {
	case CredentialsNone:
		return policy.ScopeSHA256 == ""
	case CredentialsScoped:
		return validSHA256(policy.ScopeSHA256)
	case CredentialsAmbient:
		return policy.ScopeSHA256 == ""
	default:
		return false
	}
}

func (policy ResourcePolicy) valid() bool {
	return policy.DeadlineMillis > 0 && policy.DeadlineMillis <= MaxDeadlineMillis &&
		policy.MaxInputBytes > 0 && policy.MaxInputBytes <= MaxArchiveBytes && policy.MaxOutputBytes > 0 && policy.MaxOutputBytes <= MaxArchiveBytes &&
		policy.MaxEntries > 0 && policy.MaxEntries <= MaxSnapshotEntries && policy.MaxArtifacts <= MaxArtifacts && policy.MaxOperations > 0 && policy.MaxOperations <= MaxArtifacts &&
		policy.CPUTimeMillis <= MaxDeadlineMillis && policy.MemoryBytes <= 1<<40 && policy.ProcessLimit <= 4096
}

func (program Program) valid() bool {
	return program.Kind == ProgramExternalAdapter || program.Kind == ProgramReferenceCopy || program.Kind == ProgramWaitForCancel
}

func (verifier Verifier) valid() bool {
	return verifier.Kind == VerifierProfileDecision || verifier.Kind == VerifierSHA256Equals
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > MaxIdentifierBytes {
		return false
	}
	segmentStart := true
	for _, character := range value {
		if character == '/' {
			if segmentStart {
				return false
			}
			segmentStart = true
			continue
		}
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || !segmentStart && (character == '-' || character == '.' || character == '_') {
			segmentStart = false
			continue
		}
		return false
	}
	return !segmentStart
}

func validRelativePath(value string) bool {
	return validArchivePath(value)
}

func validVersion(value string) bool {
	if len(value) == 0 || len(value) > MaxIdentifierBytes {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' ||
			index > 0 && (character == '.' || character == '-' || character == '_' || character == '+') {
			continue
		}
		return false
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
	_, _ = hash.Write([]byte("agent-eval/execution-backend/" + domain + "/v1\x00"))
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func contractError(code string) error { return fmt.Errorf("%w: %s", ErrContract, code) }
