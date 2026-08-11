package agentskills

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestEncodeGradingUsesVariantSpellingAndPreservesMetricPresence(t *testing.T) {
	view := GradingView{
		Results: []GradeResult{
			{Text: "First criterion", Passed: true, Evidence: "Observed first fact."},
			{Text: "Second criterion", Passed: false, Evidence: "Second fact was absent."},
		},
		DurationMillis: OptionalUint64{Presence: MetricObserved, Value: 0},
		TotalTokens:    OptionalUint64{Presence: MetricUnknown},
	}

	guide, err := EncodeGrading(FormatAgentSkillsGuideV1, view)
	if err != nil {
		t.Fatalf("EncodeGrading(guide) error = %v", err)
	}
	if guide.Authoritative() || guide.Kind != "grading" || guide.Format != FormatAgentSkillsGuideV1 || !bytes.HasSuffix(guide.Data, []byte("\n")) {
		t.Fatalf("guide artifact contract = %#v", guide)
	}
	guideDocument := decodeArtifact(t, guide.Data)
	guideResults := jsonArray(t, guideDocument, "assertion_results")
	if _, ok := guideDocument["expectations"]; ok {
		t.Fatal("guide grading emitted Anthropic expectations")
	}
	firstGuide := jsonObjectValue(t, guideResults[0])
	if firstGuide["assertion"] != "First criterion" || firstGuide["evidence"] != "Observed first fact." {
		t.Fatalf("guide assertion result = %#v", firstGuide)
	}
	if _, ok := firstGuide["text"]; ok {
		t.Fatal("guide assertion result used Anthropic text spelling")
	}
	durationEntry, ok := findReportEntry(guide.Report, ReportMetricUnsupported)
	if !ok || durationEntry.Scope != "grading.duration_millis" || durationEntry.Disposition != DispositionPreservedSourceOnly {
		t.Fatalf("guide duration report = %#v", guide.Report)
	}
	unknownEntry, ok := findReportEntryScope(guide.Report, ReportMetricUnknown, "grading.total_tokens")
	if !ok || unknownEntry.Scope != "grading.total_tokens" || unknownEntry.Disposition != DispositionOmitted {
		t.Fatalf("guide unknown-token report = %#v", guide.Report)
	}

	anthropic, err := EncodeGrading(FormatAnthropicSkillCreatorV1, view)
	if err != nil {
		t.Fatalf("EncodeGrading(Anthropic) error = %v", err)
	}
	if anthropic.Authoritative() {
		t.Fatal("Anthropic compatibility artifact claimed authority")
	}
	anthropicDocument := decodeArtifact(t, anthropic.Data)
	anthropicResults := jsonArray(t, anthropicDocument, "expectations")
	if _, ok := anthropicDocument["assertion_results"]; ok {
		t.Fatal("Anthropic grading emitted guide assertion_results")
	}
	firstAnthropic := jsonObjectValue(t, anthropicResults[0])
	if firstAnthropic["text"] != "First criterion" {
		t.Fatalf("Anthropic expectation = %#v", firstAnthropic)
	}
	if _, ok := firstAnthropic["assertion"]; ok {
		t.Fatal("Anthropic expectation used guide assertion spelling")
	}
	timing := jsonObject(t, anthropicDocument, "timing")
	if got := jsonNumber(t, timing, "total_duration_seconds").String(); got != "0" {
		t.Fatalf("observed zero duration = %q", got)
	}
	if _, ok := findReportEntry(anthropic.Report, ReportMetricUnsupported); ok {
		t.Fatalf("represented Anthropic duration reported unsupported: %#v", anthropic.Report)
	}
	if _, ok := findReportEntryScope(anthropic.Report, ReportMetricUnknown, "grading.total_tokens"); !ok {
		t.Fatalf("Anthropic unknown-token report = %#v", anthropic.Report)
	}

	invalid := view
	invalid.TotalTokens = OptionalUint64{Presence: MetricUnknown, Value: 1}
	_, err = EncodeGrading(FormatAnthropicSkillCreatorV1, invalid)
	requireErrorCode(t, err, ErrorInvalidExport)
}

