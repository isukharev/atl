// Package mirror owns the on-disk git-style mirror: layout, the markdown
// read-view, content hashing, the last-synced sidecar, and dirty/drift
// detection. It is backend-agnostic — it stores domain.Resource bytes and does
// not know whether they are Confluence pages or Jira issues.
package mirror

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// MDUnavailableStub replaces the .md read-view when a body fails to parse: a
// stale render from a previous revision must never sit next to a newer .csf,
// silently contradicting the source of truth. Exported so apply can uphold
// the same invariant after a merge.
const (
	ConfluenceDocumentMarkerV4   = "<!-- atl:document confluence-page v4 -->"
	ConfluenceDocumentMarkerV5   = "<!-- atl:document confluence-page v5 -->"
	ConfluenceDocumentMarker     = "<!-- atl:document confluence-page v6 -->"
	ConfluencePageFieldsMarker   = "<!-- atl:section page-fields readonly -->"
	ConfluenceBodyMarker         = "<!-- atl:section body editable -->"
	ConfluenceBodyReadOnlyMarker = "<!-- atl:section body readonly -->"
	ConfluenceCommentsMarker     = "<!-- atl:section comments readonly -->"
	ConfluenceJiraMacrosMarker   = "<!-- atl:section jira-macros readonly -->"
	ConfluenceReservedPrefix     = "<!-- atl:"
	MDUnavailableStub            = ConfluenceDocumentMarker + "\n" + ConfluenceBodyReadOnlyMarker + "\n# Content\n\n<!-- atl: markdown view unavailable for this revision (the .csf did not parse); the .csf file is the source of truth -->\n"
)

// IsSupportedLegacyConfluenceDocumentMarker reports the exact historical
// derived-view markers that this binary can reconstruct before a guarded
// migration to the current format.
func IsSupportedLegacyConfluenceDocumentMarker(marker string) bool {
	return marker == ConfluenceDocumentMarkerV4 || marker == ConfluenceDocumentMarkerV5
}

// IsFutureConfluenceDocumentMarker distinguishes a marker produced by a newer
// atl from historical formats this binary intentionally refuses to guess.
func IsFutureConfluenceDocumentMarker(marker string) bool {
	version := confluenceDocumentMarkerVersion(marker)
	current := confluenceDocumentMarkerVersion(ConfluenceDocumentMarker)
	return version > 0 && current > 0 && version > current
}

func confluenceDocumentMarkerVersion(marker string) int {
	const prefix = "<!-- atl:document confluence-page v"
	const suffix = " -->"
	if !strings.HasPrefix(marker, prefix) || !strings.HasSuffix(marker, suffix) {
		return 0
	}
	version, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(marker, prefix), suffix))
	if err != nil {
		return 0
	}
	return version
}

// ConfluenceDocumentMarkerLine returns only the first marker line and removes
// a CR attached to its line ending. All remaining Markdown bytes stay
// significant; callers must not treat this as whole-document normalization.
func ConfluenceDocumentMarkerLine(document string) string {
	first, _, _ := strings.Cut(document, "\n")
	return strings.TrimSuffix(first, "\r")
}

// Mirror is rooted at a directory holding one or more spaces.
type Mirror struct {
	Root string
}

func New(root string) *Mirror { return &Mirror{Root: root} }

// Meta is the per-page sidecar visible to the agent.
type Meta struct {
	ID         string       `json:"id"`
	Title      string       `json:"title"`
	Space      string       `json:"space"`
	Version    int          `json:"version"`
	Hash       string       `json:"content_hash"`
	Parent     string       `json:"parent,omitempty"`
	Ancestors  []string     `json:"ancestors,omitempty"`
	Labels     []string     `json:"labels,omitempty"`
	Updated    string       `json:"updated,omitempty"`
	Restricted *bool        `json:"restricted,omitempty"`
	URL        string       `json:"url,omitempty"`
	Refs       []domain.Ref `json:"fragments,omitempty"`
	// Comment summary fields are populated only by a
	// `pull --comments` (they surface comment presence to a slim .meta.json
	// read). CommentsPulled is the explicit "comments were fetched" marker, so a
	// page whose fetch returned zero comments (comment_count omitted at 0) is
	// still distinguishable from a page whose comments were never pulled. They
	// are auxiliary read-only data and never enter the content hash or the
	// version gate. All omitempty so a pull without --comments leaves the shape
	// unchanged.
	CommentsPulled         bool     `json:"comments_pulled,omitempty"`
	CommentCount           int      `json:"comment_count,omitempty"`
	CommentsTruncated      bool     `json:"comments_truncated,omitempty"`
	CommentSidecarVersion  int      `json:"comment_sidecar_version,omitempty"`
	CommentRootCount       int      `json:"comment_root_count,omitempty"`
	CommentsComplete       *bool    `json:"comments_complete,omitempty"`
	CommentThreadsComplete *bool    `json:"comment_threads_complete,omitempty"`
	CommentAnchorsComplete *bool    `json:"comment_anchors_complete,omitempty"`
	CommentPartialReasons  []string `json:"comment_partial_reasons,omitempty"`
	OpenInlineCommentCount int      `json:"open_inline_comment_count,omitempty"`
}

