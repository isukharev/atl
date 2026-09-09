package brokercontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/isukharev/atl/internal/domain"
)

func projectPageRequestFixtureV2() domain.BrokerProjectPageRequestV2 {
	return domain.BrokerProjectPageRequestV2{
		SchemaVersion: ExecutionSchemaVersionV2, Operation: domain.BrokerOperationJiraProjectIssuePageRead,
		OperationVersion: ProjectPageOperationVersion, RequestID: "request-page-1",
		Features: []string{"bounded_project_page_v1", domain.BrokerReadConsistencyIdentitySnapshotV1},
		Expect:   domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"},
		Arguments: domain.BrokerProjectPageArguments{
			ProjectKey: "EXAMPLE", Fields: []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldDescription, domain.BrokerProjectPageFieldSummary},
			StartAt: 0, MaxResults: 2,
		},
	}
}

func projectPageAdmissionFixtureV2(t testing.TB) domain.BrokerProjectPageAdmissionRequestV2 {
	t.Helper()
	request := projectPageRequestFixtureV2()
	digest, err := ProjectPageArgumentsSHA256V2(request)
	if err != nil {
		t.Fatal(err)
	}
	return domain.BrokerProjectPageAdmissionRequestV2{
		Context: fixtureContext(), Operation: request.Operation, OperationVersion: request.OperationVersion,
		RequestID: request.RequestID, Features: copyStrings(request.Features), Arguments: request.Arguments,
		ArgumentsSHA256: digest, DeadlineMillis: 30_000,
	}
}

func projectPageDecisionCoreV2(t testing.TB, context domain.BrokerVerifiedContext, requestSHA256, id string) domain.BrokerDecisionCore {
	t.Helper()
	contextDigest, err := VerifiedContextSHA256(context)
	if err != nil {
		t.Fatal(err)
	}
	return domain.BrokerDecisionCore{
		Status: domain.BrokerDecisionAllowed, DecisionID: id, AuthorityRevision: context.AuthorityRevision,
		ContextSHA256: contextDigest, RequestSHA256: requestSHA256, IssuedAtMillis: 2_000, ExpiresAtMillis: 7_000,
	}
}

