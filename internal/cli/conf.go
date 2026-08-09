package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/compose"
	"github.com/isukharev/atl/internal/config"
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

func confService(cmd *cobra.Command) (*app.ConfluenceService, error) {
	cfg, authorizer, err := confluenceCompositionInputs(cmd)
	if err != nil {
		return nil, err
	}
	return compose.NewConfluenceWithWriteAuthorizer(cfg, version.Version, authorizer, invocationCompositionOptions(cmd)...)
}

func confluenceCompositionInputs(cmd *cobra.Command) (*config.Config, domain.WriteAuthorizer, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, nil, err
	}
	authorizer, err := policyAuthorizerFor(invocationRuntimeFor(cmd), "confluence", cfg.ConfluenceURL)
	if err != nil {
		return nil, nil, err
	}
	return cfg, authorizer, nil
}

func confCommentMutationService(cmd *cobra.Command) (*app.ConfluenceService, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	settings, err := compatibility.Load(config.Dir())
	if err != nil {
		return nil, err
	}
	if settings.Confluence == nil {
		return nil, fmt.Errorf("%w: Confluence comment compatibility is not activated; run compatibility pin first", domain.ErrConfig)
	}
	authorizer, err := policyAuthorizerFor(invocationRuntimeFor(cmd), "confluence", cfg.ConfluenceURL)
	if err != nil {
		return nil, err
	}
	return compose.NewConfluenceCommentMutationsWithWriteAuthorizer(cfg, version.Version, *settings.Confluence, authorizer, invocationCompositionOptions(cmd)...)
}

func confScheduledService(cmd *cobra.Command, pagePrefetch, requestsPerSecond int) (*app.ConfluenceService, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	authorizer, err := policyAuthorizerFor(invocationRuntimeFor(cmd), "confluence", cfg.ConfluenceURL)
	if err != nil {
		return nil, err
	}
	return compose.NewConfluenceScheduledWithWriteAuthorizer(cfg, version.Version, pagePrefetch, requestsPerSecond, authorizer, invocationCompositionOptions(cmd)...)
}

