package agentskills

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestImportGuideWorkspaceRequiresCaseMappingAndProjectsStrictTiming(t *testing.T) {
	experiment := importGuideExperiment(t)
	request := WorkspaceImportRequest{
		Root:       fixturePath("workspace-guide-v1"),
		Format:     FormatAgentSkillsGuideV1,
		Experiment: experiment,
		CaseDirectories: []CaseDirectory{
			{CaseID: 1, Path: "iteration-1/eval-summarize-rows"},
			{CaseID: 2, Path: "iteration-1/eval-describe-columns"},
		},
	}
	first, err := ImportWorkspace(request)
	if err != nil {
		t.Fatalf("ImportWorkspace() error = %v", err)
	}
	second, err := ImportWorkspace(request)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("stable workspace repeat = %#v, %v", second, err)
	}
	workspace := first.Workspace
	if workspace.Format != FormatAgentSkillsGuideV1 || workspace.ExperimentSHA256 != experiment.NormalizedSHA256 ||
		!validDigest(workspace.ContentSHA256) || len(workspace.Runs) != 4 {
		t.Fatalf("workspace identity = %#v", workspace)
	}
	current := workspace.Runs[0]
	if current.CaseID != 1 || current.Configuration != TreatmentCurrentSkill || current.RunNumber != 1 ||
		!current.OutputsPresent || len(current.Outputs) != 1 || current.Outputs[0].Path != "summary.txt" {
		t.Fatalf("guide current run = %#v", current)
	}
	if !current.TimingPresent || current.DurationMillis != (OptionalUint64{Presence: MetricObserved, Value: 0}) ||
		current.TotalTokens != (OptionalUint64{Presence: MetricObserved, Value: 0}) {
		t.Fatalf("guide timing projection = %#v / %#v", current.DurationMillis, current.TotalTokens)
	}
	if !current.GradingPresent || !current.Grading.FeedbackPresent ||
		current.Grading.Feedback[0] != (FeedbackEntry{Key: "review", Value: "Synthetic output is concise."}) {
		t.Fatalf("guide grading = %#v", current.Grading)
	}
	if current.Grading.DurationMillis != current.DurationMillis || current.Grading.TotalTokens != current.TotalTokens {
		t.Fatalf("guide run metrics were not propagated into grading: %#v", current.Grading)
	}
	if !workspace.FeedbackPresent || workspace.Feedback[0].Key != "comparison" {
		t.Fatalf("guide benchmark feedback = %#v", workspace.Feedback)
	}
	if !first.Report.BlocksExecution() {
		t.Fatalf("workspace report did not retain activation/coverage blockers: %#v", first.Report)
	}
	if entry, ok := findReportEntry(first.Report, ReportVerifierCoverageUnbound); !ok || entry.Count != 2 || !entry.BlocksExecution {
		t.Fatalf("guide verifier coverage report = %#v", first.Report)
	}
	if entry, ok := findReportEntry(first.Report, ReportBenchmarkSummaryPreserved); !ok || entry.Count != 1 {
		t.Fatalf("guide benchmark report = %#v", first.Report)
	}

	invalid := request
	invalid.Format = FormatAuto
	_, err = ImportWorkspace(invalid)
	requireErrorCode(t, err, ErrorInvalidRequest)
	invalid = request
	invalid.CaseDirectories = invalid.CaseDirectories[:1]
	_, err = ImportWorkspace(invalid)
	requireErrorCode(t, err, ErrorInvalidRequest)
}

