package domain

import (
	"context"
	"io"
	"time"
)

const (
	BrokerExecutionSchemaVersionV3 = 3
	BrokerDiscoverySchemaVersionV4 = 4

	BrokerContractFamilyExecutionV3 = "atl.broker.execution.v3"

	BrokerOperationJiraAttachmentDownload BrokerOperationID  = "jira.issue.attachment.download"
	BrokerResourceJiraAttachment          BrokerResourceKind = "jira_attachment"

	BrokerAttachmentConsistencyStepSnapshotV1 = "step_snapshot_v1"
)

type BrokerAttachmentArgumentsV3 struct {
	IssueKey     string
	AttachmentID string
}

// BrokerAttachmentRequestV3 is an untrusted semantic invocation. It contains
// equality guards only and cannot select an upstream URL or authority.
type BrokerAttachmentRequestV3 struct {
	SchemaVersion    int
	Operation        BrokerOperationID
	OperationVersion int
	RequestID        string
	Features         []string
	Expect           BrokerRequestExpectations
	Arguments        BrokerAttachmentArgumentsV3
}

type BrokerAttachmentAdmissionRequestV3 struct {
	Context          BrokerVerifiedContext
	Operation        BrokerOperationID
	OperationVersion int
	RequestID        string
	Features         []string
	Arguments        BrokerAttachmentArgumentsV3
	ArgumentsSHA256  string
	DeadlineMillis   int64
}

type BrokerAttachmentAdmissionDecisionV3 struct {
	BrokerDecisionCore
	DecisionSHA256 string
}

type BrokerAttachmentMetadataPlanV3 struct {
	SelectorSHA256 string
	MetadataFields []string
	Limits         BrokerPhaseLimits
}

// BrokerJiraAttachmentSnapshotV3 contains only qualified identity and display
// metadata. The private Jira content URI is deliberately absent.
type BrokerJiraAttachmentSnapshotV3 struct {
	IssueID                  string
	IssueKey                 string
	Project                  string
	Updated                  string
	AttachmentID             string
	ParentID                 string
	Filename                 string
	MediaType                string
	Created                  string
	DeclaredSize             int64
	IssueEvidenceSHA256      string
	AttachmentEvidenceSHA256 string
	ProjectionSHA256         string
}

type BrokerAttachmentEffectV3 struct {
	Kind         BrokerEffectKind
	ResourceKind BrokerResourceKind
	Fields       []string
}

type BrokerAttachmentQualificationPhaseV3 string

const (
	BrokerAttachmentQualificationInitial BrokerAttachmentQualificationPhaseV3 = "initial"
	BrokerAttachmentQualificationPreOpen BrokerAttachmentQualificationPhaseV3 = "pre_open"
	BrokerAttachmentQualificationRelease BrokerAttachmentQualificationPhaseV3 = "release"
)

type BrokerAttachmentReleaseKindV3 string

const (
	BrokerAttachmentReleaseData     BrokerAttachmentReleaseKindV3 = "data"
	BrokerAttachmentReleaseTerminal BrokerAttachmentReleaseKindV3 = "terminal"
)

type BrokerAttachmentReleaseCoordinateV3 struct {
	Kind   BrokerAttachmentReleaseKindV3
	Index  int
	Offset int64
}

type BrokerAttachmentInitialQualificationV3 struct {
	Admission         BrokerAttachmentAdmissionRequestV3
	AdmissionDecision BrokerAttachmentAdmissionDecisionV3
	Plan              BrokerAttachmentMetadataPlanV3
}

type BrokerAttachmentPreOpenQualificationV3 struct {
	InitialOperation         BrokerAttachmentOperationAuthorizationRequestV3
	InitialOperationDecision BrokerAttachmentOperationDecisionV3
	Plan                     BrokerAttachmentMetadataPlanV3
}

type BrokerAttachmentReleaseQualificationV3 struct {
	Context            BrokerVerifiedContext
	AnchorSHA256       string
	PriorReleaseSHA256 string
	Coordinate         BrokerAttachmentReleaseCoordinateV3
	Plan               BrokerAttachmentMetadataPlanV3
}

// BrokerAttachmentQualificationRequestV3 is a closed union selected by Phase.
// Exactly one matching member is present.
type BrokerAttachmentQualificationRequestV3 struct {
	Phase   BrokerAttachmentQualificationPhaseV3
	Initial *BrokerAttachmentInitialQualificationV3
	PreOpen *BrokerAttachmentPreOpenQualificationV3
	Release *BrokerAttachmentReleaseQualificationV3
}

type BrokerAttachmentQualificationDecisionV3 struct {
	Phase BrokerAttachmentQualificationPhaseV3
	BrokerDecisionCore
	PlanSHA256     string
	DecisionSHA256 string
}

