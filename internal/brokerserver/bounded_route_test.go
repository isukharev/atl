package brokerserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
)

const boundedRouteParentWindow = 225 * time.Millisecond

type boundedRouteBlockingAuthenticator struct {
	calls    atomic.Int32
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (a *boundedRouteBlockingAuthenticator) Authenticate(ctx context.Context, _ []byte, _ brokertransport.AuthenticationChallenge) (brokertransport.Authentication, error) {
	a.calls.Add(1)
	a.once.Do(func() { close(a.started) })
	<-ctx.Done()
	select {
	case <-a.canceled:
	default:
		close(a.canceled)
	}
	return brokertransport.Authentication{}, ctx.Err()
}

type boundedRouteCountingBody struct {
	reads atomic.Int32
	body  *bytes.Reader
}

func (b *boundedRouteCountingBody) Read(data []byte) (int, error) {
	b.reads.Add(1)
	return b.body.Read(data)
}

func (*boundedRouteCountingBody) Close() error { return nil }

type boundedRouteEOFBody struct {
	io.ReadCloser
	hitEOF atomic.Bool
}

func (b *boundedRouteEOFBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	if errors.Is(err, io.EOF) {
		b.hitEOF.Store(true)
	}
	return n, err
}

type boundedRouteUnsupportedWriter struct {
	header http.Header
	writes atomic.Int32
}

func (w *boundedRouteUnsupportedWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *boundedRouteUnsupportedWriter) Write(data []byte) (int, error) {
	w.writes.Add(1)
	return len(data), nil
}

func (w *boundedRouteUnsupportedWriter) WriteHeader(int) { w.writes.Add(1) }

type boundedRouteFlushWriter struct {
	header        http.Header
	body          bytes.Buffer
	status        int
	readDeadline  time.Time
	writeDeadline time.Time
	ctx           context.Context
	flushCalls    int
	flushErr      error
}

func (w *boundedRouteFlushWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *boundedRouteFlushWriter) Write(data []byte) (int, error) { return w.body.Write(data) }
func (w *boundedRouteFlushWriter) WriteHeader(status int)         { w.status = status }
func (w *boundedRouteFlushWriter) SetReadDeadline(deadline time.Time) error {
	w.readDeadline = deadline
	return nil
}
func (w *boundedRouteFlushWriter) SetWriteDeadline(deadline time.Time) error {
	w.writeDeadline = deadline
	return nil
}
func (w *boundedRouteFlushWriter) Flush() {
	w.flushCalls++
	if w.ctx == nil || w.ctx.Err() != nil {
		w.flushErr = context.Canceled
	}
}

type boundedRouteTrackedConn struct {
	net.Conn
	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
}

func (c *boundedRouteTrackedConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline, c.writeDeadline = deadline, deadline
	c.mu.Unlock()
	return c.Conn.SetDeadline(deadline)
}

func (c *boundedRouteTrackedConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(deadline)
}

func (c *boundedRouteTrackedConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.writeDeadline = deadline
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *boundedRouteTrackedConn) snapshot() (time.Time, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readDeadline, c.writeDeadline
}

type boundedRouteTrackingListener struct {
	net.Listener
	accepted chan *boundedRouteTrackedConn
}

func (l *boundedRouteTrackingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &boundedRouteTrackedConn{Conn: conn}
	select {
	case l.accepted <- tracked:
	default:
	}
	return tracked, nil
}

func boundedRouteTestHandler(t *testing.T, authenticator brokertransport.Authenticator) *Handler {
	t.Helper()
	guard, err := NewCredentialGuard([]byte("synthetic-upstream-credential"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Close)
	return &Handler{
		config: Config{Audience: "atl-broker", BrokerID: "broker-1"}, authenticator: authenticator,
		guard: guard, permits: make(chan struct{}, 1), random: strings.NewReader(strings.Repeat("x", 128)), now: time.Now,
	}
}

func boundedRouteTLSServer(t *testing.T, handler http.Handler, parent func(context.Context) context.Context) (*httptest.Server, <-chan http.ConnState) {
	t.Helper()
	states := make(chan http.ConnState, 16)
	server := httptest.NewUnstartedServer(handler)
	if parent != nil {
		server.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context { return parent(ctx) }
	}
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		select {
		case states <- state:
		default:
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, states
}

func boundedRouteTimeoutParent(t *testing.T, timeout time.Duration) func(context.Context) context.Context {
	t.Helper()
	var mu sync.Mutex
	var cancels []context.CancelFunc
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, cancel := range cancels {
			cancel()
		}
	})
	return func(ctx context.Context) context.Context {
		bounded, cancel := context.WithTimeout(ctx, timeout)
		mu.Lock()
		cancels = append(cancels, cancel)
		mu.Unlock()
		return bounded
	}
}

func boundedRouteWaitForState(t *testing.T, states <-chan http.ConnState, want http.ConnState, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case state := <-states:
			if state == want {
				return
			}
		case <-timer.C:
			t.Fatalf("connection did not reach %s", want)
		}
	}
}

