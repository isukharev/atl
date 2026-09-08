package brokercontract

import "encoding/json"

type expectationsWire struct {
	ExecutionID       string `json:"execution_id"`
	ExecutionEpoch    string `json:"execution_epoch"`
	AuthorityRevision string `json:"authority_revision"`
}

type requestWire struct {
	SchemaVersion    int              `json:"schema_version"`
	Operation        string           `json:"operation"`
	OperationVersion int              `json:"operation_version"`
	RequestID        string           `json:"request_id"`
	Features         []string         `json:"features"`
	Expect           expectationsWire `json:"expect"`
	Arguments        json.RawMessage  `json:"arguments"`
}

type jiraIssueReadArgumentsWire struct {
	IssueKey string   `json:"issue_key"`
	Fields   []string `json:"fields"`
}

type confluencePageReadArgumentsWire struct {
	PageID     string `json:"page_id"`
	Projection string `json:"projection"`
}

type jiraCommentArgumentsWire struct {
	IssueKey             string `json:"issue_key"`
	NativeBodyBase64     string `json:"native_body_base64"`
	SatisfactionPolicy   string `json:"satisfaction_policy"`
	ExpectedProposalHash string `json:"expected_proposal_hash,omitempty"`
	OperationTicket      string `json:"operation_ticket,omitempty"`
}

type outcomeArgumentsWire struct {
	OperationTicket string `json:"operation_ticket"`
}

type jiraIssueReadFieldWire struct {
	Field   string `json:"field"`
	Present bool   `json:"present"`
	Null    bool   `json:"null"`
	Value   string `json:"value"`
}

type jiraIssueReadResultWire struct {
	SchemaVersion   int                      `json:"schema_version"`
	ArgumentsSHA256 string                   `json:"arguments_sha256"`
	IssueID         string                   `json:"issue_id"`
	Key             string                   `json:"key"`
	Project         string                   `json:"project"`
	Updated         string                   `json:"updated"`
	Fields          []jiraIssueReadFieldWire `json:"fields"`
	Complete        bool                     `json:"complete"`
}

type confluencePageReadResultWire struct {
	SchemaVersion   int    `json:"schema_version"`
	ArgumentsSHA256 string `json:"arguments_sha256"`
	PageID          string `json:"page_id"`
	Type            string `json:"type"`
	Space           string `json:"space"`
	Version         int    `json:"version"`
	Title           string `json:"title"`
	Updated         string `json:"updated"`
	Projection      string `json:"projection"`
	StorageBase64   string `json:"storage_base64"`
	StoragePresent  bool   `json:"storage_present"`
	Complete        bool   `json:"complete"`
}

type jiraCommentResultWire struct {
	SchemaVersion         int    `json:"schema_version"`
	ArgumentsSHA256       string `json:"arguments_sha256"`
	OperationTicket       string `json:"operation_ticket"`
	Mode                  string `json:"mode"`
	Status                string `json:"status"`
	ProposalHash          string `json:"proposal_hash"`
	NativeCandidateSHA256 string `json:"native_candidate_sha256"`
	VersionEvidenceSHA256 string `json:"version_evidence_sha256"`
	CommentID             string `json:"comment_id,omitempty"`
	WriteAttempted        bool   `json:"write_attempted"`
	Complete              bool   `json:"complete"`
	Reconciled            bool   `json:"reconciled"`
}

type backendBindingWire struct {
	Service           string `json:"service"`
	OriginSHA256      string `json:"origin_sha256"`
	WorkloadBackendID string `json:"workload_backend_id"`
}

type verifiedContextWire struct {
	SchemaVersion            int                `json:"schema_version"`
	PrincipalID              string             `json:"principal_id"`
	WorkloadID               string             `json:"workload_id"`
	ExecutionID              string             `json:"execution_id"`
	ExecutionEpoch           string             `json:"execution_epoch"`
	Audience                 string             `json:"audience"`
	BrokerID                 string             `json:"broker_id"`
	AuthorityRevision        string             `json:"authority_revision"`
	ExecutionNotBeforeMillis int64              `json:"execution_not_before_millis"`
	ExecutionExpiresMillis   int64              `json:"execution_expires_millis"`
	GrantExpiresMillis       int64              `json:"grant_expires_millis"`
	CredentialExpiresMillis  int64              `json:"credential_expires_millis"`
	Backend                  backendBindingWire `json:"backend"`
}

type decisionCoreWire struct {
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
	DecisionID        string `json:"decision_id"`
	AuthorityRevision string `json:"authority_revision"`
	ContextSHA256     string `json:"context_sha256"`
	RequestSHA256     string `json:"request_sha256"`
	IssuedAtMillis    int64  `json:"issued_at_millis"`
	ExpiresAtMillis   int64  `json:"expires_at_millis"`
}

