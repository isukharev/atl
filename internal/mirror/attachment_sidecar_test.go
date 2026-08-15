package mirror

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func jiraAttachmentSidecarFixture() AttachmentSidecarV1 {
	return AttachmentSidecarV1{
		SchemaVersion: AttachmentSidecarSchemaV1, Service: CorpusSnapshotJira, ParentID: "10001",
		OriginSHA256: "sha256:" + strings.Repeat("0", 64), ParentRevision: "2026-01-01T00:00:00.000+0000",
		NativeSHA256: strings.Repeat("a", 64), MetadataSHA256: strings.Repeat("b", 64),
		InventoryComplete: true, BodiesState: AttachmentBodiesNotRequested, Complete: true,
		Count: 2, PartialReasons: []AttachmentPartialReason{},
		Attachments: []AttachmentSidecarRecord{
			{ID: "2", Filename: "b.bin", DeclaredSize: 2, Author: AttachmentSidecarAuthor{}, Body: AttachmentSidecarBody{State: AttachmentBodyNotRequested}},
			{ID: "1", Filename: "a.bin", MediaType: "application/octet-stream", DeclaredSize: 1, CreatedAt: "2026-01-01", Author: AttachmentSidecarAuthor{ID: "stable", Name: "user", DisplayName: "Fixture"}, Body: AttachmentSidecarBody{State: AttachmentBodyNotRequested}},
		},
	}
}

func confluenceCapturedAttachmentSidecarFixture() AttachmentSidecarV1 {
	return AttachmentSidecarV1{
		SchemaVersion: AttachmentSidecarSchemaV1, Service: CorpusSnapshotConfluence, ParentID: "20001", ParentVersion: 7,
		OriginSHA256: "sha256:" + strings.Repeat("1", 64),
		NativeSHA256: strings.Repeat("c", 64), MetadataSHA256: strings.Repeat("d", 64),
		InventoryComplete: true, BodiesState: AttachmentBodiesComplete, Complete: true,
		Count: 1, PartialReasons: []AttachmentPartialReason{},
		Attachments: []AttachmentSidecarRecord{{
			ID: "30001", Version: 2, Filename: "a.bin", MediaType: "application/octet-stream", DeclaredSize: 3,
			Author: AttachmentSidecarAuthor{}, Body: AttachmentSidecarBody{
				State: AttachmentBodyCaptured, Path: "DOC/page.attachments/30001.body", Size: 3, SHA256: strings.Repeat("e", 64),
			},
		}},
	}
}

