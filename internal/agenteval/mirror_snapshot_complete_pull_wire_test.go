package agenteval

import (
	"bytes"
	"testing"
)

func testJiraMirrorSnapshotCompletePullAcceptsStatuses(t *testing.T) {
	for _, status := range []string{"empty", "resumable", "recovery_pending", "invalid", "inventory_limit"} {
		t.Run(status, func(t *testing.T) {
			data := validJiraCompletePullSnapshotWire(t, status)
			got, err := decodeJiraMirrorSnapshotWire(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if got.SchemaVersion != 2 || got.CompletePull == nil || got.CompletePull.Status != status {
				t.Fatalf("unexpected wire: %+v", got)
			}
			if got.Complete != got.CompletePull.Healthy || got.Reconciled != got.CompletePull.Complete {
				t.Fatalf("inconsistent summary: %+v", got)
			}
		})
	}
	for _, reason := range []string{"malformed", "unsupported_schema", "orphaned", "unreadable", "entry_limit", "byte_limit"} {
		t.Run(reason, func(t *testing.T) {
			status := "invalid"
			if reason == "entry_limit" || reason == "byte_limit" {
				status = "inventory_limit"
			}
			data := mutateJiraCompletePullWire(t, validJiraCompletePullSnapshotWire(t, status), func(pull map[string]any) {
				pull["reason"] = reason
				pull["checkpoints"], pull["journals"], pull["publications"] = 128, 128, 128
				pull["selected"], pull["completed"], pull["remaining"] = 128000000, 64000000, 64000000
			})
			if _, err := decodeJiraMirrorSnapshotWire(bytes.NewReader(data)); err != nil {
				t.Fatalf("validated partial counts rejected: %v", err)
			}
		})
	}
	for _, field := range []string{"journals", "publications"} {
		t.Run("pending only "+field, func(t *testing.T) {
			data := mutateJiraCompletePullWire(t, validJiraCompletePullSnapshotWire(t, "recovery_pending"), func(pull map[string]any) {
				pull["journals"], pull["publications"] = 0, 0
				pull[field] = 1
			})
			if _, err := decodeJiraMirrorSnapshotWire(bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func testJiraMirrorSnapshotCompletePullRejectsMemberDrift(t *testing.T) {
	valid := validJiraCompletePullSnapshotWire(t, "empty")
	for _, member := range []string{"status", "reason", "recovery", "checkpoints", "selected", "completed", "remaining", "journals", "publications", "complete", "healthy"} {
		for _, mutation := range []string{"missing", "null", "type"} {
			t.Run(member+"/"+mutation, func(t *testing.T) {
				data := mutateJiraCompletePullWire(t, valid, func(pull map[string]any) {
					switch mutation {
					case "missing":
						delete(pull, member)
					case "null":
						pull[member] = nil
					case "type":
						pull[member] = []any{}
					}
				})
				assertJiraCompletePullWireRejected(t, data)
			})
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"v2 missing":     func(root map[string]any) { delete(root, "complete_pull") },
		"v2 null":        func(root map[string]any) { root["complete_pull"] = nil },
		"v2 array":       func(root map[string]any) { root["complete_pull"] = []any{} },
		"v1 member":      func(root map[string]any) { root["schema_version"] = 1 },
		"v1 null member": func(root map[string]any) { root["schema_version"], root["complete_pull"] = 1, nil },
		"future version": func(root map[string]any) { root["schema_version"] = 3 },
		"unknown root":   func(root map[string]any) { root["extra"] = 0 },
		"unknown member": func(root map[string]any) { root["complete_pull"].(map[string]any)["extra"] = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			assertJiraCompletePullWireRejected(t, mutateMirrorSnapshotWire(t, valid, mutate))
		})
	}
	duplicate := bytes.Replace(valid, []byte(`"status":"empty"`), []byte(`"status":"empty","status":"empty"`), 1)
	if bytes.Equal(duplicate, valid) {
		t.Fatal("duplicate mutation did not apply")
	}
	assertJiraCompletePullWireRejected(t, duplicate)
	assertJiraCompletePullWireRejected(t, append(bytes.Clone(valid), []byte(` {}`)...))
	for _, version := range []int{1, 2} {
		data := mutateMirrorSnapshotWire(t, validMirrorSnapshotWire(t, "confluence"), func(root map[string]any) {
			root["schema_version"] = version
			root["complete_pull"] = map[string]any{}
		})
		if _, err := decodeConfluenceMirrorSnapshotWire(bytes.NewReader(data)); err == nil {
			t.Fatalf("Confluence accepted Jira progress member at version %d", version)
		}
	}
}

func testJiraMirrorSnapshotCompletePullRejectsContradictions(t *testing.T) {
	for _, status := range []string{"empty", "resumable", "recovery_pending", "invalid", "inventory_limit"} {
		valid := validJiraCompletePullSnapshotWire(t, status)
		for _, field := range []string{"complete", "healthy"} {
			t.Run(status+"/"+field, func(t *testing.T) {
				assertJiraCompletePullWireRejected(t, mutateJiraCompletePullWire(t, valid, func(pull map[string]any) {
					pull[field] = !pull[field].(bool)
				}))
			})
		}
		for _, field := range []string{"status", "reason", "recovery"} {
			t.Run(status+"/"+field, func(t *testing.T) {
				assertJiraCompletePullWireRejected(t, mutateJiraCompletePullWire(t, valid, func(pull map[string]any) { pull[field] = "other" }))
			})
		}
		t.Run(status+"/top reconciliation", func(t *testing.T) {
			assertJiraCompletePullWireRejected(t, mutateMirrorSnapshotWire(t, valid, func(root map[string]any) {
				root["reconciled"] = !root["reconciled"].(bool)
			}))
		})
		if status == "recovery_pending" || status == "invalid" || status == "inventory_limit" {
			t.Run(status+"/top complete", func(t *testing.T) {
				assertJiraCompletePullWireRejected(t, mutateMirrorSnapshotWire(t, valid, func(root map[string]any) { root["complete"] = true }))
			})
		}
	}
	for _, field := range []string{"checkpoints", "selected", "completed", "remaining", "journals", "publications"} {
		limit := 128
		if field == "selected" || field == "completed" || field == "remaining" {
			limit = 128000000
		}
		for _, count := range []any{-1, limit + 1, 0.5} {
			t.Run("count/"+field, func(t *testing.T) {
				assertJiraCompletePullWireRejected(t, mutateJiraCompletePullWire(t, validJiraCompletePullSnapshotWire(t, "invalid"), func(pull map[string]any) { pull[field] = count }))
			})
		}
	}
	for _, test := range []struct {
		name, status string
		mutate       func(map[string]any)
	}{
		{"empty checkpoint", "empty", func(p map[string]any) { p["checkpoints"] = 1 }},
		{"empty selection", "empty", func(p map[string]any) { p["selected"], p["remaining"] = 1, 1 }},
		{"no checkpoint", "resumable", func(p map[string]any) { p["checkpoints"] = 0 }},
		{"unexpected journal", "resumable", func(p map[string]any) { p["journals"] = 1 }},
		{"unexpected publication", "resumable", func(p map[string]any) { p["publications"] = 1 }},
		{"no pending recovery", "recovery_pending", func(p map[string]any) { p["journals"], p["publications"] = 0, 0 }},
		{"selected mismatch", "invalid", func(p map[string]any) { p["selected"] = 1 }},
		{"journals exceed checkpoints", "invalid", func(p map[string]any) { p["journals"] = 1 }},
		{"publications exceed checkpoints", "invalid", func(p map[string]any) { p["publications"] = 1 }},
		{"invalid limit reason", "invalid", func(p map[string]any) { p["reason"] = "entry_limit" }},
		{"limit invalid reason", "inventory_limit", func(p map[string]any) { p["reason"] = "malformed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertJiraCompletePullWireRejected(t, mutateJiraCompletePullWire(t, validJiraCompletePullSnapshotWire(t, test.status), test.mutate))
		})
	}
}

func validJiraCompletePullSnapshotWire(t *testing.T, status string) []byte {
	t.Helper()
	return mutateMirrorSnapshotWire(t, validMirrorSnapshotWire(t, "jira"), func(root map[string]any) {
		pull := map[string]any{
			"status": status, "reason": "", "recovery": "none", "checkpoints": 0, "selected": 0,
			"completed": 0, "remaining": 0, "journals": 0, "publications": 0, "complete": true, "healthy": true,
		}
		switch status {
		case "resumable", "recovery_pending":
			pull["checkpoints"], pull["selected"], pull["completed"], pull["remaining"] = 2, 7, 3, 4
			pull["recovery"] = "rerun_original_command"
			if status == "recovery_pending" {
				pull["journals"], pull["publications"], pull["healthy"] = 1, 1, false
				pull["recovery"] = "preserve_for_inspection"
			}
		case "invalid", "inventory_limit":
			pull["complete"], pull["healthy"], pull["recovery"] = false, false, "preserve_for_inspection"
			pull["reason"] = "malformed"
			if status == "inventory_limit" {
				pull["reason"] = "entry_limit"
			}
		}
		root["schema_version"], root["complete_pull"] = 2, pull
		root["complete"], root["reconciled"] = pull["healthy"], pull["complete"]
	})
}

func mutateJiraCompletePullWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	return mutateMirrorSnapshotWire(t, data, func(root map[string]any) { mutate(root["complete_pull"].(map[string]any)) })
}

func assertJiraCompletePullWireRejected(t *testing.T, data []byte) {
	t.Helper()
	if _, err := decodeJiraMirrorSnapshotWire(bytes.NewReader(data)); err == nil {
		t.Fatal("invalid complete-pull wire was accepted")
	}
}
