package brokercontract

import "encoding/json"

type attachmentArgumentsWireV3 struct {
	IssueKey     string `json:"issue_key"`
	AttachmentID string `json:"attachment_id"`
}

type attachmentRequestWireV3 struct {
	SchemaVersion    int                       `json:"schema_version"`
	Operation        string                    `json:"operation"`
	OperationVersion int                       `json:"operation_version"`
	RequestID        string                    `json:"request_id"`
	Features         []string                  `json:"features"`
	Expect           expectationsWire          `json:"expect"`
	Arguments        attachmentArgumentsWireV3 `json:"arguments"`
}

type attachmentAdmissionRequestWireV3 struct {
	SchemaVersion    int                       `json:"schema_version"`
	Context          verifiedContextWire       `json:"context"`
	Operation        string                    `json:"operation"`
	OperationVersion int                       `json:"operation_version"`
	RequestID        string                    `json:"request_id"`
	Features         []string                  `json:"features"`
	Arguments        attachmentArgumentsWireV3 `json:"arguments"`
	ArgumentsSHA256  string                    `json:"arguments_sha256"`
	DeadlineMillis   int64                     `json:"deadline_millis"`
}

type attachmentAdmissionDecisionWireV3 struct {
	SchemaVersion  int              `json:"schema_version"`
	Decision       decisionCoreWire `json:"decision"`
	DecisionSHA256 string           `json:"decision_sha256"`
}

type attachmentMetadataPlanWireV3 struct {
	SelectorSHA256 string          `json:"selector_sha256"`
	MetadataFields []string        `json:"metadata_fields"`
	Limits         phaseLimitsWire `json:"limits"`
}

type attachmentSnapshotWireV3 struct {
	IssueID                  string `json:"issue_id"`
	IssueKey                 string `json:"issue_key"`
	Project                  string `json:"project"`
	Updated                  string `json:"updated"`
	AttachmentID             string `json:"attachment_id"`
	ParentID                 string `json:"parent_id"`
	Filename                 string `json:"filename"`
	MediaType                string `json:"media_type"`
	Created                  string `json:"created"`
	DeclaredSize             int64  `json:"declared_size"`
	IssueEvidenceSHA256      string `json:"issue_evidence_sha256"`
	AttachmentEvidenceSHA256 string `json:"attachment_evidence_sha256"`
	ProjectionSHA256         string `json:"projection_sha256"`
}

type attachmentEffectWireV3 struct {
	Kind         string   `json:"kind"`
	ResourceKind string   `json:"resource_kind"`
	Fields       []string `json:"fields"`
}

type attachmentInitialQualificationWireV3 struct {
	Admission         attachmentAdmissionRequestWireV3  `json:"admission"`
	AdmissionDecision attachmentAdmissionDecisionWireV3 `json:"admission_decision"`
	Plan              attachmentMetadataPlanWireV3      `json:"plan"`
}

type attachmentPreOpenQualificationWireV3 struct {
	InitialOperation         json.RawMessage              `json:"initial_operation"`
	InitialOperationDecision json.RawMessage              `json:"initial_operation_decision"`
	Plan                     attachmentMetadataPlanWireV3 `json:"plan"`
}

type attachmentReleaseCoordinateWireV3 struct {
	Kind   string `json:"kind"`
	Index  int    `json:"index"`
	Offset int64  `json:"offset"`
}

type attachmentReleaseQualificationWireV3 struct {
	Context            verifiedContextWire               `json:"context"`
	AnchorSHA256       string                            `json:"anchor_sha256"`
	PriorReleaseSHA256 string                            `json:"prior_release_sha256"`
	Coordinate         attachmentReleaseCoordinateWireV3 `json:"coordinate"`
	Plan               attachmentMetadataPlanWireV3      `json:"plan"`
}

type attachmentQualificationEnvelopeWireV3 struct {
	SchemaVersion int             `json:"schema_version"`
	Phase         string          `json:"phase"`
	Request       json.RawMessage `json:"request"`
}

type attachmentQualificationDecisionEnvelopeWireV3 struct {
	SchemaVersion  int              `json:"schema_version"`
	Phase          string           `json:"phase"`
	Decision       decisionCoreWire `json:"decision"`
	Binding        json.RawMessage  `json:"binding"`
	DecisionSHA256 string           `json:"decision_sha256"`
}

type attachmentQualificationDecisionBindingWireV3 struct {
	PlanSHA256 string `json:"plan_sha256"`
}

type attachmentQualifiedOperationWireV3 struct {
	QualificationRequest  json.RawMessage          `json:"qualification_request"`
	QualificationDecision json.RawMessage          `json:"qualification_decision"`
	Snapshot              attachmentSnapshotWireV3 `json:"snapshot"`
	Effects               []attachmentEffectWireV3 `json:"effects"`
}

type attachmentTerminalFactsWireV3 struct {
	ChunkCount  int    `json:"chunk_count"`
	TotalBytes  int64  `json:"total_bytes"`
	WholeSHA256 string `json:"whole_sha256"`
	EOFProven   bool   `json:"eof_proven"`
	Complete    bool   `json:"complete"`
}

