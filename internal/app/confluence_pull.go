package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

// ---- pull ----

// PullOpts selects what to mirror and where. Render is the per-run flag override
// for the markdown view profile; a zero value leaves the effective settings
// (local + global config) untouched.
type PullOpts struct {
	ID             string
	CQL            string
	Space          string
	Depth          int
	Assets         bool
	Comments       bool
	Into           string
	Render         config.RenderService
	JiraView       string
	Incremental    bool
	Complete       bool
	DryRun         bool
	OverwriteLocal bool
	StashLocal     bool
	// RestartComplete explicitly replaces an unfinished complete-pull snapshot
	// after a fresh two-pass selection and local overwrite preflight succeed.
	RestartComplete   bool
	Since             string
	TimeZone          string
	MaxPages          int
	PagePrefetch      int
	RequestsPerSecond int
	exactRender       *RenderSettings
	evidence          *corpusPullEvidenceOptions
	// deterministicRawUsers keeps cache-qualified projections independent of
	// mutable directory display names. It is private and can only be selected by
	// the corpus builder; the complete-pull options receipt binds it explicitly.
	deterministicRawUsers bool
}

// PulledPage is one mirrored page.
type PulledPage struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Version int    `json:"version"`
	Assets  int    `json:"assets"`
	Status  string `json:"status,omitempty"`
	// Comments is the number of comments mirrored for this page. It is a pointer
	// so a --comments pull that found zero comments still emits `"comments": 0`,
	// distinguishable from a pull that never fetched them (field omitted).
	Comments *int `json:"comments,omitempty"`
}

// PullResult is the pull summary.
type PullResult struct {
	Root        string                  `json:"root"`
	Pages       []PulledPage            `json:"pages"`
	Includes    []ConfluencePullInclude `json:"includes"`
	Incremental *IncrementalPullResult  `json:"incremental,omitempty"`
	Complete    *CompletePullResult     `json:"complete_pull,omitempty"`
	LocalSafety *PullLocalSafety        `json:"local_safety,omitempty"`
	// Truncated is true when a --cql selection hit the silent pagination cap, so
	// some matching pages were NOT mirrored. TruncatedAt is the cap that was hit
	// (the number of ids collected). Both are omitted from JSON in the common,
	// non-truncated case so existing consumers see an unchanged shape.
	Truncated   bool `json:"truncated,omitempty"`
	TruncatedAt int  `json:"truncated_at,omitempty"`
	// CommentsTruncated is true when at least one page's comment listing hit the
	// adapter's fetch cap, so its mirrored comments sidecar is incomplete. The CLI
	// surfaces it as a stderr warning; omitted otherwise so the shape is unchanged.
	CommentsTruncated bool `json:"comments_truncated,omitempty"`
	// Warnings are advisory render-resolution messages; CLI-only and not serialized.
	Warnings        []string        `json:"-"`
	Scheduling      *PullScheduling `json:"scheduling,omitempty"`
	includeProgress *confluencePullIncludeProgress
}

// PullScheduling reports the exact opt-in load policy. PagePrefetch overlaps
// native body GETs only; MaxInFlight and RequestsPerSecond cover every HTTP
// attempt made through the shared Confluence/Jira scheduler.
type PullScheduling struct {
	PagePrefetch      int `json:"page_prefetch"`
	MaxInFlight       int `json:"max_in_flight"`
	RequestsPerSecond int `json:"requests_per_second"`
}

type stagedConfluenceAsset struct {
	name string
	data []byte
}

type stagedConfluenceAssetSink struct {
	slug   string
	assets []stagedConfluenceAsset
	bytes  int64
	err    error
}

const confluenceStagedAssetBytesMax int64 = 64 << 20

func (s *stagedConfluenceAssetSink) Put(name string, data []byte) (string, error) {
	safe, ok := safepath.Base(name)
	if !ok {
		s.err = fmt.Errorf("refusing unsafe asset name")
		return "", s.err
	}
	for i := range s.assets {
		if s.assets[i].name != safe {
			continue
		}
		nextBytes := s.bytes - int64(len(s.assets[i].data)) + int64(len(data))
		if nextBytes > confluenceStagedAssetBytesMax {
			s.err = fmt.Errorf("staged page assets exceed %d bytes", confluenceStagedAssetBytesMax)
			return "", s.err
		}
		s.assets[i].data = append([]byte(nil), data...)
		s.bytes = nextBytes
		return s.slug + ".assets/" + safe, nil
	}
	if int64(len(data)) > confluenceStagedAssetBytesMax-s.bytes {
		s.err = fmt.Errorf("staged page assets exceed %d bytes", confluenceStagedAssetBytesMax)
		return "", s.err
	}
	s.assets = append(s.assets, stagedConfluenceAsset{name: safe, data: append([]byte(nil), data...)})
	s.bytes += int64(len(data))
	return s.slug + ".assets/" + safe, nil
}

