package brokerserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

type projectPageServerAuthorizer struct {
	mu             sync.Mutex
	nowMillis      int64
	deny           domain.BrokerAuthorizationPhase
	discoveryCalls int
	discoveryErr   error
	mutate         func(*domain.BrokerFamilyDiscoveryProjectionV3)
	phases         []domain.BrokerAuthorizationPhase
}

func (a *projectPageServerAuthorizer) decisionCore(phase domain.BrokerAuthorizationPhase, verified domain.BrokerVerifiedContext, requestSHA256 string) domain.BrokerDecisionCore {
	status, reason := domain.BrokerDecisionAllowed, domain.BrokerReason("")
	if a.deny == phase {
		status, reason = domain.BrokerDecisionDenied, domain.BrokerReasonDenied
	}
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(verified)
	return domain.BrokerDecisionCore{
		Status: status, Reason: reason, DecisionID: string(phase) + "-page-server-1", AuthorityRevision: verified.AuthorityRevision,
		ContextSHA256: contextSHA256, RequestSHA256: requestSHA256, IssuedAtMillis: a.nowMillis, ExpiresAtMillis: a.nowMillis + 5_000,
	}
}

func (a *projectPageServerAuthorizer) record(phase domain.BrokerAuthorizationPhase) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.phases = append(a.phases, phase)
}

func (a *projectPageServerAuthorizer) phaseSnapshot() []domain.BrokerAuthorizationPhase {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]domain.BrokerAuthorizationPhase(nil), a.phases...)
}

func (a *projectPageServerAuthorizer) AdmitProjectPage(_ context.Context, request domain.BrokerProjectPageAdmissionRequestV2) (domain.BrokerProjectPageAdmissionDecisionV2, error) {
	a.record(domain.BrokerPhaseAdmission)
	digest, _ := brokercontract.ProjectPageAdmissionRequestSHA256V2(request)
	wire, err := brokercontract.EncodeProjectPageAdmissionDecisionV2(domain.BrokerProjectPageAdmissionDecisionV2{BrokerDecisionCore: a.decisionCore(domain.BrokerPhaseAdmission, request.Context, digest)})
	if err != nil {
		return domain.BrokerProjectPageAdmissionDecisionV2{}, err
	}
	return brokercontract.DecodeProjectPageAdmissionDecisionV2(wire)
}

func (a *projectPageServerAuthorizer) AuthorizeProjectPageQualification(_ context.Context, request domain.BrokerProjectPageQualificationRequestV2) (domain.BrokerProjectPageQualificationDecisionV2, error) {
	a.record(domain.BrokerPhaseQualificationAuthorization)
	requestDigest, _ := brokercontract.ProjectPageQualificationRequestSHA256V2(request)
	admissionDigest, _ := brokercontract.ProjectPageAdmissionRequestSHA256V2(request.Admission)
	planDigest, _ := brokercontract.ProjectPageQualificationPlanSHA256V2(request.Plan)
	value := domain.BrokerProjectPageQualificationDecisionV2{
		BrokerDecisionCore:     a.decisionCore(domain.BrokerPhaseQualificationAuthorization, request.Admission.Context, requestDigest),
		AdmissionRequestSHA256: admissionDigest, AdmissionDecisionSHA256: request.AdmissionDecision.DecisionSHA256, PlanSHA256: planDigest,
	}
	wire, err := brokercontract.EncodeProjectPageQualificationDecisionV2(value)
	if err != nil {
		return domain.BrokerProjectPageQualificationDecisionV2{}, err
	}
	return brokercontract.DecodeProjectPageQualificationDecisionV2(wire)
}

