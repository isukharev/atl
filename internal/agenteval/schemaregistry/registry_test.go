package schemaregistry

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

func TestBuiltInRegistryIsClosedCanonicalAndImmutable(t *testing.T) {
	registry, err := BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(registry)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, BuiltInBytes()) {
		t.Fatal("built-in registry is not its canonical encoding")
	}
	if len(registry.Entries) < 1 {
		t.Fatal("built-in registry is empty")
	}
	registry.Entries[0].Readable = append(registry.Entries[0].Readable, 999)
	again, err := BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(again.Entries[0].Readable, 999) {
		t.Fatal("caller mutated the built-in registry")
	}
	zeroBound := cloneRegistry(again)
	zeroBound.Entries[0].MaxBytes = 0
	if _, err := Encode(zeroBound); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("zero byte bound error=%v", err)
	}
	driftedBound := cloneRegistry(again)
	driftedBound.Entries[0].MaxBytes++
	if _, err := Encode(driftedBound); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("closed inventory drift error=%v", err)
	}

	for name, mutation := range map[string][]byte{
		"future":                  bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"unknown":                 bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":1,"unknown":true`), 1),
		"duplicate":               bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		"trailing":                append(slices.Clone(encoded), []byte(`{}`)...),
		"noncanonical whitespace": append([]byte(" "), encoded...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(bytes.NewReader(mutation)); !errors.Is(err, ErrInvalidRegistry) {
				t.Fatalf("error=%v, want invalid registry", err)
			}
		})
	}
}

func TestRegistryMigrationGraphIsUniqueAscendingAndContentAddressed(t *testing.T) {
	registry, err := BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	path, err := registry.MigrationPath("atl-profile", "private-workspace", 3, 4)
	if err != nil || len(path) != 1 || path[0].ID != "atl-profile/private-workspace/v3-to-v4" {
		t.Fatalf("path=%+v err=%v", path, err)
	}
	if len(ImplementationSHA256(path[0])) != 64 {
		t.Fatal("migration implementation identity is not content-addressed")
	}
	for _, versions := range [][2]int{{4, 3}, {3, 3}, {2, 4}, {3, 5}} {
		if _, err := registry.MigrationPath("atl-profile", "private-workspace", versions[0], versions[1]); !errors.Is(err, ErrMigrationPath) {
			t.Fatalf("versions=%v error=%v", versions, err)
		}
	}
	if _, err := registry.MigrationPath("standalone", "missing", 1, 2); !errors.Is(err, ErrMigrationPath) {
		t.Fatalf("unknown error=%v", err)
	}

	descriptor := Descriptor{
		Namespace: "standalone", Kind: "ambiguous", Owner: "standalone", Current: 3,
		Readable: []int{1, 2, 3}, Emitted: []int{3}, Executable: []int{3}, Disposition: "preserve",
		Privacy: "public", Migration: "explicit", MaxBytes: 1024,
		SchemaResource: "agent-eval/schema/standalone/ambiguous@3",
		MigrationEdges: []MigrationEdge{
			{ID: "one-two", From: 1, To: 2, Implementation: "one-two"},
			{ID: "one-three", From: 1, To: 3, Implementation: "one-three"},
			{ID: "two-three", From: 2, To: 3, Implementation: "two-three"},
		},
	}
	if err := validateEdges(descriptor); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("ambiguous graph error=%v", err)
	}
}

func TestRegistryInspectionBindsSchemaMigrationAndWholeRegistry(t *testing.T) {
	registry, err := BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := registry.Inspect("atl-profile", "private-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Descriptor.Current != 4 || inspection.SupportedMigrations != 1 || inspection.MigrationUnavailable ||
		len(inspection.SchemaSHA256) != 64 || len(inspection.MigrationSHA256) != 64 || len(inspection.RegistrySHA256) != 64 {
		t.Fatalf("inspection=%+v", inspection)
	}
	withoutMigration, err := registry.Inspect("standalone", "project-config")
	if err != nil || !withoutMigration.MigrationUnavailable || withoutMigration.SupportedMigrations != 0 {
		t.Fatalf("inspection=%+v err=%v", withoutMigration, err)
	}
}
