package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func boundedPostContext(t *testing.T, attempts int, bytes int64) (context.Context, context.CancelFunc, *domain.ReadBudget) {
	t.Helper()
	budget, err := domain.NewReadBudget(attempts, bytes)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	ctx = domain.WithReadIntent(domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget)))
	return ctx, cancel, budget
}

func TestPostBoundedResponseStreamRejectsInvalidControlsBeforeResolutionOrIO(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	client := New(server.URL, "PRIVATE-TOKEN", "test")
	valid, validCancel, budget := boundedPostContext(t, 1, 32)
	t.Cleanup(validCancel)
	deadlineCtx, deadlineCancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(deadlineCancel)
	noBudget := domain.WithReadIntent(domain.WithSingleAttempt(deadlineCtx))
	canceled, cancel := context.WithCancel(valid)
	cancel()

	tests := []struct {
		name          string
		client        *Client
		ctx           context.Context
		body          []byte
		success, fail int64
		wantCanceled  bool
	}{
		{name: "nil client", ctx: valid, body: []byte("{}"), success: 1, fail: 1},
		{name: "nil context", client: client, body: []byte("{}"), success: 1, fail: 1},
		{name: "missing read intent", client: client, ctx: domain.WithSingleAttempt(domain.WithReadBudget(deadlineCtx, budget)), body: []byte("{}"), success: 1, fail: 1},
		{name: "missing single attempt", client: client, ctx: domain.WithReadIntent(domain.WithReadBudget(deadlineCtx, budget)), body: []byte("{}"), success: 1, fail: 1},
		{name: "missing deadline", client: client, ctx: domain.WithReadIntent(domain.WithSingleAttempt(domain.WithReadBudget(context.Background(), budget))), body: []byte("{}"), success: 1, fail: 1},
		{name: "missing budget", client: client, ctx: noBudget, body: []byte("{}"), success: 1, fail: 1},
		{name: "invalid budget", client: client, ctx: domain.WithReadIntent(domain.WithSingleAttempt(domain.WithReadBudget(deadlineCtx, new(domain.ReadBudget)))), body: []byte("{}"), success: 1, fail: 1},
		{name: "empty request", client: client, ctx: valid, success: 1, fail: 1},
		{name: "zero success", client: client, ctx: valid, body: []byte("{}"), fail: 1},
		{name: "zero failure", client: client, ctx: valid, body: []byte("{}"), success: 1},
		{name: "oversize success", client: client, ctx: valid, body: []byte("{}"), success: maxReviewedResponseBody + 1, fail: 1},
		{name: "oversize failure", client: client, ctx: valid, body: []byte("{}"), success: 1, fail: maxReviewedResponseBody + 1},
		{name: "canceled", client: client, ctx: canceled, body: []byte("{}"), success: 1, fail: 1, wantCanceled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.client.PostBoundedResponseStream(test.ctx, "https://foreign.example/PRIVATE-PATH", test.body, test.success, test.fail)
			if result.Body != nil {
				_ = result.Body.Close()
			}
			if test.wantCanceled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want context cancellation", err)
				}
			} else if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error = %v, want check failure", err)
			}
		})
	}
	if got := calls.Load(); got != 0 || budget.Usage() != (domain.ReadBudgetUsage{}) {
		t.Fatalf("calls=%d usage=%+v", got, budget.Usage())
	}
}

func TestPostBoundedResponseStreamReturnsProjectedHeadersBeforeIncrementalBody(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		requestData, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		if request.Method != http.MethodPost || request.URL.RequestURI() != "/stream" || string(requestData) != `{"request":1}` {
			t.Errorf("request = %s %s %q", request.Method, request.URL.RequestURI(), requestData)
		}
		if request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept-Encoding") != "identity" || request.Header.Get("Accept") != "application/x-ndjson, application/json" {
			t.Errorf("headers = %#v", request.Header)
		}
		writer.Header().Set("Content-Type", "application/x-ndjson")
		writer.Header().Set("Content-Encoding", "identity")
		writer.Header().Set("X-ATL-Correlation-ID", "correlation-1")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(writer, "one\ntwo\n")
	}))
	t.Cleanup(server.Close)
	ctx, cancel, budget := boundedPostContext(t, 1, 64)
	defer cancel()
	resultCh := make(chan struct {
		result BoundedResponseStream
		err    error
	}, 1)
	go func() {
		result, err := New(server.URL, "PRIVATE-TOKEN", "test").PostBoundedResponseStream(ctx, "/stream", []byte(`{"request":1}`), 16, 4)
		resultCh <- struct {
			result BoundedResponseStream
			err    error
		}{result, err}
	}()
	var call struct {
		result BoundedResponseStream
		err    error
	}
	select {
	case call = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("method waited for response body instead of returning after headers")
	}
	if call.err != nil {
		t.Fatal(call.err)
	}
	if call.result.Status != http.StatusOK || call.result.ContentType != "application/x-ndjson" || call.result.ContentEncoding != "identity" || call.result.CorrelationID != "correlation-1" {
		t.Fatalf("result metadata = %+v", call.result)
	}
	releaseOnce.Do(func() { close(release) })
	data, readErr := io.ReadAll(call.result.Body)
	closeErr := call.result.Body.Close()
	if readErr != nil || closeErr != nil || string(data) != "one\ntwo\n" {
		t.Fatalf("body=%q read=%v close=%v", data, readErr, closeErr)
	}
	if calls.Load() != 1 || budget.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 8}) {
		t.Fatalf("calls=%d usage=%+v", calls.Load(), budget.Usage())
	}
}