func newConfCmd() *cobra.Command {
	c := &cobra.Command{Use: "conf", Short: "Confluence: mirror, read, validate, push (native storage format)"}
	c.AddCommand(
		confSearchCmd(), confSpaceCmd(), confPageCmd(), confBlogCmd(),
		confPullCmd(), confRenderCmd(), confStatusCmd(), confSnapshotCmd(), confDiffCmd(), confReconcileCmd(), confPlanCmd(), confValidateCmd(), confEditCmd(), confApplyCmd(), confPushCmd(), confTableCmd(), confCommentCmd(),
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
			if err := validatePageLimit(limit, 100); err != nil {
				return err
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.SearchQualified(cmd.Context(), cql, limit, cursor)
			if err != nil {
				return err
			}
			return emitID(cmd, result, func() string {
				return confluenceSearchText(result)
			}, func() []string {
				ids := make([]string, len(result.Results))
				for i, h := range result.Results {
					ids[i] = h.ID
				}
				return ids
			})
		},
	}
	cmd.Flags().StringVar(&cql, "cql", "", "Confluence CQL query")
	cmd.Flags().IntVar(&limit, "limit", 25, "max results (1..100)")
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
			svc, err := confService(cmd)
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
			if o.OverwriteLocal && o.StashLocal {
				return usageErr("--overwrite-local and --stash-local are mutually exclusive")
			}
			if o.PagePrefetch < 1 || o.PagePrefetch > 8 {
				return usageErr("--page-prefetch must be between 1 and 8")
			}
			if o.RequestsPerSecond < 0 || o.RequestsPerSecond > 1000 {
				return usageErr("--requests-per-second must be between 0 and 1000")
			}
			if o.ID != "" && (o.PagePrefetch > 1 || o.RequestsPerSecond > 0) {
				return usageErr("--page-prefetch and --requests-per-second scheduling requires --cql or --space")
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
			var svc *app.ConfluenceService
			if o.Incremental || o.Complete || o.PagePrefetch > 1 || o.RequestsPerSecond > 0 {
				svc, err = confScheduledService(cmd, o.PagePrefetch, o.RequestsPerSecond)
			} else {
				svc, err = confService(cmd)
			}
			if err != nil {
				return err
			}
			res, err := svc.Pull(cmd.Context(), o)
			if err != nil && (res == nil || res.LocalSafety == nil || !errors.Is(err, domain.ErrCheckFailed)) {
				return err
			}
			// Warn on stderr (never stdout — that would corrupt the JSON result).
			warnIfTruncated(cmd.ErrOrStderr(), res)
			warnRender(cmd.ErrOrStderr(), res.Warnings)
			emitErr := emit(cmd, res, func() string {
				var b strings.Builder
				fmt.Fprintf(&b, "mirror: %s (%d pages)\n", res.Root, len(res.Pages))
				appendPullLocalSafetyText(&b, res.LocalSafety)
				if res.Incremental != nil {
					inc := res.Incremental
					fmt.Fprintf(&b, "incremental: complete=%t source=%s watermark_instant=%s query_literal=%s query_literal_basis=%s backend_query_time_zone=%s safety_overlap_hours=%d next=%s matched=%d selected=%d overlap_skipped=%d boundary_skipped=%d view_migrations=%d watermark_advanced=%t\n", inc.Complete, inc.WatermarkSource, inc.WatermarkInstant, inc.QueryLiteral, inc.QueryLiteralBasis, inc.BackendQueryTimeZone, inc.SafetyOverlapHours, inc.NextInstant, inc.Matched, inc.Selected, inc.OverlapSkipped, inc.BoundarySkipped, inc.ViewMigrations, inc.WatermarkAdvanced)
				}
				if res.Complete != nil {
					complete := res.Complete
					fmt.Fprintf(&b, "complete-pull: complete=%t source=%s total=%d completed=%d remaining=%d checkpoint_active=%t selector_sha256=%s selection_sha256=%s view_migrations=%d\n", complete.Complete, complete.Source, complete.Total, complete.Completed, complete.Remaining, complete.CheckpointActive, complete.SelectorSHA256, complete.SelectionSHA256, complete.ViewMigrations)
				}
				if schedule := res.Scheduling; schedule != nil {
					fmt.Fprintf(&b, "scheduling: page_prefetch=%d max_in_flight=%d requests_per_second=%d\n", schedule.PagePrefetch, schedule.MaxInFlight, schedule.RequestsPerSecond)
				}
				for _, p := range res.Pages {
					if o.Comments && p.Comments != nil {
						fmt.Fprintf(&b, "  %s  v%d  %s  [assets:%d comments:%d]", p.ID, p.Version, p.Path, p.Assets, *p.Comments)
					} else {
						fmt.Fprintf(&b, "  %s  v%d  %s  [assets:%d]", p.ID, p.Version, p.Path, p.Assets)
					}
					if p.Status != "" {
						fmt.Fprintf(&b, "  %s", p.Status)
					}
					b.WriteByte('\n')
				}
				return strings.TrimRight(b.String(), "\n")
			})
			if emitErr != nil {
				return emitErr
			}
			return err
		},
	}
	cmd.Flags().StringVar(&o.ID, "id", "", "page id or supported same-origin URL")
	cmd.Flags().StringVar(&o.CQL, "cql", "", "CQL selecting pages")
	cmd.Flags().StringVar(&o.Space, "space", "", "space key (whole space)")
	cmd.Flags().IntVar(&o.Depth, "depth", 0, "space depth limit")
	cmd.Flags().BoolVar(&o.Assets, "assets", false, "download diagram/image renders")
	cmd.Flags().BoolVar(&o.Comments, "comments", false, "mirror page comments into <slug>.comments.json/.md sidecars")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "qualify the pull without writing mirror files, state, watermarks, or checkpoints")
	cmd.Flags().BoolVar(&o.OverwriteLocal, "overwrite-local", false, "explicitly replace qualified locally edited native .csf bytes")
	cmd.Flags().BoolVar(&o.StashLocal, "stash-local", false, "preserve qualified locally edited native .csf bytes under .atl/stash before replacing them")
	cmd.Flags().StringVar(&o.Into, "into", mirrorRootDefault("mirror"), "mirror root dir (default: $ATL_MIRROR_ROOT or \"mirror\")")
	cmd.Flags().StringVar(&o.JiraView, "jira-view", "", "named Jira list view for JQL macros (default: default; macro columns win)")
	cmd.Flags().BoolVar(&o.Incremental, "incremental", false, "pull a complete changed-page delta using a selector-bound watermark")
	cmd.Flags().BoolVar(&o.Complete, "complete", false, "exhaust and resume one exact two-pass selector snapshot (no ordinary 1000/2000 cap)")
	cmd.Flags().BoolVar(&o.RestartComplete, "restart-complete", false, "replace an unfinished complete-pull snapshot after fresh selection and local preflight")
	cmd.Flags().StringVar(&o.Since, "since", "", "first-run lower boundary as an exact RFC3339 minute with explicit offset")
	cmd.Flags().StringVar(&o.TimeZone, "time-zone", "", "removed: put the explicit offset in --since")
	_ = cmd.Flags().MarkHidden("time-zone")
	cmd.Flags().IntVar(&o.MaxPages, "max-pages", 0, "selection cap (incremental default 10000; complete 0 means no configured cap)")
	cmd.Flags().IntVar(&o.PagePrefetch, "page-prefetch", 1, "ordered native-body prefetch window for multi-page pulls (1-8; mirror writes remain serial)")
	cmd.Flags().IntVar(&o.RequestsPerSecond, "requests-per-second", 0, "shared Confluence/Jira request-start rate for scheduled pulls (0 disables proactive pacing)")
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
	var table, expectedVersion int
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
			if expectedVersion < 0 {
				return usageErr("--expected-version must be >= 1 when set")
			}
			if rawCSV && format != "csv" {
				return usageErr("--raw-csv requires --format csv")
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			res, err := svc.ExtractTablesWithOptions(cmd.Context(), id, table, app.ConfluenceTableReadOpts{
				ExpectedPageVersion: expectedVersion,
			})
			if err != nil {
				return err
			}
			acknowledgement := func() map[string]any {
				return map[string]any{
					"path": out, "format": format, "table_count": res.TableCount,
					"returned_table_count": res.ReturnedTableCount, "selection_reconciled": res.SelectionReconciled,
					"version": res.Version, "page_version_gated": res.PageVersionGated,
				}
			}
			textAcknowledgement := func() string {
				return fmt.Sprintf("%s\tformat=%s\ttables=%d", out, format, res.ReturnedTableCount)
			}
			switch format {
			case "json":
				if out != "" {
					data, err := json.MarshalIndent(res, "", "  ")
					if err != nil {
						return err
					}
					data = append(data, '\n')
					if err := app.WriteConfluenceTableArtifact(out, data); err != nil {
						return err
					}
					return emit(cmd, acknowledgement(), textAcknowledgement)
				}
				return emit(cmd, res, func() string {
					return fmt.Sprintf("%d table(s)", res.ReturnedTableCount)
				})
			case "csv":
				data, err := app.RenderConfluenceTableCSVWithOptions(res, rawCSV)
				if err != nil {
					return err
				}
				if out != "" {
					if err := app.WriteConfluenceTableArtifact(out, data); err != nil {
						return err
					}
					return emit(cmd, acknowledgement(), textAcknowledgement)
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
				return emit(cmd, acknowledgement(), textAcknowledgement)
			default:
				return nil
			}
		},
	}
	extract.Flags().StringVar(&id, "id", "", "page id or supported same-origin URL")
	extract.Flags().IntVar(&table, "table", 0, "1-based table index to extract (0 = all tables)")
	extract.Flags().IntVar(&expectedVersion, "expected-version", 0, "optional positive page version already observed for this table selection")
	extract.Flags().StringVar(&format, "format", "json", "json|csv|xlsx")
	extract.Flags().StringVar(&out, "out", "", "optional output file (required for xlsx)")
	extract.Flags().BoolVar(&rawCSV, "raw-csv", false, "write formula-leading CSV cells verbatim (unsafe in spreadsheets)")
	_ = extract.RegisterFlagCompletionFunc("format", fixedComp("json", "csv", "xlsx"))
	var summaryID string
	var summaryTable, summaryExpectedVersion int
	summary := &cobra.Command{
		Use:   "summary",
		Short: "Summarize page table structure without exposing cell content",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if summaryID == "" {
				return usageErr("--id is required")
			}
			if summaryTable < 0 {
				return usageErr("--table must be >= 1")
			}
			if summaryExpectedVersion < 0 {
				return usageErr("--expected-version must be >= 1 when set")
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			res, err := svc.SummarizeTablesWithOptions(cmd.Context(), summaryID, summaryTable, app.ConfluenceTableReadOpts{
				ExpectedPageVersion: summaryExpectedVersion,
			})
			if err != nil {
				return err
			}
			return emit(cmd, res, func() string {
				return fmt.Sprintf("%d table(s)", res.TableCount)
			})
		},
	}
	summary.Flags().StringVar(&summaryID, "id", "", "page id or supported same-origin URL")
	summary.Flags().IntVar(&summaryTable, "table", 0, "1-based table index to summarize (0 = all tables)")
	summary.Flags().IntVar(&summaryExpectedVersion, "expected-version", 0, "optional positive page version already observed for this table read")
	c.AddCommand(extract, summary)
	return c
}

