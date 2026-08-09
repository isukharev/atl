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
func NewCompatibility(cfg *config.Config, settings compatibility.Settings, version string) *app.CompatibilityService {
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
		reader, err := confluenceadapter.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, nil, confluenceTLSOptions(cfg))
		if err != nil {
			return nil, app.DependencyInvalidConfiguration
		}
		return reader, app.DependencyReady
	})
}

// NewConfluence wires the ordinary Confluence service.
func NewConfluence(cfg *config.Config, version string) (*app.ConfluenceService, error) {
	return NewConfluenceWithWriteAuthorizer(cfg, version, nil)
}

// LoadConfluence owns the production config-load boundary used by hosts that
// do not already need a qualified config value.
func LoadConfluence(version string) (*app.ConfluenceService, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return NewConfluence(cfg, version)
}

// NewConfluenceWithWriteAuthorizer adds the transport-neutral write guard to
// every concrete Confluence capability.
func NewConfluenceWithWriteAuthorizer(cfg *config.Config, version string, authorizer domain.WriteAuthorizer) (*app.ConfluenceService, error) {
	return NewConfluenceScheduledWithWriteAuthorizer(cfg, version, 0, 0, authorizer)
}

// NewConfluenceCommentMutations composes the explicitly activated mutation
// provider without affecting ordinary Confluence commands.
func NewConfluenceCommentMutations(cfg *config.Config, version string, activation compatibility.Activation) (*app.ConfluenceService, error) {
	return NewConfluenceCommentMutationsWithWriteAuthorizer(cfg, version, activation, nil)
}

func NewConfluenceCommentMutationsWithWriteAuthorizer(cfg *config.Config, version string, activation compatibility.Activation, authorizer domain.WriteAuthorizer) (*app.ConfluenceService, error) {
	cf, scheduler, err := confluenceAdapter(cfg, version, 0, 0, authorizer)
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
		Verifier: cf, Config: cfg,
		JiraReadFactory: func() (domain.Tracker, string) {
			return optionalJiraReadScheduled(cfg, version, scheduler)
		},
		CommentMutator: provider, CommentPreparer: provider,
		CommentMutationActivation: &activationCopy,
	}), nil
}

// NewConfluenceScheduled shares one bounded scheduler with Confluence and
// optional Jira macro reads.
func NewConfluenceScheduled(cfg *config.Config, version string, maxInFlight, requestsPerSecond int) (*app.ConfluenceService, error) {
	return NewConfluenceScheduledWithWriteAuthorizer(cfg, version, maxInFlight, requestsPerSecond, nil)
}

func NewConfluenceScheduledWithWriteAuthorizer(cfg *config.Config, version string, maxInFlight, requestsPerSecond int, authorizer domain.WriteAuthorizer) (*app.ConfluenceService, error) {
	cf, scheduler, err := confluenceAdapter(cfg, version, maxInFlight, requestsPerSecond, authorizer)
	if err != nil {
		return nil, err
	}
	return app.NewConfluenceService(app.ConfluenceDependencies{
		Store: cf, Users: cf.ResolveUser, Assets: cf, BaseURL: cfg.ConfluenceURL,
		Verifier: cf, Config: cfg,
		JiraReadFactory: func() (domain.Tracker, string) {
			return optionalJiraReadScheduled(cfg, version, scheduler)
		},
		RequestMaxInFlight: maxInFlight, RequestsPerSecond: requestsPerSecond,
	}), nil
}

func confluenceAdapter(cfg *config.Config, version string, maxInFlight, requestsPerSecond int, authorizer domain.WriteAuthorizer) (*confluenceadapter.Confluence, *httpx.Scheduler, error) {
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
	var options []confluenceadapter.Option
	if authorizer != nil {
		options = append(options, confluenceadapter.WithWriteAuthorizer(authorizer))
	}
	cf, err := confluenceadapter.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, scheduler, confluenceTLSOptions(cfg), options...)
	if err != nil {
		return nil, nil, err
	}
	return cf, scheduler, nil
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
	reader, err := jiraadapter.NewWithSchedulerTLS(cfg.JiraURL, token, version, scheduler, jiraTLSOptions(cfg))
	if err != nil {
		return nil, "Jira transport configuration is invalid"
	}
	return reader, ""
}

// NewJira wires the ordinary Jira service.
func NewJira(cfg *config.Config, version string) (*app.JiraService, error) {
	return NewJiraWithWriteAuthorizer(cfg, version, nil)
}

// LoadJira owns the production config-load boundary used by MCP and other
// hosts that have no independent config projection.
func LoadJira(version string) (*app.JiraService, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return NewJira(cfg, version)
}

