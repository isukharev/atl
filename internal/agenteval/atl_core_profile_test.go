package agenteval

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/core"
	"github.com/isukharev/atl/internal/agenteval/extension"
	profileatl "github.com/isukharev/atl/internal/agenteval/profile/atl"
)

func TestATLCoreProfileAdmitsCommittedSyntheticCorpus(t *testing.T) {
	builtInExtension := atlBuiltInExtensionProfile{}
	extensionDescriptor := builtInExtension.Capabilities()
	if err := extension.ValidateDescriptor(extensionDescriptor); err != nil ||
		extensionDescriptor.Role != extension.RoleProfile || len(extensionDescriptor.Operations) != 2 {
		t.Fatalf("ATL extension descriptor=%+v err=%v", extensionDescriptor, err)
	}
	extensionDescriptor.Capabilities[0].State = extension.CapabilityUnknown
	if atlExtensionProfileDescriptor().Capabilities[0].State != extension.CapabilitySupported {
		t.Fatal("ATL extension descriptor is mutable")
	}

	profile, err := newATLCoreProfile(nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := profile.Descriptor()
	if descriptor.ID != profileatl.ProfileID || extensionDescriptor.ID != string(descriptor.ID) ||
		len(descriptor.Capabilities) != CapabilityCatalogItemCount {
		t.Fatalf("descriptor=%+v", descriptor)
	}
	catalog, err := PinnedCapabilityCatalog()
	if err != nil {
		t.Fatal(err)
	}
	wantCapabilities := make(map[core.CapabilityID]struct{}, len(catalog.Capabilities))
	for _, capability := range catalog.Capabilities {
		wantCapabilities[core.CapabilityID(capability.ID)] = struct{}{}
	}
	for index, capability := range descriptor.Capabilities {
		if _, exists := wantCapabilities[capability.ID]; !exists || capability.Support != core.SupportSupported {
			t.Fatalf("capability %d=%+v is not a supported pinned capability", index, capability)
		}
		delete(wantCapabilities, capability.ID)
	}
	if len(wantCapabilities) != 0 {
		t.Fatalf("profile omitted pinned capabilities: %v", wantCapabilities)
	}
	descriptor.Capabilities[0].Support = core.SupportUnknown
	if profile.Descriptor().Capabilities[0].Support != core.SupportSupported {
		t.Fatal("ATL profile descriptor is mutable")
	}

	registry, err := core.NewRegistry(profile)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := core.NewEngine(registry)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join("..", "..", "benchmarks", "agent-eval", "*", "run.*.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantRunSpecs := 0
	for _, version := range loadEvaluatorBehaviorContract(t).CorpusVersions {
		if version.Artifact == "run_spec" {
			wantRunSpecs += version.Count
		}
	}
	if wantRunSpecs == 0 || len(paths) != wantRunSpecs {
		t.Fatalf("committed run specs=%d, want %d from the behavior contract", len(paths), wantRunSpecs)
	}
	identities := make(map[core.PlanID]string, len(paths))
	for _, path := range paths {
		contract, err := resolveRunContract(path)
		if err != nil {
			t.Fatalf("resolve committed run contract: %v", err)
		}
		if err := builtInExtension.Validate(contract); err != nil {
			t.Fatalf("profile.validate rejected committed run contract: %v", err)
		}
		plan, err := projectATLRunContract(contract)
		if err != nil {
			t.Fatalf("project committed run contract: %v", err)
		}
		again, err := projectATLRunContract(contract)
		if err != nil || !reflect.DeepEqual(plan, again) {
			t.Fatalf("ATL projection is not deterministic: %v", err)
		}
		if previous, duplicate := identities[plan.ID]; duplicate {
			t.Fatalf("duplicate core plan identity for two committed contracts: %s and %s", previous, path)
		}
		identities[plan.ID] = path
		admitted, err := engine.Admit(plan)
		if err != nil {
			t.Fatalf("admit committed ATL contract: %v", err)
		}
		canonical := admitted.Plan()
		if canonical.Task.ID != core.TaskID(contract.scenario.ID) || canonical.Attempts != uint32(contract.spec.Repetitions) || canonical.Profile != profileatl.ProfileID {
			t.Fatalf("admitted projection lost contract identity: %+v", canonical)
		}
	}
}

func TestATLCoreProjectionBindsExecutionInputs(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "benchmarks", "agent-eval", "*", "run.*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("committed run specs: %v", err)
	}
	contract, err := resolveRunContract(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "fixture.txt"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	contract.workspaceTemplate = workspace
	baseline, err := projectATLRunContract(contract)
	if err != nil {
		t.Fatal(err)
	}

	promptChanged := contract
	promptChanged.providerPrompt = append(append([]byte(nil), contract.providerPrompt...), '\n')
	assertATLCoreProjectionChanged(t, baseline, promptChanged, "provider prompt")

	fixtureChanged := contract
	fixtureChanged.fixture = &MockFixture{}
	assertATLCoreProjectionChanged(t, baseline, fixtureChanged, "fixture")

	if err := os.WriteFile(filepath.Join(workspace, "fixture.txt"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertATLCoreProjectionChanged(t, baseline, contract, "workspace")
}

func TestATLCoreAggregatePreservesLargeLegacyGroups(t *testing.T) {
	result, err := Evaluate(validScenario(), validObservation())
	if err != nil {
		t.Fatal(err)
	}
	results := make([]Result, 1025)
	for index := range results {
		results[index] = result
	}
	aggregate, err := AggregateResults(results)
	if err != nil {
		t.Fatalf("aggregate legacy-sized group: %v", err)
	}
	if len(aggregate.Groups) != 1 || aggregate.Groups[0].Runs != 1025 ||
		aggregate.Groups[0].EligibleRuns != 1025 || aggregate.Groups[0].Passes != 1025 {
		t.Fatalf("large aggregate=%+v", aggregate.Groups)
	}
}

func assertATLCoreProjectionChanged(t *testing.T, baseline core.Plan, changed resolvedRunContract, name string) {
	t.Helper()
	projected, err := projectATLRunContract(changed)
	if err != nil {
		t.Fatalf("project changed %s: %v", name, err)
	}
	if projected.ID == baseline.ID || projected.Fixture.ID == baseline.Fixture.ID {
		t.Fatalf("%s did not change plan and fixture identities", name)
	}
}
