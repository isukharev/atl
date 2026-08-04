package agenteval

import (
	"bytes"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

const (
	confluenceCommentListTool     = "confluence_comment_list"
	confluenceCommentThreadTool   = "confluence_comment_thread"
	confluenceCommentListFamily   = "confluence.comment.list"
	confluenceCommentThreadFamily = "confluence.comment.thread"
)

func TestConfluenceCommentRoutingCorpusContracts(t *testing.T) {
	tests := []struct {
		directory   string
		repetitions int
		final       string
	}{
		{
			directory: "confluence-comment-routing-mcp", repetitions: 3,
			final: `{"schema_version":1,"page_version":7,"inventory_complete":true,"selected_comment_id":"5101","thread_complete":true,"thread_text":"Synthetic approval remains pending.","brief":"Synthetic thread read."}`,
		},
		{
			directory: "confluence-comment-routing-mcp-holdout", repetitions: 1,
			final: `{"schema_version":1,"inventory_count":1,"inventory_complete":false,"list_page_version":4,"thread_page_version":9,"thread_page_version_gated":false,"thread_complete":true,"thread_text":"Synthetic rollout is paused.","brief":"Synthetic inventory is partial and the fixed thread is complete."}`,
		},
	}
	for _, test := range tests {
		t.Run(test.directory, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			var baseline RunSpec
			for _, provider := range []string{"claude", "codex"} {
				loaded, err := resolveRunContract(filepath.Join(root, "run.mcp."+provider+".json"))
				if err != nil {
					t.Fatalf("load %s: %v", provider, err)
				}
				spec := loaded.spec
				if spec.EffectiveBackendMode() != BackendModeSynthetic || spec.EffectiveSurface() != SurfaceATLMCP ||
					spec.EffectiveToolTransport() != "mcp" || spec.Repetitions != test.repetitions ||
					!slices.Equal(spec.AllowedMCPTools, []string{confluenceCommentListTool, confluenceCommentThreadTool}) ||
					len(spec.AllowedTools) != 0 || len(spec.AllowedATLCommands) != 0 || len(spec.AllowedCLICommands) != 0 ||
					spec.AllowSyntheticWrites || spec.AllowLiveWrites {
					t.Fatalf("%s route authority drifted: %+v", provider, spec)
				}
				if loaded.scenario.Budgets.MaxRemoteWrites != 0 ||
					!slices.Equal(loaded.scenario.Budgets.AllowedHTTPMethods, []string{"GET"}) {
					t.Fatalf("%s read-only budget drifted: %+v", provider, loaded.scenario.Budgets)
				}
				if provider == "claude" {
					baseline = spec
				} else if !slices.Equal(baseline.AllowedMCPTools, spec.AllowedMCPTools) {
					t.Fatal("provider tool inventories diverged")
				}
				assertConfluenceCommentChecks(t, spec, []byte(test.final), test.directory)
			}
		})
	}
}

