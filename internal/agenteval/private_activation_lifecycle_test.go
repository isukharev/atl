package agenteval

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestPrivateActivationPlanBindsBalancedOrderedDetectionOnlyRoster(t *testing.T) {
	roster := privateActivationTestRoster(10)
	plan, err := NewPrivateActivationStudyPlan(PrivateActivationStudyPlanInput{
		StudyID: "study-01", TotalAuthorizedMicroUSD: 49, ReviewerReserveMicroUSD: 7,
		Calibration: privateActivationTestCalibration(2), OrderedBalancedRoster: roster,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Provider != "codex" || plan.Cost.Assurance != PrivateActivationCostAssuranceDetectionOnly || plan.Cost.Preventive ||
		plan.Cost.CalibrationAllocatedMicroUSD != 2 || plan.Cost.TreatmentAllocatedMicroUSD != 40 || !reflect.DeepEqual(plan.Cells, roster) {
		t.Fatalf("plan=%+v", plan)
	}
	first, err := plan.SHA256()
	if err != nil || !validSHA256(first) {
		t.Fatalf("digest=%q err=%v", first, err)
	}
	reordered := plan
	reordered.Cells = append([]PrivateActivationStudyCell(nil), plan.Cells...)
	reordered.Cells[0], reordered.Cells[1] = reordered.Cells[1], reordered.Cells[0]
	second, err := reordered.SHA256()
	if err != nil || first == second {
		t.Fatalf("ordered roster was not hash-bound: first=%q second=%q err=%v", first, second, err)
	}
	roster[0].CellID = "mutated"
	if plan.Cells[0].CellID == "mutated" {
		t.Fatal("constructor retained caller roster backing storage")
	}
}

func TestPrivateActivationPlanRejectsUnsafeCostAndRosterContracts(t *testing.T) {
	valid, err := NewPrivateActivationStudyPlan(PrivateActivationStudyPlanInput{
		StudyID: "study-valid", TotalAuthorizedMicroUSD: 47, ReviewerReserveMicroUSD: 5,
		Calibration: privateActivationTestCalibration(2), OrderedBalancedRoster: privateActivationTestRoster(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*PrivateActivationStudyPlan){
		"preventive claim":              func(plan *PrivateActivationStudyPlan) { plan.Cost.Preventive = true },
		"hard assurance":                func(plan *PrivateActivationStudyPlan) { plan.Cost.Assurance = "provider_hard" },
		"wrong provider":                func(plan *PrivateActivationStudyPlan) { plan.Provider = "claude-code" },
		"borrow reviewer reserve":       func(plan *PrivateActivationStudyPlan) { plan.Cost.ReviewerReserveMicroUSD-- },
		"zero reviewer reserve":         func(plan *PrivateActivationStudyPlan) { plan.Cost.ReviewerReserveMicroUSD = 0 },
		"changed treatment partition":   func(plan *PrivateActivationStudyPlan) { plan.Cost.TreatmentAllocatedMicroUSD-- },
		"changed calibration partition": func(plan *PrivateActivationStudyPlan) { plan.Cost.CalibrationAllocatedMicroUSD-- },
		"calibration contract missing":  func(plan *PrivateActivationStudyPlan) { plan.Calibration.ContractSHA256 = "" },
		"zero calibration cap":          func(plan *PrivateActivationStudyPlan) { plan.Calibration.MaxEstimatedCostMicroUSD = 0 },
		"duplicate cell":                func(plan *PrivateActivationStudyPlan) { plan.Cells[1].CellID = plan.Cells[0].CellID },
		"unbalanced roster":             func(plan *PrivateActivationStudyPlan) { plan.Cells[0].SkillActivation = SkillActivationExplicit },
		"unknown activation":            func(plan *PrivateActivationStudyPlan) { plan.Cells[0].SkillActivation = "unknown" },
		"zero cell cap":                 func(plan *PrivateActivationStudyPlan) { plan.Cells[0].MaxEstimatedCostMicroUSD = 0 },
		"contract missing":              func(plan *PrivateActivationStudyPlan) { plan.Cells[0].ContractSHA256 = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			plan := valid
			plan.Cells = append([]PrivateActivationStudyCell(nil), valid.Cells...)
			mutate(&plan)
			if err := plan.Validate(); !errors.Is(err, ErrPrivateActivationLifecycle) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	overflow := privateActivationTestRoster(1)
	overflow[0].MaxEstimatedCostMicroUSD = math.MaxInt64
	if _, err := NewPrivateActivationStudyPlan(PrivateActivationStudyPlanInput{
		StudyID: "study-overflow", TotalAuthorizedMicroUSD: math.MaxInt64, ReviewerReserveMicroUSD: 1,
		Calibration: privateActivationTestCalibration(1), OrderedBalancedRoster: overflow,
	}); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("overflow err=%v", err)
	}
}

func TestPrivateActivationCalibrationGatesTreatmentRoster(t *testing.T) {
	lifecycle := privateActivationUncalibratedTestLifecycle(t, 10, 3)
	if lifecycle.Plan.SchemaVersion != 2 {
		t.Fatalf("plan schema=%d", lifecycle.Plan.SchemaVersion)
	}
	if _, err := lifecycle.ReserveNextCell(); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("treatment reserved before calibration: %v", err)
	}
	calibration, err := lifecycle.ReserveCalibration()
	if err != nil || calibration != lifecycle.Plan.Calibration {
		t.Fatalf("calibration=%+v err=%v", calibration, err)
	}
	if _, err := lifecycle.ReserveCalibration(); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("calibration reserved twice: %v", err)
	}
	if err := lifecycle.MarkCalibrationLaunched(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkCalibrationProviderAttemptCommitted(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.RecordCalibrationReceipt(privateActivationGoodReceipt(0)); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkCalibrationSucceeded(); err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{
		PrivateActivationEventCalibrationReserved,
		PrivateActivationEventCalibrationLaunched,
		PrivateActivationEventCalibrationProviderCommitted,
		PrivateActivationEventCalibrationReceipt,
		PrivateActivationEventCalibrationSucceeded,
	}
	for index, event := range lifecycle.Events {
		if event.SchemaVersion != 2 || event.Type != wantTypes[index] || event.CellID != "" {
			t.Fatalf("calibration event %d=%+v", index, event)
		}
	}
	cell, err := lifecycle.ReserveNextCell()
	if err != nil || cell.CellID != lifecycle.Plan.Cells[0].CellID {
		t.Fatalf("first treatment=%+v err=%v", cell, err)
	}
	if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); err != nil {
		t.Fatal(err)
	}
	calibrationAttempts, treatmentAttempts := 0, 0
	for _, event := range lifecycle.Events {
		switch event.Type {
		case PrivateActivationEventCalibrationProviderCommitted:
			calibrationAttempts++
		case PrivateActivationEventProviderCommitted:
			treatmentAttempts++
		}
	}
	if calibrationAttempts != 1 || treatmentAttempts != 1 {
		t.Fatalf("calibration attempts=%d treatment attempts=%d", calibrationAttempts, treatmentAttempts)
	}
}

func TestPrivateActivationCalibrationFailureIsTerminalAtEveryActivePhase(t *testing.T) {
	phases := []string{
		PrivateActivationEventCalibrationReserved,
		PrivateActivationEventCalibrationLaunched,
		PrivateActivationEventCalibrationProviderCommitted,
		PrivateActivationEventCalibrationReceipt,
	}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			lifecycle := privateActivationUncalibratedTestLifecycle(t, 10, 1)
			if _, err := lifecycle.ReserveCalibration(); err != nil {
				t.Fatal(err)
			}
			if phase != PrivateActivationEventCalibrationReserved {
				if err := lifecycle.MarkCalibrationLaunched(); err != nil {
					t.Fatal(err)
				}
			}
			if phase == PrivateActivationEventCalibrationProviderCommitted || phase == PrivateActivationEventCalibrationReceipt {
				if err := lifecycle.MarkCalibrationProviderAttemptCommitted(); err != nil {
					t.Fatal(err)
				}
			}
			if phase == PrivateActivationEventCalibrationReceipt {
				if err := lifecycle.RecordCalibrationReceipt(privateActivationGoodReceipt(0)); err != nil {
					t.Fatal(err)
				}
			}
			if phase == PrivateActivationEventCalibrationProviderCommitted {
				if err := lifecycle.MarkUnknown("", PrivateActivationUnknownInterrupted); err != nil {
					t.Fatal(err)
				}
			} else if err := lifecycle.MarkCalibrationFailed(PrivateActivationUnknownInterrupted); err != nil {
				t.Fatal(err)
			}
			last := lifecycle.Events[len(lifecycle.Events)-1]
			if lifecycle.Status() != PrivateActivationStudyStopped || last.Type != PrivateActivationEventCalibrationFailed || last.CellID != "" {
				t.Fatalf("status=%q last=%+v", lifecycle.Status(), last)
			}
			if _, err := lifecycle.ReserveCalibration(); !errors.Is(err, ErrPrivateActivationLifecycle) {
				t.Fatalf("terminal calibration retried: %v", err)
			}
			if _, err := lifecycle.ReserveNextCell(); !errors.Is(err, ErrPrivateActivationLifecycle) {
				t.Fatalf("treatment started after failed calibration: %v", err)
			}
			reserved, err := lifecycle.ReservedCostMicroUSD()
			if err != nil || reserved != lifecycle.Plan.Calibration.MaxEstimatedCostMicroUSD {
				t.Fatalf("reserved=%d err=%v", reserved, err)
			}
		})
	}
}

func TestPrivateActivationCalibrationUnsafeReceiptFailsClosed(t *testing.T) {
	lifecycle := privateActivationUncalibratedTestLifecycle(t, 10, 1)
	if _, err := lifecycle.ReserveCalibration(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkCalibrationLaunched(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkCalibrationProviderAttemptCommitted(); err != nil {
		t.Fatal(err)
	}
	receipt := privateActivationGoodReceipt(0)
	receipt.ProviderCompleted = false
	if err := lifecycle.RecordCalibrationReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	last := lifecycle.Events[len(lifecycle.Events)-1]
	if lifecycle.Status() != PrivateActivationStudyStopped || last.Type != PrivateActivationEventCalibrationFailed || last.Reason != PrivateActivationUnknownProvider {
		t.Fatalf("status=%q last=%+v", lifecycle.Status(), last)
	}
	if err := lifecycle.MarkCalibrationSucceeded(); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("unsafe receipt accepted: %v", err)
	}
}

func TestPrivateActivationLifecycleHashChainAndSafeFailuresContinue(t *testing.T) {
	lifecycle := privateActivationTestLifecycle(t, 10, 3)
	wantOutcomes := []string{
		PrivateActivationOutcomeContentFailure,
		PrivateActivationOutcomeOracleFailure,
		PrivateActivationOutcomeSuccess,
		PrivateActivationOutcomeSuccess,
	}
	for index, outcome := range wantOutcomes {
		cell, err := lifecycle.ReserveNextCell()
		if err != nil || cell.CellID != lifecycle.Plan.Cells[index].CellID {
			t.Fatalf("reserve %d cell=%+v err=%v", index, cell, err)
		}
		if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
			t.Fatal(err)
		}
		if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); err != nil {
			t.Fatal(err)
		}
		if err := lifecycle.RecordReceipt(cell.CellID, privateActivationGoodReceipt(index)); err != nil {
			t.Fatal(err)
		}
		if err := lifecycle.MarkDefinitive(cell.CellID, outcome); err != nil {
			t.Fatal(err)
		}
		if err := lifecycle.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if lifecycle.Status() != PrivateActivationStudyCompleted || !lifecycle.FinalizationEligible() {
		t.Fatalf("status=%q eligible=%t", lifecycle.Status(), lifecycle.FinalizationEligible())
	}
	reserved, err := lifecycle.ReservedCostMicroUSD()
	if err != nil || reserved != 42 {
		t.Fatalf("reserved=%d err=%v", reserved, err)
	}
	for index, event := range lifecycle.Events {
		if !validSHA256(event.EventSHA256) || event.Sequence != index+1 {
			t.Fatalf("event %d=%+v", index, event)
		}
		if index == 0 && event.PreviousSHA256 != "" || index > 0 && event.PreviousSHA256 != lifecycle.Events[index-1].EventSHA256 {
			t.Fatalf("broken previous hash at %d", index)
		}
	}
	if err := lifecycle.Finalize(); err != nil {
		t.Fatal(err)
	}
	if lifecycle.Status() != PrivateActivationStudyFinalized || lifecycle.FinalizationEligible() {
		t.Fatalf("status=%q eligible=%t", lifecycle.Status(), lifecycle.FinalizationEligible())
	}
	if _, err := lifecycle.ReserveNextCell(); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("finalized study resumed: %v", err)
	}
}

