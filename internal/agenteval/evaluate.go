package agenteval

import (
	"fmt"
	"sort"
)

// Evaluate applies a scenario's deterministic gates to an aggregate
// observation. Invalid contracts are rejected separately from a valid failing
// run so automation cannot confuse malformed input with a measured regression.
func Evaluate(s Scenario, o Observation) (Result, error) {
	if err := s.Validate(); err != nil {
		return Result{}, fmt.Errorf("scenario: %w", err)
	}
	if err := o.Validate(); err != nil {
		return Result{}, fmt.Errorf("observation: %w", err)
	}
	if o.ScenarioID != s.ID {
		return Result{}, fmt.Errorf("observation scenario_id %q does not match %q", o.ScenarioID, s.ID)
	}
	if o.EffectiveEligibility() == EligibilityUnsupportedCapability {
		required := make(map[string]struct{}, len(s.RequiredCapabilities))
		for _, capability := range s.RequiredCapabilities {
			required[capability] = struct{}{}
		}
		for _, capability := range o.UnavailableCapabilities {
			if _, ok := required[capability]; !ok {
				return Result{}, fmt.Errorf("unavailable capability %q is not required by the scenario", capability)
			}
		}
	}

	metrics := Metrics{
		AgentTurns: o.Metrics.AgentTurns, ToolCalls: o.Metrics.ToolCalls,
		ATLInvocations: o.Metrics.ATLInvocations, InterfaceInvocations: o.Metrics.InterfaceInvocations, Delegations: o.Metrics.Delegations,
		DuplicateBackendRequests: o.Metrics.DuplicateBackendRequests,
		OutputBytes:              o.Metrics.OutputBytes,
		InputTokens:              o.Metrics.InputTokens, OutputTokens: o.Metrics.OutputTokens,
		MainThreadInputTokens: o.Metrics.MainThreadInputTokens, MainThreadOutputTokens: o.Metrics.MainThreadOutputTokens,
		EstimatedCostMicroUSD: o.Metrics.EstimatedCostMicroUSD,
		DurationMillis:        o.Metrics.DurationMillis,
	}
	allowed := make(map[string]struct{}, len(s.Budgets.AllowedHTTPMethods))
	for _, method := range s.Budgets.AllowedHTTPMethods {
		allowed[method] = struct{}{}
	}
	violations := make([]Violation, 0)
	coverage := sortedBoolMap(o.Coverage)
	methods := sortedStringMap(o.HTTPMethods)
	if coverage["backend_requests"] {
		for method, count := range methods {
			metrics.BackendRequests += count
			if method != "GET" && method != "HEAD" && method != "OPTIONS" {
				metrics.RemoteWrites += count
			}
			if count > 0 {
				if _, ok := allowed[method]; !ok {
					violations = append(violations, Violation{Code: "http_method_not_allowed", Subject: method, Observed: int64(count)})
				}
			}
		}
	}

	limits := []struct {
		name     string
		observed int64
		limit    int64
	}{
		{"agent_turns", int64(metrics.AgentTurns), int64(s.Budgets.MaxAgentTurns)},
		{"tool_calls", int64(metrics.ToolCalls), int64(s.Budgets.MaxToolCalls)},
		{"atl_invocations", int64(metrics.ATLInvocations), int64(s.Budgets.MaxATLInvocations)},
		{"interface_invocations", int64(metrics.InterfaceInvocations), int64(s.Budgets.EffectiveMaxInterfaceInvocations())},
		{"delegations", int64(metrics.Delegations), int64(s.Budgets.MaxDelegations)},
		{"backend_requests", int64(metrics.BackendRequests), int64(s.Budgets.MaxBackendRequests)},
		{"duplicate_backend_requests", int64(metrics.DuplicateBackendRequests), int64(s.Budgets.MaxDuplicateBackendRequests)},
		{"remote_writes", int64(metrics.RemoteWrites), int64(s.Budgets.MaxRemoteWrites)},
		{"output_bytes", metrics.OutputBytes, s.Budgets.MaxOutputBytes},
		{"input_tokens", metrics.InputTokens, s.Budgets.MaxInputTokens},
		{"output_tokens", metrics.OutputTokens, s.Budgets.MaxOutputTokens},
		{"main_thread_input_tokens", metrics.MainThreadInputTokens, s.Budgets.MaxMainThreadInputTokens},
		{"main_thread_output_tokens", metrics.MainThreadOutputTokens, s.Budgets.MaxMainThreadOutputTokens},
		{"estimated_cost_microusd", metrics.EstimatedCostMicroUSD, s.Budgets.MaxEstimatedCostMicroUSD},
		{"duration_millis", metrics.DurationMillis, s.Budgets.MaxDurationMillis},
	}
	for _, item := range limits {
		coverageName := item.name
		if coverageName == "remote_writes" {
			coverageName = "backend_requests"
		}
		if coverage[coverageName] && item.observed > item.limit {
			violations = append(violations, Violation{Code: "budget_exceeded", Subject: item.name, Observed: item.observed, Limit: item.limit})
		}
	}
	for _, name := range s.RequiredMetrics {
		if !coverage[name] {
			violations = append(violations, Violation{Code: "metric_not_observed", Subject: name, Limit: 1})
		}
	}

	checks := sortedBoolMap(o.Checks)
	eligibility := o.EffectiveEligibility()
	if eligibility == EligibilitySupported {
		for _, name := range s.RequiredChecks {
			if passed, ok := checks[name]; !ok || !passed {
				violations = append(violations, Violation{Code: "required_check_failed", Subject: name, Limit: 1})
			}
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Code != violations[j].Code {
			return violations[i].Code < violations[j].Code
		}
		return violations[i].Subject < violations[j].Subject
	})
	status := "pass"
	if eligibility != EligibilitySupported {
		status = "ineligible"
	} else if len(violations) > 0 {
		status = "fail"
	}
	warnings := append([]string(nil), o.Warnings...)
	sort.Strings(warnings)
	families, err := normalizeCapabilityFamilies(o.CapabilityFamilies)
	if err != nil {
		return Result{}, fmt.Errorf("observation: %w", err)
	}
	return Result{
		SchemaVersion: ResultSchemaVersion,
		ScenarioID:    s.ID, TaskClass: s.TaskClass, DataClass: s.DataClass,
		Category: s.EffectiveCategory(), Variant: o.Variant, Surface: o.EffectiveSurface(),
		Eligibility: eligibility, UnavailableCapabilities: append([]string(nil), o.UnavailableCapabilities...),
		BackendObservation: o.BackendObservation, SafetyAssurance: o.SafetyAssurance,
		Runtime: o.Runtime, Status: status, Metrics: metrics,
		Coverage: coverage, HTTPMethods: methods, Checks: checks, Violations: violations,
		Warnings:           warnings,
		CapabilityFamilies: families,
	}, nil
}
