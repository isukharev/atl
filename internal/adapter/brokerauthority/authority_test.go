package brokerauthority

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func TestAuthorityAuthenticationAndDecisionPhasesUseExactRoutes(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	verified := authorityVerifiedContext(now)
	issuer := strings.Repeat("a", 64)
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer synthetic-server-credential" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request %s %s headers=%v", request.Method, request.URL.Path, request.Header)
		}
		body, _ := io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case authenticatePath:
			authRequest, err := brokertransport.DecodeAuthenticationRequestV1(body)
			credential := authRequest.Credential()
			defer clear(credential)
			if err != nil || string(credential) != "synthetic-workload-credential" {
				t.Errorf("authentication request=%+v err=%v", authRequest, err)
			}
			response := brokertransport.AuthenticationResponse{SchemaVersion: 1, Nonce: authRequest.Nonce, CredentialSHA256: authRequest.CredentialSHA256, IssuerSHA256: issuer, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli(), Context: verified}
			encoded, _ := brokertransport.EncodeAuthenticationResponseV1(response)
			_, _ = writer.Write(encoded)
		case admissionPath:
			value, err := brokercontract.DecodeAdmissionRequestV1(body)
			if err != nil {
				t.Error(err)
			}
			encoded, _ := brokercontract.EncodeAdmissionDecisionV1(authorityAdmissionDecision(value, now.UnixMilli()))
			_, _ = writer.Write(encoded)
		case qualificationPath:
			value, err := brokercontract.DecodeQualificationRequestV1(body)
			if err != nil {
				t.Error(err)
			}
			encoded, _ := brokercontract.EncodeQualificationDecisionV1(authorityQualificationDecision(value, now.UnixMilli()))
			_, _ = writer.Write(encoded)
		case operationPath:
			value, err := brokercontract.DecodeOperationAuthorizationRequestV1(body)
			if err != nil {
				t.Error(err)
			}
			encoded, _ := brokercontract.EncodeOperationDecisionV1(authorityOperationDecision(value, now.UnixMilli()))
			_, _ = writer.Write(encoded)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	authority := newTestAuthority(t, server, issuer)
	authority.now = func() time.Time { return now }
	authentication, err := authority.Authenticate(t.Context(), []byte("synthetic-workload-credential"), brokertransport.AuthenticationChallenge{Nonce: authorityNonce(), Audience: "atl-broker", BrokerID: "broker-1"})
	if err != nil || !reflect.DeepEqual(authentication.Context, verified) || !authentication.ReleaseDeadline.Equal(now.Add(5*time.Second)) {
		t.Fatalf("authentication=%+v err=%v", authentication, err)
	}
	admissionRequest := authorityAdmissionRequest(t, verified, now)
	admission, err := authority.Admit(t.Context(), admissionRequest)
	qualificationRequest := authorityQualificationRequest(t, admissionRequest, admission)
	qualification, qualificationErr := authority.AuthorizeQualification(t.Context(), qualificationRequest)
	operationRequest := authorityOperationRequest(t, qualificationRequest, qualification)
	operation, operationErr := authority.AuthorizeOperation(t.Context(), operationRequest)
	if err != nil || qualificationErr != nil || operationErr != nil || admission.Status != domain.BrokerDecisionAllowed || qualification.Status != domain.BrokerDecisionAllowed || operation.Status != domain.BrokerDecisionAllowed {
		t.Fatalf("decisions=%+v/%+v/%+v errors=%v/%v/%v", admission, qualification, operation, err, qualificationErr, operationErr)
	}
	if !reflect.DeepEqual(paths, []string{authenticatePath, admissionPath, qualificationPath, operationPath}) {
		t.Fatalf("paths=%v", paths)
	}
}

func TestAuthorityRefusesRedirectsAndSanitizesResponses(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				writer.Header().Set("Location", "/private-redirect-canary")
				writer.WriteHeader(status)
				_, _ = io.WriteString(writer, "private-authority-response-canary")
			}))
			defer server.Close()
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			_, err := authority.Authenticate(t.Context(), []byte("synthetic-workload-credential"), brokertransport.AuthenticationChallenge{Nonce: authorityNonce(), Audience: "atl-broker", BrokerID: "broker-1"})
			if err == nil || requests.Load() != 1 {
				t.Fatalf("err=%v requests=%d", err, requests.Load())
			}
			var apiError *httpx.APIError
			if errors.As(err, &apiError) {
				t.Fatal("raw authority response remained reachable")
			}
			for _, formatted := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprint(errors.Unwrap(err))} {
				if strings.Contains(formatted, "canary") || strings.Contains(formatted, server.URL) || strings.Contains(formatted, authenticatePath) {
					t.Fatalf("unsafe error=%q", formatted)
				}
			}
		})
	}
}

