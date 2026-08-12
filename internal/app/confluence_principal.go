package app

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

// CurrentPrincipal returns only the stable authenticated Confluence identity
// needed to bind a qualified corpus capture. Display names and email never
// participate in the durable scope digest.
func (s *ConfluenceService) CurrentPrincipal(ctx context.Context) (string, error) {
	reader, ok := s.store.(domain.ConfluenceCurrentUserReader)
	if !ok {
		return "", fmt.Errorf("%w: stable Confluence principal identity is unavailable", domain.ErrCheckFailed)
	}
	identity, err := reader.CurrentConfluenceUser(ctx)
	if err != nil {
		return "", err
	}
	if err := domain.ValidateConfluenceUserIdentity(identity); err != nil {
		return "", err
	}
	return identity.ID, nil
}
