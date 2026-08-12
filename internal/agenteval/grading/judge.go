package grading

import (
	"context"
	"slices"
	"sort"
)

func AssessReviews(ctx context.Context, admitted AdmittedPlan, evidence *PreparedEvidence, reviews []Review,
	deterministic *DeterministicComparison) (Receipt, error) {
	if ctx == nil || admitted.plan.Mode != ModeJudgeAssessment || admitted.plan.Judge == nil {
		return Receipt{}, policyError("judge_mode")
	}
	if err := evidence.requireAlive(); err != nil {
		return Receipt{}, err
	}
	if len(reviews) != len(admitted.plan.Judge.Reviewers) {
		return Receipt{}, contractError("review_count")
	}
	owned := make([]Review, len(reviews))
	for index, review := range reviews {
		owned[index] = cloneReview(review)
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].ReviewerID < owned[j].ReviewerID })
	reviewers := make([]ReviewerReceipt, len(owned))
	votes := make(map[string][]bool, len(admitted.plan.Checks))
	allCitations := make(map[string]map[Citation]struct{}, len(admitted.plan.Checks))
	usage := Usage{}
	for index, review := range owned {
		if err := contextError(ctx); err != nil {
			return Receipt{}, err
		}
		policy := admitted.plan.Judge.Reviewers[index]
		if review.ReviewerID != policy.ID || validateReview(admitted.plan, review, evidence.digest) != nil ||
			!usageWithinReviewer(review.Usage, policy, admitted.plan.Limits.DeadlineMillis) {
			return Receipt{}, contractError("review_binding")
		}
		reviewSHA, err := ReviewSHA256(admitted.plan, review)
		if err != nil {
			return Receipt{}, err
		}
		reviewers[index] = ReviewerReceipt{ReviewerID: policy.ID, Kind: policy.Kind, Model: policy.Model,
			EnvironmentSHA256: policy.EnvironmentSHA256, ReviewSHA256: reviewSHA, Usage: review.Usage}
		usage = addUsage(usage, review.Usage)
		for decisionIndex, decision := range review.Decisions {
			check := admitted.plan.Checks[decisionIndex]
			if !citationsBelongToEvidence(evidence, decision.Citations, check.Visibility) ||
				!qualitativeCitationsAdmitted(check, decision.Citations) {
				return Receipt{}, evidenceError("review_citation")
			}
			votes[check.ID] = append(votes[check.ID], decision.Passed)
			if allCitations[check.ID] == nil {
				allCitations[check.ID] = map[Citation]struct{}{}
			}
			for _, citation := range decision.Citations {
				allCitations[check.ID][citation] = struct{}{}
			}
		}
	}
	decisions := make([]Decision, len(admitted.plan.Checks))
	disagreements := []Disagreement{}
	for index, check := range admitted.plan.Checks {
		passed := 0
		for _, vote := range votes[check.ID] {
			if vote {
				passed++
			}
		}
		citations := make([]Citation, 0, len(allCitations[check.ID]))
		for citation := range allCitations[check.ID] {
			citations = append(citations, citation)
		}
		sort.Slice(citations, func(i, j int) bool {
			if citations[i].EvidenceID != citations[j].EvidenceID {
				return citations[i].EvidenceID < citations[j].EvidenceID
			}
			return citations[i].SHA256 < citations[j].SHA256
		})
		decisions[index] = Decision{CheckID: check.ID, Presence: PresenceObserved, Passed: passed > len(owned)/2,
			Authority: AuthorityJudge, Citations: citations}
		if passed != 0 && passed != len(owned) {
			disagreements = append(disagreements, Disagreement{CheckID: check.ID, Kind: DisagreementReviewers})
		}
	}
	if deterministic != nil {
		if err := ValidateReceipt(deterministic.Plan, deterministic.Receipt); err != nil || deterministic.Receipt.EvidenceSHA256 != evidence.digest ||
			deterministic.Receipt.InputProjectionSHA256 != admitted.plan.InputProjectionSHA256 || deterministic.Plan.Mode != ModeDeterministic ||
			deterministic.Pairs == nil || len(deterministic.Pairs) == 0 || len(deterministic.Pairs) > len(admitted.plan.Checks) {
			return Receipt{}, contractError("deterministic_comparison")
		}
		byID := make(map[string]Decision, len(deterministic.Receipt.Decisions))
		for _, decision := range deterministic.Receipt.Decisions {
			byID[decision.CheckID] = decision
		}
		judgeByID := make(map[string]Decision, len(decisions))
		for _, decision := range decisions {
			judgeByID[decision.CheckID] = decision
		}
		for index, pair := range deterministic.Pairs {
			if !validIdentifier(pair.JudgeCheckID) || !validIdentifier(pair.DeterministicCheckID) || index > 0 &&
				(deterministic.Pairs[index-1].JudgeCheckID > pair.JudgeCheckID || deterministic.Pairs[index-1].JudgeCheckID == pair.JudgeCheckID &&
					deterministic.Pairs[index-1].DeterministicCheckID >= pair.DeterministicCheckID) {
				return Receipt{}, contractError("deterministic_comparison_pair")
			}
			judgeDecision, judgeOK := judgeByID[pair.JudgeCheckID]
			mechanical, mechanicalOK := byID[pair.DeterministicCheckID]
			if !judgeOK || !mechanicalOK {
				return Receipt{}, contractError("deterministic_comparison_pair")
			}
			if mechanical.Presence == PresenceObserved && mechanical.Passed != judgeDecision.Passed {
				disagreements = append(disagreements, Disagreement{CheckID: pair.JudgeCheckID, Kind: DisagreementDeterministicJudge})
			}
		}
	}
	sort.Slice(disagreements, func(i, j int) bool {
		if disagreements[i].CheckID != disagreements[j].CheckID {
			return disagreements[i].CheckID < disagreements[j].CheckID
		}
		return disagreements[i].Kind < disagreements[j].Kind
	})
	receipt := newReceipt(admitted, evidence, decisions, reviewers, usage, disagreements)
	if err := validateProducedReceipt(admitted, receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func qualitativeCitationsAdmitted(check Check, citations []Citation) bool {
	if check.Qualitative == nil {
		return false
	}
	for _, citation := range citations {
		if _, admitted := slices.BinarySearch(check.Qualitative.EvidenceIDs, citation.EvidenceID); !admitted {
			return false
		}
	}
	return true
}

func validateReview(plan Plan, review Review, evidenceSHA string) error {
	if plan.Judge == nil || !validIdentifier(review.ReviewerID) || review.RubricSHA256 != plan.Judge.RubricSHA256 ||
		review.PromptContractSHA256 != plan.Judge.PromptContractSHA256 ||
		review.BlindAssignmentSHA256 != plan.Judge.BlindAssignmentSHA256 || review.Decisions == nil ||
		len(review.Decisions) != len(plan.Checks) || !validUsage(review.Usage) || evidenceSHA != "" && review.EvidenceProjectionSHA256 != evidenceSHA ||
		evidenceSHA == "" && !validSHA256(review.EvidenceProjectionSHA256) {
		return contractError("review_shape")
	}
	for index, decision := range review.Decisions {
		if decision.CheckID != plan.Checks[index].ID || decision.Citations == nil || len(decision.Citations) == 0 ||
			len(decision.Citations) > MaxCitationsPerCheck || !citationsClosed(decision.Citations, plan.Checks[index].Visibility) {
			return contractError("review_decision")
		}
	}
	return nil
}

func citationsBelongToEvidence(evidence *PreparedEvidence, citations []Citation, visibility Visibility) bool {
	for _, citation := range citations {
		ref, ok := evidence.byID[citation.EvidenceID]
		if !ok || ref.kind != citation.Kind || ref.visibility != visibility || citation.Visibility != visibility || ref.digest != citation.SHA256 {
			return false
		}
	}
	return true
}
