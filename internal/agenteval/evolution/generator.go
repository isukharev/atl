package evolution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func digestValue(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/evolution-proposal/v1\x00"))
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type requestProjection struct {
	LineageSHA256    string           `json:"lineage_sha256"`
	SkillSHA256      string           `json:"skill_sha256"`
	EvaluationSHA256 string           `json:"evaluation_sha256"`
	SelfFeedbackOnly bool             `json:"self_feedback_only"`
	Failures         []FailureSummary `json:"failures"`
}

func (request Request) projection() requestProjection {
	return requestProjection{
		LineageSHA256: request.LineageSHA256, SkillSHA256: request.SkillSHA256,
		EvaluationSHA256: request.EvaluationSHA256, SelfFeedbackOnly: request.SelfFeedbackOnly,
		Failures: cloneFailures(request.Failures),
	}
}

func proposalWithoutDigest(proposal Proposal) Proposal {
	proposal.ProposalSHA256 = ""
	return proposal
}

// Generate turns content-minimized failure summaries into a deterministic
// review-only proposal. Self-feedback-only requests remain exploratory and
// explicitly cannot claim reusable improvement.
func Generate(request Request) (Proposal, error) {
	if err := validateRequest(request); err != nil {
		return Proposal{}, err
	}
	request.Failures = cloneFailures(request.Failures)
	inputSHA, err := digestValue("input", request.projection())
	if err != nil {
		return Proposal{}, fail(ErrorInvalidInput)
	}
	changesSkill := make([]ProposalChange, len(request.Failures))
	changesEvaluation := make([]ProposalChange, len(request.Failures))
	for index, failure := range request.Failures {
		changesSkill[index] = ProposalChange{Class: failure.Class, Action: string(skillAction(failure.Class)), EvidenceSHA256: append([]string(nil), failure.EvidenceSHA256...)}
		changesEvaluation[index] = ProposalChange{Class: failure.Class, Action: string(evaluationAction(failure.Class)), EvidenceSHA256: append([]string(nil), failure.EvidenceSHA256...)}
	}
	proposal := Proposal{
		Schema: Schema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		LineageSHA256: request.LineageSHA256, BaseSkillSHA256: request.SkillSHA256,
		BaseEvaluationSHA256: request.EvaluationSHA256, InputSHA256: inputSHA,
		SelfFeedbackOnly: request.SelfFeedbackOnly, Exploratory: request.SelfFeedbackOnly,
		ReusableImprovement: !request.SelfFeedbackOnly, Failures: request.Failures,
		SkillChanges: changesSkill, EvaluationChanges: changesEvaluation,
	}
	proposalDigest, err := digestValue("proposal", proposalWithoutDigest(proposal))
	if err != nil {
		return Proposal{}, fail(ErrorInvalidProposal)
	}
	proposal.ProposalSHA256 = proposalDigest
	if err := Validate(proposal); err != nil {
		return Proposal{}, err
	}
	return cloneProposal(proposal), nil
}

func validateRequest(request Request) error {
	if !validDigest(request.LineageSHA256) || !validDigest(request.SkillSHA256) || !validDigest(request.EvaluationSHA256) {
		return fail(ErrorInvalidInput)
	}
	if len(request.Failures) == 0 || len(request.Failures) > MaxFailures {
		return fail(ErrorInvalidInput)
	}
	seenClasses := make(map[FailureClass]bool, len(request.Failures))
	for _, failure := range request.Failures {
		if failureOrdinal(failure.Class) < 0 || failure.Count == 0 || len(failure.EvidenceSHA256) == 0 || len(failure.EvidenceSHA256) > MaxEvidenceRefs || seenClasses[failure.Class] {
			return fail(ErrorInvalidInput)
		}
		seenClasses[failure.Class] = true
		for index, evidence := range failure.EvidenceSHA256 {
			if !validDigest(evidence) || index > 0 && failure.EvidenceSHA256[index-1] >= evidence {
				return fail(ErrorInvalidInput)
			}
		}
	}
	return nil
}

// Validate checks a proposal's closed shape, canonical order, action mapping,
// and content-addressed digest. It does not infer or apply any change.
func Validate(proposal Proposal) error {
	if proposal.Schema != Schema || proposal.SchemaVersion != SchemaVersion || proposal.ContractVersion != ContractVersion ||
		!validDigest(proposal.LineageSHA256) || !validDigest(proposal.BaseSkillSHA256) || !validDigest(proposal.BaseEvaluationSHA256) ||
		!validDigest(proposal.InputSHA256) || !validDigest(proposal.ProposalSHA256) || len(proposal.Failures) == 0 || len(proposal.Failures) > MaxFailures ||
		len(proposal.SkillChanges) != len(proposal.Failures) || len(proposal.EvaluationChanges) != len(proposal.Failures) ||
		proposal.Exploratory != proposal.SelfFeedbackOnly || proposal.ReusableImprovement == proposal.SelfFeedbackOnly {
		return fail(ErrorInvalidProposal)
	}
	for index, failure := range proposal.Failures {
		if failureOrdinal(failure.Class) < 0 || failure.Count == 0 || len(failure.EvidenceSHA256) == 0 || len(failure.EvidenceSHA256) > MaxEvidenceRefs ||
			index > 0 && failureOrdinal(proposal.Failures[index-1].Class) >= failureOrdinal(failure.Class) {
			return fail(ErrorInvalidProposal)
		}
		for evidenceIndex, evidence := range failure.EvidenceSHA256 {
			if !validDigest(evidence) || evidenceIndex > 0 && failure.EvidenceSHA256[evidenceIndex-1] >= evidence {
				return fail(ErrorInvalidProposal)
			}
		}
		if err := validateChange(proposal.SkillChanges[index], failure, true); err != nil {
			return err
		}
		if err := validateChange(proposal.EvaluationChanges[index], failure, false); err != nil {
			return err
		}
	}
	input, err := digestValue("input", requestProjection{
		LineageSHA256: proposal.LineageSHA256, SkillSHA256: proposal.BaseSkillSHA256,
		EvaluationSHA256: proposal.BaseEvaluationSHA256, SelfFeedbackOnly: proposal.SelfFeedbackOnly,
		Failures: cloneFailures(proposal.Failures),
	})
	if err != nil || input != proposal.InputSHA256 {
		return fail(ErrorInvalidProposal)
	}
	digest, err := digestValue("proposal", proposalWithoutDigest(proposal))
	if err != nil || digest != proposal.ProposalSHA256 {
		return fail(ErrorInvalidProposal)
	}
	return nil
}

func validateChange(change ProposalChange, failure FailureSummary, skill bool) error {
	if change.Class != failure.Class || len(change.EvidenceSHA256) != len(failure.EvidenceSHA256) {
		return fail(ErrorInvalidProposal)
	}
	for index, evidence := range change.EvidenceSHA256 {
		if evidence != failure.EvidenceSHA256[index] || !validDigest(evidence) {
			return fail(ErrorInvalidProposal)
		}
	}
	if skill && !validSkillAction(change.Action, failure.Class) || !skill && !validEvaluationAction(change.Action, failure.Class) {
		return fail(ErrorInvalidProposal)
	}
	return nil
}
