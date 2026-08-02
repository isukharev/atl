package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func TestJiraIssueGraphToolUsesClosedDefaultsAndOmitsNarrativeLabels(t *testing.T) {
	const marker = "PRIVATE-SUMMARY PRIVATE-PAGE-TITLE PRIVATE-FILENAME PRIVATE-REMOTE-TITLE"
	opts := defaultMCPGraphOptions(0)
	reader := &recordingJiraReader{graphResult: validMCPGraphResult("PROJ-1", opts, marker)}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result := callToolOK(t, client, "jira_issue_graph", map[string]any{"key": "PROJ-1"})
	if reader.graphKey != "PROJ-1" || !reflect.DeepEqual(reader.graphOpts, opts) || reader.graphOpts.ResolveConfluence {
		t.Fatalf("graph call key=%q opts=%+v want=%+v", reader.graphKey, reader.graphOpts, opts)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), marker) || strings.Contains(string(encoded), `"label"`) ||
		strings.Contains(string(encoded), `"include_development"`) || strings.Contains(string(encoded), `"scm"`) ||
		strings.Contains(string(encoded), `"development"`) {
		t.Fatalf("narrative graph label crossed MCP projection: %s", encoded)
	}
	var out JiraIssueGraphOutput
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	if out.SchemaVersion != 2 || out.RootID != "jira:issue:PROJ-1" || !out.Complete ||
		out.Bounds.RequestedDepth != 0 || out.Bounds.MaxNodes != 50 || out.Bounds.MaxEdges != 200 ||
		out.Bounds.MaxEvidence != 500 || out.Bounds.MaxRequests != 50 || out.Bounds.MaxResponseBytes != 16<<20 {
		t.Fatalf("output=%+v", out)
	}
}

func TestJiraIssueGraphToolRejectsInvalidInputBeforeJiraConstruction(t *testing.T) {
	constructed := 0
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) {
			constructed++
			return nil, errors.New("unexpected construction")
		},
	}))
	defer closeSessions()

	tests := []map[string]any{
		{},
		{"key": "proj-1"},
		{"key": " PROJ-1"},
		{"key": "PROJ-1", "depth": -1},
		{"key": "PROJ-1", "depth": 3},
		{"key": "PROJ-1", "max_nodes": 101},
		{"key": "PROJ-1", "max_edges": 501},
		{"key": "PROJ-1", "max_requests": 101},
		{"key": "PROJ-1", "max_bytes": 1023},
		{"key": "PROJ-1", "max_bytes": (1 << 20) + 1},
		{"key": "PROJ-1", "resolve_confluence": true},
	}
	for _, args := range tests {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "jira_issue_graph", Arguments: args})
		if err != nil {
			t.Fatalf("args=%v CallTool: %v", args, err)
		}
		if !result.IsError || result.StructuredContent != nil {
			t.Fatalf("args=%v result=%+v", args, result)
		}
	}
	if constructed != 0 {
		t.Fatalf("invalid graph inputs constructed Jira %d times", constructed)
	}
}

