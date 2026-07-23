package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAggregateSyntheticOutputRootCompleteStableInventory(t *testing.T) {
	requireSyntheticRootPermissionChecks(t)
	firstRoot := newSyntheticOutputRoot(t)
	result := syntheticRootTestResult(t)
	writeSyntheticRootResult(t, firstRoot, 2, result)
	writeSyntheticRootResult(t, firstRoot, 1, result)
	writePrivateTestFile(t, filepath.Join(firstRoot, result.ScenarioID, result.Runtime.Provider, result.Variant, "run-01", "provider.log"), []byte("ignored artifact\n"))

	first, err := AggregateSyntheticOutputRoot(firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	if first.SchemaVersion != SyntheticRootAggregateSchemaVersion || len(first.SourceSHA256) != 64 ||
		first.Results != 2 || first.Cohorts != 1 || first.Aggregate.SchemaVersion != AggregateSchemaVersion ||
		len(first.Aggregate.Groups) != 1 || first.Aggregate.Groups[0].Runs != 2 || first.Aggregate.Groups[0].Passes != 2 {
		t.Fatalf("aggregate=%+v", first)
	}

	secondRoot := newSyntheticOutputRoot(t)
	writeSyntheticRootResult(t, secondRoot, 1, result)
	writeSyntheticRootResult(t, secondRoot, 2, result)
	if second, err := AggregateSyntheticOutputRoot(secondRoot); err != nil || second.SourceSHA256 != first.SourceSHA256 {
		t.Fatalf("second=%+v err=%v", second, err)
	}

	changedRoot := newSyntheticOutputRoot(t)
	changed := result
	changed.Metrics.OutputBytes++
	writeSyntheticRootResult(t, changedRoot, 1, changed)
	writeSyntheticRootResult(t, changedRoot, 2, result)
	if third, err := AggregateSyntheticOutputRoot(changedRoot); err != nil || third.SourceSHA256 == first.SourceSHA256 {
		t.Fatalf("third=%+v err=%v", third, err)
	}
}

func TestAggregateSyntheticOutputRootRejectsUnsafeOrIncompleteRoots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission and symlink guarantees are Unix-specific")
	}
	result := syntheticRootTestResult(t)
	tests := []struct {
		name   string
		mutate func(*testing.T, string, Result)
		code   string
	}{
		{
			name: "run gap",
			mutate: func(t *testing.T, root string, _ Result) {
				t.Helper()
				base := filepath.Join(root, result.ScenarioID, result.Runtime.Provider, result.Variant)
				if err := os.Rename(filepath.Join(base, "run-01"), filepath.Join(base, "run-02")); err != nil {
					t.Fatal(err)
				}
			},
			code: "incomplete_cohort",
		},
		{
			name: "missing result",
			mutate: func(t *testing.T, root string, _ Result) {
				t.Helper()
				resultPath := syntheticRootResultPath(root, result, 1)
				if err := os.Rename(resultPath, filepath.Join(filepath.Dir(resultPath), "provider.json")); err != nil {
					t.Fatal(err)
				}
			},
			code: "incomplete_cohort",
		},
		{
			name: "identity mismatch",
			mutate: func(t *testing.T, root string, value Result) {
				original := syntheticRootResultPath(root, value, 1)
				value.ScenarioID = "jira.other-evidence"
				writeSyntheticRootResultAt(t, original, value)
			},
			code: "identity_mismatch",
		},
		{
			name: "private data",
			mutate: func(t *testing.T, root string, value Result) {
				value.DataClass = "private-local"
				value.Runtime.PromptContractSHA256 = ""
				writeSyntheticRootResult(t, root, 1, value)
			},
			code: "private_data",
		},
		{
			name: "non-public task class",
			mutate: func(t *testing.T, root string, value Result) {
				value.TaskClass = "private/evidence"
				writeSyntheticRootResult(t, root, 1, value)
			},
			code: "non_public_task_class",
		},
		{
			name: "symlink",
			mutate: func(t *testing.T, root string, _ Result) {
				t.Helper()
				if err := os.Symlink("result.json", filepath.Join(filepath.Dir(syntheticRootResultPath(root, result, 1)), "link")); err != nil {
					t.Fatal(err)
				}
			},
			code: "unsafe_entry",
		},
		{
			name: "invalid marker",
			mutate: func(t *testing.T, root string, _ Result) {
				writePrivateTestFile(t, filepath.Join(root, privateOutputRootMarker), []byte("wrong\n"))
			},
			code: "invalid_marker",
		},
		{
			name: "loose root permissions",
			mutate: func(t *testing.T, root string, _ Result) {
				t.Helper()
				if err := os.Chmod(root, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			code: "unsafe_permissions",
		},
		{
			name: "loose result permissions",
			mutate: func(t *testing.T, root string, _ Result) {
				t.Helper()
				if err := os.Chmod(syntheticRootResultPath(root, result, 1), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			code: "unsafe_permissions",
		},
		{
			name: "scratch residue",
			mutate: func(t *testing.T, root string, _ Result) {
				writePrivateTestFile(t, filepath.Join(root, ".ephemeral", "residue"), []byte("x"))
			},
			code: "scratch_residue",
		},
		{
			name: "missing prompt identity",
			mutate: func(t *testing.T, root string, value Result) {
				value.Runtime.PromptContractSHA256 = ""
				writeSyntheticRootResult(t, root, 1, value)
			},
			code: "invalid_result",
		},
		{
			name: "malformed result",
			mutate: func(t *testing.T, root string, _ Result) {
				writePrivateTestFile(t, syntheticRootResultPath(root, result, 1), []byte("{"))
			},
			code: "invalid_result",
		},
		{
			name: "mixed runtime",
			mutate: func(t *testing.T, root string, value Result) {
				value.Runtime.Model = "other-model"
				writeSyntheticRootResult(t, root, 2, value)
			},
			code: "mixed_contract",
		},
		{
			name: "mixed prompt contract",
			mutate: func(t *testing.T, root string, value Result) {
				value.Runtime.PromptContractSHA256 = strings.Repeat("b", 64)
				writeSyntheticRootResult(t, root, 2, value)
			},
			code: "mixed_contract",
		},
		{
			name: "empty extra cohort",
			mutate: func(t *testing.T, root string, _ Result) {
				t.Helper()
				directory := filepath.Join(root, "jira.unfinished", "codex", "baseline")
				if err := os.MkdirAll(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			code: "incomplete_cohort",
		},
		{
			name: "extra top-level file",
			mutate: func(t *testing.T, root string, _ Result) {
				writePrivateTestFile(t, filepath.Join(root, "aggregate.json"), []byte("{}\n"))
			},
			code: "invalid_layout",
		},
		{
			name: "result at wrong depth",
			mutate: func(t *testing.T, root string, _ Result) {
				t.Helper()
				original := syntheticRootResultPath(root, result, 1)
				nested := filepath.Join(filepath.Dir(original), "nested")
				if err := os.Mkdir(nested, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(original, filepath.Join(nested, "result.json")); err != nil {
					t.Fatal(err)
				}
			},
			code: "invalid_layout",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newSyntheticOutputRoot(t)
			writeSyntheticRootResult(t, root, 1, result)
			test.mutate(t, root, result)
			_, err := AggregateSyntheticOutputRoot(root)
			assertClosedSyntheticRootError(t, err, test.code, root, result.ScenarioID)
		})
	}
}

func TestAggregateSyntheticOutputRootRejectsEmptyAndChangedInventory(t *testing.T) {
	requireSyntheticRootPermissionChecks(t)
	empty := newSyntheticOutputRoot(t)
	_, err := AggregateSyntheticOutputRoot(empty)
	assertClosedSyntheticRootError(t, err, "no_results", empty, "jira.private-fixture")

	root := newSyntheticOutputRoot(t)
	result := syntheticRootTestResult(t)
	writeSyntheticRootResult(t, root, 1, result)
	_, err = aggregateSyntheticOutputRootWithHooks(root, func() {
		writePrivateTestFile(t, filepath.Join(root, result.ScenarioID, result.Runtime.Provider, result.Variant, "run-01", "late.log"), []byte("late\n"))
	}, nil)
	assertClosedSyntheticRootError(t, err, "changed_during_read", root, result.ScenarioID)
}

func TestAggregateSyntheticOutputRootRereadsExactResultBytes(t *testing.T) {
	requireSyntheticRootPermissionChecks(t)
	root := newSyntheticOutputRoot(t)
	result := syntheticRootTestResult(t)
	writeSyntheticRootResult(t, root, 1, result)
	resultPath := syntheticRootResultPath(root, result, 1)
	info, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = aggregateSyntheticOutputRootWithHooks(root, nil, func() {
		data, readErr := os.ReadFile(resultPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		changed := bytes.Replace(data, []byte(`"model": "gpt-test"`), []byte(`"model": "alt-test"`), 1)
		if bytes.Equal(changed, data) || len(changed) != len(data) {
			t.Fatal("test mutation did not preserve byte length")
		}
		if writeErr := os.WriteFile(resultPath, changed, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if timeErr := os.Chtimes(resultPath, info.ModTime(), info.ModTime()); timeErr != nil {
			t.Fatal(timeErr)
		}
	})
	assertClosedSyntheticRootError(t, err, "changed_during_read", root, result.ScenarioID)
}

func TestAggregateSyntheticOutputRootFailsClosedWithoutPermissionProof(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific fail-closed guarantee")
	}
	root := newSyntheticOutputRoot(t)
	_, err := AggregateSyntheticOutputRoot(root)
	assertClosedSyntheticRootError(t, err, "unsafe_permissions", root)
}

func syntheticRootTestResult(t *testing.T) Result {
	t.Helper()
	scenario := validScenario()
	observation := validObservation()
	observation.Runtime = Runtime{
		Provider: "codex", AgentVersion: "test-agent", Model: "gpt-test", Reasoning: "high", ATLVersion: "test-atl",
		PromptContractSHA256: strings.Repeat("a", 64),
	}
	result, err := Evaluate(scenario, observation)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newSyntheticOutputRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "synthetic-runs")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivateTestFile(t, filepath.Join(root, privateOutputRootMarker), []byte(privateOutputRootMarkerContents))
	if err := os.Mkdir(filepath.Join(root, ".ephemeral"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeSyntheticRootResult(t *testing.T, root string, run int, result Result) []byte {
	t.Helper()
	return writeSyntheticRootResultAt(t, syntheticRootResultPath(root, result, run), result)
}

func writeSyntheticRootResultAt(t *testing.T, resultPath string, result Result) []byte {
	t.Helper()
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	writePrivateTestFile(t, resultPath, data)
	return data
}

func syntheticRootResultPath(root string, result Result, run int) string {
	return filepath.Join(root, result.ScenarioID, result.Runtime.Provider, result.Variant, fmt.Sprintf("run-%02d", run), "result.json")
}

func writePrivateTestFile(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	for directory := filepath.Dir(name); ; directory = filepath.Dir(directory) {
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if directory == filepath.Dir(directory) || strings.HasSuffix(directory, "synthetic-runs") {
			break
		}
	}
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertClosedSyntheticRootError(t *testing.T, err error, code string, privateValues ...string) {
	t.Helper()
	if err == nil || err.Error() != "synthetic output root rejected: "+code {
		t.Fatalf("err=%v want code=%s", err, code)
	}
	for _, value := range append(privateValues, "run-01", "result.json") {
		if value != "" && bytes.Contains([]byte(err.Error()), []byte(value)) {
			t.Fatalf("error leaked %q: %v", value, err)
		}
	}
}

func requireSyntheticRootPermissionChecks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("aggregate-root fails closed until owner-only ACLs can be verified")
	}
}