func TestAuthorityRejectsCrossBoundAuthenticationAndProposalWithoutIO(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	issuer := strings.Repeat("a", 64)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		body, _ := io.ReadAll(request.Body)
		value, _ := brokertransport.DecodeAuthenticationRequestV1(body)
		response := brokertransport.AuthenticationResponse{SchemaVersion: 1, Nonce: "eHh4eHh4eHh4eHh4eHh4eHh4eHh4eHh4", CredentialSHA256: value.CredentialSHA256, IssuerSHA256: issuer, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli(), Context: authorityVerifiedContext(now)}
		encoded, _ := brokertransport.EncodeAuthenticationResponseV1(response)
		_, _ = writer.Write(encoded)
	}))
	defer server.Close()
	authority := newTestAuthority(t, server, issuer)
	authority.now = func() time.Time { return now }
	if _, err := authority.Authenticate(t.Context(), []byte("synthetic-workload-credential"), brokertransport.AuthenticationChallenge{Nonce: authorityNonce(), Audience: "atl-broker", BrokerID: "broker-1"}); !errors.Is(err, domain.ErrAuth) || requests.Load() != 1 {
		t.Fatalf("err=%v requests=%d", err, requests.Load())
	}
	if _, err := authority.AuthorizeProposal(context.Background(), domain.BrokerProposalAuthorizationRequest{}); !errors.Is(err, domain.ErrCheckFailed) || requests.Load() != 1 {
		t.Fatalf("proposal err=%v requests=%d", err, requests.Load())
	}
}

func TestAuthorityConfigurationRequiresFixedHTTPSOrigin(t *testing.T) {
	for _, base := range []string{"", "http://127.0.0.1:8443", "https://user@example.invalid", "https://example.invalid?authority=other", "https://example.invalid/#fragment", " https://example.invalid"} {
		if authority, err := New(Config{BaseURL: base, ServerCredential: "synthetic-server-credential", IssuerSHA256: strings.Repeat("a", 64), Version: "test"}); !errors.Is(err, domain.ErrConfig) || authority != nil {
			t.Fatalf("base=%q authority=%v err=%v", base, authority, err)
		}
	}
}

func newTestAuthority(t *testing.T, server *httptest.Server, issuer string) *Authority {
	return newTestAuthorityWithScheduler(t, server, issuer, nil)
}

