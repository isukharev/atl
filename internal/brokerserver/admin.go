package brokerserver

import (
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

const (
	HealthPath    = "/healthz"
	ReadinessPath = "/readyz"
)

type AdminConfig struct {
	Audience      string
	BrokerID      string
	MaxConcurrent int
}

type AdminDependencies struct {
	Authenticator brokertransport.Authenticator
	Guard         *CredentialGuard
	Readiness     func() string
}

type AdminHandler struct {
	config        AdminConfig
	authenticator brokertransport.Authenticator
	guard         *CredentialGuard
	readiness     func() string
	permits       chan struct{}
	random        io.Reader
	now           func() time.Time
}

func NewAdmin(config AdminConfig, dependencies AdminDependencies) (*AdminHandler, error) {
	identityErr := brokertransport.ValidateAuthenticationChallenge(brokertransport.AuthenticationChallenge{Nonce: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Audience: config.Audience, BrokerID: config.BrokerID})
	if identityErr != nil || config.MaxConcurrent <= 0 || config.MaxConcurrent > 16 || dependencies.Authenticator == nil || dependencies.Guard == nil || dependencies.Readiness == nil {
		return nil, fmt.Errorf("%w: invalid Broker admin configuration", domain.ErrUsage)
	}
	return &AdminHandler{config: config, authenticator: dependencies.Authenticator, guard: dependencies.Guard, readiness: dependencies.Readiness, permits: make(chan struct{}, config.MaxConcurrent), random: rand.Reader, now: time.Now}, nil
}

func (h *AdminHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if h == nil || request == nil {
		writeUnscannedFailure(writer, domain.BrokerReasonAuthorizationUnavailable)
		return
	}
	if !adminRouteValid(request) {
		credential, _ := workloadBearer(request)
		h.writeFailure(writer, domain.BrokerReasonMalformed, credential, "")
		clear(credential)
		return
	}
	select {
	case h.permits <- struct{}{}:
		defer func() { <-h.permits }()
	default:
		credential, _ := workloadBearer(request)
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, "")
		clear(credential)
		return
	}
	credential, err := workloadBearer(request)
	if err != nil {
		h.writeFailure(writer, domain.BrokerReasonCredentialExpired, nil, "")
		return
	}
	recordAuditCredential(writer, credential)
	defer clear(credential)
	nonce, err := nonceFrom(h.random)
	if err != nil {
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, "")
		return
	}
	recordAuditCorrelation(writer, nonce)
	authentication, err := h.authenticator.Authenticate(request.Context(), credential, brokertransport.AuthenticationChallenge{Nonce: nonce, Audience: h.config.Audience, BrokerID: h.config.BrokerID})
	if err != nil || !validAuthenticationFor(authentication, h.config.Audience, h.config.BrokerID, h.now()) {
		h.writeFailure(writer, brokerFailureReason(err, domain.BrokerReasonCredentialExpired), credential, nonce)
		return
	}
	value := brokertransport.AdminStatus{SchemaVersion: 1, Complete: true}
	switch request.URL.Path {
	case HealthPath:
		value.Kind, value.Status = brokertransport.AdminKindHealth, brokertransport.AdminStatusHealthy
	case ReadinessPath:
		value.Kind, value.Status = brokertransport.AdminKindReadiness, h.readiness()
	default:
		h.writeFailure(writer, domain.BrokerReasonMalformed, credential, nonce)
		return
	}
	body, err := brokertransport.EncodeAdminStatusV1(value)
	if err != nil || h.guard.Check(app.BrokerExactReadResult{}, body, credential) != nil {
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, nonce)
		return
	}
	server := &Handler{guard: h.guard, now: h.now}
	started, publishErr := server.publish(request.Context(), writer, body, authentication.ReleaseDeadline, nonce)
	if publishErr != nil && !started {
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, nonce)
	}
}

func (h *AdminHandler) writeFailure(writer http.ResponseWriter, reason domain.BrokerReason, credential []byte, correlation string) {
	server := &Handler{guard: h.guard}
	server.writeFailure(writer, reason, credential, correlation)
}

func adminRouteValid(request *http.Request) bool {
	if request.URL == nil || request.Method != http.MethodGet || request.ContentLength != 0 || len(request.TransferEncoding) != 0 || len(request.Header.Values("Content-Type")) != 0 ||
		request.URL.Opaque != "" || request.URL.RawPath != "" || request.URL.EscapedPath() != request.URL.Path || request.URL.RawQuery != "" || request.URL.ForceQuery ||
		headerBytes(request.Header) > MaxRequestHeaderBytes || request.Header.Get("Content-Encoding") != "" || request.Header.Get("Origin") != "" || request.Header.Get("Cookie") != "" || request.Header.Get("Upgrade") != "" || request.Header.Get("Expect") != "" {
		return false
	}
	return request.URL.Path == HealthPath || request.URL.Path == ReadinessPath
}

func nonceFrom(random io.Reader) (string, error) {
	buffer := make([]byte, 24)
	if _, err := io.ReadFull(random, buffer); err != nil {
		return "", err
	}
	return encodeNonce(buffer), nil
}
