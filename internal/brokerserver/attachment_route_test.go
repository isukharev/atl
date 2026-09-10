package brokerserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func attachmentRouteRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, brokertransport.ExecutePathV3, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	return request
}

func attachmentRouteBody(t *testing.T) []byte {
	t.Helper()
	definition, ok := brokercontract.DefinitionV3(domain.BrokerOperationJiraAttachmentDownload, brokercontract.AttachmentOperationVersionV3)
	if !ok || definition.Definition.Available {
		t.Fatal("this unavailable-route oracle requires the unenabled definition")
	}
	body, err := brokercontract.EncodeAttachmentRequestV3(domain.BrokerAttachmentRequestV3{
		SchemaVersion: 3, Operation: definition.Definition.ID, OperationVersion: definition.Definition.Version,
		RequestID: "request-1", Features: definition.Definition.RequiredFeatures,
		Expect:    domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"},
		Arguments: domain.BrokerAttachmentArgumentsV3{IssueKey: "PROJ-7", AttachmentID: "200"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAttachmentRouteKeepsUnavailableAndInvalidRequestsBeforeUpstreamIO(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
		reason domain.BrokerReason
	}{
		{name: "unavailable", reason: domain.BrokerReasonUnsupported},
		{name: "query expansion", reason: domain.BrokerReasonMalformed, mutate: func(r *http.Request) { r.URL.RawQuery = "debug=1" }},
		{name: "encoded request", reason: domain.BrokerReasonMalformed, mutate: func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
		{name: "missing credential", reason: domain.BrokerReasonCredentialExpired, mutate: func(r *http.Request) { r.Header.Del("Authorization") }},
		{name: "malformed body", reason: domain.BrokerReasonMalformed, mutate: func(r *http.Request) { r.Body = io.NopCloser(bytes.NewReader([]byte(`{}`))) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBrokerServerFixture(t, "unused", "", nil)
			request := attachmentRouteRequest(t, attachmentRouteBody(t))
			if test.mutate != nil {
				test.mutate(request)
			}
			writer := &deadlineResponseWriter{}
			fixture.handler.ServeHTTP(writer, request)
			failure, err := brokertransport.DecodeExecutionFailureV3(writer.body.Bytes())
			if err != nil || failure.Reason != test.reason || fixture.authenticator.calls != 0 || fixture.authorizer.admissionCalls != 0 || fixture.backendCalls.Load() != 0 {
				t.Fatalf("closed result=%s decode=%v auth=%d admission=%d backend=%d", failure.Reason, err, fixture.authenticator.calls, fixture.authorizer.admissionCalls, fixture.backendCalls.Load())
			}
			if len(fixture.handler.permits) != 0 || len(fixture.handler.attachmentPermits) != 0 {
				t.Fatal("request retained an admission permit")
			}
		})
	}
}

func TestAttachmentRouteAcquiresBothPermitsBeforeReadingBody(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		fixture := newBrokerServerFixture(t, "unused", "", nil)
		permit := fixture.handler.permits
		if dedicated {
			permit = fixture.handler.attachmentPermits
		}
		permit <- struct{}{}
		request := attachmentRouteRequest(t, attachmentRouteBody(t))
		body := &boundedRouteCountingBody{body: bytes.NewReader(attachmentRouteBody(t))}
		request.Body = io.NopCloser(body)
		writer := &deadlineResponseWriter{}
		fixture.handler.ServeHTTP(writer, request)
		failure, err := brokertransport.DecodeExecutionFailureV3(writer.body.Bytes())
		if err != nil || failure.Reason != domain.BrokerReasonAuthorizationUnavailable || body.reads.Load() != 0 || fixture.authenticator.calls != 0 || len(permit) != 1 {
			t.Fatalf("overload dedicated=%t reason=%s decode=%v reads=%d auth=%d", dedicated, failure.Reason, err, body.reads.Load(), fixture.authenticator.calls)
		}
		<-permit
		if len(fixture.handler.permits) != 0 || len(fixture.handler.attachmentPermits) != 0 {
			t.Fatal("rejected request leaked a permit")
		}
	}
}

func TestAttachmentDependencyRequiresBudgetComposedAuthentication(t *testing.T) {
	fixture := newBrokerServerFixture(t, "unused", "", nil)
	_, err := New(fixture.handler.config, Dependencies{
		Authenticator: fixture.authenticator, Reads: fixture.handler.reads, Guard: fixture.handler.guard,
		Attachments: &app.BrokerJiraAttachmentStreamService{},
	})
	if !errors.Is(err, domain.ErrUsage) || fixture.authenticator.calls != 0 {
		t.Fatalf("legacy-only attachment authentication: %v", err)
	}
}

func TestAttachmentHostAuditUsesVersionedRoute(t *testing.T) {
	fixture := newBrokerServerFixture(t, "unused", "", nil)
	var output bytes.Buffer
	audit, err := NewAudit(&output, fixture.handler.guard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = audit.Close(ctx)
	}()
	host := &Host{audit: audit}
	host.auditedData(fixture.handler).ServeHTTP(&deadlineResponseWriter{}, attachmentRouteRequest(t, attachmentRouteBody(t)))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := audit.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var event AuditEvent
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil || !validAuditEvent(event) || event.Route != "data_execute_v3" || event.Operation != string(domain.BrokerOperationJiraAttachmentDownload) || event.Outcome == "success" || event.Reason != string(domain.BrokerReasonUnsupported) {
		t.Fatalf("unexpected unavailable attachment audit: %+v err=%v", event, err)
	}
}
