package app

import (
	"context"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func (s *BrokerJiraCommentService) apply(prepared *brokerJiraCommentPrepared, verified domain.BrokerVerifiedContext) (BrokerJiraCommentResult, error) {
	execution := prepared.execution
	if prepared.snapshot.result.ProposalHash != prepared.options.ExpectedProposalHash {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	id := prepared.request.Arguments.JiraComment.OperationTicket
	applyCtx, applyCancel, err := execution.decisionContext(prepared.decisionDeadline)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	defer applyCancel()
	record, err := s.journal.Lookup(applyCtx, prepared.owner, id)
	if err != nil || applyCtx.Err() != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(firstBrokerReadError(err, applyCtx.Err()))
	}
	if err := s.validateApplyRecord(prepared, verified, record); err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	_, clearanceDeadline, err := s.authorizeProposal(applyCtx, prepared, prepared.operation, prepared.decision)
	if err != nil {
		return BrokerJiraCommentResult{}, err
	}
	initialDeadline := min(record.AcceptUntilMillis, prepared.decisionDeadline, clearanceDeadline)
	phaseCtx, phaseCancel, err := execution.businessContext(initialDeadline)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	directPrepared := &jiraGuardedCommentPrepared{result: prepared.snapshot.result, issueID: prepared.identity.ID, body: append([]byte(nil), prepared.options.Body...)}
	prewrite, prewriteErr := qualifyGuardedCommentPrewriteWithBackendHash(phaseCtx, s.jira, prepared.snapshot.result.BackendSHA256, prepared.identity.Key, prepared.options, directPrepared)
	contextErr := phaseCtx.Err()
	phaseCancel()
	if prewriteErr != nil || contextErr != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(firstBrokerReadError(prewriteErr, contextErr))
	}
	prewriteIdentity := domain.BrokerJiraIssueIdentity{ID: prewrite.issue.ID, Key: prewrite.issue.Key, Project: prewrite.issue.Project, Updated: prewrite.issue.Updated, Complete: prewrite.issue.Complete}
	prewriteResource, err := brokercontract.BrokerJiraCommentResourceV1(prewriteIdentity)
	if err != nil || !sameBrokerJiraCommentResource(prewriteResource, prepared.resource) {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	prewriteEffects, prewriteEffectSHA256, err := brokercontract.BrokerJiraCommentEffectsV1(prewriteResource, true)
	if err != nil || prewriteEffectSHA256 != record.Intent.EffectSHA256 {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	recovery, err := brokerJiraCommentRecoveryFromPrewrite(record, prewrite)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	artifact, err := encodeBrokerJiraCommentRecoveryArtifact(recovery)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	admitCtx, admitCancel, err := execution.decisionContext(initialDeadline)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	admitted, err := s.journal.Admit(admitCtx, brokerJiraCommentCompare(record), artifact, sha256Hex(artifact))
	admitContextErr := admitCtx.Err()
	admitCancel()
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	if admitContextErr != nil {
		return s.completeNeverDispatched(admitted, brokerJiraCommentError(admitContextErr))
	}
	operation := domain.BrokerOperationAuthorizationRequest{QualificationRequest: prepared.qualification, QualificationDecision: prepared.qualificationDecision, QualifiedResources: []domain.BrokerQualifiedResource{prewriteResource}, Effects: prewriteEffects}
	authorityCtx, authorityCancel, err := execution.decisionContext(initialDeadline)
	if err != nil {
		return s.completeNeverDispatched(admitted, brokerJiraCommentError(err))
	}
	decision, decisionDeadline, err := s.authorizeOperation(authorityCtx, operation, execution)
	authorityCancel()
	if err != nil {
		return s.completeNeverDispatched(admitted, err)
	}
	prepared.operation, prepared.decision, prepared.decisionDeadline, prepared.resource = operation, decision, decisionDeadline, prewriteResource
	proposalCtx, proposalCancel, err := execution.decisionContext(record.AcceptUntilMillis, decisionDeadline)
	if err != nil {
		return s.completeNeverDispatched(admitted, brokerJiraCommentError(err))
	}
	clearance, clearanceDeadline, err := s.authorizeProposal(proposalCtx, prepared, operation, decision)
	proposalCancel()
	if err != nil {
		return s.completeNeverDispatched(admitted, err)
	}
	dispatchDeadline := min(record.AcceptUntilMillis, decisionDeadline, clearanceDeadline)
	preflight := domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{domain.WriteVerbComment}, Targets: []domain.WriteTarget{{Service: "jira", Kind: "issue", ID: prewrite.issue.ID, Key: prewrite.issue.Key, Project: prewrite.issue.Project}}}
	if err := s.preflight.Preflight(preflight); err != nil {
		return s.completeNeverDispatched(admitted, brokerJiraCommentError(err))
	}
	if err := execution.decisionError(dispatchDeadline); err != nil {
		return s.completeNeverDispatched(admitted, brokerJiraCommentError(err))
	}
	dispatchCtx, dispatchCancel, err := execution.decisionContext(dispatchDeadline)
	if err != nil {
		return s.completeNeverDispatched(admitted, brokerJiraCommentError(err))
	}
	dispatching, err := s.journal.ClaimDispatch(dispatchCtx, brokerJiraCommentCompare(admitted))
	dispatchContextErr := dispatchCtx.Err()
	dispatchCancel()
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	if dispatchContextErr != nil {
		return s.completeClaimedNoDispatch(dispatching, dispatchContextErr)
	}
	if err := brokercontract.MatchRequestContextV1(prepared.request, verified, execution.currentMillis(), execution.deadline.UnixMilli()); err != nil {
		return s.completeClaimedNoDispatch(dispatching, err)
	}
	if err := brokercontract.ValidateOperationDecisionForV1(decision, operation, execution.currentMillis()); err != nil {
		return s.completeClaimedNoDispatch(dispatching, err)
	}
	if err := brokercontract.ValidateProposalClearanceForV1(clearance, brokerJiraCommentProposalRequest(prepared, operation, decision), execution.currentMillis()); err != nil {
		return s.completeClaimedNoDispatch(dispatching, err)
	}
	if err := execution.decisionError(dispatchDeadline); err != nil {
		return s.completeClaimedNoDispatch(dispatching, err)
	}
	phaseCtx, phaseCancel, err = execution.businessContext(dispatchDeadline)
	if err != nil {
		return s.completeClaimedNoDispatch(dispatching, err)
	}
	prepared.snapshot.result.WriteAttempted = true
	ack, writeErr := dispatchGuardedComment(phaseCtx, s.jira, prewrite)
	contextErr = phaseCtx.Err()
	phaseCancel()
	if writeDefinitelyNotAttempted(writeErr) {
		return s.completeClaimedNoDispatch(dispatching, writeErr)
	}
	if writeErr != nil && definitiveWriteRejection(writeErr) {
		prepared.snapshot.result.Status = "not_applied"
		result, resultErr := brokerJiraCommentResultFromDirect(prepared, "not_applied", "", true, false)
		if resultErr != nil {
			return BrokerJiraCommentResult{}, resultErr
		}
		return s.completeResult(prepared, dispatching, result, domain.BrokerOperationNotApplied, writeErr, dispatchDeadline)
	}
	if contextErr != nil && writeErr == nil {
		writeErr = contextErr
	}
	return s.closeoutApply(prepared, dispatching, prewrite, ack, writeErr)
}

func (s *BrokerJiraCommentService) closeoutApply(prepared *brokerJiraCommentPrepared, dispatching domain.BrokerJournalRecord, prewrite *jiraGuardedCommentPrewrite, ack domain.JiraGuardedCommentAcknowledgement, writeErr error) (BrokerJiraCommentResult, error) {
	execution := prepared.execution
	closeout, cancel, err := execution.closeoutContext(dispatching.AcceptUntilMillis)
	if err != nil {
		return s.completeUnknown(dispatching, err)
	}
	defer cancel()
	decision, decisionDeadline, err := s.authorizeOperation(closeout, prepared.operation, execution)
	if err != nil {
		return s.completeUnknown(dispatching, err)
	}
	readback, cancelReadback, err := execution.closeoutContext(dispatching.AcceptUntilMillis, decisionDeadline)
	if err != nil {
		return s.completeUnknown(dispatching, err)
	}
	defer cancelReadback()
	direct, reconcileErr := (&JiraService{}).reconcileGuardedComment(readback, s.jira, prewrite, prepared.identity.Key, prepared.snapshot.result, ack, writeErr)
	validationErr := brokercontract.ValidateOperationDecisionForV1(decision, prepared.operation, execution.currentMillis())
	var decisionErr error
	if execution.currentMillis() >= decisionDeadline {
		decisionErr = context.DeadlineExceeded
	}
	if readback.Err() != nil || validationErr != nil || decisionErr != nil {
		return s.completeUnknown(dispatching, firstBrokerReadError(readback.Err(), firstBrokerReadError(validationErr, decisionErr)))
	}
	status := direct.Status
	if status != "applied" && status != "recovered" {
		result, resultErr := brokerJiraCommentResultFromDirect(prepared, "outcome_unknown", "", true, direct.Reconciled)
		if resultErr != nil {
			return BrokerJiraCommentResult{}, resultErr
		}
		completed, completeErr := s.journal.Complete(closeout, brokerJiraCommentCompare(dispatching), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationOutcomeUnknown})
		_ = completed
		if completeErr != nil {
			return BrokerJiraCommentResult{}, brokerJiraCommentError(completeErr)
		}
		return BrokerJiraCommentResult{Comment: result, ReleaseDeadline: execution.releaseDeadline(dispatching.AcceptUntilMillis, decisionDeadline)}, brokerJiraCommentError(firstBrokerReadError(reconcileErr, domain.ErrCheckFailed))
	}
	result, err := brokerJiraCommentResultFromDirect(prepared, status, direct.CommentID, true, true)
	if err != nil {
		return BrokerJiraCommentResult{}, err
	}
	return s.completeResult(prepared, dispatching, result, domain.BrokerOperationApplied, nil, min(dispatching.AcceptUntilMillis, decisionDeadline))
}

