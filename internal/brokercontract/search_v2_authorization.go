package brokercontract

import (
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

func JiraProjectIdentityEvidenceSHA256V2(value domain.BrokerJiraProjectIdentityV2) (string, error) {
	if !validJiraProjectIdentityV2(value) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	projection := struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}{value.ID, value.Key}
	return digestExecutionV2("jira-project-identity-projection", projection)
}

func JiraProjectPageIssueIdentityEvidenceSHA256V2(value domain.BrokerJiraProjectPageIssueIdentityV2) (versionSHA256, projectionSHA256 string, err error) {
	if !validJiraProjectPageIssueIdentityV2(value) {
		return "", "", reject(domain.BrokerReasonMalformed)
	}
	version := struct {
		ID      string `json:"id"`
		Updated string `json:"updated"`
	}{value.ID, value.Updated}
	projection := struct {
		ID         string `json:"id"`
		Key        string `json:"key"`
		ProjectID  string `json:"project_id"`
		ProjectKey string `json:"project_key"`
		Updated    string `json:"updated"`
	}{value.ID, value.Key, value.ProjectID, value.ProjectKey, value.Updated}
	versionSHA256, err = digestExecutionV2("jira-project-page-issue-version-evidence", version)
	if err != nil {
		return "", "", err
	}
	projectionSHA256, err = digestExecutionV2("jira-project-page-issue-identity-projection", projection)
	return versionSHA256, projectionSHA256, err
}

func validJiraProjectIdentityV2(value domain.BrokerJiraProjectIdentityV2) bool {
	return value.Complete && validPositiveDecimal(value.ID) && validProjectKeyV2(value.Key)
}

func validJiraProjectPageIssueIdentityV2(value domain.BrokerJiraProjectPageIssueIdentityV2) bool {
	return value.Complete && validPositiveDecimal(value.ID) && len(value.Key) <= domain.BrokerMaxIdentifierBytes && domain.ValidJiraIssueKey(value.Key) &&
		validPositiveDecimal(value.ProjectID) && validProjectKeyV2(value.ProjectKey) && strings.HasPrefix(value.Key, value.ProjectKey+"-") &&
		value.Updated != "" && len(value.Updated) <= domain.BrokerMaxIdentifierBytes && validBrokerText(value.Updated)
}

func ProjectPageResourcesSHA256V2(project domain.BrokerQualifiedJiraProjectV2, issues []domain.BrokerQualifiedJiraProjectPageIssueV2) (string, error) {
	if !validQualifiedJiraProjectV2(project) || len(issues)+1 > MaxProjectPageResourcesV2 || !canonicalQualifiedIssuesV2(issues) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	wireIssues := make([]qualifiedJiraProjectPageIssueWireV2, len(issues))
	for index, issue := range issues {
		wireIssues[index] = qualifiedJiraProjectPageIssueToWireV2(issue)
	}
	value := struct {
		Project qualifiedJiraProjectWireV2            `json:"project"`
		Issues  []qualifiedJiraProjectPageIssueWireV2 `json:"issues"`
	}{qualifiedJiraProjectToWireV2(project), wireIssues}
	return digestExecutionV2("qualified-resources", value)
}

func ProjectPageEvidenceSHA256V2(value domain.BrokerProjectPageEvidenceV2) (string, error) {
	if !validProjectPageEvidenceShapeV2(value) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestExecutionV2("page-evidence", projectPageEvidenceToWireV2(value))
}

func ProjectPageEffectsSHA256V2(values []domain.BrokerProjectPageEffectV2) (string, error) {
	if len(values) == 0 || len(values) > MaxProjectPageEffectsV2 || !canonicalProjectPageEffectsV2(values) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	wire := make([]projectPageEffectWireV2, len(values))
	for index, value := range values {
		wire[index] = projectPageEffectToWireV2(value)
	}
	return digestExecutionV2("effects", wire)
}

