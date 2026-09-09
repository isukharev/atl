package brokerserver

import (
	"context"
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func (h *Handler) serveCacheQualification(writer http.ResponseWriter, request *http.Request, started time.Time) {
	routeDeadline := started.Add(time.Duration(domain.BrokerMaxDecisionLeaseMillis) * time.Millisecond)
	if parentDeadline, ok := request.Context().Deadline(); ok && parentDeadline.Before(routeDeadline) {
		routeDeadline = parentDeadline
	}
	controller := http.NewResponseController(writer)
	if controller.SetWriteDeadline(routeDeadline) != nil {
		return
	}
	if controller.SetReadDeadline(routeDeadline) != nil {
		return
	}
	bounded, cancel := context.WithDeadline(request.Context(), routeDeadline)
	defer cancel()
	request = request.WithContext(bounded)
	var credential []byte
	defer func() { clear(credential) }()
	fail := func(reason domain.BrokerReason, correlation string) {
		body, err := brokertransport.EncodeCacheFailureV2(reason)
		if err == nil {
			h.writeFailureBody(writer, reason, body, credential, correlation)
		}
	}
	if !h.routeValid(request) {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
	credential, credentialErr := workloadBearer(request)
	select {
	case h.permits <- struct{}{}:
		defer func() { <-h.permits }()
	default:
		fail(domain.BrokerReasonAuthorizationUnavailable, "")
		return
	}
	if credentialErr != nil {
		fail(domain.BrokerReasonCredentialExpired, "")
		return
	}
	recordAuditCredential(writer, credential)
	body, err := readRequestBody(request, brokertransport.MaxCacheQualificationBytesV2)
	if err != nil {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
	claim, err := brokertransport.DecodeCacheQualificationClaimV2(body)
	if err != nil {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
	nonce, err := h.nonce()
	if err != nil {
		fail(domain.BrokerReasonAuthorizationUnavailable, "")
		return
	}
	recordAuditCorrelation(writer, nonce)
	authentication, err := h.authenticator.Authenticate(request.Context(), credential, brokertransport.AuthenticationChallenge{Nonce: nonce, Audience: h.config.Audience, BrokerID: h.config.BrokerID})
	if err != nil || !h.validAuthentication(authentication) {
		fail(brokerFailureReason(err, domain.BrokerReasonCredentialExpired), nonce)
		return
	}
	if h.cache == nil {
		fail(domain.BrokerReasonUnsupported, nonce)
		return
	}
	candidate, deadline, err := brokertransport.BindCacheQualificationClaimV2(claim, authentication.Context, started, h.now(), authentication.ReleaseDeadline)
	if err != nil {
		fail(brokerFailureReason(err, domain.BrokerReasonStaleExecution), nonce)
		return
	}
	resolved, release, err := h.cache.Resolve(request.Context(), candidate, deadline)
	if err != nil {
		fail(brokerFailureReason(err, domain.BrokerReasonAuthorizationUnavailable), nonce)
		return
	}
	envelope, err := brokertransport.NewCacheQualificationEnvelopeV2(claim, resolved, release)
	if err == nil {
		body, err = brokertransport.EncodeCacheQualificationEnvelopeV2(envelope)
	}
	if err != nil || h.guard.Check(app.BrokerExactReadResult{}, body, credential) != nil {
		fail(domain.BrokerReasonAuthorizationUnavailable, nonce)
		return
	}
	published, publishErr := h.publish(request.Context(), writer, body, release, nonce)
	if publishErr != nil && !published {
		fail(domain.BrokerReasonDecisionExpired, nonce)
	}
}
