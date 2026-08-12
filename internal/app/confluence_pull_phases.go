package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

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
	if r.complete != nil {
		expectedSpace := completePullExpectedSpace(r.opts)
		if page == nil || page.ID != id || page.Type != "page" || expectedSpace != "" && page.SpaceKey != expectedSpace {
			return nil, fmt.Errorf("%w: fetched Confluence content no longer matches the qualified complete selection", domain.ErrCheckFailed)
		}
	}
	if err := requireConfluenceNativeBody(page, id, "pull"); err != nil {
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
	if root, err := csf.Parse(page.Body); err == nil {
		pageNode = root
		refs = fragment.Extract(root)
		deps := fragment.Deps{Assets: assetStage, Users: r.service.users}
		if r.opts.Assets {
			deps.Resolver = r.service.assets
		}
		refs = fragment.Resolve(r.ctx, page, refs, deps)
		if assetStage.err != nil {
			return nil, fmt.Errorf("%w: page %s assets could not be staged safely: %v", domain.ErrCheckFailed, fetched.id, assetStage.err)
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
		commentInventory, err = r.service.commentInventoryForPage(r.ctx, page, ConfluenceCommentInventoryOpts{
			Location: "all", State: "all", Depth: "all",
		})
		if err != nil {
			return nil, fmt.Errorf("pull comments %s: %w", fetched.id, err)
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

	return &confluencePullPreparedPage{
		confluencePullFetchedPage: fetched,
		refs:                      refs, assetStage: assetStage, commentInventory: commentInventory,
		commentSidecar: commentSidecar, comments: comments, commentsTruncated: commentsTruncated,
		mdOpts: mdOpts, jiraMacros: jiraMacros,
		completeEligible: !r.opts.Comments || (commentInventory.CommentsComplete && commentInventory.ThreadsComplete),
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
			return nil, fmt.Errorf("write staged assets %s: %w", revalidated.id, err)
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
	for _, asset := range revalidated.assetStage.assets {
		assetPath := filepath.Join(revalidated.dir, revalidated.slug+".assets", asset.name)
		assetRel, err := mirror.PublicArtifactPathWithin(r.result.Root, assetPath)
		if err != nil {
			return nil, fmt.Errorf("prepare staged asset %s: %w", revalidated.id, err)
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
		return nil, fmt.Errorf("stage complete-pull page %s: %w", revalidated.id, err)
	}
	staged.syncState = state
	return staged, nil
}

func (r *confluencePullRun) publishPage(staged *confluencePullStagedPage) error {
	if r.complete != nil {
		if err := r.mirror.RecoverCompletePullPublication(r.complete.checkpoint.SelectorSHA256, r.complete.checkpoint, true); err != nil {
			return fmt.Errorf("publish complete-pull page %s: %w", staged.id, err)
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