// Hash returns the canonical content hash of a body (sha256 hex of raw bytes).
func Hash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// PageDir computes the directory for a page: root/SPACE/<anc…>/<ownSlug>.
// ancestors are ancestor titles top→down (may be empty). It is pure layout
// computation and collision-blind — writers must resolve the directory through
// ClaimPageDir so a lossy slug can never overwrite a different page's files.
func (m *Mirror) PageDir(space string, ancestors []string, title string) (dir, slug string) {
	slug = slugify(title)
	return filepath.Join(m.pageParent(space, ancestors), slug), slug
}

// pageParent joins the sanitized space key and slugified ancestor titles into
// the directory that holds a page's own slug dir.
func (m *Mirror) pageParent(space string, ancestors []string) string {
	parts := []string{m.Root, safeSeg(space)}
	for _, a := range ancestors {
		parts = append(parts, slugify(a))
	}
	return filepath.Join(parts...)
}

// ClaimPageDir resolves the directory a page's files may be written to.
// Slugification is lossy — distinct sibling titles can collide ("Foo Bar" vs
// "Foo-Bar?") — so before handing out the computed dir it checks who already
// owns it via the existing <slug>.meta.json. A free dir or one owned by the
// same id is claimed as-is; one owned by a different page (or holding page
// files whose id cannot be read) makes the newcomer fall back to an id-suffixed
// slug, stable across re-pulls. If even that dir belongs to someone else, the
// claim fails loudly (ErrCheckFailed) rather than overwrite files.
func (m *Mirror) ClaimPageDir(space string, ancestors []string, title, id string) (dir, slug string, err error) {
	parent := m.pageParent(space, ancestors)
	slug = slugify(title)
	dir = filepath.Join(parent, slug)
	// A previously diverted page keeps its id-suffixed dir even after the plain
	// dir frees up — otherwise a re-pull would migrate it back and orphan the
	// suffixed copy, forking one page into two on-disk dirs.
	if id != "" {
		sslug := slug + "-" + slugify(id)
		sdir := filepath.Join(parent, sslug)
		if owner, occupied := m.pageOwner(sdir, sslug); occupied && owner == id {
			return sdir, sslug, nil
		}
	}
	owner, occupied := m.pageOwner(dir, slug)
	if !occupied || (id != "" && owner == id) {
		return dir, slug, nil
	}
	if id == "" {
		return "", "", fmt.Errorf("%w: mirror dir %s already holds another page and this page has no id to disambiguate with", domain.ErrCheckFailed, dir)
	}
	// id is server-controlled: slugify reduces it to a separator-free token so
	// the suffixed slug stays a single path component.
	slug = slug + "-" + slugify(id)
	dir = filepath.Join(parent, slug)
	if owner, occupied := m.pageOwner(dir, slug); occupied && owner != id {
		return "", "", fmt.Errorf("%w: mirror slug collision: refusing to overwrite %s, which belongs to a different page (title %q, id %s)", domain.ErrCheckFailed, dir, title, id)
	}
	return dir, slug, nil
}

// pageOwner reports whether dir already holds a page's files and, when
// readable, the owning page id. occupied is true when a <slug>.meta.json or
// <slug>.csf exists; owner is "" when the id could not be read (absent or
// corrupt meta) — callers must then treat the dir as foreign, never as free.
func (m *Mirror) pageOwner(dir, slug string) (owner string, occupied bool) {
	if mb, err := safepath.ReadFileWithin(m.Root, filepath.Join(dir, slug+".meta.json")); err == nil {
		var meta Meta
		if json.Unmarshal(mb, &meta) == nil && meta.ID != "" {
			return meta.ID, true
		}
		return "", true
	} else if !os.IsNotExist(err) {
		return "", true
	}
	if _, err := safepath.ReadFileWithin(m.Root, filepath.Join(dir, slug+".csf")); err == nil {
		return "", true
	} else if !os.IsNotExist(err) {
		return "", true
	}
	if rb, err := safepath.ReadFileWithin(m.Root, filepath.Join(dir, slug+".relocated.json")); err == nil {
		var marker relocationTombstone
		if json.Unmarshal(rb, &marker) == nil && marker.ID != "" {
			return marker.ID, true
		}
		return "", true
	} else if !os.IsNotExist(err) {
		return "", true
	}
	return "", false
}

// pageSink writes assets under <dir>/<slug>.assets/ and returns paths relative
// to the page dir (so .md links resolve).
type pageSink struct {
	root string
	dir  string
	slug string
}

