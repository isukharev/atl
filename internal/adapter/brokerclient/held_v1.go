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

// heldV1Invocation owns one selected workload session from fresh discovery
// through the single execute attempt. It is intentionally limited to the
// guarded-comment and durable-outcome v1 clients.
type heldV1Invocation struct {
	client           *Client
	definition       domain.BrokerOperationDefinition
	selected         Session
	http             *httpx.Client
	ctx              context.Context
	cancel           context.CancelFunc
	started          time.Time
	deadline         time.Time
	projection       domain.BrokerDiscoveryProjectionV2
	discoveryRequest domain.BrokerDiscoveryRequestV2
}

func (c *Client) beginHeldV1(ctx context.Context, definition domain.BrokerOperationDefinition, readIntent bool) (*heldV1Invocation, error) {
	if c == nil || c.config.Session == nil || c.now == nil || ctx == nil {
		return nil, clientError(domain.ErrConfig)
	}
	started := c.now()
	deadline := brokerClientDeadline(started, ctx, time.Duration(definition.Limits.MaxOperationMillis)*time.Millisecond)
	bounded, cancel := context.WithDeadline(ctx, deadline)
	budget, err := domain.NewChildReadBudget(
		domain.ReadBudgetFromContext(ctx), 3,
		brokertransport.MaxDiscoveryNegotiationBytesV2+brokercontract.MaxDiscoveryV2Bytes+definition.Limits.MaxResponseBytes,
	)
	if err != nil {
		cancel()
		return nil, clientError(domain.ErrCheckFailed)
	}
	requestContext := heldV1RequestContext(bounded, budget, readIntent)
	selected, err := c.config.Session.Load()
	if err != nil {
		cancel()
		selected.Clear()
		return nil, clientError(err)
	}
	if bounded.Err() != nil || !c.now().Before(deadline) {
		selected.Clear()
		cancel()
		return nil, clientError(context.DeadlineExceeded)
	}
	httpClient, err := c.newHTTPClient(selected)
	if err != nil {
		selected.Clear()
		cancel()
		return nil, clientError(domain.ErrConfig)
	}
	return &heldV1Invocation{
		client: c, definition: definition, selected: selected, http: httpClient,
		ctx: requestContext, cancel: cancel, started: started, deadline: deadline,
	}, nil
}

func heldV1RequestContext(ctx context.Context, budget *domain.ReadBudget, readIntent bool) context.Context {
	requestContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget)))
	if readIntent {
		requestContext = domain.WithReadIntent(requestContext)
	}
	return requestContext
}

func (invocation *heldV1Invocation) close() {
	if invocation == nil {
		return
	}
	if invocation.http != nil {
		invocation.http.CloseIdleConnections()
		invocation.http.ClearCredential()
	}
	invocation.selected.Clear()
	if invocation.cancel != nil {
		invocation.cancel()
	}
}

func (invocation *heldV1Invocation) discover() error {
	decisionDeadline := brokerClientDeadline(
		invocation.started, invocation.ctx,
		time.Duration(invocation.definition.Limits.MaxDecisionLeaseMillis)*time.Millisecond,
	)
	discoveryContext, cancel := context.WithDeadline(invocation.ctx, decisionDeadline)
	projection, request, err := invocation.client.discoverV2WithSession(
		discoveryContext, invocation.http, invocation.selected,
		invocation.definition.BackendService, invocation.started, decisionDeadline,
	)
	cancel()
	if err != nil {
		return err
	}
	if err := requireAllowedV1Operation(projection, invocation.definition); err != nil {
		return err
	}
	invocation.projection = projection
	invocation.discoveryRequest = request
	return nil
}

// execute performs the sole mutation-capable transport attempt. dispatched is
// true once the execute call begins, so apply callers can conservatively mark
// every later failure ambiguous.
func (invocation *heldV1Invocation) execute(request domain.BrokerRequest) ([]byte, bool, error) {
	request.Expect = invocation.selected.expectations()
	requestBody, err := brokercontract.EncodeRequestV1(request)
	if err != nil {
		return nil, false, clientError(domain.ErrUsage)
	}
	defer clear(requestBody)
	if err := invocation.client.requireCurrentSession(invocation.selected); err != nil {
		return nil, false, err
	}
	if invocation.ctx.Err() != nil || !invocation.client.now().Before(invocation.deadline) {
		return nil, false, clientError(context.DeadlineExceeded)
	}
	if !invocation.client.discoveryV2Current(invocation.projection, invocation.discoveryRequest, invocation.started) {
		return nil, false, discoveryClientError(domain.BrokerReasonDecisionExpired)
	}
	response, err := invocation.http.DoBoundedResponse(
		invocation.ctx, http.MethodPost, brokertransport.ExecutePath, requestBody,
		map[string]string{"Content-Type": "application/json"},
		invocation.definition.Limits.MaxResponseBytes, brokertransport.MaxTransportFailureBytes,
	)
	if err != nil {
		return nil, true, closedTransportError(err)
	}
	if err := invocation.client.requireCurrentSession(invocation.selected); err != nil {
		return nil, true, err
	}
	if invocation.ctx.Err() != nil || !invocation.client.now().Before(invocation.deadline) {
		return nil, true, clientError(context.DeadlineExceeded)
	}
	body, err := acceptedHeldV1Body(response)
	return body, true, err
}

