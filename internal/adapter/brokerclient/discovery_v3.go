package brokerclient

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

var _ domain.BrokerFamilyDiscoveryReaderV3 = (*Client)(nil)

// DiscoverFamily reads a fresh advisory projection for the one fixed
// execution-v2 family. The projection is not an execution grant.
func (c *Client) DiscoverFamily(ctx context.Context, service, contractFamily string) (domain.BrokerFamilyDiscoveryProjectionV3, error) {
	if c == nil || c.config.Session == nil || c.now == nil || ctx == nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, clientError(domain.ErrConfig)
	}
	if service != "jira" || contractFamily != domain.BrokerContractFamilyExecutionV2 {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, clientError(domain.ErrUsage)
	}
	started := c.now()
	deadline := brokerClientDeadline(started, ctx, time.Duration(domain.BrokerMaxDecisionLeaseMillis)*time.Millisecond)
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	budget, err := domain.NewChildReadBudget(domain.ReadBudgetFromContext(ctx), 2, brokertransport.MaxDiscoveryNegotiationBytesV3+brokercontract.MaxDiscoveryV3Bytes)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, clientError(domain.ErrCheckFailed)
	}
	requestContext := brokerClientReadContext(bounded, budget)
	session, err := c.config.Session.Load()
	if err != nil {
		session.Clear()
		return domain.BrokerFamilyDiscoveryProjectionV3{}, clientError(err)
	}
	defer session.Clear()
	if bounded.Err() != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, clientError(bounded.Err())
	}
	httpClient, err := c.newHTTPClient(session)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, clientError(domain.ErrConfig)
	}
	defer httpClient.CloseIdleConnections()
	defer httpClient.ClearCredential()

	projection, request, err := c.discoverFamilyWithSession(requestContext, httpClient, session, service, contractFamily, started, deadline)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, err
	}
	current, loadErr := c.config.Session.Load()
	if loadErr != nil {
		current.Clear()
		return domain.BrokerFamilyDiscoveryProjectionV3{}, clientError(loadErr)
	}
	unchanged := sameBrokerSession(session, current)
	current.Clear()
	if !unchanged {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, discoveryClientError(domain.BrokerReasonStaleExecution)
	}
	if bounded.Err() != nil || !c.familyDiscoveryCurrent(projection, request, started) {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, discoveryClientError(domain.BrokerReasonDecisionExpired)
	}
	return projection, nil
}

func (c *Client) discoverFamilyWithSession(ctx context.Context, httpClient *httpx.Client, session Session, service, contractFamily string, started, deadline time.Time) (domain.BrokerFamilyDiscoveryProjectionV3, domain.BrokerFamilyDiscoveryRequestV3, error) {
	requestID, err := randomIdentifier()
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, clientError(domain.ErrCheckFailed)
	}
	hello := brokertransport.DiscoveryNegotiationV3{
		SchemaVersion: domain.BrokerDiscoverySchemaVersionV3, RequestID: requestID,
		ContractFamily: contractFamily, Service: service, BrokerID: c.config.BrokerID, Audience: c.config.Audience,
		ExecutionID: session.ExecutionID, ExecutionEpoch: session.ExecutionEpoch, AuthorityRevision: session.AuthorityRevision,
		NotAfterMillis: deadline.UnixMilli(),
	}
	body, err := brokertransport.EncodeDiscoveryNegotiationV3(hello)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, clientError(err)
	}
	response, err := httpClient.DoBoundedResponse(ctx, http.MethodPost, brokertransport.DiscoveryNegotiatePathV3, body, map[string]string{"Content-Type": "application/json"}, brokertransport.MaxDiscoveryNegotiationBytesV3, brokertransport.MaxTransportFailureBytes)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, closedTransportError(err)
	}
	body, err = acceptedFamilyDiscoveryBody(response)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, err
	}
	request, err := brokertransport.DecodeNegotiatedDiscoveryV3(body, hello, c.now())
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, clientError(err)
	}
	body, err = brokercontract.EncodeFamilyDiscoveryRequestV3(request)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, clientError(err)
	}
	response, err = httpClient.DoBoundedResponse(ctx, http.MethodPost, brokertransport.DiscoveryPathV3, body, map[string]string{"Content-Type": "application/json"}, brokercontract.MaxDiscoveryV3Bytes, brokertransport.MaxTransportFailureBytes)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, closedTransportError(err)
	}
	body, err = acceptedFamilyDiscoveryBody(response)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, err
	}
	projection, err := brokercontract.DecodeFamilyDiscoveryProjectionV3(body)
	if err != nil || brokercontract.ValidateFamilyDiscoveryProjectionV3ForRequest(projection, request, c.now()) != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, clientError(domain.ErrCheckFailed)
	}
	if ctx.Err() != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, clientError(ctx.Err())
	}
	if !c.familyDiscoveryCurrent(projection, request, started) {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.BrokerFamilyDiscoveryRequestV3{}, discoveryClientError(domain.BrokerReasonDecisionExpired)
	}
	return projection, request, nil
}

func (c *Client) familyDiscoveryCurrent(projection domain.BrokerFamilyDiscoveryProjectionV3, request domain.BrokerFamilyDiscoveryRequestV3, started time.Time) bool {
	now := c.now()
	leaseDeadline := started.Add(time.Duration(projection.ExpiresAtMillis-projection.IssuedAtMillis) * time.Millisecond)
	return now.Before(leaseDeadline) && brokercontract.ValidateFamilyDiscoveryProjectionV3ForRequest(projection, request, now) == nil
}

func acceptedFamilyDiscoveryBody(response httpx.BoundedResponse) ([]byte, error) {
	// Failures before authentication need not have a correlation id. Every
	// successful response is authenticated and must carry one.
	if response.CorrelationID != "" && !validCorrelation(response.CorrelationID) {
		return nil, clientError(domain.ErrCheckFailed)
	}
	if response.Status == http.StatusOK {
		if response.CorrelationID == "" {
			return nil, clientError(domain.ErrCheckFailed)
		}
		return response.Body, nil
	}
	failure, err := brokertransport.DecodeDiscoveryFailureV3(response.Body)
	if err != nil {
		return nil, clientError(domain.ErrCheckFailed)
	}
	return nil, discoveryClientError(failure.Reason)
}

func sameBrokerSession(left, right Session) bool {
	return left.expectations() == right.expectations() && bytes.Equal(left.Credential, right.Credential)
}

func brokerClientReadContext(ctx context.Context, budget *domain.ReadBudget) context.Context {
	return domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(ctx, budget))))
}

func brokerClientDeadline(started time.Time, parent context.Context, maximum time.Duration) time.Time {
	deadline := started.Add(maximum)
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = started.Add(parentDeadline.Sub(started))
	}
	return deadline
}
