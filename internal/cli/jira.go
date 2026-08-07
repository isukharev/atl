package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mdwiki"
	"github.com/isukharev/atl/internal/version"
)

func jiraService() (*app.JiraService, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	authorizer, err := policyAuthorizerFor("jira", cfg.JiraURL)
	if err != nil {
		return nil, err
	}
	return app.NewJiraWithWriteAuthorizer(cfg, version.Version, authorizer)
}

// wikiBody resolves a Jira body flag pair: raw wiki markup from --from-file,
// or markdown converted whole-document via mdwiki when --from-md is set. The
// two are mutually exclusive; dispatch is on the flag being set, not its
// value, so `--from-md ""` cannot silently fall back to the wiki path. A
// conversion failure maps to ErrCheckFailed (exit 8) — fail-closed, nothing
// is sent to the backend.
func wikiBody(cmd *cobra.Command, fromFile, fromMD string) ([]byte, error) {
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
	wiki, err := mdwiki.ConvertDocument(string(md))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot convert markdown body: %v (constructs outside the md subset need a wiki body via --from-file)", domain.ErrCheckFailed, err)
	}
	return []byte(wiki), nil
}

func newJiraCmd() *cobra.Command {
	c := &cobra.Command{Use: "jira", Short: "Jira: read/search/pull issues, edit via commands (native wiki)"}
	cmds := []*cobra.Command{jiraIssueCmd(), jiraEpicCmd(), jiraPullCmd(), jiraRenderCmd(), jiraApplyCmd(), jiraStatusCmd(), jiraSnapshotCmd(), jiraReconcileCmd(), jiraPushCmd(), jiraExportCmd(), jiraPlanningCmd(), jiraQualityReportCmd(), jiraMeCmd(), jiraUserCmd(), jiraBoardCmd(), jiraSprintCmd(), jiraStructureCmd()}
	cmds = append(cmds, jiraMetaCmds()...)
	c.AddCommand(cmds...)
	return c
}

func splitFields(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func parseKV(pairs []string) (map[string]string, error) {
	m := map[string]string{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, usageErr("--field must be key=value, got %q", p)
		}
		m[strings.TrimSpace(k)] = v
	}
	return m, nil
}

// userID returns the most useful stable identifier for piping (-o id): the DC
// username, then user key, then the Cloud account id.
func userID(u *domain.User) string {
	switch {
	case u.Name != "":
		return u.Name
	case u.Key != "":
		return u.Key
	default:
		return u.AccountID
	}
}

func jiraMeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "me",
		Short: "Show the authenticated Jira user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			u, err := svc.Me(cmd.Context())
			if err != nil {
				return err
			}
			return emitID(cmd, u,
				func() string { return fmt.Sprintf("%s\t%s\t%s", u.Name, u.DisplayName, u.Email) },
				func() []string { return []string{userID(u)} })
		},
	}
}

func jiraUserCmd() *cobra.Command {
	c := &cobra.Command{Use: "user", Short: "Search/get Jira users"}

	var limit int
	search := &cobra.Command{
		Use:   "search <QUERY>",
		Short: "Search users by name/username",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validatePageLimit(limit, 1000); err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			us, err := svc.SearchUsers(cmd.Context(), args[0], limit)
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"users": us}, func() string {
				var b strings.Builder
				for _, u := range us {
					fmt.Fprintf(&b, "%s\t%s\t%s\n", u.Name, u.DisplayName, u.Email)
				}
				return strings.TrimRight(b.String(), "\n")
			}, func() []string {
				ids := make([]string, len(us))
				for i := range us {
					ids[i] = userID(&us[i])
				}
				return ids
			})
		},
	}
	search.Flags().IntVar(&limit, "limit", 50, "max results (1..1000)")

	get := &cobra.Command{
		Use:   "get <USERNAME>",
		Short: "Get a user by DC username",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			u, err := svc.GetUser(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emitID(cmd, u,
				func() string { return fmt.Sprintf("%s\t%s\t%s", u.Name, u.DisplayName, u.Email) },
				func() []string { return []string{userID(u)} })
		},
	}

	c.AddCommand(search, get)
	return c
}

