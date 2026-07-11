package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

// atoiArg parses a positional integer argument (a board or sprint id), mapping a
// non-numeric value to a usage error (exit 2) before any network call.
func atoiArg(name, s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, usageErr("%s must be a positive number, got %q", name, s)
	}
	return n, nil
}

// jiraBoardCmd builds read-only board analysis plus existing board discovery.
func jiraBoardCmd() *cobra.Command {
	c := &cobra.Command{Use: "board", Short: "Inspect Agile boards, workflow columns, ranked issues, and backlog"}

	var project, cursor string
	var limit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List agile boards (optionally for one project)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			boards, next, err := svc.Boards(cmd.Context(), project, limit, cursor)
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"boards": boards, "next_cursor": next}, func() string {
				var b strings.Builder
				for _, bd := range boards {
					fmt.Fprintf(&b, "%d\t%s\t%s\t%s\n", bd.ID, bd.Type, bd.ProjectKey, bd.Name)
				}
				return strings.TrimRight(b.String(), "\n")
			}, func() []string {
				ids := make([]string, len(boards))
				for i, bd := range boards {
					ids[i] = strconv.Itoa(bd.ID)
				}
				return ids
			})
		},
	}
	list.Flags().StringVar(&project, "project", "", "filter to a project (key or id)")
	list.Flags().IntVar(&limit, "limit", 50, "max results (capped at 50 by the Agile API)")
	list.Flags().StringVar(&cursor, "cursor", "", "pagination cursor (startAt)")

	get := &cobra.Command{
		Use:   "get <BOARD-ID>",
		Short: "Get a board by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoiArg("board id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			bd, err := svc.Board(cmd.Context(), id)
			if err != nil {
				return err
			}
			return emitID(cmd, bd,
				func() string { return fmt.Sprintf("%d\t%s\t%s\t%s", bd.ID, bd.Type, bd.ProjectKey, bd.Name) },
				func() []string { return []string{strconv.Itoa(bd.ID)} })
		},
	}

	configCmd := &cobra.Command{
		Use:   "config <BOARD-ID>",
		Short: "Get board filter, columns/statuses, constraints, estimation, and rank configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoiArg("board id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			config, err := svc.BoardConfiguration(cmd.Context(), id)
			if err != nil {
				return err
			}
			return emitID(cmd, config, func() string { return boardConfigText(config) }, func() []string { return []string{strconv.Itoa(config.ID)} })
		},
	}

	var issueColumns, issueJQL, issueCursor string
	var issueLimit int
	issuesCmd := boardIssuePageCmd("issues", "board", &issueColumns, &issueJQL, &issueCursor, &issueLimit)

	var backlogColumns, backlogJQL, backlogCursor string
	var backlogLimit int
	backlogCmd := boardIssuePageCmd("backlog", "backlog", &backlogColumns, &backlogJQL, &backlogCursor, &backlogLimit)

	var viewScope, viewColumns, viewJQL string
	var viewLimit int
	view := &cobra.Command{
		Use:   "view <BOARD-ID>",
		Short: "Read a normalized board/config/backlog snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoiArg("board id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			snapshot, err := svc.BoardSnapshot(cmd.Context(), id, app.BoardSnapshotOpts{Scope: viewScope, Columns: splitFields(viewColumns), JQL: viewJQL, Limit: viewLimit})
			if err != nil {
				return err
			}
			return emitID(cmd, snapshot, func() string { return app.BoardSnapshotMarkdown(snapshot) }, func() []string { return boardSnapshotKeys(snapshot) })
		},
	}
	view.Flags().StringVar(&viewScope, "scope", "all", "snapshot scope: all, board, or backlog")
	view.Flags().StringVar(&viewColumns, "columns", "", "ordered list columns (default: position,key,summary,status,board.column,assignee)")
	view.Flags().StringVar(&viewJQL, "jql", "", "optional JQL refinement applied by the board endpoints")
	view.Flags().IntVar(&viewLimit, "limit", 0, "maximum issues per requested scope (0 means all)")
	_ = view.RegisterFlagCompletionFunc("scope", fixedComp("all", "board", "backlog"))

	var exportScope, exportColumns, exportJQL, exportFormat, exportOut string
	var exportLimit int
	var exportRawCSV bool
	exportCmd := &cobra.Command{
		Use:   "export <BOARD-ID>",
		Short: "Write a normalized board snapshot as JSON, JSONL, CSV, or Markdown",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoiArg("board id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			result, err := svc.BoardExport(cmd.Context(), id, app.BoardExportOpts{
				BoardSnapshotOpts: app.BoardSnapshotOpts{Scope: exportScope, Columns: splitFields(exportColumns), JQL: exportJQL, Limit: exportLimit},
				Format:            exportFormat, Out: exportOut, RawCSV: exportRawCSV,
			})
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf("%s\tformat=%s\trows=%d\tcomplete=%t", result.Path, result.Format, result.RowCount, result.Complete)
			})
		},
	}
	exportCmd.Flags().StringVar(&exportScope, "scope", "all", "snapshot scope: all, board, or backlog")
	exportCmd.Flags().StringVar(&exportColumns, "columns", "", "ordered list columns")
	exportCmd.Flags().StringVar(&exportJQL, "jql", "", "optional JQL refinement applied by the board endpoints")
	exportCmd.Flags().IntVar(&exportLimit, "limit", 0, "maximum issues per requested scope (0 means all)")
	exportCmd.Flags().StringVar(&exportFormat, "format", "json", "export format: json, jsonl, csv, or md")
	exportCmd.Flags().StringVar(&exportOut, "out", "", "required output file path")
	exportCmd.Flags().BoolVar(&exportRawCSV, "raw-csv", false, "write formula-leading CSV cells verbatim (unsafe in spreadsheets)")
	_ = exportCmd.RegisterFlagCompletionFunc("scope", fixedComp("all", "board", "backlog"))

	c.AddCommand(list, get, configCmd, issuesCmd, backlogCmd, view, exportCmd)
	return c
}

