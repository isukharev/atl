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

func FuzzStrictIndexerCodecs(f *testing.F) {
	document := normalizeDocument(validIndexerDocument(f))
	documents, err := CanonicalIndexerDocuments([]IndexerDocument{document}, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	edge := validIndexerEdge(f, document.ID)
	edges, err := CanonicalIndexerEdges([]IndexerEdge{edge}, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	receipt, err := BuildIndexerReceipt([]IndexerQualification{{
		Service: ServiceJira, State: QualificationPartial, Basis: QualificationStructural,
		ScopeDigest: digestByte('a'), Reasons: []QualificationReason{QualificationLegacyMirror},
	}}, []IndexerDocument{document}, []IndexerEdge{edge}, []MarkdownMember{{
		DocumentID: document.ID, Path: document.MarkdownPath, Size: int64(len(document.Text)), SHA256: document.MarkdownSHA256,
	}}, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	receiptBytes, err := CanonicalIndexerReceipt(receipt, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	for kind, seed := range [][]byte{
		documents,
		edges,
		receiptBytes,
		[]byte(`{"schema_version":1,"schema_version":2}`),
		{0xff, 0x00, '{', '}'},
	} {
		f.Add(byte(kind%3), seed)
	}
	f.Fuzz(func(_ *testing.T, kind byte, data []byte) {
		limits := Limits{MaxMembers: 1_000, MaxMemberBytes: 1 << 20, MaxTotalBytes: 1 << 20, MaxManifestBytes: 1 << 20, MaxPathBytes: 1_024, MaxPathDepth: 32}
		switch kind % 3 {
		case 0:
			_, _ = ParseIndexerDocuments(data, limits)
		case 1:
			_, _ = ParseIndexerEdges(data, limits)
		case 2:
			_, _ = ParseIndexerReceipt(data, limits)
		}
	})
}

func FuzzStrictIndexerV2Codecs(f *testing.F) {
	documents, edges, markdown, artifact, member, qualifications := validIndexerV2Bundle(f)
	artifacts, err := CanonicalIndexerArtifacts([]IndexerArtifact{artifact}, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	receipt, err := BuildIndexerReceiptV2(qualifications, documents, edges, markdown,
		[]IndexerArtifact{artifact}, []ArtifactMember{member}, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	receiptBytes, err := CanonicalIndexerReceiptV2(receipt, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	for kind, seed := range [][]byte{
		artifacts,
		receiptBytes,
		[]byte(`{"schema_version":2,"schema_version":3}`),
		{0xff, 0x00, '{', '}'},
	} {
		f.Add(byte(kind%2), seed)
	}
	f.Fuzz(func(_ *testing.T, kind byte, data []byte) {
		limits := Limits{MaxMembers: 1_000, MaxMemberBytes: 1 << 20, MaxTotalBytes: 1 << 20, MaxManifestBytes: 1 << 20, MaxPathBytes: 1_024, MaxPathDepth: 32}
		if kind%2 == 0 {
			_, _ = ParseIndexerArtifacts(data, limits)
		} else {
			_, _ = ParseIndexerReceiptV2(data, limits)
		}
	})
}

func FuzzStrictCaptureReceiptCodec(f *testing.F) {
	receipt, err := BuildCaptureReceipt(validCaptureReceiptInput(), Limits{})
	if err != nil {
		f.Fatal(err)
	}
	canonical, err := CanonicalCaptureReceipt(receipt, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{
		canonical,
		[]byte(`{"schema_version":1,"schema_version":2}`),
		{0xff, 0x00, '{', '}'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ParseCaptureReceipt(data, Limits{
			MaxMembers: 1_000, MaxMemberBytes: 1 << 20, MaxTotalBytes: 1 << 20,
			MaxManifestBytes: 1 << 20, MaxPathBytes: 1_024, MaxPathDepth: 32,
		})
	})
}

func FuzzStrictBuildActiveCodec(f *testing.F) {
	canonical, err := CanonicalBuildActive(validBuildActive(), Limits{})
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range [][]byte{
		canonical,
		[]byte(`{"schema_version":1,"schema_version":2}`),
		{0xff, 0x00, '{', '}'},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ParseBuildActive(data, Limits{
			MaxMembers: 1_000, MaxMemberBytes: 1 << 20, MaxTotalBytes: 1 << 20,
			MaxManifestBytes: 1 << 20, MaxPathBytes: 1_024, MaxPathDepth: 32,
		})
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
