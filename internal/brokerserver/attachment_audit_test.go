package brokerserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func TestAttachmentAuditRequiresExplicitTerminalCompletion(t *testing.T) {
	for _, test := range []struct {
		name, route, outcome string
		status               int
		complete, panicAfter bool
		reason               domain.BrokerReason
	}{
		{name: "unfinished 200", route: "data_execute_v3", status: 200, outcome: "partial"},
		{name: "terminal committed", route: "data_execute_v3", status: 200, complete: true, outcome: "success"},
		{name: "before publication denial", route: "data_execute_v3", status: 403, reason: domain.BrokerReasonDenied, outcome: "rejected"},
		{name: "after publication denial", route: "data_execute_v3", status: 200, reason: domain.BrokerReasonDenied, outcome: "partial"},
		{name: "contradictory completion", route: "data_execute_v3", status: 200, complete: true, reason: domain.BrokerReasonDenied, outcome: "partial"},
		{name: "panic after publication", route: "data_execute_v3", status: 200, panicAfter: true, outcome: "partial"},
		{name: "buffered route unchanged", route: "data_execute_v2", status: 200, outcome: "success"},
	} {
		t.Run(test.name, func(t *testing.T) {
			guard, err := NewCredentialGuard()
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close()
			var output bytes.Buffer
			audit, err := NewAudit(&output, guard)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = audit.Close(ctx)
			}()
			handler := audit.Wrap(test.route, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				operation := domain.BrokerOperationJiraAttachmentDownload
				if test.route == "data_execute_v2" {
					operation = domain.BrokerOperationJiraProjectIssuePageRead
				}
				recordAuditOperation(writer, operation)
				recordAuditReason(writer, test.reason)
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte("SYNTHETIC-STREAM-CONTENT-CANARY"))
				if test.complete {
					recordAuditAttachmentComplete(writer)
				}
				if test.panicAfter {
					panic("SYNTHETIC-PANIC-CONTENT-CANARY")
				}
			}))
			func() {
				defer func() {
					if recovered := recover(); (recovered != nil) != test.panicAfter || recovered != nil && recovered != "Broker handler failed" {
						t.Fatalf("unexpected closed panic category: %v", recovered)
					}
				}()
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v3/execute", nil))
			}()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := audit.Close(ctx); err != nil {
				t.Fatal(err)
			}
			var event AuditEvent
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil || !validAuditEvent(event) {
				t.Fatalf("invalid audit event: %v", err)
			}
			if event.Outcome != test.outcome || event.Route != test.route || !event.Complete || event.ResponseBytes != int64(len("SYNTHETIC-STREAM-CONTENT-CANARY")) {
				t.Fatalf("unexpected audit projection: %+v", event)
			}
			if event.Outcome == "partial" && event.Reason == "" {
				t.Fatal("partial audit omitted its closed reason")
			}
			if bytes.Contains(output.Bytes(), []byte("CANARY")) {
				t.Fatal("audit exposed content")
			}
		})
	}
}

func TestAttachmentAuditPartialVocabularyIsRouteScoped(t *testing.T) {
	event := AuditEvent{SchemaVersion: 1, CorrelationID: "synthetic-correlation", Route: "data_execute_v3", Outcome: "partial", Reason: string(domain.BrokerReasonDenied), Timing: "lt_1s", RequestIndex: 1, Complete: true}
	if !validAuditEvent(event) {
		t.Fatal("attachment partial event rejected")
	}
	event.Route = "data_execute_v2"
	if validAuditEvent(event) {
		t.Fatal("partial was accepted on an unchanged buffered route")
	}
}
