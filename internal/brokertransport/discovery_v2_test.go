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

func discoveryHello(now time.Time) DiscoveryNegotiationV2 {
	return DiscoveryNegotiationV2{2, "request-1", "jira", "broker-1", "atl-broker", "execution-1", "epoch-1", "revision-1", now.Add(5 * time.Second).UnixMilli()}
}

func TestDiscoveryNegotiationBindsGuardsContextAndTime(t *testing.T) {
	now := time.UnixMilli(10000)
	hello := discoveryHello(now)
	verified := testVerifiedContext(now)
	request, err := BindDiscoveryNegotiationV2(hello, verified, now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeNegotiatedDiscoveryV2(request)
	decoded, decodeErr := DecodeNegotiatedDiscoveryV2(encoded, hello, now)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(request, decoded) {
		t.Fatalf("roundtrip %v %v", err, decodeErr)
	}
	for _, private := range []string{"principal-1", "workload-1", "jira-primary", "origin_sha256"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("negotiation leaked %s", private)
		}
	}
	for name, mutate := range map[string]func(*DiscoveryNegotiationV2){
		"request":   func(v *DiscoveryNegotiationV2) { v.RequestID = "other" },
		"service":   func(v *DiscoveryNegotiationV2) { v.Service = "confluence" },
		"broker":    func(v *DiscoveryNegotiationV2) { v.BrokerID = "other" },
		"audience":  func(v *DiscoveryNegotiationV2) { v.Audience = "other" },
		"execution": func(v *DiscoveryNegotiationV2) { v.ExecutionID = "other" },
		"epoch":     func(v *DiscoveryNegotiationV2) { v.ExecutionEpoch = "other" },
		"revision":  func(v *DiscoveryNegotiationV2) { v.AuthorityRevision = "other" },
		"deadline":  func(v *DiscoveryNegotiationV2) { v.NotAfterMillis-- },
	} {
		t.Run(name, func(t *testing.T) {
			changed := hello
			mutate(&changed)
			if _, err := DecodeNegotiatedDiscoveryV2(encoded, changed, now); err == nil {
				t.Fatal("cross-bound response accepted")
			}
		})
	}
	if _, err := DecodeNegotiatedDiscoveryV2(encoded, hello, time.UnixMilli(request.NotAfterMillis)); err == nil {
		t.Fatal("late response accepted")
	}
	verified.AuthorityRevision = "other"
	if _, err := BindDiscoveryNegotiationV2(hello, verified, now); err == nil {
		t.Fatal("stale authority negotiated")
	}
}

func TestDiscoveryTransportStrictDecodeAndFailureVersionSeparation(t *testing.T) {
	now := time.UnixMilli(10000)
	body, _ := EncodeDiscoveryNegotiationV2(discoveryHello(now))
	for _, invalid := range [][]byte{
		bytes.Replace(body, []byte(`"schema_version":2`), []byte(`"schema_version":2,"schema_version":2`), 1),
		bytes.Replace(body, []byte(`"service":"jira"`), []byte(`"service":"jira","policy":"injected"`), 1),
		bytes.Replace(body, []byte(`"schema_version":2`), []byte(`"schema_version":1`), 1),
		append(append([]byte{}, body...), []byte(` {}`)...),
		bytes.Repeat([]byte(" "), int(MaxDiscoveryNegotiationBytesV2)+1),
	} {
		if _, err := DecodeDiscoveryNegotiationV2(invalid); err == nil {
			t.Fatal("malformed negotiation accepted")
		}
	}
	for _, reason := range []domain.BrokerReason{domain.BrokerReasonStaleAuthority, domain.BrokerReasonDecisionExpired, domain.BrokerReasonRevoked, domain.BrokerReasonUnsupported, domain.BrokerReasonAuthorizationUnavailable, domain.BrokerReasonProposalClearanceRequired, domain.BrokerReasonOutcomeUnknown} {
		wire, err := EncodeDiscoveryFailureV2(reason)
		failure, decodeErr := DecodeDiscoveryFailureV2(wire)
		if err != nil || decodeErr != nil || failure.Reason != reason || failure.RetrySafe {
			t.Fatalf("failure %s %v %v", reason, err, decodeErr)
		}
		if _, err := DecodeFailureV1(wire); err == nil {
			t.Fatal("v2 failure accepted as v1")
		}
		old, _ := NewFailure(reason)
		if (reason == domain.BrokerReasonStaleAuthority || reason == domain.BrokerReasonDecisionExpired) && old.Recovery != "inspect_failure" {
			t.Fatal("v1 recovery mapping changed")
		}
		oldWire, _ := EncodeFailureV1(old)
		if _, err := DecodeDiscoveryFailureV2(oldWire); err == nil {
			t.Fatal("v1 failure accepted as v2")
		}
	}
}

