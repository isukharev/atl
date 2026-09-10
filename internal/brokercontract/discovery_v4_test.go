package brokercontract

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

	"github.com/isukharev/atl/internal/domain"
)

func TestAttachmentFamilyDiscoveryV4RequiresTheExactAvailableRow(t *testing.T) {
	request, authorization, projection := attachmentDiscoveryV4Fixture(t)
	requestWire := mustAttachmentEncode(t, request, EncodeFamilyDiscoveryRequestV4)
	decodedRequest, requestErr := DecodeFamilyDiscoveryRequestV4(requestWire)
	authorizationWire := mustAttachmentEncode(t, authorization, EncodeFamilyDiscoveryAuthorizationRequestV4)
	decodedAuthorization, authorizationErr := DecodeFamilyDiscoveryAuthorizationRequestV4(authorizationWire)
	projectionWire := mustAttachmentEncode(t, projection, EncodeFamilyDiscoveryProjectionV4)
	decodedProjection, projectionErr := DecodeFamilyDiscoveryProjectionV4(projectionWire)
	if requestErr != nil || authorizationErr != nil || projectionErr != nil || !reflect.DeepEqual(request, decodedRequest) || !reflect.DeepEqual(authorization, decodedAuthorization) || !reflect.DeepEqual(projection, decodedProjection) {
		t.Fatalf("round trip errors=%v/%v/%v", requestErr, authorizationErr, projectionErr)
	}
	if len(projection.Operations) != 1 || len(RegistryV3()) != 1 || len(AvailableDefinitionsV3()) != 1 {
		t.Fatal("available operation missing from discovery")
	}
	now := time.UnixMilli(3_000)
	if err := ValidateFamilyDiscoveryProjectionV4ForRequest(projection, request, now); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFamilyDiscoveryProjectionV4ForContext(projection, authorization, now); err != nil {
		t.Fatal(err)
	}
	changed := projection
	changed.Operations = []domain.BrokerFamilyDiscoveryOperationV4{}
	if _, err := EncodeFamilyDiscoveryProjectionV4(changed); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("complete discovery omitted the available row: %v", err)
	}
	changed = projection
	changed.ExpiresAtMillis = request.NotAfterMillis + 1
	if err := ValidateFamilyDiscoveryProjectionV4ForRequest(changed, request, now); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("extended discovery lease err=%v", err)
	}
}

func TestAttachmentFamilyDiscoveryV4RejectsNullDuplicateUnknownAndWrongFamily(t *testing.T) {
	request, _, projection := attachmentDiscoveryV4Fixture(t)
	requestWire := mustAttachmentEncode(t, request, EncodeFamilyDiscoveryRequestV4)
	projectionWire := mustAttachmentEncode(t, projection, EncodeFamilyDiscoveryProjectionV4)
	for name, vector := range map[string][]byte{
		"null expectation": bytes.Replace(requestWire, []byte(`"expect":{"execution_id":"execution-1","execution_epoch":"epoch-1","authority_revision":"revision-1"}`), []byte(`"expect":null`), 1),
		"duplicate":        bytes.Replace(requestWire, []byte(`"schema_version":4`), []byte(`"schema_version":4,"schema_version":4`), 1),
		"wrong family":     bytes.Replace(requestWire, []byte(domain.BrokerContractFamilyExecutionV3), []byte(domain.BrokerContractFamilyExecutionV2), 1),
		"unknown":          bytes.Replace(projectionWire, []byte(`"complete":true`), []byte(`"private":true,"complete":true`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			if name == "unknown" {
				_, err = DecodeFamilyDiscoveryProjectionV4(vector)
			} else {
				_, err = DecodeFamilyDiscoveryRequestV4(vector)
			}
			if !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v body=%s", err, vector)
			}
		})
	}
}

func TestPublishedAttachmentDiscoveryV4SchemaMatchesAndValidatesDeclaredProjection(t *testing.T) {
	published, err := os.ReadFile("../../docs/schemas/broker-discovery-v4.schema.json")
	if err != nil || !bytes.Equal(published, DiscoverySchemaV4()) {
		t.Fatalf("published schema mismatch err=%v", err)
	}
	var schema, legacy jsonschema.Schema
	if json.Unmarshal(DiscoverySchemaV4(), &schema) != nil || json.Unmarshal(SchemaV1(), &legacy) != nil {
		t.Fatal("schema JSON is invalid")
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		if strings.HasSuffix(uri.Path, "/broker-v1.schema.json") {
			return &legacy, nil
		}
		return nil, errors.New("unknown schema")
	}})
	if err != nil {
		t.Fatal(err)
	}
	request, authorization, projection := attachmentDiscoveryV4Fixture(t)
	for index, vector := range [][]byte{mustAttachmentEncode(t, request, EncodeFamilyDiscoveryRequestV4), mustAttachmentEncode(t, authorization, EncodeFamilyDiscoveryAuthorizationRequestV4), mustAttachmentEncode(t, projection, EncodeFamilyDiscoveryProjectionV4)} {
		var value any
		if json.Unmarshal(vector, &value) != nil || resolved.Validate(value) != nil {
			t.Fatalf("schema rejected vector %d: %s", index, vector)
		}
	}
	emptyShape := familyDiscoveryProjectionToWireV4(projection)
	emptyShape.Operations = []familyDiscoveryOperationWireV4{}
	vector, _ := json.Marshal(emptyShape)
	var value any
	if json.Unmarshal(vector, &value) != nil || resolved.Validate(value) != nil {
		t.Fatal("structural schema unexpectedly imposed current registry membership")
	}
}

