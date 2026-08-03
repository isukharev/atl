package mdwiki

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"testing"
)

type mdwikiBehaviorContract struct {
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

func loadMDWikiBehaviorContract(tb testing.TB) mdwikiBehaviorContract {
	tb.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "behavior-contract.v1.json"))
	if err != nil {
		tb.Fatal(err)
	}
	var contract mdwikiBehaviorContract
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		tb.Fatal(err)
	}
	if contract.SchemaVersion != 1 || len(contract.Cases) == 0 {
		tb.Fatalf("invalid mdwiki behavior contract header: %+v", contract)
	}
	return contract
}

func TestVersionedBehaviorContract(t *testing.T) {
	contract := loadMDWikiBehaviorContract(t)
	seen := map[string]bool{}
	markdownHeading := regexp.MustCompile(`(?m)^#{1,6} `)
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
				if out != testCase.Output {
					t.Fatalf("exact bytes drifted:\n got: %q\nwant: %q", out, testCase.Output)
				}
				for _, invariant := range testCase.Invariants {
					switch invariant {
					case "non_empty":
						if out == "" {
							t.Fatal("success returned an empty body")
						}
					case "no_markdown_heading":
						if markdownHeading.MatchString(out) {
							t.Fatalf("markdown heading leaked into wiki output: %q", out)
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
				if len(testCase.Invariants) != 1 || testCase.Invariants[0] != "no_partial_output" || out != "" {
					t.Fatalf("fail-closed contract drifted: invariants=%v output=%q", testCase.Invariants, out)
				}
			default:
				t.Fatalf("unknown outcome %q", testCase.Outcome)
			}
		})
	}
}

func TestVersionedFuzzRegressionInventory(t *testing.T) {
	contract := loadMDWikiBehaviorContract(t)
	actual, err := filepath.Glob(filepath.Join("testdata", "fuzz", "FuzzConvertDocument", "*"))
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
