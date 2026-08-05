package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
)

const (
	crossServiceDiscoveryJiraSearchRoute       = "cross-service-jira-search"
	crossServiceDiscoveryConfluenceSearchRoute = "cross-service-confluence-search"
	crossServiceDiscoveryPageRoute             = "cross-service-page"
	crossServiceDiscoveryJiraFieldsRoute       = "cross-service-jira-fields"
	crossServiceDiscoveryJiraIssueRoute        = "cross-service-jira-issue"

	crossServiceDiscoveryPageExpand = "body.storage,version,space,ancestors,metadata.labels"
)

// crossServiceDiscoveryProcessEvidence contains only strict evaluator-owned
// projections produced by the exact selected ATL MCP process.
type crossServiceDiscoveryProcessEvidence struct {
	JiraSearch       JiraSnapshotIssueList
	ConfluenceSearch ConfluenceSearchPageView
	Outline          ConfluencePageOutlineView
	Section          ConfluencePageSectionView
	Field            JiraSnapshotFieldEvidence
	Invocations      []MCPInvocation
	Summary          SyntheticATLProcessSummary
}

type crossServiceDiscoveryOutlineSelection struct {
	Heading    string
	Path       []string
	Occurrence int
}

// driveCrossServiceDiscoveryProcess executes the retained five-tool route
// against one selected default MCP process. Every downstream tool argument is
// reconstructed from the strictly decoded result that selected it, then
// reconciled with the closed admission before the process can reach a backend.
func driveCrossServiceDiscoveryProcess(
	t *testing.T,
	fixture MockFixture,
	expected crossServiceDiscoveryExpectation,
	fieldSelector string,
) crossServiceDiscoveryProcessEvidence {
	t.Helper()
	admitted := crossServiceDiscoveryMCPInvocations(t, expected, fieldSelector)
	process := startCrossServiceDiscoveryProcess(t, fixture, expected, fieldSelector, admitted)
	return executeCrossServiceDiscoveryProcess(t, process, expected, admitted)
}

func startCrossServiceDiscoveryProcess(
	t *testing.T,
	fixture MockFixture,
	expected crossServiceDiscoveryExpectation,
	fieldSelector string,
	admitted []MCPInvocation,
) *SyntheticATLProcess {
	t.Helper()
	prepared := prepareCrossServiceDiscoveryFixture(t, fixture, expected, fieldSelector, admitted)
	process, err := StartSyntheticATLProcess(context.Background(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepared,
		ScratchRoot: privateSyntheticATLScratch(t),
		MCPService:  "default", MCPInvocations: slices.Clone(admitted),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic cross-service discovery process: %v", err)
		}
	})
	return process
}

