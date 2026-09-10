package brokerserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

type hostAuthenticator struct {
	calls      atomic.Int32
	challenges chan brokertransport.AuthenticationChallenge
}

func (a *hostAuthenticator) Authenticate(_ context.Context, credential []byte, challenge brokertransport.AuthenticationChallenge) (brokertransport.Authentication, error) {
	a.calls.Add(1)
	if string(credential) != "synthetic-workload-credential" {
		return brokertransport.Authentication{}, domain.ErrAuth
	}
	select {
	case a.challenges <- challenge:
	default:
	}
	now := time.Now()
	verified := domain.BrokerVerifiedContext{
		PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1",
		Audience: challenge.Audience, BrokerID: challenge.BrokerID, AuthorityRevision: "revision-1",
		ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(time.Minute).UnixMilli(),
		GrantExpiresMillis: now.Add(time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(time.Minute).UnixMilli(),
		Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("a", 64), WorkloadBackendID: "jira-primary"},
	}
	return brokertransport.Authentication{Context: verified, ReleaseDeadline: now.Add(5 * time.Second)}, nil
}

func TestHostServesAuthenticatedAdminAndCancelsDataOnShutdown(t *testing.T) {
	certificate := testHostCertificate(t)
	dataListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		dataListener.Close()
		t.Fatal(err)
	}
	started := make(chan struct{})
	canceled := make(chan struct{})
	data := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(canceled)
	})
	authenticator := &hostAuthenticator{challenges: make(chan brokertransport.AuthenticationChallenge, 4)}
	guard, err := NewCredentialGuard([]byte("synthetic-upstream-credential"))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	var audit bytes.Buffer
	host, err := NewHost(HostConfig{DataAddress: "127.0.0.1:8443", AdminAddress: "127.0.0.1:8444", AdminAudience: "broker-admin", BrokerID: "broker-1", Certificate: certificate}, HostDependencies{Data: data, Authenticator: authenticator, Guard: guard, AuditWriter: &audit})
	if err != nil {
		t.Fatal(err)
	}
	listeners := []net.Listener{dataListener, adminListener}
	var listenMu sync.Mutex
	host.listen = func(_, _ string) (net.Listener, error) {
		listenMu.Lock()
		defer listenMu.Unlock()
		listener := listeners[0]
		listeners = listeners[1:]
		return listener, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- host.Run(ctx) }()
	waitReady(t, host)
	client := hostTLSClient(t, certificate)
	adminRequest, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+adminListener.Addr().String()+ReadinessPath, nil)
	adminRequest.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	adminResponse, err := client.Do(adminRequest)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	adminBody, _ := io.ReadAll(adminResponse.Body)
	adminResponse.Body.Close()
	status, decodeErr := brokertransport.DecodeAdminStatusV1(adminBody)
	if adminResponse.StatusCode != http.StatusOK || decodeErr != nil || status.Status != brokertransport.AdminStatusReady {
		cancel()
		t.Fatalf("status=%d body=%s decoded=%+v err=%v", adminResponse.StatusCode, adminBody, status, decodeErr)
	}
	challenge := <-authenticator.challenges
	if challenge.Audience != "broker-admin" || challenge.BrokerID != "broker-1" {
		cancel()
		t.Fatalf("challenge=%+v", challenge)
	}
	dataRequest, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+dataListener.Addr().String()+ProtocolPath, nil)
	dataDone := make(chan error, 1)
	go func() {
		response, requestErr := client.Do(dataRequest)
		if response != nil {
			response.Body.Close()
		}
		dataDone <- requestErr
	}()
	<-started
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel admitted data request")
	}
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	<-dataDone
	if host.Readiness() != brokertransport.AdminStatusUnavailable || bytes.Contains(audit.Bytes(), []byte("synthetic-workload-credential")) || bytes.Contains(audit.Bytes(), []byte("synthetic-upstream-credential")) {
		t.Fatalf("readiness=%q audit=%s", host.Readiness(), audit.Bytes())
	}
	for _, line := range bytes.Split(bytes.TrimSpace(audit.Bytes()), []byte{'\n'}) {
		var event AuditEvent
		if err := json.Unmarshal(line, &event); err != nil || !validAuditEvent(event) {
			t.Fatalf("event=%s err=%v", line, err)
		}
	}
}

