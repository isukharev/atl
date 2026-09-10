package brokerclient

import (
	"context"
	"net/http"
	"reflect"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func (c *Client) discoverFamilyV4WithSession(ctx context.Context, client *httpx.Client, session Session, started, deadline time.Time) (domain.BrokerFamilyDiscoveryProjectionV4, domain.BrokerFamilyDiscoveryRequestV4, error) {
	requestID, err := randomIdentifier()
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, clientError(domain.ErrCheckFailed)
	}
	hello := brokertransport.DiscoveryNegotiationV4{
		SchemaVersion: domain.BrokerDiscoverySchemaVersionV4, RequestID: requestID,
		ContractFamily: domain.BrokerContractFamilyExecutionV3, Service: "jira", BrokerID: c.config.BrokerID, Audience: c.config.Audience,
		ExecutionID: session.ExecutionID, ExecutionEpoch: session.ExecutionEpoch, AuthorityRevision: session.AuthorityRevision,
		NotAfterMillis: deadline.UnixMilli(),
	}
	body, err := brokertransport.EncodeDiscoveryNegotiationV4(hello)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, clientError(err)
	}
	response, err := client.DoBoundedResponse(ctx, http.MethodPost, brokertransport.DiscoveryNegotiatePathV4, body, map[string]string{"Content-Type": "application/json"}, brokertransport.MaxDiscoveryNegotiationBytesV4, brokertransport.MaxTransportFailureBytes)
	clear(body)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, closedTransportError(err)
	}
	body, err = acceptedFamilyDiscoveryBodyV4(response)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, err
	}
	request, err := brokertransport.DecodeNegotiatedDiscoveryV4(body, hello, c.now())
	clear(body)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, clientError(err)
	}
	body, err = brokercontract.EncodeFamilyDiscoveryRequestV4(request)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, clientError(err)
	}
	response, err = client.DoBoundedResponse(ctx, http.MethodPost, brokertransport.DiscoveryPathV4, body, map[string]string{"Content-Type": "application/json"}, brokercontract.MaxDiscoveryV4Bytes, brokertransport.MaxTransportFailureBytes)
	clear(body)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, closedTransportError(err)
	}
	body, err = acceptedFamilyDiscoveryBodyV4(response)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, err
	}
	projection, decodeErr := brokercontract.DecodeFamilyDiscoveryProjectionV4(body)
	clear(body)
	if decodeErr != nil || brokercontract.ValidateFamilyDiscoveryProjectionV4ForRequest(projection, request, c.now()) != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, clientError(domain.ErrCheckFailed)
	}
	if ctx.Err() != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, clientError(ctx.Err())
	}
	if !c.familyDiscoveryV4Current(projection, request, started) {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, domain.BrokerFamilyDiscoveryRequestV4{}, discoveryClientError(domain.BrokerReasonDecisionExpired)
	}
	return projection, request, nil
}

func (c *Client) familyDiscoveryV4Current(projection domain.BrokerFamilyDiscoveryProjectionV4, request domain.BrokerFamilyDiscoveryRequestV4, started time.Time) bool {
	now := c.now()
	leaseDeadline := started.Add(time.Duration(projection.ExpiresAtMillis-projection.IssuedAtMillis) * time.Millisecond)
	return now.Before(leaseDeadline) && brokercontract.ValidateFamilyDiscoveryProjectionV4ForRequest(projection, request, now) == nil
}

func acceptedFamilyDiscoveryBodyV4(response httpx.BoundedResponse) ([]byte, error) {
	if response.CorrelationID != "" && !validCorrelation(response.CorrelationID) {
		return nil, clientError(domain.ErrCheckFailed)
	}
	if response.Status == http.StatusOK {
		if response.CorrelationID == "" {
			return nil, clientError(domain.ErrCheckFailed)
		}
		return response.Body, nil
	}
	defer clear(response.Body)
	failure, err := brokertransport.DecodeDiscoveryFailureV4(response.Body)
	if err != nil {
		return nil, clientError(domain.ErrCheckFailed)
	}
	return nil, discoveryClientError(failure.Reason)
}

func requireAllowedAttachmentV3(projection domain.BrokerFamilyDiscoveryProjectionV4, definition domain.BrokerAttachmentOperationDefinitionV3) error {
	if len(projection.Operations) != 1 {
		return unsupported()
	}
	operation := projection.Operations[0]
	expected := definition.Definition
	if operation.ID != expected.ID || operation.Version != expected.Version || !operation.Supported || !slicesEqual(operation.Features, expected.RequiredFeatures) || operation.Limits != expected.Limits ||
		!reflect.DeepEqual(operation.Effects, expected.Effects) || operation.MaxMetadataItems != definition.MaxMetadataItems || operation.MaxJiraAttempts != definition.MaxJiraAttempts ||
		operation.MaxAuthenticationAttempts != definition.MaxAuthenticationAttempts || operation.MaxDecisionAttempts != definition.MaxDecisionAttempts || operation.MaxTotalHostOutboundAttempts != definition.MaxTotalHostOutboundAttempts ||
		operation.MaxCommandHostOutboundAttempts != definition.MaxCommandHostOutboundAttempts || operation.MaxJiraResponseBytes != definition.MaxJiraResponseBytes || operation.MaxAuthorityResponseBytes != definition.MaxAuthorityResponseBytes ||
		operation.MaxTotalHostResponseBytes != definition.MaxTotalHostResponseBytes || operation.MaxManifestLineBytes != definition.MaxManifestLineBytes || operation.MaxDataLineBytes != definition.MaxDataLineBytes ||
		operation.MaxTerminalLineBytes != definition.MaxTerminalLineBytes || operation.MaxFramedResponseBytes != definition.MaxFramedResponseBytes {
		return clientError(domain.ErrCheckFailed)
	}
	switch operation.Access {
	case domain.BrokerDiscoveryAccessAllowed:
		if operation.RequestAccessCorrelation != "" {
			return clientError(domain.ErrCheckFailed)
		}
		return nil
	case domain.BrokerDiscoveryAccessRequestRequired:
		if !validIdentifier(operation.RequestAccessCorrelation, 64) {
			return clientError(domain.ErrCheckFailed)
		}
		return discoveryClientError(domain.BrokerReasonDenied)
	case domain.BrokerDiscoveryAccessUnavailable:
		if operation.RequestAccessCorrelation != "" {
			return clientError(domain.ErrCheckFailed)
		}
		return unsupported()
	default:
		return clientError(domain.ErrCheckFailed)
	}
}
