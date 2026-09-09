package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

const brokerObservationTestMillis = int64(10_000)

type brokerObservationAuthorizer struct {
	nowMillis          int64
	leaseMillis        int64
	denyPhase          domain.BrokerAuthorizationPhase
	calls              []domain.BrokerAuthorizationPhase
	operation          domain.BrokerOperationAuthorizationRequest
	afterAdmission     func()
	afterQualification func()
	afterFinal         func()
}

func (a *brokerObservationAuthorizer) Admit(_ context.Context, request domain.BrokerAdmissionRequest) (domain.BrokerAdmissionDecision, error) {
	a.calls = append(a.calls, domain.BrokerPhaseAdmission)
	requestSHA256, _ := brokercontract.AdmissionRequestSHA256(request)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Context)
	value := domain.BrokerAdmissionDecision{BrokerDecisionCore: a.core(domain.BrokerPhaseAdmission, contextSHA256, requestSHA256, request.Context.AuthorityRevision)}
	wire, err := brokercontract.EncodeAdmissionDecisionV1(value)
	if a.afterAdmission != nil {
		a.afterAdmission()
	}
	if err != nil {
		return domain.BrokerAdmissionDecision{}, err
	}
	return brokercontract.DecodeAdmissionDecisionV1(wire)
}

func (a *brokerObservationAuthorizer) AuthorizeQualification(_ context.Context, request domain.BrokerQualificationRequest) (domain.BrokerQualificationDecision, error) {
	a.calls = append(a.calls, domain.BrokerPhaseQualificationAuthorization)
	requestSHA256, _ := brokercontract.QualificationRequestSHA256(request)
	admissionSHA256, _ := brokercontract.AdmissionRequestSHA256(request.Admission)
	planSHA256, _ := brokercontract.QualificationPlanSHA256(request.Plan)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(request.Admission.Context)
	value := domain.BrokerQualificationDecision{
		BrokerDecisionCore:     a.core(domain.BrokerPhaseQualificationAuthorization, contextSHA256, requestSHA256, request.Admission.Context.AuthorityRevision),
		AdmissionRequestSHA256: admissionSHA256, AdmissionDecisionSHA256: request.AdmissionDecision.DecisionSHA256, PlanSHA256: planSHA256,
	}
	wire, err := brokercontract.EncodeQualificationDecisionV1(value)
	if a.afterQualification != nil {
		a.afterQualification()
	}
	if err != nil {
		return domain.BrokerQualificationDecision{}, err
	}
	return brokercontract.DecodeQualificationDecisionV1(wire)
}

func (a *brokerObservationAuthorizer) AuthorizeOperation(_ context.Context, request domain.BrokerOperationAuthorizationRequest) (domain.BrokerOperationDecision, error) {
	a.calls = append(a.calls, domain.BrokerPhaseFinalAuthorization)
	a.operation = request
	admission := request.QualificationRequest.Admission
	requestSHA256, _ := brokercontract.OperationAuthorizationRequestSHA256(request)
	resourcesSHA256, _ := brokercontract.QualifiedResourcesSHA256(request.QualifiedResources)
	effectsSHA256, _ := brokercontract.EffectsSHA256(request.Effects)
	contextSHA256, _ := brokercontract.VerifiedContextSHA256(admission.Context)
	value := domain.BrokerOperationDecision{
		BrokerDecisionCore:          a.core(domain.BrokerPhaseFinalAuthorization, contextSHA256, requestSHA256, admission.Context.AuthorityRevision),
		QualificationDecisionSHA256: request.QualificationDecision.DecisionSHA256,
		Operation:                   admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256,
		ResourcesSHA256: resourcesSHA256, EffectsSHA256: effectsSHA256,
	}
	wire, err := brokercontract.EncodeOperationDecisionV1(value)
	if a.afterFinal != nil {
		a.afterFinal()
	}
	if err != nil {
		return domain.BrokerOperationDecision{}, err
	}
	return brokercontract.DecodeOperationDecisionV1(wire)
}

