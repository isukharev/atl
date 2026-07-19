package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func panelPolicy(maxRange int) QualitativePanelPolicy {
	return QualitativePanelPolicy{SchemaVersion: 1, Method: QualitativePanelMethod, ExpectedReviewers: 3, MaxCriterionRangeBPS: maxRange}
}

func panelReviews(t *testing.T, result Result, resultBytes, final []byte, rubric Rubric, scores [][2]int, assignment []byte) []Review {
	t.Helper()
	reviews := make([]Review, 0, len(scores))
	for index, values := range scores {
		reviewer := Reviewer{ID: "judge-" + string(rune('a'+index)), Kind: "codex", Model: "gpt-test-1"}
		var (
			review Review
			err    error
		)
		if assignment == nil {
			review, err = NewReviewTemplate(result, resultBytes, final, rubric, reviewer)
		} else {
			review, err = NewReviewTemplate(result, resultBytes, final, rubric, reviewer, assignment)
		}
		if err != nil {
			t.Fatal(err)
		}
		review.Criteria[0].Score = values[0]
		review.Criteria[1].Score = values[1]
		reviews = append(reviews, review)
	}
	return reviews
}

func panelFixture(t *testing.T) (Result, []byte, []byte, Rubric) {
	t.Helper()
	result, err := Evaluate(validScenario(), validObservation())
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return result, resultBytes, []byte(`{"answer":"synthetic"}`), testRubric(result.ScenarioID)
}

func TestAssessQualitativeReviewSetIsPermutationDeterministicAndAuditable(t *testing.T) {
	result, resultBytes, final, rubric := panelFixture(t)
	reviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{4, 3}, {3, 3}, {4, 4}}, nil)
	assessed, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(2500), reviews)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.Status != "pass" || assessed.Qualitative != nil || assessed.QualitativeReviewSet == nil {
		t.Fatalf("assessment=%+v", assessed)
	}
	set := assessed.QualitativeReviewSet
	if set.Status != "pass" || set.ScoreBPS != 9375 || set.Criteria[0].Score != 4 || set.Criteria[0].RangeBPS != 2500 {
		t.Fatalf("review set=%+v", set)
	}
	if set.Members[0].Reviewer.ID != "judge-a" || len(set.Members[0].Criteria) != len(rubric.Criteria) {
		t.Fatalf("members are not stable and auditable: %+v", set.Members)
	}
	if err := assessed.Validate(); err != nil {
		t.Fatalf("generated assessment is invalid: %v", err)
	}
	if assessed.Violations == nil {
		t.Fatal("passing assessment changed empty violations from [] to null")
	}
	requireResultJSONRoundTrip(t, assessed)

	permuted := []Review{reviews[2], reviews[0], reviews[1]}
	second, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(2500), permuted)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(assessed)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("panel order changed assessment\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}
}

func TestAssessQualitativeReviewSetRejectsBindingAndIdentityDrift(t *testing.T) {
	result, resultBytes, final, rubric := panelFixture(t)
	reviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{4, 4}, {4, 4}, {4, 4}}, nil)
	if _, err := AssessQualitativeReviewSet(result, resultBytes, append(final, 'x'), rubric, panelPolicy(9999), reviews); err == nil || !strings.Contains(err.Error(), "hashes") {
		t.Fatalf("final-response drift passed: %v", err)
	}
	driftedRubric := rubric
	driftedRubric.MinimumScoreBPS++
	if _, err := AssessQualitativeReviewSet(result, resultBytes, final, driftedRubric, panelPolicy(9999), reviews); err == nil || !strings.Contains(err.Error(), "rubric") {
		t.Fatalf("rubric drift passed: %v", err)
	}
	reviews[1].Reviewer.ID = reviews[0].Reviewer.ID
	if _, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9999), reviews); err == nil || !strings.Contains(err.Error(), "reviewer id") {
		t.Fatalf("duplicate reviewer passed: %v", err)
	}

	reviews = panelReviews(t, result, resultBytes, final, rubric, [][2]int{{4, 4}, {4, 4}, {4, 4}}, nil)
	assessed, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9999), reviews)
	if err != nil {
		t.Fatal(err)
	}
	assessed.QualitativeReviewSet.Members[1].ReviewSHA256 = assessed.QualitativeReviewSet.Members[0].ReviewSHA256
	if err := assessed.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate retained review digest passed: %v", err)
	}
}

