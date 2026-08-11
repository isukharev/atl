package agentadapter

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestContractIsClosedCanonicalAndFutureRejecting(t *testing.T) {
	contract := testContract(t)
	data, err := EncodeContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeContract(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeContract(decoded)
	if err != nil || !bytes.Equal(data, encoded) {
		t.Fatalf("round trip changed: err=%v\n%s\n%s", err, data, encoded)
	}

	mutations := map[string][]byte{
		"future":     bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"uppercase":  bytes.Replace(data, []byte(strings.Repeat("a", 64)), []byte(strings.Repeat("A", 64)), 1),
		"duplicate":  bytes.Replace(data, []byte(`{"schema":`), []byte(`{"schema":"agent-eval/agent-adapter-contract","schema":`), 1),
		"unknown":    bytes.Replace(data, []byte(`,"adapter_id":`), []byte(`,"unknown":true,"adapter_id":`), 1),
		"trailing":   append(append([]byte(nil), data...), []byte("{}\n")...),
		"no newline": append([]byte(nil), data[:len(data)-1]...),
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeContract(bytes.NewReader(mutation)); !errors.Is(err, ErrContract) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestContractRequiresExactCapabilityClosure(t *testing.T) {
	contract := testContract(t)
	for name, mutate := range map[string]func(*Contract){
		"missing":       func(value *Contract) { value.Capabilities = value.Capabilities[:len(value.Capabilities)-1] },
		"duplicate":     func(value *Contract) { value.Capabilities[1] = value.Capabilities[0] },
		"unknown state": func(value *Contract) { value.Capabilities[0].Support = "maybe" },
		"unsorted keys": func(value *Contract) {
			value.ConfigurationKeys[0], value.ConfigurationKeys[1] = value.ConfigurationKeys[1], value.ConfigurationKeys[0]
		},
		"null keys": func(value *Contract) { value.ConfigurationKeys = nil },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := contract
			candidate.Capabilities = append([]Capability(nil), contract.Capabilities...)
			candidate.ConfigurationKeys = append([]ConfigurationKey(nil), contract.ConfigurationKeys...)
			mutate(&candidate)
			if ValidateContract(candidate) == nil {
				t.Fatal("invalid contract passed")
			}
		})
	}
	if len(Capabilities()) != 18 || !strings.HasPrefix(string(Capabilities()[0]), "activation.") {
		t.Fatalf("capability closure=%v", Capabilities())
	}
}

func testContract(t *testing.T) Contract {
	t.Helper()
	support := make(map[CapabilityID]Support, len(Capabilities()))
	for _, capability := range Capabilities() {
		support[capability] = SupportSupported
	}
	contract, err := NewContract("synthetic.agent", "v1.0.0", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), support,
		[]ConfigurationKey{{Name: "model", Sensitive: false}, {Name: "token", Sensitive: true}})
	if err != nil {
		t.Fatal(err)
	}
	return contract
}
