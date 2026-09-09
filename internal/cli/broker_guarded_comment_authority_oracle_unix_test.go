//go:build !windows

package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestGuardedProcessAuthorityAcceptsOnlyExactApprovedShapes(t *testing.T) {
	fixture := guardedProcessAuthorityOracle(t)
	for _, operation := range []domain.BrokerOperationID{
		domain.BrokerOperationJiraCommentPreview,
		domain.BrokerOperationJiraCommentApply,
		domain.BrokerOperationOutcomeLookup,
	} {
		role, ticket := "writer", fixture.approvedTicket
		if operation == domain.BrokerOperationJiraCommentPreview {
			ticket = ""
		}
		if operation == domain.BrokerOperationOutcomeLookup {
			role, ticket = "observer", "unknown-ticket"
		}
		request := guardedProcessAuthorityOperation(t, fixture, operation, role, ticket)
		if err := fixture.validateAuthorityOperation(request); err != nil {
			t.Fatalf("exact %s operation rejected: %v", operation, err)
		}
	}
	proposal := guardedProcessAuthorityProposal(t, fixture)
	if err := fixture.validateAuthorityProposal(proposal); err != nil {
		t.Fatalf("exact externally approved proposal rejected: %v", err)
	}
}

func TestGuardedProcessAuthorityAdmissionIsOperationProfileAndRoleClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*guardedProcessFixture, *domain.BrokerAdmissionRequest)
	}{
		{"wrong operation", func(_ *guardedProcessFixture, request *domain.BrokerAdmissionRequest) {
			request.Operation = domain.BrokerOperationJiraIssueRead
			request.Features = nil
			request.Arguments = domain.BrokerOperationArguments{JiraIssueRead: &domain.BrokerJiraIssueReadArguments{
				IssueKey: guardedProcessIssueKey, Fields: []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary},
			}}
		}},
		{"wrong version", func(_ *guardedProcessFixture, request *domain.BrokerAdmissionRequest) { request.OperationVersion = 2 }},
		{"wrong profile", func(fixture *guardedProcessFixture, _ *domain.BrokerAdmissionRequest) {
			fixture.authorityPolicy.commentProfile = "atomic_current_project_v1"
		}},
		{"wrong feature", func(_ *guardedProcessFixture, request *domain.BrokerAdmissionRequest) {
			request.Features = []string{"other_feature"}
		}},
		{"empty body", func(_ *guardedProcessFixture, request *domain.BrokerAdmissionRequest) {
			request.Arguments.JiraComment.NativeBody = []byte{}
		}},
		{"changed principal", func(_ *guardedProcessFixture, request *domain.BrokerAdmissionRequest) {
			request.Context.PrincipalID = "another-principal"
		}},
		{"changed workload", func(_ *guardedProcessFixture, request *domain.BrokerAdmissionRequest) {
			request.Context.WorkloadID = "another-workload"
		}},
		{"wrong ticket", func(_ *guardedProcessFixture, request *domain.BrokerAdmissionRequest) {
			request.Arguments.JiraComment.OperationTicket = "other-ticket"
		}},
		{"observer write", func(_ *guardedProcessFixture, request *domain.BrokerAdmissionRequest) {
			request.Context = guardedProcessAuthorityContext("observer", request.Context.Backend)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := guardedProcessAuthorityOracle(t)
			request := guardedProcessAuthorityAdmission(t, fixture, domain.BrokerOperationJiraCommentApply, "writer", fixture.approvedTicket)
			test.mutate(fixture, &request)
			guardedProcessRefreshAdmissionDigest(t, &request)
			if err := fixture.validateAuthorityAdmission(request); err == nil {
				t.Fatal("mutated admission passed the independent authority oracle")
			}
		})
	}
}

func TestGuardedProcessAuthorityQualificationAndEffectsAreExact(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*domain.BrokerOperationAuthorizationRequest)
	}{
		{"wrong resource id", func(request *domain.BrokerOperationAuthorizationRequest) {
			request.QualifiedResources[0].ImmutableID = "102"
		}},
		{"wrong project", func(request *domain.BrokerOperationAuthorizationRequest) {
			request.QualifiedResources[0].Project = "OTHER"
		}},
		{"wrong qualification field", func(request *domain.BrokerOperationAuthorizationRequest) {
			request.QualificationRequest.Plan.MetadataFields[0] = "summary"
		}},
		{"wrong phase cap", func(request *domain.BrokerOperationAuthorizationRequest) {
			request.QualificationRequest.Plan.Limits.MaxRequests++
		}},
		{"wrong effect", func(request *domain.BrokerOperationAuthorizationRequest) {
			request.Effects[1].Kind = domain.BrokerEffectRead
		}},
		{"wrong effect field", func(request *domain.BrokerOperationAuthorizationRequest) {
			request.Effects[0].Fields[0] = "description"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := guardedProcessAuthorityOracle(t)
			request := guardedProcessAuthorityOperation(t, fixture, domain.BrokerOperationJiraCommentApply, "writer", fixture.approvedTicket)
			test.mutate(&request)
			if err := fixture.validateAuthorityOperation(request); err == nil {
				t.Fatal("mutated operation authorization passed the independent authority oracle")
			}
		})
	}
}

