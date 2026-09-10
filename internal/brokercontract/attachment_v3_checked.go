package brokercontract

import (
	"reflect"

	"github.com/isukharev/atl/internal/domain"
)

// The checked artifacts below are deliberately private and call-scoped. They
// keep canonical wire and binding facts beside the validation that established
// them, so a parent never serializes an ancestor through a second checked walk.
type attachmentAdmissionCheckedV3 struct {
	wire          attachmentAdmissionRequestWireV3
	requestSHA256 string
	context       domain.BrokerVerifiedContext
	contextSHA256 string
	arguments     domain.BrokerAttachmentArgumentsV3
	argumentsSHA  string
	deadline      int64
}

type attachmentQualificationCheckedV3 struct {
	wire          attachmentQualificationEnvelopeWireV3
	requestSHA256 string
	plan          domain.BrokerAttachmentMetadataPlanV3
	planSHA256    string
	context       domain.BrokerVerifiedContext
	contextSHA256 string
	arguments     domain.BrokerAttachmentArgumentsV3
	argumentsSHA  string
	deadline      int64

	admission         *attachmentAdmissionCheckedV3
	admissionDecision domain.BrokerAttachmentAdmissionDecisionV3
	initialOperation  *attachmentOperationCheckedV3
	initialDecision   domain.BrokerAttachmentOperationDecisionV3
}

type attachmentOperationCheckedV3 struct {
	wire          attachmentOperationEnvelopeWireV3
	requestSHA256 string
	binding       domain.BrokerAttachmentOperationDecisionV3
	context       domain.BrokerVerifiedContext
	contextSHA256 string
	deadline      int64

	qualification         *attachmentQualificationCheckedV3
	qualificationDecision domain.BrokerAttachmentQualificationDecisionV3
	snapshot              domain.BrokerJiraAttachmentSnapshotV3
	effects               []domain.BrokerAttachmentEffectV3
}

func buildAttachmentAdmissionCheckedV3(value domain.BrokerAttachmentAdmissionRequestV3) (*attachmentAdmissionCheckedV3, error) {
	if validateAttachmentAdmissionRequestV3(value) != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	wire := attachmentAdmissionRequestToWireV3(value)
	requestSHA, err := digestExecutionV3("admission-request", wire)
	if err != nil {
		return nil, err
	}
	contextSHA, err := VerifiedContextSHA256(value.Context)
	if err != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return &attachmentAdmissionCheckedV3{
		wire: wire, requestSHA256: requestSHA, context: value.Context, contextSHA256: contextSHA,
		arguments: value.Arguments, argumentsSHA: value.ArgumentsSHA256, deadline: value.DeadlineMillis,
	}, nil
}

func decisionCoreMatchesCheckedV3(value domain.BrokerDecisionCore, context domain.BrokerVerifiedContext, contextSHA256, requestSHA256 string) bool {
	return value.ContextSHA256 == contextSHA256 && value.RequestSHA256 == requestSHA256 && value.AuthorityRevision == context.AuthorityRevision
}

