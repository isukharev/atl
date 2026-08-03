package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckCoverageBoundary(t *testing.T) {
	for _, test := range []struct {
		name    string
		profile string
		wantErr string
	}{
		{name: "exact floor with zero-statement record", profile: profile(84, 16) + "example.go:5.1,5.1 0 1\n"},
		{name: "below floor", profile: profile(839, 161), wantErr: "below the reviewed 84.0% floor"},
		{name: "malformed", profile: "mode: atomic\nnot-a-record\n", wantErr: "malformed coverage profile"},
		{name: "malformed position", profile: "mode: atomic\nexample.go:line.column,2.1 1 1\n", wantErr: "malformed coverage profile"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cover.out")
			if err := os.WriteFile(path, []byte(test.profile), 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err := checkCoverage(path, "84.0", &output)
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(output.String(), "84.00%") {
					t.Fatalf("output=%q, want exact coverage", output.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error=%v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestCheckCoverageMissingProfile(t *testing.T) {
	err := checkCoverage(filepath.Join(t.TempDir(), "missing.out"), "84.0", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "read coverage profile") {
		t.Fatalf("error=%v, want missing-profile diagnostic", err)
	}
}

func TestReadProfileMergesDuplicatePackageBlocks(t *testing.T) {
	input := strings.NewReader("mode: atomic\n" +
		"example.go:1.1,2.1 4 0\n" +
		"example.go:1.1,2.1 4 1\n" +
		"example.go:3.1,4.1 1 0\n")
	covered, total, err := readProfile(input)
	if err != nil {
		t.Fatal(err)
	}
	if covered != 4 || total != 5 {
		t.Fatalf("coverage=%d/%d, want merged 4/5", covered, total)
	}
}

func TestReadProfileRejectsDuplicateStatementMismatch(t *testing.T) {
	input := strings.NewReader("mode: atomic\n" +
		"example.go:1.1,2.1 4 0\n" +
		"example.go:1.1,2.1 5 1\n")
	_, _, err := readProfile(input)
	if err == nil || !strings.Contains(err.Error(), "different statement count") {
		t.Fatalf("error=%v, want duplicate statement-count diagnostic", err)
	}
}

func TestParseTenthsPercent(t *testing.T) {
	for _, invalid := range []string{"84", "84.00", "84.x", "100.1", "-1.0"} {
		if _, err := parseTenthsPercent(invalid); err == nil {
			t.Errorf("parseTenthsPercent(%q) passed", invalid)
		}
	}
	if got, err := parseTenthsPercent("84.0"); err != nil || got != 840 {
		t.Fatalf("parseTenthsPercent(84.0)=%d,%v want 840,nil", got, err)
	}
}

func profile(covered, missed uint64) string {
	return "mode: atomic\n" +
		"example.go:1.1,2.1 " + decimal(covered) + " 1\n" +
		"example.go:3.1,4.1 " + decimal(missed) + " 0\n"
}

func decimal(value uint64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	position := len(buffer)
	for value != 0 {
		position--
		buffer[position] = digits[value%10]
		value /= 10
	}
	return string(buffer[position:])
}
