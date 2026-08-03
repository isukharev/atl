package agenteval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

const (
	jiraReferenceMCPTool   = "jira_issue_refs"
	jiraReferenceMCPFamily = "jira.issue.refs"
	// The byte bound both prompts pin. It bounds the encoded result only, so it
	// never changes the backend route.
	jiraReferenceMCPMaxBytes = 32768
	jiraReferenceMCPBrief    = "one bounded reference summary read; selection, counts, and source qualification reported exactly as returned"
)

// jiraReferenceMCPCohort is one committed built-in MCP cohort. The selector is
// the exact argument set both run specs declare; the class shape fields pin
// what makes the cohort distinct from its pair, never the arithmetic itself.
type jiraReferenceMCPCohort struct {
	directory  string
	scenarioID string

	key    string
	jql    string
	fields []string
	limit  int

	repetitions  int
	expectedGETs int

	wantMode         string
	wantIssueKeys    []string
	wantComplete     bool
	wantTruncated    bool
	wantSharedKind   string
	wantEmptySource  bool
	wantSelectedFrom string
}

func jiraReferenceMCPCohorts() []jiraReferenceMCPCohort {
	return []jiraReferenceMCPCohort{
		{
			directory:        "jira-reference-summary-mcp",
			scenarioID:       "jira.synthetic-reference-summary-mcp-v1",
			key:              "RF-42",
			fields:           []string{"customfield_20001"},
			repetitions:      3,
			expectedGETs:     2,
			wantMode:         "key",
			wantIssueKeys:    []string{"RF-42"},
			wantComplete:     true,
			wantSelectedFrom: "field.customfield_20001",
		},
		{
			directory:      "jira-reference-summary-mcp-holdout",
			scenarioID:     "jira.synthetic-reference-summary-mcp-holdout-v1",
			jql:            "project=RF",
			limit:          2,
			repetitions:    1,
			expectedGETs:   3,
			wantMode:       "jql",
			wantIssueKeys:  []string{"RF-7", "RF-8"},
			wantTruncated:  true,
			wantSharedKind: "doc",
			// RF-7 carries an inspected comment source that produced no
			// reference, so a zero-count source must survive the projection.
			wantEmptySource: true,
		},
	}
}

func (c jiraReferenceMCPCohort) root() string {
	return filepath.Join("..", "..", "benchmarks", "agent-eval", c.directory)
}

func (c jiraReferenceMCPCohort) arguments() map[string]any {
	arguments := map[string]any{"max_bytes": jiraReferenceMCPMaxBytes}
	if c.key != "" {
		arguments["key"] = c.key
	}
	if c.jql != "" {
		arguments["jql"] = c.jql
		arguments["limit"] = c.limit
	}
	if len(c.fields) > 0 {
		arguments["fields"] = c.fields
	}
	return arguments
}

