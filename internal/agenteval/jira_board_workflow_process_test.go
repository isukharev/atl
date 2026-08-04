package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func startRepositoryJiraBoardWorkflowProcess(
	t *testing.T,
	fixture MockFixture,
	admitted []MCPInvocation,
	boardID int,
) *SyntheticATLProcess {
	t.Helper()
	if boardID <= 0 || len(admitted) != 1 || admitted[0].Tool != "jira_board_view" {
		t.Fatalf("Jira board workflow requires one exact board admission: board=%d admitted=%+v", boardID, admitted)
	}
	if len(fixture.RequestSequence) != 0 || len(fixture.Routes) < 3 {
		t.Fatalf("retained Jira board fixture has an unexpected sequence shape: routes=%d sequence=%v",
			len(fixture.Routes), fixture.RequestSequence)
	}

	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	sequence := make([]string, len(prepared.Routes))
	boardPath := fixture.JiraContext + "/rest/agile/1.0/board/" + strconv.Itoa(boardID)
	boardPage, backlogPage := 0, 0
	stage := 0
	for index := range prepared.Routes {
		route := prepared.Routes[index]
		if route.Name != "" || route.Method != "GET" || route.Status != 200 ||
			len(route.Responses) != 0 || len(route.RequestBody) != 0 || len(route.QueryContains) != 0 {
			t.Fatalf("retained Jira board route %d is not one unnamed static exact GET: %+v", index, route)
		}
		var name string
		switch route.Path {
		case boardPath + "/configuration":
			if index != 0 || stage != 0 || len(route.QueryEquals) != 0 {
				t.Fatalf("retained Jira board configuration route drifted: index=%d query=%v", index, route.QueryEquals)
			}
			name, stage = "configuration", 1
		case boardPath + "/issue":
			if stage < 1 || stage > 2 {
				t.Fatalf("retained Jira board page is out of sequence at route %d", index)
			}
			assertRetainedJiraBoardPageQuery(t, route.QueryEquals, "board", index)
			boardPage++
			name, stage = fmt.Sprintf("board-page-%d", boardPage), 2
		case boardPath + "/backlog":
			if stage < 2 || stage > 3 {
				t.Fatalf("retained Jira backlog page is out of sequence at route %d", index)
			}
			assertRetainedJiraBoardPageQuery(t, route.QueryEquals, "backlog", index)
			backlogPage++
			name, stage = fmt.Sprintf("backlog-page-%d", backlogPage), 3
		default:
			t.Fatalf("retained Jira board route %d has unsupported path %q", index, route.Path)
		}
		route.Name = name
		route.closedQuery = true
		prepared.Routes[index] = route
		sequence[index] = name
	}
	if boardPage == 0 || backlogPage == 0 || stage != 3 {
		t.Fatalf("retained Jira board fixture omits a scope: board_pages=%d backlog_pages=%d", boardPage, backlogPage)
	}
	prepared.RequestSequence = sequence
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Jira board workflow fixture: %v", err)
	}

	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepared,
		ScratchRoot: privateSyntheticATLScratch(t), MCPService: "jira",
		MCPInvocations: slices.Clone(admitted),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Jira board workflow process: %v", err)
		}
	})
	return process
}

func assertRetainedJiraBoardPageQuery(t *testing.T, query map[string]string, scope string, route int) {
	t.Helper()
	if len(query) != 4 || query["startAt"] == "" || query["maxResults"] == "" ||
		query["fields"] == "" || query["jql"] == "" {
		t.Fatalf("retained Jira %s page route %d has no closed pagination query: %v", scope, route, query)
	}
}

