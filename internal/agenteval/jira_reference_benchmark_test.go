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
)

func TestRepositoryJiraReferenceSummaryFixturesDriveProviderOracles(t *testing.T) {
	tests := []struct {
		name          string
		directory     string
		expectedGETs  int
		claudeCommand string
		codexPrompt   string
		claudePrompt  string
		commandArgs   []string
	}{
		{
			name: "complete multi-source primary", directory: "jira-reference-summary",
			expectedGETs:  2,
			claudeCommand: "atl jira issue refs RF-42 --fields customfield_20001 --",
			codexPrompt:   "atl jira issue refs RF-42 --fields customfield_20001",
			claudePrompt:  "atl jira issue refs RF-42 --fields customfield_20001 --",
			commandArgs:   []string{"jira", "issue", "refs", "RF-42", "--fields", "customfield_20001"},
		},
		{
			name: "truncated cross-issue holdout", directory: "jira-reference-summary-holdout",
			expectedGETs:  3,
			claudeCommand: "atl jira issue refs --jql project=RF --limit 2 --",
			codexPrompt:   "atl jira issue refs --jql project=RF --limit 2",
			claudePrompt:  "atl jira issue refs --jql project=RF --limit 2 --",
			commandArgs:   []string{"jira", "issue", "refs", "--jql", "project=RF", "--limit", "2"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			codexSpec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli.codex.json"))
			scratchRoot := privateSyntheticATLScratch(t)
			process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
				Binary: repositorySyntheticATLBinary(t), Fixture: fixture, ScratchRoot: scratchRoot,
				CLIPolicy: CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: codexSpec.AllowedCLICommands},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := process.Close(); err != nil {
					t.Errorf("close synthetic ATL process: %v", err)
				}
			})
			called, err := process.RunCLIJSON(context.Background(), test.commandArgs...)
			if err != nil {
				t.Fatal(err)
			}
			if called.ExitCode != 0 || len(called.JSON) == 0 || len(called.Stderr) != 0 {
				t.Fatalf("selected CLI failed: exit=%d stderr_bytes=%d json_bytes=%d", called.ExitCode, len(called.Stderr), len(called.JSON))
			}
			result, err := DecodeJiraIssueRefsResult(bytes.NewReader(called.JSON))
			if err != nil {
				t.Fatal(err)
			}
			final := jiraReferenceBenchmarkFinal(t, &result)
			summary := process.Summary()
			methods, unexpected, duplicates := summary.HTTPMethods, summary.UnexpectedRequests, summary.DuplicateRequests
			if unexpected != 0 || duplicates != 0 || len(methods) != 1 || methods["GET"] != test.expectedGETs {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
			matched, matchErr := (CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: codexSpec.AllowedCLICommands}).Match(test.commandArgs)
			if matchErr != nil {
				t.Fatal(matchErr)
			}
			if !process.RequestSequenceComplete() || len(summary.CLIInvocations) != 1 ||
				summary.CLIInvocations[matched.Name] != 1 || len(summary.MCPInvocations) != 0 {
				t.Fatalf("selected process accounting drifted: sequence_complete=%v cli=%v mcp=%v",
					process.RequestSequenceComplete(), summary.CLIInvocations, summary.MCPInvocations)
			}

			for _, provider := range []string{"codex", "claude"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli."+provider+".json"))
				promptCommand := test.codexPrompt
				if provider == "claude" {
					promptCommand = test.claudePrompt
				}
				assertJiraReferenceCommandPolicy(t, root, spec, promptCommand, test.claudeCommand, test.commandArgs)
				assertJiraReferenceSchemaMatchesFinal(t, root, spec, final)
				checks, err := evaluateRunChecks(
					spec.Checks, final, "", 2, 0, unexpected, 1,
					map[string]int{"atl:jira": 1}, 0, 0, methods, true, []int{0, 0},
				)
				if err != nil {
					t.Fatal(err)
				}
				for name, passed := range checks {
					if !passed {
						t.Fatalf("%s fixture-derived final failed run check %q", provider, name)
					}
				}
				assertJiraReferenceCheckMutationFails(t, spec, final, methods)
			}
		})
	}
}