// prepareCrossServiceDiscoveryFixture leaves retained corpus bytes unchanged
// and applies evaluator-only exact request policy to a private copy. The
// sequence independently verifies the product's actual HTTP behavior after the
// closed selected-process admission.
func prepareCrossServiceDiscoveryFixture(
	t *testing.T,
	fixture MockFixture,
	expected crossServiceDiscoveryExpectation,
	fieldSelector string,
	admitted []MCPInvocation,
) MockFixture {
	t.Helper()
	if len(admitted) != 5 || len(fixture.Routes) != 5 || len(fixture.RequestSequence) != 0 {
		t.Fatalf("cross-service fixture/admission shape drifted: admissions=%d routes=%d sequence=%v",
			len(admitted), len(fixture.Routes), fixture.RequestSequence)
	}
	if fieldSelector != "Description" && fieldSelector != "description" {
		t.Fatalf("cross-service field selector %q is outside the retained route alternatives", fieldSelector)
	}

	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)

	crossServiceDiscoveryAssertRetainedRoute(t, prepared.Routes[0], fixture.JiraContext+"/rest/api/2/search",
		map[string]string{"jql": expected.jiraQuery})
	prepared.Routes[0] = crossServiceDiscoveryClosedRoute(prepared.Routes[0], crossServiceDiscoveryJiraSearchRoute,
		map[string]string{
			"jql": expected.jiraQuery, "startAt": "0", "maxResults": "10", "fields": "summary,status,updated",
		})

	crossServiceDiscoveryAssertRetainedRoute(t, prepared.Routes[1], fixture.ConfluenceContext+"/rest/api/search",
		map[string]string{"cql": expected.confluenceQuery})
	prepared.Routes[1] = crossServiceDiscoveryClosedRoute(prepared.Routes[1], crossServiceDiscoveryConfluenceSearchRoute,
		map[string]string{
			"cql": expected.confluenceQuery, "limit": "10", "start": "0", "expand": "content.version,content.space",
		})

	crossServiceDiscoveryAssertRetainedRoute(t, prepared.Routes[2],
		fixture.ConfluenceContext+"/rest/api/content/"+expected.pageID, nil)
	prepared.Routes[2] = crossServiceDiscoveryClosedRoute(prepared.Routes[2], crossServiceDiscoveryPageRoute,
		map[string]string{"expand": crossServiceDiscoveryPageExpand})

	crossServiceDiscoveryAssertRetainedRoute(t, prepared.Routes[3], fixture.JiraContext+"/rest/api/2/field", nil)
	prepared.Routes[3] = crossServiceDiscoveryClosedRoute(prepared.Routes[3], crossServiceDiscoveryJiraFieldsRoute, nil)

	crossServiceDiscoveryAssertRetainedRoute(t, prepared.Routes[4],
		fixture.JiraContext+"/rest/api/2/issue/"+expected.jiraKey, nil)
	prepared.Routes[4] = crossServiceDiscoveryClosedRoute(prepared.Routes[4], crossServiceDiscoveryJiraIssueRoute,
		map[string]string{"fields": "description,updated"})

	sequence := []string{
		crossServiceDiscoveryJiraSearchRoute,
		crossServiceDiscoveryConfluenceSearchRoute,
		crossServiceDiscoveryPageRoute,
		crossServiceDiscoveryPageRoute,
	}
	if fieldSelector == "Description" {
		sequence = append(sequence, crossServiceDiscoveryJiraFieldsRoute)
	}
	sequence = append(sequence, crossServiceDiscoveryJiraIssueRoute)
	prepared.RequestSequence = sequence
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare cross-service selected-process fixture: %v", err)
	}
	return prepared
}

func crossServiceDiscoveryAssertRetainedRoute(
	t *testing.T,
	route MockRoute,
	path string,
	query map[string]string,
) {
	t.Helper()
	if route.Name != "" || route.Method != "GET" || route.Path != path || route.Status != 200 ||
		!json.Valid(route.Body) || len(route.Responses) != 0 || len(route.RequestBody) != 0 ||
		len(route.QueryContains) != 0 || !maps.Equal(route.QueryEquals, query) || route.closedQuery {
		t.Fatalf("retained cross-service route drifted: path=%q route=%+v", path, route)
	}
}

func crossServiceDiscoveryClosedRoute(route MockRoute, name string, query map[string]string) MockRoute {
	route.Name = name
	route.QueryEquals = maps.Clone(query)
	route.closedQuery = true
	return route
}

