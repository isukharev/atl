package corpus

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestIndexerHandoffStrictCanonicalRoundTrip(t *testing.T) {
	documents := validIndexerHandoffMember()
	handoff, err := BuildIndexerHandoff(strings.Repeat("1", 32), digestByte('a'), IndexerSchemaV2, documents, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalIndexerHandoff(handoff, Limits{})
	if err != nil || !bytes.HasSuffix(canonical, []byte("\n")) {
		t.Fatalf("canonical=%q error=%v", canonical, err)
	}
	parsed, err := ParseIndexerHandoff(canonical, Limits{})
	if err != nil || parsed != handoff {
		t.Fatalf("parsed=%#v error=%v", parsed, err)
	}

	for name, mutate := range map[string]func(*IndexerHandoff){
		"generation": func(value *IndexerHandoff) { value.GenerationID = "short" },
		"digest":     func(value *IndexerHandoff) { value.GenerationDigest = strings.Repeat("A", 64) },
		"schema":     func(value *IndexerHandoff) { value.ProjectionSchema = IndexerSchemaV1 },
		"role":       func(value *IndexerHandoff) { value.Documents.Role = RoleNative },
		"stable id":  func(value *IndexerHandoff) { value.Documents.StableID = "other" },
		"path":       func(value *IndexerHandoff) { value.Documents.Path = "projection/jira/other.jsonl" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := handoff
			mutate(&invalid)
			if _, err := CanonicalIndexerHandoff(invalid, Limits{}); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := ParseIndexerHandoff(append([]byte(" "), canonical...), Limits{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("non-canonical error=%v", err)
	}
}

func validIndexerHandoffMember() Member {
	return Member{
		Service: ServiceJira, StableID: IndexerDocumentsStableID, Role: RoleDocument,
		Path: "projection/jira/documents.indexer-v1.jsonl", Size: 128, Mode: 0o600, SHA256: digestByte('b'),
	}
}