func checkAttachmentAdmissionDecisionV3(value domain.BrokerAttachmentAdmissionDecisionV3, request *attachmentAdmissionCheckedV3, nowMillis int64) (attachmentAdmissionDecisionWireV3, error) {
	if validateAttachmentAdmissionDecisionV3(value, true) != nil {
		return attachmentAdmissionDecisionWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	wire := attachmentAdmissionDecisionToWireV3(value)
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV3("admission-decision", wire)
	wire.DecisionSHA256 = claimed
	if err != nil || digest != claimed {
		return attachmentAdmissionDecisionWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	if !decisionCoreMatchesCheckedV3(value.BrokerDecisionCore, request.context, request.contextSHA256, request.requestSHA256) {
		return attachmentAdmissionDecisionWireV3{}, reject(domain.BrokerReasonStaleAuthority)
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return attachmentAdmissionDecisionWireV3{}, reject(value.Reason)
	}
	if err := validateDecisionTime(value.BrokerDecisionCore, request.context, nowMillis, request.deadline); err != nil {
		return attachmentAdmissionDecisionWireV3{}, err
	}
	return wire, nil
}

func buildAttachmentQualificationCheckedV3(value domain.BrokerAttachmentQualificationRequestV3) (*attachmentQualificationCheckedV3, error) {
	present := 0
	for _, exists := range []bool{value.Initial != nil, value.PreOpen != nil, value.Release != nil} {
		if exists {
			present++
		}
	}
	if present != 1 {
		return nil, reject(domain.BrokerReasonMalformed)
	}

	checked := &attachmentQualificationCheckedV3{}
	var payload any
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		if value.Initial == nil {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		admission, err := buildAttachmentAdmissionCheckedV3(value.Initial.Admission)
		if err != nil {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		admissionDecisionWire, err := checkAttachmentAdmissionDecisionV3(value.Initial.AdmissionDecision, admission, value.Initial.AdmissionDecision.IssuedAtMillis)
		if err != nil || !validAttachmentMetadataPlanV3(value.Initial.Plan) || value.Initial.Plan.SelectorSHA256 != admission.argumentsSHA {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		payload = attachmentInitialQualificationWireV3{admission.wire, admissionDecisionWire, attachmentMetadataPlanToWireV3(value.Initial.Plan)}
		checked.plan, checked.context, checked.contextSHA256 = value.Initial.Plan, admission.context, admission.contextSHA256
		checked.arguments, checked.argumentsSHA, checked.deadline = admission.arguments, admission.argumentsSHA, admission.deadline
		checked.admission, checked.admissionDecision = admission, value.Initial.AdmissionDecision
	case domain.BrokerAttachmentQualificationPreOpen:
		if value.PreOpen == nil || value.PreOpen.InitialOperation.Phase != domain.BrokerAttachmentOperationInitial {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		operation, err := buildAttachmentOperationCheckedV3(value.PreOpen.InitialOperation)
		if err != nil {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		decisionWire, err := checkAttachmentOperationDecisionV3(value.PreOpen.InitialOperationDecision, operation, value.PreOpen.InitialOperationDecision.IssuedAtMillis, operation.deadline)
		if err != nil || !validAttachmentMetadataPlanV3(value.PreOpen.Plan) || operation.qualification == nil ||
			!reflect.DeepEqual(value.PreOpen.Plan, operation.qualification.plan) {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		operationBody, err := marshalAttachmentPayloadV3(operation.wire)
		if err != nil {
			return nil, err
		}
		decisionBody, err := marshalAttachmentPayloadV3(decisionWire)
		if err != nil {
			return nil, err
		}
		payload = attachmentPreOpenQualificationWireV3{operationBody, decisionBody, attachmentMetadataPlanToWireV3(value.PreOpen.Plan)}
		checked.plan, checked.context, checked.contextSHA256 = value.PreOpen.Plan, operation.context, operation.contextSHA256
		checked.arguments, checked.argumentsSHA, checked.deadline = operation.qualification.arguments, operation.qualification.argumentsSHA, operation.deadline
		checked.initialOperation, checked.initialDecision = operation, value.PreOpen.InitialOperationDecision
	case domain.BrokerAttachmentQualificationRelease:
		if value.Release == nil || validateContext(value.Release.Context) != nil || value.Release.Context.Backend.Service != "jira" ||
			!validDigest(value.Release.AnchorSHA256) || !validDigest(value.Release.PriorReleaseSHA256) || !validAttachmentReleaseCoordinateV3(value.Release.Coordinate) || !validAttachmentMetadataPlanV3(value.Release.Plan) {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		contextSHA, err := VerifiedContextSHA256(value.Release.Context)
		if err != nil {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		payload = attachmentReleaseQualificationWireV3{
			contextToWire(value.Release.Context), value.Release.AnchorSHA256, value.Release.PriorReleaseSHA256,
			attachmentReleaseCoordinateWireV3{string(value.Release.Coordinate.Kind), value.Release.Coordinate.Index, value.Release.Coordinate.Offset}, attachmentMetadataPlanToWireV3(value.Release.Plan),
		}
		checked.plan, checked.context, checked.contextSHA256 = value.Release.Plan, value.Release.Context, contextSHA
		checked.argumentsSHA = value.Release.Plan.SelectorSHA256
		checked.deadline = min(value.Release.Context.ExecutionExpiresMillis, value.Release.Context.GrantExpiresMillis, value.Release.Context.CredentialExpiresMillis)
	default:
		return nil, reject(domain.BrokerReasonMalformed)
	}

	requestBody, err := marshalAttachmentPayloadV3(payload)
	if err != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	checked.wire = attachmentQualificationEnvelopeWireV3{ExecutionSchemaVersionV3, string(value.Phase), requestBody}
	checked.requestSHA256, err = digestExecutionV3("qualification-request/"+string(value.Phase), checked.wire)
	if err != nil {
		return nil, err
	}
	checked.planSHA256, err = digestExecutionV3("metadata-plan", attachmentMetadataPlanToWireV3(checked.plan))
	if err != nil {
		return nil, err
	}
	return checked, nil
}

func checkAttachmentQualificationDecisionV3(value domain.BrokerAttachmentQualificationDecisionV3, request *attachmentQualificationCheckedV3, nowMillis, operationDeadlineMillis int64) (attachmentQualificationDecisionEnvelopeWireV3, error) {
	wire, err := attachmentQualificationDecisionToWireV3(value, true)
	if err != nil {
		return attachmentQualificationDecisionEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, digestErr := digestExecutionV3("qualification-decision/"+string(value.Phase), wire)
	wire.DecisionSHA256 = claimed
	if digestErr != nil || digest != claimed || value.Phase != domain.BrokerAttachmentQualificationPhaseV3(request.wire.Phase) || value.PlanSHA256 != request.planSHA256 {
		return attachmentQualificationDecisionEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	if !decisionCoreMatchesCheckedV3(value.BrokerDecisionCore, request.context, request.contextSHA256, request.requestSHA256) {
		return attachmentQualificationDecisionEnvelopeWireV3{}, reject(domain.BrokerReasonStaleAuthority)
	}
	if err := request.validateCurrentLineageV3(nowMillis); err != nil {
		return attachmentQualificationDecisionEnvelopeWireV3{}, err
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return attachmentQualificationDecisionEnvelopeWireV3{}, reject(value.Reason)
	}
	if err := validateDecisionTime(value.BrokerDecisionCore, request.context, nowMillis, operationDeadlineMillis); err != nil {
		return attachmentQualificationDecisionEnvelopeWireV3{}, err
	}
	return wire, nil
}

func (checked *attachmentQualificationCheckedV3) validateCurrentLineageV3(nowMillis int64) error {
	if checked.admission != nil {
		if checked.admissionDecision.Status != domain.BrokerDecisionAllowed {
			return reject(checked.admissionDecision.Reason)
		}
		return validateDecisionTime(checked.admissionDecision.BrokerDecisionCore, checked.admission.context, nowMillis, checked.admission.deadline)
	}
	if checked.initialOperation != nil {
		return checked.initialOperation.validateDecisionTemporalV3(checked.initialDecision, nowMillis, checked.initialOperation.deadline)
	}
	return nil
}

func buildAttachmentOperationCheckedV3(value domain.BrokerAttachmentOperationAuthorizationRequestV3) (*attachmentOperationCheckedV3, error) {
	present := 0
	if value.Qualified != nil {
		present++
	}
	if value.Release != nil {
		present++
	}
	if present != 1 {
		return nil, reject(domain.BrokerReasonMalformed)
	}

	checked := &attachmentOperationCheckedV3{}
	checked.binding = domain.BrokerAttachmentOperationDecisionV3{Phase: value.Phase, Operation: domain.BrokerOperationJiraAttachmentDownload, OperationVersion: AttachmentOperationVersionV3}
	var payload any
	switch value.Phase {
	case domain.BrokerAttachmentOperationInitial, domain.BrokerAttachmentOperationBodyDispatch:
		wantQualification := domain.BrokerAttachmentQualificationInitial
		if value.Phase == domain.BrokerAttachmentOperationBodyDispatch {
			wantQualification = domain.BrokerAttachmentQualificationPreOpen
		}
		if value.Qualified == nil || value.Qualified.QualificationRequest.Phase != wantQualification {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		qualification, err := buildAttachmentQualificationCheckedV3(value.Qualified.QualificationRequest)
		if err != nil {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		decisionWire, err := checkAttachmentQualificationDecisionV3(value.Qualified.QualificationDecision, qualification, value.Qualified.QualificationDecision.IssuedAtMillis, qualification.deadline)
		if err != nil || !validAttachmentSnapshotV3(value.Qualified.Snapshot, true) || !validAttachmentEffectsV3(value.Qualified.Effects) ||
			!attachmentSnapshotMatchesArgumentsV3(value.Qualified.Snapshot, qualification.arguments) {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		if value.Phase == domain.BrokerAttachmentOperationBodyDispatch {
			if qualification.initialOperation == nil || !reflect.DeepEqual(qualification.initialOperation.snapshot, value.Qualified.Snapshot) ||
				!reflect.DeepEqual(qualification.initialOperation.effects, value.Qualified.Effects) {
				return nil, reject(domain.BrokerReasonMalformed)
			}
		}
		qualificationBody, err := marshalAttachmentPayloadV3(qualification.wire)
		if err != nil {
			return nil, err
		}
		decisionBody, err := marshalAttachmentPayloadV3(decisionWire)
		if err != nil {
			return nil, err
		}
		effectsWire := make([]attachmentEffectWireV3, len(value.Qualified.Effects))
		for index, effect := range value.Qualified.Effects {
			effectsWire[index] = attachmentEffectToWireV3(effect)
		}
		snapshotWire := attachmentSnapshotToWireV3(value.Qualified.Snapshot)
		payload = attachmentQualifiedOperationWireV3{qualificationBody, decisionBody, snapshotWire, effectsWire}
		checked.qualification, checked.qualificationDecision = qualification, value.Qualified.QualificationDecision
		checked.context, checked.contextSHA256, checked.deadline = qualification.context, qualification.contextSHA256, qualification.deadline
		checked.snapshot, checked.effects = value.Qualified.Snapshot, append([]domain.BrokerAttachmentEffectV3(nil), value.Qualified.Effects...)
		checked.binding.QualificationDecisionSHA256 = value.Qualified.QualificationDecision.DecisionSHA256
		checked.binding.ArgumentsSHA256 = qualification.argumentsSHA
		checked.binding.ResourcesSHA256, err = digestExecutionV3("qualified-resources", snapshotWire)
		if err != nil {
			return nil, err
		}
		checked.binding.EffectsSHA256, err = digestExecutionV3("effects", effectsWire)
		if err != nil {
			return nil, err
		}
	case domain.BrokerAttachmentOperationRelease:
		if value.Release == nil || value.Release.QualificationRequest.Phase != domain.BrokerAttachmentQualificationRelease {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		qualification, err := buildAttachmentQualificationCheckedV3(value.Release.QualificationRequest)
		if err != nil {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		// Historical release qualification uses the same execution-expiry bound
		// as the original structural validator. Public operation validation below
		// substitutes its caller-provided release deadline only for current use.
		decisionWire, err := checkAttachmentQualificationDecisionV3(value.Release.QualificationDecision, qualification, value.Release.QualificationDecision.IssuedAtMillis, value.Release.QualificationRequest.Release.Context.ExecutionExpiresMillis)
		if err != nil || !validDigest(value.Release.AnchorSHA256) || value.Release.AnchorSHA256 != value.Release.QualificationRequest.Release.AnchorSHA256 ||
			!validDigest(value.Release.PriorReleaseSHA256) || value.Release.PriorReleaseSHA256 != value.Release.QualificationRequest.Release.PriorReleaseSHA256 ||
			!validDigest(value.Release.SnapshotSHA256) || !validDigest(value.Release.ManifestCoreSHA256) || !validAttachmentReleaseFactsV3(value.Release.Facts) ||
			!releaseCoordinateMatchesFactsV3(value.Release.QualificationRequest.Release.Coordinate, value.Release.Facts) {
			return nil, reject(domain.BrokerReasonMalformed)
		}
		factsWire, err := attachmentReleaseFactsToWireV3(value.Release.Facts)
		if err != nil {
			return nil, err
		}
		qualificationBody, err := marshalAttachmentPayloadV3(qualification.wire)
		if err != nil {
			return nil, err
		}
		decisionBody, err := marshalAttachmentPayloadV3(decisionWire)
		if err != nil {
			return nil, err
		}
		payload = attachmentReleaseOperationWireV3{qualificationBody, decisionBody, value.Release.AnchorSHA256, value.Release.PriorReleaseSHA256, value.Release.SnapshotSHA256, value.Release.ManifestCoreSHA256, factsWire}
		checked.qualification, checked.qualificationDecision = qualification, value.Release.QualificationDecision
		checked.context, checked.contextSHA256, checked.deadline = qualification.context, qualification.contextSHA256, qualification.deadline
		checked.binding.QualificationDecisionSHA256 = value.Release.QualificationDecision.DecisionSHA256
		checked.binding.AnchorSHA256 = value.Release.AnchorSHA256
		checked.binding.PriorReleaseSHA256 = value.Release.PriorReleaseSHA256
		checked.binding.ManifestCoreSHA256 = value.Release.ManifestCoreSHA256
		checked.binding.ReleaseFactsSHA256, err = digestExecutionV3("release-facts-v1", factsWire)
		if err != nil {
			return nil, err
		}
	default:
		return nil, reject(domain.BrokerReasonMalformed)
	}

	requestBody, err := marshalAttachmentPayloadV3(payload)
	if err != nil {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	checked.wire = attachmentOperationEnvelopeWireV3{ExecutionSchemaVersionV3, string(value.Phase), requestBody}
	checked.requestSHA256, err = digestExecutionV3("operation-request/"+string(value.Phase), checked.wire)
	if err != nil {
		return nil, err
	}
	return checked, nil
}

func checkAttachmentOperationDecisionV3(value domain.BrokerAttachmentOperationDecisionV3, request *attachmentOperationCheckedV3, nowMillis, operationDeadlineMillis int64) (attachmentOperationDecisionEnvelopeWireV3, error) {
	wire, err := attachmentOperationDecisionToWireV3(value, true)
	if err != nil {
		return attachmentOperationDecisionEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, digestErr := digestExecutionV3("operation-decision/"+string(value.Phase), wire)
	wire.DecisionSHA256 = claimed
	if digestErr != nil || digest != claimed || !attachmentOperationDecisionMatchesBindingV3(value, request.binding) {
		return attachmentOperationDecisionEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	if !decisionCoreMatchesCheckedV3(value.BrokerDecisionCore, request.context, request.contextSHA256, request.requestSHA256) {
		return attachmentOperationDecisionEnvelopeWireV3{}, reject(domain.BrokerReasonStaleAuthority)
	}
	if err := request.validateCurrentLineageV3(nowMillis, operationDeadlineMillis); err != nil {
		return attachmentOperationDecisionEnvelopeWireV3{}, err
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return attachmentOperationDecisionEnvelopeWireV3{}, reject(value.Reason)
	}
	if err := validateDecisionTime(value.BrokerDecisionCore, request.context, nowMillis, operationDeadlineMillis); err != nil {
		return attachmentOperationDecisionEnvelopeWireV3{}, err
	}
	return wire, nil
}

func (checked *attachmentOperationCheckedV3) validateCurrentLineageV3(nowMillis, operationDeadlineMillis int64) error {
	qualificationDeadline := checked.qualification.deadline
	if domain.BrokerAttachmentOperationPhaseV3(checked.wire.Phase) == domain.BrokerAttachmentOperationRelease {
		qualificationDeadline = operationDeadlineMillis
	}
	return checked.qualification.validateDecisionTemporalV3(checked.qualificationDecision, nowMillis, qualificationDeadline)
}

func (checked *attachmentQualificationCheckedV3) validateDecisionTemporalV3(value domain.BrokerAttachmentQualificationDecisionV3, nowMillis, operationDeadlineMillis int64) error {
	if err := checked.validateCurrentLineageV3(nowMillis); err != nil {
		return err
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return reject(value.Reason)
	}
	return validateDecisionTime(value.BrokerDecisionCore, checked.context, nowMillis, operationDeadlineMillis)
}

func (checked *attachmentOperationCheckedV3) validateDecisionTemporalV3(value domain.BrokerAttachmentOperationDecisionV3, nowMillis, operationDeadlineMillis int64) error {
	if err := checked.validateCurrentLineageV3(nowMillis, operationDeadlineMillis); err != nil {
		return err
	}
	if value.Status != domain.BrokerDecisionAllowed {
		return reject(value.Reason)
	}
	return validateDecisionTime(value.BrokerDecisionCore, checked.context, nowMillis, operationDeadlineMillis)
}
