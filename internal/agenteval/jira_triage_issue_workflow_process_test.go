package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"testing"
)

type jiraTriagePrimaryProcessEvidence struct {
	Summary SyntheticATLProcessSummary
	Exits   []int
}

func startJiraTriagePrimaryProcess(
	t *testing.T,
	root string,
	fixture MockFixture,
	cohort triageCohort,
	policy CLICommandPolicy,
) *SyntheticATLProcess {
	t.Helper()
	match, err := policy.Match(triageCreateCommand(cohort))
	if err != nil || match.Name != "create" {
		t.Fatalf("primary triage create policy match=%+v err=%v", match, err)
	}
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepareJiraTriagePrimaryProcessFixture(t, fixture, cohort),
		ScratchRoot: privateSyntheticATLScratch(t), WorkspaceTemplate: filepath.Join(root, "workspace"),
		SyntheticWriteRules: SyntheticWriteRules{"create"}, CLIPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close selected Jira triage process: %v", err)
		}
	})
	return process
}

func prepareJiraTriagePrimaryProcessFixture(t *testing.T, fixture MockFixture, cohort triageCohort) MockFixture {
	t.Helper()
	if cohort.decision != "create" || !slices.Equal(fixture.RequestSequence, cohort.sequence) {
		t.Fatalf("primary triage fixture branch drifted: decision=%q sequence=%v want=%v", cohort.decision, fixture.RequestSequence, cohort.sequence)
	}
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	seen := make(map[string]struct{}, len(prepared.Routes))
	for index := range prepared.Routes {
		prepared.Routes[index].closedQuery = true
		seen[prepared.Routes[index].Name] = struct{}{}
	}
	for _, name := range cohort.sequence {
		if _, ok := seen[name]; !ok {
			t.Fatalf("primary triage fixture omits sequence route %q", name)
		}
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare primary triage fixture: %v", err)
	}
	return prepared
}

