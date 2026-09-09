package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type brokerProjectPageAuthorizerStub struct {
	nowMillis int64
	deny      domain.BrokerAuthorizationPhase
	reason    domain.BrokerReason
	lease     time.Duration
	calls     []domain.BrokerAuthorizationPhase
	operation domain.BrokerProjectPageOperationAuthorizationRequestV2
}

func (a *brokerProjectPageAuthorizerStub) decisionCore(t context.Context, phase domain.BrokerAuthorizationPhase, verified domain.BrokerVerifiedContext, requestSHA256 string) domain.BrokerDecisionCore {
	_ = t
	status, reason := domain.BrokerDecisionAllowed, domain.BrokerReason("")
	if a.deny == phase {
		status, reason = domain.BrokerDecisionDenied, a.reason
		if reason == "" {
			reason = domain.BrokerReasonDenied
		}
	}
	lease := a.lease
	if lease == 0 {
		lease = 5 * time.Second
	}
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(verified)
	return domain.BrokerDecisionCore{
		Status: status, Reason: reason, DecisionID: string(phase) + "-page-1", AuthorityRevision: verified.AuthorityRevision,
		ContextSHA256: contextSHA256, RequestSHA256: requestSHA256, IssuedAtMillis: a.nowMillis, ExpiresAtMillis: a.nowMillis + lease.Milliseconds(),
	}
}

func (a *brokerProjectPageAuthorizerStub) AdmitProjectPage(ctx context.Context, request domain.BrokerProjectPageAdmissionRequestV2) (domain.BrokerProjectPageAdmissionDecisionV2, error) {
	a.calls = append(a.calls, domain.BrokerPhaseAdmission)
	digest, _ := brokercontract.ProjectPageAdmissionRequestSHA256V2(request)
	wire, err := brokercontract.EncodeProjectPageAdmissionDecisionV2(domain.BrokerProjectPageAdmissionDecisionV2{BrokerDecisionCore: a.decisionCore(ctx, domain.BrokerPhaseAdmission, request.Context, digest)})
	if err != nil {
		return domain.BrokerProjectPageAdmissionDecisionV2{}, err
	}
	return brokercontract.DecodeProjectPageAdmissionDecisionV2(wire)
}

func (a *brokerProjectPageAuthorizerStub) AuthorizeProjectPageQualification(ctx context.Context, request domain.BrokerProjectPageQualificationRequestV2) (domain.BrokerProjectPageQualificationDecisionV2, error) {
	a.calls = append(a.calls, domain.BrokerPhaseQualificationAuthorization)
	requestDigest, _ := brokercontract.ProjectPageQualificationRequestSHA256V2(request)
	admissionDigest, _ := brokercontract.ProjectPageAdmissionRequestSHA256V2(request.Admission)
	planDigest, _ := brokercontract.ProjectPageQualificationPlanSHA256V2(request.Plan)
	value := domain.BrokerProjectPageQualificationDecisionV2{
		BrokerDecisionCore:      a.decisionCore(ctx, domain.BrokerPhaseQualificationAuthorization, request.Admission.Context, requestDigest),
		AdmissionRequestSHA256:  admissionDigest,
		AdmissionDecisionSHA256: request.AdmissionDecision.DecisionSHA256,
		PlanSHA256:              planDigest,
	}
	wire, err := brokercontract.EncodeProjectPageQualificationDecisionV2(value)
	if err != nil {
		return domain.BrokerProjectPageQualificationDecisionV2{}, err
	}
	return brokercontract.DecodeProjectPageQualificationDecisionV2(wire)
}

