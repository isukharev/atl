package brokerclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

type changingCacheSessionLoader struct{ calls atomic.Int32 }

func (loader *changingCacheSessionLoader) Load() (Session, error) {
	value := testSession()
	if loader.calls.Add(1) > 1 {
		value.ExecutionEpoch = "epoch-2"
	}
	return value, nil
}

type stableCacheSessionLoader struct {
	calls             atomic.Int32
	replaceCredential int32
	onLoad            func()
}

func (loader *stableCacheSessionLoader) Load() (Session, error) {
	if loader.onLoad != nil {
		loader.onLoad()
	}
	value := testSession()
	if loader.calls.Add(1) >= loader.replaceCredential && loader.replaceCredential > 0 {
		value.Credential = []byte("replacement-workload-credential")
	}
	return value, nil
}

func TestCacheQualificationRejectsSessionChangeAfterResponse(t *testing.T) {
	loader := &changingCacheSessionLoader{}
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		body, _ := io.ReadAll(request.Body)
		claim, err := brokertransport.DecodeCacheQualificationClaimV2(body)
		if err != nil {
			t.Error(err)
			return
		}
		now := time.Now()
		target, _ := brokercontract.CacheTargetExecutionClaimSHA256V1(claim.BrokerID, claim.Audience, claim.Expect.ExecutionID, claim.Expect.ExecutionEpoch)
		revision, _ := brokercontract.CacheAuthorityRevisionSHA256V1(claim.Expect.AuthorityRevision)
		decision := domain.BrokerCacheQualification{SchemaVersion: 1, Status: domain.BrokerDecisionAllowed, IssuerSHA256: strings.Repeat("a", 64), TargetExecutionSHA256: target, AuthorityRevisionSHA256: revision, RequestSHA256: strings.Repeat("b", 64), IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(3 * time.Second).UnixMilli()}
		envelope, _ := brokertransport.NewCacheQualificationEnvelopeV2(claim, domain.BrokerResolvedCacheQualificationV2{Decision: decision}, now.Add(2*time.Second))
		encoded, _ := brokertransport.EncodeCacheQualificationEnvelopeV2(envelope)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-ATL-Correlation-ID", "correlation-1")
		_, _ = writer.Write(encoded)
	}))
	client := newTestClient(t, server, loader, "broker-1")
	candidate := domain.BrokerCacheCandidate{Operation: domain.BrokerOperationConfluencePageRead, SelectorSHA256: strings.Repeat("1", 64), ProjectionSHA256: strings.Repeat("2", 64), EvidenceSchemaSHA256: brokercontract.CacheEvidenceSchemaSHA256V1(), GenerationSHA256: strings.Repeat("3", 64), ContentSHA256: strings.Repeat("4", 64)}
	if _, err := client.QualifyCache(t.Context(), candidate); !errors.Is(err, domain.ErrCheckFailed) || requests.Load() != 1 || loader.calls.Load() != 2 {
		t.Fatalf("error=%v requests=%d sessions=%d", err, requests.Load(), loader.calls.Load())
	}
}

func TestCacheGrantRevalidationBindsCredentialAndMonotonicLease(t *testing.T) {
	loader := &stableCacheSessionLoader{}
	server := cacheQualificationTestServer(t)
	client := newTestClient(t, server, loader, "broker-1")
	candidate := domain.BrokerCacheCandidate{Operation: domain.BrokerOperationConfluencePageRead, SelectorSHA256: strings.Repeat("1", 64), ProjectionSHA256: strings.Repeat("2", 64), EvidenceSchemaSHA256: brokercontract.CacheEvidenceSchemaSHA256V1(), GenerationSHA256: strings.Repeat("3", 64), ContentSHA256: strings.Repeat("4", 64)}
	grant, err := client.QualifyCache(t.Context(), candidate)
	if err != nil || client.ValidateCacheGrant(candidate, grant) != nil {
		t.Fatalf("grant=%#v error=%v", grant, err)
	}
	encoded, err := json.Marshal(grant)
	if err != nil || strings.Contains(string(encoded), "Lease") || strings.Contains(string(encoded), "synthetic-workload-credential") {
		t.Fatalf("serialized ephemeral state: %s error=%v", encoded, err)
	}
	loader.replaceCredential = loader.calls.Load() + 1
	if err := client.ValidateCacheGrant(candidate, grant); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("replacement credential error=%v", err)
	}
	loader.replaceCredential = 0
	var loaded atomic.Bool
	loader.onLoad = func() { loaded.Store(true) }
	resampleStarted := time.Now()
	client.now = func() time.Time {
		if loaded.Load() {
			return resampleStarted.Add(6 * time.Second)
		}
		return resampleStarted
	}
	if err := client.ValidateCacheGrant(candidate, grant); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("post-load lease expiry error=%v", err)
	}
	loader.onLoad = nil
	expired := time.Now().Add(6 * time.Second)
	client.now = func() time.Time { return expired }
	grant.Decision.IssuedAtMillis = expired.Add(-time.Second).UnixMilli()
	grant.Decision.ExpiresAtMillis = expired.Add(3 * time.Second).UnixMilli()
	grant.ReleaseDeadlineMillis = expired.Add(2 * time.Second).UnixMilli()
	if err := client.ValidateCacheGrant(candidate, grant); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("wall-clock renewal revived monotonic lease: %v", err)
	}
	grant.Lease = domain.BrokerCacheGrantLease{}
	if err := client.ValidateCacheGrant(candidate, grant); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("missing ephemeral lease error=%v", err)
	}
}

