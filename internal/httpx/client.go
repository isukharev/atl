// Package httpx is the shared HTTP infrastructure: a thin client with bearer
// auth, bounded replay-safe retries for reads (with jittered backoff + capped
// Retry-After), JSON helpers, and status→domain-error mapping. Direct URLs and
// redirects are confined to replay-safe reads at the configured backend
// origin. Adapters use it so they hold no transport policy.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const (
	defaultTimeout = 60 * time.Second
	userAgent      = "atl-cli"
	maxRedirects   = 10
)

// Client is a per-backend HTTP client (one for Confluence, one for Jira).
type Client struct {
	base       string
	baseHost   string
	baseScheme string
	token      string
	hc         *http.Client
	dl         *http.Client // streaming downloads: no whole-request timeout
	ver        string       // CLI version, for User-Agent
	scheduler  *Scheduler
	trace      io.Writer
	traceMu    sync.Mutex
	// genericConflict: this backend has no optimistic version gate, so an HTTP
	// 409 is a generic conflict (locked issue, workflow veto), NOT
	// ErrVersionConflict — exit 5 would point the caller at a re-pull/--force
	// recovery that does not exist there.
	genericConflict bool
	// requireWriteClearance enables the last-hop assertion for clients whose
	// application policy wiring is complete. It remains false by default.
	requireWriteClearance bool
}

// New builds a client for a backend base URL with a bearer PAT.
func New(base, token, version string, options ...Option) *Client {
	return NewWithScheduler(base, token, version, nil, options...)
}

// NewWithScheduler builds a client whose every transport attempt shares the
// supplied command-scoped concurrency/rate policy.
func NewWithScheduler(base, token, version string, scheduler *Scheduler, options ...Option) *Client {
	dlTransport := http.DefaultTransport.(*http.Transport).Clone()
	dlTransport.ResponseHeaderTimeout = dlHeaderTimeout
	return newWithScheduler(base, token, version, scheduler, http.DefaultTransport, dlTransport, resolveOptions(options))
}

// NewWithSchedulerTLS builds a client with an isolated backend-specific trust
// pool. An empty option preserves NewWithScheduler's exact transport shape.
func NewWithSchedulerTLS(base, token, version string, scheduler *Scheduler, tlsOptions TLSOptions, options ...Option) (*Client, error) {
	if !tlsOptions.configured() {
		return NewWithScheduler(base, token, version, scheduler, options...), nil
	}
	u, err := neturl.Parse(base)
	if err != nil || !strings.EqualFold(u.Scheme, "https") {
		return nil, fmt.Errorf("%w: configured CA bundle requires an https backend", domain.ErrConfig)
	}
	transport, err := tlsOptions.transport()
	if err != nil {
		return nil, err
	}
	dlTransport := transport.Clone()
	dlTransport.ResponseHeaderTimeout = dlHeaderTimeout
	return newWithScheduler(base, token, version, scheduler, transport, dlTransport, resolveOptions(options)), nil
}

