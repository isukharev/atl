package experiment

import (
	"errors"
	"fmt"
	"reflect"
)

var errInvalidValue = errors.New("invalid_value")

func validateCapabilityContractShape(contract CapabilityContract, requireDigest bool) error {
	if contract.Schema != CapabilitySchema || contract.SchemaVersion != SchemaVersion || contract.ContractVersion != ContractVersion ||
		(requireDigest && !validDigest(contract.CapabilityContractSHA256)) || (!requireDigest && contract.CapabilityContractSHA256 != "") {
		return contractError(ErrorInvalidCapability, fmt.Errorf("header: %w", errInvalidValue))
	}
	bindings := []string{
		contract.Runtime.AgentSHA256,
		contract.Runtime.ModelSHA256,
		contract.Runtime.EnvironmentSHA256,
		contract.Runtime.AdapterSHA256,
		contract.Runtime.ExecutionBackendSHA256,
		contract.Runtime.GraderSHA256,
		contract.Runtime.HarnessSHA256,
		contract.Runtime.BudgetsSHA256,
		contract.Runtime.AuthoritySHA256,
	}
	for _, binding := range bindings {
		if !validDigest(binding) {
			return contractError(ErrorInvalidCapability, fmt.Errorf("runtime binding: %w", errInvalidValue))
		}
	}
	if contract.Capabilities == nil || len(contract.Capabilities) != len(closedCapabilities) || len(contract.Capabilities) > MaxCapabilities {
		return contractError(ErrorInvalidCapability, fmt.Errorf("capability count: %w", errInvalidValue))
	}
	for index, capability := range contract.Capabilities {
		if capability.ID != closedCapabilities[index] || !supportValid(capability.Support) {
			return contractError(ErrorInvalidCapability, fmt.Errorf("capability %d %q want %q: %w", index, capability.ID, closedCapabilities[index], errInvalidValue))
		}
		if capability.Support == SupportSupported {
			if !validDigest(capability.BindingSHA256) {
				return contractError(ErrorInvalidCapability, fmt.Errorf("supported binding %d: %w", index, errInvalidValue))
			}
		} else if capability.BindingSHA256 != "" {
			return contractError(ErrorInvalidCapability, fmt.Errorf("unsupported binding %d: %w", index, errInvalidValue))
		}
	}
	return nil
}