func TestHostAdmissionLimiterRefusesWithoutQueue(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newAdmissionLimiter(4, 4, func() time.Time { return now })
	for index := 0; index < 4; index++ {
		if !limiter.Allow() {
			t.Fatalf("admission %d refused", index)
		}
	}
	if limiter.Allow() {
		t.Fatal("burst exceeded")
	}
	now = now.Add(250 * time.Millisecond)
	if !limiter.Allow() || limiter.Allow() {
		t.Fatal("rate refill was not exactly one token")
	}
	now = now.Add(-time.Second)
	if limiter.Allow() {
		t.Fatal("clock rollback extended admission")
	}
}

type hostBoundaryRoute struct {
	name    string
	method  string
	path    string
	version int
	decode  func([]byte) (brokertransport.Failure, error)
}

func hostBoundaryRoutes() []hostBoundaryRoute {
	return []hostBoundaryRoute{
		{name: "protocol v1", method: http.MethodGet, path: ProtocolPath, version: 1, decode: brokertransport.DecodeFailureV1},
		{name: "execution v1", method: http.MethodPost, path: ExecutePath, version: 1, decode: brokertransport.DecodeFailureV1},
		{name: "discovery v2 negotiation", method: http.MethodPost, path: brokertransport.DiscoveryNegotiatePathV2, version: 2, decode: brokertransport.DecodeDiscoveryFailureV2},
		{name: "discovery v2", method: http.MethodPost, path: brokertransport.DiscoveryPathV2, version: 2, decode: brokertransport.DecodeDiscoveryFailureV2},
		{name: "cache v2", method: http.MethodPost, path: brokertransport.CacheQualificationPathV2, version: 2, decode: brokertransport.DecodeCacheFailureV2},
		{name: "execution v2", method: http.MethodPost, path: brokertransport.ExecutePathV2, version: 2, decode: brokertransport.DecodeExecutionFailureV2},
		{name: "discovery v3 negotiation", method: http.MethodPost, path: brokertransport.DiscoveryNegotiatePathV3, version: 3, decode: brokertransport.DecodeDiscoveryFailureV3},
		{name: "discovery v3", method: http.MethodPost, path: brokertransport.DiscoveryPathV3, version: 3, decode: brokertransport.DecodeDiscoveryFailureV3},
		{name: "execution v3", method: http.MethodPost, path: brokertransport.ExecutePathV3, version: 3, decode: brokertransport.DecodeExecutionFailureV3},
		{name: "discovery v4 negotiation", method: http.MethodPost, path: brokertransport.DiscoveryNegotiatePathV4, version: 4, decode: brokertransport.DecodeDiscoveryFailureV4},
		{name: "discovery v4", method: http.MethodPost, path: brokertransport.DiscoveryPathV4, version: 4, decode: brokertransport.DecodeDiscoveryFailureV4},
	}
}

func TestHostBoundaryFailuresUseRequestRouteVersionBeforeHandler(t *testing.T) {
	boundaries := []struct {
		name      string
		configure func(*Host, *admissionLimiter, http.Handler) http.Handler
	}{
		{name: "host unavailable", configure: func(host *Host, limiter *admissionLimiter, next http.Handler) http.Handler {
			host.state.Store(0)
			return host.admissionHandler(true, limiter, next)
		}},
		{name: "rate overload", configure: func(host *Host, limiter *admissionLimiter, next http.Handler) http.Handler {
			host.state.Store(1)
			limiter.tokens = 0
			return host.admissionHandler(true, limiter, next)
		}},
		{name: "lifecycle drain", configure: func(host *Host, _ *admissionLimiter, next http.Handler) http.Handler {
			host.state.Store(2)
			return host.lifecycleHandler(next)
		}},
	}

	for _, route := range hostBoundaryRoutes() {
		for _, boundary := range boundaries {
			t.Run(route.name+"/"+boundary.name, func(t *testing.T) {
				guard, err := NewCredentialGuard([]byte("synthetic-upstream-credential"))
				if err != nil {
					t.Fatal(err)
				}
				defer guard.Close()
				host := &Host{guard: guard, active: map[uint64]context.CancelFunc{}}
				if boundary.name != "lifecycle drain" {
					host.accepting = true
				}
				limiter := newAdmissionLimiter(1, 1, time.Now)
				var handlerCalls atomic.Int32
				next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handlerCalls.Add(1) })
				handler := boundary.configure(host, limiter, next)
				var requestBody io.Reader
				if route.method == http.MethodPost {
					requestBody = strings.NewReader(`{}`)
				}
				request := httptest.NewRequest(route.method, route.path, requestBody)
				if route.method == http.MethodPost {
					request.Header.Set("Content-Type", "application/json")
				}
				request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)

				failure, decodeErr := route.decode(response.Body.Bytes())
				if response.Code != http.StatusServiceUnavailable || decodeErr != nil || failure.SchemaVersion != route.version || failure.Reason != domain.BrokerReasonAuthorizationUnavailable || handlerCalls.Load() != 0 {
					t.Fatalf("status=%d failure=%+v decode=%v handler_calls=%d body=%s", response.Code, failure, decodeErr, handlerCalls.Load(), response.Body.Bytes())
				}
				if response.Header().Get("X-ATL-Correlation-ID") != "" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("headers=%v", response.Header())
				}
			})
		}
	}
}

