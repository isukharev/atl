package agentskills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	publicationMarker       = ".agentskills-publication-incomplete"
	maxPublicationDataBytes = MaxTreeBytes - (SHA256HexCharacters + 1)
)

// PlanWorkspacePublication deterministically plans local, non-authoritative
// compatibility files for an explicit format.
func PlanWorkspacePublication(request WorkspacePublicationRequest) (WorkspacePublicationPlan, error) {
	if request.Format == FormatAuto || request.Format != request.Experiment.Format ||
		(request.Format != FormatAgentSkillsGuideV1 && request.Format != FormatAnthropicSkillCreatorV1) {
		return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, nil)
	}
	if err := validateExperimentForProjection(request.Experiment); err != nil {
		return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
	}
	casePaths, err := bindWorkspaceCasePaths(request.Format, request.Experiment, request.CaseDirectories)
	if err != nil {
		return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
	}
	runs, _, actualBaseline, err := validateAndSortBenchmark(request.Benchmark)
	if err != nil || request.Benchmark.SkillName != request.Experiment.Skill.Name {
		return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
	}
	expectedBaseline := TreatmentNoSkill
	if request.Experiment.Baseline == BaselinePreviousSkill {
		expectedBaseline = TreatmentPreviousSkill
	}
	if actualBaseline != expectedBaseline {
		return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, fmt.Errorf("publication baseline"))
	}
	sourceRuns, err := bindPublicationSource(request, runs, casePaths)
	if err != nil {
		return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
	}
	casesByID := make(map[uint32]Case, len(request.Experiment.Cases))
	for _, testCase := range request.Experiment.Cases {
		casesByID[testCase.ID] = testCase
	}
	files := make([]PublicationFile, 0, len(runs)+1)
	var plannedBytes uint64
	var report reportAccumulator
	if request.Source != nil && request.Source.BenchmarkPresent {
		report.add(ReportBenchmarkSummaryPreserved, "publication.source.benchmark", DispositionPreservedSourceOnly, false)
	}
	for _, run := range runs {
		testCase, caseExists := casesByID[run.CaseID]
		if !caseExists || !gradingExactlyCoversCase(testCase, run.Grading) ||
			(run.Configuration != TreatmentCurrentSkill && run.Configuration != expectedBaseline) ||
			(request.Format == FormatAgentSkillsGuideV1 && run.RunNumber != 1) {
			return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, fmt.Errorf("publication run binding"))
		}
		artifact, err := encodeGrading(request.Format, run.Grading, true)
		if err != nil {
			return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
		}
		mergeReportInto(&report, artifact.Report)
		prefix := casePaths[run.CaseID] + "/" + configurationName(run.Configuration)
		if request.Format == FormatAnthropicSkillCreatorV1 {
			prefix += fmt.Sprintf("/run-%d", run.RunNumber)
		}
		if request.Source == nil {
			report.add(ReportOutputsOmitted, "publication.runs[].outputs", DispositionOmitted, false)
			report.add(ReportTimingMissing, "publication.runs[].timing", DispositionOmitted, false)
		} else {
			source := sourceRuns[workspaceRunKey(run.CaseID, run.Configuration, run.RunNumber)]
			if source.OutputsPresent && len(source.Outputs) == 0 {
				report.add(ReportOutputsOmitted, "publication.runs[].outputs", DispositionOmitted, false)
			} else if !source.OutputsPresent {
				report.add(ReportOutputsMissing, "publication.runs[].outputs", DispositionOmitted, false)
			} else {
				for _, output := range source.Outputs {
					if err := appendPublicationFile(&files, &plannedBytes, prefix+"/outputs/"+output.Path, output.Data); err != nil {
						return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
					}
				}
				report.addCount(ReportOutputsPreservedSourceOnly, "publication.runs[].outputs", DispositionPreservedSourceOnly, false, countSlice(source.Outputs))
			}
			if !source.TimingPresent {
				report.add(ReportTimingMissing, "publication.runs[].timing", DispositionOmitted, false)
			} else {
				if err := appendPublicationFile(&files, &plannedBytes, prefix+"/timing.json", source.TimingFile.Data); err != nil {
					return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
				}
				report.add(ReportTimingPreservedSourceOnly, "publication.runs[].timing", DispositionPreservedSourceOnly, false)
			}
		}
		if err := appendPublicationFile(&files, &plannedBytes, prefix+"/grading.json", artifact.Data); err != nil {
			return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
		}
	}
	benchmark, err := encodeBenchmark(request.Format, request.Benchmark, true)
	if err != nil {
		return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
	}
	mergeReportInto(&report, benchmark.Report)
	if err := appendPublicationFile(&files, &plannedBytes, "benchmark.json", benchmark.Data); err != nil {
		return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
	}
	if request.Experiment.PreviousSkill != nil {
		report.add(ReportSkillDigestPreserved, "publication.previous_skill_sha256", DispositionPreservedSourceOnly, false)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	plan := WorkspacePublicationPlan{
		Format: request.Format, Baseline: request.Experiment.Baseline,
		ExperimentSHA256: request.Experiment.NormalizedSHA256,
		CaseDirectories:  publicationCaseDirectories(request.Format, runs, casePaths),
		Files:            files, Report: report.report(),
	}
	if request.Experiment.PreviousSkill != nil {
		plan.PreviousSkillSHA256 = request.Experiment.PreviousSkill.ContentSHA256
	}
	plan.ContentSHA256 = digestPublication(plan)
	if err := validatePublicationPlan(plan); err != nil {
		return WorkspacePublicationPlan{}, contractError(ErrorInvalidPublication, err)
	}
	return plan, nil
}