func TestQualitativePanelPolicyBoundsAndFiveReviewerMedian(t *testing.T) {
	for _, policy := range []QualitativePanelPolicy{
		{SchemaVersion: 1, Method: QualitativePanelMethod, ExpectedReviewers: 4, MaxCriterionRangeBPS: 2500},
		{SchemaVersion: 1, Method: QualitativePanelMethod, ExpectedReviewers: 3, MaxCriterionRangeBPS: 0},
		{SchemaVersion: 1, Method: QualitativePanelMethod, ExpectedReviewers: 3, MaxCriterionRangeBPS: 10000},
		{SchemaVersion: 1, Method: "other", ExpectedReviewers: 3, MaxCriterionRangeBPS: 2500},
	} {
		if err := policy.Validate(); err == nil {
			t.Fatalf("invalid policy passed: %+v", policy)
		}
	}
	result, resultBytes, final, rubric := panelFixture(t)
	reviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{3, 4}, {3, 4}, {4, 4}, {4, 4}, {4, 4}}, nil)
	policy := QualitativePanelPolicy{SchemaVersion: 1, Method: QualitativePanelMethod, ExpectedReviewers: 5, MaxCriterionRangeBPS: 2500}
	assessed, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, policy, reviews)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.QualitativeReviewSet.Status != "pass" || assessed.QualitativeReviewSet.Criteria[0].Score != 4 {
		t.Fatalf("five-reviewer median=%+v", assessed.QualitativeReviewSet)
	}
}

func TestAssessQualitativeReviewSetRequiresOneNeutralBlindAssignment(t *testing.T) {
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
	assignment := []byte("blind assignment without candidate content")
	reviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{4, 4}, {4, 4}, {4, 4}}, assignment)
	reviews[2].AssignmentDigest = strings.Repeat("a", 64)
	if _, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9999), reviews); err == nil || !strings.Contains(err.Error(), "same blind assignment") {
		t.Fatalf("blind mismatch passed: %v", err)
	}
}

func TestAssessQualitativeReviewSetDisagreementAndFailureModes(t *testing.T) {
	result, resultBytes, final, rubric := panelFixture(t)

	boundarySplit := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{2, 4}, {3, 4}, {4, 4}}, nil)
	assessed, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9999), boundarySplit)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.QualitativeReviewSet.Status != "disagreement" || assessed.Status != "fail" || assessed.Violations[len(assessed.Violations)-1].Code != "qualitative_review_disagreement" {
		t.Fatalf("boundary split=%+v", assessed)
	}
	requireResultJSONRoundTrip(t, assessed)

	rangeSplit := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{3, 4}, {4, 4}, {4, 4}}, nil)
	assessed, err = AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(2499), rangeSplit)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.QualitativeReviewSet.Status != "disagreement" {
		t.Fatalf("range above threshold did not disagree: %+v", assessed.QualitativeReviewSet)
	}
	requireResultJSONRoundTrip(t, assessed)

	allFail := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{2, 1}, {2, 1}, {2, 1}}, nil)
	assessed, err = AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9999), allFail)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.QualitativeReviewSet.Status != "fail" || assessed.Violations[len(assessed.Violations)-1].Code != "qualitative_review_failed" {
		t.Fatalf("unanimous failure=%+v", assessed)
	}
	requireResultJSONRoundTrip(t, assessed)
}

func TestAssessQualitativeReviewSetPreservesDeterministicViolationsWithoutMutatingInput(t *testing.T) {
	result, _, final, rubric := panelFixture(t)
	deterministic := []Violation{
		{Code: "alpha_failure", Subject: "first_check", Observed: 2, Limit: 1},
		{Code: "zeta_failure", Subject: "second_check", Observed: 3, Limit: 2},
	}
	backing := make([]Violation, len(deterministic), len(deterministic)+2)
	copy(backing, deterministic)
	result.Status = "fail"
	result.Violations = backing
	inputJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	backingBefore := append([]Violation(nil), backing[:cap(backing)]...)
	reviews := panelReviews(t, result, inputJSON, final, rubric, [][2]int{{2, 1}, {2, 1}, {2, 1}}, nil)

	assessed, err := AssessQualitativeReviewSet(result, inputJSON, final, rubric, panelPolicy(9999), reviews)
	if err != nil {
		t.Fatal(err)
	}
	inputAfter, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(inputAfter, inputJSON) {
		t.Fatalf("assessment mutated input result\nbefore=%s\nafter=%s", inputJSON, inputAfter)
	}
	backingAfter := backing[:cap(backing)]
	if !equalViolationSlices(backingAfter, backingBefore) {
		t.Fatalf("assessment mutated input violation backing array\nbefore=%+v\nafter=%+v", backingBefore, backingAfter)
	}

	want := append([]Violation(nil), deterministic...)
	want = append(want, Violation{Code: "qualitative_review_failed", Subject: rubric.ID,
		Observed: int64(assessed.QualitativeReviewSet.ScoreBPS), Limit: int64(rubric.MinimumScoreBPS)})
	if len(assessed.Violations) != len(want) {
		t.Fatalf("violations=%+v", assessed.Violations)
	}
	for _, violation := range want {
		count := 0
		for _, actual := range assessed.Violations {
			if actual == violation {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("violation %+v appears %d times in %+v", violation, count, assessed.Violations)
		}
	}

	encoded, err := json.Marshal(assessed)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResult(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("encoded assessment did not decode: %v\n%s", err, encoded)
	}
	roundTrip, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundTrip, encoded) {
		t.Fatalf("result did not round-trip\nencoded=%s\ndecoded=%s", encoded, roundTrip)
	}
}

