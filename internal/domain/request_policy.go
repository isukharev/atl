package domain

import (
	"context"
	"fmt"
	"sync"
)

type singleAttemptContextKey struct{}
type noReplayRetriesContextKey struct{}
type redactHTTPTraceContextKey struct{}
type readBudgetContextKey struct{}
type writeClearanceContextKey struct{}
type readIntentContextKey struct{}
type untrustedConfluenceReferenceContextKey struct{}
type confluenceCommentContainmentContextKey struct{}

type confluenceCommentContainment struct {
	pageID   string
	threadID string
}

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

// DiagnosticWriteAttempted classifies attempt admission as definitively
// pre-dispatch while preserving response exhaustion as an attempted outcome.
func (e *ReadBudgetExhaustedError) DiagnosticWriteAttempted() bool {
	return e == nil || e.dimension != readBudgetAttempts
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

type readBudgetScope struct {
	mu               sync.Mutex
	maxAttempts      int
	attempts         int
	maxResponseBytes int64
	responseBytes    int64
	responseGate     chan struct{}
}

// ReadBudget is a copy-safe handle over immutable canonical scope identities.
// Copying the handle aliases its counters and gates; it never copies a mutex.
type ReadBudget struct {
	scopes []*readBudgetScope
}

// NewReadBudget constructs a finite physical-attempt and aggregate
// response-byte budget. Zero is a valid closed limit for either dimension.
func NewReadBudget(maxAttempts int, maxResponseBytes int64) (*ReadBudget, error) {
	return NewReadBudgetWithUsage(maxAttempts, maxResponseBytes, ReadBudgetUsage{})
}

// NewReadBudgetWithUsage resumes a command-scoped budget from durably recorded
// physical usage. It is intentionally stricter than a fresh budget: initial
// counters must already fit within the unchanged original maxima.
func NewReadBudgetWithUsage(maxAttempts int, maxResponseBytes int64, usage ReadBudgetUsage) (*ReadBudget, error) {
	return newReadBudget(nil, maxAttempts, maxResponseBytes, usage)
}

// NewChildReadBudget constructs a fresh row-local budget whose accepted
// attempts and response bytes are also charged to parent and every ancestor.
// A nil parent deliberately produces an ordinary standalone budget.
func NewChildReadBudget(parent *ReadBudget, maxAttempts int, maxResponseBytes int64) (*ReadBudget, error) {
	return newReadBudget(parent, maxAttempts, maxResponseBytes, ReadBudgetUsage{})
}

func newReadBudget(parent *ReadBudget, maxAttempts int, maxResponseBytes int64, usage ReadBudgetUsage) (*ReadBudget, error) {
	if maxAttempts < 0 {
		return nil, fmt.Errorf("read attempt budget must be non-negative")
	}
	if maxResponseBytes < 0 {
		return nil, fmt.Errorf("read response-byte budget must be non-negative")
	}
	if usage.Attempts < 0 || usage.Attempts > maxAttempts {
		return nil, fmt.Errorf("initial read attempt usage is out of bounds")
	}
	if usage.ResponseBytes < 0 || usage.ResponseBytes > maxResponseBytes {
		return nil, fmt.Errorf("initial read response-byte usage is out of bounds")
	}
	scope := &readBudgetScope{
		maxAttempts:      maxAttempts,
		attempts:         usage.Attempts,
		maxResponseBytes: maxResponseBytes,
		responseBytes:    usage.ResponseBytes,
		responseGate:     make(chan struct{}, 1),
	}
	scope.responseGate <- struct{}{}
	scopes := make([]*readBudgetScope, 0, 1)
	if parent != nil {
		parentScopes, valid := parent.canonicalScopes()
		if !valid {
			return nil, fmt.Errorf("parent read budget is invalid")
		}
		scopes = make([]*readBudgetScope, len(parentScopes), len(parentScopes)+1)
		copy(scopes, parentScopes)
	}
	scopes = append(scopes, scope)
	return &ReadBudget{scopes: scopes}, nil
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
	scopes, valid := b.canonicalScopes()
	if !valid {
		return ErrReadAttemptBudgetExhausted
	}
	for _, scope := range scopes {
		scope.mu.Lock()
	}
	defer func() {
		for index := len(scopes) - 1; index >= 0; index-- {
			scopes[index].mu.Unlock()
		}
	}()
	for _, scope := range scopes {
		if scope.attempts >= scope.maxAttempts {
			return ErrReadAttemptBudgetExhausted
		}
	}
	for _, scope := range scopes {
		scope.attempts++
	}
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
	scopes, valid := b.canonicalScopes()
	if !valid {
		return 0, nil, ErrReadResponseBudgetExhausted
	}
	acquired := 0
	for _, scope := range scopes {
		select {
		case <-ctx.Done():
			for index := acquired - 1; index >= 0; index-- {
				scopes[index].responseGate <- struct{}{}
			}
			return 0, nil, ctx.Err()
		case <-scope.responseGate:
			acquired++
		}
	}
	if err := ctx.Err(); err != nil {
		for index := acquired - 1; index >= 0; index-- {
			scopes[index].responseGate <- struct{}{}
		}
		return 0, nil, err
	}

	remaining = int64(^uint64(0) >> 1)
	for _, scope := range scopes {
		scope.mu.Lock()
		available := scope.maxResponseBytes - scope.responseBytes
		scope.mu.Unlock()
		if available < remaining {
			remaining = available
		}
	}

	var once sync.Once
	finish = func(consumed int64) {
		once.Do(func() {
			if consumed < 0 {
				consumed = 0
			}
			if consumed > remaining {
				consumed = remaining
			}
			for _, scope := range scopes {
				scope.mu.Lock()
				scope.responseBytes += consumed
				scope.mu.Unlock()
			}
			for index := len(scopes) - 1; index >= 0; index-- {
				scopes[index].responseGate <- struct{}{}
			}
		})
	}
	return remaining, finish, nil
}

// canonicalScopes preserves canonical root-to-leaf order while failing closed
// for an empty or in-package forged handle. Public constructors cannot create
// nil or duplicate identities.
func (b *ReadBudget) canonicalScopes() ([]*readBudgetScope, bool) {
	if b == nil {
		return nil, false
	}
	if len(b.scopes) == 0 {
		return nil, false
	}
	seen := make(map[*readBudgetScope]struct{}, len(b.scopes))
	for _, scope := range b.scopes {
		if scope == nil {
			return nil, false
		}
		if _, duplicate := seen[scope]; duplicate {
			return nil, false
		}
		seen[scope] = struct{}{}
	}
	return b.scopes, true
}

// Usage returns a consistent snapshot of the accepted budget consumption.
func (b *ReadBudget) Usage() ReadBudgetUsage {
	if b == nil {
		return ReadBudgetUsage{}
	}
	scopes, valid := b.canonicalScopes()
	if !valid {
		return ReadBudgetUsage{}
	}
	leaf := scopes[len(scopes)-1]
	leaf.mu.Lock()
	defer leaf.mu.Unlock()
	return ReadBudgetUsage{Attempts: leaf.attempts, ResponseBytes: leaf.responseBytes}
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

// WithNoReplayRetries disables the generic replay-safe retry loop while
// retaining the ordinary redirect policy. Redirects remain limited by the
// caller's physical ReadBudget and the transport's origin/scheme checks.
func WithNoReplayRetries(ctx context.Context) context.Context {
	return context.WithValue(ctx, noReplayRetriesContextKey{}, true)
}

// NoReplayRetries reports whether generic retry replay is disabled. A
// SingleAttempt context is also non-retrying, but additionally refuses every
// redirect after the first physical request.
func NoReplayRetries(ctx context.Context) bool {
	requested, _ := ctx.Value(noReplayRetriesContextKey{}).(bool)
	return requested || SingleAttempt(ctx)
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

// WithUntrustedConfluenceReference marks a content id produced by URL, CQL,
// or short-link resolution. Such an id may contribute deny evidence but must
// never ground a policy allow.
func WithUntrustedConfluenceReference(ctx context.Context) context.Context {
	return context.WithValue(ctx, untrustedConfluenceReferenceContextKey{}, true)
}

// UntrustedConfluenceReference reports whether the current target identity was
// selected by reference resolution rather than supplied as a canonical id.
func UntrustedConfluenceReference(ctx context.Context) bool {
	untrusted, _ := ctx.Value(untrustedConfluenceReferenceContextKey{}).(bool)
	return untrusted
}

// WithConfluenceCommentContainment records app-validated proof that an inline
// comment thread belongs to the exact page used for authorization.
func WithConfluenceCommentContainment(ctx context.Context, pageID, threadID string) context.Context {
	return context.WithValue(ctx, confluenceCommentContainmentContextKey{}, confluenceCommentContainment{pageID: pageID, threadID: threadID})
}

// HasConfluenceCommentContainment checks exact page/thread containment proof.
func HasConfluenceCommentContainment(ctx context.Context, pageID, threadID string) bool {
	proof, _ := ctx.Value(confluenceCommentContainmentContextKey{}).(confluenceCommentContainment)
	return proof.pageID == pageID && proof.threadID == threadID
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
