package brokerserver

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/adapter/brokerauthority"
	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type serverAuthenticatorStub struct {
	authentication brokertransport.Authentication
	err            error
	calls          int
	started        chan struct{}
	release        chan struct{}
}

type deadlineResponseWriter struct {
	header        http.Header
	body          bytes.Buffer
	status        int
	writeDeadline time.Time
	readDeadline  time.Time
}

func (w *deadlineResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *deadlineResponseWriter) Write(body []byte) (int, error) {
	return w.body.Write(body)
}

func (w *deadlineResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *deadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.writeDeadline = deadline
	return nil
}

func (w *deadlineResponseWriter) SetReadDeadline(deadline time.Time) error {
	w.readDeadline = deadline
	return nil
}

func (*deadlineResponseWriter) FlushError() error { return nil }

func (s *serverAuthenticatorStub) Authenticate(_ context.Context, credential []byte, challenge brokertransport.AuthenticationChallenge) (brokertransport.Authentication, error) {
	s.calls++
	if string(credential) != "synthetic-workload-credential" || challenge.Audience != "atl-broker" || challenge.BrokerID != "broker-1" || len(challenge.Nonce) != 32 {
		return brokertransport.Authentication{}, domain.ErrAuth
	}
	if s.started != nil {
		close(s.started)
		<-s.release
	}
	return s.authentication, s.err
}

type serverAuthorizerStub struct {
	admissionCalls  int
	nowMillis       int64
	denyPhase       domain.BrokerAuthorizationPhase
	discoveryAccess domain.BrokerDiscoveryAccess
	discoveryReason domain.BrokerReason
	discoveryCalls  int
	discoveryMutate func(*domain.BrokerDiscoveryProjectionV2)
}

func (a *serverAuthorizerStub) Admit(_ context.Context, request domain.BrokerAdmissionRequest) (domain.BrokerAdmissionDecision, error) {
	a.admissionCalls++
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Context)
	requestSHA256, _ := brokercontract.AdmissionRequestSHA256(request)
	value := domain.BrokerAdmissionDecision{BrokerDecisionCore: a.core(domain.BrokerPhaseAdmission, contextSHA256, requestSHA256, request.Context.AuthorityRevision)}
	wire, err := brokercontract.EncodeAdmissionDecisionV1(value)
	if err != nil {
		return domain.BrokerAdmissionDecision{}, err
	}
	return brokercontract.DecodeAdmissionDecisionV1(wire)
}

func (a *serverAuthorizerStub) AuthorizeQualification(_ context.Context, request domain.BrokerQualificationRequest) (domain.BrokerQualificationDecision, error) {
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Admission.Context)
	requestSHA256, _ := brokercontract.QualificationRequestSHA256(request)
	admissionSHA256, _ := brokercontract.AdmissionRequestSHA256(request.Admission)
	planSHA256, _ := brokercontract.QualificationPlanSHA256(request.Plan)
	value := domain.BrokerQualificationDecision{BrokerDecisionCore: a.core(domain.BrokerPhaseQualificationAuthorization, contextSHA256, requestSHA256, request.Admission.Context.AuthorityRevision), AdmissionRequestSHA256: admissionSHA256, AdmissionDecisionSHA256: request.AdmissionDecision.DecisionSHA256, PlanSHA256: planSHA256}
	wire, err := brokercontract.EncodeQualificationDecisionV1(value)
	if err != nil {
		return domain.BrokerQualificationDecision{}, err
	}
	return brokercontract.DecodeQualificationDecisionV1(wire)
}

