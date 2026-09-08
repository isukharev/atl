package domain

type BrokerOperationID string

const (
	BrokerOperationJiraIssueRead      BrokerOperationID = "jira.issue.read"
	BrokerOperationConfluencePageRead BrokerOperationID = "confluence.page.read"
	BrokerOperationJiraCommentPreview BrokerOperationID = "jira.comment.preview"
	BrokerOperationJiraCommentApply   BrokerOperationID = "jira.comment.apply"
	BrokerOperationOutcomeLookup      BrokerOperationID = "broker.operation.outcome"
)

type BrokerJiraIssueField string

const (
	BrokerJiraIssueFieldSummary     BrokerJiraIssueField = "summary"
	BrokerJiraIssueFieldDescription BrokerJiraIssueField = "description"
	BrokerJiraIssueFieldUpdated     BrokerJiraIssueField = "updated"
)

type BrokerConfluenceProjection string

const (
	BrokerConfluenceProjectionMetadata BrokerConfluenceProjection = "metadata"
	BrokerConfluenceProjectionStorage  BrokerConfluenceProjection = "storage"
)

type BrokerJiraIssueReadArguments struct {
	IssueKey string
	Fields   []BrokerJiraIssueField
}

type BrokerConfluencePageReadArguments struct {
	PageID     string
	Projection BrokerConfluenceProjection
}

type BrokerJiraCommentArguments struct {
	IssueKey             string
	NativeBody           []byte
	SatisfactionPolicy   string
	ExpectedProposalHash string
	OperationTicket      string
}

type BrokerOutcomeArguments struct {
	OperationTicket string
}

// BrokerOperationArguments is a closed tagged union selected by Operation.
// Exactly one matching member is present.
type BrokerOperationArguments struct {
	JiraIssueRead      *BrokerJiraIssueReadArguments
	ConfluencePageRead *BrokerConfluencePageReadArguments
	JiraComment        *BrokerJiraCommentArguments
	Outcome            *BrokerOutcomeArguments
}

type BrokerLimits struct {
	MaxRequestBytes               int64
	MaxResponseBytes              int64
	MaxTotalUpstreamRequests      int
	MaxTotalUpstreamResponseBytes int64
	Qualification                 BrokerPhaseLimits
	Business                      BrokerPhaseLimits
	MaxResources                  int
	MaxFields                     int
	MaxNativeBodyBytes            int64
	MaxStreamChunks               int
	MaxStreamChunkBytes           int64
	MaxOperationMillis            int64
	MaxDecisionLeaseMillis        int64
}

type BrokerPhaseLimits struct {
	MaxRequests      int
	MaxResponseBytes int64
}

type BrokerEffectKind string

const (
	BrokerEffectRead    BrokerEffectKind = "read"
	BrokerEffectComment BrokerEffectKind = "comment"
	BrokerEffectObserve BrokerEffectKind = "observe"
)

type BrokerResourceKind string

const (
	BrokerResourceJiraIssue      BrokerResourceKind = "jira_issue"
	BrokerResourceConfluencePage BrokerResourceKind = "confluence_page"
	BrokerResourceOperation      BrokerResourceKind = "operation"
)

type BrokerEffectDefinition struct {
	Kind         BrokerEffectKind
	ResourceKind BrokerResourceKind
	Fields       []string
}

type BrokerOperationDefinition struct {
	ID                   BrokerOperationID
	Version              int
	ArgumentSchemaID     string
	ResultSchemaID       string
	ContractSchemaSHA256 string
	QualificationProfile string
	ExecutionProfile     string
	BackendService       string
	QualificationFields  []string
	RequiredFeatures     []string
	Available            bool
	Streaming            bool
	Limits               BrokerLimits
	Effects              []BrokerEffectDefinition
}

// BrokerQualifiedResource preserves presence separately from empty values so
// an unresolved hierarchy cannot be serialized as a proved root resource.
type BrokerQualifiedResource struct {
	Kind             BrokerResourceKind
	ImmutableID      string
	Key              string
	Project          string
	Space            string
	AncestorIDs      []string
	AncestorsPresent bool
	VersionEvidence  string
	ProjectionSHA256 string
}

type BrokerEffect struct {
	Kind     BrokerEffectKind
	Resource BrokerQualifiedResource
	Fields   []string
}

type BrokerQualificationPlan struct {
	SelectorSHA256 string
	MetadataFields []string
	Limits         BrokerPhaseLimits
}