func TestRepositoryJiraReferenceSummarySamplingPairIdentity(t *testing.T) {
	pair := loadRepositorySamplingPairContract(t, "jira-reference-summary")
	primaryRoot, holdoutRoot := pair.Primary.Root, pair.Holdout.Root

	for _, provider := range []string{"codex", "claude"} {
		runFile := "run.cli." + provider + ".json"
		primary, holdout := pair.Primary.Runs[runFile], pair.Holdout.Runs[runFile]
		primaryPrompt, err := os.ReadFile(filepath.Join(primaryRoot, primary.PromptFile))
		if err != nil {
			t.Fatal(err)
		}
		holdoutPrompt, err := os.ReadFile(filepath.Join(holdoutRoot, holdout.PromptFile))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(primaryPrompt, holdoutPrompt) {
			t.Fatalf("%s holdout does not have a distinct prompt contract", provider)
		}
	}
}

func jiraReferenceBenchmarkFinal(t *testing.T, result *JiraIssueRefsResult) []byte {
	t.Helper()
	issues := make([]map[string]any, 0, len(result.Issues))
	for _, issue := range result.Issues {
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
	summary := result.Summary
	final := map[string]any{
		"selection": map[string]any{
			"mode":      result.Selection.Mode,
			"count":     result.Selection.Count,
			"limit":     result.Selection.Limit,
			"complete":  result.Selection.Complete,
			"truncated": result.Selection.Truncated,
		},
		"complete":  result.Complete,
		"truncated": result.Truncated,
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
		"issues": issues,
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func jiraReferenceNamedCounts(counts map[string]int) []map[string]any {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{"name": name, "count": counts[name]})
	}
	return out
}

func assertJiraReferenceCommandPolicy(t *testing.T, root string, spec RunSpec, promptCommand, claudeCommand string, codexArgs []string) {
	t.Helper()
	prompt, err := os.ReadFile(filepath.Join(root, spec.PromptFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(prompt, []byte("`"+promptCommand+"`")) {
		t.Fatalf("%s prompt does not contain reviewed command %q", spec.Provider, promptCommand)
	}
	switch spec.Provider {
	case "codex":
		policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands}
		if _, err := policy.Match(codexArgs); err != nil {
			t.Fatalf("reference command rejected by Codex policy: %v", err)
		}
		if _, err := policy.Match(append(slices.Clone(codexArgs), "--limit", "100")); err == nil {
			t.Fatal("Codex policy accepted appended command arguments")
		}
	case "claude-code":
		if !slices.Contains(spec.AllowedATLCommands, claudeCommand) {
			t.Fatalf("Claude policy does not contain exact terminated command %q: %v", claudeCommand, spec.AllowedATLCommands)
		}
		if !strings.HasSuffix(claudeCommand, " --") {
			t.Fatalf("Claude reference prefix lacks an option terminator: %q", claudeCommand)
		}
	default:
		t.Fatalf("unexpected provider %q", spec.Provider)
	}
}

func assertJiraReferenceSchemaMatchesFinal(t *testing.T, root string, spec RunSpec, final []byte) {
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

	var mutated map[string]any
	if err := decodeJSONDocument(schemaBytes, &mutated); err != nil {
		t.Fatal(err)
	}
	properties := mutated["properties"].(map[string]any)
	issues := properties["issues"].(map[string]any)
	item := issues["items"].(map[string]any)
	referenceCount := item["properties"].(map[string]any)["reference_count"].(map[string]any)
	referenceCount["type"] = "string"
	mutatedBytes, err := json.Marshal(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateJSONSchemaSubsetInstance(mutatedBytes, final); err == nil {
		t.Fatal("fixture-derived final passed a schema with incompatible nested reference_count type")
	}
}

func assertJiraReferenceCheckMutationFails(t *testing.T, spec RunSpec, final []byte, methods map[string]int) {
	t.Helper()
	checks := slices.Clone(spec.Checks)
	for index := range checks {
		if checks[index].Name != "summary_correct" {
			continue
		}
		var expected map[string]any
		if err := json.Unmarshal(checks[index].Expected, &expected); err != nil {
			t.Fatal(err)
		}
		expected["reference_count"] = 999
		mutated, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		checks[index].Expected = mutated
		results, err := evaluateRunChecks(
			checks, final, "", 2, 0, 0, 1,
			map[string]int{"atl:jira": 1}, 0, 0, methods, true, []int{0, 0},
		)
		if err != nil {
			t.Fatal(err)
		}
		if results["summary_correct"] {
			t.Fatal("mutated nested reference count passed summary_correct")
		}
		return
	}
	t.Fatal("summary_correct check not found")
}
