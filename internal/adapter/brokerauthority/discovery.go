package brokerauthority

import (
	"context"
	"net/http"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

var _ domain.BrokerDiscoveryAuthorizerV2 = (*Authority)(nil)

func (a *Authority) DiscoverV2(ctx context.Context, request domain.BrokerDiscoveryAuthorizationRequestV2) (domain.BrokerDiscoveryProjectionV2, error) {
	fail := func() (domain.BrokerDiscoveryProjectionV2, error) {
		_, err := brokercontract.ErrorForReason(domain.BrokerReasonAuthorizationUnavailable)
		return domain.BrokerDiscoveryProjectionV2{}, err
	}
	if a == nil || a.client == nil || a.now == nil {
		return fail()
	}
	body, err := brokercontract.EncodeDiscoveryAuthorizationRequestV2(request)
	if err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, err
	}
	budget, err := domain.NewChildReadBudget(domain.ReadBudgetFromContext(ctx), 1, brokercontract.MaxDiscoveryV2Bytes)
	if err != nil {
		return fail()
	}
	bounded, cancel := context.WithTimeout(ctx, authorityCallTimeout)
	defer cancel()
	requestContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(bounded, budget))))
	response, err := a.client.DoBoundedResponse(requestContext, http.MethodPost, "/v2/discovery", body, map[string]string{"Content-Type": "application/json"}, brokercontract.MaxDiscoveryV2Bytes, brokertransport.MaxTransportFailureBytes)
	if err != nil || bounded.Err() != nil {
		return fail()
	}
	if response.Status != http.StatusOK {
		failure, err := brokertransport.DecodeDiscoveryFailureV2(response.Body)
		if err != nil {
			return fail()
		}
		_, safe := brokercontract.ErrorForReason(failure.Reason)
		return domain.BrokerDiscoveryProjectionV2{}, safe
	}
	projection, err := brokercontract.DecodeDiscoveryProjectionV2(response.Body)
	if err != nil || brokercontract.ValidateDiscoveryProjectionV2ForContext(projection, request, a.now()) != nil {
		return fail()
	}
	return projection, nil
}
