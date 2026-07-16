// Package httpx is the shared HTTP infrastructure: a thin client with bearer
// auth, bounded replay-safe retries for reads (with jittered backoff + capped
// Retry-After), JSON helpers, and status→domain-error mapping. Direct URLs and
// redirects are confined to the configured backend origin policy. Adapters use
// it so they hold no transport policy.
package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	// jsonBodyCap bounds JSON responses (and error bodies on download paths).
	jsonBodyCap = 64 << 20 // 64 MiB
	// BinBodyCap bounds a binary body that a caller chooses to buffer in RAM
	// (e.g. an asset render); streamed downloads are not size-capped.
	BinBodyCap = 1 << 30 // 1 GiB
	// dlHeaderTimeout bounds the wait for response headers on the streaming
	// download client, whose transfers are otherwise limited by inactivity
	// (downloadIdleTimeout), not total wall-clock.
	dlHeaderTimeout = 30 * time.Second
)

// downloadIdleTimeout is the stall bound for streamed bodies: each successful
// read resets it. A variable so tests can shrink it.
var downloadIdleTimeout = 60 * time.Second

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
	base       string
	baseHost   string
	baseScheme string
	token      string
	hc         *http.Client
	dl         *http.Client // streaming downloads: no whole-request timeout
	ver        string       // CLI version, for User-Agent
	scheduler  *Scheduler
	// noVersionGate: this backend has no optimistic version gate, so an HTTP
	// 409 is a generic conflict (locked issue, workflow veto), NOT
	// ErrVersionConflict — exit 5 would point the caller at a re-pull/--force
	// recovery that does not exist there. Set by the Jira adapter.
	noVersionGate bool
}

// SetNoVersionGate marks the backend as having no optimistic version gate:
// an HTTP 409 keeps its full APIError (status and body) but carries no
// ErrVersionConflict sentinel, so it maps to the generic exit code instead
// of masquerading as a version conflict.
func (c *Client) SetNoVersionGate() { c.noVersionGate = true }

// New builds a client for a backend base URL with a bearer PAT.
func New(base, token, version string) *Client {
	return NewWithScheduler(base, token, version, nil)
}

