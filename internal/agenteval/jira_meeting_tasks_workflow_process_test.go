package agenteval

import (
	"bytes"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type jiraMeetingTasksProcessEvidence struct {
	Summary   SyntheticATLProcessSummary
	Exits     []int
	Contracts []CLIErrorContract
}

func startJiraMeetingTasksProcess(
	t *testing.T,
	root string,
	fixture MockFixture,
	cohort meetingTasksCohort,
	policy CLICommandPolicy,
) *SyntheticATLProcess {
	t.Helper()
	prepared := prepareJiraMeetingTasksProcessFixture(t, fixture, cohort)
	writeRules := make(SyntheticWriteRules, 0, cohort.writes)
	for _, item := range cohort.items {
		if item.state == "unattempted" {
			break
		}
		for _, command := range [][]string{meetingTasksPreviewCommand(cohort, item), meetingTasksApplyCommand(cohort, item, strings.Repeat("a", 64))} {
			match, err := policy.Match(command)
			if err != nil {
				t.Fatalf("reconcile meeting task %q with CLI policy: %v", item.file, err)
			}
			writeRules = append(writeRules, match.Name)
		}
	}
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: prepared, ScratchRoot: privateSyntheticATLScratch(t),
		WorkspaceTemplate: filepath.Join(root, "workspace"), SyntheticWriteRules: writeRules, CLIPolicy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Jira meeting tasks process: %v", err)
		}
	})
	return process
}

func prepareJiraMeetingTasksProcessFixture(t *testing.T, fixture MockFixture, cohort meetingTasksCohort) MockFixture {
	t.Helper()
	if !slices.Equal(fixture.RequestSequence, cohort.sequence) {
		t.Fatalf("meeting process fixture shape drifted: routes=%d sequence=%v want=%v", len(fixture.Routes), fixture.RequestSequence, cohort.sequence)
	}
	prepared := prepareSyntheticJiraGuardedCreate(t, fixture)
	seen := make(map[string]struct{}, len(cohort.sequence))
	for index := range prepared.Routes {
		prepared.Routes[index].closedQuery = true
		seen[prepared.Routes[index].Name] = struct{}{}
	}
	for _, name := range cohort.sequence {
		if _, found := seen[name]; !found {
			t.Fatalf("meeting process fixture omits sequence route %q", name)
		}
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Jira meeting tasks fixture: %v", err)
	}
	return prepared
}