func TestConfluenceCommentRoutingFixturesDriveSelectedATLBinary(t *testing.T) {
	tests := []struct {
		directory           string
		listPageID          string
		threadPageID        string
		commentID           string
		wantInventoryFull   bool
		wantListVersion     int
		wantThreadVersion   int
		wantThreadVersioned bool
		wantText            string
		wantListState       string
		wantListItems       int
		wantDuplicates      int
	}{
		{
			"confluence-comment-routing-mcp", "9101", "9101", "5101",
			true, 7, 7, true, "Synthetic approval remains pending.", "open", 10, 1,
		},
		{
			"confluence-comment-routing-mcp-holdout", "9201", "9202", "6202",
			false, 4, 9, false, "Synthetic rollout is paused.", "all", 1, 0,
		},
	}
	for _, test := range tests {
		t.Run(test.directory, func(t *testing.T) {
			root := filepath.Join("..", "..", "benchmarks", "agent-eval", test.directory)
			loaded, err := resolveRunContract(filepath.Join(root, "run.mcp.codex.json"))
			if err != nil {
				t.Fatal(err)
			}
			invocations := confluenceCommentExpectedInvocations(t, loaded.spec)
			fixture := confluenceCommentRoutingProcessFixture(t,
				loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json")), test.directory)
			process := startRepositoryConfluenceEvidenceProcess(t, fixture, invocations)

			listResult, message, ok := callRepositoryConfluenceEvidence(t, process, invocations[0])
			if !ok {
				t.Fatalf("the exact comment-list read was refused: %s", message)
			}
			list, err := DecodeConfluenceCommentListView(bytes.NewReader(listResult.StructuredContent))
			if err != nil {
				t.Fatalf("decode Confluence comment list: %v", err)
			}
			if list.Complete != test.wantInventoryFull || list.PageVersion != test.wantListVersion ||
				list.PageID != test.listPageID || list.PageVersionGated || list.Count != 1 ||
				len(list.Comments) != 1 || list.Comments[0].ID == "" ||
				list.Query.Mode != "list" || list.Query.Location != "footer" ||
				list.Query.State != test.wantListState || list.Query.Depth != "root" ||
				list.Bounds.MaxCommentPages != 32 || list.Bounds.MaxItems != test.wantListItems ||
				list.Bounds.MaxBytes != 65536 {
				t.Fatalf("list result drifted: %+v", list)
			}

			threadInvocation := invocations[1]
			if test.directory == "confluence-comment-routing-mcp" {
				threadInvocation = confluenceCommentListDerivedThreadInvocation(t, list)
			}
			threadResult, message, ok := callRepositoryConfluenceEvidence(t, process, threadInvocation)
			if !ok {
				t.Fatalf("the exact comment-thread read was refused: %s", message)
			}
			thread, err := DecodeConfluenceCommentThreadView(bytes.NewReader(threadResult.StructuredContent))
			if err != nil {
				t.Fatalf("decode Confluence comment thread: %v", err)
			}
			if !thread.Complete || thread.PageVersion != test.wantThreadVersion ||
				thread.PageID != test.threadPageID || thread.PageVersionGated != test.wantThreadVersioned ||
				thread.Query.Mode != "thread" || thread.Query.CommentID != test.commentID ||
				thread.Query.Location != "all" || thread.Query.State != "all" || thread.Query.Depth != "all" ||
				thread.Bounds.MaxCommentPages != 32 || thread.Bounds.MaxItems != 10 ||
				thread.Bounds.MaxBytes != 65536 || len(thread.Comments) != 1 ||
				thread.Comments[0].ID != test.commentID ||
				thread.Comments[0].BodyText == nil || *thread.Comments[0].BodyText != test.wantText {
				t.Fatalf("thread result drifted: %+v", thread)
			}

			summary := process.Summary()
			if !process.RequestSequenceComplete() || summary.UnexpectedRequests != 0 ||
				summary.DuplicateRequests != test.wantDuplicates ||
				!equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": 6}) ||
				!equalHTTPMethods(summary.MCPInvocations, map[string]int{
					confluenceCommentListTool: 1, confluenceCommentThreadTool: 1,
				}) {
				t.Fatalf("backend route drifted: summary=%+v sequence_complete=%t",
					summary, process.RequestSequenceComplete())
			}
		})
	}
}

func TestConfluenceCommentRoutingDerivedThreadDivergenceRefusesBeforeBackend(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "confluence-comment-routing-mcp")
	loaded, err := resolveRunContract(filepath.Join(root, "run.mcp.codex.json"))
	if err != nil {
		t.Fatal(err)
	}
	invocations := confluenceCommentExpectedInvocations(t, loaded.spec)
	fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
	var page map[string]any
	if err := json.Unmarshal(fixture.Routes[0].Responses[0].Body, &page); err != nil {
		t.Fatal(err)
	}
	version, ok := page["version"].(map[string]any)
	if !ok {
		t.Fatalf("primary fixture page version is not an object: %v", page["version"])
	}
	version["number"] = float64(8)
	fixture.Routes[0].Responses[0].Body, err = json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	process := startRepositoryConfluenceEvidenceProcess(t,
		confluenceCommentRoutingProcessFixture(t, fixture, "confluence-comment-routing-mcp"), invocations)

	listResult, message, ok := callRepositoryConfluenceEvidence(t, process, invocations[0])
	if !ok {
		t.Fatalf("the drifted list read was refused before it exposed the changed evidence: %s", message)
	}
	list, err := DecodeConfluenceCommentListView(bytes.NewReader(listResult.StructuredContent))
	if err != nil {
		t.Fatal(err)
	}
	threadInvocation := confluenceCommentListDerivedThreadInvocation(t, list)
	if equalMCPInvocations([]MCPInvocation{threadInvocation}, invocations[1:]) {
		t.Fatal("changed list evidence reproduced the committed thread invocation")
	}
	if _, message, ok := callRepositoryConfluenceEvidence(t, process, threadInvocation); ok || message == "" {
		t.Fatalf("unadmitted list-derived thread reached the backend: ok=%t message=%q", ok, message)
	}

	summary := process.Summary()
	if process.RequestSequenceComplete() || summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
		!equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": 2}) ||
		!equalHTTPMethods(summary.MCPInvocations, map[string]int{confluenceCommentListTool: 1}) {
		t.Fatalf("derived-route refusal was not pre-backend: summary=%+v sequence_complete=%t",
			summary, process.RequestSequenceComplete())
	}
}

