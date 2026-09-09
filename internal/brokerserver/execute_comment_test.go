package brokerserver

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerCommentAndOutcomeRequireConfiguredServices(t *testing.T) {
	for _, operation := range []domain.BrokerOperationID{domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraCommentApply, domain.BrokerOperationOutcomeLookup} {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newBrokerServerFixture(t, "Synthetic summary", "", nil)
			request, err := brokercontract.DecodeRequestV1(fixture.requestBody)
			if err != nil {
				t.Fatal(err)
			}
			definition, ok := brokercontract.Definition(operation, 1)
			if !ok {
				t.Fatal("missing existing operation contract")
			}
			request.Operation, request.Features = operation, definition.RequiredFeatures
			if operation == domain.BrokerOperationOutcomeLookup {
				request.Arguments = domain.BrokerOperationArguments{Outcome: &domain.BrokerOutcomeArguments{OperationTicket: "ticket-1"}}
			} else {
				// Above the legacy read-envelope cap, but within the existing
				// comment schema: this reaches authentication and dispatch.
				comment := &domain.BrokerJiraCommentArguments{IssueKey: "PROJ-7", NativeBody: bytes.Repeat([]byte("x"), 80<<10), SatisfactionPolicy: "append_always"}
				if operation == domain.BrokerOperationJiraCommentApply {
					comment.ExpectedProposalHash, comment.OperationTicket = strings.Repeat("a", 64), "ticket-1"
				}
				request.Arguments = domain.BrokerOperationArguments{JiraComment: comment}
			}
			body, err := brokercontract.EncodeRequestV1(request)
			if err != nil {
				t.Fatal(err)
			}
			response := brokerRequest(t, fixture.handler, http.MethodPost, ExecutePath, body, true)
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			failure, err := brokertransport.DecodeFailureV1(data)
			if err != nil || failure.Reason != domain.BrokerReasonUnsupported || fixture.authenticator.calls != 1 || fixture.authorizer.admissionCalls != 0 || fixture.backendCalls.Load() != 0 {
				t.Fatalf("missing-service boundary: failure=%+v err=%v auth=%d admission=%d backend=%d", failure, err, fixture.authenticator.calls, fixture.authorizer.admissionCalls, fixture.backendCalls.Load())
			}
		})
	}
}

func TestBrokerV1ReadKeepsItsOwnEnvelopeCap(t *testing.T) {
	fixture := newBrokerServerFixture(t, "Synthetic summary", "", nil)
	body := append(bytes.Clone(fixture.requestBody), bytes.Repeat([]byte(" "), 64<<10)...)
	// Valid JSON and a valid read request: only the selected operation cap
	// should refuse it, before authentication or backend I/O.
	if _, err := brokercontract.DecodeRequestV1(bytes.TrimSpace(body)); err != nil {
		t.Fatal(err)
	}
	response := brokerRequest(t, fixture.handler, http.MethodPost, ExecutePath, body, true)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || fixture.authenticator.calls != 0 || fixture.backendCalls.Load() != 0 {
		t.Fatalf("oversize read status=%d auth=%d backend=%d", response.StatusCode, fixture.authenticator.calls, fixture.backendCalls.Load())
	}
}
