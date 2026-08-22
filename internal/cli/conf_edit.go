package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

// confEditCmd implements `conf edit`: precise, whitespace/invisible-tolerant
// in-place replacement for local CSF files. It exists because real CSF bodies
// are single-line and salted with U+00A0/entities, which defeats exact-match
// editing tools; the layered matcher in internal/textedit locates the target
// and splices the new bytes while preserving everything around them verbatim.
func confEditCmd() *cobra.Command {
	var oldS, newS, oldFile, newFile string
	var all, dryRun bool
	cmd := &cobra.Command{
		Use:   "edit <file>",
		Short: "Replace text in a local file, tolerant of NBSP/invisible bytes (CSF-aware)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			old, err := textFromFlagPair(oldS, oldFile, "--old")
			if err != nil {
				return err
			}
			repl, err := textFromFlagPair(newS, newFile, "--new")
			if err != nil {
				return err
			}
			if old == "" {
				return usageErr("--old (or --old-file) is required and must be non-empty")
			}
			if !cmd.Flags().Changed("new") && newFile == "" {
				return usageErr("--new (or --new-file) is required (pass --new '' to delete the matched text)")
			}
			result, err := app.EditConfluenceFile(app.ConfluenceEditOptions{
				File:   args[0],
				Old:    old,
				New:    repl,
				All:    all,
				DryRun: dryRun,
			})
			if result != nil && result.CSFOK != nil && !*result.CSFOK {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"warning: result is not well-formed CSF — fix before pushing (see problems)")
			}
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				verb := "replaced"
				if dryRun {
					verb = "would replace"
				}
				return fmt.Sprintf("%s\t%s %d occurrence(s) via %s pass", result.File, verb, result.Count, result.Pass)
			})
		},
	}
	cmd.Flags().StringVar(&oldS, "old", "", "text to find (tolerant of NBSP/zero-width/entity differences)")
	cmd.Flags().StringVar(&newS, "new", "", "replacement text (inserted verbatim)")
	cmd.Flags().StringVar(&oldFile, "old-file", "", "read the text to find from a file (- for stdin; one trailing newline is stripped)")
	cmd.Flags().StringVar(&newFile, "new-file", "", "read the replacement from a file (one trailing newline is stripped)")
	cmd.Flags().BoolVar(&all, "all", false, "replace every match instead of requiring a unique one")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report the match without writing the file")
	return cmd
}

// textFromFlagPair resolves an inline flag vs its --*-file variant.
func textFromFlagPair(inline, file, name string) (string, error) {
	if inline != "" && file != "" {
		return "", usageErr("pass either %s or %s-file, not both", name, name)
	}
	if file != "" {
		b, err := readBody(file)
		if err != nil {
			return "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
		}
		// Editors and agent Write tools terminate files with a newline that is
		// almost never meant as part of the needle/replacement in single-line
		// CSF. Strip exactly one; add two when one is really wanted.
		return strings.TrimSuffix(string(b), "\n"), nil
	}
	return inline, nil
}
