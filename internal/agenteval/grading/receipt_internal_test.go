package grading

import (
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/core"
)

func TestProducedReceiptCannotBypassAdmittedEvidenceLimit(t *testing.T) {
	support := make(map[CheckKind]Support, len(closedCheckKinds))
	for _, kind := range closedCheckKinds {
		support[kind] = SupportSupported
	}
	contract, err := NewContract("bounded-grading", "1", strings.Repeat("1", 64), strings.Repeat("2", 64), builtinModes(), support)
	if err != nil {
		t.Fatal(err)
	}
	contract.Limits.MaxEvidenceItems = 1
	contractSHA, err := ContractSHA256(contract)
	if err != nil {
		t.Fatal(err)
	}
	task := core.Task{ID: "task", Checks: []core.Check{{ID: "mechanical", Weight: 1}}}
	fixture := core.Fixture{ID: "fixture"}
	treatment := core.Treatment{ID: "treatment"}
	identity := core.AttemptIdentity{Plan: "plan", Task: task.ID, Treatment: treatment.ID, Ordinal: 1}
	inputSHA, err := CoreAttemptInputSHA256(identity, task, fixture, treatment)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{Schema: PlanSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		ContractSHA256: contractSHA, Mode: ModeDeterministic, InputProjectionSHA256: inputSHA,
		EnvironmentSHA256: strings.Repeat("4", 64), Checks: []Check{{ID: "mechanical", Kind: CheckFileExists,
			Visibility: VisibilityPublic, FileExists: &FileExistsRule{EvidenceID: "proof", Expected: true}}},
		Limits: PlanLimits{DeadlineMillis: 1000, MaxInputBytes: MaxEvidenceBytes, MaxOutputBytes: MaxReceiptBytes}}
	admitted, err := Admit(contract, plan)
	if err != nil {
		t.Fatal(err)
	}
	catalog := []Citation{
		{EvidenceID: "proof", Kind: EvidenceFile, Visibility: VisibilityPublic, SHA256: strings.Repeat("5", 64)},
		{EvidenceID: "surplus", Kind: EvidenceFile, Visibility: VisibilityPublic, SHA256: strings.Repeat("6", 64)},
	}
	prepared := &PreparedEvidence{catalog: catalog, digest: evidenceCatalogSHA256(plan.InputProjectionSHA256, catalog)}
	receipt := newReceipt(admitted, prepared, []Decision{{CheckID: "mechanical", Presence: PresenceObserved, Passed: true,
		Authority: AuthorityDeterministic, Citations: []Citation{catalog[0]}}}, []ReviewerReceipt{}, notApplicableUsage(), []Disagreement{})
	if err := ValidateReceipt(plan, receipt); err != nil {
		t.Fatalf("fixture is not globally valid: %v", err)
	}
	if err := validateProducedReceipt(admitted, receipt); err == nil {
		t.Fatal("receipt exceeded its admitted contract evidence limit")
	}
	if _, err := NewCoreGrader(identity, task, fixture, treatment, admitted, receipt); err == nil {
		t.Fatal("receipt-backed core grader bypassed its admitted evidence limit")
	}
}
