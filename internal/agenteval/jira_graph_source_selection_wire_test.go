package agenteval

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestDecodeJiraIssueGraphViewAcceptsStrictSourceSelections(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selected []string
	}{
		{name: "minimal", selected: []string{"comments"}},
		{name: "targeted snapshot", selected: []string{"issue_links", "attachments", "issue_properties"}},
		{name: "all fields", selected: []string{"issue_fields"}},
		{name: "hierarchy support", selected: []string{"hierarchy"}},
		{name: "hierarchy already covered", selected: []string{"issue_fields", "hierarchy"}},
		{name: "development", selected: []string{"comments", "development"}},
		{name: "all", selected: slices.Clone(jiraGraphWireAllSourceKinds)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := jiraGraphWireSelectedView(tc.selected)
			got, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, view)))
			if err != nil {
				t.Fatalf("decode selected graph: %v", err)
			}
			if got.SourceSelection == nil || !slices.Equal(got.SourceSelection.Selected, tc.selected) ||
				len(got.Sources) != len(tc.selected) {
				t.Fatalf("selected graph drifted: %+v", got)
			}
		})
	}
}

func TestDecodeJiraIssueGraphViewAcceptsSelectedSourceBudgetAndDepthShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		view JiraIssueGraphView
	}{
		{name: "root request limit", view: jiraGraphWireSelectedRootRequestLimitView()},
		{name: "child request limit", view: jiraGraphWireSelectedChildView(false)},
		{name: "successful child", view: jiraGraphWireSelectedChildView(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, tc.view))); err != nil {
				t.Fatalf("decode valid selected-source graph: %v", err)
			}
		})
	}
}

func TestDecodeJiraIssueGraphViewRejectsSourceSelectionShapeDrift(t *testing.T) {
	valid := jiraGraphWireEncode(t, jiraGraphWireSelectedView([]string{"issue_links", "attachments"}))
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "null", data: mutateJiraGraphSourceSelectionDocument(t, valid, func(root map[string]any) { root["source_selection"] = nil })},
		{name: "unknown member", data: mutateJiraGraphSourceSelectionDocument(t, valid, func(root map[string]any) {
			root["source_selection"].(map[string]any)["unknown"] = true
		})},
		{name: "duplicate member", data: []byte(strings.Replace(string(valid), `"selected":[`, `"selected":[],"selected":[`, 1))},
		{name: "wrong selected type", data: mutateJiraGraphSourceSelectionDocument(t, valid, func(root map[string]any) {
			root["source_selection"].(map[string]any)["selected"] = "issue_links"
		})},
		{name: "null snapshot fields", data: mutateJiraGraphSourceSelectionDocument(t, valid, func(root map[string]any) {
			root["source_selection"].(map[string]any)["snapshot"].(map[string]any)["fields"] = nil
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeJiraIssueGraphView(bytes.NewReader(tc.data)); err == nil {
				t.Fatal("invalid source-selection shape was accepted")
			}
		})
	}
}

func TestDecodeJiraIssueGraphViewRejectsSourceSelectionReconciliationDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*JiraIssueGraphView)
	}{
		{name: "version", mutate: func(view *JiraIssueGraphView) { view.SourceSelection.SchemaVersion++ }},
		{name: "null selected", mutate: func(view *JiraIssueGraphView) { view.SourceSelection.Selected = nil }},
		{name: "empty selected", mutate: func(view *JiraIssueGraphView) { view.SourceSelection.Selected = []string{} }},
		{name: "null omitted", mutate: func(view *JiraIssueGraphView) { view.SourceSelection.Omitted = nil }},
		{name: "unknown selected", mutate: func(view *JiraIssueGraphView) { view.SourceSelection.Selected[0] = "unknown" }},
		{name: "unordered selected", mutate: func(view *JiraIssueGraphView) {
			view.SourceSelection.Selected[0], view.SourceSelection.Selected[1] = view.SourceSelection.Selected[1], view.SourceSelection.Selected[0]
		}},
		{name: "duplicate selected", mutate: func(view *JiraIssueGraphView) {
			view.SourceSelection.Selected[1] = view.SourceSelection.Selected[0]
		}},
		{name: "overlapping partition", mutate: func(view *JiraIssueGraphView) {
			view.SourceSelection.Omitted = append([]string{"issue_links"}, view.SourceSelection.Omitted...)
		}},
		{name: "incomplete partition", mutate: func(view *JiraIssueGraphView) {
			view.SourceSelection.Omitted = view.SourceSelection.Omitted[1:]
		}},
		{name: "development bound", mutate: func(view *JiraIssueGraphView) {
			view.Bounds.IncludeDevelopment = true
		}},
		{name: "snapshot field order", mutate: func(view *JiraIssueGraphView) {
			view.SourceSelection.Snapshot.Fields[1], view.SourceSelection.Snapshot.Fields[2] =
				view.SourceSelection.Snapshot.Fields[2], view.SourceSelection.Snapshot.Fields[1]
		}},
		{name: "snapshot properties", mutate: func(view *JiraIssueGraphView) {
			view.SourceSelection.Snapshot.Properties = true
		}},
		{name: "snapshot reason", mutate: func(view *JiraIssueGraphView) {
			view.SourceSelection.Snapshot.SupportingFieldsReason = "hierarchy_discovery"
		}},
		{name: "unselected source row", mutate: func(view *JiraIssueGraphView) {
			view.Sources = append(view.Sources, JiraIssueGraphSource{
				NodeID: view.RootID, Kind: "comments", Requested: true, Status: "empty", Complete: true, Stability: "public_api",
			})
			view.Summary.SourceCount++
			view.Summary.SourceStatusCounts["empty"]++
		}},
		{name: "missing selected source row", mutate: func(view *JiraIssueGraphView) {
			view.Sources = view.Sources[:1]
			view.Summary.SourceCount--
			view.Summary.SourceStatusCounts["empty"]--
		}},
		{name: "selected max sources", mutate: func(view *JiraIssueGraphView) { view.Bounds.MaxSources++ }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := jiraGraphWireSelectedView([]string{"issue_links", "attachments"})
			tc.mutate(&view)
			if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, view))); err == nil {
				t.Fatal("invalid source-selection reconciliation was accepted")
			}
		})
	}
}

