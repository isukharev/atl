package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

type guardedWriteProfile uint8

const (
	guardedWriteInvalid guardedWriteProfile = iota
	guardedWriteProposal
	guardedWriteAggregateProposal
	guardedWriteCapturedAggregateProposal
	guardedWriteMove
)

type guardedWriteFlags struct {
	profile              guardedWriteProfile
	apply                bool
	expectedProposalHash string
}

func (flags *guardedWriteFlags) register(cmd *cobra.Command) {
	hashHelp, applyHelp, _ := flags.text()
	cmd.Flags().StringVar(&flags.expectedProposalHash, "expected-proposal-hash", "", hashHelp)
	cmd.Flags().BoolVar(&flags.apply, "apply", false, applyHelp)
}

func (flags *guardedWriteFlags) validate() error {
	if !flags.apply || strings.TrimSpace(flags.expectedProposalHash) != "" {
		return nil
	}
	_, _, missingHash := flags.text()
	return usageErr(missingHash)
}

func (flags *guardedWriteFlags) text() (hashHelp, applyHelp, missingHash string) {
	applyHelp = "perform the guarded write (default: dry-run)"
	missingHash = "--expected-proposal-hash is required with --apply; run the dry-run first"
	switch flags.profile {
	case guardedWriteAggregateProposal:
		hashHelp = "reviewed aggregate proposal hash (required with --apply)"
	case guardedWriteCapturedAggregateProposal:
		hashHelp = "reviewed aggregate proposal hash (required with --apply; preview captures it)"
		missingHash = "--expected-proposal-hash is required with --apply; run the dry-run first to capture it"
	case guardedWriteMove:
		hashHelp = "reviewed proposal hash (required with --apply)"
		applyHelp = "perform the guarded move (default: dry-run)"
	case guardedWriteProposal:
		hashHelp = "reviewed proposal hash (required with --apply)"
	default:
		panic("invalid guarded write profile")
	}
	return hashHelp, applyHelp, missingHash
}

// validateGuardedPreviewInvocation keeps dedicated read-only children on the
// same pure pre-configuration validation path as their guarded parents.
func validateGuardedPreviewInvocation(cmd *cobra.Command, path string) (bool, error) {
	switch path {
	case "jira issue create preview":
		return true, validateJiraGuardedCreateInvocation(cmd, false)
	case "jira issue labels preview":
		return true, validateJiraGuardedLabelInvocation(cmd, false)
	case "jira issue comment preview":
		return true, validateJiraGuardedCommentInvocation(cmd, false)
	default:
		return false, nil
	}
}
