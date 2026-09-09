package brokerserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/adapter/brokerauthority"
	"github.com/isukharev/atl/internal/adapter/brokerclient"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type cacheCorpusFixture struct {
	domain.DocStore
	principal string
	page      domain.Resource
	calls     atomic.Int32
}

func (fixture *cacheCorpusFixture) Search(context.Context, string, int, string) ([]domain.PageRef, string, error) {
	fixture.calls.Add(1)
	return []domain.PageRef{{ID: fixture.page.ID, Space: fixture.page.SpaceKey}}, "", nil
}

func (fixture *cacheCorpusFixture) SearchComplete(context.Context, string, int, string) (domain.PageSearchPage, error) {
	fixture.calls.Add(1)
	return domain.PageSearchPage{Results: []domain.PageRef{{ID: fixture.page.ID, Space: fixture.page.SpaceKey}}, Complete: true}, nil
}

func (fixture *cacheCorpusFixture) GetPage(_ context.Context, id string, _ domain.PullOpts) (*domain.Resource, error) {
	fixture.calls.Add(1)
	if id != fixture.page.ID {
		return nil, domain.ErrNotFound
	}
	value := fixture.page
	value.Body = append([]byte(nil), value.Body...)
	value.Labels = append([]string(nil), value.Labels...)
	value.Ancestors = append([]string(nil), value.Ancestors...)
	value.AncestorIDs = append([]string(nil), value.AncestorIDs...)
	return &value, nil
}

func (fixture *cacheCorpusFixture) CurrentConfluenceUser(context.Context) (domain.ConfluenceUserIdentity, error) {
	fixture.calls.Add(1)
	return domain.ConfluenceUserIdentity{ID: fixture.principal, DisplayName: "Configured Fixture"}, nil
}

func (fixture *cacheCorpusFixture) ReadConfluenceCorpusMetadata(_ context.Context, space string, maxPages int) (domain.ConfluenceCorpusMetadataInventory, error) {
	fixture.calls.Add(1)
	if space != fixture.page.SpaceKey || maxPages != 1 {
		return domain.ConfluenceCorpusMetadataInventory{}, domain.ErrCheckFailed
	}
	restricted := false
	if fixture.page.Restricted != nil {
		restricted = *fixture.page.Restricted
	}
	return domain.ConfluenceCorpusMetadataInventory{Rows: []domain.ConfluenceCorpusMetadata{{
		ID: fixture.page.ID, Type: fixture.page.Type, Title: fixture.page.Title, Space: fixture.page.SpaceKey,
		Version: fixture.page.Version, Updated: fixture.page.Updated, Parent: fixture.page.Parent,
		Ancestors: append([]string{}, fixture.page.Ancestors...), AncestorIDs: append([]string{}, fixture.page.AncestorIDs...),
		Labels: append([]string{}, fixture.page.Labels...), Restricted: restricted, URL: fixture.page.URL,
	}}, Complete: true}, nil
}

type trustedCacheCapture struct {
	sourcePrincipal string
	sourceScope     string
}

