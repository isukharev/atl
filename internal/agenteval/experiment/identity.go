package experiment

import "sort"

func SealCapabilityContract(input CapabilityContract) (CapabilityContract, error) {
	contract := cloneCapabilityContract(input)
	contract.Schema = CapabilitySchema
	contract.SchemaVersion = SchemaVersion
	contract.ContractVersion = ContractVersion
	sort.Slice(contract.Capabilities, func(left, right int) bool {
		return contract.Capabilities[left].ID < contract.Capabilities[right].ID
	})
	contract.CapabilityContractSHA256 = ""
	if err := validateCapabilityContractShape(contract, false); err != nil {
		return CapabilityContract{}, err
	}
	digest, err := digestCapabilityContract(contract)
	if err != nil {
		return CapabilityContract{}, contractError(ErrorInvalidCapability, err)
	}
	contract.CapabilityContractSHA256 = digest
	if err := ValidateCapabilityContract(contract); err != nil {
		return CapabilityContract{}, err
	}
	return contract, nil
}

func ValidateCapabilityContract(contract CapabilityContract) error {
	if err := validateCapabilityContractShape(contract, true); err != nil {
		return err
	}
	digest, err := digestCapabilityContract(contract)
	if err != nil || digest != contract.CapabilityContractSHA256 {
		return contractError(ErrorInvalidCapability, err)
	}
	return nil
}

func SealAnalysisPlan(input AnalysisPlan) (AnalysisPlan, error) {
	plan := cloneAnalysisPlan(input)
	plan.Schema = AnalysisSchema
	plan.SchemaVersion = SchemaVersion
	plan.ContractVersion = ContractVersion
	sort.Slice(plan.Stages, func(left, right int) bool {
		return stageOrdinal(plan.Stages[left].Stage) < stageOrdinal(plan.Stages[right].Stage)
	})
	sort.Slice(plan.Metrics, func(left, right int) bool { return plan.Metrics[left].ID < plan.Metrics[right].ID })
	for index := range plan.Comparisons {
		sort.Slice(plan.Comparisons[index].Stages, func(left, right int) bool {
			return stageOrdinal(plan.Comparisons[index].Stages[left]) < stageOrdinal(plan.Comparisons[index].Stages[right])
		})
		sort.Slice(plan.Comparisons[index].Metrics, func(left, right int) bool {
			return plan.Comparisons[index].Metrics[left] < plan.Comparisons[index].Metrics[right]
		})
		plan.Comparisons[index].ID = ""
		identifier, err := comparisonID(plan.Comparisons[index])
		if err != nil {
			return AnalysisPlan{}, contractError(ErrorInvalidAnalysis, err)
		}
		plan.Comparisons[index].ID = identifier
	}
	sort.Slice(plan.Comparisons, func(left, right int) bool { return plan.Comparisons[left].ID < plan.Comparisons[right].ID })
	sort.Slice(plan.RepeatedAttempts.K, func(left, right int) bool {
		return plan.RepeatedAttempts.K[left] < plan.RepeatedAttempts.K[right]
	})
	sort.Slice(plan.AllowedExclusions, func(left, right int) bool {
		return plan.AllowedExclusions[left] < plan.AllowedExclusions[right]
	})
	plan.AnalysisPlanSHA256 = ""
	if err := validateAnalysisPlanShape(plan, false); err != nil {
		return AnalysisPlan{}, err
	}
	digest, err := digestAnalysisPlan(plan)
	if err != nil {
		return AnalysisPlan{}, contractError(ErrorInvalidAnalysis, err)
	}
	plan.AnalysisPlanSHA256 = digest
	if err := ValidateAnalysisPlan(plan); err != nil {
		return AnalysisPlan{}, err
	}
	return plan, nil
}

func ValidateAnalysisPlan(plan AnalysisPlan) error {
	if err := validateAnalysisPlanShape(plan, true); err != nil {
		return err
	}
	digest, err := digestAnalysisPlan(plan)
	if err != nil || digest != plan.AnalysisPlanSHA256 {
		return contractError(ErrorInvalidAnalysis, err)
	}
	return nil
}

