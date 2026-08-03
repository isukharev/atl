package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mdwiki"
)

const (
	triagePrimaryDirectory = "jira-triage-issue-workflow"
	triageHoldoutDirectory = "jira-triage-issue-workflow-holdout"
	triageThreshold        = 75
	triageColumns          = "key,summary,status,updated"
)

type triageCandidate struct {
	key, status                         string
	signature, component, trigger, open bool
	score                               int
}

type triageCohort struct {
	directory, project, signature, component, trigger string
	queries                                           []string
	queryKeys                                         [][]string
	candidates                                        []triageCandidate
	decision, targetKey, createdKey, commentID        string
	newSummary                                        string
	sequence                                          []string
	methods                                           map[string]int
	exitCodes                                         []int
	duplicates, failures                              int
}

var triageCohorts = []triageCohort{
	{
		directory: triagePrimaryDirectory, project: "LAB", signature: "CacheRefreshError", component: "Cache", trigger: "token rotation",
		queries: []string{
			`project = LAB AND text ~ "CacheRefreshError refresh token" AND type = Bug ORDER BY updated DESC`,
			`project = LAB AND summary ~ "cache refresh" AND type = Bug ORDER BY updated DESC`,
		},
		queryKeys: [][]string{{"LAB-41"}, {"LAB-41", "LAB-52"}},
		candidates: []triageCandidate{
			{key: "LAB-41", status: "Done", signature: true, component: true, score: 65},
			{key: "LAB-52", status: "Open", component: true, trigger: true, open: true, score: 60},
		},
		decision: "create", createdKey: "LAB-101", newSummary: "Cache: refresh fails after token rotation",
		sequence: []string{"search_specific", "search_broad", "candidate_one", "candidate_two", "create"},
		methods:  map[string]int{"GET": 4, "POST": 1}, exitCodes: []int{0, 0, 0, 0, 0},
	},
	{
		directory: triageHoldoutDirectory, project: "OPS", signature: "LeaseRenewalError", component: "Indexer", trigger: "lease renewal",
		queries: []string{
			`project = OPS AND text ~ "LeaseRenewalError retry storm" AND type = Bug ORDER BY updated DESC`,
			`project = OPS AND summary ~ "indexer retry" AND type = Bug ORDER BY updated DESC`,
		},
		queryKeys: [][]string{{"OPS-88"}, {"OPS-88"}},
		candidates: []triageCandidate{
			{key: "OPS-88", status: "Open", signature: true, component: true, trigger: true, open: true, score: 100},
		},
		decision: "comment", targetKey: "OPS-88", commentID: "801", newSummary: "Indexer: retry storm after lease renewal",
		sequence: []string{"search_specific", "search_broad", "candidate", "comments", "comment", "comments"},
		methods:  map[string]int{"GET": 5, "POST": 1}, exitCodes: []int{0, 0, 0, 0, 1, 0}, duplicates: 1, failures: 1,
	},
}