func TestJiraIssueGraphInputOmittedAndFalseAreEquivalent(t *testing.T) {
	_, omitted, omittedBytes, err := validatedJiraIssueGraphInput(JiraIssueGraphInput{Key: "PROJ-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, explicit, explicitBytes, err := validatedJiraIssueGraphInput(JiraIssueGraphInput{Key: "PROJ-1", IncludeDevelopment: false})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(omitted, explicit) || omittedBytes != explicitBytes || omitted.IncludeDevelopment {
		t.Fatalf("omitted=%+v/%d explicit=%+v/%d", omitted, omittedBytes, explicit, explicitBytes)
	}
	var encoded [][]byte
	for _, args := range []map[string]any{{"key": "PROJ-1"}, {"key": "PROJ-1", "include_development": false}} {
		reader := &recordingJiraReader{graphResult: validMCPGraphResult("PROJ-1", omitted, "discarded label")}
		client, closeSessions := connectTestClient(t, New("test", Dependencies{
			Jira: func() (JiraReader, error) { return reader, nil },
		}))
		result := callToolOK(t, client, "jira_issue_graph", args)
		value, marshalErr := json.Marshal(result.StructuredContent)
		closeSessions()
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		encoded = append(encoded, value)
	}
	if string(encoded[0]) != string(encoded[1]) {
		t.Fatalf("omitted and explicit false outputs differ:\nomitted=%s\nfalse=%s", encoded[0], encoded[1])
	}
}

func TestJiraIssueGraphToolForwardsCustomBoundsAndReturnsIncompleteGraph(t *testing.T) {
	opts := app.JiraIssueGraphOptions{
		Depth: 2, MaxNodes: 75, MaxEdges: 350, MaxEvidence: 500,
		MaxRequests: 80, MaxResponseBytes: 16 << 20,
	}
	graph := validMCPGraphResult("PROJ-9", opts, "private summary")
	graph.Sources[0].Status = domain.ArtifactSourcePartial
	graph.Sources[0].Complete = false
	graph.Sources[0].PartialReason = domain.ArtifactPartialRequestFailed
	graph.Complete = false
	graph.Summary.IncompleteSourceCount = 1
	graph.Summary.SourceStatusCounts["empty"] = 7
	graph.Summary.SourceStatusCounts["partial"] = 1
	graph.Warnings = []string{"one or more requested graph sources are incomplete"}
	reader := &recordingJiraReader{graphResult: graph}
	client, closeSessions := connectTestClient(t, New("test", Dependencies{
		Jira: func() (JiraReader, error) { return reader, nil },
	}))
	defer closeSessions()

	result := callToolOK(t, client, "jira_issue_graph", map[string]any{
		"key": "PROJ-9", "depth": 2, "max_nodes": 75, "max_edges": 350,
		"max_requests": 80, "max_bytes": 1 << 20,
	})
	if !reflect.DeepEqual(reader.graphOpts, opts) || reader.graphOpts.ResolveConfluence {
		t.Fatalf("opts=%+v want=%+v", reader.graphOpts, opts)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	var out JiraIssueGraphOutput
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	if out.Complete || len(out.Sources) != 8 || out.Sources[0].PartialReason != domain.ArtifactPartialRequestFailed {
		t.Fatalf("incomplete output=%+v", out)
	}
}

func TestJiraIssueGraphToolGatesDevelopmentAndOversizeOutput(t *testing.T) {
	t.Run("development", func(t *testing.T) {
		opts := defaultMCPGraphOptions(0)
		graph := validMCPGraphResult("PROJ-1", opts, "private")
		graph.Sources[0].Kind = "development"
		reader := &recordingJiraReader{graphResult: graph}
		client, closeSessions := connectTestClient(t, New("test", Dependencies{
			Jira: func() (JiraReader, error) { return reader, nil },
		}))
		defer closeSessions()
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "jira_issue_graph", Arguments: map[string]any{"key": "PROJ-1"},
		})
		if err != nil || !result.IsError || result.StructuredContent != nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if strings.Contains(text, "development") || !strings.Contains(text, "failed validation") {
			t.Fatalf("error=%q", text)
		}
	})

	t.Run("opted in", func(t *testing.T) {
		opts := defaultMCPGraphOptions(0)
		opts.IncludeDevelopment = true
		reader := &recordingJiraReader{graphResult: validMCPDevelopmentGraphResult("PROJ-1", opts)}
		client, closeSessions := connectTestClient(t, New("test", Dependencies{
			Jira: func() (JiraReader, error) { return reader, nil },
		}))
		defer closeSessions()
		result := callToolOK(t, client, "jira_issue_graph", map[string]any{"key": "PROJ-1", "include_development": true})
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"url":"https://git.`) || !strings.Contains(string(encoded), `"scm"`) {
			t.Fatalf("Development projection crossed URL boundary or omitted SCM: %s", encoded)
		}
		var out JiraIssueGraphOutput
		if err := json.Unmarshal(encoded, &out); err != nil {
			t.Fatal(err)
		}
		if !out.Bounds.IncludeDevelopment || !reader.graphOpts.IncludeDevelopment || len(out.Nodes) != 3 || out.Nodes[1].SCM == nil {
			t.Fatalf("output=%+v opts=%+v", out, reader.graphOpts)
		}
	})

	t.Run("max bytes", func(t *testing.T) {
		opts := defaultMCPGraphOptions(0)
		reader := &recordingJiraReader{graphResult: validMCPGraphResult("PROJ-1", opts, "private")}
		client, closeSessions := connectTestClient(t, New("test", Dependencies{
			Jira: func() (JiraReader, error) { return reader, nil },
		}))
		defer closeSessions()
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "jira_issue_graph", Arguments: map[string]any{"key": "PROJ-1", "max_bytes": 1024},
		})
		if err != nil || !result.IsError || result.StructuredContent != nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if strings.Contains(text, "private") || !strings.Contains(text, "exceeds max_bytes") || !strings.Contains(text, "narrow_graph_or_raise_bound") {
			t.Fatalf("error=%q", text)
		}
	})
}

