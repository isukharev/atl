package brokerclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type refusingCommentSessionLoader struct{ calls int }

func (loader *refusingCommentSessionLoader) Load() (Session, error) {
	loader.calls++
	return Session{}, domain.ErrConfig
}

func TestGuardedCommentPublicMethodsRequireSessionAfterValidation(t *testing.T) {
	loader := &refusingCommentSessionLoader{}
	client, err := New(Config{BaseURL: "https://127.0.0.1:1", BrokerID: "broker-1", Audience: "atl-broker", Session: loader})
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error {
			_, err := client.PreviewJiraComment(t.Context(), "EXAMPLE-1", []byte("body"))
			return err
		},
		func() error {
			_, err := client.ApplyJiraComment(t.Context(), "EXAMPLE-1", []byte("body"), strings.Repeat("a", 64), "ticket-1")
			return err
		},
		func() error { _, err := client.ObserveBrokerOperation(t.Context(), "ticket-1"); return err },
	} {
		err := call()
		if !errors.Is(err, domain.ErrConfig) {
			t.Fatalf("missing session error=%v", err)
		}
	}
	if loader.calls != 3 {
		t.Fatalf("session loads=%d, want one per valid invocation", loader.calls)
	}
	if _, err := client.PreviewJiraComment(t.Context(), "not a key", []byte("body")); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("invalid preview err=%v", err)
	}
	if loader.calls != 3 {
		t.Fatal("invalid input loaded a session")
	}
}

func TestGuardedCommentDiscoveryRequiresExactAllowedOperation(t *testing.T) {
	definition, ok := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	if !ok {
		t.Fatal("missing comment apply definition")
	}
	row := domain.BrokerDiscoveryOperationV2{
		ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed,
		Features: append([]string(nil), definition.RequiredFeatures...), Limits: definition.Limits,
		Effects: append([]domain.BrokerEffectDefinition(nil), definition.Effects...),
	}
	projection := domain.BrokerDiscoveryProjectionV2{Operations: []domain.BrokerDiscoveryOperationV2{row}}
	if err := requireAllowedV1Operation(projection, definition); err != nil {
		t.Fatalf("exact row err=%v", err)
	}
	mutations := []struct {
		name   string
		change func(*domain.BrokerDiscoveryOperationV2)
		want   error
	}{
		{"version", func(v *domain.BrokerDiscoveryOperationV2) { v.Version++ }, domain.ErrCheckFailed},
		{"supported", func(v *domain.BrokerDiscoveryOperationV2) { v.Supported = false }, domain.ErrCheckFailed},
		{"features", func(v *domain.BrokerDiscoveryOperationV2) { v.Features = nil }, domain.ErrCheckFailed},
		{"limits", func(v *domain.BrokerDiscoveryOperationV2) { v.Limits.MaxTotalUpstreamRequests++ }, domain.ErrCheckFailed},
		{"effects", func(v *domain.BrokerDiscoveryOperationV2) { v.Effects = nil }, domain.ErrCheckFailed},
		{"request required", func(v *domain.BrokerDiscoveryOperationV2) {
			v.Access = domain.BrokerDiscoveryAccessRequestRequired
			v.RequestAccessCorrelation = "request-1"
		}, domain.ErrForbidden},
		{"unavailable", func(v *domain.BrokerDiscoveryOperationV2) { v.Access = domain.BrokerDiscoveryAccessUnavailable }, domain.ErrUsage},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			changed := row
			changed.Features = append([]string(nil), row.Features...)
			changed.Effects = append([]domain.BrokerEffectDefinition(nil), row.Effects...)
			test.change(&changed)
			if err := requireAllowedV1Operation(domain.BrokerDiscoveryProjectionV2{Operations: []domain.BrokerDiscoveryOperationV2{changed}}, definition); !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
	if err := requireAllowedV1Operation(domain.BrokerDiscoveryProjectionV2{}, definition); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("missing row err=%v", err)
	}
}

