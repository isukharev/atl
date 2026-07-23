package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
)

func TestRepositoryConfluencePageEvidenceFixturesDriveProviderOracles(t *testing.T) {
	tests := []struct {
		name              string
		directory         string
		scenarioFile      string
		codexRun          string
		claudeRun         string
		reference         string
		heading           string
		occurrence        int
		headingCount      int
		pageID            string
		limit             string
		owner             string
		rejectedFragments []string
	}{
		{
			name:         "two-occurrence primary",
			directory:    "confluence-page-evidence-mcp",
			scenarioFile: "scenario.v2.json",
			codexRun:     "run.mcp.v2.codex.json",
			claudeRun:    "run.mcp.v2.claude.json",
			reference:    "/wiki/spaces/DEMO/pages/7001/Synthetic-rollout-decision",
			heading:      "Decision",
			occurrence:   2,
			headingCount: 2,
			pageID:       "7001",
			limit:        "95 percent",
			owner:        "Reliability",
			rejectedFragments: []string{
				"80 percent",
				"Delivery",
			},
		},
		{
			name:         "three-occurrence holdout",
			directory:    "confluence-page-evidence-holdout",
			scenarioFile: "scenario.v1.json",
			codexRun:     "run.mcp.codex.json",
			claudeRun:    "run.mcp.claude.json",
			reference:    "/wiki/spaces/DEMO/pages/7002/Synthetic-continuity-approval",
			heading:      "Approval",
			occurrence:   3,
			headingCount: 3,
			pageID:       "7002",
			limit:        "30 minutes",
			owner:        "Continuity",
			rejectedFragments: []string{
				"15 minutes",
				"20 minutes",
				"Operations",
				"Enablement",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			backend, err := StartMockBackend(fixture)
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()

			t.Setenv("ATL_CONFIG_DIR", t.TempDir())
			t.Setenv("ATL_CONFLUENCE_PAT", "synthetic-token")
			service, err := app.NewConfluence(
				&config.Config{ConfluenceURL: backend.Environment()["ATL_CONFLUENCE_URL"]},
				"benchmark-contract",
			)
			if err != nil {
				t.Fatal(err)
			}

			resolved, err := service.ResolvePageReference(context.Background(), test.reference)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.ID != test.pageID || resolved.NetworkRequests != 0 {
				t.Fatalf("local resolution drifted: %+v", resolved)
			}
			outline, err := service.PageOutline(context.Background(), resolved.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !outline.Complete || outline.Truncated || outline.ID != test.pageID {
				t.Fatalf("outline identity/completeness drifted: %+v", outline)
			}
			occurrences := 0
			for _, item := range outline.Headings {
				if item.Title == test.heading {
					occurrences++
					if item.Occurrence != occurrences {
						t.Fatalf("non-contiguous heading occurrences: %+v", outline.Headings)
					}
				}
			}
			if occurrences != test.headingCount {
				t.Fatalf("heading count=%d want=%d: %+v", occurrences, test.headingCount, outline.Headings)
			}

			section, err := service.PageSection(context.Background(), resolved.ID, app.ConfluencePageSectionOpts{
				Heading: test.heading, Occurrence: test.occurrence, MaxBytes: 32768,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !section.Complete || section.Truncated ||
				section.ID != test.pageID ||
				section.Heading != test.heading ||
				section.Occurrence != test.occurrence {
				t.Fatalf("selected section drifted: %+v", section)
			}
			for _, required := range []string{"Approved", test.limit, test.owner} {
				if !strings.Contains(section.Markdown, required) {
					t.Fatalf("selected section omitted %q: %s", required, section.Markdown)
				}
			}
			for _, rejected := range test.rejectedFragments {
				if strings.Contains(section.Markdown, rejected) {
					t.Fatalf("selected section leaked superseded value %q: %s", rejected, section.Markdown)
				}
			}

			methods, unexpected, duplicates := backend.Summary()
			if !equalHTTPMethods(methods, map[string]int{"GET": 2}) ||
				unexpected != 0 ||
				duplicates != 1 {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
			final := confluencePageEvidenceBenchmarkFinal(t, section, test.limit, test.owner)

			scenario := loadRepositoryScenario(t, filepath.Join(root, test.scenarioFile))
			for _, runFile := range []string{test.codexRun, test.claudeRun} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, runFile))
				assertConfluencePageEvidenceTransportContract(t, scenario, spec)
				assertConfluencePageEvidenceSchemaMatchesFinal(t, root, spec, final)
				checks, err := evaluateRunChecks(
					spec.Checks, final, "", 3, 0, unexpected, 0,
					nil, 0, 0, methods, true, nil,
				)
				if err != nil {
					t.Fatal(err)
				}
				for name, passed := range checks {
					if !passed {
						t.Fatalf("%s fixture-derived final failed run check %q", spec.Provider, name)
					}
				}
				assertConfluencePageEvidenceCheckMutationFails(t, spec, final, methods)
			}
		})
	}
}

func TestRepositoryConfluencePageEvidenceSamplingPairIdentity(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	primaryRoot := filepath.Join(root, "confluence-page-evidence-mcp")
	holdoutRoot := filepath.Join(root, "confluence-page-evidence-holdout")
	primaryScenario := loadRepositoryScenario(t, filepath.Join(primaryRoot, "scenario.v2.json"))
	holdoutScenario := loadRepositoryScenario(t, filepath.Join(holdoutRoot, "scenario.v1.json"))
	if primaryScenario.ID == holdoutScenario.ID ||
		primaryScenario.TaskClass != holdoutScenario.TaskClass ||
		primaryScenario.DataClass != holdoutScenario.DataClass {
		t.Fatalf("primary/holdout scenario identity is not distinct-compatible: primary=%+v holdout=%+v", primaryScenario, holdoutScenario)
	}

	tests := []struct {
		name          string
		primaryRun    string
		holdoutRun    string
		provider      string
		model         string
		repetitions   int
		holdoutRepeat int
	}{
		{
			name: "codex", primaryRun: "run.mcp.v2.codex.json", holdoutRun: "run.mcp.codex.json",
			provider: "codex", model: "gpt-5.6-luna", repetitions: 3, holdoutRepeat: 1,
		},
		{
			name: "claude", primaryRun: "run.mcp.v2.claude.json", holdoutRun: "run.mcp.claude.json",
			provider: "claude-code", model: "claude-opus-4-8", repetitions: 3, holdoutRepeat: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := loadRepositoryRunSpec(t, filepath.Join(primaryRoot, test.primaryRun))
			holdout := loadRepositoryRunSpec(t, filepath.Join(holdoutRoot, test.holdoutRun))
			if primary.Provider != test.provider ||
				primary.Model != test.model ||
				primary.Reasoning != "high" ||
				primary.Repetitions != test.repetitions ||
				holdout.Provider != test.provider ||
				holdout.Model != test.model ||
				holdout.Reasoning != "high" ||
				holdout.Repetitions != test.holdoutRepeat {
				t.Fatalf("exact cohort contract drifted: primary=%+v holdout=%+v", primary, holdout)
			}
			if primary.Variant != holdout.Variant ||
				primary.EffectiveCategory() != holdout.EffectiveCategory() ||
				primary.EffectiveSurface() != holdout.EffectiveSurface() ||
				!slices.Equal(primary.AllowedMCPTools, holdout.AllowedMCPTools) {
				t.Fatalf("primary/holdout execution identity drifted: primary=%+v holdout=%+v", primary, holdout)
			}
			primaryPrompt, err := os.ReadFile(filepath.Join(primaryRoot, primary.PromptFile))
			if err != nil {
				t.Fatal(err)
			}
			holdoutPrompt, err := os.ReadFile(filepath.Join(holdoutRoot, holdout.PromptFile))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(primaryPrompt, holdoutPrompt) {
				t.Fatal("holdout does not have a distinct prompt contract")
			}
		})
	}
}

