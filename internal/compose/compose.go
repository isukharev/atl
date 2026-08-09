// Package compose is the product composition root. It owns configuration,
// credentials, TLS, schedulers, and concrete backend adapters, then supplies
// transport-neutral ports to internal/app.
package compose

import (
	"context"
	"errors"
	"fmt"

	confluenceadapter "github.com/isukharev/atl/internal/adapter/confluence"
	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/auth"
	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func jiraTLSOptions(cfg *config.Config) httpx.TLSOptions {
	return httpx.TLSOptions{CABundle: cfg.CABundle(config.TransportServiceJira)}
}

func confluenceTLSOptions(cfg *config.Config) httpx.TLSOptions {
	return httpx.TLSOptions{CABundle: cfg.CABundle(config.TransportServiceConfluence)}
}

// NewCompatibility composes the optional exact identity reader lazily, so
// offline status and disabled settings do not load credentials.
func NewCompatibility(cfg *config.Config, settings compatibility.Settings, version string, values ...Option) *app.CompatibilityService {
	resolved := resolveOptions(values)
	return app.NewCompatibilityService(settings, func() (domain.ExactServerMetadataReader, app.DependencySetupStatus) {
		if cfg == nil || cfg.ConfluenceURL == "" {
			return nil, app.DependencyNotConfigured
		}
		if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
			return nil, app.DependencyInvalidConfiguration
		}
		token, err := auth.Token(auth.Confluence)
		if err != nil {
			if errors.Is(err, auth.ErrNoToken) {
				return nil, app.DependencyCredentialsMissing
			}
			return nil, app.DependencyCredentialsUnavailable
		}
		reader, err := confluenceadapter.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, nil, confluenceTLSOptions(cfg), confluenceOptions(nil, resolved)...)
		if err != nil {
			return nil, app.DependencyInvalidConfiguration
		}
		return reader, app.DependencyReady
	})
}

// NewConfluence wires the ordinary Confluence service.
func NewConfluence(cfg *config.Config, version string, options ...Option) (*app.ConfluenceService, error) {
	return NewConfluenceWithWriteAuthorizer(cfg, version, nil, options...)
}

// LoadConfluence owns the production config-load boundary used by hosts that
// do not already need a qualified config value.
func LoadConfluence(version string, options ...Option) (*app.ConfluenceService, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return NewConfluence(cfg, version, options...)
}

// NewConfluenceWithWriteAuthorizer adds the transport-neutral write guard to
// every concrete Confluence capability.
func NewConfluenceWithWriteAuthorizer(cfg *config.Config, version string, authorizer domain.WriteAuthorizer, options ...Option) (*app.ConfluenceService, error) {
	return NewConfluenceScheduledWithWriteAuthorizer(cfg, version, 0, 0, authorizer, options...)
}

// NewConfluenceCommentMutations composes the explicitly activated mutation
// provider without affecting ordinary Confluence commands.
func NewConfluenceCommentMutations(cfg *config.Config, version string, activation compatibility.Activation, options ...Option) (*app.ConfluenceService, error) {
	return NewConfluenceCommentMutationsWithWriteAuthorizer(cfg, version, activation, nil, options...)
}

func NewConfluenceCommentMutationsWithWriteAuthorizer(cfg *config.Config, version string, activation compatibility.Activation, authorizer domain.WriteAuthorizer, options ...Option) (*app.ConfluenceService, error) {
	resolved := resolveOptions(options)
	cf, scheduler, err := confluenceAdapter(cfg, version, 0, 0, authorizer, resolved)
	if err != nil {
		return nil, err
	}
	provider, err := confluenceadapter.NewCommentMutationProvider(cf, activation)
	if err != nil {
		return nil, err
	}
	activationCopy := activation
	return app.NewConfluenceService(app.ConfluenceDependencies{
		Store: cf, Users: cf.ResolveUser, Assets: cf, BaseURL: cfg.ConfluenceURL,
		Verifier: cf, Config: cfg, JiraBaseURL: cfg.JiraURL,
		JiraReadFactory: func() (domain.Tracker, string) {
			return optionalJiraReadScheduled(cfg, version, scheduler, resolved)
		},
		CommentMutator: provider, CommentPreparer: provider,
		CommentMutationActivation: &activationCopy,
	}), nil
}

// NewConfluenceScheduled shares one bounded scheduler with Confluence and
// optional Jira macro reads.
func NewConfluenceScheduled(cfg *config.Config, version string, maxInFlight, requestsPerSecond int, options ...Option) (*app.ConfluenceService, error) {
	return NewConfluenceScheduledWithWriteAuthorizer(cfg, version, maxInFlight, requestsPerSecond, nil, options...)
}

