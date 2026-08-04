package agenteval

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type benchmarkWorkspaceRelationship uint8

const (
	benchmarkWorkspaceSameNeutralTree benchmarkWorkspaceRelationship = iota + 1
	benchmarkWorkspaceDistinctTrees
)

type benchmarkPairDescriptor struct {
	primaryName           string
	responseSchema        string
	distinctArtifacts     []string
	workspaceRelationship benchmarkWorkspaceRelationship
}

type benchmarkPairProvider struct {
	runFile  string
	provider string
	model    string
}

var benchmarkPairProviders = [...]benchmarkPairProvider{
	{runFile: "run.mcp.codex.json", provider: "codex", model: "gpt-5.6-luna"},
	{runFile: "run.mcp.claude.json", provider: "claude-code", model: "claude-opus-4-8"},
}

type benchmarkPairDescriptorFactory func() benchmarkPairDescriptor

func migratedBenchmarkPairDescriptorFactories() []benchmarkPairDescriptorFactory {
	return []benchmarkPairDescriptorFactory{
		confluencePageMetadataPairDescriptor,
		jiraHistorySummaryMCPPairDescriptor,
		confluenceAttachmentEvidencePairDescriptor,
		confluenceSectionVersionBoundPairDescriptor,
		confluenceSectionBoundRecoveryPairDescriptor,
		structureFolderRecoveryPairDescriptor,
		jiraSnapshotReconciliationPairDescriptor,
		jiraZeroProgressPairDescriptor,
	}
}

func migratedBenchmarkPairDescriptors() []benchmarkPairDescriptor {
	factories := migratedBenchmarkPairDescriptorFactories()
	descriptors := make([]benchmarkPairDescriptor, 0, len(factories))
	for _, factory := range factories {
		descriptors = append(descriptors, factory())
	}
	return descriptors
}

