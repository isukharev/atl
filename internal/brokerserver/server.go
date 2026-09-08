// Package brokerserver exposes the narrow authenticated HTTP boundary for ATL
// Broker operations. It dispatches only explicit semantic services.
package brokerserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

const (
	ExecutePath            = "/v1/execute"
	ProtocolPath           = "/v1/protocol"
	MaxRequestHeaderBytes  = 16 << 10
	MaxExecuteRequestBytes = 64 << 10
	DefaultMaxConcurrent   = 2
)

type Config struct {
	Audience      string
	BrokerID      string
	MaxConcurrent int
}

type Dependencies struct {
	Authenticator brokertransport.Authenticator
	Reads         *app.BrokerReadService
	Guard         *CredentialGuard
}

type Handler struct {
	config        Config
	authenticator brokertransport.Authenticator
	reads         *app.BrokerReadService
	guard         *CredentialGuard
	permits       chan struct{}
	random        io.Reader
	now           func() time.Time
}

func New(config Config, dependencies Dependencies) (*Handler, error) {
	identityErr := brokertransport.ValidateAuthenticationChallenge(brokertransport.AuthenticationChallenge{Nonce: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Audience: config.Audience, BrokerID: config.BrokerID})
	if identityErr != nil || config.MaxConcurrent <= 0 || config.MaxConcurrent > 64 || dependencies.Authenticator == nil || dependencies.Reads == nil || dependencies.Guard == nil {
		return nil, fmt.Errorf("%w: invalid Broker server configuration", domain.ErrUsage)
	}
	return &Handler{config: config, authenticator: dependencies.Authenticator, reads: dependencies.Reads, guard: dependencies.Guard, permits: make(chan struct{}, config.MaxConcurrent), random: rand.Reader, now: time.Now}, nil
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if h == nil {
		writeUnscannedFailure(writer, domain.BrokerReasonAuthorizationUnavailable)
		return
	}
	if request == nil || !h.routeValid(request) {
		h.writeFailure(writer, domain.BrokerReasonMalformed, nil, "")
		return
	}
	select {
	case h.permits <- struct{}{}:
		defer func() { <-h.permits }()
	default:
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, nil, "")
		return
	}
	credential, err := workloadBearer(request)
	if err != nil {
		h.writeFailure(writer, brokerFailureReason(err, domain.BrokerReasonCredentialExpired), nil, "")
		return
	}
	defer clear(credential)
	var operation domain.BrokerRequest
	if request.URL.Path == ExecutePath {
		body, readErr := readRequestBody(request, MaxExecuteRequestBytes)
		if readErr != nil {
			h.writeFailure(writer, domain.BrokerReasonMalformed, credential, "")
			return
		}
		operation, err = brokercontract.DecodeRequestV1(body)
		if err != nil {
			h.writeFailure(writer, brokerFailureReason(err, domain.BrokerReasonMalformed), credential, "")
			return
		}
	}
	nonce, err := h.nonce()
	if err != nil {
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, "")
		return
	}
	authentication, err := h.authenticator.Authenticate(request.Context(), credential, brokertransport.AuthenticationChallenge{Nonce: nonce, Audience: h.config.Audience, BrokerID: h.config.BrokerID})
	if err != nil || !h.validAuthentication(authentication) {
		h.writeFailure(writer, brokerFailureReason(err, domain.BrokerReasonCredentialExpired), credential, nonce)
		return
	}

	switch request.URL.Path {
	case ProtocolPath:
		encoded, encodeErr := brokertransport.EncodeProtocolV1(brokertransport.StaticProtocolV1())
		if encodeErr != nil || h.guard.Check(app.BrokerExactReadResult{}, encoded, credential) != nil {
			h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, nonce)
			return
		}
		started, publishErr := h.publish(request.Context(), writer, encoded, authentication.ReleaseDeadline, nonce)
		if publishErr != nil && !started {
			h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, nonce)
		}
	case ExecutePath:
		h.execute(writer, request, operation, authentication.Context, credential, nonce)
	default:
		h.writeFailure(writer, domain.BrokerReasonMalformed, credential, nonce)
	}
}

