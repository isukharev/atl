package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

func (s *ConfluenceService) Get(ctx context.Context, id, format string) (*domain.Resource, error) {
	return s.store.GetPage(ctx, id, domain.PullOpts{Format: format})
}

func (s *ConfluenceService) Meta(ctx context.Context, id string) (*domain.PageMeta, error) {
	return s.store.GetMeta(ctx, id)
}

func (s *ConfluenceService) History(ctx context.Context, id string) ([]domain.Version, error) {
	return s.store.History(ctx, id)
}

func (s *ConfluenceService) Tree(ctx context.Context, space string, depth int) ([]domain.PageRef, bool, error) {
	return s.store.Tree(ctx, space, depth)
}

// Comments returns a page's comments and whether the listing was truncated by a
// safety cap (so the CLI can warn instead of presenting a silently-clipped set).
func (s *ConfluenceService) Comments(ctx context.Context, id string) ([]domain.Comment, bool, error) {
	return s.store.ListComments(ctx, id)
}

func (s *ConfluenceService) AddComment(ctx context.Context, id string, body []byte) (*domain.Comment, error) {
	return s.store.AddComment(ctx, id, body)
}

func (s *ConfluenceService) Attachments(ctx context.Context, id string) ([]domain.Attachment, error) {
	return s.store.ListAttachments(ctx, id)
}

func (s *ConfluenceService) Create(ctx context.Context, space, parent, title string, body []byte) (*domain.Resource, error) {
	return s.store.CreatePage(ctx, space, parent, title, body)
}

func (s *ConfluenceService) Move(ctx context.Context, id, parent string) error {
	return s.store.MovePage(ctx, id, parent)
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
	if space == "" {
		space = src.SpaceKey
	}
	if parent == "" {
		parent = src.Parent
	}
	return s.store.CreatePage(ctx, space, parent, newTitle, src.Body)
}

// DownloadAttachment streams a page attachment by filename into outDir (an
// atomic write: an interrupted transfer never leaves a truncated file).
// Returns the written file path.
func (s *ConfluenceService) DownloadAttachment(ctx context.Context, pageID, filename string, version int, outDir string) (string, error) {
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
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	if _, err := safepath.WriteReaderAtomic(p, rc, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// UploadAttachment reads file bytes from filePath and uploads them as an
// attachment to the given page.
func (s *ConfluenceService) UploadAttachment(ctx context.Context, pageID, filePath, comment string) (*domain.Attachment, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	filename := filepath.Base(filePath)
	return s.store.UploadAttachment(ctx, pageID, filename, data, comment)
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

// PullOpts selects what to mirror and where.
type PullOpts struct {
	ID       string
	CQL      string
	Space    string
	Depth    int
	Assets   bool
	Comments bool
	Into     string
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
	Root  string       `json:"root"`
	Pages []PulledPage `json:"pages"`
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
}

// Pull mirrors pages selected by id/cql/space into Into.
func (s *ConfluenceService) Pull(ctx context.Context, o PullOpts) (*PullResult, error) {
	root := o.Into
	if root == "" {
		root = "mirror"
	}
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		return nil, err
	}
	ids, truncated, err := s.resolveIDs(ctx, o)
	if err != nil {
		return nil, err
	}
	res := &PullResult{Root: root}
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
	for _, id := range ids {
		page, err := s.store.GetPage(ctx, id, domain.PullOpts{Format: "csf"})
		if err != nil {
			return res, fmt.Errorf("pull %s: %w", id, err)
		}
		dir, slug, derr := m.ClaimPageDir(page.SpaceKey, page.Ancestors, page.Title, page.ID)
		if derr != nil {
			return res, fmt.Errorf("pull %s: %w", id, derr)
		}
		refs := []domain.Ref{}
		if root, perr := csf.Parse(page.Body); perr == nil {
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
		if o.Comments {
			if err := batch.WriteComments(dir, slug, page, refs, comments, commentsTruncated); err != nil {
				return res, fmt.Errorf("write %s: %w", id, err)
			}
		} else if err := batch.Write(dir, slug, page, refs); err != nil {
			return res, fmt.Errorf("write %s: %w", id, err)
		}
		rel, _ := filepath.Rel(root, filepath.Join(dir, slug+".csf"))
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
	}
	if err := batch.Flush(); err != nil {
		return res, err
	}
	return res, nil
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
		return []string{o.ID}, false, nil
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
		if lc.Synced != nil {
			e.SyncedVersion = lc.Synced.Version
		}
		if checkRemote && lc.Meta.ID != "" {
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
	m := mirror.New(root)
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
	page, gerr := s.store.GetPage(ctx, lc.Meta.ID, domain.PullOpts{Format: "csf"})
	if gerr != nil {
		item.Warning = "pushed but local refresh failed (re-pull recommended): " + gerr.Error()
		return item, nil
	}
	dir := filepath.Dir(path)
	slug := strings.TrimSuffix(filepath.Base(path), ".csf")
	refs := []domain.Ref{}
	if r, perr := csf.Parse(page.Body); perr == nil {
		refs = fragment.Resolve(ctx, page, fragment.Extract(r), fragment.Deps{Users: s.users})
	}
	if werr := m.Write(dir, slug, page, refs); werr != nil {
		item.Warning = "pushed but local refresh failed (re-pull recommended): " + werr.Error()
	}
	return item, nil
}

func (s *ConfluenceService) pushTargets(m *mirror.Mirror, target string) ([]string, error) {
	info, err := os.Stat(target)
	if err != nil {
		// A bad push target is a usage error (exit 2), not a generic failure.
		return nil, fmt.Errorf("%w: push target %q: %v", domain.ErrUsage, target, err)
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
	return err == nil && !strings.HasPrefix(rel, "..")
}