func (a *brokerProjectPageAuthorizerStub) AuthorizeProjectPage(ctx context.Context, request domain.BrokerProjectPageOperationAuthorizationRequestV2) (domain.BrokerProjectPageOperationDecisionV2, error) {
	a.calls = append(a.calls, domain.BrokerPhaseFinalAuthorization)
	a.operation = request
	requestDigest, _ := brokercontract.ProjectPageOperationAuthorizationRequestSHA256V2(request)
	resourcesDigest, _ := brokercontract.ProjectPageResourcesSHA256V2(request.Project, request.Issues)
	pageDigest, _ := brokercontract.ProjectPageEvidenceSHA256V2(request.Page)
	effectsDigest, _ := brokercontract.ProjectPageEffectsSHA256V2(request.Effects)
	admission := request.QualificationRequest.Admission
	value := domain.BrokerProjectPageOperationDecisionV2{
		BrokerDecisionCore:          a.decisionCore(ctx, domain.BrokerPhaseFinalAuthorization, admission.Context, requestDigest),
		QualificationDecisionSHA256: request.QualificationDecision.DecisionSHA256, Operation: admission.Operation,
		OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256,
		ResourcesSHA256: resourcesDigest, PageSHA256: pageDigest, EffectsSHA256: effectsDigest,
	}
	wire, err := brokercontract.EncodeProjectPageOperationDecisionV2(value)
	if err != nil {
		return domain.BrokerProjectPageOperationDecisionV2{}, err
	}
	return brokercontract.DecodeProjectPageOperationDecisionV2(wire)
}

type brokerProjectPageReaderStub struct {
	origin          string
	project         domain.BrokerJiraProjectIdentityV2
	identityPage    domain.BrokerJiraProjectPageIdentitySnapshotV2
	businessPage    domain.BrokerJiraProjectPageSnapshotV2
	projectCalls    int
	identityCalls   int
	businessCalls   int
	backendAttempts int
	afterProject    func()
	afterIdentity   func()
	afterBusiness   func()
}

func (s *brokerProjectPageReaderStub) BrokerOriginSHA256() (string, error) { return s.origin, nil }

func (s *brokerProjectPageReaderStub) QualifyBrokerProject(ctx context.Context, _ string) (domain.BrokerJiraProjectIdentityV2, error) {
	s.projectCalls++
	if err := chargeBrokerRead(ctx, 100); err != nil {
		return domain.BrokerJiraProjectIdentityV2{}, err
	}
	s.backendAttempts++
	if s.afterProject != nil {
		s.afterProject()
	}
	return s.project, nil
}

func (s *brokerProjectPageReaderStub) QualifyBrokerProjectIssuePage(ctx context.Context, _ string, _, _ int) (domain.BrokerJiraProjectPageIdentitySnapshotV2, error) {
	s.identityCalls++
	if err := chargeBrokerRead(ctx, 200); err != nil {
		return domain.BrokerJiraProjectPageIdentitySnapshotV2{}, err
	}
	s.backendAttempts++
	if s.afterIdentity != nil {
		s.afterIdentity()
	}
	return s.identityPage, nil
}

func (s *brokerProjectPageReaderStub) ReadBrokerProjectIssuePage(ctx context.Context, _ string, _ []domain.BrokerProjectPageField, _, _ int) (domain.BrokerJiraProjectPageSnapshotV2, error) {
	s.businessCalls++
	if err := chargeBrokerRead(ctx, 300); err != nil {
		return domain.BrokerJiraProjectPageSnapshotV2{}, err
	}
	s.backendAttempts++
	if s.afterBusiness != nil {
		s.afterBusiness()
	}
	return s.businessPage, nil
}

