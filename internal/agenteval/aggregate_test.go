package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

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

func TestAggregateResultsSeparatesSkillActivationWithoutPublishingPromptHash(t *testing.T) {
	makeResult := func(activation, digest string) Result {
		observation := validObservation()
		observation.Surface = SurfaceCLISkill
		observation.Runtime.Provider = "codex"
		observation.Runtime.SkillActivation = activation
		observation.Runtime.PromptContractSHA256 = digest
		scenario := validScenario()
		scenario.DataClass = "private-local"
		result, err := Evaluate(scenario, observation)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	arms := []struct {
		activation string
		digest     string
	}{
		{SkillActivationImplicit, strings.Repeat("a", 64)},
		{SkillActivationExplicit, strings.Repeat("b", 64)},
		{SkillActivationDeveloper, strings.Repeat("c", 64)},
		{SkillActivationCombined, strings.Repeat("d", 64)},
	}
	results := make([]Result, 0, len(arms))
	for _, arm := range arms {
		results = append(results, makeResult(arm.activation, arm.digest))
	}
	aggregate, err := AggregateResults(results)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{SkillActivationCombined, SkillActivationDeveloper, SkillActivationExplicit, SkillActivationImplicit}
	if len(aggregate.Groups) != len(want) {
		t.Fatalf("groups=%+v", aggregate.Groups)
	}
	for index, activation := range want {
		if aggregate.Groups[index].Runtime.SkillActivation != activation || aggregate.Groups[index].Runtime.PromptContractSHA256 != "" {
			t.Fatalf("group %d=%+v", index, aggregate.Groups[index])
		}
	}
	encoded, err := json.Marshal(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "prompt_contract_sha256") {
		t.Fatalf("aggregate published private prompt identity: %s", encoded)
	}
	for _, arm := range arms {
		if strings.Contains(string(encoded), arm.digest) {
			t.Fatalf("aggregate published private prompt identity: %s", encoded)
		}
	}
}

func TestAggregateResultsRejectsHiddenPromptContractDrift(t *testing.T) {
	makeResult := func(digest string) Result {
		observation := validObservation()
		observation.Surface = SurfaceCLISkill
		observation.Runtime.Provider = "codex"
		observation.Runtime.SkillActivation = SkillActivationImplicit
		observation.Runtime.PromptContractSHA256 = digest
		scenario := validScenario()
		scenario.DataClass = "private-local"
		result, err := Evaluate(scenario, observation)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := makeResult(strings.Repeat("a", 64))
	second := makeResult(strings.Repeat("b", 64))
	if _, err := AggregateResults([]Result{first, second}); err == nil || !strings.Contains(err.Error(), "prompt contract") {
		t.Fatalf("hidden prompt drift err=%v", err)
	}
}

func TestResultPromptIdentityKeepsExplicitLegacySchemasReadableAndCurrentFailClosed(t *testing.T) {
	legacy, err := Evaluate(validScenario(), validObservation())
	if err != nil {
		t.Fatal(err)
	}
	legacy.SchemaVersion = PanelResultSchemaVersion
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeResult(strings.NewReader(string(encoded))); err != nil {
		t.Fatalf("legacy v4 result became unreadable: %v", err)
	}
	legacy.Runtime.SkillActivation = SkillActivationImplicit
	legacy.Runtime.PromptContractSHA256 = strings.Repeat("a", 64)
	if err := legacy.Validate(); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("pre-prompt result accepted prompt identity: %v", err)
	}

	current := legacy
	current.SchemaVersion = ResultSchemaVersion
	current.DataClass = "private-local"
	current.Surface = SurfaceCLISkill
	current.Runtime.Provider = "codex"
	current.Runtime.SkillActivation = ""
	current.Runtime.PromptContractSHA256 = ""
	if err := current.Validate(); err == nil || !strings.Contains(err.Error(), "requires prompt contract") {
		t.Fatalf("current result missed activation identity: %v", err)
	}

	promptBound := current
	promptBound.SchemaVersion = LegacyPromptBoundResultSchemaVersion
	promptBound.Runtime.SkillActivation = SkillActivationImplicit
	promptBound.Runtime.PromptContractSHA256 = strings.Repeat("b", 64)
	encoded, err = json.Marshal(promptBound)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeResult(strings.NewReader(string(encoded))); err != nil {
		t.Fatalf("legacy prompt-bound v5 result became unreadable: %v", err)
	}
	promptBound.Runtime.SkillActivation = SkillActivationDeveloper
	if err := promptBound.Validate(); err == nil || !strings.Contains(err.Error(), "legacy prompt-bound") {
		t.Fatalf("legacy v5 accepted a new activation arm: %v", err)
	}

	current.Runtime.SkillActivation = SkillActivationDeveloper
	current.Runtime.PromptContractSHA256 = strings.Repeat("c", 64)
	if err := current.Validate(); err != nil {
		t.Fatalf("current result rejected developer activation: %v", err)
	}
	current.SchemaVersion = ResultSchemaVersion + 1
	if err := current.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported result schema_version") {
		t.Fatalf("future result schema passed: %v", err)
	}
}

