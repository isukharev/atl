package cli

import (
	"fmt"
	"path/filepath"
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
	return app.NewJira(cfg, version.Version)
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
	cmds := []*cobra.Command{jiraIssueCmd(), jiraPullCmd(), jiraRenderCmd(), jiraApplyCmd(), jiraStatusCmd(), jiraPushCmd(), jiraExportCmd(), jiraPlanningCmd(), jiraQualityReportCmd(), jiraMeCmd(), jiraUserCmd(), jiraBoardCmd(), jiraSprintCmd(), jiraStructureCmd()}
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

func jiraIssueCmd() *cobra.Command {
	c := &cobra.Command{Use: "issue", Short: "Issue operations"}

	var fields string
	get := &cobra.Command{
		Use:   "get <KEY>",
		Short: "Get an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			is, err := svc.Issue(cmd.Context(), args[0], splitFields(fields))
			if err != nil {
				return err
			}
			return emit(cmd, is, func() string {
				return fmt.Sprintf("%s [%s] %s\n\n%s", is.Key, is.Status, is.Summary, is.Body)
			})
		},
	}
	get.Flags().StringVar(&fields, "fields", "", "comma-separated field list")

	var jql, searchFields, cursor string
	var limit int
	search := &cobra.Command{
		Use:   "search",
		Short: "Search issues by JQL",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if jql == "" {
				return usageErr("--jql is required")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			issues, next, err := svc.Search(cmd.Context(), jql, splitFields(searchFields), limit, cursor)
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"issues": issues, "next_cursor": next}, func() string {
				var b strings.Builder
				for _, is := range issues {
					fmt.Fprintf(&b, "%s\t[%s]\t%s\n", is.Key, is.Status, is.Summary)
				}
				return strings.TrimRight(b.String(), "\n")
			}, func() []string {
				keys := make([]string, len(issues))
				for i, is := range issues {
					keys[i] = is.Key
				}
				return keys
			})
		},
	}
	search.Flags().StringVar(&jql, "jql", "", "JQL query")
	search.Flags().StringVar(&searchFields, "fields", "", "comma-separated field list")
	search.Flags().IntVar(&limit, "limit", 50, "max results")
	search.Flags().StringVar(&cursor, "cursor", "", "pagination cursor (startAt)")

	var project, issueType, summary, fromFile, fromMD string
	var fieldKV []string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create an issue (description = wiki via --from-file, or markdown via --from-md)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" || issueType == "" || summary == "" {
				return usageErr("--project, --type and --summary are required")
			}
			body, err := wikiBody(cmd, fromFile, fromMD)
			if err != nil {
				return err
			}
			kv, err := parseKV(fieldKV)
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			is, err := svc.Create(cmd.Context(), project, issueType, summary, body, kv)
			if err != nil {
				return err
			}
			return emitID(cmd, is, func() string { return "created " + is.Key },
				func() []string { return []string{is.Key} })
		},
	}
	create.Flags().StringVar(&project, "project", "", "project key")
	create.Flags().StringVar(&issueType, "type", "", "issue type name")
	create.Flags().StringVar(&summary, "summary", "", "summary")
	create.Flags().StringVar(&fromFile, "from-file", "", "description (wiki) file or - for stdin")
	create.Flags().StringVar(&fromMD, "from-md", "", "markdown description file or - for stdin (converted to wiki; unsupported constructs are refused)")
	create.Flags().StringArrayVar(&fieldKV, "field", nil, "extra field key=value (repeatable); a JSON object/array value is sent as JSON, e.g. priority={\"name\":\"High\"}")

	var upSummary, upFile, upMD string
	var upFieldKV []string
	update := &cobra.Command{
		Use:   "update <KEY>",
		Short: "Update an issue (summary/description/fields)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := wikiBody(cmd, upFile, upMD)
			if err != nil {
				return err
			}
			kv, err := parseKV(upFieldKV)
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.Update(cmd.Context(), args[0], upSummary, body, kv); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"key": args[0], "status": "updated"}, nil)
		},
	}
	update.Flags().StringVar(&upSummary, "summary", "", "new summary")
	update.Flags().StringVar(&upFile, "from-file", "", "new description (wiki) file or - for stdin")
	update.Flags().StringVar(&upMD, "from-md", "", "new markdown description file or - for stdin (converted to wiki; unsupported constructs are refused)")
	update.Flags().StringArrayVar(&upFieldKV, "field", nil, "field key=value (repeatable); a JSON object/array value is sent as JSON, e.g. priority={\"name\":\"High\"}")

	var edOld, edNew, edOldFile, edNewFile string
	var edAll, edDryRun bool
	edit := &cobra.Command{
		Use:   "edit <KEY>",
		Short: "Replace text in the description in one command (fetch, splice, write back)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			old, err := textFromFlagPair(edOld, edOldFile, "--old")
			if err != nil {
				return err
			}
			repl, err := textFromFlagPair(edNew, edNewFile, "--new")
			if err != nil {
				return err
			}
			if old == "" {
				return usageErr("--old (or --old-file) is required and must be non-empty")
			}
			if !cmd.Flags().Changed("new") && edNewFile == "" {
				return usageErr("--new (or --new-file) is required (pass --new '' to delete the matched text)")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			before, res, err := svc.EditDescription(cmd.Context(), args[0], old, repl, edAll, edDryRun)
			if err != nil {
				return err
			}
			m := res.Matches[0]
			out := map[string]any{
				"key":           args[0],
				"pass":          string(res.Pass),
				"count":         len(res.Matches),
				"offsets":       res.Matches,
				"dry_run":       edDryRun,
				"region_before": quoteRegion(before, m.Start, m.End),
				"region_after":  quoteRegion(res.Text, m.Start, m.Start+len(repl)),
			}
			return emit(cmd, out, func() string {
				verb := "replaced"
				if edDryRun {
					verb = "would replace"
				}
				return fmt.Sprintf("%s\t%s %d occurrence(s) via %s pass", args[0], verb, len(res.Matches), res.Pass)
			})
		},
	}
	edit.Flags().StringVar(&edOld, "old", "", "text to find in the description (tolerant of NBSP/zero-width/entity differences)")
	edit.Flags().StringVar(&edNew, "new", "", "replacement text (native wiki, inserted verbatim)")
	edit.Flags().StringVar(&edOldFile, "old-file", "", "read the text to find from a file (- for stdin; one trailing newline is stripped)")
	edit.Flags().StringVar(&edNewFile, "new-file", "", "read the replacement from a file (one trailing newline is stripped)")
	edit.Flags().BoolVar(&edAll, "all", false, "replace every match instead of requiring a unique one")
	edit.Flags().BoolVar(&edDryRun, "dry-run", false, "report the match without updating the issue")

	var to, transComment string
	var transFieldKV []string
	transition := &cobra.Command{
		Use:   "transition <KEY>",
		Short: "Transition an issue to a status (optionally setting fields)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return usageErr("--to is required")
			}
			kv, err := parseKV(transFieldKV)
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.Transition(cmd.Context(), args[0], to, transComment, kv); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"key": args[0], "to": to, "status": "transitioned"}, nil)
		},
	}
	transition.Flags().StringVar(&to, "to", "", "target status/transition name")
	transition.Flags().StringVar(&transComment, "comment", "", "optional comment")
	transition.Flags().StringArrayVar(&transFieldKV, "field", nil, "field key=value to set on the transition (repeatable), e.g. resolution={\"name\":\"Fixed\"}")

	var checkRequire, checkWarn string
	check := &cobra.Command{
		Use:   "check <KEY>",
		Short: "Audit that required/important fields are populated (non-zero exit if a required field is empty)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// An explicit --warn (even "") overrides the default set, so a caller
			// can opt out of warnings entirely with --warn "".
			warn := app.DefaultCheckFields
			if cmd.Flags().Changed("warn") {
				warn = splitFields(checkWarn)
			}
			require := splitFields(checkRequire)
			// A check that audits nothing (no --require and --warn emptied) is a
			// silent no-op gate that always passes — reject it as a usage error.
			if len(require) == 0 && len(warn) == 0 {
				return usageErr("nothing to check: pass --require and/or --warn fields")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			r, err := svc.Check(cmd.Context(), args[0], require, warn)
			if err != nil {
				return err
			}
			if err := emit(cmd, r, func() string {
				var b strings.Builder
				fmt.Fprintf(&b, "%s\tok=%t\n", r.Key, r.OK)
				if len(r.MissingRequired) > 0 {
					fmt.Fprintf(&b, "  missing required: %s\n", strings.Join(r.MissingRequired, ", "))
				}
				if len(r.MissingWarn) > 0 {
					fmt.Fprintf(&b, "  missing (warn): %s\n", strings.Join(r.MissingWarn, ", "))
				}
				return strings.TrimRight(b.String(), "\n")
			}); err != nil {
				return err
			}
			// Report on stdout, but signal failure via a distinct exit code (8) so
			// the command works as a CI / pre-transition gate that scripts can tell
			// apart from a transport/auth error.
			if !r.OK {
				return fmt.Errorf("%w: issue %s missing required fields: %s", domain.ErrCheckFailed, r.Key, strings.Join(r.MissingRequired, ", "))
			}
			return nil
		},
	}
	check.Flags().StringVar(&checkRequire, "require", "", "comma-separated fields that must be set (non-zero exit if any empty)")
	check.Flags().StringVar(&checkWarn, "warn", "", "comma-separated fields to warn about (default: assignee,priority,components,fixVersions,description)")

	var delForce, delSubtasks bool
	del := &cobra.Command{
		Use:   "delete <KEY>",
		Short: "Permanently delete an issue (requires --force; Jira DC has no trash)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !delForce {
				return usageErr("refusing to delete %s without --force (deletion is permanent on Jira DC)", args[0])
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.DeleteIssue(cmd.Context(), args[0], delSubtasks); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"key": args[0], "status": "deleted"}, nil)
		},
	}
	del.Flags().BoolVar(&delForce, "force", false, "confirm permanent deletion")
	del.Flags().BoolVar(&delSubtasks, "delete-subtasks", false, "also delete the issue's subtasks")

	var labelsAdd, labelsRemove string
	labels := &cobra.Command{
		Use:   "labels <KEY>",
		Short: "Add/remove labels on an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			add := splitFields(labelsAdd)
			remove := splitFields(labelsRemove)
			if len(add) == 0 && len(remove) == 0 {
				return usageErr("pass --add and/or --remove")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.UpdateLabels(cmd.Context(), args[0], add, remove); err != nil {
				return err
			}
			return emit(cmd, map[string]any{"key": args[0], "added": add, "removed": remove, "status": "updated"}, nil)
		},
	}
	labels.Flags().StringVar(&labelsAdd, "add", "", "comma-separated labels to add")
	labels.Flags().StringVar(&labelsRemove, "remove", "", "comma-separated labels to remove")

	var assignTo string
	var assignMe, assignNone bool
	assign := &cobra.Command{
		Use:   "assign <KEY>",
		Short: "Set or clear the issue assignee",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			picked := 0
			for _, on := range []bool{assignTo != "", assignMe, assignNone} {
				if on {
					picked++
				}
			}
			if picked != 1 {
				return usageErr("pass exactly one of --to <username>, --me, or --none")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			assignee, err := svc.Assign(cmd.Context(), args[0], assignTo, assignMe)
			if err != nil {
				return err
			}
			out := map[string]string{"key": args[0], "status": "assigned", "assignee": assignee}
			if assignee == "" {
				out["status"] = "unassigned"
			}
			return emit(cmd, out, func() string {
				if assignee == "" {
					return args[0] + "\tunassigned"
				}
				return args[0] + "\tassigned to " + assignee
			})
		},
	}
	assign.Flags().StringVar(&assignTo, "to", "", "DC username to assign the issue to")
	assign.Flags().BoolVar(&assignMe, "me", false, "assign the issue to the authenticated user")
	assign.Flags().BoolVar(&assignNone, "none", false, "remove the assignee")

	history := &cobra.Command{
		Use:   "history <KEY>",
		Short: "Show an issue's changelog (who changed what, when)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			entries, err := svc.History(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"key": args[0], "history": entries}, func() string {
				var b strings.Builder
				for _, e := range entries {
					fmt.Fprintf(&b, "%s\t%s\n", e.Created, e.Author)
					for _, it := range e.Items {
						fmt.Fprintf(&b, "  %s: %s → %s\n", it.Field, it.From, it.To)
					}
				}
				return strings.TrimRight(b.String(), "\n")
			})
		},
	}

	comment := jiraCommentCmd()
	link := jiraLinkCmd()
	plan := jiraIssuePlanCmd()
	attachment := jiraIssueAttachmentCmd()

	var epic string
	linkEpic := &cobra.Command{
		Use:   "link-epic <KEY>",
		Short: "Set the Epic Link of an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if epic == "" {
				return usageErr("--epic is required")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.LinkEpic(cmd.Context(), args[0], epic); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"issue": args[0], "epic": epic, "status": "linked"}, nil)
		},
	}
	linkEpic.Flags().StringVar(&epic, "epic", "", "epic issue key")

	var imgInto string
	images := &cobra.Command{
		Use:   "images <KEY>",
		Short: "Download image attachments to files (agent vision)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			paths, err := svc.Images(cmd.Context(), args[0], imgInto)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"key": args[0], "images": paths}, nil)
		},
	}
	images.Flags().StringVar(&imgInto, "into", "", "output dir")

	var refsJQL, refsFields string
	var refsLimit int
	refs := &cobra.Command{
		Use:   "refs [KEY]",
		Short: "Extract artifact references from one issue or a JQL selection",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := ""
			if len(args) == 1 {
				key = args[0]
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.IssueRefs(cmd.Context(), app.JiraIssueRefsOpts{
				Key:    key,
				JQL:    refsJQL,
				Fields: splitFields(refsFields),
				Limit:  refsLimit,
			})
			if err != nil {
				return err
			}
			return emit(cmd, res, func() string { return issueRefsText(res) })
		},
	}
	refs.Flags().StringVar(&refsJQL, "jql", "", "JQL selecting issues (alternative to KEY)")
	refs.Flags().StringVar(&refsFields, "fields", "", "extra comma-separated fields to fetch before extracting refs")
	refs.Flags().IntVar(&refsLimit, "limit", 100, "max issues for --jql (0 = all)")

	var treeJQL, treeEpicField, treeFields string
	var treeLimit int
	tree := &cobra.Command{
		Use:   "tree",
		Short: "Build a read-only epic-to-child tree from a JQL selection",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.IssueTree(cmd.Context(), app.JiraIssueTreeOpts{
				JQL:       treeJQL,
				EpicField: treeEpicField,
				Fields:    splitFields(treeFields),
				Limit:     treeLimit,
			})
			if err != nil {
				return err
			}
			return emit(cmd, res, func() string { return issueTreeText(res) })
		},
	}
	tree.Flags().StringVar(&treeJQL, "jql", "", "JQL selecting issues")
	tree.Flags().StringVar(&treeEpicField, "epic-field", "", "field id/name containing parent epic key")
	tree.Flags().StringVar(&treeFields, "fields", "", "extra comma-separated fields to fetch")
	tree.Flags().IntVar(&treeLimit, "limit", 100, "max issues (0 = all)")

	c.AddCommand(get, jiraIssueViewCmd(), search, create, update, edit, transition, check, del, assign, labels, history, refs, tree, comment, link, plan, jiraIssueFieldCmd(), linkEpic, attachment, images)
	return c
}