func brokerProjectPageFixture(nowMillis int64) (domain.BrokerProjectPageRequestV2, domain.BrokerVerifiedContext, *brokerProjectPageReaderStub) {
	request := domain.BrokerProjectPageRequestV2{
		SchemaVersion: brokercontract.ExecutionSchemaVersionV2, Operation: domain.BrokerOperationJiraProjectIssuePageRead,
		OperationVersion: brokercontract.ProjectPageOperationVersion, RequestID: "request-page-1",
		Features: []string{"bounded_project_page_v1", domain.BrokerReadConsistencyIdentitySnapshotV1},
		Expect:   domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"},
		Arguments: domain.BrokerProjectPageArguments{
			ProjectKey: "EXAMPLE", Fields: []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldDescription, domain.BrokerProjectPageFieldSummary},
			StartAt: 0, MaxResults: 2,
		},
	}
	verified := domain.BrokerVerifiedContext{
		PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1",
		Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", ExecutionNotBeforeMillis: nowMillis - 1_000,
		ExecutionExpiresMillis: nowMillis + 60_000, GrantExpiresMillis: nowMillis + 60_000, CredentialExpiresMillis: nowMillis + 60_000,
		Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: digestText('a'), WorkloadBackendID: "jira-primary"},
	}
	identities := []domain.BrokerJiraProjectPageIssueIdentityV2{
		{ID: "20", Key: "EXAMPLE-20", ProjectID: "7", ProjectKey: "EXAMPLE", Updated: "2026-09-08T10:00:00.000+0000", Complete: true},
		{ID: "3", Key: "EXAMPLE-3", ProjectID: "7", ProjectKey: "EXAMPLE", Updated: "2026-09-08T10:00:00.000+0000", Complete: true},
	}
	fields := func(summary string) []domain.BrokerJiraIssueReadField {
		return []domain.BrokerJiraIssueReadField{{Field: domain.BrokerJiraIssueFieldDescription, Present: true, Null: true}, {Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: summary}}
	}
	reader := &brokerProjectPageReaderStub{
		origin:  digestText('a'),
		project: domain.BrokerJiraProjectIdentityV2{ID: "7", Key: "EXAMPLE", Complete: true},
		identityPage: domain.BrokerJiraProjectPageIdentitySnapshotV2{
			StartAt: 0, MaxResults: 2, Total: 3, Issues: identities, Complete: true,
		},
		businessPage: domain.BrokerJiraProjectPageSnapshotV2{
			StartAt: 0, MaxResults: 2, Total: 3, Complete: true,
			Issues: []domain.BrokerJiraProjectPageIssueSnapshotV2{{Identity: identities[0], Fields: fields("Twenty")}, {Identity: identities[1], Fields: fields("Three")}},
		},
	}
	return request, verified, reader
}

func brokerProjectPageTestService(authorizer domain.BrokerProjectPageAuthorizerV2, reader *brokerProjectPageReaderStub) *BrokerProjectPageService {
	service, err := NewBrokerProjectPageService(authorizer, BrokerJiraProjectPageReader{
		Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: digestText('a'), WorkloadBackendID: "jira-primary"}, Reader: reader,
	})
	if err != nil {
		panic(err)
	}
	return service
}

func TestBrokerProjectPageServiceAllowsOneBufferedPageAndPreservesOrder(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, verified, reader := brokerProjectPageFixture(now.UnixMilli())
	authorizer := &brokerProjectPageAuthorizerStub{nowMillis: now.UnixMilli()}
	service := brokerProjectPageTestService(authorizer, reader)
	service.now = func() time.Time { return now }
	result, err := service.Execute(t.Context(), request, verified)
	if err != nil || result.Page == nil || result.Page.Issues[0].ID != "20" || result.Page.Issues[1].ID != "3" ||
		result.Page.Page.NextCursor != "2" || !result.Page.Page.NextCursorPresent || result.Page.Page.SelectionComplete ||
		reader.backendAttempts != 3 || !result.ReleaseDeadline.Equal(now.Add(5*time.Second)) ||
		!reflect.DeepEqual(authorizer.calls, []domain.BrokerAuthorizationPhase{domain.BrokerPhaseAdmission, domain.BrokerPhaseQualificationAuthorization, domain.BrokerPhaseFinalAuthorization}) {
		t.Fatalf("result=%+v err=%v attempts=%d auth=%v", result, err, reader.backendAttempts, authorizer.calls)
	}
	if len(authorizer.operation.Issues) != 2 || authorizer.operation.Issues[0].ID != "3" || authorizer.operation.Issues[1].ID != "20" ||
		!reflect.DeepEqual(authorizer.operation.Page.OrderedIssueIDs, []string{"20", "3"}) || len(authorizer.operation.Effects) != 3 ||
		authorizer.operation.Effects[0].Project == nil || authorizer.operation.Effects[1].Issue.ID != "3" {
		t.Fatalf("authorization=%+v", authorizer.operation)
	}
}

