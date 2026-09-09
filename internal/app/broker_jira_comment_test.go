package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const brokerJiraCommentTestMillis = int64(1_800_000_000_000)

type brokerJiraCommentAuthorizer struct {
	nowMillis          int64
	leaseMillis        int64
	allowSnapshot      bool
	denyQualification  bool
	finalCalls         int
	denyFinalCall      int
	proposalCalls      int
	denyProposalCall   int
	events             *[]string
	afterAdmission     func()
	afterQualification func()
	afterFinal         func(int)
	afterProposal      func(int)
}

func (a *brokerJiraCommentAuthorizer) event(value string) {
	if a.events != nil {
		*a.events = append(*a.events, value)
	}
}

func (a *brokerJiraCommentAuthorizer) core(phase domain.BrokerAuthorizationPhase, contextSHA256, requestSHA256, revision string, allowed bool) domain.BrokerDecisionCore {
	status, reason := domain.BrokerDecisionAllowed, domain.BrokerReason("")
	if !allowed {
		status, reason = domain.BrokerDecisionDenied, domain.BrokerReasonUnsupportedConsistency
	}
	leaseMillis := a.leaseMillis
	if leaseMillis == 0 {
		leaseMillis = domain.BrokerMaxDecisionLeaseMillis
	}
	return domain.BrokerDecisionCore{Status: status, Reason: reason, DecisionID: string(phase) + "-decision", AuthorityRevision: revision, ContextSHA256: contextSHA256, RequestSHA256: requestSHA256, IssuedAtMillis: a.nowMillis, ExpiresAtMillis: a.nowMillis + leaseMillis}
}

func (a *brokerJiraCommentAuthorizer) Admit(_ context.Context, request domain.BrokerAdmissionRequest) (domain.BrokerAdmissionDecision, error) {
	a.event("admission")
	definition, ok := brokercontract.Definition(request.Operation, request.OperationVersion)
	allowed := a.allowSnapshot && ok && definition.QualificationProfile == domain.BrokerJiraCommentQualificationProfileV1
	requestSHA256, _ := brokercontract.AdmissionRequestSHA256(request)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Context)
	wire, err := brokercontract.EncodeAdmissionDecisionV1(domain.BrokerAdmissionDecision{BrokerDecisionCore: a.core(domain.BrokerPhaseAdmission, contextSHA256, requestSHA256, request.Context.AuthorityRevision, allowed)})
	if a.afterAdmission != nil {
		a.afterAdmission()
	}
	if err != nil {
		return domain.BrokerAdmissionDecision{}, err
	}
	return brokercontract.DecodeAdmissionDecisionV1(wire)
}

func (a *brokerJiraCommentAuthorizer) AuthorizeQualification(_ context.Context, request domain.BrokerQualificationRequest) (domain.BrokerQualificationDecision, error) {
	a.event("qualification_authorization")
	requestSHA256, _ := brokercontract.QualificationRequestSHA256(request)
	admissionSHA256, _ := brokercontract.AdmissionRequestSHA256(request.Admission)
	planSHA256, _ := brokercontract.QualificationPlanSHA256(request.Plan)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Admission.Context)
	value := domain.BrokerQualificationDecision{BrokerDecisionCore: a.core(domain.BrokerPhaseQualificationAuthorization, contextSHA256, requestSHA256, request.Admission.Context.AuthorityRevision, !a.denyQualification), AdmissionRequestSHA256: admissionSHA256, AdmissionDecisionSHA256: request.AdmissionDecision.DecisionSHA256, PlanSHA256: planSHA256}
	wire, err := brokercontract.EncodeQualificationDecisionV1(value)
	if a.afterQualification != nil {
		a.afterQualification()
	}
	if err != nil {
		return domain.BrokerQualificationDecision{}, err
	}
	return brokercontract.DecodeQualificationDecisionV1(wire)
}

