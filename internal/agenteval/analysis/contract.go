// Package analysis computes bounded, deterministic statistical projections
// from one immutable experiment manifest and its canonical trial records.
// It owns no execution, filesystem, process, transport, credential, private
// workspace, scheduling, selection, tuning, or promotion authority.
package analysis

import (
	"errors"

	"github.com/isukharev/atl/internal/agenteval/experiment"
)

const (
	ReportSchema    = "agent-eval/analysis-report"
	SchemaVersion   = 1
	ContractVersion = experiment.ContractVersion

	MaxReportBytes       = 16 << 20
	MaxInputRecords      = experiment.MaxTrials * 2
	MaxPrimitiveDraws    = experiment.MaxBootstrapDraws
	MaxProbabilityDigits = 2048
	MaxStratifiedResults = experiment.MaxComparisons * experiment.MaxStrata
	MaxDimensionResults  = 16_384
	MaxPairedDeltas      = 65_536
	MaxPassAtKResults    = 4_096
	MaxTrialProjections  = experiment.MaxTrials * (experiment.MaxStages + experiment.MaxMetrics)
	maxMetricDelta       = int64(experiment.MaxMetricValue)
)

type ErrorCode string

const (
	ErrorInvalidInput  ErrorCode = "invalid_analysis_input"
	ErrorInvalidReport ErrorCode = "invalid_analysis_report"
	ErrorLimitExceeded ErrorCode = "analysis_limit_exceeded"
	ErrorInterrupted   ErrorCode = "analysis_interrupted"
)

type Error struct {
	code  ErrorCode
	cause error
}

func (e *Error) Error() string   { return string(e.code) }
func (e *Error) Unwrap() error   { return e.cause }
func (e *Error) Code() ErrorCode { return e.code }

func contractError(code ErrorCode, cause error) error { return &Error{code: code, cause: cause} }

func CodeOf(err error) (ErrorCode, bool) {
	var target *Error
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Code(), true
}

type InferenceStatus string

const (
	InferenceInsufficient InferenceStatus = "insufficient"
	InferenceDescriptive  InferenceStatus = "descriptive"
	InferenceInferential  InferenceStatus = "inferential"
)

type PairStatus string

const (
	PairComplete  PairStatus = "complete"
	PairExcluded  PairStatus = "excluded"
	PairMissing   PairStatus = "missing"
	PairDuplicate PairStatus = "duplicate"
)

type DimensionKind string

const (
	DimensionStage  DimensionKind = "stage"
	DimensionMetric DimensionKind = "metric"
)

type ExactTestMethod string

const (
	ExactMcNemar ExactTestMethod = "mcnemar_exact_binomial"
	ExactSign    ExactTestMethod = "paired_sign_exact_binomial"
)

type MultiplicityStatus string

const (
	MultiplicityHolmAdjusted       MultiplicityStatus = "holm_adjusted"
	MultiplicityExploratoryRawOnly MultiplicityStatus = "exploratory_unadjusted"
)

type ParetoRelation string

const (
	ParetoUnavailable        ParetoRelation = "unavailable"
	ParetoEqual              ParetoRelation = "equal"
	ParetoCandidateDominates ParetoRelation = "candidate_dominates"
	ParetoReferenceDominates ParetoRelation = "reference_dominates"
	ParetoTradeoff           ParetoRelation = "tradeoff"
)

// Rational is a reduced exact fraction with a positive denominator. Decimal
// strings avoid machine-width and floating-point drift in public bytes.
type Rational struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
}

type Interval struct {
	Method                string   `json:"method"`
	ConfidenceBasisPoints uint16   `json:"confidence_basis_points"`
	Samples               uint32   `json:"samples"`
	Lower                 Rational `json:"lower"`
	Upper                 Rational `json:"upper"`
}

type ExactTest struct {
	Method              ExactTestMethod    `json:"method"`
	Left                uint32             `json:"left"`
	Right               uint32             `json:"right"`
	RawProbability      Rational           `json:"raw_probability"`
	Multiplicity        MultiplicityStatus `json:"multiplicity"`
	FamilySHA256        string             `json:"family_sha256"`
	AdjustedProbability *Rational          `json:"adjusted_probability,omitempty"`
	RejectNull          *bool              `json:"reject_null,omitempty"`
}

