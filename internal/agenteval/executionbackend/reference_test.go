package executionbackend

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func referenceFixture(t *testing.T) (Contract, Plan) {
	t.Helper()
	fixture := archiveFixture(t, tarEntry{name: "input.txt", data: []byte("fixture")})
	skill := archiveFixture(t, tarEntry{name: "SKILL.md", data: []byte("skill")})
	definitions := archiveFixture(t, tarEntry{name: "task.json", data: []byte("definition")})
	fixtureSHA, _ := ArchiveSHA256(fixture, MaxArchiveBytes, MaxSnapshotEntries)
	skillSHA, _ := ArchiveSHA256(skill, MaxArchiveBytes, MaxSnapshotEntries)
	definitionsSHA, _ := ArchiveSHA256(definitions, MaxArchiveBytes, MaxSnapshotEntries)
	contract, err := ReferenceContract()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewReferencePlan(contract, ReferencePlanOptions{FixtureSHA256: fixtureSHA, SkillSHA256: skillSHA, DefinitionsSHA256: definitionsSHA,
		Resources: ResourcePolicy{DeadlineMillis: 5000, MaxInputBytes: MaxArchiveBytes, MaxOutputBytes: MaxArtifactBytes,
			MaxEntries: MaxSnapshotEntries, MaxArtifacts: 1, MaxOperations: 1},
		Artifacts: []ArtifactDeclaration{{ID: "result", MaxBytes: 1024, Privacy: PrivacyPublic}},
		Program:   Program{Kind: ProgramReferenceCopy, SourceMount: MountFixture, SourcePath: "input.txt", ArtifactID: "result"},
		Verifier:  Verifier{Kind: VerifierSHA256Equals, ArtifactID: "result", ExpectedSHA256: sha256Hex([]byte("fixture"))}})
	if err != nil {
		t.Fatal(err)
	}
	return contract, plan
}

