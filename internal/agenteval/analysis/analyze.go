package analysis

import (
	"context"
	"math/big"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

type pairRecords struct {
	pairID    string
	reference experiment.TrialRecord
	candidate experiment.TrialRecord
}

type assignmentKey struct {
	blockID     string
	treatmentID string
}

type analysisKey struct {
	comparisonID string
	stratumID    string
}

// Analyze creates one content-minimized report from the manifest's immutable
// comparison and pair membership. Input ordering has no effect; multiplicity
// is retained in InputSetSHA256 and duplicate members are explicitly excluded.
func Analyze(manifest experiment.Manifest, records []experiment.TrialRecord) (Report, error) {
	return AnalyzeContext(context.Background(), manifest, records)
}

func AnalyzeContext(ctx context.Context, manifest experiment.Manifest, records []experiment.TrialRecord) (Report, error) {
	if err := analysisContextError(ctx); err != nil {
		return Report{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return Report{}, err
	}
	if len(records) > MaxInputRecords {
		return Report{}, contractError(ErrorLimitExceeded, errInvalidValue)
	}
	recordValidator, err := experiment.NewTrialRecordValidator(manifest)
	if err != nil {
		return Report{}, contractError(ErrorInvalidInput, err)
	}
	groups := make(map[string][]experiment.TrialRecord, len(records))
	inputMembers := make([]string, 0, len(records))
	for index, record := range records {
		if index&127 == 0 {
			if err := analysisContextError(ctx); err != nil {
				return Report{}, err
			}
		}
		if err := recordValidator.Validate(record); err != nil {
			return Report{}, contractError(ErrorInvalidInput, err)
		}
		groups[record.TrialID] = append(groups[record.TrialID], record)
		inputMembers = append(inputMembers, record.TrialID+"\x00"+record.RecordSHA256)
	}
	sort.Strings(inputMembers)
	inputHash := hashParts("input-set")
	if len(inputMembers) > 0 {
		parts := make([][]byte, len(inputMembers))
		for index := range inputMembers {
			parts[index] = []byte(inputMembers[index])
		}
		inputHash = hashParts("input-set", parts...)
	}

	assignments := make(map[assignmentKey]string, experiment.MaxTrials)
	blockStrata := make(map[string]string, len(manifest.Blocks))
	stratumSet := make(map[string]bool, len(manifest.Design.Strata))
	expectedTrialIDs := make(map[string]bool, experiment.MaxTrials)
	for _, block := range manifest.Blocks {
		blockStrata[block.ID] = block.StratumID
		stratumSet[block.StratumID] = true
		for _, assignment := range block.Assignments {
			assignments[assignmentKey{block.ID, assignment.TreatmentID}] = assignment.TrialID
			expectedTrialIDs[assignment.TrialID] = true
		}
	}
	stratumIDs := make([]string, 0, len(stratumSet))
	for stratumID := range stratumSet {
		stratumIDs = append(stratumIDs, stratumID)
	}
	sort.Strings(stratumIDs)
	coverage := Coverage{
		// Manifest validation caps expected membership at MaxTrials, and the
		// input guard caps records at MaxInputRecords; both fit uint32.
		ExpectedRecords: uint32(len(expectedTrialIDs)), //nolint:gosec
		ReceivedRecords: uint32(len(records)),          //nolint:gosec
		UniqueRecords:   uint32(len(groups)),           //nolint:gosec
		Members:         make([]TrialCoverage, 0, len(expectedTrialIDs)),
		Pairs:           make([]PairCoverage, 0, len(manifest.Pairs)), Reasons: []ReasonCount{},
	}
	orderedTrialIDs := make([]string, 0, len(expectedTrialIDs))
	for trialID := range expectedTrialIDs {
		orderedTrialIDs = append(orderedTrialIDs, trialID)
	}
	sort.Strings(orderedTrialIDs)
	for _, trialID := range orderedTrialIDs {
		member := projectTrialCoverage(trialID, groups[trialID], manifest.AnalysisPlan)
		coverage.Members = append(coverage.Members, member)
		switch len(groups[trialID]) {
		case 0:
			coverage.MissingRecords++
		case 1:
		default:
			// A trial group is a subset of the MaxInputRecords-bounded input.
			coverage.DuplicateRecords += uint32(len(groups[trialID]) - 1) //nolint:gosec
		}
	}

	comparisons := make(map[string]experiment.Comparison, len(manifest.AnalysisPlan.Comparisons))
	for _, comparison := range manifest.AnalysisPlan.Comparisons {
		comparisons[comparison.ID] = comparison
	}
	pairValues := make(map[analysisKey][]pairRecords, len(comparisons)*len(stratumIDs))
	reasonCounts := map[experiment.ExclusionReason]uint32{}
	for _, pair := range manifest.Pairs {
		if err := analysisContextError(ctx); err != nil {
			return Report{}, err
		}
		comparison, ok := comparisons[pair.ComparisonID]
		if !ok {
			return Report{}, contractError(ErrorInvalidInput, errInvalidValue)
		}
		referenceID := assignments[assignmentKey{pair.BlockID, pair.ReferenceTreatmentID}]
		candidateID := assignments[assignmentKey{pair.BlockID, pair.CandidateTreatmentID}]
		reasons := append(trialGroupReasons(groups[referenceID]), trialGroupReasons(groups[candidateID])...)
		status := pairStatusForGroups(groups[referenceID], groups[candidateID])
		stratumID := blockStrata[pair.BlockID]
		if status == PairComplete {
			reference := groups[referenceID][0]
			candidate := groups[candidateID][0]
			reasons = append(reasons, selectedCoverageReasons(comparison, reference)...)
			reasons = append(reasons, selectedCoverageReasons(comparison, candidate)...)
			if len(reasons) == 0 {
				key := analysisKey{comparisonID: pair.ComparisonID, stratumID: stratumID}
				pairValues[key] = append(pairValues[key], pairRecords{pairID: pair.ID, reference: reference, candidate: candidate})
			} else {
				status = PairExcluded
			}
		}
		reasons = canonicalReasons(reasons)
		if !pairReasonsAllowedByManifest(status, reasons, manifest.AnalysisPlan) {
			return Report{}, contractError(ErrorInvalidInput, errInvalidValue)
		}
		if status == PairComplete {
			coverage.CompletePairs++
		} else {
			coverage.ExcludedPairs++
			for _, reason := range reasons {
				reasonCounts[reason]++
			}
		}
		coverage.Pairs = append(coverage.Pairs, PairCoverage{
			PairID: pair.ID, BlockID: pair.BlockID, StratumID: stratumID, ComparisonID: pair.ComparisonID, Status: status, Reasons: reasons,
		})
	}
	for reason, count := range reasonCounts {
		coverage.Reasons = append(coverage.Reasons, ReasonCount{Reason: reason, Count: count})
	}
	sort.Slice(coverage.Pairs, func(left, right int) bool { return coverage.Pairs[left].PairID < coverage.Pairs[right].PairID })
	sort.Slice(coverage.Reasons, func(left, right int) bool { return coverage.Reasons[left].Reason < coverage.Reasons[right].Reason })
	for key := range pairValues {
		sort.Slice(pairValues[key], func(left, right int) bool { return pairValues[key][left].pairID < pairValues[key][right].pairID })
	}
	passResultCount := uint64(len(stratumIDs)) * uint64(len(manifest.Treatments)) * uint64(len(manifest.AnalysisPlan.RepeatedAttempts.K))
	if passResultCount > MaxPassAtKResults {
		return Report{}, contractError(ErrorLimitExceeded, errInvalidValue)
	}
	primitiveLimit := uint64(0)
	dimensionResults := uint64(0)
	pairedDeltas := uint64(0)
	for _, comparison := range manifest.AnalysisPlan.Comparisons {
		for _, stratumID := range stratumIDs {
			pairCount := uint64(len(pairValues[analysisKey{comparisonID: comparison.ID, stratumID: stratumID}]))
			dimensions := uint64(len(comparison.Stages) + len(comparison.Metrics))
			if dimensions > MaxDimensionResults-dimensionResults {
				return Report{}, contractError(ErrorLimitExceeded, errInvalidValue)
			}
			dimensionResults += dimensions
			for range comparison.Stages {
				if pairCount > MaxPairedDeltas-pairedDeltas {
					return Report{}, contractError(ErrorLimitExceeded, errInvalidValue)
				}
				pairedDeltas += pairCount
			}
			for range comparison.Metrics {
				if pairCount > MaxPairedDeltas-pairedDeltas {
					return Report{}, contractError(ErrorLimitExceeded, errInvalidValue)
				}
				pairedDeltas += pairCount
			}
			if pairCount < uint64(manifest.AnalysisPlan.MinimumInferenceBlocks) {
				continue
			}
			addition := pairCount * uint64(manifest.AnalysisPlan.BootstrapSamples) * dimensions
			if addition > MaxPrimitiveDraws-primitiveLimit {
				return Report{}, contractError(ErrorLimitExceeded, errInvalidValue)
			}
			primitiveLimit += addition
		}
	}
	activation, err := activationSummaries(ctx, manifest, groups, stratumIDs)
	if err != nil {
		return Report{}, err
	}
	funnels, err := comparisonsFunnels(ctx, manifest, groups, stratumIDs)
	if err != nil {
		return Report{}, err
	}
	passAtK, err := passAtKResults(ctx, manifest, groups, stratumIDs)
	if err != nil {
		return Report{}, err
	}

	report := Report{
		Schema: ReportSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		ManifestSHA256: manifest.ManifestSHA256, AnalysisPlanSHA256: manifest.AnalysisPlan.AnalysisPlanSHA256,
		InputSetSHA256: inputHash, ConfidenceBasisPoints: manifest.AnalysisPlan.ConfidenceBasisPoints,
		MinimumInferenceBlocks: manifest.AnalysisPlan.MinimumInferenceBlocks,
		BootstrapSamples:       manifest.AnalysisPlan.BootstrapSamples, Multiplicity: manifest.AnalysisPlan.Multiplicity,
		Coverage: coverage, Comparisons: make([]ComparisonResult, 0, len(manifest.AnalysisPlan.Comparisons)*len(stratumIDs)),
		Activation: activation, Funnels: funnels, PassAtK: passAtK,
	}
	primitiveDraws := uint64(0)
	for _, comparison := range manifest.AnalysisPlan.Comparisons {
		for _, stratumID := range stratumIDs {
			if err := analysisContextError(ctx); err != nil {
				return Report{}, err
			}
			key := analysisKey{comparisonID: comparison.ID, stratumID: stratumID}
			result, draws, err := analyzeComparison(ctx, manifest, comparison, stratumID, pairValues[key])
			if err != nil {
				return Report{}, err
			}
			if draws > MaxPrimitiveDraws-primitiveDraws {
				return Report{}, contractError(ErrorLimitExceeded, errInvalidValue)
			}
			primitiveDraws += draws
			report.Comparisons = append(report.Comparisons, result)
		}
	}
	if primitiveDraws != primitiveLimit {
		return Report{}, contractError(ErrorInvalidInput, errInvalidValue)
	}
	if err := applyHolmContext(ctx, &report, manifest.AnalysisPlan.ConfidenceBasisPoints); err != nil {
		return Report{}, err
	}
	report.ReportSHA256 = reportDigest(report)
	if err := analysisContextError(ctx); err != nil {
		return Report{}, err
	}
	if err := validateReportForManifestContext(ctx, manifest, report, false); err != nil {
		return Report{}, err
	}
	if _, err := encodeReportCanonical(report); err != nil {
		return Report{}, err
	}
	return cloneReport(report), nil
}

func analysisContextError(ctx context.Context) error {
	if ctx == nil {
		return contractError(ErrorInterrupted, context.Canceled)
	}
	if err := ctx.Err(); err != nil {
		return contractError(ErrorInterrupted, err)
	}
	return nil
}

// ContextError lets a composing facade refuse before it starts a bounded
// source read under already-revoked authority.
func ContextError(ctx context.Context) error { return analysisContextError(ctx) }

func trialGroupReasons(records []experiment.TrialRecord) []experiment.ExclusionReason {
	switch len(records) {
	case 0:
		return []experiment.ExclusionReason{experiment.ExclusionMissingMember}
	case 1:
		record := records[0]
		if record.Eligibility == experiment.EligibilitySupported && record.Exclusion == experiment.ExclusionNone {
			return nil
		}
		if record.Exclusion != experiment.ExclusionNone {
			return []experiment.ExclusionReason{record.Exclusion}
		}
		return []experiment.ExclusionReason{experiment.ExclusionIneligible}
	default:
		return []experiment.ExclusionReason{experiment.ExclusionDuplicateMember}
	}
}

func pairStatusForGroups(reference, candidate []experiment.TrialRecord) PairStatus {
	if len(reference) > 1 || len(candidate) > 1 {
		return PairDuplicate
	}
	if len(reference) == 0 || len(candidate) == 0 {
		return PairMissing
	}
	if len(trialGroupReasons(reference))+len(trialGroupReasons(candidate)) > 0 {
		return PairExcluded
	}
	return PairComplete
}

func selectedCoverageReasons(comparison experiment.Comparison, record experiment.TrialRecord) []experiment.ExclusionReason {
	reasons := []experiment.ExclusionReason{}
	for _, selected := range comparison.Stages {
		for _, observed := range record.Stages {
			if observed.Stage == selected && observed.Presence != experiment.PresenceObserved {
				reasons = append(reasons, presenceReason(observed.Presence))
			}
		}
	}
	for _, selected := range comparison.Metrics {
		for _, observed := range record.Metrics {
			if observed.Metric == selected && observed.Presence != experiment.PresenceObserved {
				reasons = append(reasons, presenceReason(observed.Presence))
			}
		}
	}
	return reasons
}

func presenceReason(presence experiment.Presence) experiment.ExclusionReason {
	if presence == experiment.PresenceUnsupported || presence == experiment.PresenceNotApplicable {
		return experiment.ExclusionUnsupportedCapability
	}
	return experiment.ExclusionCoverageMismatch
}

func canonicalReasons(input []experiment.ExclusionReason) []experiment.ExclusionReason {
	if len(input) == 0 {
		return []experiment.ExclusionReason{}
	}
	sort.Slice(input, func(left, right int) bool { return input[left] < input[right] })
	result := input[:0]
	for _, reason := range input {
		if len(result) == 0 || result[len(result)-1] != reason {
			result = append(result, reason)
		}
	}
	return result
}

func analyzeComparison(ctx context.Context, manifest experiment.Manifest, comparison experiment.Comparison, stratumID string, pairs []pairRecords) (ComparisonResult, uint64, error) {
	// Manifest pair membership is capped at experiment.MaxPairBindings.
	completePairs := uint32(len(pairs)) //nolint:gosec
	result := ComparisonResult{ComparisonID: comparison.ID, StratumID: stratumID, CompletePairs: completePairs, Binary: []BinaryResult{}, Continuous: []ContinuousResult{}}
	domainID := comparison.ID + "\x00" + stratumID
	for _, binding := range manifest.Pairs {
		if binding.ComparisonID == comparison.ID {
			result.ReferenceTreatmentID = binding.ReferenceTreatmentID
			result.CandidateTreatmentID = binding.CandidateTreatmentID
			break
		}
	}
	stageDeclarations := map[experiment.FunnelStage]experiment.StageDeclaration{}
	for _, declaration := range manifest.AnalysisPlan.Stages {
		stageDeclarations[declaration.Stage] = declaration
	}
	metricDeclarations := map[experiment.MetricID]experiment.MetricDeclaration{}
	for _, declaration := range manifest.AnalysisPlan.Metrics {
		metricDeclarations[declaration.ID] = declaration
	}
	pairIDs := pairIDsForRecords(pairs)
	draws := uint64(0)
	for _, stage := range comparison.Stages {
		declaration := stageDeclarations[stage]
		values := make([][2]bool, len(pairs))
		for index, pair := range pairs {
			values[index] = [2]bool{stageValue(pair.reference, stage), stageValue(pair.candidate, stage)}
		}
		binary, consumed, err := analyzeBinary(ctx, manifest, domainID, DimensionStage, string(stage), declaration.Role,
			experiment.DirectionHigher, declaration.FamilySHA256, pairIDs, values)
		if err != nil {
			return ComparisonResult{}, 0, err
		}
		draws += consumed
		result.Binary = append(result.Binary, binary)
	}
	for _, metric := range comparison.Metrics {
		declaration := metricDeclarations[metric]
		if declaration.Kind == experiment.MetricBinary {
			values := make([][2]bool, len(pairs))
			for index, pair := range pairs {
				values[index] = [2]bool{metricValue(pair.reference, metric) == 1, metricValue(pair.candidate, metric) == 1}
			}
			binary, consumed, err := analyzeBinary(ctx, manifest, domainID, DimensionMetric, string(metric), declaration.Role,
				declaration.Direction, declaration.FamilySHA256, pairIDs, values)
			if err != nil {
				return ComparisonResult{}, 0, err
			}
			draws += consumed
			result.Binary = append(result.Binary, binary)
			continue
		}
		deltas := make([]int64, len(pairs))
		for index, pair := range pairs {
			candidate := metricValue(pair.candidate, metric)
			reference := metricValue(pair.reference, metric)
			// Trial validation caps both values at MaxMetricValue (< MaxInt64).
			deltas[index] = int64(candidate) - int64(reference) //nolint:gosec
		}
		continuous, consumed, err := analyzeContinuous(ctx, manifest, domainID, declaration, pairIDs, deltas)
		if err != nil {
			return ComparisonResult{}, 0, err
		}
		draws += consumed
		result.Continuous = append(result.Continuous, continuous)
	}
	if draws > MaxPrimitiveDraws {
		return ComparisonResult{}, 0, contractError(ErrorLimitExceeded, errInvalidValue)
	}
	result.Pareto = comparisonPareto(result)
	return result, draws, nil
}

func analyzeBinary(ctx context.Context, manifest experiment.Manifest, comparisonID string, kind DimensionKind, id string, role experiment.MetricRole,
	direction experiment.Direction, family string, pairIDs []string, values [][2]bool,
) (BinaryResult, uint64, error) {
	// Values originate from manifest pairs capped at MaxPairBindings.
	completePairs := uint32(len(values)) //nolint:gosec
	result := BinaryResult{Kind: kind, ID: id, Role: role, FamilySHA256: family, Direction: direction, CompletePairs: completePairs, Pairs: make([]BinaryPair, len(values))}
	deltas := make([]int64, len(values))
	for index, value := range values {
		switch {
		case !value[0] && !value[1]:
			result.BothFalse++
		case value[0] && !value[1]:
			result.ReferenceOnly++
			deltas[index] = -1
		case !value[0] && value[1]:
			result.CandidateOnly++
			deltas[index] = 1
		default:
			result.BothTrue++
		}
		result.Pairs[index] = BinaryPair{PairID: pairIDs[index], Reference: value[0], Candidate: value[1]}
	}
	result.Status = inferenceStatus(completePairs, manifest.AnalysisPlan.MinimumInferenceBlocks)
	if len(values) == 0 {
		result.RiskDifference = rationalFromInt64(0, 1)
	} else {
		result.RiskDifference = rationalFromInt64(int64(result.CandidateOnly)-int64(result.ReferenceOnly), int64(len(values)))
	}
	result.DirectionAdjusted = adjustDirection(result.RiskDifference, direction)
	result.Regression = rationalSign(result.DirectionAdjusted) < 0
	draws := uint64(0)
	if result.Status == InferenceInferential {
		domain := comparisonID + "\x00" + string(kind) + "\x00" + id
		interval, err := bootstrapInterval(ctx, deltas, manifest.AnalysisPlan.BootstrapSamples,
			manifest.AnalysisPlan.ConfidenceBasisPoints, manifest.AnalysisPlan.BootstrapSeedSHA256, domain)
		if err != nil {
			return BinaryResult{}, 0, err
		}
		result.Interval = &interval
		draws = uint64(len(values)) * uint64(manifest.AnalysisPlan.BootstrapSamples)
		probability, err := exactTwoSidedBinomialContext(ctx, result.ReferenceOnly, result.CandidateOnly)
		if err != nil {
			return BinaryResult{}, 0, err
		}
		result.ExactTest = &ExactTest{
			Method: ExactMcNemar, Left: result.ReferenceOnly, Right: result.CandidateOnly,
			RawProbability: probability,
			Multiplicity:   multiplicityStatus(role), FamilySHA256: family,
		}
	}
	return result, draws, nil
}

func pairIDsForRecords(pairs []pairRecords) []string {
	result := make([]string, len(pairs))
	for index := range pairs {
		result[index] = pairs[index].pairID
	}
	return result
}

func analyzeContinuous(ctx context.Context, manifest experiment.Manifest, comparisonID string, declaration experiment.MetricDeclaration,
	pairIDs []string, deltas []int64,
) (ContinuousResult, uint64, error) {
	// Deltas originate from manifest pairs capped at MaxPairBindings.
	completePairs := uint32(len(deltas)) //nolint:gosec
	result := ContinuousResult{
		Metric: declaration.ID, Role: declaration.Role, FamilySHA256: declaration.FamilySHA256, Direction: declaration.Direction,
		CompletePairs: completePairs, Deltas: make([]PairDelta, len(deltas)),
	}
	sum := new(big.Int)
	for _, delta := range deltas {
		sum.Add(sum, big.NewInt(delta))
		switch {
		case delta > 0:
			result.CandidateHigher++
		case delta < 0:
			result.ReferenceHigher++
		default:
			result.Equal++
		}
	}
	for index, delta := range deltas {
		result.Deltas[index] = PairDelta{PairID: pairIDs[index], Delta: rationalFromInt64(delta, 1)}
	}
	result.Status = inferenceStatus(completePairs, manifest.AnalysisPlan.MinimumInferenceBlocks)
	if len(deltas) == 0 {
		result.MeanDelta = rationalFromInt64(0, 1)
		result.MedianDelta = rationalFromInt64(0, 1)
		result.PairedSignEffect = rationalFromInt64(0, 1)
	} else {
		result.MeanDelta = rationalFromBig(sum, new(big.Int).SetUint64(uint64(len(deltas))))
		ordered := append([]int64{}, deltas...)
		sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
		middle := len(ordered) / 2
		if len(ordered)%2 == 1 {
			result.MedianDelta = rationalFromInt64(ordered[middle], 1)
		} else {
			numerator := new(big.Int).Add(big.NewInt(ordered[middle-1]), big.NewInt(ordered[middle]))
			result.MedianDelta = rationalFromBig(numerator, big.NewInt(2))
		}
		result.PairedSignEffect = rationalFromInt64(int64(result.CandidateHigher)-int64(result.ReferenceHigher), int64(len(deltas)))
	}
	result.DirectionAdjusted = adjustDirection(result.MeanDelta, declaration.Direction)
	result.Regression = rationalSign(result.DirectionAdjusted) < 0
	draws := uint64(0)
	if result.Status == InferenceInferential {
		domain := comparisonID + "\x00metric\x00" + string(declaration.ID)
		interval, err := bootstrapInterval(ctx, deltas, manifest.AnalysisPlan.BootstrapSamples,
			manifest.AnalysisPlan.ConfidenceBasisPoints, manifest.AnalysisPlan.BootstrapSeedSHA256, domain)
		if err != nil {
			return ContinuousResult{}, 0, err
		}
		result.Interval = &interval
		draws = uint64(len(deltas)) * uint64(manifest.AnalysisPlan.BootstrapSamples)
		probability, err := exactTwoSidedBinomialContext(ctx, result.ReferenceHigher, result.CandidateHigher)
		if err != nil {
			return ContinuousResult{}, 0, err
		}
		result.ExactTest = &ExactTest{
			Method: ExactSign, Left: result.ReferenceHigher, Right: result.CandidateHigher,
			RawProbability: probability,
			Multiplicity:   multiplicityStatus(declaration.Role), FamilySHA256: declaration.FamilySHA256,
		}
	}
	return result, draws, nil
}

func inferenceStatus(count, minimum uint32) InferenceStatus {
	if count == 0 {
		return InferenceInsufficient
	}
	if count < minimum {
		return InferenceDescriptive
	}
	return InferenceInferential
}

func multiplicityStatus(role experiment.MetricRole) MultiplicityStatus {
	if role == experiment.MetricExploratory {
		return MultiplicityExploratoryRawOnly
	}
	return MultiplicityHolmAdjusted
}

func adjustDirection(value Rational, direction experiment.Direction) Rational {
	if direction == experiment.DirectionHigher {
		return value
	}
	parsed, _ := parseRational(value)
	parsed.Neg(parsed)
	return rationalFromBig(parsed.Num(), parsed.Denom())
}

func rationalSign(value Rational) int {
	parsed, _ := parseRational(value)
	return parsed.Sign()
}

func pointer[T any](value T) *T { return &value }

func stageValue(record experiment.TrialRecord, stage experiment.FunnelStage) bool {
	for _, observed := range record.Stages {
		if observed.Stage == stage {
			return *observed.Value
		}
	}
	return false
}

func metricValue(record experiment.TrialRecord, metric experiment.MetricID) uint64 {
	for _, observed := range record.Metrics {
		if observed.Metric == metric && observed.Presence == experiment.PresenceObserved && observed.Value != nil {
			return *observed.Value
		}
	}
	return 0
}

func comparisonPareto(result ComparisonResult) ParetoRelation {
	if result.CompletePairs == 0 || len(result.Binary)+len(result.Continuous) == 0 {
		return ParetoUnavailable
	}
	positive, negative := false, false
	for _, dimension := range result.Binary {
		positive = positive || rationalSign(dimension.DirectionAdjusted) > 0
		negative = negative || rationalSign(dimension.DirectionAdjusted) < 0
	}
	for _, dimension := range result.Continuous {
		positive = positive || rationalSign(dimension.DirectionAdjusted) > 0
		negative = negative || rationalSign(dimension.DirectionAdjusted) < 0
	}
	switch {
	case positive && negative:
		return ParetoTradeoff
	case positive:
		return ParetoCandidateDominates
	case negative:
		return ParetoReferenceDominates
	default:
		return ParetoEqual
	}
}

func applyHolm(report *Report, confidence uint16) {
	_ = applyHolmContext(context.Background(), report, confidence)
}

func applyHolmContext(ctx context.Context, report *Report, confidence uint16) error {
	type location struct {
		comparison int
		binary     bool
		dimension  int
		key        string
		test       *ExactTest
	}
	families := map[string][]location{}
	familySizes := map[string]int{}
	for comparisonIndex := range report.Comparisons {
		if err := analysisContextError(ctx); err != nil {
			return err
		}
		comparison := &report.Comparisons[comparisonIndex]
		for dimensionIndex := range comparison.Binary {
			dimension := &comparison.Binary[dimensionIndex]
			test := dimension.ExactTest
			if dimension.Role != experiment.MetricExploratory {
				familySizes[dimension.FamilySHA256]++
			}
			if test != nil && test.Multiplicity == MultiplicityHolmAdjusted {
				key := comparison.ComparisonID + "\x00" + comparison.StratumID + "\x00binary\x00" + dimension.ID
				families[test.FamilySHA256] = append(families[test.FamilySHA256], location{comparisonIndex, true, dimensionIndex, key, test})
			}
		}
		for dimensionIndex := range comparison.Continuous {
			dimension := &comparison.Continuous[dimensionIndex]
			test := dimension.ExactTest
			if dimension.Role != experiment.MetricExploratory {
				familySizes[dimension.FamilySHA256]++
			}
			if test != nil && test.Multiplicity == MultiplicityHolmAdjusted {
				key := comparison.ComparisonID + "\x00" + comparison.StratumID + "\x00metric\x00" + string(dimension.Metric)
				families[test.FamilySHA256] = append(families[test.FamilySHA256], location{comparisonIndex, false, dimensionIndex, key, test})
			}
		}
	}
	alpha := new(big.Rat).SetFrac(big.NewInt(int64(10000-confidence)), big.NewInt(10000))
	for _, family := range families {
		if err := analysisContextError(ctx); err != nil {
			return err
		}
		sort.Slice(family, func(left, right int) bool {
			leftP, _ := parseRational(family[left].test.RawProbability)
			rightP, _ := parseRational(family[right].test.RawProbability)
			if compared := leftP.Cmp(rightP); compared != 0 {
				return compared < 0
			}
			return family[left].key < family[right].key
		})
		maximum := new(big.Rat)
		for index := range family {
			if index&127 == 0 {
				if err := analysisContextError(ctx); err != nil {
					return err
				}
			}
			raw, _ := parseRational(family[index].test.RawProbability)
			remaining := familySizes[family[index].test.FamilySHA256] - index
			adjusted := new(big.Rat).Mul(raw, new(big.Rat).SetInt64(int64(remaining)))
			if adjusted.Cmp(maximum) < 0 {
				adjusted.Set(maximum)
			}
			if adjusted.Cmp(big.NewRat(1, 1)) > 0 {
				adjusted.SetInt64(1)
			}
			maximum.Set(adjusted)
			value := rationalFromBig(adjusted.Num(), adjusted.Denom())
			reject := adjusted.Cmp(alpha) <= 0
			family[index].test.AdjustedProbability = &value
			family[index].test.RejectNull = &reject
		}
	}
	return nil
}