func publicationCaseDirectories(format Format, runs []BenchmarkRun, casePaths map[uint32]string) []CaseDirectory {
	if format != FormatAgentSkillsGuideV1 {
		return nil
	}
	seen := make(map[uint32]struct{})
	result := make([]CaseDirectory, 0)
	for _, run := range runs {
		if _, ok := seen[run.CaseID]; ok {
			continue
		}
		seen[run.CaseID] = struct{}{}
		result = append(result, CaseDirectory{CaseID: run.CaseID, Path: casePaths[run.CaseID]})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CaseID < result[j].CaseID })
	return result
}

func newPublicationFile(name string, data []byte) PublicationFile {
	return PublicationFile{
		Path: name, SHA256: digestBytes(data), SizeBytes: uint64(len(data)), Data: append([]byte(nil), data...),
	}
}

func appendPublicationFile(files *[]PublicationFile, total *uint64, name string, data []byte) error {
	size := uint64(len(data))
	if len(*files) >= MaxTreeEntries-1 || *total > maxPublicationDataBytes || size > maxPublicationDataBytes-*total {
		return fmt.Errorf("publication byte bound")
	}
	*files = append(*files, newPublicationFile(name, data))
	*total += size
	return nil
}

func bindPublicationSource(request WorkspacePublicationRequest, runs []BenchmarkRun, casePaths map[uint32]string) (map[string]WorkspaceRun, error) {
	if request.Source == nil {
		return nil, nil
	}
	source := request.Source
	expectedPrevious := ""
	if request.Experiment.PreviousSkill != nil {
		expectedPrevious = request.Experiment.PreviousSkill.ContentSHA256
	}
	if source.Format != request.Format || source.ExperimentSHA256 != request.Experiment.NormalizedSHA256 ||
		source.PreviousSkillSHA256 != expectedPrevious || !validDigest(source.ContentSHA256) || len(source.Runs) > MaxRuns {
		return nil, fmt.Errorf("publication source identity")
	}
	selected := make(map[string]BenchmarkRun, len(runs))
	for _, run := range runs {
		selected[workspaceRunKey(run.CaseID, run.Configuration, run.RunNumber)] = run
	}
	bound := make(map[string]WorkspaceRun, len(runs))
	seen := make(map[string]struct{}, len(source.Runs))
	for _, run := range source.Runs {
		key := workspaceRunKey(run.CaseID, run.Configuration, run.RunNumber)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate publication source run")
		}
		seen[key] = struct{}{}
		selectedRun, isSelected := selected[key]
		if !isSelected {
			if workspaceRunHasArtifacts(run) {
				return nil, fmt.Errorf("unselected publication source artifacts")
			}
			continue
		}
		prefix := casePaths[run.CaseID] + "/" + configurationName(run.Configuration)
		if request.Format == FormatAnthropicSkillCreatorV1 {
			prefix += fmt.Sprintf("/run-%d", run.RunNumber)
		}
		if err := validatePublicationSourceRun(run, selectedRun, prefix, request.Format); err != nil {
			return nil, err
		}
		bound[key] = run
	}
	if len(bound) != len(selected) {
		return nil, fmt.Errorf("publication source run inventory")
	}
	return bound, nil
}

