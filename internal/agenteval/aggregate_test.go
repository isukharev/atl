package agenteval

import "testing"

func TestAggregateResultsGroupsComparableRunsAndUsesNearestRank(t *testing.T) {
	results := make([]Result, 0, 5)
	for index, turns := range []int{2, 3, 4, 5, 20} {
		observation := validObservation()
		observation.Metrics.AgentTurns = turns
		scenario := validScenario()
		scenario.Budgets.MaxAgentTurns = 20
		result, err := Evaluate(scenario, observation)
		if err != nil {
			t.Fatal(err)
		}
		if index == 4 {
			result.Status = "fail"
			result.Violations = []Violation{{Code: "oracle_failed", Subject: "answer_correct"}}
		}
		results = append(results, result)
	}
	aggregate, err := AggregateResults(results)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Groups) != 1 {
		t.Fatalf("groups=%+v", aggregate.Groups)
	}
	group := aggregate.Groups[0]
	if group.Runs != 5 || group.Passes != 4 || group.SuccessRate != 0.8 {
		t.Fatalf("group=%+v", group)
	}
	if group.Metrics.AgentTurns.P50 != 4 || group.Metrics.AgentTurns.P90 != 20 {
		t.Fatalf("turn quantiles=%+v", group.Metrics.AgentTurns)
	}
}

func TestAggregateResultsSeparatesRuntimeAndVariant(t *testing.T) {
	first, err := Evaluate(validScenario(), validObservation())
	if err != nil {
		t.Fatal(err)
	}
	observation := validObservation()
	observation.Variant = "candidate"
	second, err := Evaluate(validScenario(), observation)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := AggregateResults([]Result{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Groups) != 2 || aggregate.Groups[0].Variant != "baseline" || aggregate.Groups[1].Variant != "candidate" {
		t.Fatalf("groups=%+v", aggregate.Groups)
	}
}
