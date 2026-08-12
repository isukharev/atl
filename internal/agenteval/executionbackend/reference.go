package executionbackend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

type ReferencePlanOptions struct {
	FixtureSHA256     string
	SkillSHA256       string
	DefinitionsSHA256 string
	Resources         ResourcePolicy
	Artifacts         []ArtifactDeclaration
	Program           Program
	Verifier          Verifier
}

type ReferenceInputs struct {
	Fixture     []byte
	Skill       []byte
	Definitions []byte
}

type LocalProcessPlanOptions struct {
	DefinitionsSHA256 string
	FixtureSHA256     string
	SkillSHA256       string
	DeadlineMillis    uint64
}

type Artifact struct {
	ID   string
	Data []byte
}

type RunResult struct {
	Receipt   Receipt
	Artifacts []Artifact
}

// PrepareReferenceInputs clones and validates every input while the durable
// attempt is still precommit. The returned slices share no storage with the
// caller and may be passed to RunReference without a caller-mutation race.
func PrepareReferenceInputs(ctx context.Context, admitted AdmittedPlan, inputs ReferenceInputs) (ReferenceInputs, error) {
	if ctx == nil || admitted.contract.Assurance != AssuranceHermeticReference || admitted.plan.Program.Kind == ProgramExternalAdapter {
		return ReferenceInputs{}, contractError("reference_inputs")
	}
	plan := admitted.plan
	if uint64(len(inputs.Fixture))+uint64(len(inputs.Skill))+uint64(len(inputs.Definitions)) > plan.Resources.MaxInputBytes {
		return ReferenceInputs{}, fmt.Errorf("%w: input bytes", ErrPolicy)
	}
	owned := ReferenceInputs{Fixture: slices.Clone(inputs.Fixture), Skill: slices.Clone(inputs.Skill), Definitions: slices.Clone(inputs.Definitions)}
	failed := true
	defer func() {
		if failed {
			clearReferenceInputs(&owned)
		}
	}()
	fixture, err := decodeArchiveContext(ctx, owned.Fixture, plan.Resources.MaxInputBytes, plan.Resources.MaxEntries)
	if err != nil {
		return ReferenceInputs{}, err
	}
	defer fixture.clear()
	skill, err := decodeArchiveContext(ctx, owned.Skill, plan.Resources.MaxInputBytes, plan.Resources.MaxEntries)
	if err != nil {
		return ReferenceInputs{}, err
	}
	defer skill.clear()
	definitions, err := decodeArchiveContext(ctx, owned.Definitions, plan.Resources.MaxInputBytes, plan.Resources.MaxEntries)
	if err != nil {
		return ReferenceInputs{}, err
	}
	defer definitions.clear()
	if !referenceSnapshotsWithinEntryLimit(plan.Resources.MaxEntries, fixture, skill, definitions) {
		return ReferenceInputs{}, fmt.Errorf("%w: input entries", ErrPolicy)
	}
	if err := contextCause(ctx); err != nil {
		return ReferenceInputs{}, fmt.Errorf("%w: %w", ErrInterrupted, err)
	}
	if !mountDigestsMatch(plan.Mounts, definitions.digest, fixture.digest, skill.digest) {
		return ReferenceInputs{}, fmt.Errorf("%w: input identity", ErrPolicy)
	}
	if err := validateReferenceActionInputs(plan, definitions, fixture, skill); err != nil {
		return ReferenceInputs{}, err
	}
	failed = false
	return owned, nil
}

func ReferenceContract() (Contract, error) {
	support := make(map[CapabilityID]Support, len(closedCapabilities))
	for _, capability := range closedCapabilities {
		support[capability] = referenceSupport(capability)
	}
	implementation, content := referenceImplementationIdentities()
	return NewContract("reference-hermetic", "1", implementation, content, AssuranceHermeticReference, support)
}

func referenceImplementationIdentities() (string, string) {
	return hashDomain("reference-implementation", []byte("closed-in-memory-v1")),
		hashDomain("reference-content", []byte("archive-snapshot+copy+sha256-verifier/v1"))
}