func TestJiraIssueGraphToolSchemaHasNoExpansionOrNarrativeFields(t *testing.T) {
	client, closeSessions := connectTestClient(t, New("test", Dependencies{}))
	defer closeSessions()
	for _, guidance := range []string{"jira_issue_graph", "depth 0..2", "performs no Confluence reads", "omits labels", "Development", "owner-approved host", "separately authenticated"} {
		if !strings.Contains(client.InitializeResult().Instructions, guidance) {
			t.Errorf("instructions omit %q", guidance)
		}
	}
	listed, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != "jira_issue_graph" {
			continue
		}
		input, _ := json.Marshal(tool.InputSchema)
		output, _ := json.Marshal(tool.OutputSchema)
		if !strings.Contains(string(input), `"include_development"`) {
			t.Errorf("input schema omits include_development: %s", input)
		}
		for _, forbidden := range []string{"resolve_confluence", "snippet", "raw"} {
			if strings.Contains(string(input), forbidden) {
				t.Errorf("input schema contains %q: %s", forbidden, input)
			}
		}
		if !strings.Contains(string(output), `"scm"`) || !strings.Contains(string(output), `"merge_request_state"`) {
			t.Errorf("output schema omits closed SCM projection: %s", output)
		}
		for _, forbidden := range []string{`"label"`, "snippet", "narrative", `"message"`, `"email"`, `"avatar"`, `"file"`, `"diff"`, `"timestamp"`, `"raw"`} {
			if strings.Contains(string(output), forbidden) {
				t.Errorf("output schema contains %q: %s", forbidden, output)
			}
		}
		return
	}
	t.Fatal("jira_issue_graph not registered")
}

func TestJiraIssueGraphErrorsAreStatic(t *testing.T) {
	const marker = "PRIVATE-BACKEND-BODY-OR-KEY"
	tests := []error{
		fmt.Errorf("%w: %s", domain.ErrUsage, marker),
		fmt.Errorf("%w: %s", domain.ErrConfig, marker),
		fmt.Errorf("%w: %s", domain.ErrAuth, marker),
		fmt.Errorf("%w: %s", domain.ErrForbidden, marker),
		fmt.Errorf("%w: %s", domain.ErrNotFound, marker),
		fmt.Errorf("%w: %s", domain.ErrCheckFailed, marker),
		fmt.Errorf("%w: %s", domain.ErrOutputLimit, marker),
		&httpx.APIError{Status: 503, Body: marker},
		errors.New(marker),
	}
	for _, test := range tests {
		projected := classifiedJiraIssueGraphRead(test)
		if projected == nil || strings.Contains(projected.Error(), marker) {
			t.Fatalf("error %T crossed private text: %v", test, projected)
		}
	}
}