func (a *brokerJiraCommentAuthorizer) AuthorizeOperation(_ context.Context, request domain.BrokerOperationAuthorizationRequest) (domain.BrokerOperationDecision, error) {
	a.finalCalls++
	a.event("final_authorization")
	admission := request.QualificationRequest.Admission
	requestSHA256, _ := brokercontract.OperationAuthorizationRequestSHA256(request)
	resourcesSHA256, _ := brokercontract.QualifiedResourcesSHA256(request.QualifiedResources)
	effectsSHA256, _ := brokercontract.EffectsSHA256(request.Effects)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(admission.Context)
	allowed := a.denyFinalCall == 0 || a.finalCalls != a.denyFinalCall
	value := domain.BrokerOperationDecision{BrokerDecisionCore: a.core(domain.BrokerPhaseFinalAuthorization, contextSHA256, requestSHA256, admission.Context.AuthorityRevision, allowed), QualificationDecisionSHA256: request.QualificationDecision.DecisionSHA256, Operation: admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256, ResourcesSHA256: resourcesSHA256, EffectsSHA256: effectsSHA256}
	if a.afterFinal != nil {
		a.afterFinal(a.finalCalls)
	}
	wire, err := brokercontract.EncodeOperationDecisionV1(value)
	if err != nil {
		return domain.BrokerOperationDecision{}, err
	}
	return brokercontract.DecodeOperationDecisionV1(wire)
}

func (a *brokerJiraCommentAuthorizer) AuthorizeProposal(_ context.Context, request domain.BrokerProposalAuthorizationRequest) (domain.BrokerProposalClearance, error) {
	a.proposalCalls++
	a.event("proposal_authorization")
	requestSHA256, _ := brokercontract.ProposalAuthorizationRequestSHA256(request)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.OperationRequest.QualificationRequest.Admission.Context)
	allowed := a.denyProposalCall == 0 || a.proposalCalls != a.denyProposalCall
	value := domain.BrokerProposalClearance{BrokerDecisionCore: a.core(domain.BrokerPhaseProposalClearance, contextSHA256, requestSHA256, request.OperationRequest.QualificationRequest.Admission.Context.AuthorityRevision, allowed), OperationDecisionSHA256: request.OperationDecision.DecisionSHA256, ProposalHash: request.ProposalHash, NativeCandidateSHA256: request.NativeCandidateSHA256, VersionEvidenceSHA256: request.VersionEvidenceSHA256}
	if a.afterProposal != nil {
		a.afterProposal(a.proposalCalls)
	}
	wire, err := brokercontract.EncodeProposalClearanceV1(value)
	if err != nil {
		return domain.BrokerProposalClearance{}, err
	}
	return brokercontract.DecodeProposalClearanceV1(wire)
}

type brokerJiraCommentPort struct {
	origin          string
	events          *[]string
	listPages       int
	responses       map[string]int64
	writeCalls      int
	written         []byte
	unknownReadback bool
	writeErr        error
	qualifiedIssue  *domain.BrokerJiraIssueIdentity
	readbackContext func(context.Context)
	hideWritten     bool
	readIssueCalls  int
	issueOverride   *domain.JiraGuardedCommentIssue
	readIssueErr    error
	inventoryErr    error
	mutateBaseline  bool
	extraCandidates int
	afterWrite      func()
	writeContext    func(context.Context)
}

