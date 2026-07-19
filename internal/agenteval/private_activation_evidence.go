package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/isukharev/atl/internal/safepath"
)

const privateActivationExecutionReceiptSchemaVersion = 1

type privateActivationExecutionReceipt struct {
	SchemaVersion int       `json:"schema_version"`
	Output        RunOutput `json:"output"`
	ResultSHA256  string    `json:"result_sha256"`
	FinalSHA256   string    `json:"final_sha256"`
}

func persistPrivateActivationExecutionReceipt(root, runRoot string, plan privatePlan, item privatePlanItem, output RunOutput) (string, error) {
	if len(output.Results) != 1 {
		return "", privatePlanError("execution_receipt_results")
	}
	runDirectory := filepath.Join(runRoot, "raw", item.ScenarioID, item.Provider, item.Variant, "run-01")
	resultData, err := readPrivatePlanLifecycleFile(root, filepath.Join(runDirectory, "result.json"), maxContractBytes)
	if err != nil {
		return "", privatePlanError("execution_receipt_result")
	}
	result, err := DecodeResult(bytes.NewReader(resultData))
	outputResultData, encodeErr := json.MarshalIndent(output.Results[0], "", "  ")
	outputResultData = append(outputResultData, '\n')
	if err != nil || encodeErr != nil || !bytes.Equal(resultData, outputResultData) {
		return "", privatePlanError("execution_receipt_result")
	}
	// Decode validation above proves that the exact runner-emitted bytes are a
	// valid Result. Keep the decoded value live so future changes cannot turn
	// this into a mere byte-shape check.
	_ = result
	finalData, err := readPrivatePlanLifecycleFile(root, filepath.Join(runDirectory, "final.json"), 16<<20)
	if err != nil || len(finalData) == 0 {
		return "", privatePlanError("execution_receipt_final")
	}
	receipt := privateActivationExecutionReceipt{SchemaVersion: privateActivationExecutionReceiptSchemaVersion, Output: output,
		ResultSHA256: sha256HexBytes(resultData), FinalSHA256: sha256HexBytes(finalData)}
	data, err := encodePrivateActivationExecutionReceipt(receipt)
	if err != nil {
		return "", err
	}
	path := filepath.Join(runRoot, "contracts", privatePlanItemContractKey(plan, item), "execution-receipt.json")
	if err := safepath.WriteFileExclusiveWithin(root, path, data, 0o600); err != nil {
		return "", privatePlanError("execution_receipt_write")
	}
	return sha256HexBytes(data), nil
}

func validatePrivateActivationExecutionReceipt(root string, surface PrivateBaselineSurfaceSource, rawData, finalData []byte, raw Result) error {
	receipt, _, err := readAndValidatePrivateActivationExecutionReceipt(root, surface.ExecutionReceiptPath,
		surface.ExecutionReceiptSHA256, surface.RunDirectory, rawData, finalData, raw)
	if err != nil || receipt.Output.EstimatedCostMicroUSDTotal != surface.ExecutionCostMicroUSD {
		return privatePlanError("reference_receipt")
	}
	return nil
}

func readAndValidatePrivateActivationExecutionReceipt(root, receiptPath, expectedSHA256, runDirectory string,
	rawData, finalData []byte, raw Result,
) (privateActivationExecutionReceipt, []byte, error) {
	data, err := readPrivatePlanLifecycleFile(root, receiptPath, maxContractBytes)
	if err != nil || (expectedSHA256 != "" && sha256HexBytes(data) != expectedSHA256) {
		return privateActivationExecutionReceipt{}, nil, privatePlanError("execution_receipt_read")
	}
	var receipt privateActivationExecutionReceipt
	if decodePrivateLifecycleJSON(data, &receipt) != nil || receipt.SchemaVersion != privateActivationExecutionReceiptSchemaVersion ||
		len(receipt.Output.Results) != 1 || receipt.ResultSHA256 != sha256HexBytes(rawData) ||
		receipt.FinalSHA256 != sha256HexBytes(finalData) || !reflect.DeepEqual(receipt.Output.Results[0], raw) {
		return privateActivationExecutionReceipt{}, nil, privatePlanError("execution_receipt_binding")
	}
	canonical, err := encodePrivateActivationExecutionReceipt(receipt)
	if err != nil || !bytes.Equal(canonical, data) {
		return privateActivationExecutionReceipt{}, nil, privatePlanError("execution_receipt_canonical")
	}
	if runDirectory != "" {
		storedRaw, rawErr := readPrivatePlanLifecycleFile(root, filepath.Join(runDirectory, "result.json"), maxContractBytes)
		storedFinal, finalErr := readPrivatePlanLifecycleFile(root, filepath.Join(runDirectory, "final.json"), 16<<20)
		if rawErr != nil || finalErr != nil || !bytes.Equal(storedRaw, rawData) || !bytes.Equal(storedFinal, finalData) {
			return privateActivationExecutionReceipt{}, nil, privatePlanError("execution_receipt_files")
		}
	}
	return receipt, data, nil
}

