package brokerclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type heldAttachmentInvocation struct {
	client           *Client
	definition       domain.BrokerAttachmentOperationDefinitionV3
	selected         Session
	http             *httpx.Client
	ctx              context.Context
	cancel           context.CancelFunc
	started          time.Time
	deadline         time.Time
	projection       domain.BrokerFamilyDiscoveryProjectionV4
	discoveryRequest domain.BrokerFamilyDiscoveryRequestV4
}

func (*heldAttachmentInvocation) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "Broker held attachment invocation")
}

func (c *Client) downloadJiraAttachment(ctx context.Context, key, attachmentID string) (io.ReadCloser, string, error) {
	if c == nil || c.config.Session == nil || c.now == nil || ctx == nil {
		return nil, "", clientError(domain.ErrConfig)
	}
	definition, ok := brokercontract.DefinitionV3(domain.BrokerOperationJiraAttachmentDownload, brokercontract.AttachmentOperationVersionV3)
	validation := attachmentRequestV3(definition, "validation", Session{
		ExecutionID: "validation", ExecutionEpoch: "validation", AuthorityRevision: "validation",
	}, key, attachmentID)
	if !ok {
		return nil, "", unsupported()
	}
	if _, err := brokercontract.EncodeAttachmentRequestV3(validation); err != nil {
		return nil, "", clientError(domain.ErrUsage)
	}
	// Availability is the production gate. Parser and lifecycle tests exercise
	// private constructors, never a configurable bypass around this check.
	if !definition.Definition.Available {
		return nil, "", unsupported()
	}
	return c.downloadAvailableJiraAttachment(ctx, definition, key, attachmentID)
}

func (c *Client) downloadAvailableJiraAttachment(ctx context.Context, definition domain.BrokerAttachmentOperationDefinitionV3, key, attachmentID string) (io.ReadCloser, string, error) {
	invocation, err := c.beginHeldAttachment(ctx, definition)
	if err != nil {
		return nil, "", err
	}
	transferred := false
	defer func() {
		if !transferred {
			invocation.close()
		}
	}()
	if err := invocation.discover(); err != nil {
		return nil, "", err
	}
	if err := requireAllowedAttachmentV3(invocation.projection, definition); err != nil {
		return nil, "", err
	}
	if err := invocation.client.requireCurrentSession(invocation.selected); err != nil {
		return nil, "", err
	}
	if invocation.ctx.Err() != nil || !invocation.client.now().Before(invocation.deadline) || !invocation.client.familyDiscoveryV4Current(invocation.projection, invocation.discoveryRequest, invocation.started) {
		return nil, "", clientError(context.DeadlineExceeded)
	}
	requestID, err := randomIdentifier()
	if err != nil {
		return nil, "", clientError(domain.ErrCheckFailed)
	}
	request := attachmentRequestV3(definition, requestID, invocation.selected, key, attachmentID)
	body, err := brokercontract.EncodeAttachmentRequestV3(request)
	if err != nil {
		return nil, "", clientError(domain.ErrUsage)
	}
	defer clear(body)
	response, err := invocation.http.PostBoundedResponseStream(
		invocation.ctx, brokertransport.ExecutePathV3, body,
		definition.MaxFramedResponseBytes, brokertransport.MaxTransportFailureBytes,
	)
	if err != nil {
		return nil, "", closedTransportError(err)
	}
	if response.Status != http.StatusOK {
		return nil, "", acceptedAttachmentFailureV3(response)
	}
	if !validAttachmentSuccessResponseV3(response) {
		_ = response.Body.Close()
		return nil, "", clientError(domain.ErrCheckFailed)
	}
	reader, filename, err := newHeldAttachmentReader(invocation, response, request)
	if err != nil {
		return nil, "", err
	}
	transferred = true
	return reader, filename, nil
}

func validAttachmentSuccessResponseV3(response httpx.BoundedResponseStream) bool {
	return response.Status == http.StatusOK && response.ContentType == brokertransport.ExecutionStreamMediaTypeV3 && response.ContentEncoding == "" && validCorrelation(response.CorrelationID) && response.Body != nil
}

