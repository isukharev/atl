package app

import (
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func (s *BrokerJiraCommentService) preview(prepared *brokerJiraCommentPrepared, verified domain.BrokerVerifiedContext) (BrokerJiraCommentResult, error) {
	execution := prepared.execution
	previewCtx, cancel, err := execution.decisionContext(prepared.decisionDeadline)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	defer cancel()
	reservation := domain.BrokerJournalReservation{
		Owner: prepared.owner, ExecutionSHA256: prepared.executionSHA256, AuthorityRevisionSHA256: prepared.authoritySHA256,
		ExecutionNotBeforeMillis: verified.ExecutionNotBeforeMillis, ExecutionExpiresMillis: verified.ExecutionExpiresMillis,
		GrantExpiresMillis: verified.GrantExpiresMillis, CredentialExpiresMillis: verified.CredentialExpiresMillis,
		OperationDeadlineMillis: execution.deadline.UnixMilli(), ObservationUntilMillis: execution.startedAt.Add(brokerJiraCommentObservationWindow).UnixMilli(),
		ArtifactCapacity: brokerJiraCommentRecoveryMaxBytes,
	}
	record, err := s.journal.Reserve(previewCtx, reservation)
	if err != nil || previewCtx.Err() != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(firstBrokerReadError(err, previewCtx.Err()))
	}
	if err := brokercontract.ValidateOperationDecisionForV1(prepared.decision, prepared.operation, execution.currentMillis()); err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	applyDefinition, ok := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	if !ok || brokercontract.ValidateBrokerJiraCommentDefinitionV1(applyDefinition, domain.BrokerOperationJiraCommentApply) != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	applyRequest := prepared.request
	applyRequest.Operation = domain.BrokerOperationJiraCommentApply
	applyRequest.Features = append([]string{}, applyDefinition.RequiredFeatures...)
	applyArguments := *prepared.request.Arguments.JiraComment
	applyRequest.Arguments.JiraComment = &applyArguments
	applyRequest.Arguments.JiraComment.ExpectedProposalHash = prepared.snapshot.result.ProposalHash
	applyRequest.Arguments.JiraComment.OperationTicket = record.OperationID
	argumentsSHA256, err := brokercontract.ArgumentsSHA256(applyRequest)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	nativeSHA256, err := brokercontract.NativeCandidateSHA256(domain.BrokerOperationJiraCommentApply, prepared.options.Body)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	_, effectSHA256, err := brokercontract.BrokerJiraCommentEffectsV1(prepared.resource, true)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	targetSHA256, err := brokercontract.BrokerJiraCommentTargetSHA256V1(s.backend, prepared.identity.ID)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	ticket := domain.BrokerOperationTicket{
		SchemaVersion: brokercontract.SchemaVersion, OperationID: record.OperationID,
		PrincipalSHA256: prepared.owner.PrincipalSHA256, ExecutionSHA256: prepared.executionSHA256,
		AudienceSHA256: prepared.owner.AudienceSHA256, BackendSHA256: prepared.owner.BackendSHA256,
		Operation: domain.BrokerOperationJiraCommentApply, OperationVersion: 1,
		ArgumentsSHA256: argumentsSHA256, ProposalSHA256: prepared.snapshot.result.ProposalHash,
		IssuedAtMillis: record.IssuedAtMillis, AcceptUntilMillis: record.AcceptUntilMillis,
	}
	intent := domain.BrokerJournalIntent{Ticket: ticket, NativeSHA256: nativeSHA256, TargetSHA256: targetSHA256, EffectSHA256: effectSHA256, EvidenceSHA256: prepared.resource.VersionEvidence}
	record, err = s.journal.Bind(previewCtx, prepared.owner, record.OperationID, intent)
	if err != nil || previewCtx.Err() != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(firstBrokerReadError(err, previewCtx.Err()))
	}
	binding, err := brokercontract.BrokerJournalIntentBindingSHA256V1(record, intent)
	if err != nil || binding != record.BindingSHA256 {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	previewNativeSHA256, err := brokercontract.NativeCandidateSHA256(domain.BrokerOperationJiraCommentPreview, prepared.options.Body)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	result := domain.BrokerJiraCommentResult{
		SchemaVersion: brokercontract.SchemaVersion, ArgumentsSHA256: prepared.admission.ArgumentsSHA256,
		OperationTicket: record.OperationID, Mode: "preview", Status: "proposed",
		ProposalHash: prepared.snapshot.result.ProposalHash, NativeCandidateSHA256: previewNativeSHA256,
		VersionEvidenceSHA256: prepared.resource.VersionEvidence, Complete: true,
	}
	if err := brokercontract.ValidateJiraCommentResultForV1(result, prepared.request); err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	if prepared.execution.base.Err() != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(prepared.execution.base.Err())
	}
	if err := brokercontract.ValidateOperationDecisionForV1(prepared.decision, prepared.operation, execution.currentMillis()); err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	if err := execution.decisionError(prepared.decisionDeadline); err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	return BrokerJiraCommentResult{Comment: result, ReleaseDeadline: execution.releaseDeadline(prepared.decisionDeadline, record.AcceptUntilMillis)}, nil
}