func jiraIssueViewCmd() *cobra.Command {
	var root string
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "view <KEY>",
		Short: "Render one issue as configured Markdown without writing a mirror",
		Long: "Fetch and render one Jira issue through the configured Markdown view without writing files. " +
			"Default JSON contains key and markdown; -o text emits raw Markdown. " +
			"This is read-only and creates no writeback baseline: pull the issue before editing it.",
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
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.ViewIssue(cmd.Context(), args[0], app.JiraIssueViewOpts{
				Root:   configRoot,
				Render: override,
			})
			if err != nil {
				return err
			}
			warnRender(cmd.ErrOrStderr(), res.Warnings)
			return emit(cmd, res, func() string { return res.Markdown })
		},
	}
	cmd.Flags().StringVar(&root, "render-root", mirrorRootDefault("."), "root whose .atl/config.json supplies local render settings (never written)")
	rf.register(cmd)
	return cmd
}

func jiraIssueAttachmentCmd() *cobra.Command {
	c := &cobra.Command{Use: "attachment", Short: "Attachment list/get/upload"}

	list := &cobra.Command{
		Use:   "list <KEY>",
		Short: "List issue attachments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			atts, err := svc.Attachments(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"key": args[0], "attachments": atts}, func() string {
				var b strings.Builder
				for _, a := range atts {
					fmt.Fprintf(&b, "%s\t%s\t%s\t%d bytes\n", a.ID, a.Title, a.MediaType, a.FileSize)
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

	var getID, getInto string
	get := &cobra.Command{
		Use:   "get <KEY>",
		Short: "Download an issue attachment to a directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if getID == "" {
				return usageErr("--id is required")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			path, name, err := svc.DownloadAttachment(cmd.Context(), args[0], getID, getInto)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]string{"key": args[0], "id": getID, "name": name, "path": path}, func() string {
				return path
			})
		},
	}
	get.Flags().StringVar(&getID, "id", "", "attachment id or filename")
	get.Flags().StringVar(&getInto, "into", ".", "output directory")

	var uploadFile string
	upload := &cobra.Command{
		Use:   "upload <KEY>",
		Short: "Upload a file as an issue attachment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if uploadFile == "" {
				return usageErr("--file is required")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			att, err := svc.UploadAttachment(cmd.Context(), args[0], uploadFile)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"key": args[0], "attachment": att}, nil)
		},
	}
	upload.Flags().StringVar(&uploadFile, "file", "", "local file path to upload")

	c.AddCommand(list, get, upload)
	return c
}

