package brokercontract

import "github.com/isukharev/atl/internal/domain"

// BrokerJiraCommentResourceV1 constructs the sole v1 immutable-identity
// snapshot. Project and updated are evidence, not an atomic membership claim.
func BrokerJiraCommentResourceV1(identity domain.BrokerJiraIssueIdentity) (domain.BrokerQualifiedResource, error) {
	version, projection, err := JiraIssueIdentityEvidenceSHA256V1(identity)
	if err != nil {
		return domain.BrokerQualifiedResource{}, err
	}
	return domain.BrokerQualifiedResource{
		Kind: domain.BrokerResourceJiraIssue, ImmutableID: identity.ID,
		Key: identity.Key, Project: identity.Project, AncestorIDs: []string{},
		VersionEvidence: version, ProjectionSHA256: projection,
	}, nil
}

// BrokerJiraCommentEffectsV1 returns the exact ordered effects selected by the
// operation. The digest uses the existing published effect digest contract.
func BrokerJiraCommentEffectsV1(resource domain.BrokerQualifiedResource, apply bool) ([]domain.BrokerEffect, string, error) {
	if resource.Kind != domain.BrokerResourceJiraIssue || resource.ImmutableID == "" || resource.Key == "" || resource.Project == "" ||
		resource.AncestorIDs == nil || resource.VersionEvidence == "" || resource.ProjectionSHA256 == "" {
		return nil, "", reject(domain.BrokerReasonMalformed)
	}
	effects := []domain.BrokerEffect{{Kind: domain.BrokerEffectRead, Resource: resource, Fields: []string{"actor", "comments", "identity", "updated"}}}
	if apply {
		effects = append(effects, domain.BrokerEffect{Kind: domain.BrokerEffectComment, Resource: resource, Fields: []string{"comments"}})
	}
	digest, err := EffectsSHA256(effects)
	if err != nil {
		return nil, "", err
	}
	return effects, digest, nil
}

// BrokerJiraCommentTargetSHA256V1 fences comment-dependent operations by the
// complete backend and immutable numeric issue, consistently across owners.
func BrokerJiraCommentTargetSHA256V1(backend domain.BrokerBackendBinding, issueID string) (string, error) {
	backendSHA256, err := BrokerJournalBackendSHA256V1(backend)
	if err != nil || !validPositiveDecimal(issueID) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return journalDigest("jira-comment/target", struct {
		BackendSHA256 string `json:"backend_sha256"`
		IssueID       string `json:"issue_id"`
		Dependency    string `json:"dependency"`
	}{backendSHA256, issueID, "comment_dependents"})
}

func BrokerJiraCommentResultSHA256V1(value domain.BrokerJiraCommentResult) (string, error) {
	if err := validateJiraCommentResult(value); err != nil {
		return "", err
	}
	return journalDigest("jira-comment/result", jiraCommentResultToWire(value))
}

// BrokerJiraCommentNoDispatchSHA256V1 records trusted app evidence that a
// consumed dispatch claim did not reach the operation-specific write port.
func BrokerJiraCommentNoDispatchSHA256V1(record domain.BrokerJournalRecord) (string, error) {
	if record.Phase != domain.BrokerOperationDispatching || !record.DispatchClaimed || !validDigest(record.OperationID) || !validDigest(record.BindingSHA256) || record.Sequence == 0 {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return journalDigest("jira-comment/no-dispatch", struct {
		OperationID   string `json:"operation_id"`
		BindingSHA256 string `json:"binding_sha256"`
		Sequence      uint64 `json:"sequence"`
	}{record.OperationID, record.BindingSHA256, record.Sequence})
}

// ValidateBrokerJiraCommentDefinitionV1 closes synthetic authority over the
// one canonical operation/version/profile; it never admits a stronger profile.
func ValidateBrokerJiraCommentDefinitionV1(definition domain.BrokerOperationDefinition, operation domain.BrokerOperationID) error {
	if definition.ID != operation || definition.Version != OperationVersion ||
		definition.QualificationProfile != domain.BrokerJiraCommentQualificationProfileV1 ||
		(operation != domain.BrokerOperationJiraCommentPreview && operation != domain.BrokerOperationJiraCommentApply) {
		return reject(domain.BrokerReasonUnsupportedConsistency)
	}
	return nil
}
