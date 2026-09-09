package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type BrokerOperationObservationResult struct {
	Outcome         domain.BrokerOperationOutcome
	ReleaseDeadline time.Time
}

// BrokerOperationObservationService exposes only authorized durable metadata.
// It has no backend or recovery-artifact capability by construction.
type BrokerOperationObservationService struct {
	authorizer domain.BrokerAuthorizer
	journal    domain.BrokerJournalLookup
	backend    domain.BrokerBackendBinding
	now        func() time.Time
}

func NewBrokerOperationObservationService(authorizer domain.BrokerAuthorizer, journal domain.BrokerJournalLookup, backend domain.BrokerBackendBinding) (*BrokerOperationObservationService, error) {
	if authorizer == nil || journal == nil || backend.Service != "jira" {
		return nil, fmt.Errorf("%w: broker operation observation dependencies are incomplete", domain.ErrUsage)
	}
	if _, err := brokercontract.BrokerJournalBackendSHA256V1(backend); err != nil {
		return nil, fmt.Errorf("%w: broker operation observation backend is invalid", domain.ErrUsage)
	}
	return &BrokerOperationObservationService{authorizer: authorizer, journal: journal, backend: backend, now: time.Now}, nil
}

func (s *BrokerOperationObservationService) Observe(ctx context.Context, request domain.BrokerRequest, verified domain.BrokerVerifiedContext) (BrokerOperationObservationResult, error) {
	if s == nil || s.authorizer == nil || s.journal == nil || s.now == nil || ctx == nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(domain.ErrCheckFailed)
	}
	request, definition, err := prepareBrokerOperationObservationRequest(request)
	if err != nil {
		return BrokerOperationObservationResult{}, err
	}
	startedAt := s.now()
	clock := brokerOperationObservationClock{now: s.now, startedAt: startedAt, lastMillis: startedAt.UnixMilli()}
	nowMillis := clock.currentMillis()
	if verified.Backend != s.backend {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(domain.ErrCheckFailed)
	}
	deadlineMillis := min(nowMillis+definition.Limits.MaxOperationMillis, verified.ExecutionExpiresMillis, verified.GrantExpiresMillis, verified.CredentialExpiresMillis)
	if err := brokercontract.MatchRequestContextV1(request, verified, nowMillis, deadlineMillis); err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(deadlineMillis-nowMillis)*time.Millisecond)
	defer cancel()

	argumentsSHA256, err := brokercontract.ArgumentsSHA256(request)
	if err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	admission := domain.BrokerAdmissionRequest{
		Context: verified, Operation: request.Operation, OperationVersion: request.OperationVersion,
		RequestID: request.RequestID, Features: append([]string{}, request.Features...),
		Arguments: request.Arguments, ArgumentsSHA256: argumentsSHA256, DeadlineMillis: deadlineMillis,
	}
	admissionStarted := clock.currentMillis()
	admissionDecision, callErr := s.authorizer.Admit(bounded, admission)
	if callErr == nil {
		callErr = bounded.Err()
	}
	validationErr := brokercontract.ValidateAdmissionDecisionForV1(admissionDecision, admission, clock.currentMillis())
	if callErr != nil || validationErr != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(firstBrokerReadError(callErr, validationErr))
	}
	admissionDeadline := brokerLocalDecisionDeadline(admissionDecision.BrokerDecisionCore, admissionStarted)
	qualificationRemainingMillis := admissionDeadline - clock.currentMillis()
	if qualificationRemainingMillis <= 0 {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(context.DeadlineExceeded)
	}

	qualification := domain.BrokerQualificationRequest{Admission: admission, AdmissionDecision: admissionDecision, Plan: domain.BrokerQualificationPlan{
		SelectorSHA256: argumentsSHA256, MetadataFields: append([]string{}, definition.QualificationFields...), Limits: definition.Limits.Qualification,
	}}
	qualificationContext, qualificationCancel := context.WithTimeout(bounded, time.Duration(qualificationRemainingMillis)*time.Millisecond)
	qualificationStarted := clock.currentMillis()
	qualificationDecision, callErr := s.authorizer.AuthorizeQualification(qualificationContext, qualification)
	if callErr == nil {
		callErr = qualificationContext.Err()
	}
	qualificationCancel()
	validationErr = brokercontract.ValidateQualificationDecisionForV1(qualificationDecision, qualification, clock.currentMillis())
	if callErr != nil || validationErr != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(firstBrokerReadError(callErr, validationErr))
	}
	qualificationDeadline := brokerLocalDecisionDeadline(qualificationDecision.BrokerDecisionCore, qualificationStarted)

	ticketID := request.Arguments.Outcome.OperationTicket
	resource, err := brokercontract.BrokerOperationObservationResourceV1(ticketID)
	if err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	operation := domain.BrokerOperationAuthorizationRequest{
		QualificationRequest: qualification, QualificationDecision: qualificationDecision,
		QualifiedResources: []domain.BrokerQualifiedResource{resource},
		Effects:            []domain.BrokerEffect{{Kind: domain.BrokerEffectObserve, Resource: resource, Fields: []string{}}},
	}
	operationRemainingMillis := qualificationDeadline - clock.currentMillis()
	if operationRemainingMillis <= 0 {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(context.DeadlineExceeded)
	}
	operationContext, operationCancel := context.WithTimeout(bounded, time.Duration(operationRemainingMillis)*time.Millisecond)
	operationStarted := clock.currentMillis()
	operationDecision, callErr := s.authorizer.AuthorizeOperation(operationContext, operation)
	if callErr == nil {
		callErr = operationContext.Err()
	}
	operationCancel()
	validationErr = brokercontract.ValidateOperationDecisionForV1(operationDecision, operation, clock.currentMillis())
	if callErr != nil || validationErr != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(firstBrokerReadError(callErr, validationErr))
	}
	operationDeadline := brokerLocalDecisionDeadline(operationDecision.BrokerDecisionCore, operationStarted)

	owner, err := brokercontract.BrokerJournalOwnerV1(verified)
	if err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	lookupAtMillis := clock.currentMillis()
	lookupRemainingMillis := min(deadlineMillis, operationDeadline) - lookupAtMillis
	if lookupRemainingMillis <= 0 {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(context.DeadlineExceeded)
	}
	lookupContext, lookupCancel := context.WithTimeout(bounded, time.Duration(lookupRemainingMillis)*time.Millisecond)
	record, lookupErr := s.journal.Lookup(lookupContext, owner, ticketID)
	lookupContextErr := lookupContext.Err()
	lookupCancel()
	if lookupContextErr != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(lookupContextErr)
	}
	if lookupErr != nil {
		if errors.Is(lookupErr, domain.ErrForbidden) {
			return BrokerOperationObservationResult{}, brokerOperationObservationDenied()
		}
		return BrokerOperationObservationResult{}, brokerOperationObservationError(lookupErr)
	}
	if err := bounded.Err(); err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	if record.Reservation.Owner != owner || record.BindingSHA256 == "" || record.Phase == "" {
		return BrokerOperationObservationResult{}, brokerOperationObservationDenied()
	}
	if err := validateBrokerOperationObservationRecord(record, ticketID); err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}

	observedAtMillis := clock.currentMillis()
	if observedAtMillis >= record.Reservation.ObservationUntilMillis {
		return BrokerOperationObservationResult{}, brokerOperationObservationDenied()
	}
	if err := brokercontract.MatchRequestContextV1(request, verified, observedAtMillis, deadlineMillis); err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	if err := brokercontract.ValidateOperationDecisionForV1(operationDecision, operation, observedAtMillis); err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	if observedAtMillis >= operationDeadline {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(context.DeadlineExceeded)
	}
	if err := bounded.Err(); err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}

	ticketSHA256, err := brokercontract.OperationTicketSHA256(record.Intent.Ticket)
	if err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(domain.ErrCheckFailed)
	}
	outcome, err := brokerOperationOutcomeFromRecord(record, ticketSHA256, observedAtMillis)
	if err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	if _, err := brokercontract.EncodeOperationOutcomeV1(outcome); err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(domain.ErrCheckFailed)
	}
	releaseMillis := min(deadlineMillis, operationDeadline, record.Reservation.ObservationUntilMillis)
	releaseCheckMillis := clock.currentMillis()
	if err := bounded.Err(); err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	if err := brokercontract.MatchRequestContextV1(request, verified, releaseCheckMillis, deadlineMillis); err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	if err := brokercontract.ValidateOperationDecisionForV1(operationDecision, operation, releaseCheckMillis); err != nil {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(err)
	}
	if releaseCheckMillis >= releaseMillis {
		return BrokerOperationObservationResult{}, brokerOperationObservationError(context.DeadlineExceeded)
	}
	releaseDeadline := startedAt.Add(time.Duration(releaseMillis-startedAt.UnixMilli()) * time.Millisecond)
	return BrokerOperationObservationResult{Outcome: outcome, ReleaseDeadline: releaseDeadline}, nil
}