type admissionDecisionWire struct {
	SchemaVersion  int              `json:"schema_version"`
	Decision       decisionCoreWire `json:"decision"`
	DecisionSHA256 string           `json:"decision_sha256"`
}

type admissionRequestWire struct {
	SchemaVersion    int                 `json:"schema_version"`
	Context          verifiedContextWire `json:"context"`
	Operation        string              `json:"operation"`
	OperationVersion int                 `json:"operation_version"`
	RequestID        string              `json:"request_id"`
	Features         []string            `json:"features"`
	Arguments        json.RawMessage     `json:"arguments"`
	ArgumentsSHA256  string              `json:"arguments_sha256"`
	DeadlineMillis   int64               `json:"deadline_millis"`
}

type qualificationPlanWire struct {
	SelectorSHA256 string          `json:"selector_sha256"`
	MetadataFields []string        `json:"metadata_fields"`
	Limits         phaseLimitsWire `json:"limits"`
}

type qualificationRequestWire struct {
	SchemaVersion     int                   `json:"schema_version"`
	Admission         admissionRequestWire  `json:"admission"`
	AdmissionDecision admissionDecisionWire `json:"admission_decision"`
	Plan              qualificationPlanWire `json:"plan"`
}

type qualificationDecisionWire struct {
	SchemaVersion           int              `json:"schema_version"`
	Decision                decisionCoreWire `json:"decision"`
	AdmissionRequestSHA256  string           `json:"admission_request_sha256"`
	AdmissionDecisionSHA256 string           `json:"admission_decision_sha256"`
	PlanSHA256              string           `json:"plan_sha256"`
	DecisionSHA256          string           `json:"decision_sha256"`
}

type qualifiedResourceWire struct {
	Kind             string   `json:"kind"`
	ImmutableID      string   `json:"immutable_id"`
	Key              string   `json:"key"`
	Project          string   `json:"project"`
	Space            string   `json:"space"`
	AncestorIDs      []string `json:"ancestor_ids"`
	AncestorsPresent bool     `json:"ancestors_present"`
	VersionEvidence  string   `json:"version_evidence_sha256"`
	ProjectionSHA256 string   `json:"projection_sha256"`
}

type effectWire struct {
	Kind     string                `json:"kind"`
	Resource qualifiedResourceWire `json:"resource"`
	Fields   []string              `json:"fields"`
}

type operationAuthorizationRequestWire struct {
	SchemaVersion         int                       `json:"schema_version"`
	QualificationRequest  qualificationRequestWire  `json:"qualification_request"`
	QualificationDecision qualificationDecisionWire `json:"qualification_decision"`
	QualifiedResources    []qualifiedResourceWire   `json:"qualified_resources"`
	Effects               []effectWire              `json:"effects"`
}

type operationDecisionWire struct {
	SchemaVersion               int              `json:"schema_version"`
	Decision                    decisionCoreWire `json:"decision"`
	QualificationDecisionSHA256 string           `json:"qualification_decision_sha256"`
	Operation                   string           `json:"operation"`
	OperationVersion            int              `json:"operation_version"`
	ArgumentsSHA256             string           `json:"arguments_sha256"`
	ResourcesSHA256             string           `json:"resources_sha256"`
	EffectsSHA256               string           `json:"effects_sha256"`
	DecisionSHA256              string           `json:"decision_sha256"`
}

type proposalAuthorizationRequestWire struct {
	SchemaVersion         int                               `json:"schema_version"`
	OperationRequest      operationAuthorizationRequestWire `json:"operation_request"`
	OperationDecision     operationDecisionWire             `json:"operation_decision"`
	ProposalSchemaVersion int                               `json:"proposal_schema_version"`
	ProposalHash          string                            `json:"proposal_hash"`
	NativeCandidateSHA256 string                            `json:"native_candidate_sha256"`
	VersionEvidenceSHA256 string                            `json:"version_evidence_sha256"`
}

type proposalClearanceWire struct {
	SchemaVersion           int              `json:"schema_version"`
	Decision                decisionCoreWire `json:"decision"`
	OperationDecisionSHA256 string           `json:"operation_decision_sha256"`
	ProposalHash            string           `json:"proposal_hash"`
	NativeCandidateSHA256   string           `json:"native_candidate_sha256"`
	VersionEvidenceSHA256   string           `json:"version_evidence_sha256"`
	ClearanceSHA256         string           `json:"clearance_sha256"`
}