func (s pageSink) Put(name string, data []byte) (string, error) {
	// name is a backend-supplied attachment filename: reduce it to a single safe
	// base name and refuse anything with no usable basename ("." / "..").
	safe, ok := safepath.Base(name)
	if !ok {
		return "", fmt.Errorf("refusing unsafe asset name %q", name)
	}
	adir := filepath.Join(s.dir, s.slug+".assets")
	if err := safepath.MkdirAllWithin(s.root, adir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(adir, safe)
	if !safepath.Within(adir, target) {
		return "", fmt.Errorf("refusing unsafe asset path %q", name)
	}
	if err := safepath.WriteFileWithin(s.root, target, data, 0o644); err != nil {
		return "", err
	}
	return s.slug + ".assets/" + safe, nil
}

// AssetSink returns the asset sink for a page directory.
func (m *Mirror) AssetSink(dir, slug string) domain.AssetSink {
	return pageSink{root: m.Root, dir: dir, slug: slug}
}

// Write persists a page: .csf (source of truth), .md (read view), .meta.json,
// and updates the sidecar. refs must already be resolved (display/asset filled).
// It is the single-page convenience over SyncBatch — a multi-page pull must go
// through BeginSync/Flush so the sidecar is loaded and saved once, not once
// per page.
func (m *Mirror) Write(dir, slug string, page *domain.Resource, refs []domain.Ref) error {
	return m.WriteView(dir, slug, page, refs, MDViewOpts{})
}

// WriteView is Write with an explicit markdown-view profile.
func (m *Mirror) WriteView(dir, slug string, page *domain.Resource, refs []domain.Ref, mdOpts MDViewOpts) error {
	b, err := m.BeginSync()
	if err != nil {
		return err
	}
	if err := b.WriteView(dir, slug, page, refs, mdOpts); err != nil {
		return err
	}
	return b.Flush()
}

// commentSidecar carries a page's comments for a pull that requested them (nil
// when --comments was off). It is auxiliary read-only data: the comment bytes
// never enter the content hash, are never copied to .atl/base/, and never affect
// dirty/drift/push gating — only the two sidecar files and Meta summaries.
type commentSidecar struct {
	encoded   []byte
	display   []domain.Comment
	v2        *ConfluenceCommentsSidecarV2
	truncated bool
}

// writePageFiles writes the page artifacts (.csf, .md view, .meta.json, base
// copy) and returns the .csf path relative to the mirror root; sidecar state
// is recorded by the caller. When cs is non-nil it also writes the comment
// sidecar files and stamps the comment counters into .meta.json.
func (m *Mirror) writePageFiles(dir, slug string, page *domain.Resource, refs []domain.Ref, cs *commentSidecar, mdOpts MDViewOpts) (rel string, err error) {
	if err := safepath.MkdirAllWithin(m.Root, dir, 0o755); err != nil {
		return "", err
	}
	csfPath := filepath.Join(dir, slug+".csf")
	if err := safepath.WriteFileWithin(m.Root, csfPath, page.Body, 0o644); err != nil {
		return "", err
	}
	// Markdown view — best-effort by contract: a render or write failure never
	// fails a pull. The view must also never contradict the source of truth, so
	// an unparseable body overwrites any previous revision's .md with a stub,
	// and a failed write falls back to removing the stale file. mdOpts carries the
	// profile-driven generated metadata/comments additions; a zero value renders exactly
	// the pre-profile body-only view.
	mdPath := filepath.Join(dir, slug+".md")
	md := []byte(MDUnavailableStub)
	if root, err := csf.Parse(page.Body); err == nil {
		md = RenderMarkdownOpts(root, refs, mdOpts)
	}
	if err := safepath.WriteFileWithin(m.Root, mdPath, md, 0o644); err != nil {
		_ = safepath.RemoveWithin(m.Root, mdPath)
	}
	meta := Meta{
		ID: page.ID, Title: page.Title, Space: page.SpaceKey, Version: page.Version,
		Hash: Hash(page.Body), Parent: page.Parent, Ancestors: page.Ancestors,
		Labels: page.Labels, Updated: page.Updated, Restricted: page.Restricted, Refs: refs,
	}
	// Comment sidecars are written before the meta so a mid-write failure never
	// leaves a meta claiming a comment count with no files behind it. The bytes
	// are pure read-view data: Hash above is over page.Body alone, so drift/push
	// gating is unaffected.
	if cs != nil {
		if err := m.writeCommentSidecar(dir, slug, cs.encoded, cs.display, mdOpts.CommentView); err != nil {
			return "", err
		}
		meta.CommentsPulled = true
		meta.CommentCount = len(cs.display)
		meta.CommentsTruncated = cs.truncated
		if cs.v2 != nil {
			meta.CommentSidecarVersion = cs.v2.SchemaVersion
			meta.CommentCount = cs.v2.Count
			meta.CommentRootCount = cs.v2.RootCount
			meta.CommentsComplete = boolPointer(cs.v2.CommentsComplete)
			meta.CommentThreadsComplete = boolPointer(cs.v2.ThreadsComplete)
			meta.CommentAnchorsComplete = boolPointer(cs.v2.AnchorsComplete)
			meta.CommentPartialReasons = append([]string(nil), cs.v2.PartialReasons...)
			for _, comment := range cs.v2.Comments {
				if comment.Location == domain.ConfluenceCommentLocationInline && comment.Resolution == domain.ConfluenceCommentResolutionOpen {
					meta.OpenInlineCommentCount++
				}
			}
		}
	}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := safepath.WriteFileWithin(m.Root, filepath.Join(dir, slug+".meta.json"), append(mb, '\n'), 0o644); err != nil {
		return "", err
	}
	if err := m.saveBase(page.ID, page.Body); err != nil {
		return "", err
	}
	rel, _ = filepath.Rel(m.Root, csfPath)
	return rel, nil
}

// writeCommentSidecar writes the two per-page comment artifacts next to the page
// files: <slug>.comments.json (primary; schema v2 for current pulls, with the
// legacy flat array retained only by the compatibility helper) and
// <slug>.comments.md (a derived human read view). The .md is purely derived
// from the JSON and is not part of any parity contract. Neither file feeds the
// content hash or .atl/base/.
func (m *Mirror) writeCommentSidecar(dir, slug string, encoded []byte, comments, displayComments []domain.Comment) error {
	if err := safepath.MkdirAllWithin(m.Root, dir, 0o755); err != nil {
		return err
	}
	if err := safepath.WriteFileWithin(m.Root, filepath.Join(dir, slug+".comments.json"), encoded, 0o644); err != nil {
		return err
	}
	if displayComments == nil {
		displayComments = comments
	}
	mdPath := filepath.Join(dir, slug+".comments.md")
	if err := safepath.WriteFileWithin(m.Root, mdPath, RenderCommentsMarkdown(displayComments), 0o644); err != nil {
		_ = safepath.RemoveWithin(m.Root, mdPath)
	}
	return nil
}

func boolPointer(value bool) *bool { return &value }

// RenderCommentsMarkdown renders a complete readonly comments view. Native
// Confluence storage bodies retain paragraphs, lists, links and headings; the
// plain Body field remains a fallback for legacy sidecars and other backends.
func RenderCommentsMarkdown(comments []domain.Comment) []byte {
	return renderCommentsMarkdownVersion(comments, confluenceMarkdownCurrent)
}

func renderCommentsMarkdownVersion(comments []domain.Comment, format confluenceMarkdownFormat) []byte {
	var b strings.Builder
	b.WriteString("# Comments\n\n")
	for _, c := range comments {
		fmt.Fprintf(&b, "## Comment by %s", pageSectionValue(c.Author))
		if created := pageSectionValue(c.Created); created != "" {
			fmt.Fprintf(&b, " (%s)", created)
		}
		b.WriteString("\n\n")
		body := strings.TrimSpace(c.Body)
		if c.BodyStorage != "" {
			if root, err := csf.Parse([]byte(c.BodyStorage)); err == nil {
				r := newMDRendererOffsetVersion(nil, 2, format)
				body = strings.TrimSpace(renderCommentMarkdownWithRenderer(root, r))
			}
		}
		if body != "" {
			b.WriteString(body)
			b.WriteString("\n\n")
		}
	}
	return []byte(b.String())
}

// SyncBatch accumulates sidecar updates across a multi-page write so a pull
// performs one sidecar load (BeginSync) and one save (Flush) instead of a
// full load+rewrite per page.
type SyncBatch struct {
	m           *Mirror
	sc          sidecarFile
	dirtyPages  map[string]SyncState
	dirtyViews  map[string]ViewState
	dirtyStaged map[string]*StagedState
}

// BeginSync loads the sidecar once for a batch of page writes. See saveSidecar
// for the concurrency discipline (single writer per mirror).
func (m *Mirror) BeginSync() (*SyncBatch, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return nil, err
	}
	return &SyncBatch{
		m: m, sc: sc, dirtyPages: map[string]SyncState{}, dirtyViews: map[string]ViewState{}, dirtyStaged: map[string]*StagedState{},
	}, nil
}

