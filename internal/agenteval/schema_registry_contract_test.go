package agenteval

import (
	"fmt"
	"slices"
	"testing"
)

func TestStandaloneSchemaRegistryExactlyProjectsProductContract(t *testing.T) {
	contract := loadStandaloneProductContractFixture(t)
	registry, err := BuiltInStandaloneSchemaRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := compareStandaloneSchemaRegistryContract(contract.ArtifactSchemas, registry); err != nil {
		t.Fatal(err)
	}
	if len(registry.Entries) != len(contract.ArtifactSchemas) {
		t.Fatalf("registry entries=%d, contract schemas=%d", len(registry.Entries), len(contract.ArtifactSchemas))
	}

	mutated := registry
	mutated.Entries = slices.Clone(registry.Entries)
	mutated.Entries[0].Owner = "standalone"
	if err := compareStandaloneSchemaRegistryContract(contract.ArtifactSchemas, mutated); err == nil {
		t.Fatal("owner drift passed the product-contract projection oracle")
	}
	mutated = registry
	mutated.Entries = slices.Clone(registry.Entries)
	mutated.Entries[0].MaxBytes++
	if err := compareStandaloneSchemaRegistryContract(contract.ArtifactSchemas, mutated); err == nil {
		t.Fatal("bound drift passed the product-contract projection oracle")
	}
}

func compareStandaloneSchemaRegistryContract(schemas []standaloneArtifactSchema, registry StandaloneSchemaRegistry) error {
	if len(schemas) != len(registry.Entries) {
		return fmt.Errorf("schema registry membership differs from product contract")
	}
	for index, schema := range schemas {
		descriptor := registry.Entries[index]
		key := schema.Namespace + "/" + schema.Kind
		if descriptor.Namespace != schema.Namespace || descriptor.Kind != schema.Kind || descriptor.Owner != standaloneSchemaOwner(schema.Namespace, schema.Kind) ||
			descriptor.Current != schema.Current || !slices.Equal(descriptor.Readable, schema.Readable) || !slices.Equal(descriptor.Emitted, schema.Emitted) ||
			!slices.Equal(descriptor.Executable, schema.Executable) || descriptor.Disposition != schema.Disposition || descriptor.Privacy != schema.Privacy ||
			descriptor.Migration != schema.Migration || schema.MaxBytes == nil || descriptor.MaxBytes != *schema.MaxBytes ||
			descriptor.SchemaResource != fmt.Sprintf("agent-eval/schema/%s@%d", key, schema.Current) {
			return fmt.Errorf("schema registry descriptor %q differs from product contract", key)
		}
		if key == "atl-profile/private-workspace" {
			if len(descriptor.MigrationEdges) != 1 || descriptor.MigrationEdges[0] != (StandaloneSchemaMigrationEdge{
				ID: "atl-profile/private-workspace/v3-to-v4", From: 3, To: 4,
				Implementation: "schema-version-3-to-4-preserve-encoded-fields-v1",
			}) {
				return fmt.Errorf("schema registry migration edge %q is not the reviewed implementation", key)
			}
		} else if len(descriptor.MigrationEdges) != 0 {
			return fmt.Errorf("schema registry descriptor %q invents a migration edge", key)
		}
	}
	return nil
}

func standaloneSchemaOwner(namespace, kind string) string {
	if namespace == "atl-profile" {
		return "atl-profile"
	}
	switch kind {
	case "agent-adapter-contract", "agent-observation":
		return "agentadapter"
	case "execution-backend-contract", "trial-plan", "trial-receipt":
		return "executionbackend"
	case "analysis-plan", "experiment-capability-contract", "experiment-design", "experiment-manifest", "trial-record":
		return "experiment"
	case "grade-receipt", "grader-contract", "grading-plan":
		return "grading"
	case "adapter-manifest", "adapter-message", "extension-conformance-bundle", "extension-conformance-report":
		return "extension"
	case "attempt-event", "attempt-ledger", "attempt-plan":
		return "lifecycle"
	default:
		return "standalone"
	}
}