func TestGuardedProcessAuthorityProposalRequiresExternalPreviewApproval(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*domain.BrokerProposalAuthorizationRequest)
	}{
		{"wrong proposal schema", func(request *domain.BrokerProposalAuthorizationRequest) { request.ProposalSchemaVersion = 2 }},
		{"wrong proposal hash", func(request *domain.BrokerProposalAuthorizationRequest) {
			request.ProposalHash = strings.Repeat("c", 64)
		}},
		{"wrong native digest", func(request *domain.BrokerProposalAuthorizationRequest) {
			request.NativeCandidateSHA256 = strings.Repeat("d", 64)
		}},
		{"different body with claimed approved digest", func(request *domain.BrokerProposalAuthorizationRequest) {
			admission := &request.OperationRequest.QualificationRequest.Admission
			admission.Arguments.JiraComment.NativeBody = []byte("different synthetic body")
			guardedProcessRefreshAdmissionDigest(t, admission)
			request.OperationRequest.QualificationRequest.Plan.SelectorSHA256 = admission.ArgumentsSHA256
			request.OperationDecision.ArgumentsSHA256 = admission.ArgumentsSHA256
		}},
		{"wrong evidence", func(request *domain.BrokerProposalAuthorizationRequest) {
			request.VersionEvidenceSHA256 = strings.Repeat("e", 64)
		}},
		{"wrong ticket", func(request *domain.BrokerProposalAuthorizationRequest) {
			admission := &request.OperationRequest.QualificationRequest.Admission
			admission.Arguments.JiraComment.OperationTicket = "other-ticket"
			guardedProcessRefreshAdmissionDigest(t, admission)
			request.OperationRequest.QualificationRequest.Plan.SelectorSHA256 = admission.ArgumentsSHA256
			request.OperationDecision.ArgumentsSHA256 = admission.ArgumentsSHA256
		}},
		{"observer proposal", func(request *domain.BrokerProposalAuthorizationRequest) {
			request.OperationRequest.QualificationRequest.Admission.Context = guardedProcessAuthorityContext("observer", request.OperationRequest.QualificationRequest.Admission.Context.Backend)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := guardedProcessAuthorityOracle(t)
			request := guardedProcessAuthorityProposal(t, fixture)
			test.mutate(&request)
			if err := fixture.validateAuthorityProposal(request); err == nil {
				t.Fatal("mutated proposal passed the independent authority oracle")
			}
		})
	}
}

func TestGuardedProcessAuthorityObserverAndForeignReachOnlyOutcome(t *testing.T) {
	for _, role := range []string{"writer", "replacement"} {
		t.Run(role+" comment", func(t *testing.T) {
			fixture := guardedProcessAuthorityOracle(t)
			request := guardedProcessAuthorityOperation(t, fixture, domain.BrokerOperationJiraCommentPreview, role, "")
			if err := fixture.validateAuthorityOperation(request); err != nil {
				t.Fatalf("%s comment rejected by writer-role policy: %v", role, err)
			}
		})
	}
	for _, role := range []string{"observer", "foreign"} {
		t.Run(role, func(t *testing.T) {
			fixture := guardedProcessAuthorityOracle(t)
			request := guardedProcessAuthorityOperation(t, fixture, domain.BrokerOperationOutcomeLookup, role, "unknown-ticket")
			if err := fixture.validateAuthorityOperation(request); err != nil {
				t.Fatalf("%s outcome rejected before journal lookup: %v", role, err)
			}
		})
	}
	fixture := guardedProcessAuthorityOracle(t)
	writerOutcome := guardedProcessAuthorityOperation(t, fixture, domain.BrokerOperationOutcomeLookup, "writer", "unknown-ticket")
	if err := fixture.validateAuthorityOperation(writerOutcome); err == nil {
		t.Fatal("writer session was accepted for outcome")
	}
	observerWrite := guardedProcessAuthorityOperation(t, fixture, domain.BrokerOperationJiraCommentPreview, "observer", "")
	if err := fixture.validateAuthorityOperation(observerWrite); err == nil {
		t.Fatal("observer session was accepted for comment preview")
	}
}

