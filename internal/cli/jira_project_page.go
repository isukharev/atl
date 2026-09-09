package cli

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

const (
	jiraProjectPageTextCellMaxBytes = 512
	jiraProjectPageTextMaxBytes     = 64 << 10
)

func jiraProjectPageCmd() *cobra.Command {
	var project, fields, cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "project-page",
		Short: "Read one bounded Broker-qualified Jira project issue page",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("cursor") && cursor == "" {
				return usageErr("--cursor must be a canonical decimal coordinate")
			}
			arguments, err := app.NewJiraProjectIssuePageArguments(project, splitFields(fields), limit, cursor)
			if err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.ProjectIssuePage(cmd.Context(), arguments)
			if err != nil {
				return err
			}
			return emitID(cmd, result, func() string { return jiraProjectIssuePageText(result) }, func() []string {
				keys := make([]string, len(result.Issues))
				for index, issue := range result.Issues {
					keys[index] = issue.Key
				}
				return keys
			})
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "canonical Jira project key (required)")
	cmd.Flags().StringVar(&fields, "fields", "summary", "comma-separated fields: summary,description")
	cmd.Flags().IntVar(&limit, "limit", 15, "page size (1..15)")
	cmd.Flags().StringVar(&cursor, "cursor", "0", "canonical decimal coordinate (0..1000000)")
	return cmd
}

func jiraProjectIssuePageText(result *app.JiraProjectIssuePageResult) string {
	if result == nil {
		return ""
	}
	var builder strings.Builder
	builder.Grow(min(jiraProjectPageTextMaxBytes, 512+len(result.Issues)*256))
	builder.WriteString("KEY\tID\tUPDATED\tSUMMARY\tDESCRIPTION\n")
	for _, issue := range result.Issues {
		summary, description := "<not-requested>", "<not-requested>"
		for _, field := range issue.Fields {
			value := boundedJiraProjectPageTextCell(field.Value)
			switch {
			case !field.Present:
				value = "<absent>"
			case field.Null:
				value = "<null>"
			}
			switch field.Field {
			case "summary":
				summary = value
			case "description":
				description = value
			}
		}
		fmt.Fprintf(&builder, "%s\t%s\t%s\t%s\t%s\n",
			boundedJiraProjectPageTextCell(issue.Key), boundedJiraProjectPageTextCell(issue.ID),
			boundedJiraProjectPageTextCell(issue.Updated), summary, description)
	}
	fmt.Fprintf(&builder, "page\tstart_at=%d\tmax_results=%d\ttotal=%d\tcount=%d\tnext_cursor=%s\tnext_cursor_present=%t\tcoordinate_exhausted=%t\tselection_complete=%t\tpartial_reason=%s\tcomplete=%t",
		result.Page.StartAt, result.Page.MaxResults, result.Page.Total, result.Page.Count,
		boundedJiraProjectPageTextCell(result.Page.NextCursor), result.Page.NextCursorPresent,
		result.Page.CoordinateExhausted, result.Page.SelectionComplete,
		boundedJiraProjectPageTextCell(result.Page.PartialReason), result.Complete)
	return builder.String()
}

func boundedJiraProjectPageTextCell(value string) string {
	var builder strings.Builder
	builder.Grow(min(len(value), jiraProjectPageTextCellMaxBytes))
	pendingSpace := false
	truncated := false
	for _, current := range value {
		if unicode.IsSpace(current) {
			pendingSpace = builder.Len() > 0
			continue
		}
		width := utf8.RuneLen(current)
		if width < 0 {
			width = utf8.RuneLen(utf8.RuneError)
			current = utf8.RuneError
		}
		space := 0
		if pendingSpace {
			space = 1
		}
		if builder.Len()+space+width > jiraProjectPageTextCellMaxBytes-3 {
			truncated = true
			break
		}
		if pendingSpace {
			builder.WriteByte(' ')
			pendingSpace = false
		}
		builder.WriteRune(current)
	}
	if truncated {
		builder.WriteString("...")
	}
	return builder.String()
}
