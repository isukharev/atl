package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mdcsf"
	"github.com/isukharev/atl/internal/version"
)

// warnIfTruncated writes a one-line stderr warning when a pull hit a selection
// cap (the --cql id cap or the --space tree cap), so the caller is told the
// mirror is incomplete. It writes to w (the command's stderr) and never to
// stdout, keeping the JSON result clean.
func warnIfTruncated(w io.Writer, res *app.PullResult) {
	if res == nil {
		return
	}
	if res.Truncated {
		fmt.Fprintf(w,
			"warning: selection truncated at %d pages (safety cap) — the rest was NOT mirrored; narrow the query or pull subsets\n",
			res.TruncatedAt)
	}
	if res.CommentsTruncated {
		fmt.Fprint(w,
			"warning: some pages' comments hit the fetch cap — the mirrored comments sidecars are incomplete\n")
	}
}

// createBody resolves a body flag pair: raw CSF from --from-file, or markdown
// converted whole-document via mdcsf when --from-md is set. The two are
// mutually exclusive. A conversion failure maps to ErrCheckFailed (exit 8) —
// fail-closed, nothing is sent to the backend.
func createBody(cmd *cobra.Command, fromFile, fromMD string) ([]byte, error) {
	// Dispatch on the flag being set, not its value: `--from-md ""` (e.g. an
	// empty shell variable) must not silently fall back to CSF-from-stdin.
	if !cmd.Flags().Changed("from-md") {
		return readBody(fromFile)
	}
	if fromMD == "" {
		return nil, usageErr("--from-md requires a file path or - for stdin")
	}
	if cmd.Flags().Changed("from-file") {
		return nil, usageErr("--from-file and --from-md are mutually exclusive")
	}
	md, err := readBody(fromMD)
	if err != nil {
		return nil, err
	}
	body, err := mdcsf.ConvertDocument(string(md))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot convert markdown body: %v (constructs outside the md subset need a CSF body via --from-file)", domain.ErrCheckFailed, err)
	}
	return body, nil
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
		confSearchCmd(), confSpaceCmd(), confPageCmd(), confBlogCmd(),
		confPullCmd(), confRenderCmd(), confStatusCmd(), confDiffCmd(), confPlanCmd(), confValidateCmd(), confEditCmd(), confApplyCmd(), confPushCmd(), confTableCmd(), confCommentCmd(),
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
	c := &cobra.Command{Use: "page", Short: "Page get/view/title/meta/history/create/move/delete"}
	resolve := &cobra.Command{
		Use:   "resolve <ID-OR-URL>",
		Short: "Resolve a safe page reference to its stable content id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := confService()
			if err != nil {
				return err
			}
			result, err := svc.ResolvePageReference(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emitID(cmd, result, func() string { return result.ID }, func() []string { return []string{result.ID} })
		},
	}
	outline := &cobra.Command{
		Use:   "outline <ID-OR-URL>",
		Short: "List structural page headings without rendering the full body",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := confService()
			if err != nil {
				return err
			}
			result, err := svc.PageOutline(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return app.ConfluenceOutlineMarkdown(result) })
		},
	}
	var sectionHeading string
	var sectionOccurrence, sectionMaxBytes int
	section := &cobra.Command{
		Use:   "section <ID-OR-URL>",
		Short: "Render one structurally bounded page section as Markdown",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := confService()
			if err != nil {
				return err
			}
			result, err := svc.PageSection(cmd.Context(), args[0], app.ConfluencePageSectionOpts{
				Heading: sectionHeading, Occurrence: sectionOccurrence, MaxBytes: sectionMaxBytes,
			})
			if err != nil {
				return err
			}
			if outputFormat == "text" {
				_, err := io.WriteString(cmd.OutOrStdout(), result.Markdown)
				return err
			}
			return emit(cmd, result, nil)
		},
	}
	section.Flags().StringVar(&sectionHeading, "heading", "", "exact heading text (case/whitespace normalized)")
	section.Flags().IntVar(&sectionOccurrence, "occurrence", 0, "1-based occurrence when the heading is duplicated")
	section.Flags().IntVar(&sectionMaxBytes, "max-bytes", 256<<10, "maximum Markdown bytes (1..1048576; truncates at block boundary)")
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
	get.Flags().StringVar(&id, "id", "", "page id or supported same-origin URL")
	get.Flags().StringVar(&format, "format", "csf", "csf|view")
	_ = get.RegisterFlagCompletionFunc("format", fixedComp("csf", "view"))

	view := confPageViewCmd()
	titleCmd := confPageTitleCmd()
	labelsCmd := confPageLabelsCmd()

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
			return emit(cmd, m, func() string { return confluencePageMetaText(m) })
		},
	}
	meta.Flags().StringVar(&metaID, "id", "", "page id or supported same-origin URL")

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
			return emit(cmd, map[string]any{"versions": vs}, func() string { return confluenceVersionsText(vs) })
		},
	}
	hist.Flags().StringVar(&histID, "id", "", "page id or supported same-origin URL")

	var space, parent, title, fromFile, fromMD string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a page (body = CSF via --from-file -, or markdown via --from-md)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if space == "" || title == "" {
				return usageErr("--space and --title are required")
			}
			body, err := createBody(cmd, fromFile, fromMD)
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
	create.Flags().StringVar(&fromMD, "from-md", "", "markdown body file or - for stdin (converted to CSF; unsupported constructs are refused)")

	var moveParent, moveExpectedParent, moveExpectedHash string
	var moveExpectedVersion int
	var moveApply bool
	move := &cobra.Command{
		Use:   "move <ID>",
		Short: "Preview or apply a guarded page move",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(moveParent) == "" {
				return usageErr("--parent is required")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			res, moveErr := svc.MoveGuarded(cmd.Context(), args[0], app.ConfluenceMoveOpts{
				Parent: moveParent, ExpectedVersion: moveExpectedVersion,
				ExpectedParent: moveExpectedParent, ExpectedParentSet: cmd.Flags().Changed("expected-parent"),
				ExpectedProposalHash: moveExpectedHash, Apply: moveApply,
			})
			if res != nil {
				if emitErr := emit(cmd, res, func() string {
					return fmt.Sprintf("%s\t%s\tv%d\t%s\t%s", res.Status, res.ID, res.CurrentVersion, res.ProposalHash, res.Parent)
				}); emitErr != nil {
					return emitErr
				}
			}
			return moveErr
		},
	}
	move.Flags().StringVar(&moveParent, "parent", "", "new parent page id")
	move.Flags().IntVar(&moveExpectedVersion, "expected-version", 0, "reviewed current page version (required with --apply)")
	move.Flags().StringVar(&moveExpectedParent, "expected-parent", "", "reviewed current parent id; use --expected-parent= for top-level (required with --apply)")
	move.Flags().StringVar(&moveExpectedHash, "expected-proposal-hash", "", "reviewed proposal hash (required with --apply)")
	move.Flags().BoolVar(&moveApply, "apply", false, "perform the guarded move (default: dry-run)")

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
			return emit(cmd, map[string]string{"id": m.ID, "url": m.URL}, func() string {
				return m.URL
			})
		},
	}
	open.Flags().StringVar(&openID, "id", "", "page id or supported same-origin URL")

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

	c.AddCommand(resolve, outline, section, get, view, titleCmd, labelsCmd, meta, hist, list, open, cp, create, move, del)
	return c
}

