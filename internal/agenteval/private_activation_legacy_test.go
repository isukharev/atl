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

func TestLegacyPrivateActivationProjectionAttachesPlanRejectionButNotDigestMismatch(t *testing.T) {
	valid := legacyPrivateActivationTestPlan()
	digest, err := legacyPrivateActivationStudyPlanSHA256(valid)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("invalid plan", func(t *testing.T) {
		broken := valid
		broken.Cells = append([]PrivateActivationStudyCell(nil), valid.Cells...)
		broken.Cells[0].SkillActivation = SkillActivationExplicit
		_, err := projectLegacyPrivateActivationLifecycle(broken, digest, nil)
		assertPrivateActivationLifecycleCode(t, err, "legacy_plan_hash")
		causes := privateActivationLifecycleErrorCauses(t, err)
		if len(causes) != 1 {
			t.Fatalf("causes=%v, want the classified schema-v1 plan rejection retained", causes)
		}
		var nested interface{ Code() string }
		if !errors.As(causes[0], &nested) || nested.Code() != "legacy_unbalanced_roster" {
			t.Fatalf("cause %v is not the nested schema-v1 plan verdict", causes[0])
		}
	})

	t.Run("digest mismatch only", func(t *testing.T) {
		_, err := projectLegacyPrivateActivationLifecycle(valid, differentValidSHA256(t, digest), nil)
		assertPrivateActivationLifecycleCode(t, err, "legacy_plan_hash")
		if causes := privateActivationLifecycleErrorCauses(t, err); len(causes) != 0 {
			t.Fatalf("causes=%v, want none for a digest-only mismatch", causes)
		}
	})
}

func TestLegacyPrivateActivationEventHashMismatchStaysCauseFree(t *testing.T) {
	plan := legacyPrivateActivationTestPlan()
	digest, err := legacyPrivateActivationStudyPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	events := completeLegacyPrivateActivationTestEvents(t, plan, digest)

	tampered := append([]PrivateActivationStudyEvent(nil), events...)
	// A syntactically valid digest clears the chain gate, so the recomputed-hash
	// comparison is what rejects this event.
	tampered[0].EventSHA256 = differentValidSHA256(t, events[0].EventSHA256)
	_, hashErr := projectLegacyPrivateActivationLifecycle(plan, digest, tampered)
	assertPrivateActivationLifecycleCode(t, hashErr, "legacy_event_hash")
	if causes := privateActivationLifecycleErrorCauses(t, hashErr); len(causes) != 0 {
		t.Fatalf("causes=%v, want none for a recomputed-digest mismatch", causes)
	}

	chained := append([]PrivateActivationStudyEvent(nil), events...)
	chained[0].EventSHA256 = "not-a-digest"
	_, chainErr := projectLegacyPrivateActivationLifecycle(plan, digest, chained)
	assertPrivateActivationLifecycleCode(t, chainErr, "legacy_event_chain")
	if causes := privateActivationLifecycleErrorCauses(t, chainErr); len(causes) != 0 {
		t.Fatalf("causes=%v, want none for a chain verdict", causes)
	}
}

func TestLegacyPrivateActivationRejectedIdentifiersStayOutOfTheCauseTree(t *testing.T) {
	const rejected = "../private-escape"
	valid := legacyPrivateActivationTestPlan()
	tests := map[string]struct {
		code   string
		mutate func(*PrivateActivationStudyPlan)
	}{
		"study id": {"legacy_plan", func(plan *PrivateActivationStudyPlan) { plan.StudyID = rejected }},
		"cell id":  {"legacy_cell", func(plan *PrivateActivationStudyPlan) { plan.Cells[0].CellID = rejected }},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			plan := valid
			plan.Cells = append([]PrivateActivationStudyCell(nil), valid.Cells...)
			test.mutate(&plan)
			err := validateLegacyPrivateActivationStudyPlan(plan)
			assertPrivateActivationLifecycleCode(t, err, test.code)
			// The identifier gate renders the rejected private name, so its
			// error is deliberately dropped instead of attached.
			if causes := privateActivationLifecycleErrorCauses(t, err); len(causes) != 0 {
				t.Fatalf("causes=%v, want the rejected identifier kept out of the unwrap tree", causes)
			}
			if strings.Contains(err.Error(), rejected) {
				t.Fatalf("message leaked the rejected identifier: %q", err.Error())
			}
		})
	}
}

