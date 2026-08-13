// Package experiment owns neutral, content-addressed causal experiment plans.
// It has no runner, provider, backend, filesystem, credential, private-root,
// grader, lifecycle, or ATL product authority.
package experiment

import "errors"

const (
	CapabilitySchema = "agent-eval/experiment-capability-contract"
	DesignSchema     = "agent-eval/experiment-design"
	AnalysisSchema   = "agent-eval/analysis-plan"
	ManifestSchema   = "agent-eval/experiment-manifest"
	TrialSchema      = "agent-eval/trial-record"
	SchemaVersion    = 1
	ContractVersion  = "0.1.0-pre-release"

	MaxCapabilityBytes         = 64 << 10
	MaxDesignBytes             = 1 << 20
	MaxAnalysisBytes           = 1 << 20
	MaxManifestBytes           = 16 << 20
	MaxTrialBytes              = 1 << 20
	MaxTreatments              = 64
	MaxDistractors             = 64
	MaxStrata                  = 64
	MaxBlocks                  = 4096
	MaxTrials                  = 4096
	MaxPairBindings            = 16_384
	MaxCapabilities            = 64
	MaxStages                  = 8
	MaxMetrics                 = 64
	MaxComparisons             = 128
	MaxBootstrapSamples        = 20000
	MaxBootstrapDraws          = 16_777_216
	MaxPassK                   = 64
	MaxSafetyStopCodes         = 8
	MaxMetricValue      uint64 = 1<<53 - 1
)

type ErrorCode string