func TestPostBoundedResponseStreamSelectsStatusCapAndNeverReturnsOverflowByte(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		successCap int64
		failureCap int64
		parentCap  int64
		want       string
		wantErr    error
	}{
		{name: "exact success EOF", status: 200, successCap: 4, failureCap: 1, want: "abcd"},
		{name: "empty success", status: 204, successCap: 4, failureCap: 1, want: ""},
		{name: "success overflow", status: 200, successCap: 3, failureCap: 1, want: "abc", wantErr: domain.ErrReadResponseBudgetExhausted},
		{name: "failure overflow", status: 400, successCap: 1, failureCap: 2, want: "ab", wantErr: domain.ErrReadResponseBudgetExhausted},
		{name: "parent remains effective", status: 200, successCap: 4, failureCap: 1, parentCap: 2, want: "ab", wantErr: domain.ErrReadResponseBudgetExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := "abcd"
			if test.status == 204 {
				body = ""
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				writer.(http.Flusher).Flush()
				_, _ = io.WriteString(writer, body)
			}))
			t.Cleanup(server.Close)
			parentCap := test.parentCap
			if parentCap == 0 {
				parentCap = 16
			}
			ctx, cancel, budget := boundedPostContext(t, 1, parentCap)
			defer cancel()
			result, err := New(server.URL, "token", "test").PostBoundedResponseStream(ctx, "/body", []byte("{}"), test.successCap, test.failureCap)
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(result.Body)
			closeErr := result.Body.Close()
			if string(data) != test.want || !errors.Is(readErr, test.wantErr) || closeErr != nil || result.Status != test.status {
				t.Fatalf("status=%d data=%q read=%v close=%v", result.Status, data, readErr, closeErr)
			}
			if usage := budget.Usage(); usage != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: int64(len(test.want))}) {
				t.Fatalf("usage=%+v", usage)
			}
		})
	}
}

func TestPostBoundedResponseStreamDoesNotFollowOrRetry(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
		http.StatusTooManyRequests, http.StatusServiceUnavailable,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var sourceCalls, targetCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/target" {
					targetCalls.Add(1)
					_, _ = io.WriteString(writer, "target")
					return
				}
				sourceCalls.Add(1)
				if status == http.StatusFound || status == http.StatusTemporaryRedirect {
					writer.Header().Set("Location", "/target")
				}
				writer.WriteHeader(status)
				_, _ = io.WriteString(writer, "failure")
			}))
			t.Cleanup(server.Close)
			ctx, cancel, budget := boundedPostContext(t, 1, 32)
			defer cancel()
			result, err := New(server.URL, "token", "test").PostBoundedResponseStream(ctx, "/source", []byte("{}"), 8, 8)
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(result.Body)
			closeErr := result.Body.Close()
			if string(data) != "failure" || readErr != nil || closeErr != nil || result.Status != status {
				t.Fatalf("result=%+v body=%q read=%v close=%v", result, data, readErr, closeErr)
			}
			if sourceCalls.Load() != 1 || targetCalls.Load() != 0 || budget.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 7}) {
				t.Fatalf("source=%d target=%d usage=%+v", sourceCalls.Load(), targetCalls.Load(), budget.Usage())
			}
		})
	}
}

func TestPostBoundedResponseStreamKeepsContentEncodingObservable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q", request.Header.Get("Accept-Encoding"))
		}
		writer.Header().Set("Content-Encoding", "gzip")
		_, _ = io.WriteString(writer, "not-compressed")
	}))
	t.Cleanup(server.Close)
	ctx, cancel, _ := boundedPostContext(t, 1, 32)
	defer cancel()
	result, err := New(server.URL, "token", "test").PostBoundedResponseStream(ctx, "/encoded", []byte("{}"), 16, 16)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(result.Body)
	closeErr := result.Body.Close()
	if result.ContentEncoding != "gzip" || string(data) != "not-compressed" || readErr != nil || closeErr != nil {
		t.Fatalf("encoding=%q data=%q read=%v close=%v", result.ContentEncoding, data, readErr, closeErr)
	}
}