func TestEncodeBenchmarkEmitsGuideSummaryAndAnthropicRuns(t *testing.T) {
	view := benchmarkFixture()
	guide, err := EncodeBenchmark(FormatAgentSkillsGuideV1, view)
	if err != nil {
		t.Fatalf("EncodeBenchmark(guide) error = %v", err)
	}
	secondGuide, err := EncodeBenchmark(FormatAgentSkillsGuideV1, view)
	if err != nil || !bytes.Equal(guide.Data, secondGuide.Data) || !reflect.DeepEqual(guide.Report, secondGuide.Report) {
		t.Fatalf("guide benchmark was not deterministic: %v", err)
	}
	guideDocument := decodeArtifact(t, guide.Data)
	if len(guideDocument) != 1 {
		t.Fatalf("guide top-level fields = %#v", guideDocument)
	}
	guideSummary := jsonObject(t, guideDocument, "run_summary")
	withSkill := jsonObject(t, guideSummary, "with_skill")
	withoutSkill := jsonObject(t, guideSummary, "without_skill")
	if got := jsonFloat(t, jsonObject(t, withSkill, "time_seconds"), "mean"); got != 0.5 {
		t.Fatalf("with-skill time mean = %v", got)
	}
	if got := jsonFloat(t, jsonObject(t, withSkill, "tokens"), "mean"); got != 5 {
		t.Fatalf("with-skill token mean = %v", got)
	}
	if got := jsonFloat(t, jsonObject(t, withoutSkill, "time_seconds"), "mean"); got != 2.5 {
		t.Fatalf("without-skill time mean = %v", got)
	}
	guideDelta := jsonObject(t, guideSummary, "delta")
	if got := jsonFloat(t, guideDelta, "pass_rate"); got != 0.5 {
		t.Fatalf("guide pass-rate delta = %v", got)
	}
	if got := jsonFloat(t, guideDelta, "time_seconds"); got != -2 {
		t.Fatalf("guide time delta = %v", got)
	}
	if guide.Authoritative() {
		t.Fatal("guide benchmark claimed evaluator authority")
	}
	if entry, ok := findReportEntry(guide.Report, ReportRunDetailsOmitted); !ok || entry.Count != 4 || entry.Disposition != DispositionPreservedSourceOnly {
		t.Fatalf("guide run-detail report = %#v", guide.Report)
	}
	for _, code := range []ReportCode{ReportSkillMetadataOmitted, ReportModelMetadataOmitted, ReportTimestampOmitted} {
		if _, ok := findReportEntry(guide.Report, code); !ok {
			t.Fatalf("guide report omitted loss code %q: %#v", code, guide.Report)
		}
	}

	for index := range view.Runs {
		view.Runs[index].EvalName = "status-check"
	}
	anthropic, err := EncodeBenchmark(FormatAnthropicSkillCreatorV1, view)
	if err != nil {
		t.Fatalf("EncodeBenchmark(Anthropic) error = %v", err)
	}
	anthropicDocument := decodeArtifact(t, anthropic.Data)
	metadata := jsonObject(t, anthropicDocument, "metadata")
	if metadata["skill_name"] != "report-helper" || metadata["executor_model"] != "executor-v1" || metadata["analyzer_model"] != "analyzer-v1" {
		t.Fatalf("Anthropic metadata = %#v", metadata)
	}
	runs := jsonArray(t, anthropicDocument, "runs")
	if len(runs) != 4 {
		t.Fatalf("Anthropic runs = %#v", runs)
	}
	firstRun := jsonObjectValue(t, runs[0])
	if firstRun["configuration"] != "with_skill" || firstRun["eval_name"] != "status-check" ||
		jsonNumber(t, firstRun, "run_number").String() != "1" {
		t.Fatalf("first Anthropic run = %#v", firstRun)
	}
	firstResult := jsonObject(t, firstRun, "result")
	if jsonNumber(t, firstResult, "time_seconds").String() != "0" || jsonNumber(t, firstResult, "tokens").String() != "0" {
		t.Fatalf("observed zero run metrics = %#v", firstResult)
	}
	firstExpectations := jsonArray(t, firstRun, "expectations")
	if jsonObjectValue(t, firstExpectations[0])["text"] != "criterion" {
		t.Fatalf("Anthropic run expectations = %#v", firstExpectations)
	}
	anthropicSummary := jsonObject(t, anthropicDocument, "run_summary")
	anthropicDelta := jsonObject(t, anthropicSummary, "delta")
	if anthropicDelta["pass_rate"] != "+0.50" || anthropicDelta["time_seconds"] != "-2.0" || anthropicDelta["tokens"] != "-20" {
		t.Fatalf("Anthropic deltas = %#v", anthropicDelta)
	}
	for _, code := range []ReportCode{ReportActivationUnbound, ReportVerifierCoverageUnbound, ReportPathOmitted, ReportMetricUnknown} {
		if _, ok := findReportEntry(anthropic.Report, code); !ok {
			t.Fatalf("Anthropic report omitted %q: %#v", code, anthropic.Report)
		}
	}
}

