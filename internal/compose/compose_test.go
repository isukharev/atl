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
