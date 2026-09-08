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

func TestDiscoveryV2RoundTripsAndBindsRequestContextAndTime(t *testing.T) {
	request, authorization, projection := discoveryV2Fixtures(t)
	requestWire, err := EncodeDiscoveryRequestV2(request)
	decodedRequest, requestErr := DecodeDiscoveryRequestV2(requestWire)
	authorizationWire, authorizationEncodeErr := EncodeDiscoveryAuthorizationRequestV2(authorization)
	decodedAuthorization, authorizationDecodeErr := DecodeDiscoveryAuthorizationRequestV2(authorizationWire)
	projectionWire, projectionEncodeErr := EncodeDiscoveryProjectionV2(projection)
	decodedProjection, projectionDecodeErr := DecodeDiscoveryProjectionV2(projectionWire)
	if err != nil || requestErr != nil || authorizationEncodeErr != nil || authorizationDecodeErr != nil || projectionEncodeErr != nil || projectionDecodeErr != nil ||
		!reflect.DeepEqual(decodedRequest, request) || !reflect.DeepEqual(decodedAuthorization, authorization) || !reflect.DeepEqual(decodedProjection, projection) {
		t.Fatalf("round trip errors=%v/%v/%v/%v/%v/%v", err, requestErr, authorizationEncodeErr, authorizationDecodeErr, projectionEncodeErr, projectionDecodeErr)
	}
	legacyDigest, _ := digestValue("discovery-request-v2", discoveryRequestV2ToWire(request))
	if legacyDigest == authorization.RequestSHA256 {
		t.Fatal("discovery v2 request reused the v1 digest namespace")
	}
	now := time.UnixMilli(3000)
	if err := ValidateDiscoveryProjectionV2ForRequest(projection, request, now); err != nil {
		t.Fatalf("request binding: %v", err)
	}
	if err := ValidateDiscoveryProjectionV2ForContext(projection, authorization, now); err != nil {
		t.Fatalf("context binding: %v", err)
	}

	changedRequest := request
	changedRequest.RequestID = "request-other"
	if err := ValidateDiscoveryProjectionV2ForRequest(projection, changedRequest, now); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("changed request err=%v", err)
	}
	for name, mutate := range map[string]func(*domain.BrokerDiscoveryProjectionV2){
		"audience":       func(value *domain.BrokerDiscoveryProjectionV2) { value.Audience = "other-audience" },
		"broker":         func(value *domain.BrokerDiscoveryProjectionV2) { value.BrokerID = "other-broker" },
		"context":        func(value *domain.BrokerDiscoveryProjectionV2) { value.ContextSHA256 = digestChar('f') },
		"session expiry": func(value *domain.BrokerDiscoveryProjectionV2) { value.ExpiresAtMillis = request.NotAfterMillis + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneDiscoveryV2Projection(projection)
			mutate(&changed)
			if err := ValidateDiscoveryProjectionV2ForRequest(changed, request, now); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	changedContext := authorization
	changedContext.Context.AuthorityRevision = "revision-other"
	changedContext.Request.Expect.AuthorityRevision = "revision-other"
	changedContext.RequestSHA256, _ = DiscoveryRequestSHA256V2(changedContext.Request)
	if err := ValidateDiscoveryProjectionV2ForContext(projection, changedContext, now); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("changed context err=%v", err)
	}
	if err := ValidateDiscoveryProjectionV2ForRequest(projection, request, time.UnixMilli(projection.ExpiresAtMillis)); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("expired projection err=%v", err)
	}

	future := authorization
	future.Context.ExecutionNotBeforeMillis = 10000
	futureContextDigest, _ := VerifiedContextSHA256(future.Context)
	future.Request.ContextSHA256 = futureContextDigest
	future.Request.NotAfterMillis = 11000
	future.RequestSHA256, _ = DiscoveryRequestSHA256V2(future.Request)
	futureProjection := cloneDiscoveryV2Projection(projection)
	futureProjection.ContextSHA256 = futureContextDigest
	futureProjection.RequestSHA256 = future.RequestSHA256
	futureProjection.IssuedAtMillis = 10000
	futureProjection.ExpiresAtMillis = 11000
	earlyProjection := cloneDiscoveryV2Projection(futureProjection)
	earlyProjection.IssuedAtMillis = 6000
	if err := ValidateDiscoveryProjectionV2ForContext(earlyProjection, future, time.UnixMilli(9000)); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("pre-not-before issuance err=%v", err)
	}
	if err := ValidateDiscoveryProjectionV2ForContext(futureProjection, future, time.UnixMilli(8999)); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("pre-execution projection err=%v", err)
	}
	if err := ValidateDiscoveryProjectionV2ForContext(futureProjection, future, time.UnixMilli(9000)); err != nil {
		t.Fatalf("clock-allowance boundary err=%v", err)
	}
	overscoped := authorization
	overscoped.Request.NotAfterMillis = overscoped.Context.GrantExpiresMillis + 1
	overscoped.RequestSHA256, _ = DiscoveryRequestSHA256V2(overscoped.Request)
	if _, err := EncodeDiscoveryAuthorizationRequestV2(overscoped); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("overscoped request err=%v", err)
	}
}

