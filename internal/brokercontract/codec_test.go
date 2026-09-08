package brokercontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func fixtureRequest() domain.BrokerRequest {
	return domain.BrokerRequest{
		SchemaVersion: 1, Operation: domain.BrokerOperationJiraIssueRead, OperationVersion: 1, RequestID: "request-1",
		Features: []string{},
		Expect:   domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"},
		Arguments: domain.BrokerOperationArguments{JiraIssueRead: &domain.BrokerJiraIssueReadArguments{
			IssueKey: "EXAMPLE-1", Fields: []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldDescription, domain.BrokerJiraIssueFieldSummary},
		}},
	}
}

func fixtureContext() domain.BrokerVerifiedContext {
	return domain.BrokerVerifiedContext{
		PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1",
		Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1",
		ExecutionNotBeforeMillis: 1000, ExecutionExpiresMillis: 61000, GrantExpiresMillis: 31000, CredentialExpiresMillis: 41000,
		Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: digestChar('a'), WorkloadBackendID: "jira-primary"},
	}
}

func digestChar(value byte) string { return strings.Repeat(string(value), 64) }

func TestRequestAndContextFixturesRoundTripExactly(t *testing.T) {
	requestFixture, err := os.ReadFile("testdata/v1/request-jira-read.json")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeRequestV1(fixtureRequest())
	if err != nil || !bytes.Equal(encoded, bytes.TrimSpace(requestFixture)) {
		t.Fatalf("request=%s err=%v fixture=%s", encoded, err, requestFixture)
	}
	decoded, err := DecodeRequestV1(requestFixture)
	if err != nil || !reflect.DeepEqual(decoded, fixtureRequest()) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	contextFixture, err := os.ReadFile("testdata/v1/verified-context.json")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = EncodeVerifiedContextV1(fixtureContext())
	if err != nil || !bytes.Equal(encoded, bytes.TrimSpace(contextFixture)) {
		t.Fatalf("context=%s err=%v fixture=%s", encoded, err, contextFixture)
	}
	decodedContext, err := DecodeVerifiedContextV1(contextFixture)
	if err != nil || decodedContext != fixtureContext() {
		t.Fatalf("context=%+v err=%v", decodedContext, err)
	}
}

func TestEveryOperationRequestRoundTrips(t *testing.T) {
	requests := []domain.BrokerRequest{
		fixtureRequest(),
		requestFor(domain.BrokerOperationConfluencePageRead, domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: "42", Projection: domain.BrokerConfluenceProjectionStorage}}),
		requestFor(domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "EXAMPLE-1", NativeBody: []byte("native *wiki*"), SatisfactionPolicy: "append_always"}}),
		requestFor(domain.BrokerOperationJiraCommentApply, domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "EXAMPLE-1", NativeBody: []byte("native *wiki*"), SatisfactionPolicy: "append_always", ExpectedProposalHash: digestChar('b'), OperationTicket: "ticket-1"}}),
		requestFor(domain.BrokerOperationOutcomeLookup, domain.BrokerOperationArguments{Outcome: &domain.BrokerOutcomeArguments{OperationTicket: "ticket-1"}}),
	}
	for _, request := range requests {
		t.Run(string(request.Operation), func(t *testing.T) {
			encoded, err := EncodeRequestV1(request)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeRequestV1(encoded)
			if err != nil || !reflect.DeepEqual(decoded, cloneRequest(request)) {
				t.Fatalf("decoded=%+v err=%v wire=%s", decoded, err, encoded)
			}
		})
	}
}

func requestFor(operation domain.BrokerOperationID, arguments domain.BrokerOperationArguments) domain.BrokerRequest {
	request := fixtureRequest()
	request.Operation, request.Arguments = operation, arguments
	definition, _ := Definition(operation, OperationVersion)
	request.Features = wireStrings(definition.RequiredFeatures)
	return request
}

