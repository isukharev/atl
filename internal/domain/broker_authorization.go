package domain

import "context"

type BrokerDecisionStatus string

const (
	BrokerDecisionAllowed     BrokerDecisionStatus = "allowed"
	BrokerDecisionDenied      BrokerDecisionStatus = "denied"
	BrokerDecisionUnavailable BrokerDecisionStatus = "unavailable"
)

type BrokerAuthorizationPhase string

const (
	BrokerPhaseStrictDecode               BrokerAuthorizationPhase = "strict_decode"
	BrokerPhaseAdmission                  BrokerAuthorizationPhase = "admission"
	BrokerPhaseQualificationAuthorization BrokerAuthorizationPhase = "qualification_authorization"
	BrokerPhaseQualification              BrokerAuthorizationPhase = "qualification"
	BrokerPhaseFinalAuthorization         BrokerAuthorizationPhase = "final_authorization"
	BrokerPhaseProposalClearance          BrokerAuthorizationPhase = "proposal_clearance"
	BrokerPhaseBusinessOperation          BrokerAuthorizationPhase = "business_operation"
	BrokerPhaseOutcomeObservation         BrokerAuthorizationPhase = "outcome_observation"
)

type BrokerReason string

const (
	BrokerReasonUnsupported               BrokerReason = "unsupported"
	BrokerReasonMalformed                 BrokerReason = "malformed"
	BrokerReasonDenied                    BrokerReason = "denied"
	BrokerReasonRevoked                   BrokerReason = "revoked"
	BrokerReasonGrantExpired              BrokerReason = "grant_expired"
	BrokerReasonCredentialExpired         BrokerReason = "credential_expired" // #nosec G101 -- closed protocol reason, not a credential
	BrokerReasonStaleExecution            BrokerReason = "stale_execution"
	BrokerReasonStaleAuthority            BrokerReason = "stale_authority"
	BrokerReasonDecisionExpired           BrokerReason = "decision_expired"
	BrokerReasonAuthorizationUnavailable  BrokerReason = "authorization_unavailable"
	BrokerReasonUnsupportedConsistency    BrokerReason = "unsupported_consistency"
	BrokerReasonProposalClearanceRequired BrokerReason = "proposal_clearance_required"
	BrokerReasonOperationIDConflict       BrokerReason = "operation_id_conflict"
	BrokerReasonOutcomeUnknown            BrokerReason = "outcome_unknown"
)

type BrokerAdmissionRequest struct {
	Context          BrokerVerifiedContext
	Operation        BrokerOperationID
	OperationVersion int
	RequestID        string
	Features         []string
	Arguments        BrokerOperationArguments
	ArgumentsSHA256  string
	DeadlineMillis   int64
}

type BrokerDecisionCore struct {
	Status            BrokerDecisionStatus
	Reason            BrokerReason
	DecisionID        string
	AuthorityRevision string
	ContextSHA256     string
	RequestSHA256     string
	IssuedAtMillis    int64
	ExpiresAtMillis   int64
}

type BrokerAdmissionDecision struct {
	BrokerDecisionCore
	DecisionSHA256 string
}

type BrokerQualificationRequest struct {
	Admission         BrokerAdmissionRequest
	AdmissionDecision BrokerAdmissionDecision
	Plan              BrokerQualificationPlan
}

type BrokerQualificationDecision struct {
	BrokerDecisionCore
	AdmissionRequestSHA256  string
	AdmissionDecisionSHA256 string
	PlanSHA256              string
	DecisionSHA256          string
}

type BrokerOperationAuthorizationRequest struct {
	QualificationRequest  BrokerQualificationRequest
	QualificationDecision BrokerQualificationDecision
	QualifiedResources    []BrokerQualifiedResource
	Effects               []BrokerEffect
}

type BrokerOperationDecision struct {
	BrokerDecisionCore
	QualificationDecisionSHA256 string
	Operation                   BrokerOperationID
	OperationVersion            int
	ArgumentsSHA256             string
	ResourcesSHA256             string
	EffectsSHA256               string
	DecisionSHA256              string
}

type BrokerProposalAuthorizationRequest struct {
	OperationRequest      BrokerOperationAuthorizationRequest
	OperationDecision     BrokerOperationDecision
	ProposalSchemaVersion int
	ProposalHash          string
	NativeCandidateSHA256 string
	VersionEvidenceSHA256 string
}

type BrokerProposalClearance struct {
	BrokerDecisionCore
	OperationDecisionSHA256 string
	ProposalHash            string
	NativeCandidateSHA256   string
	VersionEvidenceSHA256   string
	ClearanceSHA256         string
}

// BrokerAuthorizer keeps the four authorization phases distinct. An allowed
// earlier phase never implies an allowed later phase.
type BrokerAuthorizer interface {
	Admit(context.Context, BrokerAdmissionRequest) (BrokerAdmissionDecision, error)
	AuthorizeQualification(context.Context, BrokerQualificationRequest) (BrokerQualificationDecision, error)
	AuthorizeOperation(context.Context, BrokerOperationAuthorizationRequest) (BrokerOperationDecision, error)
	AuthorizeProposal(context.Context, BrokerProposalAuthorizationRequest) (BrokerProposalClearance, error)
}

type BrokerDiscoveryAuthorizer interface {
	Discover(context.Context, BrokerVerifiedContext) (BrokerDiscoveryProjection, error)
}

type BrokerCacheAuthorizer interface {
	QualifyCache(context.Context, BrokerCacheQualificationRequest) (BrokerCacheQualification, error)
}
