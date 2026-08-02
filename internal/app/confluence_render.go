package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

func renderConfluenceMarkdownV4(root *csf.Node, refs []domain.Ref, opts mirror.MDViewOpts) []byte {
	v5 := mirror.RenderMarkdownOptsV5(root, refs, opts)
	return bytes.Replace(v5, []byte(mirror.ConfluenceDocumentMarkerV5), []byte(mirror.ConfluenceDocumentMarkerV4), 1)
}

func renderConfluencePristineViews(root *csf.Node, refs []domain.Ref, currentOpts, v4Opts mirror.MDViewOpts) confluencePristineViews {
	v5 := mirror.RenderMarkdownOptsV5(root, refs, currentOpts)
	return confluencePristineViews{
		current: mirror.RenderMarkdownOpts(root, refs, currentOpts),
		legacy: map[string][]byte{
			mirror.ConfluenceDocumentMarkerV5: v5,
			mirror.ConfluenceDocumentMarkerV4: renderConfluenceMarkdownV4(root, refs, v4Opts),
		},
	}
}

// confMDViewOpts assembles the profile-driven markdown-view additions for a
// Confluence page from the resolved settings: typed read-only page fields and a
// "# Comments" section fed from whatever comments the caller has
// (the just-fetched set on pull, or the sidecar on render/push). An empty return
// yields the byte-identical body-only default view.
func confMDViewOpts(rs RenderSettings, page *domain.Resource, comments []domain.Comment) mirror.MDViewOpts {
	var opts mirror.MDViewOpts
	if rs.On(SecPageFields) {
		views := rs.PageFields
		if len(views) == 0 {
			views = defaultConfluencePageFields()
		}
		for _, view := range views {
			values := confluencePageFieldValues(page, view, rs.DisplayTimeZone)
			if len(values) == 0 && !view.ShowEmpty {
				continue
			}
			opts.PageFields = append(opts.PageFields, mirror.PageField{
				ID: view.ID, Label: view.Label, Placement: view.Placement,
				Values: values, ShowEmpty: view.ShowEmpty,
			})
		}
	}
	displayComments := confluenceCommentsForDisplay(comments, rs.DisplayTimeZone)
	if len(displayComments) > 0 {
		opts.CommentView = displayComments
	}
	if rs.On(SecComments) && len(displayComments) > 0 {
		opts.Comments = displayComments
	}
	return opts
}

type confluenceCommentsView struct {
	flat      []domain.Comment
	qualified *mirror.ConfluenceCommentsSidecarV2
}

func confMDViewOptsForCommentsView(rs RenderSettings, page *domain.Resource, view confluenceCommentsView) mirror.MDViewOpts {
	display := view.forDisplay(rs.DisplayTimeZone)
	opts := confMDViewOpts(rs, page, display.flat)
	if rs.On(SecComments) && display.qualified != nil {
		opts.Comments = nil
		opts.QualifiedComments = display.qualified
	}
	return opts
}

func legacyConfluenceCommentMDViewOpts(current mirror.MDViewOpts, rs RenderSettings, view confluenceCommentsView) mirror.MDViewOpts {
	legacy := current
	legacy.QualifiedComments = nil
	legacy.Comments = nil
	if rs.On(SecComments) {
		legacy.Comments = view.forDisplay(rs.DisplayTimeZone).flat
	}
	return legacy
}

func (view confluenceCommentsView) forDisplay(displayTimeZone string) confluenceCommentsView {
	view.flat = confluenceCommentsForDisplay(view.flat, displayTimeZone)
	if view.qualified == nil {
		return view
	}
	qualified := *view.qualified
	qualified.PartialReasons = append([]string{}, view.qualified.PartialReasons...)
	qualified.Diagnostics = append([]mirror.ConfluenceCommentsSidecarDiagnostic{}, view.qualified.Diagnostics...)
	qualified.Comments = append([]mirror.ConfluenceCommentsSidecarComment{}, view.qualified.Comments...)
	if displayTimeZone != "" {
		for i := range qualified.Comments {
			qualified.Comments[i].CreatedAt = renderTemporalFieldIn(qualified.Comments[i].CreatedAt, "datetime", displayTimeZone)
			qualified.Comments[i].UpdatedAt = renderTemporalFieldIn(qualified.Comments[i].UpdatedAt, "datetime", displayTimeZone)
		}
	}
	view.qualified = &qualified
	return view
}

