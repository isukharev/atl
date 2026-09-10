package brokerserver

import (
	"context"
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func (h *Handler) serveAttachmentDiscovery(writer http.ResponseWriter, request *http.Request, started time.Time) {
	request, finish, err := beginBoundedRoute(writer, request, started, time.Duration(domain.BrokerMaxDecisionLeaseMillis)*time.Millisecond)
	if err != nil {
		return
	}
	defer finish()
	credential, credentialErr := workloadBearer(request)
	defer clear(credential)
	fail := func(reason domain.BrokerReason, correlation string) {
		body, err := brokertransport.EncodeDiscoveryFailureV4(reason)
		if err == nil {
			h.writeFailureBody(writer, reason, body, credential, correlation)
			_ = http.NewResponseController(writer).Flush()
		}
	}
	if !h.routeValid(request) {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
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
	body, err := readRequestBody(request, brokertransport.MaxDiscoveryNegotiationBytesV4)
	if err != nil {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
	var hello brokertransport.DiscoveryNegotiationV4
	var discovery domain.BrokerFamilyDiscoveryRequestV4
	if request.URL.Path == brokertransport.DiscoveryNegotiatePathV4 {
		hello, err = brokertransport.DecodeDiscoveryNegotiationV4(body)
	} else {
		discovery, err = brokercontract.DecodeFamilyDiscoveryRequestV4(body)
	}
	clear(body)
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
	if err == nil {
		err = request.Context().Err()
	}
	if err == nil && !h.validAuthentication(authentication) {
		err = domain.ErrAuth
	}
	if err != nil {
		fail(brokerFailureReason(err, domain.BrokerReasonAuthorizationUnavailable), nonce)
		return
	}
	if h.attachments == nil {
		fail(domain.BrokerReasonUnsupported, nonce)
		return
	}
	deadline := authentication.ReleaseDeadline
	if request.URL.Path == brokertransport.DiscoveryNegotiatePathV4 {
		discovery, err = brokertransport.BindDiscoveryNegotiationV4(hello, authentication.Context, h.now())
		if err == nil {
			body, err = brokertransport.EncodeNegotiatedDiscoveryV4(discovery)
		}
	} else {
		var projection domain.BrokerFamilyDiscoveryProjectionV4
		bounded, cancel := context.WithDeadline(request.Context(), authentication.ReleaseDeadline)
		projection, deadline, err = h.attachments.DiscoverExecutionV3(bounded, discovery, authentication.Context)
		cancel()
		if authentication.ReleaseDeadline.Before(deadline) {
			deadline = authentication.ReleaseDeadline
		}
		if err == nil {
			body, err = brokercontract.EncodeFamilyDiscoveryProjectionV4(projection)
		}
	}
	defer clear(body)
	if err != nil {
		fail(brokerFailureReason(err, domain.BrokerReasonAuthorizationUnavailable), nonce)
		return
	}
	if h.guard.Check(app.BrokerExactReadResult{}, body, credential) != nil {
		fail(domain.BrokerReasonAuthorizationUnavailable, nonce)
		return
	}
	published, err := h.publishBounded(request, writer, body, deadline, nonce)
	if err != nil && !published {
		fail(domain.BrokerReasonDecisionExpired, nonce)
	}
}
