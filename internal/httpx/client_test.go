package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLegacyEvaluationHTTPGuardEnvironmentCannotChangeClientBehavior(t *testing.T) {
	guardPath := filepath.Join(t.TempDir(), "legacy-audit.jsonl")
	t.Setenv("ATL_EVAL_HTTP_GUARD_FILE", guardPath)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost {
			t.Errorf("method=%s, want POST", request.Method)
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(server.URL, "token", "test")
	_, err := client.Do(context.Background(), http.MethodPost, "/rest", []byte("body"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if _, err := os.Stat(guardPath); !os.IsNotExist(err) {
		t.Fatalf("legacy evaluation environment created or changed a file: %v", err)
	}

	existingPath := filepath.Join(t.TempDir(), "existing-audit.jsonl")
	const existing = "do-not-append\n"
	if err := os.WriteFile(existingPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_EVAL_HTTP_GUARD_FILE", existingPath)
	if _, err := New(server.URL, "token", "test").Do(context.Background(), http.MethodPost, "/rest", []byte("body"), nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != existing || calls.Load() != 2 {
		t.Fatalf("legacy evaluation environment changed existing data=%q calls=%d", data, calls.Load())
	}
}

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

func TestWriteClearanceBackstopRefusesUnmarkedWritesBeforeTransport(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "token", "test")
	c.requireWriteClearance = true
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, "get"} {
		_, err := c.Do(context.Background(), method, "/write", nil, nil)
		if !errors.Is(err, errUnclearedWrite) || !errors.Is(err, domain.ErrCheckFailed) {
			t.Errorf("method %q error = %v, want uncleared-write/check-failed", method, err)
		}
	}
	if _, err := c.DoStream(context.Background(), http.MethodPost, "/stream", strings.NewReader("body"), nil); !errors.Is(err, errUnclearedWrite) {
		t.Fatalf("streaming write error = %v, want uncleared-write", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("uncleared writes made %d transport attempts, want zero", got)
	}
}

func TestWriteClearanceBackstopAdmitsMarkersAndReplaySafeReads(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "token", "test")
	c.requireWriteClearance = true
	tests := []struct {
		name   string
		ctx    context.Context
		method string
	}{
		{name: "write clearance", ctx: domain.WithWriteClearance(context.Background()), method: http.MethodPost},
		{name: "read intent", ctx: domain.WithReadIntent(context.Background()), method: http.MethodPost},
		{name: "GET", ctx: context.Background(), method: http.MethodGet},
		{name: "HEAD", ctx: context.Background(), method: http.MethodHead},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := c.Do(test.ctx, test.method, "/allowed", nil, nil); err != nil {
				t.Fatalf("allowed request: %v", err)
			}
		})
	}
	if got := hits.Load(); got != int32(len(tests)) {
		t.Fatalf("allowed requests made %d transport attempts, want %d", got, len(tests))
	}
}

func TestWriteClearanceBackstopIsDisabledByDefault(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "token", "test").Do(context.Background(), http.MethodPost, "/write", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("default-disabled client made %d transport attempts, want 1", got)
	}
}

func TestResolveGETReturnsFinalSameOriginURLWithScopedAuth(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		if r.URL.Path == "/x/AwAG" {
			http.Redirect(w, r, "/pages/viewpage.action?pageId=42", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "page")
	}))
	defer srv.Close()

	finalURL, err := New(srv.URL, "secret", "test").ResolveGET(context.Background(), "/x/AwAG")
	if err != nil || finalURL != srv.URL+"/pages/viewpage.action?pageId=42" {
		t.Fatalf("finalURL=%q err=%v", finalURL, err)
	}
	if len(auth) != 2 || auth[0] != "Bearer secret" || auth[1] != "Bearer secret" {
		t.Fatalf("auth=%v", auth)
	}
}