func TestGuideCaseDirectoryContract(t *testing.T) {
	if iteration, ok := parseGuideCaseDirectory("iteration-12/eval-safe-slug2"); !ok || iteration != 12 {
		t.Fatalf("canonical guide directory = %d/%v", iteration, ok)
	}
	experiment := importGuideExperiment(t)
	validSecond := CaseDirectory{CaseID: 2, Path: "iteration-1/eval-two"}
	tests := []struct {
		name  string
		first CaseDirectory
		other CaseDirectory
	}{
		{name: "arbitrary root", first: CaseDirectory{CaseID: 1, Path: "eval-one"}, other: validSecond},
		{name: "nested root", first: CaseDirectory{CaseID: 1, Path: "iteration-1/group/eval-one"}, other: validSecond},
		{name: "zero iteration", first: CaseDirectory{CaseID: 1, Path: "iteration-0/eval-one"}, other: validSecond},
		{name: "noncanonical iteration", first: CaseDirectory{CaseID: 1, Path: "iteration-01/eval-one"}, other: validSecond},
		{name: "empty slug", first: CaseDirectory{CaseID: 1, Path: "iteration-1/eval-"}, other: validSecond},
		{name: "unsafe slug", first: CaseDirectory{CaseID: 1, Path: "iteration-1/eval-one_two"}, other: validSecond},
		{name: "mixed iterations", first: CaseDirectory{CaseID: 1, Path: "iteration-2/eval-one"}, other: validSecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ImportWorkspace(WorkspaceImportRequest{
				Root: fixturePath("workspace-guide-v1"), Format: FormatAgentSkillsGuideV1, Experiment: experiment,
				CaseDirectories: []CaseDirectory{test.first, test.other},
			})
			requireErrorCode(t, err, ErrorInvalidRequest)
		})
	}
}

func TestImportGuideWorkspaceRejectsInvalidTiming(t *testing.T) {
	experiment := importGuideExperiment(t)
	mappings := []CaseDirectory{{CaseID: 1, Path: "iteration-1/eval-one"}, {CaseID: 2, Path: "iteration-1/eval-two"}}
	tests := []struct {
		name string
		data string
	}{
		{name: "missing token count", data: `{"duration_ms":0}`},
		{name: "unknown member", data: `{"total_tokens":0,"duration_ms":0,"elapsed_seconds":0}`},
		{name: "null", data: `{"total_tokens":null,"duration_ms":0}`},
		{name: "wrong type", data: `{"total_tokens":"0","duration_ms":0}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "iteration-1", "eval-one", "with_skill", "timing.json"), test.data)
			_, err := ImportWorkspace(WorkspaceImportRequest{
				Root: root, Format: FormatAgentSkillsGuideV1, Experiment: experiment, CaseDirectories: mappings,
			})
			requireErrorCode(t, err, ErrorInvalidWorkspace)
		})
	}
}

func TestWorkspaceGradingCoverageMatchesImportedCriteriaExactly(t *testing.T) {
	experiment := importGuideExperiment(t)
	mappings := []CaseDirectory{{CaseID: 1, Path: "iteration-1/eval-one"}, {CaseID: 2, Path: "iteration-1/eval-two"}}
	tests := []struct {
		name string
		data string
	}{
		{name: "missing result", data: `{
  "summary":{"passed":1,"failed":0,"total":1,"pass_rate":1},
  "assertion_results":[{"assertion":"The summary reports three rows.","passed":true,"evidence":"Synthetic."}]
}`},
		{name: "reordered text", data: `{
  "summary":{"passed":2,"failed":0,"total":2,"pass_rate":1},
  "assertion_results":[
    {"assertion":"The output contains no invented columns.","passed":true,"evidence":"Synthetic."},
    {"assertion":"The summary reports three rows.","passed":true,"evidence":"Synthetic."}
  ]
}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "iteration-1", "eval-one", "with_skill", "grading.json"), test.data)
			_, err := ImportWorkspace(WorkspaceImportRequest{
				Root: root, Format: FormatAgentSkillsGuideV1, Experiment: experiment, CaseDirectories: mappings,
			})
			requireErrorCode(t, err, ErrorInvalidWorkspace)
		})
	}
}