func TestJiraIssueGraphEvidenceProjectionRejectsNarrativeIdentityTokens(t *testing.T) {
	if safeJiraIssueGraphEvidence(domain.ArtifactGraphEvidence{SourceID: "PRIVATE-BACKEND-BODY"}) ||
		safeJiraIssueGraphEvidence(domain.ArtifactGraphEvidence{JSONPointer: "/fields/api_token"}) {
		t.Fatal("narrative or secret-like evidence identity passed the MCP allowlist")
	}
	if !safeJiraIssueGraphEvidence(domain.ArtifactGraphEvidence{SourceID: "subtasks/0", JSONPointer: "/fields/subtasks/0"}) {
		t.Fatal("canonical structured hierarchy evidence was rejected")
	}
}

func TestJiraIssueGraphSCMProjectionIsClosed(t *testing.T) {
	base := domain.ArtifactGraphNode{
		Kind: "gitlab_project", Service: "gitlab", State: domain.ArtifactNodeStub,
		Depth: 1, Stability: domain.ArtifactStabilityExperimentalAPI,
		SCM: &domain.ArtifactGraphSCMIdentity{Host: "git.example.test", ProjectPath: "group/project"},
	}
	if out, ok := projectJiraIssueGraphSCM(base, true); !ok || out.Host != "git.example.test" || out.ProjectPath != "group/project" {
		t.Fatalf("project projection=%+v ok=%v", out, ok)
	}

	commit := base
	commit.Kind = "gitlab_commit"
	commit.SCM = &domain.ArtifactGraphSCMIdentity{Host: "git.example.test", ProjectPath: "group/project", CommitSHA: strings.Repeat("a", 40)}
	if out, ok := projectJiraIssueGraphSCM(commit, true); !ok || out.CommitSHA != strings.Repeat("a", 40) {
		t.Fatalf("commit projection=%+v ok=%v", out, ok)
	}
	branch := base
	branch.Kind = "gitlab_branch"
	branch.SCM = &domain.ArtifactGraphSCMIdentity{Host: "git.example.test", ProjectPath: "group/project", BranchName: "feature/ветка"}
	if out, ok := projectJiraIssueGraphSCM(branch, true); !ok || out.BranchName != "feature/ветка" {
		t.Fatalf("branch projection=%+v ok=%v", out, ok)
	}
	mr := base
	mr.Kind = "gitlab_merge_request"
	mr.SCM = &domain.ArtifactGraphSCMIdentity{Host: "git.example.test", ProjectPath: "group/project", MergeRequestIID: "17", MergeRequestState: "merged"}
	if out, ok := projectJiraIssueGraphSCM(mr, true); !ok || out.MergeRequestIID != "17" || out.MergeRequestState != "merged" {
		t.Fatalf("merge request projection=%+v ok=%v", out, ok)
	}

	tests := map[string]domain.ArtifactGraphNode{
		"not opted in":          base,
		"mixed case host":       mutateSCMNode(base, func(scm *domain.ArtifactGraphSCMIdentity) { scm.Host = "Git.example.test" }),
		"unsafe project path":   mutateSCMNode(base, func(scm *domain.ArtifactGraphSCMIdentity) { scm.ProjectPath = "group/../project" }),
		"canonical port":        mutateSCMNode(base, func(scm *domain.ArtifactGraphSCMIdentity) { scm.Host = "git.example.test:443" }),
		"short commit":          mutateSCMNode(commit, func(scm *domain.ArtifactGraphSCMIdentity) { scm.CommitSHA = "abc123" }),
		"commit selector clash": mutateSCMNode(commit, func(scm *domain.ArtifactGraphSCMIdentity) { scm.BranchName = "main" }),
		"control branch":        mutateSCMNode(branch, func(scm *domain.ArtifactGraphSCMIdentity) { scm.BranchName = "feature\nunsafe" }),
		"zero IID":              mutateSCMNode(mr, func(scm *domain.ArtifactGraphSCMIdentity) { scm.MergeRequestIID = "0" }),
		"missing MR state":      mutateSCMNode(mr, func(scm *domain.ArtifactGraphSCMIdentity) { scm.MergeRequestState = "" }),
		"wrong stability":       func() domain.ArtifactGraphNode { n := base; n.Stability = domain.ArtifactStabilityPublicAPI; return n }(),
		"expanded GitLab node":  func() domain.ArtifactGraphNode { n := base; n.Expanded = true; return n }(),
	}
	for name, node := range tests {
		include := name != "not opted in"
		if out, ok := projectJiraIssueGraphSCM(node, include); ok || out != nil {
			t.Errorf("%s projection=%+v ok=%v", name, out, ok)
		}
	}
}