func callRepositoryJiraBoardWorkflow(
	t *testing.T,
	process *SyntheticATLProcess,
	invocation MCPInvocation,
) SyntheticMCPResult {
	t.Helper()
	result, err := process.CallMCPJSON(t.Context(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("selected Jira board workflow failed: %s", strings.Join(result.TextContent, "\n"))
	}
	assertRepositoryMCPTextMatchesStructured(t, result)
	return result
}

func assertRepositoryJiraBoardWorkflowAccounting(
	t *testing.T,
	process *SyntheticATLProcess,
	wantRequests int,
) (map[string]int, int) {
	t.Helper()
	summary := process.Summary()
	if !process.RequestSequenceComplete() ||
		!equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": wantRequests}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
		len(summary.CLIInvocations) != 0 ||
		!equalHTTPMethods(summary.MCPInvocations, map[string]int{"jira_board_view": 1}) {
		t.Fatalf("selected Jira board process accounting drifted: summary=%+v sequence_complete=%t",
			summary, process.RequestSequenceComplete())
	}
	return summary.HTTPMethods, summary.UnexpectedRequests
}

func assertRepositoryJiraBoardAdmissionDivergencesRefuse(
	t *testing.T,
	fixture MockFixture,
	admitted MCPInvocation,
	boardID int,
) {
	t.Helper()
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "board-id", mutate: func(arguments map[string]any) { arguments["board_id"] = float64(boardID + 1) }},
		{name: "scope", mutate: func(arguments map[string]any) { arguments["scope"] = "board" }},
		{name: "columns", mutate: func(arguments map[string]any) { arguments["columns"] = []any{"key", "status"} }},
		{name: "jql", mutate: func(arguments map[string]any) { arguments["jql"] = "labels = unadmitted" }},
		{name: "limit", mutate: func(arguments map[string]any) { arguments["limit"] = float64(1) }},
		{name: "max-bytes", mutate: func(arguments map[string]any) { arguments["max_bytes"] = float64(65536) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := startRepositoryJiraBoardWorkflowProcess(t, fixture, []MCPInvocation{admitted}, boardID)
			var arguments map[string]any
			if err := json.Unmarshal(admitted.Arguments, &arguments); err != nil {
				t.Fatal(err)
			}
			test.mutate(arguments)
			mutated := mustMCPInvocation(t, admitted.Tool, arguments)
			if _, err := process.CallMCPJSON(t.Context(), mutated); err == nil {
				t.Fatalf("unadmitted Jira board %s reached the selected ATL process", test.name)
			}
			summary := process.Summary()
			if process.RequestSequenceComplete() || len(summary.HTTPMethods) != 0 ||
				summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
				len(summary.CLIInvocations) != 0 || len(summary.MCPInvocations) != 0 {
				t.Fatalf("Jira board %s divergence was not pre-backend: summary=%+v sequence_complete=%t",
					test.name, summary, process.RequestSequenceComplete())
			}
		})
	}
}

func assertSelectedJiraBoardHostileSummary(
	t *testing.T,
	result SyntheticMCPResult,
	snapshot JiraBoardSnapshot,
	hostile string,
) {
	t.Helper()
	if hostile == "" {
		t.Fatal("Jira board cohort does not name its hostile summary")
	}
	encoded, err := json.Marshal(hostile)
	if err != nil {
		t.Fatal(err)
	}
	if occurrences := bytes.Count(result.StructuredContent, encoded); occurrences != 1 {
		t.Fatalf("selected Jira board structured output contains hostile summary %d times, want one", occurrences)
	}
	occurrences := 0
	for _, row := range snapshot.Rows {
		if summary, ok := row.Values["summary"].(string); ok && summary == hostile {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("decoded Jira board rows contain hostile summary %d times, want one", occurrences)
	}
}

func assertRecursiveJSONStringsExclude(t *testing.T, data []byte, forbidden string) {
	t.Helper()
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	var inspect func(any, string)
	inspect = func(current any, path string) {
		switch typed := current.(type) {
		case string:
			if strings.Contains(typed, forbidden) {
				t.Fatalf("derived Jira board final retained hostile content at %s", path)
			}
		case []any:
			for index, item := range typed {
				inspect(item, fmt.Sprintf("%s[%d]", path, index))
			}
		case map[string]any:
			for name, item := range typed {
				if strings.Contains(name, forbidden) {
					t.Fatalf("derived Jira board final retained hostile content in member name at %s", path)
				}
				inspect(item, path+"."+name)
			}
		}
	}
	inspect(value, "final")
}
