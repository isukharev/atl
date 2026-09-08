package app

import (
	"context"
	"fmt"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type brokerReadExecution struct {
	base          context.Context
	qualification *domain.ReadBudget
	business      *domain.ReadBudget
	clock         func() time.Time
	startedAt     time.Time
	lastMillis    int64
}

func newBrokerReadExecution(ctx context.Context, definition domain.BrokerOperationDefinition, parent *domain.ReadBudget, clock func() time.Time, startedAt time.Time) (*brokerReadExecution, error) {
	limits := definition.Limits
	command, err := domain.NewChildReadBudget(parent, limits.MaxTotalUpstreamRequests, limits.MaxTotalUpstreamResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("broker read parent budget: %w", err)
	}
	qualification, err := domain.NewChildReadBudget(command, limits.Qualification.MaxRequests, limits.Qualification.MaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("broker read qualification budget: %w", err)
	}
	business, err := domain.NewChildReadBudget(command, limits.Business.MaxRequests, limits.Business.MaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("broker read business budget: %w", err)
	}
	return &brokerReadExecution{base: ctx, qualification: qualification, business: business, clock: clock, startedAt: startedAt, lastMillis: startedAt.UnixMilli()}, nil
}

func (e *brokerReadExecution) currentMillis() int64 {
	elapsed := e.clock().Sub(e.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	current := e.startedAt.UnixMilli() + elapsed.Milliseconds()
	if current > e.lastMillis {
		e.lastMillis = current
	}
	return e.lastMillis
}

func (e *brokerReadExecution) contextError() error {
	return e.base.Err()
}

func (e *brokerReadExecution) releaseDeadline(expiresAtMillis int64) time.Time {
	return e.startedAt.Add(time.Duration(expiresAtMillis-e.startedAt.UnixMilli()) * time.Millisecond)
}

func (e *brokerReadExecution) qualificationContext(expiresAtMillis int64) (context.Context, context.CancelFunc, error) {
	return e.phaseContext(e.qualification, expiresAtMillis)
}

func (e *brokerReadExecution) businessContext(expiresAtMillis int64) (context.Context, context.CancelFunc, error) {
	return e.phaseContext(e.business, expiresAtMillis)
}

func (e *brokerReadExecution) phaseContext(budget *domain.ReadBudget, expiresAtMillis int64) (context.Context, context.CancelFunc, error) {
	remainingMillis := expiresAtMillis - e.currentMillis()
	if remainingMillis <= 0 {
		return nil, nil, context.DeadlineExceeded
	}
	ctx, cancel := context.WithTimeout(e.base, time.Duration(remainingMillis)*time.Millisecond)
	return brokerReadRequestContext(ctx, budget), cancel, nil
}

func brokerReadRequestContext(ctx context.Context, budget *domain.ReadBudget) context.Context {
	return domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget)))
}
