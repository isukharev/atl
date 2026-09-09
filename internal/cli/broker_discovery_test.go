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
	access      domain.BrokerDiscoveryAccess
	reads       int
	familyReads int
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

func (r *brokerDiscoveryStub) DiscoverFamily(_ context.Context, service, family string) (domain.BrokerFamilyDiscoveryProjectionV3, error) {
	r.familyReads++
	request := domain.BrokerFamilyDiscoveryRequestV3{
		SchemaVersion: 3, RequestID: "request-family-1", ContractFamily: family, Service: service,
		BrokerID: "broker-1", Audience: "audience-1", ContextSHA256: strings.Repeat("b", 64), NotAfterMillis: 6000,
		Expect: domain.BrokerRequestExpectations{ExecutionID: "execution-1", ExecutionEpoch: "epoch-1", AuthorityRevision: "revision-1"},
	}
	digest, _ := brokercontract.FamilyDiscoveryRequestSHA256V3(request)
	operations := make([]domain.BrokerFamilyDiscoveryOperationV3, 0, len(brokercontract.AvailableDefinitionsV2()))
	for _, wrapped := range brokercontract.AvailableDefinitionsV2() {
		definition := wrapped.Definition
		operation := domain.BrokerFamilyDiscoveryOperationV3{
			ID: definition.ID, Version: definition.Version, Supported: true, Access: r.access,
			Features: definition.RequiredFeatures, Limits: definition.Limits, Effects: definition.Effects,
		}
		if r.access == domain.BrokerDiscoveryAccessRequestRequired {
			operation.RequestAccessCorrelation = "request-access-1"
		}
		operations = append(operations, operation)
	}
	projection := domain.BrokerFamilyDiscoveryProjectionV3{
		SchemaVersion: 3, RequestID: request.RequestID, RequestSHA256: digest, ContextSHA256: request.ContextSHA256,
		ExecutionID: request.Expect.ExecutionID, ExecutionEpoch: request.Expect.ExecutionEpoch,
		Audience: request.Audience, BrokerID: request.BrokerID, AuthorityRevision: request.Expect.AuthorityRevision,
		ContractFamily: family, Service: service, RegistrySHA256: brokercontract.RegistrySHA256V2(),
		ContractSchemaSHA256: brokercontract.ExecutionSchemaSHA256V2(), DiscoverySchemaSHA256: brokercontract.DiscoverySchemaSHA256V3(),
		IssuedAtMillis: 2000, ExpiresAtMillis: 6000, Operations: operations, Complete: true,
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
	for _, args := range [][]string{
		{"broker", "discover"},
		{"broker", "discover", "--service", "offline"},
		{"broker", "discover", "--service", "jira", "extra"},
		{"broker", "discover", "--service", "jira", "--family", "atl.broker.execution.v1"},
		{"broker", "discover", "--service", "confluence", "--family", domain.BrokerContractFamilyExecutionV2},
	} {
		stdout, _, err := executeCLIRaw(t, nil, args...)
		if stdout != "" || !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("args=%v stdout=%q err=%v", args, stdout, err)
		}
	}
}

func TestBrokerDiscoverReadsExplicitExecutionV2FamilyWithoutChangingDefault(t *testing.T) {
	previous := loadBrokerDiscovery
	t.Cleanup(func() { loadBrokerDiscovery = previous })
	reader := &brokerDiscoveryStub{access: domain.BrokerDiscoveryAccessAllowed}
	loads := 0
	loadBrokerDiscovery = func(service, _ string) (domain.BrokerDiscoveryReader, error) {
		loads++
		if service != domain.ServerProductJira {
			t.Fatalf("service=%q", service)
		}
		return reader, nil
	}
	stdout, stderr, code := runCLIFull(t, nil, "broker", "discover", "--service", "jira", "--family", domain.BrokerContractFamilyExecutionV2)
	projection, err := brokercontract.DecodeFamilyDiscoveryProjectionV3([]byte(stdout))
	if code != exitOK || stderr != "" || err != nil || loads != 1 || reader.familyReads != 1 || reader.reads != 0 ||
		projection.ContractFamily != domain.BrokerContractFamilyExecutionV2 || projection.Service != "jira" {
		t.Fatalf("code=%d err=%v stdout=%s stderr=%s loads=%d family=%d legacy=%d", code, err, stdout, stderr, loads, reader.familyReads, reader.reads)
	}
	stdout, _, code = runCLIFull(t, nil, "broker", "discover", "--service", "jira")
	if _, err := brokercontract.DecodeDiscoveryProjectionV2([]byte(stdout)); code != exitOK || err != nil || reader.reads != 1 || reader.familyReads != 1 {
		t.Fatalf("default v2 changed: code=%d err=%v stdout=%s legacy=%d family=%d", code, err, stdout, reader.reads, reader.familyReads)
	}
}

func TestBrokerDiscoverFamilyRequiresConcreteV3Reader(t *testing.T) {
	previous := loadBrokerDiscovery
	t.Cleanup(func() { loadBrokerDiscovery = previous })
	loadBrokerDiscovery = func(string, string) (domain.BrokerDiscoveryReader, error) {
		return discoveryReaderFuncForCLI(func(context.Context, string) (domain.BrokerDiscoveryProjectionV2, error) {
			return domain.BrokerDiscoveryProjectionV2{}, nil
		}), nil
	}
	stdout, _, err := executeCLIRaw(t, nil, "broker", "discover", "--service", "jira", "--family", domain.BrokerContractFamilyExecutionV2)
	reason, _ := brokercontract.Reason(err)
	if stdout != "" || reason != domain.BrokerReasonUnsupported {
		t.Fatalf("stdout=%q reason=%s err=%v", stdout, reason, err)
	}
}

type discoveryReaderFuncForCLI func(context.Context, string) (domain.BrokerDiscoveryProjectionV2, error)

func (function discoveryReaderFuncForCLI) Discover(ctx context.Context, service string) (domain.BrokerDiscoveryProjectionV2, error) {
	return function(ctx, service)
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
