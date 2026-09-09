package domain

import "context"

const (
	BrokerExecutionSchemaVersionV2 = 2
	BrokerProjectPageMaxStartAt    = 1_000_000
	BrokerProjectPageMaxResults    = 15

	BrokerOperationJiraProjectIssuePageRead BrokerOperationID  = "jira.project.issue_page.read"
	BrokerResourceJiraProject               BrokerResourceKind = "jira_project"
)

type BrokerProjectPageField string

const (
	BrokerProjectPageFieldDescription BrokerProjectPageField = "description"
	BrokerProjectPageFieldSummary     BrokerProjectPageField = "summary"
)

type BrokerProjectPageArguments struct {
	ProjectKey string
	Fields     []BrokerProjectPageField
	StartAt    int
	MaxResults int
}

// BrokerProjectPageRequestV2 is one untrusted semantic page request. It has no
// JQL, URL, principal, role, policy, credential, or backend selector.
type BrokerProjectPageRequestV2 struct {
	SchemaVersion    int
	Operation        BrokerOperationID
	OperationVersion int
	RequestID        string
	Features         []string
	Expect           BrokerRequestExpectations
	Arguments        BrokerProjectPageArguments
}

type BrokerProjectPageAdmissionRequestV2 struct {
	Context          BrokerVerifiedContext
	Operation        BrokerOperationID
	OperationVersion int
	RequestID        string
	Features         []string
	Arguments        BrokerProjectPageArguments
	ArgumentsSHA256  string
	DeadlineMillis   int64
}

type BrokerProjectPageAdmissionDecisionV2 struct {
	BrokerDecisionCore
	DecisionSHA256 string
}

type BrokerProjectPageQualificationStepKind string

const (
	BrokerProjectPageQualificationProject BrokerProjectPageQualificationStepKind = "jira_project_identity"
	BrokerProjectPageQualificationIssues  BrokerProjectPageQualificationStepKind = "jira_project_issue_page_identity"
)

type BrokerProjectPageQualificationStepV2 struct {
	Kind           BrokerProjectPageQualificationStepKind
	MetadataFields []string
	Limits         BrokerPhaseLimits
}

type BrokerProjectPageQualificationPlanV2 struct {
	SelectorSHA256 string
	Steps          []BrokerProjectPageQualificationStepV2
	Limits         BrokerPhaseLimits
}

type BrokerProjectPageQualificationRequestV2 struct {
	Admission         BrokerProjectPageAdmissionRequestV2
	AdmissionDecision BrokerProjectPageAdmissionDecisionV2
	Plan              BrokerProjectPageQualificationPlanV2
}

type BrokerProjectPageQualificationDecisionV2 struct {
	BrokerDecisionCore
	AdmissionRequestSHA256  string
	AdmissionDecisionSHA256 string
	PlanSHA256              string
	DecisionSHA256          string
}

type BrokerJiraProjectIdentityV2 struct {
	ID       string
	Key      string
	Complete bool
}

type BrokerJiraProjectPageIssueIdentityV2 struct {
	ID         string
	Key        string
	ProjectID  string
	ProjectKey string
	Updated    string
	Complete   bool
}

// BrokerJiraProjectPageIdentitySnapshotV2 is the content-free identity page
// admitted during qualification. Issues preserve Jira's exact returned order.
type BrokerJiraProjectPageIdentitySnapshotV2 struct {
	StartAt             int
	MaxResults          int
	Total               int
	Issues              []BrokerJiraProjectPageIssueIdentityV2
	CoordinateExhausted bool
	PaginationStalled   bool
	Complete            bool
}

type BrokerJiraProjectPageIssueSnapshotV2 struct {
	Identity BrokerJiraProjectPageIssueIdentityV2
	Fields   []BrokerJiraIssueReadField
}

type BrokerJiraProjectPageSnapshotV2 struct {
	StartAt             int
	MaxResults          int
	Total               int
	Issues              []BrokerJiraProjectPageIssueSnapshotV2
	CoordinateExhausted bool
	PaginationStalled   bool
	Complete            bool
}

type BrokerQualifiedJiraProjectV2 struct {
	ID                       string
	Key                      string
	IdentityProjectionSHA256 string
}

type BrokerQualifiedJiraProjectPageIssueV2 struct {
	ID                    string
	Key                   string
	ProjectID             string
	ProjectKey            string
	Updated               string
	VersionEvidenceSHA256 string
	ProjectionSHA256      string
}

