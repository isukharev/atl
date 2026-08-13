package analysis

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

var errInvalidValue = errors.New("invalid_value")

var closedStages = []experiment.FunnelStage{
	experiment.StageCandidateRecall,
	experiment.StageSelection,
	experiment.StageLoad,
	experiment.StageInstructionAccess,
	experiment.StageReferenceAccess,
	experiment.StageScriptAccess,
	experiment.StageUsefulAdherence,
	experiment.StageVerifierOutcome,
}

var closedReasons = map[experiment.ExclusionReason]bool{
	experiment.ExclusionMissingMember:         true,
	experiment.ExclusionDuplicateMember:       true,
	experiment.ExclusionLifecycleIncomplete:   true,
	experiment.ExclusionLifecycleUnknown:      true,
	experiment.ExclusionUnsupportedCapability: true,
	experiment.ExclusionIneligible:            true,
	experiment.ExclusionDrift:                 true,
	experiment.ExclusionGradeIncomplete:       true,
	experiment.ExclusionCoverageMismatch:      true,
}

func validateReportShapeContext(ctx context.Context, report Report) error {
	if err := analysisContextError(ctx); err != nil {
		return err
	}
	if report.Schema != ReportSchema || report.SchemaVersion != SchemaVersion || report.ContractVersion != ContractVersion ||
		!validDigest(report.ManifestSHA256) || !validDigest(report.AnalysisPlanSHA256) || !validDigest(report.InputSetSHA256) ||
		!validDigest(report.ReportSHA256) ||
		report.ConfidenceBasisPoints < 5000 || report.ConfidenceBasisPoints >= 10000 ||
		report.MinimumInferenceBlocks < 2 || report.MinimumInferenceBlocks > experiment.MaxBlocks ||
		report.BootstrapSamples < 100 || report.BootstrapSamples > experiment.MaxBootstrapSamples ||
		report.Multiplicity != experiment.MultiplicityHolm || report.Comparisons == nil || report.Activation == nil || report.Funnels == nil || report.PassAtK == nil {
		return contractError(ErrorInvalidReport, errInvalidValue)
	}
	if err := validateCoverage(report.Coverage); err != nil {
		return contractError(ErrorInvalidReport, err)
	}
	comparisonIDs := map[string]bool{}
	completeByComparison := map[analysisKey]uint32{}
	completePairIDs := map[analysisKey][]string{}
	pairComparisonIDs := map[analysisKey]bool{}
	for _, pair := range report.Coverage.Pairs {
		if err := analysisContextError(ctx); err != nil {
			return err
		}
		key := analysisKey{comparisonID: pair.ComparisonID, stratumID: pair.StratumID}
		pairComparisonIDs[key] = true
		if pair.Status == PairComplete {
			completeByComparison[key]++
			completePairIDs[key] = append(completePairIDs[key], pair.PairID)
		}
	}
	if len(report.Comparisons) == 0 || len(report.Comparisons) > MaxStratifiedResults {
		return contractError(ErrorInvalidReport, errInvalidValue)
	}
	dimensionCount := uint64(0)
	deltaCount := uint64(0)
	primitiveDraws := uint64(0)
	previousComparison := ""
	for index := range report.Comparisons {
		if err := analysisContextError(ctx); err != nil {
			return err
		}
		comparison := report.Comparisons[index]
		if comparison.Binary == nil || comparison.Continuous == nil || len(comparison.Binary)+len(comparison.Continuous) == 0 ||
			len(comparison.Binary) > experiment.MaxStages+experiment.MaxMetrics || len(comparison.Continuous) > experiment.MaxMetrics {
			return contractError(ErrorInvalidReport, errInvalidValue)
		}
		dimensions := uint64(len(comparison.Binary) + len(comparison.Continuous))
		if dimensions > MaxDimensionResults-dimensionCount {
			return contractError(ErrorInvalidReport, errInvalidValue)
		}
		dimensionCount += dimensions
		for _, continuous := range comparison.Continuous {
			if uint64(len(continuous.Deltas)) > MaxPairedDeltas-deltaCount {
				return contractError(ErrorInvalidReport, errInvalidValue)
			}
			deltaCount += uint64(len(continuous.Deltas))
			if continuous.Status == InferenceInferential {
				addition := uint64(len(continuous.Deltas)) * uint64(report.BootstrapSamples)
				if addition > MaxPrimitiveDraws-primitiveDraws {
					return contractError(ErrorInvalidReport, errInvalidValue)
				}
				primitiveDraws += addition
			}
		}
		for _, binary := range comparison.Binary {
			if uint64(len(binary.Pairs)) > MaxPairedDeltas-deltaCount {
				return contractError(ErrorInvalidReport, errInvalidValue)
			}
			deltaCount += uint64(len(binary.Pairs))
			if binary.Status == InferenceInferential {
				addition := uint64(len(binary.Pairs)) * uint64(report.BootstrapSamples)
				if addition > MaxPrimitiveDraws-primitiveDraws {
					return contractError(ErrorInvalidReport, errInvalidValue)
				}
				primitiveDraws += addition
			}
		}
		key := analysisKey{comparisonID: comparison.ComparisonID, stratumID: comparison.StratumID}
		orderKey := comparison.ComparisonID + "\x00" + comparison.StratumID
		if !validDerivedID("comparison", comparison.ComparisonID) || !validDerivedID("treatment", comparison.ReferenceTreatmentID) ||
			!validDerivedID("stratum", comparison.StratumID) || !validDerivedID("treatment", comparison.CandidateTreatmentID) ||
			comparison.ReferenceTreatmentID == comparison.CandidateTreatmentID || !pairComparisonIDs[key] || comparisonIDs[orderKey] ||
			(index > 0 && previousComparison >= orderKey) || comparison.CompletePairs != completeByComparison[key] {
			return contractError(ErrorInvalidReport, errInvalidValue)
		}
		comparisonIDs[orderKey], previousComparison = true, orderKey
		seenDimensions := map[string]bool{}
		for dimensionIndex := range comparison.Binary {
			dimension := comparison.Binary[dimensionIndex]
			key := string(dimension.Kind) + "\x00" + dimension.ID
			if seenDimensions[key] {
				return contractError(ErrorInvalidReport, errInvalidValue)
			}
			if err := validateBinaryContext(ctx, report, dimension, completePairIDs[analysisKey{comparisonID: comparison.ComparisonID, stratumID: comparison.StratumID}]); err != nil {
				if contextErr := analysisContextError(ctx); contextErr != nil {
					return contextErr
				}
				return contractError(ErrorInvalidReport, errInvalidValue)
			}
			seenDimensions[key] = true
		}
		for dimensionIndex := range comparison.Continuous {
			dimension := comparison.Continuous[dimensionIndex]
			key := string(DimensionMetric) + "\x00" + string(dimension.Metric)
			if seenDimensions[key] {
				return contractError(ErrorInvalidReport, errInvalidValue)
			}
			if err := validateContinuousContext(ctx, report, dimension, completePairIDs[analysisKey{comparisonID: comparison.ComparisonID, stratumID: comparison.StratumID}]); err != nil {
				if contextErr := analysisContextError(ctx); contextErr != nil {
					return contextErr
				}
				return contractError(ErrorInvalidReport, errInvalidValue)
			}
			seenDimensions[key] = true
		}
		if comparison.Pareto != comparisonPareto(comparison) {
			return contractError(ErrorInvalidReport, errInvalidValue)
		}
	}
	for key := range pairComparisonIDs {
		if !comparisonIDs[key.comparisonID+"\x00"+key.stratumID] {
			return contractError(ErrorInvalidReport, errInvalidValue)
		}
	}
	planned, err := validateFunnels(report.Funnels, report.Coverage.ExpectedRecords)
	if err != nil {
		return contractError(ErrorInvalidReport, err)
	}
	if err := validateActivation(report.Activation, planned); err != nil {
		return contractError(ErrorInvalidReport, err)
	}
	if len(report.PassAtK) > MaxPassAtKResults {
		return contractError(ErrorInvalidReport, errInvalidValue)
	}
	if err := validatePassAtK(report.PassAtK, planned, report.MinimumInferenceBlocks); err != nil {
		return contractError(ErrorInvalidReport, err)
	}
	expectedHolm := cloneReport(report)
	if err := applyHolmContext(ctx, &expectedHolm, report.ConfidenceBasisPoints); err != nil {
		return err
	}
	if !reflect.DeepEqual(expectedHolm, report) {
		return contractError(ErrorInvalidReport, errInvalidValue)
	}
	if report.ReportSHA256 != reportDigest(report) {
		return contractError(ErrorInvalidReport, errInvalidValue)
	}
	return nil
}

