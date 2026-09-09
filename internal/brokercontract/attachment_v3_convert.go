package brokercontract

import "github.com/isukharev/atl/internal/domain"

func attachmentRequestToWireV3(value domain.BrokerAttachmentRequestV3) attachmentRequestWireV3 {
	return attachmentRequestWireV3{value.SchemaVersion, string(value.Operation), value.OperationVersion, value.RequestID, append([]string{}, value.Features...), expectationsWire{value.Expect.ExecutionID, value.Expect.ExecutionEpoch, value.Expect.AuthorityRevision}, attachmentArgumentsToWireV3(value.Arguments)}
}

func attachmentRequestFromWireV3(wire attachmentRequestWireV3) domain.BrokerAttachmentRequestV3 {
	return domain.BrokerAttachmentRequestV3{SchemaVersion: wire.SchemaVersion, Operation: domain.BrokerOperationID(wire.Operation), OperationVersion: wire.OperationVersion, RequestID: wire.RequestID, Features: copyStrings(wire.Features), Expect: domain.BrokerRequestExpectations{ExecutionID: wire.Expect.ExecutionID, ExecutionEpoch: wire.Expect.ExecutionEpoch, AuthorityRevision: wire.Expect.AuthorityRevision}, Arguments: attachmentArgumentsFromWireV3(wire.Arguments)}
}

func attachmentArgumentsToWireV3(value domain.BrokerAttachmentArgumentsV3) attachmentArgumentsWireV3 {
	return attachmentArgumentsWireV3{value.IssueKey, value.AttachmentID}
}

func attachmentArgumentsFromWireV3(wire attachmentArgumentsWireV3) domain.BrokerAttachmentArgumentsV3 {
	return domain.BrokerAttachmentArgumentsV3{IssueKey: wire.IssueKey, AttachmentID: wire.AttachmentID}
}

func attachmentAdmissionRequestToWireV3(value domain.BrokerAttachmentAdmissionRequestV3) attachmentAdmissionRequestWireV3 {
	return attachmentAdmissionRequestWireV3{ExecutionSchemaVersionV3, contextToWire(value.Context), string(value.Operation), value.OperationVersion, value.RequestID, append([]string{}, value.Features...), attachmentArgumentsToWireV3(value.Arguments), value.ArgumentsSHA256, value.DeadlineMillis}
}

func attachmentAdmissionRequestFromWireV3(wire attachmentAdmissionRequestWireV3) domain.BrokerAttachmentAdmissionRequestV3 {
	return domain.BrokerAttachmentAdmissionRequestV3{Context: contextFromWire(wire.Context), Operation: domain.BrokerOperationID(wire.Operation), OperationVersion: wire.OperationVersion, RequestID: wire.RequestID, Features: copyStrings(wire.Features), Arguments: attachmentArgumentsFromWireV3(wire.Arguments), ArgumentsSHA256: wire.ArgumentsSHA256, DeadlineMillis: wire.DeadlineMillis}
}

func attachmentAdmissionDecisionToWireV3(value domain.BrokerAttachmentAdmissionDecisionV3) attachmentAdmissionDecisionWireV3 {
	return attachmentAdmissionDecisionWireV3{ExecutionSchemaVersionV3, decisionCoreToWire(value.BrokerDecisionCore), value.DecisionSHA256}
}

func attachmentAdmissionDecisionFromWireV3(wire attachmentAdmissionDecisionWireV3) domain.BrokerAttachmentAdmissionDecisionV3 {
	return domain.BrokerAttachmentAdmissionDecisionV3{BrokerDecisionCore: decisionCoreFromWire(wire.Decision), DecisionSHA256: wire.DecisionSHA256}
}

func attachmentMetadataPlanToWireV3(value domain.BrokerAttachmentMetadataPlanV3) attachmentMetadataPlanWireV3 {
	return attachmentMetadataPlanWireV3{value.SelectorSHA256, append([]string{}, value.MetadataFields...), phaseLimitsToWire(value.Limits)}
}

func attachmentMetadataPlanFromWireV3(wire attachmentMetadataPlanWireV3) domain.BrokerAttachmentMetadataPlanV3 {
	return domain.BrokerAttachmentMetadataPlanV3{SelectorSHA256: wire.SelectorSHA256, MetadataFields: copyStrings(wire.MetadataFields), Limits: phaseLimitsFromWire(wire.Limits)}
}

