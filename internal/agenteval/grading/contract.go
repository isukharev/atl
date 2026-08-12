// Package grading owns provider-neutral grading contracts and implementations.
package grading

import (
	"encoding/json"
	"errors"
)

const (
	ContractSchema  = "agent-eval/grader-contract"
	PlanSchema      = "agent-eval/grading-plan"
	ReceiptSchema   = "agent-eval/grade-receipt"
	SchemaVersion   = 1
	ContractVersion = "0.1.0-pre-release"

	MaxContractBytes      = 64 << 10
	MaxPlanBytes          = 1 << 20
	MaxReceiptBytes       = 4 << 20
	MaxEvidenceBytes      = 16 << 20
	MaxEvidenceItems      = 4096
	MaxChecks             = 256
	MaxIdentifierBytes    = 128
	MaxReviewerModelBytes = 2048
	MaxRelativePathBytes  = 1024
	MaxExpectedJSONBytes  = 256 << 10
	MaxScriptInstructions = 4096
	MaxScriptStack        = 256
	MaxSequenceItems      = 1024
	MaxReviewers          = 5
	MaxCitationsPerCheck  = 64
	MaxTokens             = 10_000_000
	MaxCostMicroUSD       = 1_000_000_000
	MaxDurationMillis     = 60 * 60 * 1000
	MaxAggregateTokens    = MaxTokens * MaxReviewers
	MaxAggregateCost      = MaxCostMicroUSD * MaxReviewers
	MaxAggregateDuration  = MaxDurationMillis * MaxReviewers
	MaxJSONDepth          = 64
	MaxJSONValues         = 262_144
)

var (
	ErrContract    = errors.New("grader_contract_invalid")
	ErrUnsupported = errors.New("grader_unsupported")
	ErrPolicy      = errors.New("grader_policy_denied")
	ErrEvidence    = errors.New("grader_evidence_invalid")
	ErrExecution   = errors.New("grader_execution_failed")
	ErrInterrupted = errors.New("grader_interrupted")
)

type Support string

const (
	SupportNotApplicable Support = "not_applicable"
	SupportSupported     Support = "supported"
	SupportUnknown       Support = "unknown"
	SupportUnsupported   Support = "unsupported"
)

type Mode string

const (
	ModeDeterministic   Mode = "deterministic"
	ModeScriptDSL       Mode = "script_dsl"
	ModeJudgeAssessment Mode = "judge_assessment"
)

type ExecutionClass string

const (
	ExecutionInProcess         ExecutionClass = "in_process"
	ExecutionHermeticVerifier  ExecutionClass = "hermetic_verifier"
	ExecutionOfflineAssessment ExecutionClass = "offline_assessment"
)

type CheckKind string

const (
	CheckFileExists      CheckKind = "file.exists"
	CheckFileMetadata    CheckKind = "file.metadata"
	CheckFileSHA256      CheckKind = "file.sha256"
	CheckJSONValue       CheckKind = "json.value"
	CheckJSONSchema      CheckKind = "json.schema"
	CheckCommandExit     CheckKind = "command.exit"
	CheckCommandOutput   CheckKind = "command.output"
	CheckTreeDiff        CheckKind = "tree.diff"
	CheckToolSequence    CheckKind = "tool.sequence"
	CheckActionSequence  CheckKind = "action.sequence"
	CheckSkillActivation CheckKind = "skill.activation"
	CheckSkillUse        CheckKind = "skill.use"
	CheckBudget          CheckKind = "budget.maximum"
	CheckPolicy          CheckKind = "policy.violations"
	CheckQualitative     CheckKind = "qualitative.evidence"
)

var closedCheckKinds = []CheckKind{
	CheckActionSequence,
	CheckBudget,
	CheckCommandExit,
	CheckCommandOutput,
	CheckFileExists,
	CheckFileMetadata,
	CheckFileSHA256,
	CheckJSONSchema,
	CheckJSONValue,
	CheckPolicy,
	CheckQualitative,
	CheckSkillActivation,
	CheckSkillUse,
	CheckToolSequence,
	CheckTreeDiff,
}

type Presence string

const (
	PresenceNotApplicable Presence = "not_applicable"
	PresenceObserved      Presence = "observed"
	PresenceUnknown       Presence = "unknown"
	PresenceUnsupported   Presence = "unsupported"
)

type Visibility string

const (
	VisibilityPublic Visibility = "public"
	VisibilityHidden Visibility = "hidden"
)

type Capability struct {
	Kind    CheckKind `json:"kind"`
	Support Support   `json:"support"`
}

type ModePolicy struct {
	Mode           Mode           `json:"mode"`
	Support        Support        `json:"support"`
	ExecutionClass ExecutionClass `json:"execution_class"`
	Process        bool           `json:"process"`
	Provider       bool           `json:"provider"`
	Network        bool           `json:"network"`
	Credentials    bool           `json:"credentials"`
}

