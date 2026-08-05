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
	jiraPortfolioBoardListRoute = "portfolio_board_list"
	jiraPortfolioMetadataRoute  = "portfolio_structure_metadata"
	jiraPortfolioForestRoute    = "portfolio_structure_forest"
	jiraPortfolioValuesRoute    = "portfolio_structure_values"
)

type jiraPortfolioWorkflowEvidence struct {
	Capabilities JiraPortfolioCapabilityCatalog
	Boards       JiraPortfolioBoardList
	Folders      JiraPortfolioStructureFolders
	Summary      SyntheticATLProcessSummary
}

func startJiraPortfolioDiscoveryProcess(
	t *testing.T,
	fixture MockFixture,
	policy CLICommandPolicy,
	project string,
	limit int,
	structureID int64,
) *SyntheticATLProcess {
	t.Helper()
	prepared := prepareJiraPortfolioDiscoveryFixture(t, fixture, project, limit, structureID)
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepared,
		ScratchRoot: privateSyntheticATLScratch(t), CLIPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Jira portfolio process: %v", err)
		}
	})
	return process
}

func prepareJiraPortfolioDiscoveryFixture(
	t *testing.T,
	fixture MockFixture,
	project string,
	limit int,
	structureID int64,
) MockFixture {
	t.Helper()
	if project == "" || limit < 1 || structureID <= 0 || len(fixture.Routes) != 4 || len(fixture.RequestSequence) != 0 {
		t.Fatalf("portfolio process fixture is incomplete: project=%q limit=%d structure=%d routes=%d sequence=%v",
			project, limit, structureID, len(fixture.Routes), fixture.RequestSequence)
	}
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	structure := strconv.FormatInt(structureID, 10)
	checks := []struct {
		name, method, path string
		query              map[string]string
		requestBody        bool
	}{
		{jiraPortfolioBoardListRoute, "GET", fixture.JiraContext + "/rest/agile/1.0/board", map[string]string{
			"startAt": "0", "maxResults": strconv.Itoa(limit), "projectKeyOrId": project,
		}, false},
		{jiraPortfolioMetadataRoute, "GET", fixture.JiraContext + "/rest/structure/2.0/structure/" + structure, map[string]string{
			"withOwner": "true", "withPermissions": "true",
		}, false},
		{jiraPortfolioForestRoute, "GET", fixture.JiraContext + "/rest/structure/2.0/forest/latest", map[string]string{
			"s": `{"structureId":` + structure + `}`,
		}, false},
		{jiraPortfolioValuesRoute, "POST", fixture.JiraContext + "/rest/structure/2.0/value", nil, true},
	}
	for index, check := range checks {
		route := prepared.Routes[index]
		if route.Name != "" || route.Method != check.method || route.Path != check.path || route.Status != 200 ||
			len(route.Responses) != 0 || len(route.QueryContains) != 0 || !maps.Equal(route.QueryEquals, check.query) {
			t.Fatalf("portfolio route %d drifted: %+v", index, route)
		}
		if check.requestBody {
			if len(route.RequestBody) == 0 || !jsonValid(route.RequestBody) {
				t.Fatalf("portfolio route %q lacks a static JSON request body", check.name)
			}
		} else if len(route.RequestBody) != 0 {
			t.Fatalf("portfolio GET route %q carries a request body", check.name)
		}
		route.Name = check.name
		route.closedQuery = true
		prepared.Routes[index] = route
	}
	prepared.RequestSequence = []string{
		jiraPortfolioBoardListRoute, jiraPortfolioMetadataRoute, jiraPortfolioForestRoute, jiraPortfolioValuesRoute,
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare portfolio process fixture: %v", err)
	}
	return prepared
}