func attachmentSnapshotToWireV3(value domain.BrokerJiraAttachmentSnapshotV3) attachmentSnapshotWireV3 {
	return attachmentSnapshotWireV3{value.IssueID, value.IssueKey, value.Project, value.Updated, value.AttachmentID, value.ParentID, value.Filename, value.MediaType, value.Created, value.DeclaredSize, value.IssueEvidenceSHA256, value.AttachmentEvidenceSHA256, value.ProjectionSHA256}
}

func attachmentSnapshotFromWireV3(wire attachmentSnapshotWireV3) domain.BrokerJiraAttachmentSnapshotV3 {
	return domain.BrokerJiraAttachmentSnapshotV3{IssueID: wire.IssueID, IssueKey: wire.IssueKey, Project: wire.Project, Updated: wire.Updated, AttachmentID: wire.AttachmentID, ParentID: wire.ParentID, Filename: wire.Filename, MediaType: wire.MediaType, Created: wire.Created, DeclaredSize: wire.DeclaredSize, IssueEvidenceSHA256: wire.IssueEvidenceSHA256, AttachmentEvidenceSHA256: wire.AttachmentEvidenceSHA256, ProjectionSHA256: wire.ProjectionSHA256}
}

func attachmentEffectToWireV3(value domain.BrokerAttachmentEffectV3) attachmentEffectWireV3 {
	return attachmentEffectWireV3{string(value.Kind), string(value.ResourceKind), append([]string{}, value.Fields...)}
}

func attachmentEffectFromWireV3(wire attachmentEffectWireV3) domain.BrokerAttachmentEffectV3 {
	return domain.BrokerAttachmentEffectV3{Kind: domain.BrokerEffectKind(wire.Kind), ResourceKind: domain.BrokerResourceKind(wire.ResourceKind), Fields: copyStrings(wire.Fields)}
}

func attachmentTerminalFactsToWireV3(value domain.BrokerAttachmentReleaseTerminalFactsV3) attachmentTerminalFactsWireV3 {
	return attachmentTerminalFactsWireV3{value.ChunkCount, value.TotalBytes, value.WholeSHA256, value.EOFProven, value.Complete}
}

func attachmentTerminalFactsFromWireV3(wire attachmentTerminalFactsWireV3) domain.BrokerAttachmentReleaseTerminalFactsV3 {
	return domain.BrokerAttachmentReleaseTerminalFactsV3{ChunkCount: wire.ChunkCount, TotalBytes: wire.TotalBytes, WholeSHA256: wire.WholeSHA256, EOFProven: wire.EOFProven, Complete: wire.Complete}
}

func attachmentDataReleaseToWireV3(value domain.BrokerAttachmentDataReleaseV3) attachmentDataReleaseWireV3 {
	var terminal *attachmentTerminalFactsWireV3
	if value.Terminal != nil {
		converted := attachmentTerminalFactsToWireV3(*value.Terminal)
		terminal = &converted
	}
	return attachmentDataReleaseWireV3{value.Index, value.Offset, value.DecodedBytes, value.PayloadSHA256, value.CumulativeBytes, value.CumulativeSHA256, value.ResourceSHA256, terminal}
}

func attachmentDataReleaseFromWireV3(wire attachmentDataReleaseWireV3) domain.BrokerAttachmentDataReleaseV3 {
	var terminal *domain.BrokerAttachmentReleaseTerminalFactsV3
	if wire.Terminal != nil {
		converted := attachmentTerminalFactsFromWireV3(*wire.Terminal)
		terminal = &converted
	}
	return domain.BrokerAttachmentDataReleaseV3{Index: wire.Index, Offset: wire.Offset, DecodedBytes: wire.DecodedBytes, PayloadSHA256: wire.PayloadSHA256, CumulativeBytes: wire.CumulativeBytes, CumulativeSHA256: wire.CumulativeSHA256, ResourceSHA256: wire.ResourceSHA256, Terminal: terminal}
}

