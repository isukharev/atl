package mdcsf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/isukharev/atl/internal/csf"
)

type mdcsfBehaviorContract struct {
	SchemaVersion int `json:"schema_version"`
	Cases         []struct {
		Name       string   `json:"name"`
		Input      string   `json:"input"`
		Outcome    string   `json:"outcome"`
		Output     string   `json:"output,omitempty"`
		ErrorClass string   `json:"error_class,omitempty"`
		Invariants []string `json:"invariants"`
	} `json:"cases"`
	FuzzRegressions []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"fuzz_regressions"`
}

func loadMDCSFBehaviorContract(tb testing.TB) mdcsfBehaviorContract {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "behavior-contract.v1.json"))
	if err != nil {
		tb.Fatal(err)
	}
	var contract mdcsfBehaviorContract
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		tb.Fatal(err)
	}
	if contract.SchemaVersion != 1 || len(contract.Cases) == 0 {
		tb.Fatalf("invalid mdcsf behavior contract header: %+v", contract)
	}
	return contract
}

func TestVersionedBehaviorContract(t *testing.T) {
	contract := loadMDCSFBehaviorContract(t)
	seen := map[string]bool{}
	for _, testCase := range contract.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			if testCase.Name == "" || seen[testCase.Name] {
				t.Fatalf("duplicate or empty case name %q", testCase.Name)
			}
			seen[testCase.Name] = true
			out, err := ConvertDocument(testCase.Input)
			switch testCase.Outcome {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
				if string(out) != testCase.Output {
					t.Fatalf("exact bytes drifted:\n got: %q\nwant: %q", out, testCase.Output)
				}
				for _, invariant := range testCase.Invariants {
					switch invariant {
					case "non_empty":
						if len(out) == 0 {
							t.Fatal("success returned an empty body")
						}
					case "well_formed_csf":
						if problems := csf.Validate(out); csf.HasErrors(problems) {
							t.Fatalf("success produced invalid CSF: %+v", problems)
						}
					default:
						t.Fatalf("unknown invariant %q", invariant)
					}
				}
			case "error":
				if err == nil {
					t.Fatalf("conversion succeeded with %q", out)
				}
				if testCase.ErrorClass != "UnsupportedError" {
					t.Fatalf("unknown error class %q", testCase.ErrorClass)
				}
				var unsupported *UnsupportedError
				if !errors.As(err, &unsupported) {
					t.Fatalf("error class=%T want *UnsupportedError", err)
				}
				if len(testCase.Invariants) != 1 || testCase.Invariants[0] != "no_partial_output" || out != nil {
					t.Fatalf("fail-closed contract drifted: invariants=%v output=%q", testCase.Invariants, out)
				}
			default:
				t.Fatalf("unknown outcome %q", testCase.Outcome)
			}
		})
	}
}

func TestVersionedFuzzRegressionInventory(t *testing.T) {
	contract := loadMDCSFBehaviorContract(t)
	actual, err := filepath.Glob(filepath.Join("testdata", "fuzz", "FuzzConvert", "*"))
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, len(contract.FuzzRegressions))
	for _, regression := range contract.FuzzRegressions {
		want = append(want, filepath.FromSlash(regression.Path))
	}
	sort.Strings(actual)
	sort.Strings(want)
	if !slices.Equal(actual, want) {
		t.Fatalf("fuzz regression membership drifted: actual=%v want=%v", actual, want)
	}
	for _, regression := range contract.FuzzRegressions {
		data, err := os.ReadFile(filepath.FromSlash(regression.Path))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != regression.SHA256 {
			t.Fatalf("fuzz regression %s digest drifted", regression.Path)
		}
	}
}
