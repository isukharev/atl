package agenteval

import (
	"bytes"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type jiraInverseReferenceCohort struct {
	directory       string
	target          string
	targetKind      string
	scopeJQL        string
	mode            string
	sources         string
	maxIssues       string
	maxRequests     string
	maxResponse     string
	strict          bool
	expectedJQL     string
	expectedGETs    int
	expectedRepeats int
	expectedBytes   int64
}

func jiraInverseReferenceCohorts() []jiraInverseReferenceCohort {
	return []jiraInverseReferenceCohort{
		{
			directory: "jira-inverse-reference-search", target: "https://code-inverse.example.test/platform/widget",
			targetKind: "gitlab-project", scopeJQL: "project = IRP", mode: "exhaustive", sources: "development",
			maxIssues: "10", maxRequests: "10", maxResponse: "65536",
			expectedJQL: "(project = IRP) ORDER BY key ASC", expectedGETs: 4, expectedRepeats: 1, expectedBytes: 1081,
		},
		{
			directory: "jira-inverse-reference-search-holdout", target: "8401",
			targetKind: "confluence-page", scopeJQL: "project = IRH AND labels = PRIVACY_CANARY_QUERY",
			mode: "fast", sources: "description", maxIssues: "5", maxRequests: "5", maxResponse: "32768", strict: true,
			expectedJQL:  `(project = IRH AND labels = PRIVACY_CANARY_QUERY) AND (text ~ "\"8401\"") ORDER BY key ASC`,
			expectedGETs: 2, expectedBytes: 297,
		},
	}
}

func (c jiraInverseReferenceCohort) root() string {
	return filepath.Join("..", "..", "benchmarks", "agent-eval", c.directory)
}

func (c jiraInverseReferenceCohort) args() []string {
	args := []string{
		"jira", "issue", "reference", "search",
		"--target", c.target,
		"--target-kind", c.targetKind,
		"--scope-jql", c.scopeJQL,
		"--mode", c.mode,
		"--sources", c.sources,
		"--max-issues", c.maxIssues,
		"--max-requests", c.maxRequests,
		"--max-response-bytes", c.maxResponse,
	}
	if c.strict {
		args = append(args, "--strict")
	}
	return args
}

func prepareJiraInverseReferenceProcessFixture(t *testing.T, cohort jiraInverseReferenceCohort, fixture MockFixture) MockFixture {
	t.Helper()
	wantNames := []string{"selection"}
	wantPaths := []string{fixture.JiraContext + "/rest/api/2/search"}
	wantQueries := []map[string]string{{
		"fields": "key", "jql": cohort.expectedJQL, "maxResults": cohort.maxIssues, "startAt": "0",
	}}
	if cohort.directory == "jira-inverse-reference-search" {
		wantNames = append(wantNames, "development-summary", "development-repository")
		wantPaths = append(wantPaths,
			fixture.JiraContext+"/rest/dev-status/1.0/issue/summary",
			fixture.JiraContext+"/rest/dev-status/1.0/issue/detail",
		)
		wantQueries = append(wantQueries,
			map[string]string{"issueId": "41001"},
			map[string]string{"applicationType": "GitLab", "dataType": "repository", "issueId": "41001"},
		)
	} else {
		wantNames = append(wantNames, "description-snapshot")
		wantPaths = append(wantPaths, fixture.JiraContext+"/rest/api/2/issue/IH-84")
		wantQueries = append(wantQueries, map[string]string{"fields": "description"})
	}
	wantSequence := slices.Clone(wantNames)
	if cohort.expectedRepeats == 1 {
		wantSequence = append([]string{"selection"}, wantSequence...)
	}
	if len(fixture.Routes) != len(wantNames) || !slices.Equal(fixture.RequestSequence, wantSequence) {
		t.Fatalf("inverse-reference retained route inventory drifted: routes=%d sequence=%v want=%v",
			len(fixture.Routes), fixture.RequestSequence, wantSequence)
	}
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	for index, route := range prepared.Routes {
		if route.Name != wantNames[index] || route.Method != "GET" || route.Path != wantPaths[index] ||
			!maps.Equal(route.QueryEquals, wantQueries[index]) || len(route.QueryContains) != 0 || len(route.RequestBody) != 0 {
			t.Fatalf("inverse-reference retained route %d drifted: %+v", index, route)
		}
		if index == 0 {
			if cohort.expectedRepeats == 1 && (route.Status != 0 || len(route.Body) != 0 || len(route.Responses) != 2) {
				t.Fatalf("exhaustive selection route is not exactly two stateful responses: %+v", route)
			}
			if cohort.expectedRepeats == 0 && (route.Status != 200 || len(route.Body) == 0 || len(route.Responses) != 0) {
				t.Fatalf("fast selection route is not exactly one response: %+v", route)
			}
		} else if route.Status != 200 || len(route.Body) == 0 || len(route.Responses) != 0 {
			t.Fatalf("inverse-reference retained route %d response drifted: %+v", index, route)
		}
		route.closedQuery = true
		prepared.Routes[index] = route
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare inverse-reference process fixture: %v", err)
	}
	return prepared
}

func startJiraInverseReferenceProcess(t *testing.T, cohort jiraInverseReferenceCohort, fixture MockFixture, policy CLICommandPolicy) *SyntheticATLProcess {
	t.Helper()
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepareJiraInverseReferenceProcessFixture(t, cohort, fixture),
		ScratchRoot: privateSyntheticATLScratch(t), CLIPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close inverse-reference process: %v", err)
		}
	})
	for name := range environmentMap(process.environment) {
		if strings.Contains(strings.ToUpper(name), "GITLAB") {
			t.Fatalf("inverse-reference child received GitLab config/token environment %q", name)
		}
	}
	return process
}

