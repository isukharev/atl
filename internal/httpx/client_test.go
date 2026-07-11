package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClassifyToSentinels(t *testing.T) {
	cases := map[int]error{
		400: domain.ErrUsage,
		401: domain.ErrAuth,
		403: domain.ErrForbidden,
		404: domain.ErrNotFound,
		409: domain.ErrVersionConflict,
		500: nil,
	}
	for status, want := range cases {
		if got := classify(status); got != want {
			t.Errorf("classify(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestAPIErrorUnwraps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"gone"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.Do(context.Background(), "GET", "/x", nil, nil)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	data, err := c.Do(context.Background(), "GET", "/x", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("body = %s", data)
	}
	if atomic.LoadInt32(&hits) < 3 {
		t.Errorf("expected >=3 attempts, got %d", atomic.LoadInt32(&hits))
	}
}

func TestNo4xxRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(400)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, _ = c.Do(context.Background(), "GET", "/x", nil, nil)
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("4xx should not retry; attempts = %d", atomic.LoadInt32(&hits))
	}
}

func TestNoTokenLeakToForeignHost(t *testing.T) {
	// A second host (simulating a server-supplied absolute attachment URL) must
	// NOT be contacted at all: the SSRF guard refuses the request before it is
	// issued, so the PAT can never reach it.
	var contacted int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&contacted, 1)
		w.Write([]byte("data"))
	}))
	defer foreign.Close()
	// Base is a different host.
	c := New("http://configured.invalid", "secret-pat", "test")
	if _, err := c.Do(context.Background(), "GET", foreign.URL+"/dl", nil, nil); err == nil {
		t.Fatal("expected foreign-host request to be refused")
	}
	if atomic.LoadInt32(&contacted) != 0 {
		t.Fatal("foreign host was contacted; PAT could leak")
	}
}

func TestBearerHeaderSent(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "secret-pat", "test")
	_, _ = c.Do(context.Background(), "GET", "/x", nil, nil)
	if h := <-got; h != "Bearer secret-pat" {
		t.Errorf("auth header = %q", h)
	}
}

func TestClassifyBadRequestUsage(t *testing.T) {
	if got := classify(400); got != domain.ErrUsage {
		t.Errorf("classify(400) = %v, want ErrUsage", got)
	}
}

func TestPostNotRetriedOn5xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.Do(context.Background(), http.MethodPost, "/x", []byte(`{}`), nil)
	if err == nil {
		t.Fatal("expected error on POST 5xx")
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("POST must not retry on 5xx; attempts = %d", atomic.LoadInt32(&hits))
	}
}

func TestPostNotRetriedOnTransportError(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		// Hijack and abruptly close the connection to force a transport error.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("ResponseWriter is not a Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.Do(context.Background(), http.MethodPost, "/x", []byte(`{}`), nil)
	if err == nil {
		t.Fatal("expected transport error on POST")
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("POST must not retry on transport error; attempts = %d", atomic.LoadInt32(&hits))
	}
}

func TestTransportErrorsRedactRequestURLAndPreserveCause(t *testing.T) {
	secret := "project = PRIVATE and summary ~ hidden"
	cause := errors.New("dial failed")
	leaking := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("could not reach %s: %w", r.URL.String(), cause)
	})
	c := New("https://backend.example", "tok", "test")
	c.hc.Transport = leaking
	c.dl.Transport = leaking
	path := "/search?jql=" + neturl.QueryEscape(secret) + "#private-fragment"

	for _, tc := range []struct {
		name string
		do   func() error
	}{
		{name: "buffered", do: func() error {
			_, err := c.Do(context.Background(), http.MethodPost, path, []byte(`{}`), nil)
			return err
		}},
		{name: "streamed", do: func() error {
			_, err := c.DoStream(context.Background(), http.MethodPost, path, strings.NewReader("body"), nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.do()
			if err == nil {
				t.Fatal("expected transport error")
			}
			text := err.Error()
			if strings.Contains(text, secret) || strings.Contains(text, "PRIVATE") || strings.Contains(text, "private-fragment") {
				t.Fatalf("transport error leaked request URL: %q", text)
			}
			if !strings.Contains(text, "jql=%3Credacted%3E") {
				t.Fatalf("transport error lost safe routing context: %q", text)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("transport cause was not preserved: %v", err)
			}
			var transport *TransportError
			if !errors.As(err, &transport) {
				t.Fatalf("error type = %T, want TransportError", err)
			}
		})
	}
}