func jiraPullCmd() *cobra.Command {
	var jql, into string
	var fields string
	var limit int
	var assets, dryRun, overwriteLocal, stashLocal bool
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Export issues matching --jql to one .wiki + .md + .json set per issue",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if jql == "" {
				return usageErr("--jql is required")
			}
			if overwriteLocal && stashLocal {
				return usageErr("--overwrite-local and --stash-local are mutually exclusive")
			}
			if err := validateAggregateLimit(limit); err != nil {
				return err
			}
			override, err := rf.override()
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.Pull(cmd.Context(), app.JiraPullOpts{
				JQL: jql, Into: into, Limit: limit, Fields: splitFields(fields), Assets: assets,
				DryRun: dryRun, OverwriteLocal: overwriteLocal, StashLocal: stashLocal, Render: override,
			})
			if err != nil && (res == nil || res.LocalSafety == nil || !errors.Is(err, domain.ErrCheckFailed)) {
				return err
			}
			// Warn on stderr (never stdout — that would corrupt the JSON result)
			// when image assets were selected but could not be mirrored, mirroring
			// the conf pull truncation warning: skipped assets are never silent.
			if res.AssetsSkipped > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: %d image asset(s) skipped (download or write failed) — the affected issues were still pulled without those images\n",
					res.AssetsSkipped)
			}
			if res.EpicChildrenTruncated {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: epic children truncated at %d issues — one or more mirrored epic-child sidecars are incomplete; narrow the pull selection\n",
					res.EpicChildrenTruncatedAt)
			}
			warnRender(cmd.ErrOrStderr(), res.Warnings)
			emitErr := emit(cmd, res, func() string {
				var b strings.Builder
				appendPullLocalSafetyText(&b, res.LocalSafety)
				for _, p := range res.Issues {
					fmt.Fprintf(&b, "%s\t%s", p.Key, p.Path)
					if p.Status != "" {
						fmt.Fprintf(&b, "\t%s", p.Status)
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
	cmd.Flags().StringVar(&jql, "jql", "", "JQL selecting issues")
	cmd.Flags().StringVar(&into, "into", mirrorRootDefault("mirror-jira"), "output root dir (default: $ATL_MIRROR_ROOT or \"mirror-jira\")")
	cmd.Flags().IntVar(&limit, "limit", 100, "max issues (0 = all; must be non-negative)")
	cmd.Flags().StringVar(&fields, "fields", "", "extra comma-separated field list to include in JSON snapshots")
	cmd.Flags().BoolVar(&assets, "assets", false, "also mirror each issue's image attachments into a per-issue <KEY>.assets/ dir and link them from the .md")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "qualify the pull without writing mirror files or state")
	cmd.Flags().BoolVar(&overwriteLocal, "overwrite-local", false, "explicitly replace qualified locally edited native .wiki bytes")
	cmd.Flags().BoolVar(&stashLocal, "stash-local", false, "preserve qualified locally edited native .wiki bytes under .atl/stash before replacing them")
	rf.register(cmd)
	return cmd
}

func jiraRenderCmd() *cobra.Command {
	var into string
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "render [DIR|FILE.md|FILE.wiki]",
		Short: "Regenerate .md views from local snapshots (offline; no network/PAT)",
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
			svc := app.NewJiraRenderer(cfg)
			res, err := svc.Render(target, override)
			if err != nil {
				return err
			}
			warnRender(cmd.ErrOrStderr(), res.Warnings)
			return emit(cmd, res, func() string {
				var b strings.Builder
				for _, r := range res.Rendered {
					fmt.Fprintf(&b, "%s\t%s\n", r.Key, r.Path)
				}
				return strings.TrimRight(b.String(), "\n")
			})
		},
	}
	cmd.Flags().StringVar(&into, "into", mirrorRootDefault("mirror-jira"), "mirror root dir when no target is given")
	rf.register(cmd)
	return cmd
}

func jiraStatusCmd() *cobra.Command {
	var remote bool
	var into string
	cmd := &cobra.Command{
		Use:   "status [DIR]",
		Short: "Show locally-edited (and optionally remote-drifted) mirrored issues",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveInspectionMirrorRoot(args, into, cmd.Flags().Changed("into"), "mirror-jira")
			if err != nil {
				return err
			}
			svc := &app.JiraService{}
			if remote {
				var err error
				svc, err = jiraService()
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
					if e.LocallyEdited {
						flag = "M  "
					}
					if e.RemoteDrifted {
						flag = "M↯ "
					}
					// A file whose remote check failed must not read as clean/in-sync;
					// mark the uncertainty so a "safe to push?" glance sees it.
					if e.RemoteError != "" {
						if e.LocallyEdited {
							flag = "M? "
						} else {
							flag = " ? "
						}
					}
					if e.LocalError != "" {
						flag = "M! "
					}
					fmt.Fprintf(&b, "%s%s\t%s", flag, e.Key, e.Path)
					if e.LocalError != "" {
						fmt.Fprintf(&b, "\t(local: %s)", e.LocalError)
					}
					if e.RemoteError != "" {
						fmt.Fprintf(&b, "\t(remote: %s)", e.RemoteError)
					}
					b.WriteByte('\n')
				}
				return strings.TrimRight(b.String(), "\n")
			})
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "also check remote drift (exact for one issue; otherwise qualified batches)")
	cmd.Flags().StringVar(&into, "into", "", "mirror root (or pass [DIR])")
	return cmd
}