func SealDesign(input Design) (Design, error) {
	design := cloneDesign(input)
	design.Schema = DesignSchema
	design.SchemaVersion = SchemaVersion
	design.ContractVersion = ContractVersion
	for index := range design.Treatments {
		sort.Strings(design.Treatments[index].DistractorSHA256)
	}
	sort.Slice(design.Treatments, func(left, right int) bool {
		return treatmentRequestKey(design.Treatments[left]) < treatmentRequestKey(design.Treatments[right])
	})
	sort.Slice(design.Strata, func(left, right int) bool {
		return design.Strata[left].BindingSHA256 < design.Strata[right].BindingSHA256
	})
	sort.Slice(design.Stopping.SafetyStops, func(left, right int) bool {
		return design.Stopping.SafetyStops[left] < design.Stopping.SafetyStops[right]
	})
	design.DesignSHA256 = ""
	if err := validateDesignShape(design, false); err != nil {
		return Design{}, err
	}
	digest, err := digestDesign(design)
	if err != nil {
		return Design{}, contractError(ErrorInvalidDesign, err)
	}
	design.DesignSHA256 = digest
	if err := ValidateDesign(design); err != nil {
		return Design{}, err
	}
	return design, nil
}

func ValidateDesign(design Design) error {
	if err := validateDesignShape(design, true); err != nil {
		return err
	}
	digest, err := digestDesign(design)
	if err != nil || digest != design.DesignSHA256 {
		return contractError(ErrorInvalidDesign, err)
	}
	return nil
}

func ValidateManifest(manifest Manifest) error {
	if err := validateManifestShape(manifest, true); err != nil {
		return err
	}
	digest, err := digestManifest(manifest)
	if err != nil || digest != manifest.ManifestSHA256 {
		return contractError(ErrorInvalidManifest, err)
	}
	return nil
}

func SealTrialRecord(manifest Manifest, input TrialRecord) (TrialRecord, error) {
	record := cloneTrialRecord(input)
	record.Schema = TrialSchema
	record.SchemaVersion = SchemaVersion
	record.ContractVersion = ContractVersion
	record.ManifestSHA256 = manifest.ManifestSHA256
	sort.Slice(record.Stages, func(left, right int) bool {
		return stageOrdinal(record.Stages[left].Stage) < stageOrdinal(record.Stages[right].Stage)
	})
	sort.Slice(record.Metrics, func(left, right int) bool {
		return record.Metrics[left].Metric < record.Metrics[right].Metric
	})
	record.RecordSHA256 = ""
	if err := validateTrialRecordShape(manifest, record, false); err != nil {
		return TrialRecord{}, err
	}
	digest, err := digestTrialRecord(record)
	if err != nil {
		return TrialRecord{}, contractError(ErrorInvalidTrial, err)
	}
	record.RecordSHA256 = digest
	if err := ValidateTrialRecord(manifest, record); err != nil {
		return TrialRecord{}, err
	}
	return record, nil
}

func ValidateTrialRecord(manifest Manifest, record TrialRecord) error {
	validator, err := NewTrialRecordValidator(manifest)
	if err != nil {
		return err
	}
	return validator.Validate(record)
}

// TrialRecordValidator authenticates one immutable manifest once and then
// validates any number of bound records without repeatedly deriving its pair
// registry. The manifest and assignment index are private clones, so caller
// mutation after admission cannot change subsequent decisions.
type TrialRecordValidator struct {
	manifest    Manifest
	assignments map[string]manifestAssignmentValue
}

func NewTrialRecordValidator(manifest Manifest) (*TrialRecordValidator, error) {
	if err := ValidateManifest(manifest); err != nil {
		return nil, contractError(ErrorInvalidTrial, err)
	}
	admitted := cloneManifest(manifest)
	assignments := make(map[string]manifestAssignmentValue, len(admitted.Blocks)*len(admitted.Treatments))
	for _, block := range admitted.Blocks {
		for _, assignment := range block.Assignments {
			assignments[assignment.TrialID] = manifestAssignmentValue{BlockID: block.ID, TreatmentID: assignment.TreatmentID}
		}
	}
	return &TrialRecordValidator{manifest: admitted, assignments: assignments}, nil
}

func (validator *TrialRecordValidator) Validate(record TrialRecord) error {
	if validator == nil {
		return contractError(ErrorInvalidTrial, errInvalidValue)
	}
	assignment, ok := validator.assignments[record.TrialID]
	if !ok {
		return contractError(ErrorInvalidTrial, errInvalidValue)
	}
	if err := validateTrialRecordShapeForAssignment(validator.manifest, record, true, assignment); err != nil {
		return err
	}
	digest, err := digestTrialRecord(record)
	if err != nil || digest != record.RecordSHA256 {
		return contractError(ErrorInvalidTrial, err)
	}
	return nil
}

