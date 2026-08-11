package agentskills

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/core"
)

func TestImportGuideV1AndProjectNoSkill(t *testing.T) {
	root := fixturePath("guide-v1", "skill")
	marker := filepath.Join(root, "import-executed.txt")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected hostile-script marker before import: %v", err)
	}
	request := ImportRequest{
		SkillRoot: root,
		Format:    FormatAuto,
		Baseline:  BaselineNoSkill,
	}
	first, err := Import(request)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	second, err := Import(request)
	if err != nil {
		t.Fatalf("second Import() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated import did not produce the same captured experiment")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import executed a bundled script: %v", err)
	}

	experiment := first.Experiment
	if experiment.Format != FormatAgentSkillsGuideV1 || experiment.Baseline != BaselineNoSkill {
		t.Fatalf("format/baseline = %q/%q", experiment.Format, experiment.Baseline)
	}
	if !validDigest(experiment.ContentSHA256) || !validDigest(experiment.NormalizedSHA256) {
		t.Fatalf("invalid experiment digests: %#v", experiment)
	}
	if experiment.Skill.Name != "csv-helper" || experiment.PreviousSkill != nil {
		t.Fatalf("skill projection = %#v", experiment.Skill)
	}
	if got, want := snapshotPaths(experiment.Skill.Files), []string{"SKILL.md", "scripts/hostile.sh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skill snapshot paths = %#v, want %#v", got, want)
	}
	if len(experiment.Cases) != 2 {
		t.Fatalf("case count = %d", len(experiment.Cases))
	}
	firstCase := experiment.Cases[0]
	if !firstCase.FilesPresent || !firstCase.CriteriaPresent || len(firstCase.Inputs) != 1 || len(firstCase.Criteria) != 2 {
		t.Fatalf("first case shape = %#v", firstCase)
	}
	if firstCase.Inputs[0].Path != "evals/files/rows.csv" || !bytes.Contains(firstCase.Inputs[0].Data, []byte("alpha")) {
		t.Fatalf("captured input = %#v", firstCase.Inputs[0])
	}
	if got := firstCase.Criteria[0]; got.Kind != CriterionAssertion || got.SourceField != "assertions" ||
		got.Ordinal != 1 || got.Text != "The summary reports three rows." {
		t.Fatalf("first assertion = %#v", got)
	}
	secondCase := experiment.Cases[1]
	if !secondCase.FilesPresent || secondCase.CriteriaPresent || len(secondCase.Inputs) != 0 || len(secondCase.Criteria) != 0 {
		t.Fatalf("empty-vs-missing preservation = %#v", secondCase)
	}
	if !first.Report.BlocksExecution() || len(first.Report.Entries) != 7 {
		t.Fatalf("import report = %#v", first.Report)
	}
	for _, code := range []ReportCode{
		ReportRunnerUnbound, ReportJudgeUnbound, ReportSandboxUnbound, ReportEnvironmentUnbound,
		ReportActivationUnbound, ReportVerifierCoverageUnbound,
	} {
		entry, ok := findReportEntry(first.Report, code)
		if !ok || !entry.BlocksExecution || entry.Disposition != DispositionUnsupported || entry.Count != 1 {
			t.Fatalf("report entry %q = %#v, present %v", code, entry, ok)
		}
	}

	projections, err := first.Project(ProjectOptions{Profile: core.ProfileID("profile.synthetic"), Attempts: 2})
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if len(projections) != 4 {
		t.Fatalf("projection count = %d", len(projections))
	}
	for index := 0; index < len(projections); index += 2 {
		current, baseline := projections[index], projections[index+1]
		if current.CaseID != baseline.CaseID || current.Treatment != TreatmentCurrentSkill || baseline.Treatment != TreatmentNoSkill {
			t.Fatalf("projection pair = %#v / %#v", current, baseline)
		}
		if current.Plan.Task.ID != baseline.Plan.Task.ID || current.Plan.Fixture.ID != baseline.Plan.Fixture.ID ||
			current.Plan.Attempts != 2 || baseline.Plan.Attempts != 2 {
			t.Fatalf("unpaired neutral plans = %#v / %#v", current.Plan, baseline.Plan)
		}
		if len(current.Plan.Treatment.Skills) != 1 || len(baseline.Plan.Treatment.Skills) != 0 {
			t.Fatalf("skill treatments = %#v / %#v", current.Plan.Treatment, baseline.Plan.Treatment)
		}
	}
	if got, want := checkIDs(projections[0].Plan.Task.Checks), []core.CheckID{"expected-output", "criterion-001", "criterion-002"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("case 1 checks = %#v, want %#v", got, want)
	}
	if got, want := checkIDs(projections[2].Plan.Task.Checks), []core.CheckID{"expected-output"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("case 2 checks = %#v, want %#v", got, want)
	}
}

