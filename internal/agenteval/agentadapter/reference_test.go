package agentadapter

import (
	"reflect"
	"strings"
	"testing"
)

func TestReferenceContractAndObservationAreClosedAndAttemptBound(t *testing.T) {
	contract, err := ReferenceContract()
	if err != nil {
		t.Fatalf("reference contract: %v", err)
	}
	without, err := NewReferenceObservation(contract, strings.Repeat("a", 64), false)
	if err != nil {
		t.Fatalf("reference observation without activation: %v", err)
	}
	with, err := NewReferenceObservation(contract, strings.Repeat("b", 64), true)
	if err != nil {
		t.Fatalf("reference observation with activation: %v", err)
	}
	if !without.Coverage || !with.Coverage || without.AttemptID == with.AttemptID ||
		without.Events[0].Start.Activation.Mode != ActivationUnavailable ||
		with.Events[0].Start.Activation.Mode != ActivationNative {
		t.Fatalf("unexpected observations: without=%+v with=%+v", without, with)
	}
	encoded, err := EncodeObservation(contract, with)
	if err != nil {
		t.Fatalf("encode observation: %v", err)
	}
	decoded, err := DecodeObservation(strings.NewReader(string(encoded)), contract)
	if err != nil || !reflect.DeepEqual(decoded, with) {
		t.Fatalf("round trip: decoded=%+v err=%v", decoded, err)
	}

	mutated := contract
	mutated.Capabilities = append([]Capability(nil), contract.Capabilities...)
	mutated.Capabilities[0].Support = SupportUnknown
	if err := ValidateContract(mutated); err == nil {
		t.Fatal("mutated fixed reference contract was accepted")
	}
	if _, err := NewReferenceObservation(contract, strings.Repeat("c", 63), true); err == nil {
		t.Fatal("invalid attempt identity was accepted")
	}
}
