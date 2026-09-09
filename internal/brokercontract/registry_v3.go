package brokercontract

import (
	"sort"

	"github.com/isukharev/atl/internal/domain"
)

type attachmentRegistryEffectV3 struct {
	Kind     string   `json:"kind"`
	Resource string   `json:"resource"`
	Fields   []string `json:"fields"`
}

type attachmentRegistryProjectionV3 struct {
	ID                             string                       `json:"id"`
	Version                        int                          `json:"version"`
	ArgumentSchemaID               string                       `json:"argument_schema_id"`
	ResultSchemaID                 string                       `json:"result_schema_id"`
	ContractSchemaSHA256           string                       `json:"contract_schema_sha256"`
	QualificationProfile           string                       `json:"qualification_profile"`
	ExecutionProfile               string                       `json:"execution_profile"`
	BackendService                 string                       `json:"backend_service"`
	QualificationFields            []string                     `json:"qualification_fields"`
	RequiredFeatures               []string                     `json:"required_features"`
	Available                      bool                         `json:"available"`
	Streaming                      bool                         `json:"streaming"`
	Limits                         limitsWire                   `json:"limits"`
	Effects                        []attachmentRegistryEffectV3 `json:"effects"`
	MaxMetadataItems               int                          `json:"max_metadata_items"`
	MaxJiraAttempts                int                          `json:"max_jira_attempts"`
	MaxAuthenticationAttempts      int                          `json:"max_authentication_attempts"`
	MaxDecisionAttempts            int                          `json:"max_decision_attempts"`
	MaxTotalHostOutboundAttempts   int                          `json:"max_total_host_outbound_attempts"`
	MaxCommandHostOutboundAttempts int                          `json:"max_command_host_outbound_attempts"`
	MaxJiraResponseBytes           int64                        `json:"max_jira_response_bytes"`
	MaxAuthorityResponseBytes      int64                        `json:"max_authority_response_bytes"`
	MaxTotalHostResponseBytes      int64                        `json:"max_total_host_response_bytes"`
	MaxManifestLineBytes           int64                        `json:"max_manifest_line_bytes"`
	MaxDataLineBytes               int64                        `json:"max_data_line_bytes"`
	MaxTerminalLineBytes           int64                        `json:"max_terminal_line_bytes"`
	MaxFramedResponseBytes         int64                        `json:"max_framed_response_bytes"`
}

const (
	ExecutionSchemaVersionV3     = domain.BrokerExecutionSchemaVersionV3
	AttachmentOperationVersionV3 = 1

	MaxAttachmentRequestBytesV3                = int64(64 << 10)
	MaxAttachmentNativeBodyBytesV3             = int64(16 << 20)
	MaxAttachmentDataFramesV3                  = 16
	MaxAttachmentDecodedFrameBytesV3           = int64(1 << 20)
	MaxAttachmentManifestLineBytesV3           = int64(64 << 10)
	MaxAttachmentDataLineBytesV3               = int64(1_441_792)
	MaxAttachmentTerminalLineBytesV3           = int64(16 << 10)
	MaxAttachmentFramedResponseBytesV3         = int64(24 << 20)
	MaxAttachmentJiraAttemptsV3                = 19
	MaxAttachmentAuthenticationAttemptsV3      = 17
	MaxAttachmentDecisionAttemptsV3            = 37
	MaxAttachmentHostOutboundAttemptsV3        = 73
	MaxAttachmentCommandHostOutboundAttemptsV3 = 76
	MaxAttachmentJiraResponseBytesV3           = int64(34 << 20)
	MaxAttachmentAuthorityResponseBytesV3      = int64(27 << 18)  // 6.75 MiB.
	MaxAttachmentHostResponseBytesV3           = int64(163 << 18) // 40.75 MiB.
	MaxAttachmentMetadataResponseBytesV3       = int64(1 << 20)
	MaxAttachmentAuthorityCallBytesV3          = int64(128 << 10)
	MaxAttachmentInventoryItemsV3              = 10_000
	MaxAttachmentContentURIBytesV3             = int64(64 << 10)
)

var attachmentFeaturesV3 = []string{"atomic_local_publish_v1", "attachment_id_v1", "step_snapshot_v1"}

