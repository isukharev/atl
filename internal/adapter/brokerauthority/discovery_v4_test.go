package brokerauthority

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func TestAuthorityFamilyDiscoveryV4UsesExactRouteAndCurrentProjection(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, projection := authorityFamilyDiscoveryV4Fixture(t, now)
	response := mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeFamilyDiscoveryProjectionV4(projection) })
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		calls.Add(1)
		if incoming.Method != http.MethodPost || incoming.URL.Path != brokertransport.DiscoveryPathV4 || incoming.Header.Get("Authorization") != "Bearer synthetic-server-credential" || incoming.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected discovery request method=%s path=%s headers=%v", incoming.Method, incoming.URL.Path, incoming.Header)
		}
		body, _ := io.ReadAll(incoming.Body)
		decoded, err := brokercontract.DecodeFamilyDiscoveryAuthorizationRequestV4(body)
		if err != nil || !reflect.DeepEqual(decoded, request) {
			t.Errorf("request=%+v err=%v", decoded, err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(response)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	authority.now = func() time.Time { return now }
	parent, _ := domain.NewReadBudget(1, brokercontract.MaxDiscoveryV4Bytes)
	got, err := authority.DiscoverFamilyV4(domain.WithReadBudget(t.Context(), parent), request)
	if err != nil || !reflect.DeepEqual(got, projection) || calls.Load() != 1 || parent.Usage().Attempts != 1 || brokercontract.ValidateFamilyDiscoveryProjectionV4ForContext(got, request, now) != nil {
		t.Fatalf("projection=%+v err=%v calls=%d usage=%+v", got, err, calls.Load(), parent.Usage())
	}
}

func TestAuthorityFamilyDiscoveryV4ReturnsClosedDenial(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, _ := authorityFamilyDiscoveryV4Fixture(t, now)
	body := mustAuthorityCall(t, func() ([]byte, error) { return brokertransport.EncodeDiscoveryFailureV4(domain.BrokerReasonDenied) })
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write(body)
	}))
	t.Cleanup(server.Close)
	authority := newTestAuthority(t, server, strings.Repeat("a", 64))
	if _, err := authority.DiscoverFamilyV4(t.Context(), request); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("denial err=%v", err)
	}
}

func TestAuthorityFamilyDiscoveryV4RejectsStaleMalformedAndExhaustedParent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	request, projection := authorityFamilyDiscoveryV4Fixture(t, now)
	valid := mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeFamilyDiscoveryProjectionV4(projection) })
	for _, test := range []struct {
		name     string
		body     []byte
		parent   func() *domain.ReadBudget
		wantCall int32
	}{
		{name: "stale binding", body: func() []byte {
			changed := projection
			changed.RequestSHA256 = strings.Repeat("f", 64)
			return mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeFamilyDiscoveryProjectionV4(changed) })
		}(), wantCall: 1},
		{name: "expired", body: func() []byte {
			changed := projection
			changed.IssuedAtMillis = now.Add(-time.Second).UnixMilli()
			changed.ExpiresAtMillis = now.Add(-time.Millisecond).UnixMilli()
			return mustAuthorityCall(t, func() ([]byte, error) { return brokercontract.EncodeFamilyDiscoveryProjectionV4(changed) })
		}(), wantCall: 1},
		{name: "malformed", body: bytes.Replace(valid, []byte(`"schema_version":4`), []byte(`"schema_version":4,"schema_version":4`), 1), wantCall: 1},
		{name: "exhausted parent", body: valid, parent: func() *domain.ReadBudget {
			budget, _ := domain.NewReadBudget(0, brokercontract.MaxDiscoveryV4Bytes)
			return budget
		}},
		{name: "byte parent", body: valid, parent: func() *domain.ReadBudget {
			budget, _ := domain.NewReadBudget(1, int64(len(valid)-1))
			return budget
		}, wantCall: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_, _ = writer.Write(test.body)
			}))
			t.Cleanup(server.Close)
			authority := newTestAuthority(t, server, strings.Repeat("a", 64))
			authority.now = func() time.Time { return now }
			ctx := t.Context()
			if test.parent != nil {
				ctx = domain.WithReadBudget(ctx, test.parent())
			}
			got, err := authority.DiscoverFamilyV4(ctx, request)
			if err == nil || !reflect.DeepEqual(got, domain.BrokerFamilyDiscoveryProjectionV4{}) || calls.Load() != test.wantCall {
				t.Fatalf("projection=%+v err=%v calls=%d", got, err, calls.Load())
			}
			if ok, _ := brokercontract.ContentFreeError(err); !ok {
				t.Fatalf("discovery error is not closed: %v", err)
			}
		})
	}
}

