package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type BrokerJiraCommentResult struct {
	Comment         domain.BrokerJiraCommentResult
	ReleaseDeadline time.Time
}

// BrokerJiraCommentService is an injectable app core. No runtime route creates
// it yet, and registry availability remains false.
type BrokerJiraCommentService struct {
	authorizer domain.BrokerAuthorizer
	journal    domain.BrokerJournal
	jira       domain.BrokerJiraGuardedCommentPort
	preflight  domain.WritePreflightAuthorizer
	backend    domain.BrokerBackendBinding
	now        func() time.Time
}

func NewBrokerJiraCommentService(authorizer domain.BrokerAuthorizer, journal domain.BrokerJournal, jira domain.BrokerJiraGuardedCommentPort, preflight domain.WritePreflightAuthorizer, backend domain.BrokerBackendBinding) (*BrokerJiraCommentService, error) {
	if authorizer == nil || journal == nil || jira == nil || preflight == nil || backend.Service != "jira" {
		return nil, fmt.Errorf("%w: broker Jira comment dependencies are incomplete", domain.ErrUsage)
	}
	if _, err := brokercontract.BrokerJournalBackendSHA256V1(backend); err != nil {
		return nil, fmt.Errorf("%w: broker Jira comment backend is invalid", domain.ErrUsage)
	}
	return &BrokerJiraCommentService{authorizer: authorizer, journal: journal, jira: jira, preflight: preflight, backend: backend, now: time.Now}, nil
}

type brokerJiraCommentPrepared struct {
	request               domain.BrokerRequest
	execution             *brokerJiraCommentExecution
	admission             domain.BrokerAdmissionRequest
	qualification         domain.BrokerQualificationRequest
	qualificationDecision domain.BrokerQualificationDecision
	operation             domain.BrokerOperationAuthorizationRequest
	decision              domain.BrokerOperationDecision
	decisionDeadline      int64
	identity              domain.BrokerJiraIssueIdentity
	resource              domain.BrokerQualifiedResource
	effectSHA256          string
	snapshot              *jiraGuardedCommentSnapshot
	options               JiraCommentAddOpts
	owner                 domain.BrokerJournalOwner
	executionSHA256       string
	authoritySHA256       string
}

