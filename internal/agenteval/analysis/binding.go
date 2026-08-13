package analysis

import (
	"context"
	"math/big"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

// ValidateReportForManifest proves that a structurally valid report retains
// the exact preregistered manifest vocabulary and internally reproducible
// summary evidence. Trial records are still needed to authenticate the source
// observations from which the content-minimized report was produced.
func ValidateReportForManifest(manifest experiment.Manifest, report Report) error {
	return validateReportForManifestContext(context.Background(), manifest, report, true)
}

// ValidateManifest performs the narrower admission owned by the statistical
// consumer without reading any trial records. Experiment manifest v1 remains
// readable under its historical aggregate-roster rules; analysis never pools
// strata and therefore has additional evaluability requirements.
func ValidateManifest(manifest experiment.Manifest) error {
	if err := experiment.ValidateManifest(manifest); err != nil {
		return contractError(ErrorInvalidInput, err)
	}
	return validateAnalysisRoster(manifest)
}

func validateReportForManifestContext(ctx context.Context, manifest experiment.Manifest, report Report, replayIntervals bool) error {
	if err := analysisContextError(ctx); err != nil {
		return err
	}
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	if err := analysisContextError(ctx); err != nil {
		return err
	}
	if err := validateReportShapeContext(ctx, report); err != nil {
		return err
	}
	plan := manifest.AnalysisPlan
	if report.ManifestSHA256 != manifest.ManifestSHA256 || report.AnalysisPlanSHA256 != plan.AnalysisPlanSHA256 ||
		report.ConfidenceBasisPoints != plan.ConfidenceBasisPoints || report.MinimumInferenceBlocks != plan.MinimumInferenceBlocks ||
		report.BootstrapSamples != plan.BootstrapSamples || report.Multiplicity != plan.Multiplicity {
		return contractError(ErrorInvalidReport, errInvalidValue)
	}
	stratumIDs, trialsByStratumTreatment := manifestStrataAndTrials(manifest)
	expectedRecords := uint32(0)
	for _, treatments := range trialsByStratumTreatment {
		for _, count := range treatments {
			expectedRecords += count
		}
	}
	observationsFit, err := reportObservationsFitCoverage(ctx, report, manifest)
	if err != nil {
		return err
	}
	retainedMatch, err := reportRetainedObservationsMatchSummaries(ctx, report, manifest)
	if err != nil {
		return err
	}
	continuousFeasible, err := reportContinuousDeltasFeasible(ctx, report, manifest)
	if err != nil {
		return err
	}
	if report.Coverage.ExpectedRecords != expectedRecords || !reportPairsMatchManifest(report.Coverage.Pairs, manifest.Pairs, manifest.Blocks) ||
		!reportReasonsAllowedByManifest(report, plan) ||
		!reportComparisonsMatchManifest(report.Comparisons, manifest, stratumIDs) ||
		!reportActivationMatchesManifest(report.Activation, stratumIDs) ||
		!reportFunnelsMatchManifest(report.Funnels, manifest.Treatments, stratumIDs, trialsByStratumTreatment) ||
		!reportPassAtKMatchesManifest(report.PassAtK, manifest.Treatments, stratumIDs, plan.RepeatedAttempts) ||
		!reportActivationMatchesFunnels(report.Activation, report.Funnels, manifest.Treatments) || !observationsFit || !retainedMatch || !continuousFeasible {
		return contractError(ErrorInvalidReport, errInvalidValue)
	}
	if replayIntervals {
		if err := analysisContextError(ctx); err != nil {
			return err
		}
		if err := reportIntervalsMatchManifest(ctx, manifest, report); err != nil {
			return contractError(ErrorInvalidReport, err)
		}
	}
	return nil
}

type retainedObservationKey struct {
	stratumID   string
	treatmentID string
	dimension   string
}

func reportRetainedObservationsMatchSummaries(ctx context.Context, report Report, manifest experiment.Manifest) (bool, error) {
	treatments := make(map[string]experiment.Treatment, len(manifest.Treatments))
	for _, treatment := range manifest.Treatments {
		treatments[treatment.ID] = treatment
	}
	assignments := make(map[string]map[string]string, len(manifest.Blocks))
	blockStrata := make(map[string]string, len(manifest.Blocks))
	for _, block := range manifest.Blocks {
		blockStrata[block.ID] = block.StratumID
		assignments[block.ID] = make(map[string]string, len(block.Assignments))
		for _, assignment := range block.Assignments {
			assignments[block.ID][assignment.TreatmentID] = assignment.TrialID
		}
	}
	pairs := make(map[string]experiment.PairBinding, len(manifest.Pairs))
	for _, pair := range manifest.Pairs {
		pairs[pair.ID] = pair
	}
	projectionValues := projectedBinaryValues(report)
	observations := map[retainedObservationKey]map[string]bool{}
	for comparisonIndex, comparison := range report.Comparisons {
		if comparisonIndex&127 == 0 {
			if err := analysisContextError(ctx); err != nil {
				return false, err
			}
		}
		for _, dimension := range comparison.Binary {
			for _, retained := range dimension.Pairs {
				pair, ok := pairs[retained.PairID]
				if !ok || pair.ComparisonID != comparison.ComparisonID || blockStrata[pair.BlockID] != comparison.StratumID {
					return false, nil
				}
				dimensionID := string(dimension.Kind) + "\x00" + dimension.ID
				for _, value := range []struct {
					treatmentID string
					observed    bool
				}{
					{pair.ReferenceTreatmentID, retained.Reference},
					{pair.CandidateTreatmentID, retained.Candidate},
				} {
					trialID := assignments[pair.BlockID][value.treatmentID]
					key := retainedObservationKey{comparison.StratumID, value.treatmentID, dimensionID}
					projected, projectedOK := projectionValues[trialID][dimensionID]
					if trialID == "" || !projectedOK || projected != value.observed ||
						!addRetainedObservation(observations, key, trialID, value.observed) {
						return false, nil
					}
				}
			}
		}
	}
	funnels := make(map[retainedObservationKey]FunnelStage, len(report.Funnels)*len(closedStages))
	for _, funnel := range report.Funnels {
		for _, stage := range funnel.Stages {
			funnels[retainedObservationKey{funnel.StratumID, funnel.TreatmentID, string(DimensionStage) + "\x00" + string(stage.Stage)}] = stage
		}
		for stageIndex := 1; stageIndex < len(funnel.Stages); stageIndex++ {
			previousKey := retainedObservationKey{funnel.StratumID, funnel.TreatmentID, string(DimensionStage) + "\x00" + string(funnel.Stages[stageIndex-1].Stage)}
			currentKey := retainedObservationKey{funnel.StratumID, funnel.TreatmentID, string(DimensionStage) + "\x00" + string(funnel.Stages[stageIndex].Stage)}
			tt, tf, ft, ff := uint64(0), uint64(0), uint64(0), uint64(0)
			for trialID, previous := range observations[previousKey] {
				current, ok := observations[currentKey][trialID]
				if !ok {
					continue
				}
				switch {
				case previous && current:
					tt++
				case previous:
					tf++
				case current:
					ft++
				default:
					ff++
				}
			}
			stage := funnel.Stages[stageIndex]
			previousTrueCurrentFalse := uint64(stage.EligibleTransitions - stage.Converted)
			currentFalse := uint64(stage.Observed - stage.Reached)
			if uint64(stage.Converted) < tt || previousTrueCurrentFalse < tf ||
				uint64(stage.Reached-stage.Converted) < ft || currentFalse < previousTrueCurrentFalse ||
				currentFalse-previousTrueCurrentFalse < ff {
				return false, nil
			}
		}
	}
	passRows := make(map[retainedObservationKey]PassAtKResult, len(report.PassAtK))
	for _, result := range report.PassAtK {
		key := retainedObservationKey{result.StratumID, result.TreatmentID, string(DimensionMetric) + "\x00" + string(experiment.MetricOutcome)}
		if _, exists := passRows[key]; !exists {
			passRows[key] = result
		}
	}
	activation := make(map[string]ActivationSummary, len(report.Activation))
	for _, summary := range report.Activation {
		activation[summary.StratumID] = summary
	}
	activationLower := make(map[string][4]uint64, len(report.Activation))
	for key, values := range observations {
		observed, reached := uint64(len(values)), uint64(0)
		for _, value := range values {
			if value {
				reached++
			}
		}
		if strings.HasPrefix(key.dimension, string(DimensionStage)+"\x00") {
			stage, ok := funnels[key]
			if !ok || uint64(stage.Observed) < observed || uint64(stage.Reached) < reached || uint64(stage.Observed-stage.Reached) < observed-reached {
				return false, nil
			}
			if key.dimension == string(DimensionStage)+"\x00"+string(experiment.StageLoad) {
				cells := activationLower[key.stratumID]
				expected := treatments[key.treatmentID].ExpectedActivation
				for _, value := range values {
					switch {
					case expected && value:
						cells[0]++
					case !expected && value:
						cells[1]++
					case !expected && !value:
						cells[2]++
					case expected && !value:
						cells[3]++
					}
				}
				activationLower[key.stratumID] = cells
			}
			continue
		}
		if key.dimension != string(DimensionMetric)+"\x00"+string(experiment.MetricOutcome) ||
			manifest.AnalysisPlan.RepeatedAttempts.Kind == experiment.RepeatedAttemptsNone {
			continue
		}
		pass, ok := passRows[key]
		if !ok || uint64(pass.Attempts) < observed || uint64(pass.Passed) < reached || uint64(pass.Attempts-pass.Passed) < observed-reached {
			return false, nil
		}
	}
	for stratumID, lower := range activationLower {
		summary, ok := activation[stratumID]
		if !ok || uint64(summary.TruePositive) < lower[0] || uint64(summary.FalsePositive) < lower[1] ||
			uint64(summary.TrueNegative) < lower[2] || uint64(summary.FalseNegative) < lower[3] {
			return false, nil
		}
	}
	return true, nil
}

type continuousFeasibilityEdge struct {
	left  string
	right string
	delta int64
}

func reportContinuousDeltasFeasible(ctx context.Context, report Report, manifest experiment.Manifest) (bool, error) {
	pairs := make(map[string]experiment.PairBinding, len(manifest.Pairs))
	blockStrata := make(map[string]string, len(manifest.Blocks))
	assignments := make(map[string]map[string]string, len(manifest.Blocks))
	for _, block := range manifest.Blocks {
		blockStrata[block.ID] = block.StratumID
		assignments[block.ID] = make(map[string]string, len(block.Assignments))
		for _, assignment := range block.Assignments {
			assignments[block.ID][assignment.TreatmentID] = assignment.TrialID
		}
	}
	for _, pair := range manifest.Pairs {
		pairs[pair.ID] = pair
	}
	edges := map[string][]continuousFeasibilityEdge{}
	for comparisonIndex, comparison := range report.Comparisons {
		if comparisonIndex&127 == 0 {
			if err := analysisContextError(ctx); err != nil {
				return false, err
			}
		}
		for _, dimension := range comparison.Continuous {
			for _, retained := range dimension.Deltas {
				pair, ok := pairs[retained.PairID]
				parsed, parsedOK := parseRational(retained.Delta)
				if !ok || !parsedOK || !parsed.Num().IsInt64() || pair.ComparisonID != comparison.ComparisonID ||
					blockStrata[pair.BlockID] != comparison.StratumID {
					return false, nil
				}
				left := assignments[pair.BlockID][pair.ReferenceTreatmentID]
				right := assignments[pair.BlockID][pair.CandidateTreatmentID]
				key := comparison.StratumID + "\x00" + string(dimension.Metric) + "\x00" + pair.BlockID
				edges[key] = append(edges[key], continuousFeasibilityEdge{left: left, right: right, delta: parsed.Num().Int64()})
			}
		}
	}
	limit := new(big.Int).SetUint64(experiment.MaxMetricValue)
	for _, graph := range edges {
		adjacency := map[string][]continuousFeasibilityEdge{}
		for _, edge := range graph {
			adjacency[edge.left] = append(adjacency[edge.left], edge)
			adjacency[edge.right] = append(adjacency[edge.right], continuousFeasibilityEdge{left: edge.right, right: edge.left, delta: -edge.delta})
		}
		offsets := map[string]*big.Int{}
		for start := range adjacency {
			if offsets[start] != nil {
				continue
			}
			offsets[start] = new(big.Int)
			queue := []string{start}
			minimum, maximum := new(big.Int), new(big.Int)
			for len(queue) > 0 {
				vertex := queue[0]
				queue = queue[1:]
				if err := analysisContextError(ctx); err != nil {
					return false, err
				}
				for _, edge := range adjacency[vertex] {
					want := new(big.Int).Add(offsets[vertex], big.NewInt(edge.delta))
					if existing := offsets[edge.right]; existing != nil {
						if existing.Cmp(want) != 0 {
							return false, nil
						}
						continue
					}
					offsets[edge.right] = want
					if want.Cmp(minimum) < 0 {
						minimum.Set(want)
					}
					if want.Cmp(maximum) > 0 {
						maximum.Set(want)
					}
					queue = append(queue, edge.right)
				}
			}
			if new(big.Int).Sub(maximum, minimum).Cmp(limit) > 0 {
				return false, nil
			}
		}
	}
	return true, nil
}

func addRetainedObservation(observations map[retainedObservationKey]map[string]bool, key retainedObservationKey, trialID string, value bool) bool {
	if observations[key] == nil {
		observations[key] = map[string]bool{}
	}
	previous, exists := observations[key][trialID]
	if exists && previous != value {
		return false
	}
	observations[key][trialID] = value
	return true
}

func reportReasonsAllowedByManifest(report Report, plan experiment.AnalysisPlan) bool {
	allowed := make(map[experiment.ExclusionReason]bool, len(plan.AllowedExclusions))
	for _, reason := range plan.AllowedExclusions {
		allowed[reason] = true
	}
	for _, member := range report.Coverage.Members {
		if member.Exclusion != experiment.ExclusionNone && !allowed[member.Exclusion] {
			return false
		}
	}
	for _, pair := range report.Coverage.Pairs {
		if !pairReasonsAllowed(pair.Status, pair.Reasons, allowed) {
			return false
		}
	}
	return true
}

func pairReasonsAllowedByManifest(status PairStatus, reasons []experiment.ExclusionReason, plan experiment.AnalysisPlan) bool {
	allowed := make(map[experiment.ExclusionReason]bool, len(plan.AllowedExclusions))
	for _, reason := range plan.AllowedExclusions {
		allowed[reason] = true
	}
	return pairReasonsAllowed(status, reasons, allowed)
}

func pairReasonsAllowed(status PairStatus, reasons []experiment.ExclusionReason, allowed map[experiment.ExclusionReason]bool) bool {
	for _, reason := range reasons {
		if (reason == experiment.ExclusionMissingMember || reason == experiment.ExclusionDuplicateMember) &&
			(status == PairMissing || status == PairDuplicate) {
			continue
		}
		if !allowed[reason] {
			return false
		}
	}
	return true
}

func manifestStrataAndTrials(manifest experiment.Manifest) ([]string, map[string]map[string]uint32) {
	set := map[string]bool{}
	trials := map[string]map[string]uint32{}
	for _, block := range manifest.Blocks {
		set[block.StratumID] = true
		if trials[block.StratumID] == nil {
			trials[block.StratumID] = map[string]uint32{}
		}
		for _, assignment := range block.Assignments {
			trials[block.StratumID][assignment.TreatmentID]++
		}
	}
	strata := make([]string, 0, len(set))
	for stratumID := range set {
		strata = append(strata, stratumID)
	}
	sort.Strings(strata)
	return strata, trials
}

// validateAnalysisRoster owns the narrower admission required by this
// stratum-preserving consumer. Experiment manifest v1 historically admits
// repeated-attempt and inference thresholds against the aggregate block count;
// changing that durable decoder would invalidate preserved v1 bytes. Analysis
// v1 never pools strata, so it rejects a manifest whose thresholds cannot be
// evaluated inside every declared stratum before reading any trial record.
func validateAnalysisRoster(manifest experiment.Manifest) error {
	strata, trials := manifestStrataAndTrials(manifest)
	policy := manifest.AnalysisPlan.RepeatedAttempts
	maximumK := policy.K[len(policy.K)-1]
	familyExploratory := map[string]bool{}
	familySeen := map[string]bool{}
	for _, stage := range manifest.AnalysisPlan.Stages {
		exploratory := stage.Role == experiment.MetricExploratory
		if familySeen[stage.FamilySHA256] && familyExploratory[stage.FamilySHA256] != exploratory {
			return contractError(ErrorInvalidInput, errInvalidValue)
		}
		familySeen[stage.FamilySHA256], familyExploratory[stage.FamilySHA256] = true, exploratory
	}
	for _, metric := range manifest.AnalysisPlan.Metrics {
		exploratory := metric.Role == experiment.MetricExploratory
		if familySeen[metric.FamilySHA256] && familyExploratory[metric.FamilySHA256] != exploratory {
			return contractError(ErrorInvalidInput, errInvalidValue)
		}
		familySeen[metric.FamilySHA256], familyExploratory[metric.FamilySHA256] = true, exploratory
	}
	if policy.Kind == experiment.RepeatedAttemptsAll {
		outcome := false
		for _, metric := range manifest.AnalysisPlan.Metrics {
			outcome = outcome || metric.ID == experiment.MetricOutcome
		}
		if !outcome {
			return contractError(ErrorInvalidInput, errInvalidValue)
		}
	}
	for _, stratumID := range strata {
		for _, treatment := range manifest.Treatments {
			roster := trials[stratumID][treatment.ID]
			if (policy.Kind == experiment.RepeatedAttemptsAll && maximumK > roster) ||
				(manifest.Design.CompatibilityProfile == experiment.CompatibilityNone && manifest.AnalysisPlan.MinimumInferenceBlocks > roster) {
				return contractError(ErrorInvalidInput, errInvalidValue)
			}
		}
	}
	return nil
}

func reportPairsMatchManifest(report []PairCoverage, manifest []experiment.PairBinding, blocks []experiment.Block) bool {
	if len(report) != len(manifest) {
		return false
	}
	strata := make(map[string]string, len(blocks))
	for _, block := range blocks {
		strata[block.ID] = block.StratumID
	}
	expected := append([]experiment.PairBinding{}, manifest...)
	sort.Slice(expected, func(left, right int) bool { return expected[left].ID < expected[right].ID })
	for index, pair := range expected {
		actual := report[index]
		if actual.PairID != pair.ID || actual.BlockID != pair.BlockID || actual.StratumID != strata[pair.BlockID] ||
			actual.ComparisonID != pair.ComparisonID {
			return false
		}
	}
	return true
}

func reportComparisonsMatchManifest(report []ComparisonResult, manifest experiment.Manifest, strata []string) bool {
	if len(report) != len(manifest.AnalysisPlan.Comparisons)*len(strata) {
		return false
	}
	stageDeclarations := make(map[experiment.FunnelStage]experiment.StageDeclaration, len(manifest.AnalysisPlan.Stages))
	for _, declaration := range manifest.AnalysisPlan.Stages {
		stageDeclarations[declaration.Stage] = declaration
	}
	metricDeclarations := make(map[experiment.MetricID]experiment.MetricDeclaration, len(manifest.AnalysisPlan.Metrics))
	for _, declaration := range manifest.AnalysisPlan.Metrics {
		metricDeclarations[declaration.ID] = declaration
	}
	pairByComparison := make(map[string]experiment.PairBinding, len(manifest.AnalysisPlan.Comparisons))
	for _, pair := range manifest.Pairs {
		if _, exists := pairByComparison[pair.ComparisonID]; !exists {
			pairByComparison[pair.ComparisonID] = pair
		}
	}
	index := 0
	for _, comparison := range manifest.AnalysisPlan.Comparisons {
		pair, ok := pairByComparison[comparison.ID]
		if !ok {
			return false
		}
		for _, stratumID := range strata {
			actual := report[index]
			index++
			if actual.ComparisonID != comparison.ID || actual.StratumID != stratumID ||
				actual.ReferenceTreatmentID != pair.ReferenceTreatmentID || actual.CandidateTreatmentID != pair.CandidateTreatmentID ||
				!binaryBindingsMatch(actual.Binary, comparison, stageDeclarations, metricDeclarations) ||
				!continuousBindingsMatch(actual.Continuous, comparison, metricDeclarations) {
				return false
			}
		}
	}
	return true
}

func binaryBindingsMatch(results []BinaryResult, comparison experiment.Comparison,
	stages map[experiment.FunnelStage]experiment.StageDeclaration, metrics map[experiment.MetricID]experiment.MetricDeclaration,
) bool {
	expected := len(comparison.Stages)
	for _, metric := range comparison.Metrics {
		if metrics[metric].Kind == experiment.MetricBinary {
			expected++
		}
	}
	if len(results) != expected {
		return false
	}
	index := 0
	for _, stage := range comparison.Stages {
		declaration := stages[stage]
		if !binaryBindingMatches(results[index], DimensionStage, string(stage), declaration.Role, experiment.DirectionHigher, declaration.FamilySHA256) {
			return false
		}
		index++
	}
	for _, metric := range comparison.Metrics {
		declaration := metrics[metric]
		if declaration.Kind != experiment.MetricBinary {
			continue
		}
		if !binaryBindingMatches(results[index], DimensionMetric, string(metric), declaration.Role, declaration.Direction, declaration.FamilySHA256) {
			return false
		}
		index++
	}
	return true
}

func binaryBindingMatches(result BinaryResult, kind DimensionKind, id string, role experiment.MetricRole,
	direction experiment.Direction, family string,
) bool {
	return result.Kind == kind && result.ID == id && result.Role == role && result.FamilySHA256 == family && result.Direction == direction &&
		(result.ExactTest == nil || result.ExactTest.FamilySHA256 == family)
}

func continuousBindingsMatch(results []ContinuousResult, comparison experiment.Comparison,
	metrics map[experiment.MetricID]experiment.MetricDeclaration,
) bool {
	expected := 0
	for _, metric := range comparison.Metrics {
		if metrics[metric].Kind == experiment.MetricCount {
			expected++
		}
	}
	if len(results) != expected {
		return false
	}
	index := 0
	for _, metric := range comparison.Metrics {
		declaration := metrics[metric]
		if declaration.Kind != experiment.MetricCount {
			continue
		}
		result := results[index]
		if result.Metric != metric || result.Role != declaration.Role || result.FamilySHA256 != declaration.FamilySHA256 || result.Direction != declaration.Direction ||
			(result.ExactTest != nil && result.ExactTest.FamilySHA256 != declaration.FamilySHA256) {
			return false
		}
		index++
	}
	return true
}

func reportActivationMatchesManifest(results []ActivationSummary, strata []string) bool {
	if len(results) != len(strata) {
		return false
	}
	for index := range strata {
		if results[index].StratumID != strata[index] {
			return false
		}
	}
	return true
}

func reportActivationMatchesFunnels(results []ActivationSummary, funnels []TreatmentFunnel, treatments []experiment.Treatment) bool {
	expectedActivation := make(map[string]bool, len(treatments))
	for _, treatment := range treatments {
		expectedActivation[treatment.ID] = treatment.ExpectedActivation
	}
	type counts struct {
		observed, missing, truePositive, falsePositive, trueNegative, falseNegative uint32
	}
	want := map[string]counts{}
	for _, funnel := range funnels {
		var load *FunnelStage
		for index := range funnel.Stages {
			if funnel.Stages[index].Stage == experiment.StageLoad {
				load = &funnel.Stages[index]
				break
			}
		}
		if load == nil || load.Observed > funnel.Trials {
			return false
		}
		value := want[funnel.StratumID]
		value.observed += load.Observed
		value.missing += funnel.Trials - load.Observed
		if expectedActivation[funnel.TreatmentID] {
			value.truePositive += load.Reached
			value.falseNegative += load.Observed - load.Reached
		} else {
			value.falsePositive += load.Reached
			value.trueNegative += load.Observed - load.Reached
		}
		want[funnel.StratumID] = value
	}
	if len(results) != len(want) {
		return false
	}
	for _, result := range results {
		value, ok := want[result.StratumID]
		if !ok || result.Observed != value.observed || result.Missing != value.missing ||
			result.TruePositive != value.truePositive || result.FalsePositive != value.falsePositive ||
			result.TrueNegative != value.trueNegative || result.FalseNegative != value.falseNegative {
			return false
		}
	}
	return true
}

func reportIntervalsMatchManifest(ctx context.Context, manifest experiment.Manifest, report Report) error {
	for _, comparison := range report.Comparisons {
		if err := analysisContextError(ctx); err != nil {
			return err
		}
		domain := comparison.ComparisonID + "\x00" + comparison.StratumID
		for _, result := range comparison.Binary {
			if result.Status != InferenceInferential {
				continue
			}
			values := binaryPairDeltas(result.Pairs)
			want, err := bootstrapInterval(ctx, values, report.BootstrapSamples,
				report.ConfidenceBasisPoints, manifest.AnalysisPlan.BootstrapSeedSHA256,
				domain+"\x00"+string(result.Kind)+"\x00"+result.ID)
			if err != nil || result.Interval == nil || *result.Interval != want {
				return errInvalidValue
			}
		}
		for _, result := range comparison.Continuous {
			if result.Status != InferenceInferential {
				continue
			}
			values, ok := integerPairDeltas(result.Deltas)
			if !ok {
				return errInvalidValue
			}
			want, err := bootstrapInterval(ctx, values, report.BootstrapSamples,
				report.ConfidenceBasisPoints, manifest.AnalysisPlan.BootstrapSeedSHA256,
				domain+"\x00metric\x00"+string(result.Metric))
			if err != nil || result.Interval == nil || *result.Interval != want {
				return errInvalidValue
			}
		}
	}
	return nil
}

func binaryPairDeltas(pairs []BinaryPair) []int64 {
	result := make([]int64, len(pairs))
	for index, pair := range pairs {
		switch {
		case pair.Reference && !pair.Candidate:
			result[index] = -1
		case !pair.Reference && pair.Candidate:
			result[index] = 1
		}
	}
	return result
}

func integerPairDeltas(deltas []PairDelta) ([]int64, bool) {
	result := make([]int64, len(deltas))
	for index, delta := range deltas {
		value, ok := parseRational(delta.Delta)
		if !ok || value.Denom().Cmp(big.NewInt(1)) != 0 || !value.Num().IsInt64() {
			return nil, false
		}
		result[index] = value.Num().Int64()
	}
	return result, true
}

func reportFunnelsMatchManifest(results []TreatmentFunnel, treatments []experiment.Treatment, strata []string,
	trials map[string]map[string]uint32,
) bool {
	if len(results) != len(treatments)*len(strata) {
		return false
	}
	index := 0
	for _, stratumID := range strata {
		for _, treatment := range treatments {
			result := results[index]
			index++
			if result.StratumID != stratumID || result.TreatmentID != treatment.ID || result.Trials != trials[stratumID][treatment.ID] {
				return false
			}
		}
	}
	return true
}

func reportPassAtKMatchesManifest(results []PassAtKResult, treatments []experiment.Treatment, strata []string, policy experiment.RepeatedAttemptPolicy) bool {
	if policy.Kind == experiment.RepeatedAttemptsNone {
		return len(results) == 0
	}
	if len(results) != len(treatments)*len(strata)*len(policy.K) {
		return false
	}
	index := 0
	for _, stratumID := range strata {
		for _, treatment := range treatments {
			for _, value := range policy.K {
				result := results[index]
				index++
				if result.StratumID != stratumID || result.TreatmentID != treatment.ID || result.K != value {
					return false
				}
			}
		}
	}
	return true
}
