package agentskills

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type decodedWorkspaceBenchmark struct {
	view            BenchmarkView
	metadata        WorkspaceMetadata
	feedback        []FeedbackEntry
	feedbackPresent bool
	precisionLoss   uint32
}

func decodeWorkspaceBenchmark(data []byte, format Format, baseline Baseline) (decodedWorkspaceBenchmark, error) {
	root, err := decodeBoundedJSONObject(data, ErrorInvalidWorkspace)
	if err != nil {
		return decodedWorkspaceBenchmark{}, err
	}
	baselineTreatment := TreatmentNoSkill
	if baseline == BaselinePreviousSkill {
		baselineTreatment = TreatmentPreviousSkill
	}
	if format == FormatAgentSkillsGuideV1 {
		if err := requireJSONMembers(root, []string{"run_summary"}, []string{"feedback"}); err != nil {
			return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, err)
		}
		if err := validateRunSummary(root["run_summary"], format, baselineTreatment); err != nil {
			return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, err)
		}
		result := decodedWorkspaceBenchmark{}
		if raw, ok := root["feedback"]; ok {
			result.feedback, err = decodeFeedback(raw)
			if err != nil {
				return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, err)
			}
			result.feedbackPresent = true
		}
		return result, nil
	}
	if format != FormatAnthropicSkillCreatorV1 {
		return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, nil)
	}
	if err := requireJSONMembers(root, []string{"metadata", "run_summary", "runs", "notes"}, nil); err != nil {
		return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, err)
	}
	if err := validateRunSummary(root["run_summary"], format, baselineTreatment); err != nil {
		return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, err)
	}
	metadata, evalIDs, runsPer, err := decodeBenchmarkMetadata(root["metadata"])
	if err != nil {
		return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, err)
	}
	notes, err := decodeStringArray(root["notes"], MaxNotes, MaxTextBytes)
	if err != nil {
		return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, err)
	}
	var rawRuns []json.RawMessage
	if err := json.Unmarshal(root["runs"], &rawRuns); err != nil || len(rawRuns) == 0 || len(rawRuns) > MaxRuns {
		return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, fmt.Errorf("benchmark runs"))
	}
	runs := make([]BenchmarkRun, 0, len(rawRuns))
	precisionLoss := uint32(0)
	for _, raw := range rawRuns {
		run, lost, err := decodeBenchmarkRun(raw)
		if err != nil {
			return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, err)
		}
		if lost {
			precisionLoss++
		}
		runs = append(runs, run)
	}
	view := BenchmarkView{
		SkillName: metadata.SkillName, Runs: runs,
		NotesPresent: true, Notes: notes,
	}
	if metadata.ExecutorModelPresent {
		view.ExecutorModel = metadata.ExecutorModel
	}
	if metadata.AnalyzerModelPresent {
		view.AnalyzerModel = metadata.AnalyzerModel
	}
	if metadata.TimestampPresent {
		view.Timestamp = metadata.Timestamp
	}
	_, actualRunsPer, actualBaseline, err := validateAndSortBenchmark(view)
	if err != nil || actualRunsPer != runsPer || actualBaseline != baselineTreatment || !sameCaseIDs(evalIDs, runs) {
		return decodedWorkspaceBenchmark{}, contractError(ErrorInvalidWorkspace, fmt.Errorf("benchmark pairing"))
	}
	return decodedWorkspaceBenchmark{view: view, metadata: metadata, precisionLoss: precisionLoss}, nil
}

