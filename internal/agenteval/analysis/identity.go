package analysis

import (
	"encoding/json"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

func reportDigest(report Report) string {
	projection := cloneReport(report)
	projection.ReportSHA256 = ""
	data, _ := json.Marshal(projection)
	return hashParts("report", data)
}

func cloneReport(input Report) Report {
	result := input
	result.Coverage.Members = make([]TrialCoverage, len(input.Coverage.Members))
	for index := range input.Coverage.Members {
		result.Coverage.Members[index] = input.Coverage.Members[index]
		result.Coverage.Members[index].Stages = append([]TrialStageProjection{}, input.Coverage.Members[index].Stages...)
		for stage := range result.Coverage.Members[index].Stages {
			cloneBoolPointer(&result.Coverage.Members[index].Stages[stage].Value)
		}
		result.Coverage.Members[index].Metrics = append([]TrialMetricProjection{}, input.Coverage.Members[index].Metrics...)
		for metric := range result.Coverage.Members[index].Metrics {
			cloneBoolPointer(&result.Coverage.Members[index].Metrics[metric].Value)
		}
	}
	result.Coverage.Pairs = make([]PairCoverage, len(input.Coverage.Pairs))
	for index := range input.Coverage.Pairs {
		result.Coverage.Pairs[index] = input.Coverage.Pairs[index]
		result.Coverage.Pairs[index].Reasons = append([]experimentExclusionReason{}, input.Coverage.Pairs[index].Reasons...)
	}
	result.Coverage.Reasons = append([]ReasonCount{}, input.Coverage.Reasons...)
	result.Comparisons = make([]ComparisonResult, len(input.Comparisons))
	for index := range input.Comparisons {
		result.Comparisons[index] = input.Comparisons[index]
		result.Comparisons[index].Binary = make([]BinaryResult, len(input.Comparisons[index].Binary))
		for dimension := range input.Comparisons[index].Binary {
			result.Comparisons[index].Binary[dimension] = input.Comparisons[index].Binary[dimension]
			result.Comparisons[index].Binary[dimension].Pairs = append([]BinaryPair{}, input.Comparisons[index].Binary[dimension].Pairs...)
			cloneBinaryPointers(&result.Comparisons[index].Binary[dimension])
		}
		result.Comparisons[index].Continuous = make([]ContinuousResult, len(input.Comparisons[index].Continuous))
		for dimension := range input.Comparisons[index].Continuous {
			result.Comparisons[index].Continuous[dimension] = input.Comparisons[index].Continuous[dimension]
			result.Comparisons[index].Continuous[dimension].Deltas = append([]PairDelta{}, input.Comparisons[index].Continuous[dimension].Deltas...)
			cloneContinuousPointers(&result.Comparisons[index].Continuous[dimension])
		}
	}
	result.Activation = make([]ActivationSummary, len(input.Activation))
	for index := range input.Activation {
		result.Activation[index] = input.Activation[index]
		cloneActivationPointers(&result.Activation[index])
	}
	result.Funnels = make([]TreatmentFunnel, len(input.Funnels))
	for index := range input.Funnels {
		result.Funnels[index] = input.Funnels[index]
		result.Funnels[index].Stages = make([]FunnelStage, len(input.Funnels[index].Stages))
		for stage := range input.Funnels[index].Stages {
			result.Funnels[index].Stages[stage] = input.Funnels[index].Stages[stage]
			cloneRationalPointer(&result.Funnels[index].Stages[stage].Rate)
			cloneRationalPointer(&result.Funnels[index].Stages[stage].Conversion)
		}
	}
	result.PassAtK = make([]PassAtKResult, len(input.PassAtK))
	for index := range input.PassAtK {
		result.PassAtK[index] = input.PassAtK[index]
		cloneRationalPointer(&result.PassAtK[index].PassAtK)
		cloneRationalPointer(&result.PassAtK[index].PassPowerK)
	}
	return result
}

// Keep the clone implementation independent of experiment internals while
// retaining the exact public alias type.
type experimentExclusionReason = experiment.ExclusionReason

func cloneBinaryPointers(result *BinaryResult) {
	if result.Interval != nil {
		value := *result.Interval
		result.Interval = &value
	}
	cloneExactTest(&result.ExactTest)
}

func cloneContinuousPointers(result *ContinuousResult) {
	if result.Interval != nil {
		value := *result.Interval
		result.Interval = &value
	}
	cloneExactTest(&result.ExactTest)
}

func cloneExactTest(test **ExactTest) {
	if *test == nil {
		return
	}
	value := **test
	cloneRationalPointer(&value.AdjustedProbability)
	if value.RejectNull != nil {
		reject := *value.RejectNull
		value.RejectNull = &reject
	}
	*test = &value
}

func cloneActivationPointers(result *ActivationSummary) {
	cloneRationalPointer(&result.Precision)
	cloneRationalPointer(&result.Recall)
	cloneRationalPointer(&result.FalseActivationRate)
	cloneRationalPointer(&result.UnnecessaryLoadRate)
}

func cloneRationalPointer(value **Rational) {
	if *value == nil {
		return
	}
	clone := **value
	*value = &clone
}

func cloneBoolPointer(value **bool) {
	if *value == nil {
		return
	}
	clone := **value
	*value = &clone
}