func referenceSupport(capability CapabilityID) Support {
	switch capability {
	case CapabilityArtifactDeclaredOnly, CapabilityCleanupLogical,
		CapabilityCredentialsNone, CapabilityEnvironmentExact, CapabilityFixtureReset,
		CapabilityInputsContentAddressed, CapabilityInputsImmutable, CapabilityMountsReadOnly,
		CapabilityNetworkDeny, CapabilityResourceDeadline, CapabilityResourceStorage,
		CapabilityVerifierSeparate, CapabilityWorkspaceFresh, CapabilityWorkspaceOutputOnly:
		return SupportSupported
	case CapabilityCredentialsAmbient, CapabilityNetworkAmbient, CapabilityProcessTree,
		CapabilityResourceProcesses, CapabilityVerifierSharedReadOnly:
		return SupportNotApplicable
	default:
		return SupportUnsupported
	}
}

func LocalProcessContract(implementationSHA256, contentSHA256 string) (Contract, error) {
	support := make(map[CapabilityID]Support, len(closedCapabilities))
	for _, capability := range closedCapabilities {
		support[capability] = SupportUnknown
	}
	for _, capability := range []CapabilityID{CapabilityCredentialsAmbient, CapabilityNetworkAmbient, CapabilityResourceDeadline,
		CapabilityWorkspaceFresh} {
		support[capability] = SupportSupported
	}
	for _, capability := range []CapabilityID{CapabilityNetworkDeny, CapabilityNetworkAllowlist, CapabilityResourceCPU,
		CapabilityResourceMemory, CapabilityResourceProcesses, CapabilityVerifierSeparate} {
		support[capability] = SupportUnsupported
	}
	return NewContract("local-process", "1", implementationSHA256, contentSHA256, AssuranceLocalProcess, support)
}