func validateCoverage(coverage Coverage) error {
	if coverage.ExpectedRecords == 0 || coverage.ExpectedRecords > experiment.MaxTrials || coverage.ReceivedRecords > MaxInputRecords ||
		coverage.UniqueRecords > coverage.ExpectedRecords || uint64(coverage.UniqueRecords)+uint64(coverage.DuplicateRecords) != uint64(coverage.ReceivedRecords) ||
		uint64(coverage.UniqueRecords)+uint64(coverage.MissingRecords) != uint64(coverage.ExpectedRecords) || coverage.Pairs == nil || coverage.Reasons == nil ||
		coverage.Members == nil || len(coverage.Members) != int(coverage.ExpectedRecords) ||
		len(coverage.Pairs) == 0 || len(coverage.Pairs) > experiment.MaxPairBindings ||
		// Pair length was capped at experiment.MaxPairBindings above.
		uint64(coverage.CompletePairs)+uint64(coverage.ExcludedPairs) != uint64(len(coverage.Pairs)) {
		return errInvalidValue
	}
	received, unique, missing, duplicates := uint64(0), uint64(0), uint64(0), uint64(0)
	projectionCount := uint64(0)
	previousTrial := ""
	for index, member := range coverage.Members {
		if !validDerivedID("trial", member.TrialID) || member.Records > MaxInputRecords ||
			(index > 0 && previousTrial >= member.TrialID) ||
			member.Stages == nil || member.Metrics == nil || len(member.Stages) > experiment.MaxStages || len(member.Metrics) > experiment.MaxMetrics ||
			(member.Records != 1 && member.Exclusion != experiment.ExclusionNone) ||
			(member.Records == 1 && member.Exclusion != experiment.ExclusionNone && !closedReasons[member.Exclusion]) {
			return errInvalidValue
		}
		addition := uint64(len(member.Stages) + len(member.Metrics))
		if addition > MaxTrialProjections-projectionCount {
			return errInvalidValue
		}
		projectionCount += addition
		if member.Records != 1 || member.Exclusion != experiment.ExclusionNone {
			if len(member.Stages) != 0 || len(member.Metrics) != 0 {
				return errInvalidValue
			}
		} else if !validTrialProjections(member) {
			return errInvalidValue
		}
		previousTrial = member.TrialID
		received += uint64(member.Records)
		switch member.Records {
		case 0:
			missing++
		case 1:
			unique++
		default:
			unique++
			duplicates += uint64(member.Records - 1)
		}
	}
	if received != uint64(coverage.ReceivedRecords) || unique != uint64(coverage.UniqueRecords) ||
		missing != uint64(coverage.MissingRecords) || duplicates != uint64(coverage.DuplicateRecords) {
		return errInvalidValue
	}
	pairIDs := map[string]bool{}
	reasonCounts := map[experiment.ExclusionReason]uint32{}
	complete, excluded := uint32(0), uint32(0)
	previousPair := ""
	for _, pair := range coverage.Pairs {
		if !validDerivedID("pair", pair.PairID) || !validDerivedID("block", pair.BlockID) || !validDerivedID("stratum", pair.StratumID) ||
			!validDerivedID("comparison", pair.ComparisonID) ||
			pairIDs[pair.PairID] || (previousPair != "" && previousPair >= pair.PairID) || pair.Reasons == nil || len(pair.Reasons) > 2 || !validPairStatus(pair.Status) {
			return errInvalidValue
		}
		pairIDs[pair.PairID], previousPair = true, pair.PairID
		for index, reason := range pair.Reasons {
			if !closedReasons[reason] || (index > 0 && pair.Reasons[index-1] >= reason) {
				return errInvalidValue
			}
			reasonCounts[reason]++
		}
		switch pair.Status {
		case PairComplete:
			if len(pair.Reasons) != 0 {
				return errInvalidValue
			}
			complete++
		case PairMissing:
			if !containsReason(pair.Reasons, experiment.ExclusionMissingMember) || containsReason(pair.Reasons, experiment.ExclusionDuplicateMember) {
				return errInvalidValue
			}
			excluded++
		case PairDuplicate:
			if !containsReason(pair.Reasons, experiment.ExclusionDuplicateMember) {
				return errInvalidValue
			}
			excluded++
		case PairExcluded:
			if len(pair.Reasons) == 0 || len(pair.Reasons) > 2 {
				return errInvalidValue
			}
			excluded++
		}
	}
	if complete != coverage.CompletePairs || excluded != coverage.ExcludedPairs || len(coverage.Reasons) != len(reasonCounts) {
		return errInvalidValue
	}
	for index, reason := range coverage.Reasons {
		if !closedReasons[reason.Reason] || reason.Count == 0 || reason.Count != reasonCounts[reason.Reason] ||
			(index > 0 && coverage.Reasons[index-1].Reason >= reason.Reason) {
			return errInvalidValue
		}
	}
	return nil
}