func validateAnalysisPlanShape(plan AnalysisPlan, requireDigest bool) error {
	if plan.Schema != AnalysisSchema || plan.SchemaVersion != SchemaVersion || plan.ContractVersion != ContractVersion ||
		(requireDigest && !validDigest(plan.AnalysisPlanSHA256)) || (!requireDigest && plan.AnalysisPlanSHA256 != "") ||
		plan.ConfidenceBasisPoints < 5000 || plan.ConfidenceBasisPoints >= 10000 ||
		plan.MinimumInferenceBlocks < 2 || plan.MinimumInferenceBlocks > MaxBlocks ||
		plan.BootstrapSamples < 100 || plan.BootstrapSamples > MaxBootstrapSamples ||
		!validDigest(plan.BootstrapSeedSHA256) || plan.Multiplicity != MultiplicityHolm {
		return contractError(ErrorInvalidAnalysis, errInvalidValue)
	}
	if err := validateRepeatedAttempts(plan.RepeatedAttempts); err != nil {
		return contractError(ErrorInvalidAnalysis, err)
	}
	if plan.RepeatedAttempts.K == nil || plan.Stages == nil || plan.Metrics == nil || plan.Comparisons == nil || plan.AllowedExclusions == nil ||
		len(plan.Stages) != len(closedStages) || len(plan.Stages) > MaxStages || len(plan.Metrics) == 0 || len(plan.Metrics) > MaxMetrics ||
		len(plan.Comparisons) == 0 || len(plan.Comparisons) > MaxComparisons {
		return contractError(ErrorInvalidAnalysis, errInvalidValue)
	}
	stageSet := make(map[FunnelStage]StageDeclaration, len(plan.Stages))
	primary := 0
	var primaryStage FunnelStage
	var primaryMetric MetricID
	for index, declaration := range plan.Stages {
		capability, ok := capabilityForStage(declaration.Stage)
		if !ok || declaration.Stage != closedStages[index] || declaration.Capability != capability || !validDigest(declaration.FamilySHA256) ||
			(declaration.Role != MetricPrimary && declaration.Role != MetricConfirmatory && declaration.Role != MetricExploratory) {
			return contractError(ErrorInvalidAnalysis, errInvalidValue)
		}
		if declaration.Role == MetricPrimary {
			primary++
			primaryStage = declaration.Stage
		}
		stageSet[declaration.Stage] = declaration
	}
	metricSet := make(map[MetricID]MetricDeclaration, len(plan.Metrics))
	for index, metric := range plan.Metrics {
		expectedCapability, expectedKind, ok := expectedMetricCapability(metric.ID)
		if !ok || metric.Capability != expectedCapability || metric.Kind != expectedKind || !validDigest(metric.FamilySHA256) ||
			(metric.Role != MetricPrimary && metric.Role != MetricConfirmatory && metric.Role != MetricExploratory) ||
			(metric.Direction != DirectionHigher && metric.Direction != DirectionLower) ||
			(index > 0 && plan.Metrics[index-1].ID >= metric.ID) {
			return contractError(ErrorInvalidAnalysis, errInvalidValue)
		}
		if metric.Role == MetricPrimary {
			primary++
			primaryMetric = metric.ID
		}
		metricSet[metric.ID] = metric
	}
	if primary != 1 {
		return contractError(ErrorInvalidAnalysis, errInvalidValue)
	}
	totalDraws := uint64(0)
	seenPairs := map[string]bool{}
	primarySelected := false
	for index, comparison := range plan.Comparisons {
		if comparison.Stages == nil || comparison.Metrics == nil || !validDerivedID("comparison", comparison.ID) || !selectorValid(comparison.Reference) || !selectorValid(comparison.Candidate) ||
			armKey(comparison.Reference) == armKey(comparison.Candidate) || len(comparison.Stages)+len(comparison.Metrics) == 0 ||
			len(comparison.Stages) > MaxStages || len(comparison.Metrics) > MaxMetrics ||
			(index > 0 && plan.Comparisons[index-1].ID >= comparison.ID) {
			return contractError(ErrorInvalidAnalysis, errInvalidValue)
		}
		expectedID, err := comparisonID(Comparison{Reference: comparison.Reference, Candidate: comparison.Candidate, Stages: comparison.Stages, Metrics: comparison.Metrics})
		if err != nil || expectedID != comparison.ID {
			return contractError(ErrorInvalidAnalysis, err)
		}
		unordered := armKey(comparison.Reference) + "\x00" + armKey(comparison.Candidate)
		reverse := armKey(comparison.Candidate) + "\x00" + armKey(comparison.Reference)
		if seenPairs[unordered] || seenPairs[reverse] {
			return contractError(ErrorInvalidAnalysis, errInvalidValue)
		}
		seenPairs[unordered] = true
		for stageIndex, stage := range comparison.Stages {
			if _, ok := stageSet[stage]; !ok || (stageIndex > 0 && stageOrdinal(comparison.Stages[stageIndex-1]) >= stageOrdinal(stage)) {
				return contractError(ErrorInvalidAnalysis, errInvalidValue)
			}
			if stage == primaryStage {
				primarySelected = true
			}
		}
		for metricIndex, metric := range comparison.Metrics {
			if _, ok := metricSet[metric]; !ok || (metricIndex > 0 && comparison.Metrics[metricIndex-1] >= metric) {
				return contractError(ErrorInvalidAnalysis, errInvalidValue)
			}
			if metric == primaryMetric {
				primarySelected = true
			}
		}
		totalDraws += uint64(plan.BootstrapSamples) * uint64(len(comparison.Stages)+len(comparison.Metrics))
		if totalDraws > MaxBootstrapDraws {
			return contractError(ErrorLimitExceeded, errInvalidValue)
		}
	}
	if !primarySelected {
		return contractError(ErrorInvalidAnalysis, errInvalidValue)
	}
	if len(plan.AllowedExclusions) == 0 || len(plan.AllowedExclusions) > len(closedExclusions)-1 {
		return contractError(ErrorInvalidAnalysis, errInvalidValue)
	}
	for index, exclusion := range plan.AllowedExclusions {
		if exclusion == ExclusionNone || !closedExclusions[exclusion] ||
			(index > 0 && plan.AllowedExclusions[index-1] >= exclusion) {
			return contractError(ErrorInvalidAnalysis, errInvalidValue)
		}
	}
	return nil
}

