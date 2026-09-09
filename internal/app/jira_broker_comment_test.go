package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type brokerCommentClientStub struct {
	previewCalls int
	applyCalls   int
	issueKey     string
	body         []byte
	hash         string
	ticket       string
	result       domain.BrokerJiraCommentResult
	err          error
}

func (stub *brokerCommentClientStub) PreviewJiraComment(_ context.Context, issueKey string, body []byte) (domain.BrokerJiraCommentResult, error) {
	stub.previewCalls++
	stub.issueKey, stub.body = issueKey, append([]byte(nil), body...)
	return stub.result, stub.err
}

func (stub *brokerCommentClientStub) ApplyJiraComment(_ context.Context, issueKey string, body []byte, hash, ticket string) (domain.BrokerJiraCommentResult, error) {
	stub.applyCalls++
	stub.issueKey, stub.body, stub.hash, stub.ticket = issueKey, append([]byte(nil), body...), hash, ticket
	return stub.result, stub.err
}

func brokerCommentResult(mode, status string) domain.BrokerJiraCommentResult {
	return domain.BrokerJiraCommentResult{
		SchemaVersion: 1, ArgumentsSHA256: strings.Repeat("a", 64), OperationTicket: "ticket-1",
		Mode: mode, Status: status, ProposalHash: strings.Repeat("b", 64),
		NativeCandidateSHA256: strings.Repeat("c", 64), VersionEvidenceSHA256: strings.Repeat("d", 64),
		WriteAttempted: mode == "apply", Complete: status != "outcome_unknown",
		Reconciled: status == "applied" || status == "recovered",
	}
}

func TestJiraBrokerCommentMapsClosedPreviewAndApplyDTO(t *testing.T) {
	previewPort := &brokerCommentClientStub{result: brokerCommentResult("preview", "proposed")}
	preview, err := NewJiraService(JiraDependencies{BrokerComments: previewPort}).AddBrokerCommentGuarded(
		t.Context(), "EXAMPLE-1", JiraCommentAddOpts{Body: []byte("native *wiki*")}, "",
	)
	if err != nil || previewPort.previewCalls != 1 || previewPort.applyCalls != 0 || preview.Operation != "jira.comment.preview" ||
		preview.QualificationProfile != domain.BrokerJiraCommentQualificationProfileV1 || preview.OperationTicket != "ticket-1" || preview.Status != "proposed" {
		t.Fatalf("preview=%+v port=%+v err=%v", preview, previewPort, err)
	}
	encoded, err := json.Marshal(preview)
	if err != nil || strings.Contains(string(encoded), "native *wiki*") || strings.Contains(JiraBrokerCommentText(preview), "native *wiki*") {
		t.Fatalf("encoded=%s text=%q err=%v", encoded, JiraBrokerCommentText(preview), err)
	}

	applyResult := brokerCommentResult("apply", "applied")
	applyResult.CommentID = "20"
	applyPort := &brokerCommentClientStub{result: applyResult}
	apply, err := NewJiraService(JiraDependencies{BrokerComments: applyPort}).AddBrokerCommentGuarded(
		t.Context(), "EXAMPLE-1", JiraCommentAddOpts{Body: []byte("native *wiki*"), Apply: true, ExpectedProposalHash: strings.Repeat("b", 64)}, "ticket-1",
	)
	if err != nil || applyPort.applyCalls != 1 || applyPort.hash != strings.Repeat("b", 64) || applyPort.ticket != "ticket-1" ||
		apply.Operation != "jira.comment.apply" || apply.CommentID != "20" || !apply.WriteAttempted || !apply.Complete || !apply.Reconciled {
		t.Fatalf("apply=%+v port=%+v err=%v", apply, applyPort, err)
	}
}

func TestJiraBrokerCommentPublishesTerminalResultsWithNonzeroErrors(t *testing.T) {
	for _, status := range []string{"not_applied", "outcome_unknown"} {
		t.Run(status, func(t *testing.T) {
			port := &brokerCommentClientStub{result: brokerCommentResult("apply", status)}
			result, err := NewJiraService(JiraDependencies{BrokerComments: port}).AddBrokerCommentGuarded(
				t.Context(), "EXAMPLE-1", JiraCommentAddOpts{Body: []byte("body"), Apply: true, ExpectedProposalHash: strings.Repeat("b", 64)}, "ticket-1",
			)
			var terminal interface{ DiagnosticTerminalCheckFailure() bool }
			var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
			if result == nil || result.Status != status || !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &terminal) || !terminal.DiagnosticTerminalCheckFailure() {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			gotAmbiguous := errors.As(err, &ambiguous) && ambiguous.DiagnosticAmbiguousWrite()
			if gotAmbiguous != (status == "outcome_unknown") {
				t.Fatalf("status=%s ambiguous=%t err=%v", status, gotAmbiguous, err)
			}
			if status == "outcome_unknown" && !strings.Contains(err.Error(), "<SAME-TICKET>") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestJiraBrokerCommentValidatesTicketBeforePort(t *testing.T) {
	port := &brokerCommentClientStub{}
	service := NewJiraService(JiraDependencies{BrokerComments: port})
	result, err := service.AddBrokerCommentGuarded(t.Context(), "EXAMPLE-1", JiraCommentAddOpts{
		Body: []byte("body"), Apply: true, ExpectedProposalHash: strings.Repeat("b", 64),
	}, "")
	if result != nil || !errors.Is(err, domain.ErrUsage) || port.applyCalls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, port.applyCalls, err)
	}
}

type brokerOutcomeClientStub struct {
	calls  int
	ticket string
	value  domain.BrokerOperationOutcome
	err    error
}

func (stub *brokerOutcomeClientStub) ObserveBrokerOperation(_ context.Context, ticket string) (domain.BrokerOperationOutcome, error) {
	stub.calls++
	stub.ticket = ticket
	return stub.value, stub.err
}

func TestJiraBrokerOutcomeMapsStableTicketAndDurableTruth(t *testing.T) {
	port := &brokerOutcomeClientStub{value: domain.BrokerOperationOutcome{
		SchemaVersion: 1, TicketSHA256: strings.Repeat("a", 64), Phase: domain.BrokerOperationOutcomeUnknown,
		ObservedAtMillis: 1700000000000,
	}}
	result, err := NewJiraService(JiraDependencies{BrokerOutcomes: port}).ObserveBrokerCommentOperation(t.Context(), "ticket-1")
	if err != nil || port.calls != 1 || port.ticket != "ticket-1" || result.Operation != string(domain.BrokerOperationOutcomeLookup) ||
		result.QualificationProfile != "operation_ticket_v1" || result.OperationTicket != "ticket-1" || result.Phase != domain.BrokerOperationOutcomeUnknown || result.Complete {
		t.Fatalf("result=%+v port=%+v err=%v", result, port, err)
	}
	if strings.Contains(JiraBrokerOperationOutcomeText(result), "credential") {
		t.Fatalf("text=%q", JiraBrokerOperationOutcomeText(result))
	}
}

func TestJiraBrokerOutcomeNeverFallsBackWithoutObserver(t *testing.T) {
	result, err := NewJiraService(JiraDependencies{BrokerComments: &brokerCommentClientStub{}}).ObserveBrokerCommentOperation(t.Context(), "ticket-1")
	if result != nil || !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