func TestTransportErrorRedactsDownloadURL(t *testing.T) {
	leaf := errors.New("tls failed")
	u, err := neturl.Parse("https://user:pass@backend.example/file?cql=secret#fragment")
	if err != nil {
		t.Fatal(err)
	}
	cause := &neturl.Error{Op: "Get", URL: u.String(), Err: leaf}
	got := transportError(http.MethodGet, u, cause)
	for _, text := range []string{got.Error(), fmt.Sprintf("%v", got), fmt.Sprintf("%+v", got), fmt.Sprintf("%#v", got), fmt.Sprintf("%q", got)} {
		if strings.Contains(text, "secret") || strings.Contains(text, "user") || strings.Contains(text, "pass") || strings.Contains(text, "fragment") {
			t.Fatalf("download transport error leaked URL: %q", text)
		}
	}
	if !errors.Is(got, leaf) {
		t.Fatalf("download cause was not preserved: %v", got)
	}
	var urlErr *neturl.Error
	if errors.As(got, &urlErr) {
		t.Fatalf("URL-bearing cause escaped safe wrapper: %#v", urlErr)
	}
}

func TestTransportErrorReportsOnlySafeCoarseCategory(t *testing.T) {
	u, err := neturl.Parse("https://backend.example/search?cql=private")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "canceled", err: context.Canceled, want: "canceled"},
		{name: "deadline", err: context.DeadlineExceeded, want: "timeout"},
		{name: "dns", err: &net.DNSError{Err: "private resolver detail", Name: "private.example"}, want: "dns"},
		{name: "refused", err: syscall.ECONNREFUSED, want: "connection-refused"},
		{name: "reset", err: syscall.ECONNRESET, want: "connection-lost"},
		{name: "unreachable", err: syscall.ENETUNREACH, want: "unreachable"},
		{name: "other", err: errors.New("private transport detail"), want: "network"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := transportError(http.MethodGet, u, tc.err)
			var transport *TransportError
			if !errors.As(got, &transport) || transport.Category != tc.want {
				t.Fatalf("transport = %#v, want category %q", transport, tc.want)
			}
			text := fmt.Sprintf("%+v", got)
			if !strings.Contains(text, "transport error ("+tc.want+")") {
				t.Fatalf("safe category missing from %q", text)
			}
			for _, private := range []string{"private", "resolver detail", "transport detail"} {
				if strings.Contains(text, private) {
					t.Fatalf("category diagnostic leaked cause detail: %q", text)
				}
			}
		})
	}
}

func TestPostNotRetriedOn429(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(429)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.Do(context.Background(), http.MethodPost, "/x", []byte(`{}`), nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want HTTP 429 APIError", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("POST must not retry on 429; attempts = %d", atomic.LoadInt32(&hits))
	}
}

func TestAPIErrorRedactsQueryValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`bad query`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.Do(context.Background(), http.MethodGet, "/search?jql=project%3DSECRET&fields=summary#private-fragment", nil, nil)
	if err == nil {
		t.Fatal("expected API error")
	}
	text := err.Error()
	for _, secret := range []string{"SECRET", "project%3D", "private-fragment"} {
		if strings.Contains(text, secret) {
			t.Fatalf("API error leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "jql=") || !strings.Contains(text, "fields=") {
		t.Fatalf("API error lost query parameter names: %s", text)
	}
}

func TestAPIErrorRedactionFailsClosedForMalformedAndAbsoluteURLs(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "malformed relative", path: "/search%zz#PRIVATE"},
		{name: "malformed absolute userinfo", path: "https://user:password@example.invalid/%zz"},
		{name: "absolute", path: "https://user:password@example.invalid/search?jql=SECRET#PRIVATE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := (&APIError{Status: 400, Method: http.MethodGet, Path: tc.path, Body: "bad"}).Error()
			for _, secret := range []string{"SECRET", "PRIVATE", "user", "password"} {
				if strings.Contains(text, secret) {
					t.Fatalf("error leaked %q: %s", secret, text)
				}
			}
		})
	}
}

func TestGetRetriedOn429(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	if _, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatalf("GET after 429: %v", err)
	}
	if hits != 2 {
		t.Fatalf("attempts = %d, want 2", hits)
	}
}

func TestWriteMethodsAreNeverRetriedAfterAmbiguousResponses(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			var hits int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&hits, 1)
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer srv.Close()
			c := New(srv.URL, "tok", "test")
			if _, err := c.Do(context.Background(), method, "/write", []byte(`{}`), nil); err == nil {
				t.Fatal("expected error")
			}
			if hits != 1 {
				t.Fatalf("attempts = %d, want 1", hits)
			}
		})
	}
}