func validateRepeatedAttempts(policy RepeatedAttemptPolicy) error {
	if policy.Kind != RepeatedAttemptsNone && policy.Kind != RepeatedAttemptsAll {
		return errInvalidValue
	}
	if len(policy.K) == 0 || len(policy.K) > MaxPassK {
		return errInvalidValue
	}
	for index, value := range policy.K {
		if value == 0 || value > MaxPassK || (index > 0 && policy.K[index-1] >= value) {
			return errInvalidValue
		}
	}
	if policy.Kind == RepeatedAttemptsNone && (len(policy.K) != 1 || policy.K[0] != 1) {
		return errInvalidValue
	}
	return nil
}

func validateDesignShape(design Design, requireDigest bool) error {
	if design.Schema != DesignSchema || design.SchemaVersion != SchemaVersion || design.ContractVersion != ContractVersion ||
		(requireDigest && !validDigest(design.DesignSHA256)) || (!requireDigest && design.DesignSHA256 != "") ||
		!validDigest(design.CapabilityContractSHA256) || !validDigest(design.AnalysisPlanSHA256) ||
		!validCaseBinding(design.Case) || !validCompatibility(design.CompatibilityProfile) ||
		design.Treatments == nil || design.Strata == nil || design.Ordering.LegacySequence == nil || design.Stopping.SafetyStops == nil ||
		len(design.Treatments) < 2 || len(design.Treatments) > MaxTreatments || len(design.Strata) == 0 || len(design.Strata) > MaxStrata ||
		!validDigest(design.Ordering.SeedSHA256) {
		return contractError(ErrorInvalidDesign, errInvalidValue)
	}
	seenExecution := map[string]bool{}
	seenArms := map[string]bool{}
	seenNegativeControls := map[string]bool{}
	roles := map[TreatmentRole]int{}
	for index, treatment := range design.Treatments {
		if err := validateTreatmentRequest(design, treatment); err != nil ||
			(index > 0 && treatmentRequestKey(design.Treatments[index-1]) >= treatmentRequestKey(treatment)) ||
			seenExecution[treatment.ExecutionBindingSHA256] || seenArms[armKey(treatment.Arm)] {
			return contractError(ErrorInvalidDesign, err)
		}
		seenExecution[treatment.ExecutionBindingSHA256] = true
		seenArms[armKey(treatment.Arm)] = true
		if treatment.Arm.Control != ControlPositive {
			if seenNegativeControls[treatment.ControlSHA256] {
				return contractError(ErrorInvalidDesign, errInvalidValue)
			}
			seenNegativeControls[treatment.ControlSHA256] = true
		}
		roles[treatment.Role]++
	}
	if roles[RoleReference] == 0 || roles[RoleCandidate] == 0 {
		return contractError(ErrorInvalidDesign, errInvalidValue)
	}
	if !validCompatibilityDesign(design) {
		return contractError(ErrorInvalidDesign, errInvalidValue)
	}
	// len is bounded by MaxTreatments before this conversion.
	cycle := uint32(len(design.Treatments)) //nolint:gosec // validated <= MaxTreatments
	if cycle%2 == 1 {
		cycle *= 2
	}
	var totalBlocks uint64
	for index, stratum := range design.Strata {
		if !validDigest(stratum.BindingSHA256) || stratum.Blocks == 0 ||
			(index > 0 && design.Strata[index-1].BindingSHA256 >= stratum.BindingSHA256) {
			return contractError(ErrorInvalidDesign, errInvalidValue)
		}
		if design.Ordering.Kind == OrderingWilliams && stratum.Blocks%cycle != 0 {
			return contractError(ErrorInvalidDesign, errInvalidValue)
		}
		totalBlocks += uint64(stratum.Blocks)
	}
	if totalBlocks == 0 || totalBlocks > MaxBlocks || totalBlocks*uint64(len(design.Treatments)) > MaxTrials ||
		uint64(design.Stopping.MaximumBlocks) != totalBlocks || design.Stopping.MaximumBlocks < designMinimumBlocks(design) {
		return contractError(ErrorLimitExceeded, errInvalidValue)
	}
	if design.Ordering.Kind == OrderingLegacyFixed {
		if design.CompatibilityProfile == CompatibilityNone || !validLegacySequence(design) {
			return contractError(ErrorInvalidDesign, errInvalidValue)
		}
	} else if design.Ordering.Kind != OrderingWilliams || len(design.Ordering.LegacySequence) != 0 {
		return contractError(ErrorInvalidDesign, errInvalidValue)
	}
	if err := validateStopping(design.Stopping); err != nil {
		return contractError(ErrorInvalidDesign, err)
	}
	return nil
}