func issueRefsText(res *app.JiraIssueRefsResult) string {
	var b strings.Builder
	for _, issue := range res.Issues {
		fmt.Fprintf(&b, "%s\t%s\n", issue.Key, issue.Summary)
		for _, ref := range issue.Refs {
			fmt.Fprintf(&b, "  - %s\t%s\n", ref.Kind, ref.URL)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func issueTreeText(res *app.JiraIssueTreeResult) string {
	var b strings.Builder
	writeEpics := func(title string, epics []app.JiraIssueTreeEpic) {
		if len(epics) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s\n", title)
		for _, epic := range epics {
			label := epic.Key
			if epic.Summary != "" {
				label += "\t" + epic.Summary
			}
			fmt.Fprintf(&b, "- %s\n", label)
			for _, child := range epic.Children {
				fmt.Fprintf(&b, "  - %s\t%s\n", child.Key, child.Summary)
			}
		}
	}
	writeEpics("epics", res.Epics)
	writeEpics("external_epics", res.ExternalEpics)
	if len(res.Orphans) > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("orphans\n")
		for _, issue := range res.Orphans {
			fmt.Fprintf(&b, "- %s\t%s\n", issue.Key, issue.Summary)
		}
	}
	return strings.TrimRight(b.String(), "\n")
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
	search.Flags().IntVar(&limit, "limit", 50, "max results")

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

// jiraCommentCmd builds `jira issue comment {add,list,delete}`.
func jiraCommentCmd() *cobra.Command {
	c := &cobra.Command{Use: "comment", Short: "List/add/delete issue comments"}

	var fromFile, fromMD string
	add := &cobra.Command{
		Use:   "add <KEY>",
		Short: "Add a comment (wiki via --from-file -, or markdown via --from-md)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := wikiBody(cmd, orDash(fromFile), fromMD)
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			cm, err := svc.Comment(cmd.Context(), args[0], body)
			if err != nil {
				return err
			}
			return emit(cmd, cm, nil)
		},
	}
	add.Flags().StringVar(&fromFile, "from-file", "-", "comment body file or - for stdin")
	add.Flags().StringVar(&fromMD, "from-md", "", "markdown comment file or - for stdin (converted to wiki; unsupported constructs are refused)")

	list := &cobra.Command{
		Use:   "list <KEY>",
		Short: "List an issue's comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			cs, err := svc.Comments(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"key": args[0], "comments": cs}, func() string {
				var b strings.Builder
				for _, cm := range cs {
					fmt.Fprintf(&b, "%s\t%s (%s):\n%s\n\n", cm.ID, cm.Author, cm.Created, cm.Body)
				}
				return strings.TrimRight(b.String(), "\n")
			}, func() []string {
				ids := make([]string, len(cs))
				for i, cm := range cs {
					ids[i] = cm.ID
				}
				return ids
			})
		},
	}

	del := &cobra.Command{
		Use:   "delete <KEY> <COMMENT-ID>",
		Short: "Delete a comment by id",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.DeleteComment(cmd.Context(), args[0], args[1]); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"key": args[0], "comment": args[1], "status": "deleted"}, nil)
		},
	}

	c.AddCommand(add, list, del)
	return c
}