func TestHermeticReferenceBackendIsContentAddressedAndCopyIsolated(t *testing.T) {
	contract, plan := referenceFixture(t)
	admitted, err := Admit(contract, plan)
	if err != nil {
		t.Fatal(err)
	}
	fixture := archiveFixture(t, tarEntry{name: "input.txt", data: []byte("fixture")})
	skill := archiveFixture(t, tarEntry{name: "SKILL.md", data: []byte("skill")})
	definitions := archiveFixture(t, tarEntry{name: "task.json", data: []byte("definition")})
	original := append([]byte{}, fixture...)
	result, err := RunReference(context.Background(), admitted, ReferenceInputs{Fixture: fixture, Skill: skill, Definitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Verdict != VerdictSucceeded || result.Receipt.Network != PresenceObserved || result.Receipt.Credentials != PresenceObserved ||
		len(result.Artifacts) != 1 || string(result.Artifacts[0].Data) != "fixture" {
		t.Fatalf("result=%+v", result)
	}
	result.Artifacts[0].Data[0] = 'X'
	if string(fixture) != string(original) {
		t.Fatal("caller input was mutated")
	}
	repeated, err := RunReference(context.Background(), admitted, ReferenceInputs{Fixture: fixture, Skill: skill, Definitions: definitions})
	if err != nil || repeated.Receipt.ArtifactSetSHA256 != result.Receipt.ArtifactSetSHA256 {
		t.Fatalf("repeat=%+v err=%v", repeated, err)
	}

	fixture[len(fixture)/2] ^= 1
	if _, err := RunReference(context.Background(), admitted, ReferenceInputs{Fixture: fixture, Skill: skill, Definitions: definitions}); err == nil {
		t.Fatal("changed fixture was admitted")
	}
}

func TestHermeticReferenceBackendCancellationClosesEmptyProcessTree(t *testing.T) {
	contract, copyPlan := referenceFixture(t)
	waitPlan := copyPlan
	waitPlan.Program = Program{Kind: ProgramWaitForCancel}
	waitPlan.Artifacts = []ArtifactDeclaration{}
	waitPlan.Verifier = Verifier{Kind: VerifierProfileDecision}
	waitPlan.Resources.MaxArtifacts = 0
	admitted, err := Admit(contract, waitPlan)
	if err != nil {
		t.Fatal(err)
	}
	fixture := archiveFixture(t, tarEntry{name: "input.txt", data: []byte("fixture")})
	skill := archiveFixture(t, tarEntry{name: "SKILL.md", data: []byte("skill")})
	definitions := archiveFixture(t, tarEntry{name: "task.json", data: []byte("definition")})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := RunReference(ctx, admitted, ReferenceInputs{Fixture: fixture, Skill: skill, Definitions: definitions})
	if !errors.Is(err, ErrInterrupted) || result.Receipt.Termination != PresenceObserved || result.Receipt.Cleanup != PresenceObserved || result.Receipt.Verdict != VerdictUnknown {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Receipt.InputSHA256 != declaredInputSHA256(waitPlan.Mounts) {
		t.Fatalf("interrupted input identity=%q", result.Receipt.InputSHA256)
	}
	if result.Receipt.InputBytes == 0 || result.Receipt.InputEntries != 3 || result.Receipt.Operations != 1 {
		t.Fatalf("interrupted usage=%+v", result.Receipt)
	}
}

func TestHermeticReferenceBackendRejectsUndeclaredOrOversizedArtifacts(t *testing.T) {
	contract, plan := referenceFixture(t)
	undeclared := clonePlan(plan)
	undeclared.Program.ArtifactID = "other"
	if _, err := Admit(contract, undeclared); err == nil {
		t.Fatal("undeclared artifact admitted")
	}
	oversized := clonePlan(plan)
	oversized.Artifacts[0].MaxBytes = 1
	admitted, err := Admit(contract, oversized)
	if err != nil {
		t.Fatal(err)
	}
	fixture := archiveFixture(t, tarEntry{name: "input.txt", data: []byte("fixture")})
	skill := archiveFixture(t, tarEntry{name: "SKILL.md", data: []byte("skill")})
	definitions := archiveFixture(t, tarEntry{name: "task.json", data: []byte("definition")})
	if _, err := RunReference(context.Background(), admitted, ReferenceInputs{Fixture: fixture, Skill: skill, Definitions: definitions}); !errors.Is(err, ErrPolicy) {
		t.Fatalf("oversize err=%v", err)
	}

	wrong := clonePlan(plan)
	wrong.Verifier.ExpectedSHA256 = strings.Repeat("0", 64)
	admitted, err = Admit(contract, wrong)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunReference(context.Background(), admitted, ReferenceInputs{Fixture: fixture, Skill: skill, Definitions: definitions})
	if err != nil || result.Receipt.Verdict != VerdictFailed {
		t.Fatalf("verdict=%+v err=%v", result, err)
	}
}

func TestHermeticReferenceBackendOwnsInputsAndEnforcesAggregateEntryBound(t *testing.T) {
	contract, plan := referenceFixture(t)
	fixture := archiveFixture(t, tarEntry{name: "input.txt", data: []byte("fixture")})
	skill := archiveFixture(t, tarEntry{name: "SKILL.md", data: []byte("skill")})
	definitions := archiveFixture(t, tarEntry{name: "task.json", data: []byte("definition")})
	admitted, err := Admit(contract, plan)
	if err != nil {
		t.Fatal(err)
	}
	inputs := ReferenceInputs{Fixture: fixture, Skill: skill, Definitions: definitions}
	owned, err := PrepareReferenceInputs(context.Background(), admitted, inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer clearReferenceInputs(&owned)
	clear(fixture)
	clear(skill)
	clear(definitions)
	result, err := RunReference(context.Background(), admitted, owned)
	if err != nil || result.Receipt.Verdict != VerdictSucceeded {
		t.Fatalf("owned result=%+v err=%v", result, err)
	}

	bounded := clonePlan(plan)
	bounded.Resources.MaxEntries = 2
	admitted, err = Admit(contract, bounded)
	if err != nil {
		t.Fatal(err)
	}
	inputs = ReferenceInputs{
		Fixture:     archiveFixture(t, tarEntry{name: "input.txt", data: []byte("fixture")}),
		Skill:       archiveFixture(t, tarEntry{name: "SKILL.md", data: []byte("skill")}),
		Definitions: archiveFixture(t, tarEntry{name: "task.json", data: []byte("definition")}),
	}
	if _, err := PrepareReferenceInputs(context.Background(), admitted, inputs); !errors.Is(err, ErrPolicy) {
		t.Fatalf("aggregate entry err=%v", err)
	}
}

func TestExecutionBackendReceiptCoverageAndUnknownEvidenceFailClosed(t *testing.T) {
	contract, plan := referenceFixture(t)
	admitted, err := Admit(contract, plan)
	if err != nil {
		t.Fatal(err)
	}
	fixture := archiveFixture(t, tarEntry{name: "input.txt", data: []byte("fixture")})
	skill := archiveFixture(t, tarEntry{name: "SKILL.md", data: []byte("skill")})
	definitions := archiveFixture(t, tarEntry{name: "task.json", data: []byte("definition")})
	result, err := RunReference(context.Background(), admitted, ReferenceInputs{Fixture: fixture, Skill: skill, Definitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	coverageDrift := result.Receipt
	coverageDrift.Network = PresenceUnknown
	if err := ValidateReceipt(plan, coverageDrift); err == nil {
		t.Fatal("hermetic coverage drift passed")
	}
	inputDrift := result.Receipt
	inputDrift.InputSHA256 = strings.Repeat("0", 64)
	if err := ValidateReceipt(plan, inputDrift); err == nil {
		t.Fatal("receipt with a forged input identity passed")
	}
	inputUsageDrift := result.Receipt
	inputUsageDrift.InputBytes = plan.Resources.MaxInputBytes + 1
	if err := ValidateReceipt(plan, inputUsageDrift); err == nil {
		t.Fatal("receipt exceeded the input-byte budget")
	}
	inputUsageDrift = result.Receipt
	inputUsageDrift.InputEntries = plan.Resources.MaxEntries + 1
	if err := ValidateReceipt(plan, inputUsageDrift); err == nil {
		t.Fatal("receipt exceeded the input-entry budget")
	}
	operationDrift := result.Receipt
	operationDrift.Operations = 0
	if err := ValidateReceipt(plan, operationDrift); err == nil {
		t.Fatal("terminal receipt claimed no operation")
	}
	verdictDrift := result.Receipt
	verdictDrift.Artifacts = append([]ReceiptArtifact{}, result.Receipt.Artifacts...)
	verdictDrift.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	verdictDrift.ArtifactSetSHA256 = artifactSetSHA256(verdictDrift.Artifacts)
	verdictDrift.VerifierEvidenceSHA256 = verifierEvidenceSHA256(plan.Verifier, verdictDrift.Artifacts, true)
	if err := ValidateReceipt(plan, verdictDrift); err == nil {
		t.Fatal("receipt verdict contradicted deterministic verifier")
	}
	unknown := result.Receipt
	unknown.Verdict = VerdictUnknown
	unknown.Artifacts = []ReceiptArtifact{}
	unknown.ArtifactSetSHA256 = artifactSetSHA256(unknown.Artifacts)
	unknown.VerifierEvidenceSHA256 = unknownEvidenceSHA256(unknown)
	if err := ValidateReceipt(plan, unknown); err != nil {
		t.Fatalf("valid unknown receipt: %v", err)
	}
	unknown.InputBytes++
	if err := ValidateReceipt(plan, unknown); err == nil {
		t.Fatal("unknown usage changed without changing its evidence")
	}

	bounded := plan
	bounded.Artifacts = append([]ArtifactDeclaration{}, plan.Artifacts...)
	bounded.Artifacts = append(bounded.Artifacts, ArtifactDeclaration{ID: "second", MaxBytes: 1, Privacy: PrivacyPublic})
	bounded.Resources.MaxArtifacts = 2
	bounded.Resources.MaxOperations = 2
	bounded.Resources.MaxOutputBytes = 1
	forged := result.Receipt
	forged.PlanSHA256, _ = PlanSHA256(bounded)
	forged.Artifacts = []ReceiptArtifact{{ID: "result", SHA256: plan.Verifier.ExpectedSHA256, Bytes: 1},
		{ID: "second", SHA256: strings.Repeat("1", 64), Bytes: 1}}
	forged.ArtifactSetSHA256 = artifactSetSHA256(forged.Artifacts)
	forged.VerifierEvidenceSHA256 = verifierEvidenceSHA256(bounded.Verifier, forged.Artifacts, true)
	if err := ValidateReceipt(bounded, forged); err == nil {
		t.Fatal("receipt exceeded the aggregate output-byte budget")
	}
	bounded.Resources.MaxOutputBytes = 2
	bounded.Resources.MaxArtifacts = 1
	forged.PlanSHA256, _ = PlanSHA256(bounded)
	if err := ValidateReceipt(bounded, forged); err == nil {
		t.Fatal("receipt exceeded the artifact budget")
	}
	bounded.Resources.MaxArtifacts = 2
	bounded.Resources.MaxOperations = 1
	forged.PlanSHA256, _ = PlanSHA256(bounded)
	forged.Operations = 2
	if err := ValidateReceipt(bounded, forged); err == nil {
		t.Fatal("receipt exceeded the operation budget")
	}
}