func workspaceRunHasArtifacts(run WorkspaceRun) bool {
	return run.OutputsPresent || len(run.Outputs) != 0 || run.TimingPresent || run.TimingFile != nil ||
		run.GradingPresent || run.GradingFile != nil || len(run.Grading.Results) != 0 || run.NotesPresent || len(run.Notes) != 0
}

func validatePublicationSourceRun(source WorkspaceRun, selected BenchmarkRun, prefix string, format Format) error {
	if source.CaseID != selected.CaseID || source.Configuration != selected.Configuration || source.RunNumber != selected.RunNumber ||
		(!source.OutputsPresent && len(source.Outputs) != 0) || len(source.Outputs) > MaxOutputsPerRun ||
		source.TimingPresent != (source.TimingFile != nil) {
		return fmt.Errorf("publication source run")
	}
	seenOutputs := make(map[string]struct{}, len(source.Outputs))
	for _, output := range source.Outputs {
		if !validSnapshotFile(output, MaxFileBytes) || !validSourcePath(output.Path) {
			return fmt.Errorf("publication source output")
		}
		if _, duplicate := seenOutputs[output.Path]; duplicate {
			return fmt.Errorf("duplicate publication source output")
		}
		seenOutputs[output.Path] = struct{}{}
	}
	if source.TimingPresent {
		if source.TimingFile.Path != prefix+"/timing.json" || !validSnapshotFile(*source.TimingFile, MaxJSONBytes) {
			return fmt.Errorf("publication source timing")
		}
		var decoded decodedTiming
		var err error
		if format == FormatAgentSkillsGuideV1 {
			decoded, err = decodeGuideTiming(source.TimingFile.Data)
		} else {
			decoded, err = decodeAnthropicTiming(source.TimingFile.Data)
		}
		if err != nil || source.DurationMillis != decoded.duration || source.TotalTokens != decoded.tokens ||
			selected.Grading.DurationMillis != decoded.duration || selected.Grading.TotalTokens != decoded.tokens {
			return fmt.Errorf("publication source timing")
		}
	}
	return nil
}

func validSnapshotFile(file SnapshotFile, maximum uint64) bool {
	return validSourcePath(file.Path) && file.SizeBytes == uint64(len(file.Data)) && file.SizeBytes <= maximum &&
		file.SHA256 == digestBytes(file.Data)
}

func mergeReportInto(accumulator *reportAccumulator, report Report) {
	for _, entry := range report.Entries {
		accumulator.addCount(entry.Code, entry.Scope, entry.Disposition, entry.BlocksExecution, entry.Count)
	}
}

func digestPublication(plan WorkspacePublicationPlan) string {
	builder := newDigestBuilder("workspace-publication")
	builder.addString(string(plan.Format))
	builder.addString(string(plan.Baseline))
	builder.addString(plan.ExperimentSHA256)
	builder.addString(plan.PreviousSkillSHA256)
	for _, directory := range plan.CaseDirectories {
		builder.addString(strconv.FormatUint(uint64(directory.CaseID), 10))
		builder.addString(directory.Path)
	}
	for _, file := range plan.Files {
		builder.addString(file.Path)
		builder.add(file.Data)
	}
	return builder.sum()
}

