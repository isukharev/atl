package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mirror"
)

type confluencePullQualification struct {
	local      *confluenceLocalQualification
	processIDs []string
	actions    []PullLocalAction
	errs       []error
}

func qualifyConfluencePull(m *mirror.Mirror, ids []string, opts PullOpts, stopAtBlocked bool) (*confluencePullQualification, error) {
	local, err := qualifyConfluenceLocal(m, ids, opts)
	if err != nil {
		return nil, err
	}
	processIDs, actions, errs := qualifyConfluenceProcessIDs(ids, local, stopAtBlocked)
	return &confluencePullQualification{local: local, processIDs: processIDs, actions: actions, errs: errs}, nil
}

type confluencePullRun struct {
	service       *ConfluenceService
	ctx           context.Context
	opts          PullOpts
	settings      RenderSettings
	result        *PullResult
	mirror        *mirror.Mirror
	batch         *mirror.SyncBatch
	complete      *confluenceCompleteSelection
	incremental   *confluenceIncrementalSelection
	qualification *confluencePullQualification

	commentSelectionIncomplete bool
	macroOptOutWarned          bool
}

type confluencePullFetchedPage struct {
	id         string
	page       *domain.Resource
	dir        string
	slug       string
	rel        string
	local      *confluenceLocalPage
	relocation *mirror.PageRelocation
}

type confluencePullPreparedPage struct {
	*confluencePullFetchedPage
	refs              []domain.Ref
	assetStage        *stagedConfluenceAssetSink
	commentInventory  *ConfluenceCommentInventoryResult
	commentSidecar    *mirror.ConfluenceCommentsSidecarV2
	attachmentCapture *corpusAttachmentCapture
	comments          []domain.Comment
	commentsTruncated bool
	mdOpts            mirror.MDViewOpts
	jiraMacros        *confluenceJiraMacroSidecar
	completeEligible  bool
}

type confluencePullRevalidatedPage struct {
	*confluencePullPreparedPage
	current []byte
}

type confluencePullStagedPage struct {
	*confluencePullRevalidatedPage
	status           string
	pendingStashPath string
	viewState        mirror.ViewState
	syncState        mirror.SyncState
}

func (r *confluencePullRun) processPage(id string, prefetch *orderedPagePrefetch) error {
	fetched, err := r.fetchPage(id, prefetch)
	if err != nil || fetched == nil {
		return err
	}
	prepared, err := r.preparePage(fetched)
	if err != nil {
		return err
	}
	revalidated, err := r.revalidatePage(prepared)
	if err != nil || revalidated == nil {
		return err
	}
	staged, err := r.stagePage(revalidated)
	if err != nil {
		return err
	}
	return r.publishPage(staged)
}

func (r *confluencePullRun) fetchPage(id string, prefetch *orderedPagePrefetch) (*confluencePullFetchedPage, error) {
	var page *domain.Resource
	var err error
	if prefetch != nil {
		page, err = prefetch.nextPage(id)
	} else {
		page, err = r.service.store.GetPage(r.ctx, id, domain.PullOpts{Format: "csf", IncludeRestrictions: confluenceNeedsRestrictions(r.settings)})
	}
	if err != nil {
		return nil, fmt.Errorf("pull %s: %w", id, err)
	}
	if err := requireConfluencePullProjection(page, id, "pull", r.opts); err != nil {
		return nil, err
	}
	dir, slug, err := r.mirror.ClaimPageDir(page.SpaceKey, page.Ancestors, page.Title, page.ID)
	if err != nil {
		return nil, fmt.Errorf("pull %s: %w", id, err)
	}
	nativePath, err := mirror.PublicArtifactPathWithin(r.result.Root, filepath.Join(dir, slug+".csf"))
	if err != nil {
		return nil, fmt.Errorf("pull %s: qualify native artifact: %w", id, err)
	}
	rel := nativePath.String()
	if action, targetErr := qualifyConfluenceClaimedTarget(r.mirror, id, dir, slug, rel, r.qualification.local); targetErr != nil {
		appendPullLocalBlocked(&r.result.LocalSafety, r.opts.DryRun, *action)
		if r.complete != nil {
			return nil, errors.Join(pullLocalSafetyError("confluence", r.result.LocalSafety), targetErr)
		}
		r.qualification.errs = append(r.qualification.errs, targetErr)
		return nil, nil
	}
	local := r.qualification.local.byID[id]
	if _, localErr := revalidateConfluencePullLocal(r.mirror, local); localErr != nil {
		action := PullLocalAction{ID: id, Path: filepath.ToSlash(rel), Status: pullLocalBlocked, Reason: "local_artifacts_changed"}
		appendPullLocalBlocked(&r.result.LocalSafety, r.opts.DryRun, action)
		if r.complete != nil {
			return nil, errors.Join(pullLocalSafetyError("confluence", r.result.LocalSafety), localErr)
		}
		r.qualification.errs = append(r.qualification.errs, localErr)
		return nil, nil
	}
	relocation, err := planConfluencePageRelocation(r.mirror, page.ID, rel)
	if err != nil {
		return nil, fmt.Errorf("pull %s: %w", id, err)
	}
	return &confluencePullFetchedPage{id: id, page: page, dir: dir, slug: slug, rel: rel, local: local, relocation: relocation}, nil
}