func TestWorkspaceGradingRejectsContradictoryPassRate(t *testing.T) {
	data := []byte(`{
  "summary":{"passed":1,"failed":1,"total":2,"pass_rate":0.6},
  "assertion_results":[
    {"assertion":"first","passed":true,"evidence":"Synthetic."},
    {"assertion":"second","passed":false,"evidence":"Synthetic."}
  ]
}`)
	if _, err := decodeWorkspaceGrading(data, FormatAgentSkillsGuideV1); err == nil {
		t.Fatal("decodeWorkspaceGrading() accepted a pass_rate inconsistent with passed/total")
	}
}

func TestWorkspaceReportsMissingVerifierAndMetricCoverage(t *testing.T) {
	experiment := importGuideExperiment(t)
	mappings := []CaseDirectory{{CaseID: 1, Path: "iteration-1/eval-one"}, {CaseID: 2, Path: "iteration-1/eval-two"}}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "iteration-1", "eval-one", "with_skill", "timing.json"),
		`{"total_tokens":0,"duration_ms":0}`)
	result, err := ImportWorkspace(WorkspaceImportRequest{
		Root: root, Format: FormatAgentSkillsGuideV1, Experiment: experiment, CaseDirectories: mappings,
	})
	if err != nil {
		t.Fatalf("ImportWorkspace() error = %v", err)
	}
	missing, ok := findReportEntry(result.Report, ReportVerifierCoverageMissing)
	if !ok || missing.Count != 2 || !missing.BlocksExecution {
		t.Fatalf("missing verifier coverage report = %#v", result.Report)
	}
	if unknown, ok := findReportEntryScope(result.Report, ReportMetricUnknown, "workspace.runs[].total_tokens"); !ok || unknown.Count != 3 {
		t.Fatalf("missing token report = %#v", result.Report)
	}
	if cost, ok := findReportEntryScope(result.Report, ReportMetricUnsupported, "workspace.runs[].estimated_cost_microusd"); !ok || cost.Count != 4 {
		t.Fatalf("unsupported cost report = %#v", result.Report)
	}
}

func TestImportAnthropicWorkspaceProjectsZeroMetricsModelsNotesAndOldSkill(t *testing.T) {
	experiment := importAnthropicExperiment(t)
	result, err := ImportWorkspace(WorkspaceImportRequest{
		Root: fixturePath("workspace-anthropic-v1"), Format: FormatAnthropicSkillCreatorV1, Experiment: experiment,
	})
	if err != nil {
		t.Fatalf("ImportWorkspace() error = %v", err)
	}
	workspace := result.Workspace
	if workspace.PreviousSkillSHA256 != experiment.PreviousSkill.ContentSHA256 || len(workspace.Runs) != 2 {
		t.Fatalf("old-skill binding = %#v", workspace)
	}
	current, previous := workspace.Runs[0], workspace.Runs[1]
	if current.Configuration != TreatmentCurrentSkill || current.DurationMillis != (OptionalUint64{Presence: MetricObserved, Value: 0}) ||
		current.TotalTokens != (OptionalUint64{Presence: MetricObserved, Value: 0}) {
		t.Fatalf("current observed zero metrics = %#v / %#v", current.DurationMillis, current.TotalTokens)
	}
	if previous.Configuration != TreatmentPreviousSkill || previous.DurationMillis.Value != 1000 || previous.TotalTokens.Value != 20 {
		t.Fatalf("previous metrics = %#v / %#v", previous.DurationMillis, previous.TotalTokens)
	}
	if !current.OutputsPresent || current.Outputs[0].Path != "report.md" || !current.GradingPresent || len(current.Grading.Results) != 2 {
		t.Fatalf("current artifacts = %#v", current)
	}
	if !workspace.Metadata.Present || !workspace.Metadata.ExecutorModelPresent || workspace.Metadata.ExecutorModel != "executor-v1" ||
		!workspace.Metadata.AnalyzerModelPresent || workspace.Metadata.AnalyzerModel != "analyzer-v1" || !workspace.Metadata.TimestampPresent {
		t.Fatalf("model metadata = %#v", workspace.Metadata)
	}
	if !workspace.NotesPresent || !reflect.DeepEqual(workspace.Notes, []string{"Synthetic comparison reviewed."}) ||
		!current.NotesPresent || !reflect.DeepEqual(current.Notes, []string{"Reviewed synthetic output."}) {
		t.Fatalf("review notes = %#v / %#v", workspace.Notes, current.Notes)
	}
	detailCount := uint32(0)
	for _, entry := range result.Report.Entries {
		if entry.Code == ReportTimingDetailPreserved {
			detailCount += entry.Count
		}
	}
	if detailCount != 6 {
		t.Fatalf("timing detail report = %#v", result.Report)
	}
}

