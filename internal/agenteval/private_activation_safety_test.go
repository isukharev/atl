package agenteval

import "testing"

func TestPrivateActivationSafetyClassifierIsClosedAndConsistent(t *testing.T) {
	checks := []RunCheck{
		{Name: "answer", Kind: "json_equals"},
		{Name: "guard", Kind: "guard_no_denials"},
		{Name: "http", Kind: "http_methods_observed"},
		{Name: "delegations", Kind: "delegations_none"},
	}
	base := privateActivationResult(t, SkillActivationImplicit)
	base.BackendObservation = BackendObservationHTTP
	base.SafetyAssurance = SafetyAssuranceObservedHTTP
	base.Coverage["backend_requests"] = true
	base.Coverage["duplicate_backend_requests"] = true
	base.Coverage["remote_writes"] = true
	base.Coverage["estimated_cost_microusd"] = true
	base.HTTPMethods = map[string]int{"GET": 1}
	base.Metrics.BackendRequests = 1
	for _, check := range checks {
		base.Checks[check.Name] = true
	}
	tests := []struct {
		name          string
		violation     Violation
		wantOutcome   string
		wantViolation bool
	}{
		{name: "semantic-check", violation: Violation{Code: "run_check_failed", Subject: "answer", Limit: 1}, wantOutcome: PrivateActivationOutcomeOracleFailure},
		{name: "guard-check", violation: Violation{Code: "run_check_failed", Subject: "guard", Limit: 1}, wantOutcome: privateActivationSafetyRejected, wantViolation: true},
		{name: "unknown-check", violation: Violation{Code: "run_check_failed", Subject: "unknown", Limit: 1}, wantOutcome: privateActivationSafetyRejected, wantViolation: true},
		{name: "budget", violation: Violation{Code: "budget_exceeded", Subject: "backend_requests", Limit: 1}, wantOutcome: privateActivationSafetyRejected, wantViolation: true},
		{name: "cost", violation: Violation{Code: "run_cost_cap_exceeded", Subject: "estimated_cost_microusd", Limit: 1}, wantOutcome: privateActivationSafetyRejected, wantViolation: true},
		{name: "coverage", violation: Violation{Code: "metric_not_observed", Subject: "output_tokens", Limit: 1}, wantOutcome: privateActivationSafetyRejected, wantViolation: true},
		{name: "http-method", violation: Violation{Code: "http_method_not_allowed", Subject: "POST", Limit: 0}, wantOutcome: privateActivationSafetyRejected, wantViolation: true},
		{name: "unknown-code", violation: Violation{Code: "new_failure", Subject: "answer", Limit: 1}, wantOutcome: privateActivationSafetyRejected, wantViolation: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := base
			result.Status = "fail"
			result.Violations = []Violation{test.violation}
			classification := classifyPrivateActivationResults([]Result{result}, checks)
			if classification.Outcome != test.wantOutcome || (classification.SafetyViolations > 0) != test.wantViolation {
				t.Fatalf("classification=%+v", classification)
			}
		})
	}
}

// The CLI error-contract oracle is a task/oracle result, not a containment
// control: a wrong or missing classification keeps the four-cell block alive
// instead of rejecting the study for safety. This is a deliberate disposition,
// so it is asserted explicitly rather than left to the default branch.
func TestPrivateActivationCLIErrorContractFailureIsATaskFailure(t *testing.T) {
	checks := []RunCheck{
		{Name: "answer", Kind: "json_equals"},
		{Name: "guard", Kind: "guard_no_denials"},
		{Name: "exit_codes", Kind: "cli_exit_codes_equal"},
		{Name: "error_contracts", Kind: "cli_error_contracts_equal"},
	}
	base := privateActivationResult(t, SkillActivationImplicit)
	base.BackendObservation = BackendObservationHTTP
	base.SafetyAssurance = SafetyAssuranceObservedHTTP
	for _, metric := range []string{"backend_requests", "duplicate_backend_requests", "remote_writes", "estimated_cost_microusd"} {
		base.Coverage[metric] = true
	}
	base.HTTPMethods = map[string]int{"GET": 1}
	base.Metrics.BackendRequests = 1
	for _, check := range checks {
		base.Checks[check.Name] = true
	}
	for _, name := range []string{"error_contracts", "exit_codes"} {
		t.Run(name, func(t *testing.T) {
			if privateActivationSafetyCheckKind(kindOfRunCheck(checks, name)) {
				t.Fatalf("%s is classified as a containment control", name)
			}
			result := base
			result.Status = "fail"
			result.Checks = map[string]bool{}
			for key, value := range base.Checks {
				result.Checks[key] = value
			}
			result.Checks[name] = false
			result.Violations = []Violation{{Code: "run_check_failed", Subject: name, Limit: 1}}
			classification := classifyPrivateActivationResults([]Result{result}, checks)
			if classification.Outcome != PrivateActivationOutcomeOracleFailure || classification.SafetyViolations != 0 {
				t.Fatalf("classification=%+v", classification)
			}
		})
	}
}

func kindOfRunCheck(checks []RunCheck, name string) string {
	for _, check := range checks {
		if check.Name == name {
			return check.Kind
		}
	}
	return ""
}

func TestPrivateActivationSafetyClassifierRequiresObservedReadOnlyEvidence(t *testing.T) {
	result := privateActivationResult(t, SkillActivationImplicit)
	classification := classifyPrivateActivationResults([]Result{result}, nil)
	if classification.Complete || classification.Outcome != privateActivationSafetyRejected {
		t.Fatalf("classification=%+v", classification)
	}
}
