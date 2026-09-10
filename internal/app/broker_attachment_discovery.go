package app

import (
	"context"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

// DiscoverExecutionV3 returns one current fixed-family advisory projection.
// It performs no Jira read and retains no authority for a later execution.
func (s *BrokerJiraAttachmentStreamService) DiscoverExecutionV3(ctx context.Context, request domain.BrokerFamilyDiscoveryRequestV4, verified domain.BrokerVerifiedContext) (domain.BrokerFamilyDiscoveryProjectionV4, time.Time, error) {
	fail := func(reason domain.BrokerReason) (domain.BrokerFamilyDiscoveryProjectionV4, time.Time, error) {
		_, err := brokercontract.ErrorForReason(reason)
		return domain.BrokerFamilyDiscoveryProjectionV4{}, time.Time{}, err
	}
	if s == nil || s.now == nil || s.jira.Reader == nil || s.origin == nil || ctx == nil {
		return fail(domain.BrokerReasonAuthorizationUnavailable)
	}
	authorizer, ok := s.authorizer.(domain.BrokerFamilyDiscoveryAuthorizerV4)
	if !ok {
		return fail(domain.BrokerReasonUnsupported)
	}
	if verified.Backend != s.jira.Backend {
		return fail(domain.BrokerReasonUnsupported)
	}
	origin, err := s.origin.BrokerOriginSHA256()
	if err != nil || origin != s.jira.Backend.OriginSHA256 {
		return fail(domain.BrokerReasonUnsupported)
	}
	digest, err := brokercontract.FamilyDiscoveryRequestSHA256V4(request)
	if err != nil {
		return fail(domain.BrokerReasonMalformed)
	}
	authorization := domain.BrokerFamilyDiscoveryAuthorizationRequestV4{SchemaVersion: domain.BrokerDiscoverySchemaVersionV4, Request: request, Context: verified, RequestSHA256: digest}
	if _, err := brokercontract.EncodeFamilyDiscoveryAuthorizationRequestV4(authorization); err != nil {
		return fail(domain.BrokerReasonStaleAuthority)
	}
	started := s.now()
	if ctx.Err() != nil || request.NotAfterMillis <= started.UnixMilli() || started.UnixMilli() < verified.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis {
		return fail(domain.BrokerReasonDecisionExpired)
	}
	deadline := started.Add(time.Duration(min(request.NotAfterMillis-started.UnixMilli(), domain.BrokerMaxDecisionLeaseMillis)) * time.Millisecond)
	if parent, ok := ctx.Deadline(); ok {
		parent = started.Add(parent.Sub(started))
		if parent.Before(deadline) {
			deadline = parent
		}
	}
	if !started.Before(deadline) {
		return fail(domain.BrokerReasonDecisionExpired)
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	projection, err := authorizer.DiscoverFamilyV4(bounded, authorization)
	if err != nil {
		if ok, safe := brokercontract.ContentFreeError(err); ok {
			return domain.BrokerFamilyDiscoveryProjectionV4{}, time.Time{}, safe
		}
		return fail(domain.BrokerReasonAuthorizationUnavailable)
	}
	observed := s.now()
	if bounded.Err() != nil || !observed.Before(deadline) {
		return fail(domain.BrokerReasonDecisionExpired)
	}
	if err := brokercontract.ValidateFamilyDiscoveryProjectionV4ForContext(projection, authorization, observed); err != nil {
		return domain.BrokerFamilyDiscoveryProjectionV4{}, time.Time{}, err
	}
	lease := started.Add(time.Duration(projection.ExpiresAtMillis-projection.IssuedAtMillis) * time.Millisecond)
	wall := observed.Add(time.Duration(projection.ExpiresAtMillis-observed.UnixMilli()) * time.Millisecond)
	if lease.Before(deadline) {
		deadline = lease
	}
	if wall.Before(deadline) {
		deadline = wall
	}
	if !observed.Before(deadline) {
		return fail(domain.BrokerReasonDecisionExpired)
	}
	return projection, deadline, nil
}