// TestJiraReferenceMCPFixturesDriveProviderOracles derives every
// committed oracle from the product's own bounded typed route: the reused
// synthetic fixtures answer through the production MCP server, and the decoded
// projection must satisfy the exact run checks both provider specs declare.
func TestJiraReferenceMCPFixturesDriveProviderOracles(t *testing.T) {
	for _, cohort := range jiraReferenceMCPCohorts() {
		t.Run(cohort.directory, func(t *testing.T) {
			root := cohort.root()
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			backend, client := startJiraSnapshotReconciliationMCPBackend(t, fixture)
			invocation := mustMCPInvocation(t, jiraReferenceMCPTool, cohort.arguments())
			called := callJiraSnapshotReconciliationMCP(t, client, invocation)
			if called.IsError {
				t.Fatalf("bounded reference read failed: %+v", called.Content)
			}

			var view app.JiraIssueRefsView
			decodeRepositoryStructuredContent(t, called.StructuredContent, &view)
			assertJiraReferenceMCPClassShape(t, cohort, &view)
			encoded, err := json.Marshal(called.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			rawRefs, narrative := jiraReferenceMCPProjectionLeaks(t, encoded)
			final := jiraReferenceMCPFinal(t, cohort, &view, rawRefs, narrative)

			methods, unexpected, duplicates := backend.Summary()
			if !equalHTTPMethods(methods, map[string]int{"GET": cohort.expectedGETs}) ||
				unexpected != 0 || duplicates != 0 {
				t.Fatalf("route traffic drifted: methods=%v unexpected=%d duplicates=%d",
					methods, unexpected, duplicates)
			}
			families := []CapabilityFamilyMetric{{
				Family: jiraReferenceMCPFamily, Invocations: 1, Successes: 1,
			}}
			sequence := []string{jiraReferenceMCPFamily}
			invocations := []MCPInvocation{invocation}

			scenario := loadRepositoryScenario(t, filepath.Join(root, "scenario.v1.json"))
			for _, provider := range []struct{ runFile, providerID, model string }{
				{runFile: "run.mcp.codex.json", providerID: "codex", model: "gpt-5.6-luna"},
				{runFile: "run.mcp.claude.json", providerID: "claude-code", model: "claude-opus-4-8"},
			} {
				specPath := filepath.Join(root, provider.runFile)
				// The runner's own dry-run load validates the spec against the
				// scenario, prompt, schema, rubric, workspace, and fixture.
				spec, loadedScenario, err := ValidateRunSpecFile(specPath)
				if err != nil {
					t.Fatalf("%s run spec is not loadable: %v", provider.runFile, err)
				}
				if loadedScenario.ID != scenario.ID || loadedScenario.ID != cohort.scenarioID {
					t.Fatalf("%s resolved scenario %q, want %q", provider.runFile, loadedScenario.ID, cohort.scenarioID)
				}
				assertJiraReferenceMCPRunContract(t, scenario, spec, cohort, provider.providerID, provider.model)
				assertJiraReferenceMCPResponseSchema(t, root, spec, final)
				if declared := repositoryExpectedMCPInvocations(t, spec); !equalMCPInvocations(declared, invocations) {
					t.Fatalf("%s exact invocation contract drifted: declared=%+v derived=%+v",
						provider.providerID, declared, invocations)
				}
				checks := evaluateJiraReferenceMCPChecks(t, spec, final, methods, families, sequence, invocations)
				for name, passed := range checks {
					if !passed {
						t.Fatalf("%s fixture-derived final failed run check %q: %s",
							provider.providerID, name, final)
					}
				}

				// The exact-argument oracle must be load-bearing: a widened
				// bound is a different route, not a formatting difference.
				widened := mustMCPInvocation(t, jiraReferenceMCPTool, jiraReferenceMCPWidened(cohort))
				drifted := evaluateJiraReferenceMCPChecks(
					t, spec, final, methods, families, sequence, []MCPInvocation{widened},
				)
				if drifted["route_arguments"] {
					t.Fatalf("%s route_arguments accepted a widened byte bound", provider.providerID)
				}
			}
		})
	}
}

// TestJiraReferenceMCPSamplingPairIdentity keeps the two cohorts a valid
// sampling pair: one execution identity per provider, distinct scenarios and
// task contracts, and the repetition split the MCP corpus inventory requires.
func TestJiraReferenceMCPSamplingPairIdentity(t *testing.T) {
	cohorts := jiraReferenceMCPCohorts()
	primary, holdout := cohorts[0], cohorts[1]
	pair := loadRepositorySamplingPairContract(t, "jira-reference-summary-mcp")
	primaryScenario, holdoutScenario := pair.Primary.Scenario, pair.Holdout.Scenario
	if primaryScenario.Budgets.MaxBackendRequests == holdoutScenario.Budgets.MaxBackendRequests {
		t.Fatalf("primary/holdout scenario identity is not distinct-compatible: primary=%+v holdout=%+v",
			primaryScenario, holdoutScenario)
	}

	for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
		primarySpec, holdoutSpec := pair.Primary.Runs[runFile], pair.Holdout.Runs[runFile]
		primaryPrompt := mustReadFile(t, filepath.Join(primary.root(), primarySpec.PromptFile))
		holdoutPrompt := mustReadFile(t, filepath.Join(holdout.root(), holdoutSpec.PromptFile))
		primaryFixture := mustReadFile(t, filepath.Join(primary.root(), primarySpec.FixtureFile))
		holdoutFixture := mustReadFile(t, filepath.Join(holdout.root(), holdoutSpec.FixtureFile))
		if bytes.Equal(primaryPrompt, holdoutPrompt) || bytes.Equal(primaryFixture, holdoutFixture) {
			t.Fatalf("%s holdout does not have a distinct task contract", runFile)
		}
	}
}