func privateActivationPlanItemForCell(plan privatePlan, cellID string) (privatePlanItem, bool) {
	for _, item := range plan.Items {
		if item.CellID == cellID {
			return item, true
		}
	}
	return privatePlanItem{}, false
}

func privateActivationItemEvidencePaths(root string, plan privatePlan, runID string, item privatePlanItem) (string, string) {
	runRoot := filepath.Join(root, "runs", runID)
	receiptPath := filepath.Join(runRoot, "contracts", privatePlanItemContractKey(plan, item), "execution-receipt.json")
	runDirectory := filepath.Join(runRoot, "raw", item.ScenarioID, item.Provider, item.Variant, "run-01")
	return receiptPath, runDirectory
}

func validatePrivateActivationStateEvidence(root string, plan privatePlan, state privatePlanState) error {
	for _, event := range state.Events {
		if event.Type != PrivateActivationEventReceipt {
			continue
		}
		item, ok := privateActivationPlanItemForCell(plan, event.CellID)
		if !ok {
			return privatePlanError("study_evidence")
		}
		receiptPath, runDirectory := privateActivationItemEvidencePaths(root, plan, state.RunID, item)
		rawData, rawErr := readPrivatePlanLifecycleFile(root, filepath.Join(runDirectory, "result.json"), maxContractBytes)
		finalData, finalErr := readPrivatePlanLifecycleFile(root, filepath.Join(runDirectory, "final.json"), 16<<20)
		raw, decodeErr := DecodeResult(bytes.NewReader(rawData))
		receipt, _, receiptErr := readAndValidatePrivateActivationExecutionReceipt(root, receiptPath, event.ReceiptSHA256,
			runDirectory, rawData, finalData, raw)
		costKnown := true
		for _, result := range receipt.Output.Results {
			costKnown = costKnown && result.Coverage["estimated_cost_microusd"]
		}
		detectedCost := receipt.Output.EstimatedCostMicroUSDTotal
		if !costKnown {
			detectedCost = 0
		}
		if rawErr != nil || finalErr != nil || decodeErr != nil || receiptErr != nil ||
			event.CostKnown != costKnown || event.DetectedCostMicroUSD != detectedCost ||
			!event.ProviderCompleted || !event.PersistenceComplete || !event.ContainmentCertain {
			return privatePlanError("study_evidence")
		}
	}
	return nil
}

func encodePrivateActivationExecutionReceipt(receipt privateActivationExecutionReceipt) ([]byte, error) {
	if receipt.SchemaVersion != privateActivationExecutionReceiptSchemaVersion || len(receipt.Output.Results) != 1 ||
		!validSHA256(receipt.ResultSHA256) || !validSHA256(receipt.FinalSHA256) || receipt.Output.EstimatedCostMicroUSDTotal < 0 {
		return nil, fmt.Errorf("invalid private activation execution receipt")
	}
	for _, result := range receipt.Output.Results {
		if result.Validate() != nil {
			return nil, fmt.Errorf("invalid private activation execution receipt")
		}
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func privateActivationReceiptEvent(events []PrivateActivationLifecycleEvent, cellID string) (PrivateActivationLifecycleEvent, bool) {
	var found PrivateActivationLifecycleEvent
	for _, event := range events {
		if event.CellID == cellID && event.Type == PrivateActivationEventReceipt {
			if found.Type != "" {
				return PrivateActivationLifecycleEvent{}, false
			}
			found = event
		}
	}
	return found, found.Type != ""
}
