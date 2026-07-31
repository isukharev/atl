package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func jiraIssueGraphCmd() *cobra.Command {
	var (
		depth       int
		resolve     string
		maxNodes    int
		maxEdges    int
		maxEvidence int
		maxRequests int
		maxBytes    int
		strict      bool
	)
	graphOptions := func(cmd *cobra.Command) (app.JiraIssueGraphOptions, error) {
		opts := app.JiraIssueGraphOptions{
			Depth: depth, MaxNodes: maxNodes, MaxEdges: maxEdges,
			MaxEvidence: maxEvidence, MaxRequests: maxRequests,
			MaxResponseBytes: maxBytes, ResolveConfluence: resolve == "confluence",
		}
		if resolve != "none" && resolve != "confluence" {
			return opts, usageErr("--resolve must be none or confluence")
		}
		for _, limit := range []struct {
			flag  string
			value int
		}{
			{"max-nodes", maxNodes}, {"max-edges", maxEdges}, {"max-evidence", maxEvidence},
			{"max-requests", maxRequests}, {"max-bytes", maxBytes},
		} {
			if cmd.Flags().Changed(limit.flag) && limit.value <= 0 {
				return opts, usageErr("--%s must be greater than zero", limit.flag)
			}
		}
		if _, err := app.NormalizeJiraIssueGraphOptions(opts); err != nil {
			return opts, err
		}
		return opts, nil
	}
	cmd := &cobra.Command{
		Use:   "graph <KEY>",
		Short: "Build a bounded qualified work-artifact graph for one issue",
		Long: "Build a deterministic read-only work-artifact graph from one Jira issue and its requested stable evidence sources. " +
			"The bounded schema-v2 contract applies at every depth; custom budgets and optional Confluence metadata resolution remain explicit.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			_, err := graphOptions(cmd)
			return err
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := graphOptions(cmd)
			if err != nil {
				return err
			}
			service, err := jiraService()
			if err != nil {
				return err
			}
			result, err := service.IssueGraphWithOptions(cmd.Context(), args[0], opts)
			if err != nil {
				return err
			}
			if err := emit(cmd, result, func() string { return app.JiraIssueGraphMarkdown(result) }); err != nil {
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
	_ = cmd.RegisterFlagCompletionFunc("resolve", fixedComp("none", "confluence"))
	return cmd
}