// Write persists a page like Mirror.Write but records the sync state in
// memory; the caller must Flush once at the end of the batch. The .md view is the
// default body-only render.
func (b *SyncBatch) Write(dir, slug string, page *domain.Resource, refs []domain.Ref) error {
	return b.write(dir, slug, page, refs, nil, MDViewOpts{})
}

// WriteView is Write with explicit markdown-view additions (metadata, comments).
func (b *SyncBatch) WriteView(dir, slug string, page *domain.Resource, refs []domain.Ref, mdOpts MDViewOpts) error {
	return b.write(dir, slug, page, refs, nil, mdOpts)
}

// WriteComments persists a page plus its comment sidecars (`pull --comments`).
// The comment bytes are auxiliary: the recorded sync state below hashes
// page.Body alone, so a page carrying comments sidecars still reads as Clean.
// mdOpts drives whether the .md view embeds a "# Comments" section (full
// profile) or leaves comments in the sidecar only (default profile).
func (b *SyncBatch) WriteComments(dir, slug string, page *domain.Resource, refs []domain.Ref, comments []domain.Comment, truncated bool, mdOpts MDViewOpts) error {
	list := comments
	if list == nil {
		list = []domain.Comment{}
	}
	encoded, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return b.write(dir, slug, page, refs, &commentSidecar{encoded: append(encoded, '\n'), display: comments, truncated: truncated}, mdOpts)
}

