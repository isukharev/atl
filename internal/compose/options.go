package compose

import (
	"io"
	"reflect"
	"sync"

	confluenceadapter "github.com/isukharev/atl/internal/adapter/confluence"
	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/domain"
)

// Option configures invocation-scoped concrete adapter construction.
type Option func(*options)

type options struct {
	trace io.Writer
}

type synchronizedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// WithTrace supplies the trace sink inherited by every backend client built by
// this composition, including lazy optional siblings. A nil writer is silent.
func WithTrace(w io.Writer) Option {
	if writerIsNil(w) {
		return func(options *options) { options.trace = nil }
	}
	shared := &synchronizedWriter{w: w}
	return func(options *options) { options.trace = shared }
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

func resolveOptions(values []Option) options {
	var resolved options
	for _, value := range values {
		if value != nil {
			value(&resolved)
		}
	}
	return resolved
}

func confluenceOptions(authorizer domain.WriteAuthorizer, resolved options) []confluenceadapter.Option {
	var values []confluenceadapter.Option
	if authorizer != nil {
		values = append(values, confluenceadapter.WithWriteAuthorizer(authorizer))
	}
	if resolved.trace != nil {
		values = append(values, confluenceadapter.WithTrace(resolved.trace))
	}
	return values
}

func jiraOptions(authorizer domain.WriteAuthorizer, resolved options) []jiraadapter.Option {
	var values []jiraadapter.Option
	if authorizer != nil {
		values = append(values, jiraadapter.WithWriteAuthorizer(authorizer))
	}
	if resolved.trace != nil {
		values = append(values, jiraadapter.WithTrace(resolved.trace))
	}
	return values
}
