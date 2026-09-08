package brokercontract

import (
	"encoding/json"

	"github.com/isukharev/atl/internal/domain"
)

func EncodeExecutionProjectionV1(value domain.BrokerExecutionProjection) ([]byte, error) {
	if err := validateExecutionProjection(value); err != nil {
		return nil, err
	}
	return json.Marshal(executionProjectionToWire(value))
}

func DecodeExecutionProjectionV1(data []byte) (domain.BrokerExecutionProjection, error) {
	var wire executionProjectionWire
	if err := strictDecode(data, 64<<10, &wire); err != nil {
		return domain.BrokerExecutionProjection{}, err
	}
	value := executionProjectionFromWire(wire)
	if err := validateExecutionProjection(value); err != nil {
		return domain.BrokerExecutionProjection{}, err
	}
	return value, nil
}

func validateExecutionProjection(value domain.BrokerExecutionProjection) error {
	if value.SchemaVersion != SchemaVersion || !validIdentifier(value.ExecutionID) || !validIdentifier(value.ExecutionEpoch) ||
		!validIdentifier(value.Audience) || !validIdentifier(value.AuthorityRevision) || value.ExpiresAtMillis <= 0 || !validDigest(value.ScopeSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func executionProjectionToWire(value domain.BrokerExecutionProjection) executionProjectionWire {
	return executionProjectionWire{SchemaVersion: value.SchemaVersion, ExecutionID: value.ExecutionID, ExecutionEpoch: value.ExecutionEpoch, Audience: value.Audience, AuthorityRevision: value.AuthorityRevision, ExpiresAtMillis: value.ExpiresAtMillis, ScopeSHA256: value.ScopeSHA256}
}

func executionProjectionFromWire(wire executionProjectionWire) domain.BrokerExecutionProjection {
	return domain.BrokerExecutionProjection{SchemaVersion: wire.SchemaVersion, ExecutionID: wire.ExecutionID, ExecutionEpoch: wire.ExecutionEpoch, Audience: wire.Audience, AuthorityRevision: wire.AuthorityRevision, ExpiresAtMillis: wire.ExpiresAtMillis, ScopeSHA256: wire.ScopeSHA256}
}

func EncodeOperationTicketV1(value domain.BrokerOperationTicket) ([]byte, error) {
	if err := validateTicket(value); err != nil {
		return nil, err
	}
	return json.Marshal(ticketToWire(value))
}

func DecodeOperationTicketV1(data []byte) (domain.BrokerOperationTicket, error) {
	var wire ticketWire
	if err := strictDecode(data, 64<<10, &wire); err != nil {
		return domain.BrokerOperationTicket{}, err
	}
	value := ticketFromWire(wire)
	if err := validateTicket(value); err != nil {
		return domain.BrokerOperationTicket{}, err
	}
	return value, nil
}

func OperationTicketSHA256(value domain.BrokerOperationTicket) (string, error) {
	if err := validateTicket(value); err != nil {
		return "", err
	}
	return digestValue("operation-ticket", ticketToWire(value))
}

func validateTicket(value domain.BrokerOperationTicket) error {
	definition, ok := Definition(value.Operation, value.OperationVersion)
	if value.SchemaVersion != SchemaVersion || !ok || !validIdentifier(value.OperationID) ||
		!validDigest(value.PrincipalSHA256) || !validDigest(value.ExecutionSHA256) || !validDigest(value.AudienceSHA256) || !validDigest(value.BackendSHA256) || !validDigest(value.ArgumentsSHA256) ||
		value.IssuedAtMillis <= 0 || value.AcceptUntilMillis <= value.IssuedAtMillis || value.AcceptUntilMillis-value.IssuedAtMillis > domain.BrokerMaxOperationMillis ||
		value.ProposalSHA256 != "" && !validDigest(value.ProposalSHA256) || value.Operation == domain.BrokerOperationJiraCommentApply && value.ProposalSHA256 == "" ||
		value.Operation != domain.BrokerOperationJiraCommentApply && value.ProposalSHA256 != "" || definition.Limits.MaxOperationMillis < value.AcceptUntilMillis-value.IssuedAtMillis {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func ticketToWire(value domain.BrokerOperationTicket) ticketWire {
	return ticketWire{
		SchemaVersion: value.SchemaVersion, OperationID: value.OperationID, PrincipalSHA256: value.PrincipalSHA256,
		ExecutionSHA256: value.ExecutionSHA256, AudienceSHA256: value.AudienceSHA256, BackendSHA256: value.BackendSHA256,
		Operation: string(value.Operation), OperationVersion: value.OperationVersion, ArgumentsSHA256: value.ArgumentsSHA256,
		ProposalSHA256: value.ProposalSHA256, IssuedAtMillis: value.IssuedAtMillis, AcceptUntilMillis: value.AcceptUntilMillis,
	}
}

func ticketFromWire(wire ticketWire) domain.BrokerOperationTicket {
	return domain.BrokerOperationTicket{
		SchemaVersion: wire.SchemaVersion, OperationID: wire.OperationID, PrincipalSHA256: wire.PrincipalSHA256,
		ExecutionSHA256: wire.ExecutionSHA256, AudienceSHA256: wire.AudienceSHA256, BackendSHA256: wire.BackendSHA256,
		Operation: domain.BrokerOperationID(wire.Operation), OperationVersion: wire.OperationVersion, ArgumentsSHA256: wire.ArgumentsSHA256,
		ProposalSHA256: wire.ProposalSHA256, IssuedAtMillis: wire.IssuedAtMillis, AcceptUntilMillis: wire.AcceptUntilMillis,
	}
}

func EncodeOperationOutcomeV1(value domain.BrokerOperationOutcome) ([]byte, error) {
	if err := validateOutcome(value); err != nil {
		return nil, err
	}
	return json.Marshal(outcomeToWire(value))
}

func DecodeOperationOutcomeV1(data []byte) (domain.BrokerOperationOutcome, error) {
	var wire outcomeWire
	if err := strictDecode(data, 64<<10, &wire); err != nil {
		return domain.BrokerOperationOutcome{}, err
	}
	value := outcomeFromWire(wire)
	if err := validateOutcome(value); err != nil {
		return domain.BrokerOperationOutcome{}, err
	}
	return value, nil
}

func validateOutcome(value domain.BrokerOperationOutcome) error {
	if value.SchemaVersion != SchemaVersion || !validDigest(value.TicketSHA256) || value.ObservedAtMillis <= 0 || value.ResultSHA256 != "" && !validDigest(value.ResultSHA256) {
		return reject(domain.BrokerReasonMalformed)
	}
	switch value.Phase {
	case domain.BrokerOperationAdmitted, domain.BrokerOperationDispatching:
		if value.Complete || value.Reconciled || value.ResultSHA256 != "" {
			return reject(domain.BrokerReasonMalformed)
		}
	case domain.BrokerOperationApplied:
		if !value.Complete || value.ResultSHA256 == "" {
			return reject(domain.BrokerReasonMalformed)
		}
	case domain.BrokerOperationNotApplied:
		if !value.Complete {
			return reject(domain.BrokerReasonMalformed)
		}
	case domain.BrokerOperationOutcomeUnknown:
		if value.Complete || value.ResultSHA256 != "" {
			return reject(domain.BrokerReasonMalformed)
		}
	case domain.BrokerOperationRetiredNonReplayable:
		if !value.Complete || value.Reconciled || value.ResultSHA256 != "" {
			return reject(domain.BrokerReasonMalformed)
		}
	default:
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func outcomeToWire(value domain.BrokerOperationOutcome) outcomeWire {
	return outcomeWire{SchemaVersion: value.SchemaVersion, TicketSHA256: value.TicketSHA256, Phase: string(value.Phase), ObservedAtMillis: value.ObservedAtMillis, ResultSHA256: value.ResultSHA256, Complete: value.Complete, Reconciled: value.Reconciled}
}

func outcomeFromWire(wire outcomeWire) domain.BrokerOperationOutcome {
	return domain.BrokerOperationOutcome{SchemaVersion: wire.SchemaVersion, TicketSHA256: wire.TicketSHA256, Phase: domain.BrokerOperationPhase(wire.Phase), ObservedAtMillis: wire.ObservedAtMillis, ResultSHA256: wire.ResultSHA256, Complete: wire.Complete, Reconciled: wire.Reconciled}
}

func EncodeCacheQualificationV1(value domain.BrokerCacheQualification) ([]byte, error) {
	if err := validateCacheQualification(value); err != nil {
		return nil, err
	}
	return json.Marshal(cacheQualificationToWire(value))
}

func DecodeCacheQualificationV1(data []byte) (domain.BrokerCacheQualification, error) {
	var wire cacheQualificationWire
	if err := strictDecode(data, 64<<10, &wire); err != nil {
		return domain.BrokerCacheQualification{}, err
	}
	value := cacheQualificationFromWire(wire)
	if err := validateCacheQualification(value); err != nil {
		return domain.BrokerCacheQualification{}, err
	}
	return value, nil
}

func validateCacheQualification(value domain.BrokerCacheQualification) error {
	if value.SchemaVersion != SchemaVersion || !validDecisionStatus(value.Status, value.Reason) ||
		!validDigest(value.IssuerSHA256) || !validDigest(value.TargetExecutionSHA256) || !validDigest(value.AuthorityRevisionSHA256) || !validDigest(value.RequestSHA256) ||
		value.IssuedAtMillis <= 0 || value.ExpiresAtMillis <= value.IssuedAtMillis || value.ExpiresAtMillis-value.IssuedAtMillis > domain.BrokerMaxDecisionLeaseMillis {
		return reject(domain.BrokerReasonMalformed)
	}
	return nil
}

func cacheQualificationToWire(value domain.BrokerCacheQualification) cacheQualificationWire {
	return cacheQualificationWire{SchemaVersion: value.SchemaVersion, Status: string(value.Status), Reason: string(value.Reason), IssuerSHA256: value.IssuerSHA256, TargetExecutionSHA256: value.TargetExecutionSHA256, AuthorityRevisionSHA256: value.AuthorityRevisionSHA256, RequestSHA256: value.RequestSHA256, IssuedAtMillis: value.IssuedAtMillis, ExpiresAtMillis: value.ExpiresAtMillis}
}

func cacheQualificationFromWire(wire cacheQualificationWire) domain.BrokerCacheQualification {
	return domain.BrokerCacheQualification{SchemaVersion: wire.SchemaVersion, Status: domain.BrokerDecisionStatus(wire.Status), Reason: domain.BrokerReason(wire.Reason), IssuerSHA256: wire.IssuerSHA256, TargetExecutionSHA256: wire.TargetExecutionSHA256, AuthorityRevisionSHA256: wire.AuthorityRevisionSHA256, RequestSHA256: wire.RequestSHA256, IssuedAtMillis: wire.IssuedAtMillis, ExpiresAtMillis: wire.ExpiresAtMillis}
}

func CacheQualificationRequestSHA256(value domain.BrokerCacheQualificationRequest) (string, error) {
	if err := validateCacheQualificationRequest(value); err != nil {
		return "", err
	}
	return digestValue("cache-qualification-request", cacheQualificationRequestToWire(value))
}

func EncodeCacheQualificationRequestV1(value domain.BrokerCacheQualificationRequest) ([]byte, error) {
	if err := validateCacheQualificationRequest(value); err != nil {
		return nil, err
	}
	return json.Marshal(cacheQualificationRequestToWire(value))
}

func DecodeCacheQualificationRequestV1(data []byte) (domain.BrokerCacheQualificationRequest, error) {
	var wire cacheQualificationRequestWire
	if err := strictDecode(data, 128<<10, &wire); err != nil {
		return domain.BrokerCacheQualificationRequest{}, err
	}
	value := cacheQualificationRequestFromWire(wire)
	if err := validateCacheQualificationRequest(value); err != nil {
		return domain.BrokerCacheQualificationRequest{}, err
	}
	return value, nil
}

func validateCacheQualificationRequest(value domain.BrokerCacheQualificationRequest) error {
	if value.SchemaVersion != SchemaVersion || validateContext(value.Context) != nil || !validDigest(value.SourcePrincipalSHA256) || !validDigest(value.SourceReadScopeSHA256) || !validDigest(value.SelectorSHA256) ||
		!validDigest(value.ProjectionSHA256) || !validDigest(value.EvidenceSchemaSHA256) || !validDigest(value.GenerationSHA256) || !validDigest(value.ContentSHA256) ||
		value.ExpiresAtMillis <= value.Context.ExecutionNotBeforeMillis || value.ExpiresAtMillis > value.Context.ExecutionExpiresMillis {
		return reject(domain.BrokerReasonMalformed)
	}
	definition, ok := Definition(value.Operation, OperationVersion)
	if !ok || !operationBackendMatches(definition, value.Context) {
		return reject(domain.BrokerReasonUnsupported)
	}
	return nil
}

func cacheQualificationRequestToWire(value domain.BrokerCacheQualificationRequest) cacheQualificationRequestWire {
	return cacheQualificationRequestWire{SchemaVersion: value.SchemaVersion, Context: contextToWire(value.Context), SourcePrincipalSHA256: value.SourcePrincipalSHA256, SourceReadScopeSHA256: value.SourceReadScopeSHA256, Operation: string(value.Operation), SelectorSHA256: value.SelectorSHA256, ProjectionSHA256: value.ProjectionSHA256, EvidenceSchemaSHA256: value.EvidenceSchemaSHA256, GenerationSHA256: value.GenerationSHA256, ContentSHA256: value.ContentSHA256, ExpiresAtMillis: value.ExpiresAtMillis}
}

func cacheQualificationRequestFromWire(wire cacheQualificationRequestWire) domain.BrokerCacheQualificationRequest {
	return domain.BrokerCacheQualificationRequest{SchemaVersion: wire.SchemaVersion, Context: contextFromWire(wire.Context), SourcePrincipalSHA256: wire.SourcePrincipalSHA256, SourceReadScopeSHA256: wire.SourceReadScopeSHA256, Operation: domain.BrokerOperationID(wire.Operation), SelectorSHA256: wire.SelectorSHA256, ProjectionSHA256: wire.ProjectionSHA256, EvidenceSchemaSHA256: wire.EvidenceSchemaSHA256, GenerationSHA256: wire.GenerationSHA256, ContentSHA256: wire.ContentSHA256, ExpiresAtMillis: wire.ExpiresAtMillis}
}