func defaultConfluencePageFields() []config.ConfluenceFieldView {
	ids := []string{"title", "space", "version", "labels", "updated"}
	out := make([]config.ConfluenceFieldView, 0, len(ids))
	for _, id := range ids {
		view, _ := config.NormalizeConfluenceFieldView(config.ConfluenceFieldView{ID: id})
		out = append(out, view)
	}
	return out
}

func confluencePageFieldValues(page *domain.Resource, view config.ConfluenceFieldView, displayTimeZone string) []string {
	var values []string
	switch view.ID {
	case "title":
		values = scalarPageField(page.Title)
	case "space":
		values = scalarPageField(page.SpaceKey)
	case "version":
		if page.Version > 0 {
			values = []string{strconv.Itoa(page.Version)}
		}
	case "parent":
		values = scalarPageField(page.Parent)
	case "ancestors":
		values = append(values, page.Ancestors...)
	case "labels":
		values = append(values, page.Labels...)
	case "restricted":
		if page.Restricted == nil {
			if view.ShowEmpty {
				values = []string{"Unknown — re-pull required"}
			}
		} else if *page.Restricted {
			values = []string{"Yes"}
		} else {
			values = []string{"No"}
		}
	case "updated":
		rendered := page.Updated
		if view.Format == "date" || view.Format == "datetime" {
			rendered = renderTemporalFieldIn(page.Updated, view.Format, displayTimeZone)
		}
		if rendered != "" {
			values = []string{rendered}
		}
	}
	if view.Format == "scalar" && len(values) > 1 {
		return []string{strings.Join(values, ", ")}
	}
	return values
}

func confluenceCommentsForDisplay(comments []domain.Comment, displayTimeZone string) []domain.Comment {
	if len(comments) == 0 {
		return nil
	}
	out := append([]domain.Comment(nil), comments...)
	// A missing value comes only from a legacy recorded ViewState. Preserve its
	// pre-display-timezone comment bytes so apply/incremental preflight can
	// reproduce the old pristine view until an explicit render/pull migrates it.
	if displayTimeZone == "" {
		return out
	}
	for i := range out {
		out[i].Created = renderTemporalFieldIn(out[i].Created, "datetime", displayTimeZone)
	}
	return out
}

// confluenceQualifiedCommentsForDisplay is the Stage-2 compatibility
// projection used until the qualified tree renderer replaces the flat derived
// view. The versioned JSON sidecar remains authoritative and retains every
// thread, anchor, resolution, and completeness field omitted here.
func confluenceQualifiedCommentsForDisplay(result *ConfluenceCommentInventoryResult, displayTimeZone string) []domain.Comment {
	if result == nil || len(result.Comments) == 0 {
		return nil
	}
	comments := make([]domain.Comment, 0, len(result.Comments))
	for _, comment := range result.Comments {
		comments = append(comments, domain.Comment{
			ID: comment.ID, AuthorKey: comment.Author.ID, Author: comment.Author.DisplayName,
			Created: comment.CreatedAt, Body: comment.Body, BodyStorage: comment.BodyStorage,
		})
	}
	return confluenceCommentsForDisplay(comments, displayTimeZone)
}

func confluenceCommentsSidecarV2(result *ConfluenceCommentInventoryResult) mirror.ConfluenceCommentsSidecarV2 {
	sidecar := mirror.ConfluenceCommentsSidecarV2{
		SchemaVersion: result.SchemaVersion, PageID: result.PageID, PageVersion: result.PageVersion,
		Complete: result.Complete, CommentsComplete: result.CommentsComplete,
		ThreadsComplete: result.ThreadsComplete, AnchorsComplete: result.AnchorsComplete,
		Count: result.Count, RootCount: result.RootCount,
		PartialReasons: append([]string{}, result.PartialReasons...), Capabilities: result.Capabilities,
		Comments: []mirror.ConfluenceCommentsSidecarComment{}, Diagnostics: []mirror.ConfluenceCommentsSidecarDiagnostic{},
	}
	for _, comment := range result.Comments {
		row := mirror.ConfluenceCommentsSidecarComment{
			ID: comment.ID, PageID: comment.PageID, ParentID: cloneStringPointer(comment.ParentID), RootID: cloneStringPointer(comment.RootID),
			Relation: comment.Relation, Location: comment.Location, Resolution: comment.Resolution, Version: comment.Version,
			Author:    mirror.ConfluenceCommentsSidecarAuthor{ID: comment.Author.ID, DisplayName: comment.Author.DisplayName},
			CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt, Body: comment.Body, BodyStorage: comment.BodyStorage,
		}
		if comment.Anchor != nil {
			row.Anchor = &mirror.ConfluenceCommentsSidecarAnchor{
				MarkerRef: comment.Anchor.MarkerRef, OriginalSelection: comment.Anchor.OriginalSelection,
				ObservedSelection: comment.Anchor.ObservedSelection, Status: comment.Anchor.Status,
			}
		}
		sidecar.Comments = append(sidecar.Comments, row)
	}
	for _, diagnostic := range result.Diagnostics {
		sidecar.Diagnostics = append(sidecar.Diagnostics, mirror.ConfluenceCommentsSidecarDiagnostic{
			Code: diagnostic.Code, CommentID: diagnostic.CommentID, MarkerRef: diagnostic.MarkerRef,
			Selector: diagnostic.Selector, Location: diagnostic.Location,
		})
	}
	return sidecar
}

