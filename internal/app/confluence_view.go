package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mirror"
)

// ConfluencePageViewOpts controls a transient, read-only Markdown projection.
// Root selects the presentation-only local config layer and is never written.
type ConfluencePageViewOpts struct {
	Root   string
	Render config.RenderService
}

// ConfluencePageViewResult is the scriptable JSON form of `conf page view`.
// Warnings are emitted on stderr by the CLI so stdout remains one JSON object
// or raw Markdown under -o text.
type ConfluencePageViewResult struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Space    string   `json:"space"`
	Version  int      `json:"version"`
	Markdown string   `json:"markdown"`
	Warnings []string `json:"-"`
}

// ViewPage fetches one native CSF page and renders it through the same
// configured Markdown pipeline as pull/render, without creating mirror state or
// fetching binary assets. Every generated section, including the body, is
// marked read-only because this transient document has no writeback baseline.
func (s *ConfluenceService) ViewPage(ctx context.Context, id string, opts ConfluencePageViewOpts) (*ConfluencePageViewResult, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = "."
	}
	rs, warnings := ResolveRender(s.cfg, root, opts.Render, "confluence")
	page, err := s.store.GetPage(ctx, id, domain.PullOpts{
		Format: "csf", IncludeRestrictions: confluenceNeedsRestrictions(rs),
	})
	if err != nil {
		return nil, err
	}
	node, err := csf.Parse(page.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: page %s CSF cannot be rendered as Markdown: %v", domain.ErrCheckFailed, id, err)
	}

	refs := fragment.Extract(node)
	var comments []domain.Comment
	if rs.On(SecComments) {
		var truncated bool
		comments, truncated, err = s.store.ListComments(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("view comments %s: %w", id, err)
		}
		if truncated {
			warnings = append(warnings, fmt.Sprintf("render: comments for page %s are truncated", id))
		}
	}
	mdOpts := confMDViewOpts(rs, page, comments)
	mdOpts.ReadOnly = true
	return &ConfluencePageViewResult{
		ID:       page.ID,
		Title:    page.Title,
		Space:    page.SpaceKey,
		Version:  page.Version,
		Markdown: string(mirror.RenderMarkdownOpts(node, refs, mdOpts)),
		Warnings: warnings,
	}, nil
}
