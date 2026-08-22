package agenteval

import (
	"bytes"
	"maps"
	"path/filepath"
	"slices"
	"testing"
)

type jiraSpecBacklogProcessEvidence struct {
	Summary   SyntheticATLProcessSummary
	Exits     []int
	Contracts []CLIErrorContract
	Failed    int
}

func startJiraSpecBacklogProcess(
	t *testing.T,
	root string,
	fixture MockFixture,
	cohort specBacklogCohort,
	policy CLICommandPolicy,
) *SyntheticATLProcess {
	t.Helper()
	writeRules := SyntheticWriteRules{"preview_epic", "create_epic", "preview_child_one", "create_child_one", "link_child_one"}
	if !cohort.holdout {
		writeRules = append(writeRules, "preview_child_two", "create_child_two", "link_child_two")
	}
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepareJiraSpecBacklogProcessFixture(t, fixture, cohort),
		ScratchRoot: privateSyntheticATLScratch(t), WorkspaceTemplate: filepath.Join(root, "workspace"),
		SyntheticWriteRules: writeRules,
		CLIPolicy:           policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Jira spec backlog process: %v", err)
		}
	})
	return process
}

func prepareJiraSpecBacklogProcessFixture(t *testing.T, fixture MockFixture, cohort specBacklogCohort) MockFixture {
	t.Helper()
	prepared := prepareSyntheticJiraGuardedCreate(t, fixture)
	if !slices.Equal(prepared.RequestSequence, cohort.sequence) {
		t.Fatalf("spec backlog guarded fixture shape drifted: routes=%d sequence=%v want=%v", len(prepared.Routes), prepared.RequestSequence, cohort.sequence)
	}
	seen := make(map[string]struct{}, len(cohort.sequence))
	for index := range prepared.Routes {
		prepared.Routes[index].closedQuery = true
		seen[prepared.Routes[index].Name] = struct{}{}
	}
	for _, name := range cohort.sequence {
		if _, found := seen[name]; !found {
			t.Fatalf("spec backlog process fixture omits sequence route %q", name)
		}
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Jira spec backlog fixture: %v", err)
	}
	return prepared
}

func executeJiraSpecBacklogProcess(
	t *testing.T,
	process *SyntheticATLProcess,
	cohort specBacklogCohort,
) jiraSpecBacklogProcessEvidence {
	t.Helper()
	exits := make([]int, 0, len(cohort.exitCodes))
	source := callJiraSpecBacklogSource(t, process, cohort.pageID)
	exits = append(exits, source.ExitCode)
	if source.ExitCode != 0 || len(source.Stderr) != 0 || !bytes.Contains(source.Stdout, []byte(cohort.hostileMarker)) {
		t.Fatalf("selected spec backlog source result=%+v hostile=%q", source, cohort.hostileMarker)
	}

	epic := callJiraSpecBacklogCreate(t, process, cohort, "Epic", cohort.epicSummary, "epic.md")
	exits = append(exits, epic.previewExitCode, epic.applyExitCode)
	if epic.key != cohort.epicKey {
		t.Fatalf("selected spec backlog epic key=%q want=%q", epic.key, cohort.epicKey)
	}
	childOne := callJiraSpecBacklogCreate(t, process, cohort, "Task", cohort.childSummaries[0], "child-1.md")
	exits = append(exits, childOne.previewExitCode, childOne.applyExitCode)
	if childOne.key != cohort.childKeys[0] {
		t.Fatalf("selected spec backlog child one key=%q want=%q", childOne.key, cohort.childKeys[0])
	}

	linkOne, err := process.RunSyntheticWriteCLIJSON(t.Context(), jiraSpecBacklogLinkCommand(childOne.key, epic.key)...)
	if err != nil {
		t.Fatalf("selected spec backlog child one link: %v", err)
	}
	exits = append(exits, linkOne.ExitCode)
	if cohort.holdout {
		contract := assertJiraSyntheticWriteForbidden(t, linkOne, "spec backlog child one link")
		return finishJiraSpecBacklogProcess(t, process, cohort, exits, []CLIErrorContract{contract}, 1)
	}
	assertJiraSpecBacklogLink(t, linkOne, childOne.key, epic.key)

	childTwo := callJiraSpecBacklogCreate(t, process, cohort, "Task", cohort.childSummaries[1], "child-2.md")
	exits = append(exits, childTwo.previewExitCode, childTwo.applyExitCode)
	if childTwo.key != cohort.childKeys[1] {
		t.Fatalf("selected spec backlog child two key=%q want=%q", childTwo.key, cohort.childKeys[1])
	}
	linkTwo, err := process.RunSyntheticWriteCLIJSON(t.Context(), jiraSpecBacklogLinkCommand(childTwo.key, epic.key)...)
	if err != nil {
		t.Fatalf("selected spec backlog child two link: %v", err)
	}
	exits = append(exits, linkTwo.ExitCode)
	assertJiraSpecBacklogLink(t, linkTwo, childTwo.key, epic.key)
	return finishJiraSpecBacklogProcess(t, process, cohort, exits, nil, 0)
}

