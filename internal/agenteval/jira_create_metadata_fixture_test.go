package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"testing"
)

// prepareSyntheticJiraCreateMetadata augments a copied legacy create fixture
// with the content-free type inventory that current ATL resolves before every
// issue create. Retained corpus fixtures keep their historical name payloads;
// the selected-process copy instead proves the current id-only write shape.
func prepareSyntheticJiraCreateMetadata(t *testing.T, fixture MockFixture) MockFixture {
	t.Helper()
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)

	type createRoute struct {
		index              int
		project, issueType string
	}
	creates := make([]createRoute, 0)
	issueTypes := map[string]map[string]struct{}{}
	for index := range prepared.Routes {
		route := prepared.Routes[index]
		if route.Method != http.MethodPost || route.Path != prepared.JiraContext+"/rest/api/2/issue" {
			continue
		}
		var request struct {
			Fields struct {
				Project struct {
					Key string `json:"key"`
				} `json:"project"`
				IssueType struct {
					Name string `json:"name"`
				} `json:"issuetype"`
			} `json:"fields"`
		}
		if err := json.Unmarshal(route.RequestBody, &request); err != nil || request.Fields.Project.Key == "" || request.Fields.IssueType.Name == "" {
			t.Fatalf("decode legacy Jira create route %q: %v", route.Name, err)
		}
		if issueTypes[request.Fields.Project.Key] == nil {
			issueTypes[request.Fields.Project.Key] = map[string]struct{}{}
		}
		issueTypes[request.Fields.Project.Key][request.Fields.IssueType.Name] = struct{}{}
		creates = append(creates, createRoute{index: index, project: request.Fields.Project.Key, issueType: request.Fields.IssueType.Name})
	}
	if len(creates) == 0 {
		t.Fatal("synthetic fixture has no Jira issue-create route")
	}

	projects := make([]string, 0, len(issueTypes))
	for project := range issueTypes {
		projects = append(projects, project)
	}
	sort.Strings(projects)
	metadataNames := make(map[string]string, len(projects))
	typeIDs := make(map[string]map[string]string, len(projects))
	for projectIndex, project := range projects {
		types := make([]string, 0, len(issueTypes[project]))
		for issueType := range issueTypes[project] {
			types = append(types, issueType)
		}
		sort.Strings(types)
		values := make([]map[string]any, len(types))
		typeIDs[project] = make(map[string]string, len(types))
		for typeIndex, issueType := range types {
			id := strconv.Itoa(typeIndex + 1)
			typeIDs[project][issueType] = id
			values[typeIndex] = map[string]any{"id": id, "name": issueType}
		}
		body, err := json.Marshal(map[string]any{"startAt": 0, "isLast": true, "values": values})
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("create-metadata-%d", projectIndex+1)
		metadataNames[project] = name
		prepared.Routes = append(prepared.Routes, MockRoute{
			Name: name, Method: http.MethodGet,
			Path:        prepared.JiraContext + "/rest/api/2/issue/createmeta/" + url.PathEscape(project) + "/issuetypes",
			QueryEquals: map[string]string{"startAt": "0", "maxResults": "200"},
			Status:      http.StatusOK, Body: body, closedQuery: true,
		})
	}

	metadataByCreateRoute := make(map[string]string, len(creates))
	for _, create := range creates {
		var request map[string]any
		if err := json.Unmarshal(prepared.Routes[create.index].RequestBody, &request); err != nil {
			t.Fatal(err)
		}
		fields, ok := request["fields"].(map[string]any)
		if !ok {
			t.Fatalf("legacy Jira create route %q has no fields object", prepared.Routes[create.index].Name)
		}
		fields["issuetype"] = map[string]any{"id": typeIDs[create.project][create.issueType]}
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		prepared.Routes[create.index].RequestBody = body
		metadataByCreateRoute[prepared.Routes[create.index].Name] = metadataNames[create.project]
	}
	if len(prepared.RequestSequence) > 0 {
		sequence := make([]string, 0, len(prepared.RequestSequence)+len(creates))
		for _, name := range prepared.RequestSequence {
			if metadata, ok := metadataByCreateRoute[name]; ok {
				sequence = append(sequence, metadata)
			}
			sequence = append(sequence, name)
		}
		prepared.RequestSequence = sequence
	}
	return prepared
}

