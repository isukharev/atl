package agentskills

import (
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/agenteval/core"
)

// Project converts an imported experiment into one current and one explicit
// baseline plan per case. It declares conditions but does not admit or execute
// them; the caller-selected profile retains those authorities.
func (result ImportResult) Project(options ProjectOptions) ([]PlanProjection, error) {
	return result.Experiment.Project(options)
}

// Project converts an experiment into neutral core plans.
func (experiment Experiment) Project(options ProjectOptions) ([]PlanProjection, error) {
	if !validCoreIdentifier(string(options.Profile)) || options.Attempts == 0 || options.Attempts > MaxAttempts {
		return nil, contractError(ErrorInvalidProjection, nil)
	}
	if err := validateExperimentForProjection(experiment); err != nil {
		return nil, contractError(ErrorInvalidProjection, err)
	}
	projections := make([]PlanProjection, 0, len(experiment.Cases)*2)
	for _, testCase := range experiment.Cases {
		task, fixture := projectCase(testCase)
		for _, treatment := range projectTreatments(experiment) {
			planBuilder := newDigestBuilder("plan")
			planBuilder.addString(string(options.Profile))
			planBuilder.addString(string(task.ID))
			planBuilder.addString(string(fixture.ID))
			planBuilder.addString(string(treatment.treatment.ID))
			planBuilder.addString(fmt.Sprintf("%d", options.Attempts))
			projections = append(projections, PlanProjection{
				CaseID: testCase.ID, Treatment: treatment.kind,
				Plan: core.Plan{
					ID: "plan-" + core.PlanID(planBuilder.sum()), Profile: options.Profile,
					Task: task, Fixture: fixture, Treatment: treatment.treatment,
					Attempts: options.Attempts,
				},
			})
		}
	}
	return projections, nil
}

type projectedTreatment struct {
	kind      TreatmentKind
	treatment core.Treatment
}

func projectTreatments(experiment Experiment) []projectedTreatment {
	current := projectedTreatment{
		kind: TreatmentCurrentSkill,
		treatment: core.Treatment{
			ID:     core.TreatmentID("treatment-current-" + experiment.Skill.ContentSHA256),
			Skills: []core.Skill{{ID: core.SkillID("skill-" + experiment.Skill.ContentSHA256)}},
		},
	}
	baseline := projectedTreatment{kind: TreatmentNoSkill, treatment: core.Treatment{ID: "treatment-none"}}
	if experiment.Baseline == BaselinePreviousSkill {
		baseline = projectedTreatment{
			kind: TreatmentPreviousSkill,
			treatment: core.Treatment{
				ID:     core.TreatmentID("treatment-previous-" + experiment.PreviousSkill.ContentSHA256),
				Skills: []core.Skill{{ID: core.SkillID("skill-" + experiment.PreviousSkill.ContentSHA256)}},
			},
		}
	}
	return []projectedTreatment{current, baseline}
}

func projectCase(testCase Case) (core.Task, core.Fixture) {
	fixtureBuilder := newDigestBuilder("fixture")
	fixtureBuilder.addString(fmt.Sprintf("%d", testCase.ID))
	fixtureBuilder.addString(testCase.Prompt)
	fixtureBuilder.addString(testCase.ExpectedOutput)
	for _, input := range testCase.Inputs {
		fixtureBuilder.addString(input.Path)
		fixtureBuilder.addString(input.SHA256)
	}
	fixture := core.Fixture{ID: core.FixtureID("fixture-" + fixtureBuilder.sum())}

	checks := make([]core.Check, 0, len(testCase.Criteria)+1)
	checks = append(checks, core.Check{ID: "expected-output", Weight: 1})
	taskBuilder := newDigestBuilder("task")
	taskBuilder.addString(string(fixture.ID))
	for _, criterion := range testCase.Criteria {
		identifier := core.CheckID(fmt.Sprintf("criterion-%03d", criterion.Ordinal))
		checks = append(checks, core.Check{ID: identifier, Weight: 1})
		taskBuilder.addString(string(criterion.Kind))
		taskBuilder.addString(criterion.SourceField)
		taskBuilder.addString(criterion.Text)
	}
	return core.Task{ID: core.TaskID("task-" + taskBuilder.sum()), Checks: checks}, fixture
}

