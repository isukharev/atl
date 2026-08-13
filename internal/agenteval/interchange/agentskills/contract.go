// Package agentskills imports and exports bounded, local, non-authoritative
// compatibility views for documented Agent Skills evaluation layouts. It owns
// no runner, judge, provider, backend, credential, network, or process
// authority.
package agentskills

import (
	"errors"

	"github.com/isukharev/atl/internal/agenteval/core"
)

const (
	MaxJSONBytes         = 1 << 20
	MaxFileBytes         = 8 << 20
	MaxTreeBytes         = 64 << 20
	MaxTreeEntries       = 4096
	MaxCases             = 256
	MaxCriteriaPerCase   = 255
	MaxFilesPerCase      = 256
	MaxPathBytes         = 512
	MaxTextBytes         = 64 << 10
	MaxRuns              = 4096
	MaxOutputsPerRun     = 256
	MaxFeedbackEntries   = 64
	MaxNotes             = 256
	MaxAttempts          = 1024
	MaxJSONDepth         = 128
	SHA256HexCharacters  = 64
	CompatibilityVersion = 1
)

// Format identifies one explicit documented compatibility layout. FormatAuto
// is admitted only when one unambiguous criteria spelling occurs in evals.json.
type Format string

const (
	FormatAuto                    Format = "auto"
	FormatAgentSkillsGuideV1      Format = "agentskills-guide-v1"
	FormatAnthropicSkillCreatorV1 Format = "anthropic-skill-creator-v1"
)

// Baseline is the explicit comparison intent. It is never inferred from
// ambient files or from the presence of a configured agent.
type Baseline string

const (
	BaselineNoSkill       Baseline = "no-skill"
	BaselinePreviousSkill Baseline = "previous-skill"
)

// TreatmentKind is the normalized execution-cell role.
type TreatmentKind string

const (
	TreatmentCurrentSkill  TreatmentKind = "current-skill"
	TreatmentNoSkill       TreatmentKind = "no-skill"
	TreatmentPreviousSkill TreatmentKind = "previous-skill"
)

// CriterionKind distinguishes the two documented criteria spellings.
// Projection declares checks but does not select or imply a judge.
type CriterionKind string

const (
	CriterionAssertion   CriterionKind = "assertion"
	CriterionExpectation CriterionKind = "expectation"
)

// ErrorCode is the closed compatibility failure classification. Error strings
// never contain caller paths or source content.
type ErrorCode string

const (
	ErrorInvalidRequest        ErrorCode = "invalid_request"
	ErrorInvalidRoot           ErrorCode = "invalid_root"
	ErrorUnstableSource        ErrorCode = "unstable_source"
	ErrorLimitExceeded         ErrorCode = "limit_exceeded"
	ErrorInvalidSkill          ErrorCode = "invalid_skill"
	ErrorInvalidEvals          ErrorCode = "invalid_evals"
	ErrorInvalidWorkspace      ErrorCode = "invalid_workspace"
	ErrorInvalidProjection     ErrorCode = "invalid_projection"
	ErrorInvalidExport         ErrorCode = "invalid_export"
	ErrorInvalidPublication    ErrorCode = "invalid_publication"
	ErrorInvalidDestination    ErrorCode = "invalid_destination"
	ErrorPublicationFailed     ErrorCode = "publication_failed"
	ErrorInvalidSecurityPolicy ErrorCode = "invalid_security_policy"
)

// Error retains an inspectable cause without rendering it.
type Error struct {
	code  ErrorCode
	cause error
}

func (e *Error) Error() string   { return string(e.code) }
func (e *Error) Unwrap() error   { return e.cause }
func (e *Error) Code() ErrorCode { return e.code }

func contractError(code ErrorCode, cause error) error {
	return &Error{code: code, cause: cause}
}

// CodeOf extracts an Agent Skills compatibility error classification.
func CodeOf(err error) (ErrorCode, bool) {
	var target *Error
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Code(), true
}