func newTestAuthorityWithScheduler(t *testing.T, server *httptest.Server, issuer string, scheduler *httpx.Scheduler) *Authority {
	t.Helper()
	caPath := filepath.Join(t.TempDir(), "authority-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	authority, err := New(Config{BaseURL: server.URL, ServerCredential: "synthetic-server-credential", IssuerSHA256: issuer, Version: "test", Scheduler: scheduler, TLS: httpx.TLSOptions{CABundle: caPath}})
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func authorityVerifiedContext(now time.Time) domain.BrokerVerifiedContext {
	return domain.BrokerVerifiedContext{PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(time.Minute).UnixMilli(), GrantExpiresMillis: now.Add(time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(time.Minute).UnixMilli(), Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("b", 64), WorkloadBackendID: "jira-primary"}}
}

func authorityNonce() string {
	return "bm5ubm5ubm5ubm5ubm5ubm5ubm5ubm5u"
}

func authorityAdmissionRequest(t *testing.T, verified domain.BrokerVerifiedContext, now time.Time) domain.BrokerAdmissionRequest {
	t.Helper()
	request := domain.BrokerRequest{SchemaVersion: 1, Operation: domain.BrokerOperationJiraIssueRead, OperationVersion: 1, RequestID: "request-1", Features: []string{}, Expect: domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"}, Arguments: domain.BrokerOperationArguments{JiraIssueRead: &domain.BrokerJiraIssueReadArguments{IssueKey: "PROJ-7", Fields: []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary}}}}
	digest, err := brokercontract.ArgumentsSHA256(request)
	if err != nil {
		t.Fatal(err)
	}
	return domain.BrokerAdmissionRequest{Context: verified, Operation: request.Operation, OperationVersion: 1, RequestID: request.RequestID, Features: []string{}, Arguments: request.Arguments, ArgumentsSHA256: digest, DeadlineMillis: now.Add(time.Minute).UnixMilli()}
}

func authorityAdmissionDecision(request domain.BrokerAdmissionRequest, nowMillis int64) domain.BrokerAdmissionDecision {
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Context)
	requestSHA256, _ := brokercontract.AdmissionRequestSHA256(request)
	return domain.BrokerAdmissionDecision{BrokerDecisionCore: authorityCore(domain.BrokerPhaseAdmission, contextSHA256, requestSHA256, request.Context.AuthorityRevision, nowMillis)}
}

func authorityQualificationRequest(t *testing.T, admission domain.BrokerAdmissionRequest, decision domain.BrokerAdmissionDecision) domain.BrokerQualificationRequest {
	t.Helper()
	definition, _ := brokercontract.Definition(admission.Operation, admission.OperationVersion)
	return domain.BrokerQualificationRequest{Admission: admission, AdmissionDecision: decision, Plan: domain.BrokerQualificationPlan{SelectorSHA256: admission.ArgumentsSHA256, MetadataFields: definition.QualificationFields, Limits: definition.Limits.Qualification}}
}

func authorityQualificationDecision(request domain.BrokerQualificationRequest, nowMillis int64) domain.BrokerQualificationDecision {
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Admission.Context)
	requestSHA256, _ := brokercontract.QualificationRequestSHA256(request)
	admissionSHA256, _ := brokercontract.AdmissionRequestSHA256(request.Admission)
	planSHA256, _ := brokercontract.QualificationPlanSHA256(request.Plan)
	return domain.BrokerQualificationDecision{BrokerDecisionCore: authorityCore(domain.BrokerPhaseQualificationAuthorization, contextSHA256, requestSHA256, request.Admission.Context.AuthorityRevision, nowMillis), AdmissionRequestSHA256: admissionSHA256, AdmissionDecisionSHA256: request.AdmissionDecision.DecisionSHA256, PlanSHA256: planSHA256}
}

func authorityOperationRequest(t *testing.T, qualification domain.BrokerQualificationRequest, decision domain.BrokerQualificationDecision) domain.BrokerOperationAuthorizationRequest {
	t.Helper()
	identity := domain.BrokerJiraIssueIdentity{ID: "10001", Key: "PROJ-7", Project: "PROJ", Updated: "2026-09-08T10:00:00Z", Complete: true}
	version, projection, err := brokercontract.JiraIssueIdentityEvidenceSHA256V1(identity)
	if err != nil {
		t.Fatal(err)
	}
	resource := domain.BrokerQualifiedResource{Kind: domain.BrokerResourceJiraIssue, ImmutableID: identity.ID, Key: identity.Key, Project: identity.Project, AncestorIDs: []string{}, VersionEvidence: version, ProjectionSHA256: projection}
	return domain.BrokerOperationAuthorizationRequest{QualificationRequest: qualification, QualificationDecision: decision, QualifiedResources: []domain.BrokerQualifiedResource{resource}, Effects: []domain.BrokerEffect{{Kind: domain.BrokerEffectRead, Resource: resource, Fields: []string{"summary"}}}}
}

func authorityOperationDecision(request domain.BrokerOperationAuthorizationRequest, nowMillis int64) domain.BrokerOperationDecision {
	admission := request.QualificationRequest.Admission
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(admission.Context)
	requestSHA256, _ := brokercontract.OperationAuthorizationRequestSHA256(request)
	resourcesSHA256, _ := brokercontract.QualifiedResourcesSHA256(request.QualifiedResources)
	effectsSHA256, _ := brokercontract.EffectsSHA256(request.Effects)
	return domain.BrokerOperationDecision{BrokerDecisionCore: authorityCore(domain.BrokerPhaseFinalAuthorization, contextSHA256, requestSHA256, admission.Context.AuthorityRevision, nowMillis), QualificationDecisionSHA256: request.QualificationDecision.DecisionSHA256, Operation: admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256, ResourcesSHA256: resourcesSHA256, EffectsSHA256: effectsSHA256}
}

func authorityCore(phase domain.BrokerAuthorizationPhase, contextSHA256, requestSHA256, revision string, nowMillis int64) domain.BrokerDecisionCore {
	return domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: string(phase) + "-1", AuthorityRevision: revision, ContextSHA256: contextSHA256, RequestSHA256: requestSHA256, IssuedAtMillis: nowMillis, ExpiresAtMillis: nowMillis + 5000}
}
