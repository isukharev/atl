package agenteval

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

func prepareJiraHistoryProcessFixture(
	t *testing.T,
	fixture MockFixture,
	key string,
	expectedPages, reads int,
) MockFixture {
	t.Helper()
	if key == "" || expectedPages < 1 || expectedPages > len(fixture.Routes) || reads < 1 ||
		len(fixture.Routes) < 1 || len(fixture.Routes) > 2 || len(fixture.RequestSequence) != 0 {
		t.Fatalf("Jira history process contract is incomplete: key=%q pages=%d reads=%d routes=%d sequence=%v",
			key, expectedPages, reads, len(fixture.Routes), fixture.RequestSequence)
	}
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	routeNames := make([]string, len(prepared.Routes))
	wantStarts := []string{"0", "3"}
	for index := range prepared.Routes {
		route := prepared.Routes[index]
		wantPath := fixture.JiraContext + "/rest/api/2/issue/" + key + "/changelog"
		if route.Name != "" || route.Method != "GET" || route.Path != wantPath ||
			route.Status != 200 || len(route.Responses) != 0 || len(route.RequestBody) != 0 ||
			len(route.QueryContains) != 0 || len(route.QueryEquals) != 2 ||
			route.QueryEquals["maxResults"] != "100" || route.QueryEquals["startAt"] != wantStarts[index] {
			t.Fatalf("retained Jira history route %d drifted: %+v", index, route)
		}
		routeNames[index] = "changelog-page-" + wantStarts[index]
		route.Name = routeNames[index]
		route.closedQuery = true
		prepared.Routes[index] = route
	}
	prepared.RequestSequence = make([]string, 0, expectedPages*reads)
	for range reads {
		prepared.RequestSequence = append(prepared.RequestSequence, routeNames[:expectedPages]...)
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Jira history process fixture: %v", err)
	}
	return prepared
}

func startJiraHistoryCLIProcess(
	t *testing.T,
	fixture MockFixture,
	key string,
	expectedPages int,
	policy CLICommandPolicy,
) *SyntheticATLProcess {
	t.Helper()
	return startJiraHistoryProcess(t, prepareJiraHistoryProcessFixture(t, fixture, key, expectedPages, 1),
		policy, nil)
}

func startJiraHistoryMCPProcess(
	t *testing.T,
	fixture MockFixture,
	cohort jiraHistorySummaryMCPCohort,
	admitted []MCPInvocation,
	reads int,
) *SyntheticATLProcess {
	t.Helper()
	if len(admitted) != reads {
		t.Fatalf("Jira history MCP admissions=%d want reads=%d", len(admitted), reads)
	}
	return startJiraHistoryProcess(t,
		prepareJiraHistoryProcessFixture(t, fixture, cohort.key, cohort.expectedGETs, reads),
		CLICommandPolicy{}, admitted)
}

func startJiraHistoryProcess(
	t *testing.T,
	fixture MockFixture,
	policy CLICommandPolicy,
	admitted []MCPInvocation,
) *SyntheticATLProcess {
	t.Helper()
	config := SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: fixture,
		ScratchRoot: privateSyntheticATLScratch(t), CLIPolicy: policy,
	}
	if len(admitted) > 0 {
		config.MCPService = "jira"
		config.MCPInvocations = slices.Clone(admitted)
	}
	process, err := StartSyntheticATLProcess(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Jira history process: %v", err)
		}
	})
	return process
}

func callJiraHistoryMCPProcess(
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
		t.Fatalf("selected Jira history MCP read failed: text_items=%d", len(result.TextContent))
	}
	assertRepositoryMCPTextMatchesStructured(t, result)
	return result
}

func assertJiraHistoryCLIDivergencesRefused(
	t *testing.T,
	fixture MockFixture,
	key string,
	expectedPages int,
	policy CLICommandPolicy,
	admitted []string,
) {
	t.Helper()
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "issue", args: replaceJiraHistoryCLIArgument(admitted, key, key+"-OTHER")},
		{name: "summary-only", args: slices.Delete(slices.Clone(admitted), len(admitted)-1, len(admitted))},
		{name: "extra-field", args: append(slices.Clone(admitted), "--field", "customfield_99999")},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := startJiraHistoryCLIProcess(t, fixture, key, expectedPages, policy)
			if _, err := process.RunCLIJSON(t.Context(), test.args...); err == nil {
				t.Fatalf("unadmitted Jira history CLI %s divergence crossed the process boundary", test.name)
			}
			assertJiraHistoryPreBackendRefusal(t, process)
		})
	}
}

func replaceJiraHistoryCLIArgument(arguments []string, old, replacement string) []string {
	replaced := slices.Clone(arguments)
	for index, argument := range replaced {
		if argument == old {
			replaced[index] = replacement
			break
		}
	}
	return replaced
}

func assertJiraHistoryMCPDivergencesRefused(
	t *testing.T,
	fixture MockFixture,
	cohort jiraHistorySummaryMCPCohort,
	admitted MCPInvocation,
) {
	t.Helper()
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "key", mutate: func(arguments map[string]any) { arguments["key"] = cohort.key + "-OTHER" }},
		{name: "max-bytes", mutate: func(arguments map[string]any) {
			arguments["max_bytes"] = float64(jiraHistorySummaryMCPMaxBytes / 2)
		}},
		{name: "fields", mutate: func(arguments map[string]any) {
			arguments["fields"] = []any{"customfield_99999"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := startJiraHistoryMCPProcess(t, fixture, cohort, []MCPInvocation{admitted}, 1)
			var arguments map[string]any
			if err := json.Unmarshal(admitted.Arguments, &arguments); err != nil {
				t.Fatal(err)
			}
			test.mutate(arguments)
			divergent := mustMCPInvocation(t, admitted.Tool, arguments)
			if _, err := process.CallMCPJSON(t.Context(), divergent); err == nil {
				t.Fatalf("unadmitted Jira history MCP %s divergence crossed the process boundary", test.name)
			}
			assertJiraHistoryPreBackendRefusal(t, process)
		})
	}
}

func assertJiraHistoryPreBackendRefusal(t *testing.T, process *SyntheticATLProcess) {
	t.Helper()
	summary := process.Summary()
	if process.RequestSequenceComplete() || len(summary.HTTPMethods) != 0 ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
		len(summary.CLIInvocations) != 0 || len(summary.MCPInvocations) != 0 {
		t.Fatalf("Jira history divergence was not refused pre-backend: summary=%+v sequence_complete=%t",
			summary, process.RequestSequenceComplete())
	}
}

func decodeJiraHistoryProcessSummary(t *testing.T, data []byte) JiraHistorySummaryView {
	t.Helper()
	view, err := DecodeJiraHistorySummary(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode selected Jira history summary: %v", err)
	}
	return view
}

func jiraHistoryCLIProcessPolicy(spec RunSpec) CLICommandPolicy {
	return CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: slices.Clone(spec.AllowedCLICommands)}
}

func jiraHistoryExpectedCLIInvocationCount(summary SyntheticATLProcessSummary) bool {
	return len(summary.CLIInvocations) == 1 && summary.CLIInvocations["jira_history"] == 1
}