// Disposition records whether a present source field has an exact normalized
// representation. Strictly invalid fields fail decoding and do not appear as
// losses.
type Disposition string

const (
	DispositionPreservedSourceOnly Disposition = "preserved_source_only"
	DispositionOmitted             Disposition = "omitted"
	DispositionUnsupported         Disposition = "unsupported"
)

// ReportCode is a content-free, closed diagnostic or coverage identity.
type ReportCode string

const (
	ReportRunnerUnbound              ReportCode = "runner_unbound"
	ReportJudgeUnbound               ReportCode = "judge_unbound"
	ReportSandboxUnbound             ReportCode = "sandbox_unbound"
	ReportEnvironmentUnbound         ReportCode = "environment_unbound"
	ReportAllowedToolsUnbound        ReportCode = "allowed_tools_unbound"
	ReportCompatibilityUnbound       ReportCode = "compatibility_unbound"
	ReportActivationUnbound          ReportCode = "activation_unbound"
	ReportVerifierCoverageUnbound    ReportCode = "verifier_coverage_unbound"
	ReportVerifierCoverageMissing    ReportCode = "verifier_coverage_missing"
	ReportMetricUnknown              ReportCode = "metric_unknown"
	ReportMetricUnsupported          ReportCode = "metric_unsupported"
	ReportMetricNotApplicable        ReportCode = "metric_not_applicable"
	ReportModelMetadataOmitted       ReportCode = "model_metadata_omitted"
	ReportSkillMetadataOmitted       ReportCode = "skill_metadata_omitted"
	ReportTimestampOmitted           ReportCode = "timestamp_omitted"
	ReportPathOmitted                ReportCode = "path_omitted"
	ReportRunDetailsOmitted          ReportCode = "run_details_omitted"
	ReportRunMetadataPreserved       ReportCode = "run_metadata_preserved"
	ReportTimingDetailPreserved      ReportCode = "timing_detail_preserved"
	ReportBenchmarkMissing           ReportCode = "benchmark_missing"
	ReportBenchmarkSummaryPreserved  ReportCode = "benchmark_summary_preserved"
	ReportGradingMissing             ReportCode = "grading_missing"
	ReportTimingMissing              ReportCode = "timing_missing"
	ReportOutputsMissing             ReportCode = "outputs_missing"
	ReportOutputsOmitted             ReportCode = "outputs_omitted"
	ReportOutputsPreservedSourceOnly ReportCode = "outputs_preserved_source_only"
	ReportTimingPreservedSourceOnly  ReportCode = "timing_preserved_source_only"
	ReportReviewContentPreserved     ReportCode = "review_content_preserved"
	ReportSkillDigestPreserved       ReportCode = "skill_digest_preserved"
)

// ReportEntry contains schema vocabulary and counts only. Scope is a fixed
// schema location, never a filesystem path or a source value.
type ReportEntry struct {
	Code            ReportCode
	Scope           string
	Disposition     Disposition
	Count           uint32
	BlocksExecution bool
}

// Report is sorted by code, scope, disposition, and blocking state.
type Report struct {
	Entries []ReportEntry
}

// BlocksExecution reports whether an imported format assumption still needs
// an explicit binding before a runner may be invoked.
func (r Report) BlocksExecution() bool {
	for _, entry := range r.Entries {
		if entry.BlocksExecution {
			return true
		}
	}
	return false
}

// ImportRequest selects only local roots and an explicit comparison. EvalRoot
// defaults to the evals directory below SkillRoot. Input paths in evals.json
// remain relative to SkillRoot, matching both format variants.
type ImportRequest struct {
	SkillRoot         string
	EvalRoot          string
	PreviousSkillRoot string
	Format            Format
	Baseline          Baseline
}

// SnapshotFile is one exact captured regular file. Data belongs to the
// returned result and callers should clone it before mutation.
type SnapshotFile struct {
	Path      string
	SHA256    string
	SizeBytes uint64
	Data      []byte
}

