package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
)

func evaluateAgentWorkflow(t *testing.T, scenarioFile string, observation agenteval.Observation) agenteval.Result {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", "agent-eval", scenarioFile))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scenario, err := agenteval.DecodeScenario(file)
	if err != nil {
		t.Fatalf("decode agent scenario: %v", err)
	}
	result, err := agenteval.Evaluate(scenario, observation)
	if err != nil {
		t.Fatalf("evaluate agent scenario: %v", err)
	}
	if result.Status != "pass" {
		t.Fatalf("agent workflow regression: %+v", result.Violations)
	}
	return result
}

func deterministicObservation(scenarioID string, atlInvocations int, outputBytes int64, requests []capturedReq, checks map[string]bool) agenteval.Observation {
	methods := map[string]int{}
	for _, request := range requests {
		methods[request.method]++
	}
	return agenteval.Observation{
		SchemaVersion: agenteval.ObservationSchemaVersion,
		ScenarioID:    scenarioID,
		Variant:       "contract",
		Runtime:       agenteval.Runtime{Provider: "deterministic", ATLVersion: "test-build"},
		Metrics:       agenteval.InputMetrics{ATLInvocations: atlInvocations, OutputBytes: outputBytes},
		Coverage: map[string]bool{
			"atl_invocations": true, "backend_requests": true, "output_bytes": true,
		},
		HTTPMethods: methods,
		Checks:      checks,
	}
}
