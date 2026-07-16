package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

// ---- search / get / meta / history / tree ----

func (s *ConfluenceService) Search(ctx context.Context, cql string, limit int, cursor string) ([]domain.PageRef, string, error) {
	return s.store.Search(ctx, cql, limit, cursor)
}

const confluenceSearchSchemaVersion = 1

// ConfluenceSearchResult qualifies one bounded CQL page. Complete is true only
// when the backend exposes enough pagination evidence to prove that no next
// page was omitted; callers must not interpret an empty result as absence when
// Complete is false.
type ConfluenceSearchResult struct {
	SchemaVersion int              `json:"schema_version"`
	Query         string           `json:"query"`
	Results       []domain.PageRef `json:"results"`
	Count         int              `json:"count"`
	Complete      bool             `json:"complete"`
	Truncated     bool             `json:"truncated"`
	PartialReason string           `json:"partial_reason,omitempty"`
	NextCursor    *string          `json:"next_cursor"`
}

func (s *ConfluenceService) SearchQualified(ctx context.Context, cql string, limit int, cursor string) (*ConfluenceSearchResult, error) {
	if searcher, ok := s.store.(domain.CompletePageSearcher); ok {
		page, err := searcher.SearchComplete(ctx, cql, limit, cursor)
		if err != nil {
			return nil, err
		}
		return newConfluenceSearchResult(cql, page), nil
	}

	results, next, err := s.store.Search(ctx, cql, limit, cursor)
	if err != nil {
		return nil, err
	}
	// The legacy port cannot prove terminal completeness: an empty next cursor
	// may mean exhaustion or a backend that silently omitted continuation data.
	page := domain.PageSearchPage{
		Results: results, Next: next, Complete: false,
		PartialReason: "backend search does not expose qualified pagination",
	}
	return newConfluenceSearchResult(cql, page), nil
}

func newConfluenceSearchResult(query string, page domain.PageSearchPage) *ConfluenceSearchResult {
	var next *string
	if page.Next != "" {
		value := page.Next
		next = &value
	}
	complete := page.Complete && page.Next == "" && page.PartialReason == ""
	partialReason := page.PartialReason
	if page.Complete && page.Next != "" && partialReason == "" {
		partialReason = "backend marked a page complete while providing a continuation cursor"
	}
	if !page.Complete && page.Next == "" && partialReason == "" {
		partialReason = "backend did not qualify terminal search completeness"
	}
	return &ConfluenceSearchResult{
		SchemaVersion: confluenceSearchSchemaVersion,
		Query:         query, Results: page.Results, Count: len(page.Results),
		Complete: complete, Truncated: !complete, PartialReason: partialReason, NextCursor: next,
	}
}

func (s *ConfluenceService) Get(ctx context.Context, id, format string) (*domain.Resource, error) {
	resolved, err := s.ResolvePageReference(ctx, id)
	if err != nil {
		return nil, err
	}
	id = resolved.ID
	page, err := s.store.GetPage(ctx, id, domain.PullOpts{Format: format})
	if err != nil {
		return nil, err
	}
	projection := "body.storage.value"
	bodyKind := "native body"
	if format == "view" {
		projection = "body.view.value"
		bodyKind = "rendered body"
	}
	if err := requireConfluenceBodyProjection(page, id, "page get", projection, bodyKind); err != nil {
		return nil, err
	}
	return page, nil
}

func (s *ConfluenceService) Meta(ctx context.Context, id string) (*domain.PageMeta, error) {
	resolved, err := s.ResolvePageReference(ctx, id)
	if err != nil {
		return nil, err
	}
	id = resolved.ID
	return s.store.GetMeta(ctx, id)
}

func (s *ConfluenceService) History(ctx context.Context, id string) ([]domain.Version, error) {
	resolved, err := s.ResolvePageReference(ctx, id)
	if err != nil {
		return nil, err
	}
	id = resolved.ID
	return s.store.History(ctx, id)
}

func (s *ConfluenceService) Tree(ctx context.Context, space string, depth int) ([]domain.PageRef, bool, error) {
	return s.store.Tree(ctx, space, depth)
}

// Comments returns a page's comments and whether the listing was truncated by a
// safety cap (so the CLI can warn instead of presenting a silently-clipped set).
func (s *ConfluenceService) Comments(ctx context.Context, id string) ([]domain.Comment, bool, error) {
	resolved, err := s.ResolvePageReference(ctx, id)
	if err != nil {
		return nil, false, err
	}
	id = resolved.ID
	return s.store.ListComments(ctx, id)
}

func (s *ConfluenceService) AddComment(ctx context.Context, id string, body []byte) (*domain.Comment, error) {
	return s.store.AddComment(ctx, id, body)
}

func (s *ConfluenceService) Attachments(ctx context.Context, id string) ([]domain.Attachment, error) {
	resolved, err := s.ResolvePageReference(ctx, id)
	if err != nil {
		return nil, err
	}
	id = resolved.ID
	return s.store.ListAttachments(ctx, id)
}

func (s *ConfluenceService) Create(ctx context.Context, space, parent, title string, body []byte) (*domain.Resource, error) {
	return s.store.CreatePage(ctx, space, parent, title, body)
}

