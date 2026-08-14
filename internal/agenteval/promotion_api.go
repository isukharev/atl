package agenteval

import (
	"io"

	"github.com/isukharev/atl/internal/agenteval/promotion"
)

// Promotion identities and receipts are provider-free, content-minimized
// projections. The root facade keeps command consumers from importing the
// implementation package directly.
const (
	PromotionSchema           = promotion.Schema
	PromotionComparisonSchema = promotion.ComparisonSchema
	PromotionRollbackSchema   = promotion.RollbackSchema
	PromotionSchemaVersion    = promotion.SchemaVersion
	PromotionContractVersion  = promotion.ContractVersion
)

type PromotionIdentity = promotion.Identity
type PromotionComponentReview = promotion.ComponentReview
type PromotionAxisResult = promotion.AxisResult
type PromotionComparisonInput = promotion.ComparisonInput
type PromotionDecisionReceipt = promotion.DecisionReceipt
type PromotionRollbackRequest = promotion.RollbackRequest
type PromotionRollbackReceipt = promotion.RollbackReceipt
type PromotionStore = promotion.Store

type PromotionComponent = promotion.Component
type PromotionAxis = promotion.Axis
type PromotionAxisState = promotion.AxisState
type PromotionDecision = promotion.Decision
type PromotionReason = promotion.Reason
type PromotionErrorCode = promotion.ErrorCode

const (
	PromotionComponentSkill             = promotion.ComponentSkill
	PromotionComponentEvaluation        = promotion.ComponentEvaluation
	PromotionComponentGrader            = promotion.ComponentGrader
	PromotionComponentHoldout           = promotion.ComponentHoldout
	PromotionAxisSafety                 = promotion.AxisSafety
	PromotionAxisCoverage               = promotion.AxisCoverage
	PromotionAxisRuntime                = promotion.AxisRuntime
	PromotionAxisQuality                = promotion.AxisQuality
	PromotionAxisNegativeLift           = promotion.AxisNegativeLift
	PromotionAxisResource               = promotion.AxisResource
	PromotionAxisPass                   = promotion.AxisPass
	PromotionAxisFail                   = promotion.AxisFail
	PromotionAxisUnknown                = promotion.AxisUnknown
	PromotionDecisionPromote            = promotion.DecisionPromote
	PromotionDecisionRefuse             = promotion.DecisionRefuse
	PromotionDecisionRollback           = promotion.DecisionRollback
	PromotionReasonUnreviewedSkill      = promotion.ReasonUnreviewedSkill
	PromotionReasonUnreviewedEvaluation = promotion.ReasonUnreviewedEvaluation
	PromotionReasonUnreviewedGrader     = promotion.ReasonUnreviewedGrader
	PromotionReasonUnreviewedHoldout    = promotion.ReasonUnreviewedHoldout
	PromotionReasonSafetyRegression     = promotion.ReasonSafetyRegression
	PromotionReasonCoverageMissing      = promotion.ReasonCoverageMissing
	PromotionReasonRuntimeIncompatible  = promotion.ReasonRuntimeIncompatible
	PromotionReasonQualityRegression    = promotion.ReasonQualityRegression
	PromotionReasonNegativeLift         = promotion.ReasonNegativeLift
	PromotionReasonResourceExhausted    = promotion.ReasonResourceExhausted
	PromotionReasonInterrupted          = promotion.ReasonInterrupted
	PromotionReasonIdentityMismatch     = promotion.ReasonIdentityMismatch
)

func EvaluatePromotion(input PromotionComparisonInput) (PromotionDecisionReceipt, error) {
	return promotion.Evaluate(input)
}

func EncodePromotionComparison(input PromotionComparisonInput) ([]byte, error) {
	return promotion.EncodeComparison(input)
}

func DecodePromotionComparison(reader io.Reader) (PromotionComparisonInput, error) {
	return promotion.DecodeComparison(reader)
}

func EncodePromotionDecision(receipt PromotionDecisionReceipt) ([]byte, error) {
	return promotion.EncodeDecision(receipt)
}

func DecodePromotionDecision(reader io.Reader) (PromotionDecisionReceipt, error) {
	return promotion.DecodeDecision(reader)
}

func ValidatePromotionDecision(receipt PromotionDecisionReceipt) error {
	return promotion.ValidateDecision(receipt)
}

func PlanPromotionRollback(request PromotionRollbackRequest) (PromotionRollbackReceipt, error) {
	return promotion.PlanRollback(request)
}

func EncodePromotionRollback(receipt PromotionRollbackReceipt) ([]byte, error) {
	return promotion.EncodeRollback(receipt)
}

func DecodePromotionRollback(reader io.Reader) (PromotionRollbackReceipt, error) {
	return promotion.DecodeRollback(reader)
}

func ValidatePromotionRollback(receipt PromotionRollbackReceipt) error {
	return promotion.ValidateRollback(receipt)
}

func NewPromotionStore(root string) (PromotionStore, error) {
	return promotion.NewStore(root)
}

func PromotionCodeOf(err error) (PromotionErrorCode, bool) {
	return promotion.CodeOf(err)
}
