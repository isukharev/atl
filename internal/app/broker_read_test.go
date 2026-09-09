package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type brokerReadAuthorizerStub struct {
	nowMillis  int64
	denyPhase  domain.BrokerAuthorizationPhase
	denyReason domain.BrokerReason
	lease      time.Duration
	leasePhase domain.BrokerAuthorizationPhase
	calls      []domain.BrokerAuthorizationPhase
}

func (a *brokerReadAuthorizerStub) Admit(_ context.Context, request domain.BrokerAdmissionRequest) (domain.BrokerAdmissionDecision, error) {
	a.calls = append(a.calls, domain.BrokerPhaseAdmission)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Context)
	requestSHA256, _ := brokercontract.AdmissionRequestSHA256(request)
	core := a.decisionCore(domain.BrokerPhaseAdmission, contextSHA256, requestSHA256, request.Context.AuthorityRevision)
	return encodeBrokerAdmissionDecision(core)
}

func (a *brokerReadAuthorizerStub) AuthorizeQualification(_ context.Context, request domain.BrokerQualificationRequest) (domain.BrokerQualificationDecision, error) {
	a.calls = append(a.calls, domain.BrokerPhaseQualificationAuthorization)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Admission.Context)
	requestSHA256, _ := brokercontract.QualificationRequestSHA256(request)
	admissionSHA256, _ := brokercontract.AdmissionRequestSHA256(request.Admission)
	planSHA256, _ := brokercontract.QualificationPlanSHA256(request.Plan)
	value := domain.BrokerQualificationDecision{BrokerDecisionCore: a.decisionCore(domain.BrokerPhaseQualificationAuthorization, contextSHA256, requestSHA256, request.Admission.Context.AuthorityRevision), AdmissionRequestSHA256: admissionSHA256, AdmissionDecisionSHA256: request.AdmissionDecision.DecisionSHA256, PlanSHA256: planSHA256}
	wire, err := brokercontract.EncodeQualificationDecisionV1(value)
	if err != nil {
		return domain.BrokerQualificationDecision{}, err
	}
	return brokercontract.DecodeQualificationDecisionV1(wire)
}

func (a *brokerReadAuthorizerStub) AuthorizeOperation(_ context.Context, request domain.BrokerOperationAuthorizationRequest) (domain.BrokerOperationDecision, error) {
	a.calls = append(a.calls, domain.BrokerPhaseFinalAuthorization)
	admission := request.QualificationRequest.Admission
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(admission.Context)
	requestSHA256, _ := brokercontract.OperationAuthorizationRequestSHA256(request)
	resourcesSHA256, _ := brokercontract.QualifiedResourcesSHA256(request.QualifiedResources)
	effectsSHA256, _ := brokercontract.EffectsSHA256(request.Effects)
	value := domain.BrokerOperationDecision{BrokerDecisionCore: a.decisionCore(domain.BrokerPhaseFinalAuthorization, contextSHA256, requestSHA256, admission.Context.AuthorityRevision), QualificationDecisionSHA256: request.QualificationDecision.DecisionSHA256, Operation: admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256, ResourcesSHA256: resourcesSHA256, EffectsSHA256: effectsSHA256}
	wire, err := brokercontract.EncodeOperationDecisionV1(value)
	if err != nil {
		return domain.BrokerOperationDecision{}, err
	}
	return brokercontract.DecodeOperationDecisionV1(wire)
}

func (a *brokerReadAuthorizerStub) AuthorizeProposal(context.Context, domain.BrokerProposalAuthorizationRequest) (domain.BrokerProposalClearance, error) {
	return domain.BrokerProposalClearance{}, domain.ErrUsage
}

func (a *brokerReadAuthorizerStub) decisionCore(phase domain.BrokerAuthorizationPhase, contextSHA256, requestSHA256, revision string) domain.BrokerDecisionCore {
	status, reason := domain.BrokerDecisionAllowed, domain.BrokerReason("")
	if a.denyPhase == phase {
		status, reason = domain.BrokerDecisionDenied, a.denyReason
		if reason == "" {
			reason = domain.BrokerReasonDenied
		}
	}
	lease := a.lease
	if a.leasePhase != "" && a.leasePhase != phase {
		lease = 5 * time.Second
	}
	if lease == 0 {
		lease = 5 * time.Second
	}
	return domain.BrokerDecisionCore{Status: status, Reason: reason, DecisionID: string(phase) + "-1", AuthorityRevision: revision, ContextSHA256: contextSHA256, RequestSHA256: requestSHA256, IssuedAtMillis: a.nowMillis, ExpiresAtMillis: a.nowMillis + lease.Milliseconds()}
}

