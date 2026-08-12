// Package executionbackend owns the provider-neutral trial execution contract.
package executionbackend

import (
	"errors"
	"fmt"
	"slices"
	"sort"
)

const (
	ContractSchema  = "agent-eval/execution-backend-contract"
	PlanSchema      = "agent-eval/trial-plan"
	ReceiptSchema   = "agent-eval/trial-receipt"
	SchemaVersion   = 1
	ContractVersion = "0.1.0-pre-release"

	MaxContractBytes   = 64 << 10
	MaxPlanBytes       = 256 << 10
	MaxReceiptBytes    = 64 << 10
	MaxArchiveBytes    = 64 << 20
	MaxSnapshotEntries = 4096
	MaxArtifactBytes   = 8 << 20
	MaxArtifacts       = 256
	MaxIdentifierBytes = 128
	MaxRelativePath    = 1024
	MaxDeadlineMillis  = 60 * 60 * 1000
)

var (
	ErrContract    = errors.New("execution_backend_contract_invalid")
	ErrUnsupported = errors.New("execution_backend_unsupported")
	ErrPolicy      = errors.New("execution_backend_policy_denied")
	ErrExecution   = errors.New("execution_backend_execution_failed")
	ErrInterrupted = errors.New("execution_backend_interrupted")
)

type Support string

const (
	SupportNotApplicable Support = "not_applicable"
	SupportSupported     Support = "supported"
	SupportUnknown       Support = "unknown"
	SupportUnsupported   Support = "unsupported"
)

type Assurance string

const (
	AssuranceHermeticReference    Assurance = "hermetic_reference"
	AssuranceIsolatedDeclaredGaps Assurance = "isolated_declared_gaps"
	AssuranceLocalProcess         Assurance = "local_process"
)

type CapabilityID string

const (
	CapabilityArtifactDeclaredOnly   CapabilityID = "artifact.declared_only"
	CapabilityCleanupLogical         CapabilityID = "cleanup.logical"
	CapabilityCredentialsAmbient     CapabilityID = "credentials.ambient"
	CapabilityCredentialsNone        CapabilityID = "credentials.none"
	CapabilityCredentialsScoped      CapabilityID = "credentials.scoped"
	CapabilityEnvironmentExact       CapabilityID = "environment.exact"
	CapabilityFixtureReset           CapabilityID = "fixture.reset"
	CapabilityInputsContentAddressed CapabilityID = "inputs.content_addressed"
	CapabilityInputsImmutable        CapabilityID = "inputs.immutable"
	CapabilityMountsReadOnly         CapabilityID = "mounts.read_only"
	CapabilityNetworkAllowlist       CapabilityID = "network.allowlist"
	CapabilityNetworkAmbient         CapabilityID = "network.ambient"
	CapabilityNetworkDeny            CapabilityID = "network.deny"
	CapabilityProcessTree            CapabilityID = "process.tree"
	CapabilityResourceCPU            CapabilityID = "resource.cpu"
	CapabilityResourceDeadline       CapabilityID = "resource.deadline"
	CapabilityResourceMemory         CapabilityID = "resource.memory"
	CapabilityResourceProcesses      CapabilityID = "resource.processes"
	CapabilityResourceStorage        CapabilityID = "resource.storage"
	CapabilityVerifierSeparate       CapabilityID = "verifier.separate_copy"
	CapabilityVerifierSharedReadOnly CapabilityID = "verifier.shared_readonly"
	CapabilityWorkspaceFresh         CapabilityID = "workspace.fresh"
	CapabilityWorkspaceOutputOnly    CapabilityID = "workspace.output_only"
)

var closedCapabilities = []CapabilityID{
	CapabilityArtifactDeclaredOnly,
	CapabilityCleanupLogical,
	CapabilityCredentialsAmbient,
	CapabilityCredentialsNone,
	CapabilityCredentialsScoped,
	CapabilityEnvironmentExact,
	CapabilityFixtureReset,
	CapabilityInputsContentAddressed,
	CapabilityInputsImmutable,
	CapabilityMountsReadOnly,
	CapabilityNetworkAllowlist,
	CapabilityNetworkAmbient,
	CapabilityNetworkDeny,
	CapabilityProcessTree,
	CapabilityResourceCPU,
	CapabilityResourceDeadline,
	CapabilityResourceMemory,
	CapabilityResourceProcesses,
	CapabilityResourceStorage,
	CapabilityVerifierSeparate,
	CapabilityVerifierSharedReadOnly,
	CapabilityWorkspaceFresh,
	CapabilityWorkspaceOutputOnly,
}

