package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

var errStrictTransportUnavailable = fmt.Errorf("%w: strict physical-attempt transport is unavailable", domain.ErrCheckFailed)

// strictDispatchTransport keeps ordinary unbudgeted traffic on its pooled
// transport while routing every single-attempt or budgeted request through a
// transport whose one RoundTrip is one fresh HTTP/1 connection.
type strictDispatchTransport struct {
	ordinary http.RoundTripper
	strict   http.RoundTripper
}

func (t strictDispatchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	strict := domain.SingleAttempt(req.Context()) || domain.ReadBudgetFromContext(req.Context()) != nil
	if !strict {
		return t.ordinary.RoundTrip(req)
	}
	if t.strict == nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errStrictTransportUnavailable
	}
	return t.strict.RoundTrip(req)
}

// newStrictHTTP1Transport clones the complete supported transport policy but
// removes every standard-library replay path. DisableKeepAlives prevents an
// HTTP/1 connection from becoming reused; the explicit protocol and ALPN
// settings also remove HTTP/2's independent internal retry loop.
//
// An arbitrary RoundTripper may replay internally, and a custom TLS dialer may
// negotiate a protocol outside TLSClientConfig. Strict requests fail closed for
// those shapes while ordinary unbudgeted requests retain the injected behavior.
func newStrictHTTP1Transport(base http.RoundTripper) http.RoundTripper {
	transport, ok := base.(*http.Transport)
	if !ok || transport == nil {
		return nil
	}
	// DialTLS is deprecated but remains a supported field whose caller-owned
	// handshake policy cannot be safely rewritten for the strict clone.
	if transport.DialTLS != nil || transport.DialTLSContext != nil { //nolint:staticcheck // compatibility guard for the supported deprecated field
		return nil
	}
	strict := transport.Clone()
	strict.DisableKeepAlives = true
	strict.ForceAttemptHTTP2 = false
	strict.Protocols = &http.Protocols{}
	strict.Protocols.SetHTTP1(true)
	strict.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if strict.TLSClientConfig != nil {
		strict.TLSClientConfig.NextProtos = []string{"http/1.1"}
	}
	return strict
}

// readBudgetTransport admits the deadline and budget immediately before strict
// dispatch; absent controls leave ordinary behavior unchanged.
type readBudgetTransport struct{ base http.RoundTripper }

func (t readBudgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkReadDispatch(req.Context()); err != nil {
		return nil, err
	}
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
