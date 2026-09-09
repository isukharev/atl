package brokerauthority

import (
	"context"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const proposalPath = "/v1/authorize/proposal"

// AuthorizeProposal asks the authority to clear one exact native candidate.
// The application validates the returned clearance against the current clock
// immediately before the guarded operation, like the other decision phases.
func (a *Authority) AuthorizeProposal(ctx context.Context, request domain.BrokerProposalAuthorizationRequest) (domain.BrokerProposalClearance, error) {
	body, err := brokercontract.EncodeProposalAuthorizationRequestV1(request)
	if err != nil {
		return domain.BrokerProposalClearance{}, err
	}
	response, err := a.post(ctx, proposalPath, body, maxAuthorityDecisionBytes)
	if err != nil {
		return domain.BrokerProposalClearance{}, err
	}
	clearance, err := brokercontract.DecodeProposalClearanceV1(response)
	if err != nil {
		return domain.BrokerProposalClearance{}, authorityError(err)
	}
	return clearance, nil
}
