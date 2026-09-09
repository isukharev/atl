package brokerauthority

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestAuthorityFamilyDiscoveryV3UsesExactRouteAndBindsProjection(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, projection := authorityFamilyDiscoveryV3Fixture(t, now)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		calls.Add(1)
		if incoming.Method != http.MethodPost || incoming.URL.Path != brokertransport.DiscoveryPathV3 ||
			incoming.Header.Get("Authorization") != "Bearer synthetic-server-credential" || incoming.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected discovery request %s %s headers=%v", incoming.Method, incoming.URL.Path, incoming.Header)
		}
		body, _ := io.ReadAll(incoming.Body)
		decoded, err := brokercontract.DecodeFamilyDiscoveryAuthorizationRequestV3(body)
		if err != nil || !reflect.DeepEqual(decoded, request) {
			t.Errorf("request=%+v err=%v", decoded, err)
		}
		encoded, _ := brokercontract.EncodeFamilyDiscoveryProjectionV3(projection)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(encoded)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	authority.now = func() time.Time { return now }
	got, err := authority.DiscoverFamilyV3(t.Context(), request)
	if err != nil || calls.Load() != 1 || !reflect.DeepEqual(got, projection) {
		t.Fatalf("projection=%+v err=%v calls=%d", got, err, calls.Load())
	}
}

func TestAuthorityFamilyDiscoveryV3PreservesStrictFailureReason(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, _ := authorityFamilyDiscoveryV3Fixture(t, now)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if incoming.URL.Path != brokertransport.DiscoveryPathV3 {
			t.Errorf("unexpected fallback path %s", incoming.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		encoded, _ := brokertransport.EncodeDiscoveryFailureV3(domain.BrokerReasonRevoked)
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write(encoded)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	_, err := authority.DiscoverFamilyV3(t.Context(), request)
	if reason, ok := brokercontract.Reason(err); !ok || reason != domain.BrokerReasonRevoked {
		t.Fatalf("reason=%s ok=%v err=%v", reason, ok, err)
	}
}

func TestAuthorityFamilyDiscoveryV3RejectsValidWrongBindingSeparatelyFromMalformed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, projection := authorityFamilyDiscoveryV3Fixture(t, now)
	for _, test := range []struct {
		name string
		body func() []byte
	}{
		{name: "valid wrong binding", body: func() []byte {
			changed := projection
			changed.RequestID = "request-other"
			encoded, err := brokercontract.EncodeFamilyDiscoveryProjectionV3(changed)
			if err != nil {
				t.Fatal(err)
			}
			return encoded
		}},
		{name: "malformed", body: func() []byte {
			encoded, _ := brokercontract.EncodeFamilyDiscoveryProjectionV3(projection)
			return bytes.Replace(encoded, []byte(`"schema_version":3`), []byte(`"schema_version":3,"schema_version":3`), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(test.body())
			}))
			t.Cleanup(server.Close)
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			authority.now = func() time.Time { return now }
			_, err := authority.DiscoverFamilyV3(t.Context(), request)
			if reason, ok := brokercontract.Reason(err); !ok || reason != domain.BrokerReasonAuthorizationUnavailable {
				t.Fatalf("reason=%s ok=%v err=%v", reason, ok, err)
			}
		})
	}
}

func TestAuthorityFamilyDiscoveryV3FailuresAreBoundedSingleAttemptAndNeverFallBack(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, projection := authorityFamilyDiscoveryV3Fixture(t, now)
	valid, _ := brokercontract.EncodeFamilyDiscoveryProjectionV3(projection)
	validFailure, _ := brokertransport.EncodeDiscoveryFailureV3(domain.BrokerReasonRevoked)
	for _, test := range []struct {
		name   string
		status int
		body   []byte
	}{
		{name: "absent", status: http.StatusNotFound, body: []byte("private-authority-canary")},
		{name: "transient", status: http.StatusServiceUnavailable, body: []byte("private-authority-canary")},
		{name: "redirect", status: http.StatusTemporaryRedirect, body: []byte("private-authority-canary")},
		{name: "oversize success", status: http.StatusOK, body: append(bytes.Clone(valid), bytes.Repeat([]byte(" "), int(brokercontract.MaxDiscoveryV3Bytes)+1)...)},
		{name: "oversize failure", status: http.StatusForbidden, body: append(bytes.Clone(validFailure), bytes.Repeat([]byte(" "), int(brokertransport.MaxTransportFailureBytes)+1)...)},
		{name: "trailing object", status: http.StatusOK, body: append(bytes.Clone(valid), []byte(`{"private":"private-authority-canary"}`)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			var fallback atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				calls.Add(1)
				if incoming.URL.Path == "/v2/discovery" {
					fallback.Add(1)
				}
				if test.name == "redirect" {
					writer.Header().Set("Location", "/v2/discovery")
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write(test.body)
			}))
			t.Cleanup(server.Close)
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			authority.now = func() time.Time { return now }
			_, err := authority.DiscoverFamilyV3(t.Context(), request)
			if reason, ok := brokercontract.Reason(err); !ok || reason != domain.BrokerReasonAuthorizationUnavailable || calls.Load() != 1 || fallback.Load() != 0 {
				t.Fatalf("reason=%s ok=%v err=%v calls=%d fallback=%d", reason, ok, err, calls.Load(), fallback.Load())
			}
			assertAuthorityErrorContentFree(t, err, server.URL, brokertransport.DiscoveryPathV3, "private-authority-canary")
		})
	}
}

