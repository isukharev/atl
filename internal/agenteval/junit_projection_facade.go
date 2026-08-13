package agenteval

import (
	"fmt"
	"io"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/analysis"
	"github.com/isukharev/atl/internal/agenteval/experiment"
)

// ProjectJUnitResults adapts validated ATL-profile Result artifacts to the
// generic JUnit projection. Analysis reports require the manifest-bound
// ProjectJUnitResultsWithManifests entry point; this compatibility form fails
// closed when reports are supplied rather than silently projecting unchecked
// analysis data.
//
// Each AnalysisReport must have already passed DecodeAnalysisReport (or
// analysis.ValidateReportForManifest) with its owning manifest. AnalysisReport
// deliberately carries no manifest, so this facade can enforce only the
// self-contained schema, digest-shape, and paired-decision preconditions below;
// it cannot prove the report-to-manifest binding itself.
func ProjectJUnitResults(results []Result, reports []AnalysisReport) (JUnitReport, error) {
	if len(reports) != 0 {
		return JUnitReport{}, fmt.Errorf("%w: analysis manifest required", ErrInvalidJUnitInput)
	}
	return projectJUnitResults(results, nil)
}

// ProjectJUnitResultsWithManifests adapts validated Result artifacts and
// manifest-bound AnalysisReport artifacts. Every report is validated against
// its corresponding manifest before any decision can enter the projection.
// The canonical artifacts remain authoritative; this facade does not
// recalculate a threshold or statistic.
func ProjectJUnitResultsWithManifests(results []Result, reports []AnalysisReport, manifests []ExperimentManifest) (JUnitReport, error) {
	if len(reports) == 0 || len(reports) != len(manifests) {
		return JUnitReport{}, fmt.Errorf("%w: analysis reports and manifests must be paired", ErrInvalidJUnitInput)
	}
	for index := range reports {
		if err := analysis.ValidateReportForManifest(manifests[index], reports[index]); err != nil {
			return JUnitReport{}, fmt.Errorf("analysis report %d is not manifest-bound: %w", index, ErrInvalidJUnitInput)
		}
	}
	return projectJUnitResults(results, reports)
}

func projectJUnitResults(results []Result, reports []AnalysisReport) (JUnitReport, error) {
	input := JUnitProjectionInput{
		Results:   make([]JUnitResultInput, 0, len(results)),
		Decisions: make([]JUnitPairedDecisionInput, 0),
	}
	for index, result := range results {
		if err := result.Validate(); err != nil {
			return JUnitReport{}, fmt.Errorf("result %d is not canonical: %w", index, ErrInvalidJUnitInput)
		}
		violations := make([]JUnitViolationInput, 0, len(result.Violations))
		for _, violation := range result.Violations {
			violations = append(violations, JUnitViolationInput{Code: violation.Code, Subject: violation.Subject})
		}
		evidenceCovered, evidenceState := result.EvidenceAttempt.Coverage, string(result.EvidenceAttempt.State)
		if result.EvidenceReport.Coverage {
			evidenceCovered = true
			if evidenceState == "" || evidenceState == string(EvidenceAttemptStateNone) {
				evidenceState = string(result.EvidenceReport.State)
			}
		}
		input.Results = append(input.Results, JUnitResultInput{
			Identity:        boundedJUnitIdentity('r', result.ScenarioID+"/"+result.Variant),
			SchemaVersion:   result.SchemaVersion,
			Status:          result.Status,
			Eligibility:     result.EffectiveEligibility(),
			Violations:      violations,
			EvidenceCovered: evidenceCovered,
			EvidenceState:   evidenceState,
		})
	}
	for index, report := range reports {
		decisions, err := junitDecisionsFromAnalysis(report)
		if err != nil {
			return JUnitReport{}, fmt.Errorf("analysis report %d is not canonical: %w", index, ErrInvalidJUnitInput)
		}
		input.Decisions = append(input.Decisions, decisions...)
	}
	return ProjectJUnit(input)
}

// ProjectJUnitResult is the one-artifact convenience form.
func ProjectJUnitResult(result Result) (JUnitReport, error) {
	return ProjectJUnitResults([]Result{result}, nil)
}

// ProjectJUnitAnalysis projects all paired dimension decisions in one report
// after proving its binding to the supplied manifest.
func ProjectJUnitAnalysis(report AnalysisReport, manifest ExperimentManifest) (JUnitReport, error) {
	return ProjectJUnitResultsWithManifests(nil, []AnalysisReport{report}, []ExperimentManifest{manifest})
}

// EncodeJUnitProjection is an explicit name for callers that prefer the
// projection terminology; EncodeJUnit remains the canonical short form.
func EncodeJUnitProjection(report JUnitReport) ([]byte, error) {
	return EncodeJUnit(report)
}