// validateBenchmarkPair owns only repository-wide pair artifact and provider
// invariants. Cell semantics, evidence construction, route checks, budgets,
// failure policy, and mutation oracles remain in the benchmark cell.
func validateBenchmarkPair(descriptor benchmarkPairDescriptor, pair repositorySamplingPair) error {
	if err := validateBenchmarkPairDescriptor(descriptor); err != nil {
		return err
	}
	if filepath.Base(pair.Primary.Root) != descriptor.primaryName ||
		filepath.Base(pair.Holdout.Root) != descriptor.primaryName+"-holdout" {
		return fmt.Errorf("pair roots do not match primary %q: primary=%q holdout=%q",
			descriptor.primaryName, pair.Primary.Root, pair.Holdout.Root)
	}

	primarySchema, err := os.ReadFile(filepath.Join(pair.Primary.Root, descriptor.responseSchema))
	if err != nil {
		return fmt.Errorf("read primary response schema: %w", err)
	}
	holdoutSchema, err := os.ReadFile(filepath.Join(pair.Holdout.Root, descriptor.responseSchema))
	if err != nil {
		return fmt.Errorf("read holdout response schema: %w", err)
	}
	if !bytes.Equal(primarySchema, holdoutSchema) {
		return fmt.Errorf("response schema %q is not byte-identical across the pair", descriptor.responseSchema)
	}
	for _, name := range descriptor.distinctArtifacts {
		primary, readErr := os.ReadFile(filepath.Join(pair.Primary.Root, name))
		if readErr != nil {
			return fmt.Errorf("read primary distinct artifact %q: %w", name, readErr)
		}
		holdout, readErr := os.ReadFile(filepath.Join(pair.Holdout.Root, name))
		if readErr != nil {
			return fmt.Errorf("read holdout distinct artifact %q: %w", name, readErr)
		}
		if bytes.Equal(primary, holdout) {
			return fmt.Errorf("holdout reused primary artifact %q", name)
		}
	}

	primaryWorkspace, err := benchmarkPairTreeDigest(filepath.Join(pair.Primary.Root, "workspace"))
	if err != nil {
		return fmt.Errorf("digest primary workspace: %w", err)
	}
	holdoutWorkspace, err := benchmarkPairTreeDigest(filepath.Join(pair.Holdout.Root, "workspace"))
	if err != nil {
		return fmt.Errorf("digest holdout workspace: %w", err)
	}
	workspaceEqual := primaryWorkspace == holdoutWorkspace
	switch descriptor.workspaceRelationship {
	case benchmarkWorkspaceSameNeutralTree:
		if !workspaceEqual {
			return fmt.Errorf("neutral workspace trees are not byte-identical")
		}
	case benchmarkWorkspaceDistinctTrees:
		if workspaceEqual {
			return fmt.Errorf("holdout reused the primary workspace tree")
		}
	}

	wantRunFiles := []string{benchmarkPairProviders[0].runFile, benchmarkPairProviders[1].runFile}
	slices.Sort(wantRunFiles)
	for name, cohort := range map[string]repositorySamplingCohort{
		"primary": pair.Primary,
		"holdout": pair.Holdout,
	} {
		gotRunFiles := sortedMapKeys(cohort.Runs)
		if !slices.Equal(gotRunFiles, wantRunFiles) {
			return fmt.Errorf("%s provider rows=%v want=%v", name, gotRunFiles, wantRunFiles)
		}
	}

	for _, provider := range benchmarkPairProviders {
		primary := pair.Primary.Runs[provider.runFile]
		holdout := pair.Holdout.Runs[provider.runFile]
		if err := validateBenchmarkProviderRow("primary", provider, primary, 3, descriptor.responseSchema); err != nil {
			return err
		}
		if err := validateBenchmarkProviderRow("holdout", provider, holdout, 1, descriptor.responseSchema); err != nil {
			return err
		}
		if !equalPrivateComparisonJSON(
			benchmarkPairExecutionIdentityOf(primary),
			benchmarkPairExecutionIdentityOf(holdout),
		) {
			return fmt.Errorf("%s primary/holdout execution identity drifted", provider.provider)
		}
	}

	for name, cohort := range map[string]repositorySamplingCohort{
		"primary": pair.Primary,
		"holdout": pair.Holdout,
	} {
		codex := cohort.Runs[benchmarkPairProviders[0].runFile]
		claude := cohort.Runs[benchmarkPairProviders[1].runFile]
		if equalPrivateComparisonJSON(codex.Pricing, claude.Pricing) {
			return fmt.Errorf("%s provider pricing rows are not distinct", name)
		}
		if !equalPrivateComparisonJSON(neutralBenchmarkProvider(codex), neutralBenchmarkProvider(claude)) {
			return fmt.Errorf("%s provider rows differ beyond provider, model, and pricing", name)
		}
	}
	return nil
}

func validateBenchmarkPairDescriptor(descriptor benchmarkPairDescriptor) error {
	if !safeBenchmarkPairName(descriptor.primaryName) {
		return fmt.Errorf("benchmark pair primary name is missing or unsafe: %q", descriptor.primaryName)
	}
	if !safeBenchmarkPairArtifact(descriptor.responseSchema) {
		return fmt.Errorf("response schema path is missing or unsafe: %q", descriptor.responseSchema)
	}
	if len(descriptor.distinctArtifacts) == 0 {
		return fmt.Errorf("distinct artifact inventory is empty")
	}
	seen := map[string]bool{descriptor.responseSchema: true}
	for _, name := range descriptor.distinctArtifacts {
		if !safeBenchmarkPairArtifact(name) {
			return fmt.Errorf("distinct artifact path is missing or unsafe: %q", name)
		}
		if name == "workspace" || seen[name] {
			return fmt.Errorf("distinct artifact inventory repeats reserved artifact %q", name)
		}
		seen[name] = true
	}
	if descriptor.workspaceRelationship != benchmarkWorkspaceSameNeutralTree &&
		descriptor.workspaceRelationship != benchmarkWorkspaceDistinctTrees {
		return fmt.Errorf("workspace relationship is not declared")
	}
	return validateMigratedBenchmarkPairDescriptor(descriptor)
}