func (h *Handler) execute(writer http.ResponseWriter, request *http.Request, operation domain.BrokerRequest, verified domain.BrokerVerifiedContext, credential []byte, correlation string) {
	result, err := h.reads.Execute(request.Context(), operation, verified)
	if err != nil {
		h.writeFailure(writer, brokerFailureReason(err, domain.BrokerReasonAuthorizationUnavailable), credential, correlation)
		return
	}
	var encoded []byte
	switch {
	case result.JiraIssue != nil && result.ConfluencePage == nil:
		encoded, err = brokercontract.EncodeJiraIssueReadResultV1(*result.JiraIssue)
	case result.ConfluencePage != nil && result.JiraIssue == nil:
		encoded, err = brokercontract.EncodeConfluencePageReadResultV1(*result.ConfluencePage)
	default:
		err = domain.ErrCheckFailed
	}
	if err != nil || h.guard.Check(result, encoded, credential) != nil {
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, correlation)
		return
	}
	started, publishErr := h.publish(request.Context(), writer, encoded, result.ReleaseDeadline, correlation)
	if publishErr != nil && !started {
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, correlation)
	}
}

func (h *Handler) publish(ctx context.Context, writer http.ResponseWriter, body []byte, deadline time.Time, correlation string) (bool, error) {
	if ctx == nil {
		return false, domain.ErrUsage
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if requestDeadline, ok := ctx.Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	if len(body) == 0 || deadline.IsZero() || !h.now().Before(deadline) {
		return false, context.DeadlineExceeded
	}
	controller := http.NewResponseController(writer)
	if err := controller.SetWriteDeadline(deadline); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !h.now().Before(deadline) {
		return false, context.DeadlineExceeded
	}
	header := writer.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Type", "application/json")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	header.Set("X-Content-Type-Options", "nosniff")
	if h.guard.Check(app.BrokerExactReadResult{}, []byte(correlation), nil) == nil {
		header.Set("X-ATL-Correlation-ID", correlation)
	}
	writer.WriteHeader(http.StatusOK)
	written, err := writer.Write(body)
	if err == nil && written != len(body) {
		err = io.ErrShortWrite
	}
	return true, err
}

func (h *Handler) routeValid(request *http.Request) bool {
	if request.URL == nil || request.URL.Opaque != "" || request.URL.RawPath != "" || request.URL.EscapedPath() != request.URL.Path || request.URL.RawQuery != "" || request.URL.ForceQuery || headerBytes(request.Header) > MaxRequestHeaderBytes ||
		request.Header.Get("Content-Encoding") != "" || request.Header.Get("Origin") != "" || request.Header.Get("Cookie") != "" || request.Header.Get("Upgrade") != "" || request.Header.Get("Expect") != "" {
		return false
	}
	switch request.URL.Path {
	case ExecutePath:
		return request.Method == http.MethodPost && len(request.Header.Values("Content-Type")) == 1 && request.Header.Get("Content-Type") == "application/json"
	case ProtocolPath:
		return request.Method == http.MethodGet && request.ContentLength == 0 && len(request.TransferEncoding) == 0 && len(request.Header.Values("Content-Type")) == 0
	default:
		return false
	}
}

func (h *Handler) nonce() (string, error) {
	buffer := make([]byte, 24)
	if _, err := io.ReadFull(h.random, buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func workloadBearer(request *http.Request) ([]byte, error) {
	values := request.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return nil, domain.ErrAuth
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if len(token) == 0 || len(token) > brokertransport.MaxWorkloadCredentialBytes || strings.ContainsAny(token, " \t\r\n,") {
		return nil, domain.ErrAuth
	}
	for _, current := range []byte(token) {
		if current < 0x21 || current > 0x7e {
			return nil, domain.ErrAuth
		}
	}
	return []byte(token), nil
}

func readRequestBody(request *http.Request, maximum int64) ([]byte, error) {
	if request.Body == nil || request.ContentLength > maximum {
		return nil, domain.ErrUsage
	}
	limited := io.LimitReader(request.Body, maximum+1)
	body, err := io.ReadAll(limited)
	if err != nil || int64(len(body)) > maximum || len(body) == 0 {
		return nil, domain.ErrUsage
	}
	return body, nil
}

func headerBytes(header http.Header) int {
	total := 0
	for name, values := range header {
		total += len(name) + 4
		for _, value := range values {
			total += len(value) + 2
		}
	}
	return total
}

func brokerFailureReason(err error, fallback domain.BrokerReason) domain.BrokerReason {
	if reason, ok := brokercontract.Reason(err); ok {
		return reason
	}
	switch {
	case errors.Is(err, domain.ErrUsage):
		return domain.BrokerReasonMalformed
	case errors.Is(err, domain.ErrAuth):
		return domain.BrokerReasonCredentialExpired
	case errors.Is(err, domain.ErrForbidden):
		return domain.BrokerReasonDenied
	case errors.Is(err, context.DeadlineExceeded):
		return domain.BrokerReasonDecisionExpired
	default:
		return fallback
	}
}

func (h *Handler) validAuthentication(authentication brokertransport.Authentication) bool {
	now := h.now()
	if _, err := brokercontract.EncodeVerifiedContextV1(authentication.Context); err != nil || authentication.Context.Audience != h.config.Audience || authentication.Context.BrokerID != h.config.BrokerID ||
		authentication.ReleaseDeadline.IsZero() || !now.Before(authentication.ReleaseDeadline) || authentication.ReleaseDeadline.Sub(now) > time.Duration(brokertransport.AuthenticationLeaseMillis)*time.Millisecond ||
		now.UnixMilli() < authentication.Context.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis ||
		authentication.ReleaseDeadline.UnixMilli() > authentication.Context.ExecutionExpiresMillis || authentication.ReleaseDeadline.UnixMilli() > authentication.Context.GrantExpiresMillis || authentication.ReleaseDeadline.UnixMilli() > authentication.Context.CredentialExpiresMillis {
		return false
	}
	return true
}

func (h *Handler) writeFailure(writer http.ResponseWriter, reason domain.BrokerReason, credential []byte, correlation string) {
	failure, err := brokertransport.NewFailure(reason)
	if err != nil {
		failure, _ = brokertransport.NewFailure(domain.BrokerReasonAuthorizationUnavailable)
	}
	body, err := brokertransport.EncodeFailureV1(failure)
	if err != nil {
		return
	}
	if h.guard.Check(app.BrokerExactReadResult{}, body, credential) != nil {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Length", "0")
		writer.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	header := writer.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Type", "application/json")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	header.Set("X-Content-Type-Options", "nosniff")
	if correlation != "" && h.guard.Check(app.BrokerExactReadResult{}, []byte(correlation), credential) == nil {
		header.Set("X-ATL-Correlation-ID", correlation)
	}
	writer.WriteHeader(failureHTTPStatus(failure.Reason))
	_, _ = writer.Write(body)
}

func writeUnscannedFailure(writer http.ResponseWriter, reason domain.BrokerReason) {
	failure, err := brokertransport.NewFailure(reason)
	if err != nil {
		return
	}
	body, err := brokertransport.EncodeFailureV1(failure)
	if err != nil {
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(failureHTTPStatus(reason))
	_, _ = writer.Write(body)
}

func failureHTTPStatus(reason domain.BrokerReason) int {
	switch reason {
	case domain.BrokerReasonMalformed:
		return http.StatusBadRequest
	case domain.BrokerReasonUnsupported:
		return http.StatusUnprocessableEntity
	case domain.BrokerReasonCredentialExpired:
		return http.StatusUnauthorized
	case domain.BrokerReasonDenied, domain.BrokerReasonRevoked, domain.BrokerReasonGrantExpired, domain.BrokerReasonStaleExecution, domain.BrokerReasonStaleAuthority, domain.BrokerReasonUnsupportedConsistency, domain.BrokerReasonProposalClearanceRequired:
		return http.StatusForbidden
	case domain.BrokerReasonDecisionExpired:
		return http.StatusGatewayTimeout
	default:
		return http.StatusServiceUnavailable
	}
}