func executeCrossServiceDiscoveryProcess(
	t *testing.T,
	process *SyntheticATLProcess,
	expected crossServiceDiscoveryExpectation,
	admitted []MCPInvocation,
) crossServiceDiscoveryProcessEvidence {
	t.Helper()
	observed := make([]MCPInvocation, 0, len(admitted))

	jiraInvocation := admitted[0]
	jiraResult := callCrossServiceDiscoveryMCP(t, process, jiraInvocation)
	jiraSearch, err := DecodeJiraSnapshotIssueList(bytes.NewReader(jiraResult.StructuredContent))
	if err != nil {
		t.Fatalf("decode selected Jira search: %v", err)
	}
	assertCrossServiceDiscoveryJiraSearch(t, jiraSearch, expected)
	observed = append(observed, jiraInvocation)

	confluenceInvocation := admitted[1]
	confluenceResult := callCrossServiceDiscoveryMCP(t, process, confluenceInvocation)
	confluenceSearch, err := DecodeConfluenceSearchPageView(bytes.NewReader(confluenceResult.StructuredContent))
	if err != nil {
		t.Fatalf("decode selected Confluence search: %v", err)
	}
	assertCrossServiceDiscoveryConfluenceSearch(t, confluenceSearch, expected)
	observed = append(observed, confluenceInvocation)

	outlineInvocation := mustMCPInvocation(t, "confluence_page_outline", map[string]any{
		"reference": confluenceSearch.Results[0].ID,
	})
	assertCrossServiceDiscoveryDerivedAdmission(t, admitted, len(observed), outlineInvocation)
	outlineResult := callCrossServiceDiscoveryMCP(t, process, outlineInvocation)
	outline, err := DecodeConfluencePageOutlineView(bytes.NewReader(outlineResult.StructuredContent))
	if err != nil {
		t.Fatalf("decode selected Confluence outline: %v", err)
	}
	selection := assertCrossServiceDiscoveryOutline(t, outline, confluenceSearch.Results[0].ID, expected)
	observed = append(observed, outlineInvocation)

	sectionInvocation := mustMCPInvocation(t, "confluence_page_section", map[string]any{
		"reference": confluenceSearch.Results[0].ID, "heading": selection.Heading,
		"occurrence": selection.Occurrence, "expected_page_version": outline.Version,
		"max_bytes": 32768,
	})
	assertCrossServiceDiscoveryDerivedAdmission(t, admitted, len(observed), sectionInvocation)
	sectionResult := callCrossServiceDiscoveryMCP(t, process, sectionInvocation)
	section, err := DecodeConfluencePageSectionView(bytes.NewReader(sectionResult.StructuredContent))
	if err != nil {
		t.Fatalf("decode selected Confluence section: %v", err)
	}
	if !section.Complete || section.Truncated || section.ID != outline.ID || section.Version != outline.Version ||
		!section.PageVersionGated || section.Heading != selection.Heading || section.Occurrence != selection.Occurrence ||
		!slices.Equal(section.Path, selection.Path) {
		t.Fatalf("selected Confluence section drifted: %+v", section)
	}
	assertCrossServiceFragments(t, "section", section.Markdown, expected.requiredSection, expected.rejectedSection)
	observed = append(observed, sectionInvocation)

	fieldSelector := crossServiceDiscoveryFieldSelector(t, admitted[len(observed)])
	fieldInvocation := mustMCPInvocation(t, "jira_issue_field_get", map[string]any{
		"key": jiraSearch.Rows[0].Key, "field": fieldSelector, "max_bytes": 16384,
	})
	assertCrossServiceDiscoveryDerivedAdmission(t, admitted, len(observed), fieldInvocation)
	fieldResult := callCrossServiceDiscoveryMCP(t, process, fieldInvocation)
	field, err := DecodeJiraSnapshotFieldEvidence(bytes.NewReader(fieldResult.StructuredContent))
	if err != nil {
		t.Fatalf("decode selected Jira field: %v", err)
	}
	fieldValue, ok := field.Value.(string)
	if !ok || !field.Complete || field.Truncated || field.Issue.Key != jiraSearch.Rows[0].Key ||
		field.Field.ID != "description" {
		t.Fatalf("selected Jira field evidence drifted: %+v", field)
	}
	if fieldSelector == "Description" && (field.Field.Name != "Description" || field.Field.Schema != "string") {
		t.Fatalf("named Jira field evidence lost catalog metadata: %+v", field.Field)
	}
	if fieldSelector == "description" && (field.Field.Name != "description" || field.Field.Schema != "") {
		t.Fatalf("canonical Jira field evidence drifted: %+v", field.Field)
	}
	assertCrossServiceFragments(t, "field", fieldValue, expected.requiredField, nil)
	observed = append(observed, fieldInvocation)

	if !equalMCPInvocations(admitted, observed) {
		t.Fatalf("cross-service selected-process invocation sequence drifted: admitted=%+v observed=%+v",
			admitted, observed)
	}
	summary := process.Summary()
	expectedGETs := 6
	if fieldSelector == "description" {
		expectedGETs = 5
	}
	if !process.RequestSequenceComplete() || !equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": expectedGETs}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 1 || len(summary.CLIInvocations) != 0 ||
		!equalHTTPMethods(summary.MCPInvocations, map[string]int{
			"jira_issue_search": 1, "confluence_search": 1, "confluence_page_outline": 1,
			"confluence_page_section": 1, "jira_issue_field_get": 1,
		}) {
		t.Fatalf("cross-service selected-process accounting drifted: summary=%+v sequence_complete=%t",
			summary, process.RequestSequenceComplete())
	}
	return crossServiceDiscoveryProcessEvidence{
		JiraSearch: jiraSearch, ConfluenceSearch: confluenceSearch, Outline: outline, Section: section, Field: field,
		Invocations: observed, Summary: summary,
	}
}