func (p *brokerJiraCommentPort) event(value string) { *p.events = append(*p.events, value) }
func (p *brokerJiraCommentPort) response(name string) int64 {
	if p.responses != nil {
		return p.responses[name]
	}
	return 1
}
func (p *brokerJiraCommentPort) take(ctx context.Context, attempts int, bytes int64) error {
	budget := domain.ReadBudgetFromContext(ctx)
	for range attempts {
		if err := budget.TakeAttempt(); err != nil {
			return err
		}
	}
	remaining, finish, err := budget.BeginResponse(ctx)
	if err != nil {
		return err
	}
	if remaining < bytes {
		finish(remaining)
		return domain.ErrReadResponseBudgetExhausted
	}
	finish(bytes)
	return nil
}
func (p *brokerJiraCommentPort) BrokerOriginSHA256() (string, error) { return p.origin, nil }
func (p *brokerJiraCommentPort) QualifyBrokerIssue(ctx context.Context, key string) (domain.BrokerJiraIssueIdentity, error) {
	p.event("qualify_issue")
	if err := p.take(ctx, 1, p.response("qualification")); err != nil {
		return domain.BrokerJiraIssueIdentity{}, err
	}
	if p.qualifiedIssue != nil {
		return *p.qualifiedIssue, nil
	}
	return domain.BrokerJiraIssueIdentity{ID: "101", Key: key, Project: "PROJ", Updated: "2027-01-15T08:00:00Z", Complete: true}, nil
}
func (p *brokerJiraCommentPort) ReadGuardedCommentActor(ctx context.Context) (domain.JiraGuardedCommentActor, error) {
	p.event("read_actor")
	if err := p.take(ctx, 1, p.response("actor")); err != nil {
		return domain.JiraGuardedCommentActor{}, err
	}
	return domain.JiraGuardedCommentActor{Name: "writer", Key: "writer-key", Complete: true}, nil
}
func (p *brokerJiraCommentPort) ReadGuardedCommentIssue(ctx context.Context, _ string) (domain.JiraGuardedCommentIssue, error) {
	p.event("read_issue")
	p.readIssueCalls++
	if p.writeCalls > 0 && p.readbackContext != nil {
		p.readbackContext(ctx)
	}
	if err := p.take(ctx, 1, p.response("issue")); err != nil {
		return domain.JiraGuardedCommentIssue{}, err
	}
	if p.readIssueErr != nil {
		return domain.JiraGuardedCommentIssue{}, p.readIssueErr
	}
	if p.issueOverride != nil {
		return *p.issueOverride, nil
	}
	updated := "2027-01-15T08:00:00Z"
	if p.writeCalls > 0 && !p.unknownReadback && !p.hideWritten {
		updated = "2027-01-15T08:00:01Z"
	}
	return domain.JiraGuardedCommentIssue{ID: "101", Key: "PROJ-1", Project: "PROJ", Updated: updated, Complete: true}, nil
}
func (p *brokerJiraCommentPort) ListJiraCommentsQualified(ctx context.Context, _ string, _ domain.JiraCommentReadOptions) (domain.JiraCommentInventory, error) {
	p.event("list_comments")
	if err := p.take(ctx, p.listPages, p.response("inventory")); err != nil {
		return domain.JiraCommentInventory{}, err
	}
	if p.inventoryErr != nil {
		return domain.JiraCommentInventory{}, p.inventoryErr
	}
	comments := []domain.Comment{{ID: "9", AuthorName: "other", AuthorKey: "other-key", Created: "2027-01-14T08:00:00Z", Updated: "2027-01-14T08:00:00Z", Body: "existing"}}
	if p.writeCalls > 0 && p.mutateBaseline {
		comments[0].Body = "mutated"
	}
	if p.writeCalls > 0 && !p.hideWritten {
		comments = append(comments, domain.Comment{ID: "10", AuthorName: "writer", AuthorKey: "writer-key", Created: "2027-01-15T08:00:01Z", Updated: "2027-01-15T08:00:01Z", Body: string(p.written)})
		for index := 0; index < p.extraCandidates; index++ {
			comments = append(comments, domain.Comment{ID: fmt.Sprintf("%d", 11+index), AuthorName: "writer", AuthorKey: "writer-key", Created: "2027-01-15T08:00:01Z", Updated: "2027-01-15T08:00:01Z", Body: string(p.written)})
		}
	}
	return completeCommentInventory(comments), nil
}
func (p *brokerJiraCommentPort) WriteGuardedComment(ctx context.Context, write domain.JiraGuardedCommentWrite) (domain.JiraGuardedCommentAcknowledgement, error) {
	p.event("dispatch_comment")
	if p.writeContext != nil {
		p.writeContext(ctx)
	}
	if writeDefinitelyNotAttempted(p.writeErr) {
		return domain.JiraGuardedCommentAcknowledgement{}, p.writeErr
	}
	if err := p.take(ctx, 1, p.response("write")); err != nil {
		return domain.JiraGuardedCommentAcknowledgement{}, err
	}
	p.writeCalls++
	p.written = append([]byte(nil), write.Body...)
	if p.afterWrite != nil {
		p.afterWrite()
	}
	return domain.JiraGuardedCommentAcknowledgement{ID: "10"}, p.writeErr
}

type brokerJiraCommentNoAttemptError struct{}

func (brokerJiraCommentNoAttemptError) Error() string                  { return "denied before dispatch" }
func (brokerJiraCommentNoAttemptError) DiagnosticWriteAttempted() bool { return false }

type brokerJiraCommentHTTPError struct{ status int }

func (e brokerJiraCommentHTTPError) Error() string   { return "closed HTTP error" }
func (e brokerJiraCommentHTTPError) HTTPStatus() int { return e.status }

type brokerJiraCommentPreflight struct {
	events *[]string
	deny   bool
	last   domain.WriteAuthorizationRequest
}

