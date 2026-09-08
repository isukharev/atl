package domain

type BrokerAvailability string

const (
	BrokerAvailabilityAvailable   BrokerAvailability = "available"
	BrokerAvailabilityUnsupported BrokerAvailability = "unsupported"
	BrokerAvailabilityUnavailable BrokerAvailability = "unavailable"
)

type BrokerDiscoveryOperation struct {
	ID           BrokerOperationID
	Version      int
	Availability BrokerAvailability
	Features     []string
	Limits       BrokerLimits
}

type BrokerDiscoveryProjection struct {
	SchemaVersion        int
	ExecutionScopeSHA256 string
	RegistrySHA256       string
	IssuedAtMillis       int64
	ExpiresAtMillis      int64
	Operations           []BrokerDiscoveryOperation
	Complete             bool
}

type BrokerExecutionProjection struct {
	SchemaVersion     int
	ExecutionID       string
	ExecutionEpoch    string
	Audience          string
	AuthorityRevision string
	ExpiresAtMillis   int64
	ScopeSHA256       string
}

type BrokerOperationPhase string

const (
	BrokerOperationAdmitted             BrokerOperationPhase = "admitted"
	BrokerOperationDispatching          BrokerOperationPhase = "dispatching"
	BrokerOperationApplied              BrokerOperationPhase = "applied"
	BrokerOperationNotApplied           BrokerOperationPhase = "not_applied"
	BrokerOperationOutcomeUnknown       BrokerOperationPhase = "outcome_unknown"
	BrokerOperationRetiredNonReplayable BrokerOperationPhase = "retired_non_replayable"
)

type BrokerOperationTicket struct {
	SchemaVersion     int
	OperationID       string
	PrincipalSHA256   string
	ExecutionSHA256   string
	AudienceSHA256    string
	BackendSHA256     string
	Operation         BrokerOperationID
	OperationVersion  int
	ArgumentsSHA256   string
	ProposalSHA256    string
	IssuedAtMillis    int64
	AcceptUntilMillis int64
}

type BrokerOperationOutcome struct {
	SchemaVersion    int
	TicketSHA256     string
	Phase            BrokerOperationPhase
	ObservedAtMillis int64
	ResultSHA256     string
	Complete         bool
	Reconciled       bool
}

type BrokerCacheQualificationRequest struct {
	SchemaVersion         int
	Context               BrokerVerifiedContext
	SourcePrincipalSHA256 string
	SourceReadScopeSHA256 string
	Operation             BrokerOperationID
	SelectorSHA256        string
	ProjectionSHA256      string
	EvidenceSchemaSHA256  string
	GenerationSHA256      string
	ContentSHA256         string
	ExpiresAtMillis       int64
}

type BrokerCacheQualification struct {
	SchemaVersion           int
	Status                  BrokerDecisionStatus
	Reason                  BrokerReason
	IssuerSHA256            string
	TargetExecutionSHA256   string
	AuthorityRevisionSHA256 string
	RequestSHA256           string
	IssuedAtMillis          int64
	ExpiresAtMillis         int64
}