func prepareBrokerOperationObservationRequest(request domain.BrokerRequest) (domain.BrokerRequest, domain.BrokerOperationDefinition, error) {
	wire, err := brokercontract.EncodeRequestV1(request)
	if err != nil {
		return domain.BrokerRequest{}, domain.BrokerOperationDefinition{}, brokerOperationObservationError(err)
	}
	request, err = brokercontract.DecodeRequestV1(wire)
	if err != nil {
		return domain.BrokerRequest{}, domain.BrokerOperationDefinition{}, brokerOperationObservationError(err)
	}
	definition, ok := brokercontract.Definition(request.Operation, request.OperationVersion)
	if !ok || request.Operation != domain.BrokerOperationOutcomeLookup {
		return domain.BrokerRequest{}, domain.BrokerOperationDefinition{}, brokerOperationObservationError(domain.ErrUsage)
	}
	return request, definition, nil
}

func validateBrokerOperationObservationRecord(record domain.BrokerJournalRecord, ticketID string) error {
	reservation := record.Reservation
	if record.OperationID != ticketID || record.Sequence == 0 ||
		!brokerObservationDigest(record.ArtifactSHA256) || record.ArtifactBytes < 1 || record.ArtifactBytes > reservation.ArtifactCapacity ||
		!brokerObservationDigest(reservation.ExecutionSHA256) || !brokerObservationDigest(reservation.AuthorityRevisionSHA256) ||
		record.IssuedAtMillis <= 0 || record.IssuedAtMillis < reservation.ExecutionNotBeforeMillis || record.AcceptUntilMillis <= record.IssuedAtMillis || record.AcceptUntilMillis-record.IssuedAtMillis > domain.BrokerMaxOperationMillis ||
		record.AcceptUntilMillis > reservation.ExecutionExpiresMillis || record.AcceptUntilMillis > reservation.GrantExpiresMillis || record.AcceptUntilMillis > reservation.CredentialExpiresMillis || record.AcceptUntilMillis > reservation.OperationDeadlineMillis ||
		reservation.OperationDeadlineMillis-record.IssuedAtMillis > domain.BrokerMaxOperationMillis || reservation.OperationDeadlineMillis > reservation.ExecutionExpiresMillis || reservation.OperationDeadlineMillis > reservation.GrantExpiresMillis || reservation.OperationDeadlineMillis > reservation.CredentialExpiresMillis ||
		reservation.ObservationUntilMillis < reservation.OperationDeadlineMillis || record.RecordedAtMillis < record.IssuedAtMillis {
		return domain.ErrCheckFailed
	}
	bindingSHA256, err := brokercontract.BrokerJournalIntentBindingSHA256V1(record, record.Intent)
	if err != nil || bindingSHA256 != record.BindingSHA256 {
		return domain.ErrCheckFailed
	}
	switch record.Phase {
	case domain.BrokerOperationAdmitted:
		if record.DispatchClaimed || record.ResultSHA256 != "" {
			return domain.ErrCheckFailed
		}
	case domain.BrokerOperationDispatching, domain.BrokerOperationOutcomeUnknown:
		if !record.DispatchClaimed || record.ResultSHA256 != "" {
			return domain.ErrCheckFailed
		}
	case domain.BrokerOperationApplied:
		if !record.DispatchClaimed || !brokerObservationDigest(record.ResultSHA256) {
			return domain.ErrCheckFailed
		}
	case domain.BrokerOperationNotApplied:
		if record.DispatchClaimed && !brokerObservationDigest(record.ResultSHA256) || record.ResultSHA256 != "" && !brokerObservationDigest(record.ResultSHA256) {
			return domain.ErrCheckFailed
		}
	default:
		return domain.ErrCheckFailed
	}
	return nil
}