type jiraSpecBacklogCreatedIssue struct {
	key             string
	previewExitCode int
	applyExitCode   int
}

func callJiraSpecBacklogCreate(
	t *testing.T,
	process *SyntheticATLProcess,
	cohort specBacklogCohort,
	issueType, summary, file string,
) jiraSpecBacklogCreatedIssue {
	t.Helper()
	previewResult, err := process.RunSyntheticWriteCLIJSON(t.Context(), jiraSpecBacklogPreviewCommand(cohort.project, issueType, summary, file)...)
	if err != nil {
		t.Fatalf("selected spec backlog create preview %q: %v", summary, err)
	}
	if previewResult.ExitCode != 0 || len(previewResult.JSON) == 0 || len(previewResult.Stderr) != 0 {
		t.Fatalf("selected spec backlog create preview %q exit=%d stdout_bytes=%d stderr=%s", summary, previewResult.ExitCode, len(previewResult.JSON), previewResult.Stderr)
	}
	preview, err := DecodeJiraGuardedCreateResult(bytes.NewReader(previewResult.JSON))
	if err != nil {
		t.Fatalf("decode selected spec backlog create preview %q: %v", summary, err)
	}
	if preview.Status != "would_apply" || preview.Mode != "preview" || preview.Project.Key != cohort.project || preview.IssueType.Name != issueType || preview.ProposalHash == "" || preview.WriteAttempted {
		t.Fatalf("selected spec backlog create preview %q=%+v", summary, preview)
	}
	applyCommand := jiraSpecBacklogApplyCommand(cohort.project, issueType, summary, file, preview.ProposalHash)
	result, err := process.RunSyntheticWriteCLIJSON(t.Context(), applyCommand...)
	if err != nil {
		t.Fatalf("selected spec backlog create apply %q: %v", summary, err)
	}
	if result.ExitCode != 0 || len(result.JSON) == 0 || len(result.Stderr) != 0 {
		t.Fatalf("selected spec backlog create apply %q exit=%d stdout_bytes=%d stderr_bytes=%d", summary, result.ExitCode, len(result.JSON), len(result.Stderr))
	}
	applied, err := DecodeJiraGuardedCreateResult(bytes.NewReader(result.JSON))
	if err != nil || applied.Status != "applied" || applied.Mode != "apply" || applied.Issue == nil || applied.Issue.Key == "" || !applied.WriteAttempted || !applied.ReadbackReconciled {
		t.Fatalf("decode selected spec backlog create apply %q: result=%+v err=%v", summary, applied, err)
	}
	return jiraSpecBacklogCreatedIssue{key: applied.Issue.Key, previewExitCode: previewResult.ExitCode, applyExitCode: result.ExitCode}
}

func callJiraSpecBacklogSource(t *testing.T, process *SyntheticATLProcess, pageID string) SyntheticCLIBytesResult {
	t.Helper()
	result, err := process.RunCLIBytes(t.Context(), "conf", "page", "view", pageID, "-o", "text")
	if err != nil {
		t.Fatalf("selected spec backlog source: %v", err)
	}
	return result
}

func jiraSpecBacklogPreviewCommand(project, issueType, summary, file string) []string {
	return []string{
		"jira", "issue", "create", "preview", "--project", project, "--type", issueType, "--summary", summary, "--from-md", file,
	}
}

func jiraSpecBacklogApplyCommand(project, issueType, summary, file, proposalHash string) []string {
	return []string{
		"jira", "issue", "create", "--project", project, "--type", issueType, "--summary", summary, "--from-md", file,
		"--apply", "--expected-proposal-hash", proposalHash,
	}
}

