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
	DiscoveryNegotiatePathV4       = "/v4/discovery/negotiate"
	DiscoveryPathV4                = "/v4/discovery"
	MaxDiscoveryNegotiationBytesV4 = int64(16 << 10)
)

//go:embed schema/broker-discovery-http-v4.schema.json
var discoveryHTTPV4 []byte

func DiscoverySchemaV4() []byte { return append([]byte(nil), discoveryHTTPV4...) }

func DiscoverySchemaSHA256V4() string {
	digest := sha256.Sum256(discoveryHTTPV4)
	return hex.EncodeToString(digest[:])
}

type DiscoveryNegotiationV4 struct {
	SchemaVersion     int    `json:"schema_version"`
	RequestID         string `json:"request_id"`
	ContractFamily    string `json:"contract_family"`
	Service           string `json:"service"`
	BrokerID          string `json:"broker_id"`
	Audience          string `json:"audience"`
	ExecutionID       string `json:"execution_id"`
	ExecutionEpoch    string `json:"execution_epoch"`
	AuthorityRevision string `json:"authority_revision"`
	NotAfterMillis    int64  `json:"not_after_millis"`
}

type discoveryNegotiatedWireV4 struct {
	SchemaVersion         int             `json:"schema_version"`
	TransportSchemaSHA256 string          `json:"transport_schema_sha256"`
	Request               json.RawMessage `json:"request"`
}

func EncodeDiscoveryNegotiationV4(value DiscoveryNegotiationV4) ([]byte, error) {
	if !validDiscoveryNegotiationV4(value) {
		return nil, malformedError()
	}
	return marshalBounded(value, MaxDiscoveryNegotiationBytesV4)
}

func DecodeDiscoveryNegotiationV4(body []byte) (DiscoveryNegotiationV4, error) {
	var value DiscoveryNegotiationV4
	if !decodeExact(body, MaxDiscoveryNegotiationBytesV4, &value) || !validDiscoveryNegotiationV4(value) {
		return DiscoveryNegotiationV4{}, malformedError()
	}
	return value, nil
}

func validDiscoveryNegotiationV4(value DiscoveryNegotiationV4) bool {
	return value.SchemaVersion == domain.BrokerDiscoverySchemaVersionV4 && validIdentifier(value.RequestID) && value.ContractFamily == domain.BrokerContractFamilyExecutionV3 && value.Service == "jira" &&
		validIdentifier(value.BrokerID) && validIdentifier(value.Audience) && validIdentifier(value.ExecutionID) && validIdentifier(value.ExecutionEpoch) && validIdentifier(value.AuthorityRevision) && value.NotAfterMillis > 0
}

func BindDiscoveryNegotiationV4(value DiscoveryNegotiationV4, verified domain.BrokerVerifiedContext, now time.Time) (domain.BrokerFamilyDiscoveryRequestV4, error) {
	digest, err := brokercontract.VerifiedContextSHA256(verified)
	if !validDiscoveryNegotiationV4(value) || err != nil {
		return domain.BrokerFamilyDiscoveryRequestV4{}, malformedError()
	}
	if value.Service != verified.Backend.Service || value.BrokerID != verified.BrokerID || value.Audience != verified.Audience || value.ExecutionID != verified.ExecutionID || value.ExecutionEpoch != verified.ExecutionEpoch {
		return domain.BrokerFamilyDiscoveryRequestV4{}, discoveryRejected(domain.BrokerReasonStaleExecution)
	}
	if value.AuthorityRevision != verified.AuthorityRevision {
		return domain.BrokerFamilyDiscoveryRequestV4{}, discoveryRejected(domain.BrokerReasonStaleAuthority)
	}
	deadline := min(value.NotAfterMillis, now.UnixMilli()+domain.BrokerMaxDecisionLeaseMillis, verified.ExecutionExpiresMillis, verified.GrantExpiresMillis, verified.CredentialExpiresMillis)
	if now.UnixMilli() < verified.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || deadline <= now.UnixMilli() {
		return domain.BrokerFamilyDiscoveryRequestV4{}, discoveryRejected(domain.BrokerReasonDecisionExpired)
	}
	return domain.BrokerFamilyDiscoveryRequestV4{SchemaVersion: domain.BrokerDiscoverySchemaVersionV4, RequestID: value.RequestID, ContractFamily: value.ContractFamily, Service: value.Service, BrokerID: value.BrokerID, Audience: value.Audience, ContextSHA256: digest, NotAfterMillis: deadline, Expect: domain.BrokerRequestExpectations{ExecutionID: value.ExecutionID, ExecutionEpoch: value.ExecutionEpoch, AuthorityRevision: value.AuthorityRevision}}, nil
}