func validTrialProjections(member TrialCoverage) bool {
	if len(member.Stages) != len(closedStages) || len(member.Metrics) == 0 {
		return false
	}
	for index, projection := range member.Stages {
		if projection.Stage != closedStages[index] || !validObservationPresence(projection.Presence) ||
			(projection.Presence == experiment.PresenceObserved) != (projection.Value != nil) {
			return false
		}
	}
	previousMetric := experiment.MetricID("")
	for index, projection := range member.Metrics {
		if projection.Metric == "" || (index > 0 && previousMetric >= projection.Metric) ||
			!validObservationPresence(projection.Presence) ||
			(projection.Value != nil && projection.Presence != experiment.PresenceObserved) {
			return false
		}
		previousMetric = projection.Metric
	}
	return true
}

func validPairStatus(status PairStatus) bool {
	return status == PairComplete || status == PairExcluded || status == PairMissing || status == PairDuplicate
}

func containsReason(reasons []experiment.ExclusionReason, want experiment.ExclusionReason) bool {
	index := sort.Search(len(reasons), func(index int) bool { return reasons[index] >= want })
	return index < len(reasons) && reasons[index] == want
}

func validateBinaryContext(ctx context.Context, report Report, result BinaryResult, completePairIDs []string) error {
	if result.Kind != DimensionStage && result.Kind != DimensionMetric || !validMetricRole(result.Role) || !validDigest(result.FamilySHA256) || !validDirection(result.Direction) ||
		(result.Kind == DimensionStage && !validStageID(result.ID)) || (result.Kind == DimensionMetric && !validMetricID(experiment.MetricID(result.ID))) {
		return errInvalidValue
	}
	sum := uint64(result.BothFalse) + uint64(result.ReferenceOnly) + uint64(result.CandidateOnly) + uint64(result.BothTrue)
	if sum != uint64(result.CompletePairs) || result.CompletePairs > experiment.MaxBlocks ||
		result.Status != inferenceStatus(result.CompletePairs, report.MinimumInferenceBlocks) || result.Pairs == nil ||
		len(result.Pairs) != int(result.CompletePairs) || len(result.Pairs) != len(completePairIDs) {
		return errInvalidValue
	}
	bothFalse, referenceOnly, candidateOnly, bothTrue := uint32(0), uint32(0), uint32(0), uint32(0)
	for index, pair := range result.Pairs {
		if !validDerivedID("pair", pair.PairID) || pair.PairID != completePairIDs[index] {
			return errInvalidValue
		}
		switch {
		case !pair.Reference && !pair.Candidate:
			bothFalse++
		case pair.Reference && !pair.Candidate:
			referenceOnly++
		case !pair.Reference && pair.Candidate:
			candidateOnly++
		case pair.Reference && pair.Candidate:
			bothTrue++
		}
	}
	if bothFalse != result.BothFalse || referenceOnly != result.ReferenceOnly || candidateOnly != result.CandidateOnly || bothTrue != result.BothTrue {
		return errInvalidValue
	}
	want := rationalFromInt64(0, 1)
	if result.CompletePairs > 0 {
		want = rationalFromInt64(int64(result.CandidateOnly)-int64(result.ReferenceOnly), int64(result.CompletePairs))
	}
	if result.RiskDifference != want || result.DirectionAdjusted != adjustDirection(want, result.Direction) ||
		result.Regression != (rationalSign(result.DirectionAdjusted) < 0) {
		return errInvalidValue
	}
	if result.Status != InferenceInferential {
		if result.Interval != nil || result.ExactTest != nil {
			return errInvalidValue
		}
		return nil
	}
	probability, err := exactTwoSidedBinomialContext(ctx, result.ReferenceOnly, result.CandidateOnly)
	if err != nil {
		return err
	}
	if validateInterval(report, result.Interval) != nil || result.ExactTest == nil || result.ExactTest.Method != ExactMcNemar ||
		result.ExactTest.Left != result.ReferenceOnly || result.ExactTest.Right != result.CandidateOnly ||
		result.ExactTest.RawProbability != probability ||
		result.ExactTest.FamilySHA256 != result.FamilySHA256 || validateExactTest(result.ExactTest, result.Role) != nil {
		return errInvalidValue
	}
	return nil
}

