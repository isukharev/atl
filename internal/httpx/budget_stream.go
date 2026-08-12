package httpx

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func newDownloadStream(ctx context.Context, rc io.ReadCloser, cancel context.CancelFunc) io.ReadCloser {
	if budget := domain.ReadBudgetFromContext(ctx); budget != nil {
		return newReadBudgetStream(ctx, rc, downloadIdleTimeout, cancel, budget)
	}
	return newIdleReader(rc, downloadIdleTimeout, cancel)
}

// readBudgetStream binds a successful streamed response to the same aggregate
// response-byte budget as buffered reads. Reservation is lazy so callers may
// open more than one stream without deadlocking before either body is read;
// the idle watchdog starts only after the stream obtains its budget turn.
type readBudgetStream struct {
	ctx    context.Context
	rc     io.ReadCloser
	cancel context.CancelFunc
	idle   time.Duration
	budget *domain.ReadBudget

	reader    *idleReader
	remaining int64
	consumed  int64
	terminal  error

	finish     func(int64)
	finishOnce sync.Once
	closeOnce  sync.Once
	closeErr   error
}

func newReadBudgetStream(ctx context.Context, rc io.ReadCloser, idle time.Duration, cancel context.CancelFunc, budget *domain.ReadBudget) io.ReadCloser {
	return &readBudgetStream{ctx: ctx, rc: rc, idle: idle, cancel: cancel, budget: budget}
}

func (r *readBudgetStream) begin() error {
	if r.reader != nil {
		return nil
	}
	if r.terminal != nil {
		return r.terminal
	}
	remaining, finish, err := r.budget.BeginResponse(r.ctx)
	if err != nil {
		r.terminal = err
		return err
	}
	r.remaining = remaining
	r.finish = finish
	r.reader = newIdleReader(r.rc, r.idle, r.cancel)
	return nil
}

func (r *readBudgetStream) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if err := r.begin(); err != nil {
		return 0, err
	}
	if r.terminal != nil {
		return 0, r.terminal
	}
	available := r.remaining - r.consumed
	if available <= 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			r.terminal = domain.ErrReadResponseBudgetExhausted
			r.finishUsage()
			r.closeUnderlying()
			return 0, r.terminal
		}
		if err != nil {
			r.finishUsage()
		}
		return 0, err
	}
	limit := len(buffer)
	if int64(limit) > available {
		limit = int(available)
	}
	n, err := r.reader.Read(buffer[:limit])
	r.consumed += int64(n)
	if err != nil {
		r.finishUsage()
	}
	return n, err
}

func (r *readBudgetStream) Close() error {
	r.finishUsage()
	r.closeUnderlying()
	return r.closeErr
}

func (r *readBudgetStream) finishUsage() {
	if r.finish == nil {
		return
	}
	r.finishOnce.Do(func() { r.finish(r.consumed) })
}

func (r *readBudgetStream) closeUnderlying() {
	r.closeOnce.Do(func() {
		if r.reader != nil {
			r.closeErr = r.reader.Close()
			return
		}
		r.cancel()
		r.closeErr = r.rc.Close()
	})
}
