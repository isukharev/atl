package brokerserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

const (
	attachmentChainWorkload  = "synthetic-attachment-workload"
	attachmentChainAuthority = "synthetic-attachment-authority"
	attachmentChainBackend   = "synthetic-attachment-backend"
)

type attachmentChainFixture struct {
	mu                   sync.Mutex
	counts               map[string]int
	nonces               map[string]bool
	violations           int
	verified             domain.BrokerVerifiedContext
	issuer               string
	payload              []byte
	denyRelease          int
	metadataDrift        int
	sourceMode           string
	bodyEntered          chan struct{}
	bodyCancelled        chan struct{}
	revokeAuthentication int
	expireAuthentication int
	releaseHook          func(int)
	handler              *Handler
	request              domain.BrokerAttachmentRequestV3
}

func newAttachmentChainFixture(t *testing.T, payload []byte) *attachmentChainFixture {
	t.Helper()
	f := &attachmentChainFixture{counts: make(map[string]int), nonces: make(map[string]bool), issuer: strings.Repeat("a", 64), payload: bytes.Clone(payload)}
	backend := httptest.NewTLSServer(http.HandlerFunc(f.serveJira))
	t.Cleanup(backend.Close)
	authorityServer := httptest.NewTLSServer(http.HandlerFunc(f.serveAuthority))
	t.Cleanup(authorityServer.Close)
	scheduler, err := httpx.NewScheduler(4, 8)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := jiraadapter.NewWithSchedulerTLS(backend.URL+"/jira", attachmentChainBackend, "test", scheduler, projectPageProcessTLSOptions(t, backend))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.CloseIdleConnections)
	origin, err := reader.BrokerOriginSHA256()
	if err != nil {
		t.Fatal(err)
	}
	binding := domain.BrokerBackendBinding{Service: "jira", OriginSHA256: origin, WorkloadBackendID: "jira-primary"}
	now := time.Now()
	f.verified = domain.BrokerVerifiedContext{
		PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1",
		Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", Backend: binding,
		ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(2 * time.Minute).UnixMilli(),
		GrantExpiresMillis: now.Add(2 * time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(2 * time.Minute).UnixMilli(),
	}
	authority, err := brokerauthority.New(brokerauthority.Config{BaseURL: authorityServer.URL, ServerCredential: attachmentChainAuthority, IssuerSHA256: f.issuer, Version: "test", Scheduler: scheduler, TLS: projectPageProcessTLSOptions(t, authorityServer)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(authority.CloseIdleConnections)
	service, err := app.NewBrokerJiraAttachmentStreamService(authority, app.BrokerJiraAttachmentStreamReader{Backend: binding, Reader: reader})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewCredentialGuard([]byte(attachmentChainAuthority), []byte(attachmentChainBackend))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Close)
	reads, err := app.NewBrokerReadService(authority, app.BrokerJiraIssueReader{Backend: binding, Reader: reader}, app.BrokerConfluencePageReader{})
	if err != nil {
		t.Fatal(err)
	}
	f.handler, err = New(Config{Audience: "atl-broker", BrokerID: "broker-1", MaxConcurrent: 1}, Dependencies{Authenticator: authority, Reads: reads, Attachments: service, Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := brokercontract.DefinitionV3(domain.BrokerOperationJiraAttachmentDownload, brokercontract.AttachmentOperationVersionV3)
	if !ok {
		t.Fatal("attachment definition missing")
	}
	f.request = domain.BrokerAttachmentRequestV3{SchemaVersion: 3, Operation: definition.Definition.ID, OperationVersion: definition.Definition.Version,
		RequestID: "request-1", Features: definition.Definition.RequiredFeatures,
		Expect:    domain.BrokerRequestExpectations{ExecutionID: f.verified.ExecutionID, ExecutionEpoch: f.verified.ExecutionEpoch, AuthorityRevision: f.verified.AuthorityRevision},
		Arguments: domain.BrokerAttachmentArgumentsV3{IssueKey: "PROJ-1", AttachmentID: "7"},
	}
	return f
}

func (f *attachmentChainFixture) bump(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[key]++
	return f.counts[key]
}

func (f *attachmentChainFixture) reject(writer http.ResponseWriter) {
	f.mu.Lock()
	f.violations++
	f.mu.Unlock()
	writer.WriteHeader(http.StatusBadRequest)
}

func (f *attachmentChainFixture) serveJira(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer "+attachmentChainBackend {
		f.reject(writer)
		return
	}
	switch request.URL.RequestURI() {
	case "/jira/rest/api/2/issue/PROJ-1?fields=attachment%2Cproject%2Cupdated":
		index := f.bump("metadata")
		updated := "2026-09-09T00:00:00Z"
		if index == f.metadataDrift {
			updated = "2026-09-09T00:00:01Z"
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"id": "101", "key": "PROJ-1", "fields": map[string]any{
			"project": map[string]any{"key": "PROJ"}, "updated": updated,
			"attachment": []any{map[string]any{"id": "7", "filename": "example.bin", "mimeType": "application/octet-stream", "size": len(f.payload),
				"created": "2026-09-08T00:00:00Z", "content": "/secure/attachment/7/example.bin", "author": map[string]any{"name": "fixture"}}},
		}})
	case "/jira/secure/attachment/7/example.bin":
		f.bump("body")
		writer.Header().Set("Content-Type", "application/octet-stream")
		switch f.sourceMode {
		case "":
			_, _ = writer.Write(f.payload)
		case "short":
			_, _ = writer.Write(f.payload[:len(f.payload)-1])
		case "extra":
			_, _ = writer.Write(f.payload)
			_, _ = writer.Write([]byte{'x'})
		case "redirect":
			writer.Header().Set("Location", "/jira/redirect-trap")
			writer.WriteHeader(http.StatusTemporaryRedirect)
		case "failure":
			writer.WriteHeader(http.StatusServiceUnavailable)
		case "blocked":
			writer.WriteHeader(http.StatusOK)
			if err := http.NewResponseController(writer).Flush(); err != nil {
				f.reject(writer)
				return
			}
			close(f.bodyEntered)
			<-request.Context().Done()
			close(f.bodyCancelled)
		default:
			f.reject(writer)
		}
	case "/jira/redirect-trap":
		f.bump("redirect")
		writer.WriteHeader(http.StatusForbidden)
	default:
		f.reject(writer)
	}
}

func (f *attachmentChainFixture) serveAuthority(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer "+attachmentChainAuthority || request.Header.Get("Content-Type") != "application/json" {
		f.reject(writer)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, brokercontract.MaxAttachmentAuthorityEnvelopeBytesV3+1))
	if err != nil || len(body) == 0 || int64(len(body)) > brokercontract.MaxAttachmentAuthorityEnvelopeBytesV3 {
		f.reject(writer)
		return
	}
	defer clear(body)
	now := time.Now().UTC().Truncate(time.Millisecond)
	var encoded []byte
	switch request.URL.Path {
	case "/v1/authenticate":
		value, decodeErr := brokertransport.DecodeAuthenticationRequestV1(body)
		if decodeErr != nil {
			f.reject(writer)
			return
		}
		credential := value.Credential()
		valid := bytes.Equal(credential, []byte(attachmentChainWorkload))
		clear(credential)
		f.mu.Lock()
		valid = valid && !f.nonces[value.Nonce]
		f.nonces[value.Nonce] = true
		f.mu.Unlock()
		if !valid {
			f.reject(writer)
			return
		}
		index := f.bump("authentication")
		if index == f.revokeAuthentication {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		if index == f.expireAuthentication {
			now = now.Add(-5 * time.Second)
		}
		encoded, err = brokertransport.EncodeAuthenticationResponseV1(brokertransport.AuthenticationResponse{SchemaVersion: 1, Nonce: value.Nonce, CredentialSHA256: value.CredentialSHA256, IssuerSHA256: f.issuer,
			IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(4 * time.Second).UnixMilli(), Context: f.verified})
	case brokertransport.DiscoveryPathV4:
		value, decodeErr := brokercontract.DecodeFamilyDiscoveryAuthorizationRequestV4(body)
		if decodeErr != nil {
			f.reject(writer)
			return
		}
		f.bump("discovery")
		encoded, err = brokercontract.EncodeFamilyDiscoveryProjectionV4(attachmentChainDiscovery(value, now))
	case brokertransport.AuthorizeAttachmentAdmissionPathV3:
		value, decodeErr := brokercontract.DecodeAttachmentAdmissionRequestV3(body)
		if decodeErr != nil {
			f.reject(writer)
			return
		}
		f.bump("admission")
		digest, digestErr := brokercontract.AttachmentAdmissionRequestSHA256V3(value)
		if digestErr != nil {
			f.reject(writer)
			return
		}
		decision := attachmentChainDecision(value.Context, digest, now)
		if value.Arguments.IssueKey != "PROJ-1" || value.Arguments.AttachmentID != "7" {
			decision.Status, decision.Reason = domain.BrokerDecisionDenied, domain.BrokerReasonDenied
		}
		encoded, err = brokercontract.EncodeAttachmentAdmissionDecisionV3(domain.BrokerAttachmentAdmissionDecisionV3{BrokerDecisionCore: decision})
	case brokertransport.AuthorizeAttachmentQualificationPathV3:
		value, decodeErr := brokercontract.DecodeAttachmentQualificationRequestV3(body)
		if decodeErr != nil {
			f.reject(writer)
			return
		}
		f.bump("qualification_" + string(value.Phase))
		plan, verified := attachmentChainQualification(value)
		digest, _ := brokercontract.AttachmentQualificationRequestSHA256V3(value)
		planDigest, _ := brokercontract.AttachmentMetadataPlanSHA256V3(plan)
		encoded, err = brokercontract.EncodeAttachmentQualificationDecisionV3(domain.BrokerAttachmentQualificationDecisionV3{Phase: value.Phase, BrokerDecisionCore: attachmentChainDecision(verified, digest, now), PlanSHA256: planDigest})
	case brokertransport.AuthorizeAttachmentOperationPathV3:
		value, decodeErr := brokercontract.DecodeAttachmentOperationAuthorizationRequestV3(body)
		if decodeErr != nil {
			f.reject(writer)
			return
		}
		index := f.bump("operation_" + string(value.Phase))
		if value.Phase == domain.BrokerAttachmentOperationRelease && f.releaseHook != nil {
			f.releaseHook(index)
		}
		decision := attachmentChainOperation(value, now)
		if value.Phase == domain.BrokerAttachmentOperationRelease && index == f.denyRelease {
			decision.Status, decision.Reason = domain.BrokerDecisionDenied, domain.BrokerReasonDenied
		}
		encoded, err = brokercontract.EncodeAttachmentOperationDecisionV3(decision)
	default:
		f.reject(writer)
		return
	}
	if err != nil {
		f.reject(writer)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(encoded)
}

func attachmentChainDecision(verified domain.BrokerVerifiedContext, requestSHA string, now time.Time) domain.BrokerDecisionCore {
	contextSHA, _ := brokercontract.VerifiedContextSHA256(verified)
	return domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "fixture-decision", AuthorityRevision: verified.AuthorityRevision,
		ContextSHA256: contextSHA, RequestSHA256: requestSHA, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(4 * time.Second).UnixMilli()}
}