func NewLocalProcessPlan(contract Contract, options LocalProcessPlanOptions) (Plan, error) {
	if contract.Assurance != AssuranceLocalProcess || !validSHA256(options.DefinitionsSHA256) || !validSHA256(options.FixtureSHA256) ||
		!validSHA256(options.SkillSHA256) || options.DeadlineMillis == 0 || options.DeadlineMillis > MaxDeadlineMillis {
		return Plan{}, contractError("local_plan")
	}
	contractSHA, err := ContractSHA256(contract)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Schema: PlanSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion, ContractSHA256: contractSHA,
		Requirements: SortedRequirements(CapabilityCredentialsAmbient, CapabilityNetworkAmbient, CapabilityResourceDeadline, CapabilityWorkspaceFresh),
		Mounts: []Mount{{ID: MountDefinitions, ContentSHA256: options.DefinitionsSHA256},
			{ID: MountFixture, ContentSHA256: options.FixtureSHA256}, {ID: MountSkill, ContentSHA256: options.SkillSHA256}},
		Network: NetworkPolicy{Mode: NetworkAmbient}, Credentials: CredentialPolicy{Mode: CredentialsAmbient},
		Resources: ResourcePolicy{DeadlineMillis: options.DeadlineMillis, MaxInputBytes: MaxArchiveBytes, MaxOutputBytes: MaxArchiveBytes,
			MaxEntries: MaxSnapshotEntries, MaxArtifacts: 0, MaxOperations: 1}, VerifierMode: VerifierProfileOwned,
		Artifacts: []ArtifactDeclaration{}, Program: Program{Kind: ProgramExternalAdapter}, Verifier: Verifier{Kind: VerifierProfileDecision}}
	if _, err := Admit(contract, plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func NewReferencePlan(contract Contract, options ReferencePlanOptions) (Plan, error) {
	contractSHA, err := ContractSHA256(contract)
	if err != nil || contract.Assurance != AssuranceHermeticReference {
		return Plan{}, contractError("reference_contract")
	}
	artifacts := slices.Clone(options.Artifacts)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].ID < artifacts[j].ID })
	plan := Plan{
		Schema: PlanSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion, ContractSHA256: contractSHA,
		Requirements: referenceRequirements(),
		Mounts: []Mount{{ID: MountDefinitions, ContentSHA256: options.DefinitionsSHA256, ReadOnly: true},
			{ID: MountFixture, ContentSHA256: options.FixtureSHA256, ReadOnly: true},
			{ID: MountSkill, ContentSHA256: options.SkillSHA256, ReadOnly: true}},
		Network: NetworkPolicy{Mode: NetworkDeny}, Credentials: CredentialPolicy{Mode: CredentialsNone},
		Resources: options.Resources, VerifierMode: VerifierSeparateCopy, Artifacts: artifacts,
		Program: options.Program, Verifier: options.Verifier,
	}
	if _, err := Admit(contract, plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func RunReference(ctx context.Context, admitted AdmittedPlan, inputs ReferenceInputs) (RunResult, error) {
	if ctx == nil {
		return RunResult{}, contractError("context")
	}
	contract, plan := admitted.contract, admitted.plan
	if contract.Assurance != AssuranceHermeticReference || plan.Program.Kind == ProgramExternalAdapter {
		return RunResult{}, fmt.Errorf("%w: reference plan", ErrUnsupported)
	}
	if err := contextCause(ctx); err != nil {
		return interruptedResult(plan, admitted.planSHA, err)
	}
	if uint64(len(inputs.Fixture))+uint64(len(inputs.Skill))+uint64(len(inputs.Definitions)) > plan.Resources.MaxInputBytes {
		return RunResult{}, fmt.Errorf("%w: input bytes", ErrPolicy)
	}
	// Plan admission proves DeadlineMillis is in 1..MaxDeadlineMillis.
	deadline := time.Duration(plan.Resources.DeadlineMillis) * time.Millisecond // #nosec G115 -- bounded before conversion.
	runContext, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	fixture, err := decodeArchiveContext(runContext, inputs.Fixture, plan.Resources.MaxInputBytes, plan.Resources.MaxEntries)
	if err != nil {
		return RunResult{}, err
	}
	defer fixture.clear()
	skill, err := decodeArchiveContext(runContext, inputs.Skill, plan.Resources.MaxInputBytes, plan.Resources.MaxEntries)
	if err != nil {
		return RunResult{}, err
	}
	defer skill.clear()
	definitions, err := decodeArchiveContext(runContext, inputs.Definitions, plan.Resources.MaxInputBytes, plan.Resources.MaxEntries)
	if err != nil {
		return RunResult{}, err
	}
	defer definitions.clear()
	if !referenceSnapshotsWithinEntryLimit(plan.Resources.MaxEntries, fixture, skill, definitions) {
		return RunResult{}, fmt.Errorf("%w: input entries", ErrPolicy)
	}
	if err := contextCause(runContext); err != nil {
		return interruptedReceipt(plan, admitted.planSHA, declaredInputSHA256(plan.Mounts), referenceInputBytes(inputs),
			referenceInputEntries(definitions, fixture, skill), 0, err)
	}
	if !mountDigestsMatch(plan.Mounts, definitions.digest, fixture.digest, skill.digest) {
		return RunResult{}, fmt.Errorf("%w: input identity", ErrPolicy)
	}
	inputSHA := combinedInputSHA256(definitions, fixture, skill)
	if plan.Program.Kind == ProgramWaitForCancel {
		<-runContext.Done()
		return interruptedReceipt(plan, admitted.planSHA, inputSHA, referenceInputBytes(inputs), referenceInputEntries(definitions, fixture, skill), 1,
			runContext.Err())
	}
	source := map[MountID]*snapshot{MountDefinitions: definitions, MountFixture: fixture, MountSkill: skill}[plan.Program.SourceMount]
	content, err := source.read(plan.Program.SourcePath)
	if err != nil {
		return RunResult{}, err
	}
	declaration := declaration(plan.Artifacts, plan.Program.ArtifactID)
	if uint64(len(content)) > declaration.MaxBytes || uint64(len(content)) > plan.Resources.MaxOutputBytes || plan.Resources.MaxArtifacts < 1 || plan.Resources.MaxOperations < 1 {
		clear(content)
		return RunResult{}, fmt.Errorf("%w: artifact bound", ErrPolicy)
	}
	agentArtifacts := map[string][]byte{plan.Program.ArtifactID: content}
	// The verifier sees a fresh copy after the agent-facing state has closed.
	verifierArtifacts := cloneArtifactMap(agentArtifacts)
	resultArtifacts := artifactSlice(agentArtifacts)
	clearArtifactMap(agentArtifacts)
	passed := verifyReference(plan.Verifier, verifierArtifacts)
	clearArtifactMap(verifierArtifacts)
	if err := contextCause(runContext); err != nil {
		clearArtifacts(resultArtifacts)
		return interruptedReceipt(plan, admitted.planSHA, inputSHA, referenceInputBytes(inputs),
			referenceInputEntries(definitions, fixture, skill), 1, err)
	}
	receiptArtifacts := make([]ReceiptArtifact, len(resultArtifacts))
	for index, artifact := range resultArtifacts {
		receiptArtifacts[index] = ReceiptArtifact{ID: artifact.ID, SHA256: sha256Hex(artifact.Data), Bytes: uint64(len(artifact.Data))}
	}
	verdict := VerdictFailed
	if passed {
		verdict = VerdictSucceeded
	}
	receipt := Receipt{Schema: ReceiptSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		ContractSHA256: plan.ContractSHA256, PlanSHA256: admitted.planSHA, InputSHA256: inputSHA,
		InputBytes: referenceInputBytes(inputs), InputEntries: referenceInputEntries(definitions, fixture, skill), Operations: 1,
		Artifacts: receiptArtifacts, ArtifactSetSHA256: artifactSetSHA256(receiptArtifacts), Verdict: verdict,
		VerifierEvidenceSHA256: verifierEvidenceSHA256(plan.Verifier, receiptArtifacts, passed),
		Termination:            PresenceObserved, Cleanup: PresenceObserved, Network: PresenceObserved, Credentials: PresenceObserved}
	if err := ValidateReceipt(plan, receipt); err != nil {
		clearArtifacts(resultArtifacts)
		return RunResult{}, err
	}
	return RunResult{Receipt: receipt, Artifacts: resultArtifacts}, nil
}

func mountDigestsMatch(mounts []Mount, definitions, fixture, skill string) bool {
	return len(mounts) == 3 && mounts[0].ID == MountDefinitions && mounts[0].ContentSHA256 == definitions &&
		mounts[1].ID == MountFixture && mounts[1].ContentSHA256 == fixture && mounts[2].ID == MountSkill && mounts[2].ContentSHA256 == skill
}

func validateReferenceActionInputs(plan Plan, definitions, fixture, skill *snapshot) error {
	if plan.Program.Kind != ProgramReferenceCopy {
		return nil
	}
	source := map[MountID]*snapshot{MountDefinitions: definitions, MountFixture: fixture, MountSkill: skill}[plan.Program.SourceMount]
	content, ok := source.files[plan.Program.SourcePath]
	declaration := declaration(plan.Artifacts, plan.Program.ArtifactID)
	if !ok || uint64(len(content)) > declaration.MaxBytes || uint64(len(content)) > plan.Resources.MaxOutputBytes ||
		plan.Resources.MaxArtifacts < 1 || plan.Resources.MaxOperations < 1 {
		return fmt.Errorf("%w: reference action", ErrPolicy)
	}
	return nil
}

func referenceSnapshotsWithinEntryLimit(limit uint32, values ...*snapshot) bool {
	entries := uint64(0)
	for _, value := range values {
		entries += uint64(len(value.entries))
	}
	return entries <= uint64(limit)
}

func referenceInputBytes(inputs ReferenceInputs) uint64 {
	return uint64(len(inputs.Fixture)) + uint64(len(inputs.Skill)) + uint64(len(inputs.Definitions))
}

func referenceInputEntries(values ...*snapshot) uint32 {
	entries := uint64(0)
	for _, value := range values {
		entries += uint64(len(value.entries))
	}
	return uint32(entries) // #nosec G115 -- admitted aggregate is bounded by MaxSnapshotEntries.
}

func verifyReference(verifier Verifier, artifacts map[string][]byte) bool {
	if verifier.Kind != VerifierSHA256Equals {
		return false
	}
	content, ok := artifacts[verifier.ArtifactID]
	return ok && sha256Hex(content) == verifier.ExpectedSHA256
}

func interruptedResult(plan Plan, planSHA string, err error) (RunResult, error) {
	return interruptedReceipt(plan, planSHA, declaredInputSHA256(plan.Mounts), 0, 0, 0, err)
}

func interruptedReceipt(plan Plan, planSHA, inputSHA string, inputBytes uint64, inputEntries, operations uint32, err error) (RunResult, error) {
	receipt := Receipt{Schema: ReceiptSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		ContractSHA256: plan.ContractSHA256, PlanSHA256: planSHA, InputSHA256: inputSHA,
		InputBytes: inputBytes, InputEntries: inputEntries, Operations: operations, Artifacts: []ReceiptArtifact{},
		ArtifactSetSHA256: artifactSetSHA256([]ReceiptArtifact{}), Verdict: VerdictUnknown,
		Termination: PresenceObserved,
		Cleanup:     PresenceObserved, Network: PresenceObserved, Credentials: PresenceObserved}
	receipt.VerifierEvidenceSHA256 = unknownEvidenceSHA256(receipt)
	if validateErr := ValidateReceipt(plan, receipt); validateErr != nil {
		return RunResult{}, validateErr
	}
	return RunResult{Receipt: receipt, Artifacts: []Artifact{}}, fmt.Errorf("%w: %w", ErrInterrupted, err)
}

func contextCause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func cloneArtifactMap(source map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(source))
	for id, data := range source {
		result[id] = slices.Clone(data)
	}
	return result
}