func encodeBrokerAdmissionDecision(core domain.BrokerDecisionCore) (domain.BrokerAdmissionDecision, error) {
	wire, err := brokercontract.EncodeAdmissionDecisionV1(domain.BrokerAdmissionDecision{BrokerDecisionCore: core})
	if err != nil {
		return domain.BrokerAdmissionDecision{}, err
	}
	return brokercontract.DecodeAdmissionDecisionV1(wire)
}

type brokerReadJiraStub struct {
	origin             string
	identity           domain.BrokerJiraIssueIdentity
	snapshot           domain.BrokerJiraIssueSnapshot
	qualification      int
	business           int
	backendAttempts    int
	afterQualification func()
	afterBusiness      func()
	waitQualification  bool
	waitBusiness       bool
}

func (s *brokerReadJiraStub) BrokerOriginSHA256() (string, error) { return s.origin, nil }

func (s *brokerReadJiraStub) QualifyBrokerIssue(ctx context.Context, _ string) (domain.BrokerJiraIssueIdentity, error) {
	s.qualification++
	if s.waitQualification {
		<-ctx.Done()
		return s.identity, nil
	}
	if err := chargeBrokerRead(ctx, 100); err != nil {
		return domain.BrokerJiraIssueIdentity{}, err
	}
	s.backendAttempts++
	if s.afterQualification != nil {
		s.afterQualification()
	}
	return s.identity, nil
}

func (s *brokerReadJiraStub) ReadBrokerIssue(ctx context.Context, _ string, _ []domain.BrokerJiraIssueField) (domain.BrokerJiraIssueSnapshot, error) {
	s.business++
	if s.waitBusiness {
		<-ctx.Done()
		return s.snapshot, nil
	}
	if err := chargeBrokerRead(ctx, 200); err != nil {
		return domain.BrokerJiraIssueSnapshot{}, err
	}
	s.backendAttempts++
	if s.afterBusiness != nil {
		s.afterBusiness()
	}
	return s.snapshot, nil
}

type brokerReadConfluenceStub struct {
	origin          string
	identity        domain.BrokerConfluencePageIdentity
	snapshot        domain.BrokerConfluencePageSnapshot
	qualification   int
	business        int
	backendAttempts int
}

func (s *brokerReadConfluenceStub) BrokerOriginSHA256() (string, error) { return s.origin, nil }

func (s *brokerReadConfluenceStub) QualifyBrokerPage(ctx context.Context, _ string) (domain.BrokerConfluencePageIdentity, error) {
	s.qualification++
	if err := chargeBrokerRead(ctx, 100); err != nil {
		return domain.BrokerConfluencePageIdentity{}, err
	}
	s.backendAttempts++
	return s.identity, nil
}

func (s *brokerReadConfluenceStub) ReadBrokerPage(ctx context.Context, _ string, _ domain.BrokerConfluenceProjection) (domain.BrokerConfluencePageSnapshot, error) {
	s.business++
	if err := chargeBrokerRead(ctx, 200); err != nil {
		return domain.BrokerConfluencePageSnapshot{}, err
	}
	s.backendAttempts++
	return s.snapshot, nil
}

func chargeBrokerRead(ctx context.Context, responseBytes int64) error {
	budget := domain.ReadBudgetFromContext(ctx)
	if !domain.SingleAttempt(ctx) || budget == nil {
		return domain.ErrCheckFailed
	}
	if err := budget.TakeAttempt(); err != nil {
		return err
	}
	remaining, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return err
	}
	if responseBytes > remaining {
		finish(remaining)
		return domain.ErrReadResponseBudgetExhausted
	}
	finish(responseBytes)
	return nil
}

