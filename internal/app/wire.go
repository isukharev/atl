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
	"github.com/isukharev/atl/internal/compatibility"
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
	jiraReadReason            string
	requestMaxInFlight        int
	requestsPerSecond         int
	commentMutator            domain.ConfluenceCommentMutator
	commentPreparer           domain.ConfluenceInlineCommentPreparer
	commentMutationActivation *compatibility.Activation
}

// JiraService bundles the Jira use-cases over a Tracker. agile and structure are
// optional plugin capabilities; in production they are the same adapter instance
// as tr, mirroring how ConfluenceService composes one adapter across several
// capability fields.
type JiraService struct {
	tr                     domain.Tracker
	agile                  domain.Agile
	structure              domain.StructureReader
	baseURL                string
	cfg                    *config.Config
	graphConfluence        domain.ConfluenceGraphPageMetadataReader
	graphConfluenceFactory func() (domain.ConfluenceGraphPageMetadataReader, string)
	graphConfluenceOnce    sync.Once
	graphConfluenceReason  string
	writeAuthorizer        domain.WriteAuthorizer
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

// NewCompatibility wires the optional exact product-identity reader lazily so
// offline status and disabled settings need neither credentials nor network.
func NewCompatibility(cfg *config.Config, settings compatibility.Settings, version string) *CompatibilityService {
	service := &CompatibilityService{settings: settings}
	service.confluenceFactory = func() (domain.ExactServerMetadataReader, string) {
		if cfg == nil || cfg.ConfluenceURL == "" {
			return nil, "backend_not_configured"
		}
		if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
			return nil, "invalid_configuration"
		}
		token, err := auth.Token(auth.Confluence)
		if err != nil {
			if errors.Is(err, auth.ErrNoToken) {
				return nil, "credentials_missing"
			}
			return nil, "credentials_unavailable"
		}
		reader, err := confluence.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, nil, confluenceTLSOptions(cfg))
		if err != nil {
			return nil, "invalid_configuration"
		}
		return reader, ""
	}
	return service
}

func jiraTLSOptions(cfg *config.Config) httpx.TLSOptions {
	return httpx.TLSOptions{CABundle: cfg.CABundle(config.TransportServiceJira)}
}

func confluenceTLSOptions(cfg *config.Config) httpx.TLSOptions {
	return httpx.TLSOptions{CABundle: cfg.CABundle(config.TransportServiceConfluence)}
}

func newDoctorServerMetadataReader(service, rawURL, token, clientVersion string, cfg *config.Config) (domain.ServerMetadataReader, error) {
	switch service {
	case domain.ServerProductJira:
		return jira.NewWithSchedulerTLS(rawURL, token, clientVersion, nil, jiraTLSOptions(cfg))
	case domain.ServerProductConfluence:
		return confluence.NewWithSchedulerTLS(rawURL, token, clientVersion, nil, confluenceTLSOptions(cfg))
	default:
		return nil, fmt.Errorf("%w: unsupported backend service", domain.ErrConfig)
	}
}

// NewConfluence wires the Confluence adapter from config + PAT.
func NewConfluence(cfg *config.Config, version string) (*ConfluenceService, error) {
	return NewConfluenceWithWriteAuthorizer(cfg, version, nil)
}

// NewConfluenceWithWriteAuthorizer wires the optional transport-neutral policy
// port through every concrete Confluence capability.
func NewConfluenceWithWriteAuthorizer(cfg *config.Config, version string, authorizer domain.WriteAuthorizer) (*ConfluenceService, error) {
	return NewConfluenceScheduledWithWriteAuthorizer(cfg, version, 0, 0, authorizer)
}

// NewConfluenceCommentMutations constructs the explicitly activated mutation
// surface without making ordinary Confluence commands load compatibility
// settings or fail because those optional owner-private settings are invalid.
func NewConfluenceCommentMutations(cfg *config.Config, version string, activation compatibility.Activation) (*ConfluenceService, error) {
	return NewConfluenceCommentMutationsWithWriteAuthorizer(cfg, version, activation, nil)
}

// NewConfluenceCommentMutationsWithWriteAuthorizer keeps compatibility writes
// behind the same adapter-owned policy guard as ordinary Confluence writes.
func NewConfluenceCommentMutationsWithWriteAuthorizer(cfg *config.Config, version string, activation compatibility.Activation, authorizer domain.WriteAuthorizer) (*ConfluenceService, error) {
	service, err := NewConfluenceWithWriteAuthorizer(cfg, version, authorizer)
	if err != nil {
		return nil, err
	}
	cf, ok := service.store.(*confluence.Confluence)
	if !ok {
		return nil, fmt.Errorf("%w: Confluence compatibility adapter is unavailable", domain.ErrConfig)
	}
	provider, err := confluence.NewCommentMutationProvider(cf, activation)
	if err != nil {
		return nil, err
	}
	service.commentMutator = provider
	service.commentPreparer = provider
	activationCopy := activation
	service.commentMutationActivation = &activationCopy
	return service, nil
}