func TestImportWorkspaceRejectsMixedLayoutsAndInvalidTiming(t *testing.T) {
	experiment := importAnthropicExperiment(t)
	t.Run("empty workspace", func(t *testing.T) {
		_, err := ImportWorkspace(WorkspaceImportRequest{
			Root: t.TempDir(), Format: FormatAnthropicSkillCreatorV1, Experiment: experiment,
		})
		requireErrorCode(t, err, ErrorInvalidWorkspace)
	})
	t.Run("direct artifact in run-N format", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "eval-7", "with_skill", "grading.json"), `{}`)
		_, err := ImportWorkspace(WorkspaceImportRequest{Root: root, Format: FormatAnthropicSkillCreatorV1, Experiment: experiment})
		requireErrorCode(t, err, ErrorInvalidWorkspace)
	})
	t.Run("legacy runs prefix is not inferred", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "runs", "eval-7", "with_skill", "run-1", "grading.json"), `{}`)
		_, err := ImportWorkspace(WorkspaceImportRequest{Root: root, Format: FormatAnthropicSkillCreatorV1, Experiment: experiment})
		requireErrorCode(t, err, ErrorInvalidWorkspace)
	})
	t.Run("timing mismatch", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "eval-7", "with_skill", "run-1", "timing.json"),
			`{"total_tokens":0,"duration_ms":0,"total_duration_seconds":1}`)
		_, err := ImportWorkspace(WorkspaceImportRequest{Root: root, Format: FormatAnthropicSkillCreatorV1, Experiment: experiment})
		requireErrorCode(t, err, ErrorInvalidWorkspace)
	})
	t.Run("unknown timing member", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "eval-7", "with_skill", "run-1", "timing.json"),
			`{"total_tokens":0,"duration_ms":0,"duration_seconds":0}`)
		_, err := ImportWorkspace(WorkspaceImportRequest{Root: root, Format: FormatAnthropicSkillCreatorV1, Experiment: experiment})
		requireErrorCode(t, err, ErrorInvalidWorkspace)
	})
}