func decodeBenchmarkMetadata(raw json.RawMessage) (WorkspaceMetadata, []uint32, uint32, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return WorkspaceMetadata{}, nil, 0, fmt.Errorf("benchmark metadata")
	}
	if err := requireJSONMembers(object, []string{"skill_name", "evals_run", "runs_per_configuration"}, []string{
		"skill_path", "executor_model", "analyzer_model", "timestamp",
	}); err != nil {
		return WorkspaceMetadata{}, nil, 0, err
	}
	name, err := decodeJSONString(object["skill_name"], 64, false)
	if err != nil || !validSkillName(name) {
		return WorkspaceMetadata{}, nil, 0, fmt.Errorf("benchmark skill name")
	}
	var rawIDs []json.RawMessage
	if err := json.Unmarshal(object["evals_run"], &rawIDs); err != nil || len(rawIDs) == 0 || len(rawIDs) > MaxCases {
		return WorkspaceMetadata{}, nil, 0, fmt.Errorf("benchmark eval ids")
	}
	ids := make([]uint32, 0, len(rawIDs))
	seen := make(map[uint32]struct{}, len(rawIDs))
	for _, rawID := range rawIDs {
		id, err := decodeJSONUint32(rawID)
		if err != nil {
			return WorkspaceMetadata{}, nil, 0, err
		}
		if _, duplicate := seen[id]; duplicate {
			return WorkspaceMetadata{}, nil, 0, fmt.Errorf("duplicate benchmark eval id")
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	runsPer, err := decodeJSONUint32(object["runs_per_configuration"])
	if err != nil || runsPer == 0 || runsPer > MaxAttempts {
		return WorkspaceMetadata{}, nil, 0, fmt.Errorf("benchmark repetitions")
	}
	metadata := WorkspaceMetadata{Present: true, SkillName: name}
	if rawPath, ok := object["skill_path"]; ok {
		metadata.SkillPath, err = decodeJSONString(rawPath, MaxPathBytes, false)
		if err != nil {
			return WorkspaceMetadata{}, nil, 0, err
		}
		metadata.SkillPathPresent = true
	}
	if rawModel, ok := object["executor_model"]; ok {
		metadata.ExecutorModel, err = decodeJSONString(rawModel, 256, false)
		if err != nil {
			return WorkspaceMetadata{}, nil, 0, err
		}
		metadata.ExecutorModelPresent = true
	}
	if rawModel, ok := object["analyzer_model"]; ok {
		metadata.AnalyzerModel, err = decodeJSONString(rawModel, 256, false)
		if err != nil {
			return WorkspaceMetadata{}, nil, 0, err
		}
		metadata.AnalyzerModelPresent = true
	}
	if rawTimestamp, ok := object["timestamp"]; ok {
		metadata.Timestamp, err = decodeJSONString(rawTimestamp, 128, false)
		if err != nil {
			return WorkspaceMetadata{}, nil, 0, err
		}
		if _, err := time.Parse(time.RFC3339, metadata.Timestamp); err != nil {
			return WorkspaceMetadata{}, nil, 0, fmt.Errorf("benchmark timestamp")
		}
		metadata.TimestampPresent = true
	}
	return metadata, ids, runsPer, nil
}

func decodeBenchmarkRun(raw json.RawMessage) (BenchmarkRun, bool, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return BenchmarkRun{}, false, fmt.Errorf("benchmark run")
	}
	if err := requireJSONMembers(object, []string{
		"eval_id", "eval_name", "configuration", "run_number", "result", "expectations", "notes",
	}, nil); err != nil {
		return BenchmarkRun{}, false, err
	}
	caseID, err := decodeJSONUint32(object["eval_id"])
	if err != nil {
		return BenchmarkRun{}, false, err
	}
	evalName, err := decodeJSONString(object["eval_name"], MaxTextBytes, false)
	if err != nil {
		return BenchmarkRun{}, false, err
	}
	configurationName, err := decodeJSONString(object["configuration"], 32, false)
	if err != nil {
		return BenchmarkRun{}, false, err
	}
	configuration, ok := treatmentFromConfiguration(configurationName)
	if !ok {
		return BenchmarkRun{}, false, fmt.Errorf("benchmark configuration")
	}
	runNumber, err := decodeJSONUint32(object["run_number"])
	if err != nil || runNumber == 0 || runNumber > MaxAttempts {
		return BenchmarkRun{}, false, fmt.Errorf("benchmark run number")
	}
	results, passed, err := decodeGradeResults(object["expectations"], "text")
	if err != nil {
		return BenchmarkRun{}, false, err
	}
	grading, precisionLoss, err := decodeBenchmarkResult(object["result"], countSlice(results), passed)
	if err != nil {
		return BenchmarkRun{}, false, err
	}
	grading.Results = results
	notes, err := decodeStringArray(object["notes"], MaxNotes, MaxTextBytes)
	if err != nil {
		return BenchmarkRun{}, false, err
	}
	return BenchmarkRun{
		CaseID: caseID, EvalName: evalName, Configuration: configuration, RunNumber: runNumber,
		Grading: grading, NotesPresent: true, Notes: notes,
	}, precisionLoss, nil
}

