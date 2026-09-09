package brokerserver

import (
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func (h *Handler) serveProjectPage(writer http.ResponseWriter, request *http.Request, started time.Time) {
	request, finish, err := beginBoundedRoute(writer, request, started, time.Duration(domain.BrokerMaxOperationMillis)*time.Millisecond)
	if err != nil {
		return
	}
	defer finish()
	credential, credentialErr := workloadBearer(request)
	defer clear(credential)
	fail := func(reason domain.BrokerReason, correlation string) {
		body, err := brokertransport.EncodeExecutionFailureV2(reason)
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
	body, err := readRequestBody(request, brokercontract.MaxProjectPageRequestBytesV2)
	if err != nil {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
	operation, err := brokercontract.DecodeProjectPageRequestV2(body)
	if err != nil {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
	recordAuditOperation(writer, operation.Operation)
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
	result, err := h.projectPages.Execute(request.Context(), operation, authentication.Context)
	if err != nil {
		fail(brokerFailureReason(err, domain.BrokerReasonAuthorizationUnavailable), nonce)
		return
	}
	if result.Page == nil {
		fail(domain.BrokerReasonAuthorizationUnavailable, nonce)
		return
	}
	body, err = brokercontract.EncodeJiraProjectPageResultV2(*result.Page)
	if err != nil || h.guard.Check(app.BrokerExactReadResult{}, body, credential) != nil {
		fail(domain.BrokerReasonAuthorizationUnavailable, nonce)
		return
	}
	deadline := result.ReleaseDeadline
	if authentication.ReleaseDeadline.Before(deadline) {
		deadline = authentication.ReleaseDeadline
	}
	published, err := h.publishBounded(request, writer, body, deadline, nonce)
	if err != nil && !published {
		fail(domain.BrokerReasonDecisionExpired, nonce)
	}
}