func (a *projectPageServerAuthorizer) AuthorizeProjectPage(_ context.Context, request domain.BrokerProjectPageOperationAuthorizationRequestV2) (domain.BrokerProjectPageOperationDecisionV2, error) {
	a.record(domain.BrokerPhaseFinalAuthorization)
	requestDigest, _ := brokercontract.ProjectPageOperationAuthorizationRequestSHA256V2(request)
	resourcesDigest, _ := brokercontract.ProjectPageResourcesSHA256V2(request.Project, request.Issues)
	pageDigest, _ := brokercontract.ProjectPageEvidenceSHA256V2(request.Page)
	effectsDigest, _ := brokercontract.ProjectPageEffectsSHA256V2(request.Effects)
	admission := request.QualificationRequest.Admission
	value := domain.BrokerProjectPageOperationDecisionV2{
		BrokerDecisionCore:          a.decisionCore(domain.BrokerPhaseFinalAuthorization, admission.Context, requestDigest),
		QualificationDecisionSHA256: request.QualificationDecision.DecisionSHA256,
		Operation:                   admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256,
		ResourcesSHA256: resourcesDigest, PageSHA256: pageDigest, EffectsSHA256: effectsDigest,
	}
	wire, err := brokercontract.EncodeProjectPageOperationDecisionV2(value)
	if err != nil {
		return domain.BrokerProjectPageOperationDecisionV2{}, err
	}
	return brokercontract.DecodeProjectPageOperationDecisionV2(wire)
}

func (a *projectPageServerAuthorizer) DiscoverFamilyV3(_ context.Context, request domain.BrokerFamilyDiscoveryAuthorizationRequestV3) (domain.BrokerFamilyDiscoveryProjectionV3, error) {
	a.mu.Lock()
	a.discoveryCalls++
	a.mu.Unlock()
	if a.discoveryErr != nil {
		return domain.BrokerFamilyDiscoveryProjectionV3{}, a.discoveryErr
	}
	projection := domain.BrokerFamilyDiscoveryProjectionV3{
		SchemaVersion: domain.BrokerDiscoverySchemaVersionV3, RequestID: request.Request.RequestID, RequestSHA256: request.RequestSHA256,
		ContextSHA256: request.Request.ContextSHA256, ExecutionID: request.Context.ExecutionID, ExecutionEpoch: request.Context.ExecutionEpoch,
		Audience: request.Context.Audience, BrokerID: request.Context.BrokerID, AuthorityRevision: request.Context.AuthorityRevision,
		ContractFamily: request.Request.ContractFamily, Service: request.Request.Service, RegistrySHA256: brokercontract.RegistrySHA256V2(),
		ContractSchemaSHA256: brokercontract.ExecutionSchemaSHA256V2(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V3(),
		IssuedAtMillis: a.nowMillis, ExpiresAtMillis: min(a.nowMillis+5_000, request.Request.NotAfterMillis),
		Operations: []domain.BrokerFamilyDiscoveryOperationV3{}, Complete: true,
	}
	for _, wrapped := range brokercontract.AvailableDefinitionsV2() {
		definition := wrapped.Definition
		projection.Operations = append(projection.Operations, domain.BrokerFamilyDiscoveryOperationV3{
			ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed,
			Features: append([]string(nil), definition.RequiredFeatures...), Limits: definition.Limits,
			Effects: append([]domain.BrokerEffectDefinition(nil), definition.Effects...),
		})
	}
	if a.mutate != nil {
		a.mutate(&projection)
	}
	return projection, nil
}

type projectPageServerReader struct {
	origin        string
	project       domain.BrokerJiraProjectIdentityV2
	identity      domain.BrokerJiraProjectPageIdentitySnapshotV2
	business      domain.BrokerJiraProjectPageSnapshotV2
	projectCalls  atomic.Int32
	identityCalls atomic.Int32
	businessCalls atomic.Int32
}

func (r *projectPageServerReader) BrokerOriginSHA256() (string, error) { return r.origin, nil }

func (r *projectPageServerReader) QualifyBrokerProject(context.Context, string) (domain.BrokerJiraProjectIdentityV2, error) {
	r.projectCalls.Add(1)
	return r.project, nil
}

func (r *projectPageServerReader) QualifyBrokerProjectIssuePage(context.Context, string, int, int) (domain.BrokerJiraProjectPageIdentitySnapshotV2, error) {
	r.identityCalls.Add(1)
	return r.identity, nil
}

func (r *projectPageServerReader) ReadBrokerProjectIssuePage(context.Context, string, []domain.BrokerProjectPageField, int, int) (domain.BrokerJiraProjectPageSnapshotV2, error) {
	r.businessCalls.Add(1)
	return r.business, nil
}

func (r *projectPageServerReader) calls() (int32, int32, int32) {
	return r.projectCalls.Load(), r.identityCalls.Load(), r.businessCalls.Load()
}

type projectPageServerFixture struct {
	base       brokerServerFixture
	authorizer *projectPageServerAuthorizer
	reader     *projectPageServerReader
	request    domain.BrokerProjectPageRequestV2
	body       []byte
}

func newProjectPageServerFixture(t *testing.T, deny domain.BrokerAuthorizationPhase, guardCredentials ...[]byte) projectPageServerFixture {
	t.Helper()
	base := newBrokerServerFixture(t, "Synthetic v1 summary", "", nil, guardCredentials...)
	definition, ok := brokercontract.DefinitionV2(domain.BrokerOperationJiraProjectIssuePageRead, brokercontract.ProjectPageOperationVersion)
	if !ok {
		t.Fatal("project-page definition is missing")
	}
	request := domain.BrokerProjectPageRequestV2{
		SchemaVersion: brokercontract.ExecutionSchemaVersionV2, Operation: domain.BrokerOperationJiraProjectIssuePageRead,
		OperationVersion: brokercontract.ProjectPageOperationVersion, RequestID: "project-page-request-1",
		Features:  append([]string(nil), definition.Definition.RequiredFeatures...),
		Expect:    domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"},
		Arguments: domain.BrokerProjectPageArguments{ProjectKey: "PROJ", Fields: []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldSummary}, StartAt: 0, MaxResults: 2},
	}
	identity := domain.BrokerJiraProjectPageIssueIdentityV2{ID: "10001", Key: "PROJ-7", ProjectID: "100", ProjectKey: "PROJ", Updated: "2026-09-08T10:00:00.000+0000", Complete: true}
	reader := &projectPageServerReader{
		origin:   base.authenticator.authentication.Context.Backend.OriginSHA256,
		project:  domain.BrokerJiraProjectIdentityV2{ID: "100", Key: "PROJ", Complete: true},
		identity: domain.BrokerJiraProjectPageIdentitySnapshotV2{StartAt: 0, MaxResults: 2, Total: 1, Issues: []domain.BrokerJiraProjectPageIssueIdentityV2{identity}, CoordinateExhausted: true, Complete: true},
		business: domain.BrokerJiraProjectPageSnapshotV2{StartAt: 0, MaxResults: 2, Total: 1, Issues: []domain.BrokerJiraProjectPageIssueSnapshotV2{{Identity: identity, Fields: []domain.BrokerJiraIssueReadField{{Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: "Synthetic project page"}}}}, CoordinateExhausted: true, Complete: true},
	}
	authorizer := &projectPageServerAuthorizer{nowMillis: base.baseTime.UnixMilli(), deny: deny}
	service, err := app.NewBrokerProjectPageService(authorizer, app.BrokerJiraProjectPageReader{Backend: base.authenticator.authentication.Context.Backend, Reader: reader})
	if err != nil {
		t.Fatal(err)
	}
	base.handler.projectPages = service
	body, err := brokercontract.EncodeProjectPageRequestV2(request)
	if err != nil {
		t.Fatal(err)
	}
	return projectPageServerFixture{base: base, authorizer: authorizer, reader: reader, request: request, body: body}
}