func confluenceCommentListDerivedThreadInvocation(
	t *testing.T,
	list ConfluenceCommentListView,
) MCPInvocation {
	t.Helper()
	if list.PageID == "" || list.PageVersion < 1 || len(list.Comments) != 1 || list.Comments[0].ID == "" {
		t.Fatalf("comment-list evidence cannot authorize one exact thread follow-up: %+v", list)
	}
	return mustMCPInvocation(t, confluenceCommentThreadTool, map[string]any{
		"page_id": list.PageID, "comment_id": list.Comments[0].ID,
		"expected_page_version": list.PageVersion, "max_items": 10, "max_bytes": 65536,
	})
}

func confluenceCommentRoutingProcessFixture(
	t *testing.T,
	fixture MockFixture,
	directory string,
) MockFixture {
	t.Helper()
	pageQuery := map[string]string{
		"expand": "body.storage,version,space,ancestors,metadata.labels",
	}
	commentQuery := func(location, parentVersion string, depthAll bool) map[string]string {
		query := map[string]string{
			"expand": "body.storage,history,version,ancestors,extensions.inlineProperties,extensions.resolution",
			"limit":  "100", "location": location, "parentVersion": parentVersion, "start": "0",
		}
		if depthAll {
			query["depth"] = "all"
		}
		return query
	}
	retainedCommentQuery := func(location string, depthAll bool) map[string]string {
		query := commentQuery(location, "unused", depthAll)
		delete(query, "parentVersion")
		return query
	}

	prepared := fixture
	switch directory {
	case "confluence-comment-routing-mcp":
		if len(fixture.Routes) != 4 {
			t.Fatalf("%s fixture has %d routes, want 4", directory, len(fixture.Routes))
		}
		page := confluenceCommentRetainedRoute(t, directory, fixture.Routes[0],
			"/wiki/rest/api/content/9101", pageQuery)
		footer := confluenceCommentRetainedRoute(t, directory, fixture.Routes[1],
			"/wiki/rest/api/content/9101/child/comment", retainedCommentQuery("footer", false))
		inline := confluenceCommentRetainedRoute(t, directory, fixture.Routes[2],
			"/wiki/rest/api/content/9101/child/comment", retainedCommentQuery("inline", true))
		resolved := confluenceCommentRetainedRoute(t, directory, fixture.Routes[3],
			"/wiki/rest/api/content/9101/child/comment", retainedCommentQuery("resolved", true))
		listVersion := confluenceCommentFixturePageVersion(t, page, 0)
		threadVersion := confluenceCommentFixturePageVersion(t, page, 1)
		prepared.Routes = []MockRoute{
			confluenceCommentClosedRoute(t, page, "primary-page", pageQuery, -1),
			confluenceCommentClosedRoute(t, footer, "primary-footer-list", commentQuery("footer", listVersion, false), 0),
			confluenceCommentClosedRoute(t, footer, "primary-footer-thread", commentQuery("footer", threadVersion, true), 1),
			confluenceCommentClosedRoute(t, inline, "primary-inline-thread", commentQuery("inline", threadVersion, true), -1),
			confluenceCommentClosedRoute(t, resolved, "primary-resolved-thread", commentQuery("resolved", threadVersion, true), -1),
		}
		prepared.RequestSequence = []string{
			"primary-page", "primary-footer-list", "primary-page", "primary-footer-thread",
			"primary-inline-thread", "primary-resolved-thread",
		}
	case "confluence-comment-routing-mcp-holdout":
		if len(fixture.Routes) != 6 {
			t.Fatalf("%s fixture has %d routes, want 6", directory, len(fixture.Routes))
		}
		listPage := confluenceCommentRetainedRoute(t, directory, fixture.Routes[0],
			"/wiki/rest/api/content/9201", pageQuery)
		listFooter := confluenceCommentRetainedRoute(t, directory, fixture.Routes[1],
			"/wiki/rest/api/content/9201/child/comment", retainedCommentQuery("footer", false))
		threadPage := confluenceCommentRetainedRoute(t, directory, fixture.Routes[2],
			"/wiki/rest/api/content/9202", pageQuery)
		threadFooter := confluenceCommentRetainedRoute(t, directory, fixture.Routes[3],
			"/wiki/rest/api/content/9202/child/comment", retainedCommentQuery("footer", true))
		threadInline := confluenceCommentRetainedRoute(t, directory, fixture.Routes[4],
			"/wiki/rest/api/content/9202/child/comment", retainedCommentQuery("inline", true))
		threadResolved := confluenceCommentRetainedRoute(t, directory, fixture.Routes[5],
			"/wiki/rest/api/content/9202/child/comment", retainedCommentQuery("resolved", true))
		listVersion := confluenceCommentFixturePageVersion(t, listPage, -1)
		threadVersion := confluenceCommentFixturePageVersion(t, threadPage, -1)
		prepared.Routes = []MockRoute{
			confluenceCommentClosedRoute(t, listPage, "holdout-list-page", pageQuery, -1),
			confluenceCommentClosedRoute(t, listFooter, "holdout-list-footer", commentQuery("footer", listVersion, false), -1),
			confluenceCommentClosedRoute(t, threadPage, "holdout-thread-page", pageQuery, -1),
			confluenceCommentClosedRoute(t, threadFooter, "holdout-thread-footer", commentQuery("footer", threadVersion, true), -1),
			confluenceCommentClosedRoute(t, threadInline, "holdout-thread-inline", commentQuery("inline", threadVersion, true), -1),
			confluenceCommentClosedRoute(t, threadResolved, "holdout-thread-resolved", commentQuery("resolved", threadVersion, true), -1),
		}
		prepared.RequestSequence = []string{
			"holdout-list-page", "holdout-list-footer", "holdout-thread-page",
			"holdout-thread-footer", "holdout-thread-inline", "holdout-thread-resolved",
		}
	default:
		t.Fatalf("unsupported Confluence comment-routing cohort %q", directory)
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Confluence comment-routing fixture: %v", err)
	}
	return prepared
}