func TestResultPromptIdentityAllowsOnlyCurrentSyntheticContracts(t *testing.T) {
	result, err := Evaluate(validScenario(), validObservation())
	if err != nil {
		t.Fatal(err)
	}
	result.EvidenceAttempt, err = NewEvidenceAttemptTelemetry(true, EvidenceAttemptCounts{Attempts: 1, Admitted: 1, Succeeded: 1})
	if err != nil {
		t.Fatal(err)
	}
	result.EvidenceReport = EvidenceOutcomeReport{Coverage: true, State: EvidenceAttemptStateSucceeded}
	result.Runtime.PromptContractSHA256 = strings.Repeat("a", 64)
	if err := result.Validate(); err != nil {
		t.Fatalf("current synthetic result rejected prompt identity: %v", err)
	}
	invalidActivation := result
	invalidActivation.Runtime.SkillActivation = SkillActivationImplicit
	if err := invalidActivation.Validate(); err == nil || !strings.Contains(err.Error(), "cannot claim") {
		t.Fatalf("synthetic result accepted activation treatment: %v", err)
	}
	aggregate, err := AggregateResults([]Result{result})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(result.Runtime.PromptContractSHA256)) || bytes.Contains(encoded, []byte("prompt_contract_sha256")) {
		t.Fatalf("aggregate exposed synthetic prompt identity: %s", encoded)
	}
	drifted := result
	drifted.Runtime.PromptContractSHA256 = strings.Repeat("b", 64)
	if _, err := AggregateResults([]Result{result, drifted}); err == nil || !strings.Contains(err.Error(), "prompt contract") {
		t.Fatalf("synthetic prompt drift passed: %v", err)
	}
	legacy := result
	legacy.SchemaVersion = LegacyEvidenceResultSchemaVersion
	if err := legacy.Validate(); err == nil || !strings.Contains(err.Error(), "outside private codex cli-skill") {
		t.Fatalf("legacy synthetic result accepted prompt identity: %v", err)
	}
	legacy.Runtime.PromptContractSHA256 = ""
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy promptless synthetic result became unreadable: %v", err)
	}
}