func brokerReadFixture(nowMillis int64) (domain.BrokerRequest, domain.BrokerVerifiedContext, *brokerReadJiraStub, *brokerReadConfluenceStub) {
	request := domain.BrokerRequest{SchemaVersion: 1, Operation: domain.BrokerOperationJiraIssueRead, OperationVersion: 1, RequestID: "request-1", Features: []string{}, Expect: domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"}, Arguments: domain.BrokerOperationArguments{JiraIssueRead: &domain.BrokerJiraIssueReadArguments{IssueKey: "EXAMPLE-1", Fields: []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldDescription, domain.BrokerJiraIssueFieldSummary}}}}
	verified := domain.BrokerVerifiedContext{PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", ExecutionNotBeforeMillis: nowMillis - 1000, ExecutionExpiresMillis: nowMillis + 60000, GrantExpiresMillis: nowMillis + 60000, CredentialExpiresMillis: nowMillis + 60000, Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: digestText('a'), WorkloadBackendID: "jira-primary"}}
	issueIdentity := domain.BrokerJiraIssueIdentity{ID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", Updated: "revision-1", Complete: true}
	jira := &brokerReadJiraStub{origin: digestText('a'), identity: issueIdentity, snapshot: domain.BrokerJiraIssueSnapshot{Identity: issueIdentity, Fields: []domain.BrokerJiraIssueReadField{{Field: domain.BrokerJiraIssueFieldDescription, Present: true, Null: true}, {Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: "Summary"}}, Complete: true}}
	pageIdentity := domain.BrokerConfluencePageIdentity{ID: "42", Type: "page", Status: "current", Space: "DOCS", Version: 7, Updated: "revision-1", AncestorIDs: []string{}, AncestorsPresent: true, Complete: true}
	confluence := &brokerReadConfluenceStub{origin: digestText('b'), identity: pageIdentity, snapshot: domain.BrokerConfluencePageSnapshot{Identity: pageIdentity, Title: "Example", Projection: domain.BrokerConfluenceProjectionStorage, Storage: []byte("<p>native</p>"), StoragePresent: true, Complete: true}}
	return request, verified, jira, confluence
}

func brokerReadTestService(authorizer domain.BrokerAuthorizer, jira *brokerReadJiraStub, confluence *brokerReadConfluenceStub) *BrokerReadService {
	service, err := NewBrokerReadService(
		authorizer,
		BrokerJiraIssueReader{Backend: brokerJiraTestBackend(), Reader: jira},
		BrokerConfluencePageReader{Backend: brokerConfluenceTestBackend(), Reader: confluence},
	)
	if err != nil {
		panic(err)
	}
	return service
}

func brokerJiraTestBackend() domain.BrokerBackendBinding {
	return domain.BrokerBackendBinding{Service: "jira", OriginSHA256: digestText('a'), WorkloadBackendID: "jira-primary"}
}

func brokerConfluenceTestBackend() domain.BrokerBackendBinding {
	return domain.BrokerBackendBinding{Service: "confluence", OriginSHA256: digestText('b'), WorkloadBackendID: "confluence-primary"}
}

func TestBrokerReadServiceExactJiraAndConfluenceAllow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, verified, jira, confluence := brokerReadFixture(now.UnixMilli())
	authorizer := &brokerReadAuthorizerStub{nowMillis: now.UnixMilli()}
	service := brokerReadTestService(authorizer, jira, confluence)
	service.now = func() time.Time { return now }
	result, err := service.Execute(t.Context(), request, verified)
	if err != nil || result.JiraIssue == nil || result.ConfluencePage != nil || !result.ReleaseDeadline.Equal(now.Add(5*time.Second)) || jira.backendAttempts != 2 || !reflect.DeepEqual(authorizer.calls, []domain.BrokerAuthorizationPhase{domain.BrokerPhaseAdmission, domain.BrokerPhaseQualificationAuthorization, domain.BrokerPhaseFinalAuthorization}) {
		t.Fatalf("result=%+v err=%v attempts=%d auth=%v", result, err, jira.backendAttempts, authorizer.calls)
	}

	request.Operation = domain.BrokerOperationConfluencePageRead
	request.Arguments = domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: "42", Projection: domain.BrokerConfluenceProjectionStorage}}
	verified.Backend = brokerConfluenceTestBackend()
	authorizer.calls = nil
	result, err = service.Execute(t.Context(), request, verified)
	if err != nil || result.ConfluencePage == nil || result.JiraIssue != nil || confluence.backendAttempts != 2 {
		t.Fatalf("result=%+v err=%v attempts=%d", result, err, confluence.backendAttempts)
	}
}

