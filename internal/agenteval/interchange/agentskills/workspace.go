package agentskills

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

type workspaceRunLocation struct {
	caseID        uint32
	configuration TreatmentKind
	runNumber     uint32
	prefix        string
}

// ImportWorkspace captures and validates a documented local iteration
// workspace without invoking a runner, grader, viewer, or aggregator.
func ImportWorkspace(request WorkspaceImportRequest) (WorkspaceImportResult, error) {
	if request.Root == "" || request.Format == FormatAuto || request.Format != request.Experiment.Format ||
		(request.Format != FormatAgentSkillsGuideV1 && request.Format != FormatAnthropicSkillCreatorV1) {
		return WorkspaceImportResult{}, contractError(ErrorInvalidRequest, nil)
	}
	if err := validateExperimentForProjection(request.Experiment); err != nil {
		return WorkspaceImportResult{}, contractError(ErrorInvalidRequest, err)
	}
	casePaths, err := bindWorkspaceCasePaths(request.Format, request.Experiment, request.CaseDirectories)
	if err != nil {
		return WorkspaceImportResult{}, err
	}
	tree, err := readStableTree(request.Root)
	if err != nil {
		return WorkspaceImportResult{}, err
	}
	if len(tree.entries) == 0 {
		return WorkspaceImportResult{}, contractError(ErrorInvalidWorkspace, fmt.Errorf("empty workspace"))
	}
	locations, err := discoverWorkspaceRuns(tree, request.Format, request.Experiment, casePaths)
	if err != nil {
		return WorkspaceImportResult{}, err
	}
	if err := validateWorkspaceLayout(tree, locations, casePaths, request.Experiment.Baseline); err != nil {
		return WorkspaceImportResult{}, err
	}

	workspace := Workspace{
		Format: request.Format, ExperimentSHA256: request.Experiment.NormalizedSHA256,
		ContentSHA256: digestWorkspaceTree(tree, request.Experiment.NormalizedSHA256),
	}
	if request.Experiment.PreviousSkill != nil {
		workspace.PreviousSkillSHA256 = request.Experiment.PreviousSkill.ContentSHA256
	}
	var report reportAccumulator
	report.add(ReportActivationUnbound, "execution.skill_activation", DispositionUnsupported, true)
	for _, location := range locations {
		run, err := decodeWorkspaceRun(tree, request.Format, location, &report)
		if err != nil {
			return WorkspaceImportResult{}, err
		}
		workspace.Runs = append(workspace.Runs, run)
	}
	benchmarkEntry, benchmarkPresent := tree.files["benchmark.json"]
	if !benchmarkPresent {
		report.add(ReportBenchmarkMissing, "workspace.benchmark", DispositionOmitted, false)
	} else {
		workspace.BenchmarkPresent = true
		workspace.BenchmarkFile = snapshotFile("benchmark.json", benchmarkEntry)
		decoded, err := decodeWorkspaceBenchmark(benchmarkEntry.data, request.Format, request.Experiment.Baseline)
		if err != nil {
			return WorkspaceImportResult{}, err
		}
		report.add(ReportBenchmarkSummaryPreserved, "benchmark.run_summary", DispositionPreservedSourceOnly, false)
		if request.Format == FormatAgentSkillsGuideV1 {
			workspace.FeedbackPresent = decoded.feedbackPresent
			workspace.Feedback = append([]FeedbackEntry(nil), decoded.feedback...)
		} else {
			if decoded.metadata.SkillName != request.Experiment.Skill.Name {
				return WorkspaceImportResult{}, contractError(ErrorInvalidWorkspace, fmt.Errorf("benchmark skill binding"))
			}
			workspace.Metadata = decoded.metadata
			workspace.NotesPresent = decoded.view.NotesPresent
			workspace.Notes = append([]string(nil), decoded.view.Notes...)
			report.addCount(ReportRunMetadataPreserved, "benchmark.runs[].eval_name", DispositionPreservedSourceOnly, false, countSlice(decoded.view.Runs))
			if decoded.metadata.SkillPathPresent {
				report.add(ReportPathOmitted, "benchmark.metadata.skill_path", DispositionPreservedSourceOnly, false)
			}
			if decoded.precisionLoss > 0 {
				report.addCount(ReportMetricUnsupported, "benchmark.runs[].result.time_seconds", DispositionPreservedSourceOnly, false, decoded.precisionLoss)
			}
			if err := reconcileBenchmarkRuns(workspace.Runs, decoded.view.Runs); err != nil {
				return WorkspaceImportResult{}, err
			}
		}
	}
	if err := validateWorkspaceCoverage(workspace.Runs, request.Experiment, &report); err != nil {
		return WorkspaceImportResult{}, err
	}
	reportWorkspaceMetricCoverage(workspace.Runs, &report)
	return WorkspaceImportResult{Workspace: workspace, Report: report.report()}, nil
}

