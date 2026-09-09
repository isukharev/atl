package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type BrokerProjectPageResult struct {
	Page            *domain.BrokerJiraProjectPageResultV2
	ReleaseDeadline time.Time
}

type BrokerJiraProjectPageReader struct {
	Backend domain.BrokerBackendBinding
	Reader  domain.BrokerJiraProjectPageReadPort
}

type BrokerProjectPageService struct {
	authorizer domain.BrokerProjectPageAuthorizerV2
	jira       BrokerJiraProjectPageReader
	now        func() time.Time
}

func NewBrokerProjectPageService(authorizer domain.BrokerProjectPageAuthorizerV2, jira BrokerJiraProjectPageReader) (*BrokerProjectPageService, error) {
	if authorizer == nil || jira.Reader == nil || jira.Backend.Service != "jira" {
		return nil, fmt.Errorf("%w: broker project-page dependencies are incomplete", domain.ErrUsage)
	}
	return &BrokerProjectPageService{authorizer: authorizer, jira: jira, now: time.Now}, nil
}

// Execute qualifies and reads one page through explicit injected dependencies;
// transport routing and publication remain outside the application service.
func (s *BrokerProjectPageService) Execute(ctx context.Context, request domain.BrokerProjectPageRequestV2, verified domain.BrokerVerifiedContext) (BrokerProjectPageResult, error) {
	if s == nil || s.authorizer == nil || s.jira.Reader == nil || s.now == nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(domain.ErrCheckFailed)
	}
	request, definition, err := prepareBrokerProjectPageRequest(request)
	if err != nil {
		return BrokerProjectPageResult{}, err
	}
	startedAt := s.now()
	nowMillis := startedAt.UnixMilli()
	observedOrigin, originErr := s.jira.Reader.BrokerOriginSHA256()
	if originErr != nil || observedOrigin != s.jira.Backend.OriginSHA256 || verified.Backend != s.jira.Backend {
		return BrokerProjectPageResult{}, brokerProjectPageError(firstBrokerReadError(originErr, domain.ErrCheckFailed))
	}
	deadlineMillis := brokerReadDeadlineMillis(nowMillis, verified, definition.Definition.Limits.MaxOperationMillis)
	if err := brokercontract.MatchProjectPageRequestContextV2(request, verified, nowMillis, deadlineMillis); err != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(err)
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(deadlineMillis-nowMillis)*time.Millisecond)
	defer cancel()
	execution, err := newBrokerReadExecution(bounded, definition.Definition, domain.ReadBudgetFromContext(ctx), s.now, startedAt)
	if err != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(err)
	}
	argumentsSHA256, err := brokercontract.ProjectPageArgumentsSHA256V2(request)
	if err != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(err)
	}
	admission := domain.BrokerProjectPageAdmissionRequestV2{
		Context: verified, Operation: request.Operation, OperationVersion: request.OperationVersion, RequestID: request.RequestID,
		Features: append([]string{}, request.Features...), Arguments: request.Arguments, ArgumentsSHA256: argumentsSHA256, DeadlineMillis: deadlineMillis,
	}
	admissionDecision, err := s.authorizer.AdmitProjectPage(bounded, admission)
	if err == nil {
		err = execution.contextError()
	}
	validationErr := brokercontract.ValidateProjectPageAdmissionDecisionV2(admissionDecision, admission, execution.currentMillis())
	if err != nil || validationErr != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(firstBrokerReadError(err, validationErr))
	}
	plan, err := brokercontract.NewProjectPageQualificationPlanV2(argumentsSHA256)
	if err != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(err)
	}
	qualification := domain.BrokerProjectPageQualificationRequestV2{Admission: admission, AdmissionDecision: admissionDecision, Plan: plan}
	qualificationDecision, err := s.authorizer.AuthorizeProjectPageQualification(bounded, qualification)
	if err == nil {
		err = execution.contextError()
	}
	validationErr = brokercontract.ValidateProjectPageQualificationDecisionV2(qualificationDecision, qualification, execution.currentMillis())
	if err != nil || validationErr != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(firstBrokerReadError(err, validationErr))
	}
	project, page, err := s.qualifyBrokerProjectPage(execution, request, qualification, qualificationDecision)
	if err != nil {
		return BrokerProjectPageResult{}, err
	}
	operation, err := brokerProjectPageAuthorizationRequest(request, qualification, qualificationDecision, project, page)
	if err != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(err)
	}
	decision, err := s.authorizer.AuthorizeProjectPage(bounded, operation)
	if err == nil {
		err = execution.contextError()
	}
	validationErr = brokercontract.ValidateProjectPageOperationDecisionV2(decision, operation, execution.currentMillis())
	if err != nil || validationErr != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(firstBrokerReadError(err, validationErr))
	}
	phaseCtx, phaseCancel, err := execution.businessContext(decision.ExpiresAtMillis)
	if err != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(err)
	}
	snapshot, err := s.jira.Reader.ReadBrokerProjectIssuePage(phaseCtx, project.ID, request.Arguments.Fields, request.Arguments.StartAt, request.Arguments.MaxResults)
	if err == nil {
		err = phaseCtx.Err()
	}
	phaseCancel()
	if err != nil || !sameBrokerProjectPageSnapshot(page, snapshot) {
		return BrokerProjectPageResult{}, brokerProjectPageError(firstBrokerReadError(err, domain.ErrCheckFailed))
	}
	result := brokerProjectPageResult(argumentsSHA256, project, snapshot)
	if err := brokercontract.ValidateJiraProjectPageResultForRequestV2(result, request); err != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(err)
	}
	if err := brokercontract.ValidateProjectPageOperationDecisionV2(decision, operation, execution.currentMillis()); err != nil {
		return BrokerProjectPageResult{}, brokerProjectPageError(err)
	}
	return BrokerProjectPageResult{Page: &result, ReleaseDeadline: execution.releaseDeadline(decision.ExpiresAtMillis)}, nil
}

