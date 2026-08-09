package compose

import (
	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

// NewConfluenceLabelsWithWriteAuthorizer composes only the label capability
// ports plus the shared exact page-reference resolver used by label listing.
func NewConfluenceLabelsWithWriteAuthorizer(cfg *config.Config, version string, authorizer domain.WriteAuthorizer) (*app.ConfluenceLabelService, error) {
	cf, _, err := confluenceAdapter(cfg, version, 0, 0, authorizer)
	if err != nil {
		return nil, err
	}
	references := app.NewConfluenceService(app.ConfluenceDependencies{Store: cf, BaseURL: cfg.ConfluenceURL})
	return app.NewConfluenceLabelService(app.ConfluenceLabelDependencies{
		Reader: cf, Writer: cf, ResolveReference: references.ResolvePageReference,
	}), nil
}

// NewJiraWatchersWithWriteAuthorizer composes the focused watcher feature.
func NewJiraWatchersWithWriteAuthorizer(cfg *config.Config, version string, authorizer domain.WriteAuthorizer) (*app.JiraWatcherService, error) {
	j, err := jiraAdapter(cfg, version, authorizer)
	if err != nil {
		return nil, err
	}
	return app.NewJiraWatcherService(app.JiraWatcherDependencies{Reader: j, Writer: j, CurrentUser: j}), nil
}

// NewJiraWorklogsWithWriteAuthorizer composes the focused worklog feature.
func NewJiraWorklogsWithWriteAuthorizer(cfg *config.Config, version string, authorizer domain.WriteAuthorizer) (*app.JiraWorklogService, error) {
	j, err := jiraAdapter(cfg, version, authorizer)
	if err != nil {
		return nil, err
	}
	return app.NewJiraWorklogService(app.JiraWorklogDependencies{Reader: j, Writer: j, CurrentUser: j}), nil
}