func TestDoStreamSendsReaderHeadersAndAuth(t *testing.T) {
	gotBody := make(chan string, 1)
	gotHeader := make(chan string, 1)
	gotAuth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		if r.ContentLength < 0 {
			var err error
			b, err = io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
		} else if _, err := io.ReadFull(r.Body, b); err != nil {
			t.Errorf("read body: %v", err)
		}
		gotBody <- string(b)
		gotHeader <- r.Header.Get("X-Test")
		gotAuth <- r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "secret-pat", "test")
	data, err := c.DoStream(context.Background(), http.MethodPost, "/upload", strings.NewReader("streamed"), map[string]string{"X-Test": "yes"})
	if err != nil {
		t.Fatalf("DoStream: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("response = %q", data)
	}
	if body := <-gotBody; body != "streamed" {
		t.Fatalf("body = %q, want streamed", body)
	}
	if h := <-gotHeader; h != "yes" {
		t.Fatalf("X-Test = %q, want yes", h)
	}
	if auth := <-gotAuth; auth != "Bearer secret-pat" {
		t.Fatalf("Authorization = %q, want bearer token", auth)
	}
}

func TestDoStreamSizedSetsContentLength(t *testing.T) {
	gotContentLength := make(chan int64, 1)
	gotTransferEncoding := make(chan []string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentLength <- r.ContentLength
		gotTransferEncoding <- append([]string(nil), r.TransferEncoding...)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "secret-pat", "test")
	if _, err := c.DoStreamSized(context.Background(), http.MethodPost, "/upload", strings.NewReader("streamed"), int64(len("streamed")), nil); err != nil {
		t.Fatalf("DoStreamSized: %v", err)
	}
	if got := <-gotContentLength; got != int64(len("streamed")) {
		t.Fatalf("ContentLength = %d, want %d", got, len("streamed"))
	}
	if got := <-gotTransferEncoding; len(got) != 0 {
		t.Fatalf("TransferEncoding = %v, want none", got)
	}
}

func TestDoStreamMapsStatusToSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.DoStream(context.Background(), http.MethodPost, "/upload", strings.NewReader("body"), nil)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetRetriedOn5xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) < 2 {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	if _, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Errorf("GET should retry on 5xx; attempts = %d", atomic.LoadInt32(&hits))
	}
}

func TestTruncationReturnsError(t *testing.T) {
	// readBody must error rather than silently truncate when the body exceeds
	// the cap (cap+1 bytes available, cap bytes allowed).
	r := strings.NewReader(strings.Repeat("a", 11))
	if _, err := readBody(r, 10); err == nil {
		t.Fatal("expected error when body exceeds cap")
	}
	// Exactly cap bytes must succeed.
	r2 := strings.NewReader(strings.Repeat("a", 10))
	if data, err := readBody(r2, 10); err != nil || len(data) != 10 {
		t.Fatalf("expected 10 bytes ok, got len=%d err=%v", len(data), err)
	}
}

func TestRetryAfterCappedNoDoubleSleep(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) < 2 {
			// A hostile huge Retry-After must be clamped to maxRetryAfter.
			w.Header().Set("Retry-After", "86400")
			w.WriteHeader(429)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	// Bound the call: if Retry-After were honored uncapped (86400s), this would
	// hang far past the deadline. With clamping to 30s it would still exceed a
	// short test deadline, so we verify the parser clamps directly below and use
	// a context here only to keep the retry loop honest about cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _ = c.Do(ctx, http.MethodGet, "/x", nil, nil)

	// Direct unit checks of clamping/parsing (no real sleeping).
	if got := clampRetryAfter(86400 * time.Second); got != maxRetryAfter {
		t.Errorf("clampRetryAfter(86400s) = %v, want %v", got, maxRetryAfter)
	}
	if got := clampRetryAfter(-5 * time.Second); got != 0 {
		t.Errorf("clampRetryAfter(negative) = %v, want 0", got)
	}
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "5")
	if got := retryAfter(resp); got != 5*time.Second {
		t.Errorf("retryAfter(5) = %v, want 5s", got)
	}
	// HTTP-date in the future, clamped to cap.
	resp.Header.Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	if got := retryAfter(resp); got != maxRetryAfter {
		t.Errorf("retryAfter(future date) = %v, want cap %v", got, maxRetryAfter)
	}
	// HTTP-date in the past, treated as 0.
	resp.Header.Set("Retry-After", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
	if got := retryAfter(resp); got != 0 {
		t.Errorf("retryAfter(past date) = %v, want 0", got)
	}
}

func TestForeignAbsoluteURLRefused(t *testing.T) {
	// An absolute URL to a different host must be refused without issuing the
	// request (blind SSRF guard).
	var hit int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.Write([]byte("data"))
	}))
	defer foreign.Close()
	c := New("http://configured.invalid", "secret-pat", "test")
	_, err := c.Do(context.Background(), http.MethodGet, foreign.URL+"/dl", nil, nil)
	if err == nil {
		t.Fatal("expected refusal of foreign absolute URL")
	}
	if !strings.Contains(err.Error(), "foreign host") {
		t.Errorf("error = %v, want foreign-host refusal", err)
	}
	if atomic.LoadInt32(&hit) != 0 {
		t.Error("foreign host must not be contacted")
	}
}

