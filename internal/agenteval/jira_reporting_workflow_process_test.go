package agenteval

import (
	"bytes"
	"maps"
	"slices"
	"strconv"
	"testing"
)

const (
	jiraStatusDoneRoute   = "status_done"
	jiraStatusActiveRoute = "status_active"
	jiraStatusRiskRoute   = "status_risk"

	jiraSprintCurrentRoute = "sprint_current"
	jiraSprintFirstRoute   = "sprint_issues_first"
	jiraSprintNextRoute    = "sprint_issues_next"
)

type jiraStatusReportProcessEvidence struct {
	Pages   []JiraSnapshotIssueList
	Summary SyntheticATLProcessSummary
}

type jiraSprintDashboardProcessEvidence struct {
	Sprint    JiraSprintCurrent
	Pages     []JiraSprintMembershipIssueList
	Summary   SyntheticATLProcessSummary
	Exits     []int
	Contracts []CLIErrorContract
}

func startJiraStatusReportProcess(t *testing.T, fixture MockFixture, policy CLICommandPolicy, queries []string) *SyntheticATLProcess {
	t.Helper()
	prepared := prepareJiraStatusReportFixture(t, fixture, queries)
	return startJiraReportingWorkflowProcess(t, prepared, policy)
}

func startJiraSprintDashboardProcess(
	t *testing.T,
	fixture MockFixture,
	policy CLICommandPolicy,
	boardID, sprintID int,
	expectContinuationFailure bool,
) *SyntheticATLProcess {
	t.Helper()
	prepared := prepareJiraSprintDashboardFixture(t, fixture, boardID, sprintID, expectContinuationFailure)
	return startJiraReportingWorkflowProcess(t, prepared, policy)
}

func startJiraReportingWorkflowProcess(t *testing.T, fixture MockFixture, policy CLICommandPolicy) *SyntheticATLProcess {
	t.Helper()
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: fixture,
		ScratchRoot: privateSyntheticATLScratch(t), CLIPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Jira reporting process: %v", err)
		}
	})
	return process
}

func prepareJiraStatusReportFixture(t *testing.T, fixture MockFixture, queries []string) MockFixture {
	t.Helper()
	if len(queries) != 3 || len(fixture.Routes) != len(queries) || len(fixture.RequestSequence) != 0 {
		t.Fatalf("status process fixture is incomplete: queries=%d routes=%d sequence=%v", len(queries), len(fixture.Routes), fixture.RequestSequence)
	}
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	routeNames := []string{jiraStatusDoneRoute, jiraStatusActiveRoute, jiraStatusRiskRoute}
	for index, query := range queries {
		route := prepared.Routes[index]
		wantQuery := map[string]string{
			"jql": query, "startAt": "0", "maxResults": "2", "fields": "summary,status,assignee,priority,updated",
		}
		if route.Name != "" || route.Method != "GET" || route.Path != fixture.JiraContext+"/rest/api/2/search" ||
			route.Status != 200 || len(route.Responses) != 0 || len(route.RequestBody) != 0 ||
			len(route.QueryContains) != 0 || !maps.Equal(route.QueryEquals, wantQuery) {
			t.Fatalf("status process route %d drifted: %+v", index, route)
		}
		route.Name = routeNames[index]
		route.closedQuery = true
		prepared.Routes[index] = route
	}
	prepared.RequestSequence = slices.Clone(routeNames)
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare status process fixture: %v", err)
	}
	return prepared
}

func prepareJiraSprintDashboardFixture(
	t *testing.T,
	fixture MockFixture,
	boardID, sprintID int,
	expectContinuationFailure bool,
) MockFixture {
	t.Helper()
	if boardID <= 0 || sprintID <= 0 || len(fixture.Routes) != 3 || len(fixture.RequestSequence) != 0 {
		t.Fatalf("sprint process fixture is incomplete: board=%d sprint=%d routes=%d sequence=%v", boardID, sprintID, len(fixture.Routes), fixture.RequestSequence)
	}
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	currentPath := fixture.JiraContext + "/rest/agile/1.0/board/" + strconv.Itoa(boardID) + "/sprint"
	issuePath := fixture.JiraContext + "/rest/agile/1.0/sprint/" + strconv.Itoa(sprintID) + "/issue"
	checks := []struct {
		name, method, path string
		status             int
		query              map[string]string
	}{
		{jiraSprintCurrentRoute, "GET", currentPath, 200, map[string]string{"startAt": "0", "maxResults": "50", "state": "active"}},
		{jiraSprintFirstRoute, "GET", issuePath, 200, map[string]string{"startAt": "0", "maxResults": "2", "fields": "summary,status,assignee,priority,issuetype,updated"}},
		{jiraSprintNextRoute, "GET", issuePath, 200, map[string]string{"startAt": "2", "maxResults": "2", "fields": "summary,status,assignee,priority,issuetype,updated"}},
	}
	if expectContinuationFailure {
		checks[2].status = 403
	}
	for index, check := range checks {
		route := prepared.Routes[index]
		if route.Name != "" || route.Method != check.method || route.Path != check.path || route.Status != check.status ||
			len(route.Responses) != 0 || len(route.RequestBody) != 0 || len(route.QueryContains) != 0 || !maps.Equal(route.QueryEquals, check.query) {
			t.Fatalf("sprint process route %d drifted: %+v", index, route)
		}
		route.Name = check.name
		route.closedQuery = true
		prepared.Routes[index] = route
	}
	prepared.RequestSequence = []string{jiraSprintCurrentRoute, jiraSprintFirstRoute, jiraSprintNextRoute}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare sprint process fixture: %v", err)
	}
	return prepared
}

