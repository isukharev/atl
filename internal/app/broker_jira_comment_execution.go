package app

import (
	"context"
	"fmt"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const brokerJiraCommentObservationWindow = time.Hour

type brokerJiraCommentExecution struct {
	base          context.Context
	deadline      time.Time
	qualification *domain.ReadBudget
	business      *domain.ReadBudget
	now           func() time.Time
	startedAt     time.Time
	lastMillis    int64
}

func newBrokerJiraCommentExecution(ctx context.Context, definition domain.BrokerOperationDefinition, parent *domain.ReadBudget, now func() time.Time, startedAt, deadline time.Time) (*brokerJiraCommentExecution, context.CancelFunc, error) {
	// Normalize every external wall deadline onto this execution's monotonic
	// anchor, including an earlier caller deadline retained in the ticket.
	deadline = startedAt.Add(deadline.Sub(startedAt))
	if callerDeadline, ok := ctx.Deadline(); ok {
		callerDeadline = startedAt.Add(callerDeadline.Sub(startedAt))
		if callerDeadline.Before(deadline) {
			deadline = callerDeadline
		}
	}
	command, err := domain.NewChildReadBudget(parent, definition.Limits.MaxTotalUpstreamRequests, definition.Limits.MaxTotalUpstreamResponseBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("broker Jira comment total budget: %w", err)
	}
	qualification, err := domain.NewChildReadBudget(command, definition.Limits.Qualification.MaxRequests, definition.Limits.Qualification.MaxResponseBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("broker Jira comment qualification budget: %w", err)
	}
	business, err := domain.NewChildReadBudget(command, definition.Limits.Business.MaxRequests, definition.Limits.Business.MaxResponseBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("broker Jira comment business budget: %w", err)
	}
	base, cancel := context.WithDeadline(ctx, deadline)
	return &brokerJiraCommentExecution{base: base, deadline: deadline, qualification: qualification, business: business, now: now, startedAt: startedAt, lastMillis: startedAt.UnixMilli()}, cancel, nil
}

func (e *brokerJiraCommentExecution) currentMillis() int64 {
	elapsed := e.now().Sub(e.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	e.lastMillis = max(e.lastMillis, e.startedAt.UnixMilli()+elapsed.Milliseconds())
	return e.lastMillis
}

func (e *brokerJiraCommentExecution) phaseContext(budget *domain.ReadBudget, expiries ...int64) (context.Context, context.CancelFunc, error) {
	deadline := e.releaseDeadline(expiries...)
	if deadline.UnixMilli() <= e.currentMillis() {
		return nil, nil, context.DeadlineExceeded
	}
	ctx, cancel := context.WithDeadline(e.base, deadline)
	return domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget))), cancel, nil
}

func (e *brokerJiraCommentExecution) qualificationContext(expiries ...int64) (context.Context, context.CancelFunc, error) {
	return e.phaseContext(e.qualification, expiries...)
}

func (e *brokerJiraCommentExecution) businessContext(expiries ...int64) (context.Context, context.CancelFunc, error) {
	return e.phaseContext(e.business, expiries...)
}

func (e *brokerJiraCommentExecution) closeoutContext(expiries ...int64) (context.Context, context.CancelFunc, error) {
	deadline := e.releaseDeadline(expiries...)
	if deadline.UnixMilli() <= e.currentMillis() {
		return nil, nil, context.DeadlineExceeded
	}
	ctx, cancel := context.WithDeadline(context.WithoutCancel(e.base), deadline)
	return domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(ctx, e.business))), cancel, nil
}

func (e *brokerJiraCommentExecution) releaseDeadline(expiries ...int64) time.Time {
	deadline := e.deadline
	for _, expiry := range expiries {
		candidate := e.startedAt.Add(time.UnixMilli(expiry).Sub(e.startedAt))
		if candidate.Before(deadline) {
			deadline = candidate
		}
	}
	return deadline
}
