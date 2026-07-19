package agenteval

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	PrivateActivationStudyPlanSchemaVersion  = 1
	PrivateActivationStudyEventSchemaVersion = 1

	PrivateActivationCostAssuranceDetectionOnly = "detection_only"

	PrivateActivationEventReserved          = "reserved"
	PrivateActivationEventLaunched          = "launched"
	PrivateActivationEventProviderCommitted = "provider_attempt_committed"
	PrivateActivationEventReceipt           = "receipt"
	PrivateActivationEventDefinitive        = "definitive"
	PrivateActivationEventUnknown           = "unknown"
	PrivateActivationEventStopped           = "stopped"
	PrivateActivationEventFinalized         = "finalized"

	PrivateActivationOutcomeSuccess        = "success"
	PrivateActivationOutcomeContentFailure = "content_failure"
	PrivateActivationOutcomeOracleFailure  = "oracle_failure"

	PrivateActivationUnknownCost         = "cost_unknown"
	PrivateActivationUnknownCostExceeded = "cost_cap_exceeded"
	PrivateActivationUnknownProvider     = "provider_unknown"
	PrivateActivationUnknownPersistence  = "persistence_unknown"
	PrivateActivationUnknownContainment  = "containment_unknown"
	PrivateActivationUnknownInterrupted  = "interrupted"
	PrivateActivationUnknownCanceled     = "operator_canceled"

	PrivateActivationStopInputDrift       = "input_drift"
	PrivateActivationStopSnapshotDrift    = "snapshot_drift"
	PrivateActivationStopReservation      = "reservation"
	PrivateActivationStopCellContract     = "cell_contract"
	PrivateActivationStopProviderSession  = "provider_session"
	PrivateActivationStopSnapshotCleanup  = "snapshot_cleanup"
	PrivateActivationStopOperatorRecovery = "operator_recovery"

	PrivateActivationStudyPending   = "pending"
	PrivateActivationStudyRunning   = "running"
	PrivateActivationStudyStopped   = "stopped"
	PrivateActivationStudyCompleted = "completed"
	PrivateActivationStudyFinalized = "finalized"
)

var ErrPrivateActivationLifecycle = errors.New("private activation lifecycle rejected")

// PrivateActivationStudyCell is one stable member of the pre-ordered causal
// roster. CellID is independent of its position so order changes cannot rename
// a treatment. ContractSHA256 binds the reviewed inputs without retaining them.
type PrivateActivationStudyCell struct {
	CellID                   string `json:"cell_id"`
	SkillActivation          string `json:"skill_activation"`
	ContractSHA256           string `json:"contract_sha256"`
	MaxEstimatedCostMicroUSD int64  `json:"max_estimated_cost_microusd"`
}

// PrivateActivationCostPartitions are authorization partitions, not a claim
// that Codex can prevent provider spend. The current Codex integration observes
// cost only after execution, so Assurance and Preventive are fixed fail-closed
// contract values.
type PrivateActivationCostPartitions struct {
	Assurance                  string `json:"assurance"`
	Preventive                 bool   `json:"preventive"`
	TotalAuthorizedMicroUSD    int64  `json:"total_authorized_microusd"`
	TreatmentAllocatedMicroUSD int64  `json:"treatment_allocated_microusd"`
	ReviewerReserveMicroUSD    int64  `json:"reviewer_reserve_microusd"`
}

// PrivateActivationStudyPlan binds a balanced roster in its exact execution
// order. The order is caller-supplied and preserved; validation never sorts it.
type PrivateActivationStudyPlan struct {
	SchemaVersion int                             `json:"schema_version"`
	StudyID       string                          `json:"study_id"`
	Provider      string                          `json:"provider"`
	Cost          PrivateActivationCostPartitions `json:"cost"`
	Cells         []PrivateActivationStudyCell    `json:"cells"`
}

// privateActivationStudyPlanContract is the plan-owned lifecycle contract.
// Keep the private name at the persistence boundary while exposing the same
// deterministic value through the pure constructor API above.
type privateActivationStudyPlanContract = PrivateActivationStudyPlan

type PrivateActivationStudyPlanInput struct {
	StudyID                 string
	TotalAuthorizedMicroUSD int64
	ReviewerReserveMicroUSD int64
	OrderedBalancedRoster   []PrivateActivationStudyCell
}

