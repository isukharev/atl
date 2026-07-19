package agenteval

import (
	"strings"
	"testing"
)

func validScenario() Scenario {
	return Scenario{
		SchemaVersion: ScenarioSchemaVersion,
		ID:            "jira.epic-evidence", TaskClass: "jira/evidence",
		Description:          "Discover fields and collect bounded epic evidence.",
		DataClass:            "synthetic",
		RequiredCapabilities: []string{"jira.issue.fields", "jira.epic.digest"},
		RequiredChecks:       []string{"answer_correct", "sources_complete"},
		RequiredMetrics:      []string{"atl_invocations", "backend_requests", "output_bytes"},
		Budgets: Budgets{
			MaxATLInvocations: 2, MaxBackendRequests: 8, MaxOutputBytes: 8192,
			AllowedHTTPMethods: []string{"GET"},
		},
	}
}

func validObservation() Observation {
	return Observation{
		SchemaVersion: ObservationSchemaVersion,
		ScenarioID:    "jira.epic-evidence", Variant: "baseline",
		Runtime: Runtime{Provider: "deterministic", ATLVersion: "0.4.0"},
		Metrics: InputMetrics{ATLInvocations: 2, OutputBytes: 4096},
		Coverage: map[string]bool{
			"atl_invocations": true, "backend_requests": true, "output_bytes": true,
		},
		HTTPMethods: map[string]int{"GET": 7},
		Checks:      map[string]bool{"answer_correct": true, "sources_complete": true},
	}
}

func TestEvaluateDistinguishesMissingMetricFromObservedZero(t *testing.T) {
	observation := validObservation()
	delete(observation.Coverage, "backend_requests")
	observation.HTTPMethods = nil
	result, err := Evaluate(validScenario(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fail" {
		t.Fatalf("result=%+v", result)
	}
	var found bool
	for _, violation := range result.Violations {
		found = found || violation.Code == "metric_not_observed" && violation.Subject == "backend_requests"
	}
	if !found {
		t.Fatalf("violations=%+v", result.Violations)
	}
}

func TestEvaluatePassesBoundedReadOnlyWorkflow(t *testing.T) {
	result, err := Evaluate(validScenario(), validObservation())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pass" || result.Metrics.BackendRequests != 7 || result.Metrics.RemoteWrites != 0 || len(result.Violations) != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateRejectsPrivateCodexCLIMissingPromptIdentity(t *testing.T) {
	scenario := validScenario()
	scenario.DataClass = "private-local"
	observation := validObservation()
	observation.Surface = SurfaceCLISkill
	observation.Runtime.Provider = "codex"

	if _, err := Evaluate(scenario, observation); err == nil || !strings.Contains(err.Error(), "prompt contract identity") {
		t.Fatalf("missing private prompt identity err=%v", err)
	}
}

func TestObservationV2RequiresExplicitMigration(t *testing.T) {
	observation := validObservation()
	observation.SchemaVersion = 2
	if err := observation.Validate(); err == nil || !strings.Contains(err.Error(), "schema_version 2") {
		t.Fatalf("legacy observation err=%v", err)
	}
}

func TestBenchmarkIdentityCompatibilityDefaultsAndExplicitValues(t *testing.T) {
	scenario := validScenario()
	observation := validObservation()
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Category != BenchmarkCategoryRouteFixed || result.Surface != SurfaceLegacyUnspecified {
		t.Fatalf("legacy identity=%+v", result)
	}

	scenario.Category = BenchmarkCategoryNeutralCommon
	scenario.RequiredSemanticChecks = []string{"answer_correct"}
	scenario.RequiredMetrics = []string{"interface_invocations", "backend_requests", "output_bytes"}
	scenario.Budgets.MaxInterfaceInvocations = scenario.Budgets.MaxATLInvocations
	scenario.Budgets.MaxATLInvocations = 0
	observation.Surface = SurfaceATLMCP
	observation.Metrics.InterfaceInvocations = observation.Metrics.ATLInvocations
	observation.Metrics.ATLInvocations = 0
	observation.Coverage["interface_invocations"] = true
	delete(observation.Coverage, "atl_invocations")
	result, err = Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Category != BenchmarkCategoryNeutralCommon || result.Surface != SurfaceATLMCP {
		t.Fatalf("explicit identity=%+v", result)
	}

	scenario.Category = "unknown"
	if err := scenario.Validate(); err == nil {
		t.Fatal("unknown category passed")
	}
	observation = validObservation()
	observation.Surface = "unknown"
	if err := observation.Validate(); err == nil {
		t.Fatal("unknown surface passed")
	}
}

func TestScenarioIDRejectsOutputPathTraversal(t *testing.T) {
	for _, value := range []string{"a/../../escaped", `a\..\escaped`, ".", ".."} {
		t.Run(value, func(t *testing.T) {
			scenario := validScenario()
			scenario.ID = value
			if err := scenario.Validate(); err == nil {
				t.Fatal("path-like scenario id passed")
			}
		})
	}
}

func TestEvaluateSupportsGenericInterfaceInvocationMetric(t *testing.T) {
	scenario := validScenario()
	scenario.RequiredMetrics = []string{"interface_invocations", "backend_requests"}
	scenario.Budgets.MaxATLInvocations = 0
	scenario.Budgets.MaxInterfaceInvocations = 2
	observation := validObservation()
	delete(observation.Coverage, "atl_invocations")
	observation.Metrics.ATLInvocations = 0
	observation.Metrics.InterfaceInvocations = 2
	observation.Coverage["interface_invocations"] = true
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pass" || result.Metrics.InterfaceInvocations != 2 {
		t.Fatalf("result=%+v", result)
	}
	observation.Metrics.InterfaceInvocations = 3
	result, err = Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fail" || len(result.Violations) == 0 || result.Violations[0].Subject != "interface_invocations" {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateMarksUnsupportedAndDriftedRunsIneligibleWithoutSemanticFailure(t *testing.T) {
	for name, eligibility := range map[string]string{
		"unsupported": EligibilityUnsupportedCapability,
		"drifted":     EligibilityInvalidatedDrift,
	} {
		t.Run(name, func(t *testing.T) {
			observation := validObservation()
			observation.Eligibility = eligibility
			observation.Checks["answer_correct"] = false
			if eligibility == EligibilityUnsupportedCapability {
				observation.UnavailableCapabilities = []string{"jira.epic.digest"}
			}
			observation.Metrics.OutputBytes = 9000
			result, err := Evaluate(validScenario(), observation)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "ineligible" || result.EffectiveEligibility() != eligibility {
				t.Fatalf("result=%+v", result)
			}
			for _, violation := range result.Violations {
				if violation.Code == "required_check_failed" {
					t.Fatalf("ineligible result received semantic failure: %+v", result.Violations)
				}
			}
			if len(result.Violations) != 1 || result.Violations[0].Code != "budget_exceeded" {
				t.Fatalf("safety/budget violation not preserved: %+v", result.Violations)
			}
		})
	}
}

func TestEligibilityValidationFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(*Observation){
		"unknown":                    func(o *Observation) { o.Eligibility = "unknown" },
		"supported with unavailable": func(o *Observation) { o.UnavailableCapabilities = []string{"jira.issue.search"} },
		"unsupported without ids":    func(o *Observation) { o.Eligibility = EligibilityUnsupportedCapability },
		"drifted with ids": func(o *Observation) {
			o.Eligibility = EligibilityInvalidatedDrift
			o.UnavailableCapabilities = []string{"jira.issue.search"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			observation := validObservation()
			mutate(&observation)
			if err := observation.Validate(); err == nil {
				t.Fatal("invalid eligibility passed")
			}
		})
	}
}

func TestEvaluateRejectsUnsupportedCapabilityOutsideScenario(t *testing.T) {
	observation := validObservation()
	observation.Eligibility = EligibilityUnsupportedCapability
	observation.UnavailableCapabilities = []string{"confluence.page.section"}
	if _, err := Evaluate(validScenario(), observation); err == nil || !strings.Contains(err.Error(), "not required") {
		t.Fatalf("err=%v", err)
	}
}

func TestEvaluateReportsBudgetsMethodsAndChecks(t *testing.T) {
	observation := validObservation()
	observation.Metrics.OutputBytes = 9000
	observation.HTTPMethods["POST"] = 1
	observation.Checks["sources_complete"] = false
	result, err := Evaluate(validScenario(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "fail" {
		t.Fatalf("status=%q", result.Status)
	}
	want := map[string]bool{
		"budget_exceeded/output_bytes":           false,
		"budget_exceeded/remote_writes":          false,
		"http_method_not_allowed/POST":           false,
		"required_check_failed/sources_complete": false,
	}
	for _, violation := range result.Violations {
		key := violation.Code + "/" + violation.Subject
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("missing violation %s in %+v", key, result.Violations)
		}
	}
}

func TestDecodeContractsFailsClosed(t *testing.T) {
	scenarioJSON := `{"schema_version":1,"id":"x","task_class":"x/y","description":"x","data_class":"synthetic","required_capabilities":[],"required_checks":["ok"],"required_metrics":["backend_requests"],"budgets":{"max_agent_turns":0,"max_tool_calls":0,"max_atl_invocations":1,"max_backend_requests":1,"max_remote_writes":0,"max_output_bytes":1,"max_input_tokens":0,"max_output_tokens":0,"max_estimated_cost_microusd":0,"max_duration_millis":0,"allowed_http_methods":["GET"]},"unknown":true}`
	if _, err := DecodeScenario(strings.NewReader(scenarioJSON)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err=%v", err)
	}
	observationJSON := `{"schema_version":1,"scenario_id":"x","variant":"baseline","runtime":{"provider":"deterministic","atl_version":"0.4.0"},"metrics":{"agent_turns":0,"tool_calls":0,"atl_invocations":0,"output_bytes":0,"input_tokens":0,"output_tokens":0,"estimated_cost_microusd":0,"duration_millis":0},"coverage":{"backend_requests":true},"http_methods":{},"checks":{"ok":true}} {}`
	if _, err := DecodeObservation(strings.NewReader(observationJSON)); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("err=%v", err)
	}
}

func TestEvaluateRejectsMismatchedScenario(t *testing.T) {
	observation := validObservation()
	observation.ScenarioID = "jira.other"
	if _, err := Evaluate(validScenario(), observation); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err=%v", err)
	}
}

func TestScenarioCapsDelegationAtThreeChildren(t *testing.T) {
	scenario := validScenario()
	scenario.Budgets.MaxDelegations = 4
	if err := scenario.Validate(); err == nil || !strings.Contains(err.Error(), "must not exceed 3") {
		t.Fatalf("err=%v", err)
	}
}

func TestObservationRejectsMainThreadTokensAboveTotal(t *testing.T) {
	observation := validObservation()
	observation.Coverage["input_tokens"] = true
	observation.Coverage["main_thread_input_tokens"] = true
	observation.Metrics.InputTokens = 10
	observation.Metrics.MainThreadInputTokens = 11
	if err := observation.Validate(); err == nil || !strings.Contains(err.Error(), "exceed total") {
		t.Fatalf("err=%v", err)
	}
}
