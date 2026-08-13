package agenteval

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeConfluenceAttachmentDiscoveryViewReconcilesCompleteAndPartial(t *testing.T) {
	complete := confluenceAttachmentDiscoveryWireFixture(t, "complete")
	view, err := DecodeConfluenceAttachmentDiscoveryView(bytes.NewReader(complete))
	if err != nil {
		t.Fatal(err)
	}
	if !view.Complete || view.Qualification != "complete" || view.Count != 1 || view.Attachments[0].ID != "att_A-21" {
		t.Fatalf("view=%+v", view)
	}

	partial := confluenceAttachmentDiscoveryWireDocument(t, "partial")
	partial["complete"] = false
	partial["reason"] = "item_limit"
	partial["total_size"] = 3
	partial["next_cursor"] = confluenceAttachmentDiscoveryWireCursor(t, strings.Repeat("a", 64), 1)
	encoded, _ := json.Marshal(partial)
	view, err = DecodeConfluenceAttachmentDiscoveryView(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if view.Complete || view.Qualification != "partial" || view.Reason != "item_limit" || view.NextCursor == "" {
		t.Fatalf("partial=%+v", view)
	}

	delete(partial, "total_size")
	encoded, _ = json.Marshal(partial)
	if _, err := DecodeConfluenceAttachmentDiscoveryView(bytes.NewReader(encoded)); err != nil {
		t.Fatalf("partial without optional total: %v", err)
	}

	failed := confluenceAttachmentDiscoveryWireDocument(t, "failed")
	failed["complete"], failed["reason"], failed["count"], failed["attachments"] = false, "read_failed", 0, []any{}
	delete(failed, "total_size")
	encoded, _ = json.Marshal(failed)
	view, err = DecodeConfluenceAttachmentDiscoveryView(bytes.NewReader(encoded))
	if err != nil || view.Qualification != "failed" || view.Count != 0 || view.TotalSize != nil || view.NextCursor != "" {
		t.Fatalf("failed=%+v err=%v", view, err)
	}
}

func TestDecodeConfluenceAttachmentDiscoveryViewRejectsOpenOrContradictoryWire(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"unknown root":    func(root map[string]any) { root["url"] = "https://example.invalid/file" },
		"missing root":    func(root map[string]any) { delete(root, "qualification") },
		"null optional":   func(root map[string]any) { root["reason"] = nil },
		"unknown bound":   func(root map[string]any) { root["bounds"].(map[string]any)["extra"] = 1 },
		"missing bound":   func(root map[string]any) { delete(root["bounds"].(map[string]any), "max_requests") },
		"null attachment": func(root map[string]any) { root["attachments"] = nil },
		"unknown attachment": func(root map[string]any) {
			root["attachments"].([]any)[0].(map[string]any)["download_path"] = "/tmp/file"
		},
		"missing attachment": func(root map[string]any) {
			delete(root["attachments"].([]any)[0].(map[string]any), "container_version")
		},
		"null attachment field":      func(root map[string]any) { root["attachments"].([]any)[0].(map[string]any)["media_type"] = nil },
		"count mismatch":             func(root map[string]any) { root["count"] = 2 },
		"complete with reason":       func(root map[string]any) { root["reason"] = "item_limit" },
		"complete with empty reason": func(root map[string]any) { root["reason"] = "" },
		"complete without total":     func(root map[string]any) { delete(root, "total_size") },
		"complete null total":        func(root map[string]any) { root["total_size"] = nil },
		"complete with empty cursor": func(root map[string]any) { root["next_cursor"] = "" },
		"complete with cursor": func(root map[string]any) {
			root["next_cursor"] = confluenceAttachmentDiscoveryWireCursor(t, strings.Repeat("a", 64), 1)
		},
		"failed marked complete": func(root map[string]any) { root["qualification"], root["reason"] = "failed", "read_failed" },
		"failed with rows": func(root map[string]any) {
			root["qualification"], root["complete"], root["reason"] = "failed", false, "read_failed"
			delete(root, "total_size")
		},
		"failed with total": func(root map[string]any) {
			root["qualification"], root["complete"], root["reason"], root["attachments"], root["count"] = "failed", false, "read_failed", []any{}, 0
		},
		"failed with cursor": func(root map[string]any) {
			root["qualification"], root["complete"], root["reason"], root["attachments"], root["count"] = "failed", false, "read_failed", []any{}, 0
			delete(root, "total_size")
			root["next_cursor"] = confluenceAttachmentDiscoveryWireCursor(t, strings.Repeat("a", 64), 0)
		},
		"invalid attachment id": func(root map[string]any) {
			root["attachments"].([]any)[0].(map[string]any)["id"] = "attachment.21"
		},
		"oversize container id": func(root map[string]any) {
			root["attachments"].([]any)[0].(map[string]any)["container_id"] = strings.Repeat("x", 257)
		},
		"self container": func(root map[string]any) {
			attachment := root["attachments"].([]any)[0].(map[string]any)
			attachment["container_id"] = attachment["id"]
		},
		"duplicate attachment": func(root map[string]any) {
			root["attachments"] = append(root["attachments"].([]any), root["attachments"].([]any)[0])
			root["count"] = 2
			root["bounds"].(map[string]any)["max_items"] = 2
			root["total_size"] = 2
		},
		"bound usage": func(root map[string]any) { root["bounds"].(map[string]any)["requests_used"] = 3 },
		"partial without reason": func(root map[string]any) {
			root["qualification"], root["complete"] = "partial", false
			root["next_cursor"] = confluenceAttachmentDiscoveryWireCursor(t, strings.Repeat("a", 64), 1)
			delete(root, "reason")
		},
		"partial without cursor": func(root map[string]any) {
			root["qualification"], root["complete"], root["reason"] = "partial", false, "item_limit"
			delete(root, "next_cursor")
		},
		"partial with empty reason": func(root map[string]any) {
			root["qualification"], root["complete"], root["reason"] = "partial", false, ""
			root["next_cursor"] = confluenceAttachmentDiscoveryWireCursor(t, strings.Repeat("a", 64), 1)
		},
		"partial with empty cursor": func(root map[string]any) {
			root["qualification"], root["complete"], root["reason"], root["next_cursor"] = "partial", false, "item_limit", ""
		},
		"failed with empty reason": func(root map[string]any) {
			root["qualification"], root["complete"], root["reason"], root["attachments"], root["count"] = "failed", false, "", []any{}, 0
			delete(root, "total_size")
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			root := confluenceAttachmentDiscoveryWireDocument(t, "complete")
			mutate(root)
			encoded, _ := json.Marshal(root)
			if _, err := DecodeConfluenceAttachmentDiscoveryView(bytes.NewReader(encoded)); err == nil {
				t.Fatal("invalid discovery wire passed")
			}
		})
	}

	duplicate := bytes.Replace(confluenceAttachmentDiscoveryWireFixture(t, "complete"), []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
	if _, err := DecodeConfluenceAttachmentDiscoveryView(bytes.NewReader(duplicate)); err == nil {
		t.Fatal("duplicate root member passed")
	}
	oversized := bytes.Repeat([]byte(" "), confluenceAttachmentDiscoveryWireMaxBytes+1)
	if _, err := DecodeConfluenceAttachmentDiscoveryView(bytes.NewReader(oversized)); err == nil {
		t.Fatal("oversized discovery wire passed")
	}
}

