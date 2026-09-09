package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type brokerDiscoveryStub struct {
	access domain.BrokerDiscoveryAccess
	reads  int
}

func (r *brokerDiscoveryStub) Discover(_ context.Context, service string) (domain.BrokerDiscoveryProjectionV2, error) {
	r.reads++
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
		operation := domain.BrokerDiscoveryOperationV2{ID: definition.ID, Version: definition.Version, Supported: true, Access: r.access, Features: definition.RequiredFeatures, Limits: definition.Limits, Effects: definition.Effects}
		if r.access == domain.BrokerDiscoveryAccessRequestRequired {
			operation.RequestAccessCorrelation = "request-access-1"
		}
		projection.Operations = append(projection.Operations, operation)
	}
	return projection, nil
}

func TestBrokerDiscoverValidatesServiceBeforeLoading(t *testing.T) {
	previous := loadBrokerDiscovery
	t.Cleanup(func() { loadBrokerDiscovery = previous })
	loadBrokerDiscovery = func(string, string) (domain.BrokerDiscoveryReader, error) {
		t.Fatal("invalid invocation loaded discovery")
		return nil, nil
	}
	for _, args := range [][]string{{"broker", "discover"}, {"broker", "discover", "--service", "offline"}, {"broker", "discover", "--service", "jira", "extra"}} {
		stdout, _, err := executeCLIRaw(t, nil, args...)
		if stdout != "" || !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("args=%v stdout=%q err=%v", args, stdout, err)
		}
	}
}

func TestBrokerDiscoverEmitsValidatedProjectionAndReadsEveryInvocation(t *testing.T) {
	previous := loadBrokerDiscovery
	t.Cleanup(func() { loadBrokerDiscovery = previous })
	reader := &brokerDiscoveryStub{}
	loads := 0
	loadBrokerDiscovery = func(string, string) (domain.BrokerDiscoveryReader, error) { loads++; return reader, nil }
	for _, service := range []string{"jira", "confluence"} {
		for _, access := range []domain.BrokerDiscoveryAccess{domain.BrokerDiscoveryAccessRequestRequired, domain.BrokerDiscoveryAccessAllowed, domain.BrokerDiscoveryAccessUnavailable} {
			reader.access = access
			before := loads
			stdout, stderr, code := runCLIFull(t, nil, "broker", "discover", "--service", service)
			projection, err := brokercontract.DecodeDiscoveryProjectionV2([]byte(stdout))
			if code != exitOK || stderr != "" || err != nil || loads != before+1 || reader.reads != loads || projection.Service != service || projection.Operations[0].Access != access {
				t.Fatalf("code=%d err=%v stdout=%s stderr=%s loads=%d reads=%d", code, err, stdout, stderr, loads, reader.reads)
			}
		}
	}
	stdout, _, code := runCLIFull(t, nil, "broker", "discover", "--service", "jira", "-o", "text")
	if code != exitOK || !strings.Contains(stdout, "v1: unavailable") {
		t.Fatalf("text=%q code=%d", stdout, code)
	}
}