func TestBrokerReadServiceAllowsOneConfiguredBackend(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, verified, jira, confluence := brokerReadFixture(now.UnixMilli())
	authorizer := &brokerReadAuthorizerStub{nowMillis: now.UnixMilli()}
	service, err := NewBrokerReadService(authorizer, BrokerJiraIssueReader{Backend: brokerJiraTestBackend(), Reader: jira}, BrokerConfluencePageReader{})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	if result, err := service.Execute(t.Context(), request, verified); err != nil || result.JiraIssue == nil || jira.backendAttempts != 2 {
		t.Fatalf("Jira result=%+v err=%v attempts=%d", result, err, jira.backendAttempts)
	}
	request.Operation = domain.BrokerOperationConfluencePageRead
	request.Arguments = domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: "42", Projection: domain.BrokerConfluenceProjectionMetadata}}
	verified.Backend = brokerConfluenceTestBackend()
	authorizer.calls = nil
	if result, err := service.Execute(t.Context(), request, verified); err == nil || result != (BrokerExactReadResult{}) || len(authorizer.calls) != 0 || confluence.backendAttempts != 0 {
		t.Fatalf("omitted result=%+v err=%v auth=%v attempts=%d", result, err, authorizer.calls, confluence.backendAttempts)
	}
}

func TestBrokerReadServiceDenialsPreserveZeroAndOneReadBoundaries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, test := range []struct {
		name              string
		mutateRequest     func(*domain.BrokerRequest)
		deny              domain.BrokerAuthorizationPhase
		wantAuth          int
		wantQualification int
	}{
		{name: "malformed", mutateRequest: func(request *domain.BrokerRequest) { request.Features = []string{"unknown_v1"} }},
		{name: "admission denied", deny: domain.BrokerPhaseAdmission, wantAuth: 1},
		{name: "qualification denied", deny: domain.BrokerPhaseQualificationAuthorization, wantAuth: 2},
		{name: "final denied", deny: domain.BrokerPhaseFinalAuthorization, wantAuth: 3, wantQualification: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, verified, jira, confluence := brokerReadFixture(now.UnixMilli())
			if test.mutateRequest != nil {
				test.mutateRequest(&request)
			}
			authorizer := &brokerReadAuthorizerStub{nowMillis: now.UnixMilli(), denyPhase: test.deny}
			service := brokerReadTestService(authorizer, jira, confluence)
			service.now = func() time.Time { return now }
			result, err := service.Execute(t.Context(), request, verified)
			if err == nil || result != (BrokerExactReadResult{}) || len(authorizer.calls) != test.wantAuth || jira.qualification != test.wantQualification || jira.business != 0 || jira.backendAttempts != test.wantQualification {
				t.Fatalf("result=%+v err=%v auth=%v qualification=%d business=%d attempts=%d", result, err, authorizer.calls, jira.qualification, jira.business, jira.backendAttempts)
			}
		})
	}
}

func TestBrokerReadServicePreservesUnsupportedConsistencyDenial(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, verified, jira, confluence := brokerReadFixture(now.UnixMilli())
	authorizer := &brokerReadAuthorizerStub{nowMillis: now.UnixMilli(), denyPhase: domain.BrokerPhaseQualificationAuthorization, denyReason: domain.BrokerReasonUnsupportedConsistency}
	service := brokerReadTestService(authorizer, jira, confluence)
	service.now = func() time.Time { return now }
	result, err := service.Execute(t.Context(), request, verified)
	reason, ok := brokercontract.Reason(err)
	if result != (BrokerExactReadResult{}) || !ok || reason != domain.BrokerReasonUnsupportedConsistency || jira.backendAttempts != 0 {
		t.Fatalf("result=%+v err=%v reason=%q attempts=%d", result, err, reason, jira.backendAttempts)
	}
}

