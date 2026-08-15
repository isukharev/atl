package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

func confPullCmd() *cobra.Command {
	var o app.PullOpts
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Mirror pages (.csf + .md + .meta.json + assets) by --id/--cql/--space",
		RunE: func(cmd *cobra.Command, _ []string) error {
			attachmentBodyFields := o.AttachmentBodies || len(o.AttachmentMediaTypes) != 0 ||
				o.MaxAttachmentBytes != 0 || o.MaxTotalAttachmentBytes != 0
			attachmentInventoryFields := o.MaxAttachmentPagesPerItem != 0 || o.MaxAttachmentsPerItem != 0
			// Mutually exclusive selectors. Checked here (not via
			// MarkFlagsMutuallyExclusive) so the violation is a usage error (exit 2)
			// rather than cobra's generic error (exit 1).
			set := 0
			for _, v := range []string{o.ID, o.CQL, o.Space} {
				if v != "" {
					set++
				}
			}
			if set > 1 {
				return usageErr("--id, --cql and --space are mutually exclusive")
			}
			if o.Incremental && o.Complete {
				return usageErr("--incremental and --complete are mutually exclusive")
			}
			if o.Attachments && !o.Complete {
				return usageErr("--attachments requires --complete")
			}
			if (attachmentBodyFields || attachmentInventoryFields) && !o.Attachments {
				return usageErr("attachment body policy and bounds require --attachments")
			}
			if o.Attachments && (o.MaxAttachmentPagesPerItem <= 0 || o.MaxAttachmentsPerItem <= 0) {
				return usageErr("--attachments requires positive --max-attachment-pages-per-page and --max-attachments-per-page")
			}
			if o.AttachmentBodies && (len(o.AttachmentMediaTypes) == 0 || o.MaxAttachmentBytes <= 0 || o.MaxTotalAttachmentBytes <= 0) {
				return usageErr("--attachment-bodies requires --attachment-media-type, --max-attachment-bytes, and --max-total-attachment-bytes")
			}
			if !o.AttachmentBodies && (len(o.AttachmentMediaTypes) != 0 || o.MaxAttachmentBytes != 0 || o.MaxTotalAttachmentBytes != 0) {
				return usageErr("--attachment-media-type and attachment-byte bounds require --attachment-bodies")
			}
			if o.AllowPartialArtifacts && !o.Complete {
				return usageErr("--allow-partial-artifacts requires --complete")
			}
			if o.AllowPartialArtifacts && !o.Comments && !o.Attachments {
				return usageErr("--allow-partial-artifacts requires --comments or --attachments")
			}
			if err := app.ValidateConfluencePullOptionalArtifacts(o); err != nil {
				return err
			}
			if o.OverwriteLocal && o.StashLocal {
				return usageErr("--overwrite-local and --stash-local are mutually exclusive")
			}
			if o.PagePrefetch < 1 || o.PagePrefetch > 8 {
				return usageErr("--page-prefetch must be between 1 and 8")
			}
			if o.RequestsPerSecond < 0 || o.RequestsPerSecond > 1000 {
				return usageErr("--requests-per-second must be between 0 and 1000")
			}
			if o.ID != "" && (o.PagePrefetch > 1 || o.RequestsPerSecond > 0) {
				return usageErr("--page-prefetch and --requests-per-second scheduling requires --cql or --space")
			}
			if o.Incremental {
				if o.ID != "" || (o.CQL == "" && o.Space == "") {
					return usageErr("--incremental requires --cql or --space and cannot use --id")
				}
				if o.Space != "" && o.Depth != 0 {
					return usageErr("--incremental --space does not support --depth")
				}
				if o.MaxPages < 0 {
					return usageErr("--max-pages must be >= 0")
				}
				if cmd.Flags().Changed("time-zone") {
					return usageErr("--time-zone was removed; pass an explicit offset in RFC3339 --since instead")
				}
			} else if o.Complete {
				if o.ID != "" || (o.CQL == "" && o.Space == "") {
					return usageErr("--complete requires --cql or --space and cannot use --id")
				}
				if o.Space != "" && o.Depth != 0 {
					return usageErr("--complete --space does not support --depth")
				}
				if o.MaxPages < 0 {
					return usageErr("--max-pages must be >= 0")
				}
				if o.Since != "" || cmd.Flags().Changed("time-zone") {
					return usageErr("--since and --time-zone cannot be used with --complete")
				}
			} else if o.Since != "" || o.RestartComplete || cmd.Flags().Changed("time-zone") || cmd.Flags().Changed("max-pages") {
				return usageErr("--since and --max-pages require --incremental or --complete; --restart-complete requires --complete; --time-zone was removed")
			}
			override, err := rf.override()
			if err != nil {
				return err
			}
			o.Render = override
			var svc *app.ConfluenceService
			if o.Incremental || o.Complete || o.PagePrefetch > 1 || o.RequestsPerSecond > 0 {
				svc, err = confScheduledService(cmd, o.PagePrefetch, o.RequestsPerSecond)
			} else {
				svc, err = confService(cmd)
			}
			if err != nil {
				return err
			}
			res, err := svc.Pull(cmd.Context(), o)
			if err != nil && (res == nil || (!res.HasFailedInclude() && (res.LocalSafety == nil || !errors.Is(err, domain.ErrCheckFailed)))) {
				return err
			}
			// Warn on stderr (never stdout — that would corrupt the JSON result).
			warnIfTruncated(cmd.ErrOrStderr(), res)
			warnRender(cmd.ErrOrStderr(), res.Warnings)
			emitErr := emit(cmd, res, func() string {
				var b strings.Builder
				fmt.Fprintf(&b, "mirror: %s (%d pages)\n", res.Root, len(res.Pages))
				for _, include := range res.Includes {
					fmt.Fprintf(&b, "include: %s requested=%t qualification=%s", include.Dimension, include.Requested, include.Qualification)
					if include.Complete != nil {
						fmt.Fprintf(&b, " complete=%t", *include.Complete)
					}
					if include.Reason != "" {
						fmt.Fprintf(&b, " reason=%s", include.Reason)
					}
					b.WriteByte('\n')
				}
				appendPullLocalSafetyText(&b, res.LocalSafety)
				if res.Incremental != nil {
					inc := res.Incremental
					fmt.Fprintf(&b, "incremental: complete=%t source=%s watermark_instant=%s query_literal=%s query_literal_basis=%s backend_query_time_zone=%s safety_overlap_hours=%d next=%s matched=%d selected=%d overlap_skipped=%d boundary_skipped=%d view_migrations=%d watermark_advanced=%t\n", inc.Complete, inc.WatermarkSource, inc.WatermarkInstant, inc.QueryLiteral, inc.QueryLiteralBasis, inc.BackendQueryTimeZone, inc.SafetyOverlapHours, inc.NextInstant, inc.Matched, inc.Selected, inc.OverlapSkipped, inc.BoundarySkipped, inc.ViewMigrations, inc.WatermarkAdvanced)
				}
				if res.Complete != nil {
					complete := res.Complete
					fmt.Fprintf(&b, "complete-pull: complete=%t source=%s total=%d completed=%d remaining=%d checkpoint_active=%t selector_sha256=%s selection_sha256=%s view_migrations=%d\n", complete.Complete, complete.Source, complete.Total, complete.Completed, complete.Remaining, complete.CheckpointActive, complete.SelectorSHA256, complete.SelectionSHA256, complete.ViewMigrations)
				}
				if schedule := res.Scheduling; schedule != nil {
					fmt.Fprintf(&b, "scheduling: page_prefetch=%d max_in_flight=%d requests_per_second=%d\n", schedule.PagePrefetch, schedule.MaxInFlight, schedule.RequestsPerSecond)
				}
				for _, p := range res.Pages {
					fmt.Fprintf(&b, "  %s  v%d  %s  [assets:%d", p.ID, p.Version, p.Path, p.Assets)
					if p.Comments != nil {
						fmt.Fprintf(&b, " comments:%d", *p.Comments)
					}
					if p.Attachments != nil {
						fmt.Fprintf(&b, " attachments:%d", *p.Attachments)
					}
					b.WriteString("]")
					if p.Status != "" {
						fmt.Fprintf(&b, "  %s", p.Status)
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
	cmd.Flags().StringVar(&o.ID, "id", "", "page id or supported same-origin URL")
	cmd.Flags().StringVar(&o.CQL, "cql", "", "CQL selecting pages")
	cmd.Flags().StringVar(&o.Space, "space", "", "space key (whole space)")
	cmd.Flags().IntVar(&o.Depth, "depth", 0, "space depth limit")
	cmd.Flags().BoolVar(&o.Assets, "assets", false, "download diagram/image renders")
	cmd.Flags().BoolVar(&o.Comments, "comments", false, "mirror page comments into <slug>.comments.json/.md sidecars")
	cmd.Flags().BoolVar(&o.Attachments, "attachments", false, "capture a bounded, qualified attachment inventory for every complete-pull page")
	cmd.Flags().BoolVar(&o.AttachmentBodies, "attachment-bodies", false, "capture allowlisted attachment bodies into contained <slug>.attachments/ files (requires --attachments)")
	cmd.Flags().StringArrayVar(&o.AttachmentMediaTypes, "attachment-media-type", nil, "exact allowed attachment MIME type (repeatable; requires --attachment-bodies)")
	cmd.Flags().IntVar(&o.MaxAttachmentPagesPerItem, "max-attachment-pages-per-page", 0, "required bounded attachment-inventory page cap for each selected page")
	cmd.Flags().IntVar(&o.MaxAttachmentsPerItem, "max-attachments-per-page", 0, "required bounded attachment-inventory item cap for each selected page")
	cmd.Flags().Int64Var(&o.MaxAttachmentBytes, "max-attachment-bytes", 0, "maximum captured size of one attachment body (requires --attachment-bodies)")
	cmd.Flags().Int64Var(&o.MaxTotalAttachmentBytes, "max-total-attachment-bytes", 0, "maximum captured attachment-body bytes across the complete pull (requires --attachment-bodies)")
	cmd.Flags().BoolVar(&o.AllowPartialArtifacts, "allow-partial-artifacts", false, "persist explicitly partial comments/attachments sidecars and complete the main page snapshot")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "qualify the pull without writing mirror files, state, watermarks, or checkpoints")
	cmd.Flags().BoolVar(&o.OverwriteLocal, "overwrite-local", false, "explicitly replace qualified locally edited native .csf bytes")
	cmd.Flags().BoolVar(&o.StashLocal, "stash-local", false, "preserve qualified locally edited native .csf bytes under .atl/stash before replacing them")
	cmd.Flags().StringVar(&o.Into, "into", mirrorRootDefault("mirror"), "mirror root dir (default: $ATL_MIRROR_ROOT or \"mirror\")")
	cmd.Flags().StringVar(&o.JiraView, "jira-view", "", "named Jira list view for JQL macros (default: default; macro columns win)")
	cmd.Flags().BoolVar(&o.Incremental, "incremental", false, "pull a complete changed-page delta using a selector-bound watermark")
	cmd.Flags().BoolVar(&o.Complete, "complete", false, "exhaust and resume one exact two-pass selector snapshot (no ordinary 1000/2000 cap)")
	cmd.Flags().BoolVar(&o.RestartComplete, "restart-complete", false, "replace an unfinished complete-pull snapshot after fresh selection and local preflight")
	cmd.Flags().StringVar(&o.Since, "since", "", "first-run lower boundary as an exact RFC3339 minute with explicit offset")
	cmd.Flags().StringVar(&o.TimeZone, "time-zone", "", "removed: put the explicit offset in --since")
	_ = cmd.Flags().MarkHidden("time-zone")
	cmd.Flags().IntVar(&o.MaxPages, "max-pages", 0, "selection cap (incremental default 10000; complete 0 means no configured cap)")
	cmd.Flags().IntVar(&o.PagePrefetch, "page-prefetch", 1, "ordered native-body prefetch window for multi-page pulls (1-8; mirror writes remain serial)")
	cmd.Flags().IntVar(&o.RequestsPerSecond, "requests-per-second", 0, "shared Confluence/Jira request-start rate for scheduled pulls (0 disables proactive pacing)")
	rf.register(cmd)
	rf.registerConfluenceJiraMacros(cmd)
	return cmd
}