func (r *confluencePullRun) preparePage(fetched *confluencePullFetchedPage) (*confluencePullPreparedPage, error) {
	page := fetched.page
	refs := []domain.Ref{}
	assetStage := &stagedConfluenceAssetSink{slug: fetched.slug}
	var pageNode *csf.Node
	assetIncludeQualification := ConfluencePullIncludeQualified
	assetIncludeReason := ""
	if root, err := csf.Parse(page.Body); err == nil {
		pageNode = root
		refs = fragment.Extract(root)
		deps := fragment.Deps{Assets: assetStage}
		if !r.opts.deterministicRawUsers {
			deps.Users = r.service.users
		}
		if r.opts.Assets {
			deps.Resolver = r.service.assets
		}
		refs = fragment.Resolve(r.ctx, page, refs, deps)
		if assetStage.err != nil {
			if err := r.result.recordConfluencePullInclude(ConfluencePullIncludeAssets, ConfluencePullIncludeFailed, ConfluencePullIncludeReasonStagingFailed); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: page %s assets could not be staged safely: %v", domain.ErrCheckFailed, fetched.id, assetStage.err)
		}
		if r.opts.Assets {
			for _, ref := range refs {
				if (ref.Kind == domain.RefDrawio || ref.Kind == domain.RefImage) && ref.Asset == "" {
					assetIncludeQualification = ConfluencePullIncludePartial
					assetIncludeReason = ConfluencePullIncludeReasonResolutionIncomplete
				}
			}
		}
	} else if r.opts.Assets {
		// Native bytes remain pullable when best-effort CSF parsing fails, but
		// the requested asset inventory cannot be called exhaustive.
		assetIncludeQualification = ConfluencePullIncludePartial
		assetIncludeReason = ConfluencePullIncludeReasonResolutionIncomplete
	}
	if r.opts.Assets {
		if err := r.result.recordConfluencePullInclude(ConfluencePullIncludeAssets, assetIncludeQualification, assetIncludeReason); err != nil {
			return nil, err
		}
	}
	page.Refs = refs

	// Comments are an opt-in include. Fetch before the write so their count and
	// truncation flag can be stamped into .meta.json in one pass. A fetch error
	// aborts the pull (the user explicitly asked for comments); a truncated
	// listing is surfaced, never silently clipped.
	var comments []domain.Comment
	var commentInventory *ConfluenceCommentInventoryResult
	var commentSidecar *mirror.ConfluenceCommentsSidecarV2
	var commentsTruncated bool
	if r.opts.Comments {
		var err error
		commentOptions := ConfluenceCommentInventoryOpts{
			Location: "all", State: "all", Depth: "all",
		}
		if r.opts.evidence != nil && r.opts.evidence.binding.Comments {
			commentOptions.MaxPages = r.opts.evidence.binding.MaxCommentPagesPerItem
			commentOptions.MaxItems = r.opts.evidence.binding.MaxCommentsPerItem
		}
		commentInventory, err = r.service.commentInventoryForPage(r.ctx, page, commentOptions)
		if err != nil {
			if r.opts.evidence != nil && r.opts.evidence.binding.Comments && r.opts.evidence.binding.AllowPartialEvidence &&
				errors.Is(err, domain.ErrForbidden) {
				commentInventory = forbiddenConfluenceCommentInventory(page, commentOptions)
			} else {
				if qualificationErr := r.result.recordConfluencePullInclude(ConfluencePullIncludeComments, ConfluencePullIncludeFailed, ConfluencePullIncludeReasonReadFailed); qualificationErr != nil {
					return nil, qualificationErr
				}
				return nil, fmt.Errorf("pull comments %s: %w", fetched.id, err)
			}
		}
		commentQualification := ConfluencePullIncludeQualified
		commentReason := ""
		if !commentInventory.CommentsComplete || !commentInventory.ThreadsComplete || !commentInventory.AnchorsComplete {
			commentQualification = ConfluencePullIncludePartial
			commentReason = ConfluencePullIncludeReasonInventoryIncomplete
		}
		if err := r.result.recordConfluencePullInclude(ConfluencePullIncludeComments, commentQualification, commentReason); err != nil {
			return nil, err
		}
		comments = confluenceQualifiedCommentsForDisplay(commentInventory, "")
		sidecar := confluenceCommentsSidecarV2(commentInventory)
		commentSidecar = &sidecar
		commentsTruncated = confluenceCommentInventoryTruncated(commentInventory)
		if !commentInventory.CommentsComplete || !commentInventory.ThreadsComplete {
			r.commentSelectionIncomplete = true
			r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("pull: comments or thread geometry for page %s are partial; inspect the versioned comment sidecar", fetched.id))
		} else if !commentInventory.AnchorsComplete {
			r.result.Warnings = append(r.result.Warnings, fmt.Sprintf("pull: inline comment anchors for page %s are partial; inspect the versioned comment sidecar", fetched.id))
		}
		if commentsTruncated {
			r.result.CommentsTruncated = true
		}
		if r.opts.evidence != nil && r.opts.evidence.binding.Comments && !r.opts.evidence.binding.AllowPartialEvidence && !commentInventory.Complete {
			return nil, fmt.Errorf("%w: requested Confluence comments are incomplete", domain.ErrCheckFailed)
		}
	}

	var attachmentCapture *corpusAttachmentCapture
	if r.opts.evidence != nil && r.opts.evidence.binding.Attachments {
		attachmentOptions := ConfluenceAttachmentInventoryOpts{
			ExpectedPageVersion: page.Version,
			MaxPages:            r.opts.evidence.binding.MaxAttachmentPagesPerItem,
			MaxItems:            r.opts.evidence.binding.MaxAttachmentsPerItem,
		}
		attachmentInventory, err := r.service.attachmentInventoryForParent(r.ctx, page.ID, page.Version, attachmentOptions)
		var inventory domain.AttachmentInventory
		if err != nil {
			if r.opts.evidence.binding.AllowPartialEvidence && errors.Is(err, domain.ErrForbidden) {
				inventory = domain.AttachmentInventory{Attachments: []domain.Attachment{}, PartialReason: mirror.AttachmentInventoryForbidden}
			} else {
				return nil, fmt.Errorf("pull attachment inventory %s: %w", fetched.id, err)
			}
		} else {
			inventory = domain.AttachmentInventory{
				Attachments: attachmentInventory.Attachments, Complete: attachmentInventory.Complete,
				PartialReason: attachmentInventory.PartialReason,
			}
		}
		stem := strings.TrimSuffix(fetched.rel, ".csf")
		captured, err := captureCorpusAttachments(r.ctx, r.result.Root, mirror.CorpusSnapshotConfluence, page.ID, stem, inventory, r.opts.evidence,
			func(ctx context.Context, attachment domain.Attachment) (io.ReadCloser, error) {
				return r.service.store.DownloadAttachment(ctx, page.ID, attachment.Title, attachment.Version)
			})
		if err != nil {
			return nil, fmt.Errorf("pull attachment bodies %s: %w", fetched.id, err)
		}
		attachmentCapture = &captured
	}
	if r.opts.evidence != nil && (r.opts.evidence.binding.Comments || r.opts.evidence.binding.Attachments) {
		current, err := r.service.store.GetMeta(r.ctx, page.ID)
		if err != nil {
			return nil, fmt.Errorf("revalidate evidence parent %s: %w", fetched.id, err)
		}
		if current == nil || current.ID != page.ID || current.Version != page.Version {
			return nil, fmt.Errorf("%w: Confluence evidence parent changed during capture", domain.ErrCheckFailed)
		}
	}

	commentView := confluenceCommentsView{flat: comments, qualified: commentSidecar}
	mdOpts := confMDViewOptsForCommentsView(r.settings, page, commentView)
	var jiraMacros *confluenceJiraMacroSidecar
	if pageNode != nil && r.settings.ExpandJiraMacros {
		var macroWarnings []string
		hasJiraMacros := len(mirror.JiraMacroDescriptors(pageNode)) > 0
		jiraReady, err := r.service.prepareConfluenceJiraMacroPopulation(r.result.Root, hasJiraMacros, r.opts.DryRun)
		if err != nil {
			return nil, err
		}
		if hasJiraMacros && !jiraReady {
			macroWarnings = append(macroWarnings, "render: Jira query macro(s) kept as placeholders because qualified Jira read access is unavailable")
		} else {
			jiraMacros, macroWarnings = r.service.resolveConfluenceJiraMacros(r.ctx, page.ID, pageNode, r.opts.JiraView)
		}
		r.result.Warnings = append(r.result.Warnings, macroWarnings...)
		mdOpts.JiraMacros = confluenceJiraMacroViews(jiraMacros)
	} else if pageNode != nil && len(mirror.JiraMacroDescriptors(pageNode)) > 0 && !r.macroOptOutWarned {
		r.result.Warnings = append(r.result.Warnings, "render: Jira query macro expansion is disabled; placeholders retained and no Jira request was made")
		r.macroOptOutWarned = true
	}

	commentEligible := !r.opts.Comments || (commentInventory.CommentsComplete && commentInventory.ThreadsComplete)
	if r.opts.evidence != nil && r.opts.evidence.binding.Comments {
		commentEligible = r.opts.evidence.binding.AllowPartialEvidence || commentInventory.Complete
	}
	attachmentEligible := attachmentCapture == nil ||
		(r.opts.evidence != nil && (r.opts.evidence.binding.AllowPartialEvidence ||
			attachmentCapture.inventoryComplete && attachmentCapture.bodiesState != mirror.AttachmentBodiesPartial))
	return &confluencePullPreparedPage{
		confluencePullFetchedPage: fetched,
		refs:                      refs, assetStage: assetStage, commentInventory: commentInventory,
		commentSidecar: commentSidecar, attachmentCapture: attachmentCapture,
		comments: comments, commentsTruncated: commentsTruncated,
		mdOpts: mdOpts, jiraMacros: jiraMacros,
		completeEligible: commentEligible && attachmentEligible,
	}, nil
}