func boardIssuePageCmd(use, scope string, columns, jql, cursor *string, limit *int) *cobra.Command {
	scopeLabel := "board scope"
	if scope == "backlog" {
		scopeLabel = "Scrum backlog scope"
	}
	cmd := &cobra.Command{
		Use:   use + " <BOARD-ID>",
		Short: "List one ranked page from the " + scopeLabel,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoiArg("board id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			list, err := svc.BoardIssueList(cmd.Context(), id, scope, splitFields(*columns), *jql, *limit, *cursor)
			if err != nil {
				return err
			}
			return emitID(cmd, list, func() string { return app.IssueListMarkdown(list, false) }, func() []string { return app.IssueListKeys(list) })
		},
	}
	cmd.Flags().StringVar(columns, "columns", "", "ordered list columns (default: position,key,summary,status,assignee)")
	cmd.Flags().StringVar(jql, "jql", "", "optional JQL refinement")
	cmd.Flags().IntVar(limit, "limit", 50, "page size (capped at 50 by the Agile API)")
	cmd.Flags().StringVar(cursor, "cursor", "", "pagination cursor (startAt)")
	return cmd
}

func boardConfigText(config *domain.BoardConfiguration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d\t%s\t%s\tfilter=%s\n", config.ID, config.Type, config.Name, config.FilterID)
	for index, column := range config.Columns {
		fmt.Fprintf(&b, "%d\t%s\tstatuses=%s\n", index, column.Name, strings.Join(column.StatusIDs, ","))
	}
	return strings.TrimRight(b.String(), "\n")
}

func boardSnapshotKeys(snapshot *app.BoardSnapshot) []string {
	keys := make([]string, len(snapshot.Rows))
	for i, row := range snapshot.Rows {
		keys[i] = row.Key
	}
	return keys
}

