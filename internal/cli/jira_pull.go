package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func jiraPullCmd() *cobra.Command {
	var jql, project, into string
	var fields string
	var limit, maxIssues int
	var assets, comments, attachments, attachmentBodies, complete, restartComplete, dryRun, overwriteLocal, stashLocal bool
	var attachmentMediaTypes []string
	var maxCommentPages, maxComments, maxAttachments int
	var maxAttachmentBytes, maxTotalAttachmentBytes int64
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Export an ordinary JQL selection or qualified complete Jira project",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if overwriteLocal && stashLocal {
				return usageErr("--overwrite-local and --stash-local are mutually exclusive")
			}
			if complete {
				if project == "" || !cmd.Flags().Changed("max-issues") {
					return usageErr("--complete requires --project and an explicit --max-issues")
				}
				if maxIssues <= 0 || maxIssues > 1_000_000 {
					return usageErr("--max-issues must be between 1 and 1000000 in complete mode")
				}
				if cmd.Flags().Changed("jql") || cmd.Flags().Changed("limit") {
					return usageErr("--complete cannot be combined with --jql or --limit")
				}
			} else {
				if jql == "" {
					return usageErr("--jql is required unless --complete is set")
				}
				if project != "" || cmd.Flags().Changed("max-issues") || restartComplete {
					return usageErr("--project, --max-issues, and --restart-complete require --complete")
				}
				if err := validateAggregateLimit(limit); err != nil {
					return err
				}
			}
			if err := app.ValidateJiraPullOptionalArtifacts(app.JiraPullOpts{
				Complete: complete,
				Comments: comments, MaxCommentPagesPerItem: maxCommentPages, MaxCommentsPerItem: maxComments,
				Attachments: attachments, AttachmentBodies: attachmentBodies, AttachmentMediaTypes: attachmentMediaTypes,
				MaxAttachmentsPerItem: maxAttachments, MaxAttachmentBytes: maxAttachmentBytes, MaxTotalAttachmentBytes: maxTotalAttachmentBytes,
			}); err != nil {
				return err
			}
			override, err := rf.override()
			if err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			effectiveLimit := limit
			if complete {
				effectiveLimit = 0
			}
			res, err := svc.Pull(cmd.Context(), app.JiraPullOpts{
				JQL: jql, Project: project, Into: into, Limit: effectiveLimit,
				MaxIssues: maxIssues, Fields: splitFields(fields), Assets: assets, Complete: complete, RestartComplete: restartComplete,
				Comments: comments, MaxCommentPagesPerItem: maxCommentPages, MaxCommentsPerItem: maxComments,
				Attachments: attachments, AttachmentBodies: attachmentBodies, AttachmentMediaTypes: attachmentMediaTypes,
				MaxAttachmentsPerItem: maxAttachments, MaxAttachmentBytes: maxAttachmentBytes, MaxTotalAttachmentBytes: maxTotalAttachmentBytes,
				DryRun: dryRun, OverwriteLocal: overwriteLocal, StashLocal: stashLocal, Render: override,
			})
			reportablePartial := res != nil && res.Complete != nil && res.Complete.PartialReason != ""
			if err != nil && !reportablePartial && (res == nil || res.LocalSafety == nil || !errors.Is(err, domain.ErrCheckFailed)) {
				return err
			}
			if res.AssetsSkipped > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: %d image asset(s) skipped (download or write failed) — the affected issues were still pulled without those images\n",
					res.AssetsSkipped)
			}
			if res.EpicChildrenTruncated {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: epic children truncated at %d issues — one or more mirrored epic-child sidecars are incomplete; narrow the pull selection\n",
					res.EpicChildrenTruncatedAt)
			}
			warnRender(cmd.ErrOrStderr(), res.Warnings)
			emitErr := emit(cmd, res, func() string {
				var b strings.Builder
				appendPullLocalSafetyText(&b, res.LocalSafety)
				if complete := res.Complete; complete != nil {
					fmt.Fprintf(&b, "complete-pull: complete=%t source=%s total=%d completed=%d remaining=%d checkpoint_active=%t selector_sha256=%s selection_sha256=%s\n", complete.Complete, complete.Source, complete.Total, complete.Completed, complete.Remaining, complete.CheckpointActive, complete.SelectorSHA256, complete.SelectionSHA256)
					if complete.PartialReason != "" {
						fmt.Fprintf(&b, "partial_reason: %s\n", complete.PartialReason)
					}
				}
				for _, issue := range res.Issues {
					fmt.Fprintf(&b, "%s\t%s", issue.Key, issue.Path)
					if issue.Status != "" {
						fmt.Fprintf(&b, "\t%s", issue.Status)
					}
					b.WriteByte('\n')
				}
				return strings.TrimRight(b.String(), "\n")
			})
			if emitErr != nil {
				return emitErr
			}
			return err
		},
	}
	cmd.Flags().StringVar(&jql, "jql", "", "JQL selecting issues")
	cmd.Flags().StringVar(&project, "project", "", "canonical project key for --complete")
	cmd.Flags().StringVar(&into, "into", mirrorRootDefault("mirror-jira"), "output root dir (default: $ATL_MIRROR_ROOT or \"mirror-jira\")")
	cmd.Flags().IntVar(&limit, "limit", 100, "max issues (0 = all; must be non-negative)")
	cmd.Flags().IntVar(&maxIssues, "max-issues", 0, "required positive selection cap for --complete")
	cmd.Flags().StringVar(&fields, "fields", "", "extra comma-separated field list to include in JSON snapshots")
	cmd.Flags().BoolVar(&assets, "assets", false, "also mirror each issue's image attachments into a per-issue <KEY>.assets/ dir and link them from the .md")
	cmd.Flags().BoolVar(&comments, "comments", false, "capture bounded, qualified Jira comment sidecars for each complete-pull issue")
	cmd.Flags().IntVar(&maxCommentPages, "max-comment-pages-per-issue", 0, "required bounded comment-page cap for each complete-pull issue")
	cmd.Flags().IntVar(&maxComments, "max-comments-per-issue", 0, "required bounded comment count cap for each complete-pull issue")
	cmd.Flags().BoolVar(&attachments, "attachments", false, "capture a bounded, qualified attachment inventory for each complete-pull issue")
	cmd.Flags().BoolVar(&attachmentBodies, "attachment-bodies", false, "capture allowlisted attachment bodies into contained private .attachments/ files (requires --attachments)")
	cmd.Flags().StringArrayVar(&attachmentMediaTypes, "attachment-media-type", nil, "exact allowed attachment MIME type (repeatable; requires --attachment-bodies)")
	cmd.Flags().IntVar(&maxAttachments, "max-attachments-per-issue", 0, "required bounded attachment-inventory cap for each complete-pull issue")
	cmd.Flags().Int64Var(&maxAttachmentBytes, "max-attachment-bytes", 0, "maximum captured size of one attachment body (requires --attachment-bodies)")
	cmd.Flags().Int64Var(&maxTotalAttachmentBytes, "max-total-attachment-bytes", 0, "maximum captured attachment-body bytes across the complete pull (requires --attachment-bodies)")
	cmd.Flags().BoolVar(&complete, "complete", false, "exhaust and resume one exact two-pass project snapshot")
	cmd.Flags().BoolVar(&restartComplete, "restart-complete", false, "replace an unfinished complete-project snapshot after fresh selection")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "qualify the pull without writing mirror files or state")
	cmd.Flags().BoolVar(&overwriteLocal, "overwrite-local", false, "explicitly replace qualified locally edited native .wiki bytes")
	cmd.Flags().BoolVar(&stashLocal, "stash-local", false, "preserve qualified locally edited native .wiki bytes under .atl/stash before replacing them")
	rf.register(cmd)
	return cmd
}
