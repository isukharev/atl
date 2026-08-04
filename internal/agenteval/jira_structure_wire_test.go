package agenteval

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestDecodeJiraStructureMetadataAcceptsReleasedProjection(t *testing.T) {
	for _, name := range []string{"Synthetic structure", " Synthetic structure "} {
		input := JiraStructureMetadataView{SchemaVersion: 1, ID: 95, Name: name, ReadOnly: true}
		got, err := DecodeJiraStructureMetadata(bytes.NewReader(jiraStructureEncode(t, input)))
		if err != nil {
			t.Fatal(err)
		}
		if got != input {
			t.Fatalf("metadata drifted: got=%+v want=%+v", got, input)
		}
	}
}

func TestDecodeJiraStructureMetadataRejectsInvalidWire(t *testing.T) {
	valid := jiraStructureEncode(t, JiraStructureMetadataView{SchemaVersion: 1, ID: 95, Name: "Synthetic structure", ReadOnly: true})
	tests := []struct {
		name string
		data []byte
	}{
		{name: "schema", data: []byte(`{"schema_version":2,"id":95,"name":"Synthetic","read_only":true}`)},
		{name: "nonpositive id", data: []byte(`{"schema_version":1,"id":0,"name":"Synthetic","read_only":true}`)},
		{name: "blank name", data: []byte(`{"schema_version":1,"id":95,"name":" ","read_only":true}`)},
		{name: "missing", data: []byte(`{"schema_version":1,"id":95,"name":"Synthetic"}`)},
		{name: "null", data: []byte(`{"schema_version":1,"id":95,"name":null,"read_only":true}`)},
		{name: "unknown", data: []byte(`{"schema_version":1,"id":95,"name":"Synthetic","read_only":true,"owner":"private"}`)},
		{name: "duplicate", data: []byte(`{"schema_version":1,"id":95,"id":96,"name":"Synthetic","read_only":true}`)},
		{name: "trailing", data: append(bytes.Clone(valid), []byte(`{}`)...)},
		{name: "oversized", data: append(bytes.Clone(valid), bytes.Repeat([]byte(" "), jiraStructureMetadataWireMaxBytes-len(valid)+1)...)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeJiraStructureMetadata(bytes.NewReader(tc.data)); err == nil {
				t.Fatal("invalid metadata was accepted")
			}
		})
	}
}

func TestDecodeJiraStructureViewAcceptsFullAndSelectedProjections(t *testing.T) {
	for _, tc := range []struct {
		name string
		view JiraStructureView
	}{
		{name: "full", view: jiraStructureValidFullView()},
		{name: "selected and gated", view: jiraStructureValidSelectedView()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeJiraStructureView(bytes.NewReader(jiraStructureEncode(t, tc.view)))
			if err != nil {
				t.Fatalf("decode valid view: %v", err)
			}
			if got.Structure.ID != tc.view.Structure.ID || got.RowCount != len(tc.view.Rows) ||
				(got.Selection != nil) != (tc.view.Selection != nil) {
				t.Fatalf("view drifted: %+v", got)
			}
		})
	}
}

func TestDecodeJiraStructureViewAcceptsOpenFieldValues(t *testing.T) {
	view := jiraStructureValidFullView()
	view.Rows[1].Values["status"] = map[string]any{
		"name": "In Progress", "plugin": []any{nil, true, json.Number("7")},
	}
	if _, err := DecodeJiraStructureView(bytes.NewReader(jiraStructureEncode(t, view))); err != nil {
		t.Fatalf("open Jira field value was rejected: %v", err)
	}
}

