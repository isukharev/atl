package brokerserver

import (
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func (h *Handler) serveFamilyDiscovery(writer http.ResponseWriter, request *http.Request, started time.Time) {
	request, finish, err := beginBoundedRoute(writer, request, started, time.Duration(domain.BrokerMaxDecisionLeaseMillis)*time.Millisecond)
	if err != nil {
		return
	}
	defer finish()
	credential, credentialErr := workloadBearer(request)
	defer clear(credential)
	fail := func(reason domain.BrokerReason, correlation string) {
		body, err := brokertransport.EncodeDiscoveryFailureV3(reason)
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
	body, err := readRequestBody(request, brokertransport.MaxDiscoveryNegotiationBytesV3)
	if err != nil {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
	var hello brokertransport.DiscoveryNegotiationV3
	var discovery domain.BrokerFamilyDiscoveryRequestV3
	if request.URL.Path == brokertransport.DiscoveryNegotiatePathV3 {
		hello, err = brokertransport.DecodeDiscoveryNegotiationV3(body)
	} else {
		discovery, err = brokercontract.DecodeFamilyDiscoveryRequestV3(body)
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
	if h.projectPages == nil {
		fail(domain.BrokerReasonUnsupported, nonce)
		return
	}
	deadline := authentication.ReleaseDeadline
	if request.URL.Path == brokertransport.DiscoveryNegotiatePathV3 {
		discovery, err = brokertransport.BindDiscoveryNegotiationV3(hello, authentication.Context, h.now())
		if err == nil {
			body, err = brokertransport.EncodeNegotiatedDiscoveryV3(discovery)
		}
	} else {
		var projection domain.BrokerFamilyDiscoveryProjectionV3
		projection, deadline, err = h.projectPages.DiscoverExecutionV2(request.Context(), discovery, authentication.Context)
		if authentication.ReleaseDeadline.Before(deadline) {
			deadline = authentication.ReleaseDeadline
		}
		if err == nil {
			body, err = brokercontract.EncodeFamilyDiscoveryProjectionV3(projection)
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
	published, err := h.publishBounded(request, writer, body, deadline, nonce)
	if err != nil && !published {
		fail(domain.BrokerReasonDecisionExpired, nonce)
	}
}
