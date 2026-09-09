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

// ReadJiraProjectIssuePage executes the sole bounded execution-v2 operation.
// Discovery is a fresh compatibility and availability check, not an
// authorization grant; the Broker performs authorization during execution.
func (c *Client) ReadJiraProjectIssuePage(ctx context.Context, arguments domain.BrokerProjectPageArguments) (domain.BrokerJiraProjectPageResultV2, error) {
	if c == nil || c.config.Session == nil || c.now == nil || ctx == nil {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(domain.ErrConfig)
	}
	definition, ok := brokercontract.DefinitionV2(domain.BrokerOperationJiraProjectIssuePageRead, brokercontract.ProjectPageOperationVersion)
	validation := projectPageRequest(arguments, definition.Definition, "validation", Session{
		ExecutionID: "validation", ExecutionEpoch: "validation", AuthorityRevision: "validation",
	})
	if !ok {
		return domain.BrokerJiraProjectPageResultV2{}, unsupported()
	}
	if _, err := brokercontract.EncodeProjectPageRequestV2(validation); err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(domain.ErrUsage)
	}
	// The production registry is the only availability gate. Tests do not
	// bypass it, so an incomplete runtime cannot contact the Broker.
	if !definition.Definition.Available {
		return domain.BrokerJiraProjectPageResultV2{}, unsupported()
	}

	started := c.now()
	deadline := brokerClientDeadline(started, ctx, time.Duration(definition.Definition.Limits.MaxOperationMillis)*time.Millisecond)
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	budget, err := domain.NewChildReadBudget(domain.ReadBudgetFromContext(ctx), 3, brokertransport.MaxDiscoveryNegotiationBytesV3+brokercontract.MaxDiscoveryV3Bytes+definition.Definition.Limits.MaxResponseBytes)
	if err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(domain.ErrCheckFailed)
	}
	requestContext := brokerClientReadContext(bounded, budget)
	session, err := c.config.Session.Load()
	if err != nil {
		session.Clear()
		return domain.BrokerJiraProjectPageResultV2{}, clientError(err)
	}
	defer session.Clear()
	if bounded.Err() != nil {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(bounded.Err())
	}
	requestID, err := randomIdentifier()
	if err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(domain.ErrCheckFailed)
	}
	request := projectPageRequest(arguments, definition.Definition, requestID, session)
	requestBody, err := brokercontract.EncodeProjectPageRequestV2(request)
	if err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(domain.ErrUsage)
	}
	httpClient, err := c.newHTTPClient(session)
	if err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(domain.ErrConfig)
	}
	defer httpClient.CloseIdleConnections()
	defer httpClient.ClearCredential()

	discoveryDeadline := brokerClientDeadline(started, bounded, time.Duration(definition.Definition.Limits.MaxDecisionLeaseMillis)*time.Millisecond)
	discoveryContext, stopDiscovery := context.WithDeadline(requestContext, discoveryDeadline)
	projection, discoveryRequest, err := c.discoverFamilyWithSession(discoveryContext, httpClient, session, "jira", domain.BrokerContractFamilyExecutionV2, started, discoveryDeadline)
	stopDiscovery()
	if err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, err
	}
	if err := requireAllowedProjectPage(projection, definition.Definition); err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, err
	}
	if bounded.Err() != nil {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(bounded.Err())
	}
	if err := c.requireCurrentSession(session); err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, err
	}
	if bounded.Err() != nil || !c.familyDiscoveryCurrent(projection, discoveryRequest, started) {
		return domain.BrokerJiraProjectPageResultV2{}, discoveryClientError(domain.BrokerReasonDecisionExpired)
	}

	response, err := httpClient.DoBoundedResponse(requestContext, http.MethodPost, brokertransport.ExecutePathV2, requestBody, map[string]string{"Content-Type": "application/json"}, definition.Definition.Limits.MaxResponseBytes, brokertransport.MaxTransportFailureBytes)
	if err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, closedTransportError(err)
	}
	body, err := acceptedProjectPageBody(response)
	if err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, err
	}
	result, decodeErr := brokercontract.DecodeJiraProjectPageResultV2(body)
	if decodeErr != nil || brokercontract.ValidateJiraProjectPageResultForRequestV2(result, request) != nil {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(domain.ErrCheckFailed)
	}
	if err := c.requireCurrentSession(session); err != nil {
		return domain.BrokerJiraProjectPageResultV2{}, err
	}
	now := c.now()
	if bounded.Err() != nil || !now.Before(deadline) {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(context.DeadlineExceeded)
	}
	return result, nil
}

