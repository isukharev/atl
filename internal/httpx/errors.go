package httpx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"syscall"

	"github.com/isukharev/atl/internal/domain"
)

type unclearedWriteError struct{}

func (*unclearedWriteError) Error() string {
	return "check failed: non-replay-safe request lacks write clearance or reviewed read intent"
}
func (*unclearedWriteError) Unwrap() error                         { return domain.ErrCheckFailed }
func (*unclearedWriteError) DiagnosticWriteAttempted() bool        { return false }
func (*unclearedWriteError) DiagnosticWriteClearanceFailure() bool { return true }

var errUnclearedWrite error = &unclearedWriteError{}

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