func prepareBrokerProjectPageRequest(request domain.BrokerProjectPageRequestV2) (domain.BrokerProjectPageRequestV2, domain.BrokerProjectPageOperationDefinitionV2, error) {
	wire, err := brokercontract.EncodeProjectPageRequestV2(request)
	if err != nil {
		return domain.BrokerProjectPageRequestV2{}, domain.BrokerProjectPageOperationDefinitionV2{}, brokerProjectPageError(err)
	}
	request, err = brokercontract.DecodeProjectPageRequestV2(wire)
	if err != nil {
		return domain.BrokerProjectPageRequestV2{}, domain.BrokerProjectPageOperationDefinitionV2{}, brokerProjectPageError(err)
	}
	definition, ok := brokercontract.DefinitionV2(request.Operation, request.OperationVersion)
	if !ok || request.Operation != domain.BrokerOperationJiraProjectIssuePageRead {
		return domain.BrokerProjectPageRequestV2{}, domain.BrokerProjectPageOperationDefinitionV2{}, brokerProjectPageError(domain.ErrUsage)
	}
	return request, definition, nil
}

func (s *BrokerProjectPageService) qualifyBrokerProjectPage(execution *brokerReadExecution, request domain.BrokerProjectPageRequestV2, qualification domain.BrokerProjectPageQualificationRequestV2, decision domain.BrokerProjectPageQualificationDecisionV2) (domain.BrokerJiraProjectIdentityV2, domain.BrokerJiraProjectPageIdentitySnapshotV2, error) {
	if err := brokercontract.ValidateProjectPageQualificationDecisionV2(decision, qualification, execution.currentMillis()); err != nil {
		return domain.BrokerJiraProjectIdentityV2{}, domain.BrokerJiraProjectPageIdentitySnapshotV2{}, brokerProjectPageError(err)
	}
	phaseCtx, cancel, err := execution.qualificationContext(decision.ExpiresAtMillis)
	if err != nil {
		return domain.BrokerJiraProjectIdentityV2{}, domain.BrokerJiraProjectPageIdentitySnapshotV2{}, brokerProjectPageError(err)
	}
	project, err := s.jira.Reader.QualifyBrokerProject(phaseCtx, request.Arguments.ProjectKey)
	if err == nil {
		err = phaseCtx.Err()
	}
	cancel()
	if err != nil {
		return domain.BrokerJiraProjectIdentityV2{}, domain.BrokerJiraProjectPageIdentitySnapshotV2{}, brokerProjectPageError(err)
	}
	if _, err := brokercontract.JiraProjectIdentityEvidenceSHA256V2(project); err != nil || project.Key != request.Arguments.ProjectKey {
		return domain.BrokerJiraProjectIdentityV2{}, domain.BrokerJiraProjectPageIdentitySnapshotV2{}, brokerProjectPageError(firstBrokerReadError(err, domain.ErrCheckFailed))
	}
	if err := brokercontract.ValidateProjectPageQualificationDecisionV2(decision, qualification, execution.currentMillis()); err != nil {
		return domain.BrokerJiraProjectIdentityV2{}, domain.BrokerJiraProjectPageIdentitySnapshotV2{}, brokerProjectPageError(err)
	}
	phaseCtx, cancel, err = execution.qualificationContext(decision.ExpiresAtMillis)
	if err != nil {
		return domain.BrokerJiraProjectIdentityV2{}, domain.BrokerJiraProjectPageIdentitySnapshotV2{}, brokerProjectPageError(err)
	}
	page, err := s.jira.Reader.QualifyBrokerProjectIssuePage(phaseCtx, project.ID, request.Arguments.StartAt, request.Arguments.MaxResults)
	if err == nil {
		err = phaseCtx.Err()
	}
	cancel()
	if err != nil || !validQualifiedBrokerProjectPage(project, page, request.Arguments) {
		return domain.BrokerJiraProjectIdentityV2{}, domain.BrokerJiraProjectPageIdentitySnapshotV2{}, brokerProjectPageError(firstBrokerReadError(err, domain.ErrCheckFailed))
	}
	if err := brokercontract.ValidateProjectPageQualificationDecisionV2(decision, qualification, execution.currentMillis()); err != nil {
		return domain.BrokerJiraProjectIdentityV2{}, domain.BrokerJiraProjectPageIdentitySnapshotV2{}, brokerProjectPageError(err)
	}
	return project, page, nil
}