func TestRequestStrictDecoderFailsClosedWithoutEcho(t *testing.T) {
	valid, _ := EncodeRequestV1(fixtureRequest())
	private := "PRIVATE-CANARY.example.invalid/secret"
	tests := [][]byte{
		bytes.Replace(valid, []byte(`"request_id":"request-1"`), []byte(`"request_id":"request-1","request_id":"`+private+`"`), 1),
		bytes.Replace(valid, []byte(`"request_id":"request-1"`), []byte(`"request_id":"request-1","role":"admin"`), 1),
		bytes.Replace(valid, []byte(`"issue_key":"EXAMPLE-1"`), []byte(`"issue_key":"EXAMPLE-1","principal":"`+private+`"`), 1),
		bytes.Replace(valid, []byte(`"fields":["description","summary"]`), []byte(`"fields":["summary","summary"]`), 1),
		bytes.Replace(valid, []byte(`"issue_key":"EXAMPLE-1"`), []byte(`"issue_key":42`), 1),
		bytes.Replace(valid, []byte(`"issue_key":"EXAMPLE-1"`), []byte(`"issue_key":"EXAMPLE-1\ud800"`), 1),
		append(append([]byte(nil), valid...), []byte(`{}`)...),
		bytes.Replace(valid, []byte(`"operation_version":1`), []byte(`"operation_version":2`), 1),
		bytes.Replace(valid, []byte(`"operation":"jira.issue.read"`), []byte(`"operation":"raw.http"`), 1),
		bytes.Replace(valid, []byte(`"operation":"jira.issue.read"`), []byte(`"Operation":"jira.issue.read"`), 1),
		bytes.Replace(valid, []byte(`"request_id":"request-1"`), []byte(`"request_id":null`), 1),
	}
	for index, input := range tests {
		_, err := DecodeRequestV1(input)
		if err == nil || strings.Contains(err.Error(), private) {
			t.Fatalf("case %d error=%q", index, err)
		}
		if !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("case %d identity=%v", index, err)
		}
	}
	oversized := bytes.Repeat([]byte{' '}, int(MaxEnvelopeBytes)+1)
	if _, err := DecodeRequestV1(oversized); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized error=%v", err)
	}
}

func TestNegativeRequestVectorsStayRejected(t *testing.T) {
	paths, err := filepath.Glob("testdata/v1/negative/*.json")
	if err != nil || len(paths) != 2 {
		t.Fatalf("negative vectors=%v err=%v", paths, err)
	}
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, decodeErr := DecodeRequestV1(data); !errors.Is(decodeErr, domain.ErrUsage) {
			t.Errorf("%s error=%v", path, decodeErr)
		}
	}
}

func TestArgumentsDigestIsSemanticAndSourceExact(t *testing.T) {
	first := fixtureRequest()
	second := fixtureRequest()
	second.Arguments.JiraIssueRead.Fields = []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary, domain.BrokerJiraIssueFieldDescription}
	a, errA := ArgumentsSHA256(first)
	b, errB := ArgumentsSHA256(second)
	if errA != nil || errB != nil || a != b || !validDigest(a) {
		t.Fatalf("field set digests=%q/%q errors=%v/%v", a, b, errA, errB)
	}
	commentA := requestFor(domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "EXAMPLE-1", NativeBody: []byte("a\n"), SatisfactionPolicy: "append_always"}})
	commentB := requestFor(domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "EXAMPLE-1", NativeBody: []byte("a"), SatisfactionPolicy: "append_always"}})
	digestA, _ := ArgumentsSHA256(commentA)
	digestB, _ := ArgumentsSHA256(commentB)
	if digestA == digestB {
		t.Fatal("native body byte change did not change arguments digest")
	}
	encoded, _ := EncodeRequestV1(first)
	var wire map[string]any
	if json.Unmarshal(encoded, &wire) != nil || wire["principal"] != nil || wire["role"] != nil || wire["backend"] != nil {
		t.Fatalf("untrusted request acquired authority fields: %s", encoded)
	}
}

func FuzzDecodeRequestV1(f *testing.F) {
	valid, _ := EncodeRequestV1(fixtureRequest())
	f.Add(valid)
	f.Add([]byte(`{"schema_version":1,"operation":"raw.http"}`))
	f.Add([]byte(`{"schema_version":1,"schema_version":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := DecodeRequestV1(data)
		if err == nil {
			encoded, encodeErr := EncodeRequestV1(value)
			if encodeErr != nil || len(encoded) == 0 || int64(len(encoded)) > MaxEnvelopeBytes {
				t.Fatalf("accepted request did not re-encode: %v", encodeErr)
			}
		}
	})
}
