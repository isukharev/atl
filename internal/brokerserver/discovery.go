package brokerserver

import (
	"net/http"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func (h *Handler) serveDiscovery(writer http.ResponseWriter, request *http.Request) {
	credential, credentialErr := workloadBearer(request)
	defer clear(credential)
	fail := func(reason domain.BrokerReason, correlation string) {
		body, err := brokertransport.EncodeDiscoveryFailureV2(reason)
		if err == nil {
			h.writeFailureBody(writer, reason, body, credential, correlation)
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
	body, err := readRequestBody(request, brokertransport.MaxDiscoveryNegotiationBytesV2)
	if err != nil {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
	var hello brokertransport.DiscoveryNegotiationV2
	var discovery domain.BrokerDiscoveryRequestV2
	if request.URL.Path == brokertransport.DiscoveryNegotiatePathV2 {
		hello, err = brokertransport.DecodeDiscoveryNegotiationV2(body)
	} else {
		discovery, err = brokercontract.DecodeDiscoveryRequestV2(body)
	}
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
	deadline := authentication.ReleaseDeadline
	if request.URL.Path == brokertransport.DiscoveryNegotiatePathV2 {
		discovery, err = brokertransport.BindDiscoveryNegotiationV2(hello, authentication.Context, h.now())
		if err == nil {
			body, err = brokertransport.EncodeNegotiatedDiscoveryV2(discovery)
		}
	} else {
		var projection domain.BrokerDiscoveryProjectionV2
		projection, deadline, err = h.reads.Discover(request.Context(), discovery, authentication.Context)
		if authentication.ReleaseDeadline.Before(deadline) {
			deadline = authentication.ReleaseDeadline
		}
		if err == nil {
			body, err = brokercontract.EncodeDiscoveryProjectionV2(projection)
		}
	}
	if err != nil {
		fail(brokerFailureReason(err, domain.BrokerReasonAuthorizationUnavailable), nonce)
		return
	}
	if h.guard.Check(app.BrokerExactReadResult{}, body, credential) != nil {
		fail(domain.BrokerReasonAuthorizationUnavailable, nonce)
		return
	}
	started, err := h.publish(request.Context(), writer, body, deadline, nonce)
	if err != nil && !started {
		fail(domain.BrokerReasonDecisionExpired, nonce)
	}
}
