package httpx

import "net/http"

type idleConnectionCloser interface{ CloseIdleConnections() }

func closeIdleConnections(transport http.RoundTripper) {
	if closer, ok := transport.(idleConnectionCloser); ok {
		closer.CloseIdleConnections()
	}
}

func (t readBudgetTransport) CloseIdleConnections()   { closeIdleConnections(t.base) }
func (t redirectIdleTransport) CloseIdleConnections() { closeIdleConnections(t.base) }
func (t scheduledRoundTripper) CloseIdleConnections() { closeIdleConnections(t.base) }
func (t strictDispatchTransport) CloseIdleConnections() {
	closeIdleConnections(t.ordinary)
	closeIdleConnections(t.strict)
}
