package brokercontract

import (
	"github.com/isukharev/atl/internal/domain"
)

// MatchRequestContextV1 validates only equality and hard lifetimes. It does
// not authenticate the context or grant the operation.
func MatchRequestContextV1(request domain.BrokerRequest, verified domain.BrokerVerifiedContext, nowMillis, deadlineMillis int64) error {
	if err := validateRequest(request); err != nil {
		return err
	}
	if err := validateContext(verified); err != nil {
		return err
	}
	definition, _ := Definition(request.Operation, request.OperationVersion)
	if !operationBackendMatches(definition, verified) {
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
	if deadlineMillis <= nowMillis || deadlineMillis-nowMillis > domain.BrokerMaxOperationMillis {
		return reject(domain.BrokerReasonDecisionExpired)
	}
	if deadlineMillis > verified.ExecutionExpiresMillis || deadlineMillis > verified.GrantExpiresMillis || deadlineMillis > verified.CredentialExpiresMillis {
		return reject(domain.BrokerReasonDecisionExpired)
	}
	return nil
}

// ValidateAdmissionDecisionForV1 binds a decision to the exact admission
// request and rejects stale, late or overlong positive decisions.
func ValidateAdmissionDecisionForV1(value domain.BrokerAdmissionDecision, request domain.BrokerAdmissionRequest, nowMillis int64) error {
	requestDigest, err := AdmissionRequestSHA256(request)
	if err != nil || validateAdmissionDecision(value, true) != nil || !verifyAdmissionDecisionDigest(value) {
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

func ValidateQualificationDecisionForV1(value domain.BrokerQualificationDecision, request domain.BrokerQualificationRequest, nowMillis int64) error {
	requestDigest, err := QualificationRequestSHA256(request)
	admissionDigest, admissionErr := AdmissionRequestSHA256(request.Admission)
	planDigest, planErr := QualificationPlanSHA256(request.Plan)
	if err != nil || admissionErr != nil || planErr != nil || validateQualificationDecision(value, true) != nil || !verifyQualificationDecisionDigest(value) ||
		value.AdmissionRequestSHA256 != admissionDigest || value.AdmissionDecisionSHA256 != request.AdmissionDecision.DecisionSHA256 || value.PlanSHA256 != planDigest {
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

func ValidateOperationDecisionForV1(value domain.BrokerOperationDecision, request domain.BrokerOperationAuthorizationRequest, nowMillis int64) error {
	admission := request.QualificationRequest.Admission
	requestDigest, err := OperationAuthorizationRequestSHA256(request)
	resourcesDigest, resourcesErr := QualifiedResourcesSHA256(request.QualifiedResources)
	effectsDigest, effectsErr := EffectsSHA256(request.Effects)
	if err != nil || resourcesErr != nil || effectsErr != nil || validateOperationDecision(value, true) != nil || !verifyOperationDecisionDigest(value) ||
		value.QualificationDecisionSHA256 != request.QualificationDecision.DecisionSHA256 || value.Operation != admission.Operation ||
		value.OperationVersion != admission.OperationVersion || value.ArgumentsSHA256 != admission.ArgumentsSHA256 ||
		value.ResourcesSHA256 != resourcesDigest || value.EffectsSHA256 != effectsDigest {
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

func ValidateProposalClearanceForV1(value domain.BrokerProposalClearance, request domain.BrokerProposalAuthorizationRequest, nowMillis int64) error {
	requestDigest, err := ProposalAuthorizationRequestSHA256(request)
	if err != nil || validateProposalClearance(value, true) != nil || !verifyProposalClearanceDigest(value) ||
		value.OperationDecisionSHA256 != request.OperationDecision.DecisionSHA256 || value.ProposalHash != request.ProposalHash ||
		value.NativeCandidateSHA256 != request.NativeCandidateSHA256 || value.VersionEvidenceSHA256 != request.VersionEvidenceSHA256 {
		return reject(domain.BrokerReasonMalformed)
	}
	context := request.OperationRequest.QualificationRequest.Admission.Context
	if err := ValidateOperationDecisionForV1(request.OperationDecision, request.OperationRequest, nowMillis); err != nil {
		return err
	}
	if !decisionCoreMatches(value.BrokerDecisionCore, context, requestDigest) {
		return reject(domain.BrokerReasonStaleAuthority)
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return reject(value.Reason)
	}
	return validateDecisionTime(value.BrokerDecisionCore, context, nowMillis, request.OperationRequest.QualificationRequest.Admission.DeadlineMillis)
}

func decisionCoreMatches(value domain.BrokerDecisionCore, context domain.BrokerVerifiedContext, requestDigest string) bool {
	contextDigest, err := VerifiedContextSHA256(context)
	return err == nil && value.ContextSHA256 == contextDigest && value.RequestSHA256 == requestDigest && value.AuthorityRevision == context.AuthorityRevision
}

func validateDecisionTime(value domain.BrokerDecisionCore, verified domain.BrokerVerifiedContext, nowMillis, operationDeadlineMillis int64) error {
	if err := validateDecisionCore(value); err != nil {
		return err
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
	if nowMillis < value.IssuedAtMillis-domain.BrokerClockAllowanceMillis || nowMillis >= value.ExpiresAtMillis || value.ExpiresAtMillis > operationDeadlineMillis ||
		value.ExpiresAtMillis > verified.ExecutionExpiresMillis || value.ExpiresAtMillis > verified.GrantExpiresMillis || value.ExpiresAtMillis > verified.CredentialExpiresMillis {
		return reject(domain.BrokerReasonDecisionExpired)
	}
	return nil
}

func ValidateProposalAuthorizationRequestV1(value domain.BrokerProposalAuthorizationRequest) error {
	decision := value.OperationDecision
	comment := value.OperationRequest.QualificationRequest.Admission.Arguments.JiraComment
	nativeDigest := ""
	if comment != nil {
		nativeDigest, _ = NativeCandidateSHA256(domain.BrokerOperationJiraCommentApply, comment.NativeBody)
	}
	if decision.Status != domain.BrokerDecisionAllowed || ValidateOperationDecisionForV1(decision, value.OperationRequest, decision.IssuedAtMillis) != nil ||
		decision.Operation != domain.BrokerOperationJiraCommentApply || value.OperationRequest.QualificationRequest.Admission.Operation != domain.BrokerOperationJiraCommentApply ||
		value.ProposalSchemaVersion != 1 || comment == nil || value.ProposalHash != comment.ExpectedProposalHash || value.NativeCandidateSHA256 != nativeDigest ||
		len(value.OperationRequest.QualifiedResources) != 1 || value.VersionEvidenceSHA256 != value.OperationRequest.QualifiedResources[0].VersionEvidence {
		return reject(domain.BrokerReasonProposalClearanceRequired)
	}
	return nil
}