func TestPrivateActivationUnknownConsumesFullCapAndStopsWithoutRetry(t *testing.T) {
	tests := []struct {
		name    string
		receipt PrivateActivationReceipt
		reason  string
	}{
		{name: "missing cost", receipt: PrivateActivationReceipt{SHA256: strings.Repeat("a", 64), ProviderCompleted: true, PersistenceComplete: true, ContainmentCertain: true}, reason: PrivateActivationUnknownCost},
		{name: "cap exceeded", receipt: PrivateActivationReceipt{SHA256: strings.Repeat("b", 64), CostKnown: true, DetectedCostMicroUSD: 11, ProviderCompleted: true, PersistenceComplete: true, ContainmentCertain: true}, reason: PrivateActivationUnknownCostExceeded},
		{name: "provider uncertain", receipt: PrivateActivationReceipt{SHA256: strings.Repeat("c", 64), CostKnown: true, DetectedCostMicroUSD: 1, PersistenceComplete: true, ContainmentCertain: true}, reason: PrivateActivationUnknownProvider},
		{name: "persistence uncertain", receipt: PrivateActivationReceipt{SHA256: strings.Repeat("d", 64), CostKnown: true, DetectedCostMicroUSD: 1, ProviderCompleted: true, ContainmentCertain: true}, reason: PrivateActivationUnknownPersistence},
		{name: "containment uncertain", receipt: PrivateActivationReceipt{SHA256: strings.Repeat("e", 64), CostKnown: true, DetectedCostMicroUSD: 1, ProviderCompleted: true, PersistenceComplete: true}, reason: PrivateActivationUnknownContainment},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := privateActivationTestLifecycle(t, 10, 3)
			cell, err := lifecycle.ReserveNextCell()
			if err != nil {
				t.Fatal(err)
			}
			if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
				t.Fatal(err)
			}
			if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); err != nil {
				t.Fatal(err)
			}
			if err := lifecycle.RecordReceipt(cell.CellID, test.receipt); err != nil {
				t.Fatal(err)
			}
			if lifecycle.Status() != PrivateActivationStudyStopped || lifecycle.FinalizationEligible() {
				t.Fatalf("status=%q eligible=%t", lifecycle.Status(), lifecycle.FinalizationEligible())
			}
			last := lifecycle.Events[len(lifecycle.Events)-1]
			if last.Type != PrivateActivationEventUnknown || last.Reason != test.reason {
				t.Fatalf("last=%+v", last)
			}
			reserved, err := lifecycle.ReservedCostMicroUSD()
			if err != nil || reserved != cell.MaxEstimatedCostMicroUSD+lifecycle.Plan.Calibration.MaxEstimatedCostMicroUSD {
				t.Fatalf("reserved=%d cap=%d err=%v", reserved, cell.MaxEstimatedCostMicroUSD, err)
			}
			if _, err := lifecycle.ReserveNextCell(); !errors.Is(err, ErrPrivateActivationLifecycle) {
				t.Fatalf("unknown study resumed: %v", err)
			}
			if err := lifecycle.MarkLaunched(cell.CellID); !errors.Is(err, ErrPrivateActivationLifecycle) {
				t.Fatalf("unknown cell relaunched: %v", err)
			}
			if err := lifecycle.Finalize(); !errors.Is(err, ErrPrivateActivationLifecycle) {
				t.Fatalf("unknown study finalized: %v", err)
			}
		})
	}
}