func equalViolationSlices(left, right []Violation) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func requireResultJSONRoundTrip(t *testing.T, result Result) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResult(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("encoded assessment did not decode: %v\n%s", err, encoded)
	}
	roundTrip, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundTrip, encoded) {
		t.Fatalf("result did not round-trip\nencoded=%s\ndecoded=%s", encoded, roundTrip)
	}
}

func TestAssessQualitativeReviewSetIndividualStatusSplitAndDeterministicFailure(t *testing.T) {
	result, resultBytes, final, rubric := panelFixture(t)
	rubric.MinimumScoreBPS = 9000
	reviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{3, 4}, {4, 2}, {4, 4}}, nil)
	assessed, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9999), reviews)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.QualitativeReviewSet.Status != "disagreement" {
		t.Fatalf("individual pass split was not disagreement: %+v", assessed.QualitativeReviewSet)
	}

	base, _, final, rubric := panelFixture(t)
	base.Status = "fail"
	base.Violations = []Violation{{Code: "required_check_failed", Subject: "answer_correct", Limit: 1}}
	baseBytes, _ := json.Marshal(base)
	reviews = panelReviews(t, base, baseBytes, final, rubric, [][2]int{{4, 4}, {4, 4}, {4, 4}}, nil)
	assessed, err = AssessQualitativeReviewSet(base, baseBytes, final, rubric, panelPolicy(9999), reviews)
	if err != nil {
		t.Fatal(err)
	}
	if assessed.QualitativeReviewSet.Status != "pass" || assessed.Status != "fail" {
		t.Fatalf("panel overrode deterministic failure: %+v", assessed)
	}
}

func TestResultRejectsMutuallyExclusiveQualitativeContracts(t *testing.T) {
	result, resultBytes, final, rubric := panelFixture(t)
	reviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{4, 4}, {4, 4}, {4, 4}}, nil)
	assessed, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9999), reviews)
	if err != nil {
		t.Fatal(err)
	}
	assessed.Qualitative = &QualitativeAssessment{}
	if err := assessed.Validate(); err == nil || !strings.Contains(err.Error(), "both legacy and panel") {
		t.Fatalf("mutually exclusive assessments passed: %v", err)
	}
}

func TestPanelSchemasPreserveOnlyExplicitLegacyReadCompatibility(t *testing.T) {
	result, resultBytes, final, rubric := panelFixture(t)
	legacyResult := result
	legacyResult.SchemaVersion = LegacyResultSchemaVersion
	legacyBytes, _ := json.Marshal(legacyResult)
	if _, err := DecodeResult(bytes.NewReader(legacyBytes)); err != nil {
		t.Fatalf("legacy singleton-capable result was not readable: %v", err)
	}
	reviews := panelReviews(t, legacyResult, legacyBytes, final, rubric, [][2]int{{4, 4}, {4, 4}, {4, 4}}, nil)
	assessed, err := AssessQualitativeReviewSet(legacyResult, legacyBytes, final, rubric, panelPolicy(9999), reviews)
	if err != nil || assessed.SchemaVersion != ResultSchemaVersion {
		t.Fatalf("panel did not upgrade result schema: version=%d err=%v", assessed.SchemaVersion, err)
	}
	assessed.SchemaVersion = LegacyResultSchemaVersion
	if err := assessed.Validate(); err == nil {
		t.Fatal("legacy result schema accepted a panel field")
	}
	review, err := NewReviewTemplate(result, resultBytes, final, rubric, Reviewer{Kind: "human"})
	if err != nil {
		t.Fatal(err)
	}
	review.SchemaVersion = LegacyReviewSchemaVersion
	if err := review.Validate(); err != nil {
		t.Fatalf("legacy reviewer-id-free packet was not readable: %v", err)
	}
	review.Reviewer.ID = "reviewer-01"
	if err := review.Validate(); err == nil {
		t.Fatal("legacy review schema accepted a reviewer id")
	}
}

