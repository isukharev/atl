package domain

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestNewReadBudgetRejectsNegativeLimits(t *testing.T) {
	if _, err := NewReadBudget(-1, 0); err == nil {
		t.Fatal("negative attempt limit unexpectedly accepted")
	}
	if _, err := NewReadBudget(0, -1); err == nil {
		t.Fatal("negative response-byte limit unexpectedly accepted")
	}
}

func TestReadBudgetContextIsOptIn(t *testing.T) {
	ctx := context.Background()
	if got := ReadBudgetFromContext(ctx); got != nil {
		t.Fatalf("background context budget = %v, want nil", got)
	}
	if got := WithReadBudget(ctx, nil); got != ctx {
		t.Fatal("nil read budget changed the context")
	}

	budget, err := NewReadBudget(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := ReadBudgetFromContext(WithReadBudget(ctx, budget)); got != budget {
		t.Fatalf("context budget = %p, want %p", got, budget)
	}
}

func TestReadBudgetConcurrentCountersNeverExceedLimits(t *testing.T) {
	const (
		attemptLimit = 37
		byteLimit    = 41
		workers      = 100
	)
	budget, err := NewReadBudget(attemptLimit, byteLimit)
	if err != nil {
		t.Fatal(err)
	}

	var admittedAttempts atomic.Int32
	var admittedBytes atomic.Int32
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := budget.TakeAttempt(); err == nil {
				admittedAttempts.Add(1)
			} else if !errors.Is(err, ErrReadAttemptBudgetExhausted) {
				t.Errorf("TakeAttempt error = %v", err)
			}

			remaining, finish, err := budget.BeginResponse(context.Background())
			if err != nil {
				t.Errorf("BeginResponse error = %v", err)
				return
			}
			if remaining > 0 {
				admittedBytes.Add(1)
				finish(1)
			} else {
				finish(0)
			}
		}()
	}
	wg.Wait()

	usage := budget.Usage()
	if usage.Attempts != attemptLimit || usage.ResponseBytes != byteLimit {
		t.Fatalf("usage = %+v, want attempts=%d response_bytes=%d", usage, attemptLimit, byteLimit)
	}
	if got := int(admittedAttempts.Load()); got != attemptLimit {
		t.Fatalf("admitted attempts = %d, want %d", got, attemptLimit)
	}
	if got := int(admittedBytes.Load()); got != byteLimit {
		t.Fatalf("admitted bytes = %d, want %d", got, byteLimit)
	}
}