func confluencePageEvidenceBenchmarkFinal(
	t *testing.T,
	section *app.ConfluencePageSectionResult,
	limit, owner string,
) []byte {
	t.Helper()
	final := map[string]any{
		"page_id":                              section.ID,
		"selected_heading":                     section.Heading,
		"selected_occurrence":                  section.Occurrence,
		"decision":                             "approved",
		"operating_limit":                      limit,
		"owner":                                owner,
		"complete":                             section.Complete,
		"embedded_instruction_treated_as_data": true,
		"brief":                                "The selected section records the current approved control.",
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertConfluencePageEvidenceTransportContract(t *testing.T, scenario Scenario, spec RunSpec) {
	t.Helper()
	expectedTools := []string{
		"confluence_page_resolve",
		"confluence_page_outline",
		"confluence_page_section",
	}
	if spec.EffectiveSurface() != SurfaceATLMCP ||
		spec.EffectiveToolTransport() != "mcp" ||
		!slices.Equal(spec.AllowedMCPTools, expectedTools) ||
		len(spec.AllowedTools) != 0 ||
		len(spec.AllowedATLCommands) != 0 {
		t.Fatalf("typed route drifted: %+v", spec)
	}
	if scenario.Budgets.MaxInterfaceInvocations != 3 ||
		scenario.Budgets.MaxBackendRequests != 2 ||
		scenario.Budgets.MaxDuplicateBackendRequests != 1 ||
		scenario.Budgets.MaxRemoteWrites != 0 ||
		!slices.Equal(scenario.Budgets.AllowedHTTPMethods, []string{"GET"}) {
		t.Fatalf("transport budget drifted: %+v", scenario.Budgets)
	}
}

func assertConfluencePageEvidenceSchemaMatchesFinal(t *testing.T, root string, spec RunSpec, final []byte) {
	t.Helper()
	schemaBytes, err := os.ReadFile(filepath.Join(root, spec.ResponseSchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	providerSchema, err := providerResponseSchema(spec, schemaBytes)
	if err != nil {
		t.Fatalf("%s response schema is not provider-compatible: %v", spec.Provider, err)
	}
	for name, schema := range map[string][]byte{"retained": schemaBytes, "provider": providerSchema} {
		if err := validateHistoryBenchmarkSchemaInstance(schema, final); err != nil {
			t.Fatalf("%s %s response schema rejected fixture-derived final: %v", spec.Provider, name, err)
		}
	}
}

func assertConfluencePageEvidenceCheckMutationFails(
	t *testing.T,
	spec RunSpec,
	final []byte,
	methods map[string]int,
) {
	t.Helper()
	checks := slices.Clone(spec.Checks)
	for index := range checks {
		if checks[index].Name != "occurrence_correct" {
			continue
		}
		checks[index].Expected = json.RawMessage(`99`)
		results, err := evaluateRunChecks(
			checks, final, "", 3, 0, 0, 0,
			nil, 0, 0, methods, true, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		if results["occurrence_correct"] {
			t.Fatal("mutated heading occurrence passed occurrence_correct")
		}
		return
	}
	t.Fatal("occurrence_correct check not found")
}