// validateMigratedBenchmarkPairDescriptor is an independent closed oracle for
// the representative migrations. Keeping these literals separate from the
// cell-owned descriptors makes removing a required artifact or weakening the
// workspace relationship observable.
func validateMigratedBenchmarkPairDescriptor(descriptor benchmarkPairDescriptor) error {
	var want benchmarkPairDescriptor
	switch descriptor.primaryName {
	case "confluence-page-metadata-mcp":
		want = benchmarkPairDescriptor{
			primaryName:           "confluence-page-metadata-mcp",
			responseSchema:        "response-schema.v1.json",
			distinctArtifacts:     []string{"fixture.json", "prompt.mcp.v1.md", "rubric.v1.json", "scenario.v1.json"},
			workspaceRelationship: benchmarkWorkspaceSameNeutralTree,
		}
	case "jira-history-summary-mcp":
		want = benchmarkPairDescriptor{
			primaryName:           "jira-history-summary-mcp",
			responseSchema:        "response-schema.v1.json",
			distinctArtifacts:     []string{"fixture.json", "prompt.mcp.v1.md", "rubric.v1.json"},
			workspaceRelationship: benchmarkWorkspaceDistinctTrees,
		}
	case "confluence-attachment-evidence-mcp":
		want = benchmarkPairDescriptor{
			primaryName:           "confluence-attachment-evidence-mcp",
			responseSchema:        "response-schema.v1.json",
			distinctArtifacts:     []string{"fixture.json", "prompt.mcp.v1.md", "rubric.v1.json", "scenario.v1.json"},
			workspaceRelationship: benchmarkWorkspaceSameNeutralTree,
		}
	case "confluence-section-version-bound-mcp":
		want = benchmarkPairDescriptor{
			primaryName:           "confluence-section-version-bound-mcp",
			responseSchema:        "response-schema.v1.json",
			distinctArtifacts:     []string{"fixture.json", "prompt.mcp.v1.md", "rubric.v1.json", "scenario.v1.json"},
			workspaceRelationship: benchmarkWorkspaceSameNeutralTree,
		}
	case "confluence-section-bound-recovery-mcp":
		want = benchmarkPairDescriptor{
			primaryName:           "confluence-section-bound-recovery-mcp",
			responseSchema:        "response-schema.v1.json",
			distinctArtifacts:     []string{"fixture.json", "prompt.mcp.v1.md", "rubric.v1.json", "scenario.v1.json"},
			workspaceRelationship: benchmarkWorkspaceSameNeutralTree,
		}
	case "jira-structure-folder-selection-recovery-mcp":
		want = benchmarkPairDescriptor{
			primaryName:           "jira-structure-folder-selection-recovery-mcp",
			responseSchema:        "response-schema.v1.json",
			distinctArtifacts:     []string{"fixture.json", "prompt.mcp.v1.md", "rubric.v1.json"},
			workspaceRelationship: benchmarkWorkspaceDistinctTrees,
		}
	case "jira-snapshot-reconciliation-mcp":
		want = benchmarkPairDescriptor{
			primaryName:           "jira-snapshot-reconciliation-mcp",
			responseSchema:        "response-schema.v1.json",
			distinctArtifacts:     []string{"fixture.json", "prompt.mcp.v1.md", "rubric.v1.json"},
			workspaceRelationship: benchmarkWorkspaceDistinctTrees,
		}
	case "jira-search-zero-progress-mcp":
		want = benchmarkPairDescriptor{
			primaryName:           "jira-search-zero-progress-mcp",
			responseSchema:        "response-schema.v1.json",
			distinctArtifacts:     []string{"fixture.json", "prompt.mcp.v1.md", "rubric.v1.json"},
			workspaceRelationship: benchmarkWorkspaceDistinctTrees,
		}
	default:
		return fmt.Errorf("benchmark pair %q descriptor is not registered", descriptor.primaryName)
	}
	if descriptor.responseSchema != want.responseSchema ||
		!slices.Equal(descriptor.distinctArtifacts, want.distinctArtifacts) ||
		descriptor.workspaceRelationship != want.workspaceRelationship {
		return fmt.Errorf("migrated pair %q descriptor drifted", descriptor.primaryName)
	}
	return nil
}

func safeBenchmarkPairName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}

func safeBenchmarkPairArtifact(name string) bool {
	return safeBenchmarkPairName(name) && name != "workspace"
}