func TestPrivateActivationManualUnknownStopsAtEveryPreDefinitivePhase(t *testing.T) {
	for _, phase := range []string{PrivateActivationEventReserved, PrivateActivationEventLaunched, PrivateActivationEventProviderCommitted, PrivateActivationEventReceipt} {
		t.Run(phase, func(t *testing.T) {
			lifecycle := privateActivationTestLifecycle(t, 10, 1)
			cell, err := lifecycle.ReserveNextCell()
			if err != nil {
				t.Fatal(err)
			}
			if phase != PrivateActivationEventReserved {
				if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
					t.Fatal(err)
				}
			}
			if phase == PrivateActivationEventProviderCommitted || phase == PrivateActivationEventReceipt {
				if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); err != nil {
					t.Fatal(err)
				}
			}
			if phase == PrivateActivationEventReceipt {
				if err := lifecycle.RecordReceipt(cell.CellID, privateActivationGoodReceipt(0)); err != nil {
					t.Fatal(err)
				}
			}
			if err := lifecycle.MarkUnknown(cell.CellID, PrivateActivationUnknownInterrupted); err != nil {
				t.Fatal(err)
			}
			if lifecycle.Status() != PrivateActivationStudyStopped {
				t.Fatalf("status=%q", lifecycle.Status())
			}
		})
	}
}

