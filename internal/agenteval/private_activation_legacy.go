package agenteval

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

const (
	legacyPrivateActivationStudyPlanSchemaVersion  = 1
	legacyPrivateActivationStudyEventSchemaVersion = 1
)

func validateLegacyPrivateActivationStudy(paths ...string) error {
	if len(paths) != 4 {
		return fmt.Errorf("legacy private activation study requires exactly four run specs")
	}
	specs := make([]RunSpec, 0, len(paths))
	commonDirectory := ""
	for index, path := range paths {
		loaded, err := loadRunInputs(RunOptions{SpecPath: path})
		if err != nil {
			return fmt.Errorf("legacy private activation study run %d is invalid", index+1)
		}
		directory, err := filepath.Abs(loaded.specDir)
		if err != nil {
			return fmt.Errorf("legacy private activation study run %d is invalid", index+1)
		}
		if commonDirectory == "" {
			commonDirectory = directory
		} else if directory != commonDirectory {
			return fmt.Errorf("legacy private activation study runs must use one case directory")
		}
		specs = append(specs, loaded.spec)
	}
	_, err := BuildPrivateActivationStudyContract(specs)
	return err
}

// legacyPrivateActivationCostPartitions and legacyPrivateActivationStudyPlan
// preserve the exact JSON envelope used before provider/tool-path calibration
// became part of the activation-study contract. They exist only for validating
// already-written private artifacts. New plans must use
// NewPrivateActivationStudyPlan and the current lifecycle schema.
type legacyPrivateActivationCostPartitions struct {
	Assurance                  string `json:"assurance"`
	Preventive                 bool   `json:"preventive"`
	TotalAuthorizedMicroUSD    int64  `json:"total_authorized_microusd"`
	TreatmentAllocatedMicroUSD int64  `json:"treatment_allocated_microusd"`
	ReviewerReserveMicroUSD    int64  `json:"reviewer_reserve_microusd"`
}

type legacyPrivateActivationStudyPlan struct {
	SchemaVersion int                                   `json:"schema_version"`
	StudyID       string                                `json:"study_id"`
	Provider      string                                `json:"provider"`
	Cost          legacyPrivateActivationCostPartitions `json:"cost"`
	Cells         []PrivateActivationStudyCell          `json:"cells"`
}

// legacyPrivateActivationProjection is intentionally read-only. In
// particular, no constructor or transition method accepts it, so a valid
// pre-calibration artifact can be inspected but cannot be resumed as a current
// calibrated study.
type legacyPrivateActivationProjection struct {
	status               string
	next                 int
	definitive           int
	reserved             int64
	detectedCostMicroUSD int64
	completedCells       []string
	stopReason           string
	activeCell           string
	activePhase          string
	activeCap            int64
	receiptSafe          bool
}