func validateContinuous(report Report, result ContinuousResult, completePairIDs []string) error {
	return validateContinuousContext(context.Background(), report, result, completePairIDs)
}

func validateContinuousContext(ctx context.Context, report Report, result ContinuousResult, completePairIDs []string) error {
	countSum := uint64(result.CandidateHigher) + uint64(result.ReferenceHigher) + uint64(result.Equal)
	if !validMetricID(result.Metric) || result.Metric == experiment.MetricOutcome || !validMetricRole(result.Role) || !validDigest(result.FamilySHA256) || !validDirection(result.Direction) ||
		result.CompletePairs > experiment.MaxBlocks ||
		countSum != uint64(result.CompletePairs) ||
		result.Status != inferenceStatus(result.CompletePairs, report.MinimumInferenceBlocks) ||
		result.Deltas == nil || len(result.Deltas) != int(result.CompletePairs) || len(result.Deltas) != len(completePairIDs) ||
		result.PairedSignEffect != signEffect(result.CandidateHigher, result.ReferenceHigher, result.CompletePairs) {
		return errInvalidValue
	}
	deltas := make([]int64, len(result.Deltas))
	sum := new(big.Int)
	candidate, reference, equal := uint32(0), uint32(0), uint32(0)
	for index, delta := range result.Deltas {
		parsed, ok := parseRational(delta.Delta)
		if !ok || parsed.Denom().Cmp(big.NewInt(1)) != 0 || !parsed.Num().IsInt64() ||
			!validDerivedID("pair", delta.PairID) || delta.PairID != completePairIDs[index] {
			return errInvalidValue
		}
		value := parsed.Num().Int64()
		if value < -maxMetricDelta || value > maxMetricDelta {
			return errInvalidValue
		}
		deltas[index] = value
		sum.Add(sum, parsed.Num())
		switch {
		case value > 0:
			candidate++
		case value < 0:
			reference++
		default:
			equal++
		}
	}
	wantMean := rationalFromInt64(0, 1)
	wantMedian := rationalFromInt64(0, 1)
	if len(deltas) > 0 {
		wantMean = rationalFromBig(sum, new(big.Int).SetUint64(uint64(len(deltas))))
		ordered := append([]int64{}, deltas...)
		sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
		middle := len(ordered) / 2
		if len(ordered)%2 == 1 {
			wantMedian = rationalFromInt64(ordered[middle], 1)
		} else {
			numerator := new(big.Int).Add(big.NewInt(ordered[middle-1]), big.NewInt(ordered[middle]))
			wantMedian = rationalFromBig(numerator, big.NewInt(2))
		}
	}
	wantDirectionAdjusted := adjustDirection(wantMean, result.Direction)
	if result.CandidateHigher != candidate || result.ReferenceHigher != reference || result.Equal != equal ||
		result.MeanDelta != wantMean || result.MedianDelta != wantMedian || result.DirectionAdjusted != wantDirectionAdjusted ||
		result.Regression != (rationalSign(wantDirectionAdjusted) < 0) {
		return errInvalidValue
	}
	if result.Status != InferenceInferential {
		if result.Interval != nil || result.ExactTest != nil {
			return errInvalidValue
		}
		return nil
	}
	probability, err := exactTwoSidedBinomialContext(ctx, result.ReferenceHigher, result.CandidateHigher)
	if err != nil {
		return err
	}
	if validateInterval(report, result.Interval) != nil || result.ExactTest == nil || result.ExactTest.Method != ExactSign ||
		result.ExactTest.Left != result.ReferenceHigher || result.ExactTest.Right != result.CandidateHigher ||
		result.ExactTest.RawProbability != probability ||
		result.ExactTest.FamilySHA256 != result.FamilySHA256 || validateExactTest(result.ExactTest, result.Role) != nil {
		return errInvalidValue
	}
	return nil
}

