package brokerserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/adapter/brokerauthority"
	"github.com/isukharev/atl/internal/adapter/brokerclient"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type cacheProcessSession struct {
	value brokerclient.Session
	calls *atomic.Int32
}

func (loader cacheProcessSession) Load() (brokerclient.Session, error) {
	if loader.calls != nil {
		loader.calls.Add(1)
	}
	value := loader.value
	value.Credential = append([]byte(nil), value.Credential...)
	return value, nil
}

type cacheNoBackendReader struct {
	origin string
	calls  *atomic.Int32
}

type cacheDeadlineAuthenticator struct{ calls atomic.Int32 }

func (authenticator *cacheDeadlineAuthenticator) Authenticate(ctx context.Context, _ []byte, _ brokertransport.AuthenticationChallenge) (brokertransport.Authentication, error) {
	authenticator.calls.Add(1)
	<-ctx.Done()
	return brokertransport.Authentication{}, ctx.Err()
}

type cacheCredentialCaptureAuthenticator struct{ credential []byte }

func (authenticator *cacheCredentialCaptureAuthenticator) Authenticate(_ context.Context, credential []byte, _ brokertransport.AuthenticationChallenge) (brokertransport.Authentication, error) {
	authenticator.credential = credential
	return brokertransport.Authentication{}, domain.ErrAuth
}

func (reader cacheNoBackendReader) BrokerOriginSHA256() (string, error) { return reader.origin, nil }
func (reader cacheNoBackendReader) QualifyBrokerPage(context.Context, string) (domain.BrokerConfluencePageIdentity, error) {
	reader.calls.Add(1)
	return domain.BrokerConfluencePageIdentity{}, domain.ErrCheckFailed
}
func (reader cacheNoBackendReader) ReadBrokerPage(context.Context, string, domain.BrokerConfluenceProjection) (domain.BrokerConfluencePageSnapshot, error) {
	reader.calls.Add(1)
	return domain.BrokerConfluencePageSnapshot{}, domain.ErrCheckFailed
}