func TestEncodeBenchmarkRejectsExpandedDocumentBeforeConstruction(t *testing.T) {
	largeText := strings.Repeat("x", MaxTextBytes)
	results := make([]GradeResult, MaxCriteriaPerCase+1)
	for index := range results {
		results[index] = GradeResult{Text: largeText, Evidence: largeText}
	}
	grading := GradingView{
		Results:        results,
		DurationMillis: OptionalUint64{Presence: MetricObserved},
		TotalTokens:    OptionalUint64{Presence: MetricObserved},
	}
	runs := make([]BenchmarkRun, 0, MaxRuns)
	for caseID := uint32(1); caseID <= MaxCases; caseID++ {
		for runNumber := uint32(1); runNumber <= 8; runNumber++ {
			runs = append(runs,
				BenchmarkRun{CaseID: caseID, Configuration: TreatmentCurrentSkill, RunNumber: runNumber, Grading: grading},
				BenchmarkRun{CaseID: caseID, Configuration: TreatmentNoSkill, RunNumber: runNumber, Grading: grading},
			)
		}
	}
	_, err := EncodeBenchmark(FormatAnthropicSkillCreatorV1, BenchmarkView{SkillName: "bounded-export", Runs: runs})
	requireErrorCode(t, err, ErrorInvalidExport)
}

func TestEncodeBenchmarkReportsSummaryPrecisionLoss(t *testing.T) {
	metric := OptionalUint64{Presence: MetricObserved, Value: 1<<53 + 1}
	grading := GradingView{
		Results:        []GradeResult{{Text: "criterion", Passed: true}},
		DurationMillis: metric,
		TotalTokens:    metric,
	}
	view := BenchmarkView{SkillName: "precision-bound", Runs: []BenchmarkRun{
		{CaseID: 1, Configuration: TreatmentCurrentSkill, RunNumber: 1, Grading: grading},
		{CaseID: 1, Configuration: TreatmentNoSkill, RunNumber: 1, Grading: grading},
	}}
	artifact, err := EncodeBenchmark(FormatAnthropicSkillCreatorV1, view)
	if err != nil {
		t.Fatalf("EncodeBenchmark() error = %v", err)
	}
	summary := jsonObject(t, decodeArtifact(t, artifact.Data), "run_summary")
	for _, configuration := range []string{"with_skill", "without_skill"} {
		row := jsonObject(t, summary, configuration)
		if _, ok := row["time_seconds"]; ok {
			t.Fatalf("%s summary retained imprecise time: %#v", configuration, row)
		}
		if _, ok := row["tokens"]; ok {
			t.Fatalf("%s summary retained imprecise tokens: %#v", configuration, row)
		}
	}
	unsupported := uint32(0)
	for _, entry := range artifact.Report.Entries {
		if entry.Code == ReportMetricUnsupported &&
			(entry.Scope == "benchmark.time_seconds" || entry.Scope == "benchmark.tokens") {
			unsupported += entry.Count
		}
	}
	if unsupported != 4 {
		t.Fatalf("precision-loss report = %#v", artifact.Report)
	}
}

