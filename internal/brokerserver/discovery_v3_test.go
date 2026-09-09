package brokerserver

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/brokertransport"
	"github.com/isukharev/atl/internal/domain"
)

func familyDiscoveryServerHello(fixture projectPageServerFixture) brokertransport.DiscoveryNegotiationV3 {
	verified := fixture.base.authenticator.authentication.Context
	return brokertransport.DiscoveryNegotiationV3{
		SchemaVersion: domain.BrokerDiscoverySchemaVersionV3, RequestID: "family-discovery-request-1",
		ContractFamily: domain.BrokerContractFamilyExecutionV2, Service: "jira", BrokerID: verified.BrokerID,
		Audience: verified.Audience, ExecutionID: verified.ExecutionID, ExecutionEpoch: verified.ExecutionEpoch,
		AuthorityRevision: verified.AuthorityRevision, NotAfterMillis: fixture.base.baseTime.Add(5 * time.Second).UnixMilli(),
	}
}

type bufferedBrokerHTTPResponse struct {
	StatusCode int
	Header     http.Header
}

func familyDiscoveryTLSRequest(t *testing.T, handler http.Handler, path string, body []byte) (bufferedBrokerHTTPResponse, []byte) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-workload-credential")
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	wire, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return bufferedBrokerHTTPResponse{StatusCode: response.StatusCode, Header: response.Header.Clone()}, wire
}

func TestFamilyDiscoveryV3NegotiatesAndProjectsAvailableDefinitionsWithoutJiraIO(t *testing.T) {
	fixture := newProjectPageServerFixture(t, "")
	hello := familyDiscoveryServerHello(fixture)
	helloWire, err := brokertransport.EncodeDiscoveryNegotiationV3(hello)
	if err != nil {
		t.Fatal(err)
	}
	response, wire := familyDiscoveryTLSRequest(t, fixture.base.handler, brokertransport.DiscoveryNegotiatePathV3, helloWire)
	negotiated, err := brokertransport.DecodeNegotiatedDiscoveryV3(wire, hello, time.Now())
	if response.StatusCode != http.StatusOK || err != nil || negotiated.ContractFamily != domain.BrokerContractFamilyExecutionV2 {
		t.Fatalf("status=%d request=%+v err=%v body=%s", response.StatusCode, negotiated, err, wire)
	}
	requestWire, err := brokercontract.EncodeFamilyDiscoveryRequestV3(negotiated)
	if err != nil {
		t.Fatal(err)
	}
	response, wire = familyDiscoveryTLSRequest(t, fixture.base.handler, brokertransport.DiscoveryPathV3, requestWire)
	projection, err := brokercontract.DecodeFamilyDiscoveryProjectionV3(wire)
	if response.StatusCode != http.StatusOK || err != nil || brokercontract.ValidateFamilyDiscoveryProjectionV3ForRequest(projection, negotiated, time.Now()) != nil {
		t.Fatalf("status=%d projection=%+v err=%v body=%s", response.StatusCode, projection, err, wire)
	}
	wantDefinitions := brokercontract.AvailableDefinitionsV2()
	if len(projection.Operations) != len(wantDefinitions) {
		t.Fatalf("operations=%+v definitions=%+v", projection.Operations, wantDefinitions)
	}
	for index, wrapped := range wantDefinitions {
		definition := wrapped.Definition
		want := domain.BrokerFamilyDiscoveryOperationV3{
			ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed,
			Features: definition.RequiredFeatures, Limits: definition.Limits, Effects: definition.Effects,
		}
		if !reflect.DeepEqual(projection.Operations[index], want) {
			t.Fatalf("operation[%d]=%+v want=%+v", index, projection.Operations[index], want)
		}
	}
	registry := brokercontract.RegistryV2()
	if len(registry) != 1 || !registry[0].Definition.Available || len(wantDefinitions) != 1 || len(projection.Operations) != 1 {
		t.Fatalf("integrated operation not advertised exactly: registry=%+v available=%+v projection=%+v", registry, wantDefinitions, projection.Operations)
	}
	projectCalls, identityCalls, businessCalls := fixture.reader.calls()
	if fixture.base.authenticator.calls != 2 || fixture.authorizer.discoveryCalls != 1 || len(fixture.authorizer.phaseSnapshot()) != 0 || fixture.base.authorizer.discoveryCalls != 0 || fixture.base.authorizer.admissionCalls != 0 || fixture.base.backendCalls.Load() != 0 || projectCalls+identityCalls+businessCalls != 0 {
		t.Fatalf("auth=%d discovery=%d phases=%v v2_discovery=%d v1_admission=%d v1_backend=%d reads=(%d,%d,%d)", fixture.base.authenticator.calls, fixture.authorizer.discoveryCalls, fixture.authorizer.phaseSnapshot(), fixture.base.authorizer.discoveryCalls, fixture.base.authorizer.admissionCalls, fixture.base.backendCalls.Load(), projectCalls, identityCalls, businessCalls)
	}
}

