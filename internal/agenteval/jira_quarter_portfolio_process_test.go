package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	jiraQuarterPortfolioAuthRoute        = "quarter-auth"
	jiraQuarterPortfolioFieldsRoute      = "quarter-fields"
	jiraQuarterPortfolioBoardConfigRoute = "quarter-board-configuration"
	jiraQuarterPortfolioBoardIssuesRoute = "quarter-board-issues"
	jiraQuarterPortfolioSectionMaxBytes  = 32768
	jiraQuarterPortfolioBoardLimit       = 50
)

type jiraQuarterPortfolioProcessEvidence struct {
	Catalog     JiraQuarterFieldCatalog
	Board       JiraQuarterBoardSnapshot
	Quarter     string
	Epics       []jiraQuarterPortfolioDerivedEpic
	Invocations []MCPInvocation
	Summary     SyntheticATLProcessSummary
}

// startJiraQuarterPortfolioProcess uses one selected, isolated default MCP
// server for the complete cross-service workflow. Exact tool admissions are
// evaluator policy; the fixture's strict HTTP sequence is an independent
// backend oracle for the resulting product requests.
func startJiraQuarterPortfolioProcess(
	t *testing.T,
	fixture MockFixture,
	expected jiraQuarterPortfolioExpectation,
	admitted []MCPInvocation,
) *SyntheticATLProcess {
	t.Helper()
	if len(admitted) != 2+2*len(expected.epics) {
		t.Fatalf("quarter portfolio exact MCP admissions=%d want=%d", len(admitted), 2+2*len(expected.epics))
	}
	prepared := prepareJiraQuarterPortfolioFixture(t, fixture, expected)
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
			t.Errorf("close synthetic Jira quarter portfolio process: %v", err)
		}
	})
	return process
}

func prepareJiraQuarterPortfolioFixture(
	t *testing.T,
	fixture MockFixture,
	expected jiraQuarterPortfolioExpectation,
) MockFixture {
	t.Helper()
	epicCount := len(expected.epics)
	if expected.boardID <= 0 || epicCount == 0 || len(fixture.Routes) != 4+3*epicCount || len(fixture.RequestSequence) != 0 {
		t.Fatalf("quarter portfolio fixture shape drifted: board=%d epics=%d routes=%d sequence=%v",
			expected.boardID, epicCount, len(fixture.Routes), fixture.RequestSequence)
	}
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	assertJiraQuarterPortfolioRoute(t, prepared.Routes[0], fixture.JiraContext+"/rest/api/2/myself")
	assertJiraQuarterPortfolioRoute(t, prepared.Routes[1], fixture.JiraContext+"/rest/api/2/field")
	boardBase := fixture.JiraContext + "/rest/agile/1.0/board/" + strconv.Itoa(expected.boardID)
	assertJiraQuarterPortfolioRoute(t, prepared.Routes[2], boardBase+"/configuration")
	assertJiraQuarterPortfolioRoute(t, prepared.Routes[3], boardBase+"/issue")

	prepared.Routes[0] = jiraQuarterPortfolioRepeatedAuthRoute(t, prepared.Routes[0], epicCount)
	prepared.Routes[1] = jiraQuarterPortfolioNamedRoute(prepared.Routes[1], jiraQuarterPortfolioFieldsRoute, nil)
	prepared.Routes[2] = jiraQuarterPortfolioNamedRoute(prepared.Routes[2], jiraQuarterPortfolioBoardConfigRoute, nil)
	prepared.Routes[3] = jiraQuarterPortfolioNamedRoute(prepared.Routes[3], jiraQuarterPortfolioBoardIssuesRoute, jiraQuarterPortfolioBoardQuery(expected))

	sequence := []string{jiraQuarterPortfolioFieldsRoute, jiraQuarterPortfolioBoardConfigRoute, jiraQuarterPortfolioBoardIssuesRoute}
	issuesStart := 4
	historyStart := issuesStart + epicCount
	sectionsStart := historyStart + epicCount
	for index, epic := range expected.epics {
		issueIndex, historyIndex, sectionIndex := issuesStart+index, historyStart+index, sectionsStart+index
		assertJiraQuarterPortfolioRoute(t, prepared.Routes[issueIndex], fixture.JiraContext+"/rest/api/2/issue/"+epic.key)
		assertJiraQuarterPortfolioRoute(t, prepared.Routes[historyIndex], fixture.JiraContext+"/rest/api/2/issue/"+epic.key+"/changelog")
		assertJiraQuarterPortfolioRoute(t, prepared.Routes[sectionIndex], fixture.ConfluenceContext+"/rest/api/content/"+epic.pageID)
		issueName := jiraQuarterPortfolioRouteName("quarter-epic", epic.key)
		historyName := jiraQuarterPortfolioRouteName("quarter-history", epic.key)
		sectionName := jiraQuarterPortfolioRouteName("quarter-section", epic.key)
		prepared.Routes[issueIndex] = jiraQuarterPortfolioNamedRoute(prepared.Routes[issueIndex], issueName, jiraQuarterPortfolioDigestQuery(expected))
		prepared.Routes[historyIndex] = jiraQuarterPortfolioNamedRoute(prepared.Routes[historyIndex], historyName, map[string]string{
			"startAt": "0", "maxResults": "100",
		})
		prepared.Routes[sectionIndex] = jiraQuarterPortfolioNamedRoute(prepared.Routes[sectionIndex], sectionName, map[string]string{
			"expand": "body.storage,version,space,ancestors,metadata.labels",
		})
		sequence = append(sequence, jiraQuarterPortfolioAuthRoute, issueName, historyName, sectionName)
	}
	prepared.RequestSequence = sequence
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Jira quarter portfolio fixture: %v", err)
	}
	return prepared
}

