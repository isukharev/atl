package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/domain"
)

func TestGuardedWriteFlagsPreserveHelpAndDryRunDefaults(t *testing.T) {
	tests := []struct {
		name      string
		profile   guardedWriteProfile
		hashHelp  string
		applyHelp string
	}{
		{
			name:      "proposal",
			profile:   guardedWriteProposal,
			hashHelp:  "reviewed proposal hash (required with --apply)",
			applyHelp: "perform the guarded write (default: dry-run)",
		},
		{
			name:      "aggregate proposal",
			profile:   guardedWriteAggregateProposal,
			hashHelp:  "reviewed aggregate proposal hash (required with --apply)",
			applyHelp: "perform the guarded write (default: dry-run)",
		},
		{
			name:      "captured aggregate proposal",
			profile:   guardedWriteCapturedAggregateProposal,
			hashHelp:  "reviewed aggregate proposal hash (required with --apply; preview captures it)",
			applyHelp: "perform the guarded write (default: dry-run)",
		},
		{
			name:      "move",
			profile:   guardedWriteMove,
			hashHelp:  "reviewed proposal hash (required with --apply)",
			applyHelp: "perform the guarded move (default: dry-run)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "test"}
			flags := guardedWriteFlags{profile: tt.profile}
			flags.register(cmd)

			if got := cmd.Flags().Lookup("expected-proposal-hash"); got == nil || got.Usage != tt.hashHelp || got.DefValue != "" {
				t.Fatalf("expected-proposal-hash flag = %#v, want help %q and empty default", got, tt.hashHelp)
			}
			if got := cmd.Flags().Lookup("apply"); got == nil || got.Usage != tt.applyHelp || got.DefValue != "false" {
				t.Fatalf("apply flag = %#v, want help %q and false default", got, tt.applyHelp)
			}
			if flags.apply {
				t.Fatal("apply defaulted true; guarded commands must remain dry-run by default")
			}
			if err := flags.validate(); err != nil {
				t.Fatalf("dry-run with an empty hash returned %v", err)
			}
		})
	}
}

func TestGuardedWriteApplyRequiresTrimmedHash(t *testing.T) {
	tests := []struct {
		name    string
		profile guardedWriteProfile
		want    string
	}{
		{
			name:    "standard",
			profile: guardedWriteProposal,
			want:    "usage error: --expected-proposal-hash is required with --apply; run the dry-run first",
		},
		{
			name:    "field set",
			profile: guardedWriteCapturedAggregateProposal,
			want:    "usage error: --expected-proposal-hash is required with --apply; run the dry-run first to capture it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := guardedWriteFlags{profile: tt.profile, apply: true, expectedProposalHash: " \t\n "}
			err := flags.validate()
			if !errors.Is(err, domain.ErrUsage) || err.Error() != tt.want {
				t.Fatalf("validate() error = %v, want %q wrapping ErrUsage", err, tt.want)
			}
		})
	}
}

func TestGuardedWriteProfileFailsLoudly(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != "invalid guarded write profile" {
			t.Fatalf("panic = %#v, want invalid guarded write profile", recovered)
		}
	}()
	flags := guardedWriteFlags{profile: guardedWriteInvalid}
	flags.register(&cobra.Command{Use: "test"})
}

