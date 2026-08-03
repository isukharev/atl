package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func TestCommandRegistryExactlyMatchesTree(t *testing.T) {
	if commandRegistryErr != nil {
		t.Fatal(commandRegistryErr)
	}
	root := newRoot()
	seen := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		path := commandRegistryPath(root, cmd)
		registration, registered := commandRegistry.nodes[path]
		if !registered {
			t.Errorf("constructed command %q is absent from registry", cmd.CommandPath())
		} else {
			seen[path] = true
			wantRole := map[commandTrait]string{
				commandGroup: commandRoleGroup, commandLeaf: commandRoleLeaf,
				commandGroup | commandLeaf: commandRoleHybrid,
			}[registration.traits&(commandGroup|commandLeaf)]
			if got := cmd.Annotations[commandRoleAnnotation]; got != wantRole {
				t.Errorf("%s role=%q want=%q", cmd.CommandPath(), got, wantRole)
			}
		}
		if registered && registration.traits&commandLeaf != 0 {
			access := cmd.Annotations[accessAnnotation]
			if access != "read-only" && access != "mutating" {
				t.Errorf("%s access=%q", cmd.CommandPath(), access)
			}
			if (access == "mutating") != (registration.traits&commandMutating != 0) {
				t.Errorf("%s mutation classification drift", cmd.CommandPath())
			}
			textSupport := cmd.Annotations[textOutputAnnotation]
			if textSupport != "supported" && textSupport != "unsupported" {
				t.Errorf("%s text output=%q", cmd.CommandPath(), textSupport)
			}
			idSupport := cmd.Annotations[idOutputAnnotation]
			if idSupport != "supported" && idSupport != "unsupported" {
				t.Errorf("%s id output=%q", cmd.CommandPath(), idSupport)
			}
			if registration.traits&commandMutating != 0 {
				if got := mutationProfile(cmd.Annotations[mutationProfileAnnotation]); got != registration.profile || !validMutationProfile(got) {
					t.Errorf("%s mutation profile=%q want=%q", cmd.CommandPath(), got, registration.profile)
				}
				for _, flag := range mutationGuardRequirementNames(registration.guard.requirements) {
					if cmd.Flags().Lookup(flag) == nil {
						t.Errorf("%s profile=%q missing structural --%s", cmd.CommandPath(), registration.profile, flag)
					}
				}
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
	for path := range commandRegistry.nodes {
		if !seen[path] {
			t.Errorf("registry command %q is no longer constructed", path)
		}
	}
}

func TestMutationProfileShapesAreEnforced(t *testing.T) {
	for name, row := range map[string]string{
		"preview without apply":   "M preview-apply expected-proposal-hash command generic json unsafe",
		"dedicated without guard": "M dedicated-apply - json unsafe",
		"plan without guard":      "M plan - json unsafe",
		"direct with guard":       "M remote-direct confirm command generic json unsafe",
		"invalid requirement":     "M preview-apply apply,unknown command generic json unsafe",
		"invalid phase":           "M preview-apply apply unknown generic json unsafe",
		"invalid family":          "M preview-apply apply command unknown json unsafe",
		"special command phase":   "M preview-apply apply command confluence-page-delete json unsafe",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCommandRegistry(row); err == nil {
				t.Fatalf("parseCommandRegistry(%q) succeeded", row)
			}
		})
	}
}