func TestGuardedCommentRequestsKeepPreviewAndApplyBindingsDistinct(t *testing.T) {
	session := testSession()
	previewDefinition, _ := brokercontract.Definition(domain.BrokerOperationJiraCommentPreview, 1)
	preview := brokerV1Request(
		domain.BrokerOperationJiraCommentPreview, previewDefinition, "preview-1", session,
		domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
			IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always",
		}},
	)
	previewWire, err := brokercontract.EncodeRequestV1(preview)
	if err != nil {
		t.Fatal(err)
	}
	decodedPreview, err := brokercontract.DecodeRequestV1(previewWire)
	if err != nil || decodedPreview.Arguments.JiraComment.ExpectedProposalHash != "" || decodedPreview.Arguments.JiraComment.OperationTicket != "" {
		t.Fatalf("preview=%+v err=%v", decodedPreview.Arguments.JiraComment, err)
	}

	applyDefinition, _ := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	apply := brokerV1Request(
		domain.BrokerOperationJiraCommentApply, applyDefinition, "apply-1", session,
		domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
			IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always",
			ExpectedProposalHash: strings.Repeat("a", 64), OperationTicket: "ticket-1",
		}},
	)
	applyWire, err := brokercontract.EncodeRequestV1(apply)
	if err != nil {
		t.Fatal(err)
	}
	decodedApply, err := brokercontract.DecodeRequestV1(applyWire)
	if err != nil || decodedApply.Arguments.JiraComment.ExpectedProposalHash != strings.Repeat("a", 64) || decodedApply.Arguments.JiraComment.OperationTicket != "ticket-1" ||
		string(decodedApply.Arguments.JiraComment.NativeBody) != "body" || decodedApply.Expect != session.expectations() {
		t.Fatalf("apply=%+v expect=%+v err=%v", decodedApply.Arguments.JiraComment, decodedApply.Expect, err)
	}
}

func TestHeldV1FailureDecoderAllowsOnlyPreauthenticationCorrelationOmission(t *testing.T) {
	failure, err := brokertransport.NewFailure(domain.BrokerReasonAuthorizationUnavailable)
	if err != nil {
		t.Fatal(err)
	}
	failureBody, err := brokertransport.EncodeFailureV1(failure)
	if err != nil {
		t.Fatal(err)
	}
	wrongVersion := []byte(strings.Replace(string(failureBody), `"schema_version":1`, `"schema_version":2`, 1))

	responses := []struct {
		name        string
		status      int
		body        []byte
		correlation string
		wantBody    bool
		wantReason  domain.BrokerReason
	}{
		{name: "correlated success", status: http.StatusOK, body: []byte(`{"result":"bounded"}`), correlation: "correlation-1", wantBody: true},
		{name: "missing success correlation", status: http.StatusOK, body: []byte(`{"result":"bounded"}`)},
		{name: "malformed success correlation", status: http.StatusOK, body: []byte(`{"result":"bounded"}`), correlation: "bad correlation"},
		{name: "uncorrelated host failure", status: http.StatusServiceUnavailable, body: failureBody, wantReason: domain.BrokerReasonAuthorizationUnavailable},
		{name: "correlated failure", status: http.StatusServiceUnavailable, body: failureBody, correlation: "correlation-1", wantReason: domain.BrokerReasonAuthorizationUnavailable},
		{name: "malformed nonempty correlation", status: http.StatusServiceUnavailable, body: failureBody, correlation: "bad correlation"},
		{name: "malformed failure", status: http.StatusServiceUnavailable, body: []byte(`{"private":"shape"}`)},
		{name: "version mismatch", status: http.StatusServiceUnavailable, body: wrongVersion},
	}
	for _, response := range responses {
		t.Run(response.name, func(t *testing.T) {
			body, err := acceptedHeldV1Body(httpx.BoundedResponse{
				Status: response.status, Body: response.body, CorrelationID: response.correlation,
			})
			if response.wantBody {
				if err != nil || string(body) != string(response.body) {
					t.Fatalf("body=%s err=%v", body, err)
				}
				return
			}
			if len(body) != 0 {
				t.Fatalf("body=%q", body)
			}
			if response.wantReason != "" {
				if reason, _ := brokercontract.Reason(err); reason != response.wantReason {
					t.Fatalf("reason=%s err=%v", reason, err)
				}
				return
			}
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestHeldV1ExecutePreservesUncorrelatedHostFailure(t *testing.T) {
	for _, operation := range []domain.BrokerOperationID{
		domain.BrokerOperationJiraCommentPreview,
		domain.BrokerOperationJiraCommentApply,
		domain.BrokerOperationOutcomeLookup,
	} {
		t.Run(string(operation), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				if request.Method != http.MethodPost || request.URL.Path != brokertransport.ExecutePath {
					t.Errorf("request=%s %s", request.Method, request.URL.Path)
				}
				failure, _ := brokertransport.NewFailure(domain.BrokerReasonAuthorizationUnavailable)
				body, _ := brokertransport.EncodeFailureV1(failure)
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusServiceUnavailable)
				_, _ = writer.Write(body)
			}))
			t.Cleanup(server.Close)

			loader := &sequencedSessionLoader{values: []Session{testSession()}}
			client := newTestClient(t, server, loader, "broker-1")
			definition, _ := brokercontract.Definition(operation, brokercontract.OperationVersion)
			invocation, err := client.beginHeldV1(t.Context(), definition, operation != domain.BrokerOperationJiraCommentApply)
			if err != nil {
				t.Fatal(err)
			}
			defer invocation.close()
			discoveryRequest := syntheticHeldDiscoveryRequest(invocation.selected)
			invocation.discoveryRequest = discoveryRequest
			invocation.projection = clientDiscoveryProjection(discoveryRequest)

			arguments := domain.BrokerOperationArguments{Outcome: &domain.BrokerOutcomeArguments{OperationTicket: "ticket-1"}}
			if operation != domain.BrokerOperationOutcomeLookup {
				arguments = domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
					IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always",
				}}
				if operation == domain.BrokerOperationJiraCommentApply {
					arguments.JiraComment.ExpectedProposalHash = strings.Repeat("a", 64)
					arguments.JiraComment.OperationTicket = "ticket-1"
				}
			}
			request := brokerV1Request(operation, definition, "request-1", invocation.selected, arguments)
			body, dispatched, err := invocation.execute(request)
			if len(body) != 0 || !dispatched || calls.Load() != 1 || loader.loads != 3 {
				t.Fatalf("body=%q dispatched=%t calls=%d loads=%d err=%v", body, dispatched, calls.Load(), loader.loads, err)
			}
			if reason, _ := brokercontract.Reason(err); reason != domain.BrokerReasonAuthorizationUnavailable {
				t.Fatalf("reason=%s err=%v", reason, err)
			}
			if operation == domain.BrokerOperationJiraCommentApply {
				ambiguous := ambiguousBrokerCommentApply(err)
				var marker interface{ DiagnosticAmbiguousWrite() bool }
				if !errors.As(ambiguous, &marker) || !marker.DiagnosticAmbiguousWrite() || calls.Load() != 1 {
					t.Fatalf("ambiguous=%v calls=%d", ambiguous, calls.Load())
				}
			}
		})
	}
}