func TestDiscoveryV2AccessAndOperationSetAreClosed(t *testing.T) {
	_, _, projection := discoveryV2Fixtures(t)
	for _, mutate := range []func(*domain.BrokerDiscoveryProjectionV2){
		func(value *domain.BrokerDiscoveryProjectionV2) { value.Operations[0].Supported = false },
		func(value *domain.BrokerDiscoveryProjectionV2) { value.Operations[0].Access = "possible" },
		func(value *domain.BrokerDiscoveryProjectionV2) {
			value.Operations[0].RequestAccessCorrelation = "unexpected"
		},
		func(value *domain.BrokerDiscoveryProjectionV2) {
			value.Operations[0].Access = domain.BrokerDiscoveryAccessRequestRequired
		},
		func(value *domain.BrokerDiscoveryProjectionV2) { value.Operations = nil },
		func(value *domain.BrokerDiscoveryProjectionV2) { value.RegistrySHA256 = digestChar('f') },
		func(value *domain.BrokerDiscoveryProjectionV2) { value.DiscoverySchemaSHA256 = digestChar('f') },
	} {
		value := cloneDiscoveryV2Projection(projection)
		mutate(&value)
		if _, err := EncodeDiscoveryProjectionV2(value); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("mutated projection accepted: %+v err=%v", value, err)
		}
	}
	requestable := cloneDiscoveryV2Projection(projection)
	requestable.Operations[0].Access = domain.BrokerDiscoveryAccessRequestRequired
	requestable.Operations[0].RequestAccessCorrelation = strings.Repeat("a", 64)
	if _, err := EncodeDiscoveryProjectionV2(requestable); err != nil {
		t.Fatalf("requestable projection: %v", err)
	}
	requestable.Operations[0].RequestAccessCorrelation += "a"
	if _, err := EncodeDiscoveryProjectionV2(requestable); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized correlation err=%v", err)
	}
}

func TestDiscoveryV2ProjectsOnlySelectedBackendService(t *testing.T) {
	for _, service := range []string{"jira", "confluence"} {
		request, authorization, projection := discoveryV2FixturesFor(t, service)
		if len(projection.Operations) != 1 || projection.Service != service || authorization.Context.Backend.Service != service || request.Service != service {
			t.Fatalf("service=%s projection=%+v", service, projection)
		}
		definition, ok := Definition(projection.Operations[0].ID, projection.Operations[0].Version)
		if !ok || definition.BackendService != service {
			t.Fatalf("service=%s operation=%+v", service, projection.Operations[0])
		}
	}
}

func TestDiscoveryV2RejectsExpandedOrLossyJSON(t *testing.T) {
	request, authorization, projection := discoveryV2Fixtures(t)
	requestWire, _ := EncodeDiscoveryRequestV2(request)
	authorizationWire, _ := EncodeDiscoveryAuthorizationRequestV2(authorization)
	projectionWire, _ := EncodeDiscoveryProjectionV2(projection)
	for name, data := range map[string][]byte{
		"request duplicate":      bytes.Replace(requestWire, []byte(`"service":`), []byte(`"service":"jira","service":`), 1),
		"request unknown":        bytes.Replace(requestWire, []byte(`"service":`), []byte(`"unknown":true,"service":`), 1),
		"authorization trailing": append(bytes.Clone(authorizationWire), []byte(`{}`)...),
		"projection null list":   bytes.Replace(projectionWire, []byte(`"operations":[`), []byte(`"operations":null,"discard":[`), 1),
		"invalid utf8":           append(bytes.Clone(projectionWire), 0xff),
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			switch {
			case strings.HasPrefix(name, "request"):
				_, err = DecodeDiscoveryRequestV2(data)
			case strings.HasPrefix(name, "authorization"):
				_, err = DecodeDiscoveryAuthorizationRequestV2(data)
			default:
				_, err = DecodeDiscoveryProjectionV2(data)
			}
			if !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	oversized := append(bytes.Clone(projectionWire), bytes.Repeat([]byte{' '}, int(MaxDiscoveryV2Bytes)+1)...)
	if _, err := DecodeDiscoveryProjectionV2(oversized); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized projection err=%v", err)
	}
}