func TestMutationRegistryPreservesReviewedAccessSet(t *testing.T) {
	type expectedMutation struct {
		profile mutationProfile
		guards  []string
	}
	want := map[string]expectedMutation{}
	for _, line := range strings.Split(`
local-direct|-|auth login
local-direct|-|auth logout
local-direct|-|conf apply
preview-apply|apply,confirm,expected-proposal-hash,expected-version|conf attachment delete
remote-direct|-|conf attachment upload
remote-direct|-|conf blog create
preview-apply|apply,expected-proposal-hash|conf comment add
dedicated-apply|apply,expected-proposal-hash|conf comment mutation apply
local-direct|-|conf edit
preview-apply|apply,expected-proposal-hash,expected-version|conf page copy
remote-direct|-|conf page create
preview-apply|apply,confirm,expected-proposal-hash,expected-version|conf page delete
preview-apply|apply,expected-proposal-hash|conf page labels add
preview-apply|apply,expected-proposal-hash|conf page labels remove
preview-apply|apply,expected-proposal-hash,expected-version,expected-parent|conf page move
preview-apply|apply,expected-proposal-hash,expected-version|conf page title set
plan|confirm,expected-proposal-hash|conf plan apply
remote-direct|-|conf push
local-direct|-|conf reconcile stage
local-direct|-|compatibility clear
local-direct|-|compatibility pin
local-direct|-|config set
local-direct|-|jira apply
remote-direct|-|jira issue assign
remote-direct|-|jira issue attachment upload
preview-apply|apply,expected-proposal-hash|jira issue comment add
remote-direct|-|jira issue comment delete
remote-direct|-|jira issue create
preview-apply|apply,confirm,expected-proposal-hash,expected-updated|jira issue delete
remote-direct|-|jira issue edit
preview-apply|apply,expected-proposal-hash,expected-updated|jira issue field set
remote-direct|-|jira issue labels
remote-direct|-|jira issue link add
remote-direct|-|jira issue link delete
remote-direct|-|jira issue link-epic
plan|apply,confirm|jira issue plan apply
preview-apply|apply,expected-proposal-hash|jira issue transition
remote-direct|-|jira issue update
preview-apply|apply,expected-proposal-hash|jira issue watchers add
preview-apply|apply,expected-proposal-hash|jira issue watchers remove
preview-apply|apply,expected-proposal-hash|jira issue worklog add
preview-apply|apply|jira push
local-direct|-|jira reconcile stage
remote-direct|-|jira sprint add
remote-direct|-|jira sprint remove
preview-apply|apply,expected-backend-sha256,confirm|mirror backend bind
dedicated-apply|from-file,candidate-hash,expected-current-hash|profile apply
local-direct|-|profile revalidate
local-direct|-|profile suggest
dedicated-apply|from-file,suggestion-hash,candidate-hash,expected-current-hash|profile suggestion apply
local-direct|-|profile suggestion reject
`, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			fields := strings.Split(line, "|")
			if len(fields) != 3 {
				t.Fatalf("invalid reviewed mutation row %q", line)
			}
			var guards []string
			if fields[1] != "-" {
				guards = strings.Split(fields[1], ",")
			}
			want[fields[2]] = expectedMutation{profile: mutationProfile(fields[0]), guards: guards}
		}
	}

	root := newRoot()
	seen := map[string]bool{}
	standardGuardNames := []string{
		"apply", "confirm", "expected-proposal-hash", "expected-version", "expected-parent",
		"expected-updated", "expected-backend-sha256", "candidate-hash", "expected-current-hash",
	}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Annotations[commandRoleAnnotation] == commandRoleLeaf || cmd.Annotations[commandRoleAnnotation] == commandRoleHybrid {
			path := commandRegistryPath(root, cmd)
			if cmd.Annotations[accessAnnotation] == "mutating" {
				seen[path] = true
				expected, ok := want[path]
				if !ok {
					t.Errorf("executable command %q unexpectedly became mutating", path)
				} else {
					if got := mutationProfile(cmd.Annotations[mutationProfileAnnotation]); got != expected.profile {
						t.Errorf("reviewed mutating command %q profile=%q want=%q", path, got, expected.profile)
					}
					wantGuard := map[string]bool{}
					for _, name := range expected.guards {
						wantGuard[name] = true
						if cmd.Flags().Lookup(name) == nil {
							t.Errorf("reviewed mutating command %q lost structural --%s", path, name)
						}
					}
					for _, name := range standardGuardNames {
						if present := cmd.Flags().Lookup(name) != nil; present != wantGuard[name] {
							t.Errorf("reviewed mutating command %q structural --%s presence=%t want=%t", path, name, present, wantGuard[name])
						}
					}
				}
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
	inventory, err := RepositoryCommandInventory()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range inventory {
		expected, ok := want[command.Path]
		if ok && !reflect.DeepEqual(command.RequiredFlags, expected.guards) {
			t.Errorf("reviewed mutating command %q RequiredFlags=%v want=%v", command.Path, command.RequiredFlags, expected.guards)
		}
	}
	for path := range want {
		if !seen[path] {
			t.Errorf("reviewed mutating command %q lost its executable classification", path)
		}
	}
	if len(seen) != 51 {
		t.Fatalf("executable mutating commands=%d want=51", len(seen))
	}
}

func TestPersistentPreRunCollisionPrecedenceIsStable(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	brokenConfig := map[string]string{"ATL_CONFIG_DIR": configDir}
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want error
	}{
		{
			name: "unsupported output precedes explicit read-only",
			args: []string{"--read-only", "--output", "id", "jira", "issue", "transition", "PROJ-1", "--to", "Done"},
			want: domain.ErrUsage,
		},
		{
			name: "explicit read-only precedes malformed apply guards",
			args: []string{"--read-only", "jira", "issue", "delete", "PROJ-1", "--apply"},
			want: domain.ErrCheckFailed,
		},
		{
			name: "apply guards precede malformed config",
			env:  brokenConfig,
			args: []string{"conf", "comment", "mutation", "apply", "--id", "1", "--thread-id", "2", "--operation", "resolve"},
			want: domain.ErrUsage,
		},
		{
			name: "malformed config precedes command input and service",
			env:  brokenConfig,
			args: []string{"jira", "issue", "create", "--project", "PROJ", "--type", "Task", "--summary", "Synthetic", "--from-file", "/definitely/missing/description.wiki"},
			want: domain.ErrConfig,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := executeCLIRaw(t, test.env, test.args...)
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want classification %v", err, test.want)
			}
		})
	}
}

