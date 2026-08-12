package grading

import "slices"

func validateProducedReceipt(admitted AdmittedPlan, receipt Receipt) error {
	for _, decision := range receipt.Decisions {
		if len(decision.Citations) > int(admitted.contract.Limits.MaxCitationsPerCheck) {
			return policyError("receipt_citations")
		}
	}
	data, err := EncodeReceipt(admitted.plan, receipt)
	if err != nil {
		return err
	}
	if uint64(len(data)) > admitted.plan.Limits.MaxOutputBytes {
		return policyError("receipt_bytes")
	}
	return nil
}

func ValidateReceipt(plan Plan, receipt Receipt) error {
	if err := ValidatePlan(plan); err != nil {
		return err
	}
	planSHA, err := PlanSHA256(plan)
	if err != nil || receipt.Schema != ReceiptSchema || receipt.SchemaVersion != SchemaVersion || receipt.ContractVersion != ContractVersion ||
		receipt.ContractSHA256 != plan.ContractSHA256 || receipt.PlanSHA256 != planSHA ||
		receipt.InputProjectionSHA256 != plan.InputProjectionSHA256 || !validSHA256(receipt.EvidenceSHA256) || receipt.Evidence == nil ||
		len(receipt.Evidence) > MaxEvidenceItems || !citationsClosedAnyVisibility(receipt.Evidence) ||
		receipt.EvidenceSHA256 != evidenceCatalogSHA256(receipt.InputProjectionSHA256, receipt.Evidence) || !receipt.Status.valid() ||
		receipt.Decisions == nil || len(receipt.Decisions) != len(plan.Checks) || receipt.Reviewers == nil ||
		receipt.Disagreements == nil || !validUsage(receipt.Usage) {
		return contractError("receipt_shape")
	}
	wantIncomplete := false
	for index, decision := range receipt.Decisions {
		check := plan.Checks[index]
		if decision.CheckID != check.ID || !decision.Presence.valid() || !decision.Authority.valid() || decision.Citations == nil ||
			len(decision.Citations) > MaxCitationsPerCheck || decision.Presence != PresenceObserved && (decision.Passed || len(decision.Citations) != 0) ||
			decision.Presence == PresenceObserved && len(decision.Citations) == 0 || !citationsClosed(decision.Citations, check.Visibility) ||
			!citationsInCatalog(decision.Citations, receipt.Evidence) {
			return contractError("receipt_decision")
		}
		wantAuthority := AuthorityDeterministic
		switch plan.Mode {
		case ModeScriptDSL:
			wantAuthority = AuthorityScript
		case ModeJudgeAssessment:
			wantAuthority = AuthorityJudge
		}
		if decision.Authority != wantAuthority {
			return contractError("receipt_authority")
		}
		wantIncomplete = wantIncomplete || decision.Presence != PresenceObserved
	}
	if wantIncomplete != (receipt.Status == ReceiptIncomplete) {
		return contractError("receipt_status")
	}
	if plan.Mode != ModeJudgeAssessment {
		if len(receipt.Reviewers) != 0 || len(receipt.Disagreements) != 0 || receipt.Usage != notApplicableUsage() {
			return contractError("mechanical_receipt")
		}
	} else if err := validateJudgeReceipt(plan, receipt); err != nil {
		return err
	}
	for index, disagreement := range receipt.Disagreements {
		if !validIdentifier(disagreement.CheckID) || !disagreement.Kind.valid() || index > 0 &&
			(receipt.Disagreements[index-1].CheckID > disagreement.CheckID || receipt.Disagreements[index-1].CheckID == disagreement.CheckID &&
				receipt.Disagreements[index-1].Kind >= disagreement.Kind) ||
			!slices.ContainsFunc(plan.Checks, func(check Check) bool { return check.ID == disagreement.CheckID }) {
			return contractError("receipt_disagreement")
		}
	}
	return nil
}

func citationsClosed(citations []Citation, visibility Visibility) bool {
	for index, citation := range citations {
		if !validIdentifier(citation.EvidenceID) || !citation.Kind.valid() || citation.Visibility != visibility || !validSHA256(citation.SHA256) ||
			index > 0 && (citations[index-1].EvidenceID > citation.EvidenceID || citations[index-1].EvidenceID == citation.EvidenceID &&
				citations[index-1].SHA256 >= citation.SHA256) {
			return false
		}
	}
	return true
}

