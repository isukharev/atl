package agenteval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFinalizeHeadlessOutcomeSeparatesLegacyAndGenericInvocationMetrics(t *testing.T) {
	for _, test := range []struct {
		name            string
		maxATL          int
		maxInterface    int
		requiredMetrics []string
		wantATL         int
		wantATLCoverage bool
	}{
		{
			name:            "legacy",
			maxATL:          3,
			requiredMetrics: []string{"atl_invocations", "interface_invocations", "backend_requests", "output_bytes", "duration_millis"},
			wantATL:         2,
			wantATLCoverage: true,
		},
		{
			name:            "generic",
			maxInterface:    3,
			requiredMetrics: []string{"interface_invocations", "backend_requests", "output_bytes", "duration_millis"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			contract := outcomeTestContract()
			contract.scenario.RequiredMetrics = test.requiredMetrics
			contract.scenario.Budgets.MaxATLInvocations = test.maxATL
			contract.scenario.Budgets.MaxInterfaceInvocations = test.maxInterface
			contract.spec.MaxEstimatedCostMicroUSD = 100
			runDir := t.TempDir()
			if err := os.Chmod(runDir, 0o700); err != nil {
				t.Fatal(err)
			}
			result, err := finalizeHeadlessOutcome(headlessOutcomeInput{
				contract: contract,
				trajectory: headlessTrajectory{
					providerMetrics: ProviderMetrics{Coverage: map[string]bool{}, CapabilityFamilyCoverage: true},
					final:           []byte(`{"answer":"ok"}`), methods: map[string]int{}, httpMethodsObserved: true,
					atlInvocations: 2,
				},
				workspace: t.TempDir(), runDir: runDir, durationMillis: 1,
				runtime: Runtime{Provider: "codex", ATLVersion: "test"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != "pass" || result.Metrics.ATLInvocations != test.wantATL ||
				result.Metrics.InterfaceInvocations != 2 || result.Coverage["atl_invocations"] != test.wantATLCoverage {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestFinalizeHeadlessOutcomeSortsAllViolationsAndBindsReceiptToExactResultBytes(t *testing.T) {
	contract := outcomeTestContract()
	contract.scenario.RequiredMetrics = []string{"interface_invocations", "backend_requests", "output_bytes", "duration_millis"}
	contract.scenario.Budgets.MaxInterfaceInvocations = 1
	contract.scenario.Budgets.MaxDurationMillis = 0
	contract.scenario.Budgets.MaxEstimatedCostMicroUSD = 100
	contract.spec.MaxEstimatedCostMicroUSD = 5
	contract.spec.Repetitions = 1
	contract.spec.Checks = append(contract.spec.Checks, RunCheck{
		Name: "zeta", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`"different"`),
	})
	attestation := &syntheticRunAttestation{
		spec: contract.spec,
		executables: syntheticExecutableDigests{
			agent: strings.Repeat("a", 64), atl: strings.Repeat("b", 64), wrapper: strings.Repeat("c", 64),
		},
	}
	runDir := t.TempDir()
	if err := os.Chmod(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var receipt SyntheticRunReceipt
	result, err := finalizeHeadlessOutcome(headlessOutcomeInput{
		contract: contract,
		trajectory: headlessTrajectory{
			providerMetrics: ProviderMetrics{
				EstimatedCostMicroUSD:    10,
				Coverage:                 map[string]bool{"estimated_cost_microusd": true},
				CapabilityFamilyCoverage: true,
			},
			final: []byte(`{"answer":"ok"}`), methods: map[string]int{}, httpMethodsObserved: true,
			atlInvocations: 2,
		},
		workspace: t.TempDir(), runDir: runDir, durationMillis: 7,
		runtime: Runtime{Provider: "codex", ATLVersion: "test"}, repetition: 1,
		taskContractSHA256: strings.Repeat("d", 64), executionContractSHA256: strings.Repeat("e", 64),
		attemptBindingSHA256: strings.Repeat("f", 64),
		attestation:          attestation, receipt: &receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantViolations := []Violation{
		{Code: "budget_exceeded", Subject: "duration_millis", Observed: 7},
		{Code: "budget_exceeded", Subject: "interface_invocations", Observed: 2, Limit: 1},
		{Code: "run_check_failed", Subject: "zeta", Limit: 1},
		{Code: "run_cost_cap_exceeded", Subject: "estimated_cost_microusd", Observed: 10, Limit: 5},
	}
	if result.Status != "fail" || !slices.Equal(result.Violations, wantViolations) {
		t.Fatalf("violations=%+v", result.Violations)
	}
	resultData, err := os.ReadFile(filepath.Join(runDir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wantData = append(wantData, '\n')
	if !bytes.Equal(resultData, wantData) {
		t.Fatalf("persisted result bytes differ from returned result")
	}
	if receipt.ResultSHA256 != sha256HexBytes(resultData) {
		t.Fatalf("receipt result digest=%q want=%q", receipt.ResultSHA256, sha256HexBytes(resultData))
	}
	if _, err := os.Stat(filepath.Join(runDir, syntheticRunReceiptFileName)); !os.IsNotExist(err) {
		t.Fatalf("finalizer persisted receipt before outer revalidation: %v", err)
	}
}

func outcomeTestContract() resolvedRunContract {
	return resolvedRunContract{
		spec: RunSpec{
			Provider: "codex", Variant: "baseline", MaxEstimatedCostMicroUSD: 100,
			Checks: []RunCheck{{Name: "answer_correct", Kind: "json_equals", Pointer: "/answer", Expected: json.RawMessage(`"ok"`)}},
		},
		scenario: Scenario{
			SchemaVersion: ScenarioSchemaVersion, ID: "runner.outcome", TaskClass: "runner/outcome",
			Description: "Verify deterministic headless outcome finalization.", DataClass: "synthetic",
			RequiredChecks: []string{"answer_correct"},
			Budgets: Budgets{
				MaxAgentTurns: 1, MaxToolCalls: 1, MaxDelegations: 1,
				MaxBackendRequests: 1, MaxOutputBytes: 1, MaxInputTokens: 1, MaxOutputTokens: 1,
				MaxMainThreadInputTokens: 1, MaxMainThreadOutputTokens: 1,
				MaxEstimatedCostMicroUSD: 100, MaxDurationMillis: 1,
			},
		},
	}
}