func validateBenchmarkProviderRow(
	cohort string,
	want benchmarkPairProvider,
	spec RunSpec,
	repetitions int,
	responseSchema string,
) error {
	if spec.Provider != want.provider || spec.Model != want.model || spec.Reasoning != "high" ||
		spec.Repetitions != repetitions {
		return fmt.Errorf("%s %s provider row drifted: provider=%q model=%q reasoning=%q repetitions=%d",
			cohort, want.runFile, spec.Provider, spec.Model, spec.Reasoning, spec.Repetitions)
	}
	if spec.ScenarioFile != "scenario.v1.json" || spec.PromptFile != "prompt.mcp.v1.md" ||
		spec.ResponseSchemaFile != responseSchema || spec.QualitativeRubricFile != "rubric.v1.json" ||
		spec.FixtureFile != "fixture.json" || spec.WorkspaceTemplate != "workspace" {
		return fmt.Errorf("%s %s artifact bindings drifted", cohort, want.runFile)
	}
	return nil
}

func benchmarkPairExecutionIdentityOf(spec RunSpec) RunSpec {
	// Primary and holdout intentionally differ only in sampling cardinality and
	// answer-bound checks. Returning the complete remaining RunSpec keeps this
	// oracle closed when the durable run schema gains another execution field.
	spec.Repetitions = 0
	spec.Checks = nil
	return spec
}

func neutralBenchmarkProvider(spec RunSpec) RunSpec {
	spec.Provider = ""
	spec.Model = ""
	spec.Pricing = Pricing{}
	return spec
}