func executeJiraPortfolioDiscoveryProcess(
	t *testing.T,
	process *SyntheticATLProcess,
	commands [][]string,
) jiraPortfolioWorkflowEvidence {
	t.Helper()
	if len(commands) != 3 {
		t.Fatalf("portfolio workflow requires three reviewed commands, got %d", len(commands))
	}
	capabilitiesJSON := callSelectedJiraPortfolioJSON(t, process, commands[0])
	capabilities, err := DecodeJiraPortfolioCapabilityCatalog(bytes.NewReader(capabilitiesJSON))
	if err != nil {
		t.Fatalf("decode selected Jira portfolio capability catalog: %v", err)
	}
	boardsJSON := callSelectedJiraPortfolioJSON(t, process, commands[1])
	foldersJSON := callSelectedJiraPortfolioJSON(t, process, commands[2])
	boards, err := DecodeJiraPortfolioBoardList(bytes.NewReader(boardsJSON))
	if err != nil {
		t.Fatalf("decode selected portfolio board list: %v", err)
	}
	folders, err := DecodeJiraPortfolioStructureFolders(bytes.NewReader(foldersJSON))
	if err != nil {
		t.Fatalf("decode selected portfolio structure folders: %v", err)
	}
	summary := process.Summary()
	if !process.RequestSequenceComplete() || !equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": 3, "POST": 1}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
		!maps.Equal(summary.CLIInvocations, map[string]int{"capabilities": 1, "jira_board_list": 1, "jira_structure_folders": 1}) ||
		len(summary.MCPInvocations) != 0 {
		t.Fatalf("selected portfolio process accounting drifted: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
	return jiraPortfolioWorkflowEvidence{
		Capabilities: capabilities, Boards: boards, Folders: folders, Summary: summary,
	}
}

func callSelectedJiraPortfolioJSON(t *testing.T, process *SyntheticATLProcess, args []string) []byte {
	t.Helper()
	result, err := process.RunCLIJSON(t.Context(), args...)
	if err != nil {
		t.Fatalf("selected portfolio command %v: %v", args, err)
	}
	if result.ExitCode != 0 || len(result.JSON) == 0 || len(result.Stderr) != 0 {
		t.Fatalf("selected portfolio command %v exit=%d stdout_bytes=%d stderr_bytes=%d", args, result.ExitCode, len(result.JSON), len(result.Stderr))
	}
	return append([]byte(nil), result.JSON...)
}

func assertJiraPortfolioDiscoveryAdmissionRefused(
	t *testing.T,
	fixture MockFixture,
	policy CLICommandPolicy,
	project string,
	limit int,
	structureID int64,
	commands [][]string,
) {
	t.Helper()
	if len(commands) != 3 {
		t.Fatal("portfolio admission contract requires exactly three commands")
	}
	for _, test := range []struct {
		name string
		args []string
	}{
		{"task", []string{"capabilities", "--task", "jira/unknown"}},
		{"board project", []string{"jira", "board", "list", "--project", project + "-OTHER", "--limit", strconv.Itoa(limit)}},
		{"board output", append(slices.Clone(commands[1]), "-o", "text")},
		{"structure id", []string{"jira", "structure", "folders", strconv.FormatInt(structureID+1, 10)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := startJiraPortfolioDiscoveryProcess(t, fixture, policy, project, limit, structureID)
			if _, err := process.RunCLIJSON(t.Context(), test.args...); err == nil {
				t.Fatalf("unreviewed portfolio command %v crossed exact process admission", test.args)
			}
			assertJiraPortfolioPreBackendRefusal(t, process)
		})
	}
}

func assertJiraPortfolioPreBackendRefusal(t *testing.T, process *SyntheticATLProcess) {
	t.Helper()
	summary := process.Summary()
	if process.RequestSequenceComplete() || len(summary.HTTPMethods) != 0 || summary.UnexpectedRequests != 0 ||
		summary.DuplicateRequests != 0 || len(summary.CLIInvocations) != 0 || len(summary.MCPInvocations) != 0 {
		t.Fatalf("portfolio command was not refused before backend work: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
}

func TestJiraPortfolioDiscoveryProcessSequenceDriftFailsClosed(t *testing.T) {
	root := filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-portfolio-source-discovery")
	fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli.codex.json"))
	policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands}
	prepared := prepareJiraPortfolioDiscoveryFixture(t, fixture, "ZX", 10, 123)
	prepared.RequestSequence = []string{
		jiraPortfolioMetadataRoute, jiraPortfolioBoardListRoute, jiraPortfolioForestRoute, jiraPortfolioValuesRoute,
	}
	if err := prepared.Validate(); err != nil {
		t.Fatal(err)
	}
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepared,
		ScratchRoot: privateSyntheticATLScratch(t), CLIPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close sequence-drift process: %v", err)
		}
	})
	result, err := process.RunCLIJSON(t.Context(), "jira", "board", "list", "--project", "ZX", "--limit", "10")
	if err != nil {
		t.Fatal(err)
	}
	summary := process.Summary()
	if result.ExitCode == 0 || process.RequestSequenceComplete() || !equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": 1}) ||
		summary.UnexpectedRequests != 1 || summary.DuplicateRequests != 0 ||
		!maps.Equal(summary.CLIInvocations, map[string]int{"jira_board_list": 1}) {
		t.Fatalf("sequence drift did not fail closed: exit=%d summary=%+v complete=%t", result.ExitCode, summary, process.RequestSequenceComplete())
	}
}

func jsonValid(data []byte) bool {
	return len(data) > 0 && json.Valid(data)
}
