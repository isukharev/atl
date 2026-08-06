package testbackend

import (
	"strings"
	"testing"
)

func TestDecodeStrictRejectsOversizedTrailingData(t *testing.T) {
	for _, input := range []string{
		`{}` + strings.Repeat(" ", maxContractBytes),
		`{}` + strings.Repeat(" ", maxContractBytes-1) + `{}`,
	} {
		var decoded map[string]any
		if err := decodeStrict(strings.NewReader(input), &decoded); err == nil || !strings.Contains(err.Error(), "contract exceeds") {
			t.Fatalf("decode error = %v, want contract size refusal", err)
		}
	}
}