func mutateSCMNode(node domain.ArtifactGraphNode, mutate func(*domain.ArtifactGraphSCMIdentity)) domain.ArtifactGraphNode {
	copySCM := *node.SCM
	node.SCM = &copySCM
	mutate(node.SCM)
	return node
}

func defaultMCPGraphOptions(depth int) app.JiraIssueGraphOptions {
	return app.JiraIssueGraphOptions{
		Depth: depth, MaxNodes: 50, MaxEdges: 200, MaxEvidence: 500,
		MaxRequests: 50, MaxResponseBytes: 16 << 20,
	}
}

func validMCPGraphResult(key string, opts app.JiraIssueGraphOptions, label string) *app.JiraIssueGraphResult {
	depth := 0
	kinds := []string{"issue_fields", "issue_links", "hierarchy", "attachments", "issue_properties", "comments", "worklogs", "remote_links"}
	if opts.IncludeDevelopment {
		kinds = append(kinds, "development")
	}
	sources := make([]domain.ArtifactGraphSource, 0, len(kinds))
	for _, kind := range kinds {
		stability := domain.ArtifactStabilityPublicAPI
		if kind == "issue_properties" {
			stability = domain.ArtifactStabilityExperimentalAPI
		}
		if kind == "development" {
			stability = domain.ArtifactStabilityExperimentalAPI
		}
		sources = append(sources, domain.ArtifactGraphSource{
			NodeID: "jira:issue:" + key, NodeDepth: &depth, Kind: kind, Requested: true,
			Status: domain.ArtifactSourceEmpty, Complete: true, Stability: stability,
		})
	}
	return &app.JiraIssueGraphResult{
		SchemaVersion: 2, RootID: "jira:issue:" + key, Complete: true,
		Bounds: app.JiraIssueGraphBounds{
			RequestedDepth: opts.Depth, MaxNodes: opts.MaxNodes, MaxEdges: opts.MaxEdges,
			MaxEvidence: opts.MaxEvidence, MaxSourceBytes: 1 << 20,
			ExpandedNodes: 1, AttemptedNodes: 1, MaxRequests: opts.MaxRequests,
			MaxResponseBytes: opts.MaxResponseBytes, MaxSources: opts.MaxNodes*len(kinds) + 1,
			MaxFrontier:        opts.MaxNodes,
			IncludeDevelopment: opts.IncludeDevelopment,
		},
		Summary: app.JiraIssueGraphSummary{
			NodeCount: 1, SourceCount: len(kinds),
			SourceStatusCounts:    map[string]int{"complete": 0, "empty": len(kinds), "partial": 0, "forbidden": 0, "unsupported": 0, "skipped": 0},
			NodeCountMatchesNodes: true, EdgeCountMatchesEdges: true,
			EvidenceCountMatchesEdges: true, SourceCountMatchesSources: true,
			SourceStatusCountsMatch: true, IncompleteCountMatches: true,
			ExpandedCountMatchesNodes: true, CompleteMatchesSources: true,
		},
		Nodes: []domain.ArtifactGraphNode{{
			ID: "jira:issue:" + key, Kind: "jira_issue", Service: "jira", ExternalID: key,
			Label: label, State: domain.ArtifactNodeResolved, Expanded: true, Depth: 0,
			Stability: domain.ArtifactStabilityPublicAPI,
		}},
		Edges: []domain.ArtifactGraphEdge{}, Sources: sources,
		Frontier: []app.JiraIssueGraphFrontierItem{}, Warnings: []string{},
	}
}

