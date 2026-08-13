package agenteval

import (
	"fmt"
	"io"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/analysis"
	"github.com/isukharev/atl/internal/agenteval/experiment"
)

// ProjectJUnitResults adapts validated ATL-profile Result and analysis-report
// artifacts to the generic JUnit projection. The canonical artifacts remain
// authoritative; this facade does not recalculate a threshold or statistic.
func ProjectJUnitResults(results []Result, reports []AnalysisReport) (JUnitReport, error) {
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
			Identity:        result.ScenarioID + "/" + result.Variant,
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

// ProjectJUnitAnalysis projects all paired dimension decisions in one
// validated analysis report.
func ProjectJUnitAnalysis(report AnalysisReport) (JUnitReport, error) {
	return ProjectJUnitResults(nil, []AnalysisReport{report})
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
	if !validSHA256(report.ReportSHA256) || len(report.Comparisons) == 0 || report.Coverage.Pairs == nil {
		return nil, fmt.Errorf("missing analysis identity or paired coverage")
	}
	decisions := make([]JUnitPairedDecisionInput, 0)
	for comparisonIndex, comparison := range report.Comparisons {
		complete, excluded, unsupported, err := junitPairCoverageFor(report, comparison.ComparisonID, comparison.StratumID)
		if err != nil {
			return nil, err
		}
		for dimensionIndex, dimension := range comparison.Binary {
			identity := fmt.Sprintf("analysis/%s/%s/%s/binary/%s", report.ReportSHA256, comparison.ComparisonID, comparison.StratumID, dimension.ID)
			decisions = append(decisions, JUnitPairedDecisionInput{
				Identity: identity, InferenceStatus: string(dimension.Status), Regression: dimension.Regression,
				CompletePairs: dimension.CompletePairs, ExcludedPairs: excluded, UnsupportedPairs: unsupported,
			})
			if dimension.CompletePairs != complete {
				return nil, fmt.Errorf("comparison %d binary dimension %d has inconsistent pair coverage", comparisonIndex, dimensionIndex)
			}
		}
		for dimensionIndex, dimension := range comparison.Continuous {
			identity := fmt.Sprintf("analysis/%s/%s/%s/continuous/%s", report.ReportSHA256, comparison.ComparisonID, comparison.StratumID, dimension.Metric)
			decisions = append(decisions, JUnitPairedDecisionInput{
				Identity: identity, InferenceStatus: string(dimension.Status), Regression: dimension.Regression,
				CompletePairs: dimension.CompletePairs, ExcludedPairs: excluded, UnsupportedPairs: unsupported,
			})
			if dimension.CompletePairs != complete {
				return nil, fmt.Errorf("comparison %d continuous dimension %d has inconsistent pair coverage", comparisonIndex, dimensionIndex)
			}
		}
	}
	if len(decisions) == 0 {
		return nil, fmt.Errorf("analysis report has no paired dimensions")
	}
	return decisions, nil
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
			for _, reason := range pair.Reasons {
				if reason == experiment.ExclusionUnsupportedCapability {
					unsupported++
				}
			}
		default:
			return 0, 0, 0, fmt.Errorf("analysis comparison contains unknown pair state")
		}
	}
	return complete, excluded, unsupported, nil
}
