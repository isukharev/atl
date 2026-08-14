// Package evolution owns the provider-free, review-only proposal contract.
// It accepts only content-minimized failure classes and opaque identities. It
// never reads source bytes, sealed holdouts, expected answers, credentials, or
// provider/backend state, and it exposes no apply or promotion operation.
package evolution

import (
	"errors"
	"sort"
	"strings"
)

const (
	Schema           = "agent-eval/evolution-proposal"
	SchemaVersion    = 1
	ContractVersion  = "0.1.0-pre-release"
	MaxProposalBytes = 1 << 20
	MaxFailures      = 64
	MaxEvidenceRefs  = 64
	MaxJSONDepth     = 32
	SHA256HexLength  = 64
)

var ErrInvalid = errors.New("evolution_proposal_invalid")

type ErrorCode string

const (
	ErrorInvalidInput    ErrorCode = "invalid_evolution_input"
	ErrorInvalidProposal ErrorCode = "invalid_evolution_proposal"
	ErrorLimitExceeded   ErrorCode = "evolution_proposal_limit_exceeded"
	ErrorConflict        ErrorCode = "evolution_proposal_conflict"
	ErrorOutcomeUnknown  ErrorCode = "evolution_proposal_outcome_unknown"
)

type Error struct{ code ErrorCode }

func (e *Error) Error() string   { return string(e.code) }
func (e *Error) Unwrap() error   { return ErrInvalid }
func (e *Error) Code() ErrorCode { return e.code }

func fail(code ErrorCode) error { return &Error{code: code} }

func CodeOf(err error) (ErrorCode, bool) {
	var target *Error
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Code(), true
}

type FailureClass string

const (
	FailureSafety    FailureClass = "safety"
	FailureCoverage  FailureClass = "coverage"
	FailureRuntime   FailureClass = "runtime"
	FailureQuality   FailureClass = "quality"
	FailureResource  FailureClass = "resource"
	FailureLifecycle FailureClass = "lifecycle"
	FailureVerifier  FailureClass = "verifier"
)

var failureClasses = [...]FailureClass{
	FailureSafety, FailureCoverage, FailureRuntime, FailureQuality,
	FailureResource, FailureLifecycle, FailureVerifier,
}

type SkillAction string

const (
	SkillReinforceSafety   SkillAction = "reinforce_safety_boundary"
	SkillClarifyCoverage   SkillAction = "clarify_coverage_boundary"
	SkillDocumentRuntime   SkillAction = "document_runtime_boundary"
	SkillClarifyQuality    SkillAction = "clarify_expected_behavior"
	SkillStateResource     SkillAction = "state_resource_boundary"
	SkillPreserveLifecycle SkillAction = "preserve_no_replay_lifecycle"
	SkillPreserveVerifier  SkillAction = "preserve_independent_verifier"
)

type EvaluationAction string

const (
	EvaluationSafety    EvaluationAction = "add_safety_assertion"
	EvaluationCoverage  EvaluationAction = "add_coverage_assertion"
	EvaluationRuntime   EvaluationAction = "add_runtime_assertion"
	EvaluationQuality   EvaluationAction = "add_quality_assertion"
	EvaluationResource  EvaluationAction = "add_resource_assertion"
	EvaluationLifecycle EvaluationAction = "add_lifecycle_assertion"
	EvaluationVerifier  EvaluationAction = "add_verifier_assertion"
)

// FailureSummary is the only input accepted from a failure collector. It
// carries a closed class, a count, and an opaque evidence digest; no failure
// text, prompt, expected answer, path, or source bytes can enter the contract.
type FailureSummary struct {
	Class          FailureClass `json:"class"`
	Count          uint32       `json:"count"`
	EvidenceSHA256 []string     `json:"evidence_sha256"`
}

// Request is an in-memory proposal request. The generator consumes a cloned
// request and never retains it after returning a proposal.
type Request struct {
	LineageSHA256    string
	SkillSHA256      string
	EvaluationSHA256 string
	SelfFeedbackOnly bool
	Failures         []FailureSummary
}

type ProposalChange struct {
	Class          FailureClass `json:"class"`
	Action         string       `json:"action"`
	EvidenceSHA256 []string     `json:"evidence_sha256"`
}

