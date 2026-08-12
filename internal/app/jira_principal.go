package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// CurrentPrincipal returns the strongest stable Jira identity available on
// the configured backend without exposing presentation or email fields.
func (s *JiraService) CurrentPrincipal(ctx context.Context) (string, error) {
	user, err := s.tr.CurrentUser(ctx)
	if err != nil {
		return "", err
	}
	if user == nil {
		return "", fmt.Errorf("%w: stable Jira principal identity is unavailable", domain.ErrCheckFailed)
	}
	for _, value := range []string{user.AccountID, user.Key, user.Name} {
		if strings.TrimSpace(value) != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("%w: stable Jira principal identity is unavailable", domain.ErrCheckFailed)
}
