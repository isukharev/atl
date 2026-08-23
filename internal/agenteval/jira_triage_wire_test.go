package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeJiraTriageWiresAcceptReleasedShapes(t *testing.T) {
	issue, err := DecodeJiraTriageIssueGet(bytes.NewReader(validJiraTriageIssueGetWire(t)))
	if err != nil || issue.Key != "SYN-41" || issue.Status != "Open" || issue.Fields["summary"] == nil {
		t.Fatalf("triage issue=%+v err=%v", issue, err)
	}

	preview, err := DecodeJiraTriageCommentPreview(bytes.NewReader(validJiraTriageCommentPreviewWire(t)))
	if err != nil || preview.Mode != "dry-run" || preview.Status != "would_apply" || preview.SatisfactionPolicy != "append_always" || !preview.Complete {
		t.Fatalf("triage preview=%+v err=%v", preview, err)
	}
	apply, err := DecodeJiraTriageCommentApply(bytes.NewReader(validJiraTriageCommentApplyWire(t)))
	if err != nil || apply.Mode != "apply" || apply.Status != "recovered" || apply.CommentID != "81" || !apply.WriteAttempted || !apply.Reconciled {
		t.Fatalf("triage apply=%+v err=%v", apply, err)
	}
	if _, err := DecodeJiraTriageCommentPreview(bytes.NewReader(validJiraTriageCommentApplyWire(t))); err == nil {
		t.Fatal("apply wire satisfied the preview producer decoder")
	}
	if _, err := DecodeJiraTriageCommentApply(bytes.NewReader(validJiraTriageCommentPreviewWire(t))); err == nil {
		t.Fatal("preview wire satisfied the apply decoder")
	}
}

func TestDecodeJiraTriageWiresFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		valid      []byte
		rootMember string
		maximum    int
		decode     func([]byte) error
		semantic   func(map[string]any)
	}{
		{
			name: "issue get", valid: validJiraTriageIssueGetWire(t), rootMember: "key", maximum: jiraTriageIssueGetWireMaxBytes,
			decode:   func(data []byte) error { _, err := DecodeJiraTriageIssueGet(bytes.NewReader(data)); return err },
			semantic: func(root map[string]any) { root["fields"].(map[string]any)["summary"] = "different" },
		},
		{
			name: "comment preview", valid: validJiraTriageCommentPreviewWire(t), rootMember: "key", maximum: jiraTriageCommentPreviewWireMaxBytes,
			decode:   func(data []byte) error { _, err := DecodeJiraTriageCommentPreview(bytes.NewReader(data)); return err },
			semantic: func(root map[string]any) { root["body_bytes"] = float64((1 << 20) + 1) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := map[string][]byte{
				"unknown root": mutateJiraTriageWire(t, test.valid, func(root map[string]any) { root["backend_payload"] = true }),
				"missing root": mutateJiraTriageWire(t, test.valid, func(root map[string]any) { delete(root, test.rootMember) }),
				"null root":    mutateJiraTriageWire(t, test.valid, func(root map[string]any) { root[test.rootMember] = nil }),
				"semantic":     mutateJiraTriageWire(t, test.valid, test.semantic),
				"trailing":     append(bytes.Clone(test.valid), []byte("\n{}")...),
				"oversize":     bytes.Repeat([]byte(" "), test.maximum+1),
				"utf8":         append([]byte{0xff}, test.valid...),
			}
			duplicate := bytes.Replace(test.valid, []byte(`"`+test.rootMember+`":`), []byte(`"`+test.rootMember+`":null,"`+test.rootMember+`":`), 1)
			if bytes.Equal(duplicate, test.valid) {
				t.Fatal("duplicate-key mutation did not apply")
			}
			invalid["duplicate root"] = duplicate
			for name, data := range invalid {
				t.Run(name, func(t *testing.T) {
					if err := test.decode(data); err == nil {
						t.Fatal("invalid triage wire was accepted")
					}
				})
			}
		})
	}
}