func TestWorkspacePublicationRoundTrip(t *testing.T) {
	t.Run("guide direct", func(t *testing.T) {
		experiment := importGuideExperiment(t)
		mappings := []CaseDirectory{{CaseID: 1, Path: "iteration-1/eval-summarize-rows"}, {CaseID: 2, Path: "iteration-1/eval-describe-columns"}}
		grading := func(passed bool) GradingView {
			duration := OptionalUint64{Presence: MetricUnknown}
			tokens := OptionalUint64{Presence: MetricUnknown}
			if passed {
				duration = OptionalUint64{Presence: MetricObserved, Value: 0}
				tokens = OptionalUint64{Presence: MetricObserved, Value: 0}
			}
			return GradingView{
				Results: []GradeResult{
					{Text: "The summary reports three rows.", Passed: passed, Evidence: "Synthetic evidence."},
					{Text: "The output contains no invented columns.", Passed: passed, Evidence: "Synthetic evidence."},
				},
				DurationMillis:  duration,
				TotalTokens:     tokens,
				FeedbackPresent: true, Feedback: []FeedbackEntry{{Key: "review", Value: "Reviewed."}},
			}
		}
		view := BenchmarkView{
			SkillName: "csv-helper", FeedbackPresent: true,
			Feedback: []FeedbackEntry{{Key: "comparison", Value: "Current is clearer."}},
			Runs: []BenchmarkRun{
				{CaseID: 1, Configuration: TreatmentCurrentSkill, RunNumber: 1, Grading: grading(true)},
				{CaseID: 1, Configuration: TreatmentNoSkill, RunNumber: 1, Grading: grading(false)},
			},
		}
		source, err := ImportWorkspace(WorkspaceImportRequest{
			Root: fixturePath("workspace-guide-v1"), Format: FormatAgentSkillsGuideV1,
			Experiment: experiment, CaseDirectories: mappings,
		})
		if err != nil {
			t.Fatalf("source ImportWorkspace() error = %v", err)
		}
		request := WorkspacePublicationRequest{
			Format: FormatAgentSkillsGuideV1, Experiment: experiment, CaseDirectories: mappings,
			Benchmark: view, Source: &source.Workspace,
		}
		plan, err := PlanWorkspacePublication(request)
		if err != nil {
			t.Fatalf("PlanWorkspacePublication() error = %v", err)
		}
		repeated, err := PlanWorkspacePublication(request)
		if err != nil || !reflect.DeepEqual(plan, repeated) || plan.Authoritative() {
			t.Fatalf("publication determinism/authority = %#v, %v", repeated, err)
		}
		if entry, ok := findReportEntry(plan.Report, ReportOutputsPreservedSourceOnly); !ok || entry.Count != 1 {
			t.Fatalf("output preservation report = %#v", plan.Report)
		}
		invalidRequest := request
		invalidView := view
		invalidView.Runs = append([]BenchmarkRun(nil), view.Runs...)
		invalidView.Runs[0].Grading.Results = append([]GradeResult(nil), view.Runs[0].Grading.Results...)
		invalidView.Runs[0].Grading.Results[0], invalidView.Runs[0].Grading.Results[1] =
			invalidView.Runs[0].Grading.Results[1], invalidView.Runs[0].Grading.Results[0]
		invalidRequest.Benchmark = invalidView
		_, err = PlanWorkspacePublication(invalidRequest)
		requireErrorCode(t, err, ErrorInvalidPublication)
		tamperedSource := source.Workspace
		tamperedSource.Runs = append([]WorkspaceRun(nil), source.Workspace.Runs...)
		tamperedSource.Runs[0].Outputs = append([]SnapshotFile(nil), source.Workspace.Runs[0].Outputs...)
		tamperedSource.Runs[0].Outputs[0].Data = append([]byte(nil), source.Workspace.Runs[0].Outputs[0].Data...)
		tamperedSource.Runs[0].Outputs[0].Data[0] ^= 1
		tamperedRequest := request
		tamperedRequest.Source = &tamperedSource
		_, err = PlanWorkspacePublication(tamperedRequest)
		requireErrorCode(t, err, ErrorInvalidPublication)
		destination := filepath.Join(t.TempDir(), "guide-publication")
		if err := plan.WriteNew(destination); err != nil {
			t.Fatalf("WriteNew() error = %v", err)
		}
		imported, err := ImportWorkspace(WorkspaceImportRequest{
			Root: destination, Format: FormatAgentSkillsGuideV1, Experiment: experiment, CaseDirectories: mappings,
		})
		if err != nil {
			t.Fatalf("round-trip ImportWorkspace() error = %v", err)
		}
		if !imported.Workspace.Runs[0].GradingPresent || !imported.Workspace.Runs[0].Grading.FeedbackPresent ||
			!imported.Workspace.FeedbackPresent || !imported.Workspace.Runs[0].OutputsPresent ||
			!imported.Workspace.Runs[0].TimingPresent {
			t.Fatalf("guide round-trip feedback = %#v", imported.Workspace)
		}
		if !bytes.Equal(imported.Workspace.Runs[0].Outputs[0].Data, source.Workspace.Runs[0].Outputs[0].Data) ||
			!bytes.Equal(imported.Workspace.Runs[0].TimingFile.Data, source.Workspace.Runs[0].TimingFile.Data) {
			t.Fatal("guide round-trip changed captured output/timing bytes")
		}
	})

	t.Run("Anthropic run-N", func(t *testing.T) {
		experiment := importAnthropicExperiment(t)
		view := anthropicPublicationBenchmark()
		view.FeedbackPresent = true
		view.Feedback = []FeedbackEntry{{Key: "review", Value: "Preserve outside the Anthropic schema."}}
		source, err := ImportWorkspace(WorkspaceImportRequest{
			Root: fixturePath("workspace-anthropic-v1"), Format: FormatAnthropicSkillCreatorV1, Experiment: experiment,
		})
		if err != nil {
			t.Fatalf("source ImportWorkspace() error = %v", err)
		}
		request := WorkspacePublicationRequest{
			Format: FormatAnthropicSkillCreatorV1, Experiment: experiment, Benchmark: view, Source: &source.Workspace,
		}
		plan, err := PlanWorkspacePublication(request)
		if err != nil {
			t.Fatalf("PlanWorkspacePublication() error = %v", err)
		}
		if plan.PreviousSkillSHA256 != experiment.PreviousSkill.ContentSHA256 || !validDigest(plan.ContentSHA256) {
			t.Fatalf("publication identity = %#v", plan)
		}
		for _, code := range []ReportCode{ReportReviewContentPreserved, ReportSkillDigestPreserved} {
			if _, ok := findReportEntry(plan.Report, code); !ok {
				t.Fatalf("publication report missing %q: %#v", code, plan.Report)
			}
		}
		destination := filepath.Join(t.TempDir(), "anthropic-publication")
		if err := plan.WriteNew(destination); err != nil {
			t.Fatalf("WriteNew() error = %v", err)
		}
		imported, err := ImportWorkspace(WorkspaceImportRequest{Root: destination, Format: FormatAnthropicSkillCreatorV1, Experiment: experiment})
		if err != nil {
			t.Fatalf("round-trip ImportWorkspace() error = %v", err)
		}
		if len(imported.Workspace.Runs) != 2 || imported.Workspace.Runs[0].DurationMillis.Value != 0 ||
			imported.Workspace.Runs[0].DurationMillis.Presence != MetricObserved || imported.Workspace.Runs[0].TotalTokens.Presence != MetricObserved ||
			imported.Workspace.Runs[1].DurationMillis.Value != 1000 || imported.Workspace.PreviousSkillSHA256 != plan.PreviousSkillSHA256 ||
			!imported.Workspace.Runs[0].OutputsPresent || !imported.Workspace.Runs[0].TimingPresent {
			t.Fatalf("Anthropic round-trip = %#v", imported.Workspace)
		}
		if !bytes.Equal(imported.Workspace.Runs[0].Outputs[0].Data, source.Workspace.Runs[0].Outputs[0].Data) ||
			!bytes.Equal(imported.Workspace.Runs[0].TimingFile.Data, source.Workspace.Runs[0].TimingFile.Data) {
			t.Fatal("Anthropic round-trip changed captured output/timing bytes")
		}
	})
}