func TestDirectSameHostSchemeDowngradeRefused(t *testing.T) {
	c := New("https://backend.invalid", "secret-pat", "test")
	_, err := c.Do(context.Background(), http.MethodGet, "http://backend.invalid/attachment", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "https→http") {
		t.Fatalf("err = %v, want direct downgrade refusal", err)
	}
}

func TestDirectSameHostHTTPSKeepsBearer(t *testing.T) {
	c := New("https://backend.invalid", "secret-pat", "test")
	resolved, err := c.resolveURL("https://backend.invalid/attachment")
	if err != nil {
		t.Fatal(err)
	}
	req, err := c.newRequest(context.Background(), http.MethodGet, resolved, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer secret-pat" {
		t.Fatalf("Authorization = %q", got)
	}
}

func TestAbsoluteNonHTTPSchemeRefused(t *testing.T) {
	c := New("https://backend.invalid", "secret-pat", "test")
	if _, err := c.Do(context.Background(), http.MethodGet, "file://backend.invalid/attachment", nil, nil); err == nil {
		t.Fatal("expected non-HTTP absolute URL to be refused")
	}
}

func TestAbsoluteURLWithUserInfoRefused(t *testing.T) {
	c := New("https://backend.invalid", "secret-pat", "test")
	if _, err := c.Do(context.Background(), http.MethodGet, "https://user:pass@backend.invalid/attachment", nil, nil); err == nil {
		t.Fatal("expected URL user information to be refused")
	}
}

func TestMixedCaseAbsoluteURLRefused(t *testing.T) {
	// Classification is by URL scheme, not a lowercase "http" prefix, so a
	// mixed-case absolute URL to a foreign host is still recognized as absolute
	// and refused (the old prefix check would have mis-joined it to the base).
	c := New("https://configured.invalid", "secret-pat", "test")
	_, err := c.Do(context.Background(), http.MethodGet, "HTTP://foreign.invalid/dl", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "foreign host") {
		t.Fatalf("err = %v, want foreign-host refusal", err)
	}
}

func TestCrossHostRedirectRefused(t *testing.T) {
	var foreignHit int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&foreignHit, 1)
		// The PAT must never reach the redirect target.
		if r.Header.Get("Authorization") != "" {
			t.Error("PAT leaked across redirect")
		}
		w.Write([]byte("leaked"))
	}))
	defer foreign.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", foreign.URL+"/dl")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "secret-pat", "test")
	_, err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("expected cross-host redirect to be refused")
	}
	if atomic.LoadInt32(&foreignHit) != 0 {
		t.Error("redirect target must not be followed")
	}
}

func TestSchemeDowngradeRedirectRefused(t *testing.T) {
	c := New("https://backend.invalid", "tok", "test")
	cr := c.hc.CheckRedirect
	if cr == nil {
		t.Fatal("CheckRedirect not configured")
	}
	// Same host but https→http downgrade must be refused.
	via := []*http.Request{{URL: mustParse(t, "https://backend.invalid/a")}}
	req := &http.Request{URL: mustParse(t, "http://backend.invalid/b")}
	if err := cr(req, via); err == nil {
		t.Error("expected https→http downgrade redirect to be refused")
	}
	// Same host, same scheme is allowed.
	via2 := []*http.Request{{URL: mustParse(t, "https://backend.invalid/a")}}
	req2 := &http.Request{URL: mustParse(t, "https://backend.invalid/c")}
	if err := cr(req2, via2); err != nil {
		t.Errorf("same-host https redirect should be allowed, got %v", err)
	}
}

func mustParse(t *testing.T, raw string) *neturl.URL {
	t.Helper()
	u, err := neturl.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// TestNoVersionGate409 locks the backend-aware 409 semantics: by default a
// 409 unwraps to ErrVersionConflict (the Confluence version gate), but a
// client marked SetNoVersionGate (Jira — no version gate exists) keeps the
// full APIError with NO version-conflict sentinel, so the CLI maps it to the
// generic exit instead of suggesting a re-pull/--force recovery.
func TestNoVersionGate409(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(409)
		w.Write([]byte(`{"errorMessages":["issue is locked"]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	_, err := c.Do(context.Background(), "GET", "/x", nil, nil)
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("default client: expected ErrVersionConflict, got %v", err)
	}

	ng := New(srv.URL, "tok", "test")
	ng.SetNoVersionGate()
	_, err = ng.Do(context.Background(), "GET", "/x", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("no-gate client: 409 must not be a version conflict, got %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("no-gate client: want APIError with status 409, got %v", err)
	}
	if !strings.Contains(err.Error(), "issue is locked") {
		t.Fatalf("the backend's own 409 body must survive, got %q", err)
	}
}
