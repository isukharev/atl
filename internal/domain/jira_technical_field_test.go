package domain

import (
	"strings"
	"testing"
)

func TestValidJiraTechnicalFieldID(t *testing.T) {
	tests := []struct {
		identifier string
		want       bool
	}{
		{"reporter", true},
		{"fixVersions", true},
		{"customfield_1", true},
		{"customfield_00042", true},
		{"Reporter", false},
		{"customfield_", false},
		{"customfield_name", false},
		{"plugin.vendor", false},
		{"Display Name", false},
		{" customfield_1", false},
		{"customfield_" + strings.Repeat("1", JiraTechnicalFieldMaxIDBytes-len("customfield_")), true},
		{"customfield_" + strings.Repeat("1", JiraTechnicalFieldMaxIDBytes), false},
	}
	for _, test := range tests {
		if got := ValidJiraTechnicalFieldID(test.identifier); got != test.want {
			t.Errorf("ValidJiraTechnicalFieldID(%q)=%t want=%t", test.identifier, got, test.want)
		}
	}
}