func validateInterval(report Report, interval *Interval) error {
	if interval == nil || interval.Method != bootstrapMethod || interval.ConfidenceBasisPoints != report.ConfidenceBasisPoints ||
		interval.Samples != report.BootstrapSamples || !validRational(interval.Lower) || !validRational(interval.Upper) {
		return errInvalidValue
	}
	lower, _ := parseRational(interval.Lower)
	upper, _ := parseRational(interval.Upper)
	if lower.Cmp(upper) > 0 {
		return errInvalidValue
	}
	return nil
}

func validateExactTest(test *ExactTest, role experiment.MetricRole) error {
	if test == nil || !validDigest(test.FamilySHA256) || !validProbability(test.RawProbability) {
		return errInvalidValue
	}
	if role == experiment.MetricExploratory {
		if test.Multiplicity != MultiplicityExploratoryRawOnly || test.AdjustedProbability != nil || test.RejectNull != nil {
			return errInvalidValue
		}
		return nil
	}
	if test.Multiplicity != MultiplicityHolmAdjusted || test.AdjustedProbability == nil || test.RejectNull == nil || !validProbability(*test.AdjustedProbability) {
		return errInvalidValue
	}
	return nil
}

func validRational(value Rational) bool {
	_, ok := parseRational(value)
	return ok
}

