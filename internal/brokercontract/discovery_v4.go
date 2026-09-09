package brokercontract

import (
	"reflect"
	"slices"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const MaxDiscoveryV4Bytes = int64(256 << 10)

type familyDiscoveryOperationWireV4 struct {
	ID                             string                  `json:"id"`
	Version                        int                     `json:"version"`
	Supported                      bool                    `json:"supported"`
	Access                         string                  `json:"access"`
	RequestAccessCorrelation       string                  `json:"request_access_correlation,omitempty"`
	Features                       []string                `json:"features"`
	Limits                         limitsWire              `json:"limits"`
	Effects                        []discoveryEffectV2Wire `json:"effects"`
	MaxMetadataItems               int                     `json:"max_metadata_items"`
	MaxJiraAttempts                int                     `json:"max_jira_attempts"`
	MaxAuthenticationAttempts      int                     `json:"max_authentication_attempts"`
	MaxDecisionAttempts            int                     `json:"max_decision_attempts"`
	MaxTotalHostOutboundAttempts   int                     `json:"max_total_host_outbound_attempts"`
	MaxCommandHostOutboundAttempts int                     `json:"max_command_host_outbound_attempts"`
	MaxJiraResponseBytes           int64                   `json:"max_jira_response_bytes"`
	MaxAuthorityResponseBytes      int64                   `json:"max_authority_response_bytes"`
	MaxTotalHostResponseBytes      int64                   `json:"max_total_host_response_bytes"`
	MaxManifestLineBytes           int64                   `json:"max_manifest_line_bytes"`
	MaxDataLineBytes               int64                   `json:"max_data_line_bytes"`
	MaxTerminalLineBytes           int64                   `json:"max_terminal_line_bytes"`
	MaxFramedResponseBytes         int64                   `json:"max_framed_response_bytes"`
}

type familyDiscoveryProjectionWireV4 struct {
	SchemaVersion         int                              `json:"schema_version"`
	RequestID             string                           `json:"request_id"`
	RequestSHA256         string                           `json:"request_sha256"`
	ContextSHA256         string                           `json:"context_sha256"`
	ExecutionID           string                           `json:"execution_id"`
	ExecutionEpoch        string                           `json:"execution_epoch"`
	Audience              string                           `json:"audience"`
	BrokerID              string                           `json:"broker_id"`
	AuthorityRevision     string                           `json:"authority_revision"`
	ContractFamily        string                           `json:"contract_family"`
	Service               string                           `json:"service"`
	RegistrySHA256        string                           `json:"registry_sha256"`
	ContractSchemaSHA256  string                           `json:"contract_schema_sha256"`
	DiscoverySchemaSHA256 string                           `json:"discovery_schema_sha256"`
	IssuedAtMillis        int64                            `json:"issued_at_millis"`
	ExpiresAtMillis       int64                            `json:"expires_at_millis"`
	Operations            []familyDiscoveryOperationWireV4 `json:"operations"`
	Complete              bool                             `json:"complete"`
}

func EncodeFamilyDiscoveryRequestV4(value domain.BrokerFamilyDiscoveryRequestV4) ([]byte, error) {
	if validateFamilyDiscoveryRequestV4(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalAttachmentV3(familyDiscoveryRequestToWireV4(value), MaxDiscoveryV4Bytes)
}

func DecodeFamilyDiscoveryRequestV4(data []byte) (domain.BrokerFamilyDiscoveryRequestV4, error) {
	var wire familyDiscoveryRequestV3Wire
	if !decodeAttachmentV3(data, MaxDiscoveryV4Bytes, &wire) {
		return domain.BrokerFamilyDiscoveryRequestV4{}, reject(domain.BrokerReasonMalformed)
	}
	value := familyDiscoveryRequestFromWireV4(wire)
	if validateFamilyDiscoveryRequestV4(value) != nil {
		return domain.BrokerFamilyDiscoveryRequestV4{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func FamilyDiscoveryRequestSHA256V4(value domain.BrokerFamilyDiscoveryRequestV4) (string, error) {
	if validateFamilyDiscoveryRequestV4(value) != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestValueInNamespace("atl.broker.discovery.v4/", "request", familyDiscoveryRequestToWireV4(value))
}

func EncodeFamilyDiscoveryAuthorizationRequestV4(value domain.BrokerFamilyDiscoveryAuthorizationRequestV4) ([]byte, error) {
	if validateFamilyDiscoveryAuthorizationRequestV4(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	wire := familyDiscoveryAuthorizationRequestV3Wire{SchemaVersion: domain.BrokerDiscoverySchemaVersionV4, Request: familyDiscoveryRequestToWireV4(value.Request), Context: contextToWire(value.Context), RequestSHA256: value.RequestSHA256}
	return marshalAttachmentV3(wire, MaxDiscoveryV4Bytes)
}

func DecodeFamilyDiscoveryAuthorizationRequestV4(data []byte) (domain.BrokerFamilyDiscoveryAuthorizationRequestV4, error) {
	var wire familyDiscoveryAuthorizationRequestV3Wire
	if !decodeAttachmentV3(data, MaxDiscoveryV4Bytes, &wire) {
		return domain.BrokerFamilyDiscoveryAuthorizationRequestV4{}, reject(domain.BrokerReasonMalformed)
	}
	value := domain.BrokerFamilyDiscoveryAuthorizationRequestV4{SchemaVersion: wire.SchemaVersion, Request: familyDiscoveryRequestFromWireV4(wire.Request), Context: contextFromWire(wire.Context), RequestSHA256: wire.RequestSHA256}
	if validateFamilyDiscoveryAuthorizationRequestV4(value) != nil {
		return domain.BrokerFamilyDiscoveryAuthorizationRequestV4{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func EncodeFamilyDiscoveryProjectionV4(value domain.BrokerFamilyDiscoveryProjectionV4) ([]byte, error) {
	if validateFamilyDiscoveryProjectionV4(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalAttachmentV3(familyDiscoveryProjectionToWireV4(value), MaxDiscoveryV4Bytes)
}

func DecodeFamilyDiscoveryProjectionV4(data []byte) (domain.BrokerFamilyDiscoveryProjectionV4, error) {
	var wire familyDiscoveryProjectionWireV4
	if !decodeAttachmentV3(data, MaxDiscoveryV4Bytes, &wire) || wire.Operations == nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, reject(domain.BrokerReasonMalformed)
	}
	value := familyDiscoveryProjectionFromWireV4(wire)
	if validateFamilyDiscoveryProjectionV4(value) != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func ValidateFamilyDiscoveryProjectionV4ForRequest(value domain.BrokerFamilyDiscoveryProjectionV4, request domain.BrokerFamilyDiscoveryRequestV4, now time.Time) error {
	digest, err := FamilyDiscoveryRequestSHA256V4(request)
	if err != nil || validateFamilyDiscoveryProjectionV4(value) != nil || value.RequestID != request.RequestID || value.RequestSHA256 != digest || value.ContextSHA256 != request.ContextSHA256 ||
		value.ExecutionID != request.Expect.ExecutionID || value.ExecutionEpoch != request.Expect.ExecutionEpoch || value.AuthorityRevision != request.Expect.AuthorityRevision ||
		value.ContractFamily != request.ContractFamily || value.Service != request.Service || value.BrokerID != request.BrokerID || value.Audience != request.Audience || value.ExpiresAtMillis > request.NotAfterMillis ||
		now.UnixMilli() < value.IssuedAtMillis-domain.BrokerClockAllowanceMillis || now.UnixMilli() >= value.ExpiresAtMillis {
		return reject(domain.BrokerReasonStaleExecution)
	}
	return nil
}

func ValidateFamilyDiscoveryProjectionV4ForContext(value domain.BrokerFamilyDiscoveryProjectionV4, request domain.BrokerFamilyDiscoveryAuthorizationRequestV4, now time.Time) error {
	contextDigest, err := VerifiedContextSHA256(request.Context)
	if err != nil || validateFamilyDiscoveryAuthorizationRequestV4(request) != nil || ValidateFamilyDiscoveryProjectionV4ForRequest(value, request.Request, now) != nil || value.ContextSHA256 != contextDigest ||
		value.Audience != request.Context.Audience || value.BrokerID != request.Context.BrokerID || value.Service != request.Context.Backend.Service ||
		now.UnixMilli() < request.Context.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || value.IssuedAtMillis < request.Context.ExecutionNotBeforeMillis ||
		value.ExpiresAtMillis > request.Context.ExecutionExpiresMillis || value.ExpiresAtMillis > request.Context.GrantExpiresMillis || value.ExpiresAtMillis > request.Context.CredentialExpiresMillis {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	return nil
}

func validateFamilyDiscoveryRequestV4(value domain.BrokerFamilyDiscoveryRequestV4) error {
	if value.SchemaVersion != domain.BrokerDiscoverySchemaVersionV4 || !validIdentifier(value.RequestID) || value.ContractFamily != domain.BrokerContractFamilyExecutionV3 || value.Service != "jira" ||
		!validIdentifier(value.BrokerID) || !validIdentifier(value.Audience) || !validDigest(value.ContextSHA256) || value.NotAfterMillis <= 0 ||
		!validIdentifier(value.Expect.ExecutionID) || !validIdentifier(value.Expect.ExecutionEpoch) || !validIdentifier(value.Expect.AuthorityRevision) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateFamilyDiscoveryAuthorizationRequestV4(value domain.BrokerFamilyDiscoveryAuthorizationRequestV4) error {
	digest, digestErr := FamilyDiscoveryRequestSHA256V4(value.Request)
	contextDigest, contextErr := VerifiedContextSHA256(value.Context)
	if value.SchemaVersion != domain.BrokerDiscoverySchemaVersionV4 || digestErr != nil || contextErr != nil || validateContext(value.Context) != nil || value.Context.Backend.Service != value.Request.Service ||
		value.Context.BrokerID != value.Request.BrokerID || value.Context.Audience != value.Request.Audience || contextDigest != value.Request.ContextSHA256 ||
		value.Request.NotAfterMillis > value.Context.ExecutionExpiresMillis || value.Request.NotAfterMillis > value.Context.GrantExpiresMillis || value.Request.NotAfterMillis > value.Context.CredentialExpiresMillis ||
		value.Context.ExecutionID != value.Request.Expect.ExecutionID || value.Context.ExecutionEpoch != value.Request.Expect.ExecutionEpoch || value.Context.AuthorityRevision != value.Request.Expect.AuthorityRevision || value.RequestSHA256 != digest {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateFamilyDiscoveryProjectionV4(value domain.BrokerFamilyDiscoveryProjectionV4) error {
	if value.SchemaVersion != domain.BrokerDiscoverySchemaVersionV4 || !validIdentifier(value.RequestID) || !validDigest(value.RequestSHA256) || !validDigest(value.ContextSHA256) || !validIdentifier(value.ExecutionID) || !validIdentifier(value.ExecutionEpoch) ||
		!validIdentifier(value.Audience) || !validIdentifier(value.BrokerID) || !validIdentifier(value.AuthorityRevision) || value.ContractFamily != domain.BrokerContractFamilyExecutionV3 || value.Service != "jira" ||
		value.RegistrySHA256 != RegistrySHA256V3() || value.ContractSchemaSHA256 != ExecutionSchemaSHA256V3() || value.DiscoverySchemaSHA256 != DiscoverySchemaSHA256V4() ||
		value.IssuedAtMillis <= 0 || value.ExpiresAtMillis <= value.IssuedAtMillis || value.ExpiresAtMillis-value.IssuedAtMillis > domain.BrokerMaxDecisionLeaseMillis || value.Operations == nil || !value.Complete {
		return reject(domain.BrokerReasonMalformed)
	}
	expected := AvailableDefinitionsV3()
	if len(value.Operations) != len(expected) {
		return reject(domain.BrokerReasonMalformed)
	}
	for index, operation := range value.Operations {
		if !validFamilyDiscoveryOperationV4(operation, expected[index]) {
			return reject(domain.BrokerReasonMalformed)
		}
	}
	return nil
}

func validFamilyDiscoveryOperationV4(value domain.BrokerFamilyDiscoveryOperationV4, expected domain.BrokerAttachmentOperationDefinitionV3) bool {
	definition := expected.Definition
	accessValid := value.Access == domain.BrokerDiscoveryAccessAllowed || value.Access == domain.BrokerDiscoveryAccessRequestRequired || value.Access == domain.BrokerDiscoveryAccessUnavailable
	correlationValid := (value.Access == domain.BrokerDiscoveryAccessRequestRequired) == (value.RequestAccessCorrelation != "") && (value.RequestAccessCorrelation == "" || validIdentifier(value.RequestAccessCorrelation) && len(value.RequestAccessCorrelation) <= 64)
	return value.ID == definition.ID && value.Version == definition.Version && value.Supported && accessValid && correlationValid && slices.Equal(value.Features, definition.RequiredFeatures) && value.Limits == definition.Limits && reflect.DeepEqual(value.Effects, definition.Effects) && value.MaxMetadataItems == expected.MaxMetadataItems &&
		value.MaxJiraAttempts == expected.MaxJiraAttempts && value.MaxAuthenticationAttempts == expected.MaxAuthenticationAttempts && value.MaxDecisionAttempts == expected.MaxDecisionAttempts && value.MaxTotalHostOutboundAttempts == expected.MaxTotalHostOutboundAttempts && value.MaxCommandHostOutboundAttempts == expected.MaxCommandHostOutboundAttempts &&
		value.MaxJiraResponseBytes == expected.MaxJiraResponseBytes && value.MaxAuthorityResponseBytes == expected.MaxAuthorityResponseBytes && value.MaxTotalHostResponseBytes == expected.MaxTotalHostResponseBytes &&
		value.MaxManifestLineBytes == expected.MaxManifestLineBytes && value.MaxDataLineBytes == expected.MaxDataLineBytes && value.MaxTerminalLineBytes == expected.MaxTerminalLineBytes && value.MaxFramedResponseBytes == expected.MaxFramedResponseBytes
}

func familyDiscoveryRequestToWireV4(value domain.BrokerFamilyDiscoveryRequestV4) familyDiscoveryRequestV3Wire {
	return familyDiscoveryRequestV3Wire{value.SchemaVersion, value.RequestID, value.ContractFamily, value.Service, value.BrokerID, value.Audience, value.ContextSHA256, value.NotAfterMillis, expectationsWire{value.Expect.ExecutionID, value.Expect.ExecutionEpoch, value.Expect.AuthorityRevision}}
}

func familyDiscoveryRequestFromWireV4(wire familyDiscoveryRequestV3Wire) domain.BrokerFamilyDiscoveryRequestV4 {
	return domain.BrokerFamilyDiscoveryRequestV4{SchemaVersion: wire.SchemaVersion, RequestID: wire.RequestID, ContractFamily: wire.ContractFamily, Service: wire.Service, BrokerID: wire.BrokerID, Audience: wire.Audience, ContextSHA256: wire.ContextSHA256, NotAfterMillis: wire.NotAfterMillis, Expect: domain.BrokerRequestExpectations{ExecutionID: wire.Expect.ExecutionID, ExecutionEpoch: wire.Expect.ExecutionEpoch, AuthorityRevision: wire.Expect.AuthorityRevision}}
}

func familyDiscoveryProjectionToWireV4(value domain.BrokerFamilyDiscoveryProjectionV4) familyDiscoveryProjectionWireV4 {
	operations := make([]familyDiscoveryOperationWireV4, len(value.Operations))
	for index, operation := range value.Operations {
		effects := make([]discoveryEffectV2Wire, len(operation.Effects))
		for effectIndex, effect := range operation.Effects {
			effects[effectIndex] = discoveryEffectV2Wire{string(effect.Kind), string(effect.ResourceKind), wireStrings(effect.Fields)}
		}
		operations[index] = familyDiscoveryOperationWireV4{string(operation.ID), operation.Version, operation.Supported, string(operation.Access), operation.RequestAccessCorrelation, wireStrings(operation.Features), limitsToWire(operation.Limits), effects, operation.MaxMetadataItems, operation.MaxJiraAttempts, operation.MaxAuthenticationAttempts, operation.MaxDecisionAttempts, operation.MaxTotalHostOutboundAttempts, operation.MaxCommandHostOutboundAttempts, operation.MaxJiraResponseBytes, operation.MaxAuthorityResponseBytes, operation.MaxTotalHostResponseBytes, operation.MaxManifestLineBytes, operation.MaxDataLineBytes, operation.MaxTerminalLineBytes, operation.MaxFramedResponseBytes}
	}
	return familyDiscoveryProjectionWireV4{value.SchemaVersion, value.RequestID, value.RequestSHA256, value.ContextSHA256, value.ExecutionID, value.ExecutionEpoch, value.Audience, value.BrokerID, value.AuthorityRevision, value.ContractFamily, value.Service, value.RegistrySHA256, value.ContractSchemaSHA256, value.DiscoverySchemaSHA256, value.IssuedAtMillis, value.ExpiresAtMillis, operations, value.Complete}
}

func familyDiscoveryProjectionFromWireV4(wire familyDiscoveryProjectionWireV4) domain.BrokerFamilyDiscoveryProjectionV4 {
	operations := make([]domain.BrokerFamilyDiscoveryOperationV4, len(wire.Operations))
	for index, operation := range wire.Operations {
		effects := make([]domain.BrokerEffectDefinition, len(operation.Effects))
		for effectIndex, effect := range operation.Effects {
			effects[effectIndex] = domain.BrokerEffectDefinition{Kind: domain.BrokerEffectKind(effect.Kind), ResourceKind: domain.BrokerResourceKind(effect.ResourceKind), Fields: copyStrings(effect.Fields)}
		}
		operations[index] = domain.BrokerFamilyDiscoveryOperationV4{ID: domain.BrokerOperationID(operation.ID), Version: operation.Version, Supported: operation.Supported, Access: domain.BrokerDiscoveryAccess(operation.Access), RequestAccessCorrelation: operation.RequestAccessCorrelation, Features: copyStrings(operation.Features), Limits: limitsFromWire(operation.Limits), Effects: effects, MaxMetadataItems: operation.MaxMetadataItems, MaxJiraAttempts: operation.MaxJiraAttempts, MaxAuthenticationAttempts: operation.MaxAuthenticationAttempts, MaxDecisionAttempts: operation.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: operation.MaxTotalHostOutboundAttempts, MaxCommandHostOutboundAttempts: operation.MaxCommandHostOutboundAttempts, MaxJiraResponseBytes: operation.MaxJiraResponseBytes, MaxAuthorityResponseBytes: operation.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: operation.MaxTotalHostResponseBytes, MaxManifestLineBytes: operation.MaxManifestLineBytes, MaxDataLineBytes: operation.MaxDataLineBytes, MaxTerminalLineBytes: operation.MaxTerminalLineBytes, MaxFramedResponseBytes: operation.MaxFramedResponseBytes}
	}
	return domain.BrokerFamilyDiscoveryProjectionV4{SchemaVersion: wire.SchemaVersion, RequestID: wire.RequestID, RequestSHA256: wire.RequestSHA256, ContextSHA256: wire.ContextSHA256, ExecutionID: wire.ExecutionID, ExecutionEpoch: wire.ExecutionEpoch, Audience: wire.Audience, BrokerID: wire.BrokerID, AuthorityRevision: wire.AuthorityRevision, ContractFamily: wire.ContractFamily, Service: wire.Service, RegistrySHA256: wire.RegistrySHA256, ContractSchemaSHA256: wire.ContractSchemaSHA256, DiscoverySchemaSHA256: wire.DiscoverySchemaSHA256, IssuedAtMillis: wire.IssuedAtMillis, ExpiresAtMillis: wire.ExpiresAtMillis, Operations: operations, Complete: wire.Complete}
}
