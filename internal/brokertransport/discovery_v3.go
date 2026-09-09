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
	DiscoveryNegotiatePathV3       = "/v3/discovery/negotiate"
	DiscoveryPathV3                = "/v3/discovery"
	MaxDiscoveryNegotiationBytesV3 = int64(16 << 10)
)

//go:embed schema/broker-discovery-http-v3.schema.json
var discoveryHTTPV3 []byte

func DiscoverySchemaV3() []byte { return append([]byte(nil), discoveryHTTPV3...) }

func DiscoverySchemaSHA256V3() string {
	digest := sha256.Sum256(discoveryHTTPV3)
	return hex.EncodeToString(digest[:])
}

// DiscoveryNegotiationV3 contains only equality guards for the one accepted
// contract family. The response supplies an authenticated context digest
// without disclosing the verified context.
type DiscoveryNegotiationV3 struct {
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

type discoveryNegotiatedWireV3 struct {
	SchemaVersion         int             `json:"schema_version"`
	TransportSchemaSHA256 string          `json:"transport_schema_sha256"`
	Request               json.RawMessage `json:"request"`
}

func EncodeDiscoveryNegotiationV3(value DiscoveryNegotiationV3) ([]byte, error) {
	if !validDiscoveryNegotiationV3(value) {
		return nil, malformedError()
	}
	return marshalBounded(value, MaxDiscoveryNegotiationBytesV3)
}

func DecodeDiscoveryNegotiationV3(body []byte) (DiscoveryNegotiationV3, error) {
	var value DiscoveryNegotiationV3
	if !decodeExact(body, MaxDiscoveryNegotiationBytesV3, &value) || !validDiscoveryNegotiationV3(value) {
		return DiscoveryNegotiationV3{}, malformedError()
	}
	return value, nil
}

func validDiscoveryNegotiationV3(value DiscoveryNegotiationV3) bool {
	return value.SchemaVersion == domain.BrokerDiscoverySchemaVersionV3 && validIdentifier(value.RequestID) &&
		value.ContractFamily == domain.BrokerContractFamilyExecutionV2 && value.Service == "jira" &&
		validIdentifier(value.BrokerID) && validIdentifier(value.Audience) && validIdentifier(value.ExecutionID) &&
		validIdentifier(value.ExecutionEpoch) && validIdentifier(value.AuthorityRevision) && value.NotAfterMillis > 0
}

func BindDiscoveryNegotiationV3(value DiscoveryNegotiationV3, verified domain.BrokerVerifiedContext, now time.Time) (domain.BrokerFamilyDiscoveryRequestV3, error) {
	digest, err := brokercontract.VerifiedContextSHA256(verified)
	if !validDiscoveryNegotiationV3(value) || err != nil {
		return domain.BrokerFamilyDiscoveryRequestV3{}, malformedError()
	}
	if value.Service != verified.Backend.Service || value.BrokerID != verified.BrokerID || value.Audience != verified.Audience ||
		value.ExecutionID != verified.ExecutionID || value.ExecutionEpoch != verified.ExecutionEpoch {
		return domain.BrokerFamilyDiscoveryRequestV3{}, discoveryRejected(domain.BrokerReasonStaleExecution)
	}
	if value.AuthorityRevision != verified.AuthorityRevision {
		return domain.BrokerFamilyDiscoveryRequestV3{}, discoveryRejected(domain.BrokerReasonStaleAuthority)
	}
	deadline := min(value.NotAfterMillis, now.UnixMilli()+domain.BrokerMaxDecisionLeaseMillis, verified.ExecutionExpiresMillis, verified.GrantExpiresMillis, verified.CredentialExpiresMillis)
	if now.UnixMilli() < verified.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || deadline <= now.UnixMilli() {
		return domain.BrokerFamilyDiscoveryRequestV3{}, discoveryRejected(domain.BrokerReasonDecisionExpired)
	}
	return domain.BrokerFamilyDiscoveryRequestV3{
		SchemaVersion: domain.BrokerDiscoverySchemaVersionV3, RequestID: value.RequestID, ContractFamily: value.ContractFamily,
		Service: value.Service, BrokerID: value.BrokerID, Audience: value.Audience, ContextSHA256: digest, NotAfterMillis: deadline,
		Expect: domain.BrokerRequestExpectations{ExecutionID: value.ExecutionID, ExecutionEpoch: value.ExecutionEpoch, AuthorityRevision: value.AuthorityRevision},
	}, nil
}

func EncodeNegotiatedDiscoveryV3(request domain.BrokerFamilyDiscoveryRequestV3) ([]byte, error) {
	body, err := brokercontract.EncodeFamilyDiscoveryRequestV3(request)
	if err != nil {
		return nil, err
	}
	return marshalBounded(discoveryNegotiatedWireV3{domain.BrokerDiscoverySchemaVersionV3, DiscoverySchemaSHA256V3(), body}, MaxDiscoveryNegotiationBytesV3)
}

func DecodeNegotiatedDiscoveryV3(body []byte, hello DiscoveryNegotiationV3, now time.Time) (domain.BrokerFamilyDiscoveryRequestV3, error) {
	var wire discoveryNegotiatedWireV3
	if !validDiscoveryNegotiationV3(hello) || !decodeExact(body, MaxDiscoveryNegotiationBytesV3, &wire) ||
		wire.SchemaVersion != domain.BrokerDiscoverySchemaVersionV3 || wire.TransportSchemaSHA256 != DiscoverySchemaSHA256V3() {
		return domain.BrokerFamilyDiscoveryRequestV3{}, malformedError()
	}
	request, err := brokercontract.DecodeFamilyDiscoveryRequestV3(wire.Request)
	if err != nil || request.RequestID != hello.RequestID || request.ContractFamily != hello.ContractFamily || request.Service != hello.Service ||
		request.BrokerID != hello.BrokerID || request.Audience != hello.Audience || request.Expect.ExecutionID != hello.ExecutionID ||
		request.Expect.ExecutionEpoch != hello.ExecutionEpoch || request.Expect.AuthorityRevision != hello.AuthorityRevision ||
		request.NotAfterMillis > hello.NotAfterMillis || request.NotAfterMillis > now.UnixMilli()+domain.BrokerMaxDecisionLeaseMillis || request.NotAfterMillis <= now.UnixMilli() {
		return domain.BrokerFamilyDiscoveryRequestV3{}, discoveryRejected(domain.BrokerReasonStaleExecution)
	}
	return request, nil
}

// EncodeDiscoveryFailureV3 keeps discovery-v3 failures independent from the
// frozen discovery-v2 and execution-v1 failure bytes.
func EncodeDiscoveryFailureV3(reason domain.BrokerReason) ([]byte, error) {
	value, err := newDiscoveryFailureV3(reason)
	if err != nil {
		return nil, err
	}
	return marshalBounded(failureWire{3, value.Status, string(value.Reason), value.Recovery, value.RetrySafe, value.Complete}, MaxTransportFailureBytes)
}

func DecodeDiscoveryFailureV3(body []byte) (Failure, error) {
	var wire failureWire
	if !decodeExact(body, MaxTransportFailureBytes, &wire) || wire.SchemaVersion != domain.BrokerDiscoverySchemaVersionV3 {
		return Failure{}, malformedError()
	}
	value := Failure{3, wire.Status, domain.BrokerReason(wire.Reason), wire.Recovery, wire.RetrySafe, wire.Complete}
	want, err := newDiscoveryFailureV3(value.Reason)
	if err != nil || !reflect.DeepEqual(value, want) {
		return Failure{}, malformedError()
	}
	return value, nil
}

func newDiscoveryFailureV3(reason domain.BrokerReason) (Failure, error) {
	ok, safe := brokercontract.ErrorForReason(reason)
	if !ok {
		return Failure{}, malformedError()
	}
	recovery := diagnostic.Recover(safe, diagnostic.OperationRead)
	return Failure{3, TransportStatusRejected, reason, string(recovery.Action), recovery.RetrySafe, true}, nil
}
