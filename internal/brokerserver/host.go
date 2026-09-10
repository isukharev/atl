package brokerserver

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/netutil"

	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

const (
	HostMaxConnections    = 16
	HostDataConnections   = 12
	HostAdminConnections  = 4
	HostDataRate          = 4
	HostAdminRate         = 2
	HostReadHeaderTimeout = 5 * time.Second
	HostReadTimeout       = 10 * time.Second
	HostIdleTimeout       = 15 * time.Second
	HostRequestTimeout    = 60 * time.Second
	HostWriteTimeout      = 65 * time.Second
	HostShutdownGrace     = 5 * time.Second
)

type HostConfig struct {
	DataAddress   string
	AdminAddress  string
	AdminAudience string
	BrokerID      string
	Certificate   tls.Certificate
}

type HostDependencies struct {
	Data          http.Handler
	Authenticator brokertransport.Authenticator
	Guard         *CredentialGuard
	AuditWriter   io.Writer
}

type Host struct {
	config  HostConfig
	data    http.Handler
	admin   *AdminHandler
	audit   *Audit
	guard   *CredentialGuard
	state   atomic.Uint32
	started atomic.Bool

	listen func(network, address string) (net.Listener, error)
	now    func() time.Time

	activeMu      sync.Mutex
	activeID      uint64
	active        map[uint64]context.CancelFunc
	accepting     bool
	activeWG      sync.WaitGroup
	done          chan struct{}
	doneOnce      sync.Once
	drained       atomic.Bool
	shutdownGrace time.Duration
}

type admissionLimiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func NewHost(config HostConfig, dependencies HostDependencies) (*Host, error) {
	if !validHostAddress(config.DataAddress) || !validHostAddress(config.AdminAddress) || config.DataAddress == config.AdminAddress || config.AdminAudience == "" || config.BrokerID == "" ||
		len(config.Certificate.Certificate) == 0 || config.Certificate.PrivateKey == nil || dependencies.Data == nil || dependencies.Authenticator == nil || dependencies.Guard == nil || dependencies.AuditWriter == nil {
		return nil, hostError(domain.ErrUsage)
	}
	host := &Host{config: config, data: dependencies.Data, guard: dependencies.Guard, listen: net.Listen, now: time.Now, active: map[uint64]context.CancelFunc{}, done: make(chan struct{}), shutdownGrace: HostShutdownGrace}
	audit, err := NewAudit(dependencies.AuditWriter, dependencies.Guard)
	if err != nil {
		return nil, hostError(err)
	}
	host.audit = audit
	admin, err := NewAdmin(AdminConfig{Audience: config.AdminAudience, BrokerID: config.BrokerID, MaxConcurrent: 2}, AdminDependencies{Authenticator: dependencies.Authenticator, Guard: dependencies.Guard, Readiness: host.Readiness})
	if err != nil {
		closeContext, cancel := context.WithTimeout(context.Background(), HostShutdownGrace)
		defer cancel()
		_ = audit.Close(closeContext)
		return nil, hostError(err)
	}
	host.admin = admin
	return host, nil
}

func (h *Host) Readiness() string {
	if h == nil || h.audit == nil || !h.audit.Healthy() {
		return brokertransport.AdminStatusUnavailable
	}
	switch h.state.Load() {
	case 1:
		return brokertransport.AdminStatusReady
	case 2:
		return brokertransport.AdminStatusDraining
	default:
		return brokertransport.AdminStatusUnavailable
	}
}

func (h *Host) Close() {
	if h == nil {
		return
	}
	if h.started.CompareAndSwap(false, true) {
		h.state.Store(0)
		h.closeAudit()
		h.markDone()
	}
}

func (h *Host) Done() <-chan struct{} { return h.done }
func (h *Host) Drained() bool         { return h != nil && h.drained.Load() }