type BrokerAttachmentOperationPhaseV3 string

const (
	BrokerAttachmentOperationInitial      BrokerAttachmentOperationPhaseV3 = "initial"
	BrokerAttachmentOperationBodyDispatch BrokerAttachmentOperationPhaseV3 = "body_dispatch"
	BrokerAttachmentOperationRelease      BrokerAttachmentOperationPhaseV3 = "release"
)

type BrokerAttachmentQualifiedOperationV3 struct {
	QualificationRequest  BrokerAttachmentQualificationRequestV3
	QualificationDecision BrokerAttachmentQualificationDecisionV3
	Snapshot              BrokerJiraAttachmentSnapshotV3
	Effects               []BrokerAttachmentEffectV3
}

type BrokerAttachmentReleaseTerminalFactsV3 struct {
	ChunkCount  int
	TotalBytes  int64
	WholeSHA256 string
	EOFProven   bool
	Complete    bool
}

type BrokerAttachmentDataReleaseV3 struct {
	Index            int
	Offset           int64
	DecodedBytes     int64
	PayloadSHA256    string
	CumulativeBytes  int64
	CumulativeSHA256 string
	ResourceSHA256   string
	Terminal         *BrokerAttachmentReleaseTerminalFactsV3
}

// BrokerAttachmentReleaseFactsV3 is a closed union selected by Kind.
type BrokerAttachmentReleaseFactsV3 struct {
	Kind     BrokerAttachmentReleaseKindV3
	Data     *BrokerAttachmentDataReleaseV3
	Terminal *BrokerAttachmentReleaseTerminalFactsV3
}

type BrokerAttachmentReleaseOperationV3 struct {
	QualificationRequest  BrokerAttachmentQualificationRequestV3
	QualificationDecision BrokerAttachmentQualificationDecisionV3
	AnchorSHA256          string
	PriorReleaseSHA256    string
	SnapshotSHA256        string
	ManifestCoreSHA256    string
	Facts                 BrokerAttachmentReleaseFactsV3
}

// BrokerAttachmentOperationAuthorizationRequestV3 is a closed union selected
// by Phase. Initial and body-dispatch use Qualified; release uses Release.
type BrokerAttachmentOperationAuthorizationRequestV3 struct {
	Phase     BrokerAttachmentOperationPhaseV3
	Qualified *BrokerAttachmentQualifiedOperationV3
	Release   *BrokerAttachmentReleaseOperationV3
}

type BrokerAttachmentOperationDecisionV3 struct {
	Phase BrokerAttachmentOperationPhaseV3
	BrokerDecisionCore
	QualificationDecisionSHA256 string
	Operation                   BrokerOperationID
	OperationVersion            int
	ArgumentsSHA256             string
	ResourcesSHA256             string
	EffectsSHA256               string
	AnchorSHA256                string
	PriorReleaseSHA256          string
	ManifestCoreSHA256          string
	ReleaseFactsSHA256          string
	DecisionSHA256              string
}

// BrokerAttachmentStreamAnchorV3 is immutable historical continuity. Its
// setup decisions are not current authorization after their leases expire.
type BrokerAttachmentStreamAnchorV3 struct {
	SchemaVersion                       int
	ContractFamily                      string
	Operation                           BrokerOperationID
	OperationVersion                    int
	Features                            []string
	Context                             BrokerVerifiedContext
	RequestSHA256                       string
	ArgumentsSHA256                     string
	ResourcesSHA256                     string
	EffectsSHA256                       string
	SnapshotSHA256                      string
	StreamID                            string
	CorrelationID                       string
	OverallDeadlineMillis               int64
	AdmissionDecisionSHA256             string
	InitialQualificationDecisionSHA256  string
	InitialOperationDecisionSHA256      string
	PreOpenQualificationDecisionSHA256  string
	BodyDispatchOperationDecisionSHA256 string
}

type BrokerAttachmentManifestCoreV3 struct {
	SchemaVersion                  int
	FrameVersion                   int
	StreamID                       string
	CorrelationID                  string
	ArgumentsSHA256                string
	AnchorSHA256                   string
	Snapshot                       BrokerJiraAttachmentSnapshotV3
	ConsistencyProfile             string
	MaxDataFrames                  int
	MaxDecodedFrameBytes           int64
	MaxNativeBodyBytes             int64
	MaxMetadataItems               int
	MaxManifestLineBytes           int64
	MaxDataLineBytes               int64
	MaxTerminalLineBytes           int64
	MaxFramedBytes                 int64
	MaxJiraAttempts                int
	MaxAuthenticationAttempts      int
	MaxDecisionAttempts            int
	MaxTotalHostOutboundAttempts   int
	MaxCommandHostOutboundAttempts int
	MaxJiraResponseBytes           int64
	MaxAuthorityResponseBytes      int64
	MaxTotalHostResponseBytes      int64
	MaxOperationMillis             int64
	MaxDecisionLeaseMillis         int64
}

