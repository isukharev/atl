package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeConfluenceCommentViewsAcceptReleasedCompleteAndPartialShapes(t *testing.T) {
	list, err := DecodeConfluenceCommentListView(bytes.NewReader(validConfluenceCommentListWire(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	if !list.Complete || list.Count != 2 || list.RootCount != 1 || list.Comments == nil || list.Diagnostics == nil || list.PartialReasons == nil {
		t.Fatalf("complete list = %+v", list)
	}
	if list.Comments[0].ParentID != nil || list.Comments[0].RootID == nil || list.Comments[0].Anchor == nil || list.Comments[1].Anchor != nil {
		t.Fatalf("required nullable list members = %+v", list.Comments)
	}

	partialList, err := DecodeConfluenceCommentListView(bytes.NewReader(validConfluenceCommentListWire(t, true)))
	if err != nil {
		t.Fatal(err)
	}
	if partialList.Complete || partialList.CommentsComplete || partialList.ThreadsComplete || !partialList.AnchorsComplete ||
		len(partialList.PartialReasons) != 1 || partialList.PartialReasons[0] != "item_limit" {
		t.Fatalf("partial list = %+v", partialList)
	}

	thread, err := DecodeConfluenceCommentThreadView(bytes.NewReader(validConfluenceCommentThreadWire(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	if !thread.Complete || thread.Query.CommentID != "101" || thread.Comments[0].BodyText == nil ||
		*thread.Comments[0].BodyText != "" || thread.Comments[1].BodyText == nil || thread.Comments[1].Anchor != nil {
		t.Fatalf("complete thread = %+v", thread)
	}

	partialThread, err := DecodeConfluenceCommentThreadView(bytes.NewReader(validConfluenceCommentThreadWire(t, true)))
	if err != nil {
		t.Fatal(err)
	}
	if partialThread.Complete || partialThread.CommentsComplete || partialThread.ThreadsComplete ||
		!partialThread.AnchorsComplete || partialThread.RootCount != 0 || len(partialThread.Comments) != 1 ||
		partialThread.Comments[0].ParentID != nil || partialThread.Comments[0].RootID != nil || partialThread.Comments[0].BodyText != nil {
		t.Fatalf("partial thread = %+v", partialThread)
	}
}

func TestDecodeConfluenceCommentViewsRejectWireDrift(t *testing.T) {
	tests := []struct {
		name   string
		valid  []byte
		decode func([]byte) error
	}{
		{
			name: "list", valid: validConfluenceCommentListWire(t, false),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceCommentListView(bytes.NewReader(data))
				return err
			},
		},
		{
			name: "thread", valid: validConfluenceCommentThreadWire(t, false),
			decode: func(data []byte) error {
				_, err := DecodeConfluenceCommentThreadView(bytes.NewReader(data))
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := map[string][]byte{
				"unknown root": mutateConfluenceCommentWire(t, test.valid, func(root map[string]any) {
					root["backend_payload"] = true
				}),
				"unknown query": mutateConfluenceCommentWire(t, test.valid, func(root map[string]any) {
					root["query"].(map[string]any)["private"] = true
				}),
				"unknown bounds": mutateConfluenceCommentWire(t, test.valid, func(root map[string]any) {
					root["bounds"].(map[string]any)["private"] = true
				}),
				"unknown capabilities": mutateConfluenceCommentWire(t, test.valid, func(root map[string]any) {
					root["capabilities"].(map[string]any)["private"] = true
				}),
				"unknown record": mutateConfluenceCommentWire(t, test.valid, func(root map[string]any) {
					firstConfluenceCommentRecord(root)["body_storage"] = "not released"
				}),
				"unknown author": mutateConfluenceCommentWire(t, test.valid, func(root map[string]any) {
					firstConfluenceCommentRecord(root)["author"].(map[string]any)["email"] = "not released"
				}),
				"unknown anchor": mutateConfluenceCommentWire(t, test.valid, func(root map[string]any) {
					firstConfluenceCommentRecord(root)["anchor"].(map[string]any)["selection"] = "not released"
				}),
				"unknown diagnostic": mutateConfluenceCommentWire(t, test.valid, func(root map[string]any) {
					root["diagnostics"] = []any{map[string]any{"code": "item_limit", "private": true}}
				}),
				"missing required": mutateConfluenceCommentWire(t, test.valid, func(root map[string]any) {
					delete(root, "schema_version")
				}),
				"trailing value": append(bytes.Clone(test.valid), []byte("\n{}")...),
			}
			duplicate := bytes.Replace(test.valid, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
			if bytes.Equal(duplicate, test.valid) {
				t.Fatal("duplicate mutation did not apply")
			}
			invalid["duplicate member"] = duplicate
			for name, data := range invalid {
				t.Run(name, func(t *testing.T) {
					if err := test.decode(data); err == nil {
						t.Fatal("invalid wire was accepted")
					}
				})
			}
		})
	}
}

func TestDecodeConfluenceCommentViewsRejectNullOutsideReleasedNullableMembers(t *testing.T) {
	valid := validConfluenceCommentListWire(t, false)
	mutations := map[string]func(map[string]any){
		"root":           func(root map[string]any) { root["page_id"] = nil },
		"query":          func(root map[string]any) { root["query"].(map[string]any)["state"] = nil },
		"bounds":         func(root map[string]any) { root["bounds"].(map[string]any)["max_items"] = nil },
		"capability":     func(root map[string]any) { root["capabilities"].(map[string]any)["footer"] = nil },
		"reasons array":  func(root map[string]any) { root["partial_reasons"] = nil },
		"reason item":    func(root map[string]any) { root["partial_reasons"] = []any{nil} },
		"comments array": func(root map[string]any) { root["comments"] = nil },
		"comment item":   func(root map[string]any) { root["comments"] = []any{nil} },
		"record":         func(root map[string]any) { firstConfluenceCommentRecord(root)["id"] = nil },
		"author":         func(root map[string]any) { firstConfluenceCommentRecord(root)["author"].(map[string]any)["id"] = nil },
		"anchor": func(root map[string]any) {
			firstConfluenceCommentRecord(root)["anchor"].(map[string]any)["status"] = nil
		},
		"diagnostics array": func(root map[string]any) { root["diagnostics"] = nil },
		"diagnostic item":   func(root map[string]any) { root["diagnostics"] = []any{nil} },
		"diagnostic code":   func(root map[string]any) { root["diagnostics"] = []any{map[string]any{"code": nil}} },
		"diagnostic optional": func(root map[string]any) {
			root["diagnostics"] = []any{map[string]any{"code": "item_limit", "comment_id": nil}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := mutateConfluenceCommentWire(t, valid, mutate)
			if _, err := DecodeConfluenceCommentListView(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid null was accepted")
			}
		})
	}

	thread := mutateConfluenceCommentWire(t, validConfluenceCommentThreadWire(t, false), func(root map[string]any) {
		firstConfluenceCommentRecord(root)["body_text"] = nil
	})
	if _, err := DecodeConfluenceCommentThreadView(bytes.NewReader(thread)); err == nil {
		t.Fatal("unqualified null body_text was accepted")
	}
}

func TestDecodeConfluenceCommentViewsRejectClosedVocabularyAndAccountingContradictions(t *testing.T) {
	valid := validConfluenceCommentListWire(t, false)
	mutations := map[string]func(map[string]any){
		"schema":                 func(root map[string]any) { root["schema_version"] = 2 },
		"page id":                func(root map[string]any) { root["page_id"] = "042" },
		"page version":           func(root map[string]any) { root["page_version"] = 0 },
		"query mode":             func(root map[string]any) { root["query"].(map[string]any)["mode"] = "inventory" },
		"query location":         func(root map[string]any) { root["query"].(map[string]any)["location"] = "backend" },
		"query state":            func(root map[string]any) { root["query"].(map[string]any)["state"] = "deleted" },
		"query depth":            func(root map[string]any) { root["query"].(map[string]any)["depth"] = "recursive" },
		"list comment selection": func(root map[string]any) { root["query"].(map[string]any)["comment_id"] = "101" },
		"page bound":             func(root map[string]any) { root["bounds"].(map[string]any)["max_comment_pages"] = 31 },
		"item minimum":           func(root map[string]any) { root["bounds"].(map[string]any)["max_items"] = 0 },
		"item maximum":           func(root map[string]any) { root["bounds"].(map[string]any)["max_items"] = 1001 },
		"byte minimum":           func(root map[string]any) { root["bounds"].(map[string]any)["max_bytes"] = 1023 },
		"byte maximum":           func(root map[string]any) { root["bounds"].(map[string]any)["max_bytes"] = 1048577 },
		"count":                  func(root map[string]any) { root["count"] = 1 },
		"root count":             func(root map[string]any) { root["root_count"] = 0 },
		"duplicate id": func(root map[string]any) {
			comments := root["comments"].([]any)
			comments[1].(map[string]any)["id"] = "101"
		},
		"record id":  func(root map[string]any) { firstConfluenceCommentRecord(root)["id"] = "0101" },
		"relation":   func(root map[string]any) { firstConfluenceCommentRecord(root)["relation"] = "child" },
		"location":   func(root map[string]any) { firstConfluenceCommentRecord(root)["location"] = "resolved" },
		"resolution": func(root map[string]any) { firstConfluenceCommentRecord(root)["resolution"] = "closed" },
		"version":    func(root map[string]any) { firstConfluenceCommentRecord(root)["version"] = -1 },
		"timestamp":  func(root map[string]any) { firstConfluenceCommentRecord(root)["created_at"] = "yesterday" },
		"author email": func(root map[string]any) {
			firstConfluenceCommentRecord(root)["author"].(map[string]any)["id"] = "reader@example.invalid"
		},
		"anchor marker": func(root map[string]any) {
			firstConfluenceCommentRecord(root)["anchor"].(map[string]any)["marker_ref"] = "bad marker"
		},
		"anchor status": func(root map[string]any) {
			firstConfluenceCommentRecord(root)["anchor"].(map[string]any)["status"] = "backend"
		},
		"unqualified anchor": func(root map[string]any) {
			firstConfluenceCommentRecord(root)["anchor"].(map[string]any)["status"] = "missing"
		},
		"capability":     func(root map[string]any) { root["capabilities"].(map[string]any)["footer"] = "available" },
		"partial reason": func(root map[string]any) { root["partial_reasons"] = []any{"backend"}; root["complete"] = false },
		"unsorted reasons": func(root map[string]any) {
			root["partial_reasons"] = []any{"page_limit", "item_limit"}
			root["complete"], root["comments_complete"], root["threads_complete"] = false, false, false
		},
		"duplicate reason": func(root map[string]any) {
			root["partial_reasons"] = []any{"item_limit", "item_limit"}
			root["complete"], root["comments_complete"], root["threads_complete"] = false, false, false
		},
		"complete with reason": func(root map[string]any) { root["partial_reasons"] = []any{"item_limit"} },
		"flags without reason": func(root map[string]any) { root["comments_complete"] = false },
		"diagnostic code":      func(root map[string]any) { root["diagnostics"] = []any{map[string]any{"code": "backend"}} },
		"diagnostic selector": func(root map[string]any) {
			root["diagnostics"] = []any{map[string]any{"code": "orphan_marker", "selector": "all"}}
		},
		"diagnostic partial mismatch": func(root map[string]any) { root["diagnostics"] = []any{map[string]any{"code": "item_limit"}} },
		"empty diagnostic optional": func(root map[string]any) {
			root["diagnostics"] = []any{map[string]any{"code": "orphan_marker", "marker_ref": ""}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := mutateConfluenceCommentWire(t, valid, mutate)
			if _, err := DecodeConfluenceCommentListView(bytes.NewReader(data)); err == nil {
				t.Fatal("contradictory list was accepted")
			}
		})
	}
}

func TestDecodeConfluenceCommentThreadRejectsIdentityAncestryScopeAndBodyContradictions(t *testing.T) {
	valid := validConfluenceCommentThreadWire(t, false)
	mutations := map[string]func(map[string]any){
		"query selection":    func(root map[string]any) { root["query"].(map[string]any)["comment_id"] = "999" },
		"query id":           func(root map[string]any) { root["query"].(map[string]any)["comment_id"] = "0101" },
		"noncanonical query": func(root map[string]any) { root["query"].(map[string]any)["depth"] = "root" },
		"missing parent":     func(root map[string]any) { root["comments"].([]any)[1].(map[string]any)["parent_id"] = "999" },
		"cycle": func(root map[string]any) {
			comments := root["comments"].([]any)
			comments[1].(map[string]any)["parent_id"] = "103"
			comments = append(comments, map[string]any{
				"id": "103", "parent_id": "102", "root_id": "101", "relation": "reply", "location": "inline",
				"resolution": "open", "version": 1, "author": map[string]any{"id": "user-3", "display_name": "Third"},
				"created_at": "", "updated_at": "", "body_text": "third", "anchor": nil,
			})
			root["comments"], root["count"] = comments, 3
		},
		"unrelated root": func(root map[string]any) {
			comments := root["comments"].([]any)
			comments = append(comments, map[string]any{
				"id": "201", "parent_id": nil, "root_id": "201", "relation": "root", "location": "footer",
				"resolution": "open", "version": 1, "author": map[string]any{"id": "user-3", "display_name": "Third"},
				"created_at": "", "updated_at": "", "body_text": "third", "anchor": nil,
			})
			root["comments"], root["count"], root["root_count"] = comments, 3, 2
		},
		"unqualified body": func(root map[string]any) { firstConfluenceCommentRecord(root)["body_text"] = nil },
		"unrelated diagnostic": func(root map[string]any) {
			root["diagnostics"] = []any{map[string]any{"code": "original_selection_changed", "comment_id": "999", "marker_ref": "marker-1"}}
		},
		"orphan diagnostic": func(root map[string]any) {
			root["diagnostics"] = []any{map[string]any{"code": "orphan_marker", "marker_ref": "marker-1"}}
		},
		"mismatched marker": func(root map[string]any) {
			root["diagnostics"] = []any{map[string]any{"code": "original_selection_changed", "comment_id": "101", "marker_ref": "other"}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			data := mutateConfluenceCommentWire(t, valid, mutate)
			if _, err := DecodeConfluenceCommentThreadView(bytes.NewReader(data)); err == nil {
				t.Fatal("contradictory thread was accepted")
			}
		})
	}
}

func TestDecodeConfluenceCommentViewsHonorExactOneMiBWireLimit(t *testing.T) {
	valid := validConfluenceCommentListWire(t, false)
	if len(valid) >= confluenceCommentWireMaxBytes {
		t.Fatalf("fixture unexpectedly has %d bytes", len(valid))
	}
	atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), confluenceCommentWireMaxBytes-len(valid))...)
	if _, err := DecodeConfluenceCommentListView(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact wire limit rejected: %v", err)
	}
	overLimit := append(bytes.Clone(atLimit), ' ')
	if _, err := DecodeConfluenceCommentListView(bytes.NewReader(overLimit)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize error = %v", err)
	}
}

func validConfluenceCommentListWire(t *testing.T, partial bool) []byte {
	t.Helper()
	rootID, replyParentID, replyRootID := "101", "101", "101"
	view := ConfluenceCommentListView{
		SchemaVersion: ConfluenceCommentViewSchemaVersion,
		PageID:        "42", PageVersion: 7, PageVersionGated: true,
		Query:    ConfluenceCommentQuery{Mode: "list", Location: "all", State: "all", Depth: "all"},
		Bounds:   ConfluenceCommentViewBounds{MaxCommentPages: 32, MaxItems: 100, MaxBytes: 1 << 20},
		Complete: true, CommentsComplete: true, ThreadsComplete: true, AnchorsComplete: true,
		Count: 2, RootCount: 1, PartialReasons: []string{}, Capabilities: validConfluenceCommentCapabilities(),
		Comments: []ConfluenceCommentListViewRecord{
			{
				ID: "101", RootID: &rootID, Relation: "root", Location: "inline", Resolution: "open", Version: 1,
				Author: ConfluenceCommentAuthor{ID: "user-1", DisplayName: "Reader"}, CreatedAt: "2026-01-01T00:00:00Z",
				UpdatedAt: "2026-01-02T00:00:00+0000", Anchor: &ConfluenceCommentViewAnchor{MarkerRef: "marker-1", Status: "matched"},
			},
			{
				ID: "102", ParentID: &replyParentID, RootID: &replyRootID, Relation: "reply", Location: "inline", Resolution: "open", Version: 2,
				Author: ConfluenceCommentAuthor{ID: "user-2", DisplayName: "Writer"}, CreatedAt: "", UpdatedAt: "", Anchor: nil,
			},
		},
		Diagnostics: []ConfluenceCommentViewDiagnostic{},
	}
	if partial {
		view.Complete, view.CommentsComplete, view.ThreadsComplete = false, false, false
		view.PartialReasons = []string{"item_limit"}
		view.Diagnostics = []ConfluenceCommentViewDiagnostic{{Code: "item_limit", Selector: "footer"}}
	}
	return marshalConfluenceCommentWire(t, view)
}

func validConfluenceCommentThreadWire(t *testing.T, partial bool) []byte {
	t.Helper()
	if partial {
		view := ConfluenceCommentThreadView{
			SchemaVersion: ConfluenceCommentViewSchemaVersion,
			PageID:        "42", PageVersion: 7, PageVersionGated: false,
			Query:    ConfluenceCommentQuery{Mode: "thread", Location: "all", State: "all", Depth: "all", CommentID: "301"},
			Bounds:   ConfluenceCommentViewBounds{MaxCommentPages: 32, MaxItems: 100, MaxBytes: 1 << 20},
			Complete: false, CommentsComplete: false, ThreadsComplete: false, AnchorsComplete: true,
			Count: 1, RootCount: 0,
			PartialReasons: []string{"comment_body_unavailable", "parent_unavailable"},
			Capabilities:   validConfluenceCommentCapabilities(),
			Comments: []ConfluenceCommentThreadViewRecord{{
				ID: "301", Relation: "unknown", Location: "footer", Resolution: "unknown", Version: 0,
				Author: ConfluenceCommentAuthor{ID: "", DisplayName: ""}, CreatedAt: "", UpdatedAt: "", BodyText: nil, Anchor: nil,
			}},
			Diagnostics: []ConfluenceCommentViewDiagnostic{},
		}
		return marshalConfluenceCommentWire(t, view)
	}
	rootID, replyParentID, replyRootID := "101", "101", "101"
	emptyBody, replyBody := "", "reply text with reader@example.invalid and https://example.invalid"
	view := ConfluenceCommentThreadView{
		SchemaVersion: ConfluenceCommentViewSchemaVersion,
		PageID:        "42", PageVersion: 7, PageVersionGated: true,
		Query:    ConfluenceCommentQuery{Mode: "thread", Location: "all", State: "all", Depth: "all", CommentID: "101"},
		Bounds:   ConfluenceCommentViewBounds{MaxCommentPages: 32, MaxItems: 100, MaxBytes: 1 << 20},
		Complete: true, CommentsComplete: true, ThreadsComplete: true, AnchorsComplete: true,
		Count: 2, RootCount: 1, PartialReasons: []string{}, Capabilities: validConfluenceCommentCapabilities(),
		Comments: []ConfluenceCommentThreadViewRecord{
			{
				ID: "101", RootID: &rootID, Relation: "root", Location: "inline", Resolution: "open", Version: 1,
				Author: ConfluenceCommentAuthor{ID: "user-1", DisplayName: "Reader"}, CreatedAt: "2026-01-01T00:00:00Z",
				UpdatedAt: "2026-01-02T00:00:00+0000", BodyText: &emptyBody,
				Anchor: &ConfluenceCommentViewAnchor{MarkerRef: "marker-1", Status: "matched"},
			},
			{
				ID: "102", ParentID: &replyParentID, RootID: &replyRootID, Relation: "reply", Location: "inline", Resolution: "open", Version: 2,
				Author: ConfluenceCommentAuthor{ID: "user-2", DisplayName: "Writer"}, CreatedAt: "", UpdatedAt: "", BodyText: &replyBody, Anchor: nil,
			},
		},
		Diagnostics: []ConfluenceCommentViewDiagnostic{},
	}
	return marshalConfluenceCommentWire(t, view)
}

func validConfluenceCommentCapabilities() ConfluenceCommentCapabilities {
	return ConfluenceCommentCapabilities{
		Footer: "observed", Inline: "observed", Resolved: "documented", DepthAll: "observed",
		ThreadAncestry: "observed", InlineProperties: "observed", Resolution: "documented",
	}
}

func mutateConfluenceCommentWire(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	return marshalConfluenceCommentWire(t, root)
}

func firstConfluenceCommentRecord(root map[string]any) map[string]any {
	return root["comments"].([]any)[0].(map[string]any)
}

func marshalConfluenceCommentWire(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