func TestHeldCommentIntentAndPreExecuteCredentialReplacement(t *testing.T) {
	definition, _ := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	budget, err := domain.NewReadBudget(3, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if domain.ReadIntent(heldV1RequestContext(t.Context(), budget, false)) || !domain.ReadIntent(heldV1RequestContext(t.Context(), budget, true)) {
		t.Fatal("apply must omit read intent while preview and outcome retain it")
	}

	replacement := testSession()
	replacement.Credential = []byte("replacement-workload-credential")
	loader := &sequencedSessionLoader{values: []Session{testSession(), replacement}}
	client, err := New(Config{BaseURL: "https://127.0.0.1:1", BrokerID: "broker-1", Audience: "atl-broker", Session: loader})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := client.beginHeldV1(t.Context(), definition, false)
	if err != nil {
		t.Fatal(err)
	}
	defer invocation.close()
	request := syntheticHeldDiscoveryRequest(invocation.selected)
	invocation.discoveryRequest = request
	invocation.projection = clientDiscoveryProjection(request)
	arguments := domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
		IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always",
		ExpectedProposalHash: strings.Repeat("a", 64), OperationTicket: "ticket-1",
	}}
	operation := brokerV1Request(domain.BrokerOperationJiraCommentApply, definition, "request-1", invocation.selected, arguments)
	body, dispatched, err := invocation.execute(operation)
	if len(body) != 0 || dispatched || loader.loads != 2 {
		t.Fatalf("body=%q dispatched=%t loads=%d err=%v", body, dispatched, loader.loads, err)
	}
	if reason, _ := brokercontract.Reason(err); reason != domain.BrokerReasonStaleExecution {
		t.Fatalf("reason=%s err=%v", reason, err)
	}
}