func jiraSnapshotCmd() *cobra.Command {
	var remote bool
	var into string
	cmd := &cobra.Command{
		Use:   "snapshot [DIR]",
		Short: "Summarize Jira mirror, baseline, raw snapshot, pending, render, and drift health without content",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveInspectionMirrorRoot(args, into, cmd.Flags().Changed("into"), "mirror-jira")
			if err != nil {
				return err
			}
			var (
				result      *app.JiraMirrorSnapshot
				snapshotErr error
			)
			if remote {
				preflight, preflightErr := app.PreflightJiraMirrorRemoteSnapshot(dir)
				if preflight == nil || preflightErr != nil || !preflight.Complete || !preflight.Reconciled {
					result, snapshotErr = preflight, preflightErr
				} else {
					svc, err := jiraService()
					if err != nil {
						return err
					}
					result, snapshotErr = svc.SnapshotMirror(cmd.Context(), dir, true)
				}
			} else {
				result, snapshotErr = app.SnapshotJiraMirror(dir)
			}
			if result != nil {
				emitErr := emitSnapshot(cmd, result, func() string {
					return fmt.Sprintf(
						"complete=%t reconciled=%t total=%d present=%d edited=%d baseline_mismatch=%d snapshot_invalid=%d pending_unbound=%d render_unsupported=%d remote_drifted=%d remote_unavailable=%d",
						result.Complete, result.Reconciled, result.Native.Total, result.Local.Present, result.Local.LocallyEdited,
						result.Native.BaselineMismatch, result.Snapshot.Invalid+result.Snapshot.KeyMismatched,
						result.Pending.Unbound, result.Render.Unsupported, result.Remote.Drifted, result.Remote.Unavailable,
					)
				})
				return snapshotResultErr(snapshotErr, emitErr)
			}
			return snapshotErr
		},
	}
	cmd.Flags().BoolVar(&remote, "remote", false, "also check remote drift (exact for one issue; otherwise qualified batches)")
	cmd.Flags().StringVar(&into, "into", "", "mirror root (or pass [DIR])")
	return cmd
}

func jiraReconcileCmd() *cobra.Command {
	group := &cobra.Command{Use: "reconcile", Short: "Compare base/local/remote native values and optionally stage exact review artifacts"}
	newLeaf := func(stage bool) *cobra.Command {
		var into string
		mode := "preview"
		if stage {
			mode = "stage"
		}
		cmd := &cobra.Command{
			Use:   mode + " <issue.wiki|issue.md>",
			Short: map[bool]string{false: "Read one issue and classify base/local/remote divergence", true: "Stage exact base/remote description artifacts without changing the working issue"}[stage],
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				svc, err := jiraService()
				if err != nil {
					return err
				}
				var result *app.JiraReconcileResult
				if stage {
					result, err = svc.StageJiraReconcile(cmd.Context(), args[0], into)
				} else {
					result, err = svc.PreviewJiraReconcile(cmd.Context(), args[0], into)
				}
				if err != nil {
					return err
				}
				return emit(cmd, result, func() string { return app.JiraReconcileMarkdown(result) })
			},
		}
		cmd.Flags().StringVar(&into, "into", "", "mirror root (defaults to nearest .atl)")
		return cmd
	}
	group.AddCommand(newLeaf(false), newLeaf(true))
	return group
}

func jiraPushCmd() *cobra.Command {
	var o app.JiraPushOpts
	cmd := &cobra.Command{
		Use:   "push <file.wiki|DIR>",
		Short: "Preview (default) or --apply guarded local Jira edits",
		Long: "Push an edited <KEY>.wiki description and any pending opt-in rich-text fields back to its Jira issue.\n\n" +
			"Dry-run by default: without --apply it only previews the unified diff and drift, " +
			"writing nothing. Fields are included only when their render descriptor explicitly enables editing. Jira has no " +
			"server-side version gate, so staleness is caught by an app-layer compare against the " +
			"pulled base. Description drift may be overridden with --force; pending-field drift always " +
			"fails closed and must be reconciled before writing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, perr := svc.Push(cmd.Context(), args[0], o)
			// res is nil when target resolution failed before any push attempt.
			// The push error wins over an output error (it is the actionable one),
			// but a failed emit must not read as success when the push itself was
			// fine — a broken stdout would silently hide the result.
			if res != nil {
				if emitErr := emit(cmd, res, func() string { return jiraPushText(res) }); perr == nil {
					perr = emitErr
				}
			}
			return perr
		},
	}
	cmd.Flags().BoolVar(&o.Apply, "apply", false, "actually write the change (default: dry-run preview only)")
	cmd.Flags().BoolVar(&o.Force, "force", false, "override description drift (pending-field drift still refuses)")
	cmd.Flags().StringVar(&o.Into, "into", "", "mirror root (defaults to nearest .atl)")
	return cmd
}

