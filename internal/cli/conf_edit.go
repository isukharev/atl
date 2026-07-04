package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/textedit"
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
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("%w: %v", domain.ErrUsage, err)
			}

			res, rerr := textedit.Replace(string(raw), old, repl, all)
			if rerr != nil {
				var amb *textedit.AmbiguousError
				var nom *textedit.NoMatchError
				switch {
				case errors.As(rerr, &amb):
					return fmt.Errorf("%w: %v", domain.ErrUsage, rerr)
				case errors.As(rerr, &nom):
					return fmt.Errorf("%w: %v", domain.ErrNotFound, rerr)
				default:
					return fmt.Errorf("%w: %v", domain.ErrUsage, rerr)
				}
			}

			out := map[string]any{
				"file":    args[0],
				"pass":    string(res.Pass),
				"count":   len(res.Matches),
				"offsets": res.Matches,
				"dry_run": dryRun,
			}
			// Show the spliced region so the caller can review exactly what
			// changed (first match; ±40 bytes of context).
			m := res.Matches[0]
			out["region_before"] = quoteRegion(string(raw), m.Start, m.End)
			out["region_after"] = quoteRegion(res.Text, m.Start, m.Start+len(repl))

			if strings.HasSuffix(args[0], ".csf") {
				problems := csf.Validate([]byte(res.Text))
				out["csf_ok"] = !csf.HasErrors(problems)
				if len(problems) > 0 {
					out["problems"] = problems
				}
				if csf.HasErrors(problems) {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"warning: result is not well-formed CSF — fix before pushing (see problems)\n")
				}
			}

			if !dryRun {
				info, serr := os.Stat(args[0])
				mode := os.FileMode(0o644)
				if serr == nil {
					mode = info.Mode()
				}
				if werr := os.WriteFile(args[0], []byte(res.Text), mode); werr != nil {
					return werr
				}
			}
			return emit(cmd, out, func() string {
				verb := "replaced"
				if dryRun {
					verb = "would replace"
				}
				return fmt.Sprintf("%s\t%s %d occurrence(s) via %s pass", args[0], verb, len(res.Matches), res.Pass)
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

// quoteRegion renders text around [start,end) with hidden bytes visible
// (%q-quoted), clamped to the file bounds.
func quoteRegion(s string, start, end int) string {
	lo, hi := start-40, end+40
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	return fmt.Sprintf("%q", s[lo:hi])
}