func confluenceCommentRetainedRoute(
	t *testing.T,
	directory string,
	route MockRoute,
	path string,
	query map[string]string,
) MockRoute {
	t.Helper()
	if route.Method != "GET" || route.Path != path || len(route.QueryContains) != 0 ||
		!maps.Equal(route.QueryEquals, query) {
		t.Fatalf("%s retained route drifted: %+v", directory, route)
	}
	return route
}

func confluenceCommentClosedRoute(
	t *testing.T,
	route MockRoute,
	name string,
	query map[string]string,
	responseIndex int,
) MockRoute {
	t.Helper()
	if responseIndex >= 0 {
		if responseIndex >= len(route.Responses) {
			t.Fatalf("route %q has no retained response %d", name, responseIndex)
		}
		response := route.Responses[responseIndex]
		route.Status, route.Body, route.Responses = response.Status, response.Body, nil
	}
	route.Name = name
	route.QueryContains = nil
	route.QueryEquals = maps.Clone(query)
	route.closedQuery = true
	return route
}

func confluenceCommentFixturePageVersion(t *testing.T, route MockRoute, responseIndex int) string {
	t.Helper()
	body := route.Body
	if responseIndex >= 0 {
		if responseIndex >= len(route.Responses) {
			t.Fatalf("page route has no retained response %d", responseIndex)
		}
		body = route.Responses[responseIndex].Body
	}
	var page struct {
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
	}
	if err := json.Unmarshal(body, &page); err != nil || page.Version.Number < 1 {
		t.Fatalf("fixture page version is not reconciled: version=%d err=%v", page.Version.Number, err)
	}
	return strconv.Itoa(page.Version.Number)
}