func assertJiraQuarterPortfolioRoute(t *testing.T, route MockRoute, path string) {
	t.Helper()
	if route.Name != "" || route.Method != "GET" || route.Path != path || route.Status != 200 || !json.Valid(route.Body) ||
		len(route.Responses) != 0 || len(route.RequestBody) != 0 || len(route.QueryContains) != 0 || len(route.QueryEquals) != 0 || route.closedQuery {
		t.Fatalf("quarter portfolio retained route drifted: path=%q route=%+v", path, route)
	}
}

func jiraQuarterPortfolioRepeatedAuthRoute(t *testing.T, route MockRoute, repeats int) MockRoute {
	t.Helper()
	if repeats < 1 {
		t.Fatal("quarter portfolio auth route needs at least one repeat")
	}
	responses := make([]MockResponse, repeats)
	for index := range responses {
		responses[index] = MockResponse{Status: route.Status, Body: bytes.Clone(route.Body)}
	}
	route.Name = jiraQuarterPortfolioAuthRoute
	route.Status, route.Body, route.Responses = 0, nil, responses
	route.QueryEquals = nil
	route.closedQuery = true
	return route
}

func jiraQuarterPortfolioNamedRoute(route MockRoute, name string, query map[string]string) MockRoute {
	route.Name = name
	route.QueryEquals = maps.Clone(query)
	route.closedQuery = true
	return route
}

func jiraQuarterPortfolioRouteName(prefix, key string) string {
	return prefix + "-" + strings.ToLower(key)
}

func jiraQuarterPortfolioBoardQuery(expected jiraQuarterPortfolioExpectation) map[string]string {
	fields := append([]string{"status", "summary", "issuetype", "updated"}, expected.fieldIDs...)
	return map[string]string{
		"startAt": "0", "maxResults": strconv.Itoa(jiraQuarterPortfolioBoardLimit), "fields": strings.Join(fields, ","),
	}
}

func jiraQuarterPortfolioDigestQuery(expected jiraQuarterPortfolioExpectation) map[string]string {
	return map[string]string{
		"fields": strings.Join([]string{
			"summary", "status", "resolution", "description", "issuetype", "updated", "issuelinks", expected.statusField,
		}, ","),
	}
}