func confPageViewCmd() *cobra.Command {
	var root, jiraView string
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "view <ID-OR-URL>",
		Short: "Render one page as configured Markdown without writing a mirror",
		Long: "Fetch native CSF and render one Confluence page through the configured Markdown view without writing mirror artifacts. " +
			"Default JSON contains stable page identity, version, and markdown; -o text emits raw Markdown. " +
			"Every region is read-only because this creates no writeback baseline: pull the page before editing it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			override, err := rf.override()
			if err != nil {
				return err
			}
			configRoot, err := filepath.Abs(root)
			if err != nil {
				return err
			}
			if detected, ok := app.MirrorRootOf(configRoot); ok {
				configRoot = detected
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			res, err := svc.ViewPage(cmd.Context(), args[0], app.ConfluencePageViewOpts{Root: configRoot, Render: override, JiraView: jiraView})
			if err != nil {
				return err
			}
			warnRender(cmd.ErrOrStderr(), res.Warnings)
			if outputFormat == "text" {
				_, err := io.WriteString(cmd.OutOrStdout(), res.Markdown)
				return err
			}
			return emit(cmd, res, nil)
		},
	}
	cmd.Flags().StringVar(&root, "render-root", mirrorRootDefault("."), "root whose .atl/config.json supplies local render settings (never written)")
	cmd.Flags().StringVar(&jiraView, "jira-view", "", "named Jira list view for JQL macros (default: default; macro columns win)")
	rf.register(cmd)
	rf.registerConfluenceJiraMacros(cmd)
	return cmd
}