func confluenceCommentExpectedInvocations(t *testing.T, spec RunSpec) []MCPInvocation {
	t.Helper()
	for _, check := range spec.Checks {
		if check.Name == "route_arguments" {
			invocations, ok := expectedMCPInvocations(check.Expected)
			if !ok || len(invocations) != 2 {
				t.Fatal("route_arguments is not an exact two-call route")
			}
			return invocations
		}
	}
	t.Fatal("route_arguments check is missing")
	return nil
}

func assertConfluenceCommentChecks(t *testing.T, spec RunSpec, final []byte, directory string) {
	t.Helper()
	expectedInvocations := confluenceCommentExpectedInvocations(t, spec)
	if len(expectedInvocations) != 2 {
		t.Fatalf("exact route=%v", expectedInvocations)
	}
	if expectedInvocations[0].Tool != confluenceCommentListTool || expectedInvocations[1].Tool != confluenceCommentThreadTool {
		t.Fatalf("inventory and expansion routes widened: %v", expectedInvocations)
	}
	var listArguments, threadArguments map[string]any
	if err := json.Unmarshal(expectedInvocations[0].Arguments, &listArguments); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expectedInvocations[1].Arguments, &threadArguments); err != nil {
		t.Fatal(err)
	}
	if directory == "confluence-comment-routing-mcp" {
		if listArguments["page_id"] != "9101" || threadArguments["page_id"] != "9101" ||
			threadArguments["expected_page_version"] != float64(7) {
			t.Fatalf("list-derived thread lost page/version binding: list=%v thread=%v", listArguments, threadArguments)
		}
	} else if listArguments["page_id"] != "9201" || threadArguments["page_id"] != "9202" ||
		threadArguments["comment_id"] != "6202" || threadArguments["expected_page_version"] != nil {
		t.Fatalf("externally fixed thread gained list provenance: list=%v thread=%v", listArguments, threadArguments)
	}
	families := []CapabilityFamilyMetric{
		{Family: confluenceCommentListFamily, Invocations: 1, Successes: 1},
		{Family: confluenceCommentThreadFamily, Invocations: 1, Successes: 1},
	}
	sequence := []string{confluenceCommentListFamily, confluenceCommentThreadFamily}
	checks, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 2, 0, 0, 0, nil, 0, 0,
		map[string]int{"GET": 6}, true, nil, families, true, sequence,
		expectedInvocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range spec.Checks {
		if !checks[check.Name] {
			t.Fatalf("load-bearing check %q failed on retained evidence: %v", check.Name, checks)
		}
	}

	wrongTool := slices.Clone(expectedInvocations)
	wrongTool[1].Tool = confluenceCommentListTool
	toolChecks, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 2, 0, 0, 0, nil, 0, 0,
		map[string]int{"GET": 6}, true, nil, families, true, sequence,
		wrongTool, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if toolChecks["route_arguments"] {
		t.Fatal("wrong tool selection passed the exact route oracle")
	}

	mutated := slices.Clone(expectedInvocations)
	var arguments map[string]any
	if err := json.Unmarshal(mutated[1].Arguments, &arguments); err != nil {
		t.Fatal(err)
	}
	if _, gated := arguments["expected_page_version"]; gated {
		arguments["expected_page_version"] = 8
	} else {
		arguments["expected_page_version"] = 9
	}
	mutated[1], _ = newMCPInvocation(confluenceCommentThreadTool, arguments)
	mutatedChecks, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, final, "", 2, 0, 0, 0, nil, 0, 0,
		map[string]int{"GET": 6}, true, nil, families, true, sequence, mutated, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if mutatedChecks["route_arguments"] {
		t.Fatal("version-gate widening passed the exact route oracle")
	}

	var answer map[string]any
	if err := json.Unmarshal(final, &answer); err != nil {
		t.Fatal(err)
	}
	answer["inventory_complete"] = !answer["inventory_complete"].(bool)
	wrongCompleteness, _ := json.Marshal(answer)
	completenessChecks, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, wrongCompleteness, "", 2, 0, 0, 0, nil, 0, 0,
		map[string]int{"GET": 6}, true, nil, families, true, sequence,
		expectedInvocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if completenessChecks["inventory_complete_exact"] {
		t.Fatal("completeness widening passed the exact semantic oracle")
	}
}