// jiraSprintCmd builds `jira sprint {list,get,current,issues,add,remove}`.
func jiraSprintCmd() *cobra.Command {
	c := &cobra.Command{Use: "sprint", Short: "Agile sprints (Jira Software): list/get/current/issues/add/remove"}

	var boardID, limit int
	var state, cursor string
	list := &cobra.Command{
		Use:   "list",
		Short: "List a board's sprints (--board ID, optional --state)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if boardID <= 0 {
				return usageErr("--board must be a positive board id")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			sprints, next, err := svc.Sprints(cmd.Context(), boardID, state, limit, cursor)
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"sprints": sprints, "next_cursor": next}, func() string {
				return sprintLines(sprints)
			}, func() []string {
				return sprintIDs(sprints)
			})
		},
	}
	list.Flags().IntVar(&boardID, "board", 0, "board id (required)")
	list.Flags().StringVar(&state, "state", "", "filter by state: active|closed|future")
	list.Flags().IntVar(&limit, "limit", 50, "max results (capped at 50 by the Agile API)")
	list.Flags().StringVar(&cursor, "cursor", "", "pagination cursor (startAt)")
	_ = list.RegisterFlagCompletionFunc("state", fixedComp("active", "closed", "future"))

	get := &cobra.Command{
		Use:   "get <SPRINT-ID>",
		Short: "Get a sprint by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoiArg("sprint id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			s, err := svc.Sprint(cmd.Context(), id)
			if err != nil {
				return err
			}
			return emitID(cmd, s,
				func() string { return fmt.Sprintf("%d\t%s\t%s", s.ID, s.State, s.Name) },
				func() []string { return []string{strconv.Itoa(s.ID)} })
		},
	}

	var curBoard int
	current := &cobra.Command{
		Use:   "current",
		Short: "Show the active sprint of a board (--board ID)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if curBoard <= 0 {
				return usageErr("--board must be a positive board id")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			s, err := svc.SprintCurrent(cmd.Context(), curBoard)
			if err != nil {
				return err
			}
			return emitID(cmd, s,
				func() string { return fmt.Sprintf("%d\t%s\t%s", s.ID, s.State, s.Name) },
				func() []string { return []string{strconv.Itoa(s.ID)} })
		},
	}
	current.Flags().IntVar(&curBoard, "board", 0, "board id (required)")

	var issuesColumns, issuesCursor string
	var issuesLimit int
	issues := &cobra.Command{
		Use:   "issues <SPRINT-ID>",
		Short: "List the issues in a sprint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoiArg("sprint id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			list, err := svc.SprintIssueList(cmd.Context(), id, splitFields(issuesColumns), issuesLimit, issuesCursor)
			if err != nil {
				return err
			}
			return emitID(cmd, list, func() string { return app.IssueListMarkdown(list, false) }, func() []string { return app.IssueListKeys(list) })
		},
	}
	issues.Flags().StringVar(&issuesColumns, "columns", "", "ordered list columns (default: position,key,summary,status,assignee)")
	issues.Flags().IntVar(&issuesLimit, "limit", 50, "max results (capped at 50 by the Agile API)")
	issues.Flags().StringVar(&issuesCursor, "cursor", "", "pagination cursor (startAt)")

	add := &cobra.Command{
		Use:   "add <SPRINT-ID> <KEY> [KEY...]",
		Short: "Move issues into a sprint",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoiArg("sprint id", args[0])
			if err != nil {
				return err
			}
			keys := args[1:]
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.AddToSprint(cmd.Context(), id, keys); err != nil {
				return err
			}
			return emit(cmd, map[string]any{"sprint": id, "issues": keys, "status": "added"}, nil)
		},
	}

	remove := &cobra.Command{
		Use:   "remove <KEY> [KEY...]",
		Short: "Move issues out of any sprint, back to the backlog",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := jiraService()
			if err != nil {
				return err
			}
			if err := svc.RemoveFromSprint(cmd.Context(), args); err != nil {
				return err
			}
			return emit(cmd, map[string]any{"issues": args, "status": "removed"}, nil)
		},
	}

	c.AddCommand(list, get, current, issues, add, remove)
	return c
}

func sprintLines(sprints []domain.Sprint) string {
	var b strings.Builder
	for _, s := range sprints {
		fmt.Fprintf(&b, "%d\t%s\t%s\n", s.ID, s.State, s.Name)
	}
	return strings.TrimRight(b.String(), "\n")
}

func sprintIDs(sprints []domain.Sprint) []string {
	ids := make([]string, len(sprints))
	for i, s := range sprints {
		ids[i] = strconv.Itoa(s.ID)
	}
	return ids
}