func TestDecodeJiraTriageWiresRejectNestedAndStateDrift(t *testing.T) {
	issueCases := map[string]func(map[string]any){
		"missing fields": func(root map[string]any) { delete(root, "fields") },
		"null fields":    func(root map[string]any) { root["fields"] = nil },
		"missing status": func(root map[string]any) { delete(root["fields"].(map[string]any), "status") },
		"wrong project": func(root map[string]any) {
			root["fields"].(map[string]any)["project"].(map[string]any)["key"] = "OTHER"
		},
		"wrong status id": func(root map[string]any) {
			root["fields"].(map[string]any)["status"].(map[string]any)["id"] = "4"
		},
		"unknown link member": func(root map[string]any) {
			root["links"].([]any)[0].(map[string]any)["backend"] = true
		},
		"invalid link direction": func(root map[string]any) {
			root["links"].([]any)[0].(map[string]any)["direction"] = "sideways"
		},
		"unknown comment member": func(root map[string]any) {
			root["comments"].([]any)[0].(map[string]any)["backend"] = true
		},
		"blank comment id": func(root map[string]any) {
			root["comments"].([]any)[0].(map[string]any)["id"] = " "
		},
		"blank description": func(root map[string]any) { root["description"] = " " },
	}
	for name, mutate := range issueCases {
		t.Run("issue "+name, func(t *testing.T) {
			if _, err := DecodeJiraTriageIssueGet(bytes.NewReader(mutateJiraTriageWire(t, validJiraTriageIssueGetWire(t), mutate))); err == nil {
				t.Fatal("invalid issue wire was accepted")
			}
		})
	}

	previewCases := map[string]func(map[string]any){
		"unknown bounds": func(root map[string]any) { root["bounds"].(map[string]any)["backend"] = true },
		"missing actor":  func(root map[string]any) { delete(root, "actor_sha256") },
		"apply mode":     func(root map[string]any) { root["mode"] = "apply" },
		"applied status": func(root map[string]any) { root["status"] = "applied" },
		"incomplete":     func(root map[string]any) { root["complete"] = false },
		"proposal case":  func(root map[string]any) { root["proposal_hash"] = strings.Repeat("A", 64) },
		"short baseline": func(root map[string]any) { root["baseline_sha256"] = "abc" },
		"wrong policy":   func(root map[string]any) { root["satisfaction_policy"] = "exact_body_present" },
		"wrong requests": func(root map[string]any) { root["usage"].(map[string]any)["requests"] = 2 },
	}
	for name, mutate := range previewCases {
		t.Run("preview "+name, func(t *testing.T) {
			if _, err := DecodeJiraTriageCommentPreview(bytes.NewReader(mutateJiraTriageWire(t, validJiraTriageCommentPreviewWire(t), mutate))); err == nil {
				t.Fatal("invalid preview wire was accepted")
			}
		})
	}

	applyCases := map[string]func(map[string]any){
		"unknown root":   func(root map[string]any) { root["backend"] = true },
		"missing id":     func(root map[string]any) { delete(root, "comment_id") },
		"wrong status":   func(root map[string]any) { root["status"] = "applied" },
		"not attempted":  func(root map[string]any) { root["write_attempted"] = false },
		"not reconciled": func(root map[string]any) { root["reconciled"] = false },
		"wrong requests": func(root map[string]any) { root["usage"].(map[string]any)["requests"] = 8 },
	}
	for name, mutate := range applyCases {
		t.Run("apply "+name, func(t *testing.T) {
			if _, err := DecodeJiraTriageCommentApply(bytes.NewReader(mutateJiraTriageWire(t, validJiraTriageCommentApplyWire(t), mutate))); err == nil {
				t.Fatal("invalid apply wire was accepted")
			}
		})
	}
}

func TestDecodeJiraTriageCommentWiresEnforceProductInvariants(t *testing.T) {
	largeKeyWire := mutateJiraTriageWire(t, validJiraTriageCommentPreviewWire(t), func(root map[string]any) {
		root["requested_key"], root["key"] = "AB-18446744073709551616", "AB-18446744073709551616"
		root["project"] = "AB"
	})
	if _, err := DecodeJiraTriageCommentPreview(bytes.NewReader(largeKeyWire)); err != nil {
		t.Fatalf("canonical key suffix is not a backend numeric id: %v", err)
	}
	tests := []struct {
		name   string
		apply  bool
		mutate func(map[string]any)
	}{
		{name: "noncanonical key", mutate: func(root map[string]any) { root["requested_key"], root["key"] = "syn-41", "syn-41" }},
		{name: "oversize key", mutate: func(root map[string]any) {
			root["requested_key"], root["key"] = "AB-"+strings.Repeat("1", 62), "AB-"+strings.Repeat("1", 62)
		}},
		{name: "zero issue id", mutate: func(root map[string]any) { root["issue_id"] = "0" }},
		{name: "overflow issue id", mutate: func(root map[string]any) { root["issue_id"] = "18446744073709551616" }},
		{name: "oversize issue id", mutate: func(root map[string]any) { root["issue_id"] = strings.Repeat("1", 65) }},
		{name: "untagged backend digest", mutate: func(root map[string]any) { root["backend_sha256"] = triageTriageHash("backend") }},
		{name: "double tagged backend digest", mutate: func(root map[string]any) { root["backend_sha256"] = "sha256:sha256:" + triageTriageHash("backend") }},
		{name: "unsupported timestamp", mutate: func(root map[string]any) { root["updated"] = "2026-08-22T10:00:00" }},
		{name: "comma timestamp", mutate: func(root map[string]any) { root["updated"] = "2026-08-22T10:00:00,123Z" }},
		{name: "count beyond inventory bound", mutate: func(root map[string]any) { root["current_count"] = json.Number("10001") }},
		{name: "zero comment id", apply: true, mutate: func(root map[string]any) { root["comment_id"] = "0" }},
		{name: "oversize comment id", apply: true, mutate: func(root map[string]any) { root["comment_id"] = strings.Repeat("1", 65) }},
		{name: "nonadvancing readback", apply: true, mutate: func(root map[string]any) { root["readback_updated"] = root["updated"] }},
		{name: "backward readback", apply: true, mutate: func(root map[string]any) { root["readback_updated"] = "2026-08-22T09:59:59Z" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire := validJiraTriageCommentPreviewWire(t)
			decode := func(data []byte) error {
				_, err := DecodeJiraTriageCommentPreview(bytes.NewReader(data))
				return err
			}
			if test.apply {
				wire = validJiraTriageCommentApplyWire(t)
				decode = func(data []byte) error {
					_, err := DecodeJiraTriageCommentApply(bytes.NewReader(data))
					return err
				}
			}
			if err := decode(mutateJiraTriageWire(t, wire, test.mutate)); err == nil {
				t.Fatal("invalid guarded comment wire was accepted")
			}
		})
	}
}

