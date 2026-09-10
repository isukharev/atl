package brokerserver

import (
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func attachmentChainDiscovery(request domain.BrokerFamilyDiscoveryAuthorizationRequestV4, now time.Time) domain.BrokerFamilyDiscoveryProjectionV4 {
	verified := request.Context
	contextSHA, _ := brokercontract.VerifiedContextSHA256(verified)
	operations := make([]domain.BrokerFamilyDiscoveryOperationV4, 0)
	for _, value := range brokercontract.AvailableDefinitionsV3() {
		definition := value.Definition
		operations = append(operations, domain.BrokerFamilyDiscoveryOperationV4{
			ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed,
			Features: definition.RequiredFeatures, Limits: definition.Limits, Effects: definition.Effects,
			MaxMetadataItems: value.MaxMetadataItems, MaxJiraAttempts: value.MaxJiraAttempts, MaxAuthenticationAttempts: value.MaxAuthenticationAttempts,
			MaxDecisionAttempts: value.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: value.MaxTotalHostOutboundAttempts,
			MaxCommandHostOutboundAttempts: value.MaxCommandHostOutboundAttempts, MaxJiraResponseBytes: value.MaxJiraResponseBytes,
			MaxAuthorityResponseBytes: value.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: value.MaxTotalHostResponseBytes,
			MaxManifestLineBytes: value.MaxManifestLineBytes, MaxDataLineBytes: value.MaxDataLineBytes,
			MaxTerminalLineBytes: value.MaxTerminalLineBytes, MaxFramedResponseBytes: value.MaxFramedResponseBytes,
		})
	}
	return domain.BrokerFamilyDiscoveryProjectionV4{
		SchemaVersion: 4, RequestID: request.Request.RequestID, RequestSHA256: request.RequestSHA256, ContextSHA256: contextSHA,
		ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, Audience: verified.Audience,
		BrokerID: verified.BrokerID, AuthorityRevision: verified.AuthorityRevision, ContractFamily: domain.BrokerContractFamilyExecutionV3,
		Service: "jira", RegistrySHA256: brokercontract.RegistrySHA256V3(), ContractSchemaSHA256: brokercontract.ExecutionSchemaSHA256V3(),
		DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V4(), IssuedAtMillis: now.UnixMilli(),
		ExpiresAtMillis: min(now.Add(4*time.Second).UnixMilli(), request.Request.NotAfterMillis), Operations: operations, Complete: true,
	}
}