// BrokerProjectPageEvidenceV2 preserves the backend page order separately
// from the canonical resource/effect order used for authorization digests.
type BrokerProjectPageEvidenceV2 struct {
	RequestedStartAt    int
	RequestedMaxResults int
	ReturnedStartAt     int
	ReturnedMaxResults  int
	Total               int
	CoordinateExhausted bool
	PaginationStalled   bool
	SelectionComplete   bool
	OrderedIssueIDs     []string
}

// BrokerProjectPageEffectV2 is a closed tagged union. Exactly one resource
// member is present and the full qualified resource is repeated in the effect.
type BrokerProjectPageEffectV2 struct {
	Kind    BrokerEffectKind
	Project *BrokerQualifiedJiraProjectV2
	Issue   *BrokerQualifiedJiraProjectPageIssueV2
	Fields  []string
}

type BrokerProjectPageOperationAuthorizationRequestV2 struct {
	QualificationRequest  BrokerProjectPageQualificationRequestV2
	QualificationDecision BrokerProjectPageQualificationDecisionV2
	Project               BrokerQualifiedJiraProjectV2
	Issues                []BrokerQualifiedJiraProjectPageIssueV2
	Page                  BrokerProjectPageEvidenceV2
	Effects               []BrokerProjectPageEffectV2
}

type BrokerProjectPageOperationDecisionV2 struct {
	BrokerDecisionCore
	QualificationDecisionSHA256 string
	Operation                   BrokerOperationID
	OperationVersion            int
	ArgumentsSHA256             string
	ResourcesSHA256             string
	PageSHA256                  string
	EffectsSHA256               string
	DecisionSHA256              string
}

type BrokerJiraProjectPageResultIssueV2 struct {
	ID         string
	Key        string
	ProjectID  string
	ProjectKey string
	Updated    string
	Fields     []BrokerJiraIssueReadField
}

type BrokerJiraProjectPageResultPageV2 struct {
	StartAt             int
	MaxResults          int
	Total               int
	Count               int
	NextCursor          string
	NextCursorPresent   bool
	CoordinateExhausted bool
	SelectionComplete   bool
	PartialReason       string
}

type BrokerJiraProjectPageResultV2 struct {
	SchemaVersion      int
	ArgumentsSHA256    string
	ConsistencyProfile string
	ProjectID          string
	ProjectKey         string
	Issues             []BrokerJiraProjectPageResultIssueV2
	Page               BrokerJiraProjectPageResultPageV2
	Complete           bool
}

type BrokerProjectPageQualificationStepDefinitionV2 struct {
	Kind           BrokerProjectPageQualificationStepKind
	MetadataFields []string
	Limits         BrokerPhaseLimits
}

// BrokerProjectPageOperationDefinitionV2 wraps the existing operation
// definition vocabulary while binding the ordered qualification steps and the
// independently bounded effect count required by this semantic family.
type BrokerProjectPageOperationDefinitionV2 struct {
	Definition         BrokerOperationDefinition
	QualificationSteps []BrokerProjectPageQualificationStepDefinitionV2
	MaxEffects         int
}

// BrokerJiraProjectPageReadPort exposes only the three fixed reads admitted by
// the project-page operation. Implementations own the configured destination
// and construct the closed numeric-project JQL themselves.
type BrokerJiraProjectPageReadPort interface {
	BrokerOriginSHA256() (string, error)
	QualifyBrokerProject(context.Context, string) (BrokerJiraProjectIdentityV2, error)
	QualifyBrokerProjectIssuePage(context.Context, string, int, int) (BrokerJiraProjectPageIdentitySnapshotV2, error)
	ReadBrokerProjectIssuePage(context.Context, string, []BrokerProjectPageField, int, int) (BrokerJiraProjectPageSnapshotV2, error)
}

// BrokerProjectPageAuthorizerV2 keeps the v2 page phases separate without
// widening the immutable v1 BrokerAuthorizer contract.
type BrokerProjectPageAuthorizerV2 interface {
	AdmitProjectPage(context.Context, BrokerProjectPageAdmissionRequestV2) (BrokerProjectPageAdmissionDecisionV2, error)
	AuthorizeProjectPageQualification(context.Context, BrokerProjectPageQualificationRequestV2) (BrokerProjectPageQualificationDecisionV2, error)
	AuthorizeProjectPage(context.Context, BrokerProjectPageOperationAuthorizationRequestV2) (BrokerProjectPageOperationDecisionV2, error)
}
