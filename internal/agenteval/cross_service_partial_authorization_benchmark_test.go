package agenteval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRepositoryCrossServicePartialAuthorizationFixturesDriveProviderOracles(t *testing.T) {
	tests := []struct {
		name, directory, topic, jiraKey, jiraStatus, pageID, heading string
		jiraQuery, confluenceQuery                                   string
		facts                                                        []string
	}{
		{
			name: "primary", directory: "cross-service-partial-authorization-mcp",
			topic: "Orchid migration readiness", jiraKey: "OPS-217", jiraStatus: "In Review",
			pageID: "9301", heading: "Current decision",
			jiraQuery:       `text ~ "Orchid migration readiness" ORDER BY updated DESC`,
			confluenceQuery: `siteSearch ~ "Orchid migration readiness"`,
			facts: []string{
				"Jira OPS-217 is In Review.",
				"Confluence page 9301 is titled Orchid migration readiness record.",
			},
		},
		{
			name: "holdout", directory: "cross-service-partial-authorization-mcp-holdout",
			topic: "Cobalt failover readiness", jiraKey: "SRE-328", jiraStatus: "Blocked",
			pageID: "9402", heading: "Outcome",
			jiraQuery:       `text ~ "Cobalt failover readiness" ORDER BY updated DESC`,
			confluenceQuery: `siteSearch ~ "Cobalt failover readiness"`,
			facts: []string{
				"Jira SRE-328 is Blocked.",
				"Confluence page 9402 is titled Cobalt failover readiness record.",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			_ = loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			scenario := loadRepositoryScenario(t, filepath.Join(root, "scenario.v1.json"))
			repetitions := 3
			if test.name == "holdout" {
				repetitions = 1
			}
			final, err := json.Marshal(map[string]any{
				"topic": test.topic, "jira_key": test.jiraKey, "jira_status": test.jiraStatus,
				"page_id": test.pageID,
				"queries": map[string]any{"jira": test.jiraQuery, "confluence": test.confluenceQuery},
				"source_status": map[string]any{
					"jira_search": "complete", "confluence_search": "complete",
					"confluence_section": "forbidden",
				},
				"evidence_complete": false, "decision": "undetermined",
				"observed_facts": test.facts, "section_claims": []string{},
				"access_limitations": []string{
					"The selected Confluence section is forbidden; its decision cannot be determined.",
				},
				"no_retry_attempted": true,
				"brief":              "Discovery succeeded, but the restricted section prevents a grounded decision.",
			})
			if err != nil {
				t.Fatal(err)
			}
			invocations := []MCPInvocation{
				mustMCPInvocation(t, "jira_issue_search", map[string]any{
					"jql": test.jiraQuery, "columns": []string{"key", "summary", "status", "updated"},
					"limit": 10, "max_bytes": 131072,
				}),
				mustMCPInvocation(t, "confluence_search", map[string]any{
					"cql": test.confluenceQuery, "limit": 10, "max_bytes": 131072,
				}),
				mustMCPInvocation(t, "confluence_page_section", map[string]any{
					"reference": test.pageID, "heading": test.heading,
					"occurrence": 1, "max_bytes": 32768,
				}),
			}
			families := []CapabilityFamilyMetric{
				{Family: "confluence.page.section", Invocations: 1, Failures: 1},
				{Family: "confluence.search", Invocations: 1, Successes: 1, OutputBytes: 1},
				{Family: "jira.issue.search", Invocations: 1, Successes: 1, OutputBytes: 1},
			}
			sequence := []string{"jira.issue.search", "confluence.search", "confluence.page.section"}
			methods := map[string]int{"GET": 3}
			for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, runFile))
				assertCrossServicePartialAuthorizationContract(t, scenario, spec, repetitions)
				assertJiraPaginatedSearchSchemaMatchesFinal(t, root, spec, final)
				if declared := repositoryExpectedMCPInvocations(t, spec); !equalMCPInvocations(declared, invocations) {
					t.Fatalf("%s invocation drifted", spec.Provider)
				}
				results, err := evaluateRunChecksWithMCPInvocations(
					spec.Checks, final, "", 3, 1, 0, 0, nil, 0, 0,
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
				var paraphrased map[string]any
				if err := json.Unmarshal(final, &paraphrased); err != nil {
					t.Fatal(err)
				}
				paraphrased["observed_facts"] = []string{
					"The selected Jira issue and its status were observed.",
					"The selected Confluence page identity was observed.",
					"The selected section could not be read.",
				}
				paraphrased["access_limitations"] = []string{
					"Authorization denied for the selected section.",
				}
				paraphrasedFinal, err := json.Marshal(paraphrased)
				if err != nil {
					t.Fatal(err)
				}
				paraphrasedResults, err := evaluateRunChecksWithMCPInvocations(
					spec.Checks, paraphrasedFinal, "", 3, 1, 0, 0, nil, 0, 0,
					methods, true, nil, families, true, sequence, invocations, true,
				)
				if err != nil {
					t.Fatal(err)
				}
				for name, passed := range paraphrasedResults {
					if !passed {
						t.Fatalf("%s grounded paraphrase failed %q", spec.Provider, name)
					}
				}
				assertCrossServicePartialAuthorizationMutationsFail(
					t, spec, final, methods, families, sequence, invocations,
				)
			}
		})
	}
}