func TestResolveGETRejectsCrossOriginRedirectBeforeRequest(t *testing.T) {
	var foreignRequests atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		foreignRequests.Add(1)
	}))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/pages/viewpage.action?pageId=42", http.StatusFound)
	}))
	defer origin.Close()

	_, err := New(origin.URL, "secret", "test").ResolveGET(context.Background(), "/x/AwAG")
	if err == nil || foreignRequests.Load() != 0 {
		t.Fatalf("err=%v foreign_requests=%d", err, foreignRequests.Load())
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

func TestReadBudgetResponseBytesNMinusOneNAndNPlusOne(t *testing.T) {
	const limit = int64(5)
	tests := []struct {
		name      string
		bodyBytes int64
		exhausted bool
	}{
		{name: "n-minus-one", bodyBytes: limit - 1},
		{name: "n", bodyBytes: limit},
		{name: "n-plus-one", bodyBytes: limit + 1, exhausted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, strings.Repeat("x", int(tt.bodyBytes)))
			}))
			defer srv.Close()

			budget := mustReadBudget(t, 1, limit)
			ctx := domain.WithReadBudget(context.Background(), budget)
			data, err := New(srv.URL, "tok", "test").Do(ctx, http.MethodGet, "/x", nil, nil)
			if tt.exhausted {
				if err != domain.ErrReadResponseBudgetExhausted {
					t.Fatalf("error = %v, want response-byte exhaustion", err)
				}
				if data != nil {
					t.Fatalf("over-budget data = %q, want nil", data)
				}
			} else if err != nil || int64(len(data)) != tt.bodyBytes {
				t.Fatalf("len(data) = %d, error = %v", len(data), err)
			}
			usage := budget.Usage()
			wantBytes := tt.bodyBytes
			if wantBytes > limit {
				wantBytes = limit
			}
			if usage.Attempts != 1 || usage.ResponseBytes != wantBytes {
				t.Fatalf("usage = %+v, want attempts=1 response_bytes=%d", usage, wantBytes)
			}
		})
	}
}

func TestReadBudgetRefusesAttemptBeforeTransport(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	budget := mustReadBudget(t, 2, 0)
	ctx := domain.WithReadBudget(context.Background(), budget)
	client := New(srv.URL, "tok", "test")
	for i := range 3 {
		_, err := client.Do(ctx, http.MethodGet, "/x", nil, nil)
		if i < 2 && err != nil {
			t.Fatalf("request %d error = %v", i+1, err)
		}
		if i == 2 && !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
			t.Fatalf("request %d error = %v, want attempt exhaustion", i+1, err)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("transport hits = %d, want 2", got)
	}
	if usage := budget.Usage(); usage.Attempts != 2 || usage.ResponseBytes != 0 {
		t.Fatalf("usage = %+v, want attempts=2 response_bytes=0", usage)
	}
}

func TestReadBudgetChargesRetryAttempt(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	budget := mustReadBudget(t, 1, 0)
	ctx := domain.WithReadBudget(context.Background(), budget)
	_, err := New(srv.URL, "tok", "test").Do(ctx, http.MethodGet, "/x", nil, nil)
	if !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
		t.Fatalf("error = %v, want attempt exhaustion before retry", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("transport hits = %d, want 1", got)
	}
	if usage := budget.Usage(); usage.Attempts != 1 {
		t.Fatalf("usage = %+v, want attempts=1", usage)
	}
}

func TestReadBudgetChargesRedirectAttempt(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/second", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	budget := mustReadBudget(t, 1, 1024)
	ctx := domain.WithReadBudget(context.Background(), budget)
	_, err := New(srv.URL, "tok", "test").Do(ctx, http.MethodGet, "/first", nil, nil)
	if !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
		t.Fatalf("error = %v, want attempt exhaustion before redirect", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("transport hits = %d, want 1", got)
	}
	if usage := budget.Usage(); usage.Attempts != 1 {
		t.Fatalf("usage = %+v, want attempts=1", usage)
	}
}

func TestReadBudgetChargesErrorBodiesAcrossRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "bad")
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	budget := mustReadBudget(t, 2, 4)
	ctx := domain.WithReadBudget(context.Background(), budget)
	_, err := New(srv.URL, "tok", "test").Do(ctx, http.MethodGet, "/x", nil, nil)
	if !errors.Is(err, domain.ErrReadResponseBudgetExhausted) {
		t.Fatalf("error = %v, want response-byte exhaustion", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("transport hits = %d, want 2", got)
	}
	if usage := budget.Usage(); usage.Attempts != 2 || usage.ResponseBytes != 4 {
		t.Fatalf("usage = %+v, want attempts=2 response_bytes=4", usage)
	}
}

func TestReadBudgetChargesPermanentErrorBodyBeforeClassification(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantError error
	}{
		{name: "within-budget", body: "gone", wantError: domain.ErrNotFound},
		{name: "over-budget", body: "gone!", wantError: domain.ErrReadResponseBudgetExhausted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			budget := mustReadBudget(t, 1, 4)
			ctx := domain.WithReadBudget(context.Background(), budget)
			_, err := New(srv.URL, "tok", "test").Do(ctx, http.MethodGet, "/x", nil, nil)
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("error = %v, want %v", err, tt.wantError)
			}
			if usage := budget.Usage(); usage.Attempts != 1 || usage.ResponseBytes != 4 {
				t.Fatalf("usage = %+v, want attempts=1 response_bytes=4", usage)
			}
		})
	}
}

