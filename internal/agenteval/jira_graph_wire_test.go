package agenteval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeJiraIssueGraphViewAcceptsReleasedShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		view JiraIssueGraphView
	}{
		{name: "base", view: jiraGraphWireBaseView()},
		{name: "development", view: jiraGraphWireDevelopmentView()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded := jiraGraphWireEncode(t, tc.view)
			got, err := DecodeJiraIssueGraphView(bytes.NewReader(encoded))
			if err != nil {
				t.Fatalf("decode valid graph: %v\n%s", err, encoded)
			}
			if got.RootID != tc.view.RootID || got.Summary.NodeCount != len(tc.view.Nodes) || got.Bounds.IncludeDevelopment != tc.view.Bounds.IncludeDevelopment {
				t.Fatalf("decoded graph drifted: %+v", got)
			}
		})
	}
}

func TestDecodeJiraIssueGraphViewAcceptsReleasedNullWarnings(t *testing.T) {
	view := jiraGraphWireBaseView()
	view.Warnings = nil
	got, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, view)))
	if err != nil {
		t.Fatalf("decode released null warnings: %v", err)
	}
	if got.Warnings != nil {
		t.Fatalf("null warnings were not preserved: %#v", got.Warnings)
	}
}

func TestDecodeJiraIssueGraphViewAcceptsOrdinaryPrivateURLPath(t *testing.T) {
	view := jiraGraphWireBaseView()
	view.Nodes[0].URL = "https://jira.example.test/private/browse/AG-1"
	if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, view))); err != nil {
		t.Fatalf("ordinary private URL path was rejected: %v", err)
	}
}

func TestJiraIssueGraphWireIdentityBounds(t *testing.T) {
	if !jiraGraphWireJiraID("jira:issue:AB-1") ||
		!jiraGraphWireJiraID("jira:issue:"+strings.Repeat("A", 32)+"-1") ||
		jiraGraphWireJiraID("jira:issue:A-1") ||
		jiraGraphWireJiraID("jira:issue:"+strings.Repeat("A", 33)+"-1") {
		t.Fatal("jira project key bounds drifted")
	}
	if !jiraGraphWirePositiveID.MatchString("1") ||
		!jiraGraphWirePositiveID.MatchString("1"+strings.Repeat("0", 31)) ||
		jiraGraphWirePositiveID.MatchString("1"+strings.Repeat("0", 32)) {
		t.Fatal("positive id bounds drifted")
	}
}

func TestDecodeJiraIssueGraphViewRejectsWireShapeDrift(t *testing.T) {
	valid := jiraGraphWireEncode(t, jiraGraphWireBaseView())
	var document map[string]any
	if err := json.Unmarshal(valid, &document); err != nil {
		t.Fatal(err)
	}

	mutate := func(fn func(map[string]any)) []byte {
		clone := map[string]any{}
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &clone); err != nil {
			t.Fatal(err)
		}
		fn(clone)
		return jiraGraphWireEncode(t, clone)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown root member", data: mutate(func(doc map[string]any) { doc["label"] = "narrative" })},
		{name: "missing root member", data: mutate(func(doc map[string]any) { delete(doc, "warnings") })},
		{name: "null array", data: mutate(func(doc map[string]any) { doc["nodes"] = nil })},
		{name: "unknown nested member", data: mutate(func(doc map[string]any) { doc["bounds"].(map[string]any)["private"] = true })},
		{name: "null optional member", data: mutate(func(doc map[string]any) { doc["nodes"].([]any)[0].(map[string]any)["url"] = nil })},
		{name: "trailing value", data: append(append([]byte(nil), valid...), []byte(`{}`)...)},
		{name: "duplicate member", data: []byte(strings.Replace(string(valid), `"root_id":`, `"root_id":"jira:issue:AG-1","root_id":`, 1))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeJiraIssueGraphView(bytes.NewReader(tc.data)); err == nil {
				t.Fatal("invalid wire was accepted")
			}
		})
	}
}

