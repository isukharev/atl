package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type BrokerExactReadResult struct {
	JiraIssue       *domain.BrokerJiraIssueReadResult
	ConfluencePage  *domain.BrokerConfluencePageReadResult
	ReleaseDeadline time.Time
}

type BrokerReadService struct {
	authorizer domain.BrokerAuthorizer
	jira       BrokerJiraIssueReader
	confluence BrokerConfluencePageReader
	now        func() time.Time
}

// BrokerJiraIssueReader pairs one server-owned destination identity with the
// exact reader constructed for that destination.
type BrokerJiraIssueReader struct {
	Backend domain.BrokerBackendBinding
	Reader  domain.BrokerJiraIssueReadPort
}

type BrokerConfluencePageReader struct {
	Backend domain.BrokerBackendBinding
	Reader  domain.BrokerConfluencePageReadPort
}

func NewBrokerReadService(authorizer domain.BrokerAuthorizer, jira BrokerJiraIssueReader, confluence BrokerConfluencePageReader) (*BrokerReadService, error) {
	if authorizer == nil || !brokerReadBindingValid(jira.Backend, jira.Reader, "jira") || !brokerReadBindingValid(confluence.Backend, confluence.Reader, "confluence") || jira.Reader == nil && confluence.Reader == nil {
		return nil, fmt.Errorf("%w: broker exact-read dependencies are incomplete", domain.ErrUsage)
	}
	return &BrokerReadService{authorizer: authorizer, jira: jira, confluence: confluence, now: time.Now}, nil
}

func (s *BrokerReadService) Execute(ctx context.Context, request domain.BrokerRequest, verified domain.BrokerVerifiedContext) (BrokerExactReadResult, error) {
	if s == nil || s.authorizer == nil || s.now == nil {
		return BrokerExactReadResult{}, brokerReadError(domain.ErrCheckFailed)
	}
	request, definition, err := prepareBrokerReadRequest(request)
	if err != nil {
		return BrokerExactReadResult{}, err
	}
	startedAt := s.now()
	nowMillis := startedAt.UnixMilli()
	matched, err := s.backendMatches(request.Operation, verified.Backend)
	if err != nil || !matched {
		return BrokerExactReadResult{}, brokerReadError(domain.ErrCheckFailed)
	}
	deadlineMillis := brokerReadDeadlineMillis(nowMillis, verified, definition.Limits.MaxOperationMillis)
	if err := brokercontract.MatchRequestContextV1(request, verified, nowMillis, deadlineMillis); err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(deadlineMillis-nowMillis)*time.Millisecond)
	defer cancel()
	execution, err := newBrokerReadExecution(bounded, definition, domain.ReadBudgetFromContext(ctx), s.now, startedAt)
	if err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	argumentsSHA256, err := brokercontract.ArgumentsSHA256(request)
	if err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	admission := domain.BrokerAdmissionRequest{
		Context: verified, Operation: request.Operation, OperationVersion: request.OperationVersion, RequestID: request.RequestID,
		Features: append([]string{}, request.Features...), Arguments: request.Arguments, ArgumentsSHA256: argumentsSHA256, DeadlineMillis: deadlineMillis,
	}
	admissionDecision, err := s.authorizer.Admit(bounded, admission)
	if err == nil {
		err = execution.contextError()
	}
	validationErr := brokercontract.ValidateAdmissionDecisionForV1(admissionDecision, admission, execution.currentMillis())
	if err != nil || validationErr != nil {
		return BrokerExactReadResult{}, brokerReadError(firstBrokerReadError(err, validationErr))
	}
	qualification := domain.BrokerQualificationRequest{Admission: admission, AdmissionDecision: admissionDecision, Plan: domain.BrokerQualificationPlan{
		SelectorSHA256: argumentsSHA256, MetadataFields: append([]string{}, definition.QualificationFields...), Limits: definition.Limits.Qualification,
	}}
	qualificationDecision, err := s.authorizer.AuthorizeQualification(bounded, qualification)
	if err == nil {
		err = execution.contextError()
	}
	validationErr = brokercontract.ValidateQualificationDecisionForV1(qualificationDecision, qualification, execution.currentMillis())
	if err != nil || validationErr != nil {
		return BrokerExactReadResult{}, brokerReadError(firstBrokerReadError(err, validationErr))
	}

	switch request.Operation {
	case domain.BrokerOperationJiraIssueRead:
		return s.executeJiraBrokerRead(execution, request, qualification, qualificationDecision)
	case domain.BrokerOperationConfluencePageRead:
		return s.executeConfluenceBrokerRead(execution, request, qualification, qualificationDecision)
	default:
		return BrokerExactReadResult{}, brokerReadError(domain.ErrUsage)
	}
}

func brokerReadBindingValid(binding domain.BrokerBackendBinding, reader any, service string) bool {
	if reader == nil {
		return binding == (domain.BrokerBackendBinding{})
	}
	return binding.Service == service
}

func (s *BrokerReadService) backendMatches(operation domain.BrokerOperationID, verified domain.BrokerBackendBinding) (bool, error) {
	var configured domain.BrokerBackendBinding
	var observedOrigin string
	var err error
	switch operation {
	case domain.BrokerOperationJiraIssueRead:
		if s.jira.Reader == nil {
			return false, nil
		}
		configured = s.jira.Backend
		observedOrigin, err = s.jira.Reader.BrokerOriginSHA256()
	case domain.BrokerOperationConfluencePageRead:
		if s.confluence.Reader == nil {
			return false, nil
		}
		configured = s.confluence.Backend
		observedOrigin, err = s.confluence.Reader.BrokerOriginSHA256()
	default:
		return false, nil
	}
	return err == nil && observedOrigin == configured.OriginSHA256 && verified == configured, err
}

func prepareBrokerReadRequest(request domain.BrokerRequest) (domain.BrokerRequest, domain.BrokerOperationDefinition, error) {
	wire, err := brokercontract.EncodeRequestV1(request)
	if err != nil {
		return domain.BrokerRequest{}, domain.BrokerOperationDefinition{}, brokerReadError(err)
	}
	request, err = brokercontract.DecodeRequestV1(wire)
	if err != nil {
		return domain.BrokerRequest{}, domain.BrokerOperationDefinition{}, brokerReadError(err)
	}
	definition, ok := brokercontract.Definition(request.Operation, request.OperationVersion)
	if !ok || !definition.Available || request.Operation != domain.BrokerOperationJiraIssueRead && request.Operation != domain.BrokerOperationConfluencePageRead {
		return domain.BrokerRequest{}, domain.BrokerOperationDefinition{}, brokerReadError(domain.ErrUsage)
	}
	return request, definition, nil
}

func brokerReadDeadlineMillis(nowMillis int64, verified domain.BrokerVerifiedContext, maximum int64) int64 {
	deadline := nowMillis + maximum
	for _, candidate := range []int64{verified.ExecutionExpiresMillis, verified.GrantExpiresMillis, verified.CredentialExpiresMillis} {
		if candidate < deadline {
			deadline = candidate
		}
	}
	return deadline
}

func firstBrokerReadError(primary, validation error) error {
	if primary != nil {
		return primary
	}
	return validation
}

func brokerReadError(err error) error {
	return brokerOperationError("broker exact read failed", err)
}

func brokerOperationError(message string, err error) error {
	if ok, safe := brokercontract.ContentFreeError(err); ok {
		return fmt.Errorf("%s: %w", message, safe)
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
	return fmt.Errorf("%s: %w", message, sentinel)
}