func NewConfluenceScheduledWithWriteAuthorizer(cfg *config.Config, version string, maxInFlight, requestsPerSecond int, authorizer domain.WriteAuthorizer, options ...Option) (*app.ConfluenceService, error) {
	resolved := resolveOptions(options)
	cf, scheduler, err := confluenceAdapter(cfg, version, maxInFlight, requestsPerSecond, authorizer, resolved)
	if err != nil {
		return nil, err
	}
	return app.NewConfluenceService(app.ConfluenceDependencies{
		Store: cf, Users: cf.ResolveUser, Assets: cf, BaseURL: cfg.ConfluenceURL,
		Verifier: cf, Config: cfg, JiraBaseURL: cfg.JiraURL,
		JiraReadFactory: func() (domain.Tracker, string) {
			return optionalJiraReadScheduled(cfg, version, scheduler, resolved)
		},
		RequestMaxInFlight: maxInFlight, RequestsPerSecond: requestsPerSecond,
	}), nil
}

func confluenceAdapter(cfg *config.Config, version string, maxInFlight, requestsPerSecond int, authorizer domain.WriteAuthorizer, resolved options) (*confluenceadapter.Confluence, *httpx.Scheduler, error) {
	if maxInFlight == 0 && requestsPerSecond != 0 {
		return nil, nil, fmt.Errorf("%w: request pacing requires a positive in-flight bound", domain.ErrUsage)
	}
	if cfg == nil || cfg.ConfluenceURL == "" {
		return nil, nil, fmt.Errorf("%w: Confluence URL not set — run `atl config set --confluence-url https://confluence.example.com` (or export ATL_CONFLUENCE_URL); see `atl auth status`", domain.ErrConfig)
	}
	if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", domain.ErrUsage, err)
	}
	token, err := auth.Token(auth.Confluence)
	if err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			return nil, nil, fmt.Errorf("%w: %v", domain.ErrConfig, err)
		}
		return nil, nil, err
	}
	var scheduler *httpx.Scheduler
	if maxInFlight != 0 {
		scheduler, err = httpx.NewScheduler(maxInFlight, requestsPerSecond)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: invalid request schedule: %v", domain.ErrUsage, err)
		}
	}
	cf, err := confluenceadapter.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, scheduler, confluenceTLSOptions(cfg), confluenceOptions(authorizer, resolved)...)
	if err != nil {
		return nil, nil, err
	}
	return cf, scheduler, nil
}

func optionalJiraReadScheduled(cfg *config.Config, version string, scheduler *httpx.Scheduler, resolved options) (domain.Tracker, string) {
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
	reader, err := jiraadapter.NewWithSchedulerTLS(cfg.JiraURL, token, version, scheduler, jiraTLSOptions(cfg), jiraOptions(nil, resolved)...)
	if err != nil {
		return nil, "Jira transport configuration is invalid"
	}
	return reader, ""
}

// NewJira wires the ordinary Jira service.
func NewJira(cfg *config.Config, version string, options ...Option) (*app.JiraService, error) {
	return NewJiraWithWriteAuthorizer(cfg, version, nil, options...)
}

// LoadJira owns the production config-load boundary used by MCP and other
// hosts that have no independent config projection.
func LoadJira(version string, options ...Option) (*app.JiraService, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return NewJira(cfg, version, options...)
}

func NewJiraWithWriteAuthorizer(cfg *config.Config, version string, authorizer domain.WriteAuthorizer, options ...Option) (*app.JiraService, error) {
	resolved := resolveOptions(options)
	j, err := jiraAdapter(cfg, version, authorizer, resolved)
	if err != nil {
		return nil, err
	}
	return app.NewJiraService(app.JiraDependencies{
		Tracker: j, Agile: j, Structure: j, BaseURL: cfg.JiraURL, Config: cfg,
		ConfluenceBaseURL: cfg.ConfluenceURL,
		ConfluenceGraphFactory: func() (domain.ConfluenceGraphPageMetadataReader, string) {
			return optionalConfluenceGraphRead(cfg, version, resolved)
		},
		ConfluenceReferenceFactory: func() (app.ConfluencePageReferenceResolver, app.DependencySetupStatus) {
			return optionalConfluenceReferenceResolver(cfg, version, resolved)
		},
		WriteAuthorizer: authorizer,
	}), nil
}