type attachmentDataReleaseWireV3 struct {
	Index            int                            `json:"index"`
	Offset           int64                          `json:"offset"`
	DecodedBytes     int64                          `json:"decoded_bytes"`
	PayloadSHA256    string                         `json:"payload_sha256"`
	CumulativeBytes  int64                          `json:"cumulative_bytes"`
	CumulativeSHA256 string                         `json:"cumulative_sha256"`
	ResourceSHA256   string                         `json:"resource_sha256"`
	Terminal         *attachmentTerminalFactsWireV3 `json:"terminal,omitempty"`
}

type attachmentReleaseFactsEnvelopeWireV3 struct {
	Kind  string          `json:"kind"`
	Facts json.RawMessage `json:"facts"`
}

type attachmentReleaseOperationWireV3 struct {
	QualificationRequest  json.RawMessage                      `json:"qualification_request"`
	QualificationDecision json.RawMessage                      `json:"qualification_decision"`
	AnchorSHA256          string                               `json:"anchor_sha256"`
	PriorReleaseSHA256    string                               `json:"prior_release_sha256"`
	SnapshotSHA256        string                               `json:"snapshot_sha256"`
	ManifestCoreSHA256    string                               `json:"manifest_core_sha256"`
	Facts                 attachmentReleaseFactsEnvelopeWireV3 `json:"facts"`
}

type attachmentOperationEnvelopeWireV3 struct {
	SchemaVersion int             `json:"schema_version"`
	Phase         string          `json:"phase"`
	Request       json.RawMessage `json:"request"`
}

type attachmentQualifiedDecisionBindingWireV3 struct {
	QualificationDecisionSHA256 string `json:"qualification_decision_sha256"`
	Operation                   string `json:"operation"`
	OperationVersion            int    `json:"operation_version"`
	ArgumentsSHA256             string `json:"arguments_sha256"`
	ResourcesSHA256             string `json:"resources_sha256"`
	EffectsSHA256               string `json:"effects_sha256"`
}

type attachmentReleaseDecisionBindingWireV3 struct {
	QualificationDecisionSHA256 string `json:"qualification_decision_sha256"`
	Operation                   string `json:"operation"`
	OperationVersion            int    `json:"operation_version"`
	AnchorSHA256                string `json:"anchor_sha256"`
	PriorReleaseSHA256          string `json:"prior_release_sha256"`
	ManifestCoreSHA256          string `json:"manifest_core_sha256"`
	ReleaseFactsSHA256          string `json:"release_facts_sha256"`
}

type attachmentOperationDecisionEnvelopeWireV3 struct {
	SchemaVersion  int              `json:"schema_version"`
	Phase          string           `json:"phase"`
	Decision       decisionCoreWire `json:"decision"`
	Binding        json.RawMessage  `json:"binding"`
	DecisionSHA256 string           `json:"decision_sha256"`
}

type attachmentAnchorWireV3 struct {
	SchemaVersion                       int                 `json:"schema_version"`
	ContractFamily                      string              `json:"contract_family"`
	Operation                           string              `json:"operation"`
	OperationVersion                    int                 `json:"operation_version"`
	Features                            []string            `json:"features"`
	Context                             verifiedContextWire `json:"context"`
	RequestSHA256                       string              `json:"request_sha256"`
	ArgumentsSHA256                     string              `json:"arguments_sha256"`
	ResourcesSHA256                     string              `json:"resources_sha256"`
	EffectsSHA256                       string              `json:"effects_sha256"`
	SnapshotSHA256                      string              `json:"snapshot_sha256"`
	StreamID                            string              `json:"stream_id"`
	CorrelationID                       string              `json:"correlation_id"`
	OverallDeadlineMillis               int64               `json:"overall_deadline_millis"`
	AdmissionDecisionSHA256             string              `json:"admission_decision_sha256"`
	InitialQualificationDecisionSHA256  string              `json:"initial_qualification_decision_sha256"`
	InitialOperationDecisionSHA256      string              `json:"initial_operation_decision_sha256"`
	PreOpenQualificationDecisionSHA256  string              `json:"pre_open_qualification_decision_sha256"`
	BodyDispatchOperationDecisionSHA256 string              `json:"body_dispatch_operation_decision_sha256"`
}