func TestFamilyDiscoveryV3FailsClosedWithoutFallback(t *testing.T) {
	for _, test := range []struct {
		name       string
		configure  func(*projectPageServerFixture, *domain.BrokerFamilyDiscoveryRequestV3)
		wantReason domain.BrokerReason
		wantCalls  int
	}{
		{name: "expired", wantReason: domain.BrokerReasonDecisionExpired, configure: func(f *projectPageServerFixture, request *domain.BrokerFamilyDiscoveryRequestV3) {
			request.NotAfterMillis = f.base.baseTime.UnixMilli()
		}},
		{name: "missing project service", wantReason: domain.BrokerReasonUnsupported, configure: func(f *projectPageServerFixture, _ *domain.BrokerFamilyDiscoveryRequestV3) {
			f.base.handler.projectPages = nil
		}},
		{name: "inconsistent projection", wantReason: domain.BrokerReasonStaleAuthority, wantCalls: 1, configure: func(f *projectPageServerFixture, _ *domain.BrokerFamilyDiscoveryRequestV3) {
			f.authorizer.mutate = func(projection *domain.BrokerFamilyDiscoveryProjectionV3) {
				projection.AuthorityRevision = "other-revision"
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectPageServerFixture(t, "")
			hello := familyDiscoveryServerHello(fixture)
			request, err := brokertransport.BindDiscoveryNegotiationV3(hello, fixture.base.authenticator.authentication.Context, fixture.base.baseTime)
			if err != nil {
				t.Fatal(err)
			}
			test.configure(&fixture, &request)
			wire, err := brokercontract.EncodeFamilyDiscoveryRequestV3(request)
			if err != nil {
				t.Fatal(err)
			}
			response, body := familyDiscoveryTLSRequest(t, fixture.base.handler, brokertransport.DiscoveryPathV3, wire)
			failure, decodeErr := brokertransport.DecodeDiscoveryFailureV3(body)
			projectCalls, identityCalls, businessCalls := fixture.reader.calls()
			if response.StatusCode == http.StatusOK || decodeErr != nil || failure.Reason != test.wantReason || fixture.base.authenticator.calls != 1 || fixture.authorizer.discoveryCalls != test.wantCalls || fixture.base.authorizer.discoveryCalls != 0 || fixture.base.authorizer.admissionCalls != 0 || fixture.base.backendCalls.Load() != 0 || projectCalls+identityCalls+businessCalls != 0 {
				t.Fatalf("status=%d failure=%+v err=%v auth=%d discovery=%d v2=%d v1=%d v1_backend=%d reads=(%d,%d,%d) body=%s", response.StatusCode, failure, decodeErr, fixture.base.authenticator.calls, fixture.authorizer.discoveryCalls, fixture.base.authorizer.discoveryCalls, fixture.base.authorizer.admissionCalls, fixture.base.backendCalls.Load(), projectCalls, identityCalls, businessCalls, body)
			}
		})
	}
}

func TestFamilyDiscoveryV3RejectsMalformedAndSiblingFieldsBeforeAuthentication(t *testing.T) {
	fixture := newProjectPageServerFixture(t, "")
	valid, err := brokertransport.EncodeDiscoveryNegotiationV3(familyDiscoveryServerHello(fixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"schema_version":3,"schema_version":3}`),
		bytes.Replace(valid, []byte(`"service":"jira"`), []byte(`"service":"jira","policy":"injected"`), 1),
		bytes.Replace(valid, []byte(domain.BrokerContractFamilyExecutionV2), []byte("atl.broker.execution.v1"), 1),
		bytes.Repeat([]byte("x"), int(brokertransport.MaxDiscoveryNegotiationBytesV3)+1),
	} {
		fixture := newProjectPageServerFixture(t, "")
		response, wire := familyDiscoveryTLSRequest(t, fixture.base.handler, brokertransport.DiscoveryNegotiatePathV3, body)
		failure, err := brokertransport.DecodeDiscoveryFailureV3(wire)
		if response.StatusCode != http.StatusBadRequest || err != nil || failure.Reason != domain.BrokerReasonMalformed || fixture.base.authenticator.calls != 0 || fixture.authorizer.discoveryCalls != 0 || fixture.base.authorizer.discoveryCalls != 0 {
			t.Fatalf("status=%d failure=%+v err=%v auth=%d discovery=%d v2=%d", response.StatusCode, failure, err, fixture.base.authenticator.calls, fixture.authorizer.discoveryCalls, fixture.base.authorizer.discoveryCalls)
		}
	}
}
