package agenteval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRepositoryConfluenceStaleNotFoundFixturesDriveProviderOracles(t *testing.T) {
	tests := []struct {
		name, directory, topic, pageID, query, heading string
		repetitions                                    int
	}{
		{"primary", "confluence-stale-not-found-mcp", "Quartz retention decision", "9501", `siteSearch ~ "Quartz retention decision"`, "Current decision", 3},
		{"holdout", "confluence-stale-not-found-mcp-holdout", "Saffron failover approval", "9602", `siteSearch ~ "Saffron failover approval"`, "Approval", 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			_ = loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			scenario := loadRepositoryScenario(t, filepath.Join(root, "scenario.v1.json"))
			final, err := json.Marshal(map[string]any{
				"topic": test.topic, "page_id": test.pageID, "query": test.query,
				"source_status":     map[string]any{"confluence_search": "complete", "confluence_section": "not_found"},
				"evidence_complete": false, "decision": "undetermined",
				"observed_facts": []string{"The search returned one selected candidate."},
				"section_claims": []string{}, "access_limitations": []string{"The selected section was not found."},
				"no_retry_attempted": true, "brief": "The indexed candidate is stale, so no decision can be grounded.",
			})
			if err != nil {
				t.Fatal(err)
			}
			invocations := []MCPInvocation{
				mustMCPInvocation(t, "confluence_search", map[string]any{"cql": test.query, "limit": 10, "max_bytes": 131072}),
				mustMCPInvocation(t, "confluence_page_section", map[string]any{
					"reference": test.pageID, "heading": test.heading, "occurrence": 1, "max_bytes": 32768,
				}),
			}
			families := []CapabilityFamilyMetric{
				{Family: "confluence.page.section", Invocations: 1, Failures: 1},
				{Family: "confluence.search", Invocations: 1, Successes: 1, OutputBytes: 1},
			}
			sequence := []string{"confluence.search", "confluence.page.section"}
			methods := map[string]int{"GET": 2}
			for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, runFile))
				if spec.Repetitions != test.repetitions ||
					!slices.Equal(spec.AllowedMCPTools, []string{"confluence_search", "confluence_page_section"}) {
					t.Fatalf("route contract drifted: %+v", spec)
				}
				if scenario.Budgets.MaxInterfaceInvocations != 2 ||
					scenario.Budgets.MaxBackendRequests != 2 ||
					scenario.Budgets.MaxDuplicateBackendRequests != 0 ||
					scenario.Budgets.MaxRemoteWrites != 0 {
					t.Fatalf("budgets drifted: %+v", scenario.Budgets)
				}
				assertJiraPaginatedSearchSchemaMatchesFinal(t, root, spec, final)
				if !equalMCPInvocations(repositoryExpectedMCPInvocations(t, spec), invocations) {
					t.Fatalf("%s invocations drifted", spec.Provider)
				}
				results, err := evaluateRunChecksWithMCPInvocations(
					spec.Checks, final, "", 2, 1, 0, 0, nil, 0, 0,
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
				assertConfluenceStaleNotFoundMutationsFail(t, spec, final, methods, families, sequence, invocations)
			}
		})
	}
}

func TestRepositoryConfluenceStaleNotFoundSamplingPairIdentity(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	primary := filepath.Join(root, "confluence-stale-not-found-mcp")
	holdout := filepath.Join(root, "confluence-stale-not-found-mcp-holdout")
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
		main := loadRepositoryRunSpec(t, filepath.Join(primary, runFile))
		hidden := loadRepositoryRunSpec(t, filepath.Join(holdout, runFile))
		if main.Variant != hidden.Variant || main.Repetitions != 3 || hidden.Repetitions != 1 ||
			main.Model != hidden.Model || main.Reasoning != "high" || hidden.Reasoning != "high" ||
			!slices.Equal(main.AllowedMCPTools, hidden.AllowedMCPTools) {
			t.Fatalf("pair drifted: primary=%+v holdout=%+v", main, hidden)
		}
	}
}

func assertConfluenceStaleNotFoundMutationsFail(
	t *testing.T,
	spec RunSpec,
	final []byte,
	methods map[string]int,
	families []CapabilityFamilyMetric,
	sequence []string,
	invocations []MCPInvocation,
) {
	t.Helper()
	asSuccess := slices.Clone(families)
	asSuccess[0].Successes, asSuccess[0].Failures = 1, 0
	results, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 2, 0, 0, 0, nil, 0, 0,
		methods, true, nil, asSuccess, true, sequence, invocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results["expected_failure"] || results["route_exact"] {
		t.Fatal("not-found expansion misclassified as success")
	}
	retried := append(slices.Clone(invocations), invocations[1])
	retryFamilies := slices.Clone(families)
	retryFamilies[0].Invocations, retryFamilies[0].Failures = 2, 2
	results, err = evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 3, 2, 0, 0, nil, 0, 0,
		map[string]int{"GET": 3}, true, nil, retryFamilies, true,
		append(slices.Clone(sequence), sequence[1]), retried, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results["bounded_interface"] || results["http_exact"] || results["route_arguments"] || results["route_exact"] || results["route_ordered"] {
		t.Fatal("retried missing section passed route oracle")
	}
	var claimed map[string]any
	if err := json.Unmarshal(final, &claimed); err != nil {
		t.Fatal(err)
	}
	claimed["decision"] = "approved"
	claimed["section_claims"] = []string{"The missing section approved the change."}
	claimed["access_limitations"] = []string{}
	claimedFinal, err := json.Marshal(claimed)
	if err != nil {
		t.Fatal(err)
	}
	results, err = evaluateRunChecksWithMCPInvocations(
		spec.Checks, claimedFinal, "", 2, 1, 0, 0, nil, 0, 0,
		methods, true, nil, families, true, sequence, invocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results["decision_undetermined"] || results["section_claims_empty"] || results["access_qualified"] {
		t.Fatal("unsupported missing-section claim passed semantic oracle")
	}
}