// NewPrivateActivationStudyPlan constructs the detection-only Codex contract.
// The authorized total must be partitioned exactly between the immutable cell
// caps and the reviewer reserve; unused authorization cannot create extra cells.
func NewPrivateActivationStudyPlan(input PrivateActivationStudyPlanInput) (PrivateActivationStudyPlan, error) {
	cells := append([]PrivateActivationStudyCell(nil), input.OrderedBalancedRoster...)
	treatment, ok := sumPrivateActivationCellCaps(cells)
	if !ok {
		return PrivateActivationStudyPlan{}, privateActivationLifecycleError("cost_overflow")
	}
	plan := PrivateActivationStudyPlan{
		SchemaVersion: PrivateActivationStudyPlanSchemaVersion,
		StudyID:       input.StudyID,
		Provider:      "codex",
		Cost: PrivateActivationCostPartitions{
			Assurance:                  PrivateActivationCostAssuranceDetectionOnly,
			Preventive:                 false,
			TotalAuthorizedMicroUSD:    input.TotalAuthorizedMicroUSD,
			TreatmentAllocatedMicroUSD: treatment,
			ReviewerReserveMicroUSD:    input.ReviewerReserveMicroUSD,
		},
		Cells: cells,
	}
	if err := plan.Validate(); err != nil {
		return PrivateActivationStudyPlan{}, err
	}
	return plan, nil
}

func (p PrivateActivationStudyPlan) Validate() error {
	if p.SchemaVersion != PrivateActivationStudyPlanSchemaVersion || validatePathComponentID("activation study id", p.StudyID) != nil || p.Provider != "codex" ||
		p.Cost.Assurance != PrivateActivationCostAssuranceDetectionOnly || p.Cost.Preventive ||
		p.Cost.TotalAuthorizedMicroUSD < 1 || p.Cost.TreatmentAllocatedMicroUSD < 1 || p.Cost.ReviewerReserveMicroUSD < 1 ||
		len(p.Cells) < 4 || len(p.Cells) > 400 {
		return privateActivationLifecycleError("plan")
	}
	treatment, ok := sumPrivateActivationCellCaps(p.Cells)
	if !ok || treatment != p.Cost.TreatmentAllocatedMicroUSD ||
		p.Cost.TreatmentAllocatedMicroUSD > p.Cost.TotalAuthorizedMicroUSD ||
		p.Cost.ReviewerReserveMicroUSD > p.Cost.TotalAuthorizedMicroUSD-p.Cost.TreatmentAllocatedMicroUSD ||
		p.Cost.TreatmentAllocatedMicroUSD+p.Cost.ReviewerReserveMicroUSD != p.Cost.TotalAuthorizedMicroUSD {
		return privateActivationLifecycleError("cost_partitions")
	}
	counts := map[string]int{
		SkillActivationImplicit:  0,
		SkillActivationExplicit:  0,
		SkillActivationDeveloper: 0,
		SkillActivationCombined:  0,
	}
	seen := make(map[string]struct{}, len(p.Cells))
	for _, cell := range p.Cells {
		if validatePathComponentID("activation cell id", cell.CellID) != nil || !validSHA256(cell.ContractSHA256) || cell.MaxEstimatedCostMicroUSD < 1 {
			return privateActivationLifecycleError("cell")
		}
		if _, exists := seen[cell.CellID]; exists {
			return privateActivationLifecycleError("duplicate_cell")
		}
		seen[cell.CellID] = struct{}{}
		if _, exists := counts[cell.SkillActivation]; !exists {
			return privateActivationLifecycleError("activation")
		}
		counts[cell.SkillActivation]++
	}
	want := counts[SkillActivationImplicit]
	if want < 1 || counts[SkillActivationExplicit] != want || counts[SkillActivationDeveloper] != want || counts[SkillActivationCombined] != want {
		return privateActivationLifecycleError("unbalanced_roster")
	}
	return nil
}

// SHA256 is a deterministic identity of the complete ordered plan.
func (p PrivateActivationStudyPlan) SHA256() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return "", privateActivationLifecycleError("plan_encode")
	}
	return sha256HexBytes(data), nil
}