func (c *Client) beginHeldAttachment(ctx context.Context, definition domain.BrokerAttachmentOperationDefinitionV3) (*heldAttachmentInvocation, error) {
	started := c.now()
	deadline := brokerClientDeadline(started, ctx, time.Duration(definition.Definition.Limits.MaxOperationMillis)*time.Millisecond)
	bounded, cancel := context.WithDeadline(ctx, deadline)
	budget, err := domain.NewChildReadBudget(
		domain.ReadBudgetFromContext(ctx), 3,
		brokertransport.MaxDiscoveryNegotiationBytesV4+brokercontract.MaxDiscoveryV4Bytes+definition.MaxFramedResponseBytes,
	)
	if err != nil {
		cancel()
		return nil, clientError(domain.ErrCheckFailed)
	}
	requestContext := brokerClientReadContext(bounded, budget)
	selected, err := c.config.Session.Load()
	if err != nil {
		selected.Clear()
		cancel()
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
	return &heldAttachmentInvocation{
		client: c, definition: definition, selected: selected, http: httpClient,
		ctx: requestContext, cancel: cancel, started: started, deadline: deadline,
	}, nil
}

func (invocation *heldAttachmentInvocation) discover() error {
	discoveryDeadline := brokerClientDeadline(invocation.started, invocation.ctx, time.Duration(invocation.definition.Definition.Limits.MaxDecisionLeaseMillis)*time.Millisecond)
	discoveryContext, cancel := context.WithDeadline(invocation.ctx, discoveryDeadline)
	projection, request, err := invocation.client.discoverFamilyV4WithSession(discoveryContext, invocation.http, invocation.selected, invocation.started, discoveryDeadline)
	cancel()
	if err != nil {
		return err
	}
	invocation.projection = projection
	invocation.discoveryRequest = request
	return nil
}

func (invocation *heldAttachmentInvocation) close() {
	if invocation == nil {
		return
	}
	if invocation.cancel != nil {
		invocation.cancel()
	}
	invocation.selected.Clear()
	if invocation.http != nil {
		invocation.http.ClearCredential()
		invocation.http.CloseIdleConnections()
	}
}

func attachmentRequestV3(definition domain.BrokerAttachmentOperationDefinitionV3, requestID string, session Session, key, attachmentID string) domain.BrokerAttachmentRequestV3 {
	return domain.BrokerAttachmentRequestV3{
		SchemaVersion: domain.BrokerExecutionSchemaVersionV3, Operation: domain.BrokerOperationJiraAttachmentDownload,
		OperationVersion: definition.Definition.Version, RequestID: requestID,
		Features: append([]string(nil), definition.Definition.RequiredFeatures...), Expect: session.expectations(),
		Arguments: domain.BrokerAttachmentArgumentsV3{IssueKey: key, AttachmentID: attachmentID},
	}
}

func acceptedAttachmentFailureV3(response httpx.BoundedResponseStream) error {
	if response.Body == nil {
		return clientError(domain.ErrCheckFailed)
	}
	defer response.Body.Close()
	if response.Status >= 200 && response.Status < 300 || response.ContentType != "application/json" || response.ContentEncoding != "" || response.CorrelationID != "" && !validCorrelation(response.CorrelationID) {
		return clientError(domain.ErrCheckFailed)
	}
	body, err := io.ReadAll(response.Body)
	defer clear(body)
	if err != nil {
		return closedTransportError(err)
	}
	failure, err := brokertransport.DecodeExecutionFailureV3(body)
	if err != nil {
		return clientError(domain.ErrCheckFailed)
	}
	_, safe := brokercontract.ErrorForReason(failure.Reason)
	if safe == nil {
		return clientError(domain.ErrCheckFailed)
	}
	return safe
}

func attachmentStreamError(err error) error {
	if ok, safe := brokercontract.ContentFreeError(err); ok {
		return safe
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded, domain.ErrReadAttemptBudgetExhausted, domain.ErrReadResponseBudgetExhausted} {
		if errors.Is(err, sentinel) {
			return closedTransportError(err)
		}
	}
	return clientError(domain.ErrCheckFailed)
}
