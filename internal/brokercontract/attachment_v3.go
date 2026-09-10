package brokercontract

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

const (
	MaxAttachmentAuthorityEnvelopeBytesV3 = int64(64 << 10)
	AttachmentFrameVersionV3              = 1
	AttachmentEmptyBodySHA256V3           = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var attachmentMetadataFieldsV3 = []string{
	"attachment.created", "attachment.filename", "attachment.id", "attachment.media_type", "attachment.parent_id", "attachment.size",
	"issue.id", "issue.key", "issue.project", "issue.updated",
}

func EncodeAttachmentRequestV3(value domain.BrokerAttachmentRequestV3) ([]byte, error) {
	if validateAttachmentRequestV3(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalAttachmentV3(attachmentRequestToWireV3(value), MaxAttachmentRequestBytesV3)
}

func DecodeAttachmentRequestV3(data []byte) (domain.BrokerAttachmentRequestV3, error) {
	var wire attachmentRequestWireV3
	if !decodeAttachmentV3(data, MaxAttachmentRequestBytesV3, &wire) {
		return domain.BrokerAttachmentRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	if wire.SchemaVersion != ExecutionSchemaVersionV3 {
		return domain.BrokerAttachmentRequestV3{}, reject(domain.BrokerReasonUnsupported)
	}
	value := attachmentRequestFromWireV3(wire)
	if validateAttachmentRequestV3(value) != nil {
		return domain.BrokerAttachmentRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func AttachmentRequestSHA256V3(value domain.BrokerAttachmentRequestV3) (string, error) {
	if validateAttachmentRequestV3(value) != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestExecutionV3("request", attachmentRequestToWireV3(value))
}

func AttachmentArgumentsSHA256V3(value domain.BrokerAttachmentArgumentsV3) (string, error) {
	if !validAttachmentArgumentsV3(value) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestExecutionV3("arguments", attachmentArgumentsToWireV3(value))
}

func EncodeAttachmentAdmissionRequestV3(value domain.BrokerAttachmentAdmissionRequestV3) ([]byte, error) {
	if validateAttachmentAdmissionRequestV3(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalAttachmentV3(attachmentAdmissionRequestToWireV3(value), MaxAttachmentAuthorityEnvelopeBytesV3)
}

func DecodeAttachmentAdmissionRequestV3(data []byte) (domain.BrokerAttachmentAdmissionRequestV3, error) {
	var wire attachmentAdmissionRequestWireV3
	if !decodeAttachmentV3(data, MaxAttachmentAuthorityEnvelopeBytesV3, &wire) {
		return domain.BrokerAttachmentAdmissionRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	if wire.SchemaVersion != ExecutionSchemaVersionV3 {
		return domain.BrokerAttachmentAdmissionRequestV3{}, reject(domain.BrokerReasonUnsupported)
	}
	value := attachmentAdmissionRequestFromWireV3(wire)
	if validateAttachmentAdmissionRequestV3(value) != nil {
		return domain.BrokerAttachmentAdmissionRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func AttachmentAdmissionRequestSHA256V3(value domain.BrokerAttachmentAdmissionRequestV3) (string, error) {
	if validateAttachmentAdmissionRequestV3(value) != nil {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestExecutionV3("admission-request", attachmentAdmissionRequestToWireV3(value))
}

func EncodeAttachmentAdmissionDecisionV3(value domain.BrokerAttachmentAdmissionDecisionV3) ([]byte, error) {
	if validateAttachmentAdmissionDecisionV3(value, false) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	wire := attachmentAdmissionDecisionToWireV3(value)
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV3("admission-decision", wire)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = digest
	return marshalAttachmentV3(wire, MaxAttachmentAuthorityCallBytesV3)
}

func DecodeAttachmentAdmissionDecisionV3(data []byte) (domain.BrokerAttachmentAdmissionDecisionV3, error) {
	var wire attachmentAdmissionDecisionWireV3
	if !decodeAttachmentV3(data, MaxAttachmentAuthorityCallBytesV3, &wire) || wire.SchemaVersion != ExecutionSchemaVersionV3 {
		return domain.BrokerAttachmentAdmissionDecisionV3{}, reject(domain.BrokerReasonMalformed)
	}
	value := attachmentAdmissionDecisionFromWireV3(wire)
	if validateAttachmentAdmissionDecisionV3(value, true) != nil || !verifyAttachmentAdmissionDecisionDigestV3(value) {
		return domain.BrokerAttachmentAdmissionDecisionV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func ValidateAttachmentAdmissionDecisionV3(value domain.BrokerAttachmentAdmissionDecisionV3, request domain.BrokerAttachmentAdmissionRequestV3, nowMillis int64) error {
	requestDigest, err := AttachmentAdmissionRequestSHA256V3(request)
	if err != nil || validateAttachmentAdmissionDecisionV3(value, true) != nil || !verifyAttachmentAdmissionDecisionDigestV3(value) {
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

func MatchAttachmentRequestContextV3(request domain.BrokerAttachmentRequestV3, verified domain.BrokerVerifiedContext, nowMillis, deadlineMillis int64) error {
	if validateAttachmentRequestV3(request) != nil || validateContext(verified) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	if verified.Backend.Service != "jira" {
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
	if deadlineMillis <= nowMillis || deadlineMillis-nowMillis > domain.BrokerMaxOperationMillis || deadlineMillis > verified.ExecutionExpiresMillis || deadlineMillis > verified.GrantExpiresMillis || deadlineMillis > verified.CredentialExpiresMillis {
		return reject(domain.BrokerReasonDecisionExpired)
	}
	return nil
}

func AttachmentSnapshotSHA256V3(value domain.BrokerJiraAttachmentSnapshotV3) (string, error) {
	if !validAttachmentSnapshotV3(value, true) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestExecutionV3("snapshot", attachmentSnapshotToWireV3(value))
}

func AttachmentResourcesSHA256V3(value domain.BrokerJiraAttachmentSnapshotV3) (string, error) {
	if !validAttachmentSnapshotV3(value, true) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestExecutionV3("qualified-resources", attachmentSnapshotToWireV3(value))
}

func AttachmentEffectsSHA256V3(values []domain.BrokerAttachmentEffectV3) (string, error) {
	if !validAttachmentEffectsV3(values) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	wires := make([]attachmentEffectWireV3, len(values))
	for index, value := range values {
		wires[index] = attachmentEffectToWireV3(value)
	}
	return digestExecutionV3("effects", wires)
}

func AttachmentMetadataPlanSHA256V3(value domain.BrokerAttachmentMetadataPlanV3) (string, error) {
	if !validAttachmentMetadataPlanV3(value) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestExecutionV3("metadata-plan", attachmentMetadataPlanToWireV3(value))
}

func EncodeAttachmentQualificationRequestV3(value domain.BrokerAttachmentQualificationRequestV3) ([]byte, error) {
	wire, err := attachmentQualificationRequestToWireV3(value)
	if err != nil {
		return nil, err
	}
	return marshalAttachmentV3(wire, MaxAttachmentAuthorityEnvelopeBytesV3)
}

func DecodeAttachmentQualificationRequestV3(data []byte) (domain.BrokerAttachmentQualificationRequestV3, error) {
	var envelope attachmentQualificationEnvelopeWireV3
	if !decodeAttachmentV3(data, MaxAttachmentAuthorityEnvelopeBytesV3, &envelope) || envelope.SchemaVersion != ExecutionSchemaVersionV3 {
		return domain.BrokerAttachmentQualificationRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	value, err := attachmentQualificationRequestFromWireV3(envelope)
	if err != nil || validateAttachmentQualificationRequestV3(value) != nil {
		return domain.BrokerAttachmentQualificationRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func AttachmentQualificationRequestSHA256V3(value domain.BrokerAttachmentQualificationRequestV3) (string, error) {
	wire, err := attachmentQualificationRequestToWireV3(value)
	if err != nil {
		return "", err
	}
	return digestExecutionV3("qualification-request/"+string(value.Phase), wire)
}

func EncodeAttachmentQualificationDecisionV3(value domain.BrokerAttachmentQualificationDecisionV3) ([]byte, error) {
	wire, err := attachmentQualificationDecisionToWireV3(value, false)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV3("qualification-decision/"+string(value.Phase), wire)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = digest
	return marshalAttachmentV3(wire, MaxAttachmentAuthorityCallBytesV3)
}

func DecodeAttachmentQualificationDecisionV3(data []byte) (domain.BrokerAttachmentQualificationDecisionV3, error) {
	var envelope attachmentQualificationDecisionEnvelopeWireV3
	if !decodeAttachmentV3(data, MaxAttachmentAuthorityCallBytesV3, &envelope) || envelope.SchemaVersion != ExecutionSchemaVersionV3 {
		return domain.BrokerAttachmentQualificationDecisionV3{}, reject(domain.BrokerReasonMalformed)
	}
	value, err := attachmentQualificationDecisionFromWireV3(envelope)
	if err != nil || !verifyAttachmentQualificationDecisionDigestV3(value) {
		return domain.BrokerAttachmentQualificationDecisionV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func ValidateAttachmentQualificationDecisionV3(value domain.BrokerAttachmentQualificationDecisionV3, request domain.BrokerAttachmentQualificationRequestV3, nowMillis, operationDeadlineMillis int64) error {
	if validateAttachmentQualificationRequestV3(request) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	requestDigest, err := AttachmentQualificationRequestSHA256V3(request)
	plan, context, lineageOK := attachmentQualificationPlanContextV3(request, nowMillis)
	planDigest, planErr := AttachmentMetadataPlanSHA256V3(plan)
	if !lineageOK {
		plan, context = attachmentQualificationPlanAndContextV3(request)
		planDigest, planErr = AttachmentMetadataPlanSHA256V3(plan)
	}
	if err != nil || planErr != nil || !verifyAttachmentQualificationDecisionDigestV3(value) || value.Phase != request.Phase || value.PlanSHA256 != planDigest {
		return reject(domain.BrokerReasonMalformed)
	}
	if !decisionCoreMatches(value.BrokerDecisionCore, context, requestDigest) {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	if !lineageOK {
		return attachmentQualificationLineageErrorV3(request, nowMillis)
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return reject(value.Reason)
	}
	return validateDecisionTime(value.BrokerDecisionCore, context, nowMillis, operationDeadlineMillis)
}

func EncodeAttachmentOperationAuthorizationRequestV3(value domain.BrokerAttachmentOperationAuthorizationRequestV3) ([]byte, error) {
	wire, err := attachmentOperationRequestToWireV3(value)
	if err != nil {
		return nil, err
	}
	return marshalAttachmentV3(wire, MaxAttachmentAuthorityEnvelopeBytesV3)
}

func DecodeAttachmentOperationAuthorizationRequestV3(data []byte) (domain.BrokerAttachmentOperationAuthorizationRequestV3, error) {
	var envelope attachmentOperationEnvelopeWireV3
	if !decodeAttachmentV3(data, MaxAttachmentAuthorityEnvelopeBytesV3, &envelope) || envelope.SchemaVersion != ExecutionSchemaVersionV3 {
		return domain.BrokerAttachmentOperationAuthorizationRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	value, err := attachmentOperationRequestFromWireV3(envelope)
	if err != nil || validateAttachmentOperationRequestV3(value) != nil {
		return domain.BrokerAttachmentOperationAuthorizationRequestV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func AttachmentOperationAuthorizationRequestSHA256V3(value domain.BrokerAttachmentOperationAuthorizationRequestV3) (string, error) {
	wire, err := attachmentOperationRequestToWireV3(value)
	if err != nil {
		return "", err
	}
	return digestExecutionV3("operation-request/"+string(value.Phase), wire)
}

func EncodeAttachmentOperationDecisionV3(value domain.BrokerAttachmentOperationDecisionV3) ([]byte, error) {
	wire, err := attachmentOperationDecisionToWireV3(value, false)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV3("operation-decision/"+string(value.Phase), wire)
	if err != nil {
		return nil, err
	}
	wire.DecisionSHA256 = digest
	return marshalAttachmentV3(wire, MaxAttachmentAuthorityCallBytesV3)
}

func DecodeAttachmentOperationDecisionV3(data []byte) (domain.BrokerAttachmentOperationDecisionV3, error) {
	var envelope attachmentOperationDecisionEnvelopeWireV3
	if !decodeAttachmentV3(data, MaxAttachmentAuthorityCallBytesV3, &envelope) || envelope.SchemaVersion != ExecutionSchemaVersionV3 {
		return domain.BrokerAttachmentOperationDecisionV3{}, reject(domain.BrokerReasonMalformed)
	}
	value, err := attachmentOperationDecisionFromWireV3(envelope)
	if err != nil || !verifyAttachmentOperationDecisionDigestV3(value) {
		return domain.BrokerAttachmentOperationDecisionV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func ValidateAttachmentOperationDecisionV3(value domain.BrokerAttachmentOperationDecisionV3, request domain.BrokerAttachmentOperationAuthorizationRequestV3, nowMillis, operationDeadlineMillis int64) error {
	requestDigest, err := AttachmentOperationAuthorizationRequestSHA256V3(request)
	binding, context, bindingErr := attachmentOperationDecisionBindingV3(request)
	if err != nil || bindingErr != nil || !verifyAttachmentOperationDecisionDigestV3(value) || !attachmentOperationDecisionMatchesBindingV3(value, binding) {
		return reject(domain.BrokerReasonMalformed)
	}
	if !decisionCoreMatches(value.BrokerDecisionCore, context, requestDigest) {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	if request.Qualified != nil {
		if err := ValidateAttachmentQualificationDecisionV3(request.Qualified.QualificationDecision, request.Qualified.QualificationRequest, nowMillis, attachmentQualificationDeadlineV3(request.Qualified.QualificationRequest)); err != nil {
			return err
		}
	} else if err := ValidateAttachmentQualificationDecisionV3(request.Release.QualificationDecision, request.Release.QualificationRequest, nowMillis, operationDeadlineMillis); err != nil {
		return err
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return reject(value.Reason)
	}
	return validateDecisionTime(value.BrokerDecisionCore, context, nowMillis, operationDeadlineMillis)
}

func AttachmentStreamAnchorSHA256V3(value domain.BrokerAttachmentStreamAnchorV3) (string, error) {
	if !validAttachmentAnchorV3(value) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestExecutionV3("stream-anchor-v1", attachmentAnchorToWireV3(value))
}

// MatchAttachmentReleaseContextV3 binds a fresh authentication result to the
// immutable anchor without making any expired setup decision current again.
func MatchAttachmentReleaseContextV3(anchor domain.BrokerAttachmentStreamAnchorV3, current domain.BrokerVerifiedContext, nowMillis, authenticationReleaseDeadlineMillis int64) error {
	if !validAttachmentAnchorV3(anchor) || validateContext(current) != nil {
		return reject(domain.BrokerReasonMalformed)
	}
	if current != anchor.Context {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	if nowMillis < current.ExecutionNotBeforeMillis-domain.BrokerClockAllowanceMillis || nowMillis >= current.ExecutionExpiresMillis {
		return reject(domain.BrokerReasonStaleExecution)
	}
	if nowMillis >= current.CredentialExpiresMillis {
		return reject(domain.BrokerReasonCredentialExpired)
	}
	if nowMillis >= current.GrantExpiresMillis {
		return reject(domain.BrokerReasonGrantExpired)
	}
	if authenticationReleaseDeadlineMillis <= nowMillis || authenticationReleaseDeadlineMillis-nowMillis > domain.BrokerMaxDecisionLeaseMillis ||
		authenticationReleaseDeadlineMillis > anchor.OverallDeadlineMillis || authenticationReleaseDeadlineMillis > current.ExecutionExpiresMillis ||
		authenticationReleaseDeadlineMillis > current.GrantExpiresMillis || authenticationReleaseDeadlineMillis > current.CredentialExpiresMillis {
		return reject(domain.BrokerReasonDecisionExpired)
	}
	return nil
}

// ValidateAttachmentReleaseQualificationV3 checks the current release
// context against historical anchor bytes and the caller's last committed
// receipt. It does not validate or revive any setup decision lease.
func ValidateAttachmentReleaseQualificationV3(request domain.BrokerAttachmentQualificationRequestV3, anchor domain.BrokerAttachmentStreamAnchorV3, expectedPriorReleaseSHA256 string, nowMillis, authenticationReleaseDeadlineMillis int64) error {
	if validateAttachmentQualificationRequestV3(request) != nil || request.Phase != domain.BrokerAttachmentQualificationRelease || request.Release == nil || !validDigest(expectedPriorReleaseSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	anchorDigest, err := AttachmentStreamAnchorSHA256V3(anchor)
	if err != nil || request.Release.AnchorSHA256 != anchorDigest || request.Release.PriorReleaseSHA256 != expectedPriorReleaseSHA256 || request.Release.Plan.SelectorSHA256 != anchor.ArgumentsSHA256 {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	return MatchAttachmentReleaseContextV3(anchor, request.Release.Context, nowMillis, authenticationReleaseDeadlineMillis)
}

func AttachmentManifestCoreSHA256V3(value domain.BrokerAttachmentManifestCoreV3) (string, error) {
	if !validAttachmentManifestCoreV3(value) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestExecutionV3("manifest-core-v1", attachmentManifestCoreToWireV3(value))
}

func AttachmentReleaseRootSHA256V3(anchorSHA256, manifestCoreSHA256 string) (string, error) {
	if !validDigest(anchorSHA256) || !validDigest(manifestCoreSHA256) {
		return "", reject(domain.BrokerReasonMalformed)
	}
	return digestExecutionV3("release-root-v1", struct {
		AnchorSHA256       string `json:"anchor_sha256"`
		ManifestCoreSHA256 string `json:"manifest_core_sha256"`
	}{anchorSHA256, manifestCoreSHA256})
}

func AttachmentReleaseFactsSHA256V3(value domain.BrokerAttachmentReleaseFactsV3) (string, error) {
	wire, err := attachmentReleaseFactsToWireV3(value)
	if err != nil {
		return "", err
	}
	return digestExecutionV3("release-facts-v1", wire)
}

// AttachmentReleaseReceiptSHA256V3 binds every emitted line in release order.
// A first-and-last data release has three lines: manifest, data, terminal.
// The stream owner separately validates the permitted line shape and flush.
func AttachmentReleaseReceiptSHA256V3(priorReleaseSHA256, releaseDecisionSHA256 string, exactLineSHA256s []string) (string, error) {
	if !validDigest(priorReleaseSHA256) || !validDigest(releaseDecisionSHA256) || len(exactLineSHA256s) == 0 || len(exactLineSHA256s) > 3 {
		return "", reject(domain.BrokerReasonMalformed)
	}
	for _, value := range exactLineSHA256s {
		if !validDigest(value) {
			return "", reject(domain.BrokerReasonMalformed)
		}
	}
	return digestExecutionV3("release-receipt-v1", struct {
		PriorReleaseSHA256    string   `json:"prior_release_sha256"`
		ReleaseDecisionSHA256 string   `json:"release_decision_sha256"`
		ExactLineSHA256s      []string `json:"exact_emitted_line_sha256s"`
	}{priorReleaseSHA256, releaseDecisionSHA256, append([]string(nil), exactLineSHA256s...)})
}

func AttachmentExactLineSHA256V3(line []byte) (string, error) {
	// Codec line caps count canonical JSON; an exact emitted line adds one LF.
	if len(line) == 0 || int64(len(line)) > MaxAttachmentDataLineBytesV3+1 {
		return "", reject(domain.BrokerReasonMalformed)
	}
	digest := sha256.Sum256(line)
	return hex.EncodeToString(digest[:]), nil
}

func validAttachmentArgumentsV3(value domain.BrokerAttachmentArgumentsV3) bool {
	return len(value.IssueKey) <= 64 && domain.ValidJiraIssueKey(value.IssueKey) && validAttachmentDecimalV3(value.AttachmentID)
}

func validAttachmentDecimalV3(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] == '0' {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' {
			return false
		}
	}
	return true
}

func validateAttachmentRequestV3(value domain.BrokerAttachmentRequestV3) error {
	definition, ok := DefinitionV3(value.Operation, value.OperationVersion)
	if value.SchemaVersion != ExecutionSchemaVersionV3 || !ok || !validIdentifier(value.RequestID) || value.Features == nil || !slices.Equal(value.Features, definition.Definition.RequiredFeatures) ||
		!validIdentifier(value.Expect.ExecutionID) || !validIdentifier(value.Expect.ExecutionEpoch) || !validIdentifier(value.Expect.AuthorityRevision) || !validAttachmentArgumentsV3(value.Arguments) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateAttachmentAdmissionRequestV3(value domain.BrokerAttachmentAdmissionRequestV3) error {
	argumentsDigest, err := AttachmentArgumentsSHA256V3(value.Arguments)
	if err != nil || validateContext(value.Context) != nil || value.Context.Backend.Service != "jira" || value.Operation != domain.BrokerOperationJiraAttachmentDownload ||
		value.OperationVersion != AttachmentOperationVersionV3 || !validIdentifier(value.RequestID) || !slices.Equal(value.Features, attachmentFeaturesV3) ||
		value.ArgumentsSHA256 != argumentsDigest || value.DeadlineMillis <= value.Context.ExecutionNotBeforeMillis || value.DeadlineMillis > value.Context.ExecutionExpiresMillis ||
		value.DeadlineMillis > value.Context.GrantExpiresMillis || value.DeadlineMillis > value.Context.CredentialExpiresMillis {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func validateAttachmentAdmissionDecisionV3(value domain.BrokerAttachmentAdmissionDecisionV3, requireDigest bool) error {
	if validateDecisionCore(value.BrokerDecisionCore) != nil || requireDigest && !validDigest(value.DecisionSHA256) || !requireDigest && value.DecisionSHA256 != "" && !validDigest(value.DecisionSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func verifyAttachmentAdmissionDecisionDigestV3(value domain.BrokerAttachmentAdmissionDecisionV3) bool {
	wire := attachmentAdmissionDecisionToWireV3(value)
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV3("admission-decision", wire)
	return err == nil && digest == claimed
}

func validAttachmentMetadataPlanV3(value domain.BrokerAttachmentMetadataPlanV3) bool {
	return validDigest(value.SelectorSHA256) && slices.Equal(value.MetadataFields, attachmentMetadataFieldsV3) &&
		value.Limits == (domain.BrokerPhaseLimits{MaxRequests: 1, MaxResponseBytes: MaxAttachmentMetadataResponseBytesV3})
}

func validAttachmentSnapshotV3(value domain.BrokerJiraAttachmentSnapshotV3, requireEvidence bool) bool {
	if !validAttachmentDecimalV3(value.IssueID) || len(value.IssueKey) > 64 || !domain.ValidJiraIssueKey(value.IssueKey) ||
		len(value.Project) == 0 || len(value.Project) > 64 || !strings.HasPrefix(value.IssueKey, value.Project+"-") || !validBrokerText(value.Project) ||
		len(value.Updated) == 0 || len(value.Updated) > 4<<10 || !validBrokerText(value.Updated) || !validAttachmentDecimalV3(value.AttachmentID) ||
		value.ParentID != value.IssueID || !validAttachmentDecimalV3(value.ParentID) || !validAttachmentFilenameV3(value.Filename) ||
		len(value.MediaType) == 0 || len(value.MediaType) > 255 || !validVisibleASCII(value.MediaType) || len(value.Created) == 0 || len(value.Created) > 4<<10 || !validBrokerText(value.Created) ||
		value.DeclaredSize < 0 || value.DeclaredSize > MaxAttachmentNativeBodyBytesV3 {
		return false
	}
	if !requireEvidence {
		return value.IssueEvidenceSHA256 == "" && value.AttachmentEvidenceSHA256 == "" && value.ProjectionSHA256 == ""
	}
	issue, attachment := attachmentEvidenceDigestsV3(value)
	projection, err := attachmentProjectionDigestV3(value, issue, attachment)
	return err == nil && value.IssueEvidenceSHA256 == issue && value.AttachmentEvidenceSHA256 == attachment && value.ProjectionSHA256 == projection
}

func validAttachmentFilenameV3(value string) bool {
	if len(value) == 0 || len(value) > 255 || !utf8.ValidString(value) || strings.TrimSpace(value) == "" || value == "." || value == ".." {
		return false
	}
	for _, current := range value {
		if current < 0x20 || current == '/' || current == '\\' || current == 0x7f {
			return false
		}
	}
	return true
}

func validVisibleASCII(value string) bool {
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	return value != ""
}

func attachmentEvidenceDigestsV3(value domain.BrokerJiraAttachmentSnapshotV3) (string, string) {
	issue, _ := digestExecutionV3("jira-issue-identity", struct {
		ID      string `json:"id"`
		Key     string `json:"key"`
		Project string `json:"project"`
		Updated string `json:"updated"`
	}{value.IssueID, value.IssueKey, value.Project, value.Updated})
	attachment, _ := digestExecutionV3("jira-attachment-identity", struct {
		ID        string `json:"id"`
		ParentID  string `json:"parent_id"`
		Filename  string `json:"filename"`
		MediaType string `json:"media_type"`
		Created   string `json:"created"`
		Size      int64  `json:"size"`
	}{value.AttachmentID, value.ParentID, value.Filename, value.MediaType, value.Created, value.DeclaredSize})
	return issue, attachment
}

func attachmentProjectionDigestV3(value domain.BrokerJiraAttachmentSnapshotV3, issue, attachment string) (string, error) {
	copy := value
	copy.IssueEvidenceSHA256, copy.AttachmentEvidenceSHA256, copy.ProjectionSHA256 = issue, attachment, ""
	return digestExecutionV3("jira-attachment-projection", attachmentSnapshotToWireV3(copy))
}

func NewAttachmentSnapshotEvidenceV3(value domain.BrokerJiraAttachmentSnapshotV3) (domain.BrokerJiraAttachmentSnapshotV3, error) {
	value.IssueEvidenceSHA256, value.AttachmentEvidenceSHA256, value.ProjectionSHA256 = "", "", ""
	if !validAttachmentSnapshotV3(value, false) {
		return domain.BrokerJiraAttachmentSnapshotV3{}, reject(domain.BrokerReasonMalformed)
	}
	value.IssueEvidenceSHA256, value.AttachmentEvidenceSHA256 = attachmentEvidenceDigestsV3(value)
	value.ProjectionSHA256, _ = attachmentProjectionDigestV3(value, value.IssueEvidenceSHA256, value.AttachmentEvidenceSHA256)
	return value, nil
}

func validAttachmentEffectsV3(values []domain.BrokerAttachmentEffectV3) bool {
	return len(values) == 2 && values[0].Kind == domain.BrokerEffectRead && values[0].ResourceKind == domain.BrokerResourceJiraIssue && slices.Equal(values[0].Fields, []string{"id", "key", "project", "updated"}) &&
		values[1].Kind == domain.BrokerEffectRead && values[1].ResourceKind == domain.BrokerResourceJiraAttachment && slices.Equal(values[1].Fields, []string{"body", "created", "filename", "id", "media_type", "parent_id", "size"})
}

func validAttachmentReleaseCoordinateV3(value domain.BrokerAttachmentReleaseCoordinateV3) bool {
	if value.Index < 0 || value.Index >= MaxAttachmentDataFramesV3 || value.Offset < 0 || value.Offset > MaxAttachmentNativeBodyBytesV3 {
		return false
	}
	return value.Kind == domain.BrokerAttachmentReleaseData && value.Offset == int64(value.Index)*MaxAttachmentDecodedFrameBytesV3 ||
		value.Kind == domain.BrokerAttachmentReleaseTerminal && value.Index == 0 && value.Offset == 0
}

func validAttachmentTerminalFactsV3(value domain.BrokerAttachmentReleaseTerminalFactsV3) bool {
	if value.ChunkCount < 0 || value.ChunkCount > MaxAttachmentDataFramesV3 || value.TotalBytes < 0 || value.TotalBytes > MaxAttachmentNativeBodyBytesV3 || !validDigest(value.WholeSHA256) || !value.EOFProven || !value.Complete {
		return false
	}
	if value.TotalBytes == 0 {
		return value.ChunkCount == 0 && value.WholeSHA256 == AttachmentEmptyBodySHA256V3
	}
	wantChunks := int((value.TotalBytes + MaxAttachmentDecodedFrameBytesV3 - 1) / MaxAttachmentDecodedFrameBytesV3)
	return value.ChunkCount == wantChunks
}

func validAttachmentReleaseFactsV3(value domain.BrokerAttachmentReleaseFactsV3) bool {
	if value.Kind == domain.BrokerAttachmentReleaseTerminal {
		return value.Data == nil && value.Terminal != nil && validAttachmentTerminalFactsV3(*value.Terminal) && value.Terminal.ChunkCount == 0 && value.Terminal.TotalBytes == 0
	}
	if value.Kind != domain.BrokerAttachmentReleaseData || value.Data == nil || value.Terminal != nil {
		return false
	}
	data := value.Data
	if data.Index < 0 || data.Index >= MaxAttachmentDataFramesV3 || data.Offset != int64(data.Index)*MaxAttachmentDecodedFrameBytesV3 || data.DecodedBytes <= 0 || data.DecodedBytes > MaxAttachmentDecodedFrameBytesV3 ||
		data.Offset+data.DecodedBytes > MaxAttachmentNativeBodyBytesV3 || data.CumulativeBytes != data.Offset+data.DecodedBytes || !validDigest(data.PayloadSHA256) ||
		!validDigest(data.CumulativeSHA256) || !validDigest(data.ResourceSHA256) {
		return false
	}
	terminalRequired := data.DecodedBytes < MaxAttachmentDecodedFrameBytesV3 || data.Index == MaxAttachmentDataFramesV3-1
	if terminalRequired && data.Terminal == nil {
		return false
	}
	return data.Terminal == nil || validAttachmentTerminalFactsV3(*data.Terminal) && data.Terminal.ChunkCount == data.Index+1 &&
		data.Terminal.TotalBytes == data.CumulativeBytes && data.Terminal.WholeSHA256 == data.CumulativeSHA256
}
