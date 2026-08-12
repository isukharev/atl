package agenteval

import (
	"context"
	"io"

	"github.com/isukharev/atl/internal/agenteval/core"
	"github.com/isukharev/atl/internal/agenteval/executionbackend"
	"github.com/isukharev/atl/internal/agenteval/grading"
	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

const (
	GraderContractSchema    = grading.ContractSchema
	GradingPlanSchema       = grading.PlanSchema
	GradeReceiptSchema      = grading.ReceiptSchema
	GradingSchemaVersion    = grading.SchemaVersion
	GraderContractMaxBytes  = grading.MaxContractBytes
	GradingPlanMaxBytes     = grading.MaxPlanBytes
	GradeReceiptMaxBytes    = grading.MaxReceiptBytes
	GradingEvidenceMaxBytes = grading.MaxEvidenceBytes
)

type GraderContract = grading.Contract
type GradingPlan = grading.Plan
type GradeReceipt = grading.Receipt
type GradingEvidenceSet = grading.EvidenceSet
type PreparedGradingEvidence = grading.PreparedEvidence
type GradingReview = grading.Review
type GradingDeterministicComparison = grading.DeterministicComparison

func DecodeGraderContract(reader io.Reader) (GraderContract, error) {
	return grading.DecodeContract(reader)
}

func EncodeGraderContract(contract GraderContract) ([]byte, error) {
	return grading.EncodeContract(contract)
}

func DecodeGradingPlan(reader io.Reader) (GradingPlan, error) {
	return grading.DecodePlan(reader)
}

func EncodeGradingPlan(plan GradingPlan) ([]byte, error) {
	return grading.EncodePlan(plan)
}

func DecodeGradeReceipt(reader io.Reader, plan GradingPlan) (GradeReceipt, error) {
	return grading.DecodeReceipt(reader, plan)
}

func EncodeGradeReceipt(plan GradingPlan, receipt GradeReceipt) ([]byte, error) {
	return grading.EncodeReceipt(plan, receipt)
}

func BuiltinGraderContract() (GraderContract, error) { return grading.BuiltinContract() }

func GraderContractSHA256(contract GraderContract) (string, error) {
	return grading.ContractSHA256(contract)
}

func GradingPlanSHA256(plan GradingPlan) (string, error) { return grading.PlanSHA256(plan) }

func AdmitGradingPlan(contract GraderContract, plan GradingPlan) (grading.AdmittedPlan, error) {
	return grading.Admit(contract, plan)
}

func PrepareGradingEvidence(ctx context.Context, admitted grading.AdmittedPlan, evidence GradingEvidenceSet) (*PreparedGradingEvidence, error) {
	return grading.PrepareEvidence(ctx, admitted, evidence)
}

func EvaluateDeterministicGrading(ctx context.Context, admitted grading.AdmittedPlan, evidence *PreparedGradingEvidence) (GradeReceipt, error) {
	return grading.EvaluateDeterministic(ctx, admitted, evidence)
}

func EvaluateHermeticScriptGrading(ctx context.Context, admitted grading.AdmittedPlan, backend ExecutionBackendContract,
	evidence *PreparedGradingEvidence) (GradeReceipt, error) {
	return grading.EvaluateScript(ctx, admitted, backend, evidence)
}

func AssessOfflineGradingReviews(ctx context.Context, admitted grading.AdmittedPlan, evidence *PreparedGradingEvidence,
	reviews []GradingReview, comparison *GradingDeterministicComparison) (GradeReceipt, error) {
	return grading.AssessReviews(ctx, admitted, evidence, reviews, comparison)
}

func NewReceiptCoreGrader(task core.Task, plan GradingPlan, receipt GradeReceipt) (*grading.CoreGrader, error) {
	return grading.NewCoreGrader(task, plan, receipt)
}

func BindGradingPlan(binding lifecycle.Binding, plan GradingPlan) (lifecycle.Binding, error) {
	digest, err := grading.PlanSHA256(plan)
	if err != nil {
		return lifecycle.Binding{}, err
	}
	binding.Identity.GraderSHA256 = digest
	return binding, nil
}

func HermeticGradingBackendContract() (executionbackend.Contract, error) {
	return executionbackend.ReferenceContract()
}
