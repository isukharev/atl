package analysis

import (
	"context"
	"reflect"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

func projectTrialCoverage(trialID string, records []experiment.TrialRecord, plan experiment.AnalysisPlan) TrialCoverage {
	member := TrialCoverage{
		TrialID: trialID, Records: uint32(len(records)), Exclusion: experiment.ExclusionNone, //nolint:gosec
		Stages: []TrialStageProjection{}, Metrics: []TrialMetricProjection{},
	}
	if len(records) != 1 {
		return member
	}
	member.Exclusion = records[0].Exclusion
	if member.Exclusion != experiment.ExclusionNone {
		return member
	}
	member.Stages = make([]TrialStageProjection, len(records[0].Stages))
	for index, observation := range records[0].Stages {
		member.Stages[index] = TrialStageProjection{Stage: observation.Stage, Presence: observation.Presence}
		if observation.Value != nil {
			value := *observation.Value
			member.Stages[index].Value = &value
		}
	}
	member.Metrics = make([]TrialMetricProjection, len(records[0].Metrics))
	for index, observation := range records[0].Metrics {
		member.Metrics[index] = TrialMetricProjection{Metric: observation.Metric, Presence: observation.Presence}
		if index < len(plan.Metrics) && plan.Metrics[index].Kind == experiment.MetricBinary && observation.Value != nil {
			value := *observation.Value == 1
			member.Metrics[index].Value = &value
		}
	}
	return member
}

func trialCoverageMatchesManifest(member TrialCoverage, plan experiment.AnalysisPlan) bool {
	if member.Records != 1 || member.Exclusion != experiment.ExclusionNone {
		return len(member.Stages) == 0 && len(member.Metrics) == 0
	}
	if len(member.Stages) != len(plan.Stages) || len(member.Metrics) != len(plan.Metrics) {
		return false
	}
	for index, projection := range member.Stages {
		if projection.Stage != plan.Stages[index].Stage || !validObservationPresence(projection.Presence) ||
			(projection.Presence == experiment.PresenceObserved) != (projection.Value != nil) {
			return false
		}
	}
	for index, projection := range member.Metrics {
		declaration := plan.Metrics[index]
		if projection.Metric != declaration.ID || !validObservationPresence(projection.Presence) {
			return false
		}
		if declaration.Kind == experiment.MetricBinary {
			if (projection.Presence == experiment.PresenceObserved) != (projection.Value != nil) {
				return false
			}
		} else if projection.Value != nil {
			return false
		}
	}
	return true
}

func validObservationPresence(presence experiment.Presence) bool {
	return presence == experiment.PresenceUnknown || presence == experiment.PresenceObserved ||
		presence == experiment.PresenceUnsupported || presence == experiment.PresenceNotApplicable
}

func reportObservationsFitCoverage(ctx context.Context, report Report, manifest experiment.Manifest) (bool, error) {
	members := make(map[string]TrialCoverage, len(report.Coverage.Members))
	for _, member := range report.Coverage.Members {
		members[member.TrialID] = member
	}
	pairs := make(map[string]experiment.PairBinding, len(manifest.Pairs))
	for _, pair := range manifest.Pairs {
		pairs[pair.ID] = pair
	}
	comparisons := make(map[string]experiment.Comparison, len(manifest.AnalysisPlan.Comparisons))
	for _, comparison := range manifest.AnalysisPlan.Comparisons {
		comparisons[comparison.ID] = comparison
	}
	assignments := make(map[string]map[string]string, len(manifest.Blocks))
	stratumSet := make(map[string]bool, len(manifest.Blocks))
	expectedMembers := make([]string, 0, report.Coverage.ExpectedRecords)
	for _, block := range manifest.Blocks {
		stratumSet[block.StratumID] = true
		assignments[block.ID] = make(map[string]string, len(block.Assignments))
		for _, assignment := range block.Assignments {
			assignments[block.ID][assignment.TreatmentID] = assignment.TrialID
			expectedMembers = append(expectedMembers, assignment.TrialID)
			member, ok := members[assignment.TrialID]
			if !ok || !trialCoverageMatchesManifest(member, manifest.AnalysisPlan) {
				return false, nil
			}
		}
	}
	sort.Strings(expectedMembers)
	if len(expectedMembers) != len(report.Coverage.Members) {
		return false, nil
	}
	for index, trialID := range expectedMembers {
		if report.Coverage.Members[index].TrialID != trialID {
			return false, nil
		}
	}
	for index, coverage := range report.Coverage.Pairs {
		if index&127 == 0 {
			if err := analysisContextError(ctx); err != nil {
				return false, err
			}
		}
		pair, ok := pairs[coverage.PairID]
		comparison, comparisonOK := comparisons[coverage.ComparisonID]
		if !ok || !comparisonOK || pair.ComparisonID != coverage.ComparisonID {
			return false, nil
		}
		reference := assignments[pair.BlockID][pair.ReferenceTreatmentID]
		candidate := assignments[pair.BlockID][pair.CandidateTreatmentID]
		if reference == "" || candidate == "" ||
			!pairCoverageMatchesMembers(coverage, members[reference], members[candidate], comparison) {
			return false, nil
		}
	}
	stratumIDs := make([]string, 0, len(stratumSet))
	for stratumID := range stratumSet {
		stratumIDs = append(stratumIDs, stratumID)
	}
	sort.Strings(stratumIDs)
	return projectedSummariesMatch(ctx, report, manifest, members, stratumIDs)
}

func pairCoverageMatchesMembers(pair PairCoverage, reference, candidate TrialCoverage, comparison experiment.Comparison) bool {
	reasons := make([]experiment.ExclusionReason, 0, 2)
	status := PairComplete
	switch {
	case reference.Records > 1 || candidate.Records > 1:
		status = PairDuplicate
		reasons = append(reasons, experiment.ExclusionDuplicateMember)
		if reference.Records == 0 || candidate.Records == 0 {
			reasons = append(reasons, experiment.ExclusionMissingMember)
		}
		if reference.Records == 1 && reference.Exclusion != experiment.ExclusionNone {
			reasons = append(reasons, reference.Exclusion)
		}
		if candidate.Records == 1 && candidate.Exclusion != experiment.ExclusionNone {
			reasons = append(reasons, candidate.Exclusion)
		}
	case reference.Records == 0 || candidate.Records == 0:
		status = PairMissing
		reasons = append(reasons, experiment.ExclusionMissingMember)
		if reference.Records == 1 && reference.Exclusion != experiment.ExclusionNone {
			reasons = append(reasons, reference.Exclusion)
		}
		if candidate.Records == 1 && candidate.Exclusion != experiment.ExclusionNone {
			reasons = append(reasons, candidate.Exclusion)
		}
	default:
		if reference.Exclusion != experiment.ExclusionNone {
			reasons = append(reasons, reference.Exclusion)
		}
		if candidate.Exclusion != experiment.ExclusionNone {
			reasons = append(reasons, candidate.Exclusion)
		}
		if len(reasons) == 0 {
			reasons = append(reasons, selectedProjectionReasons(comparison, reference)...)
			reasons = append(reasons, selectedProjectionReasons(comparison, candidate)...)
		}
		if len(reasons) > 0 {
			status = PairExcluded
		}
	}
	reasons = canonicalReasons(reasons)
	return pair.Status == status && reflect.DeepEqual(pair.Reasons, reasons)
}

func selectedProjectionReasons(comparison experiment.Comparison, member TrialCoverage) []experiment.ExclusionReason {
	reasons := make([]experiment.ExclusionReason, 0, 2)
	for _, selected := range comparison.Stages {
		for _, projection := range member.Stages {
			if projection.Stage == selected && projection.Presence != experiment.PresenceObserved {
				reasons = append(reasons, presenceReason(projection.Presence))
			}
		}
	}
	for _, selected := range comparison.Metrics {
		for _, projection := range member.Metrics {
			if projection.Metric == selected && projection.Presence != experiment.PresenceObserved {
				reasons = append(reasons, presenceReason(projection.Presence))
			}
		}
	}
	return reasons
}

func projectedSummariesMatch(ctx context.Context, report Report, manifest experiment.Manifest, members map[string]TrialCoverage, stratumIDs []string) (bool, error) {
	groups := make(map[string][]experiment.TrialRecord, len(members))
	for trialID, member := range members {
		if member.Records != 1 || member.Exclusion != experiment.ExclusionNone {
			groups[trialID] = make([]experiment.TrialRecord, member.Records)
			if member.Records == 1 {
				groups[trialID][0].Exclusion = member.Exclusion
			}
			continue
		}
		record := experiment.TrialRecord{Eligibility: experiment.EligibilitySupported, Exclusion: experiment.ExclusionNone}
		record.Stages = make([]experiment.StageObservation, len(member.Stages))
		for index, projection := range member.Stages {
			record.Stages[index] = experiment.StageObservation{Stage: projection.Stage, Presence: projection.Presence}
			if projection.Value != nil {
				value := *projection.Value
				record.Stages[index].Value = &value
			}
		}
		record.Metrics = make([]experiment.MetricObservation, len(member.Metrics))
		for index, projection := range member.Metrics {
			record.Metrics[index] = experiment.MetricObservation{Metric: projection.Metric, Presence: projection.Presence}
			if projection.Presence == experiment.PresenceObserved {
				value := uint64(0)
				if projection.Value != nil && *projection.Value {
					value = 1
				}
				record.Metrics[index].Value = &value
			}
		}
		groups[trialID] = []experiment.TrialRecord{record}
	}
	wantActivation, err := activationSummaries(ctx, manifest, groups, stratumIDs)
	if err != nil {
		return false, err
	}
	wantFunnels, err := comparisonsFunnels(ctx, manifest, groups, stratumIDs)
	if err != nil {
		return false, err
	}
	wantPassAtK, err := passAtKResults(ctx, manifest, groups, stratumIDs)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(report.Activation, wantActivation) && reflect.DeepEqual(report.Funnels, wantFunnels) &&
		reflect.DeepEqual(report.PassAtK, wantPassAtK), nil
}

func projectedBinaryValues(report Report) map[string]map[string]bool {
	result := make(map[string]map[string]bool, len(report.Coverage.Members))
	for _, member := range report.Coverage.Members {
		if member.Records != 1 || member.Exclusion != experiment.ExclusionNone {
			continue
		}
		values := make(map[string]bool, len(member.Stages)+len(member.Metrics))
		for _, projection := range member.Stages {
			if projection.Value != nil {
				values[string(DimensionStage)+"\x00"+string(projection.Stage)] = *projection.Value
			}
		}
		for _, projection := range member.Metrics {
			if projection.Value != nil {
				values[string(DimensionMetric)+"\x00"+string(projection.Metric)] = *projection.Value
			}
		}
		result[member.TrialID] = values
	}
	return result
}
