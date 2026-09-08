package app

import (
	"context"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

// Discover obtains one current advisory projection. It has no qualification or
// business-read path, and no result from it is retained by Execute.
func (s *BrokerReadService) Discover(ctx context.Context, request domain.BrokerDiscoveryRequestV2, verified domain.BrokerVerifiedContext) (domain.BrokerDiscoveryProjectionV2, time.Time, error) {
	fail := func(reason domain.BrokerReason) (domain.BrokerDiscoveryProjectionV2, time.Time, error) {
		_, err := brokercontract.ErrorForReason(reason)
		return domain.BrokerDiscoveryProjectionV2{}, time.Time{}, err
	}
	if s == nil || s.now == nil || ctx == nil {
		return fail(domain.BrokerReasonAuthorizationUnavailable)
	}
	authorizer, ok := s.authorizer.(domain.BrokerDiscoveryAuthorizerV2)
	if !ok {
		return fail(domain.BrokerReasonUnsupported)
	}
	operation := domain.BrokerOperationJiraIssueRead
	if request.Service == "confluence" {
		operation = domain.BrokerOperationConfluencePageRead
	}
	matched, err := s.backendMatches(operation, verified.Backend)
	if err != nil || !matched {
		return fail(domain.BrokerReasonUnsupported)
	}
	digest, err := brokercontract.DiscoveryRequestSHA256V2(request)
	if err != nil {
		return fail(domain.BrokerReasonMalformed)
	}
	authorization := domain.BrokerDiscoveryAuthorizationRequestV2{SchemaVersion: 2, Request: request, Context: verified, RequestSHA256: digest}
	if _, err := brokercontract.EncodeDiscoveryAuthorizationRequestV2(authorization); err != nil {
		return fail(domain.BrokerReasonStaleAuthority)
	}
	started := s.now()
	if request.NotAfterMillis <= started.UnixMilli() || started.UnixMilli() < verified.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis {
		return fail(domain.BrokerReasonDecisionExpired)
	}
	deadline := started.Add(time.Duration(min(request.NotAfterMillis-started.UnixMilli(), domain.BrokerMaxDecisionLeaseMillis)) * time.Millisecond)
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	projection, err := authorizer.DiscoverV2(bounded, authorization)
	if err != nil {
		if ok, safe := brokercontract.ContentFreeError(err); ok {
			return domain.BrokerDiscoveryProjectionV2{}, time.Time{}, safe
		}
		return fail(domain.BrokerReasonAuthorizationUnavailable)
	}
	if bounded.Err() != nil || !s.now().Before(deadline) {
		return fail(domain.BrokerReasonDecisionExpired)
	}
	if err := brokercontract.ValidateDiscoveryProjectionV2ForContext(projection, authorization, s.now()); err != nil {
		return domain.BrokerDiscoveryProjectionV2{}, time.Time{}, err
	}
	// Bound both wall time and monotonic elapsed time before releasing bytes.
	leaseDeadline := started.Add(time.Duration(projection.ExpiresAtMillis-projection.IssuedAtMillis) * time.Millisecond)
	wallDeadline := s.now().Add(time.Duration(projection.ExpiresAtMillis-s.now().UnixMilli()) * time.Millisecond)
	if leaseDeadline.Before(deadline) {
		deadline = leaseDeadline
	}
	if wallDeadline.Before(deadline) {
		deadline = wallDeadline
	}
	return projection, deadline, nil
}
