package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/version"
)

// TestMutationPreConfigRoutesPreserveLiteralContract deliberately does not
// derive cases or expectations from commandRegistry or mutation guard specs.
// It is the independent oracle for the small set of invocation failures that
// must keep preceding malformed config, credentials, stdin, and backend access.
func TestMutationPreConfigRoutesPreserveLiteralContract(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(writeRoot, ".atl"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"ATL_CONFIG_DIR":     configDir,
		"ATL_CONFLUENCE_URL": server.URL,
		"ATL_CONFLUENCE_PAT": "test-pat",
		"ATL_JIRA_URL":       server.URL,
		"ATL_JIRA_PAT":       "test-pat",
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "confluence attachment delete",
			args: []string{"conf", "attachment", "delete"},
			want: "usage error: --page-id and --id are required",
		},
		{
			name: "confluence comment mutation apply",
			args: []string{"conf", "comment", "mutation", "apply", "--id", "1", "--operation", "resolve", "--thread-id", "2"},
			want: "usage error: --apply is required for this apply command",
		},
		{
			name: "confluence page copy",
			args: []string{"conf", "page", "copy"},
			want: "usage error: --id and --title are required",
		},
		{
			name: "confluence page delete",
			args: []string{"conf", "page", "delete"},
			want: "usage error: --id is required",
		},
		{
			name: "jira issue delete",
			args: []string{"jira", "issue", "delete", "not-a-key"},
			want: "usage error: issue key must be canonical (for example PROJ-1)",
		},
		{
			name: "jira issue edit",
			args: []string{"jira", "issue", "edit", "PROJ-1", "--old", "x", "--new", "y", "--dry-run=false"},
			want: "usage error: --dry-run=false is not supported; omit --dry-run to preview",
		},
		{
			name: "mirror backend bind apply",
			args: []string{"mirror", "backend", "bind", writeRoot, "--service", "jira", "--apply", "--confirm", "BIND"},
			want: "usage error: --expected-backend-sha256 is required with --apply",
		},
		{
			name: "profile apply",
			args: []string{"profile", "apply"},
			want: "usage error: --from-file is required for this apply command",
		},
		{
			name: "profile suggestion apply",
			args: []string{"profile", "suggestion", "apply"},
			want: "usage error: --from-file is required for this apply command",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeConfig := mutationGuardTreeSnapshot(t, configDir)
			beforeRoot := mutationGuardTreeSnapshot(t, writeRoot)
			requests.Store(0)

			stdout, stderr, err := executeCLIRaw(t, env, test.args...)
			if err == nil || err.Error() != test.want {
				t.Fatalf("err=%v want exact %q", err, test.want)
			}
			if !errors.Is(err, domain.ErrUsage) || errors.Is(err, domain.ErrConfig) || codeFor(err) != exitUsage {
				t.Fatalf("err=%v code=%d, want usage before malformed config", err, codeFor(err))
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("stdout=%q stderr=%q, want both empty", stdout, stderr)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("requests=%d, want zero", got)
			}
			if after := mutationGuardTreeSnapshot(t, configDir); !reflect.DeepEqual(after, beforeConfig) {
				t.Fatalf("config tree changed: before=%v after=%v", beforeConfig, after)
			}
			if after := mutationGuardTreeSnapshot(t, writeRoot); !reflect.DeepEqual(after, beforeRoot) {
				t.Fatalf("write target changed: before=%v after=%v", beforeRoot, after)
			}
		})
	}
}

func TestJiraIssueEditPreConfigGuardPrecedesEnabledSelfUpdate(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)

	oldVersion := version.Version
	version.Version = "1.0.0"
	t.Cleanup(func() { version.Version = oldVersion })
	for _, key := range []string{
		"ATL_NO_UPDATE", "ATL_READ_ONLY", "ATL_UPDATE_DEBUG", "ATL_VERBOSE",
		"ATL_JIRA_URL", "JIRA_URL", "ATL_JIRA_PAT", "JIRA_PAT",
		"ATL_POLICY", "ATL_POLICY_FILE", "ATL_POLICY_SHA256", "ATL_POLICY_REQUIRED",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("ATL_UPDATE_URL", server.URL)
	eligibilityRoot := newRoot()
	editCommand, _, findErr := eligibilityRoot.Find([]string{"jira", "issue", "edit"})
	if findErr != nil || editCommand == nil || skipSelfUpdate(editCommand) {
		t.Fatalf("jira issue edit self-update eligibility: command=%v err=%v skipped=%t", editCommand, findErr, editCommand != nil && skipSelfUpdate(editCommand))
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "explicit false dry-run",
			args: []string{"jira", "issue", "edit", "PROJ-1", "--old", "x", "--new", "y", "--dry-run=false"},
			want: "usage error: --dry-run=false is not supported; omit --dry-run to preview",
		},
		{
			name: "malformed proposal hash",
			args: []string{"jira", "issue", "edit", "PROJ-1", "--old", "x", "--new", "y", "--apply", "--expected-proposal-hash", strings.Repeat("A", 64)},
			want: "usage error: --expected-proposal-hash must be a lowercase 64-character SHA-256",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			t.Setenv("ATL_CONFIG_DIR", configDir)
			requests.Store(0)

			var stdout, stderr bytes.Buffer
			root := newRoot()
			setRootExecutionArgs(root, test.args)
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			err := root.ExecuteContext(context.Background())
			if err == nil || err.Error() != test.want || !errors.Is(err, domain.ErrUsage) || codeFor(err) != exitUsage {
				t.Fatalf("err=%v code=%d, want exact usage %q", err, codeFor(err), test.want)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q, want both empty", stdout.String(), stderr.String())
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("self-update requests=%d, want zero", got)
			}
			if _, statErr := os.Stat(filepath.Join(configDir, ".update-check")); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("self-update stamp stat error=%v, want absent", statErr)
			}
		})
	}
}