func TestBrokerProjectPageServiceDenialsPreserveExactReadBoundaries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, test := range []struct {
		name         string
		mutate       func(*domain.BrokerProjectPageRequestV2)
		deny         domain.BrokerAuthorizationPhase
		wantAuth     int
		wantAttempts int
	}{
		{name: "malformed", mutate: func(request *domain.BrokerProjectPageRequestV2) {
			request.Arguments.ProjectKey = "private OR project=OTHER"
		}},
		{name: "admission denied", deny: domain.BrokerPhaseAdmission, wantAuth: 1},
		{name: "qualification denied", deny: domain.BrokerPhaseQualificationAuthorization, wantAuth: 2},
		{name: "final denied", deny: domain.BrokerPhaseFinalAuthorization, wantAuth: 3, wantAttempts: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, verified, reader := brokerProjectPageFixture(now.UnixMilli())
			if test.mutate != nil {
				test.mutate(&request)
			}
			authorizer := &brokerProjectPageAuthorizerStub{nowMillis: now.UnixMilli(), deny: test.deny}
			service := brokerProjectPageTestService(authorizer, reader)
			service.now = func() time.Time { return now }
			result, err := service.Execute(t.Context(), request, verified)
			if err == nil || result != (BrokerProjectPageResult{}) || len(authorizer.calls) != test.wantAuth || reader.backendAttempts != test.wantAttempts || reader.businessCalls != 0 {
				t.Fatalf("result=%+v err=%v auth=%v attempts=%d business=%d", result, err, authorizer.calls, reader.backendAttempts, reader.businessCalls)
			}
		})
	}
}

func TestBrokerProjectPageServicePreservesRevocationAndConsistencyDenials(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, reason := range []domain.BrokerReason{domain.BrokerReasonRevoked, domain.BrokerReasonUnsupportedConsistency} {
		request, verified, reader := brokerProjectPageFixture(now.UnixMilli())
		authorizer := &brokerProjectPageAuthorizerStub{nowMillis: now.UnixMilli(), deny: domain.BrokerPhaseQualificationAuthorization, reason: reason}
		service := brokerProjectPageTestService(authorizer, reader)
		service.now = func() time.Time { return now }
		result, err := service.Execute(t.Context(), request, verified)
		got, ok := brokercontract.Reason(err)
		if result != (BrokerProjectPageResult{}) || !ok || got != reason || reader.backendAttempts != 0 {
			t.Fatalf("reason=%s result=%+v err=%v got=%s attempts=%d", reason, result, err, got, reader.backendAttempts)
		}
	}
}

func TestBrokerProjectPageServiceRejectsDriftBeforePublication(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	mutations := map[string]func(*brokerProjectPageReaderStub){
		"updated": func(reader *brokerProjectPageReaderStub) {
			reader.businessPage.Issues[0].Identity.Updated = "2026-09-08T11:00:00.000+0000"
		},
		"order": func(reader *brokerProjectPageReaderStub) {
			reader.businessPage.Issues[0], reader.businessPage.Issues[1] = reader.businessPage.Issues[1], reader.businessPage.Issues[0]
		},
		"total":      func(reader *brokerProjectPageReaderStub) { reader.businessPage.Total = 4 },
		"project":    func(reader *brokerProjectPageReaderStub) { reader.businessPage.Issues[0].Identity.ProjectID = "8" },
		"coordinate": func(reader *brokerProjectPageReaderStub) { reader.businessPage.CoordinateExhausted = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			request, verified, reader := brokerProjectPageFixture(now.UnixMilli())
			mutate(reader)
			authorizer := &brokerProjectPageAuthorizerStub{nowMillis: now.UnixMilli()}
			service := brokerProjectPageTestService(authorizer, reader)
			service.now = func() time.Time { return now }
			result, err := service.Execute(t.Context(), request, verified)
			if err == nil || result != (BrokerProjectPageResult{}) || reader.backendAttempts != 3 {
				t.Fatalf("result=%+v err=%v attempts=%d", result, err, reader.backendAttempts)
			}
		})
	}
}