func jiraPushText(res *app.JiraPushResult) string {
	var b strings.Builder
	for _, it := range res.Items {
		state := "ok"
		switch {
		case it.Failed != "":
			state = "FAILED(" + it.Failed + ")"
		case it.Skipped != "":
			state = it.Skipped
		case it.Pushed:
			state = "pushed"
		case it.DryRun:
			state = "dry-run"
			if it.Drifted {
				state = "dry-run/DRIFTED"
			}
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", state, it.Key, it.Path)
		if it.DriftOverridden {
			b.WriteString("   ⚠ remote drift overridden by --force\n")
		}
		if it.Warning != "" {
			fmt.Fprintf(&b, "   ⚠ %s\n", it.Warning)
		}
		if it.Diff != "" {
			for _, line := range strings.Split(strings.TrimRight(it.Diff, "\n"), "\n") {
				fmt.Fprintf(&b, "   %s\n", line)
			}
		}
		for _, field := range it.Fields {
			fmt.Fprintf(&b, "   field %s:\n", field.ID)
			for _, line := range strings.Split(strings.TrimRight(field.Diff, "\n"), "\n") {
				if line != "" {
					fmt.Fprintf(&b, "      %s\n", line)
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func jiraExportCmd() *cobra.Command {
	var jql, out, format, fields, ids, keys string
	var limit int
	var batchSize int
	var rawCSV bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export issues to a compact file+manifest or transient stdout artifact",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return usageErr("--out is required")
			}
			if out == "-" && outputFormat == "text" {
				return usageErr("-o text is not an artifact format for --out -; use --format and omit -o text")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.Export(cmd.Context(), app.JiraExportOpts{
				JQL:       jql,
				IDs:       splitFields(ids),
				Keys:      splitFields(keys),
				BatchSize: batchSize,
				Out:       out,
				Format:    format,
				Limit:     limit,
				Fields:    splitFields(fields),
				Version:   version.Version,
				RawCSV:    rawCSV,
				Writer:    cmd.OutOrStdout(),
			})
			if err != nil {
				return err
			}
			if out == "-" {
				return nil
			}
			return emit(cmd, res, func() string {
				return fmt.Sprintf("%s\t%s\t%d issues", res.Path, res.Format, res.Count)
			})
		},
	}
	cmd.Flags().StringVar(&jql, "jql", "", "JQL selecting issues")
	cmd.Flags().StringVar(&ids, "ids", "", "comma-separated numeric issue ids; emits found rows in first-occurrence order")
	cmd.Flags().StringVar(&keys, "keys", "", "comma-separated issue keys; emits found rows in first-occurrence order")
	cmd.Flags().IntVar(&batchSize, "batch-size", 100, "max ids/keys per generated JQL batch")
	cmd.Flags().StringVar(&out, "out", "", "artifact path, or - for artifact-only stdout (no manifest)")
	cmd.Flags().StringVar(&format, "format", "jsonl", "export format: jsonl, json, or csv")
	cmd.Flags().IntVar(&limit, "limit", 100, "max issues (0 = all; must be non-negative)")
	cmd.Flags().StringVar(&fields, "fields", "", "extra comma-separated exact field ids or display names")
	cmd.Flags().BoolVar(&rawCSV, "raw-csv", false, "write formula-leading CSV cells verbatim (unsafe in spreadsheets)")
	_ = cmd.RegisterFlagCompletionFunc("format", fixedComp("jsonl", "json", "csv"))
	cmd.AddCommand(jiraExportDiffCmd())
	return cmd
}

func jiraExportDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <OLD-EXPORT> <NEW-EXPORT>",
		Short: "Compare two compact Jira export artifacts",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			diff, err := app.DiffJiraExports(args[0], args[1])
			if err != nil {
				return err
			}
			return emit(cmd, diff, func() string {
				return fmt.Sprintf("old=%d new=%d added=%d removed=%d changed=%d",
					diff.OldCount, diff.NewCount, len(diff.Added), len(diff.Removed), len(diff.Changed))
			})
		},
	}
}

func jiraPlanningCmd() *cobra.Command {
	c := &cobra.Command{Use: "planning", Short: "Read-only Jira planning quality reports"}
	c.AddCommand(jiraPlanningReportCommand("report"))
	return c
}

func jiraQualityReportCmd() *cobra.Command {
	cmd := jiraPlanningReportCommand("quality-report")
	cmd.Short = "Compatibility alias for `jira planning report`"
	return cmd
}

func jiraPlanningReportCommand(use string) *cobra.Command {
	var jql, require, estimateField, epicField, csvPath string
	var limit int
	var rawCSV bool
	cmd := &cobra.Command{
		Use:   use,
		Short: "Build a deterministic planning quality report over JQL",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if jql == "" {
				return usageErr("--jql is required")
			}
			if err := validateAggregateLimit(limit); err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.PlanningReport(cmd.Context(), app.PlanningReportOpts{
				JQL:           jql,
				Required:      splitFields(require),
				EstimateField: estimateField,
				EpicField:     epicField,
				Limit:         limit,
				CSVPath:       csvPath,
				RawCSV:        rawCSV,
			})
			if err != nil {
				return err
			}
			return emit(cmd, res, func() string {
				return fmt.Sprintf("issues=%d good=%d warn=%d poor=%d",
					res.Count, res.Summary.Good, res.Summary.Warn, res.Summary.Poor)
			})
		},
	}
	cmd.Flags().StringVar(&jql, "jql", "", "JQL selecting issues")
	cmd.Flags().StringVar(&require, "require", "", "comma-separated fields that must be populated")
	cmd.Flags().StringVar(&estimateField, "estimate-field", "", "field id/name used as the estimate check")
	cmd.Flags().StringVar(&epicField, "epic-field", "", "field id/name containing parent epic key")
	cmd.Flags().IntVar(&limit, "limit", 100, "max issues (0 = all; must be non-negative)")
	cmd.Flags().StringVar(&csvPath, "csv", "", "optional CSV report path")
	cmd.Flags().BoolVar(&rawCSV, "raw-csv", false, "write formula-leading CSV cells verbatim (unsafe in spreadsheets; requires --csv)")
	return cmd
}