func NewJiraWithWriteAuthorizer(cfg *config.Config, version string, authorizer domain.WriteAuthorizer) (*app.JiraService, error) {
	if cfg == nil || cfg.JiraURL == "" {
		return nil, fmt.Errorf("%w: Jira URL not set — run `atl config set --jira-url https://jira.example.com` (or export ATL_JIRA_URL); see `atl auth status`", domain.ErrConfig)
	}
	if err := config.CheckSecureURL(cfg.JiraURL); err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrUsage, err)
	}
	token, err := auth.Token(auth.Jira)
	if err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			return nil, fmt.Errorf("%w: %v", domain.ErrConfig, err)
		}
		return nil, err
	}
	var options []jiraadapter.Option
	if authorizer != nil {
		options = append(options, jiraadapter.WithWriteAuthorizer(authorizer))
	}
	j, err := jiraadapter.NewWithSchedulerTLS(cfg.JiraURL, token, version, nil, jiraTLSOptions(cfg), options...)
	if err != nil {
		return nil, err
	}
	return app.NewJiraService(app.JiraDependencies{
		Tracker: j, Agile: j, Structure: j, BaseURL: cfg.JiraURL, Config: cfg,
		ConfluenceGraphFactory: func() (domain.ConfluenceGraphPageMetadataReader, string) {
			return optionalConfluenceGraphRead(cfg, version)
		},
		WriteAuthorizer: authorizer,
	}), nil
}

func optionalConfluenceGraphRead(cfg *config.Config, version string) (domain.ConfluenceGraphPageMetadataReader, string) {
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
	reader, err := confluenceadapter.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, nil, confluenceTLSOptions(cfg))
	if err != nil {
		return nil, string(app.DependencyInvalidConfiguration)
	}
	return reader, ""
}

// NewEnvironment composes each optional metadata reader independently.
func NewEnvironment(cfg *config.Config, version string) *app.EnvironmentService {
	deps := app.EnvironmentDependencies{
		JiraSetup: app.DependencyNotConfigured, ConfluenceSetup: app.DependencyNotConfigured,
	}
	if cfg == nil {
		return app.NewEnvironmentService(cfg, deps)
	}
	deps.Jira, deps.JiraSetup = jiraEnvironmentReader(cfg, version)
	deps.Confluence, deps.ConfluenceSetup = confluenceEnvironmentReader(cfg, version)
	return app.NewEnvironmentService(cfg, deps)
}

func jiraEnvironmentReader(cfg *config.Config, version string) (domain.JiraTimeSemanticsReader, app.DependencySetupStatus) {
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
	reader, err := jiraadapter.NewWithSchedulerTLS(cfg.JiraURL, token, version, nil, jiraTLSOptions(cfg))
	if err != nil {
		return nil, app.DependencyInvalidConfiguration
	}
	return reader, app.DependencyReady
}

func confluenceEnvironmentReader(cfg *config.Config, version string) (domain.ConfluenceTimeSemanticsReader, app.DependencySetupStatus) {
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
	reader, err := confluenceadapter.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, nil, confluenceTLSOptions(cfg))
	if err != nil {
		return nil, app.DependencyInvalidConfiguration
	}
	return reader, app.DependencyReady
}

// VerifyConfluence qualifies a URL before constructing a verifier, then
// delegates the transport-neutral whoami operation to app.
func VerifyConfluence(ctx context.Context, rawURL, token, version string, cfg *config.Config) (string, error) {
	if err := config.CheckSecureURL(rawURL); err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	client, err := confluenceadapter.NewWithSchedulerTLS(rawURL, token, version, nil, confluenceTLSOptions(cfg))
	if err != nil {
		return "", err
	}
	return app.VerifyConfluence(ctx, client)
}

// VerifyJira mirrors VerifyConfluence for Jira.
func VerifyJira(ctx context.Context, rawURL, token, version string, cfg *config.Config) (string, error) {
	if err := config.CheckSecureURL(rawURL); err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	client, err := jiraadapter.NewWithSchedulerTLS(rawURL, token, version, nil, jiraTLSOptions(cfg))
	if err != nil {
		return "", err
	}
	return app.VerifyJira(ctx, client)
}

// DoctorRemoteDependencies provides the exact production credential and
// metadata-reader assembly used by the app-owned diagnostic projection.
func DoctorRemoteDependencies() app.DoctorRemoteDependencies {
	return app.DoctorRemoteDependencies{
		Token: func(service string) (string, error) {
			return auth.Token(auth.Service(service))
		},
		Reader: func(service, rawURL, token, version string, cfg *config.Config) (domain.ServerMetadataReader, error) {
			switch service {
			case domain.ServerProductJira:
				return jiraadapter.NewWithSchedulerTLS(rawURL, token, version, nil, jiraTLSOptions(cfg))
			case domain.ServerProductConfluence:
				return confluenceadapter.NewWithSchedulerTLS(rawURL, token, version, nil, confluenceTLSOptions(cfg))
			default:
				return nil, fmt.Errorf("%w: unsupported backend service", domain.ErrConfig)
			}
		},
	}
}

// RunDoctor supplies production remote composition without changing the app's
// content-free diagnostic and classification logic.
func RunDoctor(ctx context.Context, opts app.DoctorOptions) (*app.DoctorResult, error) {
	opts.RemoteDependencies = DoctorRemoteDependencies()
	return app.RunDoctor(ctx, opts)
}
