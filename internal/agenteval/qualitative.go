package agenteval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	RubricSchemaVersion           = 1
	ReviewSchemaVersion           = 1
	QualitativePanelSchemaVersion = 1
	QualitativePanelMethod        = "criterion-median-v1"
	maxReviewBytes                = 16 << 20
	maxRubricCriteria             = 32
)

// Rubric defines a bounded, public scoring guide. It never contains a candidate
// answer or private backend material.
type Rubric struct {
	SchemaVersion     int               `json:"schema_version"`
	ID                string            `json:"id"`
	ScenarioID        string            `json:"scenario_id"`
	MinimumScoreBPS   int               `json:"minimum_score_bps"`
	Criteria          []RubricCriterion `json:"criteria"`
	AllowedFindingIDs []string          `json:"allowed_finding_ids"`
}

type RubricCriterion struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Maximum     int    `json:"maximum"`
	Minimum     int    `json:"minimum"`
	Weight      int    `json:"weight"`
}

type Reviewer struct {
	ID    string `json:"id,omitempty"`
	Kind  string `json:"kind"`
	Model string `json:"model,omitempty"`
}

// Review is a private input produced by a maintainer or a separately prompted
// judge. Free-form rationales are deliberately excluded so they cannot leak
// candidate or backend text into a publishable contract.
type Review struct {
	SchemaVersion       int                    `json:"schema_version"`
	RubricID            string                 `json:"rubric_id"`
	RubricSHA256        string                 `json:"rubric_sha256"`
	ScenarioID          string                 `json:"scenario_id"`
	ResultSHA256        string                 `json:"result_sha256"`
	FinalResponseSHA256 string                 `json:"final_response_sha256"`
	Blinded             bool                   `json:"blinded,omitempty"`
	AssignmentDigest    string                 `json:"assignment_digest,omitempty"`
	Reviewer            Reviewer               `json:"reviewer"`
	Criteria            []ReviewCriterionScore `json:"criteria"`
	FindingIDs          []string               `json:"finding_ids"`
}

type ReviewCriterionScore struct {
	ID    string `json:"id"`
	Score int    `json:"score"`
}

// QualitativeAssessment is safe to retain in a Result: it contains only
// bounded scores, identifiers, reviewer identity, and content digests.
type QualitativeAssessment struct {
	RubricID            string                 `json:"rubric_id"`
	RubricSHA256        string                 `json:"rubric_sha256"`
	ResultSHA256        string                 `json:"result_sha256"`
	FinalResponseSHA256 string                 `json:"final_response_sha256"`
	Blinded             bool                   `json:"blinded,omitempty"`
	AssignmentDigest    string                 `json:"assignment_digest,omitempty"`
	Reviewer            Reviewer               `json:"reviewer"`
	Status              string                 `json:"status"`
	ScoreBPS            int                    `json:"score_bps"`
	Criteria            []QualitativeCriterion `json:"criteria"`
	FindingIDs          []string               `json:"finding_ids"`
}

type QualitativeCriterion struct {
	ID      string `json:"id"`
	Score   int    `json:"score"`
	Maximum int    `json:"maximum"`
	Minimum int    `json:"minimum"`
	Weight  int    `json:"weight"`
	Passed  bool   `json:"passed"`
}

// QualitativePanelPolicy is the bounded, deterministic consensus policy for a
// review panel. Schema v1 deliberately permits only odd panels of three or
// five reviewers so every majority and median is unambiguous.
type QualitativePanelPolicy struct {
	SchemaVersion        int    `json:"schema_version"`
	Method               string `json:"method"`
	ExpectedReviewers    int    `json:"expected_reviewers"`
	MaxCriterionRangeBPS int    `json:"max_criterion_range_bps"`
}

type QualitativeReviewSetAssessment struct {
	SchemaVersion       int                             `json:"schema_version"`
	Policy              QualitativePanelPolicy          `json:"policy"`
	ContractSHA256      string                          `json:"contract_sha256"`
	RubricID            string                          `json:"rubric_id"`
	RubricSHA256        string                          `json:"rubric_sha256"`
	MinimumScoreBPS     int                             `json:"minimum_score_bps"`
	ResultSHA256        string                          `json:"result_sha256"`
	FinalResponseSHA256 string                          `json:"final_response_sha256"`
	Blinded             bool                            `json:"blinded,omitempty"`
	AssignmentDigest    string                          `json:"assignment_digest,omitempty"`
	Status              string                          `json:"status"`
	ScoreBPS            int                             `json:"score_bps"`
	Members             []QualitativeReviewSetMember    `json:"members"`
	Criteria            []QualitativeReviewSetCriterion `json:"criteria"`
	FindingIDs          []string                        `json:"finding_ids"`
}