// DecodeJUnitProjection is the matching explicit decoder name.
func DecodeJUnitProjection(reader io.Reader) (JUnitReport, error) {
	return DecodeJUnit(reader)
}

func junitDecisionsFromAnalysis(report AnalysisReport) ([]JUnitPairedDecisionInput, error) {
	if err := validateJUnitAnalysisPrecondition(report); err != nil {
		return nil, err
	}
	decisions := make([]JUnitPairedDecisionInput, 0)
	for _, comparison := range report.Comparisons {
		complete, excluded, unsupported, err := junitPairCoverageFor(report, comparison.ComparisonID, comparison.StratumID)
		if err != nil {
			return nil, ErrInvalidJUnitInput
		}
		if comparison.CompletePairs != complete {
			return nil, ErrInvalidJUnitInput
		}
		for _, dimension := range comparison.Binary {
			identity := boundedJUnitIdentity('a', fmt.Sprintf("analysis/%s/%s/%s/binary/%s", report.ReportSHA256, comparison.ComparisonID, comparison.StratumID, dimension.ID))
			decisions = append(decisions, JUnitPairedDecisionInput{
				Identity: identity, InferenceStatus: string(dimension.Status), Regression: dimension.Regression,
				CompletePairs: dimension.CompletePairs, ExcludedPairs: excluded, UnsupportedPairs: unsupported,
			})
			if dimension.CompletePairs != complete {
				return nil, ErrInvalidJUnitInput
			}
		}
		for _, dimension := range comparison.Continuous {
			identity := boundedJUnitIdentity('a', fmt.Sprintf("analysis/%s/%s/%s/continuous/%s", report.ReportSHA256, comparison.ComparisonID, comparison.StratumID, dimension.Metric))
			decisions = append(decisions, JUnitPairedDecisionInput{
				Identity: identity, InferenceStatus: string(dimension.Status), Regression: dimension.Regression,
				CompletePairs: dimension.CompletePairs, ExcludedPairs: excluded, UnsupportedPairs: unsupported,
			})
			if dimension.CompletePairs != complete {
				return nil, ErrInvalidJUnitInput
			}
		}
	}
	if len(decisions) == 0 || len(decisions) > JUnitMaxTestCases {
		return nil, ErrInvalidJUnitInput
	}
	return decisions, nil
}