func TestRepositoryJiraTriageIssueFixturesDriveProductionWorkflowOracles(t *testing.T) {
	for _, cohort := range triageCohorts {
		cohort := cohort
		t.Run(cohort.directory, func(t *testing.T) {
			root := triageRoot(cohort.directory)
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			assertTriageFixtureTopology(t, fixture, cohort)
			backend, final := executeTriageProductionWorkflow(t, root, fixture, cohort)
			if !backend.RequestSequenceComplete() {
				t.Fatal("production workflow did not complete the exact request sequence")
			}
			methods, unexpected, duplicates := backend.Summary()
			if !equalHTTPMethods(methods, cohort.methods) || unexpected != 0 || duplicates != cohort.duplicates {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
			assertTriageProviderOracles(t, root, cohort, final, methods)
		})
	}
}

func executeTriageProductionWorkflow(t *testing.T, root string, fixture MockFixture, cohort triageCohort) (*MockBackend, []byte) {
	t.Helper()
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	for key, value := range backend.Environment() {
		t.Setenv(key, value)
	}
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	cfg := &config.Config{JiraURL: backend.Environment()["ATL_JIRA_URL"]}
	jira, err := app.NewJira(cfg, "benchmark-contract")
	if err != nil {
		t.Fatal(err)
	}
	var searches []*app.IssueList
	for index, query := range cohort.queries {
		list, searchErr := jira.SearchIssueList(context.Background(), query, strings.Split(triageColumns, ","), 10, "")
		if searchErr != nil {
			t.Fatal(searchErr)
		}
		if !list.Page.Complete || !slices.Equal(issueListKeys(list), cohort.queryKeys[index]) {
			t.Fatalf("search %d complete=%t keys=%v", index, list.Page.Complete, issueListKeys(list))
		}
		searches = append(searches, list)
	}
	issues := make([]*domain.Issue, 0, len(cohort.candidates))
	for _, expected := range cohort.candidates {
		issue, issueErr := jira.Issue(context.Background(), expected.key, nil)
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		actual := scoreTriageIssue(issue, cohort)
		if actual != expected {
			t.Fatalf("candidate %s score=%+v want=%+v", issue.Key, actual, expected)
		}
		issues = append(issues, issue)
	}
	if !triageMayWrite(searches, issues) {
		t.Fatal("complete search and qualification unexpectedly refused the approved branch")
	}
	if decision := triageDecision(cohort.candidates); decision != cohort.decision {
		t.Fatalf("decision=%q want=%q", decision, cohort.decision)
	}
	if cohort.decision == "create" {
		wiki := triageMarkdown(t, filepath.Join(root, "workspace", "new-bug.md"))
		created, createErr := jira.Create(context.Background(), cohort.project, "Bug", cohort.newSummary, wiki, nil)
		if createErr != nil || created.Key != cohort.createdKey {
			t.Fatalf("create=%+v err=%v", created, createErr)
		}
	} else {
		before, commentsErr := jira.Comments(context.Background(), cohort.targetKey)
		if commentsErr != nil {
			t.Fatal(commentsErr)
		}
		wiki := triageMarkdown(t, filepath.Join(root, "workspace", "duplicate-comment.md"))
		if _, commentErr := jira.Comment(context.Background(), cohort.targetKey, wiki); commentErr == nil {
			t.Fatal("ambiguous synthetic comment unexpectedly succeeded")
		}
		after, commentsErr := jira.Comments(context.Background(), cohort.targetKey)
		if commentsErr != nil {
			t.Fatal(commentsErr)
		}
		id, ok := reconcileTriageComment(before, after, string(wiki))
		if !ok || id != cohort.commentID {
			t.Fatalf("reconciled id=%q ok=%t", id, ok)
		}
	}
	return backend, triageFinal(t, cohort)
}

func issueListKeys(list *app.IssueList) []string {
	keys := make([]string, len(list.Rows))
	for index, row := range list.Rows {
		keys[index] = row.Key
	}
	return keys
}

func triageMarkdown(t *testing.T, path string) []byte {
	t.Helper()
	markdown, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wiki, err := mdwiki.ConvertDocument(string(markdown))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(wiki)
}

func scoreTriageIssue(issue *domain.Issue, cohort triageCohort) triageCandidate {
	value := triageCandidate{key: issue.Key, status: issue.Status}
	value.signature = strings.Contains(issue.Body, cohort.signature)
	value.component = strings.Contains(issue.Summary, cohort.component)
	value.trigger = strings.Contains(issue.Body, cohort.trigger)
	value.open = issue.Status == "Open"
	if value.signature {
		value.score += 40
	}
	if value.component {
		value.score += 25
	}
	if value.trigger {
		value.score += 20
	}
	if value.open {
		value.score += 15
	}
	return value
}

func triageDecision(candidates []triageCandidate) string {
	for _, candidate := range candidates {
		if candidate.open && candidate.score >= triageThreshold {
			return "comment"
		}
	}
	return "create"
}

func triageMayWrite(searches []*app.IssueList, issues []*domain.Issue) bool {
	if len(searches) != 2 || len(issues) == 0 {
		return false
	}
	return searches[0].Page.Complete && searches[1].Page.Complete
}

func reconcileTriageComment(before, after []domain.Comment, body string) (string, bool) {
	baseline := make(map[string]struct{}, len(before))
	for _, comment := range before {
		baseline[comment.ID] = struct{}{}
	}
	var matches []domain.Comment
	for _, comment := range after {
		if _, existed := baseline[comment.ID]; !existed && comment.Body == body {
			matches = append(matches, comment)
		}
	}
	if len(matches) != 1 {
		return "", false
	}
	return matches[0].ID, true
}

func triageFinal(t *testing.T, cohort triageCohort) []byte {
	t.Helper()
	queries := make([]map[string]any, len(cohort.queries))
	for index, query := range cohort.queries {
		name := "specific"
		if index == 1 {
			name = "broad"
		}
		queries[index] = map[string]any{"name": name, "jql": query, "complete": true, "keys": cohort.queryKeys[index]}
	}
	candidates := make([]map[string]any, len(cohort.candidates))
	for index, candidate := range cohort.candidates {
		candidates[index] = map[string]any{
			"key": candidate.key, "status": candidate.status, "signature_match": candidate.signature,
			"component_match": candidate.component, "trigger_match": candidate.trigger,
			"open": candidate.open, "score": candidate.score,
		}
	}
	var target, created, comment any
	if cohort.targetKey != "" {
		target = cohort.targetKey
	}
	if cohort.createdKey != "" {
		created = cohort.createdKey
	}
	if cohort.commentID != "" {
		comment = cohort.commentID
	}
	outcome := "created"
	if cohort.decision == "comment" {
		outcome = "commented_reconciled"
	}
	document := map[string]any{
		"approval": map[string]any{"state": "explicit-conditional-synthetic", "threshold": triageThreshold, "authorized_actions": []string{"create", "comment"}, "max_actions": 1},
		"queries":  queries, "candidates": candidates, "decision": cohort.decision,
		"target_key": target, "created_key": created, "comment_id": comment,
		"comment_reconciled": cohort.decision == "comment", "write_attempts": 1,
		"replayed": false, "outcome": outcome, "cli_failures": cohort.failures, "next_action": "complete",
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertTriageProviderOracles(t *testing.T, root string, cohort triageCohort, final []byte, methods map[string]int) {
	t.Helper()
	for _, provider := range []string{"codex", "claude"} {
		spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli."+provider+".json"))
		schema, err := os.ReadFile(filepath.Join(root, spec.ResponseSchemaFile))
		if err != nil {
			t.Fatal(err)
		}
		providerSchema, err := providerResponseSchema(spec, schema)
		if err != nil {
			t.Fatal(err)
		}
		for name, candidate := range map[string][]byte{"retained": schema, "provider": providerSchema} {
			if err := validateJSONSchemaSubsetInstance(candidate, final); err != nil {
				t.Fatalf("%s %s schema rejected production final: %v", spec.Provider, name, err)
			}
		}
		checks, err := evaluateRunChecks(spec.Checks, final, "", len(cohort.exitCodes), cohort.failures, 0, 1,
			map[string]int{"atl:triage-issue": 1}, 0, 0, methods, true, cohort.exitCodes)
		if err != nil {
			t.Fatal(err)
		}
		for name, passed := range checks {
			if !passed {
				t.Fatalf("%s production final failed %q", spec.Provider, name)
			}
		}
		assertTriageSemanticMutationsFail(t, spec, schema, final, cohort, methods)
	}
}

func assertTriageSemanticMutationsFail(t *testing.T, spec RunSpec, schema, final []byte, cohort triageCohort, methods map[string]int) {
	t.Helper()
	mutations := []struct {
		field, check string
		value        any
	}{
		{"approval", "approval_correct", map[string]any{"state": "explicit-conditional-synthetic", "threshold": 74}},
		{"queries", "queries_correct", []any{}}, {"candidates", "candidates_correct", []any{}},
		{"decision", "decision_correct", "wrong"}, {"target_key", "target_correct", "WRONG-1"},
		{"created_key", "created_key_correct", "WRONG-2"}, {"comment_id", "comment_id_correct", "wrong"},
		{"comment_reconciled", "reconciled_correct", cohort.decision != "comment"}, {"write_attempts", "write_attempts_correct", 2},
		{"replayed", "replayed_false", true}, {"outcome", "outcome_correct", "wrong"},
		{"cli_failures", "failure_report_correct", 99}, {"next_action", "next_action_correct", "wrong"},
	}
	for _, mutation := range mutations {
		var document map[string]any
		if err := json.Unmarshal(final, &document); err != nil {
			t.Fatal(err)
		}
		document[mutation.field] = mutation.value
		mutated, _ := json.Marshal(document)
		checks, err := evaluateRunChecks(spec.Checks, mutated, "", len(cohort.exitCodes), cohort.failures, 0, 1,
			map[string]int{"atl:triage-issue": 1}, 0, 0, methods, true, cohort.exitCodes)
		if err != nil {
			t.Fatal(err)
		}
		if checks[mutation.check] {
			t.Fatalf("mutation %s passed %s", mutation.field, mutation.check)
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"candidate status": func(document map[string]any) {
			document["candidates"].([]any)[0].(map[string]any)["status"] = "Altered"
		},
		"candidate score": func(document map[string]any) {
			document["candidates"].([]any)[0].(map[string]any)["score"] = float64(99)
		},
		"candidate key": func(document map[string]any) {
			document["candidates"].([]any)[0].(map[string]any)["key"] = "WRONG-9"
		},
	} {
		var document map[string]any
		if err := json.Unmarshal(final, &document); err != nil {
			t.Fatal(err)
		}
		mutate(document)
		mutated, _ := json.Marshal(document)
		checks, err := evaluateRunChecks(spec.Checks, mutated, "", len(cohort.exitCodes), cohort.failures, 0, 1,
			map[string]int{"atl:triage-issue": 1}, 0, 0, methods, true, cohort.exitCodes)
		if err != nil {
			t.Fatal(err)
		}
		if checks["candidates_correct"] {
			t.Fatalf("%s mutation passed candidates_correct", name)
		}
	}
	var document map[string]any
	_ = json.Unmarshal(final, &document)
	document["unexpected"] = true
	extra, _ := json.Marshal(document)
	if err := validateJSONSchemaSubsetInstance(schema, extra); err == nil {
		t.Fatal("closed response schema accepted an extra field")
	}
}

func assertTriageFixtureTopology(t *testing.T, fixture MockFixture, cohort triageCohort) {
	t.Helper()
	if !slices.Equal(fixture.RequestSequence, cohort.sequence) {
		t.Fatalf("sequence=%v want=%v", fixture.RequestSequence, cohort.sequence)
	}
	for _, route := range fixture.Routes {
		switch {
		case strings.HasPrefix(route.Name, "search_"):
			if route.Method != "GET" || route.Path != "/jira/rest/api/2/search" || route.QueryEquals["maxResults"] != "10" || route.QueryEquals["startAt"] != "0" || route.QueryEquals["fields"] != "summary,status,updated" {
				t.Fatalf("search route drifted: %+v", route)
			}
		case strings.HasPrefix(route.Name, "candidate"):
			if route.Method != "GET" || !strings.Contains(route.Path, "/issue/") || route.QueryEquals["fields"] == "" {
				t.Fatalf("candidate route drifted: %+v", route)
			}
		case route.Name == "create":
			if route.Method != "POST" || route.Path != "/jira/rest/api/2/issue" || len(route.RequestBody) == 0 {
				t.Fatalf("create route drifted: %+v", route)
			}
		case route.Name == "comments":
			if len(route.Responses) != 2 || route.QueryEquals["startAt"] != "0" || route.QueryEquals["maxResults"] != "100" {
				t.Fatalf("stateful comments route drifted: %+v", route)
			}
		case route.Name == "comment":
			if route.Method != "POST" || route.Status != 500 || len(route.RequestBody) == 0 {
				t.Fatalf("comment route drifted: %+v", route)
			}
		}
	}
}

func TestRepositoryJiraTriageIssueFailsClosedBoundaries(t *testing.T) {
	primary, holdout := triageCohorts[0], triageCohorts[1]
	primaryFixture := loadRepositoryMockFixture(t, filepath.Join(triageRoot(primary.directory), "fixture.json"))
	holdoutFixture := loadRepositoryMockFixture(t, filepath.Join(triageRoot(holdout.directory), "fixture.json"))
	t.Run("early create", func(t *testing.T) {
		backend := startTriageRawBackend(t, primaryFixture)
		before := triageRequestIndex(backend)
		if status := sendTriageRoute(t, backend, triageRoute(t, primaryFixture, "create"), nil); status != http.StatusNotFound || triageRequestIndex(backend) != before {
			t.Fatalf("status=%d cursor=%d", status, triageRequestIndex(backend))
		}
	})
	t.Run("comment before baseline", func(t *testing.T) {
		backend := startTriageRawBackend(t, holdoutFixture)
		for _, name := range []string{"search_specific", "search_broad", "candidate"} {
			if sendTriageRoute(t, backend, triageRoute(t, holdoutFixture, name), nil) != http.StatusOK {
				t.Fatalf("route %s failed", name)
			}
		}
		before := triageRequestIndex(backend)
		if status := sendTriageRoute(t, backend, triageRoute(t, holdoutFixture, "comment"), nil); status != http.StatusNotFound || triageRequestIndex(backend) != before {
			t.Fatalf("status=%d cursor=%d", status, triageRequestIndex(backend))
		}
	})
	t.Run("wrong branch", func(t *testing.T) {
		backend := startTriageRawBackend(t, primaryFixture)
		for _, name := range primary.sequence[:4] {
			if sendTriageRoute(t, backend, triageRoute(t, primaryFixture, name), nil) != http.StatusOK {
				t.Fatalf("route %s failed", name)
			}
		}
		body := triageMarkdown(t, filepath.Join(triageRoot(primary.directory), "workspace", "duplicate-comment.md"))
		payload, _ := json.Marshal(map[string]string{"body": string(body)})
		before := triageRequestIndex(backend)
		status := sendTriageRaw(t, backend, "POST", "/jira/rest/api/2/issue/LAB-52/comment", nil, payload)
		if status != http.StatusNotFound || triageRequestIndex(backend) != before {
			t.Fatalf("status=%d cursor=%d", status, triageRequestIndex(backend))
		}
	})
	t.Run("holdout create branch rejected", func(t *testing.T) {
		backend := startTriageRawBackend(t, holdoutFixture)
		for _, name := range []string{"search_specific", "search_broad", "candidate"} {
			if sendTriageRoute(t, backend, triageRoute(t, holdoutFixture, name), nil) != http.StatusOK {
				t.Fatalf("route %s failed", name)
			}
		}
		wiki := triageMarkdown(t, filepath.Join(triageRoot(holdout.directory), "workspace", "new-bug.md"))
		payload, _ := json.Marshal(map[string]any{"fields": map[string]any{"project": map[string]string{"key": "OPS"}, "issuetype": map[string]string{"name": "Bug"}, "summary": holdout.newSummary, "description": string(wiki)}})
		before := triageRequestIndex(backend)
		status := sendTriageRaw(t, backend, "POST", "/jira/rest/api/2/issue", nil, payload)
		if status != http.StatusNotFound || triageRequestIndex(backend) != before {
			t.Fatalf("status=%d cursor=%d", status, triageRequestIndex(backend))
		}
	})
	t.Run("altered create body rejected", func(t *testing.T) {
		backend := startTriageRawBackend(t, primaryFixture)
		for _, name := range primary.sequence[:4] {
			if sendTriageRoute(t, backend, triageRoute(t, primaryFixture, name), nil) != http.StatusOK {
				t.Fatalf("route %s failed", name)
			}
		}
		route := triageRoute(t, primaryFixture, "create")
		var document map[string]any
		if err := json.Unmarshal(route.RequestBody, &document); err != nil {
			t.Fatal(err)
		}
		document["fields"].(map[string]any)["description"] = "altered"
		altered, _ := json.Marshal(document)
		before := triageRequestIndex(backend)
		if status := sendTriageRoute(t, backend, route, altered); status != http.StatusNotFound || triageRequestIndex(backend) != before {
			t.Fatalf("status=%d cursor=%d", status, triageRequestIndex(backend))
		}
	})
	t.Run("no second post after ambiguity", func(t *testing.T) {
		backend, _ := executeTriageProductionWorkflow(t, triageRoot(holdout.directory), holdoutFixture, holdout)
		before := triageRequestIndex(backend)
		if status := sendTriageRoute(t, backend, triageRoute(t, holdoutFixture, "comment"), nil); status != http.StatusNotFound || triageRequestIndex(backend) != before {
			t.Fatalf("status=%d cursor=%d", status, triageRequestIndex(backend))
		}
	})
}

func TestRepositoryJiraTriageIssueReconciliationAndCompletenessNegatives(t *testing.T) {
	body := "h2. New occurrence\n\nLeaseRenewalError after lease renewal on synthetic build 23."
	baseline := []domain.Comment{{ID: "800", Body: "Initial synthetic occurrence."}}
	if _, ok := reconcileTriageComment(baseline, baseline, body); ok {
		t.Fatal("missing new-id delta reconciled")
	}
	if _, ok := reconcileTriageComment(append(baseline, domain.Comment{ID: "801", Body: body}), append(baseline, domain.Comment{ID: "801", Body: body}), body); ok {
		t.Fatal("baseline already containing new id reconciled")
	}
	if _, ok := reconcileTriageComment(baseline, append(baseline, domain.Comment{ID: "801", Body: "wrong"}), body); ok {
		t.Fatal("wrong comment body reconciled")
	}

	cohort := triageCohorts[0]
	fixture := loadRepositoryMockFixture(t, filepath.Join(triageRoot(cohort.directory), "fixture.json"))
	var bodyDocument map[string]any
	if err := json.Unmarshal(fixture.Routes[0].Body, &bodyDocument); err != nil {
		t.Fatal(err)
	}
	bodyDocument["total"] = 2
	fixture.Routes[0].Body, _ = json.Marshal(bodyDocument)
	backend := startTriageRawBackend(t, fixture)
	for key, value := range backend.Environment() {
		t.Setenv(key, value)
	}
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	jira, err := app.NewJira(&config.Config{JiraURL: backend.Environment()["ATL_JIRA_URL"]}, "benchmark-contract")
	if err != nil {
		t.Fatal(err)
	}
	list, err := jira.SearchIssueList(context.Background(), cohort.queries[0], strings.Split(triageColumns, ","), 10, "")
	if err != nil {
		t.Fatal(err)
	}
	complete := &app.IssueList{Page: app.IssueListPage{Complete: true}}
	if list.Page.Complete || triageMayWrite([]*app.IssueList{list, complete}, []*domain.Issue{{Key: "LAB-41"}}) {
		t.Fatal("incomplete search admitted a write")
	}
	methods, _, _ := backend.Summary()
	if methods["POST"] != 0 {
		t.Fatalf("incomplete search sent %d writes", methods["POST"])
	}
}

func startTriageRawBackend(t *testing.T, fixture MockFixture) *MockBackend {
	t.Helper()
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	return backend
}

func triageRoute(t *testing.T, fixture MockFixture, name string) MockRoute {
	t.Helper()
	for _, route := range fixture.Routes {
		if route.Name == name {
			return route
		}
	}
	t.Fatalf("route %q missing", name)
	return MockRoute{}
}

func sendTriageRoute(t *testing.T, backend *MockBackend, route MockRoute, override []byte) int {
	t.Helper()
	body := route.RequestBody
	if override != nil {
		body = override
	}
	return sendTriageRaw(t, backend, route.Method, route.Path, route.QueryEquals, body)
}

func sendTriageRaw(t *testing.T, backend *MockBackend, method, path string, query map[string]string, body []byte) int {
	t.Helper()
	request, err := http.NewRequest(method, backend.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	values := request.URL.Query()
	for key, value := range query {
		values.Set(key, value)
	}
	request.URL.RawQuery = values.Encode()
	if len(body) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := backend.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func triageRequestIndex(backend *MockBackend) int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.requestIndex
}

func TestRepositoryJiraTriageIssueSamplingPromptsAndPolicies(t *testing.T) {
	primaryRoot, holdoutRoot := triageRoot(triagePrimaryDirectory), triageRoot(triageHoldoutDirectory)
	primaryScenario := loadRepositoryScenario(t, filepath.Join(primaryRoot, "scenario.v1.json"))
	if primaryScenario.Budgets.MaxATLInvocations != 7 || primaryScenario.Budgets.MaxBackendRequests != 7 ||
		primaryScenario.Budgets.MaxDuplicateBackendRequests != 1 {
		t.Fatalf("primary authority envelope leaks the expected branch: %+v", primaryScenario.Budgets)
	}
	for _, root := range []string{primaryRoot, holdoutRoot} {
		reviewed, err := os.ReadFile(filepath.Join(root, "workspace", "new-bug.md"))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(reviewed))
		if strings.Contains(lower, "threshold") || strings.Contains(lower, "scored") || strings.Contains(lower, "would be the duplicate") {
			t.Fatalf("reviewed create body leaks the expected decision: %s", root)
		}
	}
	primarySchema, err := os.ReadFile(filepath.Join(primaryRoot, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	holdoutSchema, err := os.ReadFile(filepath.Join(holdoutRoot, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(primarySchema, holdoutSchema) {
		t.Fatal("paired response schemas are not byte-identical")
	}
	for _, provider := range []string{"codex", "claude"} {
		primary := loadRepositoryRunSpec(t, filepath.Join(primaryRoot, "run.cli."+provider+".json"))
		holdout := loadRepositoryRunSpec(t, filepath.Join(holdoutRoot, "run.cli."+provider+".json"))
		wantProvider, wantModel := "codex", "gpt-5.6-luna"
		if provider == "claude" {
			wantProvider, wantModel = "claude-code", "claude-opus-4-8"
		}
		if primary.Provider != wantProvider || primary.Model != wantModel || primary.Reasoning != "high" || primary.Repetitions != 3 ||
			holdout.Provider != wantProvider || holdout.Model != wantModel || holdout.Reasoning != "high" || holdout.Repetitions != 1 ||
			primary.Variant != "jira-triage-issue-workflow-v1" || holdout.Variant != primary.Variant || !primary.AllowSyntheticWrites || !holdout.AllowSyntheticWrites {
			t.Fatalf("provider cohort drifted: %s", provider)
		}
		for root, spec := range map[string]RunSpec{primaryRoot: primary, holdoutRoot: holdout} {
			prompt, readErr := os.ReadFile(filepath.Join(root, spec.PromptFile))
			if readErr != nil {
				t.Fatal(readErr)
			}
			assertTriagePromptBoundary(t, prompt, spec.Provider)
			if spec.Provider == "codex" {
				policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands}
				if err := policy.Validate(); err != nil {
					t.Fatal(err)
				}
				assertTriagePolicyAlternativesAndMutations(t, policy, root == holdoutRoot)
			}
		}
		if primary.Provider == "claude-code" {
			checks, evalErr := evaluateRunChecks(primary.Checks, triageFinal(t, triageCohorts[0]), "", 5, 0, 0, 1,
				map[string]int{"atl:jira": 1}, 0, 0, triageCohorts[0].methods, true, triageCohorts[0].exitCodes)
			if evalErr != nil {
				t.Fatal(evalErr)
			}
			if checks["used_skill"] {
				t.Fatal("wrong Claude Skill identity passed")
			}
		}
	}
	if slices.Equal(triageCohorts[0].queries, triageCohorts[1].queries) || triageCohorts[0].createdKey == triageCohorts[1].commentID || triageCohorts[0].project == triageCohorts[1].project {
		t.Fatal("primary and holdout are not disjoint")
	}
}

func assertTriagePromptBoundary(t *testing.T, prompt []byte, provider string) {
	t.Helper()
	text := string(prompt)
	if !strings.Contains(text, "already explicitly approved exactly one conditional synthetic action") || !strings.Contains(text, "threshold is 75") || !strings.Contains(text, "never perform both alternatives") {
		t.Fatal("prompt lost approval, threshold, or mutual exclusion")
	}
	if provider == "claude-code" {
		if strings.Contains(text, "env -u ATL_READ_ONLY") || !strings.Contains(text, "atl:triage-issue") || strings.Count(text, "\natl ") != 7 {
			t.Fatal("Claude prompt command/Skill boundary drifted")
		}
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "atl ") && !strings.HasSuffix(line, " --") {
				t.Fatalf("Claude command lacks trailing --: %q", line)
			}
		}
	} else {
		if !strings.Contains(text, "$triage-issue") || strings.Count(text, "env -u ATL_READ_ONLY atl ") != 2 {
			t.Fatal("Codex prompt command/Skill boundary drifted")
		}
	}
}

func assertTriagePolicyAlternativesAndMutations(t *testing.T, policy CLICommandPolicy, holdout bool) {
	t.Helper()
	names := map[string]int{}
	for _, rule := range policy.Rules {
		names[rule.Name] = rule.MaxInvocations
	}
	if names["create"] != 1 || names["comment"] != 1 || names["comments"] != 2 {
		t.Fatalf("policy alternatives/counts=%v", names)
	}
	project, key, specific, summary := "LAB", "LAB-52", triageCohorts[0].queries[0], triageCohorts[0].newSummary
	if holdout {
		project, key, specific, summary = "OPS", "OPS-88", triageCohorts[1].queries[0], triageCohorts[1].newSummary
	}
	alternatives := [][]string{
		{"jira", "issue", "create", "--project", project, "--type", "Bug", "--summary", summary, "--from-md", "new-bug.md"},
		{"jira", "issue", "comment", "list", key},
		{"jira", "issue", "comment", "add", key, "--from-md", "duplicate-comment.md"},
	}
	for _, argv := range alternatives {
		if !slices.ContainsFunc(policy.Rules, func(rule CLICommandRule) bool { return matchCLICommandRule(rule, argv) }) {
			t.Fatalf("exact branch-neutral alternative denied: %v", argv)
		}
	}
	mutations := [][]string{
		{"jira", "issue", "search", "--jql", specific + " changed", "--limit", "10", "--columns", triageColumns},
		{"jira", "issue", "search", "--jql", specific, "--limit", "11", "--columns", triageColumns},
		{"jira", "issue", "search", "--jql", specific, "--limit", "10", "--columns", "key,summary,status"},
		{"jira", "issue", "get", "WRONG-1"},
		{"jira", "issue", "create", "--project", project, "--type", "Bug", "--summary", summary + " changed", "--from-md", "new-bug.md"},
		{"jira", "issue", "comment", "add", key, "--from-md", "wrong.md"},
		{"jira", "issue", "comment", "add", "WRONG-1", "--from-md", "duplicate-comment.md"},
	}
	for _, argv := range mutations {
		if slices.ContainsFunc(policy.Rules, func(rule CLICommandRule) bool { return matchCLICommandRule(rule, argv) }) {
			t.Fatalf("mutated command admitted: %v", argv)
		}
	}
}

func triageRoot(directory string) string {
	return filepath.Join("..", "..", "benchmarks", "agent-eval", directory)
}