type QualitativeReviewSetMember struct {
	ReviewSHA256 string                 `json:"review_sha256"`
	Reviewer     Reviewer               `json:"reviewer"`
	Status       string                 `json:"status"`
	ScoreBPS     int                    `json:"score_bps"`
	Criteria     []ReviewCriterionScore `json:"criteria"`
	FindingIDs   []string               `json:"finding_ids"`
}

type QualitativeReviewSetCriterion struct {
	ID       string `json:"id"`
	Score    int    `json:"score"`
	Minimum  int    `json:"minimum"`
	Maximum  int    `json:"maximum"`
	Weight   int    `json:"weight"`
	Passed   bool   `json:"passed"`
	MinScore int    `json:"min_score"`
	MaxScore int    `json:"max_score"`
	Passes   int    `json:"passes"`
	Failures int    `json:"failures"`
	RangeBPS int    `json:"range_bps"`
}

func (q QualitativeAssessment) validate(scenarioID string) error {
	if !identifierRE.MatchString(q.RubricID) || !validSHA256(q.RubricSHA256) || !validSHA256(q.ResultSHA256) || !validSHA256(q.FinalResponseSHA256) {
		return fmt.Errorf("qualitative assessment identity is invalid")
	}
	if err := q.Reviewer.validate(); err != nil {
		return err
	}
	if err := validateBlindAssignment(q.Blinded, q.AssignmentDigest); err != nil {
		return fmt.Errorf("qualitative assessment: %w", err)
	}
	if q.Status != "pass" && q.Status != "fail" {
		return fmt.Errorf("qualitative status must be pass or fail")
	}
	if q.ScoreBPS < 0 || q.ScoreBPS > 10000 || len(q.Criteria) == 0 || len(q.Criteria) > maxRubricCriteria {
		return fmt.Errorf("qualitative assessment bounds are invalid")
	}
	seen := map[string]struct{}{}
	for _, item := range q.Criteria {
		if !identifierRE.MatchString(item.ID) || item.Maximum < 1 || item.Maximum > 100 || item.Minimum < 0 || item.Minimum > item.Maximum || item.Score < 0 || item.Score > item.Maximum || item.Weight < 1 || item.Weight > 1000 || item.Passed != (item.Score >= item.Minimum) {
			return fmt.Errorf("invalid qualitative criterion")
		}
		if _, ok := seen[item.ID]; ok {
			return fmt.Errorf("duplicate qualitative criterion %q", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	if err := validateIdentifierList("qualitative finding_ids", q.FindingIDs, false); err != nil {
		return err
	}
	_ = scenarioID
	return nil
}

func (q QualitativeReviewSetAssessment) validate(scenarioID string) error {
	if q.SchemaVersion != QualitativePanelSchemaVersion {
		return fmt.Errorf("unsupported qualitative review-set schema_version %d", q.SchemaVersion)
	}
	if err := q.Policy.Validate(); err != nil {
		return err
	}
	if !identifierRE.MatchString(q.RubricID) || !validSHA256(q.RubricSHA256) || !validSHA256(q.ResultSHA256) || !validSHA256(q.FinalResponseSHA256) || !validSHA256(q.ContractSHA256) {
		return fmt.Errorf("qualitative review-set identity is invalid")
	}
	if err := validateBlindAssignment(q.Blinded, q.AssignmentDigest); err != nil {
		return fmt.Errorf("qualitative review-set: %w", err)
	}
	if q.Status != "pass" && q.Status != "fail" && q.Status != "disagreement" {
		return fmt.Errorf("qualitative review-set status must be pass, fail, or disagreement")
	}
	if q.ScoreBPS < 0 || q.ScoreBPS > 10000 || q.MinimumScoreBPS < 0 || q.MinimumScoreBPS > 10000 || len(q.Members) != q.Policy.ExpectedReviewers {
		return fmt.Errorf("qualitative review-set bounds are invalid")
	}
	if len(q.Criteria) == 0 || len(q.Criteria) > maxRubricCriteria {
		return fmt.Errorf("qualitative review-set criteria bounds are invalid")
	}
	seenCriteria := map[string]struct{}{}
	for _, criterion := range q.Criteria {
		if !identifierRE.MatchString(criterion.ID) || criterion.Maximum < 1 || criterion.Maximum > 100 || criterion.Minimum < 0 || criterion.Minimum > criterion.Maximum || criterion.Score < 0 || criterion.Score > criterion.Maximum || criterion.Weight < 1 || criterion.Weight > 1000 {
			return fmt.Errorf("invalid qualitative review-set criterion")
		}
		if _, ok := seenCriteria[criterion.ID]; ok {
			return fmt.Errorf("duplicate qualitative review-set criterion %q", criterion.ID)
		}
		seenCriteria[criterion.ID] = struct{}{}
	}
	reviewers := make([]Reviewer, 0, len(q.Members))
	seenReviewDigests := map[string]struct{}{}
	previousID := ""
	individualPasses := 0
	criterionScores := make([][]int, len(q.Criteria))
	unionFindings := map[string]struct{}{}
	for _, member := range q.Members {
		if err := member.Reviewer.validate(); err != nil {
			return err
		}
		if member.Reviewer.ID == "" || (previousID != "" && member.Reviewer.ID <= previousID) {
			return fmt.Errorf("qualitative review-set members must be sorted by unique reviewer id")
		}
		previousID = member.Reviewer.ID
		if !validSHA256(member.ReviewSHA256) {
			return fmt.Errorf("qualitative review-set member digest is invalid")
		}
		if _, ok := seenReviewDigests[member.ReviewSHA256]; ok {
			return fmt.Errorf("duplicate qualitative review-set member digest")
		}
		seenReviewDigests[member.ReviewSHA256] = struct{}{}
		if member.Status != "pass" && member.Status != "fail" || member.ScoreBPS < 0 || member.ScoreBPS > 10000 {
			return fmt.Errorf("qualitative review-set member bounds are invalid")
		}
		if err := validateIdentifierList("qualitative review-set member finding_ids", member.FindingIDs, false); err != nil {
			return err
		}
		if !sort.StringsAreSorted(member.FindingIDs) {
			return fmt.Errorf("qualitative review-set member finding_ids must be sorted")
		}
		for _, finding := range member.FindingIDs {
			unionFindings[finding] = struct{}{}
		}
		if len(member.Criteria) != len(q.Criteria) {
			return fmt.Errorf("qualitative review-set member criteria do not match consensus criteria")
		}
		var weightedScore, weightedMaximum int64
		memberPass := true
		for index, score := range member.Criteria {
			definition := q.Criteria[index]
			if score.ID != definition.ID || score.Score < 0 || score.Score > definition.Maximum {
				return fmt.Errorf("qualitative review-set member criterion is invalid")
			}
			criterionScores[index] = append(criterionScores[index], score.Score)
			weightedScore += int64(score.Score * definition.Weight)
			weightedMaximum += int64(definition.Maximum * definition.Weight)
			if score.Score < definition.Minimum {
				memberPass = false
			}
		}
		memberScoreBPS := int((weightedScore*10000 + weightedMaximum/2) / weightedMaximum)
		if memberScoreBPS < q.MinimumScoreBPS {
			memberPass = false
		}
		expectedMemberStatus := "fail"
		if memberPass {
			expectedMemberStatus = "pass"
			individualPasses++
		}
		if member.ScoreBPS != memberScoreBPS || member.Status != expectedMemberStatus {
			return fmt.Errorf("qualitative review-set member assessment is inconsistent")
		}
		reconstructedReview := Review{
			SchemaVersion: ReviewSchemaVersion, RubricID: q.RubricID, RubricSHA256: q.RubricSHA256,
			ScenarioID: scenarioID, ResultSHA256: q.ResultSHA256, FinalResponseSHA256: q.FinalResponseSHA256,
			Blinded: q.Blinded, AssignmentDigest: q.AssignmentDigest, Reviewer: member.Reviewer,
			Criteria: append([]ReviewCriterionScore{}, member.Criteria...), FindingIDs: append([]string{}, member.FindingIDs...),
		}
		data, err := json.Marshal(reconstructedReview)
		if err != nil {
			return err
		}
		if member.ReviewSHA256 != sha256Hex(data) {
			return fmt.Errorf("qualitative review-set member digest does not match retained review")
		}
		reviewers = append(reviewers, member.Reviewer)
	}
	expectedContract, err := qualitativeReviewSetContractDigest(q.Policy, reviewers)
	if err != nil {
		return err
	}
	if q.ContractSHA256 != expectedContract {
		return fmt.Errorf("qualitative review-set contract digest does not match")
	}
	highDisagreement := individualPasses > 0 && individualPasses < len(q.Members)
	consensusPass := individualPasses > len(q.Members)/2
	var weightedScore, weightedMaximum int64
	for index, criterion := range q.Criteria {
		scores := criterionScores[index]
		sort.Ints(scores)
		passes := 0
		for _, score := range scores {
			if score >= criterion.Minimum {
				passes++
			}
		}
		failures := len(scores) - passes
		median := scores[len(scores)/2]
		rangeBPS := ceilRangeBPS(scores[len(scores)-1]-scores[0], criterion.Maximum)
		passed := median >= criterion.Minimum
		if criterion.Score != median || criterion.Passed != passed || criterion.MinScore != scores[0] || criterion.MaxScore != scores[len(scores)-1] || criterion.Passes != passes || criterion.Failures != failures || criterion.RangeBPS != rangeBPS {
			return fmt.Errorf("qualitative review-set criterion summary is inconsistent")
		}
		if !passed {
			consensusPass = false
		}
		if (passes > 0 && failures > 0) || rangeBPS > q.Policy.MaxCriterionRangeBPS {
			highDisagreement = true
		}
		weightedScore += int64(median * criterion.Weight)
		weightedMaximum += int64(criterion.Maximum * criterion.Weight)
	}
	expectedScoreBPS := int((weightedScore*10000 + weightedMaximum/2) / weightedMaximum)
	if expectedScoreBPS < q.MinimumScoreBPS {
		consensusPass = false
	}
	expectedStatus := "fail"
	if consensusPass {
		expectedStatus = "pass"
	}
	if highDisagreement {
		expectedStatus = "disagreement"
	}
	if q.ScoreBPS != expectedScoreBPS || q.Status != expectedStatus {
		return fmt.Errorf("qualitative review-set consensus is inconsistent")
	}
	if err := validateIdentifierList("qualitative review-set finding_ids", q.FindingIDs, false); err != nil {
		return err
	}
	if !sort.StringsAreSorted(q.FindingIDs) {
		return fmt.Errorf("qualitative review-set finding_ids must be sorted")
	}
	expectedFindings := make([]string, 0, len(unionFindings))
	for finding := range unionFindings {
		expectedFindings = append(expectedFindings, finding)
	}
	sort.Strings(expectedFindings)
	if strings.Join(q.FindingIDs, "\x00") != strings.Join(expectedFindings, "\x00") {
		return fmt.Errorf("qualitative review-set findings do not match members")
	}
	return nil
}

func DecodeRubric(r io.Reader) (Rubric, error) {
	var value Rubric
	if err := decodeStrict(r, &value); err != nil {
		return Rubric{}, err
	}
	if err := value.Validate(); err != nil {
		return Rubric{}, err
	}
	return value, nil
}

func DecodeReview(r io.Reader) (Review, error) {
	var value Review
	if err := decodeStrict(r, &value); err != nil {
		return Review{}, err
	}
	if err := value.Validate(); err != nil {
		return Review{}, err
	}
	return value, nil
}

func NewReviewTemplate(result Result, resultBytes, finalBytes []byte, rubric Rubric, reviewer Reviewer, blindAssignment ...[]byte) (Review, error) {
	if err := result.Validate(); err != nil {
		return Review{}, err
	}
	if result.EffectiveEligibility() != EligibilitySupported {
		return Review{}, fmt.Errorf("ineligible result cannot be qualitatively reviewed")
	}
	if err := rubric.Validate(); err != nil {
		return Review{}, err
	}
	if err := reviewer.validate(); err != nil {
		return Review{}, err
	}
	if result.ScenarioID != rubric.ScenarioID {
		return Review{}, fmt.Errorf("rubric and result scenario_id do not match")
	}
	if len(blindAssignment) > 1 {
		return Review{}, fmt.Errorf("at most one blind assignment is allowed")
	}
	blinded := len(blindAssignment) == 1
	assignmentDigest := ""
	if blinded {
		if len(blindAssignment[0]) == 0 || len(blindAssignment[0]) > maxReviewBytes {
			return Review{}, fmt.Errorf("blind assignment must contain 1..%d bytes", maxReviewBytes)
		}
		assignmentDigest = sha256Hex(blindAssignment[0])
	}
	if result.EffectiveCategory() == BenchmarkCategoryNeutralCommon && !blinded {
		return Review{}, fmt.Errorf("neutral-common review requires a blind assignment")
	}
	criteria := make([]ReviewCriterionScore, 0, len(rubric.Criteria))
	for _, item := range rubric.Criteria {
		criteria = append(criteria, ReviewCriterionScore{ID: item.ID, Score: 0})
	}
	return Review{SchemaVersion: ReviewSchemaVersion, RubricID: rubric.ID, RubricSHA256: rubricSHA256(rubric), ScenarioID: result.ScenarioID, ResultSHA256: sha256Hex(resultBytes), FinalResponseSHA256: sha256Hex(finalBytes), Blinded: blinded, AssignmentDigest: assignmentDigest, Reviewer: reviewer, Criteria: criteria, FindingIDs: []string{}}, nil
}

func (r Rubric) Validate() error {
	if r.SchemaVersion != RubricSchemaVersion {
		return fmt.Errorf("unsupported rubric schema_version %d", r.SchemaVersion)
	}
	if !identifierRE.MatchString(r.ID) || !identifierRE.MatchString(r.ScenarioID) {
		return fmt.Errorf("rubric identity is invalid")
	}
	if r.MinimumScoreBPS < 0 || r.MinimumScoreBPS > 10000 {
		return fmt.Errorf("minimum_score_bps must be in 0..10000")
	}
	if len(r.Criteria) == 0 || len(r.Criteria) > maxRubricCriteria {
		return fmt.Errorf("criteria must contain 1..%d entries", maxRubricCriteria)
	}
	seen := map[string]struct{}{}
	for _, criterion := range r.Criteria {
		if !identifierRE.MatchString(criterion.ID) {
			return fmt.Errorf("invalid rubric criterion id %q", criterion.ID)
		}
		if _, ok := seen[criterion.ID]; ok {
			return fmt.Errorf("duplicate rubric criterion %q", criterion.ID)
		}
		seen[criterion.ID] = struct{}{}
		if strings.TrimSpace(criterion.Description) == "" || len(criterion.Description) > 512 {
			return fmt.Errorf("rubric criterion %q description must contain 1..512 bytes", criterion.ID)
		}
		if criterion.Maximum < 1 || criterion.Maximum > 100 || criterion.Minimum < 0 || criterion.Minimum > criterion.Maximum || criterion.Weight < 1 || criterion.Weight > 1000 {
			return fmt.Errorf("rubric criterion %q bounds are invalid", criterion.ID)
		}
	}
	return validateIdentifierList("allowed_finding_ids", r.AllowedFindingIDs, false)
}

func (r Reviewer) validate() error {
	if r.ID != "" && !identifierRE.MatchString(r.ID) {
		return fmt.Errorf("reviewer id is invalid")
	}
	if r.Kind != "human" && r.Kind != "codex" && r.Kind != "claude-code" {
		return fmt.Errorf("reviewer kind must be human, codex, or claude-code")
	}
	if len(r.Model) > 256 || strings.ContainsAny(r.Model, "\r\n\x00") {
		return fmt.Errorf("reviewer model is invalid")
	}
	if r.Kind != "human" && strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("model reviewer requires an exact model")
	}
	return nil
}

func (p QualitativePanelPolicy) Validate() error {
	if p.SchemaVersion != QualitativePanelSchemaVersion {
		return fmt.Errorf("unsupported qualitative panel schema_version %d", p.SchemaVersion)
	}
	if p.Method != QualitativePanelMethod {
		return fmt.Errorf("unsupported qualitative panel method %q", p.Method)
	}
	if p.ExpectedReviewers != 3 && p.ExpectedReviewers != 5 {
		return fmt.Errorf("expected_reviewers must be 3 or 5")
	}
	if p.MaxCriterionRangeBPS < 1 || p.MaxCriterionRangeBPS > 9999 {
		return fmt.Errorf("max_criterion_range_bps must be in 1..9999")
	}
	return nil
}

func (r Review) Validate() error {
	if r.SchemaVersion != ReviewSchemaVersion {
		return fmt.Errorf("unsupported review schema_version %d", r.SchemaVersion)
	}
	if !identifierRE.MatchString(r.RubricID) || !identifierRE.MatchString(r.ScenarioID) {
		return fmt.Errorf("review identity is invalid")
	}
	if !validSHA256(r.RubricSHA256) || !validSHA256(r.ResultSHA256) || !validSHA256(r.FinalResponseSHA256) {
		return fmt.Errorf("review hashes must be lowercase SHA-256")
	}
	if err := r.Reviewer.validate(); err != nil {
		return err
	}
	if err := validateBlindAssignment(r.Blinded, r.AssignmentDigest); err != nil {
		return err
	}
	if len(r.Criteria) == 0 || len(r.Criteria) > maxRubricCriteria {
		return fmt.Errorf("review criteria must contain 1..%d entries", maxRubricCriteria)
	}
	seen := map[string]struct{}{}
	for _, criterion := range r.Criteria {
		if !identifierRE.MatchString(criterion.ID) || criterion.Score < 0 || criterion.Score > 100 {
			return fmt.Errorf("invalid review criterion")
		}
		if _, ok := seen[criterion.ID]; ok {
			return fmt.Errorf("duplicate review criterion %q", criterion.ID)
		}
		seen[criterion.ID] = struct{}{}
	}
	return validateIdentifierList("finding_ids", r.FindingIDs, false)
}

func AssessQualitative(result Result, resultBytes, finalBytes []byte, rubric Rubric, review Review) (Result, error) {
	if err := result.Validate(); err != nil {
		return Result{}, err
	}
	if result.Qualitative != nil || result.QualitativeReviewSet != nil {
		return Result{}, fmt.Errorf("result already contains a qualitative assessment")
	}
	if len(finalBytes) > maxReviewBytes {
		return Result{}, fmt.Errorf("final response exceeds %d bytes", maxReviewBytes)
	}
	assessment, err := assessBoundReview(result, resultBytes, finalBytes, rubric, review)
	if err != nil {
		return Result{}, err
	}
	result.Qualitative = &assessment
	if assessment.Status == "fail" {
		result.Status = "fail"
		result.Violations = append(result.Violations, Violation{Code: "qualitative_review_failed", Subject: rubric.ID, Observed: int64(assessment.ScoreBPS), Limit: int64(rubric.MinimumScoreBPS)})
		sortViolations(result.Violations)
	}
	return result, nil
}

func assessBoundReview(result Result, resultBytes, finalBytes []byte, rubric Rubric, review Review) (QualitativeAssessment, error) {
	if rubric.ScenarioID != result.ScenarioID || review.ScenarioID != result.ScenarioID || review.RubricID != rubric.ID {
		return QualitativeAssessment{}, fmt.Errorf("rubric, review, and result identity do not match")
	}
	if result.EffectiveCategory() == BenchmarkCategoryNeutralCommon && (!review.Blinded || !validSHA256(review.AssignmentDigest)) {
		return QualitativeAssessment{}, fmt.Errorf("neutral-common qualitative assessment requires a blinded assignment digest")
	}
	resultHash := sha256Hex(resultBytes)
	finalHash := sha256Hex(finalBytes)
	if review.ResultSHA256 != resultHash || review.FinalResponseSHA256 != finalHash {
		return QualitativeAssessment{}, fmt.Errorf("review hashes do not bind the supplied result and final response")
	}
	if review.RubricSHA256 != rubricSHA256(rubric) {
		return QualitativeAssessment{}, fmt.Errorf("review hash does not bind the supplied rubric")
	}
	scores := make(map[string]int, len(review.Criteria))
	for _, item := range review.Criteria {
		scores[item.ID] = item.Score
	}
	allowedFindings := map[string]struct{}{}
	for _, finding := range rubric.AllowedFindingIDs {
		allowedFindings[finding] = struct{}{}
	}
	findings := append([]string{}, review.FindingIDs...)
	for _, finding := range findings {
		if _, ok := allowedFindings[finding]; !ok {
			return QualitativeAssessment{}, fmt.Errorf("review finding %q is not allowed by rubric", finding)
		}
	}
	sort.Strings(findings)

	criteria := make([]QualitativeCriterion, 0, len(rubric.Criteria))
	var weightedScore, weightedMaximum int64
	qualitativePass := true
	for _, definition := range rubric.Criteria {
		score, ok := scores[definition.ID]
		if !ok {
			return QualitativeAssessment{}, fmt.Errorf("review omits rubric criterion %q", definition.ID)
		}
		if score > definition.Maximum {
			return QualitativeAssessment{}, fmt.Errorf("review score for %q exceeds maximum", definition.ID)
		}
		passed := score >= definition.Minimum
		if !passed {
			qualitativePass = false
		}
		criteria = append(criteria, QualitativeCriterion{ID: definition.ID, Score: score, Maximum: definition.Maximum, Minimum: definition.Minimum, Weight: definition.Weight, Passed: passed})
		weightedScore += int64(score * definition.Weight)
		weightedMaximum += int64(definition.Maximum * definition.Weight)
	}
	if len(scores) != len(rubric.Criteria) {
		return QualitativeAssessment{}, fmt.Errorf("review contains criteria not defined by rubric")
	}
	scoreBPS := int((weightedScore*10000 + weightedMaximum/2) / weightedMaximum)
	if scoreBPS < rubric.MinimumScoreBPS {
		qualitativePass = false
	}
	status := "pass"
	if !qualitativePass {
		status = "fail"
	}
	return QualitativeAssessment{RubricID: rubric.ID, RubricSHA256: review.RubricSHA256, ResultSHA256: resultHash, FinalResponseSHA256: finalHash, Blinded: review.Blinded, AssignmentDigest: review.AssignmentDigest, Reviewer: review.Reviewer, Status: status, ScoreBPS: scoreBPS, Criteria: criteria, FindingIDs: findings}, nil
}

func sortViolations(violations []Violation) {
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Code != violations[j].Code {
			return violations[i].Code < violations[j].Code
		}
		return violations[i].Subject < violations[j].Subject
	})
}