func validateLegacyPrivateActivationStudyPlan(plan PrivateActivationStudyPlan) error {
	if plan.SchemaVersion != legacyPrivateActivationStudyPlanSchemaVersion ||
		plan.Cost.CalibrationAllocatedMicroUSD != 0 ||
		plan.Calibration != (PrivateActivationCalibrationContract{}) ||
		validatePathComponentID("activation study id", plan.StudyID) != nil || plan.Provider != "codex" ||
		plan.Cost.Assurance != PrivateActivationCostAssuranceDetectionOnly || plan.Cost.Preventive ||
		plan.Cost.TotalAuthorizedMicroUSD < 1 || plan.Cost.TreatmentAllocatedMicroUSD < 1 || plan.Cost.ReviewerReserveMicroUSD < 1 ||
		len(plan.Cells) < 4 || len(plan.Cells) > 400 {
		return privateActivationLifecycleError("legacy_plan")
	}
	treatment, ok := sumPrivateActivationCellCaps(plan.Cells)
	if !ok || treatment != plan.Cost.TreatmentAllocatedMicroUSD ||
		plan.Cost.TreatmentAllocatedMicroUSD > plan.Cost.TotalAuthorizedMicroUSD ||
		plan.Cost.ReviewerReserveMicroUSD > plan.Cost.TotalAuthorizedMicroUSD-plan.Cost.TreatmentAllocatedMicroUSD ||
		plan.Cost.TreatmentAllocatedMicroUSD+plan.Cost.ReviewerReserveMicroUSD != plan.Cost.TotalAuthorizedMicroUSD {
		return privateActivationLifecycleError("legacy_cost_partitions")
	}
	counts := map[string]int{
		SkillActivationImplicit:  0,
		SkillActivationExplicit:  0,
		SkillActivationDeveloper: 0,
		SkillActivationCombined:  0,
	}
	seen := make(map[string]struct{}, len(plan.Cells))
	for _, cell := range plan.Cells {
		if validatePathComponentID("activation cell id", cell.CellID) != nil || !validSHA256(cell.ContractSHA256) || cell.MaxEstimatedCostMicroUSD < 1 {
			return privateActivationLifecycleError("legacy_cell")
		}
		if _, exists := seen[cell.CellID]; exists {
			return privateActivationLifecycleError("legacy_duplicate_cell")
		}
		seen[cell.CellID] = struct{}{}
		if _, exists := counts[cell.SkillActivation]; !exists {
			return privateActivationLifecycleError("legacy_activation")
		}
		counts[cell.SkillActivation]++
	}
	want := counts[SkillActivationImplicit]
	if want < 1 || counts[SkillActivationExplicit] != want || counts[SkillActivationDeveloper] != want || counts[SkillActivationCombined] != want {
		return privateActivationLifecycleError("legacy_unbalanced_roster")
	}
	return nil
}

func legacyPrivateActivationStudyPlanSHA256(plan PrivateActivationStudyPlan) (string, error) {
	if err := validateLegacyPrivateActivationStudyPlan(plan); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(legacyPrivateActivationStudyPlan{
		SchemaVersion: plan.SchemaVersion,
		StudyID:       plan.StudyID,
		Provider:      plan.Provider,
		Cost: legacyPrivateActivationCostPartitions{
			Assurance:                  plan.Cost.Assurance,
			Preventive:                 plan.Cost.Preventive,
			TotalAuthorizedMicroUSD:    plan.Cost.TotalAuthorizedMicroUSD,
			TreatmentAllocatedMicroUSD: plan.Cost.TreatmentAllocatedMicroUSD,
			ReviewerReserveMicroUSD:    plan.Cost.ReviewerReserveMicroUSD,
		},
		Cells: plan.Cells,
	})
	if err != nil {
		return "", privateActivationLifecycleError("legacy_plan_encode")
	}
	return sha256HexBytes(encoded), nil
}

// projectLegacyPrivateActivationLifecycle validates an immutable schema-v1
// plan/event chain and returns its historical state. It deliberately does not
// upgrade, synthesize calibration evidence, or expose mutation operations.
func projectLegacyPrivateActivationLifecycle(plan PrivateActivationStudyPlan, planSHA256 string, events []PrivateActivationStudyEvent) (legacyPrivateActivationProjection, error) {
	projection := legacyPrivateActivationProjection{status: PrivateActivationStudyPending}
	digest, err := legacyPrivateActivationStudyPlanSHA256(plan)
	if err != nil || digest != planSHA256 {
		return projection, privateActivationLifecycleError("legacy_plan_hash")
	}
	previous := ""
	for index, event := range events {
		if event.SchemaVersion != legacyPrivateActivationStudyEventSchemaVersion || event.Sequence != index+1 || event.PreviousSHA256 != previous ||
			event.PlanSHA256 != planSHA256 || !validSHA256(event.EventSHA256) {
			return projection, privateActivationLifecycleError("legacy_event_chain")
		}
		computed, hashErr := privateActivationEventSHA256(event)
		if hashErr != nil || computed != event.EventSHA256 {
			return projection, privateActivationLifecycleError("legacy_event_hash")
		}
		if err := applyLegacyPrivateActivationEvent(&projection, plan, event); err != nil {
			return projection, err
		}
		previous = event.EventSHA256
	}
	if projection.status != PrivateActivationStudyStopped && projection.status != PrivateActivationStudyFinalized {
		switch {
		case projection.definitive == len(plan.Cells):
			projection.status = PrivateActivationStudyCompleted
		case len(events) != 0:
			projection.status = PrivateActivationStudyRunning
		default:
			projection.status = PrivateActivationStudyPending
		}
	}
	return projection, nil
}