func validatePublicationPlan(plan WorkspacePublicationPlan) error {
	if (plan.Format != FormatAgentSkillsGuideV1 && plan.Format != FormatAnthropicSkillCreatorV1) ||
		(plan.Baseline != BaselineNoSkill && plan.Baseline != BaselinePreviousSkill) ||
		!validDigest(plan.ExperimentSHA256) ||
		(plan.PreviousSkillSHA256 != "" && !validDigest(plan.PreviousSkillSHA256)) ||
		(plan.Baseline == BaselineNoSkill && plan.PreviousSkillSHA256 != "") ||
		(plan.Baseline == BaselinePreviousSkill && !validDigest(plan.PreviousSkillSHA256)) ||
		len(plan.Files) == 0 || len(plan.Files) >= MaxTreeEntries || !validDigest(plan.ContentSHA256) {
		return fmt.Errorf("publication contract")
	}
	var total uint64
	previous := ""
	benchmarkCount := 0
	var benchmarkData []byte
	for _, file := range plan.Files {
		maximum := publicationFileMaximum(file.Path)
		if !validSourcePath(file.Path) || file.Path == publicationMarker || file.Path <= previous || maximum == 0 ||
			file.SizeBytes != uint64(len(file.Data)) || file.SizeBytes > maximum ||
			file.SHA256 != digestBytes(file.Data) {
			return fmt.Errorf("publication file")
		}
		if file.Path == "benchmark.json" {
			benchmarkCount++
			benchmarkData = file.Data
		}
		total += file.SizeBytes
		if total > maxPublicationDataBytes {
			return fmt.Errorf("publication byte bound")
		}
		previous = file.Path
	}
	if benchmarkCount != 1 {
		return fmt.Errorf("benchmark publication missing")
	}
	if err := validatePublicationLayout(plan, benchmarkData); err != nil {
		return err
	}
	if len(plan.Files)+len(publicationDirectories(plan.Files))+1 > MaxTreeEntries {
		return fmt.Errorf("publication entry bound")
	}
	if digestPublication(plan) != plan.ContentSHA256 {
		return fmt.Errorf("publication digest")
	}
	return nil
}

func publicationFileMaximum(name string) uint64 {
	if name == "benchmark.json" || path.Base(name) == "grading.json" || path.Base(name) == "timing.json" {
		return MaxJSONBytes
	}
	if strings.Contains(name, "/outputs/") {
		return MaxFileBytes
	}
	return 0
}

type publicationCellKey struct {
	caseID        uint32
	configuration TreatmentKind
	runNumber     uint32
}

type publicationCellState struct {
	gradingPresent bool
	grading        GradingView
}