func TestAbsentReadBudgetPreservesRetries(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "temporary")
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	data, err := New(srv.URL, "tok", "test").Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err != nil || string(data) != "ok" {
		t.Fatalf("data = %q, error = %v", data, err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("transport hits = %d, want 2", got)
	}
}

func TestReadBudgetConcurrentRequestsNeverExceedLimits(t *testing.T) {
	const (
		limit = 16
		calls = 64
	)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "x")
	}))
	defer srv.Close()

	budget := mustReadBudget(t, limit, limit)
	ctx := domain.WithReadBudget(context.Background(), budget)
	client := New(srv.URL, "tok", "test")
	var successes atomic.Int32
	var wg sync.WaitGroup
	errCh := make(chan error, calls)
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := client.Do(ctx, http.MethodGet, "/x", nil, nil)
			switch {
			case err == nil && string(data) == "x":
				successes.Add(1)
			case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
			default:
				errCh <- fmt.Errorf("data=%q: %w", data, err)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if got := int(successes.Load()); got != limit {
		t.Fatalf("successful requests = %d, want %d", got, limit)
	}
	if got := int(hits.Load()); got != limit {
		t.Fatalf("transport hits = %d, want %d", got, limit)
	}
	if usage := budget.Usage(); usage.Attempts != limit || usage.ResponseBytes != limit {
		t.Fatalf("usage = %+v, want attempts=%d response_bytes=%d", usage, limit, limit)
	}
}

func mustReadBudget(t *testing.T, attempts int, responseBytes int64) *domain.ReadBudget {
	t.Helper()
	budget, err := domain.NewReadBudget(attempts, responseBytes)
	if err != nil {
		t.Fatal(err)
	}
	return budget
}

func TestSingleAttemptContextDisablesReplaySafeRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.Do(domain.WithSingleAttempt(context.Background()), http.MethodGet, "/x", nil, nil)
	if err == nil {
		t.Fatal("single-attempt request unexpectedly succeeded")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("single-attempt request used %d HTTP attempts, want 1", got)
	}
}

func TestSingleAttemptContextDoesNotFollowRedirect(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/second", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.Do(domain.WithSingleAttempt(context.Background()), http.MethodGet, "/first", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusFound {
		t.Fatalf("error = %v, want APIError status %d", err, http.StatusFound)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("single-attempt redirect used %d HTTP attempts, want 1", got)
	}
}

func TestMutatingMethodsNeverFollowRedirects(t *testing.T) {
	methods := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	statuses := []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	}
	for _, method := range methods {
		for _, status := range statuses {
			t.Run(fmt.Sprintf("%s_%d", method, status), func(t *testing.T) {
				var original, redirected int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/original":
						atomic.AddInt32(&original, 1)
						w.Header().Set("Location", "/redirected")
						w.WriteHeader(status)
					case "/redirected":
						atomic.AddInt32(&redirected, 1)
						_, _ = io.WriteString(w, `{"ok":true}`)
					default:
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()

				c := New(srv.URL, "tok", "test")
				_, err := c.Do(context.Background(), method, "/original", []byte(`{"mutation":true}`), nil)
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != status {
					t.Fatalf("error = %v, want APIError status %d", err, status)
				}
				if got := atomic.LoadInt32(&original); got != 1 {
					t.Fatalf("original attempts = %d, want 1", got)
				}
				if got := atomic.LoadInt32(&redirected); got != 0 {
					t.Fatalf("redirected attempts = %d, want 0", got)
				}
			})
		}
	}
}

