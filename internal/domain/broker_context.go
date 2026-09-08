package domain

const (
	BrokerSchemaVersion             = 1
	BrokerMaxIdentifierBytes        = 128
	BrokerMaxAudienceBytes          = 128
	BrokerMaxAuthorityRevisionBytes = 128
	BrokerMaxDigestBytes            = 64
	BrokerMaxOperationMillis        = int64(60_000)
	BrokerMaxDecisionLeaseMillis    = int64(5_000)
	BrokerClockAllowanceMillis      = int64(1_000)
)

// BrokerBackendBinding identifies one server-owned upstream without exposing
// its URL or credentials. OriginSHA256 is the lowercase SHA-256 of its origin.
type BrokerBackendBinding struct {
	Service           string
	OriginSHA256      string
	WorkloadBackendID string
}

// BrokerVerifiedContext is supplied only by a trusted authentication adapter.
// Structural validity does not prove that the context was authenticated.
type BrokerVerifiedContext struct {
	PrincipalID              string
	WorkloadID               string
	ExecutionID              string
	ExecutionEpoch           string
	Audience                 string
	BrokerID                 string
	AuthorityRevision        string
	ExecutionNotBeforeMillis int64
	ExecutionExpiresMillis   int64
	GrantExpiresMillis       int64
	CredentialExpiresMillis  int64
	Backend                  BrokerBackendBinding
}

// BrokerRequestExpectations are client-supplied equality guards. They cannot
// select a principal, construct authority, or extend any lifetime.
type BrokerRequestExpectations struct {
	ExecutionID       string
	ExecutionEpoch    string
	AuthorityRevision string
}

// BrokerRequest carries one untrusted semantic invocation. Principal, roles,
// grants, backend destinations and authorization decisions are absent by
// construction.
type BrokerRequest struct {
	SchemaVersion    int
	Operation        BrokerOperationID
	OperationVersion int
	RequestID        string
	Features         []string
	Expect           BrokerRequestExpectations
	Arguments        BrokerOperationArguments
}
