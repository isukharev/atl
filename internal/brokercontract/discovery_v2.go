package brokercontract

import (
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const MaxDiscoveryV2Bytes = int64(256 << 10)

type discoveryRequestV2Wire struct {
	SchemaVersion  int              `json:"schema_version"`
	RequestID      string           `json:"request_id"`
	Service        string           `json:"service"`
	BrokerID       string           `json:"broker_id"`
	Audience       string           `json:"audience"`
	ContextSHA256  string           `json:"context_sha256"`
	NotAfterMillis int64            `json:"not_after_millis"`
	Expect         expectationsWire `json:"expect"`
}

type discoveryAuthorizationRequestV2Wire struct {
	SchemaVersion int                    `json:"schema_version"`
	Request       discoveryRequestV2Wire `json:"request"`
	Context       verifiedContextWire    `json:"context"`
	RequestSHA256 string                 `json:"request_sha256"`
}

type discoveryEffectV2Wire struct {
	Kind         string   `json:"kind"`
	ResourceKind string   `json:"resource_kind"`
	Fields       []string `json:"fields"`
}

type discoveryOperationV2Wire struct {
	ID                       string                  `json:"id"`
	Version                  int                     `json:"version"`
	Supported                bool                    `json:"supported"`
	Access                   string                  `json:"access"`
	RequestAccessCorrelation string                  `json:"request_access_correlation,omitempty"`
	Features                 []string                `json:"features"`
	Limits                   limitsWire              `json:"limits"`
	Effects                  []discoveryEffectV2Wire `json:"effects"`
}

type discoveryProjectionV2Wire struct {
	SchemaVersion         int                        `json:"schema_version"`
	RequestID             string                     `json:"request_id"`
	RequestSHA256         string                     `json:"request_sha256"`
	ContextSHA256         string                     `json:"context_sha256"`
	ExecutionID           string                     `json:"execution_id"`
	ExecutionEpoch        string                     `json:"execution_epoch"`
	Audience              string                     `json:"audience"`
	BrokerID              string                     `json:"broker_id"`
	AuthorityRevision     string                     `json:"authority_revision"`
	Service               string                     `json:"service"`
	RegistrySHA256        string                     `json:"registry_sha256"`
	ContractSchemaSHA256  string                     `json:"contract_schema_sha256"`
	DiscoverySchemaSHA256 string                     `json:"discovery_schema_sha256"`
	IssuedAtMillis        int64                      `json:"issued_at_millis"`
	ExpiresAtMillis       int64                      `json:"expires_at_millis"`
	Operations            []discoveryOperationV2Wire `json:"operations"`
	Complete              bool                       `json:"complete"`
}

func EncodeDiscoveryRequestV2(value domain.BrokerDiscoveryRequestV2) ([]byte, error) {
	if validateDiscoveryRequestV2(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalDiscoveryV2(discoveryRequestV2ToWire(value))
}

func DecodeDiscoveryRequestV2(data []byte) (domain.BrokerDiscoveryRequestV2, error) {
	var wire discoveryRequestV2Wire
	if !decodeDiscoveryV2(data, &wire) {
		return domain.BrokerDiscoveryRequestV2{}, reject(domain.BrokerReasonMalformed)
	}
	value := discoveryRequestV2FromWire(wire)
	if validateDiscoveryRequestV2(value) != nil {
		return domain.BrokerDiscoveryRequestV2{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func DiscoveryRequestSHA256V2(value domain.BrokerDiscoveryRequestV2) (string, error) {
	if validateDiscoveryRequestV2(value) != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestValueInNamespace("atl.broker.discovery.v2/", "request", discoveryRequestV2ToWire(value))
}

func EncodeDiscoveryAuthorizationRequestV2(value domain.BrokerDiscoveryAuthorizationRequestV2) ([]byte, error) {
	if validateDiscoveryAuthorizationRequestV2(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalDiscoveryV2(discoveryAuthorizationRequestV2Wire{2, discoveryRequestV2ToWire(value.Request), contextToWire(value.Context), value.RequestSHA256})
}

func DecodeDiscoveryAuthorizationRequestV2(data []byte) (domain.BrokerDiscoveryAuthorizationRequestV2, error) {
	var wire discoveryAuthorizationRequestV2Wire
	if !decodeDiscoveryV2(data, &wire) {
		return domain.BrokerDiscoveryAuthorizationRequestV2{}, reject(domain.BrokerReasonMalformed)
	}
	value := domain.BrokerDiscoveryAuthorizationRequestV2{SchemaVersion: wire.SchemaVersion, Request: discoveryRequestV2FromWire(wire.Request), Context: contextFromWire(wire.Context), RequestSHA256: wire.RequestSHA256}
	if validateDiscoveryAuthorizationRequestV2(value) != nil {
		return domain.BrokerDiscoveryAuthorizationRequestV2{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func EncodeDiscoveryProjectionV2(value domain.BrokerDiscoveryProjectionV2) ([]byte, error) {
	if validateDiscoveryProjectionV2(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalDiscoveryV2(discoveryProjectionV2ToWire(value))
}

func DecodeDiscoveryProjectionV2(data []byte) (domain.BrokerDiscoveryProjectionV2, error) {
	var wire discoveryProjectionV2Wire
	if !decodeDiscoveryV2(data, &wire) {
		return domain.BrokerDiscoveryProjectionV2{}, reject(domain.BrokerReasonMalformed)
	}
	value := discoveryProjectionV2FromWire(wire)
	if validateDiscoveryProjectionV2(value) != nil {
		return domain.BrokerDiscoveryProjectionV2{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func ValidateDiscoveryProjectionV2ForRequest(value domain.BrokerDiscoveryProjectionV2, request domain.BrokerDiscoveryRequestV2, now time.Time) error {
	requestDigest, err := DiscoveryRequestSHA256V2(request)
	if err != nil || validateDiscoveryProjectionV2(value) != nil || value.RequestID != request.RequestID || value.RequestSHA256 != requestDigest ||
		value.ExecutionID != request.Expect.ExecutionID || value.ExecutionEpoch != request.Expect.ExecutionEpoch || value.AuthorityRevision != request.Expect.AuthorityRevision || value.Service != request.Service ||
		value.BrokerID != request.BrokerID || value.Audience != request.Audience || value.ContextSHA256 != request.ContextSHA256 || value.ExpiresAtMillis > request.NotAfterMillis ||
		now.UnixMilli() < value.IssuedAtMillis-domain.BrokerClockAllowanceMillis || now.UnixMilli() >= value.ExpiresAtMillis {
		return reject(domain.BrokerReasonStaleExecution)
	}
	return nil
}

func ValidateDiscoveryProjectionV2ForContext(value domain.BrokerDiscoveryProjectionV2, request domain.BrokerDiscoveryAuthorizationRequestV2, now time.Time) error {
	contextDigest, err := VerifiedContextSHA256(request.Context)
	if err != nil || validateDiscoveryAuthorizationRequestV2(request) != nil || ValidateDiscoveryProjectionV2ForRequest(value, request.Request, now) != nil ||
		value.ContextSHA256 != contextDigest || value.Audience != request.Context.Audience || value.BrokerID != request.Context.BrokerID || value.Service != request.Context.Backend.Service ||
		now.UnixMilli() < request.Context.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || value.IssuedAtMillis < request.Context.ExecutionNotBeforeMillis ||
		value.ExpiresAtMillis > request.Context.ExecutionExpiresMillis || value.ExpiresAtMillis > request.Context.GrantExpiresMillis || value.ExpiresAtMillis > request.Context.CredentialExpiresMillis {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	return nil
}

func validateDiscoveryRequestV2(value domain.BrokerDiscoveryRequestV2) error {
	if value.SchemaVersion != 2 || !validIdentifier(value.RequestID) || !validService(value.Service) || !validIdentifier(value.BrokerID) || !validIdentifier(value.Audience) || !validDigest(value.ContextSHA256) || value.NotAfterMillis <= 0 ||
		!validIdentifier(value.Expect.ExecutionID) || !validIdentifier(value.Expect.ExecutionEpoch) || !validIdentifier(value.Expect.AuthorityRevision) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateDiscoveryAuthorizationRequestV2(value domain.BrokerDiscoveryAuthorizationRequestV2) error {
	digest, err := DiscoveryRequestSHA256V2(value.Request)
	contextDigest, contextErr := VerifiedContextSHA256(value.Context)
	if value.SchemaVersion != 2 || err != nil || contextErr != nil || validateContext(value.Context) != nil || value.Context.Backend.Service != value.Request.Service ||
		value.Context.BrokerID != value.Request.BrokerID || value.Context.Audience != value.Request.Audience || contextDigest != value.Request.ContextSHA256 ||
		value.Request.NotAfterMillis > value.Context.ExecutionExpiresMillis || value.Request.NotAfterMillis > value.Context.GrantExpiresMillis || value.Request.NotAfterMillis > value.Context.CredentialExpiresMillis ||
		value.Context.ExecutionID != value.Request.Expect.ExecutionID || value.Context.ExecutionEpoch != value.Request.Expect.ExecutionEpoch || value.Context.AuthorityRevision != value.Request.Expect.AuthorityRevision || value.RequestSHA256 != digest {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateDiscoveryProjectionV2(value domain.BrokerDiscoveryProjectionV2) error {
	if value.SchemaVersion != 2 || !validIdentifier(value.RequestID) || !validDigest(value.RequestSHA256) || !validDigest(value.ContextSHA256) ||
		!validIdentifier(value.ExecutionID) || !validIdentifier(value.ExecutionEpoch) || !validIdentifier(value.Audience) || !validIdentifier(value.BrokerID) || !validIdentifier(value.AuthorityRevision) || !validService(value.Service) ||
		value.RegistrySHA256 != RegistrySHA256() || value.ContractSchemaSHA256 != SchemaSHA256() || value.DiscoverySchemaSHA256 != DiscoverySchemaSHA256V2() ||
		value.IssuedAtMillis <= 0 || value.ExpiresAtMillis <= value.IssuedAtMillis || value.ExpiresAtMillis-value.IssuedAtMillis > domain.BrokerMaxDecisionLeaseMillis || !value.Complete {
		return reject(domain.BrokerReasonMalformed)
	}
	expected := availableDefinitionsForService(value.Service)
	if len(value.Operations) != len(expected) {
		return reject(domain.BrokerReasonMalformed)
	}
	for index, operation := range value.Operations {
		definition := expected[index]
		if operation.ID != definition.ID || operation.Version != definition.Version || !operation.Supported || !slices.Equal(operation.Features, definition.RequiredFeatures) || operation.Limits != definition.Limits || !reflect.DeepEqual(operation.Effects, definition.Effects) ||
			operation.Access != domain.BrokerDiscoveryAccessAllowed && operation.Access != domain.BrokerDiscoveryAccessRequestRequired && operation.Access != domain.BrokerDiscoveryAccessUnavailable ||
			(operation.Access == domain.BrokerDiscoveryAccessRequestRequired) != (operation.RequestAccessCorrelation != "") || operation.RequestAccessCorrelation != "" && (!validIdentifier(operation.RequestAccessCorrelation) || len(operation.RequestAccessCorrelation) > 64) {
			return reject(domain.BrokerReasonMalformed)
		}
	}
	return nil
}

func availableDefinitionsForService(service string) []domain.BrokerOperationDefinition {
	var definitions []domain.BrokerOperationDefinition
	for _, definition := range AvailableDefinitions() {
		if definition.BackendService == service {
			definitions = append(definitions, definition)
		}
	}
	return definitions
}

func discoveryRequestV2ToWire(value domain.BrokerDiscoveryRequestV2) discoveryRequestV2Wire {
	return discoveryRequestV2Wire{value.SchemaVersion, value.RequestID, value.Service, value.BrokerID, value.Audience, value.ContextSHA256, value.NotAfterMillis, expectationsWire{value.Expect.ExecutionID, value.Expect.ExecutionEpoch, value.Expect.AuthorityRevision}}
}

func discoveryRequestV2FromWire(wire discoveryRequestV2Wire) domain.BrokerDiscoveryRequestV2 {
	return domain.BrokerDiscoveryRequestV2{SchemaVersion: wire.SchemaVersion, RequestID: wire.RequestID, Service: wire.Service, BrokerID: wire.BrokerID, Audience: wire.Audience, ContextSHA256: wire.ContextSHA256, NotAfterMillis: wire.NotAfterMillis, Expect: domain.BrokerRequestExpectations{ExecutionID: wire.Expect.ExecutionID, ExecutionEpoch: wire.Expect.ExecutionEpoch, AuthorityRevision: wire.Expect.AuthorityRevision}}
}

func discoveryProjectionV2ToWire(value domain.BrokerDiscoveryProjectionV2) discoveryProjectionV2Wire {
	operations := make([]discoveryOperationV2Wire, len(value.Operations))
	for index, operation := range value.Operations {
		effects := make([]discoveryEffectV2Wire, len(operation.Effects))
		for effectIndex, effect := range operation.Effects {
			effects[effectIndex] = discoveryEffectV2Wire{string(effect.Kind), string(effect.ResourceKind), wireStrings(effect.Fields)}
		}
		operations[index] = discoveryOperationV2Wire{string(operation.ID), operation.Version, operation.Supported, string(operation.Access), operation.RequestAccessCorrelation, wireStrings(operation.Features), limitsToWire(operation.Limits), effects}
	}
	return discoveryProjectionV2Wire{value.SchemaVersion, value.RequestID, value.RequestSHA256, value.ContextSHA256, value.ExecutionID, value.ExecutionEpoch, value.Audience, value.BrokerID, value.AuthorityRevision, value.Service, value.RegistrySHA256, value.ContractSchemaSHA256, value.DiscoverySchemaSHA256, value.IssuedAtMillis, value.ExpiresAtMillis, operations, value.Complete}
}

func discoveryProjectionV2FromWire(wire discoveryProjectionV2Wire) domain.BrokerDiscoveryProjectionV2 {
	operations := make([]domain.BrokerDiscoveryOperationV2, len(wire.Operations))
	for index, operation := range wire.Operations {
		effects := make([]domain.BrokerEffectDefinition, len(operation.Effects))
		for effectIndex, effect := range operation.Effects {
			effects[effectIndex] = domain.BrokerEffectDefinition{Kind: domain.BrokerEffectKind(effect.Kind), ResourceKind: domain.BrokerResourceKind(effect.ResourceKind), Fields: copyStrings(effect.Fields)}
		}
		operations[index] = domain.BrokerDiscoveryOperationV2{ID: domain.BrokerOperationID(operation.ID), Version: operation.Version, Supported: operation.Supported, Access: domain.BrokerDiscoveryAccess(operation.Access), RequestAccessCorrelation: operation.RequestAccessCorrelation, Features: copyStrings(operation.Features), Limits: limitsFromWire(operation.Limits), Effects: effects}
	}
	return domain.BrokerDiscoveryProjectionV2{SchemaVersion: wire.SchemaVersion, RequestID: wire.RequestID, RequestSHA256: wire.RequestSHA256, ContextSHA256: wire.ContextSHA256, ExecutionID: wire.ExecutionID, ExecutionEpoch: wire.ExecutionEpoch, Audience: wire.Audience, BrokerID: wire.BrokerID, AuthorityRevision: wire.AuthorityRevision, Service: wire.Service, RegistrySHA256: wire.RegistrySHA256, ContractSchemaSHA256: wire.ContractSchemaSHA256, DiscoverySchemaSHA256: wire.DiscoverySchemaSHA256, IssuedAtMillis: wire.IssuedAtMillis, ExpiresAtMillis: wire.ExpiresAtMillis, Operations: operations, Complete: wire.Complete}
}

func decodeDiscoveryV2(data []byte, target any) bool {
	return len(data) > 0 && int64(len(data)) <= MaxDiscoveryV2Bytes && strictjson.DecodeExact(data, MaxCanonicalDepth, target) == nil
}

func marshalDiscoveryV2(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || int64(len(encoded)) > MaxDiscoveryV2Bytes {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return encoded, nil
}