func callCrossServiceDiscoveryMCP(
	t *testing.T,
	process *SyntheticATLProcess,
	invocation MCPInvocation,
) SyntheticMCPResult {
	t.Helper()
	result, err := process.CallMCPJSON(context.Background(), invocation)
	if err != nil {
		t.Fatalf("selected cross-service MCP %s: %v", invocation.Tool, err)
	}
	if result.IsError {
		t.Fatalf("selected cross-service MCP %s failed: %s", invocation.Tool, strings.Join(result.TextContent, "\n"))
	}
	assertRepositoryMCPTextMatchesStructured(t, result)
	return result
}

func assertCrossServiceDiscoveryJiraSearch(
	t *testing.T,
	search JiraSnapshotIssueList,
	expected crossServiceDiscoveryExpectation,
) {
	t.Helper()
	if search.Selection["jql"] != expected.jiraQuery || !search.Page.Complete || search.Page.Truncated ||
		search.Page.NextCursor != nil || len(search.Rows) != 3 || search.Rows[0].Key != expected.jiraKey {
		t.Fatalf("selected Jira candidate search drifted: %+v", search)
	}
	status, ok := search.Rows[0].Values["status"].(string)
	if !ok || status != expected.status {
		t.Fatalf("selected Jira candidate status drifted: %+v", search.Rows[0])
	}
}

func assertCrossServiceDiscoveryConfluenceSearch(
	t *testing.T,
	search ConfluenceSearchPageView,
	expected crossServiceDiscoveryExpectation,
) {
	t.Helper()
	if search.Query != expected.confluenceQuery || !search.Complete || search.Truncated || search.NextCursor != nil ||
		len(search.Results) != 3 || search.Results[0].ID != expected.pageID {
		t.Fatalf("selected Confluence candidate search drifted: %+v", search)
	}
}

func assertCrossServiceDiscoveryOutline(
	t *testing.T,
	outline ConfluencePageOutlineView,
	pageID string,
	expected crossServiceDiscoveryExpectation,
) crossServiceDiscoveryOutlineSelection {
	t.Helper()
	if !outline.Complete || outline.Truncated || outline.ID != pageID || outline.Version != expected.pageVersion {
		t.Fatalf("selected Confluence outline drifted: %+v", outline)
	}
	var selected crossServiceDiscoveryOutlineSelection
	headingCount := 0
	for _, entry := range outline.Headings {
		if entry.Title != expected.heading {
			continue
		}
		headingCount++
		if entry.Occurrence != headingCount {
			t.Fatalf("non-contiguous selected heading occurrences: %+v", outline.Headings)
		}
		if slices.Equal(entry.Path, expected.path) {
			if selected.Heading != "" {
				t.Fatalf("selected structural path is ambiguous: path=%v outline=%+v", expected.path, outline.Headings)
			}
			selected = crossServiceDiscoveryOutlineSelection{
				Heading: entry.Title, Path: slices.Clone(entry.Path), Occurrence: entry.Occurrence,
			}
		}
	}
	if headingCount != expected.headingCount || selected.Heading == "" || selected.Occurrence != expected.occurrence {
		t.Fatalf("selected heading is not structurally observable: count=%d selection=%+v outline=%+v",
			headingCount, selected, outline)
	}
	return selected
}