func TestCacheQualificationRealClientServerAuthorityChain(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	issuer := strings.Repeat("a", 64)
	origin := strings.Repeat("b", 64)
	verified := domain.BrokerVerifiedContext{PrincipalID: "target-principal", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(time.Minute).UnixMilli(), GrantExpiresMillis: now.Add(time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(time.Minute).UnixMilli(), Backend: domain.BrokerBackendBinding{Service: "confluence", OriginSHA256: origin, WorkloadBackendID: "confluence-primary"}}
	knownGeneration := strings.Repeat("3", 64)
	knownContent := strings.Repeat("4", 64)
	knownCandidate := domain.BrokerCacheCandidate{Operation: domain.BrokerOperationConfluencePageRead, SelectorSHA256: strings.Repeat("1", 64), ProjectionSHA256: strings.Repeat("2", 64), EvidenceSchemaSHA256: brokercontract.CacheEvidenceSchemaSHA256V1(), GenerationSHA256: knownGeneration, ContentSHA256: knownContent}
	otherCandidate := knownCandidate
	otherCandidate.GenerationSHA256, otherCandidate.ContentSHA256 = strings.Repeat("7", 64), strings.Repeat("8", 64)
	var authenticationCalls, qualificationCalls, backendCalls, sessionLoads atomic.Int32
	authorityServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer synthetic-server-credential" {
			t.Error("authority credential missing")
		}
		body, _ := io.ReadAll(request.Body)
		switch request.URL.Path {
		case "/v1/authenticate":
			authenticationCalls.Add(1)
			value, err := brokertransport.DecodeAuthenticationRequestV1(body)
			if err != nil {
				t.Error(err)
				return
			}
			encoded, _ := brokertransport.EncodeAuthenticationResponseV1(brokertransport.AuthenticationResponse{SchemaVersion: 1, Nonce: value.Nonce, CredentialSHA256: value.CredentialSHA256, IssuerSHA256: issuer, IssuedAtMillis: time.Now().Add(-time.Millisecond).UnixMilli(), ExpiresAtMillis: time.Now().Add(4 * time.Second).UnixMilli(), Context: verified})
			_, _ = writer.Write(encoded)
		case "/v2/authorize/cache":
			qualificationCalls.Add(1)
			candidate, err := brokercontract.DecodeCacheQualificationCandidateV2(body)
			if err != nil {
				t.Error(err)
				return
			}
			var status domain.BrokerDecisionStatus
			var reason domain.BrokerReason
			var sourcePrincipal, sourceScope string
			switch candidate.Candidate {
			case knownCandidate:
				status, reason = domain.BrokerDecisionAllowed, ""
				sourcePrincipal, sourceScope = strings.Repeat("5", 64), strings.Repeat("6", 64)
			case otherCandidate:
				status, reason = domain.BrokerDecisionDenied, domain.BrokerReasonDenied
				sourcePrincipal, sourceScope = strings.Repeat("7", 64), strings.Repeat("8", 64)
			default:
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			requestValue := domain.BrokerCacheQualificationRequest{SchemaVersion: 1, Context: candidate.Context, SourcePrincipalSHA256: sourcePrincipal, SourceReadScopeSHA256: sourceScope, Operation: candidate.Candidate.Operation, SelectorSHA256: candidate.Candidate.SelectorSHA256, ProjectionSHA256: candidate.Candidate.ProjectionSHA256, EvidenceSchemaSHA256: candidate.Candidate.EvidenceSchemaSHA256, GenerationSHA256: candidate.Candidate.GenerationSHA256, ContentSHA256: candidate.Candidate.ContentSHA256, ExpiresAtMillis: candidate.NotAfterMillis}
			requestDigest, _ := brokercontract.CacheQualificationRequestSHA256(requestValue)
			target, _ := brokercontract.CacheTargetExecutionSHA256V1(candidate.Context)
			revision, _ := brokercontract.CacheAuthorityRevisionSHA256V1(candidate.Context.AuthorityRevision)
			candidateDigest, _ := brokercontract.CacheQualificationCandidateSHA256V2(candidate)
			decisionNow := time.Now()
			resolved := domain.BrokerResolvedCacheQualificationV2{SchemaVersion: 2, CandidateSHA256: candidateDigest, Request: requestValue, Decision: domain.BrokerCacheQualification{SchemaVersion: 1, Status: status, Reason: reason, IssuerSHA256: issuer, TargetExecutionSHA256: target, AuthorityRevisionSHA256: revision, RequestSHA256: requestDigest, IssuedAtMillis: decisionNow.UnixMilli(), ExpiresAtMillis: min(candidate.NotAfterMillis, decisionNow.Add(3*time.Second).UnixMilli())}, Complete: true}
			encoded, _ := brokercontract.EncodeResolvedCacheQualificationV2(resolved)
			_, _ = writer.Write(encoded)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer authorityServer.Close()
	authorityTLS, _, err := httpx.QualifiedTLSOptionsBytes(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: authorityServer.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := brokerauthority.New(brokerauthority.Config{BaseURL: authorityServer.URL, ServerCredential: "synthetic-server-credential", IssuerSHA256: issuer, Version: "test", TLS: authorityTLS})
	if err != nil {
		t.Fatal(err)
	}
	reads, err := app.NewBrokerReadService(authority, app.BrokerJiraIssueReader{}, app.BrokerConfluencePageReader{Backend: verified.Backend, Reader: cacheNoBackendReader{origin: origin, calls: &backendCalls}})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := app.NewBrokerCacheQualificationService(authority, issuer)
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := NewCredentialGuard([]byte("synthetic-server-credential"))
	defer guard.Close()
	handler, err := New(Config{Audience: "atl-broker", BrokerID: "broker-1", MaxConcurrent: 1}, Dependencies{Authenticator: authority, Reads: reads, Cache: cache, Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	brokerServer := httptest.NewTLSServer(handler)
	defer brokerServer.Close()
	brokerTLS, _, err := httpx.QualifiedTLSOptionsBytes(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: brokerServer.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	client, err := brokerclient.New(brokerclient.Config{BaseURL: brokerServer.URL, BrokerID: "broker-1", Audience: "atl-broker", Version: "test", TLS: brokerTLS, Session: cacheProcessSession{value: brokerclient.Session{Credential: []byte("synthetic-workload-credential"), ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"}, calls: &sessionLoads}})
	if err != nil {
		t.Fatal(err)
	}
	candidate := knownCandidate
	grant, err := client.QualifyCache(t.Context(), candidate)
	if err != nil || client.ValidateCacheGrant(candidate, grant) != nil || authenticationCalls.Load() != 1 || qualificationCalls.Load() != 1 || backendCalls.Load() != 0 || sessionLoads.Load() != 3 {
		t.Fatalf("grant=%#v err=%v auth=%d qualify=%d backend=%d sessions=%d", grant, err, authenticationCalls.Load(), qualificationCalls.Load(), backendCalls.Load(), sessionLoads.Load())
	}
	if _, err := client.QualifyCache(t.Context(), otherCandidate); err == nil || authenticationCalls.Load() != 2 || qualificationCalls.Load() != 2 || backendCalls.Load() != 0 {
		t.Fatalf("other scope err=%v auth=%d qualify=%d backend=%d", err, authenticationCalls.Load(), qualificationCalls.Load(), backendCalls.Load())
	}
	unknown := candidate
	unknown.GenerationSHA256 = strings.Repeat("f", 64)
	if _, err := client.QualifyCache(t.Context(), unknown); err == nil || authenticationCalls.Load() != 3 || qualificationCalls.Load() != 3 || backendCalls.Load() != 0 {
		t.Fatalf("unknown err=%v auth=%d qualify=%d backend=%d", err, authenticationCalls.Load(), qualificationCalls.Load(), backendCalls.Load())
	}
}

func TestCacheQualificationHasDedicatedAuditRoute(t *testing.T) {
	guard, err := NewCredentialGuard([]byte("synthetic-upstream-credential"))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	var output bytes.Buffer
	audit, err := NewAudit(&output, guard)
	if err != nil {
		t.Fatal(err)
	}
	host := &Host{audit: audit}
	request := httptest.NewRequest(http.MethodPost, brokertransport.CacheQualificationPathV2, strings.NewReader("{}"))
	host.auditedData(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(httptest.NewRecorder(), request)
	if err := audit.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	var event AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil || event.Route != "data_cache_qualification" || event.Outcome != "success" || !validAuditEvent(event) {
		t.Fatalf("event=%+v error=%v", event, err)
	}
}

func TestCacheQualificationDeadlineUnblocksSlowBodyBeforeAuthentication(t *testing.T) {
	authenticator := &cacheDeadlineAuthenticator{}
	handler := cacheDeadlineTestHandler(t, authenticator)
	server, closed := cacheDeadlineTLSServer(t, handler)
	defer server.Close()
	elapsed := cacheRawStalledRequest(t, server, closed, 2, []byte("{"))
	if elapsed >= 750*time.Millisecond || authenticator.calls.Load() != 0 || len(handler.permits) != 0 {
		t.Fatalf("elapsed=%v auth=%d permits=%d", elapsed, authenticator.calls.Load(), len(handler.permits))
	}
}

func TestCacheQualificationDeadlineCancelsSlowAuthenticationAndReleasesPermit(t *testing.T) {
	authenticator := &cacheDeadlineAuthenticator{}
	handler := cacheDeadlineTestHandler(t, authenticator)
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	client := server.Client()
	client.Timeout = 2 * time.Second
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+brokertransport.CacheQualificationPathV2, bytes.NewReader(cacheDeadlineClaimBody(t)))
	request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	request.Header.Set("Content-Type", "application/json")
	started := time.Now()
	response, _ := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if time.Since(started) >= time.Second || authenticator.calls.Load() != 1 || len(handler.permits) != 0 {
		t.Fatalf("elapsed=%v auth=%d permits=%d", time.Since(started), authenticator.calls.Load(), len(handler.permits))
	}
}

func TestCacheQualificationRefusesUnsupportedConnectionDeadlines(t *testing.T) {
	authenticator := &cacheDeadlineAuthenticator{}
	handler := cacheDeadlineTestHandler(t, authenticator)
	request := httptest.NewRequest(http.MethodPost, brokertransport.CacheQualificationPathV2, bytes.NewReader(cacheDeadlineClaimBody(t)))
	request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Body.Len() != 0 || authenticator.calls.Load() != 0 || len(handler.permits) != 0 {
		t.Fatalf("body=%q auth=%d permits=%d", response.Body.String(), authenticator.calls.Load(), len(handler.permits))
	}
}

func TestCacheQualificationClearsExtractedCredentialOnReturn(t *testing.T) {
	authenticator := &cacheCredentialCaptureAuthenticator{}
	handler := cacheDeadlineTestHandler(t, authenticator)
	handler.now = time.Now
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	response, err := server.Client().Do(cacheQualifiedRequest(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if len(authenticator.credential) == 0 {
		t.Fatal("authenticator did not observe the extracted credential")
	}
	for _, value := range authenticator.credential {
		if value != 0 {
			t.Fatal("extracted credential was not cleared on handler return")
		}
	}
}

func TestCacheQualificationDeadlineBoundsOversizedStalledBody(t *testing.T) {
	authenticator := &cacheDeadlineAuthenticator{}
	handler := cacheDeadlineTestHandler(t, authenticator)
	server, closed := cacheDeadlineTLSServer(t, handler)
	defer server.Close()
	elapsed := cacheRawStalledRequest(t, server, closed, brokertransport.MaxCacheQualificationBytesV2+2, bytes.Repeat([]byte("x"), int(brokertransport.MaxCacheQualificationBytesV2)+1))
	if elapsed >= 750*time.Millisecond || authenticator.calls.Load() != 0 || len(handler.permits) != 0 {
		t.Fatalf("elapsed=%v auth=%d permits=%d", elapsed, authenticator.calls.Load(), len(handler.permits))
	}
}

func cacheRawStalledRequest(t *testing.T, server *httptest.Server, closed <-chan struct{}, contentLength int64, body []byte) time.Duration {
	t.Helper()
	address := strings.TrimPrefix(server.URL, "https://")
	config := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13} //nolint:gosec // Isolated httptest certificate.
	connection, err := tls.Dial("tcp", address, config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	started := time.Now()
	if err := connection.SetDeadline(started.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	headers := "POST " + brokertransport.CacheQualificationPathV2 + " HTTP/1.1\r\nHost: " + address + "\r\nAuthorization: Bearer synthetic-workload-credential\r\nContent-Type: application/json\r\nContent-Length: " + strconv.FormatInt(contentLength, 10) + "\r\n\r\n"
	if _, err := io.WriteString(connection, headers); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(body); err != nil {
		t.Fatal(err)
	}
	var response [1]byte
	_, _ = connection.Read(response[:])
	remaining := time.Until(started.Add(750 * time.Millisecond))
	if remaining <= 0 {
		t.Fatal("stalled connection exceeded the route proof bound before server close")
	}
	select {
	case <-closed:
	case <-time.After(remaining):
		t.Fatal("server did not close the incomplete request connection within the route proof bound")
	}
	return time.Since(started)
}

func cacheDeadlineTLSServer(t *testing.T, handler http.Handler) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	closed := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	server.StartTLS()
	return server, closed
}

func TestCacheQualificationDeadlinePreservesNormalKeepaliveReuse(t *testing.T) {
	authenticator := &cacheDeadlineAuthenticator{}
	handler := cacheDeadlineTestHandler(t, authenticator)
	handler.now = time.Now
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	client := server.Client()
	first := cacheMalformedRequest(t, server.URL)
	response, err := client.Do(first)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	var reused bool
	second := cacheMalformedRequest(t, server.URL)
	second = second.WithContext(httptrace.WithClientTrace(second.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}))
	response, err = client.Do(second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if !reused || authenticator.calls.Load() != 0 || len(handler.permits) != 0 {
		t.Fatalf("reused=%t auth=%d permits=%d", reused, authenticator.calls.Load(), len(handler.permits))
	}
}

func cacheMalformedRequest(t *testing.T, baseURL string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+brokertransport.CacheQualificationPathV2, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func cacheQualifiedRequest(t *testing.T, baseURL string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+brokertransport.CacheQualificationPathV2, bytes.NewReader(cacheDeadlineClaimBody(t)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func cacheDeadlineClaimBody(t *testing.T) []byte {
	t.Helper()
	claim := brokertransport.CacheQualificationClaimV2{
		SchemaVersion: 2, RequestID: "request-1", Service: "confluence", BrokerID: "broker-1", Audience: "atl-broker",
		Expect:         domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"},
		NotAfterMillis: time.Now().Add(5 * time.Second).UnixMilli(),
		Candidate:      domain.BrokerCacheCandidate{Operation: domain.BrokerOperationConfluencePageRead, SelectorSHA256: strings.Repeat("1", 64), ProjectionSHA256: strings.Repeat("2", 64), EvidenceSchemaSHA256: brokercontract.CacheEvidenceSchemaSHA256V1(), GenerationSHA256: strings.Repeat("3", 64), ContentSHA256: strings.Repeat("4", 64)},
	}
	body, err := brokertransport.EncodeCacheQualificationClaimV2(claim)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func cacheDeadlineTestHandler(t *testing.T, authenticator brokertransport.Authenticator) *Handler {
	t.Helper()
	guard, err := NewCredentialGuard([]byte("synthetic-upstream-credential"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Close)
	return &Handler{
		config: Config{Audience: "atl-broker", BrokerID: "broker-1"}, authenticator: authenticator, guard: guard,
		permits: make(chan struct{}, 1), random: strings.NewReader(strings.Repeat("x", 128)),
		now: func() time.Time { return time.Now().Add(-4850 * time.Millisecond) },
	}
}
