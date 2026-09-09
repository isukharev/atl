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

func TestFamilyDiscoveryV3RoundTripsAndBindsFamilyContextAndTime(t *testing.T) {
	request, authorization, projection := familyDiscoveryV3Fixtures(t)
	requestWire, requestErr := EncodeFamilyDiscoveryRequestV3(request)
	decodedRequest, requestDecodeErr := DecodeFamilyDiscoveryRequestV3(requestWire)
	authorizationWire, authorizationErr := EncodeFamilyDiscoveryAuthorizationRequestV3(authorization)
	decodedAuthorization, authorizationDecodeErr := DecodeFamilyDiscoveryAuthorizationRequestV3(authorizationWire)
	projectionWire, projectionErr := EncodeFamilyDiscoveryProjectionV3(projection)
	decodedProjection, projectionDecodeErr := DecodeFamilyDiscoveryProjectionV3(projectionWire)
	if requestErr != nil || requestDecodeErr != nil || authorizationErr != nil || authorizationDecodeErr != nil || projectionErr != nil || projectionDecodeErr != nil ||
		!reflect.DeepEqual(request, decodedRequest) || !reflect.DeepEqual(authorization, decodedAuthorization) || !reflect.DeepEqual(projection, decodedProjection) {
		t.Fatalf("round trip errors=%v/%v/%v/%v/%v/%v", requestErr, requestDecodeErr, authorizationErr, authorizationDecodeErr, projectionErr, projectionDecodeErr)
	}
	legacyDigest, _ := digestValue("discovery-request-v3", familyDiscoveryRequestV3ToWire(request))
	if legacyDigest == authorization.RequestSHA256 {
		t.Fatal("discovery v3 request reused the legacy digest namespace")
	}
	now := time.UnixMilli(3000)
	if err := ValidateFamilyDiscoveryProjectionV3ForRequest(projection, request, now); err != nil {
		t.Fatalf("request binding: %v", err)
	}
	if err := ValidateFamilyDiscoveryProjectionV3ForContext(projection, authorization, now); err != nil {
		t.Fatalf("context binding: %v", err)
	}

	for name, mutate := range map[string]func(*domain.BrokerFamilyDiscoveryProjectionV3){
		"audience":  func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.Audience = "other-audience" },
		"broker":    func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.BrokerID = "other-broker" },
		"context":   func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.ContextSHA256 = digestChar('f') },
		"execution": func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.ExecutionID = "execution-other" },
		"epoch":     func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.ExecutionEpoch = "epoch-other" },
		"revision":  func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.AuthorityRevision = "revision-other" },
		"family": func(value *domain.BrokerFamilyDiscoveryProjectionV3) {
			value.ContractFamily = "atl.broker.execution.v1"
		},
		"service": func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.Service = "confluence" },
		"expiry": func(value *domain.BrokerFamilyDiscoveryProjectionV3) {
			value.ExpiresAtMillis = request.NotAfterMillis + 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneFamilyDiscoveryV3Projection(projection)
			mutate(&changed)
			if err := ValidateFamilyDiscoveryProjectionV3ForRequest(changed, request, now); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if err := ValidateFamilyDiscoveryProjectionV3ForRequest(projection, request, time.UnixMilli(projection.ExpiresAtMillis)); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("expired projection err=%v", err)
	}

	changedContext := authorization
	changedContext.Context.AuthorityRevision = "revision-other"
	changedContext.Request.Expect.AuthorityRevision = "revision-other"
	changedContext.RequestSHA256, _ = FamilyDiscoveryRequestSHA256V3(changedContext.Request)
	if err := ValidateFamilyDiscoveryProjectionV3ForContext(projection, changedContext, now); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("changed context err=%v", err)
	}
	changedPrincipal := authorization
	changedPrincipal.Context.PrincipalID = "principal-other"
	if err := ValidateFamilyDiscoveryProjectionV3ForContext(projection, changedPrincipal, now); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("changed principal err=%v", err)
	}
	overscoped := authorization
	overscoped.Request.NotAfterMillis = overscoped.Context.GrantExpiresMillis + 1
	overscoped.RequestSHA256, _ = FamilyDiscoveryRequestSHA256V3(overscoped.Request)
	if _, err := EncodeFamilyDiscoveryAuthorizationRequestV3(overscoped); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("overscoped request err=%v", err)
	}
}

