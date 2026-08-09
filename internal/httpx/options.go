package httpx

import (
	"context"
	"fmt"
	"io"
	neturl "net/url"
	"reflect"

	"github.com/isukharev/atl/internal/domain"
)

// Option configures immutable, per-client transport behavior.
type Option func(*clientOptions)

type clientOptions struct {
	trace                 io.Writer
	genericConflict       bool
	requireWriteClearance bool
}

// WithTrace writes content-safe request and response trace lines to w. A nil
// writer leaves tracing disabled for this client.
func WithTrace(w io.Writer) Option {
	if writerIsNil(w) {
		w = nil
	}
	return func(options *clientOptions) { options.trace = w }
}

func writerIsNil(w io.Writer) bool {
	if w == nil {
		return true
	}
	value := reflect.ValueOf(w)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// WithGenericConflict keeps HTTP 409 as a generic API error instead of mapping
// it to the optimistic-version sentinel. Jira uses this because it has no
// version gate with re-pull/force recovery.
func WithGenericConflict() Option {
	return func(options *clientOptions) { options.genericConflict = true }
}

// WithRequiredWriteClearance enables the last-hop assertion that every
// non-replay-safe request carries reviewed write clearance or read intent.
func WithRequiredWriteClearance() Option {
	return func(options *clientOptions) { options.requireWriteClearance = true }
}

func resolveOptions(options []Option) clientOptions {
	var resolved clientOptions
	for _, option := range options {
		if option != nil {
			option(&resolved)
		}
	}
	return resolved
}

func traceRequestURL(ctx context.Context, u *neturl.URL) string {
	if domain.RedactedHTTPTrace(ctx) {
		return "<redacted>"
	}
	return traceURL(u)
}

func traceResponsePath(ctx context.Context, path string) string {
	if domain.RedactedHTTPTrace(ctx) {
		return "<redacted>"
	}
	return path
}

func (c *Client) tracef(format string, values ...any) {
	if c.trace == nil {
		return
	}
	c.traceMu.Lock()
	defer c.traceMu.Unlock()
	_, _ = fmt.Fprintf(c.trace, format, values...)
}
