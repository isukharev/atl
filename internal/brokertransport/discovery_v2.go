package brokertransport

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
)

const (
	DiscoveryNegotiatePathV2       = "/v2/discovery/negotiate"
	DiscoveryPathV2                = "/v2/discovery"
	MaxDiscoveryNegotiationBytesV2 = int64(16 << 10)
)

//go:embed schema/broker-discovery-http-v2.schema.json
var discoveryHTTPV2 []byte

func DiscoverySchemaV2() []byte { return append([]byte(nil), discoveryHTTPV2...) }
func DiscoverySchemaSHA256V2() string {
	digest := sha256.Sum256(discoveryHTTPV2)
	return hex.EncodeToString(digest[:])
}

// DiscoveryNegotiationV2 contains only client equality guards. The response
// supplies an authenticated context digest without disclosing that context.
type DiscoveryNegotiationV2 struct {
	SchemaVersion     int    `json:"schema_version"`
	RequestID         string `json:"request_id"`
	Service           string `json:"service"`
	BrokerID          string `json:"broker_id"`
	Audience          string `json:"audience"`
	ExecutionID       string `json:"execution_id"`
	ExecutionEpoch    string `json:"execution_epoch"`
	AuthorityRevision string `json:"authority_revision"`
	NotAfterMillis    int64  `json:"not_after_millis"`
}

type discoveryNegotiatedWireV2 struct {
	SchemaVersion         int             `json:"schema_version"`
	TransportSchemaSHA256 string          `json:"transport_schema_sha256"`
	Request               json.RawMessage `json:"request"`
}

func EncodeDiscoveryNegotiationV2(value DiscoveryNegotiationV2) ([]byte, error) {
	if !validDiscoveryNegotiationV2(value) {
		return nil, malformedError()
	}
	return marshalBounded(value, MaxDiscoveryNegotiationBytesV2)
}

func DecodeDiscoveryNegotiationV2(body []byte) (DiscoveryNegotiationV2, error) {
	var value DiscoveryNegotiationV2
	if !decodeExact(body, MaxDiscoveryNegotiationBytesV2, &value) || !validDiscoveryNegotiationV2(value) {
		return DiscoveryNegotiationV2{}, malformedError()
	}
	return value, nil
}

func validDiscoveryNegotiationV2(value DiscoveryNegotiationV2) bool {
	return value.SchemaVersion == 2 && validIdentifier(value.RequestID) && (value.Service == "jira" || value.Service == "confluence") && validIdentifier(value.BrokerID) && validIdentifier(value.Audience) && validIdentifier(value.ExecutionID) && validIdentifier(value.ExecutionEpoch) && validIdentifier(value.AuthorityRevision) && value.NotAfterMillis > 0
}

func BindDiscoveryNegotiationV2(value DiscoveryNegotiationV2, verified domain.BrokerVerifiedContext, now time.Time) (domain.BrokerDiscoveryRequestV2, error) {
	digest, err := brokercontract.VerifiedContextSHA256(verified)
	if !validDiscoveryNegotiationV2(value) || err != nil {
		return domain.BrokerDiscoveryRequestV2{}, malformedError()
	}
	if value.Service != verified.Backend.Service || value.BrokerID != verified.BrokerID || value.Audience != verified.Audience || value.ExecutionID != verified.ExecutionID || value.ExecutionEpoch != verified.ExecutionEpoch {
		return domain.BrokerDiscoveryRequestV2{}, discoveryRejected(domain.BrokerReasonStaleExecution)
	}
	if value.AuthorityRevision != verified.AuthorityRevision {
		return domain.BrokerDiscoveryRequestV2{}, discoveryRejected(domain.BrokerReasonStaleAuthority)
	}
	deadline := min(value.NotAfterMillis, now.UnixMilli()+domain.BrokerMaxDecisionLeaseMillis, verified.ExecutionExpiresMillis, verified.GrantExpiresMillis, verified.CredentialExpiresMillis)
	if now.UnixMilli() < verified.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || deadline <= now.UnixMilli() {
		return domain.BrokerDiscoveryRequestV2{}, discoveryRejected(domain.BrokerReasonDecisionExpired)
	}
	return domain.BrokerDiscoveryRequestV2{SchemaVersion: 2, RequestID: value.RequestID, Service: value.Service, BrokerID: value.BrokerID, Audience: value.Audience, ContextSHA256: digest, NotAfterMillis: deadline, Expect: domain.BrokerRequestExpectations{ExecutionID: value.ExecutionID, ExecutionEpoch: value.ExecutionEpoch, AuthorityRevision: value.AuthorityRevision}}, nil
}