func projectPageAdmissionDecisionFixtureV2(t testing.TB, admission domain.BrokerProjectPageAdmissionRequestV2) domain.BrokerProjectPageAdmissionDecisionV2 {
	t.Helper()
	digest, err := ProjectPageAdmissionRequestSHA256V2(admission)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := EncodeProjectPageAdmissionDecisionV2(domain.BrokerProjectPageAdmissionDecisionV2{BrokerDecisionCore: projectPageDecisionCoreV2(t, admission.Context, digest, "admission-page-1")})
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeProjectPageAdmissionDecisionV2(wire)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func projectPageQualificationFixtureV2(t testing.TB) (domain.BrokerProjectPageQualificationRequestV2, domain.BrokerProjectPageQualificationDecisionV2) {
	t.Helper()
	admission := projectPageAdmissionFixtureV2(t)
	plan, err := NewProjectPageQualificationPlanV2(admission.ArgumentsSHA256)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.BrokerProjectPageQualificationRequestV2{
		Admission: admission, AdmissionDecision: projectPageAdmissionDecisionFixtureV2(t, admission),
		Plan: plan,
	}
	requestDigest, err := ProjectPageQualificationRequestSHA256V2(request)
	if err != nil {
		t.Fatal(err)
	}
	admissionDigest, _ := ProjectPageAdmissionRequestSHA256V2(admission)
	planDigest, _ := ProjectPageQualificationPlanSHA256V2(request.Plan)
	value := domain.BrokerProjectPageQualificationDecisionV2{
		BrokerDecisionCore:      projectPageDecisionCoreV2(t, admission.Context, requestDigest, "qualification-page-1"),
		AdmissionRequestSHA256:  admissionDigest,
		AdmissionDecisionSHA256: request.AdmissionDecision.DecisionSHA256,
		PlanSHA256:              planDigest,
	}
	wire, err := EncodeProjectPageQualificationDecisionV2(value)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := DecodeProjectPageQualificationDecisionV2(wire)
	if err != nil {
		t.Fatal(err)
	}
	return request, decision
}

func qualifiedProjectPageIssueFixtureV2(t testing.TB, id, key string) domain.BrokerQualifiedJiraProjectPageIssueV2 {
	t.Helper()
	identity := domain.BrokerJiraProjectPageIssueIdentityV2{ID: id, Key: key, ProjectID: "7", ProjectKey: "EXAMPLE", Updated: "2026-09-08T10:00:00.000+0000", Complete: true}
	version, projection, err := JiraProjectPageIssueIdentityEvidenceSHA256V2(identity)
	if err != nil {
		t.Fatal(err)
	}
	return domain.BrokerQualifiedJiraProjectPageIssueV2{ID: id, Key: key, ProjectID: "7", ProjectKey: "EXAMPLE", Updated: identity.Updated, VersionEvidenceSHA256: version, ProjectionSHA256: projection}
}

func projectPageOperationFixtureV2(t testing.TB) (domain.BrokerProjectPageOperationAuthorizationRequestV2, domain.BrokerProjectPageOperationDecisionV2) {
	t.Helper()
	qualification, qualificationDecision := projectPageQualificationFixtureV2(t)
	projectIdentity := domain.BrokerJiraProjectIdentityV2{ID: "7", Key: "EXAMPLE", Complete: true}
	projectDigest, err := JiraProjectIdentityEvidenceSHA256V2(projectIdentity)
	if err != nil {
		t.Fatal(err)
	}
	project := domain.BrokerQualifiedJiraProjectV2{ID: "7", Key: "EXAMPLE", IdentityProjectionSHA256: projectDigest}
	first := qualifiedProjectPageIssueFixtureV2(t, "3", "EXAMPLE-3")
	second := qualifiedProjectPageIssueFixtureV2(t, "20", "EXAMPLE-20")
	request := domain.BrokerProjectPageOperationAuthorizationRequestV2{
		QualificationRequest: qualification, QualificationDecision: qualificationDecision, Project: project,
		Issues: []domain.BrokerQualifiedJiraProjectPageIssueV2{first, second},
		Page: domain.BrokerProjectPageEvidenceV2{
			RequestedStartAt: 0, RequestedMaxResults: 2, ReturnedStartAt: 0, ReturnedMaxResults: 2, Total: 3,
			OrderedIssueIDs: []string{"20", "3"},
		},
		Effects: []domain.BrokerProjectPageEffectV2{
			{Kind: domain.BrokerEffectRead, Project: &project, Fields: []string{"id", "key", "pagination"}},
			{Kind: domain.BrokerEffectRead, Issue: &first, Fields: []string{"description", "id", "key", "project", "summary", "updated"}},
			{Kind: domain.BrokerEffectRead, Issue: &second, Fields: []string{"description", "id", "key", "project", "summary", "updated"}},
		},
	}
	requestDigest, err := ProjectPageOperationAuthorizationRequestSHA256V2(request)
	if err != nil {
		t.Fatal(err)
	}
	resourcesDigest, _ := ProjectPageResourcesSHA256V2(project, request.Issues)
	pageDigest, _ := ProjectPageEvidenceSHA256V2(request.Page)
	effectsDigest, _ := ProjectPageEffectsSHA256V2(request.Effects)
	value := domain.BrokerProjectPageOperationDecisionV2{
		BrokerDecisionCore:          projectPageDecisionCoreV2(t, qualification.Admission.Context, requestDigest, "operation-page-1"),
		QualificationDecisionSHA256: qualificationDecision.DecisionSHA256, Operation: qualification.Admission.Operation,
		OperationVersion: qualification.Admission.OperationVersion, ArgumentsSHA256: qualification.Admission.ArgumentsSHA256,
		ResourcesSHA256: resourcesDigest, PageSHA256: pageDigest, EffectsSHA256: effectsDigest,
	}
	wire, err := EncodeProjectPageOperationDecisionV2(value)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := DecodeProjectPageOperationDecisionV2(wire)
	if err != nil {
		t.Fatal(err)
	}
	return request, decision
}

func projectPageResultFixtureV2(t testing.TB) domain.BrokerJiraProjectPageResultV2 {
	t.Helper()
	request := projectPageRequestFixtureV2()
	digest, err := ProjectPageArgumentsSHA256V2(request)
	if err != nil {
		t.Fatal(err)
	}
	fields := func(summary string) []domain.BrokerJiraIssueReadField {
		return []domain.BrokerJiraIssueReadField{
			{Field: domain.BrokerJiraIssueFieldDescription, Present: true, Null: true},
			{Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: summary},
		}
	}
	return domain.BrokerJiraProjectPageResultV2{
		SchemaVersion: ExecutionSchemaVersionV2, ArgumentsSHA256: digest, ConsistencyProfile: domain.BrokerReadConsistencyIdentitySnapshotV1,
		ProjectID: "7", ProjectKey: "EXAMPLE",
		Issues: []domain.BrokerJiraProjectPageResultIssueV2{
			{ID: "20", Key: "EXAMPLE-20", ProjectID: "7", ProjectKey: "EXAMPLE", Updated: "2026-09-08T10:00:00.000+0000", Fields: fields("Twenty")},
			{ID: "3", Key: "EXAMPLE-3", ProjectID: "7", ProjectKey: "EXAMPLE", Updated: "2026-09-08T10:00:00.000+0000", Fields: fields("Three")},
		},
		Page:     domain.BrokerJiraProjectPageResultPageV2{StartAt: 0, MaxResults: 2, Total: 3, Count: 2, NextCursor: "2", NextCursorPresent: true},
		Complete: true,
	}
}

func TestProjectPageV2RequestAndRegistryAreClosedAndAvailable(t *testing.T) {
	request := projectPageRequestFixtureV2()
	request.Arguments.Fields = []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldSummary, domain.BrokerProjectPageFieldDescription}
	wire, err := EncodeProjectPageRequestV2(request)
	decoded, decodeErr := DecodeProjectPageRequestV2(wire)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded.Arguments.Fields, []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldDescription, domain.BrokerProjectPageFieldSummary}) {
		t.Fatalf("decoded=%+v errors=%v/%v wire=%s", decoded, err, decodeErr, wire)
	}
	definitions := RegistryV2()
	if len(definitions) != 1 || definitions[0].Definition.ID != request.Operation || !definitions[0].Definition.Available ||
		definitions[0].Definition.Limits.MaxResources != 16 || definitions[0].MaxEffects != 16 || definitions[0].Definition.Limits.MaxTotalUpstreamRequests != 3 ||
		definitions[0].Definition.Limits.MaxTotalUpstreamResponseBytes != 68_419_584 || !validDigest(RegistrySHA256V2()) ||
		RegistrySHA256() != "a712329120114874b6d1c2f62884ca28a45cdef8616fd78073bc3a6a72fae72c" ||
		SchemaSHA256() != "fdf82ad96e59a6c32f639f4602da15632dfcb72ab0fb734df00782bf29967372" {
		t.Fatalf("definitions=%+v v2=%s v1=%s/%s", definitions, RegistrySHA256V2(), RegistrySHA256(), SchemaSHA256())
	}
}