func validProbability(value Rational) bool {
	parsed, ok := parseRational(value)
	return ok && parsed.Sign() >= 0 && parsed.Cmp(big.NewRat(1, 1)) <= 0
}

func signEffect(candidate, reference, total uint32) Rational {
	if total == 0 {
		return rationalFromInt64(0, 1)
	}
	return rationalFromInt64(int64(candidate)-int64(reference), int64(total))
}

func validMetricRole(role experiment.MetricRole) bool {
	return role == experiment.MetricPrimary || role == experiment.MetricConfirmatory || role == experiment.MetricExploratory
}

func validDirection(direction experiment.Direction) bool {
	return direction == experiment.DirectionHigher || direction == experiment.DirectionLower
}

func validStageID(value string) bool {
	for _, stage := range closedStages {
		if string(stage) == value {
			return true
		}
	}
	return false
}

func validMetricID(metric experiment.MetricID) bool {
	switch metric {
	case experiment.MetricOutcome, experiment.MetricInputTokens, experiment.MetricOutputTokens,
		experiment.MetricEstimatedCostMicroUSD, experiment.MetricDurationMillis:
		return true
	default:
		return false
	}
}

func validateActivation(results []ActivationSummary, planned map[string]map[string]uint32) error {
	if len(results) == 0 || len(results) > experiment.MaxStrata {
		return errInvalidValue
	}
	previous := ""
	seen := map[string]bool{}
	for _, result := range results {
		expected := uint32(0)
		for _, trials := range planned[result.StratumID] {
			expected += trials
		}
		if !validDerivedID("stratum", result.StratumID) || expected == 0 || seen[result.StratumID] || (previous != "" && previous >= result.StratumID) ||
			uint64(result.Observed)+uint64(result.Missing) != uint64(expected) ||
			uint64(result.TruePositive)+uint64(result.FalsePositive)+uint64(result.TrueNegative)+uint64(result.FalseNegative) != uint64(result.Observed) ||
			!optionalRatio(result.Precision, uint64(result.TruePositive), uint64(result.TruePositive)+uint64(result.FalsePositive)) ||
			!optionalRatio(result.Recall, uint64(result.TruePositive), uint64(result.TruePositive)+uint64(result.FalseNegative)) ||
			!optionalRatio(result.FalseActivationRate, uint64(result.FalsePositive), uint64(result.FalsePositive)+uint64(result.TrueNegative)) ||
			!optionalRatio(result.UnnecessaryLoadRate, uint64(result.FalsePositive), uint64(result.TruePositive)+uint64(result.FalsePositive)) {
			return errInvalidValue
		}
		seen[result.StratumID] = true
		previous = result.StratumID
	}
	if len(seen) != len(planned) {
		return errInvalidValue
	}
	return nil
}

func optionalRatio(value *Rational, numerator, denominator uint64) bool {
	if denominator == 0 {
		return value == nil
	}
	return value != nil && *value == rationalFromUint64(numerator, denominator)
}

