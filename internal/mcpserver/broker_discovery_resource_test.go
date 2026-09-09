package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type discoveryReaderFunc func(context.Context, string) (domain.BrokerDiscoveryProjectionV2, error)

func (f discoveryReaderFunc) Discover(ctx context.Context, service string) (domain.BrokerDiscoveryProjectionV2, error) {
	return f(ctx, service)
}

type familyDiscoveryReaderStub struct {
	discoveryReaderFunc
	family func(context.Context, string, string) (domain.BrokerFamilyDiscoveryProjectionV3, error)
}

func (stub *familyDiscoveryReaderStub) DiscoverFamily(ctx context.Context, service, family string) (domain.BrokerFamilyDiscoveryProjectionV3, error) {
	return stub.family(ctx, service, family)
}

func brokerResourceCount(profile ServiceProfile) int {
	switch profile {
	case ServiceDefault:
		return 5
	case ServiceJira:
		return 4
	case ServiceConfluence:
		return 3
	default:
		return 2
	}
}

func familyDiscoveryProjection(service, family string) domain.BrokerFamilyDiscoveryProjectionV3 {
	request := domain.BrokerFamilyDiscoveryRequestV3{
		SchemaVersion: 3, RequestID: "request-family-1", ContractFamily: family, Service: service,
		BrokerID: "broker-1", Audience: "audience-1", ContextSHA256: strings.Repeat("b", 64), NotAfterMillis: 6000,
		Expect: domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"},
	}
	digest, _ := brokercontract.FamilyDiscoveryRequestSHA256V3(request)
	operations := make([]domain.BrokerFamilyDiscoveryOperationV3, 0, len(brokercontract.AvailableDefinitionsV2()))
	for _, wrapped := range brokercontract.AvailableDefinitionsV2() {
		definition := wrapped.Definition
		operations = append(operations, domain.BrokerFamilyDiscoveryOperationV3{
			ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed,
			Features: definition.RequiredFeatures, Limits: definition.Limits, Effects: definition.Effects,
		})
	}
	return domain.BrokerFamilyDiscoveryProjectionV3{
		SchemaVersion: 3, RequestID: request.RequestID, RequestSHA256: digest, ContextSHA256: request.ContextSHA256,
		ExecutionID: request.Expect.ExecutionID, ExecutionEpoch: request.Expect.ExecutionEpoch,
		Audience: request.Audience, BrokerID: request.BrokerID, AuthorityRevision: request.Expect.AuthorityRevision,
		ContractFamily: family, Service: service, RegistrySHA256: brokercontract.RegistrySHA256V2(),
		ContractSchemaSHA256: brokercontract.ExecutionSchemaSHA256V2(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V3(),
		IssuedAtMillis: 2000, ExpiresAtMillis: 6000, Operations: operations, Complete: true,
	}
}

func discoveryProjection(service string, access domain.BrokerDiscoveryAccess) domain.BrokerDiscoveryProjectionV2 {
	projection := domain.BrokerDiscoveryProjectionV2{
		SchemaVersion: 2, RequestID: "request-1", RequestSHA256: strings.Repeat("a", 64), ContextSHA256: strings.Repeat("b", 64),
		ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", Audience: "audience-1", BrokerID: "broker-1", AuthorityRevision: "revision-1", Service: service,
		RegistrySHA256: brokercontract.RegistrySHA256(), ContractSchemaSHA256: brokercontract.SchemaSHA256(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V2(),
		IssuedAtMillis: 2000, ExpiresAtMillis: 6000, Complete: true,
	}
	for _, definition := range brokercontract.AvailableDefinitions() {
		if definition.BackendService != service {
			continue
		}
		operation := domain.BrokerDiscoveryOperationV2{ID: definition.ID, Version: definition.Version, Supported: true, Access: access, Features: definition.RequiredFeatures, Limits: definition.Limits, Effects: definition.Effects}
		if access == domain.BrokerDiscoveryAccessRequestRequired {
			operation.RequestAccessCorrelation = "request-access-1"
		}
		projection.Operations = append(projection.Operations, operation)
	}
	return projection
}

func TestBrokerFamilyDiscoveryResourceIsStaticPrivateAndFresh(t *testing.T) {
	for _, profile := range []ServiceProfile{ServiceDefault, ServiceJira, ServiceConfluence, ServiceOffline} {
		t.Run(string(profile), func(t *testing.T) {
			var loads, familyReads, legacyReads atomic.Int32
			deps := Dependencies{BrokerDiscovery: func(service string) (domain.BrokerDiscoveryReader, error) {
				loads.Add(1)
				return &familyDiscoveryReaderStub{
					discoveryReaderFunc: func(context.Context, string) (domain.BrokerDiscoveryProjectionV2, error) {
						legacyReads.Add(1)
						return domain.BrokerDiscoveryProjectionV2{}, nil
					},
					family: func(_ context.Context, requestedService, requestedFamily string) (domain.BrokerFamilyDiscoveryProjectionV3, error) {
						familyReads.Add(1)
						if service != domain.ServerProductJira || requestedService != service || requestedFamily != domain.BrokerContractFamilyExecutionV2 {
							return domain.BrokerFamilyDiscoveryProjectionV3{}, domain.ErrCheckFailed
						}
						return familyDiscoveryProjection(service, requestedFamily), nil
					},
				}, nil
			}}
			client, closeSessions := connectTestClient(t, NewForService("test", deps, profile))
			defer closeSessions()
			listed, err := client.ListResources(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			present := false
			for _, resource := range listed.Resources {
				present = present || resource.URI == BrokerDiscoveryJiraExecutionV2URI
			}
			wantPresent := profile == ServiceDefault || profile == ServiceJira
			if present != wantPresent || loads.Load() != 0 || familyReads.Load() != 0 || legacyReads.Load() != 0 {
				t.Fatalf("present=%t want=%t loads=%d family=%d legacy=%d", present, wantPresent, loads.Load(), familyReads.Load(), legacyReads.Load())
			}
			if !wantPresent {
				if _, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: BrokerDiscoveryJiraExecutionV2URI}); err == nil {
					t.Fatal("excluded family resource was readable")
				}
				return
			}
			for range 2 {
				result, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: BrokerDiscoveryJiraExecutionV2URI})
				if err != nil || result.TTLMs != 0 || result.CacheScope != "private" || len(result.Contents) != 1 || result.Contents[0].URI != BrokerDiscoveryJiraExecutionV2URI {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				projection, err := brokercontract.DecodeFamilyDiscoveryProjectionV3([]byte(result.Contents[0].Text))
				if err != nil || projection.Service != domain.ServerProductJira || projection.ContractFamily != domain.BrokerContractFamilyExecutionV2 {
					t.Fatalf("projection=%+v err=%v", projection, err)
				}
			}
			if loads.Load() != 2 || familyReads.Load() != 2 || legacyReads.Load() != 0 {
				t.Fatalf("loads=%d family=%d legacy=%d", loads.Load(), familyReads.Load(), legacyReads.Load())
			}
		})
	}
}