// WriteConfluenceComments persists a page with the authoritative qualified
// schema-v2 comment sidecar. Encoding and validation happen before any page
// artifact is overwritten. display is only the temporary flat derived-view
// projection and never replaces the v2 source of truth.
func (b *SyncBatch) WriteConfluenceComments(dir, slug string, page *domain.Resource, refs []domain.Ref, sidecar ConfluenceCommentsSidecarV2, display []domain.Comment, truncated bool, mdOpts MDViewOpts) error {
	encoded, err := EncodeConfluenceCommentsSidecarV2(sidecar)
	if err != nil {
		return err
	}
	decoded, err := DecodeConfluenceCommentsSidecar(encoded)
	if err != nil || decoded.V2 == nil {
		return fmt.Errorf("%w: canonical Confluence comments sidecar could not be decoded", domain.ErrCheckFailed)
	}
	canonical := *decoded.V2
	display = orderCommentProjection(canonical.Comments, display)
	mdOpts.Comments = orderCommentProjection(canonical.Comments, mdOpts.Comments)
	mdOpts.CommentView = orderCommentProjection(canonical.Comments, mdOpts.CommentView)
	if mdOpts.QualifiedComments != nil {
		displayCanonical, displayErr := qualifiedCommentsDisplayTimes(canonical, *mdOpts.QualifiedComments)
		if displayErr != nil {
			return displayErr
		}
		mdOpts.Comments = nil
		mdOpts.QualifiedComments = &displayCanonical
	}
	return b.write(dir, slug, page, refs, &commentSidecar{
		encoded: encoded, display: display, v2: &canonical, truncated: truncated,
	}, mdOpts)
}

// qualifiedCommentsDisplayTimes retains canonical sidecar ordering and content
// while applying the app-layer display timezone to temporal labels in the
// derived Markdown view. The authoritative JSON bytes stay untouched.
func qualifiedCommentsDisplayTimes(canonical, display ConfluenceCommentsSidecarV2) (ConfluenceCommentsSidecarV2, error) {
	if display.PageID != canonical.PageID || display.PageVersion != canonical.PageVersion || len(display.Comments) != len(canonical.Comments) {
		return ConfluenceCommentsSidecarV2{}, fmt.Errorf("%w: qualified Confluence comment view does not match its canonical sidecar", domain.ErrCheckFailed)
	}
	times := make(map[string][2]string, len(display.Comments))
	for _, comment := range display.Comments {
		if _, duplicate := times[comment.ID]; duplicate {
			return ConfluenceCommentsSidecarV2{}, fmt.Errorf("%w: qualified Confluence comment view contains duplicate identities", domain.ErrCheckFailed)
		}
		times[comment.ID] = [2]string{comment.CreatedAt, comment.UpdatedAt}
	}
	out := canonicalConfluenceCommentsSidecarV2(canonical)
	for i := range out.Comments {
		displayTimes, ok := times[out.Comments[i].ID]
		if !ok {
			return ConfluenceCommentsSidecarV2{}, fmt.Errorf("%w: qualified Confluence comment view identity does not match its canonical sidecar", domain.ErrCheckFailed)
		}
		out.Comments[i].CreatedAt = displayTimes[0]
		out.Comments[i].UpdatedAt = displayTimes[1]
	}
	return out, nil
}

func orderCommentProjection(order []ConfluenceCommentsSidecarComment, comments []domain.Comment) []domain.Comment {
	if comments == nil {
		return nil
	}
	byID := make(map[string]domain.Comment, len(comments))
	for _, comment := range comments {
		byID[comment.ID] = comment
	}
	out := make([]domain.Comment, 0, len(comments))
	for _, record := range order {
		if comment, exists := byID[record.ID]; exists {
			out = append(out, comment)
			delete(byID, record.ID)
		}
	}
	// Validation normally makes this empty. Preserve any compatibility-only
	// projection rows deterministically instead of silently dropping them.
	remaining := make([]domain.Comment, 0, len(byID))
	for _, comment := range byID {
		remaining = append(remaining, comment)
	}
	sort.Slice(remaining, func(i, j int) bool { return remaining[i].ID < remaining[j].ID })
	return append(out, remaining...)
}

func (b *SyncBatch) write(dir, slug string, page *domain.Resource, refs []domain.Ref, cs *commentSidecar, mdOpts MDViewOpts) error {
	rel, err := b.m.writePageFiles(dir, slug, page, refs, cs, mdOpts)
	if err != nil {
		return err
	}
	state := SyncState{ID: page.ID, Version: page.Version, Hash: Hash(page.Body), Path: rel}
	b.sc.Pages[page.ID] = state
	b.dirtyPages[page.ID] = state
	b.dirtyStaged[page.ID] = nil
	return nil
}

