// Package httpx is the shared HTTP infrastructure: a thin client with bearer
// auth, bounded retries (429/5xx with backoff + Retry-After), JSON helpers, and
// status→domain-error mapping. Adapters use it so they hold no transport policy.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const (
	defaultTimeout = 60 * time.Second
	maxRetries     = 3
	userAgent      = "atl-cli"
)

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
		hc:       &http.Client{Timeout: defaultTimeout},
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

// Do issues a request with retries and returns the raw response body on 2xx.
// path may be absolute (starts with http) or relative to base.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, headers map[string]string) ([]byte, error) {
	url := path
	if !strings.HasPrefix(path, "http") {
		url = c.base + path
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if !sleep(ctx, backoff(attempt)) {
				return nil, ctx.Err()
			}
		}
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
		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = err
			continue // network error → retry
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return data, nil
		}
		apiErr := &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(data), kind: classify(resp.StatusCode)}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = apiErr
			if ra := retryAfter(resp); ra > 0 {
				if !sleep(ctx, ra) {
					return nil, ctx.Err()
				}
			}
			continue // transient → retry
		}
		return nil, apiErr // permanent (4xx) → stop
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
// content-type. Used for attachment/asset downloads.
func (c *Client) GetBytes(ctx context.Context, path string) ([]byte, error) {
	return c.Do(ctx, http.MethodGet, path, nil, map[string]string{"Accept": "*/*"})
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

func backoff(attempt int) time.Duration {
	d := time.Duration(200<<attempt) * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

func retryAfter(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
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
