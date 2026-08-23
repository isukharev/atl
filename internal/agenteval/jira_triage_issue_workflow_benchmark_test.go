package agenteval

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
	candidateFields                                   string
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
		candidateFields: "summary,description,status,issuetype,project",
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
		methods:  map[string]int{"GET": 14, "POST": 1}, exitCodes: []int{0, 0, 0, 0, 0, 0}, duplicates: 6,
	},
	{
		directory: triageHoldoutDirectory, project: "OPS", signature: "LeaseRenewalError", component: "Indexer", trigger: "lease renewal",
		candidateFields: "summary,description,status,issuetype,project,assignee,reporter,labels,issuelinks,comment,attachment",
		queries: []string{
			`project = OPS AND text ~ "LeaseRenewalError retry storm" AND type = Bug ORDER BY updated DESC`,
			`project = OPS AND summary ~ "indexer retry" AND type = Bug ORDER BY updated DESC`,
		},
		queryKeys: [][]string{{"OPS-88"}, {"OPS-88"}},
		candidates: []triageCandidate{
			{key: "OPS-88", status: "Open", signature: true, component: true, trigger: true, open: true, score: 100},
		},
		decision: "comment", targetKey: "OPS-88", commentID: "801", newSummary: "Indexer: retry storm after lease renewal",
		sequence: []string{
			"search_specific", "search_broad", "candidate",
			"current_user", "guarded_issue_key", "comments",
			"current_user", "guarded_issue_key", "comments",
			"current_user", "guarded_issue_id", "comments", "comment", "guarded_issue_id", "comments",
		},
		methods: map[string]int{"GET": 14, "POST": 1}, exitCodes: []int{0, 0, 0, 0, 0}, duplicates: 7,
	},
}

func TestRepositoryJiraTriageIssueFixturesDriveSelectedCLIAndGuardedOracles(t *testing.T) {
	primary := triageCohorts[0]
	t.Run(primary.directory+" selected CLI", func(t *testing.T) {
		root := triageRoot(primary.directory)
		fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
		assertTriageFixtureTopology(t, fixture, primary)
		policy := triageCodexPolicy(t, root)
		evidence := executeJiraTriagePrimaryProcess(t, startJiraTriagePrimaryProcess(t, root, fixture, primary, policy), primary)
		assertTriageProviderOracles(t, root, primary, triageFinal(t, primary), evidence.Summary.HTTPMethods, evidence.Summary.DuplicateRequests)
		assertJiraTriagePrimaryProcessAdmissionRefused(t, root, fixture, primary, policy)
	})

	holdout := triageCohorts[1]
	t.Run(holdout.directory+" guarded oracle", func(t *testing.T) {
		root := triageRoot(holdout.directory)
		fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
		assertTriageFixtureTopology(t, fixture, holdout)
		assertTriageGuardedFixtureRecovery(t, fixture, holdout)
		assertTriageProviderOracles(t, root, holdout, triageFinal(t, holdout), holdout.methods, holdout.duplicates)
	})
}

func assertTriageGuardedFixtureRecovery(t *testing.T, fixture MockFixture, cohort triageCohort) {
	t.Helper()
	comments := triageRoute(t, fixture, "comments")
	if len(comments.Responses) != 4 {
		t.Fatalf("guarded comments responses=%d want=4", len(comments.Responses))
	}
	decode := func(raw json.RawMessage) []triageHistoricalComment {
		var document struct {
			StartAt  int `json:"startAt"`
			Total    int `json:"total"`
			Comments []struct {
				ID   string `json:"id"`
				Body string `json:"body"`
			} `json:"comments"`
		}
		if err := json.Unmarshal(raw, &document); err != nil || document.StartAt != 0 || document.Total != len(document.Comments) {
			t.Fatalf("decode historical comments response: start=%d total=%d comments=%d err=%v", document.StartAt, document.Total, len(document.Comments), err)
		}
		result := make([]triageHistoricalComment, len(document.Comments))
		for index, comment := range document.Comments {
			if comment.ID == "" {
				t.Fatal("guarded comment response contains an empty id")
			}
			result[index] = triageHistoricalComment{id: comment.ID, body: comment.Body}
		}
		return result
	}
	var request struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(triageRoute(t, fixture, "comment").RequestBody, &request); err != nil || request.Body == "" {
		t.Fatalf("decode historical comment request: %v", err)
	}
	commentRoute := triageRoute(t, fixture, "comment")
	if commentRoute.Status != http.StatusCreated || string(commentRoute.Body) != "{}" || commentRoute.Path != fixture.JiraContext+"/rest/api/2/issue/9188/comment" {
		t.Fatalf("guarded malformed acknowledgement route=%+v", commentRoute)
	}
	id, ok := reconcileTriageComment(decode(comments.Responses[2].Body), decode(comments.Responses[3].Body), request.Body)
	if !ok || id != cohort.commentID {
		t.Fatalf("guarded fixture reconciliation id=%q ok=%t want=%q", id, ok, cohort.commentID)
	}
}

