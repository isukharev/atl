package agentskills

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"
)

// EncodeGrading emits one upstream-compatible grading view and a complete
// missingness/loss report. It never emits an evaluator receipt.
func EncodeGrading(format Format, view GradingView) (CompatibilityArtifact, error) {
	return encodeGrading(format, view, false)
}

func encodeGrading(format Format, view GradingView, verifierCoverageBound bool) (CompatibilityArtifact, error) {
	if format != FormatAgentSkillsGuideV1 && format != FormatAnthropicSkillCreatorV1 {
		return CompatibilityArtifact{}, contractError(ErrorInvalidExport, nil)
	}
	if err := validateGradingView(view); err != nil {
		return CompatibilityArtifact{}, contractError(ErrorInvalidExport, err)
	}
	results := make([]map[string]any, len(view.Results))
	passed := uint32(0)
	for index, result := range view.Results {
		textField := "text"
		if format == FormatAgentSkillsGuideV1 {
			textField = "assertion"
		}
		results[index] = map[string]any{textField: result.Text, "passed": result.Passed, "evidence": result.Evidence}
		if result.Passed {
			passed++
		}
	}
	total := countSlice(view.Results)
	passRate := 0.0
	if total > 0 {
		passRate = float64(passed) / float64(total)
	}
	document := map[string]any{
		"summary": map[string]any{
			"passed": passed, "failed": total - passed, "total": total, "pass_rate": passRate,
		},
	}
	var report reportAccumulator
	report.add(ReportActivationUnbound, "execution.skill_activation", DispositionUnsupported, true)
	if !verifierCoverageBound {
		report.add(ReportVerifierCoverageUnbound, "verification.criteria_coverage", DispositionUnsupported, true)
	}
	if format == FormatAgentSkillsGuideV1 {
		document["assertion_results"] = results
		if view.FeedbackPresent {
			document["feedback"] = encodeFeedback(view.Feedback)
		}
		reportMetricForUnrepresented(&report, "grading.duration_millis", view.DurationMillis)
	} else {
		document["expectations"] = results
		if view.FeedbackPresent {
			report.add(ReportReviewContentPreserved, "grading.feedback", DispositionPreservedSourceOnly, false)
		}
		if view.DurationMillis.Presence == MetricObserved {
			document["timing"] = map[string]any{
				"total_duration_seconds": json.Number(millisecondsAsSeconds(view.DurationMillis.Value)),
			}
		} else {
			reportMetricAbsence(&report, "grading.duration_millis", view.DurationMillis)
		}
	}
	reportMetricForUnrepresented(&report, "grading.total_tokens", view.TotalTokens)
	reportMetricForUnrepresented(&report, "grading.estimated_cost_microusd", normalizedEstimatedCost(view.EstimatedCostMicroUSD))
	data, err := marshalCompatibilityJSON(document)
	if err != nil {
		return CompatibilityArtifact{}, contractError(ErrorInvalidExport, err)
	}
	return CompatibilityArtifact{Format: format, Kind: "grading", Data: data, Report: report.report()}, nil
}

// EncodeBenchmark emits a deterministic guide or Anthropic benchmark view.
// Unknown metrics are omitted and reported; observed zero values are emitted.
func EncodeBenchmark(format Format, view BenchmarkView) (CompatibilityArtifact, error) {
	return encodeBenchmark(format, view, false)
}