type Limits struct {
	MaxChecks             uint32 `json:"max_checks"`
	MaxEvidenceItems      uint32 `json:"max_evidence_items"`
	MaxEvidenceBytes      uint64 `json:"max_evidence_bytes"`
	MaxScriptInstructions uint32 `json:"max_script_instructions"`
	MaxCitationsPerCheck  uint32 `json:"max_citations_per_check"`
}

// Contract binds one exact grader implementation and its complete authority
// and check-family claims. Digests identify reviewed content; they are not
// publisher authentication.
type Contract struct {
	Schema               string       `json:"schema"`
	SchemaVersion        int          `json:"schema_version"`
	ContractVersion      string       `json:"contract_version"`
	GraderID             string       `json:"grader_id"`
	GraderVersion        string       `json:"grader_version"`
	ImplementationSHA256 string       `json:"implementation_sha256"`
	ContentSHA256        string       `json:"content_sha256"`
	Modes                []ModePolicy `json:"modes"`
	Capabilities         []Capability `json:"capabilities"`
	Limits               Limits       `json:"limits"`
}

type FileExistsRule struct {
	EvidenceID string `json:"evidence_id"`
	Expected   bool   `json:"expected"`
}

type FileMetadataRule struct {
	EvidenceID        string `json:"evidence_id"`
	ExpectedSizeBytes uint64 `json:"expected_size_bytes"`
	ExpectedMode      uint32 `json:"expected_mode"`
}

type FileSHA256Rule struct {
	EvidenceID     string `json:"evidence_id"`
	ExpectedSHA256 string `json:"expected_sha256"`
}

type JSONValueRule struct {
	EvidenceID string          `json:"evidence_id"`
	Pointer    string          `json:"pointer"`
	Expected   json.RawMessage `json:"expected"`
}

type JSONType string

const (
	JSONTypeArray   JSONType = "array"
	JSONTypeBoolean JSONType = "boolean"
	JSONTypeInteger JSONType = "integer"
	JSONTypeNull    JSONType = "null"
	JSONTypeNumber  JSONType = "number"
	JSONTypeObject  JSONType = "object"
	JSONTypeString  JSONType = "string"
)

type JSONField struct {
	Pointer  string   `json:"pointer"`
	Type     JSONType `json:"type"`
	Required bool     `json:"required"`
}

type JSONSchemaRule struct {
	EvidenceID string      `json:"evidence_id"`
	Fields     []JSONField `json:"fields"`
}

type CommandExitRule struct {
	EvidenceID string `json:"evidence_id"`
	Expected   int64  `json:"expected"`
}

type OutputStream string

const (
	OutputStdout OutputStream = "stdout"
	OutputStderr OutputStream = "stderr"
)

type CommandOutputRule struct {
	EvidenceID     string       `json:"evidence_id"`
	Stream         OutputStream `json:"stream"`
	ExpectedSHA256 string       `json:"expected_sha256"`
}

type TreeChangeKind string

const (
	TreeAdded    TreeChangeKind = "added"
	TreeModified TreeChangeKind = "modified"
	TreeRemoved  TreeChangeKind = "removed"
)

type TreeChangeExpectation struct {
	Path   string         `json:"path"`
	Kind   TreeChangeKind `json:"kind"`
	SHA256 string         `json:"sha256,omitempty"`
}

type TreeDiffRule struct {
	EvidenceID string                  `json:"evidence_id"`
	Expected   []TreeChangeExpectation `json:"expected"`
}

type SequenceRule struct {
	EvidenceID           string   `json:"evidence_id"`
	Expected             []string `json:"expected"`
	MinimumSimilarityBPS uint32   `json:"minimum_similarity_bps"`
}

type CountRule struct {
	EvidenceID string `json:"evidence_id"`
	Minimum    uint64 `json:"minimum"`
	Maximum    uint64 `json:"maximum"`
}

type BudgetRule struct {
	EvidenceID string `json:"evidence_id"`
	Minimum    uint64 `json:"minimum"`
	Maximum    uint64 `json:"maximum"`
}

type PolicyRule struct {
	EvidenceID        string `json:"evidence_id"`
	MaximumViolations uint64 `json:"maximum_violations"`
}

type QualitativeRule struct {
	RubricCriterionID string   `json:"rubric_criterion_id"`
	EvidenceIDs       []string `json:"evidence_ids"`
}