func TestQualifiedCorpusHandoffBindsSealedCaptureAcrossTargetContexts(t *testing.T) {
	storeA, candidateA, captureA, sourceCallsA := buildCacheCorpusFixture(t, "source-a", "alpha")
	storeB, candidateB, captureB, sourceCallsB := buildCacheCorpusFixture(t, "source-b", "beta")
	storeUnknown, _, _, sourceCallsUnknown := buildCacheCorpusFixture(t, "source-unknown", "unknown")
	storeNativeDrift, candidateNativeDrift, captureNativeDrift, sourceCallsNative := buildCacheCorpusFixture(t, "source-native", "native")
	storeProjectionDrift, candidateProjectionDrift, captureProjectionDrift, sourceCallsProjection := buildCacheCorpusFixture(t, "source-projection", "projection")
	trusted := map[domain.BrokerCacheCandidate]trustedCacheCapture{
		candidateA:               {sourcePrincipal: cacheCorpusDigest("source-a"), sourceScope: captureA.ScopeDigest},
		candidateB:               {sourcePrincipal: cacheCorpusDigest("source-b"), sourceScope: captureB.ScopeDigest},
		candidateNativeDrift:     {sourcePrincipal: cacheCorpusDigest("source-native"), sourceScope: captureNativeDrift.ScopeDigest},
		candidateProjectionDrift: {sourcePrincipal: cacheCorpusDigest("source-projection"), sourceScope: captureProjectionDrift.ScopeDigest},
	}
	sourceCallsBefore := map[*cacheCorpusFixture]int32{
		sourceCallsA: sourceCallsA.calls.Load(), sourceCallsB: sourceCallsB.calls.Load(), sourceCallsUnknown: sourceCallsUnknown.calls.Load(),
		sourceCallsNative: sourceCallsNative.calls.Load(), sourceCallsProjection: sourceCallsProjection.calls.Load(),
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	issuer := strings.Repeat("a", 64)
	origin := strings.Repeat("b", 64)
	credentialA, credentialB := "synthetic-target-a-credential", "synthetic-target-b-credential"
	credentialHashA, credentialHashB := cacheCorpusCredentialHash(t, credentialA), cacheCorpusCredentialHash(t, credentialB)
	contextA := cacheCorpusTargetContext(now, "target-a", "execution-a", "epoch-a", origin)
	contextB := cacheCorpusTargetContext(now, "target-b", "execution-b", "epoch-b", origin)
	var authenticationCalls, qualificationCalls, backendCalls atomic.Int32
	authorityServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		switch request.URL.Path {
		case "/v1/authenticate":
			authenticationCalls.Add(1)
			value, err := brokertransport.DecodeAuthenticationRequestV1(body)
			if err != nil {
				t.Error(err)
				return
			}
			verified := contextA
			switch value.CredentialSHA256 {
			case credentialHashA:
			case credentialHashB:
				verified = contextB
			default:
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			responseNow := time.Now()
			encoded, _ := brokertransport.EncodeAuthenticationResponseV1(brokertransport.AuthenticationResponse{SchemaVersion: 1, Nonce: value.Nonce, CredentialSHA256: value.CredentialSHA256, IssuerSHA256: issuer, IssuedAtMillis: responseNow.UnixMilli(), ExpiresAtMillis: responseNow.Add(4 * time.Second).UnixMilli(), Context: verified})
			_, _ = writer.Write(encoded)
		case "/v2/authorize/cache":
			qualificationCalls.Add(1)
			candidate, err := brokercontract.DecodeCacheQualificationCandidateV2(body)
			if err != nil {
				t.Error(err)
				return
			}
			record, found := trusted[candidate.Candidate]
			if !found {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			allowed := candidate.Context.PrincipalID == "target-a" && record.sourceScope == captureA.ScopeDigest || candidate.Context.PrincipalID == "target-b" && record.sourceScope == captureB.ScopeDigest
			status, reason := domain.BrokerDecisionDenied, domain.BrokerReasonDenied
			if allowed {
				status, reason = domain.BrokerDecisionAllowed, ""
			}
			requestValue := domain.BrokerCacheQualificationRequest{SchemaVersion: 1, Context: candidate.Context, SourcePrincipalSHA256: record.sourcePrincipal, SourceReadScopeSHA256: record.sourceScope, Operation: candidate.Candidate.Operation, SelectorSHA256: candidate.Candidate.SelectorSHA256, ProjectionSHA256: candidate.Candidate.ProjectionSHA256, EvidenceSchemaSHA256: candidate.Candidate.EvidenceSchemaSHA256, GenerationSHA256: candidate.Candidate.GenerationSHA256, ContentSHA256: candidate.Candidate.ContentSHA256, ExpiresAtMillis: candidate.NotAfterMillis}
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
	reads, err := app.NewBrokerReadService(authority, app.BrokerJiraIssueReader{}, app.BrokerConfluencePageReader{Backend: contextA.Backend, Reader: cacheNoBackendReader{origin: origin, calls: &backendCalls}})
	if err != nil {
		t.Fatal(err)
	}
	cacheService, err := app.NewBrokerCacheQualificationService(authority, issuer)
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := NewCredentialGuard([]byte("synthetic-server-credential"))
	defer guard.Close()
	handler, err := New(Config{Audience: "atl-broker", BrokerID: "broker-1", MaxConcurrent: 1}, Dependencies{Authenticator: authority, Reads: reads, Cache: cacheService, Guard: guard})
	if err != nil {
		t.Fatal(err)
	}
	brokerServer := httptest.NewTLSServer(handler)
	defer brokerServer.Close()
	brokerTLS, _, err := httpx.QualifiedTLSOptionsBytes(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: brokerServer.Certificate().Raw}))
	if err != nil {
		t.Fatal(err)
	}
	clientA := cacheCorpusClient(t, brokerServer.URL, brokerTLS, credentialA, contextA)
	clientB := cacheCorpusClient(t, brokerServer.URL, brokerTLS, credentialB, contextB)
	options := func(root string) app.QualifiedCorpusHandoffOptions {
		return app.QualifiedCorpusHandoffOptions{StoreRoot: root, GeneratorVersion: "test-v1", GeneratorCommit: strings.Repeat("a", 40), BuildState: corpus.BuildStateClean}
	}
	if result, err := app.PrepareQualifiedCorpusHandoff(t.Context(), options(storeA), clientA); err != nil || result.Qualification != "current_allowed" {
		t.Fatalf("target A/source A result=%#v error=%v", result, err)
	}
	if result, err := app.PrepareQualifiedCorpusHandoff(t.Context(), options(storeB), clientB); err != nil || result.Qualification != "current_allowed" {
		t.Fatalf("target B/source B result=%#v error=%v", result, err)
	}
	if result, err := app.PrepareQualifiedCorpusHandoff(t.Context(), options(storeB), clientA); result != nil || !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("cross-scope result=%#v error=%v", result, err)
	}
	if result, err := app.PrepareQualifiedCorpusHandoff(t.Context(), options(storeA), clientB); result != nil || !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("reverse cross-scope result=%#v error=%v", result, err)
	}
	if result, err := app.PrepareQualifiedCorpusHandoff(t.Context(), options(storeUnknown), clientA); result != nil || err == nil {
		t.Fatalf("unknown tuple result=%#v error=%v", result, err)
	}

	qualificationBeforeDrift := qualificationCalls.Load()
	tamperCacheCorpusMember(t, storeNativeDrift, "markdown/")
	if result, err := app.PrepareQualifiedCorpusHandoff(t.Context(), options(storeNativeDrift), clientA); result != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("native drift result=%#v error=%v", result, err)
	}
	tamperCacheCorpusMember(t, storeProjectionDrift, "projection/")
	if result, err := app.PrepareQualifiedCorpusHandoff(t.Context(), options(storeProjectionDrift), clientB); result != nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("projection drift result=%#v error=%v", result, err)
	}
	if authenticationCalls.Load() != 5 || qualificationCalls.Load() != 5 || qualificationCalls.Load() != qualificationBeforeDrift || backendCalls.Load() != 0 {
		t.Fatalf("auth=%d qualify=%d before_drift=%d backend=%d", authenticationCalls.Load(), qualificationCalls.Load(), qualificationBeforeDrift, backendCalls.Load())
	}
	for fixture, before := range sourceCallsBefore {
		if before == 0 || fixture.calls.Load() != before {
			t.Fatalf("source calls before/after handoff=%d/%d", before, fixture.calls.Load())
		}
	}
}