func crossServiceDiscoveryFieldSelector(t *testing.T, invocation MCPInvocation) string {
	t.Helper()
	if invocation.Tool != "jira_issue_field_get" {
		t.Fatalf("cross-service field admission tool=%q", invocation.Tool)
	}
	arguments := crossServiceDiscoveryInvocationArguments(t, invocation)
	selector, ok := arguments["field"].(string)
	if !ok || (selector != "Description" && selector != "description") {
		t.Fatalf("cross-service field admission arguments=%v", arguments)
	}
	return selector
}

func assertCrossServiceDiscoveryDerivedAdmission(
	t *testing.T,
	admitted []MCPInvocation,
	index int,
	derived MCPInvocation,
) {
	t.Helper()
	if index >= len(admitted) || !equalMCPInvocations(admitted[index:index+1], []MCPInvocation{derived}) {
		t.Fatalf("selected output derived an unadmitted cross-service call at %d: admitted=%+v derived=%+v",
			index, admitted, derived)
	}
}

func crossServiceDiscoveryInvocationArguments(t *testing.T, invocation MCPInvocation) map[string]any {
	t.Helper()
	var arguments map[string]any
	if err := json.Unmarshal(invocation.Arguments, &arguments); err != nil || arguments == nil {
		t.Fatalf("decode cross-service invocation arguments: %v", err)
	}
	return arguments
}

func assertCrossServiceDiscoveryCanonicalAliasEquivalent(
	t *testing.T,
	named, canonical crossServiceDiscoveryProcessEvidence,
) {
	t.Helper()
	namedValue, err := json.Marshal(named.Field.Value)
	if err != nil {
		t.Fatal(err)
	}
	canonicalValue, err := json.Marshal(canonical.Field.Value)
	if err != nil {
		t.Fatal(err)
	}
	if named.JiraSearch.Rows[0].Key != canonical.JiraSearch.Rows[0].Key ||
		named.ConfluenceSearch.Results[0].ID != canonical.ConfluenceSearch.Results[0].ID ||
		named.Outline.ID != canonical.Outline.ID || named.Outline.Version != canonical.Outline.Version ||
		named.Section.ID != canonical.Section.ID || named.Section.Version != canonical.Section.Version ||
		named.Section.Heading != canonical.Section.Heading || named.Section.Occurrence != canonical.Section.Occurrence ||
		!slices.Equal(named.Section.Path, canonical.Section.Path) ||
		named.Field.Issue != canonical.Field.Issue ||
		named.Field.Field.ID != canonical.Field.Field.ID || named.Field.Field.Custom != canonical.Field.Field.Custom ||
		named.Field.Field.Present != canonical.Field.Field.Present || named.Field.Field.Empty != canonical.Field.Field.Empty ||
		named.Field.Field.ValueType != canonical.Field.Field.ValueType ||
		named.Field.Field.Name != "Description" || named.Field.Field.Schema != "string" ||
		canonical.Field.Field.Name != "description" || canonical.Field.Field.Schema != "" ||
		!bytes.Equal(namedValue, canonicalValue) {
		t.Fatalf("canonical field-id evidence diverged from named-field evidence: named=%+v canonical=%+v",
			named, canonical)
	}
}

