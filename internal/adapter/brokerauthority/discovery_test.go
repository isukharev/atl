package brokerauthority

import (
	"bytes"
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

func TestAuthorityDiscoveryUsesStrictCurrentContextAndClosedFailures(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	verified := authorityVerifiedContext(now)
	hello := brokertransport.DiscoveryNegotiationV2{SchemaVersion: 2, RequestID: "request-1", Service: verified.Backend.Service, BrokerID: verified.BrokerID, Audience: verified.Audience, ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, AuthorityRevision: verified.AuthorityRevision, NotAfterMillis: now.Add(5 * time.Second).UnixMilli()}
	request, err := brokertransport.BindDiscoveryNegotiationV2(hello, verified, now)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := brokercontract.DiscoveryRequestSHA256V2(request)
	authorization := domain.BrokerDiscoveryAuthorizationRequestV2{SchemaVersion: 2, Request: request, Context: verified, RequestSHA256: digest}
	for _, test := range []struct {
		name   string
		status int
		mutate func([]byte) []byte
		reason domain.BrokerReason
	}{
		{name: "current"},
		{name: "duplicate", mutate: func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"schema_version":2`), []byte(`"schema_version":2,"schema_version":2`), 1)
		}},
		{name: "cross request", mutate: func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("request-1"), []byte("request-other")) }},
		{name: "private member", mutate: func(b []byte) []byte { return append([]byte(`{"private":"authority-canary",`), b[1:]...) }},
		{name: "oversize", mutate: func([]byte) []byte { return bytes.Repeat([]byte("x"), int(brokercontract.MaxDiscoveryV2Bytes)+1) }},
		{name: "redirect", status: http.StatusTemporaryRedirect},
		{name: "raw error", status: http.StatusForbidden, mutate: func([]byte) []byte { return []byte("private-authority-canary") }},
		{name: "revoked", status: http.StatusTeapot, reason: domain.BrokerReasonRevoked},
		{name: "outage", status: http.StatusTeapot, reason: domain.BrokerReasonAuthorizationUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/v2/discovery" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("unexpected authority route")
				}
				body, _ := io.ReadAll(r.Body)
				bound, err := brokercontract.DecodeDiscoveryAuthorizationRequestV2(body)
				if err != nil || bound.RequestSHA256 != digest || bound.Context != verified {
					t.Error("authority request lost context binding")
					return
				}
				p := domain.BrokerDiscoveryProjectionV2{SchemaVersion: 2, RequestID: request.RequestID, RequestSHA256: digest, ContextSHA256: request.ContextSHA256, ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, Audience: verified.Audience, BrokerID: verified.BrokerID, AuthorityRevision: verified.AuthorityRevision, Service: request.Service, RegistrySHA256: brokercontract.RegistrySHA256(), ContractSchemaSHA256: brokercontract.SchemaSHA256(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V2(), IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: request.NotAfterMillis, Complete: true}
				for _, d := range brokercontract.AvailableDefinitions() {
					if d.BackendService == p.Service {
						p.Operations = append(p.Operations, domain.BrokerDiscoveryOperationV2{ID: d.ID, Version: d.Version, Supported: true, Access: domain.BrokerDiscoveryAccessUnavailable, Features: d.RequiredFeatures, Limits: d.Limits, Effects: d.Effects})
					}
				}
				encoded, err := brokercontract.EncodeDiscoveryProjectionV2(p)
				if err != nil {
					t.Error(err)
					return
				}
				if test.reason != "" {
					encoded, _ = brokertransport.EncodeDiscoveryFailureV2(test.reason)
				}
				if test.mutate != nil {
					encoded = test.mutate(encoded)
				}
				w.Header().Set("Content-Type", "application/json")
				if test.status != 0 {
					w.Header().Set("Location", "/private-fallback")
					w.WriteHeader(test.status)
				}
				_, _ = w.Write(encoded)
			}))
			t.Cleanup(server.Close)
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			authority.now = func() time.Time { return now }
			p, err := authority.DiscoverV2(t.Context(), authorization)
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
			if test.name == "current" {
				if err != nil || len(p.Operations) != 4 {
					t.Fatalf("operation count=%d err=%v", len(p.Operations), err)
				}
				for index, id := range []domain.BrokerOperationID{domain.BrokerOperationOutcomeLookup, domain.BrokerOperationJiraCommentApply, domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraIssueRead} {
					if p.Operations[index].ID != id || p.Operations[index].Access != domain.BrokerDiscoveryAccessUnavailable {
						t.Fatal("closed Jira discovery operation or advisory access changed")
					}
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "canary") {
				t.Fatalf("err=%v", err)
			}
			if reason, _ := brokercontract.Reason(err); test.reason != "" && reason != test.reason {
				t.Fatalf("reason=%s want=%s", reason, test.reason)
			}
		})
	}
}