func guardedProcessAuthorityOracle(t *testing.T) *guardedProcessFixture {
	t.Helper()
	fixture := &guardedProcessFixture{
		backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("a", 64), WorkloadBackendID: "jira-primary"},
		authorityPolicy: guardedProcessAuthorityPolicy{
			commentProfile: domain.BrokerJiraCommentQualificationProfileV1,
			outcomeProfile: "operation_ticket_v1",
		},
		operationShapes: map[string]domain.BrokerQualifiedResource{},
	}
	previewDigest, err := brokercontract.NativeCandidateSHA256(domain.BrokerOperationJiraCommentPreview, []byte(guardedProcessBody))
	if err != nil {
		t.Fatal(err)
	}
	fixture.approveGuardedProcessProposal(t, guardedProcessCommentResult{
		SchemaVersion: 1, Operation: string(domain.BrokerOperationJiraCommentPreview),
		QualificationProfile: domain.BrokerJiraCommentQualificationProfileV1,
		ArgumentsSHA256:      strings.Repeat("a", 64), OperationTicket: "ticket-approved", Mode: "preview", Status: "proposed",
		ProposalHash: strings.Repeat("b", 64), NativeCandidateSHA256: previewDigest,
		VersionEvidenceSHA256: strings.Repeat("c", 64), Complete: true,
	})
	return fixture
}

func guardedProcessAuthorityAdmission(t *testing.T, fixture *guardedProcessFixture, operation domain.BrokerOperationID, role, ticket string) domain.BrokerAdmissionRequest {
	t.Helper()
	contextValue := guardedProcessAuthorityContext(role, fixture.backend)
	request := domain.BrokerAdmissionRequest{
		Context: contextValue, Operation: operation, OperationVersion: 1, RequestID: "request-1",
		DeadlineMillis: contextValue.ExecutionNotBeforeMillis + 60_000,
	}
	switch operation {
	case domain.BrokerOperationJiraCommentPreview:
		request.Features = []string{"guarded_proposal_v1"}
		request.Arguments = domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
			IssueKey: guardedProcessIssueKey, NativeBody: []byte(guardedProcessBody), SatisfactionPolicy: "append_always",
		}}
	case domain.BrokerOperationJiraCommentApply:
		request.Features = []string{"durable_outcome_v1", "guarded_proposal_v1", "proposal_clearance_v1"}
		request.Arguments = domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
			IssueKey: guardedProcessIssueKey, NativeBody: []byte(guardedProcessBody), SatisfactionPolicy: "append_always",
			ExpectedProposalHash: fixture.approvedHash, OperationTicket: ticket,
		}}
	case domain.BrokerOperationOutcomeLookup:
		request.Features = []string{"durable_outcome_v1"}
		request.Arguments = domain.BrokerOperationArguments{Outcome: &domain.BrokerOutcomeArguments{OperationTicket: ticket}}
	}
	guardedProcessRefreshAdmissionDigest(t, &request)
	return request
}

func guardedProcessRefreshAdmissionDigest(t *testing.T, request *domain.BrokerAdmissionRequest) {
	t.Helper()
	digest, err := brokercontract.ArgumentsSHA256(domain.BrokerRequest{
		SchemaVersion: 1, Operation: request.Operation, OperationVersion: request.OperationVersion,
		RequestID: request.RequestID, Features: append([]string(nil), request.Features...),
		Expect: domain.BrokerRequestExpectations{
			ExecutionID: request.Context.ExecutionID, ExecutionEpoch: request.Context.ExecutionEpoch, AuthorityRevision: request.Context.AuthorityRevision,
		},
		Arguments: request.Arguments,
	})
	if err != nil {
		request.ArgumentsSHA256 = ""
		return
	}
	request.ArgumentsSHA256 = digest
}