func TestFamilyDiscoveryV3RejectsUnknownOrOmittedFamily(t *testing.T) {
	request, authorization, projection := familyDiscoveryV3Fixtures(t)
	request.ContractFamily = "atl.broker.execution.v1"
	if _, err := EncodeFamilyDiscoveryRequestV3(request); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("unknown request family err=%v", err)
	}
	authorization.Request.ContractFamily = ""
	if _, err := EncodeFamilyDiscoveryAuthorizationRequestV3(authorization); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("omitted authorization family err=%v", err)
	}
	projection.ContractFamily = "atl.broker.execution.future"
	if _, err := EncodeFamilyDiscoveryProjectionV3(projection); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("unknown projection family err=%v", err)
	}

	valid, _ := EncodeFamilyDiscoveryRequestV3(familyDiscoveryRequestV3Fixture(t))
	for name, body := range map[string][]byte{
		"unknown": bytes.Replace(valid, []byte(domain.BrokerContractFamilyExecutionV2), []byte("atl.broker.execution.v9"), 1),
		"omitted": bytes.Replace(valid, []byte(`,"contract_family":"atl.broker.execution.v2"`), nil, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeFamilyDiscoveryRequestV3(body); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v body=%s", err, body)
			}
		})
	}
}

func TestFamilyDiscoveryV3BindsExactAvailableRegistryRow(t *testing.T) {
	_, _, projection := familyDiscoveryV3Fixtures(t)
	definition := RegistryV2()[0].Definition
	if !definition.Available || len(AvailableDefinitionsV2()) != 1 || len(projection.Operations) != 1 {
		t.Fatalf("available definition=%+v projection=%+v", definition, projection.Operations)
	}
	operation := familyDiscoveryOperationV3Fixture(definition)
	if !validFamilyDiscoveryOperationV3(operation, definition) {
		t.Fatal("exact available registry row rejected")
	}
	for _, access := range []domain.BrokerDiscoveryAccess{domain.BrokerDiscoveryAccessAllowed, domain.BrokerDiscoveryAccessUnavailable} {
		changed := cloneFamilyDiscoveryV3Operation(operation)
		changed.Access = access
		if !validFamilyDiscoveryOperationV3(changed, definition) {
			t.Fatalf("valid access rejected: %s", access)
		}
	}
	requestable := cloneFamilyDiscoveryV3Operation(operation)
	requestable.Access = domain.BrokerDiscoveryAccessRequestRequired
	requestable.RequestAccessCorrelation = "access-1"
	if !validFamilyDiscoveryOperationV3(requestable, definition) {
		t.Fatal("requestable operation rejected")
	}
	for name, mutate := range map[string]func(*domain.BrokerFamilyDiscoveryOperationV3){
		"supported": func(value *domain.BrokerFamilyDiscoveryOperationV3) { value.Supported = false },
		"access":    func(value *domain.BrokerFamilyDiscoveryOperationV3) { value.Access = "possible" },
		"feature":   func(value *domain.BrokerFamilyDiscoveryOperationV3) { value.Features[0] = "expanded" },
		"limit":     func(value *domain.BrokerFamilyDiscoveryOperationV3) { value.Limits.MaxResources++ },
		"effect":    func(value *domain.BrokerFamilyDiscoveryOperationV3) { value.Effects[0].Fields[0] = "name" },
		"missing correlation": func(value *domain.BrokerFamilyDiscoveryOperationV3) {
			value.Access = domain.BrokerDiscoveryAccessRequestRequired
		},
		"extra correlation": func(value *domain.BrokerFamilyDiscoveryOperationV3) { value.RequestAccessCorrelation = "access-1" },
	} {
		t.Run("operation "+name, func(t *testing.T) {
			changed := cloneFamilyDiscoveryV3Operation(operation)
			mutate(&changed)
			if validFamilyDiscoveryOperationV3(changed, definition) {
				t.Fatalf("mutated operation accepted: %+v", changed)
			}
		})
	}
	for name, mutate := range map[string]func(*domain.BrokerFamilyDiscoveryProjectionV3){
		"null operations": func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.Operations = nil },
		"missing operation": func(value *domain.BrokerFamilyDiscoveryProjectionV3) {
			value.Operations = []domain.BrokerFamilyDiscoveryOperationV3{}
		},
		"extra operation": func(value *domain.BrokerFamilyDiscoveryProjectionV3) {
			value.Operations = append(value.Operations, cloneFamilyDiscoveryV3Operation(operation))
		},
		"registry": func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.RegistrySHA256 = digestChar('f') },
		"contract": func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.ContractSchemaSHA256 = digestChar('f') },
		"schema":   func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.DiscoverySchemaSHA256 = digestChar('f') },
		"lease": func(value *domain.BrokerFamilyDiscoveryProjectionV3) {
			value.ExpiresAtMillis = value.IssuedAtMillis + domain.BrokerMaxDecisionLeaseMillis + 1
		},
		"complete": func(value *domain.BrokerFamilyDiscoveryProjectionV3) { value.Complete = false },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneFamilyDiscoveryV3Projection(projection)
			mutate(&changed)
			if _, err := EncodeFamilyDiscoveryProjectionV3(changed); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("mutated projection accepted: %+v err=%v", changed, err)
			}
		})
	}
}