func validLegacySequence(design Design) bool {
	if len(design.Ordering.LegacySequence) != int(totalDesignBlocks(design))*len(design.Treatments) {
		return false
	}
	width := len(design.Treatments)
	known := make(map[string]bool, width)
	for _, treatment := range design.Treatments {
		known[armKey(treatment.Arm)] = true
	}
	for offset := 0; offset < len(design.Ordering.LegacySequence); offset += width {
		seen := map[string]bool{}
		for _, arm := range design.Ordering.LegacySequence[offset : offset+width] {
			key := armKey(arm)
			if !selectorValid(arm) || !known[key] || seen[key] {
				return false
			}
			seen[key] = true
		}
	}
	return true
}

func designMinimumBlocks(design Design) uint32 {
	if design.CompatibilityProfile == CompatibilityNone {
		return 2
	}
	return 1
}

func validCaseBinding(binding CaseBinding) bool {
	return (binding.SourceKind == SourceNative || binding.SourceKind == SourceAgentSkills) &&
		validDigest(binding.SourceSHA256) && validDigest(binding.CaseSHA256) && validDigest(binding.TaskSHA256) &&
		validDigest(binding.FixtureSHA256) && validDigest(binding.GradingPlanSHA256)
}

func validCompatibility(profile CompatibilityProfile) bool {
	return profile == CompatibilityNone || profile == CompatibilityPrivateActivationV1 || profile == CompatibilityPrivateActivationV2
}

func validCompatibilityDesign(design Design) bool {
	if design.CompatibilityProfile == CompatibilityNone {
		return design.Ordering.Kind == OrderingWilliams
	}
	if design.Ordering.Kind != OrderingLegacyFixed || len(design.Treatments) != 4 || len(design.Strata) != 1 ||
		design.Stopping.Kind != StoppingFixedRoster || len(design.Stopping.SafetyStops) != 0 {
		return false
	}
	want := map[ActivationChannel]TreatmentRole{
		ChannelImplicit: RoleReference, ChannelExplicitUser: RoleCandidate,
		ChannelDeveloper: RoleCandidate, ChannelCombined: RoleCandidate,
	}
	skillSHA256 := ""
	for _, treatment := range design.Treatments {
		role, ok := want[treatment.Arm.ActivationChannel]
		if !ok || treatment.Arm.Condition != ConditionCurrent || treatment.Arm.SelectionAuthority != SelectionAgent ||
			treatment.Arm.Control != ControlPositive || treatment.Role != role || treatment.ControlProvenance != ControlLegacyProjection ||
			treatment.SkillSHA256 == "" || treatment.SkillVersionSHA256 != "" || treatment.RetrieverSHA256 != "" ||
			len(treatment.DistractorSHA256) != 0 {
			return false
		}
		if skillSHA256 == "" {
			skillSHA256 = treatment.SkillSHA256
		} else if skillSHA256 != treatment.SkillSHA256 {
			return false
		}
		delete(want, treatment.Arm.ActivationChannel)
	}
	return len(want) == 0
}

