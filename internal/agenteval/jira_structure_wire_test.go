package agenteval

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestDecodeJiraStructureFailureAcceptsReleasedRecoverableProjection(t *testing.T) {
	for _, test := range []struct {
		want JiraStructureFailureView
		raw  []byte
	}{
		{
			want: JiraStructureFailureView{
				Kind: "not_found", Remediation: "view_then_select_subtree",
				Message:   "selected Jira Structure folder was not found; available stored-folder count is 4",
				Available: 4,
			},
			raw: jiraStructureFailureInput(t, "not_found", 4, nil),
		},
		{
			want: JiraStructureFailureView{
				Kind: "check_failed", Remediation: "view_then_select_subtree",
				Message:   "Jira Structure folder selector is ambiguous; matching stored-folder count is 2 and available stored-folder count is 4",
				Available: 4, Matches: 2,
			},
			raw: jiraStructureFailureInput(t, "check_failed", 4, jiraStructureInt(2)),
		},
	} {
		got, err := DecodeJiraStructureFailure(bytes.NewReader(test.raw))
		if err != nil {
			t.Fatalf("decode valid %q failure: %v", test.want.Kind, err)
		}
		if got != test.want {
			t.Fatalf("failure drifted: got=%+v want=%+v", got, test.want)
		}
	}
}

func TestDecodeJiraStructureFailureRejectsInvalidWire(t *testing.T) {
	valid := jiraStructureFailureInput(t, "not_found", 4, nil)
	atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), jiraStructureFailureWireMaxBytes-len(valid))...)
	if _, err := DecodeJiraStructureFailure(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact byte bound was rejected: %v", err)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{name: "oversized", data: append(atLimit, ' ')},
		{name: "missing kind", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { delete(doc, "kind") })},
		{name: "missing remediation", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { delete(doc, "remediation") })},
		{name: "missing message", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { delete(doc, "message") })},
		{name: "missing recovery", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { delete(doc, "recovery") })},
		{name: "unknown member", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { doc["available"] = 4 })},
		{name: "null kind", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { doc["kind"] = nil })},
		{name: "null remediation", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { doc["remediation"] = nil })},
		{name: "null message", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { doc["message"] = nil })},
		{name: "null recovery", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { doc["recovery"] = nil })},
		{name: "root duplicate", data: []byte(strings.Replace(string(valid), `"kind":`, `"kind":"not_found","kind":`, 1))},
		{name: "recursive duplicate", data: []byte(strings.Replace(string(valid), `"action":`, `"action":"reread_then_reselect","action":`, 1))},
		{name: "trailing", data: append(bytes.Clone(valid), []byte(`{}`)...)},
		{name: "invalid UTF-8", data: append(bytes.Clone(valid[:len(valid)-2]), 0xff, '"', '}')},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeJiraStructureFailure(bytes.NewReader(tc.data)); err == nil {
				t.Fatal("invalid failure wire was accepted")
			} else if tc.name == "recursive duplicate" && !strings.Contains(err.Error(), "duplicate key") {
				t.Fatalf("recursive duplicate was not rejected by duplicate-key validation: %v", err)
			}
		})
	}
}

