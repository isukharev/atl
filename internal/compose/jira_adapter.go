package compose

import (
	"errors"
	"fmt"

	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/auth"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

// jiraAdapter is the shared secure concrete boundary for broad and focused
// Jira services. Keeping it here prevents feature composition from drifting in
// credentials, TLS, URL qualification, or write authorization.
func jiraAdapter(cfg *config.Config, version string, authorizer domain.WriteAuthorizer, resolved options) (*jiraadapter.Jira, error) {
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
	j, err := jiraadapter.NewWithSchedulerTLS(cfg.JiraURL, token, version, nil, jiraTLSOptions(cfg), jiraOptions(authorizer, resolved)...)
	if err != nil {
		return nil, err
	}
	return j, nil
}