func projectPageTLSRequest(t *testing.T, handler http.Handler, body []byte) (bufferedBrokerHTTPResponse, []byte) {
	t.Helper()
	return familyDiscoveryTLSRequest(t, handler, brokertransport.ExecutePathV2, body)
}

func TestProjectPageRouteUsesInjectedServiceAndPreservesPhaseBoundaries(t *testing.T) {
	for _, test := range []struct {
		name         string
		deny         domain.BrokerAuthorizationPhase
		wantStatus   int
		wantPhases   []domain.BrokerAuthorizationPhase
		wantMetadata int32
		wantBusiness int32
	}{
		{name: "success", wantStatus: http.StatusOK, wantPhases: []domain.BrokerAuthorizationPhase{domain.BrokerPhaseAdmission, domain.BrokerPhaseQualificationAuthorization, domain.BrokerPhaseFinalAuthorization}, wantMetadata: 2, wantBusiness: 1},
		{name: "admission denial", deny: domain.BrokerPhaseAdmission, wantStatus: http.StatusForbidden, wantPhases: []domain.BrokerAuthorizationPhase{domain.BrokerPhaseAdmission}},
		{name: "qualification denial", deny: domain.BrokerPhaseQualificationAuthorization, wantStatus: http.StatusForbidden, wantPhases: []domain.BrokerAuthorizationPhase{domain.BrokerPhaseAdmission, domain.BrokerPhaseQualificationAuthorization}},
		{name: "final denial", deny: domain.BrokerPhaseFinalAuthorization, wantStatus: http.StatusForbidden, wantPhases: []domain.BrokerAuthorizationPhase{domain.BrokerPhaseAdmission, domain.BrokerPhaseQualificationAuthorization, domain.BrokerPhaseFinalAuthorization}, wantMetadata: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectPageServerFixture(t, test.deny)
			response, wire := projectPageTLSRequest(t, fixture.base.handler, fixture.body)
			projectCalls, identityCalls, businessCalls := fixture.reader.calls()
			if response.StatusCode != test.wantStatus || fixture.base.authenticator.calls != 1 || projectCalls+identityCalls != test.wantMetadata || businessCalls != test.wantBusiness || fixture.base.backendCalls.Load() != 0 || !reflect.DeepEqual(fixture.authorizer.phaseSnapshot(), test.wantPhases) {
				t.Fatalf("status=%d auth=%d phases=%v reads=(%d,%d,%d) v1_backend=%d body=%s", response.StatusCode, fixture.base.authenticator.calls, fixture.authorizer.phaseSnapshot(), projectCalls, identityCalls, businessCalls, fixture.base.backendCalls.Load(), wire)
			}
			if test.wantStatus == http.StatusOK {
				result, err := brokercontract.DecodeJiraProjectPageResultV2(wire)
				if err != nil || len(result.Issues) != 1 || result.Issues[0].Fields[0].Value != "Synthetic project page" || !result.Page.CoordinateExhausted || result.Page.SelectionComplete || response.Header.Get("X-ATL-Correlation-ID") == "" {
					t.Fatalf("result=%+v err=%v headers=%v", result, err, response.Header)
				}
			} else {
				failure, err := brokertransport.DecodeExecutionFailureV2(wire)
				if err != nil || failure.Reason != domain.BrokerReasonDenied {
					t.Fatalf("failure=%+v err=%v body=%s", failure, err, wire)
				}
			}
		})
	}
}