func (a *brokerObservationAuthorizer) AuthorizeProposal(context.Context, domain.BrokerProposalAuthorizationRequest) (domain.BrokerProposalClearance, error) {
	return domain.BrokerProposalClearance{}, domain.ErrUsage
}

func (a *brokerObservationAuthorizer) core(phase domain.BrokerAuthorizationPhase, contextSHA256, requestSHA256, revision string) domain.BrokerDecisionCore {
	status, reason := domain.BrokerDecisionAllowed, domain.BrokerReason("")
	if phase == a.denyPhase {
		status, reason = domain.BrokerDecisionDenied, domain.BrokerReasonDenied
	}
	leaseMillis := a.leaseMillis
	if leaseMillis == 0 {
		leaseMillis = 5_000
	}
	return domain.BrokerDecisionCore{
		Status: status, Reason: reason, DecisionID: string(phase) + "-1", AuthorityRevision: revision,
		ContextSHA256: contextSHA256, RequestSHA256: requestSHA256,
		IssuedAtMillis: a.nowMillis, ExpiresAtMillis: a.nowMillis + leaseMillis,
	}
}

type brokerObservationJournal struct {
	record        domain.BrokerJournalRecord
	err           error
	wantOwner     *domain.BrokerJournalOwner
	lookups       int
	observedOwner domain.BrokerJournalOwner
	observedID    string
	afterLookup   func()
	waitForExpiry bool
	deadline      time.Time
}

func (j *brokerObservationJournal) Lookup(ctx context.Context, owner domain.BrokerJournalOwner, id string) (domain.BrokerJournalRecord, error) {
	j.lookups++
	j.observedOwner, j.observedID = owner, id
	j.deadline, _ = ctx.Deadline()
	if j.waitForExpiry {
		<-ctx.Done()
	}
	if j.afterLookup != nil {
		j.afterLookup()
	}
	if j.wantOwner != nil && owner != *j.wantOwner {
		return domain.BrokerJournalRecord{}, domain.ErrForbidden
	}
	return j.record, j.err
}

func brokerObservationContext() domain.BrokerVerifiedContext {
	return domain.BrokerVerifiedContext{
		PrincipalID: "principal-1", WorkloadID: "workload-1", ExecutionID: "observation-execution", ExecutionEpoch: "observation-epoch",
		Audience: "atl-broker", BrokerID: "broker-1", AuthorityRevision: "observation-revision",
		ExecutionNotBeforeMillis: brokerObservationTestMillis - 1_000, ExecutionExpiresMillis: brokerObservationTestMillis + 60_000,
		GrantExpiresMillis: brokerObservationTestMillis + 60_000, CredentialExpiresMillis: brokerObservationTestMillis + 60_000,
		Backend: domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.Repeat("a", 64), WorkloadBackendID: "jira-primary"},
	}
}

func brokerObservationRequest() domain.BrokerRequest {
	context := brokerObservationContext()
	definition, _ := brokercontract.Definition(domain.BrokerOperationOutcomeLookup, brokercontract.OperationVersion)
	return domain.BrokerRequest{
		SchemaVersion: brokercontract.SchemaVersion, Operation: domain.BrokerOperationOutcomeLookup, OperationVersion: brokercontract.OperationVersion,
		RequestID: "request-1", Features: append([]string{}, definition.RequiredFeatures...),
		Expect:    domain.BrokerRequestExpectations{ExecutionID: context.ExecutionID, ExecutionEpoch: context.ExecutionEpoch, AuthorityRevision: context.AuthorityRevision},
		Arguments: domain.BrokerOperationArguments{Outcome: &domain.BrokerOutcomeArguments{OperationTicket: strings.Repeat("1", 64)}},
	}
}

