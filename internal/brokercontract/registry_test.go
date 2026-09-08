package brokercontract

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/isukharev/atl/internal/domain"
)

func TestRegistryIsClosedStableAndAvailabilityIsExplicit(t *testing.T) {
	definitions := Registry()
	if len(definitions) != 5 || len(AvailableDefinitions()) != 2 || !validDigest(RegistrySHA256()) || !validDigest(SchemaSHA256()) {
		t.Fatalf("definitions=%d available=%d registry=%q schema=%q", len(definitions), len(AvailableDefinitions()), RegistrySHA256(), SchemaSHA256())
	}
	want := []domain.BrokerOperationID{
		domain.BrokerOperationOutcomeLookup,
		domain.BrokerOperationConfluencePageRead,
		domain.BrokerOperationJiraCommentApply,
		domain.BrokerOperationJiraCommentPreview,
		domain.BrokerOperationJiraIssueRead,
	}
	got := make([]domain.BrokerOperationID, len(definitions))
	for index, definition := range definitions {
		got[index] = definition.ID
		if definition.Version != 1 || !validSchemaID(definition.ArgumentSchemaID) || !validSchemaID(definition.ResultSchemaID) ||
			definition.ContractSchemaSHA256 != SchemaSHA256() || definition.Streaming || definition.Limits.MaxStreamChunks != 0 || definition.Limits.MaxStreamChunkBytes != 0 ||
			definition.Limits.MaxOperationMillis > domain.BrokerMaxOperationMillis || definition.Limits.MaxDecisionLeaseMillis > domain.BrokerMaxDecisionLeaseMillis || len(definition.Effects) == 0 ||
			definition.BackendService == "" || !sort.StringsAreSorted(definition.QualificationFields) || !sort.StringsAreSorted(definition.RequiredFeatures) ||
			definition.Limits.MaxTotalUpstreamRequests != definition.Limits.Qualification.MaxRequests+definition.Limits.Business.MaxRequests ||
			definition.Limits.Qualification.MaxResponseBytes > definition.Limits.MaxTotalUpstreamResponseBytes || definition.Limits.Business.MaxResponseBytes > definition.Limits.MaxTotalUpstreamResponseBytes {
			t.Fatalf("invalid definition=%+v", definition)
		}
		if definition.Available != (definition.ID == domain.BrokerOperationJiraIssueRead || definition.ID == domain.BrokerOperationConfluencePageRead) {
			t.Fatalf("availability drift=%+v", definition)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want=%v", got, want)
	}
	definitions[0].Effects[0].Fields = append(definitions[0].Effects[0].Fields, "mutated")
	again := Registry()
	if reflect.DeepEqual(definitions[0].Effects, again[0].Effects) {
		t.Fatal("registry exposed mutable package storage")
	}
	if _, ok := Definition("raw.http", 1); ok {
		t.Fatal("unknown operation resolved")
	}
}

func TestEmbeddedSchemaIsClosedAndSyntacticallyValid(t *testing.T) {
	var schema map[string]any
	if json.Unmarshal(SchemaV1(), &schema) != nil || schema["$schema"] == nil || schema["$defs"] == nil || schema["oneOf"] == nil {
		t.Fatalf("schema is malformed: %s", SchemaV1())
	}
	definitions := schema["$defs"].(map[string]any)
	for _, name := range []string{"request", "verified_context", "admission_request", "admission_decision", "qualification_request", "qualification_decision", "operation_authorization_request", "operation_decision", "proposal_authorization_request", "proposal_clearance", "jira_issue_read_result", "confluence_page_read_result", "jira_comment_preview_result", "jira_comment_apply_result", "discovery", "execution_projection", "operation_ticket", "operation_outcome", "cache_qualification_request", "cache_qualification"} {
		if definitions[name] == nil {
			t.Errorf("missing schema definition %q", name)
		}
	}
}

func TestPublishedSchemaValidatesContractVectorsAndRegistryReferences(t *testing.T) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(SchemaV1(), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	admission := fixtureAdmissionRequest()
	admissionDecision := fixtureAllowedDecision(t)
	qualification, qualificationDecision := fixtureQualification(t)
	operation, operationDecision := fixtureOperationAuthorization(t)
	proposal, clearance := fixtureProposalAuthorization(t)
	request := fixtureRequest()
	requestDigest, _ := ArgumentsSHA256(request)
	readResult := domain.BrokerJiraIssueReadResult{SchemaVersion: 1, ArgumentsSHA256: requestDigest, IssueID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", Updated: "now", Fields: []domain.BrokerJiraIssueReadField{{Field: domain.BrokerJiraIssueFieldDescription, Present: true, Null: true}, {Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: "summary"}}, Complete: true}
	previewRequest := requestFor(domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always"}})
	previewDigest, _ := ArgumentsSHA256(previewRequest)
	previewResult := domain.BrokerJiraCommentResult{SchemaVersion: 1, ArgumentsSHA256: previewDigest, OperationTicket: "ticket-1", Mode: "preview", Status: "proposed", ProposalHash: digestChar('1'), NativeCandidateSHA256: digestChar('2'), VersionEvidenceSHA256: digestChar('3'), Complete: true}
	applyRequest := requestFor(domain.BrokerOperationJiraCommentApply, domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always", ExpectedProposalHash: previewResult.ProposalHash, OperationTicket: previewResult.OperationTicket}})
	applyDigest, _ := ArgumentsSHA256(applyRequest)
	applyResult := previewResult
	applyResult.ArgumentsSHA256, applyResult.Mode, applyResult.Status, applyResult.CommentID, applyResult.WriteAttempted, applyResult.Reconciled = applyDigest, "apply", "applied", "9001", true, true
	confluenceRequest := requestFor(domain.BrokerOperationConfluencePageRead, domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: "42", Projection: domain.BrokerConfluenceProjectionMetadata}})
	confluenceDigest, _ := ArgumentsSHA256(confluenceRequest)
	confluenceResult := domain.BrokerConfluencePageReadResult{SchemaVersion: 1, ArgumentsSHA256: confluenceDigest, PageID: "42", Type: "page", Space: "DOCS", Version: 1, Title: "Example", Updated: "now", Projection: domain.BrokerConfluenceProjectionMetadata, Complete: true}
	available := AvailableDefinitions()
	discoveryOperations := make([]domain.BrokerDiscoveryOperation, len(available))
	for index, definition := range available {
		discoveryOperations[index] = domain.BrokerDiscoveryOperation{ID: definition.ID, Version: definition.Version, Availability: domain.BrokerAvailabilityAvailable, Features: wireStrings(definition.RequiredFeatures), Limits: definition.Limits}
	}
	discovery := domain.BrokerDiscoveryProjection{SchemaVersion: 1, ExecutionScopeSHA256: digestChar('a'), RegistrySHA256: RegistrySHA256(), IssuedAtMillis: 2000, ExpiresAtMillis: 7000, Operations: discoveryOperations, Complete: true}
	ticket := domain.BrokerOperationTicket{SchemaVersion: 1, OperationID: "operation-1", PrincipalSHA256: digestChar('a'), ExecutionSHA256: digestChar('b'), AudienceSHA256: digestChar('c'), BackendSHA256: digestChar('d'), Operation: domain.BrokerOperationJiraCommentApply, OperationVersion: 1, ArgumentsSHA256: digestChar('e'), ProposalSHA256: digestChar('f'), IssuedAtMillis: 2000, AcceptUntilMillis: 62000}
	ticketDigest, _ := OperationTicketSHA256(ticket)
	cacheRequest := domain.BrokerCacheQualificationRequest{SchemaVersion: 1, Context: fixtureContext(), SourcePrincipalSHA256: digestChar('a'), SourceReadScopeSHA256: digestChar('b'), Operation: domain.BrokerOperationJiraIssueRead, SelectorSHA256: digestChar('c'), ProjectionSHA256: digestChar('d'), EvidenceSchemaSHA256: digestChar('e'), GenerationSHA256: digestChar('f'), ContentSHA256: digestChar('1'), ExpiresAtMillis: 30000}
	cacheDigest, _ := CacheQualificationRequestSHA256(cacheRequest)
	vectors := []struct {
		name string
		data []byte
	}{
		{"request", mustEncode(EncodeRequestV1(request))}, {"context", mustEncode(EncodeVerifiedContextV1(fixtureContext()))},
		{"admission request", mustEncode(EncodeAdmissionRequestV1(admission))}, {"admission decision", mustEncode(EncodeAdmissionDecisionV1(admissionDecision))},
		{"qualification request", mustEncode(EncodeQualificationRequestV1(qualification))}, {"qualification decision", mustEncode(EncodeQualificationDecisionV1(qualificationDecision))},
		{"operation request", mustEncode(EncodeOperationAuthorizationRequestV1(operation))}, {"operation decision", mustEncode(EncodeOperationDecisionV1(operationDecision))},
		{"proposal request", mustEncode(EncodeProposalAuthorizationRequestV1(proposal))}, {"proposal clearance", mustEncode(EncodeProposalClearanceV1(clearance))},
		{"Jira read result", mustEncode(EncodeJiraIssueReadResultV1(readResult))}, {"Confluence read result", mustEncode(EncodeConfluencePageReadResultV1(confluenceResult))},
		{"preview result", mustEncode(EncodeJiraCommentResultV1(previewResult))}, {"apply result", mustEncode(EncodeJiraCommentResultV1(applyResult))},
		{"discovery", mustEncode(EncodeDiscoveryV1(discovery))},
		{"execution", mustEncode(EncodeExecutionProjectionV1(domain.BrokerExecutionProjection{SchemaVersion: 1, ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", AuthorityRevision: "revision-1", ExpiresAtMillis: 7000, ScopeSHA256: digestChar('a')}))},
		{"ticket", mustEncode(EncodeOperationTicketV1(ticket))}, {"outcome", mustEncode(EncodeOperationOutcomeV1(domain.BrokerOperationOutcome{SchemaVersion: 1, TicketSHA256: ticketDigest, Phase: domain.BrokerOperationOutcomeUnknown, ObservedAtMillis: 3000}))},
		{"cache request", mustEncode(EncodeCacheQualificationRequestV1(cacheRequest))}, {"cache decision", mustEncode(EncodeCacheQualificationV1(domain.BrokerCacheQualification{SchemaVersion: 1, Status: domain.BrokerDecisionAllowed, IssuerSHA256: digestChar('a'), TargetExecutionSHA256: digestChar('b'), AuthorityRevisionSHA256: digestChar('c'), RequestSHA256: cacheDigest, IssuedAtMillis: 2000, ExpiresAtMillis: 7000}))},
	}
	for _, vector := range vectors {
		var instance any
		if err := json.Unmarshal(vector.data, &instance); err != nil {
			t.Errorf("%s is not JSON: %v data=%s", vector.name, err, vector.data)
			continue
		}
		if err := resolved.Validate(instance); err != nil {
			t.Errorf("%s failed schema validation: %v data=%s", vector.name, err, vector.data)
		}
	}
	crossOperation := bytes.Replace(mustEncode(EncodeRequestV1(request)), []byte(`"operation":"jira.issue.read"`), []byte(`"operation":"confluence.page.read"`), 1)
	var invalid any
	_ = json.Unmarshal(crossOperation, &invalid)
	if err := resolved.Validate(invalid); err == nil {
		t.Fatal("schema accepted arguments for a different operation")
	}
	definitions := schema.Defs
	for _, definition := range Registry() {
		for _, reference := range []string{definition.ArgumentSchemaID, definition.ResultSchemaID} {
			name := strings.TrimPrefix(reference, "#/$defs/")
			if definitions[name] == nil {
				t.Errorf("registry reference %q does not resolve", reference)
			}
		}
	}
}

func mustEncode(data []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return data
}

func TestCanonicalDigestGoldenVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/v1/digests.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	if json.Unmarshal(data, &expected) != nil || len(expected) != 10 {
		t.Fatalf("malformed digest fixture: %s", data)
	}
	requestDigest, requestErr := ArgumentsSHA256(fixtureRequest())
	contextDigest, contextErr := VerifiedContextSHA256(fixtureContext())
	ticket := domain.BrokerOperationTicket{SchemaVersion: 1, OperationID: "operation-1", PrincipalSHA256: digestChar('a'), ExecutionSHA256: digestChar('b'), AudienceSHA256: digestChar('c'), BackendSHA256: digestChar('d'), Operation: domain.BrokerOperationJiraCommentApply, OperationVersion: 1, ArgumentsSHA256: digestChar('e'), ProposalSHA256: digestChar('f'), IssuedAtMillis: 2000, AcceptUntilMillis: 62000}
	ticketDigest, ticketErr := OperationTicketSHA256(ticket)
	admission := fixtureAllowedDecision(t)
	_, qualification := fixtureQualification(t)
	_, operation := fixtureOperationAuthorization(t)
	_, clearance := fixtureProposalAuthorization(t)
	cacheRequest := domain.BrokerCacheQualificationRequest{SchemaVersion: 1, Context: fixtureContext(), SourcePrincipalSHA256: digestChar('a'), SourceReadScopeSHA256: digestChar('b'), Operation: domain.BrokerOperationJiraIssueRead, SelectorSHA256: digestChar('c'), ProjectionSHA256: digestChar('d'), EvidenceSchemaSHA256: digestChar('e'), GenerationSHA256: digestChar('f'), ContentSHA256: digestChar('1'), ExpiresAtMillis: 30000}
	cacheDigest, cacheErr := CacheQualificationRequestSHA256(cacheRequest)
	actual := map[string]string{
		"schema_sha256": SchemaSHA256(), "registry_sha256": RegistrySHA256(), "jira_read_arguments_sha256": requestDigest,
		"verified_context_sha256": contextDigest, "operation_ticket_sha256": ticketDigest, "admission_decision_sha256": admission.DecisionSHA256,
		"qualification_decision_sha256": qualification.DecisionSHA256, "operation_decision_sha256": operation.DecisionSHA256,
		"proposal_clearance_sha256": clearance.ClearanceSHA256, "cache_qualification_request_sha256": cacheDigest,
	}
	if requestErr != nil || contextErr != nil || ticketErr != nil || cacheErr != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual=%v expected=%v errors=%v/%v/%v/%v", actual, expected, requestErr, contextErr, ticketErr, cacheErr)
	}
}