func (s *stagedConfluenceAssetSink) publish(m *mirror.Mirror, dir, slug string) error {
	sink := m.AssetSink(dir, slug)
	for _, asset := range s.assets {
		if _, err := sink.Put(asset.name, asset.data); err != nil {
			return err
		}
	}
	return nil
}

// Pull mirrors pages selected by id/cql/space into Into.
func (s *ConfluenceService) Pull(ctx context.Context, o PullOpts) (result *PullResult, retErr error) {
	if o.OverwriteLocal && o.StashLocal {
		return nil, fmt.Errorf("%w: --overwrite-local and --stash-local are mutually exclusive", domain.ErrUsage)
	}
	if o.PagePrefetch < 0 || o.PagePrefetch > 8 {
		return nil, fmt.Errorf("%w: --page-prefetch must be between 1 and 8", domain.ErrUsage)
	}
	if o.RequestsPerSecond < 0 || o.RequestsPerSecond > 1000 {
		return nil, fmt.Errorf("%w: --requests-per-second must be between 0 and 1000", domain.ErrUsage)
	}
	pagePrefetch := o.PagePrefetch
	if pagePrefetch == 0 {
		pagePrefetch = 1
	}
	optInSchedule := pagePrefetch > 1 || o.RequestsPerSecond > 0
	if optInSchedule && o.ID != "" {
		return nil, fmt.Errorf("%w: request scheduling requires a multi-page --cql or --space selector", domain.ErrUsage)
	}
	if optInSchedule && (s.requestMaxInFlight != pagePrefetch || s.requestsPerSecond != o.RequestsPerSecond) {
		return nil, fmt.Errorf("%w: pull request schedule was not installed in the service transport", domain.ErrCheckFailed)
	}
	scheduled := o.Incremental || o.Complete || optInSchedule
	if o.Incremental && o.Complete {
		return nil, fmt.Errorf("%w: --incremental and --complete are mutually exclusive", domain.ErrUsage)
	}
	if o.RestartComplete && !o.Complete {
		return nil, fmt.Errorf("%w: --restart-complete requires --complete", domain.ErrUsage)
	}
	root := o.Into
	if root == "" {
		root = "mirror"
	}
	// Resolve presentation policy before backend reads or mirror writes. In
	// particular, jira_macros=off guarantees that this command never loads Jira
	// credentials or executes page-provided JQL.
	rs, warns := resolveRenderExact(s.cfg, root, o.Render, "confluence", o.exactRender)
	if err := s.validateConfluenceJiraView(o.JiraView, rs.ExpandJiraMacros); err != nil {
		return nil, err
	}
	m := mirror.New(root)
	var err error
	if !o.DryRun {
		lock, lockErr := lockConfluenceMutations(root, true)
		if lockErr != nil {
			return nil, lockErr
		}
		defer func() { _ = lock.Unlock() }()
		if err := m.EnsureScaffold(); err != nil {
			return nil, err
		}
	}
	if err := prepareMirrorBackendPopulation(root, "confluence", s.baseURL, ".csf", o.DryRun); err != nil {
		return nil, err
	}
	var incremental *confluenceIncrementalSelection
	var complete *confluenceCompleteSelection
	var ids []string
	var truncated bool
	if o.Incremental {
		if o.ID != "" {
			return nil, fmt.Errorf("%w: --incremental cannot be used with --id", domain.ErrUsage)
		}
		incremental, err = s.prepareIncrementalPull(ctx, m, o)
		if err != nil {
			return nil, err
		}
		ids = incremental.ids
	} else if o.Complete {
		if o.ID != "" {
			return nil, fmt.Errorf("%w: --complete cannot be used with --id", domain.ErrUsage)
		}
		if o.MaxPages < 0 {
			return nil, fmt.Errorf("%w: --max-pages must be >= 0", domain.ErrUsage)
		}
		if o.Since != "" || o.TimeZone != "" {
			return nil, fmt.Errorf("%w: --since and --time-zone cannot be used with --complete", domain.ErrUsage)
		}
		if o.DryRun {
			complete, err = s.prepareCompletePullDryRun(ctx, m, o, rs)
		} else {
			complete, err = s.prepareCompletePull(ctx, m, o, rs)
		}
		if err != nil {
			return nil, err
		}
		ids = complete.checkpoint.IDs[complete.nextIndex:]
	} else {
		if o.TimeZone != "" {
			return nil, fmt.Errorf("%w: --time-zone was removed; pass an explicit offset in RFC3339 --since instead", domain.ErrUsage)
		}
		if o.Since != "" || o.MaxPages != 0 {
			return nil, fmt.Errorf("%w: --since and --max-pages require --incremental", domain.ErrUsage)
		}
		ctx, ids, truncated, err = s.resolveIDs(ctx, o)
		if err != nil {
			return nil, err
		}
	}
	// Effective settings for this root keep default/minimal views byte-identical;
	// only `full` (or an explicit include) adds metadata/comments.
	expectedIncludes := len(ids)
	if complete != nil {
		expectedIncludes = len(complete.checkpoint.IDs)
	}
	res := newConfluencePullResult(root, warns, o, expectedIncludes)
	defer res.finalizeConfluencePullIncludes()
	if complete != nil && !o.DryRun {
		if err := res.restoreConfluencePullIncludes(complete.checkpoint); err != nil {
			return res, err
		}
	}
	if scheduled {
		maxInFlight := s.requestMaxInFlight
		if maxInFlight == 0 {
			maxInFlight = 1
		}
		res.Scheduling = &PullScheduling{PagePrefetch: pagePrefetch, MaxInFlight: maxInFlight, RequestsPerSecond: s.requestsPerSecond}
	}
	if incremental != nil {
		res.Incremental = incremental.result
	}
	if complete != nil {
		res.Complete = complete.result
	}
	if truncated {
		res.Truncated = true
		res.TruncatedAt = len(ids)
	}
	qualification, err := qualifyConfluencePull(m, ids, o, complete != nil)
	if err != nil {
		return res, err
	}
	res.LocalSafety = newPullLocalSafety(o.DryRun, qualification.actions)
	if o.DryRun {
		return s.previewConfluencePull(ctx, o, rs, res, qualification.processIDs, qualification.local, qualification.errs)
	}
	if complete != nil && complete.result.Source == "restarted" && len(qualification.errs) > 0 {
		return res, errors.Join(pullLocalSafetyError("confluence", res.LocalSafety), errors.Join(qualification.errs...))
	}
	if complete != nil && complete.result.Source != "resumed" {
		if err := m.SaveCompletePullCheckpoint(complete.checkpoint); err != nil {
			return res, err
		}
		complete.result.CheckpointActive = true
	}
	if incremental != nil {
		incremental.result.ViewMigrations = 0
	}
	if complete != nil {
		complete.result.ViewMigrations = 0
	}
	// One sidecar load feeds the whole pull. Ordinary/incremental mode saves at
	// the end; complete mode saves at bounded 25-page journal boundaries. The
	// deferred flush persists pages already written when an error aborts a
	// non-complete loop and is a no-op after an explicit successful flush.
	batch, err := m.BeginSync()
	if err != nil {
		return nil, err
	}
	completeFinished := false
	completeRetireStarted := false
	var run *confluencePullRun
	if complete != nil {
		// Graceful failures commit the accepted journal prefix in the same order as
		// a normal 25-page boundary: shared sidecar, progress, journal retirement.
		defer func() {
			if completeFinished {
				return
			}
			if completeRetireStarted {
				retErr = fmt.Errorf("%w; complete-pull completion cleanup was interrupted — rerun the exact command to reconcile private resume state", retErr)
				return
			}
			var commitErr error
			if run != nil {
				commitErr = run.commitCompletePull()
			} else {
				commitErr = complete.commit(m, batch)
			}
			if commitErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("save complete-pull mirror progress: %w", commitErr))
			}
			retErr = fmt.Errorf("%w; complete-pull checkpoint is at %d/%d — rerun the exact command to resume", retErr, complete.savedIndex, complete.result.Total)
		}()
	}
	var prefetch *orderedPagePrefetch
	if pagePrefetch > 1 && !o.deterministicRawUsers {
		prefetch = newOrderedPagePrefetch(ctx, s.store, qualification.processIDs, pagePrefetch, confluenceNeedsRestrictions(rs))
		defer prefetch.close()
	}
	run = &confluencePullRun{
		service: s, ctx: ctx, opts: o, settings: rs, result: res, mirror: m, batch: batch,
		complete: complete, incremental: incremental, qualification: qualification,
	}
	if complete == nil {
		defer func() {
			if err := run.flushOrdinaryPull(); err != nil {
				retErr = errors.Join(retErr, err)
			}
		}()
	}
	for _, id := range qualification.processIDs {
		if err := run.processPage(id, prefetch); err != nil {
			return res, err
		}
	}
	if complete == nil {
		if err := run.flushOrdinaryPull(); err != nil {
			return res, err
		}
	}
	if incremental != nil {
		if len(run.qualification.errs) > 0 {
			return res, errors.Join(pullLocalSafetyError("confluence", res.LocalSafety), errors.Join(run.qualification.errs...))
		}
		if run.commentSelectionIncomplete {
			return res, fmt.Errorf("%w: incremental comments were not fully qualified; watermark unchanged", domain.ErrCheckFailed)
		}
		if err := m.SaveIncrementalWatermark(incremental.next); err != nil {
			return res, err
		}
		res.Incremental.WatermarkAdvanced = incremental.changed
	}
	if complete != nil {
		if len(run.qualification.errs) > 0 {
			return res, errors.Join(pullLocalSafetyError("confluence", res.LocalSafety), errors.Join(run.qualification.errs...))
		}
		if complete.nextIndex != len(complete.checkpoint.IDs) {
			return res, fmt.Errorf("%w: complete-pull progress ended before the exact selection was consumed", domain.ErrCheckFailed)
		}
		if err := run.commitCompletePull(); err != nil {
			return res, err
		}
		completeRetireStarted = true
		if err := m.RemoveCompletePullCheckpoint(complete.checkpoint.SelectorSHA256); err != nil {
			return res, err
		}
		complete.result.Complete = true
		complete.result.CheckpointActive = false
		completeFinished = true
	}
	if len(run.qualification.errs) > 0 {
		return res, errors.Join(pullLocalSafetyError("confluence", res.LocalSafety), errors.Join(run.qualification.errs...))
	}
	return res, nil
}