// assertCrossServiceDiscoveryArgumentDivergenceRefused proves that deviations
// from the closed planned route fail at the evaluator admission before a
// synthetic backend request is possible.
func assertCrossServiceDiscoveryArgumentDivergenceRefused(
	t *testing.T,
	fixture MockFixture,
	expected crossServiceDiscoveryExpectation,
) {
	t.Helper()
	admitted := crossServiceDiscoveryMCPInvocations(t, expected, "Description")
	for _, test := range []struct {
		name   string
		index  int
		mutate func(map[string]any)
	}{
		{name: "jira-query", index: 0, mutate: func(arguments map[string]any) {
			arguments["jql"] = expected.jiraQuery + " AND status = Done"
		}},
		{name: "field-selector", index: 4, mutate: func(arguments map[string]any) {
			arguments["field"] = "summary"
		}},
	} {
		t.Run("admission-"+test.name, func(t *testing.T) {
			process := startCrossServiceDiscoveryProcess(t, fixture, expected, "Description", admitted)
			arguments := crossServiceDiscoveryInvocationArguments(t, admitted[test.index])
			test.mutate(arguments)
			rejected := mustMCPInvocation(t, admitted[test.index].Tool, arguments)
			if _, err := process.CallMCPJSON(context.Background(), rejected); err == nil {
				t.Fatalf("unadmitted cross-service %s invocation reached selected MCP/backend", test.name)
			}
			assertCrossServiceDiscoveryPreBackendRefusal(t, process, nil, nil)
		})
	}
}

// assertCrossServiceDiscoveryDerivedDivergenceRefused changes selected evidence
// or a subsequent derived value and proves the committed closed admission
// refuses it before the next backend request.
func assertCrossServiceDiscoveryDerivedDivergenceRefused(
	t *testing.T,
	fixture MockFixture,
	expected crossServiceDiscoveryExpectation,
) {
	t.Helper()
	admitted := crossServiceDiscoveryMCPInvocations(t, expected, "Description")

	t.Run("derived-issue-key", func(t *testing.T) {
		mutated := crossServiceDiscoveryMutateJiraSearchKey(t, fixture, "OTHER-1")
		process := startCrossServiceDiscoveryProcess(t, mutated, expected, "Description", admitted)
		result := callCrossServiceDiscoveryMCP(t, process, admitted[0])
		search, err := DecodeJiraSnapshotIssueList(bytes.NewReader(result.StructuredContent))
		if err != nil {
			t.Fatal(err)
		}
		derived := mustMCPInvocation(t, "jira_issue_field_get", map[string]any{
			"key": search.Rows[0].Key, "field": "Description", "max_bytes": 16384,
		})
		if equalMCPInvocations([]MCPInvocation{derived}, admitted[4:]) {
			t.Fatal("mutated Jira search reproduced the committed field admission")
		}
		if _, err := process.CallMCPJSON(context.Background(), derived); err == nil {
			t.Fatal("search-derived field divergence reached the backend")
		}
		assertCrossServiceDiscoveryPreBackendRefusal(t, process,
			map[string]int{"GET": 1}, map[string]int{"jira_issue_search": 1})
	})

	t.Run("derived-page-id", func(t *testing.T) {
		mutated := crossServiceDiscoveryMutateConfluenceSearchID(t, fixture, "9999")
		process := startCrossServiceDiscoveryProcess(t, mutated, expected, "Description", admitted)
		callCrossServiceDiscoveryMCP(t, process, admitted[0])
		result := callCrossServiceDiscoveryMCP(t, process, admitted[1])
		search, err := DecodeConfluenceSearchPageView(bytes.NewReader(result.StructuredContent))
		if err != nil {
			t.Fatal(err)
		}
		derived := mustMCPInvocation(t, "confluence_page_outline", map[string]any{
			"reference": search.Results[0].ID,
		})
		if equalMCPInvocations([]MCPInvocation{derived}, admitted[2:3]) {
			t.Fatal("mutated Confluence search reproduced the committed outline admission")
		}
		if _, err := process.CallMCPJSON(context.Background(), derived); err == nil {
			t.Fatal("search-derived page divergence reached the backend")
		}
		assertCrossServiceDiscoveryPreBackendRefusal(t, process,
			map[string]int{"GET": 2}, map[string]int{"jira_issue_search": 1, "confluence_search": 1})
	})

	t.Run("outline-version-and-occurrence", func(t *testing.T) {
		process := startCrossServiceDiscoveryProcess(t, fixture, expected, "Description", admitted)
		callCrossServiceDiscoveryMCP(t, process, admitted[0])
		callCrossServiceDiscoveryMCP(t, process, admitted[1])
		result := callCrossServiceDiscoveryMCP(t, process, admitted[2])
		outline, err := DecodeConfluencePageOutlineView(bytes.NewReader(result.StructuredContent))
		if err != nil {
			t.Fatal(err)
		}
		selection := assertCrossServiceDiscoveryOutline(t, outline, expected.pageID, expected)
		for _, test := range []struct {
			name       string
			occurrence int
			version    int
		}{
			{name: "version", occurrence: selection.Occurrence, version: outline.Version + 1},
			{name: "occurrence", occurrence: selection.Occurrence + 1, version: outline.Version},
		} {
			derived := mustMCPInvocation(t, "confluence_page_section", map[string]any{
				"reference": outline.ID, "heading": selection.Heading, "occurrence": test.occurrence,
				"expected_page_version": test.version, "max_bytes": 32768,
			})
			if _, err := process.CallMCPJSON(context.Background(), derived); err == nil {
				t.Fatalf("outline-derived %s divergence reached the backend", test.name)
			}
		}
		assertCrossServiceDiscoveryPreBackendRefusal(t, process,
			map[string]int{"GET": 3}, map[string]int{
				"jira_issue_search": 1, "confluence_search": 1, "confluence_page_outline": 1,
			})
	})
}

