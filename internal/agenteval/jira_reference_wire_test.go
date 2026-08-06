package agenteval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeJiraIssueReferenceWiresAcceptReconciledReleasedShapes(t *testing.T) {
	result, err := DecodeJiraIssueRefsResult(bytes.NewReader(validJiraIssueReferenceWire(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Key != "RF-42" || result.Count != 1 || result.Issues[0].Refs[0].Kind != "doc" {
		t.Fatalf("result=%+v", result)
	}

	view, err := DecodeJiraIssueRefsView(bytes.NewReader(validJiraIssueReferenceWire(t, true)))
	if err != nil {
		t.Fatal(err)
	}
	if view.SchemaVersion != 1 || view.Count != 1 || view.Issues[0].Key != "RF-42" {
		t.Fatalf("view=%+v", view)
	}
}

func TestDecodeJiraIssueReferenceWiresFailClosedOnSyntaxAndMemberDrift(t *testing.T) {
	valid := validJiraIssueReferenceWire(t, false)
	tests := map[string][]byte{
		"unknown root member": mutateJiraReferenceWire(t, valid, func(root map[string]any) {
			root["private"] = true
		}),
		"missing root member": mutateJiraReferenceWire(t, valid, func(root map[string]any) {
			delete(root, "summary")
		}),
		"missing nested member": mutateJiraReferenceWire(t, valid, func(root map[string]any) {
			delete(root["selection"].(map[string]any), "mode")
		}),
		"unknown source member": mutateJiraReferenceWire(t, valid, func(root map[string]any) {
			issue := root["issues"].([]any)[0].(map[string]any)
			source := issue["sources"].(map[string]any)["description"].(map[string]any)
			source["sample"] = "private"
		}),
		"null required member": mutateJiraReferenceWire(t, valid, func(root map[string]any) {
			root["summary"] = nil
		}),
		"null optional member": mutateJiraReferenceWire(t, valid, func(root map[string]any) {
			root["warnings"] = nil
		}),
		"trailing value": append(bytes.Clone(valid), []byte("\n{}")...),
	}
	duplicate := bytes.Replace(valid, []byte(`"count":1`), []byte(`"count":1,"count":1`), 1)
	if bytes.Equal(duplicate, valid) {
		t.Fatal("duplicate-key mutation did not apply")
	}
	tests["duplicate member"] = duplicate

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeJiraIssueRefsResult(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid result wire was accepted")
			}
		})
	}

	oversized := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), maxContractBytes-len(valid)+1)...)
	if _, err := DecodeJiraIssueRefsResult(bytes.NewReader(oversized)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized result error=%v", err)
	}
}

func TestDecodeJiraIssueReferenceWiresRejectUnreconciledFacts(t *testing.T) {
	validResult := validJiraIssueReferenceWire(t, false)
	resultMutations := map[string]func(map[string]any){
		"selection count": func(root map[string]any) {
			root["selection"].(map[string]any)["count"] = float64(2)
		},
		"aggregate reference count": func(root map[string]any) {
			root["summary"].(map[string]any)["reference_count"] = float64(2)
		},
		"source completeness": func(root map[string]any) {
			issue := root["issues"].([]any)[0].(map[string]any)
			issue["sources"].(map[string]any)["description"].(map[string]any)["complete"] = false
		},
		"unknown reference kind": func(root map[string]any) {
			issue := root["issues"].([]any)[0].(map[string]any)
			issue["refs"].([]any)[0].(map[string]any)["kind"] = "private"
		},
		"raw reference disagreement": func(root map[string]any) {
			issue := root["issues"].([]any)[0].(map[string]any)
			issue["refs"] = append(issue["refs"].([]any), map[string]any{
				"url": "https://docs.example.com/second", "kind": "doc",
			})
		},
		"key mode query echo": func(root map[string]any) {
			root["jql"] = "project=RF"
		},
		"key mode issue mismatch": func(root map[string]any) {
			root["issues"].([]any)[0].(map[string]any)["key"] = "RF-43"
		},
	}
	for name, mutate := range resultMutations {
		t.Run("result "+name, func(t *testing.T) {
			data := mutateJiraReferenceWire(t, validResult, mutate)
			if _, err := DecodeJiraIssueRefsResult(bytes.NewReader(data)); err == nil {
				t.Fatal("unreconciled result was accepted")
			}
		})
	}

	validView := validJiraIssueReferenceWire(t, true)
	viewMutations := map[string]func(map[string]any){
		"schema": func(root map[string]any) { root["schema_version"] = float64(2) },
		"mode": func(root map[string]any) {
			root["selection"].(map[string]any)["mode"] = "all"
		},
		"warning": func(root map[string]any) {
			root["warnings"] = []any{"unrecognized"}
		},
		"kind total": func(root map[string]any) {
			issue := root["issues"].([]any)[0].(map[string]any)
			issue["reference_summary"].(map[string]any)["reference_kind_counts"] = map[string]any{"doc": float64(2)}
		},
	}
	for name, mutate := range viewMutations {
		t.Run("view "+name, func(t *testing.T) {
			data := mutateJiraReferenceWire(t, validView, mutate)
			if _, err := DecodeJiraIssueRefsView(bytes.NewReader(data)); err == nil {
				t.Fatal("unreconciled view was accepted")
			}
		})
	}
}

func validJiraIssueReferenceWire(t *testing.T, view bool) []byte {
	t.Helper()
	issueSummary := map[string]any{
		"reference_count":               1,
		"reference_kind_counts":         map[string]any{"doc": 1},
		"source_count":                  1,
		"source_value_counts":           map[string]any{"description": 1},
		"complete_source_count":         1,
		"incomplete_source_count":       0,
		"truncated_source_count":        0,
		"reference_count_matches_kinds": true,
		"complete_matches_sources":      true,
		"truncated_matches_sources":     true,
	}
	issue := map[string]any{
		"key":      "RF-42",
		"complete": true,
		"sources": map[string]any{
			"description": map[string]any{"complete": true, "count": 1},
		},
		"reference_summary": issueSummary,
	}
	if !view {
		issue["summary"] = "Synthetic evidence"
		issue["type"] = "Task"
		issue["refs"] = []any{map[string]any{"url": "https://docs.example.com/spec", "kind": "doc"}}
	}
	root := map[string]any{
		"count":    1,
		"complete": true,
		"selection": map[string]any{
			"mode": "key", "count": 1, "complete": true,
		},
		"summary": map[string]any{
			"issue_count":                    1,
			"complete_issue_count":           1,
			"incomplete_issue_count":         0,
			"reference_count":                1,
			"reference_kind_counts":          map[string]any{"doc": 1},
			"source_count":                   1,
			"source_value_counts":            map[string]any{"description": 1},
			"complete_source_count":          1,
			"incomplete_source_count":        0,
			"truncated_source_count":         0,
			"count_matches_issues":           true,
			"selection_count_matches_issues": true,
			"reference_count_matches_kinds":  true,
			"issue_summaries_reconciled":     true,
			"complete_matches_inputs":        true,
			"truncated_matches_inputs":       true,
		},
		"issues": []any{issue},
	}
	if view {
		root["schema_version"] = 1
	} else {
		root["key"] = "RF-42"
	}
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutateJiraReferenceWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	encoded, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func repositorySyntheticATLBinary(t *testing.T) string {
	t.Helper()
	binary, err := filepath.Abs(filepath.Join("..", "..", "atl"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("repository ATL binary is unavailable; use an evaluator Make target that builds it")
	}
	return binary
}

func privateSyntheticATLScratch(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
