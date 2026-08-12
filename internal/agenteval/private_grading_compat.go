package agenteval

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/agenteval/grading"
)

const (
	privateGradingPlanName   = "grading-plan.v1.json"
	privateGradeReceiptName  = "grade-receipt.v1.json"
	privateGradingEvidenceID = "candidate-output"
)

func privatePanelGradingPlan(contract privateQualitativeReviewPanelContract, rubric Rubric, surface PrivateBaselineSurfaceSource,
	resultData, finalData []byte,
) (grading.Contract, grading.Plan, error) {
	if len(contract.Executions) == 0 || !validSHA256(contract.BlindAssignmentSHA256) {
		return grading.Contract{}, grading.Plan{}, privatePlanError("grading_panel")
	}
	builtin, err := grading.BuiltinContract()
	if err != nil {
		return grading.Contract{}, grading.Plan{}, privatePlanError("grading_contract")
	}
	contractSHA, err := grading.ContractSHA256(builtin)
	if err != nil {
		return grading.Contract{}, grading.Plan{}, privatePlanError("grading_contract")
	}
	checks := make([]grading.Check, len(rubric.Criteria))
	for index, criterion := range rubric.Criteria {
		checks[index] = grading.Check{ID: criterion.ID, Kind: grading.CheckQualitative, Visibility: grading.VisibilityHidden,
			Qualitative: &grading.QualitativeRule{RubricCriterionID: criterion.ID, EvidenceIDs: []string{privateGradingEvidenceID}}}
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	inputSHA, err := contentMinimizedAttemptDigest("private-panel-grading-input", []string{
		sha256HexBytes(resultData), sha256HexBytes(finalData), rubricSHA256(rubric), surface.QualitativePanelContractSHA256,
	})
	if err != nil {
		return grading.Contract{}, grading.Plan{}, err
	}
	promptContractSHA, err := contentMinimizedAttemptDigest("private-panel-review-prompt-contract",
		[]string{rubricSHA256(rubric), inputSHA, surface.QualitativePanelContractSHA256})
	if err != nil {
		return grading.Contract{}, grading.Plan{}, err
	}
	policy, err := privatePanelGradingPolicy(contract, rubricSHA256(rubric), promptContractSHA)
	if err != nil {
		return grading.Contract{}, grading.Plan{}, err
	}
	environmentSHA, err := contentMinimizedAttemptDigest("private-panel-grading-environment",
		[]string{surface.QualitativePanelContractSHA256, surface.ExecutionReceiptSHA256})
	if err != nil {
		return grading.Contract{}, grading.Plan{}, err
	}
	plan := grading.Plan{Schema: grading.PlanSchema, SchemaVersion: grading.SchemaVersion, ContractVersion: grading.ContractVersion,
		ContractSHA256: contractSHA, Mode: grading.ModeJudgeAssessment, InputProjectionSHA256: inputSHA,
		EnvironmentSHA256: environmentSHA, Checks: checks, Judge: &policy,
		Limits: grading.PlanLimits{DeadlineMillis: grading.MaxDurationMillis, MaxInputBytes: grading.MaxEvidenceBytes,
			MaxOutputBytes: grading.MaxReceiptBytes}}
	if _, err := grading.Admit(builtin, plan); err != nil {
		return grading.Contract{}, grading.Plan{}, privatePlanError("grading_plan")
	}
	return builtin, plan, nil
}

func privatePanelGradingPolicy(contract privateQualitativeReviewPanelContract, rubricSHA, promptContractSHA string) (grading.JudgePolicy, error) {
	executions := make(map[string]PrivateReviewerExecution, len(contract.Executions))
	for _, execution := range contract.Executions {
		executions[execution.ReviewerID] = execution
	}
	reviewers := make([]grading.Reviewer, len(contract.Reviewers))
	for index, reviewer := range contract.Reviewers {
		execution, ok := executions[reviewer.ID]
		if !ok || reviewer.Kind != "codex" && reviewer.Kind != "claude-code" || execution.MaxEstimatedCostMicroUSD < 1 ||
			execution.MaxEstimatedCostMicroUSD > grading.MaxCostMicroUSD {
			return grading.JudgePolicy{}, privatePlanError("grading_reviewer")
		}
		environment, err := privateReviewerExecutionSHA256(execution)
		if err != nil {
			return grading.JudgePolicy{}, err
		}
		// #nosec G115 -- the signed workspace value was proven positive and within the generic bound above.
		maximumCost := uint64(execution.MaxEstimatedCostMicroUSD)
		reviewers[index] = grading.Reviewer{ID: reviewer.ID, Kind: grading.ReviewerModel, Model: reviewer.Kind + "/" + reviewer.Model,
			EnvironmentSHA256: environment, MaxInputTokens: grading.MaxTokens, MaxOutputTokens: grading.MaxTokens,
			MaxEstimatedCostMicroUSD: maximumCost}
	}
	sort.Slice(reviewers, func(i, j int) bool { return reviewers[i].ID < reviewers[j].ID })
	return grading.JudgePolicy{RubricSHA256: rubricSHA, PromptContractSHA256: promptContractSHA,
		BlindAssignmentSHA256: contract.BlindAssignmentSHA256,
		ToolPolicy:            "none", Reviewers: reviewers}, nil
}

func assessPrivatePanelWithGrading(root string, source PrivateBaselineSource, surface PrivateBaselineSurfaceSource,
	contract privateQualitativeReviewPanelContract, rubric Rubric, resultData, finalData []byte, reviews []Review,
) (grading.Plan, grading.Receipt, error) {
	builtin, plan, err := privatePanelGradingPlan(contract, rubric, surface, resultData, finalData)
	if err != nil {
		return grading.Plan{}, grading.Receipt{}, err
	}
	admitted, err := grading.Admit(builtin, plan)
	if err != nil {
		return grading.Plan{}, grading.Receipt{}, privatePlanError("grading_plan")
	}
	prepared, err := grading.PrepareEvidence(context.Background(), admitted, grading.EvidenceSet{
		InputProjectionSHA256: plan.InputProjectionSHA256,
		Files: []grading.FileEvidence{{ID: privateGradingEvidenceID, Visibility: grading.VisibilityHidden,
			Present: true, Mode: 0o600, Data: finalData}},
		Commands: []grading.CommandEvidence{}, Trees: []grading.TreeEvidence{}, Sequences: []grading.SequenceEvidence{},
		Counters: []grading.CounterEvidence{},
	})
	if err != nil {
		return grading.Plan{}, grading.Receipt{}, privatePlanError("grading_evidence")
	}
	defer prepared.Destroy()
	citations := prepared.Citations()
	if len(citations) != 1 {
		return grading.Plan{}, grading.Receipt{}, privatePlanError("grading_evidence")
	}
	rubricByID := make(map[string]RubricCriterion, len(rubric.Criteria))
	for _, criterion := range rubric.Criteria {
		rubricByID[criterion.ID] = criterion
	}
	legacyByID := make(map[string]Review, len(reviews))
	for _, review := range reviews {
		legacyByID[review.Reviewer.ID] = review
	}
	genericReviews := make([]grading.Review, len(plan.Judge.Reviewers))
	for index, reviewer := range plan.Judge.Reviewers {
		legacy, ok := legacyByID[reviewer.ID]
		if !ok {
			return grading.Plan{}, grading.Receipt{}, privatePlanError("grading_review")
		}
		scores := make(map[string]int, len(legacy.Criteria))
		for _, score := range legacy.Criteria {
			scores[score.ID] = score.Score
		}
		decisions := make([]grading.ReviewDecision, len(plan.Checks))
		for checkIndex, check := range plan.Checks {
			criterion, criterionOK := rubricByID[check.ID]
			score, scoreOK := scores[check.ID]
			if !criterionOK || !scoreOK {
				return grading.Plan{}, grading.Receipt{}, privatePlanError("grading_review")
			}
			decisions[checkIndex] = grading.ReviewDecision{CheckID: check.ID, Passed: score >= criterion.Minimum,
				Citations: slices.Clone(citations)}
		}
		usage, usageErr := privatePanelGradingUsage(root, source, surface, legacy, reviewer)
		if usageErr != nil {
			return grading.Plan{}, grading.Receipt{}, usageErr
		}
		genericReviews[index] = grading.Review{ReviewerID: reviewer.ID, RubricSHA256: plan.Judge.RubricSHA256,
			PromptContractSHA256:  plan.Judge.PromptContractSHA256,
			BlindAssignmentSHA256: plan.Judge.BlindAssignmentSHA256, EvidenceProjectionSHA256: prepared.SHA256(),
			Decisions: decisions, Usage: usage}
	}
	receipt, err := grading.AssessReviews(context.Background(), admitted, prepared, genericReviews, nil)
	if err != nil {
		return grading.Plan{}, grading.Receipt{}, privatePlanError("grading_assessment")
	}
	return plan, receipt, nil
}

func privatePanelGradingUsage(root string, source PrivateBaselineSource, surface PrivateBaselineSurfaceSource,
	review Review, reviewer grading.Reviewer,
) (grading.Usage, error) {
	packet := filepath.Join(root, filepath.FromSlash(privatePanelPacketRelative(source.RunID, privateReviewCellKey(surface), review.Reviewer.ID)))
	attemptData, err := readPrivatePlanLifecycleFile(root, filepath.Join(packet, "execution-attempt.json"), maxReviewBytes)
	if err != nil {
		return grading.Usage{}, privatePlanError("grading_usage")
	}
	receiptData, err := readPrivatePlanLifecycleFile(root, filepath.Join(packet, "execution-receipt.json"), maxReviewBytes)
	if err != nil {
		return grading.Usage{}, privatePlanError("grading_usage")
	}
	var attempt privateReviewAttempt
	var receipt privateReviewReceipt
	if decodePrivateLifecycleJSON(attemptData, &attempt) != nil || decodePrivateLifecycleJSON(receiptData, &receipt) != nil ||
		receipt.Status != "succeeded" || receipt.ReviewerID != reviewer.ID || receipt.InputTokens < 1 || receipt.OutputTokens < 1 ||
		receipt.InputTokens > grading.MaxTokens || receipt.OutputTokens > grading.MaxTokens || !receipt.CostKnown ||
		receipt.EstimatedCostMicroUSD < 1 || uint64(receipt.EstimatedCostMicroUSD) > reviewer.MaxEstimatedCostMicroUSD {
		return grading.Usage{}, privatePlanError("grading_usage")
	}
	started, startErr := time.Parse(time.RFC3339Nano, attempt.StartedAt)
	completed, completeErr := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	duration := completed.Sub(started)
	if startErr != nil || completeErr != nil || duration < 0 || duration > time.Duration(grading.MaxDurationMillis)*time.Millisecond {
		return grading.Usage{}, privatePlanError("grading_usage")
	}
	observed := func(value uint64) grading.MetricPresence {
		return grading.MetricPresence{Presence: grading.PresenceObserved, Value: value}
	}
	return grading.Usage{InputTokens: observed(uint64(receipt.InputTokens)), OutputTokens: observed(uint64(receipt.OutputTokens)),
		EstimatedCostMicroUSD: observed(uint64(receipt.EstimatedCostMicroUSD)), DurationMillis: observed(uint64(duration / time.Millisecond))}, nil
}

func writePrivatePanelGradingArtifacts(root, runDirectory string, plan grading.Plan, receipt grading.Receipt) error {
	planData, err := grading.EncodePlan(plan)
	if err != nil {
		return privatePlanError("grading_plan")
	}
	receiptData, err := grading.EncodeReceipt(plan, receipt)
	if err != nil {
		return privatePlanError("grading_receipt")
	}
	artifacts := []struct {
		name string
		data []byte
	}{
		{name: privateGradingPlanName, data: planData},
		{name: privateGradeReceiptName, data: receiptData},
	}
	for _, artifact := range artifacts {
		path := filepath.Join(runDirectory, artifact.name)
		if existing, readErr := hardenedReadFileWithinLimit(root, path, grading.MaxReceiptBytes); readErr == nil {
			if !bytes.Equal(existing, artifact.data) {
				return privatePlanError("grading_drift")
			}
			continue
		} else if !os.IsNotExist(readErr) {
			return privatePlanError("grading_write")
		}
		if err := hardenedWriteFileExclusiveWithin(root, path, artifact.data, 0o600); err != nil {
			return privatePlanError("grading_write")
		}
	}
	return nil
}

func validatePrivatePanelGradingContract(panel privateQualitativeReviewPanelContract) error {
	if len(panel.Executions) == 0 {
		return nil
	}
	if !validSHA256(panel.BlindAssignmentSHA256) {
		return privatePlanError("grading_blind_assignment")
	}
	if _, err := privatePanelGradingPolicy(panel, strings.Repeat("0", 64), strings.Repeat("0", 64)); err != nil {
		return err
	}
	return nil
}

func validatePrivatePlanGradingPanel(panel privateQualitativeReviewPanelContract) error {
	workspacePanel := PrivateQualitativeReviewPanel{Method: panel.Method, Reviewers: panel.Reviewers,
		MaxCriterionRangeBPS: panel.MaxCriterionRangeBPS, Executions: panel.Executions}
	if panel.BlindAssignmentSHA256 != "" {
		workspacePanel.BlindAssignment = "cases/grading/blind-assignment.bin"
	}
	if workspacePanel.validate() != nil {
		return privatePlanError("qualitative_panel")
	}
	return validatePrivatePanelGradingContract(panel)
}