// PrivateActivationStudyEvent is an append-only transition. EventSHA256 covers
// every preceding field, including PreviousSHA256 and the immutable plan hash.
type PrivateActivationStudyEvent struct {
	SchemaVersion        int    `json:"schema_version"`
	Sequence             int    `json:"sequence"`
	PreviousSHA256       string `json:"previous_sha256,omitempty"`
	PlanSHA256           string `json:"plan_sha256"`
	Type                 string `json:"type"`
	CellID               string `json:"cell_id,omitempty"`
	ReservedCostMicroUSD int64  `json:"reserved_cost_microusd,omitempty"`
	CostKnown            bool   `json:"cost_known,omitempty"`
	DetectedCostMicroUSD int64  `json:"detected_cost_microusd,omitempty"`
	ProviderCompleted    bool   `json:"provider_completed,omitempty"`
	PersistenceComplete  bool   `json:"persistence_complete,omitempty"`
	ContainmentCertain   bool   `json:"containment_certain,omitempty"`
	ReceiptSHA256        string `json:"receipt_sha256,omitempty"`
	Outcome              string `json:"outcome,omitempty"`
	Reason               string `json:"reason,omitempty"`
	EventSHA256          string `json:"event_sha256"`
}

// PrivateActivationLifecycleEvent is the persistence-facing name used by the
// private plan state. It is an alias so plan state and the pure state machine
// cannot drift into subtly different hash envelopes.
type PrivateActivationLifecycleEvent = PrivateActivationStudyEvent

type PrivateActivationReceipt struct {
	SHA256               string
	CostKnown            bool
	DetectedCostMicroUSD int64
	ProviderCompleted    bool
	PersistenceComplete  bool
	ContainmentCertain   bool
}

// PrivateActivationStudyLifecycle is a pure state machine. Events contain no
// timestamps, paths, prompts, or provider output, so equal transitions hash to
// equal chains.
type PrivateActivationStudyLifecycle struct {
	Plan       PrivateActivationStudyPlan    `json:"plan"`
	PlanSHA256 string                        `json:"plan_sha256"`
	Events     []PrivateActivationStudyEvent `json:"events"`
}

func NewPrivateActivationStudyLifecycle(plan PrivateActivationStudyPlan) (PrivateActivationStudyLifecycle, error) {
	digest, err := plan.SHA256()
	if err != nil {
		return PrivateActivationStudyLifecycle{}, err
	}
	plan.Cells = append([]PrivateActivationStudyCell(nil), plan.Cells...)
	return PrivateActivationStudyLifecycle{Plan: plan, PlanSHA256: digest, Events: []PrivateActivationStudyEvent{}}, nil
}

func (l PrivateActivationStudyLifecycle) Status() string {
	projection, err := l.project()
	if err != nil {
		return ""
	}
	return projection.status
}

// ReservedCostMicroUSD includes the full cap of an unknown cell. Reservations
// are never released or replaced by the smaller detected amount.
func (l PrivateActivationStudyLifecycle) ReservedCostMicroUSD() (int64, error) {
	projection, err := l.project()
	if err != nil {
		return 0, err
	}
	return projection.reserved, nil
}

func (l PrivateActivationStudyLifecycle) FinalizationEligible() bool {
	projection, err := l.project()
	return err == nil && projection.status == PrivateActivationStudyCompleted && projection.definitive == len(l.Plan.Cells)
}

// ReserveNextCell reserves the complete immutable cap before launch. It returns
// the next bound roster member and offers no selector or retry parameter.
func (l *PrivateActivationStudyLifecycle) ReserveNextCell() (PrivateActivationStudyCell, error) {
	projection, err := l.project()
	if err != nil {
		return PrivateActivationStudyCell{}, err
	}
	if projection.status != PrivateActivationStudyPending && projection.status != PrivateActivationStudyRunning {
		return PrivateActivationStudyCell{}, privateActivationLifecycleError("study_not_runnable")
	}
	if projection.activePhase != "" || projection.next >= len(l.Plan.Cells) {
		return PrivateActivationStudyCell{}, privateActivationLifecycleError("cell_in_progress")
	}
	cell := l.Plan.Cells[projection.next]
	if err := l.appendEvent(PrivateActivationStudyEvent{Type: PrivateActivationEventReserved, CellID: cell.CellID, ReservedCostMicroUSD: cell.MaxEstimatedCostMicroUSD}); err != nil {
		return PrivateActivationStudyCell{}, err
	}
	return cell, nil
}

