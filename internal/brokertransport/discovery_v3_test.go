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

func familyDiscoveryHello(now time.Time) DiscoveryNegotiationV3 {
	return DiscoveryNegotiationV3{
		SchemaVersion: 3, RequestID: "request-1", ContractFamily: domain.BrokerContractFamilyExecutionV2,
		Service: "jira", BrokerID: "broker-1", Audience: "atl-broker", ExecutionID: "execution-1",
		ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1", NotAfterMillis: now.Add(5 * time.Second).UnixMilli(),
	}
}

func TestFamilyDiscoveryNegotiationV3BindsFamilyGuardsContextAndTime(t *testing.T) {
	now := time.UnixMilli(10000)
	hello := familyDiscoveryHello(now)
	verified := testVerifiedContext(now)
	request, err := BindDiscoveryNegotiationV3(hello, verified, now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeNegotiatedDiscoveryV3(request)
	decoded, decodeErr := DecodeNegotiatedDiscoveryV3(encoded, hello, now)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(request, decoded) {
		t.Fatalf("roundtrip %v %v", err, decodeErr)
	}
	if request.NotAfterMillis != now.UnixMilli()+domain.BrokerMaxDecisionLeaseMillis {
		t.Fatalf("lease=%d", request.NotAfterMillis-now.UnixMilli())
	}
	for _, private := range []string{"principal-1", "workload-1", "jira-primary", "origin_sha256"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("negotiation leaked %s", private)
		}
	}
	for name, mutate := range map[string]func(*DiscoveryNegotiationV3){
		"request":   func(value *DiscoveryNegotiationV3) { value.RequestID = "other" },
		"family":    func(value *DiscoveryNegotiationV3) { value.ContractFamily = "atl.broker.execution.v1" },
		"service":   func(value *DiscoveryNegotiationV3) { value.Service = "confluence" },
		"broker":    func(value *DiscoveryNegotiationV3) { value.BrokerID = "other" },
		"audience":  func(value *DiscoveryNegotiationV3) { value.Audience = "other" },
		"execution": func(value *DiscoveryNegotiationV3) { value.ExecutionID = "other" },
		"epoch":     func(value *DiscoveryNegotiationV3) { value.ExecutionEpoch = "other" },
		"revision":  func(value *DiscoveryNegotiationV3) { value.AuthorityRevision = "other" },
		"deadline":  func(value *DiscoveryNegotiationV3) { value.NotAfterMillis-- },
	} {
		t.Run(name, func(t *testing.T) {
			changed := hello
			mutate(&changed)
			if _, err := DecodeNegotiatedDiscoveryV3(encoded, changed, now); err == nil {
				t.Fatal("cross-bound response accepted")
			}
		})
	}
	if _, err := DecodeNegotiatedDiscoveryV3(encoded, hello, time.UnixMilli(request.NotAfterMillis)); err == nil {
		t.Fatal("late response accepted")
	}
	verified.AuthorityRevision = "other"
	if _, err := BindDiscoveryNegotiationV3(hello, verified, now); err == nil {
		t.Fatal("stale authority negotiated")
	}
}

func TestFamilyDiscoveryTransportV3FailsClosedOnUnknownOrOmittedFamily(t *testing.T) {
	hello := familyDiscoveryHello(time.UnixMilli(10000))
	hello.ContractFamily = ""
	if _, err := EncodeDiscoveryNegotiationV3(hello); err == nil {
		t.Fatal("omitted family accepted")
	}
	hello.ContractFamily = "atl.broker.execution.v9"
	if _, err := EncodeDiscoveryNegotiationV3(hello); err == nil {
		t.Fatal("unknown family accepted")
	}

	valid, _ := EncodeDiscoveryNegotiationV3(familyDiscoveryHello(time.UnixMilli(10000)))
	for _, body := range [][]byte{
		bytes.Replace(valid, []byte(domain.BrokerContractFamilyExecutionV2), []byte("atl.broker.execution.v9"), 1),
		bytes.Replace(valid, []byte(`,"contract_family":"atl.broker.execution.v2"`), nil, 1),
	} {
		if _, err := DecodeDiscoveryNegotiationV3(body); err == nil {
			t.Fatalf("invalid family accepted: %s", body)
		}
	}
}