func brokerObservationRecord(t *testing.T, phase domain.BrokerOperationPhase) domain.BrokerJournalRecord {
	t.Helper()
	writer := brokerObservationContext()
	writer.ExecutionID, writer.ExecutionEpoch, writer.AuthorityRevision = "writer-execution", "writer-epoch", "writer-revision"
	writer.ExecutionNotBeforeMillis, writer.ExecutionExpiresMillis = 500, 5_000
	writer.GrantExpiresMillis, writer.CredentialExpiresMillis = 5_000, 5_000
	owner, err := brokercontract.BrokerJournalOwnerV1(writer)
	if err != nil {
		t.Fatal(err)
	}
	executionSHA256, revisionSHA256, err := brokercontract.BrokerJournalWriterSHA256V1(writer)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("1", 64)
	reservation := domain.BrokerJournalReservation{
		Owner: owner, ExecutionSHA256: executionSHA256, AuthorityRevisionSHA256: revisionSHA256,
		ExecutionNotBeforeMillis: 500, ExecutionExpiresMillis: 5_000, GrantExpiresMillis: 5_000, CredentialExpiresMillis: 5_000,
		OperationDeadlineMillis: 5_000, ObservationUntilMillis: brokerObservationTestMillis + 3_000, ArtifactCapacity: 128,
	}
	ticket := domain.BrokerOperationTicket{
		SchemaVersion: brokercontract.SchemaVersion, OperationID: id, PrincipalSHA256: owner.PrincipalSHA256,
		ExecutionSHA256: executionSHA256, AudienceSHA256: owner.AudienceSHA256, BackendSHA256: owner.BackendSHA256,
		Operation: domain.BrokerOperationJiraCommentApply, OperationVersion: brokercontract.OperationVersion,
		ArgumentsSHA256: strings.Repeat("2", 64), ProposalSHA256: strings.Repeat("3", 64), IssuedAtMillis: 1_000, AcceptUntilMillis: 5_000,
	}
	record := domain.BrokerJournalRecord{
		OperationID: id, Reservation: reservation, IssuedAtMillis: 1_000, AcceptUntilMillis: 5_000,
		Intent:   domain.BrokerJournalIntent{Ticket: ticket, NativeSHA256: strings.Repeat("4", 64), TargetSHA256: strings.Repeat("5", 64), EffectSHA256: strings.Repeat("6", 64), EvidenceSHA256: strings.Repeat("7", 64)},
		Sequence: 3, Phase: phase,
		ArtifactSHA256: strings.Repeat("9", 64), ArtifactBytes: 32, RecordedAtMillis: 4_000,
	}
	switch phase {
	case domain.BrokerOperationDispatching:
		record.Sequence, record.DispatchClaimed = 4, true
	case domain.BrokerOperationOutcomeUnknown:
		record.Sequence, record.DispatchClaimed = 5, true
	case domain.BrokerOperationApplied:
		record.Sequence, record.DispatchClaimed, record.ResultSHA256 = 5, true, strings.Repeat("b", 64)
	case domain.BrokerOperationNotApplied:
		record.Sequence = 4
	}
	record.BindingSHA256, err = brokercontract.BrokerJournalIntentBindingSHA256V1(record, record.Intent)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func brokerObservationService(t *testing.T, authorizer *brokerObservationAuthorizer, journal domain.BrokerJournalLookup, now *time.Time) *BrokerOperationObservationService {
	t.Helper()
	service, err := NewBrokerOperationObservationService(authorizer, journal, brokerObservationContext().Backend)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return *now }
	return service
}

