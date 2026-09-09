package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerJiraCommentLocalDecisionLeasesBoundPreparation(t *testing.T) {
	for _, stage := range []string{"admission", "qualification", "operation"} {
		t.Run(stage, func(t *testing.T) {
			service, authorizer, port, journal, _, events := brokerJiraCommentFixture(t, 1)
			localNow := time.UnixMilli(brokerJiraCommentTestMillis)
			service.now = func() time.Time { return localNow }
			authorizer.nowMillis = brokerJiraCommentTestMillis + domain.BrokerClockAllowanceMillis
			delay := func() { localNow = time.UnixMilli(brokerJiraCommentTestMillis + domain.BrokerMaxDecisionLeaseMillis) }
			switch stage {
			case "admission":
				authorizer.afterAdmission = delay
			case "qualification":
				authorizer.afterQualification = delay
			case "operation":
				authorizer.afterFinal = func(call int) {
					if call == 1 {
						delay()
					}
				}
			}

			result, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
			if !errors.Is(err, context.DeadlineExceeded) || result != (BrokerJiraCommentResult{}) || port.writeCalls != 0 || journal.record.OperationID != "" {
				t.Fatalf("result=%+v error=%v writes=%d record=%+v events=%v", result, err, port.writeCalls, journal.record, *events)
			}
			switch stage {
			case "admission":
				if containsOrdered(*events, []string{"qualification_authorization"}) {
					t.Fatalf("expired admission reached qualification: %v", *events)
				}
			case "qualification":
				if containsOrdered(*events, []string{"qualify_issue"}) {
					t.Fatalf("expired qualification reached Jira: %v", *events)
				}
			case "operation":
				if containsOrdered(*events, []string{"read_actor"}) {
					t.Fatalf("expired operation reached business reads: %v", *events)
				}
			}
		})
	}
}

func TestBrokerJiraCommentFutureAuthorityClockCannotExtendPreviewRelease(t *testing.T) {
	service, authorizer, _, _, _, _ := brokerJiraCommentFixture(t, 1)
	localNow := time.UnixMilli(brokerJiraCommentTestMillis)
	service.now = func() time.Time { return localNow }
	authorizer.nowMillis = brokerJiraCommentTestMillis + domain.BrokerClockAllowanceMillis
	result, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
	wantDeadline := time.UnixMilli(brokerJiraCommentTestMillis + domain.BrokerMaxDecisionLeaseMillis)
	if err != nil || result.Comment.Status != "proposed" || !result.ReleaseDeadline.Equal(wantDeadline) {
		t.Fatalf("result=%+v error=%v want_deadline=%v", result, err, wantDeadline)
	}
}

func TestBrokerJiraCommentLocalProposalLeaseBoundsDurableDispatch(t *testing.T) {
	for _, boundary := range []string{"after admit", "delayed proposal", "after claim"} {
		t.Run(boundary, func(t *testing.T) {
			service, authorizer, port, journal, _, events := brokerJiraCommentFixture(t, 1)
			localNow := time.UnixMilli(brokerJiraCommentTestMillis)
			service.now = func() time.Time { return localNow }
			preview := previewBrokerJiraComment(t, service)
			authorizer.nowMillis = brokerJiraCommentTestMillis + domain.BrokerClockAllowanceMillis
			delay := func() { localNow = time.UnixMilli(brokerJiraCommentTestMillis + domain.BrokerMaxDecisionLeaseMillis) }
			switch boundary {
			case "after admit":
				journal.afterAdmit = delay
			case "delayed proposal":
				authorizer.afterProposal = func(call int) {
					if call == 2 {
						delay()
					}
				}
			case "after claim":
				journal.afterClaim = delay
			}

			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			result, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
			if !errors.Is(err, context.DeadlineExceeded) || result != (BrokerJiraCommentResult{}) || port.writeCalls != 0 || journal.record.Phase != domain.BrokerOperationNotApplied {
				t.Fatalf("result=%+v error=%v writes=%d record=%+v events=%v", result, err, port.writeCalls, journal.record, *events)
			}
			if boundary == "after admit" && authorizer.finalCalls != 2 {
				t.Fatalf("expired proposal lease reached next PDP: final_calls=%d events=%v", authorizer.finalCalls, *events)
			}
			if boundary == "after claim" {
				if !journal.record.DispatchClaimed || journal.record.ResultSHA256 == "" {
					t.Fatalf("claimed no-dispatch proof missing: %+v", journal.record)
				}
			} else if journal.record.DispatchClaimed {
				t.Fatalf("dispatch claimed after expired lease: %+v", journal.record)
			}
		})
	}
}

