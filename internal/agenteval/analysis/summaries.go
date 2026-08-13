package analysis

import (
	"context"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

func activationSummaries(ctx context.Context, manifest experiment.Manifest, groups map[string][]experiment.TrialRecord, stratumIDs []string) ([]ActivationSummary, error) {
	treatments := map[string]experiment.Treatment{}
	for _, treatment := range manifest.Treatments {
		treatments[treatment.ID] = treatment
	}
	result := make([]ActivationSummary, 0, len(stratumIDs))
	for _, stratumID := range stratumIDs {
		if err := analysisContextError(ctx); err != nil {
			return nil, err
		}
		summary := ActivationSummary{StratumID: stratumID}
		for _, block := range manifest.Blocks {
			if block.StratumID != stratumID {
				continue
			}
			for _, assignment := range block.Assignments {
				records := groups[assignment.TrialID]
				if len(records) != 1 || len(trialGroupReasons(records)) != 0 {
					summary.Missing++
					continue
				}
				load, ok := observedStage(records[0], experiment.StageLoad)
				if !ok {
					summary.Missing++
					continue
				}
				summary.Observed++
				expected := treatments[assignment.TreatmentID].ExpectedActivation
				switch {
				case expected && load:
					summary.TruePositive++
				case !expected && load:
					summary.FalsePositive++
				case !expected && !load:
					summary.TrueNegative++
				case expected && !load:
					summary.FalseNegative++
				}
			}
		}
		if denominator := summary.TruePositive + summary.FalsePositive; denominator > 0 {
			value := rationalFromUint64(uint64(summary.TruePositive), uint64(denominator))
			summary.Precision = &value
		}
		if denominator := summary.TruePositive + summary.FalseNegative; denominator > 0 {
			value := rationalFromUint64(uint64(summary.TruePositive), uint64(denominator))
			summary.Recall = &value
		}
		if denominator := summary.FalsePositive + summary.TrueNegative; denominator > 0 {
			value := rationalFromUint64(uint64(summary.FalsePositive), uint64(denominator))
			summary.FalseActivationRate = &value
		}
		if denominator := summary.TruePositive + summary.FalsePositive; denominator > 0 {
			value := rationalFromUint64(uint64(summary.FalsePositive), uint64(denominator))
			summary.UnnecessaryLoadRate = &value
		}
		result = append(result, summary)
	}
	return result, nil
}

func comparisonsFunnels(ctx context.Context, manifest experiment.Manifest, groups map[string][]experiment.TrialRecord, stratumIDs []string) ([]TreatmentFunnel, error) {
	result := make([]TreatmentFunnel, 0, len(manifest.Treatments)*len(stratumIDs))
	for _, stratumID := range stratumIDs {
		for _, treatment := range manifest.Treatments {
			if err := analysisContextError(ctx); err != nil {
				return nil, err
			}
			records := make([]experiment.TrialRecord, 0, len(manifest.Blocks))
			planned := uint32(0)
			for _, block := range manifest.Blocks {
				if block.StratumID != stratumID {
					continue
				}
				for _, assignment := range block.Assignments {
					if assignment.TreatmentID != treatment.ID {
						continue
					}
					planned++
					group := groups[assignment.TrialID]
					if len(group) == 1 && len(trialGroupReasons(group)) == 0 {
						records = append(records, group[0])
					}
				}
			}
			funnel := TreatmentFunnel{StratumID: stratumID, TreatmentID: treatment.ID, Trials: planned, Stages: make([]FunnelStage, 0, len(manifest.AnalysisPlan.Stages))}
			for index, declaration := range manifest.AnalysisPlan.Stages {
				stage := FunnelStage{Stage: declaration.Stage}
				for _, record := range records {
					current, currentObserved := observedStage(record, declaration.Stage)
					if currentObserved {
						stage.Observed++
						if current {
							stage.Reached++
						}
					}
					if index == 0 {
						if currentObserved {
							stage.EligibleTransitions++
							if current {
								stage.Converted++
							}
						}
						continue
					}
					previous, previousObserved := observedStage(record, manifest.AnalysisPlan.Stages[index-1].Stage)
					if previousObserved && previous && currentObserved {
						stage.EligibleTransitions++
						if current {
							stage.Converted++
						}
					}
				}
				if stage.Observed > 0 {
					stage.Rate = pointer(rationalFromUint64(uint64(stage.Reached), uint64(stage.Observed)))
				}
				if stage.EligibleTransitions > 0 {
					stage.Conversion = pointer(rationalFromUint64(uint64(stage.Converted), uint64(stage.EligibleTransitions)))
				}
				funnel.Stages = append(funnel.Stages, stage)
			}
			result = append(result, funnel)
		}
	}
	return result, nil
}

func passAtKResults(ctx context.Context, manifest experiment.Manifest, groups map[string][]experiment.TrialRecord, stratumIDs []string) ([]PassAtKResult, error) {
	if manifest.AnalysisPlan.RepeatedAttempts.Kind == experiment.RepeatedAttemptsNone {
		return []PassAtKResult{}, nil
	}
	result := make([]PassAtKResult, 0, len(manifest.Treatments)*len(stratumIDs)*len(manifest.AnalysisPlan.RepeatedAttempts.K))
	for _, stratumID := range stratumIDs {
		for _, treatment := range manifest.Treatments {
			if err := analysisContextError(ctx); err != nil {
				return nil, err
			}
			planned, attempts, passed := uint32(0), uint32(0), uint32(0)
			complete := true
			for _, block := range manifest.Blocks {
				if block.StratumID != stratumID {
					continue
				}
				for _, assignment := range block.Assignments {
					if assignment.TreatmentID != treatment.ID {
						continue
					}
					planned++
					group := groups[assignment.TrialID]
					if len(group) != 1 || len(trialGroupReasons(group)) != 0 {
						complete = false
						continue
					}
					value, ok := observedMetric(group[0], experiment.MetricOutcome)
					if !ok || value > 1 {
						complete = false
						continue
					}
					attempts++
					passed += uint32(value)
				}
			}
			if attempts != planned {
				complete = false
			}
			for _, k := range manifest.AnalysisPlan.RepeatedAttempts.K {
				entry := PassAtKResult{StratumID: stratumID, TreatmentID: treatment.ID, K: k, Attempts: attempts, Passed: passed, Status: InferenceInsufficient}
				if complete {
					pass, power, ok := passEstimators(attempts, passed, k)
					if ok {
						entry.PassAtK, entry.PassPowerK = &pass, &power
						entry.Status = inferenceStatus(attempts, manifest.AnalysisPlan.MinimumInferenceBlocks)
					}
				}
				result = append(result, entry)
			}
		}
	}
	return result, nil
}

func observedStage(record experiment.TrialRecord, stage experiment.FunnelStage) (bool, bool) {
	for _, value := range record.Stages {
		if value.Stage == stage {
			return value.Value != nil && *value.Value, value.Presence == experiment.PresenceObserved && value.Value != nil
		}
	}
	return false, false
}

func observedMetric(record experiment.TrialRecord, metric experiment.MetricID) (uint64, bool) {
	for _, value := range record.Metrics {
		if value.Metric == metric {
			if value.Presence == experiment.PresenceObserved && value.Value != nil {
				return *value.Value, true
			}
			return 0, false
		}
	}
	return 0, false
}
