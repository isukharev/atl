package httpx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

type strictProbeObservation struct {
	path       string
	remoteAddr string
	proto      string
}

type strictRejectedBody struct {
	io.Reader
	closed atomic.Int32
}

func (b *strictRejectedBody) Close() error {
	b.closed.Add(1)
	return nil
}

func TestStrictTransportRefusalClosesStreamingRequestBody(t *testing.T) {
	custom := &closingTransport{}
	client := newWithScheduler("https://backend.example", "token", "test", nil, custom, custom, clientOptions{})
	body := &strictRejectedBody{Reader: http.NoBody}
	_, err := client.DoStream(domain.WithSingleAttempt(t.Context()), http.MethodPost, "/single", body, nil)
	if !errors.Is(err, domain.ErrCheckFailed) || body.closed.Load() != 1 {
		t.Fatalf("strict refusal error=%v request body closes=%d", err, body.closed.Load())
	}
}

type strictProbeRecorder struct {
	mu           sync.Mutex
	observations []strictProbeObservation
	targetHits   int
	errors       chan error
}

func newStrictProbeRecorder() *strictProbeRecorder {
	return &strictProbeRecorder{errors: make(chan error, 1)}
}

func (r *strictProbeRecorder) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	r.mu.Lock()
	r.observations = append(r.observations, strictProbeObservation{
		path:       request.URL.Path,
		remoteAddr: request.RemoteAddr,
		proto:      request.Proto,
	})
	if request.URL.Path == "/probe" {
		r.targetHits++
	}
	targetHit := r.targetHits
	r.mu.Unlock()

	switch request.URL.Path {
	case "/warm":
		_, _ = io.WriteString(writer, "warm")
	case "/probe":
		if targetHit == 1 {
			conn, _, err := http.NewResponseController(writer).Hijack()
			if err != nil {
				r.report(fmt.Errorf("hijack first probe response: %w", err))
				return
			}
			if err := conn.Close(); err != nil {
				r.report(fmt.Errorf("close first probe connection: %w", err))
			}
			return
		}
		if targetHit == 2 {
			_, _ = io.WriteString(writer, "ok")
			return
		}
		r.report(fmt.Errorf("unexpected probe request %d", targetHit))
		http.Error(writer, "unexpected request", http.StatusInternalServerError)
	default:
		r.report(fmt.Errorf("unexpected path %q", request.URL.Path))
		http.NotFound(writer, request)
	}
}

func (r *strictProbeRecorder) report(err error) {
	select {
	case r.errors <- err:
	default:
	}
}

func (r *strictProbeRecorder) snapshot() ([]strictProbeObservation, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]strictProbeObservation(nil), r.observations...), r.targetHits
}