func decodeBenchmarkResult(raw json.RawMessage, total, passed uint32) (GradingView, bool, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return GradingView{}, false, fmt.Errorf("benchmark result")
	}
	if err := requireJSONMembers(object, []string{"passed", "failed", "total", "pass_rate"}, []string{"time_seconds", "tokens"}); err != nil {
		return GradingView{}, false, err
	}
	summary, err := json.Marshal(map[string]json.RawMessage{
		"passed": object["passed"], "failed": object["failed"], "total": object["total"], "pass_rate": object["pass_rate"],
	})
	if err != nil || validateGradeSummary(summary, total, passed) != nil {
		return GradingView{}, false, fmt.Errorf("benchmark result summary")
	}
	view := GradingView{
		DurationMillis:        OptionalUint64{Presence: MetricUnknown},
		TotalTokens:           OptionalUint64{Presence: MetricUnknown},
		EstimatedCostMicroUSD: OptionalUint64{Presence: MetricUnsupported},
	}
	precisionLoss := false
	if rawDuration, ok := object["time_seconds"]; ok {
		view.DurationMillis, precisionLoss, err = decodeOptionalDuration(rawDuration)
		if err != nil {
			return GradingView{}, false, err
		}
	}
	if rawTokens, ok := object["tokens"]; ok {
		value, err := decodeJSONUint64(rawTokens)
		if err != nil {
			return GradingView{}, false, err
		}
		view.TotalTokens = OptionalUint64{Presence: MetricObserved, Value: value}
	}
	return view, precisionLoss, nil
}

func validateRunSummary(raw json.RawMessage, format Format, baseline TreatmentKind) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return fmt.Errorf("run summary")
	}
	currentName, baselineName := configurationName(TreatmentCurrentSkill), configurationName(baseline)
	if err := requireJSONMembers(object, []string{currentName, baselineName, "delta"}, nil); err != nil {
		return err
	}
	for _, name := range []string{currentName, baselineName} {
		var configuration map[string]json.RawMessage
		if err := json.Unmarshal(object[name], &configuration); err != nil || configuration == nil {
			return fmt.Errorf("run summary configuration")
		}
		if err := requireJSONMembers(configuration, []string{"pass_rate"}, []string{"time_seconds", "tokens"}); err != nil {
			return err
		}
		for _, rawStats := range configuration {
			if err := validateStatistics(rawStats, format); err != nil {
				return err
			}
		}
	}
	var delta map[string]json.RawMessage
	if err := json.Unmarshal(object["delta"], &delta); err != nil || delta == nil {
		return fmt.Errorf("run summary delta")
	}
	if err := requireJSONMembers(delta, nil, []string{"pass_rate", "time_seconds", "tokens"}); err != nil {
		return err
	}
	for _, value := range delta {
		if format == FormatAgentSkillsGuideV1 {
			if _, err := decodeJSONNumber(value); err != nil {
				return err
			}
		} else if _, err := decodeJSONString(value, 64, false); err != nil {
			return err
		}
	}
	return nil
}

func validateStatistics(raw json.RawMessage, format Format) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return fmt.Errorf("statistics")
	}
	required := []string{"mean", "stddev"}
	if format == FormatAnthropicSkillCreatorV1 {
		required = append(required, "min", "max")
	}
	if err := requireJSONMembers(object, required, nil); err != nil {
		return err
	}
	for _, value := range object {
		if _, err := decodeJSONNumber(value); err != nil {
			return err
		}
	}
	return nil
}

func treatmentFromConfiguration(value string) (TreatmentKind, bool) {
	switch value {
	case "with_skill":
		return TreatmentCurrentSkill, true
	case "without_skill":
		return TreatmentNoSkill, true
	case "old_skill":
		return TreatmentPreviousSkill, true
	default:
		return "", false
	}
}

func sameCaseIDs(expected []uint32, runs []BenchmarkRun) bool {
	actualSet := make(map[uint32]struct{})
	for _, run := range runs {
		actualSet[run.CaseID] = struct{}{}
	}
	actual := make([]uint32, 0, len(actualSet))
	for id := range actualSet {
		actual = append(actual, id)
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i] < actual[j] })
	expected = append([]uint32(nil), expected...)
	sort.Slice(expected, func(i, j int) bool { return expected[i] < expected[j] })
	if len(actual) != len(expected) {
		return false
	}
	for index := range actual {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}
