package brokerserver

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/adapter/brokerclient"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func (a *serverAuthorizerStub) DiscoverV2(_ context.Context, request domain.BrokerDiscoveryAuthorizationRequestV2) (domain.BrokerDiscoveryProjectionV2, error) {
	a.discoveryCalls++
	if a.discoveryReason != "" {
		_, err := brokercontract.ErrorForReason(a.discoveryReason)
		return domain.BrokerDiscoveryProjectionV2{}, err
	}
	access := a.discoveryAccess
	if access == "" {
		access = domain.BrokerDiscoveryAccessUnavailable
	}
	p := domain.BrokerDiscoveryProjectionV2{SchemaVersion: 2, RequestID: request.Request.RequestID, RequestSHA256: request.RequestSHA256, ContextSHA256: request.Request.ContextSHA256, ExecutionID: request.Context.ExecutionID, ExecutionEpoch: request.Context.ExecutionEpoch, Audience: request.Context.Audience, BrokerID: request.Context.BrokerID, AuthorityRevision: request.Context.AuthorityRevision, Service: request.Request.Service, RegistrySHA256: brokercontract.RegistrySHA256(), ContractSchemaSHA256: brokercontract.SchemaSHA256(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V2(), IssuedAtMillis: a.nowMillis, ExpiresAtMillis: min(a.nowMillis+5000, request.Request.NotAfterMillis), Complete: true}
	for _, definition := range brokercontract.AvailableDefinitions() {
		if definition.BackendService != p.Service {
			continue
		}
		correlation := ""
		if access == domain.BrokerDiscoveryAccessRequestRequired {
			correlation = "opaque-reference-1"
		}
		p.Operations = append(p.Operations, domain.BrokerDiscoveryOperationV2{ID: definition.ID, Version: definition.Version, Supported: true, Access: access, RequestAccessCorrelation: correlation, Features: definition.RequiredFeatures, Limits: definition.Limits, Effects: definition.Effects})
	}
	if a.discoveryMutate != nil {
		a.discoveryMutate(&p)
	}
	return p, nil
}

type discoverySessionLoader struct {
	revision string
	epoch    string
}

func (l *discoverySessionLoader) Load() (brokerclient.Session, error) {
	return brokerclient.Session{Credential: []byte("synthetic-workload-credential"), ExecutionID: "execution-1", ExecutionEpoch: l.epoch, AuthorityRevision: l.revision}, nil
}

func TestDiscoveryRunningClientObservesGrantRevokeAndCurrentSession(t *testing.T) {
	f := newBrokerServerFixture(t, "Synthetic", "", nil, []byte("synthetic-upstream-pat"))
	server := httptest.NewTLSServer(f.handler)
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	tls, _, err := httpx.QualifiedTLSOptions(ca)
	if err != nil {
		t.Fatal(err)
	}
	loader := &discoverySessionLoader{revision: "revision-1", epoch: "epoch-1"}
	client, err := brokerclient.New(brokerclient.Config{BaseURL: server.URL, BrokerID: "broker-1", Audience: "atl-broker", Session: loader, TLS: tls})
	if err != nil {
		t.Fatal(err)
	}
	for _, access := range []domain.BrokerDiscoveryAccess{domain.BrokerDiscoveryAccessUnavailable, domain.BrokerDiscoveryAccessAllowed, domain.BrokerDiscoveryAccessRequestRequired, domain.BrokerDiscoveryAccessUnavailable} {
		f.authorizer.discoveryAccess = access
		projection, err := client.Discover(t.Context(), "jira")
		if err != nil || len(projection.Operations) != 1 || projection.Operations[0].Access != access {
			t.Fatalf("access=%s result=%+v err=%v", access, projection, err)
		}
		encoded, _ := brokercontract.EncodeDiscoveryProjectionV2(projection)
		for _, private := range []string{"principal-1", "workload-1", "jira-primary", "PROJ", "Synthetic", "synthetic-upstream-pat", "confluence.page.read"} {
			if bytes.Contains(encoded, []byte(private)) {
				t.Fatalf("projection leaked %q", private)
			}
		}
	}
	if f.backendCalls.Load() != 0 || f.authenticator.calls != 8 || f.authorizer.discoveryCalls != 4 {
		t.Fatalf("backend=%d auth=%d discovery=%d", f.backendCalls.Load(), f.authenticator.calls, f.authorizer.discoveryCalls)
	}
	// An earlier allowed listing cannot authorize the next business operation.
	f.authorizer.denyPhase = domain.BrokerPhaseAdmission
	if _, err := client.ReadJiraIssue(t.Context(), "PROJ-7", []domain.BrokerJiraIssueField{domain.BrokerJiraIssueFieldSummary}); err == nil || f.backendCalls.Load() != 0 {
		t.Fatalf("cached allowance used: err=%v backend=%d", err, f.backendCalls.Load())
	}
	f.authenticator.authentication.Context.AuthorityRevision = "revision-2"
	if _, err := client.Discover(t.Context(), "jira"); err == nil {
		t.Fatal("stale session accepted")
	}
	loader.revision = "revision-2"
	if _, err := client.Discover(t.Context(), "jira"); err != nil {
		t.Fatalf("new session requires restart: %v", err)
	}
	f.authorizer.discoveryReason = domain.BrokerReasonRevoked
	if _, err := client.Discover(t.Context(), "jira"); err == nil {
		t.Fatal("revoked authority accepted")
	} else if reason, _ := brokercontract.Reason(err); reason != domain.BrokerReasonRevoked {
		t.Fatalf("reason=%s", reason)
	}
}

