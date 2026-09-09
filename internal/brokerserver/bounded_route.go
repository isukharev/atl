package brokerserver

import (
	"context"
	"net/http"
	"time"
)

// beginBoundedRoute anchors even a wall-only parent deadline to the local
// monotonic start, before body receipt or authentication. The write deadline
// survives final flush; unfinished request bodies retain their read bound.
// net/http may clear the read deadline after body EOF to start its background
// next-request read. It owns the subsequent keepalive deadline transitions.
func beginBoundedRoute(writer http.ResponseWriter, request *http.Request, started time.Time, maximum time.Duration) (*http.Request, func(), error) {
	remaining := maximum
	if parent, ok := request.Context().Deadline(); ok && parent.Sub(started) < remaining {
		remaining = parent.Sub(started)
	}
	deadline := started.Add(remaining)
	controller := http.NewResponseController(writer)
	if err := controller.SetReadDeadline(deadline); err != nil {
		return nil, nil, err
	}
	if err := controller.SetWriteDeadline(deadline); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithDeadline(request.Context(), deadline)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		now := time.Now()
		_ = controller.SetReadDeadline(now)
		_ = controller.SetWriteDeadline(now)
	})
	finish := func() {
		if !stop() {
			<-done
		}
		cancel()
	}
	return request.WithContext(ctx), finish, nil
}

func (h *Handler) publishBounded(request *http.Request, writer http.ResponseWriter, body []byte, deadline time.Time, correlation string) (bool, error) {
	started, err := h.publish(request.Context(), writer, body, deadline, correlation)
	if err != nil || !started {
		return started, err
	}
	// Write may only have filled net/http's buffer. Flush while the operation
	// context is still alive and the clipped connection deadline is installed.
	if err := request.Context().Err(); err != nil {
		return true, err
	}
	return true, http.NewResponseController(writer).Flush()
}
