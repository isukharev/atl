package domain

import "context"

const BrokerDiscoverySchemaVersionV2 = 2

type BrokerDiscoveryAccess string

const (
	BrokerDiscoveryAccessAllowed         BrokerDiscoveryAccess = "allowed"
	BrokerDiscoveryAccessRequestRequired BrokerDiscoveryAccess = "access_request_required"
	BrokerDiscoveryAccessUnavailable     BrokerDiscoveryAccess = "unavailable"
)

// BrokerDiscoveryRequestV2 is an untrusted operation-level request. It carries
// equality guards only and cannot select resources, roles, rules or grants.
type BrokerDiscoveryRequestV2 struct {
	SchemaVersion  int
	RequestID      string
	Service        string
	BrokerID       string
	Audience       string
	ContextSHA256  string
	NotAfterMillis int64
	Expect         BrokerRequestExpectations
}

// BrokerDiscoveryAuthorizationRequestV2 binds the client request to the
// authentication result before it crosses the authority adapter.
type BrokerDiscoveryAuthorizationRequestV2 struct {
	SchemaVersion int
	Request       BrokerDiscoveryRequestV2
	Context       BrokerVerifiedContext
	RequestSHA256 string
}

type BrokerDiscoveryOperationV2 struct {
	ID                       BrokerOperationID
	Version                  int
	Supported                bool
	Access                   BrokerDiscoveryAccess
	RequestAccessCorrelation string
	Features                 []string
	Limits                   BrokerLimits
	Effects                  []BrokerEffectDefinition
}

// BrokerDiscoveryProjectionV2 contains current advisory facts for one
// authenticated execution and backend service. It is never an authorization
// input for a subsequent operation.
type BrokerDiscoveryProjectionV2 struct {
	SchemaVersion         int
	RequestID             string
	RequestSHA256         string
	ContextSHA256         string
	ExecutionID           string
	ExecutionEpoch        string
	Audience              string
	BrokerID              string
	AuthorityRevision     string
	Service               string
	RegistrySHA256        string
	ContractSchemaSHA256  string
	DiscoverySchemaSHA256 string
	IssuedAtMillis        int64
	ExpiresAtMillis       int64
	Operations            []BrokerDiscoveryOperationV2
	Complete              bool
}

type BrokerDiscoveryAuthorizerV2 interface {
	DiscoverV2(context.Context, BrokerDiscoveryAuthorizationRequestV2) (BrokerDiscoveryProjectionV2, error)
}
