package domain

import "context"

type singleAttemptContextKey struct{}

// WithSingleAttempt disables a transport's generic replay-safe retry loop for
// the request. Redirect policy remains transport-owned and unchanged.
func WithSingleAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, singleAttemptContextKey{}, true)
}

// SingleAttempt reports whether the caller requires one transport attempt.
func SingleAttempt(ctx context.Context) bool {
	requested, _ := ctx.Value(singleAttemptContextKey{}).(bool)
	return requested
}