func (p *brokerJiraCommentPreflight) Preflight(request domain.WriteAuthorizationRequest) error {
	*p.events = append(*p.events, "local_preflight")
	p.last = request
	if p.deny {
		return domain.ErrForbidden
	}
	return nil
}

type brokerJiraCommentJournal struct {
	events             *[]string
	record             domain.BrokerJournalRecord
	artifact           []byte
	completeErr        error
	claimErrAfterState error
	reserveErr         error
	bindErr            error
	admitErr           error
	afterReserve       func()
	afterBind          func()
	afterAdmit         func()
	afterClaim         func()
}

func (j *brokerJiraCommentJournal) event(value string) { *j.events = append(*j.events, value) }
func (j *brokerJiraCommentJournal) Reserve(_ context.Context, reservation domain.BrokerJournalReservation) (domain.BrokerJournalRecord, error) {
	j.event("reserve")
	if j.reserveErr != nil {
		return domain.BrokerJournalRecord{}, j.reserveErr
	}
	id := strings.Repeat("1", 64)
	j.record = domain.BrokerJournalRecord{OperationID: id, Reservation: reservation, IssuedAtMillis: brokerJiraCommentTestMillis, AcceptUntilMillis: min(brokerJiraCommentTestMillis+60_000, reservation.OperationDeadlineMillis), Sequence: 1, RecordedAtMillis: brokerJiraCommentTestMillis}
	if j.afterReserve != nil {
		j.afterReserve()
	}
	return j.record, nil
}
func (j *brokerJiraCommentJournal) Bind(_ context.Context, owner domain.BrokerJournalOwner, id string, intent domain.BrokerJournalIntent) (domain.BrokerJournalRecord, error) {
	j.event("bind")
	if j.bindErr != nil {
		return domain.BrokerJournalRecord{}, j.bindErr
	}
	if owner != j.record.Reservation.Owner || id != j.record.OperationID {
		return domain.BrokerJournalRecord{}, domain.ErrForbidden
	}
	j.record.Intent = intent
	j.record.Sequence++
	sha, err := brokercontract.BrokerJournalIntentBindingSHA256V1(j.record, intent)
	if err != nil {
		return domain.BrokerJournalRecord{}, err
	}
	j.record.BindingSHA256 = sha
	if j.afterBind != nil {
		j.afterBind()
	}
	return j.record, nil
}
func (j *brokerJiraCommentJournal) Lookup(_ context.Context, owner domain.BrokerJournalOwner, id string) (domain.BrokerJournalRecord, error) {
	j.event("lookup")
	if owner != j.record.Reservation.Owner || id != j.record.OperationID || j.record.BindingSHA256 == "" {
		return domain.BrokerJournalRecord{}, domain.ErrForbidden
	}
	return j.record, nil
}
func (j *brokerJiraCommentJournal) Admit(_ context.Context, expected domain.BrokerJournalCompare, artifact []byte, artifactSHA256 string) (domain.BrokerJournalRecord, error) {
	j.event("admit_journal")
	if j.admitErr != nil {
		return domain.BrokerJournalRecord{}, j.admitErr
	}
	if expected != brokerJiraCommentCompare(j.record) || sha256Hex(artifact) != artifactSHA256 || j.record.Phase != "" {
		return domain.BrokerJournalRecord{}, domain.ErrCheckFailed
	}
	j.artifact = append([]byte(nil), artifact...)
	j.record.ArtifactSHA256, j.record.ArtifactBytes = artifactSHA256, int64(len(artifact))
	j.record.Phase, j.record.Sequence = domain.BrokerOperationAdmitted, j.record.Sequence+1
	if j.afterAdmit != nil {
		j.afterAdmit()
	}
	return j.record, nil
}
func (j *brokerJiraCommentJournal) ClaimDispatch(_ context.Context, expected domain.BrokerJournalCompare) (domain.BrokerJournalRecord, error) {
	j.event("claim_dispatch")
	if expected != brokerJiraCommentCompare(j.record) || j.record.Phase != domain.BrokerOperationAdmitted {
		return domain.BrokerJournalRecord{}, domain.ErrCheckFailed
	}
	j.record.Phase, j.record.DispatchClaimed, j.record.Sequence = domain.BrokerOperationDispatching, true, j.record.Sequence+1
	if j.afterClaim != nil {
		j.afterClaim()
	}
	if j.claimErrAfterState != nil {
		return domain.BrokerJournalRecord{}, j.claimErrAfterState
	}
	return j.record, nil
}
func (j *brokerJiraCommentJournal) Complete(_ context.Context, expected domain.BrokerJournalCompare, completion domain.BrokerJournalCompletion) (domain.BrokerJournalRecord, error) {
	j.event("complete_journal")
	if j.completeErr != nil {
		return domain.BrokerJournalRecord{}, j.completeErr
	}
	if expected != brokerJiraCommentCompare(j.record) {
		return domain.BrokerJournalRecord{}, domain.ErrCheckFailed
	}
	j.record.Phase, j.record.ResultSHA256, j.record.Sequence = completion.Phase, completion.ResultSHA256, j.record.Sequence+1
	return j.record, nil
}
func (j *brokerJiraCommentJournal) ReadArtifact(_ context.Context, owner domain.BrokerJournalOwner, id string) ([]byte, error) {
	if owner != j.record.Reservation.Owner || id != j.record.OperationID || len(j.artifact) == 0 {
		return nil, domain.ErrForbidden
	}
	return append([]byte(nil), j.artifact...), nil
}
func (j *brokerJiraCommentJournal) Fenced(context.Context, string) (bool, error) {
	return j.record.Phase == domain.BrokerOperationAdmitted || j.record.Phase == domain.BrokerOperationDispatching || j.record.Phase == domain.BrokerOperationOutcomeUnknown, nil
}