func TestPrepareSyntheticJiraCreateMetadataSplicesPreflightAndIDWrite(t *testing.T) {
	fixture := MockFixture{
		SchemaVersion: 1, JiraContext: "/jira", ConfluenceContext: "/wiki",
		Routes: []MockRoute{{
			Name: "create", Method: http.MethodPost, Path: "/jira/rest/api/2/issue",
			RequestBody: json.RawMessage(`{"fields":{"project":{"key":"TEST"},"issuetype":{"name":"Task"},"summary":"synthetic"}}`),
			Status:      http.StatusCreated, Body: json.RawMessage(`{"key":"TEST-1"}`),
		}},
		RequestSequence: []string{"create"},
	}
	prepared := prepareSyntheticJiraCreateMetadata(t, fixture)
	if !slices.Equal(prepared.RequestSequence, []string{"create-metadata-1", "create"}) {
		t.Fatalf("sequence=%v", prepared.RequestSequence)
	}
	backend, err := StartMockBackend(prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	metadata, err := http.Get(backend.Environment()["ATL_JIRA_URL"] + "/rest/api/2/issue/createmeta/TEST/issuetypes?startAt=0&maxResults=200")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.StatusCode != http.StatusOK {
		t.Fatalf("metadata status=%d", metadata.StatusCode)
	}
	_ = metadata.Body.Close()
	response, err := http.Post(backend.Environment()["ATL_JIRA_URL"]+"/rest/api/2/issue", "application/json", bytes.NewReader(prepared.Routes[0].RequestBody))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", response.StatusCode)
	}
	_ = response.Body.Close()
	if !backend.RequestSequenceComplete() {
		t.Fatal("metadata preflight and create did not complete the configured sequence")
	}
	var request struct {
		Fields struct {
			IssueType struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"issuetype"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(prepared.Routes[0].RequestBody, &request); err != nil || request.Fields.IssueType.ID != "1" || request.Fields.IssueType.Name != "" {
		t.Fatalf("prepared create payload=%s err=%v", prepared.Routes[0].RequestBody, err)
	}
}

// syntheticJiraCreateHistoricalContract is the frozen corpus contract. The
// selected-process tests derive a separate current-product contract below so
// the historical scenario and HTTP oracle remain independently observable.
type syntheticJiraCreateHistoricalContract struct {
	HTTPMethods                 map[string]int
	MaxBackendRequests          int
	MaxDuplicateBackendRequests int
}

type syntheticJiraCreateCurrentContract struct {
	Spec     RunSpec
	Scenario Scenario
}

func assertSyntheticJiraCreateHistoricalContract(
	t *testing.T,
	spec RunSpec,
	scenario Scenario,
	want syntheticJiraCreateHistoricalContract,
) {
	t.Helper()
	found := 0
	var methods map[string]int
	for _, check := range spec.Checks {
		if check.Kind != "http_methods_equal" {
			continue
		}
		var ok bool
		methods, ok = expectedHTTPMethods(check.Expected)
		if !ok {
			t.Fatalf("historical HTTP method oracle %q is invalid", check.Name)
		}
		found++
	}
	if found != 1 || !maps.Equal(methods, want.HTTPMethods) {
		t.Fatalf("historical HTTP method oracle=%v count=%d want=%v", methods, found, want.HTTPMethods)
	}
	if scenario.Budgets.MaxBackendRequests != want.MaxBackendRequests ||
		scenario.Budgets.MaxDuplicateBackendRequests != want.MaxDuplicateBackendRequests {
		t.Fatalf("historical backend budgets=%+v want requests=%d duplicates=%d",
			scenario.Budgets, want.MaxBackendRequests, want.MaxDuplicateBackendRequests)
	}
}

func deriveSyntheticJiraCreateCurrentContract(
	t *testing.T,
	spec RunSpec,
	scenario Scenario,
	methods map[string]int,
	duplicates int,
) syntheticJiraCreateCurrentContract {
	t.Helper()
	expected, err := json.Marshal(methods)
	if err != nil {
		t.Fatal(err)
	}
	current := syntheticJiraCreateCurrentContract{Spec: spec, Scenario: scenario}
	current.Spec.Checks = slices.Clone(spec.Checks)
	found := 0
	for index := range current.Spec.Checks {
		if current.Spec.Checks[index].Kind != "http_methods_equal" {
			continue
		}
		current.Spec.Checks[index].Expected = expected
		found++
	}
	if found != 1 {
		t.Fatalf("expected one HTTP method oracle, got %d", found)
	}
	requests := 0
	for method, count := range methods {
		if count < 1 || !slices.Contains(current.Scenario.Budgets.AllowedHTTPMethods, method) {
			t.Fatalf("current HTTP method geometry %s=%d is outside the scenario allowlist %v", method, count, current.Scenario.Budgets.AllowedHTTPMethods)
		}
		requests += count
	}
	if duplicates < 0 || duplicates > requests {
		t.Fatalf("current duplicate request geometry=%d requests=%d", duplicates, requests)
	}
	current.Scenario.Budgets.MaxBackendRequests = requests
	current.Scenario.Budgets.MaxDuplicateBackendRequests = duplicates
	if err := current.Spec.ValidateAgainstScenario(current.Scenario); err != nil {
		t.Fatalf("derive current Jira create contract: %v", err)
	}
	return current
}

func TestDeriveSyntheticJiraCreateCurrentContractKeepsHistoricalContract(t *testing.T) {
	root := "../../benchmarks/agent-eval/jira-meeting-tasks-workflow"
	historicalSpec := loadRepositoryRunSpec(t, root+"/run.cli.codex.json")
	historicalScenario := loadRepositoryScenario(t, root+"/scenario.v1.json")
	historical := syntheticJiraCreateHistoricalContract{
		HTTPMethods:        map[string]int{"GET": 4, "POST": 3},
		MaxBackendRequests: 7, MaxDuplicateBackendRequests: 2,
	}
	assertSyntheticJiraCreateHistoricalContract(t, historicalSpec, historicalScenario, historical)

	currentMethods := map[string]int{"GET": 7, "POST": 3}
	current := deriveSyntheticJiraCreateCurrentContract(t, historicalSpec, historicalScenario, currentMethods, 4)
	if current.Scenario.Budgets.MaxBackendRequests != 10 || current.Scenario.Budgets.MaxDuplicateBackendRequests != 4 {
		t.Fatalf("current backend budgets=%+v", current.Scenario.Budgets)
	}
	assertSyntheticJiraCreateHistoricalContract(t, current.Spec, current.Scenario, syntheticJiraCreateHistoricalContract{
		HTTPMethods: currentMethods, MaxBackendRequests: 10, MaxDuplicateBackendRequests: 4,
	})
	coverage := make(map[string]bool, len(current.Scenario.RequiredMetrics))
	for _, metric := range current.Scenario.RequiredMetrics {
		coverage[metric] = true
	}
	checks := make(map[string]bool, len(current.Spec.Checks))
	for _, check := range current.Spec.Checks {
		checks[check.Name] = true
	}
	observation := Observation{
		SchemaVersion: ObservationSchemaVersion, ScenarioID: current.Scenario.ID,
		Variant: current.Spec.Variant, Surface: current.Spec.Surface,
		BackendObservation: BackendObservationHTTP, SafetyAssurance: SafetyAssuranceObservedHTTP,
		Runtime: Runtime{Provider: "deterministic", ATLVersion: "test"},
		Metrics: InputMetrics{
			AgentTurns: 1, ToolCalls: 1, ATLInvocations: 1, DuplicateBackendRequests: 4,
			OutputBytes: 1, InputTokens: 1, OutputTokens: 1, MainThreadInputTokens: 1,
			MainThreadOutputTokens: 1, EstimatedCostMicroUSD: 1, DurationMillis: 1,
		},
		Coverage: coverage, HTTPMethods: currentMethods, Checks: checks,
	}
	result, err := Evaluate(current.Scenario, observation)
	if err != nil || result.Status != "pass" || result.Metrics.BackendRequests != 10 || result.Metrics.DuplicateBackendRequests != 4 {
		t.Fatalf("current geometry did not satisfy derived budget: result=%+v err=%v", result, err)
	}
	overBudget := maps.Clone(currentMethods)
	overBudget[http.MethodGet]++
	observation.HTTPMethods = overBudget
	result, err = Evaluate(current.Scenario, observation)
	if err != nil || result.Status != "fail" || !containsViolation(result.Violations, "budget_exceeded", "backend_requests") {
		t.Fatalf("derived request budget did not reject added metadata read: result=%+v err=%v", result, err)
	}
	assertSyntheticJiraCreateHistoricalContract(t, historicalSpec, historicalScenario, historical)
}