func scalarPageField(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return []string{value}
}

func confluenceNeedsRestrictions(rs RenderSettings) bool {
	if !rs.On(SecPageFields) {
		return false
	}
	for _, view := range rs.PageFields {
		if view.ID == "restricted" {
			return true
		}
	}
	return false
}

// readCommentsSidecar loads either the strict schema-v2 envelope or the
// historical flat array. A missing sidecar is normal; malformed, future, or
// page-mismatched bytes stay distinguishable so mutation preflights can fail
// closed and offline render can surface a warning.
func readCommentsSidecar(root, dir, slug, pageID string, pageVersion int) (confluenceCommentsView, error) {
	b, err := safepath.ReadFileWithin(root, filepath.Join(dir, slug+".comments.json"))
	if os.IsNotExist(err) {
		return confluenceCommentsView{}, nil
	}
	if err != nil {
		return confluenceCommentsView{}, err
	}
	decoded, err := mirror.DecodeConfluenceCommentsSidecar(b)
	if err != nil {
		return confluenceCommentsView{}, err
	}
	if decoded.Format == mirror.ConfluenceCommentsSidecarFormatLegacy {
		return confluenceCommentsView{flat: append([]domain.Comment(nil), decoded.Legacy...)}, nil
	}
	if decoded.V2 == nil || decoded.V2.PageID != pageID || decoded.V2.PageVersion != pageVersion {
		return confluenceCommentsView{}, fmt.Errorf("%w: Confluence comments sidecar does not match the page snapshot", domain.ErrCheckFailed)
	}
	comments := make([]domain.Comment, 0, len(decoded.V2.Comments))
	for _, comment := range decoded.V2.Comments {
		comments = append(comments, domain.Comment{
			ID: comment.ID, AuthorKey: comment.Author.ID, Author: comment.Author.DisplayName,
			Created: comment.CreatedAt, Body: comment.Body, BodyStorage: comment.BodyStorage,
		})
	}
	return confluenceCommentsView{flat: comments, qualified: decoded.V2}, nil
}

// ConfRendered is one re-rendered page view.
type ConfRendered struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Path  string `json:"path"`
}

// ConfRenderResult summarizes an offline `conf render`.
type ConfRenderResult struct {
	Root     string         `json:"root"`
	Rendered []ConfRendered `json:"rendered"`
	Warnings []string       `json:"-"`
}

