package agenteval

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDecodeMirrorSnapshotWiresAcceptReleasedShapes(t *testing.T) {
	jira, err := decodeJiraMirrorSnapshotWire(bytes.NewReader(validMirrorSnapshotWire(t, "jira")))
	if err != nil || jira.SchemaVersion != 1 || jira.Service != "jira" || !jira.Complete || !jira.Reconciled {
		t.Fatalf("Jira wire=%+v err=%v", jira, err)
	}
	confluence, err := decodeConfluenceMirrorSnapshotWire(bytes.NewReader(validMirrorSnapshotWire(t, "confluence")))
	if err != nil || confluence.SchemaVersion != 1 || confluence.Service != "confluence" || !confluence.Complete || !confluence.Reconciled {
		t.Fatalf("Confluence wire=%+v err=%v", confluence, err)
	}
}

func TestDecodeMirrorSnapshotWiresRejectWireDrift(t *testing.T) {
	for _, test := range []struct {
		service string
		decode  func([]byte) error
	}{
		{service: "jira", decode: func(data []byte) error {
			_, err := decodeJiraMirrorSnapshotWire(bytes.NewReader(data))
			return err
		}},
		{service: "confluence", decode: func(data []byte) error {
			_, err := decodeConfluenceMirrorSnapshotWire(bytes.NewReader(data))
			return err
		}},
	} {
		t.Run(test.service, func(t *testing.T) {
			valid := validMirrorSnapshotWire(t, test.service)
			duplicate := bytes.Replace(valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
			if bytes.Equal(duplicate, valid) {
				t.Fatal("duplicate mutation did not apply")
			}
			invalid := map[string][]byte{
				"schema version": mutateMirrorSnapshotWire(t, valid, func(root map[string]any) {
					root["schema_version"] = 2
				}),
				"service": mutateMirrorSnapshotWire(t, valid, func(root map[string]any) {
					root["service"] = "other"
				}),
				"unknown": mutateMirrorSnapshotWire(t, valid, func(root map[string]any) {
					root["backend_payload"] = true
				}),
				"missing": mutateMirrorSnapshotWire(t, valid, func(root map[string]any) {
					delete(root, "schema_version")
				}),
				"null": mutateMirrorSnapshotWire(t, valid, func(root map[string]any) {
					root["local"] = nil
				}),
				"malformed": mutateMirrorSnapshotWire(t, valid, func(root map[string]any) {
					root["local"].(map[string]any)["present"] = "one"
				}),
				"negative": mutateMirrorSnapshotWire(t, valid, func(root map[string]any) {
					root["local"].(map[string]any)["present"] = -1
				}),
				"contradictory": mutateMirrorSnapshotWire(t, valid, func(root map[string]any) {
					root["render"].(map[string]any)["reconciled"] = false
				}),
				"remote activity": mutateMirrorSnapshotWire(t, valid, func(root map[string]any) {
					root["remote_requested"] = true
					root["remote"].(map[string]any)["requested"] = true
				}),
				"duplicate": duplicate,
			}
			for name, data := range invalid {
				t.Run(name, func(t *testing.T) {
					if err := test.decode(data); err == nil {
						t.Fatal("wire drift was accepted")
					}
				})
			}

			atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), mirrorSnapshotWireMaxBytes-len(valid))...)
			if err := test.decode(atLimit); err != nil {
				t.Fatalf("wire at exact byte limit rejected: %v", err)
			}
			if err := test.decode(append(atLimit, ' ')); err == nil {
				t.Fatal("oversized wire was accepted")
			}
		})
	}
}

func validMirrorSnapshotWire(t *testing.T, service string) []byte {
	t.Helper()
	local := map[string]any{
		"present": 0, "clean": 0, "locally_edited": 0, "tracked": 0, "untracked": 0, "non_canonical": 0, "reconciled": true,
	}
	render := map[string]any{
		"expected": 0, "present": 0, "missing": 0, "current": 0, "legacy": 0, "missing_marker": 0,
		"unsupported": 0, "unreadable": 0, "state_recorded": 0, "state_missing": 0, "renderer_compatible": true, "reconciled": true,
	}
	remote := map[string]any{
		"requested": false, "eligible": 0, "attempted": 0, "not_attempted": 0, "checked": 0,
		"in_sync": 0, "drifted": 0, "unavailable": 0, "reconciled": true,
	}
	root := map[string]any{
		"schema_version": 1, "service": service, "remote_requested": false, "complete": true, "reconciled": true,
		"local": local, "render": render, "remote": remote,
	}
	switch service {
	case "jira":
		root["native"] = map[string]any{
			"total": 0, "unchanged": 0, "modified": 0, "removed": 0, "untracked": 0, "non_canonical": 0,
			"missing_baseline": 0, "baseline_mismatch": 0, "unreadable": 0, "baseline_present": 0, "baseline_missing": 0,
			"baseline_unreadable": 0, "baseline_valid": 0, "baseline_invalid": 0, "reconciled": true,
		}
		root["snapshot"] = map[string]any{
			"expected": 0, "present": 0, "missing": 0, "readable": 0, "unreadable": 0, "valid": 0,
			"invalid": 0, "key_matched": 0, "key_mismatched": 0, "reconciled": true,
		}
		root["pending"] = map[string]any{
			"total": 0, "valid": 0, "invalid": 0, "unreadable": 0, "bound": 0, "unbound": 0,
			"field_edits": 0, "active_transactions": 0, "reconciled": true,
		}
	case "confluence":
		root["native"] = map[string]any{
			"total": 0, "unchanged": 0, "added": 0, "removed": 0, "modified": 0, "malformed": 0,
			"missing_baseline": 0, "baseline_mismatch": 0, "unreadable": 0, "baseline_present": 0, "baseline_missing": 0,
			"baseline_unreadable": 0, "baseline_valid": 0, "baseline_invalid": 0, "reconciled": true,
		}
		root["validation"] = map[string]any{
			"total": 0, "present": 0, "absent": 0, "valid": 0, "invalid": 0, "unreadable": 0, "reconciled": true,
		}
	default:
		t.Fatalf("unsupported mirror service %q", service)
	}
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutateMirrorSnapshotWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	result, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