// NewConfluenceScheduled wires one request scheduler through Confluence and
// optional Jira-macro reads. maxInFlight=0 preserves the ordinary unscheduled
// constructor used by every command except explicitly bounded pull workflows.
func NewConfluenceScheduled(cfg *config.Config, version string, maxInFlight, requestsPerSecond int) (*ConfluenceService, error) {
	return NewConfluenceScheduledWithWriteAuthorizer(cfg, version, maxInFlight, requestsPerSecond, nil)
}

// NewConfluenceScheduledWithWriteAuthorizer combines bounded read scheduling
// with the optional write guard on one shared adapter instance.
func NewConfluenceScheduledWithWriteAuthorizer(cfg *config.Config, version string, maxInFlight, requestsPerSecond int, authorizer domain.WriteAuthorizer) (*ConfluenceService, error) {
	if maxInFlight == 0 && requestsPerSecond != 0 {
		return nil, fmt.Errorf("%w: request pacing requires a positive in-flight bound", domain.ErrUsage)
	}
	if cfg.ConfluenceURL == "" {
		return nil, fmt.Errorf("%w: Confluence URL not set — run `atl config set --confluence-url https://confluence.example.com` (or export ATL_CONFLUENCE_URL); see `atl auth status`", domain.ErrConfig)
	}
	if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrUsage, err)
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
	var options []confluence.Option
	if authorizer != nil {
		options = append(options, confluence.WithWriteAuthorizer(authorizer))
	}
	cf, err := confluence.NewWithSchedulerTLS(cfg.ConfluenceURL, tok, version, scheduler, confluenceTLSOptions(cfg), options...)
	if err != nil {
		return nil, err
	}
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
	reader, err := jira.NewWithSchedulerTLS(cfg.JiraURL, token, version, scheduler, jiraTLSOptions(cfg))
	if err != nil {
		return nil, "Jira transport configuration is invalid"
	}
	return reader, ""
}

func optionalConfluenceGraphRead(cfg *config.Config, version string) (domain.ConfluenceGraphPageMetadataReader, string) {
	if cfg == nil || cfg.ConfluenceURL == "" {
		return nil, "not_configured"
	}
	if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
		return nil, "invalid_configuration"
	}
	token, err := auth.Token(auth.Confluence)
	if err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			return nil, "credentials_missing"
		}
		return nil, "credentials_unavailable"
	}
	reader, err := confluence.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, nil, confluenceTLSOptions(cfg))
	if err != nil {
		return nil, "invalid_configuration"
	}
	return reader, ""
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
	return NewJiraWithWriteAuthorizer(cfg, version, nil)
}

// NewJiraWithWriteAuthorizer wires the optional transport-neutral policy port
// into the Jira adapter. Callers pass nil when no content policy is active so
// ordinary commands preserve their exact request counts.
func NewJiraWithWriteAuthorizer(cfg *config.Config, version string, authorizer domain.WriteAuthorizer) (*JiraService, error) {
	if cfg.JiraURL == "" {
		return nil, fmt.Errorf("%w: Jira URL not set — run `atl config set --jira-url https://jira.example.com` (or export ATL_JIRA_URL); see `atl auth status`", domain.ErrConfig)
	}
	if err := config.CheckSecureURL(cfg.JiraURL); err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrUsage, err)
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
	var options []jira.Option
	if authorizer != nil {
		options = append(options, jira.WithWriteAuthorizer(authorizer))
	}
	j, err := jira.NewWithSchedulerTLS(cfg.JiraURL, tok, version, nil, jiraTLSOptions(cfg), options...)
	if err != nil {
		return nil, err
	}
	service := &JiraService{tr: j, agile: j, structure: j, baseURL: cfg.JiraURL, cfg: cfg, writeAuthorizer: authorizer}
	service.graphConfluenceFactory = func() (domain.ConfluenceGraphPageMetadataReader, string) {
		return optionalConfluenceGraphRead(cfg, version)
	}
	return service, nil
}

func (s *JiraService) confluenceGraphMetadataReader() (domain.ConfluenceGraphPageMetadataReader, string) {
	if s == nil {
		return nil, "not_configured"
	}
	s.graphConfluenceOnce.Do(func() {
		if s.graphConfluence != nil || s.graphConfluenceFactory == nil {
			return
		}
		s.graphConfluence, s.graphConfluenceReason = s.graphConfluenceFactory()
	})
	if s.graphConfluence == nil && s.graphConfluenceReason == "" {
		return nil, "not_configured"
	}
	return s.graphConfluence, s.graphConfluenceReason
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
		reader, err := jira.NewWithSchedulerTLS(cfg.JiraURL, token, version, nil, jiraTLSOptions(cfg))
		if err != nil {
			s.jiraSetup = "invalid_configuration"
		} else {
			s.jiraTime = reader
		}
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
		reader, err := confluence.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, nil, confluenceTLSOptions(cfg))
		if err != nil {
			s.confluenceSetup = "invalid_configuration"
		} else {
			s.confluenceTime = reader
		}
	}
	return s
}