// assertJiraReferenceMCPClassShape pins what makes each cohort its own class.
// Every assertion reads the product's projection; none recomputes the
// reference arithmetic that IssueRefs owns.
func assertJiraReferenceMCPClassShape(t *testing.T, cohort jiraReferenceMCPCohort, view *app.JiraIssueRefsView) {
	t.Helper()
	if view.SchemaVersion != 1 || view.Selection.Mode != cohort.wantMode ||
		view.Complete != cohort.wantComplete || view.Truncated != cohort.wantTruncated ||
		view.Selection.Limit != cohort.limit || view.Count != len(cohort.wantIssueKeys) {
		t.Fatalf("selection shape drifted: %+v", view)
	}
	keys := make([]string, 0, len(view.Issues))
	for _, issue := range view.Issues {
		keys = append(keys, issue.Key)
		if !issue.Complete || issue.Truncated {
			t.Fatalf("emitted issue %q is not fully qualified: %+v", issue.Key, issue)
		}
	}
	if !slices.Equal(keys, cohort.wantIssueKeys) {
		t.Fatalf("emitted issue keys drifted: %v", keys)
	}
	if cohort.wantSelectedFrom != "" {
		if _, ok := view.Issues[0].Sources[cohort.wantSelectedFrom]; !ok {
			t.Fatalf("selected field source %q is missing: %+v", cohort.wantSelectedFrom, view.Issues[0].Sources)
		}
	}
	if cohort.wantSharedKind != "" {
		// A reference shared by two issues counts once per issue.
		if view.Summary.ReferenceKindCounts[cohort.wantSharedKind] != len(cohort.wantIssueKeys) {
			t.Fatalf("cross-issue %q aggregation drifted: %+v", cohort.wantSharedKind, view.Summary.ReferenceKindCounts)
		}
	}
	emptySource := false
	for _, issue := range view.Issues {
		for _, count := range issue.ReferenceSummary.SourceValueCounts {
			if count == 0 {
				emptySource = true
			}
		}
	}
	if emptySource != cohort.wantEmptySource {
		t.Fatalf("zero-count source shape drifted: %+v", view.Issues)
	}
}