func brokerJiraCommentContext() domain.BrokerVerifiedContext {
	return domain.BrokerVerifiedContext{PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "revision-1", ExecutionNotBeforeMillis: brokerJiraCommentTestMillis - 1_000, ExecutionExpiresMillis: brokerJiraCommentTestMillis + 60_000, GrantExpiresMillis: brokerJiraCommentTestMillis + 60_000, CredentialExpiresMillis: brokerJiraCommentTestMillis + 60_000, Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("a", 64), WorkloadBackendID: "jira-primary"}}
}

func brokerJiraCommentRequest(operation domain.BrokerOperationID, proposal, ticket string) domain.BrokerRequest {
	verified := brokerJiraCommentContext()
	definition, _ := brokercontract.Definition(operation, 1)
	return domain.BrokerRequest{SchemaVersion: 1, Operation: operation, OperationVersion: 1, RequestID: "request-1", Features: append([]string{}, definition.RequiredFeatures...), Expect: domain.BrokerRequestExpectations{ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, AuthorityRevision: verified.AuthorityRevision}, Arguments: domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "PROJ-1", NativeBody: []byte("native *wiki*\n"), SatisfactionPolicy: "append_always", ExpectedProposalHash: proposal, OperationTicket: ticket}}}
}

func brokerJiraCommentFixture(t *testing.T, pages int) (*BrokerJiraCommentService, *brokerJiraCommentAuthorizer, *brokerJiraCommentPort, *brokerJiraCommentJournal, *brokerJiraCommentPreflight, *[]string) {
	t.Helper()
	events := &[]string{}
	authorizer := &brokerJiraCommentAuthorizer{nowMillis: brokerJiraCommentTestMillis, allowSnapshot: true, events: events}
	port := &brokerJiraCommentPort{origin: brokerJiraCommentContext().Backend.OriginSHA256, events: events, listPages: pages}
	journal := &brokerJiraCommentJournal{events: events}
	preflight := &brokerJiraCommentPreflight{events: events}
	service, err := NewBrokerJiraCommentService(authorizer, journal, port, preflight, brokerJiraCommentContext().Backend)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.UnixMilli(brokerJiraCommentTestMillis) }
	return service, authorizer, port, journal, preflight, events
}

