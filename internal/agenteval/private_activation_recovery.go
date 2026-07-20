package agenteval

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/safepath"
)

// PrivateActivationRecoveryConfirmation is an operator attestation, not merely
// a UI guard: after a controller crash atl cannot mechanically prove that no
// orphaned provider process remains. The operator must establish quiescence
// before allowing offline lifecycle reconciliation.
const PrivateActivationRecoveryConfirmation = "PROVIDER_STOPPED_RECOVER"

type PrivateActivationRecoveryOptions struct {
	Root, RepositoryRoot, PlanID, ExpectedPlanSHA256, Confirm string
}

type PrivateActivationRecoverySummary struct {
	SchemaVersion         int      `json:"schema_version"`
	PlanID                string   `json:"plan_id"`
	RunID                 string   `json:"run_id"`
	Status                string   `json:"status"`
	Surfaces              []string `json:"surfaces"`
	Completed             int      `json:"completed"`
	EstimatedCostMicroUSD int64    `json:"estimated_cost_microusd"`
	CostKnown             bool     `json:"cost_known"`
}

// RecoverPrivateActivationStudy closes a crash-interrupted activation attempt
// without ever launching or replaying the provider. Active cells become
// unknown; between-cell and pre-run states receive an explicit terminal stop.
func RecoverPrivateActivationStudy(options PrivateActivationRecoveryOptions) (PrivateActivationRecoverySummary, error) {
	if options.Confirm != PrivateActivationRecoveryConfirmation || !privatePlanIDRE.MatchString(options.PlanID) || !validSHA256(options.ExpectedPlanSHA256) {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_approval")
	}
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateActivationRecoverySummary{}, privatePlanError("workspace")
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivateActivationRecoverySummary{}, privatePlanError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	plan, planData, err := loadPrivatePlan(root, options.PlanID)
	if err != nil || plan.SchemaVersion != PrivatePlanSchemaVersion || plan.Kind != PrivateRunSetKindActivationStudy || sha256HexBytes(planData) != options.ExpectedPlanSHA256 {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_plan")
	}
	statePath := filepath.Join(root, "plans", options.PlanID+".state.json")
	stateData, err := readPrivatePlanLifecycleFile(root, statePath, 1<<20)
	if err != nil {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_state")
	}
	var state privatePlanState
	if decodePrivateLifecycleJSON(stateData, &state) != nil || validatePrivatePlanState(state) != nil ||
		state.PlanSHA256 != options.ExpectedPlanSHA256 || validatePrivateActivationPlanState(plan, state) != nil ||
		(state.Status != "running" && state.Status != "interrupted") {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_state")
	}
	lifecycle, err := NewPrivateActivationStudyLifecycle(*plan.StudyContract)
	if err != nil {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_state")
	}
	lifecycle.Events = append([]PrivateActivationStudyEvent(nil), state.Events...)
	projection, err := lifecycle.project()
	if err != nil {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_state")
	}
	if err := removePrivateActivationRecoverySnapshot(root, state.RunID); err != nil || !inspectPrivateWorkspaceScratch(root) {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_cleanup")
	}
	switch projection.activePhase {
	case PrivateActivationEventCalibrationProviderCommitted:
		if err := recoverPrivateActivationCalibrationReceipt(root, plan, state, &lifecycle); err != nil {
			return PrivateActivationRecoverySummary{}, privatePlanError("recovery_evidence")
		}
		projection, err = lifecycle.project()
		if err != nil {
			return PrivateActivationRecoverySummary{}, privatePlanError("recovery_state")
		}
	case PrivateActivationEventCalibrationReceipt:
		candidate := state
		candidate.Events = append([]PrivateActivationLifecycleEvent(nil), lifecycle.Events...)
		if err := validatePrivateActivationStateEvidence(root, plan, candidate); err != nil {
			return PrivateActivationRecoverySummary{}, privatePlanError("recovery_evidence")
		}
	case PrivateActivationEventProviderCommitted:
		if err := recoverPrivateActivationReceipt(root, plan, state, &lifecycle, projection.activeCell); err != nil {
			return PrivateActivationRecoverySummary{}, privatePlanError("recovery_evidence")
		}
		projection, err = lifecycle.project()
		if err != nil {
			return PrivateActivationRecoverySummary{}, privatePlanError("recovery_state")
		}
	case PrivateActivationEventReceipt:
		candidate := state
		candidate.Events = append([]PrivateActivationLifecycleEvent(nil), lifecycle.Events...)
		if err := validatePrivateActivationStateEvidence(root, plan, candidate); err != nil {
			return PrivateActivationRecoverySummary{}, privatePlanError("recovery_evidence")
		}
	}
	if projection.activePhase != "" {
		if err := lifecycle.MarkUnknown(projection.activeCell, PrivateActivationUnknownInterrupted); err != nil {
			return PrivateActivationRecoverySummary{}, privatePlanError("recovery_transition")
		}
	} else if lifecycle.Status() != PrivateActivationStudyStopped {
		if err := lifecycle.Stop(PrivateActivationStopOperatorRecovery); err != nil {
			return PrivateActivationRecoverySummary{}, privatePlanError("recovery_transition")
		}
	}
	if err := validatePrivateActivationStateEvidence(root, plan, privatePlanState{RunID: state.RunID, Events: lifecycle.Events}); err != nil {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_evidence")
	}
	runRoot := filepath.Join(root, "runs", state.RunID)
	if info, statErr := safepath.StatWithin(root, runRoot); os.IsNotExist(statErr) {
		if len(state.Events) != 0 || state.Status != "interrupted" || safepath.MkdirAllWithin(root, runRoot, 0o700) != nil {
			return PrivateActivationRecoverySummary{}, privatePlanError("recovery_run")
		}
	} else if statErr != nil || !info.IsDir() || !privateWorkspaceDirectoryMode(info.Mode()) {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_run")
	}
	if err := persistPrivateActivationPlanState(statePath, plan, &state, &lifecycle, ""); err != nil {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_write")
	}
	// Re-read the canonical state so the summary can never overstate a failed
	// atomic write or an in-memory transition.
	written, err := readPrivatePlanLifecycleFile(root, statePath, 1<<20)
	if err != nil || decodePrivateLifecycleJSON(written, &state) != nil || validatePrivatePlanState(state) != nil ||
		validatePrivateActivationPlanState(plan, state) != nil || state.Status != "stopped" {
		return PrivateActivationRecoverySummary{}, privatePlanError("recovery_verify")
	}
	return PrivateActivationRecoverySummary{SchemaVersion: 2, PlanID: plan.PlanID, RunID: state.RunID,
		Status: state.Status, Surfaces: []string{SurfaceCLISkill}, Completed: len(state.CompletedCells),
		EstimatedCostMicroUSD: state.EstimatedCostMicroUSD, CostKnown: privateActivationDetectedCostKnown(state.Events)}, nil
}

