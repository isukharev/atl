package agentskills

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type importHooks struct {
	skill    stableTreeHooks
	evals    stableTreeHooks
	previous stableTreeHooks
}

// Import captures and validates a complete local Agent Skills evaluation
// definition without consulting environment variables or invoking anything.
func Import(request ImportRequest) (ImportResult, error) {
	return importWithHooks(request, importHooks{})
}

func importWithHooks(request ImportRequest, hooks importHooks) (ImportResult, error) {
	if request.SkillRoot == "" ||
		(request.Format != FormatAuto && request.Format != FormatAgentSkillsGuideV1 && request.Format != FormatAnthropicSkillCreatorV1) ||
		(request.Baseline != BaselineNoSkill && request.Baseline != BaselinePreviousSkill) ||
		(request.Baseline == BaselinePreviousSkill && request.PreviousSkillRoot == "") ||
		(request.Baseline == BaselineNoSkill && request.PreviousSkillRoot != "") {
		return ImportResult{}, contractError(ErrorInvalidRequest, nil)
	}

	skillTree, err := readStableTreeWithHooks(request.SkillRoot, hooks.skill)
	if err != nil {
		return ImportResult{}, err
	}
	skillDocument, ok := skillTree.files["SKILL.md"]
	if !ok {
		return ImportResult{}, contractError(ErrorInvalidSkill, fmt.Errorf("SKILL.md missing"))
	}
	metadata, err := parseSkillMetadata(skillDocument.data)
	if err != nil {
		return ImportResult{}, err
	}

	evalRoot := request.EvalRoot
	if evalRoot == "" {
		evalRoot = filepath.Join(request.SkillRoot, "evals")
	}
	evalFiles, evalData, excludedPrefix, err := captureEvalSource(request.SkillRoot, evalRoot, skillTree, hooks.evals)
	if err != nil {
		return ImportResult{}, err
	}
	decoded, err := decodeEvals(evalData, request.Format)
	if err != nil {
		return ImportResult{}, err
	}
	if decoded.skillName != metadata.name {
		return ImportResult{}, contractError(ErrorInvalidEvals, fmt.Errorf("skill names differ"))
	}

	skillFiles := snapshotFiles(skillTree, func(name string) bool {
		return name != excludedPrefix && !strings.HasPrefix(name, excludedPrefix+"/")
	})
	skillSnapshot := SkillSnapshot{
		Name: metadata.name, Files: skillFiles,
		ContentSHA256: digestSnapshotFiles("skill-snapshot", skillFiles),
	}

	var previous *SkillSnapshot
	var previousFiles []SnapshotFile
	var previousMetadata skillMetadata
	if request.Baseline == BaselinePreviousSkill {
		previousTree, err := readStableTreeWithHooks(request.PreviousSkillRoot, hooks.previous)
		if err != nil {
			return ImportResult{}, err
		}
		previousDocument, ok := previousTree.files["SKILL.md"]
		if !ok {
			return ImportResult{}, contractError(ErrorInvalidSkill, fmt.Errorf("previous SKILL.md missing"))
		}
		previousMetadata, err = parseSkillMetadata(previousDocument.data)
		if err != nil || previousMetadata.name != metadata.name {
			return ImportResult{}, contractError(ErrorInvalidSkill, err)
		}
		previousFiles = snapshotFiles(previousTree, func(name string) bool {
			return name != "evals" && !strings.HasPrefix(name, "evals/")
		})
		snapshot := SkillSnapshot{
			Name: previousMetadata.name, Files: previousFiles,
			ContentSHA256: digestSnapshotFiles("skill-snapshot", previousFiles),
		}
		previous = &snapshot
	}

	var expandedInputBytes uint64
	for _, source := range decoded.cases {
		for _, fileName := range source.files {
			entry, exists := skillTree.files[fileName]
			if !exists {
				return ImportResult{}, contractError(ErrorInvalidEvals, fmt.Errorf("input file unavailable"))
			}
			size := uint64(len(entry.data))
			if expandedInputBytes > MaxTreeBytes || size > MaxTreeBytes-expandedInputBytes {
				return ImportResult{}, contractError(ErrorLimitExceeded, fmt.Errorf("expanded input byte bound"))
			}
			expandedInputBytes += size
		}
	}

	cases := make([]Case, 0, len(decoded.cases))
	for _, source := range decoded.cases {
		current := Case{
			ID: source.id, Prompt: source.prompt, ExpectedOutput: source.expectedOutput,
			FilesPresent: source.filesPresent, CriteriaPresent: source.criteriaPresent,
		}
		for _, fileName := range source.files {
			entry := skillTree.files[fileName]
			current.Inputs = append(current.Inputs, InputFile{
				Path: fileName, SHA256: entry.digest, SizeBytes: uint64(len(entry.data)),
				Data: append([]byte(nil), entry.data...),
			})
		}
		for index, text := range source.criteria {
			current.Criteria = append(current.Criteria, Criterion{
				Kind: source.criterionKind, SourceField: source.criterionField,
				Ordinal: uint32(index + 1), Text: text,
			})
		}
		cases = append(cases, current)
	}

	experiment := Experiment{
		Format: decoded.format, Baseline: request.Baseline, Skill: skillSnapshot,
		PreviousSkill: previous, Cases: cases,
	}
	experiment.ContentSHA256 = digestExperimentSource(skillFiles, evalFiles, previousFiles)
	experiment.NormalizedSHA256 = digestNormalizedExperiment(experiment)

	var report reportAccumulator
	report.add(ReportRunnerUnbound, "execution.runner", DispositionUnsupported, true)
	report.add(ReportJudgeUnbound, "execution.judge", DispositionUnsupported, true)
	report.add(ReportSandboxUnbound, "execution.sandbox", DispositionUnsupported, true)
	report.add(ReportEnvironmentUnbound, "execution.environment", DispositionUnsupported, true)
	report.add(ReportActivationUnbound, "execution.skill_activation", DispositionUnsupported, true)
	report.add(ReportVerifierCoverageUnbound, "verification.criteria_coverage", DispositionUnsupported, true)
	report.add(ReportMetricUnknown, "execution.estimated_cost_microusd", DispositionOmitted, false)
	if metadata.hasAllowedTools || previousMetadata.hasAllowedTools {
		report.add(ReportAllowedToolsUnbound, "SKILL.md.allowed-tools", DispositionPreservedSourceOnly, true)
	}
	if metadata.hasCompatibility || previousMetadata.hasCompatibility {
		report.add(ReportCompatibilityUnbound, "SKILL.md.compatibility", DispositionPreservedSourceOnly, true)
	}
	return ImportResult{Experiment: experiment, Report: report.report()}, nil
}