func (s *BrokerJiraCommentService) validateApplyRecord(prepared *brokerJiraCommentPrepared, verified domain.BrokerVerifiedContext, record domain.BrokerJournalRecord) error {
	ticket := record.Intent.Ticket
	nativeSHA256, _ := brokercontract.NativeCandidateSHA256(domain.BrokerOperationJiraCommentApply, prepared.options.Body)
	targetSHA256, _ := brokercontract.BrokerJiraCommentTargetSHA256V1(s.backend, prepared.identity.ID)
	binding, bindingErr := brokercontract.BrokerJournalIntentBindingSHA256V1(record, record.Intent)
	if record.OperationID != prepared.request.Arguments.JiraComment.OperationTicket || record.Reservation.Owner != prepared.owner ||
		record.Reservation.ExecutionSHA256 != prepared.executionSHA256 || record.Reservation.AuthorityRevisionSHA256 != prepared.authoritySHA256 ||
		ticket.ExecutionSHA256 != prepared.executionSHA256 || ticket.ArgumentsSHA256 != prepared.admission.ArgumentsSHA256 ||
		ticket.ProposalSHA256 != prepared.options.ExpectedProposalHash || record.Intent.NativeSHA256 != nativeSHA256 ||
		record.Intent.TargetSHA256 != targetSHA256 || record.Intent.EffectSHA256 != prepared.effectSHA256 || record.Intent.EvidenceSHA256 != prepared.resource.VersionEvidence ||
		record.BindingSHA256 == "" || bindingErr != nil || binding != record.BindingSHA256 || record.Phase != "" || record.DispatchClaimed || record.Sequence < 2 ||
		prepared.execution.currentMillis() < record.Reservation.ExecutionNotBeforeMillis || prepared.execution.currentMillis() >= record.AcceptUntilMillis ||
		verified.ExecutionID != prepared.request.Expect.ExecutionID || verified.ExecutionEpoch != prepared.request.Expect.ExecutionEpoch || verified.AuthorityRevision != prepared.request.Expect.AuthorityRevision {
		return domain.ErrCheckFailed
	}
	return nil
}