func TestAttachmentSidecarV1DeterministicRoundTrip(t *testing.T) {
	for name, fixture := range map[string]AttachmentSidecarV1{
		"jira inventory":  jiraAttachmentSidecarFixture(),
		"confluence body": confluenceCapturedAttachmentSidecarFixture(),
	} {
		t.Run(name, func(t *testing.T) {
			firstID := fixture.Attachments[0].ID
			encoded, err := EncodeAttachmentSidecarV1(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if fixture.Attachments[0].ID != firstID || !bytes.HasSuffix(encoded, []byte("\n")) {
				t.Fatal("encoder mutated input or omitted newline")
			}
			decoded, err := DecodeAttachmentSidecarV1(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Attachments == nil || decoded.PartialReasons == nil || decoded.Attachments[0].ID == "" {
				t.Fatalf("decoded=%+v", decoded)
			}
			reencoded, err := EncodeAttachmentSidecarV1(decoded)
			if err != nil || !bytes.Equal(encoded, reencoded) {
				t.Fatalf("reencoded=%s error=%v", reencoded, err)
			}
		})
	}
}

func TestAttachmentSidecarV1PreservesInventoryPartial(t *testing.T) {
	fixture := jiraAttachmentSidecarFixture()
	fixture.InventoryComplete = false
	fixture.InventoryPartialReason = domain.JiraAttachmentPartialFieldUnavailable
	fixture.Complete = false
	fixture.PartialReasons = []AttachmentPartialReason{AttachmentReasonInventoryField}
	fixture.Count = 0
	fixture.Attachments = []AttachmentSidecarRecord{}
	encoded, err := EncodeAttachmentSidecarV1(fixture)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAttachmentSidecarV1(encoded)
	if err != nil || decoded.Complete || decoded.InventoryComplete || decoded.InventoryPartialReason != domain.JiraAttachmentPartialFieldUnavailable {
		t.Fatalf("decoded=%+v error=%v", decoded, err)
	}
}

func TestAttachmentSidecarV1PreservesUnavailableAndBodyPartial(t *testing.T) {
	for _, reason := range []string{AttachmentInventoryForbidden, AttachmentInventoryUnsupported} {
		fixture := jiraAttachmentSidecarFixture()
		fixture.InventoryComplete = false
		fixture.InventoryPartialReason = reason
		fixture.BodiesState = AttachmentBodiesPartial
		fixture.Complete = false
		fixture.Count = 0
		fixture.Attachments = []AttachmentSidecarRecord{}
		mapped := AttachmentReasonInventoryForbidden
		if reason == AttachmentInventoryUnsupported {
			mapped = AttachmentReasonInventoryUnsupported
		}
		fixture.PartialReasons = []AttachmentPartialReason{mapped}
		if _, err := EncodeAttachmentSidecarV1(fixture); err != nil {
			t.Fatalf("reason %q: %v", reason, err)
		}
	}

	fixture := confluenceCapturedAttachmentSidecarFixture()
	fixture.BodiesState = AttachmentBodiesPartial
	fixture.Complete = false
	fixture.PartialReasons = []AttachmentPartialReason{AttachmentReasonBodyForbidden}
	fixture.Attachments[0].Body = AttachmentSidecarBody{State: AttachmentBodyForbidden, Reason: AttachmentBodyReasonForbidden}
	encoded, err := EncodeAttachmentSidecarV1(fixture)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAttachmentSidecarV1(encoded)
	if err != nil || decoded.PartialReasons[0] != AttachmentReasonBodyForbidden {
		t.Fatalf("decoded=%+v error=%v", decoded, err)
	}
}

func TestAttachmentSidecarV1TreatsMediaExclusionAsCompletePolicy(t *testing.T) {
	fixture := confluenceCapturedAttachmentSidecarFixture()
	fixture.Attachments[0].Body = AttachmentSidecarBody{State: AttachmentBodyExcluded, Reason: AttachmentBodyReasonMediaExcluded}
	if _, err := EncodeAttachmentSidecarV1(fixture); err != nil {
		t.Fatal(err)
	}
}

func TestAttachmentSidecarV1RejectsSchemaAndJSONDrift(t *testing.T) {
	valid, err := EncodeAttachmentSidecarV1(jiraAttachmentSidecarFixture())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"future":      bytes.Replace(valid, []byte(`"schema_version": 1`), []byte(`"schema_version": 2`), 1),
		"unversioned": bytes.Replace(valid, []byte("  \"schema_version\": 1,\n"), nil, 1),
		"unknown":     bytes.Replace(valid, []byte(`"service":`), []byte(`"extra":true,"service":`), 1),
		"duplicate":   bytes.Replace(valid, []byte(`"service": "jira"`), []byte(`"service":"jira","service":"jira"`), 1),
		"trailing":    append(append([]byte(nil), valid...), []byte(`[]`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if decoded, err := DecodeAttachmentSidecarV1(data); !errors.Is(err, domain.ErrCheckFailed) || decoded.Attachments != nil {
				t.Fatalf("decoded=%+v error=%v", decoded, err)
			}
		})
	}
}

func TestAttachmentSidecarV1RejectsBindingAndBodyDrift(t *testing.T) {
	for name, mutate := range map[string]func(*AttachmentSidecarV1){
		"jira version":     func(value *AttachmentSidecarV1) { value.ParentVersion = 1 },
		"missing origin":   func(value *AttachmentSidecarV1) { value.OriginSHA256 = "" },
		"missing revision": func(value *AttachmentSidecarV1) { value.ParentRevision = "" },
		"uppercase digest": func(value *AttachmentSidecarV1) { value.NativeSHA256 = strings.Repeat("A", 64) },
		"count":            func(value *AttachmentSidecarV1) { value.Count++ },
		"complete partial": func(value *AttachmentSidecarV1) {
			value.PartialReasons = []AttachmentPartialReason{AttachmentReasonBodyFailed}
		},
		"duplicate": func(value *AttachmentSidecarV1) { value.Attachments[1].ID = value.Attachments[0].ID },
		"body in assets": func(value *AttachmentSidecarV1) {
			*value = confluenceCapturedAttachmentSidecarFixture()
			value.Attachments[0].Body.Path = "DOC/page.assets/30001.body"
		},
		"body size": func(value *AttachmentSidecarV1) {
			*value = confluenceCapturedAttachmentSidecarFixture()
			value.Attachments[0].Body.Size++
		},
		"body hash": func(value *AttachmentSidecarV1) {
			*value = confluenceCapturedAttachmentSidecarFixture()
			value.Attachments[0].Body.SHA256 = "bad"
		},
		"body identity": func(value *AttachmentSidecarV1) {
			*value = confluenceCapturedAttachmentSidecarFixture()
			value.Attachments[0].Body.Path = "DOC/page.attachments/other.body"
		},
		"unexplained partial": func(value *AttachmentSidecarV1) {
			*value = confluenceCapturedAttachmentSidecarFixture()
			value.BodiesState = AttachmentBodiesPartial
			value.Complete = false
		},
		"missing aggregate reason": func(value *AttachmentSidecarV1) {
			*value = confluenceCapturedAttachmentSidecarFixture()
			value.BodiesState = AttachmentBodiesPartial
			value.Complete = false
			value.Attachments[0].Body = AttachmentSidecarBody{State: AttachmentBodyFailed, Reason: AttachmentBodyReasonFailed}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := jiraAttachmentSidecarFixture()
			mutate(&value)
			if _, err := EncodeAttachmentSidecarV1(value); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestAttachmentSidecarV1DecoderRejectsCollectionExpansion(t *testing.T) {
	for name, mutate := range map[string]func(*AttachmentSidecarV1){
		"attachments": func(value *AttachmentSidecarV1) {
			value.Count = maxAttachmentSidecarRecords + 1
			value.Attachments = make([]AttachmentSidecarRecord, maxAttachmentSidecarRecords+1)
		},
		"partial reasons": func(value *AttachmentSidecarV1) {
			value.Count = 0
			value.Attachments = []AttachmentSidecarRecord{}
			value.PartialReasons = make([]AttachmentPartialReason, maxAttachmentSidecarRecords+17)
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := jiraAttachmentSidecarFixture()
			mutate(&value)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeAttachmentSidecarV1(data); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("decoder expansion error=%v", err)
			}
		})
	}
}
