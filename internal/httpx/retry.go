package httpx

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxRetries = 3
	// maxRetryAfter caps an honored Retry-After so a hostile or misconfigured
	// backend cannot pin the CLI for an arbitrary duration.
	maxRetryAfter = 30 * time.Second
)

// replaySafe reports whether the generic transport may repeat a request after
// an ambiguous response. Writes deliberately require endpoint-aware
// reconciliation rather than relying on HTTP's broad idempotency definition.
func replaySafe(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return true
	default:
		return false
	}
}

// backoff returns an exponential delay with full jitter (a random duration in
// [d/2, d]) to avoid a thundering herd. The base is capped at 5s before jitter.
func backoff(attempt int) time.Duration {
	d := time.Duration(200<<attempt) * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	half := d / 2
	// Jitter is retry timing, not a security primitive; a non-crypto PRNG is
	// intentional here.
	return half + time.Duration(rand.Int64N(int64(half)+1)) //nolint:gosec // G404: jitter is non-cryptographic by design
}

// retryAfter parses a Retry-After header (integer seconds or RFC 7231
// HTTP-date), clamping the result to [0, maxRetryAfter] so a hostile value
// cannot pin the CLI. A missing/invalid header or a past date yields 0.
func retryAfter(resp *http.Response) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return clampRetryAfter(time.Duration(secs) * time.Second)
	}
	if t, err := http.ParseTime(v); err == nil {
		return clampRetryAfter(time.Until(t))
	}
	return 0
}

func clampRetryAfter(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