func TestBrokerProjectPageServiceLeaseCancellationAndParentBudget(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, test := range []struct {
		name         string
		configure    func(*brokerProjectPageReaderStub, *time.Time)
		parent       *domain.ReadBudget
		wantAttempts int
		wantAuth     int
	}{
		{name: "lease after project", configure: func(reader *brokerProjectPageReaderStub, current *time.Time) {
			reader.afterProject = func() { *current = now.Add(6 * time.Second) }
		}, wantAttempts: 1, wantAuth: 2},
		{name: "lease after identity", configure: func(reader *brokerProjectPageReaderStub, current *time.Time) {
			reader.afterIdentity = func() { *current = now.Add(6 * time.Second) }
		}, wantAttempts: 2, wantAuth: 2},
		{name: "lease after business", configure: func(reader *brokerProjectPageReaderStub, current *time.Time) {
			reader.afterBusiness = func() { *current = now.Add(6 * time.Second) }
		}, wantAttempts: 3, wantAuth: 3},
		{name: "parent attempts", parent: mustBrokerProjectPageBudget(t, 2, 1<<20), wantAttempts: 2, wantAuth: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, verified, reader := brokerProjectPageFixture(now.UnixMilli())
			authorizer := &brokerProjectPageAuthorizerStub{nowMillis: now.UnixMilli()}
			service := brokerProjectPageTestService(authorizer, reader)
			current := now
			service.now = func() time.Time { return current }
			if test.configure != nil {
				test.configure(reader, &current)
			}
			ctx := t.Context()
			if test.parent != nil {
				ctx = domain.WithReadBudget(ctx, test.parent)
			}
			result, err := service.Execute(ctx, request, verified)
			if err == nil || result != (BrokerProjectPageResult{}) || reader.backendAttempts != test.wantAttempts || len(authorizer.calls) != test.wantAuth {
				t.Fatalf("result=%+v err=%v attempts=%d auth=%v", result, err, reader.backendAttempts, authorizer.calls)
			}
		})
	}

	request, verified, reader := brokerProjectPageFixture(now.UnixMilli())
	authorizer := &brokerProjectPageAuthorizerStub{nowMillis: now.UnixMilli()}
	service := brokerProjectPageTestService(authorizer, reader)
	service.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(t.Context())
	reader.afterBusiness = cancel
	result, err := service.Execute(ctx, request, verified)
	if !errors.Is(err, context.Canceled) || result != (BrokerProjectPageResult{}) || reader.backendAttempts != 3 {
		t.Fatalf("canceled result=%+v err=%v attempts=%d", result, err, reader.backendAttempts)
	}
}

func mustBrokerProjectPageBudget(t testing.TB, attempts int, bytes int64) *domain.ReadBudget {
	t.Helper()
	budget, err := domain.NewReadBudget(attempts, bytes)
	if err != nil {
		t.Fatal(err)
	}
	return budget
}