func TestConfluencePageDeleteRejectsNonCanonicalIDBeforeConfiguration(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"ATL_CONFIG_DIR":     configDir,
		"ATL_CONFLUENCE_URL": server.URL,
		"ATL_CONFLUENCE_PAT": "test-pat",
	}
	tests := []struct {
		name, id, want string
	}{
		{name: "blank", id: "", want: "--id is required"},
		{name: "zero", id: "0", want: "positive numeric content id"},
		{name: "leading zero", id: "01", want: "positive numeric content id"},
		{name: "negative", id: "-1", want: "positive numeric content id"},
		{name: "positive sign", id: "+1", want: "positive numeric content id"},
		{name: "leading whitespace", id: " 1", want: "positive numeric content id"},
		{name: "trailing whitespace", id: "1 ", want: "positive numeric content id"},
		{name: "non-numeric", id: "page-name", want: "positive numeric content id"},
	}
	for _, test := range tests {
		for _, apply := range []bool{false, true} {
			name := "preview"
			args := []string{"conf", "page", "delete", "--id", test.id}
			if apply {
				name = "apply"
				args = append(args, "--apply", "--confirm", "TRASH", "--expected-version", "1", "--expected-proposal-hash", strings.Repeat("a", 64))
			}
			t.Run(test.name+"/"+name, func(t *testing.T) {
				requests.Store(0)
				stdout, stderr, err := executeCLIRaw(t, env, args...)
				if !errors.Is(err, domain.ErrUsage) || codeFor(err) != exitUsage || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error=%v code=%d, want usage containing %q", err, codeFor(err), test.want)
				}
				if stdout != "" || stderr != "" || requests.Load() != 0 {
					t.Fatalf("stdout=%q stderr=%q requests=%d, want no effects", stdout, stderr, requests.Load())
				}
			})
		}
	}
}