func brokerOperationOutcomeFromRecord(record domain.BrokerJournalRecord, ticketSHA256 string, observedAtMillis int64) (domain.BrokerOperationOutcome, error) {
	outcome := domain.BrokerOperationOutcome{SchemaVersion: brokercontract.SchemaVersion, TicketSHA256: ticketSHA256, Phase: record.Phase, ObservedAtMillis: observedAtMillis, ResultSHA256: record.ResultSHA256}
	switch record.Phase {
	case domain.BrokerOperationAdmitted, domain.BrokerOperationDispatching, domain.BrokerOperationOutcomeUnknown:
	case domain.BrokerOperationApplied:
		// Applied is persisted only from a qualified result digest. This reports
		// stored reconciliation evidence; observation itself performs no readback.
		outcome.Complete, outcome.Reconciled = true, true
	case domain.BrokerOperationNotApplied:
		outcome.Complete = true
	default:
		return domain.BrokerOperationOutcome{}, domain.ErrCheckFailed
	}
	return outcome, nil
}

type brokerOperationObservationClock struct {
	now        func() time.Time
	startedAt  time.Time
	lastMillis int64
}

func (c *brokerOperationObservationClock) currentMillis() int64 {
	elapsed := c.now().Sub(c.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	c.lastMillis = max(c.lastMillis, c.startedAt.UnixMilli()+elapsed.Milliseconds())
	return c.lastMillis
}

func brokerObservationDigest(value string) bool {
	if len(value) != domain.BrokerMaxDigestBytes {
		return false
	}
	for _, current := range value {
		if current >= '0' && current <= '9' || current >= 'a' && current <= 'f' {
			continue
		}
		return false
	}
	return true
}

func brokerOperationObservationDenied() error {
	_, err := brokercontract.ErrorForReason(domain.BrokerReasonDenied)
	return brokerOperationObservationError(err)
}

func brokerOperationObservationError(err error) error {
	if ok, safe := brokercontract.ContentFreeError(err); ok {
		return fmt.Errorf("broker operation observation failed: %w", safe)
	}
	sentinel := domain.ErrCheckFailed
	switch {
	case errors.Is(err, context.Canceled):
		sentinel = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		sentinel = context.DeadlineExceeded
	case errors.Is(err, domain.ErrUsage):
		sentinel = domain.ErrUsage
	case errors.Is(err, domain.ErrAuth):
		sentinel = domain.ErrAuth
	case errors.Is(err, domain.ErrForbidden):
		sentinel = domain.ErrForbidden
	}
	return fmt.Errorf("broker operation observation failed: %w", sentinel)
}