func TestPureGroupsHelpAndRejectUnknownTokens(t *testing.T) {
	var groups []string
	for path, registration := range commandRegistry.nodes {
		if registration.traits&(commandGroup|commandLeaf) == commandGroup {
			groups = append(groups, path)
		}
	}
	sort.Strings(groups)
	for index, path := range groups {
		name := path
		if name == "" {
			name = "root"
		}
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			args := strings.Fields(path)
			stdout, _, err := executeCLIRaw(t, nil, args...)
			if err != nil || !strings.Contains(stdout, "Usage:") {
				t.Fatalf("zero-arg group help err=%v stdout=%q", err, stdout)
			}

			unknown := fmt.Sprintf("__atl_unknown_%d__", index)
			unknownArgs := append(append([]string(nil), args...), unknown)
			stdout, _, err = executeCLIRaw(t, nil, unknownArgs...)
			if !errors.Is(err, domain.ErrUsage) || codeFor(err) != exitUsage || stdout != "" {
				t.Fatalf("unknown group token err=%v code=%d stdout=%q", err, codeFor(err), stdout)
			}
			var rendered bytes.Buffer
			writeError(&rendered, "json", err, codeFor(err))
			var body map[string]any
			if decodeErr := json.Unmarshal(rendered.Bytes(), &body); decodeErr != nil || body["code"] != float64(exitUsage) || body["kind"] != "usage_error" {
				t.Fatalf("unknown JSON=%s decodeErr=%v", rendered.String(), decodeErr)
			}
		})
	}
}

func TestPureGroupHelpBypassesConfigAndAccessSetup(t *testing.T) {
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": cfgDir}, "conf", "page")
	if code != exitOK || !strings.Contains(stdout, "Usage:") {
		t.Fatalf("group help exit=%d stdout=%q", code, stdout)
	}
	if _, code = runCLI(t, nil, "--output", "invalid", "conf", "page"); code != exitUsage {
		t.Fatalf("invalid global output on group exit=%d, want %d", code, exitUsage)
	}
}

func TestDedicatedApplyAndBackendBindMissingEvidenceAreUsageErrors(t *testing.T) {
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	brokenConfig := map[string]string{"ATL_CONFIG_DIR": cfgDir}
	for name, args := range map[string][]string{
		"dedicated route without apply": {"conf", "comment", "mutation", "apply", "--id", "1", "--thread-id", "2", "--operation", "resolve"},
		"dedicated route without hash":  {"conf", "comment", "mutation", "apply", "--id", "1", "--thread-id", "2", "--operation", "resolve", "--apply"},
		"profile apply without inputs":  {"profile", "apply"},
		"suggestion apply without inputs": {
			"profile", "suggestion", "apply",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := executeCLIRaw(t, brokenConfig, args...)
			if !errors.Is(err, domain.ErrUsage) || codeFor(err) != exitUsage {
				t.Fatalf("err=%v code=%d", err, codeFor(err))
			}
		})
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	brokenBindConfig := map[string]string{"ATL_CONFIG_DIR": cfgDir, "ATL_JIRA_URL": "https://jira.example.test"}
	_, _, err := executeCLIRaw(t, brokenBindConfig, "mirror", "backend", "bind", root, "--service", "jira", "--apply", "--confirm", "BIND")
	if !errors.Is(err, domain.ErrUsage) || codeFor(err) != exitUsage {
		t.Fatalf("missing bind hash err=%v code=%d", err, codeFor(err))
	}
	env := map[string]string{"ATL_JIRA_URL": "https://jira.example.test"}
	_, _, err = executeCLIRaw(t, env, "mirror", "backend", "bind", root, "--service", "jira", "--apply",
		"--expected-backend-sha256", "sha256:"+strings.Repeat("0", 64), "--confirm", "BIND")
	if !errors.Is(err, domain.ErrCheckFailed) || codeFor(err) != exitCheckFailed {
		t.Fatalf("supplied stale bind evidence err=%v code=%d", err, codeFor(err))
	}
}