type sequencedSessionLoader struct {
	mu      sync.Mutex
	values  []Session
	loads   int
	loadErr error
}

func (loader *sequencedSessionLoader) Load() (Session, error) {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	loader.loads++
	if loader.loadErr != nil {
		return Session{}, loader.loadErr
	}
	index := min(loader.loads-1, len(loader.values)-1)
	value := loader.values[index]
	value.Credential = append([]byte(nil), value.Credential...)
	return value, nil
}

func TestHeldCommentExecuteReloadsSessionAroundOneNoRedirectAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path != brokertransport.ExecutePath || request.Method != http.MethodPost {
			t.Errorf("request=%s %s", request.Method, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-ATL-Correlation-ID", "correlation-1")
		body, _ := io.ReadAll(request.Body)
		operation, err := brokercontract.DecodeRequestV1(body)
		if err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		digest, _ := brokercontract.ArgumentsSHA256(operation)
		native, _ := brokercontract.NativeCandidateSHA256(operation.Operation, operation.Arguments.JiraComment.NativeBody)
		encoded, _ := brokercontract.EncodeJiraCommentResultV1(domain.BrokerJiraCommentResult{
			SchemaVersion: 1, ArgumentsSHA256: digest, OperationTicket: "ticket-1", Mode: "apply", Status: "applied",
			ProposalHash: operation.Arguments.JiraComment.ExpectedProposalHash, NativeCandidateSHA256: native,
			VersionEvidenceSHA256: strings.Repeat("b", 64), CommentID: "20", WriteAttempted: true, Complete: true, Reconciled: true,
		})
		_, _ = writer.Write(encoded)
	}))
	t.Cleanup(server.Close)
	loader := &sequencedSessionLoader{values: []Session{testSession()}}
	client := newTestClient(t, server, loader, "broker-1")
	definition, _ := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	invocation, err := client.beginHeldV1(t.Context(), definition, false)
	if err != nil {
		t.Fatal(err)
	}
	defer invocation.close()
	request := syntheticHeldDiscoveryRequest(invocation.selected)
	invocation.discoveryRequest = request
	invocation.projection = clientDiscoveryProjection(request)
	arguments := domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
		IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always",
		ExpectedProposalHash: strings.Repeat("a", 64), OperationTicket: "ticket-1",
	}}
	operation := brokerV1Request(domain.BrokerOperationJiraCommentApply, definition, "request-1", invocation.selected, arguments)
	body, dispatched, err := invocation.execute(operation)
	if err != nil || !dispatched || calls.Load() != 1 || loader.loads != 3 {
		t.Fatalf("dispatched=%t calls=%d loads=%d err=%v", dispatched, calls.Load(), loader.loads, err)
	}
	result, err := brokercontract.DecodeJiraCommentResultV1(body)
	if err != nil || result.Status != "applied" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestHeldCommentExecuteSuppressesBufferedResultAfterCredentialReplacement(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-ATL-Correlation-ID", "correlation-1")
		_, _ = writer.Write([]byte(`{"synthetic":"buffered"}`))
	}))
	t.Cleanup(server.Close)
	replacement := testSession()
	replacement.Credential = []byte("replacement-workload-credential")
	loader := &sequencedSessionLoader{values: []Session{testSession(), testSession(), replacement}}
	client := newTestClient(t, server, loader, "broker-1")
	definition, _ := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	invocation, err := client.beginHeldV1(t.Context(), definition, false)
	if err != nil {
		t.Fatal(err)
	}
	defer invocation.close()
	request := syntheticHeldDiscoveryRequest(invocation.selected)
	invocation.discoveryRequest = request
	invocation.projection = clientDiscoveryProjection(request)
	arguments := domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
		IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always",
		ExpectedProposalHash: strings.Repeat("a", 64), OperationTicket: "ticket-1",
	}}
	operation := brokerV1Request(domain.BrokerOperationJiraCommentApply, definition, "request-1", invocation.selected, arguments)
	body, dispatched, err := invocation.execute(operation)
	if len(body) != 0 || !dispatched || calls.Load() != 1 {
		t.Fatalf("body=%q dispatched=%t calls=%d err=%v", body, dispatched, calls.Load(), err)
	}
	reason, _ := brokercontract.Reason(err)
	if reason != domain.BrokerReasonStaleExecution {
		t.Fatalf("reason=%s err=%v", reason, err)
	}
	ambiguous := ambiguousBrokerCommentApply(err)
	var marker interface{ DiagnosticAmbiguousWrite() bool }
	if !errors.Is(ambiguous, domain.ErrCheckFailed) || !errors.As(ambiguous, &marker) || !marker.DiagnosticAmbiguousWrite() || !strings.Contains(ambiguous.Error(), "<SAME-TICKET>") {
		t.Fatalf("ambiguous=%v", ambiguous)
	}
}