func validateTreatmentRequest(design Design, treatment TreatmentRequest) error {
	if treatment.DistractorSHA256 == nil || !selectorValid(treatment.Arm) || (treatment.Role != RoleReference && treatment.Role != RoleCandidate && treatment.Role != RoleControl) ||
		!validDigest(treatment.ControlSHA256) || !validDigest(treatment.ExecutionBindingSHA256) ||
		!validOptionalDigest(treatment.SkillSHA256) || !validOptionalDigest(treatment.SkillVersionSHA256) ||
		!validOptionalDigest(treatment.RetrieverSHA256) || len(treatment.DistractorSHA256) > MaxDistractors ||
		!sortedUniqueStrings(treatment.DistractorSHA256) {
		return errInvalidValue
	}
	for _, digest := range treatment.DistractorSHA256 {
		if !validDigest(digest) || digest == treatment.SkillSHA256 {
			return errInvalidValue
		}
	}
	if !validTreatmentAuthority(treatment.Arm) || !validTreatmentMaterials(treatment) {
		return errInvalidValue
	}
	if treatment.Arm.Control == ControlStaleVersionMismatch && treatment.Arm.Condition != ConditionPrevious ||
		treatment.Arm.Control == ControlAdversarialDistractor && treatment.Arm.Condition != ConditionOracleDistractors {
		return errInvalidValue
	}
	if treatment.Arm.Control == ControlPositive {
		if treatment.ControlSHA256 != design.Case.CaseSHA256 || treatment.Role == RoleControl ||
			(treatment.ControlProvenance != ControlFromSource &&
				(treatment.ControlProvenance != ControlLegacyProjection || design.CompatibilityProfile == CompatibilityNone)) {
			return errInvalidValue
		}
	} else {
		if treatment.Role != RoleControl || treatment.ControlSHA256 == design.Case.CaseSHA256 {
			return errInvalidValue
		}
		if treatment.ControlProvenance != ControlSeparatelyAuthored &&
			(treatment.ControlProvenance != ControlLegacyProjection || design.CompatibilityProfile == CompatibilityNone) {
			return errInvalidValue
		}
	}
	expected := treatment.Arm.Control == ControlPositive && treatment.Arm.Condition != ConditionNone &&
		treatment.Arm.Condition != ConditionRetrievedAbsent
	if treatment.ExpectedActivation != expected {
		return errInvalidValue
	}
	return nil
}

func validTreatmentAuthority(arm ArmSelector) bool {
	switch arm.Condition {
	case ConditionNone:
		return arm.SelectionAuthority == SelectionNone
	case ConditionForcedOracle:
		return arm.SelectionAuthority == SelectionHarness
	case ConditionAutonomousOracle, ConditionOracleDistractors, ConditionCurrent, ConditionPrevious:
		return arm.SelectionAuthority == SelectionAgent
	case ConditionRetrievedPresent, ConditionRetrievedAbsent:
		return arm.SelectionAuthority == SelectionRetriever
	default:
		return false
	}
}

func validTreatmentMaterials(treatment TreatmentRequest) bool {
	switch treatment.Arm.Condition {
	case ConditionNone:
		return treatment.SkillSHA256 == "" && treatment.SkillVersionSHA256 == "" && treatment.RetrieverSHA256 == "" && len(treatment.DistractorSHA256) == 0
	case ConditionPrevious:
		return treatment.SkillSHA256 != "" && treatment.SkillVersionSHA256 != "" && treatment.RetrieverSHA256 == "" && len(treatment.DistractorSHA256) == 0
	case ConditionOracleDistractors:
		return treatment.SkillSHA256 != "" && treatment.RetrieverSHA256 == "" && len(treatment.DistractorSHA256) > 0
	case ConditionRetrievedPresent, ConditionRetrievedAbsent:
		return treatment.SkillSHA256 != "" && treatment.RetrieverSHA256 != "" && len(treatment.DistractorSHA256) == 0
	case ConditionCurrent, ConditionForcedOracle, ConditionAutonomousOracle:
		return treatment.SkillSHA256 != "" && treatment.RetrieverSHA256 == "" && len(treatment.DistractorSHA256) == 0
	default:
		return false
	}
}