func digestCapabilityContract(contract CapabilityContract) (string, error) {
	projection := contract
	projection.CapabilityContractSHA256 = ""
	return digestProjection("capability-contract", projection)
}

func digestAnalysisPlan(plan AnalysisPlan) (string, error) {
	projection := cloneAnalysisPlan(plan)
	projection.AnalysisPlanSHA256 = ""
	return digestProjection("analysis-plan", projection)
}

func digestDesign(design Design) (string, error) {
	projection := cloneDesign(design)
	projection.DesignSHA256 = ""
	return digestProjection("design", projection)
}

func digestManifest(manifest Manifest) (string, error) {
	projection := cloneManifest(manifest)
	projection.ManifestSHA256 = ""
	return digestProjection("manifest", projection)
}

func digestTrialRecord(record TrialRecord) (string, error) {
	projection := cloneTrialRecord(record)
	projection.RecordSHA256 = ""
	return digestProjection("trial-record", projection)
}

func cloneCapabilityContract(input CapabilityContract) CapabilityContract {
	result := input
	result.Capabilities = append([]Capability{}, input.Capabilities...)
	return result
}

func cloneAnalysisPlan(input AnalysisPlan) AnalysisPlan {
	result := input
	result.RepeatedAttempts.K = append([]uint32{}, input.RepeatedAttempts.K...)
	result.Stages = append([]StageDeclaration{}, input.Stages...)
	result.Metrics = append([]MetricDeclaration{}, input.Metrics...)
	result.Comparisons = make([]Comparison, len(input.Comparisons))
	for index := range input.Comparisons {
		result.Comparisons[index] = input.Comparisons[index]
		result.Comparisons[index].Stages = append([]FunnelStage{}, input.Comparisons[index].Stages...)
		result.Comparisons[index].Metrics = append([]MetricID{}, input.Comparisons[index].Metrics...)
	}
	result.AllowedExclusions = append([]ExclusionReason{}, input.AllowedExclusions...)
	return result
}

func cloneDesign(input Design) Design {
	result := input
	result.Treatments = make([]TreatmentRequest, len(input.Treatments))
	for index := range input.Treatments {
		result.Treatments[index] = input.Treatments[index]
		result.Treatments[index].DistractorSHA256 = append([]string{}, input.Treatments[index].DistractorSHA256...)
	}
	result.Strata = append([]StratumRequest{}, input.Strata...)
	result.Ordering.LegacySequence = append([]ArmSelector{}, input.Ordering.LegacySequence...)
	result.Stopping.SafetyStops = append([]SafetyStopCode{}, input.Stopping.SafetyStops...)
	return result
}

func cloneManifest(input Manifest) Manifest {
	result := input
	result.Design = cloneDesign(input.Design)
	result.CapabilityContract = cloneCapabilityContract(input.CapabilityContract)
	result.AnalysisPlan = cloneAnalysisPlan(input.AnalysisPlan)
	result.RequiredCapabilities = append([]CapabilityID{}, input.RequiredCapabilities...)
	result.Treatments = make([]Treatment, len(input.Treatments))
	for index := range input.Treatments {
		result.Treatments[index] = input.Treatments[index]
		result.Treatments[index].DistractorSHA256 = append([]string{}, input.Treatments[index].DistractorSHA256...)
	}
	result.Blocks = make([]Block, len(input.Blocks))
	for index := range input.Blocks {
		result.Blocks[index] = input.Blocks[index]
		result.Blocks[index].Assignments = append([]Assignment{}, input.Blocks[index].Assignments...)
	}
	result.Pairs = append([]PairBinding{}, input.Pairs...)
	return result
}

func cloneTrialRecord(input TrialRecord) TrialRecord {
	result := input
	result.Stages = make([]StageObservation, len(input.Stages))
	for index := range input.Stages {
		result.Stages[index] = input.Stages[index]
		if input.Stages[index].Value != nil {
			value := *input.Stages[index].Value
			result.Stages[index].Value = &value
		}
	}
	result.Metrics = make([]MetricObservation, len(input.Metrics))
	for index := range input.Metrics {
		result.Metrics[index] = input.Metrics[index]
		if input.Metrics[index].Value != nil {
			value := *input.Metrics[index].Value
			result.Metrics[index].Value = &value
		}
	}
	return result
}

func stageOrdinal(stage FunnelStage) int {
	for index, candidate := range closedStages {
		if stage == candidate {
			return index
		}
	}
	return len(closedStages)
}