func (r *confluencePullRun) revalidatePage(prepared *confluencePullPreparedPage) (*confluencePullRevalidatedPage, error) {
	if action, targetErr := qualifyConfluenceClaimedTarget(r.mirror, prepared.id, prepared.dir, prepared.slug, prepared.rel, r.qualification.local); targetErr != nil {
		appendPullLocalBlocked(&r.result.LocalSafety, r.opts.DryRun, *action)
		return nil, errors.Join(pullLocalSafetyError("confluence", r.result.LocalSafety), targetErr)
	}
	current, err := revalidateConfluencePullLocal(r.mirror, prepared.local)
	if err != nil {
		action := PullLocalAction{ID: prepared.id, Path: filepath.ToSlash(prepared.rel), Status: pullLocalBlocked, Reason: "local_artifacts_changed"}
		appendPullLocalBlocked(&r.result.LocalSafety, r.opts.DryRun, action)
		if r.complete != nil {
			return nil, errors.Join(pullLocalSafetyError("confluence", r.result.LocalSafety), err)
		}
		r.qualification.errs = append(r.qualification.errs, err)
		return nil, nil
	}
	return &confluencePullRevalidatedPage{confluencePullPreparedPage: prepared, current: current}, nil
}

func (r *confluencePullRun) stagePage(revalidated *confluencePullRevalidatedPage) (*confluencePullStagedPage, error) {
	if r.complete == nil {
		if err := revalidated.assetStage.publish(r.mirror, revalidated.dir, revalidated.slug); err != nil {
			return nil, r.assetPublicationError(revalidated.assetStage, fmt.Errorf("write staged assets %s: %w", revalidated.id, err))
		}
		if _, err := revalidateConfluencePullLocal(r.mirror, revalidated.local); err != nil {
			action := PullLocalAction{ID: revalidated.id, Path: filepath.ToSlash(revalidated.rel), Status: pullLocalBlocked, Reason: "local_artifacts_changed"}
			appendPullLocalBlocked(&r.result.LocalSafety, r.opts.DryRun, action)
			return nil, errors.Join(pullLocalSafetyError("confluence", r.result.LocalSafety), err)
		}
	}

	staged := &confluencePullStagedPage{confluencePullRevalidatedPage: revalidated}
	if revalidated.local != nil && revalidated.local.dirty {
		if r.opts.StashLocal {
			stashPath, err := r.mirror.SaveNativeStash("confluence", revalidated.id, ".csf", revalidated.current)
			if err != nil {
				return nil, err
			}
			staged.status = pullLocalStashed
			staged.pendingStashPath = stashPath
			setPullLocalActionStashPath(r.result.LocalSafety, revalidated.id, stashPath)
		} else {
			staged.status = pullLocalOverwritten
		}
	}
	staged.viewState = viewStateOf(r.settings)
	if r.complete == nil {
		return staged, nil
	}

	var state mirror.SyncState
	var artifacts []mirror.CompletePullArtifact
	var err error
	if r.opts.Comments {
		state, artifacts, err = r.mirror.PrepareCompletePullConfluenceComments(revalidated.dir, revalidated.slug, revalidated.page, revalidated.refs, *revalidated.commentSidecar, revalidated.comments, revalidated.commentsTruncated, revalidated.mdOpts)
	} else {
		state, artifacts, err = r.mirror.PrepareCompletePullView(revalidated.dir, revalidated.slug, revalidated.page, revalidated.refs, revalidated.mdOpts)
	}
	if err != nil {
		return nil, fmt.Errorf("prepare complete-pull page %s: %w", revalidated.id, err)
	}
	if revalidated.attachmentCapture != nil {
		stem := strings.TrimSuffix(state.Path, ".csf")
		metadata, metadataErr := completePullArtifactData(artifacts, stem+".meta.json")
		if metadataErr != nil {
			return nil, fmt.Errorf("prepare attachment metadata %s: %w", revalidated.id, metadataErr)
		}
		attachmentArtifacts, attachmentErr := finalizeCorpusAttachmentCapture(
			r.mirror, mirror.CorpusSnapshotConfluence, stem, revalidated.page.ID, revalidated.page.Version, "",
			state.Hash, mirror.Hash(metadata), *revalidated.attachmentCapture,
		)
		if attachmentErr != nil {
			return nil, fmt.Errorf("prepare attachment evidence %s: %w", revalidated.id, attachmentErr)
		}
		artifacts = append(artifacts, attachmentArtifacts...)
	}
	for _, asset := range revalidated.assetStage.assets {
		assetPath := filepath.Join(revalidated.dir, revalidated.slug+".assets", asset.name)
		assetRel, err := mirror.PublicArtifactPathWithin(r.result.Root, assetPath)
		if err != nil {
			return nil, r.assetPublicationError(revalidated.assetStage, fmt.Errorf("prepare staged asset %s: %w", revalidated.id, err))
		}
		artifacts = append(artifacts, mirror.CompletePullArtifact{Path: assetRel, Data: asset.data, Mode: 0o644})
	}
	macroPath := confluenceJiraMacroPath(revalidated.dir, revalidated.slug)
	macroRel, err := mirror.PublicArtifactPathWithin(r.result.Root, macroPath)
	if err != nil {
		return nil, fmt.Errorf("prepare Jira macro sidecar %s: %w", revalidated.id, err)
	}
	if revalidated.jiraMacros == nil {
		artifacts = append(artifacts, mirror.CompletePullArtifact{Path: macroRel, Remove: true})
	} else {
		macroBytes, err := encodeConfluenceJiraMacroSidecar(revalidated.jiraMacros)
		if err != nil {
			return nil, fmt.Errorf("prepare Jira macro sidecar %s: %w", revalidated.id, err)
		}
		artifacts = append(artifacts, mirror.CompletePullArtifact{Path: macroRel, Data: macroBytes, Mode: 0o600})
	}
	entry := mirror.CompletePullJournalEntry{State: state, View: staged.viewState}
	if err := r.mirror.PrepareCompletePullPublication(r.complete.checkpoint, r.complete.nextIndex, entry, revalidated.completeEligible, artifacts, revalidated.relocation); err != nil {
		return nil, r.assetPublicationError(revalidated.assetStage, fmt.Errorf("stage complete-pull page %s: %w", revalidated.id, err))
	}
	staged.syncState = state
	return staged, nil
}