func TestBrokerJiraCommentPreviewApplyDurableOrderAndExactBudgets(t *testing.T) {
	service, _, port, journal, preflight, events := brokerJiraCommentFixture(t, 100)
	previewBudget, _ := domain.NewReadBudget(102, 16<<20)
	preview, err := service.Execute(domain.WithReadBudget(t.Context(), previewBudget), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
	if err != nil || preview.Comment.Status != "proposed" || journal.record.Phase != "" || len(journal.artifact) != 0 {
		t.Fatalf("preview=%+v record=%+v err=%v", preview, journal.record, err)
	}
	if usage := previewBudget.Usage(); usage.Attempts != 102 || usage.ResponseBytes != 3 {
		t.Fatalf("preview usage=%+v", usage)
	}
	applyBudget, _ := domain.NewReadBudget(306, 16<<20)
	applyRequest := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
	applyRequest.RequestID = "request-2"
	apply, err := service.Execute(domain.WithReadBudget(t.Context(), applyBudget), applyRequest, brokerJiraCommentContext())
	if err != nil || apply.Comment.Status != "applied" || port.writeCalls != 1 || journal.record.Phase != domain.BrokerOperationApplied {
		t.Fatalf("apply=%+v writes=%d record=%+v err=%v events=%v", apply, port.writeCalls, journal.record, err, *events)
	}
	if usage := applyBudget.Usage(); usage.Attempts != 306 || usage.ResponseBytes != 9 {
		t.Fatalf("apply usage=%+v", usage)
	}
	if string(port.written) != "native *wiki*\n" || len(journal.artifact) == 0 {
		t.Fatalf("written=%q artifact=%d", port.written, len(journal.artifact))
	}
	if len(preflight.last.Targets) != 1 || preflight.last.Targets[0].ID != "101" || preflight.last.Targets[0].Project != "PROJ" {
		t.Fatalf("preflight=%+v", preflight.last)
	}
	wantOrder := []string{"admit_journal", "final_authorization", "proposal_authorization", "local_preflight", "claim_dispatch", "dispatch_comment", "final_authorization", "read_issue", "list_comments", "complete_journal"}
	if !containsOrdered(*events, wantOrder) {
		t.Fatalf("events=%v missing order=%v", *events, wantOrder)
	}
}

func TestBrokerJiraCommentStrongProjectProfileDeniedBeforeProtectedIO(t *testing.T) {
	service, authorizer, port, journal, _, events := brokerJiraCommentFixture(t, 1)
	authorizer.allowSnapshot = false
	_, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
	if err == nil || port.writeCalls != 0 || journal.record.OperationID != "" || len(*events) != 1 || (*events)[0] != "admission" {
		t.Fatalf("err=%v writes=%d record=%+v events=%v", err, port.writeCalls, journal.record, *events)
	}
}

func TestBrokerJiraCommentRejectsDifferentQualifiedSelectorBeforeBusiness(t *testing.T) {
	service, authorizer, port, journal, _, events := brokerJiraCommentFixture(t, 1)
	port.qualifiedIssue = &domain.BrokerJiraIssueIdentity{ID: "102", Key: "PROJ-2", Project: "PROJ", Updated: "2027-01-15T08:00:00Z", Complete: true}
	_, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
	if err == nil || authorizer.finalCalls != 0 || journal.record.OperationID != "" || containsOrdered(*events, []string{"read_actor"}) {
		t.Fatalf("error=%v final_calls=%d events=%v", err, authorizer.finalCalls, *events)
	}
}

func TestBrokerJiraCommentReadbackContextUsesFreshDecisionLease(t *testing.T) {
	service, authorizer, port, _, _, _ := brokerJiraCommentFixture(t, 1)
	preview, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	port.readbackContext = func(ctx context.Context) {
		checked = true
		deadline, ok := ctx.Deadline()
		if !ok || deadline.UnixMilli() > authorizer.nowMillis+domain.BrokerMaxDecisionLeaseMillis {
			t.Errorf("readback deadline=%v exceeds fresh decision lease", deadline)
		}
	}
	request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
	result, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
	if err != nil || result.Comment.Status != "applied" || !checked {
		t.Fatalf("result=%+v error=%v checked=%t", result, err, checked)
	}
}

func TestBrokerJiraCommentLateReadbackCannotPersistApplied(t *testing.T) {
	service, authorizer, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
	service.now = func() time.Time { return time.UnixMilli(authorizer.nowMillis) }
	preview, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
	if err != nil {
		t.Fatal(err)
	}
	port.readbackContext = func(context.Context) {
		authorizer.nowMillis += domain.BrokerMaxDecisionLeaseMillis + 1
	}
	request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
	result, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
	if err == nil || result.Comment.Status == "applied" || port.writeCalls != 1 || journal.record.Phase != domain.BrokerOperationOutcomeUnknown || journal.record.ResultSHA256 != "" {
		t.Fatalf("result=%+v error=%v writes=%d phase=%s digest=%q", result, err, port.writeCalls, journal.record.Phase, journal.record.ResultSHA256)
	}
}

func TestBrokerJiraCommentQualificationAndParentResponseCaps(t *testing.T) {
	for _, test := range []struct {
		name      string
		responses map[string]int64
		wantError bool
		wantBytes int64
	}{
		{"qualification boundary", map[string]int64{"qualification": 64 << 10}, false, 64 << 10},
		{"qualification overflow", map[string]int64{"qualification": (64 << 10) + 1}, true, 64 << 10},
		{"parent boundary", map[string]int64{"qualification": 1, "inventory": (16 << 20) - 1}, false, 16 << 20},
		{"parent overflow", map[string]int64{"qualification": 1, "actor": 16 << 20}, true, 16 << 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
			port.responses = test.responses
			budget, _ := domain.NewReadBudget(102, 16<<20)
			_, err := service.Execute(domain.WithReadBudget(t.Context(), budget), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
			if (err != nil) != test.wantError || budget.Usage().ResponseBytes != test.wantBytes {
				t.Fatalf("err=%v usage=%+v", err, budget.Usage())
			}
			if test.wantError && journal.record.OperationID != "" {
				t.Fatalf("failed response cap reserved %+v", journal.record)
			}
		})
	}
}

func TestBrokerJiraCommentPreviewRequestCapIncludesQualification(t *testing.T) {
	service, _, _, journal, _, _ := brokerJiraCommentFixture(t, 100)
	budget, _ := domain.NewReadBudget(101, 16<<20)
	_, err := service.Execute(domain.WithReadBudget(t.Context(), budget), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
	if !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) || budget.Usage().Attempts != 101 || journal.record.OperationID != "" {
		t.Fatalf("err=%v usage=%+v record=%+v", err, budget.Usage(), journal.record)
	}
}

func TestBrokerJiraCommentPostAdmissionRevocationAndLocalDenialDoNotDispatch(t *testing.T) {
	for _, test := range []struct {
		name string
		set  func(*brokerJiraCommentAuthorizer, *brokerJiraCommentPreflight)
	}{
		{"authority", func(a *brokerJiraCommentAuthorizer, _ *brokerJiraCommentPreflight) { a.denyFinalCall = 3 }},
		{"proposal", func(a *brokerJiraCommentAuthorizer, _ *brokerJiraCommentPreflight) { a.denyProposalCall = 2 }},
		{"local", func(_ *brokerJiraCommentAuthorizer, p *brokerJiraCommentPreflight) { p.deny = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, authorizer, port, journal, preflight, _ := brokerJiraCommentFixture(t, 1)
			preview, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
			if err != nil {
				t.Fatal(err)
			}
			test.set(authorizer, preflight)
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			_, err = service.Execute(t.Context(), request, brokerJiraCommentContext())
			if err == nil || port.writeCalls != 0 || journal.record.Phase != domain.BrokerOperationNotApplied || journal.record.DispatchClaimed {
				t.Fatalf("err=%v writes=%d record=%+v", err, port.writeCalls, journal.record)
			}
		})
	}
}

func TestBrokerJiraCommentDurabilityFailuresDoNotCrossNextBoundary(t *testing.T) {
	for _, test := range []struct {
		name        string
		apply       bool
		set         func(*brokerJiraCommentJournal)
		wantEvent   string
		forbidEvent string
	}{
		{"reserve", false, func(j *brokerJiraCommentJournal) { j.reserveErr = domain.ErrCheckFailed }, "reserve", "bind"},
		{"bind", false, func(j *brokerJiraCommentJournal) { j.bindErr = domain.ErrCheckFailed }, "bind", "lookup"},
		{"admit", true, func(j *brokerJiraCommentJournal) { j.admitErr = domain.ErrCheckFailed }, "admit_journal", "claim_dispatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, port, journal, _, events := brokerJiraCommentFixture(t, 1)
			if !test.apply {
				test.set(journal)
				_, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
				if err == nil || port.writeCalls != 0 || !containsOrdered(*events, []string{test.wantEvent}) || containsOrdered(*events, []string{test.forbidEvent}) {
					t.Fatalf("err=%v writes=%d events=%v", err, port.writeCalls, *events)
				}
				return
			}
			preview, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
			if err != nil {
				t.Fatal(err)
			}
			*events = nil
			test.set(journal)
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			_, err = service.Execute(t.Context(), request, brokerJiraCommentContext())
			if err == nil || port.writeCalls != 0 || !containsOrdered(*events, []string{test.wantEvent}) || containsOrdered(*events, []string{test.forbidEvent}) {
				t.Fatalf("err=%v writes=%d events=%v", err, port.writeCalls, *events)
			}
		})
	}
}