type limitsWire struct {
	MaxRequestBytes               int64           `json:"max_request_bytes"`
	MaxResponseBytes              int64           `json:"max_response_bytes"`
	MaxTotalUpstreamRequests      int             `json:"max_total_upstream_requests"`
	MaxTotalUpstreamResponseBytes int64           `json:"max_total_upstream_response_bytes"`
	Qualification                 phaseLimitsWire `json:"qualification"`
	Business                      phaseLimitsWire `json:"business"`
	MaxResources                  int             `json:"max_resources"`
	MaxFields                     int             `json:"max_fields"`
	MaxNativeBodyBytes            int64           `json:"max_native_body_bytes"`
	MaxStreamChunks               int             `json:"max_stream_chunks"`
	MaxStreamChunkBytes           int64           `json:"max_stream_chunk_bytes"`
	MaxOperationMillis            int64           `json:"max_operation_millis"`
	MaxDecisionLeaseMillis        int64           `json:"max_decision_lease_millis"`
}

type phaseLimitsWire struct {
	MaxRequests      int   `json:"max_requests"`
	MaxResponseBytes int64 `json:"max_response_bytes"`
}

type discoveryOperationWire struct {
	ID           string     `json:"id"`
	Version      int        `json:"version"`
	Availability string     `json:"availability"`
	Features     []string   `json:"features"`
	Limits       limitsWire `json:"limits"`
}

type discoveryWire struct {
	SchemaVersion        int                      `json:"schema_version"`
	ExecutionScopeSHA256 string                   `json:"execution_scope_sha256"`
	RegistrySHA256       string                   `json:"registry_sha256"`
	IssuedAtMillis       int64                    `json:"issued_at_millis"`
	ExpiresAtMillis      int64                    `json:"expires_at_millis"`
	Operations           []discoveryOperationWire `json:"operations"`
	Complete             bool                     `json:"complete"`
}

type executionProjectionWire struct {
	SchemaVersion     int    `json:"schema_version"`
	ExecutionID       string `json:"execution_id"`
	ExecutionEpoch    string `json:"execution_epoch"`
	Audience          string `json:"audience"`
	AuthorityRevision string `json:"authority_revision"`
	ExpiresAtMillis   int64  `json:"expires_at_millis"`
	ScopeSHA256       string `json:"scope_sha256"`
}

type ticketWire struct {
	SchemaVersion     int    `json:"schema_version"`
	OperationID       string `json:"operation_id"`
	PrincipalSHA256   string `json:"principal_sha256"`
	ExecutionSHA256   string `json:"execution_sha256"`
	AudienceSHA256    string `json:"audience_sha256"`
	BackendSHA256     string `json:"backend_sha256"`
	Operation         string `json:"operation"`
	OperationVersion  int    `json:"operation_version"`
	ArgumentsSHA256   string `json:"arguments_sha256"`
	ProposalSHA256    string `json:"proposal_sha256,omitempty"`
	IssuedAtMillis    int64  `json:"issued_at_millis"`
	AcceptUntilMillis int64  `json:"accept_until_millis"`
}

type outcomeWire struct {
	SchemaVersion    int    `json:"schema_version"`
	TicketSHA256     string `json:"ticket_sha256"`
	Phase            string `json:"phase"`
	ObservedAtMillis int64  `json:"observed_at_millis"`
	ResultSHA256     string `json:"result_sha256,omitempty"`
	Complete         bool   `json:"complete"`
	Reconciled       bool   `json:"reconciled"`
}

type cacheQualificationWire struct {
	SchemaVersion           int    `json:"schema_version"`
	Status                  string `json:"status"`
	Reason                  string `json:"reason,omitempty"`
	IssuerSHA256            string `json:"issuer_sha256"`
	TargetExecutionSHA256   string `json:"target_execution_sha256"`
	AuthorityRevisionSHA256 string `json:"authority_revision_sha256"`
	RequestSHA256           string `json:"request_sha256"`
	IssuedAtMillis          int64  `json:"issued_at_millis"`
	ExpiresAtMillis         int64  `json:"expires_at_millis"`
}

type cacheQualificationRequestWire struct {
	SchemaVersion         int                 `json:"schema_version"`
	Context               verifiedContextWire `json:"context"`
	SourcePrincipalSHA256 string              `json:"source_principal_sha256"`
	SourceReadScopeSHA256 string              `json:"source_read_scope_sha256"`
	Operation             string              `json:"operation"`
	SelectorSHA256        string              `json:"selector_sha256"`
	ProjectionSHA256      string              `json:"projection_sha256"`
	EvidenceSchemaSHA256  string              `json:"evidence_schema_sha256"`
	GenerationSHA256      string              `json:"generation_sha256"`
	ContentSHA256         string              `json:"content_sha256"`
	ExpiresAtMillis       int64               `json:"expires_at_millis"`
}