func bindWorkspaceCasePaths(format Format, experiment Experiment, mappings []CaseDirectory) (map[uint32]string, error) {
	cases := make(map[uint32]struct{}, len(experiment.Cases))
	for _, testCase := range experiment.Cases {
		cases[testCase.ID] = struct{}{}
	}
	result := make(map[uint32]string, len(cases))
	if format == FormatAnthropicSkillCreatorV1 {
		if len(mappings) != 0 {
			return nil, contractError(ErrorInvalidRequest, fmt.Errorf("anthropic case mappings are inferred"))
		}
		for id := range cases {
			result[id] = "eval-" + strconv.FormatUint(uint64(id), 10)
		}
		return result, nil
	}
	if len(mappings) != len(cases) {
		return nil, contractError(ErrorInvalidRequest, fmt.Errorf("guide case mapping incomplete"))
	}
	seenPaths := make(map[string]struct{}, len(mappings))
	var sharedIteration uint32
	for _, mapping := range mappings {
		iteration, validPath := parseGuideCaseDirectory(mapping.Path)
		if _, ok := cases[mapping.CaseID]; !ok || !validPath {
			return nil, contractError(ErrorInvalidRequest, fmt.Errorf("guide case mapping"))
		}
		if sharedIteration == 0 {
			sharedIteration = iteration
		} else if iteration != sharedIteration {
			return nil, contractError(ErrorInvalidRequest, fmt.Errorf("mixed guide iterations"))
		}
		if _, duplicate := result[mapping.CaseID]; duplicate {
			return nil, contractError(ErrorInvalidRequest, fmt.Errorf("duplicate guide case id"))
		}
		if _, duplicate := seenPaths[mapping.Path]; duplicate {
			return nil, contractError(ErrorInvalidRequest, fmt.Errorf("duplicate guide case path"))
		}
		result[mapping.CaseID] = mapping.Path
		seenPaths[mapping.Path] = struct{}{}
	}
	return result, nil
}

func parseGuideCaseDirectory(value string) (uint32, bool) {
	if !validSourcePath(value) {
		return 0, false
	}
	components := strings.Split(value, "/")
	if len(components) != 2 || !strings.HasPrefix(components[0], "iteration-") ||
		!strings.HasPrefix(components[1], "eval-") {
		return 0, false
	}
	iterationText := strings.TrimPrefix(components[0], "iteration-")
	iteration, err := strconv.ParseUint(iterationText, 10, 32)
	if err != nil || iteration == 0 || iterationText != strconv.FormatUint(iteration, 10) ||
		!validGuideWorkspaceSlug(strings.TrimPrefix(components[1], "eval-")) {
		return 0, false
	}
	return uint32(iteration), true
}

func validGuideWorkspaceSlug(value string) bool {
	if value == "" || len(value) > 128 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	previousHyphen := false
	for _, character := range []byte(value) {
		if character == '-' {
			if previousHyphen {
				return false
			}
			previousHyphen = true
			continue
		}
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
		previousHyphen = false
	}
	return true
}