func TestMutationGuardSpecsPreserveReviewedPhasesAndFamilies(t *testing.T) {
	type expectedGuard struct {
		phase  mutationGuardPhase
		family mutationGuardFamily
	}
	want := map[string]expectedGuard{
		"conf attachment delete":       {mutationGuardPreConfig, mutationGuardConfluenceAttachmentDelete},
		"conf comment add":             {mutationGuardCommandOwned, mutationGuardGeneric},
		"conf comment mutation apply":  {mutationGuardPreConfig, mutationGuardGeneric},
		"conf page copy":               {mutationGuardPreConfig, mutationGuardConfluencePageCopy},
		"conf page delete":             {mutationGuardPreConfig, mutationGuardConfluencePageDelete},
		"conf page labels add":         {mutationGuardCommandOwned, mutationGuardGeneric},
		"conf page labels remove":      {mutationGuardCommandOwned, mutationGuardGeneric},
		"conf page move":               {mutationGuardCommandOwned, mutationGuardGeneric},
		"conf page title set":          {mutationGuardCommandOwned, mutationGuardGeneric},
		"conf plan apply":              {mutationGuardCommandOwned, mutationGuardGeneric},
		"corpus cache retention apply": {mutationGuardPreConfig, mutationGuardGeneric},
		"jira issue comment add":       {mutationGuardPreConfig, mutationGuardJiraGuardedComment},
		"jira issue create":            {mutationGuardPreConfig, mutationGuardJiraGuardedCreate},
		"jira issue delete":            {mutationGuardPreConfig, mutationGuardJiraIssueDelete},
		"jira issue edit":              {mutationGuardPreConfig, mutationGuardJiraDescriptionEdit},
		"jira issue labels":            {mutationGuardPreConfig, mutationGuardJiraGuardedLabels},
		"jira issue link add":          {mutationGuardPreConfig, mutationGuardJiraGuardedLink},
		"jira issue link delete":       {mutationGuardPreConfig, mutationGuardJiraGuardedLink},
		"jira issue field set":         {mutationGuardPreConfig, mutationGuardJiraGuardedField},
		"jira issue plan apply":        {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue transition":        {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue watchers add":      {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue watchers remove":   {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue worklog add":       {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira push":                    {mutationGuardCommandOwned, mutationGuardGeneric},
		"mirror backend bind":          {mutationGuardPreConfigOnApply, mutationGuardGeneric},
		"profile apply":                {mutationGuardPreConfig, mutationGuardGeneric},
		"profile suggestion apply":     {mutationGuardPreConfig, mutationGuardGeneric},
	}

	seen := 0
	for path, registration := range commandRegistry.nodes {
		if len(registration.guard.requirements) == 0 {
			if registration.guard.phase != 0 || registration.guard.family != 0 {
				t.Errorf("unguarded command %q has phase=%d family=%d", path, registration.guard.phase, registration.guard.family)
			}
			continue
		}
		seen++
		expected, ok := want[path]
		if !ok {
			t.Errorf("guarded command %q is absent from reviewed typed inventory", path)
			continue
		}
		if registration.guard.phase != expected.phase || registration.guard.family != expected.family {
			t.Errorf("guarded command %q phase/family=%d/%d want=%d/%d", path,
				registration.guard.phase, registration.guard.family, expected.phase, expected.family)
		}
		for _, requirement := range registration.guard.requirements {
			if _, ok := mutationGuardRequirementName(requirement); !ok {
				t.Errorf("guarded command %q has invalid typed requirement %d", path, requirement)
			}
		}
	}
	if seen != 28 || len(want) != 28 {
		t.Fatalf("typed guarded commands=%d reviewed=%d want=28", seen, len(want))
	}
	for path := range want {
		registration, ok := commandRegistry.nodes[path]
		if !ok || len(registration.guard.requirements) == 0 {
			t.Errorf("reviewed guarded command %q is not typed", path)
		}
	}
}

func TestMutationGuardRequirementsPreserveReviewedPresenceSemantics(t *testing.T) {
	want := map[mutationGuardRequirement]struct {
		name     string
		presence mutationGuardPresence
	}{
		mutationGuardApply:                 {"apply", mutationGuardPresenceTrue},
		mutationGuardConfirm:               {"confirm", mutationGuardPresenceNonBlank},
		mutationGuardExpectedProposalHash:  {"expected-proposal-hash", mutationGuardPresenceNonBlank},
		mutationGuardExpectedVersion:       {"expected-version", mutationGuardPresenceNonBlank},
		mutationGuardExpectedParent:        {"expected-parent", mutationGuardPresenceExplicit},
		mutationGuardExpectedUpdated:       {"expected-updated", mutationGuardPresenceNonBlank},
		mutationGuardExpectedBackendSHA256: {"expected-backend-sha256", mutationGuardPresenceNonBlank},
		mutationGuardFromFile:              {"from-file", mutationGuardPresenceNonBlank},
		mutationGuardSuggestionHash:        {"suggestion-hash", mutationGuardPresenceNonBlank},
		mutationGuardCandidateHash:         {"candidate-hash", mutationGuardPresenceNonBlank},
		mutationGuardExpectedCurrentHash:   {"expected-current-hash", mutationGuardPresenceNonBlank},
		mutationGuardExpectedPlanDigest:    {"expected-plan-digest", mutationGuardPresenceNonBlank},
	}
	for requirement, expected := range want {
		name, ok := mutationGuardRequirementName(requirement)
		if !ok || name != expected.name {
			t.Errorf("requirement %d name=%q valid=%v want=%q", requirement, name, ok, expected.name)
		}
		presence, ok := mutationGuardRequirementPresence(requirement)
		if !ok || presence != expected.presence {
			t.Errorf("requirement %d presence=%d valid=%v want=%d", requirement, presence, ok, expected.presence)
		}
	}
	if len(want) != int(mutationGuardExpectedPlanDigest) {
		t.Fatalf("reviewed requirements=%d enum extent=%d", len(want), mutationGuardExpectedPlanDigest)
	}
}

type mutationGuardSnapshotEntry struct {
	mode fs.FileMode
	body []byte
}

func mutationGuardTreeSnapshot(t *testing.T, root string) map[string]mutationGuardSnapshotEntry {
	t.Helper()
	snapshot := map[string]mutationGuardSnapshotEntry{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := mutationGuardSnapshotEntry{mode: info.Mode()}
		if info.Mode().IsRegular() {
			item.body, err = os.ReadFile(path)
			if err != nil {
				return err
			}
			item.body = bytes.Clone(item.body)
		}
		snapshot[rel] = item
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