const (
	ErrorInvalidCapability ErrorCode = "invalid_experiment_capability_contract"
	ErrorInvalidDesign     ErrorCode = "invalid_experiment_design"
	ErrorInvalidAnalysis   ErrorCode = "invalid_analysis_plan"
	ErrorUnsupportedDesign ErrorCode = "unsupported_experiment_design"
	ErrorInvalidManifest   ErrorCode = "invalid_experiment_manifest"
	ErrorInvalidTrial      ErrorCode = "invalid_trial_record"
	ErrorLimitExceeded     ErrorCode = "experiment_limit_exceeded"
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

type Support string

const (
	SupportUnknown       Support = "unknown"
	SupportSupported     Support = "supported"
	SupportUnsupported   Support = "unsupported"
	SupportNotApplicable Support = "not_applicable"
)

type Presence string

const (
	PresenceUnknown       Presence = "unknown"
	PresenceObserved      Presence = "observed"
	PresenceUnsupported   Presence = "unsupported"
	PresenceNotApplicable Presence = "not_applicable"
)

type SourceKind string

const (
	SourceNative      SourceKind = "native"
	SourceAgentSkills SourceKind = "agent_skills"
)

type Condition string

const (
	ConditionNone              Condition = "none"
	ConditionCurrent           Condition = "current"
	ConditionPrevious          Condition = "previous"
	ConditionForcedOracle      Condition = "forced_oracle"
	ConditionAutonomousOracle  Condition = "autonomous_oracle"
	ConditionOracleDistractors Condition = "oracle_with_distractors"
	ConditionRetrievedPresent  Condition = "retrieved_oracle_present"
	ConditionRetrievedAbsent   Condition = "retrieved_oracle_absent"
)

type ActivationChannel string

const (
	ChannelImplicit      ActivationChannel = "implicit"
	ChannelExplicitUser  ActivationChannel = "explicit_user"
	ChannelDeveloper     ActivationChannel = "developer"
	ChannelCombined      ActivationChannel = "combined"
	ChannelAdapterNative ActivationChannel = "adapter_native"
)

type SelectionAuthority string

const (
	SelectionNone      SelectionAuthority = "none"
	SelectionHarness   SelectionAuthority = "harness"
	SelectionAgent     SelectionAuthority = "agent"
	SelectionRetriever SelectionAuthority = "retriever"
)

type ControlClass string

const (
	ControlPositive              ControlClass = "positive"
	ControlNearMissNegative      ControlClass = "near_miss_negative"
	ControlIrrelevant            ControlClass = "irrelevant"
	ControlUnsupportedDomain     ControlClass = "unsupported_domain"
	ControlStaleVersionMismatch  ControlClass = "stale_version_mismatch"
	ControlAdversarialDistractor ControlClass = "adversarial_distractor"
)

type ControlProvenance string

const (
	ControlFromSource         ControlProvenance = "source_case"
	ControlSeparatelyAuthored ControlProvenance = "separately_authored"
	ControlLegacyProjection   ControlProvenance = "legacy_projection"
)

type TreatmentRole string

const (
	RoleReference TreatmentRole = "reference"
	RoleCandidate TreatmentRole = "candidate"
	RoleControl   TreatmentRole = "control"
)

type CapabilityID string

const (
	CapabilityConditionNone                CapabilityID = "condition.none"
	CapabilityConditionCurrent             CapabilityID = "condition.current"
	CapabilityConditionPrevious            CapabilityID = "condition.previous"
	CapabilityConditionForcedOracle        CapabilityID = "condition.forced_oracle"
	CapabilityConditionAutonomousOracle    CapabilityID = "condition.autonomous_oracle"
	CapabilityConditionOracleDistractors   CapabilityID = "condition.oracle_with_distractors"
	CapabilityConditionRetrievedPresent    CapabilityID = "condition.retrieved_oracle_present"
	CapabilityConditionRetrievedAbsent     CapabilityID = "condition.retrieved_oracle_absent"
	CapabilityChannelImplicit              CapabilityID = "channel.implicit"
	CapabilityChannelExplicitUser          CapabilityID = "channel.explicit_user"
	CapabilityChannelDeveloper             CapabilityID = "channel.developer"
	CapabilityChannelCombined              CapabilityID = "channel.combined"
	CapabilityChannelAdapterNative         CapabilityID = "channel.adapter_native"
	CapabilityControlPositive              CapabilityID = "control.positive"
	CapabilityControlNearMissNegative      CapabilityID = "control.near_miss_negative"
	CapabilityControlIrrelevant            CapabilityID = "control.irrelevant"
	CapabilityControlUnsupportedDomain     CapabilityID = "control.unsupported_domain"
	CapabilityControlStaleVersionMismatch  CapabilityID = "control.stale_version_mismatch"
	CapabilityControlAdversarialDistractor CapabilityID = "control.adversarial_distractor"
	CapabilityObserveCandidateRecall       CapabilityID = "observe.candidate_recall"
	CapabilityObserveSelection             CapabilityID = "observe.selection"
	CapabilityObserveLoad                  CapabilityID = "observe.load"
	CapabilityObserveInstructionAccess     CapabilityID = "observe.instruction_access"
	CapabilityObserveReferenceAccess       CapabilityID = "observe.reference_access"
	CapabilityObserveScriptAccess          CapabilityID = "observe.script_access"
	CapabilityObserveUsefulAdherence       CapabilityID = "observe.useful_adherence"
	CapabilityObserveVerifierOutcome       CapabilityID = "observe.verifier_outcome"
	CapabilityObserveOutcome               CapabilityID = "observe.outcome"
	CapabilityObserveInputTokens           CapabilityID = "observe.input_tokens"
	CapabilityObserveOutputTokens          CapabilityID = "observe.output_tokens"
	CapabilityObserveCost                  CapabilityID = "observe.estimated_cost_microusd"
	CapabilityObserveDuration              CapabilityID = "observe.duration_millis"
)

type FunnelStage string

const (
	StageCandidateRecall   FunnelStage = "candidate_recall"
	StageSelection         FunnelStage = "selection"
	StageLoad              FunnelStage = "load"
	StageInstructionAccess FunnelStage = "instruction_access"
	StageReferenceAccess   FunnelStage = "reference_access"
	StageScriptAccess      FunnelStage = "script_access"
	StageUsefulAdherence   FunnelStage = "useful_adherence"
	StageVerifierOutcome   FunnelStage = "verifier_outcome"
)

type MetricID string

const (
	MetricOutcome               MetricID = "outcome"
	MetricInputTokens           MetricID = "input_tokens"
	MetricOutputTokens          MetricID = "output_tokens"
	MetricEstimatedCostMicroUSD MetricID = "estimated_cost_microusd"
	MetricDurationMillis        MetricID = "duration_millis"
)

type MetricKind string

const (
	MetricBinary MetricKind = "binary"
	MetricCount  MetricKind = "count"
)

type MetricRole string

const (
	MetricPrimary      MetricRole = "primary"
	MetricConfirmatory MetricRole = "confirmatory"
	MetricExploratory  MetricRole = "exploratory"
)

type Direction string

const (
	DirectionHigher Direction = "higher_is_better"
	DirectionLower  Direction = "lower_is_better"
)

type OrderingKind string

const (
	OrderingWilliams    OrderingKind = "williams"
	OrderingLegacyFixed OrderingKind = "legacy_fixed"
)

type StoppingKind string

const (
	StoppingFixedRoster         StoppingKind = "fixed_roster"
	StoppingSafetyOrFixedRoster StoppingKind = "safety_or_fixed_roster"
)

type SafetyStopCode string

const (
	SafetyStopCriticalFinding    SafetyStopCode = "critical_finding"
	SafetyStopAuthorityViolation SafetyStopCode = "authority_violation"
	SafetyStopBudgetExhausted    SafetyStopCode = "budget_exhausted"
)

type Multiplicity string

const MultiplicityHolm Multiplicity = "holm"

type RepeatedAttemptKind string

const (
	RepeatedAttemptsNone RepeatedAttemptKind = "none"
	RepeatedAttemptsAll  RepeatedAttemptKind = "fixed_exchangeable_all_attempts"
)

type CompatibilityProfile string

const (
	CompatibilityNone                CompatibilityProfile = "none"
	CompatibilityPrivateActivationV1 CompatibilityProfile = "private_activation_v1"
	CompatibilityPrivateActivationV2 CompatibilityProfile = "private_activation_v2"
)

type LifecycleState string

const (
	LifecyclePlanned      LifecycleState = "planned"
	LifecycleCommitted    LifecycleState = "committed"
	LifecycleSpawning     LifecycleState = "spawning"
	LifecycleRunning      LifecycleState = "running"
	LifecycleSucceeded    LifecycleState = "succeeded"
	LifecycleFailed       LifecycleState = "failed"
	LifecycleCanceled     LifecycleState = "canceled"
	LifecycleTimedOut     LifecycleState = "timed_out"
	LifecycleUnknown      LifecycleState = "unknown"
	LifecycleUnsupported  LifecycleState = "unsupported"
	LifecyclePolicyDenied LifecycleState = "policy_denied"
)

type Eligibility string

const (
	EligibilitySupported   Eligibility = "supported"
	EligibilityUnsupported Eligibility = "unsupported"
	EligibilityIneligible  Eligibility = "ineligible"
	EligibilityDrifted     Eligibility = "drifted"
)

type ExclusionReason string

const (
	ExclusionNone                  ExclusionReason = "none"
	ExclusionMissingMember         ExclusionReason = "missing_member"
	ExclusionDuplicateMember       ExclusionReason = "duplicate_member"
	ExclusionLifecycleIncomplete   ExclusionReason = "lifecycle_incomplete"
	ExclusionLifecycleUnknown      ExclusionReason = "lifecycle_unknown"
	ExclusionUnsupportedCapability ExclusionReason = "unsupported_capability"
	ExclusionIneligible            ExclusionReason = "ineligible"
	ExclusionDrift                 ExclusionReason = "drift"
	ExclusionGradeIncomplete       ExclusionReason = "grade_incomplete"
	ExclusionCoverageMismatch      ExclusionReason = "coverage_mismatch"
)

type RuntimeBinding struct {
	AgentSHA256            string `json:"agent_sha256"`
	ModelSHA256            string `json:"model_sha256"`
	EnvironmentSHA256      string `json:"environment_sha256"`
	AdapterSHA256          string `json:"adapter_sha256"`
	ExecutionBackendSHA256 string `json:"execution_backend_sha256"`
	GraderSHA256           string `json:"grader_sha256"`
	HarnessSHA256          string `json:"harness_sha256"`
	BudgetsSHA256          string `json:"budgets_sha256"`
	AuthoritySHA256        string `json:"authority_sha256"`
}

type Capability struct {
	ID            CapabilityID `json:"id"`
	Support       Support      `json:"support"`
	BindingSHA256 string       `json:"binding_sha256"`
}

type CapabilityContract struct {
	Schema                   string         `json:"schema"`
	SchemaVersion            int            `json:"schema_version"`
	ContractVersion          string         `json:"contract_version"`
	Runtime                  RuntimeBinding `json:"runtime"`
	Capabilities             []Capability   `json:"capabilities"`
	CapabilityContractSHA256 string         `json:"capability_contract_sha256"`
}

type CaseBinding struct {
	SourceKind        SourceKind `json:"source_kind"`
	SourceSHA256      string     `json:"source_sha256"`
	CaseSHA256        string     `json:"case_sha256"`
	TaskSHA256        string     `json:"task_sha256"`
	FixtureSHA256     string     `json:"fixture_sha256"`
	GradingPlanSHA256 string     `json:"grading_plan_sha256"`
}

type ArmSelector struct {
	Condition          Condition          `json:"condition"`
	ActivationChannel  ActivationChannel  `json:"activation_channel"`
	SelectionAuthority SelectionAuthority `json:"selection_authority"`
	Control            ControlClass       `json:"control"`
}

type TreatmentRequest struct {
	Arm                    ArmSelector       `json:"arm"`
	Role                   TreatmentRole     `json:"role"`
	SkillSHA256            string            `json:"skill_sha256,omitempty"`
	SkillVersionSHA256     string            `json:"skill_version_sha256,omitempty"`
	DistractorSHA256       []string          `json:"distractor_sha256"`
	RetrieverSHA256        string            `json:"retriever_sha256,omitempty"`
	ControlSHA256          string            `json:"control_sha256"`
	ControlProvenance      ControlProvenance `json:"control_provenance"`
	ExecutionBindingSHA256 string            `json:"execution_binding_sha256"`
	ExpectedActivation     bool              `json:"expected_activation"`
}

type StratumRequest struct {
	BindingSHA256 string `json:"binding_sha256"`
	Blocks        uint32 `json:"blocks"`
}

type OrderingPolicy struct {
	Kind           OrderingKind  `json:"kind"`
	SeedSHA256     string        `json:"seed_sha256"`
	LegacySequence []ArmSelector `json:"legacy_sequence"`
}

type StoppingRule struct {
	Kind          StoppingKind     `json:"kind"`
	MaximumBlocks uint32           `json:"maximum_blocks"`
	SafetyStops   []SafetyStopCode `json:"safety_stops"`
}

type Design struct {
	Schema                   string               `json:"schema"`
	SchemaVersion            int                  `json:"schema_version"`
	ContractVersion          string               `json:"contract_version"`
	CompatibilityProfile     CompatibilityProfile `json:"compatibility_profile"`
	CapabilityContractSHA256 string               `json:"capability_contract_sha256"`
	AnalysisPlanSHA256       string               `json:"analysis_plan_sha256"`
	Case                     CaseBinding          `json:"case"`
	Treatments               []TreatmentRequest   `json:"treatments"`
	Strata                   []StratumRequest     `json:"strata"`
	Ordering                 OrderingPolicy       `json:"ordering"`
	Stopping                 StoppingRule         `json:"stopping"`
	DesignSHA256             string               `json:"design_sha256"`
}

type MetricDeclaration struct {
	ID           MetricID     `json:"id"`
	Kind         MetricKind   `json:"kind"`
	Role         MetricRole   `json:"role"`
	Direction    Direction    `json:"direction"`
	Capability   CapabilityID `json:"capability"`
	FamilySHA256 string       `json:"family_sha256"`
}

type StageDeclaration struct {
	Stage        FunnelStage  `json:"stage"`
	Role         MetricRole   `json:"role"`
	Capability   CapabilityID `json:"capability"`
	FamilySHA256 string       `json:"family_sha256"`
}

type Comparison struct {
	ID        string        `json:"id"`
	Reference ArmSelector   `json:"reference"`
	Candidate ArmSelector   `json:"candidate"`
	Stages    []FunnelStage `json:"stages"`
	Metrics   []MetricID    `json:"metrics"`
}

type RepeatedAttemptPolicy struct {
	Kind RepeatedAttemptKind `json:"kind"`
	K    []uint32            `json:"k"`
}

type AnalysisPlan struct {
	Schema                 string                `json:"schema"`
	SchemaVersion          int                   `json:"schema_version"`
	ContractVersion        string                `json:"contract_version"`
	ConfidenceBasisPoints  uint16                `json:"confidence_basis_points"`
	MinimumInferenceBlocks uint32                `json:"minimum_inference_blocks"`
	BootstrapSamples       uint32                `json:"bootstrap_samples"`
	BootstrapSeedSHA256    string                `json:"bootstrap_seed_sha256"`
	Multiplicity           Multiplicity          `json:"multiplicity"`
	RepeatedAttempts       RepeatedAttemptPolicy `json:"repeated_attempts"`
	Stages                 []StageDeclaration    `json:"stages"`
	Metrics                []MetricDeclaration   `json:"metrics"`
	Comparisons            []Comparison          `json:"comparisons"`
	AllowedExclusions      []ExclusionReason     `json:"allowed_exclusions"`
	AnalysisPlanSHA256     string                `json:"analysis_plan_sha256"`
}

type Treatment struct {
	ID                        string            `json:"id"`
	Arm                       ArmSelector       `json:"arm"`
	Role                      TreatmentRole     `json:"role"`
	SkillSHA256               string            `json:"skill_sha256,omitempty"`
	SkillVersionSHA256        string            `json:"skill_version_sha256,omitempty"`
	DistractorSHA256          []string          `json:"distractor_sha256"`
	RetrieverSHA256           string            `json:"retriever_sha256,omitempty"`
	ControlSHA256             string            `json:"control_sha256"`
	ControlProvenance         ControlProvenance `json:"control_provenance"`
	ExecutionBindingSHA256    string            `json:"execution_binding_sha256"`
	ExpectedActivation        bool              `json:"expected_activation"`
	AutonomousRoutingEligible bool              `json:"autonomous_routing_eligible"`
}

type Assignment struct {
	TrialID     string `json:"trial_id"`
	TreatmentID string `json:"treatment_id"`
	Position    uint32 `json:"position"`
}

type Block struct {
	ID          string       `json:"id"`
	Ordinal     uint32       `json:"ordinal"`
	StratumID   string       `json:"stratum_id"`
	Assignments []Assignment `json:"assignments"`
}

type PairBinding struct {
	ID                   string `json:"id"`
	BlockID              string `json:"block_id"`
	ComparisonID         string `json:"comparison_id"`
	ReferenceTreatmentID string `json:"reference_treatment_id"`
	CandidateTreatmentID string `json:"candidate_treatment_id"`
}

type Manifest struct {
	Schema                  string             `json:"schema"`
	SchemaVersion           int                `json:"schema_version"`
	ContractVersion         string             `json:"contract_version"`
	Design                  Design             `json:"design"`
	CapabilityContract      CapabilityContract `json:"capability_contract"`
	AnalysisPlan            AnalysisPlan       `json:"analysis_plan"`
	RequiredCapabilities    []CapabilityID     `json:"required_capabilities"`
	Treatments              []Treatment        `json:"treatments"`
	Blocks                  []Block            `json:"blocks"`
	Pairs                   []PairBinding      `json:"pairs"`
	PositionBalanceComplete bool               `json:"position_balance_complete"`
	ManifestSHA256          string             `json:"manifest_sha256"`
}

type StageObservation struct {
	Stage    FunnelStage `json:"stage"`
	Presence Presence    `json:"presence"`
	Value    *bool       `json:"value,omitempty"`
}

type MetricObservation struct {
	Metric   MetricID `json:"metric"`
	Presence Presence `json:"presence"`
	Value    *uint64  `json:"value,omitempty"`
}

type TrialRecord struct {
	Schema                 string              `json:"schema"`
	SchemaVersion          int                 `json:"schema_version"`
	ContractVersion        string              `json:"contract_version"`
	ManifestSHA256         string              `json:"manifest_sha256"`
	TrialID                string              `json:"trial_id"`
	BlockID                string              `json:"block_id"`
	TreatmentID            string              `json:"treatment_id"`
	AttemptPlanSHA256      string              `json:"attempt_plan_sha256"`
	LifecycleState         LifecycleState      `json:"lifecycle_state"`
	Eligibility            Eligibility         `json:"eligibility"`
	Exclusion              ExclusionReason     `json:"exclusion"`
	AgentObservationSHA256 string              `json:"agent_observation_sha256,omitempty"`
	GradeReceiptSHA256     string              `json:"grade_receipt_sha256,omitempty"`
	LifecycleEventSHA256   string              `json:"lifecycle_event_sha256,omitempty"`
	Stages                 []StageObservation  `json:"stages"`
	Metrics                []MetricObservation `json:"metrics"`
	RecordSHA256           string              `json:"record_sha256"`
}
