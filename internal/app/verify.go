package app

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/adapter/confluence"
	"github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

// VerifyConfluence confirms a Confluence URL+PAT by calling whoami and returns
// the authenticated user's display name. The URL is checked for https first so
// the PAT is never transmitted in cleartext. Nothing is persisted here — the
// auth-login wizard persists only after this succeeds. ctx is threaded to the
// adapter so a server/MCP caller keeps cancellation/deadline propagation.
func VerifyConfluence(ctx context.Context, url, token, version string) (string, error) {
	return VerifyConfluenceWithConfig(ctx, url, token, version, nil)
}

func VerifyConfluenceWithConfig(ctx context.Context, url, token, version string, cfg *config.Config) (string, error) {
	if err := config.CheckSecureURL(url); err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	client, err := confluence.NewWithSchedulerTLS(url, token, version, nil, confluenceTLSOptions(cfg))
	if err != nil {
		return "", err
	}
	return client.Whoami(ctx)
}

// VerifyJira mirrors VerifyConfluence for Jira.
func VerifyJira(ctx context.Context, url, token, version string) (string, error) {
	return VerifyJiraWithConfig(ctx, url, token, version, nil)
}

func VerifyJiraWithConfig(ctx context.Context, url, token, version string, cfg *config.Config) (string, error) {
	if err := config.CheckSecureURL(url); err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	client, err := jira.NewWithSchedulerTLS(url, token, version, nil, jiraTLSOptions(cfg))
	if err != nil {
		return "", err
	}
	return client.Whoami(ctx)
}