func captureEvalSource(skillRoot, evalRoot string, skillTree capturedTree, hooks stableTreeHooks) ([]SnapshotFile, []byte, string, error) {
	skillAbsolute, skillErr := filepath.Abs(skillRoot)
	evalAbsolute, evalErr := filepath.Abs(evalRoot)
	if skillErr != nil || evalErr != nil {
		return nil, nil, "", contractError(ErrorInvalidRequest, fmt.Errorf("resolve root"))
	}
	relative, relErr := filepath.Rel(skillAbsolute, evalAbsolute)
	insideSkill := relErr == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	if insideSkill {
		prefix := filepath.ToSlash(relative)
		if !validSourcePath(prefix) {
			return nil, nil, "", contractError(ErrorInvalidRequest, fmt.Errorf("eval root relative path"))
		}
		manifestName := prefix + "/evals.json"
		manifest, ok := skillTree.files[manifestName]
		if !ok {
			return nil, nil, "", contractError(ErrorInvalidEvals, fmt.Errorf("evals.json missing"))
		}
		files := snapshotFiles(skillTree, func(name string) bool {
			return name == prefix || strings.HasPrefix(name, prefix+"/")
		})
		return files, append([]byte(nil), manifest.data...), prefix, nil
	}
	fromEval, fromEvalErr := filepath.Rel(evalAbsolute, skillAbsolute)
	evalContainsSkill := fromEvalErr == nil && fromEval != "." && fromEval != ".." &&
		!strings.HasPrefix(fromEval, ".."+string(filepath.Separator))
	if relative == "." || evalContainsSkill {
		return nil, nil, "", contractError(ErrorInvalidRequest, fmt.Errorf("skill and eval roots overlap"))
	}
	evalTree, err := readStableTreeWithHooks(evalRoot, hooks)
	if err != nil {
		return nil, nil, "", err
	}
	manifest, ok := evalTree.files["evals.json"]
	if !ok {
		return nil, nil, "", contractError(ErrorInvalidEvals, fmt.Errorf("evals.json missing"))
	}
	return snapshotFiles(evalTree, nil), append([]byte(nil), manifest.data...), "", nil
}

func digestExperimentSource(skillFiles, evalFiles, previousFiles []SnapshotFile) string {
	type namespacedFile struct {
		namespace string
		file      SnapshotFile
	}
	var files []namespacedFile
	for _, file := range skillFiles {
		files = append(files, namespacedFile{namespace: "current", file: file})
	}
	for _, file := range evalFiles {
		files = append(files, namespacedFile{namespace: "eval", file: file})
	}
	for _, file := range previousFiles {
		files = append(files, namespacedFile{namespace: "previous", file: file})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].namespace != files[j].namespace {
			return files[i].namespace < files[j].namespace
		}
		return files[i].file.Path < files[j].file.Path
	})
	builder := newDigestBuilder("experiment-source")
	for _, item := range files {
		builder.addString(item.namespace)
		builder.addString(item.file.Path)
		builder.add(item.file.Data)
	}
	return builder.sum()
}

func digestNormalizedExperiment(experiment Experiment) string {
	builder := newDigestBuilder("experiment-normalized")
	builder.addString(string(experiment.Format))
	builder.addString(string(experiment.Baseline))
	builder.addString(experiment.Skill.Name)
	builder.addString(experiment.Skill.ContentSHA256)
	if experiment.PreviousSkill != nil {
		builder.addString(experiment.PreviousSkill.ContentSHA256)
	} else {
		builder.add(nil)
	}
	for _, testCase := range experiment.Cases {
		builder.addString(fmt.Sprintf("%d", testCase.ID))
		builder.addString(testCase.Prompt)
		builder.addString(testCase.ExpectedOutput)
		builder.addString(strconv.FormatBool(testCase.FilesPresent))
		builder.addString(strconv.FormatBool(testCase.CriteriaPresent))
		for _, input := range testCase.Inputs {
			builder.addString(input.Path)
			builder.addString(input.SHA256)
		}
		for _, criterion := range testCase.Criteria {
			builder.addString(string(criterion.Kind))
			builder.addString(criterion.SourceField)
			builder.addString(criterion.Text)
		}
	}
	return builder.sum()
}