// jiraLinkCmd builds `jira issue link {add,list,delete}`.
func jiraLinkCmd() *cobra.Command {
	c := &cobra.Command{Use: "link", Short: "List/add/delete issue links"}

	var linkTo, linkType string
	add := &cobra.Command{
		Use:   "add <KEY>",
		Short: "Link an issue to another (--to KEY2 --type blocks)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if linkTo == "" || linkType == "" {
				return usageErr("--to and --type are required")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.Link(cmd.Context(), args[0], linkTo, linkType); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"from": args[0], "to": linkTo, "type": linkType, "status": "linked"}, nil)
		},
	}
	add.Flags().StringVar(&linkTo, "to", "", "target issue key")
	add.Flags().StringVar(&linkType, "type", "", "link type name (e.g. blocks)")

	list := &cobra.Command{
		Use:   "list <KEY>",
		Short: "List an issue's links (with link ids for deletion)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			links, err := svc.Links(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"key": args[0], "links": links}, func() string {
				var b strings.Builder
				for _, l := range links {
					fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", l.ID, l.Direction, l.Type, l.Key)
				}
				return strings.TrimRight(b.String(), "\n")
			}, func() []string {
				ids := make([]string, len(links))
				for i, l := range links {
					ids[i] = l.ID
				}
				return ids
			})
		},
	}

	del := &cobra.Command{
		Use:   "delete <LINK-ID>",
		Short: "Delete an issue link by id (see `link list`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.DeleteLink(cmd.Context(), args[0]); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"link": args[0], "status": "deleted"}, nil)
		},
	}

	var suggestCSV string
	suggest := &cobra.Command{
		Use:   "suggest",
		Short: "Suggest missing links from a reviewed CSV plan without writing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.SuggestLinks(cmd.Context(), app.JiraLinkSuggestOpts{CSVPath: suggestCSV})
			if err != nil {
				return err
			}
			return emit(cmd, res, func() string { return linkSuggestText(res) })
		},
	}
	suggest.Flags().StringVar(&suggestCSV, "csv", "", "CSV plan with source,target,type and optional rationale")

	c.AddCommand(add, list, del, suggest)
	return c
}