type Capability struct {
	ID      CapabilityID `json:"id"`
	Support Support      `json:"support"`
}

// Contract binds one exact backend implementation and its complete capability
// claims. ContentSHA256 identifies the selected image, executable, or built-in
// content projection; it is not publisher authentication.
type Contract struct {
	Schema               string       `json:"schema"`
	SchemaVersion        int          `json:"schema_version"`
	ContractVersion      string       `json:"contract_version"`
	BackendID            string       `json:"backend_id"`
	BackendVersion       string       `json:"backend_version"`
	ImplementationSHA256 string       `json:"implementation_sha256"`
	ContentSHA256        string       `json:"content_sha256"`
	Assurance            Assurance    `json:"assurance"`
	Capabilities         []Capability `json:"capabilities"`
}

type NetworkMode string

const (
	NetworkDeny      NetworkMode = "deny"
	NetworkAllowlist NetworkMode = "allowlist"
	NetworkAmbient   NetworkMode = "ambient"
)

type CredentialMode string

const (
	CredentialsNone    CredentialMode = "none"
	CredentialsScoped  CredentialMode = "scoped"
	CredentialsAmbient CredentialMode = "ambient"
)

type VerifierMode string

const (
	VerifierSeparateCopy   VerifierMode = "separate_copy"
	VerifierSharedReadOnly VerifierMode = "shared_readonly"
	VerifierProfileOwned   VerifierMode = "profile_owned"
)

type MountID string

const (
	MountDefinitions MountID = "definitions"
	MountFixture     MountID = "fixture"
	MountSkill       MountID = "skill"
)

type Mount struct {
	ID            MountID `json:"id"`
	ContentSHA256 string  `json:"content_sha256"`
	ReadOnly      bool    `json:"read_only"`
}

type NetworkPolicy struct {
	Mode            NetworkMode `json:"mode"`
	AllowlistSHA256 string      `json:"allowlist_sha256,omitempty"`
}

type CredentialPolicy struct {
	Mode        CredentialMode `json:"mode"`
	ScopeSHA256 string         `json:"scope_sha256,omitempty"`
}

type ResourcePolicy struct {
	DeadlineMillis uint64 `json:"deadline_millis"`
	MaxInputBytes  uint64 `json:"max_input_bytes"`
	MaxOutputBytes uint64 `json:"max_output_bytes"`
	MaxEntries     uint32 `json:"max_entries"`
	MaxArtifacts   uint32 `json:"max_artifacts"`
	MaxOperations  uint32 `json:"max_operations"`
	CPUTimeMillis  uint64 `json:"cpu_time_millis,omitempty"`
	MemoryBytes    uint64 `json:"memory_bytes,omitempty"`
	ProcessLimit   uint32 `json:"process_limit,omitempty"`
}

type ArtifactPrivacy string

const (
	PrivacyContentMinimized ArtifactPrivacy = "content_minimized"
	PrivacyOwnerPrivate     ArtifactPrivacy = "owner_private"
	PrivacyPublic           ArtifactPrivacy = "public"
)

type ArtifactDeclaration struct {
	ID       string          `json:"id"`
	MaxBytes uint64          `json:"max_bytes"`
	Privacy  ArtifactPrivacy `json:"privacy"`
}

type ProgramKind string

const (
	ProgramExternalAdapter ProgramKind = "external_adapter"
	ProgramReferenceCopy   ProgramKind = "reference_copy"
	ProgramWaitForCancel   ProgramKind = "wait_for_cancel"
)

type Program struct {
	Kind        ProgramKind `json:"kind"`
	SourceMount MountID     `json:"source_mount,omitempty"`
	SourcePath  string      `json:"source_path,omitempty"`
	ArtifactID  string      `json:"artifact_id,omitempty"`
}

type VerifierKind string

const (
	VerifierProfileDecision VerifierKind = "profile_decision"
	VerifierSHA256Equals    VerifierKind = "sha256_equals"
)

type Verifier struct {
	Kind           VerifierKind `json:"kind"`
	ArtifactID     string       `json:"artifact_id,omitempty"`
	ExpectedSHA256 string       `json:"expected_sha256,omitempty"`
}