func TestWorkspacePublicationWriterRefusesOverwriteAndDetectsMutation(t *testing.T) {
	experiment := importAnthropicExperiment(t)
	plan, err := PlanWorkspacePublication(WorkspacePublicationRequest{
		Format: FormatAnthropicSkillCreatorV1, Experiment: experiment, Benchmark: anthropicPublicationBenchmark(),
	})
	if err != nil {
		t.Fatalf("PlanWorkspacePublication() error = %v", err)
	}
	parent := t.TempDir()
	existing := filepath.Join(parent, "existing")
	writeFile(t, filepath.Join(existing, "sentinel.txt"), "retain\n")
	err = plan.WriteNew(existing)
	requireErrorCode(t, err, ErrorInvalidDestination)
	if data, readErr := os.ReadFile(filepath.Join(existing, "sentinel.txt")); readErr != nil || string(data) != "retain\n" {
		t.Fatalf("existing destination changed: %q, %v", data, readErr)
	}
	if err := plan.WriteNew("relative-destination"); codeOfMust(err) != ErrorInvalidDestination {
		t.Fatalf("relative destination error = %v", err)
	}

	tampered := clonePublicationPlan(plan)
	tampered.Files[0].Data[0] ^= 1
	err = tampered.WriteNew(filepath.Join(parent, "tampered"))
	requireErrorCode(t, err, ErrorInvalidPublication)

	forgedLayout := clonePublicationPlan(plan)
	for index := range forgedLayout.Files {
		forgedLayout.Files[index].Path = strings.Replace(forgedLayout.Files[index].Path, "/old_skill/", "/without_skill/", 1)
	}
	sort.Slice(forgedLayout.Files, func(i, j int) bool { return forgedLayout.Files[i].Path < forgedLayout.Files[j].Path })
	forgedLayout.ContentSHA256 = digestPublication(forgedLayout)
	err = forgedLayout.WriteNew(filepath.Join(parent, "forged-layout"))
	requireErrorCode(t, err, ErrorInvalidPublication)
	if _, statErr := os.Stat(filepath.Join(parent, "forged-layout")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("forged publication created a destination: %v", statErr)
	}

	legacyLayout := clonePublicationPlan(plan)
	for index := range legacyLayout.Files {
		if legacyLayout.Files[index].Path != "benchmark.json" {
			legacyLayout.Files[index].Path = "runs/" + legacyLayout.Files[index].Path
		}
	}
	sort.Slice(legacyLayout.Files, func(i, j int) bool { return legacyLayout.Files[i].Path < legacyLayout.Files[j].Path })
	legacyLayout.ContentSHA256 = digestPublication(legacyLayout)
	err = legacyLayout.WriteNew(filepath.Join(parent, "legacy-layout"))
	requireErrorCode(t, err, ErrorInvalidPublication)

	mutatedDestination := filepath.Join(parent, "mutated")
	err = writePublicationWithHooks(plan, mutatedDestination, publicationHooks{
		beforeCompletion: func(root string) {
			writeFile(t, filepath.Join(root, "unexpected.json"), `{}`)
		},
	})
	requireErrorCode(t, err, ErrorPublicationFailed)
	if _, statErr := os.Stat(filepath.Join(mutatedDestination, publicationMarker)); statErr != nil {
		t.Fatalf("failed publication did not retain its incomplete marker: %v", statErr)
	}
	_, err = ImportWorkspace(WorkspaceImportRequest{
		Root: mutatedDestination, Format: FormatAnthropicSkillCreatorV1, Experiment: experiment,
	})
	requireErrorCode(t, err, ErrorInvalidWorkspace)

	symlinkParent := filepath.Join(parent, "symlink-parent")
	if symlinkErr := os.Symlink(t.TempDir(), symlinkParent); symlinkErr != nil {
		t.Fatalf("Symlink(): %v", symlinkErr)
	}
	err = plan.WriteNew(filepath.Join(symlinkParent, "publication"))
	requireErrorCode(t, err, ErrorInvalidDestination)

	mkdirOpenDestination := filepath.Join(parent, "mkdir-open-replaced")
	movedCreatedDestination := filepath.Join(parent, "mkdir-created-original")
	err = writePublicationWithHooks(plan, mkdirOpenDestination, publicationHooks{
		beforeDestinationOpen: func(root string) {
			if renameErr := os.Rename(root, movedCreatedDestination); renameErr != nil {
				t.Fatalf("Rename(): %v", renameErr)
			}
			if mkdirErr := os.Mkdir(root, 0o700); mkdirErr != nil {
				t.Fatalf("Mkdir(): %v", mkdirErr)
			}
		},
	})
	requireErrorCode(t, err, ErrorPublicationFailed)
	for _, root := range []string{mkdirOpenDestination, movedCreatedDestination} {
		entries, readErr := os.ReadDir(root)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("mkdir-to-open replacement received publication data at %q: %#v, %v", root, entries, readErr)
		}
	}

	replacedDestination := filepath.Join(parent, "replaced")
	movedDestination := filepath.Join(parent, "moved-owned-publication")
	err = writePublicationWithHooks(plan, replacedDestination, publicationHooks{
		afterDestinationCreated: func(root string) {
			if renameErr := os.Rename(root, movedDestination); renameErr != nil {
				t.Fatalf("Rename(): %v", renameErr)
			}
			writeFile(t, filepath.Join(root, "decoy.txt"), "retain decoy\n")
		},
	})
	requireErrorCode(t, err, ErrorPublicationFailed)
	if data, readErr := os.ReadFile(filepath.Join(replacedDestination, "decoy.txt")); readErr != nil || string(data) != "retain decoy\n" {
		t.Fatalf("replacement destination was deleted: %q, %v", data, readErr)
	}
}

