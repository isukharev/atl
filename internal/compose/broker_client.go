package compose

import (
	"fmt"

	"github.com/isukharev/atl/internal/adapter/brokerclient"
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func brokerMode(cfg *config.Config) bool {
	return cfg != nil && config.EffectiveConnectionMode(cfg.ConnectionMode) == config.ConnectionModeBroker
}

// LoadBrokerDiscovery loads configuration and selects a lazy Broker session
// reader. It cannot resolve a backend PAT or fall back to a direct adapter.
func LoadBrokerDiscovery(service, version string) (domain.BrokerDiscoveryReader, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return newBrokerClient(cfg, service, version, nil)
}

// NewBrokerCacheQualification composes only the Confluence Broker session
// client used by the explicit qualified handoff. It cannot load an upstream
// PAT and refuses direct mode.
func NewBrokerCacheQualification(cfg *config.Config, version string) (domain.BrokerCacheQualificationReader, error) {
	client, err := newBrokerClient(cfg, domain.ServerProductConfluence, version, nil)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func newBrokerClient(cfg *config.Config, service, version string, scheduler *httpx.Scheduler) (*brokerclient.Client, error) {
	if cfg == nil || cfg.Broker == nil || !brokerMode(cfg) {
		return nil, fmt.Errorf("%w: Broker client is not configured", domain.ErrConfig)
	}
	if err := config.ValidateBrokerClientConfig(cfg.ConnectionMode, cfg.Broker); err != nil {
		return nil, fmt.Errorf("%w: invalid Broker client configuration", domain.ErrConfig)
	}
	sessionPath := ""
	switch service {
	case domain.ServerProductJira:
		sessionPath = cfg.Broker.JiraSessionFile
	case domain.ServerProductConfluence:
		sessionPath = cfg.Broker.ConfluenceSessionFile
	default:
		return nil, fmt.Errorf("%w: unsupported Broker service", domain.ErrConfig)
	}
	if sessionPath == "" {
		return nil, fmt.Errorf("%w: Broker session is not configured for the selected service", domain.ErrConfig)
	}
	tlsOptions, _, err := httpx.QualifiedTLSOptions(cfg.Broker.CAFile)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Broker trust configuration", domain.ErrConfig)
	}
	return brokerclient.New(brokerclient.Config{
		BaseURL: cfg.Broker.BaseURL, BrokerID: cfg.Broker.BrokerID, Audience: cfg.Broker.Audience,
		Version: version, Session: brokerclient.FileSessionLoader{Path: sessionPath}, Scheduler: scheduler, TLS: tlsOptions,
	})
}

func newBrokerConfluenceService(cfg *config.Config, version string, maxInFlight, requestsPerSecond int) (*app.ConfluenceService, error) {
	var scheduler *httpx.Scheduler
	var err error
	if maxInFlight == 0 && requestsPerSecond != 0 {
		return nil, fmt.Errorf("%w: request pacing requires a positive in-flight bound", domain.ErrUsage)
	}
	if maxInFlight != 0 {
		scheduler, err = httpx.NewScheduler(maxInFlight, requestsPerSecond)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid request schedule", domain.ErrUsage)
		}
	}
	client, err := newBrokerClient(cfg, domain.ServerProductConfluence, version, scheduler)
	if err != nil {
		return nil, err
	}
	store, err := brokerclient.NewConfluence(client)
	if err != nil {
		return nil, err
	}
	return app.NewConfluenceService(app.ConfluenceDependencies{
		Store: store, Config: cfg, RequestMaxInFlight: maxInFlight, RequestsPerSecond: requestsPerSecond,
	}), nil
}

func newBrokerJiraService(cfg *config.Config, version string, authorizer domain.WriteAuthorizer) (*app.JiraService, error) {
	client, err := newBrokerClient(cfg, domain.ServerProductJira, version, nil)
	if err != nil {
		return nil, err
	}
	tracker, err := brokerclient.NewJira(client)
	if err != nil {
		return nil, err
	}
	return app.NewJiraService(app.JiraDependencies{Tracker: tracker, Agile: tracker, Structure: tracker, Config: cfg, WriteAuthorizer: authorizer}), nil
}
