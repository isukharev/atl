package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/version"
)

func newCorpusCmd() *cobra.Command {
	group := &cobra.Command{
		Use: "corpus", Short: "Build sealed, zero-egress local corpus generations",
	}
	var jiraRoot, confluenceRoot, storeRoot string
	var initializeStore, allowUnreconciled bool
	export := &cobra.Command{
		Use:   "export",
		Short: "Project pristine local mirrors into a sealed indexer-v1 generation",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return usageErr("corpus export accepts no positional arguments")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			build := version.Current()
			state := corpus.BuildStateUnknown
			switch build.BuildState {
			case "clean":
				state = corpus.BuildStateClean
			case "dirty":
				state = corpus.BuildStateModified
			}
			result, err := app.ExportCorpus(cmd.Context(), app.CorpusExportOptions{
				JiraRoot: jiraRoot, ConfluenceRoot: confluenceRoot,
				StoreRoot: storeRoot, InitializeStore: initializeStore,
				AllowUnreconciled: allowUnreconciled,
				GeneratorVersion:  build.Version, BuildState: state,
			})
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf("generation=%s readiness=%s documents=%d edges=%d markdown=%d reused=%t",
					result.Generation.GenerationDigest, result.Projection.Readiness,
					result.Projection.Counts.Documents, result.Projection.Counts.Edges,
					result.Projection.Counts.MarkdownFiles, result.Reused)
			})
		},
	}
	export.Flags().StringVar(&jiraRoot, "jira", "", "initialized Jira mirror root")
	export.Flags().StringVar(&confluenceRoot, "confluence", "", "initialized Confluence mirror root")
	export.Flags().StringVar(&storeRoot, "store", "", "existing owner-only sealed-generation store root")
	export.Flags().BoolVar(&initializeStore, "initialize-store", false, "initialize an existing empty 0700 store root")
	export.Flags().BoolVar(&allowUnreconciled, "allow-unreconciled", false, "diagnostic export of pristine bases despite staged lineage (always non-ready)")
	group.AddCommand(export)
	return group
}