// SkillSnapshot is a byte- and path-addressed skill tree. Evals are excluded
// from treatment identity and captured separately as experiment inputs.
type SkillSnapshot struct {
	Name          string
	ContentSHA256 string
	Files         []SnapshotFile
}

// InputFile binds one eval reference to captured bytes without retaining an
// ambient path.
type InputFile struct {
	Path      string
	SHA256    string
	SizeBytes uint64
	Data      []byte
}

// Criterion preserves source text, order, and spelling exactly.
type Criterion struct {
	Kind        CriterionKind
	SourceField string
	Ordinal     uint32
	Text        string
}

// Case is one normalized eval case.
type Case struct {
	ID              uint32
	Prompt          string
	ExpectedOutput  string
	FilesPresent    bool
	CriteriaPresent bool
	Inputs          []InputFile
	Criteria        []Criterion
}

// Experiment is the complete captured source projection. ContentSHA256 binds
// exact selected source bytes; NormalizedSHA256 binds their semantic mapping.
type Experiment struct {
	Format           Format
	Baseline         Baseline
	ContentSHA256    string
	NormalizedSHA256 string
	Skill            SkillSnapshot
	PreviousSkill    *SkillSnapshot
	Cases            []Case
}

// ImportResult returns both the usable local values and every format-level
// assumption which was not mapped into neutral core values.
type ImportResult struct {
	Experiment Experiment
	Report     Report
}

// ProjectOptions supplies the profile chosen by a composition root. The
// interchange adapter deliberately has no profile registry or default.
type ProjectOptions struct {
	Profile  core.ProfileID
	Attempts uint32
}

// PlanProjection retains the source case and treatment role beside one
// neutral plan.
type PlanProjection struct {
	CaseID    uint32
	Treatment TreatmentKind
	Plan      core.Plan
}

// MetricPresence keeps an observed zero distinct from missing, unsupported,
// and not-applicable measurements in compatibility exports.
type MetricPresence string

const (
	MetricUnknown       MetricPresence = "unknown"
	MetricObserved      MetricPresence = "observed"
	MetricUnsupported   MetricPresence = "unsupported"
	MetricNotApplicable MetricPresence = "not_applicable"
)

type OptionalUint64 struct {
	Presence MetricPresence
	Value    uint64
}

// FeedbackEntry preserves one documented review-feedback map member in
// canonical key order.
type FeedbackEntry struct {
	Key   string
	Value string
}

// GradeResult is one compatibility assertion/expectation result.
type GradeResult struct {
	Text     string
	Passed   bool
	Evidence string
}

// GradingView is deliberately not an evaluator receipt. Timing and token
// coverage are observations used by compatibility views only.
type GradingView struct {
	Results               []GradeResult
	DurationMillis        OptionalUint64
	TotalTokens           OptionalUint64
	EstimatedCostMicroUSD OptionalUint64
	FeedbackPresent       bool
	Feedback              []FeedbackEntry
}

// BenchmarkRun is one non-authoritative compatibility row.
type BenchmarkRun struct {
	CaseID uint32
	// EvalName preserves the Anthropic benchmark run label. Empty asks an
	// authoring caller to use the canonical eval-<id> compatibility label.
	EvalName      string
	Configuration TreatmentKind
	RunNumber     uint32
	Grading       GradingView
	NotesPresent  bool
	Notes         []string
}

// BenchmarkView supplies only local, caller-selected metadata. Empty model
// fields remain unknown rather than acquiring ambient defaults.
type BenchmarkView struct {
	SkillName       string
	ExecutorModel   string
	AnalyzerModel   string
	Timestamp       string
	Runs            []BenchmarkRun
	FeedbackPresent bool
	Feedback        []FeedbackEntry
	NotesPresent    bool
	Notes           []string
}