func encodeBenchmark(format Format, view BenchmarkView, verifierCoverageBound bool) (CompatibilityArtifact, error) {
	if format != FormatAgentSkillsGuideV1 && format != FormatAnthropicSkillCreatorV1 {
		return CompatibilityArtifact{}, contractError(ErrorInvalidExport, nil)
	}
	if format == FormatAnthropicSkillCreatorV1 && !anthropicBenchmarkWithinEncodingBound(view) {
		return CompatibilityArtifact{}, contractError(ErrorInvalidExport, fmt.Errorf("benchmark encoding bound"))
	}
	runs, runsPerConfiguration, baseline, err := validateAndSortBenchmark(view)
	if err != nil {
		return CompatibilityArtifact{}, contractError(ErrorInvalidExport, err)
	}
	if format == FormatAgentSkillsGuideV1 {
		for _, run := range runs {
			if run.EvalName != "" {
				return CompatibilityArtifact{}, contractError(ErrorInvalidExport, fmt.Errorf("guide eval name"))
			}
		}
	}
	var report reportAccumulator
	report.add(ReportActivationUnbound, "execution.skill_activation", DispositionUnsupported, true)
	if !verifierCoverageBound {
		report.add(ReportVerifierCoverageUnbound, "verification.criteria_coverage", DispositionUnsupported, true)
	}
	for _, run := range runs {
		reportMetricForUnrepresented(&report, "benchmark.runs[].estimated_cost_microusd",
			normalizedEstimatedCost(run.Grading.EstimatedCostMicroUSD))
		if run.Grading.FeedbackPresent {
			report.add(ReportReviewContentPreserved, "benchmark.runs[].grading.feedback", DispositionPreservedSourceOnly, false)
		}
	}
	summary := buildRunSummary(format, runs, baseline, &report)
	document := map[string]any{"run_summary": summary}
	if format == FormatAnthropicSkillCreatorV1 {
		evalIDs := make([]uint32, 0)
		seen := make(map[uint32]struct{})
		encodedRuns := make([]map[string]any, 0, len(runs))
		for _, run := range runs {
			if _, ok := seen[run.CaseID]; !ok {
				seen[run.CaseID] = struct{}{}
				evalIDs = append(evalIDs, run.CaseID)
			}
			encodedRuns = append(encodedRuns, encodeAnthropicRun(run, &report))
		}
		metadata := map[string]any{
			"skill_name": view.SkillName, "evals_run": evalIDs,
			"runs_per_configuration": runsPerConfiguration,
		}
		if view.ExecutorModel != "" {
			metadata["executor_model"] = view.ExecutorModel
		} else {
			report.add(ReportModelMetadataOmitted, "benchmark.metadata.executor_model", DispositionOmitted, false)
		}
		if view.AnalyzerModel != "" {
			metadata["analyzer_model"] = view.AnalyzerModel
		} else {
			report.add(ReportModelMetadataOmitted, "benchmark.metadata.analyzer_model", DispositionOmitted, false)
		}
		if view.Timestamp != "" {
			metadata["timestamp"] = view.Timestamp
		}
		report.add(ReportPathOmitted, "benchmark.metadata.skill_path", DispositionOmitted, false)
		document["metadata"] = metadata
		document["runs"] = encodedRuns
		document["notes"] = encodeNotes(view.Notes)
		if view.FeedbackPresent {
			report.add(ReportReviewContentPreserved, "benchmark.feedback", DispositionPreservedSourceOnly, false)
		}
	} else {
		if view.FeedbackPresent {
			document["feedback"] = encodeFeedback(view.Feedback)
		}
		if view.NotesPresent {
			report.add(ReportReviewContentPreserved, "benchmark.notes", DispositionPreservedSourceOnly, false)
		}
		report.add(ReportSkillMetadataOmitted, "benchmark.skill_name", DispositionPreservedSourceOnly, false)
		if view.ExecutorModel != "" {
			report.add(ReportModelMetadataOmitted, "benchmark.executor_model", DispositionPreservedSourceOnly, false)
		}
		if view.AnalyzerModel != "" {
			report.add(ReportModelMetadataOmitted, "benchmark.analyzer_model", DispositionPreservedSourceOnly, false)
		}
		if view.Timestamp != "" {
			report.add(ReportTimestampOmitted, "benchmark.timestamp", DispositionPreservedSourceOnly, false)
		}
		report.addCount(ReportRunDetailsOmitted, "benchmark.runs", DispositionPreservedSourceOnly, false, countSlice(runs))
	}
	data, err := marshalCompatibilityJSON(document)
	if err != nil {
		return CompatibilityArtifact{}, contractError(ErrorInvalidExport, err)
	}
	return CompatibilityArtifact{Format: format, Kind: "benchmark", Data: data, Report: report.report()}, nil
}