func TestPublishedDiscoveryV2SchemaMatchesAndValidatesVectors(t *testing.T) {
	published, err := os.ReadFile("../../docs/schemas/broker-discovery-v2.schema.json")
	if err != nil || !bytes.Equal(published, DiscoverySchemaV2()) || !validDigest(DiscoverySchemaSHA256V2()) {
		t.Fatalf("published schema mismatch err=%v", err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(DiscoverySchemaV2(), &schema); err != nil {
		t.Fatal(err)
	}
	var semantic jsonschema.Schema
	if err := json.Unmarshal(SchemaV1(), &semantic); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		if strings.HasSuffix(uri.Path, "/broker-v1.schema.json") {
			return &semantic, nil
		}
		return nil, errors.New("unrecognized schema")
	}})
	if err != nil {
		t.Fatal(err)
	}
	request, authorization, projection := discoveryV2Fixtures(t)
	vectors := [][]byte{mustEncode(EncodeDiscoveryRequestV2(request)), mustEncode(EncodeDiscoveryAuthorizationRequestV2(authorization)), mustEncode(EncodeDiscoveryProjectionV2(projection))}
	for index, vector := range vectors {
		var value any
		if json.Unmarshal(vector, &value) != nil || resolved.Validate(value) != nil {
			t.Fatalf("schema rejected vector %d: %s", index, vector)
		}
	}
}

func discoveryV2Fixtures(t testing.TB) (domain.BrokerDiscoveryRequestV2, domain.BrokerDiscoveryAuthorizationRequestV2, domain.BrokerDiscoveryProjectionV2) {
	return discoveryV2FixturesFor(t, "jira")
}

func discoveryV2FixturesFor(t testing.TB, service string) (domain.BrokerDiscoveryRequestV2, domain.BrokerDiscoveryAuthorizationRequestV2, domain.BrokerDiscoveryProjectionV2) {
	t.Helper()
	context := fixtureContext()
	context.Backend.Service = service
	context.Backend.WorkloadBackendID = service + "-primary"
	contextDigest, err := VerifiedContextSHA256(context)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.BrokerDiscoveryRequestV2{SchemaVersion: 2, RequestID: "request-1", Service: service, BrokerID: context.BrokerID, Audience: context.Audience, ContextSHA256: contextDigest, NotAfterMillis: 6000, Expect: domain.BrokerRequestExpectations{ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, AuthorityRevision: context.AuthorityRevision}}
	requestDigest, err := DiscoveryRequestSHA256V2(request)
	if err != nil {
		t.Fatal(err)
	}
	definition := availableDefinitionsForService(service)[0]
	operation := domain.BrokerDiscoveryOperationV2{ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed, Features: wireStrings(definition.RequiredFeatures), Limits: definition.Limits, Effects: definition.Effects}
	projection := domain.BrokerDiscoveryProjectionV2{
		SchemaVersion: 2, RequestID: request.RequestID, RequestSHA256: requestDigest, ContextSHA256: contextDigest,
		ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, Audience: context.Audience, BrokerID: context.BrokerID, AuthorityRevision: context.AuthorityRevision, Service: service,
		RegistrySHA256: RegistrySHA256(), ContractSchemaSHA256: SchemaSHA256(), DiscoverySchemaSHA256: DiscoverySchemaSHA256V2(),
		IssuedAtMillis: 2000, ExpiresAtMillis: 6000, Operations: []domain.BrokerDiscoveryOperationV2{operation}, Complete: true,
	}
	authorization := domain.BrokerDiscoveryAuthorizationRequestV2{SchemaVersion: 2, Request: request, Context: context, RequestSHA256: requestDigest}
	return request, authorization, projection
}

func cloneDiscoveryV2Projection(value domain.BrokerDiscoveryProjectionV2) domain.BrokerDiscoveryProjectionV2 {
	value.Operations = append([]domain.BrokerDiscoveryOperationV2(nil), value.Operations...)
	for index := range value.Operations {
		value.Operations[index].Features = append([]string(nil), value.Operations[index].Features...)
		value.Operations[index].Effects = append([]domain.BrokerEffectDefinition(nil), value.Operations[index].Effects...)
		for effectIndex := range value.Operations[index].Effects {
			value.Operations[index].Effects[effectIndex].Fields = append([]string(nil), value.Operations[index].Effects[effectIndex].Fields...)
		}
	}
	return value
}

func FuzzDecodeDiscoveryProjectionV2(f *testing.F) {
	_, _, projection := discoveryV2Fixtures(f)
	valid, err := EncodeDiscoveryProjectionV2(projection)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{"schema_version":2}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := DecodeDiscoveryProjectionV2(data)
		if err != nil {
			return
		}
		encoded, err := EncodeDiscoveryProjectionV2(value)
		if err != nil || int64(len(encoded)) > MaxDiscoveryV2Bytes {
			t.Fatalf("re-encode len=%d err=%v", len(encoded), err)
		}
		roundTrip, err := DecodeDiscoveryProjectionV2(encoded)
		if err != nil || !reflect.DeepEqual(roundTrip, value) {
			t.Fatalf("round trip err=%v", err)
		}
	})
}
