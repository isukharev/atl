package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type BrokerCacheQualificationService struct {
	authorizer     domain.BrokerResolvedCacheAuthorizerV2
	expectedIssuer string
	now            func() time.Time
}

func NewBrokerCacheQualificationService(authorizer domain.BrokerResolvedCacheAuthorizerV2, expectedIssuer string) (*BrokerCacheQualificationService, error) {
	if authorizer == nil || len(expectedIssuer) != domain.BrokerMaxDigestBytes {
		return nil, fmt.Errorf("%w: Broker cache qualification dependencies are incomplete", domain.ErrUsage)
	}
	for _, current := range expectedIssuer {
		if current < '0' || current > '9' && current < 'a' || current > 'f' {
			return nil, fmt.Errorf("%w: Broker cache qualification issuer is invalid", domain.ErrUsage)
		}
	}
	return &BrokerCacheQualificationService{authorizer: authorizer, expectedIssuer: expectedIssuer, now: time.Now}, nil
}

func (s *BrokerCacheQualificationService) Resolve(ctx context.Context, candidate domain.BrokerCacheQualificationCandidateV2, deadline time.Time) (domain.BrokerResolvedCacheQualificationV2, time.Time, error) {
	fail := func(err error) (domain.BrokerResolvedCacheQualificationV2, time.Time, error) {
		return domain.BrokerResolvedCacheQualificationV2{}, time.Time{}, brokerCacheError(err)
	}
	if s == nil || s.authorizer == nil || s.now == nil || ctx == nil || deadline.IsZero() || deadline.UnixMilli() != candidate.NotAfterMillis || !s.now().Before(deadline) {
		return fail(domain.ErrCheckFailed)
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	started := s.now()
	resolved, err := s.authorizer.ResolveAndQualifyCacheV2(bounded, candidate)
	if err == nil {
		err = bounded.Err()
	}
	observed := s.now()
	validationErr := brokercontract.ValidateResolvedCacheQualificationV2(resolved, candidate, s.expectedIssuer, observed)
	if err != nil || validationErr != nil || !observed.Before(deadline) {
		if err == nil && validationErr == nil {
			err = context.DeadlineExceeded
		}
		if err == nil {
			err = validationErr
		}
		return fail(err)
	}
	release := deadline
	wall := observed.Add(time.Duration(resolved.Decision.ExpiresAtMillis-observed.UnixMilli()) * time.Millisecond)
	lease := started.Add(time.Duration(resolved.Decision.ExpiresAtMillis-resolved.Decision.IssuedAtMillis) * time.Millisecond)
	if wall.Before(release) {
		release = wall
	}
	if lease.Before(release) {
		release = lease
	}
	if !observed.Before(release) {
		return fail(context.DeadlineExceeded)
	}
	return resolved, release, nil
}

func brokerCacheError(err error) error {
	if ok, safe := brokercontract.ContentFreeError(err); ok {
		return fmt.Errorf("broker cache qualification failed: %w", safe)
	}
	sentinel := domain.ErrCheckFailed
	for _, candidate := range []error{context.Canceled, context.DeadlineExceeded, domain.ErrUsage, domain.ErrAuth, domain.ErrForbidden, domain.ErrReadAttemptBudgetExhausted, domain.ErrReadResponseBudgetExhausted} {
		if errors.Is(err, candidate) {
			sentinel = candidate
			break
		}
	}
	return fmt.Errorf("broker cache qualification failed: %w", sentinel)
}