func validateGradingView(view GradingView) error {
	if len(view.Results) == 0 || len(view.Results) > MaxCriteriaPerCase+1 ||
		!validOptionalUint64(view.DurationMillis) || !validOptionalUint64(view.TotalTokens) ||
		!validEstimatedCost(view.EstimatedCostMicroUSD) ||
		!validFeedback(view.FeedbackPresent, view.Feedback) {
		return fmt.Errorf("grading contract")
	}
	for _, result := range view.Results {
		if result.Text == "" || len(result.Text) > MaxTextBytes || len(result.Evidence) > MaxTextBytes ||
			!utf8.ValidString(result.Text) || !utf8.ValidString(result.Evidence) {
			return fmt.Errorf("grading result")
		}
	}
	return nil
}

func validEstimatedCost(metric OptionalUint64) bool {
	return metric == (OptionalUint64{}) || validOptionalUint64(metric)
}

func normalizedEstimatedCost(metric OptionalUint64) OptionalUint64 {
	if metric == (OptionalUint64{}) {
		return OptionalUint64{Presence: MetricUnknown}
	}
	return metric
}

func validOptionalUint64(metric OptionalUint64) bool {
	switch metric.Presence {
	case MetricObserved:
		return true
	case MetricUnknown, MetricUnsupported, MetricNotApplicable:
		return metric.Value == 0
	default:
		return false
	}
}

func validFeedback(present bool, entries []FeedbackEntry) bool {
	if !present {
		return len(entries) == 0
	}
	if len(entries) > MaxFeedbackEntries {
		return false
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Key == "" || len(entry.Key) > 128 || len(entry.Value) > MaxTextBytes ||
			!utf8.ValidString(entry.Key) || !utf8.ValidString(entry.Value) {
			return false
		}
		if _, duplicate := seen[entry.Key]; duplicate {
			return false
		}
		seen[entry.Key] = struct{}{}
	}
	return true
}

func encodeFeedback(entries []FeedbackEntry) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		result[entry.Key] = entry.Value
	}
	return result
}

func validNotes(present bool, notes []string) bool {
	if !present {
		return len(notes) == 0
	}
	if len(notes) > MaxNotes {
		return false
	}
	for _, note := range notes {
		if note == "" || len(note) > MaxTextBytes || !utf8.ValidString(note) {
			return false
		}
	}
	return true
}

func reportMetricForUnrepresented(report *reportAccumulator, scope string, metric OptionalUint64) {
	switch metric.Presence {
	case MetricObserved:
		report.add(ReportMetricUnsupported, scope, DispositionPreservedSourceOnly, false)
	case MetricUnknown:
		report.add(ReportMetricUnknown, scope, DispositionOmitted, false)
	case MetricUnsupported:
		report.add(ReportMetricUnsupported, scope, DispositionUnsupported, false)
	case MetricNotApplicable:
		report.add(ReportMetricNotApplicable, scope, DispositionOmitted, false)
	}
}

func reportMetricAbsence(report *reportAccumulator, scope string, metric OptionalUint64) {
	switch metric.Presence {
	case MetricUnknown:
		report.add(ReportMetricUnknown, scope, DispositionOmitted, false)
	case MetricUnsupported:
		report.add(ReportMetricUnsupported, scope, DispositionUnsupported, false)
	case MetricNotApplicable:
		report.add(ReportMetricNotApplicable, scope, DispositionOmitted, false)
	}
}

func millisecondsAsSeconds(milliseconds uint64) string {
	seconds := milliseconds / 1000
	remainder := milliseconds % 1000
	if remainder == 0 {
		return strconv.FormatUint(seconds, 10)
	}
	fraction := fmt.Sprintf("%03d", remainder)
	fraction = stringTrimRight(fraction, '0')
	return strconv.FormatUint(seconds, 10) + "." + fraction
}

func stringTrimRight(value string, character byte) string {
	for len(value) > 0 && value[len(value)-1] == character {
		value = value[:len(value)-1]
	}
	return value
}

