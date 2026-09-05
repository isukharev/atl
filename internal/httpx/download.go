package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/isukharev/atl/internal/domain"
)

// GetStream GETs path and returns the response body as a stream (binary
// downloads). Retries/backoff apply only until the 2xx headers arrive; the
// body is then consumed by the caller, bounded by an inactivity deadline
// instead of the JSON client's whole-request timeout. A transport error after
// successful headers is never retried.
func (c *Client) GetStream(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := validateNoReplayReadBudget(ctx); err != nil {
		return nil, err
	}
	url, err := c.resolveURL(path)
	if err != nil {
		return nil, err
	}
	var lastErr error
	skipBackoff := false
	retries := maxRetries
	if domain.NoReplayRetries(ctx) {
		retries = 0
	}
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 && !skipBackoff {
			if !sleep(ctx, backoff(attempt)) {
				return nil, ctx.Err()
			}
		}
		skipBackoff = false
		// A per-attempt cancel lets the idle watchdog abort a stalled body
		// without touching the caller's context.
		rctx, cancel := context.WithCancel(ctx)
		rctx = context.WithValue(rctx, downloadRedirectCancelKey{}, cancel)
		req, err := c.newRequest(rctx, http.MethodGet, url, nil, map[string]string{"Accept": "*/*"})
		if err != nil {
			cancel()
			return nil, err
		}
		c.tracef("→ GET %s\n", traceRequestURL(ctx, req.URL))
		resp, err := c.dl.Do(req)
		if err != nil {
			interrupted := rctx.Err() != nil
			cancel()
			c.tracef("× GET %s (transport error: %s)\n", traceRequestURL(ctx, req.URL), transportErrorCategory(err))
			if budgetErr := readBudgetExhaustion(err); budgetErr != nil {
				return nil, budgetErr
			}
			lastErr = transportError(http.MethodGet, req.URL, err)
			if interrupted || errors.Is(err, errRedirectLimit) {
				return nil, lastErr
			}
			continue // GET is idempotent → retry
		}
		c.tracef("← %d %s\n", resp.StatusCode, traceResponsePath(ctx, req.URL.Path))
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return newDownloadStream(ctx, resp.Body, cancel), nil
		}
		result := c.classifyAttempt(http.MethodGet, resp)
		data, readErr := readIdleResponseBody(ctx, resp.Body, jsonBodyCap, downloadIdleTimeout, cancel)
		_ = resp.Body.Close()
		cancel()
		if readErr != nil {
			return nil, readErr
		}
		apiErr := &APIError{Status: resp.StatusCode, Method: http.MethodGet, Path: path, Body: string(data), kind: result.kind}
		if result.retryable && !domain.NoReplayRetries(ctx) {
			lastErr = apiErr
			if result.retryDelay > 0 {
				if !sleep(ctx, result.retryDelay) {
					return nil, ctx.Err()
				}
				skipBackoff = true
			}
			continue
		}
		return nil, apiErr
	}
	return nil, lastErr
}

func validateNoReplayReadBudget(ctx context.Context) error {
	if domain.NoReplayRetries(ctx) && !domain.SingleAttempt(ctx) && domain.ReadBudgetFromContext(ctx) == nil {
		return fmt.Errorf("%w: no-replay read requires a finite physical-attempt budget", domain.ErrCheckFailed)
	}
	return nil
}
