package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeConfluenceSpaceTreeViewReconcilesQualification(t *testing.T) {
	view, err := DecodeConfluenceSpaceTreeView(bytes.NewReader(confluenceSpaceTreeWireFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	if !view.Complete || view.Count != 2 || view.Pages[1].Parent != "root_A-10" {
		t.Fatalf("view=%+v", view)
	}
	partial := confluenceSpaceTreeWireDocument()
	partial["complete"], partial["truncated"], partial["partial_reason"] = false, true, "item_limit"
	encoded, _ := json.Marshal(partial)
	view, err = DecodeConfluenceSpaceTreeView(bytes.NewReader(encoded))
	if err != nil || view.Complete || !view.Truncated || view.PartialReason != "item_limit" {
		t.Fatalf("partial=%+v err=%v", view, err)
	}
}

func TestDecodeConfluenceSpaceTreeViewRejectsOpenOrContradictoryWire(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"unknown root": func(root map[string]any) { root["cursor"] = "unsafe" },
		"missing root": func(root map[string]any) { delete(root, "complete") },
		"null root":    func(root map[string]any) { root["space"] = nil },
		"oversize space": func(root map[string]any) {
			root["space"] = strings.Repeat("s", 256)
		},
		"unknown bound": func(root map[string]any) { root["bounds"].(map[string]any)["other"] = 1 },
		"missing bound": func(root map[string]any) { delete(root["bounds"].(map[string]any), "scanned_items") },
		"null pages":    func(root map[string]any) { root["pages"] = nil },
		"unknown page": func(root map[string]any) {
			root["pages"].([]any)[0].(map[string]any)["url"] = "https://example.invalid"
		},
		"missing page field": func(root map[string]any) {
			delete(root["pages"].([]any)[0].(map[string]any), "version")
		},
		"null parent": func(root map[string]any) {
			root["pages"].([]any)[1].(map[string]any)["parent"] = nil
		},
		"explicit empty root parent": func(root map[string]any) {
			root["pages"].([]any)[0].(map[string]any)["parent"] = ""
		},
		"count mismatch":           func(root map[string]any) { root["count"] = 1 },
		"complete partial":         func(root map[string]any) { root["partial_reason"] = "item_limit" },
		"complete empty partial":   func(root map[string]any) { root["partial_reason"] = "" },
		"complete false truncated": func(root map[string]any) { root["truncated"] = false },
		"partial no reason": func(root map[string]any) {
			root["complete"], root["truncated"] = false, true
		},
		"partial false truncated": func(root map[string]any) {
			root["complete"], root["truncated"], root["partial_reason"] = false, false, "item_limit"
		},
		"partial empty reason": func(root map[string]any) {
			root["complete"], root["truncated"], root["partial_reason"] = false, true, ""
		},
		"foreign space":   func(root map[string]any) { root["pages"].([]any)[0].(map[string]any)["space"] = "OTHER" },
		"invalid page id": func(root map[string]any) { root["pages"].([]any)[0].(map[string]any)["id"] = "page.1" },
		"oversize parent id": func(root map[string]any) {
			root["pages"].([]any)[1].(map[string]any)["parent"] = strings.Repeat("p", 257)
		},
		"self parent": func(root map[string]any) {
			page := root["pages"].([]any)[1].(map[string]any)
			page["parent"] = page["id"]
		},
		"duplicate page": func(root map[string]any) {
			root["pages"] = append(root["pages"].([]any), root["pages"].([]any)[0])
			root["count"] = 3
			root["bounds"].(map[string]any)["scanned_items"] = 3
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			root := confluenceSpaceTreeWireDocument()
			mutate(root)
			encoded, _ := json.Marshal(root)
			if _, err := DecodeConfluenceSpaceTreeView(bytes.NewReader(encoded)); err == nil {
				t.Fatal("invalid tree wire passed")
			}
		})
	}
	duplicate := bytes.Replace(confluenceSpaceTreeWireFixture(t), []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
	if _, err := DecodeConfluenceSpaceTreeView(bytes.NewReader(duplicate)); err == nil {
		t.Fatal("duplicate root member passed")
	}
	invalidUTF8 := bytes.Replace(confluenceSpaceTreeWireFixture(t), []byte(`"space":"DOC"`), []byte{'"', 's', 'p', 'a', 'c', 'e', '"', ':', '"', 0xff, '"'}, 1)
	if _, err := DecodeConfluenceSpaceTreeView(bytes.NewReader(invalidUTF8)); err == nil {
		t.Fatal("invalid UTF-8 space passed")
	}
}

func confluenceSpaceTreeWireFixture(t *testing.T) []byte {
	t.Helper()
	encoded, err := json.Marshal(confluenceSpaceTreeWireDocument())
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func confluenceSpaceTreeWireDocument() map[string]any {
	return map[string]any{
		"schema_version": 1, "space": "DOC", "depth": 0, "count": 2,
		"complete": true, "consistency": "live_unproven",
		"bounds": map[string]any{
			"max_items": 10, "max_scanned_items": 20, "max_requests": 2,
			"max_response_bytes": 65536, "deadline_ms": 5000, "scanned_items": 2,
			"requests_used": 1, "response_bytes_used": 512,
		},
		"pages": []any{
			map[string]any{"id": "root_A-10", "title": "Root", "space": "DOC", "version": 3},
			map[string]any{"id": "child_B-11", "title": "Child", "space": "DOC", "version": 2, "parent": "root_A-10"},
		},
	}
}
