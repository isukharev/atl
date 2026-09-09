package app

import (
	"context"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

// DiscoverExecutionV2 is advisory only. It neither qualifies a resource nor
// retains an authority projection for a later Execute call.
func (s *BrokerProjectPageService) DiscoverExecutionV2(ctx context.Context, request domain.BrokerFamilyDiscoveryRequestV3, verified domain.BrokerVerifiedContext) (domain.BrokerFamilyDiscoveryProjectionV3, time.Time, error) {
	fail := func(reason domain.BrokerReason) (domain.BrokerFamilyDiscoveryProjectionV3, time.Time, error) {
		_, err := brokercontract.ErrorForReason(reason)
		return domain.BrokerFamilyDiscoveryProjectionV3{}, time.Time{}, err
	}
	if s == nil || s.now == nil || s.jira.Reader == nil || ctx == nil {
		return fail(domain.BrokerReasonAuthorizationUnavailable)
	}
	authorizer, ok := s.authorizer.(domain.BrokerFamilyDiscoveryAuthorizerV3)
	if !ok {
		return fail(domain.BrokerReasonUnsupported)
	}
	origin, err := s.jira.Reader.BrokerOriginSHA256()
	if err != nil || origin != s.jira.Backend.OriginSHA256 || verified.Backend != s.jira.Backend {
		return fail(domain.BrokerReasonUnsupported)
	}
	digest, err := brokercontract.FamilyDiscoveryRequestSHA256V3(request)
	if err != nil {
		return fail(domain.BrokerReasonMalformed)
	}
	authorization := domain.BrokerFamilyDiscoveryAuthorizationRequestV3{SchemaVersion: 3, Request: request, Context: verified, RequestSHA256: digest}
	if _, err := brokercontract.EncodeFamilyDiscoveryAuthorizationRequestV3(authorization); err != nil {
		return fail(domain.BrokerReasonStaleAuthority)
	}
	started := s.now()
	if ctx.Err() != nil || request.NotAfterMillis <= started.UnixMilli() || started.UnixMilli() < verified.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis {
		return fail(domain.BrokerReasonDecisionExpired)
	}
	deadline := started.Add(time.Duration(min(request.NotAfterMillis-started.UnixMilli(), domain.BrokerMaxDecisionLeaseMillis)) * time.Millisecond)
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	projection, err := authorizer.DiscoverFamilyV3(bounded, authorization)
	if err != nil {
		if ok, safe := brokercontract.ContentFreeError(err); ok {
			return domain.BrokerFamilyDiscoveryProjectionV3{}, time.Time{}, safe
		}
		return fail(domain.BrokerReasonAuthorizationUnavailable)
	}
	observed := s.now()
	if bounded.Err() != nil || !observed.Before(deadline) {
		return fail(domain.BrokerReasonDecisionExpired)
	}
	if err := brokercontract.ValidateFamilyDiscoveryProjectionV3ForContext(projection, authorization, observed); err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, time.Time{}, err
	}
	// Anchor both authority lease length and remaining wall-clock lifetime to
	// local observations. A backwards wall jump cannot renew the lease.
	lease := started.Add(time.Duration(projection.ExpiresAtMillis-projection.IssuedAtMillis) * time.Millisecond)
	wall := observed.Add(time.Duration(projection.ExpiresAtMillis-observed.UnixMilli()) * time.Millisecond)
	if lease.Before(deadline) {
		deadline = lease
	}
	if wall.Before(deadline) {
		deadline = wall
	}
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = observed.Add(parent.Sub(observed))
	}
	if !observed.Before(deadline) {
		return fail(domain.BrokerReasonDecisionExpired)
	}
	return projection, deadline, nil
}