// Proposal is a review-only, content-minimized candidate. SkillChanges and
// EvaluationChanges are deliberately separate arrays. There is no method in
// this package that applies either array to a skill, eval, grader, policy, or
// holdout.
type Proposal struct {
	Schema               string           `json:"schema"`
	SchemaVersion        int              `json:"schema_version"`
	ContractVersion      string           `json:"contract_version"`
	LineageSHA256        string           `json:"lineage_sha256"`
	BaseSkillSHA256      string           `json:"base_skill_sha256"`
	BaseEvaluationSHA256 string           `json:"base_evaluation_sha256"`
	InputSHA256          string           `json:"input_sha256"`
	SelfFeedbackOnly     bool             `json:"self_feedback_only"`
	Exploratory          bool             `json:"exploratory"`
	ReusableImprovement  bool             `json:"reusable_improvement"`
	Failures             []FailureSummary `json:"failures"`
	SkillChanges         []ProposalChange `json:"skill_changes"`
	EvaluationChanges    []ProposalChange `json:"evaluation_changes"`
	ProposalSHA256       string           `json:"proposal_sha256"`
}

func failureOrdinal(value FailureClass) int {
	for index, candidate := range failureClasses {
		if candidate == value {
			return index
		}
	}
	return -1
}

func validDigest(value string) bool {
	if len(value) != SHA256HexLength || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func cloneFailures(values []FailureSummary) []FailureSummary {
	result := make([]FailureSummary, len(values))
	for index, value := range values {
		result[index] = value
		result[index].EvidenceSHA256 = append([]string(nil), value.EvidenceSHA256...)
		sort.Strings(result[index].EvidenceSHA256)
	}
	sort.Slice(result, func(left, right int) bool {
		leftOrdinal, rightOrdinal := failureOrdinal(result[left].Class), failureOrdinal(result[right].Class)
		if leftOrdinal != rightOrdinal {
			return leftOrdinal < rightOrdinal
		}
		return strings.Join(result[left].EvidenceSHA256, "\x00") < strings.Join(result[right].EvidenceSHA256, "\x00")
	})
	return result
}

func cloneChanges(values []ProposalChange) []ProposalChange {
	result := make([]ProposalChange, len(values))
	for index, value := range values {
		result[index] = value
		result[index].EvidenceSHA256 = append([]string(nil), value.EvidenceSHA256...)
		sort.Strings(result[index].EvidenceSHA256)
	}
	sort.Slice(result, func(left, right int) bool {
		leftOrdinal, rightOrdinal := failureOrdinal(result[left].Class), failureOrdinal(result[right].Class)
		if leftOrdinal != rightOrdinal {
			return leftOrdinal < rightOrdinal
		}
		return result[left].Action < result[right].Action
	})
	return result
}

func cloneProposal(value Proposal) Proposal {
	value.Failures = cloneFailures(value.Failures)
	value.SkillChanges = cloneChanges(value.SkillChanges)
	value.EvaluationChanges = cloneChanges(value.EvaluationChanges)
	return value
}

func skillAction(class FailureClass) SkillAction {
	switch class {
	case FailureSafety:
		return SkillReinforceSafety
	case FailureCoverage:
		return SkillClarifyCoverage
	case FailureRuntime:
		return SkillDocumentRuntime
	case FailureQuality:
		return SkillClarifyQuality
	case FailureResource:
		return SkillStateResource
	case FailureLifecycle:
		return SkillPreserveLifecycle
	case FailureVerifier:
		return SkillPreserveVerifier
	default:
		return ""
	}
}

func evaluationAction(class FailureClass) EvaluationAction {
	switch class {
	case FailureSafety:
		return EvaluationSafety
	case FailureCoverage:
		return EvaluationCoverage
	case FailureRuntime:
		return EvaluationRuntime
	case FailureQuality:
		return EvaluationQuality
	case FailureResource:
		return EvaluationResource
	case FailureLifecycle:
		return EvaluationLifecycle
	case FailureVerifier:
		return EvaluationVerifier
	default:
		return ""
	}
}

func validSkillAction(value string, class FailureClass) bool {
	return value == string(skillAction(class))
}

func validEvaluationAction(value string, class FailureClass) bool {
	return value == string(evaluationAction(class))
}
