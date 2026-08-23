package domain

import (
	"context"
	"errors"
	"runtime"
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

func TestReadBudgetZeroValueAndMalformedHandlesFailClosed(t *testing.T) {
	valid, err := NewReadBudget(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		budget *ReadBudget
	}{
		{name: "zero value", budget: new(ReadBudget)},
		{name: "nil scope", budget: &ReadBudget{scopes: []*readBudgetScope{nil}}},
		{name: "duplicate scope", budget: &ReadBudget{scopes: []*readBudgetScope{valid.scopes[0], valid.scopes[0]}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.budget.TakeAttempt(); !errors.Is(err, ErrReadAttemptBudgetExhausted) {
				t.Fatalf("TakeAttempt error = %v", err)
			}
			remaining, finish, err := test.budget.BeginResponse(t.Context())
			if remaining != 0 || finish != nil || !errors.Is(err, ErrReadResponseBudgetExhausted) {
				t.Fatalf("BeginResponse remaining=%d finish_nil=%t error=%v", remaining, finish == nil, err)
			}
			if child, err := NewChildReadBudget(test.budget, 1, 1); child != nil || err == nil {
				t.Fatalf("child=%v error=%v", child, err)
			}
		})
	}
}

func TestReadBudgetZeroAttemptLimitStillRefuses(t *testing.T) {
	budget, err := NewReadBudget(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.TakeAttempt(); !errors.Is(err, ErrReadAttemptBudgetExhausted) {
		t.Fatalf("TakeAttempt error = %v", err)
	}
	if got := budget.Usage(); got != (ReadBudgetUsage{}) {
		t.Fatalf("usage = %+v", got)
	}
}

func TestReadBudgetExhaustionAttemptClassification(t *testing.T) {
	var diagnostic interface{ DiagnosticWriteAttempted() bool }
	if !errors.As(ErrReadAttemptBudgetExhausted, &diagnostic) || diagnostic.DiagnosticWriteAttempted() {
		t.Fatal("attempt exhaustion was not classified as definitively pre-dispatch")
	}
	if !errors.As(ErrReadResponseBudgetExhausted, &diagnostic) || !diagnostic.DiagnosticWriteAttempted() {
		t.Fatal("response exhaustion was not classified as attempted")
	}
}

func TestNewReadBudgetWithUsageResumesExactCounters(t *testing.T) {
	budget, err := NewReadBudgetWithUsage(3, 10, ReadBudgetUsage{Attempts: 2, ResponseBytes: 7})
	if err != nil {
		t.Fatal(err)
	}
	if got := budget.Usage(); got != (ReadBudgetUsage{Attempts: 2, ResponseBytes: 7}) {
		t.Fatalf("usage = %#v", got)
	}
	if err := budget.TakeAttempt(); err != nil {
		t.Fatal(err)
	}
	if err := budget.TakeAttempt(); err != ErrReadAttemptBudgetExhausted {
		t.Fatalf("attempt exhaustion = %v", err)
	}
	remaining, finish, err := budget.BeginResponse(context.Background())
	if err != nil || remaining != 3 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
	finish(3)
	if got := budget.Usage(); got != (ReadBudgetUsage{Attempts: 3, ResponseBytes: 10}) {
		t.Fatalf("final usage = %#v", got)
	}
	for _, usage := range []ReadBudgetUsage{{Attempts: -1}, {Attempts: 4}, {ResponseBytes: -1}, {ResponseBytes: 11}} {
		if budget, err := NewReadBudgetWithUsage(3, 10, usage); err == nil || budget != nil {
			t.Fatalf("usage=%#v budget=%#v err=%v", usage, budget, err)
		}
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

func TestWriteClearanceAndReadIntentAreIndependentOptInMarkers(t *testing.T) {
	ctx := context.Background()
	if HasWriteClearance(ctx) || ReadIntent(ctx) {
		t.Fatal("background context unexpectedly carries request authorization markers")
	}
	writeContext := WithWriteClearance(ctx)
	if !HasWriteClearance(writeContext) || ReadIntent(writeContext) {
		t.Fatal("write-clearance context did not preserve marker independence")
	}
	readContext := WithReadIntent(ctx)
	if HasWriteClearance(readContext) || !ReadIntent(readContext) {
		t.Fatal("read-intent context did not preserve marker independence")
	}
	both := WithReadIntent(writeContext)
	if !HasWriteClearance(both) || !ReadIntent(both) {
		t.Fatal("nested request authorization markers were not preserved")
	}
}

func TestNoReplayRetriesIsIndependentFromSingleAttempt(t *testing.T) {
	ctx := context.Background()
	if NoReplayRetries(ctx) || SingleAttempt(ctx) {
		t.Fatal("background context unexpectedly carries retry policy")
	}
	noReplay := WithNoReplayRetries(ctx)
	if !NoReplayRetries(noReplay) || SingleAttempt(noReplay) {
		t.Fatal("no-replay policy unexpectedly became single-attempt")
	}
	single := WithSingleAttempt(ctx)
	if !NoReplayRetries(single) || !SingleAttempt(single) {
		t.Fatal("single-attempt policy did not also disable retries")
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

func TestChildReadBudgetAttemptsChargeEveryScopeAtomically(t *testing.T) {
	parent, err := NewReadBudget(1, 10)
	if err != nil {
		t.Fatal(err)
	}
	child, err := NewChildReadBudget(parent, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.TakeAttempt(); err != nil {
		t.Fatal(err)
	}
	if err := child.TakeAttempt(); !errors.Is(err, ErrReadAttemptBudgetExhausted) {
		t.Fatalf("second child attempt = %v", err)
	}
	if got := child.Usage().Attempts; got != 1 {
		t.Fatalf("child attempts = %d, want 1", got)
	}
	if got := parent.Usage().Attempts; got != 1 {
		t.Fatalf("parent attempts = %d, want 1", got)
	}

	standalone, err := NewChildReadBudget(nil, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := standalone.TakeAttempt(); err != nil {
		t.Fatalf("nil-parent child attempt: %v", err)
	}
}

func TestSiblingReadBudgetsCannotOversubscribeParent(t *testing.T) {
	parent, _ := NewReadBudget(31, 37)
	left, _ := NewChildReadBudget(parent, 31, 37)
	right, _ := NewChildReadBudget(parent, 31, 37)
	children := []*ReadBudget{left, right}

	var admitted atomic.Int32
	var wg sync.WaitGroup
	for index := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := children[index%len(children)]
			if err := child.TakeAttempt(); err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, ErrReadAttemptBudgetExhausted) {
				t.Errorf("TakeAttempt error = %v", err)
			}
			remaining, finish, err := child.BeginResponse(context.Background())
			if err != nil {
				t.Errorf("BeginResponse error = %v", err)
				return
			}
			if remaining > 0 {
				finish(1)
			} else {
				finish(0)
			}
		}()
	}
	wg.Wait()
	if got := int(admitted.Load()); got != 31 {
		t.Fatalf("admitted attempts = %d, want 31", got)
	}
	if got := parent.Usage(); got != (ReadBudgetUsage{Attempts: 31, ResponseBytes: 37}) {
		t.Fatalf("parent usage = %+v", got)
	}
	if total := left.Usage().ResponseBytes + right.Usage().ResponseBytes; total != 37 {
		t.Fatalf("sibling response bytes = %d, want 37", total)
	}
}

func TestChildReadBudgetResponseChargesExactlyOnceAndCancellationReleasesAncestors(t *testing.T) {
	parent, _ := NewReadBudget(1, 9)
	child, _ := NewChildReadBudget(parent, 1, 7)
	remaining, finish, err := child.BeginResponse(context.Background())
	if err != nil || remaining != 7 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
	finish(5)
	finish(7)
	if got := child.Usage().ResponseBytes; got != 5 {
		t.Fatalf("child response bytes = %d, want 5", got)
	}
	if got := parent.Usage().ResponseBytes; got != 5 {
		t.Fatalf("parent response bytes = %d, want 5", got)
	}

	// Force cancellation after the parent gate is acquired but before the
	// child gate is available, then prove the parent gate was released.
	childLeaf := child.scopes[len(child.scopes)-1]
	<-childLeaf.responseGate
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, beginErr := child.BeginResponse(ctx)
		done <- beginErr
	}()
	for spins := 0; len(parent.scopes[0].responseGate) != 0 && spins < 10_000; spins++ {
		runtime.Gosched()
	}
	if len(parent.scopes[0].responseGate) != 0 {
		t.Fatal("child waiter did not acquire parent gate")
	}
	cancel()
	if beginErr := <-done; !errors.Is(beginErr, context.Canceled) {
		t.Fatalf("canceled BeginResponse = %v", beginErr)
	}
	childLeaf.responseGate <- struct{}{}
	remaining, release, err := parent.BeginResponse(context.Background())
	if err != nil || remaining != 4 {
		t.Fatalf("parent gate after cancellation remaining=%d err=%v", remaining, err)
	}
	release(0)
}

func TestReadBudgetCopiedHandlesAliasCanonicalScopes(t *testing.T) {
	parent, _ := NewReadBudget(2, 8)
	child, _ := NewChildReadBudget(parent, 2, 6)
	copiedParent := *parent
	copiedChild := *child
	if err := copiedChild.TakeAttempt(); err != nil {
		t.Fatal(err)
	}
	remaining, finish, err := copiedChild.BeginResponse(context.Background())
	if err != nil || remaining != 6 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
	finish(4)
	if child.Usage() != (ReadBudgetUsage{Attempts: 1, ResponseBytes: 4}) || copiedChild.Usage() != child.Usage() {
		t.Fatalf("child=%+v copy=%+v", child.Usage(), copiedChild.Usage())
	}
	if parent.Usage() != (ReadBudgetUsage{Attempts: 1, ResponseBytes: 4}) || copiedParent.Usage() != parent.Usage() {
		t.Fatalf("parent=%+v copy=%+v", parent.Usage(), copiedParent.Usage())
	}
}

func TestReadBudgetDeepChainUsesMinimumCapacityAndClampedIdempotentFinish(t *testing.T) {
	root, _ := NewReadBudget(3, 9)
	middle, _ := NewChildReadBudget(root, 2, 5)
	leaf, _ := NewChildReadBudget(middle, 1, 7)
	remaining, finish, err := leaf.BeginResponse(context.Background())
	if err != nil || remaining != 5 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
	finish(99)
	finish(1)
	for name, budget := range map[string]*ReadBudget{"root": root, "middle": middle, "leaf": leaf} {
		if got := budget.Usage().ResponseBytes; got != 5 {
			t.Fatalf("%s response bytes=%d want=5", name, got)
		}
	}
	if err := leaf.TakeAttempt(); err != nil {
		t.Fatal(err)
	}
	if err := leaf.TakeAttempt(); !errors.Is(err, ErrReadAttemptBudgetExhausted) {
		t.Fatalf("leaf exhaustion=%v", err)
	}
	if root.Usage().Attempts != 1 || middle.Usage().Attempts != 1 || leaf.Usage().Attempts != 1 {
		t.Fatalf("attempt usage root=%+v middle=%+v leaf=%+v", root.Usage(), middle.Usage(), leaf.Usage())
	}
}