func TestAuthorityFamilyDiscoveryV3ParentCancellationAfterArrivalFailsClosed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, _ := authorityFamilyDiscoveryV3Fixture(t, now)
	arrived := make(chan struct{}, 1)
	releaseServer := make(chan struct{})
	defer close(releaseServer)
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, incoming *http.Request) {
		arrived <- struct{}{}
		select {
		case <-incoming.Context().Done():
		case <-releaseServer:
		}
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	authority.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := authority.DiscoverFamilyV3(ctx, request)
		done <- err
	}()
	waitAuthorityArrival(t, arrived, done)
	cancel()
	err := waitAuthorityResult(t, done)
	if reason, ok := brokercontract.Reason(err); !ok || reason != domain.BrokerReasonAuthorizationUnavailable {
		t.Fatalf("reason=%s ok=%v err=%v", reason, ok, err)
	}
}

func TestAuthorityFamilyDiscoveryV3FiveSecondDeadlineAndParentClipUseSchedulerBoundary(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, _ := authorityFamilyDiscoveryV3Fixture(t, now)
	scheduler, err := httpx.NewScheduler(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	holderArrived := make(chan struct{}, 1)
	releaseHolder := make(chan struct{})
	var discoveryCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if incoming.URL.Path == "/hold" {
			holderArrived <- struct{}{}
			select {
			case <-releaseHolder:
			case <-incoming.Context().Done():
			}
			_, _ = writer.Write([]byte(`{}`))
			return
		}
		discoveryCalls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthorityWithScheduler(t, server, strings.Repeat("a", 64), scheduler)
	authority.now = func() time.Time { return now }
	budget, err := domain.NewReadBudget(1, 16)
	if err != nil {
		t.Fatal(err)
	}
	holderDone := make(chan error, 1)
	holderContext, cancelHolder := context.WithTimeout(t.Context(), 9*time.Second)
	defer cancelHolder()
	go func() {
		ctx := domain.WithSingleAttempt(domain.WithReadIntent(domain.WithReadBudget(holderContext, budget)))
		_, holdErr := authority.client.DoBoundedResponse(ctx, http.MethodPost, "/hold", []byte(`{}`), nil, 16, 16)
		holderDone <- holdErr
	}()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseHolder) }) }
	defer release()
	waitAuthorityArrival(t, holderArrived, holderDone)

	started := time.Now()
	if _, err := authority.DiscoverFamilyV3(t.Context(), request); reasonForAuthorityError(err) != domain.BrokerReasonAuthorizationUnavailable {
		t.Fatalf("five-second bound err=%v", err)
	}
	if elapsed := time.Since(started); elapsed < authorityCallTimeout-time.Second || elapsed > authorityCallTimeout+2*time.Second {
		t.Fatalf("authority bound elapsed=%s", elapsed)
	}

	parent, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started = time.Now()
	if _, err := authority.DiscoverFamilyV3(parent, request); reasonForAuthorityError(err) != domain.BrokerReasonAuthorizationUnavailable {
		t.Fatalf("parent clip err=%v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("parent deadline was not clipped: %s", elapsed)
	}
	if discoveryCalls.Load() != 0 {
		t.Fatalf("blocked discovery calls reached server: %d", discoveryCalls.Load())
	}
	release()
	if err := waitAuthorityResult(t, holderDone); err != nil {
		t.Fatalf("holder err=%v", err)
	}
}

