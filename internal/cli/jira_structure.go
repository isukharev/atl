package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

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
	c := &cobra.Command{Use: "structure", Short: "Read Tempo Structure metadata, forests, rows, and values"}

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
			rowList, version, err := svc.StructureRows(cmd.Context(), id)
			if err != nil {
				return err
			}
			out := map[string]any{"structure_id": id, "version": version, "rows": rowList}
			return emitID(cmd, out, func() string { return structureRowLines(rowList) }, func() []string {
				ids := make([]string, len(rowList))
				for i, r := range rowList {
					ids[i] = strconv.FormatInt(r.RowID, 10)
				}
				return ids
			})
		},
	}

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

	c.AddCommand(get, forest, rows, values)
	return c
}

func structureRowLines(rows []domain.StructureRow) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%d\tdepth=%d\tparent=%d\t%s\t%s\n", r.RowID, r.Depth, r.ParentRowID, r.ItemType, r.ItemID)
	}
	return strings.TrimRight(b.String(), "\n")
}
