// Package app holds transport-agnostic use-cases. It depends on ports
// (domain.DocStore/Tracker) and the mirror engine, never on cobra or net/http
// directly, so the same logic can back a future server tier.
package app

import (
	"fmt"

	"github.com/isukharev/atl/internal/adapter/confluence"
	"github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/auth"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

// ConfluenceService bundles the Confluence use-cases over a DocStore + mirror.
type ConfluenceService struct {
	store   domain.DocStore
	users   domain.UserResolver
	assets  domain.AssetResolver
	baseURL string
}

// JiraService bundles the Jira use-cases over a Tracker.
type JiraService struct {
	tr      domain.Tracker
	baseURL string
}

// NewConfluence wires the Confluence adapter from config + PAT.
func NewConfluence(cfg *config.Config, version string) (*ConfluenceService, error) {
	if cfg.ConfluenceURL == "" {
		return nil, fmt.Errorf("%w: Confluence URL not set (ATL_CONFLUENCE_URL / CONFLUENCE_URL or `atl config`)", domain.ErrUsage)
	}
	tok, err := auth.Token(auth.Confluence)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrAuth, err)
	}
	cf := confluence.New(cfg.ConfluenceURL, tok, version)
	return &ConfluenceService{store: cf, users: cf.ResolveUser, assets: cf, baseURL: cfg.ConfluenceURL}, nil
}

// NewJira wires the Jira adapter from config + PAT.
func NewJira(cfg *config.Config, version string) (*JiraService, error) {
	if cfg.JiraURL == "" {
		return nil, fmt.Errorf("%w: Jira URL not set (ATL_JIRA_URL / JIRA_URL or `atl config`)", domain.ErrUsage)
	}
	tok, err := auth.Token(auth.Jira)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrAuth, err)
	}
	return &JiraService{tr: jira.New(cfg.JiraURL, tok, version), baseURL: cfg.JiraURL}, nil
}