// Check uses one and only one typed rule matching Kind.
type Check struct {
	ID              string             `json:"id"`
	Kind            CheckKind          `json:"kind"`
	Visibility      Visibility         `json:"visibility"`
	FileExists      *FileExistsRule    `json:"file_exists,omitempty"`
	FileMetadata    *FileMetadataRule  `json:"file_metadata,omitempty"`
	FileSHA256      *FileSHA256Rule    `json:"file_sha256,omitempty"`
	JSONValue       *JSONValueRule     `json:"json_value,omitempty"`
	JSONSchema      *JSONSchemaRule    `json:"json_schema,omitempty"`
	CommandExit     *CommandExitRule   `json:"command_exit,omitempty"`
	CommandOutput   *CommandOutputRule `json:"command_output,omitempty"`
	TreeDiff        *TreeDiffRule      `json:"tree_diff,omitempty"`
	ToolSequence    *SequenceRule      `json:"tool_sequence,omitempty"`
	ActionSequence  *SequenceRule      `json:"action_sequence,omitempty"`
	SkillActivation *CountRule         `json:"skill_activation,omitempty"`
	SkillUse        *CountRule         `json:"skill_use,omitempty"`
	Budget          *BudgetRule        `json:"budget,omitempty"`
	Policy          *PolicyRule        `json:"policy,omitempty"`
	Qualitative     *QualitativeRule   `json:"qualitative,omitempty"`
}

type ScriptOperation string

const (
	ScriptFileExists        ScriptOperation = "load.file_exists"
	ScriptFileSHA256Equals  ScriptOperation = "load.file_sha256_equals"
	ScriptJSONEquals        ScriptOperation = "load.json_equals"
	ScriptCommandExitEquals ScriptOperation = "load.command_exit_equals"
	ScriptResourceMaximum   ScriptOperation = "load.resource_maximum"
	ScriptEventCountMinimum ScriptOperation = "load.event_count_minimum"
	ScriptAnd               ScriptOperation = "boolean.and"
	ScriptNot               ScriptOperation = "boolean.not"
	ScriptOr                ScriptOperation = "boolean.or"
	ScriptEmit              ScriptOperation = "emit"
)

type ScriptInstruction struct {
	Operation      ScriptOperation `json:"operation"`
	CheckID        string          `json:"check_id,omitempty"`
	EvidenceID     string          `json:"evidence_id,omitempty"`
	Pointer        string          `json:"pointer,omitempty"`
	ExpectedSHA256 string          `json:"expected_sha256,omitempty"`
	ExpectedJSON   json.RawMessage `json:"expected_json,omitempty"`
	Integer        *int64          `json:"integer,omitempty"`
	Unsigned       *uint64         `json:"unsigned,omitempty"`
}

type ReviewerKind string

const (
	ReviewerHuman ReviewerKind = "human"
	ReviewerModel ReviewerKind = "model"
)

type Reviewer struct {
	ID                       string       `json:"id"`
	Kind                     ReviewerKind `json:"kind"`
	Model                    string       `json:"model,omitempty"`
	EnvironmentSHA256        string       `json:"environment_sha256,omitempty"`
	MaxInputTokens           uint64       `json:"max_input_tokens"`
	MaxOutputTokens          uint64       `json:"max_output_tokens"`
	MaxEstimatedCostMicroUSD uint64       `json:"max_estimated_cost_microusd"`
}

type JudgePolicy struct {
	RubricSHA256          string     `json:"rubric_sha256"`
	PromptContractSHA256  string     `json:"prompt_contract_sha256"`
	BlindAssignmentSHA256 string     `json:"blind_assignment_sha256"`
	ToolPolicy            string     `json:"tool_policy"`
	Reviewers             []Reviewer `json:"reviewers"`
}

type PlanLimits struct {
	DeadlineMillis uint64 `json:"deadline_millis"`
	MaxInputBytes  uint64 `json:"max_input_bytes"`
	MaxOutputBytes uint64 `json:"max_output_bytes"`
}

// Plan preregisters grading semantics before an attempt can be committed.
type Plan struct {
	Schema                 string              `json:"schema"`
	SchemaVersion          int                 `json:"schema_version"`
	ContractVersion        string              `json:"contract_version"`
	ContractSHA256         string              `json:"contract_sha256"`
	Mode                   Mode                `json:"mode"`
	InputProjectionSHA256  string              `json:"input_projection_sha256"`
	EnvironmentSHA256      string              `json:"environment_sha256"`
	ExecutionBackendSHA256 string              `json:"execution_backend_sha256,omitempty"`
	Checks                 []Check             `json:"checks"`
	Script                 []ScriptInstruction `json:"script,omitempty"`
	Judge                  *JudgePolicy        `json:"judge,omitempty"`
	Limits                 PlanLimits          `json:"limits"`
}

// AdmittedPlan is an immutable contract/plan snapshot. Accessors return deep
// copies so caller mutation cannot change grading authority after admission.
type AdmittedPlan struct {
	contract Contract
	plan     Plan
	planSHA  string
}