// RegistryV3 is deliberately unavailable until the complete selected-binary
// stream oracle passes. Compiled schema support is not runtime availability.
func RegistryV3() []domain.BrokerAttachmentOperationDefinitionV3 {
	definition := domain.BrokerOperationDefinition{
		ID:                   domain.BrokerOperationJiraAttachmentDownload,
		Version:              AttachmentOperationVersionV3,
		ArgumentSchemaID:     "#/$defs/jira_attachment_arguments",
		ResultSchemaID:       "#/$defs/stream_line",
		ContractSchemaSHA256: ExecutionSchemaSHA256V3(),
		QualificationProfile: "jira_issue_attachment_step_snapshot_v1",
		ExecutionProfile:     "bounded_attachment_stream_v1",
		BackendService:       "jira",
		QualificationFields: []string{
			"attachment.created", "attachment.filename", "attachment.id", "attachment.media_type", "attachment.parent_id", "attachment.size",
			"issue.id", "issue.key", "issue.project", "issue.updated",
		},
		RequiredFeatures: append([]string(nil), attachmentFeaturesV3...),
		Available:        false,
		Streaming:        true,
		Limits: domain.BrokerLimits{
			MaxRequestBytes:               MaxAttachmentRequestBytesV3,
			MaxResponseBytes:              MaxAttachmentFramedResponseBytesV3,
			MaxTotalUpstreamRequests:      MaxAttachmentHostOutboundAttemptsV3,
			MaxTotalUpstreamResponseBytes: MaxAttachmentHostResponseBytesV3,
			Qualification:                 domain.BrokerPhaseLimits{MaxRequests: 18, MaxResponseBytes: 18 << 20},
			Business:                      domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: MaxAttachmentNativeBodyBytesV3},
			MaxResources:                  2,
			MaxFields:                     0,
			MaxNativeBodyBytes:            MaxAttachmentNativeBodyBytesV3,
			MaxStreamChunks:               MaxAttachmentDataFramesV3,
			MaxStreamChunkBytes:           MaxAttachmentDecodedFrameBytesV3,
			MaxOperationMillis:            domain.BrokerMaxOperationMillis,
			MaxDecisionLeaseMillis:        domain.BrokerMaxDecisionLeaseMillis,
		},
		Effects: []domain.BrokerEffectDefinition{
			{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraIssue, Fields: []string{"id", "key", "project", "updated"}},
			{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraAttachment, Fields: []string{"body", "created", "filename", "id", "media_type", "parent_id", "size"}},
		},
	}
	return []domain.BrokerAttachmentOperationDefinitionV3{{
		Definition:       definition,
		MaxMetadataItems: MaxAttachmentInventoryItemsV3,
		MaxJiraAttempts:  MaxAttachmentJiraAttemptsV3, MaxAuthenticationAttempts: MaxAttachmentAuthenticationAttemptsV3,
		MaxDecisionAttempts: MaxAttachmentDecisionAttemptsV3, MaxTotalHostOutboundAttempts: MaxAttachmentHostOutboundAttemptsV3,
		MaxCommandHostOutboundAttempts: MaxAttachmentCommandHostOutboundAttemptsV3,
		MaxJiraResponseBytes:           MaxAttachmentJiraResponseBytesV3, MaxAuthorityResponseBytes: MaxAttachmentAuthorityResponseBytesV3,
		MaxTotalHostResponseBytes: MaxAttachmentHostResponseBytesV3, MaxManifestLineBytes: MaxAttachmentManifestLineBytesV3,
		MaxDataLineBytes: MaxAttachmentDataLineBytesV3, MaxTerminalLineBytes: MaxAttachmentTerminalLineBytesV3,
		MaxFramedResponseBytes: MaxAttachmentFramedResponseBytesV3,
	}}
}

func DefinitionV3(id domain.BrokerOperationID, version int) (domain.BrokerAttachmentOperationDefinitionV3, bool) {
	definition := RegistryV3()[0]
	if definition.Definition.ID != id || definition.Definition.Version != version {
		return domain.BrokerAttachmentOperationDefinitionV3{}, false
	}
	return cloneAttachmentDefinitionV3(definition), true
}

func AvailableDefinitionsV3() []domain.BrokerAttachmentOperationDefinitionV3 {
	definitions := RegistryV3()
	result := make([]domain.BrokerAttachmentOperationDefinitionV3, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Definition.Available {
			result = append(result, cloneAttachmentDefinitionV3(definition))
		}
	}
	return result
}

func cloneAttachmentDefinitionV3(value domain.BrokerAttachmentOperationDefinitionV3) domain.BrokerAttachmentOperationDefinitionV3 {
	value.Definition = cloneDefinition(value.Definition)
	return value
}

func RegistrySHA256V3() string {
	definitions := RegistryV3()
	projection := make([]any, len(definitions))
	for index, wrapped := range definitions {
		definition := wrapped.Definition
		effects := make([]attachmentRegistryEffectV3, len(definition.Effects))
		for effectIndex, effect := range definition.Effects {
			fields := copyStrings(effect.Fields)
			sort.Strings(fields)
			effects[effectIndex] = attachmentRegistryEffectV3{string(effect.Kind), string(effect.ResourceKind), fields}
		}
		projection[index] = attachmentRegistryProjectionV3{
			ID: string(definition.ID), Version: definition.Version, ArgumentSchemaID: definition.ArgumentSchemaID,
			ResultSchemaID: definition.ResultSchemaID, ContractSchemaSHA256: definition.ContractSchemaSHA256,
			QualificationProfile: definition.QualificationProfile, ExecutionProfile: definition.ExecutionProfile, BackendService: definition.BackendService,
			QualificationFields: wireStrings(definition.QualificationFields), RequiredFeatures: wireStrings(definition.RequiredFeatures), Available: definition.Available, Streaming: definition.Streaming,
			Limits: limitsToWire(definition.Limits), Effects: effects, MaxMetadataItems: wrapped.MaxMetadataItems, MaxJiraAttempts: wrapped.MaxJiraAttempts, MaxAuthenticationAttempts: wrapped.MaxAuthenticationAttempts,
			MaxDecisionAttempts: wrapped.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: wrapped.MaxTotalHostOutboundAttempts,
			MaxCommandHostOutboundAttempts: wrapped.MaxCommandHostOutboundAttempts,
			MaxJiraResponseBytes:           wrapped.MaxJiraResponseBytes, MaxAuthorityResponseBytes: wrapped.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: wrapped.MaxTotalHostResponseBytes,
			MaxManifestLineBytes: wrapped.MaxManifestLineBytes, MaxDataLineBytes: wrapped.MaxDataLineBytes, MaxTerminalLineBytes: wrapped.MaxTerminalLineBytes, MaxFramedResponseBytes: wrapped.MaxFramedResponseBytes,
		}
	}
	digest, err := digestExecutionV3("registry", projection)
	if err != nil {
		panic("invalid static broker execution-v3 registry")
	}
	return digest
}