func discoverWorkspaceRuns(tree capturedTree, format Format, experiment Experiment, casePaths map[uint32]string) ([]workspaceRunLocation, error) {
	directories := make(map[string]struct{})
	for _, entry := range tree.entries {
		if entry.isDir {
			directories[entry.path] = struct{}{}
		}
	}
	treatments := workspaceTreatments(experiment.Baseline)
	locations := make([]workspaceRunLocation, 0)
	for _, testCase := range experiment.Cases {
		for _, treatment := range treatments {
			cell := casePaths[testCase.ID] + "/" + configurationName(treatment)
			if format == FormatAgentSkillsGuideV1 {
				locations = append(locations, workspaceRunLocation{
					caseID: testCase.ID, configuration: treatment, runNumber: 1, prefix: cell,
				})
				continue
			}
			runNumbers := make(map[uint32]struct{})
			prefix := cell + "/"
			for directory := range directories {
				if !strings.HasPrefix(directory, prefix) {
					continue
				}
				remainder := strings.TrimPrefix(directory, prefix)
				if strings.Contains(remainder, "/") || !strings.HasPrefix(remainder, "run-") {
					continue
				}
				number, err := strconv.ParseUint(strings.TrimPrefix(remainder, "run-"), 10, 32)
				if err != nil || number == 0 || number > MaxAttempts || remainder != "run-"+strconv.FormatUint(number, 10) {
					return nil, contractError(ErrorInvalidWorkspace, fmt.Errorf("invalid run directory"))
				}
				runNumbers[uint32(number)] = struct{}{}
			}
			for number := uint32(1); number <= countMap(runNumbers); number++ {
				if _, ok := runNumbers[number]; !ok {
					return nil, contractError(ErrorInvalidWorkspace, fmt.Errorf("sparse run directories"))
				}
				locations = append(locations, workspaceRunLocation{
					caseID: testCase.ID, configuration: treatment, runNumber: number,
					prefix: cell + "/run-" + strconv.FormatUint(uint64(number), 10),
				})
			}
		}
	}
	sort.Slice(locations, func(i, j int) bool {
		if locations[i].caseID != locations[j].caseID {
			return locations[i].caseID < locations[j].caseID
		}
		if locations[i].configuration != locations[j].configuration {
			return locations[i].configuration == TreatmentCurrentSkill
		}
		return locations[i].runNumber < locations[j].runNumber
	})
	return locations, nil
}

func workspaceTreatments(baseline Baseline) []TreatmentKind {
	result := []TreatmentKind{TreatmentCurrentSkill, TreatmentNoSkill}
	if baseline == BaselinePreviousSkill {
		result[1] = TreatmentPreviousSkill
	}
	return result
}

