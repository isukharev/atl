package brokercontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func fixtureAdmissionRequest() domain.BrokerAdmissionRequest {
	request := fixtureRequest()
	digest, _ := ArgumentsSHA256(request)
	return domain.BrokerAdmissionRequest{
		Context: fixtureContext(), Operation: domain.BrokerOperationJiraIssueRead, OperationVersion: 1,
		RequestID: "request-1", Features: copyStrings(request.Features), Arguments: cloneRequest(request).Arguments, ArgumentsSHA256: digest, DeadlineMillis: 30000,
	}
}

func fixtureAllowedDecision(t *testing.T) domain.BrokerAdmissionDecision {
	t.Helper()
	admission := fixtureAdmissionRequest()
	requestDigest, _ := AdmissionRequestSHA256(admission)
	contextDigest, _ := VerifiedContextSHA256(admission.Context)
	wire, err := EncodeAdmissionDecisionV1(domain.BrokerAdmissionDecision{
		BrokerDecisionCore: domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "decision-1", AuthorityRevision: "revision-1", ContextSHA256: contextDigest, RequestSHA256: requestDigest, IssuedAtMillis: 2000, ExpiresAtMillis: 7000},
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeAdmissionDecisionV1(wire)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func fixtureQualification(t *testing.T) (domain.BrokerQualificationRequest, domain.BrokerQualificationDecision) {
	t.Helper()
	admission := fixtureAdmissionRequest()
	admissionDecision := fixtureAllowedDecision(t)
	definition, _ := Definition(admission.Operation, admission.OperationVersion)
	request := domain.BrokerQualificationRequest{Admission: admission, AdmissionDecision: admissionDecision, Plan: domain.BrokerQualificationPlan{
		SelectorSHA256: admission.ArgumentsSHA256, MetadataFields: []string{"id", "key", "project", "updated"}, Limits: definition.Limits.Qualification,
	}}
	requestDigest, _ := QualificationRequestSHA256(request)
	admissionRequestDigest, _ := AdmissionRequestSHA256(admission)
	planDigest, _ := QualificationPlanSHA256(request.Plan)
	contextDigest, _ := VerifiedContextSHA256(admission.Context)
	wire, err := EncodeQualificationDecisionV1(domain.BrokerQualificationDecision{
		BrokerDecisionCore:     domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "qualification-1", AuthorityRevision: "revision-1", ContextSHA256: contextDigest, RequestSHA256: requestDigest, IssuedAtMillis: 2000, ExpiresAtMillis: 7000},
		AdmissionRequestSHA256: admissionRequestDigest, AdmissionDecisionSHA256: admissionDecision.DecisionSHA256, PlanSHA256: planDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := DecodeQualificationDecisionV1(wire)
	if err != nil {
		t.Fatal(err)
	}
	return request, decision
}

func fixtureOperationAuthorization(t *testing.T) (domain.BrokerOperationAuthorizationRequest, domain.BrokerOperationDecision) {
	t.Helper()
	qualification, qualificationDecision := fixtureQualification(t)
	resource := domain.BrokerQualifiedResource{Kind: domain.BrokerResourceJiraIssue, ImmutableID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", AncestorIDs: []string{}, VersionEvidence: digestChar('d'), ProjectionSHA256: digestChar('e')}
	request := domain.BrokerOperationAuthorizationRequest{
		QualificationRequest: qualification, QualificationDecision: qualificationDecision, QualifiedResources: []domain.BrokerQualifiedResource{resource},
		Effects: []domain.BrokerEffect{{Kind: domain.BrokerEffectRead, Resource: resource, Fields: []string{"description", "summary"}}},
	}
	requestDigest, _ := OperationAuthorizationRequestSHA256(request)
	resourcesDigest, _ := QualifiedResourcesSHA256(request.QualifiedResources)
	effectsDigest, _ := EffectsSHA256(request.Effects)
	contextDigest, _ := VerifiedContextSHA256(request.QualificationRequest.Admission.Context)
	wire, err := EncodeOperationDecisionV1(domain.BrokerOperationDecision{
		BrokerDecisionCore:          domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "operation-1", AuthorityRevision: "revision-1", ContextSHA256: contextDigest, RequestSHA256: requestDigest, IssuedAtMillis: 2000, ExpiresAtMillis: 7000},
		QualificationDecisionSHA256: qualificationDecision.DecisionSHA256, Operation: request.QualificationRequest.Admission.Operation, OperationVersion: 1, ArgumentsSHA256: request.QualificationRequest.Admission.ArgumentsSHA256, ResourcesSHA256: resourcesDigest, EffectsSHA256: effectsDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := DecodeOperationDecisionV1(wire)
	if err != nil {
		t.Fatal(err)
	}
	return request, decision
}

func fixtureProposalAuthorization(t *testing.T) (domain.BrokerProposalAuthorizationRequest, domain.BrokerProposalClearance) {
	t.Helper()
	operationRequest, operationDecision := fixtureApplyOperationAuthorization(t)
	nativeDigest, _ := NativeCandidateSHA256(domain.BrokerOperationJiraCommentApply, operationRequest.QualificationRequest.Admission.Arguments.JiraComment.NativeBody)
	request := domain.BrokerProposalAuthorizationRequest{OperationRequest: operationRequest, OperationDecision: operationDecision, ProposalSchemaVersion: 1, ProposalHash: digestChar('3'), NativeCandidateSHA256: nativeDigest, VersionEvidenceSHA256: digestChar('5')}
	requestDigest, _ := ProposalAuthorizationRequestSHA256(request)
	contextDigest, _ := VerifiedContextSHA256(operationRequest.QualificationRequest.Admission.Context)
	wire, err := EncodeProposalClearanceV1(domain.BrokerProposalClearance{
		BrokerDecisionCore:      domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "clearance-1", AuthorityRevision: "revision-1", ContextSHA256: contextDigest, RequestSHA256: requestDigest, IssuedAtMillis: 2000, ExpiresAtMillis: 7000},
		OperationDecisionSHA256: operationDecision.DecisionSHA256, ProposalHash: request.ProposalHash, NativeCandidateSHA256: request.NativeCandidateSHA256, VersionEvidenceSHA256: request.VersionEvidenceSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	clearance, err := DecodeProposalClearanceV1(wire)
	if err != nil {
		t.Fatal(err)
	}
	return request, clearance
}

func fixtureApplyOperationAuthorization(t *testing.T) (domain.BrokerOperationAuthorizationRequest, domain.BrokerOperationDecision) {
	return fixtureApplyOperationAuthorizationWithBody(t, []byte("body"))
}

func fixtureApplyOperationAuthorizationWithBody(t *testing.T, body []byte) (domain.BrokerOperationAuthorizationRequest, domain.BrokerOperationDecision) {
	t.Helper()
	request := requestFor(domain.BrokerOperationJiraCommentApply, domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "EXAMPLE-1", NativeBody: body, SatisfactionPolicy: "append_always", ExpectedProposalHash: digestChar('3'), OperationTicket: "ticket-1"}})
	argumentsDigest, _ := ArgumentsSHA256(request)
	admission := domain.BrokerAdmissionRequest{Context: fixtureContext(), Operation: request.Operation, OperationVersion: 1, RequestID: request.RequestID, Features: copyStrings(request.Features), Arguments: cloneRequest(request).Arguments, ArgumentsSHA256: argumentsDigest, DeadlineMillis: 30000}
	admissionRequestDigest, _ := AdmissionRequestSHA256(admission)
	contextDigest, _ := VerifiedContextSHA256(admission.Context)
	admissionWire, err := EncodeAdmissionDecisionV1(domain.BrokerAdmissionDecision{BrokerDecisionCore: domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "decision-apply", AuthorityRevision: "revision-1", ContextSHA256: contextDigest, RequestSHA256: admissionRequestDigest, IssuedAtMillis: 2000, ExpiresAtMillis: 7000}})
	if err != nil {
		t.Fatal(err)
	}
	admissionDecision, _ := DecodeAdmissionDecisionV1(admissionWire)
	definition, _ := Definition(admission.Operation, 1)
	qualification := domain.BrokerQualificationRequest{Admission: admission, AdmissionDecision: admissionDecision, Plan: domain.BrokerQualificationPlan{SelectorSHA256: argumentsDigest, MetadataFields: []string{"id", "key", "project", "updated"}, Limits: definition.Limits.Qualification}}
	qualificationRequestDigest, _ := QualificationRequestSHA256(qualification)
	planDigest, _ := QualificationPlanSHA256(qualification.Plan)
	qualificationWire, err := EncodeQualificationDecisionV1(domain.BrokerQualificationDecision{BrokerDecisionCore: domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "qualification-apply", AuthorityRevision: "revision-1", ContextSHA256: contextDigest, RequestSHA256: qualificationRequestDigest, IssuedAtMillis: 2000, ExpiresAtMillis: 7000}, AdmissionRequestSHA256: admissionRequestDigest, AdmissionDecisionSHA256: admissionDecision.DecisionSHA256, PlanSHA256: planDigest})
	if err != nil {
		t.Fatal(err)
	}
	qualificationDecision, _ := DecodeQualificationDecisionV1(qualificationWire)
	resource := domain.BrokerQualifiedResource{Kind: domain.BrokerResourceJiraIssue, ImmutableID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", AncestorIDs: []string{}, VersionEvidence: digestChar('5'), ProjectionSHA256: digestChar('6')}
	operation := domain.BrokerOperationAuthorizationRequest{QualificationRequest: qualification, QualificationDecision: qualificationDecision, QualifiedResources: []domain.BrokerQualifiedResource{resource}, Effects: []domain.BrokerEffect{
		{Kind: domain.BrokerEffectRead, Resource: resource, Fields: []string{"actor", "comments", "identity", "updated"}},
		{Kind: domain.BrokerEffectComment, Resource: resource, Fields: []string{"comments"}},
	}}
	operationRequestDigest, _ := OperationAuthorizationRequestSHA256(operation)
	resourcesDigest, _ := QualifiedResourcesSHA256(operation.QualifiedResources)
	effectsDigest, _ := EffectsSHA256(operation.Effects)
	operationWire, err := EncodeOperationDecisionV1(domain.BrokerOperationDecision{BrokerDecisionCore: domain.BrokerDecisionCore{Status: domain.BrokerDecisionAllowed, DecisionID: "operation-apply", AuthorityRevision: "revision-1", ContextSHA256: contextDigest, RequestSHA256: operationRequestDigest, IssuedAtMillis: 2000, ExpiresAtMillis: 7000}, QualificationDecisionSHA256: qualificationDecision.DecisionSHA256, Operation: admission.Operation, OperationVersion: 1, ArgumentsSHA256: argumentsDigest, ResourcesSHA256: resourcesDigest, EffectsSHA256: effectsDigest})
	if err != nil {
		t.Fatal(err)
	}
	operationDecision, _ := DecodeOperationDecisionV1(operationWire)
	return operation, operationDecision
}

func TestAuthorizationPortWireRoundTrips(t *testing.T) {
	admission := fixtureAdmissionRequest()
	encoded, err := EncodeAdmissionRequestV1(admission)
	decodedAdmission, decodeErr := DecodeAdmissionRequestV1(encoded)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decodedAdmission, admission) {
		t.Fatalf("admission=%+v errors=%v/%v", decodedAdmission, err, decodeErr)
	}

	plan, qualificationDecision := fixtureQualification(t)
	encoded, err = EncodeQualificationRequestV1(plan)
	decodedPlan, decodeErr := DecodeQualificationRequestV1(encoded)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decodedPlan, plan) {
		t.Fatalf("qualification=%+v errors=%v/%v", decodedPlan, err, decodeErr)
	}

	encoded, err = EncodeQualificationDecisionV1(qualificationDecision)
	decodedQualificationDecision, decodeErr := DecodeQualificationDecisionV1(encoded)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decodedQualificationDecision, qualificationDecision) {
		t.Fatalf("qualification decision=%+v errors=%v/%v", decodedQualificationDecision, err, decodeErr)
	}

	operationRequest, operationDecision := fixtureOperationAuthorization(t)
	encoded, err = EncodeOperationAuthorizationRequestV1(operationRequest)
	decodedOperationRequest, decodeErr := DecodeOperationAuthorizationRequestV1(encoded)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decodedOperationRequest, operationRequest) {
		t.Fatalf("operation request=%+v errors=%v/%v wire=%s", decodedOperationRequest, err, decodeErr, encoded)
	}

	encoded, err = EncodeOperationDecisionV1(operationDecision)
	decodedOperationDecision, decodeErr := DecodeOperationDecisionV1(encoded)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decodedOperationDecision, operationDecision) {
		t.Fatalf("operation decision=%+v errors=%v/%v", decodedOperationDecision, err, decodeErr)
	}

	proposalRequest, _ := fixtureProposalAuthorization(t)
	encoded, err = EncodeProposalAuthorizationRequestV1(proposalRequest)
	decodedProposalRequest, decodeErr := DecodeProposalAuthorizationRequestV1(encoded)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decodedProposalRequest, proposalRequest) {
		t.Fatalf("proposal request=%+v errors=%v/%v", decodedProposalRequest, err, decodeErr)
	}
}