func TestStreamingMutationNeverFollowsRedirect(t *testing.T) {
	var original, redirected int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/original":
			atomic.AddInt32(&original, 1)
			w.Header().Set("Location", "/redirected")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/redirected":
			atomic.AddInt32(&redirected, 1)
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	_, err := c.DoStream(context.Background(), http.MethodPost, "/original", strings.NewReader("mutation"), nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTemporaryRedirect {
		t.Fatalf("error = %v, want APIError status %d", err, http.StatusTemporaryRedirect)
	}
	if got := atomic.LoadInt32(&original); got != 1 {
		t.Fatalf("original attempts = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&redirected); got != 0 {
		t.Fatalf("redirected attempts = %d, want 0", got)
	}
}

func TestReplaySafeMethodsFollowSameOriginRedirects(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			var original, redirected int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/original":
					atomic.AddInt32(&original, 1)
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(http.StatusTemporaryRedirect)
				case "/redirected":
					atomic.AddInt32(&redirected, 1)
					if r.Method != method {
						t.Errorf("redirected method = %s, want %s", r.Method, method)
					}
					w.WriteHeader(http.StatusOK)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			c := New(srv.URL, "tok", "test")
			if _, err := c.Do(context.Background(), method, "/original", nil, nil); err != nil {
				t.Fatalf("redirected %s failed: %v", method, err)
			}
			if got := atomic.LoadInt32(&original); got != 1 {
				t.Fatalf("original attempts = %d, want 1", got)
			}
			if got := atomic.LoadInt32(&redirected); got != 1 {
				t.Fatalf("redirected attempts = %d, want 1", got)
			}
		})
	}
}

func TestRedactedHTTPTraceOmitsRequestIdentity(t *testing.T) {
	var trace bytes.Buffer
	SetTrace(&trace)
	t.Cleanup(func() { SetTrace(nil) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	ctx := domain.WithRedactedHTTPTrace(context.Background())
	if _, err := c.Do(ctx, http.MethodGet, "/rest/api/2/issue/PRIVATE-9", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := trace.String(); strings.Contains(got, "PRIVATE-9") || strings.Contains(got, "/rest/api") || !strings.Contains(got, "<redacted>") {
		t.Fatalf("redacted trace=%q", got)
	}
}

func TestSingleAttemptContextDisablesStreamRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.GetStream(domain.WithSingleAttempt(context.Background()), "/x")
	if err == nil {
		t.Fatal("single-attempt stream unexpectedly succeeded")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("single-attempt stream used %d HTTP attempts, want 1", got)
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

func TestDoStreamSizedStalledResponseIsIdleBounded(t *testing.T) {
	shrinkIdle(t, 120*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "prefix")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := New(srv.URL, "secret-pat", "test")
	_, err := c.DoStreamSized(context.Background(), http.MethodPost, "/upload", strings.NewReader("streamed"), int64(len("streamed")), nil)
	if err == nil || !strings.Contains(err.Error(), "download stalled") {
		t.Fatalf("stalled upload response error = %v, want inactivity classification", err)
	}
}

func TestDoStreamSizedStartsIdleWindowAfterResponseBudgetAdmission(t *testing.T) {
	const idle = 80 * time.Millisecond
	shrinkIdle(t, idle)
	requestReachedBackend := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(requestReachedBackend)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	budget := mustReadBudget(t, 1, 1024)
	_, release, err := budget.BeginResponse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			release(0)
		}
	}()

	ctx := domain.WithReadBudget(context.Background(), budget)
	done := make(chan error, 1)
	go func() {
		_, callErr := New(srv.URL, "secret-pat", "test").DoStreamSized(ctx, http.MethodPost, "/upload", strings.NewReader("streamed"), int64(len("streamed")), nil)
		done <- callErr
	}()
	<-requestReachedBackend
	select {
	case callErr := <-done:
		t.Fatalf("request completed while response budget was reserved: %v", callErr)
	case <-time.After(2 * idle):
	}
	release(0)
	released = true
	if callErr := <-done; callErr != nil {
		t.Fatalf("request after response-budget admission: %v", callErr)
	}
}

func TestDoStreamSizedHonorsCallerCancellationWhileReadingResponse(t *testing.T) {
	responseStarted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, "prefix")
		w.(http.Flusher).Flush()
		close(responseStarted)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, callErr := New(srv.URL, "secret-pat", "test").DoStreamSized(ctx, http.MethodPost, "/upload", strings.NewReader("streamed"), int64(len("streamed")), nil)
		done <- callErr
	}()
	<-responseStarted
	cancel()
	select {
	case callErr := <-done:
		if !errors.Is(callErr, context.Canceled) {
			t.Fatalf("caller cancellation error = %v, want context.Canceled", callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not stop upload response read")
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
	via := []*http.Request{{Method: http.MethodGet, URL: mustParse(t, "https://backend.invalid/a")}}
	req := &http.Request{URL: mustParse(t, "http://backend.invalid/b")}
	if err := cr(req, via); err == nil {
		t.Error("expected https→http downgrade redirect to be refused")
	}
	// Same host, same scheme is allowed.
	via2 := []*http.Request{{Method: http.MethodGet, URL: mustParse(t, "https://backend.invalid/a")}}
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