func TestDecodeJiraIssueGraphViewRejectsReconciliationFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*JiraIssueGraphView)
	}{
		{name: "schema", mutate: func(view *JiraIssueGraphView) { view.SchemaVersion = 1 }},
		{name: "root", mutate: func(view *JiraIssueGraphView) { view.RootID = "jira:issue:ag-1" }},
		{name: "node bound", mutate: func(view *JiraIssueGraphView) { view.Bounds.MaxNodes = 0 }},
		{name: "request usage", mutate: func(view *JiraIssueGraphView) { view.Bounds.RequestsUsed = view.Bounds.MaxRequests + 1 }},
		{name: "max sources formula", mutate: func(view *JiraIssueGraphView) { view.Bounds.MaxSources++ }},
		{name: "node count", mutate: func(view *JiraIssueGraphView) { view.Summary.NodeCount++ }},
		{name: "status counts", mutate: func(view *JiraIssueGraphView) { view.Summary.SourceStatusCounts["empty"]-- }},
		{name: "duplicate node", mutate: func(view *JiraIssueGraphView) {
			view.Nodes = append(view.Nodes, view.Nodes[0])
			view.Summary.NodeCount++
		}},
		{name: "duplicate source identity", mutate: func(view *JiraIssueGraphView) {
			view.Sources[1] = view.Sources[0]
			view.Summary.SourceStatusCounts["empty"] = len(view.Sources)
		}},
		{name: "source ordering", mutate: func(view *JiraIssueGraphView) {
			view.Sources[0], view.Sources[1] = view.Sources[1], view.Sources[0]
		}},
		{name: "source completeness", mutate: func(view *JiraIssueGraphView) {
			view.Sources[0].Status = "partial"
			view.Sources[0].Complete = true
			view.Sources[0].PartialReason = "request_failed"
		}},
		{name: "source partial reason", mutate: func(view *JiraIssueGraphView) {
			view.Sources[0].Status = "partial"
			view.Sources[0].Complete = false
			view.Sources[0].PartialReason = "backend said no"
		}},
		{name: "warning mismatch", mutate: func(view *JiraIssueGraphView) { view.Warnings = []string{"narrative"} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := jiraGraphWireBaseView()
			tc.mutate(&view)
			if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, view))); err == nil {
				t.Fatal("invalid reconciliation was accepted")
			}
		})
	}
}

func TestDecodeJiraIssueGraphViewRejectsTopologyFrontierAndSCMFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*JiraIssueGraphView)
	}{
		{name: "missing edge endpoint", mutate: func(view *JiraIssueGraphView) { view.Edges[0].To = "jira:issue:MISSING-1" }},
		{name: "forged edge id", mutate: func(view *JiraIssueGraphView) { view.Edges[0].ID = "edge:" + strings.Repeat("0", 64) }},
		{name: "duplicate edge", mutate: func(view *JiraIssueGraphView) {
			view.Edges = append(view.Edges, view.Edges[0])
			view.Summary.EdgeCount++
			view.Summary.EvidenceCount++
		}},
		{name: "evidence source node", mutate: func(view *JiraIssueGraphView) { view.Edges[0].Evidence[0].SourceNodeID = view.Edges[0].To }},
		{name: "evidence count", mutate: func(view *JiraIssueGraphView) { view.Summary.EvidenceCount++ }},
		{name: "development opt in", mutate: func(view *JiraIssueGraphView) {
			view.Bounds.IncludeDevelopment = false
			view.Bounds.MaxSources = view.Bounds.MaxNodes*8 + 1
		}},
		{name: "scm host", mutate: func(view *JiraIssueGraphView) { view.Nodes[1].SCM.Host = "CODE.example.test" }},
		{name: "scm selector shape", mutate: func(view *JiraIssueGraphView) { view.Nodes[1].SCM.BranchName = "also-set" }},
		{name: "scm url", mutate: func(view *JiraIssueGraphView) { view.Nodes[1].URL = "https://code.example.test/team/repo" }},
		{name: "frontier reason", mutate: func(view *JiraIssueGraphView) {
			view.Frontier = []JiraIssueGraphFrontier{{NodeID: "jira:issue:NEXT-2", Depth: 1, Reason: "server_error"}}
			view.Bounds.FrontierCount = 1
			view.Truncated = true
		}},
		{name: "frontier node depth", mutate: func(view *JiraIssueGraphView) {
			view.Frontier = []JiraIssueGraphFrontier{{NodeID: view.RootID, Depth: 1, Reason: "output_limit"}}
			view.Bounds.FrontierCount = 1
			view.Truncated = true
		}},
		{name: "development target depth", mutate: func(view *JiraIssueGraphView) {
			view.Bounds.RequestedDepth = 1
			view.Nodes[1].Depth = 2
			view.Nodes[2].Depth = 2
		}},
		{name: "development target without edge", mutate: func(view *JiraIssueGraphView) {
			view.Edges = view.Edges[1:]
			view.Summary.EdgeCount = 1
			view.Summary.EvidenceCount = 1
			for index := range view.Sources {
				if view.Sources[index].Kind == "development" {
					view.Sources[index].Status = "empty"
					view.Sources[index].Count = 0
				}
			}
			view.Summary.SourceStatusCounts["complete"] = 0
			view.Summary.SourceStatusCounts["empty"] = len(view.Sources)
		}},
		{name: "development project without artifact", mutate: func(view *JiraIssueGraphView) {
			view.Nodes = append(view.Nodes[:1], view.Nodes[2])
			view.Edges = view.Edges[1:]
			view.Summary.NodeCount = len(view.Nodes)
			view.Summary.EdgeCount = 1
			view.Summary.EvidenceCount = 1
			for index := range view.Sources {
				if view.Sources[index].Kind == "development" {
					view.Sources[index].Status = "empty"
					view.Sources[index].Count = 0
				}
			}
			view.Summary.SourceStatusCounts["complete"] = 0
			view.Summary.SourceStatusCounts["empty"] = len(view.Sources)
		}},
		{name: "development artifact without project", mutate: func(view *JiraIssueGraphView) {
			view.Nodes = view.Nodes[:2]
			view.Edges = view.Edges[:1]
			view.Summary.NodeCount = len(view.Nodes)
			view.Summary.EdgeCount = 1
			view.Summary.EvidenceCount = 1
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := jiraGraphWireDevelopmentView()
			tc.mutate(&view)
			if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, view))); err == nil {
				t.Fatal("invalid topology was accepted")
			}
		})
	}
}