func optionalConfluenceReferenceResolver(cfg *config.Config, version string, resolved options) (app.ConfluencePageReferenceResolver, app.DependencySetupStatus) {
	if cfg == nil || cfg.ConfluenceURL == "" {
		return nil, app.DependencyNotConfigured
	}
	if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
		return nil, app.DependencyInvalidConfiguration
	}
	token, err := auth.Token(auth.Confluence)
	if err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			return nil, app.DependencyCredentialsMissing
		}
		return nil, app.DependencyCredentialsUnavailable
	}
	reader, err := confluenceadapter.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, nil, confluenceTLSOptions(cfg), confluenceOptions(nil, resolved)...)
	if err != nil {
		return nil, app.DependencyInvalidConfiguration
	}
	return app.NewConfluenceService(app.ConfluenceDependencies{
		Store: reader, BaseURL: cfg.ConfluenceURL, Config: cfg,
	}), app.DependencyReady
}

func optionalConfluenceGraphRead(cfg *config.Config, version string, resolved options) (domain.ConfluenceGraphPageMetadataReader, string) {
	if cfg == nil || cfg.ConfluenceURL == "" {
		return nil, string(app.DependencyNotConfigured)
	}
	if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
		return nil, string(app.DependencyInvalidConfiguration)
	}
	token, err := auth.Token(auth.Confluence)
	if err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			return nil, string(app.DependencyCredentialsMissing)
		}
		return nil, string(app.DependencyCredentialsUnavailable)
	}
	reader, err := confluenceadapter.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, nil, confluenceTLSOptions(cfg), confluenceOptions(nil, resolved)...)
	if err != nil {
		return nil, string(app.DependencyInvalidConfiguration)
	}
	return reader, ""
}

// NewEnvironment composes each optional metadata reader independently.
func NewEnvironment(cfg *config.Config, version string, options ...Option) *app.EnvironmentService {
	resolved := resolveOptions(options)
	deps := app.EnvironmentDependencies{
		JiraSetup: app.DependencyNotConfigured, ConfluenceSetup: app.DependencyNotConfigured,
	}
	if cfg == nil {
		return app.NewEnvironmentService(cfg, deps)
	}
	deps.Jira, deps.JiraSetup = jiraEnvironmentReader(cfg, version, resolved)
	deps.Confluence, deps.ConfluenceSetup = confluenceEnvironmentReader(cfg, version, resolved)
	return app.NewEnvironmentService(cfg, deps)
}

func jiraEnvironmentReader(cfg *config.Config, version string, resolved options) (domain.JiraTimeSemanticsReader, app.DependencySetupStatus) {
	if cfg.JiraURL == "" {
		return nil, app.DependencyNotConfigured
	}
	if err := config.CheckSecureURL(cfg.JiraURL); err != nil {
		return nil, app.DependencyInvalidConfiguration
	}
	token, err := auth.Token(auth.Jira)
	if err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			return nil, app.DependencyCredentialsMissing
		}
		return nil, app.DependencyCredentialsUnavailable
	}
	reader, err := jiraadapter.NewWithSchedulerTLS(cfg.JiraURL, token, version, nil, jiraTLSOptions(cfg), jiraOptions(nil, resolved)...)
	if err != nil {
		return nil, app.DependencyInvalidConfiguration
	}
	return reader, app.DependencyReady
}

func confluenceEnvironmentReader(cfg *config.Config, version string, resolved options) (domain.ConfluenceTimeSemanticsReader, app.DependencySetupStatus) {
	if cfg.ConfluenceURL == "" {
		return nil, app.DependencyNotConfigured
	}
	if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
		return nil, app.DependencyInvalidConfiguration
	}
	token, err := auth.Token(auth.Confluence)
	if err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			return nil, app.DependencyCredentialsMissing
		}
		return nil, app.DependencyCredentialsUnavailable
	}
	reader, err := confluenceadapter.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, nil, confluenceTLSOptions(cfg), confluenceOptions(nil, resolved)...)
	if err != nil {
		return nil, app.DependencyInvalidConfiguration
	}
	return reader, app.DependencyReady
}

// VerifyConfluence qualifies a URL before constructing a verifier, then
// delegates the transport-neutral whoami operation to app.
func VerifyConfluence(ctx context.Context, rawURL, token, version string, cfg *config.Config, options ...Option) (string, error) {
	if err := config.CheckSecureURL(rawURL); err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	client, err := confluenceadapter.NewWithSchedulerTLS(rawURL, token, version, nil, confluenceTLSOptions(cfg), confluenceOptions(nil, resolveOptions(options))...)
	if err != nil {
		return "", err
	}
	return app.VerifyConfluence(ctx, client)
}