func confPullCmd() *cobra.Command {
	var o app.PullOpts
	var rf renderFlags
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
			if o.Incremental && o.Complete {
				return usageErr("--incremental and --complete are mutually exclusive")
			}
			if o.Incremental {
				if o.ID != "" || (o.CQL == "" && o.Space == "") {
					return usageErr("--incremental requires --cql or --space and cannot use --id")
				}
				if o.Space != "" && o.Depth != 0 {
					return usageErr("--incremental --space does not support --depth")
				}
				if o.MaxPages < 0 {
					return usageErr("--max-pages must be >= 0")
				}
				if cmd.Flags().Changed("time-zone") {
					return usageErr("--time-zone was removed; pass an explicit offset in RFC3339 --since instead")
				}
			} else if o.Complete {
				if o.ID != "" || (o.CQL == "" && o.Space == "") {
					return usageErr("--complete requires --cql or --space and cannot use --id")
				}
				if o.Space != "" && o.Depth != 0 {
					return usageErr("--complete --space does not support --depth")
				}
				if o.MaxPages < 0 {
					return usageErr("--max-pages must be >= 0")
				}
				if o.Since != "" || cmd.Flags().Changed("time-zone") {
					return usageErr("--since and --time-zone cannot be used with --complete")
				}
			} else if o.Since != "" || o.RestartComplete || cmd.Flags().Changed("time-zone") || cmd.Flags().Changed("max-pages") {
				return usageErr("--since and --max-pages require --incremental or --complete; --restart-complete requires --complete; --time-zone was removed")
			}
			override, err := rf.override()
			if err != nil {
				return err
			}
			o.Render = override
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
			warnRender(cmd.ErrOrStderr(), res.Warnings)
			return emit(cmd, res, func() string {
				var b strings.Builder
				fmt.Fprintf(&b, "mirror: %s (%d pages)\n", res.Root, len(res.Pages))
				if res.Incremental != nil {
					inc := res.Incremental
					fmt.Fprintf(&b, "incremental: complete=%t source=%s watermark_instant=%s query_literal=%s query_literal_basis=%s backend_query_time_zone=%s safety_overlap_hours=%d next=%s matched=%d selected=%d overlap_skipped=%d boundary_skipped=%d view_migrations=%d watermark_advanced=%t\n", inc.Complete, inc.WatermarkSource, inc.WatermarkInstant, inc.QueryLiteral, inc.QueryLiteralBasis, inc.BackendQueryTimeZone, inc.SafetyOverlapHours, inc.NextInstant, inc.Matched, inc.Selected, inc.OverlapSkipped, inc.BoundarySkipped, inc.ViewMigrations, inc.WatermarkAdvanced)
				}
				if res.Complete != nil {
					complete := res.Complete
					fmt.Fprintf(&b, "complete-pull: complete=%t source=%s total=%d completed=%d remaining=%d checkpoint_active=%t selector_sha256=%s selection_sha256=%s view_migrations=%d\n", complete.Complete, complete.Source, complete.Total, complete.Completed, complete.Remaining, complete.CheckpointActive, complete.SelectorSHA256, complete.SelectionSHA256, complete.ViewMigrations)
				}
				for _, p := range res.Pages {
					if o.Comments && p.Comments != nil {
						fmt.Fprintf(&b, "  %s  v%d  %s  [assets:%d comments:%d]\n", p.ID, p.Version, p.Path, p.Assets, *p.Comments)
					} else {
						fmt.Fprintf(&b, "  %s  v%d  %s  [assets:%d]\n", p.ID, p.Version, p.Path, p.Assets)
					}
				}
				return strings.TrimRight(b.String(), "\n")
			})
		},
	}
	cmd.Flags().StringVar(&o.ID, "id", "", "page id or supported same-origin URL")
	cmd.Flags().StringVar(&o.CQL, "cql", "", "CQL selecting pages")
	cmd.Flags().StringVar(&o.Space, "space", "", "space key (whole space)")
	cmd.Flags().IntVar(&o.Depth, "depth", 0, "space depth limit")
	cmd.Flags().BoolVar(&o.Assets, "assets", false, "download diagram/image renders")
	cmd.Flags().BoolVar(&o.Comments, "comments", false, "mirror page comments into <slug>.comments.json/.md sidecars")
	cmd.Flags().StringVar(&o.Into, "into", mirrorRootDefault("mirror"), "mirror root dir (default: $ATL_MIRROR_ROOT or \"mirror\")")
	cmd.Flags().StringVar(&o.JiraView, "jira-view", "", "named Jira list view for JQL macros (default: default; macro columns win)")
	cmd.Flags().BoolVar(&o.Incremental, "incremental", false, "pull a complete changed-page delta using a selector-bound watermark")
	cmd.Flags().BoolVar(&o.Complete, "complete", false, "exhaust and resume one exact two-pass selector snapshot (no ordinary 1000/2000 cap)")
	cmd.Flags().BoolVar(&o.RestartComplete, "restart-complete", false, "replace an unfinished complete-pull snapshot after fresh selection and local preflight")
	cmd.Flags().StringVar(&o.Since, "since", "", "first-run lower boundary as an exact RFC3339 minute with explicit offset")
	cmd.Flags().StringVar(&o.TimeZone, "time-zone", "", "removed: put the explicit offset in --since")
	_ = cmd.Flags().MarkHidden("time-zone")
	cmd.Flags().IntVar(&o.MaxPages, "max-pages", 0, "selection cap (incremental default 10000; complete 0 means no configured cap)")
	rf.register(cmd)
	rf.registerConfluenceJiraMacros(cmd)
	return cmd
}