// ReadJiraProjectIssuePage forwards the optional semantic page reader without
// widening Jira's general Tracker search surface.
func (j *Jira) ReadJiraProjectIssuePage(ctx context.Context, arguments domain.BrokerProjectPageArguments) (domain.BrokerJiraProjectPageResultV2, error) {
	if j == nil || j.client == nil {
		return domain.BrokerJiraProjectPageResultV2{}, clientError(domain.ErrConfig)
	}
	return j.client.ReadJiraProjectIssuePage(ctx, arguments)
}

func projectPageRequest(arguments domain.BrokerProjectPageArguments, definition domain.BrokerOperationDefinition, requestID string, session Session) domain.BrokerProjectPageRequestV2 {
	return domain.BrokerProjectPageRequestV2{
		SchemaVersion: brokercontract.ExecutionSchemaVersionV2, Operation: domain.BrokerOperationJiraProjectIssuePageRead,
		OperationVersion: definition.Version, RequestID: requestID, Features: append([]string{}, definition.RequiredFeatures...),
		Expect: session.expectations(), Arguments: domain.BrokerProjectPageArguments{
			ProjectKey: arguments.ProjectKey, Fields: append([]domain.BrokerProjectPageField{}, arguments.Fields...),
			StartAt: arguments.StartAt, MaxResults: arguments.MaxResults,
		},
	}
}

func requireAllowedProjectPage(projection domain.BrokerFamilyDiscoveryProjectionV3, definition domain.BrokerOperationDefinition) error {
	if len(projection.Operations) != 1 {
		return unsupported()
	}
	operation := projection.Operations[0]
	if operation.ID != definition.ID || operation.Version != definition.Version || !operation.Supported ||
		!slicesEqual(operation.Features, definition.RequiredFeatures) || operation.Limits != definition.Limits ||
		!reflect.DeepEqual(operation.Effects, definition.Effects) {
		return clientError(domain.ErrCheckFailed)
	}
	switch operation.Access {
	case domain.BrokerDiscoveryAccessAllowed:
		if operation.RequestAccessCorrelation != "" {
			return clientError(domain.ErrCheckFailed)
		}
		return nil
	case domain.BrokerDiscoveryAccessRequestRequired:
		return discoveryClientError(domain.BrokerReasonDenied)
	case domain.BrokerDiscoveryAccessUnavailable:
		return unsupported()
	default:
		return clientError(domain.ErrCheckFailed)
	}
}

func (c *Client) requireCurrentSession(selected Session) error {
	current, err := c.config.Session.Load()
	if err != nil {
		current.Clear()
		return clientError(err)
	}
	unchanged := sameBrokerSession(selected, current)
	current.Clear()
	if !unchanged {
		return discoveryClientError(domain.BrokerReasonStaleExecution)
	}
	return nil
}

func acceptedProjectPageBody(response httpx.BoundedResponse) ([]byte, error) {
	if !validCorrelation(response.CorrelationID) {
		return nil, clientError(domain.ErrCheckFailed)
	}
	if response.Status == http.StatusOK {
		return response.Body, nil
	}
	failure, err := brokertransport.DecodeExecutionFailureV2(response.Body)
	if err != nil {
		return nil, clientError(domain.ErrCheckFailed)
	}
	_, safe := brokercontract.ErrorForReason(failure.Reason)
	if safe == nil {
		return nil, clientError(domain.ErrCheckFailed)
	}
	return nil, safe
}