func attachmentAnchorToWireV3(value domain.BrokerAttachmentStreamAnchorV3) attachmentAnchorWireV3 {
	return attachmentAnchorWireV3{
		SchemaVersion: value.SchemaVersion, ContractFamily: value.ContractFamily, Operation: string(value.Operation), OperationVersion: value.OperationVersion, Features: append([]string{}, value.Features...), Context: contextToWire(value.Context),
		RequestSHA256: value.RequestSHA256, ArgumentsSHA256: value.ArgumentsSHA256, ResourcesSHA256: value.ResourcesSHA256, EffectsSHA256: value.EffectsSHA256, SnapshotSHA256: value.SnapshotSHA256,
		StreamID: value.StreamID, CorrelationID: value.CorrelationID, OverallDeadlineMillis: value.OverallDeadlineMillis,
		AdmissionDecisionSHA256: value.AdmissionDecisionSHA256, InitialQualificationDecisionSHA256: value.InitialQualificationDecisionSHA256,
		InitialOperationDecisionSHA256: value.InitialOperationDecisionSHA256, PreOpenQualificationDecisionSHA256: value.PreOpenQualificationDecisionSHA256,
		BodyDispatchOperationDecisionSHA256: value.BodyDispatchOperationDecisionSHA256,
	}
}

func attachmentAnchorFromWireV3(wire attachmentAnchorWireV3) domain.BrokerAttachmentStreamAnchorV3 {
	return domain.BrokerAttachmentStreamAnchorV3{
		SchemaVersion: wire.SchemaVersion, ContractFamily: wire.ContractFamily, Operation: domain.BrokerOperationID(wire.Operation), OperationVersion: wire.OperationVersion, Features: copyStrings(wire.Features), Context: contextFromWire(wire.Context),
		RequestSHA256: wire.RequestSHA256, ArgumentsSHA256: wire.ArgumentsSHA256, ResourcesSHA256: wire.ResourcesSHA256, EffectsSHA256: wire.EffectsSHA256, SnapshotSHA256: wire.SnapshotSHA256,
		StreamID: wire.StreamID, CorrelationID: wire.CorrelationID, OverallDeadlineMillis: wire.OverallDeadlineMillis,
		AdmissionDecisionSHA256: wire.AdmissionDecisionSHA256, InitialQualificationDecisionSHA256: wire.InitialQualificationDecisionSHA256,
		InitialOperationDecisionSHA256: wire.InitialOperationDecisionSHA256, PreOpenQualificationDecisionSHA256: wire.PreOpenQualificationDecisionSHA256,
		BodyDispatchOperationDecisionSHA256: wire.BodyDispatchOperationDecisionSHA256,
	}
}

func attachmentManifestCoreToWireV3(value domain.BrokerAttachmentManifestCoreV3) attachmentManifestCoreWireV3 {
	return attachmentManifestCoreWireV3{
		SchemaVersion: value.SchemaVersion, FrameVersion: value.FrameVersion, Kind: "manifest", StreamID: value.StreamID, CorrelationID: value.CorrelationID,
		ArgumentsSHA256: value.ArgumentsSHA256, AnchorSHA256: value.AnchorSHA256, Snapshot: attachmentSnapshotToWireV3(value.Snapshot), ConsistencyProfile: value.ConsistencyProfile,
		MaxDataFrames: value.MaxDataFrames, MaxDecodedFrameBytes: value.MaxDecodedFrameBytes, MaxNativeBodyBytes: value.MaxNativeBodyBytes, MaxMetadataItems: value.MaxMetadataItems,
		MaxManifestLineBytes: value.MaxManifestLineBytes, MaxDataLineBytes: value.MaxDataLineBytes, MaxTerminalLineBytes: value.MaxTerminalLineBytes, MaxFramedBytes: value.MaxFramedBytes,
		MaxJiraAttempts: value.MaxJiraAttempts, MaxAuthenticationAttempts: value.MaxAuthenticationAttempts, MaxDecisionAttempts: value.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: value.MaxTotalHostOutboundAttempts, MaxCommandHostOutboundAttempts: value.MaxCommandHostOutboundAttempts,
		MaxJiraResponseBytes: value.MaxJiraResponseBytes, MaxAuthorityResponseBytes: value.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: value.MaxTotalHostResponseBytes,
		MaxOperationMillis: value.MaxOperationMillis, MaxDecisionLeaseMillis: value.MaxDecisionLeaseMillis,
	}
}