// AssessQualitativeReviewSet binds an odd review panel to one immutable result,
// final response, rubric, and blind assignment, then computes criterion-wise
// median consensus. Individual reviews remain inputs; only bounded assessments
// and their canonical digests are retained.
func AssessQualitativeReviewSet(result Result, resultBytes, finalBytes []byte, rubric Rubric, policy QualitativePanelPolicy, reviews []Review) (Result, error) {
	if err := result.Validate(); err != nil {
		return Result{}, err
	}
	if result.Qualitative != nil || result.QualitativeReviewSet != nil {
		return Result{}, fmt.Errorf("result already contains a qualitative assessment")
	}
	if err := rubric.Validate(); err != nil {
		return Result{}, err
	}
	if err := policy.Validate(); err != nil {
		return Result{}, err
	}
	if len(finalBytes) > maxReviewBytes {
		return Result{}, fmt.Errorf("final response exceeds %d bytes", maxReviewBytes)
	}
	if len(reviews) != policy.ExpectedReviewers {
		return Result{}, fmt.Errorf("review panel requires exactly %d reviews", policy.ExpectedReviewers)
	}

	type assessedMember struct {
		assessment QualitativeAssessment
		digest     string
	}
	members := make([]assessedMember, 0, len(reviews))
	reviewerIDs := map[string]struct{}{}
	reviewerIdentities := map[string]struct{}{}
	reviewDigests := map[string]struct{}{}
	panelBlinded := reviews[0].Blinded
	panelAssignment := reviews[0].AssignmentDigest
	for index, review := range reviews {
		if err := review.Validate(); err != nil {
			return Result{}, fmt.Errorf("review %d: %w", index, err)
		}
		if review.Reviewer.ID == "" {
			return Result{}, fmt.Errorf("review %d: panel reviewer id is required", index)
		}
		if _, ok := reviewerIDs[review.Reviewer.ID]; ok {
			return Result{}, fmt.Errorf("duplicate panel reviewer id %q", review.Reviewer.ID)
		}
		reviewerIDs[review.Reviewer.ID] = struct{}{}
		identity := reviewerIdentityKey(review.Reviewer)
		if _, ok := reviewerIdentities[identity]; ok {
			return Result{}, fmt.Errorf("duplicate panel reviewer identity")
		}
		reviewerIdentities[identity] = struct{}{}
		if review.Blinded != panelBlinded || review.AssignmentDigest != panelAssignment {
			return Result{}, fmt.Errorf("panel reviews must bind the same blind assignment")
		}
		assessment, err := assessBoundReview(result, resultBytes, finalBytes, rubric, review)
		if err != nil {
			return Result{}, fmt.Errorf("review %d: %w", index, err)
		}
		digest, err := canonicalReviewSHA256(review, rubric)
		if err != nil {
			return Result{}, fmt.Errorf("review %d: %w", index, err)
		}
		if _, ok := reviewDigests[digest]; ok {
			return Result{}, fmt.Errorf("duplicate canonical panel review digest")
		}
		reviewDigests[digest] = struct{}{}
		members = append(members, assessedMember{assessment: assessment, digest: digest})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].assessment.Reviewer.ID < members[j].assessment.Reviewer.ID })

	reviewers := make([]Reviewer, 0, len(members))
	setMembers := make([]QualitativeReviewSetMember, 0, len(members))
	findingSet := map[string]struct{}{}
	individualPasses := 0
	for _, member := range members {
		reviewers = append(reviewers, member.assessment.Reviewer)
		if member.assessment.Status == "pass" {
			individualPasses++
		}
		for _, finding := range member.assessment.FindingIDs {
			findingSet[finding] = struct{}{}
		}
		memberCriteria := make([]ReviewCriterionScore, 0, len(member.assessment.Criteria))
		for _, criterion := range member.assessment.Criteria {
			memberCriteria = append(memberCriteria, ReviewCriterionScore{ID: criterion.ID, Score: criterion.Score})
		}
		setMembers = append(setMembers, QualitativeReviewSetMember{
			ReviewSHA256: member.digest,
			Reviewer:     member.assessment.Reviewer,
			Status:       member.assessment.Status,
			ScoreBPS:     member.assessment.ScoreBPS,
			Criteria:     memberCriteria,
			FindingIDs:   append([]string{}, member.assessment.FindingIDs...),
		})
	}
	findings := make([]string, 0, len(findingSet))
	for finding := range findingSet {
		findings = append(findings, finding)
	}
	sort.Strings(findings)

	highDisagreement := individualPasses > 0 && individualPasses < len(members)
	consensusPass := individualPasses > len(members)/2
	criteria := make([]QualitativeReviewSetCriterion, 0, len(rubric.Criteria))
	var weightedScore, weightedMaximum int64
	for criterionIndex, definition := range rubric.Criteria {
		scores := make([]int, 0, len(members))
		passes := 0
		for _, member := range members {
			criterion := member.assessment.Criteria[criterionIndex]
			scores = append(scores, criterion.Score)
			if criterion.Passed {
				passes++
			}
		}
		sort.Ints(scores)
		median := scores[len(scores)/2]
		failures := len(scores) - passes
		rangeBPS := ceilRangeBPS(scores[len(scores)-1]-scores[0], definition.Maximum)
		passed := median >= definition.Minimum
		if !passed {
			consensusPass = false
		}
		if (passes > 0 && failures > 0) || rangeBPS > policy.MaxCriterionRangeBPS {
			highDisagreement = true
		}
		criteria = append(criteria, QualitativeReviewSetCriterion{
			ID: definition.ID, Score: median, Minimum: definition.Minimum,
			Maximum: definition.Maximum, Weight: definition.Weight, Passed: passed,
			MinScore: scores[0], MaxScore: scores[len(scores)-1], Passes: passes,
			Failures: failures, RangeBPS: rangeBPS,
		})
		weightedScore += int64(median * definition.Weight)
		weightedMaximum += int64(definition.Maximum * definition.Weight)
	}
	scoreBPS := int((weightedScore*10000 + weightedMaximum/2) / weightedMaximum)
	if scoreBPS < rubric.MinimumScoreBPS {
		consensusPass = false
	}
	status := "fail"
	if consensusPass {
		status = "pass"
	}
	if highDisagreement {
		status = "disagreement"
	}
	contractDigest, err := QualitativeReviewSetContractSHA256(policy, reviewers)
	if err != nil {
		return Result{}, err
	}
	resultHash := sha256Hex(resultBytes)
	finalHash := sha256Hex(finalBytes)
	result.QualitativeReviewSet = &QualitativeReviewSetAssessment{
		SchemaVersion: QualitativePanelSchemaVersion, Policy: policy, ContractSHA256: contractDigest,
		RubricID: rubric.ID, RubricSHA256: rubricSHA256(rubric), MinimumScoreBPS: rubric.MinimumScoreBPS, ResultSHA256: resultHash,
		FinalResponseSHA256: finalHash, Blinded: panelBlinded, AssignmentDigest: panelAssignment,
		Status: status, ScoreBPS: scoreBPS, Members: setMembers, Criteria: criteria,
		FindingIDs: findings,
	}
	if status != "pass" {
		result.Status = "fail"
		violation := Violation{Code: "qualitative_review_failed", Subject: rubric.ID, Observed: int64(scoreBPS), Limit: int64(rubric.MinimumScoreBPS)}
		if status == "disagreement" {
			violation = Violation{Code: "qualitative_review_disagreement", Subject: rubric.ID, Observed: 1}
		}
		result.Violations = append(result.Violations, violation)
		sortViolations(result.Violations)
	}
	return result, nil
}

