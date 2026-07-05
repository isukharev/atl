package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

// shrinkIdle shrinks the download idle timeout for a test and restores it.
// Tests using it must not run in parallel (package-level toggle).
func shrinkIdle(t *testing.T, d time.Duration) {
	t.Helper()
	old := downloadIdleTimeout
	downloadIdleTimeout = d
	t.Cleanup(func() { downloadIdleTimeout = old })
}

// TestGetStreamSlowDripIsBoundedByInactivityNotWallClock: a transfer that
// takes longer than the idle deadline in total — but never stalls longer than
// it between chunks — succeeds. Under a whole-request timeout of the same
// magnitude it would have been killed; this pins the decoupling that #64 is
// about.
func TestGetStreamSlowDripIsBoundedByInactivityNotWallClock(t *testing.T) {
	shrinkIdle(t, 200*time.Millisecond)
	const chunks = 8 // 8 × 100ms = 800ms total, 4× the idle deadline
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f := w.(http.Flusher)
		for range chunks {
			_, _ = io.WriteString(w, "chunk!")
			f.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	rc, err := c.GetStream(context.Background(), "/big")
	if err != nil {
		t.Fatalf("GetStream: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("a live slow drip must not be killed: %v", err)
	}
	if len(data) != chunks*len("chunk!") {
		t.Errorf("read %d bytes, want %d", len(data), chunks*len("chunk!"))
	}
}

// TestGetStreamStallFailsWithClearError: a mid-body stall beyond the idle
// deadline aborts the read with an error that names the stall, instead of
// hanging forever.
func TestGetStreamStallFailsWithClearError(t *testing.T) {
	shrinkIdle(t, 120*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "prefix")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // stall until the client gives up
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "test")
	rc, err := c.GetStream(context.Background(), "/stall")
	if err != nil {
		t.Fatalf("GetStream: %v", err)
	}
	defer rc.Close()
	_, err = io.ReadAll(rc)
	if err == nil {
		t.Fatal("stalled body must fail the read")
	}
	if !strings.Contains(err.Error(), "download stalled") {
		t.Errorf("stall error = %v, want a 'download stalled' message", err)
	}
}

// TestGetStreamRefusesForeignHost: an absolute URL off the configured backend
// is never requested (blind SSRF guard, same as the buffered path).
func TestGetStreamRefusesForeignHost(t *testing.T) {
	c := New("https://backend.example", "tok", "test")
	_, err := c.GetStream(context.Background(), "https://evil.example/steal")
	if err == nil || !strings.Contains(err.Error(), "foreign host") {
		t.Fatalf("err = %v, want foreign-host refusal", err)
	}
}

// TestGetStreamMapsStatusToSentinel: a 404 surfaces as ErrNotFound so exit
// codes keep working on the streaming path.
func TestGetStreamMapsStatusToSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such attachment", http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	_, err := c.GetStream(context.Background(), "/gone")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestGetStreamRetriesTransient: a 500 then 200 succeeds (retries apply until
// the 2xx headers arrive).
func TestGetStreamRetriesTransient(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "payload")
	}))
	defer srv.Close()
	c := New(srv.URL, "tok", "test")
	rc, err := c.GetStream(context.Background(), "/flaky")
	if err != nil {
		t.Fatalf("GetStream after transient 500: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "payload" || calls != 2 {
		t.Errorf("data=%q calls=%d", data, calls)
	}
}

// TestGetStreamSendsTokenOnlyToBackendHost: the PAT rides on a same-host
// stream request (parity with the buffered path's host-scoped auth).
func TestGetStreamSendsTokenOnlyToBackendHost(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "x")
	}))
	defer srv.Close()
	c := New(srv.URL, "sekret", "test")
	rc, err := c.GetStream(context.Background(), "/f")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(rc)
	rc.Close()
	if got != "Bearer sekret" {
		t.Errorf("Authorization = %q, want the bearer PAT on the backend host", got)
	}
}

// TestIdleWatchdogYieldsToFreshProgress pins the race fix: a watchdog fire
// that observes fresh read progress must reschedule, NOT cancel — a read
// racing the fire wins, so a live stream can never be irrecoverably poisoned.
func TestIdleWatchdogYieldsToFreshProgress(t *testing.T) {
	canceled := false
	r := &idleReader{
		rc:     io.NopCloser(strings.NewReader("")),
		idle:   time.Hour,
		cancel: func() { canceled = true },
	}
	r.timer = time.NewTimer(time.Hour) // parked; the test drives watchdog directly
	defer r.timer.Stop()

	// Fresh progress → the fire is a false alarm: no cancel, no stall.
	r.progress.Store(time.Now().UnixNano())
	r.watchdog()
	if canceled || r.stalled.Load() {
		t.Fatalf("watchdog canceled despite fresh progress (canceled=%v stalled=%v)", canceled, r.stalled.Load())
	}

	// Stale progress → genuine stall: cancel and mark.
	r.progress.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	r.watchdog()
	if !canceled || !r.stalled.Load() {
		t.Fatalf("watchdog did not cancel a genuinely stalled stream (canceled=%v stalled=%v)", canceled, r.stalled.Load())
	}
}

// TestReadCappedRefusesOversize pins the buffered-asset bound: beyond max is
// an error, not a truncation.
func TestReadCappedRefusesOversize(t *testing.T) {
	data, err := ReadCapped(strings.NewReader("12345"), 5)
	if err != nil || string(data) != "12345" {
		t.Fatalf("at-cap read failed: %q %v", data, err)
	}
	if _, err := ReadCapped(strings.NewReader("123456"), 5); err == nil {
		t.Fatal("oversize body must error, not truncate")
	}
}
