package brokercontract

import (
	"bytes"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestJiraIssueReadResultV1RoundTripsAsClosedCompleteProjection(t *testing.T) {
	request := fixtureRequest()
	argumentsDigest, _ := ArgumentsSHA256(request)
	value := domain.BrokerJiraIssueReadResult{
		SchemaVersion: 1, ArgumentsSHA256: argumentsDigest, IssueID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", Updated: "2026-09-08T10:00:00.000+0000", Complete: true,
		Fields: []domain.BrokerJiraIssueReadField{
			{Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: "A summary"},
			{Field: domain.BrokerJiraIssueFieldDescription, Present: true, Null: true},
		},
	}
	encoded, err := EncodeJiraIssueReadResultV1(value)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Index(encoded, []byte(`"field":"description"`)) > bytes.Index(encoded, []byte(`"field":"summary"`)) {
		t.Fatalf("field set was not canonicalized: %s", encoded)
	}
	decoded, err := DecodeJiraIssueReadResultV1(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want := value
	want.Fields = sortedJiraReadResultFields(want.Fields)
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded=%+v want=%+v", decoded, want)
	}
	if err := ValidateJiraIssueReadResultForV1(decoded, request); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Arguments.JiraIssueRead = &domain.BrokerJiraIssueReadArguments{IssueKey: "EXAMPLE-2", Fields: request.Arguments.JiraIssueRead.Fields}
	if err := ValidateJiraIssueReadResultForV1(decoded, changed); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("cross-request result error=%v", err)
	}
}

func TestConfluencePageReadResultV1PreservesExactStorageBytes(t *testing.T) {
	request := requestFor(domain.BrokerOperationConfluencePageRead, domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: "42", Projection: domain.BrokerConfluenceProjectionStorage}})
	argumentsDigest, _ := ArgumentsSHA256(request)
	storage := []byte(`<p>first</p>` + "\n" + `<ac:structured-macro ac:name="status"/>`)
	value := domain.BrokerConfluencePageReadResult{
		SchemaVersion: 1, ArgumentsSHA256: argumentsDigest, PageID: "42", Type: "page", Space: "DOCS", Version: 7, Title: "Example", Updated: "2026-09-08T10:00:00.000Z",
		Projection: domain.BrokerConfluenceProjectionStorage, Storage: storage, StoragePresent: true, Complete: true,
	}
	encoded, err := EncodeConfluencePageReadResultV1(value)
	if err != nil || !bytes.Contains(encoded, []byte(base64.StdEncoding.EncodeToString(storage))) {
		t.Fatalf("encoded=%s err=%v", encoded, err)
	}
	decoded, err := DecodeConfluencePageReadResultV1(encoded)
	if err != nil || !reflect.DeepEqual(decoded, value) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if err := ValidateConfluencePageReadResultForV1(decoded, request); err != nil {
		t.Fatal(err)
	}

	metadata := value
	metadataRequest := requestFor(domain.BrokerOperationConfluencePageRead, domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: "42", Projection: domain.BrokerConfluenceProjectionMetadata}})
	metadata.ArgumentsSHA256, _ = ArgumentsSHA256(metadataRequest)
	metadata.Projection = domain.BrokerConfluenceProjectionMetadata
	metadata.Storage = nil
	metadata.StoragePresent = false
	encoded, err = EncodeConfluencePageReadResultV1(metadata)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = DecodeConfluencePageReadResultV1(encoded)
	if err != nil || !reflect.DeepEqual(decoded, metadata) {
		t.Fatalf("metadata=%+v err=%v", decoded, err)
	}
	if err := ValidateConfluencePageReadResultForV1(decoded, metadataRequest); err != nil {
		t.Fatal(err)
	}
}

func TestReadResultV1FailsClosedWithoutEcho(t *testing.T) {
	private := "PRIVATE-CANARY.example.invalid/secret"
	inputs := [][]byte{
		[]byte(`{"schema_version":1,"arguments_sha256":"` + digestChar('a') + `","issue_id":"10001","key":"EXAMPLE-1","project":"EXAMPLE","updated":"now","fields":[{"field":"summary","present":true,"null":false,"value":"ok"}],"complete":true,"private":"` + private + `"}`),
		[]byte(`{"schema_version":1,"arguments_sha256":"` + digestChar('a') + `","issue_id":"10001","key":"EXAMPLE-1","project":"EXAMPLE","updated":"now","fields":[{"field":"summary","present":true,"null":false,"value":"ok"},{"field":"description","present":true,"null":false,"value":"later"}],"complete":true}`),
		[]byte(`{"schema_version":1,"arguments_sha256":"` + digestChar('a') + `","issue_id":"10001","key":"EXAMPLE-1","project":"EXAMPLE","updated":"now","fields":[{"field":"description","present":true,"null":true,"value":"must-be-empty"}],"complete":true}`),
	}
	for index, input := range inputs {
		_, err := DecodeJiraIssueReadResultV1(input)
		if !errors.Is(err, domain.ErrUsage) || strings.Contains(err.Error(), private) {
			t.Fatalf("case %d error=%q", index, err)
		}
	}
	invalidStorage := base64.StdEncoding.EncodeToString([]byte{0xff})
	input := []byte(`{"schema_version":1,"arguments_sha256":"` + digestChar('a') + `","page_id":"42","type":"page","space":"DOCS","version":1,"title":"Example","updated":"now","projection":"storage","storage_base64":"` + invalidStorage + `","storage_present":true,"complete":true}`)
	if _, err := DecodeConfluencePageReadResultV1(input); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("invalid storage error=%v", err)
	}
	hiddenStorage := []byte(`{"schema_version":1,"arguments_sha256":"` + digestChar('a') + `","page_id":"42","type":"page","space":"DOCS","version":1,"title":"Example","updated":"now","projection":"metadata","storage_base64":"c2VjcmV0","storage_present":false,"complete":true}`)
	if _, err := DecodeConfluencePageReadResultV1(hiddenStorage); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("hidden storage error=%v", err)
	}
}

