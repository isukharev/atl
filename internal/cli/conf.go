package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/version"
)

// warnIfTruncated writes a one-line stderr warning when a --cql pull hit the
// silent page cap, so the caller is told the mirror is incomplete. It writes to
// w (the command's stderr) and never to stdout, keeping the JSON result clean.
func warnIfTruncated(w io.Writer, res *app.PullResult) {
	if res != nil && res.Truncated {
		fmt.Fprintf(w,
			"warning: --cql selection truncated at %d pages (silent cap); narrow the query or pull by --space to get the rest\n",
			res.TruncatedAt)
	}
}

func confService() (*app.ConfluenceService, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return app.NewConfluence(cfg, version.Version)
}

func newConfCmd() *cobra.Command {
	c := &cobra.Command{Use: "conf", Short: "Confluence: mirror, read, validate, push (native storage format)"}
	c.AddCommand(
		confSearchCmd(), confSpaceCmd(), confPageCmd(),
		confPullCmd(), confStatusCmd(), confValidateCmd(), confPushCmd(), confCommentCmd(),
	)
	return c
}

func confSearchCmd() *cobra.Command {
	var cql, cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search pages by CQL → id/title/space/version/excerpt",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cql == "" {
				return usageErr("--cql is required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			hits, next, err := svc.Search(cmd.Context(), cql, limit, cursor)
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"results": hits, "next_cursor": next}, func() string {
				var b strings.Builder
				for _, h := range hits {
					fmt.Fprintf(&b, "%s\tv%d\t%s\t%s\n", h.ID, h.Version, h.Space, h.Title)
				}
				return strings.TrimRight(b.String(), "\n")
			}, func() []string {
				ids := make([]string, len(hits))
				for i, h := range hits {
					ids[i] = h.ID
				}
				return ids
			})
		},
	}
	cmd.Flags().StringVar(&cql, "cql", "", "Confluence CQL query")
	cmd.Flags().IntVar(&limit, "limit", 25, "max results")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor (start offset)")
	return cmd
}

func confSpaceCmd() *cobra.Command {
	c := &cobra.Command{Use: "space", Short: "Space-level operations"}
	var space string
	var depth int
	tree := &cobra.Command{
		Use:   "tree",
		Short: "Print the page hierarchy of a space",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if space == "" {
				return usageErr("--space is required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			refs, err := svc.Tree(cmd.Context(), space, depth)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"pages": refs}, func() string {
				var b strings.Builder
				for _, r := range refs {
					fmt.Fprintf(&b, "%s\t%s\t(parent %s)\n", r.ID, r.Title, r.Parent)
				}
				return strings.TrimRight(b.String(), "\n")
			})
		},
	}
	tree.Flags().StringVar(&space, "space", "", "space key")
	tree.Flags().IntVar(&depth, "depth", 0, "max depth (0 = unlimited)")
	c.AddCommand(tree)
	return c
}

