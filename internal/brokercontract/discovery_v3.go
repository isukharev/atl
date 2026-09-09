package brokercontract

import (
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const MaxDiscoveryV3Bytes = int64(256 << 10)

type familyDiscoveryRequestV3Wire struct {
	SchemaVersion  int              `json:"schema_version"`
	RequestID      string           `json:"request_id"`
	ContractFamily string           `json:"contract_family"`
	Service        string           `json:"service"`
	BrokerID       string           `json:"broker_id"`
	Audience       string           `json:"audience"`
	ContextSHA256  string           `json:"context_sha256"`
	NotAfterMillis int64            `json:"not_after_millis"`
	Expect         expectationsWire `json:"expect"`
}

type familyDiscoveryAuthorizationRequestV3Wire struct {
	SchemaVersion int                          `json:"schema_version"`
	Request       familyDiscoveryRequestV3Wire `json:"request"`
	Context       verifiedContextWire          `json:"context"`
	RequestSHA256 string                       `json:"request_sha256"`
}

type familyDiscoveryProjectionV3Wire struct {
	SchemaVersion         int                        `json:"schema_version"`
	RequestID             string                     `json:"request_id"`
	RequestSHA256         string                     `json:"request_sha256"`
	ContextSHA256         string                     `json:"context_sha256"`
	ExecutionID           string                     `json:"execution_id"`
	ExecutionEpoch        string                     `json:"execution_epoch"`
	Audience              string                     `json:"audience"`
	BrokerID              string                     `json:"broker_id"`
	AuthorityRevision     string                     `json:"authority_revision"`
	ContractFamily        string                     `json:"contract_family"`
	Service               string                     `json:"service"`
	RegistrySHA256        string                     `json:"registry_sha256"`
	ContractSchemaSHA256  string                     `json:"contract_schema_sha256"`
	DiscoverySchemaSHA256 string                     `json:"discovery_schema_sha256"`
	IssuedAtMillis        int64                      `json:"issued_at_millis"`
	ExpiresAtMillis       int64                      `json:"expires_at_millis"`
	Operations            []discoveryOperationV2Wire `json:"operations"`
	Complete              bool                       `json:"complete"`
}

func EncodeFamilyDiscoveryRequestV3(value domain.BrokerFamilyDiscoveryRequestV3) ([]byte, error) {
	if validateFamilyDiscoveryRequestV3(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalDiscoveryV3(familyDiscoveryRequestV3ToWire(value))
}

func DecodeFamilyDiscoveryRequestV3(data []byte) (domain.BrokerFamilyDiscoveryRequestV3, error) {
	var wire familyDiscoveryRequestV3Wire
	if !decodeDiscoveryV3(data, &wire) {
		return domain.BrokerFamilyDiscoveryRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	value := familyDiscoveryRequestV3FromWire(wire)
	if validateFamilyDiscoveryRequestV3(value) != nil {
		return domain.BrokerFamilyDiscoveryRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func FamilyDiscoveryRequestSHA256V3(value domain.BrokerFamilyDiscoveryRequestV3) (string, error) {
	if validateFamilyDiscoveryRequestV3(value) != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestValueInNamespace("atl.broker.discovery.v3/", "request", familyDiscoveryRequestV3ToWire(value))
}

func EncodeFamilyDiscoveryAuthorizationRequestV3(value domain.BrokerFamilyDiscoveryAuthorizationRequestV3) ([]byte, error) {
	if validateFamilyDiscoveryAuthorizationRequestV3(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalDiscoveryV3(familyDiscoveryAuthorizationRequestV3Wire{
		SchemaVersion: 3,
		Request:       familyDiscoveryRequestV3ToWire(value.Request),
		Context:       contextToWire(value.Context),
		RequestSHA256: value.RequestSHA256,
	})
}

func DecodeFamilyDiscoveryAuthorizationRequestV3(data []byte) (domain.BrokerFamilyDiscoveryAuthorizationRequestV3, error) {
	var wire familyDiscoveryAuthorizationRequestV3Wire
	if !decodeDiscoveryV3(data, &wire) {
		return domain.BrokerFamilyDiscoveryAuthorizationRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	value := domain.BrokerFamilyDiscoveryAuthorizationRequestV3{
		SchemaVersion: wire.SchemaVersion,
		Request:       familyDiscoveryRequestV3FromWire(wire.Request),
		Context:       contextFromWire(wire.Context),
		RequestSHA256: wire.RequestSHA256,
	}
	if validateFamilyDiscoveryAuthorizationRequestV3(value) != nil {
		return domain.BrokerFamilyDiscoveryAuthorizationRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func EncodeFamilyDiscoveryProjectionV3(value domain.BrokerFamilyDiscoveryProjectionV3) ([]byte, error) {
	if validateFamilyDiscoveryProjectionV3(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalDiscoveryV3(familyDiscoveryProjectionV3ToWire(value))
}

func DecodeFamilyDiscoveryProjectionV3(data []byte) (domain.BrokerFamilyDiscoveryProjectionV3, error) {
	var wire familyDiscoveryProjectionV3Wire
	if !decodeDiscoveryV3(data, &wire) || wire.Operations == nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, reject(domain.BrokerReasonMalformed)
	}
	value := familyDiscoveryProjectionV3FromWire(wire)
	if validateFamilyDiscoveryProjectionV3(value) != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func ValidateFamilyDiscoveryProjectionV3ForRequest(value domain.BrokerFamilyDiscoveryProjectionV3, request domain.BrokerFamilyDiscoveryRequestV3, now time.Time) error {
	requestDigest, err := FamilyDiscoveryRequestSHA256V3(request)
	if err != nil || validateFamilyDiscoveryProjectionV3(value) != nil || value.RequestID != request.RequestID || value.RequestSHA256 != requestDigest ||
		value.ExecutionID != request.Expect.ExecutionID || value.ExecutionEpoch != request.Expect.ExecutionEpoch || value.AuthorityRevision != request.Expect.AuthorityRevision ||
		value.ContractFamily != request.ContractFamily || value.Service != request.Service || value.BrokerID != request.BrokerID || value.Audience != request.Audience ||
		value.ContextSHA256 != request.ContextSHA256 || value.ExpiresAtMillis > request.NotAfterMillis ||
		now.UnixMilli() < value.IssuedAtMillis-domain.BrokerClockAllowanceMillis || now.UnixMilli() >= value.ExpiresAtMillis {
		return reject(domain.BrokerReasonStaleExecution)
	}
	return nil
}

func ValidateFamilyDiscoveryProjectionV3ForContext(value domain.BrokerFamilyDiscoveryProjectionV3, request domain.BrokerFamilyDiscoveryAuthorizationRequestV3, now time.Time) error {
	contextDigest, err := VerifiedContextSHA256(request.Context)
	if err != nil || validateFamilyDiscoveryAuthorizationRequestV3(request) != nil || ValidateFamilyDiscoveryProjectionV3ForRequest(value, request.Request, now) != nil ||
		value.ContextSHA256 != contextDigest || value.Audience != request.Context.Audience || value.BrokerID != request.Context.BrokerID || value.Service != request.Context.Backend.Service ||
		now.UnixMilli() < request.Context.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || value.IssuedAtMillis < request.Context.ExecutionNotBeforeMillis ||
		value.ExpiresAtMillis > request.Context.ExecutionExpiresMillis || value.ExpiresAtMillis > request.Context.GrantExpiresMillis || value.ExpiresAtMillis > request.Context.CredentialExpiresMillis {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	return nil
}

func validateFamilyDiscoveryRequestV3(value domain.BrokerFamilyDiscoveryRequestV3) error {
	if value.SchemaVersion != domain.BrokerDiscoverySchemaVersionV3 || !validIdentifier(value.RequestID) ||
		value.ContractFamily != domain.BrokerContractFamilyExecutionV2 || value.Service != "jira" ||
		!validIdentifier(value.BrokerID) || !validIdentifier(value.Audience) || !validDigest(value.ContextSHA256) || value.NotAfterMillis <= 0 ||
		!validIdentifier(value.Expect.ExecutionID) || !validIdentifier(value.Expect.ExecutionEpoch) || !validIdentifier(value.Expect.AuthorityRevision) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateFamilyDiscoveryAuthorizationRequestV3(value domain.BrokerFamilyDiscoveryAuthorizationRequestV3) error {
	digest, err := FamilyDiscoveryRequestSHA256V3(value.Request)
	contextDigest, contextErr := VerifiedContextSHA256(value.Context)
	if value.SchemaVersion != domain.BrokerDiscoverySchemaVersionV3 || err != nil || contextErr != nil || validateContext(value.Context) != nil ||
		value.Context.Backend.Service != value.Request.Service || value.Context.BrokerID != value.Request.BrokerID || value.Context.Audience != value.Request.Audience ||
		contextDigest != value.Request.ContextSHA256 || value.Request.NotAfterMillis > value.Context.ExecutionExpiresMillis ||
		value.Request.NotAfterMillis > value.Context.GrantExpiresMillis || value.Request.NotAfterMillis > value.Context.CredentialExpiresMillis ||
		value.Context.ExecutionID != value.Request.Expect.ExecutionID || value.Context.ExecutionEpoch != value.Request.Expect.ExecutionEpoch ||
		value.Context.AuthorityRevision != value.Request.Expect.AuthorityRevision || value.RequestSHA256 != digest {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateFamilyDiscoveryProjectionV3(value domain.BrokerFamilyDiscoveryProjectionV3) error {
	if value.SchemaVersion != domain.BrokerDiscoverySchemaVersionV3 || !validIdentifier(value.RequestID) || !validDigest(value.RequestSHA256) || !validDigest(value.ContextSHA256) ||
		!validIdentifier(value.ExecutionID) || !validIdentifier(value.ExecutionEpoch) || !validIdentifier(value.Audience) || !validIdentifier(value.BrokerID) || !validIdentifier(value.AuthorityRevision) ||
		value.ContractFamily != domain.BrokerContractFamilyExecutionV2 || value.Service != "jira" || value.RegistrySHA256 != RegistrySHA256V2() ||
		value.ContractSchemaSHA256 != ExecutionSchemaSHA256V2() || value.DiscoverySchemaSHA256 != DiscoverySchemaSHA256V3() ||
		value.IssuedAtMillis <= 0 || value.ExpiresAtMillis <= value.IssuedAtMillis || value.ExpiresAtMillis-value.IssuedAtMillis > domain.BrokerMaxDecisionLeaseMillis || value.Operations == nil || !value.Complete {
		return reject(domain.BrokerReasonMalformed)
	}
	expected := AvailableDefinitionsV2()
	if len(value.Operations) != len(expected) {
		return reject(domain.BrokerReasonMalformed)
	}
	for index, operation := range value.Operations {
		definition := expected[index].Definition
		if !validFamilyDiscoveryOperationV3(operation, definition) {
			return reject(domain.BrokerReasonMalformed)
		}
	}
	return nil
}

func validFamilyDiscoveryOperationV3(operation domain.BrokerFamilyDiscoveryOperationV3, definition domain.BrokerOperationDefinition) bool {
	accessValid := operation.Access == domain.BrokerDiscoveryAccessAllowed || operation.Access == domain.BrokerDiscoveryAccessRequestRequired || operation.Access == domain.BrokerDiscoveryAccessUnavailable
	correlationValid := (operation.Access == domain.BrokerDiscoveryAccessRequestRequired) == (operation.RequestAccessCorrelation != "") &&
		(operation.RequestAccessCorrelation == "" || validIdentifier(operation.RequestAccessCorrelation) && len(operation.RequestAccessCorrelation) <= 64)
	return operation.ID == definition.ID && operation.Version == definition.Version && operation.Supported && accessValid && correlationValid &&
		slices.Equal(operation.Features, definition.RequiredFeatures) && operation.Limits == definition.Limits && reflect.DeepEqual(operation.Effects, definition.Effects)
}

func familyDiscoveryRequestV3ToWire(value domain.BrokerFamilyDiscoveryRequestV3) familyDiscoveryRequestV3Wire {
	return familyDiscoveryRequestV3Wire{
		value.SchemaVersion, value.RequestID, value.ContractFamily, value.Service, value.BrokerID, value.Audience,
		value.ContextSHA256, value.NotAfterMillis,
		expectationsWire{value.Expect.ExecutionID, value.Expect.ExecutionEpoch, value.Expect.AuthorityRevision},
	}
}

func familyDiscoveryRequestV3FromWire(wire familyDiscoveryRequestV3Wire) domain.BrokerFamilyDiscoveryRequestV3 {
	return domain.BrokerFamilyDiscoveryRequestV3{
		SchemaVersion: wire.SchemaVersion, RequestID: wire.RequestID, ContractFamily: wire.ContractFamily, Service: wire.Service,
		BrokerID: wire.BrokerID, Audience: wire.Audience, ContextSHA256: wire.ContextSHA256, NotAfterMillis: wire.NotAfterMillis,
		Expect: domain.BrokerRequestExpectations{ExecutionID: wire.Expect.ExecutionID, ExecutionEpoch: wire.Expect.ExecutionEpoch, AuthorityRevision: wire.Expect.AuthorityRevision},
	}
}

func familyDiscoveryProjectionV3ToWire(value domain.BrokerFamilyDiscoveryProjectionV3) familyDiscoveryProjectionV3Wire {
	operations := make([]discoveryOperationV2Wire, len(value.Operations))
	for index, operation := range value.Operations {
		effects := make([]discoveryEffectV2Wire, len(operation.Effects))
		for effectIndex, effect := range operation.Effects {
			effects[effectIndex] = discoveryEffectV2Wire{string(effect.Kind), string(effect.ResourceKind), wireStrings(effect.Fields)}
		}
		operations[index] = discoveryOperationV2Wire{string(operation.ID), operation.Version, operation.Supported, string(operation.Access), operation.RequestAccessCorrelation, wireStrings(operation.Features), limitsToWire(operation.Limits), effects}
	}
	return familyDiscoveryProjectionV3Wire{
		value.SchemaVersion, value.RequestID, value.RequestSHA256, value.ContextSHA256, value.ExecutionID, value.ExecutionEpoch,
		value.Audience, value.BrokerID, value.AuthorityRevision, value.ContractFamily, value.Service, value.RegistrySHA256,
		value.ContractSchemaSHA256, value.DiscoverySchemaSHA256, value.IssuedAtMillis, value.ExpiresAtMillis, operations, value.Complete,
	}
}

func familyDiscoveryProjectionV3FromWire(wire familyDiscoveryProjectionV3Wire) domain.BrokerFamilyDiscoveryProjectionV3 {
	operations := make([]domain.BrokerFamilyDiscoveryOperationV3, len(wire.Operations))
	for index, operation := range wire.Operations {
		effects := make([]domain.BrokerEffectDefinition, len(operation.Effects))
		for effectIndex, effect := range operation.Effects {
			effects[effectIndex] = domain.BrokerEffectDefinition{Kind: domain.BrokerEffectKind(effect.Kind), ResourceKind: domain.BrokerResourceKind(effect.ResourceKind), Fields: copyStrings(effect.Fields)}
		}
		operations[index] = domain.BrokerFamilyDiscoveryOperationV3{
			ID: domain.BrokerOperationID(operation.ID), Version: operation.Version, Supported: operation.Supported,
			Access: domain.BrokerDiscoveryAccess(operation.Access), RequestAccessCorrelation: operation.RequestAccessCorrelation,
			Features: copyStrings(operation.Features), Limits: limitsFromWire(operation.Limits), Effects: effects,
		}
	}
	return domain.BrokerFamilyDiscoveryProjectionV3{
		SchemaVersion: wire.SchemaVersion, RequestID: wire.RequestID, RequestSHA256: wire.RequestSHA256, ContextSHA256: wire.ContextSHA256,
		ExecutionID: wire.ExecutionID, ExecutionEpoch: wire.ExecutionEpoch, Audience: wire.Audience, BrokerID: wire.BrokerID,
		AuthorityRevision: wire.AuthorityRevision, ContractFamily: wire.ContractFamily, Service: wire.Service,
		RegistrySHA256: wire.RegistrySHA256, ContractSchemaSHA256: wire.ContractSchemaSHA256, DiscoverySchemaSHA256: wire.DiscoverySchemaSHA256,
		IssuedAtMillis: wire.IssuedAtMillis, ExpiresAtMillis: wire.ExpiresAtMillis, Operations: operations, Complete: wire.Complete,
	}
}

func decodeDiscoveryV3(data []byte, target any) bool {
	return len(data) > 0 && int64(len(data)) <= MaxDiscoveryV3Bytes && strictjson.DecodeExact(data, MaxCanonicalDepth, target) == nil
}

func marshalDiscoveryV3(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || int64(len(encoded)) > MaxDiscoveryV3Bytes {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return encoded, nil
}
