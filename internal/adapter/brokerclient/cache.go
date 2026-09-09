package brokerclient

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

var _ domain.BrokerCacheQualificationReader = (*Client)(nil)

func (c *Client) QualifyCache(ctx context.Context, candidate domain.BrokerCacheCandidate) (domain.BrokerCacheGrant, error) {
	if c == nil || c.config.Session == nil || c.now == nil || ctx == nil {
		return domain.BrokerCacheGrant{}, clientError(domain.ErrConfig)
	}
	started := c.now()
	clientDeadline := cacheQualificationDeadline(started, ctx)
	bounded, cancel := context.WithDeadline(ctx, clientDeadline)
	defer cancel()
	session, err := c.config.Session.Load()
	if err != nil {
		return domain.BrokerCacheGrant{}, clientError(err)
	}
	defer session.Clear()
	requestID, err := randomIdentifier()
	if err != nil {
		return domain.BrokerCacheGrant{}, clientError(domain.ErrCheckFailed)
	}
	claim := brokertransport.CacheQualificationClaimV2{
		SchemaVersion: 2, RequestID: requestID, Service: "confluence", BrokerID: c.config.BrokerID, Audience: c.config.Audience,
		Expect: session.expectations(), NotAfterMillis: clientDeadline.UnixMilli(), Candidate: candidate,
	}
	body, err := brokertransport.EncodeCacheQualificationClaimV2(claim)
	if err != nil {
		return domain.BrokerCacheGrant{}, clientError(domain.ErrUsage)
	}
	budget, err := domain.NewChildReadBudget(domain.ReadBudgetFromContext(ctx), 1, brokertransport.MaxCacheQualificationBytesV2)
	if err != nil {
		return domain.BrokerCacheGrant{}, clientError(domain.ErrCheckFailed)
	}
	requestContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(bounded, budget))))
	httpClient, err := c.newHTTPClient(session)
	if err != nil {
		return domain.BrokerCacheGrant{}, clientError(domain.ErrConfig)
	}
	defer httpClient.CloseIdleConnections()
	defer httpClient.ClearCredential()
	response, err := httpClient.DoBoundedResponse(requestContext, http.MethodPost, brokertransport.CacheQualificationPathV2, body, map[string]string{"Content-Type": "application/json"}, brokertransport.MaxCacheQualificationBytesV2, brokertransport.MaxTransportFailureBytes)
	if err != nil {
		return domain.BrokerCacheGrant{}, closedTransportError(err)
	}
	body, err = acceptedCacheQualificationBody(response)
	if err != nil {
		return domain.BrokerCacheGrant{}, err
	}
	envelope, err := brokertransport.DecodeCacheQualificationEnvelopeV2(body)
	if err != nil || brokertransport.ValidateCacheQualificationEnvelopeV2(envelope, claim, c.now()) != nil || bounded.Err() != nil {
		return domain.BrokerCacheGrant{}, clientError(domain.ErrCheckFailed)
	}
	current, err := c.config.Session.Load()
	if err != nil {
		return domain.BrokerCacheGrant{}, clientError(err)
	}
	defer current.Clear()
	if current.expectations() != session.expectations() || !bytes.Equal(current.Credential, session.Credential) ||
		brokertransport.ValidateCacheQualificationEnvelopeV2(envelope, claim, c.now()) != nil || bounded.Err() != nil {
		return domain.BrokerCacheGrant{}, clientError(domain.ErrCheckFailed)
	}
	observed := c.now()
	serverDeadline := observed.Add(time.Duration(envelope.ReleaseDeadlineMillis-observed.UnixMilli()) * time.Millisecond)
	releaseDeadline := clientDeadline
	if serverDeadline.Before(releaseDeadline) {
		releaseDeadline = serverDeadline
	}
	lease := domain.NewBrokerCacheGrantLease(session.Credential, releaseDeadline)
	if !lease.Active(observed) {
		return domain.BrokerCacheGrant{}, clientError(domain.ErrCheckFailed)
	}
	return domain.BrokerCacheGrant{
		Decision: envelope.Qualification, CandidateSHA256: envelope.CandidateSHA256, TargetExecutionSHA256: envelope.TargetExecutionSHA256,
		AuthorityRevisionSHA256: envelope.AuthorityRevisionSHA256, RequestSHA256: envelope.RequestSHA256,
		ReleaseDeadlineMillis: envelope.ReleaseDeadlineMillis, Lease: lease,
	}, nil
}

