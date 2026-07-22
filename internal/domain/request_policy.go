package domain

import "context"

type singleAttemptContextKey struct{}
type redactHTTPTraceContextKey struct{}

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
