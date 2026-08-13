package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/compose"
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
		Short: "Project pristine local mirrors into a sealed indexer-v2 generation",
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

	var diffStoreRoot, identityArtifact string
	diff := &cobra.Command{
		Use:   "diff",
		Short: "Verify the current qualified generation membership delta",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return usageErr("corpus diff accepts no positional arguments")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := app.DiffCorpusGeneration(cmd.Context(), app.CorpusGenerationDiffOptions{
				StoreRoot: diffStoreRoot, IdentityArtifact: identityArtifact,
			})
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				summary := fmt.Sprintf(
					"qualification=%s added=%d retained=%d changed=%d tombstoned=%d predecessor=%s successor=%s tombstone=%s identity_artifact_written=%t",
					result.Qualification, result.Counts.Added, result.Counts.Retained, result.Counts.Changed,
					result.Counts.Tombstoned, result.PredecessorGenerationDigest,
					result.SuccessorGenerationDigest, result.TombstoneDigest, result.IdentityArtifactWritten,
				)
				if result.Reason != "" {
					summary += " reason=" + string(result.Reason)
				}
				return summary
			})
		},
	}
	diff.Flags().StringVar(&diffStoreRoot, "store", "", "existing owner-only sealed-generation store root")
	diff.Flags().StringVar(&identityArtifact, "identity-artifact", "", "exclusive identity-bearing artifact path under an existing 0700 parent")

	var handoffStoreRoot, handoffArtifact string
	handoff := &cobra.Command{
		Use:   "handoff",
		Short: "Verify a qualified sealed document inventory for an indexer",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return usageErr("corpus handoff accepts no positional arguments")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := app.PrepareCorpusHandoff(cmd.Context(), app.CorpusHandoffOptions{
				StoreRoot: handoffStoreRoot, HandoffArtifact: handoffArtifact,
			})
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf(
					"qualification=%s generation=%s projection_schema=%d members=%d bytes=%d handoff_artifact_written=%t",
					result.Qualification, result.Generation.GenerationDigest, result.Generation.ProjectionSchema,
					result.Generation.Totals.Members, result.Generation.Totals.Bytes, result.HandoffArtifactWritten,
				)
			})
		},
	}
	handoff.Flags().StringVar(&handoffStoreRoot, "store", "", "existing owner-only sealed-generation store root")
	handoff.Flags().StringVar(&handoffArtifact, "handoff-artifact", "", "exclusive private document-route artifact path under an existing 0700 parent outside the store")

	cache := &cobra.Command{Use: "cache", Short: "Inspect and retain owner-private corpus cache generations"}
	var cacheStatusStore string
	cacheStatus := &cobra.Command{
		Use: "status", Aliases: []string{"doctor"}, Short: "Verify a cache inventory without backend access",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return usageErr("corpus cache status accepts no positional arguments")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := app.StatusCorpusCache(cmd.Context(), app.CorpusCacheStatusOptions{StoreRoot: cacheStatusStore})
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf("initialized=%t current=%t binding=%s sealed=%d candidates=%d unsealed=%d",
					result.Initialized, result.Current, result.Binding, result.Retention.SealedGenerations,
					result.Retention.CandidateGenerations, result.Retention.UnsealedStages)
			})
		},
	}
	cacheStatus.Flags().StringVar(&cacheStatusStore, "store", "", "existing owner-only sealed-generation cache root")

	retention := &cobra.Command{Use: "retention", Short: "Preview or apply finite cache generation retention"}
	var previewStore, previewArtifact string
	var retainPredecessors int
	preview := &cobra.Command{
		Use: "preview", Short: "Write an exclusive private hash-bound retention plan",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return usageErr("corpus cache retention preview accepts no positional arguments")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := app.PreviewCorpusCacheRetention(cmd.Context(), app.CorpusCacheRetentionPreviewOptions{
				StoreRoot: previewStore, RetainPredecessors: retainPredecessors, PlanArtifact: previewArtifact,
			})
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf("plan=%s protected=%d candidates=%d unsealed=%d plan_artifact_written=%t",
					result.PlanDigest, result.Status.ProtectedGenerations, result.Status.CandidateGenerations,
					result.Status.UnsealedStages, result.PlanArtifactWritten)
			})
		},
	}
	preview.Flags().StringVar(&previewStore, "store", "", "existing owner-only sealed-generation cache root")
	preview.Flags().IntVar(&retainPredecessors, "retain-predecessors", 1, "finite protected predecessor depth including the current delta dependency")
	preview.Flags().StringVar(&previewArtifact, "plan-artifact", "", "exclusive private retention plan path under an existing 0700 parent outside the store")

	var applyStore, applyArtifact, expectedPlanDigest string
	var applyConfirmed bool
	applyRetention := &cobra.Command{
		Use: "apply", Short: "Apply one exact reviewed retention plan",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return usageErr("corpus cache retention apply accepts no positional arguments")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := app.ApplyCorpusCacheRetention(cmd.Context(), app.CorpusCacheRetentionApplyOptions{
				StoreRoot: applyStore, PlanArtifact: applyArtifact, ExpectedPlanDigest: expectedPlanDigest, Apply: applyConfirmed,
			})
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf("removed=%d remaining=%d complete=%t",
					result.Status.RemovedThisApply, result.Status.RemainingCandidates, result.Status.Complete)
			})
		},
	}
	applyRetention.Flags().StringVar(&applyStore, "store", "", "existing owner-only sealed-generation cache root")
	applyRetention.Flags().StringVar(&applyArtifact, "plan-artifact", "", "reviewed private retention plan path outside the store")
	applyRetention.Flags().StringVar(&expectedPlanDigest, "expected-plan-digest", "", "exact lowercase digest emitted by retention preview")
	applyRetention.Flags().BoolVar(&applyConfirmed, "apply", false, "delete only the reviewed inactive candidate generations")
	retention.AddCommand(preview, applyRetention)
	cache.AddCommand(cacheStatus, retention)

	var buildOptions app.CorpusBuildOptions
	build := &cobra.Command{
		Use:   "build",
		Short: "Capture qualified remote selections into one sealed generation",
		Annotations: map[string]string{
			explicitReadOnlyAnnotation:       "required",
			corpusBuildClosedErrorAnnotation: "required",
		},
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return app.CorpusBuildFailure(app.CorpusBuildPhaseValidate, usageErr("corpus build accepts no positional arguments"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := app.ValidateCorpusBuildOptions(buildOptions); err != nil {
				return app.CorpusBuildFailure(app.CorpusBuildPhaseValidate, err)
			}
			cfg, err := loadConfig()
			if err != nil {
				return app.CorpusBuildFailure(app.CorpusBuildPhaseValidate, err)
			}
			build := version.Current()
			state := corpus.BuildStateUnknown
			switch build.BuildState {
			case "clean":
				state = corpus.BuildStateClean
			case "dirty":
				state = corpus.BuildStateModified
			}
			service, err := compose.NewCorpusBuild(cfg, compose.CorpusBuildSelection{
				Jira: buildOptions.JiraProject != "", Confluence: buildOptions.ConfluenceSpace != "",
				MaxInFlight: buildOptions.MaxInFlight, RequestsPerSecond: buildOptions.RequestsPerSecond,
				GeneratorVersion: build.Version, GeneratorCommit: build.Commit, BuildState: state,
				QualifiedCacheTrust: app.CorpusBuildCacheFastPathEligible(buildOptions),
			}, invocationCompositionOptions(cmd)...)
			if err != nil {
				return app.CorpusBuildFailure(app.CorpusBuildPhaseValidate, err)
			}
			result, err := service.Build(cmd.Context(), buildOptions)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				publication := "published"
				if result.Reused {
					publication = "reused"
				}
				return fmt.Sprintf("generation=%s readiness=%s services=%d documents=%d edges=%d source=%s publication=%s elapsed_ms=%d",
					result.Generation.GenerationDigest, result.Projection.Readiness, len(result.Services),
					result.Projection.Counts.Documents, result.Projection.Counts.Edges,
					result.Source, publication, result.ElapsedMS)
			})
		},
	}
	build.Flags().StringVar(&buildOptions.Root, "root", "", "existing owner-only corpus root")
	build.Flags().BoolVar(&buildOptions.Initialize, "initialize", false, "initialize an existing empty 0700 corpus root")
	build.Flags().BoolVar(&buildOptions.Restart, "restart", false, "recover and retain an interrupted attempt, then start a fresh capture")
	build.Flags().StringVar(&buildOptions.CacheRoot, "cache-root", "", "existing owner-only sealed-generation cache root")
	build.Flags().BoolVar(&buildOptions.InitializeCache, "initialize-cache", false, "initialize an existing empty 0700 cache root")
	build.Flags().IntVar(&buildOptions.CacheMaxRequests, "cache-max-requests", 0, "separate physical HTTP attempt cap for cache probes")
	build.Flags().Int64Var(&buildOptions.CacheMaxResponseBytes, "cache-max-response-bytes", 0, "separate buffered response-byte cap for cache probes")
	build.Flags().DurationVar(&buildOptions.CacheDeadline, "cache-deadline", 0, "per-phase cache probe duration budget")
	build.Flags().StringVar(&buildOptions.JiraProject, "jira-project", "", "canonical Jira project key")
	build.Flags().IntVar(&buildOptions.MaxJiraIssues, "max-jira-issues", 0, "exact positive Jira selection cap")
	build.Flags().StringVar(&buildOptions.ConfluenceSpace, "confluence-space", "", "canonical Confluence space key")
	build.Flags().IntVar(&buildOptions.MaxConfluencePages, "max-confluence-pages", 0, "exact positive Confluence selection cap")
	build.Flags().IntVar(&buildOptions.MaxRequests, "max-requests", 0, "aggregate physical HTTP attempt cap")
	build.Flags().Int64Var(&buildOptions.MaxResponseBytes, "max-response-bytes", 0, "aggregate buffered HTTP response-byte cap")
	build.Flags().IntVar(&buildOptions.MaxMembers, "max-members", 0, "sealed generation member cap")
	build.Flags().Int64Var(&buildOptions.MaxGenerationBytes, "max-generation-bytes", 0, "sealed generation byte cap")
	build.Flags().DurationVar(&buildOptions.Deadline, "deadline", 0, "absolute attempt duration budget")
	build.Flags().IntVar(&buildOptions.MaxInFlight, "max-in-flight", 0, "shared concurrent HTTP attempt cap")
	build.Flags().IntVar(&buildOptions.RequestsPerSecond, "requests-per-second", 0, "shared HTTP start-rate cap")
	build.Flags().BoolVar(&buildOptions.Comments, "comments", false, "capture qualified comments for every selected item")
	build.Flags().IntVar(&buildOptions.MaxCommentPagesPerItem, "max-comment-pages-per-item", 0, "comment page cap for each selected item")
	build.Flags().IntVar(&buildOptions.MaxCommentsPerItem, "max-comments-per-item", 0, "comment count cap for each selected item")
	build.Flags().BoolVar(&buildOptions.Attachments, "attachments", false, "capture qualified attachment inventories")
	build.Flags().IntVar(&buildOptions.MaxAttachmentPagesPerItem, "max-attachment-pages-per-item", 0, "attachment page cap for each selected item")
	build.Flags().IntVar(&buildOptions.MaxAttachmentsPerItem, "max-attachments-per-item", 0, "attachment count cap for each selected item")
	build.Flags().BoolVar(&buildOptions.AttachmentBodies, "attachment-bodies", false, "capture allowlisted native attachment bodies")
	build.Flags().StringArrayVar(&buildOptions.AttachmentMediaTypes, "attachment-media-type", nil, "exact allowed attachment MIME type (repeatable)")
	build.Flags().Int64Var(&buildOptions.MaxAttachmentBytes, "max-attachment-bytes", 0, "per-attachment body byte cap")
	build.Flags().Int64Var(&buildOptions.MaxTotalAttachmentBytes, "max-total-attachment-bytes", 0, "generation-wide attachment body byte cap")
	build.Flags().BoolVar(&buildOptions.AllowPartialEvidence, "allow-partial-evidence", false, "publish requested evidence with explicit partial qualifications")

	group.AddCommand(build, cache, diff, export, handoff)
	return group
}