func TestFamilyDiscoveryV3RejectsExpandedOrLossyJSON(t *testing.T) {
	request, authorization, projection := familyDiscoveryV3Fixtures(t)
	requestWire, _ := EncodeFamilyDiscoveryRequestV3(request)
	authorizationWire, _ := EncodeFamilyDiscoveryAuthorizationRequestV3(authorization)
	projectionWire, _ := EncodeFamilyDiscoveryProjectionV3(projection)
	var nullProjection familyDiscoveryProjectionV3Wire
	if err := json.Unmarshal(projectionWire, &nullProjection); err != nil {
		t.Fatal(err)
	}
	nullProjection.Operations = nil
	nullProjectionWire, err := json.Marshal(nullProjection)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"request duplicate":      bytes.Replace(requestWire, []byte(`"service":`), []byte(`"service":"jira","service":`), 1),
		"request unknown":        bytes.Replace(requestWire, []byte(`"service":`), []byte(`"policy":true,"service":`), 1),
		"authorization trailing": append(bytes.Clone(authorizationWire), []byte(`{}`)...),
		"projection null list":   nullProjectionWire,
		"invalid utf8":           bytes.Replace(projectionWire, []byte(`"request_id":"request-1"`), []byte("\"request_id\":\"request-\xff\""), 1),
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			if name == "invalid utf8" {
				var raw familyDiscoveryProjectionV3Wire
				if !json.Valid(data) || decodeDiscoveryV3(data, &raw) {
					t.Fatal("malformed UTF-8 must fail exact decoding before semantic identifier checks")
				}
			}
			switch {
			case strings.HasPrefix(name, "request"):
				_, err = DecodeFamilyDiscoveryRequestV3(data)
			case strings.HasPrefix(name, "authorization"):
				_, err = DecodeFamilyDiscoveryAuthorizationRequestV3(data)
			default:
				_, err = DecodeFamilyDiscoveryProjectionV3(data)
			}
			if !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	oversized := append(bytes.Clone(projectionWire), bytes.Repeat([]byte{' '}, int(MaxDiscoveryV3Bytes)+1)...)
	if _, err := DecodeFamilyDiscoveryProjectionV3(oversized); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized projection err=%v", err)
	}
}