func TestDecodeJiraIssueGraphViewRejectsUnselectedEvidence(t *testing.T) {
	view := jiraGraphWireEvidenceView()
	view.SourceSelection = jiraGraphWireSelection([]string{"comments"})
	view.Sources = jiraGraphWireSelectedSources(view.RootID, 0, view.SourceSelection.Selected)
	view.Bounds.MaxSources = view.Bounds.MaxNodes*len(view.SourceSelection.Selected) + 1
	view.Summary.SourceCount = len(view.Sources)
	view.Summary.SourceStatusCounts["complete"] = 0
	view.Summary.SourceStatusCounts["empty"] = len(view.Sources)
	if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, view))); err == nil {
		t.Fatal("evidence from an omitted source was accepted")
	}
}

func TestJiraGraphSourceSelectionRequiresExactHierarchyProjection(t *testing.T) {
	view := jiraGraphWireSelectedView([]string{"hierarchy"})
	for _, mutate := range []func(*JiraIssueGraphView){
		func(view *JiraIssueGraphView) { view.SourceSelection.Snapshot.Fields = []string{"summary"} },
		func(view *JiraIssueGraphView) { view.SourceSelection.Snapshot.SupportingFieldsReason = "" },
	} {
		candidate := jiraGraphWireClone(t, view)
		mutate(&candidate)
		if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, candidate))); err == nil {
			t.Fatal("invalid hierarchy-support projection was accepted")
		}
	}
}

func jiraGraphWireSelectedView(selected []string) JiraIssueGraphView {
	view := jiraGraphWireBaseView()
	view.SourceSelection = jiraGraphWireSelection(selected)
	view.Bounds.IncludeDevelopment = slices.Contains(selected, "development")
	view.Bounds.MaxSources = view.Bounds.MaxNodes*len(selected) + 1
	view.Sources = jiraGraphWireSelectedSources(view.RootID, 0, selected)
	view.Summary.SourceCount = len(selected)
	view.Summary.SourceStatusCounts["empty"] = len(selected)
	return view
}

func jiraGraphWireSelectedRootRequestLimitView() JiraIssueGraphView {
	view := jiraGraphWireSelectedView([]string{"issue_links"})
	view.Nodes[0].State = "unresolved"
	view.Nodes[0].Expanded = false
	view.Sources[0].Status = "partial"
	view.Sources[0].Complete = false
	view.Sources[0].Truncated = true
	view.Sources[0].PartialReason = "request_limit"
	view.Bounds.ExpandedNodes = 0
	view.Bounds.AttemptedNodes = 0
	view.Bounds.RequestsUsed = 0
	view.Bounds.FrontierCount = 1
	view.Summary.IncompleteSourceCount = 1
	view.Summary.SourceStatusCounts["empty"] = 0
	view.Summary.SourceStatusCounts["partial"] = 1
	view.Complete = false
	view.Truncated = true
	view.Frontier = []JiraIssueGraphFrontier{{NodeID: view.RootID, Reason: "request_limit"}}
	view.Warnings = []string{"one or more requested graph sources are incomplete"}
	return view
}

