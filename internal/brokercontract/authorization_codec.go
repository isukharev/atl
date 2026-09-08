package brokercontract

import (
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

func EncodeAdmissionRequestV1(value domain.BrokerAdmissionRequest) ([]byte, error) {
	if err := validateAdmissionRequest(value); err != nil {
		return nil, err
	}
	return marshalEnvelope(admissionRequestToWire(value), MaxEnvelopeBytes)
}

func DecodeAdmissionRequestV1(data []byte) (domain.BrokerAdmissionRequest, error) {
	var wire admissionRequestWire
	if err := strictDecode(data, MaxEnvelopeBytes, &wire); err != nil {
		return domain.BrokerAdmissionRequest{}, err
	}
	value, convertErr := admissionRequestFromWire(wire)
	if wire.SchemaVersion != SchemaVersion || convertErr != nil || validateAdmissionRequest(value) != nil {
		return domain.BrokerAdmissionRequest{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func validateAdmissionRequest(value domain.BrokerAdmissionRequest) error {
	definition, ok := Definition(value.Operation, value.OperationVersion)
	if !ok {
		return reject(domain.BrokerReasonUnsupported)
	}
	argumentsDigest, argumentsErr := argumentsSHA256(value.Operation, value.Arguments)
	if validateContext(value.Context) != nil || !operationBackendMatches(definition, value.Context) || !validIdentifier(value.RequestID) || value.Features == nil || !slices.Equal(value.Features, definition.RequiredFeatures) || argumentsErr != nil || value.ArgumentsSHA256 != argumentsDigest ||
		value.DeadlineMillis <= value.Context.ExecutionNotBeforeMillis || value.DeadlineMillis > value.Context.ExecutionExpiresMillis ||
		value.DeadlineMillis > value.Context.GrantExpiresMillis || value.DeadlineMillis > value.Context.CredentialExpiresMillis ||
		definition.Limits.MaxOperationMillis > domain.BrokerMaxOperationMillis {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func admissionRequestToWire(value domain.BrokerAdmissionRequest) admissionRequestWire {
	arguments, _ := encodeArguments(value.Operation, value.Arguments)
	return admissionRequestWire{SchemaVersion: SchemaVersion, Context: contextToWire(value.Context), Operation: string(value.Operation), OperationVersion: value.OperationVersion, RequestID: value.RequestID, Features: wireStrings(value.Features), Arguments: arguments, ArgumentsSHA256: value.ArgumentsSHA256, DeadlineMillis: value.DeadlineMillis}
}

func admissionRequestFromWire(wire admissionRequestWire) (domain.BrokerAdmissionRequest, error) {
	operation := domain.BrokerOperationID(wire.Operation)
	arguments, err := decodeArguments(operation, wire.Arguments)
	if err != nil {
		return domain.BrokerAdmissionRequest{}, err
	}
	return domain.BrokerAdmissionRequest{Context: contextFromWire(wire.Context), Operation: operation, OperationVersion: wire.OperationVersion, RequestID: wire.RequestID, Features: copyStrings(wire.Features), Arguments: arguments, ArgumentsSHA256: wire.ArgumentsSHA256, DeadlineMillis: wire.DeadlineMillis}, nil
}

func AdmissionRequestSHA256(value domain.BrokerAdmissionRequest) (string, error) {
	if err := validateAdmissionRequest(value); err != nil {
		return "", err
	}
	return digestValue("admission-request", admissionRequestToWire(value))
}

func EncodeQualificationRequestV1(value domain.BrokerQualificationRequest) ([]byte, error) {
	if err := validateQualificationRequest(value); err != nil {
		return nil, err
	}
	return marshalEnvelope(qualificationRequestToWire(value), MaxEnvelopeBytes)
}

func DecodeQualificationRequestV1(data []byte) (domain.BrokerQualificationRequest, error) {
	var wire qualificationRequestWire
	if err := strictDecode(data, MaxEnvelopeBytes, &wire); err != nil {
		return domain.BrokerQualificationRequest{}, err
	}
	value, convertErr := qualificationRequestFromWire(wire)
	if wire.SchemaVersion != SchemaVersion || convertErr != nil || validateQualificationRequest(value) != nil {
		return domain.BrokerQualificationRequest{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func validateQualificationRequest(value domain.BrokerQualificationRequest) error {
	if validateAdmissionRequest(value.Admission) != nil || ValidateAdmissionDecisionForV1(value.AdmissionDecision, value.Admission, value.AdmissionDecision.IssuedAtMillis) != nil ||
		!validDigest(value.Plan.SelectorSHA256) || value.Plan.SelectorSHA256 != value.Admission.ArgumentsSHA256 || len(value.Plan.MetadataFields) == 0 || len(value.Plan.MetadataFields) > MaxMetadataFields {
		return reject(domain.BrokerReasonMalformed)
	}
	definition, _ := Definition(value.Admission.Operation, value.Admission.OperationVersion)
	if !phaseLimitsWithin(value.Plan.Limits, definition.Limits.Qualification) || len(value.Plan.MetadataFields) > len(definition.QualificationFields) {
		return reject(domain.BrokerReasonMalformed)
	}
	allowedMetadata := make(map[string]bool, len(definition.QualificationFields))
	for _, field := range definition.QualificationFields {
		allowedMetadata[field] = true
	}
	seen := map[string]bool{}
	for _, field := range value.Plan.MetadataFields {
		if !allowedMetadata[field] || seen[field] {
			return reject(domain.BrokerReasonMalformed)
		}
		seen[field] = true
	}
	if !sort.StringsAreSorted(value.Plan.MetadataFields) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func qualificationRequestToWire(value domain.BrokerQualificationRequest) qualificationRequestWire {
	return qualificationRequestWire{SchemaVersion: SchemaVersion, Admission: admissionRequestToWire(value.Admission), AdmissionDecision: admissionDecisionToWire(value.AdmissionDecision), Plan: qualificationPlanWire{SelectorSHA256: value.Plan.SelectorSHA256, MetadataFields: wireStrings(value.Plan.MetadataFields), Limits: phaseLimitsToWire(value.Plan.Limits)}}
}

func qualificationRequestFromWire(wire qualificationRequestWire) (domain.BrokerQualificationRequest, error) {
	admission, err := admissionRequestFromWire(wire.Admission)
	if err != nil {
		return domain.BrokerQualificationRequest{}, err
	}
	return domain.BrokerQualificationRequest{Admission: admission, AdmissionDecision: admissionDecisionFromWire(wire.AdmissionDecision), Plan: domain.BrokerQualificationPlan{SelectorSHA256: wire.Plan.SelectorSHA256, MetadataFields: copyStrings(wire.Plan.MetadataFields), Limits: phaseLimitsFromWire(wire.Plan.Limits)}}, nil
}

func QualificationRequestSHA256(value domain.BrokerQualificationRequest) (string, error) {
	if err := validateQualificationRequest(value); err != nil {
		return "", err
	}
	return digestValue("qualification-request", qualificationRequestToWire(value))
}

func QualificationPlanSHA256(value domain.BrokerQualificationPlan) (string, error) {
	if !validDigest(value.SelectorSHA256) || len(value.MetadataFields) == 0 || len(value.MetadataFields) > MaxMetadataFields || !sort.StringsAreSorted(value.MetadataFields) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestValue("qualification-plan", qualificationPlanWire{SelectorSHA256: value.SelectorSHA256, MetadataFields: wireStrings(value.MetadataFields), Limits: phaseLimitsToWire(value.Limits)})
}

func phaseLimitsWithin(value, maximum domain.BrokerPhaseLimits) bool {
	return value.MaxRequests >= 0 && value.MaxRequests <= maximum.MaxRequests && value.MaxResponseBytes >= 0 && value.MaxResponseBytes <= maximum.MaxResponseBytes &&
		(value.MaxRequests == 0) == (value.MaxResponseBytes == 0)
}

func EncodeQualificationDecisionV1(value domain.BrokerQualificationDecision) ([]byte, error) {
	if err := validateQualificationDecision(value, false); err != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	wire := qualificationDecisionToWire(value)
	wire.DecisionSHA256 = ""
	digest, err := digestValue("qualification-decision", wire)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = digest
	return json.Marshal(wire)
}

func DecodeQualificationDecisionV1(data []byte) (domain.BrokerQualificationDecision, error) {
	var wire qualificationDecisionWire
	if err := strictDecode(data, 128<<10, &wire); err != nil {
		return domain.BrokerQualificationDecision{}, err
	}
	value := qualificationDecisionFromWire(wire)
	if err := validateQualificationDecision(value, true); err != nil || !verifyQualificationDecisionDigest(value) {
		return domain.BrokerQualificationDecision{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func validateQualificationDecision(value domain.BrokerQualificationDecision, requireDigest bool) error {
	if validateDecisionCore(value.BrokerDecisionCore) != nil || !validDigest(value.AdmissionRequestSHA256) || !validDigest(value.AdmissionDecisionSHA256) || !validDigest(value.PlanSHA256) ||
		requireDigest && !validDigest(value.DecisionSHA256) || !requireDigest && value.DecisionSHA256 != "" && !validDigest(value.DecisionSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func qualificationDecisionToWire(value domain.BrokerQualificationDecision) qualificationDecisionWire {
	return qualificationDecisionWire{SchemaVersion: SchemaVersion, Decision: decisionCoreToWire(value.BrokerDecisionCore), AdmissionRequestSHA256: value.AdmissionRequestSHA256, AdmissionDecisionSHA256: value.AdmissionDecisionSHA256, PlanSHA256: value.PlanSHA256, DecisionSHA256: value.DecisionSHA256}
}

func qualificationDecisionFromWire(wire qualificationDecisionWire) domain.BrokerQualificationDecision {
	return domain.BrokerQualificationDecision{BrokerDecisionCore: decisionCoreFromWire(wire.Decision), AdmissionRequestSHA256: wire.AdmissionRequestSHA256, AdmissionDecisionSHA256: wire.AdmissionDecisionSHA256, PlanSHA256: wire.PlanSHA256, DecisionSHA256: wire.DecisionSHA256}
}

func verifyQualificationDecisionDigest(value domain.BrokerQualificationDecision) bool {
	wire := qualificationDecisionToWire(value)
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, err := digestValue("qualification-decision", wire)
	return err == nil && digest == claimed
}

func EncodeOperationAuthorizationRequestV1(value domain.BrokerOperationAuthorizationRequest) ([]byte, error) {
	if err := validateOperationAuthorizationRequest(value); err != nil {
		return nil, err
	}
	return marshalEnvelope(operationAuthorizationRequestToWire(value), MaxEnvelopeBytes)
}

func DecodeOperationAuthorizationRequestV1(data []byte) (domain.BrokerOperationAuthorizationRequest, error) {
	var wire operationAuthorizationRequestWire
	if err := strictDecode(data, MaxEnvelopeBytes, &wire); err != nil {
		return domain.BrokerOperationAuthorizationRequest{}, err
	}
	value, convertErr := operationAuthorizationRequestFromWire(wire)
	if wire.SchemaVersion != SchemaVersion || convertErr != nil || validateOperationAuthorizationRequest(value) != nil {
		return domain.BrokerOperationAuthorizationRequest{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func validateOperationAuthorizationRequest(value domain.BrokerOperationAuthorizationRequest) error {
	if validateQualificationRequest(value.QualificationRequest) != nil ||
		ValidateQualificationDecisionForV1(value.QualificationDecision, value.QualificationRequest, value.QualificationDecision.IssuedAtMillis) != nil ||
		len(value.QualifiedResources) != 1 {
		return reject(domain.BrokerReasonMalformed)
	}
	admission := value.QualificationRequest.Admission
	definition, _ := Definition(admission.Operation, admission.OperationVersion)
	resource := value.QualifiedResources[0]
	if !validQualifiedResource(resource) || resource.Kind != definition.Effects[0].ResourceKind || !resourceMatchesAdmission(resource, admission) {
		return reject(domain.BrokerReasonMalformed)
	}
	if len(value.Effects) != len(definition.Effects) {
		return reject(domain.BrokerReasonMalformed)
	}
	for index, effect := range value.Effects {
		expected := definition.Effects[index]
		if !validEffect(effect) || effect.Kind != expected.Kind || !sameQualifiedResource(effect.Resource, resource) ||
			!effectFieldsWithinDefinition(effect.Fields, expected.Fields) || !slices.Equal(effect.Fields, expectedEffectFields(admission, effect.Kind)) {
			return reject(domain.BrokerReasonMalformed)
		}
	}
	return nil
}

func resourceMatchesAdmission(resource domain.BrokerQualifiedResource, admission domain.BrokerAdmissionRequest) bool {
	switch admission.Operation {
	case domain.BrokerOperationJiraIssueRead:
		return admission.Arguments.JiraIssueRead != nil && resource.Key == admission.Arguments.JiraIssueRead.IssueKey
	case domain.BrokerOperationConfluencePageRead:
		return admission.Arguments.ConfluencePageRead != nil && resource.ImmutableID == admission.Arguments.ConfluencePageRead.PageID
	case domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraCommentApply:
		return admission.Arguments.JiraComment != nil && resource.Key == admission.Arguments.JiraComment.IssueKey
	case domain.BrokerOperationOutcomeLookup:
		return admission.Arguments.Outcome != nil && resource.ImmutableID == admission.Arguments.Outcome.OperationTicket
	default:
		return false
	}
}

func expectedEffectFields(admission domain.BrokerAdmissionRequest, kind domain.BrokerEffectKind) []string {
	switch admission.Operation {
	case domain.BrokerOperationJiraIssueRead:
		fields := make([]string, len(admission.Arguments.JiraIssueRead.Fields))
		for index, field := range admission.Arguments.JiraIssueRead.Fields {
			fields[index] = string(field)
		}
		slices.Sort(fields)
		return fields
	case domain.BrokerOperationConfluencePageRead:
		return []string{string(admission.Arguments.ConfluencePageRead.Projection)}
	case domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraCommentApply:
		if kind == domain.BrokerEffectRead {
			return []string{"actor", "comments", "identity", "updated"}
		}
		return []string{"comments"}
	case domain.BrokerOperationOutcomeLookup:
		return []string{}
	default:
		return nil
	}
}

func effectFieldsWithinDefinition(fields, allowed []string) bool {
	if len(allowed) == 0 {
		return len(fields) == 0
	}
	if len(fields) == 0 {
		return false
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = true
	}
	for _, field := range fields {
		if !allowedSet[field] {
			return false
		}
	}
	return true
}

func validQualifiedResource(value domain.BrokerQualifiedResource) bool {
	if !validDigest(value.VersionEvidence) || !validDigest(value.ProjectionSHA256) || len(value.AncestorIDs) > MaxResources {
		return false
	}
	switch value.Kind {
	case domain.BrokerResourceJiraIssue:
		return validPositiveDecimal(value.ImmutableID) && domain.ValidJiraIssueKey(value.Key) && validIdentifier(value.Project) && strings.HasPrefix(value.Key, value.Project+"-") && value.Space == "" && !value.AncestorsPresent && len(value.AncestorIDs) == 0
	case domain.BrokerResourceConfluencePage:
		if !domain.ValidConfluenceContentID(value.ImmutableID) || value.Key != "" || value.Project != "" || !validIdentifier(value.Space) || !value.AncestorsPresent || value.AncestorIDs == nil {
			return false
		}
		seen := map[string]bool{}
		for _, ancestor := range value.AncestorIDs {
			if !domain.ValidConfluenceContentID(ancestor) || ancestor == value.ImmutableID || seen[ancestor] {
				return false
			}
			seen[ancestor] = true
		}
		return true
	case domain.BrokerResourceOperation:
		return validIdentifier(value.ImmutableID) && value.Key == "" && value.Project == "" && value.Space == "" && !value.AncestorsPresent && len(value.AncestorIDs) == 0
	}
	return false
}

func validPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func validEffect(value domain.BrokerEffect) bool {
	if !validQualifiedResource(value.Resource) || len(value.Fields) > MaxMetadataFields || !sort.StringsAreSorted(value.Fields) {
		return false
	}
	seen := map[string]bool{}
	for _, field := range value.Fields {
		if !validIdentifier(field) || seen[field] {
			return false
		}
		seen[field] = true
	}
	return value.Kind == domain.BrokerEffectRead || value.Kind == domain.BrokerEffectComment || value.Kind == domain.BrokerEffectObserve
}

func sameQualifiedResource(left, right domain.BrokerQualifiedResource) bool {
	leftDigest, leftErr := digestValue("qualified-resource", qualifiedResourceToWire(left))
	rightDigest, rightErr := digestValue("qualified-resource", qualifiedResourceToWire(right))
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest
}

func operationAuthorizationRequestToWire(value domain.BrokerOperationAuthorizationRequest) operationAuthorizationRequestWire {
	resources := make([]qualifiedResourceWire, len(value.QualifiedResources))
	for index, resource := range value.QualifiedResources {
		resources[index] = qualifiedResourceToWire(resource)
	}
	effects := make([]effectWire, len(value.Effects))
	for index, effect := range value.Effects {
		effects[index] = effectToWire(effect)
	}
	return operationAuthorizationRequestWire{SchemaVersion: SchemaVersion, QualificationRequest: qualificationRequestToWire(value.QualificationRequest), QualificationDecision: qualificationDecisionToWire(value.QualificationDecision), QualifiedResources: resources, Effects: effects}
}

func operationAuthorizationRequestFromWire(wire operationAuthorizationRequestWire) (domain.BrokerOperationAuthorizationRequest, error) {
	qualification, err := qualificationRequestFromWire(wire.QualificationRequest)
	if err != nil {
		return domain.BrokerOperationAuthorizationRequest{}, err
	}
	resources := make([]domain.BrokerQualifiedResource, len(wire.QualifiedResources))
	for index, resource := range wire.QualifiedResources {
		resources[index] = qualifiedResourceFromWire(resource)
	}
	effects := make([]domain.BrokerEffect, len(wire.Effects))
	for index, effect := range wire.Effects {
		effects[index] = effectFromWire(effect)
	}
	return domain.BrokerOperationAuthorizationRequest{QualificationRequest: qualification, QualificationDecision: qualificationDecisionFromWire(wire.QualificationDecision), QualifiedResources: resources, Effects: effects}, nil
}

func OperationAuthorizationRequestSHA256(value domain.BrokerOperationAuthorizationRequest) (string, error) {
	if err := validateOperationAuthorizationRequest(value); err != nil {
		return "", err
	}
	return digestValue("operation-authorization-request", operationAuthorizationRequestToWire(value))
}

func QualifiedResourcesSHA256(values []domain.BrokerQualifiedResource) (string, error) {
	if len(values) == 0 || len(values) > MaxResources {
		return "", reject(domain.BrokerReasonMalformed)
	}
	wire := make([]qualifiedResourceWire, len(values))
	for index, value := range values {
		if !validQualifiedResource(value) {
			return "", reject(domain.BrokerReasonMalformed)
		}
		wire[index] = qualifiedResourceToWire(value)
	}
	return digestValue("qualified-resources", wire)
}

func EffectsSHA256(values []domain.BrokerEffect) (string, error) {
	if len(values) == 0 || len(values) > MaxEffects {
		return "", reject(domain.BrokerReasonMalformed)
	}
	wire := make([]effectWire, len(values))
	for index, value := range values {
		if !validEffect(value) {
			return "", reject(domain.BrokerReasonMalformed)
		}
		wire[index] = effectToWire(value)
	}
	return digestValue("effects", wire)
}

func qualifiedResourceToWire(value domain.BrokerQualifiedResource) qualifiedResourceWire {
	return qualifiedResourceWire{Kind: string(value.Kind), ImmutableID: value.ImmutableID, Key: value.Key, Project: value.Project, Space: value.Space, AncestorIDs: wireStrings(value.AncestorIDs), AncestorsPresent: value.AncestorsPresent, VersionEvidence: value.VersionEvidence, ProjectionSHA256: value.ProjectionSHA256}
}

func qualifiedResourceFromWire(wire qualifiedResourceWire) domain.BrokerQualifiedResource {
	return domain.BrokerQualifiedResource{Kind: domain.BrokerResourceKind(wire.Kind), ImmutableID: wire.ImmutableID, Key: wire.Key, Project: wire.Project, Space: wire.Space, AncestorIDs: copyStrings(wire.AncestorIDs), AncestorsPresent: wire.AncestorsPresent, VersionEvidence: wire.VersionEvidence, ProjectionSHA256: wire.ProjectionSHA256}
}

func effectToWire(value domain.BrokerEffect) effectWire {
	return effectWire{Kind: string(value.Kind), Resource: qualifiedResourceToWire(value.Resource), Fields: wireStrings(value.Fields)}
}

func effectFromWire(wire effectWire) domain.BrokerEffect {
	return domain.BrokerEffect{Kind: domain.BrokerEffectKind(wire.Kind), Resource: qualifiedResourceFromWire(wire.Resource), Fields: copyStrings(wire.Fields)}
}

func EncodeOperationDecisionV1(value domain.BrokerOperationDecision) ([]byte, error) {
	if err := validateOperationDecision(value, false); err != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	wire := operationDecisionToWire(value)
	wire.DecisionSHA256 = ""
	digest, err := digestValue("operation-decision", wire)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = digest
	return json.Marshal(wire)
}

func DecodeOperationDecisionV1(data []byte) (domain.BrokerOperationDecision, error) {
	var wire operationDecisionWire
	if err := strictDecode(data, 128<<10, &wire); err != nil {
		return domain.BrokerOperationDecision{}, err
	}
	value := operationDecisionFromWire(wire)
	if err := validateOperationDecision(value, true); err != nil || !verifyOperationDecisionDigest(value) {
		return domain.BrokerOperationDecision{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func operationDecisionToWire(value domain.BrokerOperationDecision) operationDecisionWire {
	return operationDecisionWire{SchemaVersion: SchemaVersion, Decision: decisionCoreToWire(value.BrokerDecisionCore), QualificationDecisionSHA256: value.QualificationDecisionSHA256, Operation: string(value.Operation), OperationVersion: value.OperationVersion, ArgumentsSHA256: value.ArgumentsSHA256, ResourcesSHA256: value.ResourcesSHA256, EffectsSHA256: value.EffectsSHA256, DecisionSHA256: value.DecisionSHA256}
}

func operationDecisionFromWire(wire operationDecisionWire) domain.BrokerOperationDecision {
	return domain.BrokerOperationDecision{BrokerDecisionCore: decisionCoreFromWire(wire.Decision), QualificationDecisionSHA256: wire.QualificationDecisionSHA256, Operation: domain.BrokerOperationID(wire.Operation), OperationVersion: wire.OperationVersion, ArgumentsSHA256: wire.ArgumentsSHA256, ResourcesSHA256: wire.ResourcesSHA256, EffectsSHA256: wire.EffectsSHA256, DecisionSHA256: wire.DecisionSHA256}
}

func validateOperationDecision(value domain.BrokerOperationDecision, requireDigest bool) error {
	if validateDecisionCore(value.BrokerDecisionCore) != nil || !validDigest(value.QualificationDecisionSHA256) || !validDigest(value.ArgumentsSHA256) ||
		!validDigest(value.ResourcesSHA256) || !validDigest(value.EffectsSHA256) ||
		requireDigest && !validDigest(value.DecisionSHA256) || !requireDigest && value.DecisionSHA256 != "" && !validDigest(value.DecisionSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	if _, ok := Definition(value.Operation, value.OperationVersion); !ok {
		return reject(domain.BrokerReasonUnsupported)
	}
	return nil
}

func verifyOperationDecisionDigest(value domain.BrokerOperationDecision) bool {
	wire := operationDecisionToWire(value)
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, err := digestValue("operation-decision", wire)
	return err == nil && digest == claimed
}

func EncodeProposalAuthorizationRequestV1(value domain.BrokerProposalAuthorizationRequest) ([]byte, error) {
	if err := ValidateProposalAuthorizationRequestV1(value); err != nil {
		return nil, err
	}
	return marshalEnvelope(proposalAuthorizationRequestToWire(value), MaxEnvelopeBytes)
}

func DecodeProposalAuthorizationRequestV1(data []byte) (domain.BrokerProposalAuthorizationRequest, error) {
	var wire proposalAuthorizationRequestWire
	if err := strictDecode(data, MaxEnvelopeBytes, &wire); err != nil {
		return domain.BrokerProposalAuthorizationRequest{}, err
	}
	value, convertErr := proposalAuthorizationRequestFromWire(wire)
	if wire.SchemaVersion != SchemaVersion || convertErr != nil || ValidateProposalAuthorizationRequestV1(value) != nil {
		return domain.BrokerProposalAuthorizationRequest{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func proposalAuthorizationRequestToWire(value domain.BrokerProposalAuthorizationRequest) proposalAuthorizationRequestWire {
	return proposalAuthorizationRequestWire{SchemaVersion: SchemaVersion, OperationRequest: operationAuthorizationRequestToWire(value.OperationRequest), OperationDecision: operationDecisionToWire(value.OperationDecision), ProposalSchemaVersion: value.ProposalSchemaVersion, ProposalHash: value.ProposalHash, NativeCandidateSHA256: value.NativeCandidateSHA256, VersionEvidenceSHA256: value.VersionEvidenceSHA256}
}

func proposalAuthorizationRequestFromWire(wire proposalAuthorizationRequestWire) (domain.BrokerProposalAuthorizationRequest, error) {
	operationRequest, err := operationAuthorizationRequestFromWire(wire.OperationRequest)
	if err != nil {
		return domain.BrokerProposalAuthorizationRequest{}, err
	}
	return domain.BrokerProposalAuthorizationRequest{OperationRequest: operationRequest, OperationDecision: operationDecisionFromWire(wire.OperationDecision), ProposalSchemaVersion: wire.ProposalSchemaVersion, ProposalHash: wire.ProposalHash, NativeCandidateSHA256: wire.NativeCandidateSHA256, VersionEvidenceSHA256: wire.VersionEvidenceSHA256}, nil
}

func ProposalAuthorizationRequestSHA256(value domain.BrokerProposalAuthorizationRequest) (string, error) {
	if err := ValidateProposalAuthorizationRequestV1(value); err != nil {
		return "", err
	}
	return digestValue("proposal-authorization-request", proposalAuthorizationRequestToWire(value))
}

func verifyAdmissionDecisionDigest(value domain.BrokerAdmissionDecision) bool {
	claimed := value.DecisionSHA256
	wire := admissionDecisionToWire(value)
	wire.DecisionSHA256 = ""
	digest, err := digestValue("admission-decision", wire)
	return err == nil && digest == claimed
}