type BrokerAttachmentManifestLineV3 struct {
	Core                  BrokerAttachmentManifestCoreV3
	ManifestCoreSHA256    string
	ReleaseDecisionSHA256 string
}

type BrokerAttachmentDataLineV3 struct {
	SchemaVersion         int
	FrameVersion          int
	Kind                  BrokerAttachmentReleaseKindV3
	StreamID              string
	Index                 int
	Offset                int64
	DecodedBytes          int64
	PayloadBase64         string
	PayloadSHA256         string
	CumulativeBytes       int64
	CumulativeSHA256      string
	ResourceSHA256        string
	PriorReleaseSHA256    string
	ReleaseDecisionSHA256 string
}

type BrokerAttachmentTerminalLineV3 struct {
	SchemaVersion         int
	FrameVersion          int
	Kind                  BrokerAttachmentReleaseKindV3
	StreamID              string
	ChunkCount            int
	TotalBytes            int64
	DeclaredSize          int64
	WholeSHA256           string
	PriorReleaseSHA256    string
	ReleaseDecisionSHA256 string
	EOFProven             bool
	Complete              bool
}

type BrokerJiraAttachmentOpenHandleV3 interface {
	Snapshot() BrokerJiraAttachmentSnapshotV3
	Open(context.Context, time.Time) (io.ReadCloser, error)
	Close() error
}

type BrokerJiraAttachmentPortV3 interface {
	QualifyBrokerJiraAttachment(context.Context, string, string) (BrokerJiraAttachmentSnapshotV3, error)
	PrepareBrokerJiraAttachment(context.Context, BrokerJiraAttachmentSnapshotV3) (BrokerJiraAttachmentOpenHandleV3, error)
}

// BrokerAttachmentAuthorizerV3 keeps the three fixed authority routes
// distinct. Phase unions remain closed inside qualification and operation.
type BrokerAttachmentAuthorizerV3 interface {
	AdmitAttachment(context.Context, BrokerAttachmentAdmissionRequestV3) (BrokerAttachmentAdmissionDecisionV3, error)
	AuthorizeAttachmentQualification(context.Context, BrokerAttachmentQualificationRequestV3) (BrokerAttachmentQualificationDecisionV3, error)
	AuthorizeAttachmentOperation(context.Context, BrokerAttachmentOperationAuthorizationRequestV3) (BrokerAttachmentOperationDecisionV3, error)
}

type BrokerAttachmentOperationDefinitionV3 struct {
	Definition                     BrokerOperationDefinition
	MaxMetadataItems               int
	MaxJiraAttempts                int
	MaxAuthenticationAttempts      int
	MaxDecisionAttempts            int
	MaxTotalHostOutboundAttempts   int
	MaxCommandHostOutboundAttempts int
	MaxJiraResponseBytes           int64
	MaxAuthorityResponseBytes      int64
	MaxTotalHostResponseBytes      int64
	MaxManifestLineBytes           int64
	MaxDataLineBytes               int64
	MaxTerminalLineBytes           int64
	MaxFramedResponseBytes         int64
}

type BrokerFamilyDiscoveryRequestV4 struct {
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

type BrokerFamilyDiscoveryAuthorizationRequestV4 struct {
	SchemaVersion int
	Request       BrokerFamilyDiscoveryRequestV4
	Context       BrokerVerifiedContext
	RequestSHA256 string
}

type BrokerFamilyDiscoveryOperationV4 struct {
	ID                             BrokerOperationID
	Version                        int
	Supported                      bool
	Access                         BrokerDiscoveryAccess
	RequestAccessCorrelation       string
	Features                       []string
	Limits                         BrokerLimits
	Effects                        []BrokerEffectDefinition
	MaxMetadataItems               int
	MaxJiraAttempts                int
	MaxAuthenticationAttempts      int
	MaxDecisionAttempts            int
	MaxTotalHostOutboundAttempts   int
	MaxCommandHostOutboundAttempts int
	MaxJiraResponseBytes           int64
	MaxAuthorityResponseBytes      int64
	MaxTotalHostResponseBytes      int64
	MaxManifestLineBytes           int64
	MaxDataLineBytes               int64
	MaxTerminalLineBytes           int64
	MaxFramedResponseBytes         int64
}

type BrokerFamilyDiscoveryProjectionV4 struct {
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
	Operations            []BrokerFamilyDiscoveryOperationV4
	Complete              bool
}

type BrokerFamilyDiscoveryAuthorizerV4 interface {
	DiscoverFamilyV4(context.Context, BrokerFamilyDiscoveryAuthorizationRequestV4) (BrokerFamilyDiscoveryProjectionV4, error)
}