// jiraReferenceMCPProjectionLeaks observes the encoded tool result rather than
// trusting the prompt: the built-in tool must never emit reference URLs or
// issue narrative, so both self-reported projection facts rest on the product.
func jiraReferenceMCPProjectionLeaks(t *testing.T, encoded []byte) (rawRefs, narrative bool) {
	t.Helper()
	var document struct {
		Refs   json.RawMessage `json:"refs"`
		Issues []struct {
			Refs    json.RawMessage `json:"refs"`
			Summary json.RawMessage `json:"summary"`
			Type    json.RawMessage `json:"type"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	rawRefs = len(document.Refs) > 0
	for _, issue := range document.Issues {
		if len(issue.Refs) > 0 {
			rawRefs = true
		}
		if len(issue.Summary) > 0 || len(issue.Type) > 0 {
			narrative = true
		}
	}
	if bytes.Contains(encoded, []byte("https://")) {
		t.Fatalf("bounded reference result carried a URL: %s", encoded)
	}
	return rawRefs, narrative
}

// jiraReferenceMCPFinal projects the typed result into the committed closed
// response schema. Every value is a direct field projection of the decoded view
// or of an argument this drive actually sent; nothing is recomputed.
func jiraReferenceMCPFinal(
	t *testing.T,
	cohort jiraReferenceMCPCohort,
	view *app.JiraIssueRefsView,
	rawRefs, narrative bool,
) []byte {
	t.Helper()
	issues := make([]map[string]any, 0, len(view.Issues))
	for _, issue := range view.Issues {
		summary := issue.ReferenceSummary
		issues = append(issues, map[string]any{
			"key":                     issue.Key,
			"complete":                issue.Complete,
			"truncated":               issue.Truncated,
			"reference_count":         summary.ReferenceCount,
			"reference_kind_counts":   jiraReferenceNamedCounts(summary.ReferenceKindCounts),
			"source_count":            summary.SourceCount,
			"source_value_counts":     jiraReferenceNamedCounts(summary.SourceValueCounts),
			"complete_source_count":   summary.CompleteSourceCount,
			"incomplete_source_count": summary.IncompleteSourceCount,
			"truncated_source_count":  summary.TruncatedSourceCount,
			"reconciliation": map[string]any{
				"reference_count_matches_kinds": summary.ReferenceCountMatchesKinds,
				"complete_matches_sources":      summary.CompleteMatchesSources,
				"truncated_matches_sources":     summary.TruncatedMatchesSources,
			},
		})
	}
	summary := view.Summary
	requestedFields := []string{}
	if len(cohort.fields) > 0 {
		requestedFields = slices.Clone(cohort.fields)
	}
	encoded, err := json.Marshal(map[string]any{
		"selection": map[string]any{
			"mode":      view.Selection.Mode,
			"count":     view.Selection.Count,
			"limit":     view.Selection.Limit,
			"complete":  view.Selection.Complete,
			"truncated": view.Selection.Truncated,
		},
		"complete":  view.Complete,
		"truncated": view.Truncated,
		"summary": map[string]any{
			"issue_count":             summary.IssueCount,
			"complete_issue_count":    summary.CompleteIssueCount,
			"incomplete_issue_count":  summary.IncompleteIssueCount,
			"reference_count":         summary.ReferenceCount,
			"reference_kind_counts":   jiraReferenceNamedCounts(summary.ReferenceKindCounts),
			"source_count":            summary.SourceCount,
			"source_value_counts":     jiraReferenceNamedCounts(summary.SourceValueCounts),
			"complete_source_count":   summary.CompleteSourceCount,
			"incomplete_source_count": summary.IncompleteSourceCount,
			"truncated_source_count":  summary.TruncatedSourceCount,
			"reconciliation": map[string]any{
				"count_matches_issues":           summary.CountMatchesIssues,
				"selection_count_matches_issues": summary.SelectionCountMatchesIssues,
				"reference_count_matches_kinds":  summary.ReferenceCountMatchesKinds,
				"issue_summaries_reconciled":     summary.IssueSummariesReconciled,
				"complete_matches_inputs":        summary.CompleteMatchesInputs,
				"truncated_matches_inputs":       summary.TruncatedMatchesInputs,
			},
		},
		"issues":           issues,
		"requested_key":    jiraReferenceMCPNullableString(cohort.key),
		"requested_jql":    jiraReferenceMCPNullableString(cohort.jql),
		"requested_fields": requestedFields,
		"requested_limit":  cohort.limit,
		// The typed tool exposes no raw-reference selector, so the route cannot
		// have asked for one; presence is observed on the encoded result.
		"raw_refs_requested":      false,
		"raw_refs_present":        rawRefs,
		"issue_narrative_present": narrative,
		"content_mutated":         false,
		"brief":                   jiraReferenceMCPBrief,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func jiraReferenceMCPNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func jiraReferenceMCPWidened(cohort jiraReferenceMCPCohort) map[string]any {
	arguments := cohort.arguments()
	arguments["max_bytes"] = jiraReferenceMCPMaxBytes * 2
	return arguments
}

func evaluateJiraReferenceMCPChecks(
	t *testing.T,
	spec RunSpec,
	final []byte,
	methods map[string]int,
	families []CapabilityFamilyMetric,
	sequence []string,
	invocations []MCPInvocation,
) map[string]bool {
	t.Helper()
	results, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", len(sequence), 0, 0, 0,
		nil, 0, 0, methods, true, nil,
		families, true, sequence, invocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func assertJiraReferenceMCPRunContract(
	t *testing.T,
	scenario Scenario,
	spec RunSpec,
	cohort jiraReferenceMCPCohort,
	provider, model string,
) {
	t.Helper()
	if spec.Provider != provider || spec.Model != model || spec.Reasoning != "high" ||
		spec.EffectiveSurface() != SurfaceATLMCP || spec.EffectiveToolTransport() != "mcp" ||
		spec.PromptFile != "prompt.mcp.v1.md" || spec.Repetitions != cohort.repetitions {
		t.Fatalf("%s run identity drifted: %+v", provider, spec)
	}
	if !slices.Equal(spec.AllowedMCPTools, []string{jiraReferenceMCPTool}) ||
		len(spec.AllowedTools) != 0 || len(spec.AllowedATLCommands) != 0 || len(spec.AllowedCLICommands) != 0 {
		t.Fatalf("%s run admits a route beyond the one typed tool: %+v", provider, spec)
	}
	if scenario.Budgets.MaxATLInvocations != 0 || scenario.Budgets.EffectiveMaxInterfaceInvocations() != 1 ||
		scenario.Budgets.MaxBackendRequests != cohort.expectedGETs ||
		scenario.Budgets.MaxDuplicateBackendRequests != 0 || scenario.Budgets.MaxRemoteWrites != 0 ||
		scenario.Budgets.MaxDelegations != 0 ||
		!slices.Equal(scenario.Budgets.AllowedHTTPMethods, []string{"GET"}) {
		t.Fatalf("%s scenario budgets drifted: %+v", provider, scenario.Budgets)
	}
	if !slices.Equal(scenario.RequiredCapabilities, []string{jiraReferenceMCPFamily}) {
		t.Fatalf("%s scenario capability contract drifted: %v", provider, scenario.RequiredCapabilities)
	}
	declared := make(map[string]struct{}, len(spec.Checks))
	for _, check := range spec.Checks {
		declared[check.Name] = struct{}{}
	}
	for _, name := range scenario.RequiredChecks {
		if _, ok := declared[name]; !ok {
			t.Fatalf("%s run spec omits required check %q", provider, name)
		}
	}
}

func assertJiraReferenceMCPResponseSchema(t *testing.T, root string, spec RunSpec, final []byte) {
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
		if err := validateJSONSchemaSubsetInstance(schema, final); err != nil {
			t.Fatalf("%s %s response schema rejected fixture-derived final: %v", spec.Provider, name, err)
		}
	}

	var schema struct {
		Type                 string                     `json:"type"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Fatal("response schema root is not a closed object")
	}
	var document map[string]any
	if err := json.Unmarshal(final, &document); err != nil {
		t.Fatal(err)
	}
	properties := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		properties = append(properties, name)
	}
	answered := make([]string, 0, len(document))
	for name := range document {
		answered = append(answered, name)
	}
	required := slices.Clone(schema.Required)
	slices.Sort(properties)
	slices.Sort(answered)
	slices.Sort(required)
	if !slices.Equal(properties, answered) || !slices.Equal(required, properties) {
		t.Fatalf("schema/final root mismatch: properties=%v required=%v final=%v", properties, required, answered)
	}
}
