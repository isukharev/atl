package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/version"
)

func jiraService() (*app.JiraService, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return app.NewJira(cfg, version.Version)
}

func newJiraCmd() *cobra.Command {
	c := &cobra.Command{Use: "jira", Short: "Jira: read/search/pull issues, edit via commands (native wiki)"}
	cmds := []*cobra.Command{jiraIssueCmd(), jiraPullCmd(), jiraMeCmd(), jiraUserCmd(), jiraBoardCmd(), jiraSprintCmd()}
	cmds = append(cmds, jiraMetaCmds()...)
	c.AddCommand(cmds...)
	return c
}

func splitFields(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
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

	var project, issueType, summary, fromFile string
	var fieldKV []string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create an issue (description = wiki via --from-file -)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" || issueType == "" || summary == "" {
				return usageErr("--project, --type and --summary are required")
			}
			body, err := readBody(fromFile)
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
	create.Flags().StringArrayVar(&fieldKV, "field", nil, "extra field key=value (repeatable)")

	var upSummary, upFile string
	var upFieldKV []string
	update := &cobra.Command{
		Use:   "update <KEY>",
		Short: "Update an issue (summary/description/fields)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var body []byte
			var err error
			if upFile != "" {
				body, err = readBody(upFile)
				if err != nil {
					return err
				}
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
	update.Flags().StringArrayVar(&upFieldKV, "field", nil, "field key=value (repeatable)")

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

	c.AddCommand(get, search, create, update, transition, check, del, labels, history, comment, link, linkEpic, images)
	return c
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

	var fromFile string
	add := &cobra.Command{
		Use:   "add <KEY>",
		Short: "Add a comment (wiki via --from-file -)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readBody(orDash(fromFile))
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

	c.AddCommand(add, list, del)
	return c
}

func jiraPullCmd() *cobra.Command {
	var jql, into string
	var limit int
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Export issues matching --jql to one md+json per issue",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if jql == "" {
				return usageErr("--jql is required")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			pulled, err := svc.Pull(cmd.Context(), jql, into, limit)
			if err != nil {
				return err
			}
			return emit(cmd, map[string]any{"into": into, "issues": pulled}, func() string {
				var b strings.Builder
				for _, p := range pulled {
					fmt.Fprintf(&b, "%s\t%s\n", p.Key, p.Path)
				}
				return strings.TrimRight(b.String(), "\n")
			})
		},
	}
	cmd.Flags().StringVar(&jql, "jql", "", "JQL selecting issues")
	cmd.Flags().StringVar(&into, "into", mirrorRootDefault("mirror-jira"), "output root dir (default: $ATL_MIRROR_ROOT or \"mirror-jira\")")
	cmd.Flags().IntVar(&limit, "limit", 100, "max issues (0 = all)")
	return cmd
}

func jiraMetaCmds() []*cobra.Command {
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
			return emit(cmd, map[string]any{"fields": fs}, nil)
		},
	}

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

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
