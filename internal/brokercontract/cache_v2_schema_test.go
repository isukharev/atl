package brokercontract

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
)

func TestPublishedCacheV2SchemaMatchesEmbeddedAndWire(t *testing.T) {
	published, err := os.ReadFile("../../docs/schemas/broker-cache-v2.schema.json")
	if err != nil || !bytes.Equal(published, CacheSchemaV2()) || len(CacheSchemaSHA256V2()) != 64 {
		t.Fatalf("schema parity error=%v", err)
	}
	var schema, semantic jsonschema.Schema
	if json.Unmarshal(CacheSchemaV2(), &schema) != nil || json.Unmarshal(SchemaV1(), &semantic) != nil {
		t.Fatal("decode schemas")
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
		if strings.HasSuffix(uri.Path, "/broker-v1.schema.json") {
			return &semantic, nil
		}
		return nil, errors.New("unknown schema")
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	candidate := cacheTestCandidate(now)
	for _, body := range [][]byte{mustEncode(EncodeCacheQualificationCandidateV2(candidate)), mustEncode(EncodeResolvedCacheQualificationV2(cacheTestResolution(t, candidate, now)))} {
		var value any
		if err := json.Unmarshal(body, &value); err != nil || resolved.Validate(value) != nil {
			t.Fatalf("wire rejected: err=%v body=%s", err, body)
		}
	}
}