func validateAndSortBenchmark(view BenchmarkView) ([]BenchmarkRun, uint32, TreatmentKind, error) {
	if !validSkillName(view.SkillName) || len(view.Runs) == 0 || len(view.Runs) > MaxRuns ||
		len(view.ExecutorModel) > 256 || len(view.AnalyzerModel) > 256 ||
		!utf8.ValidString(view.ExecutorModel) || !utf8.ValidString(view.AnalyzerModel) ||
		!validFeedback(view.FeedbackPresent, view.Feedback) || !validNotes(view.NotesPresent, view.Notes) {
		return nil, 0, "", fmt.Errorf("benchmark contract")
	}
	if view.Timestamp != "" {
		if _, err := time.Parse(time.RFC3339, view.Timestamp); err != nil {
			return nil, 0, "", fmt.Errorf("benchmark timestamp")
		}
	}
	runs := append([]BenchmarkRun(nil), view.Runs...)
	seen := make(map[string]struct{}, len(runs))
	baseline := TreatmentKind("")
	cellRuns := make(map[string]map[uint32]struct{})
	caseTreatments := make(map[uint32]map[TreatmentKind]struct{})
	for _, run := range runs {
		if run.Configuration != TreatmentCurrentSkill && run.Configuration != TreatmentNoSkill && run.Configuration != TreatmentPreviousSkill ||
			run.RunNumber == 0 || run.RunNumber > MaxAttempts || validateGradingView(run.Grading) != nil ||
			len(run.EvalName) > MaxTextBytes || !utf8.ValidString(run.EvalName) || !validNotes(run.NotesPresent, run.Notes) {
			return nil, 0, "", fmt.Errorf("benchmark run")
		}
		if run.Configuration != TreatmentCurrentSkill {
			if baseline != "" && baseline != run.Configuration {
				return nil, 0, "", fmt.Errorf("mixed baselines")
			}
			baseline = run.Configuration
		}
		identity := fmt.Sprintf("%d/%s/%d", run.CaseID, run.Configuration, run.RunNumber)
		if _, duplicate := seen[identity]; duplicate {
			return nil, 0, "", fmt.Errorf("duplicate benchmark run")
		}
		seen[identity] = struct{}{}
		cell := fmt.Sprintf("%d/%s", run.CaseID, run.Configuration)
		if cellRuns[cell] == nil {
			cellRuns[cell] = make(map[uint32]struct{})
		}
		cellRuns[cell][run.RunNumber] = struct{}{}
		if caseTreatments[run.CaseID] == nil {
			caseTreatments[run.CaseID] = make(map[TreatmentKind]struct{})
		}
		caseTreatments[run.CaseID][run.Configuration] = struct{}{}
	}
	if len(caseTreatments) > MaxCases {
		return nil, 0, "", fmt.Errorf("benchmark case bound")
	}
	if baseline == "" {
		return nil, 0, "", fmt.Errorf("baseline missing")
	}
	var runsPer uint32
	for caseID, treatments := range caseTreatments {
		if len(treatments) != 2 {
			return nil, 0, "", fmt.Errorf("incomplete case")
		}
		current := cellRuns[fmt.Sprintf("%d/%s", caseID, TreatmentCurrentSkill)]
		comparison := cellRuns[fmt.Sprintf("%d/%s", caseID, baseline)]
		if !equalRunNumbers(current, comparison) {
			return nil, 0, "", fmt.Errorf("unpaired runs")
		}
		currentCount := countMap(current)
		if runsPer == 0 {
			runsPer = currentCount
		} else if runsPer != currentCount {
			return nil, 0, "", fmt.Errorf("unequal repetitions")
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CaseID != runs[j].CaseID {
			return runs[i].CaseID < runs[j].CaseID
		}
		if runs[i].Configuration != runs[j].Configuration {
			return runs[i].Configuration == TreatmentCurrentSkill
		}
		return runs[i].RunNumber < runs[j].RunNumber
	})
	return runs, runsPer, baseline, nil
}