func newWithScheduler(base, token, version string, scheduler *Scheduler, transport http.RoundTripper, dlTransport http.RoundTripper, options clientOptions) *Client {
	base = strings.TrimRight(base, "/")
	host := ""
	scheme := ""
	if u, err := neturl.Parse(base); err == nil {
		host = u.Host
		scheme = strings.ToLower(u.Scheme)
	}
	// Refuse any redirect that leaves the configured backend host or
	// downgrades https→http. Confluence/Jira Data Center serve downloads
	// from the same host, so same-host redirects suffice; this closes the
	// same-host scheme-downgrade PAT leak and redirect-based SSRF.
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		if !sameHost(host, req.URL.Host) {
			return fmt.Errorf("refusing cross-host redirect to %q", req.URL.Host)
		}
		redirectScheme := strings.ToLower(req.URL.Scheme)
		if redirectScheme != "http" && redirectScheme != "https" {
			return fmt.Errorf("refusing redirect with unsupported scheme %q", req.URL.Scheme)
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme == "http" {
			return fmt.Errorf("refusing https→http redirect to %q", req.URL.Host)
		}
		// Redirect handling is another physical request. A same-origin 307/308
		// preserves the method and body, while 301/302/303 can turn a write into
		// a server-selected read. Neither behavior is safe for a mutation: the
		// endpoint-aware write path must classify and reconcile the original 3xx
		// instead of allowing the shared client to issue a second request.
		if len(via) > 0 && !replaySafe(via[0].Method) {
			return http.ErrUseLastResponse
		}
		// A caller that requested one transport attempt must never turn one
		// logical probe into a second request by following even a safe redirect.
		// ErrUseLastResponse returns the 3xx response to the normal classifier
		// without issuing the redirected request.
		if len(via) > 0 && domain.SingleAttempt(via[0].Context()) {
			return http.ErrUseLastResponse
		}
		if len(via) >= maxRedirects {
			return errRedirectLimit
		}
		return nil
	}
	return &Client{
		base:                  base,
		baseHost:              host,
		baseScheme:            scheme,
		token:                 token,
		ver:                   version,
		scheduler:             scheduler,
		trace:                 options.trace,
		genericConflict:       options.genericConflict,
		requireWriteClearance: options.requireWriteClearance,
		hc: &http.Client{
			Transport:     scheduleTransport(readBudgetTransport{base: transport}, scheduler),
			Timeout:       defaultTimeout,
			CheckRedirect: checkRedirect,
		},
		dl: &http.Client{
			Transport:     scheduleTransport(readBudgetTransport{base: redirectIdleTransport{base: dlTransport}}, scheduler),
			CheckRedirect: checkRedirect,
		},
	}
}

// Base returns the backend base URL.
func (c *Client) Base() string { return c.base }

// Do issues a request with retries and returns the raw response body on 2xx.
// path may be absolute (starts with http) or relative to base. JSON responses
// are bounded at jsonBodyCap.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, headers map[string]string) ([]byte, error) {
	if err := validateNoReplayReadBudget(ctx); err != nil {
		return nil, err
	}
	return c.do(ctx, method, path, body, headers, jsonBodyCap)
}

// DoWithBodyLimit is the bounded raw-body variant for narrowly reviewed
// caller-decoded responses, including bounded JSON and non-JSON endpoints. It
// retains the normal auth, origin, retry, redirect, trace-redaction, status,
// and aggregate read-budget policies. Callers must use a positive limit no
// larger than the ordinary JSON cap.
func (c *Client) DoWithBodyLimit(ctx context.Context, method, path string, body []byte, headers map[string]string, maxBytes int64) ([]byte, error) {
	if err := validateNoReplayReadBudget(ctx); err != nil {
		return nil, err
	}
	if maxBytes <= 0 || maxBytes > jsonBodyCap {
		return nil, fmt.Errorf("invalid response body limit")
	}
	return c.do(ctx, method, path, body, headers, maxBytes)
}

// ResolveGET follows the client's normal redirect policy for one GET and
// returns the final response URL without reading the success body. It is for
// same-origin short-link resolution; callers must still validate the returned
// path as an application-level reference.
func (c *Client) ResolveGET(ctx context.Context, path string) (string, error) {
	if domain.NoReplayRetries(ctx) && !domain.SingleAttempt(ctx) && domain.ReadBudgetFromContext(ctx) == nil {
		return "", fmt.Errorf("%w: no-replay read requires a finite physical-attempt budget", domain.ErrCheckFailed)
	}
	resolved, err := c.resolveURL(path)
	if err != nil {
		return "", err
	}
	req, err := c.newRequest(ctx, http.MethodGet, resolved, nil, nil)
	if err != nil {
		return "", err
	}
	c.tracef("→ GET %s\n", traceRequestURL(ctx, req.URL))
	resp, err := c.hc.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if budgetErr := readBudgetExhaustion(err); budgetErr != nil {
			return "", budgetErr
		}
		return "", transportError(http.MethodGet, req.URL, err)
	}
	defer resp.Body.Close()
	finalURL := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL
	}
	c.tracef("← %d %s\n", resp.StatusCode, traceResponsePath(ctx, finalURL.Path))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return finalURL.String(), nil
	}
	data, readErr := readResponseBody(ctx, resp.Body, jsonBodyCap)
	if readErr != nil {
		return "", readErr
	}
	kind := c.classifyResult(resp.StatusCode)
	return "", &APIError{Status: resp.StatusCode, Method: http.MethodGet, Path: path, Body: string(data), kind: kind}
}

