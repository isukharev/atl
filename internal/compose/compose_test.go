package compose

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func isolateCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	for _, key := range []string{"ATL_CONFLUENCE_PAT", "CONFLUENCE_PAT", "ATL_JIRA_PAT", "JIRA_PAT"} {
		t.Setenv(key, "")
	}
}

func TestServiceCompositionPreservesSetupSentinels(t *testing.T) {
	isolateCredentials(t)
	if _, err := NewConfluence(nil, "test"); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("Confluence missing URL error=%v", err)
	}
	if _, err := NewJira(nil, "test"); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("Jira missing URL error=%v", err)
	}
	if _, err := NewConfluenceScheduled(nil, "test", 0, 1); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("schedule precedence error=%v", err)
	}
	if _, err := NewConfluence(&config.Config{ConfluenceURL: "http://confluence.example.com"}, "test"); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("Confluence insecure URL error=%v", err)
	}
	if _, err := NewJira(&config.Config{JiraURL: "http://jira.example.com"}, "test"); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("Jira insecure URL error=%v", err)
	}
	if _, err := NewConfluence(&config.Config{ConfluenceURL: "https://confluence.example.com"}, "test"); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("Confluence missing token error=%v", err)
	}
	if _, err := NewJira(&config.Config{JiraURL: "https://jira.example.com"}, "test"); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("Jira missing token error=%v", err)
	}
}

func TestFocusedFeatureCompositionPreservesSetupSentinels(t *testing.T) {
	isolateCredentials(t)
	constructors := []struct {
		name        string
		missing     func() error
		insecure    func() error
		missingAuth func() error
	}{
		{
			name: "Confluence labels",
			missing: func() error {
				_, err := NewConfluenceLabelsWithWriteAuthorizer(nil, "test", nil)
				return err
			},
			insecure: func() error {
				_, err := NewConfluenceLabelsWithWriteAuthorizer(&config.Config{ConfluenceURL: "http://confluence.example.com"}, "test", nil)
				return err
			},
			missingAuth: func() error {
				_, err := NewConfluenceLabelsWithWriteAuthorizer(&config.Config{ConfluenceURL: "https://confluence.example.com"}, "test", nil)
				return err
			},
		},
		{
			name: "Jira watchers",
			missing: func() error {
				_, err := NewJiraWatchersWithWriteAuthorizer(nil, "test", nil)
				return err
			},
			insecure: func() error {
				_, err := NewJiraWatchersWithWriteAuthorizer(&config.Config{JiraURL: "http://jira.example.com"}, "test", nil)
				return err
			},
			missingAuth: func() error {
				_, err := NewJiraWatchersWithWriteAuthorizer(&config.Config{JiraURL: "https://jira.example.com"}, "test", nil)
				return err
			},
		},
		{
			name: "Jira worklogs",
			missing: func() error {
				_, err := NewJiraWorklogsWithWriteAuthorizer(nil, "test", nil)
				return err
			},
			insecure: func() error {
				_, err := NewJiraWorklogsWithWriteAuthorizer(&config.Config{JiraURL: "http://jira.example.com"}, "test", nil)
				return err
			},
			missingAuth: func() error {
				_, err := NewJiraWorklogsWithWriteAuthorizer(&config.Config{JiraURL: "https://jira.example.com"}, "test", nil)
				return err
			},
		},
	}
	for _, constructor := range constructors {
		t.Run(constructor.name, func(t *testing.T) {
			if err := constructor.missing(); !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("missing URL error=%v", err)
			}
			if err := constructor.insecure(); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("insecure URL error=%v", err)
			}
			if err := constructor.missingAuth(); !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("missing token error=%v", err)
			}
		})
	}
}

func TestOptionalCompositionKeepsClosedSiblingStatuses(t *testing.T) {
	isolateCredentials(t)
	cfg := &config.Config{
		JiraURL:       "https://jira.example.com",
		ConfluenceURL: "https://confluence.example.com",
	}
	environment := NewEnvironment(cfg, "test").InspectEnvironment(context.Background(), nil)
	if environment.Jira.Status != string(app.DependencyCredentialsMissing) || environment.Confluence.Status != string(app.DependencyCredentialsMissing) {
		t.Fatalf("environment statuses=%q/%q", environment.Jira.Status, environment.Confluence.Status)
	}
	settings := compatibility.Settings{SchemaVersion: compatibility.SettingsSchemaVersion, Confluence: &compatibility.Activation{
		ProviderID: compatibility.ConfluenceInlineCommentsDCProfileID, Version: "9.5.2", BuildNumber: "12345",
	}}
	status := NewCompatibility(cfg, settings, "test").Status(context.Background(), true)
	if status.Status != app.CompatibilityStatusUnavailable || status.Reason != string(app.DependencyCredentialsMissing) {
		t.Fatalf("compatibility status=%+v", status)
	}
}

func TestQualifiedConfluenceBaseURLAppliesTransportPolicy(t *testing.T) {
	t.Setenv("ATL_ALLOW_INSECURE", "")
	if got := qualifiedConfluenceBaseURL(&config.Config{ConfluenceURL: "http://confluence.example.com"}); got != "" {
		t.Fatalf("insecure Confluence base URL=%q", got)
	}
	if got := qualifiedConfluenceBaseURL(&config.Config{ConfluenceURL: "https://confluence.example.com/wiki"}); got != "https://confluence.example.com/wiki" {
		t.Fatalf("secure Confluence base URL=%q", got)
	}
	if got := qualifiedConfluenceBaseURL(&config.Config{ConfluenceURL: "http://127.0.0.1:8090/wiki"}); got != "http://127.0.0.1:8090/wiki" {
		t.Fatalf("loopback Confluence base URL=%q", got)
	}
	t.Setenv("ATL_ALLOW_INSECURE", "1")
	if got := qualifiedConfluenceBaseURL(&config.Config{ConfluenceURL: "http://confluence.example.com/wiki"}); got != "http://confluence.example.com/wiki" {
		t.Fatalf("trusted Confluence base URL=%q", got)
	}
}