func TestFamilyDiscoveryTransportV3StrictDecodeAndFailureVersionSeparation(t *testing.T) {
	now := time.UnixMilli(10000)
	body, _ := EncodeDiscoveryNegotiationV3(familyDiscoveryHello(now))
	for _, invalid := range [][]byte{
		bytes.Replace(body, []byte(`"schema_version":3`), []byte(`"schema_version":3,"schema_version":3`), 1),
		bytes.Replace(body, []byte(`"service":"jira"`), []byte(`"service":"jira","policy":"injected"`), 1),
		bytes.Replace(body, []byte(`"schema_version":3`), []byte(`"schema_version":2`), 1),
		append(bytes.Clone(body), []byte(` {}`)...),
		bytes.Repeat([]byte(" "), int(MaxDiscoveryNegotiationBytesV3)+1),
	} {
		if _, err := DecodeDiscoveryNegotiationV3(invalid); err == nil {
			t.Fatal("malformed negotiation accepted")
		}
	}
	for _, reason := range []domain.BrokerReason{
		domain.BrokerReasonStaleAuthority, domain.BrokerReasonDecisionExpired, domain.BrokerReasonRevoked,
		domain.BrokerReasonUnsupported, domain.BrokerReasonAuthorizationUnavailable, domain.BrokerReasonUnsupportedConsistency,
	} {
		wire, err := EncodeDiscoveryFailureV3(reason)
		failure, decodeErr := DecodeDiscoveryFailureV3(wire)
		if err != nil || decodeErr != nil || failure.Reason != reason || failure.RetrySafe {
			t.Fatalf("failure %s %v %v", reason, err, decodeErr)
		}
		if _, err := DecodeDiscoveryFailureV2(wire); err == nil {
			t.Fatal("v3 failure accepted as v2")
		}
		oldWire, _ := EncodeDiscoveryFailureV2(reason)
		if _, err := DecodeDiscoveryFailureV3(oldWire); err == nil {
			t.Fatal("v2 failure accepted as v3")
		}
	}
}

func TestFamilyDiscoveryTransportV3PublishedSchemaAndVectors(t *testing.T) {
	published, err := os.ReadFile("../../docs/schemas/broker-discovery-http-v3.schema.json")
	if err != nil || !bytes.Equal(published, DiscoverySchemaV3()) || DiscoverySchemaSHA256V3() != "983369752920b4e74dc42a53d5943a169e3f369f8804184b9f8183d07b864ac7" {
		t.Fatalf("published schema differs: %v", err)
	}
	var transport, semantic, legacy jsonschema.Schema
	for _, value := range []struct {
		body   []byte
		target *jsonschema.Schema
	}{{DiscoverySchemaV3(), &transport}, {brokercontract.DiscoverySchemaV3(), &semantic}, {brokercontract.SchemaV1(), &legacy}} {
		if err := json.Unmarshal(value.body, value.target); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := transport.Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		switch {
		case strings.HasSuffix(uri.Path, "/broker-discovery-v3.schema.json"):
			return &semantic, nil
		case strings.HasSuffix(uri.Path, "/broker-v1.schema.json"):
			return &legacy, nil
		default:
			return nil, errors.New("unknown schema")
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	hello := familyDiscoveryHello(time.UnixMilli(10000))
	request, _ := BindDiscoveryNegotiationV3(hello, testVerifiedContext(time.UnixMilli(10000)), time.UnixMilli(10000))
	first, _ := EncodeDiscoveryNegotiationV3(hello)
	second, _ := EncodeNegotiatedDiscoveryV3(request)
	third, _ := EncodeDiscoveryFailureV3(domain.BrokerReasonDecisionExpired)
	for _, body := range [][]byte{first, second, third} {
		var value any
		if err := json.Unmarshal(body, &value); err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFamilyDiscoveryV3PreservesFrozenDiscoveryV2TransportDigest(t *testing.T) {
	if DiscoverySchemaSHA256V2() != "a27ec355cb2255065af9f1cca85dc7bcadda64bae5d86fc94a32a18da854a045" ||
		SchemaSHA256() != "0584e1709d35c8dfe27982c568d93034e0b19465edf550ee564d42441dd771b5" {
		t.Fatalf("frozen transport digest changed: %s/%s", DiscoverySchemaSHA256V2(), SchemaSHA256())
	}
}

func FuzzFamilyDiscoveryTransportV3(f *testing.F) {
	now := time.UnixMilli(10000)
	hello := familyDiscoveryHello(now)
	body, _ := EncodeDiscoveryNegotiationV3(hello)
	f.Add(body)
	request, _ := BindDiscoveryNegotiationV3(hello, testVerifiedContext(now), now)
	body, _ = EncodeNegotiatedDiscoveryV3(request)
	f.Add(body)
	body, _ = EncodeDiscoveryFailureV3(domain.BrokerReasonRevoked)
	f.Add(body)
	f.Add([]byte(`{"schema_version":3,"schema_version":3}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if value, err := DecodeDiscoveryNegotiationV3(body); err == nil {
			encoded, err := EncodeDiscoveryNegotiationV3(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeDiscoveryNegotiationV3(encoded); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = DecodeNegotiatedDiscoveryV3(body, hello, now)
		if value, err := DecodeDiscoveryFailureV3(body); err == nil {
			if _, err := EncodeDiscoveryFailureV3(value.Reason); err != nil {
				t.Fatal(err)
			}
		}
	})
}
