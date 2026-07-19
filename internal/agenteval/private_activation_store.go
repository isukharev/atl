package agenteval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/safepath"
)

const (
	PrivateActivationReferenceConfirmation = "REFERENCE"
	PrivateActivationPromotionConfirmation = "PROMOTE"
	privateActivationStoredSchemaVersion   = 1
)

type PrivateActivationReferenceSetOptions struct {
	Root, RepositoryRoot, PlanID, Reference, Confirm string
}

type PrivateActivationReferenceSummary struct {
	SchemaVersion int                    `json:"schema_version"`
	Stored        bool                   `json:"stored"`
	Gates         PrivateActivationGates `json:"gates"`
}

type PrivateActivationPromotionOptions struct {
	Root, RepositoryRoot, Reference, Confirm string
}

type PrivateActivationPromotionSummary struct {
	SchemaVersion int  `json:"schema_version"`
	Promoted      bool `json:"promoted"`
}

type PrivateActivationReferenceCompareOptions struct {
	Root, RepositoryRoot, Reference string
}

type privateStoredActivationReference struct {
	SchemaVersion        int    `json:"schema_version"`
	ReferenceAlias       string `json:"reference_alias"`
	PlanID               string `json:"plan_id"`
	PlanSHA256           string `json:"plan_sha256"`
	CommonContractSHA256 string `json:"common_contract_sha256"`
	// RunTreeSHA256 proves which complete immutable run tree was consumed at
	// capture time. Compare and promotion intentionally use the compact stored
	// reference after capture so normal private-run pruning remains possible.
	RunTreeSHA256 string                     `json:"run_tree_sha256"`
	Reference     PrivateActivationReference `json:"reference"`
}

type privateActivationCurrentPointer struct {
	SchemaVersion   int    `json:"schema_version"`
	Reference       string `json:"reference"`
	ReferenceSHA256 string `json:"reference_sha256"`
}