func TestReadOnlyFlagBlocksMutationBeforeNetwork(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer srv.Close()
	_, code := runCLI(t, jiraEnv(srv), "--read-only", "jira", "issue", "create", "--project", "PROJ", "--type", "Task", "--summary", "blocked", "--from-file", "/definitely/missing/description.wiki")
	if code != exitCheckFailed || requests != 0 {
		t.Fatalf("exit=%d requests=%d", code, requests)
	}
}

func TestReadOnlyPolicyAllowsBackendRead(t *testing.T) {
	srv := jsonServer(t, http.StatusOK, `{"issues":[],"total":0}`)
	defer srv.Close()
	if _, code := runCLI(t, jiraEnv(srv), "--read-only", "jira", "issue", "search", "--jql", "project=PROJ"); code != exitOK {
		t.Fatalf("read exit=%d", code)
	}
}

func TestReconcilePreviewAndStageHaveDistinctAccessPolicies(t *testing.T) {
	root := newRoot()
	for _, service := range []string{"conf", "jira"} {
		preview, _, err := root.Find([]string{service, "reconcile", "preview"})
		if err != nil || preview.Annotations[accessAnnotation] != "read-only" {
			t.Fatalf("%s preview access=%q err=%v", service, preview.Annotations[accessAnnotation], err)
		}
		stage, _, err := root.Find([]string{service, "reconcile", "stage"})
		if err != nil || stage.Annotations[accessAnnotation] != "mutating" {
			t.Fatalf("%s stage access=%q err=%v", service, stage.Annotations[accessAnnotation], err)
		}
		if _, code := runCLI(t, map[string]string{"ATL_READ_ONLY": "1"}, service, "reconcile", "stage", "/definitely/missing.native"); code != exitCheckFailed {
			t.Fatalf("%s stage under read-only policy exit=%d", service, code)
		}
	}
}

func TestJiraCommentPreviewIsReadOnlyAndAddRemainsMutating(t *testing.T) {
	root := newRoot()
	preview, _, err := root.Find([]string{"jira", "issue", "comment", "preview"})
	if err != nil || preview.Annotations[accessAnnotation] != "read-only" {
		t.Fatalf("preview access=%q err=%v", preview.Annotations[accessAnnotation], err)
	}
	add, _, err := root.Find([]string{"jira", "issue", "comment", "add"})
	if err != nil || add.Annotations[accessAnnotation] != "mutating" {
		t.Fatalf("add access=%q err=%v", add.Annotations[accessAnnotation], err)
	}

	server := newJiraCommentCLIServer(t)
	previewOut, previewCode := runCLI(t, jiraEnv(server.srv), "--read-only", "jira", "issue", "comment", "preview", "PROJ-1",
		"--from-file", writeCommentBody(t, "reviewed body"))
	if previewCode != exitOK || !strings.Contains(previewOut, `"status": "would_apply"`) || server.post != 0 || server.list != 1 || server.myself != 1 {
		t.Fatalf("read-only preview exit=%d requests=myself:%d list:%d post:%d out=%q",
			previewCode, server.myself, server.list, server.post, previewOut)
	}

	requests := 0
	blockedServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer blockedServer.Close()
	if _, code := runCLI(t, jiraEnv(blockedServer), "--read-only", "jira", "issue", "comment", "add", "PROJ-1",
		"--from-file", "/definitely/missing/comment.wiki"); code != exitCheckFailed || requests != 0 {
		t.Fatalf("read-only add exit=%d requests=%d", code, requests)
	}
}

