// Package httpx is the shared HTTP infrastructure: a thin client with bearer
// auth, bounded idempotency-aware retries (429 for any method; transport/5xx
// only for idempotent methods, with jittered backoff + capped Retry-After),
// JSON helpers, and status→domain-error mapping. Redirects are confined to the
// configured backend host. Adapters use it so they hold no transport policy.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const (
	defaultTimeout = 60 * time.Second
	maxRetries     = 3
	userAgent      = "atl-cli"
	// maxRetryAfter caps an honored Retry-After so a hostile or misconfigured
	// backend cannot pin the CLI for an arbitrary duration.
	maxRetryAfter = 30 * time.Second
	// jsonBodyCap bounds JSON responses; binary downloads use a larger cap.
	jsonBodyCap = 64 << 20 // 64 MiB
	binBodyCap  = 1 << 30  // 1 GiB
)

// traceWriter, when non-nil, receives a one-line trace of every request and
// response (method, URL, status). It is a package-level toggle set by the CLI's
// --verbose/ATL_VERBOSE wiring before any request runs. The bearer token is
// never written here. The RWMutex makes the toggle safe even if a future test
// flips it while a request is in flight.
var (
	traceMu     sync.RWMutex
	traceWriter io.Writer
)

// SetTrace enables (w != nil) or disables (w == nil) HTTP request tracing for
// all clients. Pass a stderr-like writer to turn it on.
func SetTrace(w io.Writer) {
	traceMu.Lock()
	traceWriter = w
	traceMu.Unlock()
}

func tracef(format string, a ...any) {
	traceMu.RLock()
	w := traceWriter
	traceMu.RUnlock()
	if w != nil {
		fmt.Fprintf(w, format, a...)
	}
}

// Client is a per-backend HTTP client (one for Confluence, one for Jira).
type Client struct {
	base     string
	baseHost string
	token    string
	hc       *http.Client
	ver      string // CLI version, for User-Agent
}

// New builds a client for a backend base URL with a bearer PAT.
func New(base, token, version string) *Client {
	base = strings.TrimRight(base, "/")
	host := ""
	if u, err := neturl.Parse(base); err == nil {
		host = u.Host
	}
	return &Client{
		base:     base,
		baseHost: host,
		token:    token,
		ver:      version,
		hc: &http.Client{
			Timeout: defaultTimeout,
			// Refuse any redirect that leaves the configured backend host or
			// downgrades https→http. Confluence/Jira Data Center serve downloads
			// from the same host, so same-host redirects suffice; this closes the
			// same-host scheme-downgrade PAT leak and redirect-based SSRF.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if !sameHost(host, req.URL.Host) {
					return fmt.Errorf("refusing cross-host redirect to %q", req.URL.Host)
				}
				if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
					return fmt.Errorf("refusing https→http redirect to %q", req.URL.Host)
				}
				return nil
			},
		},
	}
}

// Base returns the backend base URL.
func (c *Client) Base() string { return c.base }

// APIError carries the HTTP status and body and unwraps to a domain sentinel so
// the CLI can map it to an exit code.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
	kind   error
}

func (e *APIError) Error() string {
	msg := e.Body
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return fmt.Sprintf("%s %s → HTTP %d: %s", e.Method, e.Path, e.Status, strings.TrimSpace(msg))
}

func (e *APIError) Unwrap() error { return e.kind }

// sameHost reports whether a server-supplied URL host matches the configured
// backend host. An empty request host means a base-relative path (same host).
func sameHost(base, reqHost string) bool {
	return reqHost == "" || strings.EqualFold(reqHost, base)
}

func classify(status int) error {
	switch {
	case status == http.StatusBadRequest:
		return domain.ErrUsage
	case status == http.StatusUnauthorized:
		return domain.ErrAuth
	case status == http.StatusForbidden:
		return domain.ErrForbidden
	case status == http.StatusNotFound:
		return domain.ErrNotFound
	case status == http.StatusConflict:
		return domain.ErrVersionConflict
	default:
		return nil
	}
}

// idempotent reports whether a method is safe to retry on transport error or
// 5xx. POST is excluded: a committed-but-lost POST would double-execute.
func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

