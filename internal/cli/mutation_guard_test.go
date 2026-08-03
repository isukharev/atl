package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/isukharev/atl/internal/domain"
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

func TestMutationGuardSpecsPreserveReviewedPhasesAndFamilies(t *testing.T) {
	type expectedGuard struct {
		phase  mutationGuardPhase
		family mutationGuardFamily
	}
	want := map[string]expectedGuard{
		"conf attachment delete":      {mutationGuardPreConfig, mutationGuardConfluenceAttachmentDelete},
		"conf comment add":            {mutationGuardCommandOwned, mutationGuardGeneric},
		"conf comment mutation apply": {mutationGuardPreConfig, mutationGuardGeneric},
		"conf page copy":              {mutationGuardPreConfig, mutationGuardConfluencePageCopy},
		"conf page delete":            {mutationGuardPreConfig, mutationGuardConfluencePageDelete},
		"conf page labels add":        {mutationGuardCommandOwned, mutationGuardGeneric},
		"conf page labels remove":     {mutationGuardCommandOwned, mutationGuardGeneric},
		"conf page move":              {mutationGuardCommandOwned, mutationGuardGeneric},
		"conf page title set":         {mutationGuardCommandOwned, mutationGuardGeneric},
		"conf plan apply":             {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue comment add":      {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue delete":           {mutationGuardPreConfig, mutationGuardJiraIssueDelete},
		"jira issue field set":        {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue plan apply":       {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue transition":       {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue watchers add":     {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue watchers remove":  {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira issue worklog add":      {mutationGuardCommandOwned, mutationGuardGeneric},
		"jira push":                   {mutationGuardCommandOwned, mutationGuardGeneric},
		"mirror backend bind":         {mutationGuardPreConfigOnApply, mutationGuardGeneric},
		"profile apply":               {mutationGuardPreConfig, mutationGuardGeneric},
		"profile suggestion apply":    {mutationGuardPreConfig, mutationGuardGeneric},
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
	if seen != 22 || len(want) != 22 {
		t.Fatalf("typed guarded commands=%d reviewed=%d want=22", seen, len(want))
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
	if len(want) != int(mutationGuardExpectedCurrentHash) {
		t.Fatalf("reviewed requirements=%d enum extent=%d", len(want), mutationGuardExpectedCurrentHash)
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