func TestBrokerOperationObservationProjectsStoredPhaseTruth(t *testing.T) {
	for _, test := range []struct {
		phase      domain.BrokerOperationPhase
		complete   bool
		reconciled bool
	}{
		{domain.BrokerOperationAdmitted, false, false},
		{domain.BrokerOperationDispatching, false, false},
		{domain.BrokerOperationApplied, true, true},
		{domain.BrokerOperationNotApplied, true, false},
		{domain.BrokerOperationOutcomeUnknown, false, false},
	} {
		t.Run(string(test.phase), func(t *testing.T) {
			now := time.UnixMilli(brokerObservationTestMillis)
			record := brokerObservationRecord(t, test.phase)
			record.Sequence = 7
			before := record
			authorizer := &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis}
			journal := &brokerObservationJournal{record: record}
			service := brokerObservationService(t, authorizer, journal, &now)
			result, err := service.Observe(t.Context(), brokerObservationRequest(), brokerObservationContext())
			wantTicketSHA256, _ := brokercontract.OperationTicketSHA256(record.Intent.Ticket)
			wantResource, _ := brokercontract.BrokerOperationObservationResourceV1(record.OperationID)
			if err != nil || result.Outcome.Phase != test.phase || result.Outcome.Complete != test.complete || result.Outcome.Reconciled != test.reconciled ||
				result.Outcome.TicketSHA256 != wantTicketSHA256 || result.Outcome.ResultSHA256 != record.ResultSHA256 || result.Outcome.ObservedAtMillis != brokerObservationTestMillis ||
				!result.ReleaseDeadline.Equal(time.UnixMilli(record.Reservation.ObservationUntilMillis)) || journal.lookups != 1 || journal.observedID != record.OperationID || journal.record != before {
				t.Fatalf("result=%+v err=%v lookups=%d record changed=%t", result, err, journal.lookups, journal.record != before)
			}
			if !reflect.DeepEqual(authorizer.calls, []domain.BrokerAuthorizationPhase{domain.BrokerPhaseAdmission, domain.BrokerPhaseQualificationAuthorization, domain.BrokerPhaseFinalAuthorization}) ||
				len(authorizer.operation.QualifiedResources) != 1 || !reflect.DeepEqual(authorizer.operation.QualifiedResources[0], wantResource) ||
				len(authorizer.operation.Effects) != 1 || authorizer.operation.Effects[0].Kind != domain.BrokerEffectObserve || !reflect.DeepEqual(authorizer.operation.Effects[0].Resource, wantResource) || len(authorizer.operation.Effects[0].Fields) != 0 {
				t.Fatalf("authorization=%+v calls=%v", authorizer.operation, authorizer.calls)
			}
			if _, err := brokercontract.EncodeOperationOutcomeV1(result.Outcome); err != nil {
				t.Fatalf("outcome does not re-encode: %v", err)
			}
		})
	}
}

func TestBrokerOperationObservationDeniesBeforeLookupAtEveryAuthorityBoundary(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*domain.BrokerRequest, *domain.BrokerVerifiedContext)
		denyPhase domain.BrokerAuthorizationPhase
		wantCalls int
	}{
		{"malformed", func(r *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) { r.RequestID = "" }, "", 0},
		{"backend mismatch", func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext) {
			c.Backend.WorkloadBackendID = "jira-secondary"
		}, "", 0},
		{"admission", nil, domain.BrokerPhaseAdmission, 1},
		{"qualification", nil, domain.BrokerPhaseQualificationAuthorization, 2},
		{"final observe", nil, domain.BrokerPhaseFinalAuthorization, 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.UnixMilli(brokerObservationTestMillis)
			request, verified := brokerObservationRequest(), brokerObservationContext()
			if test.mutate != nil {
				test.mutate(&request, &verified)
			}
			authorizer := &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis, denyPhase: test.denyPhase}
			journal := &brokerObservationJournal{record: brokerObservationRecord(t, domain.BrokerOperationApplied)}
			service := brokerObservationService(t, authorizer, journal, &now)
			result, err := service.Observe(t.Context(), request, verified)
			if err == nil || result != (BrokerOperationObservationResult{}) || len(authorizer.calls) != test.wantCalls || journal.lookups != 0 {
				t.Fatalf("result=%+v err=%v calls=%v lookups=%d", result, err, authorizer.calls, journal.lookups)
			}
		})
	}
}