func TestDecodeJiraStructureViewRejectsClosedMemberDrift(t *testing.T) {
	valid := jiraStructureEncode(t, jiraStructureValidFullView())
	var original map[string]any
	if err := json.Unmarshal(valid, &original); err != nil {
		t.Fatal(err)
	}
	mutate := func(fn func(map[string]any)) []byte {
		encoded := jiraStructureEncode(t, original)
		var clone map[string]any
		if err := json.Unmarshal(encoded, &clone); err != nil {
			t.Fatal(err)
		}
		fn(clone)
		return jiraStructureEncode(t, clone)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown root", data: mutate(func(doc map[string]any) { doc["owner"] = "private" })},
		{name: "missing root", data: mutate(func(doc map[string]any) { delete(doc, "row_count") })},
		{name: "null rows", data: mutate(func(doc map[string]any) { doc["rows"] = nil })},
		{name: "null selection", data: mutate(func(doc map[string]any) { doc["selection"] = nil })},
		{name: "unknown row", data: mutate(func(doc map[string]any) { doc["rows"].([]any)[0].(map[string]any)["label"] = "private" })},
		{name: "null values", data: mutate(func(doc map[string]any) { doc["rows"].([]any)[0].(map[string]any)["values"] = nil })},
		{name: "null attributes", data: mutate(func(doc map[string]any) { doc["projection"].(map[string]any)["attributes"] = nil })},
		{name: "duplicate", data: []byte(strings.Replace(string(valid), `"row_count":`, `"row_count":2,"row_count":`, 1))},
		{name: "trailing", data: append(bytes.Clone(valid), []byte(`true`)...)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeJiraStructureView(bytes.NewReader(tc.data)); err == nil {
				t.Fatal("invalid wire was accepted")
			}
		})
	}
}

func TestDecodeJiraStructureViewRejectsReconciliationFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*JiraStructureView)
	}{
		{name: "schema", mutate: func(view *JiraStructureView) { view.SchemaVersion = 2 }},
		{name: "identity", mutate: func(view *JiraStructureView) { view.Structure.ID = 0 }},
		{name: "forest version", mutate: func(view *JiraStructureView) { view.ForestVersion.Version = -1 }},
		{name: "gated zero signature", mutate: func(view *JiraStructureView) {
			view.ForestVersionGated = true
			view.ForestVersion.Signature = 0
		}},
		{name: "projection kind", mutate: func(view *JiraStructureView) { view.Projection.Kind = "browser-view" }},
		{name: "projection source", mutate: func(view *JiraStructureView) { view.Projection.Source = "default" }},
		{name: "projection preset", mutate: func(view *JiraStructureView) { view.Projection.View = "" }},
		{name: "projection browser", mutate: func(view *JiraStructureView) { view.Projection.BrowserViewReproduced = true }},
		{name: "projection duplicate", mutate: func(view *JiraStructureView) {
			view.Projection.Attributes[1] = view.Projection.Attributes[0]
		}},
		{name: "row count", mutate: func(view *JiraStructureView) { view.RowCount++ }},
		{name: "issue count", mutate: func(view *JiraStructureView) { view.IssueCount++ }},
		{name: "duplicate row", mutate: func(view *JiraStructureView) {
			view.Rows[1].RowID = view.Rows[0].RowID
		}},
		{name: "row ordering", mutate: func(view *JiraStructureView) { view.Rows[1].Position = 0 }},
		{name: "row values count", mutate: func(view *JiraStructureView) { delete(view.Rows[1].Values, "status") }},
		{name: "inaccessible unknown", mutate: func(view *JiraStructureView) {
			view.Complete = false
			view.InaccessibleRows = []int64{999}
		}},
		{name: "inaccessible marked accessible", mutate: func(view *JiraStructureView) {
			view.Complete = false
			view.InaccessibleRows = []int64{view.Rows[1].RowID}
		}},
		{name: "unlisted inaccessible", mutate: func(view *JiraStructureView) {
			view.Complete = false
			view.Rows[1].Accessible = false
		}},
		{name: "complete warning", mutate: func(view *JiraStructureView) { view.Warnings = []string{"partial labels"} }},
		{name: "incomplete without evidence", mutate: func(view *JiraStructureView) { view.Complete = false }},
		{name: "duplicate warning", mutate: func(view *JiraStructureView) {
			view.Complete = false
			view.Warnings = []string{"partial labels", "partial labels"}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := jiraStructureValidFullView()
			tc.mutate(&view)
			if _, err := DecodeJiraStructureView(bytes.NewReader(jiraStructureEncode(t, view))); err == nil {
				t.Fatal("invalid reconciliation was accepted")
			}
		})
	}
}

