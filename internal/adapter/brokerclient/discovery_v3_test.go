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

type replacingFamilySessionLoader struct {
	calls atomic.Int32
}

func (loader *replacingFamilySessionLoader) Load() (Session, error) {
	value := testSession()
	if loader.calls.Add(1) > 1 {
		value.Credential = []byte("replacement-workload-credential")
	}
	return value, nil
}

type familyDiscoveryResponder func(http.ResponseWriter, domain.BrokerFamilyDiscoveryRequestV3, []byte)

func TestFamilyDiscoveryClientRejectsElapsedLeaseEvenWhenWallCurrent(t *testing.T) {
	started := time.Now()
	var advanced atomic.Bool
	var calls atomic.Int32
	server := newFamilyDiscoveryBroker(t, &calls, func(w http.ResponseWriter, _ domain.BrokerFamilyDiscoveryRequestV3, valid []byte) {
		projection, err := brokercontract.DecodeFamilyDiscoveryProjectionV3(valid)
		if err != nil {
			t.Error(err)
			return
		}
		projection.IssuedAtMillis = started.Add(2 * time.Second).UnixMilli()
		projection.ExpiresAtMillis = started.Add(3 * time.Second).UnixMilli()
		body, err := brokercontract.EncodeFamilyDiscoveryProjectionV3(projection)
		if err != nil {
			t.Error(err)
			return
		}
		advanced.Store(true)
		_, _ = w.Write(body)
	})
	t.Cleanup(server.Close)
	client := newTestClient(t, server, &countingSessionLoader{value: testSession()}, "broker-1")
	client.now = func() time.Time {
		if advanced.Load() {
			return started.Add(2 * time.Second)
		}
		return started
	}
	projection, err := client.DiscoverFamily(t.Context(), "jira", domain.BrokerContractFamilyExecutionV2)
	if reason, _ := brokercontract.Reason(err); reason != domain.BrokerReasonDecisionExpired || projection.Complete || calls.Load() != 2 {
		t.Fatalf("accepted elapsed lease: reason=%s projection=%+v calls=%d error=%v", reason, projection, calls.Load(), err)
	}
}

func TestFamilyDiscoveryClientReturnsFreshAdvisoryProjection(t *testing.T) {
	loader := &countingSessionLoader{value: testSession()}
	var calls atomic.Int32
	server := newFamilyDiscoveryBroker(t, &calls, nil)
	client := newTestClient(t, server, loader, "broker-1")
	projection, err := client.DiscoverFamily(t.Context(), "jira", domain.BrokerContractFamilyExecutionV2)
	if err != nil || !projection.Complete || projection.ContractFamily != domain.BrokerContractFamilyExecutionV2 || projection.Service != "jira" {
		t.Fatalf("projection=%+v err=%v", projection, err)
	}
	available := brokercontract.AvailableDefinitionsV2()
	if len(projection.Operations) != len(available) {
		t.Fatalf("projection operations=%d available definitions=%d", len(projection.Operations), len(available))
	}
	if !brokercontract.RegistryV2()[0].Definition.Available && len(projection.Operations) != 0 {
		t.Fatalf("unavailable registry unexpectedly advertised operations: %+v", projection.Operations)
	}
	if calls.Load() != 2 || loader.calls.Load() != 2 {
		t.Fatalf("requests=%d session loads=%d", calls.Load(), loader.calls.Load())
	}
}

func TestFamilyDiscoveryClientRejectsInvalidSelectorsBeforeSessionOrNetwork(t *testing.T) {
	loader := &countingSessionLoader{value: testSession()}
	client, err := New(Config{BaseURL: "https://127.0.0.1:1", BrokerID: "broker-1", Audience: "atl-broker", Session: loader})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ service, family string }{
		{"confluence", domain.BrokerContractFamilyExecutionV2},
		{"jira", "atl.broker.execution.v1"},
		{"jira", ""},
	} {
		if _, err := client.DiscoverFamily(t.Context(), test.service, test.family); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("service=%q family=%q err=%v", test.service, test.family, err)
		}
	}
	if loader.calls.Load() != 0 {
		t.Fatalf("session loads=%d", loader.calls.Load())
	}
}