func TestAuthorizationWireRejectsUnknownScopeAndWidenedLimits(t *testing.T) {
	admission, _ := EncodeAdmissionRequestV1(fixtureAdmissionRequest())
	if _, err := DecodeAdmissionRequestV1(bytes.Replace(admission, []byte(`"request_id":"request-1"`), []byte(`"request_id":"request-1","role":"admin"`), 1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("forged role error=%v", err)
	}
	if _, err := DecodeAdmissionRequestV1(bytes.Replace(admission, []byte(`"context":{"schema_version":1`), []byte(`"context":{"schema_version":2`), 1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("nested future version error=%v", err)
	}
	if _, err := DecodeAdmissionRequestV1(bytes.Replace(admission, []byte(`"deadline_millis":30000`), []byte(`"deadline_millis":null`), 1)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("required null error=%v", err)
	}
	plan, _ := fixtureQualification(t)
	plan.Plan.Limits.MaxRequests++
	if _, err := EncodeQualificationRequestV1(plan); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("widened plan error=%v", err)
	}
	request, _ := fixtureOperationAuthorization(t)
	request.QualifiedResources[0].Key = "OTHER-1"
	request.Effects[0].Resource = request.QualifiedResources[0]
	if _, err := EncodeOperationAuthorizationRequestV1(request); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("cross-project resource error=%v", err)
	}
	request, _ = fixtureOperationAuthorization(t)
	request.Effects[0].Fields = []string{"comments"}
	if _, err := EncodeOperationAuthorizationRequestV1(request); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("widened effect fields error=%v", err)
	}
}

