package app

import (
	"context"
	"errors"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

// jiraGuardedExecution owns one row's immutable execution boundary. The row
// budget remains locally observable while an incoming command budget, when
// present, is charged as its parent.
type jiraGuardedExecution struct {
	ctx      context.Context
	deadline time.Time
	budget   *domain.ReadBudget
	cancel   context.CancelFunc
}

func preserveGuardedBudgetCause(original, sanitized error) error {
	causes := []error{sanitized}
	for _, sentinel := range []error{domain.ErrReadAttemptBudgetExhausted, domain.ErrReadResponseBudgetExhausted} {
		if errors.Is(original, sentinel) && !errors.Is(sanitized, sentinel) {
			causes = append(causes, sentinel)
		}
	}
	return errors.Join(causes...)
}

func newJiraGuardedExecution(ctx context.Context, parent *domain.ReadBudget, maxAttempts int, maxResponseBytes int64, duration time.Duration) (*jiraGuardedExecution, error) {
	budget, err := domain.NewChildReadBudget(parent, maxAttempts, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(duration)
	if incoming, ok := ctx.Deadline(); ok && incoming.Before(deadline) {
		deadline = incoming
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	bounded = domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(bounded, budget)))
	return &jiraGuardedExecution{ctx: bounded, deadline: deadline, budget: budget, cancel: cancel}, nil
}

func (e *jiraGuardedExecution) Close() {
	if e != nil && e.cancel != nil {
		e.cancel()
	}
}

func (e *jiraGuardedExecution) Closeout() (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.WithoutCancel(e.ctx), e.deadline)
}

func (e *jiraGuardedExecution) Usage() domain.ReadBudgetUsage {
	if e == nil {
		return domain.ReadBudgetUsage{}
	}
	return e.budget.Usage()
}
