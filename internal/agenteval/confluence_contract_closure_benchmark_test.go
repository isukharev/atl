package agenteval

import (
	"bytes"
	"path/filepath"
	"slices"
	"testing"
)

type confluenceContractClosureCohort struct {
	directory   string
	taskClass   string
	capability  string
	repetitions int
}

func confluenceContractClosureCohorts() []confluenceContractClosureCohort {
	return []confluenceContractClosureCohort{
		{"confluence-attachment-discovery-mcp", "confluence/attachment-discovery", "confluence.attachment.search", 3},
		{"confluence-attachment-discovery-mcp-holdout", "confluence/attachment-discovery", "confluence.attachment.search", 1},
		{"confluence-space-hierarchy", "confluence/space-hierarchy", "confluence.space.tree", 3},
		{"confluence-space-hierarchy-holdout", "confluence/space-hierarchy", "confluence.space.tree", 1},
	}
}

func TestRepositoryConfluenceContractClosureCoversBothModelsAndHoldouts(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for _, cohort := range confluenceContractClosureCohorts() {
		t.Run(cohort.directory, func(t *testing.T) {
			directory := filepath.Join(root, cohort.directory)
			scenario := loadRepositoryScenario(t, filepath.Join(directory, "scenario.v1.json"))
			if scenario.TaskClass != cohort.taskClass || !slices.Equal(scenario.RequiredCapabilities, []string{cohort.capability}) ||
				scenario.Budgets.MaxRemoteWrites != 0 || !slices.Equal(scenario.Budgets.AllowedHTTPMethods, []string{"GET"}) {
				t.Fatalf("scenario escaped its read-only singleton contract: %+v", scenario)
			}

			prefix := "run.mcp."
			if cohort.taskClass == "confluence/space-hierarchy" {
				prefix = "run.cli."
			}
			codex := loadRepositoryRunSpec(t, filepath.Join(directory, prefix+"codex.json"))
			claude := loadRepositoryRunSpec(t, filepath.Join(directory, prefix+"claude.json"))
			for provider, spec := range map[string]RunSpec{"codex": codex, "claude": claude} {
				if err := spec.ValidateAgainstScenario(scenario); err != nil {
					t.Fatalf("%s run is not scenario-compatible: %v", provider, err)
				}
				if spec.Repetitions != cohort.repetitions || spec.Reasoning != "high" || spec.AllowSyntheticWrites || spec.AllowLiveWrites {
					t.Fatalf("%s sampling contract drifted: %+v", provider, spec)
				}
			}
			if codex.Provider != "codex" || codex.Model != "gpt-5.6-luna" ||
				claude.Provider != "claude-code" || claude.Model != "claude-opus-4-8" {
				t.Fatalf("provider/model coverage drifted: codex=%s/%s claude=%s/%s", codex.Provider, codex.Model, claude.Provider, claude.Model)
			}
			if codex.PromptFile != claude.PromptFile || codex.ResponseSchemaFile != claude.ResponseSchemaFile ||
				codex.QualitativeRubricFile != claude.QualitativeRubricFile || codex.FixtureFile != claude.FixtureFile ||
				codex.WorkspaceTemplate != claude.WorkspaceTemplate || codex.Variant != claude.Variant ||
				codex.Repetitions != claude.Repetitions {
				t.Fatalf("provider comparison fields diverged: codex=%+v claude=%+v", codex, claude)
			}
			if cohort.taskClass == "confluence/attachment-discovery" {
				if !slices.Equal(codex.AllowedMCPTools, []string{"confluence_attachment_search"}) || len(codex.AllowedCLICommands) != 0 {
					t.Fatalf("attachment discovery route widened: %+v", codex)
				}
			} else if len(codex.AllowedMCPTools) != 0 || len(codex.AllowedCLICommands) != 1 ||
				!slices.Equal(codex.AllowedCLICommands[0].Command, []string{"conf", "space", "tree"}) {
				t.Fatalf("space hierarchy route widened: %+v", codex)
			}
		})
	}
}