// Plan binds the complete effective policy before backend entry. It stores no
// host path, environment value, credential, URL, or raw artifact.
type Plan struct {
	Schema          string                `json:"schema"`
	SchemaVersion   int                   `json:"schema_version"`
	ContractVersion string                `json:"contract_version"`
	ContractSHA256  string                `json:"contract_sha256"`
	Requirements    []CapabilityID        `json:"requirements"`
	Mounts          []Mount               `json:"mounts"`
	Network         NetworkPolicy         `json:"network"`
	Credentials     CredentialPolicy      `json:"credentials"`
	Resources       ResourcePolicy        `json:"resources"`
	VerifierMode    VerifierMode          `json:"verifier_mode"`
	Artifacts       []ArtifactDeclaration `json:"artifacts"`
	Program         Program               `json:"program"`
	Verifier        Verifier              `json:"verifier"`
}

type Presence string

const (
	PresenceNotApplicable Presence = "not_applicable"
	PresenceObserved      Presence = "observed"
	PresenceUnknown       Presence = "unknown"
	PresenceUnsupported   Presence = "unsupported"
)

type Verdict string

const (
	VerdictFailed        Verdict = "failed"
	VerdictNotApplicable Verdict = "not_applicable"
	VerdictSucceeded     Verdict = "succeeded"
	VerdictUnknown       Verdict = "unknown"
)

type ReceiptArtifact struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Bytes  uint64 `json:"bytes"`
}

// Receipt is content-minimized proof. Raw inputs, outputs, environment, paths,
// credentials, and diagnostic text are deliberately absent.
type Receipt struct {
	Schema                 string            `json:"schema"`
	SchemaVersion          int               `json:"schema_version"`
	ContractVersion        string            `json:"contract_version"`
	ContractSHA256         string            `json:"contract_sha256"`
	PlanSHA256             string            `json:"plan_sha256"`
	InputSHA256            string            `json:"input_sha256"`
	Artifacts              []ReceiptArtifact `json:"artifacts"`
	ArtifactSetSHA256      string            `json:"artifact_set_sha256"`
	Verdict                Verdict           `json:"verdict"`
	VerifierEvidenceSHA256 string            `json:"verifier_evidence_sha256"`
	Termination            Presence          `json:"termination"`
	Cleanup                Presence          `json:"cleanup"`
	Network                Presence          `json:"network"`
	Credentials            Presence          `json:"credentials"`
}

type AdmittedPlan struct {
	contract Contract
	plan     Plan
	planSHA  string
}

func (a AdmittedPlan) Contract() Contract { return cloneContract(a.contract) }
func (a AdmittedPlan) Plan() Plan         { return clonePlan(a.plan) }
func (a AdmittedPlan) SHA256() string     { return a.planSHA }

func Capabilities() []CapabilityID { return slices.Clone(closedCapabilities) }