func validQualifiedBrokerProjectPage(project domain.BrokerJiraProjectIdentityV2, page domain.BrokerJiraProjectPageIdentitySnapshotV2, arguments domain.BrokerProjectPageArguments) bool {
	if !project.Complete || !page.Complete || page.StartAt != arguments.StartAt || page.MaxResults <= 0 || page.MaxResults > arguments.MaxResults ||
		len(page.Issues) > arguments.MaxResults || page.Total < 0 {
		return false
	}
	seenIDs, seenKeys := map[string]bool{}, map[string]bool{}
	for _, issue := range page.Issues {
		if _, _, err := brokercontract.JiraProjectPageIssueIdentityEvidenceSHA256V2(issue); err != nil || issue.ProjectID != project.ID || issue.ProjectKey != project.Key || seenIDs[issue.ID] || seenKeys[issue.Key] {
			return false
		}
		seenIDs[issue.ID], seenKeys[issue.Key] = true, true
	}
	next := page.StartAt + len(page.Issues)
	return next >= page.StartAt && next <= page.Total && page.CoordinateExhausted == (next == page.Total) && page.PaginationStalled == (len(page.Issues) == 0 && next < page.Total)
}

func brokerProjectPageAuthorizationRequest(request domain.BrokerProjectPageRequestV2, qualification domain.BrokerProjectPageQualificationRequestV2, decision domain.BrokerProjectPageQualificationDecisionV2, project domain.BrokerJiraProjectIdentityV2, page domain.BrokerJiraProjectPageIdentitySnapshotV2) (domain.BrokerProjectPageOperationAuthorizationRequestV2, error) {
	projectDigest, err := brokercontract.JiraProjectIdentityEvidenceSHA256V2(project)
	if err != nil {
		return domain.BrokerProjectPageOperationAuthorizationRequestV2{}, err
	}
	qualifiedProject := domain.BrokerQualifiedJiraProjectV2{ID: project.ID, Key: project.Key, IdentityProjectionSHA256: projectDigest}
	qualifiedIssues := make([]domain.BrokerQualifiedJiraProjectPageIssueV2, len(page.Issues))
	ordered := make([]string, len(page.Issues))
	for index, identity := range page.Issues {
		version, projection, evidenceErr := brokercontract.JiraProjectPageIssueIdentityEvidenceSHA256V2(identity)
		if evidenceErr != nil {
			return domain.BrokerProjectPageOperationAuthorizationRequestV2{}, evidenceErr
		}
		qualifiedIssues[index] = domain.BrokerQualifiedJiraProjectPageIssueV2{ID: identity.ID, Key: identity.Key, ProjectID: identity.ProjectID, ProjectKey: identity.ProjectKey, Updated: identity.Updated, VersionEvidenceSHA256: version, ProjectionSHA256: projection}
		ordered[index] = identity.ID
	}
	sort.Slice(qualifiedIssues, func(i, j int) bool { return brokerProjectPageNumericLess(qualifiedIssues[i].ID, qualifiedIssues[j].ID) })
	effects := make([]domain.BrokerProjectPageEffectV2, len(qualifiedIssues)+1)
	effects[0] = domain.BrokerProjectPageEffectV2{Kind: domain.BrokerEffectRead, Project: &qualifiedProject, Fields: []string{"id", "key", "pagination"}}
	issueFields := []string{"id", "key", "project", "updated"}
	for _, field := range request.Arguments.Fields {
		issueFields = append(issueFields, string(field))
	}
	sort.Strings(issueFields)
	for index := range qualifiedIssues {
		issue := qualifiedIssues[index]
		effects[index+1] = domain.BrokerProjectPageEffectV2{Kind: domain.BrokerEffectRead, Issue: &issue, Fields: append([]string{}, issueFields...)}
	}
	return domain.BrokerProjectPageOperationAuthorizationRequestV2{
		QualificationRequest: qualification, QualificationDecision: decision, Project: qualifiedProject, Issues: qualifiedIssues,
		Page: domain.BrokerProjectPageEvidenceV2{
			RequestedStartAt: request.Arguments.StartAt, RequestedMaxResults: request.Arguments.MaxResults,
			ReturnedStartAt: page.StartAt, ReturnedMaxResults: page.MaxResults, Total: page.Total,
			CoordinateExhausted: page.CoordinateExhausted, PaginationStalled: page.PaginationStalled,
			SelectionComplete: false, OrderedIssueIDs: ordered,
		},
		Effects: effects,
	}, nil
}