// Do issues a request with retries and returns the raw response body on 2xx.
// path may be absolute (starts with http) or relative to base. JSON responses
// are bounded at jsonBodyCap.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, headers map[string]string) ([]byte, error) {
	return c.do(ctx, method, path, body, headers, jsonBodyCap)
}

// do is the retry/transport core shared by Do (JSON cap) and GetBytes (binary
// cap). maxBytes bounds the response body; exceeding it is an error, not a
// silent truncation.
func (c *Client) do(ctx context.Context, method, path string, body []byte, headers map[string]string, maxBytes int64) ([]byte, error) {
	// path may be a relative path (joined to base) or an absolute URL drawn from a
	// server response (e.g. an attachment "content" link). Classify by scheme via
	// url.IsAbs, not a "http" prefix: the prefix mis-reads a relative path like
	// "httpcache/..." as absolute and a mixed-case "HTTPS://..." as relative.
	url := path
	u, err := neturl.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if u.IsAbs() {
		// Refuse any absolute URL that points off the configured backend host to
		// avoid blind SSRF; never issue the request.
		if !sameHost(c.baseHost, u.Host) {
			return nil, fmt.Errorf("refusing request to foreign host %q", u.Host)
		}
	} else {
		url = c.base + path
	}
	var lastErr error
	skipBackoff := false
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 && !skipBackoff {
			if !sleep(ctx, backoff(attempt)) {
				return nil, ctx.Err()
			}
		}
		skipBackoff = false
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return nil, err
		}
		// Only ever send the PAT to the configured backend host. A path may be
		// an absolute URL drawn from a server response (e.g. a Jira attachment
		// "content" link); if it points elsewhere we must NOT leak the token.
		if sameHost(c.baseHost, req.URL.Host) {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent+"/"+c.ver)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		tracef("→ %s %s\n", method, req.URL.String())
		resp, err := c.hc.Do(req)
		if err != nil {
			tracef("× %s %s (transport error: %v)\n", method, req.URL.String(), err)
			// A committed-but-lost POST would double-execute if retried; only
			// idempotent methods retry on transport errors.
			if !idempotent(method) {
				return nil, err
			}
			lastErr = err
			continue // network error → retry
		}
		tracef("← %d %s\n", resp.StatusCode, req.URL.Path)
		data, err := readBody(resp.Body, maxBytes)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return data, nil
		}
		apiErr := &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(data), kind: classify(resp.StatusCode)}
		// 429 is returned before the request is processed, so it is safe to retry
		// for any method. Other transient failures (transport, 5xx) only retry
		// for idempotent methods.
		retryable := resp.StatusCode == http.StatusTooManyRequests ||
			(resp.StatusCode >= 500 && idempotent(method))
		if retryable {
			lastErr = apiErr
			if ra := retryAfter(resp); ra > 0 {
				if !sleep(ctx, ra) {
					return nil, ctx.Err()
				}
				skipBackoff = true // already waited per Retry-After; no double sleep
			}
			continue // transient → retry
		}
		return nil, apiErr // permanent → stop
	}
	return nil, lastErr
}

// readBody reads up to max bytes, returning an error if the body is larger
// (rather than silently truncating) or if the read itself fails.
func readBody(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("response body exceeds %d bytes", max)
	}
	return data, nil
}

// GetJSON GETs path and unmarshals into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	data, err := c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	return unmarshal(data, out)
}

// SendJSON marshals in, sends it with method, and unmarshals the response into
// out (out may be nil to ignore the body).
func (c *Client) SendJSON(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = b
	}
	data, err := c.Do(ctx, method, path, body, nil)
	if err != nil {
		return err
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return unmarshal(data, out)
}

// GetBytes returns the raw bytes of a GET (binary downloads), with the
// content-type. Used for attachment/asset downloads, so it allows a larger
// response body than the JSON cap.
func (c *Client) GetBytes(ctx context.Context, path string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, nil, map[string]string{"Accept": "*/*"}, binBodyCap)
}

func unmarshal(data []byte, out any) error {
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
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