func TestBrokerOperationObservationForeignUnknownAndUnboundAreIndistinguishable(t *testing.T) {
	base := brokerObservationRecord(t, domain.BrokerOperationApplied)
	cases := []struct {
		name   string
		mutate func(*domain.BrokerRequest, *domain.BrokerVerifiedContext, *brokerObservationJournal)
	}{
		{"unknown", func(_ *domain.BrokerRequest, _ *domain.BrokerVerifiedContext, j *brokerObservationJournal) {
			j.err = fmt.Errorf("private journal path: %w", domain.ErrForbidden)
		}},
		{"foreign principal", func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext, j *brokerObservationJournal) {
			c.PrincipalID = "principal-2"
			j.wantOwner = &base.Reservation.Owner
		}},
		{"foreign workload", func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext, j *brokerObservationJournal) {
			c.WorkloadID = "workload-2"
			j.wantOwner = &base.Reservation.Owner
		}},
		{"foreign backend origin", func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext, j *brokerObservationJournal) {
			c.Backend.OriginSHA256 = strings.Repeat("c", 64)
			j.wantOwner = &base.Reservation.Owner
		}},
		{"foreign backend workload", func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext, j *brokerObservationJournal) {
			c.Backend.WorkloadBackendID = "jira-secondary"
			j.wantOwner = &base.Reservation.Owner
		}},
		{"foreign audience", func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext, j *brokerObservationJournal) {
			c.Audience = "other-audience"
			j.wantOwner = &base.Reservation.Owner
		}},
		{"foreign broker", func(_ *domain.BrokerRequest, c *domain.BrokerVerifiedContext, j *brokerObservationJournal) {
			c.BrokerID = "broker-2"
			j.wantOwner = &base.Reservation.Owner
		}},
		{"unbound", func(_ *domain.BrokerRequest, _ *domain.BrokerVerifiedContext, j *brokerObservationJournal) {
			j.record.BindingSHA256, j.record.Phase = "", ""
		}},
		{"non-digest id", func(r *domain.BrokerRequest, _ *domain.BrokerVerifiedContext, j *brokerObservationJournal) {
			r.Arguments.Outcome.OperationTicket = "unknown-ticket"
			j.err = domain.ErrForbidden
		}},
	}
	var wantError string
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			now := time.UnixMilli(brokerObservationTestMillis)
			request := brokerObservationRequest()
			verified := brokerObservationContext()
			journal := &brokerObservationJournal{record: base}
			test.mutate(&request, &verified, journal)
			authorizer := &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis}
			service, newErr := NewBrokerOperationObservationService(authorizer, journal, verified.Backend)
			if newErr != nil {
				t.Fatal(newErr)
			}
			service.now = func() time.Time { return now }
			result, err := service.Observe(t.Context(), request, verified)
			if err == nil || !errors.Is(err, domain.ErrForbidden) || result != (BrokerOperationObservationResult{}) || journal.lookups != 1 || strings.Contains(err.Error(), "private") {
				t.Fatalf("result=%+v err=%v lookups=%d", result, err, journal.lookups)
			}
			if wantError == "" {
				wantError = err.Error()
			} else if err.Error() != wantError {
				t.Fatalf("observable error differs: %q / %q", err.Error(), wantError)
			}
		})
	}
}

func TestBrokerOperationObservationCurrentAuthorityAndFixedDeadline(t *testing.T) {
	now := time.UnixMilli(brokerObservationTestMillis)
	record := brokerObservationRecord(t, domain.BrokerOperationApplied)
	journal := &brokerObservationJournal{record: record, afterLookup: func() { now = time.UnixMilli(record.Reservation.ObservationUntilMillis) }}
	authorizer := &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis}
	result, err := brokerObservationService(t, authorizer, journal, &now).Observe(t.Context(), brokerObservationRequest(), brokerObservationContext())
	if err == nil || !errors.Is(err, domain.ErrForbidden) || result != (BrokerOperationObservationResult{}) || journal.lookups != 1 {
		t.Fatalf("expired observation result=%+v err=%v lookups=%d", result, err, journal.lookups)
	}

	now = time.UnixMilli(brokerObservationTestMillis)
	verified := brokerObservationContext()
	verified.GrantExpiresMillis = brokerObservationTestMillis
	journal = &brokerObservationJournal{record: record}
	authorizer = &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis}
	if _, err := brokerObservationService(t, authorizer, journal, &now).Observe(t.Context(), brokerObservationRequest(), verified); err == nil || journal.lookups != 0 || len(authorizer.calls) != 0 {
		t.Fatalf("expired current grant err=%v calls=%v lookups=%d", err, authorizer.calls, journal.lookups)
	}

	now = time.UnixMilli(brokerObservationTestMillis)
	journal = &brokerObservationJournal{record: record, afterLookup: func() { now = time.UnixMilli(brokerObservationTestMillis - 5_000) }}
	authorizer = &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis}
	result, err = brokerObservationService(t, authorizer, journal, &now).Observe(t.Context(), brokerObservationRequest(), brokerObservationContext())
	if err != nil || result.Outcome.ObservedAtMillis != brokerObservationTestMillis || !result.ReleaseDeadline.Equal(time.UnixMilli(record.Reservation.ObservationUntilMillis)) {
		t.Fatalf("clock rollback changed observation or deadline: result=%+v err=%v", result, err)
	}
}