func TestGatedJiraCommentResultsAreClosedAndRequestBound(t *testing.T) {
	previewRequest := requestFor(domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always"}})
	digest, _ := ArgumentsSHA256(previewRequest)
	nativeDigest, _ := NativeCandidateSHA256(previewRequest.Operation, previewRequest.Arguments.JiraComment.NativeBody)
	preview := domain.BrokerJiraCommentResult{SchemaVersion: 1, ArgumentsSHA256: digest, OperationTicket: "ticket-1", Mode: "preview", Status: "proposed", ProposalHash: digestChar('b'), NativeCandidateSHA256: nativeDigest, VersionEvidenceSHA256: digestChar('d'), Complete: true}
	wire, err := EncodeJiraCommentResultV1(preview)
	decoded, decodeErr := DecodeJiraCommentResultV1(wire)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, preview) || ValidateJiraCommentResultForV1(decoded, previewRequest) != nil {
		t.Fatalf("preview=%+v errors=%v/%v", decoded, err, decodeErr)
	}
	applyRequest := requestFor(domain.BrokerOperationJiraCommentApply, domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always", ExpectedProposalHash: preview.ProposalHash, OperationTicket: preview.OperationTicket}})
	applyDigest, _ := ArgumentsSHA256(applyRequest)
	applyNativeDigest, _ := NativeCandidateSHA256(applyRequest.Operation, applyRequest.Arguments.JiraComment.NativeBody)
	apply := preview
	apply.ArgumentsSHA256, apply.NativeCandidateSHA256, apply.Mode, apply.Status, apply.CommentID, apply.WriteAttempted, apply.Reconciled = applyDigest, applyNativeDigest, "apply", "applied", "9001", true, true
	wire, err = EncodeJiraCommentResultV1(apply)
	decoded, decodeErr = DecodeJiraCommentResultV1(wire)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, apply) || ValidateJiraCommentResultForV1(decoded, applyRequest) != nil {
		t.Fatalf("apply=%+v errors=%v/%v", decoded, err, decodeErr)
	}
	for _, outcome := range []domain.BrokerJiraCommentResult{
		func() domain.BrokerJiraCommentResult { value := apply; value.Status = "recovered"; return value }(),
		func() domain.BrokerJiraCommentResult {
			value := apply
			value.Status, value.CommentID, value.Reconciled = "not_applied", "", false
			return value
		}(),
		func() domain.BrokerJiraCommentResult {
			value := apply
			value.Status, value.CommentID, value.Complete, value.Reconciled = "outcome_unknown", "", false, false
			return value
		}(),
		func() domain.BrokerJiraCommentResult {
			value := apply
			value.Status, value.CommentID, value.Complete, value.Reconciled = "outcome_unknown", "", false, true
			return value
		}(),
	} {
		wire, err := EncodeJiraCommentResultV1(outcome)
		decoded, decodeErr := DecodeJiraCommentResultV1(wire)
		if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, outcome) || ValidateJiraCommentResultForV1(decoded, applyRequest) != nil {
			t.Fatalf("closeout=%+v errors=%v/%v", decoded, err, decodeErr)
		}
	}
}

func FuzzDecodeReadResultsV1(f *testing.F) {
	jira, _ := EncodeJiraIssueReadResultV1(domain.BrokerJiraIssueReadResult{
		SchemaVersion: 1, ArgumentsSHA256: digestChar('a'), IssueID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", Updated: "now", Complete: true,
		Fields: []domain.BrokerJiraIssueReadField{{Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: "summary"}},
	})
	confluence, _ := EncodeConfluencePageReadResultV1(domain.BrokerConfluencePageReadResult{
		SchemaVersion: 1, ArgumentsSHA256: digestChar('a'), PageID: "42", Type: "page", Space: "DOCS", Version: 1, Title: "Example", Updated: "now",
		Projection: domain.BrokerConfluenceProjectionMetadata, Complete: true,
	})
	f.Add(jira)
	f.Add(confluence)
	f.Fuzz(func(t *testing.T, data []byte) {
		if value, err := DecodeJiraIssueReadResultV1(data); err == nil {
			if encoded, encodeErr := EncodeJiraIssueReadResultV1(value); encodeErr != nil || int64(len(encoded)) > MaxReadResultWireBytes {
				t.Fatalf("accepted Jira result did not re-encode: %v", encodeErr)
			}
		}
		if value, err := DecodeConfluencePageReadResultV1(data); err == nil {
			if encoded, encodeErr := EncodeConfluencePageReadResultV1(value); encodeErr != nil || int64(len(encoded)) > MaxReadResultWireBytes {
				t.Fatalf("accepted Confluence result did not re-encode: %v", encodeErr)
			}
		}
	})
}

func TestReadResultEscapingBudgetIsComputedBeforeEncoding(t *testing.T) {
	value := strings.Repeat(`\`, 1<<20) + strings.Repeat("\u2028", 100)
	encodedBytes, ok := escapedJSONTextBytes(value)
	want := int64((2 << 20) + 600)
	if !ok || encodedBytes != want {
		t.Fatalf("encoded bytes=%d want=%d valid=%t", encodedBytes, want, ok)
	}
	if _, ok := escapedJSONTextBytes("hidden\x00control"); ok {
		t.Fatal("JSON-expanding control byte accepted")
	}
}