func attachmentChainQualification(value domain.BrokerAttachmentQualificationRequestV3) (domain.BrokerAttachmentMetadataPlanV3, domain.BrokerVerifiedContext) {
	if value.Initial != nil {
		return value.Initial.Plan, value.Initial.Admission.Context
	}
	if value.PreOpen != nil {
		_, verified := attachmentChainQualification(value.PreOpen.InitialOperation.Qualified.QualificationRequest)
		return value.PreOpen.Plan, verified
	}
	return value.Release.Plan, value.Release.Context
}

func attachmentChainOperation(value domain.BrokerAttachmentOperationAuthorizationRequestV3, now time.Time) domain.BrokerAttachmentOperationDecisionV3 {
	var qualification domain.BrokerAttachmentQualificationRequestV3
	var qualificationDecision string
	if value.Release != nil {
		qualification, qualificationDecision = value.Release.QualificationRequest, value.Release.QualificationDecision.DecisionSHA256
	} else {
		qualification, qualificationDecision = value.Qualified.QualificationRequest, value.Qualified.QualificationDecision.DecisionSHA256
	}
	_, verified := attachmentChainQualification(qualification)
	digest, _ := brokercontract.AttachmentOperationAuthorizationRequestSHA256V3(value)
	decision := domain.BrokerAttachmentOperationDecisionV3{Phase: value.Phase, BrokerDecisionCore: attachmentChainDecision(verified, digest, now),
		QualificationDecisionSHA256: qualificationDecision, Operation: domain.BrokerOperationJiraAttachmentDownload, OperationVersion: 1}
	if value.Release != nil {
		decision.AnchorSHA256, decision.PriorReleaseSHA256, decision.ManifestCoreSHA256 = value.Release.AnchorSHA256, value.Release.PriorReleaseSHA256, value.Release.ManifestCoreSHA256
		decision.ReleaseFactsSHA256, _ = brokercontract.AttachmentReleaseFactsSHA256V3(value.Release.Facts)
	} else {
		initial := qualification.Initial
		if initial == nil {
			initial = qualification.PreOpen.InitialOperation.Qualified.QualificationRequest.Initial
		}
		decision.ArgumentsSHA256, _ = brokercontract.AttachmentArgumentsSHA256V3(initial.Admission.Arguments)
		decision.ResourcesSHA256, _ = brokercontract.AttachmentResourcesSHA256V3(value.Qualified.Snapshot)
		decision.EffectsSHA256, _ = brokercontract.AttachmentEffectsSHA256V3(value.Qualified.Effects)
	}
	return decision
}
