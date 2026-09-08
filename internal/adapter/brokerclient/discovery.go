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

// Discover reloads the workload session on every call and never caches a
// projection. Neither it nor negotiation can invoke a backend read.
func (c *Client) Discover(ctx context.Context, service string) (domain.BrokerDiscoveryProjectionV2, error) {
	if c == nil || c.config.Session == nil || ctx == nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(domain.ErrConfig)
	}
	if service != "jira" && service != "confluence" {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(domain.ErrUsage)
	}
	session, err := c.config.Session.Load()
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(err)
	}
	defer session.Clear()
	requestID, err := randomIdentifier()
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(domain.ErrCheckFailed)
	}
	started := time.Now()
	hello := brokertransport.DiscoveryNegotiationV2{SchemaVersion: 2, RequestID: requestID, Service: service, BrokerID: c.config.BrokerID, Audience: c.config.Audience, ExecutionID: session.ExecutionID, ExecutionEpoch: session.ExecutionEpoch, AuthorityRevision: session.AuthorityRevision, NotAfterMillis: started.UnixMilli() + domain.BrokerMaxDecisionLeaseMillis}
	body, err := brokertransport.EncodeDiscoveryNegotiationV2(hello)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(err)
	}
	budget, err := domain.NewChildReadBudget(domain.ReadBudgetFromContext(ctx), 2, brokertransport.MaxDiscoveryNegotiationBytesV2+brokercontract.MaxDiscoveryV2Bytes)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(err)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(domain.BrokerMaxDecisionLeaseMillis)*time.Millisecond)
	defer cancel()
	requestContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(bounded, budget))))
	client, err := c.newHTTPClient(session)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(domain.ErrConfig)
	}
	defer client.CloseIdleConnections()
	defer client.ClearCredential()
	response, err := client.DoBoundedResponse(requestContext, http.MethodPost, brokertransport.DiscoveryNegotiatePathV2, body, map[string]string{"Content-Type": "application/json"}, brokertransport.MaxDiscoveryNegotiationBytesV2, brokertransport.MaxTransportFailureBytes)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, closedTransportError(err)
	}
	body, err = acceptedDiscoveryBody(response)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, err
	}
	request, err := brokertransport.DecodeNegotiatedDiscoveryV2(body, hello, time.Now())
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(err)
	}
	body, err = brokercontract.EncodeDiscoveryRequestV2(request)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(err)
	}
	response, err = client.DoBoundedResponse(requestContext, http.MethodPost, brokertransport.DiscoveryPathV2, body, map[string]string{"Content-Type": "application/json"}, brokercontract.MaxDiscoveryV2Bytes, brokertransport.MaxTransportFailureBytes)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, closedTransportError(err)
	}
	body, err = acceptedDiscoveryBody(response)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, err
	}
	projection, err := brokercontract.DecodeDiscoveryProjectionV2(body)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(err)
	}
	if err := brokercontract.ValidateDiscoveryProjectionV2ForRequest(projection, request, time.Now()); err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(err)
	}
	if bounded.Err() != nil || time.Since(started) >= time.Duration(projection.ExpiresAtMillis-projection.IssuedAtMillis)*time.Millisecond {
		return domain.BrokerDiscoveryProjectionV2{}, discoveryClientError(domain.BrokerReasonDecisionExpired)
	}
	current, err := c.config.Session.Load()
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, clientError(err)
	}
	defer current.Clear()
	if current.expectations() != session.expectations() || !bytes.Equal(current.Credential, session.Credential) {
		return domain.BrokerDiscoveryProjectionV2{}, discoveryClientError(domain.BrokerReasonStaleExecution)
	}
	if bounded.Err() != nil || brokercontract.ValidateDiscoveryProjectionV2ForRequest(projection, request, time.Now()) != nil {
		return domain.BrokerDiscoveryProjectionV2{}, discoveryClientError(domain.BrokerReasonDecisionExpired)
	}
	return projection, nil
}

func acceptedDiscoveryBody(response httpx.BoundedResponse) ([]byte, error) {
	// Failures before authentication have no server correlation yet. Success
	// must carry the authenticated correlation; any present value is bounded.
	if response.CorrelationID != "" && !validCorrelation(response.CorrelationID) {
		return nil, clientError(domain.ErrCheckFailed)
	}
	if response.Status == http.StatusOK {
		if response.CorrelationID == "" {
			return nil, clientError(domain.ErrCheckFailed)
		}
		return response.Body, nil
	}
	failure, err := brokertransport.DecodeDiscoveryFailureV2(response.Body)
	if err != nil {
		return nil, clientError(domain.ErrCheckFailed)
	}
	return nil, discoveryClientError(failure.Reason)
}

func discoveryClientError(reason domain.BrokerReason) error {
	_, err := brokercontract.ErrorForReason(reason)
	return err
}