func validateFunnels(funnels []TreatmentFunnel, expected uint32) (map[string]map[string]uint32, error) {
	if len(funnels) == 0 || len(funnels) > experiment.MaxTreatments*experiment.MaxStrata {
		return nil, errInvalidValue
	}
	seen := map[string]bool{}
	planned := map[string]map[string]uint32{}
	totalTrials := uint64(0)
	previous := ""
	for _, funnel := range funnels {
		key := funnel.StratumID + "\x00" + funnel.TreatmentID
		if !validDerivedID("stratum", funnel.StratumID) || !validDerivedID("treatment", funnel.TreatmentID) || seen[key] ||
			(previous != "" && previous >= key) || funnel.Trials == 0 || funnel.Trials > expected ||
			funnel.Stages == nil || len(funnel.Stages) != len(closedStages) {
			return nil, errInvalidValue
		}
		seen[key], previous = true, key
		if planned[funnel.StratumID] == nil {
			planned[funnel.StratumID] = map[string]uint32{}
		}
		planned[funnel.StratumID][funnel.TreatmentID] = funnel.Trials
		totalTrials += uint64(funnel.Trials)
		for index, stage := range funnel.Stages {
			if stage.Stage != closedStages[index] || stage.Observed > funnel.Trials || stage.Reached > stage.Observed ||
				stage.EligibleTransitions > funnel.Trials || stage.Converted > stage.EligibleTransitions ||
				!optionalRatio(stage.Rate, uint64(stage.Reached), uint64(stage.Observed)) ||
				!optionalRatio(stage.Conversion, uint64(stage.Converted), uint64(stage.EligibleTransitions)) {
				return nil, errInvalidValue
			}
			if index == 0 {
				if stage.EligibleTransitions != stage.Observed || stage.Converted != stage.Reached {
					return nil, errInvalidValue
				}
				continue
			}
			previousStage := funnel.Stages[index-1]
			if stage.EligibleTransitions > previousStage.Reached || stage.EligibleTransitions > stage.Observed ||
				stage.EligibleTransitions < intersectionLowerBound(previousStage.Reached, stage.Observed, funnel.Trials) ||
				stage.Converted > stage.Reached ||
				stage.Converted < intersectionLowerBound(stage.EligibleTransitions, stage.Reached, stage.Observed) {
				return nil, errInvalidValue
			}
		}
	}
	if totalTrials != uint64(expected) {
		return nil, errInvalidValue
	}
	return planned, nil
}

func intersectionLowerBound(left, right, universe uint32) uint32 {
	sum := uint64(left) + uint64(right)
	if sum <= uint64(universe) {
		return 0
	}
	return uint32(sum - uint64(universe)) //nolint:gosec // both inputs are subsets of universe.
}

func validatePassAtK(results []PassAtKResult, planned map[string]map[string]uint32, minimum uint32) error {
	if results == nil || len(results) > experiment.MaxTreatments*experiment.MaxPassK {
		return errInvalidValue
	}
	if len(results) == 0 {
		return nil
	}
	seen := map[string]bool{}
	type rosterCounts struct {
		attempts uint32
		passed   uint32
	}
	rosters := map[string]rosterCounts{}
	previous := ""
	for _, result := range results {
		if result.K == 0 || result.K > experiment.MaxPassK {
			return errInvalidValue
		}
		key := result.StratumID + "\x00" + result.TreatmentID + "\x00" + leftPadK(result.K)
		rosterKey := result.StratumID + "\x00" + result.TreatmentID
		plannedTrials := planned[result.StratumID][result.TreatmentID]
		if !validDerivedID("stratum", result.StratumID) || !validDerivedID("treatment", result.TreatmentID) || plannedTrials == 0 ||
			seen[key] || (previous != "" && previous >= key) ||
			result.Attempts > plannedTrials || result.Passed > result.Attempts {
			return errInvalidValue
		}
		counts := rosterCounts{attempts: result.Attempts, passed: result.Passed}
		if previousCounts, exists := rosters[rosterKey]; exists && previousCounts != counts {
			return errInvalidValue
		}
		rosters[rosterKey] = counts
		seen[key], previous = true, key
		pass, power, complete := passEstimators(result.Attempts, result.Passed, result.K)
		complete = complete && result.Attempts == plannedTrials
		if !complete {
			if result.PassAtK != nil || result.PassPowerK != nil || result.Status != InferenceInsufficient {
				return errInvalidValue
			}
			continue
		}
		if result.PassAtK == nil || result.PassPowerK == nil || *result.PassAtK != pass || *result.PassPowerK != power ||
			result.Status != inferenceStatus(result.Attempts, minimum) {
			return errInvalidValue
		}
	}
	return nil
}

func leftPadK(value uint32) string {
	const digits = "000"
	encoded := new(big.Int).SetUint64(uint64(value)).String()
	return digits[:len(digits)-len(encoded)] + encoded
}