func triageIssueListKeys(list JiraSnapshotIssueList) []string {
	keys := make([]string, len(list.Rows))
	for index, row := range list.Rows {
		keys[index] = row.Key
	}
	return keys
}

func scoreTriageIssue(issue JiraTriageIssueGet, cohort triageCohort) triageCandidate {
	value := triageCandidate{key: issue.Key, status: issue.Status}
	value.signature = strings.Contains(issue.Description, cohort.signature)
	value.component = strings.Contains(issue.Summary, cohort.component)
	value.trigger = strings.Contains(issue.Description, cohort.trigger)
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

func triageMayWrite(searches []JiraSnapshotIssueList, issues []JiraTriageIssueGet) bool {
	if len(searches) != 2 || len(issues) == 0 {
		return false
	}
	return searches[0].Page.Complete && searches[1].Page.Complete
}

type triageHistoricalComment struct {
	id, body string
}

func reconcileTriageComment(before, after []triageHistoricalComment, body string) (string, bool) {
	baseline := make(map[string]struct{}, len(before))
	for _, comment := range before {
		baseline[comment.id] = struct{}{}
	}
	var matches []triageHistoricalComment
	for _, comment := range after {
		if _, existed := baseline[comment.id]; !existed && comment.body == body {
			matches = append(matches, comment)
		}
	}
	if len(matches) != 1 {
		return "", false
	}
	return matches[0].id, true
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

func assertTriageProviderOracles(t *testing.T, root string, cohort triageCohort, final []byte, methods map[string]int, duplicates int) {
	t.Helper()
	for _, provider := range []string{"codex", "claude"} {
		spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli."+provider+".json"))
		scenario := loadRepositoryScenario(t, filepath.Join(root, spec.ScenarioFile))
		assertSyntheticJiraCreateHistoricalContract(t, spec, scenario, syntheticJiraCreateHistoricalContract{
			HTTPMethods: methods, MaxBackendRequests: methods[http.MethodGet] + methods[http.MethodPost], MaxDuplicateBackendRequests: duplicates,
		})
		if err := spec.ValidateAgainstScenario(scenario); err != nil {
			t.Fatal(err)
		}
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
			if route.Method != "GET" || !strings.Contains(route.Path, "/issue/") || route.QueryEquals["fields"] != cohort.candidateFields {
				t.Fatalf("candidate route drifted: %+v", route)
			}
		case route.Name == "create":
			if route.Method != "POST" || route.Path != "/jira/rest/api/2/issue" || len(route.RequestBody) == 0 {
				t.Fatalf("create route drifted: %+v", route)
			}
		case route.Name == "comments":
			if len(route.Responses) != 4 || route.Path != "/jira/rest/api/2/issue/9188/comment" || route.QueryEquals["startAt"] != "0" || route.QueryEquals["maxResults"] != "100" {
				t.Fatalf("stateful comments route drifted: %+v", route)
			}
		case route.Name == "comment":
			if route.Method != "POST" || route.Path != "/jira/rest/api/2/issue/9188/comment" || route.Status != http.StatusCreated || string(route.Body) != "{}" || len(route.RequestBody) == 0 {
				t.Fatalf("comment route drifted: %+v", route)
			}
		case route.Name == "current_user":
			if route.Method != "GET" || route.Path != "/jira/rest/api/2/myself" || route.Status != http.StatusOK {
				t.Fatalf("guarded actor route drifted: %+v", route)
			}
		case strings.HasPrefix(route.Name, "guarded_issue_"):
			if route.Method != "GET" || route.QueryEquals["fields"] != "project,updated" || len(route.Responses) != 2 {
				t.Fatalf("guarded issue route drifted: %+v", route)
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
		payload := []byte(`{"body":"wrong branch"}`)
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
		payload := []byte(`{"fields":{}}`)
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
		backend := startTriageRawBackend(t, holdoutFixture)
		for _, name := range holdout.sequence {
			want := http.StatusOK
			if name == "comment" {
				want = http.StatusCreated
			}
			if status := sendTriageRoute(t, backend, triageRoute(t, holdoutFixture, name), nil); status != want {
				t.Fatalf("guarded route %s status=%d want=%d", name, status, want)
			}
		}
		if !backend.RequestSequenceComplete() {
			t.Fatal("guarded request sequence did not complete")
		}
		before := triageRequestIndex(backend)
		if status := sendTriageRoute(t, backend, triageRoute(t, holdoutFixture, "comment"), nil); status != http.StatusNotFound || triageRequestIndex(backend) != before {
			t.Fatalf("status=%d cursor=%d", status, triageRequestIndex(backend))
		}
	})
}

func TestRepositoryJiraTriageIssueReconciliationAndCompletenessNegatives(t *testing.T) {
	body := "h2. New occurrence\n\nLeaseRenewalError after lease renewal on synthetic build 23."
	baseline := []triageHistoricalComment{{id: "800", body: "Initial synthetic occurrence."}}
	if _, ok := reconcileTriageComment(baseline, baseline, body); ok {
		t.Fatal("missing new-id delta reconciled")
	}
	if _, ok := reconcileTriageComment(append(baseline, triageHistoricalComment{id: "801", body: body}), append(baseline, triageHistoricalComment{id: "801", body: body}), body); ok {
		t.Fatal("baseline already containing new id reconciled")
	}
	if _, ok := reconcileTriageComment(baseline, append(baseline, triageHistoricalComment{id: "801", body: "wrong"}), body); ok {
		t.Fatal("wrong comment body reconciled")
	}

	cohort := triageCohorts[0]
	root := triageRoot(cohort.directory)
	fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
	assertJiraTriageIncompleteSearchIsReadOnly(t, fixture, cohort, triageCodexPolicy(t, root))
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
	server := backend.HTTPServer()
	request, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
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
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func triageRequestIndex(backend *MockBackend) int {
	return backend.RequestIndex()
}

func TestRepositoryJiraTriageIssueSamplingPromptsAndPolicies(t *testing.T) {
	primaryRoot, holdoutRoot := triageRoot(triagePrimaryDirectory), triageRoot(triageHoldoutDirectory)
	primaryScenario := loadRepositoryScenario(t, filepath.Join(primaryRoot, "scenario.v1.json"))
	if primaryScenario.Budgets.MaxATLInvocations != 6 || primaryScenario.Budgets.MaxBackendRequests != 15 ||
		primaryScenario.Budgets.MaxDuplicateBackendRequests != 6 {
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
			policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands}
			if err := policy.Validate(); err != nil {
				t.Fatal(err)
			}
			assertTriagePolicyAlternativesAndMutations(t, policy, root == holdoutRoot)
			cohort := triageCohorts[0]
			if root == holdoutRoot {
				cohort = triageCohorts[1]
			}
			assertTriageCandidateCommands(t, prompt, policy, cohort)
		}
		if primary.Provider == "claude-code" {
			checks, evalErr := evaluateRunChecks(primary.Checks, triageFinal(t, triageCohorts[0]), "", len(triageCohorts[0].exitCodes), 0, 0, 1,
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
		if !strings.Contains(text, "atl:triage-issue") {
			t.Fatal("Claude prompt command/Skill boundary drifted")
		}
	} else {
		if !strings.Contains(text, "$triage-issue") {
			t.Fatal("Codex prompt command/Skill boundary drifted")
		}
	}
	if strings.Count(text, "env -u ATL_READ_ONLY atl ") != 2 ||
		strings.Count(text, "--expected-proposal-hash PREVIEW_PROPOSAL_HASH") != 2 {
		t.Fatal("prompt guarded-write boundary drifted")
	}
}

func assertTriageCandidateCommands(t *testing.T, prompt []byte, policy CLICommandPolicy, cohort triageCohort) {
	t.Helper()
	for _, candidate := range cohort.candidates {
		argv := []string{"jira", "issue", "get", candidate.key}
		command := "atl " + strings.Join(argv, " ")
		if cohort.directory == triagePrimaryDirectory {
			argv = append(argv, "--fields", cohort.candidateFields)
			command += " --fields " + cohort.candidateFields
		}
		if !slices.ContainsFunc(policy.Rules, func(rule CLICommandRule) bool { return matchCLICommandRule(rule, argv) }) {
			t.Fatalf("policy does not contain exact candidate command %q", command)
		}
		if !strings.Contains(string(prompt), "\n"+command+"\n") {
			t.Fatalf("Claude prompt does not contain exact candidate command %q", command)
		}
		if cohort.directory == triagePrimaryDirectory {
			broad := "atl jira issue get " + candidate.key
			if slices.ContainsFunc(policy.Rules, func(rule CLICommandRule) bool {
				return matchCLICommandRule(rule, []string{"jira", "issue", "get", candidate.key})
			}) || strings.Contains(string(prompt), "\n"+broad+"\n") {
				t.Fatalf("Claude primary workflow admitted broad candidate command %q", broad)
			}
		}
	}
}

func assertTriagePolicyAlternativesAndMutations(t *testing.T, policy CLICommandPolicy, holdout bool) {
	t.Helper()
	names := map[string]int{}
	for _, rule := range policy.Rules {
		names[rule.Name] = rule.MaxInvocations
	}
	if names["preview_create"] != 1 || names["create"] != 1 || names["preview_comment"] != 1 || names["comment"] != 1 {
		t.Fatalf("policy alternatives/counts=%v", names)
	}
	bindings := map[string][2]int{}
	for _, rule := range policy.Rules {
		counts := bindings[rule.BindsProposalHash]
		if rule.BindsProposalHash != "" {
			counts[0]++
			bindings[rule.BindsProposalHash] = counts
		}
		counts = bindings[rule.RequiresProposalHash]
		if rule.RequiresProposalHash != "" {
			counts[1]++
			bindings[rule.RequiresProposalHash] = counts
		}
	}
	if bindings["create"] != [2]int{1, 1} || bindings["comment"] != [2]int{1, 1} {
		t.Fatalf("proposal bindings=%v", bindings)
	}
	project, key, specific, summary := "LAB", "LAB-52", triageCohorts[0].queries[0], triageCohorts[0].newSummary
	if holdout {
		project, key, specific, summary = "OPS", "OPS-88", triageCohorts[1].queries[0], triageCohorts[1].newSummary
	}
	alternatives := [][]string{
		{"jira", "issue", "create", "preview", "--project", project, "--type", "Bug", "--summary", summary, "--from-md", "new-bug.md"},
		{"jira", "issue", "create", "--project", project, "--type", "Bug", "--summary", summary, "--from-md", "new-bug.md", "--apply", "--expected-proposal-hash", strings.Repeat("a", 64)},
		{"jira", "issue", "comment", "preview", key, "--from-md", "duplicate-comment.md"},
		{"jira", "issue", "comment", "add", key, "--from-md", "duplicate-comment.md", "--apply", "--expected-proposal-hash", strings.Repeat("a", 64)},
	}
	for _, argv := range alternatives {
		if !slices.ContainsFunc(policy.Rules, func(rule CLICommandRule) bool { return matchCLICommandRule(rule, argv) }) {
			t.Fatalf("exact branch-neutral alternative denied: %v", argv)
		}
	}
	wrongCandidate := []string{"jira", "issue", "get", "WRONG-1"}
	if !holdout {
		wrongCandidate = append(wrongCandidate, "--fields", triageCohorts[0].candidateFields)
	}
	mutations := [][]string{
		{"jira", "issue", "search", "--jql", specific + " changed", "--limit", "10", "--columns", triageColumns},
		{"jira", "issue", "search", "--jql", specific, "--limit", "11", "--columns", triageColumns},
		{"jira", "issue", "search", "--jql", specific, "--limit", "10", "--columns", "key,summary,status"},
		wrongCandidate,
		{"jira", "issue", "create", "--project", project, "--type", "Bug", "--summary", summary + " changed", "--from-md", "new-bug.md"},
		{"jira", "issue", "comment", "preview", key, "--from-md", "wrong.md"},
		{"jira", "issue", "comment", "add", key, "--from-md", "duplicate-comment.md"},
		{"jira", "issue", "comment", "add", "WRONG-1", "--from-md", "duplicate-comment.md", "--apply", "--expected-proposal-hash", strings.Repeat("a", 64)},
	}
	if !holdout {
		mutations = append(mutations,
			[]string{"jira", "issue", "get", "LAB-41"},
			[]string{"jira", "issue", "get", "LAB-41", "--fields", "summary,status"},
		)
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

func triageCodexPolicy(t *testing.T, root string) CLICommandPolicy {
	t.Helper()
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli.codex.json"))
	policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	return policy
}
