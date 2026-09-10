package httpx

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type cancelReadCloser struct {
	started chan struct{}
	done    <-chan struct{}
	closed  atomic.Int32
}

func (r *cancelReadCloser) Read(buffer []byte) (int, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	<-r.done
	return copy(buffer, "abc"), context.Canceled
}

func (r *cancelReadCloser) Close() error {
	r.closed.Add(1)
	return nil
}

type observedReadCloser struct {
	reader io.Reader
	reads  atomic.Int32
	closed atomic.Int32
}

func (r *observedReadCloser) Read(buffer []byte) (int, error) {
	r.reads.Add(1)
	return r.reader.Read(buffer)
}

func (r *observedReadCloser) Close() error {
	r.closed.Add(1)
	return nil
}

func TestReadBudgetStreamConcurrentCloseJoinsReadAndChargesReturnedBytes(t *testing.T) {
	budget, err := domain.NewReadBudget(1, 8)
	if err != nil {
		t.Fatal(err)
	}
	requestCtx, requestCancel := context.WithCancel(domain.WithReadBudget(t.Context(), budget))
	body := &cancelReadCloser{started: make(chan struct{}), done: requestCtx.Done()}
	stream := newDownloadStream(requestCtx, body, requestCancel)
	type result struct {
		n   int
		err error
	}
	readDone := make(chan result, 1)
	go func() {
		buffer := make([]byte, 8)
		n, readErr := stream.Read(buffer)
		readDone <- result{n: n, err: readErr}
	}()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("read did not reach the underlying body")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- stream.Close() }()
	select {
	case got := <-readDone:
		if got.n != 3 || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("read = (%d, %v), want (3, context canceled)", got.n, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the active read")
	}
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("Close: %v", closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join the active read")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := budget.Usage(); got.ResponseBytes != 3 {
		t.Fatalf("usage = %+v, want three returned bytes", got)
	}
	if got := body.closed.Load(); got != 1 {
		t.Fatalf("underlying closes = %d, want 1", got)
	}
}

func TestReadBudgetStreamCloseWakesBlockedBudgetTurnAndReleasesEveryGate(t *testing.T) {
	parent, err := domain.NewReadBudget(3, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithReadBudget(t.Context(), parent)
	firstBody := &observedReadCloser{reader: &oneByteThenBlock{first: []byte("a"), release: make(chan struct{})}}
	first := newDownloadStream(ctx, firstBody, func() {})
	buffer := make([]byte, 1)
	if n, readErr := first.Read(buffer); n != 1 || readErr != nil {
		t.Fatalf("first read = (%d, %v)", n, readErr)
	}

	secondBody := &observedReadCloser{reader: &oneByteThenBlock{first: []byte("b"), release: make(chan struct{})}}
	requestCanceled := make(chan struct{})
	var cancelOnce atomic.Bool
	second := newDownloadStream(ctx, secondBody, func() {
		if cancelOnce.CompareAndSwap(false, true) {
			close(requestCanceled)
		}
	})
	type result struct {
		n   int
		err error
	}
	readDone := make(chan result, 1)
	go func() {
		n, readErr := second.Read(buffer)
		readDone <- result{n: n, err: readErr}
	}()
	select {
	case got := <-readDone:
		t.Fatalf("blocked read completed before Close: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	if got := secondBody.reads.Load(); got != 0 {
		t.Fatalf("blocked stream reached body %d times", got)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close blocked stream: %v", err)
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("Close did not invoke the request cancel")
	}
	select {
	case got := <-readDone:
		if got.n != 0 || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("blocked read = (%d, %v), want cancellation", got.n, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wake the budget wait")
	}
	if got := secondBody.reads.Load(); got != 0 {
		t.Fatalf("closed stream reached body %d times", got)
	}
	if got := secondBody.closed.Load(); got != 1 {
		t.Fatalf("blocked underlying closes = %d, want 1", got)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}

	thirdBody := &observedReadCloser{reader: io.NopCloser(nilReader{})}
	third := newDownloadStream(ctx, thirdBody, func() {})
	if n, readErr := third.Read(buffer); n != 0 || !errors.Is(readErr, io.EOF) {
		t.Fatalf("third read = (%d, %v), want EOF", n, readErr)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("Close third: %v", err)
	}
	if got := parent.Usage().ResponseBytes; got != 1 {
		t.Fatalf("parent bytes = %d, want only first byte", got)
	}
}

type nilReader struct{}

func (nilReader) Read([]byte) (int, error) { return 0, io.EOF }

type oneByteThenBlock struct {
	first   []byte
	release chan struct{}
}

func (r *oneByteThenBlock) Read(buffer []byte) (int, error) {
	if len(r.first) > 0 {
		n := copy(buffer, r.first)
		r.first = r.first[n:]
		return n, nil
	}
	<-r.release
	return 0, io.EOF
}