func guardedProcessAuthorityContext(role string, backend domain.BrokerBackendBinding) domain.BrokerVerifiedContext {
	now := time.Now().UTC().Truncate(time.Millisecond)
	kind := role
	if role == "writer" {
		kind = "writer"
	}
	_, execution, epoch, principal, workload := guardedAuthenticationIdentity(map[string]string{
		"writer": guardedProcessWriterCredential, "replacement": guardedProcessReplacementCredential,
		"observer": guardedProcessObserverCredential, "foreign": guardedProcessForeignCredential,
	}[kind])
	return domain.BrokerVerifiedContext{
		PrincipalID: principal, WorkloadID: workload, ExecutionID: execution, ExecutionEpoch: epoch,
		Audience: "broker-data", BrokerID: "broker-1", AuthorityRevision: "revision-1",
		ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(2 * time.Minute).UnixMilli(),
		GrantExpiresMillis: now.Add(2 * time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(2 * time.Minute).UnixMilli(), Backend: backend,
	}
}

func guardedProcessAuthorityQualification(t *testing.T, fixture *guardedProcessFixture, operation domain.BrokerOperationID, role, ticket string) domain.BrokerQualificationRequest {
	t.Helper()
	admission := guardedProcessAuthorityAdmission(t, fixture, operation, role, ticket)
	fields := []string{"id", "key", "project", "updated"}
	limits := domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: 64 << 10}
	if operation == domain.BrokerOperationOutcomeLookup {
		fields, limits = []string{"operation_id"}, domain.BrokerPhaseLimits{}
	}
	return domain.BrokerQualificationRequest{
		Admission: admission,
		Plan:      domain.BrokerQualificationPlan{SelectorSHA256: admission.ArgumentsSHA256, MetadataFields: fields, Limits: limits},
	}
}

func guardedProcessAuthorityOperation(t *testing.T, fixture *guardedProcessFixture, operation domain.BrokerOperationID, role, ticket string) domain.BrokerOperationAuthorizationRequest {
	t.Helper()
	qualification := guardedProcessAuthorityQualification(t, fixture, operation, role, ticket)
	var resource domain.BrokerQualifiedResource
	if operation == domain.BrokerOperationOutcomeLookup {
		var err error
		resource, err = brokercontract.BrokerOperationObservationResourceV1(ticket)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		version, projection, err := brokercontract.JiraIssueIdentityEvidenceSHA256V1(domain.BrokerJiraIssueIdentity{
			ID: guardedProcessIssueID, Key: guardedProcessIssueKey, Project: guardedProcessProject,
			Updated: "2026-09-09T10:00:00Z", Complete: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		resource = domain.BrokerQualifiedResource{
			Kind: domain.BrokerResourceJiraIssue, ImmutableID: guardedProcessIssueID, Key: guardedProcessIssueKey,
			Project: guardedProcessProject, AncestorIDs: []string{}, VersionEvidence: version, ProjectionSHA256: projection,
		}
	}
	effects := []domain.BrokerEffect{{Kind: domain.BrokerEffectRead, Resource: resource, Fields: []string{"actor", "comments", "identity", "updated"}}}
	switch operation {
	case domain.BrokerOperationJiraCommentApply:
		effects = append(effects, domain.BrokerEffect{Kind: domain.BrokerEffectComment, Resource: resource, Fields: []string{"comments"}})
	case domain.BrokerOperationOutcomeLookup:
		effects = []domain.BrokerEffect{{Kind: domain.BrokerEffectObserve, Resource: resource, Fields: []string{}}}
	}
	return domain.BrokerOperationAuthorizationRequest{QualificationRequest: qualification, QualifiedResources: []domain.BrokerQualifiedResource{resource}, Effects: effects}
}

func guardedProcessAuthorityProposal(t *testing.T, fixture *guardedProcessFixture) domain.BrokerProposalAuthorizationRequest {
	t.Helper()
	operation := guardedProcessAuthorityOperation(t, fixture, domain.BrokerOperationJiraCommentApply, "writer", fixture.approvedTicket)
	nativeDigest, err := brokercontract.NativeCandidateSHA256(domain.BrokerOperationJiraCommentApply, []byte(guardedProcessBody))
	if err != nil {
		t.Fatal(err)
	}
	resourcesDigest, err := brokercontract.QualifiedResourcesSHA256(operation.QualifiedResources)
	if err != nil {
		t.Fatal(err)
	}
	effectsDigest, err := brokercontract.EffectsSHA256(operation.Effects)
	if err != nil {
		t.Fatal(err)
	}
	admission := operation.QualificationRequest.Admission
	return domain.BrokerProposalAuthorizationRequest{
		OperationRequest: operation,
		OperationDecision: domain.BrokerOperationDecision{
			Operation: admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256,
			ResourcesSHA256: resourcesDigest, EffectsSHA256: effectsDigest,
		},
		ProposalSchemaVersion: 1, ProposalHash: fixture.approvedHash,
		NativeCandidateSHA256: nativeDigest, VersionEvidenceSHA256: operation.QualifiedResources[0].VersionEvidence,
	}
}