func TestPrivateActivationTerminalStopBetweenCellsBindsProjection(t *testing.T) {
	lifecycle := privateActivationTestLifecycle(t, 10, 1)
	cell, err := lifecycle.ReserveNextCell()
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.RecordReceipt(cell.CellID, privateActivationGoodReceipt(0)); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("receipt before durable provider attempt err=%v", err)
	}
	if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("duplicate provider attempt boundary err=%v", err)
	}
	if err := lifecycle.RecordReceipt(cell.CellID, privateActivationGoodReceipt(0)); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkDefinitive(cell.CellID, PrivateActivationOutcomeSuccess); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(PrivateActivationStopInputDrift); err != nil {
		t.Fatal(err)
	}
	projection, err := lifecycle.project()
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.Status() != PrivateActivationStudyStopped || projection.stopReason != PrivateActivationStopInputDrift ||
		projection.detectedCostMicroUSD != 2 || !reflect.DeepEqual(projection.completedCells, []string{cell.CellID}) {
		t.Fatalf("projection=%+v", projection)
	}
	last := lifecycle.Events[len(lifecycle.Events)-1]
	if last.Type != PrivateActivationEventStopped || last.CellID != "" || !validSHA256(last.EventSHA256) {
		t.Fatalf("last=%+v", last)
	}
	if _, err := lifecycle.ReserveNextCell(); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("stopped study resumed: %v", err)
	}

	active := privateActivationTestLifecycle(t, 10, 1)
	if _, err := active.ReserveNextCell(); err != nil {
		t.Fatal(err)
	}
	if err := active.Stop(PrivateActivationStopInputDrift); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("active cell stopped without unknown transition: %v", err)
	}
}