func recoverPrivateActivationCalibrationReceipt(root string, plan privatePlan, state privatePlanState,
	lifecycle *PrivateActivationStudyLifecycle,
) error {
	path := filepath.Join(root, "runs", state.RunID, "calibration", "execution-receipt.json")
	if _, err := safepath.StatWithin(root, path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return privatePlanError("recovery_calibration_receipt")
	}
	data, err := readPrivatePlanLifecycleFile(root, path, 1<<20)
	if err != nil {
		return privatePlanError("recovery_calibration_receipt")
	}
	var envelope privateActivationCalibrationExecutionReceipt
	if decodePrivateLifecycleJSON(data, &envelope) != nil ||
		validatePrivateActivationCalibrationReceipt(root, state.RunID, plan, sha256HexBytes(data)) != nil {
		return privatePlanError("recovery_calibration_receipt")
	}
	return lifecycle.RecordCalibrationReceipt(PrivateActivationReceipt{
		SHA256:               sha256HexBytes(data),
		CostKnown:            true,
		DetectedCostMicroUSD: envelope.Receipt.EstimatedCostMicroUSD,
		ProviderCompleted:    true,
		PersistenceComplete:  true,
		ContainmentCertain:   true,
	})
}

func removePrivateActivationRecoverySnapshot(root, runID string) error {
	if !privateRunIDRE.MatchString(runID) {
		return privatePlanError("recovery_snapshot")
	}
	target := filepath.Join(root, ".ephemeral", "execution-"+runID)
	info, err := safepath.StatWithin(root, target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() || !privateWorkspaceDirectoryMode(info.Mode()) {
		return privatePlanError("recovery_snapshot")
	}
	return removePrivateTree(root, target)
}

func recoverPrivateActivationReceipt(root string, plan privatePlan, state privatePlanState,
	lifecycle *PrivateActivationStudyLifecycle, cellID string,
) error {
	item, ok := privateActivationPlanItemForCell(plan, cellID)
	if !ok {
		return privatePlanError("recovery_receipt")
	}
	receiptPath, runDirectory := privateActivationItemEvidencePaths(root, plan, state.RunID, item)
	if _, err := safepath.StatWithin(root, receiptPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return privatePlanError("recovery_receipt")
	}
	rawData, rawErr := readPrivatePlanLifecycleFile(root, filepath.Join(runDirectory, "result.json"), maxContractBytes)
	finalData, finalErr := readPrivatePlanLifecycleFile(root, filepath.Join(runDirectory, "final.json"), 16<<20)
	raw, decodeErr := DecodeResult(bytes.NewReader(rawData))
	receipt, data, receiptErr := readAndValidatePrivateActivationExecutionReceipt(root, receiptPath, "", runDirectory,
		rawData, finalData, raw)
	if rawErr != nil || finalErr != nil || decodeErr != nil || receiptErr != nil {
		return privatePlanError("recovery_receipt")
	}
	costKnown := true
	for _, result := range receipt.Output.Results {
		costKnown = costKnown && result.Coverage["estimated_cost_microusd"]
	}
	detectedCost := receipt.Output.EstimatedCostMicroUSDTotal
	if !costKnown {
		detectedCost = 0
	}
	return lifecycle.RecordReceipt(cellID, PrivateActivationReceipt{SHA256: sha256HexBytes(data), CostKnown: costKnown,
		DetectedCostMicroUSD: detectedCost, ProviderCompleted: true, PersistenceComplete: true, ContainmentCertain: true})
}