func SetPrivateActivationReference(options PrivateActivationReferenceSetOptions) (PrivateActivationReferenceSummary, error) {
	if options.Confirm != PrivateActivationReferenceConfirmation || !validPrivateActivationReferenceAlias(options.Reference) {
		return PrivateActivationReferenceSummary{}, privatePlanError("reference_approval")
	}
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateActivationReferenceSummary{}, privatePlanError("workspace")
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivateActivationReferenceSummary{}, privatePlanError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()

	source, err := LoadCompletedPrivateRun(root, options.RepositoryRoot, options.PlanID)
	if err != nil || source.Kind != PrivateRunSetKindActivationStudy || len(source.Surfaces) != 4 {
		return PrivateActivationReferenceSummary{}, privatePlanError("reference_source")
	}
	plan, _, err := loadPrivatePlan(root, source.PlanID)
	if err != nil || plan.ActivationContract == nil {
		return PrivateActivationReferenceSummary{}, privatePlanError("reference_source")
	}
	initialTreeHash, _, _, err := hashPrivatePruneTree(root, source.RunRoot)
	if err != nil {
		return PrivateActivationReferenceSummary{}, privatePlanError("reference_tree")
	}
	inputs := make([]PrivateActivationCellInput, 0, len(source.Surfaces))
	for _, surface := range source.Surfaces {
		rawData, rawErr := readPrivatePlanLifecycleFile(root, filepath.Join(surface.RunDirectory, "result.json"), maxContractBytes)
		data, readErr := readPrivatePlanLifecycleFile(root, filepath.Join(surface.RunDirectory, "reviewed-result.json"), maxContractBytes)
		if readErr != nil {
			return PrivateActivationReferenceSummary{}, privatePlanError("reference_review")
		}
		raw, rawDecodeErr := DecodeResult(bytes.NewReader(rawData))
		result, decodeErr := DecodeResult(bytes.NewReader(data))
		finalData, finalErr := readPrivatePlanLifecycleFile(root, filepath.Join(surface.RunDirectory, "final.json"), 16<<20)
		if rawErr != nil || rawDecodeErr != nil || decodeErr != nil || result.Runtime.SkillActivation != surface.SkillActivation ||
			!samePrivateResultIdentity(raw, result) || !hasPrivateQualitativeAssessment(result) || result.QualitativeReviewSet == nil ||
			result.QualitativeReviewSet.AssignmentDigest == "" || !result.QualitativeReviewSet.Blinded ||
			finalErr != nil || validatePrivateActivationExecutionReceipt(root, surface, rawData, finalData, raw) != nil ||
			validatePrivateAssessmentBinding(root, surface, rawData, result) != nil {
			return PrivateActivationReferenceSummary{}, privatePlanError("reference_result")
		}
		var runChecks []RunCheck
		matchedItem := false
		for _, item := range plan.Items {
			if item.CellID == surface.CellID {
				matchedItem = true
				if !privateActivationResultMatchesPlan(raw, plan, item) {
					return PrivateActivationReferenceSummary{}, privatePlanError("reference_result_identity")
				}
				loaded, loadErr := loadRunInputs(RunOptions{SpecPath: filepath.Join(root, filepath.FromSlash(item.SpecPath))})
				if loadErr != nil {
					return PrivateActivationReferenceSummary{}, privatePlanError("reference_spec")
				}
				treatment, ok := plan.ActivationContract.Treatment(surface.SkillActivation)
				exactSpec, encodeErr := json.Marshal(loaded.spec)
				if !ok || encodeErr != nil || sha256HexBytes(exactSpec) != treatment.RunSpecSHA256 {
					return PrivateActivationReferenceSummary{}, privatePlanError("reference_spec_drift")
				}
				runChecks = append([]RunCheck(nil), loaded.spec.Checks...)
				break
			}
		}
		if !matchedItem || len(runChecks) == 0 {
			return PrivateActivationReferenceSummary{}, privatePlanError("reference_spec")
		}
		classification := classifyPrivateActivationResults([]Result{result}, runChecks)
		inputs = append(inputs, PrivateActivationCellInput{Treatment: surface.SkillActivation, Result: result,
			SafetyComplete: classification.Complete, SafetyViolationCount: classification.SafetyViolations})
	}
	reference, err := CapturePrivateActivationReference(inputs)
	if err != nil {
		return PrivateActivationReferenceSummary{}, privatePlanError("reference_capture")
	}
	gates, _ := PrivateActivationReferenceGates(reference)
	treeHash, _, _, err := hashPrivatePruneTree(root, source.RunRoot)
	if err != nil || treeHash != initialTreeHash {
		return PrivateActivationReferenceSummary{}, privatePlanError("reference_tree")
	}
	stored := privateStoredActivationReference{SchemaVersion: privateActivationStoredSchemaVersion, ReferenceAlias: options.Reference,
		PlanID: source.PlanID, PlanSHA256: source.PlanSHA256,
		CommonContractSHA256: plan.ActivationContract.CommonContractSHA256, RunTreeSHA256: treeHash, Reference: reference}
	data, err := encodePrivateStoredActivationReference(stored)
	if err != nil {
		return PrivateActivationReferenceSummary{}, err
	}
	directory, err := privateActivationReferenceDirectory(root, true)
	if err != nil {
		return PrivateActivationReferenceSummary{}, privatePlanError("reference_directory")
	}
	path := filepath.Join(directory, options.Reference+".json")
	if existing, readErr := safepath.ReadFileWithinLimit(root, path, maxContractBytes); readErr == nil {
		info, statErr := safepath.StatWithin(root, path)
		if statErr != nil || !info.Mode().IsRegular() || !privateWorkspaceFileMode(info.Mode()) {
			return PrivateActivationReferenceSummary{}, privatePlanError("reference_mode")
		}
		if !bytes.Equal(existing, data) {
			return PrivateActivationReferenceSummary{}, privatePlanError("reference_exists")
		}
	} else if !os.IsNotExist(readErr) {
		return PrivateActivationReferenceSummary{}, privatePlanError("reference_read")
	} else if err := safepath.WriteFileExclusiveWithin(root, path, data, 0o600); err != nil {
		return PrivateActivationReferenceSummary{}, privatePlanError("reference_write")
	}
	return PrivateActivationReferenceSummary{SchemaVersion: 1, Stored: true, Gates: gates}, nil
}

func CompareStoredPrivateActivationReference(options PrivateActivationReferenceCompareOptions) (PrivateActivationReport, error) {
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil || (options.Reference != "current" && !validPrivateActivationReferenceAlias(options.Reference)) {
		return PrivateActivationReport{}, privatePlanError("reference_input")
	}
	if _, err := privateActivationReferenceDirectory(root, false); err != nil {
		return PrivateActivationReport{}, privatePlanError("reference_directory")
	}
	stored, _, err := loadPrivateStoredActivationReference(root, options.Reference)
	if err != nil {
		return PrivateActivationReport{}, err
	}
	return ComparePrivateActivationReference(stored.Reference)
}

func PromotePrivateActivationReference(options PrivateActivationPromotionOptions) (PrivateActivationPromotionSummary, error) {
	if options.Confirm != PrivateActivationPromotionConfirmation || !validPrivateActivationReferenceAlias(options.Reference) {
		return PrivateActivationPromotionSummary{}, privatePlanError("promotion_approval")
	}
	root, _, err := privateWorkspaceLocations(options.Root, options.RepositoryRoot, false)
	if err != nil {
		return PrivateActivationPromotionSummary{}, privatePlanError("workspace")
	}
	lock, err := acquirePrivateWorkspaceLock(root)
	if err != nil {
		return PrivateActivationPromotionSummary{}, privatePlanError("workspace_busy")
	}
	defer func() { _ = lock.Unlock() }()
	if _, err := privateActivationReferenceDirectory(root, false); err != nil {
		return PrivateActivationPromotionSummary{}, privatePlanError("reference_directory")
	}
	stored, data, err := loadPrivateStoredActivationReference(root, options.Reference)
	if err != nil {
		return PrivateActivationPromotionSummary{}, err
	}
	if err := ValidatePrivateActivationReferencePromotion(stored.Reference); err != nil {
		return PrivateActivationPromotionSummary{}, privatePlanError("promotion_gates")
	}
	pointer := privateActivationCurrentPointer{SchemaVersion: 1, Reference: options.Reference, ReferenceSHA256: sha256HexBytes(data)}
	pointerData, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return PrivateActivationPromotionSummary{}, privatePlanError("promotion_encode")
	}
	path := filepath.Join(root, "baselines", "activation-studies", "current.json")
	if err := safepath.WriteFileWithin(root, path, append(pointerData, '\n'), 0o600); err != nil {
		return PrivateActivationPromotionSummary{}, privatePlanError("promotion_write")
	}
	return PrivateActivationPromotionSummary{SchemaVersion: 1, Promoted: true}, nil
}