func TestLegacyPrivateActivationPlanStateNestsLifecycleRejectionsOnly(t *testing.T) {
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
	validPlan := privatePlan{Kind: PrivateRunSetKindActivationStudy, StudyContract: &study, Items: items}
	validState := privatePlanState{
		SchemaVersion: 2, Status: "completed", CompletedCells: privateActivationCellIDsFromRoster(study.Cells),
		EstimatedCostMicroUSD: 12, CompletedAt: "2026-07-19T12:00:00Z", Events: events,
	}
	if err := validateLegacyPrivateActivationPlanState(validPlan, validState); err != nil {
		t.Fatal(err)
	}

	t.Run("plan hash rejection", func(t *testing.T) {
		broken := study
		broken.Cells = append([]PrivateActivationStudyCell(nil), study.Cells...)
		broken.Cells[0].SkillActivation = SkillActivationExplicit
		plan := validPlan
		plan.StudyContract = &broken
		err := validateLegacyPrivateActivationPlanState(plan, validState)
		assertLegacyPrivateActivationPlanStateCode(t, err)
		causes := legacyPrivateActivationPlanStateCauses(t, err)
		if len(causes) != 1 || !errors.Is(causes[0], ErrPrivateActivationLifecycle) {
			t.Fatalf("causes=%v, want the classified lifecycle rejection retained", causes)
		}
		var nested interface{ Code() string }
		if !errors.As(causes[0], &nested) || nested.Code() != "legacy_unbalanced_roster" {
			t.Fatalf("cause %v is not the nested lifecycle verdict", causes[0])
		}
	})

	t.Run("projection rejection", func(t *testing.T) {
		state := validState
		state.Events = append([]PrivateActivationStudyEvent(nil), validState.Events...)
		state.Events[0].EventSHA256 = differentValidSHA256(t, validState.Events[0].EventSHA256)
		err := validateLegacyPrivateActivationPlanState(validPlan, state)
		assertLegacyPrivateActivationPlanStateCode(t, err)
		causes := legacyPrivateActivationPlanStateCauses(t, err)
		if len(causes) != 1 || !errors.Is(causes[0], ErrPrivateActivationLifecycle) {
			t.Fatalf("causes=%v, want the classified lifecycle rejection retained", causes)
		}
		var nested interface{ Code() string }
		if !errors.As(causes[0], &nested) || nested.Code() != "legacy_event_hash" {
			t.Fatalf("cause %v is not the nested lifecycle verdict", causes[0])
		}
		// The outer plan-state classification keeps precedence.
		var outer interface{ Code() string }
		if !errors.As(err, &outer) || outer.Code() != "legacy_study_state" {
			t.Fatalf("error %v lost its outer code", err)
		}
	})

	// Each rejection below is decided by comparing the accepted projection with
	// the recorded state, so none of them carries a cause.
	cleanTests := map[string]func(*privatePlan, *privatePlanState){
		"schema verdict":      func(_ *privatePlan, state *privatePlanState) { state.SchemaVersion = 3 },
		"cost comparison":     func(_ *privatePlan, state *privatePlanState) { state.EstimatedCostMicroUSD = 11 },
		"cell comparison":     func(_ *privatePlan, state *privatePlanState) { state.CompletedCells = nil },
		"stop comparison":     func(_ *privatePlan, state *privatePlanState) { state.StopReason = PrivateActivationStopReservation },
		"interrupted verdict": func(_ *privatePlan, state *privatePlanState) { state.Status = "interrupted" },
		"running verdict":     func(_ *privatePlan, state *privatePlanState) { state.Status = "running" },
		"stopped verdict":     func(_ *privatePlan, state *privatePlanState) { state.Status = "stopped" },
		"unknown status":      func(_ *privatePlan, state *privatePlanState) { state.Status = "archived" },
		"completion stamp":    func(_ *privatePlan, state *privatePlanState) { state.CompletedAt = "" },
	}
	for name, mutate := range cleanTests {
		t.Run(name, func(t *testing.T) {
			plan, state := validPlan, validState
			mutate(&plan, &state)
			err := validateLegacyPrivateActivationPlanState(plan, state)
			assertLegacyPrivateActivationPlanStateCode(t, err)
			if causes := legacyPrivateActivationPlanStateCauses(t, err); len(causes) != 0 {
				t.Fatalf("causes=%v, want none for a state comparison", causes)
			}
		})
	}
}

func assertLegacyPrivateActivationPlanStateCode(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrPrivatePlanRejected) {
		t.Fatalf("err=%v, want the private plan sentinel", err)
	}
	if got, want := err.Error(), ErrPrivatePlanRejected.Error()+": legacy_study_state"; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
}

func legacyPrivateActivationPlanStateCauses(t *testing.T, err error) []error {
	t.Helper()
	multi, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("%T does not unwrap to multiple errors", err)
	}
	tree := multi.Unwrap()
	if len(tree) == 0 || !errors.Is(tree[0], ErrPrivatePlanRejected) {
		t.Fatalf("unwrap tree=%v, want the sentinel first", tree)
	}
	return tree[1:]
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
