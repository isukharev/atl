package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

const reportingColumns = "key,summary,status,assignee,priority,updated"
const dashboardColumns = "position,key,summary,status,assignee,priority,issuetype,updated"

type reportingWorkflowCase struct {
	directory, scope, period string
	queries                  []string
}

func TestJiraStatusReportWorkflowFixturesDriveProviderOracles(t *testing.T) {
	cases := []reportingWorkflowCase{
		{
			directory: "jira-status-report-workflow", scope: "project ORB", period: "2026-07-20..2026-07-26",
			queries: []string{
				"project = ORB AND statusCategory = Done AND resolved DURING (\"2026-07-20\", \"2026-07-26\") ORDER BY key ASC",
				"project = ORB AND statusCategory != Done ORDER BY key ASC",
				"project = ORB AND priority in (Highest, High) AND statusCategory != Done ORDER BY priority DESC, key ASC",
			},
		},
		{
			directory: "jira-status-report-workflow-holdout", scope: "project PINE", period: "2026-07-13..2026-07-20",
			queries: []string{
				"project = PINE AND statusCategory = Done AND resolved DURING (\"2026-07-13\", \"2026-07-20\") ORDER BY key ASC",
				"project = PINE AND statusCategory != Done ORDER BY key ASC",
				"project = PINE AND priority in (Highest, High) AND statusCategory != Done ORDER BY priority DESC, key ASC",
			},
		},
	}
	for _, test := range cases {
		t.Run(test.directory, func(t *testing.T) {
			root := reportingWorkflowRoot(test.directory)
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			assertStatusReportFixtureRoutes(t, fixture, test.queries)
			backend, service := startReportingWorkflowBackend(t, root)
			defer backend.Close()
			columns := strings.Split(reportingColumns, ",")
			pages := make([]*app.IssueList, len(test.queries))
			for i, query := range test.queries {
				page, err := service.SearchIssueList(context.Background(), query, columns, 2, "")
				if err != nil {
					t.Fatal(err)
				}
				pages[i] = page
			}
			final := statusReportFinal(t, test, pages)
			methods, unexpected, duplicates := backend.Summary()
			if !equalHTTPMethods(methods, map[string]int{"GET": 3}) || unexpected != 0 || duplicates != 0 {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
			commands := statusReportCommands(test.queries)
			assertReportingWorkflowSpecs(t, root, "atl:status-report", final, methods, 0, []int{0, 0, 0}, nil, commands)
			assertReportingSemanticMutationFails(t, root, final, methods, "/metrics/done_observed", "metrics_correct", 0, []int{0, 0, 0}, "atl:status-report")
			assertReportingSemanticMutationFails(t, root, final, methods, "/interpretation/qualification", "interpretation_correct", 0, []int{0, 0, 0}, "atl:status-report")
			if test.directory == "jira-status-report-workflow" {
				assertStatusFixtureMutationsChangeOracles(t, root, fixture, test)
			}
		})
	}
}

func statusReportFinal(t *testing.T, test reportingWorkflowCase, pages []*app.IssueList) []byte {
	t.Helper()
	ids := []string{"done", "active", "risk"}
	sources := make([]map[string]any, len(pages))
	seen, unassigned := map[string]bool{}, 0
	partialID, partialNext, qualificationMetric := "", "", ""
	for i, page := range pages {
		next := ""
		if page.Page.NextCursor != nil {
			next = *page.Page.NextCursor
		}
		sources[i] = map[string]any{"id": ids[i], "complete": page.Page.Complete, "truncated": page.Page.Truncated, "next_cursor": next, "observed_count": len(page.Rows)}
		if !page.Page.Complete {
			if partialID != "" {
				t.Fatal("fixture contains more than one partial source")
			}
			partialID, partialNext = ids[i], next
			qualificationMetric = map[string]string{"done": "done_observed", "active": "in_flight_observed", "risk": "high_risk_observed"}[ids[i]]
		}
		if i == 0 {
			continue
		}
		for _, row := range page.Rows {
			if !seen[row.Key] {
				seen[row.Key] = true
				if row.Values["assignee"] == nil || row.Values["assignee"] == "" {
					unassigned++
				}
			}
		}
	}
	if partialID == "" || partialNext == "" {
		t.Fatal("fixture must retain a named partial source")
	}
	facts := []string{
		formatStatusRows("done", pages[0].Rows, func(row app.IssueListRow) string { return stringValue(row.Values["status"]) }),
		formatStatusRows("active", pages[1].Rows, func(row app.IssueListRow) string {
			assignee := stringValue(row.Values["assignee"])
			if assignee == "" {
				assignee = "Unassigned"
			}
			return stringValue(row.Values["status"]) + "@" + assignee
		}),
		formatStatusRows("risk", pages[2].Rows, func(row app.IssueListRow) string {
			return stringValue(row.Values["priority"]) + "/" + stringValue(row.Values["status"])
		}),
	}
	risks := make([]string, len(pages[2].Rows))
	for i, row := range pages[2].Rows {
		risks[i] = row.Key
	}
	qualification := fmt.Sprintf("Partial: %s is truncated at next_cursor %s; %s=%d is an observed minimum.", partialID, partialNext, qualificationMetric, len(pages[map[string]int{"done": 0, "active": 1, "risk": 2}[partialID]].Rows))
	final := map[string]any{
		"scope": test.scope, "period": test.period, "sources": sources,
		"metrics":          map[string]any{"done_observed": len(pages[0].Rows), "in_flight_observed": len(pages[1].Rows), "high_risk_observed": len(pages[2].Rows), "unassigned_observed": unassigned},
		"observed_facts":   facts,
		"interpretation":   map[string]any{"rag": "amber", "qualification": qualification, "risks": risks},
		"evidence_quality": "partial", "not_published": true,
	}
	return mustJSON(t, final)
}

func formatStatusRows(source string, rows []app.IssueListRow, value func(app.IssueListRow) string) string {
	parts := make([]string, len(rows))
	for i, row := range rows {
		parts[i] = row.Key + "=" + value(row)
	}
	return source + " observed: " + strings.Join(parts, ", ")
}

func assertStatusFixtureMutationsChangeOracles(t *testing.T, root string, fixture MockFixture, test reportingWorkflowCase) {
	t.Helper()
	fieldMutation := mutateReportingFixtureBody(t, fixture, 1, func(body map[string]any) {
		issues := body["issues"].([]any)
		fields := issues[0].(map[string]any)["fields"].(map[string]any)
		fields["status"].(map[string]any)["name"] = "Review"
	})
	fieldFinal, fieldMethods := driveStatusReportFixture(t, fieldMutation, test)
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli.codex.json"))
	checks, err := evaluateReportingWorkflowChecks(spec, fieldFinal, 3, 0, "atl:status-report", fieldMethods, []int{0, 0, 0}, nil)
	if err != nil || checks["facts_correct"] {
		t.Fatalf("fixture status-field mutation did not reject facts oracle: checks=%v err=%v", checks, err)
	}

	pageMutation := mutateReportingFixtureBody(t, fixture, 1, func(body map[string]any) {
		issues := body["issues"].([]any)
		body["issues"] = issues[:1]
	})
	pageFinal, pageMethods := driveStatusReportFixture(t, pageMutation, test)
	checks, err = evaluateReportingWorkflowChecks(spec, pageFinal, 3, 0, "atl:status-report", pageMethods, []int{0, 0, 0}, nil)
	if err != nil || checks["sources_correct"] || checks["interpretation_correct"] {
		t.Fatalf("fixture page-metadata mutation did not reject source/qualification oracles: checks=%v err=%v", checks, err)
	}
}

func driveStatusReportFixture(t *testing.T, fixture MockFixture, test reportingWorkflowCase) ([]byte, map[string]int) {
	t.Helper()
	backend, service := startReportingWorkflowBackendFixture(t, fixture)
	defer backend.Close()
	columns := strings.Split(reportingColumns, ",")
	pages := make([]*app.IssueList, len(test.queries))
	for i, query := range test.queries {
		page, err := service.SearchIssueList(context.Background(), query, columns, 2, "")
		if err != nil {
			t.Fatal(err)
		}
		pages[i] = page
	}
	methods, unexpected, duplicates := backend.Summary()
	if unexpected != 0 || duplicates != 0 {
		t.Fatalf("mutated status fixture unexpected=%d duplicates=%d", unexpected, duplicates)
	}
	return statusReportFinal(t, test, pages), methods
}

type sprintWorkflowCase struct {
	directory             string
	boardID, sprintID     int
	cursors               []string
	statusBuckets         map[string]string
	expectContinuationErr bool
}

func TestJiraSprintDashboardWorkflowFixturesDriveProviderOracles(t *testing.T) {
	cases := []sprintWorkflowCase{
		{directory: "jira-sprint-dashboard-workflow", boardID: 31, sprintID: 71, cursors: []string{"", "2"}, statusBuckets: map[string]string{"To Do": "to_do", "In Progress": "in_progress", "In Review": "in_review", "Done": "done"}},
		{directory: "jira-sprint-dashboard-workflow-holdout", boardID: 44, sprintID: 88, cursors: []string{"", "2"}, statusBuckets: map[string]string{"Ready": "to_do", "Doing": "in_progress", "Review": "in_review", "Done": "done"}, expectContinuationErr: true},
	}
	for _, test := range cases {
		t.Run(test.directory, func(t *testing.T) {
			root := reportingWorkflowRoot(test.directory)
			fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
			assertSprintDashboardFixtureRoutes(t, fixture, test)
			backend, service := startReportingWorkflowBackend(t, root)
			defer backend.Close()
			sprint, err := service.SprintCurrent(context.Background(), test.boardID)
			if err != nil {
				t.Fatal(err)
			}
			columns := strings.Split(dashboardColumns, ",")
			var pages []*app.IssueList
			var continuationErr error
			for i, cursor := range test.cursors {
				page, pageErr := service.SprintIssueList(context.Background(), test.sprintID, columns, 2, cursor)
				if pageErr != nil {
					if !test.expectContinuationErr || i != 1 {
						t.Fatal(pageErr)
					}
					if !errors.Is(pageErr, domain.ErrForbidden) {
						t.Fatalf("holdout continuation error=%v want ErrForbidden", pageErr)
					}
					continuationErr = pageErr
					break
				}
				pages = append(pages, page)
			}
			if test.expectContinuationErr && (len(pages) != 1 || continuationErr == nil) {
				t.Fatalf("holdout retained %d successful pages with continuation error %v", len(pages), continuationErr)
			}
			final := sprintDashboardFinal(t, test, sprint, pages, continuationErr)
			methods, unexpected, duplicates := backend.Summary()
			if !equalHTTPMethods(methods, map[string]int{"GET": 3}) || unexpected != 0 || duplicates != 0 {
				t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
			}
			failed, exits := 0, []int{0, 0, 0}
			var contracts []CLIErrorContract
			if test.expectContinuationErr {
				failed, exits = 1, []int{0, 0, 6}
				contracts = []CLIErrorContract{{ExitCode: 6, Kind: "forbidden", Remediation: "request_access"}}
			}
			commands := sprintDashboardCommands(test)
			assertReportingWorkflowSpecs(t, root, "atl:sprint-dashboard", final, methods, failed, exits, contracts, commands)
			assertReportingSemanticMutationFails(t, root, final, methods, "/snapshot/complete", "snapshot_correct", failed, exits, "atl:sprint-dashboard")
			assertReportingSemanticMutationFails(t, root, final, methods, "/qualification", "qualification_correct", failed, exits, "atl:sprint-dashboard")
			if test.directory == "jira-sprint-dashboard-workflow" {
				assertSprintPageMetadataMutationChangesOracles(t, root, fixture, test)
			}
		})
	}
}

func sprintDashboardFinal(t *testing.T, test sprintWorkflowCase, sprint any, pages []*app.IssueList, continuationErr error) []byte {
	t.Helper()
	sprintBytes := mustJSON(t, sprint)
	var sprintDoc map[string]any
	if err := json.Unmarshal(sprintBytes, &sprintDoc); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{"to_do": 0, "in_progress": 0, "in_review": 0, "done": 0}
	attention := map[string][]string{"stale_in_flight": {}, "unassigned_non_done": {}, "high_priority_not_started": {}, "wip_concentration": {}}
	type loadCount struct{ total, wip int }
	loads := map[string]loadCount{}
	var keys []string
	for _, page := range pages {
		for _, row := range page.Rows {
			keys = append(keys, row.Key)
			status, _ := row.Values["status"].(string)
			bucket := test.statusBuckets[status]
			counts[bucket]++
			assignee, _ := row.Values["assignee"].(string)
			if assignee == "" {
				assignee = "Unassigned"
			}
			wip := bucket == "in_progress" || bucket == "in_review"
			load := loads[assignee]
			load.total++
			if wip {
				load.wip++
			}
			loads[assignee] = load
			if wip && stringValue(row.Values["updated"]) < "2026-07-25T12:00:00" {
				attention["stale_in_flight"] = append(attention["stale_in_flight"], row.Key)
			}
			if assignee == "Unassigned" && bucket != "done" {
				attention["unassigned_non_done"] = append(attention["unassigned_non_done"], row.Key)
			}
			priority := stringValue(row.Values["priority"])
			if bucket == "to_do" && (priority == "High" || priority == "Highest") {
				attention["high_priority_not_started"] = append(attention["high_priority_not_started"], row.Key)
			}
		}
	}
	for assignee, load := range loads {
		if assignee != "Unassigned" && load.wip >= 2 {
			attention["wip_concentration"] = append(attention["wip_concentration"], assignee)
		}
	}
	for _, values := range attention {
		sort.Strings(values)
	}
	assignees := make([]string, 0, len(loads))
	for assignee := range loads {
		assignees = append(assignees, assignee)
	}
	sort.Slice(assignees, func(i, j int) bool { return assignees[j] == "Unassigned" || assignees[i] < assignees[j] })
	loadRows := make([]map[string]any, 0, len(assignees))
	for _, assignee := range assignees {
		loadRows = append(loadRows, map[string]any{"assignee": assignee, "total": loads[assignee].total, "in_flight": loads[assignee].wip})
	}
	if len(pages) == 0 {
		t.Fatal("dashboard requires at least one successful membership page")
	}
	lastPage := pages[len(pages)-1]
	nextCursor := ""
	if lastPage.Page.NextCursor != nil {
		nextCursor = *lastPage.Page.NextCursor
	}
	complete := lastPage.Page.Complete && continuationErr == nil
	truncated := !complete
	qualification := fmt.Sprintf("Complete issue-count snapshot across %d pages and %d observed issues.", len(pages), len(keys))
	if continuationErr != nil {
		if !errors.Is(continuationErr, domain.ErrForbidden) || nextCursor == "" {
			t.Fatalf("partial snapshot lacks forbidden continuation provenance: err=%v next=%q", continuationErr, nextCursor)
		}
		qualification = fmt.Sprintf("Partial issue-count snapshot: continuation cursor %s was forbidden; rollups cover %d observed issues only.", nextCursor, len(keys))
	} else if !lastPage.Page.Complete {
		qualification = fmt.Sprintf("Partial issue-count snapshot: continuation cursor %s was not fetched; rollups cover %d observed issues only.", nextCursor, len(keys))
	}
	final := map[string]any{
		"board_id":      test.boardID,
		"sprint":        map[string]any{"id": int(sprintDoc["id"].(float64)), "name": sprintDoc["name"], "state": sprintDoc["state"], "start_date": sprintDoc["start_date"], "end_date": sprintDoc["end_date"]},
		"snapshot":      map[string]any{"complete": complete, "truncated": truncated, "next_cursor": nextCursor, "pages": len(pages), "observed_total": len(keys)},
		"status_counts": counts, "attention": attention, "load": loadRows, "issue_keys": keys,
		"qualification": qualification, "writes_performed": false,
	}
	return mustJSON(t, final)
}

func assertSprintPageMetadataMutationChangesOracles(t *testing.T, root string, fixture MockFixture, test sprintWorkflowCase) {
	t.Helper()
	mutation := mutateReportingFixtureBody(t, fixture, 2, func(body map[string]any) { body["total"] = float64(5) })
	backend, service := startReportingWorkflowBackendFixture(t, mutation)
	defer backend.Close()
	sprint, err := service.SprintCurrent(context.Background(), test.boardID)
	if err != nil {
		t.Fatal(err)
	}
	columns := strings.Split(dashboardColumns, ",")
	pages := make([]*app.IssueList, 0, 2)
	for _, cursor := range test.cursors {
		page, pageErr := service.SprintIssueList(context.Background(), test.sprintID, columns, 2, cursor)
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		pages = append(pages, page)
	}
	final := sprintDashboardFinal(t, test, sprint, pages, nil)
	methods, unexpected, duplicates := backend.Summary()
	if unexpected != 0 || duplicates != 0 {
		t.Fatalf("mutated sprint fixture unexpected=%d duplicates=%d", unexpected, duplicates)
	}
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli.codex.json"))
	checks, err := evaluateReportingWorkflowChecks(spec, final, 3, 0, "atl:sprint-dashboard", methods, []int{0, 0, 0}, nil)
	if err != nil || checks["snapshot_correct"] || checks["qualification_correct"] {
		t.Fatalf("fixture sprint-page mutation did not reject snapshot/qualification oracles: checks=%v err=%v", checks, err)
	}
}

func mutateReportingFixtureBody(t *testing.T, fixture MockFixture, routeIndex int, mutate func(map[string]any)) MockFixture {
	t.Helper()
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var clone MockFixture
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(clone.Routes[routeIndex].Body, &body); err != nil {
		t.Fatal(err)
	}
	mutate(body)
	clone.Routes[routeIndex].Body, err = json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := clone.Validate(); err != nil {
		t.Fatalf("mutated fixture invalid: %v", err)
	}
	return clone
}

func startReportingWorkflowBackend(t *testing.T, root string) (*MockBackend, *app.JiraService) {
	t.Helper()
	return startReportingWorkflowBackendFixture(t, loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json")))
}

func startReportingWorkflowBackendFixture(t *testing.T, fixture MockFixture) (*MockBackend, *app.JiraService) {
	t.Helper()
	backend, err := StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_JIRA_PAT", "synthetic-token")
	service, err := app.NewJira(&config.Config{JiraURL: backend.Environment()["ATL_JIRA_URL"]}, "benchmark-contract")
	if err != nil {
		backend.Close()
		t.Fatal(err)
	}
	return backend, service
}

type reviewedCLICommand struct {
	args          []string
	codex, claude string
}

func assertReportingWorkflowSpecs(t *testing.T, root, skill string, final []byte, methods map[string]int, failed int, exits []int, contracts []CLIErrorContract, commands []reviewedCLICommand) {
	t.Helper()
	for _, provider := range []string{"codex", "claude"} {
		spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli."+provider+".json"))
		prompt, err := os.ReadFile(filepath.Join(root, spec.PromptFile))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(prompt, []byte("`"+skill+"`")) {
			t.Fatalf("%s prompt does not require intended workflow skill %s", provider, skill)
		}
		assertReviewedCLICommands(t, spec, prompt, commands)
		if provider == "codex" {
			if len(spec.AllowedATLCommands) != 0 || len(spec.AllowedCLICommands) == 0 {
				t.Fatal("codex workflow must use structured CLI policy only")
			}
			for _, rule := range spec.AllowedCLICommands {
				if !reportingReadOnlyCommand(rule.Command) {
					t.Fatalf("codex policy admitted non-read command %v", rule.Command)
				}
			}
		} else if len(spec.AllowedCLICommands) != 0 || len(spec.AllowedATLCommands) != len(exits) {
			t.Fatal("claude workflow must retain exact terminated command prefixes")
		}
		assertReportingResponseSchema(t, root, spec, final)
		checks, err := evaluateReportingWorkflowChecks(spec, final, len(exits), failed, skill, methods, exits, contracts)
		if err != nil {
			t.Fatal(err)
		}
		for name, passed := range checks {
			if !passed {
				t.Fatalf("%s fixture-derived final failed %q", provider, name)
			}
		}
		if provider == "claude" {
			wrongSkill, err := evaluateReportingWorkflowChecks(spec, final, len(exits), failed, "atl:jira", methods, exits, contracts)
			if err != nil || wrongSkill["used_skill"] {
				t.Fatalf("%s generic Jira skill incorrectly satisfied workflow activation", provider)
			}
		}
		wrongMethods, err := evaluateReportingWorkflowChecks(spec, final, len(exits), failed, skill, map[string]int{"GET": methods["GET"] - 1, "POST": 1}, exits, contracts)
		if err != nil || wrongMethods["http_exact"] {
			t.Fatalf("%s write-shaped transport mutation passed", provider)
		}
		if len(contracts) > 0 {
			absent, err := evaluateReportingWorkflowChecks(spec, final, len(exits), failed, skill, methods, exits, nil)
			if err != nil || absent["error_contracts"] {
				t.Fatalf("%s absent CLI error contract passed", provider)
			}
			wrong := []CLIErrorContract{{ExitCode: 4, Kind: "not_found", Remediation: "verify_identifier_or_access"}}
			mismatched, err := evaluateReportingWorkflowChecks(spec, final, len(exits), failed, skill, methods, exits, wrong)
			if err != nil || mismatched["error_contracts"] {
				t.Fatalf("%s wrong CLI error contract passed", provider)
			}
		}
	}
}

func evaluateReportingWorkflowChecks(spec RunSpec, final []byte, invocations, failed int, skill string, methods map[string]int, exits []int, contracts []CLIErrorContract) (map[string]bool, error) {
	return evaluateRunChecksWithCLIErrorContracts(spec.Checks, final, "", invocations, failed, 0, 1, map[string]int{skill: 1},
		0, 0, methods, true, exits, nil, false, nil, nil, false, contracts)
}

func assertReviewedCLICommands(t *testing.T, spec RunSpec, prompt []byte, commands []reviewedCLICommand) {
	t.Helper()
	if len(commands) == 0 {
		t.Fatal("reviewed command contract is empty")
	}
	if spec.Provider == "codex" {
		if len(spec.AllowedCLICommands) != len(commands) {
			t.Fatalf("codex policy has %d rules want %d", len(spec.AllowedCLICommands), len(commands))
		}
		policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands}
		for _, command := range commands {
			if _, err := policy.Match(command.args); err != nil {
				t.Fatalf("codex policy does not match exact reviewed args %v: %v", command.args, err)
			}
			if !bytes.Contains(prompt, []byte("`"+command.codex+"`")) {
				t.Fatalf("codex prompt omits exact reviewed command %q", command.codex)
			}
		}
		return
	}
	want := make([]string, len(commands))
	for i, command := range commands {
		want[i] = command.claude
		if !bytes.Contains(prompt, []byte("`"+command.claude+"`")) {
			t.Fatalf("claude prompt omits exact reviewed command %q", command.claude)
		}
	}
	if !slices.Equal(spec.AllowedATLCommands, want) {
		t.Fatalf("claude exact commands=%v want=%v", spec.AllowedATLCommands, want)
	}
}

func assertReportingResponseSchema(t *testing.T, root string, spec RunSpec, final []byte) {
	t.Helper()
	schemaBytes, err := os.ReadFile(filepath.Join(root, spec.ResponseSchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	providerSchema, err := providerResponseSchema(spec, schemaBytes)
	if err != nil {
		t.Fatalf("%s response schema is not provider-compatible: %v", spec.Provider, err)
	}
	for name, schema := range map[string][]byte{"retained": schemaBytes, "provider": providerSchema} {
		if err := validateJSONSchemaSubsetInstance(schema, final); err != nil {
			t.Fatalf("%s %s response schema rejected fixture-derived final: %v", spec.Provider, name, err)
		}
	}
	var schema struct {
		Type                 string                     `json:"type"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Fatal("response schema root is not closed")
	}
	var document map[string]any
	if err := json.Unmarshal(final, &document); err != nil {
		t.Fatal(err)
	}
	properties, required, actual := make([]string, 0, len(schema.Properties)), slices.Clone(schema.Required), make([]string, 0, len(document))
	for name := range schema.Properties {
		properties = append(properties, name)
	}
	for name := range document {
		actual = append(actual, name)
	}
	slices.Sort(properties)
	slices.Sort(required)
	slices.Sort(actual)
	if !slices.Equal(properties, required) || !slices.Equal(properties, actual) {
		t.Fatalf("schema/final root mismatch: properties=%v required=%v actual=%v", properties, required, actual)
	}
}

func assertReportingSemanticMutationFails(t *testing.T, root string, final []byte, methods map[string]int, pointer, checkName string, failed int, exits []int, skill string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(final, &doc); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	parent := doc
	for _, part := range parts[:len(parts)-1] {
		parent = parent[part].(map[string]any)
	}
	leaf := parts[len(parts)-1]
	switch value := parent[leaf].(type) {
	case bool:
		parent[leaf] = !value
	case float64:
		parent[leaf] = value + 1
	case string:
		parent[leaf] = value + " [mutated]"
	default:
		t.Fatalf("unsupported semantic mutation at %s", pointer)
	}
	mutated := mustJSON(t, doc)
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli.codex.json"))
	var contracts []CLIErrorContract
	if failed > 0 {
		contracts = []CLIErrorContract{{ExitCode: 6, Kind: "forbidden", Remediation: "request_access"}}
	}
	baseline, err := evaluateReportingWorkflowChecks(spec, final, 3, failed, skill, methods, exits, contracts)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := evaluateReportingWorkflowChecks(spec, mutated, 3, failed, skill, methods, exits, contracts)
	if err != nil {
		t.Fatal(err)
	}
	if !baseline[checkName] || checks[checkName] {
		t.Fatalf("semantic mutation %s did not uniquely reject %s: baseline=%v mutated=%v", pointer, checkName, baseline[checkName], checks[checkName])
	}
}

func TestJiraReportingWorkflowSamplingPairs(t *testing.T) {
	pairs := []struct{ primary, holdout, taskClass, skill string }{
		{"jira-status-report-workflow", "jira-status-report-workflow-holdout", "jira/status-report", "atl:status-report"},
		{"jira-sprint-dashboard-workflow", "jira-sprint-dashboard-workflow-holdout", "jira/sprint-dashboard", "atl:sprint-dashboard"},
	}
	for _, pair := range pairs {
		t.Run(pair.taskClass, func(t *testing.T) {
			primaryRoot, holdoutRoot := reportingWorkflowRoot(pair.primary), reportingWorkflowRoot(pair.holdout)
			primaryScenario := loadRepositoryScenario(t, filepath.Join(primaryRoot, "scenario.v1.json"))
			holdoutScenario := loadRepositoryScenario(t, filepath.Join(holdoutRoot, "scenario.v1.json"))
			if primaryScenario.ID == holdoutScenario.ID || primaryScenario.TaskClass != pair.taskClass || holdoutScenario.TaskClass != pair.taskClass || primaryScenario.DataClass != "synthetic" || holdoutScenario.DataClass != "synthetic" {
				t.Fatal("primary/holdout scenario identity drifted")
			}
			for _, file := range []string{"fixture.json", "prompt.codex.v1.md", "prompt.claude.v1.md"} {
				primary, err := os.ReadFile(filepath.Join(primaryRoot, file))
				if err != nil {
					t.Fatal(err)
				}
				holdout, err := os.ReadFile(filepath.Join(holdoutRoot, file))
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Equal(primary, holdout) {
					t.Fatalf("holdout %s is not distinct", file)
				}
			}
			primaryIdentity := reportingFixtureIdentities(t, loadRepositoryMockFixture(t, filepath.Join(primaryRoot, "fixture.json")))
			holdoutIdentity := reportingFixtureIdentities(t, loadRepositoryMockFixture(t, filepath.Join(holdoutRoot, "fixture.json")))
			if setsOverlap(primaryIdentity.issues, holdoutIdentity.issues) ||
				setsOverlap(primaryIdentity.projects, holdoutIdentity.projects) ||
				setsOverlap(primaryIdentity.boards, holdoutIdentity.boards) ||
				setsOverlap(primaryIdentity.sprints, holdoutIdentity.sprints) {
				t.Fatalf("primary/holdout fixture identities overlap: primary=%+v holdout=%+v", primaryIdentity, holdoutIdentity)
			}
			if pair.taskClass == "jira/status-report" && (len(primaryIdentity.projects) == 0 || len(holdoutIdentity.projects) == 0 || len(primaryIdentity.issues) == 0 || len(holdoutIdentity.issues) == 0) {
				t.Fatal("status-report fixtures lack disjoint project/issue identities")
			}
			if pair.taskClass == "jira/sprint-dashboard" && (len(primaryIdentity.boards) == 0 || len(holdoutIdentity.boards) == 0 || len(primaryIdentity.sprints) == 0 || len(holdoutIdentity.sprints) == 0 || len(primaryIdentity.issues) == 0 || len(holdoutIdentity.issues) == 0) {
				t.Fatal("sprint-dashboard fixtures lack disjoint board/sprint/issue identities")
			}
			primarySchema, _ := os.ReadFile(filepath.Join(primaryRoot, "response-schema.v1.json"))
			holdoutSchema, _ := os.ReadFile(filepath.Join(holdoutRoot, "response-schema.v1.json"))
			if !bytes.Equal(primarySchema, holdoutSchema) {
				t.Fatal("primary/holdout response schema drifted")
			}
			for _, provider := range []string{"codex", "claude"} {
				primary := loadRepositoryRunSpec(t, filepath.Join(primaryRoot, "run.cli."+provider+".json"))
				holdout := loadRepositoryRunSpec(t, filepath.Join(holdoutRoot, "run.cli."+provider+".json"))
				wantProvider, wantModel := "codex", "gpt-5.6-luna"
				if provider == "claude" {
					wantProvider, wantModel = "claude-code", "claude-opus-4-8"
				}
				if primary.Provider != wantProvider || holdout.Provider != wantProvider || primary.Model != wantModel || holdout.Model != wantModel || primary.Reasoning != "high" || holdout.Reasoning != "high" || primary.Variant != holdout.Variant || primary.Repetitions != 3 || holdout.Repetitions != 1 || primary.EffectiveCategory() != "surface-native" || holdout.EffectiveCategory() != "surface-native" || primary.EffectiveSurface() != "cli-skill" || holdout.EffectiveSurface() != "cli-skill" {
					t.Fatalf("%s sampling identity drifted", provider)
				}
				if !slices.Equal(primary.AllowedTools, holdout.AllowedTools) {
					t.Fatalf("%s provider tool identity drifted", provider)
				}
			}
		})
	}
}

func reportingWorkflowRoot(directory string) string {
	return filepath.Join("..", "..", "benchmarks", "agent-eval", directory)
}

func statusReportCommands(queries []string) []reviewedCLICommand {
	commands := make([]reviewedCLICommand, len(queries))
	for i, query := range queries {
		codex := "atl jira issue search --jql '" + query + "' --columns " + reportingColumns + " --limit 2"
		commands[i] = reviewedCLICommand{
			args:  []string{"jira", "issue", "search", "--jql", query, "--columns", reportingColumns, "--limit", "2"},
			codex: codex, claude: codex + " --",
		}
	}
	return commands
}

func sprintDashboardCommands(test sprintWorkflowCase) []reviewedCLICommand {
	current := fmt.Sprintf("atl jira sprint current --board %d", test.boardID)
	first := fmt.Sprintf("atl jira sprint issues %d --columns %s --limit 2", test.sprintID, dashboardColumns)
	next := first + " --cursor 2"
	return []reviewedCLICommand{
		{args: []string{"jira", "sprint", "current", "--board", fmt.Sprint(test.boardID)}, codex: current, claude: current + " --"},
		{args: []string{"jira", "sprint", "issues", fmt.Sprint(test.sprintID), "--columns", dashboardColumns, "--limit", "2"}, codex: first, claude: first + " --"},
		{args: []string{"jira", "sprint", "issues", fmt.Sprint(test.sprintID), "--columns", dashboardColumns, "--limit", "2", "--cursor", "2"}, codex: next, claude: next + " --"},
	}
}

func assertStatusReportFixtureRoutes(t *testing.T, fixture MockFixture, queries []string) {
	t.Helper()
	if len(fixture.Routes) != len(queries) {
		t.Fatalf("status fixture routes=%d want=%d", len(fixture.Routes), len(queries))
	}
	for i, route := range fixture.Routes {
		if route.Method != "GET" || route.Path != "/jira/rest/api/2/search" || route.QueryEquals["jql"] != queries[i] ||
			route.QueryEquals["startAt"] != "0" || route.QueryEquals["maxResults"] != "2" ||
			route.QueryEquals["fields"] != "summary,status,assignee,priority,updated" {
			t.Fatalf("status fixture route %d drifted: %+v", i, route)
		}
	}
}

func assertSprintDashboardFixtureRoutes(t *testing.T, fixture MockFixture, test sprintWorkflowCase) {
	t.Helper()
	if len(fixture.Routes) != 3 {
		t.Fatalf("sprint fixture routes=%d want=3", len(fixture.Routes))
	}
	currentPath := fmt.Sprintf("/jira/rest/agile/1.0/board/%d/sprint", test.boardID)
	issuePath := fmt.Sprintf("/jira/rest/agile/1.0/sprint/%d/issue", test.sprintID)
	current := fixture.Routes[0]
	if current.Method != "GET" || current.Path != currentPath || current.QueryEquals["startAt"] != "0" || current.QueryEquals["maxResults"] != "50" || current.QueryEquals["state"] != "active" {
		t.Fatalf("current sprint fixture route drifted: %+v", current)
	}
	for i, cursor := range []string{"0", "2"} {
		route := fixture.Routes[i+1]
		if route.Method != "GET" || route.Path != issuePath || route.QueryEquals["startAt"] != cursor || route.QueryEquals["maxResults"] != "2" || route.QueryEquals["fields"] != "summary,status,assignee,priority,issuetype,updated" {
			t.Fatalf("sprint issue fixture route %d drifted: %+v", i, route)
		}
	}
	if test.expectContinuationErr && fixture.Routes[2].Status != 403 {
		t.Fatalf("holdout continuation status=%d want=403", fixture.Routes[2].Status)
	}
}

type reportingFixtureIdentity struct {
	projects, issues, boards, sprints map[string]struct{}
}

func reportingFixtureIdentities(t *testing.T, fixture MockFixture) reportingFixtureIdentity {
	t.Helper()
	identity := reportingFixtureIdentity{
		projects: map[string]struct{}{}, issues: map[string]struct{}{},
		boards: map[string]struct{}{}, sprints: map[string]struct{}{},
	}
	var collectIssueKeys func(any)
	collectIssueKeys = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if key, ok := value["key"].(string); ok && key != "" {
				identity.issues[key] = struct{}{}
			}
			for _, child := range value {
				collectIssueKeys(child)
			}
		case []any:
			for _, child := range value {
				collectIssueKeys(child)
			}
		}
	}
	for _, route := range fixture.Routes {
		if query := route.QueryEquals["jql"]; query != "" {
			const marker = "project = "
			if index := strings.Index(query, marker); index >= 0 {
				fields := strings.Fields(query[index+len(marker):])
				if len(fields) > 0 {
					identity.projects[fields[0]] = struct{}{}
				}
			}
		}
		var id int
		if _, err := fmt.Sscanf(route.Path, "/jira/rest/agile/1.0/board/%d/sprint", &id); err == nil {
			identity.boards[fmt.Sprint(id)] = struct{}{}
		}
		id = 0
		if _, err := fmt.Sscanf(route.Path, "/jira/rest/agile/1.0/sprint/%d/issue", &id); err == nil {
			identity.sprints[fmt.Sprint(id)] = struct{}{}
		}
		var body any
		if err := json.Unmarshal(route.Body, &body); err != nil {
			t.Fatal(err)
		}
		collectIssueKeys(body)
	}
	return identity
}

func setsOverlap(left, right map[string]struct{}) bool {
	for value := range left {
		if _, ok := right[value]; ok {
			return true
		}
	}
	return false
}

func reportingReadOnlyCommand(command []string) bool {
	return slices.Equal(command, []string{"jira", "issue", "search"}) || slices.Equal(command, []string{"jira", "sprint", "current"}) || slices.Equal(command, []string{"jira", "sprint", "issues"})
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