func TestDecodeJiraStructureFailureRejectsSemanticDrift(t *testing.T) {
	valid := jiraStructureFailureInput(t, "not_found", 4, nil)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "empty kind", mutate: func(doc map[string]any) { doc["kind"] = "" }},
		{name: "unnormalized kind", mutate: func(doc map[string]any) { doc["kind"] = " not_found" }},
		{name: "empty remediation", mutate: func(doc map[string]any) { doc["remediation"] = "" }},
		{name: "unnormalized remediation", mutate: func(doc map[string]any) { doc["remediation"] = "view_then_select_subtree " }},
		{name: "empty message", mutate: func(doc map[string]any) { doc["message"] = "" }},
		{name: "unnormalized message", mutate: func(doc map[string]any) { doc["message"] = " safe" }},
		{name: "unknown kind", mutate: func(doc map[string]any) { doc["kind"] = "usage_error" }},
		{name: "wrong remediation", mutate: func(doc map[string]any) { doc["remediation"] = "verify_identifier_or_access" }},
		{name: "wrong schema", mutate: func(doc map[string]any) { doc["recovery"].(map[string]any)["schema_version"] = 2 }},
		{name: "wrong action", mutate: func(doc map[string]any) { doc["recovery"].(map[string]any)["action"] = "adjust_request" }},
		{name: "retry safe", mutate: func(doc map[string]any) { doc["recovery"].(map[string]any)["retry_safe"] = true }},
		{name: "wrong capability", mutate: func(doc map[string]any) {
			doc["recovery"].(map[string]any)["next_capability"] = "confluence.page.outline"
		}},
		{name: "missing available", mutate: func(doc map[string]any) { delete(doc["recovery"].(map[string]any), "available") }},
		{name: "zero available", mutate: func(doc map[string]any) { doc["recovery"].(map[string]any)["available"] = 0 }},
		{name: "not found with matches", mutate: func(doc map[string]any) { doc["recovery"].(map[string]any)["matches"] = 2 }},
		{name: "check failed without matches", mutate: func(doc map[string]any) { doc["kind"] = "check_failed" }},
		{name: "one match", mutate: func(doc map[string]any) {
			doc["kind"] = "check_failed"
			doc["recovery"].(map[string]any)["matches"] = 1
		}},
		{name: "matches exceed available", mutate: func(doc map[string]any) {
			doc["kind"] = "check_failed"
			doc["recovery"].(map[string]any)["matches"] = 5
		}},
		{name: "unknown recovery member", mutate: func(doc map[string]any) { doc["recovery"].(map[string]any)["private"] = true }},
		{name: "null recovery member", mutate: func(doc map[string]any) { doc["recovery"].(map[string]any)["retry_safe"] = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := jiraStructureFailureMutate(t, valid, test.mutate)
			if _, err := DecodeJiraStructureFailure(bytes.NewReader(data)); err == nil {
				t.Fatal("semantic drift was accepted")
			}
		})
	}
}

func TestDecodeJiraStructureForestMismatchFailureAcceptsReleasedProjection(t *testing.T) {
	want := JiraStructureForestMismatchFailureView{
		Kind: "check_failed", Remediation: "reread_structure_view_then_retry_expected_forest_version",
		Message:  "expected Jira Structure forest signature -55 version 7 does not match current signature 66 version 8",
		Expected: JiraStructureForestVersion{Signature: -55, Version: 7},
		Observed: JiraStructureForestVersion{Signature: 66, Version: 8},
	}
	got, err := DecodeJiraStructureForestMismatchFailure(bytes.NewReader(
		jiraStructureForestMismatchFailureInput(t, want.Expected, want.Observed),
	))
	if err != nil {
		t.Fatalf("decode valid forest mismatch: %v", err)
	}
	if got != want {
		t.Fatalf("forest mismatch drifted: got=%+v want=%+v", got, want)
	}
}

