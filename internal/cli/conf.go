package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/version"
)

// warnIfTruncated writes a one-line stderr warning when a pull hit a selection
// cap (the --cql id cap or the --space tree cap), so the caller is told the
// mirror is incomplete. It writes to w (the command's stderr) and never to
// stdout, keeping the JSON result clean.
func warnIfTruncated(w io.Writer, res *app.PullResult) {
	if res != nil && res.Truncated {
		fmt.Fprintf(w,
			"warning: selection truncated at %d pages (safety cap) — the rest was NOT mirrored; narrow the query or pull subsets\n",
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
		confPullCmd(), confStatusCmd(), confValidateCmd(), confPushCmd(), confTableCmd(), confCommentCmd(),
		confAttachmentCmd(), confMeCmd(),
	)
	return c
}

func confSearchCmd() *cobra.Command {
	var cql, cursor string
	var limit int
	var srchSpace, srchTitle, srchLabel, srchType string
	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search pages by CQL → id/title/space/version/excerpt",
		RunE: func(cmd *cobra.Command, _ []string) error {
			hasConv := srchSpace != "" || srchTitle != "" || srchLabel != "" || srchType != ""
			if cql != "" && hasConv {
				return usageErr("--cql cannot be combined with --space/--title/--label/--type")
			}
			if cql == "" {
				cql = buildSearchCQL(srchSpace, srchTitle, srchLabel, srchType)
			}
			if cql == "" {
				return usageErr("--cql or at least one of --space/--title/--label/--type is required")
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
	cmd.Flags().StringVar(&srchSpace, "space", "", "filter by space key")
	cmd.Flags().StringVar(&srchTitle, "title", "", "filter by title (substring match)")
	cmd.Flags().StringVar(&srchLabel, "label", "", "filter by label")
	cmd.Flags().StringVar(&srchType, "type", "", "filter by content type (e.g. page, blogpost)")
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
			refs, truncated, err := svc.Tree(cmd.Context(), space, depth)
			if err != nil {
				return err
			}
			out := map[string]any{"pages": refs}
			if truncated {
				out["truncated"] = true
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: space listing truncated at %d pages (safety cap) — the rest is NOT shown\n", len(refs))
			}
			return emit(cmd, out, func() string {
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
	_ = get.RegisterFlagCompletionFunc("format", fixedComp("csf", "view"))

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

	var listSpace, listStatus, listCursor string
	var listLimit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List pages in a space",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listSpace == "" {
				return usageErr("--space is required")
			}
			q := buildSearchCQL(listSpace, "", "", "") + ` AND type = page`
			if listStatus != "" {
				q += ` AND status = ` + cqlEscape(listStatus)
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			hits, next, err := svc.Search(cmd.Context(), q, listLimit, listCursor)
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
	list.Flags().StringVar(&listSpace, "space", "", "space key")
	list.Flags().StringVar(&listStatus, "status", "", "current|archived|trashed")
	_ = list.RegisterFlagCompletionFunc("status", fixedComp("current", "archived", "trashed"))
	list.Flags().IntVar(&listLimit, "limit", 25, "max results")
	list.Flags().StringVar(&listCursor, "cursor", "", "pagination cursor (start offset)")

	var openID string
	open := &cobra.Command{
		Use:   "open",
		Short: "Open a page in the system browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if openID == "" {
				return usageErr("--id is required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			m, err := svc.Meta(cmd.Context(), openID)
			if err != nil {
				return err
			}
			if m.URL == "" {
				return fmt.Errorf("%w: page %s has no web URL", domain.ErrNotFound, openID)
			}
			if err := defaultBrowserOpener(cmd.Context(), m.URL); err != nil {
				return fmt.Errorf("open browser: %w", err)
			}
			return emit(cmd, map[string]string{"id": openID, "url": m.URL}, func() string {
				return m.URL
			})
		},
	}
	open.Flags().StringVar(&openID, "id", "", "page id")

	var copyID, copyTitle, copySpace, copyParent string
	cp := &cobra.Command{
		Use:   "copy",
		Short: "Copy a page (same CSF body, new title/space/parent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if copyID == "" || copyTitle == "" {
				return usageErr("--id and --title are required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			page, err := svc.CopyPage(cmd.Context(), copyID, copyTitle, copySpace, copyParent)
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"id": page.ID, "title": page.Title, "version": page.Version, "url": page.URL},
				nil, func() []string { return []string{page.ID} })
		},
	}
	cp.Flags().StringVar(&copyID, "id", "", "source page id")
	cp.Flags().StringVar(&copyTitle, "title", "", "new page title")
	cp.Flags().StringVar(&copySpace, "space", "", "target space key (default: same as source)")
	cp.Flags().StringVar(&copyParent, "parent", "", "target parent page id (default: same as source)")

	c.AddCommand(get, meta, hist, list, open, cp, create, move, del)
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

func confTableCmd() *cobra.Command {
	c := &cobra.Command{Use: "table", Short: "Extract Confluence tables from native storage"}
	var id, format, out string
	var table int
	extract := &cobra.Command{
		Use:   "extract",
		Short: "Extract page tables as structured JSON, CSV, or XLSX",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id == "" {
				return usageErr("--id is required")
			}
			switch format {
			case "json", "csv", "xlsx":
			default:
				return usageErr("--format must be json, csv, or xlsx")
			}
			if table < 0 {
				return usageErr("--table must be >= 1")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			res, err := svc.ExtractTables(cmd.Context(), id, table)
			if err != nil {
				return err
			}
			switch format {
			case "json":
				if out != "" {
					data, err := json.MarshalIndent(res, "", "  ")
					if err != nil {
						return err
					}
					data = append(data, '\n')
					if err := os.WriteFile(out, data, 0o644); err != nil {
						return err
					}
					return emit(cmd, map[string]any{"path": out, "format": format, "table_count": res.TableCount}, nil)
				}
				return emit(cmd, res, func() string {
					return fmt.Sprintf("%d table(s)", res.TableCount)
				})
			case "csv":
				data, err := app.RenderConfluenceTableCSV(res)
				if err != nil {
					return err
				}
				if out != "" {
					if err := os.WriteFile(out, data, 0o644); err != nil {
						return err
					}
					return emit(cmd, map[string]any{"path": out, "format": format, "table_count": res.TableCount}, nil)
				}
				_, err = cmd.OutOrStdout().Write(data)
				return err
			case "xlsx":
				if out == "" {
					return usageErr("--out is required for --format xlsx")
				}
				if err := app.WriteConfluenceTableXLSX(out, res); err != nil {
					return err
				}
				return emit(cmd, map[string]any{"path": out, "format": format, "table_count": res.TableCount}, nil)
			default:
				return nil
			}
		},
	}
	extract.Flags().StringVar(&id, "id", "", "page id")
	extract.Flags().IntVar(&table, "table", 0, "1-based table index to extract (0 = all tables)")
	extract.Flags().StringVar(&format, "format", "json", "json|csv|xlsx")
	extract.Flags().StringVar(&out, "out", "", "optional output file (required for xlsx)")
	_ = extract.RegisterFlagCompletionFunc("format", fixedComp("json", "csv", "xlsx"))
	c.AddCommand(extract)
	return c
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
			// res is nil when target resolution failed before any push attempt;
			// emitting it would print a stray "null" (json) or panic in pushText.
			if res != nil {
				_ = emit(cmd, res, func() string { return pushText(res) })
			}
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

func confAttachmentCmd() *cobra.Command {
	c := &cobra.Command{Use: "attachment", Short: "Attachment list/get/upload/delete"}

	var listID string
	list := &cobra.Command{
		Use:   "list",
		Short: "List attachments on a page",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listID == "" {
				return usageErr("--id is required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			atts, err := svc.Attachments(cmd.Context(), listID)
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"attachments": atts}, func() string {
				var b strings.Builder
				for _, a := range atts {
					fmt.Fprintf(&b, "%s\t%s\t%d bytes\n", a.ID, a.Title, a.FileSize)
				}
				return strings.TrimRight(b.String(), "\n")
			}, func() []string {
				ids := make([]string, len(atts))
				for i, a := range atts {
					ids[i] = a.ID
				}
				return ids
			})
		},
	}
	list.Flags().StringVar(&listID, "id", "", "page id")

	var getPageID, getName, getInto string
	var getVersion int
	get := &cobra.Command{
		Use:   "get",
		Short: "Download an attachment to a directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if getPageID == "" || getName == "" {
				return usageErr("--id and --name are required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			path, err := svc.DownloadAttachment(cmd.Context(), getPageID, getName, getVersion, getInto)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]string{"path": path, "name": getName}, func() string {
				return path
			})
		},
	}
	get.Flags().StringVar(&getPageID, "id", "", "page id")
	get.Flags().StringVar(&getName, "name", "", "attachment filename")
	get.Flags().IntVar(&getVersion, "version", 0, "attachment version (0 = latest)")
	get.Flags().StringVar(&getInto, "into", ".", "output directory")

	var uploadPageID, uploadFile, uploadComment string
	upload := &cobra.Command{
		Use:   "upload",
		Short: "Upload a file as an attachment to a page",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if uploadPageID == "" || uploadFile == "" {
				return usageErr("--id and --file are required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			att, err := svc.UploadAttachment(cmd.Context(), uploadPageID, uploadFile, uploadComment)
			if err != nil {
				return err
			}
			return emit(cmd, att, nil)
		},
	}
	upload.Flags().StringVar(&uploadPageID, "id", "", "page id")
	upload.Flags().StringVar(&uploadFile, "file", "", "local file path to upload")
	upload.Flags().StringVar(&uploadComment, "comment", "", "optional attachment comment")

	var delAttID string
	var delAttForce bool
	del := &cobra.Command{
		Use:   "delete",
		Short: "Delete an attachment by id (requires --force; deletion is permanent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if delAttID == "" {
				return usageErr("--id is required")
			}
			if !delAttForce {
				return usageErr("refusing to delete attachment %s without --force (deletion is permanent)", delAttID)
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			if err := svc.DeleteAttachment(cmd.Context(), delAttID); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"id": delAttID, "status": "deleted"}, nil)
		},
	}
	del.Flags().StringVar(&delAttID, "id", "", "attachment id")
	del.Flags().BoolVar(&delAttForce, "force", false, "confirm permanent deletion")

	c.AddCommand(list, get, upload, del)
	return c
}

func confMeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "me",
		Short: "Print the authenticated Confluence user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := confService()
			if err != nil {
				return err
			}
			name, err := svc.Whoami(cmd.Context())
			if err != nil {
				return err
			}
			return emit(cmd, map[string]string{"displayName": name}, func() string {
				return name
			})
		},
	}
}
