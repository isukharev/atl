package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func previewBrokerJiraComment(t *testing.T, service *BrokerJiraCommentService) BrokerJiraCommentResult {
	t.Helper()
	result, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBrokerJiraCommentWhitespaceBodyRefusesBeforeProtectedIO(t *testing.T) {
	for _, body := range []string{" ", "\t\r\n", "\u00a0\u2003"} {
		t.Run(body, func(t *testing.T) {
			service, _, port, journal, _, events := brokerJiraCommentFixture(t, 1)
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", "")
			request.Arguments.JiraComment.NativeBody = []byte(body)
			if _, err := brokercontract.EncodeRequestV1(request); err != nil {
				t.Fatalf("fixture must reach application body validation: %v", err)
			}
			result, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
			if !errors.Is(err, domain.ErrUsage) || result.Comment.OperationTicket != "" || len(*events) != 0 ||
				port.writeCalls != 0 || journal.record.OperationID != "" || len(journal.artifact) != 0 {
				t.Fatalf("error=%v ticket=%q events=%v writes=%d reserved=%t", err, result.Comment.OperationTicket, *events, port.writeCalls, journal.record.OperationID != "")
			}
		})
	}
}

func TestBrokerJiraCommentInitialBoundariesDenyBeforeDurableState(t *testing.T) {
	for _, test := range []struct {
		name string
		set  func(*BrokerJiraCommentService, *brokerJiraCommentAuthorizer, *brokerJiraCommentPort, *domain.BrokerVerifiedContext)
	}{
		{"backend binding", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentAuthorizer, _ *brokerJiraCommentPort, verified *domain.BrokerVerifiedContext) {
			verified.Backend.WorkloadBackendID = "other"
		}},
		{"observed origin", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentAuthorizer, port *brokerJiraCommentPort, _ *domain.BrokerVerifiedContext) {
			port.origin = strings.Repeat("b", 64)
		}},
		{"qualification", func(_ *BrokerJiraCommentService, authorizer *brokerJiraCommentAuthorizer, _ *brokerJiraCommentPort, _ *domain.BrokerVerifiedContext) {
			authorizer.denyQualification = true
		}},
		{"initial final", func(_ *BrokerJiraCommentService, authorizer *brokerJiraCommentAuthorizer, _ *brokerJiraCommentPort, _ *domain.BrokerVerifiedContext) {
			authorizer.denyFinalCall = 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, authorizer, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
			verified := brokerJiraCommentContext()
			test.set(service, authorizer, port, &verified)
			_, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), verified)
			if err == nil || port.writeCalls != 0 || journal.record.OperationID != "" {
				t.Fatalf("error=%v writes=%d record=%+v", err, port.writeCalls, journal.record)
			}
		})
	}
}

func TestBrokerJiraCommentInitialProposalDenialDoesNotAdmit(t *testing.T) {
	service, authorizer, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
	preview := previewBrokerJiraComment(t, service)
	authorizer.denyProposalCall = 1
	request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
	_, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
	if err == nil || port.writeCalls != 0 || journal.record.Phase != "" || len(journal.artifact) != 0 {
		t.Fatalf("error=%v writes=%d record=%+v artifact=%d", err, port.writeCalls, journal.record, len(journal.artifact))
	}
}

func TestBrokerJiraCommentPrewriteDriftStopsBeforeArtifactAndDispatch(t *testing.T) {
	service, authorizer, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
	preview := previewBrokerJiraComment(t, service)
	authorizer.afterProposal = func(call int) {
		if call == 1 {
			port.issueOverride = &domain.JiraGuardedCommentIssue{ID: "101", Key: "PROJ-1", Project: "PROJ", Updated: "2027-01-15T08:00:02Z", Complete: true}
		}
	}
	request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
	_, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
	if err == nil || port.writeCalls != 0 || journal.record.Phase != "" || len(journal.artifact) != 0 {
		t.Fatalf("error=%v writes=%d record=%+v artifact=%d", err, port.writeCalls, journal.record, len(journal.artifact))
	}
}