func TestCacheQualificationNormalizesEarlierWallOnlyParentDeadline(t *testing.T) {
	claims := make(chan brokertransport.CacheQualificationClaimV2, 1)
	loader := &stableCacheSessionLoader{}
	server := cacheQualificationTestServerWithClaims(t, claims)
	client := newTestClient(t, server, loader, "broker-1")
	candidate := domain.BrokerCacheCandidate{Operation: domain.BrokerOperationConfluencePageRead, SelectorSHA256: strings.Repeat("1", 64), ProjectionSHA256: strings.Repeat("2", 64), EvidenceSchemaSHA256: brokercontract.CacheEvidenceSchemaSHA256V1(), GenerationSHA256: strings.Repeat("3", 64), ContentSHA256: strings.Repeat("4", 64)}
	started := time.Now()
	wallOnly := time.UnixMilli(started.Add(1500 * time.Millisecond).UnixMilli())
	ctx, cancel := context.WithDeadline(t.Context(), wallOnly)
	defer cancel()
	normalized := cacheQualificationDeadline(started, ctx)
	monotonicPreserved := normalized != normalized.Round(0) //nolint:staticcheck // Time == intentionally inspects the monotonic reading.
	if !normalized.Equal(wallOnly) || !monotonicPreserved {
		t.Fatalf("normalized=%v wall=%v monotonic_preserved=%t", normalized, wallOnly, monotonicPreserved)
	}
	grant, err := client.QualifyCache(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	var claim brokertransport.CacheQualificationClaimV2
	select {
	case claim = <-claims:
	case <-ctx.Done():
		t.Fatal("qualification completed without a bounded claim receipt")
	}
	if claim.NotAfterMillis != wallOnly.UnixMilli() || grant.Lease.Active(started.Add(2*time.Second)) {
		t.Fatalf("claim_deadline=%d parent=%d lease_active=%t error=%v", claim.NotAfterMillis, wallOnly.UnixMilli(), grant.Lease.Active(started.Add(2*time.Second)), err)
	}
}

func cacheQualificationTestServer(t *testing.T) *httptest.Server {
	return cacheQualificationTestServerWithClaims(t, nil)
}

func cacheQualificationTestServerWithClaims(t *testing.T, claims chan<- brokertransport.CacheQualificationClaimV2) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		claim, err := brokertransport.DecodeCacheQualificationClaimV2(body)
		if err != nil {
			t.Error(err)
			return
		}
		if claims != nil {
			claims <- claim
		}
		now := time.Now()
		target, _ := brokercontract.CacheTargetExecutionClaimSHA256V1(claim.BrokerID, claim.Audience, claim.Expect.ExecutionID, claim.Expect.ExecutionEpoch)
		revision, _ := brokercontract.CacheAuthorityRevisionSHA256V1(claim.Expect.AuthorityRevision)
		decision := domain.BrokerCacheQualification{SchemaVersion: 1, Status: domain.BrokerDecisionAllowed, IssuerSHA256: strings.Repeat("a", 64), TargetExecutionSHA256: target, AuthorityRevisionSHA256: revision, RequestSHA256: strings.Repeat("b", 64), IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: min(claim.NotAfterMillis, now.Add(3*time.Second).UnixMilli())}
		release := min(claim.NotAfterMillis, now.Add(2*time.Second).UnixMilli())
		envelope, _ := brokertransport.NewCacheQualificationEnvelopeV2(claim, domain.BrokerResolvedCacheQualificationV2{Decision: decision}, time.UnixMilli(release))
		encoded, _ := brokertransport.EncodeCacheQualificationEnvelopeV2(envelope)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-ATL-Correlation-ID", "correlation-1")
		_, _ = writer.Write(encoded)
	}))
	t.Cleanup(server.Close)
	return server
}
