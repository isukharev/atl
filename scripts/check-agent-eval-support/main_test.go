package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSupportPolicyBaselineAndClosedMutations(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	data, err := os.ReadFile(filepath.Join(root, "docs", "maintainers", "agent-eval-support.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var baseline policy
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	if err := validatePolicyJSONShape(data); err != nil {
		t.Fatalf("checked-in support policy JSON shape rejected: %v", err)
	}
	if err := validate(baseline); err != nil {
		t.Fatalf("checked-in support policy rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"omitted-required-member", func(value []byte) []byte {
			return bytes.Replace(value, []byte(",\n    \"named_consumer\": false\n"), []byte("\n"), 1)
		}},
		{"duplicate-member", func(value []byte) []byte {
			return bytes.Replace(value, []byte("    \"automatic_updates\": false\n"), []byte("    \"automatic_updates\": false,\n    \"automatic_updates\": false\n"), 1)
		}},
		{"null-required-bool", func(value []byte) []byte {
			return bytes.Replace(value, []byte("    \"named_consumer\": false\n"), []byte("    \"named_consumer\": null\n"), 1)
		}},
		{"null-optional-string", func(value []byte) []byte {
			return bytes.Replace(value, []byte("      \"architecture\": \"arm64\",\n"), []byte("      \"architecture\": null,\n"), 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := test.mutate(data)
			if err := validatePolicyJSONShape(mutated); err == nil {
				t.Fatalf("JSON shape accepted %s", test.name)
			}
		})
	}

	tests := []struct {
		name   string
		mutate func(*policy)
	}{
		{"cadence", func(value *policy) { value.Cadence.Stable = "stable" }},
		{"excluded-platform", func(value *policy) { value.ExcludedPlatforms[0].Reason = "signed_distribution_matrix_not_proven" }},
		{"component-route", func(value *policy) { value.Components[2].Route = "1388" }},
		{"schema-policy", func(value *policy) { value.Compatibility.SchemaPolicy = "accept_future" }},
		{"automatic-updates", func(value *policy) { value.Security.AutoUpdates = true }},
		{"release-prerequisite-order", func(value *policy) {
			value.Release.StablePrerequisites[0], value.Release.StablePrerequisites[1] = value.Release.StablePrerequisites[1], value.Release.StablePrerequisites[0]
		}},
		{"release-prerequisite-omission", func(value *policy) {
			value.Release.StablePrerequisites = value.Release.StablePrerequisites[:len(value.Release.StablePrerequisites)-1]
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := clonePolicy(t, baseline)
			test.mutate(&value)
			if err := validate(value); err == nil {
				t.Fatalf("validate accepted mutated support policy")
			}
		})
	}
}

func clonePolicy(t *testing.T, value policy) policy {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone policy
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