func TestAggregateQualitativeReviewSetSeparatesContractsAndCountsConsensus(t *testing.T) {
	result, resultBytes, final, rubric := panelFixture(t)
	passingReviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{4, 4}, {4, 4}, {4, 4}}, nil)
	passing, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9999), passingReviews)
	if err != nil {
		t.Fatal(err)
	}
	disagreeingReviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{2, 4}, {3, 4}, {4, 4}}, nil)
	disagreeing, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9998), disagreeingReviews)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := AggregateResults([]Result{passing, disagreeing})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Groups) != 2 {
		t.Fatalf("panel contracts were not separated: %+v", aggregate)
	}
	disagreements := 0
	for _, group := range aggregate.Groups {
		if group.Runs != 1 || group.QualitativeReviewSet == nil || group.QualitativeReviewSet.ScoreBPS.ObservedRuns != 1 {
			t.Fatalf("members were counted as runs or consensus omitted: %+v", group)
		}
		disagreements += group.QualitativeReviewSet.Disagreements
	}
	if disagreements != 1 {
		t.Fatalf("aggregate disagreement count=%d: %+v", disagreements, aggregate)
	}
}

func TestAggregateQualitativeReviewSetSeparatesBlindAssignments(t *testing.T) {
	result, resultBytes, final, rubric := panelFixture(t)
	firstReviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{4, 4}, {4, 4}, {4, 4}}, []byte("blind mapping a"))
	first, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9999), firstReviews)
	if err != nil {
		t.Fatal(err)
	}
	secondReviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{4, 4}, {4, 4}, {4, 4}}, []byte("blind mapping b"))
	second, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9999), secondReviews)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := AggregateResults([]Result{first, second})
	if err != nil || len(aggregate.Groups) != 2 {
		t.Fatalf("blind assignments were merged: groups=%+v err=%v", aggregate.Groups, err)
	}
	encoded, _ := json.Marshal(aggregate)
	if bytes.Contains(encoded, []byte(first.QualitativeReviewSet.AssignmentDigest)) || bytes.Contains(encoded, []byte(second.QualitativeReviewSet.AssignmentDigest)) {
		t.Fatalf("private assignment digest was published: %s", encoded)
	}
}

func TestAggregateQualitativeReviewSetFiltersDeterministicFailuresButKeepsDisagreementEfficiency(t *testing.T) {
	result, _, final, rubric := panelFixture(t)
	result.Category = BenchmarkCategorySurfaceNative
	resultBytes, _ := json.Marshal(result)
	disagreeingReviews := panelReviews(t, result, resultBytes, final, rubric, [][2]int{{2, 4}, {3, 4}, {4, 4}}, nil)
	disagreeing, err := AssessQualitativeReviewSet(result, resultBytes, final, rubric, panelPolicy(9998), disagreeingReviews)
	if err != nil {
		t.Fatal(err)
	}
	deterministicFailure := result
	deterministicFailure.Status = "fail"
	deterministicFailure.Violations = []Violation{{Code: "required_check_failed", Subject: "answer_correct", Limit: 1}}
	failedBytes, _ := json.Marshal(deterministicFailure)
	passingReviews := panelReviews(t, deterministicFailure, failedBytes, final, rubric, [][2]int{{4, 4}, {4, 4}, {4, 4}}, nil)
	failed, err := AssessQualitativeReviewSet(deterministicFailure, failedBytes, final, rubric, panelPolicy(9998), passingReviews)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := AggregateResults([]Result{disagreeing, failed})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Groups) != 1 || aggregate.Groups[0].QualitativeReviewSet == nil {
		t.Fatalf("aggregate=%+v", aggregate)
	}
	group := aggregate.Groups[0]
	if group.QualitativeReviewSet.ScoreBPS.ObservedRuns != 1 || group.QualitativeReviewSet.Disagreements != 1 || group.Metrics.ATLInvocations.ObservedRuns != 1 {
		t.Fatalf("deterministic filtering or disagreement efficiency is wrong: %+v", group)
	}
}