func buildCacheCorpusFixture(t *testing.T, principal, content string) (string, domain.BrokerCacheCandidate, corpus.CaptureReceipt, *cacheCorpusFixture) {
	t.Helper()
	restricted := false
	fixture := &cacheCorpusFixture{principal: principal, page: domain.Resource{ID: "10", Type: "page", Title: "Configured " + content, SpaceKey: "DOC", Version: 1, Body: []byte("<p>" + content + "</p>"), BodyPresent: true, AncestorsPresent: true, Labels: []string{"synthetic"}, Updated: "2026-09-09T10:00:00Z", Restricted: &restricted, URL: "https://confluence.example.test/pages/10"}}
	confluence := app.NewConfluenceService(app.ConfluenceDependencies{Store: fixture, CorpusMetadata: fixture, BaseURL: "https://confluence.example.test", RequestMaxInFlight: 1, RequestsPerSecond: 100})
	service := app.NewCorpusBuildService(app.CorpusBuildDependencies{Confluence: confluence, GeneratorVersion: "test-v1", GeneratorCommit: strings.Repeat("a", 40), BuildState: corpus.BuildStateClean, ConfluenceTrustDigest: strings.Repeat("b", 64)})
	workspaceRoot, cacheRoot := cacheCorpusPrivateRoot(t), cacheCorpusPrivateRoot(t)
	result, err := service.Build(t.Context(), app.CorpusBuildOptions{Root: workspaceRoot, Initialize: true, CacheRoot: cacheRoot, InitializeCache: true, CacheMaxRequests: 10, CacheMaxResponseBytes: 1 << 20, CacheDeadline: 30 * time.Second, ConfluenceSpace: "DOC", MaxConfluencePages: 1, MaxRequests: 100, MaxResponseBytes: 1 << 20, MaxMembers: 100, MaxGenerationBytes: 4 << 20, Deadline: time.Minute, MaxInFlight: 1, RequestsPerSecond: 100})
	if err != nil || result.Generation.GenerationDigest == "" {
		t.Fatalf("build result=%#v error=%v", result, err)
	}
	candidate, capture := cacheCorpusCandidate(t, cacheRoot)
	return cacheRoot, candidate, capture, fixture
}

