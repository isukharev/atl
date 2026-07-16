package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type scheduledRoundTripper struct {
	base      http.RoundTripper
	scheduler *Scheduler
}

func (t scheduledRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	release, err := t.scheduler.acquire(req.Context())
	if err != nil {
		return nil, err
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	if resp.Body == nil {
		release()
		return resp, nil
	}
	resp.Body = &scheduledBody{ReadCloser: resp.Body, release: release}
	return resp, nil
}

type scheduledBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *scheduledBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.release)
	}
	return n, err
}

func (b *scheduledBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

func scheduleTransport(base http.RoundTripper, scheduler *Scheduler) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if scheduler == nil {
		return base
	}
	return scheduledRoundTripper{base: base, scheduler: scheduler}
}

// Scheduler bounds all HTTP attempts that share it. It deliberately schedules
// attempts rather than logical operations, so retries, streamed downloads, and
// optional cross-service reads cannot bypass a command's reviewed load policy.
type Scheduler struct {
	permits  chan struct{}
	interval time.Duration

	mu       sync.Mutex
	next     time.Time
	cooldown time.Time
}

// NewScheduler builds a command-scoped scheduler. requestsPerSecond=0 disables
// proactive pacing while retaining the in-flight bound.
func NewScheduler(maxInFlight, requestsPerSecond int) (*Scheduler, error) {
	if maxInFlight < 1 || maxInFlight > 32 {
		return nil, fmt.Errorf("max in-flight requests must be between 1 and 32")
	}
	if requestsPerSecond < 0 || requestsPerSecond > 1000 {
		return nil, fmt.Errorf("requests per second must be between 0 and 1000")
	}
	s := &Scheduler{permits: make(chan struct{}, maxInFlight)}
	if requestsPerSecond > 0 {
		s.interval = time.Second / time.Duration(requestsPerSecond)
	}
	return s, nil
}

// acquire waits for both an in-flight permit and the next allowed start time.
// The returned release is idempotent only by caller discipline: every success
// path must call it exactly once after the response body is consumed/closed.
func (s *Scheduler) acquire(ctx context.Context) (func(), error) {
	if s == nil {
		return func() {}, nil
	}
	select {
	case s.permits <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-s.permits }
	for {
		s.mu.Lock()
		now := time.Now()
		start := s.next
		if s.cooldown.After(start) {
			start = s.cooldown
		}
		if !start.After(now) {
			if s.interval > 0 {
				s.next = now.Add(s.interval)
			}
			s.mu.Unlock()
			return release, nil
		}
		s.mu.Unlock()
		if !sleep(ctx, time.Until(start)) {
			release()
			return nil, ctx.Err()
		}
	}
}

// deferFor makes a server Retry-After visible to every client sharing this
// scheduler. Existing in-flight requests finish; no new attempt starts before
// the longest observed bounded deadline.
func (s *Scheduler) deferFor(d time.Duration) {
	if s == nil || d <= 0 {
		return
	}
	until := time.Now().Add(d)
	s.mu.Lock()
	if until.After(s.cooldown) {
		s.cooldown = until
	}
	s.mu.Unlock()
}
