package brokerauthority

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func TestAuthorityProposalUsesExactRequestAndClearance(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	request := authorityProposalRequest(t, now, []byte("native *candidate*"))
	var calls atomic.Int32
	var received domain.BrokerProposalAuthorizationRequest
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, httpRequest *http.Request) {
		calls.Add(1)
		if httpRequest.Method != http.MethodPost || httpRequest.URL.Path != proposalPath || httpRequest.URL.RawQuery != "" {
			t.Errorf("request=%s %s", httpRequest.Method, httpRequest.URL.RequestURI())
		}
		if httpRequest.Header.Get("Authorization") != "Bearer synthetic-server-credential" || httpRequest.Header.Get("Content-Type") != "application/json" || httpRequest.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected headers=%v", httpRequest.Header)
		}
		body, err := io.ReadAll(httpRequest.Body)
		if err != nil || int64(len(body)) > brokercontract.MaxEnvelopeBytes {
			t.Errorf("request bytes=%d err=%v", len(body), err)
			return
		}
		received, err = brokercontract.DecodeProposalAuthorizationRequestV1(body)
		if err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		encoded, err := brokercontract.EncodeProposalClearanceV1(authorityProposalClearance(t, received, now.UnixMilli()))
		if err != nil {
			t.Errorf("encode clearance: %v", err)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(encoded)
	}))
	defer server.Close()

	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	clearance, err := authority.AuthorizeProposal(t.Context(), request)
	if err != nil || brokercontract.ValidateProposalClearanceForV1(clearance, request, now.UnixMilli()) != nil {
		t.Fatalf("clearance=%+v err=%v", clearance, err)
	}
	if calls.Load() != 1 || !reflect.DeepEqual(received, request) {
		t.Fatalf("calls=%d\nreceived=%+v\nwant=%+v", calls.Load(), received, request)
	}
	if received.ProposalHash != request.ProposalHash || received.NativeCandidateSHA256 != request.NativeCandidateSHA256 || received.VersionEvidenceSHA256 != request.VersionEvidenceSHA256 || received.OperationDecision.DecisionSHA256 != request.OperationDecision.DecisionSHA256 || received.OperationRequest.QualificationRequest.Admission.Context != request.OperationRequest.QualificationRequest.Admission.Context {
		t.Fatal("proposal request lost candidate, proposal, operation, version, or context binding")
	}
}

func TestAuthorityProposalRejectsInvalidRequestWithoutIO(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))

	invalid := authorityProposalRequest(t, now, []byte("native candidate"))
	invalid.OperationDecision.Status = domain.BrokerDecisionDenied
	invalid.OperationDecision.Reason = domain.BrokerReasonDenied
	if _, err := authority.AuthorizeProposal(t.Context(), invalid); !errors.Is(err, domain.ErrCheckFailed) || calls.Load() != 0 {
		t.Fatalf("operation-only grant proposal err=%v calls=%d", err, calls.Load())
	}
}