func TestPrivateActivationLifecycleRejectsOutOfOrderAndTamperedEvents(t *testing.T) {
	lifecycle := privateActivationTestLifecycle(t, 10, 1)
	if err := lifecycle.MarkLaunched(lifecycle.Plan.Cells[0].CellID); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("launch without reservation err=%v", err)
	}
	cell, err := lifecycle.ReserveNextCell()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.ReserveNextCell(); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("second reservation err=%v", err)
	}
	if err := lifecycle.MarkLaunched(lifecycle.Plan.Cells[1].CellID); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("reordered launch err=%v", err)
	}
	if err := lifecycle.MarkDefinitive(cell.CellID, PrivateActivationOutcomeSuccess); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("definitive without receipt err=%v", err)
	}
	if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkProviderAttemptCommitted(cell.CellID); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.RecordReceipt(cell.CellID, privateActivationGoodReceipt(0)); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkDefinitive(cell.CellID, "custom_failure"); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("unreviewed outcome err=%v", err)
	}

	tampered := lifecycle
	tampered.Events = append([]PrivateActivationStudyEvent(nil), lifecycle.Events...)
	tampered.Events[0].ReservedCostMicroUSD++
	if err := tampered.Validate(); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("tampered event err=%v", err)
	}
	tampered = lifecycle
	tampered.Plan.Cells = append([]PrivateActivationStudyCell(nil), lifecycle.Plan.Cells...)
	tampered.Plan.Cells[0].CellID = "different"
	if err := tampered.Validate(); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("tampered plan err=%v", err)
	}
}

