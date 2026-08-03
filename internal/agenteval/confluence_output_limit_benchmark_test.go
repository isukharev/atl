package agenteval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRepositoryConfluenceOutputLimitFixturesDriveProviderOracles(t *testing.T) {
	tests := []struct {
		name, directory, topic, query string
		repetitions                   int
	}{
		{"primary", "confluence-output-limit-mcp", "Silver retention decision", `siteSearch ~ "Silver retention decision"`, 3},
		{"holdout", "confluence-output-limit-mcp-holdout", "Coral failover approval", `siteSearch ~ "Coral failover approval"`, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			if len(fixture.Routes) != 1 || fixture.Routes[0].Status != 200 ||
				len(fixture.Routes[0].Responses) != 0 {
				t.Fatalf("output-limit fixture route drifted: %+v", fixture.Routes)
			}
			scenario := loadRepositoryScenario(t, filepath.Join(root, "scenario.v1.json"))
			final, err := json.Marshal(map[string]any{
				"topic": test.topic, "query": test.query, "selected_max_bytes": 1024,
				"source_status":     map[string]any{"confluence_search": "output_limit_exceeded"},
				"evidence_complete": false, "decision": "undetermined",
				"observed_facts":     []string{"selected_bound_rejected_complete_result"},
				"source_claims":      []string{},
				"access_limitations": []string{"no_result_returned"},
				"no_agent_retry":     true, "bound_raised": false, "partial_result_used": false,
				"brief": "output_bound_prevented_grounded_decision",
			})
			if err != nil {
				t.Fatal(err)
			}
			invocations := []MCPInvocation{
				mustMCPInvocation(t, "confluence_search", map[string]any{
					"cql": test.query, "limit": 10, "max_bytes": 1024,
				}),
			}
			families := []CapabilityFamilyMetric{
				{Family: "confluence.search", Invocations: 1, Failures: 1},
			}
			sequence := []string{"confluence.search"}
			methods := map[string]int{"GET": 1}
			for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, runFile))
				if spec.Repetitions != test.repetitions ||
					!slices.Equal(spec.AllowedMCPTools, []string{"confluence_search"}) {
					t.Fatalf("route contract drifted: %+v", spec)
				}
				if scenario.Budgets.MaxInterfaceInvocations != 1 ||
					scenario.Budgets.MaxBackendRequests != 1 ||
					scenario.Budgets.MaxDuplicateBackendRequests != 0 ||
					scenario.Budgets.MaxRemoteWrites != 0 {
					t.Fatalf("budgets drifted: %+v", scenario.Budgets)
				}
				assertJiraPaginatedSearchSchemaMatchesFinal(t, root, spec, final)
				if !equalMCPInvocations(repositoryExpectedMCPInvocations(t, spec), invocations) {
					t.Fatalf("%s invocations drifted", spec.Provider)
				}
				results, err := evaluateRunChecksWithMCPInvocations(
					spec.Checks, final, "", 1, 1, 0, 0, nil, 0, 0,
					methods, true, nil, families, true, sequence, invocations, true,
				)
				if err != nil {
					t.Fatal(err)
				}
				for name, passed := range results {
					if !passed {
						t.Fatalf("%s fixture final failed %q", spec.Provider, name)
					}
				}
				assertConfluenceOutputLimitMutationsFail(t, spec, final, invocations)
			}
		})
	}
}