func artifactSlice(source map[string][]byte) []Artifact {
	result := make([]Artifact, 0, len(source))
	for id, data := range source {
		result = append(result, Artifact{ID: id, Data: slices.Clone(data)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func clearArtifactMap(values map[string][]byte) {
	for id, data := range values {
		clear(data)
		delete(values, id)
	}
}
func clearArtifacts(values []Artifact) {
	for index := range values {
		clear(values[index].Data)
		values[index].Data = nil
	}
}

func artifactSetSHA256(artifacts []ReceiptArtifact) string {
	data, _ := json.Marshal(artifacts)
	return hashDomain("artifact-set", data)
}

func verifierEvidenceSHA256(verifier Verifier, artifacts []ReceiptArtifact, passed bool) string {
	data, _ := json.Marshal(struct {
		Verifier  Verifier          `json:"verifier"`
		Artifacts []ReceiptArtifact `json:"artifacts"`
		Passed    bool              `json:"passed"`
	}{verifier, artifacts, passed})
	return hashDomain("verifier-evidence", data)
}

func receiptVerifierDecision(verifier Verifier, artifacts []ReceiptArtifact) (bool, bool) {
	if verifier.Kind != VerifierSHA256Equals {
		return false, false
	}
	for _, artifact := range artifacts {
		if artifact.ID == verifier.ArtifactID {
			return artifact.SHA256 == verifier.ExpectedSHA256, true
		}
	}
	return false, true
}

func unknownEvidenceSHA256(receipt Receipt) string {
	data, _ := json.Marshal(struct {
		InputSHA256       string   `json:"input_sha256"`
		InputBytes        uint64   `json:"input_bytes"`
		InputEntries      uint32   `json:"input_entries"`
		Operations        uint32   `json:"operations"`
		ArtifactSetSHA256 string   `json:"artifact_set_sha256"`
		Termination       Presence `json:"termination"`
		Cleanup           Presence `json:"cleanup"`
		Network           Presence `json:"network"`
		Credentials       Presence `json:"credentials"`
	}{receipt.InputSHA256, receipt.InputBytes, receipt.InputEntries, receipt.Operations, receipt.ArtifactSetSHA256,
		receipt.Termination, receipt.Cleanup, receipt.Network, receipt.Credentials})
	return hashDomain("unknown-evidence", data)
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func IsInterrupted(err error) bool { return errors.Is(err, ErrInterrupted) }

func clearReferenceInputs(inputs *ReferenceInputs) {
	if inputs == nil {
		return
	}
	clear(inputs.Fixture)
	clear(inputs.Skill)
	clear(inputs.Definitions)
	inputs.Fixture = nil
	inputs.Skill = nil
	inputs.Definitions = nil
}