func executeJiraTriagePrimaryProcess(
	t *testing.T,
	process *SyntheticATLProcess,
	cohort triageCohort,
) jiraTriagePrimaryProcessEvidence {
	t.Helper()
	exits := make([]int, 0, len(cohort.sequence))
	searches := make([]JiraSnapshotIssueList, 0, len(cohort.queries))
	for index := range cohort.queries {
		result := callJiraTriageCLIJSON(t, process, triageSearchCommand(cohort, index))
		exits = append(exits, result.ExitCode)
		list, err := DecodeJiraSnapshotIssueList(bytes.NewReader(result.JSON))
		if err != nil {
			t.Fatalf("decode selected triage search %d: %v", index, err)
		}
		if list.Projection.View != "explicit" || !list.Page.Complete || !slices.Equal(triageIssueListKeys(list), cohort.queryKeys[index]) {
			t.Fatalf("selected triage search %d view=%q complete=%t keys=%v want=%v", index, list.Projection.View, list.Page.Complete, triageIssueListKeys(list), cohort.queryKeys[index])
		}
		searches = append(searches, list)
	}

	issues := make([]JiraTriageIssueGet, 0, len(cohort.candidates))
	for _, expected := range cohort.candidates {
		result := callJiraTriageCLIJSON(t, process, triageIssueGetCommand(cohort, expected.key))
		exits = append(exits, result.ExitCode)
		issue, err := DecodeJiraTriageIssueGet(bytes.NewReader(result.JSON))
		if err != nil {
			t.Fatalf("decode selected triage issue %q: %v", expected.key, err)
		}
		if actual := scoreTriageIssue(issue, cohort); actual != expected {
			t.Fatalf("selected triage candidate %q score=%+v want=%+v", issue.Key, actual, expected)
		}
		issues = append(issues, issue)
	}
	if !triageMayWrite(searches, issues) {
		t.Fatal("complete selected triage reads unexpectedly refused the approved branch")
	}
	if decision := triageDecision(cohort.candidates); decision != cohort.decision {
		t.Fatalf("selected triage decision=%q want=%q", decision, cohort.decision)
	}

	result, err := process.RunSyntheticWriteCLIJSON(t.Context(), triageCreateCommand(cohort)...)
	if err != nil {
		t.Fatalf("selected triage create: %v", err)
	}
	exits = append(exits, result.ExitCode)
	if result.ExitCode != 0 || len(result.JSON) == 0 || len(result.Stderr) != 0 {
		t.Fatalf("selected triage create exit=%d stdout_bytes=%d stderr_bytes=%d", result.ExitCode, len(result.JSON), len(result.Stderr))
	}
	created, err := DecodeJiraIssueCreate(bytes.NewReader(result.JSON))
	if err != nil {
		t.Fatalf("decode selected triage create: %v", err)
	}
	if want := triageFixtureCreateDescription(t, process.config.Fixture); created.Key != cohort.createdKey || created.Summary != cohort.newSummary ||
		created.Project != cohort.project || created.Type != "Bug" || created.Status != "" || created.Description != want {
		t.Fatalf("selected triage create=%+v want key=%q summary=%q project=%q description=%q", created, cohort.createdKey, cohort.newSummary, cohort.project, want)
	}
	if !slices.Equal(exits, cohort.exitCodes) {
		t.Fatalf("selected triage exits=%v want=%v", exits, cohort.exitCodes)
	}

	summary := process.Summary()
	expectedCLI := make(map[string]int, len(cohort.sequence))
	for _, name := range cohort.sequence {
		expectedCLI[name]++
	}
	if !process.RequestSequenceComplete() || !maps.Equal(summary.HTTPMethods, cohort.methods) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != cohort.duplicates ||
		!maps.Equal(summary.CLIInvocations, expectedCLI) || len(summary.MCPInvocations) != 0 {
		t.Fatalf("selected triage primary accounting drifted: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
	if _, err := process.RunSyntheticWriteCLIJSON(t.Context(), triageCreateCommand(cohort)...); err == nil {
		t.Fatal("selected triage create replay was admitted")
	}
	afterReplay := process.Summary()
	if !maps.Equal(afterReplay.HTTPMethods, summary.HTTPMethods) || afterReplay.UnexpectedRequests != summary.UnexpectedRequests ||
		afterReplay.DuplicateRequests != summary.DuplicateRequests || !maps.Equal(afterReplay.CLIInvocations, summary.CLIInvocations) {
		t.Fatalf("selected triage create replay changed evidence: before=%+v after=%+v", summary, afterReplay)
	}
	return jiraTriagePrimaryProcessEvidence{Summary: summary, Exits: exits}
}

func callJiraTriageCLIJSON(t *testing.T, process *SyntheticATLProcess, args []string) SyntheticCLIResult {
	t.Helper()
	result, err := process.RunCLIJSON(t.Context(), args...)
	if err != nil {
		t.Fatalf("selected triage command %v: %v", args, err)
	}
	if result.ExitCode != 0 || len(result.JSON) == 0 || len(result.Stderr) != 0 {
		t.Fatalf("selected triage command %v exit=%d stdout_bytes=%d stderr_bytes=%d", args, result.ExitCode, len(result.JSON), len(result.Stderr))
	}
	return result
}

func triageSearchCommand(cohort triageCohort, index int) []string {
	return []string{"jira", "issue", "search", "--jql", cohort.queries[index], "--limit", "10", "--columns", triageColumns}
}

func triageIssueGetCommand(cohort triageCohort, key string) []string {
	args := []string{"jira", "issue", "get", key}
	if cohort.directory == triagePrimaryDirectory {
		args = append(args, "--fields", cohort.candidateFields)
	}
	return args
}

func triageCreateCommand(cohort triageCohort) []string {
	return []string{
		"jira", "issue", "create", "--project", cohort.project, "--type", "Bug", "--summary", cohort.newSummary, "--from-md", "new-bug.md",
	}
}

func triageFixtureCreateDescription(t *testing.T, fixture MockFixture) string {
	t.Helper()
	var document struct {
		Fields struct {
			Description string `json:"description"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(triageRoute(t, fixture, "create").RequestBody, &document); err != nil || document.Fields.Description == "" {
		t.Fatalf("decode triage create fixture description: %v", err)
	}
	return document.Fields.Description
}

func assertJiraTriagePrimaryProcessAdmissionRefused(
	t *testing.T,
	root string,
	fixture MockFixture,
	cohort triageCohort,
	policy CLICommandPolicy,
) {
	t.Helper()
	mutations := [][]string{
		{"jira", "issue", "search", "--jql", cohort.queries[0] + " changed", "--limit", "10", "--columns", triageColumns},
		{"jira", "issue", "get", "WRONG-1"},
		{"jira", "issue", "create", "--project", cohort.project, "--type", "Bug", "--summary", cohort.newSummary, "--from-md", "wrong.md"},
		{"jira", "issue", "create", "--project", cohort.project, "--type", "Bug", "--summary", cohort.newSummary + " changed", "--from-md", "new-bug.md"},
	}
	for _, args := range mutations {
		process := startJiraTriagePrimaryProcess(t, root, fixture, cohort, policy)
		if len(args) >= 3 && slices.Equal(args[:3], []string{"jira", "issue", "create"}) {
			_, _ = process.RunSyntheticWriteCLIJSON(t.Context(), args...)
		} else {
			_, _ = process.RunCLIJSON(t.Context(), args...)
		}
		assertJiraTriagePreBackendRefusal(t, process)
	}
	process := startJiraTriagePrimaryProcess(t, root, fixture, cohort, policy)
	if _, err := process.RunSyntheticWriteCLIJSON(t.Context(), triageIssueGetCommand(cohort, cohort.candidates[0].key)...); err == nil {
		t.Fatal("selected triage read was admitted through the synthetic write entry point")
	}
	assertJiraTriagePreBackendRefusal(t, process)

	process = startJiraTriagePrimaryProcess(t, root, fixture, cohort, policy)
	assertJiraSyntheticWriteReadOnlyByDefault(t, process, triageCreateCommand(cohort))
}

func assertJiraTriagePreBackendRefusal(t *testing.T, process *SyntheticATLProcess) {
	t.Helper()
	summary := process.Summary()
	if process.RequestSequenceComplete() || len(summary.HTTPMethods) != 0 || summary.UnexpectedRequests != 0 ||
		summary.DuplicateRequests != 0 || len(summary.CLIInvocations) != 0 || len(summary.MCPInvocations) != 0 {
		t.Fatalf("triage command divergence was not refused pre-backend: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
}

func assertJiraTriageIncompleteSearchIsReadOnly(t *testing.T, fixture MockFixture, cohort triageCohort, policy CLICommandPolicy) {
	t.Helper()
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	for index := range prepared.Routes {
		prepared.Routes[index].closedQuery = true
		if prepared.Routes[index].Name != "search_specific" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal(prepared.Routes[index].Body, &response); err != nil {
			t.Fatal(err)
		}
		response["total"] = 2
		body, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		prepared.Routes[index].Body = body
	}
	prepared.RequestSequence = []string{"search_specific"}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare incomplete triage search fixture: %v", err)
	}
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepared, ScratchRoot: privateSyntheticATLScratch(t), CLIPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close incomplete triage process: %v", err)
		}
	})
	result := callJiraTriageCLIJSON(t, process, triageSearchCommand(cohort, 0))
	list, err := DecodeJiraSnapshotIssueList(bytes.NewReader(result.JSON))
	if err != nil {
		t.Fatal(err)
	}
	complete := JiraSnapshotIssueList{Page: JiraSnapshotIssueListPage{Complete: true}}
	if list.Page.Complete || triageMayWrite([]JiraSnapshotIssueList{list, complete}, []JiraTriageIssueGet{{Key: cohort.candidates[0].key}}) {
		t.Fatal("incomplete selected triage search admitted a write")
	}
	summary := process.Summary()
	if !process.RequestSequenceComplete() || !maps.Equal(summary.HTTPMethods, map[string]int{"GET": 1}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
		!maps.Equal(summary.CLIInvocations, map[string]int{"search_specific": 1}) || len(summary.MCPInvocations) != 0 {
		t.Fatalf("incomplete selected triage search accounting drifted: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
}

const (
	triagePreviewActorName = "synthetic-operator"
	triagePreviewActorKey  = "synthetic-operator"
)

func TestRepositoryJiraTriageHoldoutCurrentCLIGuardedPreviewIsNoWrite(t *testing.T) {
	holdout := triageCohorts[1]
	root := triageRoot(holdout.directory)
	fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
	assertTriageFixtureTopology(t, fixture, holdout)
	policy := triageCodexPolicy(t, root)
	args := triageHoldoutCommentCommand(holdout)

	// The regular admission path remains typed read-only before command-specific
	// input handling, even though the current command default is a preview.
	normal := startJiraTriageHoldoutPreviewProcess(t, root, fixture, holdout, policy)
	assertJiraSyntheticWriteReadOnlyByDefault(t, normal, args)

	process := startJiraTriageHoldoutPreviewProcess(t, root, fixture, holdout, policy)
	workspaceBefore, err := digestWorkspaceTree(process.workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	result, err := process.RunSyntheticWriteCLIJSON(t.Context(), args...)
	if err != nil {
		t.Fatalf("selected holdout guarded preview: %v", err)
	}
	if result.ExitCode != 0 || len(result.JSON) == 0 || len(result.Stderr) != 0 {
		t.Fatalf("selected holdout guarded preview exit=%d stdout_bytes=%d stderr_bytes=%d", result.ExitCode, len(result.JSON), len(result.Stderr))
	}
	preview, err := DecodeJiraTriageCommentPreview(bytes.NewReader(result.JSON))
	if err != nil {
		t.Fatalf("decode selected holdout guarded preview: %v", err)
	}
	body := triageFixtureCommentBody(t, fixture)
	ids := triageFixtureCommentBaselineIDs(t, fixture)
	if preview.Key != holdout.targetKey || preview.Mode != "dry-run" || preview.Status != "would_apply" || !preview.Complete ||
		preview.Body != body || preview.BodySHA256 != triageSHA256([]byte(body)) || preview.BodyBytes != len(body) ||
		preview.Actor != (JiraTriageCommentActor{Name: triagePreviewActorName, Key: triagePreviewActorKey}) ||
		preview.CurrentCount != len(ids) || preview.BaselineSHA256 != triageBaselineSHA256(t, ids) {
		t.Fatalf("selected holdout guarded preview=%+v body=%q baseline_ids=%v", preview, body, ids)
	}
	workspaceAfter, err := digestWorkspaceTree(process.workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if workspaceAfter != workspaceBefore {
		t.Fatal("selected holdout guarded preview modified its reviewed workspace input")
	}

	summary := process.Summary()
	if !process.RequestSequenceComplete() || !maps.Equal(summary.HTTPMethods, map[string]int{"GET": 2}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
		!maps.Equal(summary.CLIInvocations, map[string]int{"comment": 1}) || len(summary.MCPInvocations) != 0 {
		t.Fatalf("selected holdout guarded preview accounting drifted: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
	if _, err := process.RunSyntheticWriteCLIJSON(t.Context(), args...); err == nil {
		t.Fatal("selected holdout guarded preview replay was admitted")
	}
	afterReplay := process.Summary()
	if !maps.Equal(afterReplay.HTTPMethods, summary.HTTPMethods) || afterReplay.UnexpectedRequests != summary.UnexpectedRequests ||
		afterReplay.DuplicateRequests != summary.DuplicateRequests || !maps.Equal(afterReplay.CLIInvocations, summary.CLIInvocations) {
		t.Fatalf("selected holdout guarded preview replay changed evidence: before=%+v after=%+v", summary, afterReplay)
	}
}

func startJiraTriageHoldoutPreviewProcess(
	t *testing.T,
	root string,
	fixture MockFixture,
	cohort triageCohort,
	policy CLICommandPolicy,
) *SyntheticATLProcess {
	t.Helper()
	args := triageHoldoutCommentCommand(cohort)
	match, err := policy.Match(args)
	if err != nil || match.Name != "comment" {
		t.Fatalf("holdout triage comment policy match=%+v err=%v", match, err)
	}
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepareJiraTriageHoldoutPreviewFixture(t, fixture, cohort),
		ScratchRoot: privateSyntheticATLScratch(t), WorkspaceTemplate: filepath.Join(root, "workspace"),
		SyntheticWriteRules: SyntheticWriteRules{"comment"}, CLIPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close selected holdout triage preview process: %v", err)
		}
	})
	return process
}

func prepareJiraTriageHoldoutPreviewFixture(t *testing.T, fixture MockFixture, cohort triageCohort) MockFixture {
	t.Helper()
	if cohort.decision != "comment" || !slices.Equal(fixture.RequestSequence, cohort.sequence) {
		t.Fatalf("holdout triage fixture branch drifted: decision=%q sequence=%v want=%v", cohort.decision, fixture.RequestSequence, cohort.sequence)
	}
	comments := triageRoute(t, fixture, "comments")
	if len(comments.Responses) != 2 {
		t.Fatalf("holdout triage preview needs one retained baseline from two historical responses, got %d", len(comments.Responses))
	}
	comments.Responses = []MockResponse{comments.Responses[0]}
	comments.closedQuery = true
	prepared := fixture
	prepared.Routes = []MockRoute{{
		Name: "current_user", Method: "GET", Path: fixture.JiraContext + "/rest/api/2/myself", Status: 200, closedQuery: true,
		Body: json.RawMessage(`{"name":"synthetic-operator","key":"synthetic-operator","displayName":"Synthetic Operator","active":true}`),
	}, comments}
	prepared.RequestSequence = []string{"current_user", "comments"}
	if slices.Equal(prepared.RequestSequence, cohort.sequence) || len(prepared.RequestSequence) != 2 {
		t.Fatalf("holdout preview sequence accidentally became the retained historical trace: %v", prepared.RequestSequence)
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare holdout triage preview fixture: %v", err)
	}
	return prepared
}

func triageHoldoutCommentCommand(cohort triageCohort) []string {
	return []string{"jira", "issue", "comment", "add", cohort.targetKey, "--from-md", "duplicate-comment.md"}
}

func triageFixtureCommentBody(t *testing.T, fixture MockFixture) string {
	t.Helper()
	var document struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(triageRoute(t, fixture, "comment").RequestBody, &document); err != nil || document.Body == "" {
		t.Fatalf("decode triage comment fixture body: %v", err)
	}
	return document.Body
}

func triageFixtureCommentBaselineIDs(t *testing.T, fixture MockFixture) []string {
	t.Helper()
	route := triageRoute(t, fixture, "comments")
	if len(route.Responses) == 0 {
		t.Fatal("triage comments fixture has no baseline response")
	}
	var document struct {
		Comments []struct {
			ID string `json:"id"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(route.Responses[0].Body, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Comments) == 0 {
		t.Fatal("triage comments fixture baseline is empty")
	}
	ids := make([]string, len(document.Comments))
	seen := make(map[string]struct{}, len(document.Comments))
	for index, comment := range document.Comments {
		if comment.ID == "" {
			t.Fatal("triage comments fixture baseline contains an empty id")
		}
		if _, duplicate := seen[comment.ID]; duplicate {
			t.Fatalf("triage comments fixture baseline contains duplicate id %q", comment.ID)
		}
		seen[comment.ID] = struct{}{}
		ids[index] = comment.ID
	}
	slices.Sort(ids)
	return ids
}

func triageBaselineSHA256(t *testing.T, ids []string) string {
	t.Helper()
	canonical, err := json.Marshal(struct {
		SchemaVersion int      `json:"schema_version"`
		IDs           []string `json:"ids"`
	}{SchemaVersion: 1, IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	return triageSHA256(canonical)
}

func triageSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