func boundedRouteRawRequest(t *testing.T, server *httptest.Server, states <-chan http.ConnState, contentLength int64, body []byte, afterActive func()) time.Duration {
	t.Helper()
	address := strings.TrimPrefix(server.URL, "https://")
	config := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13} //nolint:gosec // Isolated loopback httptest certificate.
	connection, err := tls.Dial("tcp", address, config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	started := time.Now()
	if err := connection.SetDeadline(started.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	headers := "POST " + brokertransport.ExecutePathV2 + " HTTP/1.1\r\nHost: " + address + "\r\nAuthorization: Bearer synthetic-workload-credential\r\nContent-Type: application/json\r\nContent-Length: " + strconv.FormatInt(contentLength, 10) + "\r\n\r\n"
	if _, err := io.WriteString(connection, headers); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(body); err != nil {
		t.Fatal(err)
	}
	boundedRouteWaitForState(t, states, http.StateActive, time.Second)
	if afterActive != nil {
		afterActive()
	}
	var one [1]byte
	_, _ = connection.Read(one[:])
	boundedRouteWaitForState(t, states, http.StateClosed, 2*time.Second)
	return time.Since(started)
}

func TestBoundedRouteParentDeadlineClosesIncompleteBodyBeforeAuthentication(t *testing.T) {
	for _, test := range []struct {
		name          string
		contentLength int64
		body          []byte
	}{
		{name: "partial", contentLength: 2, body: []byte("{")},
		{name: "oversized stalled", contentLength: brokercontract.MaxProjectPageRequestBytesV2 + 2, body: bytes.Repeat([]byte("x"), int(brokercontract.MaxProjectPageRequestBytesV2)+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &boundedRouteBlockingAuthenticator{started: make(chan struct{}), canceled: make(chan struct{})}
			handler := boundedRouteTestHandler(t, authenticator)
			server, states := boundedRouteTLSServer(t, handler, boundedRouteTimeoutParent(t, boundedRouteParentWindow))
			elapsed := boundedRouteRawRequest(t, server, states, test.contentLength, test.body, nil)
			if elapsed < 100*time.Millisecond || elapsed >= 2*time.Second || authenticator.calls.Load() != 0 || len(handler.permits) != 0 {
				t.Fatalf("elapsed=%v auth=%d permits=%d", elapsed, authenticator.calls.Load(), len(handler.permits))
			}
		})
	}
}

func TestBoundedRouteParentCancellationInterruptsBodyAndAuthentication(t *testing.T) {
	t.Run("body", func(t *testing.T) {
		authenticator := &boundedRouteBlockingAuthenticator{started: make(chan struct{}), canceled: make(chan struct{})}
		handler := boundedRouteTestHandler(t, authenticator)
		cancelReady := make(chan context.CancelFunc, 1)
		server, states := boundedRouteTLSServer(t, handler, func(ctx context.Context) context.Context {
			bounded, cancel := context.WithCancel(ctx)
			cancelReady <- cancel
			return bounded
		})
		elapsed := boundedRouteRawRequest(t, server, states, 2, []byte("{"), func() { (<-cancelReady)() })
		if elapsed >= 2*time.Second || authenticator.calls.Load() != 0 || len(handler.permits) != 0 {
			t.Fatalf("elapsed=%v auth=%d permits=%d", elapsed, authenticator.calls.Load(), len(handler.permits))
		}
	})

	t.Run("authentication", func(t *testing.T) {
		fixture := newProjectPageServerFixture(t, "")
		authenticator := &boundedRouteBlockingAuthenticator{started: make(chan struct{}), canceled: make(chan struct{})}
		fixture.base.handler.authenticator = authenticator
		cancelReady := make(chan context.CancelFunc, 1)
		server, _ := boundedRouteTLSServer(t, fixture.base.handler, func(ctx context.Context) context.Context {
			bounded, cancel := context.WithCancel(ctx)
			cancelReady <- cancel
			return bounded
		})
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+brokertransport.ExecutePathV2, bytes.NewReader(fixture.body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
		request.Header.Set("Content-Type", "application/json")
		done := make(chan struct{})
		go func() {
			response, _ := server.Client().Do(request)
			if response != nil {
				response.Body.Close()
			}
			close(done)
		}()
		select {
		case <-authenticator.started:
		case <-time.After(2 * time.Second):
			t.Fatal("authentication did not start")
		}
		cancel := <-cancelReady
		cancel()
		select {
		case <-authenticator.canceled:
		case <-time.After(2 * time.Second):
			t.Fatal("authentication context was not canceled")
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("canceled authentication did not release the client")
		}
		if authenticator.calls.Load() != 1 || len(fixture.base.handler.permits) != 0 {
			t.Fatalf("auth=%d permits=%d", authenticator.calls.Load(), len(fixture.base.handler.permits))
		}
	})
}

func TestBoundedRouteRefusesUnsupportedDeadlineWriterWithoutIO(t *testing.T) {
	authenticator := &boundedRouteBlockingAuthenticator{started: make(chan struct{}), canceled: make(chan struct{})}
	handler := boundedRouteTestHandler(t, authenticator)
	body := &boundedRouteCountingBody{body: bytes.NewReader([]byte(`{}`))}
	request := httptest.NewRequest(http.MethodPost, brokertransport.ExecutePathV2, body)
	request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	request.Header.Set("Content-Type", "application/json")
	writer := &boundedRouteUnsupportedWriter{}
	handler.ServeHTTP(writer, request)
	if writer.writes.Load() != 0 || body.reads.Load() != 0 || authenticator.calls.Load() != 0 || len(handler.permits) != 0 {
		t.Fatalf("writes=%d reads=%d auth=%d permits=%d", writer.writes.Load(), body.reads.Load(), authenticator.calls.Load(), len(handler.permits))
	}
}

func TestBoundedRoutePublishFlushesWhileContextIsLiveAndKeepsClippedDeadline(t *testing.T) {
	started := time.Now()
	parentDeadline := started.Add(500 * time.Millisecond)
	ctx, cancel := context.WithDeadline(t.Context(), parentDeadline)
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, brokertransport.ExecutePathV2, bytes.NewReader([]byte(`{}`))).WithContext(ctx)
	writer := &boundedRouteFlushWriter{}
	bounded, finish, err := beginBoundedRoute(writer, request, started, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	writer.ctx = bounded.Context()
	handler := &Handler{guard: &CredentialGuard{}, now: time.Now}
	published, err := handler.publishBounded(bounded, writer, []byte(`{"complete":true}`), started.Add(time.Second), "")
	if !published || err != nil || writer.flushCalls != 1 || writer.flushErr != nil || writer.status != http.StatusOK || writer.body.String() != `{"complete":true}` {
		t.Fatalf("published=%t err=%v flushes=%d flush_err=%v status=%d body=%s", published, err, writer.flushCalls, writer.flushErr, writer.status, writer.body.String())
	}
	if writer.readDeadline.IsZero() || writer.writeDeadline.IsZero() || !writer.readDeadline.Equal(parentDeadline) || !writer.writeDeadline.Equal(parentDeadline) || bounded.Context().Err() != nil {
		t.Fatalf("read=%v write=%v parent=%v context=%v", writer.readDeadline, writer.writeDeadline, parentDeadline, bounded.Context().Err())
	}
	finish()
	if bounded.Context().Err() == nil {
		t.Fatal("finish did not release the bounded context")
	}
}

func TestBoundedRouteCompletedRequestsReuseKeepaliveConnection(t *testing.T) {
	authenticator := &boundedRouteBlockingAuthenticator{started: make(chan struct{}), canceled: make(chan struct{})}
	handler := boundedRouteTestHandler(t, authenticator)
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client := server.Client()
	request := func() *http.Request {
		value, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+brokertransport.ExecutePathV2, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		value.Header.Set("Authorization", "Bearer synthetic-workload-credential")
		value.Header.Set("Content-Type", "application/json")
		return value
	}
	response, err := client.Do(request())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	reused := false
	second := request()
	second = second.WithContext(httptrace.WithClientTrace(second.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
	response, err = client.Do(second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if !reused || authenticator.calls.Load() != 0 || len(handler.permits) != 0 {
		t.Fatalf("reused=%t auth=%d permits=%d", reused, authenticator.calls.Load(), len(handler.permits))
	}
}

func TestBoundedRouteRetainsClippedWriteDeadlineThroughHandlerReturnAfterBodyEOF(t *testing.T) {
	handler := boundedRouteTestHandler(t, &boundedRouteBlockingAuthenticator{started: make(chan struct{}), canceled: make(chan struct{})})
	type observation struct {
		read, write, parent time.Time
		hitEOF              bool
	}
	returned := make(chan observation, 1)
	var listener *boundedRouteTrackingListener
	outer := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := &boundedRouteEOFBody{ReadCloser: request.Body}
		request.Body = body
		parentDeadline, _ := request.Context().Deadline()
		handler.ServeHTTP(writer, request)
		var connection *boundedRouteTrackedConn
		select {
		case connection = <-listener.accepted:
		case <-time.After(2 * time.Second):
			returned <- observation{}
			return
		}
		readDeadline, writeDeadline := connection.snapshot()
		returned <- observation{read: readDeadline, write: writeDeadline, parent: parentDeadline, hitEOF: body.hitEOF.Load()}
	})
	server := httptest.NewUnstartedServer(outer)
	parent := boundedRouteTimeoutParent(t, 450*time.Millisecond)
	server.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return parent(ctx)
	}
	listener = &boundedRouteTrackingListener{Listener: server.Listener, accepted: make(chan *boundedRouteTrackedConn, 1)}
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+brokertransport.ExecutePathV2, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	select {
	case got := <-returned:
		// net/http starts its next-request background read only after the
		// current request body reaches EOF, and that transition may clear the
		// read deadline. The route-owned write deadline must remain clipped
		// through its explicit flush and handler return.
		if !got.hitEOF || got.parent.IsZero() || got.write.IsZero() || !got.write.Equal(got.parent) || !got.read.IsZero() && got.read.After(got.parent) {
			t.Fatalf("eof=%t read=%v write=%v parent=%v", got.hitEOF, got.read, got.write, got.parent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler return was not observed")
	}
}