func (s *BrokerJiraCommentService) authorizeOperation(ctx context.Context, operation domain.BrokerOperationAuthorizationRequest, execution *brokerJiraCommentExecution) (domain.BrokerOperationDecision, int64, error) {
	callStarted := execution.currentMillis()
	decision, err := s.authorizer.AuthorizeOperation(ctx, operation)
	validationErr := brokercontract.ValidateOperationDecisionForV1(decision, operation, execution.currentMillis())
	if err != nil || ctx.Err() != nil || validationErr != nil {
		return domain.BrokerOperationDecision{}, 0, brokerJiraCommentError(firstBrokerReadError(err, firstBrokerReadError(ctx.Err(), validationErr)))
	}
	deadline := brokerLocalDecisionDeadline(decision.BrokerDecisionCore, callStarted)
	if execution.currentMillis() >= deadline {
		return domain.BrokerOperationDecision{}, 0, brokerJiraCommentError(context.DeadlineExceeded)
	}
	return decision, deadline, nil
}

func (s *BrokerJiraCommentService) authorizeProposal(ctx context.Context, prepared *brokerJiraCommentPrepared, operation domain.BrokerOperationAuthorizationRequest, decision domain.BrokerOperationDecision) (domain.BrokerProposalClearance, int64, error) {
	request := brokerJiraCommentProposalRequest(prepared, operation, decision)
	callStarted := prepared.execution.currentMillis()
	clearance, err := s.authorizer.AuthorizeProposal(ctx, request)
	validationErr := brokercontract.ValidateProposalClearanceForV1(clearance, request, prepared.execution.currentMillis())
	if err != nil || ctx.Err() != nil || validationErr != nil {
		return domain.BrokerProposalClearance{}, 0, brokerJiraCommentError(firstBrokerReadError(err, firstBrokerReadError(ctx.Err(), validationErr)))
	}
	deadline := brokerLocalDecisionDeadline(clearance.BrokerDecisionCore, callStarted)
	if prepared.execution.currentMillis() >= deadline {
		return domain.BrokerProposalClearance{}, 0, brokerJiraCommentError(context.DeadlineExceeded)
	}
	return clearance, deadline, nil
}