func TestProjectPageRouteFailsClosedOnMalformedStaleSiblingAndDataEcho(t *testing.T) {
	for _, test := range []struct {
		name         string
		mutate       func(*projectPageServerFixture)
		wantAuth     int
		wantMetadata int32
		wantBusiness int32
	}{
		{name: "malformed sibling request member", mutate: func(f *projectPageServerFixture) {
			f.body = bytes.Replace(f.body, []byte(`"project_key":"PROJ"`), []byte(`"project_key":"PROJ","policy":"injected"`), 1)
		}},
		{name: "stale context", wantAuth: 1, mutate: func(f *projectPageServerFixture) {
			f.request.Expect.AuthorityRevision = "stale-revision"
			f.body, _ = brokercontract.EncodeProjectPageRequestV2(f.request)
		}},
		{name: "unrequested sibling field", wantAuth: 1, wantMetadata: 2, wantBusiness: 1, mutate: func(f *projectPageServerFixture) {
			f.reader.business.Issues[0].Fields = append(f.reader.business.Issues[0].Fields, domain.BrokerJiraIssueReadField{Field: domain.BrokerJiraIssueFieldDescription, Present: true, Value: "sibling data"})
		}},
		{name: "workload credential in result", wantAuth: 1, wantMetadata: 2, wantBusiness: 1, mutate: func(f *projectPageServerFixture) {
			f.reader.business.Issues[0].Fields[0].Value = "synthetic-workload-credential"
		}},
		{name: "configured secret in result", wantAuth: 1, wantMetadata: 2, wantBusiness: 1, mutate: func(f *projectPageServerFixture) {
			f.reader.business.Issues[0].Fields[0].Value = "synthetic-sensitive-page-data"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			guard := [][]byte(nil)
			if test.name == "configured secret in result" {
				guard = [][]byte{[]byte("synthetic-sensitive-page-data")}
			}
			fixture := newProjectPageServerFixture(t, "", guard...)
			test.mutate(&fixture)
			response, wire := projectPageTLSRequest(t, fixture.base.handler, fixture.body)
			projectCalls, identityCalls, businessCalls := fixture.reader.calls()
			if response.StatusCode == http.StatusOK || fixture.base.authenticator.calls != test.wantAuth || projectCalls+identityCalls != test.wantMetadata || businessCalls != test.wantBusiness || fixture.base.backendCalls.Load() != 0 || bytes.Contains(wire, []byte("synthetic-workload-credential")) || bytes.Contains(wire, []byte("synthetic-sensitive-page-data")) || bytes.Contains(wire, []byte("sibling data")) {
				t.Fatalf("status=%d auth=%d reads=(%d,%d,%d) v1_backend=%d body=%s", response.StatusCode, fixture.base.authenticator.calls, projectCalls, identityCalls, businessCalls, fixture.base.backendCalls.Load(), wire)
			}
			failure, err := brokertransport.DecodeExecutionFailureV2(wire)
			if err != nil || failure.Reason == "" {
				t.Fatalf("failure=%+v err=%v body=%s", failure, err, wire)
			}
		})
	}
}