// validateLegacyPrivateActivationPlanState binds the historical outer plan
// state to the schema-v1 lifecycle. Callers must keep this on inspection/load
// paths; execution, recovery, reference capture, and promotion require the
// current calibrated contract.
func validateLegacyPrivateActivationPlanState(plan privatePlan, state privatePlanState) error {
	if plan.Kind != PrivateRunSetKindActivationStudy || plan.StudyContract == nil ||
		plan.StudyContract.SchemaVersion != legacyPrivateActivationStudyPlanSchemaVersion || state.SchemaVersion != 2 {
		return privatePlanError("legacy_study_state")
	}
	planSHA256, err := legacyPrivateActivationStudyPlanSHA256(*plan.StudyContract)
	if err != nil {
		return privatePlanError("legacy_study_state")
	}
	projection, err := projectLegacyPrivateActivationLifecycle(*plan.StudyContract, planSHA256, state.Events)
	if err != nil || !equalStrings(state.CompletedCells, projection.completedCells) ||
		state.EstimatedCostMicroUSD != projection.detectedCostMicroUSD || state.StopReason != projection.stopReason {
		return privatePlanError("legacy_study_state")
	}
	switch state.Status {
	case "interrupted":
		if projection.status != PrivateActivationStudyPending || len(state.Events) != 0 || len(state.CompletedCells) != 0 ||
			state.EstimatedCostMicroUSD != 0 || state.StopReason != "" || state.CompletedAt != "" {
			return privatePlanError("legacy_study_state")
		}
	case "running":
		if projection.status != PrivateActivationStudyPending && projection.status != PrivateActivationStudyRunning || state.CompletedAt != "" {
			return privatePlanError("legacy_study_state")
		}
	case "stopped":
		if projection.status != PrivateActivationStudyStopped || state.CompletedAt != "" {
			return privatePlanError("legacy_study_state")
		}
	case "completed":
		if projection.status != PrivateActivationStudyCompleted || !equalStrings(state.CompletedCells, privatePlanCellIDs(plan.Items)) || state.CompletedAt == "" {
			return privatePlanError("legacy_study_state")
		}
	default:
		return privatePlanError("legacy_study_state")
	}
	return nil
}