func executeJiraMeetingTasksProcess(
	t *testing.T,
	process *SyntheticATLProcess,
	cohort meetingTasksCohort,
) jiraMeetingTasksProcessEvidence {
	t.Helper()
	exits := make([]int, 0, len(cohort.sequence))
	source := callJiraMeetingTasksSource(t, process, cohort.pageID)
	exits = append(exits, source.ExitCode)
	if source.ExitCode != 0 || len(source.Stderr) != 0 || !bytes.Contains(source.Stdout, []byte(cohort.hostile)) {
		t.Fatalf("selected meeting source result=%+v hostile=%q", source, cohort.hostile)
	}

	for index, query := range cohort.queries {
		result := callJiraMeetingTasksJSON(t, process, []string{"jira", "user", "search", query, "--limit", "5"})
		exits = append(exits, result.ExitCode)
		users, err := DecodeJiraUserSearch(bytes.NewReader(result.JSON))
		if err != nil {
			t.Fatalf("decode selected meeting user search %q: %v", query, err)
		}
		names := make([]string, len(users.Users))
		for userIndex, user := range users.Users {
			names[userIndex] = user.Name
		}
		if want := meetingTasksResolutionNames(t, cohort.resolutions[index]); !slices.Equal(names, want) {
			t.Fatalf("selected meeting user search %q names=%v want=%v", query, names, want)
		}
	}

	var contracts []CLIErrorContract
	for _, item := range cohort.items {
		if item.state == "unattempted" {
			break
		}
		previewResult, err := process.RunSyntheticWriteCLIJSON(t.Context(), meetingTasksPreviewCommand(cohort, item)...)
		if err != nil {
			t.Fatalf("selected meeting create preview %q: %v", item.summary, err)
		}
		exits = append(exits, previewResult.ExitCode)
		preview, decodeErr := DecodeJiraGuardedCreateResult(bytes.NewReader(previewResult.JSON))
		if decodeErr != nil || previewResult.ExitCode != 0 || len(previewResult.Stderr) != 0 || preview.Status != "would_apply" || preview.Mode != "preview" {
			t.Fatalf("selected meeting create preview %q=%+v exit=%d stderr=%q err=%v", item.summary, preview, previewResult.ExitCode, previewResult.Stderr, decodeErr)
		}
		result, err := process.RunSyntheticWriteCLIJSON(t.Context(), meetingTasksApplyCommand(cohort, item, preview.ProposalHash)...)
		if err != nil {
			t.Fatalf("selected meeting create apply %q: %v", item.summary, err)
		}
		exits = append(exits, result.ExitCode)
		if item.state == "failed" {
			contracts = append(contracts, assertJiraSyntheticWriteForbidden(t, result, "meeting create"))
			break
		}
		if result.ExitCode != 0 || len(result.JSON) == 0 || len(result.Stderr) != 0 {
			t.Fatalf("selected meeting create %q exit=%d stdout_bytes=%d stderr=%q", item.summary, result.ExitCode, len(result.JSON), result.Stderr)
		}
		created, decodeErr := DecodeJiraGuardedCreateResult(bytes.NewReader(result.JSON))
		if decodeErr != nil {
			t.Fatalf("decode selected meeting create %q: %v", item.summary, decodeErr)
		}
		if created.Status != "applied" || created.Mode != "apply" || created.Issue == nil || created.Issue.Key != item.key ||
			created.Project.Key != cohort.project || created.IssueType.Name != "Task" || !created.ReadbackReconciled {
			t.Fatalf("selected meeting create=%+v want key=%q project=%q", created, item.key, cohort.project)
		}
	}

	if !slices.Equal(exits, cohort.exitCodes) {
		t.Fatalf("selected meeting exits=%v want=%v", exits, cohort.exitCodes)
	}
	summary := process.Summary()
	expectedCLI := map[string]int{"source_read": 1}
	for _, name := range cohort.sequence {
		if strings.HasPrefix(name, "user_") {
			expectedCLI[name]++
		}
	}
	for _, item := range cohort.items {
		if item.state == "unattempted" {
			break
		}
		for _, command := range [][]string{meetingTasksPreviewCommand(cohort, item), meetingTasksApplyCommand(cohort, item, strings.Repeat("a", 64))} {
			match, err := process.config.CLIPolicy.Match(command)
			if err != nil {
				t.Fatal(err)
			}
			expectedCLI[match.Name]++
		}
	}
	if !process.RequestSequenceComplete() || !maps.Equal(summary.HTTPMethods, cohort.methods) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != cohort.duplicates ||
		!maps.Equal(summary.CLIInvocations, expectedCLI) || len(summary.MCPInvocations) != 0 {
		t.Fatalf("selected meeting process accounting drifted: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
	if len(contracts) != cohort.failed {
		t.Fatalf("selected meeting failure contracts=%d want=%d", len(contracts), cohort.failed)
	}
	return jiraMeetingTasksProcessEvidence{Summary: summary, Exits: exits, Contracts: contracts}
}

func callJiraMeetingTasksSource(t *testing.T, process *SyntheticATLProcess, pageID string) SyntheticCLIBytesResult {
	t.Helper()
	result, err := process.RunCLIBytes(t.Context(), "conf", "page", "view", pageID, "-o", "text")
	if err != nil {
		t.Fatalf("selected meeting source: %v", err)
	}
	return result
}

func callJiraMeetingTasksJSON(t *testing.T, process *SyntheticATLProcess, args []string) SyntheticCLIResult {
	t.Helper()
	result, err := process.RunCLIJSON(t.Context(), args...)
	if err != nil {
		t.Fatalf("selected meeting command %v: %v", args, err)
	}
	if result.ExitCode != 0 || len(result.JSON) == 0 || len(result.Stderr) != 0 {
		t.Fatalf("selected meeting command %v exit=%d stdout_bytes=%d stderr_bytes=%d", args, result.ExitCode, len(result.JSON), len(result.Stderr))
	}
	return result
}

func meetingTasksResolutionNames(t *testing.T, resolution map[string]any) []string {
	t.Helper()
	value, ok := resolution["candidate_usernames"].([]string)
	if !ok {
		t.Fatalf("meeting resolution candidate_usernames=%T", resolution["candidate_usernames"])
	}
	return value
}

func meetingTasksCreateCommand(cohort meetingTasksCohort, item meetingTaskItem) []string {
	args := []string{
		"jira", "issue", "create", "--project", cohort.project, "--type", "Task", "--summary", item.summary, "--from-md", item.file,
	}
	if item.assignee != "" {
		args = append(args, "--field", `assignee={"name":"`+item.assignee+`"}`)
	}
	if item.due != "" {
		args = append(args, "--field", "duedate="+item.due)
	}
	return args
}

func meetingTasksPreviewCommand(cohort meetingTasksCohort, item meetingTaskItem) []string {
	args := meetingTasksCreateCommand(cohort, item)
	return append(append(slices.Clone(args[:3]), "preview"), args[3:]...)
}

func meetingTasksApplyCommand(cohort meetingTasksCohort, item meetingTaskItem, proposalHash string) []string {
	return append(meetingTasksCreateCommand(cohort, item), "--apply", "--expected-proposal-hash", proposalHash)
}

func assertJiraSyntheticWriteForbidden(t *testing.T, result SyntheticCLIResult, operation string) CLIErrorContract {
	t.Helper()
	if result.ExitCode != 6 || len(result.JSON) != 0 || len(result.Stderr) == 0 {
		t.Fatalf("selected %s exit=%d stdout_bytes=%d stderr_bytes=%d, want typed exit 6", operation, result.ExitCode, len(result.JSON), len(result.Stderr))
	}
	contract, ok := ParseCLIErrorContract(result.ExitCode, result.Stderr)
	if !ok || contract != (CLIErrorContract{ExitCode: 6, Kind: "forbidden", Remediation: "request_access"}) {
		t.Fatalf("selected %s contract=%+v classified=%t", operation, contract, ok)
	}
	return contract
}

func assertJiraMeetingTasksProcessAdmissionRefused(
	t *testing.T,
	root string,
	fixture MockFixture,
	cohort meetingTasksCohort,
	policy CLICommandPolicy,
) {
	t.Helper()
	project, user, summary := cohort.project, "arivera", "Prepare release checklist"
	if cohort.directory == meetingTasksHoldoutDirectory {
		user, summary = "rchen", "Confirm archive policy"
	}
	mutations := [][]string{
		{"jira", "user", "search", "Wrong User", "--limit", "5"},
		{"jira", "issue", "create", "--project", project, "--type", "Task", "--summary", summary, "--from-md", "wrong.md"},
		{"jira", "issue", "create", "--project", "WRONG", "--type", "Task", "--summary", summary, "--from-md", "item-1.md"},
		{"jira", "issue", "create", "--project", project, "--type", "Task", "--summary", summary, "--from-md", "item-1.md", "--field", `assignee={"name":"` + user + `"}`},
	}
	for _, args := range mutations {
		process := startJiraMeetingTasksProcess(t, root, fixture, cohort, policy)
		if len(args) >= 3 && slices.Equal(args[:3], []string{"jira", "issue", "create"}) {
			_, _ = process.RunSyntheticWriteCLIJSON(t.Context(), args...)
		} else {
			_, _ = process.RunCLIJSON(t.Context(), args...)
		}
		assertJiraMeetingTasksPreBackendRefusal(t, process)
	}
	process := startJiraMeetingTasksProcess(t, root, fixture, cohort, policy)
	if _, err := process.RunSyntheticWriteCLIJSON(t.Context(), "conf", "page", "view", cohort.pageID, "-o", "text"); err == nil {
		t.Fatal("read command was admitted through synthetic write entry point")
	}
	assertJiraMeetingTasksPreBackendRefusal(t, process)

	var attempted meetingTaskItem
	for _, item := range cohort.items {
		if item.state != "unattempted" {
			attempted = item
			break
		}
	}
	process = startJiraMeetingTasksProcess(t, root, fixture, cohort, policy)
	assertJiraSyntheticWriteReadOnlyByDefault(t, process, meetingTasksApplyCommand(cohort, attempted, strings.Repeat("a", 64)))
}

func assertJiraSyntheticWriteReadOnlyByDefault(t *testing.T, process *SyntheticATLProcess, args []string) {
	t.Helper()
	match, err := process.config.CLIPolicy.Match(args)
	if err != nil {
		t.Fatalf("match normal synthetic write command: %v", err)
	}
	result, err := process.RunCLIJSON(t.Context(), args...)
	if err != nil {
		t.Fatalf("run normal synthetic write command: %v", err)
	}
	contract, ok := ParseCLIErrorContract(result.ExitCode, result.Stderr)
	if result.ExitCode != 8 || len(result.JSON) != 0 || !ok || contract != (CLIErrorContract{
		ExitCode: 8, Kind: "read_only_policy", Remediation: "request_human_approval",
	}) {
		t.Fatalf("normal synthetic write result=%+v contract=%+v classified=%t", result, contract, ok)
	}
	if _, replayErr := process.RunSyntheticWriteCLIJSON(t.Context(), args...); replayErr == nil {
		t.Fatal("normal read-only attempt did not consume the one-shot CLI budget")
	}
	summary := process.Summary()
	if process.RequestSequenceComplete() || len(summary.HTTPMethods) != 0 || summary.UnexpectedRequests != 0 ||
		summary.DuplicateRequests != 0 || !maps.Equal(summary.CLIInvocations, map[string]int{match.Name: 1}) ||
		len(summary.MCPInvocations) != 0 {
		t.Fatalf("normal synthetic write reached the backend or replayed: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
}

func assertJiraMeetingTasksPreBackendRefusal(t *testing.T, process *SyntheticATLProcess) {
	t.Helper()
	summary := process.Summary()
	if process.RequestSequenceComplete() || len(summary.HTTPMethods) != 0 || summary.UnexpectedRequests != 0 ||
		summary.DuplicateRequests != 0 || len(summary.CLIInvocations) != 0 || len(summary.MCPInvocations) != 0 {
		t.Fatalf("meeting command divergence was not pre-backend: summary=%+v sequence_complete=%t", summary, process.RequestSequenceComplete())
	}
}
