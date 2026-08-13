package agenteval

import (
	"errors"
	"io"

	"github.com/isukharev/atl/internal/agenteval/experiment"
	"github.com/isukharev/atl/internal/agenteval/interchange/agentskills"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

const (
	ExperimentCapabilitySchema = experiment.CapabilitySchema
	ExperimentDesignSchema     = experiment.DesignSchema
	ExperimentAnalysisSchema   = experiment.AnalysisSchema
	ExperimentManifestSchema   = experiment.ManifestSchema
	ExperimentTrialSchema      = experiment.TrialSchema
	ExperimentSchemaVersion    = experiment.SchemaVersion

	ExperimentCapabilityMaxBytes = experiment.MaxCapabilityBytes
	ExperimentDesignMaxBytes     = experiment.MaxDesignBytes
	ExperimentAnalysisMaxBytes   = experiment.MaxAnalysisBytes
	ExperimentManifestMaxBytes   = experiment.MaxManifestBytes
	ExperimentTrialMaxBytes      = experiment.MaxTrialBytes
)

var ErrExperimentProjection = errors.New("experiment_projection_invalid")

type ExperimentErrorCode = experiment.ErrorCode
type ExperimentCapabilityContract = experiment.CapabilityContract
type ExperimentDesign = experiment.Design
type ExperimentAnalysisPlan = experiment.AnalysisPlan
type ExperimentManifest = experiment.Manifest
type ExperimentTrialRecord = experiment.TrialRecord
type ExperimentCaseBinding = experiment.CaseBinding
type ExperimentTreatmentRequest = experiment.TreatmentRequest
type ExperimentStratumRequest = experiment.StratumRequest
type ExperimentOrderingPolicy = experiment.OrderingPolicy
type ExperimentStoppingRule = experiment.StoppingRule
type ExperimentArmSelector = experiment.ArmSelector

func SealExperimentCapabilityContract(contract ExperimentCapabilityContract) (ExperimentCapabilityContract, error) {
	return experiment.SealCapabilityContract(contract)
}

func SealExperimentDesign(design ExperimentDesign) (ExperimentDesign, error) {
	return experiment.SealDesign(design)
}

func SealExperimentAnalysisPlan(plan ExperimentAnalysisPlan) (ExperimentAnalysisPlan, error) {
	return experiment.SealAnalysisPlan(plan)
}

func CompileExperiment(design ExperimentDesign, capability ExperimentCapabilityContract, analysis ExperimentAnalysisPlan) (ExperimentManifest, error) {
	return experiment.Compile(design, capability, analysis)
}

func ExperimentErrorCodeOf(err error) (ExperimentErrorCode, bool) { return experiment.CodeOf(err) }

func DecodeExperimentCapabilityContract(reader io.Reader) (ExperimentCapabilityContract, error) {
	return experiment.DecodeCapabilityContract(reader)
}

func DecodeExperimentDesign(reader io.Reader) (ExperimentDesign, error) {
	return experiment.DecodeDesign(reader)
}

func DecodeExperimentAnalysisPlan(reader io.Reader) (ExperimentAnalysisPlan, error) {
	return experiment.DecodeAnalysisPlan(reader)
}

func DecodeExperimentManifest(reader io.Reader) (ExperimentManifest, error) {
	return experiment.DecodeManifest(reader)
}

func DecodeExperimentTrialRecord(reader io.Reader, manifest ExperimentManifest) (ExperimentTrialRecord, error) {
	return experiment.DecodeTrialRecord(reader, manifest)
}

func EncodeExperimentCapabilityContract(contract ExperimentCapabilityContract) ([]byte, error) {
	return experiment.EncodeCapabilityContract(contract)
}

func EncodeExperimentDesign(design ExperimentDesign) ([]byte, error) {
	return experiment.EncodeDesign(design)
}

func EncodeExperimentAnalysisPlan(plan ExperimentAnalysisPlan) ([]byte, error) {
	return experiment.EncodeAnalysisPlan(plan)
}

func EncodeExperimentManifest(manifest ExperimentManifest) ([]byte, error) {
	return experiment.EncodeManifest(manifest)
}

func EncodeExperimentTrialRecord(manifest ExperimentManifest, record ExperimentTrialRecord) ([]byte, error) {
	return experiment.EncodeTrialRecord(manifest, record)
}

// AgentSkillsExperimentCaseProjection is an in-memory, content-addressed
// bridge. It contains no prompt, expected output, criterion text, input bytes,
// path, feedback, or workspace artifact.
type AgentSkillsExperimentCaseProjection struct {
	CaseID              uint32
	Case                ExperimentCaseBinding
	CurrentSkillSHA256  string
	PreviousSkillSHA256 string
}

// AgentSkillsExperimentProjection returns exact neutral case identities plus
// the existing content-minimized compatibility report. It does not synthesize
// negative controls, choose an analysis, execute a runner, or write a file.
type AgentSkillsExperimentProjection struct {
	ImportReport AgentSkillsImportReport
	Cases        []AgentSkillsExperimentCaseProjection
}

func ProjectAgentSkillsExperiment(options AgentSkillsImportOptions) (AgentSkillsExperimentProjection, error) {
	imported, err := agentskills.Import(agentSkillsImportRequest(options))
	if err != nil {
		return AgentSkillsExperimentProjection{}, err
	}
	projection := AgentSkillsExperimentProjection{
		ImportReport: agentSkillsImportReport(imported),
		Cases:        make([]AgentSkillsExperimentCaseProjection, 0, len(imported.Experiment.Cases)),
	}
	previous := ""
	if imported.Experiment.PreviousSkill != nil {
		previous = imported.Experiment.PreviousSkill.ContentSHA256
	}
	for _, importedCase := range imported.Experiment.Cases {
		caseSHA256, digestErr := contentMinimizedAttemptDigest("agent-skills-experiment-case", importedCase)
		if digestErr != nil {
			return AgentSkillsExperimentProjection{}, ErrExperimentProjection
		}
		taskSHA256, digestErr := contentMinimizedAttemptDigest("agent-skills-experiment-task", struct {
			Prompt         string                  `json:"prompt"`
			ExpectedOutput string                  `json:"expected_output"`
			Criteria       []agentskills.Criterion `json:"criteria"`
		}{importedCase.Prompt, importedCase.ExpectedOutput, importedCase.Criteria})
		if digestErr != nil {
			return AgentSkillsExperimentProjection{}, ErrExperimentProjection
		}
		fixtureSHA256, digestErr := contentMinimizedAttemptDigest("agent-skills-experiment-fixture", importedCase.Inputs)
		if digestErr != nil {
			return AgentSkillsExperimentProjection{}, ErrExperimentProjection
		}
		gradingSHA256, digestErr := contentMinimizedAttemptDigest("agent-skills-experiment-grading", struct {
			ExpectedOutput string                  `json:"expected_output"`
			Criteria       []agentskills.Criterion `json:"criteria"`
		}{importedCase.ExpectedOutput, importedCase.Criteria})
		if digestErr != nil {
			return AgentSkillsExperimentProjection{}, ErrExperimentProjection
		}
		projection.Cases = append(projection.Cases, AgentSkillsExperimentCaseProjection{
			CaseID: importedCase.ID,
			Case: ExperimentCaseBinding{
				SourceKind: experiment.SourceAgentSkills, SourceSHA256: imported.Experiment.ContentSHA256,
				CaseSHA256: caseSHA256, TaskSHA256: taskSHA256, FixtureSHA256: fixtureSHA256,
				GradingPlanSHA256: gradingSHA256,
			},
			CurrentSkillSHA256: imported.Experiment.Skill.ContentSHA256, PreviousSkillSHA256: previous,
		})
	}
	return projection, nil
}

// ExperimentAttemptBindings projects the full immutable trial roster into the
// existing lifecycle contract. The manifest identity binds treatment order,
// while each attempt gets a distinct trial identity. No attempt is committed.
func ExperimentAttemptBindings(manifest ExperimentManifest) ([]lifecycle.Binding, error) {
	trialDigests, err := experiment.TrialExperimentSHA256s(manifest)
	if err != nil {
		return nil, err
	}
	treatments := make(map[string]experiment.Treatment, len(manifest.Treatments))
	for _, treatment := range manifest.Treatments {
		treatments[treatment.ID] = treatment
	}
	bindings := make([]lifecycle.Binding, 0, len(manifest.Blocks)*len(manifest.Treatments))
	for _, block := range manifest.Blocks {
		for _, assignment := range block.Assignments {
			treatment, ok := treatments[assignment.TreatmentID]
			if !ok {
				return nil, ErrExperimentProjection
			}
			experimentSHA256, ok := trialDigests[assignment.TrialID]
			if !ok {
				return nil, ErrExperimentProjection
			}
			skillSHA256 := treatment.SkillSHA256
			if skillSHA256 == "" {
				skillSHA256, err = contentMinimizedAttemptDigest("experiment-no-skill", treatment.ID)
				if err != nil {
					return nil, ErrExperimentProjection
				}
			}
			bindings = append(bindings, lifecycle.Binding{
				Privacy: lifecycle.PrivacyContentMinimized,
				Identity: lifecycle.Identity{
					ExperimentSHA256:  experimentSHA256,
					TaskSHA256:        manifest.Design.Case.TaskSHA256,
					SkillSHA256:       skillSHA256,
					AgentSHA256:       manifest.CapabilityContract.Runtime.AgentSHA256,
					ModelSHA256:       manifest.CapabilityContract.Runtime.ModelSHA256,
					EnvironmentSHA256: manifest.CapabilityContract.Runtime.EnvironmentSHA256,
					GraderSHA256:      manifest.CapabilityContract.Runtime.GraderSHA256,
					BudgetsSHA256:     manifest.CapabilityContract.Runtime.BudgetsSHA256,
					AdapterSHA256:     manifest.CapabilityContract.Runtime.AdapterSHA256,
					AuthoritySHA256:   manifest.CapabilityContract.Runtime.AuthoritySHA256,
				},
			})
		}
	}
	return bindings, nil
}

// EnsureExperimentRoster persists the exact full planned roster before any
// caller may commit its first attempt. It can complete a crash-interrupted
// exact prefix but never extends or changes a partially committed experiment.
func EnsureExperimentRoster(store *AttemptLedgerStore, manifest ExperimentManifest) ([]lifecycle.Plan, error) {
	if store == nil {
		return nil, ErrExperimentProjection
	}
	bindings, err := ExperimentAttemptBindings(manifest)
	if err != nil {
		return nil, err
	}
	return store.EnsureRoster(bindings)
}
