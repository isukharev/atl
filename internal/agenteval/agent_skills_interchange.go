package agenteval

import (
	"fmt"
	"sort"

	"github.com/isukharev/atl/internal/agenteval/interchange/agentskills"
)

const (
	AgentSkillsImportReportSchema  = "agent-eval/agent-skills-import-report"
	AgentSkillsImportReportVersion = 1
	AgentSkillsExportReportSchema  = "agent-eval/agent-skills-export-report"
	AgentSkillsExportReportVersion = 1
)

// AgentSkillsImportOptions selects exact local roots and one explicit
// compatibility interpretation. Empty EvalRoot uses SkillRoot/evals.
type AgentSkillsImportOptions struct {
	SkillRoot         string
	EvalRoot          string
	PreviousSkillRoot string
	Format            string
	Baseline          string
}

// AgentSkillsCaseDirectory binds one public Guide workspace directory to an
// imported case. The adapter validates the exact relative layout; callers do
// not infer a directory from prompt or skill content.
type AgentSkillsCaseDirectory struct {
	CaseID uint32
	Path   string
}

// AgentSkillsExportOptions selects one exact source, workspace, new local
// destination, and explicit Guide directory mapping. Export never executes a
// runner or grader and never overwrites an existing destination.
type AgentSkillsExportOptions struct {
	Import          AgentSkillsImportOptions
	WorkspaceRoot   string
	Destination     string
	CaseDirectories []AgentSkillsCaseDirectory
}

// AgentSkillsCompatibilityEntry is a content-free loss or unsupported-field
// projection. Scope is schema vocabulary, never a filesystem path.
type AgentSkillsCompatibilityEntry struct {
	Code            string `json:"code"`
	Scope           string `json:"scope"`
	Disposition     string `json:"disposition"`
	Count           uint32 `json:"count"`
	BlocksExecution bool   `json:"blocks_execution"`
}

// AgentSkillsImportReport is the content-minimized result of one deterministic
// source capture. Digests are local comparison identities, not anonymization
// or publication approval.
type AgentSkillsImportReport struct {
	Schema                string                          `json:"schema"`
	SchemaVersion         int                             `json:"schema_version"`
	ContractVersion       string                          `json:"contract_version"`
	Format                string                          `json:"format"`
	Baseline              string                          `json:"baseline"`
	ContentSHA256         string                          `json:"content_sha256"`
	NormalizedSHA256      string                          `json:"normalized_sha256"`
	CurrentSkillSHA256    string                          `json:"current_skill_sha256"`
	PreviousSkillSHA256   string                          `json:"previous_skill_sha256,omitempty"`
	CaseCount             uint32                          `json:"case_count"`
	InputCount            uint32                          `json:"input_count"`
	CriterionCount        uint32                          `json:"criterion_count"`
	CurrentSkillFileCount uint32                          `json:"current_skill_file_count"`
	PreviousFileCount     uint32                          `json:"previous_skill_file_count"`
	ExecutionEligible     bool                            `json:"execution_eligible"`
	Compatibility         []AgentSkillsCompatibilityEntry `json:"compatibility"`
}

// AgentSkillsExportReport is a content-minimized receipt for a
// non-authoritative compatibility export. Its digests are local comparison
// identities, not anonymization, publisher authentication, or evaluator
// lifecycle evidence.
type AgentSkillsExportReport struct {
	Schema                 string                          `json:"schema"`
	SchemaVersion          int                             `json:"schema_version"`
	ContractVersion        string                          `json:"contract_version"`
	Format                 string                          `json:"format"`
	Baseline               string                          `json:"baseline"`
	SourceContentSHA256    string                          `json:"source_content_sha256"`
	NormalizedSHA256       string                          `json:"normalized_sha256"`
	WorkspaceContentSHA256 string                          `json:"workspace_content_sha256"`
	PublicationSHA256      string                          `json:"publication_sha256"`
	PreviousSkillSHA256    string                          `json:"previous_skill_sha256,omitempty"`
	CaseCount              uint32                          `json:"case_count"`
	RunCount               uint32                          `json:"run_count"`
	FileCount              uint32                          `json:"file_count"`
	Authoritative          bool                            `json:"authoritative"`
	Compatibility          []AgentSkillsCompatibilityEntry `json:"compatibility"`
}