// CompatibilityArtifact is a view for an upstream tool, never an evaluator
// lifecycle or grading receipt.
type CompatibilityArtifact struct {
	Format Format
	Kind   string
	Data   []byte
	Report Report
}

// Authoritative is always false by contract.
func (CompatibilityArtifact) Authoritative() bool { return false }

// CaseDirectory binds one exact iteration-N/eval-<slug> guide directory to one
// imported eval ID. Guide workspace slugs are never inferred from order or
// text, and all bindings in a request must share one positive iteration N.
type CaseDirectory struct {
	CaseID uint32
	Path   string
}

// WorkspaceImportRequest binds a stable workspace read to one already
// imported experiment. FormatAuto is never accepted for workspace layouts.
type WorkspaceImportRequest struct {
	Root            string
	Format          Format
	Experiment      Experiment
	CaseDirectories []CaseDirectory
}

// WorkspaceMetadata preserves the model metadata admitted by the Anthropic
// benchmark schema. Paths remain informational and grant no read authority.
type WorkspaceMetadata struct {
	Present              bool
	SkillName            string
	SkillPathPresent     bool
	SkillPath            string
	ExecutorModelPresent bool
	ExecutorModel        string
	AnalyzerModelPresent bool
	AnalyzerModel        string
	TimestampPresent     bool
	Timestamp            string
}

// WorkspaceRun is one stable, non-authoritative iteration cell capture.
type WorkspaceRun struct {
	CaseID         uint32
	EvalName       string
	Configuration  TreatmentKind
	RunNumber      uint32
	OutputsPresent bool
	Outputs        []SnapshotFile
	TimingPresent  bool
	TimingFile     *SnapshotFile
	DurationMillis OptionalUint64
	TotalTokens    OptionalUint64
	// EstimatedCostMicroUSD is unsupported by both compatibility workspace
	// schemas and is retained as an explicit presence state only.
	EstimatedCostMicroUSD OptionalUint64
	GradingPresent        bool
	GradingFile           *SnapshotFile
	Grading               GradingView
	NotesPresent          bool
	Notes                 []string
}

// Workspace is the normalized projection of one exact stable iteration tree.
type Workspace struct {
	Format              Format
	ExperimentSHA256    string
	PreviousSkillSHA256 string
	ContentSHA256       string
	Runs                []WorkspaceRun
	BenchmarkPresent    bool
	BenchmarkFile       *SnapshotFile
	Metadata            WorkspaceMetadata
	FeedbackPresent     bool
	Feedback            []FeedbackEntry
	NotesPresent        bool
	Notes               []string
}

// WorkspaceImportResult returns the stable values and every unsupported or
// missing format field discovered while mapping them.
type WorkspaceImportResult struct {
	Workspace Workspace
	Report    Report
}

// WorkspacePublicationRequest creates local, non-authoritative compatibility
// files. A bound Source permits exact captured output and timing bytes to join
// re-encoded grading.json and benchmark.json; no ambient source path is read.
// Planning never executes a runner or grader.
type WorkspacePublicationRequest struct {
	Format          Format
	Experiment      Experiment
	CaseDirectories []CaseDirectory
	Benchmark       BenchmarkView
	// Source optionally supplies already captured local output and timing bytes.
	// The planner never reads ambient paths from this value.
	Source *Workspace
}

// PublicationFile is one exact planned regular file.
type PublicationFile struct {
	Path      string
	SHA256    string
	SizeBytes uint64
	Data      []byte
}

// WorkspacePublicationPlan is a deterministic, non-authoritative in-memory
// publication. WriteNew is its only filesystem mutation operation.
type WorkspacePublicationPlan struct {
	Format              Format
	Baseline            Baseline
	ExperimentSHA256    string
	PreviousSkillSHA256 string
	CaseDirectories     []CaseDirectory
	ContentSHA256       string
	Files               []PublicationFile
	Report              Report
}

// Authoritative is always false by contract.
func (WorkspacePublicationPlan) Authoritative() bool { return false }
