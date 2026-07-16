package agenteval

import "testing"

func TestAggregateResultsGroupsComparableRunsAndUsesNearestRank(t *testing.T) {
	results := make([]Result, 0, 5)
	for index, turns := range []int{2, 3, 4, 5, 20} {
		observation := validObservation()
		observation.Metrics.AgentTurns = turns
		observation.Coverage["agent_turns"] = true
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

func TestAggregateResultsDoesNotTreatUnavailableMetricAsZero(t *testing.T) {
	first, err := Evaluate(validScenario(), validObservation())
	if err != nil {
		t.Fatal(err)
	}
	scenario := validScenario()
	scenario.RequiredMetrics = []string{"atl_invocations", "backend_requests"}
	observation := validObservation()
	delete(observation.Coverage, "output_bytes")
	observation.Metrics.OutputBytes = 0
	second, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := AggregateResults([]Result{first, second})
	if err != nil {
		t.Fatal(err)
	}
	metric := aggregate.Groups[0].Metrics.OutputBytes
	if metric.ObservedRuns != 1 || metric.P50 != first.Metrics.OutputBytes {
		t.Fatalf("output bytes=%+v", metric)
	}
}

func TestAggregateResultsSeparatesMainThreadAndTotalTokens(t *testing.T) {
	scenario := validScenario()
	scenario.RequiredMetrics = append(scenario.RequiredMetrics, "input_tokens", "main_thread_input_tokens")
	scenario.Budgets.MaxInputTokens = 500
	scenario.Budgets.MaxMainThreadInputTokens = 200
	observation := validObservation()
	observation.Metrics.InputTokens = 400
	observation.Metrics.MainThreadInputTokens = 100
	observation.Coverage["input_tokens"] = true
	observation.Coverage["main_thread_input_tokens"] = true
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := AggregateResults([]Result{result})
	if err != nil {
		t.Fatal(err)
	}
	metrics := aggregate.Groups[0].Metrics
	if metrics.InputTokens.P50 != 400 || metrics.MainThreadInputTokens.P50 != 100 {
		t.Fatalf("metrics=%+v", metrics)
	}
}

func TestAggregateResultsAttributesCapabilityFamiliesOnlyAcrossCoveredRuns(t *testing.T) {
	scenario := validScenario()
	first := validObservation()
	first.Coverage["capability_families"] = true
	first.CapabilityFamilies = []CapabilityFamilyMetric{{Family: "jira.fields", Invocations: 1, Successes: 1, OutputBytes: 100}}
	second := validObservation()
	second.Coverage["capability_families"] = true
	second.CapabilityFamilies = []CapabilityFamilyMetric{{Family: "jira.epic.digest", Invocations: 1, Failures: 1, OutputBytes: 200}}
	third := validObservation()
	results := make([]Result, 0, 3)
	for _, observation := range []Observation{first, second, third} {
		result, err := Evaluate(scenario, observation)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
	}
	aggregate, err := AggregateResults(results)
	if err != nil {
		t.Fatal(err)
	}
	families := aggregate.Groups[0].CapabilityFamilies
	if len(families) != 2 || families[0].Family != "jira.epic.digest" || families[0].Invocations.ObservedRuns != 2 || families[0].Failures.P90 != 1 || families[1].Family != "jira.fields" || families[1].OutputBytes.P90 != 100 {
		t.Fatalf("families=%+v", families)
	}
}
