package httpx

import (
	"context"
	"net/http"
	"sync"

	"github.com/isukharev/atl/internal/domain"
)

type downloadRedirectCancelKey struct{}

type redirectIdleTransport struct{ base http.RoundTripper }

// RoundTrip bounds the body drain performed inside http.Client before a
// redirect is followed or refused. Final bodies retain their existing lazy
// budget reservation and idle policy.
func (t redirectIdleTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil || response.Body == nil {
		return response, err
	}
	cancel, ok := request.Context().Value(downloadRedirectCancelKey{}).(context.CancelFunc)
	if !ok || !replaySafe(request.Method) || domain.SingleAttempt(request.Context()) || response.Header.Get("Location") == "" {
		return response, nil
	}
	switch response.StatusCode {
	case 301, 302, 303, 307, 308:
		body := &redirectIdleBody{}
		body.idleReader = newIdleReader(response.Body, downloadIdleTimeout, func() {
			body.mu.Lock()
			defer body.mu.Unlock()
			if !body.closed {
				cancel()
			}
		})
		response.Body = body
	}
	return response, nil
}

type redirectIdleBody struct {
	*idleReader
	mu     sync.Mutex
	closed bool
}

func (body *redirectIdleBody) Close() error {
	body.mu.Lock()
	body.closed = true
	body.mu.Unlock()
	body.timer.Stop()
	// Normal redirect cleanup must not cancel the next hop's context.
	return body.rc.Close()
}