func brokerJiraCommentProposalRequest(prepared *brokerJiraCommentPrepared, operation domain.BrokerOperationAuthorizationRequest, decision domain.BrokerOperationDecision) domain.BrokerProposalAuthorizationRequest {
	nativeSHA256, _ := brokercontract.NativeCandidateSHA256(domain.BrokerOperationJiraCommentApply, prepared.options.Body)
	return domain.BrokerProposalAuthorizationRequest{OperationRequest: operation, OperationDecision: decision, ProposalSchemaVersion: 1, ProposalHash: prepared.options.ExpectedProposalHash, NativeCandidateSHA256: nativeSHA256, VersionEvidenceSHA256: operation.QualifiedResources[0].VersionEvidence}
}

func brokerJiraCommentCompare(record domain.BrokerJournalRecord) domain.BrokerJournalCompare {
	return domain.BrokerJournalCompare{Owner: record.Reservation.Owner, OperationID: record.OperationID, BindingSHA256: record.BindingSHA256, Sequence: record.Sequence}
}

func sameBrokerJiraCommentResource(left, right domain.BrokerQualifiedResource) bool {
	return left.Kind == right.Kind && left.ImmutableID == right.ImmutableID && left.Key == right.Key && left.Project == right.Project &&
		left.VersionEvidence == right.VersionEvidence && left.ProjectionSHA256 == right.ProjectionSHA256 &&
		left.AncestorIDs != nil && right.AncestorIDs != nil && len(left.AncestorIDs) == 0 && len(right.AncestorIDs) == 0
}

func brokerJiraCommentCompletionContext(record domain.BrokerJournalRecord) (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.Background(), time.UnixMilli(record.Reservation.OperationDeadlineMillis))
}

