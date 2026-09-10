package brokerserver

import (
	"bytes"
	"net/http"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func (h *Handler) serveAttachment(writer http.ResponseWriter, request *http.Request, started time.Time) {
	request, finish, err := beginBoundedRoute(writer, request, started, time.Duration(domain.BrokerMaxOperationMillis)*time.Millisecond)
	if err != nil {
		return
	}
	defer finish()
	credential, credentialErr := workloadBearer(request)
	defer clear(credential)
	fail := func(reason domain.BrokerReason, correlation string) {
		h.failAttachment(writer, reason, credential, correlation, false)
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
	select {
	case h.attachmentPermits <- struct{}{}:
		defer func() { <-h.attachmentPermits }()
	default:
		fail(domain.BrokerReasonAuthorizationUnavailable, "")
		return
	}
	if credentialErr != nil {
		fail(domain.BrokerReasonCredentialExpired, "")
		return
	}
	recordAuditCredential(writer, credential)
	body, err := readRequestBody(request, brokercontract.MaxAttachmentRequestBytesV3)
	if err != nil {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
	operation, err := brokercontract.DecodeAttachmentRequestV3(body)
	clear(body)
	if err != nil {
		fail(domain.BrokerReasonMalformed, "")
		return
	}
	recordAuditOperation(writer, operation.Operation)
	definition, ok := brokercontract.DefinitionV3(operation.Operation, operation.OperationVersion)
	if !ok || !definition.Definition.Available || h.attachments == nil || h.attachmentAuthenticator == nil {
		fail(domain.BrokerReasonUnsupported, "")
		return
	}
	budgets, err := app.NewBrokerAttachmentExecutionBudgets()
	if err != nil {
		fail(domain.BrokerReasonAuthorizationUnavailable, "")
		return
	}
	nonce, err := h.nonce()
	if err != nil {
		fail(domain.BrokerReasonAuthorizationUnavailable, "")
		return
	}
	recordAuditCorrelation(writer, nonce)
	authentication, err := h.authenticateAttachment(request, credential, nonce, budgets)
	if err != nil {
		fail(brokerFailureReason(err, domain.BrokerReasonAuthorizationUnavailable), nonce)
		return
	}
	h.executeAttachment(writer, request, operation, authentication, credential, nonce, budgets)
}

func (h *Handler) authenticateAttachment(request *http.Request, credential []byte, nonce string, budgets *app.BrokerAttachmentExecutionBudgets) (brokertransport.Authentication, error) {
	ctx, err := budgets.AuthenticationContext(request.Context())
	if err != nil {
		return brokertransport.Authentication{}, err
	}
	authentication, err := h.attachmentAuthenticator.AuthenticateAttachmentV3(ctx, credential, brokertransport.AuthenticationChallenge{Nonce: nonce, Audience: h.config.Audience, BrokerID: h.config.BrokerID})
	if err != nil {
		return brokertransport.Authentication{}, err
	}
	if ctx.Err() != nil {
		return brokertransport.Authentication{}, ctx.Err()
	}
	if !h.validAuthentication(authentication) {
		return brokertransport.Authentication{}, domain.ErrAuth
	}
	return authentication, nil
}

func (h *Handler) executeAttachment(writer http.ResponseWriter, request *http.Request, operation domain.BrokerAttachmentRequestV3, initial brokertransport.Authentication, credential []byte, correlation string, budgets *app.BrokerAttachmentExecutionBudgets) {
	scanner, err := h.guard.NewAttachmentCredentialScanner(credential)
	if err != nil {
		h.failAttachment(writer, domain.BrokerReasonAuthorizationUnavailable, credential, correlation, false)
		return
	}
	defer scanner.Close()
	tailBytes, err := scanner.LookaheadBytes()
	if err != nil {
		h.failAttachment(writer, domain.BrokerReasonAuthorizationUnavailable, credential, correlation, false)
		return
	}
	state, err := h.attachments.Start(request.Context(), app.BrokerAttachmentStreamStart{
		Request: operation, Verified: initial.Context, InitialAuthenticationDeadline: initial.ReleaseDeadline,
		StreamID: correlation, CorrelationID: correlation, ScannerTailBytes: tailBytes, Budgets: budgets,
	})
	if err != nil {
		h.failAttachment(writer, brokerFailureReason(err, domain.BrokerReasonAuthorizationUnavailable), credential, correlation, false)
		return
	}
	// The completed path checks closure before its success audit; this fallback
	// preserves the primary failure on all earlier exits.
	defer func() { _ = state.Close() }()
	publisher := &attachmentPublisher{ctx: request.Context(), writer: writer, scanner: scanner, correlation: correlation, now: h.now}
	fail := func(err error) {
		h.failAttachment(writer, brokerFailureReason(err, domain.BrokerReasonAuthorizationUnavailable), credential, correlation, publisher.started)
	}
	var tail, candidateBytes []byte
	defer func() {
		clear(tail)
		clear(candidateBytes)
	}()
	for {
		candidate, err := state.ReadCandidate(tail)
		candidateBytes = candidate.Bytes
		clear(tail)
		tail = nil
		if err != nil {
			fail(err)
			return
		}
		if err := scanner.CheckNativeWindow(candidate.Bytes); err != nil {
			fail(err)
			return
		}
		nonce, err := h.nonce()
		if err != nil {
			fail(domain.ErrCheckFailed)
			return
		}
		fresh, err := h.authenticateAttachment(request, credential, nonce, budgets)
		if err != nil {
			fail(err)
			return
		}
		payloadBytes := min(len(candidate.Bytes), int(brokercontract.MaxAttachmentDecodedFrameBytesV3))
		release, err := state.AuthorizeRelease(app.BrokerAttachmentFreshAuthorization{Context: fresh.Context, ReleaseDeadline: fresh.ReleaseDeadline}, app.BrokerAttachmentReleaseSelection{
			CandidateID: candidate.ID, Payload: candidate.Bytes[:payloadBytes], RetainedTail: candidate.Bytes[payloadBytes:],
		})
		if err != nil {
			fail(err)
			return
		}
		hashes, err := publisher.publish(release.Lines, release.FlushNotAfter)
		if err != nil {
			fail(err)
			return
		}
		complete, err := state.CommitRelease(release.CandidateID, hashes)
		if err != nil {
			fail(err)
			return
		}
		if complete {
			if err := state.Close(); err != nil {
				fail(err)
				return
			}
			recordAuditAttachmentComplete(writer)
			return
		}
		tail = bytes.Clone(candidate.Bytes[payloadBytes:])
		clear(candidateBytes)
		candidateBytes = nil
	}
}

func (h *Handler) failAttachment(writer http.ResponseWriter, reason domain.BrokerReason, credential []byte, correlation string, published bool) {
	if published {
		recordAuditReason(writer, reason)
		// Prevent net/http's final implicit flush from publishing buffered bytes
		// after the failed release. Already delivered bytes cannot be retracted.
		_ = http.NewResponseController(writer).SetWriteDeadline(time.Now())
		return
	}
	body, err := brokertransport.EncodeExecutionFailureV3(reason)
	if err == nil {
		h.writeFailureBody(writer, reason, body, credential, correlation)
		_ = http.NewResponseController(writer).Flush()
	}
}