func (h *Host) Run(ctx context.Context) error {
	if h == nil || ctx == nil || !h.started.CompareAndSwap(false, true) {
		return hostError(domain.ErrUsage)
	}
	if ctx.Err() != nil {
		h.closeAudit()
		h.markDone()
		return nil
	}
	dataListener, err := h.listen("tcp", h.config.DataAddress)
	if err != nil {
		h.closeAudit()
		h.markDone()
		return hostError(domain.ErrConfig)
	}
	adminListener, err := h.listen("tcp", h.config.AdminAddress)
	if err != nil {
		_ = dataListener.Close()
		h.closeAudit()
		h.markDone()
		return hostError(domain.ErrConfig)
	}

	dataLimiter := newAdmissionLimiter(HostDataRate, HostDataRate, h.now)
	adminLimiter := newAdmissionLimiter(HostAdminRate, HostAdminRate, h.now)
	dataHandler := h.lifecycleHandler(h.auditedData(h.admissionHandler(true, dataLimiter, h.data)))
	adminHandler := h.lifecycleHandler(h.auditedAdmin(h.admissionHandler(false, adminLimiter, h.admin)))
	dataServer := newHTTPServer(dataHandler)
	adminServer := newHTTPServer(adminHandler)
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}, Certificates: []tls.Certificate{h.config.Certificate}}
	dataServer.TLSConfig = tlsConfig.Clone()
	adminServer.TLSConfig = tlsConfig.Clone()
	dataServer.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
	adminServer.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}

	h.activeMu.Lock()
	h.accepting = true
	h.activeMu.Unlock()
	errorsCh := make(chan error, 2)
	go func() {
		errorsCh <- dataServer.Serve(tls.NewListener(netutil.LimitListener(dataListener, HostDataConnections), dataServer.TLSConfig))
	}()
	go func() {
		errorsCh <- adminServer.Serve(tls.NewListener(netutil.LimitListener(adminListener, HostAdminConnections), adminServer.TLSConfig))
	}()
	h.state.Store(1)

	var runErr error
	select {
	case <-ctx.Done():
	case serveErr := <-errorsCh:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			runErr = hostError(domain.ErrCheckFailed)
		}
	}
	h.state.Store(2)
	h.stopAndCancelActive()
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), h.shutdownGrace)
	defer shutdownCancel()
	shutdownErr := shutdownServers(shutdownContext, dataServer, adminServer)
	_ = dataServer.Close()
	_ = adminServer.Close()
	_ = dataListener.Close()
	_ = adminListener.Close()
	go func() {
		h.activeWG.Wait()
		h.markDone()
	}()
	drained := false
	select {
	case <-h.done:
		drained = true
	case <-shutdownContext.Done():
	}
	auditErr := h.audit.Close(shutdownContext)
	h.state.Store(0)
	if runErr != nil {
		return runErr
	}
	if shutdownErr != nil || !drained || auditErr != nil {
		return hostError(domain.ErrCheckFailed)
	}
	return nil
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler: handler, ReadHeaderTimeout: HostReadHeaderTimeout, ReadTimeout: HostReadTimeout,
		WriteTimeout: HostWriteTimeout, IdleTimeout: HostIdleTimeout, MaxHeaderBytes: MaxRequestHeaderBytes,
		ErrorLog: log.New(io.Discard, "", 0),
	}
}

func (h *Host) admissionHandler(data bool, limiter *admissionLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		state := h.state.Load()
		if state == 0 || data && state != 1 {
			h.writeHostFailure(writer, request)
			return
		}
		if !limiter.Allow() {
			h.writeHostFailure(writer, request)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (h *Host) lifecycleHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request == nil {
			writeUnscannedFailure(writer, domain.BrokerReasonMalformed)
			return
		}
		bounded, cancel := context.WithTimeout(request.Context(), HostRequestTimeout)
		id, registered := h.register(cancel)
		if !registered {
			cancel()
			h.writeHostFailure(writer, request)
			return
		}
		defer h.unregister(id, cancel)
		request = request.WithContext(bounded)
		controller := http.NewResponseController(writer)
		deadlineDone := make(chan struct{})
		stopDeadline := context.AfterFunc(bounded, func() {
			defer close(deadlineDone)
			_ = controller.SetWriteDeadline(time.Now())
		})
		defer func() {
			if !stopDeadline() {
				<-deadlineDone
			}
		}()
		next.ServeHTTP(writer, request)
	})
}

func (h *Host) writeHostFailure(writer http.ResponseWriter, request *http.Request) {
	var credential []byte
	if request != nil {
		credential, _ = workloadBearer(request)
	}
	defer clear(credential)
	reason := domain.BrokerReasonAuthorizationUnavailable
	server := &Handler{guard: h.guard}
	if request != nil && request.URL != nil {
		switch request.URL.Path {
		case brokertransport.DiscoveryNegotiatePathV2, brokertransport.DiscoveryPathV2:
			body, err := brokertransport.EncodeDiscoveryFailureV2(reason)
			if err == nil {
				server.writeFailureBody(writer, reason, body, credential, "")
			}
			return
		case brokertransport.CacheQualificationPathV2:
			body, err := brokertransport.EncodeCacheFailureV2(reason)
			if err == nil {
				server.writeFailureBody(writer, reason, body, credential, "")
			}
			return
		case brokertransport.ExecutePathV2:
			body, err := brokertransport.EncodeExecutionFailureV2(reason)
			if err == nil {
				server.writeFailureBody(writer, reason, body, credential, "")
			}
			return
		case brokertransport.ExecutePathV3:
			body, err := brokertransport.EncodeExecutionFailureV3(reason)
			if err == nil {
				server.writeFailureBody(writer, reason, body, credential, "")
			}
			return
		case brokertransport.DiscoveryNegotiatePathV4, brokertransport.DiscoveryPathV4:
			body, err := brokertransport.EncodeDiscoveryFailureV4(reason)
			if err == nil {
				server.writeFailureBody(writer, reason, body, credential, "")
			}
			return
		case brokertransport.DiscoveryNegotiatePathV3, brokertransport.DiscoveryPathV3:
			body, err := brokertransport.EncodeDiscoveryFailureV3(reason)
			if err == nil {
				server.writeFailureBody(writer, reason, body, credential, "")
			}
			return
		}
	}
	server.writeFailure(writer, reason, credential, "")
}

