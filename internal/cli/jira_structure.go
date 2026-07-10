package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func atoi64Arg(name, s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, usageErr("%s must be a positive number, got %q", name, s)
	}
	return n, nil
}

func parseInt64List(name, s string) ([]int64, error) {
	if strings.TrimSpace(s) == "" {
		return nil, usageErr("%s is required", name)
	}
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil || n <= 0 {
			return nil, usageErr("%s must contain positive row ids, got %q", name, part)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, usageErr("%s is required", name)
	}
	return out, nil
}

// jiraStructureCmd builds read-only Tempo Structure commands.
func jiraStructureCmd() *cobra.Command {
	c := &cobra.Command{Use: "structure", Short: "Read Tempo Structure metadata, forests, rows, values, and issue snapshots"}

	get := &cobra.Command{
		Use:   "get <STRUCTURE-ID>",
		Short: "Get Structure metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoi64Arg("structure id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			st, err := svc.Structure(cmd.Context(), id)
			if err != nil {
				return err
			}
			return emitID(cmd, st,
				func() string { return fmt.Sprintf("%d\t%s", st.ID, st.Name) },
				func() []string { return []string{strconv.FormatInt(st.ID, 10)} })
		},
	}

	forest := &cobra.Command{
		Use:   "forest <STRUCTURE-ID>",
		Short: "Get the latest raw Structure forest formula",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoi64Arg("structure id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			f, err := svc.StructureForest(cmd.Context(), id)
			if err != nil {
				return err
			}
			return emit(cmd, f, func() string {
				return fmt.Sprintf("version=%d signature=%d formula_len=%d",
					f.Version.Version, f.Version.Signature, len(f.Formula))
			})
		},
	}

	var rowsRoot, rowsRootFields string
	rows := &cobra.Command{
		Use:   "rows <STRUCTURE-ID>",
		Short: "Parse the latest Structure forest into rows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoi64Arg("structure id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.StructureRowsWithOptions(cmd.Context(), id, app.StructureRowsOpts{
				Root:       rowsRoot,
				RootFields: splitFields(rowsRootFields),
			})
			if err != nil {
				return err
			}
			return emitID(cmd, res, func() string { return structureRowLines(res.Rows) }, func() []string {
				ids := make([]string, len(res.Rows))
				for i, r := range res.Rows {
					ids[i] = strconv.FormatInt(r.RowID, 10)
				}
				return ids
			})
		},
	}
	rows.Flags().StringVar(&rowsRoot, "root", "", "optional root row/id/text; emits the first matching row subtree")
	rows.Flags().StringVar(&rowsRootFields, "root-fields", "key,summary", "comma-separated Structure attributes used when matching --root")

	var valueRows, valueFields string
	values := &cobra.Command{
		Use:   "values <STRUCTURE-ID>",
		Short: "Get Structure attribute values for selected rows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoi64Arg("structure id", args[0])
			if err != nil {
				return err
			}
			rows, err := parseInt64List("--rows", valueRows)
			if err != nil {
				return err
			}
			fields := splitFields(valueFields)
			if len(fields) == 0 {
				return usageErr("--fields is required")
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			vals, err := svc.StructureValues(cmd.Context(), id, rows, fields)
			if err != nil {
				return err
			}
			return emit(cmd, vals, nil)
		},
	}
	values.Flags().StringVar(&valueRows, "rows", "", "comma-separated Structure row ids")
	values.Flags().StringVar(&valueFields, "fields", "", "comma-separated Structure attribute ids (for example key,summary,status)")

	var pullRoot, pullRootFields, pullFields, pullOut string
	var pullBatchSize, pullLimit int
	pullIssues := &cobra.Command{
		Use:   "pull-issues <STRUCTURE-ID>",
		Short: "Fetch Jira issue snapshots referenced by Structure rows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoi64Arg("structure id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.StructurePullIssues(cmd.Context(), id, app.StructureIssuePullOpts{
				Root:       pullRoot,
				RootFields: splitFields(pullRootFields),
				Fields:     splitFields(pullFields),
				BatchSize:  pullBatchSize,
				Limit:      pullLimit,
				Out:        pullOut,
			})
			if err != nil {
				return err
			}
			return emitID(cmd, res, func() string {
				return fmt.Sprintf("rows=%d issue_ids=%d issues=%d", len(res.Rows), len(res.IssueIDs), len(res.Issues))
			}, func() []string { return res.IssueIDs })
		},
	}
	pullIssues.Flags().StringVar(&pullRoot, "root", "", "optional root row/id/text; fetches issues from the first matching subtree")
	pullIssues.Flags().StringVar(&pullRootFields, "root-fields", "key,summary", "comma-separated Structure attributes used when matching --root")
	pullIssues.Flags().StringVar(&pullFields, "fields", "", "comma-separated Jira fields to include")
	pullIssues.Flags().IntVar(&pullBatchSize, "batch-size", 100, "issue id batch size for generated JQL")
	pullIssues.Flags().IntVar(&pullLimit, "limit", 0, "maximum Jira issues to fetch (0 means no limit)")
	pullIssues.Flags().StringVar(&pullOut, "out", "", "optional JSON file path for the pulled snapshot")

	var exportRoot, exportRootFields, exportFields, exportFormat, exportOut string
	var exportBatchSize, exportLimit int
	var exportRawCSV bool
	exportCmd := &cobra.Command{
		Use:   "export <STRUCTURE-ID>",
		Short: "Write an offline Structure tree export with Jira issue fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := atoi64Arg("structure id", args[0])
			if err != nil {
				return err
			}
			svc, err := jiraService()
			if err != nil {
				return err
			}
			res, err := svc.StructureExport(cmd.Context(), id, app.StructureExportOpts{
				Root:       exportRoot,
				RootFields: splitFields(exportRootFields),
				Fields:     splitFields(exportFields),
				BatchSize:  exportBatchSize,
				Limit:      exportLimit,
				Format:     exportFormat,
				Out:        exportOut,
				RawCSV:     exportRawCSV,
			})
			if err != nil {
				return err
			}
			return emit(cmd, res, func() string {
				return fmt.Sprintf("%s\tformat=%s\trows=%d\tissues=%d", res.Path, res.Format, res.RowCount, res.IssueCount)
			})
		},
	}
	exportCmd.Flags().StringVar(&exportRoot, "root", "", "optional root row/id/text; exports the first matching subtree")
	exportCmd.Flags().StringVar(&exportRootFields, "root-fields", "key,summary", "comma-separated Structure attributes used when matching --root")
	exportCmd.Flags().StringVar(&exportFields, "fields", "", "comma-separated Jira fields to include")
	exportCmd.Flags().IntVar(&exportBatchSize, "batch-size", 100, "issue id batch size for generated JQL")
	exportCmd.Flags().IntVar(&exportLimit, "limit", 0, "maximum Jira issues to fetch (0 means no limit)")
	exportCmd.Flags().StringVar(&exportFormat, "format", "json", "export format: json, csv, or md")
	exportCmd.Flags().StringVar(&exportOut, "out", "", "required output file path")
	exportCmd.Flags().BoolVar(&exportRawCSV, "raw-csv", false, "write formula-leading CSV cells verbatim (unsafe in spreadsheets)")

	c.AddCommand(get, forest, rows, values, pullIssues, exportCmd)
	return c
}

func structureRowLines(rows []domain.StructureRow) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%d\tdepth=%d\tparent=%d\t%s\t%s\n", r.RowID, r.Depth, r.ParentRowID, r.ItemType, r.ItemID)
	}
	return strings.TrimRight(b.String(), "\n")
}
