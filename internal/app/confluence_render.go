package app

import (
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
func readCommentsSidecar(root, dir, slug, pageID string, pageVersion int) ([]domain.Comment, error) {
	b, err := safepath.ReadFileWithin(root, filepath.Join(dir, slug+".comments.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decoded, err := mirror.DecodeConfluenceCommentsSidecar(b)
	if err != nil {
		return nil, err
	}
	if decoded.Format == mirror.ConfluenceCommentsSidecarFormatLegacy {
		return append([]domain.Comment(nil), decoded.Legacy...), nil
	}
	if decoded.V2 == nil || decoded.V2.PageID != pageID || decoded.V2.PageVersion != pageVersion {
		return nil, fmt.Errorf("%w: Confluence comments sidecar does not match the page snapshot", domain.ErrCheckFailed)
	}
	comments := make([]domain.Comment, 0, len(decoded.V2.Comments))
	for _, comment := range decoded.V2.Comments {
		comments = append(comments, domain.Comment{
			ID: comment.ID, AuthorKey: comment.Author.ID, Author: comment.Author.DisplayName,
			Created: comment.CreatedAt, Body: comment.Body, BodyStorage: comment.BodyStorage,
		})
	}
	return comments, nil
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
	paths, err := confRenderTargets(m, target)
	if err != nil {
		return nil, err
	}
	vs := viewStateOf(rs)
	views := map[string]mirror.ViewState{}
	// Inspect every existing target before rewriting any sibling so one future
	// view version cannot leave a directory render half-migrated.
	for _, csfPath := range paths {
		dir := filepath.Dir(csfPath)
		slug := strings.TrimSuffix(filepath.Base(csfPath), ".csf")
		if err := preflightConfluenceRenderView(root, filepath.Join(dir, slug+".md")); err != nil {
			return res, err
		}
	}
	for _, csfPath := range paths {
		lc, body, err := m.LoadCSF(csfPath)
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
				comments = nil
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
func confRenderTargets(m *mirror.Mirror, target string) ([]string, error) {
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
	locals, err := m.ListCSF()
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

// preflightConfluenceRenderView prevents an older binary from destroying an
// existing view whose document format it does not understand. Legacy v1 and
// unversioned views remain intentionally render-migratable to the current
// format; only an explicit different version marker is a downgrade hazard.
func preflightConfluenceRenderView(root, mdPath string) error {
	b, err := safepath.ReadFileWithin(root, mdPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect existing render target %s: %v", domain.ErrCheckFailed, mdPath, err)
	}
	first := mirror.ConfluenceDocumentMarkerLine(string(b))
	if strings.HasPrefix(first, "<!-- atl:document confluence-page") &&
		first != mirror.ConfluenceDocumentMarker &&
		first != "<!-- atl:document confluence-page v3 -->" &&
		first != "<!-- atl:document confluence-page v2 -->" &&
		first != "<!-- atl:document confluence-page v1 -->" &&
		first != "<!-- atl:document confluence-page -->" {
		return fmt.Errorf("%w: existing view %s uses unsupported format marker %q; preserve it and update atl before rendering — do not downgrade it with this binary", domain.ErrCheckFailed, mdPath, first)
	}
	return nil
}