func attachmentManifestCoreFromWireV3(wire attachmentManifestCoreWireV3) domain.BrokerAttachmentManifestCoreV3 {
	return domain.BrokerAttachmentManifestCoreV3{
		SchemaVersion: wire.SchemaVersion, FrameVersion: wire.FrameVersion, StreamID: wire.StreamID, CorrelationID: wire.CorrelationID,
		ArgumentsSHA256: wire.ArgumentsSHA256, AnchorSHA256: wire.AnchorSHA256, Snapshot: attachmentSnapshotFromWireV3(wire.Snapshot), ConsistencyProfile: wire.ConsistencyProfile,
		MaxDataFrames: wire.MaxDataFrames, MaxDecodedFrameBytes: wire.MaxDecodedFrameBytes, MaxNativeBodyBytes: wire.MaxNativeBodyBytes, MaxMetadataItems: wire.MaxMetadataItems,
		MaxManifestLineBytes: wire.MaxManifestLineBytes, MaxDataLineBytes: wire.MaxDataLineBytes, MaxTerminalLineBytes: wire.MaxTerminalLineBytes, MaxFramedBytes: wire.MaxFramedBytes,
		MaxJiraAttempts: wire.MaxJiraAttempts, MaxAuthenticationAttempts: wire.MaxAuthenticationAttempts, MaxDecisionAttempts: wire.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: wire.MaxTotalHostOutboundAttempts, MaxCommandHostOutboundAttempts: wire.MaxCommandHostOutboundAttempts,
		MaxJiraResponseBytes: wire.MaxJiraResponseBytes, MaxAuthorityResponseBytes: wire.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: wire.MaxTotalHostResponseBytes,
		MaxOperationMillis: wire.MaxOperationMillis, MaxDecisionLeaseMillis: wire.MaxDecisionLeaseMillis,
	}
}

func attachmentManifestLineToWireV3(value domain.BrokerAttachmentManifestLineV3) attachmentManifestLineWireV3 {
	core := attachmentManifestCoreToWireV3(value.Core)
	return attachmentManifestLineWireV3{
		SchemaVersion: core.SchemaVersion, FrameVersion: core.FrameVersion, Kind: core.Kind, StreamID: core.StreamID, CorrelationID: core.CorrelationID,
		ArgumentsSHA256: core.ArgumentsSHA256, AnchorSHA256: core.AnchorSHA256, Snapshot: core.Snapshot, ConsistencyProfile: core.ConsistencyProfile,
		MaxDataFrames: core.MaxDataFrames, MaxDecodedFrameBytes: core.MaxDecodedFrameBytes, MaxNativeBodyBytes: core.MaxNativeBodyBytes, MaxMetadataItems: core.MaxMetadataItems,
		MaxManifestLineBytes: core.MaxManifestLineBytes, MaxDataLineBytes: core.MaxDataLineBytes, MaxTerminalLineBytes: core.MaxTerminalLineBytes, MaxFramedBytes: core.MaxFramedBytes,
		MaxJiraAttempts: core.MaxJiraAttempts, MaxAuthenticationAttempts: core.MaxAuthenticationAttempts, MaxDecisionAttempts: core.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: core.MaxTotalHostOutboundAttempts, MaxCommandHostOutboundAttempts: core.MaxCommandHostOutboundAttempts,
		MaxJiraResponseBytes: core.MaxJiraResponseBytes, MaxAuthorityResponseBytes: core.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: core.MaxTotalHostResponseBytes,
		MaxOperationMillis: core.MaxOperationMillis, MaxDecisionLeaseMillis: core.MaxDecisionLeaseMillis,
		ManifestCoreSHA256: value.ManifestCoreSHA256, ReleaseDecisionSHA256: value.ReleaseDecisionSHA256,
	}
}

