package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func confPageTitleCmd() *cobra.Command {
	group := &cobra.Command{Use: "title", Short: "Guarded page-title operations"}
	var fromFile, expectedProposalHash string
	var expectedVersion int
	var applyWrite bool
	set := &cobra.Command{
		Use:   "set <ID>",
		Short: "Preview or apply a bounded file-backed page title",
		Long: "Read a one-line title from a bounded file/stdin and preview by default. " +
			"Apply requires --expected-version and --expected-proposal-hash from the exact reviewed dry-run, " +
			"writes the unchanged native CSF under the version gate, and verifies the final state without replay.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(fromFile) == "" {
				return usageErr("--from-file is required (use - for stdin)")
			}
			title, err := readConfluenceTitleInput(fromFile)
			if err != nil {
				return err
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			res, setErr := svc.SetTitleGuarded(cmd.Context(), args[0], app.ConfluenceTitleSetOpts{
				Title: title, ExpectedVersion: expectedVersion,
				ExpectedProposalHash: expectedProposalHash, Apply: applyWrite,
			})
			if res != nil {
				if emitErr := emit(cmd, res, func() string { return confluenceTitleSetText(res) }); emitErr != nil {
					return emitErr
				}
			}
			return setErr
		},
	}
	set.Flags().StringVar(&fromFile, "from-file", "", "title file or - for stdin (required; bounded to 4096 bytes)")
	set.Flags().IntVar(&expectedVersion, "expected-version", 0, "reviewed current page version (required with --apply)")
	set.Flags().StringVar(&expectedProposalHash, "expected-proposal-hash", "", "reviewed aggregate proposal hash (required with --apply)")
	set.Flags().BoolVar(&applyWrite, "apply", false, "perform the guarded write (default: dry-run)")
	group.AddCommand(set)
	return group
}

func readConfluenceTitleInput(path string) ([]byte, error) {
	var r io.Reader
	var closeFn func() error
	if path == "-" {
		if stdinIsTerminal() {
			return nil, usageErr("stdin is a terminal and no title was piped; pass --from-file FILE or pipe the title")
		}
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		r, closeFn = f, f.Close
	}
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}
	data, err := io.ReadAll(io.LimitReader(r, app.ConfluenceTitleInputCap+1))
	if err != nil {
		return nil, err
	}
	if len(data) > app.ConfluenceTitleInputCap {
		return nil, usageErr("title input exceeds %d bytes", app.ConfluenceTitleInputCap)
	}
	return data, nil
}

func confluenceTitleSetText(res *app.ConfluenceTitleSetResult) string {
	if res == nil {
		return ""
	}
	return fmt.Sprintf("%s\t%s\tv%d\t%s\t%s", res.Status, res.ID, res.CurrentVersion, res.ProposalHash, res.Title)
}
