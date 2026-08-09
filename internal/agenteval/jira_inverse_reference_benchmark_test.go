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

func TestRepositoryJiraInverseReferenceFixturesDriveSelectedATLBinary(t *testing.T) {
	for _, cohort := range jiraInverseReferenceCohorts() {
		t.Run(cohort.directory, func(t *testing.T) {
			root := cohort.root()
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			codex := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli.codex.json"))
			policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: slices.Clone(codex.AllowedCLICommands)}
			process := startJiraInverseReferenceProcess(t, cohort, fixture, policy)
			view, called := runJiraInverseReferenceCLI(t, process, cohort)
			assertJiraInverseReferenceProcessAccounting(t, process, cohort)

			canaries := []string{cohort.target, cohort.scopeJQL, "PRIVACY_CANARY_FRAGMENT", "PRIVACY_CANARY_SOURCE_TEXT", "PRIVACY_CANARY_DEVELOPMENT_TITLE"}
			final := inverseReferenceFinalJSON(t, view, called.Stdout, canaries)
			if bytes.Contains(called.Stdout, []byte("code-inverse.example.test")) ||
				bytes.Contains(called.Stdout, []byte("platform/widget")) {
				t.Fatalf("inverse-reference result exposed GitLab target coordinates: %s", called.Stdout)
			}
			if cohort.strict {
				contract, ok := ParseCLIErrorContract(called.ExitCode, called.Stderr)
				if !ok || contract != (CLIErrorContract{ExitCode: 8, Kind: "check_failed", Remediation: "review_failed_check"}) {
					t.Fatalf("strict inverse-reference exit contract drifted: ok=%t contract=%+v stderr_bytes=%d",
						ok, contract, len(called.Stderr))
				}
				if view.Complete || view.AbsenceProven || view.Selection.Reason != "mode_fast" || len(view.Matches) != 0 {
					t.Fatalf("fast strict result made a false absence/completeness claim: %+v", view)
				}
			} else if len(called.Stderr) != 0 || !view.Complete || view.AbsenceProven || len(view.Matches) != 1 {
				t.Fatalf("complete exhaustive match drifted: view=%+v stderr_bytes=%d", view, len(called.Stderr))
			}

			for _, provider := range []string{"codex", "claude"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli."+provider+".json"))
				assertJiraInverseReferenceRunContract(t, root, spec, cohort)
				assertJiraInverseReferenceResponseSchema(t, root, spec, final)
				failed := 0
				exits := []int{0, 0}
				if cohort.strict {
					failed, exits[1] = 1, 8
				}
				checks, err := evaluateRunChecks(
					spec.Checks, final, "", 2, failed, 0, 1,
					map[string]int{"atl:jira": 1}, 0, 0, map[string]int{"GET": cohort.expectedGETs}, true, exits,
				)
				if err != nil {
					t.Fatal(err)
				}
				for name, passed := range checks {
					if !passed {
						t.Fatalf("%s fixture-derived final failed run check %q: %s", provider, name, final)
					}
				}
			}
			assertJiraInverseReferenceAdmissionRefusals(t, cohort, fixture, policy)
		})
	}
}

func TestRepositoryJiraInverseReferenceSamplingPairIdentity(t *testing.T) {
	pair := loadRepositorySamplingPairContract(t, "jira-inverse-reference-search")
	for _, provider := range []string{"codex", "claude"} {
		runFile := "run.cli." + provider + ".json"
		primary, holdout := pair.Primary.Runs[runFile], pair.Holdout.Runs[runFile]
		primaryPrompt, err := os.ReadFile(filepath.Join(pair.Primary.Root, primary.PromptFile))
		if err != nil {
			t.Fatal(err)
		}
		holdoutPrompt, err := os.ReadFile(filepath.Join(pair.Holdout.Root, holdout.PromptFile))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(primaryPrompt, holdoutPrompt) {
			t.Fatalf("%s inverse-reference holdout prompt is not distinct", provider)
		}
	}
	primarySchema, err := os.ReadFile(filepath.Join(pair.Primary.Root, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	holdoutSchema, err := os.ReadFile(filepath.Join(pair.Holdout.Root, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(primarySchema, holdoutSchema) {
		t.Fatal("inverse-reference primary and holdout response schemas are not byte-identical")
	}
}

func assertJiraInverseReferenceRunContract(t *testing.T, root string, spec RunSpec, cohort jiraInverseReferenceCohort) {
	t.Helper()
	prompt, err := os.ReadFile(filepath.Join(root, spec.PromptFile))
	if err != nil {
		t.Fatal(err)
	}
	command := jiraInverseReferenceShellCommand(cohort)
	if spec.Provider == "claude-code" {
		command += " --"
	}
	if !bytes.Contains(prompt, []byte("`"+command+"`")) {
		t.Fatalf("%s prompt does not bind exact inverse-reference command %q", spec.Provider, command)
	}
	lower := strings.ToLower(string(prompt))
	for _, required := range []string{"exact advertised skill file", "routed reference named by", "do not search for skills", "do not delegate"} {
		if !strings.Contains(lower, required) {
			t.Fatalf("%s prompt omits bounded activation/safety guidance %q", spec.Provider, required)
		}
	}
	switch spec.Provider {
	case "codex":
		policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands}
		if match, err := policy.Match(cohort.args()); err != nil || match.Name != "jira_inverse_reference" {
			t.Fatalf("Codex inverse-reference command policy mismatch: match=%+v err=%v", match, err)
		}
		if _, err := policy.Match(append(slices.Clone(cohort.args()), "--max-requests", "25000")); err == nil {
			t.Fatal("Codex policy admitted a widened inverse-reference bound")
		}
	case "claude-code":
		if !slices.Contains(spec.AllowedATLCommands, command) || !strings.HasSuffix(command, " --") {
			t.Fatalf("Claude policy omits exact terminated inverse-reference command %q", command)
		}
	default:
		t.Fatalf("unexpected provider %q", spec.Provider)
	}
}

func assertJiraInverseReferenceResponseSchema(t *testing.T, root string, spec RunSpec, final []byte) {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join(root, spec.ResponseSchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	providerSchema, err := providerResponseSchema(spec, schema)
	if err != nil {
		t.Fatalf("%s response schema is not provider-compatible: %v", spec.Provider, err)
	}
	for name, candidate := range map[string][]byte{"retained": schema, "provider": providerSchema} {
		if err := validateJSONSchemaSubsetInstance(candidate, final); err != nil {
			t.Fatalf("%s %s response schema rejected fixture-derived final: %v", spec.Provider, name, err)
		}
	}
	var shape struct {
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
	}
	if err := json.Unmarshal(schema, &shape); err != nil {
		t.Fatal(err)
	}
	properties := make([]string, 0, len(shape.Properties))
	for name := range shape.Properties {
		properties = append(properties, name)
	}
	slices.Sort(properties)
	required := slices.Clone(shape.Required)
	slices.Sort(required)
	if shape.AdditionalProperties == nil || *shape.AdditionalProperties || !slices.Equal(properties, required) {
		t.Fatalf("inverse-reference response schema root is not exact: properties=%v required=%v", properties, required)
	}
}
