package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func TestGetStreamBeforeRejectsInvalidControlsBeforeResolutionOrIO(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(server.Close)
	client := New(server.URL, "PRIVATE-TOKEN", "test")
	bodyCtx, bodyCancel := context.WithTimeout(t.Context(), 2*time.Second)
	t.Cleanup(bodyCancel)
	parent, err := domain.NewReadBudget(2, 16)
	if err != nil {
		t.Fatal(err)
	}
	valid := domain.WithSingleAttempt(domain.WithReadBudget(bodyCtx, parent))

	tests := []struct {
		name     string
		ctx      context.Context
		dispatch time.Time
	}{
		{"nil context", nil, time.Now().Add(time.Second)},
		{"missing body deadline", domain.WithSingleAttempt(domain.WithReadBudget(context.Background(), parent)), time.Now().Add(time.Second)},
		{"zero dispatch deadline", valid, time.Time{}},
		{"dispatch after body deadline", valid, time.Now().Add(3 * time.Second)},
		{"missing single attempt", domain.WithReadBudget(bodyCtx, parent), time.Now().Add(time.Second)},
		{"missing read budget", domain.WithSingleAttempt(bodyCtx), time.Now().Add(time.Second)},
		{"invalid read budget", domain.WithSingleAttempt(domain.WithReadBudget(bodyCtx, new(domain.ReadBudget))), time.Now().Add(time.Second)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream, callErr := client.GetStreamBefore(test.ctx, "https://foreign.example/PRIVATE-PATH", test.dispatch)
			if stream != nil || !errors.Is(callErr, domain.ErrCheckFailed) || errors.Is(callErr, domain.ErrReadDispatchExpired) {
				t.Fatalf("stream=%v error=%v", stream, callErr)
			}
		})
	}
	if calls.Load() != 0 || parent.Usage() != (domain.ReadBudgetUsage{}) {
		t.Fatalf("calls=%d usage=%+v", calls.Load(), parent.Usage())
	}
}

func TestGetStreamBeforeAlreadyExpiredRefusesWithoutAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(server.Close)
	client := New(server.URL, "PRIVATE-TOKEN", "test")
	ctx, cancel, budget := streamDispatchTestContext(t, 16)
	defer cancel()
	stream, err := client.GetStreamBefore(ctx, "/body", time.Now().Add(-time.Second))
	if stream != nil || err != domain.ErrReadDispatchExpired || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("stream=%v error=%v", stream, err)
	}
	if calls.Load() != 0 || budget.Usage() != (domain.ReadBudgetUsage{}) {
		t.Fatalf("calls=%d usage=%+v", calls.Load(), budget.Usage())
	}
}