func (l *PrivateActivationStudyLifecycle) MarkLaunched(cellID string) error {
	projection, err := l.project()
	if err != nil {
		return err
	}
	if projection.activeCell != cellID || projection.activePhase != PrivateActivationEventReserved {
		return privateActivationLifecycleError("launch_transition")
	}
	return l.appendEvent(PrivateActivationStudyEvent{Type: PrivateActivationEventLaunched, CellID: cellID})
}

// MarkProviderAttemptCommitted records the irrevocable boundary immediately
// before spawning the provider process. Setup failures before this boundary do
// not consume an attempt. A subsequent spawn failure does consume one because
// committing after spawn would permit replay after a crash in between.
func (l *PrivateActivationStudyLifecycle) MarkProviderAttemptCommitted(cellID string) error {
	projection, err := l.project()
	if err != nil {
		return err
	}
	if projection.activeCell != cellID || projection.activePhase != PrivateActivationEventLaunched {
		return privateActivationLifecycleError("provider_commit_transition")
	}
	return l.appendEvent(PrivateActivationStudyEvent{Type: PrivateActivationEventProviderCommitted, CellID: cellID})
}

// RecordReceipt records post-hoc cost detection. An incomplete/uncertain
// receipt or a detected cap breach atomically appends a terminal unknown event;
// no caller decision can continue the remaining roster.
func (l *PrivateActivationStudyLifecycle) RecordReceipt(cellID string, receipt PrivateActivationReceipt) error {
	projection, err := l.project()
	if err != nil {
		return err
	}
	if projection.activeCell != cellID || projection.activePhase != PrivateActivationEventProviderCommitted || !validSHA256(receipt.SHA256) ||
		receipt.DetectedCostMicroUSD < 0 || (!receipt.CostKnown && receipt.DetectedCostMicroUSD != 0) {
		return privateActivationLifecycleError("receipt_transition")
	}
	if err := l.appendEvent(PrivateActivationStudyEvent{Type: PrivateActivationEventReceipt, CellID: cellID, ReceiptSHA256: receipt.SHA256,
		CostKnown: receipt.CostKnown, DetectedCostMicroUSD: receipt.DetectedCostMicroUSD, ProviderCompleted: receipt.ProviderCompleted,
		PersistenceComplete: receipt.PersistenceComplete, ContainmentCertain: receipt.ContainmentCertain}); err != nil {
		return err
	}
	reason := ""
	switch {
	case !receipt.ProviderCompleted:
		reason = PrivateActivationUnknownProvider
	case !receipt.PersistenceComplete:
		reason = PrivateActivationUnknownPersistence
	case !receipt.ContainmentCertain:
		reason = PrivateActivationUnknownContainment
	case !receipt.CostKnown:
		reason = PrivateActivationUnknownCost
	case receipt.DetectedCostMicroUSD > projection.activeCap:
		reason = PrivateActivationUnknownCostExceeded
	}
	if reason != "" {
		return l.appendEvent(PrivateActivationStudyEvent{Type: PrivateActivationEventUnknown, CellID: cellID, Reason: reason})
	}
	return nil
}

// MarkDefinitive accepts content/oracle failures as measured outcomes and moves
// to the next pre-bound cell. Only uncertainty, not a poor answer, stops study.
func (l *PrivateActivationStudyLifecycle) MarkDefinitive(cellID, outcome string) error {
	projection, err := l.project()
	if err != nil {
		return err
	}
	if projection.activeCell != cellID || projection.activePhase != PrivateActivationEventReceipt || !validPrivateActivationOutcome(outcome) {
		return privateActivationLifecycleError("definitive_transition")
	}
	return l.appendEvent(PrivateActivationStudyEvent{Type: PrivateActivationEventDefinitive, CellID: cellID, Outcome: outcome})
}