func assertCrossServiceDiscoveryPreBackendRefusal(
	t *testing.T,
	process *SyntheticATLProcess,
	wantHTTP, wantMCP map[string]int,
) {
	t.Helper()
	summary := process.Summary()
	if process.RequestSequenceComplete() || summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
		len(summary.CLIInvocations) != 0 || !equalHTTPMethods(summary.HTTPMethods, wantHTTP) ||
		!equalHTTPMethods(summary.MCPInvocations, wantMCP) {
		t.Fatalf("cross-service divergence was not refused before backend work: summary=%+v sequence_complete=%t",
			summary, process.RequestSequenceComplete())
	}
}

func crossServiceDiscoveryMutateJiraSearchKey(t *testing.T, fixture MockFixture, key string) MockFixture {
	t.Helper()
	return crossServiceDiscoveryMutateRouteBody(t, fixture, 0, func(body map[string]any) {
		issues, ok := body["issues"].([]any)
		if !ok || len(issues) == 0 {
			t.Fatalf("cross-service Jira search body has invalid issues: %T", body["issues"])
		}
		issue, ok := issues[0].(map[string]any)
		if !ok {
			t.Fatalf("cross-service Jira search first issue has type %T", issues[0])
		}
		issue["key"] = key
	})
}

func crossServiceDiscoveryMutateConfluenceSearchID(t *testing.T, fixture MockFixture, id string) MockFixture {
	t.Helper()
	return crossServiceDiscoveryMutateRouteBody(t, fixture, 1, func(body map[string]any) {
		results, ok := body["results"].([]any)
		if !ok || len(results) == 0 {
			t.Fatalf("cross-service Confluence search body has invalid results: %T", body["results"])
		}
		result, ok := results[0].(map[string]any)
		if !ok {
			t.Fatalf("cross-service Confluence search first result has type %T", results[0])
		}
		content, ok := result["content"].(map[string]any)
		if !ok {
			t.Fatalf("cross-service Confluence search first content has type %T", result["content"])
		}
		content["id"] = id
	})
}

func crossServiceDiscoveryMutateRouteBody(
	t *testing.T,
	fixture MockFixture,
	routeIndex int,
	mutate func(map[string]any),
) MockFixture {
	t.Helper()
	if routeIndex < 0 || routeIndex >= len(fixture.Routes) {
		t.Fatalf("cross-service route index %d is outside fixture", routeIndex)
	}
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	var body map[string]any
	if err := json.Unmarshal(prepared.Routes[routeIndex].Body, &body); err != nil {
		t.Fatal(err)
	}
	mutate(body)
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Routes[routeIndex].Body = encoded
	return prepared
}