func TestHeldCommentApplyDeadlineAfterBufferedResponseIsAmbiguous(t *testing.T) {
	base := time.Now()
	var advanced atomic.Bool
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-ATL-Correlation-ID", "correlation-1")
		_, _ = writer.Write([]byte(`{"synthetic":"buffered"}`))
		advanced.Store(true)
	}))
	t.Cleanup(server.Close)
	loader := &sequencedSessionLoader{values: []Session{testSession()}}
	client := newTestClient(t, server, loader, "broker-1")
	client.now = func() time.Time {
		if advanced.Load() {
			return base.Add(61 * time.Second)
		}
		return base
	}
	definition, _ := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	invocation, err := client.beginHeldV1(t.Context(), definition, false)
	if err != nil {
		t.Fatal(err)
	}
	defer invocation.close()
	request := syntheticHeldDiscoveryRequest(invocation.selected)
	request.NotAfterMillis = base.Add(4 * time.Second).UnixMilli()
	invocation.discoveryRequest = request
	invocation.projection = clientDiscoveryProjection(request)
	invocation.projection.IssuedAtMillis = base.UnixMilli()
	arguments := domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
		IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always",
		ExpectedProposalHash: strings.Repeat("a", 64), OperationTicket: "ticket-1",
	}}
	operation := brokerV1Request(domain.BrokerOperationJiraCommentApply, definition, "request-1", invocation.selected, arguments)
	body, dispatched, err := invocation.execute(operation)
	if len(body) != 0 || !dispatched || calls.Load() != 1 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("body=%q dispatched=%t calls=%d err=%v", body, dispatched, calls.Load(), err)
	}
	wrapped := ambiguousBrokerCommentApply(err)
	var marker interface{ DiagnosticAmbiguousWrite() bool }
	if !errors.As(wrapped, &marker) || !marker.DiagnosticAmbiguousWrite() {
		t.Fatalf("wrapped=%v", wrapped)
	}
}

func TestHeldCommentExecuteDoesNotFollowRedirect(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Location", brokertransport.ExecutePath+"/again")
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	loader := &sequencedSessionLoader{values: []Session{testSession()}}
	client := newTestClient(t, server, loader, "broker-1")
	definition, _ := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, 1)
	invocation, err := client.beginHeldV1(t.Context(), definition, false)
	if err != nil {
		t.Fatal(err)
	}
	defer invocation.close()
	request := syntheticHeldDiscoveryRequest(invocation.selected)
	invocation.discoveryRequest = request
	invocation.projection = clientDiscoveryProjection(request)
	arguments := domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{
		IssueKey: "EXAMPLE-1", NativeBody: []byte("body"), SatisfactionPolicy: "append_always",
		ExpectedProposalHash: strings.Repeat("a", 64), OperationTicket: "ticket-1",
	}}
	operation := brokerV1Request(domain.BrokerOperationJiraCommentApply, definition, "request-1", invocation.selected, arguments)
	_, dispatched, err := invocation.execute(operation)
	if !dispatched || err == nil || calls.Load() != 1 {
		t.Fatalf("dispatched=%t err=%v calls=%d", dispatched, err, calls.Load())
	}
}

func syntheticHeldDiscoveryRequest(session Session) domain.BrokerDiscoveryRequestV2 {
	now := time.Now()
	return domain.BrokerDiscoveryRequestV2{
		SchemaVersion: 2, RequestID: "discovery-1", Service: "jira", BrokerID: "broker-1", Audience: "atl-broker",
		ContextSHA256: strings.Repeat("c", 64), NotAfterMillis: now.Add(4 * time.Second).UnixMilli(), Expect: session.expectations(),
	}
}

func TestApplyAmbiguityRetainsClosedCause(t *testing.T) {
	cause := context.DeadlineExceeded
	err := ambiguousBrokerCommentApply(cause)
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, cause) || strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("err=%v", err)
	}
}