// VerifyJira mirrors VerifyConfluence for Jira.
func VerifyJira(ctx context.Context, rawURL, token, version string, cfg *config.Config, options ...Option) (string, error) {
	if err := config.CheckSecureURL(rawURL); err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	client, err := jiraadapter.NewWithSchedulerTLS(rawURL, token, version, nil, jiraTLSOptions(cfg), jiraOptions(nil, resolveOptions(options))...)
	if err != nil {
		return "", err
	}
	return app.VerifyJira(ctx, client)
}

// DoctorDependencies projects concrete configuration, credentials, and TLS
// health into app-owned values. The reader closure captures the qualified
// config so app never receives a config-shaped construction seam.
func DoctorDependencies(options ...Option) app.DoctorDependencies {
	resolved := resolveOptions(options)
	cfgInspection := config.Inspect()
	credentialInspection := auth.Inspect()
	cfg := cfgInspection.Effective
	transport := config.TransportProjection(cfg)
	return app.DoctorDependencies{
		Config: app.DoctorConfigInspection{
			Status: cfgInspection.Status, Reason: cfgInspection.Reason,
			DirectorySource: cfgInspection.DirectorySource,
			File: app.DoctorFileInspection{
				Present: cfgInspection.File.Present, Status: cfgInspection.File.Status,
				OwnerOnly: cfgInspection.File.OwnerOnly, PermissionKnown: cfgInspection.File.PermissionKnown,
			},
			ConfluenceURL: cfg.ConfluenceURL, ConfluenceURLSource: cfgInspection.ConfluenceURLSource,
			ConfluenceURLStatus: doctorURLStatus(cfg.ConfluenceURL),
			JiraURL:             cfg.JiraURL, JiraURLSource: cfgInspection.JiraURLSource,
			JiraURLStatus: doctorURLStatus(cfg.JiraURL),
			ReadOnly:      cfg.ReadOnly,
			Transport: app.DoctorTransport{
				Confluence: doctorCABundle(cfg.CABundle(config.TransportServiceConfluence), transport.Confluence),
				Jira:       doctorCABundle(cfg.CABundle(config.TransportServiceJira), transport.Jira),
			},
		},
		Credentials: app.DoctorCredentialInspection{
			Store: app.DoctorCredentialStore{
				Present: credentialInspection.Store.Present, Status: credentialInspection.Store.Status,
				OwnerOnly: credentialInspection.Store.OwnerOnly, PermissionKnown: credentialInspection.Store.PermissionKnown,
			},
			Confluence: app.DoctorCredential{
				Present: credentialInspection.Confluence.Present,
				Source:  credentialInspection.Confluence.Source, Status: credentialInspection.Confluence.Status,
			},
			Jira: app.DoctorCredential{
				Present: credentialInspection.Jira.Present,
				Source:  credentialInspection.Jira.Source, Status: credentialInspection.Jira.Status,
			},
		},
		Token: func(service string) (string, error) {
			return auth.Token(auth.Service(service))
		},
		Reader: func(service, rawURL, token, version string) (domain.ServerMetadataReader, error) {
			switch service {
			case domain.ServerProductJira:
				return jiraadapter.NewWithSchedulerTLS(rawURL, token, version, nil, jiraTLSOptions(cfg), jiraOptions(nil, resolved)...)
			case domain.ServerProductConfluence:
				return confluenceadapter.NewWithSchedulerTLS(rawURL, token, version, nil, confluenceTLSOptions(cfg), confluenceOptions(nil, resolved)...)
			default:
				return nil, fmt.Errorf("%w: unsupported backend service", domain.ErrConfig)
			}
		},
	}
}

func doctorURLStatus(rawURL string) string {
	if rawURL == "" {
		return "not_configured"
	}
	if err := config.CheckSecureURL(rawURL); err != nil {
		return "invalid"
	}
	return "valid"
}

func doctorCABundle(path string, summary config.BackendTransportSummary) app.DoctorCABundle {
	out := app.DoctorCABundle{
		Configured: summary.CABundleConfigured, Source: summary.CABundleSource, Status: "not_configured",
	}
	if !out.Configured {
		return out
	}
	if err := httpx.ValidateCABundle(path); err != nil {
		out.Status = "invalid"
		out.Reason = "ca_bundle_invalid"
		return out
	}
	out.Status = "available"
	return out
}

// RunDoctor supplies production remote composition without changing the app's
// content-free diagnostic and classification logic.
func RunDoctor(ctx context.Context, opts app.DoctorOptions, options ...Option) (*app.DoctorResult, error) {
	opts.Dependencies = DoctorDependencies(options...)
	return app.RunDoctor(ctx, opts)
}
