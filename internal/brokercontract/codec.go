package brokercontract

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

func strictDecode(data []byte, maximum int64, target any) error {
	if len(data) == 0 || int64(len(data)) > maximum || strictjson.ValidateNestingDepth(data, MaxCanonicalDepth) != nil || strictjson.Validate(data) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	dynamic, err := strictjson.DecodeValue(data)
	if err != nil || !matchesExactJSONShape(reflect.TypeOf(target), dynamic) {
		return reject(domain.BrokerReasonMalformed)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return reject(domain.BrokerReasonMalformed)
	}
	if !wireVersionsAreCurrent(reflect.ValueOf(target)) {
		return reject(domain.BrokerReasonUnsupported)
	}
	return nil
}

func marshalEnvelope(value any, maximum int64) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || int64(len(encoded)) > maximum {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return encoded, nil
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()

func matchesExactJSONShape(target reflect.Type, value any) bool {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == rawMessageType {
		return value != nil
	}
	switch target.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		allowed := make(map[string]reflect.StructField, target.NumField())
		for index := range target.NumField() {
			field := target.Field(index)
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "" {
				name = field.Name
			}
			if name == "-" || !field.IsExported() {
				continue
			}
			allowed[name] = field
			member, present := object[name]
			optional := strings.Contains(","+options+",", ",omitempty,")
			if !present {
				if !optional {
					return false
				}
				continue
			}
			if optional {
				if text, ok := member.(string); ok && text == "" {
					return false
				}
			}
			if member == nil || !matchesExactJSONShape(field.Type, member) {
				return false
			}
		}
		for name := range object {
			if _, ok := allowed[name]; !ok {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		members, ok := value.([]any)
		if !ok {
			return false
		}
		for _, member := range members {
			if member == nil || !matchesExactJSONShape(target.Elem(), member) {
				return false
			}
		}
		return true
	default:
		return value != nil
	}
}

func wireVersionsAreCurrent(value reflect.Value) bool {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Struct:
		for index := range value.NumField() {
			field := value.Type().Field(index)
			current := value.Field(index)
			if field.Name == "SchemaVersion" && current.Kind() == reflect.Int && current.Int() != SchemaVersion {
				return false
			}
			if !wireVersionsAreCurrent(current) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if value.Type() == rawMessageType {
			return true
		}
		for index := range value.Len() {
			if !wireVersionsAreCurrent(value.Index(index)) {
				return false
			}
		}
	}
	return true
}

func EncodeRequestV1(value domain.BrokerRequest) ([]byte, error) {
	if err := validateRequest(value); err != nil {
		return nil, err
	}
	arguments, err := encodeArguments(value.Operation, value.Arguments)
	if err != nil {
		return nil, err
	}
	wire := requestWire{
		SchemaVersion: value.SchemaVersion, Operation: string(value.Operation), OperationVersion: value.OperationVersion,
		RequestID: value.RequestID, Features: wireStrings(value.Features),
		Expect:    expectationsWire{ExecutionID: value.Expect.ExecutionID, ExecutionEpoch: value.Expect.ExecutionEpoch, AuthorityRevision: value.Expect.AuthorityRevision},
		Arguments: arguments,
	}
	encoded, err := json.Marshal(wire)
	definition, _ := Definition(value.Operation, value.OperationVersion)
	if err != nil || int64(len(encoded)) > definition.Limits.MaxRequestBytes {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return encoded, nil
}

func DecodeRequestV1(data []byte) (domain.BrokerRequest, error) {
	var wire requestWire
	if err := strictDecode(data, MaxEnvelopeBytes, &wire); err != nil {
		return domain.BrokerRequest{}, err
	}
	definition, ok := Definition(domain.BrokerOperationID(wire.Operation), wire.OperationVersion)
	if !ok {
		return domain.BrokerRequest{}, reject(domain.BrokerReasonUnsupported)
	}
	if int64(len(data)) > definition.Limits.MaxRequestBytes {
		return domain.BrokerRequest{}, reject(domain.BrokerReasonMalformed)
	}
	arguments, err := decodeArguments(definition.ID, wire.Arguments)
	if err != nil {
		return domain.BrokerRequest{}, err
	}
	value := domain.BrokerRequest{
		SchemaVersion: wire.SchemaVersion, Operation: definition.ID, OperationVersion: wire.OperationVersion, RequestID: wire.RequestID, Features: copyStrings(wire.Features),
		Expect:    domain.BrokerRequestExpectations{ExecutionID: wire.Expect.ExecutionID, ExecutionEpoch: wire.Expect.ExecutionEpoch, AuthorityRevision: wire.Expect.AuthorityRevision},
		Arguments: arguments,
	}
	if err := validateRequest(value); err != nil {
		return domain.BrokerRequest{}, err
	}
	return cloneRequest(value), nil
}

func encodeArguments(operation domain.BrokerOperationID, value domain.BrokerOperationArguments) ([]byte, error) {
	switch operation {
	case domain.BrokerOperationJiraIssueRead:
		fields := make([]string, len(value.JiraIssueRead.Fields))
		for index, field := range value.JiraIssueRead.Fields {
			fields[index] = string(field)
		}
		sort.Strings(fields)
		return json.Marshal(jiraIssueReadArgumentsWire{IssueKey: value.JiraIssueRead.IssueKey, Fields: fields})
	case domain.BrokerOperationConfluencePageRead:
		return json.Marshal(confluencePageReadArgumentsWire{PageID: value.ConfluencePageRead.PageID, Projection: string(value.ConfluencePageRead.Projection)})
	case domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraCommentApply:
		return json.Marshal(jiraCommentArgumentsWire{
			IssueKey: value.JiraComment.IssueKey, NativeBodyBase64: base64.StdEncoding.EncodeToString(value.JiraComment.NativeBody),
			SatisfactionPolicy: value.JiraComment.SatisfactionPolicy, ExpectedProposalHash: value.JiraComment.ExpectedProposalHash,
			OperationTicket: value.JiraComment.OperationTicket,
		})
	case domain.BrokerOperationOutcomeLookup:
		return json.Marshal(outcomeArgumentsWire{OperationTicket: value.Outcome.OperationTicket})
	default:
		return nil, reject(domain.BrokerReasonUnsupported)
	}
}

func decodeArguments(operation domain.BrokerOperationID, data []byte) (domain.BrokerOperationArguments, error) {
	switch operation {
	case domain.BrokerOperationJiraIssueRead:
		var wire jiraIssueReadArgumentsWire
		if err := strictDecode(data, 64<<10, &wire); err != nil {
			return domain.BrokerOperationArguments{}, err
		}
		fields := make([]domain.BrokerJiraIssueField, len(wire.Fields))
		for index, field := range wire.Fields {
			fields[index] = domain.BrokerJiraIssueField(field)
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i] < fields[j] })
		return domain.BrokerOperationArguments{JiraIssueRead: &domain.BrokerJiraIssueReadArguments{IssueKey: wire.IssueKey, Fields: fields}}, nil
	case domain.BrokerOperationConfluencePageRead:
		var wire confluencePageReadArgumentsWire
		if err := strictDecode(data, 64<<10, &wire); err != nil {
			return domain.BrokerOperationArguments{}, err
		}
		return domain.BrokerOperationArguments{ConfluencePageRead: &domain.BrokerConfluencePageReadArguments{PageID: wire.PageID, Projection: domain.BrokerConfluenceProjection(wire.Projection)}}, nil
	case domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraCommentApply:
		var wire jiraCommentArgumentsWire
		if err := strictDecode(data, MaxEnvelopeBytes, &wire); err != nil {
			return domain.BrokerOperationArguments{}, err
		}
		body, ok := validBase64(wire.NativeBodyBase64, MaxJiraCommentBodyBytes)
		if !ok {
			return domain.BrokerOperationArguments{}, reject(domain.BrokerReasonMalformed)
		}
		return domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
			IssueKey: wire.IssueKey, NativeBody: body, SatisfactionPolicy: wire.SatisfactionPolicy,
			ExpectedProposalHash: wire.ExpectedProposalHash, OperationTicket: wire.OperationTicket,
		}}, nil
	case domain.BrokerOperationOutcomeLookup:
		var wire outcomeArgumentsWire
		if err := strictDecode(data, 64<<10, &wire); err != nil {
			return domain.BrokerOperationArguments{}, err
		}
		return domain.BrokerOperationArguments{Outcome: &domain.BrokerOutcomeArguments{OperationTicket: wire.OperationTicket}}, nil
	default:
		return domain.BrokerOperationArguments{}, reject(domain.BrokerReasonUnsupported)
	}
}