func TestBrokerProjectPageServiceReportsOnlyCoordinateTruth(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, test := range []struct {
		name       string
		total      int
		exhausted  bool
		stalled    bool
		wantReason string
	}{
		{name: "empty terminal", total: 0, exhausted: true},
		{name: "stalled", total: 3, stalled: true, wantReason: brokercontract.BrokerProjectPagePartialPaginationStalledV2},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, verified, reader := brokerProjectPageFixture(now.UnixMilli())
			reader.identityPage.Issues = []domain.BrokerJiraProjectPageIssueIdentityV2{}
			reader.identityPage.Total, reader.identityPage.CoordinateExhausted, reader.identityPage.PaginationStalled = test.total, test.exhausted, test.stalled
			reader.businessPage.Issues = []domain.BrokerJiraProjectPageIssueSnapshotV2{}
			reader.businessPage.Total, reader.businessPage.CoordinateExhausted, reader.businessPage.PaginationStalled = test.total, test.exhausted, test.stalled
			authorizer := &brokerProjectPageAuthorizerStub{nowMillis: now.UnixMilli()}
			service := brokerProjectPageTestService(authorizer, reader)
			service.now = func() time.Time { return now }
			result, err := service.Execute(t.Context(), request, verified)
			if err != nil || result.Page == nil || result.Page.Page.SelectionComplete || result.Page.Page.CoordinateExhausted != test.exhausted ||
				result.Page.Page.PartialReason != test.wantReason || result.Page.Page.NextCursorPresent {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestBrokerProjectPageServiceAllowsExplicitIdentityOnlyProjection(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, verified, reader := brokerProjectPageFixture(now.UnixMilli())
	request.Arguments.Fields = []domain.BrokerProjectPageField{}
	for index := range reader.businessPage.Issues {
		reader.businessPage.Issues[index].Fields = []domain.BrokerJiraIssueReadField{}
	}
	authorizer := &brokerProjectPageAuthorizerStub{nowMillis: now.UnixMilli()}
	service := brokerProjectPageTestService(authorizer, reader)
	service.now = func() time.Time { return now }
	result, err := service.Execute(t.Context(), request, verified)
	if err != nil || result.Page == nil || result.Page.Issues[0].Fields == nil || len(result.Page.Issues[0].Fields) != 0 || reader.backendAttempts != 3 {
		t.Fatalf("result=%+v err=%v attempts=%d", result, err, reader.backendAttempts)
	}
}

func TestBrokerProjectPageServiceStopsContinuationAtOffsetCeiling(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, verified, reader := brokerProjectPageFixture(now.UnixMilli())
	request.Arguments.StartAt = domain.BrokerProjectPageMaxStartAt
	reader.identityPage.StartAt = request.Arguments.StartAt
	reader.identityPage.Issues = reader.identityPage.Issues[:1]
	reader.identityPage.Total = request.Arguments.StartAt + 2
	reader.businessPage.StartAt = request.Arguments.StartAt
	reader.businessPage.Issues = reader.businessPage.Issues[:1]
	reader.businessPage.Total = request.Arguments.StartAt + 2
	authorizer := &brokerProjectPageAuthorizerStub{nowMillis: now.UnixMilli()}
	service := brokerProjectPageTestService(authorizer, reader)
	service.now = func() time.Time { return now }
	result, err := service.Execute(t.Context(), request, verified)
	if err != nil || result.Page == nil || result.Page.Page.NextCursorPresent || result.Page.Page.CoordinateExhausted ||
		result.Page.Page.SelectionComplete || result.Page.Page.PartialReason != brokercontract.BrokerProjectPagePartialOffsetLimitV2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestBrokerProjectPageServiceRejectsBackendBindingBeforeAuthority(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, verified, reader := brokerProjectPageFixture(now.UnixMilli())
	authorizer := &brokerProjectPageAuthorizerStub{nowMillis: now.UnixMilli()}
	service := brokerProjectPageTestService(authorizer, reader)
	service.now = func() time.Time { return now }
	verified.Backend.OriginSHA256 = digestText('b')
	result, err := service.Execute(t.Context(), request, verified)
	if err == nil || result != (BrokerProjectPageResult{}) || len(authorizer.calls) != 0 || reader.backendAttempts != 0 {
		t.Fatalf("result=%+v err=%v auth=%v attempts=%d", result, err, authorizer.calls, reader.backendAttempts)
	}
}
