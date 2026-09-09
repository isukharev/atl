package brokerclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

type changingDiscoverySession struct{ calls int }

func (l *changingDiscoverySession) Load() (Session, error) {
	l.calls++
	session := testSession()
	if l.calls > 1 {
		session.ExecutionEpoch = "epoch-other"
	}
	return session, nil
}

func TestDiscoveryClientRejectsHostileResponsesWithoutRetryOrFallback(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*domain.BrokerDiscoveryProjectionV2)
		wire   func([]byte) []byte
		status int
		loader SessionLoader
	}{
		{name: "request mismatch", change: func(p *domain.BrokerDiscoveryProjectionV2) { p.RequestID = "other" }},
		{name: "context mismatch", change: func(p *domain.BrokerDiscoveryProjectionV2) { p.ContextSHA256 = strings.Repeat("b", 64) }},
		{name: "expired", change: func(p *domain.BrokerDiscoveryProjectionV2) {
			p.IssuedAtMillis = time.Now().Add(-2 * time.Second).UnixMilli()
			p.ExpiresAtMillis = time.Now().Add(-time.Second).UnixMilli()
		}},
		{name: "wrong schema", wire: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"schema_version":2`), []byte(`"schema_version":1`), 1)
		}},
		{name: "duplicate", wire: func(body []byte) []byte {
			return bytes.Replace(body, []byte(`"schema_version":2`), []byte(`"schema_version":2,"schema_version":2`), 1)
		}},
		{name: "private unknown member", wire: func(body []byte) []byte { return append([]byte(`{"private":"response-canary",`), body[1:]...) }},
		{name: "oversize", wire: func([]byte) []byte { return bytes.Repeat([]byte("x"), int(brokercontract.MaxDiscoveryV2Bytes)+1) }},
		{name: "redirect", status: http.StatusTemporaryRedirect},
		{name: "raw denial", status: http.StatusForbidden, wire: func([]byte) []byte { return []byte("private-response-canary") }},
		{name: "session replaced", loader: &changingDiscoverySession{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-ATL-Correlation-ID", "correlation-1")
				body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<10))
				if r.URL.Path == brokertransport.DiscoveryNegotiatePathV2 {
					hello, err := brokertransport.DecodeDiscoveryNegotiationV2(body)
					if err != nil {
						t.Error(err)
						return
					}
					request := domain.BrokerDiscoveryRequestV2{SchemaVersion: 2, RequestID: hello.RequestID, Service: hello.Service, BrokerID: hello.BrokerID, Audience: hello.Audience, ContextSHA256: strings.Repeat("a", 64), NotAfterMillis: hello.NotAfterMillis, Expect: domain.BrokerRequestExpectations{ExecutionID: hello.ExecutionID, ExecutionEpoch: hello.ExecutionEpoch, AuthorityRevision: hello.AuthorityRevision}}
					encoded, _ := brokertransport.EncodeNegotiatedDiscoveryV2(request)
					_, _ = w.Write(encoded)
					return
				}
				if r.URL.Path != brokertransport.DiscoveryPathV2 {
					t.Error("unexpected fallback path")
					return
				}
				request, err := brokercontract.DecodeDiscoveryRequestV2(body)
				if err != nil {
					t.Error(err)
					return
				}
				p := clientDiscoveryProjection(request)
				if test.change != nil {
					test.change(&p)
				}
				encoded, err := brokercontract.EncodeDiscoveryProjectionV2(p)
				if err != nil {
					t.Error(err)
					return
				}
				if test.wire != nil {
					encoded = test.wire(encoded)
				}
				if test.status != 0 {
					w.Header().Set("Location", "/private-fallback")
					w.WriteHeader(test.status)
				}
				_, _ = w.Write(encoded)
			}))
			t.Cleanup(server.Close)
			loader := test.loader
			if loader == nil {
				loader = &countingSessionLoader{value: testSession()}
			}
			client := newTestClient(t, server, loader, "broker-1")
			if _, err := client.Discover(t.Context(), "jira"); err == nil || calls.Load() != 2 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			} else if strings.Contains(err.Error(), "canary") {
				t.Fatal("private error escaped")
			}
		})
	}
}

func clientDiscoveryProjection(request domain.BrokerDiscoveryRequestV2) domain.BrokerDiscoveryProjectionV2 {
	digest, _ := brokercontract.DiscoveryRequestSHA256V2(request)
	p := domain.BrokerDiscoveryProjectionV2{SchemaVersion: 2, RequestID: request.RequestID, RequestSHA256: digest, ContextSHA256: request.ContextSHA256, ExecutionID: request.Expect.ExecutionID, ExecutionEpoch: request.Expect.ExecutionEpoch, Audience: request.Audience, BrokerID: request.BrokerID, AuthorityRevision: request.Expect.AuthorityRevision, Service: request.Service, RegistrySHA256: brokercontract.RegistrySHA256(), ContractSchemaSHA256: brokercontract.SchemaSHA256(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V2(), IssuedAtMillis: time.Now().UnixMilli(), ExpiresAtMillis: request.NotAfterMillis, Complete: true}
	for _, d := range brokercontract.AvailableDefinitions() {
		if d.BackendService == request.Service {
			p.Operations = append(p.Operations, domain.BrokerDiscoveryOperationV2{ID: d.ID, Version: d.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed, Features: d.RequiredFeatures, Limits: d.Limits, Effects: d.Effects})
		}
	}
	return p
}

func TestDiscoveryClientClosedDenialDoesNotGuessFromHTTPStatus(t *testing.T) {
	for _, reason := range []domain.BrokerReason{domain.BrokerReasonRevoked, domain.BrokerReasonGrantExpired, domain.BrokerReasonStaleAuthority, domain.BrokerReasonUnsupported, domain.BrokerReasonAuthorizationUnavailable} {
		var calls atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTeapot)
			body, _ := brokertransport.EncodeDiscoveryFailureV2(reason)
			_, _ = w.Write(body)
		}))
		client := newTestClient(t, server, &countingSessionLoader{value: testSession()}, "broker-1")
		_, err := client.Discover(t.Context(), "jira")
		got, _ := brokercontract.Reason(err)
		if got != reason || calls.Load() != 1 {
			t.Fatalf("reason=%s got=%s calls=%d", reason, got, calls.Load())
		}
		server.Close()
	}
	loader := &countingSessionLoader{value: testSession()}
	client, _ := New(Config{BaseURL: "https://127.0.0.1:1", BrokerID: "broker-1", Audience: "atl-broker", Session: loader})
	if _, err := client.Discover(context.Background(), "other"); !errors.Is(err, domain.ErrUsage) || loader.calls.Load() != 0 {
		t.Fatalf("err=%v session=%d", err, loader.calls.Load())
	}
}