func TestDecodeJiraIssueGraphViewRejectsEvidenceOrderingAndSensitiveCoordinates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*JiraIssueGraphView)
	}{
		{name: "ordering", mutate: func(view *JiraIssueGraphView) {
			view.Edges[0].Evidence[0], view.Edges[0].Evidence[1] = view.Edges[0].Evidence[1], view.Edges[0].Evidence[0]
		}},
		{name: "private source id", mutate: func(view *JiraIssueGraphView) {
			view.Edges[0].Evidence[0].SourceID = "private-field"
		}},
		{name: "private json pointer", mutate: func(view *JiraIssueGraphView) {
			view.Edges[0].Evidence[0].JSONPointer = "/fields/private"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := jiraGraphWireEvidenceView()
			tc.mutate(&view)
			if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, view))); err == nil {
				t.Fatal("invalid evidence was accepted")
			}
		})
	}
}

func TestDecodeJiraIssueGraphViewEnforcesOneMiBBound(t *testing.T) {
	valid := jiraGraphWireEncode(t, jiraGraphWireBaseView())
	if len(valid) >= jiraGraphWireMaxBytes {
		t.Fatalf("test fixture is unexpectedly large: %d", len(valid))
	}
	atLimit := append(bytes.Clone(valid), bytes.Repeat([]byte(" "), jiraGraphWireMaxBytes-len(valid))...)
	if _, err := DecodeJiraIssueGraphView(bytes.NewReader(atLimit)); err != nil {
		t.Fatalf("exact bound was rejected: %v", err)
	}
	over := append(bytes.Clone(atLimit), ' ')
	if _, err := DecodeJiraIssueGraphView(bytes.NewReader(over)); err == nil {
		t.Fatal("oversized wire was accepted")
	}
}

func jiraGraphWireBaseView() JiraIssueGraphView {
	rootID := "jira:issue:AG-1"
	sources := jiraGraphWireSources(rootID, 0, false)
	return JiraIssueGraphView{
		SchemaVersion: JiraIssueGraphViewSchemaVersion,
		RootID:        rootID,
		Complete:      true,
		Bounds: JiraIssueGraphBounds{
			RequestedDepth: 0, MaxNodes: 12, MaxEdges: 16, MaxEvidence: jiraGraphWireMaxEvidence,
			MaxSourceBytes: jiraGraphWireMaxSourceBytes, ExpandedNodes: 1, AttemptedNodes: 1,
			MaxRequests: 8, RequestsUsed: 1, MaxResponseBytes: jiraGraphWireMaxResponseBytes,
			ResponseBytesUsed: 256, MaxSources: 12*8 + 1, MaxFrontier: 12,
		},
		Summary: JiraIssueGraphSummary{
			NodeCount: 1, SourceCount: len(sources), SourceStatusCounts: map[string]int{
				"complete": 0, "empty": len(sources), "partial": 0, "forbidden": 0, "unsupported": 0, "skipped": 0,
			},
			NodeCountMatchesNodes: true, EdgeCountMatchesEdges: true, EvidenceCountMatchesEdges: true,
			SourceCountMatchesSources: true, SourceStatusCountsMatch: true, IncompleteCountMatches: true,
			ExpandedCountMatchesNodes: true, CompleteMatchesSources: true,
		},
		Nodes: []JiraIssueGraphNode{{
			ID: rootID, Kind: "jira_issue", Service: "jira", ExternalID: "AG-1",
			State: "resolved", Expanded: true, Stability: "public_api",
		}},
		Edges: []JiraIssueGraphEdge{}, Sources: sources,
		Frontier: []JiraIssueGraphFrontier{}, Warnings: []string{},
	}
}