type PairCoverage struct {
	PairID       string                       `json:"pair_id"`
	BlockID      string                       `json:"block_id"`
	StratumID    string                       `json:"stratum_id"`
	ComparisonID string                       `json:"comparison_id"`
	Status       PairStatus                   `json:"status"`
	Reasons      []experiment.ExclusionReason `json:"reasons"`
}

type ReasonCount struct {
	Reason experiment.ExclusionReason `json:"reason"`
	Count  uint32                     `json:"count"`
}

// TrialStageProjection retains the closed presence class and Boolean value of
// one clean singleton stage. It contains no body, path, or free-form value.
type TrialStageProjection struct {
	Stage    experiment.FunnelStage `json:"stage"`
	Presence experiment.Presence    `json:"presence"`
	Value    *bool                  `json:"value,omitempty"`
}

// TrialMetricProjection retains the closed presence class of one clean
// singleton metric. Value is present only for manifest-declared binary metrics;
// absolute count values remain omitted from the content-minimized report.
type TrialMetricProjection struct {
	Metric   experiment.MetricID `json:"metric"`
	Presence experiment.Presence `json:"presence"`
	Value    *bool               `json:"value,omitempty"`
}

// TrialCoverage retains the manifest-derived trial identity, input
// multiplicity, record-level exclusion, and the minimum labeled observation
// projection needed to authenticate pair reasons and aggregate summaries.
// Stage and metric projections are populated only for clean singletons.
type TrialCoverage struct {
	TrialID   string                     `json:"trial_id"`
	Records   uint32                     `json:"records"`
	Exclusion experiment.ExclusionReason `json:"exclusion"`
	Stages    []TrialStageProjection     `json:"stages"`
	Metrics   []TrialMetricProjection    `json:"metrics"`
}

type Coverage struct {
	ExpectedRecords  uint32          `json:"expected_records"`
	ReceivedRecords  uint32          `json:"received_records"`
	UniqueRecords    uint32          `json:"unique_records"`
	MissingRecords   uint32          `json:"missing_records"`
	DuplicateRecords uint32          `json:"duplicate_records"`
	CompletePairs    uint32          `json:"complete_pairs"`
	ExcludedPairs    uint32          `json:"excluded_pairs"`
	Members          []TrialCoverage `json:"members"`
	Pairs            []PairCoverage  `json:"pairs"`
	Reasons          []ReasonCount   `json:"reasons"`
}

type BinaryResult struct {
	Kind              DimensionKind         `json:"kind"`
	ID                string                `json:"id"`
	Role              experiment.MetricRole `json:"role"`
	FamilySHA256      string                `json:"family_sha256"`
	Direction         experiment.Direction  `json:"direction"`
	Status            InferenceStatus       `json:"status"`
	CompletePairs     uint32                `json:"complete_pairs"`
	Pairs             []BinaryPair          `json:"pairs"`
	BothFalse         uint32                `json:"both_false"`
	ReferenceOnly     uint32                `json:"reference_only"`
	CandidateOnly     uint32                `json:"candidate_only"`
	BothTrue          uint32                `json:"both_true"`
	RiskDifference    Rational              `json:"risk_difference"`
	Interval          *Interval             `json:"interval,omitempty"`
	ExactTest         *ExactTest            `json:"exact_test,omitempty"`
	DirectionAdjusted Rational              `json:"direction_adjusted_effect"`
	Regression        bool                  `json:"regression"`
}

type PairDelta struct {
	PairID string   `json:"pair_id"`
	Delta  Rational `json:"delta"`
}

type BinaryPair struct {
	PairID    string `json:"pair_id"`
	Reference bool   `json:"reference"`
	Candidate bool   `json:"candidate"`
}