func EncodeNegotiatedDiscoveryV4(request domain.BrokerFamilyDiscoveryRequestV4) ([]byte, error) {
	body, err := brokercontract.EncodeFamilyDiscoveryRequestV4(request)
	if err != nil {
		return nil, err
	}
	return marshalBounded(discoveryNegotiatedWireV4{domain.BrokerDiscoverySchemaVersionV4, DiscoverySchemaSHA256V4(), body}, MaxDiscoveryNegotiationBytesV4)
}

func DecodeNegotiatedDiscoveryV4(body []byte, hello DiscoveryNegotiationV4, now time.Time) (domain.BrokerFamilyDiscoveryRequestV4, error) {
	var wire discoveryNegotiatedWireV4
	if !validDiscoveryNegotiationV4(hello) || !decodeExact(body, MaxDiscoveryNegotiationBytesV4, &wire) || wire.SchemaVersion != domain.BrokerDiscoverySchemaVersionV4 || wire.TransportSchemaSHA256 != DiscoverySchemaSHA256V4() {
		return domain.BrokerFamilyDiscoveryRequestV4{}, malformedError()
	}
	request, err := brokercontract.DecodeFamilyDiscoveryRequestV4(wire.Request)
	if err != nil || request.RequestID != hello.RequestID || request.ContractFamily != hello.ContractFamily || request.Service != hello.Service || request.BrokerID != hello.BrokerID || request.Audience != hello.Audience ||
		request.Expect.ExecutionID != hello.ExecutionID || request.Expect.ExecutionEpoch != hello.ExecutionEpoch || request.Expect.AuthorityRevision != hello.AuthorityRevision || request.NotAfterMillis > hello.NotAfterMillis ||
		request.NotAfterMillis > now.UnixMilli()+domain.BrokerMaxDecisionLeaseMillis || request.NotAfterMillis <= now.UnixMilli() {
		return domain.BrokerFamilyDiscoveryRequestV4{}, discoveryRejected(domain.BrokerReasonStaleExecution)
	}
	return request, nil
}

func EncodeDiscoveryFailureV4(reason domain.BrokerReason) ([]byte, error) {
	value, err := newDiscoveryFailureV4(reason)
	if err != nil {
		return nil, err
	}
	return marshalBounded(failureWire{domain.BrokerDiscoverySchemaVersionV4, value.Status, string(value.Reason), value.Recovery, value.RetrySafe, value.Complete}, MaxTransportFailureBytes)
}

func DecodeDiscoveryFailureV4(body []byte) (Failure, error) {
	var wire failureWire
	if !decodeExact(body, MaxTransportFailureBytes, &wire) || wire.SchemaVersion != domain.BrokerDiscoverySchemaVersionV4 {
		return Failure{}, malformedError()
	}
	value := Failure{wire.SchemaVersion, wire.Status, domain.BrokerReason(wire.Reason), wire.Recovery, wire.RetrySafe, wire.Complete}
	want, err := newDiscoveryFailureV4(value.Reason)
	if err != nil || !reflect.DeepEqual(value, want) {
		return Failure{}, malformedError()
	}
	return value, nil
}

func newDiscoveryFailureV4(reason domain.BrokerReason) (Failure, error) {
	ok, safe := brokercontract.ErrorForReason(reason)
	if !ok {
		return Failure{}, malformedError()
	}
	recovery := diagnostic.Recover(safe, diagnostic.OperationRead)
	return Failure{domain.BrokerDiscoverySchemaVersionV4, TransportStatusRejected, reason, string(recovery.Action), recovery.RetrySafe, true}, nil
}
