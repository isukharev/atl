package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
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
	cmds := []*cobra.Command{jiraIssueCmd(), jiraPullCmd()}
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
	transition := &cobra.Command{
		Use:   "transition <KEY>",
		Short: "Transition an issue to a status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return usageErr("--to is required")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.Transition(cmd.Context(), args[0], to, transComment); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"key": args[0], "to": to, "status": "transitioned"}, nil)
		},
	}
	transition.Flags().StringVar(&to, "to", "", "target status/transition name")
	transition.Flags().StringVar(&transComment, "comment", "", "optional comment")

	var commentFile string
	comment := &cobra.Command{
		Use:   "comment <KEY>",
		Short: "Add a comment (wiki via --from-file -)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readBody(orDash(commentFile))
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
	comment.Flags().StringVar(&commentFile, "from-file", "-", "comment body file or - for stdin")

	var linkTo, linkType string
	link := &cobra.Command{
		Use:   "link <KEY>",
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
	link.Flags().StringVar(&linkTo, "to", "", "target issue key")
	link.Flags().StringVar(&linkType, "type", "", "link type name (e.g. blocks)")

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

	c.AddCommand(get, search, create, update, transition, comment, link, linkEpic, images)
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
