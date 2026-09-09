package brokercontract

import (
	"encoding/json"
	"slices"
	"sort"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const executionDigestNamespaceV2 = "atl.broker.execution.v2/"

func strictDecodeExecutionV2(data []byte, maximum int64, target any) error {
	if len(data) == 0 || int64(len(data)) > maximum || strictjson.DecodeExact(data, MaxCanonicalDepth, target) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func digestExecutionV2(kind string, value any) (string, error) {
	return digestValueInNamespace(executionDigestNamespaceV2, kind, value)
}

func EncodeProjectPageRequestV2(value domain.BrokerProjectPageRequestV2) ([]byte, error) {
	value = normalizeProjectPageRequestV2(value)
	if err := validateProjectPageRequestV2(value); err != nil {
		return nil, err
	}
	return marshalEnvelope(projectPageRequestToWireV2(value), MaxProjectPageRequestBytesV2)
}

func DecodeProjectPageRequestV2(data []byte) (domain.BrokerProjectPageRequestV2, error) {
	var wire projectPageRequestWireV2
	if err := strictDecodeExecutionV2(data, MaxProjectPageRequestBytesV2, &wire); err != nil {
		return domain.BrokerProjectPageRequestV2{}, err
	}
	if wire.SchemaVersion != ExecutionSchemaVersionV2 {
		return domain.BrokerProjectPageRequestV2{}, reject(domain.BrokerReasonUnsupported)
	}
	value := projectPageRequestFromWireV2(wire)
	if err := validateProjectPageRequestV2(value); err != nil {
		return domain.BrokerProjectPageRequestV2{}, err
	}
	return value, nil
}

func ProjectPageArgumentsSHA256V2(value domain.BrokerProjectPageRequestV2) (string, error) {
	value = normalizeProjectPageRequestV2(value)
	if err := validateProjectPageRequestV2(value); err != nil {
		return "", err
	}
	return projectPageArgumentsSHA256V2(value.Arguments)
}

func projectPageArgumentsSHA256V2(value domain.BrokerProjectPageArguments) (string, error) {
	value = normalizeProjectPageArguments(value)
	if err := validateProjectPageArguments(value); err != nil {
		return "", err
	}
	return digestExecutionV2("arguments/jira.project.issue_page.read/v1", projectPageArgumentsToWireV2(value))
}

func validateProjectPageRequestV2(value domain.BrokerProjectPageRequestV2) error {
	definition, ok := DefinitionV2(value.Operation, value.OperationVersion)
	if value.SchemaVersion != ExecutionSchemaVersionV2 || !ok {
		return reject(domain.BrokerReasonUnsupported)
	}
	if !validIdentifier(value.RequestID) || value.Features == nil ||
		!validIdentifier(value.Expect.ExecutionID) || !validIdentifier(value.Expect.ExecutionEpoch) ||
		!validIdentifier(value.Expect.AuthorityRevision) || validateProjectPageArguments(value.Arguments) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	if !slices.Equal(value.Features, definition.Definition.RequiredFeatures) {
		return reject(domain.BrokerReasonUnsupported)
	}
	return nil
}

func validateProjectPageArguments(value domain.BrokerProjectPageArguments) error {
	if !validProjectKeyV2(value.ProjectKey) || value.Fields == nil || len(value.Fields) > 2 ||
		value.StartAt < 0 || value.StartAt > domain.BrokerProjectPageMaxStartAt ||
		value.MaxResults <= 0 || value.MaxResults > domain.BrokerProjectPageMaxResults {
		return reject(domain.BrokerReasonMalformed)
	}
	seen := map[domain.BrokerProjectPageField]bool{}
	previous := domain.BrokerProjectPageField("")
	for _, field := range value.Fields {
		if field != domain.BrokerProjectPageFieldDescription && field != domain.BrokerProjectPageFieldSummary ||
			seen[field] || previous != "" && field <= previous {
			return reject(domain.BrokerReasonMalformed)
		}
		seen[field] = true
		previous = field
	}
	return nil
}

func validProjectKeyV2(value string) bool {
	return len(value) <= 32 && domain.ValidJiraIssueKey(value+"-1")
}

func normalizeProjectPageRequestV2(value domain.BrokerProjectPageRequestV2) domain.BrokerProjectPageRequestV2 {
	value.Features = append([]string(nil), value.Features...)
	value.Arguments = normalizeProjectPageArguments(value.Arguments)
	return value
}

func normalizeProjectPageArguments(value domain.BrokerProjectPageArguments) domain.BrokerProjectPageArguments {
	value.Fields = append([]domain.BrokerProjectPageField{}, value.Fields...)
	slices.Sort(value.Fields)
	return value
}

func projectPageRequestToWireV2(value domain.BrokerProjectPageRequestV2) projectPageRequestWireV2 {
	return projectPageRequestWireV2{
		SchemaVersion: value.SchemaVersion, Operation: string(value.Operation), OperationVersion: value.OperationVersion,
		RequestID: value.RequestID, Features: wireStrings(value.Features),
		Expect:    expectationsWire{ExecutionID: value.Expect.ExecutionID, ExecutionEpoch: value.Expect.ExecutionEpoch, AuthorityRevision: value.Expect.AuthorityRevision},
		Arguments: projectPageArgumentsToWireV2(value.Arguments),
	}
}

func projectPageRequestFromWireV2(wire projectPageRequestWireV2) domain.BrokerProjectPageRequestV2 {
	return domain.BrokerProjectPageRequestV2{
		SchemaVersion: wire.SchemaVersion, Operation: domain.BrokerOperationID(wire.Operation), OperationVersion: wire.OperationVersion,
		RequestID: wire.RequestID, Features: copyStrings(wire.Features),
		Expect:    domain.BrokerRequestExpectations{ExecutionID: wire.Expect.ExecutionID, ExecutionEpoch: wire.Expect.ExecutionEpoch, AuthorityRevision: wire.Expect.AuthorityRevision},
		Arguments: projectPageArgumentsFromWireV2(wire.Arguments),
	}
}

func projectPageArgumentsToWireV2(value domain.BrokerProjectPageArguments) projectPageArgumentsWireV2 {
	fields := make([]string, len(value.Fields))
	for index, field := range value.Fields {
		fields[index] = string(field)
	}
	return projectPageArgumentsWireV2{ProjectKey: value.ProjectKey, Fields: fields, StartAt: value.StartAt, MaxResults: value.MaxResults}
}

func projectPageArgumentsFromWireV2(wire projectPageArgumentsWireV2) domain.BrokerProjectPageArguments {
	fields := make([]domain.BrokerProjectPageField, len(wire.Fields))
	for index, field := range wire.Fields {
		fields[index] = domain.BrokerProjectPageField(field)
	}
	return domain.BrokerProjectPageArguments{ProjectKey: wire.ProjectKey, Fields: fields, StartAt: wire.StartAt, MaxResults: wire.MaxResults}
}

func EncodeProjectPageAdmissionRequestV2(value domain.BrokerProjectPageAdmissionRequestV2) ([]byte, error) {
	value = normalizeProjectPageAdmissionRequestV2(value)
	if err := validateProjectPageAdmissionRequestV2(value); err != nil {
		return nil, err
	}
	return marshalEnvelope(projectPageAdmissionRequestToWireV2(value), MaxEnvelopeBytes)
}

func DecodeProjectPageAdmissionRequestV2(data []byte) (domain.BrokerProjectPageAdmissionRequestV2, error) {
	var wire projectPageAdmissionRequestWireV2
	if err := strictDecodeExecutionV2(data, MaxEnvelopeBytes, &wire); err != nil {
		return domain.BrokerProjectPageAdmissionRequestV2{}, err
	}
	if wire.SchemaVersion != ExecutionSchemaVersionV2 || wire.Context.SchemaVersion != SchemaVersion {
		return domain.BrokerProjectPageAdmissionRequestV2{}, reject(domain.BrokerReasonUnsupported)
	}
	value := projectPageAdmissionRequestFromWireV2(wire)
	if err := validateProjectPageAdmissionRequestV2(value); err != nil {
		return domain.BrokerProjectPageAdmissionRequestV2{}, err
	}
	return value, nil
}

func ProjectPageAdmissionRequestSHA256V2(value domain.BrokerProjectPageAdmissionRequestV2) (string, error) {
	value = normalizeProjectPageAdmissionRequestV2(value)
	if err := validateProjectPageAdmissionRequestV2(value); err != nil {
		return "", err
	}
	return digestExecutionV2("admission-request", projectPageAdmissionRequestToWireV2(value))
}

func validateProjectPageAdmissionRequestV2(value domain.BrokerProjectPageAdmissionRequestV2) error {
	definition, ok := DefinitionV2(value.Operation, value.OperationVersion)
	argumentsDigest, argumentsErr := projectPageArgumentsSHA256V2(value.Arguments)
	if !ok || validateContext(value.Context) != nil || definition.Definition.BackendService != value.Context.Backend.Service ||
		!validIdentifier(value.RequestID) || value.Features == nil || !slices.Equal(value.Features, definition.Definition.RequiredFeatures) ||
		argumentsErr != nil || value.ArgumentsSHA256 != argumentsDigest ||
		value.DeadlineMillis <= value.Context.ExecutionNotBeforeMillis || value.DeadlineMillis > value.Context.ExecutionExpiresMillis ||
		value.DeadlineMillis > value.Context.GrantExpiresMillis || value.DeadlineMillis > value.Context.CredentialExpiresMillis ||
		definition.Definition.Limits.MaxOperationMillis > domain.BrokerMaxOperationMillis {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func normalizeProjectPageAdmissionRequestV2(value domain.BrokerProjectPageAdmissionRequestV2) domain.BrokerProjectPageAdmissionRequestV2 {
	value.Features = append([]string(nil), value.Features...)
	value.Arguments = normalizeProjectPageArguments(value.Arguments)
	return value
}

func projectPageAdmissionRequestToWireV2(value domain.BrokerProjectPageAdmissionRequestV2) projectPageAdmissionRequestWireV2 {
	return projectPageAdmissionRequestWireV2{
		SchemaVersion: ExecutionSchemaVersionV2, Context: contextToWire(value.Context), Operation: string(value.Operation),
		OperationVersion: value.OperationVersion, RequestID: value.RequestID, Features: wireStrings(value.Features),
		Arguments: projectPageArgumentsToWireV2(value.Arguments), ArgumentsSHA256: value.ArgumentsSHA256, DeadlineMillis: value.DeadlineMillis,
	}
}

func projectPageAdmissionRequestFromWireV2(wire projectPageAdmissionRequestWireV2) domain.BrokerProjectPageAdmissionRequestV2 {
	return domain.BrokerProjectPageAdmissionRequestV2{
		Context: contextFromWire(wire.Context), Operation: domain.BrokerOperationID(wire.Operation), OperationVersion: wire.OperationVersion,
		RequestID: wire.RequestID, Features: copyStrings(wire.Features), Arguments: projectPageArgumentsFromWireV2(wire.Arguments),
		ArgumentsSHA256: wire.ArgumentsSHA256, DeadlineMillis: wire.DeadlineMillis,
	}
}

func MatchProjectPageRequestContextV2(request domain.BrokerProjectPageRequestV2, verified domain.BrokerVerifiedContext, nowMillis, deadlineMillis int64) error {
	request = normalizeProjectPageRequestV2(request)
	if validateProjectPageRequestV2(request) != nil || validateContext(verified) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	definition, _ := DefinitionV2(request.Operation, request.OperationVersion)
	if definition.Definition.BackendService != verified.Backend.Service {
		return reject(domain.BrokerReasonUnsupported)
	}
	if request.Expect.ExecutionID != verified.ExecutionID || request.Expect.ExecutionEpoch != verified.ExecutionEpoch {
		return reject(domain.BrokerReasonStaleExecution)
	}
	if request.Expect.AuthorityRevision != verified.AuthorityRevision {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	if nowMillis < verified.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || nowMillis >= verified.ExecutionExpiresMillis {
		return reject(domain.BrokerReasonStaleExecution)
	}
	if nowMillis >= verified.CredentialExpiresMillis {
		return reject(domain.BrokerReasonCredentialExpired)
	}
	if nowMillis >= verified.GrantExpiresMillis {
		return reject(domain.BrokerReasonGrantExpired)
	}
	if deadlineMillis <= nowMillis || deadlineMillis-nowMillis > domain.BrokerMaxOperationMillis ||
		deadlineMillis > verified.ExecutionExpiresMillis || deadlineMillis > verified.GrantExpiresMillis || deadlineMillis > verified.CredentialExpiresMillis {
		return reject(domain.BrokerReasonDecisionExpired)
	}
	return nil
}

func EncodeProjectPageAdmissionDecisionV2(value domain.BrokerProjectPageAdmissionDecisionV2) ([]byte, error) {
	if err := validateProjectPageAdmissionDecisionV2(value, false); err != nil {
		return nil, err
	}
	wire := projectPageAdmissionDecisionToWireV2(value)
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV2("admission-decision", wire)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = digest
	return json.Marshal(wire)
}

func DecodeProjectPageAdmissionDecisionV2(data []byte) (domain.BrokerProjectPageAdmissionDecisionV2, error) {
	var wire projectPageAdmissionDecisionWireV2
	if err := strictDecodeExecutionV2(data, 64<<10, &wire); err != nil {
		return domain.BrokerProjectPageAdmissionDecisionV2{}, err
	}
	if wire.SchemaVersion != ExecutionSchemaVersionV2 {
		return domain.BrokerProjectPageAdmissionDecisionV2{}, reject(domain.BrokerReasonUnsupported)
	}
	value := projectPageAdmissionDecisionFromWireV2(wire)
	if validateProjectPageAdmissionDecisionV2(value, true) != nil || !verifyProjectPageAdmissionDecisionDigestV2(value) {
		return domain.BrokerProjectPageAdmissionDecisionV2{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func ValidateProjectPageAdmissionDecisionV2(value domain.BrokerProjectPageAdmissionDecisionV2, request domain.BrokerProjectPageAdmissionRequestV2, nowMillis int64) error {
	requestDigest, err := ProjectPageAdmissionRequestSHA256V2(request)
	if err != nil || validateProjectPageAdmissionDecisionV2(value, true) != nil || !verifyProjectPageAdmissionDecisionDigestV2(value) {
		return reject(domain.BrokerReasonMalformed)
	}
	if !decisionCoreMatches(value.BrokerDecisionCore, request.Context, requestDigest) {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return reject(value.Reason)
	}
	return validateDecisionTime(value.BrokerDecisionCore, request.Context, nowMillis, request.DeadlineMillis)
}

func validateProjectPageAdmissionDecisionV2(value domain.BrokerProjectPageAdmissionDecisionV2, requireDigest bool) error {
	if validateDecisionCore(value.BrokerDecisionCore) != nil ||
		requireDigest && !validDigest(value.DecisionSHA256) || !requireDigest && value.DecisionSHA256 != "" && !validDigest(value.DecisionSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func projectPageAdmissionDecisionToWireV2(value domain.BrokerProjectPageAdmissionDecisionV2) projectPageAdmissionDecisionWireV2 {
	return projectPageAdmissionDecisionWireV2{SchemaVersion: ExecutionSchemaVersionV2, Decision: decisionCoreToWire(value.BrokerDecisionCore), DecisionSHA256: value.DecisionSHA256}
}

func projectPageAdmissionDecisionFromWireV2(wire projectPageAdmissionDecisionWireV2) domain.BrokerProjectPageAdmissionDecisionV2 {
	return domain.BrokerProjectPageAdmissionDecisionV2{BrokerDecisionCore: decisionCoreFromWire(wire.Decision), DecisionSHA256: wire.DecisionSHA256}
}

func verifyProjectPageAdmissionDecisionDigestV2(value domain.BrokerProjectPageAdmissionDecisionV2) bool {
	wire := projectPageAdmissionDecisionToWireV2(value)
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV2("admission-decision", wire)
	return err == nil && digest == claimed
}

func EncodeProjectPageQualificationRequestV2(value domain.BrokerProjectPageQualificationRequestV2) ([]byte, error) {
	value = normalizeProjectPageQualificationRequestV2(value)
	if err := validateProjectPageQualificationRequestV2(value); err != nil {
		return nil, err
	}
	return marshalEnvelope(projectPageQualificationRequestToWireV2(value), MaxEnvelopeBytes)
}

func DecodeProjectPageQualificationRequestV2(data []byte) (domain.BrokerProjectPageQualificationRequestV2, error) {
	var wire projectPageQualificationRequestWireV2
	if err := strictDecodeExecutionV2(data, MaxEnvelopeBytes, &wire); err != nil {
		return domain.BrokerProjectPageQualificationRequestV2{}, err
	}
	if wire.SchemaVersion != ExecutionSchemaVersionV2 || wire.Admission.SchemaVersion != ExecutionSchemaVersionV2 ||
		wire.Admission.Context.SchemaVersion != SchemaVersion || wire.AdmissionDecision.SchemaVersion != ExecutionSchemaVersionV2 {
		return domain.BrokerProjectPageQualificationRequestV2{}, reject(domain.BrokerReasonUnsupported)
	}
	value := projectPageQualificationRequestFromWireV2(wire)
	if err := validateProjectPageQualificationRequestV2(value); err != nil {
		return domain.BrokerProjectPageQualificationRequestV2{}, err
	}
	return value, nil
}

func ProjectPageQualificationRequestSHA256V2(value domain.BrokerProjectPageQualificationRequestV2) (string, error) {
	value = normalizeProjectPageQualificationRequestV2(value)
	if err := validateProjectPageQualificationRequestV2(value); err != nil {
		return "", err
	}
	return digestExecutionV2("qualification-request", projectPageQualificationRequestToWireV2(value))
}

func ProjectPageQualificationPlanSHA256V2(value domain.BrokerProjectPageQualificationPlanV2) (string, error) {
	if err := validateProjectPageQualificationPlanV2(value); err != nil {
		return "", err
	}
	return digestExecutionV2("qualification-plan", projectPageQualificationPlanToWireV2(value))
}

func validateProjectPageQualificationRequestV2(value domain.BrokerProjectPageQualificationRequestV2) error {
	if validateProjectPageAdmissionRequestV2(value.Admission) != nil ||
		ValidateProjectPageAdmissionDecisionV2(value.AdmissionDecision, value.Admission, value.AdmissionDecision.IssuedAtMillis) != nil ||
		value.Plan.SelectorSHA256 != value.Admission.ArgumentsSHA256 || validateProjectPageQualificationPlanV2(value.Plan) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateProjectPageQualificationPlanV2(value domain.BrokerProjectPageQualificationPlanV2) error {
	definition, _ := DefinitionV2(domain.BrokerOperationJiraProjectIssuePageRead, ProjectPageOperationVersion)
	if !validDigest(value.SelectorSHA256) || value.Limits != definition.Definition.Limits.Qualification || len(value.Steps) != len(definition.QualificationSteps) {
		return reject(domain.BrokerReasonMalformed)
	}
	for index, step := range value.Steps {
		expected := definition.QualificationSteps[index]
		if step.Kind != expected.Kind || step.Limits != expected.Limits || !slices.Equal(step.MetadataFields, expected.MetadataFields) || !sort.StringsAreSorted(step.MetadataFields) {
			return reject(domain.BrokerReasonMalformed)
		}
	}
	return nil
}

func normalizeProjectPageQualificationRequestV2(value domain.BrokerProjectPageQualificationRequestV2) domain.BrokerProjectPageQualificationRequestV2 {
	value.Admission = normalizeProjectPageAdmissionRequestV2(value.Admission)
	value.Plan.Steps = append([]domain.BrokerProjectPageQualificationStepV2(nil), value.Plan.Steps...)
	for index := range value.Plan.Steps {
		value.Plan.Steps[index].MetadataFields = append([]string(nil), value.Plan.Steps[index].MetadataFields...)
	}
	return value
}

func projectPageQualificationPlanToWireV2(value domain.BrokerProjectPageQualificationPlanV2) projectPageQualificationPlanWireV2 {
	steps := make([]projectPageQualificationStepWireV2, len(value.Steps))
	for index, step := range value.Steps {
		steps[index] = projectPageQualificationStepWireV2{Kind: string(step.Kind), MetadataFields: wireStrings(step.MetadataFields), Limits: phaseLimitsToWire(step.Limits)}
	}
	return projectPageQualificationPlanWireV2{SelectorSHA256: value.SelectorSHA256, Steps: steps, Limits: phaseLimitsToWire(value.Limits)}
}

func projectPageQualificationPlanFromWireV2(wire projectPageQualificationPlanWireV2) domain.BrokerProjectPageQualificationPlanV2 {
	steps := make([]domain.BrokerProjectPageQualificationStepV2, len(wire.Steps))
	for index, step := range wire.Steps {
		steps[index] = domain.BrokerProjectPageQualificationStepV2{Kind: domain.BrokerProjectPageQualificationStepKind(step.Kind), MetadataFields: copyStrings(step.MetadataFields), Limits: phaseLimitsFromWire(step.Limits)}
	}
	return domain.BrokerProjectPageQualificationPlanV2{SelectorSHA256: wire.SelectorSHA256, Steps: steps, Limits: phaseLimitsFromWire(wire.Limits)}
}

func projectPageQualificationRequestToWireV2(value domain.BrokerProjectPageQualificationRequestV2) projectPageQualificationRequestWireV2 {
	return projectPageQualificationRequestWireV2{SchemaVersion: ExecutionSchemaVersionV2, Admission: projectPageAdmissionRequestToWireV2(value.Admission), AdmissionDecision: projectPageAdmissionDecisionToWireV2(value.AdmissionDecision), Plan: projectPageQualificationPlanToWireV2(value.Plan)}
}

func projectPageQualificationRequestFromWireV2(wire projectPageQualificationRequestWireV2) domain.BrokerProjectPageQualificationRequestV2 {
	return domain.BrokerProjectPageQualificationRequestV2{Admission: projectPageAdmissionRequestFromWireV2(wire.Admission), AdmissionDecision: projectPageAdmissionDecisionFromWireV2(wire.AdmissionDecision), Plan: projectPageQualificationPlanFromWireV2(wire.Plan)}
}

func EncodeProjectPageQualificationDecisionV2(value domain.BrokerProjectPageQualificationDecisionV2) ([]byte, error) {
	if err := validateProjectPageQualificationDecisionV2(value, false); err != nil {
		return nil, err
	}
	wire := projectPageQualificationDecisionToWireV2(value)
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV2("qualification-decision", wire)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = digest
	return json.Marshal(wire)
}

func DecodeProjectPageQualificationDecisionV2(data []byte) (domain.BrokerProjectPageQualificationDecisionV2, error) {
	var wire projectPageQualificationDecisionWireV2
	if err := strictDecodeExecutionV2(data, 128<<10, &wire); err != nil {
		return domain.BrokerProjectPageQualificationDecisionV2{}, err
	}
	if wire.SchemaVersion != ExecutionSchemaVersionV2 {
		return domain.BrokerProjectPageQualificationDecisionV2{}, reject(domain.BrokerReasonUnsupported)
	}
	value := projectPageQualificationDecisionFromWireV2(wire)
	if validateProjectPageQualificationDecisionV2(value, true) != nil || !verifyProjectPageQualificationDecisionDigestV2(value) {
		return domain.BrokerProjectPageQualificationDecisionV2{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func ValidateProjectPageQualificationDecisionV2(value domain.BrokerProjectPageQualificationDecisionV2, request domain.BrokerProjectPageQualificationRequestV2, nowMillis int64) error {
	requestDigest, requestErr := ProjectPageQualificationRequestSHA256V2(request)
	admissionDigest, admissionErr := ProjectPageAdmissionRequestSHA256V2(request.Admission)
	planDigest, planErr := ProjectPageQualificationPlanSHA256V2(request.Plan)
	if requestErr != nil || admissionErr != nil || planErr != nil || validateProjectPageQualificationDecisionV2(value, true) != nil ||
		!verifyProjectPageQualificationDecisionDigestV2(value) || value.AdmissionRequestSHA256 != admissionDigest ||
		value.AdmissionDecisionSHA256 != request.AdmissionDecision.DecisionSHA256 || value.PlanSHA256 != planDigest {
		return reject(domain.BrokerReasonMalformed)
	}
	if !decisionCoreMatches(value.BrokerDecisionCore, request.Admission.Context, requestDigest) {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return reject(value.Reason)
	}
	return validateDecisionTime(value.BrokerDecisionCore, request.Admission.Context, nowMillis, request.Admission.DeadlineMillis)
}

func validateProjectPageQualificationDecisionV2(value domain.BrokerProjectPageQualificationDecisionV2, requireDigest bool) error {
	if validateDecisionCore(value.BrokerDecisionCore) != nil || !validDigest(value.AdmissionRequestSHA256) ||
		!validDigest(value.AdmissionDecisionSHA256) || !validDigest(value.PlanSHA256) ||
		requireDigest && !validDigest(value.DecisionSHA256) || !requireDigest && value.DecisionSHA256 != "" && !validDigest(value.DecisionSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func projectPageQualificationDecisionToWireV2(value domain.BrokerProjectPageQualificationDecisionV2) projectPageQualificationDecisionWireV2 {
	return projectPageQualificationDecisionWireV2{SchemaVersion: ExecutionSchemaVersionV2, Decision: decisionCoreToWire(value.BrokerDecisionCore), AdmissionRequestSHA256: value.AdmissionRequestSHA256, AdmissionDecisionSHA256: value.AdmissionDecisionSHA256, PlanSHA256: value.PlanSHA256, DecisionSHA256: value.DecisionSHA256}
}

func projectPageQualificationDecisionFromWireV2(wire projectPageQualificationDecisionWireV2) domain.BrokerProjectPageQualificationDecisionV2 {
	return domain.BrokerProjectPageQualificationDecisionV2{BrokerDecisionCore: decisionCoreFromWire(wire.Decision), AdmissionRequestSHA256: wire.AdmissionRequestSHA256, AdmissionDecisionSHA256: wire.AdmissionDecisionSHA256, PlanSHA256: wire.PlanSHA256, DecisionSHA256: wire.DecisionSHA256}
}

func verifyProjectPageQualificationDecisionDigestV2(value domain.BrokerProjectPageQualificationDecisionV2) bool {
	wire := projectPageQualificationDecisionToWireV2(value)
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV2("qualification-decision", wire)
	return err == nil && digest == claimed
}

func NewProjectPageQualificationPlanV2(selectorSHA256 string) (domain.BrokerProjectPageQualificationPlanV2, error) {
	if !validDigest(selectorSHA256) {
		return domain.BrokerProjectPageQualificationPlanV2{}, reject(domain.BrokerReasonMalformed)
	}
	definition, _ := DefinitionV2(domain.BrokerOperationJiraProjectIssuePageRead, ProjectPageOperationVersion)
	steps := make([]domain.BrokerProjectPageQualificationStepV2, len(definition.QualificationSteps))
	for index, step := range definition.QualificationSteps {
		steps[index] = domain.BrokerProjectPageQualificationStepV2{Kind: step.Kind, MetadataFields: copyStrings(step.MetadataFields), Limits: step.Limits}
	}
	return domain.BrokerProjectPageQualificationPlanV2{SelectorSHA256: selectorSHA256, Steps: steps, Limits: definition.Definition.Limits.Qualification}, nil
}