func TestEncodeBenchmarkDistinguishesUnknownFromObservedZero(t *testing.T) {
	grading := func(duration OptionalUint64) GradingView {
		return GradingView{
			Results:        []GradeResult{{Text: "criterion", Passed: true}},
			DurationMillis: duration,
			TotalTokens:    OptionalUint64{Presence: MetricObserved, Value: 0},
		}
	}
	view := BenchmarkView{
		SkillName: "report-helper",
		Runs: []BenchmarkRun{
			{CaseID: 1, Configuration: TreatmentCurrentSkill, RunNumber: 1, Grading: grading(OptionalUint64{Presence: MetricUnknown})},
			{CaseID: 1, Configuration: TreatmentNoSkill, RunNumber: 1, Grading: grading(OptionalUint64{Presence: MetricObserved, Value: 0})},
		},
	}

	artifact, err := EncodeBenchmark(FormatAnthropicSkillCreatorV1, view)
	if err != nil {
		t.Fatalf("EncodeBenchmark() error = %v", err)
	}
	document := decodeArtifact(t, artifact.Data)
	summary := jsonObject(t, document, "run_summary")
	if _, ok := jsonObject(t, summary, "with_skill")["time_seconds"]; ok {
		t.Fatal("unknown duration was serialized as an observed value")
	}
	withoutTime := jsonObject(t, jsonObject(t, summary, "without_skill"), "time_seconds")
	if got := jsonFloat(t, withoutTime, "mean"); got != 0 {
		t.Fatalf("observed zero duration mean = %v", got)
	}
	runs := jsonArray(t, document, "runs")
	currentResult := jsonObject(t, jsonObjectValue(t, runs[0]), "result")
	baselineResult := jsonObject(t, jsonObjectValue(t, runs[1]), "result")
	if _, ok := currentResult["time_seconds"]; ok {
		t.Fatal("unknown per-run duration was serialized")
	}
	if jsonNumber(t, baselineResult, "time_seconds").String() != "0" || jsonNumber(t, baselineResult, "tokens").String() != "0" {
		t.Fatalf("observed zero per-run metrics = %#v", baselineResult)
	}
	unknownCount := uint32(0)
	for _, entry := range artifact.Report.Entries {
		if entry.Code == ReportMetricUnknown && (entry.Scope == "benchmark.time_seconds" ||
			entry.Scope == "benchmark.runs[].result.time_seconds") {
			unknownCount += entry.Count
		}
	}
	if unknownCount != 2 {
		t.Fatalf("unknown metric report = %#v", artifact.Report)
	}
}

func TestExportReportsCostActivationAndVerifierCoverageLosses(t *testing.T) {
	grading := GradingView{
		Results:               []GradeResult{{Text: "criterion", Passed: true}},
		DurationMillis:        OptionalUint64{Presence: MetricObserved},
		TotalTokens:           OptionalUint64{Presence: MetricObserved},
		EstimatedCostMicroUSD: OptionalUint64{Presence: MetricObserved},
	}
	artifact, err := EncodeGrading(FormatAnthropicSkillCreatorV1, grading)
	if err != nil {
		t.Fatalf("EncodeGrading() error = %v", err)
	}
	for _, code := range []ReportCode{ReportActivationUnbound, ReportVerifierCoverageUnbound} {
		entry, ok := findReportEntry(artifact.Report, code)
		if !ok || !entry.BlocksExecution {
			t.Fatalf("grading report omitted blocking %q: %#v", code, artifact.Report)
		}
	}
	cost, ok := findReportEntryScope(artifact.Report, ReportMetricUnsupported, "grading.estimated_cost_microusd")
	if !ok || cost.Disposition != DispositionPreservedSourceOnly {
		t.Fatalf("grading cost report = %#v", artifact.Report)
	}

	benchmark := BenchmarkView{SkillName: "report-helper", Runs: []BenchmarkRun{
		{CaseID: 1, Configuration: TreatmentCurrentSkill, RunNumber: 1, Grading: grading},
		{CaseID: 1, Configuration: TreatmentNoSkill, RunNumber: 1, Grading: grading},
	}}
	artifact, err = EncodeBenchmark(FormatAgentSkillsGuideV1, benchmark)
	if err != nil {
		t.Fatalf("EncodeBenchmark() error = %v", err)
	}
	cost, ok = findReportEntryScope(artifact.Report, ReportMetricUnsupported, "benchmark.runs[].estimated_cost_microusd")
	if !ok || cost.Count != 2 {
		t.Fatalf("benchmark cost report = %#v", artifact.Report)
	}
}

func TestEncodeBenchmarkRejectsUnpairedAndMixedConfigurations(t *testing.T) {
	base := benchmarkFixture()
	tests := []struct {
		name string
		view BenchmarkView
	}{
		{name: "unpaired", view: BenchmarkView{SkillName: base.SkillName, Runs: base.Runs[:3]}},
		{name: "sparse run numbers", view: BenchmarkView{SkillName: base.SkillName, Runs: []BenchmarkRun{
			{CaseID: 1, Configuration: TreatmentCurrentSkill, RunNumber: 2, Grading: base.Runs[0].Grading},
			{CaseID: 1, Configuration: TreatmentNoSkill, RunNumber: 2, Grading: base.Runs[0].Grading},
		}}},
		{name: "mixed baselines", view: BenchmarkView{SkillName: base.SkillName, Runs: append(append([]BenchmarkRun(nil), base.Runs...), BenchmarkRun{
			CaseID: 2, Configuration: TreatmentPreviousSkill, RunNumber: 1, Grading: base.Runs[0].Grading,
		})}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := EncodeBenchmark(FormatAnthropicSkillCreatorV1, test.view)
			requireErrorCode(t, err, ErrorInvalidExport)
		})
	}

	invalidMetric := base
	invalidMetric.Runs = append([]BenchmarkRun(nil), base.Runs...)
	invalidMetric.Runs[0].Grading.DurationMillis = OptionalUint64{Presence: MetricUnknown, Value: 1}
	_, err := EncodeBenchmark(FormatAnthropicSkillCreatorV1, invalidMetric)
	requireErrorCode(t, err, ErrorInvalidExport)
}

