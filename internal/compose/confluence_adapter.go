package compose

import (
	"errors"
	"fmt"

	confluenceadapter "github.com/isukharev/atl/internal/adapter/confluence"
	"github.com/isukharev/atl/internal/auth"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

func confluenceAdapterScheduled(cfg *config.Config, version string, scheduler *httpx.Scheduler, authorizer domain.WriteAuthorizer, resolved options) (*confluenceadapter.Confluence, error) {
	token, err := confluenceAdapterCredentials(cfg)
	if err != nil {
		return nil, err
	}
	return newConfluenceAdapterScheduled(cfg, token, version, scheduler, authorizer, resolved)
}

func confluenceAdapterCredentials(cfg *config.Config) (string, error) {
	if cfg == nil || cfg.ConfluenceURL == "" {
		return "", fmt.Errorf("%w: Confluence URL not set — run `atl config set --confluence-url https://confluence.example.com` (or export ATL_CONFLUENCE_URL); see `atl auth status`", domain.ErrConfig)
	}
	if err := config.CheckSecureURL(cfg.ConfluenceURL); err != nil {
		return "", fmt.Errorf("%w: %w", domain.ErrUsage, err)
	}
	token, err := auth.Token(auth.Confluence)
	if err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			return "", fmt.Errorf("%w: %v", domain.ErrConfig, err)
		}
		return "", err
	}
	return token, nil
}

func newConfluenceAdapterScheduled(cfg *config.Config, token, version string, scheduler *httpx.Scheduler, authorizer domain.WriteAuthorizer, resolved options) (*confluenceadapter.Confluence, error) {
	adapter, err := confluenceadapter.NewWithSchedulerTLS(cfg.ConfluenceURL, token, version, scheduler, confluenceTLSOptions(cfg), confluenceOptions(authorizer, resolved)...)
	if err != nil {
		return nil, err
	}
	return adapter, nil
}