func validateStopping(stopping StoppingRule) error {
	if stopping.Kind != StoppingFixedRoster && stopping.Kind != StoppingSafetyOrFixedRoster || stopping.MaximumBlocks == 0 ||
		len(stopping.SafetyStops) > MaxSafetyStopCodes {
		return errInvalidValue
	}
	if stopping.Kind == StoppingFixedRoster && len(stopping.SafetyStops) != 0 ||
		stopping.Kind == StoppingSafetyOrFixedRoster && len(stopping.SafetyStops) == 0 {
		return errInvalidValue
	}
	for index, code := range stopping.SafetyStops {
		if (code != SafetyStopCriticalFinding && code != SafetyStopAuthorityViolation && code != SafetyStopBudgetExhausted) ||
			(index > 0 && stopping.SafetyStops[index-1] >= code) {
			return errInvalidValue
		}
	}
	return nil
}

func validateManifestShape(manifest Manifest, requireDigest bool) error {
	if manifest.Schema != ManifestSchema || manifest.SchemaVersion != SchemaVersion || manifest.ContractVersion != ContractVersion ||
		(requireDigest && !validDigest(manifest.ManifestSHA256)) || (!requireDigest && manifest.ManifestSHA256 != "") ||
		manifest.RequiredCapabilities == nil || manifest.Treatments == nil || manifest.Blocks == nil || manifest.Pairs == nil ||
		ValidateDesign(manifest.Design) != nil ||
		ValidateCapabilityContract(manifest.CapabilityContract) != nil || ValidateAnalysisPlan(manifest.AnalysisPlan) != nil ||
		manifest.Design.CapabilityContractSHA256 != manifest.CapabilityContract.CapabilityContractSHA256 ||
		manifest.Design.AnalysisPlanSHA256 != manifest.AnalysisPlan.AnalysisPlanSHA256 {
		return contractError(ErrorInvalidManifest, errInvalidValue)
	}
	if err := validateCompilationAdmission(manifest.Design, manifest.CapabilityContract, manifest.AnalysisPlan); err != nil {
		return contractError(ErrorInvalidManifest, err)
	}
	expected, err := deriveManifest(manifest.Design, manifest.CapabilityContract, manifest.AnalysisPlan)
	if err != nil {
		return contractError(ErrorInvalidManifest, err)
	}
	expected.ManifestSHA256 = manifest.ManifestSHA256
	if !reflect.DeepEqual(expected, manifest) {
		return contractError(ErrorInvalidManifest, errInvalidValue)
	}
	return nil
}

func validateTrialRecordShape(manifest Manifest, record TrialRecord, requireDigest bool) error {
	assignment, ok := manifestAssignment(manifest, record.TrialID)
	if !ok {
		return contractError(ErrorInvalidTrial, errInvalidValue)
	}
	return validateTrialRecordShapeForAssignment(manifest, record, requireDigest, assignment)
}

