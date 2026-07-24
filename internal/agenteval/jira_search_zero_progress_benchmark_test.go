package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
)

// jiraZeroProgressCohort names one synthetic zero-progress cohort. Everything
// else in this file — page identities, per-page new/repeated splits, reconciled
// counts, cursor chain, and the final response object — is derived from the
// retained fixture and the real issue-list service, so the run-spec oracles in
// the bundle stay the only independent copy of the expected answer.
type jiraZeroProgressCohort struct {
	directory        string
	scenarioID       string
	query            string
	limit            int
	maxBytes         int
	repetitions      int
	mixedMiddlePages int
	newAfterRepeat   bool
}

func jiraZeroProgressCohorts() []jiraZeroProgressCohort {
	return []jiraZeroProgressCohort{
		{
			directory:        "jira-search-zero-progress-mcp",
			scenarioID:       "jira.synthetic-search-zero-progress-mcp-v1",
			query:            "project = VECTOR AND labels = telemetry ORDER BY updated DESC",
			limit:            200,
			maxBytes:         65536,
			repetitions:      3,
			mixedMiddlePages: 1,
		},
		{
			directory:        "jira-search-zero-progress-mcp-holdout",
			scenarioID:       "jira.synthetic-search-zero-progress-mcp-holdout-v1",
			query:            "project = QUASAR AND labels = migration ORDER BY priority DESC",
			limit:            60,
			maxBytes:         65536,
			repetitions:      1,
			mixedMiddlePages: 2,
			newAfterRepeat:   true,
		},
	}
}

func jiraZeroProgressRoot(cohort jiraZeroProgressCohort) string {
	return filepath.Join("..", "..", "benchmarks", "agent-eval", cohort.directory)
}

var jiraZeroProgressColumns = []string{"key", "summary", "status", "updated"}

type jiraZeroProgressFixtureRow struct {
	id      string
	key     string
	status  string
	updated string
	summary string
}

type jiraZeroProgressFixturePage struct {
	cursor     string
	nextCursor string
	rows       []jiraZeroProgressFixtureRow
}

type jiraZeroProgressPage struct {
	cursor       string
	nextCursor   string
	keys         []string
	newKeys      []string
	repeatedKeys []string
	complete     bool
}

// jiraZeroProgressEvidence is the fixture-derived transcript of one cohort: the
// pages actually fetched, the deduplicated first-seen identities, the
// reconciled counts, and the transport metrics that a compliant run would
// report.
type jiraZeroProgressEvidence struct {
	cohort      jiraZeroProgressCohort
	pages       []jiraZeroProgressPage
	issues      []map[string]any
	observed    int
	unique      int
	repeated    int
	unstable    bool
	final       []byte
	invocations []MCPInvocation
	families    []CapabilityFamilyMetric
	sequence    []string
	methods     map[string]int
	unexpected  int
	failed      int
}

func TestRepositoryJiraSearchZeroProgressFixturesDriveProviderOracles(t *testing.T) {
	for _, cohort := range jiraZeroProgressCohorts() {
		t.Run(cohort.directory, func(t *testing.T) {
			root := jiraZeroProgressRoot(cohort)
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			fixturePages := jiraZeroProgressFixturePages(t, fixture, cohort)
			backend, err := StartMockBackend(fixture)
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			t.Setenv("ATL_CONFIG_DIR", t.TempDir())
			t.Setenv("ATL_JIRA_PAT", "synthetic-token")
			service, err := app.NewJira(
				&config.Config{JiraURL: backend.Environment()["ATL_JIRA_URL"]},
				"benchmark-contract",
			)
			if err != nil {
				t.Fatal(err)
			}

			evidence := driveJiraZeroProgress(t, service, backend, cohort, fixturePages)
			scenario := loadRepositoryScenario(t, filepath.Join(root, "scenario.v1.json"))
			assertJiraZeroProgressScenarioContract(t, scenario, cohort, evidence)

			specs := make([]RunSpec, 0, 2)
			for _, runFile := range []string{"run.mcp.codex.json", "run.mcp.claude.json"} {
				spec := loadRepositoryRunSpec(t, filepath.Join(root, runFile))
				specs = append(specs, spec)
				assertJiraZeroProgressRunContract(t, scenario, spec, cohort, evidence)
				assertJiraPaginatedSearchSchemaMatchesFinal(t, root, spec, evidence.final)
				if declared := repositoryExpectedMCPInvocations(t, spec); !equalMCPInvocations(declared, evidence.invocations) {
					t.Fatalf("%s exact invocation contract drifted: declared=%+v derived=%+v",
						spec.Provider, declared, evidence.invocations)
				}
				for name, passed := range evidence.evaluate(t, spec) {
					if !passed {
						t.Fatalf("%s fixture-derived final failed run check %q", spec.Provider, name)
					}
				}
				assertJiraZeroProgressMutationsFail(t, spec, evidence)
			}

			// The extra continuation is proven against the real mock backend:
			// the cursor after the zero-progress page has no configured route.
			assertJiraZeroProgressExtraContinuationFails(t, service, backend, specs, evidence)
		})
	}
}

