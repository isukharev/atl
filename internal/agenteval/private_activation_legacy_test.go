package agenteval

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyPrivateActivationLifecycleRemainsReadOnlyInspectable(t *testing.T) {
	plan := legacyPrivateActivationTestPlan()
	if err := plan.Validate(); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("current lifecycle accepted legacy plan: %v", err)
	}
	digest, err := legacyPrivateActivationStudyPlanSHA256(plan)
	if err != nil || !validSHA256(digest) {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	pending, err := projectLegacyPrivateActivationLifecycle(plan, digest, nil)
	if err != nil || pending.status != PrivateActivationStudyPending || pending.reserved != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}

	events := make([]PrivateActivationStudyEvent, 0, len(plan.Cells)*5)
	for _, cell := range plan.Cells {
		events = appendLegacyPrivateActivationTestEvent(t, events, digest, PrivateActivationStudyEvent{
			Type: PrivateActivationEventReserved, CellID: cell.CellID, ReservedCostMicroUSD: cell.MaxEstimatedCostMicroUSD,
		})
		events = appendLegacyPrivateActivationTestEvent(t, events, digest, PrivateActivationStudyEvent{Type: PrivateActivationEventLaunched, CellID: cell.CellID})
		events = appendLegacyPrivateActivationTestEvent(t, events, digest, PrivateActivationStudyEvent{Type: PrivateActivationEventProviderCommitted, CellID: cell.CellID})
		events = appendLegacyPrivateActivationTestEvent(t, events, digest, PrivateActivationStudyEvent{
			Type: PrivateActivationEventReceipt, CellID: cell.CellID, ReceiptSHA256: strings.Repeat("a", 64),
			CostKnown: true, DetectedCostMicroUSD: 3, ProviderCompleted: true, PersistenceComplete: true, ContainmentCertain: true,
		})
		events = appendLegacyPrivateActivationTestEvent(t, events, digest, PrivateActivationStudyEvent{
			Type: PrivateActivationEventDefinitive, CellID: cell.CellID, Outcome: PrivateActivationOutcomeOracleFailure,
		})
	}
	completed, err := projectLegacyPrivateActivationLifecycle(plan, digest, events)
	if err != nil {
		t.Fatal(err)
	}
	if completed.status != PrivateActivationStudyCompleted || completed.reserved != 40 || completed.detectedCostMicroUSD != 12 ||
		!reflect.DeepEqual(completed.completedCells, privateActivationCellIDsFromRoster(plan.Cells)) {
		t.Fatalf("completed=%+v", completed)
	}
}

func TestLegacyPrivateActivationPlanHashPreservesHistoricalEnvelope(t *testing.T) {
	plan := legacyPrivateActivationTestPlan()
	digest, err := legacyPrivateActivationStudyPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON := `{"schema_version":1,"study_id":"legacy-study","provider":"codex","cost":{"assurance":"detection_only","preventive":false,"total_authorized_microusd":45,"treatment_allocated_microusd":40,"reviewer_reserve_microusd":5},"cells":`
	if want := sha256HexBytes([]byte(wantJSON + mustLegacyPrivateActivationCellsJSON(t, plan.Cells) + `}`)); digest != want {
		t.Fatalf("legacy digest=%q want=%q", digest, want)
	}
	currentData, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	currentEncoded := string(currentData)
	if !strings.Contains(currentEncoded, `"calibration_allocated_microusd":0`) || !strings.Contains(currentEncoded, `"calibration"`) || sha256HexBytes([]byte(currentEncoded)) == digest {
		t.Fatalf("current envelope unexpectedly matches legacy: %s", currentEncoded)
	}
}