func acceptedHeldV1Body(response httpx.BoundedResponse) ([]byte, error) {
	// Authentication has not necessarily minted a correlation when the host
	// refuses admission. Any correlation that is present remains closed, while
	// authenticated success must carry one.
	if response.CorrelationID != "" && !validCorrelation(response.CorrelationID) {
		return nil, clientError(domain.ErrCheckFailed)
	}
	if response.Status == http.StatusOK {
		if response.CorrelationID == "" {
			return nil, clientError(domain.ErrCheckFailed)
		}
		return response.Body, nil
	}
	failure, err := brokertransport.DecodeFailureV1(response.Body)
	if err != nil {
		return nil, clientError(domain.ErrCheckFailed)
	}
	_, safe := brokercontract.ErrorForReason(failure.Reason)
	if safe == nil {
		return nil, clientError(domain.ErrCheckFailed)
	}
	return nil, safe
}

func (invocation *heldV1Invocation) stillCurrent() error {
	if invocation.ctx.Err() != nil || !invocation.client.now().Before(invocation.deadline) {
		return clientError(context.DeadlineExceeded)
	}
	return nil
}

func (c *Client) discoverV2WithSession(ctx context.Context, client *httpx.Client, session Session, service string, started, deadline time.Time) (domain.BrokerDiscoveryProjectionV2, domain.BrokerDiscoveryRequestV2, error) {
	requestID, err := randomIdentifier()
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, domain.BrokerDiscoveryRequestV2{}, clientError(domain.ErrCheckFailed)
	}
	hello := brokertransport.DiscoveryNegotiationV2{
		SchemaVersion: 2, RequestID: requestID, Service: service,
		BrokerID: c.config.BrokerID, Audience: c.config.Audience,
		ExecutionID: session.ExecutionID, ExecutionEpoch: session.ExecutionEpoch,
		AuthorityRevision: session.AuthorityRevision, NotAfterMillis: deadline.UnixMilli(),
	}
	body, err := brokertransport.EncodeDiscoveryNegotiationV2(hello)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, domain.BrokerDiscoveryRequestV2{}, clientError(err)
	}
	response, err := client.DoBoundedResponse(ctx, http.MethodPost, brokertransport.DiscoveryNegotiatePathV2, body, map[string]string{"Content-Type": "application/json"}, brokertransport.MaxDiscoveryNegotiationBytesV2, brokertransport.MaxTransportFailureBytes)
	clear(body)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, domain.BrokerDiscoveryRequestV2{}, closedTransportError(err)
	}
	body, err = acceptedDiscoveryBody(response)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, domain.BrokerDiscoveryRequestV2{}, err
	}
	request, err := brokertransport.DecodeNegotiatedDiscoveryV2(body, hello, c.now())
	clear(body)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, domain.BrokerDiscoveryRequestV2{}, clientError(err)
	}
	body, err = brokercontract.EncodeDiscoveryRequestV2(request)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, domain.BrokerDiscoveryRequestV2{}, clientError(err)
	}
	response, err = client.DoBoundedResponse(ctx, http.MethodPost, brokertransport.DiscoveryPathV2, body, map[string]string{"Content-Type": "application/json"}, brokercontract.MaxDiscoveryV2Bytes, brokertransport.MaxTransportFailureBytes)
	clear(body)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, domain.BrokerDiscoveryRequestV2{}, closedTransportError(err)
	}
	body, err = acceptedDiscoveryBody(response)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, domain.BrokerDiscoveryRequestV2{}, err
	}
	projection, err := brokercontract.DecodeDiscoveryProjectionV2(body)
	clear(body)
	if err != nil || brokercontract.ValidateDiscoveryProjectionV2ForRequest(projection, request, c.now()) != nil {
		return domain.BrokerDiscoveryProjectionV2{}, domain.BrokerDiscoveryRequestV2{}, clientError(domain.ErrCheckFailed)
	}
	if ctx.Err() != nil || !c.discoveryV2Current(projection, request, started) {
		return domain.BrokerDiscoveryProjectionV2{}, domain.BrokerDiscoveryRequestV2{}, discoveryClientError(domain.BrokerReasonDecisionExpired)
	}
	return projection, request, nil
}

func (c *Client) discoveryV2Current(projection domain.BrokerDiscoveryProjectionV2, request domain.BrokerDiscoveryRequestV2, started time.Time) bool {
	now := c.now()
	leaseDeadline := started.Add(time.Duration(projection.ExpiresAtMillis-projection.IssuedAtMillis) * time.Millisecond)
	return now.Before(leaseDeadline) && brokercontract.ValidateDiscoveryProjectionV2ForRequest(projection, request, now) == nil
}

func requireAllowedV1Operation(projection domain.BrokerDiscoveryProjectionV2, definition domain.BrokerOperationDefinition) error {
	found := false
	for _, operation := range projection.Operations {
		if operation.ID != definition.ID {
			continue
		}
		if found || operation.Version != definition.Version || !operation.Supported ||
			!slicesEqual(operation.Features, definition.RequiredFeatures) || operation.Limits != definition.Limits ||
			!reflect.DeepEqual(operation.Effects, definition.Effects) {
			return clientError(domain.ErrCheckFailed)
		}
		found = true
		correlationPresent := operation.RequestAccessCorrelation != ""
		if correlationPresent != (operation.Access == domain.BrokerDiscoveryAccessRequestRequired) ||
			correlationPresent && !validIdentifier(operation.RequestAccessCorrelation, 64) {
			return clientError(domain.ErrCheckFailed)
		}
		switch operation.Access {
		case domain.BrokerDiscoveryAccessAllowed:
			if operation.RequestAccessCorrelation != "" {
				return clientError(domain.ErrCheckFailed)
			}
		case domain.BrokerDiscoveryAccessRequestRequired:
			return discoveryClientError(domain.BrokerReasonDenied)
		case domain.BrokerDiscoveryAccessUnavailable:
			return unsupported()
		default:
			return clientError(domain.ErrCheckFailed)
		}
	}
	if !found {
		return unsupported()
	}
	return nil
}
