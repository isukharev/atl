package brokertransport

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

func TestPublishedHTTPBrokerSchemaMatchesEmbeddedContract(t *testing.T) {
	published, err := os.ReadFile("../../docs/schemas/broker-http-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(published, SchemaV1()) || !validDigest(SchemaSHA256()) || StaticProtocolV1().TransportSchemaSHA256 != SchemaSHA256() {
		t.Fatal("published and embedded Broker HTTP schemas differ")
	}
}

func TestPublishedHTTPBrokerSchemaValidatesWireVectors(t *testing.T) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(SchemaV1(), &schema); err != nil {
		t.Fatal(err)
	}
	var semantic jsonschema.Schema
	if err := json.Unmarshal(brokercontract.SchemaV1(), &semantic); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		if strings.HasSuffix(uri.Path, "/broker-v1.schema.json") {
			return &semantic, nil
		}
		return nil, errors.New("unrecognized schema")
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	request, _ := NewAuthenticationRequest([]byte("synthetic-workload-credential"), AuthenticationChallenge{Nonce: testNonce('n'), Audience: "atl-broker", BrokerID: "broker-1"})
	response := AuthenticationResponse{SchemaVersion: 1, Nonce: request.Nonce, CredentialSHA256: request.CredentialSHA256, IssuerSHA256: strings.Repeat("a", 64), IssuedAtMillis: now.UnixMilli(), ExpiresAtMillis: now.Add(5 * time.Second).UnixMilli(), Context: testVerifiedContext(now)}
	failure, _ := NewFailure(domain.BrokerReasonDenied)
	vectors := [][]byte{
		mustTransport(EncodeAuthenticationRequestV1(request)),
		mustTransport(EncodeAuthenticationResponseV1(response)),
		mustTransport(EncodeFailureV1(failure)),
		mustTransport(EncodeProtocolV1(StaticProtocolV1())),
		mustTransport(EncodeAdminStatusV1(AdminStatus{SchemaVersion: 1, Kind: AdminKindReadiness, Status: AdminStatusReady, Complete: true})),
		mustTransport(EncodeAdminStatusV1(AdminStatus{SchemaVersion: 1, Kind: AdminKindHealth, Status: AdminStatusHealthy, Complete: true})),
		mustTransport(EncodeAdminStatusV1(AdminStatus{SchemaVersion: 1, Kind: AdminKindReadiness, Status: AdminStatusReady, Complete: true})),
	}
	for index, vector := range vectors {
		var value any
		if err := json.Unmarshal(vector, &value); err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(value); err != nil {
			t.Fatalf("vector %d failed schema: %v data=%s", index, err, vector)
		}
	}
}

func mustTransport(data []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return data
}