func equalRunNumbers(first, second map[uint32]struct{}) bool {
	if len(first) == 0 || len(first) != len(second) {
		return false
	}
	for value, count := uint32(1), countMap(first); value <= count; value++ {
		if _, ok := first[value]; !ok {
			return false
		}
		if _, ok := second[value]; !ok {
			return false
		}
	}
	return true
}

func buildRunSummary(format Format, runs []BenchmarkRun, baseline TreatmentKind, report *reportAccumulator) map[string]any {
	byConfiguration := map[TreatmentKind][]BenchmarkRun{}
	for _, run := range runs {
		byConfiguration[run.Configuration] = append(byConfiguration[run.Configuration], run)
	}
	result := make(map[string]any)
	for _, configuration := range []TreatmentKind{TreatmentCurrentSkill, baseline} {
		rows := byConfiguration[configuration]
		passRates := make([]float64, len(rows))
		for index, row := range rows {
			passRates[index] = gradingPassRate(row.Grading)
		}
		entry := map[string]any{"pass_rate": encodeStats(format, calculateStats(passRates))}
		if values, complete := observedMetricValues(rows, true, report); complete {
			entry["time_seconds"] = encodeStats(format, calculateStats(values))
		}
		if values, complete := observedMetricValues(rows, false, report); complete {
			entry["tokens"] = encodeStats(format, calculateStats(values))
		}
		result[configurationName(configuration)] = entry
	}
	delta := map[string]any{}
	current := result[configurationName(TreatmentCurrentSkill)].(map[string]any)
	comparison := result[configurationName(baseline)].(map[string]any)
	for _, metric := range []string{"pass_rate", "time_seconds", "tokens"} {
		left, leftOK := statsMean(current[metric])
		right, rightOK := statsMean(comparison[metric])
		if !leftOK || !rightOK {
			continue
		}
		value := left - right
		if format == FormatAnthropicSkillCreatorV1 {
			precision := 2
			if metric == "time_seconds" {
				precision = 1
			}
			if metric == "tokens" {
				precision = 0
			}
			delta[metric] = strconv.FormatFloat(value, 'f', precision, 64)
			if value >= 0 {
				delta[metric] = "+" + delta[metric].(string)
			}
		} else {
			delta[metric] = roundFour(value)
		}
	}
	result["delta"] = delta
	return result
}

func gradingPassRate(view GradingView) float64 {
	passed := 0
	for _, result := range view.Results {
		if result.Passed {
			passed++
		}
	}
	return float64(passed) / float64(len(view.Results))
}

func observedMetricValues(rows []BenchmarkRun, duration bool, report *reportAccumulator) ([]float64, bool) {
	const maxExactlyRepresentedFloatInteger = uint64(1 << 53)
	values := make([]float64, 0, len(rows))
	complete := true
	for _, row := range rows {
		metric := row.Grading.TotalTokens
		scope := "benchmark.tokens"
		if duration {
			metric = row.Grading.DurationMillis
			scope = "benchmark.time_seconds"
		}
		switch metric.Presence {
		case MetricObserved:
			if metric.Value > maxExactlyRepresentedFloatInteger {
				report.add(ReportMetricUnsupported, scope, DispositionUnsupported, false)
				complete = false
				continue
			}
			value := float64(metric.Value)
			if duration {
				value /= 1000
			}
			values = append(values, value)
		case MetricUnknown:
			report.add(ReportMetricUnknown, scope, DispositionOmitted, false)
			complete = false
		case MetricUnsupported:
			report.add(ReportMetricUnsupported, scope, DispositionUnsupported, false)
			complete = false
		case MetricNotApplicable:
			report.add(ReportMetricNotApplicable, scope, DispositionOmitted, false)
			complete = false
		}
	}
	return values, complete
}

type statistics struct{ mean, stddev, minimum, maximum float64 }

func calculateStats(values []float64) statistics {
	mean := 0.0
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	variance := 0.0
	if len(values) > 1 {
		for _, value := range values {
			variance += (value - mean) * (value - mean)
		}
		variance /= float64(len(values) - 1)
	}
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	return statistics{roundFour(mean), roundFour(math.Sqrt(variance)), roundFour(minimum), roundFour(maximum)}
}