func ceilRangeBPS(scoreRange, maximum int) int {
	return (scoreRange*10000 + maximum - 1) / maximum
}

func reviewerIdentityKey(reviewer Reviewer) string {
	return reviewer.ID + "\x00" + reviewer.Kind + "\x00" + reviewer.Model
}

// QualitativeReviewSetContractSHA256 identifies a compatible panel contract.
// It binds policy and the exact reviewer roster, but intentionally not a
// rubric, result, final response, scores, findings, or review digests.
func QualitativeReviewSetContractSHA256(policy QualitativePanelPolicy, reviewers []Reviewer) (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	roster := append([]Reviewer{}, reviewers...)
	if len(roster) != policy.ExpectedReviewers {
		return "", fmt.Errorf("reviewer roster requires exactly %d members", policy.ExpectedReviewers)
	}
	sort.Slice(roster, func(i, j int) bool { return roster[i].ID < roster[j].ID })
	seenIDs := map[string]struct{}{}
	seenIdentities := map[string]struct{}{}
	for _, reviewer := range roster {
		if err := reviewer.validate(); err != nil {
			return "", err
		}
		if reviewer.ID == "" {
			return "", fmt.Errorf("panel reviewer id is required")
		}
		if _, ok := seenIDs[reviewer.ID]; ok {
			return "", fmt.Errorf("duplicate panel reviewer id %q", reviewer.ID)
		}
		seenIDs[reviewer.ID] = struct{}{}
		identity := reviewerIdentityKey(reviewer)
		if _, ok := seenIdentities[identity]; ok {
			return "", fmt.Errorf("duplicate panel reviewer identity")
		}
		seenIdentities[identity] = struct{}{}
	}
	return qualitativeReviewSetContractDigest(policy, roster)
}