func TestPostBoundedResponseStreamHeaderDeadlineAndDroppedReplyUseOneAttempt(t *testing.T) {
	t.Run("header deadline", func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			calls.Add(1)
			<-release
		}))
		t.Cleanup(server.Close)
		t.Cleanup(func() { close(release) })
		budget, err := domain.NewReadBudget(1, 16)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
		defer cancel()
		ctx = domain.WithReadIntent(domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget)))
		result, callErr := New(server.URL, "token", "test").PostBoundedResponseStream(ctx, "/headers", []byte("{}"), 8, 8)
		if result.Body != nil {
			_ = result.Body.Close()
		}
		if !errors.Is(callErr, context.DeadlineExceeded) || calls.Load() != 1 || budget.Usage() != (domain.ReadBudgetUsage{Attempts: 1}) {
			t.Fatalf("result=%+v error=%v calls=%d usage=%+v", result, callErr, calls.Load(), budget.Usage())
		}
	})
	t.Run("dropped reply", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			connection, _, err := http.NewResponseController(writer).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = connection.Close()
		}))
		t.Cleanup(server.Close)
		ctx, cancel, budget := boundedPostContext(t, 1, 16)
		defer cancel()
		result, callErr := New(server.URL, "token", "test").PostBoundedResponseStream(ctx, "/drop", []byte("{}"), 8, 8)
		if callErr == nil || result.Body != nil || calls.Load() != 1 || budget.Usage() != (domain.ReadBudgetUsage{Attempts: 1}) {
			t.Fatalf("result=%+v error=%v calls=%d usage=%+v", result, callErr, calls.Load(), budget.Usage())
		}
	})
}

func TestPostBoundedResponseStreamIdleTimeoutRetainsBodyClass(t *testing.T) {
	oldIdle := downloadIdleTimeout
	downloadIdleTimeout = 40 * time.Millisecond
	t.Cleanup(func() { downloadIdleTimeout = oldIdle })
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	ctx, cancel, budget := boundedPostContext(t, 1, 16)
	defer cancel()
	result, err := New(server.URL, "token", "test").PostBoundedResponseStream(ctx, "/stall", []byte("{}"), 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(result.Body)
	closeErr := result.Body.Close()
	if len(data) != 0 || readErr == nil || !strings.Contains(readErr.Error(), "download stalled") || closeErr != nil {
		t.Fatalf("data=%q read=%v close=%v", data, readErr, closeErr)
	}
	if usage := budget.Usage(); usage != (domain.ReadBudgetUsage{Attempts: 1}) {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestPostBoundedResponseStreamCloseReleasesSchedulerPermit(t *testing.T) {
	scheduler, err := NewScheduler(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "body")
	}))
	t.Cleanup(server.Close)
	client := NewWithScheduler(server.URL, "token", "test", scheduler)
	firstCtx, firstCancel, firstBudget := boundedPostContext(t, 1, 8)
	defer firstCancel()
	first, err := client.PostBoundedResponseStream(firstCtx, "/first", []byte("{}"), 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(scheduler.permits); got != 1 {
		t.Fatalf("held permits=%d, want 1", got)
	}
	if err := first.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(scheduler.permits); got != 0 {
		t.Fatalf("permits after close=%d, want 0", got)
	}
	secondCtx, secondCancel, secondBudget := boundedPostContext(t, 1, 8)
	defer secondCancel()
	second, err := client.PostBoundedResponseStream(secondCtx, "/second", []byte("{}"), 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(second.Body)
	closeErr := second.Body.Close()
	if string(data) != "body" || readErr != nil || closeErr != nil {
		t.Fatalf("data=%q read=%v close=%v", data, readErr, closeErr)
	}
	if firstBudget.Usage() != (domain.ReadBudgetUsage{Attempts: 1}) || secondBudget.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 4}) {
		t.Fatalf("first=%+v second=%+v", firstBudget.Usage(), secondBudget.Usage())
	}
}

func TestBoundedResponseMetadataIsClosedAndFinite(t *testing.T) {
	for _, test := range []struct {
		name   string
		header http.Header
		ok     bool
	}{
		{name: "empty", header: http.Header{}, ok: true},
		{name: "duplicate", header: http.Header{"Content-Type": {"application/json", "text/plain"}}},
		{name: "control", header: http.Header{"X-Atl-Correlation-Id": {"safe\x00hidden"}}},
		{name: "aggregate overflow", header: http.Header{"Content-Type": {strings.Repeat("a", maxProjectedResponseMetadataBytes+1)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, ok := boundedResponseMetadata(test.header)
			if ok != test.ok {
				t.Fatalf("ok=%t, want %t", ok, test.ok)
			}
		})
	}
}