func TestAppendPublicationFileChecksBoundBeforeRetention(t *testing.T) {
	var files []PublicationFile
	total := uint64(maxPublicationDataBytes)
	data := []byte("{}")
	if err := appendPublicationFile(&files, &total, "benchmark.json", data); err == nil {
		t.Fatal("appendPublicationFile() accepted an aggregate beyond MaxTreeBytes")
	}
	if len(files) != 0 || total != maxPublicationDataBytes {
		t.Fatalf("rejected artifact was retained: len=%d total=%d", len(files), total)
	}

	total = 0
	if err := appendPublicationFile(&files, &total, "benchmark.json", data); err != nil {
		t.Fatalf("appendPublicationFile() error = %v", err)
	}
	data[0] = '['
	if string(files[0].Data) != "{}" || total != 2 {
		t.Fatalf("retained publication file = %#v, total=%d", files[0], total)
	}
}

func importGuideExperiment(t *testing.T) Experiment {
	t.Helper()
	result, err := Import(ImportRequest{SkillRoot: fixturePath("guide-v1", "skill"), Format: FormatAgentSkillsGuideV1, Baseline: BaselineNoSkill})
	if err != nil {
		t.Fatalf("Import(guide): %v", err)
	}
	return result.Experiment
}

func importAnthropicExperiment(t *testing.T) Experiment {
	t.Helper()
	result, err := Import(ImportRequest{
		SkillRoot: fixturePath("anthropic-v1", "skill"), PreviousSkillRoot: fixturePath("anthropic-v1", "previous"),
		Format: FormatAnthropicSkillCreatorV1, Baseline: BaselinePreviousSkill,
	})
	if err != nil {
		t.Fatalf("Import(Anthropic): %v", err)
	}
	return result.Experiment
}