func TestJiraTransitionPreviewIsReadOnlyAndParentRemainsMutating(t *testing.T) {
	root := newRoot()
	preview, _, err := root.Find([]string{"jira", "issue", "transition", "preview"})
	if err != nil || preview.Annotations[accessAnnotation] != "read-only" {
		t.Fatalf("preview access=%q err=%v", preview.Annotations[accessAnnotation], err)
	}
	transition, _, err := root.Find([]string{"jira", "issue", "transition"})
	if err != nil || transition.Annotations[accessAnnotation] != "mutating" {
		t.Fatalf("transition access=%q err=%v", transition.Annotations[accessAnnotation], err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	if _, code := runCLI(t, jiraEnv(server), "--read-only", "jira", "issue", "transition", "PROJ-1", "--to", "Done"); code != exitCheckFailed || requests != 0 {
		t.Fatalf("read-only transition exit=%d requests=%d", code, requests)
	}
}

func TestReadOnlyEnvironmentAndConfigCannotBeDowngradedByFalseFlag(t *testing.T) {
	if _, code := runCLI(t, map[string]string{"ATL_READ_ONLY": "1"}, "jira", "issue", "delete", "PROJ-1",
		"--apply", "--confirm", "DELETE", "--expected-updated", "2026-08-02T20:00:00.000+0000", "--expected-proposal-hash", strings.Repeat("a", 64)); code != exitCheckFailed {
		t.Fatalf("env policy exit=%d", code)
	}

	t.Setenv("ATL_NO_UPDATE", "1")
	t.Setenv("ATL_READ_ONLY", "")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	if err := config.Save(&config.Config{ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	root := newRoot()
	root.SetArgs([]string{"--read-only=false", "config", "set", "safety.read_only", "false"})
	if err := root.ExecuteContext(context.Background()); codeFor(err) != exitCheckFailed {
		t.Fatalf("config policy error=%v code=%d", err, codeFor(err))
	}
}

func TestUnclassifiedCommandFailsClosed(t *testing.T) {
	cmd := &cobra.Command{Use: "future", Annotations: map[string]string{accessAnnotation: "unclassified"}}
	err := enforceAccessPolicy(cmd, false)
	if codeFor(err) != exitCheckFailed {
		t.Fatalf("error=%v code=%d", err, codeFor(err))
	}
	if kind, remediation := classifyError(err); kind != "internal_error" || remediation != "report_bug" {
		t.Fatalf("classification=%q/%q", kind, remediation)
	}
}

func TestCobraHelpAndCompletionBuiltinsRemainReadOnly(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		want          string
		captureStdout bool
	}{
		{"help", []string{"help"}, "Usage:", false},
		{"nested_help", []string{"help", "jira"}, "Jira: read/search/pull", false},
		{"completion_script", []string{"--read-only", "completion", "bash"}, "__start_atl", true},
		{"hidden_completion", []string{"--read-only", cobra.ShellCompRequestCmd, "jira", "iss"}, ":", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out string
			var code int
			if tt.captureStdout {
				read, write, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				original := os.Stdout
				os.Stdout = write
				_, code = runCLI(t, nil, tt.args...)
				_ = write.Close()
				os.Stdout = original
				captured, readErr := io.ReadAll(read)
				_ = read.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				out = string(captured)
			} else {
				out, code = runCLI(t, nil, tt.args...)
			}
			if code != exitOK || !strings.Contains(out, tt.want) {
				t.Fatalf("exit=%d output=%q, want %q", code, out, tt.want)
			}
		})
	}
}

func TestReadOnlyRefusalHasStableJSONMetadata(t *testing.T) {
	var output bytes.Buffer
	writeError(&output, "json", &readOnlyPolicyError{Command: "atl jira push"}, exitCheckFailed)
	var body map[string]any
	if err := json.Unmarshal(output.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["policy"] != "read_only" || body["command"] != "atl jira push" || body["code"] != float64(exitCheckFailed) || body["kind"] != "read_only_policy" || body["remediation"] != "request_human_approval" {
		t.Fatalf("body=%v", body)
	}
	recovery, ok := body["recovery"].(map[string]any)
	if !ok || recovery["schema_version"] != float64(1) || recovery["action"] != "request_human_approval" || recovery["retry_safe"] != false {
		t.Fatalf("recovery=%v", body["recovery"])
	}
}

func TestMalformedConfigKeepsOfflineDiagnosticsButBlocksWritesAndOnlineReads(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	malformed := []byte(`{"read_only":`)
	if err := os.WriteFile(cfgPath, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ATL_CONFIG_DIR": cfgDir}
	for _, args := range [][]string{{"version"}, {"help"}, {"profile", "show"}} {
		if _, code := runCLI(t, env, args...); code != exitOK {
			t.Errorf("%v exit=%d, want diagnostic success", args, code)
		}
	}
	if _, code := runCLI(t, env, "config", "show"); code != exitConfig {
		t.Fatalf("config show exit=%d, want explicit malformed-config diagnosis", code)
	}
	if _, code := runCLI(t, env, "config", "set", "safety.read_only", "false"); code != exitConfig {
		t.Fatalf("config mutation exit=%d", code)
	}
	if after, err := os.ReadFile(cfgPath); err != nil || !bytes.Equal(after, malformed) {
		t.Fatalf("malformed config changed: %q err=%v", after, err)
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer srv.Close()
	env["ATL_JIRA_URL"] = srv.URL
	env["ATL_JIRA_PAT"] = "test-pat"
	if _, code := runCLI(t, env, "jira", "issue", "search", "--jql", "project=PROJ"); code != exitConfig || requests != 0 {
		t.Fatalf("online read exit=%d requests=%d", code, requests)
	}
}
