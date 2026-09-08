// Package brokercontract defines ATL's strict, transport-neutral Broker v1
// wire contract. It performs validation and canonicalization without I/O.
package brokercontract

import (
	"sort"

	"github.com/isukharev/atl/internal/domain"
)

const (
	SchemaVersion           = 1
	OperationVersion        = 1
	MaxEnvelopeBytes        = int64(2 << 20)
	MaxRegistryOperations   = 64
	MaxDiscoveryOperations  = 64
	MaxEffects              = 16
	MaxResources            = 16
	MaxMetadataFields       = 32
	MaxCanonicalDepth       = 64
	MaxJiraCommentBodyBytes = int64(1 << 20)
	MaxJiraCommentResponses = int64(16 << 20)
	// Read results may contain a 64 MiB native value. The JSON transport bound
	// includes canonical base64 expansion and bounded result metadata.
	MaxReadResultValueBytes = int64(64 << 20)
	MaxReadResultWireBytes  = int64(129 << 20)
	MaxReadTitleBytes       = int64(64 << 10)
	MaxReadEnvelopeOverhead = int64(1 << 20)
)

func Registry() []domain.BrokerOperationDefinition {
	schemaDigest := SchemaSHA256()
	definitions := []domain.BrokerOperationDefinition{
		{
			ID: domain.BrokerOperationJiraIssueRead, Version: 1,
			ArgumentSchemaID: "#/$defs/jira_issue_read_arguments", ResultSchemaID: "#/$defs/jira_issue_read_result",
			ContractSchemaSHA256: schemaDigest, QualificationProfile: "exact_jira_issue_v1", ExecutionProfile: "exact_reads_v1", BackendService: "jira", QualificationFields: []string{"id", "key", "project", "updated"}, Available: true,
			Limits:  domain.BrokerLimits{MaxRequestBytes: 64 << 10, MaxResponseBytes: MaxReadResultWireBytes, MaxTotalUpstreamRequests: 2, MaxTotalUpstreamResponseBytes: (64 << 20) + (64 << 10), Qualification: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 64 << 10}, Business: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 64 << 20}, MaxResources: 1, MaxFields: 3, MaxOperationMillis: 60_000, MaxDecisionLeaseMillis: 5_000},
			Effects: []domain.BrokerEffectDefinition{{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraIssue, Fields: []string{"description", "summary", "updated"}}},
		},
		{
			ID: domain.BrokerOperationConfluencePageRead, Version: 1,
			ArgumentSchemaID: "#/$defs/confluence_page_read_arguments", ResultSchemaID: "#/$defs/confluence_page_read_result",
			ContractSchemaSHA256: schemaDigest, QualificationProfile: "exact_confluence_page_v1", ExecutionProfile: "exact_reads_v1", BackendService: "confluence", QualificationFields: []string{"ancestors", "id", "space", "updated", "version"}, Available: true,
			Limits:  domain.BrokerLimits{MaxRequestBytes: 64 << 10, MaxResponseBytes: MaxReadResultWireBytes, MaxTotalUpstreamRequests: 2, MaxTotalUpstreamResponseBytes: (64 << 20) + (64 << 10), Qualification: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 64 << 10}, Business: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 64 << 20}, MaxResources: 1, MaxFields: 1, MaxOperationMillis: 60_000, MaxDecisionLeaseMillis: 5_000},
			Effects: []domain.BrokerEffectDefinition{{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceConfluencePage, Fields: []string{"metadata", "storage"}}},
		},
		{
			ID: domain.BrokerOperationJiraCommentPreview, Version: 1,
			ArgumentSchemaID: "#/$defs/jira_comment_preview_arguments", ResultSchemaID: "#/$defs/jira_comment_preview_result",
			ContractSchemaSHA256: schemaDigest, QualificationProfile: "exact_jira_comment_v1", ExecutionProfile: "guarded_comment_v1", BackendService: "jira", QualificationFields: []string{"id", "key", "project", "updated"}, Available: false,
			Limits:           domain.BrokerLimits{MaxRequestBytes: 2 << 20, MaxResponseBytes: 1 << 20, MaxTotalUpstreamRequests: 102, MaxTotalUpstreamResponseBytes: MaxJiraCommentResponses, Qualification: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 64 << 10}, Business: domain.BrokerPhaseLimits{MaxRequests: 101, MaxResponseBytes: MaxJiraCommentResponses}, MaxResources: 1, MaxFields: 1, MaxNativeBodyBytes: MaxJiraCommentBodyBytes, MaxOperationMillis: 60_000, MaxDecisionLeaseMillis: 5_000},
			Effects:          []domain.BrokerEffectDefinition{{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraIssue, Fields: []string{"actor", "comments", "identity", "updated"}}},
			RequiredFeatures: []string{"guarded_proposal_v1"},
		},
		{
			ID: domain.BrokerOperationJiraCommentApply, Version: 1,
			ArgumentSchemaID: "#/$defs/jira_comment_apply_arguments", ResultSchemaID: "#/$defs/jira_comment_apply_result",
			ContractSchemaSHA256: schemaDigest, QualificationProfile: "exact_jira_comment_v1", ExecutionProfile: "guarded_comment_journal_v1", BackendService: "jira", QualificationFields: []string{"id", "key", "project", "updated"}, Available: false,
			Limits: domain.BrokerLimits{MaxRequestBytes: 2 << 20, MaxResponseBytes: 1 << 20, MaxTotalUpstreamRequests: 306, MaxTotalUpstreamResponseBytes: MaxJiraCommentResponses, Qualification: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 64 << 10}, Business: domain.BrokerPhaseLimits{MaxRequests: 305, MaxResponseBytes: MaxJiraCommentResponses}, MaxResources: 1, MaxFields: 1, MaxNativeBodyBytes: MaxJiraCommentBodyBytes, MaxOperationMillis: 60_000, MaxDecisionLeaseMillis: 5_000},
			Effects: []domain.BrokerEffectDefinition{
				{Kind: domain.BrokerEffectRead, ResourceKind: domain.BrokerResourceJiraIssue, Fields: []string{"actor", "comments", "identity", "updated"}},
				{Kind: domain.BrokerEffectComment, ResourceKind: domain.BrokerResourceJiraIssue, Fields: []string{"comments"}},
			},
			RequiredFeatures: []string{"durable_outcome_v1", "guarded_proposal_v1", "proposal_clearance_v1"},
		},
		{
			ID: domain.BrokerOperationOutcomeLookup, Version: 1,
			ArgumentSchemaID: "#/$defs/outcome_arguments", ResultSchemaID: "#/$defs/operation_outcome",
			ContractSchemaSHA256: schemaDigest, QualificationProfile: "operation_ticket_v1", ExecutionProfile: "durable_observation_v1", BackendService: "jira", QualificationFields: []string{"operation_id"}, Available: false,
			Limits:           domain.BrokerLimits{MaxRequestBytes: 64 << 10, MaxResponseBytes: 1 << 20, MaxTotalUpstreamRequests: 1, MaxTotalUpstreamResponseBytes: 1 << 20, Qualification: domain.BrokerPhaseLimits{}, Business: domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 1 << 20}, MaxResources: 1, MaxOperationMillis: 60_000, MaxDecisionLeaseMillis: 5_000},
			Effects:          []domain.BrokerEffectDefinition{{Kind: domain.BrokerEffectObserve, ResourceKind: domain.BrokerResourceOperation}},
			RequiredFeatures: []string{"durable_outcome_v1"},
		},
	}
	for index := range definitions {
		definitions[index] = cloneDefinition(definitions[index])
	}
	sort.Slice(definitions, func(i, j int) bool {
		if definitions[i].ID == definitions[j].ID {
			return definitions[i].Version < definitions[j].Version
		}
		return definitions[i].ID < definitions[j].ID
	})
	return definitions
}

func Definition(id domain.BrokerOperationID, version int) (domain.BrokerOperationDefinition, bool) {
	for _, definition := range Registry() {
		if definition.ID == id && definition.Version == version {
			return cloneDefinition(definition), true
		}
	}
	return domain.BrokerOperationDefinition{}, false
}

func AvailableDefinitions() []domain.BrokerOperationDefinition {
	all := Registry()
	out := make([]domain.BrokerOperationDefinition, 0, len(all))
	for _, definition := range all {
		if definition.Available {
			out = append(out, cloneDefinition(definition))
		}
	}
	return out
}

func cloneDefinition(value domain.BrokerOperationDefinition) domain.BrokerOperationDefinition {
	value.QualificationFields = append([]string(nil), value.QualificationFields...)
	value.RequiredFeatures = append([]string(nil), value.RequiredFeatures...)
	value.Effects = append([]domain.BrokerEffectDefinition(nil), value.Effects...)
	for index := range value.Effects {
		value.Effects[index].Fields = append([]string(nil), value.Effects[index].Fields...)
	}
	return value
}