// InspectAgentSkillsImport performs a deterministic, provider-free capture and
// returns only the bounded compatibility projection. It does not execute or
// write the imported project.
func InspectAgentSkillsImport(options AgentSkillsImportOptions) (AgentSkillsImportReport, error) {
	result, err := agentskills.Import(agentskills.ImportRequest{
		SkillRoot: options.SkillRoot, EvalRoot: options.EvalRoot,
		PreviousSkillRoot: options.PreviousSkillRoot,
		Format:            agentskills.Format(options.Format), Baseline: agentskills.Baseline(options.Baseline),
	})
	if err != nil {
		return AgentSkillsImportReport{}, err
	}
	report := AgentSkillsImportReport{
		Schema: AgentSkillsImportReportSchema, SchemaVersion: AgentSkillsImportReportVersion,
		ContractVersion: StandaloneContractVersion, Format: string(result.Experiment.Format),
		Baseline: string(result.Experiment.Baseline), ContentSHA256: result.Experiment.ContentSHA256,
		NormalizedSHA256:      result.Experiment.NormalizedSHA256,
		CurrentSkillSHA256:    result.Experiment.Skill.ContentSHA256,
		CaseCount:             boundedAgentSkillsCount(len(result.Experiment.Cases)),
		CurrentSkillFileCount: boundedAgentSkillsCount(len(result.Experiment.Skill.Files)),
		ExecutionEligible:     !result.Report.BlocksExecution(),
		Compatibility:         make([]AgentSkillsCompatibilityEntry, 0, len(result.Report.Entries)),
	}
	if result.Experiment.PreviousSkill != nil {
		report.PreviousSkillSHA256 = result.Experiment.PreviousSkill.ContentSHA256
		report.PreviousFileCount = boundedAgentSkillsCount(len(result.Experiment.PreviousSkill.Files))
	}
	for _, testCase := range result.Experiment.Cases {
		report.InputCount += boundedAgentSkillsCount(len(testCase.Inputs))
		report.CriterionCount += boundedAgentSkillsCount(len(testCase.Criteria))
	}
	for _, entry := range result.Report.Entries {
		report.Compatibility = append(report.Compatibility, AgentSkillsCompatibilityEntry{
			Code: string(entry.Code), Scope: entry.Scope, Disposition: string(entry.Disposition),
			Count: entry.Count, BlocksExecution: entry.BlocksExecution,
		})
	}
	return report, nil
}

// ExportAgentSkillsWorkspace strictly captures an imported source and an
// existing iteration workspace, projects their non-authoritative compatibility
// view, and writes it to one exact new local destination. The returned report
// contains no path, prompt, output, evidence, feedback, note, or model value.
func ExportAgentSkillsWorkspace(options AgentSkillsExportOptions) (AgentSkillsExportReport, error) {
	imported, err := agentskills.Import(agentSkillsImportRequest(options.Import))
	if err != nil {
		return AgentSkillsExportReport{}, err
	}
	caseDirectories := make([]agentskills.CaseDirectory, 0, len(options.CaseDirectories))
	for _, directory := range options.CaseDirectories {
		caseDirectories = append(caseDirectories, agentskills.CaseDirectory{CaseID: directory.CaseID, Path: directory.Path})
	}
	workspaceResult, err := agentskills.ImportWorkspace(agentskills.WorkspaceImportRequest{
		Root: options.WorkspaceRoot, Format: imported.Experiment.Format,
		Experiment: imported.Experiment, CaseDirectories: caseDirectories,
	})
	if err != nil {
		return AgentSkillsExportReport{}, err
	}
	benchmark := agentSkillsBenchmarkView(imported.Experiment, workspaceResult.Workspace)
	plan, err := agentskills.PlanWorkspacePublication(agentskills.WorkspacePublicationRequest{
		Format: imported.Experiment.Format, Experiment: imported.Experiment,
		CaseDirectories: caseDirectories, Benchmark: benchmark, Source: &workspaceResult.Workspace,
	})
	if err != nil {
		return AgentSkillsExportReport{}, err
	}
	if err := plan.WriteNew(options.Destination); err != nil {
		return AgentSkillsExportReport{}, err
	}
	report := AgentSkillsExportReport{
		Schema: AgentSkillsExportReportSchema, SchemaVersion: AgentSkillsExportReportVersion,
		ContractVersion: StandaloneContractVersion, Format: string(imported.Experiment.Format),
		Baseline: string(imported.Experiment.Baseline), SourceContentSHA256: imported.Experiment.ContentSHA256,
		NormalizedSHA256:       imported.Experiment.NormalizedSHA256,
		WorkspaceContentSHA256: workspaceResult.Workspace.ContentSHA256,
		PublicationSHA256:      plan.ContentSHA256, CaseCount: boundedAgentSkillsCount(len(imported.Experiment.Cases)),
		RunCount:  boundedAgentSkillsCount(len(workspaceResult.Workspace.Runs)),
		FileCount: boundedAgentSkillsCount(len(plan.Files)), Authoritative: false,
		Compatibility: mergeAgentSkillsReports(imported.Report, workspaceResult.Report, plan.Report),
	}
	if imported.Experiment.PreviousSkill != nil {
		report.PreviousSkillSHA256 = imported.Experiment.PreviousSkill.ContentSHA256
	}
	return report, nil
}

// AgentSkillsErrorCode returns the closed compatibility-adapter error code
// without exposing its content-bearing cause.
func AgentSkillsErrorCode(err error) (string, bool) {
	code, ok := agentskills.CodeOf(err)
	return string(code), ok
}

func agentSkillsImportRequest(options AgentSkillsImportOptions) agentskills.ImportRequest {
	return agentskills.ImportRequest{
		SkillRoot: options.SkillRoot, EvalRoot: options.EvalRoot,
		PreviousSkillRoot: options.PreviousSkillRoot,
		Format:            agentskills.Format(options.Format), Baseline: agentskills.Baseline(options.Baseline),
	}
}