// Render regenerates the `.md` read views of a Confluence mirror offline — no
// network, no PAT. target is a mirror directory, a `<slug>.md`, or a
// `<slug>.csf`; the mirror root is resolved by walking up to the `.atl` marker.
// For each page it parses the `.csf` substrate, reads the meta (refs, title,
// space, version, labels) and the `<slug>.comments.json` sidecar (when present),
// and rewrites `<slug>.md` under the effective render settings. A `.csf` that
// fails to parse gets the MDUnavailableStub (the same contract as pull). It
// records each page's view state in `.atl/state.json` (so a later `conf apply`
// reproduces the exact pristine view) but never touches the
// `.csf`/`.meta.json` substrate or the `pages` sync entries, so `conf status`
// stays clean.
func (s *ConfluenceService) Render(target string, override config.RenderService) (*ConfRenderResult, error) {
	if target == "" {
		target = "mirror"
	}
	root := target
	if r, ok := MirrorRootOf(target); ok {
		root = r
	}
	if _, err := os.Stat(target); err != nil {
		return nil, localConfluenceTargetError("render", target, err)
	}
	rs, warns := ResolveRender(s.cfg, root, override, "confluence")
	res := &ConfRenderResult{Root: root, Rendered: []ConfRendered{}, Warnings: warns}
	lock, err := lockConfluenceMutations(root, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	m := mirror.New(root)
	snapshot, err := m.BeginReadSnapshot()
	if err != nil {
		return nil, err
	}
	paths, err := confRenderTargets(snapshot, target)
	if err != nil {
		return nil, err
	}
	vs := viewStateOf(rs)
	views := map[string]mirror.ViewState{}
	// Inspect every existing target before rewriting any sibling so one future
	// view version cannot leave a directory render half-migrated.
	for _, csfPath := range paths {
		if err := preflightConfluenceRenderView(m, snapshot, csfPath); err != nil {
			return res, err
		}
	}
	for _, csfPath := range paths {
		lc, body, err := snapshot.LoadCSF(csfPath)
		if err != nil {
			continue // unreadable page: skip, never fail the batch
		}
		dir := filepath.Dir(csfPath)
		slug := strings.TrimSuffix(filepath.Base(csfPath), ".csf")
		mdPath := filepath.Join(dir, slug+".md")
		md := []byte(mirror.MDUnavailableStub)
		if node, perr := csf.Parse(body); perr == nil {
			page := &domain.Resource{
				ID:         lc.Meta.ID,
				Title:      lc.Meta.Title,
				SpaceKey:   lc.Meta.Space,
				Version:    lc.Meta.Version,
				Parent:     lc.Meta.Parent,
				Ancestors:  lc.Meta.Ancestors,
				Labels:     lc.Meta.Labels,
				Updated:    lc.Meta.Updated,
				Restricted: lc.Meta.Restricted,
			}
			if confluenceNeedsRestrictions(rs) && page.Restricted == nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("render: restriction state for page %s was not mirrored; re-pull before relying on that field", lc.Meta.ID))
			}
			comments, commentErr := readCommentsSidecar(root, dir, slug, lc.Meta.ID, lc.Meta.Version)
			if commentErr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("render: comments for page %s are unavailable (%v); re-pull with --comments", lc.Meta.ID, commentErr))
				comments = confluenceCommentsView{}
			}
			mdOpts, sidecarErr := confMDViewOptsFromSidecars(rs, page, comments, root, dir, slug, lc.Meta.ID, node)
			if sidecarErr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("render: Jira macro enrichment for page %s is unavailable (%v); re-pull to refresh it", lc.Meta.ID, sidecarErr))
			}
			md = mirror.RenderMarkdownOpts(node, lc.Meta.Refs, mdOpts)
		}
		if err := safepath.WriteFileWithin(root, mdPath, md, 0o644); err != nil {
			return res, err
		}
		if lc.Meta.ID != "" {
			views[lc.Meta.ID] = vs
		}
		rel, _ := filepath.Rel(root, mdPath)
		res.Rendered = append(res.Rendered, ConfRendered{ID: lc.Meta.ID, Title: lc.Meta.Title, Path: rel})
	}
	// Persist the recorded views in one load-modify-save. This writes only the
	// `views` map, never a `pages` sync entry, so `conf status` stays clean.
	if err := m.SaveViewStates(views); err != nil {
		return res, err
	}
	return res, nil
}

// confRenderTargets resolves a render target to the `.csf` paths to rewrite. A
// file target maps to its sibling `.csf`; a directory target lists every tracked
// `.csf` under it.
func confRenderTargets(snapshot *mirror.ReadSnapshot, target string) ([]string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, localConfluenceTargetError("render", target, err)
	}
	if !info.IsDir() {
		csfPath := target
		switch {
		case strings.HasSuffix(target, ".csf"):
			// already the substrate
		case strings.HasSuffix(target, ".md"):
			csfPath = strings.TrimSuffix(target, ".md") + ".csf"
		default:
			return nil, fmt.Errorf("%w: render target %q must be a directory, a .md, or a .csf file", domain.ErrUsage, target)
		}
		if _, err := os.Stat(csfPath); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("%w: no .csf for render target %q", domain.ErrNotFound, target)
			}
			return nil, fmt.Errorf("%w: inspect .csf for render target %q: %v", domain.ErrCheckFailed, target, err)
		}
		return []string{csfPath}, nil
	}
	locals, err := snapshot.ListCSF()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, lc := range locals {
		if within(target, lc.Path) {
			out = append(out, lc.Path)
		}
	}
	return out, nil
}

