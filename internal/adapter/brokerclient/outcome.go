package brokerclient

import (
	"context"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

var _ domain.BrokerOperationOutcomeClient = (*Client)(nil)

func (c *Client) ObserveBrokerOperation(ctx context.Context, operationTicket string) (domain.BrokerOperationOutcome, error) {
	if c == nil || c.config.Session == nil || ctx == nil {
		return domain.BrokerOperationOutcome{}, clientError(domain.ErrConfig)
	}
	definition, ok := brokercontract.Definition(domain.BrokerOperationOutcomeLookup, brokercontract.OperationVersion)
	arguments := domain.BrokerOperationArguments{Outcome: &domain.BrokerOutcomeArguments{OperationTicket: operationTicket}}
	validation := brokerV1Request(domain.BrokerOperationOutcomeLookup, definition, "validation", Session{
		ExecutionID: "validation", ExecutionEpoch: "validation", AuthorityRevision: "validation",
	}, arguments)
	if !ok || definition.QualificationProfile != "operation_ticket_v1" || definition.ExecutionProfile != "durable_observation_v1" || definition.BackendService != "jira" {
		return domain.BrokerOperationOutcome{}, unsupported()
	}
	if _, err := brokercontract.EncodeRequestV1(validation); err != nil {
		return domain.BrokerOperationOutcome{}, clientError(domain.ErrUsage)
	}
	if !definition.Available {
		return domain.BrokerOperationOutcome{}, unsupported()
	}
	invocation, err := c.beginHeldV1(ctx, definition, true)
	if err != nil {
		return domain.BrokerOperationOutcome{}, err
	}
	defer invocation.close()
	if err := invocation.discover(); err != nil {
		return domain.BrokerOperationOutcome{}, err
	}
	requestID, err := randomIdentifier()
	if err != nil {
		return domain.BrokerOperationOutcome{}, clientError(domain.ErrCheckFailed)
	}
	request := brokerV1Request(domain.BrokerOperationOutcomeLookup, definition, requestID, invocation.selected, arguments)
	body, _, err := invocation.execute(request)
	if err != nil {
		return domain.BrokerOperationOutcome{}, err
	}
	result, decodeErr := brokercontract.DecodeOperationOutcomeV1(body)
	clear(body)
	if decodeErr != nil {
		return domain.BrokerOperationOutcome{}, clientError(domain.ErrCheckFailed)
	}
	if err := invocation.stillCurrent(); err != nil {
		return domain.BrokerOperationOutcome{}, err
	}
	return result, nil
}