func (s *ConfluenceService) Delete(ctx context.Context, id string) error {
	return s.store.DeletePage(ctx, id)
}

// CopyPage fetches the source page's native CSF body and creates a new page
// with the same body bytes under the target space/parent with a new title.
// If space or parent are empty, the source page's values are used as defaults.
func (s *ConfluenceService) CopyPage(ctx context.Context, srcID, newTitle, space, parent string) (*domain.Resource, error) {
	src, err := s.store.GetPage(ctx, srcID, domain.PullOpts{Format: "csf"})
	if err != nil {
		return nil, err
	}
	if err := requireConfluenceNativeBody(src, srcID, "copy"); err != nil {
		return nil, err
	}
	if space == "" {
		space = src.SpaceKey
	}
	if parent == "" {
		parent = src.Parent
	}
	return s.store.CreatePage(ctx, space, parent, newTitle, src.Body)
}

func requireConfluenceNativeBody(page *domain.Resource, id, operation string) error {
	return requireConfluenceBodyProjection(page, id, operation, "body.storage.value", "native body")
}

func requireConfluenceBodyProjection(page *domain.Resource, id, operation, projection, bodyKind string) error {
	if page == nil || !page.BodyPresent {
		return fmt.Errorf("%w: %s page %s response omitted %s; refusing to treat a partial projection as an empty %s", domain.ErrCheckFailed, operation, id, projection, bodyKind)
	}
	return nil
}

func localConfluenceTargetError(operation, target string, err error) error {
	if os.IsNotExist(err) {
		return fmt.Errorf("%w: %s target %q does not exist", domain.ErrNotFound, operation, target)
	}
	return fmt.Errorf("%w: inspect %s target %q: %v", domain.ErrCheckFailed, operation, target, err)
}

