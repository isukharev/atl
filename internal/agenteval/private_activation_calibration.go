package agenteval

import (
	"bytes"
	"encoding/json"
	"path/filepath"

	"github.com/isukharev/atl/internal/safepath"
)

const privateActivationCalibrationReceiptSchemaVersion = 1

type privateActivationCalibrationExecutionReceipt struct {
	SchemaVersion int                         `json:"schema_version"`
	PlanSHA256    string                      `json:"plan_sha256"`
	Contract      CodexCLICalibrationContract `json:"contract"`
	Receipt       CodexCLICalibrationReceipt  `json:"receipt"`
}

func persistPrivateActivationCalibrationReceipt(root, runRoot string, plan privatePlan, contract CodexCLICalibrationContract, receipt CodexCLICalibrationReceipt) (string, error) {
	if plan.StudyContract == nil || contract.SHA256 != plan.StudyContract.Calibration.ContractSHA256 ||
		contract.MaxEstimatedCostMicroUSD != plan.StudyContract.Calibration.MaxEstimatedCostMicroUSD || receipt.Validate(contract) != nil {
		return "", privatePlanError("calibration_receipt_binding")
	}
	planData, err := encodePrivatePlan(plan)
	if err != nil {
		return "", err
	}
	envelope := privateActivationCalibrationExecutionReceipt{SchemaVersion: privateActivationCalibrationReceiptSchemaVersion,
		PlanSHA256: sha256HexBytes(planData), Contract: contract, Receipt: receipt}
	data, err := encodePrivateActivationCalibrationReceipt(envelope)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(runRoot, "calibration")
	if err := safepath.MkdirAllWithin(root, directory, 0o700); err != nil {
		return "", privatePlanError("calibration_receipt_directory")
	}
	path := filepath.Join(directory, "execution-receipt.json")
	if err := safepath.WriteFileExclusiveWithin(root, path, data, 0o600); err != nil {
		return "", privatePlanError("calibration_receipt_write")
	}
	return sha256HexBytes(data), nil
}

func validatePrivateActivationCalibrationReceipt(root, runID string, plan privatePlan, expectedSHA256 string) error {
	if !privateRunIDRE.MatchString(runID) || !validSHA256(expectedSHA256) || plan.StudyContract == nil {
		return privatePlanError("calibration_receipt_binding")
	}
	path := filepath.Join(root, "runs", runID, "calibration", "execution-receipt.json")
	data, err := safepath.ReadFileWithinLimit(root, path, 1<<20)
	if err != nil {
		return privatePlanError("calibration_receipt_read")
	}
	var envelope privateActivationCalibrationExecutionReceipt
	if decodePrivateLifecycleJSON(data, &envelope) != nil || sha256HexBytes(data) != expectedSHA256 {
		return privatePlanError("calibration_receipt_binding")
	}
	canonical, err := encodePrivateActivationCalibrationReceiptForPlan(envelope, plan.SchemaVersion)
	if err != nil || !bytes.Equal(canonical, data) {
		return privatePlanError("calibration_receipt_canonical")
	}
	planData, err := encodePrivatePlan(plan)
	if err != nil || envelope.PlanSHA256 != sha256HexBytes(planData) || validatePrivateActivationCalibrationEnvelope(envelope, plan.SchemaVersion) != nil ||
		envelope.Contract.SHA256 != plan.StudyContract.Calibration.ContractSHA256 ||
		envelope.Contract.MaxEstimatedCostMicroUSD != plan.StudyContract.Calibration.MaxEstimatedCostMicroUSD {
		return privatePlanError("calibration_receipt_binding")
	}
	return nil
}

func encodePrivateActivationCalibrationReceipt(receipt privateActivationCalibrationExecutionReceipt) ([]byte, error) {
	if receipt.SchemaVersion != privateActivationCalibrationReceiptSchemaVersion || !validSHA256(receipt.PlanSHA256) ||
		validatePrivateActivationCalibrationEnvelope(receipt, PrivatePlanSchemaVersion) != nil {
		return nil, privatePlanError("calibration_receipt_encode")
	}
	return marshalPrivateActivationCalibrationReceipt(receipt)
}

func encodePrivateActivationCalibrationReceiptForPlan(receipt privateActivationCalibrationExecutionReceipt, planSchemaVersion int) ([]byte, error) {
	if receipt.SchemaVersion != privateActivationCalibrationReceiptSchemaVersion || !validSHA256(receipt.PlanSHA256) ||
		validatePrivateActivationCalibrationEnvelope(receipt, planSchemaVersion) != nil {
		return nil, privatePlanError("calibration_receipt_encode")
	}
	return marshalPrivateActivationCalibrationReceipt(receipt)
}

func validatePrivateActivationCalibrationEnvelope(receipt privateActivationCalibrationExecutionReceipt, planSchemaVersion int) error {
	switch planSchemaVersion {
	case PrivatePlanSchemaVersion:
		if receipt.Contract.Validate() != nil || receipt.Receipt.Validate(receipt.Contract) != nil {
			return privatePlanError("calibration_receipt_contract")
		}
	case LegacyToolQualifiedPrivatePlanSchemaVersion:
		if receipt.Contract.validateLegacyToolQualified() != nil || receipt.Receipt.validateLegacyToolQualified(receipt.Contract) != nil {
			return privatePlanError("calibration_receipt_contract")
		}
	default:
		return privatePlanError("calibration_receipt_contract")
	}
	return nil
}

func marshalPrivateActivationCalibrationReceipt(receipt privateActivationCalibrationExecutionReceipt) ([]byte, error) {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, privatePlanError("calibration_receipt_encode")
	}
	return append(data, '\n'), nil
}

func markAndPersistPrivateActivationCalibrationFailed(statePath string, plan privatePlan, state *privatePlanState,
	lifecycle *PrivateActivationStudyLifecycle, reason string,
) error {
	if lifecycle == nil {
		return nil
	}
	if err := lifecycle.MarkCalibrationFailed(reason); err != nil {
		return err
	}
	return persistPrivateActivationPlanState(statePath, plan, state, lifecycle, "")
}

func privateActivationCalibrationPostRunUnknownReason(lifecycle *PrivateActivationStudyLifecycle) string {
	if lifecycle == nil {
		return PrivateActivationUnknownProvider
	}
	projection, err := lifecycle.project()
	if err == nil && (projection.activePhase == PrivateActivationEventCalibrationProviderCommitted ||
		projection.activePhase == PrivateActivationEventCalibrationReceipt) {
		return PrivateActivationUnknownProvider
	}
	return PrivateActivationUnknownInterrupted
}
