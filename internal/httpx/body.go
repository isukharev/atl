package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const (
	// jsonBodyCap bounds JSON responses (and error bodies on download paths).
	jsonBodyCap = 64 << 20 // 64 MiB
	// BinBodyCap bounds a binary body that a caller chooses to buffer in RAM
	// (e.g. an asset render); streamed downloads are not size-capped.
	BinBodyCap = 1 << 30 // 1 GiB
)

// downloadIdleTimeout is the stall bound for streamed bodies: each successful
// read resets it. A variable so tests can shrink it.
var downloadIdleTimeout = 60 * time.Second

// readBody reads up to max bytes, returning an error if the body is larger
// (rather than silently truncating) or if the read itself fails.
func readBody(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("response body exceeds %d bytes", max)
	}
	return data, nil
}

// readResponseBody applies the ordinary per-response cap and, when present,
// the command-scoped aggregate cap. Budgeted reads are serialized so two
// concurrent bodies cannot both buffer against the same remaining allowance.
func readResponseBody(ctx context.Context, r io.Reader, max int64) ([]byte, error) {
	return readResponseBodyWith(ctx, max, func(limit int64) ([]byte, error) {
		return io.ReadAll(io.LimitReader(r, limit))
	})
}

// readIdleResponseBody waits for any shared response-budget reservation before
// starting the inactivity watchdog. Time queued behind another bounded reader
// is not backend inactivity and must not cancel an otherwise healthy request.
func readIdleResponseBody(ctx context.Context, rc io.ReadCloser, max int64, idle time.Duration, cancel context.CancelFunc) ([]byte, error) {
	return readResponseBodyWith(ctx, max, func(limit int64) (data []byte, err error) {
		idleBody := newIdleReader(rc, idle, cancel)
		data, err = io.ReadAll(io.LimitReader(idleBody, limit))
		if closeErr := idleBody.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close response body: %w", closeErr)
		}
		return data, err
	})
}

// readResponseBodyWith centralizes the per-response and aggregate-budget
// accounting while allowing streaming callers to install a watchdog only once
// their turn to consume the response begins. read receives an inclusive
// detection limit (the accepted maximum plus one byte).
func readResponseBodyWith(ctx context.Context, max int64, read func(limit int64) ([]byte, error)) ([]byte, error) {
	budget := domain.ReadBudgetFromContext(ctx)
	if budget == nil {
		data, err := read(max + 1)
		if err != nil {
			return nil, fmt.Errorf("read response body: %w", err)
		}
		if int64(len(data)) > max {
			return nil, fmt.Errorf("response body exceeds %d bytes", max)
		}
		return data, nil
	}

	remaining, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return nil, err
	}
	consumed := int64(0)
	defer func() { finish(consumed) }()

	limit := max
	if remaining < limit {
		limit = remaining
	}
	data, readErr := read(limit + 1)
	consumed = int64(len(data))
	if consumed > remaining {
		consumed = remaining
		return nil, domain.ErrReadResponseBudgetExhausted
	}
	if readErr != nil {
		return nil, fmt.Errorf("read response body: %w", readErr)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("response body exceeds %d bytes", max)
	}
	return data, nil
}

func readBudgetExhaustion(err error) error {
	switch {
	case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
		return domain.ErrReadAttemptBudgetExhausted
	case errors.Is(err, domain.ErrReadResponseBudgetExhausted):
		return domain.ErrReadResponseBudgetExhausted
	default:
		return nil
	}
}

// ReadCapped fully reads a stream a caller has chosen to buffer in RAM (e.g.
// an asset render), erroring beyond max rather than silently truncating.
func ReadCapped(r io.Reader, max int64) ([]byte, error) {
	return readBody(r, max)
}

// idleReader bounds a streamed body by inactivity: a watchdog cancels the
// underlying request when no read has made progress within idle, so a stalled
// transfer fails with a clear error instead of hanging forever. Progress is a
// timestamp the watchdog consults, NOT a timer the reads reset: a read racing
// the watchdog fire therefore wins (the watchdog sees fresh progress and just
// reschedules), so a fire can never irrecoverably poison a live stream.
type idleReader struct {
	rc       io.ReadCloser
	timer    *time.Timer
	idle     time.Duration
	cancel   context.CancelFunc
	stalled  atomic.Bool
	progress atomic.Int64 // unix nanos of the last read progress
}

func newIdleReader(rc io.ReadCloser, idle time.Duration, cancel context.CancelFunc) *idleReader {
	r := &idleReader{rc: rc, idle: idle, cancel: cancel}
	r.progress.Store(time.Now().UnixNano())
	r.timer = time.AfterFunc(idle, r.watchdog)
	return r
}

// watchdog cancels the request only when no read progressed within idle;
// otherwise it reschedules itself for the remainder of the window.
func (r *idleReader) watchdog() {
	elapsed := time.Duration(time.Now().UnixNano() - r.progress.Load())
	if elapsed < r.idle {
		r.timer.Reset(r.idle - elapsed)
		return
	}
	r.stalled.Store(true)
	r.cancel()
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if n > 0 || err == nil {
		r.progress.Store(time.Now().UnixNano())
	}
	if err != nil && !errors.Is(err, io.EOF) && r.stalled.Load() {
		return n, fmt.Errorf("download stalled: no data received for %s: %w", r.idle, err)
	}
	return n, err
}

func (r *idleReader) Close() error {
	r.timer.Stop()
	r.cancel()
	return r.rc.Close()
}