func attachmentDiscoveryV4Fixture(t attachmentTestTB) (domain.BrokerFamilyDiscoveryRequestV4, domain.BrokerFamilyDiscoveryAuthorizationRequestV4, domain.BrokerFamilyDiscoveryProjectionV4) {
	t.Helper()
	context := attachmentContextFixture()
	contextDigest, _ := VerifiedContextSHA256(context)
	request := domain.BrokerFamilyDiscoveryRequestV4{SchemaVersion: 4, RequestID: "request-1", ContractFamily: domain.BrokerContractFamilyExecutionV3, Service: "jira", BrokerID: context.BrokerID, Audience: context.Audience, ContextSHA256: contextDigest, NotAfterMillis: 7_000, Expect: domain.BrokerRequestExpectations{ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, AuthorityRevision: context.AuthorityRevision}}
	requestDigest, _ := FamilyDiscoveryRequestSHA256V4(request)
	authorization := domain.BrokerFamilyDiscoveryAuthorizationRequestV4{SchemaVersion: 4, Request: request, Context: context, RequestSHA256: requestDigest}
	projection := domain.BrokerFamilyDiscoveryProjectionV4{SchemaVersion: 4, RequestID: request.RequestID, RequestSHA256: requestDigest, ContextSHA256: contextDigest, ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, Audience: context.Audience, BrokerID: context.BrokerID, AuthorityRevision: context.AuthorityRevision, ContractFamily: request.ContractFamily, Service: request.Service, RegistrySHA256: RegistrySHA256V3(), ContractSchemaSHA256: ExecutionSchemaSHA256V3(), DiscoverySchemaSHA256: DiscoverySchemaSHA256V4(), IssuedAtMillis: 2_000, ExpiresAtMillis: 7_000, Operations: []domain.BrokerFamilyDiscoveryOperationV4{}, Complete: true}
	projection.Operations = []domain.BrokerFamilyDiscoveryOperationV4{attachmentDiscoveryOperationV4(RegistryV3()[0])}
	return request, authorization, projection
}

func attachmentDiscoveryOperationV4(value domain.BrokerAttachmentOperationDefinitionV3) domain.BrokerFamilyDiscoveryOperationV4 {
	definition := value.Definition
	return domain.BrokerFamilyDiscoveryOperationV4{ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessUnavailable, Features: append([]string{}, definition.RequiredFeatures...), Limits: definition.Limits, Effects: append([]domain.BrokerEffectDefinition{}, definition.Effects...), MaxMetadataItems: value.MaxMetadataItems, MaxJiraAttempts: value.MaxJiraAttempts, MaxAuthenticationAttempts: value.MaxAuthenticationAttempts, MaxDecisionAttempts: value.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: value.MaxTotalHostOutboundAttempts, MaxCommandHostOutboundAttempts: value.MaxCommandHostOutboundAttempts, MaxJiraResponseBytes: value.MaxJiraResponseBytes, MaxAuthorityResponseBytes: value.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: value.MaxTotalHostResponseBytes, MaxManifestLineBytes: value.MaxManifestLineBytes, MaxDataLineBytes: value.MaxDataLineBytes, MaxTerminalLineBytes: value.MaxTerminalLineBytes, MaxFramedResponseBytes: value.MaxFramedResponseBytes}
}

func FuzzDecodeAttachmentDiscoveryV4(f *testing.F) {
	f.Add([]byte(`{"schema_version":4,"contract_family":"atl.broker.execution.v3"}`))
	request, _, projection := attachmentDiscoveryV4Fixture(f)
	f.Add(mustAttachmentEncode(f, request, EncodeFamilyDiscoveryRequestV4))
	f.Add(mustAttachmentEncode(f, projection, EncodeFamilyDiscoveryProjectionV4))
	f.Fuzz(func(_ *testing.T, body []byte) {
		_, _ = DecodeFamilyDiscoveryRequestV4(body)
		_, _ = DecodeFamilyDiscoveryProjectionV4(body)
	})
}
