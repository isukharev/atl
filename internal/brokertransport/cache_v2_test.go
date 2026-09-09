package brokertransport

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func cacheTransportClaim(now time.Time) CacheQualificationClaimV2 {
	return CacheQualificationClaimV2{SchemaVersion: 2, RequestID: "request-1", Service: "confluence", BrokerID: "broker-1", Audience: "atl-broker", Expect: domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"}, NotAfterMillis: now.Add(5 * time.Second).UnixMilli(), Candidate: domain.BrokerCacheCandidate{Operation: domain.BrokerOperationConfluencePageRead, SelectorSHA256: strings.Repeat("1", 64), ProjectionSHA256: strings.Repeat("2", 64), EvidenceSchemaSHA256: brokercontract.CacheEvidenceSchemaSHA256V1(), GenerationSHA256: strings.Repeat("3", 64), ContentSHA256: strings.Repeat("4", 64)}}
}

func TestCacheQualificationHTTPV2RoundTripAndDeadline(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	claim := cacheTransportClaim(now)
	body, err := EncodeCacheQualificationClaimV2(claim)
	decoded, decodeErr := DecodeCacheQualificationClaimV2(body)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, claim) {
		t.Fatalf("claim=%#v encode=%v decode=%v", decoded, err, decodeErr)
	}
	if _, err := DecodeCacheQualificationClaimV2(bytes.Replace(body, []byte(`"service":"confluence"`), []byte(`"service":"confluence","source_scope":"forged"`), 1)); err == nil {
		t.Fatal("unknown claim field accepted")
	}
	verified := testVerifiedContext(now)
	verified.Backend.Service = "confluence"
	started := now
	authDeadline := now.Add(3 * time.Second)
	candidate, deadline, err := BindCacheQualificationClaimV2(claim, verified, started, now.Add(2*time.Second), authDeadline)
	if err != nil || !deadline.Equal(authDeadline) || candidate.NotAfterMillis != authDeadline.UnixMilli() {
		t.Fatalf("candidate=%#v deadline=%v error=%v", candidate, deadline, err)
	}
	if _, _, err := BindCacheQualificationClaimV2(claim, verified, started, authDeadline, authDeadline); err == nil {
		t.Fatal("exhausted authentication deadline accepted")
	}
}

func TestCacheQualificationEnvelopeBindsEveryVisibleLink(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	claim := cacheTransportClaim(now)
	verified := testVerifiedContext(now)
	verified.Backend.Service = "confluence"
	candidate, deadline, err := BindCacheQualificationClaimV2(claim, verified, now, now, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	resolved := cacheTransportResolution(t, candidate, now)
	envelope, err := NewCacheQualificationEnvelopeV2(claim, resolved, deadline.Add(-time.Second))
	body, encodeErr := EncodeCacheQualificationEnvelopeV2(envelope)
	decoded, decodeErr := DecodeCacheQualificationEnvelopeV2(body)
	if err != nil || encodeErr != nil || decodeErr != nil || ValidateCacheQualificationEnvelopeV2(decoded, claim, now) != nil {
		t.Fatalf("envelope=%#v new=%v encode=%v decode=%v", decoded, err, encodeErr, decodeErr)
	}
	for name, mutate := range map[string]func(*CacheQualificationEnvelopeV2){
		"claim":     func(v *CacheQualificationEnvelopeV2) { v.ClaimSHA256 = strings.Repeat("b", 64) },
		"candidate": func(v *CacheQualificationEnvelopeV2) { v.CandidateSHA256 = strings.Repeat("b", 64) },
		"target":    func(v *CacheQualificationEnvelopeV2) { v.TargetExecutionSHA256 = strings.Repeat("b", 64) },
		"revision":  func(v *CacheQualificationEnvelopeV2) { v.AuthorityRevisionSHA256 = strings.Repeat("b", 64) },
		"request":   func(v *CacheQualificationEnvelopeV2) { v.RequestSHA256 = strings.Repeat("b", 64) },
		"deadline":  func(v *CacheQualificationEnvelopeV2) { v.ReleaseDeadlineMillis = now.UnixMilli() },
		"renewal": func(v *CacheQualificationEnvelopeV2) {
			v.Qualification.IssuedAtMillis = now.Add(999 * time.Millisecond).UnixMilli()
			v.Qualification.ExpiresAtMillis = claim.NotAfterMillis + 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := envelope
			mutate(&changed)
			if ValidateCacheQualificationEnvelopeV2(changed, claim, now) == nil {
				t.Fatal("changed envelope validated")
			}
		})
	}
	if _, err := DecodeCacheQualificationEnvelopeV2(bytes.Replace(body, []byte(`"complete":true`), []byte(`"complete":true,"source":"forged"`), 1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("unknown envelope field error=%v", err)
	}
}

func cacheTransportResolution(t *testing.T, candidate domain.BrokerCacheQualificationCandidateV2, now time.Time) domain.BrokerResolvedCacheQualificationV2 {
	t.Helper()
	request := domain.BrokerCacheQualificationRequest{SchemaVersion: 1, Context: candidate.Context, SourcePrincipalSHA256: strings.Repeat("5", 64), SourceReadScopeSHA256: strings.Repeat("6", 64), Operation: candidate.Candidate.Operation, SelectorSHA256: candidate.Candidate.SelectorSHA256, ProjectionSHA256: candidate.Candidate.ProjectionSHA256, EvidenceSchemaSHA256: candidate.Candidate.EvidenceSchemaSHA256, GenerationSHA256: candidate.Candidate.GenerationSHA256, ContentSHA256: candidate.Candidate.ContentSHA256, ExpiresAtMillis: candidate.NotAfterMillis}
	requestDigest, _ := brokercontract.CacheQualificationRequestSHA256(request)
	target, _ := brokercontract.CacheTargetExecutionSHA256V1(candidate.Context)
	revision, _ := brokercontract.CacheAuthorityRevisionSHA256V1(candidate.Context.AuthorityRevision)
	candidateDigest, _ := brokercontract.CacheQualificationCandidateSHA256V2(candidate)
	return domain.BrokerResolvedCacheQualificationV2{SchemaVersion: 2, CandidateSHA256: candidateDigest, Request: request, Decision: domain.BrokerCacheQualification{SchemaVersion: 1, Status: domain.BrokerDecisionAllowed, IssuerSHA256: strings.Repeat("a", 64), TargetExecutionSHA256: target, AuthorityRevisionSHA256: revision, RequestSHA256: requestDigest, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(4 * time.Second).UnixMilli()}, Complete: true}
}

func FuzzCacheQualificationClaimV2StrictCodec(f *testing.F) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	seed, _ := EncodeCacheQualificationClaimV2(cacheTransportClaim(now))
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := DecodeCacheQualificationClaimV2(data)
		if err != nil {
			return
		}
		canonical, err := EncodeCacheQualificationClaimV2(value)
		if err != nil || !bytes.Equal(data, canonical) {
			t.Fatal("non-canonical cache claim accepted")
		}
	})
}