type attachmentManifestCoreWireV3 struct {
	SchemaVersion                  int                      `json:"schema_version"`
	FrameVersion                   int                      `json:"frame_version"`
	Kind                           string                   `json:"kind"`
	StreamID                       string                   `json:"stream_id"`
	CorrelationID                  string                   `json:"correlation_id"`
	ArgumentsSHA256                string                   `json:"arguments_sha256"`
	AnchorSHA256                   string                   `json:"anchor_sha256"`
	Snapshot                       attachmentSnapshotWireV3 `json:"snapshot"`
	ConsistencyProfile             string                   `json:"consistency_profile"`
	MaxDataFrames                  int                      `json:"max_data_frames"`
	MaxDecodedFrameBytes           int64                    `json:"max_decoded_frame_bytes"`
	MaxNativeBodyBytes             int64                    `json:"max_native_body_bytes"`
	MaxMetadataItems               int                      `json:"max_metadata_items"`
	MaxManifestLineBytes           int64                    `json:"max_manifest_line_bytes"`
	MaxDataLineBytes               int64                    `json:"max_data_line_bytes"`
	MaxTerminalLineBytes           int64                    `json:"max_terminal_line_bytes"`
	MaxFramedBytes                 int64                    `json:"max_framed_bytes"`
	MaxJiraAttempts                int                      `json:"max_jira_attempts"`
	MaxAuthenticationAttempts      int                      `json:"max_authentication_attempts"`
	MaxDecisionAttempts            int                      `json:"max_decision_attempts"`
	MaxTotalHostOutboundAttempts   int                      `json:"max_total_host_outbound_attempts"`
	MaxCommandHostOutboundAttempts int                      `json:"max_command_host_outbound_attempts"`
	MaxJiraResponseBytes           int64                    `json:"max_jira_response_bytes"`
	MaxAuthorityResponseBytes      int64                    `json:"max_authority_response_bytes"`
	MaxTotalHostResponseBytes      int64                    `json:"max_total_host_response_bytes"`
	MaxOperationMillis             int64                    `json:"max_operation_millis"`
	MaxDecisionLeaseMillis         int64                    `json:"max_decision_lease_millis"`
}

type attachmentManifestLineWireV3 struct {
	SchemaVersion                  int                      `json:"schema_version"`
	FrameVersion                   int                      `json:"frame_version"`
	Kind                           string                   `json:"kind"`
	StreamID                       string                   `json:"stream_id"`
	CorrelationID                  string                   `json:"correlation_id"`
	ArgumentsSHA256                string                   `json:"arguments_sha256"`
	AnchorSHA256                   string                   `json:"anchor_sha256"`
	Snapshot                       attachmentSnapshotWireV3 `json:"snapshot"`
	ConsistencyProfile             string                   `json:"consistency_profile"`
	MaxDataFrames                  int                      `json:"max_data_frames"`
	MaxDecodedFrameBytes           int64                    `json:"max_decoded_frame_bytes"`
	MaxNativeBodyBytes             int64                    `json:"max_native_body_bytes"`
	MaxMetadataItems               int                      `json:"max_metadata_items"`
	MaxManifestLineBytes           int64                    `json:"max_manifest_line_bytes"`
	MaxDataLineBytes               int64                    `json:"max_data_line_bytes"`
	MaxTerminalLineBytes           int64                    `json:"max_terminal_line_bytes"`
	MaxFramedBytes                 int64                    `json:"max_framed_bytes"`
	MaxJiraAttempts                int                      `json:"max_jira_attempts"`
	MaxAuthenticationAttempts      int                      `json:"max_authentication_attempts"`
	MaxDecisionAttempts            int                      `json:"max_decision_attempts"`
	MaxTotalHostOutboundAttempts   int                      `json:"max_total_host_outbound_attempts"`
	MaxCommandHostOutboundAttempts int                      `json:"max_command_host_outbound_attempts"`
	MaxJiraResponseBytes           int64                    `json:"max_jira_response_bytes"`
	MaxAuthorityResponseBytes      int64                    `json:"max_authority_response_bytes"`
	MaxTotalHostResponseBytes      int64                    `json:"max_total_host_response_bytes"`
	MaxOperationMillis             int64                    `json:"max_operation_millis"`
	MaxDecisionLeaseMillis         int64                    `json:"max_decision_lease_millis"`
	ManifestCoreSHA256             string                   `json:"manifest_core_sha256"`
	ReleaseDecisionSHA256          string                   `json:"release_decision_sha256"`
}

type attachmentDataLineWireV3 struct {
	SchemaVersion         int    `json:"schema_version"`
	FrameVersion          int    `json:"frame_version"`
	Kind                  string `json:"kind"`
	StreamID              string `json:"stream_id"`
	Index                 int    `json:"index"`
	Offset                int64  `json:"offset"`
	DecodedBytes          int64  `json:"decoded_bytes"`
	PayloadBase64         string `json:"payload_base64"`
	PayloadSHA256         string `json:"payload_sha256"`
	CumulativeBytes       int64  `json:"cumulative_bytes"`
	CumulativeSHA256      string `json:"cumulative_sha256"`
	ResourceSHA256        string `json:"resource_sha256"`
	PriorReleaseSHA256    string `json:"prior_release_sha256"`
	ReleaseDecisionSHA256 string `json:"release_decision_sha256"`
}

type attachmentTerminalLineWireV3 struct {
	SchemaVersion         int    `json:"schema_version"`
	FrameVersion          int    `json:"frame_version"`
	Kind                  string `json:"kind"`
	StreamID              string `json:"stream_id"`
	ChunkCount            int    `json:"chunk_count"`
	TotalBytes            int64  `json:"total_bytes"`
	DeclaredSize          int64  `json:"declared_size"`
	WholeSHA256           string `json:"whole_sha256"`
	PriorReleaseSHA256    string `json:"prior_release_sha256"`
	ReleaseDecisionSHA256 string `json:"release_decision_sha256"`
	EOFProven             bool   `json:"eof_proven"`
	Complete              bool   `json:"complete"`
}