func TestGetStreamBeforeQueueExpiryConsumesNoAttemptAndReleasesWait(t *testing.T) {
	scheduler, err := NewScheduler(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := scheduler.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	held := true
	t.Cleanup(func() {
		if held {
			hold()
		}
	})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(server.Close)
	client := NewWithScheduler(server.URL, "PRIVATE-TOKEN", "test", scheduler)
	ctx, cancel, budget := streamDispatchTestContext(t, 16)
	defer cancel()
	stream, err := client.GetStreamBefore(ctx, "/queued", time.Now().Add(40*time.Millisecond))
	if stream != nil || err != domain.ErrReadDispatchExpired {
		t.Fatalf("stream=%v error=%v", stream, err)
	}
	if calls.Load() != 0 || budget.Usage() != (domain.ReadBudgetUsage{}) {
		t.Fatalf("calls=%d usage=%+v", calls.Load(), budget.Usage())
	}
	hold()
	held = false
	ordinary, err := client.GetStream(t.Context(), "/ordinary")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(ordinary)
	closeErr := ordinary.Close()
	if readErr != nil || closeErr != nil || string(data) != "ok" || calls.Load() != 1 {
		t.Fatalf("data=%q read=%v close=%v calls=%d", data, readErr, closeErr, calls.Load())
	}
}

func TestReadBudgetTransportFinalDispatchGatePrecedesAttempt(t *testing.T) {
	budget, err := domain.NewReadBudget(1, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithReadBudget(t.Context(), budget)
	ctx = withReadDispatchNotAfter(ctx, time.Now().Add(-time.Second))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid/body", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := &streamDispatchCountingTransport{}
	response, err := (readBudgetTransport{base: base}).RoundTrip(request)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if response != nil || err != domain.ErrReadDispatchExpired || base.calls.Load() != 0 || budget.Usage() != (domain.ReadBudgetUsage{}) {
		t.Fatalf("response=%v error=%v calls=%d usage=%+v", response, err, base.calls.Load(), budget.Usage())
	}
}

func TestReadBudgetTransportFinalDispatchGatePreservesCancellationBeforeAttempt(t *testing.T) {
	for _, test := range []struct {
		name     string
		dispatch time.Time
	}{
		{"expired dispatch", time.Now().Add(-time.Second)},
		{"future dispatch", time.Now().Add(time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget, err := domain.NewReadBudget(1, 16)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			ctx = withReadDispatchNotAfter(domain.WithReadBudget(ctx, budget), test.dispatch)
			cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.invalid/body", nil)
			if err != nil {
				t.Fatal(err)
			}
			base := &streamDispatchCountingTransport{}
			response, err := (readBudgetTransport{base: base}).RoundTrip(request)
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			if response != nil || !errors.Is(err, context.Canceled) || errors.Is(err, domain.ErrReadDispatchExpired) ||
				base.calls.Load() != 0 || budget.Usage() != (domain.ReadBudgetUsage{}) {
				t.Fatalf("response=%v error=%v calls=%d usage=%+v", response, err, base.calls.Load(), budget.Usage())
			}
		})
	}
}

func TestGetStreamBeforeBodyMayOutliveDispatchDeadline(t *testing.T) {
	headers := make(chan struct{})
	releaseBody := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		close(headers)
		select {
		case <-releaseBody:
			_, _ = io.WriteString(writer, "after")
		case <-request.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	client := New(server.URL, "PRIVATE-TOKEN", "test")
	ctx, cancel, budget := streamDispatchTestContext(t, 16)
	defer cancel()
	dispatch := time.Now().Add(500 * time.Millisecond)
	stream, err := client.GetStreamBefore(ctx, "/body", dispatch)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-headers:
	case <-time.After(time.Second):
		t.Fatal("server did not admit the request before the dispatch deadline")
	}
	timer := time.NewTimer(time.Until(dispatch) + 20*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	close(releaseBody)
	data, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || string(data) != "after" || calls.Load() != 1 ||
		budget.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 5}) {
		t.Fatalf("data=%q read=%v close=%v calls=%d usage=%+v", data, readErr, closeErr, calls.Load(), budget.Usage())
	}
}

func TestGetStreamBeforeCloseReleasesSchedulerPermit(t *testing.T) {
	scheduler, err := NewScheduler(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondEntered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/second" {
			secondEntered <- struct{}{}
			_, _ = io.WriteString(writer, "ok")
			return
		}
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	client := NewWithScheduler(server.URL, "PRIVATE-TOKEN", "test", scheduler)
	ctx, cancel, _ := streamDispatchTestContext(t, 16)
	defer cancel()
	stream, err := client.GetStreamBefore(ctx, "/hold", time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(scheduler.permits); got != 1 {
		t.Fatalf("scheduler permits=%d, want one held by the open stream", got)
	}
	secondDone := make(chan error, 1)
	go func() {
		body, callErr := client.GetStream(t.Context(), "/second")
		if body != nil {
			_, _ = io.Copy(io.Discard, body)
			_ = body.Close()
		}
		secondDone <- callErr
	}()
	select {
	case <-secondEntered:
		t.Fatal("second request bypassed the held stream permit")
	case <-time.After(20 * time.Millisecond):
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second request remained blocked after stream close")
	}
}

func TestGetStreamBeforePostDispatchCancellationReleasesSchedulerPermit(t *testing.T) {
	scheduler, err := NewScheduler(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/second" {
			secondEntered <- struct{}{}
			_, _ = io.WriteString(writer, "ok")
			return
		}
		close(firstEntered)
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	client := NewWithScheduler(server.URL, "PRIVATE-TOKEN", "test", scheduler)
	ctx, cancel, budget := streamDispatchTestContext(t, 16)
	firstDone := make(chan error, 1)
	go func() {
		stream, callErr := client.GetStreamBefore(ctx, "/hold", time.Now().Add(time.Second))
		if stream != nil {
			_ = stream.Close()
		}
		firstDone <- callErr
	}()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("first request did not enter transport")
	}
	if got := len(scheduler.permits); got != 1 {
		cancel()
		t.Fatalf("scheduler permits=%d, want one held by the in-flight request", got)
	}
	secondDone := make(chan error, 1)
	go func() {
		body, callErr := client.GetStream(t.Context(), "/second")
		if body != nil {
			_, _ = io.Copy(io.Discard, body)
			_ = body.Close()
		}
		secondDone <- callErr
	}()
	select {
	case <-secondEntered:
		cancel()
		t.Fatal("second request bypassed the in-flight permit")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case callErr := <-firstDone:
		if !errors.Is(callErr, context.Canceled) || errors.Is(callErr, domain.ErrReadDispatchExpired) {
			t.Fatalf("first error=%v", callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request did not return")
	}
	select {
	case callErr := <-secondDone:
		if callErr != nil {
			t.Fatal(callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("second request remained blocked after cancellation")
	}
	if budget.Usage().Attempts != 1 {
		t.Fatalf("usage=%+v", budget.Usage())
	}
}

func TestGetStreamBeforePostDispatchHeaderAndBodyTimeoutsKeepOriginalClass(t *testing.T) {
	t.Run("body deadline before headers", func(t *testing.T) {
		entered := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(entered)
			<-request.Context().Done()
		}))
		t.Cleanup(server.Close)
		budget, err := domain.NewReadBudget(1, 16)
		if err != nil {
			t.Fatal(err)
		}
		bodyCtx, cancel := context.WithTimeout(t.Context(), 700*time.Millisecond)
		defer cancel()
		bodyDeadline, _ := bodyCtx.Deadline()
		ctx := domain.WithSingleAttempt(domain.WithReadBudget(bodyCtx, budget))
		done := make(chan error, 1)
		go func() {
			stream, callErr := New(server.URL, "PRIVATE-TOKEN", "test").GetStreamBefore(ctx, "/headers", bodyDeadline.Add(-100*time.Millisecond))
			if stream != nil {
				_ = stream.Close()
			}
			done <- callErr
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not enter transport")
		}
		select {
		case callErr := <-done:
			if !errors.Is(callErr, context.DeadlineExceeded) || errors.Is(callErr, domain.ErrReadDispatchExpired) {
				t.Fatalf("error=%v", callErr)
			}
		case <-time.After(time.Second):
			t.Fatal("header wait did not honor body deadline")
		}
		if budget.Usage().Attempts != 1 {
			t.Fatalf("usage=%+v", budget.Usage())
		}
	})

	t.Run("idle body after dispatch deadline", func(t *testing.T) {
		shrinkIdle(t, 650*time.Millisecond)
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = io.WriteString(writer, "prefix")
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}))
		t.Cleanup(server.Close)
		ctx, cancel, budget := streamDispatchTestContext(t, 16)
		defer cancel()
		stream, err := New(server.URL, "PRIVATE-TOKEN", "test").GetStreamBefore(ctx, "/body", time.Now().Add(500*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(stream)
		_ = stream.Close()
		if string(data) != "prefix" || readErr == nil || !strings.Contains(readErr.Error(), "download stalled") ||
			errors.Is(readErr, domain.ErrReadDispatchExpired) || budget.Usage().Attempts != 1 {
			t.Fatalf("data=%q error=%v usage=%+v", data, readErr, budget.Usage())
		}
	})
}

func TestGetStreamBeforeTerminalFailuresUseOnePhysicalAttempt(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		drop   bool
	}{
		{"redirect", http.StatusFound, false},
		{"rate limit", http.StatusTooManyRequests, false},
		{"service unavailable", http.StatusServiceUnavailable, false},
		{"lost reply", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if test.drop {
					connection, _, err := http.NewResponseController(writer).Hijack()
					if err != nil {
						t.Errorf("hijack: %v", err)
						return
					}
					_ = connection.Close()
					return
				}
				if test.status == http.StatusFound {
					writer.Header().Set("Location", "/second")
				}
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, "PRIVATE-ERROR-BODY")
			}))
			t.Cleanup(server.Close)
			ctx, cancel, budget := streamDispatchTestContext(t, 1024)
			defer cancel()
			stream, err := New(server.URL, "PRIVATE-TOKEN", "test").GetStreamBefore(ctx, "/body", time.Now().Add(time.Second))
			if stream != nil || err == nil || errors.Is(err, domain.ErrReadAttemptBudgetExhausted) ||
				errors.Is(err, domain.ErrReadDispatchExpired) || calls.Load() != 1 || budget.Usage().Attempts != 1 {
				t.Fatalf("stream=%v error=%v calls=%d usage=%+v", stream, err, calls.Load(), budget.Usage())
			}
			if test.drop {
				var transportErr *TransportError
				if !errors.As(err, &transportErr) {
					t.Fatalf("error=%T %v, want TransportError", err, err)
				}
			} else {
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.Status != test.status {
					t.Fatalf("error=%T %v, want APIError status %d", err, err, test.status)
				}
			}
		})
	}
}