func EncodeProjectPageOperationAuthorizationRequestV2(value domain.BrokerProjectPageOperationAuthorizationRequestV2) ([]byte, error) {
	value = normalizeProjectPageOperationAuthorizationRequestV2(value)
	if err := validateProjectPageOperationAuthorizationRequestV2(value); err != nil {
		return nil, err
	}
	return marshalEnvelope(projectPageOperationAuthorizationRequestToWireV2(value), MaxEnvelopeBytes)
}

func DecodeProjectPageOperationAuthorizationRequestV2(data []byte) (domain.BrokerProjectPageOperationAuthorizationRequestV2, error) {
	var wire projectPageOperationAuthorizationRequestWireV2
	if err := strictDecodeExecutionV2(data, MaxEnvelopeBytes, &wire); err != nil {
		return domain.BrokerProjectPageOperationAuthorizationRequestV2{}, err
	}
	if !projectPageOperationAuthorizationWireVersionsV2(wire) {
		return domain.BrokerProjectPageOperationAuthorizationRequestV2{}, reject(domain.BrokerReasonUnsupported)
	}
	value := projectPageOperationAuthorizationRequestFromWireV2(wire)
	if err := validateProjectPageOperationAuthorizationRequestV2(value); err != nil {
		return domain.BrokerProjectPageOperationAuthorizationRequestV2{}, err
	}
	return value, nil
}

func ProjectPageOperationAuthorizationRequestSHA256V2(value domain.BrokerProjectPageOperationAuthorizationRequestV2) (string, error) {
	value = normalizeProjectPageOperationAuthorizationRequestV2(value)
	if err := validateProjectPageOperationAuthorizationRequestV2(value); err != nil {
		return "", err
	}
	return digestExecutionV2("operation-authorization-request", projectPageOperationAuthorizationRequestToWireV2(value))
}

func validateProjectPageOperationAuthorizationRequestV2(value domain.BrokerProjectPageOperationAuthorizationRequestV2) error {
	if validateProjectPageQualificationRequestV2(value.QualificationRequest) != nil ||
		ValidateProjectPageQualificationDecisionV2(value.QualificationDecision, value.QualificationRequest, value.QualificationDecision.IssuedAtMillis) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	admission := value.QualificationRequest.Admission
	if !validQualifiedJiraProjectV2(value.Project) || value.Project.Key != admission.Arguments.ProjectKey ||
		len(value.Issues) > domain.BrokerProjectPageMaxResults || len(value.Issues)+1 > MaxProjectPageResourcesV2 || !canonicalQualifiedIssuesV2(value.Issues) ||
		!validProjectPageEvidenceForRequestV2(value.Page, admission.Arguments, value.Issues) || len(value.Effects) != len(value.Issues)+1 ||
		!canonicalProjectPageEffectsV2(value.Effects) || !projectPageEffectsMatchV2(value) {
		return reject(domain.BrokerReasonMalformed)
	}
	for _, issue := range value.Issues {
		if issue.ProjectID != value.Project.ID || issue.ProjectKey != value.Project.Key {
			return reject(domain.BrokerReasonMalformed)
		}
	}
	return nil
}

func validQualifiedJiraProjectV2(value domain.BrokerQualifiedJiraProjectV2) bool {
	identity := domain.BrokerJiraProjectIdentityV2{ID: value.ID, Key: value.Key, Complete: true}
	digest, err := JiraProjectIdentityEvidenceSHA256V2(identity)
	return err == nil && value.IdentityProjectionSHA256 == digest
}

func validQualifiedJiraProjectPageIssueV2(value domain.BrokerQualifiedJiraProjectPageIssueV2) bool {
	identity := domain.BrokerJiraProjectPageIssueIdentityV2{ID: value.ID, Key: value.Key, ProjectID: value.ProjectID, ProjectKey: value.ProjectKey, Updated: value.Updated, Complete: true}
	version, projection, err := JiraProjectPageIssueIdentityEvidenceSHA256V2(identity)
	return err == nil && value.VersionEvidenceSHA256 == version && value.ProjectionSHA256 == projection
}