func cacheCorpusCandidate(t *testing.T, root string) (domain.BrokerCacheCandidate, corpus.CaptureReceipt) {
	t.Helper()
	store, err := corpus.Open(root, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	generation, err := store.SelectCurrent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()
	manifest := generation.Manifest()
	if len(manifest.Qualifications) != 1 {
		t.Fatalf("qualifications=%#v", manifest.Qualifications)
	}
	var captureMember corpus.Member
	for _, member := range manifest.Members {
		if member.Path == "capture/confluence/receipt.capture-v1.json" {
			captureMember = member
		}
	}
	var body bytes.Buffer
	if _, err := generation.CopyMember(t.Context(), captureMember.Service, captureMember.StableID, captureMember.Role, &body); err != nil {
		t.Fatal(err)
	}
	capture, err := corpus.ParseCaptureReceipt(body.Bytes(), corpus.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	qualification := manifest.Qualifications[0]
	return domain.BrokerCacheCandidate{Operation: domain.BrokerOperationConfluencePageRead, SelectorSHA256: qualification.SelectorDigest, ProjectionSHA256: qualification.ProjectionDigest, EvidenceSchemaSHA256: brokercontract.CacheEvidenceSchemaSHA256V1(), GenerationSHA256: generation.Receipt().GenerationDigest, ContentSHA256: capture.SnapshotDigest}, capture
}

func cacheCorpusClient(t *testing.T, baseURL string, tlsOptions httpx.TLSOptions, credential string, verified domain.BrokerVerifiedContext) *brokerclient.Client {
	t.Helper()
	client, err := brokerclient.New(brokerclient.Config{BaseURL: baseURL, BrokerID: "broker-1", Audience: "atl-broker", Version: "test", TLS: tlsOptions, Session: cacheProcessSession{value: brokerclient.Session{Credential: []byte(credential), ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, AuthorityRevision: verified.AuthorityRevision}}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func cacheCorpusTargetContext(now time.Time, principal, execution, epoch, origin string) domain.BrokerVerifiedContext {
	return domain.BrokerVerifiedContext{PrincipalID: principal, WorkloadID: "workload-" + principal, ExecutionID: execution, ExecutionEpoch: epoch, Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(time.Minute).UnixMilli(), GrantExpiresMillis: now.Add(time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(time.Minute).UnixMilli(), Backend: domain.BrokerBackendBinding{Service: "confluence", OriginSHA256: origin, WorkloadBackendID: "confluence-primary"}}
}

func cacheCorpusCredentialHash(t *testing.T, credential string) string {
	t.Helper()
	request, err := brokertransport.NewAuthenticationRequest([]byte(credential), brokertransport.AuthenticationChallenge{Nonce: strings.Repeat("n", 32), Audience: "atl-broker", BrokerID: "broker-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer request.Clear()
	return request.CredentialSHA256
}

func cacheCorpusDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func cacheCorpusPrivateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func tamperCacheCorpusMember(t *testing.T, root, pathPrefix string) {
	t.Helper()
	store, err := corpus.Open(root, corpus.Options{})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := store.SelectCurrent(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	generationID := generation.ID()
	var selected corpus.Member
	for _, member := range generation.Manifest().Members {
		if strings.HasPrefix(member.Path, pathPrefix) {
			selected = member
			break
		}
	}
	_ = generation.Close()
	_ = store.Close()
	if selected.Path == "" {
		t.Fatalf("member prefix %s missing", pathPrefix)
	}
	path := filepath.Join(root, "generations", generationID, "artifacts", filepath.FromSlash(selected.Path))
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
}
