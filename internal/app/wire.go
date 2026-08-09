// Package app holds transport-agnostic use-cases. It depends on domain ports
// and the mirror engine, while concrete backend assembly lives in internal/compose.
package app

import (
	"sync"

	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

// DependencySetupStatus is the closed, privacy-safe result vocabulary used by
// outer composition when an optional backend cannot be constructed.
type DependencySetupStatus string

const (
	DependencyReady                  DependencySetupStatus = ""
	DependencyNotConfigured          DependencySetupStatus = "not_configured"
	DependencyInvalidConfiguration   DependencySetupStatus = "invalid_configuration"
	DependencyCredentialsMissing     DependencySetupStatus = "credentials_missing"
	DependencyCredentialsUnavailable DependencySetupStatus = "credentials_unavailable"
)

// ConfluenceService bundles the Confluence use-cases over a DocStore + mirror.
// cfg holds non-secret render configuration; backend access goes through ports.
type ConfluenceService struct {
	store           domain.DocStore
	users           domain.UserResolver
	assets          domain.AssetResolver
	baseURL         string
	verifier        domain.Verifier
	cfg             *config.Config
	jiraBaseURL     string
	jiraRead        domain.Tracker
	jiraReadFactory func() (domain.Tracker, string)
	jiraReadOnce    sync.Once
	jiraReadReason  string

	requestMaxInFlight        int
	requestsPerSecond         int
	commentMutator            domain.ConfluenceCommentMutator
	commentPreparer           domain.ConfluenceInlineCommentPreparer
	commentMutationActivation *compatibility.Activation
}

// ConfluenceDependencies contains only transport-neutral ports and values
// already qualified by the outer composition owner.
type ConfluenceDependencies struct {
	Store                     domain.DocStore
	Users                     domain.UserResolver
	Assets                    domain.AssetResolver
	BaseURL                   string
	Verifier                  domain.Verifier
	Config                    *config.Config
	JiraBaseURL               string
	JiraReadFactory           func() (domain.Tracker, string)
	RequestMaxInFlight        int
	RequestsPerSecond         int
	CommentMutator            domain.ConfluenceCommentMutator
	CommentPreparer           domain.ConfluenceInlineCommentPreparer
	CommentMutationActivation *compatibility.Activation
}

// NewConfluenceService is a pure constructor from domain ports.
func NewConfluenceService(deps ConfluenceDependencies) *ConfluenceService {
	var activation *compatibility.Activation
	if deps.CommentMutationActivation != nil {
		copy := *deps.CommentMutationActivation
		activation = &copy
	}
	return &ConfluenceService{
		store: deps.Store, users: deps.Users, assets: deps.Assets,
		baseURL: deps.BaseURL, verifier: deps.Verifier, cfg: deps.Config,
		jiraBaseURL:        deps.JiraBaseURL,
		jiraReadFactory:    deps.JiraReadFactory,
		requestMaxInFlight: deps.RequestMaxInFlight, requestsPerSecond: deps.RequestsPerSecond,
		commentMutator: deps.CommentMutator, commentPreparer: deps.CommentPreparer,
		commentMutationActivation: activation,
	}
}

// JiraService bundles Jira use-cases over domain ports.
type JiraService struct {
	tr                       domain.Tracker
	agile                    domain.Agile
	structure                domain.StructureReader
	baseURL                  string
	cfg                      *config.Config
	confluenceBaseURL        string
	graphConfluence          domain.ConfluenceGraphPageMetadataReader
	graphConfluenceFactory   func() (domain.ConfluenceGraphPageMetadataReader, string)
	graphConfluenceOnce      sync.Once
	graphConfluenceReason    string
	inverseConfluenceBaseURL string
	inverseConfluence        ConfluencePageReferenceResolver
	inverseConfluenceFactory func() (ConfluencePageReferenceResolver, string)
	inverseConfluenceOnce    sync.Once
	inverseConfluenceReason  string
	writeAuthorizer          domain.WriteAuthorizer
}

// JiraDependencies contains only qualified transport-neutral ports.
type JiraDependencies struct {
	Tracker                    domain.Tracker
	Agile                      domain.Agile
	Structure                  domain.StructureReader
	BaseURL                    string
	Config                     *config.Config
	ConfluenceBaseURL          string
	ConfluenceGraphFactory     func() (domain.ConfluenceGraphPageMetadataReader, string)
	ConfluenceReferenceFactory func() (ConfluencePageReferenceResolver, DependencySetupStatus)
	WriteAuthorizer            domain.WriteAuthorizer
}

// NewJiraService is a pure constructor from domain ports.
func NewJiraService(deps JiraDependencies) *JiraService {
	service := &JiraService{
		tr: deps.Tracker, agile: deps.Agile, structure: deps.Structure,
		baseURL: deps.BaseURL, cfg: deps.Config, confluenceBaseURL: deps.ConfluenceBaseURL,
		graphConfluenceFactory:   deps.ConfluenceGraphFactory,
		inverseConfluenceBaseURL: deps.ConfluenceBaseURL,
		writeAuthorizer:          deps.WriteAuthorizer,
	}
	if deps.ConfluenceReferenceFactory != nil {
		service.inverseConfluenceFactory = func() (ConfluencePageReferenceResolver, string) {
			resolver, status := deps.ConfluenceReferenceFactory()
			return resolver, string(status)
		}
	}
	return service
}

func (s *JiraService) confluenceGraphMetadataReader() (domain.ConfluenceGraphPageMetadataReader, string) {
	if s == nil {
		return nil, string(DependencyNotConfigured)
	}
	s.graphConfluenceOnce.Do(func() {
		if s.graphConfluence != nil || s.graphConfluenceFactory == nil {
			return
		}
		s.graphConfluence, s.graphConfluenceReason = s.graphConfluenceFactory()
	})
	if s.graphConfluence == nil && s.graphConfluenceReason == "" {
		return nil, string(DependencyNotConfigured)
	}
	return s.graphConfluence, s.graphConfluenceReason
}

// NewConfluenceRenderer builds the offline Confluence renderer without backend
// ports, credentials, or transport configuration.
func NewConfluenceRenderer(cfg *config.Config) *ConfluenceService {
	return NewConfluenceService(ConfluenceDependencies{Config: cfg})
}

// NewJiraRenderer builds the offline Jira renderer without backend ports,
// credentials, or transport configuration.
func NewJiraRenderer(cfg *config.Config) *JiraService {
	return NewJiraService(JiraDependencies{Config: cfg})
}

// NewCompatibilityService constructs compatibility qualification from an
// optional exact-metadata domain-port factory.
func NewCompatibilityService(settings compatibility.Settings, factory func() (domain.ExactServerMetadataReader, DependencySetupStatus)) *CompatibilityService {
	service := &CompatibilityService{settings: settings}
	if factory != nil {
		service.confluenceFactory = func() (domain.ExactServerMetadataReader, string) {
			reader, status := factory()
			return reader, string(status)
		}
	}
	return service
}

// EnvironmentService composes bounded metadata ports. Setup failures are kept
// as closed values so one missing backend cannot hide its configured sibling.
type EnvironmentService struct {
	cfg             *config.Config
	jiraTime        domain.JiraTimeSemanticsReader
	confluenceTime  domain.ConfluenceTimeSemanticsReader
	jiraSetup       string
	confluenceSetup string
}

// EnvironmentDependencies is the already-qualified optional backend projection.
type EnvironmentDependencies struct {
	Jira            domain.JiraTimeSemanticsReader
	JiraSetup       DependencySetupStatus
	Confluence      domain.ConfluenceTimeSemanticsReader
	ConfluenceSetup DependencySetupStatus
}

// NewEnvironmentService is a pure constructor from optional metadata ports.
func NewEnvironmentService(cfg *config.Config, deps EnvironmentDependencies) *EnvironmentService {
	return &EnvironmentService{
		cfg: cfg, jiraTime: deps.Jira, confluenceTime: deps.Confluence,
		jiraSetup: string(deps.JiraSetup), confluenceSetup: string(deps.ConfluenceSetup),
	}
}