func validatePublicationLayout(plan WorkspacePublicationPlan, benchmarkData []byte) error {
	guideCases := make(map[string]uint32)
	if plan.Format == FormatAgentSkillsGuideV1 {
		if len(plan.CaseDirectories) == 0 || len(plan.CaseDirectories) > MaxCases {
			return fmt.Errorf("publication guide case mapping")
		}
		var previousID, iteration uint32
		for _, directory := range plan.CaseDirectories {
			currentIteration, ok := parseGuideCaseDirectory(directory.Path)
			if !ok || directory.CaseID == 0 || directory.CaseID <= previousID ||
				(iteration != 0 && currentIteration != iteration) {
				return fmt.Errorf("publication guide case mapping")
			}
			if _, duplicate := guideCases[directory.Path]; duplicate {
				return fmt.Errorf("publication guide case mapping")
			}
			iteration, previousID = currentIteration, directory.CaseID
			guideCases[directory.Path] = directory.CaseID
		}
	} else if len(plan.CaseDirectories) != 0 {
		return fmt.Errorf("publication anthropic case mapping")
	}

	expectedBaseline := TreatmentNoSkill
	if plan.Baseline == BaselinePreviousSkill {
		expectedBaseline = TreatmentPreviousSkill
	}
	cells := make(map[publicationCellKey]publicationCellState)
	usedGuideCases := make(map[uint32]struct{})
	for _, file := range plan.Files {
		if file.Path == "benchmark.json" {
			continue
		}
		key, artifact, err := publicationFileCell(plan.Format, file.Path, guideCases)
		if err != nil || (key.configuration != TreatmentCurrentSkill && key.configuration != expectedBaseline) {
			return fmt.Errorf("publication layout")
		}
		state := cells[key]
		if artifact == "grading" {
			if state.gradingPresent {
				return fmt.Errorf("duplicate publication grading")
			}
			decoded, err := decodeWorkspaceGrading(file.Data, plan.Format)
			if err != nil {
				return fmt.Errorf("publication grading")
			}
			state.gradingPresent, state.grading = true, decoded.view
		}
		cells[key] = state
		usedGuideCases[key.caseID] = struct{}{}
	}
	if len(cells) == 0 {
		return fmt.Errorf("publication cells missing")
	}
	pairs := make(map[[2]uint32]map[TreatmentKind]struct{})
	for key, state := range cells {
		if !state.gradingPresent {
			return fmt.Errorf("publication cell grading missing")
		}
		pair := [2]uint32{key.caseID, key.runNumber}
		if pairs[pair] == nil {
			pairs[pair] = make(map[TreatmentKind]struct{})
		}
		pairs[pair][key.configuration] = struct{}{}
	}
	for _, treatments := range pairs {
		if len(treatments) != 2 {
			return fmt.Errorf("publication cell pair incomplete")
		}
		if _, ok := treatments[TreatmentCurrentSkill]; !ok {
			return fmt.Errorf("publication current cell missing")
		}
		if _, ok := treatments[expectedBaseline]; !ok {
			return fmt.Errorf("publication baseline cell missing")
		}
	}
	if plan.Format == FormatAgentSkillsGuideV1 && len(usedGuideCases) != len(plan.CaseDirectories) {
		return fmt.Errorf("publication guide mapping unused")
	}
	decodedBenchmark, err := decodeWorkspaceBenchmark(benchmarkData, plan.Format, plan.Baseline)
	if err != nil {
		return fmt.Errorf("publication benchmark")
	}
	if plan.Format == FormatAnthropicSkillCreatorV1 {
		if len(decodedBenchmark.view.Runs) != len(cells) {
			return fmt.Errorf("publication benchmark inventory")
		}
		for _, run := range decodedBenchmark.view.Runs {
			key := publicationCellKey{caseID: run.CaseID, configuration: run.Configuration, runNumber: run.RunNumber}
			state, ok := cells[key]
			if !ok || !sameGradeResults(state.grading.Results, run.Grading.Results) {
				return fmt.Errorf("publication benchmark grading")
			}
		}
	}
	return nil
}

func publicationFileCell(format Format, name string, guideCases map[string]uint32) (publicationCellKey, string, error) {
	prefix, artifact := "", ""
	switch {
	case strings.HasSuffix(name, "/grading.json"):
		prefix, artifact = strings.TrimSuffix(name, "/grading.json"), "grading"
	case strings.HasSuffix(name, "/timing.json"):
		prefix, artifact = strings.TrimSuffix(name, "/timing.json"), "timing"
	case strings.Contains(name, "/outputs/"):
		prefix, artifact = strings.SplitN(name, "/outputs/", 2)[0], "output"
		output := strings.TrimPrefix(name, prefix+"/outputs/")
		if !validSourcePath(output) {
			return publicationCellKey{}, "", fmt.Errorf("publication output path")
		}
	default:
		return publicationCellKey{}, "", fmt.Errorf("publication artifact path")
	}
	components := strings.Split(prefix, "/")
	if format == FormatAgentSkillsGuideV1 {
		if len(components) != 3 {
			return publicationCellKey{}, "", fmt.Errorf("publication guide path")
		}
		caseID, ok := guideCases[strings.Join(components[:2], "/")]
		configuration, validConfiguration := treatmentFromConfiguration(components[2])
		if !ok || !validConfiguration {
			return publicationCellKey{}, "", fmt.Errorf("publication guide path")
		}
		return publicationCellKey{caseID: caseID, configuration: configuration, runNumber: 1}, artifact, nil
	}
	if len(components) != 3 || !strings.HasPrefix(components[0], "eval-") || !strings.HasPrefix(components[2], "run-") {
		return publicationCellKey{}, "", fmt.Errorf("publication anthropic path")
	}
	caseText, runText := strings.TrimPrefix(components[0], "eval-"), strings.TrimPrefix(components[2], "run-")
	caseValue, caseErr := strconv.ParseUint(caseText, 10, 32)
	runValue, runErr := strconv.ParseUint(runText, 10, 32)
	configuration, validConfiguration := treatmentFromConfiguration(components[1])
	if caseErr != nil || runErr != nil || caseValue == 0 || runValue == 0 || runValue > MaxAttempts ||
		caseText != strconv.FormatUint(caseValue, 10) || runText != strconv.FormatUint(runValue, 10) || !validConfiguration {
		return publicationCellKey{}, "", fmt.Errorf("publication anthropic path")
	}
	return publicationCellKey{
		caseID:        uint32(caseValue), // #nosec G115 -- ParseUint above bounds this value to 32 bits.
		configuration: configuration,
		runNumber:     uint32(runValue), // #nosec G115 -- ParseUint above bounds this value to 32 bits.
	}, artifact, nil
}