func NewContract(id, version, implementationSHA256, contentSHA256 string, assurance Assurance, support map[CapabilityID]Support) (Contract, error) {
	for capability := range support {
		if !slices.Contains(closedCapabilities, capability) {
			return Contract{}, contractError("unknown_capability")
		}
	}
	capabilities := make([]Capability, len(closedCapabilities))
	for index, capability := range closedCapabilities {
		state, ok := support[capability]
		if !ok {
			state = SupportUnknown
		}
		capabilities[index] = Capability{ID: capability, Support: state}
	}
	contract := Contract{Schema: ContractSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		BackendID: id, BackendVersion: version, ImplementationSHA256: implementationSHA256,
		ContentSHA256: contentSHA256, Assurance: assurance, Capabilities: capabilities}
	if err := ValidateContract(contract); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func ValidateContract(contract Contract) error {
	if contract.Schema != ContractSchema || contract.SchemaVersion != SchemaVersion || contract.ContractVersion != ContractVersion ||
		!validIdentifier(contract.BackendID) || !validVersion(contract.BackendVersion) ||
		!validSHA256(contract.ImplementationSHA256) || !validSHA256(contract.ContentSHA256) || !contract.Assurance.valid() ||
		len(contract.Capabilities) != len(closedCapabilities) {
		return contractError("shape")
	}
	claims := make(map[CapabilityID]Support, len(contract.Capabilities))
	for index, capability := range contract.Capabilities {
		if capability.ID != closedCapabilities[index] || !capability.Support.valid() {
			return contractError("capabilities")
		}
		claims[capability.ID] = capability.Support
	}
	if contract.Assurance == AssuranceHermeticReference {
		implementation, content := referenceImplementationIdentities()
		if contract.BackendID != "reference-hermetic" || contract.BackendVersion != "1" ||
			contract.ImplementationSHA256 != implementation || contract.ContentSHA256 != content {
			return contractError("hermetic_identity")
		}
		for _, capability := range closedCapabilities {
			if claims[capability] != referenceSupport(capability) {
				return contractError("hermetic_claim")
			}
		}
	}
	return nil
}

func Admit(contract Contract, plan Plan) (AdmittedPlan, error) {
	if err := ValidateContract(contract); err != nil {
		return AdmittedPlan{}, err
	}
	contractSHA, err := ContractSHA256(contract)
	if err != nil || plan.ContractSHA256 != contractSHA {
		return AdmittedPlan{}, contractError("plan_binding")
	}
	if err := ValidatePlan(plan); err != nil {
		return AdmittedPlan{}, err
	}
	claims := make(map[CapabilityID]Support, len(contract.Capabilities))
	for _, capability := range contract.Capabilities {
		claims[capability.ID] = capability.Support
	}
	for _, required := range plan.Requirements {
		switch claims[required] {
		case SupportSupported:
		case SupportUnsupported, SupportNotApplicable:
			return AdmittedPlan{}, fmt.Errorf("%w: %s", ErrUnsupported, required)
		case SupportUnknown:
			return AdmittedPlan{}, fmt.Errorf("%w: %s", ErrUnsupported, required)
		default:
			return AdmittedPlan{}, contractError("requirement")
		}
	}
	if err := validatePolicyClaims(claims, plan); err != nil {
		return AdmittedPlan{}, err
	}
	if err := validateAssurancePlan(contract, plan); err != nil {
		return AdmittedPlan{}, err
	}
	planSHA, err := PlanSHA256(plan)
	if err != nil {
		return AdmittedPlan{}, err
	}
	return AdmittedPlan{contract: cloneContract(contract), plan: clonePlan(plan), planSHA: planSHA}, nil
}

func ValidatePlan(plan Plan) error {
	if plan.Schema != PlanSchema || plan.SchemaVersion != SchemaVersion || plan.ContractVersion != ContractVersion || !validSHA256(plan.ContractSHA256) ||
		plan.Requirements == nil || len(plan.Requirements) > len(closedCapabilities) || len(plan.Mounts) != 3 ||
		plan.Artifacts == nil || len(plan.Artifacts) > MaxArtifacts || !plan.Network.valid() || !plan.Credentials.valid() ||
		!plan.VerifierMode.valid() || !plan.Resources.valid() || !plan.Program.valid() || !plan.Verifier.valid() {
		return contractError("plan_shape")
	}
	for index, requirement := range plan.Requirements {
		if !slices.Contains(closedCapabilities, requirement) || index > 0 && plan.Requirements[index-1] >= requirement {
			return contractError("requirements")
		}
	}
	wantMounts := []MountID{MountDefinitions, MountFixture, MountSkill}
	for index, mount := range plan.Mounts {
		if mount.ID != wantMounts[index] || !validSHA256(mount.ContentSHA256) {
			return contractError("mounts")
		}
	}
	for index, artifact := range plan.Artifacts {
		if !validIdentifier(artifact.ID) || artifact.MaxBytes == 0 || artifact.MaxBytes > MaxArtifactBytes || !artifact.Privacy.valid() ||
			index > 0 && plan.Artifacts[index-1].ID >= artifact.ID {
			return contractError("artifacts")
		}
	}
	if plan.Program.Kind == ProgramReferenceCopy {
		if !plan.Program.SourceMount.valid() || !validRelativePath(plan.Program.SourcePath) || !validIdentifier(plan.Program.ArtifactID) ||
			!declaresArtifact(plan.Artifacts, plan.Program.ArtifactID) {
			return contractError("reference_program")
		}
	} else if plan.Program.Kind == ProgramWaitForCancel {
		if plan.Program.SourceMount != "" || plan.Program.SourcePath != "" || plan.Program.ArtifactID != "" || len(plan.Artifacts) != 0 {
			return contractError("cancel_program")
		}
	} else if plan.Program.SourceMount != "" || plan.Program.SourcePath != "" || plan.Program.ArtifactID != "" {
		return contractError("external_program")
	}
	if plan.Verifier.Kind == VerifierSHA256Equals {
		if !validIdentifier(plan.Verifier.ArtifactID) || !validSHA256(plan.Verifier.ExpectedSHA256) || !declaresArtifact(plan.Artifacts, plan.Verifier.ArtifactID) ||
			plan.VerifierMode != VerifierSeparateCopy {
			return contractError("reference_verifier")
		}
	} else if plan.Verifier.ArtifactID != "" || plan.Verifier.ExpectedSHA256 != "" {
		return contractError("profile_verifier")
	}
	return nil
}

func ValidateReceipt(plan Plan, receipt Receipt) error {
	planSHA, err := PlanSHA256(plan)
	if err != nil || receipt.Schema != ReceiptSchema || receipt.SchemaVersion != SchemaVersion || receipt.ContractVersion != ContractVersion ||
		receipt.ContractSHA256 != plan.ContractSHA256 || receipt.PlanSHA256 != planSHA || !validSHA256(receipt.InputSHA256) ||
		receipt.Artifacts == nil || len(receipt.Artifacts) > len(plan.Artifacts) || !validSHA256(receipt.ArtifactSetSHA256) ||
		!receipt.Verdict.valid() || !validSHA256(receipt.VerifierEvidenceSHA256) || !receipt.Termination.valid() ||
		!receipt.Cleanup.valid() || !receipt.Network.valid() || !receipt.Credentials.valid() {
		return contractError("receipt_shape")
	}
	for index, artifact := range receipt.Artifacts {
		if !validIdentifier(artifact.ID) || !validSHA256(artifact.SHA256) || artifact.Bytes > MaxArtifactBytes ||
			index > 0 && receipt.Artifacts[index-1].ID >= artifact.ID || !declaresArtifact(plan.Artifacts, artifact.ID) ||
			artifact.Bytes > declaration(plan.Artifacts, artifact.ID).MaxBytes {
			return contractError("receipt_artifacts")
		}
	}
	if receipt.ArtifactSetSHA256 != artifactSetSHA256(receipt.Artifacts) {
		return contractError("receipt_artifact_set")
	}
	switch receipt.Verdict {
	case VerdictSucceeded, VerdictFailed:
		if passed, known := receiptVerifierDecision(plan.Verifier, receipt.Artifacts); known && passed != (receipt.Verdict == VerdictSucceeded) {
			return contractError("receipt_verdict")
		}
		if receipt.VerifierEvidenceSHA256 != verifierEvidenceSHA256(plan.Verifier, receipt.Artifacts, receipt.Verdict == VerdictSucceeded) {
			return contractError("receipt_verifier")
		}
	case VerdictUnknown:
		if len(receipt.Artifacts) != 0 || receipt.VerifierEvidenceSHA256 != unknownEvidenceSHA256(receipt) {
			return contractError("receipt_unknown")
		}
	case VerdictNotApplicable:
		if len(receipt.Artifacts) != 0 || receipt.VerifierEvidenceSHA256 != hashDomain("not-applicable", nil) ||
			receipt.Termination != PresenceNotApplicable || receipt.Cleanup != PresenceNotApplicable ||
			receipt.Network != PresenceNotApplicable || receipt.Credentials != PresenceNotApplicable {
			return contractError("receipt_not_applicable")
		}
	}
	if slices.Equal(plan.Requirements, referenceRequirements()) &&
		(receipt.Termination != PresenceObserved || receipt.Cleanup != PresenceObserved ||
			receipt.Network != PresenceObserved || receipt.Credentials != PresenceObserved) {
		return contractError("receipt_hermetic_coverage")
	}
	return nil
}

func validatePolicyClaims(claims map[CapabilityID]Support, plan Plan) error {
	require := func(capability CapabilityID) error {
		if claims[capability] != SupportSupported {
			return fmt.Errorf("%w: %s", ErrUnsupported, capability)
		}
		return nil
	}
	switch plan.Network.Mode {
	case NetworkDeny:
		if err := require(CapabilityNetworkDeny); err != nil {
			return err
		}
	case NetworkAllowlist:
		if err := require(CapabilityNetworkAllowlist); err != nil {
			return err
		}
	case NetworkAmbient:
		if err := require(CapabilityNetworkAmbient); err != nil {
			return err
		}
	}
	switch plan.Credentials.Mode {
	case CredentialsNone:
		if err := require(CapabilityCredentialsNone); err != nil {
			return err
		}
	case CredentialsScoped:
		if err := require(CapabilityCredentialsScoped); err != nil {
			return err
		}
	case CredentialsAmbient:
		if err := require(CapabilityCredentialsAmbient); err != nil {
			return err
		}
	}
	if plan.Resources.DeadlineMillis > 0 {
		if err := require(CapabilityResourceDeadline); err != nil {
			return err
		}
	}
	for _, mount := range plan.Mounts {
		if mount.ReadOnly {
			if err := require(CapabilityMountsReadOnly); err != nil {
				return err
			}
			break
		}
	}
	if slices.Contains(plan.Requirements, CapabilityResourceStorage) {
		if err := require(CapabilityResourceStorage); err != nil {
			return err
		}
	}
	if plan.Resources.CPUTimeMillis > 0 || slices.Contains(plan.Requirements, CapabilityResourceCPU) {
		if err := require(CapabilityResourceCPU); err != nil {
			return err
		}
	}
	if plan.Resources.MemoryBytes > 0 || slices.Contains(plan.Requirements, CapabilityResourceMemory) {
		if err := require(CapabilityResourceMemory); err != nil {
			return err
		}
	}
	if plan.Resources.ProcessLimit > 0 || slices.Contains(plan.Requirements, CapabilityResourceProcesses) {
		if err := require(CapabilityResourceProcesses); err != nil {
			return err
		}
	}
	switch plan.VerifierMode {
	case VerifierSeparateCopy:
		return require(CapabilityVerifierSeparate)
	case VerifierSharedReadOnly:
		return require(CapabilityVerifierSharedReadOnly)
	case VerifierProfileOwned:
		return nil
	default:
		return contractError("verifier_mode")
	}
}

func validateAssurancePlan(contract Contract, plan Plan) error {
	if contract.Assurance != AssuranceHermeticReference {
		return nil
	}
	if !slices.Equal(plan.Requirements, referenceRequirements()) || plan.Network.Mode != NetworkDeny ||
		plan.Credentials.Mode != CredentialsNone || plan.VerifierMode != VerifierSeparateCopy ||
		!plan.Mounts[0].ReadOnly || !plan.Mounts[1].ReadOnly || !plan.Mounts[2].ReadOnly ||
		(plan.Program.Kind != ProgramReferenceCopy && plan.Program.Kind != ProgramWaitForCancel) ||
		plan.Resources.CPUTimeMillis != 0 || plan.Resources.MemoryBytes != 0 || plan.Resources.ProcessLimit != 0 {
		return fmt.Errorf("%w: hermetic policy", ErrPolicy)
	}
	return nil
}

func referenceRequirements() []CapabilityID {
	return SortedRequirements(CapabilityArtifactDeclaredOnly, CapabilityCleanupLogical, CapabilityCredentialsNone,
		CapabilityEnvironmentExact, CapabilityFixtureReset, CapabilityInputsContentAddressed, CapabilityInputsImmutable,
		CapabilityMountsReadOnly, CapabilityNetworkDeny, CapabilityResourceDeadline, CapabilityResourceStorage,
		CapabilityVerifierSeparate, CapabilityWorkspaceFresh, CapabilityWorkspaceOutputOnly)
}

func SortedRequirements(values ...CapabilityID) []CapabilityID {
	result := slices.Clone(values)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return slices.Compact(result)
}

func cloneContract(contract Contract) Contract {
	contract.Capabilities = slices.Clone(contract.Capabilities)
	return contract
}

func clonePlan(plan Plan) Plan {
	plan.Requirements = slices.Clone(plan.Requirements)
	plan.Mounts = slices.Clone(plan.Mounts)
	plan.Artifacts = slices.Clone(plan.Artifacts)
	return plan
}

func declaresArtifact(declarations []ArtifactDeclaration, id string) bool {
	return declaration(declarations, id).ID != ""
}
func declaration(declarations []ArtifactDeclaration, id string) ArtifactDeclaration {
	index, found := slices.BinarySearchFunc(declarations, id, func(value ArtifactDeclaration, wanted string) int {
		if value.ID < wanted {
			return -1
		}
		if value.ID > wanted {
			return 1
		}
		return 0
	})
	if !found {
		return ArtifactDeclaration{}
	}
	return declarations[index]
}
