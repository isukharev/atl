package brokertransport

import (
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const (
	CacheQualificationPathV2     = "/v2/cache/qualify"
	MaxCacheQualificationBytesV2 = int64(64 << 10)
)

//go:embed schema/broker-cache-http-v2.schema.json
var cacheHTTPV2 []byte

func CacheSchemaV2() []byte { return append([]byte(nil), cacheHTTPV2...) }
func CacheSchemaSHA256V2() string {
	digest := sha256.Sum256(cacheHTTPV2)
	return hex.EncodeToString(digest[:])
}

type CacheQualificationClaimV2 struct {
	SchemaVersion  int                              `json:"schema_version"`
	RequestID      string                           `json:"request_id"`
	Service        string                           `json:"service"`
	BrokerID       string                           `json:"broker_id"`
	Audience       string                           `json:"audience"`
	Expect         domain.BrokerRequestExpectations `json:"-"`
	NotAfterMillis int64                            `json:"not_after_millis"`
	Candidate      domain.BrokerCacheCandidate      `json:"-"`
}

type cacheQualificationClaimV2Wire struct {
	SchemaVersion        int    `json:"schema_version"`
	RequestID            string `json:"request_id"`
	Service              string `json:"service"`
	BrokerID             string `json:"broker_id"`
	Audience             string `json:"audience"`
	ExecutionID          string `json:"execution_id"`
	ExecutionEpoch       string `json:"execution_epoch"`
	AuthorityRevision    string `json:"authority_revision"`
	NotAfterMillis       int64  `json:"not_after_millis"`
	Operation            string `json:"operation"`
	SelectorSHA256       string `json:"selector_sha256"`
	ProjectionSHA256     string `json:"projection_sha256"`
	EvidenceSchemaSHA256 string `json:"evidence_schema_sha256"`
	GenerationSHA256     string `json:"generation_sha256"`
	ContentSHA256        string `json:"content_sha256"`
}

type CacheQualificationEnvelopeV2 struct {
	SchemaVersion           int
	TransportSchemaSHA256   string
	ClaimSHA256             string
	CandidateSHA256         string
	TargetExecutionSHA256   string
	AuthorityRevisionSHA256 string
	RequestSHA256           string
	Qualification           domain.BrokerCacheQualification
	ReleaseDeadlineMillis   int64
	Complete                bool
}

type cacheQualificationEnvelopeV2Wire struct {
	SchemaVersion           int             `json:"schema_version"`
	TransportSchemaSHA256   string          `json:"transport_schema_sha256"`
	ClaimSHA256             string          `json:"claim_sha256"`
	CandidateSHA256         string          `json:"candidate_sha256"`
	TargetExecutionSHA256   string          `json:"target_execution_sha256"`
	AuthorityRevisionSHA256 string          `json:"authority_revision_sha256"`
	RequestSHA256           string          `json:"request_sha256"`
	Qualification           json.RawMessage `json:"qualification"`
	ReleaseDeadlineMillis   int64           `json:"release_deadline_millis"`
	Complete                bool            `json:"complete"`
}

func EncodeCacheQualificationClaimV2(value CacheQualificationClaimV2) ([]byte, error) {
	if !validCacheQualificationClaimV2(value) {
		return nil, malformedError()
	}
	return marshalBounded(cacheQualificationClaimToWire(value), MaxCacheQualificationBytesV2)
}

func DecodeCacheQualificationClaimV2(data []byte) (CacheQualificationClaimV2, error) {
	var wire cacheQualificationClaimV2Wire
	if !decodeExact(data, MaxCacheQualificationBytesV2, &wire) {
		return CacheQualificationClaimV2{}, malformedError()
	}
	value := cacheQualificationClaimFromWire(wire)
	if !validCacheQualificationClaimV2(value) {
		return CacheQualificationClaimV2{}, malformedError()
	}
	return value, nil
}

func CacheQualificationClaimSHA256V2(value CacheQualificationClaimV2) (string, error) {
	if !validCacheQualificationClaimV2(value) {
		return "", malformedError()
	}
	body, err := json.Marshal(cacheQualificationClaimToWire(value))
	if err != nil {
		return "", malformedError()
	}
	hash := sha256.New()
	writeTransportHashPart(hash, []byte("atl.broker.cache.http.v2/claim"))
	writeTransportHashPart(hash, body)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func BindCacheQualificationClaimV2(value CacheQualificationClaimV2, verified domain.BrokerVerifiedContext, started, now, authenticationDeadline time.Time) (domain.BrokerCacheQualificationCandidateV2, time.Time, error) {
	if !validCacheQualificationClaimV2(value) || verified.Backend.Service != value.Service || verified.BrokerID != value.BrokerID || verified.Audience != value.Audience ||
		verified.ExecutionID != value.Expect.ExecutionID || verified.ExecutionEpoch != value.Expect.ExecutionEpoch {
		return domain.BrokerCacheQualificationCandidateV2{}, time.Time{}, cacheRejected(domain.BrokerReasonStaleExecution)
	}
	if verified.AuthorityRevision != value.Expect.AuthorityRevision {
		return domain.BrokerCacheQualificationCandidateV2{}, time.Time{}, cacheRejected(domain.BrokerReasonStaleAuthority)
	}
	deadline := started.Add(time.Duration(domain.BrokerMaxDecisionLeaseMillis) * time.Millisecond)
	for _, candidate := range []time.Time{authenticationDeadline, time.UnixMilli(value.NotAfterMillis), time.UnixMilli(verified.ExecutionExpiresMillis), time.UnixMilli(verified.GrantExpiresMillis), time.UnixMilli(verified.CredentialExpiresMillis)} {
		if candidate.Before(deadline) {
			deadline = candidate
		}
	}
	if now.UnixMilli() < verified.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || !now.Before(deadline) {
		return domain.BrokerCacheQualificationCandidateV2{}, time.Time{}, cacheRejected(domain.BrokerReasonDecisionExpired)
	}
	candidate := domain.BrokerCacheQualificationCandidateV2{SchemaVersion: domain.BrokerCacheSchemaVersionV2, Context: verified, Candidate: value.Candidate, NotAfterMillis: deadline.UnixMilli()}
	if _, err := brokercontract.EncodeCacheQualificationCandidateV2(candidate); err != nil {
		return domain.BrokerCacheQualificationCandidateV2{}, time.Time{}, cacheRejected(domain.BrokerReasonMalformed)
	}
	return candidate, deadline, nil
}

func NewCacheQualificationEnvelopeV2(claim CacheQualificationClaimV2, resolved domain.BrokerResolvedCacheQualificationV2, releaseDeadline time.Time) (CacheQualificationEnvelopeV2, error) {
	claimDigest, err := CacheQualificationClaimSHA256V2(claim)
	if err != nil || releaseDeadline.IsZero() || releaseDeadline.UnixMilli() > resolved.Decision.ExpiresAtMillis {
		return CacheQualificationEnvelopeV2{}, malformedError()
	}
	value := CacheQualificationEnvelopeV2{
		SchemaVersion: 2, TransportSchemaSHA256: CacheSchemaSHA256V2(), ClaimSHA256: claimDigest,
		CandidateSHA256:       mustCacheCandidateDigest(claim.Candidate),
		TargetExecutionSHA256: resolved.Decision.TargetExecutionSHA256, AuthorityRevisionSHA256: resolved.Decision.AuthorityRevisionSHA256,
		RequestSHA256: resolved.Decision.RequestSHA256, Qualification: resolved.Decision,
		ReleaseDeadlineMillis: releaseDeadline.UnixMilli(), Complete: true,
	}
	if !validCacheQualificationEnvelopeV2(value) {
		return CacheQualificationEnvelopeV2{}, malformedError()
	}
	return value, nil
}

func EncodeCacheQualificationEnvelopeV2(value CacheQualificationEnvelopeV2) ([]byte, error) {
	if !validCacheQualificationEnvelopeV2(value) {
		return nil, malformedError()
	}
	qualification, err := brokercontract.EncodeCacheQualificationV1(value.Qualification)
	if err != nil {
		return nil, malformedError()
	}
	wire := cacheQualificationEnvelopeV2Wire{value.SchemaVersion, value.TransportSchemaSHA256, value.ClaimSHA256, value.CandidateSHA256, value.TargetExecutionSHA256, value.AuthorityRevisionSHA256, value.RequestSHA256, qualification, value.ReleaseDeadlineMillis, value.Complete}
	return marshalBounded(wire, MaxCacheQualificationBytesV2)
}

func DecodeCacheQualificationEnvelopeV2(data []byte) (CacheQualificationEnvelopeV2, error) {
	var wire cacheQualificationEnvelopeV2Wire
	if !decodeExact(data, MaxCacheQualificationBytesV2, &wire) {
		return CacheQualificationEnvelopeV2{}, malformedError()
	}
	qualification, err := brokercontract.DecodeCacheQualificationV1(wire.Qualification)
	value := CacheQualificationEnvelopeV2{wire.SchemaVersion, wire.TransportSchemaSHA256, wire.ClaimSHA256, wire.CandidateSHA256, wire.TargetExecutionSHA256, wire.AuthorityRevisionSHA256, wire.RequestSHA256, qualification, wire.ReleaseDeadlineMillis, wire.Complete}
	if err != nil || !validCacheQualificationEnvelopeV2(value) {
		return CacheQualificationEnvelopeV2{}, malformedError()
	}
	return value, nil
}

func ValidateCacheQualificationEnvelopeV2(value CacheQualificationEnvelopeV2, claim CacheQualificationClaimV2, now time.Time) error {
	claimDigest, claimErr := CacheQualificationClaimSHA256V2(claim)
	candidateDigest, candidateErr := brokercontract.CacheCandidateSHA256V1(claim.Candidate)
	targetDigest, targetErr := brokercontract.CacheTargetExecutionClaimSHA256V1(claim.BrokerID, claim.Audience, claim.Expect.ExecutionID, claim.Expect.ExecutionEpoch)
	revisionDigest, revisionErr := brokercontract.CacheAuthorityRevisionSHA256V1(claim.Expect.AuthorityRevision)
	if claimErr != nil || candidateErr != nil || targetErr != nil || revisionErr != nil || !validCacheQualificationEnvelopeV2(value) ||
		value.ClaimSHA256 != claimDigest || value.CandidateSHA256 != candidateDigest || value.TargetExecutionSHA256 != targetDigest || value.AuthorityRevisionSHA256 != revisionDigest ||
		value.RequestSHA256 != value.Qualification.RequestSHA256 || value.TargetExecutionSHA256 != value.Qualification.TargetExecutionSHA256 ||
		value.AuthorityRevisionSHA256 != value.Qualification.AuthorityRevisionSHA256 || value.Qualification.Status != domain.BrokerDecisionAllowed ||
		value.Qualification.Reason != "" || value.ReleaseDeadlineMillis > value.Qualification.ExpiresAtMillis || value.ReleaseDeadlineMillis > claim.NotAfterMillis ||
		value.Qualification.ExpiresAtMillis > claim.NotAfterMillis ||
		now.UnixMilli() < value.Qualification.IssuedAtMillis-domain.BrokerClockAllowanceMillis || now.UnixMilli() >= value.ReleaseDeadlineMillis {
		return cacheRejected(domain.BrokerReasonStaleAuthority)
	}
	return nil
}

func EncodeCacheFailureV2(reason domain.BrokerReason) ([]byte, error) {
	if !validCacheFailureReason(reason) {
		return nil, malformedError()
	}
	return EncodeDiscoveryFailureV2(reason)
}
func DecodeCacheFailureV2(data []byte) (Failure, error) {
	value, err := DecodeDiscoveryFailureV2(data)
	if err != nil || !validCacheFailureReason(value.Reason) {
		return Failure{}, malformedError()
	}
	return value, nil
}

func validCacheFailureReason(reason domain.BrokerReason) bool {
	switch reason {
	case domain.BrokerReasonUnsupported, domain.BrokerReasonMalformed, domain.BrokerReasonDenied, domain.BrokerReasonRevoked,
		domain.BrokerReasonGrantExpired, domain.BrokerReasonCredentialExpired, domain.BrokerReasonStaleExecution,
		domain.BrokerReasonStaleAuthority, domain.BrokerReasonDecisionExpired, domain.BrokerReasonAuthorizationUnavailable:
		return true
	default:
		return false
	}
}

func validCacheQualificationClaimV2(value CacheQualificationClaimV2) bool {
	if value.SchemaVersion != 2 || !validIdentifier(value.RequestID) || value.Service != "confluence" || !validIdentifier(value.BrokerID) || !validIdentifier(value.Audience) ||
		!validIdentifier(value.Expect.ExecutionID) || !validIdentifier(value.Expect.ExecutionEpoch) || !validIdentifier(value.Expect.AuthorityRevision) || value.NotAfterMillis <= 0 ||
		value.Candidate.Operation != domain.BrokerOperationConfluencePageRead || value.Candidate.EvidenceSchemaSHA256 != brokercontract.CacheEvidenceSchemaSHA256V1() {
		return false
	}
	for _, digest := range []string{value.Candidate.SelectorSHA256, value.Candidate.ProjectionSHA256, value.Candidate.EvidenceSchemaSHA256, value.Candidate.GenerationSHA256, value.Candidate.ContentSHA256} {
		if !validDigest(digest) {
			return false
		}
	}
	return true
}

func validCacheQualificationEnvelopeV2(value CacheQualificationEnvelopeV2) bool {
	encoded, err := brokercontract.EncodeCacheQualificationV1(value.Qualification)
	return value.SchemaVersion == 2 && value.TransportSchemaSHA256 == CacheSchemaSHA256V2() && validDigest(value.ClaimSHA256) &&
		validDigest(value.CandidateSHA256) && validDigest(value.TargetExecutionSHA256) && validDigest(value.AuthorityRevisionSHA256) && validDigest(value.RequestSHA256) && err == nil && len(encoded) > 0 &&
		value.RequestSHA256 == value.Qualification.RequestSHA256 && value.TargetExecutionSHA256 == value.Qualification.TargetExecutionSHA256 &&
		value.AuthorityRevisionSHA256 == value.Qualification.AuthorityRevisionSHA256 && value.ReleaseDeadlineMillis > 0 && value.ReleaseDeadlineMillis <= value.Qualification.ExpiresAtMillis && value.Complete
}

func cacheQualificationClaimToWire(value CacheQualificationClaimV2) cacheQualificationClaimV2Wire {
	return cacheQualificationClaimV2Wire{value.SchemaVersion, value.RequestID, value.Service, value.BrokerID, value.Audience, value.Expect.ExecutionID, value.Expect.ExecutionEpoch, value.Expect.AuthorityRevision, value.NotAfterMillis, string(value.Candidate.Operation), value.Candidate.SelectorSHA256, value.Candidate.ProjectionSHA256, value.Candidate.EvidenceSchemaSHA256, value.Candidate.GenerationSHA256, value.Candidate.ContentSHA256}
}

func cacheQualificationClaimFromWire(wire cacheQualificationClaimV2Wire) CacheQualificationClaimV2 {
	return CacheQualificationClaimV2{SchemaVersion: wire.SchemaVersion, RequestID: wire.RequestID, Service: wire.Service, BrokerID: wire.BrokerID, Audience: wire.Audience,
		Expect: domain.BrokerRequestExpectations{ExecutionID: wire.ExecutionID, ExecutionEpoch: wire.ExecutionEpoch, AuthorityRevision: wire.AuthorityRevision}, NotAfterMillis: wire.NotAfterMillis,
		Candidate: domain.BrokerCacheCandidate{Operation: domain.BrokerOperationID(wire.Operation), SelectorSHA256: wire.SelectorSHA256, ProjectionSHA256: wire.ProjectionSHA256, EvidenceSchemaSHA256: wire.EvidenceSchemaSHA256, GenerationSHA256: wire.GenerationSHA256, ContentSHA256: wire.ContentSHA256}}
}

func cacheRejected(reason domain.BrokerReason) error {
	_, err := brokercontract.ErrorForReason(reason)
	return err
}

func mustCacheCandidateDigest(candidate domain.BrokerCacheCandidate) string {
	digest, _ := brokercontract.CacheCandidateSHA256V1(candidate)
	return digest
}

func writeTransportHashPart(hash interface{ Write([]byte) (int, error) }, part []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(part)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write(part)
}