func confRenderCmd() *cobra.Command {
	var into string
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "render [DIR|FILE.md|FILE.csf]",
		Short: "Regenerate .md views from local .csf + meta + sidecars (offline; no network/PAT)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := into
			if len(args) == 1 {
				target = args[0]
			}
			override, err := rf.override()
			if err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			svc := app.NewConfluenceRenderer(cfg)
			res, err := svc.Render(target, override)
			if err != nil {
				return err
			}
			warnRender(cmd.ErrOrStderr(), res.Warnings)
			return emit(cmd, res, func() string {
				var b strings.Builder
				fmt.Fprintf(&b, "mirror: %s (%d pages)\n", res.Root, len(res.Rendered))
				for _, r := range res.Rendered {
					fmt.Fprintf(&b, "  %s  %s\n", r.ID, r.Path)
				}
				return strings.TrimRight(b.String(), "\n")
			})
		},
	}
	cmd.Flags().StringVar(&into, "into", mirrorRootDefault("mirror"), "mirror root dir when no target is given")
	rf.register(cmd)
	return cmd
}

func confTableCmd() *cobra.Command {
	c := &cobra.Command{Use: "table", Short: "Extract Confluence tables from native storage"}
	var id, format, out string
	var table int
	var rawCSV bool
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
			if rawCSV && format != "csv" {
				return usageErr("--raw-csv requires --format csv")
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
					return emit(cmd, map[string]any{"path": out, "format": format, "table_count": res.TableCount}, func() string {
						return fmt.Sprintf("%s\tformat=%s\ttables=%d", out, format, res.TableCount)
					})
				}
				return emit(cmd, res, func() string {
					return fmt.Sprintf("%d table(s)", res.TableCount)
				})
			case "csv":
				data, err := app.RenderConfluenceTableCSVWithOptions(res, rawCSV)
				if err != nil {
					return err
				}
				if out != "" {
					if err := os.WriteFile(out, data, 0o644); err != nil {
						return err
					}
					return emit(cmd, map[string]any{"path": out, "format": format, "table_count": res.TableCount}, func() string {
						return fmt.Sprintf("%s\tformat=%s\ttables=%d", out, format, res.TableCount)
					})
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
				return emit(cmd, map[string]any{"path": out, "format": format, "table_count": res.TableCount}, func() string {
					return fmt.Sprintf("%s\tformat=%s\ttables=%d", out, format, res.TableCount)
				})
			default:
				return nil
			}
		},
	}
	extract.Flags().StringVar(&id, "id", "", "page id or supported same-origin URL")
	extract.Flags().IntVar(&table, "table", 0, "1-based table index to extract (0 = all tables)")
	extract.Flags().StringVar(&format, "format", "json", "json|csv|xlsx")
	extract.Flags().StringVar(&out, "out", "", "optional output file (required for xlsx)")
	extract.Flags().BoolVar(&rawCSV, "raw-csv", false, "write formula-leading CSV cells verbatim (unsafe in spreadsheets)")
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
					if e.NonCanonical {
						fmt.Fprintf(&b, "S! %s\t%s\t(canonical: %s)\n", e.ID, e.Path, e.CanonicalPath)
						continue
					}
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

