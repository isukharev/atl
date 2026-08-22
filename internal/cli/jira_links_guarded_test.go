package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestJiraGuardedLinkParentsAndPreviewChildrenHaveExactAccess(t *testing.T) {
	root := newRoot()
	for _, item := range []struct{ path, access string }{
		{"jira issue link add", "mutating"}, {"jira issue link add preview", "read-only"},
		{"jira issue link delete", "mutating"}, {"jira issue link delete preview", "read-only"},
	} {
		cmd, _, err := root.Find(strings.Fields(item.path))
		if err != nil || cmd == nil || cmd.Annotations[accessAnnotation] != item.access {
			t.Fatalf("%s command=%v err=%v access=%q", item.path, cmd, err, cmd.Annotations[accessAnnotation])
		}
		if cmd.Annotations[textOutputAnnotation] != "unsupported" || cmd.Annotations[idOutputAnnotation] != "unsupported" {
			t.Fatalf("%s unexpectedly admits non-JSON output", item.path)
		}
	}
}

func TestJiraGuardedLinkCompletePreConfigFlagMatrix(t *testing.T) {
	validHash := strings.Repeat("a", 64)
	tests := [][]string{
		{"jira", "issue", "link", "add", "bad", "--to", "OPS-2", "--type", "Blocks"},
		{"jira", "issue", "link", "add", "APP-1", "--type", "Blocks"},
		{"jira", "issue", "link", "add", "APP-1", "--to", "APP-1", "--type", "Blocks"},
		{"jira", "issue", "link", "add", "APP-1", "--to", "OPS-2", "--type", "Blocks", "--expected-proposal-hash", validHash},
		{"jira", "issue", "link", "add", "APP-1", "--to", "OPS-2", "--type", "Blocks", "--apply", "--expected-proposal-hash", strings.Repeat("A", 64)},
		{"jira", "issue", "link", "delete", "90", "--to", "OPS-2", "--type", "Blocks"},
		{"jira", "issue", "link", "delete", "09", "--from", "APP-1", "--to", "OPS-2", "--type", "Blocks"},
	}
	for _, args := range tests {
		if _, code := runCLI(t, map[string]string{"ATL_CONFIG_DIR": t.TempDir()}, args...); code != exitUsage {
			t.Errorf("%v exit=%d want usage", args, code)
		}
	}
}

func TestJiraGuardedLinkReadOnlyPolicyPrecedesMalformedParentGuards(t *testing.T) {
	if _, code := runCLI(t, nil, "--read-only", "jira", "issue", "link", "add", "bad"); code != exitCheckFailed {
		t.Fatalf("exit=%d want read-only refusal", code)
	}
}

func TestJiraGuardedLinkPreflightExtractsExactCompoundLinkTargets(t *testing.T) {
	root := newRoot()
	tests := []struct {
		path []string
		args []string
		set  map[string]string
	}{
		{path: []string{"jira", "issue", "link", "add"}, args: []string{"APP-1"}, set: map[string]string{"to": "OPS-2"}},
		{path: []string{"jira", "issue", "link", "delete"}, args: []string{"90"}, set: map[string]string{"from": "APP-1", "to": "OPS-2"}},
	}
	want := []domain.WriteTarget{{Service: "jira", Kind: "link", Key: "APP-1", Project: "APP"}, {Service: "jira", Kind: "link", Key: "OPS-2", Project: "OPS"}}
	for _, test := range tests {
		cmd, _, err := root.Find(test.path)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range test.set {
			if err := cmd.Flags().Set(name, value); err != nil {
				t.Fatal(err)
			}
		}
		got, err := policyPreflightTargets(cmd, test.args, policyIdentityJiraLinkEndpoints)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%v targets=%+v err=%v", test.path, got, err)
		}
	}
}