func TestDecodeJiraTriageWiresHonorExactBounds(t *testing.T) {
	for _, test := range []struct {
		name    string
		valid   []byte
		maximum int
		decode  func([]byte) error
	}{
		{"issue", validJiraTriageIssueGetWire(t), jiraTriageIssueGetWireMaxBytes, func(data []byte) error { _, err := DecodeJiraTriageIssueGet(bytes.NewReader(data)); return err }},
		{"preview", validJiraTriageCommentPreviewWire(t), jiraTriageCommentPreviewWireMaxBytes, func(data []byte) error { _, err := DecodeJiraTriageCommentPreview(bytes.NewReader(data)); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			atLimit := append(bytes.Clone(test.valid), bytes.Repeat([]byte(" "), test.maximum-len(test.valid))...)
			if err := test.decode(atLimit); err != nil {
				t.Fatalf("exact wire limit rejected: %v", err)
			}
			if err := test.decode(append(atLimit, ' ')); err == nil {
				t.Fatal("oversize wire was accepted")
			}
		})
	}
}

func validJiraTriageIssueGetWire(t *testing.T) []byte {
	t.Helper()
	return mustJiraTriageJSON(t, map[string]any{
		"id": "9041", "key": "SYN-41", "summary": "Synthetic cache failure", "status": "Open", "status_id": "3",
		"type": "Bug", "project": "SYN", "description": "SyntheticError after token rotation.",
		"fields": map[string]any{
			"summary": "Synthetic cache failure", "description": "SyntheticError after token rotation.",
			"status": map[string]any{"id": "3", "name": "Open"}, "issuetype": map[string]any{"name": "Bug"},
			"project": map[string]any{"key": "SYN"}, "backend_value": map[string]any{"nested": []any{true, nil}},
		},
		"labels":   []any{"synthetic"},
		"links":    []any{map[string]any{"id": "71", "type": "duplicates", "type_name": "Duplicate", "direction": "outward", "key": "SYN-42"}},
		"comments": []any{map[string]any{"id": "81", "author": "Synthetic User", "created": "2026-08-05T00:00:00Z", "body": "Synthetic note."}},
	})
}

func validJiraTriageCommentPreviewWire(t *testing.T) []byte {
	t.Helper()
	return mustJiraTriageJSON(t, map[string]any{
		"schema_version": 1, "operation": "jira_issue_comment_append", "satisfaction_policy": "append_always",
		"backend_sha256": "sha256:" + triageTriageHash("backend"), "requested_key": "SYN-41", "issue_id": "9041",
		"key": "SYN-41", "project": "SYN", "updated": "2026-08-22T10:00:00Z",
		"body_sha256": triageTriageHash("body"), "body_bytes": 57, "actor_sha256": triageTriageHash("actor"),
		"current_count": 1, "baseline_sha256": triageTriageHash("baseline"), "exact_body_count": 0,
		"bounds": map[string]any{
			"max_key_bytes": 64, "max_body_bytes": 1 << 20, "max_evidence_id_bytes": 64,
			"max_evidence_metadata_bytes": 64 << 10, "max_pages": 100, "max_items": 10_000,
			"max_inventory_bytes": 16 << 20, "preview_max_requests": 102, "apply_max_requests": 306,
			"max_aggregate_response_bytes": 16 << 20, "deadline_millis": 60_000,
		},
		"usage": map[string]any{"requests": 3, "response_bytes": 1024},
		"mode":  "dry-run", "status": "would_apply", "proposal_hash": triageTriageHash("proposal"),
		"write_attempted": false, "reconciled": false, "complete": true,
	})
}

func validJiraTriageCommentApplyWire(t *testing.T) []byte {
	t.Helper()
	return mutateJiraTriageWire(t, validJiraTriageCommentPreviewWire(t), func(root map[string]any) {
		root["mode"] = "apply"
		root["status"] = "recovered"
		root["readback_updated"] = "2026-08-22T10:00:01Z"
		root["comment_id"] = "81"
		root["write_attempted"] = true
		root["reconciled"] = true
		root["usage"].(map[string]any)["requests"] = json.Number("9")
	})
}

func mutateJiraTriageWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	return mustJiraTriageJSON(t, root)
}

func mustJiraTriageJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func triageTriageHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