func (a *serverAuthorizerStub) AuthorizeOperation(_ context.Context, request domain.BrokerOperationAuthorizationRequest) (domain.BrokerOperationDecision, error) {
	admission := request.QualificationRequest.Admission
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(admission.Context)
	requestSHA256, _ := brokercontract.OperationAuthorizationRequestSHA256(request)
	resourcesSHA256, _ := brokercontract.QualifiedResourcesSHA256(request.QualifiedResources)
	effectsSHA256, _ := brokercontract.EffectsSHA256(request.Effects)
	value := domain.BrokerOperationDecision{BrokerDecisionCore: a.core(domain.BrokerPhaseFinalAuthorization, contextSHA256, requestSHA256, admission.Context.AuthorityRevision), QualificationDecisionSHA256: request.QualificationDecision.DecisionSHA256, Operation: admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256, ResourcesSHA256: resourcesSHA256, EffectsSHA256: effectsSHA256}
	wire, err := brokercontract.EncodeOperationDecisionV1(value)
	if err != nil {
		return domain.BrokerOperationDecision{}, err
	}
	return brokercontract.DecodeOperationDecisionV1(wire)
}

func (*serverAuthorizerStub) AuthorizeProposal(context.Context, domain.BrokerProposalAuthorizationRequest) (domain.BrokerProposalClearance, error) {
	return domain.BrokerProposalClearance{}, domain.ErrUsage
}

func (a *serverAuthorizerStub) core(phase domain.BrokerAuthorizationPhase, contextSHA256, requestSHA256, revision string) domain.BrokerDecisionCore {
	status, reason := domain.BrokerDecisionAllowed, domain.BrokerReason("")
	if a.denyPhase == phase {
		status, reason = domain.BrokerDecisionDenied, domain.BrokerReasonDenied
	}
	return domain.BrokerDecisionCore{Status: status, Reason: reason, DecisionID: string(phase) + "-1", AuthorityRevision: revision, ContextSHA256: contextSHA256, RequestSHA256: requestSHA256, IssuedAtMillis: a.nowMillis, ExpiresAtMillis: a.nowMillis + 5000}
}

type brokerServerFixture struct {
	handler       *Handler
	authenticator *serverAuthenticatorStub
	backend       *httptest.Server
	backendCalls  *atomic.Int32
	requestBody   []byte
	baseTime      time.Time
	authorizer    *serverAuthorizerStub
}