func TestBrokerFamilyDiscoveryUnknownURIAndLegacyReaderFailClosed(t *testing.T) {
	var loads atomic.Int32
	deps := Dependencies{BrokerDiscovery: func(string) (domain.BrokerDiscoveryReader, error) {
		loads.Add(1)
		return discoveryReaderFunc(func(context.Context, string) (domain.BrokerDiscoveryProjectionV2, error) {
			return domain.BrokerDiscoveryProjectionV2{}, nil
		}), nil
	}}
	client, closeSessions := connectTestClient(t, NewForService("test", deps, ServiceJira))
	defer closeSessions()
	for _, uri := range []string{
		"atl://broker/discovery/jira/atl.broker.execution.v1",
		"atl://broker/discovery/confluence/atl.broker.execution.v2",
	} {
		if _, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Fatalf("unknown family URI %q was readable", uri)
		}
	}
	if loads.Load() != 0 {
		t.Fatalf("unknown family URI loaded configuration %d times", loads.Load())
	}
	_, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: BrokerDiscoveryJiraExecutionV2URI})
	if err == nil || loads.Load() != 1 || !strings.Contains(err.Error(), "usage_error") {
		t.Fatalf("legacy reader err=%v loads=%d", err, loads.Load())
	}
}

func TestBrokerDiscoveryResourceProfilesAreLazyAndReadFresh(t *testing.T) {
	for _, profile := range []ServiceProfile{ServiceDefault, ServiceJira, ServiceConfluence, ServiceOffline} {
		t.Run(string(profile), func(t *testing.T) {
			var loads, reads atomic.Int32
			var access atomic.Value
			access.Store(domain.BrokerDiscoveryAccessRequestRequired)
			deps := Dependencies{BrokerDiscovery: func(service string) (domain.BrokerDiscoveryReader, error) {
				loads.Add(1)
				return discoveryReaderFunc(func(_ context.Context, requested string) (domain.BrokerDiscoveryProjectionV2, error) {
					reads.Add(1)
					if requested != service {
						return domain.BrokerDiscoveryProjectionV2{}, domain.ErrCheckFailed
					}
					return discoveryProjection(service, access.Load().(domain.BrokerDiscoveryAccess)), nil
				}), nil
			}}
			client, closeSessions := connectTestClient(t, NewForService("test", deps, profile))
			defer closeSessions()
			listed, err := client.ListResources(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(listed.Resources) != brokerResourceCount(profile) || loads.Load() != 0 || reads.Load() != 0 {
				t.Fatalf("listing eagerly loaded discovery or wrong inventory: %+v", listed)
			}
			if listed.TTLMs != 0 || listed.CacheScope != "public" {
				t.Fatalf("listing cache: %+v", listed.Cacheable)
			}
			for _, service := range []string{"jira", "confluence"} {
				uri := "atl://broker/discovery/" + service
				if profile != ServiceDefault && string(profile) != service {
					if _, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri}); err == nil {
						t.Fatalf("excluded service %s readable", service)
					}
					continue
				}
				for _, state := range []domain.BrokerDiscoveryAccess{domain.BrokerDiscoveryAccessRequestRequired, domain.BrokerDiscoveryAccessAllowed, domain.BrokerDiscoveryAccessUnavailable} {
					access.Store(state)
					before := loads.Load()
					result, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
					if err != nil {
						t.Fatal(err)
					}
					if loads.Load() != before+1 || reads.Load() != loads.Load() || result.TTLMs != 0 || result.CacheScope != "private" || len(result.Contents) != 1 {
						t.Fatalf("read freshness/cache: %+v loads=%d reads=%d", result, loads.Load(), reads.Load())
					}
					projection, err := brokercontract.DecodeDiscoveryProjectionV2([]byte(result.Contents[0].Text))
					if err != nil || projection.Service != service || projection.Operations[0].Access != state {
						t.Fatalf("projection=%+v err=%v", projection, err)
					}
				}
			}
		})
	}
}