func encodeStats(format Format, stats statistics) map[string]any {
	result := map[string]any{"mean": stats.mean, "stddev": stats.stddev}
	if format == FormatAnthropicSkillCreatorV1 {
		result["min"] = stats.minimum
		result["max"] = stats.maximum
	}
	return result
}

func statsMean(value any) (float64, bool) {
	statistics, ok := value.(map[string]any)
	if !ok {
		return 0, false
	}
	mean, ok := statistics["mean"].(float64)
	return mean, ok
}

func roundFour(value float64) float64 { return math.Round(value*10000) / 10000 }

func encodeAnthropicRun(run BenchmarkRun, report *reportAccumulator) map[string]any {
	passed := uint32(0)
	expectations := make([]map[string]any, len(run.Grading.Results))
	for index, result := range run.Grading.Results {
		expectations[index] = map[string]any{"text": result.Text, "passed": result.Passed, "evidence": result.Evidence}
		if result.Passed {
			passed++
		}
	}
	total := countSlice(run.Grading.Results)
	result := map[string]any{
		"pass_rate": float64(passed) / float64(total), "passed": passed,
		"failed": total - passed, "total": total,
	}
	if run.Grading.DurationMillis.Presence == MetricObserved {
		result["time_seconds"] = json.Number(millisecondsAsSeconds(run.Grading.DurationMillis.Value))
	} else {
		reportMetricAbsence(report, "benchmark.runs[].result.time_seconds", run.Grading.DurationMillis)
	}
	if run.Grading.TotalTokens.Presence == MetricObserved {
		result["tokens"] = run.Grading.TotalTokens.Value
	} else {
		reportMetricAbsence(report, "benchmark.runs[].result.tokens", run.Grading.TotalTokens)
	}
	evalName := run.EvalName
	if evalName == "" {
		evalName = fmt.Sprintf("eval-%d", run.CaseID)
	}
	return map[string]any{
		"eval_id": run.CaseID, "eval_name": evalName,
		"configuration": configurationName(run.Configuration), "run_number": run.RunNumber,
		"result": result, "expectations": expectations, "notes": encodeNotes(run.Notes),
	}
}

// anthropicBenchmarkWithinEncodingBound rejects obviously oversized public
// views before constructing the nested map/slice document passed to json.Marshal.
// Every charged byte occurs verbatim in the encoded document; the small fixed
// charges are deliberately below the JSON syntax emitted for each row/result.
func anthropicBenchmarkWithinEncodingBound(view BenchmarkView) bool {
	remaining := uint64(MaxJSONBytes - 1)
	charge := func(amount uint64) bool {
		if amount > remaining {
			return false
		}
		remaining -= amount
		return true
	}
	if !charge(uint64(len(view.SkillName)+len(view.ExecutorModel)+len(view.AnalyzerModel)+len(view.Timestamp))) ||
		!charge(uint64(len(view.Notes))*4) {
		return false
	}
	for _, note := range view.Notes {
		if !charge(uint64(len(note))) {
			return false
		}
	}
	for _, run := range view.Runs {
		if !charge(32+uint64(len(run.EvalName))) || !charge(uint64(len(run.Notes))*4) {
			return false
		}
		for _, note := range run.Notes {
			if !charge(uint64(len(note))) {
				return false
			}
		}
		for _, result := range run.Grading.Results {
			if !charge(16 + uint64(len(result.Text)+len(result.Evidence))) {
				return false
			}
		}
	}
	return true
}

func encodeNotes(notes []string) []string {
	result := make([]string, len(notes))
	copy(result, notes)
	return result
}

func configurationName(configuration TreatmentKind) string {
	switch configuration {
	case TreatmentCurrentSkill:
		return "with_skill"
	case TreatmentNoSkill:
		return "without_skill"
	case TreatmentPreviousSkill:
		return "old_skill"
	default:
		return ""
	}
}

func marshalCompatibilityJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > MaxJSONBytes {
		return nil, fmt.Errorf("compatibility JSON bound")
	}
	return append(data, '\n'), nil
}