func TestDecodeJiraStructureViewRejectsSelectionFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*JiraStructureView)
	}{
		{name: "kind", mutate: func(view *JiraStructureView) { view.Selection.Kind = "approximate" }},
		{name: "folder id", mutate: func(view *JiraStructureView) { view.Selection.FolderID = "" }},
		{name: "row id", mutate: func(view *JiraStructureView) { view.Selection.RowID++ }},
		{name: "path", mutate: func(view *JiraStructureView) { view.Selection.Path = []string{"Plan", ""} }},
		{name: "root relative depth", mutate: func(view *JiraStructureView) { *view.Rows[0].RelativeDepth = 1 }},
		{name: "descendant relative depth", mutate: func(view *JiraStructureView) { *view.Rows[1].RelativeDepth = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := jiraStructureValidSelectedView()
			tc.mutate(&view)
			if _, err := DecodeJiraStructureView(bytes.NewReader(jiraStructureEncode(t, view))); err == nil {
				t.Fatal("invalid selection was accepted")
			}
		})
	}
}

func TestDecodeJiraStructureViewEnforcesRowAndByteBounds(t *testing.T) {
	view := jiraStructureValidFullView()
	template := view.Rows[1]
	view.Rows = view.Rows[:1]
	for index := 0; index < jiraStructureViewMaxRows-1; index++ {
		row := template
		row.RowID = int64(index + 1000)
		row.ItemID = strconv.Itoa(index + 10000)
		row.Position = index + 1
		view.Rows = append(view.Rows, row)
	}
	view.RowCount = len(view.Rows)
	view.IssueCount = jiraStructureViewMaxRows - 1
	if _, err := DecodeJiraStructureView(bytes.NewReader(jiraStructureEncode(t, view))); err != nil {
		t.Fatalf("exact row bound was rejected: %v", err)
	}
	overRow := template
	overRow.RowID = 3000
	overRow.ItemID = "99999"
	overRow.Position = jiraStructureViewMaxRows
	view.Rows = append(view.Rows, overRow)
	view.RowCount++
	view.IssueCount++
	if _, err := DecodeJiraStructureView(bytes.NewReader(jiraStructureEncode(t, view))); err == nil {
		t.Fatal("more than 1000 rows were accepted")
	}

	valid := jiraStructureEncode(t, jiraStructureValidFullView())
	atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), jiraStructureViewWireMaxBytes-len(valid))...)
	if _, err := DecodeJiraStructureView(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact byte bound was rejected: %v", err)
	}
	if _, err := DecodeJiraStructureView(bytes.NewReader(append(atLimit, ' '))); err == nil {
		t.Fatal("oversized view was accepted")
	}
}

func jiraStructureValidFullView() JiraStructureView {
	attributes := []string{"key", "summary", "status", "assignee"}
	return JiraStructureView{
		SchemaVersion: 1,
		Structure:     JiraStructureIdentity{ID: 95, Name: "Synthetic structure", ReadOnly: true},
		ForestVersion: JiraStructureForestVersion{Signature: 9501, Version: 21},
		Projection: JiraStructureProjection{
			Kind: "jira-fields-v1", Source: "explicit", Attributes: attributes, View: "explicit",
		},
		Rows: []JiraStructureRow{
			{
				RowID: 100, Depth: 0, ItemType: "folder", ItemID: "folder-a", Position: 0, Accessible: true,
				Values: map[string]any{"key": nil, "summary": "Plan", "status": nil, "assignee": nil},
			},
			{
				RowID: 101, Depth: 1, ParentRowID: 100, ItemType: "issue", ItemID: "20001", Position: 1, Accessible: true,
				Values: map[string]any{"key": "SYN-1", "summary": "Task", "status": "Open", "assignee": nil},
			},
		},
		RowCount: 2, IssueCount: 1, Complete: true,
		InaccessibleRows: []int64{}, Warnings: []string{},
	}
}

func jiraStructureValidSelectedView() JiraStructureView {
	view := jiraStructureValidFullView()
	zero, one := 0, 1
	view.Rows[0].RelativeDepth = &zero
	view.Rows[1].RelativeDepth = &one
	view.ForestVersionGated = true
	view.Selection = &JiraStructureSelection{
		Kind: "folder-row", FolderID: "folder-a", RowID: 100, Path: []string{"Plan"},
	}
	return view
}

func jiraStructureEncode(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