func ArgumentsSHA256(value domain.BrokerRequest) (string, error) {
	if err := validateRequest(value); err != nil {
		return "", err
	}
	return argumentsSHA256(value.Operation, value.Arguments)
}

func argumentsSHA256(operation domain.BrokerOperationID, value domain.BrokerOperationArguments) (string, error) {
	if err := validateArguments(operation, value); err != nil {
		return "", err
	}
	arguments, err := encodeArguments(operation, value)
	if err != nil {
		return "", err
	}
	var decoded any
	if err := strictDecode(arguments, MaxEnvelopeBytes, &decoded); err != nil {
		return "", err
	}
	return digestValue("arguments/"+string(operation)+"/v1", decoded)
}

func EncodeVerifiedContextV1(value domain.BrokerVerifiedContext) ([]byte, error) {
	if err := validateContext(value); err != nil {
		return nil, err
	}
	return json.Marshal(contextToWire(value))
}

func DecodeVerifiedContextV1(data []byte) (domain.BrokerVerifiedContext, error) {
	var wire verifiedContextWire
	if err := strictDecode(data, 64<<10, &wire); err != nil {
		return domain.BrokerVerifiedContext{}, err
	}
	value := contextFromWire(wire)
	if wire.SchemaVersion != SchemaVersion || validateContext(value) != nil {
		return domain.BrokerVerifiedContext{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func contextToWire(value domain.BrokerVerifiedContext) verifiedContextWire {
	return verifiedContextWire{
		SchemaVersion: SchemaVersion, PrincipalID: value.PrincipalID, WorkloadID: value.WorkloadID, ExecutionID: value.ExecutionID,
		ExecutionEpoch: value.ExecutionEpoch, Audience: value.Audience, BrokerID: value.BrokerID, AuthorityRevision: value.AuthorityRevision,
		ExecutionNotBeforeMillis: value.ExecutionNotBeforeMillis, ExecutionExpiresMillis: value.ExecutionExpiresMillis,
		GrantExpiresMillis: value.GrantExpiresMillis, CredentialExpiresMillis: value.CredentialExpiresMillis,
		Backend: backendBindingWire{Service: value.Backend.Service, OriginSHA256: value.Backend.OriginSHA256, WorkloadBackendID: value.Backend.WorkloadBackendID},
	}
}

func contextFromWire(wire verifiedContextWire) domain.BrokerVerifiedContext {
	return domain.BrokerVerifiedContext{
		PrincipalID: wire.PrincipalID, WorkloadID: wire.WorkloadID, ExecutionID: wire.ExecutionID, ExecutionEpoch: wire.ExecutionEpoch,
		Audience: wire.Audience, BrokerID: wire.BrokerID, AuthorityRevision: wire.AuthorityRevision,
		ExecutionNotBeforeMillis: wire.ExecutionNotBeforeMillis, ExecutionExpiresMillis: wire.ExecutionExpiresMillis,
		GrantExpiresMillis: wire.GrantExpiresMillis, CredentialExpiresMillis: wire.CredentialExpiresMillis,
		Backend: domain.BrokerBackendBinding{Service: wire.Backend.Service, OriginSHA256: wire.Backend.OriginSHA256, WorkloadBackendID: wire.Backend.WorkloadBackendID},
	}
}

func VerifiedContextSHA256(value domain.BrokerVerifiedContext) (string, error) {
	if err := validateContext(value); err != nil {
		return "", err
	}
	return digestValue("verified-context", contextToWire(value))
}

func EncodeAdmissionDecisionV1(value domain.BrokerAdmissionDecision) ([]byte, error) {
	if err := validateAdmissionDecision(value, false); err != nil {
		return nil, err
	}
	wire := admissionDecisionToWire(value)
	wire.DecisionSHA256 = ""
	digest, err := digestValue("admission-decision", wire)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = digest
	return json.Marshal(wire)
}

func DecodeAdmissionDecisionV1(data []byte) (domain.BrokerAdmissionDecision, error) {
	var wire admissionDecisionWire
	if err := strictDecode(data, 64<<10, &wire); err != nil {
		return domain.BrokerAdmissionDecision{}, err
	}
	value := admissionDecisionFromWire(wire)
	if wire.SchemaVersion != SchemaVersion || validateAdmissionDecision(value, true) != nil {
		return domain.BrokerAdmissionDecision{}, reject(domain.BrokerReasonMalformed)
	}
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, err := digestValue("admission-decision", wire)
	if err != nil || digest != claimed {
		return domain.BrokerAdmissionDecision{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func validateAdmissionDecision(value domain.BrokerAdmissionDecision, requireDigest bool) error {
	if validateDecisionCore(value.BrokerDecisionCore) != nil ||
		requireDigest && !validDigest(value.DecisionSHA256) || !requireDigest && value.DecisionSHA256 != "" && !validDigest(value.DecisionSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func admissionDecisionToWire(value domain.BrokerAdmissionDecision) admissionDecisionWire {
	return admissionDecisionWire{SchemaVersion: SchemaVersion, Decision: decisionCoreToWire(value.BrokerDecisionCore), DecisionSHA256: value.DecisionSHA256}
}

func admissionDecisionFromWire(wire admissionDecisionWire) domain.BrokerAdmissionDecision {
	return domain.BrokerAdmissionDecision{BrokerDecisionCore: decisionCoreFromWire(wire.Decision), DecisionSHA256: wire.DecisionSHA256}
}

func validateDecisionCore(value domain.BrokerDecisionCore) error {
	if !validDecisionStatus(value.Status, value.Reason) || !validIdentifier(value.DecisionID) || !validIdentifier(value.AuthorityRevision) ||
		!validDigest(value.ContextSHA256) || !validDigest(value.RequestSHA256) || value.IssuedAtMillis <= 0 ||
		value.ExpiresAtMillis <= value.IssuedAtMillis || value.ExpiresAtMillis-value.IssuedAtMillis > domain.BrokerMaxDecisionLeaseMillis {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func decisionCoreToWire(value domain.BrokerDecisionCore) decisionCoreWire {
	return decisionCoreWire{Status: string(value.Status), Reason: string(value.Reason), DecisionID: value.DecisionID, AuthorityRevision: value.AuthorityRevision, ContextSHA256: value.ContextSHA256, RequestSHA256: value.RequestSHA256, IssuedAtMillis: value.IssuedAtMillis, ExpiresAtMillis: value.ExpiresAtMillis}
}

func decisionCoreFromWire(wire decisionCoreWire) domain.BrokerDecisionCore {
	return domain.BrokerDecisionCore{Status: domain.BrokerDecisionStatus(wire.Status), Reason: domain.BrokerReason(wire.Reason), DecisionID: wire.DecisionID, AuthorityRevision: wire.AuthorityRevision, ContextSHA256: wire.ContextSHA256, RequestSHA256: wire.RequestSHA256, IssuedAtMillis: wire.IssuedAtMillis, ExpiresAtMillis: wire.ExpiresAtMillis}
}

func EncodeProposalClearanceV1(value domain.BrokerProposalClearance) ([]byte, error) {
	if err := validateProposalClearance(value, false); err != nil {
		return nil, err
	}
	wire := proposalClearanceToWire(value)
	wire.ClearanceSHA256 = ""
	digest, err := digestValue("proposal-clearance", wire)
	if err != nil {
		return nil, err
	}
	wire.ClearanceSHA256 = digest
	return json.Marshal(wire)
}

func DecodeProposalClearanceV1(data []byte) (domain.BrokerProposalClearance, error) {
	var wire proposalClearanceWire
	if err := strictDecode(data, 64<<10, &wire); err != nil {
		return domain.BrokerProposalClearance{}, err
	}
	value := proposalClearanceFromWire(wire)
	if wire.SchemaVersion != SchemaVersion || validateProposalClearance(value, true) != nil {
		return domain.BrokerProposalClearance{}, reject(domain.BrokerReasonMalformed)
	}
	claimed := wire.ClearanceSHA256
	wire.ClearanceSHA256 = ""
	digest, err := digestValue("proposal-clearance", wire)
	if err != nil || digest != claimed {
		return domain.BrokerProposalClearance{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func validateProposalClearance(value domain.BrokerProposalClearance, requireDigest bool) error {
	if validateDecisionCore(value.BrokerDecisionCore) != nil || !validDigest(value.OperationDecisionSHA256) || !validDigest(value.ProposalHash) ||
		!validDigest(value.NativeCandidateSHA256) || !validDigest(value.VersionEvidenceSHA256) ||
		requireDigest && !validDigest(value.ClearanceSHA256) || !requireDigest && value.ClearanceSHA256 != "" && !validDigest(value.ClearanceSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func proposalClearanceToWire(value domain.BrokerProposalClearance) proposalClearanceWire {
	return proposalClearanceWire{SchemaVersion: SchemaVersion, Decision: decisionCoreToWire(value.BrokerDecisionCore), OperationDecisionSHA256: value.OperationDecisionSHA256, ProposalHash: value.ProposalHash, NativeCandidateSHA256: value.NativeCandidateSHA256, VersionEvidenceSHA256: value.VersionEvidenceSHA256, ClearanceSHA256: value.ClearanceSHA256}
}

func proposalClearanceFromWire(wire proposalClearanceWire) domain.BrokerProposalClearance {
	return domain.BrokerProposalClearance{BrokerDecisionCore: decisionCoreFromWire(wire.Decision), OperationDecisionSHA256: wire.OperationDecisionSHA256, ProposalHash: wire.ProposalHash, NativeCandidateSHA256: wire.NativeCandidateSHA256, VersionEvidenceSHA256: wire.VersionEvidenceSHA256, ClearanceSHA256: wire.ClearanceSHA256}
}

func verifyProposalClearanceDigest(value domain.BrokerProposalClearance) bool {
	wire := proposalClearanceToWire(value)
	claimed := wire.ClearanceSHA256
	wire.ClearanceSHA256 = ""
	digest, err := digestValue("proposal-clearance", wire)
	return err == nil && digest == claimed
}

func EncodeDiscoveryV1(value domain.BrokerDiscoveryProjection) ([]byte, error) {
	wire, err := discoveryToWire(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wire)
}

func DecodeDiscoveryV1(data []byte) (domain.BrokerDiscoveryProjection, error) {
	var wire discoveryWire
	if err := strictDecode(data, 1<<20, &wire); err != nil {
		return domain.BrokerDiscoveryProjection{}, err
	}
	value := discoveryFromWire(wire)
	if err := validateDiscovery(value); wire.SchemaVersion != SchemaVersion || err != nil {
		return domain.BrokerDiscoveryProjection{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func discoveryToWire(value domain.BrokerDiscoveryProjection) (discoveryWire, error) {
	if err := validateDiscovery(value); err != nil {
		return discoveryWire{}, err
	}
	operations := make([]discoveryOperationWire, len(value.Operations))
	for index, operation := range value.Operations {
		operations[index] = discoveryOperationWire{ID: string(operation.ID), Version: operation.Version, Availability: string(operation.Availability), Features: wireStrings(operation.Features), Limits: limitsToWire(operation.Limits)}
	}
	return discoveryWire{SchemaVersion: SchemaVersion, ExecutionScopeSHA256: value.ExecutionScopeSHA256, RegistrySHA256: value.RegistrySHA256, IssuedAtMillis: value.IssuedAtMillis, ExpiresAtMillis: value.ExpiresAtMillis, Operations: operations, Complete: value.Complete}, nil
}

func discoveryFromWire(wire discoveryWire) domain.BrokerDiscoveryProjection {
	operations := make([]domain.BrokerDiscoveryOperation, len(wire.Operations))
	for index, operation := range wire.Operations {
		operations[index] = domain.BrokerDiscoveryOperation{ID: domain.BrokerOperationID(operation.ID), Version: operation.Version, Availability: domain.BrokerAvailability(operation.Availability), Features: copyStrings(operation.Features), Limits: limitsFromWire(operation.Limits)}
	}
	return domain.BrokerDiscoveryProjection{SchemaVersion: wire.SchemaVersion, ExecutionScopeSHA256: wire.ExecutionScopeSHA256, RegistrySHA256: wire.RegistrySHA256, IssuedAtMillis: wire.IssuedAtMillis, ExpiresAtMillis: wire.ExpiresAtMillis, Operations: operations, Complete: wire.Complete}
}

func validateDiscovery(value domain.BrokerDiscoveryProjection) error {
	if value.SchemaVersion != SchemaVersion || !validDigest(value.ExecutionScopeSHA256) || !validDigest(value.RegistrySHA256) || value.RegistrySHA256 != RegistrySHA256() ||
		value.IssuedAtMillis <= 0 || value.ExpiresAtMillis <= value.IssuedAtMillis || value.ExpiresAtMillis-value.IssuedAtMillis > domain.BrokerMaxDecisionLeaseMillis || len(value.Operations) > MaxDiscoveryOperations {
		return reject(domain.BrokerReasonMalformed)
	}
	seen := map[domain.BrokerOperationID]bool{}
	previous := domain.BrokerOperationID("")
	for _, operation := range value.Operations {
		definition, ok := Definition(operation.ID, operation.Version)
		if !ok || seen[operation.ID] || previous != "" && operation.ID <= previous || operation.Limits != definition.Limits || operation.Features == nil || !slices.Equal(operation.Features, definition.RequiredFeatures) ||
			operation.Availability != domain.BrokerAvailabilityAvailable && operation.Availability != domain.BrokerAvailabilityUnsupported && operation.Availability != domain.BrokerAvailabilityUnavailable ||
			operation.Availability == domain.BrokerAvailabilityAvailable && !definition.Available {
			return reject(domain.BrokerReasonMalformed)
		}
		seen[operation.ID] = true
		previous = operation.ID
	}
	return nil
}

func limitsToWire(value domain.BrokerLimits) limitsWire {
	return limitsWire{MaxRequestBytes: value.MaxRequestBytes, MaxResponseBytes: value.MaxResponseBytes, MaxTotalUpstreamRequests: value.MaxTotalUpstreamRequests, MaxTotalUpstreamResponseBytes: value.MaxTotalUpstreamResponseBytes, Qualification: phaseLimitsToWire(value.Qualification), Business: phaseLimitsToWire(value.Business), MaxResources: value.MaxResources, MaxFields: value.MaxFields, MaxNativeBodyBytes: value.MaxNativeBodyBytes, MaxStreamChunks: value.MaxStreamChunks, MaxStreamChunkBytes: value.MaxStreamChunkBytes, MaxOperationMillis: value.MaxOperationMillis, MaxDecisionLeaseMillis: value.MaxDecisionLeaseMillis}
}

func limitsFromWire(wire limitsWire) domain.BrokerLimits {
	return domain.BrokerLimits{MaxRequestBytes: wire.MaxRequestBytes, MaxResponseBytes: wire.MaxResponseBytes, MaxTotalUpstreamRequests: wire.MaxTotalUpstreamRequests, MaxTotalUpstreamResponseBytes: wire.MaxTotalUpstreamResponseBytes, Qualification: phaseLimitsFromWire(wire.Qualification), Business: phaseLimitsFromWire(wire.Business), MaxResources: wire.MaxResources, MaxFields: wire.MaxFields, MaxNativeBodyBytes: wire.MaxNativeBodyBytes, MaxStreamChunks: wire.MaxStreamChunks, MaxStreamChunkBytes: wire.MaxStreamChunkBytes, MaxOperationMillis: wire.MaxOperationMillis, MaxDecisionLeaseMillis: wire.MaxDecisionLeaseMillis}
}

func phaseLimitsToWire(value domain.BrokerPhaseLimits) phaseLimitsWire {
	return phaseLimitsWire{MaxRequests: value.MaxRequests, MaxResponseBytes: value.MaxResponseBytes}
}

func phaseLimitsFromWire(wire phaseLimitsWire) domain.BrokerPhaseLimits {
	return domain.BrokerPhaseLimits{MaxRequests: wire.MaxRequests, MaxResponseBytes: wire.MaxResponseBytes}
}

func cloneRequest(value domain.BrokerRequest) domain.BrokerRequest {
	value.Features = copyStrings(value.Features)
	if value.Arguments.JiraIssueRead != nil {
		copy := *value.Arguments.JiraIssueRead
		copy.Fields = append([]domain.BrokerJiraIssueField(nil), copy.Fields...)
		value.Arguments.JiraIssueRead = &copy
	}
	if value.Arguments.ConfluencePageRead != nil {
		copy := *value.Arguments.ConfluencePageRead
		value.Arguments.ConfluencePageRead = &copy
	}
	if value.Arguments.JiraComment != nil {
		copy := *value.Arguments.JiraComment
		copy.NativeBody = bytes.Clone(copy.NativeBody)
		value.Arguments.JiraComment = &copy
	}
	if value.Arguments.Outcome != nil {
		copy := *value.Arguments.Outcome
		value.Arguments.Outcome = &copy
	}
	return value
}

func RegistrySHA256() string {
	definitions := Registry()
	projection := make([]any, len(definitions))
	for index, definition := range definitions {
		effects := make([]any, len(definition.Effects))
		for effectIndex, effect := range definition.Effects {
			fields := copyStrings(effect.Fields)
			sort.Strings(fields)
			effects[effectIndex] = struct {
				Kind     string   `json:"kind"`
				Resource string   `json:"resource"`
				Fields   []string `json:"fields"`
			}{string(effect.Kind), string(effect.ResourceKind), fields}
		}
		projection[index] = struct {
			ID                  string     `json:"id"`
			Version             int        `json:"version"`
			Arguments           string     `json:"argument_schema_id"`
			Result              string     `json:"result_schema_id"`
			Schema              string     `json:"contract_schema_sha256"`
			Qualification       string     `json:"qualification_profile"`
			Execution           string     `json:"execution_profile"`
			Backend             string     `json:"backend_service"`
			QualificationFields []string   `json:"qualification_fields"`
			RequiredFeatures    []string   `json:"required_features"`
			Available           bool       `json:"available"`
			Streaming           bool       `json:"streaming"`
			Limits              limitsWire `json:"limits"`
			Effects             []any      `json:"effects"`
		}{string(definition.ID), definition.Version, definition.ArgumentSchemaID, definition.ResultSchemaID, definition.ContractSchemaSHA256, definition.QualificationProfile, definition.ExecutionProfile, definition.BackendService, wireStrings(definition.QualificationFields), wireStrings(definition.RequiredFeatures), definition.Available, definition.Streaming, limitsToWire(definition.Limits), effects}
	}
	digest, err := digestValue("registry", projection)
	if err != nil {
		panic("invalid static broker registry")
	}
	return digest
}