func TestRepositoryCrossServicePartialAuthorizationSamplingPairIdentity(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	primary := filepath.Join(root, "cross-service-partial-authorization-mcp")
	holdout := filepath.Join(root, "cross-service-partial-authorization-mcp-holdout")
	primarySchema, err := os.ReadFile(filepath.Join(primary, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	holdoutSchema, err := os.ReadFile(filepath.Join(holdout, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(primarySchema, holdoutSchema) {
		t.Fatal("primary and holdout response schemas drifted")
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

func assertCrossServicePartialAuthorizationContract(
	t *testing.T,
	scenario Scenario,
	spec RunSpec,
	repetitions int,
) {
	t.Helper()
	expectedTools := []string{"jira_issue_search", "confluence_search", "confluence_page_section"}
	if spec.EffectiveSurface() != SurfaceATLMCP ||
		spec.EffectiveToolTransport() != "mcp" ||
		!slices.Equal(spec.AllowedMCPTools, expectedTools) ||
		len(spec.AllowedTools) != 0 ||
		len(spec.AllowedATLCommands) != 0 ||
		spec.Variant != "cross-service-partial-authorization-mcp-v1" ||
		spec.Repetitions != repetitions {
		t.Fatalf("typed route drifted: %+v", spec)
	}
	if scenario.Budgets.MaxInterfaceInvocations != 3 ||
		scenario.Budgets.MaxBackendRequests != 3 ||
		scenario.Budgets.MaxDuplicateBackendRequests != 0 ||
		scenario.Budgets.MaxRemoteWrites != 0 ||
		!slices.Equal(scenario.Budgets.AllowedHTTPMethods, []string{"GET"}) {
		t.Fatalf("transport budget drifted: %+v", scenario.Budgets)
	}
}

func assertCrossServicePartialAuthorizationMutationsFail(
	t *testing.T,
	spec RunSpec,
	final []byte,
	methods map[string]int,
	families []CapabilityFamilyMetric,
	sequence []string,
	invocations []MCPInvocation,
) {
	t.Helper()
	mutatedFamilies := slices.Clone(families)
	mutatedFamilies[0].Successes = 1
	mutatedFamilies[0].Failures = 0
	results, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 3, 0, 0, 0, nil, 0, 0,
		methods, true, nil, mutatedFamilies, true,
		sequence, invocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results["expected_failure"] || results["route_exact"] {
		t.Fatal("forbidden section misclassified as success passed failure oracle")
	}

	mutatedFamilies = slices.Clone(families)
	mutatedFamilies[0].Invocations = 2
	mutatedFamilies[0].Failures = 2
	mutatedInvocations := append(slices.Clone(invocations), invocations[2])
	mutatedSequence := append(slices.Clone(sequence), sequence[2])
	results, err = evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 4, 2, 0, 0, nil, 0, 0,
		map[string]int{"GET": 4}, true, nil, mutatedFamilies, true,
		mutatedSequence, mutatedInvocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bounded_interface", "http_exact", "route_arguments", "route_exact", "route_ordered"} {
		if results[name] {
			t.Fatalf("retried forbidden section passed %q", name)
		}
	}

	var claimed map[string]any
	if err := json.Unmarshal(final, &claimed); err != nil {
		t.Fatal(err)
	}
	claimed["decision"] = "approved"
	claimed["section_claims"] = []string{"The migration is approved."}
	claimedFinal, err := json.Marshal(claimed)
	if err != nil {
		t.Fatal(err)
	}
	results, err = evaluateRunChecksWithMCPInvocations(
		spec.Checks, claimedFinal, "", 3, 1, 0, 0, nil, 0, 0,
		methods, true, nil, families, true, sequence, invocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if results["decision_undetermined"] || results["section_claims_empty"] {
		t.Fatal("unsupported restricted-section claim passed semantic checks")
	}

	for _, test := range []struct {
		name, check string
		value       any
	}{
		{name: "missing-access-limitation", check: "access_qualified", value: []string{}},
		{name: "missing-accessible-facts", check: "observed_facts_present", value: []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var incomplete map[string]any
			if err := json.Unmarshal(final, &incomplete); err != nil {
				t.Fatal(err)
			}
			if test.check == "access_qualified" {
				incomplete["access_limitations"] = test.value
			} else {
				incomplete["observed_facts"] = test.value
			}
			incompleteFinal, err := json.Marshal(incomplete)
			if err != nil {
				t.Fatal(err)
			}
			results, err := evaluateRunChecksWithMCPInvocations(
				spec.Checks, incompleteFinal, "", 3, 1, 0, 0, nil, 0, 0,
				methods, true, nil, families, true, sequence, invocations, true,
			)
			if err != nil {
				t.Fatal(err)
			}
			if results[test.check] {
				t.Fatalf("empty evidence passed %q", test.check)
			}
		})
	}
}