func runJiraInverseReferenceCLI(t *testing.T, process *SyntheticATLProcess, cohort jiraInverseReferenceCohort) (JiraInverseReferenceView, SyntheticCLIBytesResult) {
	t.Helper()
	called, err := process.RunCLIBytes(t.Context(), cohort.args()...)
	if err != nil {
		t.Fatal(err)
	}
	wantExit := 0
	if cohort.strict {
		wantExit = 8
	}
	if called.ExitCode != wantExit || len(bytes.TrimSpace(called.Stdout)) == 0 {
		t.Fatalf("selected inverse-reference CLI exit/stdout drifted: exit=%d stdout=%d stderr=%d",
			called.ExitCode, len(called.Stdout), len(called.Stderr))
	}
	view, err := DecodeJiraInverseReferenceView(bytes.NewReader(called.Stdout))
	if err != nil {
		t.Fatalf("decode selected inverse-reference result: %v", err)
	}
	wantMaxIssues, _ := strconv.Atoi(cohort.maxIssues)
	wantMaxRequests, _ := strconv.Atoi(cohort.maxRequests)
	wantMaxResponse, _ := strconv.ParseInt(cohort.maxResponse, 10, 64)
	if view.Usage.MaxIssues != wantMaxIssues || view.Usage.MaxRequests != wantMaxRequests ||
		view.Usage.MaxResponseBytes != wantMaxResponse || view.Usage.Requests != cohort.expectedGETs ||
		view.Usage.ResponseBytes != cohort.expectedBytes || !view.Usage.Reconciled {
		t.Fatalf("selected inverse-reference shared request/byte ledger drifted: %+v", view.Usage)
	}
	return view, called
}

func assertJiraInverseReferenceProcessAccounting(t *testing.T, process *SyntheticATLProcess, cohort jiraInverseReferenceCohort) {
	t.Helper()
	summary := process.Summary()
	if !equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": cohort.expectedGETs}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != cohort.expectedRepeats ||
		!process.RequestSequenceComplete() || len(summary.CLIInvocations) != 1 ||
		summary.CLIInvocations["jira_inverse_reference"] != 1 || len(summary.MCPInvocations) != 0 {
		t.Fatalf("inverse-reference process accounting drifted: summary=%+v sequence_complete=%t",
			summary, process.RequestSequenceComplete())
	}
}