// Record adds a sidecar sync-state entry for a resource whose substrate files
// the caller wrote itself (e.g. Jira's <KEY>.wiki), so a backend that does not
// go through writePageFiles can still share the batch's single sidecar
// load/save. The pristine base copy is the caller's responsibility (SaveBaseExt).
func (b *SyncBatch) Record(st SyncState) {
	b.sc.Pages[st.ID] = st
	b.dirtyPages[st.ID] = st
	b.dirtyStaged[st.ID] = nil
}

// RecordView records the render settings a resource's .md view was written with,
// keyed by the same id as Record (page id / issue key), so apply can later
// reproduce the exact pristine view. Flushed with the rest of the batch.
func (b *SyncBatch) RecordView(id string, vs ViewState) {
	if b.sc.Views == nil {
		b.sc.Views = map[string]ViewState{}
	}
	b.sc.Views[id] = vs
	b.dirtyViews[id] = vs
}

// Flush saves the accumulated sidecar state; a no-op when nothing was written,
// so it is safe to call again on error paths after a successful flush.
func (b *SyncBatch) Flush() error {
	if len(b.dirtyPages) == 0 && len(b.dirtyViews) == 0 && len(b.dirtyStaged) == 0 {
		return nil
	}
	if err := b.m.mergeSidecarPatch(b.dirtyPages, b.dirtyViews, b.dirtyStaged); err != nil {
		return err
	}
	clear(b.dirtyPages)
	clear(b.dirtyViews)
	clear(b.dirtyStaged)
	return nil
}

// EnsureScaffold writes a .gitignore guarding secrets in the mirror root.
func (m *Mirror) EnsureScaffold() error {
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return err
	}
	gi := filepath.Join(m.Root, ".gitignore")
	if _, err := os.Stat(gi); os.IsNotExist(err) {
		_ = safepath.WriteFileWithin(m.Root, gi, []byte("# atl mirror — never commit secrets\n.atl/\ncredentials.json\n*.pat\n"), 0o644)
	}
	return nil
}

// LocalCSF describes a tracked .csf file and its expected (last-synced) state.
type LocalCSF struct {
	Path             string // absolute path to the .csf
	Meta             Meta
	Synced           *SyncState // last-synced state at this exact path (nil if untracked)
	Current          string     // current on-disk content hash
	Dirty            bool       // current != synced
	TrackedElsewhere bool       // same id has a different canonical sidecar path
	CanonicalPath    string     // canonical path relative to mirror root
}

// ReadSnapshot is an immutable view of one decoded mirror sidecar. Native
// bodies and neighboring metadata are deliberately not retained: LoadCSF
// reads one selected page on demand, keeping whole-mirror inspections bounded
// by the size of one page rather than the sum of every page body.
//
// Callers that cross a mutation or final-validation boundary must open a new
// snapshot so they observe the latest committed sidecar state.
type ReadSnapshot struct {
	root string
	sc   sidecarFile
}

// BeginReadSnapshot decodes state.json exactly once for a streaming read
// phase. The returned snapshot exposes no mutable sidecar maps.
func (m *Mirror) BeginReadSnapshot() (*ReadSnapshot, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return nil, err
	}
	return &ReadSnapshot{root: m.Root, sc: sc}, nil
}

// LoadCSF reads one native page and its metadata against the captured sidecar.
func (s *ReadSnapshot) LoadCSF(csfPath string) (*LocalCSF, []byte, error) {
	return loadCSFWith(s.root, s.sc, csfPath)
}

// ViewStateOf returns a defensive copy of one captured render state.
func (s *ReadSnapshot) ViewStateOf(id string) (ViewState, bool) {
	state, ok := s.sc.Views[id]
	if !ok {
		return ViewState{}, false
	}
	state.Sections = append([]string(nil), state.Sections...)
	state.CustomFields = append([]string(nil), state.CustomFields...)
	state.FieldViews = append([]FieldViewState(nil), state.FieldViews...)
	state.PageFields = append([]FieldViewState(nil), state.PageFields...)
	return state, true
}