func TestDecodeConfluenceAttachmentDiscoveryViewRejectsUnboundContinuation(t *testing.T) {
	for name, cursor := range map[string]string{
		"wrong scope": confluenceAttachmentDiscoveryWireCursor(t, strings.Repeat("b", 64), 1),
		"wrong start": confluenceAttachmentDiscoveryWireCursor(t, strings.Repeat("a", 64), 2),
		"padded":      confluenceAttachmentDiscoveryWireCursor(t, strings.Repeat("a", 64), 1) + "=",
	} {
		t.Run(name, func(t *testing.T) {
			root := confluenceAttachmentDiscoveryWireDocument(t, "partial")
			root["complete"] = false
			root["reason"] = "item_limit"
			root["next_cursor"] = cursor
			encoded, _ := json.Marshal(root)
			if _, err := DecodeConfluenceAttachmentDiscoveryView(bytes.NewReader(encoded)); err == nil {
				t.Fatal("unbound continuation passed")
			}
		})
	}
}

func confluenceAttachmentDiscoveryWireFixture(t *testing.T, qualification string) []byte {
	t.Helper()
	encoded, err := json.Marshal(confluenceAttachmentDiscoveryWireDocument(t, qualification))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func confluenceAttachmentDiscoveryWireDocument(_ *testing.T, qualification string) map[string]any {
	return map[string]any{
		"schema_version": 1,
		"qualification":  qualification,
		"complete":       true,
		"consistency":    "live_unproven",
		"scope_sha256":   strings.Repeat("a", 64),
		"start_offset":   0,
		"count":          1,
		"total_size":     1,
		"bounds": map[string]any{
			"max_items": 1, "max_requests": 2, "max_response_bytes": 65536,
			"deadline_ms": 5000, "requests_used": 1, "response_bytes_used": 512,
		},
		"attachments": []any{map[string]any{
			"id": "att_A-21", "title": "diagram.png", "type": "attachment", "version": 3,
			"container_id": "page_root-10", "container_type": "page", "container_version": 7,
			"space": "DOC", "media_type": "image/png", "file_size": 42,
		}},
	}
}

func confluenceAttachmentDiscoveryWireCursor(t *testing.T, scope string, start int) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"schema_version": 1, "scope_sha256": scope, "start": start})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}
