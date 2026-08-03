package agenteval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRepositoryConfluenceRateLimitFixturesDriveProviderOracles(t *testing.T) {
	tests := []struct {
		name, directory, topic, query string
		repetitions                   int
	}{
		{"primary", "confluence-rate-limit-mcp", "Amber quota decision", `siteSearch ~ "Amber quota decision"`, 3},
		{"holdout", "confluence-rate-limit-mcp-holdout", "Indigo recovery approval", `siteSearch ~ "Indigo recovery approval"`, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			if len(fixture.Routes) != 1 || len(fixture.Routes[0].Responses) != 4 {
				t.Fatalf("rate-limit fixture route drifted: %+v", fixture.Routes)
			}
			for _, response := range fixture.Routes[0].Responses {
				if response.Status != 429 {
					t.Fatalf("rate-limit response=%+v", response)
				}
			}
			scenario := loadRepositoryScenario(t, filepath.Join(root, "scenario.v1.json"))
			final, err := json.Marshal(map[string]any{
				"topic": test.topic, "query": test.query,
				"source_status":     map[string]any{"confluence_search": "rate_limited"},
				"evidence_complete": false, "decision": "undetermined",
				"observed_facts":     []string{"The bounded search ended with a rate limit."},
				"source_claims":      []string{},
				"access_limitations": []string{"The source returned no usable search evidence."},
				"no_agent_retry":     true,
				"brief":              "The source is rate limited, so no current decision can be grounded.",
			})
			if err != nil {
				t.Fatal(err)
			}
			invocations := []MCPInvocation{
				mustMCPInvocation(t, "confluence_search", map[string]any{
					"cql": test.query, "limit": 10, "max_bytes": 131072,
				}),
			}
			families := []CapabilityFamilyMetric{
				{Family: "confluence.search", Invocations: 1, Failures: 1},
			}
			sequence := []string{"confluence.search"}
			methods := map[string]int{"GET": 4}
			for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, runFile))
				if spec.Repetitions != test.repetitions ||
					!slices.Equal(spec.AllowedMCPTools, []string{"confluence_search"}) {
					t.Fatalf("route contract drifted: %+v", spec)
				}
				if scenario.Budgets.MaxInterfaceInvocations != 1 ||
					scenario.Budgets.MaxBackendRequests != 4 ||
					scenario.Budgets.MaxDuplicateBackendRequests != 3 ||
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
				assertConfluenceRateLimitMutationsFail(t, spec, final, invocations)
			}
		})
	}
}

func TestRepositoryConfluenceRateLimitSamplingPairIdentity(t *testing.T) {
	pair := loadRepositorySamplingPairContract(t, "confluence-rate-limit-mcp")
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

func assertConfluenceRateLimitMutationsFail(
	t *testing.T,
	spec RunSpec,
	final []byte,
	invocations []MCPInvocation,
) {
	t.Helper()
	methods := map[string]int{"GET": 4}
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
		t.Fatal("rate limit misclassified as success")
	}

	retried := append(slices.Clone(invocations), invocations[0])
	retryFamilies := []CapabilityFamilyMetric{{Family: "confluence.search", Invocations: 2, Failures: 2}}
	results, err = evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 2, 2, 0, 0, nil, 0, 0,
		map[string]int{"GET": 8}, true, nil, retryFamilies, true,
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
	claimed["source_claims"] = []string{"The unavailable source approved the change."}
	claimed["access_limitations"] = []string{}
	claimed["no_agent_retry"] = false
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
		results["access_qualified"] || results["no_agent_retry"] {
		t.Fatal("unsupported rate-limited source claim passed semantic oracle")
	}
}