func TestBrokerJiraCommentPreviewBindsApplyIntentAndFixedLifetimes(t *testing.T) {
	service, authorizer, _, journal, _, events := brokerJiraCommentFixture(t, 1)
	preview := previewBrokerJiraComment(t, service)
	applyNative, err := brokercontract.NativeCandidateSHA256(domain.BrokerOperationJiraCommentApply, []byte("native *wiki*\n"))
	if err != nil {
		t.Fatal(err)
	}
	if journal.record.Intent.NativeSHA256 != applyNative || journal.record.Intent.NativeSHA256 == preview.Comment.NativeCandidateSHA256 {
		t.Fatalf("intent=%q preview=%q", journal.record.Intent.NativeSHA256, preview.Comment.NativeCandidateSHA256)
	}
	applyRequest := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
	applyArguments, err := brokercontract.ArgumentsSHA256(applyRequest)
	if err != nil || applyArguments != journal.record.Intent.Ticket.ArgumentsSHA256 {
		t.Fatalf("apply arguments=%q ticket=%q error=%v", applyArguments, journal.record.Intent.Ticket.ArgumentsSHA256, err)
	}
	applyRequest.RequestID = "different-transport-request"
	changedRequestArguments, err := brokercontract.ArgumentsSHA256(applyRequest)
	if err != nil || changedRequestArguments != applyArguments {
		t.Fatalf("transport request ID changed arguments: %q/%q error=%v", applyArguments, changedRequestArguments, err)
	}
	reservation := journal.record.Reservation
	if reservation.OperationDeadlineMillis != brokerJiraCommentTestMillis+60_000 || reservation.ObservationUntilMillis != brokerJiraCommentTestMillis+int64(time.Hour/time.Millisecond) ||
		journal.record.AcceptUntilMillis != brokerJiraCommentTestMillis+60_000 || reservation.ArtifactCapacity != brokerJiraCommentRecoveryMaxBytes || !containsOrdered(*events, []string{"reserve", "bind"}) {
		t.Fatalf("record=%+v events=%v", journal.record, *events)
	}

	service, authorizer, _, journal, _, _ = brokerJiraCommentFixture(t, 1)
	journal.afterBind = func() { authorizer.nowMillis += domain.BrokerMaxDecisionLeaseMillis + 1 }
	service.now = func() time.Time { return time.UnixMilli(authorizer.nowMillis) }
	result, err := service.Execute(t.Context(), brokerJiraCommentRequest(domain.BrokerOperationJiraCommentPreview, "", ""), brokerJiraCommentContext())
	if err == nil || result.Comment.OperationTicket != "" || journal.record.BindingSHA256 == "" {
		t.Fatalf("late preview=%+v error=%v record=%+v", result, err, journal.record)
	}
}

func TestBrokerJiraCommentApplyRejectsEveryBoundIdentityMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*BrokerJiraCommentService, *brokerJiraCommentPort, *brokerJiraCommentJournal, *domain.BrokerRequest, *domain.BrokerVerifiedContext)
	}{
		{"ticket", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentPort, _ *brokerJiraCommentJournal, request *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) {
			request.Arguments.JiraComment.OperationTicket = strings.Repeat("9", 64)
		}},
		{"current execution", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentPort, _ *brokerJiraCommentJournal, request *domain.BrokerRequest, verified *domain.BrokerVerifiedContext) {
			verified.ExecutionEpoch = "epoch-2"
			request.Expect.ExecutionEpoch = verified.ExecutionEpoch
		}},
		{"current authority", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentPort, _ *brokerJiraCommentJournal, request *domain.BrokerRequest, verified *domain.BrokerVerifiedContext) {
			verified.AuthorityRevision = "revision-2"
			request.Expect.AuthorityRevision = verified.AuthorityRevision
		}},
		{"workload owner", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentPort, _ *brokerJiraCommentJournal, _ *domain.BrokerRequest, verified *domain.BrokerVerifiedContext) {
			verified.WorkloadID = "workload-2"
		}},
		{"audience owner", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentPort, _ *brokerJiraCommentJournal, _ *domain.BrokerRequest, verified *domain.BrokerVerifiedContext) {
			verified.Audience = "other-audience"
		}},
		{"complete backend", func(service *BrokerJiraCommentService, _ *brokerJiraCommentPort, _ *brokerJiraCommentJournal, _ *domain.BrokerRequest, verified *domain.BrokerVerifiedContext) {
			verified.Backend.WorkloadBackendID = "jira-secondary"
			service.backend = verified.Backend
		}},
		{"ticket version", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentPort, journal *brokerJiraCommentJournal, _ *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) {
			journal.record.Intent.Ticket.OperationVersion++
		}},
		{"target", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentPort, journal *brokerJiraCommentJournal, _ *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) {
			journal.record.Intent.TargetSHA256 = strings.Repeat("8", 64)
		}},
		{"effect", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentPort, journal *brokerJiraCommentJournal, _ *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) {
			journal.record.Intent.EffectSHA256 = strings.Repeat("7", 64)
		}},
		{"evidence", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentPort, journal *brokerJiraCommentJournal, _ *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) {
			journal.record.Intent.EvidenceSHA256 = strings.Repeat("6", 64)
		}},
		{"binding", func(_ *BrokerJiraCommentService, _ *brokerJiraCommentPort, journal *brokerJiraCommentJournal, _ *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) {
			journal.record.BindingSHA256 = strings.Repeat("5", 64)
		}},
		{"issue id", func(_ *BrokerJiraCommentService, port *brokerJiraCommentPort, _ *brokerJiraCommentJournal, _ *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) {
			port.qualifiedIssue = &domain.BrokerJiraIssueIdentity{ID: "102", Key: "PROJ-1", Project: "PROJ", Updated: "2027-01-15T08:00:00Z", Complete: true}
		}},
		{"issue project", func(_ *BrokerJiraCommentService, port *brokerJiraCommentPort, _ *brokerJiraCommentJournal, _ *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) {
			port.qualifiedIssue = &domain.BrokerJiraIssueIdentity{ID: "101", Key: "PROJ-1", Project: "OTHER", Updated: "2027-01-15T08:00:00Z", Complete: true}
		}},
		{"version evidence", func(_ *BrokerJiraCommentService, port *brokerJiraCommentPort, _ *brokerJiraCommentJournal, _ *domain.BrokerRequest, _ *domain.BrokerVerifiedContext) {
			port.qualifiedIssue = &domain.BrokerJiraIssueIdentity{ID: "101", Key: "PROJ-1", Project: "PROJ", Updated: "2027-01-15T08:00:02Z", Complete: true}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
			preview := previewBrokerJiraComment(t, service)
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			verified := brokerJiraCommentContext()
			test.mutate(service, port, journal, &request, &verified)
			_, err := service.Execute(t.Context(), request, verified)
			if err == nil || port.writeCalls != 0 {
				t.Fatalf("error=%v writes=%d record=%+v", err, port.writeCalls, journal.record)
			}
		})
	}
}