func newStrictProbeClient(t *testing.T, handler http.Handler, enableHTTP2 bool) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = enableHTTP2
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewWithSchedulerTLS(server.URL, "token", "test", nil, TLSOptions{rootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client, server
}

func TestStrictTransportPreventsTransparentRetryForBufferedAndStreamedReads(t *testing.T) {
	for _, entrypoint := range []string{"buffered", "streamed"} {
		for _, policy := range []string{"single attempt", "budget only", "single attempt with budget", "ordinary control"} {
			t.Run(entrypoint+"/"+policy, func(t *testing.T) {
				recorder := newStrictProbeRecorder()
				client, _ := newStrictProbeClient(t, recorder, false)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()

				var warm []byte
				var err error
				if entrypoint == "buffered" {
					warm, err = client.Do(ctx, http.MethodGet, "/warm", nil, nil)
				} else {
					var stream io.ReadCloser
					stream, err = client.GetStream(ctx, "/warm")
					if err == nil {
						warm, err = io.ReadAll(stream)
						if closeErr := stream.Close(); err == nil {
							err = closeErr
						}
					}
				}
				if err != nil || string(warm) != "warm" {
					t.Fatalf("warm body=%q error=%v", warm, err)
				}

				requestCtx := ctx
				var budget *domain.ReadBudget
				switch policy {
				case "single attempt":
					requestCtx = domain.WithSingleAttempt(requestCtx)
				case "budget only":
					budget, err = domain.NewReadBudget(1, 16)
					if err != nil {
						t.Fatal(err)
					}
					requestCtx = domain.WithReadBudget(requestCtx, budget)
				case "single attempt with budget":
					budget, err = domain.NewReadBudget(1, 16)
					if err != nil {
						t.Fatal(err)
					}
					requestCtx = domain.WithSingleAttempt(domain.WithReadBudget(requestCtx, budget))
				}

				var body []byte
				if entrypoint == "buffered" {
					body, err = client.Do(requestCtx, http.MethodGet, "/probe", nil, nil)
				} else {
					var stream io.ReadCloser
					stream, err = client.GetStream(requestCtx, "/probe")
					if err == nil {
						body, err = io.ReadAll(stream)
						if closeErr := stream.Close(); err == nil {
							err = closeErr
						}
					}
				}

				ordinary := policy == "ordinary control"
				if ordinary {
					if err != nil || string(body) != "ok" {
						t.Fatalf("ordinary body=%q error=%v", body, err)
					}
				} else if err == nil {
					t.Fatalf("strict request unexpectedly succeeded with body %q", body)
				}
				if policy == "budget only" && !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
					t.Fatalf("budget-only error=%v, want attempt exhaustion before outer retry", err)
				}
				select {
				case serverErr := <-recorder.errors:
					t.Fatal(serverErr)
				default:
				}

				observations, targetHits := recorder.snapshot()
				wantRequests, wantTargetHits := 2, 1
				if ordinary {
					wantRequests, wantTargetHits = 3, 2
				}
				if len(observations) != wantRequests || targetHits != wantTargetHits {
					t.Fatalf("observations=%+v target hits=%d, want requests=%d target hits=%d", observations, targetHits, wantRequests, wantTargetHits)
				}
				if observations[0].path != "/warm" || observations[1].path != "/probe" {
					t.Fatalf("request sequence=%+v", observations)
				}
				if ordinary {
					if observations[0].remoteAddr != observations[1].remoteAddr || observations[1].remoteAddr == observations[2].remoteAddr || observations[2].path != "/probe" {
						t.Fatalf("ordinary request did not reuse then retry on a fresh connection: %+v", observations)
					}
				} else if observations[0].remoteAddr == observations[1].remoteAddr {
					t.Fatalf("strict request reused the ordinary connection: %+v", observations)
				}
				for _, observation := range observations {
					if observation.proto != "HTTP/1.1" {
						t.Fatalf("protocol=%q, want HTTP/1.1", observation.proto)
					}
				}
				if budget != nil && budget.Usage() != (domain.ReadBudgetUsage{Attempts: 1}) {
					t.Fatalf("budget usage=%+v, want one attempt and no response bytes", budget.Usage())
				}
			})
		}
	}
}

func TestStrictTransportUsesHTTP1AgainstHTTP2CapablePeer(t *testing.T) {
	var (
		mu     sync.Mutex
		protos = map[string]string{}
	)
	client, _ := newStrictProbeClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		protos[request.URL.Path] = request.Proto
		mu.Unlock()
		_, _ = io.WriteString(writer, "ok")
	}), true)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if _, err := client.Do(ctx, http.MethodGet, "/ordinary", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(domain.WithSingleAttempt(ctx), http.MethodGet, "/single", nil, nil); err != nil {
		t.Fatal(err)
	}
	budget, err := domain.NewReadBudget(1, 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(domain.WithReadBudget(ctx, budget), http.MethodGet, "/budget", nil, nil); err != nil {
		t.Fatal(err)
	}
	for path, requestCtx := range map[string]context.Context{
		"/ordinary-stream": ctx,
		"/single-stream":   domain.WithSingleAttempt(ctx),
	} {
		stream, err := client.GetStream(requestCtx, path)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("path=%s read=%v close=%v", path, readErr, closeErr)
		}
	}

	mu.Lock()
	got := make(map[string]string, len(protos))
	for path, proto := range protos {
		got[path] = proto
	}
	mu.Unlock()
	want := map[string]string{
		"/ordinary":        "HTTP/2.0",
		"/single":          "HTTP/1.1",
		"/budget":          "HTTP/1.1",
		"/ordinary-stream": "HTTP/2.0",
		"/single-stream":   "HTTP/1.1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("protocols=%v, want %v", got, want)
	}
}

