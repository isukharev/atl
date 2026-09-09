package httpx

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type readDispatchNotAfterContextKey struct{}

func withReadDispatchNotAfter(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(ctx, readDispatchNotAfterContextKey{}, deadline)
}

func readDispatchNotAfter(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	deadline, ok := ctx.Value(readDispatchNotAfterContextKey{}).(time.Time)
	return deadline, ok
}

func checkReadDispatch(ctx context.Context) error {
	deadline, ok := readDispatchNotAfter(ctx)
	if !ok {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline.IsZero() || !time.Now().Before(deadline) {
		return domain.ErrReadDispatchExpired
	}
	return nil
}

func readDispatchWaitContext(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	deadline, ok := readDispatchNotAfter(ctx)
	if !ok {
		return ctx, func() {}, false
	}
	waitCtx, cancel := context.WithDeadline(ctx, deadline)
	return waitCtx, cancel, true
}

func prepareStreamDispatchContext(ctx context.Context, dispatchNotAfter time.Time) (context.Context, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: stream body context is nil", domain.ErrCheckFailed)
	}
	bodyDeadline, bounded := ctx.Deadline()
	if !bounded {
		return nil, fmt.Errorf("%w: stream body context requires a deadline", domain.ErrCheckFailed)
	}
	if dispatchNotAfter.IsZero() {
		return nil, fmt.Errorf("%w: stream dispatch deadline is required", domain.ErrCheckFailed)
	}
	if dispatchNotAfter.After(bodyDeadline) {
		return nil, fmt.Errorf("%w: stream dispatch deadline exceeds body deadline", domain.ErrCheckFailed)
	}
	if !domain.SingleAttempt(ctx) {
		return nil, fmt.Errorf("%w: stream dispatch requires single-attempt mode", domain.ErrCheckFailed)
	}
	parent := domain.ReadBudgetFromContext(ctx)
	if parent == nil {
		return nil, fmt.Errorf("%w: stream dispatch requires a finite read budget", domain.ErrCheckFailed)
	}
	budget, err := domain.NewChildReadBudget(parent, 1, math.MaxInt64)
	if err != nil {
		return nil, fmt.Errorf("%w: stream dispatch read budget is invalid", domain.ErrCheckFailed)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !time.Now().Before(dispatchNotAfter) {
		return nil, domain.ErrReadDispatchExpired
	}
	ctx = domain.WithReadBudget(ctx, budget)
	return withReadDispatchNotAfter(ctx, dispatchNotAfter), nil
}