func TestLegacyPrivateActivationLifecycleRejectsHybridAndTamperedArtifacts(t *testing.T) {
	valid := legacyPrivateActivationTestPlan()
	digest, err := legacyPrivateActivationStudyPlanSHA256(valid)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*PrivateActivationStudyPlan){
		"calibration partition": func(plan *PrivateActivationStudyPlan) { plan.Cost.CalibrationAllocatedMicroUSD = 1 },
		"calibration contract": func(plan *PrivateActivationStudyPlan) {
			plan.Calibration = PrivateActivationCalibrationContract{ContractSHA256: strings.Repeat("b", 64), MaxEstimatedCostMicroUSD: 1}
		},
		"current schema": func(plan *PrivateActivationStudyPlan) { plan.SchemaVersion = PrivateActivationStudyPlanSchemaVersion },
		"unbalanced":     func(plan *PrivateActivationStudyPlan) { plan.Cells[0].SkillActivation = SkillActivationExplicit },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			plan := valid
			plan.Cells = append([]PrivateActivationStudyCell(nil), valid.Cells...)
			mutate(&plan)
			if err := validateLegacyPrivateActivationStudyPlan(plan); !errors.Is(err, ErrPrivateActivationLifecycle) {
				t.Fatalf("err=%v", err)
			}
		})
	}

	reserved := appendLegacyPrivateActivationTestEvent(t, nil, digest, PrivateActivationStudyEvent{
		Type: PrivateActivationEventReserved, CellID: valid.Cells[0].CellID, ReservedCostMicroUSD: valid.Cells[0].MaxEstimatedCostMicroUSD,
	})
	t.Run("current event schema", func(t *testing.T) {
		events := append([]PrivateActivationStudyEvent(nil), reserved...)
		events[0].SchemaVersion = PrivateActivationStudyEventSchemaVersion
		events[0].EventSHA256, _ = privateActivationEventSHA256(events[0])
		if _, err := projectLegacyPrivateActivationLifecycle(valid, digest, events); !errors.Is(err, ErrPrivateActivationLifecycle) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("calibration event", func(t *testing.T) {
		event := PrivateActivationStudyEvent{SchemaVersion: legacyPrivateActivationStudyEventSchemaVersion, Sequence: 1,
			PlanSHA256: digest, Type: PrivateActivationEventCalibrationReserved, ReservedCostMicroUSD: 1}
		event.EventSHA256, _ = privateActivationEventSHA256(event)
		if _, err := projectLegacyPrivateActivationLifecycle(valid, digest, []PrivateActivationStudyEvent{event}); !errors.Is(err, ErrPrivateActivationLifecycle) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("tampered hash", func(t *testing.T) {
		events := append([]PrivateActivationStudyEvent(nil), reserved...)
		events[0].ReservedCostMicroUSD--
		if _, err := projectLegacyPrivateActivationLifecycle(valid, digest, events); !errors.Is(err, ErrPrivateActivationLifecycle) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestLegacyPrivateActivationPlanStateValidatesForInspectionOnly(t *testing.T) {
	study := legacyPrivateActivationTestPlan()
	digest, err := legacyPrivateActivationStudyPlanSHA256(study)
	if err != nil {
		t.Fatal(err)
	}
	events := completeLegacyPrivateActivationTestEvents(t, study, digest)
	items := make([]privatePlanItem, 0, len(study.Cells))
	for _, cell := range study.Cells {
		items = append(items, privatePlanItem{CellID: cell.CellID})
	}
	plan := privatePlan{Kind: PrivateRunSetKindActivationStudy, StudyContract: &study, Items: items}
	state := privatePlanState{
		SchemaVersion: 2, Status: "completed", CompletedCells: privateActivationCellIDsFromRoster(study.Cells),
		EstimatedCostMicroUSD: 12, CompletedAt: "2026-07-19T12:00:00Z", Events: events,
	}
	if err := validateLegacyPrivateActivationPlanState(plan, state); err != nil {
		t.Fatal(err)
	}
	state.Events = append([]PrivateActivationStudyEvent(nil), state.Events...)
	state.Events[len(state.Events)-1].EventSHA256 = strings.Repeat("f", 64)
	if err := validateLegacyPrivateActivationPlanState(plan, state); !errors.Is(err, ErrPrivatePlanRejected) {
		t.Fatalf("tampered state err=%v", err)
	}
}

func legacyPrivateActivationTestPlan() PrivateActivationStudyPlan {
	return PrivateActivationStudyPlan{
		SchemaVersion: legacyPrivateActivationStudyPlanSchemaVersion,
		StudyID:       "legacy-study",
		Provider:      "codex",
		Cost: PrivateActivationCostPartitions{
			Assurance: PrivateActivationCostAssuranceDetectionOnly, TotalAuthorizedMicroUSD: 45,
			TreatmentAllocatedMicroUSD: 40, ReviewerReserveMicroUSD: 5,
		},
		Cells: privateActivationTestRoster(10),
	}
}

func appendLegacyPrivateActivationTestEvent(t *testing.T, events []PrivateActivationStudyEvent, planSHA256 string, event PrivateActivationStudyEvent) []PrivateActivationStudyEvent {
	t.Helper()
	event.SchemaVersion = legacyPrivateActivationStudyEventSchemaVersion
	event.Sequence = len(events) + 1
	event.PlanSHA256 = planSHA256
	if len(events) != 0 {
		event.PreviousSHA256 = events[len(events)-1].EventSHA256
	}
	var err error
	event.EventSHA256, err = privateActivationEventSHA256(event)
	if err != nil {
		t.Fatal(err)
	}
	return append(events, event)
}

func completeLegacyPrivateActivationTestEvents(t *testing.T, plan PrivateActivationStudyPlan, digest string) []PrivateActivationStudyEvent {
	t.Helper()
	events := make([]PrivateActivationStudyEvent, 0, len(plan.Cells)*5)
	for _, cell := range plan.Cells {
		events = appendLegacyPrivateActivationTestEvent(t, events, digest, PrivateActivationStudyEvent{
			Type: PrivateActivationEventReserved, CellID: cell.CellID, ReservedCostMicroUSD: cell.MaxEstimatedCostMicroUSD,
		})
		events = appendLegacyPrivateActivationTestEvent(t, events, digest, PrivateActivationStudyEvent{Type: PrivateActivationEventLaunched, CellID: cell.CellID})
		events = appendLegacyPrivateActivationTestEvent(t, events, digest, PrivateActivationStudyEvent{Type: PrivateActivationEventProviderCommitted, CellID: cell.CellID})
		events = appendLegacyPrivateActivationTestEvent(t, events, digest, PrivateActivationStudyEvent{
			Type: PrivateActivationEventReceipt, CellID: cell.CellID, ReceiptSHA256: strings.Repeat("a", 64),
			CostKnown: true, DetectedCostMicroUSD: 3, ProviderCompleted: true, PersistenceComplete: true, ContainmentCertain: true,
		})
		events = appendLegacyPrivateActivationTestEvent(t, events, digest, PrivateActivationStudyEvent{
			Type: PrivateActivationEventDefinitive, CellID: cell.CellID, Outcome: PrivateActivationOutcomeOracleFailure,
		})
	}
	return events
}

func privateActivationCellIDsFromRoster(cells []PrivateActivationStudyCell) []string {
	out := make([]string, 0, len(cells))
	for _, cell := range cells {
		out = append(out, cell.CellID)
	}
	return out
}

func mustLegacyPrivateActivationCellsJSON(t *testing.T, cells []PrivateActivationStudyCell) string {
	t.Helper()
	data, err := json.Marshal(cells)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