func TestProjectPageV2CanonicalDigestGoldens(t *testing.T) {
	data, err := os.ReadFile("testdata/execution-v2/digests.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	if json.Unmarshal(data, &expected) != nil || len(expected) != 10 {
		t.Fatalf("malformed digest fixture: %s", data)
	}
	request := projectPageRequestFixtureV2()
	arguments, argumentsErr := ProjectPageArgumentsSHA256V2(request)
	projectIdentity := domain.BrokerJiraProjectIdentityV2{ID: "7", Key: "EXAMPLE", Complete: true}
	project, projectErr := JiraProjectIdentityEvidenceSHA256V2(projectIdentity)
	issue := domain.BrokerJiraProjectPageIssueIdentityV2{ID: "3", Key: "EXAMPLE-3", ProjectID: "7", ProjectKey: "EXAMPLE", Updated: "2026-09-08T10:00:00.000+0000", Complete: true}
	issueVersion, issueProjection, issueErr := JiraProjectPageIssueIdentityEvidenceSHA256V2(issue)
	plan, newPlanErr := NewProjectPageQualificationPlanV2(arguments)
	planDigest, planErr := ProjectPageQualificationPlanSHA256V2(plan)
	operation, _ := projectPageOperationFixtureV2(t)
	resources, resourcesErr := ProjectPageResourcesSHA256V2(operation.Project, operation.Issues)
	page, pageErr := ProjectPageEvidenceSHA256V2(operation.Page)
	effects, effectsErr := ProjectPageEffectsSHA256V2(operation.Effects)
	actual := map[string]string{
		"schema_sha256": ExecutionSchemaSHA256V2(), "registry_sha256": RegistrySHA256V2(), "arguments_sha256": arguments,
		"project_identity_sha256": project, "issue_version_sha256": issueVersion, "issue_projection_sha256": issueProjection,
		"qualification_plan_sha256": planDigest, "resources_sha256": resources, "page_sha256": page, "effects_sha256": effects,
	}
	if argumentsErr != nil || projectErr != nil || issueErr != nil || newPlanErr != nil || planErr != nil || resourcesErr != nil || pageErr != nil || effectsErr != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual=%v expected=%v errors=%v/%v/%v/%v/%v/%v/%v/%v", actual, expected, argumentsErr, projectErr, issueErr, newPlanErr, planErr, resourcesErr, pageErr, effectsErr)
	}
}