func executeJiraQuarterPortfolioProcess(
	t *testing.T,
	process *SyntheticATLProcess,
	expected jiraQuarterPortfolioExpectation,
	admitted []MCPInvocation,
) jiraQuarterPortfolioProcessEvidence {
	t.Helper()
	invocations := make([]MCPInvocation, 0, len(admitted))
	fieldsInvocation := mustMCPInvocation(t, "jira_fields", map[string]any{})
	invocations = append(invocations, fieldsInvocation)
	fieldsResult := callJiraQuarterPortfolioMCP(t, process, fieldsInvocation)
	catalog, err := DecodeJiraQuarterFieldCatalog(bytes.NewReader(fieldsResult.StructuredContent))
	if err != nil {
		t.Fatalf("decode selected Jira quarter field catalog: %v", err)
	}
	fieldIDs := jiraQuarterPortfolioSelectedFields(t, catalog, expected)
	columns := append([]string{"key", "summary", "status", "issuetype", "updated"}, fieldIDs...)
	if !slices.Equal(columns, expected.columns) {
		t.Fatalf("selected catalog did not derive board columns: got=%v want=%v", columns, expected.columns)
	}

	boardInvocation := mustMCPInvocation(t, "jira_board_view", map[string]any{
		"board_id": expected.boardID, "scope": "board", "limit": jiraQuarterPortfolioBoardLimit, "columns": columns,
		"epic_field": fieldIDs[0], "done_statuses": []string{"Done"},
	})
	invocations = append(invocations, boardInvocation)
	boardResult := callJiraQuarterPortfolioMCP(t, process, boardInvocation)
	board, err := DecodeJiraQuarterBoardSnapshot(bytes.NewReader(boardResult.StructuredContent))
	if err != nil {
		t.Fatalf("decode selected Jira quarter board: %v", err)
	}
	if !board.Complete || board.Truncated || board.RowCount != expected.boardRows || board.Board.ID != expected.boardID ||
		!slices.Equal(board.Projection.Columns, columns) || !board.EpicRollup.Complete ||
		board.EpicRollup.EpicField != fieldIDs[0] || !slices.Equal(board.EpicRollup.DoneStatuses, []string{"Done"}) ||
		len(board.EpicRollup.Epics) != len(expected.epics) {
		t.Fatalf("selected Jira quarter board drifted: %+v", board)
	}

	derived := make([]jiraQuarterPortfolioDerivedEpic, 0, len(board.EpicRollup.Epics))
	quarter := ""
	for index, rollup := range board.EpicRollup.Epics {
		want := expected.epics[index]
		if rollup.Key != want.key || !rollup.ParentPresent || !rollup.TimestampCoverageComplete ||
			rollup.TimestampedChildren != rollup.ChildCount || rollup.MissingUpdatedChildren != 0 ||
			strings.TrimSpace(rollup.LatestChildUpdated) == "" {
			t.Fatalf("selected Jira quarter rollup[%d] drifted: %+v", index, rollup)
		}
		row := jiraQuarterPortfolioBoardRow(t, board.JiraBoardSnapshot, rollup.Key)
		pageReference := jiraQuarterPortfolioBoardString(t, row, fieldIDs[2])
		if pageReference != want.pageReference {
			t.Fatalf("epic %s selected page reference=%q want=%q", rollup.Key, pageReference, want.pageReference)
		}
		digestInvocation := mustMCPInvocation(t, "jira_epic_digest", map[string]any{
			"key": rollup.Key, "quarter": expected.quarter,
			"include": []string{"identity", "status-field", "history"}, "status_field": fieldIDs[1], "projection": "compact",
		})
		invocations = append(invocations, digestInvocation)
		digestResult := callJiraQuarterPortfolioMCP(t, process, digestInvocation)
		digest, err := DecodeJiraQuarterCompactEpicDigest(bytes.NewReader(digestResult.StructuredContent))
		if err != nil {
			t.Fatalf("decode selected Jira quarter digest for %s: %v", rollup.Key, err)
		}
		if digest.Epic.Key != rollup.Key || digest.StatusField.ID != fieldIDs[1] {
			t.Fatalf("selected Jira quarter digest drifted for %s: %+v", rollup.Key, digest)
		}
		if quarter == "" {
			quarter = digest.Period.Quarter
		}
		if digest.Period.Quarter != quarter || quarter != expected.quarter {
			t.Fatalf("selected Jira quarter digest period=%q want=%q", digest.Period.Quarter, expected.quarter)
		}
		sectionInvocation := mustMCPInvocation(t, "confluence_page_section", map[string]any{
			"reference": pageReference, "heading": "Results", "occurrence": 1, "max_bytes": jiraQuarterPortfolioSectionMaxBytes,
		})
		invocations = append(invocations, sectionInvocation)
		sectionResult := callJiraQuarterPortfolioMCP(t, process, sectionInvocation)
		section, err := DecodeConfluencePageSectionView(bytes.NewReader(sectionResult.StructuredContent))
		if err != nil {
			t.Fatalf("decode selected Confluence section for %s: %v", rollup.Key, err)
		}
		if !section.Complete || section.Truncated || section.ID != want.pageID || section.Heading != "Results" || section.Occurrence != 1 ||
			!strings.Contains(section.Markdown, want.evidenceResult) {
			t.Fatalf("selected section drifted for %s: %+v", rollup.Key, section)
		}
		if expected.rejectedPageMarker != "" && strings.Contains(section.Markdown, expected.rejectedPageMarker) {
			t.Fatalf("selected section for %s leaked rejected appendix marker %q", rollup.Key, expected.rejectedPageMarker)
		}
		item := jiraQuarterPortfolioDerivedEpic{
			key: rollup.Key, outcome: jiraQuarterPortfolioOutcome(t, row.Status, digest.StatusField.Value, rollup.ChildCount, rollup.DoneChildCount),
			totalChildren: rollup.ChildCount, doneChildren: rollup.DoneChildCount,
			statusStale:    jiraQuarterPortfolioStatusStale(t, digest.StatusField, rollup.LatestChildUpdated),
			evidenceResult: jiraQuarterPortfolioSectionResult(t, section.Markdown),
		}
		if item.outcome != want.outcome || item.totalChildren != want.totalChildren || item.doneChildren != want.doneChildren ||
			item.statusStale != want.statusStale || item.evidenceResult != want.evidenceResult {
			t.Fatalf("selected fixture-derived epic=%+v want=%+v", item, want)
		}
		derived = append(derived, item)
	}
	if !equalMCPInvocations(admitted, invocations) {
		t.Fatalf("selected MCP invocation sequence drifted: admitted=%+v observed=%+v", admitted, invocations)
	}
	summary := process.Summary()
	if !process.RequestSequenceComplete() || !maps.Equal(summary.HTTPMethods, map[string]int{"GET": expected.backendRequests}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != expected.duplicateRequests || len(summary.CLIInvocations) != 0 ||
		!maps.Equal(summary.MCPInvocations, map[string]int{
			"jira_fields": 1, "jira_board_view": 1, "jira_epic_digest": len(expected.epics), "confluence_page_section": len(expected.epics),
		}) {
		t.Fatalf("selected Jira quarter process accounting drifted: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
	return jiraQuarterPortfolioProcessEvidence{
		Catalog: catalog, Board: board, Quarter: quarter, Epics: derived, Invocations: invocations, Summary: summary,
	}
}

func jiraQuarterPortfolioSelectedFields(
	t *testing.T,
	catalog JiraQuarterFieldCatalog,
	expected jiraQuarterPortfolioExpectation,
) []string {
	t.Helper()
	if len(catalog.Fields) != len(expected.fieldIDs) {
		t.Fatalf("selected field catalog count=%d want=%d", len(catalog.Fields), len(expected.fieldIDs))
	}
	ids := make([]string, len(catalog.Fields))
	for index, field := range catalog.Fields {
		if field.ID != expected.fieldIDs[index] || field.Name != expected.fieldNames[index] || !field.Custom {
			t.Fatalf("selected field[%d]=%+v want id=%q name=%q custom", index, field, expected.fieldIDs[index], expected.fieldNames[index])
		}
		ids[index] = field.ID
	}
	return ids
}

func callJiraQuarterPortfolioMCP(t *testing.T, process *SyntheticATLProcess, invocation MCPInvocation) SyntheticMCPResult {
	t.Helper()
	result, err := process.CallMCPJSON(context.Background(), invocation)
	if err != nil {
		t.Fatalf("selected quarter MCP %s: %v", invocation.Tool, err)
	}
	if result.IsError {
		t.Fatalf("selected quarter MCP %s failed: %s", invocation.Tool, strings.Join(result.TextContent, "\n"))
	}
	assertRepositoryMCPTextMatchesStructured(t, result)
	return result
}

func assertJiraQuarterPortfolioProcessRouteMutationsFail(
	t *testing.T,
	fixture MockFixture,
	expected jiraQuarterPortfolioExpectation,
	admitted []MCPInvocation,
) {
	t.Helper()
	for _, test := range []struct {
		name   string
		index  int
		mutate func(map[string]any)
	}{
		{name: "board", index: 1, mutate: func(arguments map[string]any) { arguments["board_id"] = float64(expected.boardID + 1) }},
		{name: "columns", index: 1, mutate: func(arguments map[string]any) {
			columns := arguments["columns"].([]any)
			columns[0], columns[1] = columns[1], columns[0]
		}},
		{name: "epic-field", index: 1, mutate: func(arguments map[string]any) { delete(arguments, "epic_field") }},
		{name: "done-statuses", index: 1, mutate: func(arguments map[string]any) { arguments["done_statuses"] = []any{"Closed"} }},
		{name: "status-field", index: 2, mutate: func(arguments map[string]any) { arguments["status_field"] = "customfield_99999" }},
		{name: "cap", index: 3, mutate: func(arguments map[string]any) {
			arguments["max_bytes"] = float64(jiraQuarterPortfolioSectionMaxBytes / 2)
		}},
	} {
		t.Run("process-route-arguments-"+test.name, func(t *testing.T) {
			process := startJiraQuarterPortfolioProcess(t, fixture, expected, admitted)
			arguments := jiraQuarterPortfolioInvocationArguments(t, admitted[test.index])
			test.mutate(arguments)
			mutated := mustMCPInvocation(t, admitted[test.index].Tool, arguments)
			if _, err := process.CallMCPJSON(context.Background(), mutated); err == nil {
				t.Fatalf("unadmitted quarter %s divergence reached selected MCP", test.name)
			}
			assertJiraQuarterPortfolioPreBackendRefusal(t, process)
		})
	}
	for _, test := range []struct {
		name       string
		invocation MCPInvocation
	}{
		{name: "malformed", invocation: MCPInvocation{Tool: "jira_fields", Arguments: []byte("{")}},
		{name: "unknown", invocation: mustMCPInvocation(t, "jira_fields", map[string]any{"unreviewed": true})},
		{name: "null", invocation: mustMCPInvocation(t, "jira_fields", map[string]any{"summary_only": nil})},
		{name: "oversized", invocation: mustMCPInvocation(t, "jira_fields", map[string]any{
			"unreviewed": strings.Repeat("x", 1<<20),
		})},
	} {
		t.Run("process-argument-wire-"+test.name, func(t *testing.T) {
			process := startJiraQuarterPortfolioProcess(t, fixture, expected, admitted)
			if _, err := process.CallMCPJSON(context.Background(), test.invocation); err == nil {
				t.Fatalf("invalid quarter %s argument crossed selected MCP admission", test.name)
			}
			assertJiraQuarterPortfolioPreBackendRefusal(t, process)
		})
	}

	t.Run("process-route-order", func(t *testing.T) {
		process := startJiraQuarterPortfolioProcess(t, fixture, expected, admitted)
		callJiraQuarterPortfolioMCP(t, process, admitted[0])
		callJiraQuarterPortfolioMCP(t, process, admitted[1])
		result, err := process.CallMCPJSON(context.Background(), admitted[3])
		if err != nil {
			t.Fatalf("swapped quarter section was rejected before selected MCP/backend: %v", err)
		}
		if !result.IsError {
			t.Fatal("swapped quarter section unexpectedly succeeded")
		}
		summary := process.Summary()
		if process.RequestSequenceComplete() || !maps.Equal(summary.HTTPMethods, map[string]int{"GET": 4}) ||
			summary.UnexpectedRequests != 1 || summary.DuplicateRequests != 0 || len(summary.CLIInvocations) != 0 ||
			!maps.Equal(summary.MCPInvocations, map[string]int{
				"jira_fields": 1, "jira_board_view": 1, "confluence_page_section": 1,
			}) {
			t.Fatalf("swapped quarter order did not fail at strict backend sequence: summary=%+v sequence_complete=%t result=%+v",
				summary, process.RequestSequenceComplete(), result)
		}
	})
}

func jiraQuarterPortfolioInvocationArguments(t *testing.T, invocation MCPInvocation) map[string]any {
	t.Helper()
	var arguments map[string]any
	if err := json.Unmarshal(invocation.Arguments, &arguments); err != nil {
		t.Fatal(err)
	}
	return arguments
}

func assertJiraQuarterPortfolioPreBackendRefusal(t *testing.T, process *SyntheticATLProcess) {
	t.Helper()
	summary := process.Summary()
	if process.RequestSequenceComplete() || len(summary.HTTPMethods) != 0 || summary.UnexpectedRequests != 0 ||
		summary.DuplicateRequests != 0 || len(summary.CLIInvocations) != 0 || len(summary.MCPInvocations) != 0 {
		t.Fatalf("quarter route divergence was not pre-backend: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
}