func TestBrokerOperationObservationCancellationAndLateDecisionPublishNothing(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*time.Time, *brokerObservationAuthorizer, *brokerObservationJournal, context.CancelFunc)
	}{
		{"canceled after lookup", func(_ *time.Time, _ *brokerObservationAuthorizer, j *brokerObservationJournal, cancel context.CancelFunc) {
			j.afterLookup = cancel
		}},
		{"final decision expires", func(now *time.Time, a *brokerObservationAuthorizer, _ *brokerObservationJournal, _ context.CancelFunc) {
			a.afterFinal = func() { *now = time.UnixMilli(brokerObservationTestMillis + 5_000) }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.UnixMilli(brokerObservationTestMillis)
			authorizer := &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis}
			journal := &brokerObservationJournal{record: brokerObservationRecord(t, domain.BrokerOperationApplied)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			test.setup(&now, authorizer, journal, cancel)
			result, err := brokerObservationService(t, authorizer, journal, &now).Observe(ctx, brokerObservationRequest(), brokerObservationContext())
			if err == nil || result != (BrokerOperationObservationResult{}) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if test.name == "final decision expires" && journal.lookups != 0 {
				t.Fatalf("late decision reached lookup: %d", journal.lookups)
			}
		})
	}
}

func TestBrokerOperationObservationLocalDecisionLeasesBoundNextBoundary(t *testing.T) {
	for _, stage := range []string{"admission", "qualification", "operation"} {
		t.Run(stage, func(t *testing.T) {
			localNow := time.UnixMilli(brokerObservationTestMillis)
			authorizer := &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis + domain.BrokerClockAllowanceMillis}
			delay := func() { localNow = time.UnixMilli(brokerObservationTestMillis + domain.BrokerMaxDecisionLeaseMillis) }
			switch stage {
			case "admission":
				authorizer.afterAdmission = delay
			case "qualification":
				authorizer.afterQualification = delay
			case "operation":
				authorizer.afterFinal = delay
			}
			journal := &brokerObservationJournal{record: brokerObservationRecord(t, domain.BrokerOperationApplied)}
			result, err := brokerObservationService(t, authorizer, journal, &localNow).Observe(t.Context(), brokerObservationRequest(), brokerObservationContext())
			wantCalls := map[string]int{"admission": 1, "qualification": 2, "operation": 3}[stage]
			if !errors.Is(err, context.DeadlineExceeded) || result != (BrokerOperationObservationResult{}) || len(authorizer.calls) != wantCalls || journal.lookups != 0 {
				t.Fatalf("result=%+v error=%v calls=%v lookups=%d", result, err, authorizer.calls, journal.lookups)
			}
		})
	}
}

