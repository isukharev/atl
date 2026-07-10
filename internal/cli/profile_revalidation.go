package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
	profilepkg "github.com/isukharev/atl/internal/profile"
)

func newProfileRevalidateCmd() *cobra.Command {
	var fromFile, out string
	cmd := &cobra.Command{
		Use:   "revalidate",
		Short: "Record explicit schema checks and emit verified facts as observations",
		Long: "Consume caller-approved backend check results. Missing/failed checks are remembered\n" +
			"outside profile.json; only verified facts enter the private observations artifact.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if samePath(fromFile, out) {
				return usageErr("--out must differ from --from-file")
			}
			data, err := readProfileJSON(fromFile)
			if err != nil {
				return err
			}
			batch, err := profilepkg.DecodeRevalidationBatchStrict(data)
			if err != nil {
				return err
			}
			result, err := profilepkg.ApplyRevalidation(config.Dir(), out, batch)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf("observations: %s\nhash: %s\nchecks: %d", result.Path, result.ObservationsHash, len(result.Entries))
			})
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "", "revalidation results JSON file, or - for stdin (required)")
	cmd.Flags().StringVar(&out, "out", "", "private *.atl-observations.json path (required; parent mode 0700)")
	_ = cmd.MarkFlagRequired("from-file")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func samePath(a, b string) bool {
	if a == "-" || b == "-" {
		return false
	}
	aAbs, aErr := filepath.Abs(a)
	bAbs, bErr := filepath.Abs(b)
	if aErr != nil || bErr != nil {
		return false
	}
	if aAbs == bAbs {
		return true
	}
	aInfo, aStatErr := os.Stat(aAbs)
	bInfo, bStatErr := os.Stat(bAbs)
	return aStatErr == nil && bStatErr == nil && os.SameFile(aInfo, bInfo)
}

func newProfileRevalidationCmd() *cobra.Command {
	group := &cobra.Command{Use: "revalidation", Short: "Inspect fresh, stale, pending, missing, and failed schema knowledge"}
	var staleBeforeRaw, service string
	status := &cobra.Command{
		Use:   "status",
		Short: "Classify schema facts at an explicit deterministic cutoff",
		RunE: func(cmd *cobra.Command, _ []string) error {
			staleBefore, err := time.Parse(time.RFC3339, staleBeforeRaw)
			if err != nil {
				return usageErr("--stale-before must be RFC3339: %v", err)
			}
			result, err := profilepkg.RevalidationStatusFor(config.Dir(), staleBefore, service)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return revalidationStatusText(result)
			})
		},
	}
	status.Flags().StringVar(&staleBeforeRaw, "stale-before", "", "facts verified before this RFC3339 instant are stale (required)")
	status.Flags().StringVar(&service, "service", "", "narrow to jira|confluence")
	_ = status.MarkFlagRequired("stale-before")
	_ = status.RegisterFlagCompletionFunc("service", fixedComp("jira", "confluence"))
	group.AddCommand(status)
	return group
}

func revalidationStatusText(result profilepkg.RevalidationStatus) string {
	out := fmt.Sprintf("profile_hash: %s\nstale_before: %s", result.ProfileHash, result.StaleBefore.Format(time.RFC3339))
	for _, entry := range result.Entries {
		out += fmt.Sprintf("\n%s %s: %s", entry.Service, entry.ID, entry.Status)
	}
	return out
}
