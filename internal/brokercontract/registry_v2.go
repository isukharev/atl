package brokercontract

import (
	"sort"

	"github.com/isukharev/atl/internal/domain"
)

const (
	ExecutionSchemaVersionV2           = domain.BrokerExecutionSchemaVersionV2
	ProjectPageOperationVersion        = 1
	MaxProjectPageEffectsV2            = 16
	MaxProjectPageResourcesV2          = 16
	MaxProjectPageRequestBytesV2       = int64(64 << 10)
	MaxProjectPageResultWireBytesV2    = MaxReadResultWireBytes
	MaxProjectIdentityBytesV2          = int64(256 << 10)
	MaxProjectPageIdentityBytesV2      = int64(1 << 20)
	MaxProjectPageBusinessBytesV2      = int64(64 << 20)
	MaxProjectPageQualificationBytesV2 = MaxProjectIdentityBytesV2 + MaxProjectPageIdentityBytesV2
	MaxProjectPageUpstreamBytesV2      = MaxProjectPageQualificationBytesV2 + MaxProjectPageBusinessBytesV2
)

var projectPageQualificationStepsV2 = []domain.BrokerProjectPageQualificationStepDefinitionV2{
	{
		Kind:           domain.BrokerProjectPageQualificationProject,
		MetadataFields: []string{"id", "key"},
		Limits:         domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: MaxProjectIdentityBytesV2},
	},
	{
		Kind: domain.BrokerProjectPageQualificationIssues,
		MetadataFields: []string{
			"issues.id", "issues.key", "issues.project.id", "issues.project.key",
			"issues.updated", "max_results", "start_at", "total",
		},
		Limits: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: MaxProjectPageIdentityBytesV2},
	},
}

// RegistryV2 is the gated execution-v2 registry. Availability remains false
// until a separately reviewed transport, runtime, and family-aware discovery
// integration are composed.
func RegistryV2() []domain.BrokerProjectPageOperationDefinitionV2 {
	definition := domain.BrokerOperationDefinition{
		ID:                   domain.BrokerOperationJiraProjectIssuePageRead,
		Version:              ProjectPageOperationVersion,
		ArgumentSchemaID:     "#/$defs/jira_project_issue_page_arguments",
		ResultSchemaID:       "#/$defs/jira_project_issue_page_result",
		ContractSchemaSHA256: ExecutionSchemaSHA256V2(),
		QualificationProfile: "jira_project_issue_page_v1",
		ExecutionProfile:     "bounded_project_page_v1",
		BackendService:       "jira",
		QualificationFields: []string{
			"id", "issues.id", "issues.key", "issues.project.id", "issues.project.key",
			"issues.updated", "key", "max_results", "start_at", "total",
		},
		RequiredFeatures: []string{"bounded_project_page_v1", domain.BrokerReadConsistencyIdentitySnapshotV1},
		Available:        false,
		Streaming:        false,
		Limits: domain.BrokerLimits{
			MaxRequestBytes:               MaxProjectPageRequestBytesV2,
			MaxResponseBytes:              MaxProjectPageResultWireBytesV2,
			MaxTotalUpstreamRequests:      3,
			MaxTotalUpstreamResponseBytes: MaxProjectPageUpstreamBytesV2,
			Qualification: domain.BrokerPhaseLimits{
				MaxRequests: 2, MaxResponseBytes: MaxProjectPageQualificationBytesV2,
			},
			Business: domain.BrokerPhaseLimits{
				MaxRequests: 1, MaxResponseBytes: MaxProjectPageBusinessBytesV2,
			},
			MaxResources:           MaxProjectPageResourcesV2,
			MaxFields:              2,
			MaxOperationMillis:     domain.BrokerMaxOperationMillis,
			MaxDecisionLeaseMillis: domain.BrokerMaxDecisionLeaseMillis,
		},
		Effects: []domain.BrokerEffectDefinition{
			{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraProject, Fields: []string{"id", "key", "pagination"}},
			{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraIssue, Fields: []string{"description", "id", "key", "project", "summary", "updated"}},
		},
	}
	return []domain.BrokerProjectPageOperationDefinitionV2{{
		Definition:         cloneDefinition(definition),
		QualificationSteps: cloneProjectPageQualificationStepDefinitionsV2(projectPageQualificationStepsV2),
		MaxEffects:         MaxProjectPageEffectsV2,
	}}
}

