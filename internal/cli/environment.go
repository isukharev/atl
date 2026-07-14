package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/version"
)

func newEnvironmentCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "environment",
		Short: "Inspect explicit runtime and backend semantics",
	}
	inspect := &cobra.Command{
		Use:   "inspect",
		Short: "Report query, server, user, display, and incremental time semantics",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			local, _ := loadLocalFromCwd(cmd.ErrOrStderr())
			result := app.NewEnvironment(cfg, version.Version).InspectEnvironment(cmd.Context(), local)
			return emit(cmd, result, func() string { return environmentInspectText(result) })
		},
	}
	c.AddCommand(inspect)
	return c
}

func environmentInspectText(result *app.EnvironmentInspectResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "complete: %t\n", result.Complete)
	fmt.Fprintf(&b, "display_time_zone: %s\n", environmentFactText(result.DisplayTimeZone))
	fmt.Fprintf(&b, "jira: configured=%t status=%s\n", result.Jira.Configured, result.Jira.Status)
	fmt.Fprintf(&b, "jira_server_utc_offset: %s\n", environmentFactText(result.Jira.ServerUTCOffset))
	fmt.Fprintf(&b, "jira_user_time_zone: %s\n", environmentFactText(result.Jira.UserTimeZone))
	fmt.Fprintf(&b, "jira_jql_time_zone: %s\n", environmentFactText(result.Jira.JQLTimeZone))
	fmt.Fprintf(&b, "confluence: configured=%t status=%s\n", result.Confluence.Configured, result.Confluence.Status)
	fmt.Fprintf(&b, "confluence_user_time_zone: %s\n", environmentFactText(result.Confluence.UserTimeZone))
	fmt.Fprintf(&b, "confluence_cql_time_zone: %s\n", environmentFactText(result.Confluence.CQLTimeZone))
	fmt.Fprintf(&b, "incremental_query_literal_time_zone: %s\n", environmentFactText(result.Incremental.QueryLiteralTimeZone))
	fmt.Fprintf(&b, "incremental_backend_query_time_zone: %s\n", environmentFactText(result.Incremental.BackendQueryTimeZone))
	fmt.Fprintf(&b, "incremental_safety_overlap_hours: %d\n", result.Incremental.SafetyOverlapHours)
	fmt.Fprintf(&b, "incremental_exact_timestamp_filter: %t\n", result.Incremental.ExactTimestampFilter)
	fmt.Fprintf(&b, "hidden_calibration_requests: %t", result.Incremental.HiddenCalibrationRequests)
	return b.String()
}

func environmentFactText(fact app.EnvironmentTimeFact) string {
	value := fact.Value
	if value == "" {
		value = "unknown"
	}
	out := fmt.Sprintf("%s (evidence=%s source=%s", value, fact.Evidence, fact.Source)
	if fact.Reason != "" {
		out += " reason=" + fact.Reason
	}
	return out + ")"
}