// DoStream issues a request whose body is streamed from r and returns a bounded
// response body. It uses the streaming client, so long uploads are not killed by
// the normal JSON client's whole-request timeout. The caller must provide
// replayable retry behavior if it needs retries; this helper sends one request.
func (c *Client) DoStream(ctx context.Context, method, path string, r io.Reader, headers map[string]string) ([]byte, error) {
	return c.DoStreamSized(ctx, method, path, r, -1, headers)
}

// DoStreamSized is DoStream with an explicit request Content-Length when
// contentLength is non-negative.
func (c *Client) DoStreamSized(ctx context.Context, method, path string, r io.Reader, contentLength int64, headers map[string]string) ([]byte, error) {
	url, err := c.resolveURL(path)
	if err != nil {
		return nil, err
	}
	// The upload itself may legitimately take a long time, so keep the caller's
	// wall-clock semantics. A child cancellation context lets the shared idle
	// reader stop a backend that accepted the upload and then stalled while
	// streaming its response body.
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rctx = context.WithValue(rctx, downloadRedirectCancelKey{}, cancel)
	req, err := c.newRequestReader(rctx, method, url, r, headers)
	if err != nil {
		return nil, err
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	c.tracef("→ %s %s\n", method, traceRequestURL(ctx, req.URL))
	resp, err := c.dl.Do(req)
	if err != nil {
		c.tracef("× %s %s (transport error: %s)\n", method, traceRequestURL(ctx, req.URL), transportErrorCategory(err))
		if budgetErr := readBudgetExhaustion(err); budgetErr != nil {
			return nil, budgetErr
		}
		return nil, transportError(method, req.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.tracef("← %d %s\n", resp.StatusCode, traceResponsePath(ctx, req.URL.Path))
	data, err := readIdleResponseBody(ctx, resp.Body, jsonBodyCap, downloadIdleTimeout, cancel)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return data, nil
	}
	kind := c.classifyResult(resp.StatusCode)
	return nil, &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(data), kind: kind}
}

// do is the buffered retry/transport core behind Do. maxBytes bounds the
// response body; exceeding it is an error, not a silent truncation. Binary
// downloads use GetStream instead.
func (c *Client) do(ctx context.Context, method, path string, body []byte, headers map[string]string, maxBytes int64) ([]byte, error) {
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
		req, err := c.newRequest(ctx, method, url, body, headers)
		if err != nil {
			return nil, err
		}
		c.tracef("→ %s %s\n", method, traceRequestURL(ctx, req.URL))
		resp, err := c.hc.Do(req)
		if err != nil {
			c.tracef("× %s %s (transport error: %s)\n", method, traceRequestURL(ctx, req.URL), transportErrorCategory(err))
			if budgetErr := readBudgetExhaustion(err); budgetErr != nil {
				return nil, budgetErr
			}
			safeErr := transportError(method, req.URL, err)
			// A committed-but-lost write can double-execute or turn success into a
			// misleading conflict/not-found; only replay-safe reads retry here.
			if !replaySafe(method) || errors.Is(err, errRedirectLimit) {
				return nil, safeErr
			}
			lastErr = safeErr
			continue // network error → retry
		}
		c.tracef("← %d %s\n", resp.StatusCode, traceResponsePath(ctx, req.URL.Path))
		result := c.classifyAttempt(method, resp)
		data, err := readResponseBody(ctx, resp.Body, maxBytes)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return data, nil
		}
		apiErr := &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(data), kind: result.kind}
		// A response does not prove that a write was uncommitted. Only replay-safe
		// reads retry generically; write endpoints must reconcile explicitly.
		if result.retryable && !domain.NoReplayRetries(ctx) {
			lastErr = apiErr
			if result.retryDelay > 0 {
				if !sleep(ctx, result.retryDelay) {
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

// GetJSON GETs path and unmarshals into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	data, err := c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	return unmarshal(data, out)
}

// GetJSONUseNumber is GetJSON with lossless json.Number values inside dynamic
// maps/slices. Typed numeric struct fields continue to decode normally. Use it
// when a caller must compare arbitrary server JSON without float64 precision
// loss (for example guarded field idempotency checks).
func (c *Client) GetJSONUseNumber(ctx context.Context, path string, out any) error {
	data, err := c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return fmt.Errorf("decode response: trailing data: %w", err)
	}
	return nil
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

func unmarshal(data []byte, out any) error {
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