func TestGuardedWriteRealCommandsPreserveProfilesAndFailBeforeInputOrService(t *testing.T) {
	tests := []struct {
		name      string
		path      []string
		hashHelp  string
		applyHelp string
		wantError string
	}{
		{
			name: "confluence title",
			path: []string{"conf", "page", "title", "set", "42", "--from-file", "/definitely/missing/title.txt",
				"--expected-version", "1", "--apply"},
			hashHelp:  "reviewed aggregate proposal hash (required with --apply)",
			applyHelp: "perform the guarded write (default: dry-run)",
			wantError: "usage error: --expected-proposal-hash is required with --apply; run the dry-run first",
		},
		{
			name:      "confluence labels",
			path:      []string{"conf", "page", "labels", "add", "42", "reviewed", "--apply"},
			hashHelp:  "reviewed proposal hash (required with --apply)",
			applyHelp: "perform the guarded write (default: dry-run)",
			wantError: "usage error: --expected-proposal-hash is required with --apply; run the dry-run first",
		},
		{
			name: "confluence move",
			path: []string{"conf", "page", "move", "42", "--parent", "41", "--expected-version", "1",
				"--expected-parent=", "--apply"},
			hashHelp:  "reviewed proposal hash (required with --apply)",
			applyHelp: "perform the guarded move (default: dry-run)",
			wantError: "usage error: --expected-proposal-hash is required with --apply; run the dry-run first",
		},
		{
			name: "Jira field set",
			path: []string{"jira", "issue", "field", "set", "PROJ-1",
				"--from-file", "customfield_1=/definitely/missing/value.json", "--allow-fields", "customfield_1",
				"--expected-updated", "reviewed", "--apply"},
			hashHelp:  "reviewed aggregate proposal hash (required with --apply; preview captures it)",
			applyHelp: "perform the guarded write (default: dry-run)",
			wantError: "usage error: --expected-proposal-hash is required with --apply; run the dry-run first to capture it",
		},
		{
			name:      "Jira watcher",
			path:      []string{"jira", "issue", "watchers", "add", "PROJ-1", "--username", "reviewed", "--apply"},
			hashHelp:  "reviewed proposal hash (required with --apply)",
			applyHelp: "perform the guarded write (default: dry-run)",
			wantError: "usage error: --expected-proposal-hash is required with --apply; run the dry-run first",
		},
		{
			name: "Jira worklog",
			path: []string{"jira", "issue", "worklog", "add", "PROJ-1", "--time", "1h", "--from-file",
				"/definitely/missing/comment.txt", "--apply"},
			hashHelp:  "reviewed proposal hash (required with --apply)",
			applyHelp: "perform the guarded write (default: dry-run)",
			wantError: "usage error: --expected-proposal-hash is required with --apply; run the dry-run first",
		},
		{
			name:      "Jira comment",
			path:      []string{"jira", "issue", "comment", "add", "PROJ-1", "--from-file", "/definitely/missing/comment.wiki", "--apply"},
			hashHelp:  "reviewed proposal hash (required with --apply)",
			applyHelp: "perform the guarded write (default: dry-run)",
			wantError: "usage error: --expected-proposal-hash is required with --apply; run the dry-run first",
		},
		{
			name:      "Jira transition",
			path:      []string{"jira", "issue", "transition", "PROJ-1", "--to", "Done", "--apply"},
			hashHelp:  "reviewed proposal hash (required with --apply)",
			applyHelp: "perform the guarded write (default: dry-run)",
			wantError: "usage error: --expected-proposal-hash is required with --apply; run the dry-run first",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := isolatedGuardedWriteRoot(t)
			command, _, err := root.Find(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := command.Flags().Lookup("expected-proposal-hash"); got == nil || got.Usage != tt.hashHelp || got.DefValue != "" {
				t.Fatalf("expected-proposal-hash flag = %#v, want help %q and empty default", got, tt.hashHelp)
			}
			if got := command.Flags().Lookup("apply"); got == nil || got.Usage != tt.applyHelp || got.DefValue != "false" {
				t.Fatalf("apply flag = %#v, want help %q and false default", got, tt.applyHelp)
			}
			root.SetArgs(tt.path)
			var stdout bytes.Buffer
			root.SetOut(&stdout)
			err = root.ExecuteContext(context.Background())
			if !errors.Is(err, domain.ErrUsage) || err.Error() != tt.wantError {
				t.Fatalf("ExecuteContext() error = %v, want %q wrapping ErrUsage", err, tt.wantError)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestGuardedWriteEndpointPrerequisitesStillPrecedeHash(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "title version", args: []string{"conf", "page", "title", "set", "42", "--from-file", "missing", "--apply"},
			want: "usage error: --expected-version is required with --apply; run the dry-run first"},
		{name: "move version", args: []string{"conf", "page", "move", "42", "--parent", "41", "--apply"},
			want: "usage error: --expected-version is required with --apply; run the dry-run first"},
		{name: "move parent", args: []string{"conf", "page", "move", "42", "--parent", "41", "--expected-version", "1", "--apply"},
			want: "usage error: --expected-parent is required with --apply; use --expected-parent= for a top-level page"},
		{name: "field updated", args: []string{"jira", "issue", "field", "set", "PROJ-1", "--from-file", "customfield_1=missing",
			"--allow-fields", "customfield_1", "--apply"},
			want: "usage error: --expected-updated is required with --apply; run the dry-run first to capture it"},
		{name: "watcher identity", args: []string{"jira", "issue", "watchers", "add", "PROJ-1", "--apply"},
			want: "usage error: pass exactly one of --username or --me"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := isolatedGuardedWriteRoot(t)
			root.SetArgs(tt.args)
			err := root.ExecuteContext(context.Background())
			if !errors.Is(err, domain.ErrUsage) || err.Error() != tt.want {
				t.Fatalf("ExecuteContext() error = %v, want %q wrapping ErrUsage", err, tt.want)
			}
		})
	}
}

func isolatedGuardedWriteRoot(t *testing.T) *cobra.Command {
	t.Helper()
	t.Setenv("ATL_NO_UPDATE", "1")
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	for _, name := range []string{"ATL_CONFLUENCE_URL", "CONFLUENCE_URL", "ATL_JIRA_URL", "JIRA_URL",
		"ATL_CONFLUENCE_PAT", "CONFLUENCE_PAT", "ATL_JIRA_PAT", "JIRA_PAT", "ATL_READ_ONLY"} {
		t.Setenv(name, "")
	}
	return newRoot()
}

func TestGuardedWriteMissingHashPrecedesFileRead(t *testing.T) {
	out, _, code := runCLIFull(t, nil,
		"jira", "issue", "field", "set", "PROJ-1",
		"--from-file", "customfield_1=/definitely/missing/guarded-write.json",
		"--allow-fields", "customfield_1", "--expected-updated", "reviewed", "--apply")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage %d; missing file was likely read first", code, exitUsage)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want empty output", out)
	}
}

func TestGuardedWriteMissingHashPrecedesServiceConstruction(t *testing.T) {
	out, _, code := runCLIFull(t, nil,
		"conf", "page", "labels", "add", "42", "reviewed", "--apply")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage %d; service was likely constructed first", code, exitUsage)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want empty output", out)
	}
}