func qualitativeReviewSetContractDigest(policy QualitativePanelPolicy, roster []Reviewer) (string, error) {
	contract := struct {
		SchemaVersion int                    `json:"schema_version"`
		Policy        QualitativePanelPolicy `json:"policy"`
		Reviewers     []Reviewer             `json:"reviewers"`
	}{QualitativePanelSchemaVersion, policy, roster}
	data, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func canonicalReviewSHA256(review Review, rubric Rubric) (string, error) {
	scores := make(map[string]int, len(review.Criteria))
	for _, criterion := range review.Criteria {
		scores[criterion.ID] = criterion.Score
	}
	canonical := review
	canonical.Criteria = make([]ReviewCriterionScore, 0, len(rubric.Criteria))
	for _, definition := range rubric.Criteria {
		score, ok := scores[definition.ID]
		if !ok {
			return "", fmt.Errorf("review omits rubric criterion %q", definition.ID)
		}
		canonical.Criteria = append(canonical.Criteria, ReviewCriterionScore{ID: definition.ID, Score: score})
	}
	canonical.FindingIDs = append([]string{}, review.FindingIDs...)
	sort.Strings(canonical.FindingIDs)
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func validateBlindAssignment(blinded bool, digest string) error {
	if blinded {
		if !validSHA256(digest) {
			return fmt.Errorf("blinded review requires a SHA-256 assignment digest")
		}
		return nil
	}
	if digest != "" {
		return fmt.Errorf("unblinded review cannot contain an assignment digest")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func rubricSHA256(rubric Rubric) string {
	data, _ := json.Marshal(rubric)
	return sha256Hex(data)
}