func TestHostBoundaryPositiveRoutesReachHandler(t *testing.T) {
	for _, route := range hostBoundaryRoutes() {
		t.Run(route.name, func(t *testing.T) {
			guard, err := NewCredentialGuard()
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close()
			host := &Host{guard: guard, active: map[uint64]context.CancelFunc{}, accepting: true}
			host.state.Store(1)
			limiter := newAdmissionLimiter(1, 1, time.Now)
			var handlerCalls atomic.Int32
			next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				handlerCalls.Add(1)
				writer.WriteHeader(http.StatusNoContent)
			})
			request := httptest.NewRequest(route.method, route.path, nil)
			response := httptest.NewRecorder()
			host.lifecycleHandler(host.admissionHandler(true, limiter, next)).ServeHTTP(response, request)
			if response.Code != http.StatusNoContent || handlerCalls.Load() != 1 {
				t.Fatalf("status=%d handler_calls=%d", response.Code, handlerCalls.Load())
			}
		})
	}
}

func TestAuditRefusesEventVocabularyMatchingWorkloadCredential(t *testing.T) {
	guard, err := NewCredentialGuard()
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	var output bytes.Buffer
	audit, err := NewAudit(&output, guard)
	if err != nil {
		t.Fatal(err)
	}
	handler := audit.Wrap("data_execute", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) }))
	request := httptest.NewRequest(http.MethodGet, ExecutePath, nil)
	request.Header.Set("Authorization", "Bearer data_execute")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	safe := audit.Wrap("data_protocol", http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) }))
	safe.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProtocolPath, nil))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := audit.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !audit.Healthy() || output.Len() == 0 || !bytes.Contains(output.Bytes(), []byte(`"dropped_events":1`)) {
		t.Fatalf("healthy=%t output=%q", audit.Healthy(), output.String())
	}
}