// MarkUnknown is the conservative path when no valid receipt can be created.
// It is terminal and retains the full pre-launch reservation.
func (l *PrivateActivationStudyLifecycle) MarkUnknown(cellID, reason string) error {
	projection, err := l.project()
	if err != nil {
		return err
	}
	if projection.activeCell != cellID || (projection.activePhase != PrivateActivationEventReserved && projection.activePhase != PrivateActivationEventLaunched &&
		projection.activePhase != PrivateActivationEventProviderCommitted && projection.activePhase != PrivateActivationEventReceipt) || !validPrivateActivationUnknownReason(reason) {
		return privateActivationLifecycleError("unknown_transition")
	}
	return l.appendEvent(PrivateActivationStudyEvent{Type: PrivateActivationEventUnknown, CellID: cellID, Reason: reason})
}

// Stop terminates a block between cells. An active reserved, launched,
// provider-committed, or receipted cell must use MarkUnknown so its full
// reservation and uncertainty remain explicit in the event chain.
func (l *PrivateActivationStudyLifecycle) Stop(reason string) error {
	projection, err := l.project()
	if err != nil {
		return err
	}
	if projection.status == PrivateActivationStudyStopped || projection.status == PrivateActivationStudyFinalized ||
		projection.activePhase != "" || !validPrivateActivationStopReason(reason) {
		return privateActivationLifecycleError("stop_transition")
	}
	return l.appendEvent(PrivateActivationStudyEvent{Type: PrivateActivationEventStopped, Reason: reason})
}

func (l *PrivateActivationStudyLifecycle) Finalize() error {
	if !l.FinalizationEligible() {
		return privateActivationLifecycleError("not_eligible")
	}
	return l.appendEvent(PrivateActivationStudyEvent{Type: PrivateActivationEventFinalized})
}

// CanStartPrivateActivationStudy rejects a second active plan and any reuse of
// a StudyID, including a stopped unknown study. Replacement policy must create
// a separately bound identity instead of silently retrying the same study.
func CanStartPrivateActivationStudy(candidate PrivateActivationStudyPlan, existing []PrivateActivationStudyLifecycle) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	for _, lifecycle := range existing {
		if err := lifecycle.Validate(); err != nil {
			return err
		}
		if lifecycle.Plan.StudyID == candidate.StudyID {
			return privateActivationLifecycleError("study_reuse")
		}
		switch lifecycle.Status() {
		case PrivateActivationStudyPending, PrivateActivationStudyRunning, PrivateActivationStudyCompleted:
			return privateActivationLifecycleError("active_plan")
		}
	}
	return nil
}

func (l PrivateActivationStudyLifecycle) Validate() error {
	_, err := l.project()
	return err
}

