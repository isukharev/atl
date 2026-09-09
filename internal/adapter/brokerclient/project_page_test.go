package brokerclient

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func TestProjectPageClientRefusesInvalidArgumentsAndUnavailableRuntimeBeforeIO(t *testing.T) {
	loader := &countingSessionLoader{value: testSession()}
	client, err := New(Config{BaseURL: "https://127.0.0.1:1", BrokerID: "broker-1", Audience: "atl-broker", Session: loader})
	if err != nil {
		t.Fatal(err)
	}
	valid := domain.BrokerProjectPageArguments{ProjectKey: "EXAMPLE", Fields: []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldSummary}, MaxResults: 15}
	invalid := []domain.BrokerProjectPageArguments{
		{ProjectKey: "example", Fields: valid.Fields, MaxResults: 15},
		{ProjectKey: "EXAMPLE", Fields: []domain.BrokerProjectPageField{"status"}, MaxResults: 15},
		{ProjectKey: "EXAMPLE", Fields: []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldSummary, domain.BrokerProjectPageFieldSummary}, MaxResults: 15},
		{ProjectKey: "EXAMPLE", Fields: valid.Fields, StartAt: -1, MaxResults: 15},
		{ProjectKey: "EXAMPLE", Fields: valid.Fields, StartAt: domain.BrokerProjectPageMaxStartAt + 1, MaxResults: 15},
		{ProjectKey: "EXAMPLE", Fields: valid.Fields, MaxResults: 0},
		{ProjectKey: "EXAMPLE", Fields: valid.Fields, MaxResults: domain.BrokerProjectPageMaxResults + 1},
	}
	for _, arguments := range invalid {
		if _, err := client.ReadJiraProjectIssuePage(context.Background(), arguments); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("arguments=%+v err=%v", arguments, err)
		}
	}
	definition, ok := brokercontract.DefinitionV2(domain.BrokerOperationJiraProjectIssuePageRead, brokercontract.ProjectPageOperationVersion)
	if !ok {
		t.Fatal("missing project-page definition")
	}
	if !definition.Definition.Available {
		if _, err := client.ReadJiraProjectIssuePage(context.Background(), valid); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("unavailable err=%v", err)
		} else if reason, _ := brokercontract.Reason(err); reason != domain.BrokerReasonUnsupported {
			t.Fatalf("unavailable reason=%s err=%v", reason, err)
		}
	}
	if loader.calls.Load() != 0 {
		t.Fatalf("session loads=%d", loader.calls.Load())
	}
}

func TestProjectPageRequestBindsSelectedSessionAndCanonicalArguments(t *testing.T) {
	definition, ok := brokercontract.DefinitionV2(domain.BrokerOperationJiraProjectIssuePageRead, brokercontract.ProjectPageOperationVersion)
	if !ok {
		t.Fatal("missing project-page definition")
	}
	request := projectPageRequest(domain.BrokerProjectPageArguments{
		ProjectKey: "EXAMPLE", Fields: []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldSummary, domain.BrokerProjectPageFieldDescription},
		StartAt: 15, MaxResults: 15,
	}, definition.Definition, "request-1", testSession())
	wire, err := brokercontract.EncodeProjectPageRequestV2(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := brokercontract.DecodeProjectPageRequestV2(wire)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Expect != testSession().expectations() || decoded.RequestID != "request-1" ||
		len(decoded.Arguments.Fields) != 2 || decoded.Arguments.Fields[0] != domain.BrokerProjectPageFieldDescription || decoded.Arguments.Fields[1] != domain.BrokerProjectPageFieldSummary {
		t.Fatalf("decoded=%+v", decoded)
	}
}

func TestProjectPageRequiresExactAllowedDiscoveryRow(t *testing.T) {
	definition, ok := brokercontract.DefinitionV2(domain.BrokerOperationJiraProjectIssuePageRead, brokercontract.ProjectPageOperationVersion)
	if !ok {
		t.Fatal("missing project-page definition")
	}
	operation := domain.BrokerFamilyDiscoveryOperationV3{
		ID: definition.Definition.ID, Version: definition.Definition.Version, Supported: true,
		Access: domain.BrokerDiscoveryAccessAllowed, Features: append([]string{}, definition.Definition.RequiredFeatures...),
		Limits: definition.Definition.Limits, Effects: append([]domain.BrokerEffectDefinition{}, definition.Definition.Effects...),
	}
	projection := domain.BrokerFamilyDiscoveryProjectionV3{Operations: []domain.BrokerFamilyDiscoveryOperationV3{operation}}
	if err := requireAllowedProjectPage(projection, definition.Definition); err != nil {
		t.Fatalf("exact allowed row: %v", err)
	}
	projection.Operations[0].Access = domain.BrokerDiscoveryAccessRequestRequired
	projection.Operations[0].RequestAccessCorrelation = "access-request-1"
	if err := requireAllowedProjectPage(projection, definition.Definition); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("request-required err=%v", err)
	}
	projection.Operations[0].Access = domain.BrokerDiscoveryAccessUnavailable
	projection.Operations[0].RequestAccessCorrelation = ""
	if err := requireAllowedProjectPage(projection, definition.Definition); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("unavailable err=%v", err)
	}
	projection.Operations[0].Access = domain.BrokerDiscoveryAccessAllowed
	projection.Operations[0].Limits.MaxFields++
	if err := requireAllowedProjectPage(projection, definition.Definition); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("changed limit err=%v", err)
	}
}

func TestProjectPageFailureDecoderIsStrictAndContentFree(t *testing.T) {
	denied, err := brokertransport.EncodeDiscoveryFailureV2(domain.BrokerReasonDenied)
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []struct {
		name   string
		status int
		body   []byte
		id     string
	}{
		{name: "missing correlation", status: 200, body: []byte(`{}`)},
		{name: "invalid correlation", status: 200, body: []byte(`{}`), id: "bad correlation"},
		{name: "raw private failure", status: 503, body: []byte("private-response-canary"), id: "correlation-1"},
		{name: "strict denial", status: 403, body: denied, id: "correlation-1"},
	} {
		t.Run(response.name, func(t *testing.T) {
			_, err := acceptedProjectPageBody(httpx.BoundedResponse{Status: response.status, Body: response.body, CorrelationID: response.id})
			if response.name == "strict denial" {
				if reason, _ := brokercontract.Reason(err); reason != domain.BrokerReasonDenied {
					t.Fatalf("reason=%s err=%v", reason, err)
				}
				return
			}
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
			if err != nil && err.Error() == "private-response-canary" {
				t.Fatal("private response content escaped")
			}
		})
	}
}