func (a AdmittedPlan) Contract() Contract { return cloneContract(a.contract) }
func (a AdmittedPlan) Plan() Plan         { return clonePlan(a.plan) }
func (a AdmittedPlan) SHA256() string     { return a.planSHA }

type Citation struct {
	EvidenceID string       `json:"evidence_id"`
	Kind       EvidenceKind `json:"kind"`
	Visibility Visibility   `json:"visibility"`
	SHA256     string       `json:"sha256"`
}

type EvidenceKind string

const (
	EvidenceFile     EvidenceKind = "file"
	EvidenceCommand  EvidenceKind = "command"
	EvidenceTree     EvidenceKind = "tree"
	EvidenceSequence EvidenceKind = "sequence"
	EvidenceCounter  EvidenceKind = "counter"
)

type Authority string

const (
	AuthorityDeterministic Authority = "deterministic"
	AuthorityScript        Authority = "script"
	AuthorityJudge         Authority = "judge"
)

type Decision struct {
	CheckID   string     `json:"check_id"`
	Presence  Presence   `json:"presence"`
	Passed    bool       `json:"passed"`
	Authority Authority  `json:"authority"`
	Citations []Citation `json:"citations"`
}

type MetricPresence struct {
	Presence Presence `json:"presence"`
	Value    uint64   `json:"value"`
}

type Usage struct {
	InputTokens           MetricPresence `json:"input_tokens"`
	OutputTokens          MetricPresence `json:"output_tokens"`
	EstimatedCostMicroUSD MetricPresence `json:"estimated_cost_microusd"`
	DurationMillis        MetricPresence `json:"duration_millis"`
}

// ReviewDecision is a content-minimized reviewer decision. Citations bind the
// decision to the admitted evidence projection; they never contain raw output.
type ReviewDecision struct {
	CheckID   string     `json:"check_id"`
	Passed    bool       `json:"passed"`
	Citations []Citation `json:"citations"`
}

// Review is supplied after a human or model reviewer has completed work in a
// separately governed environment. This package never launches a reviewer.
type Review struct {
	ReviewerID               string           `json:"reviewer_id"`
	RubricSHA256             string           `json:"rubric_sha256"`
	PromptContractSHA256     string           `json:"prompt_contract_sha256"`
	BlindAssignmentSHA256    string           `json:"blind_assignment_sha256"`
	EvidenceProjectionSHA256 string           `json:"evidence_projection_sha256"`
	Decisions                []ReviewDecision `json:"decisions"`
	Usage                    Usage            `json:"usage"`
}

// ReviewerReceipt preserves exact reviewer provenance and bounded resource
// coverage without retaining a prompt, raw task, or raw evidence.
type ReviewerReceipt struct {
	ReviewerID        string       `json:"reviewer_id"`
	Kind              ReviewerKind `json:"kind"`
	Model             string       `json:"model,omitempty"`
	EnvironmentSHA256 string       `json:"environment_sha256,omitempty"`
	ReviewSHA256      string       `json:"review_sha256"`
	Usage             Usage        `json:"usage"`
}

type DisagreementKind string

const (
	DisagreementReviewers          DisagreementKind = "reviewers"
	DisagreementDeterministicJudge DisagreementKind = "deterministic_vs_judge"
)

type Disagreement struct {
	CheckID string           `json:"check_id"`
	Kind    DisagreementKind `json:"kind"`
}

// DeterministicComparison binds a separately validated deterministic receipt
// used only to surface cross-authority disagreement. Pairs are sorted and may
// name each judge check at most once.
type DeterministicComparison struct {
	Plan    Plan             `json:"plan"`
	Receipt Receipt          `json:"receipt"`
	Pairs   []ComparisonPair `json:"pairs"`
}

type ComparisonPair struct {
	JudgeCheckID         string `json:"judge_check_id"`
	DeterministicCheckID string `json:"deterministic_check_id"`
}

type ReceiptStatus string

const (
	ReceiptComplete   ReceiptStatus = "complete"
	ReceiptIncomplete ReceiptStatus = "incomplete"
)

// Receipt is the content-minimized terminal grading projection.
type Receipt struct {
	Schema                string            `json:"schema"`
	SchemaVersion         int               `json:"schema_version"`
	ContractVersion       string            `json:"contract_version"`
	ContractSHA256        string            `json:"contract_sha256"`
	PlanSHA256            string            `json:"plan_sha256"`
	InputProjectionSHA256 string            `json:"input_projection_sha256"`
	EvidenceSHA256        string            `json:"evidence_sha256"`
	Evidence              []Citation        `json:"evidence"`
	Status                ReceiptStatus     `json:"status"`
	Decisions             []Decision        `json:"decisions"`
	Reviewers             []ReviewerReceipt `json:"reviewers"`
	Usage                 Usage             `json:"usage"`
	Disagreements         []Disagreement    `json:"disagreements"`
}