func canonicalQualifiedIssuesV2(values []domain.BrokerQualifiedJiraProjectPageIssueV2) bool {
	seenIDs, seenKeys := map[string]bool{}, map[string]bool{}
	for index, value := range values {
		if !validQualifiedJiraProjectPageIssueV2(value) || seenIDs[value.ID] || seenKeys[value.Key] ||
			index > 0 && !numericIDLessV2(values[index-1].ID, value.ID) {
			return false
		}
		seenIDs[value.ID], seenKeys[value.Key] = true, true
	}
	return true
}

func numericIDLessV2(left, right string) bool {
	a, leftErr := strconv.ParseUint(left, 10, 64)
	b, rightErr := strconv.ParseUint(right, 10, 64)
	return leftErr == nil && rightErr == nil && a < b
}

func validProjectPageEvidenceShapeV2(value domain.BrokerProjectPageEvidenceV2) bool {
	if value.RequestedStartAt < 0 || value.RequestedStartAt > domain.BrokerProjectPageMaxStartAt ||
		value.RequestedMaxResults <= 0 || value.RequestedMaxResults > domain.BrokerProjectPageMaxResults ||
		value.ReturnedStartAt != value.RequestedStartAt || value.ReturnedMaxResults <= 0 || value.ReturnedMaxResults > value.RequestedMaxResults ||
		value.Total < 0 || len(value.OrderedIssueIDs) > value.RequestedMaxResults || len(value.OrderedIssueIDs) > value.ReturnedMaxResults || value.SelectionComplete {
		return false
	}
	seen := map[string]bool{}
	for _, id := range value.OrderedIssueIDs {
		if !validPositiveDecimal(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	next := value.ReturnedStartAt + len(value.OrderedIssueIDs)
	if next < value.ReturnedStartAt || next > value.Total {
		return false
	}
	exhausted := next == value.Total
	stalled := len(value.OrderedIssueIDs) == 0 && next < value.Total
	return value.CoordinateExhausted == exhausted && value.PaginationStalled == stalled
}

func validProjectPageEvidenceForRequestV2(value domain.BrokerProjectPageEvidenceV2, arguments domain.BrokerProjectPageArguments, issues []domain.BrokerQualifiedJiraProjectPageIssueV2) bool {
	if !validProjectPageEvidenceShapeV2(value) || value.RequestedStartAt != arguments.StartAt || value.RequestedMaxResults != arguments.MaxResults ||
		len(value.OrderedIssueIDs) != len(issues) {
		return false
	}
	set := make(map[string]bool, len(issues))
	for _, issue := range issues {
		set[issue.ID] = true
	}
	for _, id := range value.OrderedIssueIDs {
		if !set[id] {
			return false
		}
	}
	return true
}

func canonicalProjectPageEffectsV2(values []domain.BrokerProjectPageEffectV2) bool {
	for index, effect := range values {
		if effect.Kind != domain.BrokerEffectRead || !validProjectPageEffectFieldsV2(effect.Fields) || index == 0 && (effect.Project == nil || effect.Issue != nil || !validQualifiedJiraProjectV2(*effect.Project)) ||
			index > 0 && (effect.Project != nil || effect.Issue == nil) {
			return false
		}
		if index == 0 && !slices.Equal(effect.Fields, []string{"id", "key", "pagination"}) {
			return false
		}
		if index > 0 && !validQualifiedJiraProjectPageIssueV2(*effect.Issue) {
			return false
		}
		if index > 0 && (!slices.Contains(effect.Fields, "id") || !slices.Contains(effect.Fields, "key") || !slices.Contains(effect.Fields, "project") || !slices.Contains(effect.Fields, "updated") || slices.Contains(effect.Fields, "pagination")) {
			return false
		}
		if index > 1 && !numericIDLessV2(values[index-1].Issue.ID, effect.Issue.ID) {
			return false
		}
	}
	return true
}

func validProjectPageEffectFieldsV2(fields []string) bool {
	if len(fields) == 0 || len(fields) > 6 || !sort.StringsAreSorted(fields) {
		return false
	}
	allowed := map[string]bool{"description": true, "id": true, "key": true, "pagination": true, "project": true, "summary": true, "updated": true}
	seen := map[string]bool{}
	for _, field := range fields {
		if !allowed[field] || seen[field] {
			return false
		}
		seen[field] = true
	}
	return true
}

func projectPageEffectsMatchV2(value domain.BrokerProjectPageOperationAuthorizationRequestV2) bool {
	if len(value.Effects) != len(value.Issues)+1 || value.Effects[0].Project == nil || *value.Effects[0].Project != value.Project ||
		!slices.Equal(value.Effects[0].Fields, []string{"id", "key", "pagination"}) {
		return false
	}
	wantFields := projectPageIssueEffectFieldsV2(value.QualificationRequest.Admission.Arguments.Fields)
	for index, issue := range value.Issues {
		effect := value.Effects[index+1]
		if effect.Issue == nil || *effect.Issue != issue || !slices.Equal(effect.Fields, wantFields) {
			return false
		}
	}
	return true
}

func projectPageIssueEffectFieldsV2(selected []domain.BrokerProjectPageField) []string {
	fields := []string{"id", "key", "project", "updated"}
	for _, field := range selected {
		fields = append(fields, string(field))
	}
	sort.Strings(fields)
	return fields
}

func normalizeProjectPageOperationAuthorizationRequestV2(value domain.BrokerProjectPageOperationAuthorizationRequestV2) domain.BrokerProjectPageOperationAuthorizationRequestV2 {
	value.QualificationRequest = normalizeProjectPageQualificationRequestV2(value.QualificationRequest)
	value.Issues = append([]domain.BrokerQualifiedJiraProjectPageIssueV2{}, value.Issues...)
	sort.Slice(value.Issues, func(i, j int) bool { return numericIDLessV2(value.Issues[i].ID, value.Issues[j].ID) })
	value.Page.OrderedIssueIDs = append([]string{}, value.Page.OrderedIssueIDs...)
	value.Effects = cloneProjectPageEffectsV2(value.Effects)
	for index := range value.Effects {
		sort.Strings(value.Effects[index].Fields)
	}
	sort.SliceStable(value.Effects, func(i, j int) bool { return projectPageEffectLessV2(value.Effects[i], value.Effects[j]) })
	return value
}

func projectPageEffectLessV2(left, right domain.BrokerProjectPageEffectV2) bool {
	if left.Project != nil {
		return right.Project == nil
	}
	if right.Project != nil {
		return false
	}
	if left.Issue == nil {
		return false
	}
	if right.Issue == nil {
		return true
	}
	return numericIDLessV2(left.Issue.ID, right.Issue.ID)
}

func cloneProjectPageEffectsV2(values []domain.BrokerProjectPageEffectV2) []domain.BrokerProjectPageEffectV2 {
	out := append([]domain.BrokerProjectPageEffectV2(nil), values...)
	for index := range out {
		out[index].Fields = append([]string(nil), out[index].Fields...)
		if out[index].Project != nil {
			copy := *out[index].Project
			out[index].Project = &copy
		}
		if out[index].Issue != nil {
			copy := *out[index].Issue
			out[index].Issue = &copy
		}
	}
	return out
}

func projectPageOperationAuthorizationWireVersionsV2(wire projectPageOperationAuthorizationRequestWireV2) bool {
	return wire.SchemaVersion == ExecutionSchemaVersionV2 && wire.QualificationRequest.SchemaVersion == ExecutionSchemaVersionV2 &&
		wire.QualificationRequest.Admission.SchemaVersion == ExecutionSchemaVersionV2 &&
		wire.QualificationRequest.Admission.Context.SchemaVersion == SchemaVersion &&
		wire.QualificationRequest.AdmissionDecision.SchemaVersion == ExecutionSchemaVersionV2 &&
		wire.QualificationDecision.SchemaVersion == ExecutionSchemaVersionV2
}

func projectPageOperationAuthorizationRequestToWireV2(value domain.BrokerProjectPageOperationAuthorizationRequestV2) projectPageOperationAuthorizationRequestWireV2 {
	issues := make([]qualifiedJiraProjectPageIssueWireV2, len(value.Issues))
	for index, issue := range value.Issues {
		issues[index] = qualifiedJiraProjectPageIssueToWireV2(issue)
	}
	effects := make([]projectPageEffectWireV2, len(value.Effects))
	for index, effect := range value.Effects {
		effects[index] = projectPageEffectToWireV2(effect)
	}
	return projectPageOperationAuthorizationRequestWireV2{
		SchemaVersion: ExecutionSchemaVersionV2, QualificationRequest: projectPageQualificationRequestToWireV2(value.QualificationRequest),
		QualificationDecision: projectPageQualificationDecisionToWireV2(value.QualificationDecision), Project: qualifiedJiraProjectToWireV2(value.Project),
		Issues: issues, Page: projectPageEvidenceToWireV2(value.Page), Effects: effects,
	}
}

func projectPageOperationAuthorizationRequestFromWireV2(wire projectPageOperationAuthorizationRequestWireV2) domain.BrokerProjectPageOperationAuthorizationRequestV2 {
	issues := make([]domain.BrokerQualifiedJiraProjectPageIssueV2, len(wire.Issues))
	for index, issue := range wire.Issues {
		issues[index] = qualifiedJiraProjectPageIssueFromWireV2(issue)
	}
	effects := make([]domain.BrokerProjectPageEffectV2, len(wire.Effects))
	for index, effect := range wire.Effects {
		effects[index] = projectPageEffectFromWireV2(effect)
	}
	return domain.BrokerProjectPageOperationAuthorizationRequestV2{
		QualificationRequest: projectPageQualificationRequestFromWireV2(wire.QualificationRequest), QualificationDecision: projectPageQualificationDecisionFromWireV2(wire.QualificationDecision),
		Project: qualifiedJiraProjectFromWireV2(wire.Project), Issues: issues, Page: projectPageEvidenceFromWireV2(wire.Page), Effects: effects,
	}
}

func qualifiedJiraProjectToWireV2(value domain.BrokerQualifiedJiraProjectV2) qualifiedJiraProjectWireV2 {
	return qualifiedJiraProjectWireV2{ID: value.ID, Key: value.Key, IdentityProjectionSHA256: value.IdentityProjectionSHA256}
}

func qualifiedJiraProjectFromWireV2(wire qualifiedJiraProjectWireV2) domain.BrokerQualifiedJiraProjectV2 {
	return domain.BrokerQualifiedJiraProjectV2{ID: wire.ID, Key: wire.Key, IdentityProjectionSHA256: wire.IdentityProjectionSHA256}
}

func qualifiedJiraProjectPageIssueToWireV2(value domain.BrokerQualifiedJiraProjectPageIssueV2) qualifiedJiraProjectPageIssueWireV2 {
	return qualifiedJiraProjectPageIssueWireV2{ID: value.ID, Key: value.Key, ProjectID: value.ProjectID, ProjectKey: value.ProjectKey, Updated: value.Updated, VersionEvidenceSHA256: value.VersionEvidenceSHA256, ProjectionSHA256: value.ProjectionSHA256}
}

func qualifiedJiraProjectPageIssueFromWireV2(wire qualifiedJiraProjectPageIssueWireV2) domain.BrokerQualifiedJiraProjectPageIssueV2 {
	return domain.BrokerQualifiedJiraProjectPageIssueV2{ID: wire.ID, Key: wire.Key, ProjectID: wire.ProjectID, ProjectKey: wire.ProjectKey, Updated: wire.Updated, VersionEvidenceSHA256: wire.VersionEvidenceSHA256, ProjectionSHA256: wire.ProjectionSHA256}
}

func projectPageEvidenceToWireV2(value domain.BrokerProjectPageEvidenceV2) projectPageEvidenceWireV2 {
	return projectPageEvidenceWireV2{RequestedStartAt: value.RequestedStartAt, RequestedMaxResults: value.RequestedMaxResults, ReturnedStartAt: value.ReturnedStartAt, ReturnedMaxResults: value.ReturnedMaxResults, Total: value.Total, CoordinateExhausted: value.CoordinateExhausted, PaginationStalled: value.PaginationStalled, SelectionComplete: value.SelectionComplete, OrderedIssueIDs: wireStrings(value.OrderedIssueIDs)}
}

func projectPageEvidenceFromWireV2(wire projectPageEvidenceWireV2) domain.BrokerProjectPageEvidenceV2 {
	return domain.BrokerProjectPageEvidenceV2{RequestedStartAt: wire.RequestedStartAt, RequestedMaxResults: wire.RequestedMaxResults, ReturnedStartAt: wire.ReturnedStartAt, ReturnedMaxResults: wire.ReturnedMaxResults, Total: wire.Total, CoordinateExhausted: wire.CoordinateExhausted, PaginationStalled: wire.PaginationStalled, SelectionComplete: wire.SelectionComplete, OrderedIssueIDs: copyStrings(wire.OrderedIssueIDs)}
}

func projectPageEffectToWireV2(value domain.BrokerProjectPageEffectV2) projectPageEffectWireV2 {
	wire := projectPageEffectWireV2{Kind: string(value.Kind), Fields: wireStrings(value.Fields)}
	if value.Project != nil {
		project := qualifiedJiraProjectToWireV2(*value.Project)
		wire.Project = &project
	}
	if value.Issue != nil {
		issue := qualifiedJiraProjectPageIssueToWireV2(*value.Issue)
		wire.Issue = &issue
	}
	return wire
}

func projectPageEffectFromWireV2(wire projectPageEffectWireV2) domain.BrokerProjectPageEffectV2 {
	value := domain.BrokerProjectPageEffectV2{Kind: domain.BrokerEffectKind(wire.Kind), Fields: copyStrings(wire.Fields)}
	if wire.Project != nil {
		project := qualifiedJiraProjectFromWireV2(*wire.Project)
		value.Project = &project
	}
	if wire.Issue != nil {
		issue := qualifiedJiraProjectPageIssueFromWireV2(*wire.Issue)
		value.Issue = &issue
	}
	return value
}

func EncodeProjectPageOperationDecisionV2(value domain.BrokerProjectPageOperationDecisionV2) ([]byte, error) {
	if err := validateProjectPageOperationDecisionV2(value, false); err != nil {
		return nil, err
	}
	wire := projectPageOperationDecisionToWireV2(value)
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV2("operation-decision", wire)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = digest
	return json.Marshal(wire)
}

func DecodeProjectPageOperationDecisionV2(data []byte) (domain.BrokerProjectPageOperationDecisionV2, error) {
	var wire projectPageOperationDecisionWireV2
	if err := strictDecodeExecutionV2(data, 128<<10, &wire); err != nil {
		return domain.BrokerProjectPageOperationDecisionV2{}, err
	}
	if wire.SchemaVersion != ExecutionSchemaVersionV2 {
		return domain.BrokerProjectPageOperationDecisionV2{}, reject(domain.BrokerReasonUnsupported)
	}
	value := projectPageOperationDecisionFromWireV2(wire)
	if validateProjectPageOperationDecisionV2(value, true) != nil || !verifyProjectPageOperationDecisionDigestV2(value) {
		return domain.BrokerProjectPageOperationDecisionV2{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func ValidateProjectPageOperationDecisionV2(value domain.BrokerProjectPageOperationDecisionV2, request domain.BrokerProjectPageOperationAuthorizationRequestV2, nowMillis int64) error {
	request = normalizeProjectPageOperationAuthorizationRequestV2(request)
	admission := request.QualificationRequest.Admission
	requestDigest, requestErr := ProjectPageOperationAuthorizationRequestSHA256V2(request)
	resourcesDigest, resourcesErr := ProjectPageResourcesSHA256V2(request.Project, request.Issues)
	pageDigest, pageErr := ProjectPageEvidenceSHA256V2(request.Page)
	effectsDigest, effectsErr := ProjectPageEffectsSHA256V2(request.Effects)
	if requestErr != nil || resourcesErr != nil || pageErr != nil || effectsErr != nil || validateProjectPageOperationDecisionV2(value, true) != nil ||
		!verifyProjectPageOperationDecisionDigestV2(value) || value.QualificationDecisionSHA256 != request.QualificationDecision.DecisionSHA256 ||
		value.Operation != admission.Operation || value.OperationVersion != admission.OperationVersion || value.ArgumentsSHA256 != admission.ArgumentsSHA256 ||
		value.ResourcesSHA256 != resourcesDigest || value.PageSHA256 != pageDigest || value.EffectsSHA256 != effectsDigest {
		return reject(domain.BrokerReasonMalformed)
	}
	if !decisionCoreMatches(value.BrokerDecisionCore, admission.Context, requestDigest) {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return reject(value.Reason)
	}
	return validateDecisionTime(value.BrokerDecisionCore, admission.Context, nowMillis, admission.DeadlineMillis)
}

func validateProjectPageOperationDecisionV2(value domain.BrokerProjectPageOperationDecisionV2, requireDigest bool) error {
	if validateDecisionCore(value.BrokerDecisionCore) != nil || !validDigest(value.QualificationDecisionSHA256) ||
		value.Operation != domain.BrokerOperationJiraProjectIssuePageRead || value.OperationVersion != ProjectPageOperationVersion ||
		!validDigest(value.ArgumentsSHA256) || !validDigest(value.ResourcesSHA256) || !validDigest(value.PageSHA256) || !validDigest(value.EffectsSHA256) ||
		requireDigest && !validDigest(value.DecisionSHA256) || !requireDigest && value.DecisionSHA256 != "" && !validDigest(value.DecisionSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func projectPageOperationDecisionToWireV2(value domain.BrokerProjectPageOperationDecisionV2) projectPageOperationDecisionWireV2 {
	return projectPageOperationDecisionWireV2{SchemaVersion: ExecutionSchemaVersionV2, Decision: decisionCoreToWire(value.BrokerDecisionCore), QualificationDecisionSHA256: value.QualificationDecisionSHA256, Operation: string(value.Operation), OperationVersion: value.OperationVersion, ArgumentsSHA256: value.ArgumentsSHA256, ResourcesSHA256: value.ResourcesSHA256, PageSHA256: value.PageSHA256, EffectsSHA256: value.EffectsSHA256, DecisionSHA256: value.DecisionSHA256}
}

func projectPageOperationDecisionFromWireV2(wire projectPageOperationDecisionWireV2) domain.BrokerProjectPageOperationDecisionV2 {
	return domain.BrokerProjectPageOperationDecisionV2{BrokerDecisionCore: decisionCoreFromWire(wire.Decision), QualificationDecisionSHA256: wire.QualificationDecisionSHA256, Operation: domain.BrokerOperationID(wire.Operation), OperationVersion: wire.OperationVersion, ArgumentsSHA256: wire.ArgumentsSHA256, ResourcesSHA256: wire.ResourcesSHA256, PageSHA256: wire.PageSHA256, EffectsSHA256: wire.EffectsSHA256, DecisionSHA256: wire.DecisionSHA256}
}

func verifyProjectPageOperationDecisionDigestV2(value domain.BrokerProjectPageOperationDecisionV2) bool {
	wire := projectPageOperationDecisionToWireV2(value)
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV2("operation-decision", wire)
	return err == nil && digest == claimed
}
