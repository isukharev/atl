package app

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

// VerifyConfluence confirms a composed Confluence verifier. URL, credential,
// TLS, and adapter qualification belong to the outer composition owner.
func VerifyConfluence(ctx context.Context, verifier domain.Verifier) (string, error) {
	if verifier == nil {
		return "", fmt.Errorf("%w: Confluence verifier is unavailable", domain.ErrConfig)
	}
	return verifier.Whoami(ctx)
}

// VerifyJira confirms a composed Jira verifier.
func VerifyJira(ctx context.Context, verifier domain.Verifier) (string, error) {
	if verifier == nil {
		return "", fmt.Errorf("%w: Jira verifier is unavailable", domain.ErrConfig)
	}
	return verifier.Whoami(ctx)
}
