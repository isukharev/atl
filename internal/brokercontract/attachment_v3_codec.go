package brokercontract

import (
	"bytes"
	"reflect"
	"slices"

	"github.com/isukharev/atl/internal/domain"
)

func attachmentQualificationRequestToWireV3(value domain.BrokerAttachmentQualificationRequestV3) (attachmentQualificationEnvelopeWireV3, error) {
	if validateAttachmentQualificationRequestV3(value) != nil {
		return attachmentQualificationEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	var payload any
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		payload = attachmentInitialQualificationWireV3{
			attachmentAdmissionRequestToWireV3(value.Initial.Admission), attachmentAdmissionDecisionToWireV3(value.Initial.AdmissionDecision), attachmentMetadataPlanToWireV3(value.Initial.Plan),
		}
	case domain.BrokerAttachmentQualificationPreOpen:
		operation, err := attachmentOperationRequestToWireV3(value.PreOpen.InitialOperation)
		if err != nil {
			return attachmentQualificationEnvelopeWireV3{}, err
		}
		decision, err := attachmentOperationDecisionToWireV3(value.PreOpen.InitialOperationDecision, true)
		if err != nil {
			return attachmentQualificationEnvelopeWireV3{}, err
		}
		operationBody, err := marshalAttachmentPayloadV3(operation)
		if err != nil {
			return attachmentQualificationEnvelopeWireV3{}, err
		}
		decisionBody, err := marshalAttachmentPayloadV3(decision)
		if err != nil {
			return attachmentQualificationEnvelopeWireV3{}, err
		}
		payload = attachmentPreOpenQualificationWireV3{operationBody, decisionBody, attachmentMetadataPlanToWireV3(value.PreOpen.Plan)}
	case domain.BrokerAttachmentQualificationRelease:
		payload = attachmentReleaseQualificationWireV3{
			contextToWire(value.Release.Context), value.Release.AnchorSHA256, value.Release.PriorReleaseSHA256,
			attachmentReleaseCoordinateWireV3{string(value.Release.Coordinate.Kind), value.Release.Coordinate.Index, value.Release.Coordinate.Offset}, attachmentMetadataPlanToWireV3(value.Release.Plan),
		}
	default:
		return attachmentQualificationEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	body, err := marshalAttachmentPayloadV3(payload)
	if err != nil {
		return attachmentQualificationEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	return attachmentQualificationEnvelopeWireV3{ExecutionSchemaVersionV3, string(value.Phase), body}, nil
}

func attachmentQualificationRequestFromWireV3(envelope attachmentQualificationEnvelopeWireV3) (domain.BrokerAttachmentQualificationRequestV3, error) {
	value := domain.BrokerAttachmentQualificationRequestV3{Phase: domain.BrokerAttachmentQualificationPhaseV3(envelope.Phase)}
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		var wire attachmentInitialQualificationWireV3
		if !decodeAttachmentV3(envelope.Request, MaxAttachmentAuthorityEnvelopeBytesV3, &wire) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		value.Initial = &domain.BrokerAttachmentInitialQualificationV3{
			Admission: attachmentAdmissionRequestFromWireV3(wire.Admission), AdmissionDecision: attachmentAdmissionDecisionFromWireV3(wire.AdmissionDecision), Plan: attachmentMetadataPlanFromWireV3(wire.Plan),
		}
	case domain.BrokerAttachmentQualificationPreOpen:
		var wire attachmentPreOpenQualificationWireV3
		if !decodeAttachmentV3(envelope.Request, MaxAttachmentAuthorityEnvelopeBytesV3, &wire) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		var operationEnvelope attachmentOperationEnvelopeWireV3
		if !decodeAttachmentV3(wire.InitialOperation, MaxAttachmentAuthorityEnvelopeBytesV3, &operationEnvelope) || operationEnvelope.SchemaVersion != ExecutionSchemaVersionV3 || operationEnvelope.Phase != string(domain.BrokerAttachmentOperationInitial) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		operation, err := attachmentOperationRequestFromWireV3(operationEnvelope)
		if err != nil {
			return value, err
		}
		var decisionEnvelope attachmentOperationDecisionEnvelopeWireV3
		if !decodeAttachmentV3(wire.InitialOperationDecision, MaxAttachmentAuthorityCallBytesV3, &decisionEnvelope) || decisionEnvelope.SchemaVersion != ExecutionSchemaVersionV3 || decisionEnvelope.Phase != string(domain.BrokerAttachmentOperationInitial) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		decision, err := attachmentOperationDecisionFromWireV3(decisionEnvelope)
		if err != nil {
			return value, err
		}
		if !verifyAttachmentOperationDecisionDigestV3(decision) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		value.PreOpen = &domain.BrokerAttachmentPreOpenQualificationV3{InitialOperation: operation, InitialOperationDecision: decision, Plan: attachmentMetadataPlanFromWireV3(wire.Plan)}
	case domain.BrokerAttachmentQualificationRelease:
		var wire attachmentReleaseQualificationWireV3
		if !decodeAttachmentV3(envelope.Request, MaxAttachmentAuthorityEnvelopeBytesV3, &wire) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		value.Release = &domain.BrokerAttachmentReleaseQualificationV3{
			Context: contextFromWire(wire.Context), AnchorSHA256: wire.AnchorSHA256, PriorReleaseSHA256: wire.PriorReleaseSHA256,
			Coordinate: domain.BrokerAttachmentReleaseCoordinateV3{Kind: domain.BrokerAttachmentReleaseKindV3(wire.Coordinate.Kind), Index: wire.Coordinate.Index, Offset: wire.Coordinate.Offset}, Plan: attachmentMetadataPlanFromWireV3(wire.Plan),
		}
	default:
		return value, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func validateAttachmentQualificationRequestV3(value domain.BrokerAttachmentQualificationRequestV3) error {
	present := 0
	for _, exists := range []bool{value.Initial != nil, value.PreOpen != nil, value.Release != nil} {
		if exists {
			present++
		}
	}
	if present != 1 {
		return reject(domain.BrokerReasonMalformed)
	}
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		if value.Initial == nil || validateAttachmentAdmissionRequestV3(value.Initial.Admission) != nil ||
			ValidateAttachmentAdmissionDecisionV3(value.Initial.AdmissionDecision, value.Initial.Admission, value.Initial.AdmissionDecision.IssuedAtMillis) != nil || !validAttachmentMetadataPlanV3(value.Initial.Plan) ||
			value.Initial.Plan.SelectorSHA256 != value.Initial.Admission.ArgumentsSHA256 {
			return reject(domain.BrokerReasonMalformed)
		}
	case domain.BrokerAttachmentQualificationPreOpen:
		if value.PreOpen == nil || value.PreOpen.InitialOperation.Phase != domain.BrokerAttachmentOperationInitial ||
			validateAttachmentOperationRequestV3(value.PreOpen.InitialOperation) != nil ||
			ValidateAttachmentOperationDecisionV3(value.PreOpen.InitialOperationDecision, value.PreOpen.InitialOperation, value.PreOpen.InitialOperationDecision.IssuedAtMillis, attachmentOperationDeadlineV3(value.PreOpen.InitialOperation)) != nil ||
			!validAttachmentMetadataPlanV3(value.PreOpen.Plan) || !reflect.DeepEqual(value.PreOpen.Plan, value.PreOpen.InitialOperation.Qualified.QualificationRequest.Initial.Plan) {
			return reject(domain.BrokerReasonMalformed)
		}
	case domain.BrokerAttachmentQualificationRelease:
		if value.Release == nil || validateContext(value.Release.Context) != nil || value.Release.Context.Backend.Service != "jira" ||
			!validDigest(value.Release.AnchorSHA256) || !validDigest(value.Release.PriorReleaseSHA256) || !validAttachmentReleaseCoordinateV3(value.Release.Coordinate) || !validAttachmentMetadataPlanV3(value.Release.Plan) {
			return reject(domain.BrokerReasonMalformed)
		}
	default:
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func attachmentQualificationPlanContextV3(value domain.BrokerAttachmentQualificationRequestV3, nowMillis int64) (domain.BrokerAttachmentMetadataPlanV3, domain.BrokerVerifiedContext, bool) {
	if validateAttachmentQualificationRequestV3(value) != nil {
		return domain.BrokerAttachmentMetadataPlanV3{}, domain.BrokerVerifiedContext{}, false
	}
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		if ValidateAttachmentAdmissionDecisionV3(value.Initial.AdmissionDecision, value.Initial.Admission, nowMillis) != nil {
			return domain.BrokerAttachmentMetadataPlanV3{}, domain.BrokerVerifiedContext{}, false
		}
		return value.Initial.Plan, value.Initial.Admission.Context, true
	case domain.BrokerAttachmentQualificationPreOpen:
		if ValidateAttachmentOperationDecisionV3(value.PreOpen.InitialOperationDecision, value.PreOpen.InitialOperation, nowMillis, attachmentOperationDeadlineV3(value.PreOpen.InitialOperation)) != nil {
			return domain.BrokerAttachmentMetadataPlanV3{}, domain.BrokerVerifiedContext{}, false
		}
		return value.PreOpen.Plan, attachmentOperationContextV3(value.PreOpen.InitialOperation), true
	case domain.BrokerAttachmentQualificationRelease:
		return value.Release.Plan, value.Release.Context, true
	default:
		return domain.BrokerAttachmentMetadataPlanV3{}, domain.BrokerVerifiedContext{}, false
	}
}

func attachmentQualificationPlanAndContextV3(value domain.BrokerAttachmentQualificationRequestV3) (domain.BrokerAttachmentMetadataPlanV3, domain.BrokerVerifiedContext) {
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		return value.Initial.Plan, value.Initial.Admission.Context
	case domain.BrokerAttachmentQualificationPreOpen:
		return value.PreOpen.Plan, attachmentOperationContextV3(value.PreOpen.InitialOperation)
	case domain.BrokerAttachmentQualificationRelease:
		return value.Release.Plan, value.Release.Context
	default:
		return domain.BrokerAttachmentMetadataPlanV3{}, domain.BrokerVerifiedContext{}
	}
}

func attachmentQualificationLineageErrorV3(value domain.BrokerAttachmentQualificationRequestV3, nowMillis int64) error {
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		return ValidateAttachmentAdmissionDecisionV3(value.Initial.AdmissionDecision, value.Initial.Admission, nowMillis)
	case domain.BrokerAttachmentQualificationPreOpen:
		return ValidateAttachmentOperationDecisionV3(value.PreOpen.InitialOperationDecision, value.PreOpen.InitialOperation, nowMillis, attachmentOperationDeadlineV3(value.PreOpen.InitialOperation))
	case domain.BrokerAttachmentQualificationRelease:
		return nil
	default:
		return reject(domain.BrokerReasonMalformed)
	}
}

func attachmentQualificationDecisionToWireV3(value domain.BrokerAttachmentQualificationDecisionV3, requireDigest bool) (attachmentQualificationDecisionEnvelopeWireV3, error) {
	if !validAttachmentQualificationPhaseV3(value.Phase) || validateDecisionCore(value.BrokerDecisionCore) != nil || !validDigest(value.PlanSHA256) ||
		requireDigest && !validDigest(value.DecisionSHA256) || !requireDigest && value.DecisionSHA256 != "" && !validDigest(value.DecisionSHA256) {
		return attachmentQualificationDecisionEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	binding, err := marshalAttachmentPayloadV3(attachmentQualificationDecisionBindingWireV3{value.PlanSHA256})
	if err != nil {
		return attachmentQualificationDecisionEnvelopeWireV3{}, err
	}
	return attachmentQualificationDecisionEnvelopeWireV3{ExecutionSchemaVersionV3, string(value.Phase), decisionCoreToWire(value.BrokerDecisionCore), binding, value.DecisionSHA256}, nil
}

func attachmentQualificationDecisionFromWireV3(wire attachmentQualificationDecisionEnvelopeWireV3) (domain.BrokerAttachmentQualificationDecisionV3, error) {
	phase := domain.BrokerAttachmentQualificationPhaseV3(wire.Phase)
	var binding attachmentQualificationDecisionBindingWireV3
	if !validAttachmentQualificationPhaseV3(phase) || !decodeAttachmentV3(wire.Binding, MaxAttachmentAuthorityCallBytesV3, &binding) {
		return domain.BrokerAttachmentQualificationDecisionV3{}, reject(domain.BrokerReasonMalformed)
	}
	value := domain.BrokerAttachmentQualificationDecisionV3{Phase: phase, BrokerDecisionCore: decisionCoreFromWire(wire.Decision), PlanSHA256: binding.PlanSHA256, DecisionSHA256: wire.DecisionSHA256}
	if _, err := attachmentQualificationDecisionToWireV3(value, true); err != nil {
		return domain.BrokerAttachmentQualificationDecisionV3{}, err
	}
	return value, nil
}

func verifyAttachmentQualificationDecisionDigestV3(value domain.BrokerAttachmentQualificationDecisionV3) bool {
	wire, err := attachmentQualificationDecisionToWireV3(value, true)
	if err != nil {
		return false
	}
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV3("qualification-decision/"+string(value.Phase), wire)
	return err == nil && digest == claimed
}

func validAttachmentQualificationPhaseV3(value domain.BrokerAttachmentQualificationPhaseV3) bool {
	return value == domain.BrokerAttachmentQualificationInitial || value == domain.BrokerAttachmentQualificationPreOpen || value == domain.BrokerAttachmentQualificationRelease
}

func attachmentOperationRequestToWireV3(value domain.BrokerAttachmentOperationAuthorizationRequestV3) (attachmentOperationEnvelopeWireV3, error) {
	if validateAttachmentOperationRequestV3(value) != nil {
		return attachmentOperationEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	var payload any
	switch value.Phase {
	case domain.BrokerAttachmentOperationInitial, domain.BrokerAttachmentOperationBodyDispatch:
		qualification, err := attachmentQualificationRequestToWireV3(value.Qualified.QualificationRequest)
		if err != nil {
			return attachmentOperationEnvelopeWireV3{}, err
		}
		decision, err := attachmentQualificationDecisionToWireV3(value.Qualified.QualificationDecision, true)
		if err != nil {
			return attachmentOperationEnvelopeWireV3{}, err
		}
		qualificationBody, err := marshalAttachmentPayloadV3(qualification)
		if err != nil {
			return attachmentOperationEnvelopeWireV3{}, err
		}
		decisionBody, err := marshalAttachmentPayloadV3(decision)
		if err != nil {
			return attachmentOperationEnvelopeWireV3{}, err
		}
		effects := make([]attachmentEffectWireV3, len(value.Qualified.Effects))
		for index, effect := range value.Qualified.Effects {
			effects[index] = attachmentEffectToWireV3(effect)
		}
		payload = attachmentQualifiedOperationWireV3{qualificationBody, decisionBody, attachmentSnapshotToWireV3(value.Qualified.Snapshot), effects}
	case domain.BrokerAttachmentOperationRelease:
		qualification, err := attachmentQualificationRequestToWireV3(value.Release.QualificationRequest)
		if err != nil {
			return attachmentOperationEnvelopeWireV3{}, err
		}
		decision, err := attachmentQualificationDecisionToWireV3(value.Release.QualificationDecision, true)
		if err != nil {
			return attachmentOperationEnvelopeWireV3{}, err
		}
		facts, err := attachmentReleaseFactsToWireV3(value.Release.Facts)
		if err != nil {
			return attachmentOperationEnvelopeWireV3{}, err
		}
		qualificationBody, err := marshalAttachmentPayloadV3(qualification)
		if err != nil {
			return attachmentOperationEnvelopeWireV3{}, err
		}
		decisionBody, err := marshalAttachmentPayloadV3(decision)
		if err != nil {
			return attachmentOperationEnvelopeWireV3{}, err
		}
		payload = attachmentReleaseOperationWireV3{qualificationBody, decisionBody, value.Release.AnchorSHA256, value.Release.PriorReleaseSHA256, value.Release.SnapshotSHA256, value.Release.ManifestCoreSHA256, facts}
	default:
		return attachmentOperationEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	body, err := marshalAttachmentPayloadV3(payload)
	if err != nil {
		return attachmentOperationEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	return attachmentOperationEnvelopeWireV3{ExecutionSchemaVersionV3, string(value.Phase), body}, nil
}

func attachmentOperationRequestFromWireV3(envelope attachmentOperationEnvelopeWireV3) (domain.BrokerAttachmentOperationAuthorizationRequestV3, error) {
	value := domain.BrokerAttachmentOperationAuthorizationRequestV3{Phase: domain.BrokerAttachmentOperationPhaseV3(envelope.Phase)}
	switch value.Phase {
	case domain.BrokerAttachmentOperationInitial, domain.BrokerAttachmentOperationBodyDispatch:
		var wire attachmentQualifiedOperationWireV3
		if !decodeAttachmentV3(envelope.Request, MaxAttachmentAuthorityEnvelopeBytesV3, &wire) || wire.Effects == nil {
			return value, reject(domain.BrokerReasonMalformed)
		}
		wantQualification := domain.BrokerAttachmentQualificationInitial
		if value.Phase == domain.BrokerAttachmentOperationBodyDispatch {
			wantQualification = domain.BrokerAttachmentQualificationPreOpen
		}
		var qualificationEnvelope attachmentQualificationEnvelopeWireV3
		if !decodeAttachmentV3(wire.QualificationRequest, MaxAttachmentAuthorityEnvelopeBytesV3, &qualificationEnvelope) || qualificationEnvelope.SchemaVersion != ExecutionSchemaVersionV3 || qualificationEnvelope.Phase != string(wantQualification) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		qualification, err := attachmentQualificationRequestFromWireV3(qualificationEnvelope)
		if err != nil {
			return value, err
		}
		var decisionEnvelope attachmentQualificationDecisionEnvelopeWireV3
		if !decodeAttachmentV3(wire.QualificationDecision, MaxAttachmentAuthorityCallBytesV3, &decisionEnvelope) || decisionEnvelope.SchemaVersion != ExecutionSchemaVersionV3 || decisionEnvelope.Phase != string(wantQualification) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		decision, err := attachmentQualificationDecisionFromWireV3(decisionEnvelope)
		if err != nil {
			return value, err
		}
		if !verifyAttachmentQualificationDecisionDigestV3(decision) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		effects := make([]domain.BrokerAttachmentEffectV3, len(wire.Effects))
		for index, effect := range wire.Effects {
			effects[index] = attachmentEffectFromWireV3(effect)
		}
		value.Qualified = &domain.BrokerAttachmentQualifiedOperationV3{QualificationRequest: qualification, QualificationDecision: decision, Snapshot: attachmentSnapshotFromWireV3(wire.Snapshot), Effects: effects}
	case domain.BrokerAttachmentOperationRelease:
		var wire attachmentReleaseOperationWireV3
		if !decodeAttachmentV3(envelope.Request, MaxAttachmentAuthorityEnvelopeBytesV3, &wire) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		var qualificationEnvelope attachmentQualificationEnvelopeWireV3
		if !decodeAttachmentV3(wire.QualificationRequest, MaxAttachmentAuthorityEnvelopeBytesV3, &qualificationEnvelope) || qualificationEnvelope.SchemaVersion != ExecutionSchemaVersionV3 || qualificationEnvelope.Phase != string(domain.BrokerAttachmentQualificationRelease) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		qualification, err := attachmentQualificationRequestFromWireV3(qualificationEnvelope)
		if err != nil {
			return value, err
		}
		var decisionEnvelope attachmentQualificationDecisionEnvelopeWireV3
		if !decodeAttachmentV3(wire.QualificationDecision, MaxAttachmentAuthorityCallBytesV3, &decisionEnvelope) || decisionEnvelope.SchemaVersion != ExecutionSchemaVersionV3 || decisionEnvelope.Phase != string(domain.BrokerAttachmentQualificationRelease) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		decision, err := attachmentQualificationDecisionFromWireV3(decisionEnvelope)
		if err != nil {
			return value, err
		}
		if !verifyAttachmentQualificationDecisionDigestV3(decision) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		facts, err := attachmentReleaseFactsFromWireV3(wire.Facts)
		if err != nil {
			return value, err
		}
		value.Release = &domain.BrokerAttachmentReleaseOperationV3{QualificationRequest: qualification, QualificationDecision: decision, AnchorSHA256: wire.AnchorSHA256, PriorReleaseSHA256: wire.PriorReleaseSHA256, SnapshotSHA256: wire.SnapshotSHA256, ManifestCoreSHA256: wire.ManifestCoreSHA256, Facts: facts}
	default:
		return value, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func validateAttachmentOperationRequestV3(value domain.BrokerAttachmentOperationAuthorizationRequestV3) error {
	present := 0
	if value.Qualified != nil {
		present++
	}
	if value.Release != nil {
		present++
	}
	if present != 1 {
		return reject(domain.BrokerReasonMalformed)
	}
	switch value.Phase {
	case domain.BrokerAttachmentOperationInitial, domain.BrokerAttachmentOperationBodyDispatch:
		wantQualification := domain.BrokerAttachmentQualificationInitial
		if value.Phase == domain.BrokerAttachmentOperationBodyDispatch {
			wantQualification = domain.BrokerAttachmentQualificationPreOpen
		}
		if value.Qualified == nil || value.Qualified.QualificationRequest.Phase != wantQualification || validateAttachmentQualificationRequestV3(value.Qualified.QualificationRequest) != nil ||
			ValidateAttachmentQualificationDecisionV3(value.Qualified.QualificationDecision, value.Qualified.QualificationRequest, value.Qualified.QualificationDecision.IssuedAtMillis, attachmentQualificationDeadlineV3(value.Qualified.QualificationRequest)) != nil ||
			!validAttachmentSnapshotV3(value.Qualified.Snapshot, true) || !validAttachmentEffectsV3(value.Qualified.Effects) || !attachmentSnapshotMatchesArgumentsV3(value.Qualified.Snapshot, attachmentQualificationArgumentsV3(value.Qualified.QualificationRequest)) {
			return reject(domain.BrokerReasonMalformed)
		}
		if value.Phase == domain.BrokerAttachmentOperationBodyDispatch {
			initial := value.Qualified.QualificationRequest.PreOpen.InitialOperation.Qualified
			if initial == nil || !reflect.DeepEqual(initial.Snapshot, value.Qualified.Snapshot) || !reflect.DeepEqual(initial.Effects, value.Qualified.Effects) {
				return reject(domain.BrokerReasonMalformed)
			}
		}
	case domain.BrokerAttachmentOperationRelease:
		if value.Release == nil || value.Release.QualificationRequest.Phase != domain.BrokerAttachmentQualificationRelease || validateAttachmentQualificationRequestV3(value.Release.QualificationRequest) != nil ||
			ValidateAttachmentQualificationDecisionV3(value.Release.QualificationDecision, value.Release.QualificationRequest, value.Release.QualificationDecision.IssuedAtMillis, value.Release.QualificationRequest.Release.Context.ExecutionExpiresMillis) != nil ||
			!validDigest(value.Release.AnchorSHA256) || value.Release.AnchorSHA256 != value.Release.QualificationRequest.Release.AnchorSHA256 ||
			!validDigest(value.Release.PriorReleaseSHA256) || value.Release.PriorReleaseSHA256 != value.Release.QualificationRequest.Release.PriorReleaseSHA256 ||
			!validDigest(value.Release.SnapshotSHA256) || !validDigest(value.Release.ManifestCoreSHA256) || !validAttachmentReleaseFactsV3(value.Release.Facts) ||
			!releaseCoordinateMatchesFactsV3(value.Release.QualificationRequest.Release.Coordinate, value.Release.Facts) {
			return reject(domain.BrokerReasonMalformed)
		}
	default:
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func attachmentOperationDecisionBindingV3(request domain.BrokerAttachmentOperationAuthorizationRequestV3) (domain.BrokerAttachmentOperationDecisionV3, domain.BrokerVerifiedContext, error) {
	if validateAttachmentOperationRequestV3(request) != nil {
		return domain.BrokerAttachmentOperationDecisionV3{}, domain.BrokerVerifiedContext{}, reject(domain.BrokerReasonMalformed)
	}
	binding := domain.BrokerAttachmentOperationDecisionV3{Phase: request.Phase, Operation: domain.BrokerOperationJiraAttachmentDownload, OperationVersion: AttachmentOperationVersionV3}
	if request.Qualified != nil {
		binding.QualificationDecisionSHA256 = request.Qualified.QualificationDecision.DecisionSHA256
		arguments := attachmentQualificationArgumentsV3(request.Qualified.QualificationRequest)
		binding.ArgumentsSHA256, _ = AttachmentArgumentsSHA256V3(arguments)
		binding.ResourcesSHA256, _ = AttachmentResourcesSHA256V3(request.Qualified.Snapshot)
		binding.EffectsSHA256, _ = AttachmentEffectsSHA256V3(request.Qualified.Effects)
		return binding, attachmentOperationContextV3(request), nil
	}
	binding.QualificationDecisionSHA256 = request.Release.QualificationDecision.DecisionSHA256
	binding.AnchorSHA256 = request.Release.AnchorSHA256
	binding.PriorReleaseSHA256 = request.Release.PriorReleaseSHA256
	binding.ManifestCoreSHA256 = request.Release.ManifestCoreSHA256
	binding.ReleaseFactsSHA256, _ = AttachmentReleaseFactsSHA256V3(request.Release.Facts)
	return binding, request.Release.QualificationRequest.Release.Context, nil
}

func attachmentOperationDecisionMatchesBindingV3(value, binding domain.BrokerAttachmentOperationDecisionV3) bool {
	return value.Phase == binding.Phase && value.QualificationDecisionSHA256 == binding.QualificationDecisionSHA256 && value.Operation == binding.Operation && value.OperationVersion == binding.OperationVersion &&
		value.ArgumentsSHA256 == binding.ArgumentsSHA256 && value.ResourcesSHA256 == binding.ResourcesSHA256 && value.EffectsSHA256 == binding.EffectsSHA256 &&
		value.AnchorSHA256 == binding.AnchorSHA256 && value.PriorReleaseSHA256 == binding.PriorReleaseSHA256 && value.ManifestCoreSHA256 == binding.ManifestCoreSHA256 && value.ReleaseFactsSHA256 == binding.ReleaseFactsSHA256
}

func attachmentOperationDecisionToWireV3(value domain.BrokerAttachmentOperationDecisionV3, requireDigest bool) (attachmentOperationDecisionEnvelopeWireV3, error) {
	if !validAttachmentOperationPhaseV3(value.Phase) || validateDecisionCore(value.BrokerDecisionCore) != nil || !validDigest(value.QualificationDecisionSHA256) || value.Operation != domain.BrokerOperationJiraAttachmentDownload || value.OperationVersion != AttachmentOperationVersionV3 ||
		requireDigest && !validDigest(value.DecisionSHA256) || !requireDigest && value.DecisionSHA256 != "" && !validDigest(value.DecisionSHA256) {
		return attachmentOperationDecisionEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	var binding any
	if value.Phase == domain.BrokerAttachmentOperationRelease {
		if value.ArgumentsSHA256 != "" || value.ResourcesSHA256 != "" || value.EffectsSHA256 != "" || !validDigest(value.AnchorSHA256) || !validDigest(value.PriorReleaseSHA256) || !validDigest(value.ManifestCoreSHA256) || !validDigest(value.ReleaseFactsSHA256) {
			return attachmentOperationDecisionEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
		}
		binding = attachmentReleaseDecisionBindingWireV3{value.QualificationDecisionSHA256, string(value.Operation), value.OperationVersion, value.AnchorSHA256, value.PriorReleaseSHA256, value.ManifestCoreSHA256, value.ReleaseFactsSHA256}
	} else {
		if !validDigest(value.ArgumentsSHA256) || !validDigest(value.ResourcesSHA256) || !validDigest(value.EffectsSHA256) || value.AnchorSHA256 != "" || value.PriorReleaseSHA256 != "" || value.ManifestCoreSHA256 != "" || value.ReleaseFactsSHA256 != "" {
			return attachmentOperationDecisionEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
		}
		binding = attachmentQualifiedDecisionBindingWireV3{value.QualificationDecisionSHA256, string(value.Operation), value.OperationVersion, value.ArgumentsSHA256, value.ResourcesSHA256, value.EffectsSHA256}
	}
	body, err := marshalAttachmentPayloadV3(binding)
	if err != nil {
		return attachmentOperationDecisionEnvelopeWireV3{}, err
	}
	return attachmentOperationDecisionEnvelopeWireV3{ExecutionSchemaVersionV3, string(value.Phase), decisionCoreToWire(value.BrokerDecisionCore), body, value.DecisionSHA256}, nil
}

func attachmentOperationDecisionFromWireV3(wire attachmentOperationDecisionEnvelopeWireV3) (domain.BrokerAttachmentOperationDecisionV3, error) {
	value := domain.BrokerAttachmentOperationDecisionV3{Phase: domain.BrokerAttachmentOperationPhaseV3(wire.Phase), BrokerDecisionCore: decisionCoreFromWire(wire.Decision), DecisionSHA256: wire.DecisionSHA256}
	if value.Phase == domain.BrokerAttachmentOperationRelease {
		var binding attachmentReleaseDecisionBindingWireV3
		if !decodeAttachmentV3(wire.Binding, MaxAttachmentAuthorityCallBytesV3, &binding) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		value.QualificationDecisionSHA256, value.Operation, value.OperationVersion = binding.QualificationDecisionSHA256, domain.BrokerOperationID(binding.Operation), binding.OperationVersion
		value.AnchorSHA256, value.PriorReleaseSHA256, value.ManifestCoreSHA256, value.ReleaseFactsSHA256 = binding.AnchorSHA256, binding.PriorReleaseSHA256, binding.ManifestCoreSHA256, binding.ReleaseFactsSHA256
	} else {
		var binding attachmentQualifiedDecisionBindingWireV3
		if !decodeAttachmentV3(wire.Binding, MaxAttachmentAuthorityCallBytesV3, &binding) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		value.QualificationDecisionSHA256, value.Operation, value.OperationVersion = binding.QualificationDecisionSHA256, domain.BrokerOperationID(binding.Operation), binding.OperationVersion
		value.ArgumentsSHA256, value.ResourcesSHA256, value.EffectsSHA256 = binding.ArgumentsSHA256, binding.ResourcesSHA256, binding.EffectsSHA256
	}
	if _, err := attachmentOperationDecisionToWireV3(value, true); err != nil {
		return value, err
	}
	return value, nil
}

func verifyAttachmentOperationDecisionDigestV3(value domain.BrokerAttachmentOperationDecisionV3) bool {
	wire, err := attachmentOperationDecisionToWireV3(value, true)
	if err != nil {
		return false
	}
	claimed := wire.DecisionSHA256
	wire.DecisionSHA256 = ""
	digest, err := digestExecutionV3("operation-decision/"+string(value.Phase), wire)
	return err == nil && digest == claimed
}

func validAttachmentOperationPhaseV3(value domain.BrokerAttachmentOperationPhaseV3) bool {
	return value == domain.BrokerAttachmentOperationInitial || value == domain.BrokerAttachmentOperationBodyDispatch || value == domain.BrokerAttachmentOperationRelease
}

func attachmentOperationContextV3(value domain.BrokerAttachmentOperationAuthorizationRequestV3) domain.BrokerVerifiedContext {
	if value.Qualified == nil {
		return value.Release.QualificationRequest.Release.Context
	}
	return attachmentQualificationContextV3(value.Qualified.QualificationRequest)
}

func attachmentQualificationContextV3(value domain.BrokerAttachmentQualificationRequestV3) domain.BrokerVerifiedContext {
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		return value.Initial.Admission.Context
	case domain.BrokerAttachmentQualificationPreOpen:
		return attachmentOperationContextV3(value.PreOpen.InitialOperation)
	case domain.BrokerAttachmentQualificationRelease:
		return value.Release.Context
	default:
		return domain.BrokerVerifiedContext{}
	}
}

func attachmentQualificationArgumentsV3(value domain.BrokerAttachmentQualificationRequestV3) domain.BrokerAttachmentArgumentsV3 {
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		return value.Initial.Admission.Arguments
	case domain.BrokerAttachmentQualificationPreOpen:
		return attachmentQualificationArgumentsV3(value.PreOpen.InitialOperation.Qualified.QualificationRequest)
	default:
		return domain.BrokerAttachmentArgumentsV3{}
	}
}

func attachmentQualificationDeadlineV3(value domain.BrokerAttachmentQualificationRequestV3) int64 {
	switch value.Phase {
	case domain.BrokerAttachmentQualificationInitial:
		return value.Initial.Admission.DeadlineMillis
	case domain.BrokerAttachmentQualificationPreOpen:
		return attachmentOperationDeadlineV3(value.PreOpen.InitialOperation)
	case domain.BrokerAttachmentQualificationRelease:
		context := value.Release.Context
		return min(context.ExecutionExpiresMillis, context.GrantExpiresMillis, context.CredentialExpiresMillis)
	default:
		return 0
	}
}

func attachmentOperationDeadlineV3(value domain.BrokerAttachmentOperationAuthorizationRequestV3) int64 {
	if value.Qualified != nil {
		return attachmentQualificationDeadlineV3(value.Qualified.QualificationRequest)
	}
	return attachmentQualificationDeadlineV3(value.Release.QualificationRequest)
}

func attachmentSnapshotMatchesArgumentsV3(snapshot domain.BrokerJiraAttachmentSnapshotV3, arguments domain.BrokerAttachmentArgumentsV3) bool {
	return snapshot.IssueKey == arguments.IssueKey && snapshot.AttachmentID == arguments.AttachmentID
}

func releaseCoordinateMatchesFactsV3(coordinate domain.BrokerAttachmentReleaseCoordinateV3, facts domain.BrokerAttachmentReleaseFactsV3) bool {
	if coordinate.Kind != facts.Kind {
		return false
	}
	if facts.Kind == domain.BrokerAttachmentReleaseTerminal {
		return coordinate.Index == 0 && coordinate.Offset == 0
	}
	return coordinate.Index == facts.Data.Index && coordinate.Offset == facts.Data.Offset
}

func attachmentReleaseFactsToWireV3(value domain.BrokerAttachmentReleaseFactsV3) (attachmentReleaseFactsEnvelopeWireV3, error) {
	if !validAttachmentReleaseFactsV3(value) {
		return attachmentReleaseFactsEnvelopeWireV3{}, reject(domain.BrokerReasonMalformed)
	}
	var payload any
	if value.Kind == domain.BrokerAttachmentReleaseData {
		payload = attachmentDataReleaseToWireV3(*value.Data)
	} else {
		payload = attachmentTerminalFactsToWireV3(*value.Terminal)
	}
	body, err := marshalAttachmentPayloadV3(payload)
	if err != nil {
		return attachmentReleaseFactsEnvelopeWireV3{}, err
	}
	return attachmentReleaseFactsEnvelopeWireV3{string(value.Kind), body}, nil
}

func attachmentReleaseFactsFromWireV3(wire attachmentReleaseFactsEnvelopeWireV3) (domain.BrokerAttachmentReleaseFactsV3, error) {
	value := domain.BrokerAttachmentReleaseFactsV3{Kind: domain.BrokerAttachmentReleaseKindV3(wire.Kind)}
	switch value.Kind {
	case domain.BrokerAttachmentReleaseData:
		var payload attachmentDataReleaseWireV3
		if !decodeAttachmentV3(wire.Facts, MaxAttachmentAuthorityEnvelopeBytesV3, &payload) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		data := attachmentDataReleaseFromWireV3(payload)
		value.Data = &data
	case domain.BrokerAttachmentReleaseTerminal:
		var payload attachmentTerminalFactsWireV3
		if !decodeAttachmentV3(wire.Facts, MaxAttachmentAuthorityEnvelopeBytesV3, &payload) {
			return value, reject(domain.BrokerReasonMalformed)
		}
		terminal := attachmentTerminalFactsFromWireV3(payload)
		value.Terminal = &terminal
	default:
		return value, reject(domain.BrokerReasonMalformed)
	}
	if !validAttachmentReleaseFactsV3(value) {
		return value, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func EncodeAttachmentStreamAnchorV3(value domain.BrokerAttachmentStreamAnchorV3) ([]byte, error) {
	if !validAttachmentAnchorV3(value) {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalAttachmentV3(attachmentAnchorToWireV3(value), MaxAttachmentAuthorityEnvelopeBytesV3)
}

func DecodeAttachmentStreamAnchorV3(data []byte) (domain.BrokerAttachmentStreamAnchorV3, error) {
	var wire attachmentAnchorWireV3
	if !decodeAttachmentV3(data, MaxAttachmentAuthorityEnvelopeBytesV3, &wire) {
		return domain.BrokerAttachmentStreamAnchorV3{}, reject(domain.BrokerReasonMalformed)
	}
	value := attachmentAnchorFromWireV3(wire)
	if !validAttachmentAnchorV3(value) {
		return domain.BrokerAttachmentStreamAnchorV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func validAttachmentAnchorV3(value domain.BrokerAttachmentStreamAnchorV3) bool {
	if value.SchemaVersion != 1 || value.ContractFamily != domain.BrokerContractFamilyExecutionV3 || value.Operation != domain.BrokerOperationJiraAttachmentDownload || value.OperationVersion != AttachmentOperationVersionV3 ||
		!slices.Equal(value.Features, attachmentFeaturesV3) || validateContext(value.Context) != nil || value.Context.Backend.Service != "jira" || !validIdentifier(value.StreamID) || !validIdentifier(value.CorrelationID) ||
		value.OverallDeadlineMillis <= value.Context.ExecutionNotBeforeMillis || value.OverallDeadlineMillis > value.Context.ExecutionExpiresMillis || value.OverallDeadlineMillis > value.Context.GrantExpiresMillis || value.OverallDeadlineMillis > value.Context.CredentialExpiresMillis {
		return false
	}
	for _, digest := range []string{value.RequestSHA256, value.ArgumentsSHA256, value.ResourcesSHA256, value.EffectsSHA256, value.SnapshotSHA256,
		value.AdmissionDecisionSHA256, value.InitialQualificationDecisionSHA256, value.InitialOperationDecisionSHA256,
		value.PreOpenQualificationDecisionSHA256, value.BodyDispatchOperationDecisionSHA256} {
		if !validDigest(digest) {
			return false
		}
	}
	return true
}

func EncodeAttachmentManifestLineV3(value domain.BrokerAttachmentManifestLineV3) ([]byte, error) {
	coreDigest, err := AttachmentManifestCoreSHA256V3(value.Core)
	if err != nil || value.ManifestCoreSHA256 != coreDigest || !validDigest(value.ReleaseDecisionSHA256) {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalAttachmentV3(attachmentManifestLineToWireV3(value), MaxAttachmentManifestLineBytesV3)
}

func DecodeAttachmentManifestLineV3(data []byte) (domain.BrokerAttachmentManifestLineV3, error) {
	var wire attachmentManifestLineWireV3
	if !decodeAttachmentV3(data, MaxAttachmentManifestLineBytesV3, &wire) {
		return domain.BrokerAttachmentManifestLineV3{}, reject(domain.BrokerReasonMalformed)
	}
	value := attachmentManifestLineFromWireV3(wire)
	canonical, err := EncodeAttachmentManifestLineV3(value)
	if err != nil {
		return domain.BrokerAttachmentManifestLineV3{}, err
	}
	if !bytes.Equal(data, canonical) {
		return domain.BrokerAttachmentManifestLineV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func EncodeAttachmentDataLineV3(value domain.BrokerAttachmentDataLineV3) ([]byte, error) {
	if !validAttachmentDataLineV3(value) {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalAttachmentV3(attachmentDataLineToWireV3(value), MaxAttachmentDataLineBytesV3)
}

func DecodeAttachmentDataLineV3(data []byte) (domain.BrokerAttachmentDataLineV3, error) {
	var wire attachmentDataLineWireV3
	if !decodeAttachmentV3(data, MaxAttachmentDataLineBytesV3, &wire) {
		return domain.BrokerAttachmentDataLineV3{}, reject(domain.BrokerReasonMalformed)
	}
	value := attachmentDataLineFromWireV3(wire)
	if !validAttachmentDataLineV3(value) {
		return domain.BrokerAttachmentDataLineV3{}, reject(domain.BrokerReasonMalformed)
	}
	canonical, err := EncodeAttachmentDataLineV3(value)
	if err != nil || !bytes.Equal(data, canonical) {
		return domain.BrokerAttachmentDataLineV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func EncodeAttachmentTerminalLineV3(value domain.BrokerAttachmentTerminalLineV3) ([]byte, error) {
	if !validAttachmentTerminalLineV3(value) {
		return nil, reject(domain.BrokerReasonMalformed)
	}
	return marshalAttachmentV3(attachmentTerminalLineToWireV3(value), MaxAttachmentTerminalLineBytesV3)
}

func DecodeAttachmentTerminalLineV3(data []byte) (domain.BrokerAttachmentTerminalLineV3, error) {
	var wire attachmentTerminalLineWireV3
	if !decodeAttachmentV3(data, MaxAttachmentTerminalLineBytesV3, &wire) {
		return domain.BrokerAttachmentTerminalLineV3{}, reject(domain.BrokerReasonMalformed)
	}
	value := attachmentTerminalLineFromWireV3(wire)
	if !validAttachmentTerminalLineV3(value) {
		return domain.BrokerAttachmentTerminalLineV3{}, reject(domain.BrokerReasonMalformed)
	}
	canonical, err := EncodeAttachmentTerminalLineV3(value)
	if err != nil || !bytes.Equal(data, canonical) {
		return domain.BrokerAttachmentTerminalLineV3{}, reject(domain.BrokerReasonMalformed)
	}
	return value, nil
}

func validAttachmentManifestCoreV3(value domain.BrokerAttachmentManifestCoreV3) bool {
	return value.SchemaVersion == ExecutionSchemaVersionV3 && value.FrameVersion == AttachmentFrameVersionV3 && validIdentifier(value.StreamID) && validIdentifier(value.CorrelationID) &&
		validDigest(value.ArgumentsSHA256) && validDigest(value.AnchorSHA256) && validAttachmentSnapshotV3(value.Snapshot, true) && value.ConsistencyProfile == domain.BrokerAttachmentConsistencyStepSnapshotV1 &&
		value.MaxDataFrames == MaxAttachmentDataFramesV3 && value.MaxDecodedFrameBytes == MaxAttachmentDecodedFrameBytesV3 && value.MaxNativeBodyBytes == MaxAttachmentNativeBodyBytesV3 && value.MaxMetadataItems == MaxAttachmentInventoryItemsV3 &&
		value.MaxManifestLineBytes == MaxAttachmentManifestLineBytesV3 && value.MaxDataLineBytes == MaxAttachmentDataLineBytesV3 && value.MaxTerminalLineBytes == MaxAttachmentTerminalLineBytesV3 && value.MaxFramedBytes == MaxAttachmentFramedResponseBytesV3 &&
		value.MaxJiraAttempts == MaxAttachmentJiraAttemptsV3 && value.MaxAuthenticationAttempts == MaxAttachmentAuthenticationAttemptsV3 && value.MaxDecisionAttempts == MaxAttachmentDecisionAttemptsV3 && value.MaxTotalHostOutboundAttempts == MaxAttachmentHostOutboundAttemptsV3 && value.MaxCommandHostOutboundAttempts == MaxAttachmentCommandHostOutboundAttemptsV3 &&
		value.MaxJiraResponseBytes == MaxAttachmentJiraResponseBytesV3 && value.MaxAuthorityResponseBytes == MaxAttachmentAuthorityResponseBytesV3 && value.MaxTotalHostResponseBytes == MaxAttachmentHostResponseBytesV3 &&
		value.MaxOperationMillis == domain.BrokerMaxOperationMillis && value.MaxDecisionLeaseMillis == domain.BrokerMaxDecisionLeaseMillis
}