func DefinitionV2(id domain.BrokerOperationID, version int) (domain.BrokerProjectPageOperationDefinitionV2, bool) {
	for _, definition := range RegistryV2() {
		if definition.Definition.ID == id && definition.Definition.Version == version {
			return cloneProjectPageDefinitionV2(definition), true
		}
	}
	return domain.BrokerProjectPageOperationDefinitionV2{}, false
}

func RegistrySHA256V2() string {
	definitions := RegistryV2()
	projection := make([]any, len(definitions))
	for index, wrapped := range definitions {
		definition := wrapped.Definition
		effects := make([]any, len(definition.Effects))
		for effectIndex, effect := range definition.Effects {
			fields := copyStrings(effect.Fields)
			sort.Strings(fields)
			effects[effectIndex] = struct {
				Kind     string   `json:"kind"`
				Resource string   `json:"resource"`
				Fields   []string `json:"fields"`
			}{string(effect.Kind), string(effect.ResourceKind), fields}
		}
		steps := make([]any, len(wrapped.QualificationSteps))
		for stepIndex, step := range wrapped.QualificationSteps {
			steps[stepIndex] = struct {
				Kind   string          `json:"kind"`
				Fields []string        `json:"metadata_fields"`
				Limits phaseLimitsWire `json:"limits"`
			}{string(step.Kind), wireStrings(step.MetadataFields), phaseLimitsToWire(step.Limits)}
		}
		projection[index] = struct {
			ID                  string     `json:"id"`
			Version             int        `json:"version"`
			Arguments           string     `json:"argument_schema_id"`
			Result              string     `json:"result_schema_id"`
			Schema              string     `json:"contract_schema_sha256"`
			Qualification       string     `json:"qualification_profile"`
			Execution           string     `json:"execution_profile"`
			Backend             string     `json:"backend_service"`
			QualificationFields []string   `json:"qualification_fields"`
			QualificationSteps  []any      `json:"qualification_steps"`
			RequiredFeatures    []string   `json:"required_features"`
			Available           bool       `json:"available"`
			Streaming           bool       `json:"streaming"`
			Limits              limitsWire `json:"limits"`
			MaxEffects          int        `json:"max_effects"`
			Effects             []any      `json:"effects"`
		}{
			string(definition.ID), definition.Version, definition.ArgumentSchemaID,
			definition.ResultSchemaID, definition.ContractSchemaSHA256,
			definition.QualificationProfile, definition.ExecutionProfile,
			definition.BackendService, wireStrings(definition.QualificationFields),
			steps, wireStrings(definition.RequiredFeatures), definition.Available,
			definition.Streaming, limitsToWire(definition.Limits), wrapped.MaxEffects, effects,
		}
	}
	digest, err := digestValueInNamespace("atl.broker.execution.v2/", "registry", projection)
	if err != nil {
		panic("invalid static broker execution-v2 registry")
	}
	return digest
}

func cloneProjectPageDefinitionV2(value domain.BrokerProjectPageOperationDefinitionV2) domain.BrokerProjectPageOperationDefinitionV2 {
	value.Definition = cloneDefinition(value.Definition)
	value.QualificationSteps = cloneProjectPageQualificationStepDefinitionsV2(value.QualificationSteps)
	return value
}

func cloneProjectPageQualificationStepDefinitionsV2(values []domain.BrokerProjectPageQualificationStepDefinitionV2) []domain.BrokerProjectPageQualificationStepDefinitionV2 {
	out := append([]domain.BrokerProjectPageQualificationStepDefinitionV2(nil), values...)
	for index := range out {
		out[index].MetadataFields = append([]string(nil), out[index].MetadataFields...)
	}
	return out
}