func qualifyConfluenceProcessIDs(ids []string, qualified *confluenceLocalQualification, stopAtBlocked bool) ([]string, []PullLocalAction, []error) {
	process := make([]string, 0, len(ids))
	actions := make([]PullLocalAction, 0)
	errs := make([]error, 0)
	blockedPosition := false
	for _, id := range ids {
		local := qualified.byID[id]
		if local != nil && local.blockedErr != nil {
			action := PullLocalAction{ID: id, Path: local.path, Status: pullLocalBlocked, Reason: "local_artifacts_unqualified"}
			if local.action != nil {
				action = *local.action
				if local.action.Status != pullLocalBlocked {
					action.Reason = "local_artifacts_unqualified"
				}
				action.Status = pullLocalBlocked
			}
			if len(local.current) > 0 {
				action.CurrentSHA256 = mirror.Hash(local.current)
			}
			if len(local.baseline) > 0 {
				action.BaselineSHA256 = mirror.Hash(local.baseline)
			}
			actions = append(actions, action)
			errs = append(errs, local.blockedErr)
			if stopAtBlocked {
				blockedPosition = true
			}
			continue
		}
		if local != nil && local.dirty {
			action := *local.action
			actions = append(actions, action)
		}
		if !blockedPosition {
			process = append(process, id)
		}
	}
	return process, actions, errs
}