func authorityFamilyDiscoveryV4Fixture(t testing.TB, now time.Time) (domain.BrokerFamilyDiscoveryAuthorizationRequestV4, domain.BrokerFamilyDiscoveryProjectionV4) {
	t.Helper()
	contextValue := authorityVerifiedContext(now)
	contextDigest, err := brokercontract.VerifiedContextSHA256(contextValue)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.BrokerFamilyDiscoveryRequestV4{SchemaVersion: 4, RequestID: "request-1", ContractFamily: domain.BrokerContractFamilyExecutionV3, Service: "jira", BrokerID: contextValue.BrokerID, Audience: contextValue.Audience, ContextSHA256: contextDigest, NotAfterMillis: now.Add(5 * time.Second).UnixMilli(), Expect: domain.BrokerRequestExpectations{ExecutionID: contextValue.ExecutionID, ExecutionEpoch: contextValue.ExecutionEpoch, AuthorityRevision: contextValue.AuthorityRevision}}
	requestDigest, err := brokercontract.FamilyDiscoveryRequestSHA256V4(request)
	if err != nil {
		t.Fatal(err)
	}
	authorization := domain.BrokerFamilyDiscoveryAuthorizationRequestV4{SchemaVersion: 4, Request: request, Context: contextValue, RequestSHA256: requestDigest}
	projection := domain.BrokerFamilyDiscoveryProjectionV4{SchemaVersion: 4, RequestID: request.RequestID, RequestSHA256: requestDigest, ContextSHA256: contextDigest, ExecutionID: contextValue.ExecutionID, ExecutionEpoch: contextValue.ExecutionEpoch, Audience: contextValue.Audience, BrokerID: contextValue.BrokerID, AuthorityRevision: contextValue.AuthorityRevision, ContractFamily: request.ContractFamily, Service: request.Service, RegistrySHA256: brokercontract.RegistrySHA256V3(), ContractSchemaSHA256: brokercontract.ExecutionSchemaSHA256V3(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V4(), IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: request.NotAfterMillis, Operations: []domain.BrokerFamilyDiscoveryOperationV4{}, Complete: true}
	value := brokercontract.RegistryV3()[0]
	definition := value.Definition
	projection.Operations = []domain.BrokerFamilyDiscoveryOperationV4{{
		ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed,
		Features: definition.RequiredFeatures, Limits: definition.Limits, Effects: definition.Effects,
		MaxMetadataItems: value.MaxMetadataItems, MaxJiraAttempts: value.MaxJiraAttempts, MaxAuthenticationAttempts: value.MaxAuthenticationAttempts,
		MaxDecisionAttempts: value.MaxDecisionAttempts, MaxTotalHostOutboundAttempts: value.MaxTotalHostOutboundAttempts,
		MaxCommandHostOutboundAttempts: value.MaxCommandHostOutboundAttempts, MaxJiraResponseBytes: value.MaxJiraResponseBytes,
		MaxAuthorityResponseBytes: value.MaxAuthorityResponseBytes, MaxTotalHostResponseBytes: value.MaxTotalHostResponseBytes,
		MaxManifestLineBytes: value.MaxManifestLineBytes, MaxDataLineBytes: value.MaxDataLineBytes,
		MaxTerminalLineBytes: value.MaxTerminalLineBytes, MaxFramedResponseBytes: value.MaxFramedResponseBytes,
	}}
	return authorization, projection
}