type ContinuousResult struct {
	Metric            experiment.MetricID   `json:"metric"`
	Role              experiment.MetricRole `json:"role"`
	FamilySHA256      string                `json:"family_sha256"`
	Direction         experiment.Direction  `json:"direction"`
	Status            InferenceStatus       `json:"status"`
	CompletePairs     uint32                `json:"complete_pairs"`
	Deltas            []PairDelta           `json:"deltas"`
	CandidateHigher   uint32                `json:"candidate_higher"`
	ReferenceHigher   uint32                `json:"reference_higher"`
	Equal             uint32                `json:"equal"`
	MeanDelta         Rational              `json:"mean_delta"`
	MedianDelta       Rational              `json:"median_delta"`
	PairedSignEffect  Rational              `json:"paired_sign_effect"`
	Interval          *Interval             `json:"interval,omitempty"`
	ExactTest         *ExactTest            `json:"exact_test,omitempty"`
	DirectionAdjusted Rational              `json:"direction_adjusted_effect"`
	Regression        bool                  `json:"regression"`
}

type ComparisonResult struct {
	ComparisonID         string             `json:"comparison_id"`
	StratumID            string             `json:"stratum_id"`
	ReferenceTreatmentID string             `json:"reference_treatment_id"`
	CandidateTreatmentID string             `json:"candidate_treatment_id"`
	CompletePairs        uint32             `json:"complete_pairs"`
	Binary               []BinaryResult     `json:"binary"`
	Continuous           []ContinuousResult `json:"continuous"`
	Pareto               ParetoRelation     `json:"pareto"`
}

type ActivationSummary struct {
	StratumID           string    `json:"stratum_id"`
	Observed            uint32    `json:"observed"`
	Missing             uint32    `json:"missing"`
	TruePositive        uint32    `json:"true_positive"`
	FalsePositive       uint32    `json:"false_positive"`
	TrueNegative        uint32    `json:"true_negative"`
	FalseNegative       uint32    `json:"false_negative"`
	Precision           *Rational `json:"precision,omitempty"`
	Recall              *Rational `json:"recall,omitempty"`
	FalseActivationRate *Rational `json:"false_activation_rate,omitempty"`
	UnnecessaryLoadRate *Rational `json:"unnecessary_load_rate,omitempty"`
}

type FunnelStage struct {
	Stage               experiment.FunnelStage `json:"stage"`
	Observed            uint32                 `json:"observed"`
	Reached             uint32                 `json:"reached"`
	EligibleTransitions uint32                 `json:"eligible_transitions"`
	Converted           uint32                 `json:"converted"`
	Rate                *Rational              `json:"rate,omitempty"`
	Conversion          *Rational              `json:"conversion,omitempty"`
}

type TreatmentFunnel struct {
	StratumID   string        `json:"stratum_id"`
	TreatmentID string        `json:"treatment_id"`
	Trials      uint32        `json:"trials"`
	Stages      []FunnelStage `json:"stages"`
}

type PassAtKResult struct {
	StratumID   string          `json:"stratum_id"`
	TreatmentID string          `json:"treatment_id"`
	K           uint32          `json:"k"`
	Status      InferenceStatus `json:"status"`
	Attempts    uint32          `json:"attempts"`
	Passed      uint32          `json:"passed"`
	PassAtK     *Rational       `json:"pass_at_k,omitempty"`
	PassPowerK  *Rational       `json:"pass_power_k,omitempty"`
}

type Report struct {
	Schema                 string                  `json:"schema"`
	SchemaVersion          int                     `json:"schema_version"`
	ContractVersion        string                  `json:"contract_version"`
	ManifestSHA256         string                  `json:"manifest_sha256"`
	AnalysisPlanSHA256     string                  `json:"analysis_plan_sha256"`
	InputSetSHA256         string                  `json:"input_set_sha256"`
	ConfidenceBasisPoints  uint16                  `json:"confidence_basis_points"`
	MinimumInferenceBlocks uint32                  `json:"minimum_inference_blocks"`
	BootstrapSamples       uint32                  `json:"bootstrap_samples"`
	Multiplicity           experiment.Multiplicity `json:"multiplicity"`
	Coverage               Coverage                `json:"coverage"`
	Comparisons            []ComparisonResult      `json:"comparisons"`
	Activation             []ActivationSummary     `json:"activation"`
	Funnels                []TreatmentFunnel       `json:"funnels"`
	PassAtK                []PassAtKResult         `json:"pass_at_k"`
	ReportSHA256           string                  `json:"report_sha256"`
}