func TestBrokerDiscoveryResourceErrorsAreClosedAndNotCached(t *testing.T) {
	var loads atomic.Int32
	deps := Dependencies{BrokerDiscovery: func(string) (domain.BrokerDiscoveryReader, error) {
		loads.Add(1)
		return nil, fmt.Errorf("%w: private-secret-value", domain.ErrConfig)
	}}
	client, closeSessions := connectTestClient(t, New("test", deps))
	defer closeSessions()
	for range 2 {
		_, err := client.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: BrokerDiscoveryJiraURI})
		if err == nil || strings.Contains(err.Error(), "private-secret-value") {
			t.Fatalf("unsafe error=%v", err)
		}
		if !strings.Contains(err.Error(), "configuration_error") || !strings.Contains(err.Error(), "recovery") {
			t.Fatalf("missing structured error=%v", err)
		}
	}
	if loads.Load() != 2 {
		t.Fatalf("loads=%d", loads.Load())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(brokerDiscoveryReadPolicy.classify(domain.ErrConfig).Error()), &body); err != nil || len(body) != 4 {
		t.Fatalf("error envelope=%v err=%v", body, err)
	}
}

func TestBrokerDiscoveryProductionDependencyLoadsConfigOnlyOnReadWithoutPAT(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", directory)
	if err := os.Mkdir(filepath.Join(directory, "credentials.json"), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, []byte("}{"), 0600); err != nil {
		t.Fatal(err)
	}
	deps := ProductionDependencies("test")
	client, closeSessions := connectTestClient(t, New("test", deps))
	defer closeSessions()
	if _, err := client.ListResources(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.BrokerDiscovery("jira"); err == nil {
		t.Fatal("malformed config not loaded on demand")
	}
	config := `{"connection_mode":"broker","broker":{"base_url":"https://broker.example.test","broker_id":"broker-1","audience":"atl-broker","jira_session_file":"/synthetic/session.json"}}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if reader, err := deps.BrokerDiscovery("jira"); err != nil || reader == nil {
		t.Fatalf("Broker client load read PAT store or session: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{"connection_mode":"direct","jira_url":"https://jira.example.test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.BrokerDiscovery("jira"); err == nil {
		t.Fatal("direct mode accepted for discovery")
	}
}