func TestDecodeJiraStructureForestMismatchFailureRejectsDrift(t *testing.T) {
	valid := jiraStructureForestMismatchFailureInput(t,
		JiraStructureForestVersion{Signature: -55, Version: 7},
		JiraStructureForestVersion{Signature: 66, Version: 8},
	)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "wrong kind", mutate: func(doc map[string]any) { doc["kind"] = "not_found" }},
		{name: "wrong remediation", mutate: func(doc map[string]any) { doc["remediation"] = "view_then_select_subtree" }},
		{name: "unnormalized message", mutate: func(doc map[string]any) { doc["message"] = " stale" }},
		{name: "wrong action", mutate: func(doc map[string]any) { doc["recovery"].(map[string]any)["action"] = "adjust_request" }},
		{name: "retry safe", mutate: func(doc map[string]any) { doc["recovery"].(map[string]any)["retry_safe"] = true }},
		{name: "wrong capability", mutate: func(doc map[string]any) {
			doc["recovery"].(map[string]any)["next_capability"] = "confluence.page.outline"
		}},
		{name: "missing expected", mutate: func(doc map[string]any) { delete(doc["recovery"].(map[string]any), "expected_forest") }},
		{name: "null observed", mutate: func(doc map[string]any) { doc["recovery"].(map[string]any)["observed_forest"] = nil }},
		{name: "zero signature", mutate: func(doc map[string]any) {
			doc["recovery"].(map[string]any)["expected_forest"].(map[string]any)["signature"] = 0
		}},
		{name: "nonpositive version", mutate: func(doc map[string]any) {
			doc["recovery"].(map[string]any)["observed_forest"].(map[string]any)["version"] = 0
		}},
		{name: "equal forests", mutate: func(doc map[string]any) {
			doc["recovery"].(map[string]any)["observed_forest"] = doc["recovery"].(map[string]any)["expected_forest"]
		}},
		{name: "selection facts", mutate: func(doc map[string]any) {
			recovery := doc["recovery"].(map[string]any)
			delete(recovery, "expected_forest")
			delete(recovery, "observed_forest")
			recovery["available"] = 4
		}},
		{name: "unknown nested member", mutate: func(doc map[string]any) {
			doc["recovery"].(map[string]any)["expected_forest"].(map[string]any)["private"] = true
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := jiraStructureFailureMutate(t, valid, test.mutate)
			if _, err := DecodeJiraStructureForestMismatchFailure(bytes.NewReader(data)); err == nil {
				t.Fatal("forest mismatch drift was accepted")
			}
		})
	}

	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "unknown root member", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { doc["private"] = true })},
		{name: "null recovery", data: jiraStructureFailureMutate(t, valid, func(doc map[string]any) { doc["recovery"] = nil })},
		{name: "recursive duplicate", data: []byte(strings.Replace(string(valid), `"signature":`, `"signature":-55,"signature":`, 1))},
		{name: "trailing", data: append(bytes.Clone(valid), []byte(`true`)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeJiraStructureForestMismatchFailure(bytes.NewReader(test.data)); err == nil {
				t.Fatal("invalid forest mismatch wire was accepted")
			}
		})
	}
}

func jiraStructureFailureInput(t *testing.T, kind string, available int, matches *int) []byte {
	t.Helper()
	message := "selected Jira Structure folder was not found; available stored-folder count is 4"
	recovery := map[string]any{
		"schema_version": 1, "action": "reread_then_reselect", "retry_safe": false,
		"next_capability": "jira.structure.view", "available": available,
	}
	if matches != nil {
		recovery["matches"] = *matches
		message = "Jira Structure folder selector is ambiguous; matching stored-folder count is 2 and available stored-folder count is 4"
	}
	return jiraStructureEncode(t, map[string]any{
		"kind": kind, "remediation": "view_then_select_subtree", "message": message, "recovery": recovery,
	})
}

func jiraStructureForestMismatchFailureInput(
	t *testing.T,
	expected, observed JiraStructureForestVersion,
) []byte {
	t.Helper()
	return jiraStructureEncode(t, map[string]any{
		"kind":        "check_failed",
		"remediation": "reread_structure_view_then_retry_expected_forest_version",
		"message":     "expected Jira Structure forest signature -55 version 7 does not match current signature 66 version 8",
		"recovery": map[string]any{
			"schema_version": 1, "action": "reread_then_reselect", "retry_safe": false,
			"next_capability": "jira.structure.view",
			"expected_forest": map[string]any{"signature": expected.Signature, "version": expected.Version},
			"observed_forest": map[string]any{"signature": observed.Signature, "version": observed.Version},
		},
	})
}

func jiraStructureFailureMutate(t *testing.T, input []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(input, &doc); err != nil {
		t.Fatal(err)
	}
	mutate(doc)
	return jiraStructureEncode(t, doc)
}

func jiraStructureInt(value int) *int {
	return &value
}

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
