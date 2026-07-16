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
	RubricSchemaVersion = 1
	ReviewSchemaVersion = 1
	maxReviewBytes      = 16 << 20
	maxRubricCriteria   = 32
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

func (q QualitativeAssessment) validate(scenarioID string) error {
	if !identifierRE.MatchString(q.RubricID) || !validSHA256(q.RubricSHA256) || !validSHA256(q.ResultSHA256) || !validSHA256(q.FinalResponseSHA256) {
		return fmt.Errorf("qualitative assessment identity is invalid")
	}
	if err := q.Reviewer.validate(); err != nil {
		return err
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

func NewReviewTemplate(result Result, resultBytes, finalBytes []byte, rubric Rubric, reviewer Reviewer) (Review, error) {
	if err := result.Validate(); err != nil {
		return Review{}, err
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
	criteria := make([]ReviewCriterionScore, 0, len(rubric.Criteria))
	for _, item := range rubric.Criteria {
		criteria = append(criteria, ReviewCriterionScore{ID: item.ID, Score: 0})
	}
	return Review{SchemaVersion: ReviewSchemaVersion, RubricID: rubric.ID, RubricSHA256: rubricSHA256(rubric), ScenarioID: result.ScenarioID, ResultSHA256: sha256Hex(resultBytes), FinalResponseSHA256: sha256Hex(finalBytes), Reviewer: reviewer, Criteria: criteria, FindingIDs: []string{}}, nil
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
	if result.Qualitative != nil {
		return Result{}, fmt.Errorf("result already contains a qualitative assessment")
	}
	if len(finalBytes) > maxReviewBytes {
		return Result{}, fmt.Errorf("final response exceeds %d bytes", maxReviewBytes)
	}
	if rubric.ScenarioID != result.ScenarioID || review.ScenarioID != result.ScenarioID || review.RubricID != rubric.ID {
		return Result{}, fmt.Errorf("rubric, review, and result identity do not match")
	}
	resultHash := sha256Hex(resultBytes)
	finalHash := sha256Hex(finalBytes)
	if review.ResultSHA256 != resultHash || review.FinalResponseSHA256 != finalHash {
		return Result{}, fmt.Errorf("review hashes do not bind the supplied result and final response")
	}
	if review.RubricSHA256 != rubricSHA256(rubric) {
		return Result{}, fmt.Errorf("review hash does not bind the supplied rubric")
	}
	scores := make(map[string]int, len(review.Criteria))
	for _, item := range review.Criteria {
		scores[item.ID] = item.Score
	}
	allowedFindings := map[string]struct{}{}
	for _, finding := range rubric.AllowedFindingIDs {
		allowedFindings[finding] = struct{}{}
	}
	findings := append([]string(nil), review.FindingIDs...)
	for _, finding := range findings {
		if _, ok := allowedFindings[finding]; !ok {
			return Result{}, fmt.Errorf("review finding %q is not allowed by rubric", finding)
		}
	}
	sort.Strings(findings)

	criteria := make([]QualitativeCriterion, 0, len(rubric.Criteria))
	var weightedScore, weightedMaximum int64
	qualitativePass := true
	for _, definition := range rubric.Criteria {
		score, ok := scores[definition.ID]
		if !ok {
			return Result{}, fmt.Errorf("review omits rubric criterion %q", definition.ID)
		}
		if score > definition.Maximum {
			return Result{}, fmt.Errorf("review score for %q exceeds maximum", definition.ID)
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
		return Result{}, fmt.Errorf("review contains criteria not defined by rubric")
	}
	scoreBPS := int((weightedScore*10000 + weightedMaximum/2) / weightedMaximum)
	if scoreBPS < rubric.MinimumScoreBPS {
		qualitativePass = false
	}
	status := "pass"
	if !qualitativePass {
		status = "fail"
	}
	result.Qualitative = &QualitativeAssessment{RubricID: rubric.ID, RubricSHA256: review.RubricSHA256, ResultSHA256: resultHash, FinalResponseSHA256: finalHash, Reviewer: review.Reviewer, Status: status, ScoreBPS: scoreBPS, Criteria: criteria, FindingIDs: findings}
	if !qualitativePass {
		result.Status = "fail"
		result.Violations = append(result.Violations, Violation{Code: "qualitative_review_failed", Subject: rubric.ID, Observed: int64(scoreBPS), Limit: int64(rubric.MinimumScoreBPS)})
		sort.Slice(result.Violations, func(i, j int) bool {
			if result.Violations[i].Code != result.Violations[j].Code {
				return result.Violations[i].Code < result.Violations[j].Code
			}
			return result.Violations[i].Subject < result.Violations[j].Subject
		})
	}
	return result, nil
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
