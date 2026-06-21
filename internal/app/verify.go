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
// auth-login wizard persists only after this succeeds.
func VerifyConfluence(url, token, version string) (string, error) {
	if err := config.CheckSecureURL(url); err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	return confluence.New(url, token, version).Whoami(context.Background())
}

// VerifyJira mirrors VerifyConfluence for Jira.
func VerifyJira(url, token, version string) (string, error) {
	if err := config.CheckSecureURL(url); err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrUsage, err)
	}
	return jira.New(url, token, version).Whoami(context.Background())
}