func jiraMetaCmds() []*cobra.Command {
	var nameLike, fieldID, idLike, schema, custom string
	var summaryOnly bool
	fields := &cobra.Command{
		Use:   "fields",
		Short: "List qualified Jira fields without values",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			result, err := svc.FieldCatalog(cmd.Context(), app.JiraFieldCatalogOpts{
				ID: fieldID, NameLike: nameLike, IDLike: idLike, Schema: schema, Custom: custom,
				SummaryOnly: summaryOnly,
			})
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return jiraFieldsText(result) })
		},
	}
	fields.Flags().StringVar(&nameLike, "name-like", "", "case-insensitive substring filter for field name")
	fields.Flags().StringVar(&fieldID, "id", "", "exact field id filter")
	fields.Flags().StringVar(&idLike, "id-like", "", "case-insensitive substring filter for field id")
	fields.Flags().StringVar(&schema, "schema", "", "exact schema type filter")
	fields.Flags().StringVar(&custom, "custom", "", "filter custom fields: true or false")
	fields.Flags().BoolVar(&summaryOnly, "summary-only", false, "omit field definitions and return reconciled counts")

	var project, issueType, field string
	opts := &cobra.Command{
		Use:   "field-options",
		Short: "List allowed values of a field for a project/type",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" || field == "" {
				return usageErr("--project and --field are required")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			vals, err := svc.FieldOptions(cmd.Context(), project, issueType, field)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"options": vals}, func() string { return stringLines(vals) })
		},
	}
	opts.Flags().StringVar(&project, "project", "", "project key")
	opts.Flags().StringVar(&issueType, "type", "", "issue type name")
	opts.Flags().StringVar(&field, "field", "", "field id or name")

	var transKey string
	transitions := &cobra.Command{
		Use:   "transitions",
		Short: "List available transitions for an issue",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if transKey == "" {
				return usageErr("--key is required")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			trs, err := svc.Transitions(cmd.Context(), transKey)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"transitions": trs}, func() string { return jiraTransitionsText(trs) })
		},
	}
	transitions.Flags().StringVar(&transKey, "key", "", "issue key")

	linkTypes := &cobra.Command{
		Use:   "link-types",
		Short: "List issue link types",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			lts, err := svc.LinkTypes(cmd.Context())
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"link_types": lts}, func() string { return stringLines(lts) })
		},
	}

	return []*cobra.Command{fields, opts, transitions, linkTypes}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
