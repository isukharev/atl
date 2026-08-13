package compose

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

// CorpusBuildSelection is the already-validated, content-free composition
// projection. Only selected backends load credentials or construct clients.
type CorpusBuildSelection struct {
	Jira                bool
	Confluence          bool
	MaxInFlight         int
	RequestsPerSecond   int
	GeneratorVersion    string
	GeneratorCommit     string
	BuildState          corpus.BuildState
	QualifiedCacheTrust bool
}

// NewCorpusBuild composes one shared physical-request scheduler and injects a
// deny-all write authorizer into every selected adapter.
func NewCorpusBuild(cfg *config.Config, selection CorpusBuildSelection, values ...Option) (*app.CorpusBuildService, error) {
	if !selection.Jira && !selection.Confluence {
		return nil, fmt.Errorf("%w: corpus build requires a selected backend", domain.ErrUsage)
	}
	if selection.QualifiedCacheTrust && (!selection.Confluence || selection.Jira) {
		return nil, fmt.Errorf("%w: qualified cache trust requires sole Confluence selection", domain.ErrUsage)
	}
	if selection.MaxInFlight <= 0 || selection.RequestsPerSecond <= 0 {
		return nil, fmt.Errorf("%w: corpus build requires a finite request schedule", domain.ErrUsage)
	}
	scheduler, err := httpx.NewScheduler(selection.MaxInFlight, selection.RequestsPerSecond)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid corpus build request schedule", domain.ErrUsage)
	}
	resolved := resolveOptions(values)
	authorizer := corpusDenyAllWrites{}
	dependencies := app.CorpusBuildDependencies{
		GeneratorVersion: selection.GeneratorVersion,
		GeneratorCommit:  selection.GeneratorCommit,
		BuildState:       selection.BuildState,
	}
	if selection.Confluence {
		token, err := confluenceAdapterCredentials(cfg)
		if err != nil {
			return nil, err
		}
		var adapterTLS httpx.TLSOptions
		if selection.QualifiedCacheTrust && !selection.Jira && cfg != nil && cfg.CABundle(config.TransportServiceConfluence) != "" {
			var trustDigest string
			adapterTLS, trustDigest, err = httpx.QualifiedTLSOptions(cfg.CABundle(config.TransportServiceConfluence))
			if err != nil {
				return nil, err
			}
			dependencies.ConfluenceTrustDigest = trustDigest
		} else {
			adapterTLS = confluenceTLSOptions(cfg)
		}
		adapter, err := newConfluenceAdapterScheduledTLS(cfg, token, selection.GeneratorVersion, scheduler, authorizer, resolved, adapterTLS)
		if err != nil {
			return nil, err
		}
		dependencies.Confluence = app.NewConfluenceService(app.ConfluenceDependencies{
			Store: adapter, CorpusMetadata: adapter, Users: adapter.ResolveUser, Assets: adapter,
			BaseURL: cfg.ConfluenceURL, Verifier: adapter, Config: cfg,
			RequestMaxInFlight: selection.MaxInFlight, RequestsPerSecond: selection.RequestsPerSecond,
		})
	}
	if selection.Jira {
		adapter, err := jiraAdapterScheduled(cfg, selection.GeneratorVersion, scheduler, authorizer, resolved)
		if err != nil {
			return nil, err
		}
		dependencies.Jira = app.NewJiraService(app.JiraDependencies{
			Tracker: adapter, Agile: adapter, Structure: adapter,
			BaseURL: cfg.JiraURL, Config: cfg, WriteAuthorizer: authorizer,
		})
	}
	return app.NewCorpusBuildService(dependencies), nil
}

type corpusDenyAllWrites struct{}

func (corpusDenyAllWrites) Authorize(context.Context, domain.WriteAuthorizationRequest) (context.Context, error) {
	return nil, fmt.Errorf("%w: corpus build forbids backend writes", domain.ErrCheckFailed)
}

func (corpusDenyAllWrites) Preflight(domain.WriteAuthorizationRequest) error {
	return fmt.Errorf("%w: corpus build forbids backend writes", domain.ErrCheckFailed)
}