func TestBrokerReadServiceRejectsDriftExpiryAndParentBudgetWithoutPublication(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, verified, jira, confluence := brokerReadFixture(now.UnixMilli())
	authorizer := &brokerReadAuthorizerStub{nowMillis: now.UnixMilli()}
	service := brokerReadTestService(authorizer, jira, confluence)
	service.now = func() time.Time { return now }
	jira.snapshot.Identity.Updated = "revision-2"
	if result, err := service.Execute(t.Context(), request, verified); err == nil || result != (BrokerExactReadResult{}) {
		t.Fatalf("drift result=%+v err=%v", result, err)
	}

	request, verified, jira, confluence = brokerReadFixture(now.UnixMilli())
	authorizer = &brokerReadAuthorizerStub{nowMillis: now.UnixMilli()}
	service = brokerReadTestService(authorizer, jira, confluence)
	current := now
	service.now = func() time.Time { return current }
	jira.afterQualification = func() { current = now.Add(6 * time.Second) }
	if result, err := service.Execute(t.Context(), request, verified); err == nil || result != (BrokerExactReadResult{}) || len(authorizer.calls) != 2 || jira.business != 0 {
		t.Fatalf("qualification expiry result=%+v err=%v auth=%v business=%d", result, err, authorizer.calls, jira.business)
	}

	request, verified, jira, confluence = brokerReadFixture(now.UnixMilli())
	authorizer = &brokerReadAuthorizerStub{nowMillis: now.UnixMilli()}
	service = brokerReadTestService(authorizer, jira, confluence)
	current = now
	service.now = func() time.Time { return current }
	jira.afterBusiness = func() { current = now.Add(6 * time.Second) }
	if result, err := service.Execute(t.Context(), request, verified); err == nil || result != (BrokerExactReadResult{}) {
		t.Fatalf("expiry result=%+v err=%v", result, err)
	}

	request, verified, jira, confluence = brokerReadFixture(now.UnixMilli())
	authorizer = &brokerReadAuthorizerStub{nowMillis: now.UnixMilli()}
	service = brokerReadTestService(authorizer, jira, confluence)
	service.now = func() time.Time { return now }
	parent, _ := domain.NewReadBudget(1, 1<<20)
	ctx := domain.WithReadBudget(t.Context(), parent)
	if result, err := service.Execute(ctx, request, verified); err == nil || result != (BrokerExactReadResult{}) || jira.backendAttempts != 1 {
		t.Fatalf("budget result=%+v err=%v attempts=%d", result, err, jira.backendAttempts)
	}
}

