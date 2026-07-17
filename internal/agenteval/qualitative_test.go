package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func testRubric(scenarioID string) Rubric {
	return Rubric{SchemaVersion: 1, ID: "evidence-answer", ScenarioID: scenarioID, MinimumScoreBPS: 7000,
		Criteria: []RubricCriterion{
			{ID: "grounding", Description: "Claims remain grounded.", Maximum: 4, Minimum: 3, Weight: 3},
			{ID: "clarity", Description: "The answer is clear.", Maximum: 4, Minimum: 2, Weight: 1},
		}, AllowedFindingIDs: []string{"unsupported_claim", "verbose"}}
}

func TestAssessQualitativeBindsPrivateAnswerAndCannotOverrideDeterministicFailure(t *testing.T) {
	result, err := Evaluate(validScenario(), validObservation())
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, _ := json.Marshal(result)
	final := []byte(`{"answer":"synthetic"}`)
	rubric := testRubric(result.ScenarioID)
	review, err := NewReviewTemplate(result, resultBytes, final, rubric, Reviewer{Kind: "codex", Model: "gpt-test-1"})
	if err != nil {
		t.Fatal(err)
	}
	review.Criteria[0].Score = 4
	review.Criteria[1].Score = 3
	assessed, err := AssessQualitative(result, resultBytes, final, rubric, review)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.Status != "pass" || assessed.Qualitative == nil || assessed.Qualitative.ScoreBPS != 9375 {
		t.Fatalf("assessed=%+v", assessed)
	}
	encoded, _ := json.Marshal(assessed)
	if !bytes.Contains(encoded, []byte(`"finding_ids":[]`)) {
		t.Fatalf("empty findings must encode as an array: %s", encoded)
	}

	failed := result
	failed.Status = "fail"
	failed.Violations = []Violation{{Code: "required_check_failed", Subject: "answer_correct", Limit: 1}}
	failedBytes, _ := json.Marshal(failed)
	review, err = NewReviewTemplate(failed, failedBytes, final, rubric, Reviewer{Kind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	review.Criteria[0].Score = 4
	review.Criteria[1].Score = 4
	assessed, err = AssessQualitative(failed, failedBytes, final, rubric, review)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.Status != "fail" || assessed.Qualitative.Status != "pass" {
		t.Fatalf("qualitative review overrode deterministic failure: %+v", assessed)
	}
}

func TestAssessQualitativeFailsClosedOnLowCriterionAndHashDrift(t *testing.T) {
	result, err := Evaluate(validScenario(), validObservation())
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, _ := json.Marshal(result)
	final := []byte(`{"answer":"synthetic"}`)
	rubric := testRubric(result.ScenarioID)
	review, _ := NewReviewTemplate(result, resultBytes, final, rubric, Reviewer{Kind: "human"})
	review.Criteria[0].Score = 2
	review.Criteria[1].Score = 4
	assessed, err := AssessQualitative(result, resultBytes, final, rubric, review)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.Status != "fail" || assessed.Qualitative.Status != "fail" || len(assessed.Violations) == 0 {
		t.Fatalf("assessed=%+v", assessed)
	}
	if _, err := AssessQualitative(result, resultBytes, append(final, 'x'), rubric, review); err == nil {
		t.Fatal("changed final response passed hash binding")
	}
}

func TestAggregateSeparatesQualitativeReviewerAndReportsScores(t *testing.T) {
	base, _ := Evaluate(validScenario(), validObservation())
	baseBytes, _ := json.Marshal(base)
	final := []byte(`{"answer":"synthetic"}`)
	rubric := testRubric(base.ScenarioID)
	results := []Result{}
	for _, score := range []int{3, 4} {
		review, _ := NewReviewTemplate(base, baseBytes, final, rubric, Reviewer{Kind: "codex", Model: "gpt-test-1"})
		review.Criteria[0].Score, review.Criteria[1].Score = score, 4
		assessed, err := AssessQualitative(base, baseBytes, final, rubric, review)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, assessed)
	}
	aggregate, err := AggregateResults(results)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Groups) != 1 || aggregate.Groups[0].Qualitative == nil || aggregate.Groups[0].Qualitative.ScoreBPS.ObservedRuns != 2 || aggregate.Groups[0].Qualitative.Reviewer.Model != "gpt-test-1" || aggregate.Groups[0].Qualitative.RubricID != rubric.ID || aggregate.Groups[0].Qualitative.RubricSHA256 == "" {
		t.Fatalf("aggregate=%+v", aggregate)
	}
}

func TestNeutralQualitativeReviewRequiresAndBindsBlindAssignment(t *testing.T) {
	scenario := validScenario()
	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.RequiredSemanticChecks = []string{"answer_correct"}
	scenario.RequiredMetrics = []string{"interface_invocations", "backend_requests"}
	scenario.Budgets.MaxATLInvocations = 0
	scenario.Budgets.MaxInterfaceInvocations = 2
	observation := validObservation()
	observation.Surface = SurfaceATLMCP
	observation.Metrics.InterfaceInvocations = observation.Metrics.ATLInvocations
	observation.Metrics.ATLInvocations = 0
	delete(observation.Coverage, "atl_invocations")
	observation.Coverage["interface_invocations"] = true
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, _ := json.Marshal(result)
	final := []byte(`{"answer":"synthetic"}`)
	rubric := testRubric(result.ScenarioID)
	if _, err := NewReviewTemplate(result, resultBytes, final, rubric, Reviewer{Kind: "human"}); err == nil || !strings.Contains(err.Error(), "blind") {
		t.Fatalf("unblinded neutral review passed: %v", err)
	}
	assignment := []byte("candidate B maps to an answer hidden from the reviewer")
	review, err := NewReviewTemplate(result, resultBytes, final, rubric, Reviewer{Kind: "human"}, assignment)
	if err != nil {
		t.Fatal(err)
	}
	if !review.Blinded || review.AssignmentDigest != sha256Hex(assignment) {
		t.Fatalf("review=%+v", review)
	}
	encoded, _ := json.Marshal(review)
	if bytes.Contains(encoded, assignment) {
		t.Fatalf("blind assignment content persisted: %s", encoded)
	}
	review.Criteria[0].Score = 4
	review.Criteria[1].Score = 4
	assessed, err := AssessQualitative(result, resultBytes, final, rubric, review)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.Qualitative == nil || !assessed.Qualitative.Blinded || assessed.Qualitative.AssignmentDigest != review.AssignmentDigest {
		t.Fatalf("assessment=%+v", assessed.Qualitative)
	}

	review.Blinded = false
	review.AssignmentDigest = ""
	if _, err := AssessQualitative(result, resultBytes, final, rubric, review); err == nil {
		t.Fatal("neutral assessment accepted stripped blind metadata")
	}
}
