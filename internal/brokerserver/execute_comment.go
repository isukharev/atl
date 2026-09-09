package brokerserver

import (
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func (h *Handler) executeComment(writer http.ResponseWriter, request *http.Request, operation domain.BrokerRequest, verified domain.BrokerVerifiedContext, authenticationDeadline time.Time, credential []byte, correlation string) {
	if h.comments == nil {
		h.writeFailure(writer, domain.BrokerReasonUnsupported, credential, correlation)
		return
	}
	result, executeErr := h.comments.Execute(request.Context(), operation, verified)
	// A persisted negative/ambiguous result is useful even with a nonzero app
	// error. Never turn an error accompanying a positive result into success.
	if executeErr != nil && (operation.Operation != domain.BrokerOperationJiraCommentApply || result.Comment.Status != "not_applied" && result.Comment.Status != "outcome_unknown") {
		h.writeFailure(writer, brokerFailureReason(executeErr, domain.BrokerReasonAuthorizationUnavailable), credential, correlation)
		return
	}
	if brokercontract.ValidateJiraCommentResultForV1(result.Comment, operation) != nil {
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, correlation)
		return
	}
	body, err := brokercontract.EncodeJiraCommentResultV1(result.Comment)
	if err != nil {
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, correlation)
		return
	}
	h.publishCommentResult(writer, request, body, result.ReleaseDeadline, authenticationDeadline, credential, correlation)
}

func (h *Handler) executeOutcome(writer http.ResponseWriter, request *http.Request, operation domain.BrokerRequest, verified domain.BrokerVerifiedContext, authenticationDeadline time.Time, credential []byte, correlation string) {
	if h.outcomes == nil {
		h.writeFailure(writer, domain.BrokerReasonUnsupported, credential, correlation)
		return
	}
	result, err := h.outcomes.Observe(request.Context(), operation, verified)
	if err != nil {
		h.writeFailure(writer, brokerFailureReason(err, domain.BrokerReasonAuthorizationUnavailable), credential, correlation)
		return
	}
	body, err := brokercontract.EncodeOperationOutcomeV1(result.Outcome)
	if err != nil {
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, correlation)
		return
	}
	h.publishCommentResult(writer, request, body, result.ReleaseDeadline, authenticationDeadline, credential, correlation)
}

func (h *Handler) publishCommentResult(writer http.ResponseWriter, request *http.Request, body []byte, deadline, authenticationDeadline time.Time, credential []byte, correlation string) {
	if h.guard.Check(app.BrokerExactReadResult{}, body, credential) != nil {
		h.writeFailure(writer, domain.BrokerReasonAuthorizationUnavailable, credential, correlation)
		return
	}
	if authenticationDeadline.Before(deadline) {
		deadline = authenticationDeadline
	}
	started, err := h.publishBounded(request, writer, body, deadline, correlation)
	if err != nil && !started {
		h.writeFailure(writer, domain.BrokerReasonDecisionExpired, credential, correlation)
	}
}