func validateTrialRecordShapeForAssignment(manifest Manifest, record TrialRecord, requireDigest bool, assignment manifestAssignmentValue) error {
	if record.Schema != TrialSchema || record.SchemaVersion != SchemaVersion || record.ContractVersion != ContractVersion ||
		record.ManifestSHA256 != manifest.ManifestSHA256 || !validDigest(record.AttemptPlanSHA256) ||
		record.Stages == nil || record.Metrics == nil ||
		(requireDigest && !validDigest(record.RecordSHA256)) || (!requireDigest && record.RecordSHA256 != "") ||
		!validOptionalDigest(record.AgentObservationSHA256) || !validOptionalDigest(record.GradeReceiptSHA256) ||
		!validDigest(record.LifecycleEventSHA256) {
		return contractError(ErrorInvalidTrial, errInvalidValue)
	}
	if assignment.BlockID != record.BlockID || assignment.TreatmentID != record.TreatmentID {
		return contractError(ErrorInvalidTrial, errInvalidValue)
	}
	if err := validateTrialStatus(record); err != nil {
		return contractError(ErrorInvalidTrial, err)
	}
	if record.Exclusion != ExclusionNone && !exclusionAllowed(manifest.AnalysisPlan, record.Exclusion) {
		return contractError(ErrorInvalidTrial, errInvalidValue)
	}
	if len(record.Stages) != len(closedStages) || len(record.Metrics) != len(manifest.AnalysisPlan.Metrics) {
		return contractError(ErrorInvalidTrial, errInvalidValue)
	}
	for index, stage := range record.Stages {
		if stage.Stage != closedStages[index] || !presenceValid(stage.Presence) ||
			(stage.Presence == PresenceObserved) != (stage.Value != nil) {
			return contractError(ErrorInvalidTrial, errInvalidValue)
		}
	}
	for index, metric := range record.Metrics {
		declaration := manifest.AnalysisPlan.Metrics[index]
		if metric.Metric != declaration.ID || !presenceValid(metric.Presence) ||
			(metric.Presence == PresenceObserved) != (metric.Value != nil) {
			return contractError(ErrorInvalidTrial, errInvalidValue)
		}
		if metric.Value != nil && (*metric.Value > MaxMetricValue || declaration.Kind == MetricBinary && *metric.Value > 1) {
			return contractError(ErrorInvalidTrial, errInvalidValue)
		}
	}
	return nil
}

func exclusionAllowed(plan AnalysisPlan, reason ExclusionReason) bool {
	for _, allowed := range plan.AllowedExclusions {
		if allowed == reason {
			return true
		}
	}
	return false
}

func validateTrialStatus(record TrialRecord) error {
	terminal := record.LifecycleState == LifecycleSucceeded || record.LifecycleState == LifecycleFailed ||
		record.LifecycleState == LifecycleCanceled || record.LifecycleState == LifecycleTimedOut ||
		record.LifecycleState == LifecycleUnknown || record.LifecycleState == LifecycleUnsupported ||
		record.LifecycleState == LifecyclePolicyDenied
	if !terminal {
		return errInvalidValue
	}
	switch record.Eligibility {
	case EligibilitySupported:
		if (record.LifecycleState != LifecycleSucceeded && record.LifecycleState != LifecycleFailed) || record.Exclusion != ExclusionNone ||
			!validDigest(record.AgentObservationSHA256) || !validDigest(record.GradeReceiptSHA256) {
			return errInvalidValue
		}
	case EligibilityUnsupported:
		if record.LifecycleState != LifecycleUnsupported || record.Exclusion != ExclusionUnsupportedCapability ||
			record.AgentObservationSHA256 != "" || record.GradeReceiptSHA256 != "" {
			return errInvalidValue
		}
	case EligibilityIneligible:
		if record.Exclusion == ExclusionNone || record.Exclusion == ExclusionDrift || !closedExclusions[record.Exclusion] ||
			record.GradeReceiptSHA256 != "" {
			return errInvalidValue
		}
	case EligibilityDrifted:
		if record.Exclusion != ExclusionDrift || record.GradeReceiptSHA256 != "" {
			return errInvalidValue
		}
	default:
		return errInvalidValue
	}
	return nil
}

type manifestAssignmentValue struct {
	BlockID     string
	TreatmentID string
}

func manifestAssignment(manifest Manifest, trialID string) (manifestAssignmentValue, bool) {
	for _, block := range manifest.Blocks {
		for _, assignment := range block.Assignments {
			if assignment.TrialID == trialID {
				return manifestAssignmentValue{BlockID: block.ID, TreatmentID: assignment.TreatmentID}, true
			}
		}
	}
	return manifestAssignmentValue{}, false
}