func TestProjectPageV2AuthorizationRoundTripsAndBindsPageOrder(t *testing.T) {
	admission := projectPageAdmissionFixtureV2(t)
	admissionWire := mustEncode(EncodeProjectPageAdmissionRequestV2(admission))
	decodedAdmission, err := DecodeProjectPageAdmissionRequestV2(admissionWire)
	if err != nil || !reflect.DeepEqual(decodedAdmission, normalizeProjectPageAdmissionRequestV2(admission)) {
		t.Fatalf("admission=%+v err=%v", decodedAdmission, err)
	}
	qualification, qualificationDecision := projectPageQualificationFixtureV2(t)
	qualificationWire := mustEncode(EncodeProjectPageQualificationRequestV2(qualification))
	decodedQualification, err := DecodeProjectPageQualificationRequestV2(qualificationWire)
	if err != nil || !reflect.DeepEqual(decodedQualification, qualification) || ValidateProjectPageQualificationDecisionV2(qualificationDecision, qualification, 3_000) != nil {
		t.Fatalf("qualification=%+v err=%v", decodedQualification, err)
	}
	operation, operationDecision := projectPageOperationFixtureV2(t)
	operationWire := mustEncode(EncodeProjectPageOperationAuthorizationRequestV2(operation))
	decodedOperation, err := DecodeProjectPageOperationAuthorizationRequestV2(operationWire)
	if err != nil || !reflect.DeepEqual(decodedOperation, operation) || ValidateProjectPageOperationDecisionV2(operationDecision, operation, 3_000) != nil ||
		!slices.Equal(decodedOperation.Page.OrderedIssueIDs, []string{"20", "3"}) || decodedOperation.Issues[0].ID != "3" {
		t.Fatalf("operation=%+v err=%v", decodedOperation, err)
	}
	changed := operation
	changed.Page.OrderedIssueIDs = []string{"3", "20"}
	if ValidateProjectPageOperationDecisionV2(operationDecision, changed, 3_000) == nil {
		t.Fatal("operation decision accepted changed backend row order")
	}
	changed = operation
	changed.Effects[1].Fields = []string{"id", "key", "project", "updated"}
	if _, err := EncodeProjectPageOperationAuthorizationRequestV2(changed); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("narrowed per-issue effect error=%v", err)
	}
}