func TestExportersReportNotApplicableMetrics(t *testing.T) {
	grading := GradingView{
		Results:        []GradeResult{{Text: "criterion", Passed: true}},
		DurationMillis: OptionalUint64{Presence: MetricNotApplicable},
		TotalTokens:    OptionalUint64{Presence: MetricNotApplicable},
	}
	artifact, err := EncodeGrading(FormatAnthropicSkillCreatorV1, grading)
	if err != nil {
		t.Fatalf("EncodeGrading() error = %v", err)
	}
	count := uint32(0)
	for _, entry := range artifact.Report.Entries {
		if entry.Code == ReportMetricNotApplicable {
			count += entry.Count
		}
	}
	if count != 2 {
		t.Fatalf("grading not-applicable report = %#v", artifact.Report)
	}

	benchmark := BenchmarkView{
		SkillName: "report-helper",
		Runs: []BenchmarkRun{
			{CaseID: 1, Configuration: TreatmentCurrentSkill, RunNumber: 1, Grading: grading},
			{CaseID: 1, Configuration: TreatmentNoSkill, RunNumber: 1, Grading: grading},
		},
	}
	artifact, err = EncodeBenchmark(FormatAnthropicSkillCreatorV1, benchmark)
	if err != nil {
		t.Fatalf("EncodeBenchmark() error = %v", err)
	}
	count = 0
	for _, entry := range artifact.Report.Entries {
		if entry.Code == ReportMetricNotApplicable {
			count += entry.Count
		}
	}
	if count != 8 {
		t.Fatalf("benchmark not-applicable report = %#v", artifact.Report)
	}
}

func benchmarkFixture() BenchmarkView {
	grading := func(passed bool, milliseconds, tokens uint64) GradingView {
		return GradingView{
			Results:        []GradeResult{{Text: "criterion", Passed: passed, Evidence: "synthetic evidence"}},
			DurationMillis: OptionalUint64{Presence: MetricObserved, Value: milliseconds},
			TotalTokens:    OptionalUint64{Presence: MetricObserved, Value: tokens},
		}
	}
	return BenchmarkView{
		SkillName:     "report-helper",
		ExecutorModel: "executor-v1",
		AnalyzerModel: "analyzer-v1",
		Timestamp:     "2026-08-09T12:00:00Z",
		Runs: []BenchmarkRun{
			{CaseID: 7, Configuration: TreatmentNoSkill, RunNumber: 2, Grading: grading(false, 3000, 30)},
			{CaseID: 7, Configuration: TreatmentCurrentSkill, RunNumber: 1, Grading: grading(true, 0, 0)},
			{CaseID: 7, Configuration: TreatmentNoSkill, RunNumber: 1, Grading: grading(false, 2000, 20)},
			{CaseID: 7, Configuration: TreatmentCurrentSkill, RunNumber: 2, Grading: grading(false, 1000, 10)},
		},
	}
}

func decodeArtifact(t *testing.T, data []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("artifact trailing decode = %v", err)
	}
	return result
}

func jsonObject(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key]
	if !ok {
		t.Fatalf("missing object field %q in %#v", key, parent)
	}
	return jsonObjectValue(t, value)
}

func jsonObjectValue(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value is %T, want object", value)
	}
	return result
}

func jsonArray(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	value, ok := parent[key]
	if !ok {
		t.Fatalf("missing array field %q in %#v", key, parent)
	}
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("field %q is %T, want array", key, value)
	}
	return result
}

func jsonNumber(t *testing.T, parent map[string]any, key string) json.Number {
	t.Helper()
	value, ok := parent[key]
	if !ok {
		t.Fatalf("missing number field %q in %#v", key, parent)
	}
	result, ok := value.(json.Number)
	if !ok {
		t.Fatalf("field %q is %T, want json.Number", key, value)
	}
	return result
}

func jsonFloat(t *testing.T, parent map[string]any, key string) float64 {
	t.Helper()
	value, err := jsonNumber(t, parent, key).Float64()
	if err != nil {
		t.Fatalf("field %q float: %v", key, err)
	}
	return value
}