func confStatusCmd() *cobra.Command {
	var remote bool
	var into string
	cmd := &cobra.Command{
		Use:   "status [DIR]",
		Short: "Show locally-edited and remote-drifted pages",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveInspectionMirrorRoot(args, into, cmd.Flags().Changed("into"), "mirror")
			if err != nil {
				return err
			}
			svc := &app.ConfluenceService{}
			if remote {
				var err error
				svc, err = confService(cmd)
				if err != nil {
					return err
				}
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
	cmd.Flags().StringVar(&into, "into", "", "mirror root (or pass [DIR])")
	return cmd
}

func confSnapshotCmd() *cobra.Command {
	var remote bool
	var into string
	cmd := &cobra.Command{
		Use:   "snapshot [DIR]",
		Short: "Summarize mirror, baseline, validation, render, and drift health without content",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveInspectionMirrorRoot(args, into, cmd.Flags().Changed("into"), "mirror")
			if err != nil {
				return err
			}
			var (
				result      *app.ConfluenceMirrorSnapshot
				snapshotErr error
			)
			if remote {
				preflight, preflightErr := app.PreflightConfluenceMirrorRemoteSnapshot(dir)
				if preflight == nil || preflightErr != nil || !preflight.Complete || !preflight.Reconciled {
					result, snapshotErr = preflight, preflightErr
				} else {
					svc, err := confService(cmd)
					if err != nil {
						return err
					}
					result, snapshotErr = svc.SnapshotMirror(cmd.Context(), dir, true)
				}
			} else {
				result, snapshotErr = app.SnapshotConfluenceMirror(dir)
			}
			if result != nil {
				emitErr := emitSnapshot(cmd, result, func() string {
					return fmt.Sprintf(
						"complete=%t reconciled=%t total=%d present=%d edited=%d baseline_mismatch=%d invalid=%d render_unsupported=%d remote_drifted=%d remote_unavailable=%d",
						result.Complete, result.Reconciled, result.Native.Total, result.Local.Present,
						result.Local.LocallyEdited, result.Native.BaselineMismatch, result.Validation.Invalid,
						result.Render.Unsupported, result.Remote.Drifted, result.Remote.Unavailable,
					)
				})
				return snapshotResultErr(snapshotErr, emitErr)
			}
			return snapshotErr
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "also check remote drift (one single-attempt metadata probe per eligible tracked page)")
	cmd.Flags().StringVar(&into, "into", "", "mirror root (or pass [DIR])")
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

func confReconcileCmd() *cobra.Command {
	group := &cobra.Command{Use: "reconcile", Short: "Compare base/local/remote native bodies and optionally stage exact review artifacts"}
	newLeaf := func(stage bool) *cobra.Command {
		var into string
		mode := "preview"
		if stage {
			mode = "stage"
		}
		cmd := &cobra.Command{
			Use:   mode + " <page.csf|page.md>",
			Short: map[bool]string{false: "Read one page and classify base/local/remote divergence", true: "Stage exact base/remote artifacts without changing the working page"}[stage],
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				svc, err := confService(cmd)
				if err != nil {
					return err
				}
				var result *app.ConfluenceReconcileResult
				if stage {
					result, err = svc.StageConfluenceReconcile(cmd.Context(), args[0], into)
				} else {
					result, err = svc.PreviewConfluenceReconcile(cmd.Context(), args[0], into)
				}
				if err != nil {
					return err
				}
				return emit(cmd, result, func() string { return app.ConfluenceReconcileMarkdown(result) })
			},
		}
		cmd.Flags().StringVar(&into, "into", "", "mirror root (defaults to nearest .atl)")
		return cmd
	}
	group.AddCommand(newLeaf(false), newLeaf(true))
	return group
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
			svc, err := confService(cmd)
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
			svc, err := confService(cmd)
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
	var cloudCompat bool
	cmd := &cobra.Command{
		Use:   "validate <file.csf>",
		Short: "Validate CSF well-formedness + sanity → machine-readable problems",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readBody(args[0])
			if err != nil {
				return err
			}
			problems := csf.ValidateWithOptions(body, csf.Options{CloudCompat: cloudCompat})
			err = nil
			if csf.HasErrors(problems) {
				err = fmt.Errorf("%w: %s: not well-formed", domain.ErrCheckFailed, args[0])
			}
			out := map[string]any{"file": args[0], "ok": !csf.HasErrors(problems), "problems": problems}
			if cloudCompat {
				// Only present with the flag, so default output is unchanged.
				// The pack is versioned because Atlassian's documentation moves.
				out["cloud_compat"] = map[string]any{
					"rule_pack":   csf.CloudCompatRulePack,
					"source_date": csf.CloudCompatSourceDate,
				}
			}
			_ = emit(cmd, out, nil)
			return err
		},
	}
	cmd.Flags().BoolVar(&cloudCompat, "cloud-compat", false,
		"also report advisory Confluence Cloud compatibility findings (cloud-compat/* warnings; never blocks)")
	return cmd
}

func confPushCmd() *cobra.Command {
	var o app.PushOpts
	cmd := &cobra.Command{
		Use:   "push <file.csf|DIR>",
		Short: "Validate + push under the version gate; --dry-run prints consequences",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if res, perr := app.PreflightConfluencePushCSF(args[0], o); perr != nil {
				_ = emit(cmd, res, func() string { return pushText(res) })
				return perr
			}
			svc, err := confService(cmd)
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
	var id, location, state, depth string
	var expectedVersion int
	var legacyFlat bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List qualified footer and inline comment threads",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id == "" {
				return usageErr("--id is required")
			}
			if legacyFlat {
				for _, flag := range []string{"location", "state", "depth", "expected-version"} {
					if cmd.Flags().Changed(flag) {
						return usageErr("--legacy-flat cannot be combined with v2 filters or --expected-version")
					}
				}
			} else if err := app.ValidateConfluenceCommentInventoryOpts(app.ConfluenceCommentInventoryOpts{
				Location: location, State: state, Depth: depth, ExpectedPageVersion: expectedVersion,
			}); err != nil {
				return err
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			if legacyFlat {
				cs, truncated, err := svc.Comments(cmd.Context(), id)
				if err != nil {
					return err
				}
				if truncated {
					fmt.Fprint(cmd.ErrOrStderr(),
						"warning: comment listing hit the fetch cap — some comments were not returned\n")
				}
				return emit(cmd, map[string]any{"comments": cs}, func() string { return commentsText(cs) })
			}
			result, err := svc.CommentInventory(cmd.Context(), id, app.ConfluenceCommentInventoryOpts{
				Location: location, State: state, Depth: depth, ExpectedPageVersion: expectedVersion,
			})
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return confluenceCommentInventoryText(result) })
		},
	}
	list.Flags().StringVar(&id, "id", "", "page id or supported same-origin URL")
	list.Flags().StringVar(&location, "location", "all", "comment location: all|footer|inline|resolved")
	list.Flags().StringVar(&state, "state", "all", "resolution state: all|open|resolved|unknown")
	list.Flags().StringVar(&depth, "depth", "all", "thread depth: root|all")
	list.Flags().IntVar(&expectedVersion, "expected-version", 0, "require this exact page version (0 disables the gate)")
	list.Flags().BoolVar(&legacyFlat, "legacy-flat", false, "emit the temporary legacy flat comment shape")

	var threadID, commentID string
	var threadExpectedVersion int
	thread := &cobra.Command{
		Use:   "thread",
		Short: "Read one exact qualified comment thread",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if threadID == "" {
				return usageErr("--id is required")
			}
			if commentID == "" {
				return usageErr("--comment-id is required")
			}
			if err := app.ValidateConfluenceCommentID(commentID); err != nil {
				return err
			}
			if threadExpectedVersion < 0 {
				return usageErr("--expected-version must be positive (0 disables the gate)")
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.CommentThread(cmd.Context(), threadID, commentID, threadExpectedVersion)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return confluenceCommentInventoryText(result) })
		},
	}
	thread.Flags().StringVar(&threadID, "id", "", "page id or supported same-origin URL")
	thread.Flags().StringVar(&commentID, "comment-id", "", "exact numeric comment id")
	thread.Flags().IntVar(&threadExpectedVersion, "expected-version", 0, "require this exact page version (0 disables the gate)")

	preview := confFooterCommentMutationCmd(false)
	add := confFooterCommentMutationCmd(true)
	mutation := confInlineCommentMutationCmd()

	c.AddCommand(list, thread, preview, add, mutation)
	return c
}