func TestAuditReplacesHandlerPanicWithClosedCategory(t *testing.T) {
	guard, _ := NewCredentialGuard()
	defer guard.Close()
	var output bytes.Buffer
	audit, err := NewAudit(&output, guard)
	if err != nil {
		t.Fatal(err)
	}
	handler := audit.Wrap("data_protocol", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private panic canary") }))
	func() {
		defer func() {
			if recovered := recover(); recovered != "Broker handler failed" {
				t.Fatalf("panic=%v", recovered)
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ProtocolPath, nil))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := audit.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte("private panic canary")) || !bytes.Contains(output.Bytes(), []byte(`"reason":"authorization_unavailable"`)) {
		t.Fatalf("audit=%q", output.String())
	}
}

func TestHostRequestDeadlineCallbackStopsBeforeWriterReuse(t *testing.T) {
	host := &Host{active: map[uint64]context.CancelFunc{}, accepting: true}
	host.state.Store(1)
	now := time.Now()
	limiter := newAdmissionLimiter(1, 1, func() time.Time { return now })
	release := now.Add(time.Second)
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(writer).SetWriteDeadline(release); err != nil {
			t.Fatal(err)
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	writer := &deadlineResponseWriter{}
	request := httptest.NewRequest(http.MethodGet, ProtocolPath, nil)
	host.lifecycleHandler(host.admissionHandler(true, limiter, next)).ServeHTTP(writer, request)
	if writer.status != http.StatusNoContent || !writer.writeDeadline.Equal(release) {
		t.Fatalf("status=%d retained deadline=%v", writer.status, writer.writeDeadline)
	}
	host.activeMu.Lock()
	active := len(host.active)
	host.activeMu.Unlock()
	if active != 0 {
		t.Fatalf("active requests=%d", active)
	}
}

func TestHostAdmissionFailureDoesNotEchoWorkloadCredential(t *testing.T) {
	guard, err := NewCredentialGuard()
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	for _, route := range hostBoundaryRoutes() {
		t.Run(route.name, func(t *testing.T) {
			host := &Host{guard: guard, active: map[uint64]context.CancelFunc{}}
			host.state.Store(1)
			limiter := newAdmissionLimiter(1, 1, time.Now)
			if !limiter.Allow() {
				t.Fatal("failed to consume test admission token")
			}
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("rejected request reached handler") })
			request := httptest.NewRequest(route.method, route.path, nil)
			request.Header.Set("Authorization", "Bearer authorization_unavailable")
			response := httptest.NewRecorder()
			host.admissionHandler(true, limiter, next).ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || response.Body.Len() != 0 {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestHostRejectsPublicOrSharedListener(t *testing.T) {
	server := newHTTPServer(http.NotFoundHandler())
	if server.ErrorLog == nil || server.ErrorLog.Writer() != io.Discard {
		t.Fatal("host exposes default net/http diagnostics")
	}
	certificate := testHostCertificate(t)
	guard, _ := NewCredentialGuard([]byte("synthetic-upstream-credential"))
	defer guard.Close()
	authenticator := &hostAuthenticator{challenges: make(chan brokertransport.AuthenticationChallenge, 1)}
	data := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, config := range []HostConfig{
		{DataAddress: "0.0.0.0:8443", AdminAddress: "127.0.0.1:8444", AdminAudience: "broker-admin", BrokerID: "broker-1", Certificate: certificate},
		{DataAddress: "127.0.0.1:8443", AdminAddress: "127.0.0.1:8443", AdminAudience: "broker-admin", BrokerID: "broker-1", Certificate: certificate},
	} {
		if _, err := NewHost(config, HostDependencies{Data: data, Authenticator: authenticator, Guard: guard, AuditWriter: io.Discard}); err == nil {
			t.Fatalf("config=%+v", config)
		}
	}
}

func TestHostRetainsDependenciesUntilTimedOutHandlerExits(t *testing.T) {
	certificate := testHostCertificate(t)
	dataListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		dataListener.Close()
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	data := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	})
	authenticator := &hostAuthenticator{challenges: make(chan brokertransport.AuthenticationChallenge, 1)}
	guard, _ := NewCredentialGuard([]byte("synthetic-upstream-credential"))
	defer guard.Close()
	host, err := NewHost(HostConfig{DataAddress: "127.0.0.1:8443", AdminAddress: "127.0.0.1:8444", AdminAudience: "broker-admin", BrokerID: "broker-1", Certificate: certificate}, HostDependencies{Data: data, Authenticator: authenticator, Guard: guard, AuditWriter: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	host.shutdownGrace = 50 * time.Millisecond
	listeners := []net.Listener{dataListener, adminListener}
	var listenMu sync.Mutex
	host.listen = func(_, _ string) (net.Listener, error) {
		listenMu.Lock()
		defer listenMu.Unlock()
		listener := listeners[0]
		listeners = listeners[1:]
		return listener, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- host.Run(ctx) }()
	waitReady(t, host)
	client := hostTLSClient(t, certificate)
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+dataListener.Addr().String()+ProtocolPath, nil)
	requestDone := make(chan struct{})
	go func() {
		response, _ := client.Do(request)
		if response != nil {
			response.Body.Close()
		}
		close(requestDone)
	}()
	<-started
	cancel()
	select {
	case err := <-runDone:
		if err == nil || host.Drained() {
			t.Fatalf("err=%v drained=%t", err, host.Drained())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("host exceeded its single shutdown grace")
	}
	close(release)
	select {
	case <-host.Done():
	case <-time.After(time.Second):
		t.Fatal("handler completion was not published")
	}
	if !host.Drained() {
		t.Fatal("host did not become safe to dispose")
	}
	<-requestDone
}

func testHostCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.StartTLS()
	certificate := server.TLS.Certificates[0]
	server.Close()
	return certificate
}

func hostTLSClient(t *testing.T, certificate tls.Certificate) *http.Client {
	t.Helper()
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, ForceAttemptHTTP2: false}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func waitReady(t *testing.T, host *Host) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for host.Readiness() != brokertransport.AdminStatusReady {
		if time.Now().After(deadline) {
			t.Fatal("host did not become ready")
		}
		runtime.Gosched()
	}
}