func validateWorkspaceLayout(tree capturedTree, locations []workspaceRunLocation, casePaths map[uint32]string, baseline Baseline) error {
	allowedDirectories := make(map[string]struct{})
	for _, casePath := range casePaths {
		for current := casePath; current != "."; current = path.Dir(current) {
			allowedDirectories[current] = struct{}{}
		}
		for _, treatment := range workspaceTreatments(baseline) {
			allowedDirectories[casePath+"/"+configurationName(treatment)] = struct{}{}
		}
	}
	for _, location := range locations {
		casePath := casePaths[location.caseID]
		cell := casePath + "/" + configurationName(location.configuration)
		allowedDirectories[cell] = struct{}{}
		if location.prefix != cell {
			allowedDirectories[location.prefix] = struct{}{}
		}
		allowedDirectories[location.prefix+"/outputs"] = struct{}{}
	}
	for _, entry := range tree.entries {
		if entry.path == "benchmark.json" && !entry.isDir {
			continue
		}
		matched := false
		for _, location := range locations {
			if entry.path == location.prefix+"/grading.json" || entry.path == location.prefix+"/timing.json" {
				matched = !entry.isDir
				break
			}
			outputPrefix := location.prefix + "/outputs/"
			if strings.HasPrefix(entry.path, outputPrefix) {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if entry.isDir {
			if _, ok := allowedDirectories[entry.path]; ok {
				continue
			}
		}
		return contractError(ErrorInvalidWorkspace, fmt.Errorf("unknown workspace entry"))
	}
	return nil
}

func decodeWorkspaceRun(tree capturedTree, format Format, location workspaceRunLocation, report *reportAccumulator) (WorkspaceRun, error) {
	run := WorkspaceRun{
		CaseID: location.caseID, Configuration: location.configuration, RunNumber: location.runNumber,
		DurationMillis: OptionalUint64{Presence: MetricUnknown}, TotalTokens: OptionalUint64{Presence: MetricUnknown},
		EstimatedCostMicroUSD: OptionalUint64{Presence: MetricUnsupported},
		Grading: GradingView{
			DurationMillis:        OptionalUint64{Presence: MetricUnknown},
			TotalTokens:           OptionalUint64{Presence: MetricUnknown},
			EstimatedCostMicroUSD: OptionalUint64{Presence: MetricUnsupported},
		},
	}
	outputDirectory := location.prefix + "/outputs"
	for _, entry := range tree.entries {
		if entry.path == outputDirectory && entry.isDir {
			run.OutputsPresent = true
		}
	}
	for filePath, entry := range tree.files {
		prefix := outputDirectory + "/"
		if !strings.HasPrefix(filePath, prefix) {
			continue
		}
		relative := strings.TrimPrefix(filePath, prefix)
		output := snapshotFile(relative, entry)
		run.Outputs = append(run.Outputs, *output)
	}
	sort.Slice(run.Outputs, func(i, j int) bool { return run.Outputs[i].Path < run.Outputs[j].Path })
	if len(run.Outputs) > MaxOutputsPerRun {
		return WorkspaceRun{}, contractError(ErrorLimitExceeded, fmt.Errorf("workspace outputs"))
	}
	if !run.OutputsPresent {
		report.add(ReportOutputsMissing, "workspace.runs[].outputs", DispositionOmitted, false)
	}

	timingPath := location.prefix + "/timing.json"
	if entry, ok := tree.files[timingPath]; ok {
		run.TimingPresent = true
		run.TimingFile = snapshotFile(timingPath, entry)
		if format == FormatAgentSkillsGuideV1 {
			decoded, err := decodeGuideTiming(entry.data)
			if err != nil {
				return WorkspaceRun{}, err
			}
			run.DurationMillis, run.TotalTokens = decoded.duration, decoded.tokens
		} else {
			decoded, err := decodeAnthropicTiming(entry.data)
			if err != nil {
				return WorkspaceRun{}, err
			}
			run.DurationMillis, run.TotalTokens = decoded.duration, decoded.tokens
			if decoded.detailCount > 0 {
				report.addCount(ReportTimingDetailPreserved, "workspace.runs[].timing", DispositionPreservedSourceOnly, false, decoded.detailCount)
			}
		}
	} else {
		report.add(ReportTimingMissing, "workspace.runs[].timing", DispositionOmitted, false)
	}

	gradingPath := location.prefix + "/grading.json"
	if entry, ok := tree.files[gradingPath]; ok {
		run.GradingPresent = true
		run.GradingFile = snapshotFile(gradingPath, entry)
		decoded, err := decodeWorkspaceGrading(entry.data, format)
		if err != nil {
			return WorkspaceRun{}, err
		}
		if decoded.timingDetailCount > 0 {
			report.addCount(ReportTimingDetailPreserved, "workspace.runs[].grading.timing", DispositionPreservedSourceOnly, false, decoded.timingDetailCount)
		}
		if decoded.precisionLoss {
			report.add(ReportMetricUnsupported, "workspace.runs[].grading.timing.total_duration_seconds", DispositionPreservedSourceOnly, false)
		}
		if format == FormatAnthropicSkillCreatorV1 {
			merged, err := mergeWorkspaceMetric(run.DurationMillis, decoded.view.DurationMillis)
			if err != nil {
				return WorkspaceRun{}, contractError(ErrorInvalidWorkspace, err)
			}
			run.DurationMillis = merged
		}
		run.Grading = decoded.view
		run.Grading.DurationMillis = run.DurationMillis
		run.Grading.TotalTokens = run.TotalTokens
		run.Grading.EstimatedCostMicroUSD = run.EstimatedCostMicroUSD
	} else {
		report.add(ReportGradingMissing, "workspace.runs[].grading", DispositionOmitted, false)
	}
	return run, nil
}

func reconcileBenchmarkRuns(workspaceRuns []WorkspaceRun, benchmarkRuns []BenchmarkRun) error {
	if len(workspaceRuns) != len(benchmarkRuns) {
		return contractError(ErrorInvalidWorkspace, fmt.Errorf("benchmark run inventory"))
	}
	byKey := make(map[string]int, len(workspaceRuns))
	for index, run := range workspaceRuns {
		byKey[workspaceRunKey(run.CaseID, run.Configuration, run.RunNumber)] = index
	}
	for _, benchmark := range benchmarkRuns {
		index, ok := byKey[workspaceRunKey(benchmark.CaseID, benchmark.Configuration, benchmark.RunNumber)]
		if !ok {
			return contractError(ErrorInvalidWorkspace, fmt.Errorf("benchmark run identity"))
		}
		run := &workspaceRuns[index]
		run.EvalName = benchmark.EvalName
		if run.GradingPresent && !sameGradeResults(run.Grading.Results, benchmark.Grading.Results) {
			return contractError(ErrorInvalidWorkspace, fmt.Errorf("benchmark grading mismatch"))
		}
		if !run.GradingPresent {
			run.Grading = benchmark.Grading
		}
		var err error
		run.DurationMillis, err = mergeWorkspaceMetric(run.DurationMillis, benchmark.Grading.DurationMillis)
		if err != nil {
			return contractError(ErrorInvalidWorkspace, err)
		}
		run.TotalTokens, err = mergeWorkspaceMetric(run.TotalTokens, benchmark.Grading.TotalTokens)
		if err != nil {
			return contractError(ErrorInvalidWorkspace, err)
		}
		run.EstimatedCostMicroUSD, err = mergeWorkspaceMetric(run.EstimatedCostMicroUSD, benchmark.Grading.EstimatedCostMicroUSD)
		if err != nil {
			return contractError(ErrorInvalidWorkspace, err)
		}
		if run.GradingPresent {
			run.Grading.DurationMillis = run.DurationMillis
			run.Grading.TotalTokens = run.TotalTokens
			run.Grading.EstimatedCostMicroUSD = run.EstimatedCostMicroUSD
		}
		run.NotesPresent = benchmark.NotesPresent
		run.Notes = append([]string(nil), benchmark.Notes...)
		delete(byKey, workspaceRunKey(benchmark.CaseID, benchmark.Configuration, benchmark.RunNumber))
	}
	if len(byKey) != 0 {
		return contractError(ErrorInvalidWorkspace, fmt.Errorf("benchmark run inventory"))
	}
	return nil
}

func gradingExactlyCoversCase(testCase Case, grading GradingView) bool {
	if !testCase.CriteriaPresent || len(testCase.Criteria) == 0 || len(grading.Results) != len(testCase.Criteria) {
		return false
	}
	for index := range testCase.Criteria {
		if grading.Results[index].Text != testCase.Criteria[index].Text {
			return false
		}
	}
	return true
}

func validateWorkspaceCoverage(runs []WorkspaceRun, experiment Experiment, report *reportAccumulator) error {
	casesByID := make(map[uint32]Case, len(experiment.Cases))
	for _, testCase := range experiment.Cases {
		casesByID[testCase.ID] = testCase
	}
	for _, run := range runs {
		testCase, ok := casesByID[run.CaseID]
		if !ok {
			return contractError(ErrorInvalidWorkspace, fmt.Errorf("workspace case coverage"))
		}
		if !testCase.CriteriaPresent || len(testCase.Criteria) == 0 {
			if len(run.Grading.Results) != 0 {
				return contractError(ErrorInvalidWorkspace, fmt.Errorf("grading without criteria"))
			}
			report.add(ReportVerifierCoverageUnbound, "verification.criteria_coverage", DispositionUnsupported, true)
			continue
		}
		if len(run.Grading.Results) == 0 {
			report.add(ReportVerifierCoverageMissing, "verification.criteria_coverage", DispositionOmitted, true)
			continue
		}
		if !gradingExactlyCoversCase(testCase, run.Grading) {
			return contractError(ErrorInvalidWorkspace, fmt.Errorf("grading criteria coverage"))
		}
	}
	return nil
}

func reportWorkspaceMetricCoverage(runs []WorkspaceRun, report *reportAccumulator) {
	for _, run := range runs {
		reportMetricAbsence(report, "workspace.runs[].duration_millis", run.DurationMillis)
		reportMetricAbsence(report, "workspace.runs[].total_tokens", run.TotalTokens)
		reportMetricForUnrepresented(report, "workspace.runs[].estimated_cost_microusd", run.EstimatedCostMicroUSD)
	}
}

func mergeWorkspaceMetric(first, second OptionalUint64) (OptionalUint64, error) {
	if second.Presence == MetricUnknown {
		return first, nil
	}
	if first.Presence == MetricUnknown || first.Presence == MetricUnsupported {
		return second, nil
	}
	if second.Presence == MetricUnsupported {
		return first, nil
	}
	if first.Presence != second.Presence || first.Value != second.Value {
		return OptionalUint64{}, fmt.Errorf("metric mismatch")
	}
	return first, nil
}

func sameGradeResults(first, second []GradeResult) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func workspaceRunKey(caseID uint32, configuration TreatmentKind, runNumber uint32) string {
	return strconv.FormatUint(uint64(caseID), 10) + "/" + string(configuration) + "/" + strconv.FormatUint(uint64(runNumber), 10)
}

func snapshotFile(name string, entry capturedEntry) *SnapshotFile {
	return &SnapshotFile{
		Path: name, SHA256: entry.digest, SizeBytes: uint64(len(entry.data)), Data: append([]byte(nil), entry.data...),
	}
}

func digestWorkspaceTree(tree capturedTree, experimentDigest string) string {
	builder := newDigestBuilder("workspace-source")
	builder.addString(experimentDigest)
	for _, entry := range tree.entries {
		builder.addString(entry.path)
		if entry.isDir {
			builder.addString("directory")
		} else {
			builder.addString("file")
			builder.add(entry.data)
		}
	}
	return builder.sum()
}