func confInlineCommentMutationCmd() *cobra.Command {
	command := &cobra.Command{Use: "mutation", Short: "Guarded inline-comment thread mutations"}
	command.AddCommand(confInlineCommentMutationLeaf(false), confInlineCommentMutationLeaf(true))
	return command
}

func confInlineCommentMutationLeaf(applyCapable bool) *cobra.Command {
	var id, operation, threadID, fromFile, selectionFile string
	var occurrence int
	guardedWrite := guardedWriteFlags{profile: guardedWriteProposal}
	use, short := "preview", "Preview an inline-comment thread mutation"
	if applyCapable {
		use, short = "apply", "Preview or apply an inline-comment thread mutation"
	}
	cmd := &cobra.Command{
		Use: use, Short: short,
		Long: "Bind inline-create, reply, resolve, or reopen to the exact page version, complete comment inventory, actor, and activated provider. " +
			"Apply requires the reviewed proposal hash, performs one provider write attempt, and reconciles without replay.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id == "" {
				return usageErr("--id is required")
			}
			var op domain.ConfluenceCommentMutationOperation
			switch operation {
			case "inline-create":
				op = domain.ConfluenceCommentMutationInlineCreate
			case "reply":
				op = domain.ConfluenceCommentMutationReply
			case "resolve":
				op = domain.ConfluenceCommentMutationResolve
			case "reopen":
				op = domain.ConfluenceCommentMutationReopen
			default:
				return usageErr("--operation must be inline-create, reply, resolve, or reopen")
			}
			if applyCapable {
				if err := guardedWrite.validate(); err != nil {
					return err
				}
			}
			if op != domain.ConfluenceCommentMutationInlineCreate && (cmd.Flags().Changed("selection-file") || cmd.Flags().Changed("occurrence")) {
				return usageErr("--selection-file and --occurrence are only valid for inline-create")
			}
			var body, selection []byte
			switch op {
			case domain.ConfluenceCommentMutationInlineCreate:
				if threadID != "" {
					return usageErr("--thread-id is not valid for inline-create")
				}
				if fromFile == "" || selectionFile == "" {
					return usageErr("--from-file and --selection-file are required for inline-create")
				}
				if occurrence < 0 {
					return usageErr("--occurrence must be zero or positive")
				}
				if fromFile == "-" && selectionFile == "-" {
					return usageErr("--from-file and --selection-file cannot both read stdin")
				}
				var err error
				body, err = readConfluenceFooterCommentBody(fromFile)
				if err != nil {
					return err
				}
				selection, err = readConfluenceInlineSelection(selectionFile)
				if err != nil {
					return err
				}
			case domain.ConfluenceCommentMutationReply:
				if threadID == "" {
					return usageErr("--thread-id is required for reply")
				}
				if fromFile == "" {
					return usageErr("--from-file is required for reply")
				}
				var err error
				body, err = readConfluenceFooterCommentBody(fromFile)
				if err != nil {
					return err
				}
			default:
				if threadID == "" {
					return usageErr("--thread-id is required for resolve or reopen")
				}
				if cmd.Flags().Changed("from-file") {
					return usageErr("--from-file is only valid for inline-create or reply")
				}
			}
			svc, err := confCommentMutationService(cmd)
			if err != nil {
				return err
			}
			result, mutationErr := svc.MutateCommentGuarded(cmd.Context(), id, app.ConfluenceCommentMutationOpts{
				Operation: op, ThreadID: threadID, Body: body, Selection: selection, Occurrence: occurrence,
				Apply: applyCapable && guardedWrite.apply, ExpectedProposalHash: guardedWrite.expectedProposalHash,
			})
			if result != nil {
				if emitErr := emit(cmd, result, nil); emitErr != nil {
					return emitErr
				}
			}
			return mutationErr
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page id or supported same-origin URL")
	cmd.Flags().StringVar(&operation, "operation", "", "inline-create, reply, resolve, or reopen")
	cmd.Flags().StringVar(&threadID, "thread-id", "", "exact numeric root thread id")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "bounded native-CSF comment body file or - for stdin")
	cmd.Flags().StringVar(&selectionFile, "selection-file", "", "exact UTF-8 inline selection file or - for stdin")
	cmd.Flags().IntVar(&occurrence, "occurrence", 0, "zero-based exact selection occurrence")
	if applyCapable {
		guardedWrite.register(cmd)
	}
	return cmd
}

