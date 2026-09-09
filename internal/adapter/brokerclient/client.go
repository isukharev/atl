// Package brokerclient implements ATL's narrow remote Broker read adapter.
// It exposes semantic operations only; callers cannot select arbitrary routes.
package brokerclient

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type Config struct {
	BaseURL   string
	BrokerID  string
	Audience  string
	Version   string
	Session   SessionLoader
	Scheduler *httpx.Scheduler
	TLS       httpx.TLSOptions
}

type Client struct {
	config Config
	now    func() time.Time
}

func New(config Config) (*Client, error) {
	parsed, parseErr := url.Parse(config.BaseURL)
	_, originErr := backendid.OriginSHA256(config.BaseURL)
	if parseErr != nil || originErr != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" ||
		strings.TrimSpace(config.BaseURL) != config.BaseURL || strings.HasSuffix(config.BaseURL, "/") || config.Session == nil ||
		!validIdentifier(config.BrokerID, domain.BrokerMaxIdentifierBytes) || !validIdentifier(config.Audience, domain.BrokerMaxAudienceBytes) {
		return nil, clientError(domain.ErrConfig)
	}
	return &Client{config: config, now: time.Now}, nil
}

func (c *Client) ReadJiraIssue(ctx context.Context, key string, fields []domain.BrokerJiraIssueField) (domain.BrokerJiraIssueReadResult, error) {
	request, err := c.request(domain.BrokerOperationJiraIssueRead, domain.BrokerOperationArguments{JiraIssueRead: &domain.BrokerJiraIssueReadArguments{IssueKey: key, Fields: append([]domain.BrokerJiraIssueField(nil), fields...)}})
	if err != nil {
		return domain.BrokerJiraIssueReadResult{}, err
	}
	body, boundRequest, err := c.execute(ctx, request)
	if err != nil {
		return domain.BrokerJiraIssueReadResult{}, err
	}
	result, err := brokercontract.DecodeJiraIssueReadResultV1(body)
	if err != nil || brokercontract.ValidateJiraIssueReadResultForV1(result, boundRequest) != nil {
		return domain.BrokerJiraIssueReadResult{}, clientError(domain.ErrCheckFailed)
	}
	return result, nil
}

func (c *Client) ReadConfluencePage(ctx context.Context, id string, projection domain.BrokerConfluenceProjection) (domain.BrokerConfluencePageReadResult, error) {
	request, err := c.request(domain.BrokerOperationConfluencePageRead, domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: id, Projection: projection}})
	if err != nil {
		return domain.BrokerConfluencePageReadResult{}, err
	}
	body, boundRequest, err := c.execute(ctx, request)
	if err != nil {
		return domain.BrokerConfluencePageReadResult{}, err
	}
	result, err := brokercontract.DecodeConfluencePageReadResultV1(body)
	if err != nil || brokercontract.ValidateConfluencePageReadResultForV1(result, boundRequest) != nil {
		return domain.BrokerConfluencePageReadResult{}, clientError(domain.ErrCheckFailed)
	}
	return result, nil
}

func (c *Client) request(operation domain.BrokerOperationID, arguments domain.BrokerOperationArguments) (domain.BrokerRequest, error) {
	requestID, err := randomIdentifier()
	if err != nil {
		return domain.BrokerRequest{}, clientError(domain.ErrCheckFailed)
	}
	definition, ok := brokercontract.Definition(operation, brokercontract.OperationVersion)
	if !ok || !definition.Available {
		return domain.BrokerRequest{}, unsupported()
	}
	request := domain.BrokerRequest{
		SchemaVersion: brokercontract.SchemaVersion, Operation: operation, OperationVersion: definition.Version,
		RequestID: requestID, Features: append([]string{}, definition.RequiredFeatures...),
		Expect:    domain.BrokerRequestExpectations{ExecutionID: "validation", ExecutionEpoch: "validation", AuthorityRevision: "validation"},
		Arguments: arguments,
	}
	if _, err := brokercontract.EncodeRequestV1(request); err != nil {
		if ok, safe := brokercontract.ContentFreeError(err); ok {
			return domain.BrokerRequest{}, safe
		}
		return domain.BrokerRequest{}, clientError(domain.ErrUsage)
	}
	request.Expect = domain.BrokerRequestExpectations{}
	return request, nil
}