func attachmentManifestLineFromWireV3(wire attachmentManifestLineWireV3) domain.BrokerAttachmentManifestLineV3 {
	core := attachmentManifestCoreWireV3{
		SchemaVersion: wire.SchemaVersion, FrameVersion: wire.FrameVersion, Kind: wire.Kind, StreamID: wire.StreamID, CorrelationID: wire.CorrelationID,
		ArgumentsSHA256: wire.ArgumentsSHA256, AnchorSHA256: wire.AnchorSHA256, Snapshot: wire.Snapshot, ConsistencyProfile: wire.ConsistencyProfile,
		MaxDataFrames: wire.MaxDataFrames, MaxDecodedFrameBytes: wire.MaxDecodedFrameBytes, MaxNativeBodyBytes: wire.MaxNativeBodyBytes, MaxMetadataItems: wire.MaxMetadataItems,
		MaxManifestLineBytes: wire.MaxManifestLineBytes, MaxDataLineBytes: wire.MaxDataLineBytes, MaxTerminalLineBytes: wire.MaxTerminalLineBytes, MaxFramedBytes: wire.MaxFramedBytes,
		MaxJiraAttempts: wire.MaxJiraAttempts, MaxAuthenticationAttempts: wire.MaxAuthenticationAttempts, MaxDecisionAttempts: wire.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: wire.MaxTotalHostOutboundAttempts, MaxCommandHostOutboundAttempts: wire.MaxCommandHostOutboundAttempts,
		MaxJiraResponseBytes: wire.MaxJiraResponseBytes, MaxAuthorityResponseBytes: wire.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: wire.MaxTotalHostResponseBytes,
		MaxOperationMillis: wire.MaxOperationMillis, MaxDecisionLeaseMillis: wire.MaxDecisionLeaseMillis,
	}
	return domain.BrokerAttachmentManifestLineV3{Core: attachmentManifestCoreFromWireV3(core), ManifestCoreSHA256: wire.ManifestCoreSHA256, ReleaseDecisionSHA256: wire.ReleaseDecisionSHA256}
}

func attachmentDataLineToWireV3(value domain.BrokerAttachmentDataLineV3) attachmentDataLineWireV3 {
	return attachmentDataLineWireV3{value.SchemaVersion, value.FrameVersion, string(value.Kind), value.StreamID, value.Index, value.Offset, value.DecodedBytes, value.PayloadBase64, value.PayloadSHA256, value.CumulativeBytes, value.CumulativeSHA256, value.ResourceSHA256, value.PriorReleaseSHA256, value.ReleaseDecisionSHA256}
}

func attachmentDataLineFromWireV3(wire attachmentDataLineWireV3) domain.BrokerAttachmentDataLineV3 {
	return domain.BrokerAttachmentDataLineV3{SchemaVersion: wire.SchemaVersion, FrameVersion: wire.FrameVersion, Kind: domain.BrokerAttachmentReleaseKindV3(wire.Kind), StreamID: wire.StreamID, Index: wire.Index, Offset: wire.Offset, DecodedBytes: wire.DecodedBytes, PayloadBase64: wire.PayloadBase64, PayloadSHA256: wire.PayloadSHA256, CumulativeBytes: wire.CumulativeBytes, CumulativeSHA256: wire.CumulativeSHA256, ResourceSHA256: wire.ResourceSHA256, PriorReleaseSHA256: wire.PriorReleaseSHA256, ReleaseDecisionSHA256: wire.ReleaseDecisionSHA256}
}

func attachmentTerminalLineToWireV3(value domain.BrokerAttachmentTerminalLineV3) attachmentTerminalLineWireV3 {
	return attachmentTerminalLineWireV3{value.SchemaVersion, value.FrameVersion, string(value.Kind), value.StreamID, value.ChunkCount, value.TotalBytes, value.DeclaredSize, value.WholeSHA256, value.PriorReleaseSHA256, value.ReleaseDecisionSHA256, value.EOFProven, value.Complete}
}

func attachmentTerminalLineFromWireV3(wire attachmentTerminalLineWireV3) domain.BrokerAttachmentTerminalLineV3 {
	return domain.BrokerAttachmentTerminalLineV3{SchemaVersion: wire.SchemaVersion, FrameVersion: wire.FrameVersion, Kind: domain.BrokerAttachmentReleaseKindV3(wire.Kind), StreamID: wire.StreamID, ChunkCount: wire.ChunkCount, TotalBytes: wire.TotalBytes, DeclaredSize: wire.DeclaredSize, WholeSHA256: wire.WholeSHA256, PriorReleaseSHA256: wire.PriorReleaseSHA256, ReleaseDecisionSHA256: wire.ReleaseDecisionSHA256, EOFProven: wire.EOFProven, Complete: wire.Complete}
}