func clonePublicationPlan(plan WorkspacePublicationPlan) WorkspacePublicationPlan {
	result := plan
	result.CaseDirectories = append([]CaseDirectory(nil), plan.CaseDirectories...)
	result.Files = append([]PublicationFile(nil), plan.Files...)
	for index := range result.Files {
		result.Files[index].Data = append([]byte(nil), plan.Files[index].Data...)
	}
	result.Report.Entries = append([]ReportEntry(nil), plan.Report.Entries...)
	return result
}

type publicationHooks struct {
	beforeDestinationOpen   func(string)
	afterDestinationCreated func(string)
	beforeCompletion        func(string)
}

// WriteNew writes a validated plan into one exact, previously nonexistent
// absolute destination. Its marker detects cooperative process interruption;
// it is not an atomic transaction or a power-loss durability mechanism. The
// identity checks do not exclude hostile same-UID parent renames. Failures
// leave the newly created destination in place rather than deleting a path
// whose identity could have raced.
func (plan WorkspacePublicationPlan) WriteNew(destination string) error {
	return writePublicationWithHooks(plan, destination, publicationHooks{})
}

func writePublicationWithHooks(plan WorkspacePublicationPlan, destination string, hooks publicationHooks) error {
	if err := validatePublicationPlan(plan); err != nil {
		return contractError(ErrorInvalidPublication, err)
	}
	plan = clonePublicationPlan(plan)
	if err := validatePublicationPlan(plan); err != nil {
		return contractError(ErrorInvalidPublication, err)
	}
	if destination == "" || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return contractError(ErrorInvalidDestination, nil)
	}
	parentPath, base := filepath.Dir(destination), filepath.Base(destination)
	if base == "." || base == string(filepath.Separator) || !validSourcePath(base) {
		return contractError(ErrorInvalidDestination, nil)
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&fs.ModeSymlink != 0 {
		return contractError(ErrorInvalidDestination, err)
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return contractError(ErrorInvalidDestination, err)
	}
	defer func() { _ = parent.Close() }()
	if err := verifyStableTreeRoot(parentPath, parentInfo, parent); err != nil {
		return contractError(ErrorInvalidDestination, err)
	}
	if _, err := parent.Lstat(base); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return contractError(ErrorInvalidDestination, err)
	}
	if err := parent.Mkdir(base, 0o700); err != nil {
		return contractError(ErrorInvalidDestination, err)
	}
	createdInfo, createdErr := parent.Lstat(base)
	if createdErr != nil || !createdInfo.IsDir() || createdInfo.Mode()&fs.ModeSymlink != 0 {
		return contractError(ErrorPublicationFailed, createdErr)
	}
	if hooks.beforeDestinationOpen != nil {
		hooks.beforeDestinationOpen(destination)
	}
	destinationRoot, err := parent.OpenRoot(base)
	if err != nil {
		return contractError(ErrorPublicationFailed, err)
	}
	defer func() { _ = destinationRoot.Close() }()
	if !publicationDestinationOwned(parent, base, destinationRoot, createdInfo) ||
		!stableRootIdentity(parentPath, parentInfo, parent) {
		return contractError(ErrorPublicationFailed, fmt.Errorf("destination changed"))
	}
	if hooks.afterDestinationCreated != nil {
		hooks.afterDestinationCreated(destination)
	}
	if !publicationDestinationOwned(parent, base, destinationRoot, createdInfo) ||
		!stableRootIdentity(parentPath, parentInfo, parent) {
		return contractError(ErrorPublicationFailed, fmt.Errorf("destination changed"))
	}
	if err := writePublicationFile(destinationRoot, publicationMarker, []byte(plan.ContentSHA256+"\n")); err != nil {
		return contractError(ErrorPublicationFailed, err)
	}
	for _, planned := range plan.Files {
		directory := path.Dir(planned.Path)
		if directory != "." {
			if err := destinationRoot.MkdirAll(filepath.FromSlash(directory), 0o700); err != nil {
				return contractError(ErrorPublicationFailed, err)
			}
		}
		if err := writePublicationFile(destinationRoot, filepath.FromSlash(planned.Path), planned.Data); err != nil {
			return contractError(ErrorPublicationFailed, err)
		}
	}
	if hooks.beforeCompletion != nil {
		hooks.beforeCompletion(destination)
	}
	if !publicationDestinationOwned(parent, base, destinationRoot, createdInfo) ||
		!stableRootIdentity(parentPath, parentInfo, parent) {
		return contractError(ErrorPublicationFailed, fmt.Errorf("publication root changed"))
	}
	captured, err := readStableTreeRoot(destinationRoot)
	if err != nil || !publicationMatchesTree(plan, captured, true) {
		return contractError(ErrorPublicationFailed, err)
	}
	if !publicationDestinationOwned(parent, base, destinationRoot, createdInfo) ||
		!stableRootIdentity(parentPath, parentInfo, parent) {
		return contractError(ErrorPublicationFailed, fmt.Errorf("publication root changed"))
	}
	if err := destinationRoot.Remove(publicationMarker); err != nil {
		return contractError(ErrorPublicationFailed, err)
	}
	if !publicationDestinationOwned(parent, base, destinationRoot, createdInfo) ||
		!stableRootIdentity(parentPath, parentInfo, parent) {
		return contractError(ErrorPublicationFailed, fmt.Errorf("publication root changed"))
	}
	return nil
}