func linkSuggestText(res *app.JiraLinkSuggestResult) string {
	var b strings.Builder
	for _, candidate := range res.Candidates {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", candidate.Source, candidate.Target, candidate.Type, candidate.Rationale)
	}
	return strings.TrimRight(b.String(), "\n")
}

func jiraIssuePlanCmd() *cobra.Command {
	c := &cobra.Command{Use: "plan", Short: "Preview/apply guarded Jira operation plans"}

	var csvPath, confirm, allowOps, allowFields, allowLinkTypes string
	var apply, continueOnError bool
	applyCmd := &cobra.Command{
		Use:   "apply",
		Short: "Preview or apply a guarded CSV operation plan",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.ApplyPlan(cmd.Context(), app.JiraPlanApplyOpts{
				CSVPath:         csvPath,
				Apply:           apply,
				Confirm:         confirm,
				AllowOps:        splitFields(allowOps),
				AllowFields:     splitFields(allowFields),
				AllowLinkTypes:  splitFields(allowLinkTypes),
				ContinueOnError: continueOnError,
			})
			if res == nil {
				return err
			}
			if emitErr := emit(cmd, res, func() string { return issuePlanApplyText(res) }); emitErr != nil {
				return emitErr
			}
			return err
		},
	}
	applyCmd.Flags().StringVar(&csvPath, "csv", "", "CSV plan with op,source and operation-specific columns")
	applyCmd.Flags().BoolVar(&apply, "apply", false, "perform writes; default is dry-run")
	applyCmd.Flags().StringVar(&confirm, "confirm", "", "required value APPLY when --apply is set")
	applyCmd.Flags().StringVar(&allowOps, "allow-ops", "link", "comma-separated allowed operations: link,label_add,label_remove,comment,field")
	applyCmd.Flags().StringVar(&allowFields, "allow-fields", "", "comma-separated field ids/names allowed for field operations")
	applyCmd.Flags().StringVar(&allowLinkTypes, "allow-link-types", "", "comma-separated explicit link-type exceptions to Jira metadata")
	applyCmd.Flags().BoolVar(&continueOnError, "continue-on-error", false, "continue independent rows after a blocked or failed operation")

	c.AddCommand(applyCmd)
	return c
}