func jiraGraphWireSelectedChildView(expanded bool) JiraIssueGraphView {
	view := jiraGraphWireSelectedView([]string{"issue_links"})
	view.Bounds.RequestedDepth = 1
	target := JiraIssueGraphNode{
		ID: "jira:issue:AG-2", Kind: "jira_issue", Service: "jira", ExternalID: "AG-2",
		State: "unresolved", Depth: 1, Stability: "public_api",
	}
	edge := JiraIssueGraphEdge{
		From: view.RootID, To: target.ID, Kind: "jira_link", RelationType: "Blocks", Relation: "blocks",
		Direction: "outward", Current: true, Confidence: "exact", Stability: "public_api",
		Evidence: []JiraIssueGraphEvidence{{
			Collector: "issue_links", SourceNodeID: view.RootID, SourceKind: "field", SourceID: "issuelinks",
			JSONPointer: "/fields/issuelinks/0", Extraction: "structured",
		}},
	}
	edge.ID = jiraGraphWireEdgeID(edge)
	view.Nodes = append(view.Nodes, target)
	view.Edges = []JiraIssueGraphEdge{edge}
	view.Sources[0].Status = "complete"
	view.Sources[0].Count = 1
	view.Sources = append(view.Sources, jiraGraphWireSelectedSources(target.ID, 1, []string{"issue_links"})...)
	view.Summary.NodeCount = 2
	view.Summary.EdgeCount = 1
	view.Summary.EvidenceCount = 1
	view.Summary.SourceCount = 2
	view.Summary.SourceStatusCounts["complete"] = 1
	view.Summary.SourceStatusCounts["empty"] = 1
	if expanded {
		view.Nodes[1].State = "resolved"
		view.Nodes[1].Expanded = true
		view.Bounds.ExpandedNodes = 2
		view.Bounds.AttemptedNodes = 2
		view.Bounds.FollowedNodes = 1
		return view
	}
	view.Sources[1].Status = "partial"
	view.Sources[1].Complete = false
	view.Sources[1].Truncated = true
	view.Sources[1].PartialReason = "request_limit"
	view.Bounds.FrontierCount = 1
	view.Summary.IncompleteSourceCount = 1
	view.Summary.SourceStatusCounts["empty"] = 0
	view.Summary.SourceStatusCounts["partial"] = 1
	view.Complete = false
	view.Truncated = true
	view.Frontier = []JiraIssueGraphFrontier{{NodeID: target.ID, Depth: 1, Reason: "request_limit"}}
	view.Warnings = []string{"one or more requested graph sources are incomplete"}
	return view
}

func jiraGraphWireSelection(selected []string) *JiraIssueGraphSourceSelection {
	selected = slices.Clone(selected)
	selectedSet := map[string]bool{}
	for _, kind := range selected {
		selectedSet[kind] = true
	}
	omitted := make([]string, 0, len(jiraGraphWireAllSourceKinds)-len(selected))
	for _, kind := range jiraGraphWireAllSourceKinds {
		if !selectedSet[kind] {
			omitted = append(omitted, kind)
		}
	}
	fields := []string{"summary"}
	reason := ""
	if selectedSet["issue_fields"] || selectedSet["hierarchy"] {
		fields = []string{"*all"}
		if selectedSet["hierarchy"] && !selectedSet["issue_fields"] {
			reason = "hierarchy_discovery"
		}
	} else {
		if selectedSet["issue_links"] {
			fields = append(fields, "issuelinks")
		}
		if selectedSet["attachments"] {
			fields = append(fields, "attachment")
		}
	}
	return &JiraIssueGraphSourceSelection{
		SchemaVersion: JiraIssueGraphSourceSelectionSchemaVersion,
		Selected:      selected, Omitted: omitted,
		Snapshot: JiraIssueGraphSourceSelectionSnapshot{
			Fields: fields, Properties: selectedSet["issue_properties"], SupportingFieldsReason: reason,
		},
	}
}

func jiraGraphWireSelectedSources(root string, depth int, kinds []string) []JiraIssueGraphSource {
	sources := make([]JiraIssueGraphSource, 0, len(kinds))
	for _, kind := range kinds {
		stability := "public_api"
		if kind == "issue_properties" || kind == "development" {
			stability = "experimental_api"
		}
		sources = append(sources, JiraIssueGraphSource{
			NodeID: root, NodeDepth: depth, Kind: kind, Requested: true,
			Status: "empty", Complete: true, Stability: stability,
		})
	}
	return sources
}

func jiraGraphWireClone(t *testing.T, view JiraIssueGraphView) JiraIssueGraphView {
	t.Helper()
	var clone JiraIssueGraphView
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func mutateJiraGraphSourceSelectionDocument(t *testing.T, data []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	mutate(root)
	encoded, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
