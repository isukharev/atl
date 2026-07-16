// Package app holds transport-agnostic use-cases. It depends on ports
// (domain.DocStore/Tracker) and the mirror engine, never on cobra or net/http
// directly, so the same logic can back a future server tier.
package app

import (
	"errors"
	"fmt"
	"sync"

	"github.com/isukharev/atl/internal/adapter/confluence"
	"github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/auth"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

// ConfluenceService bundles the Confluence use-cases over a DocStore + mirror.
// cfg holds the non-secret global config so render resolution (profiles) can
// merge global + per-mirror local settings; it is never used to reach the
// backend (that goes through store).
type ConfluenceService struct {
	store           domain.DocStore
	users           domain.UserResolver
	assets          domain.AssetResolver
	baseURL         string
	verifier        domain.Verifier
	cfg             *config.Config
	jiraRead        domain.Tracker
	jiraReadFactory func() (domain.Tracker, string)
	jiraReadOnce    sync.Once
	// jiraReadReason is deliberately coarse and URL-free for render warnings.
	jiraReadReason     string
	requestMaxInFlight int
	requestsPerSecond  int
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

// EnvironmentService composes the bounded metadata readers used by
// `environment inspect`. Setup failures are retained as closed status values so
// one missing backend never hides diagnostics for the other one.
type EnvironmentService struct {
	cfg             *config.Config
	jiraTime        domain.JiraTimeSemanticsReader
	confluenceTime  domain.ConfluenceTimeSemanticsReader
	jiraSetup       string
	confluenceSetup string
}

// NewConfluence wires the Confluence adapter from config + PAT.
func NewConfluence(cfg *config.Config, version string) (*ConfluenceService, error) {
	return NewConfluenceScheduled(cfg, version, 0, 0)
}

// NewConfluenceScheduled wires one request scheduler through Confluence and
// optional Jira-macro reads. maxInFlight=0 preserves the ordinary unscheduled
// constructor used by every command except explicitly bounded pull workflows.
func NewConfluenceScheduled(cfg *config.Config, version string, maxInFlight, requestsPerSecond int) (*ConfluenceService, error) {
	if maxInFlight == 0 && requestsPerSecond != 0 {
		return nil, fmt.Errorf("%w: request pacing requires a positive in-flight bound", domain.ErrUsage)
	}
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
	var scheduler *httpx.Scheduler
	if maxInFlight != 0 {
		scheduler, err = httpx.NewScheduler(maxInFlight, requestsPerSecond)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid request schedule: %v", domain.ErrUsage, err)
		}
	}
	cf := confluence.NewWithScheduler(cfg.ConfluenceURL, tok, version, scheduler)
	service := &ConfluenceService{
		store: cf, users: cf.ResolveUser, assets: cf, baseURL: cfg.ConfluenceURL, verifier: cf, cfg: cfg,
		requestMaxInFlight: maxInFlight, requestsPerSecond: requestsPerSecond,
	}
	service.jiraReadFactory = func() (domain.Tracker, string) { return optionalJiraReadScheduled(cfg, version, scheduler) }
	return service, nil
}

func optionalJiraReadScheduled(cfg *config.Config, version string, scheduler *httpx.Scheduler) (domain.Tracker, string) {
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
	return jira.NewWithScheduler(cfg.JiraURL, token, version, scheduler), ""
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

// NewEnvironment wires only metadata/current-user readers. It never performs a
// request itself and deliberately degrades absent URLs/credentials into report
// status instead of preventing the configured sibling backend from being read.
func NewEnvironment(cfg *config.Config, version string) *EnvironmentService {
	s := &EnvironmentService{cfg: cfg}
	if cfg == nil {
		s.jiraSetup = "not_configured"
		s.confluenceSetup = "not_configured"
		return s
	}
	if cfg.JiraURL == "" {
		s.jiraSetup = "not_configured"
	} else if err := config.CheckSecureURL(cfg.JiraURL); err != nil {
		s.jiraSetup = "invalid_configuration"
	} else if token, err := auth.Token(auth.Jira); err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			s.jiraSetup = "credentials_missing"
		} else {
			s.jiraSetup = "credentials_unavailable"
		}
	} else {
		s.jiraTime = jira.New(cfg.JiraURL, token, version)
	}
	if cfg.ConfluenceURL == "" {
		s.confluenceSetup = "not_configured"
	} else if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
		s.confluenceSetup = "invalid_configuration"
	} else if token, err := auth.Token(auth.Confluence); err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			s.confluenceSetup = "credentials_missing"
		} else {
			s.confluenceSetup = "credentials_unavailable"
		}
	} else {
		s.confluenceTime = confluence.New(cfg.ConfluenceURL, token, version)
	}
	return s
}
