package brokercontract

import (
	"bytes"
	"slices"

	"github.com/isukharev/atl/internal/domain"
)

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
	_, err := buildAttachmentQualificationCheckedV3(value)
	return err
}

func attachmentQualificationPlanContextV3(value domain.BrokerAttachmentQualificationRequestV3, nowMillis int64) (domain.BrokerAttachmentMetadataPlanV3, domain.BrokerVerifiedContext, bool) {
	checked, err := buildAttachmentQualificationCheckedV3(value)
	if err != nil || checked.validateCurrentLineageV3(nowMillis) != nil {
		return domain.BrokerAttachmentMetadataPlanV3{}, domain.BrokerVerifiedContext{}, false
	}
	return checked.plan, checked.context, true
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
	checked, err := buildAttachmentOperationCheckedV3(value)
	if err != nil {
		return attachmentOperationEnvelopeWireV3{}, err
	}
	return checked.wire, nil
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

func attachmentOperationDecisionBindingV3(request domain.BrokerAttachmentOperationAuthorizationRequestV3) (domain.BrokerAttachmentOperationDecisionV3, domain.BrokerVerifiedContext, error) {
	checked, err := buildAttachmentOperationCheckedV3(request)
	if err != nil {
		return domain.BrokerAttachmentOperationDecisionV3{}, domain.BrokerVerifiedContext{}, reject(domain.BrokerReasonMalformed)
	}
	return checked.binding, checked.context, nil
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
