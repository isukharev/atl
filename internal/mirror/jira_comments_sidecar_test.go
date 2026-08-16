package mirror

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func jiraCommentsSidecarFixture() JiraCommentsSidecarV1 {
	return JiraCommentsSidecarV1{
		SchemaVersion: JiraCommentsSidecarSchemaV1, Service: CorpusSnapshotJira, ParentID: "10001",
		OriginSHA256: "sha256:" + strings.Repeat("0", 64), ParentRevision: "2026-01-01T00:00:00.000+0000",
		NativeSHA256: strings.Repeat("a", 64), MetadataSHA256: strings.Repeat("b", 64),
		Complete: true, Count: 2, Total: 2, TotalKnown: true, PageCount: 2,
		Comments: []JiraCommentsSidecarComment{
			{ID: "2", Body: "second"},
			{ID: "1", AuthorKey: "stable", AuthorName: "user", AuthorDisplayName: "Fixture", CreatedAt: "2026-01-01", UpdatedAt: "2026-01-02", ParentID: "3", Body: "first"},
		},
	}
}

func TestJiraCommentsSidecarV1DeterministicRoundTrip(t *testing.T) {
	fixture := jiraCommentsSidecarFixture()
	encoded, err := EncodeJiraCommentsSidecarV1(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.Comments[0].ID != "2" || !bytes.HasSuffix(encoded, []byte("\n")) {
		t.Fatal("encoder mutated input or omitted newline")
	}
	decoded, err := DecodeJiraCommentsSidecarV1(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Comments[0].ID != "1" || decoded.Comments[0].AuthorKey != "stable" || decoded.ParentID != "10001" {
		t.Fatalf("decoded=%+v", decoded)
	}
	reencoded, err := EncodeJiraCommentsSidecarV1(decoded)
	if err != nil || !bytes.Equal(encoded, reencoded) {
		t.Fatalf("reencoded=%s error=%v", reencoded, err)
	}
}

func TestJiraCommentsSidecarV1PreservesQualifiedPartial(t *testing.T) {
	fixture := jiraCommentsSidecarFixture()
	fixture.Complete = false
	fixture.PartialReason = domain.JiraCommentPartialPageLimit
	fixture.Total = 3
	encoded, err := EncodeJiraCommentsSidecarV1(fixture)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeJiraCommentsSidecarV1(encoded)
	if err != nil || decoded.Complete || decoded.PartialReason != domain.JiraCommentPartialPageLimit || decoded.Total != 3 {
		t.Fatalf("decoded=%+v error=%v", decoded, err)
	}
}

func TestJiraCommentsSidecarV1PreservesUnavailableInventory(t *testing.T) {
	for _, reason := range []string{JiraCommentsPartialForbidden, JiraCommentsPartialUnsupported} {
		fixture := jiraCommentsSidecarFixture()
		fixture.Complete = false
		fixture.PartialReason = reason
		fixture.Count = 0
		fixture.Total = 0
		fixture.TotalKnown = false
		fixture.PageCount = 0
		fixture.Comments = []JiraCommentsSidecarComment{}
		encoded, err := EncodeJiraCommentsSidecarV1(fixture)
		if err != nil {
			t.Fatalf("reason %q: %v", reason, err)
		}
		decoded, err := DecodeJiraCommentsSidecarV1(encoded)
		if err != nil || decoded.Complete || decoded.PartialReason != reason || decoded.Comments == nil {
			t.Fatalf("reason %q: decoded=%+v error=%v", reason, decoded, err)
		}
	}
}

func TestJiraCommentsSidecarV1RejectsSchemaAndJSONDrift(t *testing.T) {
	valid, err := EncodeJiraCommentsSidecarV1(jiraCommentsSidecarFixture())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"future":      bytes.Replace(valid, []byte(`"schema_version": 1`), []byte(`"schema_version": 2`), 1),
		"unversioned": bytes.Replace(valid, []byte("  \"schema_version\": 1,\n"), nil, 1),
		"unknown":     bytes.Replace(valid, []byte(`"service":`), []byte(`"extra":true,"service":`), 1),
		"duplicate":   bytes.Replace(valid, []byte(`"service": "jira"`), []byte(`"service":"jira","service":"jira"`), 1),
		"trailing":    append(append([]byte(nil), valid...), []byte(`{}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if decoded, err := DecodeJiraCommentsSidecarV1(data); !errors.Is(err, domain.ErrCheckFailed) || decoded.Comments != nil {
				t.Fatalf("decoded=%+v error=%v", decoded, err)
			}
		})
	}
}

func TestJiraCommentsSidecarV1RejectsBindingAndInventoryDrift(t *testing.T) {
	for name, mutate := range map[string]func(*JiraCommentsSidecarV1){
		"key parent":       func(value *JiraCommentsSidecarV1) { value.ParentID = "PROJ-1" },
		"missing origin":   func(value *JiraCommentsSidecarV1) { value.OriginSHA256 = "" },
		"missing revision": func(value *JiraCommentsSidecarV1) { value.ParentRevision = "" },
		"uppercase digest": func(value *JiraCommentsSidecarV1) { value.NativeSHA256 = strings.Repeat("A", 64) },
		"count":            func(value *JiraCommentsSidecarV1) { value.Count++ },
		"complete reason":  func(value *JiraCommentsSidecarV1) { value.PartialReason = domain.JiraCommentPartialPageLimit },
		"duplicate":        func(value *JiraCommentsSidecarV1) { value.Comments[1].ID = value.Comments[0].ID },
	} {
		t.Run(name, func(t *testing.T) {
			value := jiraCommentsSidecarFixture()
			mutate(&value)
			if _, err := EncodeJiraCommentsSidecarV1(value); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestJiraCommentsSidecarV1RejectsCollectionExpansionBeforeCopy(t *testing.T) {
	value := jiraCommentsSidecarFixture()
	value.Comments = make([]JiraCommentsSidecarComment, domain.JiraCommentReadMaxItems+1)
	value.Count = len(value.Comments)
	value.Total = len(value.Comments)
	for index := range value.Comments {
		value.Comments[index] = JiraCommentsSidecarComment{ID: fmt.Sprintf("%d", index+1), Body: "x"}
	}
	if _, err := canonicalJiraCommentsSidecar(value); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error=%v", err)
	}
}
