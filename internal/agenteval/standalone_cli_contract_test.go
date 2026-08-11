package agenteval

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestStandaloneProjectConfigV1IsClosedCanonicalAndFutureRejecting(t *testing.T) {
	repetitions := 3
	profile, model := "profile.synthetic", "model-synthetic"
	config := StandaloneProjectConfig{
		Schema: StandaloneProjectConfigSchema, SchemaVersion: StandaloneProjectConfigVersion,
		ContractVersion: StandaloneContractVersion, Profile: &profile, Model: &model,
		Repetitions: &repetitions,
	}
	encoded, err := EncodeStandaloneProjectConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStandaloneProjectConfig(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := EncodeStandaloneProjectConfig(decoded)
	if err != nil || !bytes.Equal(reencoded, encoded) {
		t.Fatalf("project config round trip changed bytes: %v", err)
	}
	for _, mutation := range [][]byte{
		bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":1,"unknown":true`), 1),
		bytes.Replace(encoded, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		bytes.Replace(encoded, []byte(`"profile":"profile.synthetic"`), []byte(`"profile":null`), 1),
		bytes.Replace(encoded, []byte(`"profile":"profile.synthetic"`), []byte(`"profile":"   "`), 1),
		bytes.Replace(encoded, []byte(`"profile":"profile.synthetic"`), []byte(`"profile":"`+strings.Repeat("x", StandaloneProjectConfigIdentifierMaxBytes+1)+`"`), 1),
		append(bytes.Clone(encoded), []byte(`{}`)...),
	} {
		if _, err := DecodeStandaloneProjectConfig(bytes.NewReader(mutation)); !errors.Is(err, ErrStandaloneProjectConfig) {
			t.Fatalf("project config mutation passed or changed error class: %v", err)
		}
	}
}
