package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSchedulerBoundsOutstandingAttemptsAcrossClients(t *testing.T) {
	scheduler, err := NewScheduler(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	var active, peak atomic.Int32
	entered := make(chan struct{}, 3)
	releaseHandlers := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- struct{}{}
		<-releaseHandlers
		active.Add(-1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	clients := []*Client{
		NewWithScheduler(srv.URL, "a", "test", scheduler),
		NewWithScheduler(srv.URL, "b", "test", scheduler),
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, callErr := clients[i%2].Do(context.Background(), http.MethodGet, "/", nil, nil)
			errs <- callErr
		}(i)
	}
	for range 2 {
		<-entered
	}
	select {
	case <-entered:
		t.Fatal("third request entered before an in-flight permit was released")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseHandlers)
	wg.Wait()
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak in-flight requests = %d, want 2", got)
	}
}

func TestSchedulerPacesRequestStarts(t *testing.T) {
	scheduler, err := NewScheduler(3, 20) // one start per 50ms
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var starts []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c := NewWithScheduler(srv.URL, "token", "test", scheduler)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Do(context.Background(), http.MethodGet, "/", nil, nil)
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 3 {
		t.Fatalf("starts=%d, want 3", len(starts))
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 35*time.Millisecond {
			t.Fatalf("request start gap %s, want at least 35ms", gap)
		}
	}
}

func TestSchedulerCountsRedirectTransportHops(t *testing.T) {
	scheduler, err := NewScheduler(1, 20)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var starts []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		if r.URL.Path == "/short" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "page")
	}))
	defer srv.Close()
	if _, err := NewWithScheduler(srv.URL, "token", "test", scheduler).ResolveGET(context.Background(), "/short"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 2 || starts[1].Sub(starts[0]) < 35*time.Millisecond {
		t.Fatalf("redirect starts=%v, want two paced transport hops", starts)
	}
}

func TestSchedulerHoldsStreamPermitUntilClose(t *testing.T) {
	scheduler, err := NewScheduler(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	secondEntered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/second" {
			secondEntered <- struct{}{}
		}
		_, _ = io.WriteString(w, "body")
	}))
	defer srv.Close()
	c := NewWithScheduler(srv.URL, "token", "test", scheduler)
	stream, err := c.GetStream(context.Background(), "/stream")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, callErr := c.Do(context.Background(), http.MethodGet, "/second", nil, nil)
		done <- callErr
	}()
	select {
	case <-secondEntered:
		t.Fatal("second request bypassed the open stream's in-flight permit")
	case <-time.After(30 * time.Millisecond):
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerSharedCooldownAndCancellationRelease(t *testing.T) {
	scheduler, err := NewScheduler(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	release, err := scheduler.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := scheduler.acquire(ctx); err == nil {
		t.Fatal("waiting acquisition should honor cancellation")
	}
	release()

	scheduler.deferFor(45 * time.Millisecond)
	started := time.Now()
	release, err = scheduler.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if waited := time.Since(started); waited < 35*time.Millisecond {
		t.Fatalf("shared cooldown waited %s, want at least 35ms", waited)
	}
}

func TestRetryAfterDefersOtherClientSharingScheduler(t *testing.T) {
	scheduler, err := NewScheduler(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	throttled := make(chan struct{}, 1)
	var throttleCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/throttle" && throttleCalls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			throttled <- struct{}{}
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	first := NewWithScheduler(srv.URL, "a", "test", scheduler)
	second := NewWithScheduler(srv.URL, "b", "test", scheduler)
	firstDone := make(chan error, 1)
	go func() {
		_, callErr := first.Do(context.Background(), http.MethodGet, "/throttle", nil, nil)
		firstDone <- callErr
	}()
	<-throttled
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		scheduler.mu.Lock()
		observed := scheduler.cooldown.After(time.Now())
		scheduler.mu.Unlock()
		if observed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Retry-After was not published to the shared scheduler")
		}
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	if _, err := second.Do(context.Background(), http.MethodGet, "/other", nil, nil); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(started); waited < 800*time.Millisecond {
		t.Fatalf("other client waited %s, want shared Retry-After cooldown", waited)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerValidation(t *testing.T) {
	for _, tc := range []struct{ inFlight, rate int }{{0, 0}, {33, 0}, {1, -1}, {1, 1001}} {
		if _, err := NewScheduler(tc.inFlight, tc.rate); err == nil {
			t.Fatalf("NewScheduler(%d,%d) accepted invalid bounds", tc.inFlight, tc.rate)
		}
	}
}