func confPageCmd() *cobra.Command {
	c := &cobra.Command{Use: "page", Short: "Page get/meta/history/create/move/delete"}
	var id, format string
	get := &cobra.Command{
		Use:   "get",
		Short: "Print a page body (csf|view)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id == "" {
				return usageErr("--id is required")
			}
			if format != "csf" && format != "view" {
				return usageErr("--format must be csf or view")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			page, err := svc.Get(cmd.Context(), id, format)
			if err != nil {
				return err
			}
			// Body is text; print raw for piping.
			if outputFormat == "text" {
				fmt.Fprintln(cmd.OutOrStdout(), string(page.Body))
				return nil
			}
			return emit(cmd, map[string]any{
				"id": page.ID, "title": page.Title, "space": page.SpaceKey,
				"version": page.Version, "body": string(page.Body), "url": page.URL,
			}, nil)
		},
	}
	get.Flags().StringVar(&id, "id", "", "page id")
	get.Flags().StringVar(&format, "format", "csf", "csf|view")

	var metaID string
	meta := &cobra.Command{
		Use:   "meta",
		Short: "Print page metadata (version/ancestors/labels/restrictions)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if metaID == "" {
				return usageErr("--id is required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			m, err := svc.Meta(cmd.Context(), metaID)
			if err != nil {
				return err
			}
			return emit(cmd, m, nil)
		},
	}
	meta.Flags().StringVar(&metaID, "id", "", "page id")

	var histID string
	hist := &cobra.Command{
		Use:   "history",
		Short: "List page versions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if histID == "" {
				return usageErr("--id is required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			vs, err := svc.History(cmd.Context(), histID)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"versions": vs}, nil)
		},
	}
	hist.Flags().StringVar(&histID, "id", "", "page id")

	var space, parent, title, fromFile string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a page (body = CSF via --from-file -)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if space == "" || title == "" {
				return usageErr("--space and --title are required")
			}
			body, err := readBody(fromFile)
			if err != nil {
				return err
			}
			if probs := csf.Validate(body); csf.HasErrors(probs) {
				// Emit the problems, but exit non-zero so an agent learns the page
				// was NOT created (previously this returned exit 0 — a silent no-op).
				_ = emit(cmd, map[string]any{"problems": probs}, nil)
				return usageErr("CSF not well-formed (see problems); page not created")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			page, err := svc.Create(cmd.Context(), space, parent, title, body)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"id": page.ID, "title": page.Title, "version": page.Version, "url": page.URL}, nil)
		},
	}
	create.Flags().StringVar(&space, "space", "", "space key")
	create.Flags().StringVar(&parent, "parent", "", "parent page id")
	create.Flags().StringVar(&title, "title", "", "page title")
	create.Flags().StringVar(&fromFile, "from-file", "-", "CSF body file or - for stdin")

	var moveID, moveParent string
	move := &cobra.Command{
		Use:   "move",
		Short: "Reparent a page",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if moveID == "" || moveParent == "" {
				return usageErr("--id and --parent are required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			if err := svc.Move(cmd.Context(), moveID, moveParent); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"id": moveID, "parent": moveParent, "status": "moved"}, nil)
		},
	}
	move.Flags().StringVar(&moveID, "id", "", "page id")
	move.Flags().StringVar(&moveParent, "parent", "", "new parent page id")

	var delID string
	del := &cobra.Command{
		Use:   "delete",
		Short: "Trash a page (may be 403 by per-space perms → exit 6)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if delID == "" {
				return usageErr("--id is required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			if err := svc.Delete(cmd.Context(), delID); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"id": delID, "status": "trashed"}, nil)
		},
	}
	del.Flags().StringVar(&delID, "id", "", "page id")

	c.AddCommand(get, meta, hist, create, move, del)
	return c
}

func confPullCmd() *cobra.Command {
	var o app.PullOpts
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Mirror pages (.csf + .md + .meta.json + assets) by --id/--cql/--space",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Mutually exclusive selectors. Checked here (not via
			// MarkFlagsMutuallyExclusive) so the violation is a usage error (exit 2)
			// rather than cobra's generic error (exit 1).
			set := 0
			for _, v := range []string{o.ID, o.CQL, o.Space} {
				if v != "" {
					set++
				}
			}
			if set > 1 {
				return usageErr("--id, --cql and --space are mutually exclusive")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			res, err := svc.Pull(cmd.Context(), o)
			if err != nil {
				return err
			}
			// Warn on stderr (never stdout — that would corrupt the JSON result).
			warnIfTruncated(cmd.ErrOrStderr(), res)
			return emit(cmd, res, func() string {
				var b strings.Builder
				fmt.Fprintf(&b, "mirror: %s (%d pages)\n", res.Root, len(res.Pages))
				for _, p := range res.Pages {
					fmt.Fprintf(&b, "  %s  v%d  %s  [assets:%d]\n", p.ID, p.Version, p.Path, p.Assets)
				}
				return strings.TrimRight(b.String(), "\n")
			})
		},
	}
	cmd.Flags().StringVar(&o.ID, "id", "", "page id")
	cmd.Flags().StringVar(&o.CQL, "cql", "", "CQL selecting pages")
	cmd.Flags().StringVar(&o.Space, "space", "", "space key (whole space)")
	cmd.Flags().IntVar(&o.Depth, "depth", 0, "space depth limit")
	cmd.Flags().BoolVar(&o.Assets, "assets", false, "download diagram/image renders")
	cmd.Flags().StringVar(&o.Into, "into", mirrorRootDefault("mirror"), "mirror root dir (default: $ATL_MIRROR_ROOT or \"mirror\")")
	return cmd
}

