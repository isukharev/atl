// Package promotion owns the provider-free promotion and exact rollback
// contract. It carries only immutable identities, bounded axis decisions, and
// content-minimized receipts; sealed evidence and source bytes never enter
// this package.
package promotion

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

const (
	Schema           = "agent-eval/promotion-decision"
	ComparisonSchema = "agent-eval/promotion-comparison"
	RollbackSchema   = "agent-eval/promotion-rollback"
	SchemaVersion    = 1
	ContractVersion  = "0.1.0-pre-release"
	MaxReceiptBytes  = 1 << 20
	MaxComponents    = 4
	MaxAxes          = 6
	MaxReasons       = 16
	SHA256HexLength  = 64
)

var ErrInvalid = errors.New("promotion_invalid")

type ErrorCode string

const (
	ErrorInvalidIdentity     ErrorCode = "invalid_promotion_identity"
	ErrorInvalidReview       ErrorCode = "invalid_promotion_review"
	ErrorInvalidAxis         ErrorCode = "invalid_promotion_axis"
	ErrorPromotionRefused    ErrorCode = "promotion_refused"
	ErrorInvalidReceipt      ErrorCode = "invalid_promotion_receipt"
	ErrorInvalidRollback     ErrorCode = "invalid_rollback_receipt"
	ErrorLimitExceeded       ErrorCode = "promotion_limit_exceeded"
	ErrorConflict            ErrorCode = "promotion_conflict"
	ErrorUnsupportedPlatform ErrorCode = "promotion_unsupported_platform"
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

type Component string

const (
	ComponentSkill      Component = "skill"
	ComponentEvaluation Component = "evaluation"
	ComponentGrader     Component = "grader"
	ComponentHoldout    Component = "holdout"
)

var components = [...]Component{ComponentSkill, ComponentEvaluation, ComponentGrader, ComponentHoldout}

func Components() []Component {
	result := make([]Component, len(components))
	copy(result, components[:])
	return result
}

type Axis string

const (
	AxisSafety       Axis = "safety"
	AxisCoverage     Axis = "coverage"
	AxisRuntime      Axis = "runtime"
	AxisQuality      Axis = "quality"
	AxisNegativeLift Axis = "negative_lift"
	AxisResource     Axis = "resource"
)

var axes = [...]Axis{AxisSafety, AxisCoverage, AxisRuntime, AxisQuality, AxisNegativeLift, AxisResource}

func Axes() []Axis {
	result := make([]Axis, len(axes))
	copy(result, axes[:])
	return result
}

type AxisState string

const (
	AxisPass    AxisState = "pass"
	AxisFail    AxisState = "fail"
	AxisUnknown AxisState = "unknown"
)

type Decision string

const (
	DecisionPromote  Decision = "promote"
	DecisionRefuse   Decision = "refuse"
	DecisionRollback Decision = "rollback"
)

type Reason string

const (
	ReasonUnreviewedSkill      Reason = "unreviewed_skill"
	ReasonUnreviewedEvaluation Reason = "unreviewed_evaluation"
	ReasonUnreviewedGrader     Reason = "unreviewed_grader"
	ReasonUnreviewedHoldout    Reason = "unreviewed_holdout"
	ReasonSafetyRegression     Reason = "safety_regression"
	ReasonCoverageMissing      Reason = "coverage_missing"
	ReasonRuntimeIncompatible  Reason = "runtime_incompatible"
	ReasonQualityRegression    Reason = "quality_regression"
	ReasonNegativeLift         Reason = "negative_lift"
	ReasonResourceExhausted    Reason = "resource_exhausted"
	ReasonInterrupted          Reason = "decision_interrupted"
	ReasonIdentityMismatch     Reason = "identity_mismatch"
)

// Identity is the complete immutable identity tuple used by promotion. All
// fields are opaque SHA-256 values; aliases such as "latest" are rejected.
type Identity struct {
	LineageSHA256    string `json:"lineage_sha256"`
	SkillSHA256      string `json:"skill_sha256"`
	EvaluationSHA256 string `json:"evaluation_sha256"`
	GraderSHA256     string `json:"grader_sha256"`
	HoldoutSHA256    string `json:"holdout_sha256"`
	RuntimeSHA256    string `json:"runtime_sha256"`
}

// ComponentReview is the content-minimized proof that one independently
// reviewed component diff was considered. ReviewSHA256 identifies the review
// record without retaining its content.
type ComponentReview struct {
	Component       Component `json:"component"`
	ReferenceSHA256 string    `json:"reference_sha256"`
	CandidateSHA256 string    `json:"candidate_sha256"`
	ReviewSHA256    string    `json:"review_sha256"`
	Reviewed        bool      `json:"reviewed"`
}

type AxisResult struct {
	Axis           Axis      `json:"axis"`
	State          AxisState `json:"state"`
	Blocking       bool      `json:"blocking"`
	Reason         Reason    `json:"reason,omitempty"`
	EvidenceSHA256 string    `json:"evidence_sha256,omitempty"`
}

// ComparisonInput is intentionally not an execution request. Callers supply
// already-computed, immutable identities and independent axis decisions.
type ComparisonInput struct {
	Schema          string            `json:"schema"`
	SchemaVersion   int               `json:"schema_version"`
	ContractVersion string            `json:"contract_version"`
	Reference       Identity          `json:"reference"`
	Candidate       Identity          `json:"candidate"`
	Reviews         []ComponentReview `json:"reviews"`
	Axes            []AxisResult      `json:"axes"`
	Interrupted     bool              `json:"interrupted"`
}

type DecisionReceipt struct {
	Schema          string            `json:"schema"`
	SchemaVersion   int               `json:"schema_version"`
	ContractVersion string            `json:"contract_version"`
	Decision        Decision          `json:"decision"`
	Reference       Identity          `json:"reference"`
	Candidate       Identity          `json:"candidate"`
	Reviews         []ComponentReview `json:"reviews"`
	Axes            []AxisResult      `json:"axes"`
	Reasons         []Reason          `json:"reasons"`
	Interrupted     bool              `json:"interrupted,omitempty"`
	ReceiptSHA256   string            `json:"receipt_sha256"`
}

// RollbackRequest names the exact current and prior immutable identities. A
// rollback never accepts an alias or discovers a prior state from a directory.
type RollbackRequest struct {
	Current             Identity `json:"current"`
	Restore             Identity `json:"restore"`
	AuthorizationSHA256 string   `json:"authorization_sha256"`
}

type RollbackReceipt struct {
	Schema              string   `json:"schema"`
	SchemaVersion       int      `json:"schema_version"`
	ContractVersion     string   `json:"contract_version"`
	Decision            Decision `json:"decision"`
	Current             Identity `json:"current"`
	Restore             Identity `json:"restore"`
	Restored            bool     `json:"restored"`
	RequestSHA256       string   `json:"request_sha256,omitempty"`
	AuthorizationSHA256 string   `json:"authorization_sha256"`
	ReceiptSHA256       string   `json:"receipt_sha256"`
}

func validDigest(value string) bool {
	if len(value) != SHA256HexLength {
		return false
	}
	if value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func identityValues(identity Identity) []string {
	return []string{identity.LineageSHA256, identity.SkillSHA256, identity.EvaluationSHA256,
		identity.GraderSHA256, identity.HoldoutSHA256, identity.RuntimeSHA256}
}

func validateIdentity(identity Identity) error {
	for _, value := range identityValues(identity) {
		if !validDigest(value) {
			return fail(ErrorInvalidIdentity)
		}
	}
	return nil
}

func identityEqual(left, right Identity) bool { return left == right }

func componentDigest(identity Identity, component Component) string {
	switch component {
	case ComponentSkill:
		return identity.SkillSHA256
	case ComponentEvaluation:
		return identity.EvaluationSHA256
	case ComponentGrader:
		return identity.GraderSHA256
	case ComponentHoldout:
		return identity.HoldoutSHA256
	default:
		return ""
	}
}

func componentOrdinal(component Component) int {
	for index, value := range components {
		if value == component {
			return index
		}
	}
	return -1
}

func axisOrdinal(axis Axis) int {
	for index, value := range axes {
		if value == axis {
			return index
		}
	}
	return -1
}

func reasonOrdinal(reason Reason) int {
	ordered := []Reason{ReasonUnreviewedSkill, ReasonUnreviewedEvaluation, ReasonUnreviewedGrader, ReasonUnreviewedHoldout,
		ReasonSafetyRegression, ReasonCoverageMissing, ReasonRuntimeIncompatible, ReasonQualityRegression,
		ReasonNegativeLift, ReasonResourceExhausted, ReasonInterrupted, ReasonIdentityMismatch}
	for index, value := range ordered {
		if value == reason {
			return index
		}
	}
	return -1
}

func sortReviews(reviews []ComponentReview) {
	sort.Slice(reviews, func(left, right int) bool {
		return componentOrdinal(reviews[left].Component) < componentOrdinal(reviews[right].Component)
	})
}

func sortAxes(values []AxisResult) {
	sort.Slice(values, func(left, right int) bool { return axisOrdinal(values[left].Axis) < axisOrdinal(values[right].Axis) })
}

func sortReasons(values []Reason) {
	sort.Slice(values, func(left, right int) bool { return reasonOrdinal(values[left]) < reasonOrdinal(values[right]) })
}