func TestCanStartPrivateActivationStudyRejectsSecondActiveAndStudyReuse(t *testing.T) {
	active := privateActivationTestLifecycle(t, 10, 1)
	candidatePlan, err := NewPrivateActivationStudyPlan(PrivateActivationStudyPlanInput{
		StudyID: "study-next", TotalAuthorizedMicroUSD: 43, ReviewerReserveMicroUSD: 1,
		Calibration: privateActivationTestCalibration(2), OrderedBalancedRoster: privateActivationTestRoster(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := CanStartPrivateActivationStudy(candidatePlan, []PrivateActivationStudyLifecycle{active}); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("second active plan err=%v", err)
	}

	stopped := active
	cell, err := stopped.ReserveNextCell()
	if err != nil {
		t.Fatal(err)
	}
	if err := stopped.MarkUnknown(cell.CellID, PrivateActivationUnknownInterrupted); err != nil {
		t.Fatal(err)
	}
	reused := candidatePlan
	reused.StudyID = stopped.Plan.StudyID
	if err := CanStartPrivateActivationStudy(reused, []PrivateActivationStudyLifecycle{stopped}); !errors.Is(err, ErrPrivateActivationLifecycle) {
		t.Fatalf("stopped study reused err=%v", err)
	}
	if err := CanStartPrivateActivationStudy(candidatePlan, []PrivateActivationStudyLifecycle{stopped}); err != nil {
		t.Fatalf("distinct study after terminal stop rejected: %v", err)
	}
}

func TestPrivateActivationLifecycleJSONRoundTripPreservesChain(t *testing.T) {
	lifecycle := privateActivationTestLifecycle(t, 10, 2)
	cell, err := lifecycle.ReserveNextCell()
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkLaunched(cell.CellID); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PrivateActivationStudyLifecycle
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, lifecycle) {
		t.Fatalf("round trip changed lifecycle\n got: %+v\nwant: %+v", decoded, lifecycle)
	}
}

func privateActivationTestRoster(cap int64) []PrivateActivationStudyCell {
	activations := []string{SkillActivationCombined, SkillActivationImplicit, SkillActivationDeveloper, SkillActivationExplicit}
	cells := make([]PrivateActivationStudyCell, 0, len(activations))
	for index, activation := range activations {
		cells = append(cells, PrivateActivationStudyCell{
			CellID: "cell-" + string(rune('a'+index)), SkillActivation: activation,
			ContractSHA256: strings.Repeat(string(rune('a'+index)), 64), MaxEstimatedCostMicroUSD: cap,
		})
	}
	return cells
}

func privateActivationTestLifecycle(t *testing.T, cellCap, reviewerReserve int64) PrivateActivationStudyLifecycle {
	t.Helper()
	lifecycle := privateActivationUncalibratedTestLifecycle(t, cellCap, reviewerReserve)
	privateActivationCompleteCalibration(t, &lifecycle)
	return lifecycle
}

func privateActivationUncalibratedTestLifecycle(t *testing.T, cellCap, reviewerReserve int64) PrivateActivationStudyLifecycle {
	t.Helper()
	plan, err := NewPrivateActivationStudyPlan(PrivateActivationStudyPlanInput{
		StudyID: "study-lifecycle", TotalAuthorizedMicroUSD: 4*cellCap + reviewerReserve + 2,
		ReviewerReserveMicroUSD: reviewerReserve, Calibration: privateActivationTestCalibration(2),
		OrderedBalancedRoster: privateActivationTestRoster(cellCap),
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewPrivateActivationStudyLifecycle(plan)
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle
}

func privateActivationTestCalibration(cap int64) PrivateActivationCalibrationContract {
	return PrivateActivationCalibrationContract{ContractSHA256: strings.Repeat("9", 64), MaxEstimatedCostMicroUSD: cap}
}

func privateActivationCompleteCalibration(t *testing.T, lifecycle *PrivateActivationStudyLifecycle) {
	t.Helper()
	if _, err := lifecycle.ReserveCalibration(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkCalibrationLaunched(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkCalibrationProviderAttemptCommitted(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.RecordCalibrationReceipt(privateActivationGoodReceipt(0)); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.MarkCalibrationSucceeded(); err != nil {
		t.Fatal(err)
	}
}

func privateActivationGoodReceipt(index int) PrivateActivationReceipt {
	return PrivateActivationReceipt{
		SHA256: strings.Repeat(string(rune('f'-index)), 64), CostKnown: true, DetectedCostMicroUSD: int64(index + 1),
		ProviderCompleted: true, PersistenceComplete: true, ContainmentCertain: true,
	}
}