func TestBrokerJiraCommentCancellationAfterAdmitPersistsNotApplied(t *testing.T) {
	service, _, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
	preview := previewBrokerJiraComment(t, service)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	journal.afterAdmit = cancel
	request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
	result, err := service.Execute(ctx, request, brokerJiraCommentContext())
	if !errors.Is(err, context.Canceled) || result != (BrokerJiraCommentResult{}) || port.writeCalls != 0 || journal.record.Phase != domain.BrokerOperationNotApplied || journal.record.DispatchClaimed {
		t.Fatalf("result=%+v error=%v writes=%d record=%+v", result, err, port.writeCalls, journal.record)
	}
}

func TestBrokerJiraCommentQueuedDispatchCarriesLocalProposalDeadline(t *testing.T) {
	service, authorizer, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
	localNow := time.UnixMilli(brokerJiraCommentTestMillis)
	service.now = func() time.Time { return localNow }
	preview := previewBrokerJiraComment(t, service)
	authorizer.nowMillis = brokerJiraCommentTestMillis + domain.BrokerClockAllowanceMillis
	port.writeErr = brokerJiraCommentNoAttemptError{}
	checked := false
	port.writeContext = func(ctx context.Context) {
		checked = true
		deadline, ok := ctx.Deadline()
		want := brokerJiraCommentTestMillis + domain.BrokerMaxDecisionLeaseMillis
		if !ok || deadline.UnixMilli() != want {
			t.Errorf("dispatch deadline=%v want_millis=%d", deadline, want)
		}
	}
	request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
	result, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
	if err == nil || result != (BrokerJiraCommentResult{}) || !checked || port.writeCalls != 0 || journal.record.Phase != domain.BrokerOperationNotApplied || !journal.record.DispatchClaimed {
		t.Fatalf("result=%+v error=%v checked=%t writes=%d record=%+v", result, err, checked, port.writeCalls, journal.record)
	}
}

func TestBrokerJiraCommentLateWriteUsesOnlyFreshCloseoutLease(t *testing.T) {
	for _, delayedCloseout := range []bool{false, true} {
		name := "fresh closeout succeeds"
		if delayedCloseout {
			name = "delayed closeout becomes unknown"
		}
		t.Run(name, func(t *testing.T) {
			service, authorizer, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
			localNow := time.UnixMilli(brokerJiraCommentTestMillis)
			service.now = func() time.Time { return localNow }
			preview := previewBrokerJiraComment(t, service)
			authorizer.nowMillis = brokerJiraCommentTestMillis + domain.BrokerClockAllowanceMillis
			readsBeforeCloseout := 0
			port.afterWrite = func() {
				readsBeforeCloseout = port.readIssueCalls
				if !delayedCloseout {
					localNow = time.UnixMilli(brokerJiraCommentTestMillis + domain.BrokerMaxDecisionLeaseMillis)
					authorizer.nowMillis = localNow.UnixMilli() + domain.BrokerClockAllowanceMillis
				}
			}
			if delayedCloseout {
				authorizer.afterFinal = func(call int) {
					if call == 4 {
						localNow = time.UnixMilli(brokerJiraCommentTestMillis + domain.BrokerMaxDecisionLeaseMillis)
					}
				}
			}
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			result, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
			if port.writeCalls != 1 {
				t.Fatalf("writes=%d result=%+v error=%v", port.writeCalls, result, err)
			}
			if delayedCloseout {
				if err == nil || result != (BrokerJiraCommentResult{}) || journal.record.Phase != domain.BrokerOperationOutcomeUnknown || port.readIssueCalls != readsBeforeCloseout {
					t.Fatalf("result=%+v error=%v record=%+v reads=%d before=%d", result, err, journal.record, port.readIssueCalls, readsBeforeCloseout)
				}
				return
			}
			wantDeadline := time.UnixMilli(brokerJiraCommentTestMillis + 2*domain.BrokerMaxDecisionLeaseMillis)
			if err != nil || result.Comment.Status != "applied" || journal.record.Phase != domain.BrokerOperationApplied || !result.ReleaseDeadline.Equal(wantDeadline) || port.readIssueCalls <= readsBeforeCloseout {
				t.Fatalf("result=%+v error=%v record=%+v reads=%d before=%d want_deadline=%v", result, err, journal.record, port.readIssueCalls, readsBeforeCloseout, wantDeadline)
			}
		})
	}
}