func jiraGraphWireDevelopmentView() JiraIssueGraphView {
	view := jiraGraphWireBaseView()
	view.Bounds.IncludeDevelopment = true
	view.Bounds.MaxSources = view.Bounds.MaxNodes*9 + 1
	view.Sources = jiraGraphWireSources(view.RootID, 0, true)
	projectSCM := &JiraIssueGraphSCM{Host: "code.example.test", ProjectPath: "team/repo"}
	commitSCM := &JiraIssueGraphSCM{Host: projectSCM.Host, ProjectPath: projectSCM.ProjectPath, CommitSHA: strings.Repeat("1", 40)}
	projectHash := jiraGraphWireHash("https://" + projectSCM.Host + "\x00" + projectSCM.ProjectPath)
	commit := JiraIssueGraphNode{
		ID: "gitlab:commit:" + projectHash + ":" + commitSCM.CommitSHA, Kind: "gitlab_commit", Service: "gitlab",
		State: "stub", Depth: 1, Stability: "experimental_api", SCM: commitSCM,
	}
	project := JiraIssueGraphNode{
		ID: "gitlab:project:" + projectHash, Kind: "gitlab_project", Service: "gitlab",
		State: "stub", Depth: 1, Stability: "experimental_api", SCM: projectSCM,
	}
	view.Nodes = append(view.Nodes, commit, project)
	commitEdge := jiraGraphWireDevelopmentEdge(view.RootID, commit, "development_commit")
	projectEdge := jiraGraphWireDevelopmentEdge(view.RootID, project, "development_project")
	view.Edges = []JiraIssueGraphEdge{commitEdge, projectEdge}
	for index := range view.Sources {
		if view.Sources[index].Kind == "development" {
			view.Sources[index].Status = "complete"
			view.Sources[index].Count = 1
		}
	}
	view.Summary.NodeCount = len(view.Nodes)
	view.Summary.EdgeCount = len(view.Edges)
	view.Summary.EvidenceCount = len(view.Edges)
	view.Summary.SourceCount = len(view.Sources)
	view.Summary.SourceStatusCounts["empty"] = len(view.Sources) - 1
	view.Summary.SourceStatusCounts["complete"] = 1
	return view
}

func jiraGraphWireEvidenceView() JiraIssueGraphView {
	view := jiraGraphWireBaseView()
	target := JiraIssueGraphNode{
		ID: "jira:issue:REF-2", Kind: "jira_issue", Service: "jira", ExternalID: "REF-2",
		State: "stub", Depth: 1, Stability: "public_api",
	}
	view.Nodes = append(view.Nodes, target)
	edge := JiraIssueGraphEdge{
		From: view.RootID, To: target.ID, Kind: "mentions", Direction: "outbound", Current: true,
		Confidence: "exact", Stability: "public_api",
		Evidence: []JiraIssueGraphEvidence{
			{Collector: "issue_fields", SourceNodeID: view.RootID, SourceKind: "field", SourceID: "a", JSONPointer: "/fields/a", Extraction: "jira_key"},
			{Collector: "issue_fields", SourceNodeID: view.RootID, SourceKind: "field", SourceID: "b", JSONPointer: "/fields/b", Extraction: "jira_key"},
		},
	}
	edge.ID = jiraGraphWireEdgeID(edge)
	view.Edges = []JiraIssueGraphEdge{edge}
	view.Summary.NodeCount = len(view.Nodes)
	view.Summary.EdgeCount = 1
	view.Summary.EvidenceCount = 2
	for index := range view.Sources {
		if view.Sources[index].Kind == "issue_fields" {
			view.Sources[index].Status = "complete"
			view.Sources[index].Count = 1
		}
	}
	view.Summary.SourceStatusCounts["complete"] = 1
	view.Summary.SourceStatusCounts["empty"]--
	return view
}

func jiraGraphWireDevelopmentEdge(root string, target JiraIssueGraphNode, kind string) JiraIssueGraphEdge {
	edge := JiraIssueGraphEdge{
		From: root, To: target.ID, Kind: kind, Direction: "outbound", Current: true,
		Confidence: "exact", Stability: "experimental_api",
		Evidence: []JiraIssueGraphEvidence{{
			Collector: "development", SourceNodeID: root, SourceKind: "development_detail",
			SourceID: jiraGraphWireDevelopmentEvidenceID(kind, target.SCM), Extraction: "structured",
		}},
	}
	edge.ID = jiraGraphWireEdgeID(edge)
	return edge
}

func jiraGraphWireSources(root string, depth int, development bool) []JiraIssueGraphSource {
	kinds := jiraGraphWireSourceKinds(development)
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

func jiraGraphWireEncode(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
