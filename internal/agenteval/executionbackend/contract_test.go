package executionbackend

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExecutionBackendContractIsClosedCanonicalAndImmutable(t *testing.T) {
	contract, err := ReferenceContract()
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeContract(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Assurance != AssuranceHermeticReference || len(decoded.Capabilities) != len(Capabilities()) {
		t.Fatalf("decoded=%+v", decoded)
	}
	decoded.Capabilities[0].Support = SupportUnknown
	again, err := ReferenceContract()
	if err != nil || again.Capabilities[0].Support != SupportSupported {
		t.Fatalf("contract alias: %+v %v", again, err)
	}
	invalidIdentity := contract
	invalidIdentity.BackendID = "bad//id"
	if err := ValidateContract(invalidIdentity); err == nil {
		t.Fatal("accepted malformed namespaced backend ID")
	}
	invalidVersion := contract
	invalidVersion.BackendVersion = `1"quoted`
	if err := ValidateContract(invalidVersion); err == nil {
		t.Fatal("accepted malformed backend version")
	}
	if _, err := NewContract("example", "1", strings.Repeat("a", 64), strings.Repeat("b", 64), AssuranceLocalProcess,
		map[CapabilityID]Support{"unknown.capability": SupportSupported}); err == nil {
		t.Fatal("constructor ignored an unknown capability")
	}
	hermeticDrift := contract
	hermeticDrift.Capabilities = append([]Capability{}, contract.Capabilities...)
	hermeticDrift.Capabilities[16].Support = SupportSupported
	if err := ValidateContract(hermeticDrift); err == nil {
		t.Fatal("hermetic contract overclaimed unsupported memory enforcement")
	}
	hermeticIdentityDrift := contract
	hermeticIdentityDrift.ContentSHA256 = strings.Repeat("f", 64)
	if err := ValidateContract(hermeticIdentityDrift); err == nil {
		t.Fatal("hermetic contract accepted a foreign implementation identity")
	}

	for name, mutation := range map[string][]byte{
		"future":       bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"unknown":      bytes.Replace(data, []byte(`,"backend_id"`), []byte(`,"extra":true,"backend_id"`), 1),
		"duplicate":    bytes.Replace(data, []byte(`,"backend_id"`), []byte(`,"schema_version":1,"backend_id"`), 1),
		"trailing":     append(append([]byte{}, data...), []byte("{}\n")...),
		"noncanonical": append([]byte(" "), data...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeContract(bytes.NewReader(mutation)); err == nil {
				t.Fatal("accepted mutation")
			}
		})
	}

	_, plan := referenceFixture(t)
	planData, err := EncodePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	assertExecutionBackendClosedWire(t, "plan", planData, func(data []byte) error {
		_, err := DecodePlan(bytes.NewReader(data))
		return err
	})
	admitted, err := Admit(contract, plan)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunReference(context.Background(), admitted, ReferenceInputs{
		Fixture:     archiveFixture(t, tarEntry{name: "input.txt", data: []byte("fixture")}),
		Skill:       archiveFixture(t, tarEntry{name: "SKILL.md", data: []byte("skill")}),
		Definitions: archiveFixture(t, tarEntry{name: "task.json", data: []byte("definition")}),
	})
	if err != nil {
		t.Fatal(err)
	}
	receiptData, err := EncodeReceipt(plan, result.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	assertExecutionBackendClosedWire(t, "receipt", receiptData, func(data []byte) error {
		_, err := DecodeReceipt(bytes.NewReader(data), plan)
		return err
	})
}

func assertExecutionBackendClosedWire(t *testing.T, family string, data []byte, decode func([]byte) error) {
	t.Helper()
	for name, mutation := range map[string][]byte{
		"future":    bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"unknown":   bytes.Replace(data, []byte(`,"schema_version"`), []byte(`,"extra":true,"schema_version"`), 1),
		"duplicate": bytes.Replace(data, []byte(`,"schema_version"`), []byte(`,"schema":"duplicate","schema_version"`), 1),
		"trailing":  append(append([]byte{}, data...), []byte("{}\n")...),
	} {
		t.Run(family+"-"+name, func(t *testing.T) {
			if err := decode(mutation); err == nil {
				t.Fatal("accepted mutation")
			}
		})
	}
}

func TestExecutionBackendAdmissionFailsClosedBeforeEntry(t *testing.T) {
	contract, plan := referenceFixture(t)
	if _, err := Admit(contract, plan); err != nil {
		t.Fatal(err)
	}

	unsupported := plan
	unsupported.Resources.CPUTimeMillis = 1
	if _, err := Admit(contract, unsupported); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("cpu err=%v", err)
	}

	ambient := plan
	ambient.Network = NetworkPolicy{Mode: NetworkAmbient}
	if _, err := Admit(contract, ambient); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ambient err=%v", err)
	}

	credentials := plan
	credentials.Credentials = CredentialPolicy{Mode: CredentialsAmbient}
	if _, err := Admit(contract, credentials); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("credentials err=%v", err)
	}

	localSupport := map[CapabilityID]Support{}
	implementation := strings.Repeat("a", 64)
	local, err := LocalProcessContract(implementation, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range local.Capabilities {
		localSupport[claim.ID] = claim.Support
	}
	if local.Assurance != AssuranceLocalProcess || localSupport[CapabilityNetworkDeny] != SupportUnsupported || localSupport[CapabilityProcessTree] != SupportUnknown {
		t.Fatalf("local claims=%+v", local)
	}
}