func loadPrivateStoredActivationReference(root, alias string) (privateStoredActivationReference, []byte, error) {
	expectedDigest := ""
	if alias == "current" {
		pointerData, err := readPrivatePlanLifecycleFile(root, filepath.Join(root, "baselines", "activation-studies", "current.json"), maxContractBytes)
		if err != nil {
			return privateStoredActivationReference{}, nil, privatePlanError("current_reference")
		}
		var pointer privateActivationCurrentPointer
		if decodePrivateLifecycleJSON(pointerData, &pointer) != nil || pointer.SchemaVersion != 1 ||
			!validPrivateActivationReferenceAlias(pointer.Reference) || !validSHA256(pointer.ReferenceSHA256) {
			return privateStoredActivationReference{}, nil, privatePlanError("current_reference")
		}
		alias = pointer.Reference
		expectedDigest = pointer.ReferenceSHA256
	}
	path := filepath.Join(root, "baselines", "activation-studies", alias+".json")
	data, err := readPrivatePlanLifecycleFile(root, path, maxContractBytes)
	if err != nil {
		return privateStoredActivationReference{}, nil, privatePlanError("reference_read")
	}
	var stored privateStoredActivationReference
	if decodePrivateLifecycleJSON(data, &stored) != nil || stored.SchemaVersion != privateActivationStoredSchemaVersion || stored.ReferenceAlias != alias ||
		!validPrivateActivationReferenceAlias(stored.ReferenceAlias) || !privatePlanIDRE.MatchString(stored.PlanID) ||
		!validSHA256(stored.PlanSHA256) || !validSHA256(stored.CommonContractSHA256) || !validSHA256(stored.RunTreeSHA256) || stored.Reference.Validate() != nil {
		return privateStoredActivationReference{}, nil, privatePlanError("reference_decode")
	}
	canonical, err := encodePrivateStoredActivationReference(stored)
	if err != nil || !bytes.Equal(data, canonical) {
		return privateStoredActivationReference{}, nil, privatePlanError("reference_canonical")
	}
	if expectedDigest != "" && sha256HexBytes(data) != expectedDigest {
		return privateStoredActivationReference{}, nil, privatePlanError("current_reference")
	}
	plan, planData, err := loadPrivatePlan(root, stored.PlanID)
	if err != nil || sha256HexBytes(planData) != stored.PlanSHA256 || plan.Kind != PrivateRunSetKindActivationStudy ||
		plan.ActivationContract == nil || plan.ActivationContract.CommonContractSHA256 != stored.CommonContractSHA256 {
		return privateStoredActivationReference{}, nil, privatePlanError("reference_plan")
	}
	return stored, data, nil
}

func encodePrivateStoredActivationReference(stored privateStoredActivationReference) ([]byte, error) {
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return nil, privatePlanError("reference_encode")
	}
	return append(data, '\n'), nil
}

func privateActivationResultMatchesPlan(result Result, plan privatePlan, item privatePlanItem) bool {
	return result.DataClass == "private-local" && result.ScenarioID == item.ScenarioID && result.Variant == item.Variant &&
		result.EffectiveSurface() == item.Surface && result.Runtime.Provider == item.Provider &&
		result.Runtime.Provider == plan.Provider && result.Runtime.Model == plan.Model &&
		result.Runtime.SkillActivation == item.SkillActivation && result.Runtime.PromptContractSHA256 == item.PromptContractSHA256
}

func privateActivationReferenceDirectory(root string, create bool) (string, error) {
	directory := filepath.Join(root, "baselines", "activation-studies")
	if create {
		if err := safepath.MkdirAllWithin(root, directory, 0o700); err != nil {
			return "", err
		}
	}
	info, err := safepath.StatWithin(root, directory)
	if err != nil || !info.IsDir() || !privateWorkspaceDirectoryMode(info.Mode()) {
		return "", privatePlanError("reference_directory_mode")
	}
	return directory, nil
}

func validPrivateActivationReferenceAlias(value string) bool {
	return value != "current" && privateWorkspaceAliasRE.MatchString(value)
}
