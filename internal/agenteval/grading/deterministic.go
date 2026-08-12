package grading

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

func EvaluateDeterministic(ctx context.Context, admitted AdmittedPlan, evidence *PreparedEvidence) (Receipt, error) {
	if ctx == nil || admitted.plan.Mode != ModeDeterministic {
		return Receipt{}, policyError("deterministic_mode")
	}
	if err := evidence.requireAlive(); err != nil {
		return Receipt{}, err
	}
	decisions := make([]Decision, 0, len(admitted.plan.Checks))
	for _, check := range admitted.plan.Checks {
		if err := contextError(ctx); err != nil {
			return Receipt{}, err
		}
		decisions = append(decisions, evaluateMechanicalCheck(check, evidence, AuthorityDeterministic))
	}
	receipt := newReceipt(admitted, evidence, decisions, []ReviewerReceipt{}, notApplicableUsage(), []Disagreement{})
	if err := validateProducedReceipt(admitted, receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func evaluateMechanicalCheck(check Check, evidence *PreparedEvidence, authority Authority) Decision {
	decision := Decision{CheckID: check.ID, Presence: PresenceUnknown, Authority: authority, Citations: []Citation{}}
	observed := func(id string, passed bool) Decision {
		decision.Presence = PresenceObserved
		decision.Passed = passed
		decision.Citations = []Citation{evidence.citation(id)}
		return decision
	}
	switch check.Kind {
	case CheckFileExists:
		ref, ok := evidence.reference(check.FileExists.EvidenceID, check.Visibility, EvidenceFile)
		if ok {
			return observed(check.FileExists.EvidenceID, evidence.set.Files[ref.index].Present == check.FileExists.Expected)
		}
	case CheckFileMetadata:
		ref, ok := evidence.reference(check.FileMetadata.EvidenceID, check.Visibility, EvidenceFile)
		if ok {
			file := evidence.set.Files[ref.index]
			return observed(check.FileMetadata.EvidenceID, file.Present && uint64(len(file.Data)) == check.FileMetadata.ExpectedSizeBytes &&
				file.Mode == check.FileMetadata.ExpectedMode)
		}
	case CheckFileSHA256:
		ref, ok := evidence.reference(check.FileSHA256.EvidenceID, check.Visibility, EvidenceFile)
		if ok {
			file := evidence.set.Files[ref.index]
			return observed(check.FileSHA256.EvidenceID, file.Present && sha256Hex(file.Data) == check.FileSHA256.ExpectedSHA256)
		}
	case CheckJSONValue:
		ref, ok := evidence.reference(check.JSONValue.EvidenceID, check.Visibility, EvidenceFile)
		if ok {
			file := evidence.set.Files[ref.index]
			value, valid := decodeEvidenceJSON(file.Data)
			selected, found := resolveJSONPointer(value, check.JSONValue.Pointer)
			return observed(check.JSONValue.EvidenceID, file.Present && valid && found && jsonValueEqual(selected, check.JSONValue.Expected))
		}
	case CheckJSONSchema:
		ref, ok := evidence.reference(check.JSONSchema.EvidenceID, check.Visibility, EvidenceFile)
		if ok {
			file := evidence.set.Files[ref.index]
			value, valid := decodeEvidenceJSON(file.Data)
			passed := file.Present && valid
			for _, field := range check.JSONSchema.Fields {
				selected, found := resolveJSONPointer(value, field.Pointer)
				if field.Required && !found || found && !jsonValueHasType(selected, field.Type) {
					passed = false
				} else if found && field.MinimumItems != 0 {
					items, array := selected.([]any)
					passed = passed && array && len(items) >= int(field.MinimumItems)
				}
			}
			return observed(check.JSONSchema.EvidenceID, passed)
		}
	case CheckCommandExit:
		ref, ok := evidence.reference(check.CommandExit.EvidenceID, check.Visibility, EvidenceCommand)
		if ok {
			return observed(check.CommandExit.EvidenceID, evidence.set.Commands[ref.index].ExitCode == check.CommandExit.Expected)
		}
	case CheckCommandOutput:
		ref, ok := evidence.reference(check.CommandOutput.EvidenceID, check.Visibility, EvidenceCommand)
		if ok {
			command := evidence.set.Commands[ref.index]
			output := command.Stdout
			if check.CommandOutput.Stream == OutputStderr {
				output = command.Stderr
			}
			return observed(check.CommandOutput.EvidenceID, sha256Hex(output) == check.CommandOutput.ExpectedSHA256)
		}
	case CheckTreeDiff:
		ref, ok := evidence.reference(check.TreeDiff.EvidenceID, check.Visibility, EvidenceTree)
		if ok {
			return observed(check.TreeDiff.EvidenceID, slices.Equal(evidence.set.Trees[ref.index].Changes, check.TreeDiff.Expected))
		}
	case CheckToolSequence:
		ref, ok := evidence.reference(check.ToolSequence.EvidenceID, check.Visibility, EvidenceSequence)
		if ok {
			return observed(check.ToolSequence.EvidenceID, sequenceRuleMatches(evidence.set.Sequences[ref.index].Values, check.ToolSequence))
		}
	case CheckActionSequence:
		ref, ok := evidence.reference(check.ActionSequence.EvidenceID, check.Visibility, EvidenceSequence)
		if ok {
			return observed(check.ActionSequence.EvidenceID, sequenceRuleMatches(evidence.set.Sequences[ref.index].Values, check.ActionSequence))
		}
	case CheckSkillActivation:
		ref, ok := evidence.reference(check.SkillActivation.EvidenceID, check.Visibility, EvidenceCounter)
		if ok {
			value := evidence.set.Counters[ref.index].Value
			return observed(check.SkillActivation.EvidenceID, value >= check.SkillActivation.Minimum && value <= check.SkillActivation.Maximum)
		}
	case CheckSkillUse:
		ref, ok := evidence.reference(check.SkillUse.EvidenceID, check.Visibility, EvidenceCounter)
		if ok {
			value := evidence.set.Counters[ref.index].Value
			return observed(check.SkillUse.EvidenceID, value >= check.SkillUse.Minimum && value <= check.SkillUse.Maximum)
		}
	case CheckBudget:
		ref, ok := evidence.reference(check.Budget.EvidenceID, check.Visibility, EvidenceCounter)
		if ok {
			value := evidence.set.Counters[ref.index].Value
			return observed(check.Budget.EvidenceID, value >= check.Budget.Minimum && value <= check.Budget.Maximum)
		}
	case CheckPolicy:
		ref, ok := evidence.reference(check.Policy.EvidenceID, check.Visibility, EvidenceCounter)
		if ok {
			return observed(check.Policy.EvidenceID, evidence.set.Counters[ref.index].Value <= check.Policy.MaximumViolations)
		}
	}
	return decision
}

func sequenceRuleMatches(observed []string, rule *SequenceRule) bool {
	if rule.Alternatives != nil {
		return slices.ContainsFunc(rule.Alternatives, func(expected []string) bool { return slices.Equal(observed, expected) })
	}
	return sequenceSimilarityBPS(observed, rule.Expected) >= rule.MinimumSimilarityBPS
}

// sequenceSimilarityBPS is the symmetric LCS similarity
// 2*LCS/(len(observed)+len(expected)), expressed in basis points.
func sequenceSimilarityBPS(observed, expected []string) uint32 {
	if len(observed) == 0 && len(expected) == 0 {
		return 10_000
	}
	if len(observed) == 0 || len(expected) == 0 {
		return 0
	}
	if len(expected) > len(observed) {
		observed, expected = expected, observed
	}
	row := make([]int, len(expected)+1)
	for _, left := range observed {
		previous := 0
		for index, right := range expected {
			above := row[index+1]
			if left == right {
				row[index+1] = previous + 1
			} else if row[index] > row[index+1] {
				row[index+1] = row[index]
			}
			previous = above
		}
	}
	// #nosec G115 -- both sequences are capped at MaxSequenceItems, so the ratio is in 0..10000.
	return uint32((20_000 * row[len(expected)]) / (len(observed) + len(expected)))
}

func newReceipt(admitted AdmittedPlan, evidence *PreparedEvidence, decisions []Decision, reviewers []ReviewerReceipt, usage Usage,
	disagreements []Disagreement) Receipt {
	status := ReceiptComplete
	if slices.ContainsFunc(decisions, func(decision Decision) bool { return decision.Presence != PresenceObserved }) {
		status = ReceiptIncomplete
	}
	return Receipt{
		Schema: ReceiptSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		ContractSHA256: admitted.plan.ContractSHA256, PlanSHA256: admitted.planSHA,
		InputProjectionSHA256: admitted.plan.InputProjectionSHA256, EvidenceSHA256: evidence.digest, Evidence: slices.Clone(evidence.catalog),
		Status: status, Decisions: decisions, Reviewers: reviewers, Usage: usage, Disagreements: disagreements,
	}
}

func notApplicableMetric() MetricPresence { return MetricPresence{Presence: PresenceNotApplicable} }

func notApplicableUsage() Usage {
	return Usage{InputTokens: notApplicableMetric(), OutputTokens: notApplicableMetric(), EstimatedCostMicroUSD: notApplicableMetric(),
		DurationMillis: notApplicableMetric()}
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