func TestProjectPageUnavailableDependencyAuthenticatesThenReturnsUnsupportedWithoutV1Fallback(t *testing.T) {
	fixture := newProjectPageServerFixture(t, "")
	fixture.base.handler.projectPages = nil
	response, wire := projectPageTLSRequest(t, fixture.base.handler, fixture.body)
	failure, err := brokertransport.DecodeExecutionFailureV2(wire)
	projectCalls, identityCalls, businessCalls := fixture.reader.calls()
	if response.StatusCode == http.StatusOK || err != nil || failure.Reason != domain.BrokerReasonUnsupported || fixture.base.authenticator.calls != 1 || fixture.base.authorizer.admissionCalls != 0 || fixture.base.backendCalls.Load() != 0 || projectCalls+identityCalls+businessCalls != 0 {
		t.Fatalf("status=%d failure=%+v err=%v auth=%d v1=%d v1_backend=%d reads=(%d,%d,%d)", response.StatusCode, failure, err, fixture.base.authenticator.calls, fixture.base.authorizer.admissionCalls, fixture.base.backendCalls.Load(), projectCalls, identityCalls, businessCalls)
	}
}

func TestProjectPageHostAuditRecordsSuccessfulAndDeniedOperation(t *testing.T) {
	for _, deny := range []domain.BrokerAuthorizationPhase{"", domain.BrokerPhaseFinalAuthorization} {
		t.Run(string(deny), func(t *testing.T) {
			fixture := newProjectPageServerFixture(t, deny)
			var output bytes.Buffer
			audit, err := NewAudit(&output, fixture.base.handler.guard)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			defer func() { _ = audit.Close(ctx) }()
			host := &Host{audit: audit}
			served := make(chan struct{})
			audited := host.auditedData(fixture.base.handler)
			response, _ := projectPageTLSRequest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(served)
				audited.ServeHTTP(w, r)
			}), fixture.body)
			select {
			case <-served:
			case <-ctx.Done():
				t.Fatal("audited handler did not complete")
			}
			if err := audit.Close(ctx); err != nil {
				t.Fatal(err)
			}
			var event AuditEvent
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
				t.Fatalf("missing valid operation audit: %v", err)
			}
			if !validAuditEvent(event) || event.Route != "data_execute_v2" || event.Operation != string(domain.BrokerOperationJiraProjectIssuePageRead) || event.Outcome != auditOutcome(response.StatusCode) || event.ResponseBytes == 0 {
				t.Fatalf("unexpected event: %+v", event)
			}
			if bytes.Contains(output.Bytes(), []byte("PROJ")) || bytes.Contains(output.Bytes(), []byte("Synthetic project page")) || bytes.Contains(output.Bytes(), []byte("synthetic-workload-credential")) {
				t.Fatal("audit disclosed protected request or response content")
			}
		})
	}
}