func TestDiscoveryServerRejectsUnboundAndUnsafeProjectionsWithoutBackendIO(t *testing.T) {
	for name, change := range map[string]func(*domain.BrokerDiscoveryProjectionV2){
		"request": func(p *domain.BrokerDiscoveryProjectionV2) { p.RequestID = "other" },
		"context": func(p *domain.BrokerDiscoveryProjectionV2) { p.ContextSHA256 = strings.Repeat("f", 64) },
		"expiry":  func(p *domain.BrokerDiscoveryProjectionV2) { p.ExpiresAtMillis = p.IssuedAtMillis },
		"sibling": func(p *domain.BrokerDiscoveryProjectionV2) { p.Service = "confluence" },
		"policy":  func(p *domain.BrokerDiscoveryProjectionV2) { p.Operations[0].Features = []string{"policy-rule"} },
		"credential": func(p *domain.BrokerDiscoveryProjectionV2) {
			p.Operations[0].Access = domain.BrokerDiscoveryAccessRequestRequired
			p.Operations[0].RequestAccessCorrelation = "synthetic-upstream-pat"
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBrokerServerFixture(t, "Synthetic", "", nil, []byte("synthetic-upstream-pat"))
			f.authorizer.discoveryMutate = change
			request := discoveryServerRequest(t, f)
			body, _ := brokercontract.EncodeDiscoveryRequestV2(request)
			response := brokerRequest(t, f.handler, http.MethodPost, brokertransport.DiscoveryPathV2, body, true)
			defer response.Body.Close()
			wire, _ := io.ReadAll(response.Body)
			if response.StatusCode == http.StatusOK || f.backendCalls.Load() != 0 || bytes.Contains(wire, []byte("synthetic-upstream-pat")) {
				t.Fatalf("status=%d backend=%d body=%s", response.StatusCode, f.backendCalls.Load(), wire)
			}
			if _, err := brokertransport.DecodeDiscoveryFailureV2(wire); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func discoveryServerRequest(t *testing.T, f brokerServerFixture) domain.BrokerDiscoveryRequestV2 {
	t.Helper()
	hello := brokertransport.DiscoveryNegotiationV2{SchemaVersion: 2, RequestID: "discovery-1", Service: "jira", BrokerID: "broker-1", Audience: "atl-broker", ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1", NotAfterMillis: f.baseTime.Add(5 * time.Second).UnixMilli()}
	request, err := brokertransport.BindDiscoveryNegotiationV2(hello, f.authenticator.authentication.Context, f.baseTime)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestDiscoveryServerStrictRouteAndAuthenticationBounds(t *testing.T) {
	for _, path := range []string{brokertransport.DiscoveryNegotiatePathV2, brokertransport.DiscoveryPathV2} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodHead} {
			f := newBrokerServerFixture(t, "Synthetic", "", nil)
			response := brokerRequest(t, f.handler, method, path, []byte(`{}`), true)
			response.Body.Close()
			if response.StatusCode == http.StatusOK || f.authenticator.calls != 0 || f.backendCalls.Load() != 0 {
				t.Fatalf("accepted %s %s", method, path)
			}
		}
	}
	for _, body := range [][]byte{[]byte(`{"schema_version":2,"schema_version":2}`), []byte(`{"authority":"injected"}`), bytes.Repeat([]byte("x"), int(brokertransport.MaxDiscoveryNegotiationBytesV2)+1)} {
		f := newBrokerServerFixture(t, "Synthetic", "", nil)
		response := brokerRequest(t, f.handler, http.MethodPost, brokertransport.DiscoveryNegotiatePathV2, body, true)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest || f.authenticator.calls != 0 || f.backendCalls.Load() != 0 {
			t.Fatal("malformed discovery reached authority")
		}
	}
}