func TestRepositoryConfluenceOutputLimitSamplingPairIdentity(t *testing.T) {
	pair := loadRepositorySamplingPairContract(t, "confluence-output-limit-mcp")
	primary, holdout := pair.Primary.Root, pair.Holdout.Root
	mainSchema, err := os.ReadFile(filepath.Join(primary, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	hiddenSchema, err := os.ReadFile(filepath.Join(holdout, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mainSchema, hiddenSchema) {
		t.Fatal("primary and holdout schemas drifted")
	}
	for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
		main, hidden := pair.Primary.Runs[runFile], pair.Holdout.Runs[runFile]
		if main.Reasoning != "high" || hidden.Reasoning != "high" ||
			!slices.Equal(main.AllowedMCPTools, hidden.AllowedMCPTools) {
			t.Fatalf("pair drifted: primary=%+v holdout=%+v", main, hidden)
		}
	}
}

func assertConfluenceOutputLimitMutationsFail(
	t *testing.T,
	spec RunSpec,
	final []byte,
	invocations []MCPInvocation,
) {
	t.Helper()
	methods := map[string]int{"GET": 1}
	sequence := []string{"confluence.search"}
	families := []CapabilityFamilyMetric{{Family: "confluence.search", Invocations: 1, Failures: 1}}

	asSuccess := []CapabilityFamilyMetric{{Family: "confluence.search", Invocations: 1, Successes: 1}}
	results, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 1, 0, 0, 0, nil, 0, 0,
		methods, true, nil, asSuccess, true, sequence, invocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results["expected_failure"] || results["route_exact"] {
		t.Fatal("output-limit rejection misclassified as success")
	}

	raised := slices.Clone(invocations)
	raised[0] = mustMCPInvocation(t, "confluence_search", map[string]any{
		"cql":   invocationStringArgument(t, invocations[0], "cql"),
		"limit": 10, "max_bytes": 2048,
	})
	results, err = evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 1, 1, 0, 0, nil, 0, 0,
		methods, true, nil, families, true, sequence, raised, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results["route_arguments"] {
		t.Fatal("raised bound passed exact invocation oracle")
	}

	retried := append(slices.Clone(invocations), invocations[0])
	retryFamilies := []CapabilityFamilyMetric{{Family: "confluence.search", Invocations: 2, Failures: 2}}
	results, err = evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 2, 2, 0, 0, nil, 0, 0,
		map[string]int{"GET": 2}, true, nil, retryFamilies, true,
		[]string{"confluence.search", "confluence.search"}, retried, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results["bounded_interface"] || results["http_exact"] || results["route_arguments"] ||
		results["route_exact"] || results["route_ordered"] {
		t.Fatal("second agent-level search passed route oracle")
	}

	var claimed map[string]any
	if err := json.Unmarshal(final, &claimed); err != nil {
		t.Fatal(err)
	}
	claimed["decision"] = "approved"
	claimed["source_claims"] = []string{"The rejected payload approved the change."}
	claimed["observed_facts"] = []string{"PRIMARY_OUTPUT_LIMIT_PAYLOAD approved the change."}
	claimed["brief"] = "The rejected payload approved the change."
	claimed["access_limitations"] = []string{"The rejected payload was enough."}
	claimed["no_agent_retry"] = false
	claimed["bound_raised"] = true
	claimed["partial_result_used"] = true
	claimedFinal, err := json.Marshal(claimed)
	if err != nil {
		t.Fatal(err)
	}
	results, err = evaluateRunChecksWithMCPInvocations(
		spec.Checks, claimedFinal, "", 1, 1, 0, 0, nil, 0, 0,
		methods, true, nil, families, true, sequence, invocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results["decision_undetermined"] || results["source_claims_empty"] ||
		results["access_exact"] || results["facts_exact"] || results["brief_exact"] ||
		results["no_agent_retry"] ||
		results["bound_not_raised"] || results["partial_result_unused"] {
		t.Fatal("unsupported rejected-payload claim passed semantic oracle")
	}
}

func invocationStringArgument(t *testing.T, invocation MCPInvocation, name string) string {
	t.Helper()
	var arguments map[string]any
	if err := json.Unmarshal(invocation.Arguments, &arguments); err != nil {
		t.Fatal(err)
	}
	value, ok := arguments[name].(string)
	if !ok {
		t.Fatalf("argument %q=%#v", name, arguments[name])
	}
	return value
}
