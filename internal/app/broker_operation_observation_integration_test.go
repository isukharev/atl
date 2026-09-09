//go:build linux || darwin

package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/adapter/brokerjournal"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type observationIntegrationAuthorizer struct{}

func (observationIntegrationAuthorizer) Admit(_ context.Context, request domain.BrokerAdmissionRequest) (domain.BrokerAdmissionDecision, error) {
	requestSHA256, _ := brokercontract.AdmissionRequestSHA256(request)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Context)
	value := domain.BrokerAdmissionDecision{BrokerDecisionCore: observationIntegrationDecision("admission", request.Context, contextSHA256, requestSHA256)}
	wire, err := brokercontract.EncodeAdmissionDecisionV1(value)
	if err != nil {
		return domain.BrokerAdmissionDecision{}, err
	}
	return brokercontract.DecodeAdmissionDecisionV1(wire)
}

func (observationIntegrationAuthorizer) AuthorizeQualification(_ context.Context, request domain.BrokerQualificationRequest) (domain.BrokerQualificationDecision, error) {
	requestSHA256, _ := brokercontract.QualificationRequestSHA256(request)
	admissionSHA256, _ := brokercontract.AdmissionRequestSHA256(request.Admission)
	planSHA256, _ := brokercontract.QualificationPlanSHA256(request.Plan)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Admission.Context)
	value := domain.BrokerQualificationDecision{
		BrokerDecisionCore:     observationIntegrationDecision("qualification", request.Admission.Context, contextSHA256, requestSHA256),
		AdmissionRequestSHA256: admissionSHA256, AdmissionDecisionSHA256: request.AdmissionDecision.DecisionSHA256, PlanSHA256: planSHA256,
	}
	wire, err := brokercontract.EncodeQualificationDecisionV1(value)
	if err != nil {
		return domain.BrokerQualificationDecision{}, err
	}
	return brokercontract.DecodeQualificationDecisionV1(wire)
}

func (observationIntegrationAuthorizer) AuthorizeOperation(_ context.Context, request domain.BrokerOperationAuthorizationRequest) (domain.BrokerOperationDecision, error) {
	admission := request.QualificationRequest.Admission
	requestSHA256, _ := brokercontract.OperationAuthorizationRequestSHA256(request)
	resourcesSHA256, _ := brokercontract.QualifiedResourcesSHA256(request.QualifiedResources)
	effectsSHA256, _ := brokercontract.EffectsSHA256(request.Effects)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(admission.Context)
	value := domain.BrokerOperationDecision{
		BrokerDecisionCore:          observationIntegrationDecision("operation", admission.Context, contextSHA256, requestSHA256),
		QualificationDecisionSHA256: request.QualificationDecision.DecisionSHA256,
		Operation:                   admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256,
		ResourcesSHA256: resourcesSHA256, EffectsSHA256: effectsSHA256,
	}
	wire, err := brokercontract.EncodeOperationDecisionV1(value)
	if err != nil {
		return domain.BrokerOperationDecision{}, err
	}
	return brokercontract.DecodeOperationDecisionV1(wire)
}

func (observationIntegrationAuthorizer) AuthorizeProposal(context.Context, domain.BrokerProposalAuthorizationRequest) (domain.BrokerProposalClearance, error) {
	return domain.BrokerProposalClearance{}, domain.ErrUsage
}

func observationIntegrationDecision(id string, verified domain.BrokerVerifiedContext, contextSHA256, requestSHA256 string) domain.BrokerDecisionCore {
	now := time.Now().UnixMilli()
	return domain.BrokerDecisionCore{
		Status: domain.BrokerDecisionAllowed, DecisionID: id, AuthorityRevision: verified.AuthorityRevision,
		ContextSHA256: contextSHA256, RequestSHA256: requestSHA256, IssuedAtMillis: now, ExpiresAtMillis: now + 5_000,
	}
}

type countingJournalLookup struct {
	journal domain.BrokerJournalLookup
	count   int
}

