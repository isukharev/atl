package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/grading"
	profileatl "github.com/isukharev/atl/internal/agenteval/profile/atl"
)

func TestCurrentATLChecksExecuteThroughGenericDeterministicReceipts(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "expected.json"), []byte(`{"answer":"ok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fileData := []byte("bound workspace artifact\n")
	if err := os.WriteFile(filepath.Join(workspace, "artifact.txt"), fileData, 0o600); err != nil {
		t.Fatal(err)
	}
	mcpExpected := json.RawMessage(`[{"tool":"jira_issue_get","arguments":{"key":"TEST-1"}}]`)
	routeExpected := json.RawMessage(`[{"http_methods":{"GET":1},"invocations":[{"tool":"jira_issue_get","arguments":{"key":"TEST-1"}}]},{"http_methods":{"POST":1},"invocations":[{"tool":"jira_issue_search","arguments":{"query":"project = TEST"}}]}]`)
	checks := []RunCheck{
		{Name: "atl-all", Kind: "atl_all_succeeded"},
		{Name: "atl-failures", Kind: "atl_failures_equals", Expected: json.RawMessage(`0`)},
		{Name: "atl-max", Kind: "atl_invocations_max", Maximum: 2},
		{Name: "atl-min", Kind: "atl_invocations_min", Minimum: 2},
		{Name: "capability-families", Kind: "capability_families_equal", Expected: json.RawMessage(`[{"family":"jira.issue.get","invocations":1,"successes":1,"failures":0}]`)},
		{Name: "capability-sequence", Kind: "capability_sequence_equal", Expected: json.RawMessage(`["jira.issue.get"]`)},
		{Name: "cli-errors", Kind: "cli_error_contracts_equal", Expected: json.RawMessage(`[{"exit_code":2,"kind":"usage_error","remediation":"fix_request"}]`)},
		{Name: "cli-exits", Kind: "cli_exit_codes_equal", Expected: json.RawMessage(`[0,2]`)},
		{Name: "delegations-min", Kind: "delegations_min", Minimum: 1},
		{Name: "delegations-none", Kind: "delegations_none"},
		{Name: "guard-clean", Kind: "guard_no_denials"},
		{Name: "http-equal", Kind: "http_methods_equal", Expected: json.RawMessage(`{"GET":1}`)},
		{Name: "http-observed", Kind: "http_methods_observed"},
		{Name: "interface-all", Kind: "interface_all_succeeded"},
		{Name: "interface-failures", Kind: "interface_failures_equals", Expected: json.RawMessage(`0`)},
		{Name: "interface-max", Kind: "interface_invocations_max", Maximum: 2},
		{Name: "interface-min", Kind: "interface_invocations_min", Minimum: 2},
		{Name: "json-array", Kind: "json_array_min_items", Pointer: "/items", Minimum: 2},
		{Name: "json-equal", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`"ok"`)},
		{Name: "json-workspace", Kind: "json_equals_workspace_json", Pointer: "/answer", Expected: json.RawMessage(`{"path":"expected.json","pointer":"/answer"}`)},
		{Name: "json-present", Kind: "json_present", Pointer: "/present"},
		{Name: "optional-period", Kind: "json_string_equals_optional_period", Pointer: "/period", Expected: json.RawMessage(`"2026-Q3"`)},
		{Name: "mcp-equal", Kind: "mcp_invocations_equal", Expected: mcpExpected},
		{Name: "mcp-multiset", Kind: "mcp_invocations_multiset_equal", Expected: mcpExpected},
		{Name: "mcp-route", Kind: "mcp_route_one_of", Expected: routeExpected},
		{Name: "mock-clean", Kind: "mock_no_unexpected"},
		{Name: "skill-used", Kind: "skill_invocations_min", Expected: json.RawMessage(`"atl:jira"`), Minimum: 1},
		{Name: "workspace-sha", Kind: "workspace_file_sha256", Expected: json.RawMessage(`{"path":"artifact.txt","sha256":"` + sha256HexBytes(fileData) + `"}`)},
	}
	plan, err := newATLGradingPlan(checks, workspace, strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("evaluate: %v cause=%v nested=%v", err, errors.Unwrap(err), errors.Unwrap(errors.Unwrap(err)))
	}
	for _, check := range plan.Checks {
		family, ok := profileatl.LegacyGradingFamily(atlCheckKind(checks, check.ID))
		if !ok || family != check.Kind {
			t.Fatalf("ATL check %q mapped to %q, catalog=%q found=%t", check.ID, check.Kind, family, ok)
		}
	}
	invocation := MCPInvocation{Tool: "jira_issue_get", Arguments: json.RawMessage(`{"key":"TEST-1"}`)}
	evaluation, err := evaluateATLChecksWithPlan(context.Background(), plan, checks, atlGradingObservation{
		final: []byte(`{"answer":"ok","items":[1,2],"period":"2026-Q3.","present":false}`), workspace: workspace,
		atlInvocations: 2, failedATL: 0, unexpectedRequests: 0, skillInvocations: 1,
		skillInvocationsByName: map[string]int{"atl:jira": 1}, delegations: 1, guardDenials: 0,
		httpMethods: map[string]int{"GET": 1}, httpMethodsObserved: true, cliExitCodes: []int{0, 2},
		capabilityFamilies:         []CapabilityFamilyMetric{{Family: "jira.issue.get", Invocations: 1, Successes: 1}},
		capabilityFamiliesObserved: true, capabilitySequence: []string{"jira.issue.get"},
		mcpInvocations: []MCPInvocation{invocation}, mcpInvocationsObserved: true,
		cliErrorContracts: []CLIErrorContract{{ExitCode: 2, Kind: "usage_error", Remediation: "fix_request"}},
	})
	if err != nil {
		t.Fatalf("evaluate: %v cause=%v nested=%v", err, errors.Unwrap(err), errors.Unwrap(errors.Unwrap(err)))
	}
	if evaluation.receipt.Status != grading.ReceiptComplete || len(evaluation.receipt.Decisions) != len(checks) ||
		len(evaluation.receipt.Evidence) == 0 {
		t.Fatalf("generic receipt=%+v", evaluation.receipt)
	}
	for _, decision := range evaluation.receipt.Decisions {
		name := atlCheckName(checks, decision.CheckID)
		wantPassed := name != "delegations-none"
		if decision.Passed != wantPassed || decision.Authority != grading.AuthorityDeterministic || decision.Presence != grading.PresenceObserved ||
			len(decision.Citations) != 1 || evaluation.checks[name] != wantPassed {
			t.Fatalf("decision=%+v", decision)
		}
	}

	mutated := checks
	mutatedPlan, err := newATLGradingPlan(mutated, workspace, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	failed, err := evaluateATLChecksWithPlan(context.Background(), mutatedPlan, mutated, atlGradingObservation{
		final: []byte(`{"answer":"wrong","items":[1],"period":"wrong"}`), workspace: workspace,
		httpMethods: map[string]int{"POST": 1}, httpMethodsObserved: true, mcpInvocationsObserved: true,
		capabilityFamiliesObserved: true, skillInvocationsByName: map[string]int{},
	})
	if err != nil {
		t.Fatalf("mutated evaluate: %v cause=%v nested=%v", err, errors.Unwrap(err), errors.Unwrap(errors.Unwrap(err)))
	}
	for _, id := range []string{"json-array", "json-equal", "json-present", "optional-period", "mcp-route"} {
		if failed.checks[id] {
			t.Fatalf("mutated ATL evidence passed %q", id)
		}
	}
	missingChecks := []RunCheck{checks[4], checks[22]}
	missingPlan, err := newATLGradingPlan(missingChecks, workspace, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	missing, err := evaluateATLChecksWithPlan(context.Background(), missingPlan, missingChecks,
		atlGradingObservation{final: []byte(`{}`), workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range missing.receipt.Decisions {
		if decision.Presence != grading.PresenceUnknown || decision.Passed || missing.checks[decision.CheckID] {
			t.Fatalf("missing ATL evidence was not preserved as unknown: %+v", decision)
		}
	}
}

func TestATLGradingAdapterRejectsUncheckedNarrowingInputs(t *testing.T) {
	for _, test := range []RunCheck{{Name: "negative", Kind: "atl_invocations_min", Minimum: -1},
		{Name: "oversized-array", Kind: "json_array_min_items", Pointer: "/items", Minimum: grading.MaxEvidenceItems + 1}} {
		if _, err := newATLGradingPlan([]RunCheck{test}, t.TempDir(), strings.Repeat("a", 64)); err == nil {
			t.Fatalf("unchecked ATL bound was admitted: %+v", test)
		}
	}
}

func TestATLGradingAdapterPreservesLegacyPointerNamesAndAdmittedBounds(t *testing.T) {
	workspace := t.TempDir()
	artifact := bytes.Repeat([]byte{'x'}, maxWorkspaceArtifactBytes)
	if err := os.WriteFile(filepath.Join(workspace, "large.bin"), artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	longExpected := strings.Repeat("x", maxOptionalPeriodExpectedBytes)
	checks := []RunCheck{
		{Name: "a//b", Kind: "atl_invocations_max", Maximum: int(^uint(0) >> 1)},
		{Name: "array-equals", Kind: "json_equals", Pointer: "/items/0/value", Expected: json.RawMessage(`"ok"`)},
		{Name: "array-present", Kind: "json_present", Pointer: "/items/0"},
		{Name: "array-minimum", Kind: "json_array_min_items", Pointer: "/groups/0", Minimum: 1},
		{Name: "long-period", Kind: "json_string_equals_optional_period", Pointer: "/period", Expected: json.RawMessage(strconv.Quote(longExpected))},
		{Name: "large-file-a", Kind: "workspace_file_sha256", Expected: json.RawMessage(`{"path":"large.bin","sha256":"` + sha256HexBytes(artifact) + `"}`)},
		{Name: "large-file-b", Kind: "workspace_file_sha256", Expected: json.RawMessage(`{"path":"large.bin","sha256":"` + sha256HexBytes(artifact) + `"}`)},
		{Name: "missing-workspace-json", Kind: "json_equals_workspace_json", Pointer: "/present",
			Expected: json.RawMessage(`{"path":"missing.json","pointer":"/answer"}`)},
		{Name: "oversized-observation", Kind: "cli_exit_codes_equal", Expected: json.RawMessage(`[0]`)},
	}
	plan, err := newATLGradingPlan(checks, workspace, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	evaluation, err := evaluateATLChecksWithPlan(context.Background(), plan, checks, atlGradingObservation{
		final:     []byte(`{"items":[{"value":"ok"}],"groups":[[1]],"period":` + strconv.Quote(longExpected) + `,"present":true}`),
		workspace: workspace, atlInvocations: 1, cliExitCodes: make([]int, grading.MaxSequenceItems+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"array-equals", "array-present", "array-minimum", "missing-workspace-json", "oversized-observation"} {
		if evaluation.checks[name] {
			t.Fatalf("legacy object-only pointer unexpectedly traversed an array for %q", name)
		}
	}
	for _, name := range []string{"a//b", "long-period", "large-file-a", "large-file-b"} {
		if !evaluation.checks[name] {
			t.Fatalf("legacy-valid admitted bound did not pass for %q", name)
		}
	}
}

func TestATLGradingAdapterDefersWorkspaceJSONComparisonUntilEvaluation(t *testing.T) {
	workspace := t.TempDir()
	check := RunCheck{Name: "dynamic-workspace-json", Kind: "json_equals_workspace_json", Pointer: "/proposal_hash",
		Expected: json.RawMessage(`{"path":"plan.json","pointer":"/proposal_hash"}`)}
	plan, err := newATLGradingPlan([]RunCheck{check}, workspace, strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	planCheck, found := atlPlanCheck(plan, check.Name)
	if !found || planCheck.Kind != grading.CheckJSONValue {
		t.Fatalf("dynamic workspace check=%+v found=%v", planCheck, found)
	}
	if err := os.WriteFile(filepath.Join(workspace, "plan.json"), []byte(`{"proposal_hash":"bound-after-plan"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	evaluation, err := evaluateATLChecksWithPlan(context.Background(), plan, []RunCheck{check}, atlGradingObservation{
		final: []byte(`{"proposal_hash":"bound-after-plan"}`), workspace: workspace})
	if err != nil || !evaluation.checks[check.Name] {
		t.Fatalf("matching evaluation=%+v err=%v", evaluation, err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "plan.json"), []byte(`{"proposal_hash":"drifted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	evaluation, err = evaluateATLChecksWithPlan(context.Background(), plan, []RunCheck{check}, atlGradingObservation{
		final: []byte(`{"proposal_hash":"bound-after-plan"}`), workspace: workspace})
	if err != nil || evaluation.checks[check.Name] {
		t.Fatalf("drift evaluation=%+v err=%v", evaluation, err)
	}
	mutated := check
	mutated.Pointer = "/other"
	evaluation, err = evaluateATLChecksWithPlan(context.Background(), plan, []RunCheck{mutated}, atlGradingObservation{
		final: []byte(`{"other":"drifted"}`), workspace: workspace})
	if err != nil || evaluation.checks[check.Name] {
		t.Fatalf("predicate mutation evaluation=%+v err=%v", evaluation, err)
	}
}

func TestATLGradingAdapterBindsProducedProposalHashAtEvaluation(t *testing.T) {
	hash := strings.Repeat("a", 64)
	check := RunCheck{Name: "dynamic-proposal", Kind: "json_equals_proposal_hash_binding", Pointer: "/proposal_hash",
		Expected: json.RawMessage(`{"binding":"jira_field"}`)}
	plan, err := newATLGradingPlan([]RunCheck{check}, "", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	planCheck, found := atlPlanCheck(plan, check.Name)
	if !found || planCheck.Kind != grading.CheckJSONValue {
		t.Fatalf("dynamic proposal check=%+v found=%v", planCheck, found)
	}
	evaluation, err := evaluateATLChecksWithPlan(context.Background(), plan, []RunCheck{check}, atlGradingObservation{
		final: []byte(`{"proposal_hash":"` + hash + `"}`), producedProposalHashes: map[string]string{"jira_field": hash}})
	if err != nil || !evaluation.checks[check.Name] {
		t.Fatalf("matching evaluation=%+v err=%v", evaluation, err)
	}
	for name, observation := range map[string]atlGradingObservation{
		"missing":  {final: []byte(`{"proposal_hash":"` + hash + `"}`)},
		"mismatch": {final: []byte(`{"proposal_hash":"` + strings.Repeat("b", 64) + `"}`), producedProposalHashes: map[string]string{"jira_field": hash}},
	} {
		t.Run(name, func(t *testing.T) {
			evaluation, err := evaluateATLChecksWithPlan(context.Background(), plan, []RunCheck{check}, observation)
			if err != nil || evaluation.checks[check.Name] {
				t.Fatalf("evaluation=%+v err=%v", evaluation, err)
			}
		})
	}
}

func TestPrivatePanelGradingHashesLegacyValidRubricCriterionIDs(t *testing.T) {
	digest := strings.Repeat("a", 64)
	reviewers := []Reviewer{{ID: "reviewer-01", Kind: "codex", Model: "model"},
		{ID: "reviewer-02", Kind: "codex", Model: "model"}, {ID: "reviewer-03", Kind: "codex", Model: "model"}}
	executions := make([]PrivateReviewerExecution, len(reviewers))
	for index, reviewer := range reviewers {
		executions[index] = PrivateReviewerExecution{ReviewerID: reviewer.ID, Reasoning: "high", TimeoutSeconds: 30,
			Pricing: Pricing{InputMicroUSDPerMillionTokens: 1, OutputMicroUSDPerMillionTokens: 1}, MaxEstimatedCostMicroUSD: 100}
	}
	contract := privateQualitativeReviewPanelContract{Method: QualitativePanelMethod, Reviewers: reviewers,
		MaxCriterionRangeBPS: 2500, BlindAssignmentSHA256: digest, Executions: executions}
	rubric := Rubric{SchemaVersion: RubricSchemaVersion, ID: "rubric", ScenarioID: "scenario", MinimumScoreBPS: 5000,
		Criteria:          []RubricCriterion{{ID: "a//b", Description: "Synthetic criterion.", Maximum: 4, Minimum: 2, Weight: 1}},
		AllowedFindingIDs: []string{}}
	_, plan, err := privatePanelGradingPlan(contract, rubric, PrivateBaselineSurfaceSource{
		QualitativePanelContractSHA256: digest, ExecutionReceiptSHA256: strings.Repeat("b", 64)}, []byte("result"), []byte("final"))
	if err != nil {
		t.Fatal(err)
	}
	want := privateGradingCriterionID("a//b")
	if len(plan.Checks) != 1 || plan.Checks[0].ID != want || plan.Checks[0].Qualitative.RubricCriterionID != want {
		t.Fatalf("hashed rubric projection=%+v want=%q", plan.Checks, want)
	}
}

func atlCheckKind(checks []RunCheck, id string) string {
	for _, check := range checks {
		if atlGradingCheckID(check.Name) == id {
			return check.Kind
		}
	}
	return ""
}

func atlCheckName(checks []RunCheck, id string) string {
	for _, check := range checks {
		if atlGradingCheckID(check.Name) == id {
			return check.Name
		}
	}
	return ""
}