func readConfluenceInlineSelection(path string) ([]byte, error) {
	if path == "" {
		return nil, usageErr("--selection-file is required for inline-create")
	}
	if path == "-" {
		if stdinIsTerminal() {
			return nil, usageErr("stdin is a terminal and no selection was piped; pass --selection-file FILE or pipe the selection")
		}
		return readBounded(os.Stdin, app.ConfluenceFooterCommentBodyMaxBytes)
	}
	selection, err := readFileBounded(path, app.ConfluenceFooterCommentBodyMaxBytes)
	if err == nil {
		return selection, nil
	}
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: selection file %q does not exist", domain.ErrNotFound, path)
	}
	if errors.Is(err, domain.ErrUsage) {
		return nil, err
	}
	return nil, fmt.Errorf("%w: read selection file %q: %v", domain.ErrCheckFailed, path, err)
}

func confFooterCommentMutationCmd(applyCapable bool) *cobra.Command {
	var id, fromFile string
	guardedWrite := guardedWriteFlags{profile: guardedWriteProposal}
	use, short := "preview", "Preview a bounded footer comment"
	if applyCapable {
		use, short = "add", "Preview or apply a bounded footer comment"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: "Preview by default against the exact page version, actor, capability, and complete footer-comment baseline. " +
			"Apply requires the reviewed proposal hash, sends at most one POST, and reconciles without replay.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id == "" {
				return usageErr("--id is required")
			}
			if applyCapable {
				if err := guardedWrite.validate(); err != nil {
					return err
				}
			}
			body, err := readConfluenceFooterCommentBody(fromFile)
			if err != nil {
				return err
			}
			body, err = app.ValidateConfluenceFooterCommentBody(body)
			if err != nil {
				return err
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, mutationErr := svc.AddFooterCommentGuarded(cmd.Context(), id, app.ConfluenceFooterCommentAddOpts{
				Body: body, Apply: applyCapable && guardedWrite.apply,
				ExpectedProposalHash: guardedWrite.expectedProposalHash,
			})
			if result != nil {
				if emitErr := emit(cmd, result, func() string { return app.ConfluenceFooterCommentAddText(result) }); emitErr != nil {
					return emitErr
				}
			}
			return mutationErr
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "page id or supported same-origin URL")
	cmd.Flags().StringVar(&fromFile, "from-file", "-", "bounded native-CSF footer comment body file or - for stdin")
	if applyCapable {
		guardedWrite.register(cmd)
	}
	return cmd
}

