package brokercontract

import (
	"encoding/json"
	"reflect"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const MaxCacheQualificationV2Bytes = int64(64 << 10)

type cacheCandidateWire struct {
	Operation            string `json:"operation"`
	SelectorSHA256       string `json:"selector_sha256"`
	ProjectionSHA256     string `json:"projection_sha256"`
	EvidenceSchemaSHA256 string `json:"evidence_schema_sha256"`
	GenerationSHA256     string `json:"generation_sha256"`
	ContentSHA256        string `json:"content_sha256"`
}

type cacheQualificationCandidateV2Wire struct {
	SchemaVersion  int                 `json:"schema_version"`
	Context        verifiedContextWire `json:"context"`
	Candidate      cacheCandidateWire  `json:"candidate"`
	NotAfterMillis int64               `json:"not_after_millis"`
}

type resolvedCacheQualificationV2Wire struct {
	SchemaVersion   int                           `json:"schema_version"`
	CandidateSHA256 string                        `json:"candidate_sha256"`
	Request         cacheQualificationRequestWire `json:"request"`
	Decision        cacheQualificationWire        `json:"decision"`
	Complete        bool                          `json:"complete"`
}

func EncodeCacheQualificationCandidateV2(value domain.BrokerCacheQualificationCandidateV2) ([]byte, error) {
	if validateCacheQualificationCandidateV2(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalCacheV2(cacheQualificationCandidateV2ToWire(value))
}

func DecodeCacheQualificationCandidateV2(data []byte) (domain.BrokerCacheQualificationCandidateV2, error) {
	var wire cacheQualificationCandidateV2Wire
	if !decodeCacheV2(data, &wire) {
		return domain.BrokerCacheQualificationCandidateV2{}, reject(domain.BrokerReasonMalformed)
	}
	value := cacheQualificationCandidateV2FromWire(wire)
	if validateCacheQualificationCandidateV2(value) != nil {
		return domain.BrokerCacheQualificationCandidateV2{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func CacheQualificationCandidateSHA256V2(value domain.BrokerCacheQualificationCandidateV2) (string, error) {
	if validateCacheQualificationCandidateV2(value) != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestValueInNamespace("atl.broker.cache.v2/", "candidate", cacheQualificationCandidateV2ToWire(value))
}

func CacheCandidateSHA256V1(value domain.BrokerCacheCandidate) (string, error) {
	if validateCacheCandidate(value) != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestValueInNamespace("atl.broker.cache.v1/", "candidate", cacheCandidateToWire(value))
}

func EncodeResolvedCacheQualificationV2(value domain.BrokerResolvedCacheQualificationV2) ([]byte, error) {
	if validateResolvedCacheQualificationV2(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalCacheV2(resolvedCacheQualificationV2Wire{
		SchemaVersion: value.SchemaVersion, CandidateSHA256: value.CandidateSHA256,
		Request: cacheQualificationRequestToWire(value.Request), Decision: cacheQualificationToWire(value.Decision), Complete: value.Complete,
	})
}

func DecodeResolvedCacheQualificationV2(data []byte) (domain.BrokerResolvedCacheQualificationV2, error) {
	var wire resolvedCacheQualificationV2Wire
	if !decodeCacheV2(data, &wire) {
		return domain.BrokerResolvedCacheQualificationV2{}, reject(domain.BrokerReasonMalformed)
	}
	value := domain.BrokerResolvedCacheQualificationV2{
		SchemaVersion: wire.SchemaVersion, CandidateSHA256: wire.CandidateSHA256,
		Request: cacheQualificationRequestFromWire(wire.Request), Decision: cacheQualificationFromWire(wire.Decision), Complete: wire.Complete,
	}
	if validateResolvedCacheQualificationV2(value) != nil {
		return domain.BrokerResolvedCacheQualificationV2{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

// ValidateResolvedCacheQualificationV2 proves that an authority resolved only
// the two source hashes: every other semantic-v1 request field is fixed by the
// authenticated target context and locally verified candidate.
func ValidateResolvedCacheQualificationV2(value domain.BrokerResolvedCacheQualificationV2, candidate domain.BrokerCacheQualificationCandidateV2, expectedIssuer string, now time.Time) error {
	candidateDigest, candidateErr := CacheQualificationCandidateSHA256V2(candidate)
	requestDigest, requestErr := CacheQualificationRequestSHA256(value.Request)
	targetDigest, targetErr := CacheTargetExecutionSHA256V1(candidate.Context)
	revisionDigest, revisionErr := CacheAuthorityRevisionSHA256V1(candidate.Context.AuthorityRevision)
	if candidateErr != nil || requestErr != nil || targetErr != nil || revisionErr != nil || validateResolvedCacheQualificationV2(value) != nil ||
		value.CandidateSHA256 != candidateDigest || !cacheRequestMatchesCandidate(value.Request, candidate) || value.Decision.IssuerSHA256 != expectedIssuer ||
		value.Decision.RequestSHA256 != requestDigest || value.Decision.TargetExecutionSHA256 != targetDigest || value.Decision.AuthorityRevisionSHA256 != revisionDigest {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	if value.Decision.Status != domain.BrokerDecisionAllowed {
		return reject(value.Decision.Reason)
	}
	current := now.UnixMilli()
	if current < candidate.Context.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || current < value.Decision.IssuedAtMillis-domain.BrokerClockAllowanceMillis ||
		current >= value.Decision.ExpiresAtMillis || value.Decision.ExpiresAtMillis > candidate.NotAfterMillis ||
		value.Decision.ExpiresAtMillis > candidate.Context.ExecutionExpiresMillis || value.Decision.ExpiresAtMillis > candidate.Context.GrantExpiresMillis ||
		value.Decision.ExpiresAtMillis > candidate.Context.CredentialExpiresMillis {
		return reject(domain.BrokerReasonDecisionExpired)
	}
	return nil
}

func CacheTargetExecutionSHA256V1(context domain.BrokerVerifiedContext) (string, error) {
	if validateContext(context) != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return CacheTargetExecutionClaimSHA256V1(context.BrokerID, context.Audience, context.ExecutionID, context.ExecutionEpoch)
}

// CacheTargetExecutionClaimSHA256V1 is the client-checkable execution
// projection, not a digest of the complete authenticated context. The request
// digest separately binds principal, workload, backend, and all hard expiries.
func CacheTargetExecutionClaimSHA256V1(brokerID, audience, executionID, executionEpoch string) (string, error) {
	if !validIdentifier(brokerID) || !validIdentifier(audience) || !validIdentifier(executionID) || !validIdentifier(executionEpoch) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestValueInNamespace("atl.broker.cache.v1/", "target-execution", struct {
		BrokerID, Audience, ExecutionID, ExecutionEpoch string
	}{brokerID, audience, executionID, executionEpoch})
}

func CacheAuthorityRevisionSHA256V1(revision string) (string, error) {
	if !validIdentifier(revision) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestValueInNamespace("atl.broker.cache.v1/", "authority-revision", struct {
		AuthorityRevision string `json:"authority_revision"`
	}{revision})
}

func CacheEvidenceSchemaSHA256V1() string {
	digest, err := digestValueInNamespace("atl.broker.cache.v1/", "evidence-schema", struct {
		Manifest, Receipt, Capture, Projection, CacheBinding int
		Comments, Attachments, AttachmentBodies, NestedReads bool
	}{1, 1, 1, 2, 1, false, false, false, false})
	if err != nil {
		panic("invalid cache evidence schema")
	}
	return digest
}

func validateCacheCandidate(value domain.BrokerCacheCandidate) error {
	if value.Operation != domain.BrokerOperationConfluencePageRead || value.EvidenceSchemaSHA256 != CacheEvidenceSchemaSHA256V1() {
		return reject(domain.BrokerReasonUnsupported)
	}
	for _, digest := range []string{value.SelectorSHA256, value.ProjectionSHA256, value.EvidenceSchemaSHA256, value.GenerationSHA256, value.ContentSHA256} {
		if !validDigest(digest) {
			return reject(domain.BrokerReasonMalformed)
		}
	}
	return nil
}

func validateCacheQualificationCandidateV2(value domain.BrokerCacheQualificationCandidateV2) error {
	if value.SchemaVersion != domain.BrokerCacheSchemaVersionV2 || validateContext(value.Context) != nil || validateCacheCandidate(value.Candidate) != nil ||
		value.Context.Backend.Service != "confluence" || value.NotAfterMillis <= value.Context.ExecutionNotBeforeMillis ||
		value.NotAfterMillis > value.Context.ExecutionExpiresMillis || value.NotAfterMillis > value.Context.GrantExpiresMillis || value.NotAfterMillis > value.Context.CredentialExpiresMillis {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateResolvedCacheQualificationV2(value domain.BrokerResolvedCacheQualificationV2) error {
	if value.SchemaVersion != domain.BrokerCacheSchemaVersionV2 || !validDigest(value.CandidateSHA256) || validateCacheQualificationRequest(value.Request) != nil || validateCacheQualification(value.Decision) != nil || !value.Complete {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func cacheRequestMatchesCandidate(request domain.BrokerCacheQualificationRequest, candidate domain.BrokerCacheQualificationCandidateV2) bool {
	return request.SchemaVersion == SchemaVersion && reflect.DeepEqual(request.Context, candidate.Context) &&
		request.Operation == candidate.Candidate.Operation && request.SelectorSHA256 == candidate.Candidate.SelectorSHA256 &&
		request.ProjectionSHA256 == candidate.Candidate.ProjectionSHA256 && request.EvidenceSchemaSHA256 == candidate.Candidate.EvidenceSchemaSHA256 &&
		request.GenerationSHA256 == candidate.Candidate.GenerationSHA256 && request.ContentSHA256 == candidate.Candidate.ContentSHA256 &&
		request.ExpiresAtMillis == candidate.NotAfterMillis
}

func cacheQualificationCandidateV2ToWire(value domain.BrokerCacheQualificationCandidateV2) cacheQualificationCandidateV2Wire {
	return cacheQualificationCandidateV2Wire{value.SchemaVersion, contextToWire(value.Context), cacheCandidateToWire(value.Candidate), value.NotAfterMillis}
}

func cacheQualificationCandidateV2FromWire(wire cacheQualificationCandidateV2Wire) domain.BrokerCacheQualificationCandidateV2 {
	return domain.BrokerCacheQualificationCandidateV2{SchemaVersion: wire.SchemaVersion, Context: contextFromWire(wire.Context), Candidate: cacheCandidateFromWire(wire.Candidate), NotAfterMillis: wire.NotAfterMillis}
}

func cacheCandidateToWire(value domain.BrokerCacheCandidate) cacheCandidateWire {
	return cacheCandidateWire{string(value.Operation), value.SelectorSHA256, value.ProjectionSHA256, value.EvidenceSchemaSHA256, value.GenerationSHA256, value.ContentSHA256}
}

func cacheCandidateFromWire(wire cacheCandidateWire) domain.BrokerCacheCandidate {
	return domain.BrokerCacheCandidate{Operation: domain.BrokerOperationID(wire.Operation), SelectorSHA256: wire.SelectorSHA256, ProjectionSHA256: wire.ProjectionSHA256, EvidenceSchemaSHA256: wire.EvidenceSchemaSHA256, GenerationSHA256: wire.GenerationSHA256, ContentSHA256: wire.ContentSHA256}
}

func decodeCacheV2(data []byte, target any) bool {
	return len(data) > 0 && int64(len(data)) <= MaxCacheQualificationV2Bytes && strictjson.DecodeExact(data, MaxCanonicalDepth, target) == nil
}

func marshalCacheV2(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || int64(len(encoded)) > MaxCacheQualificationV2Bytes {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return encoded, nil
}
