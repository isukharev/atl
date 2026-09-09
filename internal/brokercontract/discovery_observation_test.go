package brokercontract

import (
	"bytes"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestDiscoveryV2AvailableObservationRoundTripsCanonicalEmptyFields(t *testing.T) {
	_, _, projection := discoveryV2Fixtures(t)
	projection.Operations = nil
	foundObservation := false
	for _, definition := range availableDefinitionsForService("jira") {
		foundObservation = foundObservation || definition.ID == domain.BrokerOperationOutcomeLookup
		projection.Operations = append(projection.Operations, domain.BrokerDiscoveryOperationV2{
			ID: definition.ID, Version: definition.Version, Supported: true, Access: domain.BrokerDiscoveryAccessAllowed,
			Features: wireStrings(definition.RequiredFeatures), Limits: definition.Limits, Effects: definition.Effects,
		})
	}
	if !foundObservation {
		t.Fatal("compiled observation operation is missing")
	}
	wire, err := EncodeDiscoveryProjectionV2(projection)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDiscoveryProjectionV2(wire)
	if err != nil {
		t.Fatalf("canonical zero-field observation did not round-trip: %v", err)
	}
	reencoded, err := EncodeDiscoveryProjectionV2(decoded)
	if err != nil || !bytes.Equal(wire, reencoded) {
		t.Fatal("discovery canonical bytes changed after decoding", err)
	}
	if !bytes.Contains(wire, []byte(`"fields":[]`)) {
		t.Fatal("observation does not contain its canonical empty field array")
	}
	invalid := bytes.Replace(wire, []byte(`"fields":[]`), []byte(`"fields":null`), 1)
	if _, err := DecodeDiscoveryProjectionV2(invalid); err == nil {
		t.Fatal("null observation fields bypassed the array contract")
	}
}

func TestGuardedCommentEnablementPreservesPriorRegistryDefinitionBytes(t *testing.T) {
	definitions := Registry()
	changed := 0
	for index, definition := range definitions {
		switch definition.ID {
		case domain.BrokerOperationJiraCommentPreview, domain.BrokerOperationJiraCommentApply, domain.BrokerOperationOutcomeLookup:
			if !definition.Available {
				t.Fatal("guarded runtime operation was not enabled")
			}
			definitions[index].Available = false
			changed++
		}
	}
	const prior = "c579297a8d9ac454aaf0fdbd17a83167480f70a53d3446e5aead3776849bd5b5"
	if changed != 3 || registrySHA256(definitions) != prior || RegistrySHA256() == prior {
		t.Fatalf("registry changed beyond its three availability flags: restored=%s enabled=%s", registrySHA256(definitions), RegistrySHA256())
	}
}