// validateJUnitAnalysisPrecondition is intentionally narrower than the
// manifest-bound analysis validator. It rejects malformed or future-shaped
// reports before any report fields become projection identities, while
// leaving manifest vocabulary, trial membership, digest binding, interval
// replay, and statistical semantics to analysis.ValidateReportForManifest.
func validateJUnitAnalysisPrecondition(report AnalysisReport) error {
	if report.Schema != AnalysisReportSchema || report.SchemaVersion != AnalysisReportSchemaVersion ||
		report.ContractVersion != AnalysisReportContractVersion ||
		!validSHA256(report.ManifestSHA256) || !validSHA256(report.AnalysisPlanSHA256) ||
		!validSHA256(report.InputSetSHA256) || !validSHA256(report.ReportSHA256) ||
		report.Coverage.ExpectedRecords == 0 || report.Coverage.ExpectedRecords > experiment.MaxTrials ||
		report.Coverage.Members == nil || len(report.Coverage.Members) != int(report.Coverage.ExpectedRecords) ||
		report.Coverage.Pairs == nil || len(report.Coverage.Pairs) == 0 || len(report.Coverage.Pairs) > experiment.MaxPairBindings ||
		report.Coverage.Reasons == nil || report.Comparisons == nil || len(report.Comparisons) == 0 ||
		len(report.Comparisons) > analysis.MaxStratifiedResults || report.Activation == nil || report.Funnels == nil || report.PassAtK == nil {
		return ErrInvalidJUnitInput
	}
	if uint64(report.Coverage.CompletePairs)+uint64(report.Coverage.ExcludedPairs) != uint64(len(report.Coverage.Pairs)) {
		return ErrInvalidJUnitInput
	}
	pairIDs := make(map[string]struct{}, len(report.Coverage.Pairs))
	completePairs := make(map[string]uint32, len(report.Comparisons))
	for _, pair := range report.Coverage.Pairs {
		if !validJUnitInputIdentity(pair.PairID) || !validJUnitInputIdentity(pair.BlockID) ||
			!validJUnitInputIdentity(pair.StratumID) || !validJUnitInputIdentity(pair.ComparisonID) {
			return ErrInvalidJUnitInput
		}
		if _, exists := pairIDs[pair.PairID]; exists {
			return ErrInvalidJUnitInput
		}
		pairIDs[pair.PairID] = struct{}{}
		key := pair.ComparisonID + "\x00" + pair.StratumID
		switch pair.Status {
		case analysis.PairComplete:
			if len(pair.Reasons) != 0 {
				return ErrInvalidJUnitInput
			}
			completePairs[key]++
		case analysis.PairExcluded, analysis.PairMissing, analysis.PairDuplicate:
			if len(pair.Reasons) == 0 || len(pair.Reasons) > 2 {
				return ErrInvalidJUnitInput
			}
		default:
			return ErrInvalidJUnitInput
		}
		for _, reason := range pair.Reasons {
			switch reason {
			case experiment.ExclusionMissingMember, experiment.ExclusionDuplicateMember,
				experiment.ExclusionLifecycleIncomplete, experiment.ExclusionLifecycleUnknown,
				experiment.ExclusionUnsupportedCapability, experiment.ExclusionIneligible,
				experiment.ExclusionDrift, experiment.ExclusionGradeIncomplete,
				experiment.ExclusionCoverageMismatch:
			default:
				return ErrInvalidJUnitInput
			}
		}
	}
	comparisonIDs := make(map[string]struct{}, len(report.Comparisons))
	dimensionCount := 0
	for _, comparison := range report.Comparisons {
		if !validJUnitInputIdentity(comparison.ComparisonID) || !validJUnitInputIdentity(comparison.StratumID) ||
			!validJUnitInputIdentity(comparison.ReferenceTreatmentID) || !validJUnitInputIdentity(comparison.CandidateTreatmentID) ||
			comparison.ReferenceTreatmentID == comparison.CandidateTreatmentID || comparison.Binary == nil || comparison.Continuous == nil ||
			len(comparison.Binary)+len(comparison.Continuous) == 0 {
			return ErrInvalidJUnitInput
		}
		comparisonKey := comparison.ComparisonID + "\x00" + comparison.StratumID
		if _, exists := comparisonIDs[comparisonKey]; exists {
			return ErrInvalidJUnitInput
		}
		comparisonIDs[comparisonKey] = struct{}{}
		if _, exists := completePairs[comparisonKey]; !exists || comparison.CompletePairs != completePairs[comparisonKey] {
			return ErrInvalidJUnitInput
		}
		dimensionCount += len(comparison.Binary) + len(comparison.Continuous)
		if dimensionCount > JUnitMaxTestCases || dimensionCount > analysis.MaxDimensionResults {
			return ErrInvalidJUnitInput
		}
		for _, dimension := range comparison.Binary {
			if !validJUnitInputIdentity(dimension.ID) ||
				(dimension.Kind != analysis.DimensionStage && dimension.Kind != analysis.DimensionMetric) ||
				!validJUnitInferenceStatus(dimension.Status) || dimension.CompletePairs != comparison.CompletePairs {
				return ErrInvalidJUnitInput
			}
		}
		for _, dimension := range comparison.Continuous {
			if !validJUnitInputIdentity(string(dimension.Metric)) || !validJUnitInferenceStatus(dimension.Status) ||
				dimension.CompletePairs != comparison.CompletePairs {
				return ErrInvalidJUnitInput
			}
		}
	}
	return nil
}

func validJUnitInferenceStatus(status analysis.InferenceStatus) bool {
	switch status {
	case analysis.InferenceInsufficient, analysis.InferenceDescriptive, analysis.InferenceInferential:
		return true
	default:
		return false
	}
}

func junitPairCoverageFor(report AnalysisReport, comparisonID, stratumID string) (uint32, uint32, uint32, error) {
	pairs := make([]analysis.PairCoverage, 0)
	for _, pair := range report.Coverage.Pairs {
		if pair.ComparisonID == comparisonID && pair.StratumID == stratumID {
			pairs = append(pairs, pair)
		}
	}
	if len(pairs) == 0 {
		return 0, 0, 0, fmt.Errorf("analysis comparison has no paired coverage")
	}
	sort.Slice(pairs, func(left, right int) bool { return pairs[left].PairID < pairs[right].PairID })
	var complete, excluded, unsupported uint32
	for _, pair := range pairs {
		switch pair.Status {
		case analysis.PairComplete:
			complete++
		case analysis.PairExcluded, analysis.PairMissing, analysis.PairDuplicate:
			excluded++
			allUnsupported := len(pair.Reasons) > 0
			for _, reason := range pair.Reasons {
				if reason != experiment.ExclusionUnsupportedCapability {
					allUnsupported = false
				}
			}
			if allUnsupported {
				unsupported++
			}
		default:
			return 0, 0, 0, fmt.Errorf("analysis comparison contains unknown pair state")
		}
	}
	return complete, excluded, unsupported, nil
}