func validMCPDevelopmentGraphResult(key string, opts app.JiraIssueGraphOptions) *app.JiraIssueGraphResult {
	result := validMCPGraphResult(key, opts, "discarded issue label")
	const (
		host        = "git.example.test"
		projectPath = "group/project"
		commitSHA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	rootID := "jira:issue:" + key
	projectHash := testJiraGraphHash("https://" + host + "\x00" + projectPath)
	projectID := "gitlab:project:" + projectHash
	commitID := "gitlab:commit:" + projectHash + ":" + commitSHA
	commitSCM := &domain.ArtifactGraphSCMIdentity{Host: host, ProjectPath: projectPath, CommitSHA: commitSHA}
	projectSCM := &domain.ArtifactGraphSCMIdentity{Host: host, ProjectPath: projectPath}
	result.Nodes = append(result.Nodes,
		domain.ArtifactGraphNode{ID: commitID, Kind: "gitlab_commit", Service: "gitlab", URL: "https://" + host + "/" + projectPath + "/-/commit/" + commitSHA, State: domain.ArtifactNodeStub, Depth: 1, Stability: domain.ArtifactStabilityExperimentalAPI, SCM: commitSCM},
		domain.ArtifactGraphNode{ID: projectID, Kind: "gitlab_project", Service: "gitlab", URL: "https://" + host + "/" + projectPath, State: domain.ArtifactNodeStub, Depth: 1, Stability: domain.ArtifactStabilityExperimentalAPI, SCM: projectSCM},
	)
	makeEdge := func(kind, target string, scm *domain.ArtifactGraphSCMIdentity) domain.ArtifactGraphEdge {
		edge := domain.ArtifactGraphEdge{
			From: rootID, To: target, Kind: kind, Direction: "outbound", Current: true,
			Confidence: "exact", Stability: domain.ArtifactStabilityExperimentalAPI,
			Evidence: []domain.ArtifactGraphEvidence{{
				Collector: "development", SourceNodeID: rootID, SourceKind: "development_detail",
				SourceID:   testJiraGraphHash(strings.Join([]string{kind, scm.Host, scm.ProjectPath, scm.CommitSHA, scm.BranchName, scm.MergeRequestIID}, "\x00")),
				Extraction: "structured",
			}},
		}
		edge.ID = testJiraGraphEdgeID(edge)
		return edge
	}
	result.Edges = []domain.ArtifactGraphEdge{
		makeEdge("development_commit", commitID, commitSCM),
		makeEdge("development_project", projectID, projectSCM),
	}
	result.Sources[len(result.Sources)-1].Status = domain.ArtifactSourceComplete
	result.Sources[len(result.Sources)-1].Count = 1
	result.Summary.NodeCount = 3
	result.Summary.EdgeCount = 2
	result.Summary.EvidenceCount = 2
	result.Summary.SourceStatusCounts["empty"]--
	result.Summary.SourceStatusCounts["complete"]++
	return result
}

func testJiraGraphHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func testJiraGraphEdgeID(edge domain.ArtifactGraphEdge) string {
	return "edge:" + testJiraGraphHash(strings.Join([]string{
		"atl-jira-graph-edge-v1", edge.From, edge.To, edge.Kind,
		edge.RelationType, edge.Relation, edge.Direction,
	}, "\x00"))
}
