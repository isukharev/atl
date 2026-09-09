package brokertransport

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestAttachmentV3TransportPathsAndFailuresAreClosed(t *testing.T) {
	if ExecutionSchemaSHA256V3() != "acb21638b4c48ba715621cbc30ad6aadaff670a9244f373f9bcee477f027a969" ||
		DiscoverySchemaSHA256V4() != "a25bbe1a84de11b598756593faffec1ad3191980343f2036a31b20cff726f760" {
		t.Fatal("attachment transport schema bytes changed")
	}
	paths := []string{ExecutePathV3, AuthorizeAttachmentAdmissionPathV3, AuthorizeAttachmentQualificationPathV3, AuthorizeAttachmentOperationPathV3, DiscoveryNegotiatePathV4, DiscoveryPathV4}
	want := []string{"/v3/execute", "/v3/authorize/attachment/admission", "/v3/authorize/attachment/qualification", "/v3/authorize/attachment/operation", "/v4/discovery/negotiate", "/v4/discovery"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths=%v", paths)
	}
	if ExecutionStreamMediaTypeV3 != "application/x-ndjson" {
		t.Fatal("stream media type changed")
	}
	for _, reason := range []domain.BrokerReason{domain.BrokerReasonMalformed, domain.BrokerReasonDenied, domain.BrokerReasonDecisionExpired, domain.BrokerReasonAuthorizationUnavailable} {
		body, err := EncodeExecutionFailureV3(reason)
		decoded, decodeErr := DecodeExecutionFailureV3(body)
		if err != nil || decodeErr != nil || decoded.SchemaVersion != 3 || decoded.Reason != reason || decoded.RetrySafe || !decoded.Complete {
			t.Fatalf("execution failure=%+v errors=%v/%v", decoded, err, decodeErr)
		}
		discoveryBody, err := EncodeDiscoveryFailureV4(reason)
		discovery, decodeErr := DecodeDiscoveryFailureV4(discoveryBody)
		if err != nil || decodeErr != nil || discovery.SchemaVersion != 4 || discovery.Reason != reason || discovery.RetrySafe || !discovery.Complete {
			t.Fatalf("discovery failure=%+v errors=%v/%v", discovery, err, decodeErr)
		}
	}
	valid, _ := EncodeExecutionFailureV3(domain.BrokerReasonDenied)
	for name, body := range map[string][]byte{
		"duplicate": bytes.Replace(valid, []byte(`"schema_version":3`), []byte(`"schema_version":3,"schema_version":3`), 1),
		"unknown":   bytes.Replace(valid, []byte(`"complete":true`), []byte(`"private":true,"complete":true`), 1),
		"wrong v2":  bytes.Replace(valid, []byte(`"schema_version":3`), []byte(`"schema_version":2`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeExecutionFailureV3(body); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v body=%s", err, body)
			}
		})
	}
}

