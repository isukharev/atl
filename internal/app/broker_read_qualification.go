package app

import (
	"reflect"
	"sort"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func (s *BrokerReadService) executeJiraBrokerRead(execution *brokerReadExecution, request domain.BrokerRequest, qualification domain.BrokerQualificationRequest, qualificationDecision domain.BrokerQualificationDecision) (BrokerExactReadResult, error) {
	phaseCtx, cancel, err := execution.qualificationContext(qualificationDecision.ExpiresAtMillis)
	if err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	identity, err := s.jira.Reader.QualifyBrokerIssue(phaseCtx, request.Arguments.JiraIssueRead.IssueKey)
	if err == nil {
		err = phaseCtx.Err()
	}
	cancel()
	if err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	if err := brokercontract.ValidateQualificationDecisionForV1(qualificationDecision, qualification, execution.currentMillis()); err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	versionSHA256, projectionSHA256, err := brokercontract.JiraIssueIdentityEvidenceSHA256V1(identity)
	if err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	resource := domain.BrokerQualifiedResource{Kind: domain.BrokerResourceJiraIssue, ImmutableID: identity.ID, Key: identity.Key, Project: identity.Project, AncestorIDs: []string{}, VersionEvidence: versionSHA256, ProjectionSHA256: projectionSHA256}
	fields := make([]string, len(request.Arguments.JiraIssueRead.Fields))
	for index, field := range request.Arguments.JiraIssueRead.Fields {
		fields[index] = string(field)
	}
	sort.Strings(fields)
	operation := domain.BrokerOperationAuthorizationRequest{QualificationRequest: qualification, QualificationDecision: qualificationDecision, QualifiedResources: []domain.BrokerQualifiedResource{resource}, Effects: []domain.BrokerEffect{{Kind: domain.BrokerEffectRead, Resource: resource, Fields: fields}}}
	decision, err := s.authorizer.AuthorizeOperation(execution.base, operation)
	if err == nil {
		err = execution.contextError()
	}
	validationErr := brokercontract.ValidateOperationDecisionForV1(decision, operation, execution.currentMillis())
	if err != nil || validationErr != nil {
		return BrokerExactReadResult{}, brokerReadError(firstBrokerReadError(err, validationErr))
	}
	phaseCtx, cancel, err = execution.businessContext(decision.ExpiresAtMillis)
	if err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	snapshot, err := s.jira.Reader.ReadBrokerIssue(phaseCtx, identity.ID, request.Arguments.JiraIssueRead.Fields)
	if err == nil {
		err = phaseCtx.Err()
	}
	cancel()
	if err != nil || !snapshot.Complete || snapshot.Identity != identity {
		return BrokerExactReadResult{}, brokerReadError(firstBrokerReadError(err, domain.ErrCheckFailed))
	}
	result := domain.BrokerJiraIssueReadResult{SchemaVersion: 1, ArgumentsSHA256: qualification.Admission.ArgumentsSHA256, IssueID: identity.ID, Key: identity.Key, Project: identity.Project, Updated: identity.Updated, Fields: append([]domain.BrokerJiraIssueReadField(nil), snapshot.Fields...), Complete: true}
	if err := brokercontract.ValidateJiraIssueReadResultForV1(result, request); err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	if err := brokercontract.ValidateOperationDecisionForV1(decision, operation, execution.currentMillis()); err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	return BrokerExactReadResult{JiraIssue: &result, ReleaseDeadline: execution.releaseDeadline(decision.ExpiresAtMillis)}, nil
}

func (s *BrokerReadService) executeConfluenceBrokerRead(execution *brokerReadExecution, request domain.BrokerRequest, qualification domain.BrokerQualificationRequest, qualificationDecision domain.BrokerQualificationDecision) (BrokerExactReadResult, error) {
	phaseCtx, cancel, err := execution.qualificationContext(qualificationDecision.ExpiresAtMillis)
	if err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	identity, err := s.confluence.Reader.QualifyBrokerPage(phaseCtx, request.Arguments.ConfluencePageRead.PageID)
	if err == nil {
		err = phaseCtx.Err()
	}
	cancel()
	if err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	if err := brokercontract.ValidateQualificationDecisionForV1(qualificationDecision, qualification, execution.currentMillis()); err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	versionSHA256, projectionSHA256, err := brokercontract.ConfluencePageIdentityEvidenceSHA256V1(identity)
	if err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	resource := domain.BrokerQualifiedResource{Kind: domain.BrokerResourceConfluencePage, ImmutableID: identity.ID, Space: identity.Space, AncestorIDs: append([]string{}, identity.AncestorIDs...), AncestorsPresent: identity.AncestorsPresent, VersionEvidence: versionSHA256, ProjectionSHA256: projectionSHA256}
	projection := request.Arguments.ConfluencePageRead.Projection
	operation := domain.BrokerOperationAuthorizationRequest{QualificationRequest: qualification, QualificationDecision: qualificationDecision, QualifiedResources: []domain.BrokerQualifiedResource{resource}, Effects: []domain.BrokerEffect{{Kind: domain.BrokerEffectRead, Resource: resource, Fields: []string{string(projection)}}}}
	decision, err := s.authorizer.AuthorizeOperation(execution.base, operation)
	if err == nil {
		err = execution.contextError()
	}
	validationErr := brokercontract.ValidateOperationDecisionForV1(decision, operation, execution.currentMillis())
	if err != nil || validationErr != nil {
		return BrokerExactReadResult{}, brokerReadError(firstBrokerReadError(err, validationErr))
	}
	phaseCtx, cancel, err = execution.businessContext(decision.ExpiresAtMillis)
	if err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	snapshot, err := s.confluence.Reader.ReadBrokerPage(phaseCtx, identity.ID, projection)
	if err == nil {
		err = phaseCtx.Err()
	}
	cancel()
	if err != nil || !snapshot.Complete || !sameBrokerConfluenceIdentity(snapshot.Identity, identity) {
		return BrokerExactReadResult{}, brokerReadError(firstBrokerReadError(err, domain.ErrCheckFailed))
	}
	var storage []byte
	if snapshot.StoragePresent {
		storage = make([]byte, len(snapshot.Storage))
		copy(storage, snapshot.Storage)
	}
	result := domain.BrokerConfluencePageReadResult{SchemaVersion: 1, ArgumentsSHA256: qualification.Admission.ArgumentsSHA256, PageID: identity.ID, Type: identity.Type, Space: identity.Space, Version: identity.Version, Title: snapshot.Title, Updated: identity.Updated, Projection: projection, Storage: storage, StoragePresent: snapshot.StoragePresent, Complete: true}
	if err := brokercontract.ValidateConfluencePageReadResultForV1(result, request); err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	if err := brokercontract.ValidateOperationDecisionForV1(decision, operation, execution.currentMillis()); err != nil {
		return BrokerExactReadResult{}, brokerReadError(err)
	}
	return BrokerExactReadResult{ConfluencePage: &result, ReleaseDeadline: execution.releaseDeadline(decision.ExpiresAtMillis)}, nil
}

func sameBrokerConfluenceIdentity(left, right domain.BrokerConfluencePageIdentity) bool {
	leftAncestors, rightAncestors := left.AncestorIDs, right.AncestorIDs
	left.AncestorIDs, right.AncestorIDs = nil, nil
	return reflect.DeepEqual(left, right) && reflect.DeepEqual(leftAncestors, rightAncestors)
}
