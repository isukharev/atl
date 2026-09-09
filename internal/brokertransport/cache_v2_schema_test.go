package brokertransport

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestPublishedCacheHTTPV2SchemaMatchesEmbeddedAndWire(t *testing.T) {
	published, err := os.ReadFile("../../docs/schemas/broker-cache-http-v2.schema.json")
	if err != nil || !bytes.Equal(published, CacheSchemaV2()) || len(CacheSchemaSHA256V2()) != 64 {
		t.Fatalf("schema parity error=%v", err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(CacheSchemaV2(), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	claim := cacheTransportClaim(now)
	verified := testVerifiedContext(now)
	verified.Backend.Service = "confluence"
	candidate, deadline, _ := BindCacheQualificationClaimV2(claim, verified, now, now, now.Add(5*time.Second))
	envelope, _ := NewCacheQualificationEnvelopeV2(claim, cacheTransportResolution(t, candidate, now), deadline.Add(-time.Second))
	failure, _ := EncodeCacheFailureV2("denied")
	for _, body := range [][]byte{mustTransport(EncodeCacheQualificationClaimV2(claim)), mustTransport(EncodeCacheQualificationEnvelopeV2(envelope)), failure} {
		var value any
		if err := json.Unmarshal(body, &value); err != nil || resolved.Validate(value) != nil {
			t.Fatalf("wire rejected: err=%v body=%s", err, body)
		}
	}
}