func confStatusCmd() *cobra.Command {
	var remote bool
	cmd := &cobra.Command{
		Use:   "status [DIR]",
		Short: "Show locally-edited and remote-drifted pages",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := mirrorRootDefault("mirror")
			if len(args) == 1 {
				dir = args[0]
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			entries, err := svc.Status(cmd.Context(), dir, remote)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"entries": entries}, func() string {
				var b strings.Builder
				for _, e := range entries {
					flag := "   "
					if e.LocallyEdited {
						flag = "M  "
					}
					if e.Drifted {
						flag = "M↯ "
					}
					// A page whose remote check failed must not read as clean/in-sync;
					// mark it so the human "safe to push?" view shows the uncertainty.
					if e.RemoteError != "" {
						if e.LocallyEdited {
							flag = "M? "
						} else {
							flag = " ? "
						}
					}
					fmt.Fprintf(&b, "%s%s\t%s", flag, e.ID, e.Path)
					if e.RemoteError != "" {
						fmt.Fprintf(&b, "\t(remote: %s)", e.RemoteError)
					}
					b.WriteByte('\n')
				}
				return strings.TrimRight(b.String(), "\n")
			})
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "also check remote drift (one request per page)")
	return cmd
}

func confValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file.csf>",
		Short: "Validate CSF well-formedness + sanity → machine-readable problems",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readBody(args[0])
			if err != nil {
				return err
			}
			problems := csf.Validate(body)
			err = nil
			if csf.HasErrors(problems) {
				err = fmt.Errorf("%s: not well-formed", args[0])
			}
			_ = emit(cmd, map[string]any{"file": args[0], "ok": !csf.HasErrors(problems), "problems": problems}, nil)
			return err
		},
	}
}

func confPushCmd() *cobra.Command {
	var o app.PushOpts
	cmd := &cobra.Command{
		Use:   "push <file.csf|DIR>",
		Short: "Validate + push under the version gate; --dry-run prints consequences",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := confService()
			if err != nil {
				return err
			}
			res, perr := svc.Push(cmd.Context(), args[0], o)
			_ = emit(cmd, res, func() string { return pushText(res) })
			return perr
		},
	}
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "show consequences without pushing")
	cmd.Flags().BoolVar(&o.Force, "force", false, "override the version gate (clobber remote drift)")
	cmd.Flags().StringVar(&o.Into, "into", "", "mirror root (defaults to nearest .atl)")
	return cmd
}

func pushText(res *app.PushResult) string {
	var b strings.Builder
	for _, it := range res.Items {
		state := "ok"
		switch {
		case it.Failed != "":
			state = "FAILED(" + it.Failed + ")"
		case it.Skipped != "":
			state = it.Skipped
		case it.DryRun:
			state = "dry-run"
			if it.Drifted {
				state = "dry-run/DRIFTED"
			}
		case it.Pushed:
			state = fmt.Sprintf("pushed v%d", it.NewVersion)
		case len(it.Problems) > 0 && csfHasErr(it.Problems):
			state = "INVALID"
		}
		fmt.Fprintf(&b, "%s\t%s\n", state, it.Path)
		if it.Warning != "" {
			fmt.Fprintf(&b, "   ⚠ %s\n", it.Warning)
		}
		for _, r := range it.Removed {
			fmt.Fprintf(&b, "   - removes %s %s\n", r.Kind, r.Display)
		}
		for _, p := range it.Problems {
			fmt.Fprintf(&b, "   ! %s:%d:%d %s\n", p.Severity, p.Line, p.Col, p.Message)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func csfHasErr(ps []csf.Problem) bool { return csf.HasErrors(ps) }

func confCommentCmd() *cobra.Command {
	c := &cobra.Command{Use: "comment", Short: "Page comments"}
	var id string
	list := &cobra.Command{
		Use:   "list",
		Short: "List comments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id == "" {
				return usageErr("--id is required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			cs, err := svc.Comments(cmd.Context(), id)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"comments": cs}, nil)
		},
	}
	list.Flags().StringVar(&id, "id", "", "page id")

	var addID, fromFile string
	add := &cobra.Command{
		Use:   "add",
		Short: "Add a comment (body = CSF via --from-file -)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if addID == "" {
				return usageErr("--id is required")
			}
			body, err := readBody(fromFile)
			if err != nil {
				return err
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			cm, err := svc.AddComment(cmd.Context(), addID, body)
			if err != nil {
				return err
			}
			return emit(cmd, cm, nil)
		},
	}
	add.Flags().StringVar(&addID, "id", "", "page id")
	add.Flags().StringVar(&fromFile, "from-file", "-", "comment body file or - for stdin")

	c.AddCommand(list, add)
	return c
}
