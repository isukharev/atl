package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

type jiraInverseReferenceCLIService struct {
	result  *app.JiraInverseReferenceResult
	err     error
	calls   int
	options []app.JiraInverseReferenceOptions
}

func (s *jiraInverseReferenceCLIService) SearchInverseReferences(_ context.Context, opts app.JiraInverseReferenceOptions) (*app.JiraInverseReferenceResult, error) {
	s.calls++
	s.options = append(s.options, opts)
	return s.result, s.err
}

func TestJiraInverseReferenceHelpDescribesBoundedPolicy(t *testing.T) {
	stdout, stderr, code := runCLIFull(t, nil, "jira", "issue", "reference", "search", "--help")
	if code != exitOK || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{
		"Fast may stop early and therefore cannot prove absence.",
		"Exhaustive performs two caller-visible ordered passes",
		"without reading GitLab or discovered URLs",
		"Fields, Development, and Properties are opt-in sources.",
		"--max-response-bytes",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help missing %q:\n%s", want, stdout)
		}
	}
}

func TestJiraInverseReferenceRequiredFlagsFailBeforeConfig(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "issue", "reference", "search"},
		{"jira", "issue", "reference", "search", "--target", "123"},
		{"jira", "issue", "reference", "search", "--target", "123", "--target-kind", "confluence-page"},
		{"jira", "issue", "reference", "search", "--target", "123", "--target-kind", "confluence-page", "--scope-jql", "project = PROJ"},
		{"jira", "issue", "reference", "search", "--target", "123", "--target-kind", "confluence-page", "--scope-jql", "project = PROJ", "--mode", "exhaustive"},
		{"jira", "issue", "reference", "search", "--target", "123", "--target-kind", "confluence-page", "--scope-jql", "project = PROJ", "--mode", "exhaustive", "--sources", "description"},
	} {
		stdout, stderr, code := runCLIFull(t, nil, args...)
		if code != exitUsage || stdout != "" {
			t.Fatalf("args=%v exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
		if strings.Contains(stderr, "config") {
			t.Fatalf("args=%v reached configuration: %q", args, stderr)
		}
	}
}

func TestJiraInverseReferenceRejectsInvalidFlagsBeforeConfig(t *testing.T) {
	base := []string{
		"jira", "issue", "reference", "search",
		"--target", "123", "--target-kind", "confluence-page",
		"--scope-jql", "project = PROJ", "--mode", "exhaustive",
		"--sources", "description", "--max-issues", "1", "--max-requests", "1", "--max-response-bytes", "1",
	}
	for _, extra := range [][]string{
		{"--target-kind", "unknown"},
		{"--mode", "unknown"},
		{"--sources", "unknown"},
		{"--sources", "description,description"},
		{"--sources", "description,"},
		{"--fields", "customfield_10000"},
		{"--sources", "fields"},
		{"--sources", "fields", "--fields", "customfield_10000,customfield_10000"},
		{"--max-issues", "0"},
		{"--max-requests", "0"},
		{"--max-response-bytes", "0"},
	} {
		args := append(append([]string(nil), base...), extra...)
		stdout, stderr, code := runCLIFull(t, nil, args...)
		if code != exitUsage || stdout != "" {
			t.Fatalf("args=%v exit=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
		if strings.Contains(stderr, "config") {
			t.Fatalf("args=%v reached configuration: %q", args, stderr)
		}
	}
}

func TestJiraInverseReferenceFlagMapsAreExactAndStable(t *testing.T) {
	sources, err := inverseReferenceSources([]string{"description,remote-links", "worklogs"})
	if err != nil {
		t.Fatal(err)
	}
	wantSources := []domain.JiraInverseReferenceSource{
		domain.JiraInverseReferenceSourceDescription,
		domain.JiraInverseReferenceSourceRemoteLinks,
		domain.JiraInverseReferenceSourceWorklogs,
	}
	if len(sources) != len(wantSources) {
		t.Fatalf("sources=%v want=%v", sources, wantSources)
	}
	for i := range wantSources {
		if sources[i] != wantSources[i] {
			t.Fatalf("sources=%v want=%v", sources, wantSources)
		}
	}
	if kind, err := inverseReferenceTargetKind("gitlab-project"); err != nil || kind != domain.JiraInverseReferenceTargetGitLabProject {
		t.Fatalf("target kind=%q err=%v", kind, err)
	}
	if mode, err := inverseReferenceMode("fast"); err != nil || mode != domain.JiraInverseReferenceModeFast {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
	fields, err := inverseReferenceFields([]string{"customfield_10000,customfield_20000"})
	if err != nil || strings.Join(fields, ",") != "customfield_10000,customfield_20000" {
		t.Fatalf("fields=%v err=%v", fields, err)
	}
}

func TestJiraInverseReferenceCommandDefaultJSONAndCompleteStrictGolden(t *testing.T) {
	target := "https://target-input.example.invalid/group/repository"
	scope := `project = SAFE AND text ~ "SCOPE_INPUT_NEVER_EMIT"`
	service := &jiraInverseReferenceCLIService{result: jiraInverseReferenceCLICompleteResult()}
	factoryCalls := 0

	args := jiraInverseReferenceCLIArgs(target, scope, "exhaustive", "development")
	args = append(args, "--strict")
	stdout, stderr, err := runJiraInverseReferenceCommand(t, func() (jiraInverseReferenceSearcher, error) {
		factoryCalls++
		return service, nil
	}, args...)
	if jiraInverseReferenceCommandExit(err) != exitOK || stderr != "" {
		t.Fatalf("exit=%d err=%v stderr=%q", jiraInverseReferenceCommandExit(err), err, stderr)
	}
	if factoryCalls != 1 || service.calls != 1 || len(service.options) != 1 {
		t.Fatalf("factory calls=%d service calls=%d options=%d", factoryCalls, service.calls, len(service.options))
	}
	if got := service.options[0]; got.Target != target || got.ScopeJQL != scope || got.Mode != domain.JiraInverseReferenceModeExhaustive {
		t.Fatalf("options=%+v", got)
	}
	assertInverseReferenceValuesAbsent(t, stdout, target, scope)
	assertGolden(t, "jira_issue_reference_search.json", []byte(stdout))
}

func TestJiraInverseReferenceCommandExplicitTextIsEscapedGolden(t *testing.T) {
	target := "https://target-input.example.invalid/group/repository"
	scope := `project = SAFE AND text ~ "SCOPE_INPUT_NEVER_EMIT"`
	result := jiraInverseReferenceCLICompleteResult()
	result.Matches[0].IssueKey = "SAFE|1 <tag>\nnext\\path"
	service := &jiraInverseReferenceCLIService{result: result}

	args := append([]string{"--output", "text"}, jiraInverseReferenceCLIArgs(target, scope, "exhaustive", "development")...)
	stdout, stderr, err := runJiraInverseReferenceCommand(t, func() (jiraInverseReferenceSearcher, error) {
		return service, nil
	}, args...)
	if jiraInverseReferenceCommandExit(err) != exitOK || stderr != "" || service.calls != 1 {
		t.Fatalf("exit=%d err=%v stderr=%q calls=%d", jiraInverseReferenceCommandExit(err), err, stderr, service.calls)
	}
	assertInverseReferenceValuesAbsent(t, stdout, target, scope)
	if !strings.Contains(stdout, `SAFE\|1 &lt;tag&gt; next\\path`) || strings.Contains(stdout, "<tag>") || strings.HasSuffix(stdout, "\n\n") {
		t.Fatalf("text output is not safely escaped and singly terminated: %q", stdout)
	}
	assertGolden(t, "jira_issue_reference_search.txt", []byte(stdout))
}

func TestJiraInverseReferenceCommandStrictEmitsIncompleteResultThenExitsCheckFailure(t *testing.T) {
	target := "https://target-input.example.invalid/group/repository"
	scope := `project = SAFE AND text ~ "SCOPE_INPUT_NEVER_EMIT"`
	service := &jiraInverseReferenceCLIService{result: jiraInverseReferenceCLIIncompleteResult()}
	args := jiraInverseReferenceCLIArgs(target, scope, "fast", "description")
	args = append(args, "--strict")

	stdout, stderr, err := runJiraInverseReferenceCommand(t, func() (jiraInverseReferenceSearcher, error) {
		return service, nil
	}, args...)
	if !errors.Is(err, domain.ErrCheckFailed) || jiraInverseReferenceCommandExit(err) != exitCheckFailed || stderr != "" || service.calls != 1 {
		t.Fatalf("exit=%d err=%v stderr=%q calls=%d stdout=%q", jiraInverseReferenceCommandExit(err), err, stderr, service.calls, stdout)
	}
	var result app.JiraInverseReferenceResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode qualified result: %v (stdout=%q)", decodeErr, stdout)
	}
	if result.Complete || result.Selection.Reason != app.JiraInverseReferenceReasonModeFast {
		t.Fatalf("strict result=%+v", result)
	}
	assertInverseReferenceValuesAbsent(t, stdout+err.Error(), target, scope)
}

func TestJiraInverseReferenceInvalidInputsFailBeforeServiceAndDoNotEchoValues(t *testing.T) {
	validTarget := "https://target-input.example.invalid/group/repository"
	validScope := "project = SAFE"
	tests := []struct {
		name      string
		args      []string
		forbidden []string
	}{
		{
			name: "missing bound",
			args: jiraInverseReferenceCLIArgs(validTarget, validScope, "exhaustive", "development")[:18],
		},
		{
			name:      "target control",
			args:      jiraInverseReferenceCLIArgs("https://target-input.example.invalid/group/repository\nTARGET_PRIVATE_MARKER", validScope, "exhaustive", "development"),
			forbidden: []string{"TARGET_PRIVATE_MARKER", "target-input.example.invalid"},
		},
		{
			name:      "scope order",
			args:      jiraInverseReferenceCLIArgs(validTarget, "project = SAFE ORDER BY SCOPE_PRIVATE_MARKER", "exhaustive", "development"),
			forbidden: []string{"SCOPE_PRIVATE_MARKER", "target-input.example.invalid"},
		},
		{
			name:      "unknown source",
			args:      jiraInverseReferenceCLIArgs(validTarget, validScope, "exhaustive", "SOURCE_PRIVATE_MARKER"),
			forbidden: []string{"SOURCE_PRIVATE_MARKER", "target-input.example.invalid"},
		},
		{
			name:      "invalid field",
			args:      append(jiraInverseReferenceCLIArgs(validTarget, validScope, "exhaustive", "fields"), "--fields", "FIELD PRIVATE MARKER"),
			forbidden: []string{"FIELD PRIVATE MARKER", "target-input.example.invalid"},
		},
		{
			name:      "duplicate field",
			args:      append(jiraInverseReferenceCLIArgs(validTarget, validScope, "exhaustive", "fields"), "--fields", "customfield_private,customfield_private"),
			forbidden: []string{"customfield_private", "target-input.example.invalid"},
		},
		{
			name:      "positional value",
			args:      append(jiraInverseReferenceCLIArgs(validTarget, validScope, "exhaustive", "development"), "POSITIONAL_PRIVATE_MARKER"),
			forbidden: []string{"POSITIONAL_PRIVATE_MARKER", "target-input.example.invalid"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			factoryCalls := 0
			stdout, stderr, err := runJiraInverseReferenceCommand(t, func() (jiraInverseReferenceSearcher, error) {
				factoryCalls++
				return &jiraInverseReferenceCLIService{}, nil
			}, test.args...)
			if !errors.Is(err, domain.ErrUsage) || jiraInverseReferenceCommandExit(err) != exitUsage || stdout != "" || stderr != "" {
				t.Fatalf("exit=%d err=%v stdout=%q stderr=%q", jiraInverseReferenceCommandExit(err), err, stdout, stderr)
			}
			if factoryCalls != 0 {
				t.Fatalf("invalid input constructed service %d time(s)", factoryCalls)
			}
			forbidden := append([]string{validTarget, validScope}, test.forbidden...)
			assertInverseReferenceValuesAbsent(t, err.Error(), forbidden...)
		})
	}
}

func runJiraInverseReferenceCommand(t *testing.T, newService jiraInverseReferenceServiceFactory, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	runtime := &invocationRuntime{outputFormat: "json", processPolicy: newProcessPolicy()}

	root := &cobra.Command{Use: "atl", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVarP(&runtime.outputFormat, "output", "o", "json", "output format: json|text|id")
	root.SetFlagErrorFunc(func(_ *cobra.Command, flagErr error) error {
		return usageErr("%v", flagErr)
	})
	jira := &cobra.Command{Use: "jira"}
	issue := &cobra.Command{Use: "issue"}
	issue.AddCommand(jiraIssueInverseReferenceCmdWithService(newService))
	jira.AddCommand(issue)
	root.AddCommand(jira)
	normalizeArgs(root)

	var stdoutBuffer, stderrBuffer bytes.Buffer
	root.SetArgs(args)
	root.SetOut(&stdoutBuffer)
	root.SetErr(&stderrBuffer)
	err = root.ExecuteContext(context.WithValue(t.Context(), invocationRuntimeContextKey{}, runtime))
	return stdoutBuffer.String(), stderrBuffer.String(), err
}

func jiraInverseReferenceCommandExit(err error) int {
	if err == nil {
		return exitOK
	}
	return codeFor(err)
}

func jiraInverseReferenceCLIArgs(target, scope, mode, source string) []string {
	return []string{
		"jira", "issue", "reference", "search",
		"--target", target,
		"--target-kind", "gitlab-project",
		"--scope-jql", scope,
		"--mode", mode,
		"--sources", source,
		"--max-issues", "10",
		"--max-requests", "10",
		"--max-response-bytes", "65536",
	}
}

func jiraInverseReferenceCLICompleteResult() *app.JiraInverseReferenceResult {
	return &app.JiraInverseReferenceResult{
		SchemaVersion: 1,
		Target: app.JiraInverseReferenceTargetResult{
			Kind:     domain.JiraInverseReferenceTargetGitLabProject,
			OpaqueID: strings.Repeat("a", 64),
		},
		Mode:              domain.JiraInverseReferenceModeExhaustive,
		Sources:           []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceDevelopment},
		EffectiveFieldIDs: []string{},
		TargetResolution:  app.JiraInverseReferencePhase{Complete: true},
		Selection:         app.JiraInverseReferencePhase{Complete: true},
		Verification:      app.JiraInverseReferencePhase{Complete: true},
		Counts: app.JiraInverseReferenceCounts{
			SelectedIssues: 1, CandidateIssues: 1, ScannedIssues: 2,
			VerifiedIssues: 1, MatchedIssues: 1, Matches: 1,
		},
		SourceCounts: []app.JiraInverseReferenceSourceCounts{{
			Source: domain.JiraInverseReferenceSourceDevelopment, Complete: 1,
			Total: 1, Reconciled: true, Reasons: []app.JiraInverseReferenceReasonCount{},
		}},
		Matches: []app.JiraInverseReferenceResultMatch{{
			IssueKey: "IR-41", Relation: app.JiraInverseReferenceRelationDevelopment,
			Direction: app.JiraInverseReferenceDirectionIssueToTarget,
			Source:    domain.JiraInverseReferenceSourceDevelopment, Stability: domain.ArtifactStabilityExperimentalAPI,
			Confidence: "exact", Complete: true,
		}},
		Frontier:       app.JiraInverseReferenceFrontier{Phase: "complete", VerifiedIssues: 1},
		Reconciliation: app.JiraInverseReferenceReconciliation{Counts: true, Sources: true, Matches: true, Usage: true},
		Usage: app.JiraInverseReferenceUsage{
			MaxIssues: 10, MaxRequests: 10, Requests: 4,
			MaxResponseBytes: 65536, ResponseBytes: 1024, Reconciled: true,
		},
		Complete: true,
	}
}

func jiraInverseReferenceCLIIncompleteResult() *app.JiraInverseReferenceResult {
	return &app.JiraInverseReferenceResult{
		SchemaVersion: 1,
		Target: app.JiraInverseReferenceTargetResult{
			Kind:     domain.JiraInverseReferenceTargetGitLabProject,
			OpaqueID: strings.Repeat("b", 64),
		},
		Mode:              domain.JiraInverseReferenceModeFast,
		Sources:           []domain.JiraInverseReferenceSource{domain.JiraInverseReferenceSourceDescription},
		EffectiveFieldIDs: []string{},
		TargetResolution:  app.JiraInverseReferencePhase{Complete: true},
		Selection: app.JiraInverseReferencePhase{
			Reason: app.JiraInverseReferenceReasonModeFast,
		},
		Verification: app.JiraInverseReferencePhase{Complete: true},
		Counts: app.JiraInverseReferenceCounts{
			SelectedIssues: 1, CandidateIssues: 1, ScannedIssues: 1, VerifiedIssues: 1,
		},
		SourceCounts: []app.JiraInverseReferenceSourceCounts{{
			Source: domain.JiraInverseReferenceSourceDescription, Complete: 1,
			Total: 1, Reconciled: true, Reasons: []app.JiraInverseReferenceReasonCount{},
		}},
		Matches:        []app.JiraInverseReferenceResultMatch{},
		Frontier:       app.JiraInverseReferenceFrontier{Phase: "selection", Pass: 1},
		Reconciliation: app.JiraInverseReferenceReconciliation{Counts: true, Sources: true, Matches: true, Usage: true},
		Usage: app.JiraInverseReferenceUsage{
			MaxIssues: 10, MaxRequests: 10, Requests: 2,
			MaxResponseBytes: 65536, ResponseBytes: 512, Reconciled: true,
		},
	}
}

func assertInverseReferenceValuesAbsent(t *testing.T, text string, values ...string) {
	t.Helper()
	for _, value := range values {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("output or error echoed caller-controlled value %q: %s", value, text)
		}
	}
}