func TestRepositoryConfluenceAttachmentDiscoverySelectedProcess(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	for _, directory := range []string{"confluence-attachment-discovery-mcp", "confluence-attachment-discovery-mcp-holdout"} {
		t.Run(directory, func(t *testing.T) {
			path := filepath.Join(root, directory)
			spec := loadRepositoryRunSpec(t, filepath.Join(path, "run.mcp.codex.json"))
			invocations := repositoryExpectedMCPInvocations(t, spec)
			if len(invocations) != 1 || invocations[0].Tool != "confluence_attachment_search" {
				t.Fatalf("exact attachment discovery admission drifted: %+v", invocations)
			}
			process := startRepositoryConfluenceEvidenceProcess(t, loadRepositoryMockFixture(t, filepath.Join(path, "fixture.json")), invocations)
			result, message, ok := callRepositoryConfluenceEvidence(t, process, invocations[0])
			if !ok {
				t.Fatalf("selected attachment discovery failed: %s", message)
			}
			view, err := DecodeConfluenceAttachmentDiscoveryView(bytes.NewReader(result.StructuredContent))
			if err != nil {
				t.Fatal(err)
			}
			wantComplete := directory == "confluence-attachment-discovery-mcp"
			if view.Complete != wantComplete || view.Count == 0 {
				t.Fatalf("selected result=%+v want_complete=%t", view, wantComplete)
			}
			summary := process.Summary()
			if !process.RequestSequenceComplete() || summary.HTTPMethods["GET"] != 1 || summary.UnexpectedRequests != 0 ||
				summary.DuplicateRequests != 0 || summary.MCPInvocations["confluence_attachment_search"] != 1 {
				t.Fatalf("selected process accounting drifted: %+v", summary)
			}
		})
	}
}

func TestRepositoryConfluenceSpaceHierarchySelectedProcess(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval")
	tests := []struct {
		directory    string
		args         []string
		wantComplete bool
	}{
		{"confluence-space-hierarchy", []string{"conf", "space", "tree", "--space", "DOC", "--depth", "0", "--max-items", "10", "--max-scanned-items", "20", "--max-requests", "2", "--max-response-bytes", "65536", "--deadline", "5s"}, true},
		{"confluence-space-hierarchy-holdout", []string{"conf", "space", "tree", "--space", "OPS", "--depth", "0", "--max-items", "2", "--max-scanned-items", "20", "--max-requests", "2", "--max-response-bytes", "65536", "--deadline", "5s"}, false},
	}
	for _, test := range tests {
		t.Run(test.directory, func(t *testing.T) {
			path := filepath.Join(root, test.directory)
			spec := loadRepositoryRunSpec(t, filepath.Join(path, "run.cli.codex.json"))
			process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
				Binary: repositorySyntheticATLBinary(t), Fixture: loadRepositoryMockFixture(t, filepath.Join(path, "fixture.json")),
				ScratchRoot: privateSyntheticATLScratch(t),
				CLIPolicy:   CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: slices.Clone(spec.AllowedCLICommands)},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := process.Close(); err != nil {
					t.Errorf("close synthetic space tree process: %v", err)
				}
			})
			result, err := process.RunCLIJSON(t.Context(), test.args...)
			wantStderr := []byte(nil)
			if !test.wantComplete {
				wantStderr = []byte("warning: space listing is partial after 2 pages (item_limit) — omitted pages are NOT proven absent\n")
			}
			if err != nil || result.ExitCode != 0 || !bytes.Equal(result.Stderr, wantStderr) {
				t.Fatalf("selected tree failed: result=%+v err=%v", result, err)
			}
			view, err := DecodeConfluenceSpaceTreeView(bytes.NewReader(result.JSON))
			if err != nil {
				t.Fatal(err)
			}
			if view.Complete != test.wantComplete || view.Count == 0 {
				t.Fatalf("selected result=%+v want_complete=%t", view, test.wantComplete)
			}
			summary := process.Summary()
			if !process.RequestSequenceComplete() || summary.HTTPMethods["GET"] != 1 || summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 {
				t.Fatalf("selected process accounting drifted: %+v", summary)
			}
		})
	}
}