func TestPublishedFamilyDiscoveryV3SchemaMatchesAndValidatesVectors(t *testing.T) {
	published, err := os.ReadFile("../../docs/schemas/broker-discovery-v3.schema.json")
	if err != nil || !bytes.Equal(published, DiscoverySchemaV3()) || DiscoverySchemaSHA256V3() != "f9f1637099c4d973b1381727e612b04294b0cef77898b3897a968d96d7ad8c49" {
		t.Fatalf("published schema mismatch err=%v", err)
	}
	var schema, legacy jsonschema.Schema
	if err := json.Unmarshal(DiscoverySchemaV3(), &schema); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(SchemaV1(), &legacy); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		if strings.HasSuffix(uri.Path, "/broker-v1.schema.json") {
			return &legacy, nil
		}
		return nil, errors.New("unrecognized schema")
	}})
	if err != nil {
		t.Fatal(err)
	}
	request, authorization, projection := familyDiscoveryV3Fixtures(t)
	futureProjection := cloneFamilyDiscoveryV3Projection(projection)
	futureProjection.Operations = []domain.BrokerFamilyDiscoveryOperationV3{familyDiscoveryOperationV3Fixture(RegistryV2()[0].Definition)}
	futureVector, err := json.Marshal(familyDiscoveryProjectionV3ToWire(futureProjection))
	if err != nil {
		t.Fatal(err)
	}
	for index, vector := range [][]byte{
		mustEncode(EncodeFamilyDiscoveryRequestV3(request)),
		mustEncode(EncodeFamilyDiscoveryAuthorizationRequestV3(authorization)),
		mustEncode(EncodeFamilyDiscoveryProjectionV3(projection)),
		futureVector,
	} {
		var value any
		if json.Unmarshal(vector, &value) != nil || resolved.Validate(value) != nil {
			t.Fatalf("schema rejected vector %d: %s", index, vector)
		}
	}
	for name, mutate := range map[string]func(*domain.BrokerLimits){
		"request bytes":          func(limits *domain.BrokerLimits) { limits.MaxRequestBytes = 1 },
		"qualification requests": func(limits *domain.BrokerLimits) { limits.Qualification.MaxRequests++ },
		"native body":            func(limits *domain.BrokerLimits) { limits.MaxNativeBodyBytes = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneFamilyDiscoveryV3Projection(futureProjection)
			mutate(&changed.Operations[0].Limits)
			vector, err := json.Marshal(familyDiscoveryProjectionV3ToWire(changed))
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal(vector, &value); err != nil {
				t.Fatal(err)
			}
			if resolved.Validate(value) == nil {
				t.Fatal("schema accepted non-registry operation limits")
			}
		})
	}
}

func TestFamilyDiscoveryV3PreservesFrozenV1AndDiscoveryV2Digests(t *testing.T) {
	if RegistrySHA256() != "a712329120114874b6d1c2f62884ca28a45cdef8616fd78073bc3a6a72fae72c" ||
		SchemaSHA256() != "fdf82ad96e59a6c32f639f4602da15632dfcb72ab0fb734df00782bf29967372" ||
		DiscoverySchemaSHA256V2() != "9b7415d7fed9dcf0cd7f53e2b0c212ffd5a22dbca382081ac85d70be95a84d30" {
		t.Fatalf("frozen semantic digest changed: %s/%s/%s", RegistrySHA256(), SchemaSHA256(), DiscoverySchemaSHA256V2())
	}
}

func familyDiscoveryRequestV3Fixture(t testing.TB) domain.BrokerFamilyDiscoveryRequestV3 {
	t.Helper()
	context := fixtureContext()
	contextDigest, err := VerifiedContextSHA256(context)
	if err != nil {
		t.Fatal(err)
	}
	return domain.BrokerFamilyDiscoveryRequestV3{
		SchemaVersion: 3, RequestID: "request-1", ContractFamily: domain.BrokerContractFamilyExecutionV2,
		Service: "jira", BrokerID: context.BrokerID, Audience: context.Audience, ContextSHA256: contextDigest, NotAfterMillis: 6000,
		Expect: domain.BrokerRequestExpectations{ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, AuthorityRevision: context.AuthorityRevision},
	}
}

