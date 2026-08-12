package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestGenerationDeltaCanonicalRoundTripAndClassification(t *testing.T) {
	retained := normalizeDocument(validIndexerDocument(t))
	retained.ID = digestByte('1')
	changedBefore := retained
	changedBefore.ID = digestByte('2')
	changedBefore.Title = "before"
	removed := retained
	removed.ID = digestByte('3')
	changedAfter := changedBefore
	changedAfter.Title = "after"
	added := retained
	added.ID = digestByte('4')

	delta, err := BuildGenerationDelta(strings.Repeat("1", 32), digestByte('a'), digestByte('b'), digestByte('c'),
		[]GenerationDeltaBinding{validGenerationDeltaBinding(ServiceJira)},
		[]IndexerDocument{removed, changedBefore, retained}, []IndexerDocument{added, retained, changedAfter}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if delta.Counts != (GenerationDeltaCounts{Added: 1, Retained: 1, Changed: 1, Tombstoned: 1}) || len(delta.Records) != 4 {
		t.Fatalf("delta=%#v", delta)
	}
	wantStates := []GenerationDeltaState{GenerationDeltaRetained, GenerationDeltaChanged, GenerationDeltaTombstoned, GenerationDeltaAdded}
	for index, record := range delta.Records {
		if record.State != wantStates[index] {
			t.Fatalf("record[%d]=%#v", index, record)
		}
	}
	if delta.Records[2].Reason != GenerationDeltaAbsentQualified || delta.Records[3].Reason != "" {
		t.Fatalf("records=%#v", delta.Records)
	}

	canonical, err := CanonicalGenerationDelta(delta, Limits{})
	if err != nil || !bytes.HasSuffix(canonical, []byte("\n")) {
		t.Fatalf("canonical=%q error=%v", canonical, err)
	}
	parsed, err := ParseGenerationDelta(canonical, Limits{})
	if err != nil || !bytes.Equal(mustCanonicalGenerationDelta(t, parsed), canonical) {
		t.Fatalf("parsed=%#v error=%v", parsed, err)
	}
	shuffled, err := BuildGenerationDelta(strings.Repeat("1", 32), digestByte('a'), digestByte('b'), digestByte('c'),
		[]GenerationDeltaBinding{validGenerationDeltaBinding(ServiceJira)},
		[]IndexerDocument{retained, removed, changedBefore}, []IndexerDocument{changedAfter, added, retained}, Limits{})
	if err != nil || !bytes.Equal(mustCanonicalGenerationDelta(t, shuffled), canonical) {
		t.Fatalf("shuffled=%#v error=%v", shuffled, err)
	}

	sum := sha256.Sum256(canonical)
	artifact, err := BuildGenerationDiffArtifact(delta, digestByte('d'), hex.EncodeToString(sum[:]), Limits{})
	if err != nil || artifact.SuccessorGenerationDigest != digestByte('d') || artifact.TombstoneDigest != hex.EncodeToString(sum[:]) {
		t.Fatalf("artifact=%#v error=%v", artifact, err)
	}
	artifactBytes, err := CanonicalGenerationDiffArtifact(artifact, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	parsedArtifact, err := ParseGenerationDiffArtifact(artifactBytes, Limits{})
	if err != nil || parsedArtifact.Counts != delta.Counts || len(parsedArtifact.Records) != len(delta.Records) {
		t.Fatalf("artifact=%#v error=%v", parsedArtifact, err)
	}
	if _, err := BuildGenerationDiffArtifact(delta, digestByte('d'), digestByte('e'), Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("unbound artifact error=%v", err)
	}
	artifact.Records = append([]GenerationDeltaRecord(nil), artifact.Records...)
	artifact.Records[0].Service = ServiceConfluence
	artifact.Records[0].Kind = ObjectPage
	if _, err := CanonicalGenerationDiffArtifact(artifact, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("artifact service outside binding error=%v", err)
	}
	invalidArtifactBytes, err := marshalCanonical(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseGenerationDiffArtifact(invalidArtifactBytes, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("parsed artifact service outside binding error=%v", err)
	}
}

func TestGenerationDeltaTreatsRenameAsChangedIdentity(t *testing.T) {
	before := normalizeDocument(validIndexerDocument(t))
	after := before
	after.Key = "RENAMED-2"
	after.Title = "Renamed"
	after.Source.Path = "RENAMED/RENAMED-2.wiki"
	delta, err := BuildGenerationDelta(strings.Repeat("2", 32), digestByte('a'), digestByte('b'), digestByte('c'),
		[]GenerationDeltaBinding{validGenerationDeltaBinding(ServiceJira)}, []IndexerDocument{before}, []IndexerDocument{after}, Limits{})
	if err != nil || delta.Counts.Changed != 1 || delta.Counts.Tombstoned != 0 || delta.Records[0].ID != before.ID {
		t.Fatalf("delta=%#v error=%v", delta, err)
	}
}

func TestGenerationDeltaTreatsConfluenceRelocationAsChangedIdentity(t *testing.T) {
	before := normalizeDocument(validIndexerDocument(t))
	before.Service = ServiceConfluence
	before.Kind = ObjectPage
	before.Key = ""
	before.Source.Path = "SPACE/Before/page.csf"
	after := before
	after.Title = "Renamed page"
	after.Source.Path = "SPACE/After/page.csf"
	delta, err := BuildGenerationDelta(strings.Repeat("2", 32), digestByte('a'), digestByte('b'), digestByte('c'),
		[]GenerationDeltaBinding{validGenerationDeltaBinding(ServiceConfluence)}, []IndexerDocument{before}, []IndexerDocument{after}, Limits{})
	if err != nil || delta.Counts.Changed != 1 || delta.Counts.Tombstoned != 0 || delta.Records[0].ID != before.ID {
		t.Fatalf("delta=%#v error=%v", delta, err)
	}
}

func TestGenerationDeltaRejectsIdentityAndLineageDrift(t *testing.T) {
	document := normalizeDocument(validIndexerDocument(t))
	otherKind := document
	otherKind.Kind = ObjectAttachment
	otherService := document
	otherService.Service = ServiceConfluence
	otherService.Kind = ObjectPage
	otherService.Key = ""
	for name, run := range map[string]func() error{
		"generation id": func() error {
			_, err := BuildGenerationDelta("short", digestByte('a'), digestByte('b'), digestByte('c'), []GenerationDeltaBinding{validGenerationDeltaBinding(ServiceJira)}, []IndexerDocument{}, []IndexerDocument{}, Limits{})
			return err
		},
		"binding scope": func() error {
			binding := validGenerationDeltaBinding(ServiceJira)
			binding.ScopeDigest = "short"
			_, err := BuildGenerationDelta(strings.Repeat("1", 32), digestByte('a'), digestByte('b'), digestByte('c'), []GenerationDeltaBinding{binding}, []IndexerDocument{}, []IndexerDocument{}, Limits{})
			return err
		},
		"duplicate binding": func() error {
			binding := validGenerationDeltaBinding(ServiceJira)
			_, err := BuildGenerationDelta(strings.Repeat("1", 32), digestByte('a'), digestByte('b'), digestByte('c'), []GenerationDeltaBinding{binding, binding}, []IndexerDocument{}, []IndexerDocument{}, Limits{})
			return err
		},
		"document service outside binding": func() error {
			_, err := BuildGenerationDelta(strings.Repeat("1", 32), digestByte('a'), digestByte('b'), digestByte('c'), []GenerationDeltaBinding{validGenerationDeltaBinding(ServiceJira)}, []IndexerDocument{otherService}, []IndexerDocument{}, Limits{})
			return err
		},
		"identity kind drift": func() error {
			_, err := BuildGenerationDelta(strings.Repeat("1", 32), digestByte('a'), digestByte('b'), digestByte('c'), []GenerationDeltaBinding{validGenerationDeltaBinding(ServiceJira)}, []IndexerDocument{document}, []IndexerDocument{otherKind}, Limits{})
			return err
		},
		"duplicate document": func() error {
			_, err := BuildGenerationDelta(strings.Repeat("1", 32), digestByte('a'), digestByte('b'), digestByte('c'), []GenerationDeltaBinding{validGenerationDeltaBinding(ServiceJira)}, []IndexerDocument{document, document}, []IndexerDocument{}, Limits{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGenerationDeltaStrictCodecRejectsTamper(t *testing.T) {
	document := normalizeDocument(validIndexerDocument(t))
	delta, err := BuildGenerationDelta(strings.Repeat("1", 32), digestByte('a'), digestByte('b'), digestByte('c'),
		[]GenerationDeltaBinding{validGenerationDeltaBinding(ServiceJira)}, []IndexerDocument{document}, []IndexerDocument{}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	canonical := mustCanonicalGenerationDelta(t, delta)
	for name, data := range map[string][]byte{
		"unknown field": bytes.Replace(canonical, []byte(`"schema_version":1`), []byte(`"schema_version":1,"future":true`), 1),
		"non canonical": append([]byte(" "), canonical...),
		"trailing":      append(append([]byte(nil), canonical...), []byte("{}\n")...),
		"future schema": bytes.Replace(canonical, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseGenerationDelta(data, Limits{}); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	tampered := delta
	tampered.Records = append([]GenerationDeltaRecord(nil), delta.Records...)
	tampered.Records[0].Reason = ""
	if _, err := CanonicalGenerationDelta(tampered, Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered error=%v", err)
	}
	if _, err := ParseGenerationDelta(canonical, Limits{MaxMemberBytes: 8}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("bounded error=%v", err)
	}
}

func validGenerationDeltaBinding(service Service) GenerationDeltaBinding {
	return GenerationDeltaBinding{
		Service: service, ReceiptSchema: CaptureReceiptSchemaV1,
		ScopeDigest: digestByte('a'), SelectorDigest: digestByte('b'), OptionsDigest: digestByte('c'),
	}
}

func mustCanonicalGenerationDelta(t testing.TB, delta GenerationDelta) []byte {
	t.Helper()
	data, err := CanonicalGenerationDelta(delta, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