func (s *BrokerJiraCommentService) completeNeverDispatched(record domain.BrokerJournalRecord, cause error) (BrokerJiraCommentResult, error) {
	ctx, cancel := brokerJiraCommentCompletionContext(record)
	defer cancel()
	_, err := s.journal.Complete(ctx, brokerJiraCommentCompare(record), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationNotApplied})
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	return BrokerJiraCommentResult{}, cause
}

func (s *BrokerJiraCommentService) completeClaimedNoDispatch(record domain.BrokerJournalRecord, cause error) (BrokerJiraCommentResult, error) {
	proof, err := brokercontract.BrokerJiraCommentNoDispatchSHA256V1(record)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	ctx, cancel := brokerJiraCommentCompletionContext(record)
	defer cancel()
	_, err = s.journal.Complete(ctx, brokerJiraCommentCompare(record), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationNotApplied, ResultSHA256: proof})
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	return BrokerJiraCommentResult{}, brokerJiraCommentError(cause)
}

func (s *BrokerJiraCommentService) completeUnknown(record domain.BrokerJournalRecord, cause error) (BrokerJiraCommentResult, error) {
	ctx, cancel := brokerJiraCommentCompletionContext(record)
	defer cancel()
	_, err := s.journal.Complete(ctx, brokerJiraCommentCompare(record), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationOutcomeUnknown})
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	return BrokerJiraCommentResult{}, brokerJiraCommentError(cause)
}

func (s *BrokerJiraCommentService) completeResult(prepared *brokerJiraCommentPrepared, record domain.BrokerJournalRecord, result domain.BrokerJiraCommentResult, phase domain.BrokerOperationPhase, cause error, releaseMillis int64) (BrokerJiraCommentResult, error) {
	digest, err := brokercontract.BrokerJiraCommentResultSHA256V1(result)
	if err != nil {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	ctx, cancel := brokerJiraCommentCompletionContext(record)
	defer cancel()
	completed, err := s.journal.Complete(ctx, brokerJiraCommentCompare(record), domain.BrokerJournalCompletion{Phase: phase, ResultSHA256: digest})
	if err != nil || completed.ResultSHA256 != digest || completed.Phase != phase {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(firstBrokerReadError(err, domain.ErrCheckFailed))
	}
	if cause != nil {
		return BrokerJiraCommentResult{Comment: result, ReleaseDeadline: prepared.execution.releaseDeadline(releaseMillis, record.AcceptUntilMillis)}, brokerJiraCommentError(cause)
	}
	if prepared.execution.base.Err() != nil || prepared.execution.currentMillis() >= releaseMillis {
		return BrokerJiraCommentResult{}, brokerJiraCommentError(firstBrokerReadError(prepared.execution.base.Err(), context.DeadlineExceeded))
	}
	return BrokerJiraCommentResult{Comment: result, ReleaseDeadline: prepared.execution.releaseDeadline(releaseMillis, record.AcceptUntilMillis)}, nil
}

func brokerJiraCommentResultFromDirect(prepared *brokerJiraCommentPrepared, status, commentID string, writeAttempted, reconciled bool) (domain.BrokerJiraCommentResult, error) {
	nativeSHA256, err := brokercontract.NativeCandidateSHA256(prepared.request.Operation, prepared.options.Body)
	if err != nil {
		return domain.BrokerJiraCommentResult{}, brokerJiraCommentError(err)
	}
	result := domain.BrokerJiraCommentResult{SchemaVersion: 1, ArgumentsSHA256: prepared.admission.ArgumentsSHA256, OperationTicket: prepared.request.Arguments.JiraComment.OperationTicket, Mode: "apply", Status: status, ProposalHash: prepared.options.ExpectedProposalHash, NativeCandidateSHA256: nativeSHA256, VersionEvidenceSHA256: prepared.resource.VersionEvidence, CommentID: commentID, WriteAttempted: writeAttempted, Complete: status != "outcome_unknown", Reconciled: reconciled}
	if err := brokercontract.ValidateJiraCommentResultForV1(result, prepared.request); err != nil {
		return domain.BrokerJiraCommentResult{}, brokerJiraCommentError(domain.ErrCheckFailed)
	}
	return result, nil
}