func agentSkillsBenchmarkView(experiment agentskills.Experiment, workspace agentskills.Workspace) agentskills.BenchmarkView {
	view := agentskills.BenchmarkView{
		SkillName: experiment.Skill.Name, FeedbackPresent: workspace.FeedbackPresent,
		Feedback:     append([]agentskills.FeedbackEntry(nil), workspace.Feedback...),
		NotesPresent: workspace.NotesPresent, Notes: append([]string(nil), workspace.Notes...),
		Runs: make([]agentskills.BenchmarkRun, 0, len(workspace.Runs)),
	}
	if workspace.Metadata.ExecutorModelPresent {
		view.ExecutorModel = workspace.Metadata.ExecutorModel
	}
	if workspace.Metadata.AnalyzerModelPresent {
		view.AnalyzerModel = workspace.Metadata.AnalyzerModel
	}
	if workspace.Metadata.TimestampPresent {
		view.Timestamp = workspace.Metadata.Timestamp
	}
	cases := make(map[uint32]agentskills.Case, len(experiment.Cases))
	for _, testCase := range experiment.Cases {
		cases[testCase.ID] = testCase
	}
	complete := make(map[string]bool, len(workspace.Runs))
	for _, run := range workspace.Runs {
		complete[agentSkillsRunKey(run.CaseID, run.Configuration, run.RunNumber)] = agentSkillsGradingCoversCase(cases[run.CaseID], run)
	}
	baseline := agentskills.TreatmentNoSkill
	if experiment.Baseline == agentskills.BaselinePreviousSkill {
		baseline = agentskills.TreatmentPreviousSkill
	}
	for _, run := range workspace.Runs {
		if !complete[agentSkillsRunKey(run.CaseID, run.Configuration, run.RunNumber)] {
			continue
		}
		pairedTreatment := baseline
		if run.Configuration == baseline {
			pairedTreatment = agentskills.TreatmentCurrentSkill
		}
		if run.Configuration != agentskills.TreatmentCurrentSkill && run.Configuration != baseline ||
			!complete[agentSkillsRunKey(run.CaseID, pairedTreatment, run.RunNumber)] {
			continue
		}
		grading := run.Grading
		grading.Results = append([]agentskills.GradeResult(nil), run.Grading.Results...)
		grading.Feedback = append([]agentskills.FeedbackEntry(nil), run.Grading.Feedback...)
		view.Runs = append(view.Runs, agentskills.BenchmarkRun{
			CaseID: run.CaseID, EvalName: run.EvalName, Configuration: run.Configuration, RunNumber: run.RunNumber,
			Grading: grading, NotesPresent: run.NotesPresent, Notes: append([]string(nil), run.Notes...),
		})
	}
	return view
}

func agentSkillsRunKey(caseID uint32, treatment agentskills.TreatmentKind, runNumber uint32) string {
	return fmt.Sprintf("%d/%s/%d", caseID, treatment, runNumber)
}

func agentSkillsGradingCoversCase(testCase agentskills.Case, run agentskills.WorkspaceRun) bool {
	if !testCase.CriteriaPresent || len(testCase.Criteria) == 0 || !run.GradingPresent ||
		len(testCase.Criteria) != len(run.Grading.Results) {
		return false
	}
	for index, criterion := range testCase.Criteria {
		if criterion.Text != run.Grading.Results[index].Text {
			return false
		}
	}
	return true
}

func mergeAgentSkillsReports(reports ...agentskills.Report) []AgentSkillsCompatibilityEntry {
	type key struct {
		code, scope, disposition string
		blocking                 bool
	}
	counts := make(map[key]uint32)
	for _, report := range reports {
		for _, entry := range report.Entries {
			currentKey := key{
				code: string(entry.Code), scope: entry.Scope, disposition: string(entry.Disposition),
				blocking: entry.BlocksExecution,
			}
			current := counts[currentKey]
			if ^uint32(0)-current < entry.Count {
				counts[currentKey] = ^uint32(0)
			} else {
				counts[currentKey] = current + entry.Count
			}
		}
	}
	result := make([]AgentSkillsCompatibilityEntry, 0, len(counts))
	for currentKey, count := range counts {
		result = append(result, AgentSkillsCompatibilityEntry{
			Code: currentKey.code, Scope: currentKey.scope, Disposition: currentKey.disposition,
			Count: count, BlocksExecution: currentKey.blocking,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Code != result[j].Code {
			return result[i].Code < result[j].Code
		}
		if result[i].Scope != result[j].Scope {
			return result[i].Scope < result[j].Scope
		}
		if result[i].Disposition != result[j].Disposition {
			return result[i].Disposition < result[j].Disposition
		}
		return !result[i].BlocksExecution && result[j].BlocksExecution
	})
	return result
}

func boundedAgentSkillsCount(count int) uint32 {
	var value uint32
	for range count {
		value++
	}
	return value
}