func brokerProjectPageNumericLess(left, right string) bool {
	a, leftErr := strconv.ParseUint(left, 10, 64)
	b, rightErr := strconv.ParseUint(right, 10, 64)
	return leftErr == nil && rightErr == nil && a < b
}

func sameBrokerProjectPageSnapshot(qualified domain.BrokerJiraProjectPageIdentitySnapshotV2, business domain.BrokerJiraProjectPageSnapshotV2) bool {
	if !qualified.Complete || !business.Complete || qualified.StartAt != business.StartAt || qualified.MaxResults != business.MaxResults ||
		qualified.Total != business.Total || qualified.CoordinateExhausted != business.CoordinateExhausted || qualified.PaginationStalled != business.PaginationStalled ||
		len(qualified.Issues) != len(business.Issues) {
		return false
	}
	for index := range qualified.Issues {
		if qualified.Issues[index] != business.Issues[index].Identity {
			return false
		}
	}
	return true
}

func brokerProjectPageResult(argumentsSHA256 string, project domain.BrokerJiraProjectIdentityV2, snapshot domain.BrokerJiraProjectPageSnapshotV2) domain.BrokerJiraProjectPageResultV2 {
	issues := make([]domain.BrokerJiraProjectPageResultIssueV2, len(snapshot.Issues))
	for index, issue := range snapshot.Issues {
		issues[index] = domain.BrokerJiraProjectPageResultIssueV2{ID: issue.Identity.ID, Key: issue.Identity.Key, ProjectID: issue.Identity.ProjectID, ProjectKey: issue.Identity.ProjectKey, Updated: issue.Identity.Updated, Fields: append([]domain.BrokerJiraIssueReadField{}, issue.Fields...)}
	}
	page := domain.BrokerJiraProjectPageResultPageV2{StartAt: snapshot.StartAt, MaxResults: snapshot.MaxResults, Total: snapshot.Total, Count: len(snapshot.Issues), CoordinateExhausted: snapshot.CoordinateExhausted, SelectionComplete: false}
	if snapshot.PaginationStalled {
		page.PartialReason = brokercontract.BrokerProjectPagePartialPaginationStalledV2
	} else if snapshot.StartAt+len(snapshot.Issues) > domain.BrokerProjectPageMaxStartAt && !snapshot.CoordinateExhausted {
		page.PartialReason = brokercontract.BrokerProjectPagePartialOffsetLimitV2
	} else if !snapshot.CoordinateExhausted {
		page.NextCursor = strconv.Itoa(snapshot.StartAt + len(snapshot.Issues))
		page.NextCursorPresent = true
	}
	return domain.BrokerJiraProjectPageResultV2{SchemaVersion: brokercontract.ExecutionSchemaVersionV2, ArgumentsSHA256: argumentsSHA256, ConsistencyProfile: domain.BrokerReadConsistencyIdentitySnapshotV1, ProjectID: project.ID, ProjectKey: project.Key, Issues: issues, Page: page, Complete: true}
}

func brokerProjectPageError(err error) error {
	return brokerOperationError("broker project-page read failed", err)
}