func jiraSpecBacklogLinkCommand(issue, epic string) []string {
	return []string{"jira", "issue", "link-epic", issue, "--epic", epic}
}

func assertJiraSpecBacklogLink(t *testing.T, result SyntheticCLIResult, issue, epic string) {
	t.Helper()
	if result.ExitCode != 0 || len(result.JSON) == 0 || len(result.Stderr) != 0 {
		t.Fatalf("selected spec backlog link %q -> %q exit=%d stdout_bytes=%d stderr_bytes=%d", issue, epic, result.ExitCode, len(result.JSON), len(result.Stderr))
	}
	link, err := DecodeJiraEpicLink(bytes.NewReader(result.JSON))
	if err != nil {
		t.Fatalf("decode selected spec backlog link %q -> %q: %v", issue, epic, err)
	}
	if link.Issue != issue || link.Epic != epic || link.Status != "linked" {
		t.Fatalf("selected spec backlog link=%+v want issue=%q epic=%q", link, issue, epic)
	}
}

func finishJiraSpecBacklogProcess(
	t *testing.T,
	process *SyntheticATLProcess,
	cohort specBacklogCohort,
	exits []int,
	contracts []CLIErrorContract,
	failed int,
) jiraSpecBacklogProcessEvidence {
	t.Helper()
	if !slices.Equal(exits, cohort.exitCodes) {
		t.Fatalf("selected spec backlog exits=%v want=%v", exits, cohort.exitCodes)
	}
	summary := process.Summary()
	expectedCLI := map[string]int{
		"source_read": 1, "preview_epic": 1, "create_epic": 1, "preview_child_one": 1, "create_child_one": 1, "link_child_one": 1,
	}
	if !cohort.holdout {
		expectedCLI["preview_child_two"] = 1
		expectedCLI["create_child_two"] = 1
		expectedCLI["link_child_two"] = 1
	}
	if !process.RequestSequenceComplete() || !maps.Equal(summary.HTTPMethods, cohort.methods) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != cohort.duplicates ||
		!maps.Equal(summary.CLIInvocations, expectedCLI) || len(summary.MCPInvocations) != 0 {
		t.Fatalf("selected spec backlog process accounting drifted: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
	if len(contracts) != failed {
		t.Fatalf("selected spec backlog failure contracts=%d want=%d", len(contracts), failed)
	}
	return jiraSpecBacklogProcessEvidence{Summary: summary, Exits: exits, Contracts: contracts, Failed: failed}
}

func assertJiraSpecBacklogProcessAdmissionRefused(
	t *testing.T,
	root string,
	fixture MockFixture,
	cohort specBacklogCohort,
	policy CLICommandPolicy,
) {
	t.Helper()
	mutations := [][]string{
		{"conf", "page", "view", "wrong", "-o", "text"},
		jiraSpecBacklogPreviewCommand(cohort.project, "Epic", "wrong summary", "epic.md"),
		jiraSpecBacklogLinkCommand("WRONG-1", cohort.epicKey),
	}
	for _, args := range mutations {
		process := startJiraSpecBacklogProcess(t, root, fixture, cohort, policy)
		if args[0] == "conf" {
			_, _ = process.RunCLIBytes(t.Context(), args...)
		} else {
			_, _ = process.RunSyntheticWriteCLIJSON(t.Context(), args...)
		}
		assertJiraSpecBacklogPreBackendRefusal(t, process)
	}
	process := startJiraSpecBacklogProcess(t, root, fixture, cohort, policy)
	if _, err := process.RunSyntheticWriteCLIJSON(t.Context(), "conf", "page", "view", cohort.pageID, "-o", "text"); err == nil {
		t.Fatal("read command was admitted through synthetic write entry point")
	}
	assertJiraSpecBacklogPreBackendRefusal(t, process)
}

func assertJiraSpecBacklogPreBackendRefusal(t *testing.T, process *SyntheticATLProcess) {
	t.Helper()
	summary := process.Summary()
	if process.RequestSequenceComplete() || len(summary.HTTPMethods) != 0 || summary.UnexpectedRequests != 0 ||
		summary.DuplicateRequests != 0 || len(summary.CLIInvocations) != 0 || len(summary.MCPInvocations) != 0 {
		t.Fatalf("spec backlog command divergence was not pre-backend: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
}