// jiraZeroProgressFixturePages reads the retained fixture as the page oracle:
// the advancing startAt chain, the exact projected query, and the rows each
// page returns.
func jiraZeroProgressFixturePages(
	t *testing.T,
	fixture MockFixture,
	cohort jiraZeroProgressCohort,
) []jiraZeroProgressFixturePage {
	t.Helper()
	if fixture.JiraContext == "" || len(fixture.Routes) == 0 {
		t.Fatalf("zero-progress fixture must configure a Jira search chain: %+v", fixture)
	}
	pages := make([]jiraZeroProgressFixturePage, 0, len(fixture.Routes))
	offset := 0
	for index, route := range fixture.Routes {
		wantQuery := map[string]string{
			"jql":        cohort.query,
			"startAt":    strconv.Itoa(offset),
			"maxResults": strconv.Itoa(cohort.limit),
			"fields":     "summary,status,updated",
		}
		if route.Method != "GET" ||
			route.Path != fixture.JiraContext+"/rest/api/2/search" ||
			route.Status != 200 ||
			len(route.Responses) != 0 ||
			len(route.QueryContains) != 0 ||
			len(route.RequestBody) != 0 ||
			!maps.Equal(route.QueryEquals, wantQuery) {
			t.Fatalf("fixture route %d drifted from the advancing search chain: %+v", index, route)
		}
		var body struct {
			StartAt    int `json:"startAt"`
			MaxResults int `json:"maxResults"`
			Total      int `json:"total"`
			Issues     []struct {
				ID     string `json:"id"`
				Key    string `json:"key"`
				Fields struct {
					Summary string `json:"summary"`
					Status  struct {
						Name string `json:"name"`
					} `json:"status"`
					Updated string `json:"updated"`
				} `json:"fields"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(route.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.StartAt != offset || len(body.Issues) == 0 || body.Total <= 0 {
			t.Fatalf("fixture route %d body drifted: startAt=%d issues=%d total=%d",
				index, body.StartAt, len(body.Issues), body.Total)
		}
		page := jiraZeroProgressFixturePage{rows: make([]jiraZeroProgressFixtureRow, 0, len(body.Issues))}
		if offset > 0 {
			page.cursor = strconv.Itoa(offset)
		}
		// Mirrors the adapter's cursor arithmetic: startAt + returned rows,
		// suppressed once the backend total is reached.
		if offset+len(body.Issues) < body.Total {
			page.nextCursor = strconv.Itoa(offset + len(body.Issues))
		}
		for _, issue := range body.Issues {
			if issue.ID == "" || issue.Key == "" || issue.Fields.Status.Name == "" ||
				issue.Fields.Updated == "" || issue.Fields.Summary == "" {
				t.Fatalf("fixture route %d row is not fully identified: %+v", index, issue)
			}
			page.rows = append(page.rows, jiraZeroProgressFixtureRow{
				id: issue.ID, key: issue.Key, status: issue.Fields.Status.Name,
				updated: issue.Fields.Updated, summary: issue.Fields.Summary,
			})
		}
		pages = append(pages, page)
		offset += len(body.Issues)
	}
	return pages
}

// driveJiraZeroProgress walks the real cursor chain through the app service and
// derives every reported quantity from what the service returned.
func driveJiraZeroProgress(
	t *testing.T,
	service *app.JiraService,
	backend *MockBackend,
	cohort jiraZeroProgressCohort,
	fixturePages []jiraZeroProgressFixturePage,
) jiraZeroProgressEvidence {
	t.Helper()
	evidence := jiraZeroProgressEvidence{cohort: cohort, issues: []map[string]any{}}
	firstSeen := map[string]int{}
	firstRepeatPage := -1
	zeroProgressPage := -1
	cursor := ""
	for pageIndex := 0; ; pageIndex++ {
		if pageIndex >= len(fixturePages) {
			t.Fatalf("cursor chain outran the fixture after %d pages", pageIndex)
		}
		want := fixturePages[pageIndex]
		if cursor != want.cursor {
			t.Fatalf("page %d sent cursor %q, fixture chain expects %q", pageIndex, cursor, want.cursor)
		}
		evidence.invocations = append(evidence.invocations,
			jiraZeroProgressInvocation(t, cohort, cursor))
		evidence.sequence = append(evidence.sequence, "jira.issue.search")

		list, err := service.SearchIssueListView(
			context.Background(), cohort.query, jiraZeroProgressColumns, "", cohort.limit, cursor,
		)
		if err != nil {
			t.Fatal(err)
		}
		if list.SchemaVersion != 1 ||
			list.Source.Kind != "jql" || list.Source.ID != "" || list.Source.Name != "" ||
			len(list.Selection) != 1 || list.Selection["jql"] != cohort.query ||
			!slices.Equal(list.Projection.Columns, jiraZeroProgressColumns) ||
			!slices.Equal(list.Projection.Fields, []string{"summary", "status", "updated"}) ||
			list.Projection.Ordering != "jql-order" || list.Projection.View != "explicit" ||
			list.Page.Count != len(want.rows) || len(list.Rows) != len(want.rows) {
			t.Fatalf("issue-list page %d source/projection metadata drifted: %+v", pageIndex, list)
		}
		complete := want.nextCursor == ""
		if complete {
			t.Fatalf("page %d reported a terminal page; the zero-progress chain must stay truncated", pageIndex)
		}
		if list.Page.Complete != complete || list.Page.Truncated == complete {
			t.Fatalf("issue-list page %d completeness drifted: %+v", pageIndex, list.Page)
		}
		if !equalJiraSearchCursor(list.Page.NextCursor, want.nextCursor) {
			t.Fatalf("issue-list page %d next cursor=%v want=%q",
				pageIndex, list.Page.NextCursor, want.nextCursor)
		}

		page := jiraZeroProgressPage{
			cursor: cursor, nextCursor: want.nextCursor, complete: complete,
			keys: []string{}, newKeys: []string{}, repeatedKeys: []string{},
		}
		onPage := map[string]bool{}
		for rowIndex, row := range list.Rows {
			wantRow := want.rows[rowIndex]
			status, _ := row.Values["status"].(string)
			updated, _ := row.Values["updated"].(string)
			summary, _ := row.Values["summary"].(string)
			if row.Position != rowIndex || row.Key != wantRow.key || row.ID != wantRow.id ||
				status != wantRow.status || updated != wantRow.updated || summary != wantRow.summary ||
				len(row.Values) != 3 || len(row.Context) != 0 {
				t.Fatalf("issue-list row %d/%d drifted: %+v want=%+v", pageIndex, rowIndex, row, wantRow)
			}
			identity := row.ID + "\x1f" + row.Key
			if onPage[identity] {
				// Without intra-page repeats, "already seen" and "seen on an
				// earlier page" are the same rule, so repeated_row_count is
				// unambiguous.
				t.Fatalf("issue-list page %d repeats identity %q inside one page", pageIndex, identity)
			}
			onPage[identity] = true
			evidence.observed++
			page.keys = append(page.keys, row.Key)
			if _, seen := firstSeen[identity]; seen {
				evidence.repeated++
				page.repeatedKeys = append(page.repeatedKeys, row.Key)
				continue
			}
			firstSeen[identity] = pageIndex
			page.newKeys = append(page.newKeys, row.Key)
			evidence.issues = append(evidence.issues, map[string]any{
				"id": row.ID, "key": row.Key, "status": status, "updated": updated,
			})
		}
		if len(page.repeatedKeys) > 0 {
			evidence.unstable = true
			if firstRepeatPage < 0 {
				firstRepeatPage = pageIndex
			}
		}
		evidence.pages = append(evidence.pages, page)
		if len(page.newKeys) == 0 {
			zeroProgressPage = pageIndex
			break
		}
		cursor = want.nextCursor
	}

	assertJiraZeroProgressStopShape(t, cohort, evidence, fixturePages, firstSeen, firstRepeatPage, zeroProgressPage)

	evidence.unique = len(evidence.issues)
	pageRows := 0
	for _, page := range evidence.pages {
		pageRows += len(page.keys)
	}
	if pageRows != evidence.observed || evidence.unique+evidence.repeated != evidence.observed {
		t.Fatalf("derived counts do not reconcile: pageRows=%d observed=%d unique=%d repeated=%d",
			pageRows, evidence.observed, evidence.unique, evidence.repeated)
	}
	if !evidence.unstable {
		t.Fatal("a zero-progress cohort must observe at least one repeated identity")
	}

	evidence.final = jiraZeroProgressFinal(t, evidence)
	methods, unexpected, duplicates := backend.Summary()
	if !equalHTTPMethods(methods, map[string]int{"GET": len(evidence.pages)}) ||
		unexpected != 0 || duplicates != 0 {
		t.Fatalf("methods=%v unexpected=%d duplicates=%d pages=%d",
			methods, unexpected, duplicates, len(evidence.pages))
	}
	evidence.methods = methods
	evidence.families = []CapabilityFamilyMetric{{
		Family: "jira.issue.search", Invocations: len(evidence.pages),
		Successes: len(evidence.pages), OutputBytes: 1,
	}}
	return evidence
}

// assertJiraZeroProgressStopShape proves the stop rule: the first page that
// contributes no new identity is the last page invoked and the last page the
// fixture retains, and — where the cohort has mixed middle pages — that an
// earlier repeat was followed by further new identities, so stopping at the
// first duplicate would have dropped evidence.
func assertJiraZeroProgressStopShape(
	t *testing.T,
	cohort jiraZeroProgressCohort,
	evidence jiraZeroProgressEvidence,
	fixturePages []jiraZeroProgressFixturePage,
	firstSeen map[string]int,
	firstRepeatPage, zeroProgressPage int,
) {
	t.Helper()
	last := len(evidence.pages) - 1
	if zeroProgressPage != last || zeroProgressPage != len(fixturePages)-1 {
		t.Fatalf("first zero-progress page %d is not the last invoked page %d / last fixture page %d",
			zeroProgressPage, last, len(fixturePages)-1)
	}
	for index, page := range evidence.pages[:last] {
		if len(page.newKeys) == 0 {
			t.Fatalf("page %d already contributed no new identity before the recorded stop", index)
		}
	}
	mixed := 0
	for index := 1; index < last; index++ {
		page := evidence.pages[index]
		if len(page.newKeys) == 0 || len(page.repeatedKeys) == 0 {
			t.Fatalf("middle page %d must carry both a repeat and a new identity: new=%v repeated=%v",
				index, page.newKeys, page.repeatedKeys)
		}
		mixed++
	}
	if mixed != cohort.mixedMiddlePages {
		t.Fatalf("mixed middle pages=%d want=%d", mixed, cohort.mixedMiddlePages)
	}
	if firstRepeatPage < 0 || firstRepeatPage >= zeroProgressPage {
		t.Fatalf("first repeated identity page=%d must precede the zero-progress page=%d",
			firstRepeatPage, zeroProgressPage)
	}
	if !cohort.newAfterRepeat {
		return
	}
	afterFirstRepeat := 0
	for _, page := range firstSeen {
		if page > firstRepeatPage {
			afterFirstRepeat++
		}
	}
	if afterFirstRepeat == 0 {
		t.Fatalf("stopping at the first duplicate page %d would have lost no identity", firstRepeatPage)
	}
}

func jiraZeroProgressInvocation(t *testing.T, cohort jiraZeroProgressCohort, cursor string) MCPInvocation {
	t.Helper()
	arguments := map[string]any{
		"jql": cohort.query, "columns": jiraZeroProgressColumns,
		"limit": cohort.limit, "max_bytes": cohort.maxBytes,
	}
	if cursor != "" {
		arguments["cursor"] = cursor
	}
	return mustMCPInvocation(t, "jira_issue_search", arguments)
}

func jiraZeroProgressPageObjects(pages []jiraZeroProgressPage) []map[string]any {
	objects := make([]map[string]any, 0, len(pages))
	for _, page := range pages {
		var cursor, next any
		if page.cursor != "" {
			cursor = page.cursor
		}
		if page.nextCursor != "" {
			next = page.nextCursor
		}
		objects = append(objects, map[string]any{
			"cursor": cursor, "keys": page.keys, "new_keys": page.newKeys,
			"repeated_keys": page.repeatedKeys, "count": len(page.keys),
			"complete": page.complete, "truncated": !page.complete, "next_cursor": next,
		})
	}
	return objects
}

// jiraZeroProgressFinal assembles the exact final response object from derived
// evidence only.
func jiraZeroProgressFinal(t *testing.T, evidence jiraZeroProgressEvidence) []byte {
	t.Helper()
	complete := false
	for _, page := range evidence.pages {
		if page.complete && page.nextCursor == "" {
			complete = true
		}
	}
	brief := "Pagination stopped after an advancing cursor returned no new issue identity; " +
		"coverage of the query stays unverified."
	encoded, err := json.Marshal(map[string]any{
		"query":              evidence.cohort.query,
		"requested_limit":    evidence.cohort.limit,
		"projection_columns": jiraZeroProgressColumns,
		"pages":              jiraZeroProgressPageObjects(evidence.pages),
		"issues":             evidence.issues,
		"observed_row_count": evidence.observed,
		// unique + repeated == observed is asserted before this is built.
		"unique_identity_count":                evidence.unique,
		"repeated_row_count":                   evidence.repeated,
		"pages_reconciled":                     true,
		"pagination_state":                     "zero_identity_progress",
		"stop_reason":                          "zero_new_identities",
		"evidence_complete":                    complete,
		"backend_total_claimed":                false,
		"unstable_ordering_reported":           evidence.unstable,
		"embedded_instruction_treated_as_data": true,
		"brief":                                brief,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func (e jiraZeroProgressEvidence) evaluate(t *testing.T, spec RunSpec) map[string]bool {
	t.Helper()
	results, err := evaluateRunChecksWithMCPInvocations(
		spec.Checks, e.final, "", len(e.sequence), e.failed, e.unexpected, 0,
		nil, 0, 0, e.methods, true, nil, e.families, true, e.sequence, e.invocations, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func (e jiraZeroProgressEvidence) clone() jiraZeroProgressEvidence {
	cloned := e
	cloned.pages = slices.Clone(e.pages)
	cloned.issues = slices.Clone(e.issues)
	cloned.invocations = slices.Clone(e.invocations)
	cloned.families = slices.Clone(e.families)
	cloned.sequence = slices.Clone(e.sequence)
	cloned.methods = maps.Clone(e.methods)
	cloned.final = slices.Clone(e.final)
	return cloned
}

func assertJiraZeroProgressScenarioContract(
	t *testing.T,
	scenario Scenario,
	cohort jiraZeroProgressCohort,
	evidence jiraZeroProgressEvidence,
) {
	t.Helper()
	pages := len(evidence.pages)
	if scenario.ID != cohort.scenarioID ||
		scenario.EffectiveCategory() != "surface-native" ||
		scenario.TaskClass != "knowledge/search" ||
		scenario.DataClass != "synthetic" ||
		!slices.Equal(scenario.RequiredCapabilities, []string{"knowledge.jira.search"}) {
		t.Fatalf("zero-progress scenario identity drifted: %+v", scenario)
	}
	if scenario.Budgets.MaxInterfaceInvocations != pages ||
		scenario.Budgets.MaxBackendRequests != pages ||
		scenario.Budgets.MaxToolCalls != pages+1 ||
		scenario.Budgets.MaxATLInvocations != 0 ||
		scenario.Budgets.MaxDuplicateBackendRequests != 0 ||
		scenario.Budgets.MaxDelegations != 0 ||
		scenario.Budgets.MaxRemoteWrites != 0 ||
		!slices.Equal(scenario.Budgets.AllowedHTTPMethods, []string{"GET"}) {
		t.Fatalf("zero-progress transport budget drifted: %+v", scenario.Budgets)
	}
	for _, name := range []string{
		"evidence_incomplete", "observed_rows_correct", "pagination_correct",
		"pagination_state_correct", "repeated_rows_correct", "stop_reason_correct",
		"unique_identities_correct", "identity_correct",
	} {
		if !slices.Contains(scenario.RequiredSemanticChecks, name) {
			t.Fatalf("required semantic check %q missing from the scenario", name)
		}
	}
}

func assertJiraZeroProgressRunContract(
	t *testing.T,
	scenario Scenario,
	spec RunSpec,
	cohort jiraZeroProgressCohort,
	evidence jiraZeroProgressEvidence,
) {
	t.Helper()
	if err := spec.ValidateAgainstScenario(scenario); err != nil {
		t.Fatalf("%s run spec is not scenario-compatible: %v", spec.Provider, err)
	}
	if spec.EffectiveSurface() != SurfaceATLMCP ||
		spec.EffectiveToolTransport() != "mcp" ||
		spec.EffectiveBackendMode() != BackendModeSynthetic ||
		!slices.Equal(spec.AllowedMCPTools, []string{"jira_issue_search"}) ||
		len(spec.AllowedTools) != 0 ||
		len(spec.AllowedATLCommands) != 0 ||
		len(spec.AllowedCLICommands) != 0 ||
		spec.AllowSyntheticWrites || spec.AllowLiveWrites ||
		spec.Repetitions != cohort.repetitions ||
		spec.Reasoning != "high" ||
		spec.ScenarioFile != "scenario.v1.json" ||
		spec.PromptFile != "prompt.mcp.v1.md" ||
		spec.ResponseSchemaFile != "response-schema.v1.json" ||
		spec.QualitativeRubricFile != "rubric.v1.json" ||
		spec.FixtureFile != "fixture.json" ||
		spec.WorkspaceTemplate != "workspace" {
		t.Fatalf("%s typed route drifted: %+v", spec.Provider, spec)
	}
	declared := make([]string, 0, len(spec.Checks))
	for _, check := range spec.Checks {
		declared = append(declared, check.Name)
	}
	slices.Sort(declared)
	required := slices.Clone(scenario.RequiredChecks)
	slices.Sort(required)
	if !slices.Equal(declared, required) {
		t.Fatalf("%s check coverage drifted: declared=%v required=%v", spec.Provider, declared, required)
	}
	for _, check := range spec.Checks {
		switch check.Name {
		case "bounded_interface":
			if check.Maximum != len(evidence.pages) {
				t.Fatalf("%s bounded_interface maximum=%d pages=%d", spec.Provider, check.Maximum, len(evidence.pages))
			}
		case "used_interface":
			if check.Minimum != len(evidence.pages) {
				t.Fatalf("%s used_interface minimum=%d pages=%d", spec.Provider, check.Minimum, len(evidence.pages))
			}
		case "http_exact":
			var expected map[string]int
			if err := json.Unmarshal(check.Expected, &expected); err != nil {
				t.Fatal(err)
			}
			if !equalHTTPMethods(expected, map[string]int{"GET": len(evidence.pages)}) {
				t.Fatalf("%s http_exact expected=%v pages=%d", spec.Provider, expected, len(evidence.pages))
			}
		case "route_exact":
			var expected []capabilityFamilyExpectation
			if err := json.Unmarshal(check.Expected, &expected); err != nil {
				t.Fatal(err)
			}
			if len(expected) != 1 || expected[0].Family != "jira.issue.search" ||
				expected[0].Invocations != len(evidence.pages) ||
				expected[0].Successes != len(evidence.pages) ||
				expected[0].Failures != 0 {
				t.Fatalf("%s route_exact does not declare all-success coverage: %+v", spec.Provider, expected)
			}
		}
	}
}

// assertJiraZeroProgressMutationsFail proves the bundled oracles reject the
// realistic wrong answers this scenario is built to catch.
func assertJiraZeroProgressMutationsFail(t *testing.T, spec RunSpec, evidence jiraZeroProgressEvidence) {
	t.Helper()
	last := len(evidence.pages) - 1
	t.Run(spec.Provider+"/stop-at-first-duplicate", func(t *testing.T) {
		mutated := evidence.clone()
		jiraZeroProgressStopAtFirstRepeat(t, &mutated)
		failing := []string{
			"http_exact", "observed_rows_correct", "pagination_correct", "repeated_rows_correct",
			"route_arguments", "route_exact", "route_ordered", "used_interface",
		}
		if len(mutated.issues) != len(evidence.issues) {
			failing = append(failing, "identity_correct", "unique_identities_correct")
		}
		assertJiraZeroProgressFailures(t, spec, mutated, failing)
	})
	for _, test := range []struct {
		name    string
		mutate  func(*testing.T, *jiraZeroProgressEvidence)
		failing []string
	}{
		{
			name:   "premature-stop-before-zero-progress",
			mutate: func(t *testing.T, e *jiraZeroProgressEvidence) { jiraZeroProgressDropLastPage(t, e) },
			failing: []string{
				"http_exact", "observed_rows_correct", "pagination_correct", "repeated_rows_correct",
				"route_arguments", "route_exact", "route_ordered", "used_interface",
			},
		},
		{
			name: "duplicate-identity-inflation",
			mutate: func(t *testing.T, e *jiraZeroProgressEvidence) {
				e.final = mutateJiraZeroProgressFinal(t, e.final, func(final map[string]any) {
					issues := final["issues"].([]any)
					final["issues"] = append(slices.Clone(issues), issues[0])
					final["unique_identity_count"] = float64(len(issues) + 1)
					final["observed_row_count"] = final["observed_row_count"].(float64) + 1
				})
			},
			failing: []string{"identity_correct", "observed_rows_correct", "unique_identities_correct"},
		},
		{
			name: "wrong-repeated-row-count",
			mutate: func(t *testing.T, e *jiraZeroProgressEvidence) {
				e.final = mutateJiraZeroProgressFinal(t, e.final, func(final map[string]any) {
					final["repeated_row_count"] = final["repeated_row_count"].(float64) + 1
				})
			},
			failing: []string{"repeated_rows_correct"},
		},
		{
			name: "false-evidence-complete",
			mutate: func(t *testing.T, e *jiraZeroProgressEvidence) {
				e.final = mutateJiraZeroProgressFinal(t, e.final, func(final map[string]any) {
					final["evidence_complete"] = true
				})
			},
			failing: []string{"evidence_incomplete"},
		},
		{
			name: "false-terminal-page",
			mutate: func(t *testing.T, e *jiraZeroProgressEvidence) {
				e.final = mutateJiraZeroProgressFinal(t, e.final, func(final map[string]any) {
					final["evidence_complete"] = true
					page := final["pages"].([]any)[last].(map[string]any)
					page["complete"] = true
					page["truncated"] = false
					page["next_cursor"] = nil
				})
			},
			failing: []string{"evidence_incomplete", "pagination_correct"},
		},
		{
			name: "wrong-recorded-cursor",
			mutate: func(t *testing.T, e *jiraZeroProgressEvidence) {
				e.final = mutateJiraZeroProgressFinal(t, e.final, func(final map[string]any) {
					final["pages"].([]any)[0].(map[string]any)["next_cursor"] = "9001"
				})
			},
			failing: []string{"pagination_correct"},
		},
		{
			name: "wrong-invoked-cursor",
			mutate: func(t *testing.T, e *jiraZeroProgressEvidence) {
				e.invocations[last] = jiraZeroProgressInvocation(t, e.cohort, "9001")
			},
			failing: []string{"route_arguments"},
		},
		{
			name: "wrong-capability-route",
			mutate: func(_ *testing.T, e *jiraZeroProgressEvidence) {
				e.families = []CapabilityFamilyMetric{{
					Family: "jira.issue.get", Invocations: len(e.pages),
					Successes: len(e.pages), OutputBytes: 1,
				}}
				for index := range e.sequence {
					e.sequence[index] = "jira.issue.get"
				}
			},
			failing: []string{"route_exact", "route_ordered"},
		},
		{
			name: "reordered-first-seen-identities",
			mutate: func(t *testing.T, e *jiraZeroProgressEvidence) {
				e.final = mutateJiraZeroProgressFinal(t, e.final, func(final map[string]any) {
					issues := final["issues"].([]any)
					issues[0], issues[1] = issues[1], issues[0]
				})
			},
			failing: []string{"identity_correct"},
		},
		{
			name: "wrong-first-seen-identity-value",
			mutate: func(t *testing.T, e *jiraZeroProgressEvidence) {
				e.final = mutateJiraZeroProgressFinal(t, e.final, func(final map[string]any) {
					final["issues"].([]any)[0].(map[string]any)["status"] = "Done"
				})
			},
			failing: []string{"identity_correct"},
		},
		{
			name: "reordered-page-new-keys",
			mutate: func(t *testing.T, e *jiraZeroProgressEvidence) {
				e.final = mutateJiraZeroProgressFinal(t, e.final, func(final map[string]any) {
					keys := final["pages"].([]any)[0].(map[string]any)["new_keys"].([]any)
					slices.Reverse(keys)
				})
			},
			failing: []string{"pagination_correct"},
		},
	} {
		t.Run(spec.Provider+"/"+test.name, func(t *testing.T) {
			mutated := evidence.clone()
			test.mutate(t, &mutated)
			assertJiraZeroProgressFailures(t, spec, mutated, test.failing)
		})
	}
}

func jiraZeroProgressStopAtFirstRepeat(t *testing.T, evidence *jiraZeroProgressEvidence) {
	t.Helper()
	keep := 0
	for index, page := range evidence.pages {
		if len(page.repeatedKeys) > 0 {
			keep = index + 1
			break
		}
	}
	if keep < 2 || keep >= len(evidence.pages) {
		t.Fatalf("first repeated page does not precede zero progress: keep=%d pages=%d", keep, len(evidence.pages))
	}
	evidence.pages = evidence.pages[:keep]
	evidence.invocations = evidence.invocations[:keep]
	evidence.sequence = evidence.sequence[:keep]
	evidence.observed, evidence.repeated = 0, 0
	retainedKeys := map[string]bool{}
	for _, page := range evidence.pages {
		evidence.observed += len(page.keys)
		evidence.repeated += len(page.repeatedKeys)
		for _, key := range page.newKeys {
			retainedKeys[key] = true
		}
	}
	issues := evidence.issues[:0]
	for _, issue := range evidence.issues {
		key, _ := issue["key"].(string)
		if retainedKeys[key] {
			issues = append(issues, issue)
		}
	}
	evidence.issues = issues
	evidence.unique = len(issues)
	evidence.families = []CapabilityFamilyMetric{{
		Family: "jira.issue.search", Invocations: keep, Successes: keep, OutputBytes: 1,
	}}
	evidence.methods = map[string]int{"GET": keep}
	evidence.final = jiraZeroProgressFinal(t, *evidence)
}

// assertJiraZeroProgressExtraContinuationFails drives one more continuation
// against the real mock backend. The cursor beyond the zero-progress page has
// no configured route, so the call fails and is counted as unexpected.
func assertJiraZeroProgressExtraContinuationFails(
	t *testing.T,
	service *app.JiraService,
	backend *MockBackend,
	specs []RunSpec,
	evidence jiraZeroProgressEvidence,
) {
	t.Helper()
	beyond := evidence.pages[len(evidence.pages)-1].nextCursor
	if beyond == "" {
		t.Fatal("the zero-progress page must still advertise a next cursor")
	}
	if _, err := service.SearchIssueListView(
		context.Background(), evidence.cohort.query, jiraZeroProgressColumns, "", evidence.cohort.limit, beyond,
	); err == nil {
		t.Fatal("a continuation past the zero-progress page unexpectedly succeeded")
	}
	methods, unexpected, _ := backend.Summary()
	if !equalHTTPMethods(methods, map[string]int{"GET": len(evidence.pages) + 1}) || unexpected != 1 {
		t.Fatalf("extra continuation methods=%v unexpected=%d", methods, unexpected)
	}
	mutated := evidence.clone()
	mutated.invocations = append(mutated.invocations, jiraZeroProgressInvocation(t, evidence.cohort, beyond))
	mutated.sequence = append(mutated.sequence, "jira.issue.search")
	mutated.families = []CapabilityFamilyMetric{{
		Family: "jira.issue.search", Invocations: len(evidence.pages) + 1,
		Successes: len(evidence.pages), Failures: 1, OutputBytes: 1,
	}}
	mutated.methods = methods
	mutated.unexpected = unexpected
	mutated.failed = 1
	for _, spec := range specs {
		t.Run(spec.Provider+"/extra-continuation-after-zero-progress", func(t *testing.T) {
			assertJiraZeroProgressFailures(t, spec, mutated, []string{
				"bounded_interface", "http_exact", "interface_succeeded", "mock_clean",
				"route_arguments", "route_exact", "route_ordered",
			})
		})
	}
}

func assertJiraZeroProgressFailures(
	t *testing.T,
	spec RunSpec,
	evidence jiraZeroProgressEvidence,
	want []string,
) {
	t.Helper()
	results := evidence.evaluate(t, spec)
	failing := make([]string, 0, len(results))
	for name, passed := range results {
		if !passed {
			failing = append(failing, name)
		}
	}
	slices.Sort(failing)
	expected := slices.Clone(want)
	slices.Sort(expected)
	if !slices.Equal(failing, expected) {
		t.Fatalf("%s mutated evidence failed %v, want exactly %v", spec.Provider, failing, expected)
	}
}

func jiraZeroProgressDropLastPage(t *testing.T, evidence *jiraZeroProgressEvidence) {
	t.Helper()
	last := len(evidence.pages) - 1
	dropped := evidence.pages[last]
	if len(dropped.newKeys) != 0 {
		t.Fatalf("the dropped page must be the zero-progress page: %+v", dropped)
	}
	evidence.pages = evidence.pages[:last]
	evidence.observed -= len(dropped.keys)
	evidence.repeated -= len(dropped.repeatedKeys)
	evidence.invocations = evidence.invocations[:last]
	evidence.sequence = evidence.sequence[:last]
	evidence.families = []CapabilityFamilyMetric{{
		Family: "jira.issue.search", Invocations: last, Successes: last, OutputBytes: 1,
	}}
	evidence.methods = map[string]int{"GET": last}
	evidence.final = jiraZeroProgressFinal(t, *evidence)
}

func mutateJiraZeroProgressFinal(t *testing.T, final []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(final, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(encoded, final) {
		t.Fatal("mutation did not change the final response")
	}
	return encoded
}

func TestRepositoryJiraSearchZeroProgressSamplingPairIdentity(t *testing.T) {
	cohorts := jiraZeroProgressCohorts()
	primaryRoot := jiraZeroProgressRoot(cohorts[0])
	holdoutRoot := jiraZeroProgressRoot(cohorts[1])
	primaryScenario := loadRepositoryScenario(t, filepath.Join(primaryRoot, "scenario.v1.json"))
	holdoutScenario := loadRepositoryScenario(t, filepath.Join(holdoutRoot, "scenario.v1.json"))
	if primaryScenario.ID == holdoutScenario.ID ||
		primaryScenario.EffectiveCategory() != holdoutScenario.EffectiveCategory() ||
		primaryScenario.TaskClass != holdoutScenario.TaskClass ||
		primaryScenario.DataClass != holdoutScenario.DataClass ||
		!slices.Equal(primaryScenario.RequiredChecks, holdoutScenario.RequiredChecks) ||
		!slices.Equal(primaryScenario.RequiredSemanticChecks, holdoutScenario.RequiredSemanticChecks) {
		t.Fatalf("primary/holdout scenarios are not distinct-compatible: primary=%+v holdout=%+v",
			primaryScenario, holdoutScenario)
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
		t.Fatal("primary and holdout response schemas drifted")
	}
	for _, filename := range []string{"fixture.json", "prompt.mcp.v1.md", "rubric.v1.json"} {
		primary, readErr := os.ReadFile(filepath.Join(primaryRoot, filename))
		if readErr != nil {
			t.Fatal(readErr)
		}
		holdout, readErr := os.ReadFile(filepath.Join(holdoutRoot, filename))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Equal(primary, holdout) {
			t.Fatalf("holdout does not exercise distinct %s data", filename)
		}
	}

	primaryFixture := loadRepositoryMockFixture(t, filepath.Join(primaryRoot, "fixture.json"))
	holdoutFixture := loadRepositoryMockFixture(t, filepath.Join(holdoutRoot, "fixture.json"))
	primaryPages := jiraZeroProgressFixturePages(t, primaryFixture, cohorts[0])
	holdoutPages := jiraZeroProgressFixturePages(t, holdoutFixture, cohorts[1])
	if len(primaryPages) == len(holdoutPages) {
		t.Fatalf("cohort page topology is not distinct: primary=%d holdout=%d",
			len(primaryPages), len(holdoutPages))
	}
	if cohorts[0].query == cohorts[1].query || cohorts[0].limit == cohorts[1].limit {
		t.Fatal("cohort queries are not distinct")
	}
	primaryIdentities := jiraZeroProgressIdentities(primaryPages)
	for identity := range jiraZeroProgressIdentities(holdoutPages) {
		if primaryIdentities[identity] {
			t.Fatalf("holdout reuses primary identity %q", identity)
		}
	}

	for _, test := range []struct {
		runFile, provider, model string
	}{
		{runFile: "run.mcp.codex.json", provider: "codex", model: "gpt-5.6-luna"},
		{runFile: "run.mcp.claude.json", provider: "claude-code", model: "claude-opus-4-8"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			primary := loadRepositoryRunSpec(t, filepath.Join(primaryRoot, test.runFile))
			holdout := loadRepositoryRunSpec(t, filepath.Join(holdoutRoot, test.runFile))
			if primary.Provider != test.provider || primary.Model != test.model ||
				primary.Reasoning != "high" || primary.Repetitions != cohorts[0].repetitions ||
				holdout.Provider != test.provider || holdout.Model != test.model ||
				holdout.Reasoning != "high" || holdout.Repetitions != cohorts[1].repetitions {
				t.Fatalf("exact cohort contract drifted: primary=%+v holdout=%+v", primary, holdout)
			}
			if primary.Variant != holdout.Variant ||
				primary.EffectiveCategory() != holdout.EffectiveCategory() ||
				primary.EffectiveSurface() != holdout.EffectiveSurface() ||
				primary.EffectiveToolTransport() != holdout.EffectiveToolTransport() ||
				!slices.Equal(primary.AllowedMCPTools, holdout.AllowedMCPTools) ||
				!slices.Equal(primary.DataCapabilities, holdout.DataCapabilities) {
				t.Fatalf("primary/holdout execution identity drifted: primary=%+v holdout=%+v", primary, holdout)
			}
			if equalPrivateComparisonJSON(primary.Checks, holdout.Checks) {
				t.Fatal("holdout oracles are not bound to distinct evidence")
			}
		})
	}

	// The two provider run specs of one cohort may differ only in provider,
	// model, and pricing metadata.
	for _, root := range []string{primaryRoot, holdoutRoot} {
		codex := loadRepositoryRunSpec(t, filepath.Join(root, "run.mcp.codex.json"))
		claude := loadRepositoryRunSpec(t, filepath.Join(root, "run.mcp.claude.json"))
		if codex.Provider == claude.Provider || codex.Model == claude.Model ||
			equalPrivateComparisonJSON(codex.Pricing, claude.Pricing) {
			t.Fatalf("%s provider pair is not distinct: codex=%+v claude=%+v", root, codex, claude)
		}
		codex.Provider, claude.Provider = "", ""
		codex.Model, claude.Model = "", ""
		codex.Pricing, claude.Pricing = Pricing{}, Pricing{}
		if !equalPrivateComparisonJSON(codex, claude) {
			t.Fatalf("%s provider pair differs beyond provider/model/pricing metadata", root)
		}
	}
}

func jiraZeroProgressIdentities(pages []jiraZeroProgressFixturePage) map[string]bool {
	identities := map[string]bool{}
	for _, page := range pages {
		for _, row := range page.rows {
			identities[row.id+"\x1f"+row.key] = true
		}
	}
	return identities
}

var jiraZeroProgressNumberRE = regexp.MustCompile(`\d+`)

// TestRepositoryJiraSearchZeroProgressPromptsWithholdAnswers proves the prompts
// state the general stop rule without leaking the fixture answer: no identity,
// no page count, and no answer count.
func TestRepositoryJiraSearchZeroProgressPromptsWithholdAnswers(t *testing.T) {
	for _, cohort := range jiraZeroProgressCohorts() {
		t.Run(cohort.directory, func(t *testing.T) {
			root := jiraZeroProgressRoot(cohort)
			raw, err := os.ReadFile(filepath.Join(root, "prompt.mcp.v1.md"))
			if err != nil {
				t.Fatal(err)
			}
			prompt := strings.Join(strings.Fields(string(raw)), " ")
			for _, fragment := range []string{
				"Deduplicate by stable issue identity",
				"Continue paginating while a page contributes at least one identity you have not seen before",
				"Stop as soon as a successful page reached through an advancing cursor contributes no new identity",
				"do not re-read an earlier cursor",
				"do not treat stopping as proof that you have seen every matching issue",
				"Set `evidence_complete=true` only if a page reported `complete=true`",
			} {
				if !strings.Contains(prompt, fragment) {
					t.Fatalf("prompt no longer states the general zero-progress rule: missing %q", fragment)
				}
			}

			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			pages := jiraZeroProgressFixturePages(t, fixture, cohort)
			for _, page := range pages {
				for _, row := range page.rows {
					for _, secret := range []string{row.id, row.key, row.updated, row.summary} {
						if strings.Contains(prompt, secret) {
							t.Fatalf("prompt discloses fixture evidence %q", secret)
						}
					}
					status := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(row.status) + `\b`)
					if status.MatchString(prompt) {
						t.Fatalf("prompt discloses fixture status %q", row.status)
					}
				}
			}

			// Only the declared limit and byte bound may appear as numbers, so
			// no page count or answer count can be read out of the prompt.
			allowed := map[string]bool{
				strconv.Itoa(cohort.limit): true, strconv.Itoa(cohort.maxBytes): true,
			}
			for _, number := range jiraZeroProgressNumberRE.FindAllString(prompt, -1) {
				if !allowed[number] {
					t.Fatalf("prompt discloses the count %q", number)
				}
			}
			for _, word := range []string{
				"two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
			} {
				if regexp.MustCompile(`(?i)\b` + word + `\b`).MatchString(prompt) {
					t.Fatalf("prompt discloses the spelled count %q", word)
				}
			}
		})
	}
}