func TestImportAnthropicV1AndProjectPreviousSkill(t *testing.T) {
	result, err := Import(ImportRequest{
		SkillRoot:         fixturePath("anthropic-v1", "skill"),
		PreviousSkillRoot: fixturePath("anthropic-v1", "previous"),
		Format:            FormatAuto,
		Baseline:          BaselinePreviousSkill,
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Experiment.Format != FormatAnthropicSkillCreatorV1 || result.Experiment.PreviousSkill == nil {
		t.Fatalf("experiment = %#v", result.Experiment)
	}
	if got, want := snapshotPaths(result.Experiment.Skill.Files), []string{"SKILL.md", "references/format.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("current snapshot = %#v, want %#v", got, want)
	}
	if got, want := snapshotPaths(result.Experiment.PreviousSkill.Files), []string{"SKILL.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("previous snapshot = %#v, want %#v", got, want)
	}
	testCase := result.Experiment.Cases[0]
	if len(testCase.Criteria) != 2 || testCase.Criteria[1].Kind != CriterionExpectation ||
		testCase.Criteria[1].SourceField != "expectations" || testCase.Criteria[1].Text != "The body says the status is ready." {
		t.Fatalf("expectations = %#v", testCase.Criteria)
	}
	if len(result.Report.Entries) != 9 || !result.Report.BlocksExecution() {
		t.Fatalf("report = %#v", result.Report)
	}
	for _, code := range []ReportCode{ReportAllowedToolsUnbound, ReportCompatibilityUnbound} {
		entry, ok := findReportEntry(result.Report, code)
		if !ok || entry.Disposition != DispositionPreservedSourceOnly || !entry.BlocksExecution {
			t.Fatalf("metadata report %q = %#v, present %v", code, entry, ok)
		}
	}

	projections, err := result.Project(ProjectOptions{Profile: "profile.synthetic", Attempts: 1})
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if len(projections) != 2 || projections[0].Treatment != TreatmentCurrentSkill || projections[1].Treatment != TreatmentPreviousSkill {
		t.Fatalf("projections = %#v", projections)
	}
	if len(projections[0].Plan.Treatment.Skills) != 1 || len(projections[1].Plan.Treatment.Skills) != 1 ||
		projections[0].Plan.Treatment.Skills[0].ID == projections[1].Plan.Treatment.Skills[0].ID {
		t.Fatalf("current/previous treatments = %#v / %#v", projections[0].Plan.Treatment, projections[1].Plan.Treatment)
	}
}

func TestDecodeEvalsRejectsAmbiguousAndNonCanonicalDTOs(t *testing.T) {
	validWithoutCriteria := `{"skill_name":"valid-skill","evals":[{"id":1,"prompt":"p","expected_output":"e"}]}`
	if decoded, err := decodeEvals([]byte(validWithoutCriteria), FormatAgentSkillsGuideV1); err != nil || decoded.cases[0].criteriaPresent {
		t.Fatalf("explicit guide decode = %#v, %v", decoded, err)
	}
	if decoded, err := decodeEvals([]byte(validWithoutCriteria), FormatAnthropicSkillCreatorV1); err != nil || decoded.cases[0].criteriaPresent {
		t.Fatalf("explicit Anthropic decode = %#v, %v", decoded, err)
	}

	tests := []struct {
		name   string
		format Format
		json   string
	}{
		{name: "auto without discriminator", format: FormatAuto, json: validWithoutCriteria},
		{name: "both spellings", format: FormatAuto, json: `{"skill_name":"valid-skill","evals":[{"id":1,"prompt":"p","expected_output":"e","assertions":[],"expectations":[]}]}`},
		{name: "mixed spellings", format: FormatAuto, json: `{"skill_name":"valid-skill","evals":[{"id":1,"prompt":"p","expected_output":"e","assertions":[]},{"id":2,"prompt":"p","expected_output":"e","expectations":[]}]}`},
		{name: "variant mismatch", format: FormatAnthropicSkillCreatorV1, json: `{"skill_name":"valid-skill","evals":[{"id":1,"prompt":"p","expected_output":"e","assertions":[]}]}`},
		{name: "duplicate member", format: FormatAgentSkillsGuideV1, json: `{"skill_name":"valid-skill","skill_name":"other","evals":[]}`},
		{name: "unknown member", format: FormatAgentSkillsGuideV1, json: `{"skill_name":"valid-skill","evals":[{"id":1,"prompt":"p","expected_output":"e","extra":true}]}`},
		{name: "duplicate id", format: FormatAgentSkillsGuideV1, json: `{"skill_name":"valid-skill","evals":[{"id":1,"prompt":"p","expected_output":"e"},{"id":1,"prompt":"q","expected_output":"f"}]}`},
		{name: "parent traversal", format: FormatAgentSkillsGuideV1, json: `{"skill_name":"valid-skill","evals":[{"id":1,"prompt":"p","expected_output":"e","files":["../outside"]}]}`},
		{name: "windows path", format: FormatAgentSkillsGuideV1, json: `{"skill_name":"valid-skill","evals":[{"id":1,"prompt":"p","expected_output":"e","files":["C:\\\\outside"]}]}`},
		{name: "duplicate path", format: FormatAgentSkillsGuideV1, json: `{"skill_name":"valid-skill","evals":[{"id":1,"prompt":"p","expected_output":"e","files":["input.txt","input.txt"]}]}`},
		{name: "null files", format: FormatAgentSkillsGuideV1, json: `{"skill_name":"valid-skill","evals":[{"id":1,"prompt":"p","expected_output":"e","files":null}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeEvals([]byte(test.json), test.format)
			requireErrorCode(t, err, ErrorInvalidEvals)
			if strings.Contains(err.Error(), "outside") || strings.Contains(err.Error(), "valid-skill") {
				t.Fatalf("rendered error leaked source content: %q", err)
			}
		})
	}

	_, err := decodeEvals(bytes.Repeat([]byte{' '}, MaxJSONBytes+1), FormatAgentSkillsGuideV1)
	requireErrorCode(t, err, ErrorLimitExceeded)
}

func TestDecodeEvalsEnforcesCaseBound(t *testing.T) {
	cases := make([]map[string]any, MaxCases+1)
	for index := range cases {
		cases[index] = map[string]any{
			"id": index + 1, "prompt": "p", "expected_output": "e", "assertions": []string{},
		}
	}
	data, err := json.Marshal(map[string]any{"skill_name": "valid-skill", "evals": cases})
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	_, err = decodeEvals(data, FormatAgentSkillsGuideV1)
	requireErrorCode(t, err, ErrorLimitExceeded)
}

func TestImportBoundsExpandedRepeatedInputReferencesBeforeCloning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SKILL.md"), "---\nname: repeated-input\ndescription: Synthetic repeated input.\n---\n")
	input := bytes.Repeat([]byte{'x'}, 2<<20)
	if err := os.WriteFile(filepath.Join(root, "input.bin"), input, 0o600); err != nil {
		t.Fatalf("WriteFile(input.bin): %v", err)
	}
	cases := make([]map[string]any, 33)
	for index := range cases {
		cases[index] = map[string]any{
			"id": index + 1, "prompt": "p", "expected_output": "e",
			"files": []string{"input.bin"}, "assertions": []string{},
		}
	}
	evals, err := json.Marshal(map[string]any{"skill_name": "repeated-input", "evals": cases})
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	writeFile(t, filepath.Join(root, "evals", "evals.json"), string(evals))

	_, err = Import(ImportRequest{SkillRoot: root, Format: FormatAgentSkillsGuideV1, Baseline: BaselineNoSkill})
	requireErrorCode(t, err, ErrorLimitExceeded)
}

func TestImportExternalEvalRootAndRejectsOverlappingAncestor(t *testing.T) {
	container := t.TempDir()
	skillRoot := filepath.Join(container, "skill")
	evalRoot := filepath.Join(container, "external-evals")
	writeFile(t, filepath.Join(skillRoot, "SKILL.md"), "---\nname: local-skill\ndescription: Local skill.\n---\n")
	writeFile(t, filepath.Join(skillRoot, "input.txt"), "contained input\n")
	writeFile(t, filepath.Join(skillRoot, "evals", "skill-asset.txt"), "part of the skill\n")
	writeFile(t, filepath.Join(evalRoot, "evals.json"), `{"skill_name":"local-skill","evals":[{"id":1,"prompt":"p","expected_output":"e","files":["input.txt"],"assertions":[]}]}`)

	result, err := Import(ImportRequest{
		SkillRoot: skillRoot,
		EvalRoot:  evalRoot,
		Format:    FormatAgentSkillsGuideV1,
		Baseline:  BaselineNoSkill,
	})
	if err != nil {
		t.Fatalf("external eval import error = %v", err)
	}
	if got := string(result.Experiment.Cases[0].Inputs[0].Data); got != "contained input\n" {
		t.Fatalf("external eval input = %q", got)
	}
	if got, want := snapshotPaths(result.Experiment.Skill.Files), []string{"SKILL.md", "evals/skill-asset.txt", "input.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("external eval import excluded an unrelated skill subtree: %#v, want %#v", got, want)
	}

	_, err = Import(ImportRequest{
		SkillRoot: skillRoot,
		EvalRoot:  container,
		Format:    FormatAgentSkillsGuideV1,
		Baseline:  BaselineNoSkill,
	})
	requireErrorCode(t, err, ErrorInvalidRequest)
}

func TestProjectRejectsSemanticMutation(t *testing.T) {
	result, err := Import(ImportRequest{
		SkillRoot: fixturePath("guide-v1", "skill"),
		Format:    FormatAgentSkillsGuideV1,
		Baseline:  BaselineNoSkill,
	})
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	absent := result.Experiment
	absent.Cases = append([]Case(nil), absent.Cases...)
	presentEmpty := absent
	presentEmpty.Cases = append([]Case(nil), absent.Cases...)
	presentEmpty.Cases[1].CriteriaPresent = true
	if digestNormalizedExperiment(absent) == digestNormalizedExperiment(presentEmpty) {
		t.Fatal("normalized digest collapsed missing and explicitly empty criteria")
	}

	mutated := result.Experiment
	mutated.Cases = append([]Case(nil), mutated.Cases...)
	mutated.Cases[0].Criteria = append([]Criterion(nil), mutated.Cases[0].Criteria...)
	mutated.Cases[0].Criteria[0].Kind = CriterionExpectation
	mutated.Cases[0].Criteria[0].SourceField = "expectations"
	mutated.NormalizedSHA256 = digestNormalizedExperiment(mutated)
	_, err = mutated.Project(ProjectOptions{Profile: "profile.synthetic", Attempts: 1})
	requireErrorCode(t, err, ErrorInvalidProjection)
}

func fixturePath(parts ...string) string {
	all := append([]string{"testdata"}, parts...)
	return filepath.Join(all...)
}

func writeFile(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", name, err)
	}
	if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", name, err)
	}
}

func requireErrorCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", want)
	}
	got, ok := CodeOf(err)
	if !ok || got != want {
		t.Fatalf("error = %v, code = %q/%v, want %q", err, got, ok, want)
	}
}

func snapshotPaths(files []SnapshotFile) []string {
	result := make([]string, len(files))
	for index, file := range files {
		result[index] = file.Path
	}
	return result
}

func checkIDs(checks []core.Check) []core.CheckID {
	result := make([]core.CheckID, len(checks))
	for index, check := range checks {
		result[index] = check.ID
	}
	return result
}

func findReportEntry(report Report, code ReportCode) (ReportEntry, bool) {
	for _, entry := range report.Entries {
		if entry.Code == code {
			return entry, true
		}
	}
	return ReportEntry{}, false
}

func findReportEntryScope(report Report, code ReportCode, scope string) (ReportEntry, bool) {
	for _, entry := range report.Entries {
		if entry.Code == code && entry.Scope == scope {
			return entry, true
		}
	}
	return ReportEntry{}, false
}