func (c *Client) execute(ctx context.Context, request domain.BrokerRequest) ([]byte, domain.BrokerRequest, error) {
	if c == nil || c.config.Session == nil {
		return nil, domain.BrokerRequest{}, clientError(domain.ErrConfig)
	}
	session, err := c.config.Session.Load()
	if err != nil {
		return nil, domain.BrokerRequest{}, clientError(err)
	}
	defer session.Clear()
	request.Expect = session.expectations()
	body, err := brokercontract.EncodeRequestV1(request)
	if err != nil {
		return nil, domain.BrokerRequest{}, clientError(domain.ErrUsage)
	}
	definition, ok := brokercontract.Definition(request.Operation, request.OperationVersion)
	if !ok || !definition.Available {
		return nil, domain.BrokerRequest{}, unsupported()
	}
	budget, err := domain.NewChildReadBudget(domain.ReadBudgetFromContext(ctx), 2, brokertransport.MaxProtocolBytes+definition.Limits.MaxResponseBytes)
	if err != nil {
		return nil, domain.BrokerRequest{}, clientError(domain.ErrCheckFailed)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(definition.Limits.MaxOperationMillis)*time.Millisecond)
	defer cancel()
	requestContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(bounded, budget))))
	httpClient, err := c.newHTTPClient(session)
	if err != nil {
		return nil, domain.BrokerRequest{}, clientError(domain.ErrConfig)
	}
	defer httpClient.CloseIdleConnections()
	defer httpClient.ClearCredential()
	protocolResponse, err := httpClient.DoBoundedResponse(requestContext, http.MethodGet, brokertransport.ProtocolPath, nil, nil, brokertransport.MaxProtocolBytes, brokertransport.MaxTransportFailureBytes)
	if err != nil {
		return nil, domain.BrokerRequest{}, closedTransportError(err)
	}
	protocolBody, err := acceptedBody(protocolResponse)
	if err != nil {
		return nil, domain.BrokerRequest{}, err
	}
	protocol, err := brokertransport.DecodeProtocolV1(protocolBody)
	if err != nil || protocol.BrokerID != c.config.BrokerID || protocol.Audience != c.config.Audience || !protocolSupports(protocol, definition) {
		return nil, domain.BrokerRequest{}, clientError(domain.ErrCheckFailed)
	}
	executeResponse, err := httpClient.DoBoundedResponse(requestContext, http.MethodPost, brokertransport.ExecutePath, body, map[string]string{"Content-Type": "application/json"}, definition.Limits.MaxResponseBytes, brokertransport.MaxTransportFailureBytes)
	if err != nil {
		return nil, domain.BrokerRequest{}, closedTransportError(err)
	}
	accepted, err := acceptedBody(executeResponse)
	return accepted, request, err
}

func (c *Client) newHTTPClient(session Session) (*httpx.Client, error) {
	return httpx.NewWithSchedulerTLS(c.config.BaseURL, string(session.Credential), c.config.Version, c.config.Scheduler, c.config.TLS, httpx.WithNoProxy())
}

func acceptedBody(response httpx.BoundedResponse) ([]byte, error) {
	if !validCorrelation(response.CorrelationID) {
		return nil, clientError(domain.ErrCheckFailed)
	}
	if response.Status == http.StatusOK {
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

func protocolSupports(protocol brokertransport.Protocol, definition domain.BrokerOperationDefinition) bool {
	for _, operation := range protocol.Operations {
		if operation.ID == definition.ID {
			return operation.Version == definition.Version && operation.MaxRequestBytes == definition.Limits.MaxRequestBytes && operation.MaxResponseBytes == definition.Limits.MaxResponseBytes && slicesEqual(operation.Features, definition.RequiredFeatures)
		}
	}
	return false
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validCorrelation(value string) bool { return validIdentifier(value, 64) }

func randomIdentifier() (string, error) {
	var value [18]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func closedTransportError(err error) error {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded, domain.ErrReadAttemptBudgetExhausted, domain.ErrReadResponseBudgetExhausted} {
		if errors.Is(err, sentinel) {
			return clientError(sentinel)
		}
	}
	return clientError(domain.ErrCheckFailed)
}