func reasonForAuthorityError(err error) domain.BrokerReason {
	reason, _ := brokercontract.Reason(err)
	return reason
}

func TestAuthorityFamilyDiscoveryV3BoundsStreamingBodiesBeforeEOF(t *testing.T) {
	for _, success := range []bool{true, false} {
		name := "failure"
		if success {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			request, projection := authorityFamilyDiscoveryV3Fixture(t, now)
			body, err := brokertransport.EncodeDiscoveryFailureV3(domain.BrokerReasonRevoked)
			maximum, status := brokertransport.MaxTransportFailureBytes, http.StatusForbidden
			if success {
				body, err = brokercontract.EncodeFamilyDiscoveryProjectionV3(projection)
				maximum, status = brokercontract.MaxDiscoveryV3Bytes, http.StatusOK
			}
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, bytes.Repeat([]byte(" "), int(maximum)+1)...)
			release := make(chan struct{})
			defer close(release)
			arrived := make(chan struct{}, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				arrived <- struct{}{}
				writer.WriteHeader(status)
				_, _ = writer.Write(body)
				_ = http.NewResponseController(writer).Flush()
				// Deliberately withhold response EOF until the client has
				// returned. A decoder after unbounded io.ReadAll cannot pass.
				<-release
			}))
			t.Cleanup(server.Close)
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := authority.DiscoverFamilyV3(ctx, request)
				done <- err
			}()
			waitAuthorityArrival(t, arrived, done)
			select {
			case err := <-done:
				if reasonForAuthorityError(err) != domain.BrokerReasonAuthorizationUnavailable || ctx.Err() != nil {
					t.Fatalf("body bound returned error=%v context=%v", err, ctx.Err())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("oversized response waited for EOF instead of enforcing its byte bound")
			}
		})
	}
}

func authorityFamilyDiscoveryV3Fixture(t testing.TB, now time.Time) (domain.BrokerFamilyDiscoveryAuthorizationRequestV3, domain.BrokerFamilyDiscoveryProjectionV3) {
	t.Helper()
	verified := authorityVerifiedContext(now)
	contextSHA256, err := brokercontract.VerifiedContextSHA256(verified)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.BrokerFamilyDiscoveryRequestV3{
		SchemaVersion: domain.BrokerDiscoverySchemaVersionV3, RequestID: "request-discovery-1", ContractFamily: domain.BrokerContractFamilyExecutionV2,
		Service: verified.Backend.Service, BrokerID: verified.BrokerID, Audience: verified.Audience, ContextSHA256: contextSHA256,
		NotAfterMillis: now.Add(5 * time.Second).UnixMilli(),
		Expect:         domain.BrokerRequestExpectations{ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, AuthorityRevision: verified.AuthorityRevision},
	}
	requestSHA256, err := brokercontract.FamilyDiscoveryRequestSHA256V3(request)
	if err != nil {
		t.Fatal(err)
	}
	operations := make([]domain.BrokerFamilyDiscoveryOperationV3, 0, len(brokercontract.AvailableDefinitionsV2()))
	for _, definition := range brokercontract.AvailableDefinitionsV2() {
		operations = append(operations, domain.BrokerFamilyDiscoveryOperationV3{
			ID: definition.Definition.ID, Version: definition.Definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessUnavailable,
			Features: append([]string(nil), definition.Definition.RequiredFeatures...), Limits: definition.Definition.Limits,
			Effects: append([]domain.BrokerEffectDefinition(nil), definition.Definition.Effects...),
		})
	}
	authorization := domain.BrokerFamilyDiscoveryAuthorizationRequestV3{SchemaVersion: domain.BrokerDiscoverySchemaVersionV3, Request: request, Context: verified, RequestSHA256: requestSHA256}
	projection := domain.BrokerFamilyDiscoveryProjectionV3{
		SchemaVersion: domain.BrokerDiscoverySchemaVersionV3, RequestID: request.RequestID, RequestSHA256: requestSHA256, ContextSHA256: contextSHA256,
		ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch, Audience: verified.Audience, BrokerID: verified.BrokerID,
		AuthorityRevision: verified.AuthorityRevision, ContractFamily: request.ContractFamily, Service: request.Service,
		RegistrySHA256: brokercontract.RegistrySHA256V2(), ContractSchemaSHA256: brokercontract.ExecutionSchemaSHA256V2(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V3(),
		IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: request.NotAfterMillis, Operations: operations, Complete: true,
	}
	return authorization, projection
}
