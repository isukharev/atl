package brokercontract

type projectPageArgumentsWireV2 struct {
	ProjectKey string   `json:"project_key"`
	Fields     []string `json:"fields"`
	StartAt    int      `json:"start_at"`
	MaxResults int      `json:"max_results"`
}

type projectPageRequestWireV2 struct {
	SchemaVersion    int                        `json:"schema_version"`
	Operation        string                     `json:"operation"`
	OperationVersion int                        `json:"operation_version"`
	RequestID        string                     `json:"request_id"`
	Features         []string                   `json:"features"`
	Expect           expectationsWire           `json:"expect"`
	Arguments        projectPageArgumentsWireV2 `json:"arguments"`
}

type projectPageAdmissionRequestWireV2 struct {
	SchemaVersion    int                        `json:"schema_version"`
	Context          verifiedContextWire        `json:"context"`
	Operation        string                     `json:"operation"`
	OperationVersion int                        `json:"operation_version"`
	RequestID        string                     `json:"request_id"`
	Features         []string                   `json:"features"`
	Arguments        projectPageArgumentsWireV2 `json:"arguments"`
	ArgumentsSHA256  string                     `json:"arguments_sha256"`
	DeadlineMillis   int64                      `json:"deadline_millis"`
}

type projectPageAdmissionDecisionWireV2 struct {
	SchemaVersion  int              `json:"schema_version"`
	Decision       decisionCoreWire `json:"decision"`
	DecisionSHA256 string           `json:"decision_sha256"`
}

type projectPageQualificationStepWireV2 struct {
	Kind           string          `json:"kind"`
	MetadataFields []string        `json:"metadata_fields"`
	Limits         phaseLimitsWire `json:"limits"`
}

type projectPageQualificationPlanWireV2 struct {
	SelectorSHA256 string                               `json:"selector_sha256"`
	Steps          []projectPageQualificationStepWireV2 `json:"steps"`
	Limits         phaseLimitsWire                      `json:"limits"`
}

type projectPageQualificationRequestWireV2 struct {
	SchemaVersion     int                                `json:"schema_version"`
	Admission         projectPageAdmissionRequestWireV2  `json:"admission"`
	AdmissionDecision projectPageAdmissionDecisionWireV2 `json:"admission_decision"`
	Plan              projectPageQualificationPlanWireV2 `json:"plan"`
}

type projectPageQualificationDecisionWireV2 struct {
	SchemaVersion           int              `json:"schema_version"`
	Decision                decisionCoreWire `json:"decision"`
	AdmissionRequestSHA256  string           `json:"admission_request_sha256"`
	AdmissionDecisionSHA256 string           `json:"admission_decision_sha256"`
	PlanSHA256              string           `json:"plan_sha256"`
	DecisionSHA256          string           `json:"decision_sha256"`
}

type qualifiedJiraProjectWireV2 struct {
	ID                       string `json:"id"`
	Key                      string `json:"key"`
	IdentityProjectionSHA256 string `json:"identity_projection_sha256"`
}

type qualifiedJiraProjectPageIssueWireV2 struct {
	ID                    string `json:"id"`
	Key                   string `json:"key"`
	ProjectID             string `json:"project_id"`
	ProjectKey            string `json:"project_key"`
	Updated               string `json:"updated"`
	VersionEvidenceSHA256 string `json:"version_evidence_sha256"`
	ProjectionSHA256      string `json:"projection_sha256"`
}

type projectPageEvidenceWireV2 struct {
	RequestedStartAt    int      `json:"requested_start_at"`
	RequestedMaxResults int      `json:"requested_max_results"`
	ReturnedStartAt     int      `json:"returned_start_at"`
	ReturnedMaxResults  int      `json:"returned_max_results"`
	Total               int      `json:"total"`
	CoordinateExhausted bool     `json:"coordinate_exhausted"`
	PaginationStalled   bool     `json:"pagination_stalled"`
	SelectionComplete   bool     `json:"selection_complete"`
	OrderedIssueIDs     []string `json:"ordered_issue_ids"`
}

type projectPageEffectWireV2 struct {
	Kind    string                               `json:"kind"`
	Project *qualifiedJiraProjectWireV2          `json:"project,omitempty"`
	Issue   *qualifiedJiraProjectPageIssueWireV2 `json:"issue,omitempty"`
	Fields  []string                             `json:"fields"`
}

type projectPageOperationAuthorizationRequestWireV2 struct {
	SchemaVersion         int                                    `json:"schema_version"`
	QualificationRequest  projectPageQualificationRequestWireV2  `json:"qualification_request"`
	QualificationDecision projectPageQualificationDecisionWireV2 `json:"qualification_decision"`
	Project               qualifiedJiraProjectWireV2             `json:"project"`
	Issues                []qualifiedJiraProjectPageIssueWireV2  `json:"issues"`
	Page                  projectPageEvidenceWireV2              `json:"page"`
	Effects               []projectPageEffectWireV2              `json:"effects"`
}

type projectPageOperationDecisionWireV2 struct {
	SchemaVersion               int              `json:"schema_version"`
	Decision                    decisionCoreWire `json:"decision"`
	QualificationDecisionSHA256 string           `json:"qualification_decision_sha256"`
	Operation                   string           `json:"operation"`
	OperationVersion            int              `json:"operation_version"`
	ArgumentsSHA256             string           `json:"arguments_sha256"`
	ResourcesSHA256             string           `json:"resources_sha256"`
	PageSHA256                  string           `json:"page_sha256"`
	EffectsSHA256               string           `json:"effects_sha256"`
	DecisionSHA256              string           `json:"decision_sha256"`
}

type projectPageResultIssueWireV2 struct {
	ID         string                   `json:"id"`
	Key        string                   `json:"key"`
	ProjectID  string                   `json:"project_id"`
	ProjectKey string                   `json:"project_key"`
	Updated    string                   `json:"updated"`
	Fields     []jiraIssueReadFieldWire `json:"fields"`
}

type projectPageResultPageWireV2 struct {
	StartAt             int     `json:"start_at"`
	MaxResults          int     `json:"max_results"`
	Total               int     `json:"total"`
	Count               int     `json:"count"`
	NextCursor          *string `json:"next_cursor,omitempty"`
	CoordinateExhausted bool    `json:"coordinate_exhausted"`
	SelectionComplete   bool    `json:"selection_complete"`
	PartialReason       string  `json:"partial_reason,omitempty"`
}

type projectPageResultWireV2 struct {
	SchemaVersion      int                            `json:"schema_version"`
	ArgumentsSHA256    string                         `json:"arguments_sha256"`
	ConsistencyProfile string                         `json:"consistency_profile"`
	ProjectID          string                         `json:"project_id"`
	ProjectKey         string                         `json:"project_key"`
	Issues             []projectPageResultIssueWireV2 `json:"issues"`
	Page               projectPageResultPageWireV2    `json:"page"`
	Complete           bool                           `json:"complete"`
}
