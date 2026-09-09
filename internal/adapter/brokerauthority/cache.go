package brokerauthority

import (
	"context"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const cacheQualificationPathV2 = "/v2/authorize/cache"

var _ domain.BrokerResolvedCacheAuthorizerV2 = (*Authority)(nil)

func (a *Authority) ResolveAndQualifyCacheV2(ctx context.Context, candidate domain.BrokerCacheQualificationCandidateV2) (domain.BrokerResolvedCacheQualificationV2, error) {
	if a == nil || a.client == nil || a.now == nil {
		return domain.BrokerResolvedCacheQualificationV2{}, authorityError(domain.ErrConfig)
	}
	body, err := brokercontract.EncodeCacheQualificationCandidateV2(candidate)
	if err != nil {
		return domain.BrokerResolvedCacheQualificationV2{}, err
	}
	response, err := a.post(ctx, cacheQualificationPathV2, body, brokercontract.MaxCacheQualificationV2Bytes)
	if err != nil {
		return domain.BrokerResolvedCacheQualificationV2{}, err
	}
	resolved, err := brokercontract.DecodeResolvedCacheQualificationV2(response)
	if err != nil {
		return domain.BrokerResolvedCacheQualificationV2{}, authorityError(domain.ErrCheckFailed)
	}
	if err := brokercontract.ValidateResolvedCacheQualificationV2(resolved, candidate, a.issuerSHA256, a.now()); err != nil {
		return domain.BrokerResolvedCacheQualificationV2{}, authorityError(err)
	}
	return resolved, nil
}