func (l *countingJournalLookup) Lookup(ctx context.Context, owner domain.BrokerJournalOwner, id string) (domain.BrokerJournalRecord, error) {
	l.count++
	return l.journal.Lookup(ctx, owner, id)
}

func TestBrokerOperationObservationReadsReopenedJournalWithoutMutation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	writer := observationIntegrationContext(now, "writer-execution", "writer-epoch", "writer-revision")
	owner, err := brokercontract.BrokerJournalOwnerV1(writer)
	if err != nil {
		t.Fatal(err)
	}
	executionSHA256, revisionSHA256, err := brokercontract.BrokerJournalWriterSHA256V1(writer)
	if err != nil {
		t.Fatal(err)
	}
	// Resolve only this owned fixture's platform alias; production journal
	// paths must still reject symlinked parents (including Darwin's /var alias).
	temp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(temp, "journal")
	journal, err := brokerjournal.Create(path, brokerjournal.Identity{BrokerSHA256: owner.BrokerSHA256, BackendSHA256: owner.BackendSHA256}, brokerjournal.Limits{Records: 2, ReservedBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	reservation := domain.BrokerJournalReservation{
		Owner: owner, ExecutionSHA256: executionSHA256, AuthorityRevisionSHA256: revisionSHA256,
		ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(30 * time.Second).UnixMilli(),
		GrantExpiresMillis: now.Add(30 * time.Second).UnixMilli(), CredentialExpiresMillis: now.Add(30 * time.Second).UnixMilli(),
		OperationDeadlineMillis: now.Add(30 * time.Second).UnixMilli(), ObservationUntilMillis: now.Add(time.Minute).UnixMilli(), ArtifactCapacity: 128,
	}
	record, err := journal.Reserve(t.Context(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	ticket := domain.BrokerOperationTicket{
		SchemaVersion: 1, OperationID: record.OperationID, PrincipalSHA256: owner.PrincipalSHA256, ExecutionSHA256: executionSHA256,
		AudienceSHA256: owner.AudienceSHA256, BackendSHA256: owner.BackendSHA256, Operation: domain.BrokerOperationJiraCommentApply,
		OperationVersion: 1, ArgumentsSHA256: strings.Repeat("2", 64), ProposalSHA256: strings.Repeat("3", 64),
		IssuedAtMillis: record.IssuedAtMillis, AcceptUntilMillis: record.AcceptUntilMillis,
	}
	intent := domain.BrokerJournalIntent{
		Ticket: ticket, NativeSHA256: strings.Repeat("4", 64), TargetSHA256: strings.Repeat("5", 64),
		EffectSHA256: strings.Repeat("6", 64), EvidenceSHA256: strings.Repeat("7", 64),
	}
	record, err = journal.Bind(t.Context(), owner, record.OperationID, intent)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("synthetic recovery evidence")
	record, err = journal.Admit(t.Context(), observationComparison(record), artifact, observationDigest(artifact))
	if err != nil {
		t.Fatal(err)
	}
	record, err = journal.ClaimDispatch(t.Context(), observationComparison(record))
	if err != nil {
		t.Fatal(err)
	}
	record, err = journal.Complete(t.Context(), observationComparison(record), domain.BrokerJournalCompletion{Phase: domain.BrokerOperationApplied, ResultSHA256: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := brokerjournal.Open(path, brokerjournal.Identity{BrokerSHA256: owner.BrokerSHA256, BackendSHA256: owner.BackendSHA256}, brokerjournal.Limits{Records: 2, ReservedBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	observer := observationIntegrationContext(time.Now(), "observer-execution", "observer-epoch", "observer-revision")
	definition, _ := brokercontract.Definition(domain.BrokerOperationOutcomeLookup, 1)
	request := domain.BrokerRequest{
		SchemaVersion: 1, Operation: domain.BrokerOperationOutcomeLookup, OperationVersion: 1, RequestID: "request-1",
		Features:  append([]string{}, definition.RequiredFeatures...),
		Expect:    domain.BrokerRequestExpectations{ExecutionID: observer.ExecutionID, ExecutionEpoch: observer.ExecutionEpoch, AuthorityRevision: observer.AuthorityRevision},
		Arguments: domain.BrokerOperationArguments{Outcome: &domain.BrokerOutcomeArguments{OperationTicket: record.OperationID}},
	}
	lookup := &countingJournalLookup{journal: reopened}
	service, err := app.NewBrokerOperationObservationService(observationIntegrationAuthorizer{}, lookup, observer.Backend)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Observe(t.Context(), request, observer)
	if err != nil || lookup.count != 1 || result.Outcome.Phase != domain.BrokerOperationApplied || !result.Outcome.Complete || !result.Outcome.Reconciled || result.Outcome.ResultSHA256 != record.ResultSHA256 {
		t.Fatalf("result=%+v err=%v lookups=%d", result, err, lookup.count)
	}
	after, err := reopened.Lookup(t.Context(), owner, record.OperationID)
	if err != nil || after != record {
		t.Fatalf("observation mutated durable record: after=%+v err=%v", after, err)
	}

	const observers = 8
	results := make(chan domain.BrokerOperationOutcome, observers)
	errorsSeen := make(chan error, observers)
	var group sync.WaitGroup
	for index := range observers {
		group.Add(1)
		go func() {
			defer group.Done()
			current := observationIntegrationContext(time.Now(), "observer-execution-"+strconv.Itoa(index), "observer-epoch", "observer-revision")
			concurrentRequest := request
			concurrentRequest.RequestID = "request-" + strconv.Itoa(index)
			concurrentRequest.Expect = domain.BrokerRequestExpectations{ExecutionID: current.ExecutionID, ExecutionEpoch: current.ExecutionEpoch, AuthorityRevision: current.AuthorityRevision}
			currentService, serviceErr := app.NewBrokerOperationObservationService(observationIntegrationAuthorizer{}, reopened, current.Backend)
			if serviceErr != nil {
				errorsSeen <- serviceErr
				return
			}
			currentResult, observeErr := currentService.Observe(t.Context(), concurrentRequest, current)
			results <- currentResult.Outcome
			errorsSeen <- observeErr
		}()
	}
	group.Wait()
	close(results)
	close(errorsSeen)
	for observeErr := range errorsSeen {
		if observeErr != nil {
			t.Fatalf("concurrent observation: %v", observeErr)
		}
	}
	for outcome := range results {
		if outcome.Phase != result.Outcome.Phase || outcome.TicketSHA256 != result.Outcome.TicketSHA256 || outcome.ResultSHA256 != result.Outcome.ResultSHA256 {
			t.Fatalf("concurrent outcome=%+v want=%+v", outcome, result.Outcome)
		}
	}
}

func observationIntegrationContext(now time.Time, execution, epoch, revision string) domain.BrokerVerifiedContext {
	return domain.BrokerVerifiedContext{
		PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: execution, ExecutionEpoch: epoch,
		Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: revision,
		ExecutionNotBeforeMillis: now.Add(-time.Second).UnixMilli(), ExecutionExpiresMillis: now.Add(time.Minute).UnixMilli(),
		GrantExpiresMillis: now.Add(time.Minute).UnixMilli(), CredentialExpiresMillis: now.Add(time.Minute).UnixMilli(),
		Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("a", 64), WorkloadBackendID: "jira-primary"},
	}
}

func observationComparison(record domain.BrokerJournalRecord) domain.BrokerJournalCompare {
	return domain.BrokerJournalCompare{Owner: record.Reservation.Owner, OperationID: record.OperationID, BindingSHA256: record.BindingSHA256, Sequence: record.Sequence}
}

func observationDigest(value []byte) string {
	// The fixture intentionally uses the same public SHA-256 representation as
	// the journal contract without opening any artifact through the app service.
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
