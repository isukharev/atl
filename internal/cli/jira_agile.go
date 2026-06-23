package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/domain"
)

// atoiArg parses a positional integer argument (a board or sprint id), mapping a
// non-numeric value to a usage error (exit 2) before any network call.
func atoiArg(name, s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, usageErr("%s must be a number, got %q", name, s)
	}
	return n, nil
}

// jiraBoardCmd builds `jira board {list,get}` over the Agile API (Jira Software).
func jiraBoardCmd() *cobra.Command {
	c := &cobra.Command{Use: "board", Short: "Agile boards (Jira Software): list/get"}

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
	list.Flags().IntVar(&limit, "limit", 50, "max results")
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

	c.AddCommand(list, get)
	return c
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
	list.Flags().IntVar(&limit, "limit", 50, "max results")
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

	var issuesFields, issuesCursor string
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
			list, next, err := svc.SprintIssues(cmd.Context(), id, splitFields(issuesFields), issuesLimit, issuesCursor)
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"sprint": id, "issues": list, "next_cursor": next}, func() string {
				var b strings.Builder
				for _, is := range list {
					fmt.Fprintf(&b, "%s\t[%s]\t%s\n", is.Key, is.Status, is.Summary)
				}
				return strings.TrimRight(b.String(), "\n")
			}, func() []string {
				keys := make([]string, len(list))
				for i, is := range list {
					keys[i] = is.Key
				}
				return keys
			})
		},
	}
	issues.Flags().StringVar(&issuesFields, "fields", "", "comma-separated field list")
	issues.Flags().IntVar(&issuesLimit, "limit", 50, "max results")
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