// NewWithScheduler builds a client whose every transport attempt shares the
// supplied command-scoped concurrency/rate policy.
func NewWithScheduler(base, token, version string, scheduler *Scheduler) *Client {
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
		return nil
	}
	// The download transport keeps the default dial/TLS bounds and adds a
	// response-header deadline; the body itself is bounded by inactivity in
	// GetStream, so a large transfer on a slow link is not killed by the
	// whole-request timeout the JSON client uses.
	dlTransport := http.DefaultTransport.(*http.Transport).Clone()
	dlTransport.ResponseHeaderTimeout = dlHeaderTimeout
	return &Client{
		base:       base,
		baseHost:   host,
		baseScheme: scheme,
		token:      token,
		ver:        version,
		scheduler:  scheduler,
		hc: &http.Client{
			Transport:     scheduleTransport(withEvaluationHTTPGuard(http.DefaultTransport), scheduler),
			Timeout:       defaultTimeout,
			CheckRedirect: checkRedirect,
		},
		dl: &http.Client{
			Transport:     scheduleTransport(withEvaluationHTTPGuard(dlTransport), scheduler),
			CheckRedirect: checkRedirect,
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

// TransportError keeps selectors and other query values out of stderr while
// retaining errors.Is identity for cancellation and ambiguous-write
// reconciliation. The cause is deliberately not exposed through Unwrap:
// standard url.Error and custom transports may repeat the complete request URL.
type TransportError struct {
	Method   string
	Category string
	safeURL  string
	err      error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("%s %s: transport error (%s)", e.Method, e.safeURL, e.Category)
}

// Is preserves sentinel/cancellation checks without making the potentially
// URL-bearing cause available to generic unwrapping loggers.
func (e *TransportError) Is(target error) bool { return errors.Is(e.err, target) }

// Format keeps alternate fmt verbs from printing the private cause as a Go
// struct. That cause can contain an unredacted *url.Error.
func (e *TransportError) Format(state fmt.State, verb rune) {
	safe := e.Error()
	if verb == 'q' {
		safe = strconv.Quote(safe)
	}
	_, _ = io.WriteString(state, safe)
}

func transportError(method string, u *neturl.URL, err error) error {
	safe := ""
	if u != nil {
		safe = redactURLString(u.String())
	}
	return &TransportError{Method: method, Category: transportErrorCategory(err), safeURL: safe, err: err}
}

// transportErrorCategory intentionally returns only a small type-derived
// vocabulary. It never includes Error() text from the cause, which may contain
// a raw request URL, proxy address, hostname, or selector.
func transportErrorCategory(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var hostnameErr x509.HostnameError
	var authorityErr x509.UnknownAuthorityError
	var invalidErr x509.CertificateInvalidError
	var tlsHeaderErr tls.RecordHeaderError
	if errors.As(err, &hostnameErr) || errors.As(err, &authorityErr) ||
		errors.As(err, &invalidErr) || errors.As(err, &tlsHeaderErr) {
		return "tls"
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection-refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return "connection-lost"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return "unreachable"
	default:
		return "network"
	}
}

func (e *APIError) Error() string {
	msg := e.Body
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return fmt.Sprintf("%s %s → HTTP %d: %s", e.Method, redactURLString(e.Path), e.Status, strings.TrimSpace(msg))
}

func (e *APIError) Unwrap() error { return e.kind }

// HTTPStatus exposes the received response status without coupling upper
// layers to this concrete transport error type.
func (e *APIError) HTTPStatus() int { return e.Status }

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

// Do issues a request with retries and returns the raw response body on 2xx.
// path may be absolute (starts with http) or relative to base. JSON responses
// are bounded at jsonBodyCap.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, headers map[string]string) ([]byte, error) {
	return c.do(ctx, method, path, body, headers, jsonBodyCap)
}

// ResolveGET follows the client's normal redirect policy for one GET and
// returns the final response URL without reading the success body. It is for
// same-origin short-link resolution; callers must still validate the returned
// path as an application-level reference.
func (c *Client) ResolveGET(ctx context.Context, path string) (string, error) {
	resolved, err := c.resolveURL(path)
	if err != nil {
		return "", err
	}
	req, err := c.newRequest(ctx, http.MethodGet, resolved, nil, nil)
	if err != nil {
		return "", err
	}
	tracef("→ GET %s\n", traceURL(req.URL))
	resp, err := c.hc.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return "", transportError(http.MethodGet, req.URL, err)
	}
	defer resp.Body.Close()
	finalURL := req.URL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL
	}
	tracef("← %d %s\n", resp.StatusCode, finalURL.Path)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return finalURL.String(), nil
	}
	data, readErr := readBody(resp.Body, jsonBodyCap)
	if readErr != nil {
		return "", readErr
	}
	kind := classify(resp.StatusCode)
	if c.noVersionGate && kind == domain.ErrVersionConflict {
		kind = nil
	}
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
	req, err := c.newRequestReader(ctx, method, url, r, headers)
	if err != nil {
		return nil, err
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	tracef("→ %s %s\n", method, traceURL(req.URL))
	resp, err := c.dl.Do(req)
	if err != nil {
		tracef("× %s %s (transport error: %s)\n", method, traceURL(req.URL), transportErrorCategory(err))
		return nil, transportError(method, req.URL, err)
	}
	tracef("← %d %s\n", resp.StatusCode, req.URL.Path)
	data, err := readBody(resp.Body, jsonBodyCap)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return data, nil
	}
	kind := classify(resp.StatusCode)
	if c.noVersionGate && kind == domain.ErrVersionConflict {
		kind = nil
	}
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
	for attempt := 0; attempt <= maxRetries; attempt++ {
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
		tracef("→ %s %s\n", method, traceURL(req.URL))
		resp, err := c.hc.Do(req)
		if err != nil {
			tracef("× %s %s (transport error: %s)\n", method, traceURL(req.URL), transportErrorCategory(err))
			safeErr := transportError(method, req.URL, err)
			// A committed-but-lost write can double-execute or turn success into a
			// misleading conflict/not-found; only replay-safe reads retry here.
			if !replaySafe(method) {
				return nil, safeErr
			}
			lastErr = safeErr
			continue // network error → retry
		}
		tracef("← %d %s\n", resp.StatusCode, req.URL.Path)
		retryable := replaySafe(method) &&
			(resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500)
		retryDelay := time.Duration(0)
		if retryable {
			retryDelay = retryAfter(resp)
			c.scheduler.deferFor(retryDelay)
		}
		data, err := readBody(resp.Body, maxBytes)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return data, nil
		}
		kind := classify(resp.StatusCode)
		if c.noVersionGate && kind == domain.ErrVersionConflict {
			kind = nil // generic conflict on a backend without a version gate
		}
		apiErr := &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(data), kind: kind}
		// A response does not prove that a write was uncommitted. Only replay-safe
		// reads retry generically; write endpoints must reconcile explicitly.
		if retryable {
			lastErr = apiErr
			if retryDelay > 0 {
				if !sleep(ctx, retryDelay) {
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

// resolveURL joins a relative path to base, or validates an absolute URL drawn
// from a server response (e.g. an attachment "content" link). Classify by
// scheme via url.IsAbs, not a "http" prefix: the prefix mis-reads a relative
// path like "httpcache/..." as absolute and a mixed-case "HTTPS://..." as
// relative. An absolute URL pointing off the configured backend host is
// refused outright (blind SSRF) — the request is never issued.
func (c *Client) resolveURL(path string) (string, error) {
	u, err := neturl.Parse(path)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if u.IsAbs() {
		if !sameHost(c.baseHost, u.Host) {
			return "", fmt.Errorf("refusing request to foreign host %q", u.Host)
		}
		if u.User != nil {
			return "", fmt.Errorf("refusing request URL with user information")
		}
		scheme := strings.ToLower(u.Scheme)
		if scheme != "http" && scheme != "https" {
			return "", fmt.Errorf("refusing request with unsupported scheme %q", u.Scheme)
		}
		if c.baseScheme == "https" && scheme != "https" {
			return "", fmt.Errorf("refusing https→http request to %q", u.Host)
		}
		return path, nil
	}
	return c.base + path, nil
}

// newRequest builds one attempt's request with auth/UA headers. The PAT is
// only ever sent to the configured backend host: a path may be an absolute URL
// drawn from a server response; if it points elsewhere we must NOT leak the
// token.
func (c *Client) newRequest(ctx context.Context, method, url string, body []byte, headers map[string]string) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := c.newRequestReader(ctx, method, url, rdr, headers)
	if err != nil {
		return nil, err
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) newRequestReader(ctx context.Context, method, url string, body io.Reader, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if sameHost(c.baseHost, req.URL.Host) && (c.baseScheme != "https" || req.URL.Scheme == "https") {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent+"/"+c.ver)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// traceURL preserves routing information while replacing every query value.
// Selectors often contain issue keys, page titles, JQL, or CQL and therefore
// do not belong in CI/debug logs by default.
func traceURL(u *neturl.URL) string {
	if u == nil {
		return ""
	}
	redacted := *u
	redacted.User = nil
	q := redacted.Query()
	for key, values := range q {
		for i := range values {
			values[i] = "<redacted>"
		}
		q[key] = values
	}
	redacted.RawQuery = q.Encode()
	redacted.Fragment = ""
	return redacted.String()
}

func redactURLString(raw string) string {
	u, err := neturl.Parse(raw)
	if err == nil {
		return traceURL(u)
	}
	// Request construction already rejects malformed URLs. Keep this fallback
	// opaque and fail-closed for manually constructed APIError values and future
	// callers: malformed bytes can hide fragments, userinfo, or query content.
	return "<redacted-invalid-url>"
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

// GetStream GETs path and returns the response body as a stream (binary
// downloads). Retries/backoff apply only until the 2xx headers arrive; the
// body is then consumed by the caller, bounded by an inactivity deadline
// (each read resets it) instead of the JSON client's whole-request timeout —
// a large transfer on a slow link is limited by stalls, not total wall-clock,
// and is never buffered in RAM here. The caller must Close the stream. A
// transport error mid-body is not retried (the partial read cannot be
// transparently resumed).
func (c *Client) GetStream(ctx context.Context, path string) (io.ReadCloser, error) {
	url, err := c.resolveURL(path)
	if err != nil {
		return nil, err
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
		// A per-attempt cancel lets the idle watchdog abort a stalled body
		// without touching the caller's context.
		rctx, cancel := context.WithCancel(ctx)
		req, err := c.newRequest(rctx, http.MethodGet, url, nil, map[string]string{"Accept": "*/*"})
		if err != nil {
			cancel()
			return nil, err
		}
		tracef("→ GET %s\n", traceURL(req.URL))
		resp, err := c.dl.Do(req)
		if err != nil {
			cancel()
			tracef("× GET %s (transport error: %s)\n", traceURL(req.URL), transportErrorCategory(err))
			lastErr = transportError(http.MethodGet, req.URL, err)
			continue // GET is idempotent → retry
		}
		tracef("← %d %s\n", resp.StatusCode, req.URL.Path)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return newIdleReader(resp.Body, downloadIdleTimeout, cancel), nil
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		retryDelay := time.Duration(0)
		if retryable {
			retryDelay = retryAfter(resp)
			c.scheduler.deferFor(retryDelay)
		}
		data, rerr := readBody(resp.Body, jsonBodyCap)
		resp.Body.Close()
		cancel()
		if rerr != nil {
			return nil, rerr
		}
		kind := classify(resp.StatusCode)
		if c.noVersionGate && kind == domain.ErrVersionConflict {
			kind = nil
		}
		apiErr := &APIError{Status: resp.StatusCode, Method: http.MethodGet, Path: path, Body: string(data), kind: kind}
		if retryable {
			lastErr = apiErr
			if retryDelay > 0 {
				if !sleep(ctx, retryDelay) {
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

// ReadCapped fully reads a stream a caller has chosen to buffer in RAM (e.g.
// an asset render), erroring beyond max rather than silently truncating.
func ReadCapped(r io.Reader, max int64) ([]byte, error) {
	return readBody(r, max)
}

// idleReader bounds a streamed body by inactivity: a watchdog cancels the
// underlying request when no read has made progress within idle, so a stalled
// transfer fails with a clear error instead of hanging forever. Progress is a
// timestamp the watchdog consults, NOT a timer the reads reset: a read racing
// the watchdog fire therefore wins (the watchdog sees fresh progress and just
// reschedules), so a fire can never irrecoverably poison a live stream.
type idleReader struct {
	rc       io.ReadCloser
	timer    *time.Timer
	idle     time.Duration
	cancel   context.CancelFunc
	stalled  atomic.Bool
	progress atomic.Int64 // unix nanos of the last read progress
}

func newIdleReader(rc io.ReadCloser, idle time.Duration, cancel context.CancelFunc) *idleReader {
	r := &idleReader{rc: rc, idle: idle, cancel: cancel}
	r.progress.Store(time.Now().UnixNano())
	r.timer = time.AfterFunc(idle, r.watchdog)
	return r
}

// watchdog cancels the request only when no read progressed within idle;
// otherwise it reschedules itself for the remainder of the window.
func (r *idleReader) watchdog() {
	elapsed := time.Duration(time.Now().UnixNano() - r.progress.Load())
	if elapsed < r.idle {
		r.timer.Reset(r.idle - elapsed)
		return
	}
	r.stalled.Store(true)
	r.cancel()
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if n > 0 || err == nil {
		r.progress.Store(time.Now().UnixNano())
	}
	if err != nil && !errors.Is(err, io.EOF) && r.stalled.Load() {
		return n, fmt.Errorf("download stalled: no data received for %s: %w", r.idle, err)
	}
	return n, err
}

func (r *idleReader) Close() error {
	r.timer.Stop()
	r.cancel()
	return r.rc.Close()
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