func TestStrictHTTP1TransportPreservesBasePolicyAndClearsReplayProtocols(t *testing.T) {
	proxyURL, err := url.Parse("http://proxy.example.invalid:8080")
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	base.ResponseHeaderTimeout = 7 * time.Second
	base.TLSClientConfig = &tls.Config{
		RootCAs:    roots,
		ServerName: "backend.example",
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h2", "http/1.1"},
	}
	base.ForceAttemptHTTP2 = true
	base.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{
		"h2": func(string, *tls.Conn) http.RoundTripper { return nil },
	}
	strictRT := newStrictHTTP1Transport(base)
	strict, ok := strictRT.(*http.Transport)
	if !ok {
		t.Fatalf("strict transport type=%T", strictRT)
	}
	t.Cleanup(base.CloseIdleConnections)
	t.Cleanup(strict.CloseIdleConnections)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://backend.example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	gotProxy, err := strict.Proxy(request)
	if err != nil || gotProxy.String() != proxyURL.String() {
		t.Fatalf("proxy=%v error=%v", gotProxy, err)
	}
	if strict.ResponseHeaderTimeout != base.ResponseHeaderTimeout || strict.DialContext == nil {
		t.Fatalf("strict transport lost deadlines or dialer: %+v", strict)
	}
	if strict.TLSClientConfig == base.TLSClientConfig || strict.TLSClientConfig.RootCAs != roots || strict.TLSClientConfig.ServerName != "backend.example" || strict.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("strict transport lost TLS policy: %+v", strict.TLSClientConfig)
	}
	if !reflect.DeepEqual(strict.TLSClientConfig.NextProtos, []string{"http/1.1"}) || strict.TLSNextProto == nil || len(strict.TLSNextProto) != 0 {
		t.Fatalf("strict alternate protocols remain: next=%v handlers=%v", strict.TLSClientConfig.NextProtos, strict.TLSNextProto)
	}
	if !strict.DisableKeepAlives || strict.ForceAttemptHTTP2 || strict.Protocols == nil || !strict.Protocols.HTTP1() || strict.Protocols.HTTP2() || strict.Protocols.UnencryptedHTTP2() {
		t.Fatalf("strict replay controls are incomplete: %+v protocols=%v", strict, strict.Protocols)
	}
	if base.DisableKeepAlives || !base.ForceAttemptHTTP2 || !reflect.DeepEqual(base.TLSClientConfig.NextProtos, []string{"h2", "http/1.1"}) || len(base.TLSNextProto) != 1 {
		t.Fatalf("strict clone mutated ordinary base: %+v", base)
	}
}