func newBrokerServerFixture(t *testing.T, summary string, denyPhase domain.BrokerAuthorizationPhase, authErr error, guardCredentials ...[]byte) brokerServerFixture {
	t.Helper()
	baseTime := time.Now().UTC().Truncate(time.Millisecond)
	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		backendCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.RequestURI() {
		case "/rest/api/2/issue/PROJ-7?fields=project%2Cupdated":
			_, _ = io.WriteString(writer, `{"id":"10001","key":"PROJ-7","fields":{"project":{"key":"PROJ"},"updated":"2026-09-08T10:00:00Z"}}`)
		case "/rest/api/2/issue/10001?fields=project%2Csummary%2Cupdated":
			_, _ = io.WriteString(writer, `{"id":"10001","key":"PROJ-7","fields":{"project":{"key":"PROJ"},"summary":`+strconvQuote(summary)+`,"updated":"2026-09-08T10:00:00Z"}}`)
		default:
			t.Errorf("unexpected backend request %s", request.URL.RequestURI())
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(backend.Close)
	reader := jiraadapter.New(backend.URL, "synthetic-upstream-pat", "test")
	origin, err := reader.BrokerOriginSHA256()
	if err != nil {
		t.Fatal(err)
	}
	binding := domain.BrokerBackendBinding{Service: "jira", OriginSHA256: origin, WorkloadBackendID: "jira-primary"}
	verified := domain.BrokerVerifiedContext{PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", ExecutionNotBeforeMillis: baseTime.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: baseTime.Add(time.Minute).UnixMilli(), GrantExpiresMillis: baseTime.Add(time.Minute).UnixMilli(), CredentialExpiresMillis: baseTime.Add(time.Minute).UnixMilli(), Backend: binding}
	authenticator := &serverAuthenticatorStub{authentication: brokertransport.Authentication{Context: verified, ReleaseDeadline: baseTime.Add(5 * time.Second)}, err: authErr}
	authorizer := &serverAuthorizerStub{nowMillis: baseTime.UnixMilli(), denyPhase: denyPhase}
	reads, err := app.NewBrokerReadService(authorizer, app.BrokerJiraIssueReader{Backend: binding, Reader: reader}, app.BrokerConfluencePageReader{})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewCredentialGuard(guardCredentials...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Close)
	handler, err := New(Config{Audience: "atl-broker", BrokerID: "broker-1", MaxConcurrent: 1}, Dependencies{Authenticator: authenticator, Reads: reads, Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	requestValue := domain.BrokerRequest{SchemaVersion: 1, Operation: domain.BrokerOperationJiraIssueRead, OperationVersion: 1, RequestID: "untrusted-request-1", Features: []string{}, Expect: domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"}, Arguments: domain.BrokerOperationArguments{JiraIssueRead: &domain.BrokerJiraIssueReadArguments{IssueKey: "PROJ-7", Fields: []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary}}}}
	requestBody, err := brokercontract.EncodeRequestV1(requestValue)
	if err != nil {
		t.Fatal(err)
	}
	return brokerServerFixture{handler: handler, authenticator: authenticator, backend: backend, backendCalls: &backendCalls, requestBody: requestBody, baseTime: baseTime, authorizer: authorizer}
}

func TestBrokerServerExactReadBoundaries(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        func([]byte) []byte
		authErr     error
		denyPhase   domain.BrokerAuthorizationPhase
		wantStatus  int
		wantAuth    int
		wantBackend int32
	}{
		{name: "malformed", body: func([]byte) []byte { return []byte(`{}`) }, wantStatus: http.StatusBadRequest},
		{name: "authentication", authErr: domain.ErrAuth, wantStatus: http.StatusUnauthorized, wantAuth: 1},
		{name: "qualification denied", denyPhase: domain.BrokerPhaseQualificationAuthorization, wantStatus: http.StatusForbidden, wantAuth: 1},
		{name: "final denied", denyPhase: domain.BrokerPhaseFinalAuthorization, wantStatus: http.StatusForbidden, wantAuth: 1, wantBackend: 1},
		{name: "allow", wantStatus: http.StatusOK, wantAuth: 1, wantBackend: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBrokerServerFixture(t, "Synthetic summary", test.denyPhase, test.authErr)
			body := fixture.requestBody
			if test.body != nil {
				body = test.body(body)
			}
			response := brokerRequest(t, fixture.handler, http.MethodPost, ExecutePath, body, true)
			defer response.Body.Close()
			responseBody, _ := io.ReadAll(response.Body)
			if response.StatusCode != test.wantStatus || fixture.authenticator.calls != test.wantAuth || fixture.backendCalls.Load() != test.wantBackend {
				t.Fatalf("status=%d auth=%d backend=%d body=%s", response.StatusCode, fixture.authenticator.calls, fixture.backendCalls.Load(), responseBody)
			}
			if test.wantStatus == http.StatusOK {
				result, err := brokercontract.DecodeJiraIssueReadResultV1(responseBody)
				if err != nil || len(result.Fields) != 1 || result.Fields[0].Value != "Synthetic summary" || response.Header.Get("X-ATL-Correlation-ID") == "" {
					t.Fatalf("result=%+v err=%v headers=%v", result, err, response.Header)
				}
			} else if _, err := brokertransport.DecodeFailureV1(responseBody); err != nil {
				t.Fatalf("failure=%s err=%v", responseBody, err)
			}
		})
	}
}

func TestBrokerServerProtocolRequiresAuthentication(t *testing.T) {
	fixture := newBrokerServerFixture(t, "Synthetic summary", "", nil)
	response := brokerRequest(t, fixture.handler, http.MethodGet, ProtocolPath, nil, true)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	protocol, err := brokertransport.DecodeProtocolV1(body)
	if response.StatusCode != http.StatusOK || err != nil || len(protocol.Operations) != 2 || fixture.authenticator.calls != 1 || fixture.backendCalls.Load() != 0 {
		t.Fatalf("status=%d protocol=%+v err=%v auth=%d backend=%d", response.StatusCode, protocol, err, fixture.authenticator.calls, fixture.backendCalls.Load())
	}
	response = brokerRequest(t, fixture.handler, http.MethodGet, ProtocolPath, nil, false)
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || fixture.authenticator.calls != 1 {
		t.Fatalf("status=%d auth=%d", response.StatusCode, fixture.authenticator.calls)
	}
}

func TestBrokerServerPublishHonorsRequestCancellationAndEarlierDeadline(t *testing.T) {
	baseTime := time.Now()
	handler := &Handler{guard: &CredentialGuard{}, now: func() time.Time { return baseTime }}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	canceledWriter := &deadlineResponseWriter{}
	started, err := handler.publish(canceled, canceledWriter, []byte(`{}`), baseTime.Add(5*time.Second), "")
	if started || !errors.Is(err, context.Canceled) || canceledWriter.status != 0 || len(canceledWriter.header) != 0 || !canceledWriter.writeDeadline.IsZero() {
		t.Fatalf("started=%t err=%v writer=%+v", started, err, canceledWriter)
	}

	requestDeadline := baseTime.Add(2 * time.Second)
	bounded, cancel := context.WithDeadline(context.Background(), requestDeadline)
	defer cancel()
	boundedWriter := &deadlineResponseWriter{}
	started, err = handler.publish(bounded, boundedWriter, []byte(`{}`), baseTime.Add(5*time.Second), "")
	if !started || err != nil || boundedWriter.status != http.StatusOK || boundedWriter.body.String() != `{}` || !boundedWriter.writeDeadline.Equal(requestDeadline) {
		t.Fatalf("started=%t err=%v writer=%+v", started, err, boundedWriter)
	}
}

func TestBrokerServerRefusesCredentialEchoAndExpiredRelease(t *testing.T) {
	for _, test := range []struct {
		name    string
		summary string
		guard   [][]byte
		expire  bool
	}{
		{name: "workload credential", summary: "synthetic-workload-credential"},
		{name: "upstream credential", summary: "synthetic-upstream-pat", guard: [][]byte{[]byte("synthetic-upstream-pat")}},
		{name: "release expired", summary: "Synthetic summary", expire: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBrokerServerFixture(t, test.summary, "", nil, test.guard...)
			if test.expire {
				fixture.handler.now = func() time.Time {
					if fixture.backendCalls.Load() >= 2 {
						return fixture.baseTime.Add(6 * time.Second)
					}
					return fixture.baseTime
				}
			}
			response := brokerRequest(t, fixture.handler, http.MethodPost, ExecutePath, fixture.requestBody, true)
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != http.StatusServiceUnavailable || bytes.Contains(body, []byte(test.summary)) || bytes.Contains(body, []byte("synthetic-workload-credential")) || bytes.Contains(body, []byte("synthetic-upstream-pat")) {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
			if _, err := brokertransport.DecodeFailureV1(body); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBrokerServerRejectsExpandedRoutesAndHeadersBeforeAuthentication(t *testing.T) {
	fixture := newBrokerServerFixture(t, "Synthetic summary", "", nil)
	for _, target := range []string{ExecutePath + "?debug=1", ExecutePath + "/", "/v1/unknown"} {
		response := brokerRequest(t, fixture.handler, http.MethodPost, target, fixture.requestBody, true)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("target=%q status=%d", target, response.StatusCode)
		}
	}
	request := httptest.NewRequest(http.MethodPost, ExecutePath, bytes.NewReader(fixture.requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Add("Authorization", "Bearer synthetic-workload-credential")
	request.Header.Add("Authorization", "Bearer second-credential")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || fixture.authenticator.calls != 0 || fixture.backendCalls.Load() != 0 {
		t.Fatalf("status=%d auth=%d backend=%d", recorder.Code, fixture.authenticator.calls, fixture.backendCalls.Load())
	}
	response := brokerRequest(t, fixture.handler, http.MethodPost, ExecutePath, bytes.Repeat([]byte{' '}, int(MaxExecuteRequestBytes)+1), true)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || fixture.authenticator.calls != 0 || fixture.backendCalls.Load() != 0 {
		t.Fatalf("oversized status=%d auth=%d backend=%d", response.StatusCode, fixture.authenticator.calls, fixture.backendCalls.Load())
	}
}

func TestBrokerServerDoesNotEchoCredentialThatMatchesFailureVocabulary(t *testing.T) {
	fixture := newBrokerServerFixture(t, "Synthetic summary", domain.BrokerPhaseFinalAuthorization, nil, []byte("request_access"))
	response := brokerRequest(t, fixture.handler, http.MethodPost, ExecutePath, fixture.requestBody, true)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusServiceUnavailable || len(body) != 0 || bytes.Contains(body, []byte("request_access")) {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}
}

func TestBrokerServerRejectsOverloadBeforeSecondAuthentication(t *testing.T) {
	fixture := newBrokerServerFixture(t, "Synthetic summary", "", nil)
	fixture.authenticator.started = make(chan struct{})
	fixture.authenticator.release = make(chan struct{})
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	firstDone := make(chan *http.Response, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, server.URL+ExecutePath, bytes.NewReader(fixture.requestBody))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
		response, _ := server.Client().Do(request)
		if response != nil {
			response.Body.Close()
		}
		firstDone <- response
	}()
	<-fixture.authenticator.started
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+ExecutePath, bytes.NewReader(fixture.requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	second, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusServiceUnavailable || fixture.authenticator.calls != 1 || fixture.backendCalls.Load() != 0 {
		t.Fatalf("status=%d auth=%d backend=%d", second.StatusCode, fixture.authenticator.calls, fixture.backendCalls.Load())
	}
	close(fixture.authenticator.release)
	first := <-firstDone
	if first == nil {
		t.Fatal("first request failed")
	}
	first.Body.Close()
}

func TestBrokerServerAuthorityAndBackendHTTPChain(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		backendCalls.Add(1)
		switch request.URL.RequestURI() {
		case "/rest/api/2/issue/PROJ-7?fields=project%2Cupdated":
			_, _ = io.WriteString(writer, `{"id":"10001","key":"PROJ-7","fields":{"project":{"key":"PROJ"},"updated":"2026-09-08T10:00:00Z"}}`)
		case "/rest/api/2/issue/10001?fields=project%2Csummary%2Cupdated":
			_, _ = io.WriteString(writer, `{"id":"10001","key":"PROJ-7","fields":{"project":{"key":"PROJ"},"summary":"Synthetic summary","updated":"2026-09-08T10:00:00Z"}}`)
		default:
			t.Errorf("unexpected backend request %s", request.URL.RequestURI())
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	reader := jiraadapter.New(backend.URL, "synthetic-upstream-pat", "test")
	origin, err := reader.BrokerOriginSHA256()
	if err != nil {
		t.Fatal(err)
	}
	binding := domain.BrokerBackendBinding{Service: "jira", OriginSHA256: origin, WorkloadBackendID: "jira-primary"}
	verified := domain.BrokerVerifiedContext{PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(time.Minute).UnixMilli(), GrantExpiresMillis: now.Add(time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(time.Minute).UnixMilli(), Backend: binding}
	issuer := strings.Repeat("a", 64)
	decisionSource := &serverAuthorizerStub{nowMillis: now.UnixMilli()}
	var authorityCalls atomic.Int32
	authorityServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorityCalls.Add(1)
		if request.Header.Get("Authorization") != "Bearer synthetic-server-credential" {
			t.Error("authority credential missing")
		}
		body, _ := io.ReadAll(request.Body)
		switch request.URL.Path {
		case "/v1/authenticate":
			value, decodeErr := brokertransport.DecodeAuthenticationRequestV1(body)
			credential := value.Credential()
			defer clear(credential)
			if decodeErr != nil || string(credential) != "synthetic-workload-credential" {
				t.Errorf("authentication=%+v err=%v", value, decodeErr)
			}
			encoded, _ := brokertransport.EncodeAuthenticationResponseV1(brokertransport.AuthenticationResponse{SchemaVersion: 1, Nonce: value.Nonce, CredentialSHA256: value.CredentialSHA256, IssuerSHA256: issuer, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli(), Context: verified})
			_, _ = writer.Write(encoded)
		case "/v1/authorize/admission":
			value, _ := brokercontract.DecodeAdmissionRequestV1(body)
			decision, _ := decisionSource.Admit(request.Context(), value)
			encoded, _ := brokercontract.EncodeAdmissionDecisionV1(decision)
			_, _ = writer.Write(encoded)
		case "/v1/authorize/qualification":
			value, _ := brokercontract.DecodeQualificationRequestV1(body)
			decision, _ := decisionSource.AuthorizeQualification(request.Context(), value)
			encoded, _ := brokercontract.EncodeQualificationDecisionV1(decision)
			_, _ = writer.Write(encoded)
		case "/v1/authorize/operation":
			value, _ := brokercontract.DecodeOperationAuthorizationRequestV1(body)
			decision, _ := decisionSource.AuthorizeOperation(request.Context(), value)
			encoded, _ := brokercontract.EncodeOperationDecisionV1(decision)
			_, _ = writer.Write(encoded)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer authorityServer.Close()
	caPath := filepath.Join(t.TempDir(), "authority-ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: authorityServer.Certificate().Raw})
	if err := os.WriteFile(caPath, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	authority, err := brokerauthority.New(brokerauthority.Config{BaseURL: authorityServer.URL, ServerCredential: "synthetic-server-credential", IssuerSHA256: issuer, Version: "test", TLS: httpx.TLSOptions{CABundle: caPath}})
	if err != nil {
		t.Fatal(err)
	}
	reads, err := app.NewBrokerReadService(authority, app.BrokerJiraIssueReader{Backend: binding, Reader: reader}, app.BrokerConfluencePageReader{})
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := NewCredentialGuard([]byte("synthetic-upstream-pat"), []byte("synthetic-server-credential"))
	defer guard.Close()
	handler, err := New(Config{Audience: "atl-broker", BrokerID: "broker-1", MaxConcurrent: 1}, Dependencies{Authenticator: authority, Reads: reads, Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	requestValue := domain.BrokerRequest{SchemaVersion: 1, Operation: domain.BrokerOperationJiraIssueRead, OperationVersion: 1, RequestID: "request-1", Features: []string{}, Expect: domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"}, Arguments: domain.BrokerOperationArguments{JiraIssueRead: &domain.BrokerJiraIssueReadArguments{IssueKey: "PROJ-7", Fields: []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary}}}}
	requestBody, _ := brokercontract.EncodeRequestV1(requestValue)
	response := brokerRequest(t, handler, http.MethodPost, ExecutePath, requestBody, true)
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(response.Body)
	result, decodeErr := brokercontract.DecodeJiraIssueReadResultV1(responseBody)
	if response.StatusCode != http.StatusOK || decodeErr != nil || len(result.Fields) != 1 || authorityCalls.Load() != 4 || backendCalls.Load() != 2 {
		t.Fatalf("status=%d result=%+v err=%v authority=%d backend=%d body=%s", response.StatusCode, result, decodeErr, authorityCalls.Load(), backendCalls.Load(), responseBody)
	}
}

func brokerRequest(t *testing.T, handler http.Handler, method, target string, body []byte, authenticated bool) *http.Response {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	request, err := http.NewRequestWithContext(t.Context(), method, server.URL+target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func strconvQuote(value string) string {
	return strconv.Quote(value)
}