type privateActivationProjection struct {
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

func (l PrivateActivationStudyLifecycle) project() (privateActivationProjection, error) {
	projection := privateActivationProjection{status: PrivateActivationStudyPending}
	digest, err := l.Plan.SHA256()
	if err != nil || digest != l.PlanSHA256 {
		return projection, privateActivationLifecycleError("plan_hash")
	}
	previous := ""
	for index, event := range l.Events {
		if event.SchemaVersion != PrivateActivationStudyEventSchemaVersion || event.Sequence != index+1 || event.PreviousSHA256 != previous ||
			event.PlanSHA256 != l.PlanSHA256 || !validSHA256(event.EventSHA256) {
			return projection, privateActivationLifecycleError("event_chain")
		}
		computed, hashErr := privateActivationEventSHA256(event)
		if hashErr != nil || computed != event.EventSHA256 {
			return projection, privateActivationLifecycleError("event_hash")
		}
		if err := applyPrivateActivationEvent(&projection, l.Plan, event); err != nil {
			return projection, err
		}
		previous = event.EventSHA256
	}
	if projection.status != PrivateActivationStudyStopped && projection.status != PrivateActivationStudyFinalized {
		switch {
		case projection.definitive == len(l.Plan.Cells):
			projection.status = PrivateActivationStudyCompleted
		case len(l.Events) != 0:
			projection.status = PrivateActivationStudyRunning
		default:
			projection.status = PrivateActivationStudyPending
		}
	}
	return projection, nil
}

func applyPrivateActivationEvent(p *privateActivationProjection, plan PrivateActivationStudyPlan, event PrivateActivationStudyEvent) error {
	if p.status == PrivateActivationStudyStopped || p.status == PrivateActivationStudyFinalized {
		return privateActivationLifecycleError("terminal_transition")
	}
	switch event.Type {
	case PrivateActivationEventReserved:
		if p.activePhase != "" || p.next >= len(plan.Cells) {
			return privateActivationLifecycleError("reservation_transition")
		}
		cell := plan.Cells[p.next]
		if event.CellID != cell.CellID || event.ReservedCostMicroUSD != cell.MaxEstimatedCostMicroUSD ||
			event.CostKnown || event.DetectedCostMicroUSD != 0 || event.ProviderCompleted || event.PersistenceComplete || event.ContainmentCertain ||
			event.ReceiptSHA256 != "" || event.Outcome != "" || event.Reason != "" {
			return privateActivationLifecycleError("reservation_event")
		}
		if p.reserved > plan.Cost.TreatmentAllocatedMicroUSD-event.ReservedCostMicroUSD {
			return privateActivationLifecycleError("reservation_budget")
		}
		p.reserved += event.ReservedCostMicroUSD
		p.activeCell, p.activePhase, p.activeCap = event.CellID, event.Type, event.ReservedCostMicroUSD
	case PrivateActivationEventLaunched:
		if p.activePhase != PrivateActivationEventReserved || event.CellID != p.activeCell || !emptyPrivateActivationEventPayload(event) {
			return privateActivationLifecycleError("launch_event")
		}
		p.activePhase = event.Type
	case PrivateActivationEventProviderCommitted:
		if p.activePhase != PrivateActivationEventLaunched || event.CellID != p.activeCell || !emptyPrivateActivationEventPayload(event) {
			return privateActivationLifecycleError("provider_commit_event")
		}
		p.activePhase = event.Type
	case PrivateActivationEventReceipt:
		if p.activePhase != PrivateActivationEventProviderCommitted || event.CellID != p.activeCell || !validSHA256(event.ReceiptSHA256) || event.DetectedCostMicroUSD < 0 ||
			(!event.CostKnown && event.DetectedCostMicroUSD != 0) || event.ReservedCostMicroUSD != 0 || event.Outcome != "" || event.Reason != "" {
			return privateActivationLifecycleError("receipt_event")
		}
		p.activePhase = event.Type
		if event.CostKnown {
			if p.detectedCostMicroUSD > int64(^uint64(0)>>1)-event.DetectedCostMicroUSD {
				return privateActivationLifecycleError("receipt_cost_overflow")
			}
			p.detectedCostMicroUSD += event.DetectedCostMicroUSD
		}
		p.receiptSafe = event.ProviderCompleted && event.PersistenceComplete && event.ContainmentCertain &&
			event.CostKnown && event.DetectedCostMicroUSD <= p.activeCap
	case PrivateActivationEventDefinitive:
		if p.activePhase != PrivateActivationEventReceipt || !p.receiptSafe || event.CellID != p.activeCell || !validPrivateActivationOutcome(event.Outcome) ||
			event.ReservedCostMicroUSD != 0 || event.CostKnown || event.DetectedCostMicroUSD != 0 || event.ProviderCompleted ||
			event.PersistenceComplete || event.ContainmentCertain || event.ReceiptSHA256 != "" || event.Reason != "" {
			return privateActivationLifecycleError("definitive_event")
		}
		p.definitive++
		p.next++
		p.completedCells = append(p.completedCells, event.CellID)
		p.activeCell, p.activePhase, p.activeCap, p.receiptSafe = "", "", 0, false
	case PrivateActivationEventUnknown:
		if p.activePhase == "" || event.CellID != p.activeCell || !validPrivateActivationUnknownReason(event.Reason) ||
			event.ReservedCostMicroUSD != 0 || event.CostKnown || event.DetectedCostMicroUSD != 0 || event.ProviderCompleted ||
			event.PersistenceComplete || event.ContainmentCertain || event.ReceiptSHA256 != "" || event.Outcome != "" {
			return privateActivationLifecycleError("unknown_event")
		}
		p.status = PrivateActivationStudyStopped
		p.stopReason = event.Reason
		p.activeCell, p.activePhase, p.activeCap, p.receiptSafe = "", "", 0, false
	case PrivateActivationEventStopped:
		if p.activePhase != "" || !validPrivateActivationStopReason(event.Reason) || event.CellID != "" ||
			event.ReservedCostMicroUSD != 0 || event.CostKnown || event.DetectedCostMicroUSD != 0 || event.ProviderCompleted ||
			event.PersistenceComplete || event.ContainmentCertain || event.ReceiptSHA256 != "" || event.Outcome != "" {
			return privateActivationLifecycleError("stop_event")
		}
		p.status = PrivateActivationStudyStopped
		p.stopReason = event.Reason
	case PrivateActivationEventFinalized:
		if p.definitive != len(plan.Cells) || p.activePhase != "" || event.CellID != "" || !emptyPrivateActivationEventPayload(event) {
			return privateActivationLifecycleError("finalize_event")
		}
		p.status = PrivateActivationStudyFinalized
	default:
		return privateActivationLifecycleError("event_type")
	}
	return nil
}

func (l *PrivateActivationStudyLifecycle) appendEvent(event PrivateActivationStudyEvent) error {
	if _, err := l.project(); err != nil {
		return err
	}
	event.SchemaVersion = PrivateActivationStudyEventSchemaVersion
	event.Sequence = len(l.Events) + 1
	event.PlanSHA256 = l.PlanSHA256
	if len(l.Events) != 0 {
		event.PreviousSHA256 = l.Events[len(l.Events)-1].EventSHA256
	}
	digest, err := privateActivationEventSHA256(event)
	if err != nil {
		return err
	}
	event.EventSHA256 = digest
	candidate := append(append([]PrivateActivationStudyEvent(nil), l.Events...), event)
	copyLifecycle := *l
	copyLifecycle.Events = candidate
	if _, err := copyLifecycle.project(); err != nil {
		return err
	}
	l.Events = candidate
	return nil
}

func privateActivationEventSHA256(event PrivateActivationStudyEvent) (string, error) {
	event.EventSHA256 = ""
	data, err := json.Marshal(event)
	if err != nil {
		return "", privateActivationLifecycleError("event_encode")
	}
	return sha256HexBytes(data), nil
}

func sumPrivateActivationCellCaps(cells []PrivateActivationStudyCell) (int64, bool) {
	var total int64
	for _, cell := range cells {
		if cell.MaxEstimatedCostMicroUSD < 0 || total > int64(^uint64(0)>>1)-cell.MaxEstimatedCostMicroUSD {
			return 0, false
		}
		total += cell.MaxEstimatedCostMicroUSD
	}
	return total, true
}

func validPrivateActivationOutcome(value string) bool {
	return value == PrivateActivationOutcomeSuccess || value == PrivateActivationOutcomeContentFailure || value == PrivateActivationOutcomeOracleFailure
}

func validPrivateActivationUnknownReason(value string) bool {
	switch value {
	case PrivateActivationUnknownCost, PrivateActivationUnknownCostExceeded, PrivateActivationUnknownProvider,
		PrivateActivationUnknownPersistence, PrivateActivationUnknownContainment, PrivateActivationUnknownInterrupted,
		PrivateActivationUnknownCanceled:
		return true
	default:
		return false
	}
}

func validPrivateActivationStopReason(value string) bool {
	switch value {
	case PrivateActivationStopInputDrift, PrivateActivationStopSnapshotDrift, PrivateActivationStopReservation,
		PrivateActivationStopCellContract, PrivateActivationStopProviderSession, PrivateActivationStopSnapshotCleanup,
		PrivateActivationStopOperatorRecovery:
		return true
	default:
		return false
	}
}

func emptyPrivateActivationEventPayload(event PrivateActivationStudyEvent) bool {
	return event.ReservedCostMicroUSD == 0 && !event.CostKnown && event.DetectedCostMicroUSD == 0 &&
		!event.ProviderCompleted && !event.PersistenceComplete && !event.ContainmentCertain && event.ReceiptSHA256 == "" &&
		event.Outcome == "" && event.Reason == ""
}

func privateActivationLifecycleError(code string) error {
	return fmt.Errorf("%w: %s", ErrPrivateActivationLifecycle, code)
}
