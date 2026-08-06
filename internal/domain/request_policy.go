package domain

import (
	"context"
	"fmt"
	"sync"
)

type singleAttemptContextKey struct{}
type redactHTTPTraceContextKey struct{}
type readBudgetContextKey struct{}
type writeClearanceContextKey struct{}
type readIntentContextKey struct{}

// ReadBudgetExhaustedError is a content-free transport classification. Static
// instances below let callers distinguish which closed budget dimension was
// exhausted without exposing a request URL, response body, or live counters.
type ReadBudgetExhaustedError struct {
	dimension readBudgetDimension
}

type readBudgetDimension uint8

const (
	readBudgetAttempts readBudgetDimension = iota + 1
	readBudgetResponseBytes
)

func (e *ReadBudgetExhaustedError) Error() string {
	switch e.dimension {
	case readBudgetAttempts:
		return "read attempt budget exhausted"
	case readBudgetResponseBytes:
		return "read response-byte budget exhausted"
	default:
		return "read budget exhausted"
	}
}

var (
	// ErrReadAttemptBudgetExhausted means another physical transport attempt
	// was refused before it reached the backend.
	ErrReadAttemptBudgetExhausted = &ReadBudgetExhaustedError{dimension: readBudgetAttempts}
	// ErrReadResponseBudgetExhausted means buffering another response byte
	// would exceed the command-scoped aggregate limit.
	ErrReadResponseBudgetExhausted = &ReadBudgetExhaustedError{dimension: readBudgetResponseBytes}
)

// ReadBudgetUsage is an atomic snapshot of accepted physical attempts and
// buffered response bytes. Neither value can exceed its configured maximum.
type ReadBudgetUsage struct {
	Attempts      int
	ResponseBytes int64
}

// ReadBudget is an opt-in command-scoped hard limit shared by every request
// carrying it. Its counters and response reservations are safe for concurrent
// clients and transports.
type ReadBudget struct {
	mu sync.Mutex

	maxAttempts      int
	attempts         int
	maxResponseBytes int64
	responseBytes    int64

	// Only one response is admitted against the remaining aggregate byte
	// allowance at a time. This prevents concurrent readers from each
	// buffering against the same remaining bytes.
	responseGate chan struct{}
}

// NewReadBudget constructs a finite physical-attempt and aggregate
// response-byte budget. Zero is a valid closed limit for either dimension.
func NewReadBudget(maxAttempts int, maxResponseBytes int64) (*ReadBudget, error) {
	if maxAttempts < 0 {
		return nil, fmt.Errorf("read attempt budget must be non-negative")
	}
	if maxResponseBytes < 0 {
		return nil, fmt.Errorf("read response-byte budget must be non-negative")
	}
	b := &ReadBudget{
		maxAttempts:      maxAttempts,
		maxResponseBytes: maxResponseBytes,
		responseGate:     make(chan struct{}, 1),
	}
	b.responseGate <- struct{}{}
	return b, nil
}

// WithReadBudget carries budget through every request derived from ctx. A nil
// budget is deliberately inert, matching an absent budget.
func WithReadBudget(ctx context.Context, budget *ReadBudget) context.Context {
	if budget == nil {
		return ctx
	}
	return context.WithValue(ctx, readBudgetContextKey{}, budget)
}

// ReadBudgetFromContext returns the shared budget, or nil when this command did
// not opt in to transport budgeting.
func ReadBudgetFromContext(ctx context.Context) *ReadBudget {
	if ctx == nil {
		return nil
	}
	budget, _ := ctx.Value(readBudgetContextKey{}).(*ReadBudget)
	return budget
}

// TakeAttempt atomically admits one physical transport attempt. Exhaustion is
// reported before the request reaches the underlying RoundTripper.
func (b *ReadBudget) TakeAttempt() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.attempts >= b.maxAttempts {
		return ErrReadAttemptBudgetExhausted
	}
	b.attempts++
	return nil
}

// BeginResponse serializes buffering against the remaining aggregate byte
// allowance. The caller must invoke finish exactly once with the accepted byte
// count; values outside [0, remaining] are clamped so usage never exceeds the
// configured maximum.
func (b *ReadBudget) BeginResponse(ctx context.Context) (remaining int64, finish func(int64), err error) {
	if b == nil {
		return 0, func(int64) {}, nil
	}
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case <-b.responseGate:
	}

	b.mu.Lock()
	remaining = b.maxResponseBytes - b.responseBytes
	b.mu.Unlock()

	var once sync.Once
	finish = func(consumed int64) {
		once.Do(func() {
			if consumed < 0 {
				consumed = 0
			}
			if consumed > remaining {
				consumed = remaining
			}
			b.mu.Lock()
			b.responseBytes += consumed
			b.mu.Unlock()
			b.responseGate <- struct{}{}
		})
	}
	return remaining, finish, nil
}

// Usage returns a consistent snapshot of the accepted budget consumption.
func (b *ReadBudget) Usage() ReadBudgetUsage {
	if b == nil {
		return ReadBudgetUsage{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return ReadBudgetUsage{Attempts: b.attempts, ResponseBytes: b.responseBytes}
}

// WithSingleAttempt limits a request to one transport hop: the generic
// replay-safe retry loop is disabled and redirect responses are not followed.
func WithSingleAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, singleAttemptContextKey{}, true)
}

// SingleAttempt reports whether the caller requires one transport attempt.
func SingleAttempt(ctx context.Context) bool {
	requested, _ := ctx.Value(singleAttemptContextKey{}).(bool)
	return requested
}

// WithWriteClearance records that an application authorization decision
// admitted the write represented by this request context.
func WithWriteClearance(ctx context.Context) context.Context {
	return context.WithValue(ctx, writeClearanceContextKey{}, true)
}

// HasWriteClearance reports whether the request context carries an admitted
// application authorization decision.
func HasWriteClearance(ctx context.Context) bool {
	cleared, _ := ctx.Value(writeClearanceContextKey{}).(bool)
	return cleared
}

// WithReadIntent marks a reviewed read that uses a non-replay-safe HTTP method.
func WithReadIntent(ctx context.Context) context.Context {
	return context.WithValue(ctx, readIntentContextKey{}, true)
}

// ReadIntent reports whether a non-replay-safe request is a reviewed read.
func ReadIntent(ctx context.Context) bool {
	read, _ := ctx.Value(readIntentContextKey{}).(bool)
	return read
}

// WithRedactedHTTPTrace prevents request identity from appearing in verbose
// transport traces. It is used by aggregate-only probes whose public contract
// intentionally omits resource ids and paths.
func WithRedactedHTTPTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, redactHTTPTraceContextKey{}, true)
}

// RedactedHTTPTrace reports whether verbose transport traces must omit request
// URLs and response paths for this context.
func RedactedHTTPTrace(ctx context.Context) bool {
	requested, _ := ctx.Value(redactHTTPTraceContextKey{}).(bool)
	return requested
}