func TestAggregateResultsSeparatesBenchmarkCategoryAndSurface(t *testing.T) {
	baseScenario := validScenario()
	baseObservation := validObservation()
	baseObservation.Surface = SurfaceCLISkill
	cli, err := Evaluate(baseScenario, baseObservation)
	if err != nil {
		t.Fatal(err)
	}
	mcpObservation := validObservation()
	mcpObservation.Surface = SurfaceATLMCP
	mcp, err := Evaluate(baseScenario, mcpObservation)
	if err != nil {
		t.Fatal(err)
	}
	neutralScenario := validScenario()
	neutralScenario.Category = BenchmarkCategoryNeutralCommon
	neutralScenario.RequiredSemanticChecks = []string{"answer_correct"}
	neutralScenario.RequiredMetrics = []string{"interface_invocations", "backend_requests", "output_bytes"}
	neutralScenario.Budgets.MaxInterfaceInvocations = neutralScenario.Budgets.MaxATLInvocations
	neutralScenario.Budgets.MaxATLInvocations = 0
	neutralObservation := validObservation()
	neutralObservation.Surface = SurfaceCLISkill
	neutralObservation.Metrics.InterfaceInvocations = neutralObservation.Metrics.ATLInvocations
	neutralObservation.Metrics.ATLInvocations = 0
	neutralObservation.Coverage["interface_invocations"] = true
	delete(neutralObservation.Coverage, "atl_invocations")
	neutral, err := Evaluate(neutralScenario, neutralObservation)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := AggregateResults([]Result{neutral, mcp, cli})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Groups) != 3 {
		t.Fatalf("groups=%+v", aggregate.Groups)
	}
	identities := map[string]bool{}
	for _, group := range aggregate.Groups {
		identities[group.Category+"/"+group.Surface] = true
	}
	for _, want := range []string{
		BenchmarkCategoryRouteFixed + "/" + SurfaceCLISkill,
		BenchmarkCategoryRouteFixed + "/" + SurfaceATLMCP,
		BenchmarkCategoryNeutralCommon + "/" + SurfaceCLISkill,
	} {
		if !identities[want] {
			t.Fatalf("missing %s in %+v", want, aggregate.Groups)
		}
	}
}

func TestAggregateResultsIncludesGenericInterfaceInvocations(t *testing.T) {
	scenario := validScenario()
	scenario.RequiredMetrics = []string{"interface_invocations", "backend_requests"}
	scenario.Budgets.MaxInterfaceInvocations = 3
	observation := validObservation()
	delete(observation.Coverage, "atl_invocations")
	observation.Metrics.ATLInvocations = 0
	observation.Coverage["interface_invocations"] = true
	observation.Metrics.InterfaceInvocations = 3
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := AggregateResults([]Result{result})
	if err != nil {
		t.Fatal(err)
	}
	metric := aggregate.Groups[0].Metrics.InterfaceInvocations
	if metric.ObservedRuns != 1 || metric.P50 != 3 {
		t.Fatalf("interface invocations=%+v", metric)
	}
}

func TestAggregateResultsReportsEligibilityAndFiltersNeutralEfficiency(t *testing.T) {
	scenario := validScenario()
	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.RequiredSemanticChecks = []string{"answer_correct"}
	scenario.RequiredMetrics = []string{"interface_invocations", "backend_requests"}
	scenario.Budgets.MaxInterfaceInvocations = 4
	scenario.Budgets.MaxATLInvocations = 0
	makeObservation := func(eligibility string, invocations int) Observation {
		o := validObservation()
		o.Surface = SurfaceCLISkill
		o.Eligibility = eligibility
		o.Metrics.ATLInvocations = 0
		o.Metrics.InterfaceInvocations = invocations
		delete(o.Coverage, "atl_invocations")
		o.Coverage["interface_invocations"] = true
		if eligibility == EligibilityUnsupportedCapability {
			o.UnavailableCapabilities = []string{"jira.epic.digest"}
		}
		return o
	}
	observations := []Observation{
		makeObservation(EligibilitySupported, 2),
		makeObservation(EligibilityUnsupportedCapability, 4),
		makeObservation(EligibilityInvalidatedDrift, 3),
	}
	results := make([]Result, 0, len(observations))
	for _, observation := range observations {
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
	group := aggregate.Groups[0]
	if group.Runs != 3 || group.EligibleRuns != 1 || group.UnsupportedRuns != 1 || group.DriftedRuns != 1 || group.CoverageRate != 0.5 || group.SuccessRate != 1 {
		t.Fatalf("group=%+v", group)
	}
	if group.Metrics.InterfaceInvocations.ObservedRuns != 1 || group.Metrics.InterfaceInvocations.P50 != 2 {
		t.Fatalf("ineligible efficiency leaked into quantiles: %+v", group.Metrics.InterfaceInvocations)
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
