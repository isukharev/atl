package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

// confMDViewOpts assembles the profile-driven markdown-view additions for a
// Confluence page from the resolved settings: a metadata frontmatter (full
// profile) and a "## Comments" section fed from whatever comments the caller has
// (the just-fetched set on pull, or the sidecar on render/push). An empty return
// yields the byte-identical body-only default view.
func confMDViewOpts(rs RenderSettings, page *domain.Resource, comments []domain.Comment) mirror.MDViewOpts {
	var opts mirror.MDViewOpts
	if rs.On(SecFrontmatter) {
		opts.Frontmatter = &mirror.PageFrontmatter{
			Title:   page.Title,
			Space:   page.SpaceKey,
			Version: page.Version,
			Labels:  page.Labels,
		}
	}
	if rs.On(SecComments) && len(comments) > 0 {
		opts.Comments = comments
	}
	return opts
}

// readCommentsSidecar loads a page's `<slug>.comments.json` sidecar into a comment
// slice. A missing or unreadable sidecar yields nil so the "## Comments" section
// is silently skipped rather than failing the render (its contract).
func readCommentsSidecar(dir, slug string) []domain.Comment {
	b, err := os.ReadFile(filepath.Join(dir, slug+".comments.json"))
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
// fails to parse gets the MDUnavailableStub (the same contract as pull). It never
// touches the `.csf`/`.meta.json`/sidecar substrate, so `conf status` stays clean.
func (s *ConfluenceService) Render(target string, override config.RenderService) (*ConfRenderResult, error) {
	if target == "" {
		target = "mirror"
	}
	root := target
	if r, ok := MirrorRootOf(target); ok {
		root = r
	}
	rs, warns := ResolveRender(s.cfg, root, override, "confluence")
	res := &ConfRenderResult{Root: root, Rendered: []ConfRendered{}, Warnings: warns}
	m := mirror.New(root)
	paths, err := confRenderTargets(m, target)
	if err != nil {
		return nil, err
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
				Title:    lc.Meta.Title,
				SpaceKey: lc.Meta.Space,
				Version:  lc.Meta.Version,
				Labels:   lc.Meta.Labels,
			}
			mdOpts := confMDViewOpts(rs, page, readCommentsSidecar(dir, slug))
			md = mirror.RenderMarkdownOpts(node, lc.Meta.Refs, mdOpts)
		}
		if err := safepath.WriteFile(mdPath, md, 0o644); err != nil {
			return res, err
		}
		rel, _ := filepath.Rel(root, mdPath)
		res.Rendered = append(res.Rendered, ConfRendered{ID: lc.Meta.ID, Title: lc.Meta.Title, Path: rel})
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