func TestGetStreamBeforeParentByteCeilingRejectsOverflow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "abcd")
	}))
	t.Cleanup(server.Close)
	ctx, cancel, budget := streamDispatchTestContext(t, 3)
	defer cancel()
	stream, err := New(server.URL, "PRIVATE-TOKEN", "test").GetStreamBefore(ctx, "/body", time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(stream)
	_ = stream.Close()
	if string(data) != "abc" || !errors.Is(readErr, domain.ErrReadResponseBudgetExhausted) ||
		budget.Usage() != (domain.ReadBudgetUsage{Attempts: 1, ResponseBytes: 3}) {
		t.Fatalf("data=%q error=%v usage=%+v", data, readErr, budget.Usage())
	}
}

func TestGetStreamBeforeLeavesOrdinaryGetStreamRetryBehaviorUnchanged(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(writer, "ordinary")
	}))
	t.Cleanup(server.Close)
	stream, err := New(server.URL, "PRIVATE-TOKEN", "test").GetStream(t.Context(), "/body")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if string(data) != "ordinary" || readErr != nil || closeErr != nil || calls.Load() != 2 {
		t.Fatalf("data=%q read=%v close=%v calls=%d", data, readErr, closeErr, calls.Load())
	}
}

func streamDispatchTestContext(t *testing.T, maxBytes int64) (context.Context, context.CancelFunc, *domain.ReadBudget) {
	t.Helper()
	budget, err := domain.NewReadBudget(1, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	return domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget)), cancel, budget
}

type streamDispatchCountingTransport struct{ calls atomic.Int32 }

func (t *streamDispatchCountingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("ok")),
		Request:    request,
	}, nil
}