func TestBrokerJiraCommentApplyRejectsEveryConsumedPhase(t *testing.T) {
	for _, phase := range []domain.BrokerOperationPhase{
		domain.BrokerOperationAdmitted,
		domain.BrokerOperationDispatching,
		domain.BrokerOperationApplied,
		domain.BrokerOperationNotApplied,
		domain.BrokerOperationOutcomeUnknown,
		domain.BrokerOperationRetiredNonReplayable,
	} {
		t.Run(string(phase), func(t *testing.T) {
			service, _, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
			preview := previewBrokerJiraComment(t, service)
			journal.record.Phase = phase
			journal.record.DispatchClaimed = phase != domain.BrokerOperationAdmitted
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			_, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
			if err == nil || port.writeCalls != 0 {
				t.Fatalf("error=%v writes=%d record=%+v", err, port.writeCalls, journal.record)
			}
		})
	}
}

func TestBrokerJiraCommentReadbackClassifications(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*brokerJiraCommentPort)
		wantStatus string
		wantErr    bool
	}{
		{"lost reply exact recovery", func(port *brokerJiraCommentPort) { port.writeErr = errors.New("lost reply") }, "recovered", false},
		{"lost reply zero candidates", func(port *brokerJiraCommentPort) { port.writeErr = errors.New("lost reply"); port.hideWritten = true }, "outcome_unknown", true},
		{"lost reply multiple candidates", func(port *brokerJiraCommentPort) { port.writeErr = errors.New("lost reply"); port.extraCandidates = 1 }, "outcome_unknown", true},
		{"baseline mutation", func(port *brokerJiraCommentPort) { port.mutateBaseline = true }, "outcome_unknown", true},
		{"revision did not advance", func(port *brokerJiraCommentPort) { port.unknownReadback = true }, "outcome_unknown", true},
		{"project moved", func(port *brokerJiraCommentPort) {
			port.afterWrite = func() {
				port.issueOverride = &domain.JiraGuardedCommentIssue{ID: "101", Key: "OTHER-1", Project: "OTHER", Updated: "2027-01-15T08:00:01Z", Complete: true}
			}
		}, "outcome_unknown", true},
		{"readback outage", func(port *brokerJiraCommentPort) {
			port.afterWrite = func() { port.readIssueErr = domain.ErrCheckFailed }
		}, "outcome_unknown", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
			preview := previewBrokerJiraComment(t, service)
			test.configure(port)
			request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
			result, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
			if (err != nil) != test.wantErr || result.Comment.Status != test.wantStatus || port.writeCalls != 1 {
				t.Fatalf("result=%+v error=%v writes=%d", result, err, port.writeCalls)
			}
			wantPhase := domain.BrokerOperationApplied
			if test.wantErr {
				wantPhase = domain.BrokerOperationOutcomeUnknown
			}
			if journal.record.Phase != wantPhase || test.wantErr && journal.record.ResultSHA256 != "" {
				t.Fatalf("record=%+v", journal.record)
			}
		})
	}
}

func TestBrokerJiraCommentPostClaimExpiryProvesNoDispatch(t *testing.T) {
	service, authorizer, port, journal, _, _ := brokerJiraCommentFixture(t, 1)
	service.now = func() time.Time { return time.UnixMilli(authorizer.nowMillis) }
	preview := previewBrokerJiraComment(t, service)
	journal.afterClaim = func() { authorizer.nowMillis += domain.BrokerMaxDecisionLeaseMillis + 1 }
	request := brokerJiraCommentRequest(domain.BrokerOperationJiraCommentApply, preview.Comment.ProposalHash, preview.Comment.OperationTicket)
	_, err := service.Execute(t.Context(), request, brokerJiraCommentContext())
	if err == nil || port.writeCalls != 0 || journal.record.Phase != domain.BrokerOperationNotApplied || !journal.record.DispatchClaimed || journal.record.ResultSHA256 == "" {
		t.Fatalf("error=%v writes=%d record=%+v", err, port.writeCalls, journal.record)
	}
}