func validateExperimentForProjection(experiment Experiment) error {
	if (experiment.Format != FormatAgentSkillsGuideV1 && experiment.Format != FormatAnthropicSkillCreatorV1) ||
		(experiment.Baseline != BaselineNoSkill && experiment.Baseline != BaselinePreviousSkill) ||
		!validSkillName(experiment.Skill.Name) || len(experiment.Cases) == 0 || len(experiment.Cases) > MaxCases ||
		!validDigest(experiment.ContentSHA256) || !validDigest(experiment.NormalizedSHA256) {
		return fmt.Errorf("experiment contract")
	}
	if err := validateSnapshot(experiment.Skill); err != nil {
		return err
	}
	if experiment.Baseline == BaselinePreviousSkill {
		if experiment.PreviousSkill == nil || experiment.PreviousSkill.Name != experiment.Skill.Name || validateSnapshot(*experiment.PreviousSkill) != nil {
			return fmt.Errorf("previous snapshot")
		}
	} else if experiment.PreviousSkill != nil {
		return fmt.Errorf("unexpected previous snapshot")
	}
	seenCases := make(map[uint32]struct{}, len(experiment.Cases))
	var expandedInputBytes uint64
	for _, testCase := range experiment.Cases {
		if _, duplicate := seenCases[testCase.ID]; duplicate || testCase.Prompt == "" || testCase.ExpectedOutput == "" ||
			len(testCase.Prompt) > MaxTextBytes || len(testCase.ExpectedOutput) > MaxTextBytes ||
			!utf8.ValidString(testCase.Prompt) || !utf8.ValidString(testCase.ExpectedOutput) ||
			len(testCase.Inputs) > MaxFilesPerCase || len(testCase.Criteria) > MaxCriteriaPerCase ||
			(!testCase.FilesPresent && len(testCase.Inputs) != 0) ||
			(!testCase.CriteriaPresent && len(testCase.Criteria) != 0) {
			return fmt.Errorf("case contract")
		}
		seenCases[testCase.ID] = struct{}{}
		seenInput := make(map[string]struct{}, len(testCase.Inputs))
		for _, input := range testCase.Inputs {
			if !validSourcePath(input.Path) || input.SizeBytes != uint64(len(input.Data)) {
				return fmt.Errorf("input contract")
			}
			if expandedInputBytes > MaxTreeBytes || input.SizeBytes > MaxTreeBytes-expandedInputBytes {
				return fmt.Errorf("expanded input byte bound")
			}
			expandedInputBytes += input.SizeBytes
			if digestBytes(input.Data) != input.SHA256 {
				return fmt.Errorf("input contract")
			}
			if _, duplicate := seenInput[input.Path]; duplicate {
				return fmt.Errorf("input duplicate")
			}
			seenInput[input.Path] = struct{}{}
		}
		for index, criterion := range testCase.Criteria {
			expectedKind, expectedField := CriterionAssertion, "assertions"
			if experiment.Format == FormatAnthropicSkillCreatorV1 {
				expectedKind, expectedField = CriterionExpectation, "expectations"
			}
			if criterion.Ordinal != uint32(index+1) || criterion.Text == "" || len(criterion.Text) > MaxTextBytes ||
				!utf8.ValidString(criterion.Text) || criterion.Kind != expectedKind || criterion.SourceField != expectedField {
				return fmt.Errorf("criterion contract")
			}
		}
	}
	if digestNormalizedExperiment(experiment) != experiment.NormalizedSHA256 {
		return fmt.Errorf("experiment digest")
	}
	return nil
}

func validateSnapshot(snapshot SkillSnapshot) error {
	if !validSkillName(snapshot.Name) || !validDigest(snapshot.ContentSHA256) ||
		len(snapshot.Files) == 0 || len(snapshot.Files) > MaxTreeEntries {
		return fmt.Errorf("snapshot contract")
	}
	files := append([]SnapshotFile(nil), snapshot.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var total uint64
	for index, file := range files {
		if !validSourcePath(file.Path) || file.SizeBytes != uint64(len(file.Data)) || len(file.Data) > MaxFileBytes ||
			digestBytes(file.Data) != file.SHA256 || (index > 0 && files[index-1].Path == file.Path) {
			return fmt.Errorf("snapshot file contract")
		}
		total += file.SizeBytes
		if total > MaxTreeBytes {
			return fmt.Errorf("snapshot byte bound")
		}
	}
	if digestSnapshotFiles("skill-snapshot", files) != snapshot.ContentSHA256 {
		return fmt.Errorf("snapshot digest")
	}
	return nil
}

func validCoreIdentifier(value string) bool {
	if value == "" || len(value) > 128 || !coreIdentifierFirst(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !coreIdentifierFirst(character) && character != '.' && character != '_' && character != '-' && character != '/' {
			return false
		}
	}
	return true
}

func coreIdentifierFirst(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}
