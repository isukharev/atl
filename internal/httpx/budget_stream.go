package httpx

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func newDownloadStream(ctx context.Context, rc io.ReadCloser, cancel context.CancelFunc) io.ReadCloser {
	if budget := domain.ReadBudgetFromContext(ctx); budget != nil {
		waitCtx, waitCancel := context.WithCancel(ctx)
		return newReadBudgetStream(waitCtx, rc, downloadIdleTimeout, func() {
			waitCancel()
			cancel()
		}, budget)
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
	readMu    sync.Mutex
	closed    atomic.Bool

	finish        func(int64)
	finishOnce    sync.Once
	bodyCloseOnce sync.Once
	closeOnce     sync.Once
	closeErr      error
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
	if r.closed.Load() {
		finish(0)
		r.terminal = r.ctx.Err()
		if r.terminal == nil {
			r.terminal = context.Canceled
		}
		return r.terminal
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
	if r.closed.Load() {
		return 0, r.closedError()
	}
	r.readMu.Lock()
	defer r.readMu.Unlock()
	if r.closed.Load() {
		return 0, r.closedError()
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
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		// Cancellation cannot wait for readMu: it is what wakes a Read blocked
		// on the response-budget gate or the underlying HTTP body.
		r.cancel()
		r.readMu.Lock()
		defer r.readMu.Unlock()
		r.finishUsage()
		r.closeUnderlying()
	})
	return r.closeErr
}

func (r *readBudgetStream) closedError() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

func (r *readBudgetStream) finishUsage() {
	if r.finish == nil {
		return
	}
	r.finishOnce.Do(func() { r.finish(r.consumed) })
}

func (r *readBudgetStream) closeUnderlying() {
	r.bodyCloseOnce.Do(func() {
		if r.reader != nil {
			r.closeErr = r.reader.Close()
			return
		}
		r.cancel()
		r.closeErr = r.rc.Close()
	})
}