func TestStrictDispatchWiringPreservesSchedulerNoProxyAndDownloadDeadline(t *testing.T) {
	scheduler, err := NewScheduler(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	client := NewWithScheduler("https://backend.example", "token", "test", scheduler, WithNoProxy())
	t.Cleanup(client.CloseIdleConnections)
	if client.hc.Timeout != defaultTimeout {
		t.Fatalf("buffered timeout=%v, want %v", client.hc.Timeout, defaultTimeout)
	}

	bufferedScheduled, ok := client.hc.Transport.(scheduledRoundTripper)
	if !ok || bufferedScheduled.scheduler != scheduler {
		t.Fatalf("buffered transport=%T scheduler preserved=%t", client.hc.Transport, ok && bufferedScheduled.scheduler == scheduler)
	}
	bufferedBudget, ok := bufferedScheduled.base.(readBudgetTransport)
	if !ok {
		t.Fatalf("buffered scheduled base=%T", bufferedScheduled.base)
	}
	bufferedDispatch, ok := bufferedBudget.base.(strictDispatchTransport)
	if !ok {
		t.Fatalf("buffered budget base=%T", bufferedBudget.base)
	}
	requireNoProxyTransportPair(t, bufferedDispatch, 0)

	downloadScheduled, ok := client.dl.Transport.(scheduledRoundTripper)
	if !ok || downloadScheduled.scheduler != scheduler {
		t.Fatalf("download transport=%T scheduler preserved=%t", client.dl.Transport, ok && downloadScheduled.scheduler == scheduler)
	}
	downloadBudget, ok := downloadScheduled.base.(readBudgetTransport)
	if !ok {
		t.Fatalf("download scheduled base=%T", downloadScheduled.base)
	}
	redirect, ok := downloadBudget.base.(redirectIdleTransport)
	if !ok {
		t.Fatalf("download budget base=%T", downloadBudget.base)
	}
	downloadDispatch, ok := redirect.base.(strictDispatchTransport)
	if !ok {
		t.Fatalf("download redirect base=%T", redirect.base)
	}
	requireNoProxyTransportPair(t, downloadDispatch, dlHeaderTimeout)
}

func requireNoProxyTransportPair(t *testing.T, dispatch strictDispatchTransport, headerTimeout time.Duration) {
	t.Helper()
	ordinary, ordinaryOK := dispatch.ordinary.(*http.Transport)
	strict, strictOK := dispatch.strict.(*http.Transport)
	if !ordinaryOK || !strictOK {
		t.Fatalf("ordinary=%T strict=%T", dispatch.ordinary, dispatch.strict)
	}
	if ordinary.Proxy != nil || strict.Proxy != nil {
		t.Fatalf("no-proxy client retained proxy: ordinary=%p strict=%p", ordinary.Proxy, strict.Proxy)
	}
	if ordinary.ResponseHeaderTimeout != headerTimeout || strict.ResponseHeaderTimeout != headerTimeout {
		t.Fatalf("response header timeouts ordinary=%v strict=%v, want %v", ordinary.ResponseHeaderTimeout, strict.ResponseHeaderTimeout, headerTimeout)
	}
}

func TestStrictDispatchFailsClosedForUnsupportedRoundTripper(t *testing.T) {
	var calls atomic.Int32
	custom := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
			Request:    request,
		}, nil
	})
	client := newWithScheduler("https://backend.example", "token", "test", nil, custom, custom, clientOptions{})
	if _, err := client.Do(t.Context(), http.MethodGet, "/ordinary", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(domain.WithSingleAttempt(t.Context()), http.MethodGet, "/single", nil, nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("single-attempt error=%v, want fail-closed check", err)
	}
	budget, err := domain.NewReadBudget(1, 16)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.WithNoReplayRetries(domain.WithReadBudget(t.Context(), budget))
	if _, err := client.GetStream(ctx, "/budget"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("budgeted stream error=%v, want fail-closed check", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("unsupported transport calls=%d, want only ordinary call", calls.Load())
	}
}

func TestStrictDispatchFailsClosedForCustomTLSDialer(t *testing.T) {
	var calls atomic.Int32
	dialErr := errors.New("synthetic TLS dial failure")
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	base.DialTLSContext = func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, dialErr
	}
	download := base.Clone()
	download.ResponseHeaderTimeout = dlHeaderTimeout
	client := newWithScheduler("https://backend.example", "token", "test", nil, base, download, clientOptions{})
	t.Cleanup(client.CloseIdleConnections)

	if _, err := client.Do(domain.WithSingleAttempt(t.Context()), http.MethodGet, "/single", nil, nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("single-attempt error=%v, want fail-closed check", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("strict request invoked custom TLS dialer %d times", calls.Load())
	}
	if _, err := client.Do(t.Context(), http.MethodPost, "/ordinary", nil, nil); !errors.Is(err, dialErr) {
		t.Fatalf("ordinary error=%v, want custom dial error", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("ordinary request invoked custom TLS dialer %d times, want 1", calls.Load())
	}
}