func cacheQualificationDeadline(started time.Time, parent context.Context) time.Time {
	deadline := started.Add(time.Duration(domain.BrokerMaxDecisionLeaseMillis) * time.Millisecond)
	if parentDeadline, ok := parent.Deadline(); ok {
		parentRemaining := parentDeadline.Sub(started)
		if parentRemaining < deadline.Sub(started) {
			deadline = started.Add(parentRemaining)
		}
	}
	return deadline
}

func (c *Client) ValidateCacheGrant(candidate domain.BrokerCacheCandidate, grant domain.BrokerCacheGrant) error {
	candidateDigest, candidateErr := brokercontract.CacheCandidateSHA256V1(candidate)
	_, decisionErr := brokercontract.EncodeCacheQualificationV1(grant.Decision)
	if c == nil || c.config.Session == nil || c.now == nil {
		return clientError(domain.ErrCheckFailed)
	}
	if grant.Decision.Status != domain.BrokerDecisionAllowed || grant.Decision.Reason != "" ||
		candidateErr != nil || decisionErr != nil || grant.CandidateSHA256 != candidateDigest ||
		grant.RequestSHA256 != grant.Decision.RequestSHA256 || grant.TargetExecutionSHA256 != grant.Decision.TargetExecutionSHA256 ||
		grant.AuthorityRevisionSHA256 != grant.Decision.AuthorityRevisionSHA256 || grant.ReleaseDeadlineMillis > grant.Decision.ExpiresAtMillis {
		return clientError(domain.ErrCheckFailed)
	}
	// Encoding a fresh claim validates the complete candidate shape without
	// retaining the original request id or credential.
	session, err := c.config.Session.Load()
	if err != nil {
		return clientError(err)
	}
	defer session.Clear()
	now := c.now()
	if now.UnixMilli() < grant.Decision.IssuedAtMillis-domain.BrokerClockAllowanceMillis {
		return clientError(domain.ErrCheckFailed)
	}
	target, targetErr := brokercontract.CacheTargetExecutionClaimSHA256V1(c.config.BrokerID, c.config.Audience, session.ExecutionID, session.ExecutionEpoch)
	revision, revisionErr := brokercontract.CacheAuthorityRevisionSHA256V1(session.AuthorityRevision)
	probe := brokertransport.CacheQualificationClaimV2{SchemaVersion: 2, RequestID: "validation", Service: "confluence", BrokerID: c.config.BrokerID, Audience: c.config.Audience, Expect: session.expectations(), NotAfterMillis: grant.ReleaseDeadlineMillis, Candidate: candidate}
	if targetErr != nil || revisionErr != nil || target != grant.TargetExecutionSHA256 || revision != grant.AuthorityRevisionSHA256 || !grant.Lease.Matches(session.Credential, now) {
		return clientError(domain.ErrCheckFailed)
	}
	if _, err := brokertransport.EncodeCacheQualificationClaimV2(probe); err != nil {
		return clientError(domain.ErrCheckFailed)
	}
	return nil
}

func acceptedCacheQualificationBody(response httpx.BoundedResponse) ([]byte, error) {
	if !validCorrelation(response.CorrelationID) {
		return nil, clientError(domain.ErrCheckFailed)
	}
	if response.Status == http.StatusOK {
		return response.Body, nil
	}
	failure, err := brokertransport.DecodeCacheFailureV2(response.Body)
	if err != nil {
		return nil, clientError(domain.ErrCheckFailed)
	}
	_, safe := brokercontract.ErrorForReason(failure.Reason)
	return nil, safe
}