func TestAttachmentDiscoveryNegotiationV4BindsExactContextAndFiveSecondLease(t *testing.T) {
	now := time.UnixMilli(2_000)
	context := attachmentTransportContext()
	hello := DiscoveryNegotiationV4{SchemaVersion: 4, RequestID: "request-1", ContractFamily: domain.BrokerContractFamilyExecutionV3, Service: "jira", BrokerID: context.BrokerID, Audience: context.Audience, ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, AuthorityRevision: context.AuthorityRevision, NotAfterMillis: 20_000}
	body, err := EncodeDiscoveryNegotiationV4(hello)
	decoded, decodeErr := DecodeDiscoveryNegotiationV4(body)
	request, bindErr := BindDiscoveryNegotiationV4(decoded, context, now)
	if err != nil || decodeErr != nil || bindErr != nil || !reflect.DeepEqual(decoded, hello) || request.NotAfterMillis != now.Add(5*time.Second).UnixMilli() {
		t.Fatalf("hello=%+v request=%+v errors=%v/%v/%v", decoded, request, err, decodeErr, bindErr)
	}
	negotiated, err := EncodeNegotiatedDiscoveryV4(request)
	decodedRequest, decodeErr := DecodeNegotiatedDiscoveryV4(negotiated, hello, now)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decodedRequest, request) {
		t.Fatalf("request=%+v errors=%v/%v", decodedRequest, err, decodeErr)
	}
	changed := hello
	changed.AuthorityRevision = "other-revision"
	if _, err := BindDiscoveryNegotiationV4(changed, context, now); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("changed authority revision err=%v", err)
	}
	changed = hello
	changed.ContractFamily = domain.BrokerContractFamilyExecutionV2
	if _, err := EncodeDiscoveryNegotiationV4(changed); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("wrong family err=%v", err)
	}
	for name, malformed := range map[string][]byte{
		"null":      bytes.Replace(body, []byte(`"request_id":"request-1"`), []byte(`"request_id":null`), 1),
		"duplicate": bytes.Replace(body, []byte(`"schema_version":4`), []byte(`"schema_version":4,"schema_version":4`), 1),
		"unknown":   bytes.Replace(body, []byte(`"not_after_millis":20000`), []byte(`"private":true,"not_after_millis":20000`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeDiscoveryNegotiationV4(malformed); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPublishedAttachmentTransportSchemasMatchAndValidateVectors(t *testing.T) {
	for _, pair := range []struct {
		path string
		body []byte
	}{{"../../docs/schemas/broker-execution-http-v3.schema.json", ExecutionSchemaV3()}, {"../../docs/schemas/broker-discovery-http-v4.schema.json", DiscoverySchemaV4()}} {
		published, err := os.ReadFile(pair.path)
		var strict map[string]any
		if err != nil || !bytes.Equal(published, pair.body) || !decodeExact(pair.body, 1<<20, &strict) {
			t.Fatalf("published schema mismatch path=%s err=%v", pair.path, err)
		}
	}
	var executionHTTP, discoveryHTTP, semanticExecution, semanticDiscovery, legacy jsonschema.Schema
	for body, target := range map[string]*jsonschema.Schema{
		string(ExecutionSchemaV3()): &executionHTTP, string(DiscoverySchemaV4()): &discoveryHTTP,
		string(brokercontract.ExecutionSchemaV3()): &semanticExecution, string(brokercontract.DiscoverySchemaV4()): &semanticDiscovery,
		string(brokercontract.SchemaV1()): &legacy,
	} {
		if err := json.Unmarshal([]byte(body), target); err != nil {
			t.Fatal(err)
		}
	}
	loader := func(uri *url.URL) (*jsonschema.Schema, error) {
		switch {
		case strings.HasSuffix(uri.Path, "/broker-execution-v3.schema.json"):
			return &semanticExecution, nil
		case strings.HasSuffix(uri.Path, "/broker-discovery-v4.schema.json"):
			return &semanticDiscovery, nil
		case strings.HasSuffix(uri.Path, "/broker-v1.schema.json"):
			return &legacy, nil
		default:
			return nil, errors.New("unknown schema")
		}
	}
	resolvedExecution, err := executionHTTP.Resolve(&jsonschema.ResolveOptions{Loader: loader})
	if err != nil {
		t.Fatal(err)
	}
	resolvedDiscovery, err := discoveryHTTP.Resolve(&jsonschema.ResolveOptions{Loader: loader})
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(2_000)
	context := attachmentTransportContext()
	hello := DiscoveryNegotiationV4{SchemaVersion: 4, RequestID: "request-1", ContractFamily: domain.BrokerContractFamilyExecutionV3, Service: "jira", BrokerID: context.BrokerID, Audience: context.Audience, ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, AuthorityRevision: context.AuthorityRevision, NotAfterMillis: 7_000}
	request, _ := BindDiscoveryNegotiationV4(hello, context, now)
	semanticRequest := domain.BrokerAttachmentRequestV3{SchemaVersion: 3, Operation: domain.BrokerOperationJiraAttachmentDownload, OperationVersion: 1, RequestID: "request-1", Features: []string{"atomic_local_publish_v1", "attachment_id_v1", "step_snapshot_v1"}, Expect: domain.BrokerRequestExpectations{ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, AuthorityRevision: context.AuthorityRevision}, Arguments: domain.BrokerAttachmentArgumentsV3{IssueKey: "PROJ-1", AttachmentID: "200"}}
	for index, vector := range [][]byte{mustTransport(EncodeExecutionFailureV3(domain.BrokerReasonDenied)), mustTransport(brokercontract.EncodeAttachmentRequestV3(semanticRequest)), mustTransport(EncodeDiscoveryNegotiationV4(hello)), mustTransport(EncodeNegotiatedDiscoveryV4(request)), mustTransport(EncodeDiscoveryFailureV4(domain.BrokerReasonDenied))} {
		var value any
		if json.Unmarshal(vector, &value) != nil {
			t.Fatalf("vector %d is not JSON", index)
		}
		resolved := resolvedDiscovery
		if index <= 1 {
			resolved = resolvedExecution
		}
		if err := resolved.Validate(value); err != nil {
			t.Fatalf("schema rejected vector %d: %v", index, err)
		}
	}
}

func attachmentTransportContext() domain.BrokerVerifiedContext {
	return domain.BrokerVerifiedContext{PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", ExecutionNotBeforeMillis: 1_000, ExecutionExpiresMillis: 63_000, GrantExpiresMillis: 63_000, CredentialExpiresMillis: 63_000, Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("a", 64), WorkloadBackendID: "jira-primary"}}
}

func FuzzDecodeAttachmentTransportV3V4(f *testing.F) {
	f.Add([]byte(`{"schema_version":3,"status":"rejected"}`))
	context := attachmentTransportContext()
	hello := DiscoveryNegotiationV4{SchemaVersion: 4, RequestID: "request-1", ContractFamily: domain.BrokerContractFamilyExecutionV3, Service: "jira", BrokerID: context.BrokerID, Audience: context.Audience, ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, AuthorityRevision: context.AuthorityRevision, NotAfterMillis: 7_000}
	f.Add(mustTransport(EncodeExecutionFailureV3(domain.BrokerReasonDenied)))
	f.Add(mustTransport(EncodeDiscoveryNegotiationV4(hello)))
	f.Add(mustTransport(EncodeDiscoveryFailureV4(domain.BrokerReasonDenied)))
	f.Fuzz(func(_ *testing.T, body []byte) {
		_, _ = DecodeExecutionFailureV3(body)
		_, _ = DecodeDiscoveryNegotiationV4(body)
		_, _ = DecodeDiscoveryFailureV4(body)
	})
}