func (s *BrokerJiraCommentService) Execute(ctx context.Context, request domain.BrokerRequest, verified domain.BrokerVerifiedContext) (BrokerJiraCommentResult, error) {
	if s == nil || s.authorizer == nil || s.journal == nil || s.jira == nil || s.preflight == nil || s.now == nil || ctx == nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	request, definition, err := prepareBrokerJiraCommentRequest(request)
	if err != nil {
		return BrokerJiraCommentResult{}, err
	}
	startedAt := s.now()
	deadlineMillis := brokerReadDeadlineMillis(startedAt.UnixMilli(), verified, definition.Limits.MaxOperationMillis)
	if verified.Backend != s.backend {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	origin, err := s.jira.BrokerOriginSHA256()
	if err != nil || origin != s.backend.OriginSHA256 {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	if err := brokercontract.MatchRequestContextV1(request, verified, startedAt.UnixMilli(), deadlineMillis); err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	execution, cancel, err := newBrokerJiraCommentExecution(ctx, definition, domain.ReadBudgetFromContext(ctx), s.now, startedAt, time.UnixMilli(deadlineMillis))
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	defer cancel()
	prepared, err := s.prepare(execution, request, definition, verified)
	if err != nil {
		return BrokerJiraCommentResult{}, err
	}
	if request.Operation == domain.BrokerOperationJiraCommentPreview {
		return s.preview(prepared, verified)
	}
	return s.apply(prepared, verified)
}

func prepareBrokerJiraCommentRequest(request domain.BrokerRequest) (domain.BrokerRequest, domain.BrokerOperationDefinition, error) {
	wire, err := brokercontract.EncodeRequestV1(request)
	if err != nil {
		return domain.BrokerRequest{}, domain.BrokerOperationDefinition{}, brokerJiraCommentError(err)
	}
	request, err = brokercontract.DecodeRequestV1(wire)
	if err != nil {
		return domain.BrokerRequest{}, domain.BrokerOperationDefinition{}, brokerJiraCommentError(err)
	}
	definition, ok := brokercontract.Definition(request.Operation, request.OperationVersion)
	if !ok || brokercontract.ValidateBrokerJiraCommentDefinitionV1(definition, request.Operation) != nil {
		return domain.BrokerRequest{}, domain.BrokerOperationDefinition{}, brokerJiraCommentError(domain.ErrUsage)
	}
	body, err := ValidateJiraCommentBody(request.Arguments.JiraComment.NativeBody)
	if err != nil {
		return domain.BrokerRequest{}, domain.BrokerOperationDefinition{}, brokerJiraCommentError(err)
	}
	request.Arguments.JiraComment.NativeBody = body
	return request, definition, nil
}

func (s *BrokerJiraCommentService) prepare(execution *brokerJiraCommentExecution, request domain.BrokerRequest, definition domain.BrokerOperationDefinition, verified domain.BrokerVerifiedContext) (*brokerJiraCommentPrepared, error) {
	argumentsSHA256, err := brokercontract.ArgumentsSHA256(request)
	if err != nil {
		return nil, brokerJiraCommentError(err)
	}
	admission := domain.BrokerAdmissionRequest{Context: verified, Operation: request.Operation, OperationVersion: request.OperationVersion, RequestID: request.RequestID, Features: append([]string{}, request.Features...), Arguments: request.Arguments, ArgumentsSHA256: argumentsSHA256, DeadlineMillis: execution.deadline.UnixMilli()}
	admissionStarted := execution.currentMillis()
	admissionDecision, err := s.authorizer.Admit(execution.base, admission)
	validationErr := brokercontract.ValidateAdmissionDecisionForV1(admissionDecision, admission, execution.currentMillis())
	if err != nil || execution.base.Err() != nil || validationErr != nil {
		return nil, brokerJiraCommentError(firstBrokerReadError(err, firstBrokerReadError(execution.base.Err(), validationErr)))
	}
	qualification := domain.BrokerQualificationRequest{Admission: admission, AdmissionDecision: admissionDecision, Plan: domain.BrokerQualificationPlan{SelectorSHA256: argumentsSHA256, MetadataFields: append([]string{}, definition.QualificationFields...), Limits: definition.Limits.Qualification}}
	authorityCtx, authorityCancel, err := execution.decisionContext(brokerLocalDecisionDeadline(admissionDecision.BrokerDecisionCore, admissionStarted))
	if err != nil {
		return nil, brokerJiraCommentError(err)
	}
	qualificationStarted := execution.currentMillis()
	qualificationDecision, err := s.authorizer.AuthorizeQualification(authorityCtx, qualification)
	contextErr := authorityCtx.Err()
	authorityCancel()
	validationErr = brokercontract.ValidateQualificationDecisionForV1(qualificationDecision, qualification, execution.currentMillis())
	if err != nil || contextErr != nil || validationErr != nil {
		return nil, brokerJiraCommentError(firstBrokerReadError(err, firstBrokerReadError(contextErr, validationErr)))
	}
	qualificationDeadline := brokerLocalDecisionDeadline(qualificationDecision.BrokerDecisionCore, qualificationStarted)
	phaseCtx, phaseCancel, err := execution.qualificationContext(qualificationDeadline)
	if err != nil {
		return nil, brokerJiraCommentError(err)
	}
	identity, readErr := s.jira.QualifyBrokerIssue(phaseCtx, request.Arguments.JiraComment.IssueKey)
	contextErr = phaseCtx.Err()
	phaseCancel()
	if readErr != nil || contextErr != nil {
		return nil, brokerJiraCommentError(firstBrokerReadError(readErr, contextErr))
	}
	if err := brokercontract.ValidateQualificationDecisionForV1(qualificationDecision, qualification, execution.currentMillis()); err != nil {
		return nil, brokerJiraCommentError(err)
	}
	if err := execution.decisionError(qualificationDeadline); err != nil {
		return nil, brokerJiraCommentError(err)
	}
	if identity.Key != request.Arguments.JiraComment.IssueKey {
		return nil, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	resource, err := brokercontract.BrokerJiraCommentResourceV1(identity)
	if err != nil {
		return nil, brokerJiraCommentError(err)
	}
	apply := request.Operation == domain.BrokerOperationJiraCommentApply
	effects, effectSHA256, err := brokercontract.BrokerJiraCommentEffectsV1(resource, apply)
	if err != nil {
		return nil, brokerJiraCommentError(err)
	}
	operation := domain.BrokerOperationAuthorizationRequest{QualificationRequest: qualification, QualificationDecision: qualificationDecision, QualifiedResources: []domain.BrokerQualifiedResource{resource}, Effects: effects}
	authorityCtx, authorityCancel, err = execution.decisionContext(qualificationDeadline)
	if err != nil {
		return nil, brokerJiraCommentError(err)
	}
	operationStarted := execution.currentMillis()
	decision, err := s.authorizer.AuthorizeOperation(authorityCtx, operation)
	contextErr = authorityCtx.Err()
	authorityCancel()
	validationErr = brokercontract.ValidateOperationDecisionForV1(decision, operation, execution.currentMillis())
	if err != nil || contextErr != nil || validationErr != nil {
		return nil, brokerJiraCommentError(firstBrokerReadError(err, firstBrokerReadError(contextErr, validationErr)))
	}
	decisionDeadline := brokerLocalDecisionDeadline(decision.BrokerDecisionCore, operationStarted)
	options := JiraCommentAddOpts{Body: append([]byte(nil), request.Arguments.JiraComment.NativeBody...), Apply: apply, ExpectedProposalHash: request.Arguments.JiraComment.ExpectedProposalHash, SatisfactionPolicy: request.Arguments.JiraComment.SatisfactionPolicy}
	phaseCtx, phaseCancel, err = execution.businessContext(decisionDeadline)
	if err != nil {
		return nil, brokerJiraCommentError(err)
	}
	snapshot, readErr := buildGuardedCommentSnapshotFromQualifiedIssue(phaseCtx, s.jira, backendid.Prefix+s.backend.OriginSHA256, identity, identity.Key, options)
	contextErr = phaseCtx.Err()
	phaseCancel()
	if readErr != nil || contextErr != nil {
		return nil, brokerJiraCommentError(firstBrokerReadError(readErr, contextErr))
	}
	if err := brokercontract.ValidateOperationDecisionForV1(decision, operation, execution.currentMillis()); err != nil {
		return nil, brokerJiraCommentError(err)
	}
	if err := execution.decisionError(decisionDeadline); err != nil {
		return nil, brokerJiraCommentError(err)
	}
	owner, err := brokercontract.BrokerJournalOwnerV1(verified)
	if err != nil {
		return nil, brokerJiraCommentError(err)
	}
	executionSHA256, authoritySHA256, err := brokercontract.BrokerJournalWriterSHA256V1(verified)
	if err != nil {
		return nil, brokerJiraCommentError(err)
	}
	return &brokerJiraCommentPrepared{
		request: request, execution: execution, admission: admission,
		qualification: qualification, qualificationDecision: qualificationDecision,
		operation: operation, decision: decision, decisionDeadline: decisionDeadline, identity: identity, resource: resource,
		effectSHA256: effectSHA256, snapshot: snapshot, options: options, owner: owner,
		executionSHA256: executionSHA256, authoritySHA256: authoritySHA256,
	}, nil
}

func brokerJiraCommentError(err error) error {
	if ok, safe := brokercontract.ContentFreeError(err); ok {
		return fmt.Errorf("broker Jira comment failed: %w", safe)
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
	case errors.Is(err, domain.ErrReadAttemptBudgetExhausted):
		sentinel = domain.ErrReadAttemptBudgetExhausted
	case errors.Is(err, domain.ErrReadResponseBudgetExhausted):
		sentinel = domain.ErrReadResponseBudgetExhausted
	}
	return fmt.Errorf("broker Jira comment failed: %w", sentinel)
}