func setPullLocalActionResult(safety *PullLocalSafety, id, status, stashPath string) {
	if safety == nil {
		return
	}
	for i := range safety.Actions {
		if safety.Actions[i].ID == id {
			safety.Actions[i].Status = status
			safety.Actions[i].StashPath = stashPath
			return
		}
	}
}

func setPullLocalActionStashPath(safety *PullLocalSafety, id, stashPath string) {
	if safety == nil {
		return
	}
	for i := range safety.Actions {
		if safety.Actions[i].ID == id {
			safety.Actions[i].StashPath = stashPath
			return
		}
	}
}

func qualifyConfluenceClaimedTarget(m *mirror.Mirror, id, dir, slug, rel string, qualified *confluenceLocalQualification) (*PullLocalAction, error) {
	if local := qualified.byID[id]; local != nil && filepath.Clean(filepath.FromSlash(local.path)) == filepath.Clean(filepath.FromSlash(rel)) {
		return nil, nil
	}
	entries, err := safepath.ReadDirWithin(m.Root, dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	action := &PullLocalAction{ID: id, Path: filepath.ToSlash(rel), Status: pullLocalBlocked, Reason: "target_artifacts_unqualified"}
	if err != nil {
		return action, fmt.Errorf("%w: inspect untracked pull target for page %s: %v", domain.ErrCheckFailed, id, err)
	}
	owned := map[string]bool{
		slug + ".csf": true, slug + ".md": true, slug + ".meta.json": true, slug + ".comments.json": true, slug + ".comments.md": true,
		slug + ".attachments.json": true, slug + ".attachments": true, slug + ".jira-macros.json": true, slug + ".assets": true,
		slug + ".relocated.json": true,
	}
	for _, entry := range entries {
		if owned[entry.Name()] {
			return action, fmt.Errorf("%w: untracked pull target %s for page %s has local page artifacts; preserve or relocate them before pull", domain.ErrCheckFailed, filepath.Join(dir, slug), id)
		}
	}
	return nil, nil
}

func revalidateConfluencePullLocal(m *mirror.Mirror, local *confluenceLocalPage) ([]byte, error) {
	if local == nil {
		return nil, nil
	}
	csfPath := filepath.Join(m.Root, filepath.FromSlash(local.path))
	paths := []string{csfPath, strings.TrimSuffix(csfPath, ".csf") + ".md", strings.TrimSuffix(csfPath, ".csf") + ".meta.json"}
	want := [][]byte{local.current, local.derived, local.metadata}
	for i, path := range paths {
		current, err := safepath.ReadFileWithin(m.Root, path)
		if err != nil || !bytes.Equal(current, want[i]) {
			return nil, fmt.Errorf("%w: page %s local mirror artifacts changed after pull qualification; preserving them", domain.ErrCheckFailed, local.id)
		}
		if i == 0 {
			want[0] = current
		}
	}
	return want[0], nil
}

func (s *ConfluenceService) previewConfluencePull(ctx context.Context, o PullOpts, rs RenderSettings, res *PullResult, ids []string, qualified *confluenceLocalQualification, qualificationErrs []error) (*PullResult, error) {
	m := mirror.New(res.Root)
	for _, id := range ids {
		page, err := s.store.GetPage(ctx, id, domain.PullOpts{Format: "csf", IncludeRestrictions: confluenceNeedsRestrictions(rs)})
		if err != nil {
			return res, fmt.Errorf("preview pull %s: %w", id, err)
		}
		if err := requireConfluencePullProjection(page, id, "pull preview", o); err != nil {
			return res, err
		}
		status := "would_pull"
		dir, slug, claimErr := m.ClaimPageDir(page.SpaceKey, page.Ancestors, page.Title, page.ID)
		if claimErr != nil {
			return res, claimErr
		}
		path, _ := filepath.Rel(res.Root, filepath.Join(dir, slug+".csf"))
		if action, targetErr := qualifyConfluenceClaimedTarget(m, id, dir, slug, path, qualified); targetErr != nil {
			appendPullLocalBlocked(&res.LocalSafety, true, *action)
			qualificationErrs = append(qualificationErrs, targetErr)
			if o.Complete {
				break
			}
			continue
		}
		if local := qualified.byID[id]; local != nil {
			if _, localErr := revalidateConfluencePullLocal(m, local); localErr != nil {
				action := PullLocalAction{ID: id, Path: filepath.ToSlash(path), Status: pullLocalBlocked, Reason: "local_artifacts_changed"}
				appendPullLocalBlocked(&res.LocalSafety, true, action)
				qualificationErrs = append(qualificationErrs, localErr)
				if o.Complete {
					break
				}
				continue
			}
			if local.dirty && o.StashLocal {
				status = pullLocalWouldStash
			} else if local.dirty && o.OverwriteLocal {
				status = pullLocalWouldOverwrite
			}
		}
		res.Pages = append(res.Pages, PulledPage{ID: id, Title: page.Title, Path: filepath.ToSlash(path), Version: page.Version, Status: status})
	}
	if len(qualificationErrs) > 0 {
		return res, errors.Join(pullLocalSafetyError("confluence", res.LocalSafety), errors.Join(qualificationErrs...))
	}
	return res, nil
}

// prepareCompletePullDryRun reproduces complete selection/checkpoint binding
// without creating or replacing any durable checkpoint.
func (s *ConfluenceService) prepareCompletePullDryRun(ctx context.Context, m *mirror.Mirror, o PullOpts, rs RenderSettings) (*confluenceCompleteSelection, error) {
	selector, query, err := completePullSelector(o)
	if err != nil {
		return nil, err
	}
	selectorSHA256 := selectorHash(selector)
	optionsSHA256, err := completePullOptionsHash(s.cfg, o, rs)
	if err != nil {
		return nil, err
	}
	checkpoint, found, err := m.CompletePullCheckpoint(selectorSHA256)
	if err != nil {
		return nil, err
	}
	if found && !o.RestartComplete {
		if checkpoint.Service != confluenceCompletePullService || checkpoint.SelectorSHA256 != selectorSHA256 || checkpoint.OptionsSHA256 != optionsSHA256 {
			return nil, fmt.Errorf("%w: complete-pull checkpoint does not match this selector and its options", domain.ErrCheckFailed)
		}
		selectionSHA256, hashErr := confluenceCompleteHashJSON(checkpoint.IDs)
		if hashErr != nil || selectionSHA256 != checkpoint.SelectionSHA256 || !sort.StringsAreSorted(checkpoint.IDs) {
			return nil, fmt.Errorf("%w: complete-pull checkpoint selection identity is invalid", domain.ErrCheckFailed)
		}
		selection := newCompleteSelection(checkpoint, "resumed", 0)
		selection.result.CheckpointActive = true
		return selection, nil
	}
	search, err := completePullSearch(s.store)
	if err != nil {
		return nil, err
	}
	first, err := collectCompletePullIDsForSearch(ctx, search, query, o.MaxPages, completePullExpectedSpace(o))
	if err != nil {
		return nil, err
	}
	second, err := collectCompletePullIDsForSearch(ctx, search, query, o.MaxPages, completePullExpectedSpace(o))
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(first, second) {
		return nil, fmt.Errorf("%w: complete-pull selection changed during pagination; retry after the backend settles", domain.ErrCheckFailed)
	}
	selectionSHA256, err := confluenceCompleteHashJSON(second)
	if err != nil {
		return nil, err
	}
	checkpoint = mirror.CompletePullCheckpoint{
		Service: confluenceCompletePullService, SelectorSHA256: selectorSHA256, OptionsSHA256: optionsSHA256,
		SelectionSHA256: selectionSHA256, IDs: second, Includes: mirror.CompletePullIncludeProgress{EvidenceComplete: true},
	}
	source := "new"
	if found {
		source = "restarted"
	}
	selection := newCompleteSelection(checkpoint, source, 0)
	selection.result.CheckpointActive = false
	return selection, nil
}

// planConfluencePageRelocation reconstructs the exact recorded pristine view
// at a page's old tracked path. This keeps mirror's filesystem primitive
// backend-neutral while letting it reject both native and unapplied Markdown
// edits before a metadata-driven path change.
func planConfluencePageRelocation(m *mirror.Mirror, id, newRel string) (*mirror.PageRelocation, error) {
	st, ok, err := m.SyncStateOf(id)
	if err != nil || !ok || filepath.Clean(st.Path) == filepath.Clean(newRel) {
		return nil, err
	}
	oldCSF := filepath.Join(m.Root, filepath.FromSlash(st.Path))
	oldBase := strings.TrimSuffix(oldCSF, ".csf")
	for _, path := range []string{oldCSF, oldBase + ".md", oldBase + ".meta.json"} {
		if _, readErr := safepath.ReadFileWithin(m.Root, path); os.IsNotExist(readErr) {
			// Route every kind of absent primary artifact through the mirror's
			// complete three-file classifier. LoadCSF's metadata diagnostics are
			// intentionally formatted for users and need not preserve fs identity.
			return m.PlanPageRelocation(id, newRel, nil)
		}
	}
	lc, current, err := m.LoadCSF(oldCSF)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect tracked relocation source %s: %v", domain.ErrCheckFailed, oldCSF, err)
	}
	base, ok := m.BaseBody(id)
	if !ok || mirror.Hash(current) != mirror.Hash(base) {
		return nil, fmt.Errorf("%w: old tracked page %s has local native edits; apply/push or preserve them before re-pulling", domain.ErrCheckFailed, oldCSF)
	}
	view, hasView, err := m.ViewStateOf(id)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(oldCSF)
	slug := strings.TrimSuffix(filepath.Base(oldCSF), ".csf")
	md := []byte(mirror.MDUnavailableStub)
	views := confluencePristineViews{current: md, legacy: map[string][]byte{
		mirror.ConfluenceDocumentMarkerV5: bytes.Replace(md, []byte(mirror.ConfluenceDocumentMarker), []byte(mirror.ConfluenceDocumentMarkerV5), 1),
		mirror.ConfluenceDocumentMarkerV4: bytes.Replace(md, []byte(mirror.ConfluenceDocumentMarker), []byte(mirror.ConfluenceDocumentMarkerV4), 1),
	}}
	if node, parseErr := csf.Parse(base); parseErr == nil {
		opts := mirror.MDViewOpts{}
		legacyOpts := opts
		if hasView {
			comments, commentErr := readCommentsSidecar(m.Root, dir, slug, lc.Meta.ID, lc.Meta.Version)
			if commentErr != nil {
				return nil, fmt.Errorf("%w: Confluence comments sidecar cannot reproduce relocation source: %v", domain.ErrCheckFailed, commentErr)
			}
			renderSettings := settingsFromViewState(view)
			opts, err = confMDViewOptsFromSidecars(renderSettings, confPageFromMeta(lc.Meta), comments, m.Root, dir, slug, lc.Meta.ID, node)
			if err != nil {
				return nil, fmt.Errorf("%w: Jira macro enrichment sidecar cannot reproduce relocation source: %v; remove only the generated .jira-macros.json sidecar, then run `conf pull`", domain.ErrCheckFailed, err)
			}
			legacyOpts = legacyConfluenceCommentMDViewOpts(opts, renderSettings, comments)
		}
		views = renderConfluencePristineViews(node, lc.Meta.Refs, opts, legacyOpts)
		md = views.current
	}
	actualMD, err := safepath.ReadFileWithin(m.Root, oldBase+".md")
	if err != nil {
		return nil, fmt.Errorf("%w: inspect tracked relocation Markdown %s: %v", domain.ErrCheckFailed, oldBase+".md", err)
	}
	migrates, matchErr := matchConfluencePristineView(actualMD, views)
	if matchErr != nil {
		return nil, fmt.Errorf("%w: tracked relocation page %s %v", domain.ErrCheckFailed, id, matchErr)
	}
	if migrates {
		// The mirror primitive revalidates this exact legacy hash immediately
		// before retirement. The newly published path is still rendered current.
		md = actualMD
	}
	return m.PlanPageRelocation(id, newRel, md)
}

// resolveIDs returns the page ids a pull should mirror plus whether the
// selection was truncated by a cap (the --cql id cap or the space tree cap).
func (s *ConfluenceService) resolveIDs(ctx context.Context, o PullOpts) (context.Context, []string, bool, error) {
	switch {
	case o.ID != "":
		resolved, err := s.ResolvePageReference(ctx, o.ID)
		if err != nil {
			return ctx, nil, false, err
		}
		ctx = resolved.Context(ctx)
		return ctx, []string{resolved.ID}, false, nil
	case o.CQL != "":
		ids, truncated, err := s.collectSearch(ctx, o.CQL)
		return ctx, ids, truncated, err
	case o.Space != "":
		refs, truncated, err := s.store.Tree(ctx, o.Space, o.Depth)
		if err != nil {
			return ctx, nil, false, err
		}
		ids := make([]string, 0, len(refs))
		for _, r := range refs {
			ids = append(ids, r.ID)
		}
		return ctx, ids, truncated, nil
	default:
		return ctx, nil, false, fmt.Errorf("%w: pull needs --id, --cql or --space", domain.ErrUsage)
	}
}