func TestBrokerJiraCommentUnknownReadbackPersistsFence(t *testing.T) {
	service, _, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
	preview, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
	if err != nil {
		t.Fatal(err)
	}
	port.unknownReadback = true
	request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
	result, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
	if err == nil || result.Comment.Status != "outcome_unknown" || port.writeCalls != 1 || journal.record.Phase != domain.BrokerOperationOutcomeUnknown {
		t.Fatalf("result=%+v err=%v writes=%d record=%+v", result, err, port.writeCalls, journal.record)
	}
	fenced, _ := journal.Fenced(t.Context(), journal.record.Intent.TargetSHA256)
	if !fenced {
		t.Fatal("unknown result lost target fence")
	}
}

func TestBrokerJiraCommentLastHopNoAttemptAndDefinitiveRejectionAreTerminal(t *testing.T) {
	for _, test := range []struct {
		name       string
		writeErr   error
		wantWrites int
		wantResult bool
	}{
		{"no attempt", brokerJiraCommentNoAttemptError{}, 0, false},
		{"definitive rejection", brokerJiraCommentHTTPError{status: 403}, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
			preview, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
			if err != nil {
				t.Fatal(err)
			}
			port.writeErr = test.writeErr
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			result, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
			if err == nil || port.writeCalls != test.wantWrites || journal.record.Phase != domain.BrokerOperationNotApplied || !journal.record.DispatchClaimed || (result.Comment.Status != "") != test.wantResult {
				t.Fatalf("result=%+v err=%v writes=%d record=%+v", result, err, port.writeCalls, journal.record)
			}
		})
	}
}