func TestBrokerReadServiceRejectsCancellationAfterBufferedBusinessRead(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, verified, jira, confluence := brokerReadFixture(now.UnixMilli())
	authorizer := &brokerReadAuthorizerStub{nowMillis: now.UnixMilli()}
	service := brokerReadTestService(authorizer, jira, confluence)
	service.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(t.Context())
	jira.afterBusiness = cancel
	result, err := service.Execute(ctx, request, verified)
	if result != (BrokerExactReadResult{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestBrokerReadServiceRejectsReaderBindingMismatchBeforeAuthorization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, test := range []struct {
		name   string
		mutate func(*domain.BrokerVerifiedContext, *BrokerReadService)
	}{
		{name: "verified differs from configured", mutate: func(verified *domain.BrokerVerifiedContext, _ *BrokerReadService) {
			verified.Backend.OriginSHA256 = digestText('c')
		}},
		{name: "configured differs from reader", mutate: func(verified *domain.BrokerVerifiedContext, service *BrokerReadService) {
			service.jira.Backend.OriginSHA256 = digestText('c')
			verified.Backend = service.jira.Backend
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, verified, jira, confluence := brokerReadFixture(now.UnixMilli())
			authorizer := &brokerReadAuthorizerStub{nowMillis: now.UnixMilli()}
			service := brokerReadTestService(authorizer, jira, confluence)
			test.mutate(&verified, service)
			result, err := service.Execute(t.Context(), request, verified)
			if result != (BrokerExactReadResult{}) || err == nil || len(authorizer.calls) != 0 || jira.backendAttempts != 0 || confluence.backendAttempts != 0 {
				t.Fatalf("result=%+v err=%v auth=%v jira=%d confluence=%d", result, err, authorizer.calls, jira.backendAttempts, confluence.backendAttempts)
			}
		})
	}
}

func TestBrokerReadServiceDecisionDeadlinePreventsQueuedDispatch(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, phase := range []string{"qualification", "business"} {
		t.Run(phase, func(t *testing.T) {
			request, verified, jira, confluence := brokerReadFixture(now.UnixMilli())
			leasePhase := domain.BrokerPhaseQualificationAuthorization
			if phase == "business" {
				leasePhase = domain.BrokerPhaseFinalAuthorization
			}
			authorizer := &brokerReadAuthorizerStub{nowMillis: now.UnixMilli(), lease: 10 * time.Millisecond, leasePhase: leasePhase}
			service := brokerReadTestService(authorizer, jira, confluence)
			service.now = func() time.Time { return now }
			jira.waitQualification = phase == "qualification"
			jira.waitBusiness = phase == "business"
			result, err := service.Execute(t.Context(), request, verified)
			wantAttempts := 0
			if phase == "business" {
				wantAttempts = 1
			}
			if result != (BrokerExactReadResult{}) || !errors.Is(err, context.DeadlineExceeded) || jira.backendAttempts != wantAttempts {
				t.Fatalf("result=%+v err=%v attempts=%d", result, err, jira.backendAttempts)
			}
			if jira.qualification != 1 || phase == "business" && jira.business != 1 {
				t.Fatal("deadline test did not reach the selected queued phase")
			}
		})
	}
}

func TestBrokerReadServicePreservesEmptyConfluenceStoragePresence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, verified, jira, confluence := brokerReadFixture(now.UnixMilli())
	request.Operation = domain.BrokerOperationConfluencePageRead
	request.Arguments = domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: "42", Projection: domain.BrokerConfluenceProjectionStorage}}
	verified.Backend = brokerConfluenceTestBackend()
	confluence.snapshot.Storage = []byte{}
	authorizer := &brokerReadAuthorizerStub{nowMillis: now.UnixMilli()}
	service := brokerReadTestService(authorizer, jira, confluence)
	service.now = func() time.Time { return now }
	result, err := service.Execute(t.Context(), request, verified)
	if err != nil || result.ConfluencePage == nil || !result.ConfluencePage.StoragePresent || result.ConfluencePage.Storage == nil || len(result.ConfluencePage.Storage) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestBrokerReadExecutionClockNeverMovesBackward(t *testing.T) {
	started := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	current := started
	definition, ok := brokercontract.Definition(domain.BrokerOperationJiraIssueRead, 1)
	if !ok {
		t.Fatal("missing exact-read definition")
	}
	execution, err := newBrokerReadExecution(t.Context(), definition, nil, func() time.Time { return current }, started)
	if err != nil {
		t.Fatal(err)
	}
	current = started.Add(4 * time.Second)
	if got := execution.currentMillis(); got != started.UnixMilli()+4000 {
		t.Fatalf("advanced=%d", got)
	}
	current = started.Add(-time.Hour)
	if got := execution.currentMillis(); got != started.UnixMilli()+4000 {
		t.Fatalf("rollback=%d", got)
	}
}

func digestText(value byte) string {
	result := make([]byte, 64)
	for index := range result {
		result[index] = value
	}
	return string(result)
}

func TestBrokerReadErrorDoesNotExposeUpstreamText(t *testing.T) {
	private := errors.New("PRIVATE-CANARY.example.invalid/secret")
	if got := brokerReadError(private).Error(); got != "broker exact read failed: check failed" {
		t.Fatalf("error=%q", got)
	}
	_, contractErr := brokercontract.DecodeRequestV1([]byte(`{}`))
	safe := brokerReadError(errors.Join(contractErr, private))
	reason, ok := brokercontract.Reason(safe)
	if !ok || reason != domain.BrokerReasonMalformed {
		t.Fatalf("reason=%q err=%v", reason, safe)
	}
	for _, formatted := range []string{safe.Error(), fmt.Sprintf("%+v", safe), fmt.Sprintf("%#v", safe), fmt.Sprint(errors.Unwrap(safe))} {
		if strings.Contains(formatted, "PRIVATE-CANARY") {
			t.Fatalf("unsafe error: %q", formatted)
		}
	}
}