func FuzzDecodeAdmissionRequestV1(f *testing.F) {
	seed, _ := EncodeAdmissionRequestV1(fixtureAdmissionRequest())
	f.Add(seed)
	f.Add([]byte(`{"schema_version":1,"context":{}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := DecodeAdmissionRequestV1(data)
		if err == nil {
			if encoded, encodeErr := EncodeAdmissionRequestV1(value); encodeErr != nil || len(encoded) == 0 {
				t.Fatalf("accepted admission did not re-encode: %v", encodeErr)
			}
		}
	})
}

func TestQualifiedResourceWirePreservesExplicitEmptyAncestors(t *testing.T) {
	resources := []domain.BrokerQualifiedResource{
		{Kind: domain.BrokerResourceJiraIssue, ImmutableID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", AncestorIDs: []string{}, VersionEvidence: digestChar('a'), ProjectionSHA256: digestChar('b')},
		{Kind: domain.BrokerResourceConfluencePage, ImmutableID: "42", Space: "DOCS", AncestorIDs: []string{}, AncestorsPresent: true, VersionEvidence: digestChar('a'), ProjectionSHA256: digestChar('b')},
		{Kind: domain.BrokerResourceConfluencePage, ImmutableID: "42", Space: "DOCS", AncestorIDs: []string{"7"}, AncestorsPresent: true, VersionEvidence: digestChar('a'), ProjectionSHA256: digestChar('b')},
	}
	for _, resource := range resources {
		encoded, err := json.Marshal(qualifiedResourceToWire(resource))
		var wire qualifiedResourceWire
		decodeErr := strictDecode(encoded, 64<<10, &wire)
		decoded := qualifiedResourceFromWire(wire)
		if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, resource) || !validQualifiedResource(decoded) {
			t.Fatalf("resource=%+v decoded=%+v errors=%v/%v wire=%s", resource, decoded, err, decodeErr, encoded)
		}
	}
}

func TestMaximumCommentBodyAuthorizationChainRoundTrips(t *testing.T) {
	body := bytes.Repeat([]byte{'x'}, int(MaxJiraCommentBodyBytes))
	operation, decision := fixtureApplyOperationAuthorizationWithBody(t, body)
	wire, err := EncodeOperationAuthorizationRequestV1(operation)
	decoded, decodeErr := DecodeOperationAuthorizationRequestV1(wire)
	if err != nil || decodeErr != nil || int64(len(wire)) > MaxEnvelopeBytes || !reflect.DeepEqual(decoded, operation) {
		t.Fatalf("operation bytes=%d errors=%v/%v", len(wire), err, decodeErr)
	}
	nativeDigest, _ := NativeCandidateSHA256(domain.BrokerOperationJiraCommentApply, body)
	proposal := domain.BrokerProposalAuthorizationRequest{OperationRequest: operation, OperationDecision: decision, ProposalSchemaVersion: 1, ProposalHash: digestChar('3'), NativeCandidateSHA256: nativeDigest, VersionEvidenceSHA256: digestChar('5')}
	wire, err = EncodeProposalAuthorizationRequestV1(proposal)
	decodedProposal, decodeErr := DecodeProposalAuthorizationRequestV1(wire)
	if err != nil || decodeErr != nil || int64(len(wire)) > MaxEnvelopeBytes || !reflect.DeepEqual(decodedProposal, proposal) {
		t.Fatalf("proposal bytes=%d errors=%v/%v", len(wire), err, decodeErr)
	}
	oversized := requestFor(domain.BrokerOperationJiraCommentApply, domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "EXAMPLE-1", NativeBody: append(body, 'x'), SatisfactionPolicy: "append_always", ExpectedProposalHash: digestChar('3'), OperationTicket: "ticket-1"}})
	if _, err := EncodeRequestV1(oversized); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("oversized body error=%v", err)
	}
}