// preflightConfluenceRenderView proves the existing derived view byte-for-byte
// before an explicit render rewrites it. This prevents a format migration from
// treating edited legacy bytes as pristine and preserves the whole-batch guarantee.
func preflightConfluenceRenderView(m *mirror.Mirror, snapshot *mirror.ReadSnapshot, csfPath string) error {
	dir := filepath.Dir(csfPath)
	slug := strings.TrimSuffix(filepath.Base(csfPath), ".csf")
	mdPath := filepath.Join(dir, slug+".md")
	actual, err := safepath.ReadFileWithin(m.Root, mdPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect existing render target %s: %v", domain.ErrCheckFailed, mdPath, err)
	}
	marker := mirror.ConfluenceDocumentMarkerLine(string(actual))
	if marker == mirror.ConfluenceDocumentMarker {
		// Explicit offline render retains its established regeneration contract
		// for the format this binary owns. The strict byte proof below is for
		// migration only, where old generated bytes could otherwise be mistaken
		// for a locally edited legacy view.
		return nil
	}
	if !mirror.IsSupportedLegacyConfluenceDocumentMarker(marker) {
		if strings.HasPrefix(marker, "<!-- atl:document confluence-page") {
			if mirror.IsFutureConfluenceDocumentMarker(marker) {
				return fmt.Errorf("%w: existing view %s uses future format marker %q; preserve it and update atl before rendering — do not downgrade it with this binary", domain.ErrCheckFailed, mdPath, marker)
			}
			return fmt.Errorf("%w: existing view %s uses unsupported historical format marker %q; preserve it because this binary cannot reconstruct its exact generated bytes", domain.ErrCheckFailed, mdPath, marker)
		}
		return fmt.Errorf("%w: existing view %s has an unrecognized or missing document marker; preserve it before rendering", domain.ErrCheckFailed, mdPath)
	}
	lc, body, err := snapshot.LoadCSF(csfPath)
	if err != nil {
		return fmt.Errorf("%w: reconstruct existing render target %s: %v", domain.ErrCheckFailed, mdPath, err)
	}
	current := []byte(mirror.MDUnavailableStub)
	views := confluencePristineViews{current: current, legacy: map[string][]byte{
		mirror.ConfluenceDocumentMarkerV5: bytes.Replace(current, []byte(mirror.ConfluenceDocumentMarker), []byte(mirror.ConfluenceDocumentMarkerV5), 1),
		mirror.ConfluenceDocumentMarkerV4: bytes.Replace(current, []byte(mirror.ConfluenceDocumentMarker), []byte(mirror.ConfluenceDocumentMarkerV4), 1),
	}}
	if node, parseErr := csf.Parse(body); parseErr == nil {
		viewState, hasView := snapshot.ViewStateOf(lc.Meta.ID)
		renderSettings := RenderSettings{Sections: map[string]bool{}}
		if hasView {
			renderSettings = settingsFromViewState(viewState)
		}
		comments, commentErr := readCommentsSidecar(m.Root, dir, slug, lc.Meta.ID, lc.Meta.Version)
		if commentErr != nil {
			// Offline render may warn and omit an invalid auxiliary sidecar, but it
			// still cannot overwrite a view that depended on those unreadable bytes.
			comments = confluenceCommentsView{}
		}
		opts, sidecarErr := confMDViewOptsFromSidecars(renderSettings, confPageFromMeta(lc.Meta), comments, m.Root, dir, slug, lc.Meta.ID, node)
		if sidecarErr != nil {
			opts = confMDViewOptsForCommentsView(renderSettings, confPageFromMeta(lc.Meta), comments)
		}
		legacyOpts := legacyConfluenceCommentMDViewOpts(opts, renderSettings, comments)
		views = renderConfluencePristineViews(node, lc.Meta.Refs, opts, legacyOpts)
	}
	if _, matchErr := matchConfluencePristineView(actual, views); matchErr != nil {
		return fmt.Errorf("%w: existing view %s %v", domain.ErrCheckFailed, mdPath, matchErr)
	}
	return nil
}
