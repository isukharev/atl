package brokercontract

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func cacheTestCandidate(now time.Time) domain.BrokerCacheQualificationCandidateV2 {
	context := fixtureContext()
	context.ExecutionNotBeforeMillis = now.Add(-time.Second).UnixMilli()
	context.ExecutionExpiresMillis = now.Add(time.Minute).UnixMilli()
	context.GrantExpiresMillis = now.Add(time.Minute).UnixMilli()
	context.CredentialExpiresMillis = now.Add(time.Minute).UnixMilli()
	context.Backend.Service = "confluence"
	return domain.BrokerCacheQualificationCandidateV2{SchemaVersion: 2, Context: context, NotAfterMillis: now.Add(5 * time.Second).UnixMilli(), Candidate: domain.BrokerCacheCandidate{
		Operation: domain.BrokerOperationConfluencePageRead, SelectorSHA256: digestChar('1'), ProjectionSHA256: digestChar('2'),
		EvidenceSchemaSHA256: CacheEvidenceSchemaSHA256V1(), GenerationSHA256: digestChar('3'), ContentSHA256: digestChar('4'),
	}}
}

func cacheTestResolution(t *testing.T, candidate domain.BrokerCacheQualificationCandidateV2, now time.Time) domain.BrokerResolvedCacheQualificationV2 {
	t.Helper()
	request := domain.BrokerCacheQualificationRequest{SchemaVersion: 1, Context: candidate.Context, SourcePrincipalSHA256: digestChar('5'), SourceReadScopeSHA256: digestChar('6'), Operation: candidate.Candidate.Operation, SelectorSHA256: candidate.Candidate.SelectorSHA256, ProjectionSHA256: candidate.Candidate.ProjectionSHA256, EvidenceSchemaSHA256: candidate.Candidate.EvidenceSchemaSHA256, GenerationSHA256: candidate.Candidate.GenerationSHA256, ContentSHA256: candidate.Candidate.ContentSHA256, ExpiresAtMillis: candidate.NotAfterMillis}
	requestDigest, err := CacheQualificationRequestSHA256(request)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := CacheTargetExecutionSHA256V1(candidate.Context)
	revision, _ := CacheAuthorityRevisionSHA256V1(candidate.Context.AuthorityRevision)
	candidateDigest, _ := CacheQualificationCandidateSHA256V2(candidate)
	return domain.BrokerResolvedCacheQualificationV2{SchemaVersion: 2, CandidateSHA256: candidateDigest, Request: request, Decision: domain.BrokerCacheQualification{SchemaVersion: 1, Status: domain.BrokerDecisionAllowed, IssuerSHA256: digestChar('a'), TargetExecutionSHA256: target, AuthorityRevisionSHA256: revision, RequestSHA256: requestDigest, IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(4 * time.Second).UnixMilli()}, Complete: true}
}

func TestCacheQualificationV2StrictRoundTripAndValidation(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	candidate := cacheTestCandidate(now)
	body, err := EncodeCacheQualificationCandidateV2(candidate)
	decoded, decodeErr := DecodeCacheQualificationCandidateV2(body)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, candidate) {
		t.Fatalf("candidate=%#v encode=%v decode=%v", decoded, err, decodeErr)
	}
	resolution := cacheTestResolution(t, candidate, now)
	body, err = EncodeResolvedCacheQualificationV2(resolution)
	decodedResolution, decodeErr := DecodeResolvedCacheQualificationV2(body)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decodedResolution, resolution) || ValidateResolvedCacheQualificationV2(decodedResolution, candidate, digestChar('a'), now) != nil {
		t.Fatalf("resolution=%#v encode=%v decode=%v", decodedResolution, err, decodeErr)
	}
	if _, err := DecodeResolvedCacheQualificationV2(bytes.Replace(body, []byte(`"complete":true`), []byte(`"complete":true,"source":"caller"`), 1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("unknown member error=%v", err)
	}
}

func TestCacheQualificationV2RejectsCopiedOrStaleBindings(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	candidate := cacheTestCandidate(now)
	base := cacheTestResolution(t, candidate, now)
	for name, mutate := range map[string]func(*domain.BrokerResolvedCacheQualificationV2, *domain.BrokerCacheQualificationCandidateV2){
		"candidate": func(value *domain.BrokerResolvedCacheQualificationV2, _ *domain.BrokerCacheQualificationCandidateV2) {
			value.CandidateSHA256 = digestChar('b')
		},
		"source": func(value *domain.BrokerResolvedCacheQualificationV2, _ *domain.BrokerCacheQualificationCandidateV2) {
			value.Request.SourceReadScopeSHA256 = digestChar('b')
		},
		"full-context": func(value *domain.BrokerResolvedCacheQualificationV2, _ *domain.BrokerCacheQualificationCandidateV2) {
			value.Request.Context.WorkloadID = "copied-workload"
			value.Decision.RequestSHA256, _ = CacheQualificationRequestSHA256(value.Request)
		},
		"generation": func(_ *domain.BrokerResolvedCacheQualificationV2, value *domain.BrokerCacheQualificationCandidateV2) {
			value.Candidate.GenerationSHA256 = digestChar('b')
		},
		"issuer": func(value *domain.BrokerResolvedCacheQualificationV2, _ *domain.BrokerCacheQualificationCandidateV2) {
			value.Decision.IssuerSHA256 = digestChar('b')
		},
		"request": func(value *domain.BrokerResolvedCacheQualificationV2, _ *domain.BrokerCacheQualificationCandidateV2) {
			value.Decision.RequestSHA256 = digestChar('b')
		},
		"target": func(value *domain.BrokerResolvedCacheQualificationV2, _ *domain.BrokerCacheQualificationCandidateV2) {
			value.Decision.TargetExecutionSHA256 = digestChar('b')
		},
		"revision": func(value *domain.BrokerResolvedCacheQualificationV2, _ *domain.BrokerCacheQualificationCandidateV2) {
			value.Decision.AuthorityRevisionSHA256 = digestChar('b')
		},
		"expired": func(value *domain.BrokerResolvedCacheQualificationV2, _ *domain.BrokerCacheQualificationCandidateV2) {
			value.Decision.ExpiresAtMillis = now.UnixMilli()
		},
	} {
		t.Run(name, func(t *testing.T) {
			resolution, changed := base, candidate
			mutate(&resolution, &changed)
			if ValidateResolvedCacheQualificationV2(resolution, changed, digestChar('a'), now) == nil {
				t.Fatal("altered resolution validated")
			}
		})
	}
}

func TestCacheEvidenceSchemaDigestIsStableAndDistinct(t *testing.T) {
	got := CacheEvidenceSchemaSHA256V1()
	const want = "4b27767e56368c869b65602afdad408aa248f87483633320b5da9ff66373ca01"
	if len(got) != 64 || got != strings.ToLower(got) || got != want {
		t.Fatalf("evidence schema digest=%q", got)
	}
}

func FuzzCacheQualificationV2StrictCodecs(f *testing.F) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	candidate := cacheTestCandidate(now)
	body, _ := EncodeCacheQualificationCandidateV2(candidate)
	f.Add(body)
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := DecodeCacheQualificationCandidateV2(data)
		if err != nil {
			return
		}
		canonical, err := EncodeCacheQualificationCandidateV2(value)
		if err != nil || !bytes.Equal(data, canonical) {
			t.Fatalf("non-canonical cache candidate accepted")
		}
	})
}
