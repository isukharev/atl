package httpx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// readBudgetTransport charges immediately before the underlying RoundTrip, so
// retries and redirects are physical attempts while scheduler waits are not.
// An absent context budget leaves transport behavior unchanged.
type readBudgetTransport struct {
	base http.RoundTripper
}

func (t readBudgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if budget := domain.ReadBudgetFromContext(req.Context()); budget != nil {
		if err := budget.TakeAttempt(); err != nil {
			return nil, err
		}
	}
	return t.base.RoundTrip(req)
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
	if c.requireWriteClearance && !replaySafe(method) && !domain.HasWriteClearance(ctx) && !domain.ReadIntent(ctx) {
		return nil, errUnclearedWrite
	}
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
