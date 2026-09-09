package brokerauthority

import (
	"context"
	"net/http"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

var _ domain.BrokerFamilyDiscoveryAuthorizerV3 = (*Authority)(nil)

func (a *Authority) DiscoverFamilyV3(ctx context.Context, request domain.BrokerFamilyDiscoveryAuthorizationRequestV3) (domain.BrokerFamilyDiscoveryProjectionV3, error) {
	fail := func() (domain.BrokerFamilyDiscoveryProjectionV3, error) {
		_, err := brokercontract.ErrorForReason(domain.BrokerReasonAuthorizationUnavailable)
		return domain.BrokerFamilyDiscoveryProjectionV3{}, err
	}
	if a == nil || a.client == nil || a.now == nil {
		return fail()
	}
	body, err := brokercontract.EncodeFamilyDiscoveryAuthorizationRequestV3(request)
	if err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, err
	}
	budget, err := domain.NewChildReadBudget(domain.ReadBudgetFromContext(ctx), 1, brokercontract.MaxDiscoveryV3Bytes)
	if err != nil {
		return fail()
	}
	bounded, cancel := context.WithTimeout(ctx, authorityCallTimeout)
	defer cancel()
	requestContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(bounded, budget))))
	response, err := a.client.DoBoundedResponse(requestContext, http.MethodPost, brokertransport.DiscoveryPathV3, body, map[string]string{"Content-Type": "application/json"}, brokercontract.MaxDiscoveryV3Bytes, brokertransport.MaxTransportFailureBytes)
	if err != nil || bounded.Err() != nil {
		return fail()
	}
	if response.Status != http.StatusOK {
		failure, err := brokertransport.DecodeDiscoveryFailureV3(response.Body)
		if err != nil {
			return fail()
		}
		_, safe := brokercontract.ErrorForReason(failure.Reason)
		return domain.BrokerFamilyDiscoveryProjectionV3{}, safe
	}
	projection, err := brokercontract.DecodeFamilyDiscoveryProjectionV3(response.Body)
	if err != nil || brokercontract.ValidateFamilyDiscoveryProjectionV3ForContext(projection, request, a.now()) != nil {
		return fail()
	}
	return projection, nil
}