func applyLegacyPrivateActivationEvent(p *legacyPrivateActivationProjection, plan PrivateActivationStudyPlan, event PrivateActivationStudyEvent) error {
	if p.status == PrivateActivationStudyStopped || p.status == PrivateActivationStudyFinalized {
		return privateActivationLifecycleError("legacy_terminal_transition")
	}
	switch event.Type {
	case PrivateActivationEventReserved:
		if p.activePhase != "" || p.next >= len(plan.Cells) {
			return privateActivationLifecycleError("legacy_reservation_transition")
		}
		cell := plan.Cells[p.next]
		if event.CellID != cell.CellID || event.ReservedCostMicroUSD != cell.MaxEstimatedCostMicroUSD ||
			event.CostKnown || event.DetectedCostMicroUSD != 0 || event.ProviderCompleted || event.PersistenceComplete || event.ContainmentCertain ||
			event.ReceiptSHA256 != "" || event.Outcome != "" || event.Reason != "" {
			return privateActivationLifecycleError("legacy_reservation_event")
		}
		if p.reserved > plan.Cost.TreatmentAllocatedMicroUSD-event.ReservedCostMicroUSD {
			return privateActivationLifecycleError("legacy_reservation_budget")
		}
		p.reserved += event.ReservedCostMicroUSD
		p.activeCell, p.activePhase, p.activeCap = event.CellID, event.Type, event.ReservedCostMicroUSD
	case PrivateActivationEventLaunched:
		if p.activePhase != PrivateActivationEventReserved || event.CellID != p.activeCell || !emptyPrivateActivationEventPayload(event) {
			return privateActivationLifecycleError("legacy_launch_event")
		}
		p.activePhase = event.Type
	case PrivateActivationEventProviderCommitted:
		if p.activePhase != PrivateActivationEventLaunched || event.CellID != p.activeCell || !emptyPrivateActivationEventPayload(event) {
			return privateActivationLifecycleError("legacy_provider_commit_event")
		}
		p.activePhase = event.Type
	case PrivateActivationEventReceipt:
		if p.activePhase != PrivateActivationEventProviderCommitted || event.CellID != p.activeCell || !validSHA256(event.ReceiptSHA256) || event.DetectedCostMicroUSD < 0 ||
			(!event.CostKnown && event.DetectedCostMicroUSD != 0) || event.ReservedCostMicroUSD != 0 || event.Outcome != "" || event.Reason != "" {
			return privateActivationLifecycleError("legacy_receipt_event")
		}
		p.activePhase = event.Type
		if event.CostKnown {
			if p.detectedCostMicroUSD > int64(^uint64(0)>>1)-event.DetectedCostMicroUSD {
				return privateActivationLifecycleError("legacy_receipt_cost_overflow")
			}
			p.detectedCostMicroUSD += event.DetectedCostMicroUSD
		}
		p.receiptSafe = event.ProviderCompleted && event.PersistenceComplete && event.ContainmentCertain &&
			event.CostKnown && event.DetectedCostMicroUSD <= p.activeCap
	case PrivateActivationEventDefinitive:
		if p.activePhase != PrivateActivationEventReceipt || !p.receiptSafe || event.CellID != p.activeCell || !validPrivateActivationOutcome(event.Outcome) ||
			event.ReservedCostMicroUSD != 0 || event.CostKnown || event.DetectedCostMicroUSD != 0 || event.ProviderCompleted ||
			event.PersistenceComplete || event.ContainmentCertain || event.ReceiptSHA256 != "" || event.Reason != "" {
			return privateActivationLifecycleError("legacy_definitive_event")
		}
		p.definitive++
		p.next++
		p.completedCells = append(p.completedCells, event.CellID)
		p.activeCell, p.activePhase, p.activeCap, p.receiptSafe = "", "", 0, false
	case PrivateActivationEventUnknown:
		if p.activePhase == "" || event.CellID != p.activeCell || !validPrivateActivationUnknownReason(event.Reason) ||
			event.ReservedCostMicroUSD != 0 || event.CostKnown || event.DetectedCostMicroUSD != 0 || event.ProviderCompleted ||
			event.PersistenceComplete || event.ContainmentCertain || event.ReceiptSHA256 != "" || event.Outcome != "" {
			return privateActivationLifecycleError("legacy_unknown_event")
		}
		p.status = PrivateActivationStudyStopped
		p.stopReason = event.Reason
		p.activeCell, p.activePhase, p.activeCap, p.receiptSafe = "", "", 0, false
	case PrivateActivationEventStopped:
		if p.activePhase != "" || !validPrivateActivationStopReason(event.Reason) || event.CellID != "" ||
			event.ReservedCostMicroUSD != 0 || event.CostKnown || event.DetectedCostMicroUSD != 0 || event.ProviderCompleted ||
			event.PersistenceComplete || event.ContainmentCertain || event.ReceiptSHA256 != "" || event.Outcome != "" {
			return privateActivationLifecycleError("legacy_stop_event")
		}
		p.status = PrivateActivationStudyStopped
		p.stopReason = event.Reason
	case PrivateActivationEventFinalized:
		if p.definitive != len(plan.Cells) || p.activePhase != "" || event.CellID != "" || !emptyPrivateActivationEventPayload(event) {
			return privateActivationLifecycleError("legacy_finalize_event")
		}
		p.status = PrivateActivationStudyFinalized
	default:
		return privateActivationLifecycleError("legacy_event_type")
	}
	return nil
}