func TestAuthorityProposalStrictlyDecodesClearance(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	request := authorityProposalRequest(t, now, []byte("native candidate"))
	valid, err := brokercontract.EncodeProposalClearanceV1(authorityProposalClearance(t, request, now.UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"malformed": []byte(`{"schema_version":`),
		"duplicate": bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		"unknown":   bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"raw_policy":"private-policy-canary"`), 1),
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = writer.Write(response)
			}))
			defer server.Close()
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			_, err := authority.AuthorizeProposal(t.Context(), request)
			if !errors.Is(err, domain.ErrUsage) || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
			assertAuthorityProposalErrorIsContentFree(t, err, server.URL, "private-policy-canary")
		})
	}
}

func TestAuthorityProposalClearanceRequiresCallerValidation(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	request := authorityProposalRequest(t, now, []byte("native candidate"))
	tests := []struct {
		name   string
		mutate func(*domain.BrokerProposalClearance)
		reason domain.BrokerReason
	}{
		{name: "wrong proposal", mutate: func(value *domain.BrokerProposalClearance) { value.ProposalHash = strings.Repeat("8", 64) }, reason: domain.BrokerReasonMalformed},
		{name: "wrong candidate", mutate: func(value *domain.BrokerProposalClearance) { value.NativeCandidateSHA256 = strings.Repeat("8", 64) }, reason: domain.BrokerReasonMalformed},
		{name: "wrong operation", mutate: func(value *domain.BrokerProposalClearance) { value.OperationDecisionSHA256 = strings.Repeat("8", 64) }, reason: domain.BrokerReasonMalformed},
		{name: "wrong version", mutate: func(value *domain.BrokerProposalClearance) { value.VersionEvidenceSHA256 = strings.Repeat("8", 64) }, reason: domain.BrokerReasonMalformed},
		{name: "wrong context", mutate: func(value *domain.BrokerProposalClearance) { value.ContextSHA256 = strings.Repeat("8", 64) }, reason: domain.BrokerReasonStaleAuthority},
		{name: "stale authority", mutate: func(value *domain.BrokerProposalClearance) { value.AuthorityRevision = "revision-2" }, reason: domain.BrokerReasonStaleAuthority},
		{name: "expired", mutate: func(value *domain.BrokerProposalClearance) {
			value.IssuedAtMillis, value.ExpiresAtMillis = now.Add(-6*time.Second).UnixMilli(), now.Add(-time.Second).UnixMilli()
		}, reason: domain.BrokerReasonDecisionExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := authorityProposalClearance(t, request, now.UnixMilli())
			test.mutate(&value)
			value.ClearanceSHA256 = ""
			wire, err := brokercontract.EncodeProposalClearanceV1(value)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(wire) }))
			defer server.Close()
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			clearance, err := authority.AuthorizeProposal(t.Context(), request)
			if err != nil {
				t.Fatalf("transport/decode err=%v", err)
			}
			validationErr := brokercontract.ValidateProposalClearanceForV1(clearance, request, now.UnixMilli())
			if reason, ok := brokercontract.Reason(validationErr); !ok || reason != test.reason {
				t.Fatalf("validation err=%v reason=%q/%t want=%q", validationErr, reason, ok, test.reason)
			}
		})
	}
}

func TestAuthorityProposalBoundsDeadlineResponseAndAttempts(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	request := authorityProposalRequest(t, now, []byte("native candidate"))

	t.Run("caller cancellation after request arrival", func(t *testing.T) {
		var calls atomic.Int32
		received := make(chan struct{})
		release := make(chan struct{})
		server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			calls.Add(1)
			close(received)
			<-release
		}))
		defer server.Close()
		defer close(release)
		authority := newTestAuthority(t, server, strings.Repeat("a", 64))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := authority.AuthorizeProposal(ctx, request)
			result <- err
		}()
		select {
		case <-received:
			cancel()
		case <-time.After(2 * time.Second):
			t.Fatal("proposal did not reach authority")
		}
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
		case <-time.After(2 * time.Second):
			t.Fatal("proposal did not observe caller cancellation")
		}
	})

	t.Run("five second authority deadline", func(t *testing.T) {
		scheduler, err := httpx.NewScheduler(1, 0)
		if err != nil {
			t.Fatal(err)
		}
		held := make(chan struct{})
		release := make(chan struct{})
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, httpRequest *http.Request) {
			if httpRequest.URL.Path != "/hold" {
				t.Errorf("unexpected request reached authority: %s", httpRequest.URL.Path)
				return
			}
			close(held)
			<-release
			_, _ = writer.Write([]byte(`{}`))
		}))
		defer server.Close()
		authority := newTestAuthorityWithScheduler(t, server, strings.Repeat("a", 64), scheduler)
		budget, err := domain.NewReadBudget(1, 1024)
		if err != nil {
			t.Fatal(err)
		}
		blockerContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(t.Context(), budget))))
		blockerResult := make(chan error, 1)
		go func() {
			_, err := authority.client.DoBoundedResponse(blockerContext, http.MethodPost, "/hold", []byte(`{}`), nil, 1024, 1024)
			blockerResult <- err
		}()
		select {
		case <-held:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("scheduler blocker did not reach authority")
		}
		parent, cancel := context.WithTimeout(t.Context(), 9*time.Second)
		defer cancel()
		started := time.Now()
		_, proposalErr := authority.AuthorizeProposal(parent, request)
		elapsed := time.Since(started)
		close(release)
		if !errors.Is(proposalErr, context.DeadlineExceeded) || elapsed < 4*time.Second || elapsed >= 7*time.Second {
			t.Fatalf("err=%v elapsed=%s", proposalErr, elapsed)
		}
		select {
		case err := <-blockerResult:
			if err != nil {
				t.Fatalf("scheduler blocker: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler blocker did not finish")
		}
	})

	validClearance, err := brokercontract.EncodeProposalClearanceV1(authorityProposalClearance(t, request, now.UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	oversizedValidJSON := append(append([]byte{}, validClearance...), bytes.Repeat([]byte{' '}, int(maxAuthorityDecisionBytes)-len(validClearance)+1)...)
	for _, test := range []struct {
		name     string
		status   int
		response []byte
	}{
		{name: "success body budget", status: http.StatusOK, response: oversizedValidJSON},
		{name: "failure body budget", status: http.StatusInternalServerError, response: bytes.Repeat([]byte{'x'}, int(maxAuthorityDecisionBytes)+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				calls.Add(1)
				defer incoming.Body.Close()
				if _, err := io.Copy(io.Discard, incoming.Body); err != nil {
					t.Errorf("read submitted proposal request: %v", err)
					return
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write(test.response)
			}))
			defer server.Close()
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			_, err := authority.AuthorizeProposal(t.Context(), request)
			if !errors.Is(err, domain.ErrReadResponseBudgetExhausted) || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
			assertAuthorityProposalErrorIsContentFree(t, err, server.URL, strings.Repeat("x", 32), string(validClearance))
		})
	}

	t.Run("redirect", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			writer.Header().Set("Location", proposalPath+"?private=redirect-canary")
			writer.WriteHeader(http.StatusTemporaryRedirect)
			_, _ = io.WriteString(writer, "private-response-canary")
		}))
		defer server.Close()
		authority := newTestAuthority(t, server, strings.Repeat("a", 64))
		_, err := authority.AuthorizeProposal(t.Context(), request)
		if err == nil || calls.Load() != 1 {
			t.Fatalf("err=%v calls=%d", err, calls.Load())
		}
		var apiError *httpx.APIError
		if errors.As(err, &apiError) {
			t.Fatal("raw authority response remained reachable")
		}
		assertAuthorityProposalErrorIsContentFree(t, err, server.URL, "redirect-canary", "response-canary", proposalPath)
	})
}

func authorityProposalRequest(t *testing.T, now time.Time, nativeBody []byte) domain.BrokerProposalAuthorizationRequest {
	t.Helper()
	proposalHash := strings.Repeat("3", 64)
	definition, ok := brokercontract.Definition(domain.BrokerOperationJiraCommentApply, brokercontract.OperationVersion)
	if !ok {
		t.Fatal("missing Jira comment apply definition")
	}
	request := domain.BrokerRequest{
		SchemaVersion: 1, Operation: definition.ID, OperationVersion: definition.Version, RequestID: "request-apply",
		Features:  append([]string{}, definition.RequiredFeatures...),
		Expect:    domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"},
		Arguments: domain.BrokerOperationArguments{JiraComment: &domain.BrokerJiraCommentArguments{IssueKey: "PROJ-7", NativeBody: append([]byte{}, nativeBody...), SatisfactionPolicy: "append_always", ExpectedProposalHash: proposalHash, OperationTicket: "ticket-1"}},
	}
	argumentsSHA256, err := brokercontract.ArgumentsSHA256(request)
	if err != nil {
		t.Fatal(err)
	}
	admission := domain.BrokerAdmissionRequest{Context: authorityVerifiedContext(now), Operation: request.Operation, OperationVersion: request.OperationVersion, RequestID: request.RequestID, Features: append([]string{}, request.Features...), Arguments: request.Arguments, ArgumentsSHA256: argumentsSHA256, DeadlineMillis: now.Add(time.Minute).UnixMilli()}
	admissionRequestSHA256, err := brokercontract.AdmissionRequestSHA256(admission)
	if err != nil {
		t.Fatal(err)
	}
	contextSHA256, err := brokercontract.VerifiedContextSHA256(admission.Context)
	if err != nil {
		t.Fatal(err)
	}
	admissionDecision := proposalAdmissionDecision(t, admission, authorityCore(domain.BrokerPhaseAdmission, contextSHA256, admissionRequestSHA256, admission.Context.AuthorityRevision, now.UnixMilli()))
	qualification := domain.BrokerQualificationRequest{Admission: admission, AdmissionDecision: admissionDecision, Plan: domain.BrokerQualificationPlan{SelectorSHA256: argumentsSHA256, MetadataFields: append([]string{}, definition.QualificationFields...), Limits: definition.Limits.Qualification}}
	qualificationRequestSHA256, err := brokercontract.QualificationRequestSHA256(qualification)
	if err != nil {
		t.Fatal(err)
	}
	planSHA256, err := brokercontract.QualificationPlanSHA256(qualification.Plan)
	if err != nil {
		t.Fatal(err)
	}
	qualificationValue := domain.BrokerQualificationDecision{BrokerDecisionCore: authorityCore(domain.BrokerPhaseQualificationAuthorization, contextSHA256, qualificationRequestSHA256, admission.Context.AuthorityRevision, now.UnixMilli()), AdmissionRequestSHA256: admissionRequestSHA256, AdmissionDecisionSHA256: admissionDecision.DecisionSHA256, PlanSHA256: planSHA256}
	qualificationDecision := proposalQualificationDecision(t, qualificationValue)
	resource, err := brokercontract.BrokerJiraCommentResourceV1(domain.BrokerJiraIssueIdentity{ID: "10001", Key: "PROJ-7", Project: "PROJ", Updated: "2026-09-09T10:00:00Z", Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	effects, effectsSHA256, err := brokercontract.BrokerJiraCommentEffectsV1(resource, true)
	if err != nil {
		t.Fatal(err)
	}
	operation := domain.BrokerOperationAuthorizationRequest{QualificationRequest: qualification, QualificationDecision: qualificationDecision, QualifiedResources: []domain.BrokerQualifiedResource{resource}, Effects: effects}
	operationRequestSHA256, err := brokercontract.OperationAuthorizationRequestSHA256(operation)
	if err != nil {
		t.Fatal(err)
	}
	resourcesSHA256, err := brokercontract.QualifiedResourcesSHA256(operation.QualifiedResources)
	if err != nil {
		t.Fatal(err)
	}
	operationValue := domain.BrokerOperationDecision{BrokerDecisionCore: authorityCore(domain.BrokerPhaseFinalAuthorization, contextSHA256, operationRequestSHA256, admission.Context.AuthorityRevision, now.UnixMilli()), QualificationDecisionSHA256: qualificationDecision.DecisionSHA256, Operation: admission.Operation, OperationVersion: admission.OperationVersion, ArgumentsSHA256: admission.ArgumentsSHA256, ResourcesSHA256: resourcesSHA256, EffectsSHA256: effectsSHA256}
	operationDecision := proposalOperationDecision(t, operationValue)
	nativeSHA256, err := brokercontract.NativeCandidateSHA256(admission.Operation, nativeBody)
	if err != nil {
		t.Fatal(err)
	}
	return domain.BrokerProposalAuthorizationRequest{OperationRequest: operation, OperationDecision: operationDecision, ProposalSchemaVersion: 1, ProposalHash: proposalHash, NativeCandidateSHA256: nativeSHA256, VersionEvidenceSHA256: resource.VersionEvidence}
}

func authorityProposalClearance(t *testing.T, request domain.BrokerProposalAuthorizationRequest, nowMillis int64) domain.BrokerProposalClearance {
	t.Helper()
	requestSHA256, err := brokercontract.ProposalAuthorizationRequestSHA256(request)
	if err != nil {
		t.Fatal(err)
	}
	contextSHA256, err := brokercontract.VerifiedContextSHA256(request.OperationRequest.QualificationRequest.Admission.Context)
	if err != nil {
		t.Fatal(err)
	}
	return domain.BrokerProposalClearance{BrokerDecisionCore: authorityCore(domain.BrokerPhaseProposalClearance, contextSHA256, requestSHA256, request.OperationRequest.QualificationRequest.Admission.Context.AuthorityRevision, nowMillis), OperationDecisionSHA256: request.OperationDecision.DecisionSHA256, ProposalHash: request.ProposalHash, NativeCandidateSHA256: request.NativeCandidateSHA256, VersionEvidenceSHA256: request.VersionEvidenceSHA256}
}

func proposalAdmissionDecision(t *testing.T, request domain.BrokerAdmissionRequest, core domain.BrokerDecisionCore) domain.BrokerAdmissionDecision {
	t.Helper()
	wire, err := brokercontract.EncodeAdmissionDecisionV1(domain.BrokerAdmissionDecision{BrokerDecisionCore: core})
	if err != nil {
		t.Fatal(err)
	}
	value, err := brokercontract.DecodeAdmissionDecisionV1(wire)
	if err != nil || brokercontract.ValidateAdmissionDecisionForV1(value, request, core.IssuedAtMillis) != nil {
		t.Fatalf("admission decision err=%v", err)
	}
	return value
}

func proposalQualificationDecision(t *testing.T, input domain.BrokerQualificationDecision) domain.BrokerQualificationDecision {
	t.Helper()
	wire, err := brokercontract.EncodeQualificationDecisionV1(input)
	if err != nil {
		t.Fatal(err)
	}
	value, err := brokercontract.DecodeQualificationDecisionV1(wire)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func proposalOperationDecision(t *testing.T, input domain.BrokerOperationDecision) domain.BrokerOperationDecision {
	t.Helper()
	wire, err := brokercontract.EncodeOperationDecisionV1(input)
	if err != nil {
		t.Fatal(err)
	}
	value, err := brokercontract.DecodeOperationDecisionV1(wire)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertAuthorityProposalErrorIsContentFree(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	for _, formatted := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprint(errors.Unwrap(err))} {
		for _, value := range forbidden {
			if strings.Contains(formatted, value) {
				t.Fatalf("unsafe error=%q", formatted)
			}
		}
	}
}