// DownloadAttachment streams a page attachment by filename into outDir (an
// atomic write: an interrupted transfer never leaves a truncated file).
// Returns the written file path.
func (s *ConfluenceService) DownloadAttachment(ctx context.Context, pageID, filename string, version int, outDir string) (string, error) {
	resolved, err := s.ResolvePageReference(ctx, pageID)
	if err != nil {
		return "", err
	}
	pageID = resolved.ID
	if outDir == "" {
		outDir = "."
	}
	safeName, ok := safepath.Base(filename)
	if !ok {
		return "", fmt.Errorf("%w: unsafe attachment filename %q", domain.ErrUsage, filename)
	}
	p := filepath.Join(outDir, safeName)
	if !safepath.Within(outDir, p) {
		return "", fmt.Errorf("%w: attachment path would escape output directory", domain.ErrUsage)
	}
	rc, err := s.store.DownloadAttachment(ctx, pageID, filename, version)
	if err != nil {
		return "", err // fail before MkdirAll: a 404 must not leave an empty outDir
	}
	defer rc.Close()
	if err := safepath.MkdirAllWithin(outDir, outDir, 0o755); err != nil {
		return "", err
	}
	if _, err := safepath.WriteReaderAtomicWithin(outDir, p, rc, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// UploadAttachment streams filePath as an attachment to the given page.
func (s *ConfluenceService) UploadAttachment(ctx context.Context, pageID, filePath, comment string) (*domain.Attachment, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	filename := filepath.Base(filePath)
	return s.store.UploadAttachment(ctx, pageID, filename, f, info.Size(), comment)
}

// DeleteAttachment removes an attachment by its content id.
func (s *ConfluenceService) DeleteAttachment(ctx context.Context, attachmentID string) error {
	return s.store.DeleteAttachment(ctx, attachmentID)
}

// Whoami returns the display name of the authenticated Confluence user.
func (s *ConfluenceService) Whoami(ctx context.Context) (string, error) {
	if s.verifier == nil {
		return "", fmt.Errorf("%w: whoami not supported by this store", domain.ErrConfig)
	}
	return s.verifier.Whoami(ctx)
}

// Validate parses CSF bytes and returns diagnostics.
func (s *ConfluenceService) Validate(body []byte) []csf.Problem {
	return csf.Validate(body)
}

// ---- pull ----

// PullOpts selects what to mirror and where. Render is the per-run flag override
// for the markdown view profile; a zero value leaves the effective settings
// (local + global config) untouched.
type PullOpts struct {
	ID          string
	CQL         string
	Space       string
	Depth       int
	Assets      bool
	Comments    bool
	Into        string
	Render      config.RenderService
	JiraView    string
	Incremental bool
	Complete    bool
	// RestartComplete explicitly replaces an unfinished complete-pull snapshot
	// after a fresh two-pass selection and local overwrite preflight succeed.
	RestartComplete   bool
	Since             string
	TimeZone          string
	MaxPages          int
	PagePrefetch      int
	RequestsPerSecond int
}

// PulledPage is one mirrored page.
type PulledPage struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Version int    `json:"version"`
	Assets  int    `json:"assets"`
	// Comments is the number of comments mirrored for this page. It is a pointer
	// so a --comments pull that found zero comments still emits `"comments": 0`,
	// distinguishable from a pull that never fetched them (field omitted).
	Comments *int `json:"comments,omitempty"`
}

// PullResult is the pull summary.
type PullResult struct {
	Root        string                 `json:"root"`
	Pages       []PulledPage           `json:"pages"`
	Incremental *IncrementalPullResult `json:"incremental,omitempty"`
	Complete    *CompletePullResult    `json:"complete_pull,omitempty"`
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
	// Warnings carries advisory render-resolution messages (unknown section names,
	// malformed local config). Not serialized (the pull JSON shape is unchanged by
	// profiles); the CLI prints it on stderr.
	Warnings   []string        `json:"-"`
	Scheduling *PullScheduling `json:"scheduling,omitempty"`
}

// PullScheduling reports the exact opt-in load policy. PagePrefetch overlaps
// native body GETs only; MaxInFlight and RequestsPerSecond cover every HTTP
// attempt made through the shared Confluence/Jira scheduler.
type PullScheduling struct {
	PagePrefetch      int `json:"page_prefetch"`
	MaxInFlight       int `json:"max_in_flight"`
	RequestsPerSecond int `json:"requests_per_second"`
}

// Pull mirrors pages selected by id/cql/space into Into.
func (s *ConfluenceService) Pull(ctx context.Context, o PullOpts) (result *PullResult, retErr error) {
	if o.PagePrefetch < 0 || o.PagePrefetch > 8 {
		return nil, fmt.Errorf("%w: --page-prefetch must be between 1 and 8", domain.ErrUsage)
	}
	if o.RequestsPerSecond < 0 || o.RequestsPerSecond > 1000 {
		return nil, fmt.Errorf("%w: --requests-per-second must be between 0 and 1000", domain.ErrUsage)
	}
	if !o.Incremental && !o.Complete && (o.PagePrefetch > 1 || o.RequestsPerSecond > 0) {
		return nil, fmt.Errorf("%w: request scheduling requires --incremental or --complete", domain.ErrUsage)
	}
	if (o.PagePrefetch > 1 || o.RequestsPerSecond > 0) &&
		(s.requestMaxInFlight != o.PagePrefetch || s.requestsPerSecond != o.RequestsPerSecond) {
		return nil, fmt.Errorf("%w: pull request schedule was not installed in the service transport", domain.ErrCheckFailed)
	}
	root := o.Into
	if root == "" {
		root = "mirror"
	}
	// Resolve presentation policy before backend reads or mirror writes. In
	// particular, jira_macros=off guarantees that this command never loads Jira
	// credentials or executes page-provided JQL.
	rs, warns := ResolveRender(s.cfg, root, o.Render, "confluence")
	if err := s.validateConfluenceJiraView(o.JiraView, rs.ExpandJiraMacros); err != nil {
		return nil, err
	}
	m := mirror.New(root)
	lock, err := lockConfluenceMutations(root, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	if err := m.EnsureScaffold(); err != nil {
		return nil, err
	}
	var incremental *confluenceIncrementalSelection
	var complete *confluenceCompleteSelection
	var ids []string
	var truncated bool
	if o.Incremental && o.Complete {
		return nil, fmt.Errorf("%w: --incremental and --complete are mutually exclusive", domain.ErrUsage)
	}
	if o.RestartComplete && !o.Complete {
		return nil, fmt.Errorf("%w: --restart-complete requires --complete", domain.ErrUsage)
	}
	if o.Incremental {
		if o.ID != "" {
			return nil, fmt.Errorf("%w: --incremental cannot be used with --id", domain.ErrUsage)
		}
		incremental, err = s.prepareIncrementalPull(ctx, m, o)
		if err != nil {
			return nil, err
		}
		ids = incremental.ids
		viewMigrations, preflightErr := preflightIncrementalOverwrite(m, ids)
		if preflightErr != nil {
			return nil, preflightErr
		}
		incremental.result.ViewMigrations = viewMigrations
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
		complete, err = s.prepareCompletePull(ctx, m, o, rs)
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
		ids, truncated, err = s.resolveIDs(ctx, o)
		if err != nil {
			return nil, err
		}
	}
	// Resolve the effective render settings for THIS mirror root (local config
	// lives under it). Default/minimal keep the body-only view byte-identical to
	// today; only `full` (or an explicit include) adds metadata/comments.
	res := &PullResult{Root: root, Warnings: warns}
	if o.Incremental || o.Complete {
		prefetch := o.PagePrefetch
		if prefetch == 0 {
			prefetch = 1
		}
		maxInFlight := s.requestMaxInFlight
		if maxInFlight == 0 {
			maxInFlight = 1
		}
		res.Scheduling = &PullScheduling{PagePrefetch: prefetch, MaxInFlight: maxInFlight, RequestsPerSecond: s.requestsPerSecond}
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
	// One sidecar load for the whole pull; one save at the end. The deferred
	// flush persists the pages already written when an error aborts the loop,
	// so a partial pull is not reported as never-synced (Flush is a no-op after
	// the explicit success-path call below).
	batch, err := m.BeginSync()
	if err != nil {
		return nil, err
	}
	defer func() { _ = batch.Flush() }()
	completeFinished := false
	completeRetireStarted := false
	if complete != nil {
		// Graceful failures durably record every page whose mirror sidecar commit
		// succeeds. A hard process crash may replay at most the small batch since
		// the last checkpoint, but can never skip an uncommitted page.
		defer func() {
			if completeFinished {
				return
			}
			if completeRetireStarted {
				retErr = fmt.Errorf("%w; complete-pull completion cleanup was interrupted — rerun the exact command to reconcile private resume state", retErr)
				return
			}
			if err := batch.Flush(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("save complete-pull mirror progress: %w", err))
			} else if complete.nextIndex > complete.savedIndex {
				if err := complete.save(m); err != nil {
					retErr = errors.Join(retErr, fmt.Errorf("save complete-pull checkpoint: %w", err))
				}
			}
			retErr = fmt.Errorf("%w; complete-pull checkpoint is at %d/%d — rerun the exact command to resume", retErr, complete.savedIndex, complete.result.Total)
		}()
	}
	macroOptOutWarned := false
	var prefetch *orderedPagePrefetch
	if o.PagePrefetch > 1 {
		prefetch = newOrderedPagePrefetch(ctx, s.store, ids, o.PagePrefetch, confluenceNeedsRestrictions(rs))
		defer prefetch.close()
	}
	for _, id := range ids {
		var page *domain.Resource
		if prefetch != nil {
			page, err = prefetch.nextPage(id)
		} else {
			page, err = s.store.GetPage(ctx, id, domain.PullOpts{Format: "csf", IncludeRestrictions: confluenceNeedsRestrictions(rs)})
		}
		if err != nil {
			return res, fmt.Errorf("pull %s: %w", id, err)
		}
		if err := requireConfluenceNativeBody(page, id, "pull"); err != nil {
			return res, err
		}
		dir, slug, derr := m.ClaimPageDir(page.SpaceKey, page.Ancestors, page.Title, page.ID)
		if derr != nil {
			return res, fmt.Errorf("pull %s: %w", id, derr)
		}
		rel, _ := filepath.Rel(root, filepath.Join(dir, slug+".csf"))
		relocation, rerr := planConfluencePageRelocation(m, page.ID, rel)
		if rerr != nil {
			return res, fmt.Errorf("pull %s: %w", id, rerr)
		}
		refs := []domain.Ref{}
		var pageNode *csf.Node
		if root, perr := csf.Parse(page.Body); perr == nil {
			pageNode = root
			refs = fragment.Extract(root)
			deps := fragment.Deps{Assets: m.AssetSink(dir, slug), Users: s.users}
			if o.Assets {
				deps.Resolver = s.assets
			}
			refs = fragment.Resolve(ctx, page, refs, deps)
		}
		page.Refs = refs
		// Comments are an opt-in include. Fetch before the write so their count and
		// truncation flag can be stamped into .meta.json in one pass. A fetch error
		// aborts the pull (the user explicitly asked for comments); a truncated
		// listing is surfaced, never silently clipped.
		var comments []domain.Comment
		var commentsTruncated bool
		if o.Comments {
			comments, commentsTruncated, err = s.store.ListComments(ctx, id)
			if err != nil {
				return res, fmt.Errorf("pull comments %s: %w", id, err)
			}
			if commentsTruncated {
				res.CommentsTruncated = true
			}
		}
		mdOpts := confMDViewOpts(rs, page, comments)
		var jiraMacros *confluenceJiraMacroSidecar
		if pageNode != nil && rs.ExpandJiraMacros {
			var macroWarnings []string
			jiraMacros, macroWarnings = s.resolveConfluenceJiraMacros(ctx, page.ID, pageNode, o.JiraView)
			res.Warnings = append(res.Warnings, macroWarnings...)
			mdOpts.JiraMacros = confluenceJiraMacroViews(jiraMacros)
		} else if pageNode != nil && len(mirror.JiraMacroDescriptors(pageNode)) > 0 && !macroOptOutWarned {
			res.Warnings = append(res.Warnings, "render: Jira query macro expansion is disabled; placeholders retained and no Jira request was made")
			macroOptOutWarned = true
		}
		if o.Comments {
			if err := batch.WriteComments(dir, slug, page, refs, comments, commentsTruncated, mdOpts); err != nil {
				return res, fmt.Errorf("write %s: %w", id, err)
			}
		} else if err := batch.WriteView(dir, slug, page, refs, mdOpts); err != nil {
			return res, fmt.Errorf("write %s: %w", id, err)
		}
		if err := writeConfluenceJiraMacroSidecar(root, dir, slug, jiraMacros); err != nil {
			return res, fmt.Errorf("write Jira macro sidecar %s: %w", id, err)
		}
		// Record the render settings this .md view was written with so `conf
		// apply` can reproduce the exact pristine view (metadata + comments
		// stay read-only) instead of guessing from the ambient config.
		batch.RecordView(page.ID, viewStateOf(rs))
		if relocation != nil {
			// Publish the new canonical state before retiring the old exact page
			// artifacts. A crash can therefore leave only an untracked stale copy,
			// never a sidecar that calls the stale path current.
			if err := batch.Flush(); err != nil {
				return res, err
			}
			if err := m.RetirePageRelocation(relocation); err != nil {
				return res, err
			}
		}
		assetCount := 0
		for _, r := range refs {
			if r.Asset != "" {
				assetCount++
			}
		}
		pp := PulledPage{ID: id, Title: page.Title, Path: rel, Version: page.Version, Assets: assetCount}
		if o.Comments {
			n := len(comments)
			pp.Comments = &n
		}
		res.Pages = append(res.Pages, pp)
		if complete != nil {
			if commentsTruncated {
				return res, fmt.Errorf("%w: complete-pull comments for page %s were truncated; checkpoint remains before this page", domain.ErrCheckFailed, id)
			}
			complete.advance()
			if complete.shouldCheckpoint() {
				if err := batch.Flush(); err != nil {
					return res, err
				}
				if err := complete.save(m); err != nil {
					return res, err
				}
			}
		}
	}
	if err := batch.Flush(); err != nil {
		return res, err
	}
	if incremental != nil {
		if res.CommentsTruncated {
			return res, fmt.Errorf("%w: incremental comments were truncated; watermark unchanged", domain.ErrCheckFailed)
		}
		if err := m.SaveIncrementalWatermark(incremental.next); err != nil {
			return res, err
		}
		res.Incremental.WatermarkAdvanced = incremental.changed
	}
	if complete != nil {
		if complete.nextIndex != len(complete.checkpoint.IDs) {
			return res, fmt.Errorf("%w: complete-pull progress ended before the exact selection was consumed", domain.ErrCheckFailed)
		}
		if err := complete.save(m); err != nil {
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
	return res, nil
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
	if node, parseErr := csf.Parse(base); parseErr == nil {
		opts := mirror.MDViewOpts{}
		if hasView {
			opts, err = confMDViewOptsFromSidecars(settingsFromViewState(view), confPageFromMeta(lc.Meta), readCommentsSidecar(m.Root, dir, slug), m.Root, dir, slug, lc.Meta.ID, node)
			if err != nil {
				return nil, fmt.Errorf("%w: Jira macro enrichment sidecar cannot reproduce relocation source: %v; remove only the generated .jira-macros.json sidecar, then run `conf pull`", domain.ErrCheckFailed, err)
			}
		}
		md = mirror.RenderMarkdownOpts(node, lc.Meta.Refs, opts)
	}
	actualMD, err := safepath.ReadFileWithin(m.Root, oldBase+".md")
	if err != nil {
		return nil, fmt.Errorf("%w: inspect tracked relocation Markdown %s: %v", domain.ErrCheckFailed, oldBase+".md", err)
	}
	migrates, matchErr := matchConfluencePristineView(actualMD, md)
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

// cqlPullCap bounds how many ids a `--cql` pull collects. Confluence offers no
// "unbounded" escape, so the loop stops here; the boolean returned alongside the
// ids lets the caller surface that a cap was hit instead of silently dropping
// the overflow.
const cqlPullCap = 1000

// resolveIDs returns the page ids a pull should mirror plus whether the
// selection was truncated by a cap (the --cql id cap or the space tree cap).
func (s *ConfluenceService) resolveIDs(ctx context.Context, o PullOpts) (ids []string, truncated bool, err error) {
	switch {
	case o.ID != "":
		resolved, err := s.ResolvePageReference(ctx, o.ID)
		if err != nil {
			return nil, false, err
		}
		return []string{resolved.ID}, false, nil
	case o.CQL != "":
		return s.collectSearch(ctx, o.CQL)
	case o.Space != "":
		refs, truncated, err := s.store.Tree(ctx, o.Space, o.Depth)
		if err != nil {
			return nil, false, err
		}
		ids := make([]string, 0, len(refs))
		for _, r := range refs {
			ids = append(ids, r.ID)
		}
		return ids, truncated, nil
	default:
		return nil, false, fmt.Errorf("%w: pull needs --id, --cql or --space", domain.ErrUsage)
	}
}

// collectSearch pages a CQL query into ids, stopping at cqlPullCap. truncated is
// true only when matches genuinely remain beyond the cap, so the caller can warn
// without crying wolf when the results happen to end exactly at the cap.
func (s *ConfluenceService) collectSearch(ctx context.Context, cql string) (ids []string, truncated bool, err error) {
	cursor := ""
	for len(ids) < cqlPullCap {
		hits, next, err := s.store.Search(ctx, cql, 100, cursor)
		if err != nil {
			return nil, false, err
		}
		for _, h := range hits {
			if h.ID != "" {
				ids = append(ids, h.ID)
			}
		}
		if next == "" || len(hits) == 0 {
			return ids, false, nil // backend exhausted at or under the cap
		}
		cursor = next
	}
	// Reached the cap. A dangling next cursor does not prove more matches exist
	// (the next page may be empty), so probe one row rather than warn falsely.
	hits, _, perr := s.store.Search(ctx, cql, 1, cursor)
	if perr != nil {
		// Don't fail the pull over a truncation probe; assume truncated so we
		// under-claim completeness rather than over-claim it.
		return ids, true, nil
	}
	return ids, len(hits) > 0, nil
}

// ---- status ----

// StatusEntry reports the sync state of one mirrored page.
type StatusEntry struct {
	Path          string `json:"path"`
	ID            string `json:"id"`
	Title         string `json:"title"`
	LocallyEdited bool   `json:"locally_edited"`
	SyncedVersion int    `json:"synced_version"`
	RemoteVersion int    `json:"remote_version,omitempty"`
	Drifted       bool   `json:"remote_drifted"`
	RemoteError   string `json:"remote_error,omitempty"`
	NonCanonical  bool   `json:"non_canonical,omitempty"`
	CanonicalPath string `json:"canonical_path,omitempty"`
}

// Status reports locally-edited and remote-drifted pages under dir.
func (s *ConfluenceService) Status(ctx context.Context, dir string, checkRemote bool) ([]StatusEntry, error) {
	if dir == "" {
		dir = "mirror"
	}
	m := mirror.New(dir)
	locals, err := m.ListCSF()
	if err != nil {
		return nil, err
	}
	var out []StatusEntry
	for _, lc := range locals {
		e := StatusEntry{Path: lc.Path, ID: lc.Meta.ID, Title: lc.Meta.Title, LocallyEdited: lc.Dirty}
		if lc.TrackedElsewhere {
			e.NonCanonical = true
			e.CanonicalPath = filepath.Join(m.Root, filepath.FromSlash(lc.CanonicalPath))
		}
		if lc.Synced != nil {
			e.SyncedVersion = lc.Synced.Version
		}
		if checkRemote && lc.Meta.ID != "" && !lc.TrackedElsewhere {
			// Record the reason a remote check failed (deleted/forbidden/network)
			// so a page that could not be checked is not silently reported as
			// in-sync — which would mislead a "safe to push?" decision.
			if meta, err := s.store.GetMeta(ctx, lc.Meta.ID); err == nil {
				e.RemoteVersion = meta.Version
				e.Drifted = e.SyncedVersion > 0 && meta.Version != e.SyncedVersion
			} else {
				e.RemoteError = failReason(err)
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// ---- push ----

// PushOpts controls a push.
type PushOpts struct {
	DryRun bool
	Force  bool
	Into   string // mirror root (for refresh-after-push)
}

// PushItem is the outcome for one file.
type PushItem struct {
	Path       string        `json:"path"`
	ID         string        `json:"id"`
	Problems   []csf.Problem `json:"problems,omitempty"`
	Removed    []domain.Ref  `json:"removed_fragments,omitempty"`
	Added      []domain.Ref  `json:"added_fragments,omitempty"`
	Pushed     bool          `json:"pushed"`
	DryRun     bool          `json:"dry_run,omitempty"`
	NewVersion int           `json:"new_version,omitempty"`
	Skipped    string        `json:"skipped,omitempty"`
	Drifted    bool          `json:"remote_drifted,omitempty"`
	Failed     string        `json:"failed,omitempty"`
	Warning    string        `json:"warning,omitempty"`
}

// PushResult aggregates per-file outcomes.
type PushResult struct {
	Items []PushItem `json:"items"`
}

// Push validates and pushes one .csf file or every dirty file under a dir. The
// optimistic version gate refuses on drift (exit 5) unless Force.
func (s *ConfluenceService) Push(ctx context.Context, target string, o PushOpts) (*PushResult, error) {
	root := o.Into
	if root == "" {
		root = mirrorRootOf(target)
	}
	if _, err := os.Stat(target); err != nil {
		return nil, localConfluenceTargetError("push", target, err)
	}
	m := mirror.New(root)
	lock, err := lockConfluenceMutations(root, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	files, err := s.pushTargets(m, target)
	if err != nil {
		return nil, err
	}
	res := &PushResult{}
	var worst error
	for _, f := range files {
		item, ferr := s.pushOne(ctx, m, f, o)
		res.Items = append(res.Items, item)
		// Keep the most actionable failure so a batch push surfaces a version
		// conflict (exit 5) rather than whichever file happens to sort first.
		worst = moreSevereErr(worst, ferr)
	}
	return res, worst
}

// errRank orders push failures by actionability so the aggregate exit code
// reflects the most useful one (version-conflict highest: it tells an agent to
// re-pull and retry). The rank is NOT the exit code: forbidden ranks below
// version-conflict here yet maps to exit 6, while version-conflict maps to 5 —
// the rank only decides which error wins; codeFor then maps the winner.
func errRank(err error) int {
	switch {
	case err == nil:
		return -1
	case errors.Is(err, domain.ErrCheckFailed):
		// Jira push's drift refusal (exit 8): "re-pull or --force" is the most
		// actionable outcome, so it wins a batch aggregate. Confluence push never
		// produces this per-file (a corrupt sidecar aborts before the loop).
		return 6
	case errors.Is(err, domain.ErrVersionConflict):
		return 5
	case errors.Is(err, domain.ErrForbidden):
		return 4
	case errors.Is(err, domain.ErrAuth):
		return 3
	case errors.Is(err, domain.ErrNotFound):
		return 2
	case errors.Is(err, domain.ErrUsage):
		return 1
	default:
		return 0
	}
}

func moreSevereErr(a, b error) error {
	if errRank(b) > errRank(a) {
		return b
	}
	return a
}

func failReason(err error) string {
	switch {
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden"
	case errors.Is(err, domain.ErrNotFound):
		return "not-found"
	case errors.Is(err, domain.ErrAuth):
		return "auth"
	case errors.Is(err, domain.ErrUsage):
		return "usage"
	default:
		return "error"
	}
}

func (s *ConfluenceService) pushOne(ctx context.Context, m *mirror.Mirror, path string, o PushOpts) (PushItem, error) {
	item := PushItem{Path: path, DryRun: o.DryRun}
	lc, body, err := m.LoadCSF(path)
	if err != nil {
		return item, err
	}
	item.ID = lc.Meta.ID
	if lc.TrackedElsewhere {
		item.Skipped = "non-canonical-path"
		return item, fmt.Errorf("%w: %s is a stale copy for page %s; canonical mirror path is %s — never push this copy, including with --force; reconcile or remove only the stale primary artifacts", domain.ErrCheckFailed, path, lc.Meta.ID, filepath.Join(m.Root, filepath.FromSlash(lc.CanonicalPath)))
	}
	// Block on malformed CSF.
	problems := csf.Validate(body)
	item.Problems = problems
	if csf.HasErrors(problems) {
		return item, fmt.Errorf("%s: malformed CSF (see problems)", path)
	}
	// Nothing to push if the file still matches its last-synced state (unless
	// forced): pushing an unchanged body would create a no-op remote revision.
	if !lc.Dirty && !o.Force {
		item.Skipped = "unchanged"
		return item, nil
	}
	// Consequence diff against the pristine base.
	if base, ok := m.BaseBody(lc.Meta.ID); ok {
		item.Removed, item.Added = diffFragments(base, body)
	}
	if o.DryRun {
		// Report whether a real push would be refused by the version gate, so the
		// consequence preview is not silently wrong about a drifted page.
		if lc.Synced != nil && lc.Meta.ID != "" {
			if meta, merr := s.store.GetMeta(ctx, lc.Meta.ID); merr == nil {
				if meta.Version != lc.Synced.Version {
					item.Drifted = true
					item.Warning = fmt.Sprintf("remote drifted to v%d (synced v%d); a real push would be refused (exit 5) without --force", meta.Version, lc.Synced.Version)
				}
			} else {
				// Be honest when drift could not be checked (mirrors `status`): a
				// failed probe must not read as "no drift" in the preview.
				item.Warning = "could not verify remote drift (" + failReason(merr) + "); a real push may still be refused by the version gate"
			}
		}
		return item, nil
	}
	if lc.Meta.ID == "" {
		return item, fmt.Errorf("%w: %s has no id (pull it first)", domain.ErrUsage, path)
	}
	expect := lc.Meta.Version
	if lc.Synced != nil {
		expect = lc.Synced.Version
	}
	newVer, err := s.store.UpdatePage(ctx, lc.Meta.ID, expect, lc.Meta.Title, body, o.Force)
	if err != nil {
		if errors.Is(err, domain.ErrVersionConflict) {
			item.Skipped = "version-conflict"
		} else {
			item.Failed = failReason(err)
		}
		return item, err
	}
	item.Pushed = true
	item.NewVersion = newVer
	// Refresh the mirror entry so base/version/hash track the new remote state.
	// If this fails the sidecar goes stale and the NEXT push could spuriously
	// report drift — surface it as a warning rather than swallowing it.
	refreshRS, _ := ResolveRender(s.cfg, m.Root, config.RenderService{}, "confluence")
	if view, ok, verr := m.ViewStateOf(lc.Meta.ID); verr == nil && ok {
		refreshRS = settingsFromViewState(view)
	}
	page, gerr := s.store.GetPage(ctx, lc.Meta.ID, domain.PullOpts{
		Format: "csf", IncludeRestrictions: confluenceNeedsRestrictions(refreshRS),
	})
	if gerr != nil {
		item.Warning = "pushed but local refresh failed (re-pull recommended): " + gerr.Error()
		return item, nil
	}
	if berr := requireConfluenceNativeBody(page, lc.Meta.ID, "post-push refresh"); berr != nil {
		item.Warning = "pushed but local refresh returned a partial body projection; local files were preserved (re-pull recommended)"
		return item, nil
	}
	item.Warning = appendWarning(item.Warning, s.refreshConfluenceMirror(ctx, m, lc, path, page, refreshRS, "pushed"))
	return item, nil
}

// refreshConfluenceMirror records one already-verified remote page through the
// same native/view/sidecar path used after push. Remote mutation has already
// succeeded, so failures are returned as warnings for the caller to surface;
// WriteView atomically advances the native/base/sync set, while a later
// render-view-state failure remains an explicit re-pull warning.
func (s *ConfluenceService) refreshConfluenceMirror(ctx context.Context, m *mirror.Mirror, lc *mirror.LocalCSF, path string, page *domain.Resource, refreshRS RenderSettings, verb string) string {
	var warning string
	dir := filepath.Dir(path)
	slug := strings.TrimSuffix(filepath.Base(path), ".csf")
	refs := []domain.Ref{}
	var pageNode *csf.Node
	if r, perr := csf.Parse(page.Body); perr == nil {
		pageNode = r
		refs = fragment.Resolve(ctx, page, fragment.Extract(r), fragment.Deps{Users: s.users})
	}
	// Keep the refreshed .md view consistent with the mirror's configured profile
	// (no per-run override on push): a full-profile mirror keeps its metadata/
	// comments after a push instead of reverting to the body-only default. Comments
	// are read from the existing sidecar (push does not fetch them).
	mdOpts := confMDViewOpts(refreshRS, page, readCommentsSidecar(m.Root, dir, slug))
	if pageNode != nil {
		if len(mirror.JiraMacroDescriptors(pageNode)) == 0 {
			sidecarPath := confluenceJiraMacroPath(dir, slug)
			if _, statErr := safepath.StatWithin(m.Root, sidecarPath); statErr == nil {
				if removeErr := writeConfluenceJiraMacroSidecar(m.Root, dir, slug, nil); removeErr != nil {
					return appendWarning(warning, verb+" but obsolete Jira macro view state could not be retired; local files were preserved: "+removeErr.Error())
				}
				warning = appendWarning(warning, verb+"; Jira query results were retired because the native macro set changed")
			} else if !os.IsNotExist(statErr) {
				return appendWarning(warning, verb+" but Jira macro view state could not be inspected; local files were preserved: "+statErr.Error())
			}
		}
		var sidecarErr error
		mdOpts, sidecarErr = confMDViewOptsFromSidecars(refreshRS, page, readCommentsSidecar(m.Root, dir, slug), m.Root, dir, slug, lc.Meta.ID, pageNode)
		if sidecarErr != nil {
			if errors.Is(sidecarErr, errStaleConfluenceJiraMacroSidecar) {
				if removeErr := writeConfluenceJiraMacroSidecar(m.Root, dir, slug, nil); removeErr != nil {
					return appendWarning(warning, verb+" but obsolete Jira macro view state could not be retired; local files were preserved (remove only the generated .jira-macros.json sidecar, then run `conf pull`): "+removeErr.Error())
				}
				mdOpts = confMDViewOpts(refreshRS, page, readCommentsSidecar(m.Root, dir, slug))
				warning = appendWarning(warning, verb+"; Jira query results were retired because the native macro set changed — re-pull to resolve current macros")
			} else {
				return appendWarning(warning, verb+" but Jira macro view state could not be reproduced; local files were preserved (remove only the generated .jira-macros.json sidecar, then run `conf pull`): "+sidecarErr.Error())
			}
		}
	}
	if werr := m.WriteView(dir, slug, page, refs, mdOpts); werr != nil {
		warning = appendWarning(warning, verb+" but local refresh failed (re-pull recommended): "+werr.Error())
	} else if verr := m.SaveViewStates(map[string]mirror.ViewState{lc.Meta.ID: viewStateOf(refreshRS)}); verr != nil {
		// Recording the view state is best-effort, like the refresh itself: the
		// push already succeeded, so a sidecar-record failure is a warning, not a
		// failed push.
		warning = appendWarning(warning, verb+" but view state could not be recorded (re-pull recommended): "+verr.Error())
	}
	return warning
}

func (s *ConfluenceService) pushTargets(m *mirror.Mirror, target string) ([]string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, localConfluenceTargetError("push", target, err)
	}
	if !info.IsDir() {
		return []string{target}, nil
	}
	locals, err := m.ListCSF()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, lc := range locals {
		// A directory push operates on the dirty set; --force overrides the version
		// gate for those files (see pushOne) but deliberately does not resurrect
		// locally-clean pages — that would create no-op revisions or revert remote
		// changes. Force a specific clean page by naming it as the target instead.
		if lc.Dirty && within(target, lc.Path) {
			files = append(files, lc.Path)
		}
	}
	return files, nil
}

// diffFragments compares fragments present in two bodies.
func diffFragments(oldBody, newBody []byte) (removed, added []domain.Ref) {
	oldRefs := extractSafe(oldBody)
	newRefs := extractSafe(newBody)
	key := func(r domain.Ref) string { return string(r.Kind) + "\x00" + r.Key }
	om := map[string]domain.Ref{}
	for _, r := range oldRefs {
		om[key(r)] = r
	}
	nm := map[string]bool{}
	for _, r := range newRefs {
		nm[key(r)] = true
	}
	// Iterate the ordered oldRefs (not the map) so removed_fragments is emitted
	// in a stable, document order across runs; dedup with seen.
	seen := map[string]bool{}
	for _, r := range oldRefs {
		k := key(r)
		if !nm[k] && !seen[k] {
			removed = append(removed, r)
			seen[k] = true
		}
	}
	for _, r := range newRefs {
		if _, ok := om[key(r)]; !ok {
			added = append(added, r)
		}
	}
	return removed, added
}

func extractSafe(body []byte) []domain.Ref {
	root, err := csf.Parse(body)
	if err != nil {
		return nil
	}
	return fragment.Extract(root)
}

func mirrorRootOf(target string) string {
	// Walk up to a directory containing .atl; fall back to "mirror".
	if root, ok := MirrorRootOf(target); ok {
		return root
	}
	return "mirror"
}

// MirrorRootOf walks up from target (a file or directory) up to 12 levels
// looking for an .atl marker dir, returning the mirror root and whether one was
// found. Callers that need to distinguish "no mirror here" from the "mirror"
// fallback (e.g. `config set --local`) use this directly.
func MirrorRootOf(target string) (string, bool) {
	dir := target
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		dir = filepath.Dir(target)
	}
	for i := 0; i < 12 && dir != "." && dir != "/" && dir != ""; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".atl")); err == nil {
			return dir, true
		}
		dir = filepath.Dir(dir)
	}
	return "", false
}

func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
