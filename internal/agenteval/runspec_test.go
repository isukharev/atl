package agenteval

import (
	"encoding/json"
	"strings"
	"testing"
)

func validRunSpec() RunSpec {
	return RunSpec{
		SchemaVersion: RunSpecSchemaVersion, ScenarioFile: "scenario.json",
		Provider: "codex", Variant: "baseline", Model: "gpt-test-1",
		PromptFile: "prompt.md", ResponseSchemaFile: "response.json",
		WorkspaceTemplate: "workspace", FixtureFile: "fixture.json",
		Repetitions: 3, TimeoutSeconds: 300, MaxEstimatedCostMicroUSD: 10_000_000,
		Pricing:      Pricing{InputMicroUSDPerMillionTokens: 1_000_000, OutputMicroUSDPerMillionTokens: 2_000_000},
		AllowedTools: []string{"Bash(atl *)"},
		Checks: []RunCheck{
			{Name: "answer_correct", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`"ok"`)},
			{Name: "used_atl", Kind: "atl_invocations_min", Minimum: 1},
		},
	}
}

func TestRunSpecFailsClosedOnCostPathsAndOracle(t *testing.T) {
	for name, mutate := range map[string]func(*RunSpec){
		"cost":    func(spec *RunSpec) { spec.MaxEstimatedCostMicroUSD = maxRunCostMicroUSD + 1 },
		"path":    func(spec *RunSpec) { spec.PromptFile = "../private" },
		"oracle":  func(spec *RunSpec) { spec.Checks[0].Expected = json.RawMessage(`no`) },
		"pricing": func(spec *RunSpec) { spec.Pricing = Pricing{} },
	} {
		t.Run(name, func(t *testing.T) {
			spec := validRunSpec()
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("invalid run spec passed")
			}
		})
	}
}

func TestRunSpecRequiresScenarioOracleAndCostBoundary(t *testing.T) {
	scenario := validScenario()
	scenario.RequiredChecks = []string{"answer_correct", "used_atl"}
	scenario.Budgets.MaxEstimatedCostMicroUSD = 2_000_000
	spec := validRunSpec()
	if err := spec.ValidateAgainstScenario(scenario); err == nil || !strings.Contains(err.Error(), "cost cap") {
		t.Fatalf("err=%v", err)
	}
	spec.MaxEstimatedCostMicroUSD = 6_000_000
	spec.Checks = spec.Checks[:1]
	if err := spec.ValidateAgainstScenario(scenario); err == nil || !strings.Contains(err.Error(), "used_atl") {
		t.Fatalf("err=%v", err)
	}
}

func TestEvaluateRunChecksUsesStructuredValuesOnly(t *testing.T) {
	checks := []RunCheck{
		{Name: "equals", Kind: "json_equals", Pointer: "/nested/value", Expected: json.RawMessage(`7`)},
		{Name: "present", Kind: "json_present", Pointer: "/nested"},
		{Name: "used", Kind: "atl_invocations_min", Minimum: 2},
		{Name: "routes", Kind: "mock_no_unexpected"},
	}
	result, err := evaluateRunChecks(checks, []byte(`{"nested":{"value":7}}`), 2, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for name, passed := range result {
		if !passed {
			t.Errorf("check %s failed", name)
		}
	}
}