func TestDiscoveryTransportKeepsV1FailureMapping(t *testing.T) {
	for _, reason := range []domain.BrokerReason{domain.BrokerReasonStaleExecution, domain.BrokerReasonStaleAuthority, domain.BrokerReasonDecisionExpired, domain.BrokerReasonUnsupportedConsistency} {
		failure, err := NewFailure(reason)
		wire, encodeErr := EncodeFailureV1(failure)
		decoded, decodeErr := DecodeFailureV1(wire)
		if err != nil || encodeErr != nil || decodeErr != nil || decoded.Recovery != "inspect_failure" || decoded.RetrySafe {
			t.Fatalf("v1 mapping changed for %s: %+v", reason, decoded)
		}
	}
}

func TestDiscoveryTransportPublishedSchemaAndVectors(t *testing.T) {
	published, err := os.ReadFile("../../docs/schemas/broker-discovery-http-v2.schema.json")
	if err != nil || !bytes.Equal(published, DiscoverySchemaV2()) {
		t.Fatalf("published schema differs: %v", err)
	}
	var transport, semantic, legacy jsonschema.Schema
	for _, v := range []struct {
		body   []byte
		target *jsonschema.Schema
	}{{DiscoverySchemaV2(), &transport}, {brokercontract.DiscoverySchemaV2(), &semantic}, {brokercontract.SchemaV1(), &legacy}} {
		if err := json.Unmarshal(v.body, v.target); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := transport.Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		switch {
		case strings.HasSuffix(uri.Path, "/broker-discovery-v2.schema.json"):
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
	now := time.UnixMilli(10000)
	hello := discoveryHello(now)
	request, _ := BindDiscoveryNegotiationV2(hello, testVerifiedContext(now), now)
	first, _ := EncodeDiscoveryNegotiationV2(hello)
	second, _ := EncodeNegotiatedDiscoveryV2(request)
	third, _ := EncodeDiscoveryFailureV2(domain.BrokerReasonDecisionExpired)
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

func FuzzDiscoveryTransportV2(f *testing.F) {
	now := time.UnixMilli(10000)
	hello := discoveryHello(now)
	body, _ := EncodeDiscoveryNegotiationV2(hello)
	f.Add(body)
	request, _ := BindDiscoveryNegotiationV2(hello, testVerifiedContext(now), now)
	body, _ = EncodeNegotiatedDiscoveryV2(request)
	f.Add(body)
	body, _ = EncodeDiscoveryFailureV2(domain.BrokerReasonRevoked)
	f.Add(body)
	f.Add([]byte(`{"schema_version":2,"schema_version":2}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if value, err := DecodeDiscoveryNegotiationV2(body); err == nil {
			encoded, err := EncodeDiscoveryNegotiationV2(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeDiscoveryNegotiationV2(encoded); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = DecodeNegotiatedDiscoveryV2(body, hello, now)
		if value, err := DecodeDiscoveryFailureV2(body); err == nil {
			if _, err := EncodeDiscoveryFailureV2(value.Reason); err != nil {
				t.Fatal(err)
			}
		}
	})
}