func readConfluenceFooterCommentBody(path string) ([]byte, error) {
	switch path {
	case "", "-":
		if stdinIsTerminal() {
			return nil, usageErr("stdin is a terminal and no body was piped; pass --from-file FILE or pipe the body")
		}
		return readBounded(os.Stdin, app.ConfluenceFooterCommentBodyMaxBytes)
	default:
		body, err := readFileBounded(path, app.ConfluenceFooterCommentBodyMaxBytes)
		if err == nil {
			return body, nil
		}
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: body file %q does not exist", domain.ErrNotFound, path)
		}
		if errors.Is(err, domain.ErrUsage) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: read body file %q: %v", domain.ErrCheckFailed, path, err)
	}
}

func confAttachmentCmd() *cobra.Command {
	c := &cobra.Command{Use: "attachment", Short: "Attachment list/get/upload/delete"}

	var listID string
	var listExpectedVersion int
	list := &cobra.Command{
		Use:   "list",
		Short: "List attachments on a page with explicit completeness",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listID == "" {
				return usageErr("--id is required")
			}
			if listExpectedVersion < 0 {
				return usageErr("--expected-version must be a positive page version (0 disables the gate)")
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			inventory, err := svc.AttachmentInventory(cmd.Context(), listID, app.ConfluenceAttachmentInventoryOpts{
				ExpectedPageVersion: listExpectedVersion,
			})
			if err != nil {
				return err
			}
			atts := inventory.Attachments
			return emitID(cmd, inventory, func() string {
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
	list.Flags().IntVar(&listExpectedVersion, "expected-version", 0, "refuse the listing unless the page is at this version (0 = no gate)")

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
			svc, err := confService(cmd)
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
			svc, err := confService(cmd)
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

	var delAttPageID, delAttID, delAttConfirm string
	var delAttExpectedVersion int
	delAttGuard := guardedWriteFlags{profile: guardedWriteProposal}
	del := &cobra.Command{
		Use:   "delete",
		Short: "Preview or apply one reviewed permanent attachment deletion",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, deleteErr := svc.DeleteAttachmentGuarded(cmd.Context(), delAttPageID, delAttID, app.ConfluenceAttachmentDeleteOpts{
				Apply: delAttGuard.apply, Confirm: delAttConfirm, ExpectedPageVersion: delAttExpectedVersion,
				ExpectedProposalHash: delAttGuard.expectedProposalHash,
			})
			if result == nil {
				return deleteErr
			}
			emitErr := emit(cmd, result, func() string { return app.ConfluenceAttachmentDeleteText(result) })
			return guardedMutationResultErr(deleteErr, emitErr, result.WriteAttempted, "attachment deletion")
		},
	}
	del.Flags().StringVar(&delAttPageID, "page-id", "", "containing page id")
	del.Flags().StringVar(&delAttID, "id", "", "attachment id")
	del.Flags().IntVar(&delAttExpectedVersion, "expected-version", 0, "reviewed current page version (required with --apply)")
	del.Flags().StringVar(&delAttConfirm, "confirm", "", "must be exactly DELETE with --apply")
	delAttGuard.register(del)

	c.AddCommand(list, get, upload, del)
	return c
}

func confMeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "me",
		Short: "Print the authenticated Confluence user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := confService(cmd)
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