func TestFamilyDiscoveryClientRejectsHostileResponsesWithoutFallback(t *testing.T) {
	for _, test := range []struct {
		name      string
		respond   familyDiscoveryResponder
		loader    SessionLoader
		wantCalls int32
	}{
		{name: "malformed", wantCalls: 2, respond: func(w http.ResponseWriter, _ domain.BrokerFamilyDiscoveryRequestV3, _ []byte) {
			_, _ = w.Write([]byte(`{"private":"response-canary"}`))
		}},
		{name: "wrong family", wantCalls: 2, respond: func(w http.ResponseWriter, request domain.BrokerFamilyDiscoveryRequestV3, valid []byte) {
			_ = request
			_, _ = w.Write(bytes.Replace(valid, []byte(domain.BrokerContractFamilyExecutionV2), []byte("atl.broker.execution.v9"), 1))
		}},
		{name: "denied", wantCalls: 2, respond: func(w http.ResponseWriter, _ domain.BrokerFamilyDiscoveryRequestV3, _ []byte) {
			body, _ := brokertransport.EncodeDiscoveryFailureV3(domain.BrokerReasonDenied)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(body)
		}},
		{name: "oversized", wantCalls: 2, respond: func(w http.ResponseWriter, _ domain.BrokerFamilyDiscoveryRequestV3, valid []byte) {
			_, _ = w.Write(append(bytes.Clone(valid), bytes.Repeat([]byte(" "), int(brokercontract.MaxDiscoveryV3Bytes)+1)...))
		}},
		{name: "redirect", wantCalls: 2, respond: func(w http.ResponseWriter, _ domain.BrokerFamilyDiscoveryRequestV3, _ []byte) {
			w.Header().Set("Location", brokertransport.DiscoveryPathV2)
			w.WriteHeader(http.StatusTemporaryRedirect)
		}},
		{name: "same guards replaced credential", wantCalls: 2, loader: &replacingFamilySessionLoader{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			loader := test.loader
			if loader == nil {
				loader = &countingSessionLoader{value: testSession()}
			}
			var calls atomic.Int32
			server := newFamilyDiscoveryBroker(t, &calls, test.respond)
			client := newTestClient(t, server, loader, "broker-1")
			_, err := client.DiscoverFamily(t.Context(), "jira", domain.BrokerContractFamilyExecutionV2)
			if err == nil || calls.Load() != test.wantCalls {
				t.Fatalf("err=%v requests=%d", err, calls.Load())
			}
			if strings.Contains(err.Error(), "canary") {
				t.Fatal("private response content escaped")
			}
			if test.name == "same guards replaced credential" {
				reason, _ := brokercontract.Reason(err)
				if reason != domain.BrokerReasonStaleExecution {
					t.Fatalf("reason=%s err=%v", reason, err)
				}
			}
			if test.name == "denied" {
				reason, _ := brokercontract.Reason(err)
				if reason != domain.BrokerReasonDenied {
					t.Fatalf("reason=%s err=%v", reason, err)
				}
			}
		})
	}
}

func TestFamilyDiscoveryClientHonorsParentDeadlineAndBudget(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		var calls atomic.Int32
		server := newFamilyDiscoveryBroker(t, &calls, func(_ http.ResponseWriter, request domain.BrokerFamilyDiscoveryRequestV3, _ []byte) {
			<-time.After(time.Until(time.UnixMilli(request.NotAfterMillis)) + 25*time.Millisecond)
		})
		client := newTestClient(t, server, &countingSessionLoader{value: testSession()}, "broker-1")
		ctx, cancel := context.WithTimeout(t.Context(), 750*time.Millisecond)
		defer cancel()
		started := time.Now()
		if _, err := client.DiscoverFamily(ctx, "jira", domain.BrokerContractFamilyExecutionV2); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
		// The parent clips every phase, including TLS setup under load. The
		// successful two-request path is asserted separately above.
		if calls.Load() > 2 || time.Since(started) >= 2*time.Second {
			t.Fatalf("requests=%d elapsed=%v", calls.Load(), time.Since(started))
		}
	})

	t.Run("attempt budget", func(t *testing.T) {
		var calls atomic.Int32
		server := newFamilyDiscoveryBroker(t, &calls, nil)
		client := newTestClient(t, server, &countingSessionLoader{value: testSession()}, "broker-1")
		parent, err := domain.NewReadBudget(1, brokertransport.MaxDiscoveryNegotiationBytesV3+brokercontract.MaxDiscoveryV3Bytes)
		if err != nil {
			t.Fatal(err)
		}
		ctx := domain.WithReadBudget(t.Context(), parent)
		if _, err := client.DiscoverFamily(ctx, "jira", domain.BrokerContractFamilyExecutionV2); !errors.Is(err, domain.ErrReadAttemptBudgetExhausted) {
			t.Fatalf("err=%v", err)
		}
		if calls.Load() != 1 || parent.Usage().Attempts != 1 {
			t.Fatalf("requests=%d usage=%+v", calls.Load(), parent.Usage())
		}
	})

	t.Run("response budget", func(t *testing.T) {
		var calls atomic.Int32
		server := newFamilyDiscoveryBroker(t, &calls, nil)
		client := newTestClient(t, server, &countingSessionLoader{value: testSession()}, "broker-1")
		parent, err := domain.NewReadBudget(2, 1)
		if err != nil {
			t.Fatal(err)
		}
		ctx := domain.WithReadBudget(t.Context(), parent)
		if _, err := client.DiscoverFamily(ctx, "jira", domain.BrokerContractFamilyExecutionV2); !errors.Is(err, domain.ErrReadResponseBudgetExhausted) {
			t.Fatalf("err=%v", err)
		}
		if calls.Load() != 1 || parent.Usage().ResponseBytes > 1 {
			t.Fatalf("requests=%d usage=%+v", calls.Load(), parent.Usage())
		}
	})
}