func stableRootIdentity(rootPath string, initial fs.FileInfo, root *os.Root) bool {
	ambient, ambientErr := os.Lstat(rootPath)
	opened, openedErr := root.Stat(".")
	return ambientErr == nil && openedErr == nil && ambient.IsDir() && opened.IsDir() &&
		ambient.Mode()&fs.ModeSymlink == 0 && os.SameFile(initial, ambient) && os.SameFile(initial, opened)
}

func publicationDestinationOwned(parent *os.Root, base string, destination *os.Root, created fs.FileInfo) bool {
	ambient, ambientErr := parent.Lstat(base)
	opened, openedErr := destination.Stat(".")
	return ambientErr == nil && openedErr == nil && ambient.IsDir() && opened.IsDir() &&
		ambient.Mode()&fs.ModeSymlink == 0 && os.SameFile(created, ambient) && os.SameFile(created, opened)
}

func writePublicationFile(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written := 0
	for written < len(data) {
		count, err := file.Write(data[written:])
		if err != nil || count == 0 {
			_ = file.Close()
			return fmt.Errorf("publication write")
		}
		written += count
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func publicationMatchesTree(plan WorkspacePublicationPlan, tree capturedTree, markerPresent bool) bool {
	expectedFiles := len(plan.Files)
	if markerPresent {
		expectedFiles++
		marker, ok := tree.files[publicationMarker]
		if !ok || string(marker.data) != plan.ContentSHA256+"\n" {
			return false
		}
	}
	if len(tree.files) != expectedFiles {
		return false
	}
	expectedDirectories := publicationDirectories(plan.Files)
	for _, planned := range plan.Files {
		entry, ok := tree.files[planned.Path]
		if !ok || entry.digest != planned.SHA256 || uint64(len(entry.data)) != planned.SizeBytes {
			return false
		}
	}
	actualDirectories := 0
	for _, entry := range tree.entries {
		if !entry.isDir {
			continue
		}
		actualDirectories++
		if _, ok := expectedDirectories[entry.path]; !ok {
			return false
		}
	}
	return actualDirectories == len(expectedDirectories)
}

func publicationDirectories(files []PublicationFile) map[string]struct{} {
	result := make(map[string]struct{})
	for _, file := range files {
		for directory := path.Dir(file.Path); directory != "."; directory = path.Dir(directory) {
			result[directory] = struct{}{}
		}
	}
	return result
}
