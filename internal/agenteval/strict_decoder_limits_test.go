package agenteval

import (
	"strings"
	"testing"
)

func TestStrictDecodersRejectOversizedTrailingData(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		decode  func(string) error
		message string
	}{
		{
			name:  "run spec",
			limit: maxRunSpecBytes,
			decode: func(input string) error {
				_, err := DecodeRunSpec(strings.NewReader(input))
				return err
			},
			message: "run spec exceeds",
		},
		{
			name:  "cli command policy",
			limit: maxCLICommandPolicyBytes,
			decode: func(input string) error {
				_, err := DecodeCLICommandPolicy(strings.NewReader(input))
				return err
			},
			message: "cli command policy exceeds",
		},
		{
			name:  "synthetic run receipt",
			limit: maxSyntheticRunReceiptBytes,
			decode: func(input string) error {
				_, err := DecodeSyntheticRunReceipt(strings.NewReader(input))
				return err
			},
			message: "synthetic run receipt exceeds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, input := range []string{
				`{}` + strings.Repeat(" ", test.limit),
				`{}` + strings.Repeat(" ", test.limit-1) + `{}`,
			} {
				if err := test.decode(input); err == nil || !strings.Contains(err.Error(), test.message) {
					t.Fatalf("decode error = %v, want %q", err, test.message)
				}
			}
		})
	}
}