func TestProjectPageV2ResultTruthAndRequestBinding(t *testing.T) {
	request := projectPageRequestFixtureV2()
	result := projectPageResultFixtureV2(t)
	wire, err := EncodeJiraProjectPageResultV2(result)
	decoded, decodeErr := DecodeJiraProjectPageResultV2(wire)
	if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, result) || ValidateJiraProjectPageResultForRequestV2(decoded, request) != nil {
		t.Fatalf("decoded=%+v errors=%v/%v wire=%s", decoded, err, decodeErr, wire)
	}
	stalled := result
	stalled.Issues = []domain.BrokerJiraProjectPageResultIssueV2{}
	stalled.Page = domain.BrokerJiraProjectPageResultPageV2{StartAt: 0, MaxResults: 2, Total: 3, Count: 0, PartialReason: BrokerProjectPagePartialPaginationStalledV2}
	wire, err = EncodeJiraProjectPageResultV2(stalled)
	if err != nil || bytes.Contains(wire, []byte(`"next_cursor"`)) || !bytes.Contains(wire, []byte(`"selection_complete":false`)) {
		t.Fatalf("stalled wire=%s err=%v", wire, err)
	}
	empty := stalled
	empty.Page.Total, empty.Page.CoordinateExhausted, empty.Page.PartialReason = 0, true, ""
	if _, err := EncodeJiraProjectPageResultV2(empty); err != nil {
		t.Fatalf("empty terminal result: %v", err)
	}
	falseClaims := []domain.BrokerJiraProjectPageResultV2{
		normalizeJiraProjectPageResultV2(result), normalizeJiraProjectPageResultV2(result), normalizeJiraProjectPageResultV2(result),
	}
	falseClaims[0].Page.SelectionComplete = true
	falseClaims[1].Page.NextCursorPresent = false
	falseClaims[2].Issues[0].ProjectID = "8"
	for index, value := range falseClaims {
		if _, err := EncodeJiraProjectPageResultV2(value); !errors.Is(err, domain.ErrUsage) {
			t.Errorf("false claim %d error=%v", index, err)
		}
	}
	identityOnlyRequest := request
	identityOnlyRequest.Arguments.Fields = []domain.BrokerProjectPageField{}
	identityOnly := result
	identityOnly.ArgumentsSHA256, _ = ProjectPageArgumentsSHA256V2(identityOnlyRequest)
	for index := range identityOnly.Issues {
		identityOnly.Issues[index].Fields = []domain.BrokerJiraIssueReadField{}
	}
	if wire, err := EncodeJiraProjectPageResultV2(identityOnly); err != nil || ValidateJiraProjectPageResultForRequestV2(identityOnly, identityOnlyRequest) != nil || !bytes.Contains(wire, []byte(`"fields":[]`)) {
		t.Fatalf("identity-only wire=%s err=%v", wire, err)
	}
}

func TestProjectPageV2ResultStopsAtSupportedOffsetCeiling(t *testing.T) {
	baseRequest := projectPageRequestFixtureV2()
	baseResult := projectPageResultFixtureV2(t)
	baseResult.Issues = append([]domain.BrokerJiraProjectPageResultIssueV2{}, baseResult.Issues[:1]...)

	ceilingRequest := baseRequest
	ceilingRequest.Arguments.StartAt = domain.BrokerProjectPageMaxStartAt
	ceiling := baseResult
	ceiling.ArgumentsSHA256, _ = ProjectPageArgumentsSHA256V2(ceilingRequest)
	ceiling.Page = domain.BrokerJiraProjectPageResultPageV2{
		StartAt: domain.BrokerProjectPageMaxStartAt, MaxResults: 2, Total: domain.BrokerProjectPageMaxStartAt + 2,
		Count: 1, PartialReason: BrokerProjectPagePartialOffsetLimitV2,
	}
	wire, err := EncodeJiraProjectPageResultV2(ceiling)
	if err != nil || ValidateJiraProjectPageResultForRequestV2(ceiling, ceilingRequest) != nil || bytes.Contains(wire, []byte(`"next_cursor"`)) {
		t.Fatalf("ceiling wire=%s err=%v", wire, err)
	}

	belowRequest := baseRequest
	belowRequest.Arguments.StartAt = domain.BrokerProjectPageMaxStartAt - 1
	below := baseResult
	below.ArgumentsSHA256, _ = ProjectPageArgumentsSHA256V2(belowRequest)
	below.Page = domain.BrokerJiraProjectPageResultPageV2{
		StartAt: domain.BrokerProjectPageMaxStartAt - 1, MaxResults: 2, Total: domain.BrokerProjectPageMaxStartAt + 1,
		Count: 1, NextCursor: "1000000", NextCursorPresent: true,
	}
	if _, err := EncodeJiraProjectPageResultV2(below); err != nil || ValidateJiraProjectPageResultForRequestV2(below, belowRequest) != nil {
		t.Fatalf("below-ceiling result err=%v", err)
	}

	exhausted := ceiling
	exhausted.Page.Total = domain.BrokerProjectPageMaxStartAt + 1
	exhausted.Page.CoordinateExhausted = true
	exhausted.Page.PartialReason = ""
	if _, err := EncodeJiraProjectPageResultV2(exhausted); err != nil {
		t.Fatalf("exhaustion did not win at ceiling: %v", err)
	}
}

