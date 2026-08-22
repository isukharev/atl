package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func jiraIssueCmd() *cobra.Command {
	c := &cobra.Command{Use: "issue", Short: "Issue operations"}
	c.AddCommand(jiraIssueTypesCmd(), jiraIssueCreateCheckCmd(), jiraIssueCreateMetadataCmd(), jiraIssueInverseReferenceCmd())

	var fields string
	get := &cobra.Command{
		Use:   "get <KEY>",
		Short: "Get an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			is, err := svc.IssueResolved(cmd.Context(), args[0], splitFields(fields))
			if err != nil {
				return err
			}
			return emit(cmd, is, func() string {
				return fmt.Sprintf("%s [%s] %s\n\n%s", is.Key, is.Status, is.Summary, is.Body)
			})
		},
	}
	get.Flags().StringVar(&fields, "fields", "", "comma-separated field list")

	var jql, searchColumns, searchView, cursor string
	var limit int
	search := &cobra.Command{
		Use:   "search",
		Short: "Search issues by JQL",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if jql == "" {
				return usageErr("--jql is required")
			}
			if err := validatePageLimit(limit, 1000); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			list, err := svc.SearchIssueListView(cmd.Context(), jql, splitFields(searchColumns), searchView, limit, cursor)
			if err != nil {
				return err
			}
			return emitID(cmd, list, func() string { return app.IssueListMarkdown(list, false) }, func() []string { return app.IssueListKeys(list) })
		},
	}
	search.Flags().StringVar(&jql, "jql", "", "JQL query")
	search.Flags().StringVar(&searchColumns, "columns", "", "ordered list columns (default: key,summary,status,assignee)")
	search.Flags().StringVar(&searchView, "view", "", "named Jira list view from config (default: default; explicit --columns wins)")
	search.Flags().IntVar(&limit, "limit", 50, "max results (1..1000)")
	search.Flags().StringVar(&cursor, "cursor", "", "pagination cursor (startAt)")

	var childrenColumns, childrenView, childrenCursor, childrenEpicField string
	var childrenLimit int
	children := &cobra.Command{
		Use:   "children <EPIC-KEY>",
		Short: "List direct epic children through the common IssueList projection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validatePageLimit(childrenLimit, 1000); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			list, err := svc.EpicChildrenIssueList(cmd.Context(), args[0], app.JiraEpicChildrenOpts{
				Columns: splitFields(childrenColumns), View: childrenView, Limit: childrenLimit, Cursor: childrenCursor, EpicField: childrenEpicField,
			})
			if err != nil {
				return err
			}
			return emitID(cmd, list, func() string { return app.IssueListMarkdown(list, false) }, func() []string { return app.IssueListKeys(list) })
		},
	}
	children.Flags().StringVar(&childrenColumns, "columns", "", "ordered list columns (default: key,summary,status,issuetype,assignee)")
	children.Flags().StringVar(&childrenView, "view", "", "named Jira list view from config (default: default; explicit --columns wins)")
	children.Flags().IntVar(&childrenLimit, "limit", 50, "max results (1..1000)")
	children.Flags().StringVar(&childrenCursor, "cursor", "", "pagination cursor (startAt)")
	children.Flags().StringVar(&childrenEpicField, "epic-field", "", "Epic Link field id or display name (auto-detected when omitted)")

	create := jiraIssueCreateCmd()

	var upSummary, upFile, upMD string
	var upFieldKV, upFieldJSON []string
	update := &cobra.Command{
		Use:   "update <KEY>",
		Short: "Update an issue (summary/description/fields)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := wikiBody(cmd, upFile, upMD)
			if err != nil {
				return err
			}
			kv, err := parseJiraFieldInputs(upFieldKV, upFieldJSON, false)
			if err != nil {
				return err
			}
			svc, err := jiraService(cmd)
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
	update.Flags().StringArrayVar(&upFieldKV, "field", nil, "field key=value (repeatable); JSON objects/arrays are sent as JSON")
	update.Flags().StringArrayVar(&upFieldJSON, "field-json", nil, "field key=JSON (repeatable); sends an explicit JSON value including scalars")

	edit := jiraDescriptionEditCmd()

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
			svc, err := jiraService(cmd)
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

	var delApply, delSubtasks bool
	var delConfirm, delExpectedUpdated, delExpectedProposalHash string
	del := &cobra.Command{
		Use:   "delete <KEY>",
		Short: "Preview or apply one reviewed permanent issue deletion",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, deleteErr := svc.DeleteIssueGuarded(cmd.Context(), args[0], app.JiraIssueDeleteOpts{
				Apply: delApply, Confirm: delConfirm, DeleteSubtasks: delSubtasks,
				ExpectedUpdated: delExpectedUpdated, ExpectedProposalHash: delExpectedProposalHash,
			})
			if result == nil {
				return deleteErr
			}
			emitErr := emit(cmd, result, func() string { return app.JiraIssueDeleteText(result) })
			return guardedMutationResultErr(deleteErr, emitErr, result.WriteAttempted, "Jira issue deletion")
		},
	}
	del.Flags().BoolVar(&delApply, "apply", false, "apply the reviewed permanent deletion (default: preview only)")
	del.Flags().StringVar(&delConfirm, "confirm", "", "exact confirmation token DELETE (required with --apply)")
	del.Flags().BoolVar(&delSubtasks, "delete-subtasks", false, "include explicit cascade intent in the reviewed proposal")
	del.Flags().StringVar(&delExpectedUpdated, "expected-updated", "", "reviewed Jira updated marker (required with --apply)")
	del.Flags().StringVar(&delExpectedProposalHash, "expected-proposal-hash", "", "reviewed proposal hash (required with --apply)")

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
			svc, err := jiraService(cmd)
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
			svc, err := jiraService(cmd)
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

	var historyFields []string
	var historySince, historyUntil string
	var historySummaryOnly bool
	history := &cobra.Command{
		Use:   "history <KEY>",
		Short: "Show an issue's changelog (who changed what, when)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("summary-only") && !historySummaryOnly {
				return usageErr("--summary-only cannot be false; omit the flag to request raw history")
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.HistoryFiltered(cmd.Context(), args[0], app.JiraHistoryOpts{
				Fields: historyFields, Since: historySince, Until: historyUntil,
			})
			if err != nil {
				return err
			}
			if historySummaryOnly {
				summary := app.JiraHistorySummaryProjection(result)
				return emit(cmd, summary, func() string { return app.JiraHistorySummaryMarkdown(summary) })
			}
			return emit(cmd, result, func() string { return app.JiraHistoryMarkdown(result) })
		},
	}
	history.Flags().StringArrayVar(&historyFields, "field", nil, "exact field id or display name to include (repeatable)")
	history.Flags().StringVar(&historySince, "since", "", "include changes at/after date (Jira user zone) or explicit timestamp")
	history.Flags().StringVar(&historyUntil, "until", "", "include changes through date (Jira user zone) or explicit timestamp")
	history.Flags().BoolVar(&historySummaryOnly, "summary-only", false, "omit raw history and emit its deterministic summary projection")

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
			svc, err := jiraService(cmd)
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
			svc, err := jiraService(cmd)
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
		Short: "Extract provenance-qualified artifact references",
		Long: "Extract deterministic artifact references from one issue or a JQL selection. " +
			"Selection, description, requested fields, and comments carry explicit completeness; " +
			"an empty refs list proves absence only when complete is true.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := ""
			if len(args) == 1 {
				key = args[0]
			}
			svc, err := jiraService(cmd)
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
	refs.Flags().StringVar(&refsFields, "fields", "", "extra comma-separated field ids or exact display names to extract refs from")
	refs.Flags().IntVar(&refsLimit, "limit", 100, "max issues for --jql (0 = all)")

	var treeJQL, treeEpicField, treeFields string
	var treeLimit int
	tree := &cobra.Command{
		Use:   "tree",
		Short: "Build a read-only epic-to-child tree from a JQL selection",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateAggregateLimit(treeLimit); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
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
	tree.Flags().IntVar(&treeLimit, "limit", 100, "max issues (0 = all; must be non-negative)")

	c.AddCommand(get, jiraIssueViewCmd(), jiraIssueFieldsCmd(), jiraIssueGraphCmd(), search, children, create, update, edit, jiraTransitionCmd(), check, del, assign, labels, jiraIssueWatchersCmd(), jiraIssueWorklogCmd(), history, refs, tree, comment, link, plan, jiraIssueFieldCmd(), linkEpic, attachment, images)
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
			svc, err := jiraService(cmd)
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
			if invocationRuntimeFor(cmd).outputFormat == "text" {
				_, err := io.WriteString(cmd.OutOrStdout(), res.Markdown)
				return err
			}
			return emit(cmd, res, nil)
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
			svc, err := jiraService(cmd)
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
			svc, err := jiraService(cmd)
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
			svc, err := jiraService(cmd)
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
	fmt.Fprintf(&b, "Complete: %t\nSelection: %s, %d issue(s), complete=%t", res.Complete, res.Selection.Mode, res.Selection.Count, res.Selection.Complete)
	if res.Selection.Truncated {
		b.WriteString(", truncated=true")
	}
	b.WriteString("\n\n")
	rows := [][]string{}
	for _, issue := range res.Issues {
		if len(issue.Refs) == 0 {
			rows = append(rows, []string{issue.Key, issue.Summary, strconv.FormatBool(issue.Complete), "", ""})
		} else {
			for _, ref := range issue.Refs {
				rows = append(rows, []string{issue.Key, issue.Summary, strconv.FormatBool(issue.Complete), ref.Kind, ref.URL})
			}
		}
	}
	b.WriteString(app.MarkdownTable([]string{"Key", "Summary", "Complete", "Kind", "URL"}, rows))
	wroteWarnings := false
	if len(res.Warnings) > 0 {
		b.WriteString("\nWarnings:\n")
		wroteWarnings = true
		for _, warning := range res.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}
	for _, issue := range res.Issues {
		if len(issue.Warnings) == 0 {
			continue
		}
		if !wroteWarnings {
			b.WriteString("\nWarnings:\n")
			wroteWarnings = true
		}
		for _, warning := range issue.Warnings {
			fmt.Fprintf(&b, "- %s: %s\n", issue.Key, warning)
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

// jiraLinkCmd builds `jira issue link {add,list,delete}`.
func jiraLinkCmd() *cobra.Command {
	c := &cobra.Command{Use: "link", Short: "List/add/delete issue links"}
	add := jiraGuardedLinkAddCmd()

	list := &cobra.Command{
		Use:   "list <KEY>",
		Short: "List an issue's links (with link ids for deletion)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService(cmd)
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

	del := jiraGuardedLinkDeleteCmd()

	var suggestCSV string
	suggest := &cobra.Command{
		Use:   "suggest",
		Short: "Suggest missing links from a reviewed CSV plan without writing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := jiraService(cmd)
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
			svc, err := jiraService(cmd)
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