func benchmarkPairTreeDigest(root string) ([sha256.Size]byte, error) {
	hasher := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = hasher.Write([]byte(filepath.ToSlash(relative)))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write(data)
		_, _ = hasher.Write([]byte{0})
		return nil
	})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func TestBenchmarkPairHarnessAcceptsExplicitWorkspaceRelationships(t *testing.T) {
	for _, descriptor := range migratedBenchmarkPairDescriptors() {
		t.Run(descriptor.primaryName, func(t *testing.T) {
			pair := syntheticBenchmarkPair(t, descriptor)
			if err := validateBenchmarkPair(descriptor, pair); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBenchmarkPairHarnessRejectsMutations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(t *testing.T, descriptor *benchmarkPairDescriptor, pair *repositorySamplingPair)
		wantErr string
	}{
		{
			name: "missing primary name",
			mutate: func(_ *testing.T, descriptor *benchmarkPairDescriptor, _ *repositorySamplingPair) {
				descriptor.primaryName = ""
			},
			wantErr: "primary name is missing or unsafe",
		},
		{
			name: "missing response schema",
			mutate: func(_ *testing.T, descriptor *benchmarkPairDescriptor, _ *repositorySamplingPair) {
				descriptor.responseSchema = ""
			},
			wantErr: "response schema path is missing or unsafe",
		},
		{
			name: "missing distinct artifact inventory",
			mutate: func(_ *testing.T, descriptor *benchmarkPairDescriptor, _ *repositorySamplingPair) {
				descriptor.distinctArtifacts = nil
			},
			wantErr: "distinct artifact inventory is empty",
		},
		{
			name: "unsafe artifact path",
			mutate: func(_ *testing.T, descriptor *benchmarkPairDescriptor, _ *repositorySamplingPair) {
				descriptor.distinctArtifacts[0] = "../fixture.json"
			},
			wantErr: "distinct artifact path is missing or unsafe",
		},
		{
			name: "undeclared workspace relationship",
			mutate: func(_ *testing.T, descriptor *benchmarkPairDescriptor, _ *repositorySamplingPair) {
				descriptor.workspaceRelationship = 0
			},
			wantErr: "workspace relationship is not declared",
		},
		{
			name: "wrong pair root",
			mutate: func(t *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				pair.Holdout.Root = filepath.Join(t.TempDir(), "wrong-holdout")
			},
			wantErr: "pair roots do not match primary",
		},
		{
			name: "schema drift",
			mutate: func(t *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				writeBenchmarkPairFile(t, filepath.Join(pair.Holdout.Root, "response-schema.v1.json"), "schema-holdout")
			},
			wantErr: "response schema",
		},
		{
			name: "artifact reuse",
			mutate: func(t *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				primary, err := os.ReadFile(filepath.Join(pair.Primary.Root, "fixture.json"))
				if err != nil {
					t.Fatal(err)
				}
				writeBenchmarkPairFile(t, filepath.Join(pair.Holdout.Root, "fixture.json"), string(primary))
			},
			wantErr: `holdout reused primary artifact "fixture.json"`,
		},
		{
			name: "wrong workspace relationship",
			mutate: func(t *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				writeBenchmarkPairFile(t, filepath.Join(pair.Holdout.Root, "workspace", "README.md"), "distinct")
			},
			wantErr: "neutral workspace trees are not byte-identical",
		},
		{
			name: "distinct workspace cloned from primary",
			mutate: func(t *testing.T, descriptor *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				*descriptor = jiraHistorySummaryMCPPairDescriptor()
				*pair = syntheticBenchmarkPair(t, *descriptor)
				writeBenchmarkPairFile(t, filepath.Join(pair.Holdout.Root, "workspace", "README.md"), "neutral")
			},
			wantErr: "holdout reused the primary workspace tree",
		},
		{
			name: "extra provider row",
			mutate: func(_ *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				pair.Primary.Runs["run.mcp.extra.json"] = pair.Primary.Runs["run.mcp.codex.json"]
			},
			wantErr: "primary provider rows=",
		},
		{
			name: "wrong provider",
			mutate: func(_ *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				spec := pair.Primary.Runs["run.mcp.codex.json"]
				spec.Provider = "other"
				pair.Primary.Runs["run.mcp.codex.json"] = spec
			},
			wantErr: "primary run.mcp.codex.json provider row drifted",
		},
		{
			name: "wrong model",
			mutate: func(_ *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				spec := pair.Holdout.Runs["run.mcp.claude.json"]
				spec.Model = "other"
				pair.Holdout.Runs["run.mcp.claude.json"] = spec
			},
			wantErr: "holdout run.mcp.claude.json provider row drifted",
		},
		{
			name: "reasoning drift",
			mutate: func(_ *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				spec := pair.Primary.Runs["run.mcp.claude.json"]
				spec.Reasoning = "medium"
				pair.Primary.Runs["run.mcp.claude.json"] = spec
			},
			wantErr: "primary run.mcp.claude.json provider row drifted",
		},
		{
			name: "repetition drift",
			mutate: func(_ *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				spec := pair.Holdout.Runs["run.mcp.codex.json"]
				spec.Repetitions = 2
				pair.Holdout.Runs["run.mcp.codex.json"] = spec
			},
			wantErr: "holdout run.mcp.codex.json provider row drifted",
		},
		{
			name: "execution identity drift",
			mutate: func(_ *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
					spec := pair.Holdout.Runs[runFile]
					spec.AllowedMCPTools = []string{"other_tool"}
					pair.Holdout.Runs[runFile] = spec
				}
			},
			wantErr: "primary/holdout execution identity drifted",
		},
		{
			name: "execution timeout drift across holdout providers",
			mutate: func(_ *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
					spec := pair.Holdout.Runs[runFile]
					spec.TimeoutSeconds++
					pair.Holdout.Runs[runFile] = spec
				}
			},
			wantErr: "primary/holdout execution identity drifted",
		},
		{
			name: "gateway bound drift across holdout providers",
			mutate: func(_ *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
					spec := pair.Holdout.Runs[runFile]
					spec.GatewayMaxResponseBytes = 1
					pair.Holdout.Runs[runFile] = spec
				}
			},
			wantErr: "primary/holdout execution identity drifted",
		},
		{
			name: "provider neutral field drift",
			mutate: func(_ *testing.T, _ *benchmarkPairDescriptor, pair *repositorySamplingPair) {
				for _, cohort := range []*repositorySamplingCohort{&pair.Primary, &pair.Holdout} {
					spec := cohort.Runs["run.mcp.claude.json"]
					spec.AllowedTools = []string{"Read"}
					cohort.Runs["run.mcp.claude.json"] = spec
				}
			},
			wantErr: "provider rows differ beyond provider, model, and pricing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor := confluencePageMetadataPairDescriptor()
			pair := syntheticBenchmarkPair(t, descriptor)
			test.mutate(t, &descriptor, &pair)
			err := validateBenchmarkPair(descriptor, pair)
			if err == nil || !bytes.Contains([]byte(err.Error()), []byte(test.wantErr)) {
				t.Fatalf("error=%v want fragment %q", err, test.wantErr)
			}
		})
	}
	artifactBindingMutations := map[string]func(*RunSpec){
		"scenario": func(spec *RunSpec) { spec.ScenarioFile = "other.json" },
		"prompt":   func(spec *RunSpec) { spec.PromptFile = "other.md" },
		"schema":   func(spec *RunSpec) { spec.ResponseSchemaFile = "other.json" },
		"rubric":   func(spec *RunSpec) { spec.QualitativeRubricFile = "other.json" },
		"fixture":  func(spec *RunSpec) { spec.FixtureFile = "other.json" },
		"workspace": func(spec *RunSpec) {
			spec.WorkspaceTemplate = "other"
		},
	}
	for name, mutate := range artifactBindingMutations {
		t.Run("artifact binding "+name, func(t *testing.T) {
			descriptor := confluencePageMetadataPairDescriptor()
			pair := syntheticBenchmarkPair(t, descriptor)
			spec := pair.Primary.Runs[benchmarkPairProviders[0].runFile]
			mutate(&spec)
			pair.Primary.Runs[benchmarkPairProviders[0].runFile] = spec
			if err := validateBenchmarkPair(descriptor, pair); err == nil ||
				!bytes.Contains([]byte(err.Error()), []byte("artifact bindings drifted")) {
				t.Fatalf("error=%v want artifact binding rejection", err)
			}
		})
	}
}