func executeJiraStatusReportProcess(
	t *testing.T,
	process *SyntheticATLProcess,
	commands [][]string,
	queries []string,
) jiraStatusReportProcessEvidence {
	t.Helper()
	if len(commands) != 3 || len(queries) != len(commands) {
		t.Fatalf("status workflow command/query contract=%d/%d, want 3/3", len(commands), len(queries))
	}
	pages := make([]JiraSnapshotIssueList, len(commands))
	for index, args := range commands {
		result := callSelectedJiraReportingJSON(t, process, args)
		page, err := DecodeJiraSnapshotIssueList(bytes.NewReader(result))
		if err != nil {
			t.Fatalf("decode selected status page %d: %v", index, err)
		}
		if jql, _ := page.Selection["jql"].(string); jql != queries[index] {
			t.Fatalf("selected status page %d selection=%q want %q", index, jql, queries[index])
		}
		pages[index] = page
	}
	summary := process.Summary()
	if !process.RequestSequenceComplete() || !equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": 3}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
		!maps.Equal(summary.CLIInvocations, map[string]int{"status_done": 1, "status_active": 1, "status_risk": 1}) || len(summary.MCPInvocations) != 0 {
		t.Fatalf("selected status process accounting drifted: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
	return jiraStatusReportProcessEvidence{Pages: pages, Summary: summary}
}

func executeJiraSprintDashboardProcess(
	t *testing.T,
	process *SyntheticATLProcess,
	commands [][]string,
	expectContinuationFailure bool,
) jiraSprintDashboardProcessEvidence {
	t.Helper()
	if len(commands) != 3 {
		t.Fatalf("sprint workflow requires three reviewed commands, got %d", len(commands))
	}
	currentJSON := callSelectedJiraReportingJSON(t, process, commands[0])
	sprint, err := DecodeJiraSprintCurrent(bytes.NewReader(currentJSON))
	if err != nil {
		t.Fatalf("decode selected current sprint: %v", err)
	}
	firstJSON := callSelectedJiraReportingJSON(t, process, commands[1])
	first, err := DecodeJiraSprintMembershipIssueList(bytes.NewReader(firstJSON))
	if err != nil {
		t.Fatalf("decode selected first sprint membership page: %v", err)
	}
	pages := []JiraSprintMembershipIssueList{first}
	exits := []int{0, 0}
	var contracts []CLIErrorContract
	continuation, err := process.RunCLIJSON(t.Context(), commands[2]...)
	if err != nil {
		t.Fatalf("selected sprint continuation command %v: %v", commands[2], err)
	}
	exits = append(exits, continuation.ExitCode)
	if expectContinuationFailure {
		if continuation.ExitCode != 6 || len(continuation.JSON) != 0 || len(continuation.Stderr) == 0 {
			t.Fatalf("selected sprint continuation exit=%d stdout_bytes=%d stderr_bytes=%d, want typed exit 6", continuation.ExitCode, len(continuation.JSON), len(continuation.Stderr))
		}
		contract, ok := ParseCLIErrorContract(continuation.ExitCode, continuation.Stderr)
		if !ok || contract != (CLIErrorContract{ExitCode: 6, Kind: "forbidden", Remediation: "request_access"}) {
			t.Fatalf("selected sprint continuation contract=%+v classified=%t", contract, ok)
		}
		contracts = []CLIErrorContract{contract}
	} else {
		if continuation.ExitCode != 0 || len(continuation.JSON) == 0 || len(continuation.Stderr) != 0 {
			t.Fatalf("selected sprint continuation exit=%d stdout_bytes=%d stderr_bytes=%d", continuation.ExitCode, len(continuation.JSON), len(continuation.Stderr))
		}
		next, decodeErr := DecodeJiraSprintMembershipIssueList(bytes.NewReader(continuation.JSON))
		if decodeErr != nil {
			t.Fatalf("decode selected next sprint membership page: %v", decodeErr)
		}
		pages = append(pages, next)
	}
	summary := process.Summary()
	if !process.RequestSequenceComplete() || !equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": 3}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
		!maps.Equal(summary.CLIInvocations, map[string]int{"sprint_current": 1, "sprint_issues_first": 1, "sprint_issues_next": 1}) || len(summary.MCPInvocations) != 0 {
		t.Fatalf("selected sprint process accounting drifted: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
	return jiraSprintDashboardProcessEvidence{Sprint: sprint, Pages: pages, Summary: summary, Exits: exits, Contracts: contracts}
}

func callSelectedJiraReportingJSON(t *testing.T, process *SyntheticATLProcess, args []string) []byte {
	t.Helper()
	result, err := process.RunCLIJSON(t.Context(), args...)
	if err != nil {
		t.Fatalf("selected reporting command %v: %v", args, err)
	}
	if result.ExitCode != 0 || len(result.JSON) == 0 || len(result.Stderr) != 0 {
		t.Fatalf("selected reporting command %v exit=%d stdout_bytes=%d stderr_bytes=%d", args, result.ExitCode, len(result.JSON), len(result.Stderr))
	}
	return append([]byte(nil), result.JSON...)
}

func assertJiraStatusReportAdmissionRefused(
	t *testing.T,
	fixture MockFixture,
	policy CLICommandPolicy,
	queries []string,
	commands [][]string,
) {
	t.Helper()
	if len(queries) != 3 || len(commands) != 3 {
		t.Fatal("status admission contract requires three queries and commands")
	}
	for _, test := range []struct {
		name string
		args []string
	}{
		{"jql", []string{"jira", "issue", "search", "--jql", queries[0] + " AND labels = unreviewed", "--columns", reportingColumns, "--limit", "2"}},
		{"cursor", append(slices.Clone(commands[1]), "--cursor", "3")},
		{"output", append(slices.Clone(commands[2]), "-o", "text")},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := startJiraStatusReportProcess(t, fixture, policy, queries)
			if _, err := process.RunCLIJSON(t.Context(), test.args...); err == nil {
				t.Fatalf("unreviewed status command %v crossed exact process admission", test.args)
			}
			assertJiraReportingPreBackendRefusal(t, process)
		})
	}
}

func assertJiraSprintDashboardAdmissionRefused(
	t *testing.T,
	fixture MockFixture,
	policy CLICommandPolicy,
	boardID, sprintID int,
	expectContinuationFailure bool,
	commands [][]string,
) {
	t.Helper()
	if len(commands) != 3 {
		t.Fatal("sprint admission contract requires three commands")
	}
	for _, test := range []struct {
		name string
		args []string
	}{
		{"board", []string{"jira", "sprint", "current", "--board", strconv.Itoa(boardID + 1)}},
		{"sprint", []string{"jira", "sprint", "issues", strconv.Itoa(sprintID + 1), "--columns", dashboardColumns, "--limit", "2"}},
		{"cursor", append(slices.Clone(commands[1]), "--cursor", "3")},
		{"output", append(slices.Clone(commands[2]), "-o", "text")},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := startJiraSprintDashboardProcess(t, fixture, policy, boardID, sprintID, expectContinuationFailure)
			if _, err := process.RunCLIJSON(t.Context(), test.args...); err == nil {
				t.Fatalf("unreviewed sprint command %v crossed exact process admission", test.args)
			}
			assertJiraReportingPreBackendRefusal(t, process)
		})
	}
}

func assertJiraReportingPreBackendRefusal(t *testing.T, process *SyntheticATLProcess) {
	t.Helper()
	summary := process.Summary()
	if process.RequestSequenceComplete() || len(summary.HTTPMethods) != 0 || summary.UnexpectedRequests != 0 ||
		summary.DuplicateRequests != 0 || len(summary.CLIInvocations) != 0 || len(summary.MCPInvocations) != 0 {
		t.Fatalf("reporting command was not refused before backend work: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
}

func jiraReportingWorkflowCommands(commands []reviewedCLICommand) [][]string {
	args := make([][]string, len(commands))
	for index, command := range commands {
		args[index] = slices.Clone(command.args)
	}
	return args
}