func EncodeNegotiatedDiscoveryV2(request domain.BrokerDiscoveryRequestV2) ([]byte, error) {
	body, err := brokercontract.EncodeDiscoveryRequestV2(request)
	if err != nil {
		return nil, err
	}
	return marshalBounded(discoveryNegotiatedWireV2{2, DiscoverySchemaSHA256V2(), body}, MaxDiscoveryNegotiationBytesV2)
}

func DecodeNegotiatedDiscoveryV2(body []byte, hello DiscoveryNegotiationV2, now time.Time) (domain.BrokerDiscoveryRequestV2, error) {
	var wire discoveryNegotiatedWireV2
	if !validDiscoveryNegotiationV2(hello) || !decodeExact(body, MaxDiscoveryNegotiationBytesV2, &wire) || wire.SchemaVersion != 2 || wire.TransportSchemaSHA256 != DiscoverySchemaSHA256V2() {
		return domain.BrokerDiscoveryRequestV2{}, malformedError()
	}
	request, err := brokercontract.DecodeDiscoveryRequestV2(wire.Request)
	if err != nil || request.RequestID != hello.RequestID || request.Service != hello.Service || request.BrokerID != hello.BrokerID || request.Audience != hello.Audience || request.Expect.ExecutionID != hello.ExecutionID || request.Expect.ExecutionEpoch != hello.ExecutionEpoch || request.Expect.AuthorityRevision != hello.AuthorityRevision || request.NotAfterMillis > hello.NotAfterMillis || request.NotAfterMillis > now.UnixMilli()+domain.BrokerMaxDecisionLeaseMillis || request.NotAfterMillis <= now.UnixMilli() {
		return domain.BrokerDiscoveryRequestV2{}, discoveryRejected(domain.BrokerReasonStaleExecution)
	}
	return request, nil
}

// EncodeDiscoveryFailureV2 uses an independent failure version; operation and
// authentication v1 bytes remain independent of this optional transport.
func EncodeDiscoveryFailureV2(reason domain.BrokerReason) ([]byte, error) {
	value, err := newDiscoveryFailureV2(reason)
	if err != nil {
		return nil, err
	}
	return marshalBounded(failureWire{2, value.Status, string(value.Reason), value.Recovery, value.RetrySafe, value.Complete}, MaxTransportFailureBytes)
}

func DecodeDiscoveryFailureV2(body []byte) (Failure, error) {
	var wire failureWire
	if !decodeExact(body, MaxTransportFailureBytes, &wire) || wire.SchemaVersion != 2 {
		return Failure{}, malformedError()
	}
	value := Failure{2, wire.Status, domain.BrokerReason(wire.Reason), wire.Recovery, wire.RetrySafe, wire.Complete}
	want, err := newDiscoveryFailureV2(value.Reason)
	if err != nil || !reflect.DeepEqual(value, want) {
		return Failure{}, malformedError()
	}
	return value, nil
}

func newDiscoveryFailureV2(reason domain.BrokerReason) (Failure, error) {
	ok, safe := brokercontract.ErrorForReason(reason)
	if !ok {
		return Failure{}, malformedError()
	}
	recovery := diagnostic.Recover(safe, diagnostic.OperationRead)
	return Failure{2, TransportStatusRejected, reason, string(recovery.Action), recovery.RetrySafe, true}, nil
}

func discoveryRejected(reason domain.BrokerReason) error {
	_, err := brokercontract.ErrorForReason(reason)
	return err
}