func issuePlanApplyText(res *app.JiraPlanApplyResult) string {
	var b strings.Builder
	for _, row := range res.Results {
		fmt.Fprintf(&b, "%d\t%s\t%s\t%s", row.Row, row.Status, row.Op, row.Source)
		if row.Target != "" {
			fmt.Fprintf(&b, "\t%s", row.Target)
		}
		if row.Field != "" {
			fmt.Fprintf(&b, "\t%s=%s", row.Field, row.Value)
		}
		if row.Message != "" {
			fmt.Fprintf(&b, "\t%s", row.Message)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func jiraPullCmd() *cobra.Command {
	var jql, into string
	var fields string
	var limit int
	var assets bool
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Export issues matching --jql to one .wiki + .md + .json set per issue",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if jql == "" {
				return usageErr("--jql is required")
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
				JQL:    jql,
				Into:   into,
				Limit:  limit,
				Fields: splitFields(fields),
				Assets: assets,
				Render: override,
			})
			if err != nil {
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
			return emit(cmd, res, func() string {
				var b strings.Builder
				for _, p := range res.Issues {
					fmt.Fprintf(&b, "%s\t%s\n", p.Key, p.Path)
				}
				return strings.TrimRight(b.String(), "\n")
			})
		},
	}
	cmd.Flags().StringVar(&jql, "jql", "", "JQL selecting issues")
	cmd.Flags().StringVar(&into, "into", mirrorRootDefault("mirror-jira"), "output root dir (default: $ATL_MIRROR_ROOT or \"mirror-jira\")")
	cmd.Flags().IntVar(&limit, "limit", 100, "max issues (0 = all)")
	cmd.Flags().StringVar(&fields, "fields", "", "extra comma-separated field list to include in JSON snapshots")
	cmd.Flags().BoolVar(&assets, "assets", false, "also mirror each issue's image attachments into a per-issue <KEY>.assets/ dir and link them from the .md")
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
	cmd := &cobra.Command{
		Use:   "status [DIR]",
		Short: "Show locally-edited (and optionally remote-drifted) mirrored issues",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := mirrorRootDefault("mirror-jira")
			if len(args) == 1 {
				dir = args[0]
			}
			svc, err := jiraService()
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
	cmd.Flags().BoolVar(&remote, "remote", false, "also check remote drift (one request per issue)")
	return cmd
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
		Short: "Export issues matching --jql to one compact artifact plus a manifest",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return usageErr("--out is required")
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
			})
			if err != nil {
				return err
			}
			return emit(cmd, res, func() string {
				return fmt.Sprintf("%s\t%s\t%d issues", res.Path, res.Format, res.Count)
			})
		},
	}
	cmd.Flags().StringVar(&jql, "jql", "", "JQL selecting issues")
	cmd.Flags().StringVar(&ids, "ids", "", "comma-separated numeric issue ids; generates batched `id in (...)` JQL")
	cmd.Flags().StringVar(&keys, "keys", "", "comma-separated issue keys; generates batched `key in (...)` JQL")
	cmd.Flags().IntVar(&batchSize, "batch-size", 100, "max ids/keys per generated JQL batch")
	cmd.Flags().StringVar(&out, "out", "", "output artifact path (manifest is written to <out>.manifest.json)")
	cmd.Flags().StringVar(&format, "format", "jsonl", "export format: jsonl, json, or csv")
	cmd.Flags().IntVar(&limit, "limit", 100, "max issues (0 = all)")
	cmd.Flags().StringVar(&fields, "fields", "", "extra comma-separated field list to include")
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
	cmd.Flags().IntVar(&limit, "limit", 100, "max issues (0 = all)")
	cmd.Flags().StringVar(&csvPath, "csv", "", "optional CSV report path")
	cmd.Flags().BoolVar(&rawCSV, "raw-csv", false, "write formula-leading CSV cells verbatim (unsafe in spreadsheets; requires --csv)")
	return cmd
}

