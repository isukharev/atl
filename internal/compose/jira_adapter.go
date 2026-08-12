package compose

import (
	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

// jiraAdapter is the shared secure concrete boundary for broad and focused
// Jira services. Keeping it here prevents feature composition from drifting in
// credentials, TLS, URL qualification, or write authorization.
func jiraAdapter(cfg *config.Config, version string, authorizer domain.WriteAuthorizer, resolved options) (*jiraadapter.Jira, error) {
	return jiraAdapterScheduled(cfg, version, nil, authorizer, resolved)
}