func (r *confluencePullRun) publishPage(staged *confluencePullStagedPage) error {
	if r.complete != nil {
		if err := r.mirror.RecoverCompletePullPublication(r.complete.checkpoint.SelectorSHA256, r.complete.checkpoint, true); err != nil {
			return r.assetPublicationError(staged.assetStage, fmt.Errorf("publish complete-pull page %s: %w", staged.id, err))
		}
		if staged.completeEligible {
			r.batch.Record(staged.syncState)
			r.batch.RecordView(staged.page.ID, staged.viewState)
		}
	} else {
		if r.opts.Comments {
			if err := r.batch.WriteConfluenceComments(staged.dir, staged.slug, staged.page, staged.refs, *staged.commentSidecar, staged.comments, staged.commentsTruncated, staged.mdOpts); err != nil {
				return fmt.Errorf("write %s: %w", staged.id, err)
			}
		} else if err := r.batch.WriteView(staged.dir, staged.slug, staged.page, staged.refs, staged.mdOpts); err != nil {
			return fmt.Errorf("write %s: %w", staged.id, err)
		}
		if err := writeConfluenceJiraMacroSidecar(r.result.Root, staged.dir, staged.slug, staged.jiraMacros); err != nil {
			return fmt.Errorf("write Jira macro sidecar %s: %w", staged.id, err)
		}
		// Record the render settings this .md view was written with so `conf
		// apply` reproduces the exact pristine view rather than ambient config.
		r.batch.RecordView(staged.page.ID, staged.viewState)
	}

	if staged.status != "" {
		setPullLocalActionResult(r.result.LocalSafety, staged.id, staged.status, staged.pendingStashPath)
	}
	if r.complete == nil && staged.relocation != nil {
		// Publish the new canonical state before retiring the old exact page
		// artifacts, so a crash can leave only an untracked stale copy.
		if err := r.batch.Flush(); err != nil {
			return err
		}
		if err := r.mirror.RetirePageRelocation(staged.relocation); err != nil {
			return err
		}
	}
	assetCount := 0
	for _, ref := range staged.refs {
		if ref.Asset != "" {
			assetCount++
		}
	}
	page := PulledPage{ID: staged.id, Title: staged.page.Title, Path: staged.rel, Version: staged.page.Version, Assets: assetCount, Status: staged.status}
	if r.opts.Comments {
		n := staged.commentInventory.Count
		page.Comments = &n
	}
	r.result.Pages = append(r.result.Pages, page)
	if local := r.qualification.local.byID[staged.id]; local != nil && local.migrates {
		if r.incremental != nil {
			r.incremental.result.ViewMigrations++
		}
		if r.complete != nil {
			r.complete.result.ViewMigrations++
		}
	}
	if r.complete != nil {
		if !staged.completeEligible {
			return fmt.Errorf("%w: complete-pull comments for page %s were not fully qualified; checkpoint remains before this page", domain.ErrCheckFailed, staged.id)
		}
		r.complete.advance()
		if r.complete.shouldCheckpoint() {
			if err := r.complete.commit(r.mirror, r.batch); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *confluencePullRun) assetPublicationError(stage *stagedConfluenceAssetSink, err error) error {
	if !r.opts.Assets || stage == nil || len(stage.assets) == 0 {
		return err
	}
	if qualificationErr := r.result.recordConfluencePullInclude(
		ConfluencePullIncludeAssets, ConfluencePullIncludeFailed, ConfluencePullIncludeReasonStagingFailed,
	); qualificationErr != nil {
		return errors.Join(err, qualificationErr)
	}
	return err
}
