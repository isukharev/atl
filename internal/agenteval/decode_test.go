package agenteval

import (
	"encoding/json"
	"strings"
	"testing"
)

type decodeBoundaryContract struct {
	Value string `json:"value"`
}

func TestDecodeStrictEnforcesTheCompleteByteBoundary(t *testing.T) {
	stringContract := func(size int) string {
		t.Helper()
		const prefix = `{"value":"`
		const suffix = `"}`
		if size < len(prefix)+len(suffix) {
			t.Fatalf("contract size %d is too small", size)
		}
		return prefix + strings.Repeat("a", size-len(prefix)-len(suffix)) + suffix
	}
	const short = `{"value":"ok"}`
	tests := []struct {
		name      string
		contract  string
		wantError string
	}{
		{name: "primary exactly at limit", contract: stringContract(maxContractBytes)},
		{name: "primary exceeds limit", contract: stringContract(maxContractBytes + 1), wantError: "contract exceeds"},
		{name: "trailing whitespace exactly at limit", contract: short + strings.Repeat(" ", maxContractBytes-len(short))},
		{name: "trailing whitespace exceeds limit", contract: short + strings.Repeat(" ", maxContractBytes-len(short)+1), wantError: "contract exceeds"},
		{name: "multiple values within limit", contract: short + ` {}`, wantError: "multiple JSON values"},
		{name: "second value hidden beyond limit", contract: short + strings.Repeat(" ", maxContractBytes+1-len(short)) + `{}`, wantError: "contract exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value decodeBoundaryContract
			err := decodeStrict(strings.NewReader(test.contract), &value)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("bounded contract rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error=%v want substring %q", err, test.wantError)
			}
		})
	}
}

func TestDecodeScenarioRejectsOversizedTrailingWhitespace(t *testing.T) {
	encoded, err := json.Marshal(validScenario())
	if err != nil {
		t.Fatal(err)
	}
	contract := string(encoded) + strings.Repeat(" ", maxContractBytes-len(encoded)+1)
	if _, err := DecodeScenario(strings.NewReader(contract)); err == nil || !strings.Contains(err.Error(), "contract exceeds") {
		t.Fatalf("oversized scenario error=%v", err)
	}
}