// SyncStates returns deterministic copies of every state captured by this
// snapshot. Consumers must still filter by substrate extension.
func (s *ReadSnapshot) SyncStates() []SyncState {
	out := make([]SyncState, 0, len(s.sc.Pages))
	for _, state := range s.sc.Pages {
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ListCSF streams every securely inventoried native page against the captured
// sidecar and retains metadata only. It preserves ListCSF's deterministic order
// and fail-on-first-unreadable contract without repeatedly decoding state.json.
func (s *ReadSnapshot) ListCSF() ([]*LocalCSF, error) {
	m := New(s.root)
	paths, err := m.ListCSFPaths()
	if err != nil {
		return nil, err
	}
	out := make([]*LocalCSF, 0, len(paths))
	for _, path := range paths {
		local, _, loadErr := s.LoadCSF(path)
		if loadErr != nil {
			return nil, fmt.Errorf("load mirror page %s: %w", path, loadErr)
		}
		out = append(out, local)
	}
	return out, nil
}

// LoadCSF reads a .csf path and its neighboring meta + sidecar state. A
// corrupt sidecar is an error — reporting the page as never-synced instead
// would silently disable the version gate and drift detection.
func (m *Mirror) LoadCSF(csfPath string) (*LocalCSF, []byte, error) {
	snapshot, err := m.BeginReadSnapshot()
	if err != nil {
		return nil, nil, err
	}
	return snapshot.LoadCSF(csfPath)
}

// LoadCSFWithinLimit is the allocation-bounded form used by safety-sensitive
// local preflights. It reads one byte past max and fails rather than accepting
// a truncated native body.
func (m *Mirror) LoadCSFWithinLimit(csfPath string, max int64) (*LocalCSF, []byte, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return nil, nil, err
	}
	return loadCSFWithReader(m.Root, sc, csfPath, func(root, path string) ([]byte, error) {
		return safepath.ReadFileWithinLimit(root, path, max)
	})
}

// LoadCSFMany loads an exact caller-selected set against one sidecar snapshot.
// It preserves input order and fails on the first unreadable entry. Batch
// preflights use it to avoid repeatedly decoding a large shared state.json
// while still inspecting no file outside their reviewed set.
func (m *Mirror) LoadCSFMany(paths []string) ([]*LocalCSF, [][]byte, error) {
	snapshot, err := m.BeginReadSnapshot()
	if err != nil {
		return nil, nil, err
	}
	locals := make([]*LocalCSF, 0, len(paths))
	bodies := make([][]byte, 0, len(paths))
	for _, path := range paths {
		local, body, err := snapshot.LoadCSF(path)
		if err != nil {
			return nil, nil, fmt.Errorf("load mirror page %s: %w", path, err)
		}
		locals = append(locals, local)
		bodies = append(bodies, body)
	}
	return locals, bodies, nil
}

// loadCSFWith is LoadCSF against an already-loaded sidecar, so ListCSF can
// load the sidecar once instead of once per file.
func loadCSFWith(root string, sc sidecarFile, csfPath string) (*LocalCSF, []byte, error) {
	return loadCSFWithReader(root, sc, csfPath, safepath.ReadFileWithin)
}

func loadCSFWithReader(root string, sc sidecarFile, csfPath string, read func(string, string) ([]byte, error)) (*LocalCSF, []byte, error) {
	body, err := read(root, csfPath)
	if err != nil {
		return nil, nil, err
	}
	lc := &LocalCSF{Path: csfPath, Current: Hash(body)}
	metaPath := strings.TrimSuffix(csfPath, ".csf") + ".meta.json"
	mb, err := safepath.ReadFileWithin(root, metaPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read metadata %s: %v", domain.ErrCheckFailed, metaPath, err)
	}
	if err := json.Unmarshal(mb, &lc.Meta); err != nil {
		return nil, nil, fmt.Errorf("%w: parse metadata %s: %v", domain.ErrCheckFailed, metaPath, err)
	}
	if lc.Meta.ID == "" {
		return nil, nil, fmt.Errorf("%w: metadata %s has no page id", domain.ErrCheckFailed, metaPath)
	}
	if st, ok := sc.Pages[lc.Meta.ID]; ok {
		if sameTrackedPath(root, csfPath, st.Path) {
			s := st
			lc.Synced = &s
			lc.Dirty = s.Hash != lc.Current
		} else {
			lc.TrackedElsewhere = true
			lc.CanonicalPath = st.Path
			lc.Dirty = true
		}
	} else {
		lc.Dirty = true // untracked / never synced
	}
	return lc, body, nil
}

func sameTrackedPath(root, absolute, trackedRel string) bool {
	rel, err := filepath.Rel(root, absolute)
	return err == nil && filepath.Clean(rel) == filepath.Clean(filepath.FromSlash(trackedRel))
}

// ListCSF walks the mirror returning every tracked .csf with dirty status.
func (m *Mirror) ListCSF() ([]*LocalCSF, error) {
	snapshot, err := m.BeginReadSnapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.ListCSF()
}

// ListCSFPaths securely inventories native Confluence substrate paths without
// reading their metadata. Integrity-sensitive batch analysis can then classify
// a corrupt page explicitly while still failing closed on traversal errors or
// descendant symlinks. Paths are sorted and `.atl` is never visited.
func (m *Mirror) ListCSFPaths() ([]string, error) {
	walkRoot, err := filepath.EvalSymlinks(m.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve mirror root %s: %w", m.Root, err)
	}
	var out []string
	err = filepath.Walk(walkRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walk mirror at %s: %w", p, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing descendant symlink in mirror: %s", p)
		}
		if info.IsDir() {
			if info.Name() == ".atl" {
				return filepath.SkipDir // sidecar (pristine base copies) is not user content
			}
			return nil
		}
		if strings.HasSuffix(p, ".csf") {
			logicalPath, mapErr := logicalWalkPath(m.Root, walkRoot, p)
			if mapErr != nil {
				return mapErr
			}
			out = append(out, logicalPath)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// LocalWiki describes a tracked `.wiki` substrate file (the Jira analog of
// LocalCSF) and its expected (last-synced) sidecar state. The sidecar is keyed
// by the issue key, which is the file's basename — there is no neighboring
// meta.json, so Key is derived from the path rather than read from disk.
type LocalWiki struct {
	Path             string     // absolute path to the .wiki
	Key              string     // issue key (basename minus ".wiki") = sidecar key
	Synced           *SyncState // last-synced state at this exact path (nil if untracked)
	Current          string     // current on-disk content hash
	Dirty            bool       // current != synced (untracked reads as dirty)
	TrackedElsewhere bool       // same key has a different canonical sidecar path
	CanonicalPath    string     // canonical path relative to mirror root
}

// LoadWiki reads a `.wiki` path and its sidecar sync state. A corrupt sidecar is
// an error — reporting the issue as never-synced would silently disable drift
// detection, exactly as for LoadCSF.
func (m *Mirror) LoadWiki(wikiPath string) (*LocalWiki, []byte, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return nil, nil, err
	}
	return loadWikiWith(m.Root, sc, wikiPath)
}

// LoadWikiWithinLimit is the allocation-bounded form used by reconcile and
// other integrity-sensitive local inspections.
func (m *Mirror) LoadWikiWithinLimit(wikiPath string, max int64) (*LocalWiki, []byte, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return nil, nil, err
	}
	return loadWikiWithReader(m.Root, sc, wikiPath, func(root, path string) ([]byte, error) {
		return safepath.ReadFileWithinLimit(root, path, max)
	})
}