func TestBrokerOperationObservationBoundsLookupByFinalDecision(t *testing.T) {
	now := time.UnixMilli(brokerObservationTestMillis)
	record := brokerObservationRecord(t, domain.BrokerOperationApplied)
	journal := &brokerObservationJournal{record: record}
	authorizer := &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis + domain.BrokerClockAllowanceMillis, leaseMillis: 2_000}
	result, err := brokerObservationService(t, authorizer, journal, &now).Observe(t.Context(), brokerObservationRequest(), brokerObservationContext())
	remaining := time.Until(journal.deadline)
	if err != nil || journal.lookups != 1 || remaining <= 0 || remaining > 2*time.Second || !result.ReleaseDeadline.Equal(time.UnixMilli(brokerObservationTestMillis+2_000)) {
		t.Fatalf("result=%+v err=%v lookups=%d remaining=%v", result, err, journal.lookups, remaining)
	}
}

func TestBrokerOperationObservationRejectsLookupThatOutlivesDecision(t *testing.T) {
	now := time.UnixMilli(brokerObservationTestMillis)
	journal := &brokerObservationJournal{record: brokerObservationRecord(t, domain.BrokerOperationApplied), waitForExpiry: true}
	authorizer := &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis, leaseMillis: 50}
	result, err := brokerObservationService(t, authorizer, journal, &now).Observe(t.Context(), brokerObservationRequest(), brokerObservationContext())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || result != (BrokerOperationObservationResult{}) || journal.lookups != 1 {
		t.Fatalf("result=%+v err=%v lookups=%d", result, err, journal.lookups)
	}
}

func TestBrokerOperationObservationRejectsInconsistentRecords(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.BrokerJournalRecord)
	}{
		{"record id", func(r *domain.BrokerJournalRecord) { r.OperationID = strings.Repeat("e", 64) }},
		{"ticket owner", func(r *domain.BrokerJournalRecord) { r.Intent.Ticket.PrincipalSHA256 = strings.Repeat("e", 64) }},
		{"writer", func(r *domain.BrokerJournalRecord) { r.Intent.Ticket.ExecutionSHA256 = strings.Repeat("e", 64) }},
		{"binding", func(r *domain.BrokerJournalRecord) { r.BindingSHA256 = strings.Repeat("f", 64) }},
		{"changed intent", func(r *domain.BrokerJournalRecord) { r.Intent.TargetSHA256 = strings.Repeat("e", 64) }},
		{"phase dispatch", func(r *domain.BrokerJournalRecord) { r.DispatchClaimed = false }},
		{"zero sequence", func(r *domain.BrokerJournalRecord) { r.Sequence = 0 }},
		{"result", func(r *domain.BrokerJournalRecord) { r.ResultSHA256 = "invalid" }},
		{"observation deadline", func(r *domain.BrokerJournalRecord) {
			r.Reservation.ObservationUntilMillis = r.Reservation.OperationDeadlineMillis - 1
		}},
		{"retired unsupported", func(r *domain.BrokerJournalRecord) {
			r.Phase = domain.BrokerOperationRetiredNonReplayable
			r.ResultSHA256 = ""
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.UnixMilli(brokerObservationTestMillis)
			record := brokerObservationRecord(t, domain.BrokerOperationApplied)
			test.mutate(&record)
			journal := &brokerObservationJournal{record: record}
			authorizer := &brokerObservationAuthorizer{nowMillis: brokerObservationTestMillis}
			result, err := brokerObservationService(t, authorizer, journal, &now).Observe(t.Context(), brokerObservationRequest(), brokerObservationContext())
			if err == nil || !errors.Is(err, domain.ErrCheckFailed) || result != (BrokerOperationObservationResult{}) || journal.lookups != 1 {
				t.Fatalf("result=%+v err=%v lookups=%d", result, err, journal.lookups)
			}
		})
	}
}

func TestBrokerOperationObservationDoesNotEnableRegistry(t *testing.T) {
	definition, _ := brokercontract.Definition(domain.BrokerOperationOutcomeLookup, brokercontract.OperationVersion)
	if definition.Available {
		t.Fatal("internal observation service enabled public registry operation")
	}
	for _, available := range brokercontract.AvailableDefinitions() {
		if available.ID == domain.BrokerOperationOutcomeLookup {
			t.Fatal("outcome operation entered available registry")
		}
	}
}
