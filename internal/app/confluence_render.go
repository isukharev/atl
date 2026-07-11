package app

import (
	"encoding/json"
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
			values := confluencePageFieldValues(page, view)
			if len(values) == 0 && !view.ShowEmpty {
				continue
			}
			opts.PageFields = append(opts.PageFields, mirror.PageField{
				ID: view.ID, Label: view.Label, Placement: view.Placement,
				Values: values, ShowEmpty: view.ShowEmpty,
			})
		}
	}
	if rs.On(SecComments) && len(comments) > 0 {
		opts.Comments = comments
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

func confluencePageFieldValues(page *domain.Resource, view config.ConfluenceFieldView) []string {
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
			rendered = renderTemporalField(page.Updated, view.Format)
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

// readCommentsSidecar loads a page's `<slug>.comments.json` sidecar into a comment
// slice. A missing or unreadable sidecar yields nil so the "# Comments" section
// is silently skipped rather than failing the render (its contract).
func readCommentsSidecar(root, dir, slug string) []domain.Comment {
	b, err := safepath.ReadFileWithin(root, filepath.Join(dir, slug+".comments.json"))
	if err != nil {
		return nil
	}
	var comments []domain.Comment
	if err := json.Unmarshal(b, &comments); err != nil {
		return nil
	}
	return comments
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
		return nil, fmt.Errorf("%w: render target %q: %v", domain.ErrUsage, target, err)
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
			mdOpts := confMDViewOpts(rs, page, readCommentsSidecar(root, dir, slug))
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
		return nil, fmt.Errorf("%w: render target %q: %v", domain.ErrUsage, target, err)
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
			return nil, fmt.Errorf("%w: no .csf for %q (%v)", domain.ErrUsage, target, err)
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