// loadWikiWith is LoadWiki against an already-loaded sidecar, so ListWiki can
// load the sidecar once instead of once per file.
func loadWikiWith(root string, sc sidecarFile, wikiPath string) (*LocalWiki, []byte, error) {
	return loadWikiWithReader(root, sc, wikiPath, safepath.ReadFileWithin)
}

func loadWikiWithReader(root string, sc sidecarFile, wikiPath string, read func(string, string) ([]byte, error)) (*LocalWiki, []byte, error) {
	body, err := read(root, wikiPath)
	if err != nil {
		return nil, nil, err
	}
	key := strings.TrimSuffix(filepath.Base(wikiPath), ".wiki")
	lw := &LocalWiki{Path: wikiPath, Key: key, Current: Hash(body)}
	if st, ok := sc.Pages[key]; ok {
		if sameTrackedPath(root, wikiPath, st.Path) {
			s := st
			lw.Synced = &s
			lw.Dirty = s.Hash != lw.Current
		} else {
			lw.TrackedElsewhere = true
			lw.CanonicalPath = st.Path
			lw.Dirty = true
		}
	} else {
		lw.Dirty = true // untracked / never synced
	}
	return lw, body, nil
}

// ListWiki walks the mirror returning every tracked `.wiki` with dirty status.
func (m *Mirror) ListWiki() ([]*LocalWiki, error) {
	sc, err := m.loadSidecar()
	if err != nil {
		return nil, err
	}
	walkRoot, err := filepath.EvalSymlinks(m.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve mirror root %s: %w", m.Root, err)
	}
	var out []*LocalWiki
	err = filepath.Walk(walkRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walk mirror at %s: %w", p, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing descendant symlink in mirror: %s", p)
		}
		if info.IsDir() {
			if info.Name() == ".atl" {
				return filepath.SkipDir // sidecar (pristine base copies) is not user content
			}
			return nil
		}
		if strings.HasSuffix(p, ".wiki") {
			logicalPath, mapErr := logicalWalkPath(m.Root, walkRoot, p)
			if mapErr != nil {
				return mapErr
			}
			lw, _, loadErr := loadWikiWith(m.Root, sc, logicalPath)
			if loadErr != nil {
				return fmt.Errorf("load mirror issue %s: %w", logicalPath, loadErr)
			}
			out = append(out, lw)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

func logicalWalkPath(logicalRoot, walkRoot, path string) (string, error) {
	rel, err := filepath.Rel(walkRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("map mirror path %s to root %s", path, logicalRoot)
	}
	return filepath.Join(logicalRoot, rel), nil
}

// slugify keeps unicode letters/digits (Cyrillic included), lowercases, and
// turns everything else into single hyphens.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteRune('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "untitled"
	}
	// Truncate by runes, not bytes, so multibyte (e.g. Cyrillic) titles are
	// never split mid-character.
	if r := []rune(out); len(r) > 80 {
		out = strings.Trim(string(r[:80]), "-")
	}
	return out
}

// safeSeg sanitizes a single path segment (space key) without lowercasing. It
// neutralizes separators and "." / ".." and escapes dot-prefixed names so a
// hostile server space key (including the reserved ".atl") can neither traverse
// upward nor collide with the mirror's internal state directory.
func safeSeg(s string) string {
	return safepath.Segment(s)
}
