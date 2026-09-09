package domain

import "context"

const (
	BrokerDiscoverySchemaVersionV3  = 3
	BrokerContractFamilyExecutionV2 = "atl.broker.execution.v2"
)

// BrokerFamilyDiscoveryRequestV3 is an untrusted request for the one fixed
// execution-v2 contract family. Its context and session members are equality
// guards only; the request cannot carry authority or select a fallback family.
type BrokerFamilyDiscoveryRequestV3 struct {
	SchemaVersion  int
	RequestID      string
	ContractFamily string
	Service        string
	BrokerID       string
	Audience       string
	ContextSHA256  string
	NotAfterMillis int64
	Expect         BrokerRequestExpectations
}

// BrokerFamilyDiscoveryAuthorizationRequestV3 binds the fixed-family client
// request to a verified context before it crosses the authority boundary.
type BrokerFamilyDiscoveryAuthorizationRequestV3 struct {
	SchemaVersion int
	Request       BrokerFamilyDiscoveryRequestV3
	Context       BrokerVerifiedContext
	RequestSHA256 string
}

type BrokerFamilyDiscoveryOperationV3 struct {
	ID                       BrokerOperationID
	Version                  int
	Supported                bool
	Access                   BrokerDiscoveryAccess
	RequestAccessCorrelation string
	Features                 []string
	Limits                   BrokerLimits
	Effects                  []BrokerEffectDefinition
}

// BrokerFamilyDiscoveryProjectionV3 contains fresh advisory facts for one
// authenticated execution, backend service, and fixed contract family. It is
// never an authorization input and grants no permission to execute.
type BrokerFamilyDiscoveryProjectionV3 struct {
	SchemaVersion         int
	RequestID             string
	RequestSHA256         string
	ContextSHA256         string
	ExecutionID           string
	ExecutionEpoch        string
	Audience              string
	BrokerID              string
	AuthorityRevision     string
	ContractFamily        string
	Service               string
	RegistrySHA256        string
	ContractSchemaSHA256  string
	DiscoverySchemaSHA256 string
	IssuedAtMillis        int64
	ExpiresAtMillis       int64
	Operations            []BrokerFamilyDiscoveryOperationV3
	Complete              bool
}

// BrokerFamilyDiscoveryAuthorizerV3 is deliberately fixed to discovery v3;
// unknown or omitted families are rejected by the semantic codec.
type BrokerFamilyDiscoveryAuthorizerV3 interface {
	DiscoverFamilyV3(context.Context, BrokerFamilyDiscoveryAuthorizationRequestV3) (BrokerFamilyDiscoveryProjectionV3, error)
}

// BrokerFamilyDiscoveryReaderV3 reads a fresh advisory projection. It cannot
// authorize execution and has no family fallback.
type BrokerFamilyDiscoveryReaderV3 interface {
	DiscoverFamily(ctx context.Context, service, contractFamily string) (BrokerFamilyDiscoveryProjectionV3, error)
}