func anthropicPublicationBenchmark() BenchmarkView {
	grading := func(passed []bool, duration, tokens uint64) GradingView {
		return GradingView{
			Results: []GradeResult{
				{Text: "The title identifies a status report.", Passed: passed[0], Evidence: "Synthetic title evidence."},
				{Text: "The body says the status is ready.", Passed: passed[1], Evidence: "Synthetic body evidence."},
			},
			DurationMillis: OptionalUint64{Presence: MetricObserved, Value: duration},
			TotalTokens:    OptionalUint64{Presence: MetricObserved, Value: tokens},
		}
	}
	return BenchmarkView{
		SkillName: "report-helper", ExecutorModel: "executor-v1", AnalyzerModel: "analyzer-v1", Timestamp: "2026-08-09T12:00:00Z",
		NotesPresent: true, Notes: []string{"Synthetic comparison reviewed."},
		Runs: []BenchmarkRun{
			{CaseID: 7, Configuration: TreatmentCurrentSkill, RunNumber: 1, Grading: grading([]bool{true, true}, 0, 0), NotesPresent: true, Notes: []string{"Reviewed current output."}},
			{CaseID: 7, Configuration: TreatmentPreviousSkill, RunNumber: 1, Grading: grading([]bool{false, true}, 1000, 20), NotesPresent: true, Notes: []string{}},
		},
	}
}

func codeOfMust(err error) ErrorCode {
	code, _ := CodeOf(err)
	return code
}