func TestBrokerJiraCommentChangedIntentAndForeignOwnerNeverDispatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*domain.BrokerRequest, *brokerJiraCommentJournal)
	}{
		{"body", func(r *domain.BrokerRequest, _ *brokerJiraCommentJournal) {
			r.Arguments.JiraComment.NativeBody = []byte("changed")
		}},
		{"proposal", func(r *domain.BrokerRequest, _ *brokerJiraCommentJournal) {
			r.Arguments.JiraComment.ExpectedProposalHash = strings.Repeat("f", 64)
		}},
		{"foreign owner", func(_ *domain.BrokerRequest, j *brokerJiraCommentJournal) {
			j.record.Reservation.Owner.PrincipalSHA256 = strings.Repeat("f", 64)
		}},
		{"unbound", func(_ *domain.BrokerRequest, j *brokerJiraCommentJournal) { j.record.BindingSHA256 = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
			preview, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
			if err != nil {
				t.Fatal(err)
			}
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			test.mutate(&request, journal)
			_, err = service.Execute(t.Context(), request, brokerJiraCommentContext())
			if err == nil || port.writeCalls != 0 {
				t.Fatalf("err=%v writes=%d", err, port.writeCalls)
			}
		})
	}
}

func TestBrokerJiraCommentClaimAndTerminalFailuresFenceDuplicates(t *testing.T) {
	for _, test := range []struct {
		name string
		set  func(*brokerJiraCommentJournal)
	}{
		{"claim", func(j *brokerJiraCommentJournal) { j.claimErrAfterState = domain.ErrCheckFailed }},
		{"terminal", func(j *brokerJiraCommentJournal) { j.completeErr = domain.ErrCheckFailed }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
			preview, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
			if err != nil {
				t.Fatal(err)
			}
			test.set(journal)
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			_, err = service.Execute(t.Context(), request, brokerJiraCommentContext())
			if err == nil || journal.record.Phase != domain.BrokerOperationDispatching {
				t.Fatalf("err=%v record=%+v", err, journal.record)
			}
			writes := port.writeCalls
			journal.completeErr = nil
			journal.claimErrAfterState = nil
			_, duplicateErr := service.Execute(t.Context(), request, brokerJiraCommentContext())
			if duplicateErr == nil || port.writeCalls != writes {
				t.Fatalf("duplicate err=%v writes=%d before=%d", duplicateErr, port.writeCalls, writes)
			}
		})
	}
}

func containsOrdered(got, want []string) bool {
	index := 0
	for _, current := range got {
		if index < len(want) && current == want[index] {
			index++
		}
	}
	return index == len(want)
}
