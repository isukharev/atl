package compose

import (
	"context"
	"fmt"

	confluenceadapter "github.com/isukharev/atl/internal/adapter/confluence"
	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/auth"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

// DoctorDependencies projects concrete configuration, credentials, and the
// selected services' TLS health into app-owned values.
func DoctorDependencies(service string, options ...Option) app.DoctorDependencies {
	return doctorDependencies(service, httpx.ValidateCABundle, options...)
}

func doctorDependencies(service string, validateCABundle func(string) error, options ...Option) app.DoctorDependencies {
	resolved := resolveOptions(options)
	cfgInspection := config.Inspect()
	cfg := cfgInspection.Effective
	if cfgInspection.Status == "invalid" || cfgInspection.Status == "unavailable" {
		deps := brokerDoctorDependencies(service, cfgInspection, validateCABundle)
		deps.Config.ConnectionMode = "invalid"
		deps.RemoteSkipReason = "invalid_configuration"
		return deps
	}
	mode := config.EffectiveConnectionMode(cfg.ConnectionMode)
	if mode == config.ConnectionModeBroker {
		return brokerDoctorDependencies(service, cfgInspection, validateCABundle)
	}
	if mode != config.ConnectionModeDirect {
		deps := brokerDoctorDependencies(service, cfgInspection, validateCABundle)
		deps.Config.ConnectionMode = "invalid"
		deps.RemoteSkipReason = "invalid_configuration"
		return deps
	}
	credentialInspection := auth.Inspect()
	transport := config.TransportProjection(cfg)
	return app.DoctorDependencies{
		Config: app.DoctorConfigInspection{
			ConnectionMode: config.ConnectionModeDirect,
			Status:         cfgInspection.Status, Reason: cfgInspection.Reason,
			DirectorySource: cfgInspection.DirectorySource,
			File: app.DoctorFileInspection{
				Present: cfgInspection.File.Present, Status: cfgInspection.File.Status,
				OwnerOnly: cfgInspection.File.OwnerOnly, PermissionKnown: cfgInspection.File.PermissionKnown,
			},
			ConfluenceURL: cfg.ConfluenceURL, ConfluenceURLSource: cfgInspection.ConfluenceURLSource,
			ConfluenceURLStatus: doctorURLStatus(cfg.ConfluenceURL),
			JiraURL:             cfg.JiraURL, JiraURLSource: cfgInspection.JiraURLSource,
			JiraURLStatus: doctorURLStatus(cfg.JiraURL), ReadOnly: cfg.ReadOnly,
			Transport: doctorTransportProjection(service, cfg, transport, validateCABundle),
		},
		Credentials: app.DoctorCredentialInspection{
			Store: app.DoctorCredentialStore{
				Present: credentialInspection.Store.Present, Status: credentialInspection.Store.Status,
				OwnerOnly: credentialInspection.Store.OwnerOnly, PermissionKnown: credentialInspection.Store.PermissionKnown,
			},
			Confluence: app.DoctorCredential{Present: credentialInspection.Confluence.Present, Source: credentialInspection.Confluence.Source, Status: credentialInspection.Confluence.Status},
			Jira:       app.DoctorCredential{Present: credentialInspection.Jira.Present, Source: credentialInspection.Jira.Source, Status: credentialInspection.Jira.Status},
		},
		Token: func(service string) (string, error) { return auth.Token(auth.Service(service)) },
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

// RunDoctor supplies production remote composition without changing the app's
// content-free diagnostic and classification logic.
func RunDoctor(ctx context.Context, opts app.DoctorOptions, options ...Option) (*app.DoctorResult, error) {
	opts.Dependencies = DoctorDependencies(opts.Service, options...)
	return app.RunDoctor(ctx, opts)
}
