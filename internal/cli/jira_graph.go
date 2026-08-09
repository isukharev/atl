package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func jiraIssueGraphCmd() *cobra.Command {
	var (
		depth              int
		resolve            string
		maxNodes           int
		maxEdges           int
		maxEvidence        int
		maxRequests        int
		maxBytes           int
		strict             bool
		includeDevelopment bool
		projection         string
		selectors          []string
	)
	graphOptions := func(cmd *cobra.Command) (app.JiraIssueGraphOptions, app.JiraIssueGraphProjectionOptions, error) {
		opts := app.JiraIssueGraphOptions{
			Depth: depth, MaxNodes: maxNodes, MaxEdges: maxEdges,
			MaxEvidence: maxEvidence, MaxRequests: maxRequests,
			MaxResponseBytes: maxBytes, ResolveConfluence: resolve == "confluence",
			IncludeDevelopment: includeDevelopment,
		}
		if resolve != "none" && resolve != "confluence" {
			return opts, app.JiraIssueGraphProjectionOptions{}, usageErr("--resolve must be none or confluence")
		}
		for _, limit := range []struct {
			flag  string
			value int
		}{
			{"max-nodes", maxNodes}, {"max-edges", maxEdges}, {"max-evidence", maxEvidence},
			{"max-requests", maxRequests}, {"max-bytes", maxBytes},
		} {
			if cmd.Flags().Changed(limit.flag) && limit.value <= 0 {
				return opts, app.JiraIssueGraphProjectionOptions{}, usageErr("--%s must be greater than zero", limit.flag)
			}
		}
		if _, err := app.NormalizeJiraIssueGraphOptions(opts); err != nil {
			return opts, app.JiraIssueGraphProjectionOptions{}, err
		}
		projectionOpts, err := app.NormalizeJiraIssueGraphProjection(projection, selectors, includeDevelopment)
		if err != nil {
			return opts, projectionOpts, err
		}
		if projectionOpts.Projection == "compact" {
			if output := cmd.Flag("output"); output != nil && output.Value.String() != "json" {
				return opts, projectionOpts, usageErr("--projection compact requires --output json")
			}
		}
		return opts, projectionOpts, nil
	}
	cmd := &cobra.Command{
		Use:   "graph <KEY>",
		Short: "Build a bounded qualified work-artifact graph for one issue",
		Long: "Build a deterministic read-only work-artifact graph from one Jira issue and its requested evidence sources. " +
			"The bounded schema-v2 contract applies at every depth; custom budgets, optional Confluence metadata resolution, and experimental Development identities remain explicit.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			_, _, err := graphOptions(cmd)
			return err
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, projectionOpts, err := graphOptions(cmd)
			if err != nil {
				return err
			}
			service, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := service.IssueGraphWithOptions(cmd.Context(), args[0], opts)
			if err != nil {
				return err
			}
			output := any(result)
			text := func() string { return app.JiraIssueGraphMarkdown(result) }
			if projectionOpts.Projection == "compact" {
				output, err = app.ProjectJiraIssueGraphCompact(result, projectionOpts)
				if err != nil {
					return err
				}
				text = nil
			}
			if err := emit(cmd, output, text); err != nil {
				return err
			}
			if strict && !result.Complete {
				return fmt.Errorf("%w: Jira graph contains incomplete requested sources", domain.ErrCheckFailed)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&depth, "depth", 0, fmt.Sprintf("follow exact structured Jira relations up to depth 0..%d", app.JiraIssueGraphMaxDepth))
	cmd.Flags().StringVar(&resolve, "resolve", "none", "metadata resolution: none|confluence")
	cmd.Flags().IntVar(&maxNodes, "max-nodes", 0, fmt.Sprintf("node limit (default %d, max %d)", app.JiraIssueGraphDefaultMaxNodes, app.JiraIssueGraphMaxNodes))
	cmd.Flags().IntVar(&maxEdges, "max-edges", 0, fmt.Sprintf("edge limit (default %d, max %d)", app.JiraIssueGraphDefaultMaxEdges, app.JiraIssueGraphMaxEdges))
	cmd.Flags().IntVar(&maxEvidence, "max-evidence", 0, fmt.Sprintf("evidence limit (default %d, max %d)", app.JiraIssueGraphDefaultMaxEvidence, app.JiraIssueGraphMaxEvidence))
	cmd.Flags().IntVar(&maxRequests, "max-requests", 0, fmt.Sprintf("physical HTTP attempt limit (default %d, max %d)", app.JiraIssueGraphDefaultMaxRequests, app.JiraIssueGraphMaxRequests))
	cmd.Flags().IntVar(&maxBytes, "max-bytes", 0, fmt.Sprintf("buffered response byte limit (default %d, max %d)", app.JiraIssueGraphDefaultResponseBytes, app.JiraIssueGraphMaxResponseBytes))
	cmd.Flags().BoolVar(&strict, "strict", false, "emit the graph, then fail when any requested source is incomplete")
	cmd.Flags().BoolVar(&includeDevelopment, "include-development", false, "include experimental Jira Development project/commit/branch/MR identities")
	cmd.Flags().StringVar(&projection, "projection", "full", "JSON projection: full|compact")
	cmd.Flags().StringSliceVar(&selectors, "select", nil, "compact facts: urls,scm,none (repeat or comma-separated)")
	_ = cmd.RegisterFlagCompletionFunc("resolve", fixedComp("none", "confluence"))
	_ = cmd.RegisterFlagCompletionFunc("projection", fixedComp("full", "compact"))
	_ = cmd.RegisterFlagCompletionFunc("select", fixedComp("urls", "scm", "none"))
	return cmd
}
