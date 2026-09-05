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
	ctx = resolved.Context(ctx)
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
	ctx = resolved.Context(ctx)
	id = resolved.ID
	return s.store.GetMeta(ctx, id)
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
	ctx = resolved.Context(ctx)
	id = resolved.ID
	return s.store.ListComments(ctx, id)
}

func (s *ConfluenceService) Attachments(ctx context.Context, id string) ([]domain.Attachment, error) {
	resolved, err := s.ResolvePageReference(ctx, id)
	if err != nil {
		return nil, err
	}
	ctx = resolved.Context(ctx)
	id = resolved.ID
	return s.store.ListAttachments(ctx, id)
}

func (s *ConfluenceService) Create(ctx context.Context, space, parent, title string, body []byte) (*domain.Resource, error) {
	return s.store.CreatePage(ctx, space, parent, title, body)
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
	if checkRemote {
		if err := requireMirrorBackend(dir, "confluence", s.baseURL); err != nil {
			return nil, err
		}
	}
	var bulkMetadata map[string]confluenceRemoteMetadataEvidence
	if checkRemote {
		ids := make([]string, 0, len(locals))
		for _, lc := range locals {
			if lc.Meta.ID != "" && !lc.TrackedElsewhere {
				ids = append(ids, lc.Meta.ID)
			}
		}
		if reader, ok := s.store.(domain.QualifiedConfluencePageMetadataBatchReader); ok && len(ids) > 1 {
			probeContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
			bulkMetadata = readConfluenceRemoteMetadataBatches(probeContext, reader, ids)
		}
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
			// Record a closed reason when remote evidence is unavailable so the
			// page is not silently reported as in-sync. Exact single-page reads
			// retain their typed reason; a batch failure stays deliberately coarse.
			if bulkMetadata != nil {
				evidence, ok := bulkMetadata[lc.Meta.ID]
				if ok && evidence.available {
					e.RemoteVersion = evidence.version
					e.Drifted = e.SyncedVersion > 0 && evidence.version != e.SyncedVersion
				} else {
					e.RemoteError = confluenceRemoteEvidenceIncomplete
					if ok && evidence.reason != "" {
						e.RemoteError = evidence.reason
					}
				}
			} else if meta, err := s.store.GetMeta(ctx, lc.Meta.ID); err == nil {
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

// PreflightConfluencePushCSF performs the narrow, filesystem-only part of a
// push that can be decided before backend configuration: error-severity CSF
// validation for otherwise readable, canonical push targets. A nil result and
// nil error means the caller must continue through the normal configured Push;
// it can mean either that every selected body passed or that target/mirror
// resolution must be left to Push so existing non-CSF error precedence is
// preserved. Push always repeats validation while holding the mutation lock.
func PreflightConfluencePushCSF(target string, o PushOpts) (*PushResult, error) {
	root := o.Into
	if root == "" {
		root = mirrorRootOf(target)
	}
	guard, err := beginMirrorSnapshotLock(root, filepath.Join(root, ".atl", confluenceMutationLockName))
	if err != nil {
		// Preserve the configured Push path's lock/config error precedence.
		return nil, nil
	}
	m := mirror.New(root)
	files, err := pushTargets(m, target)
	if err != nil {
		_, _ = guard.finish()
		return nil, nil
	}
	locals, bodies, err := m.LoadCSFMany(files)
	if err != nil {
		_, _ = guard.finish()
		return nil, nil
	}

	res := &PushResult{}
	for i, body := range bodies {
		// The locked push has a stronger, path-specific refusal for stale
		// relocation copies. Do not let body validation replace that error.
		if locals[i].TrackedElsewhere {
			continue
		}
		problems := csf.Validate(body)
		if !csf.HasErrors(problems) {
			continue
		}
		res.Items = append(res.Items, PushItem{
			Path: files[i], ID: locals[i].Meta.ID, Problems: problems, DryRun: o.DryRun,
		})
	}
	retry, finishErr := guard.finish()
	if retry || finishErr != nil {
		// A legacy mirror may create its persistent lock while this read-only
		// inspection is running. Discard that snapshot and let locked Push
		// resolve the authoritative error instead.
		return nil, nil
	}
	if len(res.Items) == 0 {
		return nil, nil
	}
	if len(res.Items) == 1 {
		return res, fmt.Errorf("%w: %s: malformed CSF (see problems)", domain.ErrCheckFailed, res.Items[0].Path)
	}
	return res, fmt.Errorf("%w: malformed CSF in %d push targets (see problems)", domain.ErrCheckFailed, len(res.Items))
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
	files, err := pushTargets(m, target)
	if err != nil {
		return nil, err
	}
	if err := requireMirrorBackend(root, "confluence", s.baseURL); err != nil {
		return nil, err
	}
	res := &PushResult{}
	var worst error
	for _, f := range files {
		item, ferr := s.pushOne(ctx, m, f, o)
		res.Items = append(res.Items, item)
		// Keep the most actionable failure according to errRank rather than
		// whichever file happens to sort first.
		worst = moreSevereErr(worst, ferr)
	}
	return res, worst
}

// errRank orders push failures by actionability so the aggregate exit code
// reflects the most useful one. Local check failures rank highest because the
// batch is unsafe to continue; among backend failures, version conflict remains
// the highest. The rank is NOT the exit code: it only decides which error wins;
// codeFor then maps the winner.
func errRank(err error) int {
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if errors.As(err, &ambiguous) && ambiguous.DiagnosticAmbiguousWrite() {
		return 7
	}
	switch {
	case err == nil:
		return -1
	case errors.Is(err, domain.ErrCheckFailed):
		// A failed local safety check (including malformed CSF) is the most
		// actionable outcome, so it wins a batch aggregate.
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
		return item, fmt.Errorf("%w: %s: malformed CSF (see problems)", domain.ErrCheckFailed, path)
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
	newVer, err := s.updateConfluencePush(ctx, lc.Meta.ID, expect, lc.Meta.Title, body, o.Force)
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
	if warning := confluencePushRefreshWarning(page, lc.Meta.ID, newVer, body); warning != "" {
		item.Warning = warning
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
	comments, commentErr := readCommentsSidecar(m.Root, dir, slug, page.ID, page.Version)
	if commentErr != nil {
		warning = appendWarning(warning, verb+"; existing comment view is stale or unreadable and was omitted — re-pull with --comments to refresh it")
		comments = confluenceCommentsView{}
	}
	mdOpts := confMDViewOptsForCommentsView(refreshRS, page, comments)
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
		mdOpts, sidecarErr = confMDViewOptsFromSidecars(refreshRS, page, comments, m.Root, dir, slug, lc.Meta.ID, pageNode)
		if sidecarErr != nil {
			if errors.Is(sidecarErr, errStaleConfluenceJiraMacroSidecar) {
				if removeErr := writeConfluenceJiraMacroSidecar(m.Root, dir, slug, nil); removeErr != nil {
					return appendWarning(warning, verb+" but obsolete Jira macro view state could not be retired; local files were preserved (remove only the generated .jira-macros.json sidecar, then run `conf pull`): "+removeErr.Error())
				}
				mdOpts = confMDViewOptsForCommentsView(refreshRS, page, comments)
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

func pushTargets(m *mirror.Mirror, target string) ([]string, error) {
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
