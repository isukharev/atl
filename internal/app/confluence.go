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

func (s *ConfluenceService) Tree(ctx context.Context, space string, depth int) ([]domain.PageRef, error) {
	return s.store.Tree(ctx, space, depth)
}

func (s *ConfluenceService) Comments(ctx context.Context, id string) ([]domain.Comment, error) {
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

// Validate parses CSF bytes and returns diagnostics.
func (s *ConfluenceService) Validate(body []byte) []csf.Problem {
	return csf.Validate(body)
}

// ---- pull ----

// PullOpts selects what to mirror and where.
type PullOpts struct {
	ID     string
	CQL    string
	Space  string
	Depth  int
	Assets bool
	Into   string
}

// PulledPage is one mirrored page.
type PulledPage struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Version int    `json:"version"`
	Assets  int    `json:"assets"`
}

// PullResult is the pull summary.
type PullResult struct {
	Root  string       `json:"root"`
	Pages []PulledPage `json:"pages"`
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
	ids, err := s.resolveIDs(ctx, o)
	if err != nil {
		return nil, err
	}
	res := &PullResult{Root: root}
	for _, id := range ids {
		page, err := s.store.GetPage(ctx, id, domain.PullOpts{Format: "csf"})
		if err != nil {
			return res, fmt.Errorf("pull %s: %w", id, err)
		}
		dir, slug := m.PageDir(page.SpaceKey, page.Ancestors, page.Title)
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
		if err := m.Write(dir, slug, page, refs); err != nil {
			return res, fmt.Errorf("write %s: %w", id, err)
		}
		rel, _ := filepath.Rel(root, filepath.Join(dir, slug+".csf"))
		assetCount := 0
		for _, r := range refs {
			if r.Asset != "" {
				assetCount++
			}
		}
		res.Pages = append(res.Pages, PulledPage{ID: id, Title: page.Title, Path: rel, Version: page.Version, Assets: assetCount})
	}
	return res, nil
}

func (s *ConfluenceService) resolveIDs(ctx context.Context, o PullOpts) ([]string, error) {
	switch {
	case o.ID != "":
		return []string{o.ID}, nil
	case o.CQL != "":
		return s.collectSearch(ctx, o.CQL)
	case o.Space != "":
		refs, err := s.store.Tree(ctx, o.Space, o.Depth)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(refs))
		for _, r := range refs {
			ids = append(ids, r.ID)
		}
		return ids, nil
	default:
		return nil, fmt.Errorf("%w: pull needs --id, --cql or --space", domain.ErrUsage)
	}
}

func (s *ConfluenceService) collectSearch(ctx context.Context, cql string) ([]string, error) {
	var ids []string
	cursor := ""
	for len(ids) < 1000 {
		hits, next, err := s.store.Search(ctx, cql, 100, cursor)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			if h.ID != "" {
				ids = append(ids, h.ID)
			}
		}
		if next == "" || len(hits) == 0 {
			break
		}
		cursor = next
	}
	return ids, nil
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
			if meta, err := s.store.GetMeta(ctx, lc.Meta.ID); err == nil {
				e.RemoteVersion = meta.Version
				e.Drifted = e.SyncedVersion > 0 && meta.Version != e.SyncedVersion
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
		if ferr != nil && worst == nil {
			worst = ferr
		}
	}
	return res, worst
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
	// Consequence diff against the pristine base.
	if base, ok := m.BaseBody(lc.Meta.ID); ok {
		item.Removed, item.Added = diffFragments(base, body)
	}
	if o.DryRun {
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
		return nil, err
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
	for k, r := range om {
		if !nm[k] {
			removed = append(removed, r)
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
	dir := target
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		dir = filepath.Dir(target)
	}
	for i := 0; i < 12 && dir != "." && dir != "/" && dir != ""; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".atl")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return "mirror"
}

func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !strings.HasPrefix(rel, "..")
}