func assertJiraInverseReferenceAdmissionRefusals(t *testing.T, cohort jiraInverseReferenceCohort, fixture MockFixture, policy CLICommandPolicy) {
	t.Helper()
	mutations := [][]string{
		replaceJiraInverseReferenceFlag(cohort.args(), "--target", cohort.target+"/other"),
		replaceJiraInverseReferenceFlag(cohort.args(), "--mode", map[string]string{"exhaustive": "fast", "fast": "exhaustive"}[cohort.mode]),
		replaceJiraInverseReferenceFlag(cohort.args(), "--max-requests", "25000"),
	}
	for index, args := range mutations {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			process := startJiraInverseReferenceProcess(t, cohort, fixture, policy)
			if _, err := process.RunCLIBytes(t.Context(), args...); err == nil {
				t.Fatal("unadmitted inverse-reference divergence crossed the process boundary")
			}
			summary := process.Summary()
			if len(summary.HTTPMethods) != 0 || summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
				len(summary.CLIInvocations) != 0 || len(summary.MCPInvocations) != 0 || process.RequestSequenceComplete() {
				t.Fatalf("inverse-reference divergence was not refused pre-backend: %+v", summary)
			}
		})
	}
}

func replaceJiraInverseReferenceFlag(args []string, flag, replacement string) []string {
	out := slices.Clone(args)
	for index := 0; index+1 < len(out); index++ {
		if out[index] == flag {
			out[index+1] = replacement
			return out
		}
	}
	return append(out, flag, replacement)
}

func jiraInverseReferenceShellCommand(cohort jiraInverseReferenceCohort) string {
	args := cohort.args()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--scope-jql" {
			args[index+1] = "'" + args[index+1] + "'"
			break
		}
	}
	return "atl " + strings.Join(args, " ")
}

func inverseReferenceFinalJSON(t *testing.T, view JiraInverseReferenceView, encoded []byte, canaries []string) []byte {
	t.Helper()
	canaryPresent := false
	for _, canary := range canaries {
		canaryPresent = canaryPresent || bytes.Contains(encoded, []byte(canary))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"jql", "url", "host", "path", "snippet", "body", "title", "username", "application_name", "property_key"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatalf("inverse-reference result exposed forbidden root member %q", forbidden)
		}
	}
	phase := func(value JiraInverseReferencePhaseView) map[string]any {
		out := map[string]any{"complete": value.Complete, "reason": nil}
		if value.Reason != "" {
			out["reason"] = value.Reason
		}
		return out
	}
	matches := make([]map[string]any, 0, len(view.Matches))
	for _, match := range view.Matches {
		item := map[string]any{
			"issue_key": match.IssueKey, "relation": match.Relation, "direction": match.Direction,
			"source": match.Source, "technical_field_id": nil, "stability": match.Stability,
			"confidence": match.Confidence, "complete": match.Complete,
		}
		if match.TechnicalFieldID != "" {
			item["technical_field_id"] = match.TechnicalFieldID
		}
		matches = append(matches, item)
	}
	frontier := map[string]any{
		"phase": view.Frontier.Phase, "pass": nil, "page_start": nil,
		"verified_issues": view.Frontier.VerifiedIssues, "source": nil, "source_reason": nil,
	}
	if view.Frontier.Pass != 0 {
		frontier["pass"] = view.Frontier.Pass
	}
	if view.Frontier.PageStart != 0 {
		frontier["page_start"] = view.Frontier.PageStart
	}
	if view.Frontier.Source != "" {
		frontier["source"] = view.Frontier.Source
	}
	if view.Frontier.SourceReason != "" {
		frontier["source_reason"] = view.Frontier.SourceReason
	}
	final := map[string]any{
		"target_kind": view.Target.Kind, "mode": view.Mode, "sources": view.Sources,
		"selection": phase(view.Selection), "verification": phase(view.Verification), "counts": view.Counts,
		"source_counts": view.SourceCounts, "matches": matches, "frontier": frontier,
		"reconciliation": view.Reconciliation,
		"usage": map[string]any{
			"requests": view.Usage.Requests, "response_bytes": view.Usage.ResponseBytes,
			"reconciled": view.Usage.Reconciled,
		},
		"complete": view.Complete, "absence_proven": view.AbsenceProven,
		"privacy": map[string]any{"canary_present": canaryPresent, "transport_fields_present": false},
	}
	data, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