func TestProjectPageV2StrictDecodersRejectAuthorityAndShapeSmuggling(t *testing.T) {
	valid := mustEncode(EncodeProjectPageRequestV2(projectPageRequestFixtureV2()))
	private := "PRIVATE-CANARY.example.invalid/secret"
	inputs := [][]byte{
		bytes.Replace(valid, []byte(`"project_key":"EXAMPLE"`), []byte(`"project_key":"EXAMPLE","jql":"project=PRIVATE"`), 1),
		bytes.Replace(valid, []byte(`"project_key":"EXAMPLE"`), []byte(`"project_key":"EXAMPLE","backend_url":"https://`+private+`"`), 1),
		bytes.Replace(valid, []byte(`"fields":["description","summary"]`), []byte(`"fields":["summary","summary"]`), 1),
		bytes.Replace(valid, []byte(`"max_results":2`), []byte(`"max_results":16`), 1),
		bytes.Replace(valid, []byte(`"start_at":0`), []byte(`"start_at":1000001`), 1),
		bytes.Replace(valid, []byte(`"schema_version":2`), []byte(`"schema_version":1`), 1),
		append(bytes.Clone(valid), []byte(`{}`)...),
	}
	for index, input := range inputs {
		_, err := DecodeProjectPageRequestV2(input)
		if err == nil || strings.Contains(fmtAll(err), private) {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
	resultWire := mustEncode(EncodeJiraProjectPageResultV2(projectPageResultFixtureV2(t)))
	for index, input := range [][]byte{
		bytes.Replace(resultWire, []byte(`"complete":true`), []byte(`"complete":true,"private":"`+private+`"`), 1),
		bytes.Replace(resultWire, []byte(`"selection_complete":false`), []byte(`"selection_complete":true`), 1),
		bytes.Replace(resultWire, []byte(`"next_cursor":"2"`), []byte(`"next_cursor":"2","next_cursor":"3"`), 1),
	} {
		_, err := DecodeJiraProjectPageResultV2(input)
		if err == nil || strings.Contains(fmtAll(err), private) {
			t.Fatalf("result case %d error=%v", index, err)
		}
	}
}

func TestProjectPageV2RejectsOversizedIssueKeys(t *testing.T) {
	key := "EXAMPLE-" + strings.Repeat("1", domain.BrokerMaxIdentifierBytes)
	operation, _ := projectPageOperationFixtureV2(t)
	issue := &operation.Issues[0]
	issue.Key = key
	// Rebind otherwise-valid evidence so rejection proves the identifier bound,
	// not an incidental stale projection digest.
	projection, err := digestExecutionV2("jira-project-page-issue-identity-projection", struct {
		ID         string `json:"id"`
		Key        string `json:"key"`
		ProjectID  string `json:"project_id"`
		ProjectKey string `json:"project_key"`
		Updated    string `json:"updated"`
	}{issue.ID, issue.Key, issue.ProjectID, issue.ProjectKey, issue.Updated})
	if err != nil {
		t.Fatal(err)
	}
	issue.ProjectionSHA256 = projection
	operation.Effects[1].Issue = issue
	operationWire, err := json.Marshal(projectPageOperationAuthorizationRequestToWireV2(operation))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeProjectPageOperationAuthorizationRequestV2(operationWire); err == nil {
		t.Error("authorization accepted oversized issue key with matching evidence")
	}
	result := projectPageResultFixtureV2(t)
	result.Issues[0].Key = key
	resultWire, err := json.Marshal(jiraProjectPageResultToWireV2(result))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeJiraProjectPageResultV2(resultWire); err == nil {
		t.Error("result accepted oversized issue key")
	}
}

func fmtAll(err error) string {
	return strings.Join([]string{err.Error(), strings.TrimSpace(string(mustJSON(err.Error())))}, " ")
}

func mustJSON(value string) []byte {
	data, _ := json.Marshal(value)
	return data
}

func TestPublishedExecutionV2SchemaMatchesAndValidatesVectors(t *testing.T) {
	published, err := os.ReadFile("../../docs/schemas/broker-execution-v2.schema.json")
	if err != nil || !bytes.Equal(published, ExecutionSchemaV2()) || !validDigest(ExecutionSchemaSHA256V2()) {
		t.Fatalf("published schema mismatch err=%v", err)
	}
	var schema, v1 jsonschema.Schema
	if json.Unmarshal(ExecutionSchemaV2(), &schema) != nil || json.Unmarshal(SchemaV1(), &v1) != nil {
		t.Fatal("schema JSON is invalid")
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		if strings.HasSuffix(uri.Path, "/broker-v1.schema.json") {
			return &v1, nil
		}
		return nil, errors.New("unrecognized schema")
	}})
	if err != nil {
		t.Fatal(err)
	}
	admission := projectPageAdmissionFixtureV2(t)
	admissionDecision := projectPageAdmissionDecisionFixtureV2(t, admission)
	qualification, qualificationDecision := projectPageQualificationFixtureV2(t)
	operation, operationDecision := projectPageOperationFixtureV2(t)
	vectors := [][]byte{
		mustEncode(EncodeProjectPageRequestV2(projectPageRequestFixtureV2())),
		mustEncode(EncodeProjectPageAdmissionRequestV2(admission)), mustEncode(EncodeProjectPageAdmissionDecisionV2(admissionDecision)),
		mustEncode(EncodeProjectPageQualificationRequestV2(qualification)), mustEncode(EncodeProjectPageQualificationDecisionV2(qualificationDecision)),
		mustEncode(EncodeProjectPageOperationAuthorizationRequestV2(operation)), mustEncode(EncodeProjectPageOperationDecisionV2(operationDecision)),
		mustEncode(EncodeJiraProjectPageResultV2(projectPageResultFixtureV2(t))),
	}
	for index, vector := range vectors {
		var value any
		if json.Unmarshal(vector, &value) != nil || resolved.Validate(value) != nil {
			t.Fatalf("schema rejected vector %d: %s", index, vector)
		}
	}
	for _, vector := range [][]byte{vectors[5], vectors[7]} {
		oversized := bytes.ReplaceAll(vector, []byte(`"EXAMPLE-20"`), []byte(`"EXAMPLE-`+strings.Repeat("1", domain.BrokerMaxIdentifierBytes)+`"`))
		var value any
		if bytes.Equal(oversized, vector) || json.Unmarshal(oversized, &value) != nil {
			t.Fatal("oversized-key schema fixture is invalid")
		}
		if resolved.Validate(value) == nil {
			t.Error("published schema accepted oversized issue key")
		}
	}
	resultWire := vectors[len(vectors)-1]
	for name, invalidWire := range map[string][]byte{
		"field outside v2":  bytes.Replace(resultWire, []byte(`"field":"description"`), []byte(`"field":"status"`), 1),
		"cursor over limit": bytes.Replace(resultWire, []byte(`"next_cursor":"2"`), []byte(`"next_cursor":"1000001"`), 1),
	} {
		var value any
		if json.Unmarshal(invalidWire, &value) != nil || resolved.Validate(value) == nil {
			t.Errorf("schema accepted %s: %s", name, invalidWire)
		}
	}
}

func FuzzDecodeProjectPageRequestV2(f *testing.F) {
	valid, _ := EncodeProjectPageRequestV2(projectPageRequestFixtureV2())
	f.Add(valid)
	f.Add([]byte(`{"schema_version":2,"operation":"raw.http"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := DecodeProjectPageRequestV2(data)
		if err != nil {
			return
		}
		encoded, err := EncodeProjectPageRequestV2(value)
		if err != nil || int64(len(encoded)) > MaxProjectPageRequestBytesV2 {
			t.Fatalf("accepted request failed re-encode: %v", err)
		}
	})
}

func FuzzDecodeJiraProjectPageResultV2(f *testing.F) {
	valid, _ := EncodeJiraProjectPageResultV2(projectPageResultFixtureV2(f))
	f.Add(valid)
	f.Add([]byte(`{"schema_version":2}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		value, err := DecodeJiraProjectPageResultV2(data)
		if err != nil {
			return
		}
		encoded, err := EncodeJiraProjectPageResultV2(value)
		if err != nil || int64(len(encoded)) > MaxProjectPageResultWireBytesV2 {
			t.Fatalf("accepted result failed re-encode: %v", err)
		}
	})
}