func jiraMetaCmds() []*cobra.Command {
	var nameLike, fieldID, idLike, schema, custom string
	fields := &cobra.Command{
		Use:   "fields",
		Short: "List Jira fields (id/name/custom)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			fs, err := svc.Fields(cmd.Context())
			if err != nil {
				return err
			}
			fs, err = filterFieldDefs(fs, fieldID, nameLike, idLike, schema, custom)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"fields": fs}, nil)
		},
	}
	fields.Flags().StringVar(&nameLike, "name-like", "", "case-insensitive substring filter for field name")
	fields.Flags().StringVar(&fieldID, "id", "", "exact field id filter")
	fields.Flags().StringVar(&idLike, "id-like", "", "case-insensitive substring filter for field id")
	fields.Flags().StringVar(&schema, "schema", "", "exact schema type filter")
	fields.Flags().StringVar(&custom, "custom", "", "filter custom fields: true or false")

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
			return emit(cmd, map[string]any{"options": vals}, nil)
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
			return emit(cmd, map[string]any{"transitions": trs}, nil)
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
			return emit(cmd, map[string]any{"link_types": lts}, nil)
		},
	}

	return []*cobra.Command{fields, opts, transitions, linkTypes}
}

func filterFieldDefs(fs []domain.FieldDef, id, nameLike, idLike, schema, custom string) ([]domain.FieldDef, error) {
	id = strings.TrimSpace(id)
	nameLike = strings.ToLower(strings.TrimSpace(nameLike))
	idLike = strings.ToLower(strings.TrimSpace(idLike))
	schema = strings.TrimSpace(schema)
	custom = strings.ToLower(strings.TrimSpace(custom))
	var wantCustom *bool
	if custom != "" {
		switch custom {
		case "true", "1", "yes":
			v := true
			wantCustom = &v
		case "false", "0", "no":
			v := false
			wantCustom = &v
		default:
			return nil, usageErr("--custom must be true or false")
		}
	}
	if id == "" && nameLike == "" && idLike == "" && schema == "" && wantCustom == nil {
		return fs, nil
	}
	out := make([]domain.FieldDef, 0, len(fs))
	for _, f := range fs {
		if id != "" && f.ID != id {
			continue
		}
		if idLike != "" && !strings.Contains(strings.ToLower(f.ID), idLike) {
			continue
		}
		if nameLike != "" && !strings.Contains(strings.ToLower(f.Name), nameLike) {
			continue
		}
		if schema != "" && f.Schema != schema {
			continue
		}
		if wantCustom != nil && f.Custom != *wantCustom {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