func confDiffCmd() *cobra.Command {
	var into string
	cmd := &cobra.Command{
		Use:   "diff [file.csf|DIR]",
		Short: "Compare native mirror bodies with their last-synced baselines (offline)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) == 1 {
				target = args[0]
			} else if into == "" {
				into = mirrorRootDefault("mirror")
			}
			result, diffErr := app.DiffConfluenceMirror(target, into)
			if result != nil {
				emitErr := emit(cmd, result, func() string { return app.ConfluenceDiffMarkdown(result) })
				if diffErr == nil {
					return emitErr
				}
			}
			return diffErr
		},
	}
	cmd.Flags().StringVar(&into, "into", "", "mirror root (defaults to nearest .atl, or configured mirror when no target is given)")
	return cmd
}

func confPlanCmd() *cobra.Command {
	group := &cobra.Command{Use: "plan", Short: "Create and execute review-bound multi-page write plans"}
	var createInto, createOut string
	create := &cobra.Command{
		Use:   "create [file.csf|DIR]",
		Short: "Build a deterministic native update plan (offline)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) == 1 {
				target = args[0]
			} else if createInto == "" {
				createInto = mirrorRootDefault("mirror")
			}
			result, err := app.CreateConfluencePlan(target, createInto, createOut)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return app.ConfluencePlanCreateMarkdown(result) })
		},
	}
	create.Flags().StringVar(&createInto, "into", "", "mirror root (defaults to nearest .atl, or configured mirror when no target is given)")
	create.Flags().StringVar(&createOut, "out", "", "durable private plan file (required)")

	preview := &cobra.Command{
		Use:   "preview <plan.json>",
		Short: "Run the complete read-only local and remote plan preflight",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := confService()
			if err != nil {
				return err
			}
			result, previewErr := svc.PreviewConfluencePlan(cmd.Context(), args[0])
			if result != nil {
				emitErr := emit(cmd, result, func() string { return app.ConfluencePlanApplyMarkdown(result) })
				if previewErr == nil {
					return emitErr
				}
			}
			return previewErr
		},
	}

	var confirm, expectedHash string
	apply := &cobra.Command{
		Use:   "apply <plan.json>",
		Short: "Execute a reviewed plan with exact hash and confirmation gates",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if confirm != "APPLY" {
				return usageErr("--confirm must be exactly APPLY")
			}
			if expectedHash == "" {
				return usageErr("--expected-proposal-hash is required with --confirm APPLY")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			result, applyErr := svc.ApplyConfluencePlan(cmd.Context(), args[0], app.ConfluencePlanApplyOpts{Confirm: confirm, ExpectedProposalHash: expectedHash})
			if result != nil {
				emitErr := emit(cmd, result, func() string { return app.ConfluencePlanApplyMarkdown(result) })
				if applyErr == nil {
					return emitErr
				}
			}
			return applyErr
		},
	}
	apply.Flags().StringVar(&confirm, "confirm", "", "execute only when exactly APPLY (required)")
	apply.Flags().StringVar(&expectedHash, "expected-proposal-hash", "", "exact proposal hash printed by reviewed preview")
	group.AddCommand(create, preview, apply)
	return group
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
			cs, truncated, err := svc.Comments(cmd.Context(), id)
			if err != nil {
				return err
			}
			if truncated {
				fmt.Fprint(cmd.ErrOrStderr(),
					"warning: comment listing hit the fetch cap — some comments were not returned\n")
			}
			return emit(cmd, map[string]any{"comments": cs}, func() string { return commentsText(cs) })
		},
	}
	list.Flags().StringVar(&id, "id", "", "page id or supported same-origin URL")

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
	list.Flags().StringVar(&listID, "id", "", "page id or supported same-origin URL")

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
	get.Flags().StringVar(&getPageID, "id", "", "page id or supported same-origin URL")
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