func familyDiscoveryV3Fixtures(t testing.TB) (domain.BrokerFamilyDiscoveryRequestV3, domain.BrokerFamilyDiscoveryAuthorizationRequestV3, domain.BrokerFamilyDiscoveryProjectionV3) {
	t.Helper()
	request := familyDiscoveryRequestV3Fixture(t)
	context := fixtureContext()
	requestDigest, err := FamilyDiscoveryRequestSHA256V3(request)
	if err != nil {
		t.Fatal(err)
	}
	definitions := AvailableDefinitionsV2()
	operations := make([]domain.BrokerFamilyDiscoveryOperationV3, len(definitions))
	for index, definition := range definitions {
		operations[index] = familyDiscoveryOperationV3Fixture(definition.Definition)
	}
	projection := domain.BrokerFamilyDiscoveryProjectionV3{
		SchemaVersion: 3, RequestID: request.RequestID, RequestSHA256: requestDigest, ContextSHA256: request.ContextSHA256,
		ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, Audience: context.Audience, BrokerID: context.BrokerID,
		AuthorityRevision: context.AuthorityRevision, ContractFamily: request.ContractFamily, Service: request.Service,
		RegistrySHA256: RegistrySHA256V2(), ContractSchemaSHA256: ExecutionSchemaSHA256V2(), DiscoverySchemaSHA256: DiscoverySchemaSHA256V3(),
		IssuedAtMillis: 2000, ExpiresAtMillis: 6000, Operations: operations, Complete: true,
	}
	authorization := domain.BrokerFamilyDiscoveryAuthorizationRequestV3{SchemaVersion: 3, Request: request, Context: context, RequestSHA256: requestDigest}
	return request, authorization, projection
}

func familyDiscoveryOperationV3Fixture(definition domain.BrokerOperationDefinition) domain.BrokerFamilyDiscoveryOperationV3 {
	return domain.BrokerFamilyDiscoveryOperationV3{
		ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessUnavailable,
		Features: copyStrings(definition.RequiredFeatures), Limits: definition.Limits, Effects: definition.Effects,
	}
}

func cloneFamilyDiscoveryV3Operation(value domain.BrokerFamilyDiscoveryOperationV3) domain.BrokerFamilyDiscoveryOperationV3 {
	value.Features = append([]string(nil), value.Features...)
	value.Effects = append([]domain.BrokerEffectDefinition(nil), value.Effects...)
	for index := range value.Effects {
		value.Effects[index].Fields = append([]string(nil), value.Effects[index].Fields...)
	}
	return value
}

func cloneFamilyDiscoveryV3Projection(value domain.BrokerFamilyDiscoveryProjectionV3) domain.BrokerFamilyDiscoveryProjectionV3 {
	operations := make([]domain.BrokerFamilyDiscoveryOperationV3, len(value.Operations))
	copy(operations, value.Operations)
	value.Operations = operations
	for index := range value.Operations {
		value.Operations[index].Features = append([]string(nil), value.Operations[index].Features...)
		value.Operations[index].Effects = append([]domain.BrokerEffectDefinition(nil), value.Operations[index].Effects...)
		for effectIndex := range value.Operations[index].Effects {
			value.Operations[index].Effects[effectIndex].Fields = append([]string(nil), value.Operations[index].Effects[effectIndex].Fields...)
		}
	}
	return value
}

func FuzzDecodeFamilyDiscoveryProjectionV3(f *testing.F) {
	_, _, projection := familyDiscoveryV3Fixtures(f)
	valid, err := EncodeFamilyDiscoveryProjectionV3(projection)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{"schema_version":3}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := DecodeFamilyDiscoveryProjectionV3(data)
		if err != nil {
			return
		}
		encoded, err := EncodeFamilyDiscoveryProjectionV3(value)
		if err != nil || int64(len(encoded)) > MaxDiscoveryV3Bytes {
			t.Fatalf("re-encode len=%d err=%v", len(encoded), err)
		}
		roundTrip, err := DecodeFamilyDiscoveryProjectionV3(encoded)
		if err != nil || !reflect.DeepEqual(roundTrip, value) {
			t.Fatalf("round trip err=%v", err)
		}
	})
}