func TestFamilyDiscoveryClientBoundsResponseBeforeStreamingEOF(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	server := newFamilyDiscoveryBroker(t, &calls, func(w http.ResponseWriter, _ domain.BrokerFamilyDiscoveryRequestV3, valid []byte) {
		_, _ = w.Write(append(bytes.Clone(valid), bytes.Repeat([]byte(" "), int(brokercontract.MaxDiscoveryV3Bytes)+1)...))
		_ = http.NewResponseController(w).Flush()
		<-release
	})
	client := newTestClient(t, server, &countingSessionLoader{value: testSession()}, "broker-1")
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.DiscoverFamily(ctx, "jira", domain.BrokerContractFamilyExecutionV2)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || ctx.Err() != nil || calls.Load() != 2 {
			t.Fatalf("error=%v context=%v requests=%d", err, ctx.Err(), calls.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("response byte bound waited for streaming EOF")
	}
}

func newFamilyDiscoveryBroker(t *testing.T, calls *atomic.Int32, respond familyDiscoveryResponder) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer synthetic-workload-credential" {
			t.Errorf("authorization header mismatch")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-ATL-Correlation-ID", "correlation-1")
		body, err := io.ReadAll(io.LimitReader(r.Body, brokercontract.MaxDiscoveryV3Bytes+1))
		if err != nil {
			t.Error(err)
			return
		}
		switch r.URL.Path {
		case brokertransport.DiscoveryNegotiatePathV3:
			hello, err := brokertransport.DecodeDiscoveryNegotiationV3(body)
			if err != nil {
				t.Error(err)
				return
			}
			request := domain.BrokerFamilyDiscoveryRequestV3{
				SchemaVersion: domain.BrokerDiscoverySchemaVersionV3, RequestID: hello.RequestID,
				ContractFamily: hello.ContractFamily, Service: hello.Service, BrokerID: hello.BrokerID, Audience: hello.Audience,
				ContextSHA256: strings.Repeat("a", 64), NotAfterMillis: hello.NotAfterMillis,
				Expect: domain.BrokerRequestExpectations{ExecutionID: hello.ExecutionID, ExecutionEpoch: hello.ExecutionEpoch, AuthorityRevision: hello.AuthorityRevision},
			}
			encoded, err := brokertransport.EncodeNegotiatedDiscoveryV3(request)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = w.Write(encoded)
		case brokertransport.DiscoveryPathV3:
			request, err := brokercontract.DecodeFamilyDiscoveryRequestV3(body)
			if err != nil {
				t.Error(err)
				return
			}
			projection := familyDiscoveryProjectionForClient(t, request, domain.BrokerDiscoveryAccessAllowed)
			encoded, err := brokercontract.EncodeFamilyDiscoveryProjectionV3(projection)
			if err != nil {
				t.Error(err)
				return
			}
			if respond != nil {
				respond(w, request, encoded)
				return
			}
			_, _ = w.Write(encoded)
		default:
			t.Errorf("unexpected fallback path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func familyDiscoveryProjectionForClient(t testing.TB, request domain.BrokerFamilyDiscoveryRequestV3, access domain.BrokerDiscoveryAccess) domain.BrokerFamilyDiscoveryProjectionV3 {
	t.Helper()
	digest, err := brokercontract.FamilyDiscoveryRequestSHA256V3(request)
	if err != nil {
		t.Fatal(err)
	}
	operations := make([]domain.BrokerFamilyDiscoveryOperationV3, 0, len(brokercontract.AvailableDefinitionsV2()))
	for _, wrapped := range brokercontract.AvailableDefinitionsV2() {
		definition := wrapped.Definition
		operation := domain.BrokerFamilyDiscoveryOperationV3{
			ID: definition.ID, Version: definition.Version, Supported: true, Access: access,
			Features: append([]string{}, definition.RequiredFeatures...), Limits: definition.Limits,
			Effects: append([]domain.BrokerEffectDefinition{}, definition.Effects...),
		}
		if access == domain.BrokerDiscoveryAccessRequestRequired {
			operation.RequestAccessCorrelation = "access-request-1"
		}
		operations = append(operations, operation)
	}
	now := time.Now()
	expires := request.NotAfterMillis
	if expires <= now.UnixMilli() {
		t.Fatal("test request already expired")
	}
	return domain.BrokerFamilyDiscoveryProjectionV3{
		SchemaVersion: domain.BrokerDiscoverySchemaVersionV3, RequestID: request.RequestID, RequestSHA256: digest,
		ContextSHA256: request.ContextSHA256, ExecutionID: request.Expect.ExecutionID, ExecutionEpoch: request.Expect.ExecutionEpoch,
		Audience: request.Audience, BrokerID: request.BrokerID, AuthorityRevision: request.Expect.AuthorityRevision,
		ContractFamily: request.ContractFamily, Service: request.Service, RegistrySHA256: brokercontract.RegistrySHA256V2(),
		ContractSchemaSHA256: brokercontract.ExecutionSchemaSHA256V2(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V3(),
		IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: expires, Operations: operations, Complete: true,
	}
}