func TestBenchmarkPairHarnessRejectsMigratedDescriptorWeakening(t *testing.T) {
	for _, descriptor := range migratedBenchmarkPairDescriptors() {
		for index, artifact := range descriptor.distinctArtifacts {
			t.Run(descriptor.primaryName+"/remove-"+artifact, func(t *testing.T) {
				drifted := descriptor
				drifted.distinctArtifacts = slices.Delete(slices.Clone(descriptor.distinctArtifacts), index, index+1)
				if err := validateBenchmarkPairDescriptor(drifted); err == nil ||
					!bytes.Contains([]byte(err.Error()), []byte("descriptor drifted")) {
					t.Fatalf("error=%v want migrated descriptor drift", err)
				}
			})
		}
		t.Run(descriptor.primaryName+"/workspace-relationship", func(t *testing.T) {
			drifted := descriptor
			if drifted.workspaceRelationship == benchmarkWorkspaceSameNeutralTree {
				drifted.workspaceRelationship = benchmarkWorkspaceDistinctTrees
			} else {
				drifted.workspaceRelationship = benchmarkWorkspaceSameNeutralTree
			}
			if err := validateBenchmarkPairDescriptor(drifted); err == nil ||
				!bytes.Contains([]byte(err.Error()), []byte("descriptor drifted")) {
				t.Fatalf("error=%v want migrated descriptor drift", err)
			}
		})
	}
	unknown := confluencePageMetadataPairDescriptor()
	unknown.primaryName = "unregistered-pair"
	if err := validateBenchmarkPairDescriptor(unknown); err == nil ||
		!bytes.Contains([]byte(err.Error()), []byte("descriptor is not registered")) {
		t.Fatalf("error=%v want unregistered descriptor rejection", err)
	}
}

