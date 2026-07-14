package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

const accessAnnotation = "atl.access"

type readOnlyPolicyError struct{ Command string }

func (e *readOnlyPolicyError) Error() string {
	return fmt.Sprintf("read-only policy blocks mutating command %q; remove the policy only after explicit human approval", e.Command)
}

func (e *readOnlyPolicyError) Unwrap() error { return domain.ErrCheckFailed }

func readOnlyErrorMetadata(err error) (string, bool) {
	var policyErr *readOnlyPolicyError
	if errors.As(err, &policyErr) {
		return policyErr.Command, true
	}
	return "", false
}

type accessPolicyInvariantError struct{ Command string }

func (e *accessPolicyInvariantError) Error() string {
	return fmt.Sprintf("command %q has no access-policy classification", e.Command)
}

func (e *accessPolicyInvariantError) Unwrap() error { return domain.ErrCheckFailed }

func accessPolicyInvariantMetadata(err error) (string, bool) {
	var invariantErr *accessPolicyInvariantError
	if errors.As(err, &invariantErr) {
		return invariantErr.Command, true
	}
	return "", false
}

var mutatingCommandPaths = stringSetFromLines(`
auth login
auth logout
conf apply
conf attachment delete
conf attachment upload
conf blog create
conf comment add
conf edit
conf page copy
conf page create
conf page delete
conf page labels add
conf page labels remove
conf page move
conf page title set
conf plan apply
conf push
config set
jira apply
jira issue assign
jira issue attachment upload
jira issue comment add
jira issue comment delete
jira issue create
jira issue delete
jira issue edit
jira issue field set
jira issue labels
jira issue link add
jira issue link delete
jira issue link-epic
jira issue plan apply
jira issue transition
jira issue update
jira issue watchers add
jira issue watchers remove
jira issue worklog add
jira push
jira sprint add
jira sprint remove
profile apply
profile revalidate
profile suggest
profile suggestion apply
profile suggestion reject
`)

// knownCommandPaths is the registration contract. New leaves are unclassified
// at runtime and in tests until a reviewer deliberately adds them here.
var knownCommandPaths = stringSetFromLines(`
auth login
auth logout
auth status
conf apply
conf attachment delete
conf attachment get
conf attachment list
conf attachment upload
conf blog create
conf comment add
conf comment list
conf diff
conf edit
conf me
conf page copy
conf page create
conf page delete
conf page get
conf page history
conf page labels add
conf page labels list
conf page labels remove
conf page list
conf page meta
conf page move
conf page open
conf page outline
conf page resolve
conf page section
conf page title set
conf page view
conf plan apply
conf plan create
conf plan preview
conf pull
conf push
conf render
conf search
conf space tree
conf status
conf table extract
conf validate
completion bash
completion fish
completion powershell
completion zsh
config set
config show
environment inspect
help
jira apply
jira board backlog
jira board config
jira board export
jira board get
jira board issues
jira board list
jira board view
jira epic digest
jira export
jira export diff
jira field-options
jira fields
jira issue assign
jira issue attachment get
jira issue attachment list
jira issue attachment upload
jira issue check
jira issue children
jira issue comment add
jira issue comment delete
jira issue comment list
jira issue create
jira issue delete
jira issue edit
jira issue field set
jira issue fields
jira issue get
jira issue history
jira issue images
jira issue labels
jira issue link add
jira issue link delete
jira issue link list
jira issue link suggest
jira issue link-epic
jira issue plan apply
jira issue refs
jira issue search
jira issue transition
jira issue tree
jira issue update
jira issue view
jira issue watchers add
jira issue watchers list
jira issue watchers remove
jira issue worklog add
jira issue worklog list
jira link-types
jira me
jira planning report
jira pull
jira push
jira quality-report
jira render
jira sprint add
jira sprint current
jira sprint get
jira sprint issues
jira sprint list
jira sprint remove
jira status
jira structure export
jira structure folders
jira structure forest
jira structure get
jira structure pull-issues
jira structure rows
jira structure values
jira structure view
jira transitions
jira user get
jira user search
manifest create
profile apply
profile guidance
profile preview
profile revalidate
profile revalidation status
profile show
profile suggest
profile suggestion apply
profile suggestion reject
profile suggestion review
version
`)

func stringSetFromLines(value string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out[line] = true
		}
	}
	return out
}

func classifyCommandTree(root *cobra.Command) {
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Run != nil || cmd.RunE != nil {
			path := strings.TrimPrefix(cmd.CommandPath(), root.Name()+" ")
			if cmd.Annotations == nil {
				cmd.Annotations = map[string]string{}
			}
			classifyTextOutput(cmd, path)
			switch {
			case !knownCommandPaths[path]:
				cmd.Annotations[accessAnnotation] = "unclassified"
			case mutatingCommandPaths[path]:
				cmd.Annotations[accessAnnotation] = "mutating"
			default:
				cmd.Annotations[accessAnnotation] = "read-only"
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
}

func resolveReadOnlyPolicy(cmd *cobra.Command, flagEnabled bool) (bool, error) {
	if flagEnabled || envReadOnly() {
		return true, nil
	}
	// Offline/trivial reads are incapable of backend/config mutation and are
	// already guaranteed not to self-update. Keep them usable for diagnosis
	// when config.json itself is malformed; every mutator and online read still
	// decodes the policy strictly below.
	if cmd.Annotations[accessAnnotation] == "read-only" && skipSelfUpdate(cmd) {
		return false, nil
	}
	cfg, err := config.LoadForEdit()
	if err != nil {
		return false, err
	}
	return cfg.ReadOnly, nil
}

func enforceAccessPolicy(cmd *cobra.Command, enabled bool) error {
	access := cmd.Annotations[accessAnnotation]
	if access == "unclassified" || access == "" {
		if cmd.Name() == cobra.ShellCompRequestCmd || cmd.Name() == cobra.ShellCompNoDescRequestCmd {
			return nil
		}
		return &accessPolicyInvariantError{Command: cmd.CommandPath()}
	}
	if access != "mutating" {
		return nil
	}
	if enabled {
		return &readOnlyPolicyError{Command: cmd.CommandPath()}
	}
	return nil
}

func envReadOnly() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ATL_READ_ONLY"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
