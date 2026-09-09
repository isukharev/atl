package domain

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"time"
)

const BrokerCacheSchemaVersionV2 = 2

// BrokerCacheCandidate is derived from one fully verified immutable corpus
// generation. It contains no source identity or read-scope claim: only the
// external authority may resolve those from its trusted capture inventory.
type BrokerCacheCandidate struct {
	Operation            BrokerOperationID
	SelectorSHA256       string
	ProjectionSHA256     string
	EvidenceSchemaSHA256 string
	GenerationSHA256     string
	ContentSHA256        string
}

// BrokerCacheQualificationCandidateV2 binds a cache candidate to the target
// context authenticated by the Broker before it crosses the authority port.
type BrokerCacheQualificationCandidateV2 struct {
	SchemaVersion  int
	Context        BrokerVerifiedContext
	Candidate      BrokerCacheCandidate
	NotAfterMillis int64
}

// BrokerResolvedCacheQualificationV2 is returned only by an authority that
// resolved source identity and scope from a trusted capture tuple. Request and
// Decision retain the existing strict semantic-v1 wire and digest contracts.
type BrokerResolvedCacheQualificationV2 struct {
	SchemaVersion   int
	CandidateSHA256 string
	Request         BrokerCacheQualificationRequest
	Decision        BrokerCacheQualification
	Complete        bool
}

// BrokerCacheGrant is the client-visible result. Source bindings stay inside
// the Broker/authority trust boundary.
type BrokerCacheGrant struct {
	Decision                BrokerCacheQualification
	CandidateSHA256         string
	TargetExecutionSHA256   string
	AuthorityRevisionSHA256 string
	RequestSHA256           string
	ReleaseDeadlineMillis   int64
	Lease                   BrokerCacheGrantLease `json:"-"`
}

// BrokerCacheGrantLease is process-local proof of the original session and
// monotonic release bound. Its fields are deliberately opaque and never enter
// a wire or JSON result.
type BrokerCacheGrantLease struct {
	credentialSHA256 [sha256.Size]byte
	releaseDeadline  time.Time
	initialized      bool
}

func NewBrokerCacheGrantLease(credential []byte, releaseDeadline time.Time) BrokerCacheGrantLease {
	if len(credential) == 0 || releaseDeadline.IsZero() {
		return BrokerCacheGrantLease{}
	}
	return BrokerCacheGrantLease{credentialSHA256: sha256.Sum256(credential), releaseDeadline: releaseDeadline, initialized: true}
}

func (lease BrokerCacheGrantLease) Active(now time.Time) bool {
	return lease.initialized && !now.IsZero() && now.Before(lease.releaseDeadline)
}

func (lease BrokerCacheGrantLease) Matches(credential []byte, now time.Time) bool {
	if !lease.Active(now) || len(credential) == 0 {
		return false
	}
	digest := sha256.Sum256(credential)
	return subtle.ConstantTimeCompare(lease.credentialSHA256[:], digest[:]) == 1
}

// BrokerResolvedCacheAuthorizerV2 resolves trusted source bindings and makes
// one current decision. Implementations must not trust caller-supplied hashes
// as proof that a capture tuple exists.
type BrokerResolvedCacheAuthorizerV2 interface {
	ResolveAndQualifyCacheV2(context.Context, BrokerCacheQualificationCandidateV2) (BrokerResolvedCacheQualificationV2, error)
}

// BrokerCacheQualificationReader is the narrow client port used by the
// qualified corpus handoff. It cannot select a backend or disclose source
// bindings.
type BrokerCacheQualificationReader interface {
	QualifyCache(context.Context, BrokerCacheCandidate) (BrokerCacheGrant, error)
	ValidateCacheGrant(BrokerCacheCandidate, BrokerCacheGrant) error
}