func citationsClosedAnyVisibility(citations []Citation) bool {
	for index, citation := range citations {
		if !validIdentifier(citation.EvidenceID) || !citation.Kind.valid() || !citation.Visibility.valid() || !validSHA256(citation.SHA256) ||
			index > 0 && citations[index-1].EvidenceID >= citation.EvidenceID {
			return false
		}
	}
	return true
}

func citationsInCatalog(citations, catalog []Citation) bool {
	for _, citation := range citations {
		index, found := slices.BinarySearchFunc(catalog, citation.EvidenceID, func(item Citation, id string) int {
			if item.EvidenceID < id {
				return -1
			}
			if item.EvidenceID > id {
				return 1
			}
			return 0
		})
		if !found || catalog[index] != citation {
			return false
		}
	}
	return true
}

func validMetric(metric MetricPresence, maximum uint64) bool {
	if !metric.Presence.valid() {
		return false
	}
	if metric.Presence == PresenceObserved {
		return metric.Value <= maximum
	}
	return metric.Value == 0
}

func validUsage(usage Usage) bool {
	return validMetric(usage.InputTokens, MaxAggregateTokens) && validMetric(usage.OutputTokens, MaxAggregateTokens) &&
		validMetric(usage.EstimatedCostMicroUSD, MaxAggregateCost) && validMetric(usage.DurationMillis, MaxAggregateDuration)
}

func validateJudgeReceipt(plan Plan, receipt Receipt) error {
	if plan.Judge == nil || len(receipt.Reviewers) != len(plan.Judge.Reviewers) {
		return contractError("judge_receipt")
	}
	var usage Usage
	for index, reviewer := range receipt.Reviewers {
		policy := plan.Judge.Reviewers[index]
		if reviewer.ReviewerID != policy.ID || reviewer.Kind != policy.Kind || reviewer.Model != policy.Model ||
			reviewer.EnvironmentSHA256 != policy.EnvironmentSHA256 || !validSHA256(reviewer.ReviewSHA256) || !validUsage(reviewer.Usage) ||
			!usageWithinReviewer(reviewer.Usage, policy, plan.Limits.DeadlineMillis) {
			return contractError("judge_reviewer_receipt")
		}
		usage = addUsage(usage, reviewer.Usage)
	}
	if usage != receipt.Usage {
		return contractError("judge_usage")
	}
	return nil
}

func usageWithinReviewer(usage Usage, reviewer Reviewer, deadlineMillis uint64) bool {
	if reviewer.Kind == ReviewerHuman {
		return usage == notApplicableUsage()
	}
	return usage.InputTokens.Presence == PresenceObserved && usage.InputTokens.Value <= reviewer.MaxInputTokens &&
		usage.OutputTokens.Presence == PresenceObserved && usage.OutputTokens.Value <= reviewer.MaxOutputTokens &&
		usage.EstimatedCostMicroUSD.Presence == PresenceObserved && usage.EstimatedCostMicroUSD.Value <= reviewer.MaxEstimatedCostMicroUSD &&
		usage.DurationMillis.Presence == PresenceObserved && usage.DurationMillis.Value <= deadlineMillis
}

func addUsage(left, right Usage) Usage {
	return Usage{InputTokens: addMetric(left.InputTokens, right.InputTokens, MaxAggregateTokens),
		OutputTokens:          addMetric(left.OutputTokens, right.OutputTokens, MaxAggregateTokens),
		EstimatedCostMicroUSD: addMetric(left.EstimatedCostMicroUSD, right.EstimatedCostMicroUSD, MaxAggregateCost),
		DurationMillis:        addMetric(left.DurationMillis, right.DurationMillis, MaxAggregateDuration)}
}

func addMetric(left, right MetricPresence, maximum uint64) MetricPresence {
	if left.Presence == "" {
		return right
	}
	if left.Presence == PresenceNotApplicable {
		return right
	}
	if right.Presence == PresenceNotApplicable {
		return left
	}
	if left.Presence != PresenceObserved || right.Presence != PresenceObserved || left.Value > maximum-right.Value {
		return MetricPresence{Presence: PresenceUnknown}
	}
	return MetricPresence{Presence: PresenceObserved, Value: left.Value + right.Value}
}