func TestBenchmarkPairHarnessDescriptorFactoryInventory(t *testing.T) {
	factories := migratedBenchmarkPairDescriptorFactories()
	if len(factories) != 8 {
		t.Fatalf("migrated descriptor factories=%d want=8", len(factories))
	}
	names := make([]string, 0, len(factories))
	seen := make(map[string]bool, len(factories))
	for _, factory := range factories {
		descriptor := factory()
		if err := validateBenchmarkPairDescriptor(descriptor); err != nil {
			t.Fatalf("factory %q returned an invalid descriptor: %v", descriptor.primaryName, err)
		}
		if seen[descriptor.primaryName] {
			t.Fatalf("duplicate migrated descriptor factory %q", descriptor.primaryName)
		}
		seen[descriptor.primaryName] = true
		names = append(names, descriptor.primaryName)
	}
	// This literal oracle is intentionally independent from both the factory
	// inventory and the closed descriptor switch above.
	want := []string{
		"confluence-page-metadata-mcp",
		"jira-history-summary-mcp",
		"confluence-attachment-evidence-mcp",
		"confluence-section-version-bound-mcp",
		"confluence-section-bound-recovery-mcp",
		"jira-structure-folder-selection-recovery-mcp",
		"jira-snapshot-reconciliation-mcp",
		"jira-search-zero-progress-mcp",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("migrated descriptor names=%v want=%v", names, want)
	}
}

func TestBenchmarkPairHarnessValidatesRepositoryInventory(t *testing.T) {
	for _, factory := range migratedBenchmarkPairDescriptorFactories() {
		descriptor := factory()
		t.Run(descriptor.primaryName, func(t *testing.T) {
			pair := loadRepositorySamplingPairContract(t, descriptor.primaryName)
			if err := validateBenchmarkPair(descriptor, pair); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func syntheticBenchmarkPair(
	t *testing.T,
	descriptor benchmarkPairDescriptor,
) repositorySamplingPair {
	t.Helper()
	root := t.TempDir()
	primaryRoot := filepath.Join(root, descriptor.primaryName)
	holdoutRoot := filepath.Join(root, descriptor.primaryName+"-holdout")
	for _, cohortRoot := range []string{primaryRoot, holdoutRoot} {
		if err := os.MkdirAll(filepath.Join(cohortRoot, "workspace"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeBenchmarkPairFile(t, filepath.Join(cohortRoot, descriptor.responseSchema), "schema")
	}
	for _, artifact := range descriptor.distinctArtifacts {
		writeBenchmarkPairFile(t, filepath.Join(primaryRoot, artifact), artifact+"-primary")
		writeBenchmarkPairFile(t, filepath.Join(holdoutRoot, artifact), artifact+"-holdout")
	}
	writeBenchmarkPairFile(t, filepath.Join(primaryRoot, "workspace", "README.md"), "neutral")
	holdoutWorkspace := "neutral"
	if descriptor.workspaceRelationship == benchmarkWorkspaceDistinctTrees {
		holdoutWorkspace = "distinct"
	}
	writeBenchmarkPairFile(t, filepath.Join(holdoutRoot, "workspace", "README.md"), holdoutWorkspace)

	primaryRuns := syntheticBenchmarkProviderRows(3)
	holdoutRuns := syntheticBenchmarkProviderRows(1)
	return repositorySamplingPair{
		Primary: repositorySamplingCohort{Root: primaryRoot, Runs: primaryRuns},
		Holdout: repositorySamplingCohort{Root: holdoutRoot, Runs: holdoutRuns},
	}
}

func syntheticBenchmarkProviderRows(repetitions int) map[string]RunSpec {
	rows := make(map[string]RunSpec, len(benchmarkPairProviders))
	for index, provider := range benchmarkPairProviders {
		rows[provider.runFile] = RunSpec{
			BackendMode:           BackendModeSynthetic,
			Category:              "surface-native",
			Surface:               string(SurfaceATLMCP),
			ToolTransport:         "mcp",
			Provider:              provider.provider,
			Variant:               "synthetic-pair-v1",
			Model:                 provider.model,
			Reasoning:             "high",
			AllowedTools:          []string{},
			AllowedMCPTools:       []string{"synthetic_read"},
			Repetitions:           repetitions,
			Pricing:               Pricing{InputMicroUSDPerMillionTokens: int64(index + 1)},
			ScenarioFile:          "scenario.v1.json",
			PromptFile:            "prompt.mcp.v1.md",
			ResponseSchemaFile:    "response-schema.v1.json",
			QualitativeRubricFile: "rubric.v1.json",
			FixtureFile:           "fixture.json",
			WorkspaceTemplate:     "workspace",
		}
	}
	return rows
}

func writeBenchmarkPairFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
