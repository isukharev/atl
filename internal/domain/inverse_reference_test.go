package domain

import "testing"

func TestJiraInverseReferenceClosedVocabularies(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"target", ValidJiraInverseReferenceTargetKind(JiraInverseReferenceTargetConfluencePage)},
		{"mode", ValidJiraInverseReferenceMode(JiraInverseReferenceModeExhaustive)},
		{"source", ValidJiraInverseReferenceSource(JiraInverseReferenceSourceProperties)},
		{"description source", ValidJiraInverseReferenceSource(JiraInverseReferenceSourceDescription)},
		{"worklogs source", ValidJiraInverseReferenceSource(JiraInverseReferenceSourceWorklogs)},
		{"order", ValidJiraInverseReferenceOrder(JiraInverseReferenceOrderAscending)},
		{"descending order", !ValidJiraInverseReferenceOrder("descending")},
		{"match status", ValidJiraInverseReferenceMatchStatus(JiraInverseReferenceIndeterminate)},
		{"source status", ValidJiraInverseReferenceSourceStatus(JiraInverseReferenceSourcePartial)},
		{"reason", ValidJiraInverseReferenceReason(JiraInverseReferenceReasonFieldMissing)},
		{"unknown target", !ValidJiraInverseReferenceTargetKind("page_title")},
		{"unknown mode", !ValidJiraInverseReferenceMode("all")},
		{"unknown source", !ValidJiraInverseReferenceSource("history")},
		{"unknown order", !ValidJiraInverseReferenceOrder("random")},
		{"unknown match status", !ValidJiraInverseReferenceMatchStatus("matched_with_snippet")},
		{"unknown source status", !ValidJiraInverseReferenceSourceStatus("backend_prose")},
		{"unknown reason", !ValidJiraInverseReferenceReason("backend supplied explanation")},
	}
	for _, test := range tests {
		if !test.ok {
			t.Errorf("%s vocabulary result = false", test.name)
		}
	}
}

func TestJiraInverseReferenceFieldSnapshotDistinguishesMissingAndNull(t *testing.T) {
	missing := JiraInverseReferenceFieldSnapshot{FieldID: "customfield_1"}
	null := JiraInverseReferenceFieldSnapshot{FieldID: "customfield_1", Present: true, Value: []byte("null")}
	if missing.Present {
		t.Fatal("missing field is present")
	}
	if !null.Present || string(null.Value) != "null" {
		t.Fatalf("explicit null field = %#v", null)
	}
}
