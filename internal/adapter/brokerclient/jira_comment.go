package brokerclient

import (
	"context"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

var _ domain.BrokerJiraCommentClient = (*Client)(nil)
var _ domain.BrokerJiraCommentClient = (*Jira)(nil)

func (c *Client) PreviewJiraComment(ctx context.Context, issueKey string, nativeBody []byte) (domain.BrokerJiraCommentResult, error) {
	if c == nil || c.config.Session == nil || ctx == nil {
		return domain.BrokerJiraCommentResult{}, clientError(domain.ErrConfig)
	}
	arguments := domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
		IssueKey: issueKey, NativeBody: append([]byte(nil), nativeBody...), SatisfactionPolicy: "append_always",
	}}
	defer clear(arguments.JiraComment.NativeBody)
	return c.executeJiraComment(ctx, domain.BrokerOperationJiraCommentPreview, arguments)
}

func (c *Client) ApplyJiraComment(ctx context.Context, issueKey string, nativeBody []byte, expectedProposalHash, operationTicket string) (domain.BrokerJiraCommentResult, error) {
	if c == nil || c.config.Session == nil || ctx == nil {
		return domain.BrokerJiraCommentResult{}, clientError(domain.ErrConfig)
	}
	arguments := domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
		IssueKey: issueKey, NativeBody: append([]byte(nil), nativeBody...), SatisfactionPolicy: "append_always",
		ExpectedProposalHash: expectedProposalHash, OperationTicket: operationTicket,
	}}
	defer clear(arguments.JiraComment.NativeBody)
	return c.executeJiraComment(ctx, domain.BrokerOperationJiraCommentApply, arguments)
}

func (c *Client) executeJiraComment(ctx context.Context, operation domain.BrokerOperationID, arguments domain.BrokerOperationArguments) (domain.BrokerJiraCommentResult, error) {
	definition, ok := brokercontract.Definition(operation, brokercontract.OperationVersion)
	validation := brokerV1Request(operation, definition, "validation", Session{
		ExecutionID: "validation", ExecutionEpoch: "validation", AuthorityRevision: "validation",
	}, arguments)
	if !ok || brokercontract.ValidateBrokerJiraCommentDefinitionV1(definition, operation) != nil {
		return domain.BrokerJiraCommentResult{}, unsupported()
	}
	if _, err := brokercontract.EncodeRequestV1(validation); err != nil {
		return domain.BrokerJiraCommentResult{}, clientError(domain.ErrUsage)
	}
	if !definition.Available {
		return domain.BrokerJiraCommentResult{}, unsupported()
	}
	invocation, err := c.beginHeldV1(ctx, definition, operation == domain.BrokerOperationJiraCommentPreview)
	if err != nil {
		return domain.BrokerJiraCommentResult{}, err
	}
	defer invocation.close()
	if err := invocation.discover(); err != nil {
		return domain.BrokerJiraCommentResult{}, err
	}
	requestID, err := randomIdentifier()
	if err != nil {
		return domain.BrokerJiraCommentResult{}, clientError(domain.ErrCheckFailed)
	}
	request := brokerV1Request(operation, definition, requestID, invocation.selected, arguments)
	body, dispatched, err := invocation.execute(request)
	if err != nil {
		if operation == domain.BrokerOperationJiraCommentApply && dispatched {
			return domain.BrokerJiraCommentResult{}, ambiguousBrokerCommentApply(err)
		}
		return domain.BrokerJiraCommentResult{}, err
	}
	result, decodeErr := brokercontract.DecodeJiraCommentResultV1(body)
	clear(body)
	if decodeErr != nil || brokercontract.ValidateJiraCommentResultForV1(result, request) != nil {
		if operation == domain.BrokerOperationJiraCommentApply {
			return domain.BrokerJiraCommentResult{}, ambiguousBrokerCommentApply(domain.ErrCheckFailed)
		}
		return domain.BrokerJiraCommentResult{}, clientError(domain.ErrCheckFailed)
	}
	if err := invocation.stillCurrent(); err != nil {
		if operation == domain.BrokerOperationJiraCommentApply {
			return domain.BrokerJiraCommentResult{}, ambiguousBrokerCommentApply(err)
		}
		return domain.BrokerJiraCommentResult{}, err
	}
	return result, nil
}

func (j *Jira) PreviewJiraComment(ctx context.Context, issueKey string, nativeBody []byte) (domain.BrokerJiraCommentResult, error) {
	if j == nil || j.client == nil {
		return domain.BrokerJiraCommentResult{}, clientError(domain.ErrConfig)
	}
	return j.client.PreviewJiraComment(ctx, issueKey, nativeBody)
}

func (j *Jira) ApplyJiraComment(ctx context.Context, issueKey string, nativeBody []byte, expectedProposalHash, operationTicket string) (domain.BrokerJiraCommentResult, error) {
	if j == nil || j.client == nil {
		return domain.BrokerJiraCommentResult{}, clientError(domain.ErrConfig)
	}
	return j.client.ApplyJiraComment(ctx, issueKey, nativeBody, expectedProposalHash, operationTicket)
}

func brokerV1Request(operation domain.BrokerOperationID, definition domain.BrokerOperationDefinition, requestID string, session Session, arguments domain.BrokerOperationArguments) domain.BrokerRequest {
	return domain.BrokerRequest{
		SchemaVersion: brokercontract.SchemaVersion, Operation: operation, OperationVersion: definition.Version,
		RequestID: requestID, Features: append([]string(nil), definition.RequiredFeatures...),
		Expect: session.expectations(), Arguments: arguments,
	}
}

type brokerCommentApplyAmbiguity struct{ cause error }

func (*brokerCommentApplyAmbiguity) Error() string {
	return "Broker Jira comment apply outcome is unknown; do not retry apply; run `atl jira issue comment outcome --operation-ticket <SAME-TICKET>` with the original operation ticket"
}
func (e *brokerCommentApplyAmbiguity) Unwrap() []error {
	return []error{domain.ErrCheckFailed, e.cause}
}
func (*brokerCommentApplyAmbiguity) DiagnosticAmbiguousWrite() bool { return true }

func ambiguousBrokerCommentApply(cause error) error {
	return &brokerCommentApplyAmbiguity{cause: cause}
}
