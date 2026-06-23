package cli

import "github.com/spf13/cobra"

// fixedComp returns a flag-value completion that always offers the given values
// and suppresses file completion. Used for flags with a small fixed value set
// (e.g. -o json|text|id) so a shell completes the values, not filenames. Dynamic
// completion of issue keys / space keys would require a metadata cache, which
// this tool does not yet have, so completion is limited to fixed value sets.
func fixedComp(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}
