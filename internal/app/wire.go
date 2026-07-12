// Package app holds transport-agnostic use-cases. It depends on ports
// (domain.DocStore/Tracker) and the mirror engine, never on cobra or net/http
// directly, so the same logic can back a future server tier.
package app

import (
	"errors"
	"fmt"

	"github.com/isukharev/atl/internal/adapter/confluence"
	"github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/auth"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

// ConfluenceService bundles the Confluence use-cases over a DocStore + mirror.
// cfg holds the non-secret global config so render resolution (profiles) can
// merge global + per-mirror local settings; it is never used to reach the
// backend (that goes through store).
type ConfluenceService struct {
	store    domain.DocStore
	users    domain.UserResolver
	assets   domain.AssetResolver
	baseURL  string
	verifier domain.Verifier
	cfg      *config.Config
	jiraRead domain.Tracker
	// jiraReadReason is deliberately coarse and URL-free for render warnings.
	jiraReadReason string
}

// JiraService bundles the Jira use-cases over a Tracker. agile and structure are
// optional plugin capabilities; in production they are the same adapter instance
// as tr, mirroring how ConfluenceService composes one adapter across several
// capability fields.
type JiraService struct {
	tr        domain.Tracker
	agile     domain.Agile
	structure domain.StructureReader
	baseURL   string
	cfg       *config.Config
}

// NewConfluence wires the Confluence adapter from config + PAT.
func NewConfluence(cfg *config.Config, version string) (*ConfluenceService, error) {
	if cfg.ConfluenceURL == "" {
		return nil, fmt.Errorf("%w: Confluence URL not set — run `atl config set --confluence-url https://confluence.example.com` (or export ATL_CONFLUENCE_URL); see `atl auth status`", domain.ErrConfig)
	}
	if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	tok, err := auth.Token(auth.Confluence)
	if err != nil {
		// A token that is simply *not configured* is a setup problem (ErrConfig →
		// exit 7), distinct from a server-side rejection (ErrAuth → exit 3) — so a
		// script can tell "run `atl auth login`" from "the token was refused". A
		// corrupt/unreadable credentials file is neither; let it stay a generic
		// error (exit 1) rather than misreport it as "not set up".
		if errors.Is(err, auth.ErrNoToken) {
			return nil, fmt.Errorf("%w: %v", domain.ErrConfig, err)
		}
		return nil, err
	}
	cf := confluence.New(cfg.ConfluenceURL, tok, version)
	service := &ConfluenceService{store: cf, users: cf.ResolveUser, assets: cf, baseURL: cfg.ConfluenceURL, verifier: cf, cfg: cfg}
	service.jiraRead, service.jiraReadReason = optionalJiraRead(cfg, version)
	return service, nil
}

func optionalJiraRead(cfg *config.Config, version string) (domain.Tracker, string) {
	if cfg == nil || cfg.JiraURL == "" {
		return nil, "Jira URL is not configured"
	}
	if err := config.CheckSecureURL(cfg.JiraURL); err != nil {
		return nil, "Jira URL is not approved for authenticated reads"
	}
	token, err := auth.Token(auth.Jira)
	if err != nil {
		return nil, "Jira credentials are not configured"
	}
	return jira.New(cfg.JiraURL, token, version), ""
}

// NewConfluenceRenderer builds a ConfluenceService for the offline `conf render`
// use-case. It carries only the global config (for profile resolution) and never
// constructs a DocStore, so it needs no backend URL or PAT — Render walks the
// local mirror and rewrites `.md` views without any network access.
func NewConfluenceRenderer(cfg *config.Config) *ConfluenceService {
	return &ConfluenceService{cfg: cfg}
}

// NewJira wires the Jira adapter from config + PAT.
func NewJira(cfg *config.Config, version string) (*JiraService, error) {
	if cfg.JiraURL == "" {
		return nil, fmt.Errorf("%w: Jira URL not set — run `atl config set --jira-url https://jira.example.com` (or export ATL_JIRA_URL); see `atl auth status`", domain.ErrConfig)
	}
	if err := config.CheckSecureURL(cfg.JiraURL); err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	tok, err := auth.Token(auth.Jira)
	if err != nil {
		// Not-configured token → setup problem (ErrConfig → exit 7); a corrupt or
		// unreadable store stays a generic error (exit 1). See NewConfluence.
		if errors.Is(err, auth.ErrNoToken) {
			return nil, fmt.Errorf("%w: %v", domain.ErrConfig, err)
		}
		return nil, err
	}
	j := jira.New(cfg.JiraURL, tok, version)
	return &JiraService{tr: j, agile: j, structure: j, baseURL: cfg.JiraURL, cfg: cfg}, nil
}

// NewJiraRenderer builds a JiraService for the offline `jira render` use-case. It
// carries only the global config (for profile resolution) and never constructs a
// Tracker, so it needs no backend URL or PAT — Render decodes local `<KEY>.json`
// snapshots and rewrites `.md` views without any network access.
func NewJiraRenderer(cfg *config.Config) *JiraService {
	return &JiraService{cfg: cfg}
}
