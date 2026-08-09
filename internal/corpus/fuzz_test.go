package corpus

import (
	"strings"
	"testing"
)

func FuzzStrictGenerationCodecs(f *testing.F) {
	manifest := validManifest()
	manifestBytes, err := canonicalManifest(manifest, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	receipt := validReceiptForFuzz(f, manifest)
	receiptBytes, err := canonicalReceipt(receipt, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	pointerBytes, err := canonicalPointer(Pointer{
		SchemaVersion: PointerSchemaV1, GenerationID: strings.Repeat("0", 32), GenerationDigest: receipt.GenerationDigest,
	})
	if err != nil {
		f.Fatal(err)
	}
	for kind, seed := range [][]byte{
		manifestBytes,
		receiptBytes,
		pointerBytes,
		[]byte(`{"schema_version":1,"schema_version":2}`),
		{0xff, 0x00, '{', '}'},
	} {
		f.Add(byte(kind%3), seed)
	}
	f.Fuzz(func(_ *testing.T, kind byte, data []byte) {
		switch kind % 3 {
		case 0:
			_, _ = parseManifest(data, Limits{MaxManifestBytes: 1 << 20})
		case 1:
			_, _ = parseReceipt(data, Limits{MaxManifestBytes: 1 << 20})
		case 2:
			_, _ = parsePointer(data)
		}
	})
}

func FuzzMemberSpecValidation(f *testing.F) {
	for _, seed := range []string{"safe/member", "../escape", "dir\\file", "dir:name", "π/member"} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, value string) {
		_ = validateMemberSpec(MemberSpec{
			Service: ServiceJira, StableID: "synthetic", Role: RoleNative, Path: value,
		}, Limits{MaxPathBytes: 1024, MaxPathDepth: 32})
	})
}

func validReceiptForFuzz(t testing.TB, manifest Manifest) Receipt {
	t.Helper()
	manifestBytes, err := canonicalManifest(manifest, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := inventoryDigest(manifest.Members)
	if err != nil {
		t.Fatal(err)
	}
	receipt := Receipt{
		SchemaVersion: ReceiptSchemaV1, ManifestSchema: ManifestSchemaV1,
		ProjectionSchema: manifest.ProjectionSchema, GeneratorVersion: manifest.GeneratorVersion, BuildState: manifest.BuildState,
		PredecessorDigest: manifest.PredecessorDigest, Qualifications: append([]Qualification(nil), manifest.Qualifications...),
		TombstoneDigest: manifest.TombstoneDigest, Totals: manifest.Totals,
		ManifestDigest: manifestDigest(manifestBytes), InventoryDigest: inventory,
	}
	receipt.GenerationDigest, err = generationDigest(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