func (h *Host) auditedData(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		route := "data_unknown"
		if request != nil && request.URL != nil {
			switch request.URL.Path {
			case ExecutePath:
				route = "data_execute"
			case ProtocolPath:
				route = "data_protocol"
			case brokertransport.CacheQualificationPathV2:
				route = "data_cache_qualification"
			case brokertransport.ExecutePathV2:
				route = "data_execute_v2"
			case brokertransport.ExecutePathV3:
				route = "data_execute_v3"
			case brokertransport.DiscoveryNegotiatePathV4, brokertransport.DiscoveryPathV4:
				route = "data_discovery_v4"
			case brokertransport.DiscoveryNegotiatePathV3, brokertransport.DiscoveryPathV3:
				route = "data_discovery_v3"
			}
		}
		h.audit.Wrap(route, next).ServeHTTP(writer, request)
	})
}

func (h *Host) auditedAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		route := "admin_unknown"
		if request != nil && request.URL != nil {
			switch request.URL.Path {
			case HealthPath:
				route = "admin_health"
			case ReadinessPath:
				route = "admin_readiness"
			}
		}
		h.audit.Wrap(route, next).ServeHTTP(writer, request)
	})
}

func (h *Host) register(cancel context.CancelFunc) (uint64, bool) {
	h.activeMu.Lock()
	defer h.activeMu.Unlock()
	if !h.accepting {
		return 0, false
	}
	h.activeID++
	h.activeWG.Add(1)
	h.active[h.activeID] = cancel
	return h.activeID, true
}

func (h *Host) unregister(id uint64, cancel context.CancelFunc) {
	cancel()
	h.activeMu.Lock()
	delete(h.active, id)
	h.activeMu.Unlock()
	h.activeWG.Done()
}

func (h *Host) stopAndCancelActive() {
	h.activeMu.Lock()
	h.accepting = false
	cancels := make([]context.CancelFunc, 0, len(h.active))
	for _, cancel := range h.active {
		cancels = append(cancels, cancel)
	}
	h.activeMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (h *Host) closeAudit() {
	ctx, cancel := context.WithTimeout(context.Background(), h.shutdownGrace)
	defer cancel()
	_ = h.audit.Close(ctx)
}

func (h *Host) markDone() {
	h.doneOnce.Do(func() {
		h.drained.Store(true)
		close(h.done)
	})
}

func shutdownServers(ctx context.Context, servers ...*http.Server) error {
	results := make(chan error, len(servers))
	for _, server := range servers {
		go func() { results <- server.Shutdown(ctx) }()
	}
	var joined []error
	for range servers {
		if err := <-results; err != nil {
			joined = append(joined, err)
		}
	}
	if len(joined) != 0 {
		for _, server := range servers {
			_ = server.Close()
		}
	}
	return errors.Join(joined...)
}

func newAdmissionLimiter(rate, burst int, now func() time.Time) *admissionLimiter {
	return &admissionLimiter{rate: float64(rate), burst: float64(burst), tokens: float64(burst), last: now(), now: now}
}

func validHostAddress(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	host, port, err := net.SplitHostPort(value)
	ip := net.ParseIP(host)
	parsedPort, portErr := strconv.Atoi(port)
	return err == nil && ip != nil && ip.IsLoopback() && portErr == nil && parsedPort > 0 && parsedPort <= 65535 && strconv.Itoa(parsedPort) == port
}

func (l *admissionLimiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	elapsed := now.Sub(l.last).Seconds()
	if elapsed > 0 {
		l.tokens += elapsed * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

type brokerHostError struct{ cause error }

func (*brokerHostError) Error() string   { return "Broker host failed" }
func (e *brokerHostError) Unwrap() error { return e.cause }

func hostError(cause error) error { return &brokerHostError{cause: cause} }
